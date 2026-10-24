'use strict';

/* ============================================================================
 * themes.js — the Appearance system / theme manager (v1.19).
 * ----------------------------------------------------------------------------
 * FUTURE AI — how theming works here:
 *   - The UI palette lives in CSS custom properties (styles.css :root
 *     holds the default theme's values). A theme is nothing but a
 *     COMPLETE set of values for THEME_VARS below, applied as inline
 *     properties on <html> — no reload, no re-render, so in-flight
 *     state (Builder draft, Advisor scan) is untouched by a switch.
 *   - Built-ins: projectionist (the shipped default — its values ARE
 *     the :root values), midnight, cinema, light, contrast, plex
 *     (a color homage — colors only, no third-party artwork/names in
 *     the artwork sense; the name is the owner's label for the card).
 *   - The user's choice + custom palette persist SERVER-SIDE in the
 *     app settings store (theme/get + theme/set bridge routes) — the
 *     store is the source of truth. A localStorage mirror
 *     ('pcm-theme-v1', the resolved palette) exists only so the
 *     inline pre-paint script in index.html can apply the theme
 *     before first paint; on load the server value wins and the
 *     mirror is rewritten.
 *   - The overlay badge engine (composite.js families/styles) is NOT
 *     themed — badge art is content, not UI chrome.
 *   - THEME_VARS is mirrored by the Go validator in
 *     internal/api/handlers_theme.go — keep the two lists in sync;
 *     the API tests and the DOM harness both pin completeness.
 * ========================================================================== */
(() => {
  const CACHE_KEY = 'pcm-theme-v1';

  // Canonical palette: every var a complete theme must define.
  const THEME_VARS = [
    '--bg-0', '--bg-1', '--sidebar-bg', '--panel', '--panel-strong', '--panel-2',
    '--surface-solid', '--surface-2', '--inset', '--input-bg', '--toast-bg',
    '--border', '--border-soft', '--border-solid',
    '--text', '--muted', '--faint',
    '--amber', '--amber-deep', '--amber-bright', '--accent-ink', '--accent-ink-2',
    '--violet', '--violet-text',
    '--green', '--green-ink', '--green-ink-2', '--red', '--red-text', '--blue',
    '--warn', '--warn-text', '--ok-text', '--err-text',
    '--focus', '--backdrop', '--scroll', '--scroll-hover', '--track',
    '--hover', '--hover-soft', '--fill',
    '--glow-1', '--glow-2', '--vignette',
    '--ph-1', '--ph-2', '--ph-3', '--ph-4', '--ph-text', '--ph-text-2',
    '--chip-bg', '--chip-bg-hover', '--photo-chip', '--photo-chip-2',
  ];

  // The custom editor's rows (var → label). Editing applies the picked
  // color as an opaque hex; untouched vars keep their seeded values.
  const EDITABLE = [
    ['--bg-1', 'App background'],
    ['--bg-0', 'Deep background'],
    ['--panel', 'Panel'],
    ['--border', 'Border'],
    ['--text', 'Text'],
    ['--muted', 'Muted text'],
    ['--faint', 'Faint text'],
    ['--amber', 'Accent'],
    ['--amber-deep', 'Accent (deep)'],
    ['--red', 'Danger'],
  ];

  const BUILTINS = {
    // The shipped default. Values are exactly styles.css's :root —
    // the DOM harness pins the two against each other.
    projectionist: {
      label: 'Projectionist', sub: 'The shipped default — indigo night, amber accent.', scheme: 'dark',
      vars: {
        '--bg-0': '#070912', '--bg-1': '#0b0d16', '--sidebar-bg': 'rgba(10, 12, 22, 0.72)',
        '--panel': 'rgba(255, 255, 255, 0.035)', '--panel-strong': 'rgba(255, 255, 255, 0.06)', '--panel-2': 'rgba(255, 255, 255, 0.03)',
        '--surface-solid': '#121627', '--surface-2': '#141828', '--inset': 'rgba(0, 0, 0, 0.28)',
        '--input-bg': 'rgba(0, 0, 0, 0.35)', '--toast-bg': 'rgba(18, 22, 39, 0.96)',
        '--border': 'rgba(255, 255, 255, 0.09)', '--border-soft': 'rgba(255, 255, 255, 0.055)', '--border-solid': '#303545',
        '--text': '#f2f0ff', '--muted': '#9aa0b8', '--faint': '#6b7190',
        '--amber': '#f5a524', '--amber-deep': '#e07b00', '--amber-bright': '#ffb63d',
        '--accent-ink': '#1c1204', '--accent-ink-2': '#1a1206',
        '--violet': '#8b5cf6', '--violet-text': '#d9c8ff',
        '--green': '#34d399', '--green-ink': '#06270f', '--green-ink-2': '#0b2e1f',
        '--red': '#f87171', '--red-text': '#fda4af', '--blue': '#60a5fa',
        '--warn': '#ffaa3c', '--warn-text': '#ffd9a0', '--ok-text': '#7dd87d', '--err-text': '#ff9a9a',
        '--focus': 'rgba(245, 165, 36, 0.7)', '--backdrop': 'rgba(4, 6, 12, 0.7)',
        '--scroll': 'rgba(255, 255, 255, 0.12)', '--scroll-hover': 'rgba(255, 255, 255, 0.2)',
        '--track': 'rgba(255, 255, 255, 0.07)', '--hover': 'rgba(255, 255, 255, 0.09)',
        '--hover-soft': 'rgba(255, 255, 255, 0.05)', '--fill': 'rgba(255, 255, 255, 0.04)',
        '--glow-1': 'rgba(139, 92, 246, 0.16)', '--glow-2': 'rgba(245, 165, 36, 0.10)', '--vignette': 'rgba(0, 0, 0, 0.5)',
        '--ph-1': '#232a44', '--ph-2': '#12162a', '--ph-3': '#1b2033', '--ph-4': '#101424',
        '--ph-text': 'rgba(255, 255, 255, 0.16)', '--ph-text-2': 'rgba(255, 255, 255, 0.25)',
        '--chip-bg': 'rgba(8, 10, 18, 0.82)', '--chip-bg-hover': 'rgba(8, 10, 18, 0.96)',
        '--photo-chip': 'rgba(0, 0, 0, 0.65)', '--photo-chip-2': 'rgba(0, 0, 0, 0.72)',
      },
    },
    midnight: {
      label: 'Midnight', sub: 'Deep blue — navy surfaces, sky accent.', scheme: 'dark',
      vars: {
        '--bg-0': '#04070f', '--bg-1': '#060b18', '--sidebar-bg': 'rgba(5, 10, 22, 0.78)',
        '--panel': 'rgba(147, 184, 255, 0.05)', '--panel-strong': 'rgba(147, 184, 255, 0.09)', '--panel-2': 'rgba(147, 184, 255, 0.04)',
        '--surface-solid': '#0a1322', '--surface-2': '#0d1830', '--inset': 'rgba(2, 6, 16, 0.4)',
        '--input-bg': 'rgba(2, 6, 16, 0.45)', '--toast-bg': 'rgba(8, 15, 30, 0.97)',
        '--border': 'rgba(147, 184, 255, 0.14)', '--border-soft': 'rgba(147, 184, 255, 0.08)', '--border-solid': '#27354f',
        '--text': '#e8efff', '--muted': '#93a5c9', '--faint': '#64769c',
        '--amber': '#4da3ff', '--amber-deep': '#2f6fdd', '--amber-bright': '#7cc0ff',
        '--accent-ink': '#04122b', '--accent-ink-2': '#03101f',
        '--violet': '#38bdf8', '--violet-text': '#bae6fd',
        '--green': '#34d399', '--green-ink': '#06270f', '--green-ink-2': '#0b2e1f',
        '--red': '#fb7185', '--red-text': '#fda4af', '--blue': '#60a5fa',
        '--warn': '#fbbf24', '--warn-text': '#fde68a', '--ok-text': '#7dd87d', '--err-text': '#ff9a9a',
        '--focus': 'rgba(77, 163, 255, 0.75)', '--backdrop': 'rgba(2, 4, 10, 0.72)',
        '--scroll': 'rgba(147, 184, 255, 0.16)', '--scroll-hover': 'rgba(147, 184, 255, 0.28)',
        '--track': 'rgba(147, 184, 255, 0.09)', '--hover': 'rgba(147, 184, 255, 0.1)',
        '--hover-soft': 'rgba(147, 184, 255, 0.06)', '--fill': 'rgba(147, 184, 255, 0.05)',
        '--glow-1': 'rgba(37, 99, 235, 0.22)', '--glow-2': 'rgba(56, 189, 248, 0.12)', '--vignette': 'rgba(0, 0, 5, 0.55)',
        '--ph-1': '#16294d', '--ph-2': '#0a1428', '--ph-3': '#12203c', '--ph-4': '#081020',
        '--ph-text': 'rgba(210, 228, 255, 0.2)', '--ph-text-2': 'rgba(210, 228, 255, 0.3)',
        '--chip-bg': 'rgba(3, 7, 16, 0.85)', '--chip-bg-hover': 'rgba(3, 7, 16, 0.97)',
        '--photo-chip': 'rgba(0, 0, 0, 0.65)', '--photo-chip-2': 'rgba(0, 0, 0, 0.72)',
      },
    },
    cinema: {
      label: 'Cinema', sub: 'Warm bronze and amber on near-black — the picture palace.', scheme: 'dark',
      vars: {
        '--bg-0': '#0a0705', '--bg-1': '#0e0a06', '--sidebar-bg': 'rgba(16, 11, 6, 0.8)',
        '--panel': 'rgba(255, 196, 110, 0.045)', '--panel-strong': 'rgba(255, 196, 110, 0.08)', '--panel-2': 'rgba(255, 196, 110, 0.035)',
        '--surface-solid': '#171008', '--surface-2': '#1d1409', '--inset': 'rgba(0, 0, 0, 0.4)',
        '--input-bg': 'rgba(0, 0, 0, 0.45)', '--toast-bg': 'rgba(24, 17, 9, 0.97)',
        '--border': 'rgba(255, 196, 110, 0.13)', '--border-soft': 'rgba(255, 196, 110, 0.07)', '--border-solid': '#453219',
        '--text': '#f7ead2', '--muted': '#c2a87e', '--faint': '#8a7350',
        '--amber': '#d9903f', '--amber-deep': '#a8641c', '--amber-bright': '#f0b269',
        '--accent-ink': '#201304', '--accent-ink-2': '#1c1002',
        '--violet': '#b04a5a', '--violet-text': '#f3c1c9',
        '--green': '#7fb069', '--green-ink': '#12240a', '--green-ink-2': '#16290c',
        '--red': '#e05d44', '--red-text': '#f5a08e', '--blue': '#7d9dbd',
        '--warn': '#e8a33d', '--warn-text': '#f7cf8b', '--ok-text': '#a9c97e', '--err-text': '#f0957e',
        '--focus': 'rgba(217, 144, 63, 0.75)', '--backdrop': 'rgba(5, 3, 1, 0.72)',
        '--scroll': 'rgba(255, 214, 150, 0.14)', '--scroll-hover': 'rgba(255, 214, 150, 0.26)',
        '--track': 'rgba(255, 214, 150, 0.08)', '--hover': 'rgba(255, 214, 150, 0.1)',
        '--hover-soft': 'rgba(255, 214, 150, 0.055)', '--fill': 'rgba(255, 214, 150, 0.045)',
        '--glow-1': 'rgba(217, 144, 63, 0.14)', '--glow-2': 'rgba(176, 74, 90, 0.10)', '--vignette': 'rgba(0, 0, 0, 0.6)',
        '--ph-1': '#33240f', '--ph-2': '#191006', '--ph-3': '#2a1d0d', '--ph-4': '#120b04',
        '--ph-text': 'rgba(255, 230, 190, 0.2)', '--ph-text-2': 'rgba(255, 230, 190, 0.3)',
        '--chip-bg': 'rgba(12, 8, 3, 0.85)', '--chip-bg-hover': 'rgba(12, 8, 3, 0.97)',
        '--photo-chip': 'rgba(0, 0, 0, 0.65)', '--photo-chip-2': 'rgba(0, 0, 0, 0.72)',
      },
    },
    light: {
      label: 'Light', sub: 'A genuine light theme — paper surfaces, ink text.', scheme: 'light',
      vars: {
        '--bg-0': '#e8eaf1', '--bg-1': '#f4f5f9', '--sidebar-bg': 'rgba(255, 255, 255, 0.82)',
        '--panel': 'rgba(23, 28, 54, 0.045)', '--panel-strong': 'rgba(23, 28, 54, 0.08)', '--panel-2': 'rgba(23, 28, 54, 0.03)',
        '--surface-solid': '#ffffff', '--surface-2': '#eceef5', '--inset': 'rgba(23, 28, 54, 0.05)',
        '--input-bg': '#ffffff', '--toast-bg': 'rgba(255, 255, 255, 0.97)',
        '--border': 'rgba(23, 28, 54, 0.16)', '--border-soft': 'rgba(23, 28, 54, 0.1)', '--border-solid': '#c3c9d8',
        '--text': '#171c30', '--muted': '#4d5570', '--faint': '#78809a',
        '--amber': '#d97706', '--amber-deep': '#b45309', '--amber-bright': '#f59e0b',
        '--accent-ink': '#241300', '--accent-ink-2': '#241300',
        '--violet': '#7c3aed', '--violet-text': '#5b21b6',
        '--green': '#059669', '--green-ink': '#ffffff', '--green-ink-2': '#ffffff',
        '--red': '#dc2626', '--red-text': '#b91c1c', '--blue': '#2563eb',
        '--warn': '#b45309', '--warn-text': '#92400e', '--ok-text': '#047857', '--err-text': '#b91c1c',
        '--focus': 'rgba(217, 119, 6, 0.8)', '--backdrop': 'rgba(30, 34, 54, 0.45)',
        '--scroll': 'rgba(23, 28, 54, 0.22)', '--scroll-hover': 'rgba(23, 28, 54, 0.35)',
        '--track': 'rgba(23, 28, 54, 0.08)', '--hover': 'rgba(23, 28, 54, 0.07)',
        '--hover-soft': 'rgba(23, 28, 54, 0.045)', '--fill': 'rgba(23, 28, 54, 0.04)',
        '--glow-1': 'rgba(124, 58, 237, 0.08)', '--glow-2': 'rgba(245, 165, 36, 0.1)', '--vignette': 'rgba(60, 66, 96, 0.1)',
        '--ph-1': '#d5d9e6', '--ph-2': '#b9c0d4', '--ph-3': '#c9cede', '--ph-4': '#aeb6cc',
        '--ph-text': 'rgba(23, 28, 54, 0.3)', '--ph-text-2': 'rgba(23, 28, 54, 0.4)',
        '--chip-bg': 'rgba(20, 24, 40, 0.82)', '--chip-bg-hover': 'rgba(20, 24, 40, 0.95)',
        '--photo-chip': 'rgba(0, 0, 0, 0.6)', '--photo-chip-2': 'rgba(0, 0, 0, 0.68)',
      },
    },
    contrast: {
      label: 'High contrast', sub: 'Black, white and signal yellow — maximum legibility.', scheme: 'dark',
      vars: {
        '--bg-0': '#000000', '--bg-1': '#000000', '--sidebar-bg': 'rgba(0, 0, 0, 0.95)',
        '--panel': 'rgba(255, 255, 255, 0.07)', '--panel-strong': 'rgba(255, 255, 255, 0.14)', '--panel-2': 'rgba(255, 255, 255, 0.05)',
        '--surface-solid': '#0a0a0a', '--surface-2': '#141414', '--inset': 'rgba(255, 255, 255, 0.06)',
        '--input-bg': '#000000', '--toast-bg': 'rgba(0, 0, 0, 0.98)',
        '--border': 'rgba(255, 255, 255, 0.55)', '--border-soft': 'rgba(255, 255, 255, 0.35)', '--border-solid': '#8a8a8a',
        '--text': '#ffffff', '--muted': '#e8e8e8', '--faint': '#c4c4c4',
        '--amber': '#ffd60a', '--amber-deep': '#ffb700', '--amber-bright': '#ffe14d',
        '--accent-ink': '#000000', '--accent-ink-2': '#000000',
        '--violet': '#c4b5fd', '--violet-text': '#ddd6fe',
        '--green': '#4ade80', '--green-ink': '#000000', '--green-ink-2': '#000000',
        '--red': '#ff6b6b', '--red-text': '#ffb3b3', '--blue': '#7cc7ff',
        '--warn': '#ffb020', '--warn-text': '#ffd88a', '--ok-text': '#86efac', '--err-text': '#fca5a5',
        '--focus': '#ffffff', '--backdrop': 'rgba(0, 0, 0, 0.85)',
        '--scroll': 'rgba(255, 255, 255, 0.45)', '--scroll-hover': 'rgba(255, 255, 255, 0.65)',
        '--track': 'rgba(255, 255, 255, 0.16)', '--hover': 'rgba(255, 255, 255, 0.14)',
        '--hover-soft': 'rgba(255, 255, 255, 0.1)', '--fill': 'rgba(255, 255, 255, 0.08)',
        '--glow-1': 'rgba(255, 214, 10, 0.06)', '--glow-2': 'rgba(255, 214, 10, 0.05)', '--vignette': 'rgba(0, 0, 0, 0.7)',
        '--ph-1': '#2a2a2a', '--ph-2': '#111111', '--ph-3': '#222222', '--ph-4': '#0d0d0d',
        '--ph-text': 'rgba(255, 255, 255, 0.45)', '--ph-text-2': 'rgba(255, 255, 255, 0.55)',
        '--chip-bg': 'rgba(0, 0, 0, 0.9)', '--chip-bg-hover': 'rgba(0, 0, 0, 0.98)',
        '--photo-chip': 'rgba(0, 0, 0, 0.8)', '--photo-chip-2': 'rgba(0, 0, 0, 0.85)',
      },
    },
    // A color homage to a certain media server's web UI: charcoal
    // surfaces in the #1a–#28 range, warm-gray text, its signature
    // orange as the accent. Colors only — no logos or artwork.
    plex: {
      label: 'Plex', sub: 'Charcoal and warm gray, with that familiar orange.', scheme: 'dark',
      vars: {
        '--bg-0': '#141414', '--bg-1': '#1f1f1f', '--sidebar-bg': 'rgba(24, 24, 24, 0.92)',
        '--panel': 'rgba(255, 255, 255, 0.055)', '--panel-strong': 'rgba(255, 255, 255, 0.09)', '--panel-2': 'rgba(255, 255, 255, 0.045)',
        '--surface-solid': '#282828', '--surface-2': '#2e2e2e', '--inset': 'rgba(0, 0, 0, 0.25)',
        '--input-bg': 'rgba(0, 0, 0, 0.3)', '--toast-bg': 'rgba(40, 40, 40, 0.98)',
        '--border': 'rgba(255, 255, 255, 0.1)', '--border-soft': 'rgba(255, 255, 255, 0.06)', '--border-solid': '#454545',
        '--text': '#e8e6e3', '--muted': '#b3b0ab', '--faint': '#85827d',
        '--amber': '#e5a00d', '--amber-deep': '#c07f02', '--amber-bright': '#f5b81f',
        '--accent-ink': '#1a1206', '--accent-ink-2': '#1a1206',
        '--violet': '#a39e93', '--violet-text': '#d8d4cc',
        '--green': '#5ea244', '--green-ink': '#0e2405', '--green-ink-2': '#122b06',
        '--red': '#e05252', '--red-text': '#f2a49d', '--blue': '#6f9fd8',
        '--warn': '#e5a00d', '--warn-text': '#f5c65c', '--ok-text': '#93c47d', '--err-text': '#efa198',
        '--focus': 'rgba(229, 160, 13, 0.75)', '--backdrop': 'rgba(10, 10, 10, 0.7)',
        '--scroll': 'rgba(255, 255, 255, 0.14)', '--scroll-hover': 'rgba(255, 255, 255, 0.24)',
        '--track': 'rgba(255, 255, 255, 0.08)', '--hover': 'rgba(255, 255, 255, 0.09)',
        '--hover-soft': 'rgba(255, 255, 255, 0.05)', '--fill': 'rgba(255, 255, 255, 0.045)',
        '--glow-1': 'rgba(229, 160, 13, 0.05)', '--glow-2': 'rgba(229, 160, 13, 0.04)', '--vignette': 'rgba(0, 0, 0, 0.35)',
        '--ph-1': '#3a3a3a', '--ph-2': '#232323', '--ph-3': '#333333', '--ph-4': '#1c1c1c',
        '--ph-text': 'rgba(255, 255, 255, 0.22)', '--ph-text-2': 'rgba(255, 255, 255, 0.32)',
        '--chip-bg': 'rgba(15, 15, 15, 0.85)', '--chip-bg-hover': 'rgba(15, 15, 15, 0.97)',
        '--photo-chip': 'rgba(0, 0, 0, 0.65)', '--photo-chip-2': 'rgba(0, 0, 0, 0.72)',
      },
    },
  };
  const BUILTIN_ORDER = ['projectionist', 'midnight', 'cinema', 'light', 'contrast', 'plex'];
  const SWATCH_VARS = ['--bg-1', '--surface-solid', '--amber', '--violet', '--text'];

  // ---- state ---------------------------------------------------------------
  let curName = 'projectionist';
  let curVars = { ...BUILTINS.projectionist.vars };
  let curScheme = BUILTINS.projectionist.scheme;
  let savedCustom = null;   // palette persisted in the settings store (or null)
  let draftCustom = null;   // palette shown in the editor (seeded lazily)

  const COLOR_RE = /^(?:#[0-9a-fA-F]{3}|#[0-9a-fA-F]{6}|#[0-9a-fA-F]{8}|rgba?\(\s*\d{1,3}\s*,\s*\d{1,3}\s*,\s*\d{1,3}\s*(?:,\s*(?:0|1|0?\.\d+)\s*)?\))$/;
  const HEX_RE = /^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/;

  // validatePalette returns null when `vars` is a complete, safe
  // palette (every THEME_VARS key, color-shaped string values), else
  // a plain-language error. Shared by import and the server sync.
  function validatePalette(vars) {
    if (!vars || typeof vars !== 'object' || Array.isArray(vars)) return 'the theme has no palette object';
    for (const k of Object.keys(vars)) {
      if (!THEME_VARS.includes(k)) return `unknown variable ${k}`;
    }
    const missing = THEME_VARS.filter((v) => !(v in vars));
    if (missing.length) return `the palette is incomplete — ${missing.length} variable${missing.length === 1 ? ' is' : 's are'} missing (first: ${missing[0]})`;
    for (const v of THEME_VARS) {
      if (typeof vars[v] !== 'string' || !COLOR_RE.test(vars[v].trim())) return `${v} is not a color value`;
    }
    return null;
  }

  function schemeOf(name, vars) {
    if (name !== 'custom') return (BUILTINS[name] || BUILTINS.projectionist).scheme;
    return schemeOfVars(vars || curVars);
  }
  // A custom palette's scheme is inferred from its background's
  // luminance — dark text on a light background needs light chrome
  // (native scrollbars, form controls) and vice versa.
  function schemeOfVars(vars) {
    const rgb = parseColor(vars['--bg-1']);
    if (!rgb) return 'dark';
    const lum = (0.2126 * rgb[0] + 0.7152 * rgb[1] + 0.0722 * rgb[2]) / 255;
    return lum > 0.5 ? 'light' : 'dark';
  }
  function parseColor(value) {
    const s = String(value || '').trim();
    let m = s.match(/^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/);
    if (m) {
      let h = m[1];
      if (h.length === 3) h = h.split('').map((c) => c + c).join('');
      return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)];
    }
    m = s.match(/^rgba?\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})/);
    if (m) return [+m[1], +m[2], +m[3]];
    return null;
  }
  function toHexInput(value) {
    const rgb = parseColor(value);
    if (!rgb) return '#000000';
    return '#' + rgb.map((n) => n.toString(16).padStart(2, '0')).join('');
  }

  // ---- application -----------------------------------------------------------
  function applyVars(vars, scheme) {
    const el = document.documentElement;
    for (const v of THEME_VARS) el.style.setProperty(v, vars[v]);
    el.style.colorScheme = scheme || 'dark';
  }
  function writeCache() {
    try {
      localStorage.setItem(CACHE_KEY, JSON.stringify({ name: curName, vars: curVars, scheme: curScheme }));
    } catch { /* private mode etc. — the server store still holds it */ }
  }
  // applyCached replays the localStorage mirror (the index.html
  // pre-paint script does the same inline; this covers any load
  // where that script could not run).
  function applyCached() {
    try {
      const c = JSON.parse(localStorage.getItem(CACHE_KEY) || 'null');
      if (c && c.vars && !validatePalette(c.vars)) {
        curName = typeof c.name === 'string' ? c.name : 'custom';
        curVars = { ...c.vars };
        curScheme = c.scheme || schemeOfVars(curVars);
        applyVars(curVars, curScheme);
        return true;
      }
    } catch { /* fall through to the default */ }
    return false;
  }

  // setTheme makes `name` (+ palette for custom) the live theme,
  // updates the mirror, refreshes the manager UI, and persists to
  // the settings store unless opts.persist === false (the server
  // sync itself must not echo a write back).
  async function setTheme(name, vars, opts = {}) {
    if (name === 'custom') {
      const err = validatePalette(vars);
      if (err) { setStatus('Custom theme not applied: ' + err + '.', true); return false; }
      curVars = { ...vars };
    } else if (BUILTINS[name]) {
      curVars = { ...BUILTINS[name].vars };
    } else {
      return false;
    }
    curName = name;
    curScheme = schemeOf(name, curVars);
    applyVars(curVars, curScheme);
    writeCache();
    renderAppearance();
    if (opts.persist !== false && window.pcm && window.pcm.themeSet) {
      const r = await window.pcm.themeSet(name === 'custom' ? { name, custom: curVars } : { name });
      if (!r || !r.ok) { setStatus('Theme applied, but saving it failed — it will not survive a restart.', true); return true; }
      if (name === 'custom') savedCustom = { ...curVars };
    }
    return true;
  }

  // syncFromServer: the settings store is the source of truth. Called
  // once at load; on success the mirror is rewritten from the server
  // value even when it disagrees with the cache.
  async function syncFromServer() {
    if (!window.pcm || !window.pcm.themeGet) return;
    let r;
    try { r = await window.pcm.themeGet(); } catch { return; }
    if (!r || !r.ok) return;
    if (r.custom && !validatePalette(r.custom)) savedCustom = { ...r.custom };
    if (r.name === 'custom' && savedCustom) {
      draftCustom = { ...savedCustom };
      await setTheme('custom', savedCustom, { persist: false });
    } else if (BUILTINS[r.name]) {
      await setTheme(r.name, null, { persist: false });
    }
  }

  // ---- export / import ---------------------------------------------------------
  function exportJSON() {
    ensureDraft();
    return JSON.stringify({ app: 'projectionist-theme', version: 1, name: 'Custom', vars: draftCustom }, null, 2);
  }
  // importJSON validates and, on success, saves + applies the palette
  // as the custom theme. On any failure the live theme is untouched.
  async function importJSON(text) {
    let obj;
    try { obj = JSON.parse(String(text || '')); } catch { return { ok: false, error: 'That is not valid JSON.' }; }
    const vars = obj && typeof obj === 'object' ? (obj.vars && typeof obj.vars === 'object' ? obj.vars : obj) : null;
    const err = validatePalette(vars);
    if (err) return { ok: false, error: 'Theme not imported: ' + err + '.' };
    draftCustom = { ...vars };
    const applied = await setTheme('custom', draftCustom);
    if (!applied) return { ok: false, error: 'Theme not imported: the palette could not be applied.' };
    return { ok: true };
  }

  // ---- Appearance UI (Settings tab) ---------------------------------------------
  const $id = (id) => document.getElementById(id);
  const escHtml = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  function setStatus(msg, isErr) {
    const el = $id('theme-status');
    if (!el) return;
    el.textContent = msg || '';
    el.style.color = isErr ? 'var(--err-text)' : '';
  }
  function ensureDraft() {
    if (!draftCustom) draftCustom = { ...(savedCustom || curVars) };
    return draftCustom;
  }
  function swatches(vars) {
    return `<span class="theme-swatches">${SWATCH_VARS.map((v) =>
      `<span style="background:${escHtml(vars[v] || '')}"></span>`).join('')}</span>`;
  }
  function renderAppearance() {
    const cards = $id('theme-cards');
    if (!cards) return;
    ensureDraft();
    const customSub = savedCustom
      ? (JSON.stringify(draftCustom) === JSON.stringify(savedCustom) ? 'Your saved palette.' : 'Unsaved edits — Save to keep them.')
      : 'Not saved yet — edits apply live, Save keeps them.';
    cards.innerHTML = BUILTIN_ORDER.map((name) => {
      const t = BUILTINS[name];
      return `<button type="button" class="theme-card${curName === name ? ' active' : ''}" data-theme="${name}" aria-pressed="${curName === name}">
        ${swatches(t.vars)}<strong>${escHtml(t.label)}</strong><span class="theme-sub">${escHtml(t.sub)}</span></button>`;
    }).join('') + `<button type="button" class="theme-card${curName === 'custom' ? ' active' : ''}" data-theme="custom" aria-pressed="${curName === 'custom'}">
        ${swatches(draftCustom)}<strong>Custom</strong><span class="theme-sub">${escHtml(customSub)}</span></button>`;
    cards.querySelectorAll('.theme-card').forEach((b) => b.addEventListener('click', async () => {
      const name = b.dataset.theme;
      if (name === 'custom') { await setTheme('custom', ensureDraft()); setStatus('Custom theme applied and saved.'); }
      else { await setTheme(name); setStatus(`Theme saved: ${BUILTINS[name].label}.`); }
    }));
    renderEditor();
  }
  function renderEditor() {
    const host = $id('theme-editor');
    if (!host) return;
    ensureDraft();
    host.innerHTML = `
      <h4 class="theme-editor-head">Custom theme</h4>
      <p class="muted small">Starts from the active theme. Edits apply live as the Custom theme; Save writes them to the app's settings.</p>
      ${EDITABLE.map(([v, label]) => `
      <div class="theme-var-row">
        <label for="tv${v}">${escHtml(label)}</label>
        <input type="color" id="tv${v}" data-var="${v}" value="${escHtml(toHexInput(draftCustom[v]))}">
        <input type="text" class="input theme-hex" data-var="${v}" value="${escHtml(toHexInput(draftCustom[v]))}" spellcheck="false" autocomplete="off" aria-label="${escHtml(label)} hex value">
        <span class="theme-var-name">${v}</span>
      </div>`).join('')}
      <div class="theme-actions">
        <button type="button" class="btn primary small" id="theme-save">Save custom theme</button>
        <button type="button" class="btn ghost small" id="theme-reset">Reset to default</button>
        <button type="button" class="btn secondary small" id="theme-export">Export</button>
        <button type="button" class="btn secondary small" id="theme-import-toggle">Import</button>
      </div>
      <div class="theme-import-box" id="theme-import-box" hidden>
        <label class="field">Theme JSON
          <textarea class="input" id="theme-import-text" placeholder='{"app":"projectionist-theme","version":1,"vars":{ ... }}'></textarea>
        </label>
        <div class="theme-actions">
          <button type="button" class="btn primary small" id="theme-import-apply">Import theme</button>
          <label class="btn secondary small" for="theme-import-file" style="cursor:pointer">Import from file…</label>
          <input type="file" id="theme-import-file" accept=".json,application/json" hidden>
        </div>
        <p class="theme-err" id="theme-import-error" hidden></p>
      </div>`;
    host.querySelectorAll('input[type="color"][data-var], .theme-hex[data-var]').forEach((inp) => {
      inp.addEventListener('input', () => onEditVar(inp.dataset.var, inp.value, inp));
    });
    $id('theme-save').addEventListener('click', async () => {
      await setTheme('custom', ensureDraft());
      setStatus('Custom theme saved.');
    });
    $id('theme-reset').addEventListener('click', async () => {
      await setTheme('projectionist');
      setStatus('Back to the default theme: Projectionist.');
    });
    $id('theme-export').addEventListener('click', () => {
      const text = exportJSON();
      try {
        const blob = new Blob([text], { type: 'application/json' });
        const a = document.createElement('a');
        a.href = URL.createObjectURL(blob);
        a.download = 'projectionist-theme.json';
        a.click();
        setTimeout(() => URL.revokeObjectURL(a.href), 4000);
        setStatus('Custom theme exported as projectionist-theme.json.');
      } catch {
        setStatus('Export failed in this browser — copy the JSON from Import instead.', true);
      }
    });
    $id('theme-import-toggle').addEventListener('click', () => {
      const box = $id('theme-import-box');
      box.hidden = !box.hidden;
      if (!box.hidden) $id('theme-import-text').focus();
    });
    const doImport = async (text) => {
      const errEl = $id('theme-import-error');
      const res = await importJSON(text);
      if (res.ok) {
        errEl.hidden = true;
        $id('theme-import-box').hidden = true;
        setStatus('Theme imported, saved, and applied as your Custom theme.');
      } else {
        errEl.textContent = res.error;
        errEl.hidden = false;
      }
    };
    $id('theme-import-apply').addEventListener('click', () => doImport($id('theme-import-text').value));
    $id('theme-import-file').addEventListener('change', async (e) => {
      const f = e.target.files && e.target.files[0];
      if (!f) return;
      try { await doImport(await f.text()); } catch { await doImport(''); }
      e.target.value = '';
    });
  }
  function onEditVar(varName, raw, sourceEl) {
    const value = String(raw || '').trim();
    const host = $id('theme-editor');
    const pair = host ? host.querySelectorAll(`[data-var="${varName}"]`) : [];
    if (!HEX_RE.test(value)) {
      if (sourceEl && sourceEl.classList.contains('theme-hex')) sourceEl.classList.add('invalid');
      return;
    }
    pair.forEach((el) => { el.classList.remove('invalid'); if (el !== sourceEl) el.value = value; });
    ensureDraft();
    draftCustom[varName] = value;
    // Live-apply as the custom theme, but do NOT persist edits until
    // Save — the settings store keeps the last saved choice.
    curName = 'custom';
    curVars = { ...draftCustom };
    curScheme = schemeOfVars(curVars);
    applyVars(curVars, curScheme);
    writeCache();
    refreshActiveCards();
  }
  // Card highlight refresh without a full re-render (a full render
  // would rebuild the editor mid-typing and eat the user's input).
  function refreshActiveCards() {
    const cards = $id('theme-cards');
    if (!cards) return;
    cards.querySelectorAll('.theme-card').forEach((b) => {
      const on = b.dataset.theme === curName;
      b.classList.toggle('active', on);
      b.setAttribute('aria-pressed', String(on));
    });
    const customCard = cards.querySelector('[data-theme="custom"]');
    if (customCard) customCard.querySelector('.theme-swatches').outerHTML = swatches(draftCustom || curVars);
  }

  // ---- boot -----------------------------------------------------------------------
  applyCached();
  renderAppearance();
  syncFromServer();

  window.PCMThemes = {
    VARS: THEME_VARS,
    THEMES: BUILTINS,
    EDITABLE,
    current: () => ({ name: curName, vars: { ...curVars }, scheme: curScheme }),
    savedCustom: () => (savedCustom ? { ...savedCustom } : null),
    applyTheme: (name) => setTheme(name, name === 'custom' ? ensureDraft() : null),
    setTheme,
    syncFromServer,
    applyCached,
    validatePalette,
    exportJSON,
    importJSON,
  };
})();
