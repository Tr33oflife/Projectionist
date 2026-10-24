package api

// art.go — artwork serving for the Go build.
//
// /art replaces Electron's pleximg:// protocol handler, ported
// behavior-for-behavior from registerArtProtocol() in main.js:
//   - the renderer asks for ?w=<px>; we translate that to Plex's own
//     /photo/:/transcode so the SERVER sends a small JPEG instead of the
//     full poster (w is clamped 0..1200; w=0/absent = original bytes,
//     which the overlay designer needs at full resolution);
//   - connections are walked in ranked order (8s per attempt), the
//     winner is remembered per server, and HTTP errors fall through to
//     the next connection just like reads do;
//   - a 400-entry in-memory LRU (keyed machine|w|path) serves repeat
//     views without touching the network; only bodies < 4MB are cached;
//     poster-changing operations evict the item's path (all sizes) via
//     invalidateArt, because a thumb path outlives the bytes behind it;
//   - the Plex token travels in the X-Plex-Token HEADER — never in a
//     URL the renderer could leak into a log.
//
// /badges/{bundle|custom}/<file> replaces the overlayimg:// protocol:
// bundle = embedded assets/badges, custom = <dataDir>/badges (the owner's
// own PNGs). Badge files are name-validated like the Electron handler
// listed them; no path traversal is possible (single path segment,
// strict regex).

import (
	"container/list"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"plexcollectionmanager/internal/plex"
)

var artPathRe = regexp.MustCompile(`^/[a-zA-Z0-9/_.\\-]+$`)

// ---- LRU -----------------------------------------------------------------------

type artCacheEntry struct {
	key  string
	body []byte
	typ  string
}

// artLRU is a tiny bounded LRU (the JS used a Map with delete/re-set
// touch semantics and oldest-first eviction — same here).
type artLRU struct {
	max   int
	ll    *list.List
	index map[string]*list.Element
}

func newArtLRU(max int) *artLRU {
	return &artLRU{max: max, ll: list.New(), index: map[string]*list.Element{}}
}

func (c *artLRU) get(key string) (*artCacheEntry, bool) {
	el, ok := c.index[key]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*artCacheEntry), true
}

func (c *artLRU) set(key string, body []byte, typ string) {
	if el, ok := c.index[key]; ok {
		el.Value = &artCacheEntry{key: key, body: body, typ: typ}
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&artCacheEntry{key: key, body: body, typ: typ})
	c.index[key] = el
	for c.ll.Len() > c.max {
		back := c.ll.Back()
		if back == nil {
			break
		}
		delete(c.index, back.Value.(*artCacheEntry).key)
		c.ll.Remove(back)
	}
}

// deletePath evicts every cached size of ONE artwork path on ONE
// server (keys are "machine|w|path"). Poster writes change the bytes
// behind a thumb path that itself never changes, so without this a
// cached pre-write image would be served at that key indefinitely.
// Returns the number of entries evicted.
func (c *artLRU) deletePath(machine, path string) int {
	prefix := machine + "|"
	n := 0
	for key, el := range c.index {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		rest := key[len(prefix):]
		i := strings.Index(rest, "|")
		if i < 0 || rest[i+1:] != path {
			continue
		}
		c.ll.Remove(el)
		delete(c.index, key)
		n++
	}
	return n
}

// invalidateArt drops every cached size of one item's artwork. Called
// by the poster-changing paths (overlay apply, run restore,
// restore-defaults) once an item's write completes, so the app's own
// grids and stage re-fetch the new poster instead of serving the
// pre-write bytes from the LRU. Best-effort by design: the write has
// already succeeded, and a stale grid is a display issue, not a data
// one — but leaving the hook uncalled anywhere a poster changes is a
// bug.
func (s *Server) invalidateArt(machine, path string) {
	if machine == "" || path == "" {
		return
	}
	if i := strings.Index(path, "?"); i >= 0 {
		path = path[:i] // same normalisation handleArt applies
	}
	s.artMu.Lock()
	s.artCache.deletePath(machine, path)
	s.artMu.Unlock()
}

// ---- /art ------------------------------------------------------------------------

func (s *Server) handleArt(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	machine := q.Get("machine")
	rawPath := q.Get("path")

	s.mu.Lock()
	var server *plex.Server
	for _, srv := range s.servers {
		if srv.Machine == machine {
			server = srv
			break
		}
	}
	s.mu.Unlock()
	if server == nil {
		http.Error(w, "unknown server", http.StatusNotFound)
		return
	}
	// Plex serves AUTO-COMPOSITE collection thumbs as a path WITH a
	// query string ("/library/collections/4/composite/1792…?width=400
	// &height=600"). The size hint is meaningless to us — /art does its
	// own sizing — so only the path portion is validated and fetched.
	// Before this split, EVERY composite-thumb collection failed here
	// with 400 (regex rejects '?', '=', '&'): the owner's "half the
	// collection posters dont load" (2026-10-22) — custom-poster
	// collections have plain /library/metadata thumbs and loaded fine.
	if i := strings.Index(rawPath, "?"); i >= 0 {
		rawPath = rawPath[:i]
	}
	if !artPathRe.MatchString(rawPath) {
		http.Error(w, "bad artwork path", http.StatusBadRequest)
		return
	}
	wpx := 0
	if v, err := strconv.Atoi(q.Get("w")); err == nil {
		wpx = v
	}
	if wpx < 0 {
		wpx = 0
	}
	if wpx > 1200 {
		wpx = 1200
	}
	cacheKey := fmt.Sprintf("%s|%d|%s", machine, wpx, rawPath)

	s.artMu.Lock()
	hit, found := s.artCache.get(cacheKey)
	s.artMu.Unlock()
	if found {
		writeArt(w, hit.body, hit.typ)
		return
	}

	plexPath := rawPath
	if wpx > 0 {
		plexPath = fmt.Sprintf("/photo/:/transcode?width=%d&height=%d&minSize=1&upscale=0&url=%s",
			wpx, (wpx*3+1)/2, url.QueryEscape(rawPath))
	}

	conns := plex.RankConnections(server.Connections)
	if len(conns) == 0 {
		conns = []plex.Connection{server.BestConnection}
	}
	s.artMu.Lock()
	good := s.goodArtConn[machine]
	s.artMu.Unlock()
	if good != "" {
		for i, c := range conns {
			if c.URI == good && i > 0 {
				conns = append([]plex.Connection{c}, append(conns[:i:i], conns[i+1:]...)...)
				break
			}
		}
	}

	client := &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // JS: redirect:'error' — never follow
		},
	}
	lastStatus := http.StatusBadGateway
	for _, conn := range conns {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
			strings.TrimSuffix(conn.URI, "/")+plexPath, nil)
		if err != nil {
			continue
		}
		req.Header.Set("X-Plex-Token", server.Token)
		req.Header.Set("Accept", "image/*")
		res, err := client.Do(req)
		if err != nil {
			lastStatus = http.StatusBadGateway
			continue
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			lastStatus = res.StatusCode
			res.Body.Close()
			continue
		}
		body, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
		res.Body.Close()
		if err != nil {
			lastStatus = http.StatusBadGateway
			continue
		}
		s.artMu.Lock()
		s.goodArtConn[machine] = conn.URI
		s.artMu.Unlock()
		typ := res.Header.Get("Content-Type")
		if i := strings.Index(typ, ";"); i >= 0 {
			typ = typ[:i]
		}
		if typ == "" {
			typ = "image/jpeg"
		}
		if len(body) > 0 && len(body) < 4*1024*1024 {
			s.artMu.Lock()
			s.artCache.set(cacheKey, body, typ)
			s.artMu.Unlock()
		}
		writeArt(w, body, typ)
		return
	}
	http.Error(w, "artwork unavailable", lastStatus)
}

func writeArt(w http.ResponseWriter, body []byte, typ string) {
	w.Header().Set("Content-Type", typ)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(body)
}

// ---- /badges/* ----------------------------------------------------------------------

var badgeFileRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*\.png$`)

func (s *Server) handleBadgeFile(pack string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		file := r.PathValue("file")
		if !badgeFileRe.MatchString(strings.ToLower(file)) || strings.Contains(file, "..") {
			http.Error(w, "bad badge file", http.StatusBadRequest)
			return
		}
		var data []byte
		var err error
		if pack == "bundle" {
			data, err = fs.ReadFile(s.cfg.BadgesFS, file)
		} else {
			data, err = readCustomBadge(s.customBadgeDir(), file)
		}
		if err != nil {
			http.Error(w, "badge not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "private, max-age=3600")
		_, _ = w.Write(data)
	}
}

func readCustomBadge(dir, file string) ([]byte, error) {
	// file is a single validated segment (no slashes possible); join +
	// verify containment anyway as defense in depth.
	full := filepath.Join(dir, file)
	if filepath.Dir(full) != filepath.Clean(dir) {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(full)
}

// ---- art import (upload) ------------------------------------------------------------

// handleArtImportImage is the Go replacement for Electron's
// dialog-based art:import: bridge.js collects the file through an
// <input type=file> and uploads it here as multipart form data. The
// bytes go through the exact same validation as a file import
// (collectionart.ImportBytes) and the response shape is identical.
func (s *Server) handleArtImportImage(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		failErr(w, err)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		failErr(w, err)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (12<<20)+1))
	if err != nil {
		failErr(w, err)
		return
	}
	art, err := s.art.ImportBytes(header.Filename, data)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"art": art})
}
