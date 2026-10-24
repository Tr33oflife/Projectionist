package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"plexcollectionmanager/internal/advisor"
	"plexcollectionmanager/internal/overlays"
	"plexcollectionmanager/internal/store"
)

// ---- Settings backup & restore (v1.22) --------------------------------------
//
// Export downloads one JSON document; import validates it fully BEFORE
// writing anything, then applies it. The format marker + version let
// import reject foreign files plainly instead of misreading them:
//
//	{
//	  "format": "projectionist-settings-backup", "formatVersion": 1,
//	  "appVersion": "1.22.0", "exportedAt": "2026-10-23T22:00:00Z",
//	  "settings": { ...portable settings.json keys... },
//	  "advisorSettings": { ... },
//	  "templates": { "Name": { ...template file... } },
//	  "leaving": { "machine": { "ratingKey": {"title": ..., "date": ...} } },
//	  "keys": { "tmdb": "...", ... }   // ONLY when exported with the opt-in
//	}
//
// Machine-bound state is NEVER in an export and is DROPPED on import even
// when a hand-edited file contains it (see store.machineBoundSettings and
// the exclusion list in internal/store/backup.go): server selection
// (defaultMachine), the per-install clientId, any token/session-shaped
// key, the plex.tv user token, per-server access tokens, the per-launch
// session token, cached art, overlay backups, the advisor API cache, and
// advisor drafts/dismissals (they reference a specific server's library).
//
// API keys: excluded from export by default. The export opt-in
// (includeKeys) adds the six named API secrets plus the Trakt OAuth
// bundle — all of them credential-shaped, all of them only ever included
// when the user explicitly asks. The plex.tv user token is never
// included, opt-in or not. On import, keys land ONLY when the file
// contains them AND the caller confirms (confirmKeys); without
// confirmation the existing keys are left untouched and the rest of the
// import still applies.

const (
	backupFormat        = "projectionist-settings-backup"
	backupFormatVersion = 1
	backupMaxBytes      = 5 << 20 // 5 MiB — templates + leaving dates are small; anything bigger is not a backup
)

// backupKeyFields maps export key-field names to store secret names.
// secretFor (services.go) maps the other direction for the six named API
// secrets; traktOAuth is the Trakt OAuth bundle, a credential of the same
// sensitivity class.
var backupKeyFields = map[string]string{
	"tmdb":              "tmdb:api-key",
	"traktClientId":     "trakt:client-id",
	"traktClientSecret": "trakt:client-secret",
	"omdb":              "omdb:api-key",
	"fanart":            "fanart:api-key",
	"aiKey":             "ai:api-key",
	"traktOAuth":        "trakt:oauth",
}

type backupDocument struct {
	Format          string                                   `json:"format"`
	FormatVersion   int                                      `json:"formatVersion"`
	AppVersion      string                                   `json:"appVersion"`
	ExportedAt      string                                   `json:"exportedAt"`
	Settings        map[string]any                           `json:"settings"`
	AdvisorSettings map[string]any                           `json:"advisorSettings"`
	Templates       map[string]json.RawMessage               `json:"templates"`
	Leaving         map[string]map[string]store.LeavingEntry `json:"leaving"`
	Keys            map[string]string                        `json:"keys,omitempty"`
}

// handleSettingsExportBackup assembles the backup document. The UI
// downloads the returned "backup" object as projectionist-settings-
// backup.json — the filename never advertises whether keys are inside.
func (s *Server) handleSettingsExportBackup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IncludeKeys bool `json:"includeKeys"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	doc := backupDocument{
		Format:        backupFormat,
		FormatVersion: backupFormatVersion,
		AppVersion:    s.cfg.Version,
		ExportedAt:    time.Now().UTC().Format(time.RFC3339),
		Settings:      s.store.PortableSettings(),
		Templates:     map[string]json.RawMessage{},
		Leaving:       map[string]map[string]store.LeavingEntry{},
	}
	if adv, err := s.advisorStore.GetSettings(); err == nil {
		raw, _ := json.Marshal(adv)
		_ = json.Unmarshal(raw, &doc.AdvisorSettings)
	}
	if tpl, err := s.templates.ExportAll(); err == nil {
		doc.Templates = tpl
	}
	if lv, err := s.store.LeavingAll(); err == nil {
		doc.Leaving = lv
	}
	if body.IncludeKeys {
		keys := map[string]string{}
		for field, secret := range backupKeyFields {
			if v := s.store.GetSecret(secret); v != "" {
				keys[field] = v
			}
		}
		if len(keys) > 0 {
			doc.Keys = keys
		}
	}
	ok(w, envelope{"backup": doc})
}

// handleSettingsImportBackup validates the whole document first, then
// applies it. Any validation failure changes nothing.
func (s *Server) handleSettingsImportBackup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Backup      json.RawMessage `json:"backup"`
		ConfirmKeys bool            `json:"confirmKeys"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	if len(body.Backup) == 0 {
		failMsg(w, "No backup document was provided.", "")
		return
	}
	if len(body.Backup) > backupMaxBytes {
		failMsg(w, "That file is too large to be a settings backup.", "")
		return
	}
	var doc backupDocument
	if err := json.Unmarshal(body.Backup, &doc); err != nil {
		failMsg(w, "That file is not valid JSON.", "")
		return
	}
	if doc.Format != backupFormat {
		failMsg(w, "That is not a Projectionist settings backup — its format marker is missing or different.", "")
		return
	}
	if doc.FormatVersion != backupFormatVersion {
		failMsg(w, fmt.Sprintf("That backup uses format version %d, which this build cannot read (it reads version %d).", doc.FormatVersion, backupFormatVersion), "")
		return
	}
	// ---- validate everything before writing anything --------------------
	for name := range doc.Templates {
		if _, err := overlays.ValidateTemplateRaw(doc.Templates[name]); err != nil {
			failMsg(w, "Template "+name+" could not be imported: "+err.Error(), "")
			return
		}
	}
	for m, per := range doc.Leaving {
		for k, e := range per {
			if k == "" {
				failMsg(w, fmt.Sprintf("A leaving entry on %q is missing its ratingKey.", m), "")
				return
			}
			if !store.ValidLeavingDate(e.Date) {
				failMsg(w, fmt.Sprintf("Leaving entry %q on %q has an invalid date %q.", k, m, e.Date), "")
				return
			}
		}
	}
	for field := range doc.Keys {
		if _, known := backupKeyFields[field]; !known {
			failMsg(w, "Unknown credential field in backup: "+field, "")
			return
		}
	}
	// A hand-edited backup must not smuggle a bad donate username into
	// the widget loader (the username is interpolated into a third-
	// party script call). The setter validates the same way, so a
	// genuine export can never trip this — only a tampered file does,
	// and it fails the whole import.
	if raw, present := doc.Settings["donateUsername"]; present {
		if s, isStr := raw.(string); !isStr || (s != "" && !validDonateUsername(s)) {
			failMsg(w, "The donate username in that backup is not valid — nothing was imported.", "")
			return
		}
	}
	var advSettings advisor.Settings
	if doc.AdvisorSettings != nil {
		raw, _ := json.Marshal(doc.AdvisorSettings)
		if err := json.Unmarshal(raw, &advSettings); err != nil {
			failMsg(w, "The advisor settings in that backup are not readable.", "")
			return
		}
	}

	// ---- apply ------------------------------------------------------------
	// A settings section carrying at least one portable key replaces
	// the portable key set — that is restore semantics, and it is what
	// makes a second export after import content-equivalent for the
	// portable fields. A section with NO portable keys (missing, empty,
	// or machine-bound-only) is a no-op for settings: wiping the user's
	// settings because a file happened to carry no portable keys would
	// be destructive, and a deliberate wipe is indistinguishable from a
	// file that merely predates a setting.
	settingsApplied := 0
	var dropped []string
	if doc.Settings != nil {
		portable := 0
		for k := range doc.Settings {
			if !store.MachineBoundSettings(k) {
				portable++
			}
		}
		if portable > 0 {
			var err error
			dropped, err = s.store.ReplacePortableSettings(doc.Settings)
			if err != nil {
				failErr(w, err)
				return
			}
			sort.Strings(dropped)
			settingsApplied = portable
		} else {
			// Still report the machine-bound keys that were dropped.
			for k := range doc.Settings {
				dropped = append(dropped, k)
			}
			sort.Strings(dropped)
		}
	}
	if doc.AdvisorSettings != nil {
		if _, err := s.advisorStore.SetSettingsStruct(advSettings); err != nil {
			failErr(w, err)
			return
		}
	}
	templatesImported := 0
	for _, raw := range doc.Templates {
		if _, err := s.templates.ImportRaw(raw); err != nil {
			failErr(w, err)
			return
		}
		templatesImported++
	}
	leavingDates := 0
	if doc.Leaving != nil {
		if err := s.store.SetLeavingAll(doc.Leaving); err != nil {
			failErr(w, err)
			return
		}
		for _, per := range doc.Leaving {
			leavingDates += len(per)
		}
	}
	keysImported := 0
	keysSkipped := false
	if len(doc.Keys) > 0 {
		if body.ConfirmKeys {
			for field, v := range doc.Keys {
				if v == "" {
					continue // an empty value never clears an existing key on import
				}
				if err := s.store.SetSecret(backupKeyFields[field], v); err != nil {
					failErr(w, err)
					return
				}
				keysImported++
			}
		} else {
			keysSkipped = true
		}
	}
	ok(w, envelope{
		"settingsApplied":   settingsApplied,
		"advisorApplied":    doc.AdvisorSettings != nil,
		"templatesImported": templatesImported,
		"leavingDates":      leavingDates,
		"keysImported":      keysImported,
		"keysSkipped":       keysSkipped,
		"dropped":           dropped,
	})
}

// ---- Donate button (v1.22 rider, corrected twice) ---------------------------
//
// The owner supplied the exact implementation: the Ko-fi overlay widget
// (floating-chat donate button). There is NO toggle — his call: the
// widget simply appears when there's internet and doesn't when there
// isn't. On app load, when a Ko-fi username is set, the page attempts
// to load https://storage.ko-fi.com/cdn/scripts/overlay-widget.js and,
// on success, calls kofiWidgetOverlay.draw with his exact config
// (floating-chat, "Donate", #323842 background, white text). On load
// failure (offline, CDN unreachable) it fails SILENTLY: no widget, no
// error toast, no broken UI. The username persists as the portable
// "donateUsername" setting, so it rides backup export/import like any
// other preference; the field is prefilled with the author's own public
// Ko-fi identity, supplied by him for this purpose — a default for
// forkers to replace, not a leak of private infrastructure.
//
// The username charset is validated server-side (and on import): Ko-fi
// usernames are letters, digits, hyphens and underscores. The value is
// interpolated into a third-party script call, so anything else is
// rejected rather than escaped-and-hoped.

var donateUsernameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func validDonateUsername(v string) bool {
	return donateUsernameRe.MatchString(v)
}

// donateEnabledSetting reports the stored "Show donate button"
// toggle, defaulting to ON when nothing is stored yet (v1.23 toggle;
// v1.22 installs predate it, and their behavior was always-on).
// A stored non-boolean is treated as ON rather than erroring — the
// toggle is display-only, never a security gate.
func donateEnabledSetting(st *store.Store) bool {
	v, ok := st.GetSettings()["donateEnabled"]
	if !ok {
		return true
	}
	b, isBool := v.(bool)
	return !isBool || b
}

func (s *Server) handleMetaSetDonate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Enabled  *bool  `json:"enabled"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	name := strings.TrimSpace(body.Username)
	if name != "" && !validDonateUsername(name) {
		failMsg(w, "A Ko-fi username uses only letters, numbers, hyphens and underscores.", "")
		return
	}
	patch := map[string]any{"donateUsername": name}
	if body.Enabled != nil {
		patch["donateEnabled"] = *body.Enabled
	}
	if err := s.store.SetSettings(patch); err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"username": name, "enabled": donateEnabledSetting(s.store)})
}
