// web/composite.test.cjs — rule-engine tests for the Overlay Studio.
// Run: node web/composite.test.cjs   (no deps; composite.js is dual-mode)
// Covers the v1 rules (regression) and the facts-v2 stream rules: every
// detection kind must fire ONLY on positive evidence, and value badges
// must receive their label from the rule result.
'use strict';
const assert = require('node:assert');
const { evaluateRule, evaluateRun, evaluateFamily, layoutFamilyDrawables, defaultFamilies, familySettings, familyDef, FAMILY_DEFS, FAMILY_STACK_ORDER, audioFormatName, audioChannelLabel, auroraShell, signatureTextWidth, streamingDisplayName, groupApplyOutcomes, BADGE_MAX_WFRAC, LEGIBILITY_MIN_TEXT_PX, geometryFor, legibilityFloorPx, EPISODE_FAMILIES, EPISODE_BADGE_CAP, CANONICAL_GEOM, LANDSCAPE_GEOM, aspectRatioText, originLanguageText, FAMILY_RATING_VOCAB, FAMILY_RATINGS_DEFAULT, FAMILY_RATING_GROUPS, clampFamilyRatings, evaluateCustomRule, sanitizeCustomBadge } = require('./composite.js');

let n = 0;
function ev(rule, facts) { n++; return evaluateRule(rule, facts); }
function shows(rule, facts, msg) { assert.strictEqual(ev(rule, facts).show, true, msg || JSON.stringify(rule)); }
function hides(rule, facts, msg) { assert.strictEqual(ev(rule, facts).show, false, msg || JSON.stringify(rule)); }
function label(rule, facts, want) { assert.strictEqual(ev(rule, facts).label, want, JSON.stringify(rule)); }

// ---- v1 regression ----------------------------------------------------
shows({ kind: 'always' }, {});
shows({ kind: 'tmdb-rating', op: '>=', value: 7 }, { tmdbRating: 7.5 });
hides({ kind: 'tmdb-rating', op: '>=', value: 7 }, { tmdbRating: 6.9 });
hides({ kind: 'tmdb-rating', op: '>=', value: 7 }, {});
shows({ kind: 'is-4k' }, { width: 3840 });
hides({ kind: 'is-4k' }, { width: 1920 });
// v1.9.1 (defect C): the unwatched label is drawn ON the generic
// count pill, so it is the counted TEXT — never a bare numeral.
// (Was '3' before the style pass; that pin WAS the bare-numeral draw.)
label({ kind: 'unwatched' }, { unwatchedCount: 3 }, '3 UNWATCHED');
label({ kind: 'unwatched' }, { unwatchedCount: 1 }, 'UNWATCHED');
hides({ kind: 'unwatched' }, { unwatchedCount: 0 });
shows({ kind: 'is-new', days: 14 }, { addedAtDays: 3 });
assert.strictEqual(ev({ kind: 'tech-manual' }, {}).warn, true, 'tech-manual still warns');

// ---- imdb-rating: Plex value counts without an OMDb key ----------------
shows({ kind: 'imdb-rating', op: '>=', value: 0 }, { imdbRating: 8.7, hasOmdbKey: false });
label({ kind: 'imdb-rating', op: '>=', value: 0 }, { imdbRating: 8.7, hasOmdbKey: false }, '8.7');
hides({ kind: 'imdb-rating', op: '>=', value: 0 }, { imdbRating: null, hasOmdbKey: true });
hides({ kind: 'imdb-rating', op: '>=', value: 9 }, { imdbRating: 8.7, hasOmdbKey: true });

// ---- rt rules -----------------------------------------------------------
label({ kind: 'rt-rating', op: '>=', value: 0 }, { rtRating: 91 }, '91%');
shows({ kind: 'rt-rating', op: '>=', value: 80 }, { rtRating: 91 });
hides({ kind: 'rt-rating', op: '>=', value: 95 }, { rtRating: 91 });
hides({ kind: 'rt-rating' }, { rtRating: null });
label({ kind: 'rt-audience', op: '>=', value: 0 }, { rtAudience: 88 }, '88%');
hides({ kind: 'rt-audience' }, {});

// ---- stream detection kinds: positive evidence only ---------------------
shows({ kind: 'is-dolby-vision' }, { dv: true });
hides({ kind: 'is-dolby-vision' }, { dv: false });
hides({ kind: 'is-dolby-vision' }, {});
shows({ kind: 'is-hdr' }, { hdrAny: true });
hides({ kind: 'is-hdr' }, { hdrAny: false });
shows({ kind: 'is-atmos' }, { atmos: true });
hides({ kind: 'is-atmos' }, { atmos: false });
shows({ kind: 'is-dtsx' }, { dtsx: true });
hides({ kind: 'is-dtsx' }, {});

// ---- resolution -----------------------------------------------------------
shows({ kind: 'resolution-is', value: '4K' }, { resolutionTier: '4K' });
label({ kind: 'resolution-is', value: '4K' }, { resolutionTier: '4K' }, '4K');
hides({ kind: 'resolution-is', value: '4K' }, { resolutionTier: '1080p' });
hides({ kind: 'resolution-is', value: '4K' }, { resolutionTier: null });
label({ kind: 'resolution-combo' }, { resolutionTier: '4K', dv: true, hdr10: false, hlg: false }, '4K • DV');
label({ kind: 'resolution-combo' }, { resolutionTier: '1080p', dv: false, hdr10: true }, '1080p • HDR');
label({ kind: 'resolution-combo' }, { resolutionTier: '4K', dv: true, hdr10: true }, '4K • DV • HDR');
label({ kind: 'resolution-combo' }, { resolutionTier: '720p', hlg: true }, '720p • HLG');
hides({ kind: 'resolution-combo' }, { resolutionTier: null, dv: false, hdr10: false, hlg: false });

// ---- item-field rules ------------------------------------------------------
label({ kind: 'has-content-rating' }, { contentRating: 'PG-13' }, 'PG-13');
hides({ kind: 'has-content-rating' }, { contentRating: null });
label({ kind: 'is-edition' }, { editionTitle: "Director's Cut" }, "Director's Cut");
hides({ kind: 'is-edition' }, {});
label({ kind: 'tv-status-is', value: 'Ended' }, { tvStatus: 'Ended' }, 'Ended');
hides({ kind: 'tv-status-is', value: 'Ended' }, { tvStatus: 'Returning' });
label({ kind: 'tv-status-is' }, { tvStatus: 'Airing' }, 'Airing');
hides({ kind: 'tv-status-is' }, { tvStatus: null });

// ---- language ---------------------------------------------------------------
// language-is DEFAULT mode (no rule value): draws the item's real
// audio — SUB/DUAL from the Japanese pattern, otherwise the language
// code(s) themselves. 'dub' alone (English track, no Japanese) must
// draw ENG, not DUB: on a native-English title English is the
// language, not a dub.
label({ kind: 'language-is' }, { dubClass: 'sub', audioLangs: ['jpn'], audioTracks: 1 }, 'SUB');
label({ kind: 'language-is' }, { dubClass: 'dual', audioLangs: ['eng', 'jpn'], audioTracks: 2 }, 'DUAL');
label({ kind: 'language-is' }, { dubClass: 'dub', audioLangs: ['eng'], audioTracks: 1 }, 'ENG');
label({ kind: 'language-is' }, { dubClass: 'unknown', audioLangs: ['fra'], audioTracks: 1 }, 'FRA');
label({ kind: 'language-is' }, { dubClass: 'unknown', audioLangs: ['eng', 'fra'], audioTracks: 2 }, 'DUAL');
label({ kind: 'language-is' }, { dubClass: 'unknown', audioLangs: ['eng', 'fra', 'spa'], audioTracks: 3 }, 'MULTI');
hides({ kind: 'language-is' }, { dubClass: 'unknown', audioLangs: [], audioTracks: 0, type: 'movie' });
hides({ kind: 'language-is' }, { dubClass: 'unknown', audioLangs: [], audioTracks: 0, type: 'show' });
label({ kind: 'language-is', value: 'sub' }, { dubClass: 'sub' }, 'SUB');
label({ kind: 'language-is', value: 'dual' }, { dubClass: 'dual' }, 'DUAL');
hides({ kind: 'language-is', value: 'sub' }, { dubClass: 'dub' });
hides({ kind: 'language-is' }, { dubClass: 'unknown' });
label({ kind: 'language-is', value: 'dub' }, { dubClass: 'dub' }, 'DUB');
hides({ kind: 'language-is' }, { dubClass: 'dub' }); // default mode + dub verdict but NO language facts: nothing honest to draw

console.log(`composite.test.cjs: all ${n} evaluateRule checks passed`);

// ---- evaluateRun: the honest dry-run -------------------------------------
// Mixed library: an HDR/DV item, an SDR item, a highly-rated SDR item,
// and one whose metadata fetch failed. A tech-only layout must rewrite
// ONLY the HDR item; the failed item must land in failedFacts — never
// silently folded into "skipped".
{
  const hdrItem = { ratingKey: '1', title: 'HDR Movie' };
  const sdrItem = { ratingKey: '2', title: 'SDR Movie' };
  const ratedItem = { ratingKey: '3', title: 'Rated SDR' };
  const deadItem = { ratingKey: '4', title: 'Fetch Failed' };
  const ghostItem = { ratingKey: '5', title: 'Never Fetched' };
  const factsByKey = {
    1: { resolutionTier: '4K', dv: true, hdr10: true, hlg: false, hdrAny: true, width: 3840, tmdbRating: 8.7 },
    2: { resolutionTier: '1080p', dv: false, hdr10: false, hlg: false, hdrAny: false, width: 1920, tmdbRating: 6.1 },
    3: { resolutionTier: '1080p', dv: false, hdr10: false, hlg: false, hdrAny: false, width: 1920, tmdbRating: 8.7 },
    4: { __error: 'Plex returned HTTP 500' },
    // 5 deliberately absent — a missing key is also a failed fetch.
  };
  const techLayout = [{ badgeId: 'badge-hdr', rule: { kind: 'is-hdr' } }];
  const run = evaluateRun([hdrItem, sdrItem, deadItem, ghostItem], factsByKey, techLayout);
  assert.strictEqual(run.willApply.length, 1, 'only the HDR item is rewritten');
  assert.strictEqual(run.willApply[0].item, hdrItem);
  assert.deepStrictEqual(run.willApply[0].badges, [{ badgeId: 'badge-hdr', label: null }]);
  // SDR item: skipped (not failed), and the reason names the HDR miss.
  assert.strictEqual(run.skipped.length, 1, 'SDR item skipped for a tech-only layout');
  assert.strictEqual(run.skipped[0].item, sdrItem);
  assert.match(run.skipped[0].reason, /badge-hdr: no HDR stream detected/);
  // Failures are their own bucket, with the error carried through.
  assert.strictEqual(run.failedFacts.length, 2);
  assert.strictEqual(run.failedFacts[0].item, deadItem);
  assert.strictEqual(run.failedFacts[0].error, 'Plex returned HTTP 500');
  assert.strictEqual(run.failedFacts[1].item, ghostItem);

  // Value badge: the rescombo label is composed from facts and carried
  // through verbatim — that string is what will be drawn on the poster.
  const comboLayout = [
    { badgeId: 'badge-hdr', rule: { kind: 'is-hdr' } },
    { badgeId: 'pill-rescombo', rule: { kind: 'resolution-combo' } },
  ];
  const runC = evaluateRun([hdrItem, sdrItem], factsByKey, comboLayout);
  const hdrBadges = runC.willApply.find((w) => w.item === hdrItem).badges;
  assert.deepStrictEqual(hdrBadges.map((b) => b.badgeId), ['badge-hdr', 'pill-rescombo']);
  assert.strictEqual(hdrBadges[1].label, '4K • DV • HDR');
  // The SDR item still earns the combo pill (tier only) — partial fire
  // lands in willApply with ONLY the firing badges listed.
  const sdrBadges = runC.willApply.find((w) => w.item === sdrItem).badges;
  assert.deepStrictEqual(sdrBadges, [{ badgeId: 'pill-rescombo', label: '1080p' }]);

  // Rating layout: the star's label (the value that will be drawn)
  // rides through; the below-threshold item is skipped.
  const starLayout = [{ badgeId: 'star', rule: { kind: 'tmdb-rating', op: '>=', value: 7 } }];
  const run2 = evaluateRun([hdrItem, sdrItem, ratedItem], factsByKey, starLayout);
  assert.strictEqual(run2.willApply.length, 2);
  assert.strictEqual(run2.willApply.find((w) => w.item === ratedItem).badges[0].label, '8.7');
  assert.strictEqual(run2.skipped.length, 1);
  assert.strictEqual(run2.skipped[0].item, sdrItem);

  // Empty layout: everything with facts is skipped, nothing is applied.
  const run3 = evaluateRun([hdrItem], factsByKey, []);
  assert.strictEqual(run3.willApply.length, 0);
  assert.strictEqual(run3.skipped[0].reason, 'no badges in the layout');
  console.log('composite.test.cjs: evaluateRun checks passed');
}

// ---- Kometa-style families -------------------------------------------------
// Every family evaluator: fires with the right text on crafted facts,
// hides on missing facts. Missing data must NEVER fire — that is the
// whole honesty contract of the family system.
{
  let m = 0;
  const fam = (id, settings, facts) => { m++; return evaluateFamily(id, settings, facts); };
  const fShows = (id, settings, facts, text) => {
    const v = fam(id, settings, facts);
    assert.strictEqual(v.show, true, `${id} should fire`);
    if (text !== undefined) assert.strictEqual(v.text, text, `${id} text`);
  };
  const fHides = (id, settings, facts) => assert.strictEqual(fam(id, settings, facts).show, false, `${id} should hide`);

  const rich = {
    type: 'movie', resolutionTier: '4K', dv: true, hdr10: true, hlg: false, hdrAny: true,
    atmos: true, dtsx: false, dtshdma: false, truehd: false,
    bestAudioLabel: 'eac3 • 5.1 • eng',
    tmdbRating: 8.7, imdbRating: 8.9, rtRating: 91, rtAudience: 88, metascore: 82,
    contentRating: 'PG-13', editionTitle: "Director's Cut",
    dubClass: 'dual', audioLangs: ['eng', 'jpn'], audioTracks: 2, mediaCount: 2,
  };
  const bare = { type: 'movie' };

  // resolution — dovetail composition, identical to resolution-combo.
  fShows('resolution', {}, rich, '4K • DV • HDR');
  fShows('resolution', {}, { resolutionTier: '1080p', hdr10: true }, '1080p • HDR');
  fShows('resolution', {}, { resolutionTier: 'SD' }, 'SD');
  fHides('resolution', {}, bare);
  fHides('resolution', {}, { resolutionTier: null, dv: false, hdr10: false, hlg: false });

  // audio — premium flags win; plain codecs come from bestAudioLabel.
  fShows('audio', {}, rich, 'Dolby Atmos');
  fShows('audio', {}, { dtsx: true }, 'DTS:X');
  fShows('audio', {}, { dtshdma: true }, 'DTS-HD MA');
  fShows('audio', {}, { truehd: true }, 'TrueHD');
  fShows('audio', {}, { bestAudioLabel: 'eac3 • 5.1 • eng' }, 'DD+');
  fShows('audio', {}, { bestAudioLabel: 'ac3 • 2.0 • eng' }, 'Dolby Digital');
  fShows('audio', {}, { bestAudioLabel: 'aac • 2.0 • eng' }, 'AAC');
  fHides('audio', {}, { bestAudioLabel: 'wmav2 • 2.0 • eng' }); // unrecognized codec: no invented name
  fHides('audio', {}, bare);

  // ratings — slots resolve independently; unknown sources are skipped,
  // never faked. One entry per slot in the layout drawables.
  const rv = fam('ratings', { slots: ['tmdb', 'imdb', 'rt-critic'] }, rich);
  assert.deepStrictEqual(rv.slots.map((s) => s.text), ['8.7', '8.9', '91%']);
  assert.deepStrictEqual(rv.slots.map((s) => s.badgeId), ['star', 'imdb', 'tomato']);
  const rvPartial = fam('ratings', { slots: ['tmdb', 'imdb'] }, { tmdbRating: 7.2 });
  assert.strictEqual(rvPartial.show, true);
  assert.deepStrictEqual(rvPartial.slots.map((s) => s.source), ['tmdb']);
  assert.match(rvPartial.note, /IMDb/);
  fShows('ratings', { slots: ['rt-audience'] }, rich, '88%');
  fShows('ratings', { slots: ['metascore'] }, rich, '82');
  fHides('ratings', { slots: ['tmdb', 'imdb', 'rt-critic'] }, bare);
  fHides('ratings', { slots: ['imdb'] }, { tmdbRating: 9.1 }); // wrong source present ≠ slot filled

  // content-rating / edition — the item's own text or nothing.
  fShows('content-rating', {}, rich, 'PG-13');
  fHides('content-rating', {}, bare);
  fShows('edition', {}, rich, "Director's Cut");
  fHides('edition', {}, bare);

  // status — shows only, TMDb status text.
  fShows('status', {}, { type: 'show', tvStatus: 'Returning Series' }, 'Returning Series');
  fHides('status', {}, { type: 'movie', tvStatus: 'Returning Series' });
  fHides('status', {}, { type: 'show' });

  // languages — display styles over the item's real audio languages.
  // Default Codes draws the languages themselves; Class draws the
  // dubscan verdict; Codes+subs appends subtitle languages.
  fShows('languages', {}, rich, 'ENG · JPN');
  fShows('languages', { style: 'class' }, rich, 'DUAL');
  fShows('languages', { style: 'codes-subs' }, { ...rich, subtitleLangs: ['eng'] }, 'ENG · JPN + subs ENG');
  fShows('languages', { style: 'codes-subs' }, { ...rich, subtitleLangs: [], hasSubs: true }, 'ENG · JPN + subs');
  fShows('languages', {}, { dubClass: 'sub', audioLangs: ['jpn'], audioTracks: 1 }, 'JPN');
  fShows('languages', { style: 'class' }, { dubClass: 'sub', audioLangs: ['jpn'], audioTracks: 1 }, 'SUB');
  fShows('languages', { style: 'class' }, { dubClass: 'dub', audioLangs: ['eng'], audioTracks: 1 }, 'DUB');
  fShows('languages', {}, { dubClass: 'dub', audioLangs: ['eng'], audioTracks: 1 }, 'ENG');
  fShows('languages', {}, { dubClass: 'unknown', audioLangs: ['eng', 'fra', 'spa'], audioTracks: 3 }, 'ENG · FRA · SPA');
  fHides('languages', { style: 'class' }, { dubClass: 'unknown', audioLangs: ['fra'], audioTracks: 1 }); // no class exists for French-only
  fHides('languages', {}, { dubClass: 'unknown', audioLangs: [], audioTracks: 0, type: 'movie' });

  // languages on SHOWS — facts are the episode union (langsFromEpisodes)
  // and the notes must disclose the source and the scan size.
  const showFacts = {
    type: 'show', audioLangs: ['eng', 'jpn'], subtitleLangs: ['eng'], hasSubs: true,
    dubClass: 'dual', audioTracks: 2, langsFromEpisodes: true, episodeCount: 24, episodeTotal: 24, langsPartial: false,
  };
  const showV = fam('languages', {}, showFacts);
  assert.strictEqual(showV.text, 'ENG · JPN');
  assert.match(showV.note, /across 24 episodes/);
  fShows('languages', { style: 'class' }, showFacts, 'DUAL');
  fShows('languages', { style: 'codes-subs' }, showFacts, 'ENG · JPN + subs ENG');
  const partialV = fam('languages', {}, { ...showFacts, episodeCount: 300, episodeTotal: 512, langsPartial: true });
  assert.match(partialV.note, /first 300 of 512/);
  const noTagShow = fam('languages', {}, { type: 'show', audioLangs: [], dubClass: 'unknown', audioTracks: 0, langsFromEpisodes: true, episodeCount: 12 });
  assert.strictEqual(noTagShow.show, false);
  assert.strictEqual(noTagShow.note, 'no language tags on any episode');
  const errShow = fam('languages', {}, { type: 'show', audioLangs: [], dubClass: 'unknown', audioTracks: 0, langsFromEpisodes: false, langsError: 'Plex returned HTTP 404' });
  assert.strictEqual(errShow.show, false);
  assert.match(errShow.note, /couldn't be read/);
  // The language-is RULE is show-aware too: aggregated facts with no
  // tags say "no language tags on any episode", not the old shrug.
  assert.strictEqual(evaluateRule({ kind: 'language-is' }, { type: 'show', dubClass: 'unknown', audioLangs: [], audioTracks: 0, langsFromEpisodes: true }).note,
    'no language tags on any episode');
  m += 5;

  // ribbon — RT Fresh at ≥60, Top Rated at the user's threshold, Off.
  fShows('ribbon', { honor: 'rt-fresh' }, rich, 'FRESH');
  fHides('ribbon', { honor: 'rt-fresh' }, { rtRating: 59 });
  fHides('ribbon', { honor: 'rt-fresh' }, bare);
  fShows('ribbon', { honor: 'top-rated', source: 'tmdb', threshold: 8 }, rich, 'TOP RATED');
  fHides('ribbon', { honor: 'top-rated', source: 'tmdb', threshold: 9 }, rich);
  fShows('ribbon', { honor: 'top-rated', source: 'imdb', threshold: 8.5 }, rich, 'TOP RATED');
  fHides('ribbon', { honor: 'off' }, rich);

  // versions — needs the mediaCount fact; 1 is not a versions badge.
  fShows('versions', {}, rich, '2 Versions');
  fShows('versions', {}, { mediaCount: 3 }, '3 Versions');
  fHides('versions', {}, { mediaCount: 1 });
  fHides('versions', {}, bare);

  // audioFormatName edge: flags beat the label parser.
  assert.strictEqual(audioFormatName({ atmos: true, bestAudioLabel: 'aac • 2.0' }), 'Dolby Atmos');
  m++;

  // ---- layout: stacking, corners, verdicts ------------------------------
  const aspectOf = () => 0.2857; // pill-like shells
  const laid = layoutFamilyDrawables(defaultFamilies(), rich, aspectOf);
  // Default layout on a movie: resolution, audio, ratings, edition,
  // content-rating fire; status is shows-only; ribbon/languages/
  // versions are OFF by default and produce NO verdict rows.
  assert.deepStrictEqual(laid.verdicts.map((v) => v.family),
    ['resolution', 'audio', 'ratings', 'edition', 'status', 'content-rating']);
  assert.strictEqual(laid.verdicts.find((v) => v.family === 'status').show, false);
  // Drawables: res + audio + 3 rating slots + edition + content = 7.
  assert.strictEqual(laid.drawables.length, 7);
  const res = laid.drawables.find((d) => d.family === 'resolution');
  const aud = laid.drawables.find((d) => d.family === 'audio');
  assert.ok(res.y < aud.y, 'audio stacks below resolution in the TL corner');
  assert.strictEqual(res.x, 0.02, 'TL margin is 20/1000');
  const ratingDraws = laid.drawables.filter((d) => d.family === 'ratings');
  assert.strictEqual(ratingDraws.length, 3);
  assert.ok(ratingDraws[0].x < ratingDraws[1].x && ratingDraws[1].x < ratingDraws[2].x, 'rating slots lay out left→right');
  assert.ok(ratingDraws[0].y > 0.8, 'ratings sit in the bottom-left');
  // Per-family geometry overrides: corner move + offset shift apply.
  const moved = layoutFamilyDrawables(
    { resolution: { on: true, corner: 'br', dx: -0.05, dy: 0 } }, rich, aspectOf);
  const movedRes = moved.drawables.find((d) => d.family === 'resolution');
  assert.ok(movedRes.x > 0.4 && movedRes.y > 0.85, 'corner + dx overrides move the badge');
  // familySettings clamps junk instead of propagating it.
  const clamped = familySettings({ resolution: { size: 99, dx: -5 } }, 'resolution');
  assert.strictEqual(clamped.size, 2);
  assert.strictEqual(clamped.dx, -0.3);
  m += 8;

  // ---- evaluateRun with the family layout shape --------------------------
  const items = [
    { ratingKey: '1', title: 'Rich' },
    { ratingKey: '2', title: 'Bare' },
    { ratingKey: '3', title: 'Dead' },
  ];
  const fbk = { 1: rich, 2: bare, 3: { __error: 'boom' } };
  const layout = { families: { resolution: { on: true }, ratings: { on: true, slots: ['tmdb'] } }, custom: [] };
  const run = evaluateRun(items, fbk, layout, aspectOf);
  assert.strictEqual(run.willApply.length, 1);
  assert.deepStrictEqual(run.willApply[0].badges, [
    { badgeId: 'pill-rescombo', label: '4K • DV • HDR', family: 'resolution' },
    { badgeId: 'star', label: '8.7', family: 'ratings' },
  ]);
  assert.strictEqual(run.skipped.length, 1);
  assert.match(run.skipped[0].reason, /Resolution: no resolution\/HDR data/);
  assert.match(run.skipped[0].reason, /Ratings: no ratings found/);
  assert.strictEqual(run.failedFacts.length, 1);
  // Families + custom placements compose in one run.
  const mixed = evaluateRun(items, fbk,
    { families: { versions: { on: true } }, custom: [{ badgeId: 'badge-hdr', rule: { kind: 'is-hdr' } }] },
    aspectOf);
  const richBadges = mixed.willApply.find((w) => w.item.title === 'Rich').badges;
  assert.deepStrictEqual(richBadges.map((b) => b.badgeId), ['pill-versions', 'badge-hdr']);
  // All families off + no custom = an empty layout, said plainly.
  const empty = evaluateRun(items, fbk, { families: { resolution: { on: false } }, custom: [] }, aspectOf);
  // v1.24: the skip reason names all three content kinds — a layout
  // with only custom badges or pins enabled is no longer "empty".
  assert.strictEqual(empty.skipped[0].reason, 'no families, custom badges, or pins enabled in the layout');
  m += 9;

  // ---- aurora variants: same layout, richer art ids -------------------
  // The swap covers the family shell AND the ratings slot glyphs;
  // aurora canvases are pixel-identical in size to classic, so the
  // geometry asserted above cannot shift when a variant flips.
  const auroraLaid = layoutFamilyDrawables(
    { resolution: { on: true, variant: 'aurora' }, ratings: { on: true, slots: ['tmdb', 'imdb', 'rt-critic'], variant: 'aurora' } },
    rich, aspectOf);
  assert.deepStrictEqual(auroraLaid.drawables.map((d) => d.badgeId),
    ['pill-rescombo-aurora', 'star-aurora', 'imdb-aurora', 'tomato-aurora']);
  const classicLaid = layoutFamilyDrawables(
    { resolution: { on: true }, ratings: { on: true, slots: ['tmdb', 'imdb', 'rt-critic'] } },
    rich, aspectOf);
  assert.deepStrictEqual(classicLaid.drawables.map((d) => d.badgeId),
    ['pill-rescombo', 'star', 'imdb', 'tomato']);
  const auroraStatus = layoutFamilyDrawables(
    { status: { on: true, variant: 'aurora' } }, { type: 'show', tvStatus: 'Returning Series' }, aspectOf);
  assert.strictEqual(auroraStatus.drawables[0].badgeId, 'ribbon-status-aurora');
  // familySettings clamps the variant like every other setting.
  assert.strictEqual(familySettings({ resolution: { variant: 'aurora' } }, 'resolution').variant, 'aurora');
  assert.strictEqual(familySettings({ resolution: { variant: 'neon' } }, 'resolution').variant, 'classic');
  assert.strictEqual(familySettings({}, 'resolution').variant, 'classic');
  assert.strictEqual(auroraShell('badge-4k'), 'badge-4k'); // no aurora twin — passes through unchanged
  m += 7;

  // ---- signature variant + new families (v1.5) ------------------------
  // Variant clamping: signature is a first-class variant; fresh
  // layouts default to it; stored layouts without one stay classic.
  assert.strictEqual(familySettings({ resolution: { variant: 'signature' } }, 'resolution').variant, 'signature');
  assert.strictEqual(defaultFamilies().resolution.variant, 'signature');
  assert.strictEqual(defaultFamilies().audio.variant, 'signature');
  assert.strictEqual(defaultFamilies().source.on, false); // new families default OFF
  m += 4;

  // source — the literal tag or nothing.
  fShows('source', {}, { sourceTag: 'REMUX' }, 'REMUX');
  fShows('source', {}, { sourceTag: 'Blu-ray' }, 'Blu-ray');
  fHides('source', {}, { sourceTag: null });
  fHides('source', {}, bare);

  // details — movies: year/runtime; shows: year/seasons/episodes,
  // existing parts only, singular/plural correct.
  fShows('details', {}, { type: 'movie', year: 2024, runtimeMinutes: 136 }, '2024 • 2H 16M');
  fShows('details', {}, { type: 'movie', runtimeMinutes: 95 }, '1H 35M');
  fShows('details', {}, { type: 'movie', year: 1999 }, '1999');
  fShows('details', {}, { type: 'show', year: 2019, seasonCount: 5, showEpisodeCount: 62 }, '2019 • 5 SEASONS • 62 EPISODES');
  fShows('details', {}, { type: 'show', seasonCount: 1, showEpisodeCount: 1 }, '1 SEASON • 1 EPISODE');
  fHides('details', {}, bare);
  fHides('details', {}, { type: 'show' });

  // subtitles — tagged languages fire; untagged streams hide with
  // the disclosure note (no invented "CC"/"Available").
  fShows('subtitles', {}, { subtitleLangs: ['eng', 'spa'], hasSubs: true }, 'ENG • SPA');
  const subUntagged = fam('subtitles', {}, { subtitleLangs: [], hasSubs: true });
  assert.strictEqual(subUntagged.show, false);
  assert.match(subUntagged.note, /no language tags/);
  fHides('subtitles', {}, { subtitleLangs: [], hasSubs: false });
  fHides('subtitles', {}, bare);
  m += 2;

  // audio detail=full — format • channels • language, parts omitted
  // when unknown; channel layout formatting 6→5.1, 8→7.1, else "N ch".
  fShows('audio', { detail: 'full' }, { atmos: true, bestAudioChannels: 6, bestAudioLang: 'eng' }, 'Dolby Atmos • 5.1 • ENG');
  fShows('audio', { detail: 'full' }, { truehd: true, bestAudioChannels: 8, bestAudioLang: 'jpn' }, 'TrueHD • 7.1 • JPN');
  fShows('audio', { detail: 'full' }, { dtsx: true }, 'DTS:X');
  fShows('audio', { detail: 'full' }, { bestAudioLabel: 'aac • 2.0 • eng', bestAudioChannels: 5 }, 'AAC • 5 ch');
  fShows('audio', { detail: 'format' }, { atmos: true, bestAudioChannels: 6, bestAudioLang: 'eng' }, 'Dolby Atmos');
  assert.strictEqual(audioChannelLabel(2), '2.0');
  assert.strictEqual(audioChannelLabel(1), '1.0');
  assert.strictEqual(audioChannelLabel(0), null);
  m += 3;

  // ---- runtime family + subtitles modes ------------------------------
  // Runtime formats: 'hm' is the details composition; 'clock' is H:MM.
  fShows('runtime', {}, { type: 'movie', runtimeMinutes: 148 }, '2H 28M');
  fShows('runtime', {}, { type: 'movie', runtimeMinutes: 120 }, '2H');
  fShows('runtime', {}, { type: 'episode', runtimeMinutes: 48 }, '48M');
  fShows('runtime', { format: 'clock' }, { type: 'movie', runtimeMinutes: 148 }, '2:28');
  fShows('runtime', { format: 'clock' }, { type: 'episode', runtimeMinutes: 48 }, '0:48');
  const rtShow = fam('runtime', {}, { type: 'show' });
  assert.strictEqual(rtShow.show, false);
  assert.match(rtShow.note, /per-episode runtimes/);
  const rtNone = fam('runtime', {}, { type: 'movie' });
  assert.strictEqual(rtNone.show, false);
  assert.match(rtNone.note, /no duration/);
  m += 2;
  // format/mode clamps, mirroring the audio detail clamp.
  assert.strictEqual(familySettings({ runtime: { format: 'clock' } }, 'runtime').format, 'clock');
  assert.strictEqual(familySettings({ runtime: { format: 'weird' } }, 'runtime').format, 'hm');
  assert.strictEqual(familySettings({ subtitles: { mode: 'full' } }, 'subtitles').mode, 'full');
  assert.strictEqual(familySettings({ subtitles: { mode: 'weird' } }, 'subtitles').mode, 'languages');
  assert.strictEqual(defaultFamilies().runtime.on, false); // runtime family default OFF
  m += 5;

  // Details dedupe: runtime family ON in the layout ⇒ details drops
  // its runtime part (year stays); runtime OFF ⇒ today's text exactly.
  const dedupeFacts = { type: 'movie', year: 2024, runtimeMinutes: 148 };
  const withRt = layoutFamilyDrawables({ details: { on: true }, runtime: { on: true } }, dedupeFacts, aspectOf);
  assert.strictEqual(withRt.verdicts.find((v) => v.family === 'details').text, '2024');
  assert.strictEqual(withRt.verdicts.find((v) => v.family === 'runtime').text, '2H 28M');
  const noRt = layoutFamilyDrawables({ details: { on: true } }, dedupeFacts, aspectOf);
  assert.strictEqual(noRt.verdicts.find((v) => v.family === 'details').text, '2024 • 2H 28M');
  m += 3;

  // Subtitles modes — count fires on UNTAGGED streams (a count is a
  // fact, not a guess); full composes only the parts facts state.
  fShows('subtitles', { mode: 'count' }, { subtitleLangs: [], hasSubs: true, subtitleCount: 4 }, '4 TRACKS');
  fShows('subtitles', { mode: 'count' }, { subtitleLangs: ['eng'], hasSubs: true, subtitleCount: 1 }, '1 TRACK');
  fHides('subtitles', { mode: 'count' }, { subtitleLangs: [], hasSubs: false, subtitleCount: 0 });
  fShows('subtitles', { mode: 'full' }, { subtitleLangs: ['eng', 'spa'], hasSubs: true, subtitleCount: 4, subtitleSDH: true }, 'ENG • SPA • 4 TRACKS • SDH');
  fShows('subtitles', { mode: 'full' }, { subtitleLangs: [], hasSubs: true, subtitleCount: 2, subtitleForced: true }, '2 TRACKS • FORCED');
  fShows('subtitles', { mode: 'full' }, { subtitleLangs: ['jpn'], hasSubs: true }, 'JPN');
  fHides('subtitles', { mode: 'full' }, bare);
  fHides('subtitles', { mode: 'full' }, { subtitleLangs: [], hasSubs: false });

  // Shells: runtime and subtitles own their classic shells now (the
  // subtitles family no longer borrows pill-language), Aurora swaps
  // too, and the signature runtime drawable carries RUNTIME.
  const rtClassic = layoutFamilyDrawables({ runtime: { on: true } }, { type: 'movie', runtimeMinutes: 95 }, aspectOf);
  assert.strictEqual(rtClassic.drawables[0].badgeId, 'pill-runtime');
  const subClassic = layoutFamilyDrawables({ subtitles: { on: true } }, { subtitleLangs: ['eng'], hasSubs: true }, aspectOf);
  assert.strictEqual(subClassic.drawables[0].badgeId, 'pill-subtitles');
  const subAurora = layoutFamilyDrawables({ subtitles: { on: true, variant: 'aurora' } }, { subtitleLangs: ['eng'], hasSubs: true }, aspectOf);
  assert.strictEqual(subAurora.drawables[0].badgeId, 'pill-subtitles-aurora');
  const rtSig = layoutFamilyDrawables({ runtime: { on: true, variant: 'signature' } }, { type: 'movie', runtimeMinutes: 95 }, aspectOf);
  assert.strictEqual(rtSig.drawables[0].microLabel, 'RUNTIME');
  assert.strictEqual(rtSig.drawables[0].label, '1H 35M');
  m += 5;

  // signature layout — procedural drawables with micro-labels, shape
  // heights, and text-measured dynamic widths.
  const sigFacts = {
    ...rich, sourceTag: 'REMUX', year: 2024, runtimeMinutes: 148,
    subtitleLangs: ['eng', 'spa'], hasSubs: true, bestAudioChannels: 8, bestAudioLang: 'eng',
  };
  const sigLaid = layoutFamilyDrawables({
    resolution: { on: true, variant: 'signature' },
    audio: { on: true, variant: 'signature', detail: 'full' },
    ratings: { on: true, slots: ['tmdb', 'imdb', 'rt-critic'], variant: 'signature' },
    'content-rating': { on: true, variant: 'signature' },
    source: { on: true, variant: 'signature' },
    details: { on: true, variant: 'signature' },
    subtitles: { on: true, variant: 'signature' },
  }, sigFacts, aspectOf);
  const byFam = {};
  for (const d of sigLaid.drawables) (byFam[d.family] = byFam[d.family] || []).push(d);
  assert.ok(sigLaid.drawables.length >= 9, 'signature layout emits every fired family');
  assert.ok(sigLaid.drawables.every((d) => d.renderMode === 'signature'));
  assert.ok(sigLaid.drawables.every((d) => typeof d.microLabel === 'string' && d.microLabel.length > 0));
  assert.ok(sigLaid.drawables.every((d) => typeof d.hFrac === 'number' && d.hFrac > 0));
  const resD = byFam.resolution[0];
  assert.strictEqual(resD.microLabel, 'RESOLUTION');
  assert.strictEqual(resD.shape, 'pill');
  assert.ok(Math.abs(resD.hFrac - 86 / 1500) < 1e-9, 'pill height 86/1500');
  assert.strictEqual(resD.label, '4K • DV • HDR');
  const audD = byFam.audio[0];
  assert.strictEqual(audD.microLabel, 'AUDIO');
  assert.strictEqual(audD.label, 'Dolby Atmos • 7.1 • ENG');
  assert.strictEqual(byFam.ratings.length, 3);
  assert.ok(byFam.ratings.every((d) => Math.abs(d.hFrac - 76 / 1500) < 1e-9), 'chip height 76/1500');
  assert.deepStrictEqual(byFam.ratings.map((d) => d.microLabel), ['TMDB', 'IMDB', 'RT']);
  assert.ok(byFam.ratings.every((d) => d.wFrac <= 0.30), 'chip width cap 0.30');
  const crD = byFam['content-rating'][0];
  assert.strictEqual(crD.shape, 'ratingbox');
  assert.strictEqual(crD.microLabel, 'RATING');
  assert.ok(Math.abs(crD.hFrac - 96 / 1500) < 1e-9, 'ratingbox height 96/1500');
  assert.strictEqual(byFam.source[0].label, 'REMUX');
  assert.strictEqual(byFam.source[0].microLabel, 'SOURCE');
  assert.strictEqual(byFam.details[0].label, '2024 • 2H 28M');
  assert.strictEqual(byFam.subtitles[0].label, 'ENG • SPA');
  assert.strictEqual(byFam.subtitles[0].microLabel, 'SUBTITLES');
  m += 24;

  // Dynamic width: longer values earn wider badges.
  const wShort = layoutFamilyDrawables({ source: { on: true, variant: 'signature' } }, { sourceTag: 'DVD' }, aspectOf).drawables[0];
  const wLong = layoutFamilyDrawables({ source: { on: true, variant: 'signature' } }, { sourceTag: 'Blu-ray' }, aspectOf).drawables[0];
  assert.ok(wLong.wFrac > wShort.wFrac, 'width grows with longer text');
  const wAudShort = layoutFamilyDrawables({ audio: { on: true, variant: 'signature' } }, { atmos: true }, aspectOf).drawables[0];
  const wAudLong = layoutFamilyDrawables({ audio: { on: true, variant: 'signature', detail: 'full' } }, { atmos: true, bestAudioChannels: 8, bestAudioLang: 'eng' }, aspectOf).drawables[0];
  assert.ok(wAudLong.wFrac > wAudShort.wFrac, 'full-detail audio is wider than format-only');
  assert.ok(signatureTextWidth('WWWW', 20, 'value') > signatureTextWidth('iiii', 20, 'value'), 'estimator: wide glyphs cost more');
  assert.ok(signatureTextWidth('HELLO', 40, 'value') > signatureTextWidth('HELLO', 20, 'value'), 'estimator scales with px');
  m += 4;

  // Row-width guard: the widest possible ratings row (size 2, every
  // chip at its 0.30 cap) lands exactly at the 92% row budget once
  // gaps are counted — the layout's proportional scale-down keeps
  // anything beyond that inside the budget by construction.
  const wideChips = layoutFamilyDrawables(
    { ratings: { on: true, size: 2, slots: ['tmdb', 'imdb', 'rt-critic'], variant: 'signature' } },
    { tmdbRating: 100, imdbRating: 100, rtRating: 100 }, aspectOf);
  assert.strictEqual(wideChips.drawables.length, 3);
  const rowTotal = wideChips.drawables.reduce((a, d) => a + d.wFrac, 0) + 0.01 * (wideChips.drawables.length - 1);
  assert.ok(rowTotal <= 0.92 + 1e-6, `maximal row fits 0.92 (got ${rowTotal.toFixed(3)})`);
  assert.ok(wideChips.drawables.every((d) => d.wFrac <= 0.30 + 1e-9), 'chips respect the 0.30 cap');
  m += 3;

  // ---- badge-pack families ------------------------------------------
  // watch-status — unwatchedCount is the whole contract: movies and
  // episodes draw UNWATCHED, shows draw the episode remainder, and
  // watched titles stay bare unless showWatched opts in.
  fShows('watch-status', {}, { type: 'movie', unwatchedCount: 1 }, 'UNWATCHED');
  fShows('watch-status', {}, { type: 'episode', unwatchedCount: 1 }, 'UNWATCHED');
  const wsWatched = fam('watch-status', {}, { type: 'movie', unwatchedCount: 0 });
  assert.strictEqual(wsWatched.show, false);
  assert.match(wsWatched.note, /badge off for watched titles/);
  fShows('watch-status', { showWatched: true }, { type: 'movie', unwatchedCount: 0 }, 'WATCHED');
  fShows('watch-status', {}, { type: 'show', unwatchedCount: 3 }, '3 UNWATCHED');
  fShows('watch-status', {}, { type: 'show', unwatchedCount: 1 }, '1 UNWATCHED');
  fHides('watch-status', {}, { type: 'show', unwatchedCount: 0 });
  fShows('watch-status', { showWatched: true }, { type: 'show', unwatchedCount: 0 }, 'ALL WATCHED');
  const wsNone = fam('watch-status', {}, { type: 'movie', unwatchedCount: null });
  assert.strictEqual(wsNone.show, false);
  assert.match(wsNone.note, /no watch data/);
  fHides('watch-status', {}, bare);
  m += 3;

  // new — addedAtDays against the window; note states both numbers.
  const newV = fam('new', { windowDays: 30 }, { addedAtDays: 3 });
  assert.strictEqual(newV.show, true);
  assert.strictEqual(newV.text, 'NEW');
  assert.strictEqual(newV.note, 'added 3d ago (window 30d)');
  fShows('new', {}, { addedAtDays: 30 }, 'NEW'); // boundary: <= window fires
  fHides('new', { windowDays: 30 }, { addedAtDays: 31 });
  fHides('new', { windowDays: 7 }, { addedAtDays: 12.5 });
  fHides('new', {}, { addedAtDays: null });
  fHides('new', {}, bare);
  m += 3;

  // decade — floor-to-decade math, from the year fact only.
  fShows('decade', {}, { year: 1994 }, '1990s');
  fShows('decade', {}, { year: 2008 }, '2000s');
  fShows('decade', {}, { year: 2000 }, '2000s');
  fShows('decade', {}, { year: 1985 }, '1980s');
  fHides('decade', {}, { year: null });
  fHides('decade', {}, bare);

  // video-codec — display map or an honest hide that NAMES the raw
  // codec; unmapped never gets dressed up.
  fShows('video-codec', {}, { videoCodec: 'hevc' }, 'HEVC');
  fShows('video-codec', {}, { videoCodec: 'h264' }, 'H.264');
  fShows('video-codec', {}, { videoCodec: 'av1' }, 'AV1');
  fShows('video-codec', {}, { videoCodec: 'vp9' }, 'VP9');
  fShows('video-codec', {}, { videoCodec: 'vp8' }, 'VP8');
  fShows('video-codec', {}, { videoCodec: 'mpeg2video' }, 'MPEG-2');
  fShows('video-codec', {}, { videoCodec: 'mpeg4' }, 'MPEG-4');
  fShows('video-codec', {}, { videoCodec: 'vc1' }, 'VC-1');
  const vcUnknown = fam('video-codec', {}, { videoCodec: 'wmv3' });
  assert.strictEqual(vcUnknown.show, false);
  assert.strictEqual(vcUnknown.note, "unmapped codec 'wmv3'");
  fHides('video-codec', {}, { videoCodec: null });
  fHides('video-codec', {}, bare);
  m += 2;

  // streaming — region picked from the full map at evaluate time;
  // display-name mapping; mode 'all' caps at three.
  const streamFacts = {
    streamingByRegion: {
      US: ['Netflix', 'Disney Plus', 'Amazon Prime Video', 'Max'],
      GB: ['Netflix'],
    },
  };
  fShows('streaming', {}, streamFacts, 'NETFLIX');
  fShows('streaming', { mode: 'all' }, streamFacts, 'NETFLIX • DISNEY+ • PRIME VIDEO');
  fShows('streaming', { region: 'GB' }, streamFacts, 'NETFLIX');
  fShows('streaming', { region: 'GB', mode: 'all' }, { streamingByRegion: { GB: ['Apple TV Plus', 'Paramount Plus'] } }, 'APPLE TV+ • PARAMOUNT+');
  const stRegion = fam('streaming', { region: 'DE' }, streamFacts);
  assert.strictEqual(stRegion.show, false);
  assert.strictEqual(stRegion.note, 'no TMDb streaming data for DE');
  const stNone = fam('streaming', {}, bare);
  assert.strictEqual(stNone.show, false);
  assert.match(stNone.note, /needs a TMDb key and a TMDb match/);
  assert.strictEqual(streamingDisplayName('Disney Plus'), 'Disney+');
  assert.strictEqual(streamingDisplayName('Some Service'), 'Some Service');
  m += 4;

  // studio — verbatim or nothing. genre — first N of Plex's tags.
  fShows('studio', {}, { studio: 'A24' }, 'A24');
  fShows('studio', {}, { studio: 'HBO' }, 'HBO');
  fHides('studio', {}, { studio: null });
  fHides('studio', {}, bare);
  fShows('genre', {}, { genres: ['Horror', 'Thriller'] }, 'HORROR');
  fShows('genre', { count: 2 }, { genres: ['Horror', 'Thriller'] }, 'HORROR • THRILLER');
  fShows('genre', { count: 2 }, { genres: ['Comedy'] }, 'COMEDY');
  fHides('genre', {}, { genres: [] });
  fHides('genre', {}, bare);

  // ribbon honor=awards — literal regexes over OMDb's sentence,
  // Oscar outranks Emmy when both are stated.
  fShows('ribbon', { honor: 'awards' }, { awardsText: 'Won 2 Oscars. 45 wins & 32 nominations total' }, 'OSCAR WINNER');
  fShows('ribbon', { honor: 'awards' }, { awardsText: 'Nominated for 1 Oscar. 3 wins & 5 nominations total' }, 'OSCAR NOMINEE');
  fShows('ribbon', { honor: 'awards' }, { awardsText: 'Won 3 Primetime Emmys. 10 wins total' }, 'EMMY WINNER');
  fShows('ribbon', { honor: 'awards' }, { awardsText: 'Won 1 Emmy. 2 wins total' }, 'EMMY WINNER');
  fShows('ribbon', { honor: 'awards' }, { awardsText: 'Nominated for 2 Emmys. 1 win total' }, 'EMMY NOMINEE');
  fShows('ribbon', { honor: 'awards' }, { awardsText: 'Won 1 Golden Globe. 4 wins total' }, 'GOLDEN GLOBE WINNER');
  fShows('ribbon', { honor: 'awards' }, { awardsText: 'Nominated for 1 Golden Globe. 2 wins total' }, 'GOLDEN GLOBE NOMINEE');
  fShows('ribbon', { honor: 'awards' }, { awardsText: 'Won 1 Oscar. Won 2 Primetime Emmys. 9 wins total' }, 'OSCAR WINNER');
  const awNone = fam('ribbon', { honor: 'awards' }, { awardsText: '3 wins & 5 nominations total' });
  assert.strictEqual(awNone.show, false);
  assert.match(awNone.note, /no Oscar\/Emmy\/Globe win or nomination/);
  fHides('ribbon', { honor: 'awards' }, bare);
  m += 2;

  // Settings clamps for the new families.
  assert.strictEqual(familySettings({ 'watch-status': { showWatched: true } }, 'watch-status').showWatched, true);
  assert.strictEqual(familySettings({ 'watch-status': { showWatched: 'yes' } }, 'watch-status').showWatched, false);
  assert.strictEqual(familySettings({ new: { windowDays: 0 } }, 'new').windowDays, 1);
  assert.strictEqual(familySettings({ new: { windowDays: 999 } }, 'new').windowDays, 365);
  assert.strictEqual(familySettings({ new: { windowDays: 'soon' } }, 'new').windowDays, 30);
  assert.strictEqual(familySettings({ streaming: { region: 'gb' } }, 'streaming').region, 'GB');
  assert.strictEqual(familySettings({ streaming: { region: 'USA' } }, 'streaming').region, 'US');
  assert.strictEqual(familySettings({ streaming: { region: 12 } }, 'streaming').region, 'US');
  assert.strictEqual(familySettings({ streaming: { mode: 'all' } }, 'streaming').mode, 'all');
  assert.strictEqual(familySettings({ streaming: { mode: 'languages' } }, 'streaming').mode, 'first');
  assert.strictEqual(familySettings({ subtitles: { mode: 'all' } }, 'subtitles').mode, 'languages');
  assert.strictEqual(familySettings({ genre: { count: 2 } }, 'genre').count, 2);
  assert.strictEqual(familySettings({ genre: { count: 5 } }, 'genre').count, 1);
  assert.strictEqual(familySettings({ ribbon: { honor: 'awards' } }, 'ribbon').honor, 'awards');
  // The new families default OFF in a fresh layout.
  for (const id of ['watch-status', 'new', 'decade', 'video-codec', 'streaming', 'studio', 'genre']) {
    assert.strictEqual(defaultFamilies()[id].on, false, `${id} defaults off`);
  }
  m += 21;

  // Signature wiring: every new family carries its micro-label and
  // stacks in its corner (watch-status last in TR, codec in TL).
  const sigPack = layoutFamilyDrawables({
    'watch-status': { on: true, variant: 'signature' },
    new: { on: true, variant: 'signature' },
    decade: { on: true, variant: 'signature' },
    'video-codec': { on: true, variant: 'signature' },
    streaming: { on: true, variant: 'signature' },
    studio: { on: true, variant: 'signature' },
    genre: { on: true, variant: 'signature' },
  }, {
    type: 'movie', unwatchedCount: 1, addedAtDays: 2, year: 1994,
    videoCodec: 'hevc', streamingByRegion: { US: ['Netflix'] },
    studio: 'A24', genres: ['Horror'],
  }, aspectOf);
  const microOf = (fam2) => sigPack.drawables.find((d) => d.family === fam2)?.microLabel;
  assert.strictEqual(microOf('watch-status'), 'WATCH');
  assert.strictEqual(microOf('new'), 'ADDED');
  assert.strictEqual(microOf('decade'), 'DECADE');
  assert.strictEqual(microOf('video-codec'), 'CODEC');
  assert.strictEqual(microOf('streaming'), 'STREAMING');
  assert.strictEqual(microOf('studio'), 'STUDIO');
  assert.strictEqual(microOf('genre'), 'GENRE');
  assert.ok(sigPack.drawables.every((d) => d.renderMode === 'signature'));
  const wsD = sigPack.drawables.find((d) => d.family === 'watch-status');
  const newD = sigPack.drawables.find((d) => d.family === 'new');
  assert.ok(newD.y < wsD.y, 'NEW stacks above watch-status in TR');
  const vcD = sigPack.drawables.find((d) => d.family === 'video-codec');
  assert.ok(vcD.x === 0.02 && vcD.y < 0.1, 'video-codec sits in the TL stack');
  m += 11;

  // Classic shells: the new families point at their own pill art.
  const classicPack = layoutFamilyDrawables({
    'watch-status': { on: true }, decade: { on: true }, 'video-codec': { on: true },
    streaming: { on: true }, studio: { on: true }, genre: { on: true }, new: { on: true },
  }, {
    type: 'movie', unwatchedCount: 1, addedAtDays: 2, year: 1994,
    videoCodec: 'hevc', streamingByRegion: { US: ['Netflix'] },
    studio: 'A24', genres: ['Horror'],
  }, aspectOf);
  assert.deepStrictEqual(classicPack.drawables.map((d) => d.badgeId).sort(),
    ['pill-decade', 'pill-genre', 'pill-new', 'pill-streaming', 'pill-studio', 'pill-video-codec', 'pill-watch-status']);
  m += 1;

  // ---- seasons (v1.9): episode-aggregate facts, identity families dark ----
  // Season facts are built by the facts layer from the season's OWN
  // episodes; families judge them exactly like item facts, except the
  // movie/show identity families that stay dark for seasons by design:
  // ribbon and decade are gated here even when the season carries
  // facts that would fire them for a movie or show (its own year, its
  // own rating). The remaining identity families (studio, genre,
  // source, edition, streaming) never fire for seasons because the
  // facts layer never carries those fields for one — pinned in the Go
  // season facts tests.
  const seasonF = {
    type: 'season', resolutionTier: '1080p', dv: false, hdr10: true, hlg: false, hdrAny: true,
    videoCodec: 'hevc', atmos: true, bestAudioLabel: 'English (TrueHD Atmos 7.1)', bestAudioChannels: 8,
    imdbRating: 8.4, tmdbRating: null, rtRating: null, rtAudience: null, metascore: null,
    contentRating: 'TV-MA', year: 2019, runtimeMinutes: 170,
    episodeCount: 3, episodeTotal: 3, langsFromEpisodes: true, langsPartial: false,
    audioLangs: ['eng', 'jpn'], subtitleLangs: ['eng'], hasSubs: true, subtitleCount: 2,
    dubClass: 'dual', audioTracks: 2, mediaCount: 0, unwatchedCount: 2, addedAtDays: 5,
  };
  fShows('resolution', {}, seasonF, '1080p • HDR');
  fShows('audio', {}, seasonF, 'Dolby Atmos');
  fShows('video-codec', {}, seasonF, 'HEVC');
  fShows('ratings', {}, seasonF);                       // its OWN imdb rating resolves a slot
  fHides('ratings', {}, { ...seasonF, imdbRating: null });
  fShows('content-rating', {}, seasonF, 'TV-MA');       // the season record's own field
  fShows('details', {}, seasonF, '2019 • 3 EPISODES • 2H 50M');
  fShows('runtime', {}, seasonF, '2H 50M');             // summed across the season's episodes
  fShows('watch-status', {}, seasonF, '2 UNWATCHED');
  fShows('watch-status', { showWatched: true }, { ...seasonF, unwatchedCount: 0 }, 'ALL WATCHED');
  fShows('languages', {}, seasonF, 'ENG · JPN');
  fShows('subtitles', {}, seasonF, 'ENG');
  fShows('new', {}, seasonF, 'NEW');
  fHides('ribbon', { honor: 'rt-fresh' }, { ...seasonF, rtRating: 95 });
  fHides('ribbon', { honor: 'top-rated', source: 'imdb', threshold: 8 }, seasonF);
  fHides('decade', {}, seasonF);                        // its own year never fires a decade
  fHides('status', {}, seasonF);
  fHides('versions', {}, seasonF);                      // mediaCount is 0 for seasons by design

  console.log(`composite.test.cjs: families checks passed (${m} evaluateFamily/layout assertions)`);
}

// ---- v1.9.1 style pass: proportion, legibility, single-sourcing ------
// Driven by a live render the owner rejected: a text-measured
// languages badge had grown into a top slab (defect B), the watch
// count read as part of the languages badge while the watch-status
// badge repeated it (A), the generic count pill drew a bare numeral
// (C), and sub-floor chips rendered as specks (D). These pins hold
// the four rules in every style.
{
  let s191 = 0;
  const aspectOf = () => 0.2857;
  const realAspect = (id) => ({ star: 1.0, imdb: 0.5, tomato: 1.0, popcorn: 1.2727 }[id] || 0.2857);
  const showFacts = {
    type: 'show', audioLangs: ['eng', 'jpn'], subtitleLangs: ['eng', 'jpn', 'spa'], hasSubs: true,
    dubClass: 'dual', audioTracks: 2, langsFromEpisodes: true, episodeCount: 62, episodeTotal: 62,
    unwatchedCount: 62, tmdbRating: 8.8, imdbRating: 8.5, rtRating: 91, contentRating: 'TV-MA',
  };

  // B — no badge is a banner: the worst-case languages texts
  // (codes+subs with several subtitle languages; six audio languages
  // in codes) hit the width cap instead of the old 0.62 slab.
  for (const [style, facts] of [['codes-subs', showFacts], ['codes', { ...showFacts, audioLangs: ['eng', 'jpn', 'spa', 'fra', 'ger', 'ita'] }]]) {
    const laid = layoutFamilyDrawables({ languages: { on: true, variant: 'signature', style } }, facts, aspectOf);
    assert.strictEqual(laid.drawables.length, 1);
    assert.ok(laid.drawables[0].wFrac <= BADGE_MAX_WFRAC + 1e-9, `languages capped (${style}): ${laid.drawables[0].wFrac}`);
    s191 += 2;
  }
  // B — the cap binds every signature badge, even at size 2 with a
  // long show-details text; classic widths were never dynamic (the
  // languages shell stays its standard 0.27 badge).
  const allSig = layoutFamilyDrawables({ details: { on: true, variant: 'signature', size: 2 } },
    { type: 'show', year: 2019, seasonCount: 5, showEpisodeCount: 62 }, aspectOf);
  assert.ok(allSig.drawables.length === 1 && allSig.drawables[0].wFrac <= BADGE_MAX_WFRAC + 1e-9);
  const langClassic = layoutFamilyDrawables({ languages: { on: true } }, showFacts, aspectOf);
  assert.ok(Math.abs(langClassic.drawables[0].wFrac - 0.27) < 1e-9);
  s191 += 2;

  // A — languages text is languages only, in every style, with
  // watch-status on or off, whatever the watch facts say: it is
  // INVARIANT to unwatchedCount, and the count lives in the
  // watch-status badge alone.
  const langText = (style, wc, wsOn) => {
    const fams = { languages: { on: true, variant: 'signature', style } };
    if (wsOn) fams['watch-status'] = { on: true, variant: 'signature' };
    const laid = layoutFamilyDrawables(fams, { ...showFacts, unwatchedCount: wc }, aspectOf);
    return laid.drawables.find((d) => d.family === 'languages').text;
  };
  for (const style of ['codes', 'class', 'codes-subs']) {
    const base = langText(style, 0, false);
    for (const wc of [1, 62]) {
      assert.strictEqual(langText(style, wc, true), base, `languages text invariant to watch facts (${style})`);
      assert.strictEqual(langText(style, wc, false), base);
    }
    assert.ok(!/WATCHED|✓/.test(base), `no watch state in languages text (${style})`);
    s191 += 5;
  }
  const wsLaid = layoutFamilyDrawables({ 'watch-status': { on: true, variant: 'signature' } }, showFacts, aspectOf);
  assert.strictEqual(wsLaid.drawables[0].text, '62 UNWATCHED');
  s191++;

  // C — the generic count pill's rule label is the counted text
  // (also pinned at the top of this file where the contract lives).
  assert.strictEqual(evaluateRule({ kind: 'unwatched' }, { unwatchedCount: 3 }).label, '3 UNWATCHED');
  assert.strictEqual(evaluateRule({ kind: 'unwatched' }, { unwatchedCount: 1 }).label, 'UNWATCHED');
  s191 += 2;

  // D — signature ratings chips at size 1 render (nominal value text
  // 76 × 0.34 = 25.8px ≥ the 24px floor); at size 0.8 (20.7px) they
  // do not render at all, and the verdict says why.
  const chips1 = layoutFamilyDrawables({ ratings: { on: true, variant: 'signature', slots: ['tmdb', 'imdb'] } }, showFacts, aspectOf);
  assert.strictEqual(chips1.drawables.length, 2);
  const chips08 = layoutFamilyDrawables({ ratings: { on: true, variant: 'signature', size: 0.8, slots: ['tmdb', 'imdb'] } }, showFacts, aspectOf);
  assert.strictEqual(chips08.drawables.length, 0);
  const v08 = chips08.verdicts.find((v) => v.family === 'ratings');
  assert.strictEqual(v08.show, true);
  assert.strictEqual(v08.undrawn, true);
  assert.match(v08.note, /legibility floor/);
  s191 += 5;
  // D — evaluateRun honesty: an item whose only badge was
  // floor-dropped is skipped, with the floor as the reason.
  const runFloor = evaluateRun([{ ratingKey: '9', title: 'Tiny ratings' }], { 9: showFacts },
    { families: { ratings: { on: true, variant: 'signature', size: 0.8, slots: ['tmdb'] } }, custom: [] }, aspectOf);
  assert.strictEqual(runFloor.willApply.length, 0);
  assert.strictEqual(runFloor.skipped.length, 1);
  assert.match(runFloor.skipped[0].reason, /legibility floor/);
  s191 += 3;
  // D — classic slots: slot aspects differ per glyph, so at size 0.5
  // the flat IMDb slot (0.5 aspect → 22px text) drops while the
  // square star slot (44px) stays — a partial drop, disclosed.
  const classicHalf = layoutFamilyDrawables({ ratings: { on: true, size: 0.5, slots: ['tmdb', 'imdb'] } }, showFacts, realAspect);
  assert.deepStrictEqual(classicHalf.drawables.map((d) => d.slot), ['tmdb']);
  const vHalf = classicHalf.verdicts.find((v) => v.family === 'ratings');
  assert.strictEqual(vHalf.partialDrop, true);
  assert.match(vHalf.note, /legibility floor/);
  s191 += 3;
  // D/B — the constants are the pinned floor and cap.
  assert.strictEqual(LEGIBILITY_MIN_TEXT_PX, 24);
  assert.strictEqual(BADGE_MAX_WFRAC, 0.46);
  s191 += 2;

  console.log(`composite.test.cjs: v1.9.1 style-pass checks passed (${s191} assertions)`);
}

// ---- episodes (v1.10): own facts, landscape geometry, the cap ------
// An episode is judged on its OWN streams — no aggregation — and
// only the technical family set may fire: identity families stay
// dark even when the episode record carries bait fields for them
// (its own rating, edition title, source tag, studio...).
{
  let e = 0;
  const fam = (id, settings, facts) => { e++; return evaluateFamily(id, settings, facts); };
  const fShows = (id, settings, facts, text) => {
    const v = fam(id, settings, facts);
    assert.strictEqual(v.show, true, `${id} should fire for an episode`);
    if (text !== undefined) assert.strictEqual(v.text, text, `${id} text`);
  };
  const fHides = (id, settings, facts) => assert.strictEqual(fam(id, settings, facts).show, false, `${id} should hide for an episode`);
  const aspectOf = () => 0.2857;

  // Baited episode: every identity field populated, on purpose.
  const epA = {
    type: 'episode', resolutionTier: '1080p', dv: false, hdr10: true, hlg: false, hdrAny: true,
    videoCodec: 'hevc', atmos: true, dtsx: false, dtshdma: false, truehd: true,
    bestAudioLabel: 'English (TrueHD Atmos 7.1)', bestAudioChannels: 8, bestAudioLang: 'eng',
    tmdbRating: 9.3, imdbRating: 9.1, rtRating: 95, rtAudience: 94, metascore: 91,
    contentRating: 'TV-MA', editionTitle: 'Extended Edition', sourceTag: 'WEB-DL',
    studio: 'HBO', genres: ['Drama'], awardsText: 'Won 2 Oscars.', tvStatus: 'Returning',
    streamingByRegion: { US: ['Netflix'] }, mediaCount: 2, year: 2024,
    audioLangs: ['eng', 'jpn'], subtitleLangs: ['eng'], hasSubs: true, subtitleCount: 1,
    subtitleForced: false, subtitleSDH: false, dubClass: 'dual', audioTracks: 2,
    runtimeMinutes: 42, unwatchedCount: 1, addedAtDays: 2, width: 1920,
  };
  // A second episode with DIFFERENT streams: the two must judge
  // differently, fact by fact — episodes are not their show.
  const epB = {
    type: 'episode', resolutionTier: '720p', dv: false, hdr10: false, hlg: false, hdrAny: false,
    videoCodec: 'h264', atmos: false, dtsx: false, dtshdma: false, truehd: false,
    bestAudioLabel: 'aac • 2.0 • eng', bestAudioChannels: 2, bestAudioLang: 'eng',
    audioLangs: ['eng'], subtitleLangs: [], hasSubs: false, subtitleCount: 0,
    dubClass: 'dub', audioTracks: 1, runtimeMinutes: 24,
    unwatchedCount: 0, addedAtDays: 90, width: 1280,
  };

  // The allowed set judges on the episode's own facts.
  fShows('resolution', {}, epA, '1080p • HDR');
  fShows('audio', {}, epA, 'Dolby Atmos');
  fShows('video-codec', {}, epA, 'HEVC');
  fShows('subtitles', {}, epA, 'ENG');
  fShows('languages', {}, epA, 'ENG · JPN');
  fShows('runtime', {}, epA, '42M');
  fShows('watch-status', {}, epA, 'UNWATCHED');
  fShows('new', {}, epA, 'NEW');
  // …and the second episode judges differently on its own facts.
  fShows('resolution', {}, epB, '720p');
  fShows('video-codec', {}, epB, 'H.264');
  fShows('audio', {}, epB, 'AAC');
  fShows('languages', {}, epB, 'ENG');
  fShows('runtime', {}, epB, '24M');
  fHides('subtitles', {}, epB);
  fHides('watch-status', {}, epB); // watched, showWatched off
  fHides('new', {}, epB);          // added 90d ago, outside the default window

  // The excluded set stays dark DESPITE the bait — the rule is the
  // episode gate in evaluateFamily, not missing facts.
  for (const id of ['ratings', 'ribbon', 'status', 'content-rating', 'edition',
    'versions', 'source', 'details', 'decade', 'streaming', 'studio', 'genre']) {
    fHides(id, {}, epA);
  }
  const ratingsGate = fam('ratings', { slots: ['imdb'] }, epA);
  assert.match(ratingsGate.note, /episode ratings are not badged/);
  const detailGate = fam('details', {}, epA);
  assert.match(detailGate.note, /not badged on episode stills/);
  // v1.11: the gate gains the three families whose facts are
  // natively per-episode (hdr, channels, aspect-ratio); franchise,
  // origin and collection stay dark (asserted in the v1.11 section).
  // v1.24: bitrate + in-progress join — both are natively per-episode
  // facts (the episode's own file bitrate / playback position).
  assert.deepStrictEqual(EPISODE_FAMILIES.slice().sort(),
    ['aspect-ratio', 'audio', 'bitrate', 'channels', 'hdr', 'in-progress', 'languages', 'new', 'resolution', 'runtime', 'subtitles', 'video-codec', 'watch-status']);
  e += 3;

  // Geometry: episode facts lay out in the landscape space; the
  // floor constant scales with it; the canonical space is untouched.
  assert.deepStrictEqual(geometryFor({ type: 'episode' }), { w: 1600, h: 900 });
  assert.deepStrictEqual(geometryFor({ type: 'movie' }), { w: 1000, h: 1500 });
  assert.deepStrictEqual(geometryFor(null), CANONICAL_GEOM);
  assert.strictEqual(legibilityFloorPx(LANDSCAPE_GEOM), 24 * 0.6);
  assert.strictEqual(legibilityFloorPx(CANONICAL_GEOM), LEGIBILITY_MIN_TEXT_PX);
  assert.strictEqual(EPISODE_BADGE_CAP, 4);
  e += 6;

  // Landscape layout (signature): the realistic five-family set
  // fires five verdicts but only EPISODE_BADGE_CAP draw — template
  // order (FAMILY_DEFS) decides, so video-codec (which follows
  // resolution, audio, watch-status and new in the defs) is the one
  // capped, and its verdict says so instead of vanishing.
  const fiveFams = {
    resolution: { on: true, variant: 'signature' },
    audio: { on: true, variant: 'signature' },
    'video-codec': { on: true, variant: 'signature' },
    'watch-status': { on: true, variant: 'signature' },
    new: { on: true, variant: 'signature' },
  };
  const laid = layoutFamilyDrawables(fiveFams, epA, aspectOf);
  assert.strictEqual(laid.drawables.length, EPISODE_BADGE_CAP,
    `landscape drawables: ${laid.drawables.map((d) => d.family).join(',')}`);
  assert.deepStrictEqual(laid.drawables.map((d) => d.family).sort(),
    ['audio', 'new', 'resolution', 'watch-status']);
  const capped = laid.verdicts.find((v) => v.family === 'video-codec');
  assert.strictEqual(capped.show, true);
  assert.strictEqual(capped.undrawn, true);
  assert.match(capped.note, /episode badge cap \(4\)/);
  e += 5;
  // Every drawable sits inside the landscape frame.
  for (const d of laid.drawables) {
    assert.ok(d.x >= 0 && d.x + d.wFrac <= 1 + 1e-9, `${d.family} inside horizontally (${d.x}+${d.wFrac})`);
    assert.ok(d.y >= 0 && d.y + (d.hFrac || 0) <= 1 + 1e-9, `${d.family} inside vertically (${d.y}+${d.hFrac})`);
    e++;
  }
  // Height-share invariance: the same badge occupies the same share
  // of the frame's height in both geometries, and a smaller share
  // of the wider frame's width.
  const laidMovie = layoutFamilyDrawables({ resolution: { on: true, variant: 'signature' } },
    { ...epA, type: 'movie' }, aspectOf);
  const resEp = laid.drawables.find((d) => d.family === 'resolution');
  const resMovie = laidMovie.drawables[0];
  assert.ok(Math.abs(resEp.hFrac - resMovie.hFrac) < 1e-9, `hFrac ${resEp.hFrac} vs ${resMovie.hFrac}`);
  assert.ok(resEp.wFrac < resMovie.wFrac, 'same badge, smaller width share on the wide frame');
  e += 2;

  // Landscape layout (classic shells): width comes from the height
  // unit, so the shell keeps its height share and proportions —
  // resolution at 0.46 canonical is 460px wide on a poster and
  // 460·0.6 = 276px (wFrac 0.1725) on a still, never 0.46 of 1600.
  const laidClassic = layoutFamilyDrawables({ resolution: { on: true } }, epA, aspectOf);
  assert.strictEqual(laidClassic.drawables.length, 1);
  assert.ok(Math.abs(laidClassic.drawables[0].wFrac - 0.46 * 1000 * 0.6 / 1600) < 1e-9,
    `classic landscape wFrac: ${laidClassic.drawables[0].wFrac}`);
  const cd = laidClassic.drawables[0];
  const cdBottomPx = cd.y * 900 + cd.wFrac * 1600 * aspectOf(cd.badgeId);
  assert.ok(cdBottomPx <= 900 + 1e-6, 'classic drawable inside the landscape frame');
  e += 3;

  // evaluateRun honesty for episodes: the capped family does not
  // turn the item into a skip (four badges DO draw), and an
  // episode earning only a gated family is skipped WITH the gate
  // as its reason.
  const runEp = evaluateRun([{ ratingKey: 'e1', title: 'Ep One' }], { e1: epA },
    { families: fiveFams, custom: [] }, aspectOf);
  assert.strictEqual(runEp.willApply.length, 1);
  assert.strictEqual(runEp.willApply[0].badges.length, EPISODE_BADGE_CAP);
  const runGate = evaluateRun([{ ratingKey: 'e1', title: 'Ep One' }], { e1: epA },
    { families: { ratings: { on: true, variant: 'signature', slots: ['imdb'] } }, custom: [] }, aspectOf);
  assert.strictEqual(runGate.willApply.length, 0);
  assert.strictEqual(runGate.skipped.length, 1);
  assert.match(runGate.skipped[0].reason, /episode ratings are not badged/);
  e += 5;

  // Run grouping carries the episode's full identity (level +
  // season + show) like it carries a season's.
  const grouped = groupApplyOutcomes([
    { ratingKey: 'e1', title: 'Ep One', outcome: 'applied', level: 'episode', parentTitle: 'Season 1', grandparentTitle: 'The Show' },
  ]);
  assert.strictEqual(grouped.applied[0].level, 'episode');
  assert.strictEqual(grouped.applied[0].parentTitle, 'Season 1');
  assert.strictEqual(grouped.applied[0].grandparentTitle, 'The Show');
  e += 3;

  console.log(`composite.test.cjs: episodes checks passed (${e} assertions)`);
}

// ---- groupApplyOutcomes: end-of-run grouping (v1.8) --------------------
// Pure grouping for the apply / restore-default summaries: outcomes
// bucket by kind, failures group BY REASON with dedup, counts agree.
{
  let g = 0;
  const entries = [
    { ratingKey: '1', title: 'Alpha', outcome: 'applied' },
    { ratingKey: '2', title: 'Beta', outcome: 'applied' },
    { ratingKey: '3', title: 'Gamma', outcome: 'no-overlay' },
    { ratingKey: '4', title: 'Delta', outcome: 'failed', error: 'Backup verification failed' },
    { ratingKey: '5', title: 'Epsilon', outcome: 'failed', error: 'Backup verification failed' },
    { ratingKey: '6', title: 'Zeta', outcome: 'failed', error: 'Poster upload returned HTTP 500' },
    { ratingKey: '7', title: 'Eta', outcome: 'unavailable', error: 'metadata unavailable' },
    { ratingKey: '8', title: 'Theta', outcome: 'failed' }, // no reason → Unknown error
  ];
  const r = groupApplyOutcomes(entries);
  assert.strictEqual(r.counts.total, 8); g++;
  assert.strictEqual(r.counts.applied, 2); g++;
  assert.strictEqual(r.counts.noOverlay, 1); g++;
  assert.strictEqual(r.counts.unavailable, 1); g++;
  assert.strictEqual(r.counts.failed, 4); g++;
  // Reason dedup: 4 failures collapse to 3 reason groups, first-seen order.
  assert.strictEqual(r.failedByReason.length, 3); g++;
  assert.strictEqual(r.failedByReason[0].reason, 'Backup verification failed'); g++;
  assert.deepStrictEqual(r.failedByReason[0].items.map((i) => i.title), ['Delta', 'Epsilon']); g++;
  assert.strictEqual(r.failedByReason[2].reason, 'Unknown error'); g++;
  assert.strictEqual(r.cancelledRun, false); g++;

  // Cancelled runs: the flag rides through, unattempted items count apart.
  const c = groupApplyOutcomes([
    { ratingKey: '1', title: 'Alpha', outcome: 'applied' },
    { ratingKey: '2', title: 'Beta', outcome: 'cancelled' },
    { ratingKey: '3', title: 'Gamma', outcome: 'cancelled' },
  ], { cancelled: true });
  assert.strictEqual(c.cancelledRun, true); g++;
  assert.strictEqual(c.counts.cancelled, 2); g++;
  assert.strictEqual(c.counts.failed, 0, 'cancelled is not failed'); g++;

  // Restore-default outcomes share the helper.
  const rd = groupApplyOutcomes([
    { ratingKey: '1', title: 'Alpha', outcome: 'restored' },
    { ratingKey: '2', title: 'Beta', outcome: 'already-default' },
    { ratingKey: '3', title: 'Gamma', outcome: 'no-provider' },
    { ratingKey: '4', title: 'Delta', outcome: 'failed', error: 'Plex did not switch the selected poster' },
  ]);
  assert.strictEqual(rd.counts.restored, 1); g++;
  assert.strictEqual(rd.counts.alreadyDefault, 1); g++;
  assert.strictEqual(rd.counts.noProvider, 1); g++;
  assert.strictEqual(rd.counts.failed, 1); g++;

  // Outcome-less entries fall back to the ok flag; junk input is empty.
  const legacy = groupApplyOutcomes([{ ratingKey: '1', title: 'A', ok: true }, { ratingKey: '2', title: 'B', ok: false, error: 'x' }]);
  assert.strictEqual(legacy.counts.applied, 1); g++;
  assert.strictEqual(legacy.counts.failed, 1); g++;
  assert.strictEqual(groupApplyOutcomes(null).counts.total, 0); g++;
  assert.strictEqual(groupApplyOutcomes([]).failedByReason.length, 0); g++;

  console.log(`composite.test.cjs: groupApplyOutcomes checks passed (${g} assertions)`);
}

// ---- badge library (v1.11): HDR, channels, aspect ratio, franchise,
// origin, collection + the plain-codec audio fix -------------------------
// Same honesty contract as every family: fire ONLY on positive
// evidence, hide with the missing fact named in the note.
{
  let v = 0;
  const fam = (id, settings, facts) => { v++; return evaluateFamily(id, settings, facts); };
  const fShows = (id, settings, facts, text) => {
    const r = fam(id, settings, facts);
    assert.strictEqual(r.show, true, `${id} should fire`);
    if (text !== undefined) assert.strictEqual(r.text, text, `${id} text`);
  };
  const fHides = (id, settings, facts) => assert.strictEqual(fam(id, settings, facts).show, false, `${id} should hide`);
  const aspectOf = () => 0.2857;

  // Registration: six new families, all default OFF, signature-first;
  // the collection family's classic shell is the ribbon shell.
  // v1.24: 30 + awards + rt-fresh + bitrate + in-progress = 34.
  assert.strictEqual(FAMILY_DEFS.length, 34); v++;
  for (const id of ['hdr', 'channels', 'aspect-ratio', 'franchise', 'origin', 'collection']) {
    const d = defaultFamilies()[id];
    assert.ok(familyDef(id), `${id} registered`);
    assert.ok(d && d.on === false && d.variant === 'signature', `${id} defaults off/signature`);
    v++;
  }
  assert.strictEqual(familyDef('hdr').shell, 'pill-hdr');
  assert.strictEqual(familyDef('channels').shell, 'pill-channels');
  assert.strictEqual(familyDef('aspect-ratio').shell, 'pill-aspect-ratio');
  assert.strictEqual(familyDef('franchise').shell, 'pill-franchise');
  assert.strictEqual(familyDef('origin').shell, 'pill-origin');
  assert.strictEqual(familyDef('collection').shell, 'ribbon-collection');
  assert.strictEqual(auroraShell('pill-hdr'), 'pill-hdr-aurora');
  assert.strictEqual(auroraShell('pill-channels'), 'pill-channels-aurora');
  assert.strictEqual(auroraShell('pill-aspect-ratio'), 'pill-aspect-ratio-aurora');
  assert.strictEqual(auroraShell('pill-franchise'), 'pill-franchise-aurora');
  assert.strictEqual(auroraShell('pill-origin'), 'pill-origin-aurora');
  assert.strictEqual(auroraShell('ribbon-collection'), 'ribbon-collection-aurora');
  v += 12;

  // ---- hdr: the flags, composed in DV/HDR10/HLG order, nothing else --
  fShows('hdr', {}, { dv: true }, 'DV');
  fShows('hdr', {}, { hdr10: true }, 'HDR10');
  fShows('hdr', {}, { hlg: true }, 'HLG');
  fShows('hdr', {}, { dv: true, hdr10: true }, 'DV • HDR10');
  fShows('hdr', {}, { dv: true, hdr10: true, hlg: true }, 'DV • HDR10 • HLG');
  fHides('hdr', {}, { dv: false, hdr10: false, hlg: false, hdrAny: false });
  fHides('hdr', {}, { type: 'movie' });
  assert.match(fam('hdr', {}, {}).note, /no HDR stream detected/); v++;

  // ---- channels: the best track's layout, releases' own notation ----
  fShows('channels', {}, { bestAudioChannels: 6 }, '5.1');
  fShows('channels', {}, { bestAudioChannels: 8 }, '7.1');
  fShows('channels', {}, { bestAudioChannels: 2 }, '2.0');
  fShows('channels', {}, { bestAudioChannels: 1 }, '1.0');
  fShows('channels', {}, { bestAudioChannels: 4 }, '4 ch');
  fHides('channels', {}, { bestAudioLabel: 'English (AAC Mono)' }); // a label is not a channel count
  assert.match(fam('channels', {}, { bestAudioLabel: 'English (AAC Mono)' }).note, /no channel count/); v++;
  fHides('channels', {}, {});

  // ---- aspect ratio: the tolerance table -----------------------------
  // Standards win within ±0.045 (2.35/2.39/2.40 are one release ratio);
  // anything else is stated as the decimal it measures — never
  // rounded into a standard it isn't near (1.90 is NOT 1.85).
  const aspectTable = [
    [2.35, '2.39:1'], [2.39, '2.39:1'], [2.40, '2.39:1'],
    [1.78, '16:9'], [16 / 9, '16:9'], [1.33, '4:3'], [4 / 3, '4:3'],
    [1.37, '1.37:1'], [1.66, '1.66:1'], [1.85, '1.85:1'], [2.0, '2.00:1'],
    [2.2, '2.20:1'], [2.76, '2.76:1'],
    [1.90, '1.90:1'], [1.50, '1.50:1'], [2.55, '2.55:1'], [1.25, '1.25:1'],
  ];
  for (const [input, want] of aspectTable) {
    assert.strictEqual(aspectRatioText(input), want, `aspect ${input}`);
    v++;
  }
  // Nearest standard wins when two are in tolerance (1.82 is nearer
  // 1.85 than 16:9; 1.81 is nearer 16:9 than 1.85).
  assert.strictEqual(aspectRatioText(1.82), '1.85:1'); v++;
  assert.strictEqual(aspectRatioText(1.81), '16:9'); v++;
  assert.strictEqual(aspectRatioText(0), null);
  assert.strictEqual(aspectRatioText(null), null);
  assert.strictEqual(aspectRatioText(undefined), null);
  assert.strictEqual(aspectRatioText(-1.78), null);
  v += 4;
  fShows('aspect-ratio', {}, { type: 'movie', aspectRatio: 2.39 }, '2.39:1');
  fShows('aspect-ratio', {}, { type: 'movie', aspectRatio: 1.78 }, '16:9');
  fShows('aspect-ratio', {}, { type: 'movie', aspectRatio: 1.9 }, '1.90:1');
  fHides('aspect-ratio', {}, { type: 'movie' });
  assert.match(fam('aspect-ratio', {}, { type: 'movie' }).note, /no aspect ratio/); v++;
  // Seasons never carry the fact — the gate states why even with bait.
  fHides('aspect-ratio', {}, { type: 'season', aspectRatio: 2.39 });
  assert.match(fam('aspect-ratio', {}, { type: 'season', aspectRatio: 2.39 }).note, /not aggregated/); v++;

  // ---- franchise: TMDb's collection name, verbatim, movies only -----
  fShows('franchise', {}, { type: 'movie', franchiseName: 'The Example Collection' }, 'The Example Collection');
  fHides('franchise', {}, { type: 'movie' }); // standalone / unmatched / no key
  assert.match(fam('franchise', {}, { type: 'movie' }).note, /no TMDb collection/); v++;
  fHides('franchise', {}, { type: 'show', franchiseName: 'Bait Collection' }); // shows hide even with bait
  fHides('franchise', {}, { type: 'episode', franchiseName: 'Bait Collection' });

  // ---- origin: only the parts TMDb states ---------------------------
  fShows('origin', {}, { type: 'movie', originalLanguage: 'ja', originCountries: ['JP'] }, 'JPN · JP');
  fShows('origin', {}, { type: 'show', originalLanguage: 'ko', originCountries: ['KR'] }, 'KOR · KR');
  fShows('origin', {}, { type: 'movie', originalLanguage: 'fr' }, 'FRA');      // language only
  fShows('origin', {}, { type: 'movie', originCountries: ['IT'] }, 'IT');      // country only
  fShows('origin', {}, { type: 'movie', originalLanguage: 'en', originCountries: ['US', 'GB'] }, 'ENG · US'); // first stated country
  assert.match(fam('origin', {}, { type: 'movie', originalLanguage: 'en', originCountries: ['US', 'GB'] }).note, /first of 2/); v++;
  fShows('origin', {}, { type: 'movie', originalLanguage: 'xx' }, 'XX');       // unmapped code stated as TMDb states it
  fHides('origin', {}, { type: 'movie' });
  fHides('origin', {}, { type: 'movie', originalLanguage: '', originCountries: [] });
  assert.strictEqual(originLanguageText('ja'), 'JPN');
  assert.strictEqual(originLanguageText(''), null);
  v += 2;

  // ---- collection: the server's own membership -----------------------
  fShows('collection', {}, { type: 'movie', collections: ['Halloween'] }, 'Halloween');
  fShows('collection', {}, { type: 'show', collections: ['Star Trek', 'Comfort Binges', 'Classics'] }, 'Star Trek +2');
  const multi = fam('collection', {}, { type: 'movie', collections: ['A Shelf', 'B Shelf'] });
  assert.strictEqual(multi.text, 'A Shelf +1'); v++;
  assert.match(multi.note, /A Shelf · B Shelf/); v++; // the verdict names them all
  fHides('collection', {}, { type: 'movie' });
  fHides('collection', {}, { type: 'movie', collections: [] });
  assert.match(fam('collection', {}, { type: 'movie' }).note, /not in any Plex collection/); v++;
  fHides('collection', {}, { type: 'episode', collections: ['Bait'] });
  fHides('collection', {}, { type: 'season', collections: ['Bait'] });

  // ---- episode gate: hdr/channels/aspect are per-episode truths -----
  const ep = {
    type: 'episode', dv: true, bestAudioChannels: 6, aspectRatio: 1.78,
    bestAudioLabel: 'English (AAC 5.1)', bestAudioCodec: 'aac',
  };
  fShows('hdr', {}, ep, 'DV');
  fShows('channels', {}, ep, '5.1');
  fShows('aspect-ratio', {}, ep, '16:9');
  fHides('franchise', {}, { ...ep, franchiseName: 'Bait Collection' });
  assert.match(fam('franchise', {}, { ...ep, franchiseName: 'Bait Collection' }).note, /not an episode badge/); v++;
  fHides('origin', {}, { ...ep, originalLanguage: 'ja', originCountries: ['JP'] });
  fHides('collection', {}, { ...ep, collections: ['Bait'] });

  // ---- seasons: hdr + channels judge on the aggregated facts --------
  // (the aggregation carries the flags and the best stream — the same
  // basis the audio and resolution families already judge seasons
  // on); aspect/franchise/origin/collection stay dark.
  const seasonFacts = {
    type: 'season', dv: true, hdr10: false, hlg: false,
    bestAudioChannels: 8, bestAudioLabel: 'English (TrueHD Atmos 7.1)', bestAudioCodec: 'truehd',
  };
  fShows('hdr', {}, seasonFacts, 'DV');
  fShows('channels', {}, seasonFacts, '7.1');
  fHides('aspect-ratio', {}, seasonFacts);
  fHides('franchise', {}, { ...seasonFacts, franchiseName: 'Bait Collection' });
  fHides('collection', {}, { ...seasonFacts, collections: ['Bait'] });
  // Origin has no evaluator-level season gate to assert here: it is
  // dark for seasons because the FACTS layer never carries TMDb
  // origin for a season item (the streaming construction) — that
  // guarantee is pinned in the Go facts tests instead.

  // ---- the audio fix: before/after verdict table ---------------------
  // BEFORE (through v1.10): every Go-served plain-codec case below
  // returned null — the reader only understood the legacy scan-form
  // label. AFTER: the codec field and the display title resolve.
  assert.strictEqual(audioFormatName({ bestAudioLabel: 'English (AAC Mono)' }), 'AAC'); v++;
  assert.strictEqual(audioFormatName({ bestAudioLabel: 'English (AC3 5.1)' }), 'Dolby Digital'); v++;
  assert.strictEqual(audioFormatName({ bestAudioLabel: 'Japanese (E-AC-3 5.1)' }), 'DD+'); v++;
  assert.strictEqual(audioFormatName({ bestAudioLabel: 'English (FLAC 5.1)' }), 'FLAC'); v++;
  assert.strictEqual(audioFormatName({ bestAudioLabel: 'DTS 5.1' }), 'DTS'); v++;
  assert.strictEqual(audioFormatName({ bestAudioCodec: 'aac', bestAudioLabel: 'English (AAC Mono)' }), 'AAC'); v++;
  assert.strictEqual(audioFormatName({ bestAudioCodec: 'eac3' }), 'DD+'); v++;
  assert.strictEqual(audioFormatName({ bestAudioCodec: 'opus' }), 'Opus'); v++;
  fShows('audio', {}, { bestAudioLabel: 'English (AAC Mono)', bestAudioCodec: 'aac' }, 'AAC');
  fShows('audio', { detail: 'full' }, { bestAudioLabel: 'English (AAC 5.1)', bestAudioCodec: 'aac', bestAudioChannels: 6, bestAudioLang: 'eng' }, 'AAC • 5.1 • ENG');
  // Unmapped stays unmapped at every step — and the note names the
  // raw codec instead of dressing it up.
  assert.strictEqual(audioFormatName({ bestAudioCodec: 'wmav2', bestAudioLabel: 'English (WMA Pro)' }), null); v++;
  fHides('audio', {}, { bestAudioLabel: 'English (WMA Pro)', bestAudioCodec: 'wmav2' });
  assert.match(fam('audio', {}, { bestAudioCodec: 'wmav2', bestAudioLabel: 'English (WMA Pro)' }).note, /wmav2/); v++;
  // Premium flag formats judge byte-identically to before the fix.
  assert.strictEqual(audioFormatName({ atmos: true, bestAudioLabel: 'English (AAC Mono)', bestAudioCodec: 'aac' }), 'Dolby Atmos'); v++;
  assert.strictEqual(audioFormatName({ dtsx: true, bestAudioCodec: 'dca' }), 'DTS:X'); v++;
  assert.strictEqual(audioFormatName({ dtshdma: true, bestAudioCodec: 'dca' }), 'DTS-HD MA'); v++;
  assert.strictEqual(audioFormatName({ truehd: true, bestAudioCodec: 'aac', bestAudioLabel: 'aac • 2.0 • eng' }), 'TrueHD'); v++;
  fShows('audio', {}, { atmos: true, truehd: true, bestAudioLabel: 'English (TrueHD Atmos 7.1)', bestAudioCodec: 'truehd' }, 'Dolby Atmos');
  // Legacy scan-form labels resolve exactly as they always did.
  assert.strictEqual(audioFormatName({ bestAudioLabel: 'eac3 • 5.1 • eng' }), 'DD+'); v++;
  assert.strictEqual(audioFormatName({ bestAudioLabel: 'aac • 2.0 • eng' }), 'AAC'); v++;
  assert.strictEqual(audioFormatName({ bestAudioLabel: 'wmav2 • 2.0 • eng' }), null); v++;

  // ---- per-style layout: classic shells, aurora twins, signature ----
  const movieFacts = {
    type: 'movie', dv: true, hdr10: true, bestAudioChannels: 6,
    bestAudioLabel: 'English (AAC 5.1)', bestAudioCodec: 'aac',
    aspectRatio: 2.39, franchiseName: 'The Example Collection',
    originalLanguage: 'ja', originCountries: ['JP'],
    collections: ['Halloween', 'Comfort Binges'],
  };
  const sixOn = (variant) => ({
    hdr: { on: true, variant }, channels: { on: true, variant },
    'aspect-ratio': { on: true, variant }, franchise: { on: true, variant },
    origin: { on: true, variant }, collection: { on: true, variant },
  });
  const classic = layoutFamilyDrawables(sixOn('classic'), movieFacts, aspectOf);
  assert.deepStrictEqual(classic.drawables.map((d) => d.badgeId).sort(),
    ['pill-aspect-ratio', 'pill-channels', 'pill-franchise', 'pill-hdr', 'pill-origin', 'ribbon-collection']);
  v++;
  const aurora = layoutFamilyDrawables(sixOn('aurora'), movieFacts, aspectOf);
  assert.deepStrictEqual(aurora.drawables.map((d) => d.badgeId).sort(),
    ['pill-aspect-ratio-aurora', 'pill-channels-aurora', 'pill-franchise-aurora', 'pill-hdr-aurora', 'pill-origin-aurora', 'ribbon-collection-aurora']);
  v++;
  const sig = layoutFamilyDrawables(sixOn('signature'), movieFacts, aspectOf);
  assert.strictEqual(sig.drawables.length, 6); v++;
  assert.ok(sig.drawables.every((d) => d.renderMode === 'signature')); v++;
  const sigByFam = {};
  for (const d of sig.drawables) sigByFam[d.family] = d;
  assert.strictEqual(sigByFam.hdr.microLabel, 'HDR');
  assert.strictEqual(sigByFam.hdr.label, 'DV • HDR10');
  assert.strictEqual(sigByFam.hdr.shape, 'pill');
  assert.strictEqual(sigByFam.channels.label, '5.1');
  assert.strictEqual(sigByFam['aspect-ratio'].label, '2.39:1');
  assert.strictEqual(sigByFam['aspect-ratio'].microLabel, 'ASPECT');
  assert.strictEqual(sigByFam.franchise.label, 'The Example Collection');
  assert.strictEqual(sigByFam.origin.label, 'JPN · JP');
  // The collection family is the BANNER in signature: ribbon shape,
  // ribbon height, first collection + count as its text.
  assert.strictEqual(sigByFam.collection.shape, 'ribbon');
  assert.ok(Math.abs(sigByFam.collection.hFrac - 92 / 1500) < 1e-9, 'ribbon height 92/1500');
  assert.strictEqual(sigByFam.collection.microLabel, 'COLLECTION');
  assert.strictEqual(sigByFam.collection.label, 'Halloween +1');
  v += 12;
  // Corner discipline: hdr/channels/aspect stack top-left under
  // resolution; franchise/origin bottom-left; the banner top-right.
  assert.ok(sigByFam.hdr.x === 0.02 && sigByFam.hdr.y < 0.2, 'hdr top-left');
  assert.ok(sigByFam.franchise.y > 0.8, 'franchise bottom-left');
  assert.ok(sigByFam.collection.x > 0.4 && sigByFam.collection.y < 0.2, 'collection banner top-right');
  v += 3;

  console.log(`composite.test.cjs: v1.11 badge-library checks passed (${v} assertions)`);
}

// ---- layman pack (v1.11.1): Family ribbon, saga position, plain-words
// runtime, genre isolation + banner presentation -------------------------
// Same honesty contract: fire only on the real fact, hide with the
// reason named.
{
  let lp = 0;
  const A = (cond, msg) => { lp++; assert.ok(cond, 'layman: ' + msg); };
  const F = (id, settings, facts) => evaluateFamily(id, settings || {}, facts);
  const aspectOf = () => 0.2857;

  // Registration + defaults: three new families, default off, in
  // their corners, dark without facts.
  for (const [id, corner] of [['family', 'tr'], ['saga-position', 'bl'], ['quick-watch', 'bl']]) {
    const def = familyDef(id);
    A(!!def && def.corner === corner, `${id} is defined in the ${corner} corner`);
    A(defaultFamilies()[id].on === false, `${id} defaults off`);
    const v = F(id, {}, {});
    A(v.show === false && v.note.length > 0, `${id} stays dark with no facts (note: ${v.note})`);
  }
  // Settings clamps (familySettings side of the sanitizer parity).
  A(familySettings({}, 'quick-watch').minutes === 90, 'quick-watch threshold defaults to 90');
  A(familySettings({ 'quick-watch': { minutes: 3 } }, 'quick-watch').minutes === 15, 'quick-watch threshold clamps to 15');
  A(familySettings({ 'quick-watch': { minutes: 9999 } }, 'quick-watch').minutes === 600, 'quick-watch threshold clamps to 600');
  A(familySettings({ 'quick-watch': { minutes: 'abc' } }, 'quick-watch').minutes === 90, 'non-numeric threshold falls back to 90');
  A(familySettings({}, 'genre').mode === 'first', 'genre mode defaults to first');
  A(familySettings({ genre: { mode: 'only' } }, 'genre').mode === 'only', 'genre mode only survives');
  A(familySettings({ genre: { mode: 'all' } }, 'genre').mode === 'first', 'streaming vocab does not leak into genre mode');
  A(familySettings({ genre: { presentation: 'banner' } }, 'genre').presentation === 'banner', 'genre presentation banner survives');
  A(familySettings({ genre: { presentation: 'wide' } }, 'genre').presentation === 'pill', 'bogus presentation falls back to pill');
  A(familySettings({}, 'genre').presentation === 'pill', 'genre presentation defaults to pill');
  A(familySettings({ genre: { genre: '  Horror  ' } }, 'genre').genre === 'Horror', 'isolated genre is trimmed');
  A(familySettings({ genre: { genre: 'x'.repeat(60) } }, 'genre').genre.length === 40, 'isolated genre is length-capped at 40');
  A(familySettings({ genre: { genre: 42 } }, 'genre').genre === '', 'non-string isolated genre falls back to empty');

  // FAMILY: the set edges on both media sides.
  A(F('family', {}, { type: 'movie', contentRating: 'PG' }).show === true, 'movie PG fires');
  A(F('family', {}, { type: 'movie', contentRating: 'G' }).text === 'FAMILY', 'movie G fires with the FAMILY text');
  A(F('family', {}, { type: 'movie', contentRating: 'PG-13' }).show === false, 'movie PG-13 stays dark (set edge)');
  A(F('family', {}, { type: 'movie', contentRating: 'R' }).show === false, 'movie R stays dark');
  A(F('family', {}, { type: 'movie', contentRating: 'pg' }).show === true, 'rating match tolerates the case Plex states');
  for (const r of ['TV-Y', 'TV-Y7', 'TV-G']) {
    A(F('family', {}, { type: 'show', contentRating: r }).show === true, `show ${r} fires`);
  }
  A(F('family', {}, { type: 'show', contentRating: 'TV-PG' }).show === false, 'show TV-PG stays dark (set edge)');
  A(F('family', {}, { type: 'show', contentRating: 'TV-Y7-FV' }).show === false, 'TV-Y7-FV is not TV-Y7 — stays dark');
  A(F('family', {}, { type: 'show', contentRating: 'TV-MA' }).show === false, 'show TV-MA stays dark');
  A(/PG-13/.test(F('family', {}, { type: 'movie', contentRating: 'PG-13' }).note), 'dark note names the actual rating');
  A(F('family', {}, { type: 'movie' }).show === false, 'unrated movie stays dark');
  A(/no content rating/.test(F('family', {}, { type: 'movie' }).note), 'unrated note names the absence');
  A(F('family', {}, { type: 'season', contentRating: 'TV-G' }).show === false, 'season stays dark even carrying a family rating (bait)');
  A(F('family', {}, { type: 'episode', contentRating: 'TV-G' }).show === false, 'episode stays dark even carrying a family rating (bait)');

  // SAGA POSITION: fire text, and the three distinct dark cases.
  const saga = F('saga-position', {}, { type: 'movie', sagaPosition: 2, sagaTotal: 4, franchiseName: 'The Saga' });
  A(saga.show === true, 'saga fires on position + total');
  A(saga.text === 'MOVIE 2 OF 4', `text is "MOVIE 2 OF 4" (got "${saga.text}")`);
  A(/TMDb states it, owned or not/.test(saga.note), 'note states the total is the TMDb saga, owned or not');
  A(F('saga-position', {}, { type: 'movie', sagaPosition: 1, sagaTotal: 1 }).show === false, 'single-part collection stays dark');
  A(/single film/.test(F('saga-position', {}, { type: 'movie', sagaPosition: 1, sagaTotal: 1 }).note), 'single-part note says why');
  A(F('saga-position', {}, { type: 'movie', sagaTotal: 3 }).show === false, 'not found among parts stays dark');
  A(/not found among the parts/.test(F('saga-position', {}, { type: 'movie', sagaTotal: 3 }).note), 'not-found note says why');
  A(F('saga-position', {}, { type: 'movie' }).show === false, 'standalone / no-key movie stays dark');
  A(F('saga-position', {}, { type: 'show', sagaPosition: 1, sagaTotal: 3 }).show === false, 'shows stay dark even with bait facts');

  // QUICK WATCH: strict-under edges, levels, custom threshold.
  const qw = { minutes: 90 };
  A(F('quick-watch', qw, { type: 'movie', runtimeMinutes: 89 }).text === 'UNDER 90 MIN', '89 min fires as UNDER 90 MIN');
  A(/89 min/.test(F('quick-watch', qw, { type: 'movie', runtimeMinutes: 89 }).note), 'fire note states the actual runtime');
  A(F('quick-watch', qw, { type: 'movie', runtimeMinutes: 90 }).show === false, 'runtime == threshold does NOT fire (under is strict)');
  A(F('quick-watch', qw, { type: 'movie', runtimeMinutes: 91 }).show === false, 'over-threshold stays dark');
  A(F('quick-watch', qw, { type: 'season', runtimeMinutes: 75 }).show === true, 'season fires on its summed total');
  A(/season total/.test(F('quick-watch', qw, { type: 'season', runtimeMinutes: 75 }).note), 'season note names the summed basis');
  A(F('quick-watch', qw, { type: 'show', runtimeMinutes: 45 }).show === false, 'show stays dark even with a runtime fact');
  A(F('quick-watch', qw, { type: 'episode', runtimeMinutes: 22 }).show === false, 'episode is gated out');
  A(/noise/.test(F('quick-watch', qw, { type: 'episode', runtimeMinutes: 22 }).note), 'episode gate note explains the noise rule');
  A(F('quick-watch', qw, { type: 'movie' }).show === false, 'no runtime fact stays dark');
  A(F('quick-watch', { minutes: 60 }, { type: 'movie', runtimeMinutes: 75 }).show === false, 'custom threshold 60 judges 75 dark');
  A(F('quick-watch', { minutes: 120 }, { type: 'movie', runtimeMinutes: 75 }).text === 'UNDER 120 MIN', 'text is composed from the SETTING, not the runtime');

  // GENRE first-mode regression pins (behavior unchanged by v1.11.1).
  A(F('genre', {}, { type: 'movie', genres: ['Drama', 'Comedy'] }).text === 'DRAMA', 'first mode: first genre by default');
  A(F('genre', { count: 2 }, { type: 'movie', genres: ['Drama', 'Comedy'] }).text === 'DRAMA • COMEDY', 'first mode: first two with count 2');
  A(F('genre', {}, { type: 'movie' }).show === false, 'first mode: no tags stays dark');

  // GENRE only-mode: isolation semantics.
  const only = { mode: 'only', genre: 'Horror' };
  const hit = F('genre', only, { type: 'movie', genres: ['Horror', 'Comedy'] });
  A(hit.show === true && hit.text === 'HORROR', 'multi-genre hit fires with the isolated genre');
  A(F('genre', { mode: 'only', genre: 'horror' }, { type: 'movie', genres: ['Horror', 'Comedy'] }).show === true, 'chosen-genre match is case-insensitive');
  A(F('genre', { mode: 'only', genre: 'HORROR' }, { type: 'movie', genres: ['horror'] }).text === 'HORROR', 'drawn text is the genre as Plex states it on the item, uppercased');
  const miss = F('genre', only, { type: 'movie', genres: ['Drama', 'Comedy'] });
  A(miss.show === false, 'absent genre stays dark');
  A(/Drama/.test(miss.note) && /Comedy/.test(miss.note) && /Horror/.test(miss.note), 'miss note names the chosen genre AND the actual genres');
  A(F('genre', only, { type: 'movie' }).show === false, 'no genre tags at all stays dark in only mode');
  A(/chosen genre/.test(F('genre', { mode: 'only' }, { type: 'movie', genres: ['Horror'] }).note), 'empty chosen genre stays dark and says so');

  // GENRE banner layout across styles; pill unchanged.
  const gfacts = { type: 'movie', genres: ['Horror', 'Comedy'] };
  for (const [variant, want] of [['classic', 'ribbon-genre'], ['aurora', 'ribbon-genre-aurora']]) {
    const { drawables } = layoutFamilyDrawables({ genre: { on: true, variant, presentation: 'banner' } }, gfacts, aspectOf);
    const g = drawables.find((d) => d.family === 'genre');
    A(!!g && g.badgeId === want, `${variant} banner draws the ribbon shell (${g && g.badgeId})`);
    A(!!g && Math.abs(g.wFrac - 0.54) < 1e-9, `${variant} banner draws at ribbon width 0.54 (got ${g && g.wFrac})`);
  }
  const sigBanner = layoutFamilyDrawables({ genre: { on: true, variant: 'signature', presentation: 'banner' } }, gfacts, aspectOf)
    .drawables.find((d) => d.family === 'genre');
  A(!!sigBanner && sigBanner.renderMode === 'signature' && sigBanner.shape === 'ribbon', 'signature banner takes the ribbon shape');
  const pill = layoutFamilyDrawables({ genre: { on: true, variant: 'classic', presentation: 'pill' } }, gfacts, aspectOf)
    .drawables.find((d) => d.family === 'genre');
  A(!!pill && pill.badgeId === 'pill-genre', 'pill presentation is unchanged');

  // Signature specs for the three new families.
  const spec = (id, settings, facts) => layoutFamilyDrawables(
    { [id]: { on: true, variant: 'signature', ...(settings || {}) } }, facts, aspectOf)
    .drawables.find((d) => d.family === id);
  const famD = spec('family', {}, { type: 'movie', contentRating: 'PG' });
  A(!!famD && famD.shape === 'ribbon', 'family signature spec takes the ribbon shape');
  A(!!famD && famD.microLabel === 'RATED', 'family signature micro label is RATED');
  const sagaD = spec('saga-position', {}, { type: 'movie', sagaPosition: 2, sagaTotal: 4 });
  A(!!sagaD && sagaD.shape === 'pill', 'saga signature spec is a pill');
  A(!!sagaD && sagaD.microLabel === 'SAGA', 'saga signature micro label is SAGA');
  const qwD = spec('quick-watch', {}, { type: 'movie', runtimeMinutes: 45 });
  A(!!qwD && qwD.shape === 'pill', 'quick-watch signature spec is a pill');
  A(!!qwD && qwD.microLabel === 'QUICK', 'quick-watch signature micro label is QUICK');
  const famClassic = layoutFamilyDrawables({ family: { on: true, variant: 'classic' } },
    { type: 'movie', contentRating: 'G' }, aspectOf).drawables.find((d) => d.family === 'family');
  A(!!famClassic && famClassic.badgeId === 'ribbon-family', 'family classic draws ribbon-family');

  assert.strictEqual(lp, 82, `layman pack assertion count (ran ${lp})`);
  console.log(`composite.test.cjs: layman-pack checks passed (${lp} assertions)`);
}

// ---- leaving soon (v1.12): the banner reads the app's own Leaving
// list (facts leavingSet / leavingDate / leavingDays). Same honesty
// contract: no entry → dark, past date → dark with the clearing note,
// and the window edge is inclusive in soon mode only. -------------
{
  let lv = 0;
  const A = (cond, msg) => { lv++; assert.ok(cond, 'leaving: ' + msg); };
  const F = (id, settings, facts) => evaluateFamily(id, settings || {}, facts);
  const aspectOf = () => 0.25;

  // Registration + defaults: a banner family, top-right, default off,
  // soon mode, 30-day window, ribbon shell in every style.
  const def = familyDef('leaving');
  A(!!def && def.corner === 'tr', 'leaving is defined in the tr corner');
  A(def.shell === 'ribbon-leaving', 'leaving classic shell is ribbon-leaving');
  A(auroraShell('ribbon-leaving') === 'ribbon-leaving-aurora', 'leaving aurora shell resolves');
  const dfl = defaultFamilies().leaving;
  A(dfl.on === false && dfl.mode === 'soon' && dfl.windowDays === 30 && dfl.variant === 'signature', 'leaving defaults off / soon / 30 days / signature');
  A(FAMILY_STACK_ORDER.tr.includes('leaving'), 'leaving is in the tr stack order');
  A(FAMILY_STACK_ORDER.tr.indexOf('leaving') > FAMILY_STACK_ORDER.tr.indexOf('ribbon'), 'leaving stacks under the honor ribbon');

  // Settings sanitizer parity (familySettings): mode vocab + the
  // window clamp, mirroring templates.go.
  A(familySettings({}, 'leaving').mode === 'soon', 'leaving mode defaults to soon');
  A(familySettings({ leaving: { mode: 'date' } }, 'leaving').mode === 'date', 'leaving mode date survives');
  A(familySettings({ leaving: { mode: 'all' } }, 'leaving').mode === 'soon', 'streaming vocab does not leak into leaving mode');
  A(familySettings({}, 'leaving').windowDays === 30, 'leaving window defaults to 30');
  A(familySettings({ leaving: { windowDays: 7 } }, 'leaving').windowDays === 7, 'leaving window 7 survives');
  A(familySettings({ leaving: { windowDays: 0 } }, 'leaving').windowDays === 1, 'leaving window clamps to 1');
  A(familySettings({ leaving: { windowDays: 9999 } }, 'leaving').windowDays === 365, 'leaving window clamps to 365');
  A(familySettings({ leaving: { windowDays: 'abc' } }, 'leaving').windowDays === 30, 'non-numeric window falls back to 30');

  // SOON mode: the window edge is inclusive; today fires.
  const soon = { mode: 'soon', windowDays: 30 };
  const atEdge = F('leaving', soon, { type: 'movie', leavingSet: true, leavingDate: '2026-11-22', leavingDays: 30 });
  A(atEdge.show === true && atEdge.text === 'LEAVING SOON', 'days == windowDays fires in soon mode');
  const pastEdge = F('leaving', soon, { type: 'movie', leavingSet: true, leavingDate: '2026-11-23', leavingDays: 31 });
  A(pastEdge.show === false, 'days == windowDays+1 stays dark in soon mode');
  A(/outside the 30-day soon window/.test(pastEdge.note), 'outside-window note names the window');
  const today = F('leaving', soon, { type: 'movie', leavingSet: true, leavingDate: '2026-10-23', leavingDays: 0 });
  A(today.show === true && today.text === 'LEAVING SOON', 'today (0 days) fires in soon mode');
  const showHit = F('leaving', soon, { type: 'show', leavingSet: true, leavingDate: '2026-10-30', leavingDays: 7 });
  A(showHit.show === true && showHit.text === 'LEAVING SOON', 'shows fire too');

  // DATE mode: no window — any future date fires, text zero-padded
  // and composed from the STORED date.
  const dateMode = { mode: 'date', windowDays: 30 };
  const far = F('leaving', dateMode, { type: 'movie', leavingSet: true, leavingDate: '2026-11-23', leavingDays: 31 });
  A(far.show === true, 'days == windowDays+1 DOES fire in date mode');
  A(far.text === 'LEAVING ON 11/23/2026', `date text is composed from the stored date (got "${far.text}")`);
  const padded = F('leaving', dateMode, { type: 'movie', leavingSet: true, leavingDate: '2026-03-05', leavingDays: 5 });
  A(padded.text === 'LEAVING ON 03/05/2026', `date text is zero-padded US format (got "${padded.text}")`);
  const todayDate = F('leaving', dateMode, { type: 'movie', leavingSet: true, leavingDate: '2026-10-23', leavingDays: 0 });
  A(todayDate.show === true && todayDate.text === 'LEAVING ON 10/23/2026', 'today (0 days) fires in date mode');

  // PAST date: dark in BOTH modes, and the note tells the owner to
  // clear the entry in the Leaving list.
  for (const st of [soon, dateMode]) {
    const past = F('leaving', st, { type: 'movie', leavingSet: true, leavingDate: '2026-10-20', leavingDays: -3 });
    A(past.show === false, `past date stays dark in ${st.mode} mode`);
    A(/has passed/.test(past.note) && /clear the entry in the Leaving soon list/.test(past.note), 'past-date note carries the clearing instruction');
  }

  // NO entry: dark, and the note says no date is set.
  const none = F('leaving', soon, { type: 'movie' });
  A(none.show === false && /no leaving date is set/.test(none.note), 'no entry stays dark and says so');
  A(F('leaving', soon, { type: 'movie', leavingSet: false, leavingDate: null, leavingDays: null }).show === false, 'explicit unset facts stay dark');

  // Set but unusable (date malformed / day count missing): dark,
  // never a guessed banner.
  A(F('leaving', soon, { type: 'movie', leavingSet: true, leavingDate: 'March 5', leavingDays: 5 }).show === false, 'malformed stored date stays dark');
  A(F('leaving', soon, { type: 'movie', leavingSet: true, leavingDate: '2026-03-05' }).show === false, 'missing day count stays dark');

  // Levels: seasons and episodes stay dark even with bait facts.
  A(F('leaving', soon, { type: 'season', leavingSet: true, leavingDate: '2026-10-30', leavingDays: 7 }).show === false, 'season stays dark');
  A(/movie or show/.test(F('leaving', soon, { type: 'season', leavingSet: true, leavingDate: '2026-10-30', leavingDays: 7 }).note), 'season gate note names the top-level rule');
  A(F('leaving', soon, { type: 'episode', leavingSet: true, leavingDate: '2026-10-30', leavingDays: 7 }).show === false, 'episode stays dark');
  A(/not one episode/.test(F('leaving', soon, { type: 'episode', leavingSet: true, leavingDate: '2026-10-30', leavingDays: 7 }).note), 'episode gate note fires');

  // Layout: the banner in all three styles — ribbon shell classic,
  // aurora twin, signature ribbon spec with its micro label.
  const lfacts = { type: 'movie', leavingSet: true, leavingDate: '2026-10-30', leavingDays: 7 };
  const classic = layoutFamilyDrawables({ leaving: { on: true, variant: 'classic' } }, lfacts, aspectOf)
    .drawables.find((d) => d.family === 'leaving');
  A(!!classic && classic.badgeId === 'ribbon-leaving', 'classic draws ribbon-leaving');
  A(!!classic && Math.abs(classic.wFrac - 0.54) < 1e-9, 'classic banner at ribbon width');
  const aurora = layoutFamilyDrawables({ leaving: { on: true, variant: 'aurora' } }, lfacts, aspectOf)
    .drawables.find((d) => d.family === 'leaving');
  A(!!aurora && aurora.badgeId === 'ribbon-leaving-aurora', 'aurora draws ribbon-leaving-aurora');
  const sig = layoutFamilyDrawables({ leaving: { on: true, variant: 'signature' } }, lfacts, aspectOf)
    .drawables.find((d) => d.family === 'leaving');
  A(!!sig && sig.renderMode === 'signature' && sig.shape === 'ribbon', 'signature takes the ribbon shape');
  A(!!sig && sig.microLabel === 'LEAVING', 'signature micro label is LEAVING');
  A(!!sig && sig.label === 'LEAVING SOON', 'signature label is the verdict text');

  assert.strictEqual(lv, 41, `leaving assertion count (ran ${lv})`);
  console.log(`composite.test.cjs: leaving checks passed (${lv} assertions)`);
}

// ---- pill settings (v1.15): the family set is a template setting,
// and the clamp is shared with the Go sanitizer. Default behavior
// is regression-pinned in the layman-pack section above (its calls
// pass no ratings key); this section pins the CONFIGURED behavior.
{
  let ps = 0;
  const A = (cond, msg) => { ps++; assert.ok(cond, 'pill-settings: ' + msg); };
  const F = (id, settings, facts) => evaluateFamily(id, settings || {}, facts);

  // The vocabulary the Studio chips offer, in canonical order.
  A(FAMILY_RATING_VOCAB.length === 12, 'rating vocabulary has 12 entries');
  A(FAMILY_RATING_GROUPS.movie.join(',') === 'G,PG,PG-13,R,NC-17', 'movie chip group is the movie ratings');
  A(FAMILY_RATING_GROUPS.tv.join(',') === 'TV-Y,TV-Y7,TV-Y7-FV,TV-G,TV-PG,TV-14,TV-MA', 'tv chip group is the TV ratings');
  assert.deepStrictEqual(FAMILY_RATINGS_DEFAULT, ['G', 'PG', 'TV-Y', 'TV-Y7', 'TV-G']);
  ps++;

  // familySettings: absent key → undefined (evaluator falls back
  // to the shipped set); present array → clamped; other families
  // never grow a ratings key.
  A(familySettings({}, 'family').ratings === undefined, 'no ratings key → undefined (shipped default applies)');
  A(familySettings({ family: {} }, 'family').ratings === undefined, 'entry without ratings → undefined');
  A(familySettings({}, 'genre').ratings === undefined, 'ratings exists only on the family family');
  assert.deepStrictEqual(
    familySettings({ family: { ratings: ['TV-PG', 'bogus', 'tv-g', 'TV-PG', 'G'] } }, 'family').ratings,
    ['G', 'TV-G', 'TV-PG']);
  ps++;
  A(familySettings({ family: { ratings: [] } }, 'family').ratings.length === 0, 'explicit empty array stays empty');
  assert.deepStrictEqual(familySettings({ family: { ratings: 'TV-PG' } }, 'family').ratings, undefined);
  ps++;
  A(familySettings({ family: { ratings: [42, null, ' R '] } }, 'family').ratings.join(',') === 'R', 'non-strings dropped, strings trimmed + uppercased');

  // clampFamilyRatings: the shared rule.
  assert.deepStrictEqual(clampFamilyRatings(['TV-MA', 'G', 'G']), ['G', 'TV-MA']);
  ps++;
  assert.deepStrictEqual(clampFamilyRatings('nope'), [...FAMILY_RATINGS_DEFAULT]);
  ps++;
  assert.deepStrictEqual(clampFamilyRatings([]), []);
  ps++;

  // Configured set: TV-PG fires once the owner adds it — the
  // owner's exact report. The miss note names the CONFIGURED set.
  const withPg = { ratings: ['TV-Y', 'TV-Y7', 'TV-G', 'TV-PG'] };
  A(F('family', withPg, { type: 'show', contentRating: 'TV-PG' }).show === true, 'TV-PG fires when TV-PG is in the set');
  A(F('family', withPg, { type: 'show', contentRating: 'TV-PG' }).text === 'FAMILY', 'configured fire draws FAMILY');
  const pgMiss = F('family', withPg, { type: 'show', contentRating: 'TV-14' });
  A(pgMiss.show === false && pgMiss.note === 'rated TV-14 — outside the family set (TV-Y / TV-Y7 / TV-G / TV-PG)',
    `miss note names the configured set (got "${pgMiss.note}")`);
  A(F('family', { ratings: ['PG-13'] }, { type: 'movie', contentRating: 'PG-13' }).show === true, 'a movie rating added to the set fires for movies');
  A(F('family', { ratings: ['PG'] }, { type: 'movie', contentRating: 'G' }).show === false, 'a configured set REPLACES the default (G not in [PG] stays dark)');

  // Exact match both ways: TV-Y7-FV is not TV-Y7, until it is
  // itself in the set.
  A(F('family', { ratings: ['TV-Y7'] }, { type: 'show', contentRating: 'TV-Y7-FV' }).show === false, 'TV-Y7-FV does not ride on TV-Y7 in a configured set');
  A(F('family', { ratings: ['TV-Y7-FV'] }, { type: 'show', contentRating: 'TV-Y7-FV' }).show === true, 'TV-Y7-FV fires when itself in the set');
  A(F('family', { ratings: ['TV-Y7-FV'] }, { type: 'show', contentRating: 'TV-Y7' }).show === false, 'TV-Y7 does not ride on TV-Y7-FV either');

  // Empty set: a distinct state from a miss — the family can
  // never fire and the note says the set is empty.
  const empty = F('family', { ratings: [] }, { type: 'movie', contentRating: 'G' });
  A(empty.show === false, 'empty set never fires');
  A(/family set is empty/.test(empty.note), `empty-set note says the set is empty (got "${empty.note}")`);
  A(!/outside the family set/.test(empty.note), 'empty-set note is not the miss note');
  A(!/family set is empty/.test(pgMiss.note), 'miss note is not the empty-set note');
  // An unrated title still names its own absence first.
  A(/no content rating/.test(F('family', { ratings: [] }, { type: 'movie' }).note), 'unrated + empty set still names the missing rating');

  // Default regression spot-checks (the full grid is pinned in
  // the layman-pack section): no ratings key → shipped notes,
  // byte-identical wording.
  A(F('family', {}, { type: 'show', contentRating: 'TV-PG' }).note === 'rated TV-PG — outside the family set (TV-Y / TV-Y7 / TV-G)',
    'default show miss note is the shipped wording');
  A(F('family', {}, { type: 'movie', contentRating: 'R' }).note === 'rated R — outside the family set (G / PG)',
    'default movie miss note is the shipped wording');

  console.log(`composite.test.cjs: pill-settings checks passed (${ps} assertions)`);
}

// ---- v1.24: new families + custom badges ----------------------------------
// The "true Kometa adversary" set: four genuinely new built-ins
// (decade + studio already existed and are verified, not duplicated),
// plus Tier 1 pins and Tier 2 rule badges. Every family fires ONLY
// on its fact; every miss names what was missing.
{
  let q = 0;
  const A = (cond, msg) => { q++; assert.ok(cond, msg); };
  const F = (id, settings, facts) => evaluateFamily(id, settings || {}, facts);

  // Built-in count: 30 shipped families + awards + rt-fresh +
  // bitrate + in-progress = 34. Decade + studio are pre-existing.
  A(FAMILY_DEFS.length === 34, `FAMILY_DEFS must be 34 (got ${FAMILY_DEFS.length})`);
  for (const id of ['awards', 'rt-fresh', 'bitrate', 'in-progress', 'decade', 'studio']) {
    A(!!familyDef(id), `${id} must be a registered family`);
  }

  // Awards: Oscar-only, literal reading of the OMDb line.
  A(F('awards', {}, { type: 'movie', awardsText: 'Won 2 Oscars. Another 3 wins.' }).text === 'OSCAR WINNER', 'Oscar win fires');
  A(F('awards', {}, { type: 'movie', awardsText: 'Nominated for 1 Oscar. Another 2 wins.' }).text === 'OSCAR NOMINEE', 'Oscar nomination fires');
  A(F('awards', {}, { type: 'show', awardsText: 'Won 1 Oscar.' }).show === true, 'awards fires on shows too');
  A(F('awards', {}, { type: 'movie', awardsText: 'Won 3 Primetime Emmys.' }).show === false, 'Emmy-only line must NOT fire the Oscar family');
  A(/no Oscar/.test(F('awards', {}, { type: 'movie', awardsText: 'Won 3 Primetime Emmys.' }).note), 'Emmy-only miss names the missing Oscar');
  A(F('awards', {}, { type: 'movie' }).show === false, 'no awards line stays dark');
  A(/no OMDb awards line/.test(F('awards', {}, { type: 'movie' }).note), 'no-line miss names the missing OMDb line');
  A(F('awards', {}, { type: 'season', awardsText: 'Won 1 Oscar.' }).show === false, 'seasons never fire awards');
  A(F('awards', {}, { type: 'episode' }).show === false, 'episodes never fire awards (episode gate)');

  // RT Fresh: 60% is Fresh by Rotten Tomatoes' own definition.
  A(F('rt-fresh', {}, { type: 'movie', rtRating: 60 }).text === 'FRESH', 'exactly 60% is Fresh');
  A(F('rt-fresh', {}, { type: 'movie', rtRating: 91 }).show === true, '91% is Fresh');
  A(F('rt-fresh', {}, { type: 'movie', rtRating: 59 }).show === false, '59% is not Fresh');
  A(/below Fresh/.test(F('rt-fresh', {}, { type: 'movie', rtRating: 59 }).note), 'below-Fresh miss names the threshold');
  A(F('rt-fresh', {}, { type: 'movie' }).show === false, 'no meter stays dark');
  A(F('rt-fresh', {}, { type: 'season', rtRating: 91 }).show === false, 'seasons never fire rt-fresh');
  A(F('rt-fresh', {}, { type: 'episode' }).show === false, 'episodes never fire rt-fresh');

  // Bitrate: the winning version's bitrate, stated as Mbps.
  A(F('bitrate', {}, { type: 'movie', bitrateKbps: 25000 }).text === '25 Mbps', '25000 kbps draws as 25 Mbps');
  A(F('bitrate', {}, { type: 'movie', bitrateKbps: 8500 }).text === '8.5 Mbps', 'sub-10 Mbps keeps one decimal');
  A(F('bitrate', {}, { type: 'episode', bitrateKbps: 12000 }).text === '12 Mbps', 'episodes carry their own bitrate');
  A(F('bitrate', {}, { type: 'movie' }).show === false, 'no bitrate stays dark');
  A(/no bitrate/.test(F('bitrate', {}, { type: 'movie' }).note), 'no-bitrate miss names the missing fact');
  A(F('bitrate', {}, { type: 'show', bitrateKbps: 25000 }).show === false, 'shows never fire bitrate (no single bitrate)');
  A(F('bitrate', {}, { type: 'season' }).show === false, 'seasons never fire bitrate');
  A(EPISODE_FAMILIES.includes('bitrate'), 'bitrate must be an episode family');

  // In progress: started but not finished, with a percentage where
  // duration + offset make it computable.
  A(F('in-progress', {}, { type: 'movie', viewOffsetMs: 3600000, viewCount: 0, runtimeMinutes: 120 }).text === 'CONTINUE 50%', 'half-watched movie draws CONTINUE 50%');
  A(F('in-progress', {}, { type: 'movie', viewOffsetMs: 60000, viewCount: 0 }).text === 'CONTINUE', 'no runtime still draws CONTINUE without a percent');
  A(F('in-progress', {}, { type: 'episode', viewOffsetMs: 300000, viewCount: 0, runtimeMinutes: 30 }).text === 'CONTINUE 17%', 'episodes carry their own progress');
  A(F('in-progress', {}, { type: 'movie', viewCount: 0 }).show === false, 'never started stays bare');
  A(/never started/.test(F('in-progress', {}, { type: 'movie', viewCount: 0 }).note), 'never-started miss names it');
  A(F('in-progress', {}, { type: 'movie', viewOffsetMs: 60000, viewCount: 1 }).show === false, 'a finished play voids the badge');
  A(F('in-progress', {}, { type: 'show', viewOffsetMs: 60000 }).show === false, 'shows never fire in-progress');
  A(EPISODE_FAMILIES.includes('in-progress'), 'in-progress must be an episode family');

  // Decade + studio: PRE-EXISTING families (v1.11), verified in
  // place — the v1.24 brief named them, but duplicating them would
  // have forked the registry.
  A(F('decade', {}, { type: 'movie', year: 1984 }).text === '1980s', 'decade draws 1980s from 1984');
  A(F('decade', {}, { type: 'movie' }).show === false, 'no year stays dark for decade');
  A(F('decade', {}, { type: 'season', year: 1984 }).show === false, 'seasons never fire decade');
  A(F('studio', {}, { type: 'movie', studio: 'A24' }).text === 'A24', 'studio draws the Plex name verbatim');
  A(F('studio', {}, { type: 'movie' }).show === false, 'no studio stays dark');

  // Opt-in discipline: every new family defaults OFF, and a legacy
  // template object (no new keys at all) evaluates identically —
  // no existing template's badge set changes silently.
  const fresh = defaultFamilies();
  for (const id of ['awards', 'rt-fresh', 'bitrate', 'in-progress']) {
    A(fresh[id] && fresh[id].on === false, `${id} must default OFF`);
  }
  const legacyLayout = { resolution: { on: true, variant: 'signature' } };
  const laidLegacy = layoutFamilyDrawables(legacyLayout, { type: 'movie', resolutionTier: '4K', awardsText: 'Won 2 Oscars.', rtRating: 91, bitrateKbps: 25000, viewOffsetMs: 60000, viewCount: 0 }, () => 0.5);
  A(!laidLegacy.verdicts.some((v) => ['awards', 'rt-fresh', 'bitrate', 'in-progress'].includes(v.family)),
    'a legacy layout (no new keys) must not evaluate the new families at all');
  A(laidLegacy.verdicts.some((v) => v.family === 'resolution' && v.show), 'legacy layout still fires its own families');

  // Rule evaluator: match + non-match for every shipped field, and
  // the first failing condition is named.
  const richFacts = { type: 'movie', genres: ['Horror', 'Comedy'], studio: 'A24', tags: ['Criterion', 'Staff Pick'], contentRating: 'R', unwatchedCount: 0, tmdbRating: 8.4, year: 1984 };
  A(evaluateCustomRule({ genre: 'horror' }, richFacts).show === true, 'genre contains is case-insensitive');
  A(evaluateCustomRule({ genre: 'Drama' }, richFacts).show === false, 'genre non-match stays dark');
  A(/genre does not include/.test(evaluateCustomRule({ genre: 'Drama' }, richFacts).note), 'genre miss names the condition');
  A(evaluateCustomRule({ studio: 'a24' }, richFacts).show === true, 'studio contains is case-insensitive');
  A(evaluateCustomRule({ studio: 'HBO' }, richFacts).show === false, 'studio non-match stays dark');
  A(evaluateCustomRule({ tag: 'criterion' }, richFacts).show === true, 'tag contains matches');
  A(evaluateCustomRule({ tag: 'Nope' }, richFacts).show === false, 'tag non-match stays dark');
  A(evaluateCustomRule({ contentRating: 'R' }, richFacts).show === true, 'content rating exact match fires');
  A(evaluateCustomRule({ contentRating: 'PG-13' }, richFacts).show === false, 'content rating mismatch stays dark');
  A(evaluateCustomRule({ watchStatus: 'watched' }, richFacts).show === true, 'watched matches unwatchedCount 0');
  A(evaluateCustomRule({ watchStatus: 'unwatched' }, richFacts).show === false, 'unwatched mismatches a watched title');
  A(evaluateCustomRule({ watchStatus: 'unwatched' }, { ...richFacts, unwatchedCount: 3 }).show === true, 'unwatched matches unwatchedCount > 0');
  A(evaluateCustomRule({ ratingMin: 8 }, richFacts).show === true, 'ratingMin met fires');
  A(evaluateCustomRule({ ratingMin: 9 }, richFacts).show === false, 'ratingMin unmet stays dark');
  A(/TMDb 8.4 is below 9/.test(evaluateCustomRule({ ratingMin: 9 }, richFacts).note), 'rating miss names the values');
  A(evaluateCustomRule({ ratingMin: 8 }, { ...richFacts, tmdbRating: undefined }).show === false, 'no TMDb rating cannot meet a threshold');
  A(evaluateCustomRule({ yearMin: 1980, yearMax: 1989 }, richFacts).show === true, 'year range inclusive fires');
  A(evaluateCustomRule({ yearMin: 1990 }, richFacts).show === false, 'year below range stays dark');
  A(evaluateCustomRule({ yearMax: 1980 }, richFacts).show === false, 'year above range stays dark');
  A(evaluateCustomRule({ genre: 'Horror', studio: 'A24', yearMin: 1980 }, richFacts).show === true, 'AND-combined conditions all pass');
  A(evaluateCustomRule({ genre: 'Horror', studio: 'HBO' }, richFacts).show === false, 'one failing AND condition darkens the badge');
  A(evaluateCustomRule({}, richFacts).show === true, 'an empty rule matches everything (editor requires a condition; a hand-edited empty rule must not silently fire nowhere)');

  // Sanitizer mirrors the server: bad presentation falls back,
  // empty text/name drops, unknown rule fields drop.
  const clean = sanitizeCustomBadge({ name: 'Horror shelf', text: 'HORROR', color: '#f87171', corner: 'bl', variant: 'aurora', rule: { genre: 'Horror', bogus: 1 } });
  A(clean && clean.text === 'HORROR' && clean.color === '#f87171', 'valid badge sanitizes intact');
  A(!('bogus' in clean.rule), 'unknown rule field dropped');
  A(sanitizeCustomBadge({ name: 'X', text: '' }) === null, 'empty text drops');
  A(sanitizeCustomBadge({ name: '', text: 'X' }) === null, 'empty name drops');
  const fb = sanitizeCustomBadge({ name: 'F', text: 'F', color: '#nope', corner: 'xx', variant: 'neon' });
  A(fb.color === '#e2e8f0' && fb.corner === 'bl' && fb.variant === 'signature', 'bad presentation falls back');

  // Layout integration: rule badges + pins draw through the normal
  // pipeline, on matching items only, with verdict rows.
  const horrorBadge = { id: 'custom-horror', name: 'Horror shelf', text: 'HORROR', color: '#f87171', corner: 'bl', variant: 'signature', rule: { genre: 'Horror' } };
  const laidMatch = layoutFamilyDrawables({}, richFacts, () => 0.5, { customBadges: [horrorBadge], pins: [] });
  A(laidMatch.verdicts.some((v) => v.family === 'custom:custom-horror' && v.show && v.text === 'HORROR'), 'matching rule badge gets a firing verdict');
  A(laidMatch.drawables.some((d) => d.family === 'custom' && d.label === 'HORROR'), 'matching rule badge draws');
  A(laidMatch.drawables.some((d) => d.family === 'custom' && d.accent === '#f87171'), 'custom accent rides the drawable');
  const laidMiss = layoutFamilyDrawables({}, { ...richFacts, genres: ['Drama'] }, () => 0.5, { customBadges: [horrorBadge], pins: [] });
  A(laidMiss.verdicts.some((v) => v.family === 'custom:custom-horror' && !v.show), 'non-matching rule badge gets a dark verdict');
  A(!laidMiss.drawables.some((d) => d.family === 'custom'), 'non-matching rule badge draws nothing');
  const pin = { id: 'pin-1', text: 'STAFF PICK', color: '#fbbf24', corner: 'tr', variant: 'signature' };
  const laidPin = layoutFamilyDrawables({}, richFacts, () => 0.5, { customBadges: [], pins: [pin] });
  A(laidPin.drawables.some((d) => d.family === 'custom' && d.label === 'STAFF PICK'), 'pin draws on its item');
  const laidNoPin = layoutFamilyDrawables({}, richFacts, () => 0.5, { customBadges: [], pins: [] });
  A(!laidNoPin.drawables.some((d) => d.family === 'custom'), 'no pin on an unpinned item');

  // Episode cap: customs count AFTER built-ins, never silently cut.
  const epFacts = { type: 'episode', resolutionTier: '1080p', bitrateKbps: 12000, viewOffsetMs: 60000, viewCount: 0, runtimeMinutes: 30 };
  const epLayout = { resolution: { on: true, variant: 'signature' }, bitrate: { on: true, variant: 'signature' }, 'in-progress': { on: true, variant: 'signature' } };
  const epLaid = layoutFamilyDrawables(epLayout, epFacts, () => 0.5, { customBadges: [horrorBadge], pins: [pin] });
  const epCustomVerdicts = epLaid.verdicts.filter((v) => v.custom);
  A(epCustomVerdicts.length === 2, 'both customs get verdicts on an episode');
  A(epCustomVerdicts.find((v) => v.family === 'custom:custom-horror').show === false, 'horror rule misses on an episode with no genres');
  const epPinVerdict = epCustomVerdicts.find((v) => v.family === 'pin:pin-1');
  A(epPinVerdict.show === true && !epPinVerdict.undrawn, 'pin draws as the 4th badge — within the cap, after 3 built-ins');
  // One more built-in (runtime fires on runtimeMinutes) fills the
  // cap, and the pin becomes 5th: undrawn, with the cap named.
  const epLayoutFull = { ...epLayout, runtime: { on: true, variant: 'signature' } };
  const epLaidFull = layoutFamilyDrawables(epLayoutFull, epFacts, () => 0.5, { customBadges: [], pins: [pin] });
  const fullPin = epLaidFull.verdicts.find((v) => v.family === 'pin:pin-1');
  A(fullPin.show === true && fullPin.undrawn === true, 'pin past the cap is marked undrawn, not silently cut');
  A(/episode badge cap/.test(fullPin.note || ''), 'cap verdict names the cap');
  A(!epLaidFull.drawables.some((d) => d.family === 'custom'), 'undrawn pin draws nothing');

  // evaluateRun: rule badges fire exactly on matching items across
  // a mixed set; pins fire only on their item.
  const items = [
    { ratingKey: '1', title: 'Horror One' },
    { ratingKey: '2', title: 'Drama One' },
    { ratingKey: '3', title: 'Horror Two' },
  ];
  const factsByKey = {
    1: { ...richFacts },
    2: { ...richFacts, genres: ['Drama'] },
    3: { ...richFacts },
  };
  const run = evaluateRun(items, factsByKey, { families: {}, custom: [], customBadges: [horrorBadge], pinsByKey: { 2: [pin] } }, () => 0.5);
  A(run.willApply.length === 3, 'all three items apply (rule badge on 1+3, pin on 2)');
  const badgesFor = (k) => (run.willApply.find((w) => String(w.item.ratingKey) === k) || { badges: [] }).badges;
  A(badgesFor('1').some((b) => b.family === 'custom'), 'rule badge on matching item 1');
  A(!badgesFor('2').some((b) => b.family === 'custom' && b.label === 'HORROR'), 'no rule badge on non-matching item 2');
  A(badgesFor('2').some((b) => b.label === 'STAFF PICK'), 'pin on item 2 only');
  A(!badgesFor('1').some((b) => b.label === 'STAFF PICK'), 'no pin on unpinned item 1');
  const runNone = evaluateRun(items, factsByKey, { families: {}, custom: [], customBadges: [{ ...horrorBadge, rule: { genre: 'Western' } }], pinsByKey: {} }, () => 0.5);
  A(runNone.willApply.length === 0, 'a rule badge matching nothing fires nowhere');
  A(runNone.skipped.length === 3 && /Horror shelf/.test(runNone.skipped[0].reason), 'match-nothing skip names the badge');

  console.log(`composite.test.cjs: v1.24 checks passed (${q} assertions)`);
}
