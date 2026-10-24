package api

// import_api_test.go — v1.14 list import preview, end to end
// through the HTTP layer: a fake PMS section (items carrying Guid
// elements) + injected list fetches (the Server override points the
// bulk-art tests established). The preview must perform NO writes —
// the fake PMS records every request, and any POST/PUT to it fails
// the test.

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"plexcollectionmanager/internal/plex"
)

type importFakePMS struct {
	mu     sync.Mutex
	writes []string
}

func (f *importFakePMS) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		f.mu.Lock()
		f.writes = append(f.writes, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/library/sections/1/all":
		_, _ = w.Write([]byte(`{"MediaContainer":{"size":8,"Metadata":[
			{"ratingKey":11,"type":"movie","title":"Fight Club","year":1999,"Guid":[{"id":"tmdb://550"},{"id":"imdb://tt0137523"}]},
			{"ratingKey":12,"type":"movie","title":"The Matrix","year":1999,"Guid":[{"id":"imdb://tt0133093"}]},
			{"ratingKey":13,"type":"movie","title":"Amelie","year":2001},
			{"ratingKey":14,"type":"movie","title":"Duplicate Movie","year":2010},
			{"ratingKey":15,"type":"movie","title":"Duplicate Movie","year":2010},
			{"ratingKey":16,"type":"movie","title":"Solo Title","year":2005},
			{"ratingKey":17,"type":"movie","title":"Twin No Year","year":2001},
			{"ratingKey":18,"type":"movie","title":"Twin No Year","year":2002}
		]}}`))
	case r.URL.Path == "/library/sections/1/collections":
		_, _ = w.Write([]byte(`{"MediaContainer":{"size":1,"Metadata":[
			{"ratingKey":90,"type":"collection","title":"Weekend Picks","childCount":2,"smart":0,"thumb":"/library/metadata/90/thumb/1","summary":"s"}
		]}}`))
	case strings.HasPrefix(r.URL.Path, "/hubs/"):
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"MediaContainer":{}}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newImportTestServer(t *testing.T) (*Server, *httptest.Server, *importFakePMS) {
	t.Helper()
	fake := &importFakePMS{}
	pms := httptest.NewServer(http.HandlerFunc(fake.handle))
	t.Cleanup(pms.Close)
	root := filepath.Join("..", "..")
	webFS := os.DirFS(filepath.Join(root, "web"))
	badgesFS := os.DirFS(filepath.Join(root, "assets", "badges"))
	catalog, err := os.ReadFile(filepath.Join(root, "assets", "collection-art.json"))
	if err != nil {
		t.Fatal(err)
	}
	webSub, _ := fs.Sub(webFS, ".")
	srv, err := NewServer(Config{
		DataDir:      t.TempDir(),
		Version:      "1.0.0-test",
		SessionToken: testToken,
		WebFS:        webSub,
		BadgesFS:     badgesFS,
		CatalogJSON:  catalog,
	})
	if err != nil {
		t.Fatal(err)
	}
	fakeSrv := &plex.Server{
		Name: "Fake", Machine: "fake-machine", Token: "t", Owned: true,
		Connections:    []plex.Connection{{URI: pms.URL, Local: true}},
		BestConnection: plex.Connection{URI: pms.URL, Local: true},
	}
	srv.servers = []*plex.Server{fakeSrv}
	srv.selectedMachine = "fake-machine"
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts, fake
}

func yearPtr(y int) *int { return &y }

func TestImportPreviewListMatching(t *testing.T) {
	srv, ts, fake := newImportTestServer(t)
	srv.fetchTMDbList = func(ctx context.Context, ref string) (fetchedList, error) {
		if ref != "12345" {
			t.Errorf("fetch ref = %q, want passthrough of the posted ref", ref)
		}
		return fetchedList{
			Title: "Weekend Picks", ItemCount: 9,
			Entries: []importEntry{
				{Title: "Fight Club", Year: yearPtr(1999), MediaType: "movie", TMDBID: "550"},       // tmdb id hit
				{Title: "The Matrix", Year: yearPtr(1999), MediaType: "movie", IMDBID: "tt0133093"}, // imdb id hit
				{Title: "The Amelie", Year: yearPtr(2001), MediaType: "movie"},                      // title+year (article dropped)
				{Title: "Duplicate Movie", Year: yearPtr(2010), MediaType: "movie"},                 // ambiguous
				{Title: "Ghost Movie", Year: yearPtr(2020), MediaType: "movie"},                     // not in library
				{Title: "Some Show", Year: yearPtr(2020), MediaType: "tv"},                          // wrong kind for a movie library
				{Title: "Solo Title", MediaType: "movie"},                                           // no-year unique title hit
				{Title: "Twin No Year", MediaType: "movie"},                                         // no-year ambiguous
				{Title: "Fight Club", Year: yearPtr(1999), MediaType: "movie", TMDBID: "550"},       // duplicate of #1
			},
		}, nil
	}
	_, out := post(t, ts.URL+"/api/import/previewList", testToken, `{"source":"tmdb","listRef":"12345","sectionKey":"1","type":1}`)
	if out["ok"] != true {
		t.Fatalf("preview: %v", out)
	}
	if out["matchedCount"] != float64(4) || out["unmatchedCount"] != float64(4) || out["duplicateCount"] != float64(1) || out["totalEntries"] != float64(9) {
		t.Errorf("counts = matched %v unmatched %v dup %v total %v, want 4/4/1/9",
			out["matchedCount"], out["unmatchedCount"], out["duplicateCount"], out["totalEntries"])
	}
	if out["titleCollision"] != true {
		t.Errorf("titleCollision = %v, want true (the fake library holds a 'Weekend Picks')", out["titleCollision"])
	}
	if out["authUsed"] != false {
		t.Errorf("authUsed = %v, want false for the TMDb fetch", out["authUsed"])
	}
	// The write set, in list order.
	members, _ := out["members"].([]any)
	gotKeys := []string{}
	for _, m := range members {
		gotKeys = append(gotKeys, m.(map[string]any)["ratingKey"].(string))
	}
	if fmt.Sprint(gotKeys) != "[11 12 13 16]" {
		t.Errorf("members (write set, list order) = %v, want [11 12 13 16]", gotKeys)
	}
	// Per-entry verdicts.
	entries, _ := out["entries"].([]any)
	if len(entries) != 9 {
		t.Fatalf("entries = %d, want 9", len(entries))
	}
	wantStatus := []string{"matched", "matched", "matched", "unmatched", "unmatched", "unmatched", "matched", "unmatched", "duplicate"}
	wantVia := []string{"tmdb", "imdb", "title+year", "", "", "", "title", "", ""}
	for i, raw := range entries {
		e := raw.(map[string]any)
		if e["status"] != wantStatus[i] {
			t.Errorf("entry %d (%v) status = %v, want %v", i, e["title"], e["status"], wantStatus[i])
		}
		if wantVia[i] != "" && e["via"] != wantVia[i] {
			t.Errorf("entry %d via = %v, want %v", i, e["via"], wantVia[i])
		}
		if wantStatus[i] != "matched" {
			if reason, _ := e["reason"].(string); reason == "" {
				t.Errorf("entry %d (%v) has no reason: %v", i, e["title"], e)
			}
		}
	}
	reasonOf := func(i int) string { r, _ := entries[i].(map[string]any)["reason"].(string); return r }
	if !strings.Contains(reasonOf(3), "ambiguous") {
		t.Errorf("entry 3 reason = %q, want ambiguous", reasonOf(3))
	}
	if reasonOf(4) != "not in library" {
		t.Errorf("entry 4 reason = %q, want 'not in library'", reasonOf(4))
	}
	if !strings.Contains(reasonOf(5), "show") {
		t.Errorf("entry 5 reason = %q, want the wrong-kind reason", reasonOf(5))
	}
	if !strings.Contains(reasonOf(7), "ambiguous") {
		t.Errorf("entry 7 reason = %q, want ambiguous (no year)", reasonOf(7))
	}
	// The match set the dry-run shows must equal the members set:
	// count matched entries and compare against len(members).
	matched := 0
	for _, raw := range entries {
		if raw.(map[string]any)["status"] == "matched" {
			matched++
		}
	}
	if matched != len(members) {
		t.Errorf("matched entries %d != members %d — dry-run counts must equal the write set", matched, len(members))
	}
	// NO writes reached the fake PMS.
	if len(fake.writes) != 0 {
		t.Errorf("preview performed writes: %v", fake.writes)
	}
}

func TestImportPreviewListTraktAuthAndValidation(t *testing.T) {
	srv, ts, _ := newImportTestServer(t)
	srv.fetchTraktList = func(ctx context.Context, ref string) (fetchedList, error) {
		return fetchedList{
			Title: "Private Picks", ItemCount: 1, AuthUsed: true,
			Entries: []importEntry{{Title: "Solo Title", MediaType: "movie"}},
		}, nil
	}
	_, out := post(t, ts.URL+"/api/import/previewList", testToken, `{"source":"trakt","listRef":"someuser/private","sectionKey":"1","type":1}`)
	if out["ok"] != true {
		t.Fatalf("trakt preview: %v", out)
	}
	if out["authUsed"] != true {
		t.Errorf("authUsed = %v, want true (list read with the connected account)", out["authUsed"])
	}
	if out["matchedCount"] != float64(1) || out["titleCollision"] != false {
		t.Errorf("trakt preview counts = %v collision = %v, want 1 matched, no collision", out["matchedCount"], out["titleCollision"])
	}
	// Validation: unknown source and missing type fail as envelopes.
	_, bad := post(t, ts.URL+"/api/import/previewList", testToken, `{"source":"bogus","listRef":"x","sectionKey":"1","type":1}`)
	if bad["ok"] != false {
		t.Errorf("bogus source = %v, want ok:false", bad)
	}
	_, bad2 := post(t, ts.URL+"/api/import/previewList", testToken, `{"source":"tmdb","listRef":"x","sectionKey":"1"}`)
	if bad2["ok"] != false {
		t.Errorf("missing type = %v, want ok:false", bad2)
	}
	// No keys configured and no override: an honest configuration error.
	srv2, ts2, _ := newImportTestServer(t)
	_ = srv2
	_, noKey := post(t, ts2.URL+"/api/import/previewList", testToken, `{"source":"tmdb","listRef":"12345","sectionKey":"1","type":1}`)
	if noKey["ok"] != false || !strings.Contains(fmt.Sprint(noKey["error"]), "TMDb") {
		t.Errorf("no-key preview = %v, want an honest TMDb configuration error", noKey)
	}
	raw, _ := json.Marshal(noKey)
	_ = raw
}
