package api

// postergen_cache_test.go — v1.21.1 preview caching, proven by
// counting requests against the fake PMS: a repeat render inside
// the cache window must perform ZERO poster downloads and ZERO
// collection detail/items/batch round-trips, and a member whose
// thumb path changed (a new poster on the server) must re-fetch
// exactly that one poster once the metadata TTL lapses.
//
// Phase order matters: expiry is stamped at write time, so the
// TTL-lapse phase runs FIRST with a shrunken TTL, and the
// repeat-render phase re-settles the cache under the real TTL
// (one unmeasured render) before measuring.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type genCountingPMS struct {
	mu       sync.Mutex
	next     http.Handler
	detail   int
	children int
	batch    int
	thumb    int
}

func (c *genCountingPMS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	c.mu.Lock()
	switch {
	case strings.HasSuffix(path, "/children"):
		c.children++
	case strings.Contains(path, "/thumb/"):
		c.thumb++
	case strings.Contains(path, "/art/"):
		// background art fetches are not part of these assertions
	case strings.HasPrefix(path, "/library/metadata/"):
		rest := strings.TrimPrefix(path, "/library/metadata/")
		switch {
		case strings.Contains(rest, ","):
			c.batch++
		case rest == "900":
			c.detail++
		}
	}
	c.mu.Unlock()
	c.next.ServeHTTP(w, r)
}

func (c *genCountingPMS) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.detail, c.children, c.batch, c.thumb = 0, 0, 0, 0
}

func (c *genCountingPMS) counts() (detail, children, batch, thumb int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.detail, c.children, c.batch, c.thumb
}

func TestPosterGenPreviewCachesAcrossRenders(t *testing.T) {
	fake, _ := newGenFakePMS(t, 9)
	counter := &genCountingPMS{next: http.HandlerFunc(fake.handle)}
	pms := httptest.NewServer(counter)
	t.Cleanup(pms.Close)
	ts, _ := newGenTestServer(t, pms.URL)

	body := `{"ratingKey":"900","template":"mosaic","grid":"3x3","plate":true}`

	// ---- phase 1: cold render, then a changed poster ----------
	oldTTL := previewMembersTTL
	previewMembersTTL = 60 * time.Millisecond
	defer func() { previewMembersTTL = oldTTL }()

	// Cold render: the full chain runs — the collection record is
	// read twice by the existing chain (once by
	// GetCollectionDetail, once by GetCollectionItems' own
	// smart/manual probe) + children + one batch + one download
	// per member poster.
	genPreview(t, ts, body)
	d, ch, b, th := counter.counts()
	if d != 2 || ch != 1 || b != 1 || th != 9 {
		t.Fatalf("cold render requests: detail=%d children=%d batch=%d thumbs=%d, want 2/1/1/9", d, ch, b, th)
	}

	// A changed poster (new thumb path, as Plex issues when the
	// item updates) re-fetches exactly that poster once the
	// metadata TTL lapses; every other member still hits the
	// image cache at its unchanged path.
	time.Sleep(150 * time.Millisecond)
	changed := fake.members[3]
	fake.setThumbPath(changed.id, "/library/metadata/"+changed.id+"/thumb/2")
	counter.reset()
	out := genPreview(t, ts, body)
	if stats := out["stats"].(map[string]any); stats["usedMembers"] != float64(9) || stats["skippedPosters"] != float64(0) {
		t.Fatalf("post-change render stats = %v", stats)
	}
	d, ch, b, th = counter.counts()
	if d != 2 || ch != 1 || b != 1 {
		t.Fatalf("post-TTL render re-resolution: detail=%d children=%d batch=%d, want 2/1/1", d, ch, b)
	}
	if th != 1 {
		t.Fatalf("post-change thumb downloads = %d, want exactly 1 (the changed member)", th)
	}

	// ---- phase 2: repeat renders are fully cache-served --------
	previewMembersTTL = oldTTL
	time.Sleep(100 * time.Millisecond) // let the short-TTL entries lapse
	genPreview(t, ts, body)            // unmeasured: re-settles the cache under the real TTL
	counter.reset()
	out = genPreview(t, ts, body)
	if stats := out["stats"].(map[string]any); stats["usedMembers"] != float64(9) || stats["skippedPosters"] != float64(0) {
		t.Fatalf("cached render stats = %v", stats)
	}
	d, ch, b, th = counter.counts()
	if d != 0 || ch != 0 || b != 0 || th != 0 {
		t.Fatalf("repeat render requests: detail=%d children=%d batch=%d thumbs=%d, want all 0", d, ch, b, th)
	}

	// ---- phase 3: draft mode rides the same caches -------------
	// Its first call fetches the batch records for its (new) key
	// set, but the four posters themselves are already in the
	// image cache from the collection renders above — zero
	// downloads. The repeat draft render fetches nothing at all.
	counter.reset()
	keys := []string{}
	for _, m := range fake.members[:4] {
		keys = append(keys, m.id)
	}
	kb, _ := json.Marshal(keys)
	draftBody := fmt.Sprintf(`{"memberKeys":%s,"title":"Draft","template":"mosaic","grid":"2x2","plate":true}`, kb)
	genPreview(t, ts, draftBody)
	d, ch, b, th = counter.counts()
	if d != 0 || ch != 0 || b != 1 || th != 0 {
		t.Fatalf("first draft render requests: detail=%d children=%d batch=%d thumbs=%d, want 0/0/1/0", d, ch, b, th)
	}
	counter.reset()
	genPreview(t, ts, draftBody)
	d, ch, b, th = counter.counts()
	if d != 0 || ch != 0 || b != 0 || th != 0 {
		t.Fatalf("second draft render requests: detail=%d children=%d batch=%d thumbs=%d, want all 0", d, ch, b, th)
	}
}
