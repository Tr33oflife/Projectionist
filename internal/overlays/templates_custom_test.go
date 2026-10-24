package overlays

// templates_custom_test.go — v1.24 rule-based custom badges:
// sanitizer behaviour + save/load round-trip through the template
// store. A bad badge must never survive into a stored template, and
// a good one must survive byte-for-byte through save AND load.

import (
	"encoding/json"
	"testing"
)

func TestSanitizeCustomBadge(t *testing.T) {
	// A full, valid badge passes with every field intact.
	good, ok := SanitizeCustomBadge(map[string]any{
		"id": "custom-horror", "name": "Horror shelf", "text": "HORROR",
		"color": "#f87171", "corner": "bl", "variant": "aurora",
		"rule": map[string]any{"genre": "Horror", "ratingMin": 8.0, "bogus": "dropped"},
	})
	if !ok {
		t.Fatal("valid custom badge must sanitize")
	}
	if good["text"] != "HORROR" || good["color"] != "#f87171" || good["variant"] != "aurora" {
		t.Errorf("sanitized badge wrong: %v", good)
	}
	rule, _ := good["rule"].(map[string]any)
	if rule["genre"] != "Horror" || rule["ratingMin"] != 8.0 {
		t.Errorf("rule wrong: %v", rule)
	}
	if _, present := rule["bogus"]; present {
		t.Error("unknown rule field must be dropped")
	}

	// Empty text or name drops the badge outright.
	if _, ok := SanitizeCustomBadge(map[string]any{"name": "X", "text": "  "}); ok {
		t.Error("empty text must drop the badge")
	}
	if _, ok := SanitizeCustomBadge(map[string]any{"name": "", "text": "X"}); ok {
		t.Error("empty name must drop the badge")
	}

	// Bad color / corner / variant fall back, never refuse the badge.
	fb, ok := SanitizeCustomBadge(map[string]any{
		"name": "Fallback", "text": "FB", "color": "#not-a-color",
		"corner": "middle", "variant": "neon",
	})
	if !ok {
		t.Fatal("badge with bad presentation must fall back, not drop")
	}
	if fb["color"] != "#e2e8f0" || fb["corner"] != "bl" || fb["variant"] != "signature" {
		t.Errorf("fallbacks wrong: %v", fb)
	}

	// Bad watchStatus is dropped from the rule, not stored verbatim.
	ws, _ := SanitizeCustomBadge(map[string]any{
		"name": "W", "text": "W", "rule": map[string]any{"watchStatus": "sometimes"},
	})
	if _, present := ws["rule"].(map[string]any)["watchStatus"]; present {
		t.Error("unknown watchStatus must be dropped from the rule")
	}
}

func TestTemplateStoreCustomBadgesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewTemplateStore(dir)
	customs := json.RawMessage(`[
		{"id": "custom-horror", "name": "Horror shelf", "text": "HORROR", "color": "#f87171", "corner": "bl", "variant": "signature",
		 "rule": {"genre": "Horror", "yearMin": 1980, "yearMax": 1989}},
		{"id": "custom-horror", "name": "Duplicate id", "text": "DUP", "color": "#f87171", "corner": "bl", "rule": {}},
		{"name": "", "text": "NOPE", "rule": {}}
	]`)
	saved, err := store.SaveLayoutCustom("Custom Night", json.RawMessage(`[]`),
		json.RawMessage(`{"resolution": {"on": true}}`), customs)
	if err != nil {
		t.Fatal(err)
	}
	// Duplicate ids dedupe, empty-name badges drop: exactly one survives.
	if saved["customBadges"] != 1 {
		t.Fatalf("save customBadges = %v, want 1", saved["customBadges"])
	}
	loaded, err := store.Load("Custom Night")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := loaded["customBadges"].([]map[string]any)
	if len(got) != 1 {
		t.Fatalf("loaded customBadges = %v, want 1", loaded["customBadges"])
	}
	if got[0]["id"] != "custom-horror" || got[0]["text"] != "HORROR" {
		t.Errorf("loaded badge wrong: %v", got[0])
	}
	rule, _ := got[0]["rule"].(map[string]any)
	if rule["genre"] != "Horror" || rule["yearMin"] != 1980.0 || rule["yearMax"] != 1989.0 {
		t.Errorf("loaded rule wrong: %v", rule)
	}
	// List reports the count so the template browser can show it.
	list := store.List()
	if len(list) != 1 || list[0]["customBadges"] != 1 {
		t.Errorf("list customBadges wrong: %v", list)
	}
	// A pure-custom template (no families, no placements) is a valid
	// template, like a pure family layout.
	if _, err := store.SaveLayoutCustom("Custom Only", json.RawMessage(`[]`), nil, customs); err != nil {
		t.Errorf("pure custom template must save: %v", err)
	}
	// Import validation accepts the customBadges array shape.
	raw, _ := json.Marshal(map[string]any{
		"name": "Imported", "placements": []any{},
		"customBadges": []any{map[string]any{"name": "I", "text": "I"}},
	})
	if _, err := ValidateTemplateRaw(raw); err != nil {
		t.Errorf("import validation must accept customBadges: %v", err)
	}
	bad, _ := json.Marshal(map[string]any{
		"name": "Bad", "placements": []any{}, "customBadges": map[string]any{"nope": true},
	})
	if _, err := ValidateTemplateRaw(bad); err == nil {
		t.Error("import validation must reject a non-array customBadges")
	}
}
