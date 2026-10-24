package overlays

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"My Cool Template!":   "my-cool-template",
		"":                    "template",
		"!!!":                 "template",
		"---Already--Slug---": "already-slug",
		"Ratings & 4K":        "ratings-4k",
		"../../etc/passwd":    "etc-passwd",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
	long := Slugify(strings.Repeat("a", 100))
	if len(long) != 60 {
		t.Errorf("slugify must cap at 60 chars, got %d", len(long))
	}
}

func TestSanitizePlacement(t *testing.T) {
	// fanart placements are excluded (JS behavior).
	if _, ok := SanitizePlacement(map[string]any{"badgeId": "fanart-logo-1", "x": 0.1}); ok {
		t.Error("fanart placement must be dropped")
	}
	// Missing / empty badgeId dropped.
	if _, ok := SanitizePlacement(map[string]any{"x": 0.1}); ok {
		t.Error("placement without badgeId must be dropped")
	}
	if _, ok := SanitizePlacement(nil); ok {
		t.Error("nil placement must be dropped")
	}
	// Clamping + defaults + rule filtering.
	c, ok := SanitizePlacement(map[string]any{
		"badgeId": "badge-4k", "x": 5.0, "y": -1.0, "wFrac": 0.3, "opacity": 0.0,
		"rule": map[string]any{"kind": "tmdb-rating", "value": 7.5, "bogus": "drop-me"},
		"text": "hello",
	})
	if !ok {
		t.Fatal("valid placement must sanitize")
	}
	if c["x"] != 1.0 || c["y"] != 0.0 {
		t.Errorf("x/y must clamp to [0,1]: %v %v", c["x"], c["y"])
	}
	if c["opacity"] != 0.1 {
		t.Errorf("opacity must clamp to min 0.1, got %v", c["opacity"])
	}
	rule, _ := c["rule"].(map[string]any)
	if rule["kind"] != "tmdb-rating" || rule["value"] != 7.5 {
		t.Errorf("rule value must be kept: %v", rule)
	}
	if _, present := rule["bogus"]; present {
		t.Error("unknown rule params must be dropped")
	}
	// Non-numeric coords fall back to defaults.
	c2, _ := SanitizePlacement(map[string]any{"badgeId": "b", "x": "nope"})
	if c2["x"] != 0.5 || c2["wFrac"] != 0.22 || c2["opacity"] != 1.0 {
		t.Errorf("defaults wrong: %v", c2)
	}
	// Non-object rule → always.
	c3, _ := SanitizePlacement(map[string]any{"badgeId": "b", "rule": "weird"})
	r3, _ := c3["rule"].(map[string]any)
	if r3["kind"] != "always" {
		t.Errorf("bad rule must become always, got %v", r3)
	}
}

func TestTemplateStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewTemplateStore(dir)
	placements := json.RawMessage(`[
		{"badgeId":"badge-4k","x":0.02,"y":0.02,"wFrac":0.22,"opacity":1,"rule":{"kind":"always"},"text":""},
		{"badgeId":"fanart-foo","x":0.5,"y":0.5,"wFrac":0.3,"opacity":1,"rule":{"kind":"always"},"text":""},
		{"badgeId":"","x":0.5}
	]`)
	saved, err := store.Save("Movie Night", placements)
	if err != nil {
		t.Fatal(err)
	}
	if saved["name"] != "Movie Night" || saved["placements"] != 1 || saved["skippedFanart"] != 1 {
		t.Fatalf("save result wrong: %v", saved)
	}
	// File lands under overlay-templates/ with a slug name.
	if _, err := os.Stat(filepath.Join(dir, "overlay-templates", "movie-night.json")); err != nil {
		t.Fatalf("template file missing: %v", err)
	}
	list := store.List()
	if len(list) != 1 || list[0]["name"] != "Movie Night" || list[0]["placements"] != 1 {
		t.Fatalf("list wrong: %v", list)
	}
	loaded, err := store.Load("Movie Night")
	if err != nil {
		t.Fatal(err)
	}
	pl, _ := loaded["placements"].([]map[string]any)
	if len(pl) != 1 || pl[0]["badgeId"] != "badge-4k" {
		t.Fatalf("loaded placements wrong: %v", loaded)
	}
	removed, err := store.Remove("Movie Night")
	if err != nil || removed["deleted"] != "Movie Night" {
		t.Fatalf("remove: %v %v", removed, err)
	}
	if len(store.List()) != 0 {
		t.Error("list must be empty after remove")
	}
	if _, err := store.Remove("Movie Night"); err == nil {
		t.Error("removing a missing template must error")
	}
	if _, err := store.Load("Movie Night"); err == nil {
		t.Error("loading a missing template must error")
	}
}

func TestTemplateStoreFamilyLayout(t *testing.T) {
	dir := t.TempDir()
	store := NewTemplateStore(dir)
	families := json.RawMessage(`{
		"resolution": {"on": true, "corner": "tl", "size": 1.1, "variant": "aurora"},
		"ratings": {"on": true, "slots": ["imdb", "tmdb", "imdb", "bogus"]},
		"status": {"on": false, "variant": "neon"},
		"ribbon": {"on": true, "honor": "top-rated", "source": "imdb", "threshold": 42},
		"bogus-family": {"on": true}
	}`)
	// A pure family layout (zero custom placements) is a valid template.
	saved, err := store.SaveLayout("Family Night", json.RawMessage(`[]`), families)
	if err != nil {
		t.Fatal(err)
	}
	if saved["kind"] != "families" || saved["families"] != 3 || saved["placements"] != 0 {
		t.Fatalf("family save result wrong: %v", saved)
	}
	list := store.List()
	if len(list) != 1 || list[0]["kind"] != "families" || list[0]["families"] != 3 {
		t.Fatalf("family list wrong: %v", list)
	}
	loaded, err := store.Load("Family Night")
	if err != nil {
		t.Fatal(err)
	}
	if loaded["legacy"] != false {
		t.Fatalf("family template must not be legacy: %v", loaded)
	}
	fam, _ := loaded["families"].(map[string]any)
	if fam == nil {
		t.Fatalf("loaded families missing: %v", loaded)
	}
	if _, bad := fam["bogus-family"]; bad {
		t.Error("unknown family ids must be dropped")
	}
	ratings, _ := fam["ratings"].(map[string]any)
	slots, _ := ratings["slots"].([]any)
	if len(slots) != 2 || slots[0] != "imdb" || slots[1] != "tmdb" {
		t.Errorf("slots must dedupe + drop unknown sources, got %v", ratings["slots"])
	}
	ribbon, _ := fam["ribbon"].(map[string]any)
	if ribbon["threshold"] != 10.0 {
		t.Errorf("threshold must clamp to 10, got %v", ribbon["threshold"])
	}
	status, _ := fam["status"].(map[string]any)
	if status["on"] != false {
		t.Error("explicit off must survive the round trip")
	}
	// Art variant: "aurora" round-trips; a bogus variant is dropped
	// (the renderer reads a missing variant as classic).
	res, _ := fam["resolution"].(map[string]any)
	if res["variant"] != "aurora" {
		t.Errorf("variant aurora must round-trip, got %v", res["variant"])
	}
	if _, bad := status["variant"]; bad {
		t.Error("bogus variant must be dropped, not stored")
	}
	// Legacy templates (saved the old way) list + load as legacy and
	// keep their placements untouched.
	if _, err := store.Save("Old School", json.RawMessage(`[{"badgeId":"badge-4k","x":0.02,"y":0.02,"wFrac":0.22,"opacity":1,"rule":{"kind":"always"},"text":""}]`)); err != nil {
		t.Fatal(err)
	}
	list = store.List()
	var legacyEntry map[string]any
	for _, e := range list {
		if e["name"] == "Old School" {
			legacyEntry = e
		}
	}
	if legacyEntry == nil || legacyEntry["kind"] != "legacy" || legacyEntry["families"] != 0 {
		t.Fatalf("legacy template must list as legacy: %v", list)
	}
	oldLoaded, err := store.Load("Old School")
	if err != nil {
		t.Fatal(err)
	}
	if oldLoaded["legacy"] != true {
		t.Errorf("legacy load must flag legacy: %v", oldLoaded)
	}
	if _, hasFam := oldLoaded["families"]; hasFam {
		t.Error("legacy load must not invent families")
	}
	if pl, _ := oldLoaded["placements"].([]map[string]any); len(pl) != 1 {
		t.Errorf("legacy placements must survive: %v", oldLoaded["placements"])
	}
	// Families that sanitize to nothing are NOT a template at all.
	if _, err := store.SaveLayout("Nothing", json.RawMessage(`[]`), json.RawMessage(`{"bogus":{"on":true}}`)); err == nil {
		t.Error("all-invalid families with no placements must error")
	}
}

// The v1.11 badge-library families (hdr, channels, aspect-ratio,
// franchise, origin, collection) are first-class template families:
// they survive the sanitize round trip with their generic settings,
// like every family registered before them.
func TestTemplateStoreBadgeLibraryFamilies(t *testing.T) {
	dir := t.TempDir()
	store := NewTemplateStore(dir)
	families := json.RawMessage(`{
		"hdr": {"on": true, "variant": "signature"},
		"channels": {"on": true, "variant": "aurora"},
		"aspect-ratio": {"on": true},
		"franchise": {"on": true, "corner": "bl"},
		"origin": {"on": false},
		"collection": {"on": true, "variant": "classic", "size": 1.2}
	}`)
	saved, err := store.SaveLayout("Badge Library", json.RawMessage(`[]`), families)
	if err != nil {
		t.Fatal(err)
	}
	// saved["families"] counts ENABLED families (the store's
	// documented semantics) — five here, since origin is explicitly
	// off. Survival of all six (incl. the off one) is asserted on
	// the loaded map below.
	if saved["families"] != 5 {
		t.Fatalf("five enabled v1.11 families must be counted, got %v", saved)
	}
	loaded, err := store.Load("Badge Library")
	if err != nil {
		t.Fatal(err)
	}
	fam, _ := loaded["families"].(map[string]any)
	for _, id := range []string{"hdr", "channels", "aspect-ratio", "franchise", "origin", "collection"} {
		if _, ok := fam[id]; !ok {
			t.Errorf("family %q missing after round trip", id)
		}
	}
	if coll, _ := fam["collection"].(map[string]any); coll["variant"] != "classic" {
		t.Errorf("collection variant = %v, want classic (round-tripped)", coll["variant"])
	}
	if orig, _ := fam["origin"].(map[string]any); orig["on"] != false {
		t.Errorf("origin on = %v, want false (explicit off survives)", orig["on"])
	}
}

func TestTemplateStoreLaymanPackFamilies(t *testing.T) {
	// v1.11.1 layman pack: the family + saga-position families
	// round-trip; quick-watch keeps its minutes threshold
	// (clamped); the genre family keeps mode "only" + the
	// isolated genre string + the banner presentation — and the
	// OTHER families' mode vocabularies are not stored on genre,
	// nor genre's on them (sanitizer parity with composite.js
	// familySettings).
	dir := t.TempDir()
	store := NewTemplateStore(dir)
	families := json.RawMessage(`{
		"family": {"on": true, "variant": "signature"},
		"saga-position": {"on": true, "variant": "aurora"},
		"quick-watch": {"on": true, "minutes": 45},
		"genre": {"on": true, "mode": "only", "genre": "Horror", "presentation": "banner", "count": 2},
		"streaming": {"on": true, "mode": "only"},
		"subtitles": {"on": true, "mode": "only"}
	}`)
	saved, err := store.SaveLayout("Layman Pack", json.RawMessage(`[]`), families)
	if err != nil {
		t.Fatal(err)
	}
	if saved["families"] != 6 {
		t.Fatalf("six enabled layman-era families must be counted, got %v", saved)
	}
	loaded, err := store.Load("Layman Pack")
	if err != nil {
		t.Fatal(err)
	}
	fam, _ := loaded["families"].(map[string]any)
	for _, id := range []string{"family", "saga-position", "quick-watch", "genre", "streaming", "subtitles"} {
		if _, ok := fam[id]; !ok {
			t.Errorf("family %q missing after round trip", id)
		}
	}
	if qw, _ := fam["quick-watch"].(map[string]any); qw["minutes"] != 45.0 {
		t.Errorf("quick-watch minutes = %v, want 45 (round-tripped)", qw["minutes"])
	}
	g, _ := fam["genre"].(map[string]any)
	if g["mode"] != "only" {
		t.Errorf("genre mode = %v, want only (round-tripped)", g["mode"])
	}
	if g["genre"] != "Horror" {
		t.Errorf("genre isolated name = %v, want Horror (round-tripped)", g["genre"])
	}
	if g["presentation"] != "banner" {
		t.Errorf("genre presentation = %v, want banner (round-tripped)", g["presentation"])
	}
	if st, _ := fam["streaming"].(map[string]any); st["mode"] != nil {
		t.Errorf("streaming mode = %v, want dropped (only is genre vocab, not streaming's)", st["mode"])
	}
	if sub, _ := fam["subtitles"].(map[string]any); sub["mode"] != nil {
		t.Errorf("subtitles mode = %v, want dropped (only is not subtitles vocab)", sub["mode"])
	}
	// Clamp + drop discipline, direct through the sanitizer.
	var decoded any
	if err := json.Unmarshal([]byte(`{
		"quick-watch": {"minutes": 9999},
		"genre": {"mode": "bogus", "presentation": "wide", "genre": "  "}
	}`), &decoded); err != nil {
		t.Fatal(err)
	}
	clean := SanitizeFamilies(decoded)
	if qw, _ := clean["quick-watch"].(map[string]any); qw["minutes"] != 600.0 {
		t.Errorf("quick-watch minutes 9999 must clamp to 600, got %v", qw["minutes"])
	}
	g2, _ := clean["genre"].(map[string]any)
	if g2["mode"] != nil || g2["presentation"] != nil {
		t.Errorf("bogus genre mode/presentation must be dropped, got mode=%v presentation=%v", g2["mode"], g2["presentation"])
	}
	if g2["genre"] != nil {
		t.Errorf("whitespace-only isolated genre must be dropped, got %v", g2["genre"])
	}
	var decoded2 any
	if err := json.Unmarshal([]byte(`{"quick-watch": {"minutes": "soon"}}`), &decoded2); err != nil {
		t.Fatal(err)
	}
	clean2 := SanitizeFamilies(decoded2)
	if qw, _ := clean2["quick-watch"].(map[string]any); qw["minutes"] != nil {
		t.Errorf("non-numeric minutes must be dropped, got %v", qw["minutes"])
	}
}

func TestTemplateStorePillSettings(t *testing.T) {
	// v1.15 pill settings: the family family's ratings set
	// round-trips through the store (known values only,
	// uppercased, deduped, vocabulary order; an explicit empty
	// set survives as empty), and the leaving family's entry
	// round-trips too — its ID was missing from the sanitizer
	// whitelist when the family shipped in v1.12, so saved
	// templates silently dropped it until now.
	dir := t.TempDir()
	store := NewTemplateStore(dir)
	families := json.RawMessage(`{
		"family": {"on": true, "variant": "signature", "ratings": ["TV-PG", "bogus", "tv-g", "TV-PG", "G", 42]},
		"leaving": {"on": true, "mode": "date", "windowDays": 45}
	}`)
	saved, err := store.SaveLayout("Pill Settings", json.RawMessage(`[]`), families)
	if err != nil {
		t.Fatal(err)
	}
	if saved["families"] != 2 {
		t.Fatalf("both enabled families must be counted (leaving included), got %v", saved)
	}
	loaded, err := store.Load("Pill Settings")
	if err != nil {
		t.Fatal(err)
	}
	fam, _ := loaded["families"].(map[string]any)
	f, _ := fam["family"].(map[string]any)
	ratings, _ := f["ratings"].([]any)
	want := []string{"G", "TV-G", "TV-PG"}
	if len(ratings) != len(want) {
		t.Fatalf("family ratings = %v, want %v (unknown dropped, deduped, vocabulary order)", ratings, want)
	}
	for i, w := range want {
		if ratings[i] != w {
			t.Errorf("family ratings[%d] = %v, want %v", i, ratings[i], w)
		}
	}
	lv, _ := fam["leaving"].(map[string]any)
	if lv == nil {
		t.Fatal("leaving family missing after round trip (sanitizer whitelist regression)")
	}
	if lv["mode"] != "date" {
		t.Errorf("leaving mode = %v, want date (round-tripped)", lv["mode"])
	}
	if lv["windowDays"] != 45.0 {
		t.Errorf("leaving windowDays = %v, want 45 (round-tripped)", lv["windowDays"])
	}
	// An explicitly empty ratings set round-trips as empty — the
	// can-never-fire state, distinct from an absent key.
	var decoded any
	if err := json.Unmarshal([]byte(`{"family": {"ratings": []}}`), &decoded); err != nil {
		t.Fatal(err)
	}
	clean := SanitizeFamilies(decoded)
	cf, _ := clean["family"].(map[string]any)
	empty, present := cf["ratings"].([]any)
	if !present || len(empty) != 0 {
		t.Errorf("explicit empty ratings must survive as an empty set, got %v (present=%v)", cf["ratings"], present)
	}
	// A non-array ratings value is dropped (absent key → the
	// renderer applies the shipped default set).
	var decoded2 any
	if err := json.Unmarshal([]byte(`{"family": {"ratings": "TV-PG"}}`), &decoded2); err != nil {
		t.Fatal(err)
	}
	clean2 := SanitizeFamilies(decoded2)
	cf2, _ := clean2["family"].(map[string]any)
	if _, present := cf2["ratings"]; present {
		t.Errorf("non-array ratings must be dropped, got %v", cf2["ratings"])
	}
}

func TestTemplateStoreErrors(t *testing.T) {
	dir := t.TempDir()
	store := NewTemplateStore(dir)
	if _, err := store.Save("  ", json.RawMessage(`[{"badgeId":"b"}]`)); err == nil {
		t.Error("empty name must error")
	}
	if _, err := store.Save("X", json.RawMessage(`[]`)); err == nil {
		t.Error("empty placements must error")
	}
	if _, err := store.Save("X", json.RawMessage(`[{"badgeId":"fanart-a"}]`)); err == nil {
		t.Error("all-fanart template must error")
	}
	// Corrupt file: load errors, list skips it.
	tdir := filepath.Join(dir, "overlay-templates")
	_ = os.MkdirAll(tdir, 0o755)
	_ = os.WriteFile(filepath.Join(tdir, "broken.json"), []byte("{nope"), 0o644)
	if _, err := store.Load("broken"); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("corrupt load must error with 'corrupt', got %v", err)
	}
	if len(store.List()) != 0 {
		t.Error("list must skip corrupt files")
	}
	// A template whose placements all sanitize away fails load.
	_ = os.WriteFile(filepath.Join(tdir, "empty.json"), []byte(`{"name":"Empty","createdAt":"","placements":[{"badgeId":"fanart-x"}]}`), 0o644)
	if _, err := store.Load("empty"); err == nil {
		t.Error("template with no usable placements must fail load")
	}
}

// Facts v2: string rule params (tier/status/dub class) and `op` must
// survive template sanitizing — the old numeric-only pass stripped them,
// and stripped `op` made saved rating rules silently never fire.
func TestSanitizePlacementKeepsStringRuleValueAndOp(t *testing.T) {
	p, ok := SanitizePlacement(map[string]any{
		"badgeId": "pill-rescombo", "x": 0.1, "y": 0.1, "wFrac": 0.3, "opacity": 1.0,
		"rule": map[string]any{"kind": "resolution-is", "value": "4K"},
	})
	if !ok {
		t.Fatal("placement dropped")
	}
	rule, _ := p["rule"].(map[string]any)
	if rule["value"] != "4K" {
		t.Errorf("string rule value lost: %v", rule)
	}

	p2, ok := SanitizePlacement(map[string]any{
		"badgeId": "tomato", "x": 0.1, "y": 0.1, "wFrac": 0.2, "opacity": 1.0,
		"rule": map[string]any{"kind": "rt-rating", "op": ">=", "value": float64(80)},
	})
	if !ok {
		t.Fatal("placement dropped")
	}
	rule2, _ := p2["rule"].(map[string]any)
	if rule2["op"] != ">=" {
		t.Errorf("rule op lost: %v", rule2)
	}
	if rule2["value"] != float64(80) {
		t.Errorf("numeric rule value lost: %v", rule2)
	}

	// A bogus op is still refused.
	p3, _ := SanitizePlacement(map[string]any{
		"badgeId": "tomato", "x": 0.1, "y": 0.1, "wFrac": 0.2, "opacity": 1.0,
		"rule": map[string]any{"kind": "rt-rating", "op": "DROP TABLE", "value": float64(1)},
	})
	rule3, _ := p3["rule"].(map[string]any)
	if _, present := rule3["op"]; present {
		t.Errorf("bogus op must be dropped: %v", rule3)
	}
}

// The badge-pack families (watch-status / new / decade / video-codec
// / streaming / studio / genre) and their settings — plus the ribbon's
// awards honor — must round-trip the sanitizer; bogus values drop.
func TestTemplateStoreBadgePackFamilies(t *testing.T) {
	dir := t.TempDir()
	store := NewTemplateStore(dir)
	families := json.RawMessage(`{
		"watch-status": {"on": true, "showWatched": true, "variant": "signature"},
		"new": {"on": true, "windowDays": 45},
		"decade": {"on": true},
		"video-codec": {"on": true},
		"streaming": {"on": true, "region": "gb", "mode": "all"},
		"studio": {"on": false},
		"genre": {"on": true, "count": 2},
		"ribbon": {"on": true, "honor": "awards"}
	}`)
	if _, err := store.SaveLayout("Badge Pack", json.RawMessage(`[]`), families); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("Badge Pack")
	if err != nil {
		t.Fatal(err)
	}
	fam, _ := loaded["families"].(map[string]any)
	if fam == nil {
		t.Fatalf("loaded families missing: %v", loaded)
	}
	for _, id := range []string{"watch-status", "new", "decade", "video-codec", "streaming", "studio", "genre"} {
		if _, ok := fam[id]; !ok {
			t.Errorf("badge-pack family %q must survive the sanitizer", id)
		}
	}
	ws, _ := fam["watch-status"].(map[string]any)
	if ws["showWatched"] != true {
		t.Errorf("showWatched=true must round-trip, got %v", ws["showWatched"])
	}
	nw, _ := fam["new"].(map[string]any)
	if nw["windowDays"] != float64(45) {
		t.Errorf("windowDays=45 must round-trip, got %v", nw["windowDays"])
	}
	st, _ := fam["streaming"].(map[string]any)
	if st["region"] != "GB" {
		t.Errorf("region must round-trip uppercased, got %v", st["region"])
	}
	if st["mode"] != "all" {
		t.Errorf("streaming mode=all must round-trip, got %v", st["mode"])
	}
	gn, _ := fam["genre"].(map[string]any)
	if gn["count"] != float64(2) {
		t.Errorf("genre count=2 must round-trip, got %v", gn["count"])
	}
	rb, _ := fam["ribbon"].(map[string]any)
	if rb["honor"] != "awards" {
		t.Errorf("honor=awards must round-trip, got %v", rb["honor"])
	}

	// Bogus values are dropped (windowDays clamps instead — it is a
	// range setting like size/threshold).
	if _, err := store.SaveLayout("Bogus Pack", json.RawMessage(`[]`), json.RawMessage(`{
		"new": {"on": true, "windowDays": 9999},
		"streaming": {"on": true, "region": "USA", "mode": "languages"},
		"genre": {"on": true, "count": 5},
		"ribbon": {"on": true, "honor": "bogus"}
	}`)); err != nil {
		t.Fatal(err)
	}
	loaded2, err := store.Load("Bogus Pack")
	if err != nil {
		t.Fatal(err)
	}
	fam2, _ := loaded2["families"].(map[string]any)
	if got := fam2["new"].(map[string]any)["windowDays"]; got != float64(365) {
		t.Errorf("windowDays=9999 must clamp to 365, got %v", got)
	}
	st2, _ := fam2["streaming"].(map[string]any)
	if _, bad := st2["region"]; bad {
		t.Error("three-letter region must be dropped, not stored")
	}
	if _, bad := st2["mode"]; bad {
		t.Error("subtitles mode vocabulary must not leak into the streaming family")
	}
	if _, bad := fam2["genre"].(map[string]any)["count"]; bad {
		t.Error("genre count=5 must be dropped (only 1 or 2)")
	}
	if _, bad := fam2["ribbon"].(map[string]any)["honor"]; bad {
		t.Error("bogus honor must be dropped, not stored")
	}
}

// Signature variant + the v1.5 families (source/details/subtitles)
// and the audio detail setting must round-trip the sanitizer; bogus
// values are dropped (renderer reads missing as classic / format).
func TestTemplateStoreSignatureAndNewFamilies(t *testing.T) {
	dir := t.TempDir()
	store := NewTemplateStore(dir)
	families := json.RawMessage(`{
		"audio": {"on": true, "variant": "signature", "detail": "full"},
		"source": {"on": true, "variant": "signature"},
		"details": {"on": false, "variant": "aurora"},
		"subtitles": {"on": true, "detail": "bogus", "mode": "full"},
		"runtime": {"on": true, "variant": "signature", "format": "clock"}
	}`)
	if _, err := store.SaveLayout("Sig Night", json.RawMessage(`[]`), families); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("Sig Night")
	if err != nil {
		t.Fatal(err)
	}
	fam, _ := loaded["families"].(map[string]any)
	if fam == nil {
		t.Fatalf("loaded families missing: %v", loaded)
	}
	audio, _ := fam["audio"].(map[string]any)
	if audio["variant"] != "signature" {
		t.Errorf("signature variant must round-trip, got %v", audio["variant"])
	}
	if audio["detail"] != "full" {
		t.Errorf("audio detail=full must round-trip, got %v", audio["detail"])
	}
	for _, id := range []string{"source", "details", "subtitles", "runtime"} {
		if _, ok := fam[id]; !ok {
			t.Errorf("new family %q must survive the sanitizer", id)
		}
	}
	subs, _ := fam["subtitles"].(map[string]any)
	if _, bad := subs["detail"]; bad {
		t.Error("bogus detail must be dropped, not stored")
	}
	if subs["mode"] != "full" {
		t.Errorf("subtitles mode=full must round-trip, got %v", subs["mode"])
	}
	rt, _ := fam["runtime"].(map[string]any)
	if rt["format"] != "clock" {
		t.Errorf("runtime format=clock must round-trip, got %v", rt["format"])
	}
	// Bogus format/mode values are dropped, never stored.
	if _, err := store.SaveLayout("Bogus Modes", json.RawMessage(`[]`), json.RawMessage(`{
		"runtime": {"on": true, "format": "weird"},
		"subtitles": {"on": true, "mode": "weird"}
	}`)); err != nil {
		t.Fatal(err)
	}
	loaded2, err := store.Load("Bogus Modes")
	if err != nil {
		t.Fatal(err)
	}
	fam2, _ := loaded2["families"].(map[string]any)
	rt2, _ := fam2["runtime"].(map[string]any)
	if _, bad := rt2["format"]; bad {
		t.Error("bogus runtime format must be dropped, not stored")
	}
	subs2, _ := fam2["subtitles"].(map[string]any)
	if _, bad := subs2["mode"]; bad {
		t.Error("bogus subtitles mode must be dropped, not stored")
	}
}
