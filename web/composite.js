// Isolate helpers from Builder globals (both modules define buildDryRun).
(() => {
'use strict';

/* ============================================================================
 * renderer/composite.js — overlay compositing math. PURE and dependency-free.
 * ----------------------------------------------------------------------------
 * FUTURE AI — the load-bearing contract of this file:
 *   - Everything in here is a pure function of its inputs: NO document, NO
 *     canvas, NO node builtins at module scope. That is deliberate — node
 *     (regression.test.cjs) and the browser (overlays.js) BOTH require this
 *     file, and the tests exercise the real placement/rule/dry-run logic.
 *   - The ONLY browser-touching function is drawComposite(), which takes an
 *     already-created <canvas> and loaded HTMLImageElements. It is guarded:
 *     calling it in node throws a clear error instead of failing weirdly.
 *   - Placement model: every badge is { badgeId, x, y, wFrac, opacity,
 *     rule, text? } where x/y are FRACTIONS (0..1) of the poster, top-left
 *     origin, and wFrac is badge width as a fraction of poster width.
 *     Height derives from the badge's aspect ratio (w/h stored per badge).
 *     Fractions — not pixels — so a placement survives poster resolution
 *     changes and the 1000x1500 design canvas maps 1:1 to any real poster.
 *
 * Rule kinds (evaluateRule): 'always' | 'tmdb-rating' | 'imdb-rating' |
 * 'rt-rating' | 'rt-audience' | 'is-4k' | 'tech-manual' | 'unwatched' |
 * 'is-new' | facts v2: 'is-dolby-vision' | 'is-hdr' | 'is-atmos' |
 * 'is-dtsx' | 'resolution-is' | 'resolution-combo' |
 * 'has-content-rating' | 'is-edition' | 'tv-status-is' | 'language-is'.
 * See evaluateRule docs.
 * ========================================================================== */

const CORNERS = ['tl', 'tr', 'bl', 'br'];

// cornerPreset(corner, badgeW, badgeH, W, H, margin=0.02) → { x, y } fractions.
// badgeW/badgeH are the badge's PIXEL size at its current scale; W/H the
// poster pixels. margin is a fraction of poster width.
function cornerPreset(corner, badgeW, badgeH, W, H, margin = 0.02) {
  const m = margin;
  const bw = badgeW / W, bh = badgeH / H;
  switch (corner) {
    case 'tl': return { x: m, y: m * (W / H) };
    case 'tr': return { x: 1 - m - bw, y: m * (W / H) };
    case 'bl': return { x: m, y: 1 - (m * (W / H)) - bh };
    case 'br': return { x: 1 - m - bw, y: 1 - (m * (W / H)) - bh };
    default: throw new Error(`Unknown corner: ${corner}`);
  }
}

// placementRect(placement, badgeAspect, W, H) → pixel rect { x, y, w, h }.
// badgeAspect = badge height / width (from badges.json w/h).
function placementRect(p, badgeAspect, W, H) {
  const w = p.wFrac * W;
  const h = w * badgeAspect;
  return { x: p.x * W, y: p.y * H, w, h, opacity: p.opacity == null ? 1 : p.opacity };
}

// evaluateRule(rule, facts) → { show, label, note, warn }.
// facts: v1 { tmdbRating, imdbRating, hasOmdbKey, width, unwatchedCount,
//          addedAtDays } + v2 stream truth from the Go facts endpoint
//          { resolutionTier, dv, hdr10, hlg, hdrAny, atmos, dtsx,
//            dtshdma, truehd, bestAudioLabel, contentRating,
//            editionTitle, audioLangs, subtitleLangs, dubClass,
//            tvStatus, rtRating, rtAudience, metascore }.
// Missing facts → rule doesn't fire (never guess). Stream booleans are
// POSITIVE-EVIDENCE only: the Go parser sets them true solely when a
// stream actually states the feature, so `=== true` IS the doctrine.
function evaluateRule(rule, facts = {}) {
  const f = facts;
  const no = (note) => ({ show: false, label: null, note, warn: false });
  switch (rule?.kind || 'always') {
    case 'always':
      return { show: true, label: null, note: null, warn: false };
    case 'tmdb-rating': {
      if (f.tmdbRating == null) return no('no TMDb rating for this title');
      const ok = compare(f.tmdbRating, rule.op, rule.value);
      return { show: ok, label: ok ? String(f.tmdbRating) : null, note: null, warn: false };
    }
    case 'imdb-rating': {
      // Facts v2 chain: Plex's own imdb:// Rating[] entry first, then
      // OMDb. The key gate is gone — only the VALUE gates now.
      if (f.imdbRating == null) return no(f.hasOmdbKey ? 'no IMDb rating for this title' : 'no IMDb rating (no Plex rating, no OMDb key)');
      const ok = compare(f.imdbRating, rule.op, rule.value);
      return { show: ok, label: ok ? String(f.imdbRating) : null, note: null, warn: false };
    }
    case 'rt-rating': {
      if (f.rtRating == null) return no('no Rotten Tomatoes score for this title');
      const ok = compare(f.rtRating, rule.op || '>=', rule.value ?? 0);
      return { show: ok, label: ok ? `${f.rtRating}%` : null, note: null, warn: false };
    }
    case 'rt-audience': {
      if (f.rtAudience == null) return no('no RT audience score for this title');
      const ok = compare(f.rtAudience, rule.op || '>=', rule.value ?? 0);
      return { show: ok, label: ok ? `${f.rtAudience}%` : null, note: null, warn: false };
    }
    case 'is-4k': {
      // Deterministic: Plex Media.width. 3800px ≈ "4K" (3840 minus tolerance).
      if (f.width == null) return no('no width data for this title');
      return { show: f.width >= 3800, label: null, note: 'detected from file', warn: false };
    }
    case 'tech-manual': {
      // Manual assignment — kept for art Plex can't prove. Since facts
      // v2 the DV/HDR/Atmos/DTS:X badges default to real stream
      // detection (cases below); this kind remains for hand-stamping,
      // and the UI labels it 'assigned by you' so it never masquerades
      // as detection. (HDR10+ is still NOT exposed by Plex — no rule
      // kind exists for it on purpose.)
      return { show: true, label: null, note: 'assigned by you', warn: true };
    }
    case 'is-dolby-vision': {
      return f.dv === true
        ? { show: true, label: null, note: 'detected from file', warn: false }
        : no('no Dolby Vision stream detected');
    }
    case 'is-hdr': {
      return f.hdrAny === true
        ? { show: true, label: null, note: 'detected from file', warn: false }
        : no('no HDR stream detected');
    }
    case 'is-atmos': {
      return f.atmos === true
        ? { show: true, label: null, note: 'detected from file', warn: false }
        : no('no Atmos audio detected');
    }
    case 'is-dtsx': {
      return f.dtsx === true
        ? { show: true, label: null, note: 'detected from file', warn: false }
        : no('no DTS:X audio detected');
    }
    case 'resolution-is': {
      if (f.resolutionTier == null) return no('no resolution data for this title');
      const ok = rule.value ? f.resolutionTier === rule.value : true;
      return { show: ok, label: ok ? f.resolutionTier : null, note: 'detected from file', warn: false };
    }
    case 'resolution-combo': {
      // Kometa-style combo text built ONLY from proven facts:
      // "4K", "4K • DV", "1080p • HDR", "4K • DV • HDR" (multi-version)…
      const parts = [];
      if (f.resolutionTier) parts.push(f.resolutionTier);
      if (f.dv === true) parts.push('DV');
      if (f.hdr10 === true) parts.push('HDR');
      if (f.hlg === true) parts.push('HLG');
      if (!parts.length) return no('no resolution/HDR data for this title');
      return { show: true, label: parts.join(' • '), note: 'detected from file', warn: false };
    }
    case 'has-content-rating': {
      if (!f.contentRating) return no('no content rating for this title');
      return { show: true, label: String(f.contentRating), note: null, warn: false };
    }
    case 'is-edition': {
      if (!f.editionTitle) return no('no edition set on this title');
      return { show: true, label: String(f.editionTitle), note: null, warn: false };
    }
    case 'tv-status-is': {
      if (!f.tvStatus) return no('no status known for this show');
      const ok = rule.value ? f.tvStatus === rule.value : true;
      return { show: ok, label: ok ? f.tvStatus : null, note: null, warn: false };
    }
    case 'language-is': {
      // The pill describes the item's ACTUAL audio. With an explicit
      // rule value (sub/dub/dual — the anime taxonomy from the advisor
      // dubscan) the dubClass must match exactly. With NO value (the
      // pill's default) it draws what is true for ANY title: SUB/DUAL
      // when the Japanese pattern is present, otherwise the real audio
      // language(s) — one language draws its code (ENG), two draw
      // DUAL, more draw MULTI. ('dub' alone just means an English track
      // exists; on a native-English title that's the language, not a
      // dub — the language display below handles it.) Unknown only
      // when Plex carries nothing classifiable, with the SPECIFIC
      // reason (the owner live report 2026-10-22: a bare "audio languages
      // unknown" explained nothing).
      const dc = f.dubClass;
      const langs = Array.isArray(f.audioLangs) ? f.audioLangs : [];
      const unknown = () => no(
        f.langsError ? `episode languages couldn't be read (${f.langsError})`
          : f.langsFromEpisodes ? 'no language tags on any episode'
            : f.type === 'show' ? 'show — audio lives on its episodes, not the show record'
              : !f.audioTracks ? 'no audio streams found on this title'
                : langs.length ? `audio languages are ${langs.join(', ')} — no sub/dub pattern`
                  : 'audio tracks carry no language tags in Plex');
      if (rule.value) {
        if (!dc || dc === 'unknown') return unknown();
        const ok = dc === rule.value;
        return { show: ok, label: ok ? dc.toUpperCase() : null, note: 'detected from audio tracks', warn: false };
      }
      if (dc === 'sub') return { show: true, label: 'SUB', note: 'Japanese audio, no English track', warn: false };
      if (dc === 'dual') return { show: true, label: 'DUAL', note: 'Japanese + English audio', warn: false };
      if (langs.length === 1) return { show: true, label: langs[0].toUpperCase(), note: 'audio language', warn: false };
      if (langs.length === 2) return { show: true, label: 'DUAL', note: `two audio languages: ${langs.join(', ')}`, warn: false };
      if (langs.length > 2) return { show: true, label: 'MULTI', note: `${langs.length} audio languages`, warn: false };
      return unknown();
    }
    case 'unwatched': {
      // The label is drawn ON the badge (the generic count pill), so
      // it is the counted TEXT, never a bare numeral (v1.9.1) — the
      // same composition the watch-status family uses: the count is
      // stated exactly when it says more than the binary state.
      const n = Number(f.unwatchedCount || 0);
      return { show: n > 0, label: n > 1 ? `${n} UNWATCHED` : n > 0 ? 'UNWATCHED' : null, note: null, warn: false };
    }
    case 'is-new': {
      const days = Number(rule.days || 14);
      if (f.addedAtDays == null) return no('no added date for this title');
      return { show: f.addedAtDays < days, label: null, note: null, warn: false };
    }
    default:
      return no(`unknown rule kind: ${rule.kind}`);
  }
}

function compare(actual, op, expected) {
  const a = Number(actual), e = Number(expected);
  switch (op) {
    case '>=': return a >= e;
    case '>': return a > e;
    case '<=': return a <= e;
    case '<': return a < e;
    case '=': return a === e;
    default: return false;
  }
}

// =====================================================================
// KOMETA-STYLE FAMILIES (2026-10-22 — the owner's order: "completely
// recreate our overlays to function like theirs do and take inspiration
// from theirs"). A FAMILY is a definition, not a sticker:
//   { id, name, description, default corner, shell badge, evaluator }.
// The layout the user builds = which families are ON + per-family
// tweaks (corner, size scale, x/y offsets, family settings like rating
// slots or the ribbon honor). At evaluate/apply time every enabled
// family is judged per item against that item's REAL facts — Kometa's
// model (their defaults are per-family rules over item metadata, not
// placed art). A family whose facts are missing does NOT fire, and the
// verdict/dry-run says exactly why. Custom PNG badges and Fanart logos
// still work as CUSTOM placements appended to the family layout.
// =====================================================================

// Fixed stacking order per corner — Kometa queues overlays by weight;
// this is our weight table. Families sharing a corner stack in this
// order with a consistent gap (see layoutFamilyDrawables).
const FAMILY_STACK_ORDER = {
  tl: ['resolution', 'hdr', 'audio', 'channels', 'video-codec', 'aspect-ratio', 'versions'],
  tr: ['status', 'ribbon', 'leaving', 'collection', 'family', 'new', 'watch-status'],
  bl: ['edition', 'details', 'runtime', 'quick-watch', 'source', 'decade', 'studio', 'genre', 'franchise', 'saga-position', 'origin', 'streaming', 'ratings'],
  br: ['content-rating', 'languages', 'subtitles'],
};

// Base width (fraction of poster width, at size scale 1) per family.
// Ratings is per SLOT — the row is slots laid side by side.
const FAMILY_WIDTH = {
  resolution: 0.46, audio: 0.42, versions: 0.30, ratings: 0.21,
  edition: 0.46, status: 0.54, ribbon: 0.54,
  'content-rating': 0.27, languages: 0.27,
  source: 0.46, details: 0.56, subtitles: 0.30, runtime: 0.32,
  'video-codec': 0.30, 'watch-status': 0.36, new: 0.26, decade: 0.28,
  streaming: 0.44, studio: 0.48, genre: 0.40,
  hdr: 0.30, channels: 0.24, 'aspect-ratio': 0.28, franchise: 0.48, origin: 0.32,
  collection: 0.54, leaving: 0.54,
  family: 0.46, 'saga-position': 0.40, 'quick-watch': 0.36,
};
// The genre family's BANNER presentation (v1.11.1) draws its ribbon
// shell at ribbon width instead of the genre pill's FAMILY_WIDTH —
// presentation is layout; the verdict text is the same either way.
const GENRE_BANNER_WFRAC = 0.54;

const RATING_SOURCE_NAMES = {
  tmdb: 'TMDb', imdb: 'IMDb', 'rt-critic': 'RT critics',
  'rt-audience': 'RT audience', metascore: 'Metascore',
};

// ratingSourceValue resolves ONE ratings slot against the facts. The
// source-priority chains from Kometa's ratings.yml (imdb slot: Plex
// imdb → OMDb; rt slots: OMDb tomatoes/audience; …) are ALREADY
// resolved into the flat fact fields by the facts layer — this is a
// pure read: a number becomes drawn text + glyph badge, else null.
function ratingSourceValue(source, facts) {
  const num = (v) => (typeof v === 'number' && isFinite(v) ? v : null);
  switch (source) {
    case 'tmdb': { const v = num(facts.tmdbRating); return v == null ? null : { text: v.toFixed(1), badgeId: 'star' }; }
    case 'imdb': { const v = num(facts.imdbRating); return v == null ? null : { text: v.toFixed(1), badgeId: 'imdb' }; }
    case 'rt-critic': { const v = num(facts.rtRating); return v == null ? null : { text: Math.round(v) + '%', badgeId: 'tomato' }; }
    case 'rt-audience': { const v = num(facts.rtAudience); return v == null ? null : { text: Math.round(v) + '%', badgeId: 'popcorn' }; }
    case 'metascore': { const v = num(facts.metascore); return v == null ? null : { text: String(Math.round(v)), badgeId: 'pill' }; }
  }
  return null;
}

// audioFormatName names the best audio format the facts PROVE — the
// premium flags first (stream classification), then the plain codec.
// Codec resolution order (v1.11 fix — a plain-codec best track used
// to earn NO badge under Go-served facts, because this reader only
// understood the legacy scan-form label):
//   1. facts.bestAudioCodec — the raw codec field the Go facts layer
//      serves for the best stream ("aac", "eac3", …), mapped below.
//   2. The legacy scan-form label's first token
//      ("aac • 2.0 • eng" → "aac") — the original behavior, kept
//      byte-identical for that form.
//   3. Plex's extended display title, CONSERVATIVELY: the first
//      token of the parenthesized segment ("English (AAC Mono)" →
//      "aac"), or of the bare title ("DTS 5.1" → "dts").
// Only mapped names ever resolve; an unmapped codec returns null and
// the family hides with the raw value in its note — no invented
// brand names, at any step.
const AUDIO_CODEC_NAMES = {
  eac3: 'DD+', ac3: 'Dolby Digital', dca: 'DTS', dts: 'DTS', aac: 'AAC',
  flac: 'FLAC', pcm_s16le: 'PCM', pcm_s24le: 'PCM', pcm_s16be: 'PCM',
  pcm_s24be: 'PCM', pcm: 'PCM',
  mp3: 'MP3', opus: 'Opus', vorbis: 'Vorbis',
};
function audioFormatName(facts) {
  if (facts.atmos) return 'Dolby Atmos';
  if (facts.dtsx) return 'DTS:X';
  if (facts.dtshdma) return 'DTS-HD MA';
  if (facts.truehd) return 'TrueHD';
  const fromCodec = AUDIO_CODEC_NAMES[String(facts.bestAudioCodec || '').trim().toLowerCase()];
  if (fromCodec) return fromCodec;
  const label = String(facts.bestAudioLabel || '');
  const legacy = AUDIO_CODEC_NAMES[label.split('•')[0].trim().toLowerCase()];
  if (legacy) return legacy;
  const seg = label.includes('(') && label.includes(')')
    ? label.slice(label.indexOf('(') + 1, label.indexOf(')'))
    : label;
  const token = seg.trim().split(/\s+/)[0].toLowerCase().replace(/[^a-z0-9_]/g, '');
  return AUDIO_CODEC_NAMES[token] || null;
}

const FAMILY_DEFS = [
  { id: 'resolution', name: 'Resolution', shell: 'pill-rescombo', corner: 'tl',
    description: 'One combined badge, dovetail style: tier + format — “4K • DV • HDR”, “1080p • HDR”, “720p”.' },
  { id: 'audio', name: 'Audio', shell: 'pill-audio', corner: 'tl',
    description: 'Names the best audio format on the file: Dolby Atmos, DTS:X, DTS-HD MA, TrueHD, DD+…' },
  { id: 'ratings', name: 'Ratings', shell: null, corner: 'bl',
    description: 'Up to three rating slots — pick the sources; each falls back through its chain (Plex → OMDb) and draws the value beside its glyph.' },
  { id: 'edition', name: 'Edition', shell: 'pill-edition', corner: 'bl',
    description: 'Draws the edition title (Director’s Cut, Extended Edition…) when Plex has one set.' },
  { id: 'status', name: 'Show status', shell: 'ribbon-status', corner: 'tr',
    description: 'Shows only: Returning / Airing / Ended / Canceled ribbon, status via TMDb.' },
  { id: 'ribbon', name: 'Honor ribbon', shell: 'ribbon-blank', corner: 'tr',
    description: 'Corner honor ribbon: RT Fresh (RT ≥ 60%), Top Rated (your threshold), or Awards (Oscar / Emmy / Golden Globe read literally from OMDb’s awards line). IMDb Top 250 is NOT offered — we have no honest chart source for it.' },
  { id: 'content-rating', name: 'Content rating', shell: 'pill-content-rating', corner: 'br',
    description: 'Draws the content rating (PG-13, TV-MA, R…) when Plex has one.' },
  { id: 'languages', name: 'Languages', shell: 'pill-language', corner: 'br',
    description: 'The item’s audio languages — codes (ENG · JPN), class (SUB / DUB / DUAL), or codes + subtitle languages. On shows it reads the episodes’ languages, so it works library-wide on TV.' },
  { id: 'versions', name: 'Versions', shell: 'pill-versions', corner: 'tl',
    description: '“2 Versions” when the item has more than one file version in Plex.' },
  { id: 'source', name: 'Source', shell: 'pill-source', corner: 'bl',
    description: 'The release source the file name actually states — REMUX, Blu-ray, WEB-DL, WEBRIP, HDTV, DVD. Read literally from the file name, never inferred from quality.' },
  { id: 'details', name: 'Details', shell: 'pill-details', corner: 'bl',
    description: 'Movies: year and runtime (“2024 • 2H 16M”). Shows: year, seasons and episodes (“2019 • 5 SEASONS • 62 EPISODES”) — only the parts Plex states.' },
  { id: 'runtime', name: 'Runtime', shell: 'pill-runtime', corner: 'bl',
    description: 'The title’s own runtime on its own badge — “2H 28M” or clock style “2:28”. Movies and episodes carry one; shows don’t (their episodes do), so it never fires for a show.' },
  { id: 'quick-watch', name: 'Quick watch', shell: 'pill-quick-watch', corner: 'bl',
    description: 'Runtime in plain words: “UNDER 90 MIN” when the title runs shorter than your threshold (the note always states the actual runtime). Movies judge on their own runtime; seasons on their episodes’ total — a short mini-series can qualify. Shows have no single runtime, and episodes are excluded (nearly all would fire, which is noise).' },
  { id: 'subtitles', name: 'Subtitles', shell: 'pill-subtitles', corner: 'br',
    description: 'Subtitle detail in three modes: the tagged languages (ENG · ESP), the track count (4 TRACKS — a fact even when streams are untagged), or full (languages • count • FORCED • SDH, only the parts Plex states).' },
  { id: 'watch-status', name: 'Watch status', shell: 'pill-watch-status', corner: 'tr',
    description: '“UNWATCHED” for movies/episodes you haven’t played; shows draw “N UNWATCHED” (episodes left). Watched titles stay bare unless you opt in.' },
  { id: 'new', name: 'New', shell: 'pill-new', corner: 'tr',
    description: '“NEW” for titles added within your window (7–365 days, from Plex’s added date).' },
  { id: 'decade', name: 'Decade', shell: 'pill-decade', corner: 'bl',
    description: 'The release decade from the year Plex states — “1980s”, “2000s”.' },
  { id: 'video-codec', name: 'Video codec', shell: 'pill-video-codec', corner: 'tl',
    description: 'The video codec of the file whose resolution wins (HEVC, H.264, AV1…) — an unrecognized codec hides the badge and names itself in the verdict instead of being dressed up.' },
  { id: 'streaming', name: 'Streaming', shell: 'pill-streaming', corner: 'bl',
    description: 'Where the title streams in YOUR region, via TMDb watch providers (flatrate only): the top service, or up to three. Needs a TMDb key and a TMDb match.' },
  { id: 'studio', name: 'Studio', shell: 'pill-studio', corner: 'bl',
    description: 'The studio or network name exactly as Plex states it (A24, HBO…).' },
  { id: 'genre', name: 'Genre', shell: 'pill-genre', corner: 'bl',
    description: 'The title’s genre from Plex’s own genre tags — the first genre (or first two), or ONE isolated genre you choose: in isolate mode only titles tagged with that genre earn the badge (a horror-comedy earns the Horror badge; everything else stays clean). Draws as a pill or as a banner ribbon — same text either way.' },
  { id: 'hdr', name: 'HDR', shell: 'pill-hdr', corner: 'tl',
    description: 'The dynamic range on its own badge — DV, HDR10, HLG, composed only from the stream flags Plex proves. SDR titles (or ones Plex says nothing about) draw nothing. HDR10+ is never claimed: Plex exposes no reliable fact for it.' },
  { id: 'channels', name: 'Channels', shell: 'pill-channels', corner: 'tl',
    description: 'The channel layout of the best audio track, stated the way releases state it — 5.1, 7.1, 2.0…' },
  { id: 'aspect-ratio', name: 'Aspect ratio', shell: 'pill-aspect-ratio', corner: 'tl',
    description: 'The winning version’s aspect ratio, stated the way releases state it — 2.39:1, 16:9, 4:3. Values near a standard ratio take its name; anything else is stated as the ratio it measures.' },
  { id: 'franchise', name: 'Franchise', shell: 'pill-franchise', corner: 'bl',
    description: 'Movies only: the saga this film belongs to, named exactly as TMDb’s collection names it (its belongs-to-collection). Standalone films draw nothing. Needs a TMDb key and a TMDb match.' },
  { id: 'saga-position', name: 'Saga position', shell: 'pill-saga-position', corner: 'bl',
    description: 'Movies only: where this film sits in its TMDb saga, counted by release date — “MOVIE 2 OF 4”. The count is the whole saga as TMDb lists it, owned or not. Single-film collections have no position to state. Needs a TMDb key and a TMDb match.' },
  { id: 'origin', name: 'Origin', shell: 'pill-origin', corner: 'bl',
    description: 'The title’s origin as TMDb states it: original language and origin country (“JPN · JP”) — only the parts the record actually states. Needs a TMDb key and a TMDb match.' },
  { id: 'collection', name: 'Collection', shell: 'ribbon-collection', corner: 'tr',
    description: 'The Plex collection(s) this title belongs to on YOUR server — the server’s own membership, no TMDb key needed. One collection is named in full; with several, the first is named and the rest counted (“+2”), with all of them listed in the verdict. Signature draws it as a banner ribbon; the other styles as a ribbon-styled pill. Movies and shows only.' },
  { id: 'family', name: 'Family', shell: 'ribbon-family', corner: 'tr',
    description: 'A friendly ribbon reading “FAMILY” when the title’s own content rating is in the family set — G / PG for movies and TV-Y / TV-Y7 / TV-G for shows by default, and the set is yours to change with the rating toggles in the settings below. Anything outside the set (PG-13, TV-MA, unrated…) draws nothing, and the verdict names the actual rating. Movies and shows only.' },
  { id: 'leaving', name: 'Leaving soon', shell: 'ribbon-leaving', corner: 'tr',
    description: 'A banner from YOUR Leaving soon list (the card on the Apply page — dates are set there, in the app, never read from Plex or any outside source): “LEAVING SOON” when the date falls inside your window, or “LEAVING ON MM/DD/YYYY” in date mode. The banner shows the list AS OF APPLY TIME — changing a date afterwards does not redraw posters already badged until they are re-applied. Movies and shows only.' },
];

function familyDef(id) { return FAMILY_DEFS.find((d) => d.id === id) || null; }

// The default family layout — a fresh Studio starts here.
function defaultFamilies() {
  // Fresh layouts lead with the Signature engine (the procedural
  // frosted-glass renderer — § signature below). Saved templates that
  // carry no variant keep the classic behaviour exactly: the
  // signature default lives HERE, in the fresh-layout factory, not in
  // familySettings' fallback.
  return {
    resolution: { on: true, variant: 'signature' },
    audio: { on: true, variant: 'signature' },
    ratings: { on: true, slots: ['tmdb', 'imdb', 'rt-critic'], variant: 'signature' },
    edition: { on: true, variant: 'signature' },
    status: { on: true, variant: 'signature' },
    ribbon: { on: false, honor: 'rt-fresh', source: 'tmdb', threshold: 8, variant: 'signature' },
    'content-rating': { on: true, variant: 'signature' },
    languages: { on: false, variant: 'signature' },
    versions: { on: false, variant: 'signature' },
    source: { on: false, variant: 'signature' },
    details: { on: false, variant: 'signature' },
    runtime: { on: false, variant: 'signature' },
    'quick-watch': { on: false, minutes: 90, variant: 'signature' },
    subtitles: { on: false, variant: 'signature' },
    'watch-status': { on: false, showWatched: false, variant: 'signature' },
    new: { on: false, windowDays: 30, variant: 'signature' },
    decade: { on: false, variant: 'signature' },
    'video-codec': { on: false, variant: 'signature' },
    streaming: { on: false, region: 'US', mode: 'first', variant: 'signature' },
    studio: { on: false, variant: 'signature' },
    genre: { on: false, count: 1, mode: 'first', presentation: 'pill', genre: '', variant: 'signature' },
    hdr: { on: false, variant: 'signature' },
    channels: { on: false, variant: 'signature' },
    'aspect-ratio': { on: false, variant: 'signature' },
    franchise: { on: false, variant: 'signature' },
    'saga-position': { on: false, variant: 'signature' },
    origin: { on: false, variant: 'signature' },
    collection: { on: false, variant: 'signature' },
    family: { on: false, variant: 'signature' },
    leaving: { on: false, mode: 'soon', windowDays: 30, variant: 'signature' },
  };
}

// Aurora style pack (2026-10-22): richer shell art at IDENTICAL
// canvas sizes (see assets/badges/generate.mjs), so swapping a
// family's variant never moves or resizes a badge. A family whose
// variant is 'aurora' draws these ids — the family shell AND the
// ratings slot glyphs. Custom placements are NOT swapped: a custom
// badge is the user's own art choice.
const AURORA_SHELLS = {
  'pill-rescombo': 'pill-rescombo-aurora',
  'pill-audio': 'pill-audio-aurora',
  'pill-edition': 'pill-edition-aurora',
  'pill-content-rating': 'pill-content-rating-aurora',
  'pill-language': 'pill-language-aurora',
  'pill-versions': 'pill-versions-aurora',
  'pill-source': 'pill-source-aurora',
  'pill-details': 'pill-details-aurora',
  'pill-runtime': 'pill-runtime-aurora',
  'pill-subtitles': 'pill-subtitles-aurora',
  'pill-watch-status': 'pill-watch-status-aurora',
  'pill-new': 'pill-new-aurora',
  'pill-decade': 'pill-decade-aurora',
  'pill-video-codec': 'pill-video-codec-aurora',
  'pill-streaming': 'pill-streaming-aurora',
  'pill-studio': 'pill-studio-aurora',
  'pill-genre': 'pill-genre-aurora',
  'pill-hdr': 'pill-hdr-aurora',
  'pill-channels': 'pill-channels-aurora',
  'pill-aspect-ratio': 'pill-aspect-ratio-aurora',
  'pill-franchise': 'pill-franchise-aurora',
  'pill-saga-position': 'pill-saga-position-aurora',
  'pill-origin': 'pill-origin-aurora',
  'pill-quick-watch': 'pill-quick-watch-aurora',
  'ribbon-collection': 'ribbon-collection-aurora',
  'ribbon-family': 'ribbon-family-aurora',
  'ribbon-leaving': 'ribbon-leaving-aurora',
  'ribbon-genre': 'ribbon-genre-aurora',
  'pill': 'pill-aurora',
  'ribbon-status': 'ribbon-status-aurora',
  'ribbon-blank': 'ribbon-blank-aurora',
  star: 'star-aurora',
  imdb: 'imdb-aurora',
  tomato: 'tomato-aurora',
  popcorn: 'popcorn-aurora',
};
function auroraShell(badgeId) { return AURORA_SHELLS[badgeId] || badgeId; }

// The family set (v1.15): the content ratings the `family` family
// treats as family ratings. This used to be hardcoded per media
// type inside the evaluator; it is a template setting now (the
// `ratings` key on the family entry). FAMILY_RATINGS_DEFAULT is the
// shipped set — movies G / PG, shows TV-Y / TV-Y7 / TV-G — and a
// family entry with no `ratings` key judges exactly as the app
// always has (the evaluator falls back to the per-type halves of
// this set, so shipped verdicts and their notes are unchanged).
// FAMILY_RATING_VOCAB is the full rating list the Studio offers as
// toggle chips; both clampFamilyRatings here and the Go sanitizer
// (templates.go) keep ONLY these values, deduped, in vocabulary
// order — so a configured set, and any verdict note listing it,
// is deterministic on both sides.
const FAMILY_RATING_VOCAB = ['G', 'PG', 'PG-13', 'R', 'NC-17',
  'TV-Y', 'TV-Y7', 'TV-Y7-FV', 'TV-G', 'TV-PG', 'TV-14', 'TV-MA'];
const FAMILY_RATINGS_DEFAULT = ['G', 'PG', 'TV-Y', 'TV-Y7', 'TV-G'];
const FAMILY_RATING_GROUPS = {
  movie: ['G', 'PG', 'PG-13', 'R', 'NC-17'],
  tv: ['TV-Y', 'TV-Y7', 'TV-Y7-FV', 'TV-G', 'TV-PG', 'TV-14', 'TV-MA'],
};
function clampFamilyRatings(raw) {
  if (!Array.isArray(raw)) return [...FAMILY_RATINGS_DEFAULT];
  const wanted = new Set();
  for (const v of raw) {
    if (typeof v !== 'string') continue;
    const u = v.trim().toUpperCase();
    if (FAMILY_RATING_VOCAB.includes(u)) wanted.add(u);
  }
  return FAMILY_RATING_VOCAB.filter((r) => wanted.has(r));
}

// familySettings merges a stored family entry over the defaults and
// clamps every value to the ranges the designer offers (mirrors the
// server-side sanitizer in templates.go — keep the two in sync). A
// family ABSENT from the layout object is OFF: a saved layout means
// exactly the families it lists (the renderer's working state is
// always the complete object from defaultFamilies(), so this only
// bites sparse/hand-written layouts — where "not listed" must never
// silently mean "on").
function familySettings(families, id) {
  const present = !!(families && families[id]);
  const raw = present ? families[id] : {};
  const def = familyDef(id);
  return {
    on: present ? raw.on !== false : false,
    variant: raw.variant === 'aurora' ? 'aurora' : raw.variant === 'signature' ? 'signature' : 'classic',
    corner: ['tl', 'tr', 'bl', 'br'].includes(raw.corner) ? raw.corner : (def ? def.corner : 'tl'),
    size: typeof raw.size === 'number' && isFinite(raw.size) ? Math.min(2, Math.max(0.5, raw.size)) : 1,
    dx: typeof raw.dx === 'number' && isFinite(raw.dx) ? Math.min(0.3, Math.max(-0.3, raw.dx)) : 0,
    dy: typeof raw.dy === 'number' && isFinite(raw.dy) ? Math.min(0.3, Math.max(-0.3, raw.dy)) : 0,
    slots: id === 'ratings'
      ? (Array.isArray(raw.slots) && raw.slots.length ? raw.slots.filter((s) => RATING_SOURCE_NAMES[s]).slice(0, 3) : ['tmdb', 'imdb', 'rt-critic'])
      : undefined,
    honor: ['rt-fresh', 'top-rated', 'awards', 'off'].includes(raw.honor) ? raw.honor : 'rt-fresh',
    source: raw.source === 'imdb' ? 'imdb' : 'tmdb',
    threshold: typeof raw.threshold === 'number' && isFinite(raw.threshold) ? Math.min(10, Math.max(0, raw.threshold)) : 8,
    style: ['codes', 'class', 'codes-subs'].includes(raw.style) ? raw.style : 'codes',
    detail: ['format', 'full'].includes(raw.detail) ? raw.detail : 'format',
    format: ['hm', 'clock'].includes(raw.format) ? raw.format : 'hm',
    // `mode` is family-owned: subtitles read languages/count/full,
    // streaming reads first/all, genre reads first/only, leaving
    // reads soon/date — like `slots`, it only exists on the families
    // that own it.
    mode: id === 'subtitles'
      ? (['languages', 'count', 'full'].includes(raw.mode) ? raw.mode : 'languages')
      : id === 'streaming'
        ? (['first', 'all'].includes(raw.mode) ? raw.mode : 'first')
        : id === 'genre'
          ? (['first', 'only'].includes(raw.mode) ? raw.mode : 'first')
          : id === 'leaving'
            ? (['soon', 'date'].includes(raw.mode) ? raw.mode : 'soon')
            : undefined,
    showWatched: id === 'watch-status' ? raw.showWatched === true : undefined,
    windowDays: id === 'new' || id === 'leaving'
      ? (typeof raw.windowDays === 'number' && isFinite(raw.windowDays) ? Math.min(365, Math.max(1, Math.round(raw.windowDays))) : 30)
      : undefined,
    region: id === 'streaming'
      ? (/^[A-Za-z]{2}$/.test(String(raw.region == null ? '' : raw.region)) ? String(raw.region).toUpperCase() : 'US')
      : undefined,
    count: id === 'genre' ? (raw.count === 2 ? 2 : 1) : undefined,
    // Genre isolation (v1.11.1): the ONE genre 'only' mode isolates,
    // a plain template string (trimmed, length-capped — mirrors the
    // templates.go sanitizer), and the pill/banner presentation.
    genre: id === 'genre'
      ? (typeof raw.genre === 'string' ? raw.genre.trim().slice(0, 40) : '')
      : undefined,
    presentation: id === 'genre' ? (raw.presentation === 'banner' ? 'banner' : 'pill') : undefined,
    // Quick-watch threshold in minutes (v1.11.1): "UNDER N MIN"
    // fires strictly below it. Same clamp on the Go side.
    minutes: id === 'quick-watch'
      ? (typeof raw.minutes === 'number' && isFinite(raw.minutes) ? Math.min(600, Math.max(15, Math.round(raw.minutes))) : 90)
      : undefined,
    // Family rating set (v1.15): the content ratings that count as
    // family. Present only when the stored entry carries a ratings
    // array (clamped like the Go sanitizer: known values, deduped,
    // vocabulary order — an explicitly empty array stays empty,
    // the distinct can-never-fire state). Absent means undefined
    // here and the shipped default set in the evaluator.
    ratings: id === 'family'
      ? (Array.isArray(raw.ratings) ? clampFamilyRatings(raw.ratings) : undefined)
      : undefined,
  };
}

// audioChannelLabel formats a stream channel count the way releases
// state it: 6 channels is "5.1", 8 is "7.1", 2 is "2.0", 1 is "1.0";
// anything else is stated plainly as "N ch" — never dressed up.
function audioChannelLabel(channels) {
  const n = Number(channels);
  if (!n || !isFinite(n) || n <= 0) return null;
  switch (n) {
    case 6: return '5.1';
    case 8: return '7.1';
    case 2: return '2.0';
    case 1: return '1.0';
    default: return `${n} ch`;
  }
}

// runtimeText formats a minute count in the runtime family's two
// styles: 'hm' ("2H 28M", "2H", "48M" — the details family's existing
// composition) or 'clock' ("2:28", "0:48"). Non-positive input is
// null: no duration, no text.
function runtimeText(minutes, format) {
  const mins = Number(minutes || 0);
  if (!(mins > 0)) return null;
  const h = Math.floor(mins / 60), m = mins % 60;
  if (format === 'clock') return `${h}:${String(m).padStart(2, '0')}`;
  return h > 0 ? (m > 0 ? `${h}H ${m}M` : `${h}H`) : `${m}M`;
}

// Display names for the video codec Plex reports on the winning
// version's video stream. The facts carry the RAW codec string; an
// unmapped codec returns null here and the family hides while its
// verdict names the raw value — no dressed-up guesses (a "DIVX"-style
// invention for msmpeg4v3 is exactly what this map refuses to do).
const VIDEO_CODEC_NAMES = {
  hevc: 'HEVC', h264: 'H.264', av1: 'AV1', vp9: 'VP9', vp8: 'VP8',
  mpeg2video: 'MPEG-2', mpeg4: 'MPEG-4', vc1: 'VC-1',
};
function videoCodecName(codec) {
  if (!codec) return null;
  return VIDEO_CODEC_NAMES[String(codec).toLowerCase()] || null;
}

// TMDb provider names, shortened to the forms the services use
// themselves. Anything unmapped passes through VERBATIM — TMDb's
// name is the fact; this map is cosmetics only.
const STREAMING_DISPLAY_NAMES = {
  'Disney Plus': 'Disney+',
  'Amazon Prime Video': 'Prime Video',
  'Apple TV Plus': 'Apple TV+',
  'Paramount Plus': 'Paramount+',
};
function streamingDisplayName(name) {
  return STREAMING_DISPLAY_NAMES[name] || name;
}

// aspectRatioText states an aspect ratio the way releases state it.
// The fact is the winning version's ratio (Plex's own aspectRatio
// field, or width/height — facts layer). A value within tolerance
// of a standard ratio takes the standard's name (2.35, 2.39 and 2.40
// are all the scope ratio releases call "2.39:1"); anything else is
// stated plainly as the decimal ratio it is ("1.90:1") — measured,
// never rounded into a standard it isn't near and never dressed up.
const ASPECT_STANDARDS = [
  { value: 1.33, text: '4:3' },
  { value: 1.37, text: '1.37:1' },
  { value: 1.66, text: '1.66:1' },
  { value: 16 / 9, text: '16:9' },
  { value: 1.85, text: '1.85:1' },
  { value: 2.00, text: '2.00:1' },
  { value: 2.20, text: '2.20:1' },
  { value: 2.39, text: '2.39:1' },
  { value: 2.76, text: '2.76:1' },
];
const ASPECT_TOLERANCE = 0.045;
function aspectRatioText(ratio) {
  const v = Number(ratio);
  if (!v || !isFinite(v) || v <= 0) return null;
  let best = null;
  for (const s of ASPECT_STANDARDS) {
    const d = Math.abs(v - s.value);
    if (d <= ASPECT_TOLERANCE && (!best || d < best.d)) best = { d, text: s.text };
  }
  if (best) return best.text;
  return `${v.toFixed(2)}:1`;
}

// ISO 639-1 → the three-letter code releases state, for the Origin
// family's language part. A language TMDb names that this map
// doesn't know is stated as its own uppercased code — TMDb's value,
// verbatim, never an invented name.
const ORIGIN_LANGUAGE_CODES = {
  en: 'ENG', ja: 'JPN', ko: 'KOR', fr: 'FRA', de: 'DEU', es: 'SPA',
  it: 'ITA', pt: 'POR', ru: 'RUS', zh: 'CHN', hi: 'HIN', ar: 'ARA',
  nl: 'NLD', sv: 'SWE', no: 'NOR', nb: 'NOR', nn: 'NOR', da: 'DAN',
  fi: 'FIN', pl: 'POL', cs: 'CES', sk: 'SLK', hu: 'HUN', el: 'ELL',
  tr: 'TUR', he: 'HEB', fa: 'FAS', ur: 'URD', bn: 'BEN', ta: 'TAM',
  te: 'TEL', ml: 'MAL', kn: 'KAN', mr: 'MAR', th: 'THA', vi: 'VIE',
  id: 'IND', ms: 'MSA', uk: 'UKR', ro: 'RON', bg: 'BUL', hr: 'HRV',
  sr: 'SRP', lt: 'LIT', lv: 'LAV', et: 'EST', is: 'ISL', ga: 'GLE',
  ca: 'CAT', eu: 'EUS', gl: 'GLG', cy: 'CYM', af: 'AFR', sw: 'SWA',
  tl: 'TGL', la: 'LAT', yi: 'YID', am: 'AMH', ne: 'NEP', si: 'SIN',
  km: 'KHM', lo: 'LAO', my: 'MYA', ka: 'KAT', hy: 'HYE', az: 'AZE',
  kk: 'KAZ', uz: 'UZB', mn: 'MON', bo: 'BOD', dz: 'DZO',
};
function originLanguageText(code) {
  const c = String(code || '').trim().toLowerCase();
  if (!c) return null;
  return ORIGIN_LANGUAGE_CODES[c] || c.toUpperCase();
}

// awardsHonorText reads OMDb's awards SENTENCE literally — the same
// honesty contract as the source tag: OMDb writes "Won 2 Oscars." /
// "Nominated for 1 Oscar." / "Won 3 Primetime Emmys.", and only
// those stated wins/nominations at Oscar / Emmy / Golden Globe level
// become a ribbon. This is a literal reading of one metadata line,
// not a chart or a ranking. Priority: Oscar > Emmy > Globe, winner
// before nominee within each.
function awardsHonorText(awardsText) {
  const t = String(awardsText || '');
  if (!t) return null;
  if (/won\s+\d+\s+oscars?/i.test(t)) return 'OSCAR WINNER';
  if (/nominated for\s+\d+\s+oscars?/i.test(t)) return 'OSCAR NOMINEE';
  if (/won\s+\d+\s+primetime emmys?/i.test(t) || /won\s+\d+\s+emmys?/i.test(t)) return 'EMMY WINNER';
  if (/nominated for\s+\d+\s+emmys?/i.test(t)) return 'EMMY NOMINEE';
  if (/won\s+\d+\s+golden globes?/i.test(t)) return 'GOLDEN GLOBE WINNER';
  if (/nominated for\s+\d+\s+golden globes?/i.test(t)) return 'GOLDEN GLOBE NOMINEE';
  return null;
}

// leavingDateText composes the US display date (MM/DD/YYYY,
// zero-padded) from a stored leaving date (YYYY-MM-DD, the facts'
// leavingDate). The stored string is the fact; anything not in that
// exact form yields null — the family hides rather than printing a
// mangled or re-interpreted date.
function leavingDateText(stored) {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(String(stored || ''));
  if (!m) return null;
  return `${m[2]}/${m[3]}/${m[1]}`;
}

// EPISODE FAMILIES (v1.10): the families an episode may carry —
// judged on the episode's OWN technical facts (its streams, its
// duration, its watch state). Every other family stays dark for
// type 'episode' EVEN IF the episode record carries the fields (an
// episode can carry a rating, a content rating, an edition title):
// episode art is a still, not a poster, and honors/identity belong
// to the movie or the show. Ratings are the called-out case —
// episode ratings are sparse agent data, never the show's identity,
// so the ratings family does not fire for episodes, full stop. The
// facts layer suppresses the identity inputs for episodes as well;
// the gate below is the stated rule that holds regardless.
const EPISODE_FAMILIES = [
  'resolution', 'audio', 'video-codec', 'subtitles', 'languages',
  'runtime', 'watch-status', 'new',
  'hdr', 'channels', 'aspect-ratio',
];
const EPISODE_FAMILY_NOTES = {
  ratings: 'episode ratings are not badged — ratings belong to the movie or show',
  ribbon: 'honors belong to movies and shows, not episodes',
  status: 'show status is a show-level fact, not an episode badge',
  'content-rating': 'content rating is not badged on episodes',
  edition: 'editions belong to movies, not episodes',
  versions: 'version counts are not badged on episodes',
  source: 'release source is not badged on episodes',
  details: 'details are identity text — not badged on episode stills',
  decade: 'decade belongs to movies and shows, not episodes',
  streaming: 'streaming is a movie/show fact, not an episode badge',
  studio: 'studio is a movie/show fact, not an episode badge',
  genre: 'genre is a movie/show fact, not an episode badge',
  franchise: 'franchise is a movie fact (TMDb collection), not an episode badge',
  origin: 'origin is a movie/show fact, not an episode badge',
  collection: 'collections belong to the movie or show, not one episode',
  family: 'family ratings belong to the movie or show, not one episode',
  leaving: 'leaving dates are set on the movie or show, not one episode',
  'saga-position': 'saga position is a movie fact, not an episode badge',
  'quick-watch': 'nearly every episode runs under the threshold — a quick-watch badge on episodes would be noise, not information',
};

// evaluateFamily judges ONE family for ONE item. Pure:
// (id, settings, facts) → { show, text, note, slots? }. Missing facts
// NEVER fire — the note names exactly what was missing.
function evaluateFamily(id, settings, facts) {
  const s = settings || {};
  const no = (note) => ({ show: false, text: null, note });
  if (!facts) return no('facts unavailable for this title');
  if (facts.type === 'episode' && !EPISODE_FAMILIES.includes(id)) {
    return no(EPISODE_FAMILY_NOTES[id] || 'not an episode badge');
  }
  switch (id) {
    case 'resolution': {
      // Same dovetail composition as the resolution-combo rule.
      const parts = [];
      if (facts.resolutionTier) parts.push(facts.resolutionTier);
      if (facts.dv === true) parts.push('DV');
      if (facts.hdr10 === true) parts.push('HDR');
      if (facts.hlg === true) parts.push('HLG');
      if (!parts.length) return no('no resolution/HDR data for this title');
      return { show: true, text: parts.join(' • '), note: 'detected from file' };
    }
    case 'audio': {
      const name = audioFormatName(facts);
      if (!name) return no(facts.bestAudioCodec
        ? `audio codec '${facts.bestAudioCodec}' has no badge name — unmapped codecs draw nothing`
        : facts.bestAudioLabel
          ? `audio codec not recognized in '${facts.bestAudioLabel}'`
          : 'no audio streams found on this title');
      // Detail setting: 'format' (default) is the name alone; 'full'
      // appends the best track's channel layout and language when the
      // facts carry them — parts are omitted, never guessed.
      if (s.detail === 'full') {
        const parts = [name];
        const ch = audioChannelLabel(facts.bestAudioChannels);
        if (ch) parts.push(ch);
        if (facts.bestAudioLang) parts.push(String(facts.bestAudioLang).toUpperCase());
        return { show: true, text: parts.join(' • '), note: 'best audio track, full detail' };
      }
      return { show: true, text: name, note: 'best audio track' };
    }
    case 'ratings': {
      const wanted = Array.isArray(s.slots) && s.slots.length ? s.slots : ['tmdb', 'imdb', 'rt-critic'];
      const slots = [];
      const missing = [];
      for (const src of wanted) {
        const v = ratingSourceValue(src, facts);
        if (v) slots.push({ source: src, text: v.text, badgeId: v.badgeId });
        else missing.push(RATING_SOURCE_NAMES[src] || src);
      }
      if (!slots.length) return no(`no ratings found — ${missing.join(', ')} unknown for this title`);
      return {
        show: true,
        text: slots.map((sl) => sl.text).join(' · '),
        slots,
        note: missing.length ? `no ${missing.join(', ')} for this title` : 'all slots resolved',
      };
    }
    case 'content-rating': {
      if (!facts.contentRating) return no('no content rating for this title');
      return { show: true, text: String(facts.contentRating), note: 'from Plex metadata' };
    }
    case 'status': {
      if (facts.type && facts.type !== 'show') return no('shows only');
      if (!facts.tvStatus) return no('no status known for this show');
      return { show: true, text: String(facts.tvStatus), note: 'status via TMDb' };
    }
    case 'languages': {
      // SINGLE-SOURCE RULE (v1.9.1): this badge's text is languages
      // only — the watch state/count belongs to the watch-status
      // family alone and is never composed into this text, whatever
      // the template combination (including watch-status disabled).
      // Display styles (family setting, default Codes):
      //   codes      — the actual audio languages: "ENG · JPN"
      //   class      — the dubscan class only: SUB / DUB / DUAL
      //   codes-subs — codes plus subtitle languages: "ENG · JPN + subs ENG"
      // For SHOWS the facts are the episode UNION (langsFromEpisodes,
      // aggregated server-side in facts.go) and every note discloses
      // that — "across 24 episodes", never a bare claim about a show
      // record that carries no streams of its own.
      if (facts.langsError) return no(`episode languages couldn't be read (${facts.langsError})`);
      const style = ['codes', 'class', 'codes-subs'].includes(s.style) ? s.style : 'codes';
      const langs = Array.isArray(facts.audioLangs) ? facts.audioLangs : [];
      const subs = Array.isArray(facts.subtitleLangs) ? facts.subtitleLangs : [];
      const scope = facts.langsFromEpisodes
        ? `across ${facts.episodeCount || 0} episodes${facts.langsPartial ? ` (first ${facts.episodeCount} of ${facts.episodeTotal})` : ''}`
        : 'this title';
      const noLangNote = facts.langsFromEpisodes
        ? 'no language tags on any episode'
        : facts.type === 'show' ? 'show — audio lives on its episodes, not the show record'
          : !facts.audioTracks ? 'no audio streams found on this title'
            : 'audio tracks carry no language tags in Plex';
      if (style === 'class') {
        const dc = facts.dubClass;
        if (dc === 'sub' || dc === 'dual' || dc === 'dub') {
          return { show: true, text: dc.toUpperCase(), note: `audio class, ${scope}` };
        }
        return no(langs.length ? `audio languages are ${langs.join(', ')} — no sub/dub pattern (${scope})` : noLangNote);
      }
      if (!langs.length) return no(noLangNote);
      let text = langs.map((l) => String(l).toUpperCase()).join(' · ');
      if (style === 'codes-subs') {
        if (subs.length) text += ` + subs ${subs.map((l) => String(l).toUpperCase()).join(' · ')}`;
        else if (facts.hasSubs) text += ' + subs';
      }
      return { show: true, text, note: `audio languages, ${scope}` };
    }
    case 'edition': {
      if (!facts.editionTitle) return no('no edition set on this title');
      return { show: true, text: String(facts.editionTitle), note: 'edition from Plex' };
    }
    case 'ribbon': {
      // Honors are movie/show identity (awards, top-rated lists) —
      // a season carries none, even when it carries a rating.
      if (facts.type === 'season') return no('honors belong to movies and shows, not seasons');
      if (s.honor === 'off') return no('honor is set to Off');
      if (s.honor === 'awards') {
        // The awards line is OMDb's own sentence (facts.awardsText);
        // awardsHonorText reads it literally. No line at all means
        // no OMDb record fed the facts — a different answer from a
        // line that names no Oscar/Emmy/Globe.
        if (!facts.awardsText) return no('no OMDb awards line for this title (needs an OMDb key and an IMDb match)');
        const honor = awardsHonorText(facts.awardsText);
        return honor
          ? { show: true, text: honor, note: `OMDb awards: ${facts.awardsText}` }
          : no('OMDb lists no Oscar/Emmy/Globe win or nomination');
      }
      if (s.honor === 'top-rated') {
        const src = s.source === 'imdb' ? 'imdb' : 'tmdb';
        const v = src === 'imdb' ? facts.imdbRating : facts.tmdbRating;
        const th = typeof s.threshold === 'number' ? s.threshold : 8;
        if (typeof v !== 'number') return no(`${RATING_SOURCE_NAMES[src]} rating unknown`);
        return v >= th
          ? { show: true, text: 'TOP RATED', note: `${RATING_SOURCE_NAMES[src]} ${v.toFixed(1)} ≥ ${th}` }
          : no(`${RATING_SOURCE_NAMES[src]} ${v.toFixed(1)} doesn't meet ≥ ${th}`);
      }
      // rt-fresh — Kometa's Rotten Tomatoes Fresh ribbon.
      if (typeof facts.rtRating !== 'number') return no('Rotten Tomatoes score unknown');
      return facts.rtRating >= 60
        ? { show: true, text: 'FRESH', note: `RT ${Math.round(facts.rtRating)}% ≥ 60% (Certified Fresh style)` }
        : no(`RT ${Math.round(facts.rtRating)}% is below Fresh (60%)`);
    }
    case 'source': {
      // The tag is computed in the Go facts layer from the first
      // Part's file basename — a LITERAL reading (the name must
      // actually say REMUX / WEB-DL / … as a whole token).
      if (!facts.sourceTag) return no('no source tag in this title’s file name');
      return { show: true, text: String(facts.sourceTag), note: 'read from the file name' };
    }
    case 'runtime': {
      // A show has NO single runtime — its episodes do — and the
      // facts layer deliberately never derives one. The note says so
      // instead of dressing a sum or an average up as a runtime.
      if (facts.type === 'show') return no('shows carry per-episode runtimes — no single runtime to state');
      const t = runtimeText(facts.runtimeMinutes, s.format === 'clock' ? 'clock' : 'hm');
      if (!t) return no('Plex states no duration for this title');
      // For a season the fact is the summed total of its episodes'
      // own durations (Go facts layer) — the note says so.
      return { show: true, text: t, note: facts.type === 'season' ? 'total across the season’s episodes, from Plex durations' : 'runtime from Plex' };
    }
    case 'quick-watch': {
      // Runtime in plain words: "UNDER 90 MIN" when the title runs
      // shorter than the template's threshold. The claim is about
      // the THRESHOLD, so the text is composed from the SETTING and
      // the note states the title's actual runtime. "Under" is
      // strict — a title AT the threshold does not fire. Movies
      // judge on their own runtime; seasons on their summed
      // episode total (the facts layer's season runtime — a short
      // mini-series honestly qualifies). Shows have no single
      // runtime (the runtime family's rule), and episodes are
      // gated out above: nearly every episode would fire, and a
      // badge that fires on everything says nothing.
      if (facts.type === 'show') return no('shows carry no single runtime — their episodes do');
      const threshold = typeof s.minutes === 'number' && isFinite(s.minutes) && s.minutes > 0 ? Math.round(s.minutes) : 90;
      const rt = Number(facts.runtimeMinutes);
      if (!rt || !isFinite(rt) || rt <= 0) return no('Plex states no runtime for this title');
      const scope = facts.type === 'season' ? 'season total across its episodes' : 'runtime from Plex';
      if (rt >= threshold) return no(`${rt} min (${scope}) is not under the ${threshold}-minute threshold`);
      return { show: true, text: `UNDER ${threshold} MIN`, note: `${rt} min (${scope}) — under the ${threshold}-minute threshold` };
    }
    case 'details': {
      const year = facts.year != null && facts.year !== '' ? String(facts.year) : null;
      if (facts.type === 'show') {
        const parts = [];
        if (year) parts.push(year);
        const sc = Number(facts.seasonCount || 0);
        if (sc > 0) parts.push(`${sc} SEASON${sc === 1 ? '' : 'S'}`);
        const ec = Number(facts.showEpisodeCount || 0);
        if (ec > 0) parts.push(`${ec} EPISODE${ec === 1 ? '' : 'S'}`);
        if (!parts.length) return no('no year, season or episode count for this show');
        return { show: true, text: parts.join(' • '), note: 'from Plex metadata' };
      }
      if (facts.type === 'season') {
        // Season details: year • episode count • total runtime —
        // the facts a season actually owns (its episodes' count and
        // summed durations, computed in the Go facts layer).
        const sparts = [];
        if (year) sparts.push(year);
        const ec = Number(facts.episodeTotal || facts.episodeCount || 0);
        if (ec > 0) sparts.push(`${ec} EPISODE${ec === 1 ? '' : 'S'}`);
        if (!s.__suppressRuntime) {
          const rt = runtimeText(facts.runtimeMinutes, 'hm');
          if (rt) sparts.push(rt);
        }
        if (!sparts.length) return no('no year, episode count or runtime for this season');
        return { show: true, text: sparts.join(' • '), note: 'from Plex metadata' };
      }
      const parts = [];
      if (year) parts.push(year);
      // Dedupe: when the dedicated runtime family is enabled in the
      // layout, layoutFamilyDrawables passes __suppressRuntime and
      // the runtime part stays out of details (it has its own badge).
      if (!s.__suppressRuntime) {
        const rt = runtimeText(facts.runtimeMinutes, 'hm');
        if (rt) parts.push(rt);
      }
      if (!parts.length) return no(s.__suppressRuntime ? 'no year for this title' : 'no year or runtime for this title');
      return { show: true, text: parts.join(' • '), note: 'from Plex metadata' };
    }
    case 'subtitles': {
      if (facts.langsError) return no(`episode subtitle languages couldn't be read (${facts.langsError})`);
      // Modes (family setting, default 'languages'):
      //   languages — EXACTLY the original behavior: tagged languages
      //     or nothing (untagged streams hide with the disclosure).
      //   count     — "4 TRACKS": a stream count is a fact even when
      //     no stream is tagged, so it fires on untagged sets too.
      //   full      — languages • count • FORCED • SDH, composed ONLY
      //     of the parts the facts actually state.
      const mode = ['languages', 'count', 'full'].includes(s.mode) ? s.mode : 'languages';
      const subs = Array.isArray(facts.subtitleLangs) ? facts.subtitleLangs : [];
      const count = Number(facts.subtitleCount || 0);
      const countText = count > 0 ? `${count} TRACK${count === 1 ? '' : 'S'}` : null;
      const scope = facts.langsFromEpisodes
        ? `across ${facts.episodeCount || 0} episodes${facts.langsPartial ? ` (first ${facts.episodeCount} of ${facts.episodeTotal})` : ''}`
        : 'this title';
      if (mode === 'count') {
        if (countText) return { show: true, text: countText, note: `subtitle tracks, ${scope}` };
        return no(facts.hasSubs
          ? 'subtitle streams present but their count is unknown'
          : 'no subtitle streams found on this title');
      }
      if (mode === 'full') {
        const parts = [];
        if (subs.length) parts.push(subs.map((l) => String(l).toUpperCase()).join(' • '));
        if (countText) parts.push(countText);
        if (facts.subtitleForced === true) parts.push('FORCED');
        if (facts.subtitleSDH === true) parts.push('SDH');
        if (!parts.length) {
          return no(facts.hasSubs
            ? 'subtitle streams present but carry no language tags, count or flags'
            : 'no subtitle streams found on this title');
        }
        return { show: true, text: parts.join(' • '), note: `subtitle detail, ${scope}` };
      }
      if (!subs.length) {
        return no(facts.hasSubs
          ? 'subtitle streams present but carry no language tags'
          : 'no subtitle streams found on this title');
      }
      return { show: true, text: subs.map((l) => String(l).toUpperCase()).join(' • '), note: `subtitle languages, ${scope}` };
    }
    case 'versions': {
      const n = Number(facts.mediaCount || 0);
      if (n > 1) return { show: true, text: `${n} Versions`, note: `${n} file versions in Plex` };
      return no(n === 1 ? 'single version' : 'version count unknown');
    }
    case 'watch-status': {
      // This family OWNS the watch state/count (v1.9.1
      // single-sourcing): no other badge composes it into its text.
      // unwatchedCount is Plex's own arithmetic (movies/episodes:
      // viewCount 0/1; shows: leafCount − viewedLeafCount). Null
      // means Plex stated no watch data — no badge, said plainly.
      const n = facts.unwatchedCount;
      if (n == null) return no('Plex states no watch data for this title');
      if (facts.type === 'show' || facts.type === 'season') {
        if (n > 0) return { show: true, text: `${n} UNWATCHED`, note: `${n} episode${n === 1 ? '' : 's'} unwatched` };
        return s.showWatched === true
          ? { show: true, text: 'ALL WATCHED', note: 'every episode watched' }
          : no('all episodes watched — badge off for watched titles');
      }
      if (n > 0) return { show: true, text: 'UNWATCHED', note: 'not watched yet' };
      return s.showWatched === true
        ? { show: true, text: 'WATCHED', note: 'watched' }
        : no('already watched — badge off for watched titles');
    }
    case 'new': {
      const w = typeof s.windowDays === 'number' ? s.windowDays : 30;
      if (facts.addedAtDays == null) return no('no added date for this title');
      const d = Math.max(0, Math.floor(facts.addedAtDays));
      return facts.addedAtDays <= w
        ? { show: true, text: 'NEW', note: `added ${d}d ago (window ${w}d)` }
        : no(`added ${d}d ago — outside the ${w}-day window`);
    }
    case 'decade': {
      // A season's year is its episodes' year — the decade badge is
      // a movie/show identity claim and stays off seasons by design.
      if (facts.type === 'season') return no('decade belongs to movies and shows, not seasons');
      const y = Number(facts.year || 0);
      if (!(y > 0)) return no('no year for this title');
      return { show: true, text: `${Math.floor(y / 10) * 10}s`, note: `decade of ${y}` };
    }
    case 'video-codec': {
      if (!facts.videoCodec) return no('no video codec data for this title');
      const name = videoCodecName(facts.videoCodec);
      if (!name) return no(`unmapped codec '${facts.videoCodec}'`);
      return { show: true, text: name, note: 'codec of the winning-resolution file' };
    }
    case 'streaming': {
      // facts.streamingByRegion is the whole TMDb watch/providers
      // map (one cached response); the REGION is a layout setting,
      // so the pick happens here, not in the facts layer. Flatrate
      // only — rent/buy was never read into the fact.
      const region = s.region || 'US';
      const byRegion = facts.streamingByRegion;
      if (!byRegion || typeof byRegion !== 'object') return no('needs a TMDb key and a TMDb match');
      const list = Array.isArray(byRegion[region]) ? byRegion[region] : [];
      if (!list.length) return no(`no TMDb streaming data for ${region}`);
      const names = list.map((nm) => streamingDisplayName(nm));
      const picked = s.mode === 'all' ? names.slice(0, 3) : names.slice(0, 1);
      return {
        show: true,
        text: picked.map((nm) => String(nm).toUpperCase()).join(' • '),
        note: `streaming (${region}) via TMDb${names.length > picked.length ? ` — ${names.length} services, showing ${picked.length}` : ''}`,
      };
    }
    case 'studio': {
      if (!facts.studio) return no('Plex states no studio for this title');
      return { show: true, text: String(facts.studio), note: 'studio from Plex' };
    }
    case 'genre': {
      // Two modes (family setting):
      //   first — the original behavior: the title's first genre
      //     (or first two) from Plex's tags, in Plex's order.
      //   only  — ISOLATION: fire ONLY when the title's genre list
      //     contains the ONE chosen genre (case-insensitive match;
      //     the drawn text is the genre exactly as Plex states it
      //     on THIS item). A horror-comedy earns the Horror badge;
      //     titles without it stay clean. One isolated genre per
      //     template is the accepted boundary — there is one
      //     settings block per family, so no multi-instance
      //     machinery exists to isolate two.
      // The pill/banner presentation is a layout concern; the
      // verdict text is identical either way.
      const gs = (Array.isArray(facts.genres) ? facts.genres : []).filter((g) => g != null && String(g) !== '');
      if (s.mode === 'only') {
        const want = String(s.genre || '').trim();
        if (!want) return no('isolation mode needs a chosen genre — set one in the family settings');
        if (!gs.length) return no('no genre tags on this title');
        const hit = gs.find((g) => String(g).trim().toLowerCase() === want.toLowerCase());
        if (!hit) return no(`no '${want}' among this title's genres (${gs.map((g) => String(g)).join(', ')})`);
        return {
          show: true,
          text: String(hit).toUpperCase(),
          note: `isolated genre from Plex — title's genres: ${gs.map((g) => String(g)).join(', ')}`,
        };
      }
      if (!gs.length) return no('no genre tags on this title');
      const count = s.count === 2 ? 2 : 1;
      return {
        show: true,
        text: gs.slice(0, count).map((g) => String(g).toUpperCase()).join(' • '),
        note: `genre${count === 2 && gs.length > 1 ? 's' : ''} from Plex`,
      };
    }
    case 'hdr': {
      // The dynamic range as its own badge — composed ONLY from
      // the stream flags the facts layer proves (DV / HDR10 / HLG),
      // in that order. SDR (or no stream data) draws nothing.
      // HDR10+ is never claimed: there is no fact for it.
      const parts = [];
      if (facts.dv === true) parts.push('DV');
      if (facts.hdr10 === true) parts.push('HDR10');
      if (facts.hlg === true) parts.push('HLG');
      if (!parts.length) return no(facts.hdrAny === true
        ? 'HDR present but its kind is not one the flags name'
        : 'no HDR stream detected (SDR or unstated)');
      return { show: true, text: parts.join(' • '), note: 'dynamic range from the stream flags' };
    }
    case 'channels': {
      // The best track's channel layout as its own badge — the
      // same audioChannelLabel the audio family's full-detail mode
      // appends. Channels ride WITH the best-scored stream (facts
      // layer), never a different track's count.
      const ch = audioChannelLabel(facts.bestAudioChannels);
      if (!ch) return no(facts.bestAudioLabel || facts.bestAudioCodec
        ? 'best audio track states no channel count'
        : 'no audio streams found on this title');
      return { show: true, text: ch, note: 'channels of the best audio track' };
    }
    case 'aspect-ratio': {
      // The winning version's ratio (facts layer, riding with the
      // winning tier like the video codec). No ratio fact → hide.
      // Seasons never carry the fact: the season aggregation
      // declines to derive one because episodes can disagree
      // about their frame — dark beats a borrowed ratio.
      if (facts.type === 'season') return no('season aspect is not aggregated — episodes can disagree about their frame');
      const text = aspectRatioText(facts.aspectRatio);
      if (!text) return no('Plex states no aspect ratio for the winning version');
      return { show: true, text, note: 'aspect ratio of the winning version' };
    }
    case 'franchise': {
      // Movies only: TMDb's belongs_to_collection name, verbatim.
      // A show has no TMDb collection fact by construction; a
      // standalone movie's record names no collection either —
      // both hide, with the reason in the note.
      if (facts.type && facts.type !== 'movie') return no('collections are a movie fact (TMDb belongs-to-collection)');
      if (!facts.franchiseName) return no('no TMDb collection for this title — standalone, unmatched, or no TMDb key');
      return { show: true, text: String(facts.franchiseName), note: 'collection named by TMDb' };
    }
    case 'saga-position': {
      // Movies only: this film's place in its TMDb collection,
      // counted by release date among ALL the parts TMDb lists —
      // the total is the saga as TMDb states it, owned or not
      // (the note says so). The facts layer sets sagaTotal
      // whenever the collection's parts list exists and
      // sagaPosition only when this movie was found among them,
      // so the three dark cases are distinct and each names
      // itself: no collection at all, a single-part collection
      // (no position to state), and not-found-among-parts.
      if (facts.type && facts.type !== 'movie') return no('saga position is a movie fact (TMDb collection)');
      const total = Number(facts.sagaTotal);
      if (!Number.isFinite(total) || total <= 0) return no('no TMDb collection for this title — standalone, unmatched, or no TMDb key');
      if (total === 1) return no('TMDb lists a single film for this collection — no position to state');
      const pos = Number(facts.sagaPosition);
      if (!Number.isFinite(pos) || pos < 1) return no('this title was not found among the parts TMDb lists for its collection');
      return {
        show: true,
        text: `MOVIE ${pos} OF ${total}`,
        note: `by release date among the ${total} parts TMDb lists${facts.franchiseName ? ` for "${facts.franchiseName}"` : ''} — the saga as TMDb states it, owned or not`,
      };
    }
    case 'origin': {
      // Original language + first origin country, as TMDb states
      // them — composed only of the parts the record states (a
      // language with no country, or countries with no language,
      // still badge on what exists). Nothing stated → hidden.
      const lang = originLanguageText(facts.originalLanguage);
      const countries = Array.isArray(facts.originCountries) ? facts.originCountries.filter((c) => c != null && String(c) !== '') : [];
      const parts = [];
      if (lang) parts.push(lang);
      if (countries.length) parts.push(String(countries[0]).toUpperCase());
      if (!parts.length) return no('TMDb states no original language or origin country for this title');
      return {
        show: true,
        text: parts.join(' · '),
        note: `origin via TMDb${countries.length > 1 ? ` (first of ${countries.length} origin countries)` : ''}`,
      };
    }
    case 'collection': {
      // The Plex collections THIS title belongs to on the server —
      // the server's own membership (facts.collections, read from
      // the item's Collection tags), distinct from the TMDb saga
      // the franchise family names. Membership is a movie/show
      // fact: episode/season facts never carry it (facts layer),
      // and the gate below states the rule regardless.
      if (facts.type === 'episode' || facts.type === 'season') {
        return no('collections belong to the movie or show, not episodes or seasons');
      }
      const list = (Array.isArray(facts.collections) ? facts.collections : [])
        .filter((c) => c != null && String(c).trim() !== '')
        .map((c) => String(c));
      if (!list.length) return no('not in any Plex collection on this server');
      if (list.length === 1) return { show: true, text: list[0], note: 'Plex collection on this server' };
      return {
        show: true,
        text: `${list[0]} +${list.length - 1}`,
        note: `in ${list.length} Plex collections: ${list.join(' · ')}`,
      };
    }
    case 'family': {
      // The friendly translation of the content-rating fact: a
      // ribbon reading FAMILY when the title's OWN rating is in
      // the family set. The set is a template setting (v1.15):
      // the configured `ratings` list when the template carries
      // one, else the shipped default for the item's media type —
      // G / PG for movies, TV-Y / TV-Y7 / TV-G for shows — so
      // unconfigured templates judge (and word their notes)
      // exactly as they always have. Matching is EXACT: TV-Y7-FV
      // is not TV-Y7 unless TV-Y7-FV is itself in the set. Any
      // other rating stays dark and the note names the actual
      // rating AND the set it was judged against; no rating at
      // all names its absence; an explicitly empty set is its
      // own state (the family can never fire) with its own note.
      // Judged at the top level only: seasons and episodes stay
      // dark even when their records carry a rating (the episode
      // gate above already returns for episodes; the season check
      // here states the same rule for seasons).
      if (facts.type === 'season' || facts.type === 'episode') {
        return no('family rating is judged on the movie or show, not its seasons or episodes');
      }
      const rating = String(facts.contentRating || '').trim().toUpperCase();
      if (!rating) return no('no content rating for this title');
      const set = Array.isArray(s.ratings)
        ? s.ratings.map((r) => String(r).trim().toUpperCase()).filter(Boolean)
        : facts.type === 'show' ? ['TV-Y', 'TV-Y7', 'TV-G'] : ['G', 'PG'];
      if (!set.length) return no(`rated ${rating} — the family set is empty (add ratings in the family settings)`);
      if (!set.includes(rating)) return no(`rated ${rating} — outside the family set (${set.join(' / ')})`);
      return { show: true, text: 'FAMILY', note: `content rating ${rating} is a family rating` };
    }
    case 'leaving': {
      // Leaving soon (v1.12): the banner reads the app's OWN Leaving
      // list — dates set in the app (the Leaving soon card on the
      // Apply page), carried in facts as leavingSet / leavingDate /
      // leavingDays, the day count judged by the facts layer against
      // the app's local date. No entry → dark, and the note says so:
      // no date is ever invented for this family.
      //
      // APPLY-TIME TRUTH: a badged poster shows the list as it stood
      // when the overlay was applied. Changing a date afterwards
      // redraws nothing until the title is re-applied — the
      // scheduled-rescan feature will close that loop later; it is
      // deliberately not part of this family.
      //
      // Movies and shows only: the manager dates top-level titles
      // (the episode gate above already returns for episodes; the
      // season check here states the same rule for seasons).
      if (facts.type === 'season' || facts.type === 'episode') {
        return no('leaving dates are set on the movie or show, not its seasons or episodes');
      }
      if (facts.leavingSet !== true) return no('no leaving date is set for this title');
      const usDate = leavingDateText(facts.leavingDate);
      const days = Number(facts.leavingDays);
      if (!usDate || !Number.isFinite(days)) {
        return no('a leaving date is set but its day count is unavailable — the banner is not guessed');
      }
      if (days < 0) {
        return no(`the leaving date ${usDate} has passed — clear the entry in the Leaving soon list on the Apply page`);
      }
      if (s.mode === 'date') {
        return {
          show: true,
          text: `LEAVING ON ${usDate}`,
          note: `leaves on ${usDate} — ${days} ${days === 1 ? 'day' : 'days'} away`,
        };
      }
      const win = typeof s.windowDays === 'number' && Number.isFinite(s.windowDays) ? s.windowDays : 30;
      if (days > win) {
        return no(`leaves ${usDate} — ${days} days away, outside the ${win}-day soon window`);
      }
      return {
        show: true,
        text: 'LEAVING SOON',
        note: `leaves ${usDate} — ${days} ${days === 1 ? 'day' : 'days'} away, inside the ${win}-day soon window`,
      };
    }
  }
  return no(`unknown family: ${id}`);
}

// ---------------------------------------------------------------------------
// SIGNATURE LAYOUT (pure). The Signature variant drops the PNG shells:
// every family part becomes a procedural spec — a micro-label over a
// value, a shape, and a width MEASURED from the text itself, so a
// "WEB-DL" source pill is narrower than a "Dolby TrueHD • 7.1 • ENG"
// audio pill. Heights are fixed per shape (fractions of poster height
// at size 1); the renderer auto-fits type into whatever rect results.
// ---------------------------------------------------------------------------

const SIGNATURE_FONT_STACK = '"Aptos Display","Segoe UI Variable Display","Segoe UI",system-ui,sans-serif';
const SIGNATURE_MICRO_LABELS = {
  resolution: 'RESOLUTION', audio: 'AUDIO', edition: 'EDITION',
  status: 'STATUS', ribbon: 'HONOR', 'content-rating': 'RATING',
  languages: 'LANGUAGES', subtitles: 'SUBTITLES', versions: 'VERSIONS',
  source: 'SOURCE', details: 'DETAILS', runtime: 'RUNTIME',
  'watch-status': 'WATCH', new: 'ADDED', decade: 'DECADE',
  'video-codec': 'CODEC', streaming: 'STREAMING', studio: 'STUDIO',
  genre: 'GENRE', hdr: 'HDR', channels: 'CHANNELS',
  'aspect-ratio': 'ASPECT', franchise: 'FRANCHISE', origin: 'ORIGIN',
  collection: 'COLLECTION', family: 'RATED',
  'saga-position': 'SAGA', 'quick-watch': 'QUICK',
  leaving: 'LEAVING',
};
const SIGNATURE_RATING_MICRO_LABELS = {
  tmdb: 'TMDB', imdb: 'IMDB', 'rt-critic': 'RT', 'rt-audience': 'RT AUDIENCE', metascore: 'METASCORE',
};
// Badge heights in poster px at size 1 (the composite canvas is
// 1000x1500; hFrac = px/1500).
const SIGNATURE_HEIGHT_PX = { pill: 86, chip: 76, ribbon: 92, ratingbox: 96 };

// ------------------------------------------------------------------
// BADGE PROPORTION + LEGIBILITY RULES (v1.9.1 — one coherent pass
// over every style, driven by a live render the owner rejected:
// a text-measured languages badge had grown into a top slab, and
// the smallest chips rendered as unreadable specks).
//   BADGE_MAX_WFRAC — no single badge may span more than this share
//     of the poster width. Every badge is a corner badge in the
//     normal flow; no family gets a banner/slab treatment, however
//     long its text runs (the renderer truncates gracefully inside
//     the cap instead).
//   LEGIBILITY_MIN_TEXT_PX — the legibility floor for CHIP text
//     (the ratings slots, the small-chip row), in design px on the
//     canonical 1000x1500 poster. A chip whose value text would
//     render below this size at its slot size does not render at
//     all — absent beats unreadable — and the signature chip
//     renderer never auto-fits chip text below it (it ellipsizes
//     at the floor). The chip backdrop/contrast treatment is the
//     frosted body + gradient + stroke + text shadow the signature
//     renderer already draws (classic chips: their shell art plus
//     the shadowed text pass in drawComposite).
// ------------------------------------------------------------------
const BADGE_MAX_WFRAC = 0.46;
const LEGIBILITY_MIN_TEXT_PX = 24;

// ------------------------------------------------------------------
// GEOMETRY (v1.10 — episode overlays). The layout pass works in a
// DESIGN SPACE: the canonical 1000×1500 portrait space every
// constant in this file was authored against, or — for episode
// facts — a 1600×900 LANDSCAPE space matching the episode still's
// 16:9 frame. One engine, two geometries: placements are fractions
// in both, and every design-px constant scales by the space's
// height (unit = H/1500), so a badge keeps its share of the frame's
// height and its proportions on either canvas — on the wider frame
// it simply claims a smaller share of the width. BADGE_MAX_WFRAC
// is already a fraction, so it binds both spaces unchanged;
// LEGIBILITY_MIN_TEXT_PX is a px constant, so it scales (see
// legibilityFloorPx).
// ------------------------------------------------------------------
const CANONICAL_GEOM = { w: 1000, h: 1500 };
const LANDSCAPE_GEOM = { w: 1600, h: 900 };
function geometryFor(facts) {
  return facts && facts.type === 'episode' ? LANDSCAPE_GEOM : CANONICAL_GEOM;
}
// The legibility floor in a geometry's own design px.
function legibilityFloorPx(geom) {
  return LEGIBILITY_MIN_TEXT_PX * ((geom && geom.h ? geom.h : CANONICAL_GEOM.h) / CANONICAL_GEOM.h);
}

// EPISODE BADGE CAP (v1.10): a small wide still carries fewer
// badges than a poster before it turns into noise — at most this
// many FAMILIES draw on one episode. When more families fire, the
// template order (FAMILY_DEFS) decides which draw, and the rest
// report "won't draw" with the cap as the reason — the same honesty
// convention as the legibility floor. A starting default: the
// owner tunes it from the proof renders.
const EPISODE_BADGE_CAP = 4;

// signatureTextWidth(text, px, weightClass) — a pure TEXT-WIDTH
// ESTIMATOR. It sums per-character advance classes (narrow i/l/space,
// wide M/W, digits, caps…) times the pixel size and a weight factor.
// It is deliberately approximate: real glyph advances vary by font,
// and the renderer's measureText auto-fit is the backstop that
// guarantees nothing ever overflows its badge. The estimator only
// needs to be close enough that badges look sized to their content.
function signatureTextWidth(text, px, weightClass) {
  const size = Number(px) > 0 ? Number(px) : 16;
  const factor = weightClass === 'micro' ? 0.94 : weightClass === 'value' ? 1.05 : 1;
  let em = 0;
  for (const ch of String(text == null ? '' : text)) {
    if (ch === ' ') em += 0.32;
    else if (ch === '•' || ch === '·') em += 0.36;
    else if (ch === '.') em += 0.30;
    else if (ch === ',') em += 0.32;
    else if (ch === ':') em += 0.34;
    else if (ch === '-' || ch === '–' || ch === '—') em += 0.44;
    else if (ch === '/') em += 0.46;
    else if (ch === '%') em += 0.94;
    else if (ch === '+') em += 0.66;
    else if (ch === "'") em += 0.26;
    else if (ch >= '0' && ch <= '9') em += 0.60;
    else if (ch >= 'A' && ch <= 'Z') em += ch === 'I' ? 0.34 : ch === 'M' ? 0.88 : ch === 'W' ? 0.96 : 0.67;
    else if (ch >= 'a' && ch <= 'z') em += ch === 'i' || ch === 'l' ? 0.28 : ch === 'm' ? 0.84 : ch === 'w' ? 0.80 : 0.55;
    else em += 0.58;
  }
  return em * size * factor;
}

// signatureSpecForPart: one evaluated family part -> signature spec.
// Width model (design px, size folded in via s.size): horizontal
// padding + an icon zone (roughly square with the badge height) +
// the wider of the micro-label and value lines. Ratings chips carry
// icon + value only. Caps keep one badge from eating the poster:
// pill/ribbon/ratingbox ≤ BADGE_MAX_WFRAC of poster width, ratings
// chip ≤ 30%.
function signatureSpecForPart(def, s, p, geom) {
  const g = geom || CANONICAL_GEOM;
  // Design-px unit of the geometry: every px constant below was
  // authored on the canonical 1000×1500 space and scales by the
  // space's height, so the spec comes out in the geometry's own px
  // and hFrac is the same share of the frame in either geometry.
  const u = g.h / CANONICAL_GEOM.h;
  const scale = (s.size || 1) * u;
  const isRating = def.id === 'ratings';
  // Ribbon-shaped families: the ribbon-native ones (status, honor,
  // collection, family, leaving) plus the genre family in its BANNER
  // presentation — same plumbing as the Collection banner.
  const shape = def.id === 'content-rating' ? 'ratingbox'
    : (def.id === 'status' || def.id === 'ribbon' || def.id === 'collection' || def.id === 'family' || def.id === 'leaving'
      || (def.id === 'genre' && s.presentation === 'banner')) ? 'ribbon' : 'pill';
  const hBase = isRating ? SIGNATURE_HEIGHT_PX.chip : SIGNATURE_HEIGHT_PX[shape];
  const hPx = hBase * scale;
  const micro = isRating
    ? (SIGNATURE_RATING_MICRO_LABELS[p.slot] || String(p.slot || '').toUpperCase())
    : (SIGNATURE_MICRO_LABELS[def.id] || def.name.toUpperCase());
  const value = String(p.text == null ? '' : p.text);
  const valueW = signatureTextWidth(value, (isRating ? 29 : shape === 'ratingbox' ? 34 : 29) * scale, 'value');
  const microW = signatureTextWidth(micro, 11 * scale, 'micro')
    + Math.max(0, micro.length - 1) * 1.3 * scale; // letter-spacing allowance
  let widthPx;
  if (isRating) widthPx = 14 * scale + hPx * 0.60 + 8 * scale + valueW + 14 * scale;
  else if (shape === 'ratingbox') widthPx = Math.max(112 * scale, 18 * scale + Math.max(microW, valueW) + 18 * scale);
  else widthPx = 16 * scale + hPx * 0.72 + 10 * scale + Math.max(microW, valueW) + 16 * scale;
  widthPx = Math.min(widthPx, (isRating ? 0.30 : BADGE_MAX_WFRAC) * g.w);
  return {
    badgeId: p.badgeId, text: value, slot: p.slot || undefined,
    microLabel: micro, shape, hFrac: hPx / g.h, widthPx,
  };
}

// layoutFamilyDrawables(families, facts, aspectOf) → { verdicts,
// drawables } for ONE item. verdicts: one row per ENABLED family
// ({ family, name, show, text, note }) for the truth rail. drawables:
// concrete placements in the placement shape the rest of the pipeline
// already speaks ({ badgeId, x, y, wFrac, opacity, label, family }),
// x/y/wFrac as fractions of poster width/height. The design space is
// the item's geometry (geometryFor): the canonical 1000×1500 poster
// space, or 1600×900 landscape for episode stills — margins, gaps
// and px sizes scale by the space (see GEOMETRY above), families
// sharing a corner stack in FAMILY_STACK_ORDER with a consistent
// gap; a family's own dx/dy (poster fractions, set by canvas drag)
// shift it from its slot. Episode items additionally obey
// EPISODE_BADGE_CAP (below). aspectOf(badgeId) → height/width of
// the shell art (0.5 fallback).
function layoutFamilyDrawables(families, facts, aspectOf) {
  const aspect = typeof aspectOf === 'function' ? aspectOf : () => 0.5;
  const geom = geometryFor(facts);
  const W = geom.w, H = geom.h, unit = H / CANONICAL_GEOM.h;
  const verdicts = [];
  const byCorner = { tl: [], tr: [], bl: [], br: [] };
  // Runtime dedupe: with the runtime family enabled, details omits
  // its runtime part (the runtime has its own badge). Computed once
  // from the WHOLE layout — a standalone details evaluation never
  // sees the flag and keeps its full text.
  const runtimeOn = familySettings(families, 'runtime').on;
  // Episode cap (v1.10): families are counted in template order as
  // they earn drawables; past EPISODE_BADGE_CAP a firing family is
  // marked undrawn with the cap as its reason — never silently cut.
  const isEpisode = !!facts && facts.type === 'episode';
  let episodeDrawn = 0;
  const capNote = `episode badge cap (${EPISODE_BADGE_CAP}) reached — not drawn`;
  for (const def of FAMILY_DEFS) {
    const s = familySettings(families, def.id);
    if (!s.on) continue;
    const v = def.id === 'details' && runtimeOn
      ? evaluateFamily(def.id, { ...s, __suppressRuntime: true }, facts)
      : evaluateFamily(def.id, s, facts);
    const verdict = { family: def.id, name: def.name, show: v.show, text: v.text, note: v.note };
    verdicts.push(verdict);
    if (!v.show) continue;
    if (isEpisode) {
      if (episodeDrawn >= EPISODE_BADGE_CAP) {
        verdict.undrawn = true;
        verdict.note = verdict.note ? `${verdict.note} · ${capNote}` : capNote;
        continue;
      }
      episodeDrawn++;
    }
    let parts = v.slots
      ? v.slots.map((sl) => ({ badgeId: sl.badgeId, text: sl.text, slot: sl.source }))
      : [{ badgeId: def.shell, text: v.text, slot: null }];
    // Genre BANNER presentation (v1.11.1): the same verdict text
    // on the ribbon shell instead of the pill — the Collection
    // banner's plumbing. Presentation is layout only; the
    // evaluator never hears about it. (Signature needs no swap:
    // its spec takes the ribbon shape for the same setting.)
    if (def.id === 'genre' && s.presentation === 'banner') {
      parts = parts.map((p) => ({ ...p, badgeId: 'ribbon-genre' }));
    }
    // Aurora variant: same parts, richer art ids (identical canvas
    // sizes, so the stacking math below is untouched).
    if (s.variant === 'aurora') {
      parts = parts.map((p) => ({ ...p, badgeId: auroraShell(p.badgeId) }));
    }
    // Signature variant: same evaluated parts, but each becomes a
    // PROCEDURAL spec (micro-label, shape, text-measured width) — no
    // PNG shell is loaded for these drawables at all.
    const sigParts = s.variant === 'signature'
      ? parts.map((pt) => signatureSpecForPart(def, s, pt, geom))
      : null;
    byCorner[s.corner].push({ def, s, v, verdict, parts, sigParts });
  }
  const drawables = [];
  // Margins and gaps in the geometry's design px: the canonical 20px
  // margin is 2% of the space's width horizontally and scales by the
  // height unit vertically; stack/row gaps scale by the unit, like
  // every other design px (GEOMETRY note above).
  const MX = 20 * (W / CANONICAL_GEOM.w), MY = 20 * unit, GAP = 14 * unit, ROWGAP = 10 * unit;
  // Legibility-floor bookkeeping (v1.9.1): a family whose chips ALL
  // fall below the floor draws nothing, and its verdict is marked
  // undrawn so the dry run and the verdicts rail say so instead of
  // claiming a badge that never renders. A partial drop (some slots
  // kept) amends the verdict note the same way. The floor is the
  // geometry's own (legibilityFloorPx): 24px canonical, scaled on
  // the landscape space.
  const floorPx = legibilityFloorPx(geom);
  const floorNote = `chip text below the ${Number(floorPx.toFixed(1))}px legibility floor at this size — not drawn`;
  const markUndrawn = (v) => { v.undrawn = true; v.note = v.note ? `${v.note} · ${floorNote}` : floorNote; };
  const markPartial = (v) => {
    v.partialDrop = true;
    const n2 = 'some chips below the legibility floor — not drawn';
    v.note = v.note ? `${v.note} · ${n2}` : n2;
  };
  for (const corner of Object.keys(byCorner)) {
    const queue = byCorner[corner].slice().sort(
      (a, b) => FAMILY_STACK_ORDER[corner].indexOf(a.def.id) - FAMILY_STACK_ORDER[corner].indexOf(b.def.id));
    let used = 0; // stacked height consumed in this corner (design px)
    for (const { def, s, verdict, parts, sigParts } of queue) {
      if (sigParts) {
        // Dynamic widths from the text estimator. If the row (badges
        // + gaps) would exceed 92% of the poster width, every badge
        // in the row scales down proportionally; the renderer's
        // measureText auto-fit is the backstop inside each badge.
        let widths = sigParts.map((sp) => sp.widthPx);
        let rowW = widths.reduce((a, b) => a + b, 0) + ROWGAP * (widths.length - 1);
        if (rowW > 0.92 * W) {
          const fit = (0.92 * W - ROWGAP * (widths.length - 1)) / widths.reduce((a, b) => a + b, 0);
          widths = widths.map((w) => w * fit);
          rowW = 0.92 * W;
        }
        let live = sigParts.map((sp, i) => ({ sp, w: widths[i] }));
        // Legibility floor (v1.9.1): ratings chips whose value text
        // would render below the floor (legibilityFloorPx — 24px
        // canonical, scaled by geometry) do not render. The nominal
        // size mirrors the renderer's chip formula (34% of badge
        // height). All chips in a row share a height, so this is
        // all-or-nothing in practice.
        if (def.id === 'ratings') {
          const kept = live.filter(({ sp }) => (sp.hFrac * H) * 0.34 >= floorPx);
          if (!kept.length) { markUndrawn(verdict); continue; }
          if (kept.length < live.length) markPartial(verdict);
          live = kept;
          rowW = live.reduce((a, e) => a + e.w, 0) + ROWGAP * (live.length - 1);
        }
        const rowH = Math.max(...live.map(({ sp }) => sp.hFrac * H));
        const topS = corner === 'tl' || corner === 'tr';
        const leftS = corner === 'tl' || corner === 'bl';
        let yS = topS ? MY + used : H - MY - used - rowH;
        let xS = leftS ? MX : W - MX - rowW;
        xS += s.dx * W; yS += s.dy * H;
        live.forEach(({ sp, w }) => {
          drawables.push({
            badgeId: sp.badgeId,
            x: xS / W, y: yS / H, wFrac: w / W, hFrac: sp.hFrac, opacity: 1,
            label: sp.text, text: sp.text, family: def.id, slot: sp.slot,
            renderMode: 'signature', microLabel: sp.microLabel, shape: sp.shape,
            rule: { kind: 'family', family: def.id },
          });
          xS += w + ROWGAP;
        });
        used += rowH + GAP;
        continue;
      }
      // Shell widths are canonical design px scaled by the
      // geometry's height unit (NOT by its width): a shell's height
      // follows its width through its aspect, so height-unit scaling
      // is what keeps the badge's share of the frame's HEIGHT — and
      // the art undistorted — on a wider landscape frame. The genre
      // banner draws at ribbon width (GENRE_BANNER_WFRAC), not the
      // genre pill's width.
      const famW = def.id === 'genre' && s.presentation === 'banner' ? GENRE_BANNER_WFRAC : FAMILY_WIDTH[def.id];
      const widths = parts.map((p) => famW * CANONICAL_GEOM.w * unit * s.size);
      const heights = parts.map((p, i) => widths[i] * (aspect(p.badgeId) || 0.5));
      let live = parts.map((p, i) => ({ p, w: widths[i], h: heights[i] }));
      // Legibility floor (v1.9.1): ratings slot chips whose drawn
      // text would fall below the floor (legibilityFloorPx) do not
      // render. The nominal size mirrors drawComposite's text
      // formula (42% of chip height); slot aspects differ per
      // glyph, so this can drop one slot and keep its neighbours.
      if (def.id === 'ratings') {
        const kept = live.filter(({ h }) => h * 0.42 >= floorPx);
        if (!kept.length) { markUndrawn(verdict); continue; }
        if (kept.length < live.length) markPartial(verdict);
        live = kept;
      }
      const rowH = Math.max(...live.map((e) => e.h));
      const rowW = live.reduce((a, e) => a + e.w, 0) + ROWGAP * (live.length - 1);
      const top = corner === 'tl' || corner === 'tr';
      const left = corner === 'tl' || corner === 'bl';
      let y = top ? MY + used : H - MY - used - rowH;
      let x = left ? MX : W - MX - rowW;
      x += s.dx * W; y += s.dy * H;
      live.forEach(({ p, w }) => {
        drawables.push({
          badgeId: p.badgeId,
          x: x / W, y: y / H, wFrac: w / W, opacity: 1,
          label: p.text, family: def.id, slot: p.slot || undefined,
          rule: { kind: 'family', family: def.id },
        });
        x += w + ROWGAP;
      });
      used += rowH + GAP;
    }
  }
  return { verdicts, drawables };
}

// evaluateRun(items, factsByKey, layout, aspectOf?) → the HONEST dry-run.
// PURE. This replaces buildDryRun, which counted every placement for
// every item without evaluating a single rule — the dry-run lied, and
// the apply step then rewrote posters that had earned zero badges (or
// failed them silently). Here a badge only counts when evaluateRule
// says it fires against the item's REAL facts.
//   items:      [{ ratingKey, title, ... }] — the resolved targets.
//   factsByKey: { [ratingKey]: facts } from overlay:itemFacts. An entry
//               of { __error: 'message' } — or a missing key — means
//               the metadata fetch FAILED for that item. It lands in
//               failedFacts, never in skipped: "couldn't check" and
//               "checked, nothing applies" are different answers and
//               the user must see them separately.
//   layout:     EITHER the legacy badge layout [{ badgeId, rule, ... }]
//               OR the family layout { families, custom: [...] } —
//               families are judged by evaluateFamily (one dry-run
//               entry per fired family; ratings yields one per slot),
//               custom placements by evaluateRule, as before.
//   aspectOf:   optional (badgeId) → shell aspect for family stacking.
// Returns:
//   willApply:   [{ item, badges: [{ badgeId, label, family? }] }] — ≥1 fires
//   skipped:     [{ item, reason }] — facts fine, zero badges fire
//   failedFacts: [{ item, error }] — facts unavailable, item untouched
// The label is the exact text a value badge will draw (null for
// art-only badges); the skipped reason names each badge and why not.
function evaluateRun(items, factsByKey, layout, aspectOf) {
  const willApply = [], skipped = [], failedFacts = [];
  let families = null;
  let places = [];
  if (Array.isArray(layout)) {
    places = layout; // legacy badge-set shape
  } else if (layout && typeof layout === 'object') {
    families = layout.families || null;
    places = Array.isArray(layout.custom) ? layout.custom
      : (Array.isArray(layout.placements) ? layout.placements : []);
  }
  const enabledFamilies = families
    ? FAMILY_DEFS.filter((d) => familySettings(families, d.id).on).length
    : 0;
  for (const item of items || []) {
    const key = String(item.ratingKey);
    const facts = factsByKey ? factsByKey[key] : undefined;
    if (!facts || facts.__error) {
      failedFacts.push({ item, error: facts?.__error || 'metadata unavailable' });
      continue;
    }
    if (!enabledFamilies && !places.length) {
      skipped.push({ item, reason: Array.isArray(layout) ? 'no badges in the layout' : 'no families or badges enabled in the layout' });
      continue;
    }
    const badges = [];
    const misses = [];
    if (families) {
      const laid = layoutFamilyDrawables(families, facts, aspectOf);
      for (const v of laid.verdicts) {
        if (!v.show) misses.push(`${v.name}: ${v.note || 'condition not met'}`);
        else if (v.undrawn) misses.push(`${v.name}: ${v.note || 'badge not drawn'}`);
      }
      for (const d of laid.drawables) {
        badges.push({ badgeId: d.badgeId, label: d.label ?? null, family: d.family });
      }
    }
    for (const p of places) {
      const ev = evaluateRule(p.rule, facts);
      if (ev.show) badges.push({ badgeId: p.badgeId, label: ev.label ?? null });
      else misses.push(`${p.badgeId}: ${ev.note || 'condition not met'}`);
    }
    if (badges.length) willApply.push({ item, badges });
    else skipped.push({ item, reason: misses.join('; ') });
  }
  return { willApply, skipped, failedFacts };
}

// isImageBytes(buf) → { ok, type } — magic-byte verification for the backup
// gate. Buffer/Uint8Array. PNG: 89 50 4E 47. JPEG: FF D8 FF.
function isImageBytes(buf) {
  const b = buf instanceof Uint8Array ? buf : null;
  if (!b || b.length < 8) return { ok: false, type: '' };
  if (b[0] === 0x89 && b[1] === 0x50 && b[2] === 0x4e && b[3] === 0x47) return { ok: true, type: 'png' };
  if (b[0] === 0xff && b[1] === 0xd8 && b[2] === 0xff) return { ok: true, type: 'jpg' };
  return { ok: false, type: '' };
}

// sidecarFor(...) → the .json sidecar shape stored next to every backup.
// The sidecar is what makes restore and re-apply possible months later.
function sidecarFor({ ratingKey, title, sourceUrl, badgeSet, posterHash, contentType, runId }) {
  return {
    version: 1,
    ratingKey: String(ratingKey),
    title: title || '',
    backedUpAt: new Date().toISOString(),
    sourceUrl: sourceUrl || '',
    contentType: contentType || '',
    posterHash: posterHash || '', // sha256 of the ORIGINAL poster bytes
    badgeSet: badgeSet ? { name: badgeSet.name || '', placements: badgeSet.placements || [], ...(badgeSet.families ? { families: badgeSet.families } : {}) } : null,
    runId: runId || '',
  };
}

// drawComposite(canvas, posterImg, drawables) — BROWSER ONLY.
// drawables: [{ img, rect: {x,y,w,h}, opacity, text? }]. Draws the poster,
// then each badge with globalAlpha; optional centered text (pill counts,
// ribbon-blank labels) in a clean sans at 40% of badge height.

// ---------------------------------------------------------------------------
// SIGNATURE RENDERER (browser-only, like drawComposite below). Draws a
// procedural frosted-glass badge: the poster itself, blurred and
// darkened behind a family-tinted gradient, a vector icon in an
// accent-tinted tile, a letter-spaced micro-label over an extra-bold
// value. NO image assets — that is the point of the variant. State
// hygiene: everything touched (filter, shadow, letterSpacing, font,
// alpha) is set inside save/restore and reset on the way out, so the
// classic image path that shares this canvas is never contaminated.
// ---------------------------------------------------------------------------

const SIGNATURE_ACCENTS = {
  resolution: '#22d3ee', audio: '#a78bfa', edition: '#f472b6',
  languages: '#2dd4bf', subtitles: '#38bdf8', 'content-rating': '#fbbf24',
  versions: '#94a3b8', source: '#f59e0b', details: '#e2e8f0',
  runtime: '#93c5fd',
  'watch-status': '#facc15', new: '#34d399', decade: '#c084fc',
  'video-codec': '#818cf8', streaming: '#fb923c', studio: '#a8a29e',
  genre: '#f87171', hdr: '#fde047', channels: '#4ade80',
  'aspect-ratio': '#f0abfc', franchise: '#fb7185', origin: '#fdba74',
  collection: '#f9a8d4', family: '#34d399',
  'saga-position': '#fda4af', 'quick-watch': '#2dd4bf',
  leaving: '#f87171',
};
// Deep navy/charcoal gradient tints per family (top, bottom).
const SIGNATURE_TINTS = {
  resolution: ['rgba(8,47,73,0.90)', 'rgba(3,10,24,0.85)'],
  audio: ['rgba(58,30,110,0.88)', 'rgba(13,8,32,0.86)'],
  edition: ['rgba(90,20,60,0.88)', 'rgba(24,6,20,0.86)'],
  languages: ['rgba(6,60,54,0.88)', 'rgba(3,20,20,0.86)'],
  subtitles: ['rgba(8,51,84,0.88)', 'rgba(3,14,28,0.86)'],
  'content-rating': ['rgba(84,58,4,0.90)', 'rgba(22,16,3,0.87)'],
  versions: ['rgba(45,55,72,0.88)', 'rgba(10,14,22,0.86)'],
  source: ['rgba(84,52,3,0.90)', 'rgba(22,14,2,0.87)'],
  details: ['rgba(43,52,68,0.88)', 'rgba(9,12,20,0.86)'],
  runtime: ['rgba(30,58,95,0.89)', 'rgba(8,17,32,0.87)'],
  ratings: ['rgba(43,36,10,0.90)', 'rgba(13,11,4,0.88)'],
  'watch-status': ['rgba(92,72,4,0.90)', 'rgba(24,19,3,0.87)'],
  new: ['rgba(6,64,48,0.89)', 'rgba(3,24,19,0.87)'],
  decade: ['rgba(66,32,110,0.89)', 'rgba(20,8,36,0.87)'],
  'video-codec': ['rgba(40,42,110,0.89)', 'rgba(11,12,36,0.87)'],
  streaming: ['rgba(96,44,6,0.90)', 'rgba(26,12,2,0.87)'],
  studio: ['rgba(58,54,50,0.89)', 'rgba(16,15,14,0.87)'],
  genre: ['rgba(92,24,24,0.89)', 'rgba(26,6,6,0.87)'],
  hdr: ['rgba(96,80,10,0.89)', 'rgba(28,22,4,0.87)'],
  channels: ['rgba(20,70,28,0.89)', 'rgba(6,22,10,0.87)'],
  'aspect-ratio': ['rgba(88,24,96,0.89)', 'rgba(26,6,30,0.87)'],
  franchise: ['rgba(96,26,44,0.89)', 'rgba(28,7,14,0.87)'],
  origin: ['rgba(104,56,10,0.90)', 'rgba(30,16,3,0.87)'],
  collection: ['rgba(110,34,70,0.89)', 'rgba(32,9,22,0.87)'],
  leaving: ['rgba(96,26,20,0.90)', 'rgba(28,7,5,0.87)'],
  family: ['rgba(6,64,48,0.89)', 'rgba(3,24,19,0.87)'],
  'saga-position': ['rgba(96,26,44,0.89)', 'rgba(28,7,14,0.87)'],
  'quick-watch': ['rgba(8,60,72,0.89)', 'rgba(3,22,28,0.87)'],
};

function sigHexRgba(hex, a) {
  const n = parseInt(String(hex).slice(1), 16);
  return `rgba(${(n >> 16) & 255},${(n >> 8) & 255},${n & 255},${a})`;
}

// Palette per drawable: status and honor ribbons are colored BY VALUE
// (Returning/Airing green, Ended slate, Canceled red; FRESH green,
// TOP RATED gold); ratings take their source's color; the rest take
// their family's accent over the family tint.
function sigPalette(spec) {
  const fam = spec.family || '';
  const t = String(spec.text == null ? '' : spec.text).toUpperCase();
  if (fam === 'status') {
    if (t.includes('RETURN') || t.includes('AIRING') || t.includes('CONTINU')) {
      return { accent: '#34d399', top: 'rgba(6,60,44,0.89)', bottom: 'rgba(3,22,17,0.87)' };
    }
    if (t.includes('CANCEL')) {
      return { accent: '#f87171', top: 'rgba(80,18,24,0.90)', bottom: 'rgba(26,5,8,0.87)' };
    }
    return { accent: '#94a3b8', top: 'rgba(45,55,72,0.88)', bottom: 'rgba(10,14,22,0.86)' };
  }
  if (fam === 'ribbon') {
    // Awards honors wear the same gold as TOP RATED.
    if (t.includes('TOP RATED') || t.includes('OSCAR') || t.includes('EMMY') || t.includes('GOLDEN GLOBE')) {
      return { accent: '#fbbf24', top: 'rgba(84,58,4,0.90)', bottom: 'rgba(22,16,3,0.87)' };
    }
    return { accent: '#34d399', top: 'rgba(6,60,44,0.89)', bottom: 'rgba(3,22,17,0.87)' };
  }
  if (fam === 'ratings') {
    const slot = spec.slot || '';
    const accent = slot === 'rt-critic' ? '#ef4444'
      : slot === 'rt-audience' ? '#fb923c'
      : slot === 'metascore' ? '#22c55e' : '#fbbf24';
    return { accent, top: SIGNATURE_TINTS.ratings[0], bottom: SIGNATURE_TINTS.ratings[1] };
  }
  const tint = SIGNATURE_TINTS[fam] || SIGNATURE_TINTS.details;
  return { accent: SIGNATURE_ACCENTS[fam] || '#e2e8f0', top: tint[0], bottom: tint[1] };
}

function sigRoundRectPath(ctx, x, y, w, h, r) {
  const rr = Math.max(0, Math.min(r, w / 2, h / 2));
  ctx.beginPath();
  if (typeof ctx.roundRect === 'function') { ctx.roundRect(x, y, w, h, rr); return; }
  ctx.moveTo(x + rr, y);
  ctx.lineTo(x + w - rr, y);
  ctx.quadraticCurveTo(x + w, y, x + w, y + rr);
  ctx.lineTo(x + w, y + h - rr);
  ctx.quadraticCurveTo(x + w, y + h, x + w - rr, y + h);
  ctx.lineTo(x + rr, y + h);
  ctx.quadraticCurveTo(x, y + h, x, y + h - rr);
  ctx.lineTo(x, y + rr);
  ctx.quadraticCurveTo(x, y, x + rr, y);
  ctx.closePath();
}

// The badge outline: pills are fully rounded, the rating box lightly
// rounded, ribbons get a notched (chevron) right end.
function sigShapePath(ctx, rect, shape) {
  if (shape === 'ribbon') {
    const { x, y, w, h } = rect;
    const notch = Math.min(h * 0.28, w * 0.14);
    const r = Math.min(10, h * 0.14);
    ctx.beginPath();
    ctx.moveTo(x + r, y);
    ctx.lineTo(x + w, y);
    ctx.lineTo(x + w - notch, y + h / 2);
    ctx.lineTo(x + w, y + h);
    ctx.lineTo(x + r, y + h);
    ctx.quadraticCurveTo(x, y + h, x, y + h - r);
    ctx.lineTo(x, y + r);
    ctx.quadraticCurveTo(x, y, x + r, y);
    ctx.closePath();
    return;
  }
  if (shape === 'ratingbox') { sigRoundRectPath(ctx, rect.x, rect.y, rect.w, rect.h, Math.min(14, rect.h * 0.16)); return; }
  sigRoundRectPath(ctx, rect.x, rect.y, rect.w, rect.h, rect.h / 2);
}

function sigStarPath(ctx, cx, cy, R, r) {
  ctx.beginPath();
  for (let i = 0; i < 10; i++) {
    const ang = -Math.PI / 2 + (i * Math.PI) / 5;
    const rad = i % 2 ? r : R;
    const px = cx + Math.cos(ang) * rad;
    const py = cy + Math.sin(ang) * rad;
    if (i) ctx.lineTo(px, py); else ctx.moveTo(px, py);
  }
  ctx.closePath();
}

// Vector icons, drawn in a box of `size` px centered at (cx, cy).
// Simple strokes/fills in the family accent — deliberately geometric
// so they stay crisp at any badge size.
function sigIcon(ctx, spec, cx, cy, size, accent) {
  ctx.save();
  ctx.translate(cx, cy);
  const s = size / 2;
  ctx.lineWidth = Math.max(1.6, size * 0.10);
  ctx.strokeStyle = accent;
  ctx.fillStyle = accent;
  ctx.lineCap = 'round';
  ctx.lineJoin = 'round';
  const fam = spec.family || '';
  if (fam === 'ratings') {
    const slot = spec.slot || '';
    if (slot === 'imdb') {
      sigRoundRectPath(ctx, -s * 1.25, -s * 0.52, size * 1.25, size * 0.52, size * 0.10);
      ctx.fillStyle = '#f5c518';
      ctx.fill();
      ctx.fillStyle = '#111111';
      ctx.font = `800 ${size * 0.34}px ${SIGNATURE_FONT_STACK}`;
      ctx.textAlign = 'center';
      ctx.textBaseline = 'middle';
      ctx.fillText('IMDb', 0, size * 0.02);
    } else if (slot === 'rt-critic') {
      ctx.beginPath();
      ctx.arc(0, s * 0.12, s * 0.66, 0, Math.PI * 2);
      ctx.fillStyle = '#ef4444';
      ctx.fill();
      ctx.beginPath(); // leaf
      ctx.moveTo(-s * 0.10, -s * 0.52);
      ctx.quadraticCurveTo(s * 0.30, -s * 1.02, s * 0.62, -s * 0.58);
      ctx.quadraticCurveTo(s * 0.18, -s * 0.44, -s * 0.10, -s * 0.52);
      ctx.fillStyle = '#22c55e';
      ctx.fill();
    } else if (slot === 'rt-audience') {
      ctx.beginPath(); // bucket
      ctx.moveTo(-s * 0.72, -s * 0.05);
      ctx.lineTo(s * 0.72, -s * 0.05);
      ctx.lineTo(s * 0.52, s * 0.85);
      ctx.lineTo(-s * 0.52, s * 0.85);
      ctx.closePath();
      ctx.strokeStyle = '#f8fafc';
      ctx.stroke();
      ctx.beginPath(); // stripes
      ctx.moveTo(-s * 0.24, -s * 0.05);
      ctx.lineTo(-s * 0.16, s * 0.85);
      ctx.moveTo(s * 0.24, -s * 0.05);
      ctx.lineTo(s * 0.16, s * 0.85);
      ctx.strokeStyle = '#ef4444';
      ctx.stroke();
      for (const bx of [-0.48, 0, 0.48]) { // popcorn bumps
        ctx.beginPath();
        ctx.arc(bx * s, -s * 0.28, s * 0.26, Math.PI, 0);
        ctx.strokeStyle = '#fde68a';
        ctx.stroke();
      }
    } else if (slot === 'metascore') {
      ctx.beginPath();
      ctx.arc(0, 0, s * 0.78, 0, Math.PI * 2);
      ctx.fillStyle = '#22c55e';
      ctx.fill();
      ctx.fillStyle = '#ffffff';
      ctx.font = `800 ${size * 0.52}px ${SIGNATURE_FONT_STACK}`;
      ctx.textAlign = 'center';
      ctx.textBaseline = 'middle';
      ctx.fillText('M', 0, size * 0.03);
    } else { // tmdb + any other slot: star
      sigStarPath(ctx, 0, 0, s * 0.85, s * 0.40);
      ctx.fillStyle = '#fbbf24';
      ctx.fill();
    }
    ctx.restore();
    return;
  }
  switch (fam) {
    case 'resolution': // expand corners
      for (const [sx, sy] of [[-1, -1], [1, -1], [-1, 1], [1, 1]]) {
        ctx.beginPath();
        ctx.moveTo(sx * s * 0.88, sy * s * 0.34);
        ctx.lineTo(sx * s * 0.88, sy * s * 0.88);
        ctx.lineTo(sx * s * 0.34, sy * s * 0.88);
        ctx.stroke();
      }
      break;
    case 'audio': { // waveform bars
      const hs = [0.42, 0.78, 1.0, 0.58, 0.34];
      const bw = size * 0.13;
      hs.forEach((hf, i) => {
        const bx = -s * 0.8 + i * s * 0.4;
        ctx.fillRect(bx - bw / 2, -s * hf, bw, s * hf * 2);
      });
      break;
    }
    case 'edition': // diamond
      ctx.beginPath();
      ctx.moveTo(0, -s * 0.82);
      ctx.lineTo(s * 0.72, 0);
      ctx.lineTo(0, s * 0.82);
      ctx.lineTo(-s * 0.72, 0);
      ctx.closePath();
      ctx.stroke();
      ctx.beginPath();
      ctx.moveTo(-s * 0.72, 0);
      ctx.lineTo(s * 0.72, 0);
      ctx.stroke();
      break;
    case 'languages': // globe
      ctx.beginPath();
      ctx.arc(0, 0, s * 0.74, 0, Math.PI * 2);
      ctx.stroke();
      ctx.beginPath();
      ctx.ellipse(0, 0, s * 0.34, s * 0.74, 0, 0, Math.PI * 2);
      ctx.stroke();
      ctx.beginPath();
      ctx.moveTo(-s * 0.74, 0);
      ctx.lineTo(s * 0.74, 0);
      ctx.stroke();
      break;
    case 'subtitles': // caption box
      sigRoundRectPath(ctx, -s * 0.82, -s * 0.56, size * 0.82, size * 0.56, size * 0.10);
      ctx.stroke();
      ctx.fillRect(-s * 0.58, -s * 0.30, s * 1.16, size * 0.11);
      ctx.fillRect(-s * 0.58, s * 0.06, s * 0.78, size * 0.11);
      break;
    case 'versions': // stacked layers
      for (const oy of [-0.42, 0, 0.42]) {
        ctx.beginPath();
        ctx.moveTo(0, (oy - 0.30) * s);
        ctx.lineTo(s * 0.78, oy * s);
        ctx.lineTo(0, (oy + 0.30) * s);
        ctx.lineTo(-s * 0.78, oy * s);
        ctx.closePath();
        ctx.stroke();
      }
      break;
    case 'source': // disc
      ctx.beginPath();
      ctx.arc(0, 0, s * 0.76, 0, Math.PI * 2);
      ctx.stroke();
      ctx.beginPath();
      ctx.arc(0, 0, s * 0.20, 0, Math.PI * 2);
      ctx.fill();
      break;
    case 'runtime': // clock: ring + two hands
      ctx.beginPath();
      ctx.arc(0, 0, s * 0.74, 0, Math.PI * 2);
      ctx.stroke();
      ctx.beginPath();
      ctx.moveTo(0, 0);
      ctx.lineTo(0, -s * 0.46);
      ctx.stroke();
      ctx.beginPath();
      ctx.moveTo(0, 0);
      ctx.lineTo(s * 0.36, s * 0.24);
      ctx.stroke();
      break;
    case 'details': // calendar
    case 'decade': // decade reuses the calendar — it IS a year glyph
      sigRoundRectPath(ctx, -s * 0.72, -s * 0.62, size * 0.72, size * 0.66, size * 0.08);
      ctx.stroke();
      ctx.beginPath();
      ctx.moveTo(-s * 0.72, -s * 0.28);
      ctx.lineTo(s * 0.72, -s * 0.28);
      ctx.stroke();
      for (const dx of [-0.38, 0.38]) ctx.fillRect(dx * s - size * 0.045, -s * 0.86, size * 0.09, size * 0.16);
      for (const [dx, dy] of [[-0.36, 0.12], [0, 0.12], [0.36, 0.12], [-0.36, 0.48], [0.36, 0.48]]) {
        ctx.beginPath();
        ctx.arc(dx * s, dy * s, size * 0.055, 0, Math.PI * 2);
        ctx.fill();
      }
      break;
    case 'ribbon': // spark
    case 'new': // the New family wears the same spark
      ctx.beginPath();
      ctx.moveTo(0, -s * 0.9);
      ctx.quadraticCurveTo(s * 0.14, -s * 0.14, s * 0.9, 0);
      ctx.quadraticCurveTo(s * 0.14, s * 0.14, 0, s * 0.9);
      ctx.quadraticCurveTo(-s * 0.14, s * 0.14, -s * 0.9, 0);
      ctx.quadraticCurveTo(-s * 0.14, -s * 0.14, 0, -s * 0.9);
      ctx.fill();
      break;
    case 'watch-status': // eye: outline + pupil
      ctx.beginPath();
      ctx.ellipse(0, 0, s * 0.82, s * 0.52, 0, 0, Math.PI * 2);
      ctx.stroke();
      ctx.beginPath();
      ctx.arc(0, 0, s * 0.24, 0, Math.PI * 2);
      ctx.fill();
      break;
    case 'video-codec': // film frame: outline + sprocket holes
      sigRoundRectPath(ctx, -s * 0.78, -s * 0.62, size * 0.78, size * 0.62, size * 0.07);
      ctx.stroke();
      for (const oy of [-0.38, 0, 0.38]) {
        ctx.fillRect(-s * 0.62, oy * s - size * 0.05, size * 0.10, size * 0.10);
        ctx.fillRect(s * 0.52, oy * s - size * 0.05, size * 0.10, size * 0.10);
      }
      break;
    case 'streaming': // play triangle in a rounded rect
      sigRoundRectPath(ctx, -s * 0.80, -s * 0.62, size * 0.80, size * 0.62, size * 0.12);
      ctx.stroke();
      ctx.beginPath();
      ctx.moveTo(-s * 0.22, -s * 0.34);
      ctx.lineTo(-s * 0.22, s * 0.34);
      ctx.lineTo(s * 0.38, 0);
      ctx.closePath();
      ctx.fill();
      break;
    case 'studio': // building: block + roof line + windows
      ctx.strokeRect(-s * 0.52, -s * 0.55, size * 0.52, size * 0.62);
      ctx.beginPath();
      ctx.moveTo(-s * 0.68, -s * 0.55);
      ctx.lineTo(s * 0.68, -s * 0.55);
      ctx.stroke();
      for (const [dx, dy] of [[-0.28, -0.28], [0.14, -0.28], [-0.28, 0.14], [0.14, 0.14]]) {
        ctx.fillRect(dx * s, dy * s, size * 0.13, size * 0.13);
      }
      break;
    case 'genre': // tag: angled label + punched hole
      ctx.beginPath();
      ctx.moveTo(-s * 0.66, -s * 0.66);
      ctx.lineTo(s * 0.08, -s * 0.66);
      ctx.lineTo(s * 0.70, -s * 0.04);
      ctx.lineTo(-s * 0.04, s * 0.70);
      ctx.closePath();
      ctx.stroke();
      ctx.beginPath();
      ctx.arc(-s * 0.36, -s * 0.36, size * 0.075, 0, Math.PI * 2);
      ctx.fill();
      break;
    case 'status': // check
      ctx.beginPath();
      ctx.moveTo(-s * 0.72, 0);
      ctx.lineTo(-s * 0.18, s * 0.52);
      ctx.lineTo(s * 0.76, -s * 0.55);
      ctx.lineWidth = Math.max(2.2, size * 0.15);
      ctx.stroke();
      break;
    case 'hdr': // sun: disc + rays — dynamic range, drawn not lettered
      ctx.beginPath();
      ctx.arc(0, 0, s * 0.38, 0, Math.PI * 2);
      ctx.fill();
      for (let i = 0; i < 8; i++) {
        const a = (i * Math.PI) / 4;
        ctx.beginPath();
        ctx.moveTo(Math.cos(a) * s * 0.58, Math.sin(a) * s * 0.58);
        ctx.lineTo(Math.cos(a) * s * 0.86, Math.sin(a) * s * 0.86);
        ctx.stroke();
      }
      break;
    case 'channels': // speaker: cone + sound arcs
      ctx.beginPath();
      ctx.moveTo(-s * 0.62, -s * 0.22);
      ctx.lineTo(-s * 0.30, -s * 0.22);
      ctx.lineTo(s * 0.08, -s * 0.58);
      ctx.lineTo(s * 0.08, s * 0.58);
      ctx.lineTo(-s * 0.30, s * 0.22);
      ctx.lineTo(-s * 0.62, s * 0.22);
      ctx.closePath();
      ctx.fill();
      for (const r of [0.42, 0.68]) {
        ctx.beginPath();
        ctx.arc(s * 0.16, 0, s * r, -Math.PI / 3.2, Math.PI / 3.2);
        ctx.stroke();
      }
      break;
    case 'aspect-ratio': // frame: wide rect + letterbox lines
      // (strokeRect, not sigRoundRectPath: primitives honor the
      // icon transform in every canvas implementation.)
      ctx.strokeRect(-s * 0.84, -s * 0.56, size * 0.84, size * 0.56);
      ctx.beginPath();
      ctx.moveTo(-s * 0.84, -s * 0.24);
      ctx.lineTo(s * 0.84, -s * 0.24);
      ctx.moveTo(-s * 0.84, s * 0.24);
      ctx.lineTo(s * 0.84, s * 0.24);
      ctx.stroke();
      break;
    case 'franchise': // chain link: two interlocking rings (a saga is a chain of films)
      ctx.beginPath();
      ctx.arc(-s * 0.30, 0, s * 0.44, 0, Math.PI * 2);
      ctx.stroke();
      ctx.beginPath();
      ctx.arc(s * 0.30, 0, s * 0.44, 0, Math.PI * 2);
      ctx.stroke();
      break;
    case 'origin': // globe: circle + meridian + parallels
      ctx.beginPath();
      ctx.arc(0, 0, s * 0.78, 0, Math.PI * 2);
      ctx.stroke();
      ctx.beginPath();
      ctx.ellipse(0, 0, s * 0.38, s * 0.78, 0, 0, Math.PI * 2);
      ctx.stroke();
      ctx.beginPath();
      ctx.moveTo(-s * 0.78, -s * 0.30);
      ctx.lineTo(s * 0.78, -s * 0.30);
      ctx.moveTo(-s * 0.78, s * 0.30);
      ctx.lineTo(s * 0.78, s * 0.30);
      ctx.stroke();
      break;
    case 'collection': // stacked frames: the server's own shelf
      // (strokeRect for the same transform reason as aspect-ratio.)
      ctx.strokeRect(-s * 0.70, -s * 0.78, size * 0.58, size * 0.58);
      ctx.strokeRect(-s * 0.44, -s * 0.44, size * 0.58, size * 0.58);
      break;
    case 'family': // heart: two lobes meeting at a point — the family mark
      ctx.beginPath();
      ctx.moveTo(0, s * 0.80);
      ctx.bezierCurveTo(-s * 1.02, s * 0.02, -s * 0.68, -s * 0.80, 0, -s * 0.28);
      ctx.bezierCurveTo(s * 0.68, -s * 0.80, s * 1.02, s * 0.02, 0, s * 0.80);
      ctx.closePath();
      ctx.fill();
      break;
    case 'leaving': // exit door: frame + arrow out — on its way out
      // (strokeRect, not sigRoundRectPath: primitives honor the
      // icon transform in every canvas implementation.)
      ctx.strokeRect(-s * 0.80, -s * 0.78, size * 0.56, size * 0.78);
      ctx.beginPath();
      ctx.moveTo(-s * 0.34, 0);
      ctx.lineTo(s * 0.74, 0);
      ctx.moveTo(s * 0.40, -s * 0.34);
      ctx.lineTo(s * 0.76, 0);
      ctx.lineTo(s * 0.40, s * 0.34);
      ctx.stroke();
      break;
    case 'saga-position': // ascending steps: the climb through a saga
      for (const [bx, bh] of [[-0.80, 0.30], [-0.27, 0.55], [0.27, 0.82]]) {
        ctx.fillRect(bx * s, s * 0.78 - bh * s, s * 0.46, bh * s);
      }
      break;
    case 'quick-watch': // lightning bolt: quick at a glance
      ctx.beginPath();
      ctx.moveTo(s * 0.20, -s * 0.86);
      ctx.lineTo(-s * 0.44, s * 0.14);
      ctx.lineTo(-s * 0.04, s * 0.14);
      ctx.lineTo(-s * 0.20, s * 0.86);
      ctx.lineTo(s * 0.44, -s * 0.14);
      ctx.lineTo(s * 0.04, -s * 0.14);
      ctx.closePath();
      ctx.fill();
      break;
    default: // generic spark
      sigStarPath(ctx, 0, 0, s * 0.8, s * 0.36);
      ctx.fill();
  }
  ctx.restore();
}

// Auto-fit: shrink the font until the text fits maxW (floor minFs),
// then ellipsis as the last resort. Leaves ctx.font/letterSpacing set
// for the caller's fillText; returns the text to draw.
function sigFitText(ctx, text, maxW, weight, startFs, minFs, letterSpacingPx) {
  let fs = startFs;
  const apply = () => {
    ctx.font = `${weight} ${fs}px ${SIGNATURE_FONT_STACK}`;
    try { ctx.letterSpacing = `${letterSpacingPx || 0}px`; } catch (e) { /* older engines */ }
  };
  apply();
  const str = String(text == null ? '' : text);
  while (fs > minFs && ctx.measureText(str).width > maxW) { fs -= 1; apply(); }
  if (ctx.measureText(str).width <= maxW) return str;
  let out = str;
  while (out.length && ctx.measureText(`${out}…`).width > maxW) out = out.slice(0, -1);
  return out ? `${out}…` : '';
}

function sigDrawText(ctx, text, x, y, fill, align, withShadow) {
  ctx.textAlign = align;
  ctx.textBaseline = 'middle';
  ctx.fillStyle = fill;
  if (withShadow) {
    ctx.shadowColor = 'rgba(0,0,0,0.45)';
    ctx.shadowBlur = 6;
    ctx.shadowOffsetX = 0;
    ctx.shadowOffsetY = 1;
  }
  ctx.fillText(text, x, y);
  ctx.shadowColor = 'rgba(0,0,0,0)';
  ctx.shadowBlur = 0;
  ctx.shadowOffsetY = 0;
}

// renderSignatureBadge(ctx, posterImg, rect, spec) — one procedural
// badge. posterImg is the full poster image (already drawn unfiltered
// across the whole canvas by drawComposite); the frost pass re-draws
// it filtered INSIDE the clipped badge path so the blur crop aligns
// with the badge's position on the poster.
function renderSignatureBadge(ctx, posterImg, rect, spec) {
  if (!ctx || !rect || !spec) return;
  const pal = sigPalette(spec);
  const shape = spec.shape || 'pill';
  const value = String(spec.text == null ? '' : spec.text);
  const micro = String(spec.microLabel || '');
  ctx.save();
  try {
    // Soft shadow under the badge silhouette.
    ctx.save();
    ctx.shadowColor = 'rgba(2,6,17,0.50)';
    ctx.shadowBlur = Math.max(10, rect.h * 0.22);
    ctx.shadowOffsetX = 0;
    ctx.shadowOffsetY = Math.max(3, rect.h * 0.07);
    sigShapePath(ctx, rect, shape);
    ctx.fillStyle = pal.bottom;
    ctx.fill();
    ctx.restore();

    // Frosted body: blur the poster behind the badge, then tint.
    ctx.save();
    sigShapePath(ctx, rect, shape);
    ctx.clip();
    let frosted = false;
    try {
      ctx.filter = 'blur(14px) saturate(150%) brightness(0.8)';
      frosted = typeof ctx.filter === 'string' && ctx.filter.indexOf('blur') >= 0;
    } catch (e) { frosted = false; }
    if (frosted && posterImg) {
      try { ctx.drawImage(posterImg, 0, 0, ctx.canvas.width, ctx.canvas.height); } catch (e) { /* tint still carries it */ }
    }
    try { ctx.filter = 'none'; } catch (e) { /* no filter support: tint alone */ }
    const grad = ctx.createLinearGradient(0, rect.y, 0, rect.y + rect.h);
    grad.addColorStop(0, pal.top);
    grad.addColorStop(1, pal.bottom);
    ctx.fillStyle = grad;
    ctx.fillRect(rect.x, rect.y, rect.w, rect.h);
    const sheen = ctx.createLinearGradient(0, rect.y, 0, rect.y + rect.h * 0.55);
    sheen.addColorStop(0, 'rgba(255,255,255,0.16)');
    sheen.addColorStop(1, 'rgba(255,255,255,0)');
    ctx.fillStyle = sheen;
    ctx.fillRect(rect.x, rect.y, rect.w, rect.h * 0.55);
    if (shape === 'ribbon') { // folded-end shading inside the notch
      const notch = Math.min(rect.h * 0.28, rect.w * 0.14);
      ctx.beginPath();
      ctx.moveTo(rect.x + rect.w, rect.y);
      ctx.lineTo(rect.x + rect.w - notch, rect.y + rect.h / 2);
      ctx.lineTo(rect.x + rect.w, rect.y + rect.h);
      ctx.closePath();
      ctx.fillStyle = 'rgba(0,0,0,0.18)';
      ctx.fill();
    }
    ctx.restore();

    // Light stroke; the rating box gets its double border.
    sigShapePath(ctx, rect, shape);
    ctx.lineWidth = 1.5;
    ctx.strokeStyle = 'rgba(255,255,255,0.38)';
    ctx.stroke();
    if (shape === 'ratingbox') {
      const inset = 5;
      sigRoundRectPath(ctx, rect.x + inset, rect.y + inset, rect.w - inset * 2, rect.h - inset * 2, Math.min(10, rect.h * 0.12));
      ctx.lineWidth = 1;
      ctx.strokeStyle = 'rgba(255,255,255,0.30)';
      ctx.stroke();
    }

    if (shape === 'ratingbox') {
      const vOut = sigFitText(ctx, value, rect.w - 18, 800, Math.max(15, rect.h * 0.38), 15, 0);
      sigDrawText(ctx, vOut, rect.x + rect.w / 2, rect.y + rect.h * 0.40, '#ffffff', 'center', true);
      if (micro) {
        const mOut = sigFitText(ctx, micro, rect.w - 14, 600, Math.max(9, rect.h * 0.115), 9, rect.h * 0.02);
        sigDrawText(ctx, mOut, rect.x + rect.w / 2, rect.y + rect.h * 0.79, pal.accent, 'center', false);
      }
    } else {
      const isChip = spec.family === 'ratings';
      const zone = rect.h * (isChip ? 0.60 : 0.64);
      const zoneX = rect.x + rect.h * (isChip ? 0.14 : 0.16);
      const zoneY = rect.y + (rect.h - zone) / 2;
      sigRoundRectPath(ctx, zoneX, zoneY, zone, zone, zone * 0.28);
      ctx.fillStyle = sigHexRgba(pal.accent, 0.18);
      ctx.fill();
      ctx.lineWidth = 1;
      ctx.strokeStyle = 'rgba(255,255,255,0.22)';
      ctx.stroke();
      sigIcon(ctx, spec, zoneX + zone / 2, rect.y + rect.h / 2, zone * 0.62, pal.accent);
      const textX = zoneX + zone + rect.h * 0.13;
      const maxW = rect.x + rect.w - rect.h * 0.18 - textX;
      if (isChip) {
        // Chip text never auto-fits below the legibility floor
        // (v1.9.1) — it ellipsizes at the floor instead of shrinking
        // into a speck.
        const vOut = sigFitText(ctx, value, maxW, 800, Math.max(LEGIBILITY_MIN_TEXT_PX, rect.h * 0.34), LEGIBILITY_MIN_TEXT_PX, 0);
        sigDrawText(ctx, vOut, textX, rect.y + rect.h * 0.52, '#ffffff', 'left', true);
      } else {
        if (micro) {
          const mOut = sigFitText(ctx, micro, maxW, 600, Math.max(9, rect.h * 0.128), 9, rect.h * 0.018);
          sigDrawText(ctx, mOut, textX, rect.y + rect.h * 0.315, pal.accent, 'left', false);
        }
        const vOut = sigFitText(ctx, value, maxW, 800, Math.max(15, rect.h * 0.315), 15, 0);
        sigDrawText(ctx, vOut, textX, rect.y + rect.h * 0.665, '#ffffff', 'left', true);
      }
    }
  } finally {
    try { ctx.filter = 'none'; } catch (e) { /* ignore */ }
    try { ctx.letterSpacing = '0px'; } catch (e) { /* ignore */ }
    ctx.restore();
  }
}

function drawComposite(canvas, posterImg, drawables) {
  if (typeof document === 'undefined' || !canvas?.getContext) {
    throw new Error('drawComposite() needs a browser canvas — pure math lives in the functions above.');
  }
  const ctx = canvas.getContext('2d');
  ctx.clearRect(0, 0, canvas.width, canvas.height);
  ctx.globalAlpha = 1;
  ctx.drawImage(posterImg, 0, 0, canvas.width, canvas.height);
  for (const d of drawables || []) {
    if (d.signature) {
      // Signature drawables carry no image: the procedural renderer
      // draws them from the spec + the poster itself (frosted crop).
      ctx.globalAlpha = d.rect.opacity == null ? 1 : d.rect.opacity;
      renderSignatureBadge(ctx, posterImg, d.rect, d.signature);
      ctx.globalAlpha = 1;
      continue;
    }
    if (!d.img) continue;
    ctx.globalAlpha = d.rect.opacity == null ? 1 : d.rect.opacity;
    ctx.drawImage(d.img, d.rect.x, d.rect.y, d.rect.w, d.rect.h);
    if (d.text) {
      // Labeled text scales DOWN to fit its badge before anything is
      // dropped (v1.9.1): a counted label ("62 UNWATCHED") on a
      // narrow pill shrinks instead of spilling past the art.
      let fs = Math.max(10, Math.round(d.rect.h * 0.42));
      ctx.font = `700 ${fs}px system-ui, sans-serif`;
      const str = String(d.text);
      while (fs > 10 && ctx.measureText(str).width > d.rect.w * 0.92) {
        fs -= 2;
        ctx.font = `700 ${fs}px system-ui, sans-serif`;
      }
      ctx.textAlign = 'center';
      ctx.textBaseline = 'middle';
      ctx.fillStyle = '#f5f2ff';
      // A soft shadow keeps runtime text legible on bright posters
      // (reset immediately after — badge art itself stays unshadowed).
      ctx.shadowColor = 'rgba(0,0,0,0.45)';
      ctx.shadowBlur = fs * 0.12;
      ctx.shadowOffsetX = 0;
      ctx.shadowOffsetY = 1;
      ctx.fillText(str, d.rect.x + d.rect.w / 2, d.rect.y + d.rect.h / 2 + 1);
      ctx.shadowColor = 'rgba(0,0,0,0)';
      ctx.shadowBlur = 0;
      ctx.shadowOffsetY = 0;
    }
  }
  ctx.globalAlpha = 1;
}

// groupApplyOutcomes(entries, opts?) → the end-of-run grouping shared
// by the one-click apply summary and the restore-default summary.
// PURE. entries: [{ ratingKey, title, outcome, error? }] with outcome
// one of applied / restored / failed / no-overlay / already-default /
// no-provider / unavailable / cancelled (an entry with no outcome is
// read off its ok flag: ok → applied, not ok → failed). Failures are
// grouped BY REASON — identical error strings share one group, first-
// seen order — so a summary can say "12 failed: backup verification
// failed (9), upload rejected (3)" instead of a 240-row wall. opts.
// cancelled marks the whole run as user-cancelled.
function groupApplyOutcomes(entries, opts) {
  const list = Array.isArray(entries) ? entries : [];
  const groups = {
    applied: [], restored: [], noOverlay: [], alreadyDefault: [],
    noProvider: [], unavailable: [], cancelled: [], failedByReason: [],
  };
  const byReason = new Map();
  for (const e of list) {
    const item = { ratingKey: String(e?.ratingKey ?? ''), title: e?.title || 'Untitled', error: e?.error || '' };
    // Sub-item identity (a season's level + parent show; an
    // episode's level + season + show) rides through grouping so
    // summary lists can name it.
    if (e?.level) {
      item.level = e.level; item.parentTitle = e.parentTitle || '';
      if (e.grandparentTitle) item.grandparentTitle = e.grandparentTitle;
    }
    let outcome = e?.outcome || '';
    if (!outcome) outcome = e?.ok ? 'applied' : 'failed';
    switch (outcome) {
      case 'applied': groups.applied.push(item); break;
      case 'restored': groups.restored.push(item); break;
      case 'no-overlay': groups.noOverlay.push(item); break;
      case 'already-default': groups.alreadyDefault.push(item); break;
      case 'no-provider': groups.noProvider.push(item); break;
      case 'unavailable': groups.unavailable.push(item); break;
      case 'cancelled': groups.cancelled.push(item); break;
      default: {
        const reason = item.error || 'Unknown error';
        if (!byReason.has(reason)) byReason.set(reason, []);
        byReason.get(reason).push(item);
      }
    }
  }
  groups.failedByReason = [...byReason.entries()].map(([reason, items]) => ({ reason, items }));
  const failed = groups.failedByReason.reduce((n, g) => n + g.items.length, 0);
  groups.counts = {
    total: list.length,
    applied: groups.applied.length,
    restored: groups.restored.length,
    noOverlay: groups.noOverlay.length,
    alreadyDefault: groups.alreadyDefault.length,
    noProvider: groups.noProvider.length,
    unavailable: groups.unavailable.length,
    cancelled: groups.cancelled.length,
    failed,
  };
  groups.cancelledRun = !!(opts && opts.cancelled);
  return groups;
}

const __compositeAPI = {
  CORNERS,
  cornerPreset,
  placementRect,
  evaluateRule,
  evaluateRun,
  groupApplyOutcomes,
  isImageBytes,
  sidecarFor,
  drawComposite,
  // Kometa-style families (2026-10-22)
  FAMILY_DEFS,
  FAMILY_STACK_ORDER,
  RATING_SOURCE_NAMES,
  AURORA_SHELLS,
  auroraShell,
  familyDef,
  defaultFamilies,
  familySettings,
  evaluateFamily,
  layoutFamilyDrawables,
  audioFormatName,
  audioChannelLabel,
  runtimeText,
  ratingSourceValue,
  videoCodecName,
  streamingDisplayName,
  awardsHonorText,
  // Badge-library expansion (v1.11): the aspect-ratio namer and
  // the origin language mapper, exported for their tolerance/code
  // tables in the unit tests.
  aspectRatioText,
  originLanguageText,
  // Family rating set (v1.15): the configurable set behind the
  // family family — vocabulary, shipped default, Studio chip
  // groups, and the shared clamp.
  FAMILY_RATING_VOCAB,
  FAMILY_RATINGS_DEFAULT,
  FAMILY_RATING_GROUPS,
  clampFamilyRatings,
  // Signature engine (procedural frosted-glass badges)
  signatureTextWidth,
  renderSignatureBadge,
  SIGNATURE_FONT_STACK,
  // Badge proportion + legibility rules (v1.9.1)
  BADGE_MAX_WFRAC,
  LEGIBILITY_MIN_TEXT_PX,
  // Episode overlays (v1.10): geometry + the episode family rule
  CANONICAL_GEOM,
  LANDSCAPE_GEOM,
  geometryFor,
  legibilityFloorPx,
  EPISODE_FAMILIES,
  EPISODE_BADGE_CAP,
};
// Dual-mode export: node (tests, apply.js) gets module.exports; the browser
// <script> tag gets window.PCMComposite. No bundler, by design.
if (typeof module !== 'undefined' && module.exports) {
  module.exports = __compositeAPI;
} else if (typeof window !== 'undefined') {
  window.PCMComposite = __compositeAPI;
}

})();
