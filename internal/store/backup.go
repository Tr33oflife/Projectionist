package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ---- Settings backup & restore (v1.22) ---------------------------------------
//
// A settings backup is PORTABLE: it carries the user's choices to another
// machine. Anything tied to THIS machine or THIS login is excluded, even
// when a hand-edited backup file contains it — the import path drops those
// fields server-side (see the api backup handler, which reports the drop
// list). The exclusion list, kept in one place so export and import can
// never drift apart:
//
//   - "defaultMachine": the selected Plex server (server selection).
//   - "clientId": the per-install Plex client identity.
//   - Any key whose name mentions "token" or "session" (defensive net for
//     future auth-shaped keys; today none exist in settings.json).
//
// NOT in settings.json at all, hence never exported by this path:
//   - The plex.tv user token (token.enc.json) — account auth; the new
//     machine signs in again. It is never exported even with the keys
//     opt-in.
//   - Per-server access tokens — not stored anywhere; re-fetched from
//     plex.tv on every launch.
//   - The per-launch session token — in-memory only.
//   - Cached art, overlay backups, and the advisor API cache — machine
//     artifacts, not settings; they rebuild on the new machine.
//   - Advisor drafts and dismissals — they reference a specific server's
//     ratingKeys and suggestion ids, so they are not portable either.
//
// The donate username ("donateUsername") and its show/hide toggle
// ("donateEnabled") ARE portable by design: they are the user's own
// public choices, and they ride export/import like any other
// preference.

// MachineBoundSettings reports whether a settings.json key is tied to this
// machine or login and must never travel in a backup.
func MachineBoundSettings(key string) bool {
	if key == "defaultMachine" || key == "clientId" {
		return true
	}
	lower := strings.ToLower(key)
	return strings.Contains(lower, "token") || strings.Contains(lower, "session")
}

// PortableSettings returns a copy of the stored settings with every
// machine-bound key removed. An empty store yields an empty object.
func (s *Store) PortableSettings() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.getSettingsLocked()
	out := make(map[string]any, len(cur))
	for k, v := range cur {
		if !MachineBoundSettings(k) {
			out[k] = v
		}
	}
	return out
}

// ReplacePortableSettings replaces the portable key set with the given
// object: every currently stored portable key is removed first, then the
// given keys are applied. Machine-bound keys in the input are dropped and
// returned so the import path can report them. Machine-bound keys already
// stored are preserved untouched.
func (s *Store) ReplacePortableSettings(next map[string]any) (dropped []string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.getSettingsLocked()
	for k := range cur {
		if !MachineBoundSettings(k) {
			delete(cur, k)
		}
	}
	for k, v := range next {
		if MachineBoundSettings(k) {
			dropped = append(dropped, k)
			continue
		}
		cur[k] = v
	}
	if err := s.writeSettingsLocked(cur); err != nil {
		return dropped, err
	}
	return dropped, nil
}

// writeSettingsLocked writes the whole settings object as indented JSON.
// Callers hold s.mu.
func (s *Store) writeSettingsLocked(cur map[string]any) error {
	raw, err := json.MarshalIndent(cur, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, settingsFile), raw, 0o644)
}

// ---- Leaving Soon, whole-file access (v1.22) ---------------------------------

// LeavingAll returns a copy of the whole Leaving file: machine →
// ratingKey → entry. Used by backup export; a corrupt file is an error,
// never a silent empty list (same rule as loadLeaving's callers).
func (s *Store) LeavingAll() (map[string]map[string]LeavingEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadLeaving()
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]LeavingEntry, len(all))
	for m, per := range all {
		cp := make(map[string]LeavingEntry, len(per))
		for k, v := range per {
			cp[k] = v
		}
		out[m] = cp
	}
	return out, nil
}

// SetLeavingAll replaces the whole Leaving file with the given map after
// validating every entry: a non-empty ratingKey and a real calendar date
// in YYYY-MM-DD form. Nothing is written when validation fails.
func (s *Store) SetLeavingAll(all map[string]map[string]LeavingEntry) error {
	for m, per := range all {
		for k, e := range per {
			if k == "" {
				return fmt.Errorf("leaving entry on %q is missing its ratingKey", m)
			}
			if !ValidLeavingDate(e.Date) {
				return fmt.Errorf("leaving entry %q on %q has an invalid date %q", k, m, e.Date)
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLeaving(all)
}
