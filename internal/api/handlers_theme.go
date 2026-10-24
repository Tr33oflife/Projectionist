package api

import (
	"net/http"
	"regexp"
)

// ---- appearance / themes (v1.19) -------------------------------------------
//
// The theme choice and the user's custom palette live in the plain
// settings store (settings.json keys uiTheme / uiThemeCustom) — the
// same store as the other non-secret settings, so they survive
// restarts and travel with the install. The renderer applies themes
// from its own built-in registry; the server never interprets colors,
// it only stores them — but it validates what it stores so a corrupt
// or hand-edited write can never put the UI into a half-themed state:
// the name must be a known theme and a custom palette must be
// COMPLETE (every variable in themeVars) with color-shaped values.
//
// themeVars mirrors THEME_VARS in web/themes.js — keep them in sync.

var themeNames = map[string]bool{
	"projectionist": true,
	"midnight":      true,
	"cinema":        true,
	"light":         true,
	"contrast":      true,
	"plex":          true,
	"custom":        true,
}

var themeVars = []string{
	"--bg-0", "--bg-1", "--sidebar-bg", "--panel", "--panel-strong", "--panel-2",
	"--surface-solid", "--surface-2", "--inset", "--input-bg", "--toast-bg",
	"--border", "--border-soft", "--border-solid",
	"--text", "--muted", "--faint",
	"--amber", "--amber-deep", "--amber-bright", "--accent-ink", "--accent-ink-2",
	"--violet", "--violet-text",
	"--green", "--green-ink", "--green-ink-2", "--red", "--red-text", "--blue",
	"--warn", "--warn-text", "--ok-text", "--err-text",
	"--focus", "--backdrop", "--scroll", "--scroll-hover", "--track",
	"--hover", "--hover-soft", "--fill",
	"--glow-1", "--glow-2", "--vignette",
	"--ph-1", "--ph-2", "--ph-3", "--ph-4", "--ph-text", "--ph-text-2",
	"--chip-bg", "--chip-bg-hover", "--photo-chip", "--photo-chip-2",
}

var themeColorRe = regexp.MustCompile(`^(#[0-9a-fA-F]{3}|#[0-9a-fA-F]{6}|#[0-9a-fA-F]{8}|rgba?\(\s*\d{1,3}\s*,\s*\d{1,3}\s*,\s*\d{1,3}\s*(,\s*(0|1|0?\.\d+)\s*)?\))$`)

// validateThemePalette returns "" for a complete, safe palette, else
// a plain-language reason. Mirrors validatePalette in themes.js.
func validateThemePalette(vars map[string]any) string {
	allowed := make(map[string]bool, len(themeVars))
	for _, v := range themeVars {
		allowed[v] = true
	}
	for k := range vars {
		if !allowed[k] {
			return "unknown variable " + k
		}
	}
	for _, v := range themeVars {
		raw, present := vars[v]
		if !present {
			return "the palette is incomplete — missing " + v
		}
		s, isStr := raw.(string)
		if !isStr || !themeColorRe.MatchString(s) {
			return v + " is not a color value"
		}
	}
	return ""
}

// handleThemeGet answers { name, custom } — the stored theme choice
// ("projectionist" when nothing was ever saved) and the saved custom
// palette (nil when none). A stored palette that no longer validates
// (e.g. hand-edited store, older build) is reported as nil, never
// served half-broken.
func (s *Server) handleThemeGet(w http.ResponseWriter, r *http.Request) {
	settings := s.store.GetSettings()
	name, _ := settings["uiTheme"].(string)
	if !themeNames[name] {
		name = "projectionist"
	}
	var custom map[string]any
	if raw, ok := settings["uiThemeCustom"].(map[string]any); ok && validateThemePalette(raw) == "" {
		custom = raw
	}
	ok(w, envelope{"name": name, "custom": custom})
}

// handleThemeSet stores { name, custom? }. Validation is whole-write:
// a rejected write changes nothing.
func (s *Server) handleThemeSet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string         `json:"name"`
		Custom map[string]any `json:"custom"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	if !themeNames[body.Name] {
		failMsg(w, "unknown theme name", "")
		return
	}
	patch := map[string]any{"uiTheme": body.Name}
	if body.Custom != nil {
		if reason := validateThemePalette(body.Custom); reason != "" {
			failMsg(w, "custom palette rejected: "+reason, "")
			return
		}
		patch["uiThemeCustom"] = body.Custom
	} else if body.Name == "custom" {
		// Choosing "custom" without supplying a palette is only valid
		// when one is already stored.
		stored, _ := s.store.GetSettings()["uiThemeCustom"].(map[string]any)
		if validateThemePalette(stored) != "" {
			failMsg(w, "no saved custom palette to apply", "")
			return
		}
	}
	if err := s.store.SetSettings(patch); err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{})
}
