package api

// postergen_cache.go — render caches for the poster generator
// preview (v1.21.1).
//
// A preview re-renders on EVERY control change in the generator
// modal (debounced, but still a burst of renders per editing
// session). Before these caches, each render re-ran the whole
// fetch chain against the server: collection detail + the full
// member list + batch records + a fresh download of every needed
// member poster — brutal on a REMOTE server, where each render
// meant re-downloading the collection's posters over the internet.
// Two caches now sit in front of that chain:
//
//   - previewImageCache: decoded member poster images. The key
//     includes the item's thumb path, and a Plex thumb path ends
//     in the item's updatedAt — so when a poster changes, its
//     path changes and the stale entry is simply never hit again
//     (self-invalidating; the orphaned entry ages out via LRU).
//     Grid copies are cached at render size (downscaled), hero
//     backgrounds at full size; the two variants are separate
//     keys. Bounded by approximate decoded bytes with LRU
//     eviction, so a huge library cannot grow it without limit.
//
//   - previewMembersCache: the member-set resolution — collection
//     detail title + items list per collection, and batch records
//     per exact key set (collection mode and Builder-draft mode
//     alike) — under a short TTL. Thirty seconds is long enough
//     to absorb a whole burst of slider-driven renders and short
//     enough that membership edits show up promptly; poster
//     CHANGES never wait on this TTL because the changed item
//     carries a new thumb path (see above).
//
// Failures are never cached: a poster that fails to fetch is
// skipped + counted on that render exactly as before, and the
// next render retries it.

import (
	"container/list"
	"context"
	"image"
	"strings"
	"sync"
	"time"

	"plexcollectionmanager/internal/plex"
)

// previewImageCacheMaxBytes bounds the decoded-image cache by
// approximate pixel bytes (w*h*4). 128 MiB holds well over a
// hundred typical render-size posters — several full mosaic
// sessions — while keeping the worst case (all full-size hero
// backgrounds) firmly bounded.
const previewImageCacheMaxBytes = int64(128) << 20

// previewMembersTTL is how long a resolved member set (collection
// items / batch records) is reused across preview renders. A var,
// not a const, so tests can shrink it.
var previewMembersTTL = 30 * time.Second

type previewImageKey struct {
	machine string
	path    string
	full    bool // full-size (hero background) vs render-size grid copy
}

type previewImageEntry struct {
	key   previewImageKey
	img   image.Image
	bytes int64
}

type previewImageCache struct {
	mu    sync.Mutex
	ll    *list.List // front = most recently used
	index map[previewImageKey]*list.Element
	total int64
}

func newPreviewImageCache() *previewImageCache {
	return &previewImageCache{ll: list.New(), index: map[previewImageKey]*list.Element{}}
}

func previewImageBytes(img image.Image) int64 {
	b := img.Bounds()
	return int64(b.Dx()) * int64(b.Dy()) * 4
}

func (c *previewImageCache) get(key previewImageKey) (image.Image, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.index[key]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*previewImageEntry).img, true
}

func (c *previewImageCache) set(key previewImageKey, img image.Image) {
	size := previewImageBytes(img)
	if size > previewImageCacheMaxBytes {
		return // a single image bigger than the whole cache is not cached
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.index[key]; ok {
		old := el.Value.(*previewImageEntry)
		c.total -= old.bytes
		el.Value = &previewImageEntry{key: key, img: img, bytes: size}
		c.total += size
		c.ll.MoveToFront(el)
	} else {
		el := c.ll.PushFront(&previewImageEntry{key: key, img: img, bytes: size})
		c.index[key] = el
		c.total += size
	}
	for c.total > previewImageCacheMaxBytes && c.ll.Len() > 0 {
		back := c.ll.Back()
		if back == nil {
			break
		}
		ent := back.Value.(*previewImageEntry)
		delete(c.index, ent.key)
		c.total -= ent.bytes
		c.ll.Remove(back)
	}
}

// ---- member-set resolution cache --------------------------------

type previewCollectionKey struct {
	machine   string
	ratingKey string
}

type previewCollectionEntry struct {
	title   string
	items   []plex.Item
	expires time.Time
}

type previewBatchEntry struct {
	records map[string]map[string]any
	expires time.Time
}

type previewMembersCache struct {
	mu          sync.Mutex
	collections map[previewCollectionKey]*previewCollectionEntry
	batches     map[string]*previewBatchEntry
}

func newPreviewMembersCache() *previewMembersCache {
	return &previewMembersCache{
		collections: map[previewCollectionKey]*previewCollectionEntry{},
		batches:     map[string]*previewBatchEntry{},
	}
}

// previewCollection resolves a collection's detail title + member
// items, serving repeat renders within previewMembersTTL from the
// cache instead of re-walking detail + children on the server.
func (s *Server) previewCollection(ctx context.Context, server *plex.Server, ratingKey string) (string, []plex.Item, error) {
	key := previewCollectionKey{machine: server.Machine, ratingKey: ratingKey}
	now := time.Now()
	s.genMembers.mu.Lock()
	if ent, ok := s.genMembers.collections[key]; ok && now.Before(ent.expires) {
		title, items := ent.title, ent.items
		s.genMembers.mu.Unlock()
		return title, items, nil
	}
	s.genMembers.mu.Unlock()

	detail, err := s.plexClient.GetCollectionDetail(ctx, server, ratingKey)
	if err != nil {
		return "", nil, err
	}
	items, err := s.plexClient.GetCollectionItems(ctx, server, ratingKey)
	if err != nil {
		return "", nil, err
	}
	s.genMembers.mu.Lock()
	s.genMembers.collections[key] = &previewCollectionEntry{
		title: detail.Title, items: items, expires: time.Now().Add(previewMembersTTL),
	}
	s.genMembers.mu.Unlock()
	return detail.Title, items, nil
}

// previewRecords resolves batch metadata records (thumb/art paths,
// titles) for an exact member-key set, cached under
// previewMembersTTL. The returned map is shared with the cache —
// callers must treat it as read-only (the preview handler only
// reads from it).
func (s *Server) previewRecords(ctx context.Context, server *plex.Server, keys []string) (map[string]map[string]any, error) {
	cacheKey := server.Machine + "\x00" + strings.Join(keys, ",")
	now := time.Now()
	s.genMembers.mu.Lock()
	if ent, ok := s.genMembers.batches[cacheKey]; ok && now.Before(ent.expires) {
		records := ent.records
		s.genMembers.mu.Unlock()
		return records, nil
	}
	s.genMembers.mu.Unlock()

	batch, err := s.plexClient.GetItemsBatch(ctx, server, keys)
	if err != nil {
		return nil, err
	}
	records := map[string]map[string]any{}
	for _, m := range batch {
		if k, _ := m["ratingKey"].(string); k != "" {
			records[k] = m
		}
	}
	s.genMembers.mu.Lock()
	s.genMembers.batches[cacheKey] = &previewBatchEntry{records: records, expires: time.Now().Add(previewMembersTTL)}
	s.genMembers.mu.Unlock()
	return records, nil
}

// fetchPreviewImage is fetchMemberImage behind the image cache:
// full=false returns (and caches) the render-size grid copy,
// full=true the full-size image for hero/title-card backgrounds.
// A cache hit performs NO server round-trip. Failures return nil
// and are not cached.
func (s *Server) fetchPreviewImage(ctx context.Context, server *plex.Server, path string, full bool) image.Image {
	if path == "" {
		return nil
	}
	key := previewImageKey{machine: server.Machine, path: path, full: full}
	if img, ok := s.genImages.get(key); ok {
		return img
	}
	img := s.fetchMemberImage(ctx, server, path)
	if img == nil {
		return nil
	}
	if !full {
		img = downscaleTo(img, 512)
	}
	s.genImages.set(key, img)
	return img
}
