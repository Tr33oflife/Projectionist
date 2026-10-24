/* ============================================================================
 * overlays.dom.test.cjs — DOM regression harness for the Overlay Studio.
 * ----------------------------------------------------------------------------
 * WHY THIS EXISTS: the node unit tests only ever exercised composite.js's
 * pure math. overlays.js itself — init, view switching, the one-click
 * apply, the restore-default card — ran for the first time on the
 * owner's machine, and v1.8.0 shipped two bugs that only exist there:
 *   1. runApplyNow called compositeFor(), which the v1.8.0 refactor had
 *      deleted — every one-click apply item failed with
 *      "compositeFor is not defined".
 *   2. loadRestoreSections() ran exactly once, at init — which can
 *      happen before a server is connected — so the Restore card's
 *      library select stayed on "Could not load libraries" forever.
 * This harness boots the REAL index.html + bridge.js + themes.js +
 * app.js + advisor.js + composite.js + overlays.js +
 * collections-manager.js in jsdom and drives the real UI wiring
 * end to end, so both regressions fail here before they can ship again.
 *
 * Run:  node web/overlays.dom.test.cjs        (exit 0 = green)
 *
 * STUBS (minimal, and the only lies in the building):
 *  - Packages live OUTSIDE the app: jsdom from ~/local/domtest,
 *    skia-canvas from ~/local/sigproof (override with PCM_DOMTEST_DIR /
 *    PCM_SIGPROOF_DIR). The app itself gains no dependency.
 *  - window.PCM: app.js IS loaded for real (production script
 *    order), so window.PCM is the app's own namespace — $, esc,
 *    call's envelope-throwing contract, artSrc, openModal, state are
 *    the production implementations, not mirrors. (Before v1.14 a
 *    hand-mirrored prelude stood in for app.js; the Builder and
 *    Advisor flows could not be exercised until it was retired.)
 *  - window.pcm: bridge.js IS loaded for real (it defines window.pcm
 *    over fetch), then its transport methods are replaced with canned
 *    envelope stubs — bridge promises resolve {ok,...} envelopes and
 *    PCM.call() is what throws, exactly like production. The
 *    restoreSession stub signs in against a fixed machine.
 *    getSections answers ok:false for its first THREE calls (app
 *    boot + rail + restore loader at init) to reproduce
 *    init-before-connect, then succeeds.
 *  - fetch: answered ONLY for /api/health, with a version string that
 *    exists nowhere in the app — the version check proves the sidebar
 *    is filled from the server's report, never a hardcoded copy.
 *    Every other fetch rejects (bridge.js's rpc converts that to an
 *    ok:false envelope; the window.pcm stubs below replace its
 *    methods before anything calls them).
 *  - Canvas: jsdom has no 2d context, so HTMLCanvasElement.getContext
 *    is backed by a real skia-canvas — drawComposite truly renders and
 *    the apply path's toBlob produces genuine PNG bytes.
 *  - Image: `new Image()` is faked. Badge URLs resolve to the REAL PNG
 *    files under ../assets/badges; every other URL resolves to one
 *    generated poster image.
 * ========================================================================== */
'use strict';

const fs = require('fs');
const path = require('path');
const os = require('os');

const WEB_DIR = __dirname;
const GO_DIR = path.dirname(WEB_DIR);
const DOMTEST_DIR = process.env.PCM_DOMTEST_DIR || path.join(os.homedir(), 'local', 'domtest');
const SIGPROOF_DIR = process.env.PCM_SIGPROOF_DIR || path.join(os.homedir(), 'local', 'sigproof');

let JSDOM, skia;
try {
  ({ JSDOM } = require(path.join(DOMTEST_DIR, 'node_modules', 'jsdom')));
  skia = require(path.join(SIGPROOF_DIR, 'node_modules', 'skia-canvas'));
} catch (e) {
  console.error('overlays.dom.test.cjs: harness deps missing (jsdom in ~/local/domtest, skia-canvas in ~/local/sigproof):', e.message);
  process.exit(2);
}

const readWeb = (f) => fs.readFileSync(path.join(WEB_DIR, f), 'utf8');
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---- canned data ------------------------------------------------------------
const SECTION = { key: '1', title: 'Movies', type: 'movie' };
const ITEM = { ratingKey: '42', title: 'Harness Movie', thumb: '/library/media/42/thumb/1699999999', type: 'movie', year: 2021 };
// v1.9: a show + one of its seasons for the Studio drill-down. The
// season's facts are what the facts layer would serve for it: judged
// on its own episodes (langsFromEpisodes, runtime = episode total),
// its own IMDb rating, and NONE of the show-identity fields.
const SHOW = { ratingKey: '77', title: 'Harness Show', thumb: '/library/media/77/thumb/1699999999', type: 'show', year: 2023 };
const SEASON = { ratingKey: '78', title: 'Season 1', thumb: '/library/media/78/thumb/1699999999', type: 'season', index: 1, leafCount: 3, parentTitle: 'Harness Show', parentRatingKey: '77' };
const SEASON_FACTS = {
  ratingKey: '78', title: 'Season 1', type: 'season',
  resolutionTier: '1080p', width: 1920, dv: false, hdr10: true, hlg: false, hdrAny: true,
  atmos: true, dtsx: false, dtshdma: false, truehd: false,
  bestAudioLabel: 'English (TrueHD Atmos 7.1)', bestAudioChannels: 8, bestAudioLang: 'eng',
  tmdbRating: null, imdbRating: 8.4, rtRating: null, rtAudience: null, metascore: null,
  contentRating: 'TV-MA', editionTitle: '', tvStatus: null,
  audioLangs: ['eng', 'jpn'], subtitleLangs: ['eng'], hasSubs: true,
  subtitleCount: 2, subtitleForced: true, subtitleSDH: false,
  dubClass: 'dual', audioTracks: 2, mediaCount: 0,
  sourceTag: null, videoCodec: 'hevc', studio: null, genres: [],
  awardsText: '', year: 2019, runtimeMinutes: 170,
  episodeCount: 3, episodeTotal: 3, langsFromEpisodes: true, langsPartial: false, langsError: '',
  addedAtDays: 5, unwatchedCount: 2,
  streamingByRegion: null,
};
// v1.10: one episode of the season above, for the second drill
// level. Its facts are the episode's OWN (1080p/hevc streams, dual
// audio, 42-minute runtime, unwatched) — plus bait: an IMDb rating
// the episode gate must keep dark, and none of the identity fields
// (the facts layer suppresses them for episodes).
const EPISODE = { ratingKey: '79', title: 'The Test Episode', thumb: '/library/media/79/thumb/1699999999', type: 'episode', index: 1, duration: 2520000 };
const EPISODE_FACTS = {
  ratingKey: '79', title: 'The Test Episode', type: 'episode',
  resolutionTier: '1080p', width: 1920, dv: false, hdr10: true, hlg: false, hdrAny: true,
  atmos: true, dtsx: false, dtshdma: false, truehd: true,
  bestAudioLabel: 'English (TrueHD Atmos 7.1)', bestAudioChannels: 8, bestAudioLang: 'eng',
  tmdbRating: null, imdbRating: 9.1, rtRating: null, rtAudience: null, metascore: null,
  contentRating: 'TV-MA', editionTitle: '', tvStatus: null,
  audioLangs: ['eng', 'jpn'], subtitleLangs: ['eng'], hasSubs: true,
  subtitleCount: 1, subtitleForced: true, subtitleSDH: false,
  dubClass: 'dual', audioTracks: 2, mediaCount: 1,
  sourceTag: null, videoCodec: 'hevc', studio: null, genres: [],
  awardsText: '', year: 2024, runtimeMinutes: 42,
  addedAtDays: 2, unwatchedCount: 1,
  streamingByRegion: null,
};
// Facts that earn the default layout's resolution badge (and several
// more) — mirrors the fixtures in composite.test.cjs.
const FACTS = {
  ratingKey: '42', title: 'Harness Movie', type: 'movie',
  resolutionTier: '4K', width: 3840, dv: true, hdr10: true, hlg: false, hdrAny: true,
  atmos: true, dtsx: false, dtshdma: false, truehd: false,
  bestAudioLabel: 'TrueHD • 7.1', bestAudioChannels: 8, bestAudioLang: 'eng',
  tmdbRating: 8.7, imdbRating: 8.9, rtRating: 91, rtAudience: 88, metascore: 80,
  contentRating: 'PG-13', editionTitle: '', tvStatus: null,
  audioLangs: ['eng'], subtitleLangs: ['eng'], hasSubs: true,
  subtitleCount: 1, subtitleForced: false, subtitleSDH: false,
  dubClass: 'unknown', audioTracks: 1, mediaCount: 1,
  sourceTag: 'REMUX', videoCodec: 'hevc', studio: 'A24', genres: ['Science Fiction'],
  awardsText: '', year: 2021, runtimeMinutes: 121,
  addedAtDays: 400, unwatchedCount: 1,
  streamingByRegion: { US: ['Netflix'] },
};
const BADGES = JSON.parse(fs.readFileSync(path.join(GO_DIR, 'assets', 'badges', 'badges.json'), 'utf8')).badges;

// ---- skia-backed poster + image resolution ----------------------------------
async function makePosterImage() {
  const cv = new skia.Canvas(500, 750);
  const ctx = cv.getContext('2d');
  const g = ctx.createLinearGradient(0, 0, 0, 750);
  g.addColorStop(0, '#2b4a7a'); g.addColorStop(1, '#0d1526');
  ctx.fillStyle = g; ctx.fillRect(0, 0, 500, 750);
  ctx.fillStyle = '#e8b34b'; ctx.fillRect(60, 90, 380, 240);
  ctx.fillStyle = '#f5f2ff'; ctx.font = '700 44px sans-serif'; ctx.fillText('HARNESS', 60, 430);
  return skia.loadImage(await cv.toBuffer('png'));
}

// Per-boot options the driver sets before constructing a JSDOM
// (the simulated-reload boot seeds a saved theme through these).
let realmOpts = {};

// ---- realm setup (runs in jsdom's beforeParse, before any page script) ------
function setupRealm(window, posterImg) {
  window.__PCM_TOKEN__ = 'domtest-token';
  const H = (window.__HARNESS__ = {
    toasts: [], capturedJobs: [], applyCalls: [], lastRecord: null, sectionsCalls: 0,
    badges: BADGES, facts: FACTS, item: ITEM, section: SECTION,
    show: SHOW, season: SEASON, seasonFacts: SEASON_FACTS,
    episode: EPISODE, episodeFacts: EPISODE_FACTS,
    stubVersion: '9.9.9-domtest',
    // v1.19 theme store stand-in (settings-store semantics: the
    // name + the saved custom palette, nothing else).
    themeStore: realmOpts.themeStore
      ? JSON.parse(JSON.stringify(realmOpts.themeStore))
      : { name: 'projectionist', custom: null },
  });
  // Seed the pre-paint localStorage mirror when the driver asks —
  // the inline script in index.html reads it during parsing.
  if (realmOpts.themeCache) {
    try { window.localStorage.setItem('pcm-theme-v1', JSON.stringify(realmOpts.themeCache)); } catch { /* jsdom storage unavailable */ }
  }
  // jsdom lacks blob URLs; the theme Export button needs them to exist.
  if (!window.URL.createObjectURL) {
    window.URL.createObjectURL = () => 'blob:domtest';
    window.URL.revokeObjectURL = () => {};
  }

  window.fetch = (url) => {
    if (String(url).indexOf('/api/health') === 0) {
      return Promise.resolve({
        ok: true, status: 200,
        json: () => Promise.resolve({ ok: true, app: 'plex-collection-manager', version: H.stubVersion }),
      });
    }
    return Promise.reject(new Error('DOM harness: no network'));
  };

  // Canvas → skia. One skia canvas per element, recreated if the
  // element's size changes; drawImage unwraps FakeImage instances.
  const proto = window.HTMLCanvasElement.prototype;
  function skiaFor(el) {
    const w = el.width || 300, h = el.height || 150;
    let rec = el.__skiaRec;
    if (!rec || rec.w !== w || rec.h !== h) {
      const canvas = new skia.Canvas(w, h);
      const ctx = canvas.getContext('2d');
      const rawDrawImage = ctx.drawImage.bind(ctx);
      ctx.drawImage = (img, ...rest) => rawDrawImage(img && img._skia ? img._skia : img, ...rest);
      rec = el.__skiaRec = { canvas, ctx, w, h };
    }
    return rec;
  }
  proto.getContext = function (kind) { return kind === '2d' ? skiaFor(this).ctx : null; };
  proto.toBlob = function (cb, type) {
    skiaFor(this).canvas.toBuffer('png').then((buf) => {
      const u8 = new window.Uint8Array(buf.length);
      u8.set(buf);
      cb(new window.Blob([u8], { type: type || 'image/png' }));
    }, () => cb(null));
  };

  // Image → fake that resolves with real skia images (badge files from
  // disk; anything else is the generated poster).
  const imageCache = new Map();
  window.Image = class FakeImage {
    constructor() {
      this._skia = null; this._src = '';
      this.naturalWidth = 0; this.naturalHeight = 0; this.width = 0; this.height = 0;
      this.crossOrigin = null; this.onload = null; this.onerror = null;
    }
    set src(u) {
      this._src = String(u || '');
      const key = this._src;
      (async () => {
        let img = imageCache.get(key);
        if (!img) {
          const m = key.match(/\/badges\/[^/]+\/([^?#]+)/);
          if (m) {
            const file = path.join(GO_DIR, 'assets', 'badges', decodeURIComponent(m[1]));
            if (fs.existsSync(file)) img = await skia.loadImage(file);
          }
          if (!img) img = posterImg;
          imageCache.set(key, img);
        }
        this._skia = img;
        this.naturalWidth = img.width; this.naturalHeight = img.height;
        this.width = img.width; this.height = img.height;
        if (this.onload) this.onload();
      })().catch((e) => { if (this.onerror) this.onerror(e); });
    }
    get src() { return this._src; }
  };

  // jsdom gap: scrollIntoView is not implemented.
  window.Element.prototype.scrollIntoView = function () {};

  // Page scripts, concatenated into ONE eval in PRODUCTION script
  // order (bridge → themes → app → collection-art → advisor →
  // composite → overlays → manager),
  // so top-level consts share the global lexical scope exactly like
  // sequential <script> tags in the browser. Since v1.14 app.js and
  // advisor.js load FOR REAL (the Builder/advisor flows are under
  // test); before v1.14 a hand-mirrored window.PCM prelude stood in
  // for app.js, which made the Builder untestable here.
  // window.pcm transport stubs — installed AFTER bridge.js defines the
  // real object; canned envelopes, phased apply capture.
  const stubs = `
    (() => {
      const H = window.__HARNESS__;
      const env = (o) => Promise.resolve({ ok: true, ...o });
      const pcm = window.pcm;
      pcm.getSections = () => {
        H.sectionsCalls++;
        // First THREE calls fail (app.js boot's loadSections + the
        // rail + the restore loader, all at init) to reproduce
        // init-before-connect; later calls succeed.
        if (H.sectionsCalls <= 3) return Promise.resolve({ ok: false, error: 'No server connected', hint: '' });
        return env({ sections: [H.section] });
      };
      pcm.getCollections = () => env({ collections: [] });
      pcm.previewFilter = () => env({ items: [H.item, H.show] });
      pcm.searchLibrary = () => env({ items: [] });
      pcm.getItemChildren = (rk) => env({ items: String(rk) === String(H.show.ratingKey) ? [H.season]
        : String(rk) === String(H.season.ratingKey) ? [H.episode] : [] });
      pcm.getSectionItems = () => env({ items: [H.item] });
      // v1.17 wizard grid fake: a 150-movie library (three "Zebra"
      // titles spread across it) served in honest pages — start/size
      // slice, exact total, title search server-side, and the rules
      // filter (genre=Action) resolving to the Advisor scan items.
      H.library150 = [];
      for (let i = 1; i <= 150; i++) {
        const title = i === 1 ? 'Zebra Opening' : i === 76 ? 'Zebra Middle' : i === 150 ? 'Zebra Finale'
          : 'Library Movie ' + String(i).padStart(3, '0');
        H.library150.push({ ratingKey: String(1000 + i), title,
          thumb: '/library/metadata/' + (1000 + i) + '/thumb/1', year: 1950 + (i % 70), type: 'movie', sectionKey: '1' });
      }
      H.pageCalls = [];
      pcm.getSectionItemsPage = (sectionKey, opts = {}) => {
        H.pageCalls.push({ sectionKey, ...opts });
        let all = H.library150;
        if (String(opts.filterQuery || '').includes('genre=Action')) all = H.advScanItems;
        if (opts.title) { const q = String(opts.title).toLowerCase(); all = all.filter((x) => x.title.toLowerCase().includes(q)); }
        const start = opts.start || 0, size = opts.size || 50;
        const items = all.slice(start, start + size);
        return env({ items, total: all.length, totalKnown: true, start, size, hasMore: start + items.length < all.length });
      };
      pcm.getCollectionItems = () => env({ items: [] });
      pcm.getSectionTagTypes = () => env({ tagTypes: [] });
      pcm.getSectionTags = () => env({ tags: [] });
      pcm.overlayBadgePacks = () => env({ bundle: { badges: H.badges }, custom: [], customDir: '' });
      pcm.overlayItemFacts = (rk) => env({ facts: String(rk) === String(H.season.ratingKey) ? H.seasonFacts
        : String(rk) === String(H.episode.ratingKey) ? H.episodeFacts : H.facts });
      pcm.advisorGetSettings = () => env({ settings: {} });
      // Theme store (v1.19): settings-store semantics over
      // H.themeStore — themeSet stores the name and, when a palette
      // rides along, the palette (the Go side validates completeness;
      // the renderer's own validator gates what it sends).
      // Collection art (collection-art.js loads for real since
      // v1.19): the picker opens on an empty suggestion list.
      pcm.artSuggestions = () => env({ items: [] });
      pcm.artPreview = () => env({ art: { url: '', label: 'stub', source: 'ThePosterDB', width: 100, height: 150, preview: '', assetId: '' } });
      pcm.artOpenTPDb = () => env({ url: 'https://theposterdb.com/' });
      pcm.artRemember = () => env({});
      // Poster generator (v1.20): preview calls are captured; the
      // stub mirrors the server's honesty rules (a fixed grid with
      // too few selected members fails with the shortfall message).
      H.genCalls = [];
      pcm.posterGenPreview = (payload) => {
        H.genCalls.push(JSON.parse(JSON.stringify(payload)));
        const keys = payload.memberKeys || [];
        if (payload.template === 'mosaic' && (payload.grid === '2x2' || payload.grid === '3x3')) {
          const need = payload.grid === '2x2' ? 4 : 9;
          if (keys.length < need) return { ok: false, error: 'A ' + payload.grid.replace('x', ' × ') + ' mosaic needs ' + need + ' posters, but only ' + keys.length + ' usable member posters were selected.', hint: '' };
        }
        return env({ art: { assetId: 'gen-asset-1', url: '', source: 'Generated poster', label: 'Generated poster — Harness', width: 1000, height: 1500, preview: 'data:image/png;base64,AAAA' },
          stats: { usedMembers: keys.length, skippedPosters: 0, selectedMembers: keys.length, memberTotal: keys.length, capApplied: false, capLimit: 169, notes: [] } });
      };
      H.genAICalls = [];
      pcm.posterGenAIImage = (prompt, model) => { H.genAICalls.push({ prompt, model }); return env({ art: { assetId: 'gen-ai-asset', url: '', source: 'AI-generated background', label: 'AI-generated background', width: 1024, height: 1024, preview: 'data:image/png;base64,BBBB' } }); };
      H.setPosterCalls = [];
      pcm.setPoster = (rk, url, assetId) => { H.setPosterCalls.push({ rk, url, assetId }); return env({}); };
      pcm.themeGet = () => env({ name: H.themeStore.name, custom: H.themeStore.custom });
      pcm.themeSet = (obj) => {
        if (obj && obj.name) H.themeStore.name = obj.name;
        if (obj && obj.custom) H.themeStore.custom = JSON.parse(JSON.stringify(obj.custom));
        return env({});
      };
      // Session + Builder/Advisor (v1.14): app.js boots for real, so
      // the restore stub signs in against the fixed machine; the
      // Advisor gets a canned scan + suggestion cards (one genre card
      // CARRYING Builder rules, one franchise card without); the
      // Builder's create is captured, and list import gets a fixture
      // preview (2 matched in order, 1 unmatched with reason, 1
      // duplicate — the same buckets the Go matcher test pins).
      pcm.restoreSession = () => env({ signedIn: true,
        user: { name: 'Harness' },
        servers: [{ machine: 'domtest-machine', name: 'Harness Server', owned: true, quality: 'local', qualityLabel: 'Local' }],
        selectedMachine: 'domtest-machine', storeBackend: 'file' });
      // Meta store (v1.20): settings-store semantics for the AI
      // fields — metaStatus reports the stored values (or the same
      // defaults the Go handler falls back to), and the field saves
      // are deliberately SLOWER (120ms) than key saves, so the
      // render race behind the v1.19 AI-fields bug is exercised for
      // real by the pin near the end of the run.
      H.aiStore = { base: '', model: '', key: false };
      pcm.metaStatus = () => env({ tmdb: false, traktClientId: false, traktClientSecret: false,
        traktConnected: false, omdb: false, fanart: false, ai: H.aiStore.key,
        aiBaseUrl: H.aiStore.base || 'https://api.openai.com/v1',
        aiModel: H.aiStore.model || 'gpt-4o-mini', storeBackend: 'file' });
      pcm.metaSetAiBaseUrl = (url) => new Promise((res) => setTimeout(() => { H.aiStore.base = url; res(env({})); }, 120));
      pcm.metaSetAiModel = (model) => new Promise((res) => setTimeout(() => { H.aiStore.model = model; res(env({})); }, 120));
      pcm.metaSetKey = (which, value) => { if (which === 'aiKey') H.aiStore.key = !!value; return env({ set: !!value }); };
      // Backup & restore + donate (v1.22): metaStatus carries the
      // donate URL; the backup stubs mirror the server's contract
      // (keys only with the opt-in; import counts from the document).
      H.donateUsername = '';
      H.donateEnabled = true;
      pcm.metaSetDonate = (username, enabled) => {
        const v = String(username || '').trim();
        if (v !== '' && !/^[A-Za-z0-9_-]{1,64}$/.test(v)) return { ok: false, error: 'A Ko-fi username uses only letters, numbers, hyphens and underscores.', hint: '' };
        H.donateUsername = v;
        if (enabled !== undefined) H.donateEnabled = !!enabled;
        return env({ username: v, enabled: H.donateEnabled });
      };
      const _metaStatusOrig = pcm.metaStatus;
      pcm.metaStatus = () => { const r = _metaStatusOrig(); return r.then((e) => ({ ...e, donateUsername: H.donateUsername, donateEnabled: H.donateEnabled })); };
      // Ko-fi widget stub: records draw calls instead of loading Ko-fi.
      H.kofiDraw = [];
      window.kofiWidgetOverlay = { draw: (username, config) => { H.kofiDraw.push({ username, config }); } };
      H.exportCalls = [];
      pcm.settingsExportBackup = (includeKeys) => {
        H.exportCalls.push(!!includeKeys);
        return env({ backup: { format: 'projectionist-settings-backup', formatVersion: 1, appVersion: 'harness',
          settings: { aiBaseUrl: 'https://example.invalid/v1', uiTheme: 'midnight', donateUsername: H.donateUsername },
          advisorSettings: { genreThreshold: 12 }, templates: { 'Seed Template': { name: 'Seed Template', placements: [] } },
          leaving: { 'domtest-machine': { '10': { title: 'Alpha', date: '2027-01-15' } } },
          ...(includeKeys ? { keys: { tmdb: 'stub-key' } } : {}) } });
      };
      H.importCalls = [];
      pcm.settingsImportBackup = (doc, confirmKeys) => {
        H.importCalls.push({ doc, confirmKeys: !!confirmKeys });
        const portable = doc && doc.settings ? Object.keys(doc.settings).filter((k) => k !== 'defaultMachine' && k !== 'clientId').length : 0;
        const templates = doc && doc.templates ? Object.keys(doc.templates).length : 0;
        let leaving = 0;
        if (doc && doc.leaving) for (const m of Object.values(doc.leaving)) leaving += Object.keys(m).length;
        const keyCount = doc && doc.keys ? Object.keys(doc.keys).length : 0;
        return env({ settingsApplied: portable, advisorApplied: !!(doc && doc.advisorSettings),
          templatesImported: templates, leavingDates: leaving,
          keysImported: confirmKeys ? keyCount : 0, keysSkipped: keyCount > 0 && !confirmKeys, dropped: [] });
      };
      pcm.aiTestConnection = () => env({ result: { ok: true, ms: 5, model: H.aiStore.model || 'gpt-4o-mini', baseUrl: H.aiStore.base || 'https://api.openai.com/v1', reply: 'ok' } });
      H.createCalls = [];
      pcm.createCollection = (payload) => { H.createCalls.push(payload); return env({ collection: { ratingKey: '900', title: payload.title } }); };
      H.advScanItems = [
        { ratingKey: '42', title: 'Harness Movie', thumb: '/library/media/42/thumb/1699999999', year: 2021, type: 'movie', sectionKey: '1' },
        { ratingKey: '43', title: 'Second Movie', thumb: '/library/media/43/thumb/1699999999', year: 2022, type: 'movie', sectionKey: '1' },
      ];
      pcm.advisorGetScan = () => env({ scan: { items: H.advScanItems,
        sections: [{ key: '1', title: 'Movies', type: 'movie' }],
        scannedAt: Date.now(), itemCount: 2 } });
      H.advCards = [
        { id: 'genre:1:Action', kind: 'genre', title: 'Action (Movies)', subtitle: '2 titles',
          memberRatingKeys: ['42', '43'], reason: 'Fixture genre suggestion.', confidence: 'deterministic',
          rules: [{ field: 'genre', op: 'is', value: 'Action' }] },
        { id: 'franchise:1:Harness', kind: 'franchise', title: 'Harness Saga', subtitle: '2 titles',
          memberRatingKeys: ['42', '43'], reason: 'Fixture franchise suggestion.', confidence: 'deterministic' },
      ];
      // Production GenerateAll filters dismissed ids server-side, so
      // a dismissed card never returns on a reload — the stub models
      // exactly that (an unconditional list resurrected one in v1.18).
      pcm.advisorSuggestions = () => env({ cards: H.advCards.filter((c) => !(H.dismissedIds || []).includes(c.id)), topLists: { movies: [], tv: [] } });
      // v1.18: drafts/dismiss get in-memory stand-ins so the pop-out
      // editor's Save-as-draft and Dismiss paths run for real.
      H.drafts = [];
      pcm.advisorGetDrafts = () => env({ drafts: H.drafts });
      pcm.advisorSaveDraft = (d) => { H.drafts.push({ id: 'draft-' + (H.drafts.length + 1), savedAt: Date.now(), ...d }); return env({}); };
      H.dismissedIds = [];
      pcm.advisorDismiss = (id) => { H.dismissedIds.push(id); return env({}); };
      // AI collection suggestions (v1.21): one scripted AI card for
      // section 1 — the model suggested 3 keys, 2 verified (the
      // third was never in the library). Dismissed ids are filtered
      // exactly like production (the server never returns a
      // dismissed card), so a re-run cannot resurrect one.
      H.aiSuggestCalls = [];
      H.aiSuggestCards = [
        { id: 'ai:1:spooky-season-picks', kind: 'ai', title: 'Spooky Season Picks',
          subtitle: 'Comfort horror for October nights.',
          memberRatingKeys: ['42', '43'],
          reason: 'Both picks share a cozy autumn mood.',
          confidence: 'heuristic', aiModel: 'cf-chat-model-x', aiSuggested: 3, aiVerified: 2 },
      ];
      pcm.aiSuggestCollections = (sectionKey) => { H.aiSuggestCalls.push(sectionKey);
        return env({ cards: H.aiSuggestCards.filter((c) => !(H.dismissedIds || []).includes(c.id)),
          calls: 2, model: 'cf-chat-model-x', sawItems: 2, totalItems: 2, sampled: false, suppressed: 1 }); };
      pcm.importPreviewList = () => env({ source: 'tmdb', listTitle: 'Harness List', authUsed: false,
        itemCount: 4, totalEntries: 4, matchedCount: 2, unmatchedCount: 1, duplicateCount: 1, titleCollision: false,
        entries: [
          { title: 'Harness Movie', year: 2021, mediaType: 'movie', status: 'matched', via: 'tmdb', item: { ratingKey: '42', title: 'Harness Movie', year: 2021, thumb: '/library/media/42/thumb/1699999999', type: 'movie' } },
          { title: 'Second Movie', year: 2022, mediaType: 'movie', status: 'matched', via: 'title+year', item: { ratingKey: '43', title: 'Second Movie', year: 2022, thumb: '/library/media/43/thumb/1699999999', type: 'movie' } },
          { title: 'Ghost Title', year: 2019, mediaType: 'movie', status: 'unmatched', reason: 'not in library', item: null },
          { title: 'Harness Movie', year: 2021, mediaType: 'movie', status: 'duplicate', reason: 'same title appears earlier in the list — imported once', item: null },
        ],
        members: [
          { ratingKey: '42', title: 'Harness Movie', year: 2021, thumb: '/library/media/42/thumb/1699999999', type: 'movie' },
          { ratingKey: '43', title: 'Second Movie', year: 2022, thumb: '/library/media/43/thumb/1699999999', type: 'movie' },
        ] });
      pcm.overlayHistory = () => env({ runs: [] });
      pcm.overlayReapplyCheck = () => env({ items: [] });
      pcm.overlayTemplatesList = () => env({ templates: [] });
      pcm.overlayClassifyPoster = () => env({ classification: { status: 'already-default' } });
      pcm.overlayRestoreDefaultPoster = () => env({ outcome: 'restored' });
      pcm.overlayRestore = () => env({ okCount: 0, results: [] });
      // Restore backed-up posters (v1.23): one row per level, plus
      // one row whose backup image is gone and one row for an item
      // gone from the library — both must render disabled.
      H.backupRows = [
        { ratingKey: '42', title: 'Harness Movie', level: 'movie', backedUpAt: '2026-10-23T10:00:00.000Z', found: true, imageExists: true },
        { ratingKey: '77', title: 'Harness Show', level: 'show', backedUpAt: '2026-10-23T09:00:00.000Z', found: true, imageExists: true },
        { ratingKey: '78', title: 'Season 1', level: 'season', parentTitle: 'Harness Show', backedUpAt: '2026-10-23T08:00:00.000Z', found: true, imageExists: true },
        { ratingKey: '79', title: 'The Test Episode', level: 'episode', parentTitle: 'Season 1', grandparentTitle: 'Harness Show', backedUpAt: '2026-10-23T07:00:00.000Z', found: true, imageExists: true },
        { ratingKey: '80', title: 'Ghost Season', level: 'season', parentTitle: 'Gone Show', backedUpAt: '2026-10-23T06:00:00.000Z', found: true, imageExists: false },
        { ratingKey: '81', title: 'Deleted Movie', level: 'movie', backedUpAt: '2026-10-23T05:00:00.000Z', found: false, imageExists: true },
      ];
      H.restoreBackupCalls = [];
      pcm.overlayListBackups = () => env({ backups: H.backupRows });
      pcm.overlayRestoreBackups = (items) => {
        H.restoreBackupCalls.push(items || []);
        return env({ outcomes: (items || []).map((it) => ({ ratingKey: String(it.ratingKey), title: it.title, ok: true, outcome: 'restored' })), record: { runId: 'run-rb-harness' } });
      };
      pcm.overlayFanartLogos = () => env({ logos: [], configured: false });
      pcm.aiProposeRibbon = () => env({ proposal: null });
      // Leaving list (v1.12): an in-memory stand-in for the app's
      // per-server store, seeded with one EXPIRED entry so the
      // manager's expired section renders on first load. Day counts
      // use the same civil-date math as the server.
      const lvIso = (n) => { const t = new Date(); t.setDate(t.getDate() + n);
        return [t.getFullYear(), String(t.getMonth() + 1).padStart(2, '0'), String(t.getDate()).padStart(2, '0')].join('-'); };
      const lvDays = (iso) => { const p = String(iso).split('-').map(Number); const now = new Date();
        return Math.round((Date.UTC(p[0], p[1] - 1, p[2]) - Date.UTC(now.getFullYear(), now.getMonth(), now.getDate())) / 86400000); };
      H.leavingDb = new Map([['599', { title: 'Expired Flick', date: lvIso(-5) }]]);
      const lvEntries = () => [...H.leavingDb.entries()]
        .map(([ratingKey, e]) => ({ ratingKey, title: e.title, date: e.date, days: lvDays(e.date), expired: lvDays(e.date) < 0 }))
        .sort((a, b) => (a.date < b.date ? -1 : a.date > b.date ? 1 : a.title.localeCompare(b.title)));
      // Collections manager (v1.13): health fixtures mirror the
      // server payload shape (collection fields + computed flags).
      // rk 201 clean; 202 every flag; 203/204 a duplicate-title pair.
      const mgrColl = (rk, title, extra) => ({ ratingKey: rk, title, childCount: 0, smart: false,
        thumb: '', art: '', summary: '', titleSort: title, addedAt: 0, updatedAt: 0,
        promotedToOwnHome: false, promotedToSharedHome: false, promotedToRecommended: false,
        hasPoster: false, hasBackdrop: false, visibilityKnown: false, flags: [], ...extra });
      H.managerCollections = [
        mgrColl('201', 'Clean Shelf', { childCount: 4, thumb: '/library/metadata/201/thumb/1', art: '/library/metadata/201/art/1', summary: 'Written.', hasPoster: true, hasBackdrop: true }),
        mgrColl('202', 'Bare Shelf', { childCount: 0, thumb: '/library/collections/202/composite/1', flags: ['EMPTY', 'NO POSTER', 'NO BACKDROP', 'NO SUMMARY'] }),
        mgrColl('203', 'Twin Shelf', { childCount: 2, thumb: '/library/metadata/203/thumb/1', summary: 'x', hasPoster: true, flags: ['NO BACKDROP', 'DUPLICATE TITLE'] }),
        mgrColl('204', 'Twin Shelf', { childCount: 1, thumb: '/library/metadata/204/thumb/1', art: '/library/metadata/204/art/1', summary: 'y', hasPoster: true, hasBackdrop: true, flags: ['DUPLICATE TITLE'] }),
      ];
      pcm.collectionHealth = () => env({ total: 4,
        flagCounts: { EMPTY: 1, 'NO POSTER': 1, 'NO BACKDROP': 2, 'NO SUMMARY': 1, 'DUPLICATE TITLE': 2 },
        collections: H.managerCollections });
      H.managerDetail = { ratingKey: '201', title: 'Clean Shelf', titleSort: 'Clean Shelf', summary: 'Written.',
        contentRating: 'PG', labels: null, childCount: 4, smart: false,
        thumb: '/library/metadata/201/thumb/1', art: '/library/metadata/201/art/1', librarySectionID: '1',
        visibility: 'unknown', visibilityKnown: false,
        visibilityNote: 'This server does not report collection visibility, so visibility is shown read-only.' };
      pcm.getCollectionDetail = () => env({ detail: H.managerDetail });
      pcm.updateCollectionMeta = (rk, fields) => {
        // Title is held; a content-rating change is deliberately NOT
        // held, so the verified-save report must show one ✓ and
        // one ✗ — the mismatch is the point of the flow.
        const checks = [];
        if (fields.title !== undefined) {
          H.managerDetail = { ...H.managerDetail, title: fields.title };
          checks.push({ field: 'title', sent: fields.title, held: fields.title, ok: true });
        }
        if (fields.contentRating !== undefined) {
          checks.push({ field: 'contentRating', sent: fields.contentRating, held: 'PG', ok: false, note: 'The server did not keep the content rating that was sent.' });
        }
        return env({ detail: H.managerDetail, checks });
      };
      pcm.collectionBulkArt = (keys) => env({ results: (keys || []).map((rk) => rk === '202'
        ? { ratingKey: '202', title: 'Bare Shelf', poster: { ok: false, error: 'no TPDb art match for "Bare Shelf"' }, backdrop: { ok: false, error: 'no TPDb art match for "Bare Shelf"' } }
        : { ratingKey: rk, title: 'Clean Shelf', poster: { ok: true }, backdrop: { ok: true } }) });
      pcm.setCollectionPosterURL = () => env({ changed: true, thumbBefore: '/library/metadata/201/thumb/1', thumbAfter: '/library/metadata/201/thumb/2' });
      pcm.leavingList = () => env({ entries: lvEntries() });
      pcm.leavingSet = (items, date) => {
        if (!/^\\d{4}-\\d{2}-\\d{2}$/.test(String(date || ''))) return Promise.resolve({ ok: false, error: 'invalid leaving date', hint: '' });
        for (const it of items || []) H.leavingDb.set(String(it.ratingKey), { title: it.title || '', date });
        return env({ entries: lvEntries() });
      };
      pcm.leavingRemove = (rk) => { H.leavingDb.delete(String(rk)); return env({ entries: lvEntries() }); };
      let record = null;
      pcm.overlayApply = (jobs, badgeSet, target, runCtx) => {
        H.applyCalls.push({ count: (jobs || []).length, phase: runCtx && runCtx.runPhase });
        if (runCtx && runCtx.runPhase === 'start') {
          record = { runId: runCtx.runId, items: [], cancelled: false, okCount: 0, failCount: 0 };
          H.lastRecord = record;
          return env({ record });
        }
        if (runCtx && runCtx.runPhase === 'append') {
          for (const j of jobs || []) {
            H.capturedJobs.push(j);
            // Mirrors the server: a job's sub-item identity (level +
            // parent title, plus the show for episodes) lands on the
            // run record's item.
            const it = { ratingKey: String(j.ratingKey), title: j.title, outcome: 'applied', ok: true, error: '' };
            if (j.level) {
              it.level = j.level; it.parentTitle = j.parentTitle || '';
              if (j.grandparentTitle) it.grandparentTitle = j.grandparentTitle;
            }
            record.items.push(it);
          }
          return env({ record });
        }
        if (runCtx && runCtx.runPhase === 'finish') {
          for (const o of runCtx.outcomes || []) record.items.push(o);
          record.cancelled = !!runCtx.cancelled;
          record.okCount = record.items.filter((i) => i.outcome === 'applied').length;
          record.failCount = record.items.filter((i) => i.outcome === 'failed').length;
          H.lastRecord = record;
          return env({ record });
        }
        return env({ record: { runId: 'run-legacy', items: [] } });
      };
    })();
  `;
  const src = [readWeb('bridge.js'), stubs, readWeb('themes.js'), readWeb('app.js'), readWeb('collection-art.js'), readWeb('advisor.js'), readWeb('composite.js'), readWeb('overlays.js'), readWeb('collections-manager.js'), readWeb('poster-generator.js')].join('\n;\n');
  // The eval runs AFTER JSDOM construction (see main): beforeParse
  // fires before the document is parsed, and app.js binds DOM
  // elements synchronously at load (production puts its <script>
  // tags at the end of <body> for the same reason). The overlays-
  // only harness never noticed because overlays.js touches the DOM
  // asynchronously. Realm stubs above (fetch, canvas, Image, token)
  // must exist before ANY script runs, so they stay in beforeParse.
  window.__PCM_SRC__ = src;
}

// ---- driver -------------------------------------------------------------------
const checks = [];
function check(name, ok, detail) {
  checks.push({ name, ok });
  console.log(`${ok ? 'PASS' : 'FAIL'} ${name}${detail ? ` — ${detail}` : ''}`);
}
async function waitFor(desc, fn, timeout = 20000) {
  const t0 = Date.now();
  for (;;) {
    let v = null;
    try { v = fn(); } catch { /* keep polling */ }
    if (v) return v;
    if (Date.now() - t0 > timeout) throw new Error('timeout waiting for: ' + desc);
    await sleep(25);
  }
}
async function softWait(fn, timeout = 3000) {
  try { return await waitFor('', fn, timeout); } catch { return null; }
}

async function main() {
  const posterImg = await makePosterImage();
  const dom = new JSDOM(readWeb('index.html'), {
    url: 'http://localhost/',
    runScripts: 'dangerously',
    pretendToBeVisual: true,
    beforeParse: (window) => setupRealm(window, posterImg),
  });
  const { window } = dom;
  const doc = window.document;
  window.eval(window.__PCM_SRC__); // DOM is parsed now — see setupRealm
  const H = window.__HARNESS__;
  const restoreSelectText = () => (doc.querySelector('#restore-section')?.textContent || '').trim();

  // The sidebar version is filled by bridge.js from GET /api/health —
  // the stubbed report carries a version that exists nowhere in the
  // app, so equality proves the fill is server-sourced.
  const versionFilled = await softWait(() =>
    (doc.querySelector('#app-version')?.textContent || '').includes(H.stubVersion));
  check('version: sidebar version comes from the server health report', !!versionFilled,
    `brand reads: ${JSON.stringify(doc.querySelector('#app-version')?.textContent || '')}`);

  // Init runs before "connect": the first three getSections calls
  // (app boot + rail + restore loader) fail. The rail shows its
  // inline error + Retry; the restore select shows the dead
  // fallback option.
  await waitFor('rail retry button after failed init load', () => doc.querySelector('.ovb-retry'));
  check('setup: restore select starts dead (init ran before connect)',
    restoreSelectText().includes('Could not load libraries'), JSON.stringify(restoreSelectText()));

  // "Connect": the rail's own Retry reloads sections + items.
  doc.querySelector('.ovb-retry').click();
  const cell = await waitFor('rail item cell', () => doc.querySelector('.ov-cell[data-kind="item"]'));
  cell.click();
  await waitFor('picked target synced to Apply', () =>
    (doc.querySelector('#btn-overlay-apply-now')?.textContent || '').includes('Apply to 1 item'));

  // Bug 2: opening the Apply view must (re)load the restore libraries.
  doc.querySelector('#overlay-subnav .subnav-item[data-view="apply"]').click();
  const populated = await softWait(() =>
    [...doc.querySelectorAll('#restore-section option')].some((o) => o.value === '1' && o.textContent.includes('Movies')));
  check('bug2: restore library select reloads when the Apply view opens', !!populated,
    `select now reads: ${JSON.stringify(restoreSelectText())}`);

  // Restore backed-up posters (v1.23): the card lists every backed-up
  // item with level + parent context; the filter narrows; per-item
  // and bulk restores pass levels through; un-restorable rows render
  // disabled and are excluded from bulk.
  await softWait(() => (doc.querySelector('#backups-list')?.textContent || '').includes('Season 1'));
  const backupRows = () => [...doc.querySelectorAll('#backups-list .leaving-row')];
  const backupTitles = () => backupRows().map((r) => r.textContent || '');
  check('backups: the card lists all backed-up items with level + parent context',
    backupRows().length === 6 && backupTitles().some((t) => t.includes('Season of Harness Show')) &&
    backupTitles().some((t) => t.includes('Harness Show · Season 1')),
    JSON.stringify(backupTitles()));
  check('backups: the restore-default card no longer claims seasons/episodes are uncovered',
    !(doc.querySelector('#restore-default-card')?.textContent || '').includes('not covered by this release') &&
    (doc.querySelector('#restore-default-card')?.textContent || '').includes('Restore backed-up posters below'), '');
  doc.querySelector('#backup-level').value = 'season';
  doc.querySelector('#backup-level').dispatchEvent(new window.Event('change', { bubbles: true }));
  check('backups: the level filter narrows to seasons only',
    backupRows().length === 2 && (doc.querySelector('#backups-note')?.textContent || '').includes('2 of 6'),
    JSON.stringify(backupTitles()));
  const seasonBtns = backupRows().map((r) => r.querySelector('[data-backup-restore]'));
  check('backups: the un-restorable row renders disabled',
    seasonBtns[0] && !seasonBtns[0].disabled && seasonBtns[1] && seasonBtns[1].disabled, '');
  seasonBtns[0].click();
  await sleep(100);
  check('backups: per-item restore passes the row with its level + parent through',
    H.restoreBackupCalls.length === 1 && H.restoreBackupCalls[0].length === 1 &&
    H.restoreBackupCalls[0][0].ratingKey === '78' && H.restoreBackupCalls[0][0].level === 'season' &&
    H.restoreBackupCalls[0][0].parentTitle === 'Harness Show' &&
    (doc.querySelector('#backups-results')?.textContent || '').includes('restored to its backed-up original'),
    JSON.stringify(H.restoreBackupCalls));
  doc.querySelector('#backup-level').value = '';
  doc.querySelector('#backup-level').dispatchEvent(new window.Event('change', { bubbles: true }));
  doc.querySelector('#btn-backups-restore-all').click();
  await sleep(100);
  const bulkKeys = (H.restoreBackupCalls[H.restoreBackupCalls.length - 1] || []).map((it) => it.ratingKey).sort();
  check('backups: bulk restore covers only the restorable rows, with levels',
    JSON.stringify(bulkKeys) === JSON.stringify(['42', '77', '78', '79']) &&
    (H.restoreBackupCalls[H.restoreBackupCalls.length - 1] || []).some((it) => it.ratingKey === '79' && it.level === 'episode' && it.grandparentTitle === 'Harness Show'),
    JSON.stringify(H.restoreBackupCalls[H.restoreBackupCalls.length - 1]));

  // Bug 1: one-click Apply must composite and submit a real PNG job.
  doc.querySelector('#btn-overlay-apply-now').click();
  const summary = await softWait(() =>
    (doc.querySelector('#overlay-results')?.textContent || '').includes('Run run-'), 30000);
  check('bug1: one-click apply completes and renders a run summary', !!summary,
    summary ? '' : `results pane: ${JSON.stringify((doc.querySelector('#overlay-results')?.textContent || '').slice(0, 200))}`);
  const rec = H.lastRecord;
  const errText = (rec?.items || []).map((i) => i.error || '').join(' | ');
  check('bug1: no outcome carries a ReferenceError', !/is not defined|ReferenceError/.test(errText),
    errText ? `errors: ${errText}` : '(no item errors)');
  check('bug1: overlayApply received at least one job', H.capturedJobs.length >= 1,
    `${H.capturedJobs.length} job(s) captured`);
  const job = H.capturedJobs[0];
  if (job) {
    const buf = Buffer.from(job.pngBase64 || '', 'base64');
    check('bug1: job pngBase64 decodes to real PNG bytes',
      buf.length > 100 && buf[0] === 0x89 && buf[1] === 0x50 && buf[2] === 0x4e && buf[3] === 0x47,
      `${buf.length} bytes, magic ${buf.slice(0, 4).toString('hex')}`);
  }
  check('bug1: run record reports the item applied',
    (rec?.items || []).filter((i) => i.outcome === 'applied').length === 1,
    `items: ${JSON.stringify((rec?.items || []).map((i) => i.outcome))}`);

  // Season drill-down (v1.9): the Studio rail's show cell carries a
  // Seasons button; drilling swaps the grid to the show's seasons;
  // picking one makes it the design target (named with its show) AND
  // the Apply scope's single item; the Apply that follows submits a
  // job carrying the season's level + parent show, and the run
  // record's item for the season carries them too. Back returns to
  // the top-level grid.
  doc.querySelector('#overlay-subnav .subnav-item[data-view="studio"]').click();
  const drillBtn = await waitFor('show cell drill button', () =>
    doc.querySelector('.ov-cell[data-rk="77"] .ov-drill-btn'));
  drillBtn.click();
  const seasonCell = await waitFor('season cell after drill-down', () =>
    doc.querySelector('.ov-cell[data-kind="item"][data-rk="78"]'));
  check('season: drill-down renders the show\'s seasons',
    !!seasonCell && (seasonCell.textContent || '').includes('Season 1'),
    `cell text: ${JSON.stringify((seasonCell?.textContent || '').slice(0, 80))}`);
  seasonCell.click();
  await waitFor('season target name', () =>
    (doc.querySelector('#overlay-target-name')?.textContent || '') === 'Harness Show — Season 1');
  check('season: picked season becomes the target, named with its show',
    (doc.querySelector('#overlay-target-name')?.textContent || '') === 'Harness Show — Season 1',
    `target: ${JSON.stringify(doc.querySelector('#overlay-target-name')?.textContent || '')}`);
  doc.querySelector('.ov-drill-back').click();
  await waitFor('top-level grid restored', () =>
    doc.querySelector('.ov-cell[data-rk="42"]') && !doc.querySelector('.ov-cell[data-rk="78"]'));
  check('season: back row returns to the top-level grid', true);
  doc.querySelector('#overlay-subnav .subnav-item[data-view="apply"]').click();
  doc.querySelector('#btn-overlay-apply-now').click();
  const seasonJob = await waitFor('season apply job captured', () =>
    H.capturedJobs.find((j) => String(j.ratingKey) === '78'), 30000);
  check('season: apply submits the season job with level + parent show',
    seasonJob.level === 'season' && seasonJob.parentTitle === 'Harness Show',
    `job: level=${JSON.stringify(seasonJob.level)} parent=${JSON.stringify(seasonJob.parentTitle)}`);
  const seasonRecItem = (H.lastRecord?.items || []).find((i) => String(i.ratingKey) === '78');
  check('season: run record identifies the season + its parent show',
    !!seasonRecItem && seasonRecItem.outcome === 'applied' &&
    seasonRecItem.level === 'season' && seasonRecItem.parentTitle === 'Harness Show',
    `record item: ${JSON.stringify(seasonRecItem || null)}`);

  // Episode drill-down (v1.10): the second drill level. From the
  // seasons grid, a season cell's Episodes button swaps to episode
  // rows (still + SxxExx + title + duration); picking a row names
  // the target with show AND season AND episode; backing out
  // returns to the seasons grid; and the episode's Apply submits a
  // job + record item carrying the full identity (level "episode",
  // parent = the season, grandparent = the show).
  doc.querySelector('#overlay-subnav .subnav-item[data-view="studio"]').click();
  const drillBtn2 = await waitFor('show cell drill button (episode pass)', () =>
    doc.querySelector('.ov-cell[data-rk="77"] .ov-drill-btn'));
  drillBtn2.click();
  const epBtn = await waitFor('season cell episodes button', () =>
    doc.querySelector('.ov-cell[data-rk="78"] .ov-drill-btn'));
  epBtn.click();
  const epRow = await waitFor('episode row after drill-down', () =>
    doc.querySelector('.ov-ep-row[data-rk="79"]'));
  check('episode: drill-down renders the season\'s episode rows',
    !!epRow && (epRow.textContent || '').includes('S01E01') &&
    (epRow.textContent || '').includes('The Test Episode') &&
    (epRow.textContent || '').includes('42:00'),
    `row text: ${JSON.stringify((epRow?.textContent || '').slice(0, 100))}`);
  epRow.click();
  await waitFor('episode target name', () =>
    (doc.querySelector('#overlay-target-name')?.textContent || '') === 'Harness Show — Season 1 — The Test Episode');
  check('episode: picked episode becomes the target, named with show + season',
    (doc.querySelector('#overlay-target-name')?.textContent || '') === 'Harness Show — Season 1 — The Test Episode',
    `target: ${JSON.stringify(doc.querySelector('#overlay-target-name')?.textContent || '')}`);
  doc.querySelector('.ov-drill-back-ep').click();
  await waitFor('seasons grid restored', () =>
    doc.querySelector('.ov-cell[data-rk="78"]') && !doc.querySelector('.ov-ep-row[data-rk="79"]'));
  check('episode: back row returns to the seasons grid', true);
  doc.querySelector('#overlay-subnav .subnav-item[data-view="apply"]').click();
  doc.querySelector('#btn-overlay-apply-now').click();
  const epJob = await waitFor('episode apply job captured', () =>
    H.capturedJobs.find((j) => String(j.ratingKey) === '79'), 30000);
  check('episode: apply submits the episode job with the full identity',
    epJob.level === 'episode' && epJob.parentTitle === 'Season 1' && epJob.grandparentTitle === 'Harness Show',
    `job: level=${JSON.stringify(epJob.level)} parent=${JSON.stringify(epJob.parentTitle)} grandparent=${JSON.stringify(epJob.grandparentTitle)}`);
  const epRecItem = (H.lastRecord?.items || []).find((i) => String(i.ratingKey) === '79');
  check('episode: run record identifies the episode + season + show',
    !!epRecItem && epRecItem.outcome === 'applied' &&
    epRecItem.level === 'episode' && epRecItem.parentTitle === 'Season 1' && epRecItem.grandparentTitle === 'Harness Show',
    `record item: ${JSON.stringify(epRecItem || null)}`);
  // The episode composite is LANDSCAPE: its PNG must be wider than
  // tall (1600×900), never a squished portrait render.
  if (epJob.pngBase64) {
    const png = Buffer.from(epJob.pngBase64, 'base64');
    const pw = png.readUInt32BE(16), ph = png.readUInt32BE(20);
    check('episode: composite PNG is landscape (1600×900)',
      pw === 1600 && ph === 900, `png is ${pw}×${ph}`);
  }

  // Leaving soon (v1.12): the Apply page's Leaving card manages the
  // app's own per-server list against the stub store: an expired
  // entry renders in its own distinct section, a date set on the
  // picked item lists with the correct days remaining, a
  // collection-wide set dates every member, an in-row edit moves a
  // date, a remove clears the entry — and an Apply for an item whose
  // facts carry a leaving entry fires the family's verdict with the
  // banner text and submits a real composite job.
  const isoPlus = (n) => { const t = new Date(); t.setDate(t.getDate() + n);
    return [t.getFullYear(), String(t.getMonth() + 1).padStart(2, '0'), String(t.getDate()).padStart(2, '0')].join('-'); };
  const leavingListText = () => doc.querySelector('#leaving-list')?.textContent || '';
  doc.querySelector('#overlay-subnav .subnav-item[data-view="studio"]').click();
  // The episode flow left the rail on the seasons grid — back out to
  // the top-level grid before picking the movie.
  const backToTop = doc.querySelector('.ov-drill-back');
  if (backToTop) backToTop.click();
  const itemCell = await waitFor('top-level movie cell for the leaving flow', () =>
    doc.querySelector('.ov-cell[data-kind="item"][data-rk="42"]'));
  itemCell.click();
  await waitFor('movie target name for the leaving flow', () =>
    (doc.querySelector('#overlay-target-name')?.textContent || '') === 'Harness Movie');
  doc.querySelector('#overlay-subnav .subnav-item[data-view="apply"]').click();
  const expiredRow = await waitFor('expired leaving entry renders distinctly', () =>
    doc.querySelector('#leaving-list .leaving-row.leaving-expired'));
  await waitFor('picked note names the picked pool', () =>
    (doc.querySelector('#leaving-picked-note')?.textContent || '').includes('1 picked top-level item'));
  check('leaving: expired entries render in their own distinct section',
    !!expiredRow && leavingListText().includes('Expired Flick') &&
    leavingListText().includes('Expired —') && leavingListText().includes('expired 5 days ago'),
    `list: ${JSON.stringify(leavingListText().slice(0, 200))}`);
  check('leaving: picked note names the picked pool',
    (doc.querySelector('#leaving-picked-note')?.textContent || '').includes('1 picked top-level item'),
    JSON.stringify(doc.querySelector('#leaving-picked-note')?.textContent || ''));
  doc.querySelector('#leaving-date').value = isoPlus(10);
  doc.querySelector('#btn-leaving-set-picked').click();
  await waitFor('picked item listed with its day count', () =>
    leavingListText().includes('Harness Movie') && leavingListText().includes('in 10 days'));
  check('leaving: set-on-picked lists the entry with the correct days remaining', true,
    `list: ${JSON.stringify(leavingListText().slice(0, 200))}`);
  window.pcm.getCollections = () => Promise.resolve({ ok: true,
    collections: [{ ratingKey: '500', title: 'Leaving Shelf' }] });
  window.pcm.getCollectionItems = () => Promise.resolve({ ok: true, items: [
    { ratingKey: '501', title: 'Coll Movie A', thumb: '', type: 'movie' },
    { ratingKey: '502', title: 'Coll Movie B', thumb: '', type: 'movie' },
  ] });
  doc.querySelector('#overlay-subnav .subnav-item[data-view="studio"]').click();
  doc.querySelector('#overlay-subnav .subnav-item[data-view="apply"]').click();
  await waitFor('leaving collection picker populated', () =>
    [...doc.querySelectorAll('#leaving-collection option')].some((o) => o.value === '500'));
  doc.querySelector('#leaving-collection').value = '500';
  doc.querySelector('#leaving-date').value = isoPlus(20);
  doc.querySelector('#btn-leaving-set-collection').click();
  await waitFor('collection members listed', () =>
    leavingListText().includes('Coll Movie A') && leavingListText().includes('Coll Movie B') &&
    leavingListText().includes('in 20 days'));
  check('leaving: collection-wide set creates an entry for each member', true,
    `list: ${JSON.stringify(leavingListText().slice(0, 240))}`);
  const movieRow = [...doc.querySelectorAll('#leaving-list .leaving-row')]
    .find((r) => (r.textContent || '').includes('Harness Movie'));
  movieRow.querySelector('.leaving-edit').value = isoPlus(3);
  movieRow.querySelector('[data-leaving-save]').click();
  await waitFor('edited date listed', () =>
    leavingListText().includes('Harness Movie') && leavingListText().includes('in 3 days'));
  check('leaving: editing a row date moves the entry', true);
  const collRow = [...doc.querySelectorAll('#leaving-list .leaving-row')]
    .find((r) => (r.textContent || '').includes('Coll Movie A'));
  collRow.querySelector('[data-leaving-remove]').click();
  await waitFor('removed entry gone', () => !leavingListText().includes('Coll Movie A'));
  check('leaving: remove clears the entry', !leavingListText().includes('Coll Movie A') &&
    leavingListText().includes('Coll Movie B'));
  H.facts = { ...FACTS, leavingSet: true, leavingDate: isoPlus(10), leavingDays: 10 };
  doc.querySelector('#overlay-subnav .subnav-item[data-view="studio"]').click();
  doc.querySelector('.ov-cell[data-kind="item"][data-rk="42"]').click();
  await waitFor('movie target picked for the leaving apply', () =>
    (doc.querySelector('#overlay-target-name')?.textContent || '') === 'Harness Movie');
  doc.querySelector('[data-fam-on="leaving"]').click();
  await waitFor('leaving family verdict fires with the banner text', () => {
    const card = doc.querySelector('[data-fam-on="leaving"]')?.closest('.family-card');
    return !!card && (card.textContent || '').includes('FIRES') && (card.textContent || '').includes('LEAVING SOON');
  });
  check('leaving: family verdict fires — will draw “LEAVING SOON”', true);
  const jobsBefore = H.capturedJobs.length;
  doc.querySelector('#overlay-subnav .subnav-item[data-view="apply"]').click();
  doc.querySelector('#btn-overlay-apply-now').click();
  const lvJob = await waitFor('leaving apply job captured', () =>
    H.capturedJobs.slice(jobsBefore).find((j) => String(j.ratingKey) === '42'), 30000);
  const lvBuf = Buffer.from(lvJob.pngBase64 || '', 'base64');
  check('leaving: apply submits a real composite job for the dated item',
    lvBuf.length > 100 && lvBuf[0] === 0x89 && lvBuf[1] === 0x50 && lvBuf[2] === 0x4e && lvBuf[3] === 0x47,
    `${lvBuf.length} bytes`);
  H.facts = FACTS;

  // Pill settings (v1.15): the family set is editable from the
  // Studio card — a TV-PG show stays dark under the shipped set
  // and fires once its chip is toggled on — and the leaving
  // family's card sets/clears the CURRENT target's date inline,
  // against the same store the Apply page manager renders.
  const daysUntilIso = (iso) => {
    const p = String(iso).split('-').map(Number);
    const now = new Date();
    return Math.round((Date.UTC(p[0], p[1] - 1, p[2]) - Date.UTC(now.getFullYear(), now.getMonth(), now.getDate())) / 86400000);
  };
  const famCard = (id) => doc.querySelector(`[data-fam-on="${id}"]`)?.closest('.family-card');
  const famCardText = (id) => famCard(id)?.textContent || '';
  // -- Family chips on a TV-PG show (the owner's exact report).
  H.facts = { ...FACTS, type: 'show', contentRating: 'TV-PG' };
  doc.querySelector('#overlay-subnav .subnav-item[data-view="studio"]').click();
  doc.querySelector('.ov-cell[data-kind="item"][data-rk="42"]').click();
  await waitFor('TV-PG show target picked', () =>
    (doc.querySelector('#overlay-target-name')?.textContent || '') === 'Harness Movie');
  const famToggleP = doc.querySelector('[data-fam-on="family"]');
  if (famToggleP && !famToggleP.checked) famToggleP.click();
  await waitFor('family verdict names the shipped set', () =>
    famCardText('family').includes("won't fire") && famCardText('family').includes('TV-Y / TV-Y7 / TV-G'));
  check('pill: TV-PG show stays dark under the shipped family set, note names it', true,
    famCardText('family').slice(0, 200));
  const pgChip = famCard('family')?.querySelector('[data-fam-rating="TV-PG"]');
  const gChip = famCard('family')?.querySelector('[data-fam-rating="TV-G"]');
  check('pill: family card renders rating chips, shipped set on, TV-PG off',
    !!pgChip && !pgChip.classList.contains('on') && !!gChip && gChip.classList.contains('on') &&
    !!famCard('family')?.querySelector('[data-fam-rating="NC-17"]') &&
    !!famCard('family')?.querySelector('[data-fam-rating="TV-Y7-FV"]'));
  pgChip.click();
  await waitFor('family verdict fires with TV-PG in the set', () =>
    famCardText('family').includes('FIRES') && famCardText('family').includes('FAMILY'));
  check('pill: toggling the TV-PG chip makes the TV-PG show fire', true,
    famCardText('family').slice(0, 200));
  famCard('family').querySelector('[data-fam-rating="TV-PG"]').click();
  await waitFor('family verdict dark again without TV-PG', () =>
    famCardText('family').includes("won't fire") && famCardText('family').includes('G / PG / TV-Y / TV-Y7 / TV-G'));
  check('pill: toggling the chip back off restores a dark verdict naming the stored set', true,
    famCardText('family').slice(0, 200));
  const famToggleOff = doc.querySelector('[data-fam-on="family"]');
  if (famToggleOff && famToggleOff.checked) famToggleOff.click();
  H.facts = FACTS;
  // -- Leaving inline control on the inspected movie.
  H.leavingDb.delete('42');
  const origItemFacts = window.pcm.overlayItemFacts;
  window.pcm.overlayItemFacts = (rk) => origItemFacts(rk).then((res) => {
    if (String(rk) !== '42' || !res || !res.facts) return res;
    const e = H.leavingDb.get('42');
    const f = { ...res.facts };
    delete f.leavingSet; delete f.leavingDate; delete f.leavingDays;
    if (e) { f.leavingSet = true; f.leavingDate = e.date; f.leavingDays = daysUntilIso(e.date); }
    return { ...res, facts: f };
  });
  doc.querySelector('.ov-cell[data-kind="item"][data-rk="42"]').click();
  await waitFor('movie target re-picked for the inline leaving flow', () =>
    (doc.querySelector('#overlay-target-name')?.textContent || '') === 'Harness Movie');
  const lvToggleP = doc.querySelector('[data-fam-on="leaving"]');
  if (lvToggleP && !lvToggleP.checked) lvToggleP.click();
  await waitFor('inline leaving control renders with not-set state', () =>
    !!famCard('leaving')?.querySelector('[data-fam-leaving-apply]') &&
    (famCard('leaving').querySelector('[data-fam-leaving-state]')?.textContent || '') === 'not set');
  check('pill: leaving card renders the inline date control on a top-level target, state not set', true,
    famCardText('leaving').slice(0, 200));
  famCard('leaving').querySelector('[data-fam-leaving-apply]').click();
  await waitFor('empty date refusal stated in the card', () =>
    (famCard('leaving')?.querySelector('[data-fam-leaving-note]')?.textContent || '').includes('Choose a date first'));
  check('pill: empty date is refused with the reason and nothing is written', !H.leavingDb.has('42'));
  const realLeavingSet = window.pcm.leavingSet;
  window.pcm.leavingSet = () => Promise.resolve({ ok: false, error: 'invalid leaving date — not a real calendar date', hint: '' });
  famCard('leaving').querySelector('[data-fam-leaving-date]').value = isoPlus(9);
  famCard('leaving').querySelector('[data-fam-leaving-apply]').click();
  await waitFor('failed write states the reason in the card', () =>
    (famCard('leaving')?.querySelector('[data-fam-leaving-note]')?.textContent || '').includes('not a real calendar date'));
  check('pill: a failed write states the error and never fakes a set',
    !H.leavingDb.has('42') &&
    (famCard('leaving').querySelector('[data-fam-leaving-state]')?.textContent || '') === 'not set');
  window.pcm.leavingSet = realLeavingSet;
  famCard('leaving').querySelector('[data-fam-leaving-date]').value = isoPlus(15);
  famCard('leaving').querySelector('[data-fam-leaving-apply]').click();
  await waitFor('inline set flips the verdict to FIRES', () =>
    famCardText('leaving').includes('FIRES') && famCardText('leaving').includes('LEAVING SOON'));
  check('pill: setting the date in the card flips the verdict to FIRES — LEAVING SOON', true,
    famCardText('leaving').slice(0, 200));
  check('pill: the inline state shows the stored date and days remaining',
    (famCard('leaving').querySelector('[data-fam-leaving-state]')?.textContent || '').includes('in 15 days') &&
    H.leavingDb.get('42')?.date === isoPlus(15),
    famCard('leaving').querySelector('[data-fam-leaving-state]')?.textContent || '');
  doc.querySelector('#overlay-subnav .subnav-item[data-view="apply"]').click();
  await waitFor('manager lists the entry set from the pill', () =>
    leavingListText().includes('Harness Movie') && leavingListText().includes('in 15 days'));
  check('pill: the Apply manager shows the same entry — one store', true,
    leavingListText().slice(0, 220));
  doc.querySelector('#overlay-subnav .subnav-item[data-view="studio"]').click();
  doc.querySelector('.ov-cell[data-kind="item"][data-rk="42"]').click();
  await waitFor('clear control present on the dated target', () =>
    !!famCard('leaving')?.querySelector('[data-fam-leaving-clear]'));
  famCard('leaving').querySelector('[data-fam-leaving-clear]').click();
  await waitFor('clear returns the target to not set', () =>
    famCardText('leaving').includes("won't fire") &&
    (famCard('leaving').querySelector('[data-fam-leaving-state]')?.textContent || '') === 'not set');
  check('pill: clear returns the target to not set', !H.leavingDb.has('42'));
  doc.querySelector('#overlay-subnav .subnav-item[data-view="apply"]').click();
  await waitFor('manager no longer lists the cleared entry', () =>
    !leavingListText().includes('in 15 days'));
  check('pill: the Apply manager drops the cleared entry too', true,
    leavingListText().slice(0, 220));
  doc.querySelector('#overlay-subnav .subnav-item[data-view="studio"]').click();
  // Season target: the inline control does not render (dates
  // belong to the movie or show, never a season).
  const drillBtnP = await waitFor('show cell drill button (pill)', () =>
    doc.querySelector('.ov-cell[data-rk="77"] .ov-drill-btn'));
  drillBtnP.click();
  const seasonCellP = await waitFor('season cell (pill)', () =>
    doc.querySelector('.ov-cell[data-kind="item"][data-rk="78"]'));
  seasonCellP.click();
  await waitFor('season target picked (pill)', () =>
    (doc.querySelector('#overlay-target-name')?.textContent || '') === 'Harness Show — Season 1');
  check('pill: a season target shows no inline date control',
    !!famCard('leaving') && !famCard('leaving').querySelector('[data-fam-leaving-apply]') &&
    !famCard('leaving').querySelector('[data-fam-leaving-date]'),
    famCardText('leaving').slice(0, 160));
  window.pcm.overlayItemFacts = origItemFacts;
  doc.querySelector('.ov-drill-back').click();
  await waitFor('top-level grid restored (pill)', () =>
    doc.querySelector('.ov-cell[data-rk="42"]') && !doc.querySelector('.ov-cell[data-rk="78"]'));
  check('pill: back row returns to the top-level grid', true);

  // Parallel restore Count: 20 items, each classification delayed
  // 40ms by the stub. A serial count cannot finish under 800ms; the
  // pool (6 in flight) lands around 4 rounds ≈ 160-250ms. Statuses
  // cycle restore / already-default / no-provider / restore /
  // reject, so the aggregated counts (8 · 4 · 4 · 4) and the restore
  // list's ORDER must match the serial expectation exactly — the
  // pool changes only the wall clock, never the result.
  const countItems = Array.from({ length: 20 }, (_, i) => ({
    ratingKey: String(100 + i), title: `Count Item ${i}`, thumb: '', type: 'movie',
  }));
  let classifyCalls = 0;
  window.pcm.getSectionItems = () => Promise.resolve({ ok: true, items: countItems });
  window.pcm.overlayClassifyPoster = (rk) => new Promise((resolve, reject) => {
    setTimeout(() => {
      classifyCalls++;
      const m = (Number(rk) - 100) % 5;
      if (m === 4) return reject(new Error('stub classify failure'));
      const status = m === 1 ? 'already-default' : m === 2 ? 'no-provider' : 'restore';
      resolve({ ok: true, classification: { status } });
    }, 40);
  });
  const countT0 = Date.now();
  doc.querySelector('#btn-restore-count').click();
  await waitFor('parallel count final note', () =>
    (doc.querySelector('#restore-count-note')?.textContent || '').includes('to restore'), 10000);
  const countElapsed = Date.now() - countT0;
  check('count: pool classifies 20 delayed items far below the serial floor',
    countElapsed < 550 && countElapsed > 100,
    `${countElapsed}ms (serial floor 800ms; pool of 6 ≈ 160-250ms)`);
  check('count: every item classified exactly once', classifyCalls === 20, `${classifyCalls} calls`);
  check('count: aggregated counts identical to the serial expectation',
    (doc.querySelector('#restore-count-note')?.textContent || '').trim() ===
    `8 to restore · 4 already default · 4 no provider poster · 4 couldn't be checked`,
    JSON.stringify((doc.querySelector('#restore-count-note')?.textContent || '').trim()));
  // The restore list's order is observable through the Run itself:
  // startRestoreDefaults walks set.restore in order, one bridge
  // call per item (the run stays serial — only the count pooled).
  const runOrder = [];
  window.pcm.overlayRestoreDefaultPoster = (job) => {
    runOrder.push(String(job.ratingKey));
    return Promise.resolve({ ok: true, outcome: 'restored' });
  };
  doc.querySelector('#btn-restore-start').click();
  await waitFor('restore run walks all counted items', () => runOrder.length === 8, 10000);
  check('count: restore list order preserved (collected by index)',
    runOrder.join(',') === '100,103,105,108,110,113,115,118', runOrder.join(','));

  // Collections manager (v1.13): health flags render with their
  // basis, the flag filter narrows the list, the editor binds the
  // server's actual fields (labels disabled when unexposed,
  // visibility read-only when unreported), a verified save shows one
  // held field and one NOT-held field, the poster-URL action reports
  // its read-back, and bulk art reports per-collection outcomes.
  await window.PCMCollectionsManager.show('1');
  const cmRows = await waitFor('manager health rows', () => {
    const rows = doc.querySelectorAll('#cm-rows .cm-row');
    return rows.length === 4 ? rows : null;
  });
  check('manager: health view renders every collection with its facts',
    cmRows.length === 4 &&
    (doc.querySelector('#cm-summary-line')?.textContent || '').includes('4 collections') &&
    (doc.querySelector('#cm-summary-line')?.textContent || '').includes('3 need attention'),
    `summary: ${JSON.stringify((doc.querySelector('#cm-summary-line')?.textContent || '').trim())}`);
  const bareRow = [...cmRows].find((r) => (r.textContent || '').includes('Bare Shelf'));
  const cleanRow = [...cmRows].find((r) => (r.textContent || '').includes('Clean Shelf'));
  check('manager: flags render on the flagged row only, with counts as basis',
    !!bareRow && ['EMPTY', 'NO POSTER', 'NO BACKDROP', 'NO SUMMARY'].every((f) => (bareRow.textContent || '').includes(f)) &&
    (bareRow.textContent || '').includes('0 members') &&
    !!cleanRow && !cleanRow.querySelector('.cm-flag') &&
    (cleanRow.textContent || '').includes('4 members'),
    `bare: ${JSON.stringify((bareRow?.textContent || '').slice(0, 130))}`);
  const cmFilter = doc.querySelector('#cm-filter');
  cmFilter.value = 'EMPTY';
  cmFilter.dispatchEvent(new window.Event('change'));
  check('manager: flag filter narrows to the flagged collections',
    doc.querySelectorAll('#cm-rows .cm-row').length === 1 &&
    (doc.querySelector('#cm-rows')?.textContent || '').includes('Bare Shelf'),
    `rows now: ${doc.querySelectorAll('#cm-rows .cm-row').length}`);
  cmFilter.value = 'all';
  cmFilter.dispatchEvent(new window.Event('change'));
  doc.querySelector('[data-cm-edit="201"]').click();
  await waitFor('editor modal fields', () => doc.querySelector('#cm-f-title'));
  check('manager: editor binds the fields the server actually holds',
    doc.querySelector('#cm-f-title').value === 'Clean Shelf' &&
    doc.querySelector('#cm-f-sort').value === 'Clean Shelf' &&
    doc.querySelector('#cm-f-rating').value === 'PG' &&
    doc.querySelector('#cm-f-summary').value === 'Written.');
  check('manager: unexposed labels are disabled with the reason, visibility read-only with its note',
    !doc.querySelector('#cm-f-labels') &&
    (doc.querySelector('#cm-vis-note')?.textContent || '').includes('not reported by this server') &&
    !doc.querySelector('input[name="cm-vis"]'),
    `vis note: ${JSON.stringify((doc.querySelector('#cm-vis-note')?.textContent || '').slice(0, 90))}`);
  doc.querySelector('#cm-f-title').value = 'Clean Shelf Renamed';
  doc.querySelector('#cm-f-rating').value = 'PG-13';
  doc.querySelector('#cm-save').click();
  await waitFor('verified save report', () =>
    (doc.querySelector('#cm-save-results')?.textContent || '').includes('verified against the server'));
  const saveText = doc.querySelector('#cm-save-results')?.textContent || '';
  check('manager: verified save shows the held field AND the not-held field',
    saveText.includes('Title') && saveText.includes('server holds: Clean Shelf Renamed') &&
    saveText.includes('Content rating') && saveText.includes('did not keep the content rating'),
    `report: ${JSON.stringify(saveText.slice(0, 220))}`);
  doc.querySelector('#cm-f-posterurl').value = 'https://example.test/new-poster.png';
  doc.querySelector('#cm-set-poster-url').click();
  await waitFor('poster URL read-back line', () =>
    (doc.querySelector('#cm-poster-url-result')?.textContent || '').includes('now serves the new poster'));
  check('manager: poster-from-URL reports its verified read-back', true);
  doc.querySelector('[data-close="cancel"]').click();
  // Bulk art: tick the clean + bare rows, apply both targets.
  doc.querySelector('.cm-check[data-rk="201"]').click();
  doc.querySelector('.cm-check[data-rk="202"]').click();
  await waitFor('bulk bar visible', () => doc.querySelector('#cm-bulkbar') && !doc.querySelector('#cm-bulkbar').hidden);
  check('manager: bulk bar counts the selection',
    (doc.querySelector('#cm-bulk-count')?.textContent || '') === '2 selected',
    JSON.stringify(doc.querySelector('#cm-bulk-count')?.textContent || ''));
  doc.querySelector('#cm-bulk-backdrop').click();
  doc.querySelector('#cm-bulk-apply').click();
  await waitFor('bulk results report', () =>
    (doc.querySelector('#cm-bulk-results')?.textContent || '').includes('Bulk art results'));
  const bulkText = doc.querySelector('#cm-bulk-results')?.textContent || '';
  check('manager: bulk art reports per-collection success and failure with reasons',
    bulkText.includes('Clean Shelf') && bulkText.includes('applied & verified') &&
    bulkText.includes('Bare Shelf') && bulkText.includes('no TPDb art match'),
    `results: ${JSON.stringify(bulkText.slice(0, 200))}`);

  // Builder wizard (v1.17) — app.js and advisor.js load FOR REAL in
  // this harness, so the wizard walks run end to end against the fake
  // 150-movie library: blank (paging + server-side search + selection
  // surviving both; abandon writes nothing; then a full create), the
  // Advisor's smart/static seeds landing in the wizard with state,
  // list import (one matched item removed in the grid; the review
  // reflects it), a rules-static create (full match list, not the
  // preview page), an empty-static block with its reason, and a
  // failed create surfacing in the Review step.
  // PRE-EXISTING CHECK CHANGES: the v1.14 Builder checks pinned the
  // old two-column layout's element ids (#btn-dryrun straight from a
  // seed, #draft-list as the only member surface, "Use these matches"
  // semantics). The wizard replaces that layout end to end, so those
  // checks are re-pinned here as wizard walks over the SAME behaviors:
  // seeds land with state intact, the dry-run still gates every write,
  // create payloads are exact, and no path bypasses the review.
  window.pcm.getSectionItems = (sectionKey, filterQuery) => Promise.resolve({ ok: true,
    items: String(filterQuery || '').includes('genre=Action') ? H.advScanItems : [H.item] });
  H.createdCollections = [];
  window.pcm.createCollection = (payload) => {
    if (payload.title === 'Fail Wizard') return Promise.resolve({ ok: false, error: 'Fixture create failed', hint: '' });
    H.createCalls.push(payload);
    const c = { ratingKey: String(900 + H.createdCollections.length), title: payload.title,
      childCount: (payload.itemRatingKeys || []).length, smart: !!payload.smart,
      thumb: '', art: '', summary: payload.summary || '', titleSort: payload.titleSort || payload.title,
      contentRating: payload.contentRating || '', addedAt: 0, updatedAt: 0,
      promotedToOwnHome: false, promotedToSharedHome: false, promotedToRecommended: false };
    H.createdCollections.push(c);
    return Promise.resolve({ ok: true, collection: { ratingKey: c.ratingKey, title: c.title } });
  };
  const itemByKey = new Map([...H.library150, ...H.advScanItems].map((i) => [String(i.ratingKey), i]));
  window.pcm.getCollections = () => Promise.resolve({ ok: true, collections: H.createdCollections });
  window.pcm.getCollectionItems = (rk) => {
    const idx = H.createdCollections.findIndex((c) => c.ratingKey === String(rk));
    const payload = idx >= 0 ? H.createCalls[idx] : null;
    return Promise.resolve({ ok: true, items: payload ? (payload.itemRatingKeys || []).map((k) => itemByKey.get(String(k))).filter(Boolean) : [] });
  };
  const wzVisible = (n) => !doc.querySelector('#wz-step-' + n).hidden;
  const gridCard = (rk) => doc.querySelector(`#wz-member-grid [data-wz-rk="${rk}"]`);
  const trayCount = () => doc.querySelector('#member-count')?.textContent;
  const pickRadio = (sel) => { const el = doc.querySelector(sel); el.click(); el.dispatchEvent(new window.Event('change', { bubbles: true })); };

  doc.querySelector('.nav-item[data-tab="collections"]').click();
  await waitFor('builder library options loaded', () => doc.querySelectorAll('#b-section option').length >= 1);

  // ---- Blank walk, part 1: paging + search + durable selection, then abandon.
  doc.querySelector('.nav-item[data-tab="builder"]').click();
  check('wizard: opens at Step 1 with the three sources offered',
    wzVisible(1) && doc.querySelectorAll('input[name="wz-source"]').length === 3 &&
    doc.querySelector('input[name="wz-source"]:checked')?.value === 'blank');
  doc.querySelector('#btn-wizard-next').click();
  await waitFor('wizard grid page 1', () => doc.querySelectorAll('#wz-member-grid .wz-card').length === 60);
  check('wizard: blank grid loads page 1 with an honest loaded/total line',
    (doc.querySelector('#wz-grid-counts')?.textContent || '') === 'Showing 60 of 150 titles',
    JSON.stringify(doc.querySelector('#wz-grid-counts')?.textContent));
  gridCard('1001').click();
  check('wizard: clicking a poster selects it (visible state + tray count)',
    gridCard('1001').classList.contains('selected') && trayCount() === '1',
    `tray: ${trayCount()}`);
  doc.querySelector('#wz-grid-more').click();
  await waitFor('wizard grid page 2 appended', () => doc.querySelectorAll('#wz-member-grid .wz-card').length === 120);
  gridCard('1061').click();
  check('wizard: selection accumulates across pages, tray count exact',
    trayCount() === '2' && gridCard('1001').classList.contains('selected') && gridCard('1061').classList.contains('selected'),
    `tray: ${trayCount()}`);
  const wzSearch = doc.querySelector('#wz-grid-search');
  wzSearch.value = 'Zebra'; wzSearch.dispatchEvent(new window.Event('input', { bubbles: true }));
  await waitFor('wizard search narrows the grid', () => doc.querySelectorAll('#wz-member-grid .wz-card').length === 3);
  check('wizard: title search narrows to the matches, counts stay honest',
    (doc.querySelector('#wz-grid-counts')?.textContent || '') === 'Showing 3 of 3 titles' && !!gridCard('1076'),
    JSON.stringify(doc.querySelector('#wz-grid-counts')?.textContent));
  check('wizard: paging + search are server-side page requests',
    H.pageCalls.some((c) => c.start === 60 && c.size === 60) && H.pageCalls.some((c) => c.title === 'Zebra'),
    JSON.stringify(H.pageCalls.slice(-4)));
  gridCard('1076').click(); // Zebra Middle, found via search
  gridCard('1001').click(); // deselect the page-1 pick from within the results
  check('wizard: selection survives searching (add + remove inside results)',
    trayCount() === '2' && gridCard('1076').classList.contains('selected') && !gridCard('1001').classList.contains('selected'),
    `tray: ${trayCount()}`);
  wzSearch.value = ''; wzSearch.dispatchEvent(new window.Event('input', { bubbles: true }));
  await waitFor('wizard search cleared, page 1 back', () => doc.querySelectorAll('#wz-member-grid .wz-card').length === 60);
  check('wizard: selections from later pages/search persist back on page 1',
    trayCount() === '2' &&
    (doc.querySelector('#draft-list')?.textContent || '').includes('Library Movie 061') &&
    (doc.querySelector('#draft-list')?.textContent || '').includes('Zebra Middle'),
    `tray: ${trayCount()} list: ${JSON.stringify((doc.querySelector('#draft-list')?.textContent || '').slice(0, 120))}`);
  doc.querySelector('#btn-builder-reset').click();
  check('wizard: abandoning mid-wizard writes nothing and resets to Step 1',
    H.createCalls.length === 0 && wzVisible(1) && trayCount() === '0',
    `creates: ${H.createCalls.length} step1: ${wzVisible(1)} tray: ${trayCount()}`);

  // ---- Advisor seeds: the smart choice lands at Step 2 (rules).
  doc.querySelector('.nav-item[data-tab="advisor"]').click();
  const genreCard = await waitFor('advisor genre card', () =>
    doc.querySelector('.sugg-card[data-id="genre:1:Action"]'));
  genreCard.click();
  await waitFor('card pop-out create actions', () => doc.querySelector('#adv-create'));
  check('builder: a rules-carrying card offers BOTH static and smart creation',
    !!doc.querySelector('#adv-create') && !!doc.querySelector('#adv-create-smart') &&
    (doc.querySelector('#adv-create')?.textContent || '').includes('static'),
    `actions: ${JSON.stringify((doc.querySelector('#modal-root')?.textContent || '').slice(0, 160))}`);
  // v1.18 — the click opens the pop-out editor; the old below-the-list
  // detail region is gone from the page and the list never hides.
  check('advisor: clicking a card opens the pop-out editor, card highlighted, list in place, no detail region',
    !!doc.querySelector('#modal-root .modal') && !doc.querySelector('#advisor-detail') &&
    !doc.querySelector('.advisor-body').hidden && genreCard.classList.contains('selected'),
    `modal: ${!!doc.querySelector('#modal-root .modal')} selected: ${genreCard.classList.contains('selected')}`);
  check('advisor: pop-out carries editable title, kind/section, reason, rules summary, members and all actions',
    doc.querySelector('#adv-pop-title')?.value === 'Action (Movies)' &&
    (doc.querySelector('#modal-root')?.textContent || '').includes('Movies') &&
    (doc.querySelector('#modal-root')?.textContent || '').includes('Fixture genre suggestion.') &&
    (doc.querySelector('#modal-root')?.textContent || '').includes('Genre') &&
    (doc.querySelector('#modal-root')?.textContent || '').includes('Action') &&
    (doc.querySelector('#modal-root')?.textContent || '').includes('Harness Movie') &&
    !!doc.querySelector('#adv-draft') && !!doc.querySelector('#adv-dismiss') &&
    !!doc.querySelector('#modal-root [data-close="cancel"]'),
    `popout: ${JSON.stringify((doc.querySelector('#modal-root')?.textContent || '').slice(0, 200))}`);
  doc.querySelector('#adv-create-smart').click();
  await waitFor('smart seed lands in the Builder', () => doc.querySelector('#b-smart')?.checked);
  check('builder: smart choice checks the toggle, seeds the rule and the title',
    doc.querySelector('#b-smart').checked === true &&
    doc.querySelector('#b-title').value === 'Action (Movies)' &&
    doc.querySelector('#rule-list select[data-k="field"]')?.value === 'genre' &&
    doc.querySelector('#rule-list input[data-k="value"]')?.value === 'Action',
    `title: ${JSON.stringify(doc.querySelector('#b-title')?.value)} query: ${JSON.stringify(doc.querySelector('#filter-query-preview')?.textContent)}`);
  check('builder: mode text explains the smart seed',
    (doc.querySelector('#builder-mode-desc')?.textContent || '').includes('smart collection from an Advisor suggestion'),
    JSON.stringify((doc.querySelector('#builder-mode-desc')?.textContent || '').slice(0, 120)));
  check('wizard: the smart seed lands on Step 2 with the rules source active',
    wzVisible(2) && doc.querySelector('input[name="wz-source"]:checked')?.value === 'rules');
  await waitFor('rule preview count matches the fake filter result', () =>
    (doc.querySelector('#wz-rule-count')?.textContent || '').includes('2 matches'));
  check('wizard: live rule preview count + first page render the matches read-only',
    (doc.querySelector('#rule-preview-grid')?.textContent || '').includes('Harness Movie') &&
    (doc.querySelector('#rule-preview-grid')?.textContent || '').includes('Second Movie'),
    JSON.stringify(doc.querySelector('#wz-rule-count')?.textContent));
  doc.querySelector('#btn-wizard-next').click();
  await waitFor('details step for the smart seed', () => wzVisible(3));
  check('wizard: Details offers the static/smart choice with the smart side pre-set',
    !!doc.querySelector('#wz-kind-static') && doc.querySelector('#wz-kind-smart')?.checked === true &&
    !doc.querySelector('#wz-kind-choice').hidden);
  doc.querySelector('#b-content-rating').value = 'PG';
  doc.querySelector('#btn-wizard-next').click();
  await waitFor('smart review enables Create', () => wzVisible(4) && !doc.querySelector('#btn-apply').disabled);
  doc.querySelector('#btn-apply').click();
  const smartYes = await waitFor('smart create confirm modal', () =>
    doc.querySelector('#modal-root [data-close="yes"]'));
  smartYes.click();
  await waitFor('smart create captured', () => H.createCalls.length === 1);
  const smartPayload = H.createCalls[0];
  check('builder: smart create stores the rules and NO member list',
    smartPayload.smart === true && smartPayload.filterQuery === 'genre=Action' &&
    Array.isArray(smartPayload.itemRatingKeys) && smartPayload.itemRatingKeys.length === 0,
    JSON.stringify(smartPayload));
  check('wizard: Details content rating rides the create payload',
    smartPayload.contentRating === 'PG', JSON.stringify(smartPayload.contentRating));
  await waitFor('wizard lands on the created collection with confirmation', () =>
    !doc.querySelector('#collection-detail').hidden &&
    (doc.querySelector('#detail-title')?.textContent || '') === 'Action (Movies)' &&
    (doc.querySelector('#collection-created-note')?.textContent || '').includes('was created'));
  check('wizard: success lands on the created collection with a plain confirmation', true);

  // ---- Advisor static choice: members seeded, toggle off, Step 2.
  doc.querySelector('.nav-item[data-tab="advisor"]').click();
  // v1.18: no detail region to back out of — the smart pass's Create
  // closed its pop-out, and returning to the tab reloads the grid.
  const genreCard2 = await waitFor('advisor genre card (static pass)', () =>
    doc.querySelector('.sugg-card[data-id="genre:1:Action"]'));
  genreCard2.click();
  await waitFor('card pop-out (static pass)', () => doc.querySelector('#adv-create'));
  doc.querySelector('#adv-create').click();
  await waitFor('static seed lands in the Builder', () =>
    (doc.querySelector('#member-count')?.textContent || '') === '2');
  check('advisor: Create closes the pop-out as it seeds the Builder',
    !doc.querySelector('#modal-root .modal'));
  check('builder: static choice seeds the members with the Smart toggle off',
    doc.querySelector('#b-smart').checked === false &&
    doc.querySelector('#member-count').textContent === '2' &&
    doc.querySelector('#b-title').value === 'Action (Movies)',
    `members: ${doc.querySelector('#member-count')?.textContent} title: ${JSON.stringify(doc.querySelector('#b-title')?.value)}`);
  check('wizard: the static seed lands on Step 2 (members) with state intact',
    wzVisible(2) && (doc.querySelector('#draft-list')?.textContent || '').includes('Second Movie'));

  // ---- Advisor pop-out editor (v1.18): the edited title flows into
  // drafts AND creates; Close returns to the list with the card
  // still highlighted; Dismiss closes and removes the suggestion.
  doc.querySelector('.nav-item[data-tab="advisor"]').click();
  const sagaCard = await waitFor('advisor franchise card (pop-out pass)', () =>
    doc.querySelector('.sugg-card[data-id="franchise:1:Harness"]'));
  sagaCard.click();
  await waitFor('franchise pop-out opens', () => doc.querySelector('#adv-pop-title'));
  check('advisor: franchise pop-out opens with the suggested title prefilled and a plain Create',
    doc.querySelector('#adv-pop-title').value === 'Harness Saga' &&
    (doc.querySelector('#adv-create')?.textContent || '').includes('Create in Builder') &&
    !doc.querySelector('#adv-create-smart'),
    `title: ${JSON.stringify(doc.querySelector('#adv-pop-title')?.value)}`);
  doc.querySelector('#adv-pop-title').value = 'My Edited Saga';
  doc.querySelector('#adv-draft').click();
  await waitFor('draft saved from the pop-out', () => H.drafts.length === 1);
  check('advisor: Save as draft files the EDITED title with the card members, pop-out stays open',
    H.drafts[0].title === 'My Edited Saga' &&
    (H.drafts[0].memberRatingKeys || []).join(',') === '42,43' &&
    !!doc.querySelector('#modal-root .modal'),
    JSON.stringify(H.drafts[0]));
  doc.querySelector('#modal-root [data-close="cancel"]').click();
  await waitFor('pop-out closed via Close', () => !doc.querySelector('#modal-root .modal'));
  check('advisor: Close returns to the list with the clicked card still highlighted',
    !!doc.querySelector('.sugg-card[data-id="franchise:1:Harness"]') &&
    doc.querySelector('.sugg-card[data-id="franchise:1:Harness"]').classList.contains('selected') &&
    !doc.querySelector('.advisor-body').hidden);
  doc.querySelector('#advisor-subnav [data-view="drafts"]').click();
  await waitFor('drafts view lists the pop-out draft', () =>
    (doc.querySelector('#drafts-list')?.textContent || '').includes('My Edited Saga'));
  check('advisor: the pop-out draft appears in the Drafts view', true);
  doc.querySelector('#advisor-subnav [data-view="suggestions"]').click();
  const genreCard3 = await waitFor('genre card (edited-title create pass)', () =>
    doc.querySelector('.sugg-card[data-id="genre:1:Action"]'));
  genreCard3.click();
  await waitFor('genre pop-out (edited-title create pass)', () => doc.querySelector('#adv-create'));
  doc.querySelector('#adv-pop-title').value = 'Renamed Action Picks';
  doc.querySelector('#adv-create').click();
  await waitFor('edited-title create lands in the Builder', () =>
    doc.querySelector('#b-title')?.value === 'Renamed Action Picks');
  check('advisor: Create from the pop-out seeds the EDITED title with the members, pop-out closed',
    doc.querySelector('#member-count').textContent === '2' && !doc.querySelector('#modal-root .modal'),
    `title: ${JSON.stringify(doc.querySelector('#b-title')?.value)} members: ${doc.querySelector('#member-count')?.textContent}`);
  doc.querySelector('.nav-item[data-tab="advisor"]').click();
  const genreCard4 = await waitFor('genre card back after create (dismiss pass)', () =>
    doc.querySelector('.sugg-card[data-id="genre:1:Action"]'));
  genreCard4.click();
  await waitFor('genre pop-out (dismiss pass)', () => doc.querySelector('#adv-dismiss'));
  doc.querySelector('#adv-dismiss').click();
  await waitFor('dismiss closes the pop-out and removes the card', () =>
    !doc.querySelector('#modal-root .modal') && !doc.querySelector('.sugg-card[data-id="genre:1:Action"]'));
  check('advisor: Dismiss closes the pop-out, removes the suggestion, and reports its id',
    (H.dismissedIds || []).join(',') === 'genre:1:Action',
    JSON.stringify(H.dismissedIds));

  // ---- AI collection suggestions (v1.21): with a key configured the
  // Advisor's AI row offers per-section suggestions; the run's cards
  // join the same rail/cards/pop-out system carrying their
  // provenance (model name + verified tally); Create seeds the
  // wizard with exactly the verified members; Dismiss persists
  // through the shared store so a re-run cannot resurrect the card;
  // with no key the row hides and ZERO calls are made.
  H.aiStore.key = true;
  doc.querySelector('.nav-item[data-tab="advisor"]').click();
  await waitFor('AI suggest row appears once a key is set', () =>
    !doc.querySelector('#ai-suggest-row').hidden && !doc.querySelector('#btn-ai-suggest').disabled);
  check('advisor: with an AI key, the suggest row shows with the scan sections to pick from',
    (doc.querySelector('#ai-section')?.textContent || '').includes('Movies'),
    JSON.stringify(doc.querySelector('#ai-section')?.textContent || ''));
  doc.querySelector('#btn-ai-suggest').click();
  const aiCard = await waitFor('AI suggestion card', () =>
    doc.querySelector('.sugg-card[data-id="ai:1:spooky-season-picks"]'));
  check('advisor: the AI card renders in the list with its verified-count line',
    (aiCard.textContent || '').includes('AI suggestions') &&
    (aiCard.textContent || '').includes('2 of 3 suggested titles verified'),
    JSON.stringify((aiCard.textContent || '').slice(0, 200)));
  check('advisor: the rail gains the AI suggestions group and the status line reports the calls made',
    [...doc.querySelectorAll('.rail-item')].some((b) => (b.textContent || '').includes('AI suggestions')) &&
    (doc.querySelector('#ai-suggest-status')?.textContent || '').includes('2 AI calls'),
    `status: ${JSON.stringify(doc.querySelector('#ai-suggest-status')?.textContent || '')}`);
  aiCard.click();
  await waitFor('AI card pop-out', () => doc.querySelector('#adv-create'));
  check('advisor: the AI pop-out states the model and the verified tally',
    (doc.querySelector('#modal-root')?.textContent || '').includes('AI suggestion') &&
    (doc.querySelector('#modal-root')?.textContent || '').includes('cf-chat-model-x') &&
    (doc.querySelector('#modal-root')?.textContent || '').includes('2 of 3 suggested titles verified in your library'),
    JSON.stringify((doc.querySelector('#modal-root')?.textContent || '').slice(0, 240)));
  doc.querySelector('#adv-create').click();
  await waitFor('AI create lands in the Builder', () =>
    doc.querySelector('#b-title')?.value === 'Spooky Season Picks');
  check('advisor: AI Create seeds the wizard with the title and exactly the verified members',
    doc.querySelector('#member-count').textContent === '2' && !doc.querySelector('#modal-root .modal'),
    `title: ${JSON.stringify(doc.querySelector('#b-title')?.value)} members: ${doc.querySelector('#member-count')?.textContent}`);
  // Back in the Advisor the deterministic list reloads (AI cards are
  // per-run); a fresh run brings the card back — until it is dismissed.
  doc.querySelector('.nav-item[data-tab="advisor"]').click();
  await waitFor('suggest row ready for the dismiss pass', () =>
    !doc.querySelector('#ai-suggest-row').hidden && !doc.querySelector('#btn-ai-suggest').disabled);
  doc.querySelector('#btn-ai-suggest').click();
  const aiCard2 = await waitFor('AI card returns on a fresh run', () =>
    doc.querySelector('.sugg-card[data-id="ai:1:spooky-season-picks"]'));
  aiCard2.click();
  await waitFor('AI pop-out (dismiss pass)', () => doc.querySelector('#adv-dismiss'));
  doc.querySelector('#adv-dismiss').click();
  await waitFor('AI dismiss removes the card', () =>
    !doc.querySelector('#modal-root .modal') && !doc.querySelector('.sugg-card[data-id="ai:1:spooky-season-picks"]'));
  check('advisor: dismissing the AI card records its section+title id',
    (H.dismissedIds || []).includes('ai:1:spooky-season-picks'),
    JSON.stringify(H.dismissedIds));
  doc.querySelector('#btn-ai-suggest').click();
  await softWait(() => H.aiSuggestCalls.length === 3, 4000);
  await softWait(() => (doc.querySelector('#ai-suggest-status')?.textContent || '').includes('proposed nothing'), 4000);
  check('advisor: a re-run after dismissal does NOT resurrect the AI card',
    !doc.querySelector('.sugg-card[data-id="ai:1:spooky-season-picks"]') && H.aiSuggestCalls.length === 3,
    `calls: ${H.aiSuggestCalls.length} status: ${JSON.stringify(doc.querySelector('#ai-suggest-status')?.textContent || '')}`);
  // No key → the row hides behind the existing guidance and the
  // bridge is never called (the stub's call log must not move).
  H.aiStore.key = false;
  doc.querySelector('.nav-item[data-tab="builder"]').click();
  doc.querySelector('.nav-item[data-tab="advisor"]').click();
  await waitFor('AI rows hide with no key configured', () =>
    doc.querySelector('#ai-suggest-row').hidden && !doc.querySelector('#ai-strip-hint').hidden);
  check('advisor: with no AI key the suggest row is hidden and zero AI calls were made',
    H.aiSuggestCalls.length === 3,
    `calls: ${H.aiSuggestCalls.length}`);

  // ---- Import walk: preview seeds the grid pre-selected; one matched
  // item is removed IN THE GRID and the review reflects the removal.
  doc.querySelector('#btn-builder-reset').click();
  pickRadio('input[name="wz-source"][value="import"]');
  doc.querySelector('#btn-wizard-next').click();
  await waitFor('import step visible', () => wzVisible(2) && !doc.querySelector('#wz-import-panel').hidden);
  doc.querySelector('#imp-ref').value = '12345';
  doc.querySelector('#btn-import-preview').click();
  await waitFor('import report renders', () =>
    (doc.querySelector('#import-results')?.textContent || '').includes('Ghost Title'));
  const draftTitles = [...doc.querySelectorAll('#draft-list .draft-item .t strong')].map((e) => e.textContent).join('|');
  check('builder: import seeds matched members in list order, unmatched shown with reasons',
    draftTitles === 'Harness Movie|Second Movie' &&
    (doc.querySelector('#import-results')?.textContent || '').includes('not in library') &&
    (doc.querySelector('#import-results')?.textContent || '').includes('imported once') &&
    doc.querySelector('#b-title').value === 'Harness List',
    `draft: ${draftTitles} title: ${JSON.stringify(doc.querySelector('#b-title')?.value)}`);
  const impCard = (rk) => doc.querySelector(`#wz-import-grid [data-wz-rk="${rk}"]`);
  check('wizard: matched items appear in the import grid pre-selected',
    impCard('42')?.classList.contains('selected') && impCard('43')?.classList.contains('selected') && trayCount() === '2');
  impCard('42').click();
  check('wizard: removing a matched item in the grid updates tray + report',
    trayCount() === '1' && !impCard('42').classList.contains('selected') &&
    (doc.querySelector('#import-results')?.textContent || '').includes('1 of 4 entries matched') &&
    (doc.querySelector('#import-results')?.textContent || '').includes('Removed before creation'),
    `tray: ${trayCount()} report: ${JSON.stringify((doc.querySelector('#import-results')?.textContent || '').slice(0, 160))}`);
  doc.querySelector('#btn-wizard-next').click();
  await waitFor('import details step', () => wzVisible(3));
  doc.querySelector('#btn-wizard-next').click();
  await waitFor('import review renders', () =>
    (doc.querySelector('#dryrun-output')?.textContent || '').includes('IMPORT'));
  check('builder: the review re-renders the import report incl. the grid removal',
    (doc.querySelector('#dryrun-output')?.textContent || '').includes('Ghost Title') &&
    (doc.querySelector('#dryrun-output')?.textContent || '').includes('not in library') &&
    (doc.querySelector('#dryrun-output')?.textContent || '').includes('Removed before creation') &&
    (doc.querySelector('#dryrun-output')?.textContent || '').includes('1 of 4 entries matched'),
    JSON.stringify((doc.querySelector('#dryrun-output')?.textContent || '').slice(0, 260)));
  await waitFor('import apply enabled', () => !doc.querySelector('#btn-apply').disabled);
  doc.querySelector('#btn-apply').click();
  const importYes = await waitFor('import create confirm modal', () =>
    doc.querySelector('#modal-root [data-close="yes"]'));
  importYes.click();
  await waitFor('import create captured', () => H.createCalls.length === 2);
  const importPayload = H.createCalls[1];
  check('builder: import create writes exactly the remaining matched set, in list order',
    importPayload.smart === false && (importPayload.itemRatingKeys || []).join(',') === '43',
    JSON.stringify(importPayload));

  // ---- Blank walk, part 2: a full create through the wizard.
  doc.querySelector('.nav-item[data-tab="builder"]').click();
  doc.querySelector('#btn-builder-reset').click();
  doc.querySelector('#btn-wizard-next').click();
  await waitFor('blank grid for the create walk', () => doc.querySelectorAll('#wz-member-grid .wz-card').length === 60);
  gridCard('1001').click();
  doc.querySelector('#wz-grid-more').click();
  await waitFor('blank grid page 2 for the create walk', () => doc.querySelectorAll('#wz-member-grid .wz-card').length === 120);
  gridCard('1061').click();
  doc.querySelector('#btn-wizard-next').click();
  await waitFor('blank details step', () => wzVisible(3));
  doc.querySelector('#b-title').value = 'Wizard Blank Picks';
  doc.querySelector('#b-content-rating').value = 'G';
  doc.querySelector('#btn-wizard-next').click();
  await waitFor('blank review shows the picked members', () =>
    (doc.querySelector('#dryrun-output')?.textContent || '').includes('ADD 2'));
  check('wizard: review diff lists exactly the picked members before create',
    (doc.querySelector('#dryrun-output')?.textContent || '').includes('Zebra Opening') &&
    (doc.querySelector('#dryrun-output')?.textContent || '').includes('Library Movie 061'),
    JSON.stringify((doc.querySelector('#dryrun-output')?.textContent || '').slice(0, 220)));
  doc.querySelector('#btn-apply').click();
  const blankYes = await waitFor('blank create confirm modal', () =>
    doc.querySelector('#modal-root [data-close="yes"]'));
  blankYes.click();
  await waitFor('blank create captured', () => H.createCalls.length === 3);
  const blankPayload = H.createCalls[2];
  check('wizard: blank create payload is exactly the grid selection, in click order',
    blankPayload.smart === false && (blankPayload.itemRatingKeys || []).join(',') === '1001,1061' &&
    blankPayload.title === 'Wizard Blank Picks' && blankPayload.contentRating === 'G',
    JSON.stringify(blankPayload));
  await waitFor('blank create lands on its collection', () =>
    (doc.querySelector('#detail-title')?.textContent || '') === 'Wizard Blank Picks');
  check('wizard: blank create lands on the created collection detail', true);

  // ---- Rules walk, static side: the preview count is the fake's
  // filter result; choosing Static materializes the FULL match list.
  doc.querySelector('.nav-item[data-tab="builder"]').click();
  doc.querySelector('#btn-builder-reset').click();
  pickRadio('input[name="wz-source"][value="rules"]');
  doc.querySelector('#btn-wizard-next').click();
  await waitFor('rules step visible', () => wzVisible(2) && !doc.querySelector('#wz-rules-panel').hidden);
  doc.querySelector('#btn-add-rule').click();
  const ruleVal = doc.querySelector('#rule-list input[data-k="value"]');
  ruleVal.value = 'Action';
  ruleVal.dispatchEvent(new window.Event('change', { bubbles: true }));
  await waitFor('rules preview count equals the fake filter result', () =>
    (doc.querySelector('#wz-rule-count')?.textContent || '').includes('2 matches'));
  check('wizard: rules preview count matches the fake filter result, matches render',
    (doc.querySelector('#wz-rule-count')?.textContent || '').includes('2 matches') &&
    (doc.querySelector('#rule-preview-grid')?.textContent || '').includes('Second Movie'),
    JSON.stringify(doc.querySelector('#wz-rule-count')?.textContent));
  doc.querySelector('#btn-wizard-next').click();
  await waitFor('rules details step', () => wzVisible(3));
  check('wizard: rules source offers BOTH static and smart at Details',
    !!doc.querySelector('#wz-kind-static') && !!doc.querySelector('#wz-kind-smart') &&
    !doc.querySelector('#wz-kind-choice').hidden);
  pickRadio('#wz-kind-static');
  doc.querySelector('#b-title').value = 'Wizard Rules Static';
  doc.querySelector('#btn-wizard-next').click();
  await waitFor('rules static review materializes all matches', () =>
    (doc.querySelector('#dryrun-output')?.textContent || '').includes('ADD 2'));
  doc.querySelector('#btn-apply').click();
  const rulesYes = await waitFor('rules static confirm modal', () =>
    doc.querySelector('#modal-root [data-close="yes"]'));
  rulesYes.click();
  await waitFor('rules static create captured', () => H.createCalls.length === 4);
  const rulesPayload = H.createCalls[3];
  check('wizard: static-from-rules creates the full matched member set (not the preview page)',
    rulesPayload.smart === false && (rulesPayload.itemRatingKeys || []).join(',') === '42,43',
    JSON.stringify(rulesPayload));

  // ---- Empty static is blocked at Review with the reason.
  doc.querySelector('#btn-builder-reset').click();
  doc.querySelector('#btn-wizard-next').click();
  await waitFor('empty walk members step', () => wzVisible(2));
  doc.querySelector('#btn-wizard-next').click();
  await waitFor('empty walk details step', () => wzVisible(3));
  doc.querySelector('#b-title').value = 'Empty Wizard';
  doc.querySelector('#btn-wizard-next').click();
  await waitFor('empty static review renders', () => wzVisible(4));
  check('wizard: empty static creation is blocked at Review with the reason',
    doc.querySelector('#btn-apply').disabled &&
    (doc.querySelector('#dryrun-output')?.textContent || '').includes('A static collection needs at least one member') &&
    (doc.querySelector('#wz-review-error')?.textContent || '').includes('A static collection needs at least one member'),
    JSON.stringify((doc.querySelector('#wz-review-error')?.textContent || '').slice(0, 140)));
  check('wizard: the blocked review created nothing', H.createCalls.length === 4);

  // ---- A failed create shows in the step and claims no success.
  doc.querySelector('#btn-builder-reset').click();
  doc.querySelector('#btn-wizard-next').click();
  await waitFor('failure walk grid', () => doc.querySelectorAll('#wz-member-grid .wz-card').length >= 60);
  gridCard('1002').click();
  doc.querySelector('#btn-wizard-next').click();
  await waitFor('failure walk details', () => wzVisible(3));
  doc.querySelector('#b-title').value = 'Fail Wizard';
  doc.querySelector('#btn-wizard-next').click();
  await waitFor('failure walk review', () => wzVisible(4) && !doc.querySelector('#btn-apply').disabled);
  doc.querySelector('#btn-apply').click();
  const failYes = await waitFor('failure confirm modal', () =>
    doc.querySelector('#modal-root [data-close="yes"]'));
  failYes.click();
  await waitFor('create failure shows in the Review step', () =>
    (doc.querySelector('#wz-review-error')?.textContent || '').includes('Fixture create failed'));
  check('wizard: a failed create stays on Review, shows the error, and records no create',
    wzVisible(4) && H.createCalls.length === 4 &&
    (doc.querySelector('#wz-review-error')?.textContent || '').includes('Nothing was applied'),
    JSON.stringify(doc.querySelector('#wz-review-error')?.textContent || ''));

  // Health view: a smart collection is marked SMART, its count
  // reads as CURRENT matches, and 0 matches is NO CURRENT MATCHES
  // (never a bare EMPTY) with the rule summary beside it.
  H.managerCollections = [...H.managerCollections, {
    ratingKey: '205', title: 'Smart Shelf', childCount: 0, smart: true,
    content: 'server://domtest-machine/com.plexapp.plugins.library/library/sections/1/all?type=1&decade=2000',
    smartSummary: 'Decade is 2000',
    thumb: '', art: '', summary: '', titleSort: 'Smart Shelf', addedAt: 0, updatedAt: 0,
    promotedToOwnHome: false, promotedToSharedHome: false, promotedToRecommended: false,
    hasPoster: false, hasBackdrop: false, visibilityKnown: false,
    flags: ['NO CURRENT MATCHES', 'NO POSTER', 'NO BACKDROP', 'NO SUMMARY'],
  }];
  window.pcm.collectionHealth = () => Promise.resolve({ ok: true, total: 5,
    flagCounts: { EMPTY: 1, 'NO CURRENT MATCHES': 1, 'NO POSTER': 2, 'NO BACKDROP': 3, 'NO SUMMARY': 2, 'DUPLICATE TITLE': 2 },
    collections: H.managerCollections });
  await window.PCMCollectionsManager.show('1');
  const smartRow = await waitFor('manager smart row', () =>
    [...doc.querySelectorAll('#cm-rows .cm-row')].find((r) => (r.textContent || '').includes('Smart Shelf')));
  const smartFlags = [...smartRow.querySelectorAll('.cm-flag')].map((e) => e.textContent);
  check('manager: smart row marked SMART, 0 matches is NO CURRENT MATCHES (never bare EMPTY), rule summary shown',
    smartFlags.includes('SMART') && smartFlags.includes('NO CURRENT MATCHES') && !smartFlags.includes('EMPTY') &&
    (smartRow.textContent || '').includes('currently 0 matches') &&
    (smartRow.textContent || '').includes('Decade is 2000') &&
    (smartRow.textContent || '').includes('rule-based'),
    `flags: ${JSON.stringify(smartFlags)} row: ${JSON.stringify((smartRow.textContent || '').slice(0, 200))}`);
  const cmFilter2 = doc.querySelector('#cm-filter');
  cmFilter2.value = 'NO CURRENT MATCHES';
  cmFilter2.dispatchEvent(new window.Event('change'));
  check('manager: NO CURRENT MATCHES is a working flag filter',
    doc.querySelectorAll('#cm-rows .cm-row').length === 1 &&
    (doc.querySelector('#cm-rows')?.textContent || '').includes('Smart Shelf'),
    `rows now: ${doc.querySelectorAll('#cm-rows .cm-row').length}`);
  cmFilter2.value = 'all';
  cmFilter2.dispatchEvent(new window.Event('change'));

  // ---- Themes (v1.19): the registry is complete; the default theme
  // is the stylesheet's :root, value for value (zero-drift parity);
  // every built-in applies app-wide from its Settings card; a custom
  // edit applies live, Save persists the whole palette to the store,
  // export/import round-trips it, bad imports are rejected with the
  // theme untouched, a switch mid-wizard costs the draft nothing, and
  // a simulated reload boots into the saved custom theme (stale
  // mirror first, then the server value wins).
  const T = window.PCMThemes;
  const normCss = (s) => String(s || '').replace(/\s+/g, ' ').trim();
  const cssRoot = (readWeb('styles.css').match(/:root\s*\{([\s\S]*?)\n\}/) || [])[1] || '';
  const cssVarVal = (name) => {
    const m = cssRoot.match(new RegExp(name.replace(/-/g, '\\-') + '\\s*:\\s*([^;]+);'));
    return m ? normCss(m[1]) : null;
  };
  check('themes: registry exposes the six built-ins over a 55-variable palette',
    !!T && T.VARS.length === 55 &&
    ['projectionist', 'midnight', 'cinema', 'light', 'contrast', 'plex'].every((n) => !!T.THEMES[n]),
    `vars: ${T && T.VARS.length}`);
  check('themes: every built-in palette is complete and valid',
    Object.values(T.THEMES).every((t) => T.validatePalette(t.vars) === null));
  const drift = T.VARS.filter((v) => cssVarVal(v) !== normCss(T.THEMES.projectionist.vars[v]));
  check('themes: stylesheet :root equals the default theme on every variable (zero-drift parity)',
    drift.length === 0, drift.length ? `drifted: ${drift.join(', ')}` : '55/55 identical');
  const appliedVar = (v) => doc.documentElement.style.getPropertyValue(v).trim();
  check('themes: boot applies the default theme (store starts at Projectionist)',
    T.current().name === 'projectionist' && appliedVar('--bg-1') === '#0b0d16' && appliedVar('--amber') === '#f5a524',
    `current: ${T.current().name}, --bg-1 ${appliedVar('--bg-1')}`);
  const themeCards = () => [...doc.querySelectorAll('#theme-cards .theme-card')];
  check('themes: Appearance renders seven cards (six built-ins + Custom), default active',
    themeCards().length === 7 &&
    (doc.querySelector('#theme-cards .theme-card.active') || {}).dataset?.theme === 'projectionist',
    `cards: ${themeCards().map((b) => b.dataset.theme).join(',')}`);
  const clickTheme = async (name) => {
    themeCards().find((b) => b.dataset.theme === name).click();
    await softWait(() => T.current().name === name);
  };
  await clickTheme('midnight');
  check('themes: Midnight applies from its card and persists',
    appliedVar('--bg-1') === '#060b18' && appliedVar('--amber') === '#4da3ff' &&
    appliedVar('--text') === '#e8efff' && H.themeStore.name === 'midnight',
    `--bg-1 ${appliedVar('--bg-1')}, store ${H.themeStore.name}`);
  await clickTheme('light');
  check('themes: Light applies (paper background, ink text, light color-scheme)',
    appliedVar('--bg-1') === '#f4f5f9' && appliedVar('--text') === '#171c30' &&
    appliedVar('--panel') === 'rgba(23, 28, 54, 0.045)' && doc.documentElement.style.colorScheme === 'light',
    `--bg-1 ${appliedVar('--bg-1')}, scheme ${doc.documentElement.style.colorScheme}`);
  await clickTheme('cinema');
  check('themes: Cinema applies (bronze accent on near-black)',
    appliedVar('--amber') === '#d9903f' && appliedVar('--bg-1') === '#0e0a06' && appliedVar('--muted') === '#c2a87e',
    `--amber ${appliedVar('--amber')}`);
  await clickTheme('contrast');
  check('themes: High contrast applies (pure black, white text, solid white focus)',
    appliedVar('--bg-1') === '#000000' && appliedVar('--text') === '#ffffff' && appliedVar('--focus') === '#ffffff',
    `--focus ${appliedVar('--focus')}`);
  await clickTheme('plex');
  check('themes: Plex applies (charcoal surfaces, warm-gray text, signature orange accent)',
    appliedVar('--amber') === '#e5a00d' && appliedVar('--bg-1') === '#1f1f1f' &&
    appliedVar('--text') === '#e8e6e3' && appliedVar('--surface-solid') === '#282828',
    `--amber ${appliedVar('--amber')}, --bg-1 ${appliedVar('--bg-1')}`);

  // A theme switch mid-wizard must cost the draft nothing: reset,
  // pick a member, title it, then switch and inspect the draft.
  doc.querySelector('#btn-builder-reset').click();
  doc.querySelector('#btn-wizard-next').click();
  await waitFor('theme-survival walk grid', () => doc.querySelectorAll('#wz-member-grid .wz-card').length >= 60);
  gridCard('1003').click();
  doc.querySelector('#btn-wizard-next').click();
  await waitFor('theme-survival walk details', () => wzVisible(3));
  doc.querySelector('#b-title').value = 'Theme Survivor';
  await clickTheme('midnight');
  check('themes: switching themes mid-wizard leaves the step, title and member intact',
    wzVisible(3) && doc.querySelector('#b-title').value === 'Theme Survivor' &&
    trayCount() === '1' && T.current().name === 'midnight',
    `title: ${JSON.stringify(doc.querySelector('#b-title').value)}, tray: ${trayCount()}`);

  // Custom: edit the accent in the editor — applies live; Save
  // persists the complete palette; export/import round-trips it.
  doc.querySelector('[data-tab="settings"]').click();
  await waitFor('appearance cards on the Settings tab', () => themeCards().length === 7);
  const hexInput = doc.querySelector('.theme-hex[data-var="--amber"]');
  hexInput.value = '#12ab34';
  hexInput.dispatchEvent(new window.Event('input', { bubbles: true }));
  check('themes: a custom color edit applies live as the Custom theme',
    T.current().name === 'custom' && appliedVar('--amber') === '#12ab34',
    `current: ${T.current().name}, --amber ${appliedVar('--amber')}`);
  doc.querySelector('#theme-save').click();
  await softWait(() => H.themeStore.name === 'custom' && H.themeStore.custom && H.themeStore.custom['--amber'] === '#12ab34');
  check('themes: Save persists the complete custom palette to the settings store',
    H.themeStore.name === 'custom' && Object.keys(H.themeStore.custom || {}).length === 55 &&
    H.themeStore.custom['--amber'] === '#12ab34',
    `stored vars: ${Object.keys(H.themeStore.custom || {}).length}`);
  const exported = T.exportJSON();
  const parsedExport = JSON.parse(exported);
  check('themes: export produces the theme JSON shape with the full palette',
    parsedExport.app === 'projectionist-theme' && parsedExport.vars['--amber'] === '#12ab34' &&
    Object.keys(parsedExport.vars).length === 55);
  await clickTheme('cinema');
  const imp = await T.importJSON(exported);
  check('themes: import round-trips to the identical palette and saves it',
    imp.ok === true && T.current().name === 'custom' && appliedVar('--amber') === '#12ab34' &&
    T.VARS.every((v) => (T.savedCustom() || {})[v] === parsedExport.vars[v]));
  const badIncomplete = await T.importJSON('{"vars":{"--bg-1":"#000000"}}');
  const badJSON = await T.importJSON('not json at all');
  const badColor = await T.importJSON(JSON.stringify({ vars: { ...parsedExport.vars, '--bg-1': 'url(evil)' } }));
  check('themes: incomplete / malformed / non-color imports are rejected, theme untouched',
    !badIncomplete.ok && !badJSON.ok && !badColor.ok &&
    T.current().name === 'custom' && appliedVar('--amber') === '#12ab34',
    `errors: ${JSON.stringify([badIncomplete.error, badJSON.error, badColor.error])}`);
  doc.querySelector('#theme-import-toggle').click();
  doc.querySelector('#theme-import-text').value = '{"vars":{}}';
  doc.querySelector('#theme-import-apply').click();
  await softWait(() => (doc.querySelector('#theme-import-error')?.textContent || '').length > 0);
  check('themes: the import box shows a plain error and keeps the current theme',
    (doc.querySelector('#theme-import-error')?.textContent || '').includes('incomplete') &&
    T.current().name === 'custom' && appliedVar('--amber') === '#12ab34',
    JSON.stringify(doc.querySelector('#theme-import-error')?.textContent || ''));
  doc.querySelector('#theme-reset').click();
  await softWait(() => T.current().name === 'projectionist');
  check('themes: Reset returns to the Projectionist default and persists it',
    T.current().name === 'projectionist' && appliedVar('--amber') === '#f5a524' && H.themeStore.name === 'projectionist',
    `store: ${H.themeStore.name}`);

  // ---- TPDb anti-bot warning (v1.19): the one-line warning rides
  // every surface that pulls art from ThePosterDB — the Builder's
  // poster card AND its Find-art picker modal, the manager's bulk
  // art area, and the manager editor's poster-from-URL block. (The
  // list-import card is TMDb/Trakt and deliberately carries none.)
  const TPDB_WARN = 'anti-bot protection has tripped on your connection';
  check('tpdb: the Builder poster card carries the anti-bot warning',
    (doc.querySelector('#tab-builder')?.textContent || '').includes(TPDB_WARN));
  doc.querySelector('#btn-find-art').click();
  await waitFor('art picker modal', () => doc.querySelector('#modal-root .modal'));
  check('tpdb: the Find collection art picker carries the anti-bot warning',
    (doc.querySelector('#modal-root')?.textContent || '').includes(TPDB_WARN),
    JSON.stringify((doc.querySelector('#modal-root .modal-body')?.textContent || '').slice(0, 120)));
  doc.querySelector('#modal-root [data-close="cancel"]').click();
  await softWait(() => !doc.querySelector('#modal-root .modal'));
  check('tpdb: the manager bulk-art area carries the anti-bot warning',
    !!doc.querySelector('#cm-art-note') && (doc.querySelector('#cm-art-note')?.textContent || '').includes(TPDB_WARN),
    JSON.stringify(doc.querySelector('#cm-art-note')?.textContent || ''));
  doc.querySelector('#cm-rows [data-cm-edit="201"]').click();
  await waitFor('manager editor modal', () => doc.querySelector('#modal-root .cm-poster-url'));
  check('tpdb: the single-collection poster-from-URL block carries the anti-bot warning',
    (doc.querySelector('#modal-root .cm-poster-url')?.textContent || '').includes(TPDB_WARN));
  doc.querySelector('#modal-root [data-close="cancel"]').click();
  await softWait(() => !doc.querySelector('#modal-root .modal'));

  // ---- Poster generator from a RULES draft (v1.21 rider — owner's
  // live report): a rules/smart draft's draft.members is EMPTY by
  // design (a smart collection stores rules, not members, and a
  // static-from-rules draft materializes its matches only at
  // review), and the generator's old click handler snapshotted
  // exactly that array — so a draft whose preview read "75 matches"
  // opened the generator with ZERO members. The generator now gets
  // its members through app.js's getDraftPosterSource bridge: rules
  // drafts yield the FULL match set (the review step's own
  // enumeration, never the 60-item preview page), with the source
  // stated in the member header; a zero-match rules draft opens with
  // an honest note, never a silent empty list dressed as a
  // memberless collection.
  const RIDER_ITEMS = Array.from({ length: 75 }, (_, i) => ({
    ratingKey: String(5000 + i), title: 'Rider Match ' + String(i + 1).padStart(2, '0'),
    thumb: '', type: 'movie', year: 2000 + (i % 20), sectionKey: '1',
  }));
  const realPage = window.pcm.getSectionItemsPage;
  const realFull = window.pcm.getSectionItems;
  window.pcm.getSectionItemsPage = (sectionKey, opts = {}) => {
    const q = String((opts && opts.filterQuery) || '');
    if (q.includes('genre=Adventure')) {
      return Promise.resolve({ ok: true, items: RIDER_ITEMS.slice(0, (opts && opts.size) || 60),
        total: RIDER_ITEMS.length, totalKnown: true, hasMore: true });
    }
    if (q.includes('genre=Nothing')) {
      return Promise.resolve({ ok: true, items: [], total: 0, totalKnown: true, hasMore: false });
    }
    return realPage(sectionKey, opts);
  };
  window.pcm.getSectionItems = (sectionKey, filterQuery) => {
    const q = String(filterQuery || '');
    if (q.includes('genre=Adventure')) return Promise.resolve({ ok: true, items: RIDER_ITEMS });
    if (q.includes('genre=Nothing')) return Promise.resolve({ ok: true, items: [] });
    return realFull(sectionKey, filterQuery);
  };
  const riderRuleDraft = async (value, countText) => {
    doc.querySelector('.nav-item[data-tab="builder"]').click();
    doc.querySelector('#btn-builder-reset').click();
    pickRadio('input[name="wz-source"][value="rules"]');
    doc.querySelector('#btn-wizard-next').click();
    await waitFor('rider rules step', () => wzVisible(2) && !doc.querySelector('#wz-rules-panel').hidden);
    doc.querySelector('#btn-add-rule').click();
    const rv = doc.querySelector('#rule-list input[data-k="value"]');
    rv.value = value;
    rv.dispatchEvent(new window.Event('change', { bubbles: true }));
    await waitFor('rider rule preview ' + value, () =>
      (doc.querySelector('#wz-rule-count')?.textContent || '').includes(countText));
    doc.querySelector('#btn-wizard-next').click();
    await waitFor('rider details step', () => wzVisible(3));
  };
  // A 75-match SMART rules draft (the owner's exact case: rules,
  // smart, preview reading "75 matches") → Generate.
  await riderRuleDraft('Adventure', '75 matches');
  pickRadio('#wz-kind-smart');
  doc.querySelector('#b-title').value = 'Rider Rules Picks';
  H.genCalls.length = 0;
  doc.querySelector('#btn-generate-art').click();
  await waitFor('generator modal (rules draft)', () => doc.querySelector('#modal-root .gen-layout'));
  await softWait(() => doc.querySelectorAll('#modal-root [data-gen-member]').length > 0, 4000);
  check('generator: a rules draft opens with the FULL match set (75), not the preview page and not zero',
    doc.querySelectorAll('#modal-root [data-gen-member]').length === 75,
    `rows: ${doc.querySelectorAll('#modal-root [data-gen-member]').length} members pane: ${JSON.stringify((doc.querySelector('#gen-members')?.textContent || '').trim().slice(0, 100))}`);
  check('generator: the member header states the rules source and count',
    (doc.querySelector('#gen-source-note')?.textContent || '').includes('75 matches from your rules') &&
    (doc.querySelector('#gen-mem-count')?.textContent || '').includes('75 of 75'),
    `note: ${JSON.stringify(doc.querySelector('#gen-source-note')?.textContent || '')} count: ${JSON.stringify(doc.querySelector('#gen-mem-count')?.textContent || '')}`);
  await waitFor('generator rules-draft preview', () => H.genCalls.length >= 1);
  check('generator: the first preview renders all 75 rule matches',
    (H.genCalls[0].memberKeys || []).length === 75,
    JSON.stringify((H.genCalls[0].memberKeys || []).length));
  doc.querySelector('#modal-root [data-close="cancel"]').click();
  await softWait(() => !doc.querySelector('#modal-root .modal'));
  // A zero-match rules draft → the honest note, not a memberless shrug.
  await riderRuleDraft('Nothing', '0 matches');
  doc.querySelector('#b-title').value = 'Rider Empty Rules';
  doc.querySelector('#btn-generate-art').click();
  await waitFor('generator modal (zero-match rules draft)', () => doc.querySelector('#modal-root .gen-layout'));
  await softWait(() => (doc.querySelector('#gen-members')?.textContent || '').length > 0, 4000);
  check('generator: a zero-match rules draft opens with the honest note — never the memberless-collection message',
    (doc.querySelector('#gen-members')?.textContent || '').includes('match nothing') &&
    !(doc.querySelector('#gen-members')?.textContent || '').includes('This collection has no members'),
    JSON.stringify((doc.querySelector('#gen-members')?.textContent || '').trim().slice(0, 160)));
  doc.querySelector('#modal-root [data-close="cancel"]').click();
  await softWait(() => !doc.querySelector('#modal-root .modal'));
  window.pcm.getSectionItemsPage = realPage;
  window.pcm.getSectionItems = realFull;
  // Restore the blank one-member draft the v1.20 generator block
  // below starts from (the failure walk's leftovers).
  doc.querySelector('#btn-builder-reset').click();
  doc.querySelector('#btn-wizard-next').click();
  await waitFor('restore blank grid', () => doc.querySelectorAll('#wz-member-grid .wz-card').length === 60);
  gridCard('1002').click();
  doc.querySelector('#b-title').value = 'Fail Wizard';

  // ---- Poster generator (v1.20): opens from the Builder wizard AND
  // the Collections manager; template/layout switching re-renders
  // through the bridge; the shortfall state is honest; the AI panel
  // shows Settings guidance with no key configured and generates
  // with one; both apply paths land (draft art slot via PCMArt,
  // manager via setPoster — the backup-gate path).
  H.genCalls.length = 0; H.setPosterCalls.length = 0; H.genAICalls.length = 0;
  doc.querySelector('#btn-generate-art').click();
  await waitFor('generator modal from the wizard', () => doc.querySelector('#modal-root .gen-layout'));
  check('generator: opens from the Builder wizard with all three templates',
    ['mosaic', 'hero', 'titlecard'].every((t) => !!doc.querySelector(`#modal-root [data-gen-template="${t}"]`)));
  await waitFor('generator first preview', () => H.genCalls.length >= 1);
  check('generator: the first preview is a Mosaic in the default All — Flower layout carrying the draft members',
    H.genCalls[0].template === 'mosaic' && H.genCalls[0].grid === 'flower' && Array.isArray(H.genCalls[0].memberKeys),
    JSON.stringify(H.genCalls[0]));
  doc.querySelector('#modal-root [data-gen-grid="spiral"]').click();
  await waitFor('generator spiral preview', () => H.genCalls.some((c) => c.grid === 'spiral'));
  check('generator: switching to All — Spiral re-renders with grid=spiral',
    H.genCalls.some((c) => c.template === 'mosaic' && c.grid === 'spiral'));
  doc.querySelector('#modal-root [data-gen-grid="3x3"]').click();
  await softWait(() => (doc.querySelector('#gen-error')?.textContent || '').length > 0);
  check('generator: a fixed grid over too few members shows the shortfall plainly and cannot be applied',
    (doc.querySelector('#gen-error')?.textContent || '').includes('needs 9 posters') &&
    doc.querySelector('#gen-apply').disabled === true,
    JSON.stringify(doc.querySelector('#gen-error')?.textContent || ''));
  doc.querySelector('#modal-root [data-gen-template="hero"]').click();
  await waitFor('generator hero preview', () => H.genCalls.some((c) => c.template === 'hero'));
  const heroCall = H.genCalls.filter((c) => c.template === 'hero').pop();
  check('generator: the Hero preview carries a member background plus blur/focal controls',
    !!heroCall.background && heroCall.background.kind === 'member' &&
    typeof heroCall.blur === 'number' && typeof heroCall.focalX === 'number',
    JSON.stringify(heroCall));
  doc.querySelector('#modal-root input[name="gen-bg"][value="ai"]').click();
  doc.querySelector('#modal-root input[name="gen-bg"][value="ai"]').dispatchEvent(new window.Event('change', { bubbles: true }));
  await waitFor('generator AI guidance', () => { const g = doc.querySelector('#gen-ai-guidance'); return g && !g.hidden; });
  check('generator: with no AI key configured, the AI panel shows the Settings pointer instead of a dead form',
    (doc.querySelector('#gen-ai-guidance')?.textContent || '').includes('Settings'));
  doc.querySelector('#modal-root [data-close="cancel"]').click();
  await softWait(() => !doc.querySelector('#modal-root .modal'));
  // With a key configured (stub flip), the AI form generates and the
  // result becomes the background of the next preview.
  const metaNoAi = window.pcm.metaStatus;
  window.pcm.metaStatus = () => Promise.resolve({ ok: true, tmdb: false, traktClientId: false, traktClientSecret: false,
    traktConnected: false, omdb: false, fanart: false, ai: true, aiBaseUrl: 'https://example.invalid/v1', aiModel: 'chat-x', storeBackend: 'file' });
  doc.querySelector('#btn-generate-art').click();
  await waitFor('generator modal from the wizard (AI pass)', () => doc.querySelector('#modal-root .gen-layout'));
  doc.querySelector('#modal-root [data-gen-template="hero"]').click();
  await waitFor('generator hero preview (AI pass)', () => H.genCalls.some((c) => c.template === 'hero'));
  doc.querySelector('#modal-root input[name="gen-bg"][value="ai"]').click();
  doc.querySelector('#modal-root input[name="gen-bg"][value="ai"]').dispatchEvent(new window.Event('change', { bubbles: true }));
  await waitFor('generator AI form', () => { const f = doc.querySelector('#gen-ai-form'); return f && !f.hidden; });
  doc.querySelector('#gen-ai-prompt').value = 'a foggy pumpkin patch at dusk';
  doc.querySelector('#gen-ai-go').click();
  await waitFor('generator AI generation + re-render', () => H.genAICalls.length >= 1 &&
    H.genCalls.some((c) => c.background && c.background.kind === 'asset' && c.background.assetId === 'gen-ai-asset'));
  check('generator: Generate with AI calls the bridge with the prompt and the result becomes the background',
    H.genAICalls.length === 1 && H.genAICalls[0].prompt === 'a foggy pumpkin patch at dusk' &&
    H.genAICalls[0].model === '@cf/black-forest-labs/flux-1-schnell',
    JSON.stringify(H.genAICalls));
  await waitFor('generator apply enabled (AI pass)', () => { const b = doc.querySelector('#gen-apply'); return b && !b.disabled; });
  doc.querySelector('#gen-apply').click();
  await softWait(() => !doc.querySelector('#modal-root .modal'));
  check('generator: Use in draft lands the render in the Builder art slot',
    (doc.querySelector('#b-poster')?.value || '').includes('Generated poster') &&
    window.PCMArt.selectedFor(doc.querySelector('#b-poster').value) === 'gen-asset-1',
    JSON.stringify(doc.querySelector('#b-poster')?.value || ''));
  window.pcm.metaStatus = metaNoAi;
  // Manager surface.
  doc.querySelector('#cm-rows [data-cm-gen="201"]').click();
  await waitFor('generator modal from the manager', () => doc.querySelector('#modal-root .gen-layout'));
  await waitFor('generator manager preview', () => H.genCalls.some((c) => String(c.ratingKey) === '201'));
  check('generator: opens from the Collections manager and previews against the collection',
    H.genCalls.some((c) => String(c.ratingKey) === '201' && c.template === 'mosaic'),
    JSON.stringify(H.genCalls.filter((c) => String(c.ratingKey) === '201').pop() || {}));
  await waitFor('generator apply enabled (manager)', () => { const b = doc.querySelector('#gen-apply'); return b && !b.disabled; });
  doc.querySelector('#gen-apply').click();
  await softWait(() => !doc.querySelector('#modal-root .modal'));
  check('generator: applying from the manager calls setPoster with the previewed asset (the backup-gate path)',
    H.setPosterCalls.some((c) => String(c.rk) === '201' && c.assetId === 'gen-asset-1'),
    JSON.stringify(H.setPosterCalls));

  // ---- Poster generator member load (v1.20.1): a getCollectionItems
  // answer of { cancelled:true, items:[] } is NOT an empty collection
  // — it means a newer load elsewhere superseded this one (the
  // server's load-cancellation rule). On the owner's remote servers
  // the generator's load lost that race and rendered "no members"
  // for collections full of titles. Pins: cancelled-first still lands
  // the members (bounded retry); persistently cancelled lands an
  // honest error with a Retry button — never the no-members message;
  // Retry after the interruption clears loads the members.
  const realGetCollectionItems = window.pcm.getCollectionItems;
  const GEN_MEMBERS = [
    { ratingKey: '501', title: 'Recovered One', thumb: '/library/media/501/thumb/1', type: 'movie', year: 2001 },
    { ratingKey: '502', title: 'Recovered Two', thumb: '/library/media/502/thumb/1', type: 'movie', year: 2002 },
  ];
  let genLoadCalls = 0;
  window.pcm.getCollectionItems = () => {
    genLoadCalls++;
    return Promise.resolve(genLoadCalls === 1
      ? { ok: true, cancelled: true, items: [] }
      : { ok: true, items: GEN_MEMBERS });
  };
  doc.querySelector('#cm-rows [data-cm-gen="201"]').click();
  await waitFor('generator modal (cancelled-load pass)', () => doc.querySelector('#modal-root .gen-layout'));
  await softWait(() => doc.querySelectorAll('#modal-root [data-gen-member]').length === 2, 8000);
  check('generator: a cancelled first member load is retried and the members render',
    doc.querySelectorAll('#modal-root [data-gen-member]').length === 2 && genLoadCalls >= 2,
    JSON.stringify({ rows: doc.querySelectorAll('#modal-root [data-gen-member]').length, calls: genLoadCalls,
      membersText: (doc.querySelector('#gen-members')?.textContent || '').trim().slice(0, 120) }));
  check('generator: recovered members are all selected and counted',
    (doc.querySelector('#gen-mem-count')?.textContent || '').includes('2 of 2'),
    JSON.stringify(doc.querySelector('#gen-mem-count')?.textContent || ''));
  doc.querySelector('#modal-root [data-close="cancel"]').click();
  await softWait(() => !doc.querySelector('#modal-root .modal'));
  window.pcm.getCollectionItems = () => Promise.resolve({ ok: true, cancelled: true, items: [] });
  doc.querySelector('#cm-rows [data-cm-gen="201"]').click();
  await waitFor('generator modal (always-cancelled pass)', () => doc.querySelector('#modal-root .gen-layout'));
  await softWait(() => doc.querySelector('#modal-root #gen-load-retry'), 12000);
  check('generator: a member load that never lands shows an error with Retry — never the no-members message',
    !!doc.querySelector('#modal-root #gen-load-retry') &&
    !(doc.querySelector('#gen-members')?.textContent || '').includes('no members to arrange'),
    JSON.stringify((doc.querySelector('#gen-members')?.textContent || '').trim().slice(0, 160)));
  if (doc.querySelector('#modal-root #gen-load-retry')) {
    window.pcm.getCollectionItems = () => Promise.resolve({ ok: true, items: GEN_MEMBERS });
    doc.querySelector('#modal-root #gen-load-retry').click();
    await softWait(() => doc.querySelectorAll('#modal-root [data-gen-member]').length === 2, 8000);
    check('generator: Retry after the interruption loads the members',
      doc.querySelectorAll('#modal-root [data-gen-member]').length === 2,
      JSON.stringify({ rows: doc.querySelectorAll('#modal-root [data-gen-member]').length }));
  }
  doc.querySelector('#modal-root [data-close="cancel"]').click();
  await softWait(() => !doc.querySelector('#modal-root .modal'));
  window.pcm.getCollectionItems = realGetCollectionItems;

  // ---- Cancelled-load audit pins (v1.20.1): the other callers that
  // used to read a cancelled answer's empty list as real members.
  // (a) Collections card Edit — a cancelled fetch must NOT seed the
  // Builder (an empty seed applied later would read as removing
  // every member); it toasts and stays put. A landed fetch seeds.
  const realGetCollections = window.pcm.getCollections;
  window.pcm.getCollections = () => Promise.resolve({ ok: true, collections: [
    { ratingKey: '777', title: 'Edit Pin Shelf', childCount: 2, smart: false, thumb: '', summary: '' } ] });
  window.pcm.getCollectionItems = () => Promise.resolve({ ok: true, cancelled: true, items: [] });
  doc.querySelector('.nav-item[data-tab="collections"]').click();
  doc.querySelector('.section-tab[data-key="1"]').click();
  await waitFor('edit-pin collection card', () => doc.querySelector('.poster-card[data-rk="777"] [data-act="edit"]'));
  doc.querySelector('.poster-card[data-rk="777"] [data-act="edit"]').click();
  await softWait(() => [...doc.querySelectorAll('#toast-root .toast')].some((t) => (t.textContent || '').includes('interrupted')), 5000);
  check('cancelled-loads: card Edit on a cancelled load stays out of the Builder and says why',
    !doc.querySelector('#tab-builder').classList.contains('active') &&
    [...doc.querySelectorAll('#toast-root .toast')].some((t) => (t.textContent || '').includes('interrupted')),
    JSON.stringify({ builderActive: doc.querySelector('#tab-builder').classList.contains('active'),
      toasts: [...doc.querySelectorAll('#toast-root .toast')].map((t) => t.textContent) }));
  window.pcm.getCollectionItems = () => Promise.resolve({ ok: true, items: GEN_MEMBERS });
  doc.querySelector('.poster-card[data-rk="777"] [data-act="edit"]').click();
  await softWait(() => doc.querySelector('#tab-builder').classList.contains('active') &&
    doc.querySelector('#b-title').value === 'Edit Pin Shelf', 5000);
  check('cancelled-loads: card Edit on a landed load seeds the Builder with the members',
    doc.querySelector('#tab-builder').classList.contains('active') &&
    doc.querySelector('#b-title').value === 'Edit Pin Shelf',
    JSON.stringify({ title: doc.querySelector('#b-title').value }));
  window.pcm.getCollections = realGetCollections;
  // (b) Overlays "Overlay" entry (applyToCollection) — a cancelled
  // load must not produce a 0-item scope confirmation; a landed one
  // confirms with the real count.
  window.pcm.getCollectionItems = () => Promise.resolve({ ok: true, cancelled: true, items: [] });
  await window.PCMOverlays.applyToCollection('500', 'Pin Shelf');
  await softWait(() => (doc.querySelector('#overlay-target-detail')?.textContent || '').includes('interrupted'), 5000);
  check('cancelled-loads: applyToCollection on a cancelled load shows no scope confirmation',
    doc.querySelector('#overlay-scope-confirm').hidden === true &&
    (doc.querySelector('#overlay-target-detail')?.textContent || '').includes('interrupted'),
    JSON.stringify({ confirmHidden: doc.querySelector('#overlay-scope-confirm').hidden,
      detail: doc.querySelector('#overlay-target-detail')?.textContent || '' }));
  window.pcm.getCollectionItems = () => Promise.resolve({ ok: true, items: GEN_MEMBERS });
  await window.PCMOverlays.applyToCollection('500', 'Pin Shelf');
  await softWait(() => doc.querySelector('#overlay-scope-confirm').hidden === false, 5000);
  check('cancelled-loads: applyToCollection on a landed load confirms with the real member count',
    doc.querySelector('#overlay-scope-confirm').hidden === false &&
    (doc.querySelector('#overlay-scope-confirm-text')?.textContent || '').includes('2 items'),
    JSON.stringify(doc.querySelector('#overlay-scope-confirm-text')?.textContent || ''));
  doc.querySelector('#btn-scope-cancel').click();
  window.pcm.getCollectionItems = realGetCollectionItems;

  // ---- AI settings persistence (v1.20 hotfix pin): on v1.19,
  // editing the AI base URL + model and then clicking an API key's
  // Save reverted both fields to the pre-save values for the rest of
  // the session — the Save handler refreshed adv.meta before the
  // fields' own (slower) change-saves landed, and renderSettings
  // rewrote the fields from that stale copy. Reproduced pre-fix in
  // this harness's exact flow; the pin is that the fields now keep
  // the edited values through the raced save AND a later re-render.
  doc.querySelector('.nav-item[data-tab="settings"]').click();
  await waitFor('settings AI fields', () => (doc.querySelector('#ai-base-url')?.value || '').length > 0);
  const CF_BASE = 'https://api.cloudflare.com/client/v4/accounts/ACC123/ai/v1';
  const typeAiField = (sel, val) => {
    const el = doc.querySelector(sel);
    el.focus(); el.value = val;
    el.dispatchEvent(new window.Event('input', { bubbles: true }));
    el.dispatchEvent(new window.Event('change', { bubbles: true }));
  };
  doc.querySelector('[data-key="aiKey"]').value = 'cf-token-123';
  typeAiField('#ai-base-url', CF_BASE);
  typeAiField('#ai-model', 'cf-chat-model-x');
  doc.querySelector('[data-save="aiKey"]').click();
  await softWait(() => H.aiStore.base === CF_BASE && H.aiStore.model === 'cf-chat-model-x' && H.aiStore.key === true, 5000);
  await sleep(300);
  check('ai-settings: base URL + model edits survive an API-key Save clicked while they are still saving',
    doc.querySelector('#ai-base-url').value === CF_BASE && doc.querySelector('#ai-model').value === 'cf-chat-model-x',
    JSON.stringify({ base: doc.querySelector('#ai-base-url').value, model: doc.querySelector('#ai-model').value, store: H.aiStore }));
  doc.querySelector('[data-key="aiKey"]').value = '';
  doc.querySelector('[data-save="aiKey"]').click();
  await sleep(500);
  check('ai-settings: a later Settings re-render still shows the saved provider values',
    doc.querySelector('#ai-base-url').value === CF_BASE && doc.querySelector('#ai-model').value === 'cf-chat-model-x',
    JSON.stringify({ base: doc.querySelector('#ai-base-url').value, model: doc.querySelector('#ai-model').value }));

  // ---- v1.21.2 pins ------------------------------------------------
  // (a) AI Save button: type into both fields WITHOUT firing
  // 'change', so the save-on-change handlers never run — the new
  // button is the only path that can commit. It must land BOTH
  // values in the store, give the saved toast, and a later
  // re-render must not revert them (the v1.20 raced flow, through
  // the button this time).
  const SAVE_BASE = 'https://ai.example.test/v1';
  const typeNoChange = (sel, val) => {
    const el = doc.querySelector(sel);
    el.focus(); el.value = val;
    el.dispatchEvent(new window.Event('input', { bubbles: true }));
  };
  typeNoChange('#ai-base-url', SAVE_BASE);
  typeNoChange('#ai-model', 'save-btn-model');
  check('ai-save: typed-but-uncommitted fields have not reached the store yet',
    H.aiStore.base === CF_BASE && H.aiStore.model === 'cf-chat-model-x',
    JSON.stringify(H.aiStore));
  doc.querySelector('#btn-ai-save').click();
  await softWait(() => H.aiStore.base === SAVE_BASE && H.aiStore.model === 'save-btn-model', 5000);
  await sleep(200);
  check('ai-save: the Save button commits BOTH AI fields and toasts the save',
    H.aiStore.base === SAVE_BASE && H.aiStore.model === 'save-btn-model' &&
    doc.querySelector('#ai-base-url').value === SAVE_BASE && doc.querySelector('#ai-model').value === 'save-btn-model' &&
    [...doc.querySelectorAll('#toast-root .toast')].some((t) => (t.textContent || '').includes('AI settings saved.')),
    JSON.stringify({ store: H.aiStore, base: doc.querySelector('#ai-base-url').value, model: doc.querySelector('#ai-model').value }));
  doc.querySelector('[data-key="aiKey"]').value = '';
  doc.querySelector('[data-save="aiKey"]').click();
  await sleep(500);
  check('ai-save: a later Settings re-render does not revert the button-saved values',
    doc.querySelector('#ai-base-url').value === SAVE_BASE && doc.querySelector('#ai-model').value === 'save-btn-model',
    JSON.stringify({ base: doc.querySelector('#ai-base-url').value, model: doc.querySelector('#ai-model').value }));

  // (b) Nav icons (v1.21.2 rider): the six sidebar icons are inline
  // Tabler SVGs now — the raw Unicode glyphs (⏻ for Connect above
  // all) rendered as a tofu box on the owner's Windows machine.
  const navIcons = [...doc.querySelectorAll('.nav-icon')];
  check('nav-icons: all six sidebar icons are inline SVGs, no bare text glyphs',
    navIcons.length === 6 && navIcons.every((s) => s.querySelector('svg') && !(s.textContent || '').trim()),
    JSON.stringify(navIcons.map((s) => s.innerHTML.slice(0, 40))));
  check('nav-icons: the Connect card tile is the plug SVG too — no U+23FB glyph left in the page',
    !!doc.querySelector('.connect-art svg') && !doc.body.innerHTML.includes('⏻'), '');
  doc.querySelector('.nav-item[data-tab="collections"]').click();
  const navSelOK = doc.querySelector('.nav-item[data-tab="collections"]').classList.contains('active');
  doc.querySelector('.nav-item[data-tab="settings"]').click();
  check('nav-icons: the tabs still select (collections, then back to settings)',
    navSelOK && doc.querySelector('.nav-item[data-tab="settings"]').classList.contains('active'), '');

  // (d) Backup & restore + donate (v1.22). The settings tab is active
  // and renderSettings has run by now.
  check('backup: the Backup & restore card renders with export/import controls and the keys opt-ins',
    !!doc.querySelector('#backup-panel') && !!doc.querySelector('#btn-backup-export') &&
    !!doc.querySelector('#btn-backup-import') && !!doc.querySelector('#backup-include-keys') &&
    !!doc.querySelector('#backup-import-keys') && !!doc.querySelector('#backup-file'), '');
  // Export: jsdom has no createObjectURL, so the harness stands one in
  // (the theme exporter has the same dependency in production).
  window.URL.createObjectURL = () => 'blob:backup-stub';
  window.URL.revokeObjectURL = () => {};
  await sleep(50);
  doc.querySelector('#btn-backup-export').click();
  await sleep(100);
  check('backup: export calls the server without keys by default and reports the filename',
    H.exportCalls.length === 1 && H.exportCalls[0] === false &&
    (doc.querySelector('#backup-status').textContent || '').includes('projectionist-settings-backup.json'),
    JSON.stringify({ calls: H.exportCalls, status: doc.querySelector('#backup-status').textContent }));
  doc.querySelector('#backup-include-keys').checked = true;
  doc.querySelector('#btn-backup-export').click();
  await sleep(100);
  check('backup: ticking the opt-in passes includeKeys on the next export',
    H.exportCalls.length === 2 && H.exportCalls[1] === true, JSON.stringify(H.exportCalls));
  // Import: a stand-in file object (jsdom File.text is version-fragile;
  // the handler only calls file.text()).
  const importDoc = { format: 'projectionist-settings-backup', formatVersion: 1,
    settings: { aiBaseUrl: 'https://example.invalid/v1', uiTheme: 'midnight' },
    templates: { 'Seed Template': { name: 'Seed Template', placements: [] } },
    leaving: { 'domtest-machine': { '10': { title: 'Alpha', date: '2027-01-15' } } },
    keys: { tmdb: 'stub-key' } };
  Object.defineProperty(doc.querySelector('#backup-file'), 'files', { value: [{ text: () => Promise.resolve(JSON.stringify(importDoc)) }], configurable: true });
  doc.querySelector('#btn-backup-import').click();
  await sleep(150);
  check('backup: import reports the actual counts and notes the unconfirmed keys',
    (doc.querySelector('#backup-status').textContent || '').includes('Imported 2 settings, 1 template, 1 Leaving Soon date') &&
    (doc.querySelector('#backup-status').textContent || '').includes('Keys in the file were left out'),
    JSON.stringify({ status: doc.querySelector('#backup-status').textContent, calls: H.importCalls }));
  // Donate (v1.23.1 fix): with nothing stored, the widget draws with
  // the author's default username — the v1.22 bug was that Settings
  // DISPLAYED the prefill while the load path passed '' and drew
  // nothing at all.
  check('donate: with nothing stored, the widget draws with the author default',
    H.kofiDraw.length === 1 && H.kofiDraw[0].username === 't4v3rn',
    JSON.stringify(H.kofiDraw));
  // The username field is prefilled with the author's public identity
  // when nothing is stored yet.
  check('donate: the username field is prefilled when nothing is stored',
    doc.querySelector('#donate-username').value === 't4v3rn', JSON.stringify(doc.querySelector('#donate-username').value));
  // Save a username: the widget loader draws with the owner's exact config.
  doc.querySelector('#donate-username').value = 'harness-user';
  doc.querySelector('#btn-donate-save').click();
  await sleep(200);
  const kofiCfg = { 'type': 'floating-chat', 'floating-chat.donateButton.text': 'Donate',
    'floating-chat.donateButton.background-color': '#323842', 'floating-chat.donateButton.text-color': '#fff' };
  check('donate: saving a username draws the Ko-fi widget with the exact owner config',
    H.kofiDraw.length === 2 && H.kofiDraw[1].username === 'harness-user' &&
    JSON.stringify(H.kofiDraw[1].config) === JSON.stringify(kofiCfg),
    JSON.stringify(H.kofiDraw));
  // Toggle (v1.23 rider 1): off means no script and no draw even
  // with the widget present; back on re-draws, which also proves
  // the off-state cleared the drawn marker (otherwise the
  // already-drawn early return would suppress the second draw).
  check('donate: the toggle renders checked by default',
    doc.querySelector('#donate-enabled').checked === true, '');
  doc.querySelector('#donate-enabled').checked = false;
  doc.querySelector('#btn-donate-save').click();
  await sleep(200);
  check('donate: toggle off saves enabled=false and draws nothing',
    H.donateEnabled === false && H.kofiDraw.length === 2 && !doc.querySelector('script[src*="ko-fi"]'),
    JSON.stringify({ enabled: H.donateEnabled, draws: H.kofiDraw.length }));
  doc.querySelector('#donate-enabled').checked = true;
  doc.querySelector('#btn-donate-save').click();
  await sleep(200);
  check('donate: toggle back on re-draws the widget',
    H.donateEnabled === true && H.kofiDraw.length === 3,
    JSON.stringify({ enabled: H.donateEnabled, draws: H.kofiDraw.length }));
  // Failure path: remove the widget stub so the loader must inject the
  // script, then fire its error — the script element goes away, no
  // widget draws, and no error toast surfaces.
  delete window.kofiWidgetOverlay;
  const drawsBefore = H.kofiDraw.length;
  doc.querySelector('.nav-item[data-tab="collections"]').click();
  doc.querySelector('.nav-item[data-tab="settings"]').click();
  await sleep(300);
  const kofiScript = doc.querySelector('script[src*="ko-fi"]');
  check('donate: without the widget present, the loader injects the Ko-fi script on re-render',
    !!kofiScript && kofiScript.src === 'https://storage.ko-fi.com/cdn/scripts/overlay-widget.js', '');
  if (kofiScript) kofiScript.dispatchEvent(new window.Event('error'));
  await sleep(100);
  const toastsNow = [...doc.querySelectorAll('#toast-root .toast')].map((t) => t.textContent || '');
  check('donate: a script load failure leaves no widget, no script element, and no error toast',
    !doc.querySelector('script[src*="ko-fi"]') && H.kofiDraw.length === drawsBefore &&
    !toastsNow.some((t) => /ko-fi|kofi/i.test(t)), JSON.stringify({ draws: H.kofiDraw.length, toasts: toastsNow }));

  // (c) Settings CSS contract: the key-row fix lives in the
  // stylesheet itself — rows wrap, the label keeps a 150px floor,
  // the actions input flexes. (Measured at build time in headless
  // Chrome: before — label column 49–73px on rows with a Clear
  // button, every row overflowing its 313px panel content by up
  // to 109px; after — full-width stacked rows at 313px, single
  // line again at 500px content, zero overflow either way.)
  const cssText = readWeb('styles.css');
  check('settings-css: key rows wrap, the label keeps a 150px floor, the actions input flexes, nav icons are sized',
    /\.key-row \{[^}]*flex-wrap: wrap/.test(cssText) &&
    /\.key-row > div:first-child \{[^}]*min-width: 150px/.test(cssText) &&
    /\.key-actions \.input \{[^}]*min-width: 0/.test(cssText) &&
    /\.nav-icon svg \{/.test(cssText), '');

  // (d) Generator zoom (v1.21.2): the Mosaic pane's Poster zoom
  // slider rides the preview payload as `scale`; Hero payloads
  // carry none (zoom is mosaic-only).
  H.genCalls.length = 0;
  doc.querySelector('#btn-generate-art').click();
  await waitFor('generator modal (zoom pin)', () => doc.querySelector('#modal-root .gen-layout'));
  const zoomEl = doc.querySelector('#gen-zoom');
  check('generator: the Mosaic pane has a Poster zoom slider at 100% (range 50–200)',
    !!zoomEl && zoomEl.min === '50' && zoomEl.max === '200' && zoomEl.value === '100',
    JSON.stringify(zoomEl && { min: zoomEl.min, max: zoomEl.max, value: zoomEl.value }));
  zoomEl.value = '150';
  zoomEl.dispatchEvent(new window.Event('input', { bubbles: true }));
  await waitFor('generator zoom preview', () => H.genCalls.some((c) => c.template === 'mosaic' && c.scale === 1.5));
  check('generator: moving the zoom slider re-previews with scale 1.5',
    H.genCalls.some((c) => c.template === 'mosaic' && c.scale === 1.5),
    JSON.stringify(H.genCalls.map((c) => c.scale)));
  doc.querySelector('#modal-root [data-gen-template="hero"]').click();
  await waitFor('generator hero preview (zoom pin)', () => H.genCalls.some((c) => c.template === 'hero'));
  check('generator: Hero payloads carry no scale (zoom is mosaic-only)',
    H.genCalls.filter((c) => c.template === 'hero').every((c) => !('scale' in c)),
    JSON.stringify(H.genCalls.filter((c) => c.template === 'hero')));
  doc.querySelector('#modal-root [data-close="cancel"]').click();
  await softWait(() => !doc.querySelector('#modal-root .modal'));

  // ---- Simulated reload: the settings store holds a saved custom
  // palette (accent #12ab34); the localStorage mirror is STALE
  // (accent #ff0000). The pre-paint script must paint the mirror;
  // the boot sync must then replace it with the server value.
  const reloadPalette = { ...T.THEMES.cinema.vars, '--amber': '#12ab34' };
  realmOpts = {
    themeStore: { name: 'custom', custom: reloadPalette },
    themeCache: { name: 'custom', vars: { ...reloadPalette, '--amber': '#ff0000' }, scheme: 'dark' },
  };
  const dom2 = new JSDOM(readWeb('index.html'), {
    url: 'http://localhost/',
    runScripts: 'dangerously',
    pretendToBeVisual: true,
    beforeParse: (w) => setupRealm(w, posterImg),
  });
  const doc2 = dom2.window.document;
  check('themes: pre-paint mirror paints the cached palette before any app script runs',
    doc2.documentElement.style.getPropertyValue('--amber').trim() === '#ff0000',
    `pre-paint --amber: ${JSON.stringify(doc2.documentElement.style.getPropertyValue('--amber'))}`);
  dom2.window.eval(dom2.window.__PCM_SRC__);
  await softWait(() => dom2.window.PCMThemes && dom2.window.PCMThemes.current().name === 'custom' &&
    doc2.documentElement.style.getPropertyValue('--amber').trim() === '#12ab34');
  check('themes: after reload the server value wins over the stale mirror (and re-syncs it)',
    dom2.window.PCMThemes.current().name === 'custom' &&
    doc2.documentElement.style.getPropertyValue('--amber').trim() === '#12ab34' &&
    JSON.parse(dom2.window.localStorage.getItem('pcm-theme-v1')).vars['--amber'] === '#12ab34',
    `applied --amber: ${JSON.stringify(doc2.documentElement.style.getPropertyValue('--amber'))}`);
  dom2.window.close();
  realmOpts = {};

  const failed = checks.filter((c) => !c.ok);
  console.log(`\noverlays.dom.test.cjs: ${checks.length - failed.length}/${checks.length} checks passed`);
  window.close();
  process.exit(failed.length ? 1 : 0);
}

main().catch((e) => {
  console.error('overlays.dom.test.cjs: harness error:', e && e.stack || e);
  process.exit(2);
});
