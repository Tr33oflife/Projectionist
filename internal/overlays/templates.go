package overlays

// overlays/templates.go — saved overlay templates (v0.4), ported from
// v0.4/src/overlays/templates.js.
//
// A template is a NAMED set of badge placements/rules designed in the
// Designer, stored as JSON under userData/overlay-templates/, and
// re-applied through the NORMAL dry-run → apply flow. Templates never
// write to Plex themselves — the backup gate in overlays.go still guards
// every poster write.
//
// Template shape:
//   { name, createdAt, placements: [{ badgeId, x, y, wFrac, opacity, rule, text }] }
//
// Rules:
//   - Names are unique (case-insensitive via slug); saving an existing
//     name overwrites (the renderer confirms in JS).
//   - Placements are sanitized on save AND on load.
//   - fanart-* badges are per-title session badges — they are EXCLUDED
//     from templates with a count returned.
//   - Filenames are slugified from the name; path traversal is impossible
//     (basename + strict slug).
//
// VERIFICATION STATUS: UNVERIFIED live — pure file I/O, unit-tested here
// with real temp dirs (JS used mocked fs via dependency injection).

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DirName is the templates directory name under userData (JS DIR_NAME).
const DirName = "overlay-templates"

var nonSlugRun = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify is JS slugify(): lowercase, non-[a-z0-9] runs → '-', trim '-',
// max 60 chars, fallback "template".
func Slugify(name string) string {
	s := nonSlugRun.ReplaceAllString(strings.ToLower(name), "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = s[:60]
	}
	if s == "" {
		return "template"
	}
	return s
}

// sliceRunes mirrors JS String.slice(0, n) closely enough for the ASCII /
// short-label strings handled here (JS counts UTF-16 code units).
func sliceRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

// jsNumber mirrors JS Number(v) for JSON-decoded values. present=false
// models a missing key (undefined → NaN). null → 0, booleans → 1/0,
// numeric strings parse, other strings → NaN.
func jsNumber(v any, present bool) float64 {
	if !present {
		return math.NaN()
	}
	switch t := v.(type) {
	case nil:
		return 0
	case float64:
		return t
	case bool:
		if t {
			return 1
		}
		return 0
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0
		}
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return n
		}
		return math.NaN()
	default:
		return math.NaN()
	}
}

func jsString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

// SanitizePlacement is JS sanitizePlacement(). Returns the clean
// placement and true, or (nil, false) to drop it. fanart-* placements are
// excluded here as well as counted by Save (JS behavior: sanitize drops
// them; save counts them before calling sanitize).
func SanitizePlacement(p map[string]any) (map[string]any, bool) {
	if p == nil {
		return nil, false
	}
	badgeID, _ := p["badgeId"].(string)
	if badgeID == "" || strings.HasPrefix(badgeID, "fanart-") {
		return nil, false
	}
	num := func(key string, min, max, dflt float64) float64 {
		v, present := p[key]
		n := jsNumber(v, present)
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return dflt
		}
		return math.Min(max, math.Max(min, n))
	}
	// Rule: a plain object → { kind }, anything else → { kind: 'always' }.
	rule := map[string]any{"kind": "always"}
	var rawRule map[string]any
	if rm, ok := p["rule"].(map[string]any); ok {
		rawRule = rm
		kind := "always"
		if ks, ok := rm["kind"].(string); ok && ks != "" {
			kind = ks
		} else if kv, present := rm["kind"]; present && kv != nil {
			kind = jsString(kv)
			if kind == "" {
				kind = "always"
			}
		}
		rule = map[string]any{"kind": sliceRunes(kind, 40)}
	}
	// Keep rule params the designer understands (value/days) — drop the rest.
	if rawRule != nil {
		for _, k := range []string{"value", "days"} {
			if v, present := rawRule[k]; present {
				if n := jsNumber(v, true); !math.IsNaN(n) && !math.IsInf(n, 0) {
					rule[k] = n
				}
			}
		}
		// Facts v2: several rule kinds carry a STRING value (resolution
		// tier "4K", TV status "Ended", dub class "sub") and rating rules
		// carry an `op`. The Electron sanitizer dropped both, which made
		// template-loaded rating rules silently never fire (missing op)
		// and would strip every v2 param — preserve them, tightly capped.
		if sv, ok := rawRule["value"].(string); ok && sv != "" {
			rule["value"] = sliceRunes(sv, 24)
		}
		if op, ok := rawRule["op"].(string); ok {
			switch op {
			case ">=", ">", "<=", "<", "=":
				rule["op"] = op
			}
		}
	}
	text := ""
	if tv, present := p["text"]; present && tv != nil {
		text = jsString(tv)
	}
	return map[string]any{
		"badgeId": sliceRunes(badgeID, 120),
		"x":       num("x", 0, 1, 0.5),
		"y":       num("y", 0, 1, 0.5),
		"wFrac":   num("wFrac", 0.05, 0.6, 0.22),
		"opacity": num("opacity", 0.1, 1, 1),
		"rule":    rule,
		"text":    sliceRunes(text, 40),
	}, true
}

// TemplateStore stores named templates under userData/overlay-templates/.
type TemplateStore struct {
	userDataDir string
}

// NewTemplateStore creates a store rooted at userDataDir.
func NewTemplateStore(userDataDir string) *TemplateStore {
	return &TemplateStore{userDataDir: userDataDir}
}

func (t *TemplateStore) dir() string { return filepath.Join(t.userDataDir, DirName) }

func (t *TemplateStore) ensureDir() { _ = os.MkdirAll(t.dir(), 0o755) }

func (t *TemplateStore) fileFor(name string) string {
	return filepath.Join(t.dir(), Slugify(name)+".json")
}

type templateFile struct {
	Name       string           `json:"name"`
	CreatedAt  string           `json:"createdAt"`
	Placements []map[string]any `json:"placements"`
	// Families is the Kometa-style family layout (2026-10-22 overlay
	// recreation): { familyId: settings }. Absent on templates saved
	// before the family system existed — those are LEGACY templates:
	// their Placements are the whole layout and they still load and
	// apply through the placements pipeline. Never drop this field or
	// silently reinterpret a legacy file.
	Families map[string]any `json:"families,omitempty"`
	// CustomBadges is the v1.24 rule-based custom badge set:
	// [{ id, name, text, color, corner, variant, rule }]. They are
	// part of the template (unlike free-text pins, which live in
	// their own store file) and are sanitized on save AND load.
	CustomBadges []map[string]any `json:"customBadges,omitempty"`
}

// ---- custom badge sanitizing (v1.24) ------------------------------------
// A rule-based custom badge: display fields + one AND-combined rule.
// Rule fields: genre (contains), studio (contains), tag (contains),
// contentRating (exact), watchStatus (watched|unwatched), ratingMin
// (TMDb rating number), yearMin, yearMax (years). Unknown fields,
// bad colors, bad corners, or empty text/name drop the badge —
// never stored verbatim.
var customBadgeColors = map[string]bool{
	"#fbbf24": true, "#22d3ee": true, "#a78bfa": true, "#34d399": true,
	"#f87171": true, "#e2e8f0": true, "#fb923c": true, "#f472b6": true,
}

var customRuleFields = map[string]bool{
	"genre": true, "studio": true, "tag": true, "contentRating": true,
	"watchStatus": true, "ratingMin": true, "yearMin": true, "yearMax": true,
}

// SanitizeCustomBadge cleans one decoded custom badge. Returns
// (nil, false) to drop it.
func SanitizeCustomBadge(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	name := strings.TrimSpace(jsString(m["name"]))
	text := strings.TrimSpace(jsString(m["text"]))
	if name == "" || text == "" {
		return nil, false
	}
	if len([]rune(text)) > 40 {
		text = string([]rune(text)[:40])
	}
	color := strings.ToLower(strings.TrimSpace(jsString(m["color"])))
	if !customBadgeColors[color] {
		color = "#e2e8f0"
	}
	corner := strings.TrimSpace(jsString(m["corner"]))
	if corner != "tl" && corner != "tr" && corner != "bl" && corner != "br" {
		corner = "bl"
	}
	variant := strings.TrimSpace(jsString(m["variant"]))
	if variant != "aurora" && variant != "classic" {
		variant = "signature"
	}
	id := strings.TrimSpace(jsString(m["id"]))
	if id == "" {
		id = "custom-" + Slugify(name)
	}
	out := map[string]any{
		"id": id, "name": sliceRunes(name, 60), "text": text,
		"color": color, "corner": corner, "variant": variant,
	}
	if rawRule, ok := m["rule"].(map[string]any); ok {
		rule := map[string]any{}
		for k, val := range rawRule {
			if !customRuleFields[k] {
				continue
			}
			switch k {
			case "genre", "studio", "tag", "contentRating":
				if s := strings.TrimSpace(jsString(val)); s != "" {
					rule[k] = sliceRunes(s, 60)
				}
			case "watchStatus":
				if s := strings.TrimSpace(jsString(val)); s == "watched" || s == "unwatched" {
					rule[k] = s
				}
			case "ratingMin", "yearMin", "yearMax":
				if n := jsNumber(val, true); !math.IsNaN(n) {
					rule[k] = n
				}
			}
		}
		out["rule"] = rule
	} else {
		out["rule"] = map[string]any{}
	}
	return out, true
}

// SanitizeCustomBadges cleans a decoded custom badge array.
func SanitizeCustomBadges(v any) []map[string]any {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := []map[string]any{}
	seen := map[string]bool{}
	for _, raw := range arr {
		if c, ok := SanitizeCustomBadge(raw); ok {
			if seen[c["id"].(string)] {
				continue
			}
			seen[c["id"].(string)] = true
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ---- family layout sanitizing ------------------------------------------------
// The family settings shape is renderer-owned, but storage still
// whitelists it (same doctrine as placements): known family ids only,
// known keys only, values clamped to the ranges the designer offers.
// Anything else is dropped, never stored verbatim.

var familyIDs = map[string]bool{
	"resolution": true, "audio": true, "ratings": true,
	"content-rating": true, "status": true, "languages": true,
	"edition": true, "ribbon": true, "versions": true,
	"source": true, "details": true, "subtitles": true,
	"runtime":      true,
	"watch-status": true, "new": true, "decade": true,
	"video-codec": true, "streaming": true, "studio": true,
	"genre": true,
	// Badge-library expansion (v1.11): HDR, channels, aspect
	// ratio, franchise, origin — no family-owned settings keys
	// beyond the generic ones sanitized for every family.
	"hdr": true, "channels": true, "aspect-ratio": true,
	"franchise": true, "origin": true, "collection": true,
	// Layman pack (v1.11.1): saga position (no family-owned keys),
	// family (its ratings set since v1.15, sanitized below), and
	// quick watch (its minutes threshold, sanitized below).
	"family": true, "saga-position": true, "quick-watch": true,
	// Leaving soon (v1.12): its mode + soon window are sanitized
	// below. The ID itself was missing from this whitelist when
	// the family shipped, so a saved template silently dropped
	// the leaving entry (the mode case below was unreachable);
	// restored in v1.15 when the family's settings were audited.
	"leaving": true,
	// New families (v1.24): awards + RT Fresh pills, bitrate,
	// in-progress. Studio + decade already existed (they are in
	// this list above via the earlier entries) — the v1.24 brief
	// named them, but duplicating them would have forked the
	// registry, so they were verified in place, not re-added.
	"awards": true, "rt-fresh": true, "bitrate": true, "in-progress": true,
}

// familyRatingVocab is the full content-rating list the family
// family's `ratings` setting may name (v1.15, mirrors
// FAMILY_RATING_VOCAB in composite.js). Both sides keep only
// these values, deduped, in this order, so a stored set — and
// any verdict note listing it — is deterministic.
var familyRatingVocab = []string{"G", "PG", "PG-13", "R", "NC-17",
	"TV-Y", "TV-Y7", "TV-Y7-FV", "TV-G", "TV-PG", "TV-14", "TV-MA"}

var familyRatingAllowed = func() map[string]bool {
	m := map[string]bool{}
	for _, r := range familyRatingVocab {
		m[r] = true
	}
	return m
}()

var ratingSources = map[string]bool{
	"tmdb": true, "imdb": true, "rt-critic": true, "rt-audience": true, "metascore": true,
}

// SanitizeFamilies cleans a decoded family-layout value. Returns nil
// when nothing valid remains (caller then treats the template as
// legacy). A family counts as ENABLED unless its stored "on" is
// explicitly false — EnabledFamilies is the count List reports.
func SanitizeFamilies(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]any{}
	for id, rawEntry := range m {
		if !familyIDs[id] {
			continue
		}
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			continue
		}
		clean := map[string]any{}
		if on, ok := entry["on"].(bool); ok {
			clean["on"] = on
		}
		if c, ok := entry["corner"].(string); ok {
			switch c {
			case "tl", "tr", "bl", "br":
				clean["corner"] = c
			}
		}
		clampNum := func(key string, min, max float64) {
			// JSON numbers decode as float64; anything else (string,
			// bool, null) is not a setting value — drop it.
			if n, ok := entry[key].(float64); ok && !math.IsNaN(n) && !math.IsInf(n, 0) {
				clean[key] = math.Min(max, math.Max(min, n))
			}
		}
		clampNum("size", 0.5, 2)
		clampNum("dx", -0.3, 0.3)
		clampNum("dy", -0.3, 0.3)
		clampNum("threshold", 0, 10)
		if rawSlots, ok := entry["slots"].([]any); ok {
			slots := []any{}
			seen := map[string]bool{}
			for _, sv := range rawSlots {
				s, ok := sv.(string)
				if ok && ratingSources[s] && !seen[s] && len(slots) < 3 {
					seen[s] = true
					slots = append(slots, s)
				}
			}
			clean["slots"] = slots
		}
		if h, ok := entry["honor"].(string); ok {
			switch h {
			case "rt-fresh", "top-rated", "awards", "off":
				clean["honor"] = h
			}
		}
		if s, ok := entry["source"].(string); ok {
			switch s {
			case "tmdb", "imdb":
				clean["source"] = s
			}
		}
		if st, ok := entry["style"].(string); ok {
			switch st {
			case "codes", "class", "codes-subs":
				clean["style"] = st
			}
		}
		// Audio detail mode (mirrors familySettings in composite.js):
		// the format name alone, or format + channels + language.
		if d, ok := entry["detail"].(string); ok {
			switch d {
			case "format", "full":
				clean["detail"] = d
			}
		}
		// Runtime format (mirrors familySettings in composite.js):
		// "2H 28M" hours/minutes, or "2:28" clock style.
		if f, ok := entry["format"].(string); ok {
			switch f {
			case "hm", "clock":
				clean["format"] = f
			}
		}
		// Mode is family-owned (mirrors familySettings in
		// composite.js): subtitles read languages/count/full, the
		// streaming family reads first/all, the genre family reads
		// first/only, the leaving family reads soon/date — the other
		// families' vocabularies are dropped, never stored.
		if md, ok := entry["mode"].(string); ok {
			switch id {
			case "streaming":
				switch md {
				case "first", "all":
					clean["mode"] = md
				}
			case "genre":
				switch md {
				case "first", "only":
					clean["mode"] = md
				}
			case "leaving":
				switch md {
				case "soon", "date":
					clean["mode"] = md
				}
			default:
				switch md {
				case "languages", "count", "full":
					clean["mode"] = md
				}
			}
		}
		// Genre isolation + presentation (v1.11.1, mirrors
		// familySettings in composite.js): the ONE genre "only"
		// mode isolates — a plain template string, trimmed and
		// length-capped like the other label strings — and the
		// pill/banner presentation choice.
		if id == "genre" {
			if g, ok := entry["genre"].(string); ok {
				if g = strings.TrimSpace(g); g != "" {
					clean["genre"] = sliceRunes(g, 40)
				}
			}
			if pr, ok := entry["presentation"].(string); ok {
				switch pr {
				case "pill", "banner":
					clean["presentation"] = pr
				}
			}
		}
		// Quick-watch threshold (v1.11.1, mirrors familySettings
		// in composite.js): the minutes under which a title is a
		// quick watch.
		if id == "quick-watch" {
			clampNum("minutes", 15, 600)
		}
		// Family rating set (v1.15, mirrors familySettings in
		// composite.js): the content ratings that count as family.
		// Only known rating strings survive — trimmed, uppercased,
		// deduped, stored in vocabulary order. An explicitly empty
		// set round-trips as empty (the family can never fire and
		// its verdict says the set is empty) — a different state
		// from an ABSENT key, which the renderer reads as the
		// shipped default set.
		if id == "family" {
			if rawRatings, ok := entry["ratings"].([]any); ok {
				present := map[string]bool{}
				for _, rv := range rawRatings {
					if s, ok := rv.(string); ok {
						if u := strings.ToUpper(strings.TrimSpace(s)); familyRatingAllowed[u] {
							present[u] = true
						}
					}
				}
				ratings := []any{}
				for _, v := range familyRatingVocab {
					if present[v] {
						ratings = append(ratings, v)
					}
				}
				clean["ratings"] = ratings
			}
		}
		// New-family settings (mirror familySettings in composite.js):
		// the "New" family's added-within window in days, the genre
		// family's 1-or-2 tag count, the watch-status family's
		// also-badge-watched switch, and the streaming family's
		// two-letter region (stored uppercase).
		clampNum("windowDays", 1, 365)
		if n, ok := entry["count"].(float64); ok && (n == 1 || n == 2) {
			clean["count"] = n
		}
		if b, ok := entry["showWatched"].(bool); ok {
			clean["showWatched"] = b
		}
		if rg, ok := entry["region"].(string); ok && len(rg) == 2 && isRegionLetter(rg[0]) && isRegionLetter(rg[1]) {
			clean["region"] = strings.ToUpper(rg)
		}
		// Art variant (mirrors familySettings in composite.js): only
		// the known packs round-trip; anything else is dropped, which
		// the renderer reads as classic.
		if vr, ok := entry["variant"].(string); ok {
			switch vr {
			case "classic", "aurora", "signature":
				clean["variant"] = vr
			}
		}
		out[id] = clean
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// isRegionLetter reports whether b is an ASCII letter — the
// streaming region is a two-letter code (^[A-Za-z]{2}$ in the
// renderer), nothing else round-trips.
func isRegionLetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// EnabledFamilies counts families whose stored "on" is not false.
func EnabledFamilies(families map[string]any) int {
	n := 0
	for _, rawEntry := range families {
		if entry, ok := rawEntry.(map[string]any); ok {
			if on, present := entry["on"].(bool); !present || on {
				n++
			}
		}
	}
	return n
}

// parsePlacements decodes a raw JSON array whose elements may or may not
// be objects (JS sanitizes non-objects away instead of failing).
func parsePlacements(raw json.RawMessage) []map[string]any {
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return nil
	}
	out := make([]map[string]any, len(elems))
	for i, e := range elems {
		var m map[string]any
		if err := json.Unmarshal(e, &m); err == nil {
			out[i] = m
		}
	}
	return out
}

// Save (or overwrite) a template. placements is the raw JSON array from
// the caller. Returns { name, placements (count), skippedFanart }.
func (t *TemplateStore) Save(name string, placements json.RawMessage) (map[string]any, error) {
	return t.SaveLayout(name, placements, nil)
}

// SaveLayout stores a Kometa-style family layout (familiesRaw =
// the raw JSON object of family settings, nil for a legacy
// badge-only template). With families present, zero custom
// placements is a valid template (a pure family layout); without them
// the legacy rule stands — at least one saveable badge is required.
// Returns { name, placements, skippedFanart, kind, families } where
// kind is "families" or "legacy" and families counts ENABLED families.
func (t *TemplateStore) SaveLayout(name string, placements json.RawMessage, familiesRaw json.RawMessage) (map[string]any, error) {
	return t.SaveLayoutCustom(name, placements, familiesRaw, nil)
}

// SaveLayoutCustom is SaveLayout plus v1.24 rule-based custom badges
// (customBadgesRaw, sanitized the same way as on load).
func (t *TemplateStore) SaveLayoutCustom(name string, placements json.RawMessage, familiesRaw json.RawMessage, customBadgesRaw json.RawMessage) (map[string]any, error) {
	cleanName := sliceRunes(strings.TrimSpace(name), 80)
	if cleanName == "" {
		return nil, fmt.Errorf("Template needs a name")
	}
	var families map[string]any
	if len(familiesRaw) > 0 && string(familiesRaw) != "null" {
		var decoded any
		if err := json.Unmarshal(familiesRaw, &decoded); err == nil {
			families = SanitizeFamilies(decoded)
		}
	}
	// Custom badges decode FIRST: like a family layout, a template
	// whose only content is rule badges is valid — the legacy
	// "at least one badge" gate must not refuse it.
	var customsEarly []map[string]any
	if len(customBadgesRaw) > 0 && string(customBadgesRaw) != "null" {
		var decoded any
		if err := json.Unmarshal(customBadgesRaw, &decoded); err == nil {
			customsEarly = SanitizeCustomBadges(decoded)
		}
	}
	var rawArr []json.RawMessage
	if len(placements) == 0 || json.Unmarshal(placements, &rawArr) != nil || len(rawArr) == 0 {
		if families == nil && len(customsEarly) == 0 {
			return nil, fmt.Errorf("Template needs at least one badge")
		}
		rawArr = nil // pure family/custom layout — custom placements are optional
	}
	raw := parsePlacements(placements)
	t.ensureDir()
	skippedFanart := 0
	clean := []map[string]any{}
	for _, p := range raw {
		if p != nil {
			if bid, ok := p["badgeId"].(string); ok && strings.HasPrefix(bid, "fanart-") {
				skippedFanart++
				continue
			}
		}
		if c, ok := SanitizePlacement(p); ok {
			clean = append(clean, c)
		}
	}
	if len(clean) == 0 && families == nil && len(customsEarly) == 0 {
		return nil, fmt.Errorf("No saveable badges — fanart logos are per-title and cannot be templated")
	}
	customs := customsEarly
	tpl := templateFile{Name: cleanName, CreatedAt: isoNow(), Placements: clean, Families: families, CustomBadges: customs}
	b, _ := json.MarshalIndent(tpl, "", "  ")
	if err := os.WriteFile(t.fileFor(cleanName), b, 0o644); err != nil {
		return nil, err
	}
	kind := "legacy"
	if families != nil {
		kind = "families"
	}
	return map[string]any{"name": cleanName, "placements": len(clean), "skippedFanart": skippedFanart, "kind": kind, "families": EnabledFamilies(families), "customBadges": len(customs)}, nil
}

// List templates: [{ name, createdAt, placements (count) }], sorted by
// name. Corrupt files are skipped (JS behavior).
func (t *TemplateStore) List() []map[string]any {
	t.ensureDir()
	out := []map[string]any{}
	entries, err := os.ReadDir(t.dir())
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(t.dir(), e.Name()))
		if err != nil {
			continue
		}
		var tpl struct {
			Name         string            `json:"name"`
			CreatedAt    string            `json:"createdAt"`
			Placements   []json.RawMessage `json:"placements"`
			Families     map[string]any    `json:"families"`
			CustomBadges []json.RawMessage `json:"customBadges"`
		}
		if err := json.Unmarshal(raw, &tpl); err != nil || tpl.Name == "" {
			continue
		}
		count := 0
		if tpl.Placements != nil {
			count = len(tpl.Placements)
		}
		kind := "legacy"
		famCount := 0
		if tpl.Families != nil {
			kind = "families"
			famCount = EnabledFamilies(tpl.Families)
		}
		out = append(out, map[string]any{"name": tpl.Name, "createdAt": tpl.CreatedAt, "placements": count, "kind": kind, "families": famCount, "customBadges": len(tpl.CustomBadges)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i]["name"].(string) < out[j]["name"].(string)
	})
	return out
}

// Load one template (sanitized). Errors on missing/corrupt. Family
// templates also return "families" (sanitized) and "legacy": false;
// badge-only templates return "legacy": true and their placements —
// the renderer reproduces them through the custom-placements pipeline
// exactly as they were saved.
func (t *TemplateStore) Load(name string) (map[string]any, error) {
	raw, err := os.ReadFile(t.fileFor(name))
	if err != nil {
		return nil, err
	}
	var tpl struct {
		Name         string          `json:"name"`
		CreatedAt    string          `json:"createdAt"`
		Placements   json.RawMessage `json:"placements"`
		Families     map[string]any  `json:"families"`
		CustomBadges json.RawMessage `json:"customBadges"`
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("Template %q is corrupt and cannot be loaded", name)
	}
	if err := json.Unmarshal(raw, &tpl); err != nil {
		return nil, fmt.Errorf("Template %q is corrupt and cannot be loaded", name)
	}
	if _, ok := probe["name"].(string); !ok {
		return nil, fmt.Errorf("Template %q is corrupt and cannot be loaded", name)
	}
	if _, ok := probe["placements"].([]any); !ok {
		return nil, fmt.Errorf("Template %q is corrupt and cannot be loaded", name)
	}
	clean := []map[string]any{}
	for _, p := range parsePlacements(tpl.Placements) {
		if c, ok := SanitizePlacement(p); ok {
			clean = append(clean, c)
		}
	}
	families := SanitizeFamilies(tpl.Families)
	var customs []map[string]any
	if len(tpl.CustomBadges) > 0 {
		var decoded any
		if err := json.Unmarshal(tpl.CustomBadges, &decoded); err == nil {
			customs = SanitizeCustomBadges(decoded)
		}
	}
	if len(clean) == 0 && families == nil && len(customs) == 0 {
		return nil, fmt.Errorf("Template %q has no usable badge placements", name)
	}
	out := map[string]any{"name": tpl.Name, "createdAt": tpl.CreatedAt, "placements": clean, "legacy": families == nil}
	if families != nil {
		out["families"] = families
	}
	if len(customs) > 0 {
		out["customBadges"] = customs
	}
	return out, nil
}

// Remove deletes one template. Returns { deleted: name }.
func (t *TemplateStore) Remove(name string) (map[string]any, error) {
	f := t.fileFor(name)
	if _, err := os.Stat(f); err != nil {
		return nil, fmt.Errorf("Template %q not found", name)
	}
	if err := os.Remove(f); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": name}, nil
}

// ---- Settings backup & restore (v1.22) --------------------------------------
//
// Templates are plain files under overlay-templates/, so they travel in a
// backup as their raw file contents, keyed by template name. Import
// validates shape (a JSON object with a non-empty name and, when present,
// an array placements / object families) and writes through the same
// slugified filename the designer uses — a hostile name can never escape
// the templates directory. Import never deletes templates that are not in
// the file; it adds or overwrites by name.

// ExportAll returns every readable template's raw file bytes, keyed by the
// template's own name. Corrupt files are skipped, like List() skips them.
func (t *TemplateStore) ExportAll() (map[string]json.RawMessage, error) {
	t.ensureDir()
	entries, err := os.ReadDir(t.dir())
	if err != nil {
		return map[string]json.RawMessage{}, nil
	}
	out := map[string]json.RawMessage{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(t.dir(), e.Name()))
		if err != nil {
			continue
		}
		var probe struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil || probe.Name == "" {
			continue
		}
		out[probe.Name] = json.RawMessage(append([]byte(nil), raw...))
	}
	return out, nil
}

// ImportRaw validates one template's raw file bytes and writes them under
// the slugified name. The name inside the file wins over the map key (the
// file is self-describing); a mismatch is not an error.
func (t *TemplateStore) ImportRaw(raw json.RawMessage) (string, error) {
	name, err := ValidateTemplateRaw(raw)
	if err != nil {
		return "", err
	}
	t.ensureDir()
	if err := os.WriteFile(t.fileFor(name), []byte(raw), 0o644); err != nil {
		return "", err
	}
	return name, nil
}

// ValidateTemplateRaw checks one template's raw file bytes without writing
// anything, so a backup import can validate every template before it
// writes the first one.
func ValidateTemplateRaw(raw json.RawMessage) (string, error) {
	var probe struct {
		Name         string          `json:"name"`
		Placements   json.RawMessage `json:"placements"`
		Families     json.RawMessage `json:"families"`
		CustomBadges json.RawMessage `json:"customBadges"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", fmt.Errorf("template is not valid JSON: %w", err)
	}
	name := strings.TrimSpace(probe.Name)
	if name == "" {
		return "", fmt.Errorf("template has no name")
	}
	if len(probe.Placements) > 0 {
		var arr []json.RawMessage
		if err := json.Unmarshal(probe.Placements, &arr); err != nil {
			return "", fmt.Errorf("template %q has placements that are not an array", name)
		}
	}
	if len(probe.Families) > 0 && string(probe.Families) != "null" {
		var obj map[string]any
		if err := json.Unmarshal(probe.Families, &obj); err != nil {
			return "", fmt.Errorf("template %q has families that are not an object", name)
		}
	}
	if len(probe.CustomBadges) > 0 && string(probe.CustomBadges) != "null" {
		var arr []json.RawMessage
		if err := json.Unmarshal(probe.CustomBadges, &arr); err != nil {
			return "", fmt.Errorf("template %q has customBadges that are not an array", name)
		}
	}
	if len(probe.Placements) == 0 && (len(probe.Families) == 0 || string(probe.Families) == "null") && len(probe.CustomBadges) == 0 {
		return "", fmt.Errorf("template %q has neither placements nor families", name)
	}
	return name, nil
}
