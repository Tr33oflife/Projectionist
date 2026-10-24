package store

// custompins.go — free-text custom badge pins (v1.24, Tier 1).
//
// A pin is user-typed badge text attached to chosen items: the user
// writes any text ("STAFF PICK", "MOVIE NIGHT"), picks a color and a
// corner, and pins it to titles — or to a whole collection, which the
// API expands into one pin per current member at creation time (later
// members do not inherit the pin; the UI says so). Pins render through
// the normal badge pipeline at apply time, exactly like a family.
//
// Storage follows the Leaving pattern: one JSON file in the data dir
// (custom-pins.json), written atomically, guarded by the store mutex,
// keyed by the server's machine identity so two servers' pins never
// mix when a ratingKey collides across them. Shape:
//
//	{ "<machine identity>": { "<pin id>": {id, ratingKey, title, text, color, corner, source} } }
//
// source names the collection a pin came from ("" for a direct item
// pin) so the manager can explain a pin's origin. Pins are never
// dropped silently: only an explicit remove deletes one.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CustomPin is one free-text badge pin.
type CustomPin struct {
	ID        string `json:"id"`
	RatingKey string `json:"ratingKey"`
	Title     string `json:"title"`
	Text      string `json:"text"`
	Color     string `json:"color"`
	Corner    string `json:"corner"`
	Variant   string `json:"variant"`
	Source    string `json:"source"`
}

// CustomPinColors is the palette a pin's color must come from — the
// same eight accents the rule-based custom badges offer, so a pin and
// a rule badge in the same color match exactly.
var CustomPinColors = map[string]bool{
	"#fbbf24": true, "#22d3ee": true, "#a78bfa": true, "#34d399": true,
	"#f87171": true, "#e2e8f0": true, "#fb923c": true, "#f472b6": true,
}

// ValidCustomPinText reports whether s is usable badge text: 1–40
// runes after trimming. Longer text is the caller's job to refuse,
// not to truncate silently — a badge that reads differently from
// what the user typed is a lie on the poster.
func ValidCustomPinText(s string) bool {
	r := []rune(strings.TrimSpace(s))
	return len(r) >= 1 && len(r) <= 40
}

// NormalizeCustomPin validates and normalizes one pin's display
// fields (text, color, corner, variant), returning the cleaned
// values or an error. Unknown colors fall back to the default slate
// — a color is presentation, not content, so falling back beats
// refusing the pin; bad corners likewise fall back to bl, and an
// unknown variant falls back to signature.
func NormalizeCustomPin(text, color, corner, variant string) (string, string, string, string, error) {
	t := strings.TrimSpace(text)
	if !ValidCustomPinText(t) {
		return "", "", "", "", fmt.Errorf("pin text must be 1–40 characters")
	}
	c := strings.ToLower(strings.TrimSpace(color))
	if !CustomPinColors[c] {
		c = "#e2e8f0"
	}
	cn := strings.TrimSpace(corner)
	if cn != "tl" && cn != "tr" && cn != "bl" && cn != "br" {
		cn = "bl"
	}
	v := strings.TrimSpace(variant)
	if v != "aurora" && v != "classic" {
		v = "signature"
	}
	return t, c, cn, v, nil
}

func (s *Store) customPinsPath() string { return filepath.Join(s.dir, "custom-pins.json") }

func (s *Store) loadCustomPins() (map[string]map[string]CustomPin, error) {
	out := map[string]map[string]CustomPin{}
	raw, err := os.ReadFile(s.customPinsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("custom pins file is unreadable: %w", err)
	}
	if out == nil {
		out = map[string]map[string]CustomPin{}
	}
	return out, nil
}

func (s *Store) saveCustomPins(all map[string]map[string]CustomPin) error {
	raw, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	tmp := s.customPinsPath() + ".tmp-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.customPinsPath())
}

func newPinID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("pin-%d", time.Now().UnixNano())
	}
	return "pin-" + hex.EncodeToString(b[:])
}

// CustomPinRef names one item a pin is being set on.
type CustomPinRef struct {
	RatingKey string `json:"ratingKey"`
	Title     string `json:"title"`
}

// CustomPins returns a copy of one server's pins, sorted by title
// then text so the manager list is deterministic.
func (s *Store) CustomPins(machine string) ([]CustomPin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadCustomPins()
	if err != nil {
		return nil, err
	}
	out := []CustomPin{}
	for _, p := range all[machine] {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Title != out[j].Title {
			return out[i].Title < out[j].Title
		}
		if out[i].Text != out[j].Text {
			return out[i].Text < out[j].Text
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// CustomPinsForItem returns one item's pins on one server. A lookup
// failure (unreadable file) reports no pins: the facts/evaluate path
// must never break metadata over a pins file, while the manager
// endpoints surface the error through CustomPins.
func (s *Store) CustomPinsForItem(machine, ratingKey string) []CustomPin {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadCustomPins()
	if err != nil {
		return nil
	}
	out := []CustomPin{}
	for _, p := range all[machine] {
		if p.RatingKey == ratingKey {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Text != out[j].Text {
			return out[i].Text < out[j].Text
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// SetCustomPins creates one pin per given item on one server. The
// caller expands collection pins into member refs before calling —
// the store never sees a collection, only items.
func (s *Store) SetCustomPins(machine string, items []CustomPinRef, text, color, corner, variant, source string) ([]CustomPin, error) {
	if machine == "" {
		return nil, fmt.Errorf("no server selected")
	}
	t, c, cn, v, err := NormalizeCustomPin(text, color, corner, variant)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no items to pin to")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadCustomPins()
	if err != nil {
		return nil, err
	}
	per := all[machine]
	if per == nil {
		per = map[string]CustomPin{}
		all[machine] = per
	}
	created := []CustomPin{}
	for _, it := range items {
		if it.RatingKey == "" {
			return nil, fmt.Errorf("an item is missing its ratingKey")
		}
		p := CustomPin{ID: newPinID(), RatingKey: it.RatingKey, Title: it.Title, Text: t, Color: c, Corner: cn, Variant: v, Source: source}
		per[p.ID] = p
		created = append(created, p)
	}
	if err := s.saveCustomPins(all); err != nil {
		return nil, err
	}
	return created, nil
}

// ---- whole-file access (backup export/import) ---------------------------
//
// CustomPinsAll returns a copy of the whole pins file: machine →
// pin id → pin. A corrupt file is an error, never a silent empty list
// (same rule as the Leaving whole-file access).
func (s *Store) CustomPinsAll() (map[string]map[string]CustomPin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadCustomPins()
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]CustomPin, len(all))
	for m, per := range all {
		cp := make(map[string]CustomPin, len(per))
		for k, v := range per {
			cp[k] = v
		}
		out[m] = cp
	}
	return out, nil
}

// SetCustomPinsAll replaces the whole pins file with the given map
// after validating every pin: a non-empty id and ratingKey and
// 1–40-rune text. Colors/corners are normalized, not rejected — they
// are presentation. Nothing is written when validation fails.
func (s *Store) SetCustomPinsAll(all map[string]map[string]CustomPin) error {
	for m, per := range all {
		for id, p := range per {
			if id == "" || p.ID == "" {
				return fmt.Errorf("custom pin on %q is missing its id", m)
			}
			if p.RatingKey == "" {
				return fmt.Errorf("custom pin %q on %q is missing its ratingKey", id, m)
			}
			if !ValidCustomPinText(p.Text) {
				return fmt.Errorf("custom pin %q on %q has invalid text", id, m)
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Normalize colors/corners/variants on import so a hand-edited
	// file can never smuggle a non-palette color into the badge
	// pipeline.
	for _, per := range all {
		for id, p := range per {
			_, c, cn, v, _ := NormalizeCustomPin(p.Text, p.Color, p.Corner, p.Variant)
			p.Color, p.Corner, p.Variant = c, cn, v
			per[id] = p
		}
	}
	return s.saveCustomPins(all)
}

// RemoveCustomPin deletes one pin on one server. Removing a pin that
// does not exist is a no-op, not an error — the owner's intent ("no
// such pin") already holds.
func (s *Store) RemoveCustomPin(machine, pinID string) error {
	if machine == "" {
		return fmt.Errorf("no server selected")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadCustomPins()
	if err != nil {
		return err
	}
	if per := all[machine]; per != nil {
		delete(per, pinID)
	}
	return s.saveCustomPins(all)
}
