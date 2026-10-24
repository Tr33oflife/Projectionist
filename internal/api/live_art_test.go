package api

// Live-PMS artwork E2E — SKIPPED unless PCM_LIVE_PLEX points at the
// throwaway sandbox PMS. Built 2026-10-22 after the owner's live report:
// "half the collection posters dont load" (Collections tab AND overlay
// picker share the /art route). Plex serves AUTO-COMPOSITE collection
// thumbs as paths WITH a query string, e.g.
//   /library/collections/4/composite/1792708507?width=400&height=600
// while custom-poster collections get plain /library/metadata/{id}/
// thumb/{ts} paths. This test walks EVERY collection + item thumb the
// app's own handlers return and fetches each through the app's REAL
// /art handler at grid width (300) and full size (0), logging status
// per thumb so a shape-specific failure cannot hide.
//
//	PCM_LIVE_PLEX=http://127.0.0.1:32400 PCM_LIVE_MACHINE=<id> \
//	  go test ./internal/api/ -run LiveArt -v

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"plexcollectionmanager/internal/plex"
)

func liveAppServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	base := os.Getenv("PCM_LIVE_PLEX")
	machine := os.Getenv("PCM_LIVE_MACHINE")
	if base == "" || machine == "" {
		t.Skip("PCM_LIVE_PLEX / PCM_LIVE_MACHINE not set — live art test skipped")
	}
	root := filepath.Join("..", "..")
	catalog, err := os.ReadFile(filepath.Join(root, "assets", "collection-art.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(Config{
		DataDir:      t.TempDir(),
		Version:      "live-test",
		SessionToken: testToken,
		WebFS:        os.DirFS(filepath.Join(root, "web")),
		BadgesFS:     os.DirFS(filepath.Join(root, "assets", "badges")),
		CatalogJSON:  catalog,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.servers = []*plex.Server{{
		Name: "LiveTest", Machine: machine, Owned: true, Token: "livetest-dummy-token",
		Connections:    []plex.Connection{{URI: base, Local: true}},
		BestConnection: plex.Connection{URI: base, Local: true}, // discovery sets this in the real app; the overlay applier reads it
	}}
	srv.selectedMachine = machine
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, machine
}

func rpc(t *testing.T, ts *httptest.Server, route string, body any, out any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", ts.URL+"/api/"+route, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-PCM-Token", testToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("%s: decode %v — %s", route, err, b)
	}
}

func TestLiveArtAllThumbs(t *testing.T) {
	ts, machine := liveAppServer(t)

	var colsEnv struct {
		OK          bool   `json:"ok"`
		Error       string `json:"error"`
		Collections []struct {
			RatingKey string `json:"ratingKey"`
			Title     string `json:"title"`
			Thumb     string `json:"thumb"`
		} `json:"collections"`
	}
	rpc(t, ts, "plex/getCollections", map[string]any{"sectionKey": "1"}, &colsEnv)
	if !colsEnv.OK {
		t.Fatalf("getCollections: %s", colsEnv.Error)
	}
	var itemsEnv struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		Items []struct {
			RatingKey string `json:"ratingKey"`
			Title     string `json:"title"`
			Thumb     string `json:"thumb"`
		} `json:"items"`
	}
	rpc(t, ts, "plex/previewFilter", map[string]any{"sectionKey": "1", "filterQuery": "", "type": nil}, &itemsEnv)
	if !itemsEnv.OK {
		t.Fatalf("previewFilter: %s", itemsEnv.Error)
	}
	t.Logf("collections: %d, items: %d", len(colsEnv.Collections), len(itemsEnv.Items))
	if len(itemsEnv.Items) == 0 {
		t.Errorf("previewFilter returned ZERO items — the overlay picker would show collections only")
	}

	type thumbRef struct{ kind, title, thumb string }
	var thumbs []thumbRef
	for _, c := range colsEnv.Collections {
		thumbs = append(thumbs, thumbRef{"collection", c.Title, c.Thumb})
	}
	for _, it := range itemsEnv.Items {
		thumbs = append(thumbs, thumbRef{"item", it.Title, it.Thumb})
	}
	failures := 0
	for _, th := range thumbs {
		for _, w := range []int{300, 0} {
			u := fmt.Sprintf("%s/art?machine=%s&path=%s&w=%d&t=%s",
				ts.URL, url.QueryEscape(machine), url.QueryEscape(th.thumb), w, testToken)
			res, err := http.Get(u)
			if err != nil {
				t.Errorf("%s %q w=%d: %v", th.kind, th.title, w, err)
				failures++
				continue
			}
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			t.Logf("%s %-22q w=%-3d -> HTTP %d (%d bytes, %s)", th.kind, th.title, w, res.StatusCode, len(body), res.Header.Get("Content-Type"))
			if res.StatusCode != 200 || len(body) == 0 {
				failures++
			}
		}
	}
	if failures > 0 {
		t.Errorf("%d artwork fetches failed", failures)
	}
}
