package api

// manager_api_test.go — v1.13 collections manager, end to end through
// the HTTP layer against a stateful fake PMS. The fake deliberately
// behaves like the real PMS 1.43 where it matters:
//   - the manage-hubs endpoint 404s (visibility unreportable);
//   - collection records carry NO Label key;
//   - label.* writes are accepted and silently not taken;
//   - titleSort only sticks in its .value form.
// The manager's contract is that all of that is SURFACED (verified
// re-reads, per-field checks, per-collection bulk results), never
// smoothed over.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"plexcollectionmanager/internal/collectionart"
	"plexcollectionmanager/internal/plex"
)

type fakeColl struct {
	title, titleSort, summary, contentRating string
	childCount                               int
	thumb, art                               string
	posterBytes                              []byte
}

type managerFakePMS struct {
	mu           sync.Mutex
	colls        map[string]*fakeColl
	ts           int
	failPosterID string
	defaultPNG   []byte
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newFakePMS(t *testing.T) (*managerFakePMS, *httptest.Server) {
	t.Helper()
	f := &managerFakePMS{colls: map[string]*fakeColl{}, ts: 1000, failPosterID: "102", defaultPNG: tinyPNG(t)}
	f.colls["100"] = &fakeColl{title: "Clean Picks", titleSort: "Clean Picks", summary: "Has one.", contentRating: "PG",
		childCount: 2, thumb: "/library/metadata/100/thumb/111", art: "/library/metadata/100/art/111"}
	f.colls["101"] = &fakeColl{title: "Bare Shelf", childCount: 0,
		thumb: "/library/collections/101/composite/111?width=400&height=600"}
	f.colls["102"] = &fakeColl{title: "Twin Shelf", titleSort: "Twin Shelf", summary: "Also has one.",
		childCount: 3, thumb: "/library/metadata/102/thumb/111", art: "/library/metadata/102/art/111"}
	f.colls["103"] = &fakeColl{title: "Twin Shelf", titleSort: "Twin Shelf", summary: "Third one.",
		childCount: 1, thumb: "/library/metadata/103/thumb/111", art: "/library/metadata/103/art/111"}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *managerFakePMS) record(id string, c *fakeColl) map[string]any {
	m := map[string]any{
		"ratingKey": id, "type": "collection", "title": c.title, "titleSort": c.titleSort,
		"summary": c.summary, "contentRating": c.contentRating, "childCount": c.childCount,
		"librarySectionID": 1, "thumb": c.thumb, "smart": 0,
	}
	if c.art != "" {
		m["art"] = c.art
	}
	return m
}

func (f *managerFakePMS) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	if strings.HasPrefix(path, "/hubs/") {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"MediaContainer":{}}`))
		return
	}
	if path == "/library/sections/1/collections" {
		meta := []any{}
		for _, id := range []string{"100", "101", "102", "103"} {
			meta = append(meta, f.record(id, f.colls[id]))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{"size": 4, "totalSize": 4, "Metadata": meta}})
		return
	}
	if strings.HasPrefix(path, "/library/metadata/") {
		rest := strings.TrimPrefix(path, "/library/metadata/")
		parts := strings.Split(rest, "/")
		id := parts[0]
		c, ok := f.colls[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch {
		case len(parts) == 1 && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{"Metadata": []any{f.record(id, c)}}})
		case len(parts) == 1 && r.Method == http.MethodPut:
			q := r.URL.Query()
			if v := q.Get("title"); q.Has("title") {
				c.title = v
			}
			if v := q.Get("titleSort.value"); q.Has("titleSort.value") {
				c.titleSort = v
			}
			if v := q.Get("summary"); q.Has("summary") {
				c.summary = v
			}
			if v := q.Get("contentRating.value"); q.Has("contentRating.value") {
				c.contentRating = v
			}
			// label.* deliberately ignored — PMS 1.43 takes no labels
			// on collections; the verified save must report it.
			_ = json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{}})
		case len(parts) == 3 && parts[1] == "thumb" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "image/png")
			if c.posterBytes != nil {
				_, _ = w.Write(c.posterBytes)
			} else {
				_, _ = w.Write(f.defaultPNG)
			}
		case len(parts) == 2 && parts[1] == "posters" && r.Method == http.MethodPost:
			if id == f.failPosterID {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("fake ingest failure"))
				return
			}
			body, _ := io.ReadAll(r.Body)
			c.posterBytes = extractMultipartFile(body)
			f.ts++
			c.thumb = fmt.Sprintf("/library/metadata/%s/thumb/%d", id, f.ts)
			w.WriteHeader(http.StatusOK)
		case len(parts) == 2 && parts[1] == "arts" && r.Method == http.MethodPost:
			f.ts++
			c.art = fmt.Sprintf("/library/metadata/%s/art/%d", id, f.ts)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
		return
	}
	if strings.HasPrefix(path, "/library/collections/") && strings.Contains(path, "/composite/") {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(f.defaultPNG)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

// extractMultipartFile pulls the file part's bytes out of a multipart
// body: the payload between the part headers' blank line and the
// closing boundary. Byte-exactness matters — the applier verifies the
// served poster against the exact bytes it uploaded.
func extractMultipartFile(body []byte) []byte {
	idx := bytes.Index(body, []byte("\r\n\r\n"))
	if idx < 0 {
		return body
	}
	rest := body[idx+4:]
	end := bytes.LastIndex(rest, []byte("\r\n--"))
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// newManagerTestServer builds the app server with the fake PMS
// selected and a fixture bulk-art source (a real PNG).
func newManagerTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	_, pms := newFakePMS(t)
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
	fake := &plex.Server{
		Name: "Fake", Machine: "fake-machine", Token: "t", Owned: true,
		Connections:    []plex.Connection{{URI: pms.URL, Local: true}},
		BestConnection: plex.Connection{URI: pms.URL, Local: true},
	}
	srv.servers = []*plex.Server{fake}
	srv.selectedMachine = "fake-machine"
	asset := tinyPNG(t)
	srv.bulkArtAsset = func(ctx context.Context, title string) (collectionart.Asset, error) {
		return collectionart.Asset{Bytes: asset, Source: "Fixture", URL: "https://example.test/poster.png"}, nil
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestManagerHealthEndpoint(t *testing.T) {
	ts := newManagerTestServer(t)
	_, out := post(t, ts.URL+"/api/plex/collectionHealth", testToken, `{"sectionKey":"1"}`)
	if out["ok"] != true {
		t.Fatalf("health: %v", out)
	}
	if out["total"] != float64(4) {
		t.Fatalf("total = %v, want 4", out["total"])
	}
	cols, _ := out["collections"].([]any)
	if len(cols) != 4 {
		t.Fatalf("collections = %d", len(cols))
	}
	flagsOf := map[string][]string{}
	for _, raw := range cols {
		c := raw.(map[string]any)
		fl, _ := c["flags"].([]any)
		for _, f := range fl {
			flagsOf[c["ratingKey"].(string)] = append(flagsOf[c["ratingKey"].(string)], f.(string))
		}
	}
	if len(flagsOf["100"]) != 0 {
		t.Errorf("clean collection flags = %v, want none", flagsOf["100"])
	}
	want101 := "[EMPTY NO POSTER NO BACKDROP NO SUMMARY]"
	if got := fmt.Sprint(flagsOf["101"]); got != want101 {
		t.Errorf("bare collection flags = %s, want %s", got, want101)
	}
	want102 := "[DUPLICATE TITLE]"
	if got := fmt.Sprint(flagsOf["102"]); got != want102 {
		t.Errorf("duplicate collection flags = %s, want %s", got, want102)
	}
	fc, _ := out["flagCounts"].(map[string]any)
	if fc["DUPLICATE TITLE"] != float64(2) || fc["EMPTY"] != float64(1) || fc["NO BACKDROP"] != float64(1) || fc["NO SUMMARY"] != float64(1) {
		t.Errorf("flagCounts = %v", fc)
	}
	// Visibility must read as NOT KNOWN (the fake's manage endpoint
	// 404s, like PMS 1.43) — never silently "library only".
	for _, raw := range cols {
		c := raw.(map[string]any)
		if c["visibilityKnown"] != false {
			t.Errorf("collection %v visibilityKnown = %v, want false", c["ratingKey"], c["visibilityKnown"])
		}
	}
}

func TestManagerDetailAndVerifiedSave(t *testing.T) {
	ts := newManagerTestServer(t)
	_, out := post(t, ts.URL+"/api/plex/getCollectionDetail", testToken, `{"ratingKey":"100"}`)
	if out["ok"] != true {
		t.Fatalf("detail: %v", out)
	}
	detail := out["detail"].(map[string]any)
	if detail["labels"] != nil {
		t.Errorf("labels = %v, want null (the server exposes no Label array — absent, never invented)", detail["labels"])
	}
	if detail["visibility"] != "unknown" || detail["visibilityKnown"] != false {
		t.Errorf("visibility = %v/%v, want unknown/false on this server", detail["visibility"], detail["visibilityKnown"])
	}
	_, out = post(t, ts.URL+"/api/plex/updateCollectionMeta", testToken,
		`{"ratingKey":"100","fields":{"title":"Renamed Picks","titleSort":"Renamed Sort","summary":"Fresh summary","contentRating":"PG-13","labels":["Alpha","Beta"]}}`)
	if out["ok"] != true {
		t.Fatalf("update: %v", out)
	}
	checks := map[string]map[string]any{}
	for _, raw := range out["checks"].([]any) {
		c := raw.(map[string]any)
		checks[c["field"].(string)] = c
	}
	for _, field := range []string{"title", "titleSort", "summary", "contentRating"} {
		if checks[field]["ok"] != true {
			t.Errorf("check %s = %v, want ok (held %v)", field, checks[field], checks[field]["held"])
		}
	}
	// The fake (like PMS 1.43) does not take labels: the verified save
	// MUST say so, with the field reported not-ok and a note.
	lab := checks["labels"]
	if lab == nil || lab["ok"] != false || lab["note"] == nil || lab["note"] == "" {
		t.Errorf("labels check = %v, want ok:false with an explanatory note", lab)
	}
	detail = out["detail"].(map[string]any)
	if detail["title"] != "Renamed Picks" || detail["titleSort"] != "Renamed Sort" || detail["contentRating"] != "PG-13" {
		t.Errorf("held detail after save = title %v sort %v rating %v", detail["title"], detail["titleSort"], detail["contentRating"])
	}
}

func TestManagerBulkArtPartialFailure(t *testing.T) {
	ts := newManagerTestServer(t)
	_, out := post(t, ts.URL+"/api/plex/collectionBulkArt", testToken,
		`{"ratingKeys":["100","102"],"poster":true,"backdrop":true}`)
	if out["ok"] != true {
		t.Fatalf("bulk: %v", out)
	}
	results, _ := out["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	byKey := map[string]map[string]any{}
	for _, raw := range results {
		r := raw.(map[string]any)
		byKey[r["ratingKey"].(string)] = r
	}
	good := byKey["100"]
	if good["poster"].(map[string]any)["ok"] != true || good["backdrop"].(map[string]any)["ok"] != true {
		t.Errorf("collection 100 result = %v, want poster+backdrop ok", good)
	}
	bad := byKey["102"]
	if bad["poster"].(map[string]any)["ok"] != false || bad["poster"].(map[string]any)["error"] == "" {
		t.Errorf("collection 102 poster = %v, want ok:false with a reason (its ingest fails)", bad["poster"])
	}
	if bad["backdrop"].(map[string]any)["ok"] != true {
		t.Errorf("collection 102 backdrop = %v, want ok (one target's failure must not sink the other)", bad["backdrop"])
	}
}
