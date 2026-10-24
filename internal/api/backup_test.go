package api

// backup_test.go — Settings backup & restore (v1.22) end to end through
// the HTTP layer: export shape (keys excluded by default, machine-bound
// state never present), round-trip into a fresh data dir, key opt-in +
// import confirmation, and the rejection paths (malformed, foreign,
// machine-bound-only) leaving settings untouched.

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"plexcollectionmanager/internal/store"
)

// postRawBytes posts a body verbatim (for the malformed-JSON rejection)
// and decodes the JSON envelope.
func postRawBytes(t *testing.T, url, token, body string) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-PCM-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("non-JSON reply %q: %v", raw, err)
	}
	return out
}

func newBackupTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	root := filepath.Join("..", "..")
	webFS := os.DirFS(filepath.Join(root, "web"))
	badgesFS := os.DirFS(filepath.Join(root, "assets", "badges"))
	catalog, err := os.ReadFile(filepath.Join(root, "assets", "collection-art.json"))
	if err != nil {
		t.Fatal(err)
	}
	webSub, _ := fs.Sub(webFS, ".")
	dataDir := t.TempDir()
	srv, err := NewServer(Config{
		DataDir:      dataDir,
		Version:      "1.22.0-test",
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
	return srv, ts
}

func seedBackupStore(t *testing.T, srv *Server) {
	t.Helper()
	if err := srv.store.SetSettings(map[string]any{
		"aiBaseUrl":      "https://example.invalid/v1",
		"aiModel":        "test-model",
		"uiTheme":        "midnight",
		"donateUsername": "seed-user",
		// machine-bound: must never travel
		"defaultMachine": "machine-seeded",
		"clientId":       "client-seeded",
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.SetSecret("tmdb:api-key", "SECRET-TMDB-VALUE"); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.SetSecret("ai:api-key", "SECRET-AI-VALUE"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.advisorStore.SetSettings(map[string]any{"genreThreshold": 12}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.templates.Save("Seed Template", json.RawMessage(`[{"badgeId":"badge-4k","x":0.02,"y":0.02,"wFrac":0.22,"opacity":1,"rule":{"kind":"always"},"text":""}]`)); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.SetLeaving("machine-seeded", []store.LeavingItemRef{{RatingKey: "10", Title: "Alpha"}}, "2027-01-15"); err != nil {
		t.Fatal(err)
	}
}

func exportBackup(t *testing.T, ts *httptest.Server, includeKeys bool) map[string]any {
	t.Helper()
	_, out := post(t, ts.URL+"/api/settings/exportBackup", testToken, fmt.Sprintf(`{"includeKeys":%v}`, includeKeys))
	if out["ok"] != true {
		t.Fatalf("exportBackup: %v", out)
	}
	backup, ok := out["backup"].(map[string]any)
	if !ok {
		t.Fatalf("export has no backup object: %v", out)
	}
	return backup
}

func TestSettingsBackupRoundTrip(t *testing.T) {
	srv, ts := newBackupTestServer(t)
	seedBackupStore(t, srv)

	// Default export: keys excluded, machine-bound state absent.
	backup := exportBackup(t, ts, false)
	if backup["format"] != "projectionist-settings-backup" || backup["formatVersion"] != float64(1) {
		t.Fatalf("format marker = %v/%v", backup["format"], backup["formatVersion"])
	}
	if _, hasKeys := backup["keys"]; hasKeys {
		t.Fatalf("default export contains a keys section")
	}
	raw, _ := json.Marshal(backup)
	if strings.Contains(string(raw), "SECRET-TMDB-VALUE") || strings.Contains(string(raw), "SECRET-AI-VALUE") {
		t.Fatalf("default export leaks a secret value")
	}
	settings := backup["settings"].(map[string]any)
	if settings["aiBaseUrl"] != "https://example.invalid/v1" || settings["uiTheme"] != "midnight" || settings["donateUsername"] != "seed-user" {
		t.Fatalf("portable settings = %v", settings)
	}
	if _, ok := settings["defaultMachine"]; ok {
		t.Fatalf("export carries server selection")
	}
	if _, ok := settings["clientId"]; ok {
		t.Fatalf("export carries the install client id")
	}
	if len(backup["templates"].(map[string]any)) != 1 {
		t.Fatalf("templates = %v", backup["templates"])
	}
	leaving := backup["leaving"].(map[string]any)
	if len(leaving["machine-seeded"].(map[string]any)) != 1 {
		t.Fatalf("leaving = %v", leaving)
	}

	// Opt-in export: keys present.
	keyed := exportBackup(t, ts, true)
	keys := keyed["keys"].(map[string]any)
	if keys["tmdb"] != "SECRET-TMDB-VALUE" || keys["aiKey"] != "SECRET-AI-VALUE" {
		t.Fatalf("opt-in keys = %v", keys)
	}

	// Round-trip into a FRESH data dir.
	_, ts2 := newBackupTestServer(t)
	kb, _ := json.Marshal(keyed)
	_, imported := post(t, ts2.URL+"/api/settings/importBackup", testToken, fmt.Sprintf(`{"backup":%s,"confirmKeys":false}`, kb))
	if imported["ok"] != true {
		t.Fatalf("import: %v", imported)
	}
	if imported["keysSkipped"] != true || imported["keysImported"] != float64(0) {
		t.Fatalf("unconfirmed import must skip keys: %v", imported)
	}
	// Verify the landed values via a second export from the target.
	backup2 := exportBackup(t, ts2, false)
	s2 := backup2["settings"].(map[string]any)
	if s2["aiBaseUrl"] != "https://example.invalid/v1" || s2["uiTheme"] != "midnight" || s2["donateUsername"] != "seed-user" {
		t.Fatalf("imported settings = %v", s2)
	}
	if len(backup2["templates"].(map[string]any)) != 1 || len(backup2["leaving"].(map[string]any)["machine-seeded"].(map[string]any)) != 1 {
		t.Fatalf("imported templates/leaving = %v / %v", backup2["templates"], backup2["leaving"])
	}
	// Second export is content-equivalent for the portable fields.
	if fmt.Sprint(backup["settings"]) != fmt.Sprint(backup2["settings"]) {
		t.Fatalf("second export settings differ:\n%v\nvs\n%v", backup["settings"], backup2["settings"])
	}

	// Confirmed import of the key-bearing file lands the keys.
	_, ts3 := newBackupTestServer(t)
	_, imported3 := post(t, ts3.URL+"/api/settings/importBackup", testToken, fmt.Sprintf(`{"backup":%s,"confirmKeys":true}`, kb))
	if imported3["ok"] != true || imported3["keysImported"] != float64(2) {
		t.Fatalf("confirmed import: %v", imported3)
	}
	keyed3 := exportBackup(t, ts3, true)
	if keyed3["keys"].(map[string]any)["tmdb"] != "SECRET-TMDB-VALUE" {
		t.Fatalf("confirmed import did not land keys: %v", keyed3["keys"])
	}
}

func TestSettingsBackupRejections(t *testing.T) {
	srv, ts := newBackupTestServer(t)
	seedBackupStore(t, srv)
	before := srv.store.PortableSettings()

	// Malformed JSON body.
	bad := postRawBytes(t, ts.URL+"/api/settings/importBackup", testToken, `{not json`)
	if bad["ok"] != false {
		t.Fatalf("malformed body accepted: %v", bad)
	}
	// Foreign format marker.
	_, foreign := post(t, ts.URL+"/api/settings/importBackup", testToken, `{"backup":{"format":"something-else","formatVersion":1},"confirmKeys":false}`)
	if foreign["ok"] != false {
		t.Fatalf("foreign format accepted: %v", foreign)
	}
	// Wrong format version.
	_, wrongVer := post(t, ts.URL+"/api/settings/importBackup", testToken, `{"backup":{"format":"projectionist-settings-backup","formatVersion":99},"confirmKeys":false}`)
	if wrongVer["ok"] != false {
		t.Fatalf("wrong version accepted: %v", wrongVer)
	}
	// Machine-bound-only file: succeeds, drops both, changes nothing.
	_, mbOnly := post(t, ts.URL+"/api/settings/importBackup", testToken, `{"backup":{"format":"projectionist-settings-backup","formatVersion":1,"settings":{"defaultMachine":"other-machine","clientId":"other-client"}},"confirmKeys":false}`)
	if mbOnly["ok"] != true {
		t.Fatalf("machine-bound-only import failed: %v", mbOnly)
	}
	dropped, _ := mbOnly["dropped"].([]any)
	if len(dropped) != 2 {
		t.Fatalf("dropped = %v", mbOnly["dropped"])
	}
	if srv.store.SettingString("defaultMachine") != "machine-seeded" || srv.store.SettingString("clientId") != "client-seeded" {
		t.Fatalf("machine-bound values were overwritten by import")
	}
	// Bad leaving date rejects the whole import, settings untouched.
	_, badDate := post(t, ts.URL+"/api/settings/importBackup", testToken, `{"backup":{"format":"projectionist-settings-backup","formatVersion":1,"settings":{"uiTheme":"light"},"leaving":{"m":{"10":{"title":"X","date":"not-a-date"}}}},"confirmKeys":false}`)
	if badDate["ok"] != false {
		t.Fatalf("bad leaving date accepted: %v", badDate)
	}
	after := srv.store.PortableSettings()
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("settings changed across rejections:\n%v\nvs\n%v", before, after)
	}
}

func TestDonateUsernameValidation(t *testing.T) {
	srv, ts := newBackupTestServer(t)
	_ = srv
	_, bad := post(t, ts.URL+"/api/meta/setDonate", testToken, `{"username":"bad name!"}`)
	if bad["ok"] != false {
		t.Fatalf("bad username accepted: %v", bad)
	}
	_, bad2 := post(t, ts.URL+"/api/meta/setDonate", testToken, `{"username":"a/b"}`)
	if bad2["ok"] != false {
		t.Fatalf("slash username accepted: %v", bad2)
	}
	_, good := post(t, ts.URL+"/api/meta/setDonate", testToken, `{"username":"seed-user_1"}`)
	if good["ok"] != true {
		t.Fatalf("valid username rejected: %v", good)
	}
	_, meta := post(t, ts.URL+"/api/meta/status", testToken, `{}`)
	if meta["donateUsername"] != "seed-user_1" {
		t.Fatalf("meta donateUsername = %v", meta["donateUsername"])
	}
	_, cleared := post(t, ts.URL+"/api/meta/setDonate", testToken, `{"username":""}`)
	if cleared["ok"] != true {
		t.Fatalf("clear rejected: %v", cleared)
	}
	_, meta2 := post(t, ts.URL+"/api/meta/status", testToken, `{}`)
	if meta2["donateUsername"] != "" {
		t.Fatalf("donateUsername not cleared: %v", meta2["donateUsername"])
	}
	// Import rejects a hand-edited backup carrying an invalid username.
	_, badImport := post(t, ts.URL+"/api/settings/importBackup", testToken, `{"backup":{"format":"projectionist-settings-backup","formatVersion":1,"settings":{"donateUsername":"x y"}},"confirmKeys":false}`)
	if badImport["ok"] != false {
		t.Fatalf("import accepted a bad donate username: %v", badImport)
	}
}
