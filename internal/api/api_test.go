package api

// api_test.go — transport-level tests for the Go build's HTTP layer.
// Everything runs against an httptest server with a temp data dir: no
// Plex server, no network. These tests pin three contracts the reused
// Electron renderer depends on:
//  1. the envelope ({ok:true,...} / {ok:false,error,hint}) on every route;
//  2. the session-token guard on /api/*;
//  3. ROUTE PARITY: every window.pcm method in web/bridge.js has a live
//     route (a missing route = a dead button = the reason for the rewrite).

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testToken = "test-session-token"

func newTestServer(t *testing.T) *httptest.Server {
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
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, url string) (int, []byte) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, body
}

func post(t *testing.T, url, token, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-PCM-Token", token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("POST %s: response not JSON: %s", url, raw)
	}
	return res.StatusCode, out
}

func TestHealthAndIndex(t *testing.T) {
	ts := newTestServer(t)
	status, body := get(t, ts.URL+"/api/health")
	if status != 200 || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("health: %d %s", status, body)
	}
	status, body = get(t, ts.URL+"/")
	if status != 200 || !strings.Contains(string(body), "bridge.js") || !strings.Contains(string(body), "/session.js") {
		t.Fatalf("index should load with session+bridge scripts: %d", status)
	}
	status, body = get(t, ts.URL+"/session.js")
	if status != 200 || !strings.Contains(string(body), testToken) {
		t.Fatalf("session.js should carry the token: %d %s", status, body)
	}
	for _, f := range []string{"bridge.js", "app.js", "advisor.js", "overlays.js", "composite.js", "collection-art.js", "collections-manager.js", "styles.css",
		"favicon.svg", "favicon.ico", "favicon-32.png", "apple-touch-icon.png", "icon-192.png", "icon-512.png"} {
		status, _ = get(t, ts.URL+"/"+f)
		if status != 200 {
			t.Fatalf("static %s: got %d", f, status)
		}
	}
}

func TestTokenGuard(t *testing.T) {
	ts := newTestServer(t)
	status, out := post(t, ts.URL+"/api/meta/status", "", `{}`)
	if status != 403 || out["ok"] != false {
		t.Fatalf("no token should be 403 envelope, got %d %v", status, out)
	}
	status, out = post(t, ts.URL+"/api/meta/status", "wrong-token", `{}`)
	if status != 403 || out["ok"] != false {
		t.Fatalf("wrong token should be 403 envelope, got %d %v", status, out)
	}
	status, out = post(t, ts.URL+"/api/meta/status", testToken, `{}`)
	if status != 200 || out["ok"] != true {
		t.Fatalf("good token should pass, got %d %v", status, out)
	}
}

func TestMetaStatusShapeAndSecretHygiene(t *testing.T) {
	ts := newTestServer(t)
	_, out := post(t, ts.URL+"/api/meta/status", testToken, `{}`)
	for _, k := range []string{"tmdb", "traktClientId", "traktClientSecret", "traktConnected", "omdb", "fanart", "ai", "aiBaseUrl", "aiModel", "storeBackend"} {
		if _, present := out[k]; !present {
			t.Fatalf("meta status missing key %q: %v", k, out)
		}
	}
	if out["tmdb"] != false {
		t.Fatalf("fresh store: tmdb should be false, got %v", out["tmdb"])
	}
	// Set a key, then confirm status flips and the VALUE never appears.
	_, out = post(t, ts.URL+"/api/meta/setKey", testToken, `{"which":"tmdb","value":"sekrit-key-123"}`)
	if out["ok"] != true || out["set"] != true {
		t.Fatalf("setKey: %v", out)
	}
	_, out = post(t, ts.URL+"/api/meta/status", testToken, `{}`)
	if out["tmdb"] != true {
		t.Fatalf("tmdb should be set now: %v", out)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "sekrit-key-123") {
		t.Fatal("secret value leaked into meta status response")
	}
	// Unknown slot is an honest error, not a silent no-op.
	_, out = post(t, ts.URL+"/api/meta/setKey", testToken, `{"which":"bogus","value":"x"}`)
	if out["ok"] != false || !strings.Contains(out["error"].(string), "Unknown key slot") {
		t.Fatalf("unknown slot should fail honestly: %v", out)
	}
}

func TestRequireServerEnvelope(t *testing.T) {
	ts := newTestServer(t)
	_, out := post(t, ts.URL+"/api/plex/getSections", testToken, `{}`)
	if out["ok"] != false || out["error"] != "No Plex server selected" {
		t.Fatalf("expected requireServer envelope, got %v", out)
	}
	if out["hint"] != "Pick a server on the Connect tab first." {
		t.Fatalf("hint should pass through, got %v", out["hint"])
	}
}

func TestAdvisorSettingsAndDraftsOffline(t *testing.T) {
	ts := newTestServer(t)
	_, out := post(t, ts.URL+"/api/advisor/getSettings", testToken, `{}`)
	if out["ok"] != true {
		t.Fatalf("getSettings: %v", out)
	}
	settings, _ := out["settings"].(map[string]any)
	if settings["genreThreshold"] != float64(8) {
		t.Fatalf("default genreThreshold should be 8: %v", settings)
	}
	_, out = post(t, ts.URL+"/api/advisor/saveDraft", testToken,
		`{"title":"Test draft","memberRatingKeys":["11","22"],"sectionKey":"1","type":"1","reason":"because"}`)
	if out["ok"] != true {
		t.Fatalf("saveDraft: %v", out)
	}
	_, out = post(t, ts.URL+"/api/advisor/getDrafts", testToken, `{}`)
	drafts, _ := out["drafts"].([]any)
	if len(drafts) != 1 {
		t.Fatalf("expected 1 draft: %v", out)
	}
	_, out = post(t, ts.URL+"/api/advisor/cacheStatus", testToken, `{}`)
	if out["ok"] != true {
		t.Fatalf("cacheStatus: %v", out)
	}
	if _, present := out["bytes"]; !present {
		t.Fatalf("cacheStatus should spread {bytes, files}: %v", out)
	}
}

func TestBadgeAndArtRoutes(t *testing.T) {
	ts := newTestServer(t)
	status, body := get(t, ts.URL+"/badges/bundle/star.png?t="+testToken)
	if status != 200 || len(body) < 100 {
		t.Fatalf("bundle badge: %d (%d bytes)", status, len(body))
	}
	if body[0] != 0x89 || body[1] != 'P' {
		t.Fatal("badge should be a PNG")
	}
	status, _ = get(t, ts.URL+"/badges/bundle/star.png") // no token
	if status != 403 {
		t.Fatalf("badge without token should be 403, got %d", status)
	}
	status, _ = get(t, ts.URL+"/badges/bundle/..%2Fsecret.png?t="+testToken)
	if status == 200 {
		t.Fatal("path traversal must not serve files")
	}
	// /art with no signed-in server → 404 unknown server (Electron parity).
	status, body = get(t, ts.URL+"/art?machine=nope&path=/library/metadata/1/thumb/1&w=300&t="+testToken)
	if status != 404 || !strings.Contains(string(body), "unknown server") {
		t.Fatalf("art unknown server: %d %s", status, body)
	}
}

func TestArtSuggestionsOffline(t *testing.T) {
	ts := newTestServer(t)
	_, out := post(t, ts.URL+"/api/art/suggestions", testToken, `{"title":"Star Wars"}`)
	if out["ok"] != true {
		t.Fatalf("suggestions: %v", out)
	}
	items, _ := out["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("catalog should know Star Wars: %v", out)
	}
	_, out = post(t, ts.URL+"/api/art/openTPDb", testToken, `{"title":"Star Wars"}`)
	if out["ok"] != true || !strings.Contains(out["url"].(string), "theposterdb.com") {
		t.Fatalf("openTPDb should return the TPDb search URL: %v", out)
	}
}

// TestRouteParity is the dead-button killer: every route the bridge can
// call must exist and answer with the envelope (never a bare 404).
func TestRouteParity(t *testing.T) {
	ts := newTestServer(t)
	routes := []string{
		"auth/startLogin", "auth/pollLogin", "auth/restoreSession", "auth/logout", "auth/selectServer",
		"plex/getSections", "plex/getCollections", "plex/getCollectionItems", "plex/getItem",
		"plex/searchLibrary", "plex/previewFilter", "plex/getSectionItemsPage", "plex/createCollection", "plex/updateCollection",
		"plex/setCollectionItems", "plex/deleteCollection", "plex/setPoster",
		"plex/collectionHealth", "plex/getCollectionDetail", "plex/updateCollectionMeta",
		"plex/collectionBulkArt", "plex/setCollectionPosterURL", "postergen/preview", "postergen/aiImage",
		"art/suggestions", "art/preview", "art/remember", "art/openTPDb",
		"advisor/scan", "advisor/getScan", "advisor/suggestions", "advisor/dismiss", "advisor/undismiss",
		"advisor/resetDismissals", "advisor/getDrafts", "advisor/saveDraft", "advisor/deleteDraft",
		"advisor/dubVerdict", "advisor/getSettings", "advisor/setSettings", "advisor/clearCache", "advisor/cacheStatus",
		"meta/status", "meta/setKey", "meta/setAiBaseUrl", "meta/setAiModel",
		"meta/traktOAuthStart", "meta/traktOAuthFinish", "meta/traktDisconnect",
		"ai/proposeRules", "ai/proposeFranchiseMerge", "ai/proposeRibbon", "ai/testConnection", "ai/suggestCollections",
		"overlay/badgePacks", "overlay/fanartLogos", "overlay/itemFacts", "overlay/apply",
		"overlay/restore", "overlay/history", "overlay/reapplyCheck",
		"overlay/templatesList", "overlay/templateSave", "overlay/templateLoad", "overlay/templateDelete",
		"leaving/list", "leaving/set", "leaving/remove",
		"theme/get", "theme/set",
	}
	for _, route := range routes {
		status, out := post(t, ts.URL+"/api/"+route, testToken, `{}`)
		if status == 404 {
			t.Fatalf("route /api/%s is NOT registered (dead button)", route)
		}
		if _, hasOK := out["ok"]; !hasOK {
			t.Fatalf("route /api/%s did not answer the envelope: status %d body %v", route, status, out)
		}
	}
}
