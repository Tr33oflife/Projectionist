package api

import (
	"encoding/json"
	"testing"
)

// TestThemeRoundTrip pins the v1.19 theme store contract: a fresh
// install reports the default theme; built-in choices persist; a
// complete custom palette persists whole; rejected writes (unknown
// name, incomplete palette, custom-without-palette) change nothing.
func TestThemeRoundTrip(t *testing.T) {
	ts := newTestServer(t)
	postJSON := func(route string, body any) map[string]any {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		_, out := post(t, ts.URL+"/api/"+route, testToken, string(raw))
		return out
	}

	out := postJSON("theme/get", map[string]any{})
	if out["ok"] != true || out["name"] != "projectionist" || out["custom"] != nil {
		t.Fatalf("fresh theme/get = %v, want name=projectionist custom=nil", out)
	}

	out = postJSON("theme/set", map[string]any{"name": "midnight"})
	if out["ok"] != true {
		t.Fatalf("set midnight = %v", out)
	}
	out = postJSON("theme/get", map[string]any{})
	if out["name"] != "midnight" {
		t.Fatalf("after set, theme/get name = %v, want midnight", out["name"])
	}

	// A complete palette built from the canonical var list.
	palette := map[string]any{}
	for _, v := range themeVars {
		palette[v] = "#112233"
	}
	palette["--bg-1"] = "#0b0d16"
	out = postJSON("theme/set", map[string]any{"name": "custom", "custom": palette})
	if out["ok"] != true {
		t.Fatalf("set custom (complete) = %v", out)
	}
	out = postJSON("theme/get", map[string]any{})
	if out["name"] != "custom" {
		t.Fatalf("after custom set, name = %v", out["name"])
	}
	gotCustom, _ := out["custom"].(map[string]any)
	if len(gotCustom) != len(themeVars) || gotCustom["--bg-1"] != "#0b0d16" {
		t.Fatalf("stored custom palette = %d vars, --bg-1 %v", len(gotCustom), gotCustom["--bg-1"])
	}

	// Incomplete palette: rejected, and the stored state is untouched.
	delete(palette, "--amber")
	out = postJSON("theme/set", map[string]any{"name": "custom", "custom": palette})
	if out["ok"] == true {
		t.Fatalf("incomplete palette was accepted: %v", out)
	}
	out = postJSON("theme/get", map[string]any{})
	if out["name"] != "custom" {
		t.Fatalf("rejected write changed state: name = %v", out["name"])
	}
	if gc, _ := out["custom"].(map[string]any); len(gc) != len(themeVars) {
		t.Fatalf("rejected write changed the stored palette: %d vars", len(gc))
	}

	// Non-color value and unknown name: rejected.
	palette["--amber"] = "not-a-color"
	out = postJSON("theme/set", map[string]any{"name": "custom", "custom": palette})
	if out["ok"] == true {
		t.Fatalf("non-color palette value was accepted: %v", out)
	}
	out = postJSON("theme/set", map[string]any{"name": "bogus"})
	if out["ok"] == true {
		t.Fatalf("unknown theme name was accepted: %v", out)
	}

	// Custom-without-palette on a fresh store: rejected (nothing saved).
	ts2 := newTestServer(t)
	raw, _ := json.Marshal(map[string]any{"name": "custom"})
	_, out2 := post(t, ts2.URL+"/api/theme/set", testToken, string(raw))
	if out2["ok"] == true {
		t.Fatalf("custom without any stored palette was accepted: %v", out2)
	}
}
