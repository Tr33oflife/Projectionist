package store

// leaving.go — the app's own Leaving list (v1.12, "Leaving Soon").
//
// The Leaving list is the app's record of which titles leave the
// owner's library soon, and on what date. The dates are set IN THE
// APP (the owner's decision, 2026-10-23): they do not come from Plex
// labels, TMDb, or any external source, and nothing in the app ever
// invents one — an item with no entry simply has no leaving date.
//
// Storage follows the store layer's patterns: one JSON file in the
// data dir (leaving.json), written atomically (temp file + rename, so
// a crash mid-save can never tear it), guarded by the store mutex.
// Shape:
//
//	{ "<machine identity>": { "<ratingKey>": {"title": "...", "date": "YYYY-MM-DD"} } }
//
// Keying by the server's machine identity keeps two servers' lists
// isolated even when a ratingKey collides across them — ratingKeys
// are only unique per server. Each entry carries a TITLE SNAPSHOT
// (the title as the owner saw it when the date was set) so the
// manager can list entries without a Plex round-trip per row; Plex
// identity is the (machine, ratingKey) pair, never the title.
//
// Entries are never dropped silently: an entry whose date has passed
// stays in the list (the manager shows it as expired and the owner
// clears it by hand). Only an explicit remove deletes an entry.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// LeavingEntry is one row of the Leaving list: a title snapshot and
// the date (YYYY-MM-DD) the title leaves.
type LeavingEntry struct {
	Title string `json:"title"`
	Date  string `json:"date"`
}

// LeavingItemRef names one item a date is being set on: its ratingKey
// on the server and the title snapshot to store with the entry.
type LeavingItemRef struct {
	RatingKey string `json:"ratingKey"`
	Title     string `json:"title"`
}

// ValidLeavingDate reports whether s is a real calendar date in
// strict YYYY-MM-DD form. The re-format equality check is what makes
// it strict: "2026-2-5" parses under some layouts but is not the
// stored form, and impossible dates (2026-02-30) fail the parse.
func ValidLeavingDate(s string) bool {
	if len(s) != 10 {
		return false
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return false
	}
	return t.Format("2006-01-02") == s
}

// DaysUntilLeaving counts whole calendar days from "today" to the
// leaving date; negative when the date has passed. "Today" is the
// APP's local date — the clock of the machine running this program
// (time.Now in its local zone), NOT the Plex server's clock: the two
// can differ, and the app's own date is the one the owner reads in
// the manager. The subtraction runs on UTC midnights built from the
// civil (year/month/day) parts, so daylight-saving shifts can never
// skew the count by an hour and round it the wrong way.
func DaysUntilLeaving(date string, now time.Time) (int, bool) {
	if !ValidLeavingDate(date) {
		return 0, false
	}
	t, _ := time.Parse("2006-01-02", date)
	local := now.Local()
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	target := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return int(target.Sub(today).Hours() / 24), true
}

func (s *Store) leavingPath() string { return filepath.Join(s.dir, "leaving.json") }

// loadLeaving reads the whole Leaving file. A missing file is an
// empty list, not an error; a corrupt file IS an error — callers that
// can surface it (the manager endpoints) do, so a broken file is
// never quietly treated as "no dates set anywhere".
func (s *Store) loadLeaving() (map[string]map[string]LeavingEntry, error) {
	out := map[string]map[string]LeavingEntry{}
	raw, err := os.ReadFile(s.leavingPath())
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
		return nil, fmt.Errorf("leaving list file is unreadable: %w", err)
	}
	if out == nil {
		out = map[string]map[string]LeavingEntry{}
	}
	return out, nil
}

// saveLeaving persists the whole Leaving file atomically.
func (s *Store) saveLeaving(all map[string]map[string]LeavingEntry) error {
	raw, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	tmp := s.leavingPath() + ".tmp-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.leavingPath())
}

// LeavingEntries returns a copy of one server's Leaving entries,
// keyed by ratingKey. Expired entries are included — expiring is a
// display state, not a deletion.
func (s *Store) LeavingEntries(machine string) (map[string]LeavingEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadLeaving()
	if err != nil {
		return nil, err
	}
	out := map[string]LeavingEntry{}
	for k, v := range all[machine] {
		out[k] = v
	}
	return out, nil
}

// LeavingEntryFor returns one item's entry on one server. A lookup
// failure (unreadable file) reports "not found": the facts layer
// calls this on every facts request, and a broken Leaving file must
// never break metadata — the manager endpoints read through
// LeavingEntries and DO surface the error there.
func (s *Store) LeavingEntryFor(machine, ratingKey string) (LeavingEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadLeaving()
	if err != nil {
		return LeavingEntry{}, false
	}
	e, ok := all[machine][ratingKey]
	return e, ok
}

// SetLeaving sets (or replaces) the leaving date for every given item
// on one server. The date must be a real calendar date in YYYY-MM-DD
// form — anything else is rejected and nothing is written.
func (s *Store) SetLeaving(machine string, items []LeavingItemRef, date string) error {
	if machine == "" {
		return fmt.Errorf("no server selected")
	}
	if !ValidLeavingDate(date) {
		return fmt.Errorf("invalid leaving date %q: not a real calendar date in YYYY-MM-DD form", date)
	}
	if len(items) == 0 {
		return fmt.Errorf("no items to set a leaving date on")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadLeaving()
	if err != nil {
		return err
	}
	per := all[machine]
	if per == nil {
		per = map[string]LeavingEntry{}
		all[machine] = per
	}
	for _, it := range items {
		if it.RatingKey == "" {
			return fmt.Errorf("an item is missing its ratingKey")
		}
		per[it.RatingKey] = LeavingEntry{Title: it.Title, Date: date}
	}
	return s.saveLeaving(all)
}

// RemoveLeaving deletes one item's entry on one server. Removing an
// entry that does not exist is a no-op, not an error — the owner's
// intent ("this title has no leaving date") already holds.
func (s *Store) RemoveLeaving(machine, ratingKey string) error {
	if machine == "" {
		return fmt.Errorf("no server selected")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadLeaving()
	if err != nil {
		return err
	}
	if per := all[machine]; per != nil {
		delete(per, ratingKey)
		if len(per) == 0 {
			delete(all, machine)
		}
	}
	return s.saveLeaving(all)
}

// SortedLeavingKeys returns a machine's entry ratingKeys in display
// order (date ascending, then title) — the manager's list order,
// shared by the API responses so every consumer renders the same.
func SortedLeavingKeys(entries map[string]LeavingEntry) []string {
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := entries[keys[i]], entries[keys[j]]
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		return a.Title < b.Title
	})
	return keys
}
