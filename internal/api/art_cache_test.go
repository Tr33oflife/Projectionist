package api

// Art-cache invalidation tests. The /art LRU is keyed machine|w|path
// and a thumb path never changes when the poster behind it does, so
// without eviction the app serves the pre-write poster forever.
// These tests pin: (1) deletePath evicts exactly one machine+path
// across all sizes; (2) end to end through the real /art handler —
// bytes flip on the "server", the cache keeps serving the old ones,
// and invalidateArt (the hook every poster-changing handler calls)
// makes the next fetch serve the new poster.

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"plexcollectionmanager/internal/plex"
)

func TestArtLRUDeletePath(t *testing.T) {
	c := newArtLRU(10)
	c.set("m1|0|/library/metadata/7/thumb/1", []byte("a0"), "image/jpeg")
	c.set("m1|300|/library/metadata/7/thumb/1", []byte("a300"), "image/jpeg")
	c.set("m1|0|/library/metadata/8/thumb/1", []byte("b0"), "image/jpeg")
	c.set("m2|0|/library/metadata/7/thumb/1", []byte("c0"), "image/jpeg")
	if n := c.deletePath("m1", "/library/metadata/7/thumb/1"); n != 2 {
		t.Fatalf("deletePath evicted %d entries, want 2 (both sizes)", n)
	}
	if _, ok := c.get("m1|0|/library/metadata/7/thumb/1"); ok {
		t.Fatal("w=0 entry survived eviction")
	}
	if _, ok := c.get("m1|300|/library/metadata/7/thumb/1"); ok {
		t.Fatal("w=300 entry survived eviction")
	}
	if _, ok := c.get("m1|0|/library/metadata/8/thumb/1"); !ok {
		t.Fatal("a different item's art was evicted")
	}
	if _, ok := c.get("m2|0|/library/metadata/7/thumb/1"); !ok {
		t.Fatal("a different server's art was evicted")
	}
	if n := c.deletePath("m1", "/library/metadata/7/thumb/1"); n != 0 {
		t.Fatalf("second deletePath evicted %d, want 0", n)
	}
}

func TestArtCacheInvalidationServesNewPoster(t *testing.T) {
	// Fake PMS whose poster bytes can flip mid-test (a poster write
	// happening server-side).
	var cur atomic.Value
	cur.Store([]byte("POSTER-A"))
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(cur.Load().([]byte))
	}))
	defer fake.Close()

	root := filepath.Join("..", "..")
	catalog, err := os.ReadFile(filepath.Join(root, "assets", "collection-art.json"))
	if err != nil {
		t.Fatal(err)
	}
	webSub, _ := fs.Sub(os.DirFS(filepath.Join(root, "web")), ".")
	srv, err := NewServer(Config{
		DataDir:      t.TempDir(),
		Version:      "test",
		SessionToken: testToken,
		WebFS:        webSub,
		BadgesFS:     os.DirFS(filepath.Join(root, "assets", "badges")),
		CatalogJSON:  catalog,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.servers = []*plex.Server{{
		Name: "Fake", Machine: "m1", Owned: true, Token: "tok",
		Connections:    []plex.Connection{{URI: fake.URL, Local: true}},
		BestConnection: plex.Connection{URI: fake.URL, Local: true},
	}}
	srv.selectedMachine = "m1"
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	thumb := "/library/metadata/7/thumb/1"
	fetch := func(wpx string) string {
		u := ts.URL + "/art?machine=m1&path=" + url.QueryEscape(thumb) + "&w=" + wpx + "&t=" + testToken
		_, body := get(t, u)
		return string(body)
	}

	if got := fetch("0"); got != "POSTER-A" {
		t.Fatalf("first fetch = %q, want POSTER-A", got)
	}
	if got := fetch("300"); got != "POSTER-A" {
		t.Fatalf("first sized fetch = %q, want POSTER-A", got)
	}
	// The poster changes on the server; the cache must keep serving
	// the old bytes (that is the staleness being pinned) …
	cur.Store([]byte("POSTER-B"))
	if got := fetch("0"); got != "POSTER-A" {
		t.Fatalf("cached fetch after server-side change = %q, want stale POSTER-A", got)
	}
	// … until the poster-change hook evicts the path.
	srv.invalidateArt("m1", thumb)
	if got := fetch("0"); got != "POSTER-B" {
		t.Fatalf("fetch after invalidation = %q, want POSTER-B", got)
	}
	if got := fetch("300"); got != "POSTER-B" {
		t.Fatalf("sized fetch after invalidation = %q, want POSTER-B (all sizes evicted)", got)
	}
	// A path that was never cached is a no-op, not an error.
	srv.invalidateArt("m1", "/library/metadata/99/thumb/9")
	srv.invalidateArt("", thumb)
	srv.invalidateArt("m1", "")
	if !strings.Contains(fetch("0"), "POSTER") {
		t.Fatal("art route broke after no-op invalidations")
	}
}
