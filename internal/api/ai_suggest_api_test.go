package api

// ai_suggest_api_test.go — v1.21: POST /api/ai/suggestCollections
// end to end against a fake Plex (one existing collection with
// members) and a fake OpenAI-shaped provider with scripted replies.
// The contract under test: verified cards only (hallucinated keys
// dropped + counted), existing-collection duplicates dropped,
// below-minimum suggestions suppressed, malformed replies are a
// plain error with no cards, dismissals persist across re-runs, and
// with no AI key configured the provider is NEVER called.

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"plexcollectionmanager/internal/advisor"
	"plexcollectionmanager/internal/plex"
)

// newAISuggestServer is newGenTestServer with the *Server kept, so
// tests can seed the Advisor scan store.
func newAISuggestServer(t *testing.T, pmsURL string) (*httptest.Server, *Server) {
	t.Helper()
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
		Connections:    []plex.Connection{{URI: pmsURL, Local: true}},
		BestConnection: plex.Connection{URI: pmsURL, Local: true},
	}
	srv.servers = []*plex.Server{fake}
	srv.selectedMachine = "fake-machine"
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, srv
}

// fakeSuggestPMS serves the two reads the endpoint makes: the
// section's collections (one, "Existing Shelf", rk 900) and that
// collection's members (keys 1..6).
func fakeSuggestPMS(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
	mux.HandleFunc("/library/sections/1/collections", func(w http.ResponseWriter, r *http.Request) {
		write(w, `{"MediaContainer":{"size":1,"totalSize":1,"Metadata":[{"ratingKey":"900","title":"Existing Shelf","type":"collection","childCount":6}]}}`)
	})
	mux.HandleFunc("/library/metadata/900", func(w http.ResponseWriter, r *http.Request) {
		write(w, `{"MediaContainer":{"size":1,"Metadata":[{"ratingKey":"900","title":"Existing Shelf","type":"collection"}]}}`)
	})
	mux.HandleFunc("/library/metadata/900/children", func(w http.ResponseWriter, r *http.Request) {
		var members []string
		for i := 1; i <= 6; i++ {
			members = append(members, fmt.Sprintf(`{"ratingKey":"%d","title":"Member %d","type":"movie"}`, i, i))
		}
		write(w, `{"MediaContainer":{"size":6,"totalSize":6,"Metadata":[`+strings.Join(members, ",")+`]}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// fakeSuggestAI answers /chat/completions with scripted content,
// counting hits so the no-key test can prove ZERO calls were made.
func fakeSuggestAI(t *testing.T, script func(call int) string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	hits := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(hits.Add(1))
		body, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": script(n)}}},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

// seedSuggestScan stores a 20-item movie scan (keys 1..20) for the
// fake machine.
func seedSuggestScan(t *testing.T, srv *Server) {
	t.Helper()
	items := make([]advisor.Item, 0, 20)
	for i := 1; i <= 20; i++ {
		y := 1990 + i
		items = append(items, advisor.Item{LibraryItem: plex.LibraryItem{
			RatingKey: fmt.Sprintf("%d", i), Title: fmt.Sprintf("Film %02d", i),
			Year: &y, Type: "movie", SectionKey: "1",
		}})
	}
	scan := &advisor.Scan{
		MachineID: "fake-machine",
		Sections:  []plex.Section{{Key: "1", Title: "Movies", Type: "movie"}},
		Items:     items,
		ItemCount: len(items),
	}
	if err := srv.advisorStore.SetScan("fake-machine", scan); err != nil {
		t.Fatal(err)
	}
}

func suggestCards(t *testing.T, out map[string]any) []map[string]any {
	t.Helper()
	raw, _ := json.Marshal(out["cards"])
	var cards []map[string]any
	if err := json.Unmarshal(raw, &cards); err != nil {
		t.Fatalf("cards decode: %v (%v)", err, out)
	}
	return cards
}

func TestAISuggestCollectionsEndToEnd(t *testing.T) {
	pms := fakeSuggestPMS(t)
	aiSrv, hits := fakeSuggestAI(t, func(call int) string {
		return `[
			{"title":"Clean Shelf","summary":"Six picks","rationale":"They rhyme.","memberRatingKeys":["7","8","9","10","11","12"]},
			{"title":"Partly Real","summary":"Five real, two invented","rationale":"Mood.","memberRatingKeys":["13","14","15","16","17","998","999"]},
			{"title":"Copy Of Existing","summary":"Same members as the existing shelf","rationale":"Dup.","memberRatingKeys":["1","2","3","4","5","6"]},
			{"title":"Tiny","summary":"Too few","rationale":"Small.","memberRatingKeys":["18","19"]}
		]`
	})
	ts, srv := newAISuggestServer(t, pms.URL)
	seedSuggestScan(t, srv)
	configureAI(t, ts, aiSrv.URL)
	if _, out := post(t, ts.URL+"/api/meta/setAiModel", testToken, `{"model":"fake-model-1"}`); out["ok"] != true {
		t.Fatalf("setAiModel: %v", out)
	}

	_, out := post(t, ts.URL+"/api/ai/suggestCollections", testToken, `{"sectionKey":"1"}`)
	if out["ok"] != true {
		t.Fatalf("suggestCollections: %v", out)
	}
	if hits.Load() != 1 {
		t.Fatalf("AI calls = %d, want 1 (20 items fit one batch)", hits.Load())
	}
	if out["calls"] != float64(1) || out["model"] != "fake-model-1" {
		t.Fatalf("calls/model = %v/%v", out["calls"], out["model"])
	}
	if out["suppressed"] != float64(2) {
		t.Fatalf("suppressed = %v, want 2 (existing duplicate + below-minimum)", out["suppressed"])
	}
	cards := suggestCards(t, out)
	if len(cards) != 2 {
		t.Fatalf("cards = %v, want exactly Clean Shelf + Partly Real", cards)
	}
	byTitle := map[string]map[string]any{}
	for _, c := range cards {
		byTitle[c["title"].(string)] = c
		if c["kind"] != "ai" || c["aiModel"] != "fake-model-1" {
			t.Fatalf("card provenance: %v", c)
		}
	}
	pr := byTitle["Partly Real"]
	if pr == nil {
		t.Fatalf("Partly Real missing: %v", byTitle)
	}
	if pr["aiSuggested"] != float64(7) || pr["aiVerified"] != float64(5) {
		t.Fatalf("Partly Real suggested/verified = %v/%v, want 7/5 (hallucinations dropped + counted)", pr["aiSuggested"], pr["aiVerified"])
	}
	members, _ := pr["memberRatingKeys"].([]any)
	if len(members) != 5 {
		t.Fatalf("Partly Real members = %v, want the 5 verified keys only", members)
	}
}

func TestAISuggestCollectionsNoKeyMakesZeroCalls(t *testing.T) {
	pms := fakeSuggestPMS(t)
	aiSrv, hits := fakeSuggestAI(t, func(call int) string { return `[]` })
	ts, srv := newAISuggestServer(t, pms.URL)
	seedSuggestScan(t, srv)
	// Point the (keyless) base URL at the fake: if the endpoint ever
	// invoked the client, the fake would be hit. It must not be.
	if _, out := post(t, ts.URL+"/api/meta/setAiBaseUrl", testToken,
		fmt.Sprintf(`{"url":%q}`, aiSrv.URL)); out["ok"] != true {
		t.Fatalf("setAiBaseUrl: %v", out)
	}
	_, out := post(t, ts.URL+"/api/ai/suggestCollections", testToken, `{"sectionKey":"1"}`)
	if out["ok"] != false {
		t.Fatalf("no key must fail, got %v", out)
	}
	if !strings.Contains(fmt.Sprint(out["error"]), "AI is not configured") {
		t.Fatalf("error = %v, want the not-configured guidance", out["error"])
	}
	if hits.Load() != 0 {
		t.Fatalf("AI provider hits = %d, want ZERO with no key configured", hits.Load())
	}
}

func TestAISuggestCollectionsMalformedReplyIsPlainError(t *testing.T) {
	pms := fakeSuggestPMS(t)
	aiSrv, _ := fakeSuggestAI(t, func(call int) string {
		return "Certainly! Here are some lovely collections for your library..."
	})
	ts, srv := newAISuggestServer(t, pms.URL)
	seedSuggestScan(t, srv)
	configureAI(t, ts, aiSrv.URL)

	_, out := post(t, ts.URL+"/api/ai/suggestCollections", testToken, `{"sectionKey":"1"}`)
	if out["ok"] != false {
		t.Fatalf("malformed reply must fail plainly, got %v", out)
	}
	if _, hasCards := out["cards"]; hasCards {
		t.Fatalf("malformed reply must produce NO cards field, got %v", out["cards"])
	}
	if !strings.Contains(fmt.Sprint(out["error"]), "could not be parsed") {
		t.Fatalf("error = %v, want the parse failure stated", out["error"])
	}
}

func TestAISuggestCollectionsDismissalPersistsAcrossRuns(t *testing.T) {
	pms := fakeSuggestPMS(t)
	aiSrv, _ := fakeSuggestAI(t, func(call int) string {
		return `[{"title":"Clean Shelf","summary":"Six picks","rationale":"They rhyme.","memberRatingKeys":["7","8","9","10","11","12"]}]`
	})
	ts, srv := newAISuggestServer(t, pms.URL)
	seedSuggestScan(t, srv)
	configureAI(t, ts, aiSrv.URL)

	_, out := post(t, ts.URL+"/api/ai/suggestCollections", testToken, `{"sectionKey":"1"}`)
	cards := suggestCards(t, out)
	if len(cards) != 1 {
		t.Fatalf("run 1 cards = %v", cards)
	}
	id, _ := cards[0]["id"].(string)
	if !strings.HasPrefix(id, "ai:1:") {
		t.Fatalf("card id = %q, want ai:1: prefix", id)
	}
	if _, out := post(t, ts.URL+"/api/advisor/dismiss", testToken,
		fmt.Sprintf(`{"cardId":%q}`, id)); out["ok"] != true {
		t.Fatalf("dismiss: %v", out)
	}
	_, out = post(t, ts.URL+"/api/ai/suggestCollections", testToken, `{"sectionKey":"1"}`)
	if out["ok"] != true {
		t.Fatalf("run 2: %v", out)
	}
	if cards := suggestCards(t, out); len(cards) != 0 {
		t.Fatalf("dismissed suggestion resurrected on re-run: %v", cards)
	}
}
