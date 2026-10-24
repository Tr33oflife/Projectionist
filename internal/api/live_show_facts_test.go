package api

// Live-PMS show-facts test — SKIPPED unless PCM_LIVE_PLEX points at a
// throwaway Plex Media Server (the sandbox PMS) and PCM_LIVE_SHOW_KEY
// names a show fixture with language-tagged EPISODE files. Proves the
// show-level language aggregation against a REAL server: allLeaves on
// a real PMS carries NO stream elements (verified by hand 2026-10-22),
// so the facts must come from batch-fetching the episode records.
// Fixture (sandbox "Test Show", section 2): E01 eng+jpn audio, E02 jpn
// audio + eng sidecar subs, E03 eng audio → union audio [eng jpn],
// subs [eng], dubClass dual, episodeCount 3.
//
//	PCM_LIVE_PLEX=http://127.0.0.1:32400 PCM_LIVE_MACHINE=<id> \
//	  PCM_LIVE_SHOW_KEY=<show ratingKey> \
//	  go test ./internal/api/ -run LiveShowFacts -v

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"plexcollectionmanager/internal/plex"
)

func TestLiveShowFactsLanguages(t *testing.T) {
	base := os.Getenv("PCM_LIVE_PLEX")
	machine := os.Getenv("PCM_LIVE_MACHINE")
	showKey := os.Getenv("PCM_LIVE_SHOW_KEY")
	if base == "" || machine == "" || showKey == "" {
		t.Skip("PCM_LIVE_PLEX / PCM_LIVE_MACHINE / PCM_LIVE_SHOW_KEY not set — live show facts test skipped")
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
		Connections: []plex.Connection{{URI: base, Local: true}},
	}}
	srv.selectedMachine = machine
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/overlay/itemFacts", bytes.NewReader([]byte(`{"ratingKey":"`+showKey+`"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-PCM-Token", testToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var env struct {
		OK    bool           `json:"ok"`
		Error string         `json:"error"`
		Facts map[string]any `json:"facts"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode: %v — body: %s", err, raw)
	}
	if !env.OK {
		t.Fatalf("itemFacts(show) failed: %s", env.Error)
	}
	f := env.Facts
	t.Logf("show facts: type=%v audioLangs=%v subtitleLangs=%v dubClass=%v episodeCount=%v langsFromEpisodes=%v langsError=%v",
		f["type"], f["audioLangs"], f["subtitleLangs"], f["dubClass"], f["episodeCount"], f["langsFromEpisodes"], f["langsError"])
	if f["type"] != "show" {
		t.Fatalf("type = %v, want show", f["type"])
	}
	if f["langsFromEpisodes"] != true {
		t.Errorf("langsFromEpisodes = %v, want true (langsError=%v)", f["langsFromEpisodes"], f["langsError"])
	}
	langs, _ := f["audioLangs"].([]any)
	if len(langs) != 2 || langs[0] != "eng" || langs[1] != "jpn" {
		t.Errorf("audioLangs = %v, want [eng jpn] (union across the 3 real episode files)", f["audioLangs"])
	}
	subs, _ := f["subtitleLangs"].([]any)
	if len(subs) != 1 || subs[0] != "eng" {
		t.Errorf("subtitleLangs = %v, want [eng] (E02 sidecar)", f["subtitleLangs"])
	}
	if f["dubClass"] != "dual" {
		t.Errorf("dubClass = %v, want dual (eng+jpn in the union)", f["dubClass"])
	}
	if f["hasSubs"] != true {
		t.Errorf("hasSubs = %v, want true", f["hasSubs"])
	}
	if f["episodeCount"] != float64(3) || f["episodeTotal"] != float64(3) {
		t.Errorf("episodeCount/Total = %v/%v, want 3/3", f["episodeCount"], f["episodeTotal"])
	}
	if f["langsPartial"] != false {
		t.Errorf("langsPartial = %v, want false", f["langsPartial"])
	}
}
