package api

// show_facts_test.go — show-level language aggregation, end to end
// through the REAL facts handler against a fake PMS on httptest
// (the owner's ask, 2026-10-22: a language badge on ALL his TV shows).
// The fake PMS mimics a REAL PMS (verified live on 1.43, 2026-10-22):
// the show record has no streams AND /allLeaves entries carry NO
// Media/Part/Stream either — streams exist only on the episode
// records, reached via the comma-batch fetch /library/metadata/{keys}.
// The handler must union those: audioLangs [eng jpn], dubClass dual,
// episodeCount set, langsFromEpisodes true. A show whose leaves fetch
// 404s must degrade to langsError WITHOUT losing the rest of its facts.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"plexcollectionmanager/internal/plex"
)

func fakePMS(t *testing.T) *httptest.Server {
	t.Helper()
	episode := func(rk string, streams string) string {
		return `{"ratingKey":"` + rk + `","type":"episode","title":"Ep ` + rk + `","Media":[{"Part":[{"Stream":[` + streams + `]}]}]}`
	}
	audio := func(code string) string { return `{"streamType":2,"languageCode":"` + code + `"}` }
	sub := func(code string) string { return `{"streamType":3,"languageCode":"` + code + `"}` }
	mux := http.NewServeMux()
	mux.HandleFunc("/library/metadata/900", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"900","type":"show","title":"Mixed Anime","leafCount":3,"viewedLeafCount":1,"Guid":[]}]}}`)
	})
	mux.HandleFunc("/library/metadata/900/allLeaves", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Real PMS shape: leaves carry identity ONLY — no Media, no
		// Stream. If the handler tried to aggregate from this payload
		// it would find nothing and this test would fail.
		keysOnly := []string{
			`{"ratingKey":"901","type":"episode","title":"Ep 901"}`,
			`{"ratingKey":"902","type":"episode","title":"Ep 902"}`,
			`{"ratingKey":"903","type":"episode","title":"Ep 903"}`,
		}
		io.WriteString(w, `{"MediaContainer":{"totalSize":3,"Metadata":[`+strings.Join(keysOnly, ",")+`]}}`)
	})
	// The batch record fetch — the ONLY place streams exist.
	mux.HandleFunc("/library/metadata/901,902,903", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		eps := []string{
			episode("901", audio("jpn")+","+sub("eng")),
			episode("902", audio("jpn")+","+audio("eng")),
			episode("903", audio("jpn")),
		}
		io.WriteString(w, `{"MediaContainer":{"size":3,"Metadata":[`+strings.Join(eps, ",")+`]}}`)
	})
	// Show 950: metadata fine, leaves endpoint broken.
	mux.HandleFunc("/library/metadata/950", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"950","type":"show","title":"Broken Leaves","leafCount":9,"viewedLeafCount":0,"Guid":[]}]}}`)
	})
	mux.HandleFunc("/library/metadata/950/allLeaves", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	})
	return httptest.NewServer(mux)
}

func appForFakePMS(t *testing.T, pmsURL string) *httptest.Server {
	t.Helper()
	root := filepath.Join("..", "..")
	catalog, err := os.ReadFile(filepath.Join(root, "assets", "collection-art.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(Config{
		DataDir:      t.TempDir(),
		Version:      "test",
		SessionToken: testToken,
		WebFS:        os.DirFS(filepath.Join(root, "web")),
		BadgesFS:     os.DirFS(filepath.Join(root, "assets", "badges")),
		CatalogJSON:  catalog,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.servers = []*plex.Server{{
		Name: "Fake", Machine: "fake-machine", Owned: true, Token: "dummy",
		Connections: []plex.Connection{{URI: pmsURL, Local: true}},
	}}
	srv.selectedMachine = "fake-machine"
	return httptest.NewServer(srv.Handler())
}

func postFacts(t *testing.T, app *httptest.Server, ratingKey string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest("POST", app.URL+"/api/overlay/itemFacts", bytes.NewReader([]byte(`{"ratingKey":"`+ratingKey+`"}`)))
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
		t.Fatalf("itemFacts(%s) failed: %s", ratingKey, env.Error)
	}
	return env.Facts
}

func TestShowFactsAggregateEpisodeLanguages(t *testing.T) {
	pms := fakePMS(t)
	defer pms.Close()
	app := appForFakePMS(t, pms.URL)
	defer app.Close()

	f := postFacts(t, app, "900")
	if f["type"] != "show" {
		t.Fatalf("type = %v, want show", f["type"])
	}
	if f["langsFromEpisodes"] != true {
		t.Errorf("langsFromEpisodes = %v, want true", f["langsFromEpisodes"])
	}
	langs, _ := f["audioLangs"].([]any)
	if len(langs) != 2 || langs[0] != "eng" || langs[1] != "jpn" {
		t.Errorf("audioLangs = %v, want [eng jpn] (union across episodes)", f["audioLangs"])
	}
	subs, _ := f["subtitleLangs"].([]any)
	if len(subs) != 1 || subs[0] != "eng" {
		t.Errorf("subtitleLangs = %v, want [eng]", f["subtitleLangs"])
	}
	if f["dubClass"] != "dual" {
		t.Errorf("dubClass = %v, want dual (jpn episodes + one eng track)", f["dubClass"])
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
	// For shows, audioTracks counts distinct audio languages.
	if f["audioTracks"] != float64(2) {
		t.Errorf("audioTracks = %v, want 2 (distinct union languages)", f["audioTracks"])
	}
}

func TestShowFactsLeavesFailureDegrades(t *testing.T) {
	pms := fakePMS(t)
	defer pms.Close()
	app := appForFakePMS(t, pms.URL)
	defer app.Close()

	f := postFacts(t, app, "950")
	if f["langsFromEpisodes"] != false {
		t.Errorf("langsFromEpisodes = %v, want false when leaves fail", f["langsFromEpisodes"])
	}
	if errStr, _ := f["langsError"].(string); errStr == "" {
		t.Errorf("langsError must name the failure, got %v", f["langsError"])
	}
	// The rest of the show's facts survive the degradation.
	if f["title"] != "Broken Leaves" || f["unwatchedCount"] == nil {
		t.Errorf("non-language facts must survive: title=%v unwatched=%v", f["title"], f["unwatchedCount"])
	}
	if f["dubClass"] != "unknown" {
		t.Errorf("dubClass = %v, want unknown (never guessed)", f["dubClass"])
	}
}
