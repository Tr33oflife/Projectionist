package api

// donate_toggle_api_test.go — the "Show donate button" toggle (v1.23
// rider 1): the toggle persists in the settings store, defaults ON
// when nothing is stored, is left alone by a save that omits it, and
// rides settings export like any other portable preference.

import (
	"net/http/httptest"
	"testing"
)

func donateStatus(t *testing.T, ts *httptest.Server) map[string]any {
	t.Helper()
	_, out := post(t, ts.URL+"/api/meta/status", testToken, `{}`)
	if out["ok"] != true {
		t.Fatalf("metaStatus: %v", out)
	}
	return out
}

func TestDonateTogglePersistsAndDefaultsOn(t *testing.T) {
	_, pms := newGenFakePMS(t, 1)
	ts, _ := newGenTestServer(t, pms.URL)

	// Nothing stored yet: the toggle reads ON (v1.22 behavior).
	if got := donateStatus(t, ts)["donateEnabled"]; got != true {
		t.Fatalf("default donateEnabled = %v, want true", got)
	}
	// Username-only save leaves the toggle alone.
	_, out := post(t, ts.URL+"/api/meta/setDonate", testToken, `{"username":"toggle-user"}`)
	if out["ok"] != true || out["username"] != "toggle-user" {
		t.Fatalf("setDonate username-only: %v", out)
	}
	if got := donateStatus(t, ts)["donateEnabled"]; got != true {
		t.Fatalf("username-only save moved donateEnabled to %v, want true", got)
	}
	// Toggle OFF persists and the username survives alongside it.
	_, out = post(t, ts.URL+"/api/meta/setDonate", testToken, `{"username":"toggle-user","enabled":false}`)
	if out["ok"] != true || out["enabled"] != false {
		t.Fatalf("setDonate enabled=false: %v", out)
	}
	st := donateStatus(t, ts)
	if st["donateEnabled"] != false || st["donateUsername"] != "toggle-user" {
		t.Fatalf("status after toggle-off = %v", st)
	}
	// A username-only save while OFF leaves OFF alone (absence is
	// not a reset).
	_, out = post(t, ts.URL+"/api/meta/setDonate", testToken, `{"username":"toggle-user2"}`)
	if out["ok"] != true {
		t.Fatalf("second username-only save: %v", out)
	}
	if got := donateStatus(t, ts)["donateEnabled"]; got != false {
		t.Fatalf("username-only save reset donateEnabled to %v, want false", got)
	}
	// Toggle back ON, and the export carries the toggle (portable
	// preference, like the username).
	_, out = post(t, ts.URL+"/api/meta/setDonate", testToken, `{"username":"toggle-user2","enabled":true}`)
	if out["ok"] != true || out["enabled"] != true {
		t.Fatalf("setDonate enabled=true: %v", out)
	}
	_, exp := post(t, ts.URL+"/api/settings/exportBackup", testToken, `{"includeKeys":false}`)
	backup, _ := exp["backup"].(map[string]any)
	settings, _ := backup["settings"].(map[string]any)
	if settings["donateEnabled"] != true || settings["donateUsername"] != "toggle-user2" {
		t.Fatalf("exported settings = %v, want donateEnabled=true + donateUsername=toggle-user2", settings)
	}
}
