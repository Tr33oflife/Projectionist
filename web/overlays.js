/* ============================================================================
 * renderer/overlays.js — Poster Overlay Studio (rebuilt 2026-10-22).
 * ----------------------------------------------------------------------------
 * FUTURE AI — how this tab works now, and WHY (the owner ordered a complete
 * overhaul of the old studio: "don't try to build on the failure it is"):
 *
 *   STUDIO view = three regions.
 *   - TARGET RAIL (left): browsable poster grids of the server's
 *     collections and items (createBrowser). Clicking a poster makes it
 *     the design target. No typed names, and NO silent catches: a failed
 *     load renders its error inline in the grid it belongs to.
 *   - STAGE (center): the canvas designer + the FAMILIES panel +
 *     custom badge trays. The overlay model is KOMETA-STYLE FAMILIES
 *     (recreated 2026-10-22 at the owner's order): a family is a definition
 *     (composite.js FAMILY_DEFS) judged per item against its facts —
 *     resolution dovetail text, best-audio naming, ≤3 ratings slots,
 *     status ribbon, episode-aggregated languages for shows, … The
 *     layout = enabled families + per-family tweaks (corner/size/
 *     offsets/settings). Hand-placed badges survive as CUSTOM
 *     placements appended to the family layout (custom PNGs + Fanart
 *     logos); families that don't fire for the target don't render,
 *     and the verdicts rail says why.
 *   - TRUTH RAIL (right): the FACTS panel renders overlay:itemFacts for
 *     the target as readable rows — the exact metadata families/badges
 *     are judged against — and the VERDICTS list evaluates every enabled
 *     family and placed custom badge against those facts: FIRES with
 *     the exact text it will draw, or WON'T FIRE with a plain reason
 *     ("needs Dolby Vision — this item's streams are HDR10 only"). A
 *     badge silently vanishing is a bug by definition.
 *
 *   APPLY view = honest three steps (replaces samples → fake dry-run):
 *   1. EVALUATE: fetch facts for EVERY target item (progress shown,
 *      failures collected by title), then composite.evaluateRun()
 *      judges the family layout + custom placements per item — pure,
 *      from the fetched facts.
 *   2. DRY-RUN: rendered from that evaluation — will-rewrite items
 *      with the actual badges/labels, skipped items with reasons,
 *      items whose facts failed in a separate list. Only firing items
 *      count as "will be rewritten".
 *   3. APPLY: composites ONLY the firing items, reusing the evaluated
 *      facts (no refetch), then overlay:apply (the verified backup
 *      gate in main is unchanged). Composite failures are reported by
 *      title. Any change to targets/layout/server since evaluation
 *      invalidates it — apply refuses a stale snapshot.
 *
 *   Facts doctrine (load-bearing, unchanged): missing data means the
 *   badge does not fire. NEVER invent metadata.
 * ========================================================================== */
(() => {
'use strict';
const P = () => window.PCM;
const CMP = () => window.PCMComposite;
const DW = 1000, DH = 1500;   // design canvas (placements are fractional, so this maps to any poster)
// Stage geometry (v1.10 — episode overlays): the design canvas
// follows the TARGET's art — the canonical portrait space above for
// posters, the landscape space for an episode's 16:9 still
// (composite.js geometryFor owns both spaces; the stage just asks).
// Placements and family drawables are fractional, so the same
// design rescales honestly when the dims change.
function dimsForType(t) {
  const g = CMP().geometryFor({ type: t });
  return { w: g.w, h: g.h };
}
function stageDims() {
  return dimsForType((ov.facts && ov.facts.type) || (ov.target && ov.target.type));
}

// Which badges auto-draw a label, and from where. 'value' = the rule's
// label verbatim (facts-v2 shells: resolution combo, content rating,
// show status, language — their text IS data, composed by evaluateRule).
const LABEL_KIND = {
  star: 'rating', imdb: 'rating', tomato: 'rating', popcorn: 'rating',
  pill: 'count', 'ribbon-blank': 'custom',
  'pill-rescombo': 'value', 'pill-content-rating': 'value',
  'ribbon-status': 'value', 'pill-language': 'value',
  'pill-audio': 'value', 'pill-edition': 'value', 'pill-versions': 'value',
};
// Sensible default rule per badge id. Facts v2: the tech badges default
// to REAL stream detection (the owner's complaint: they were manual
// stickers); 'tech-manual' remains selectable for hand-assignment.
function defaultRule(badgeId) {
  // Star is a VALUE-DISPLAY badge (its text is the rating itself), so
  // its default must fire whenever a TMDb rating EXISTS — the >= 7 gate
  // it inherited from the Electron defaults hid it on most of a normal
  // library (the owner live report 2026-10-22: a 6-rated title read
  // "won't fire — TMDb 6 doesn't meet >= 7"). A threshold stays
  // available in the rule editor for anyone who wants one.
  if (badgeId === 'star') return { kind: 'tmdb-rating', op: '>=', value: 0 };
  if (badgeId === 'imdb') return { kind: 'imdb-rating', op: '>=', value: 0 };
  if (badgeId === 'tomato') return { kind: 'rt-rating', op: '>=', value: 0 };
  if (badgeId === 'popcorn') return { kind: 'rt-audience', op: '>=', value: 0 };
  if (badgeId === 'pill') return { kind: 'unwatched' };
  if (badgeId === 'ribbon-new') return { kind: 'is-new', days: 14 };
  if (badgeId === 'badge-4k') return { kind: 'is-4k' };
  if (badgeId === 'badge-hdr') return { kind: 'is-hdr' };
  if (badgeId === 'badge-dolby-vision') return { kind: 'is-dolby-vision' };
  if (badgeId === 'badge-atmos') return { kind: 'is-atmos' };
  if (badgeId === 'badge-dtsx') return { kind: 'is-dtsx' };
  if (badgeId === 'pill-rescombo') return { kind: 'resolution-combo' };
  if (badgeId === 'pill-content-rating') return { kind: 'has-content-rating' };
  if (badgeId === 'ribbon-status') return { kind: 'tv-status-is' };
  if (badgeId === 'pill-language') return { kind: 'language-is' };
  return { kind: 'always' };
}

const ov = {
  packs: null, badgeIndex: {}, target: null, facts: null, factsError: null,
  fanartNote: null, placements: [], sel: null, posterImg: null, badgeImgs: {},
  badgeLoadToasted: new Set(),
  fanart: [], applyItems: [], evaluation: null, seq: 1,
  view: 'studio', applying: false, evaluating: false,
  rail: null, railMachine: null, applyBrowser: null,
  // Apply scopes (v1.8): 'selected' | 'collection' | 'library' | 'tag'.
  // ov.scopeItems is the resolved set for the non-selected scopes;
  // ov.pendingScope holds a resolved set awaiting its one-line confirm.
  scope: 'selected', scopeLabel: '', scopeItems: [], pendingScope: null,
  runCancel: false, restoreSet: null, restoreCancel: false, countRun: 0,
  // Kometa-style families: the layout's primary half. `families` is the
  // COMPLETE settings object (composite.defaultFamilies shape);
  // `familyDrawables` caches the last stage layout for canvas hit-tests.
  families: null, selFamily: null, familyDrawables: [],
};

// GO BUILD: badge pack art is the server's /badges route; the session
// token rides as ?t= because <img> can't send headers.
const badgeUrl = (pack, file) => `/badges/${pack}/${encodeURIComponent(file)}?t=${window.__PCM_TOKEN__ || ''}`;
const uid = () => 'b' + (ov.seq++) + Date.now().toString(36);
function loadImage(url) {
  return new Promise((res, rej) => { const i = new Image(); i.crossOrigin = 'anonymous'; i.onload = () => res(i); i.onerror = () => rej(new Error('image load failed: ' + url)); i.src = url; });
}
// Any layout/target mutation funnels through here: redraw the stage,
// re-judge the verdicts, and void any outstanding apply evaluation.
function touchLayout() { redraw(); renderVerdicts(); invalidateEvaluation(); }

// ---------- browsable poster grids ----------
// createBrowser(host, { collections, items, onPick }) — the one server
// browser used by the Studio rail AND the Apply target chooser: section
// dropdown + title filter + poster grids for collections and items.
// FUTURE AI: every load failure renders INLINE in its own grid area.
// The old picker swallowed errors into empty grids (.catch(() => [])),
// which looks exactly like an empty library — that class of bug is why
// this component exists. Do not reintroduce silent catches here.
// onPick(kind, item) — kind is 'collection' or 'item'.
function createBrowser(host, opts = {}) {
  const showCols = opts.collections !== false;
  const showItems = opts.items !== false;
  const onPick = opts.onPick || (() => {});
  // Drill-down (Studio rail only): a show's cell grows a "Seasons"
  // button that swaps the items grid to that show's seasons, and a
  // season's cell grows an "Episodes" button that swaps to that
  // season's episode rows (v1.10). A picked season or episode is an
  // ordinary item pick whose entry carries its level + parents, so
  // the pick/apply model needs no new paradigm — the drill state is
  // a small stack of two (show → season).
  const drillEnabled = opts.drill === true;
  const b = {
    sections: [], sec: null, cols: [], items: [],
    colsErr: null, itemsErr: null, searchErr: null,
    loading: false, query: '', searched: new Set(),
    selectedKey: null, timer: null, els: null,
    drillShow: null, seasons: [], seasonsErr: null, seasonsLoading: false,
    drillSeason: null, episodes: [], episodesErr: null, episodesLoading: false,
  };
  const match = (it) => !b.query || (it.title || '').toLowerCase().includes(b.query.toLowerCase());

  function mount() {
    host.innerHTML = `
      <div class="ovb-head">
        <select class="input ovb-sec"></select>
        <input class="input ovb-q" placeholder="Filter by title…" autocomplete="off">
      </div>
      ${showCols ? '<p class="tray-label ovb-cols-label" hidden>Collections in this section</p><div class="poster-grid small ovb-cols"></div>' : ''}
      ${showItems ? '<p class="tray-label ovb-items-label" hidden>Items</p><div class="poster-grid small ovb-items"></div><p class="ovb-search-note grid-err" hidden></p>' : ''}`;
    b.els = {
      sec: host.querySelector('.ovb-sec'),
      q: host.querySelector('.ovb-q'),
      colsLabel: host.querySelector('.ovb-cols-label'),
      cols: host.querySelector('.ovb-cols'),
      itemsLabel: host.querySelector('.ovb-items-label'),
      items: host.querySelector('.ovb-items'),
      searchNote: host.querySelector('.ovb-search-note'),
    };
    b.els.sec.addEventListener('change', () => { b.sec = b.els.sec.value; b.selectedKey = null; load(); });
    b.els.q.addEventListener('input', () => {
      b.query = b.els.q.value.trim();
      render();
      // Deep search: beyond the browsed page, ask the server and merge.
      // A failed search is REPORTED under the grid, never swallowed.
      const q = b.query;
      if (!showItems || q.length < 2 || b.searched.has(q.toLowerCase())) return;
      clearTimeout(b.timer);
      b.timer = setTimeout(async () => {
        b.searched.add(q.toLowerCase());
        try {
          const { items } = await P().call(window.pcm.searchLibrary(b.sec, q));
          const have = new Set(b.items.map((i) => String(i.ratingKey)));
          for (const it of items || []) if (!have.has(String(it.ratingKey))) b.items.push(it);
          b.searchErr = null;
        } catch (e) { b.searchErr = e.message || String(e); }
        render();
      }, 350);
    });
  }
  function cellHTML(kind, it, sub) {
    const key = kind + ':' + it.ratingKey;
    return `<div class="poster-card ov-cell ${b.selectedKey === key ? 'selected' : ''}" role="button" tabindex="0" data-kind="${kind}" data-rk="${P().esc(it.ratingKey)}">${P().posterHTML(it, sub)}</div>`;
  }
  // The seasons level of the items grid: a back row, then the show's
  // season posters. Season picks carry level + parent so records and
  // lists can name the season's show without another lookup.
  function seasonPickList() {
    return b.seasons.map((s) => ({
      ...s, type: 'season', level: 'season',
      parentTitle: b.drillShow ? b.drillShow.title : '',
      parentKey: b.drillShow ? String(b.drillShow.ratingKey) : '',
    }));
  }
  // The episodes level: rows, not poster cells — an episode's art is
  // a 16:9 still, so a row shows the still beside its SxxExx code,
  // title and duration. Episode picks carry the FULL identity:
  // level + season (parent) + show (grandparent), plus the season
  // number — the children payload does not state an episode's
  // parentIndex, so it comes from the drilled season itself.
  function episodePickList() {
    return b.episodes.map((ep) => ({
      ...ep, type: 'episode', level: 'episode',
      parentTitle: b.drillSeason ? b.drillSeason.title : '',
      parentKey: b.drillSeason ? String(b.drillSeason.ratingKey) : '',
      parentIndex: b.drillSeason ? b.drillSeason.index : undefined,
      grandparentTitle: b.drillShow ? b.drillShow.title : '',
      grandparentKey: b.drillShow ? String(b.drillShow.ratingKey) : '',
    }));
  }
  function fmtEpDuration(ms) {
    const totalSec = Math.round(ms / 1000);
    const h = Math.floor(totalSec / 3600);
    const m = Math.floor((totalSec % 3600) / 60);
    const sec = totalSec % 60;
    return h > 0
      ? `${h}:${String(m).padStart(2, '0')}:${String(sec).padStart(2, '0')}`
      : `${m}:${String(sec).padStart(2, '0')}`;
  }
  function episodeRowHTML(ep) {
    const key = 'item:' + ep.ratingKey;
    const code = `S${String(ep.parentIndex ?? 0).padStart(2, '0')}E${String(ep.index ?? 0).padStart(2, '0')}`;
    const dur = ep.duration ? fmtEpDuration(ep.duration) : '';
    const still = ep.thumb && P().artSrc(ep.thumb, 200)
      ? `<img src="${P().esc(P().artSrc(ep.thumb, 200))}" alt="" loading="lazy">` : '';
    return `<div class="ov-ep-row ov-cell ${b.selectedKey === key ? 'selected' : ''}" role="button" tabindex="0" data-kind="item" data-rk="${P().esc(ep.ratingKey)}">`
      + `<span class="ov-ep-thumb">${still}</span>`
      + `<span class="ov-ep-main"><span class="ov-ep-code">${P().esc(code)}</span> <span class="ov-ep-title">${P().esc(ep.title || 'Untitled episode')}</span>`
      + `${dur ? `<span class="ov-ep-dur">${P().esc(dur)}</span>` : ''}</span></div>`;
  }
  function episodesHTML() {
    const head = `<div class="ov-drill-bar"><button class="btn ghost small ov-drill-back-ep" type="button">← Seasons</button><span class="muted small">Episodes of ${P().esc(b.drillSeason.title || 'this season')} — ${P().esc(b.drillShow ? b.drillShow.title || '' : '')}</span></div>`;
    if (b.episodesLoading) return head + '<p class="muted small">Loading episodes…</p>';
    if (b.episodesErr) return head + `<p class="grid-err">Couldn't load episodes: ${P().esc(b.episodesErr)}</p>`;
    const list = episodePickList().filter(match);
    if (!list.length) return head + `<p class="muted small">${b.query ? 'No matches.' : 'No episodes in this season.'}</p>`;
    return head + `<div class="ov-ep-list">${list.map(episodeRowHTML).join('')}</div>`;
  }
  function drillHTML() {
    if (b.drillSeason) return episodesHTML();
    const head = `<div class="ov-drill-bar"><button class="btn ghost small ov-drill-back" type="button">← All items</button><span class="muted small">Seasons of ${P().esc(b.drillShow.title || 'this show')}</span></div>`;
    if (b.seasonsLoading) return head + '<p class="muted small">Loading seasons…</p>';
    if (b.seasonsErr) return head + `<p class="grid-err">Couldn't load seasons: ${P().esc(b.seasonsErr)}</p>`;
    const list = seasonPickList().filter(match);
    if (!list.length) return head + `<p class="muted small">${b.query ? 'No matches.' : 'No seasons for this show.'}</p>`;
    return head + list.map((s) => cellHTML('item', s,
      s.leafCount ? `${s.leafCount} episode${s.leafCount === 1 ? '' : 's'}` : 'season')).join('');
  }
  function gridHTML(kind) {
    const isCols = kind === 'collection';
    const err = isCols ? b.colsErr : b.itemsErr;
    if (b.loading) return '<p class="muted small">Loading…</p>';
    if (err) return `<p class="grid-err">Couldn't load ${isCols ? 'collections' : 'items'}: ${P().esc(err)}</p>`;
    if (!isCols && b.drillShow) return drillHTML();
    const list = (isCols ? b.cols : b.items).filter(match);
    if (!list.length) return `<p class="muted small">${b.query ? 'No matches.' : (isCols ? 'No collections in this section.' : 'No items in this section.')}</p>`;
    return list.map((it) => isCols
      ? cellHTML('collection', it, `${it.childCount ?? 0} items${it.smart ? ' · smart' : ''}`)
      : cellHTML('item', it, `${it.type || ''}${it.year ? ' · ' + it.year : ''}`)).join('');
  }
  function render() {
    if (!b.els) return;
    if (showCols) {
      b.els.colsLabel.hidden = !(b.cols.length || b.colsErr || b.loading);
      b.els.cols.innerHTML = gridHTML('collection');
    }
    if (showItems) {
      b.els.itemsLabel.hidden = !(b.items.length || b.itemsErr || b.loading);
      b.els.items.innerHTML = gridHTML('item');
      b.els.searchNote.hidden = !b.searchErr;
      if (b.searchErr) b.els.searchNote.textContent = `Server search failed: ${b.searchErr} — showing browse results only.`;
    }
    host.querySelectorAll('.ov-cell').forEach((el) => {
      const pick = () => {
        const pool = el.dataset.kind === 'collection' ? b.cols
          : (b.drillSeason ? episodePickList() : b.drillShow ? seasonPickList() : b.items);
        const hit = pool.find((x) => String(x.ratingKey) === el.dataset.rk);
        if (hit) onPick(el.dataset.kind, hit);
      };
      el.addEventListener('click', pick);
      el.addEventListener('keydown', (e) => { if (e.key === 'Enter') pick(); });
    });
    // Drill affordances: the back rows while drilled, a Seasons
    // button riding each show cell while at the top level, and an
    // Episodes button riding each season cell while drilled into a
    // show. Rebuilt with every render (the grids are
    // innerHTML-rendered), so they can never point at a stale list.
    if (showItems && drillEnabled && b.els && b.els.items) {
      if (b.drillSeason) {
        host.querySelector('.ov-drill-back-ep')?.addEventListener('click', () => {
          b.drillSeason = null; b.episodes = []; b.episodesErr = null; b.episodesLoading = false;
          render();
        });
      } else if (b.drillShow) {
        host.querySelector('.ov-drill-back')?.addEventListener('click', () => {
          b.drillShow = null; b.seasons = []; b.seasonsErr = null; b.seasonsLoading = false;
          render();
        });
        if (!b.seasonsLoading && !b.seasonsErr) {
          b.els.items.querySelectorAll('.ov-cell').forEach((el) => {
            const s = b.seasons.find((x) => String(x.ratingKey) === el.dataset.rk);
            if (!s) return;
            const btn = document.createElement('button');
            btn.type = 'button';
            btn.className = 'ov-drill-btn';
            btn.textContent = 'Episodes ▸';
            btn.addEventListener('click', (e) => { e.stopPropagation(); drillIntoSeason(s); });
            el.appendChild(btn);
          });
        }
      } else if (!b.loading && !b.itemsErr) {
        b.els.items.querySelectorAll('.ov-cell').forEach((el) => {
          const it = b.items.find((x) => String(x.ratingKey) === el.dataset.rk);
          if (!it || it.type !== 'show') return;
          const btn = document.createElement('button');
          btn.type = 'button';
          btn.className = 'ov-drill-btn';
          btn.textContent = 'Seasons ▸';
          btn.addEventListener('click', (e) => { e.stopPropagation(); drillInto(it); });
          el.appendChild(btn);
        });
      }
    }
  }
  async function drillInto(show) {
    b.drillShow = show; b.seasons = []; b.seasonsErr = null; b.seasonsLoading = true;
    b.drillSeason = null; b.episodes = []; b.episodesErr = null; b.episodesLoading = false;
    render();
    try {
      const { items } = await P().call(window.pcm.getItemChildren(show.ratingKey));
      if (b.drillShow !== show) return; // backed out or re-drilled meanwhile
      b.seasons = (items || []).filter((x) => x.type === 'season');
    } catch (e) {
      if (b.drillShow !== show) return;
      b.seasonsErr = e.message || String(e);
    }
    b.seasonsLoading = false;
    render();
  }
  async function drillIntoSeason(season) {
    b.drillSeason = season; b.episodes = []; b.episodesErr = null; b.episodesLoading = true;
    render();
    try {
      const { items } = await P().call(window.pcm.getItemChildren(season.ratingKey));
      if (b.drillSeason !== season) return; // backed out or re-drilled meanwhile
      b.episodes = (items || []).filter((x) => x.type === 'episode')
        .sort((x, y) => (x.index || 0) - (y.index || 0));
    } catch (e) {
      if (b.drillSeason !== season) return;
      b.episodesErr = e.message || String(e);
    }
    b.episodesLoading = false;
    render();
  }
  async function load() {
    if (!b.sec) return;
    b.loading = true; b.colsErr = null; b.itemsErr = null; b.searchErr = null;
    b.drillShow = null; b.seasons = []; b.seasonsErr = null; b.seasonsLoading = false;
    b.drillSeason = null; b.episodes = []; b.episodesErr = null; b.episodesLoading = false;
    render();
    // Per-grid outcomes: each load resolves into content OR an inline
    // error for its own grid — the other grid is unaffected.
    const jobs = [];
    if (showCols) jobs.push(P().call(window.pcm.getCollections(b.sec)).then(
      (r) => { b.cols = r.collections || []; },
      (e) => { b.cols = []; b.colsErr = e.message || String(e); }));
    if (showItems) jobs.push(P().call(window.pcm.previewFilter(b.sec, '', null)).then(
      (r) => { b.items = r.items || []; },
      (e) => { b.items = []; b.itemsErr = e.message || String(e); }));
    await Promise.all(jobs);
    b.loading = false; b.searched = new Set();
    render();
  }
  async function loadSections() {
    mount();
    let sections;
    try {
      ({ sections } = await P().call(window.pcm.getSections()));
    } catch (e) {
      host.innerHTML = `<p class="grid-err">Couldn't load library sections: ${P().esc(e.message || String(e))}</p><button class="btn ghost small ovb-retry">Retry</button>`;
      host.querySelector('.ovb-retry').addEventListener('click', loadSections);
      return;
    }
    b.sections = sections || [];
    if (!b.sections.length) {
      host.innerHTML = '<p class="muted small">No library sections — connect to a server first.</p>';
      return;
    }
    b.els.sec.innerHTML = b.sections.map((x) => `<option value="${P().esc(x.key)}">${P().esc(x.title)}</option>`).join('');
    b.sec = b.els.sec.value = String(b.sections[0].key);
    load();
  }
  return {
    loadSections,
    reload: load,
    setSelected(kind, ratingKey) {
      b.selectedKey = kind + ':' + ratingKey;
      host.querySelectorAll('.ov-cell').forEach((el) =>
        el.classList.toggle('selected', el.dataset.kind === kind && el.dataset.rk === String(ratingKey)));
    },
  };
}

// ---------- init ----------
function init() {
  document.querySelectorAll('#overlay-subnav .subnav-item').forEach((btn) =>
    btn.addEventListener('click', () => switchView(btn.dataset.view)));
  wireCanvas();
  $('btn-scope-selected').addEventListener('click', () => setScope('selected'));
  $('btn-scope-collection').addEventListener('click', () => setScope('collection'));
  $('btn-scope-library').addEventListener('click', () => setScope('library'));
  $('btn-scope-tag').addEventListener('click', () => setScope('tag'));
  $('btn-scope-start').addEventListener('click', confirmScopeStart);
  $('btn-scope-cancel').addEventListener('click', cancelScopeConfirm);
  $('btn-overlay-apply-now').addEventListener('click', runApplyNow);
  $('btn-overlay-cancel-run').addEventListener('click', () => { ov.runCancel = true; $('overlay-progress-note').textContent = 'Cancelling — the current batch will finish, then the run stops…'; });
  $('btn-overlay-evaluate').addEventListener('click', evaluateTargets);
  $('btn-restore-count').addEventListener('click', countRestoreDefaults);
  $('btn-restore-start').addEventListener('click', startRestoreDefaults);
  $('btn-restore-cancel').addEventListener('click', () => { $('restore-confirm').hidden = true; ov.restoreSet = null; });
  $('btn-restore-cancel-run').addEventListener('click', () => { ov.restoreCancel = true; $('restore-progress-note').textContent = 'Cancelling…'; });
  $('btn-backups-refresh').addEventListener('click', loadBackupList);
  $('backup-level').addEventListener('change', renderBackupList);
  $('btn-backups-restore-all').addEventListener('click', () => {
    const level = $('backup-level') ? $('backup-level').value : '';
    const shown = (ov.backups || []).filter((b) => (!level || b.level === level) && backupRestorable(b));
    if (shown.length) restoreBackups(shown);
  });
  $('btn-leaving-set-picked').addEventListener('click', leavingSetOnPicked);
  $('btn-leaving-set-collection').addEventListener('click', leavingSetOnCollection);
  $('btn-overlay-reapply-check').addEventListener('click', () => reapplyCheck(true));
  $('btn-overlay-save-template').addEventListener('click', saveTemplate);
  $('btn-families-aurora-all').addEventListener('click', () => setAllFamilyVariants('aurora'));
  $('btn-families-classic-all').addEventListener('click', () => setAllFamilyVariants('classic'));
  $('btn-families-signature-all').addEventListener('click', () => setAllFamilyVariants('signature'));
  window.pcm.onOverlayProgress(onApplyProgress);
  // Studio target rail: browsable grids, loaded now and refreshed
  // whenever the selected server changes.
  ov.rail = createBrowser($('ov-browser'), {
    drill: true,
    onPick: (kind, it) => {
      ov.rail.setSelected(kind, it.ratingKey);
      setTarget({ ratingKey: it.ratingKey, title: it.title, thumb: it.thumb, type: kind === 'collection' ? 'collection' : it.type,
        ...(it.level ? { level: it.level, parentTitle: it.parentTitle, parentKey: it.parentKey, index: it.index,
          ...(it.grandparentTitle ? { grandparentTitle: it.grandparentTitle, grandparentKey: it.grandparentKey, parentIndex: it.parentIndex } : {}) } : {}) });
    },
  });
  ov.rail.loadSections();
  ov.railMachine = P().state.machine || null;
  document.querySelector('[data-tab="overlays"]')?.addEventListener('click', () => {
    const m = P().state.machine || null;
    if (ov.rail && m !== ov.railMachine) { ov.railMachine = m; ov.rail.loadSections(); loadRestoreSections(); loadLeaving(); loadBackupList(); }
  });
  loadPacks().then(() => { renderTrays(); renderVerdicts(); renderFamilies(); })
    .catch((e) => { $('overlay-trays').innerHTML = `<p class="grid-err">Couldn't load badge packs: ${P().esc(e.message || String(e))}</p>`; P().toastErr(e); });
  if (!ov.families) ov.families = CMP().defaultFamilies();
  renderFamilies();
  setScope('selected');
  loadRestoreSections();
  loadLeaving();
  loadBackupList();
  renderHistory();
  maybeAutoReapplyCheck();
}

function switchView(v) {
  ov.view = v;
  document.querySelectorAll('#overlay-subnav .subnav-item').forEach((btn) => btn.classList.toggle('active', btn.dataset.view === v));
  ['studio', 'templates', 'apply', 'history'].forEach((x) => { $('overlay-view-' + x).hidden = x !== v; });
  if (v === 'templates') renderTemplates();
  if (v === 'history') renderHistory();
  // The restore card's library list loads again on EVERY arrival at
  // the Apply view: its init-time load can run before a server is
  // connected, and a select that failed once must not stay dead for
  // the whole session (the v1.8.0 live report).
  if (v === 'apply') { loadRestoreSections(); loadLeaving(); loadBackupList(); }
}

// ---------- badge packs ----------
async function loadPacks() {
  const { bundle, custom, customDir } = await P().call(window.pcm.overlayBadgePacks());
  ov.packs = { bundle, custom, customDir };
  ov.badgeIndex = {};
  for (const b of bundle.badges || []) {
    ov.badgeIndex[b.id] = { ...b, pack: 'bundle', src: badgeUrl('bundle', b.file), aspect: b.h / b.w };
  }
  const skipped = [];
  for (const b of custom || []) {
    const src = badgeUrl('custom', b.file);
    try {
      const img = await loadImage(src); // read real dimensions for custom PNGs
      ov.badgeIndex[b.id] = { ...b, pack: 'custom', src, aspect: img.naturalHeight / img.naturalWidth };
    } catch { skipped.push(b.file || b.id); } // named below — never silent
  }
  if (skipped.length) P().toast(`Skipped unreadable custom badge(s): ${skipped.join(', ')}`, 'error');
}
function renderTrays() {
  const host = $('overlay-trays');
  const byCat = {};
  for (const b of Object.values(ov.badgeIndex)) (byCat[b.category] = byCat[b.category] || []).push(b);
  const catLabel = { rating: 'Ratings', tech: 'Technical', status: 'Status', ribbon: 'Ribbons', custom: 'Custom (your PNGs)' };
  host.innerHTML = Object.keys(catLabel).filter((c) => byCat[c]?.length).map((c) => `
    <p class="tray-label">${catLabel[c]}</p>
    <div class="badge-tray">${byCat[c].map((b) => `
      <button class="badge-cell" data-badge="${P().esc(b.id)}" title="${P().esc(b.label)}">
        <img src="${P().esc(b.src)}" alt="${P().esc(b.label)}" loading="lazy"><span>${P().esc(b.label)}</span>
      </button>`).join('')}</div>`).join('')
    + `<p class="tray-label">Fanart logos <span class="muted small">(per-title, needs key)</span></p>
       <div class="badge-tray" id="fanart-tray"><p class="muted small">Pick a poster to load its logos.</p></div>`;
  host.querySelectorAll('.badge-cell').forEach((el) =>
    el.addEventListener('click', () => addBadge(el.dataset.badge)));
}
async function renderFanartTray() {
  const tray = $('fanart-tray');
  if (!tray) return;
  if (!ov.target?.tmdbId) {
    tray.innerHTML = `<p class="muted small">${ov.fanartNote ? P().esc(ov.fanartNote) : 'No TMDb id for this title.'}</p>`;
    return;
  }
  try {
    const { logos, configured } = await P().call(window.pcm.overlayFanartLogos(ov.target.tmdbId, ov.target.type === 'show' ? 'show' : 'movie'));
    if (!configured) { tray.innerHTML = '<p class="muted small">Add a Fanart.tv key in Settings.</p>'; return; }
    ov.fanart = logos || [];
    for (const [i, l] of ov.fanart.entries()) {
      const id = 'fanart-' + i;
      ov.badgeIndex[id] = { id, file: l.url, label: l.kind + (l.lang ? ` (${l.lang})` : ''), category: 'fanart', pack: 'fanart', src: l.url, aspect: 0.5, remote: true };
    }
    tray.innerHTML = ov.fanart.length ? ov.fanart.map((l, i) => `
      <button class="badge-cell" data-badge="fanart-${i}" title="${P().esc(l.kind)}">
        <img src="${P().esc(l.url)}" alt="" loading="lazy"><span>${P().esc(l.kind)}</span>
      </button>`).join('') : '<p class="muted small">No logos found for this title.</p>';
    tray.querySelectorAll('.badge-cell').forEach((el) => el.addEventListener('click', () => addBadge(el.dataset.badge)));
  } catch (e) { tray.innerHTML = `<p class="grid-err">${P().esc(e.message)}</p>`; }
}

// ---------- families panel (Kometa-style, 2026-10-22) ----------
// famAspectOf: shell aspect lookup shared by the stage, the verdicts
// rail and the apply evaluation (stacking heights depend on real art).
function famAspectOf(badgeId) {
  const b = ov.badgeIndex[badgeId];
  return b && b.aspect ? b.aspect : 0.5;
}
function famEntry(id) {
  if (!ov.families) ov.families = CMP().defaultFamilies();
  if (!ov.families[id]) ov.families[id] = { on: false };
  return ov.families[id];
}
// Any family change: restack the stage, re-judge the verdicts, refresh
// the cards, and void any outstanding apply evaluation.
function touchFamilies() { redraw(); renderVerdicts(); renderFamilies(); invalidateEvaluation(); }
// Header bulk switch: every family to one art pack at once (variants
// are per-family settings; this just writes them all, then the usual
// touch path voids any outstanding evaluation).
function setAllFamilyVariants(variant) {
  if (!ov.families) ov.families = CMP().defaultFamilies();
  for (const def of CMP().FAMILY_DEFS) famEntry(def.id).variant = variant;
  touchFamilies();
}
function enabledFamilyCount() {
  if (!ov.families) return 0;
  return CMP().FAMILY_DEFS.filter((d) => CMP().familySettings(ov.families, d.id).on).length;
}
// familiesFromStored rebuilds the COMPLETE working object from a
// stored template layout: families the template doesn't mention are
// OFF in it — what was saved is what you get.
function familiesFromStored(stored) {
  const out = {};
  for (const def of CMP().FAMILY_DEFS) out[def.id] = { on: false };
  for (const [id, entry] of Object.entries(stored || {})) {
    if (CMP().familyDef(id) && entry && typeof entry === 'object') out[id] = { on: true, ...entry };
  }
  return out;
}
function familySettingsHtml(def, s) {
  const cornerOpts = [['tl', 'Top left'], ['tr', 'Top right'], ['bl', 'Bottom left'], ['br', 'Bottom right']]
    .map(([v, l]) => `<option value="${v}" ${s.corner === v ? 'selected' : ''}>${l}</option>`).join('');
  let html = `
    <label class="fam-field">Corner <select class="input" data-fam-corner="${def.id}">${cornerOpts}</select></label>
    <label class="fam-field">Size <input type="range" min="0.5" max="2" step="0.1" value="${s.size}" data-fam-size="${def.id}"> <span class="muted small">${Math.round(s.size * 100)}%</span></label>
    <label class="fam-field">Style <select class="input" data-fam-variant="${def.id}">
      <option value="classic" ${s.variant === 'classic' ? 'selected' : ''}>Classic</option>
      <option value="aurora" ${s.variant === 'aurora' ? 'selected' : ''}>Aurora</option>
      <option value="signature" ${s.variant === 'signature' ? 'selected' : ''}>Signature</option></select></label>`;
  if (def.id === 'audio') {
    html += `
    <label class="fam-field">Detail <select class="input" data-fam-detail>
      <option value="format" ${s.detail === 'format' ? 'selected' : ''}>Format (Dolby Atmos)</option>
      <option value="full" ${s.detail === 'full' ? 'selected' : ''}>Full (format • channels • language)</option></select></label>`;
  }
  if (def.id === 'ratings') {
    const srcOpts = (sel) => ['', 'tmdb', 'imdb', 'rt-critic', 'rt-audience', 'metascore']
      .map((v) => `<option value="${v}" ${sel === v ? 'selected' : ''}>${v === '' ? '— none —' : CMP().RATING_SOURCE_NAMES[v]}</option>`).join('');
    html += [0, 1, 2].map((i) =>
      `<label class="fam-field">Slot ${i + 1} <select class="input" data-fam-slot="${i}">${srcOpts(s.slots[i] || '')}</select></label>`).join('');
  }
  if (def.id === 'ribbon') {
    html += `
    <label class="fam-field">Honor <select class="input" data-fam-honor>
      <option value="rt-fresh" ${s.honor === 'rt-fresh' ? 'selected' : ''}>RT Fresh (≥ 60%)</option>
      <option value="top-rated" ${s.honor === 'top-rated' ? 'selected' : ''}>Top Rated</option>
      <option value="awards" ${s.honor === 'awards' ? 'selected' : ''}>Awards (Oscar / Emmy / Globe via OMDb)</option>
      <option value="off" ${s.honor === 'off' ? 'selected' : ''}>Off</option></select></label>
    <label class="fam-field">Top Rated source <select class="input" data-fam-source>
      <option value="tmdb" ${s.source === 'tmdb' ? 'selected' : ''}>TMDb</option>
      <option value="imdb" ${s.source === 'imdb' ? 'selected' : ''}>IMDb</option></select></label>
    <label class="fam-field">Threshold <input class="input" type="number" min="0" max="10" step="0.1" value="${s.threshold}" data-fam-threshold></label>`;
  }
  if (def.id === 'languages') {
    html += `
    <label class="fam-field">Display <select class="input" data-fam-style>
      <option value="codes" ${s.style === 'codes' ? 'selected' : ''}>Codes (ENG · JPN)</option>
      <option value="class" ${s.style === 'class' ? 'selected' : ''}>Class (SUB / DUB / DUAL)</option>
      <option value="codes-subs" ${s.style === 'codes-subs' ? 'selected' : ''}>Codes + subs</option></select></label>`;
  }
  if (def.id === 'runtime') {
    html += `
    <label class="fam-field">Format <select class="input" data-fam-format>
      <option value="hm" ${s.format === 'hm' ? 'selected' : ''}>Hours + minutes (2H 28M)</option>
      <option value="clock" ${s.format === 'clock' ? 'selected' : ''}>Clock (2:28)</option></select></label>`;
  }
  if (def.id === 'subtitles') {
    html += `
    <label class="fam-field">Mode <select class="input" data-fam-mode>
      <option value="languages" ${s.mode === 'languages' ? 'selected' : ''}>Languages (ENG · SPA)</option>
      <option value="count" ${s.mode === 'count' ? 'selected' : ''}>Track count (4 TRACKS)</option>
      <option value="full" ${s.mode === 'full' ? 'selected' : ''}>Full (languages • count • FORCED • SDH)</option></select></label>`;
  }
  if (def.id === 'watch-status') {
    html += `
    <label class="fam-field"><input type="checkbox" data-fam-show-watched ${s.showWatched ? 'checked' : ''}> Also badge watched titles (WATCHED / ALL WATCHED)</label>`;
  }
  if (def.id === 'new') {
    const winOpts = [7, 30, 90, 365].includes(s.windowDays) ? [7, 30, 90, 365] : [s.windowDays, 7, 30, 90, 365];
    html += `
    <label class="fam-field">Window <select class="input" data-fam-window>
      ${winOpts.map((d) => `<option value="${d}" ${s.windowDays === d ? 'selected' : ''}>Added within ${d} days</option>`).join('')}</select></label>`;
  }
  if (def.id === 'leaving') {
    const winOpts = [7, 30, 90, 365].includes(s.windowDays) ? [7, 30, 90, 365] : [s.windowDays, 7, 30, 90, 365];
    html += `
    <label class="fam-field">Banner <select class="input" data-fam-leaving-mode>
      <option value="soon" ${s.mode === 'date' ? '' : 'selected'}>LEAVING SOON (inside the window)</option>
      <option value="date" ${s.mode === 'date' ? 'selected' : ''}>LEAVING ON the date</option></select></label>
    <label class="fam-field">Soon window <select class="input" data-fam-leaving-window>
      ${winOpts.map((d) => `<option value="${d}" ${s.windowDays === d ? 'selected' : ''}>Leaving within ${d} days</option>`).join('')}</select></label>`;
    html += leavingTargetControlHtml();
  }
  if (def.id === 'streaming') {
    html += `
    <label class="fam-field">Region <input class="input" type="text" maxlength="2" size="3" value="${P().esc(s.region || 'US')}" data-fam-region list="pcm-region-list" placeholder="US">
      <datalist id="pcm-region-list">${['US', 'GB', 'CA', 'AU', 'NZ', 'DE', 'FR', 'ES', 'IT', 'NL', 'SE', 'NO', 'DK', 'BR', 'MX', 'JP', 'IN', 'ZA'].map((c) => `<option value="${c}">`).join('')}</datalist></label>
    <label class="fam-field">Providers <select class="input" data-fam-stream-mode>
      <option value="first" ${s.mode === 'first' ? 'selected' : ''}>Top service only</option>
      <option value="all" ${s.mode === 'all' ? 'selected' : ''}>Up to three</option></select></label>`;
  }
  if (def.id === 'genre') {
    html += `
    <label class="fam-field">Genres <select class="input" data-fam-genre-count>
      <option value="1" ${s.count === 1 ? 'selected' : ''}>First genre</option>
      <option value="2" ${s.count === 2 ? 'selected' : ''}>First two</option></select></label>
    <label class="fam-field">Mode <select class="input" data-fam-genre-mode>
      <option value="first" ${s.mode === 'only' ? '' : 'selected'}>First genre(s)</option>
      <option value="only" ${s.mode === 'only' ? 'selected' : ''}>One genre only</option></select></label>
    <label class="fam-field">Style <select class="input" data-fam-genre-presentation>
      <option value="pill" ${s.presentation === 'banner' ? '' : 'selected'}>Pill</option>
      <option value="banner" ${s.presentation === 'banner' ? 'selected' : ''}>Banner ribbon</option></select></label>`;
    if (s.mode === 'only') {
      html += `
    <label class="fam-field">Genre <input class="input" type="text" data-fam-genre-name value="${P().esc(s.genre || '')}" placeholder="e.g. Horror" size="14"></label>`;
    }
  }
  if (def.id === 'quick-watch') {
    const curQ = typeof s.minutes === 'number' ? s.minutes : 90;
    const optsQ = [45, 60, 90, 120, 150, 180];
    if (!optsQ.includes(curQ)) optsQ.push(curQ);
    html += `
    <label class="fam-field">Threshold <select class="input" data-fam-quick-minutes>
      ${optsQ.sort((a, b) => a - b).map((m) => `<option value="${m}" ${m === curQ ? 'selected' : ''}>Under ${m} minutes</option>`).join('')}</select></label>`;
  }
  if (def.id === 'family') {
    // The family set (v1.15): toggle chips for the shipped rating
    // vocabulary, movie group and TV group. The effective set is
    // the configured one, or the shipped default while the entry
    // carries no ratings key — the first toggle writes the full
    // set (clamped to vocabulary order) onto the entry.
    const famSet = Array.isArray(s.ratings) ? s.ratings : CMP().FAMILY_RATINGS_DEFAULT;
    const chip = (r) => `<button type="button" class="fam-chip${famSet.includes(r) ? ' on' : ''}" data-fam-rating="${r}">${r}</button>`;
    html += `
    <div class="fam-field fam-ratings"><span>Counts as family — Movies</span><span class="fam-chips">${CMP().FAMILY_RATING_GROUPS.movie.map(chip).join('')}</span></div>
    <div class="fam-field fam-ratings"><span>TV</span><span class="fam-chips">${CMP().FAMILY_RATING_GROUPS.tv.map(chip).join('')}</span></div>`;
  }
  if (s.dx || s.dy) {
    html += `<button class="btn ghost small" data-fam-reset="${def.id}">Reset drag offset (${Math.round(s.dx * 100)}%, ${Math.round(s.dy * 100)}%)</button>`;
  }
  return html;
}
// The leaving family's inline date control (v1.15): the Studio
// verdict used to dead-end at "no leaving date is set for this
// title" — but the date lives in the app's own Leaving list, so
// the card for the CURRENT target offers to set or clear it right
// there. Same store, same endpoints as the Apply page's Leaving
// soon card (leavingSet / leavingRemove) — one list, edited from
// either place. Renders only for a top-level movie/show target
// with facts in hand: dates belong to the movie or show, so
// season/episode targets get the family's verdict only. The state
// shown comes from the target's FACTS (the facts layer's own
// leaving lookup), never from a local guess.
function leavingTargetControlHtml() {
  const t = ov.target, f = ov.facts;
  if (!t || !f || ov.factsError) return '';
  if (f.type !== 'movie' && f.type !== 'show') return '';
  let state = 'not set';
  if (f.leavingSet === true) {
    const d = Number(f.leavingDays);
    state = Number.isFinite(d)
      ? `set — leaves ${P().esc(leavingUsDate(f.leavingDate))} · ${P().esc(leavingDaysText({ days: d, expired: d < 0 }))}`
      : `set — leaves ${P().esc(leavingUsDate(f.leavingDate))}`;
  }
  return `
    <div class="fam-leaving">
      <span class="small">Leaving date for this title: <b data-fam-leaving-state>${state}</b></span>
      <label class="fam-field">Date <input type="date" class="input" data-fam-leaving-date value="${f.leavingSet === true ? P().esc(String(f.leavingDate || '')) : ''}"></label>
      <button class="btn ghost small" data-fam-leaving-apply>Set date</button>
      ${f.leavingSet === true ? '<button class="btn ghost small" data-fam-leaving-clear>Clear</button>' : ''}
      <span class="small err-text" data-fam-leaving-note></span>
    </div>`;
}
// Re-judge the CURRENT target from a fresh facts fetch (the
// post-write half of the inline leaving controls, and the same
// refresh the facts panel would do on a re-pick).
async function refreshTargetFacts() {
  const t = ov.target;
  if (!t) return;
  try {
    const { facts } = await P().call(window.pcm.overlayItemFacts(t.ratingKey));
    ov.facts = facts; ov.factsError = null;
  } catch (e) { ov.factsError = e.message || String(e); }
  renderFacts(); renderVerdicts(); renderFamilies(); redraw(); renderSelected();
}
// One inline leaving write (set or clear) against the shared
// Leaving list. Success: the manager's list state takes the
// response's fresh entries (the same list the Apply page renders)
// and the target re-judges from fresh facts, so the banner state
// updates without leaving the Studio. Failure: the reason is
// stated in the card and NOTHING re-renders — the stored state
// stays whatever it was, never a phantom "set".
async function leavingInlineWrite(noteEl, btn, call) {
  if (btn) btn.disabled = true;
  try {
    const { entries } = await P().call(call());
    leavingState.entries = entries || [];
    renderLeavingList();
    await refreshTargetFacts();
    P().toast('Leaving date updated for this title.');
  } catch (e) {
    if (noteEl) noteEl.textContent = e.message || String(e);
    if (btn) btn.disabled = false;
  }
}
function renderFamilies() {
  const host = $('overlay-families');
  if (!host) return;
  if (!ov.families) ov.families = CMP().defaultFamilies();
  host.innerHTML = CMP().FAMILY_DEFS.map((def) => {
    const s = CMP().familySettings(ov.families, def.id);
    let verdict;
    if (!s.on) verdict = '<span class="muted">off</span>';
    else if (ov.factsError) verdict = '<span class="muted">metadata failed — can\'t judge</span>';
    else if (!ov.target) verdict = '<span class="muted">pick a poster to judge it</span>';
    else if (!ov.facts) verdict = '<span class="muted">loading metadata…</span>';
    else {
      // The details card judges with the same runtime dedupe the
      // layout applies: runtime family on ⇒ no runtime part here.
      const effS = def.id === 'details' && CMP().familySettings(ov.families, 'runtime').on
        ? { ...s, __suppressRuntime: true } : s;
      const v = CMP().evaluateFamily(def.id, effS, ov.facts);
      verdict = v.show
        ? `<b class="ok-text">FIRES</b> — will draw “${P().esc(v.text || '')}”`
        : `<span class="muted">won't fire — ${P().esc(v.note || 'condition not met')}</span>`;
    }
    return `<div class="family-card${s.on ? '' : ' off'}">
      <div class="family-head">
        <label class="family-toggle"><input type="checkbox" data-fam-on="${def.id}" ${s.on ? 'checked' : ''}> <b>${P().esc(def.name)}</b></label>
        <span class="family-verdict small">${verdict}</span>
      </div>
      <p class="muted small family-desc">${P().esc(def.description)}</p>
      ${s.on ? `<div class="family-settings">${familySettingsHtml(def, s)}</div>` : ''}
    </div>`;
  }).join('');
  host.querySelectorAll('[data-fam-on]').forEach((el) => el.addEventListener('change', () => {
    famEntry(el.dataset.famOn).on = el.checked;
    touchFamilies();
  }));
  host.querySelectorAll('[data-fam-corner]').forEach((el) => el.addEventListener('change', () => {
    famEntry(el.dataset.famCorner).corner = el.value;
    touchFamilies();
  }));
  host.querySelectorAll('[data-fam-variant]').forEach((el) => el.addEventListener('change', () => {
    famEntry(el.dataset.famVariant).variant = el.value;
    touchFamilies();
  }));
  host.querySelectorAll('[data-fam-size]').forEach((el) => {
    el.addEventListener('input', () => { famEntry(el.dataset.famSize).size = Number(el.value); redraw(); });
    el.addEventListener('change', touchFamilies);
  });
  host.querySelectorAll('[data-fam-slot]').forEach((el) => el.addEventListener('change', () => {
    const cur = CMP().familySettings(ov.families, 'ratings').slots.slice();
    cur[Number(el.dataset.famSlot)] = el.value;
    famEntry('ratings').slots = cur.filter(Boolean);
    touchFamilies();
  }));
  const honor = host.querySelector('[data-fam-honor]');
  if (honor) honor.addEventListener('change', () => { famEntry('ribbon').honor = honor.value; touchFamilies(); });
  const source = host.querySelector('[data-fam-source]');
  if (source) source.addEventListener('change', () => { famEntry('ribbon').source = source.value; touchFamilies(); });
  const thr = host.querySelector('[data-fam-threshold]');
  if (thr) {
    thr.addEventListener('input', () => { famEntry('ribbon').threshold = Number(thr.value); redraw(); renderVerdicts(); });
    thr.addEventListener('change', touchFamilies);
  }
  const style = host.querySelector('[data-fam-style]');
  if (style) style.addEventListener('change', () => { famEntry('languages').style = style.value; touchFamilies(); });
  const detail = host.querySelector('[data-fam-detail]');
  if (detail) detail.addEventListener('change', () => { famEntry('audio').detail = detail.value; touchFamilies(); });
  const format = host.querySelector('[data-fam-format]');
  if (format) format.addEventListener('change', () => { famEntry('runtime').format = format.value; touchFamilies(); });
  const subMode = host.querySelector('[data-fam-mode]');
  if (subMode) subMode.addEventListener('change', () => { famEntry('subtitles').mode = subMode.value; touchFamilies(); });
  const showWatched = host.querySelector('[data-fam-show-watched]');
  if (showWatched) showWatched.addEventListener('change', () => { famEntry('watch-status').showWatched = showWatched.checked; touchFamilies(); });
  const winSel = host.querySelector('[data-fam-window]');
  if (winSel) winSel.addEventListener('change', () => { famEntry('new').windowDays = Number(winSel.value); touchFamilies(); });
  const leavingMode = host.querySelector('[data-fam-leaving-mode]');
  if (leavingMode) leavingMode.addEventListener('change', () => { famEntry('leaving').mode = leavingMode.value; touchFamilies(); });
  const leavingWin = host.querySelector('[data-fam-leaving-window]');
  if (leavingWin) leavingWin.addEventListener('change', () => { famEntry('leaving').windowDays = Number(leavingWin.value); touchFamilies(); });
  const regionIn = host.querySelector('[data-fam-region]');
  if (regionIn) regionIn.addEventListener('change', () => { famEntry('streaming').region = regionIn.value.toUpperCase(); touchFamilies(); });
  const streamMode = host.querySelector('[data-fam-stream-mode]');
  if (streamMode) streamMode.addEventListener('change', () => { famEntry('streaming').mode = streamMode.value; touchFamilies(); });
  const genreCount = host.querySelector('[data-fam-genre-count]');
  if (genreCount) genreCount.addEventListener('change', () => { famEntry('genre').count = Number(genreCount.value); touchFamilies(); });
  const genreMode = host.querySelector('[data-fam-genre-mode]');
  if (genreMode) genreMode.addEventListener('change', () => { famEntry('genre').mode = genreMode.value; touchFamilies(); });
  const genrePres = host.querySelector('[data-fam-genre-presentation]');
  if (genrePres) genrePres.addEventListener('change', () => { famEntry('genre').presentation = genrePres.value; touchFamilies(); });
  const genreName = host.querySelector('[data-fam-genre-name]');
  if (genreName) {
    genreName.addEventListener('input', () => { famEntry('genre').genre = genreName.value; redraw(); renderVerdicts(); });
    genreName.addEventListener('change', touchFamilies);
  }
  const quickMinutes = host.querySelector('[data-fam-quick-minutes]');
  if (quickMinutes) quickMinutes.addEventListener('change', () => { famEntry('quick-watch').minutes = Number(quickMinutes.value); touchFamilies(); });
  host.querySelectorAll('[data-fam-rating]').forEach((el) => el.addEventListener('click', () => {
    const cur = CMP().familySettings(ov.families, 'family');
    const set = (Array.isArray(cur.ratings) ? cur.ratings.slice() : CMP().FAMILY_RATINGS_DEFAULT.slice());
    const r = el.dataset.famRating;
    const i = set.indexOf(r);
    if (i >= 0) set.splice(i, 1); else set.push(r);
    famEntry('family').ratings = CMP().clampFamilyRatings(set);
    touchFamilies();
  }));
  const lvDateIn = host.querySelector('[data-fam-leaving-date]');
  const lvApplyBtn = host.querySelector('[data-fam-leaving-apply]');
  const lvClearBtn = host.querySelector('[data-fam-leaving-clear]');
  const lvNoteEl = host.querySelector('[data-fam-leaving-note]');
  if (lvApplyBtn) lvApplyBtn.addEventListener('click', () => {
    const v = (lvDateIn?.value || '').trim();
    if (!v) { if (lvNoteEl) lvNoteEl.textContent = 'Choose a date first — no date is invented for you.'; return; }
    const t = ov.target;
    if (!t) return;
    leavingInlineWrite(lvNoteEl, lvApplyBtn,
      () => window.pcm.leavingSet([{ ratingKey: String(t.ratingKey), title: t.title || '' }], v));
  });
  if (lvClearBtn) lvClearBtn.addEventListener('click', () => {
    const t = ov.target;
    if (!t) return;
    leavingInlineWrite(lvNoteEl, lvClearBtn,
      () => window.pcm.leavingRemove(String(t.ratingKey)));
  });
  host.querySelectorAll('[data-fam-reset]').forEach((el) => el.addEventListener('click', () => {
    const entry = famEntry(el.dataset.famReset);
    entry.dx = 0; entry.dy = 0;
    touchFamilies();
  }));
}

// ---------- design target + facts + verdicts ----------
// Picking a target fetches its REAL facts up front and shows them.
// A facts failure is a visible state (factsError), not an empty object
// that makes every detection badge quietly disappear.
async function setTarget(t) {
  ov.target = t; ov.placements = []; ov.sel = null; ov.selFamily = null; ov.fanart = [];
  ov.facts = null; ov.factsError = null; ov.fanartNote = null;
  $('overlay-target-name').textContent = t.grandparentTitle
    ? `${t.grandparentTitle} — ${t.parentTitle || 'Season'} — ${t.title || 'Untitled'}`
    : t.parentTitle ? `${t.parentTitle} — ${t.title || 'Untitled'}` : (t.title || 'Untitled');
  renderFacts(); renderVerdicts(); renderFamilies(); renderSelected();
  try { ov.posterImg = await loadImage(P().artSrc(t.thumb, 0)); }
  catch { ov.posterImg = null; P().toast(`Could not load poster art for “${t.title || 'that item'}”.`, 'error'); }
  try {
    const { facts } = await P().call(window.pcm.overlayItemFacts(t.ratingKey));
    ov.facts = facts;
  } catch (e) { ov.factsError = e.message || String(e); }
  // The Fanart tray needs a TMDb id, which lives in the advisor scan
  // cache. If the scan can't answer, the tray says so — no dead end.
  try {
    const scan = await P().call(window.pcm.advisorGetScan());
    const sItem = scan?.items?.find((i) => String(i.ratingKey) === String(t.ratingKey));
    const tm = (sItem?.guids || []).map((x) => String(x)).find((x) => x.startsWith('tmdb://'));
    ov.target.tmdbId = tm ? tm.slice(7) : '';
    if (!ov.target.tmdbId) ov.fanartNote = 'No TMDb id for this title.';
  } catch { ov.target.tmdbId = ''; ov.fanartNote = 'Couldn\'t resolve a TMDb id — the advisor scan is unavailable.'; }
  renderFanartTray();
  renderFacts(); renderVerdicts(); renderFamilies();
  redraw(); renderSelected();
  syncItemTarget();
}
// The "Apply" scope IS the Studio target: keep it in sync so the two
// views can never disagree.
function syncItemTarget() {
  if (ov.scope !== 'selected') return;
  ov.applyItems = ov.target ? [ov.target] : [];
  updateApplyButton();
  updateLeavingPickedNote();
  invalidateEvaluation();
}

function renderFacts() {
  const host = $('overlay-facts');
  if (!ov.target) { host.innerHTML = '<p class="muted small">Pick a poster to see the metadata badges are judged against.</p>'; return; }
  if (ov.factsError) {
    host.innerHTML = `<p class="grid-err">Couldn't load metadata for “${P().esc(ov.target.title || 'this item')}”: ${P().esc(ov.factsError)}</p>
      <p class="muted small">Without facts, detection badges can't be judged — nothing is guessed in their place.</p>`;
    return;
  }
  const f = ov.facts;
  if (!f) { host.innerHTML = '<p class="muted small">Loading metadata…</p>'; return; }
  const dash = '<span class="fact-none">—</span>';
  const plain = (v) => (v == null || v === '' ? dash : P().esc(String(v)));
  const chipRow = (names) => names.length
    ? names.map((n) => `<span class="fact-chip on">${P().esc(n)}</span>`).join('')
    : null;
  const hdrNames = [f.dv && 'Dolby Vision', f.hdr10 && 'HDR10', f.hlg && 'HLG'].filter(Boolean);
  const audioNames = [f.atmos && 'Atmos', f.dtsx && 'DTS:X', f.dtshdma && 'DTS-HD MA', f.truehd && 'TrueHD'].filter(Boolean);
  const videoVal = chipRow(hdrNames) ?? ((f.type === 'show' || f.type === 'season') ? dash : '<span class="muted">none detected (SDR)</span>');
  const audioVal = [f.bestAudioLabel ? P().esc(f.bestAudioLabel) : dash, chipRow(audioNames) || ''].filter(Boolean).join(' ');
  const langList = (arr) => (arr && arr.length ? P().esc(arr.join(', ')) : dash);
  const rows = [
    ['Title', P().esc(f.title || ov.target.title || 'Untitled')],
    ['Type', plain(f.type)],
    ['Resolution', f.resolutionTier ? P().esc(f.resolutionTier) + (f.width ? ` <span class="muted small">(${f.width}px wide)</span>` : '') : dash],
    ['Video', videoVal],
    ['Audio', audioVal],
    ['Best audio detail', [CMP().audioChannelLabel(f.bestAudioChannels), f.bestAudioLang ? String(f.bestAudioLang).toUpperCase() : null].filter(Boolean).join(' · ') || dash],
    ['Source', plain(f.sourceTag)],
    ['Video codec', plain(f.videoCodec)],
    ['Studio', plain(f.studio)],
    ['Genres', f.genres && f.genres.length ? P().esc(f.genres.join(', ')) : dash],
    ['Awards', plain(f.awardsText)],
    ['Year', plain(f.year)],
    ['Runtime', f.runtimeMinutes ? `${f.runtimeMinutes} min${f.type === 'season' ? ' <span class="muted small">(episode total)</span>' : ''}` : dash],
    ['Content rating', plain(f.contentRating)],
    ['Edition', plain(f.editionTitle)],
  ];
  if (f.type === 'show') {
    rows.push(['Show status', plain(f.tvStatus)]);
    rows.push(['Seasons / episodes', [f.seasonCount ? `${f.seasonCount} season(s)` : null, f.showEpisodeCount ? `${f.showEpisodeCount} episode(s)` : null].filter(Boolean).join(' · ') || dash]);
  }
  if (f.type === 'season') {
    rows.push(['From show', ov.target?.parentTitle ? P().esc(ov.target.parentTitle) : dash]);
    rows.push(['Episodes', f.episodeTotal ? `${f.episodeTotal} episode(s)` : dash]);
  }
  if (f.type === 'episode') {
    // An episode's identity comes from the pick (the facts record
    // itself carries no show/season titles worth trusting here):
    // name the show, the season, and the episode's SxxExx code —
    // the season number rides the pick (parentIndex), since the
    // children payload does not state it per episode.
    rows.push(['From show', ov.target?.grandparentTitle ? P().esc(ov.target.grandparentTitle) : dash]);
    rows.push(['Season', ov.target?.parentTitle ? P().esc(ov.target.parentTitle) : dash]);
    if (ov.target?.index != null) {
      rows.push(['Episode', `S${String(ov.target.parentIndex ?? 0).padStart(2, '0')}E${String(ov.target.index).padStart(2, '0')}`]);
    }
  }
  // Streaming row is region-aware: it answers for the region the
  // streaming family is currently set to (facts carry every region;
  // the pick is the layout's, mirrored here so the panel and the
  // badge can never disagree silently).
  const streamRegion = CMP().familySettings(ov.families || {}, 'streaming').region || 'US';
  const streamList = f.streamingByRegion && Array.isArray(f.streamingByRegion[streamRegion]) ? f.streamingByRegion[streamRegion] : [];
  rows.push(
    ['Streaming', streamList.length
      ? P().esc(streamList.map((nm) => CMP().streamingDisplayName(nm)).join(', ')) + ` <span class="muted small">(${P().esc(streamRegion)}, via TMDb)</span>`
      : (f.streamingByRegion ? `<span class="muted">none in ${P().esc(streamRegion)}</span>` : dash)],
    ['Audio languages', langList(f.audioLangs)],
    ['Subtitle languages', langList(f.subtitleLangs)],
    ['Subtitle detail', [f.subtitleCount ? `${f.subtitleCount} track(s)` : null, f.subtitleForced ? 'FORCED' : null, f.subtitleSDH ? 'SDH' : null].filter(Boolean).join(' · ') || dash],
    ['Audio class', f.dubClass && f.dubClass !== 'unknown' ? P().esc(f.dubClass.toUpperCase()) : dash],
    ['Versions', f.mediaCount > 1 ? `${f.mediaCount} file versions` : (f.mediaCount === 1 ? 'single version' : dash)],
    ['Unwatched', f.unwatchedCount == null ? dash : P().esc(String(f.unwatchedCount))],
    ['Added', f.addedAtDays == null ? dash : `${Math.max(0, Math.floor(f.addedAtDays))} day(s) ago`],
    ['TMDb', f.tmdbRating == null ? dash : P().esc(String(f.tmdbRating))],
    ['IMDb', f.imdbRating == null ? dash : P().esc(String(f.imdbRating)) + (f.imdbRatingSource ? ` <span class="muted small">(via ${f.imdbRatingSource === 'plex' ? 'Plex' : 'OMDb'})</span>` : '')],
    ['RT critics', f.rtRating == null ? dash : P().esc(String(f.rtRating)) + '%'],
    ['RT audience', f.rtAudience == null ? dash : P().esc(String(f.rtAudience)) + '%'],
    ['Metascore', plain(f.metascore)],
  );
  // Shows: the language rows above come from EPISODE aggregation
  // (facts.go) — say so, with the scan size and any failure named.
  if (f.type === 'show') {
    if (f.langsFromEpisodes) {
      rows.push(['Episode languages', `aggregated from ${f.episodeCount} episode(s)${f.langsPartial ? ` — first ${f.episodeCount} of ${f.episodeTotal} (cap)` : ''}`]);
    } else if (f.langsError) {
      rows.push(['Episode languages', `<span class="err-text">couldn't read episodes: ${P().esc(f.langsError)}</span>`]);
    }
  }
  // Seasons: EVERY stream fact above is an aggregation over this
  // season's episodes — say so once, with coverage and any failure.
  if (f.type === 'season') {
    if (f.langsFromEpisodes) {
      rows.push(['Season facts', `aggregated from this season's ${f.episodeCount} episode(s)${f.langsPartial ? ` — first ${f.episodeCount} of ${f.episodeTotal} (cap)` : ''}`]);
    } else if (f.langsError) {
      rows.push(['Season facts', `<span class="err-text">couldn't read episodes: ${P().esc(f.langsError)}</span>`]);
    }
  }
  host.innerHTML = `<div class="facts-list">${rows.map(([k, v]) => `<div class="fact-row"><span class="fact-k">${k}</span><span class="fact-v">${v}</span></div>`).join('')}</div>`;
}

// Plain-language reason a badge won't fire, grounded in the facts —
// the difference between "not HDR" and "no data" must be VISIBLE.
function verdictReason(rule, ev, f) {
  switch (rule?.kind) {
    case 'is-dolby-vision':
      if (f.hdr10) return 'needs Dolby Vision — this item\'s streams are HDR10 only';
      if (f.hlg) return 'needs Dolby Vision — this item\'s streams are HLG only';
      return 'needs Dolby Vision — no DV stream in this item';
    case 'is-hdr': return 'needs HDR — this item\'s streams are SDR';
    case 'is-atmos': return f.bestAudioLabel ? `needs Atmos — audio is ${f.bestAudioLabel}` : 'needs Atmos — no audio stream data';
    case 'is-dtsx': return f.bestAudioLabel ? `needs DTS:X — audio is ${f.bestAudioLabel}` : 'needs DTS:X — no audio stream data';
    case 'is-4k': return f.width != null ? `needs a 4K file — this one is ${f.width}px wide` : ev.note;
    case 'tmdb-rating': return f.tmdbRating != null ? `TMDb ${f.tmdbRating} doesn't meet ≥ ${rule.value}` : ev.note;
    case 'imdb-rating': return f.imdbRating != null ? `IMDb ${f.imdbRating} doesn't meet ≥ ${rule.value}` : ev.note;
    case 'rt-rating': return f.rtRating != null ? `RT ${f.rtRating}% doesn't meet ≥ ${rule.value}%` : ev.note;
    case 'rt-audience': return f.rtAudience != null ? `RT audience ${f.rtAudience}% doesn't meet ≥ ${rule.value}%` : ev.note;
    case 'resolution-is': return f.resolutionTier ? `resolution is ${f.resolutionTier}, not ${rule.value}` : ev.note;
    case 'tv-status-is': return f.tvStatus ? `status is ${f.tvStatus}${rule.value ? `, not ${rule.value}` : ''}` : ev.note;
    case 'language-is': return f.dubClass && f.dubClass !== 'unknown' ? `audio class is ${f.dubClass}${rule.value ? `, not ${rule.value}` : ''}` : ev.note;
    case 'unwatched': return 'already watched';
    case 'is-new': return f.addedAtDays != null ? `added ${Math.floor(f.addedAtDays)} day(s) ago — outside the ${rule.days || 14}-day window` : ev.note;
    default: return ev.note || 'condition not met';
  }
}

function renderVerdicts() {
  const host = $('overlay-verdicts');
  if (!host) return;
  if (!ov.target) { host.innerHTML = '<p class="muted small">Verdicts appear once a poster is picked.</p>'; return; }
  if (ov.factsError) { host.innerHTML = '<p class="muted small">No verdicts — metadata failed to load (see above).</p>'; return; }
  if (!ov.facts) { host.innerHTML = '<p class="muted small">Loading…</p>'; return; }
  const f = ov.facts;
  // Families first — one row per ENABLED family, judged by the same
  // pure layout pass the stage and the apply flow use.
  const laid = CMP().layoutFamilyDrawables(ov.families || CMP().defaultFamilies(), f, famAspectOf);
  const famRows = laid.verdicts.map((v) => v.undrawn
    ? `<div class="verdict-row"><span class="verdict-dot off"></span><span class="verdict-name">${P().esc(v.name)}</span><span class="verdict-note muted">won't draw — ${P().esc(v.note || 'below the legibility floor at this size')}</span></div>`
    : v.show
      ? `<div class="verdict-row"><span class="verdict-dot on"></span><span class="verdict-name">${P().esc(v.name)}</span><span class="verdict-note"><b class="ok-text">FIRES</b> — will draw “${P().esc(v.text || '')}”${v.note ? ` <span class="muted">(${P().esc(v.note)})</span>` : ''}</span></div>`
      : `<div class="verdict-row"><span class="verdict-dot off"></span><span class="verdict-name">${P().esc(v.name)}</span><span class="verdict-note muted">won't fire — ${P().esc(v.note || 'condition not met')}</span></div>`);
  const customRows = ov.placements.map((p) => {
    const name = ov.badgeIndex[p.badgeId]?.label || p.badgeId;
    const ev = CMP().evaluateRule(p.rule, f);
    if (ev.show) {
      const draw = ev.label != null ? `will draw “${P().esc(ev.label)}”` : 'will draw the badge art';
      return `<div class="verdict-row"><span class="verdict-dot on"></span><span class="verdict-name">${P().esc(name)}</span><span class="verdict-note"><b class="ok-text">FIRES</b> — ${draw}${ev.note ? ` <span class="muted">(${P().esc(ev.note)})</span>` : ''}</span></div>`;
    }
    return `<div class="verdict-row"><span class="verdict-dot off"></span><span class="verdict-name">${P().esc(name)}</span><span class="verdict-note muted">won't fire — ${P().esc(verdictReason(p.rule, ev, f))}</span></div>`;
  });
  host.innerHTML =
    (famRows.length ? '<p class="tray-label">Families</p>' + famRows.join('') : '<p class="muted small">No families enabled — enable some in the Families panel, or place custom badges.</p>')
    + (customRows.length ? '<p class="tray-label">Custom badges</p>' + customRows.join('') : '');
}

// ---------- designer canvas ----------
function canvasPos(e) {
  const cv = $('overlay-canvas'), r = cv.getBoundingClientRect();
  return { x: (e.clientX - r.left) / r.width * cv.width, y: (e.clientY - r.top) / r.height * cv.height };
}
function hitBadge(px, py) {
  const { w: W, h: H } = stageDims();
  for (let i = ov.placements.length - 1; i >= 0; i--) {
    const p = ov.placements[i], b = ov.badgeIndex[p.badgeId];
    if (!b) continue;
    const rc = CMP().placementRect(p, b.aspect, W, H);
    if (px >= rc.x && px <= rc.x + rc.w && py >= rc.y && py <= rc.y + rc.h) return p;
  }
  return null;
}
// Family drawables are computed by the layout pass (composite.js);
// redraw() caches them on ov.familyDrawables for hit-testing here.
// dims defaults to the stage's; compositeFor passes its own (the
// item's facts decide the composite's geometry, not the stage's).
function familyRect(d, dims) {
  const { w: W, h: H } = dims || stageDims();
  // Signature drawables carry their own height (hFrac of frame
  // height — their width is text-measured, so the shell aspect
  // ratio says nothing about them). Everything else derives height
  // from the shell art's aspect, as before.
  if (d.hFrac) return { x: d.x * W, y: d.y * H, w: d.wFrac * W, h: d.hFrac * H };
  const a = famAspectOf(d.badgeId);
  return { x: d.x * W, y: d.y * H, w: d.wFrac * W, h: d.wFrac * W * a };
}
function hitFamily(px, py) {
  for (let i = ov.familyDrawables.length - 1; i >= 0; i--) {
    const d = ov.familyDrawables[i];
    const rc = familyRect(d);
    if (px >= rc.x && px <= rc.x + rc.w && py >= rc.y && py <= rc.y + rc.h) return d;
  }
  return null;
}
function wireCanvas() {
  const cv = $('overlay-canvas');
  let drag = null;
  cv.addEventListener('pointerdown', (e) => {
    if (!ov.posterImg) return;
    const { x, y } = canvasPos(e);
    const hit = hitBadge(x, y);
    if (hit) {
      const b = ov.badgeIndex[hit.badgeId];
      const { w: W, h: H } = stageDims();
      const rc = CMP().placementRect(hit, b.aspect, W, H);
      drag = { kind: 'placement', p: hit, dx: x - rc.x, dy: y - rc.y };
      ov.sel = hit.uid; ov.selFamily = null;
      cv.setPointerCapture(e.pointerId);
    } else {
      const fd = hitFamily(x, y);
      if (fd) {
        // Grabbing a family badge drags the FAMILY: the move becomes
        // its dx/dy offset (poster fractions) and the stack reflows.
        const s = CMP().familySettings(ov.families, fd.family);
        drag = { kind: 'family', id: fd.family, startDx: s.dx, startDy: s.dy, grabX: x, grabY: y };
        ov.sel = null; ov.selFamily = fd.family;
        cv.setPointerCapture(e.pointerId);
      } else { ov.sel = null; ov.selFamily = null; }
    }
    renderSelected(); redraw();
  });
  cv.addEventListener('pointermove', (e) => {
    if (!drag) return;
    const { x, y } = canvasPos(e);
    const { w: W, h: H } = stageDims();
    if (drag.kind === 'placement') {
      drag.p.x = Math.min(1, Math.max(0, (x - drag.dx) / W));
      drag.p.y = Math.min(1, Math.max(0, (y - drag.dy) / H));
    } else {
      const entry = famEntry(drag.id);
      entry.dx = Math.min(0.3, Math.max(-0.3, drag.startDx + (x - drag.grabX) / W));
      entry.dy = Math.min(0.3, Math.max(-0.3, drag.startDy + (y - drag.grabY) / H));
    }
    redraw();
  });
  // A finished drag changed the layout snapshot — void any evaluation.
  cv.addEventListener('pointerup', () => {
    if (drag) {
      const wasFamily = drag.kind === 'family';
      drag = null;
      invalidateEvaluation();
      if (wasFamily) renderFamilies(); // show the offset + reset button
    }
    renderSelected();
  });
}
async function ensureBadgeImg(badgeId) {
  if (!ov.badgeImgs[badgeId]) ov.badgeImgs[badgeId] = await loadImage(ov.badgeIndex[badgeId].src);
  return ov.badgeImgs[badgeId];
}
function addBadge(badgeId) {
  if (!ov.posterImg) { P().toast('Pick a poster first.', 'error'); return; }
  const b = ov.badgeIndex[badgeId];
  if (!b) return;
  // Default: top-right corner, 22% of frame width.
  const { w: W, h: H } = stageDims();
  const wFrac = 0.22;
  const rc = { w: wFrac * W, h: wFrac * W * b.aspect };
  const { x, y } = CMP().cornerPreset('tr', rc.w, rc.h, W, H);
  const p = { uid: uid(), badgeId, x, y, wFrac, opacity: 1, rule: defaultRule(badgeId), text: '' };
  ov.placements.push(p);
  ov.sel = p.uid;
  renderSelected();
  touchLayout();
}
function resolveText(p, ev) {
  const lk = LABEL_KIND[p.badgeId] || 'none';
  if (lk === 'custom') return p.text || '';
  if (lk === 'rating' || lk === 'count' || lk === 'value') return ev.label || '';
  return '';
}
// compositeFor(item, layout, facts) → base64 PNG of the item's poster
// with the layout composited at full size — the per-item engine of the
// one-click Apply (runApplyNow). It builds EXACTLY the drawables
// redraw() builds for the stage (same layout pass, same drawable
// shapes, same text resolution), so what the stage shows for an item
// is byte-for-byte what Apply writes for it. layout is the
// { families, custom } shape evaluateRun judges. A badge whose art
// fails to load fails THIS item loudly (the run records the reason) —
// compositing without it in silence is the old studio's sin.
async function compositeFor(item, layout, facts) {
  const poster = await loadImage(P().artSrc(item.thumb, 0));
  // The composite renders at the ITEM's geometry: landscape for an
  // episode's still, portrait for posters — the same dims the
  // layout pass used (it derives them from the same facts).
  const dims = dimsForType((facts && facts.type) || item.type);
  const cv = document.createElement('canvas');
  cv.width = dims.w; cv.height = dims.h;
  const drawables = [];
  const laid = CMP().layoutFamilyDrawables(layout.families, facts, famAspectOf);
  for (const d of laid.drawables) {
    if (d.renderMode === 'signature') {
      drawables.push({ signature: d, rect: familyRect(d, dims) });
      continue;
    }
    const b = ov.badgeIndex[d.badgeId];
    if (!b) continue;
    const img = await ensureBadgeImg(d.badgeId);
    drawables.push({ img, rect: familyRect(d, dims), text: d.label || '' });
  }
  for (const p of layout.custom || []) {
    const b = ov.badgeIndex[p.badgeId];
    if (!b) continue;
    const ev = CMP().evaluateRule(p.rule, facts);
    if (!ev.show) continue;
    const img = await ensureBadgeImg(p.badgeId);
    drawables.push({ img, rect: CMP().placementRect(p, b.aspect, dims.w, dims.h), text: resolveText(p, ev) });
  }
  CMP().drawComposite(cv, poster, drawables);
  const blob = await new Promise((res) => cv.toBlob(res, 'image/png'));
  if (!blob) throw new Error('canvas produced no image');
  const buf = new Uint8Array(await blob.arrayBuffer());
  let bin = '';
  const CHUNK = 0x8000;
  for (let i = 0; i < buf.length; i += CHUNK) bin += String.fromCharCode.apply(null, buf.subarray(i, i + CHUNK));
  return btoa(bin);
}
async function redraw() {
  const cv = $('overlay-canvas'), ctx = cv.getContext('2d');
  // The stage canvas matches the target's geometry (portrait for
  // posters, landscape for an episode still); resizing clears the
  // canvas, which the rest of this function repaints anyway.
  const { w: W, h: H } = stageDims();
  if (cv.width !== W || cv.height !== H) { cv.width = W; cv.height = H; }
  ctx.clearRect(0, 0, W, H);
  if (!ov.posterImg) {
    ov.familyDrawables = [];
    ctx.fillStyle = '#141828'; ctx.fillRect(0, 0, W, H);
    ctx.fillStyle = '#8a93b2'; ctx.font = '44px system-ui'; ctx.textAlign = 'center';
    ctx.fillText('Pick a poster to start designing', W / 2, H / 2);
    return;
  }
  const drawables = [];
  // Families form the base overlay layer; custom placements draw on
  // top. A family renders only when it FIRES for this target (the
  // verdicts rail explains the ones that don't). The layout pass also
  // feeds the canvas hit-test cache.
  ov.familyDrawables = [];
  if (ov.facts) {
    const laid = CMP().layoutFamilyDrawables(ov.families || CMP().defaultFamilies(), ov.facts, famAspectOf);
    ov.familyDrawables = laid.drawables;
    for (const d of laid.drawables) {
      if (d.renderMode === 'signature') {
        // Procedural badge: no art to load — drawComposite renders
        // it from the spec against the poster itself.
        drawables.push({ signature: d, rect: familyRect(d) });
        continue;
      }
      const b = ov.badgeIndex[d.badgeId];
      if (!b) continue;
      try {
        const img = await ensureBadgeImg(d.badgeId);
        drawables.push({ img, rect: familyRect(d), text: d.label || '' });
      } catch {
        if (!ov.badgeLoadToasted.has(d.badgeId)) {
          ov.badgeLoadToasted.add(d.badgeId);
          P().toast(`Badge art failed to load: ${b.label || d.badgeId} — it will be skipped in composites.`, 'error');
        }
      }
    }
  }
  for (const p of ov.placements) {
    const b = ov.badgeIndex[p.badgeId];
    if (!b) continue;
    const ev = CMP().evaluateRule(p.rule, ov.facts || {});
    if (!ev.show) continue; // didn't fire for THIS target — the verdicts rail says why
    try {
      const img = await ensureBadgeImg(p.badgeId);
      // Fanart logos: learn real aspect on first load.
      if (p.badgeId.startsWith('fanart-') && b.aspect === 0.5 && img.naturalWidth) b.aspect = img.naturalHeight / img.naturalWidth;
      drawables.push({ img, rect: CMP().placementRect(p, b.aspect, W, H), text: resolveText(p, ev) });
    } catch {
      // Badge art failed: say so once per badge instead of silently
      // compositing without it (silent skips are the old studio's sin).
      if (!ov.badgeLoadToasted.has(p.badgeId)) {
        ov.badgeLoadToasted.add(p.badgeId);
        P().toast(`Badge art failed to load: ${b.label || p.badgeId} — it will be skipped in composites.`, 'error');
      }
    }
  }
  CMP().drawComposite(cv, ov.posterImg, drawables);
  // selection outline — custom placement…
  const sel = ov.placements.find((p) => p.uid === ov.sel);
  if (sel) {
    const b = ov.badgeIndex[sel.badgeId];
    if (b) {
      const rc = CMP().placementRect(sel, b.aspect, W, H);
      ctx.strokeStyle = '#b4a0ff'; ctx.lineWidth = 4; ctx.setLineDash([14, 10]);
      ctx.strokeRect(rc.x, rc.y, rc.w, rc.h); ctx.setLineDash([]);
    }
  }
  // …or the selected family (its first drawable carries the outline).
  if (ov.selFamily) {
    const d = ov.familyDrawables.find((x) => x.family === ov.selFamily);
    if (d) {
      const rc = familyRect(d);
      ctx.strokeStyle = '#7fd4ff'; ctx.lineWidth = 4; ctx.setLineDash([14, 10]);
      ctx.strokeRect(rc.x, rc.y, rc.w, rc.h); ctx.setLineDash([]);
    }
  }
}

// ---------- selected-badge panel ----------
function renderSelected() {
  const host = $('overlay-sel'), head = $('overlay-sel-head');
  const p = ov.placements.find((x) => x.uid === ov.sel);
  if (!p) {
    head.hidden = true;
    host.innerHTML = ov.selFamily
      ? `<p class="muted small"><b>${P().esc(CMP().familyDef(ov.selFamily)?.name || ov.selFamily)}</b> family selected — drag it on the canvas to offset it from its stack slot, or change its corner/size in the Families panel.</p>`
      : '<p class="muted small">No badge selected.</p>';
    return;
  }
  head.hidden = false;
  const b = ov.badgeIndex[p.badgeId];
  const ev = CMP().evaluateRule(p.rule, ov.facts || {});
  host.innerHTML = `
    <p><strong>${P().esc(b.label)}</strong></p>
    <p class="muted small">Rule: ${P().esc(describeRule(p.rule))}${ev.note ? ` · <em>${P().esc(ev.note)}</em>` : ''}${!ev.show ? ' · <b>currently hidden for this target</b>' : ''}</p>
    <label class="field">Size <input type="range" id="os-size" min="0.05" max="0.6" step="0.01" value="${p.wFrac}"></label>
    <label class="field">Opacity <input type="range" id="os-op" min="0.1" max="1" step="0.05" value="${p.opacity}"></label>
    <div class="btn-row">
      ${CMP().CORNERS.map((c) => `<button class="btn ghost small" data-corner="${c}">${c.toUpperCase()}</button>`).join('')}
    </div>
    <div class="btn-row">
      <button class="btn ghost small" data-nudge="l">←</button><button class="btn ghost small" data-nudge="r">→</button>
      <button class="btn ghost small" data-nudge="u">↑</button><button class="btn ghost small" data-nudge="d">↓</button>
    </div>
    <label class="field">Rule
      <select id="os-rule" class="input">
        ${RULE_KINDS.map((k) =>
          `<option value="${k}" ${p.rule.kind === k ? 'selected' : ''}>${ruleLabel(k)}</option>`).join('')}
      </select></label>
    <div id="os-rule-params"></div>
    ${(LABEL_KIND[p.badgeId] === 'custom') ? `<label class="field">Text <input id="os-text" class="input" value="${P().esc(p.text || '')}" placeholder="Ribbon text"></label><button class="btn ghost small" id="os-ai-text">AI ideas</button>` : ''}
    <div class="btn-row"><button class="btn danger small" id="os-del">Remove badge</button></div>`;
  $('os-size').addEventListener('input', (e) => { p.wFrac = Number(e.target.value); redraw(); });
  $('os-size').addEventListener('change', invalidateEvaluation);
  $('os-op').addEventListener('input', (e) => { p.opacity = Number(e.target.value); redraw(); });
  $('os-op').addEventListener('change', invalidateEvaluation);
  host.querySelectorAll('[data-corner]').forEach((btn) => btn.addEventListener('click', () => {
    const { w: W, h: H } = stageDims();
    const rc = { w: p.wFrac * W, h: p.wFrac * W * b.aspect };
    Object.assign(p, CMP().cornerPreset(btn.dataset.corner, rc.w, rc.h, W, H));
    touchLayout(); renderSelected();
  }));
  host.querySelectorAll('[data-nudge]').forEach((btn) => btn.addEventListener('click', () => {
    const d = 0.01, n = btn.dataset.nudge;
    if (n === 'l') p.x -= d; if (n === 'r') p.x += d; if (n === 'u') p.y -= d; if (n === 'd') p.y += d;
    p.x = Math.min(1, Math.max(0, p.x)); p.y = Math.min(1, Math.max(0, p.y));
    touchLayout();
  }));
  $('os-rule').addEventListener('change', (e) => { p.rule = ruleFromUI(e.target.value, p.rule); touchLayout(); renderSelected(); });
  renderRuleParams(p);
  const txt = $('os-text');
  if (txt) {
    txt.addEventListener('input', (e) => { p.text = e.target.value; touchLayout(); });
    const aiBtn = $('os-ai-text');
    if (aiBtn) aiBtn.addEventListener('click', () => aiRibbonIdeas(null, (t) => { p.text = t; txt.value = t; touchLayout(); }));
  }
  $('os-del').addEventListener('click', () => {
    ov.placements = ov.placements.filter((x) => x.uid !== p.uid);
    ov.sel = null; renderSelected(); touchLayout();
  });
}
// Every rule kind the designer offers. Detection kinds (facts v2) read
// the item's real Plex streams via overlay:itemFacts — see composite.js.
const RULE_KINDS = [
  'always', 'tmdb-rating', 'imdb-rating', 'rt-rating', 'rt-audience',
  'is-4k', 'is-dolby-vision', 'is-hdr', 'is-atmos', 'is-dtsx',
  'resolution-is', 'resolution-combo', 'has-content-rating', 'is-edition',
  'tv-status-is', 'language-is', 'unwatched', 'is-new', 'tech-manual',
];
const RES_TIERS = ['4K', '1080p', '720p', '576p', '480p', 'SD'];
const TV_STATUSES = ['Returning', 'Airing', 'Ended', 'Canceled'];
const DUB_CLASSES = ['sub', 'dub', 'dual'];
function renderRuleParams(pp) {
  const holder = $('os-rule-params');
  if (!holder) return;
  const numField = (label, val, min, max, step) => {
    holder.innerHTML = `<label class="field">${label} <input id="os-rv" class="input" type="number" min="${min}" max="${max}" step="${step}" value="${val}"></label>`;
    $('os-rv').addEventListener('input', (e) => { pp.rule.value = Number(e.target.value); touchLayout(); });
  };
  const selField = (label, options, val, anyLabel) => {
    holder.innerHTML = `<label class="field">${label} <select id="os-rv" class="input">
      ${anyLabel ? `<option value="" ${!val ? 'selected' : ''}>${anyLabel}</option>` : ''}
      ${options.map((o) => `<option value="${o}" ${val === o ? 'selected' : ''}>${o}</option>`).join('')}</select></label>`;
    $('os-rv').addEventListener('change', (e) => { pp.rule.value = e.target.value; touchLayout(); renderSelected(); });
  };
  if (pp.rule.kind === 'tmdb-rating' || pp.rule.kind === 'imdb-rating') {
    numField('Minimum rating', pp.rule.value ?? 7, 0, 10, 0.5);
  } else if (pp.rule.kind === 'rt-rating' || pp.rule.kind === 'rt-audience') {
    numField('Minimum score (%)', pp.rule.value ?? 0, 0, 100, 1);
  } else if (pp.rule.kind === 'is-new') {
    holder.innerHTML = `<label class="field">Added within (days) <input id="os-rv" class="input" type="number" min="1" max="90" value="${pp.rule.days ?? 14}"></label>`;
    $('os-rv').addEventListener('input', (e) => { pp.rule.days = Number(e.target.value); touchLayout(); });
  } else if (pp.rule.kind === 'resolution-is') {
    selField('Resolution is', RES_TIERS, pp.rule.value || '4K', '');
  } else if (pp.rule.kind === 'tv-status-is') {
    selField('Show status is', TV_STATUSES, pp.rule.value || '', 'Any known status');
  } else if (pp.rule.kind === 'language-is') {
    selField('Audio is', DUB_CLASSES, pp.rule.value || '', 'Any (sub/dub/dual)');
  } else holder.innerHTML = '';
}
function ruleLabel(k) {
  return {
    always: 'Always show', 'tmdb-rating': 'TMDb rating ≥ …', 'imdb-rating': 'IMDb rating ≥ …',
    'rt-rating': 'Rotten Tomatoes ≥ …', 'rt-audience': 'RT audience ≥ …',
    'is-4k': 'Only 4K files', 'is-dolby-vision': 'Only Dolby Vision files', 'is-hdr': 'Only HDR files',
    'is-atmos': 'Only Atmos audio', 'is-dtsx': 'Only DTS:X audio',
    'resolution-is': 'Resolution is …', 'resolution-combo': 'Resolution combo (auto text)',
    'has-content-rating': 'Has content rating (auto text)', 'is-edition': 'Is an edition (auto text)',
    'tv-status-is': 'Show status is …', 'language-is': 'Audio language is …',
    'tech-manual': 'Manual (you decide)', unwatched: 'Only if unwatched', 'is-new': 'Only if recently added',
  }[k] || k;
}
function describeRule(r) {
  if (!r || r.kind === 'always') return 'always';
  if (r.kind === 'tmdb-rating' || r.kind === 'imdb-rating') return `${r.kind === 'tmdb-rating' ? 'TMDb' : 'IMDb'} ≥ ${r.value}`;
  if (r.kind === 'rt-rating' || r.kind === 'rt-audience') return `${r.kind === 'rt-rating' ? 'RT' : 'RT audience'} ≥ ${r.value}%`;
  if (r.kind === 'is-new') return `added < ${r.days || 14}d ago`;
  if (r.kind === 'resolution-is') return `resolution is ${r.value || '4K'}`;
  if (r.kind === 'tv-status-is') return r.value ? `status is ${r.value}` : 'any known show status';
  if (r.kind === 'language-is') return r.value ? `audio is ${r.value}` : 'sub/dub/dual (auto)';
  return ruleLabel(r.kind).toLowerCase();
}
function ruleFromUI(kind, old) {
  const base = { kind };
  if (kind === 'tmdb-rating' || kind === 'imdb-rating') { base.op = '>='; base.value = typeof old?.value === 'number' ? old.value : 7; }
  if (kind === 'rt-rating' || kind === 'rt-audience') { base.op = '>='; base.value = typeof old?.value === 'number' ? old.value : 0; }
  if (kind === 'is-new') base.days = old?.days ?? 14;
  if (kind === 'resolution-is') base.value = typeof old?.value === 'string' && RES_TIERS.includes(old.value) ? old.value : '4K';
  if (kind === 'tv-status-is') base.value = typeof old?.value === 'string' ? old.value : '';
  if (kind === 'language-is') base.value = typeof old?.value === 'string' ? old.value : '';
  return base;
}

// ---------- templates ----------
// A template is a LAYOUT: the family layout (enabled families +
// settings) plus any custom placements. Templates saved before the
// family system existed are LEGACY — badge placements only; they list
// with a "legacy" tag and still load + apply through the placements
// pipeline exactly as saved (loading one turns all families off and
// says so). Templates never write to Plex themselves.
async function saveTemplate() {
  const famCount = enabledFamilyCount();
  if (!famCount && !ov.placements.length) { P().toast('Enable at least one family — or place a custom badge — first.', 'error'); return; }
  const m = P().openModal({
    title: 'Save as template',
    bodyHTML: `<p class="muted small">This layout will be saved: ${famCount} famil${famCount === 1 ? 'y' : 'ies'} enabled${ov.placements.length ? ` + ${ov.placements.length} custom badge(s)` : ''}. Fanart logos can't be templated (they're per-title) and are skipped.</p>
      <label class="field">Template name<input id="tpl-name" class="input" placeholder="e.g. Kometa-style defaults" maxlength="80" autocomplete="off"></label>`,
    footHTML: '<button class="btn ghost" data-close="cancel">Cancel</button><button class="btn primary" id="tpl-save-go">Save template</button>',
  });
  m.root.querySelector('#tpl-save-go').addEventListener('click', async () => {
    const name = m.root.querySelector('#tpl-name').value.trim();
    if (!name) return;
    try {
      // Families are only sent when at least one is enabled — a
      // custom-badges-only design stays a legacy-shaped save.
      const r = await P().call(window.pcm.overlayTemplateSave(name, ov.placements, famCount ? ov.families : null));
      m.close('saved');
      P().toast(r.kind === 'families'
        ? `Template \u201c${name}\u201d saved (${r.families} families${r.placements ? ` + ${r.placements} custom badges` : ''}${r.skippedFanart ? `, ${r.skippedFanart} fanart skipped` : ''}).`
        : `Template \u201c${name}\u201d saved (${r.placements} badges${r.skippedFanart ? `, ${r.skippedFanart} fanart skipped` : ''}).`, 'success');
    } catch (e) { P().toastErr(e); }
  });
  await m.closed;
}

async function renderTemplates() {
  const host = $('overlay-templates');
  host.innerHTML = '<p class="muted">Loading templates\u2026</p>';
  let templates = [];
  try { ({ templates } = await P().call(window.pcm.overlayTemplatesList())); }
  catch (e) { host.innerHTML = `<p class="ai-err">${P().esc(e.message)}</p>`; return; }
  if (!templates.length) {
    host.innerHTML = '<div class="empty"><p>No templates yet.</p><p class="muted">Design a layout in the Studio (families and/or custom badges), then hit <strong>Save as template</strong>.</p></div>';
    return;
  }
  host.innerHTML = templates.map((t) => `
    <div class="template-row">
      <div><strong>${P().esc(t.name)}</strong> ${t.kind === 'legacy'
        ? '<span class="kind-chip">legacy</span>'
        : '<span class="kind-chip">families</span>'}
      <p class="muted small">${t.kind === 'families'
        ? `${t.families} famil${t.families === 1 ? 'y' : 'ies'}${t.placements ? ` + ${t.placements} custom badge(s)` : ''}`
        : `${t.placements} badge(s)`}${t.createdAt ? ` \u00b7 saved ${P().esc(new Date(t.createdAt).toLocaleDateString())}` : ''}</p></div>
      <div class="btn-row">
        <button class="btn primary small" data-tapply="${P().esc(t.name)}">Apply template</button>
        <button class="btn ghost small danger" data-tdel="${P().esc(t.name)}">Delete</button>
      </div>
    </div>`).join('');
  host.querySelectorAll('[data-tapply]').forEach((b) => b.addEventListener('click', () => applyTemplate(b.dataset.tapply)));
  host.querySelectorAll('[data-tdel]').forEach((b) => b.addEventListener('click', async () => {
    const m = await P().openModal({
      title: `Delete template \u201c${b.dataset.tdel}\u201d?`,
      bodyHTML: '<p class="muted">This only deletes the saved layout — no posters are affected.</p>',
      footHTML: '<button class="btn ghost" data-close="no">Cancel</button><button class="btn danger" data-close="yes">Delete</button>',
    }).closed;
    if (m !== 'yes') return;
    try { await P().call(window.pcm.overlayTemplateDelete(b.dataset.tdel)); renderTemplates(); }
    catch (e) { P().toastErr(e); }
  }));
}

// Apply template: load the layout (families + custom placements) into
// the Studio, then jump to the Apply view so the user picks a target
// (one item / collection / section / filtered set) and runs evaluate →
// dry-run → apply. A LEGACY template loads its badge placements with
// all families off — nothing about it changes meaning.
async function applyTemplate(name) {
  let tpl;
  try { tpl = await P().call(window.pcm.overlayTemplateLoad(name)); }
  catch (e) { P().toastErr(e); return; }
  ov.placements = tpl.placements.map((p) => ({ ...p, uid: uid() }));
  ov.sel = null; ov.selFamily = null;
  if (tpl.families) {
    ov.families = familiesFromStored(tpl.families);
    renderSelected();
    touchLayout(); renderFamilies();
    switchView('apply');
    P().toast(`Template \u201c${tpl.name}\u201d loaded (family layout${ov.placements.length ? ` + ${ov.placements.length} custom badges` : ''}) — pick a target below, then Apply overlays.`, 'success');
  } else {
    ov.families = familiesFromStored(null);
    renderSelected();
    touchLayout(); renderFamilies();
    switchView('apply');
    P().toast(`Legacy template \u201c${tpl.name}\u201d loaded (${ov.placements.length} badges) — it predates families, so all families are off. Pick a target below, then Apply overlays.`, 'success');
  }
}

// ---------- apply scopes ----------
// Four scopes, one resolved item list (ov.applyItems). 'selected' is
// the Studio poster. The other three resolve through server
// enumeration (collection members, paged section /all, filtered /all
// for a tag) and then ask for ONE one-line confirmation — a count and
// a click, never a report to read first.
// getCollectionItems answers { cancelled:true, items:[] } when a
// newer load for the same collection supersedes it (the server's
// load-cancellation rule) — that empty list is NOT the collection's
// members, and no caller may proceed on it as if it were. One
// immediate retry covers the common race; callers check .cancelled
// on the result and stop honestly when it persists.
async function loadCollectionItems(ratingKey) {
  let r = await P().call(window.pcm.getCollectionItems(ratingKey));
  if (r && r.cancelled) r = await P().call(window.pcm.getCollectionItems(ratingKey));
  return r || { cancelled: true, items: [] };
}

function slimItems(items) {
  return (items || []).map((i) => ({
    ratingKey: i.ratingKey, title: i.title, thumb: i.thumb, type: i.type,
    // Sub-item identity (seasons, episodes) rides along when present
    // so apply lists, run records and history can name the parents.
    ...(i.level ? { level: i.level, parentTitle: i.parentTitle || '', parentKey: i.parentKey || '',
      ...(i.grandparentTitle ? { grandparentTitle: i.grandparentTitle, grandparentKey: i.grandparentKey || '' } : {}) } : {}),
  }));
}
// How an item/entry reads in lists: seasons name their show first
// ("Show — Season 1"), episodes name show AND season ("Show —
// Season 1 — Title"), top-level items just their title.
function displayTitle(x) {
  if (x && x.grandparentTitle) return `${x.grandparentTitle} — ${x.parentTitle || 'Season'} — ${x.title || 'Untitled'}`;
  return x && x.parentTitle ? `${x.parentTitle} — ${x.title || 'Untitled'}` : ((x && x.title) || 'Untitled');
}
function setApplyItems(items, detailText) {
  ov.applyItems = items;
  if (detailText != null) $('overlay-target-detail').textContent = detailText;
  updateApplyButton();
  updateLeavingPickedNote();
  invalidateEvaluation();
}
function currentScopeItems() {
  return ov.scope === 'selected' ? (ov.target ? [ov.target] : []) : ov.scopeItems;
}
function updateApplyButton() {
  const btn = $('btn-overlay-apply-now');
  if (!btn) return;
  const n = currentScopeItems().length;
  btn.textContent = ov.scope === 'selected' && n ? `Apply to ${n} item${n === 1 ? '' : 's'}` : 'Apply overlays';
  btn.disabled = ov.applying;
}
const SCOPE_BUTTONS = { selected: 'btn-scope-selected', collection: 'btn-scope-collection', library: 'btn-scope-library', tag: 'btn-scope-tag' };
async function setScope(scope) {
  ov.scope = scope;
  ov.pendingScope = null;
  $('overlay-scope-confirm').hidden = true;
  for (const [k, id] of Object.entries(SCOPE_BUTTONS)) $(id).classList.toggle('primary', k === scope);
  const picker = $('overlay-scope-picker');
  picker.innerHTML = '';
  if (scope === 'selected') {
    ov.scopeItems = [];
    setApplyItems(ov.target ? [ov.target] : [], ov.target ? `Picked: ${displayTitle(ov.target)}` : 'Pick a poster in the Studio first.');
    return;
  }
  if (scope === 'collection') {
    // The same browsable grids as the Studio rail: pick a section,
    // then a collection; its members resolve on pick.
    const browser = createBrowser(picker, {
      items: false,
      onPick: async (kind, c) => {
        browser.setSelected(kind, c.ratingKey);
        $('overlay-target-detail').textContent = `Loading “${c.title}”…`;
        try {
          const r = await loadCollectionItems(c.ratingKey);
          if (r.cancelled) {
            $('overlay-target-detail').innerHTML = `<span class="err-text">Loading “${P().esc(c.title)}” was interrupted by a newer load — pick the collection again.</span>`;
            return;
          }
          askScopeConfirm(slimItems(r.items), `collection “${c.title}”`, { type: 'collection', ratingKey: String(c.ratingKey), title: c.title });
        } catch (e) {
          $('overlay-target-detail').innerHTML = `<span class="err-text">Couldn't load “${P().esc(c.title)}”: ${P().esc(e.message)}</span>`;
          P().toastErr(e);
        }
      },
    });
    browser.loadSections();
    return;
  }
  // library + tag share a section picker; tag adds type/value pickers.
  picker.innerHTML = '<p class="muted small">Loading sections…</p>';
  let sections;
  try { ({ sections } = await P().call(window.pcm.getSections())); }
  catch (e) { picker.innerHTML = `<p class="grid-err">Couldn't load sections: ${P().esc(e.message)}</p>`; return; }
  if (!sections?.length) { picker.innerHTML = '<p class="muted small">No library sections — connect to a server first.</p>'; return; }
  if (scope === 'library') {
    picker.innerHTML = `<div class="poster-grid small">${sections.map((x) => `
      <div class="poster-card ov-cell" role="button" tabindex="0" data-k="${P().esc(x.key)}">
        <div class="poster-frame"><div class="poster-fallback">${P().esc((x.title || '?')[0].toUpperCase())}</div></div>
        <p class="poster-title">${P().esc(x.title)}</p><p class="poster-sub"><span class="kind-chip">${P().esc(x.type || 'section')}</span></p>
      </div>`).join('')}</div>`;
    picker.querySelectorAll('[data-k]').forEach((el) => {
      const pick = async () => {
        picker.querySelectorAll('.ov-cell').forEach((c) => c.classList.toggle('selected', c === el));
        const sec = sections.find((x) => String(x.key) === el.dataset.k);
        $('overlay-target-detail').textContent = `Loading “${sec.title}”…`;
        try {
          const { items } = await P().call(window.pcm.getSectionItems(sec.key, '', null));
          askScopeConfirm(slimItems(items), `library “${sec.title}”`, { type: 'section', sectionKey: String(sec.key), title: sec.title });
        } catch (e) {
          $('overlay-target-detail').innerHTML = `<span class="err-text">Couldn't load library “${P().esc(sec.title)}”: ${P().esc(e.message)}</span>`;
          P().toastErr(e);
        }
      };
      el.addEventListener('click', pick);
      el.addEventListener('keydown', (e) => { if (e.key === 'Enter') pick(); });
    });
    return;
  }
  // tag scope: section → tag type → tag value → filtered enumeration.
  picker.innerHTML = `
    <div class="field-row">
      <label class="field">Library <select id="overlay-scope-section" class="input">${sections.map((x) => `<option value="${P().esc(x.key)}">${P().esc(x.title)}</option>`).join('')}</select></label>
      <label class="field">Tag type <select id="overlay-scope-tag-type" class="input"><option value="">Loading…</option></select></label>
      <label class="field">Tag <select id="overlay-scope-tag-value" class="input"><option value="">Pick a type first</option></select></label>
    </div>`;
  const secSel = $('overlay-scope-section'), typeSel = $('overlay-scope-tag-type'), valSel = $('overlay-scope-tag-value');
  const loadTypes = async () => {
    typeSel.innerHTML = '<option value="">Loading…</option>';
    valSel.innerHTML = '<option value="">Pick a type first</option>';
    try {
      const { tagTypes } = await P().call(window.pcm.getSectionTagTypes(secSel.value));
      typeSel.innerHTML = (tagTypes || []).map((t) => `<option value="${P().esc(t.filter)}">${P().esc(t.title)}</option>`).join('') || '<option value="">No tag types</option>';
      if (tagTypes?.length) loadValues();
    } catch (e) { typeSel.innerHTML = '<option value="">Failed to load</option>'; P().toastErr(e); }
  };
  const loadValues = async () => {
    if (!typeSel.value) return;
    valSel.innerHTML = '<option value="">Loading…</option>';
    try {
      const { tags } = await P().call(window.pcm.getSectionTags(secSel.value, typeSel.value));
      valSel.innerHTML = (tags || []).map((t) => `<option value="${P().esc(t.value)}">${P().esc(t.name)}</option>`).join('') || '<option value="">No values</option>';
      if (tags?.length) resolveTagScope();
    } catch (e) { valSel.innerHTML = '<option value="">Failed to load</option>'; P().toastErr(e); }
  };
  const resolveTagScope = async () => {
    if (!typeSel.value || !valSel.value) return;
    const sec = sections.find((x) => String(x.key) === secSel.value);
    const tagName = valSel.selectedOptions[0]?.textContent || valSel.value;
    $('overlay-target-detail').textContent = `Loading ${typeSel.value} “${tagName}”…`;
    try {
      const filterQuery = `${encodeURIComponent(typeSel.value)}=${encodeURIComponent(valSel.value)}`;
      const { items } = await P().call(window.pcm.getSectionItems(secSel.value, filterQuery, null));
      askScopeConfirm(slimItems(items), `${typeSel.selectedOptions[0]?.textContent || typeSel.value} “${tagName}” in “${sec?.title || ''}”`,
        { type: 'tag', sectionKey: String(secSel.value), tagType: typeSel.value, tagValue: valSel.value, tagName });
    } catch (e) {
      $('overlay-target-detail').innerHTML = `<span class="err-text">Couldn't load that tag set: ${P().esc(e.message)}</span>`;
      P().toastErr(e);
    }
  };
  secSel.addEventListener('change', loadTypes);
  typeSel.addEventListener('change', loadValues);
  valSel.addEventListener('change', resolveTagScope);
  loadTypes();
}
// The one-line confirm for the collection / library / tag scopes.
function askScopeConfirm(items, label, target) {
  ov.pendingScope = { items, label, target };
  ov.scopeItems = items;
  $('overlay-scope-confirm-text').textContent = `Apply to ${items.length.toLocaleString()} item${items.length === 1 ? '' : 's'} in ${label}?`;
  $('overlay-scope-confirm').hidden = false;
  $('overlay-target-detail').textContent = `${label}: ${items.length} items — confirm to run, or press Apply overlays after confirming.`;
  updateApplyButton();
}
function cancelScopeConfirm() {
  ov.pendingScope = null;
  $('overlay-scope-confirm').hidden = true;
}
function confirmScopeStart() {
  if (!ov.pendingScope) return;
  const p = ov.pendingScope;
  ov.pendingScope = null;
  $('overlay-scope-confirm').hidden = true;
  ov.scopeItems = p.items;
  ov.scopeLabel = p.label;
  ov.scopeTarget = p.target;
  setApplyItems(p.items, `${p.label}: ${p.items.length} items`);
  runApplyNow();
}

// ---------- preview (optional dry-run) ----------
// Preview is a look-first tool: it fetches facts and judges the layout
// so the user can SEE what would happen. It is never a gate — the
// one-click Apply re-fetches and re-judges at click time, so nothing
// here can go stale in a way that matters. The snapshot below only
// labels the preview itself (server + scope + items + layout) so a
// changed target set marks it out of date.
function evalSnapshot() {
  const fams = CMP().FAMILY_DEFS.map((d) => {
    const s = CMP().familySettings(ov.families, d.id);
    return [d.id, s.on, s.corner, s.size, s.dx, s.dy, s.slots || null, s.honor, s.source, s.threshold, s.style, s.variant, s.detail, s.format, s.mode ?? null, s.showWatched ?? null, s.windowDays ?? null, s.region ?? null, s.count ?? null, s.presentation ?? null, s.genre ?? null, s.minutes ?? null, s.ratings ? s.ratings.join(' ') : null];
  });
  return JSON.stringify([
    P().state.machine,
    ov.scope,
    ov.applyItems.map((i) => String(i.ratingKey)),
    ov.placements.map((p) => [p.badgeId, p.x, p.y, p.wFrac, p.opacity, p.rule, p.text || '']),
    fams,
  ]);
}
function invalidateEvaluation() {
  if (!ov.evaluation) return;
  ov.evaluation = null;
  const host = $('overlay-eval');
  if (host) host.innerHTML = '<p class="muted small">Targets or layout changed — this preview is out of date. Apply always judges fresh at click time.</p>';
}

// PREVIEW: fetch facts for every target (progress + per-item failures
// by title), then judge every placement with the pure evaluateRun().
// Facts failures are data ({ __error }), not exceptions that vanish:
// the item is listed as "couldn't be checked" and is never composited.
async function evaluateTargets() {
  if (ov.evaluating || ov.applying) return;
  if (!enabledFamilyCount() && !ov.placements.length) { P().toast('Enable at least one family — or place a custom badge — first.', 'error'); return; }
  if (!ov.applyItems.length) { P().toast('Resolve a target first.', 'error'); return; }
  ov.evaluating = true;
  $('btn-overlay-evaluate').disabled = true;
  $('overlay-results').innerHTML = '';
  $('overlay-eval').innerHTML = '';
  const snapshot = evalSnapshot();
  const items = ov.applyItems.slice();
  const factsByKey = {};
  const prog = $('overlay-progress'), note = $('overlay-progress-note'), bar = $('overlay-bar-fill');
  prog.hidden = false;
  try {
    for (let i = 0; i < items.length; i++) {
      const it = items[i];
      note.textContent = `Evaluating ${i + 1}/${items.length}: ${it.title}`;
      bar.style.width = Math.round(((i + 1) / items.length) * 100) + '%';
      try {
        const { facts } = await P().call(window.pcm.overlayItemFacts(it.ratingKey));
        factsByKey[String(it.ratingKey)] = facts;
      } catch (e) {
        factsByKey[String(it.ratingKey)] = { __error: e.message || String(e) };
      }
    }
  } finally {
    prog.hidden = true;
    ov.evaluating = false;
    $('btn-overlay-evaluate').disabled = false;
  }
  if (snapshot !== evalSnapshot()) { P().toast('Targets or layout changed while previewing — this preview is out of date (Apply still judges fresh).', 'error'); return; }
  const result = CMP().evaluateRun(items, factsByKey, { families: ov.families, custom: ov.placements }, famAspectOf);
  ov.evaluation = { snapshot, factsByKey, ...result };
  renderEvaluation();
}

// The Preview render, purely from the evaluation. Three lists, always
// labeled: will be rewritten (with the badges/labels each item
// actually earns), skipped (with reasons), couldn't be checked.
function badgeName(id) { return ov.badgeIndex[id]?.label || id; }
function renderEvaluation() {
  const ev = ov.evaluation;
  const host = $('overlay-eval');
  if (!ev) { host.innerHTML = ''; return; }
  const { willApply, skipped, failedFacts } = ev;
  const cap = 60;
  const chips = (badges) => badges.map((b) => b.family
    ? `<span class="eval-badge">${P().esc(CMP().familyDef(b.family)?.name || b.family)}${b.label ? ` · ${P().esc(b.label)}` : ''}</span>`
    : `<span class="eval-badge">${P().esc(badgeName(b.badgeId))}${b.label ? ` · ${P().esc(b.label)}` : ''}</span>`).join(' ');
  const more = (n) => (n > cap ? `<p class="muted small">…and ${n - cap} more</p>` : '');
  host.innerHTML = `
    <div class="dryrun-box">
      <h3>Preview — ${willApply.length} poster${willApply.length === 1 ? '' : 's'} would be rewritten</h3>
      <p class="muted small">${skipped.length} skipped (nothing applies) · ${failedFacts.length} couldn't be checked. This is a look only — nothing was written. “Apply overlays” above runs the real job, judged fresh at click time.</p>
      ${willApply.length ? `<p class="tray-label">Will be rewritten</p>
        <div class="dryrun-list">${willApply.slice(0, cap).map((w) => `
          <div class="dryrun-row"><span>${P().esc(displayTitle(w.item))}</span><span>${chips(w.badges)}</span></div>`).join('')}${more(willApply.length)}</div>`
        : '<p class="muted">No poster earns any badge in this layout — nothing would be written.</p>'}
      ${skipped.length ? `<p class="tray-label">Skipped — nothing applies</p>
        <div class="dryrun-list">${skipped.slice(0, cap).map((s) => `
          <div class="dryrun-row"><span>${P().esc(displayTitle(s.item))}</span><span class="muted small">${P().esc(s.reason)}</span></div>`).join('')}${more(skipped.length)}</div>` : ''}
      ${failedFacts.length ? `<p class="tray-label">Couldn't be checked — metadata failed</p>
        <div class="dryrun-list">${failedFacts.map((s) => `
          <div class="dryrun-row"><span>${P().esc(displayTitle(s.item))}</span><span class="err-text">${P().esc(s.error)}</span></div>`).join('')}</div>` : ''}
      <div class="btn-row">
        <button class="btn ghost" id="btn-overlay-cancel-dry">Close preview</button>
      </div>
    </div>`;
  $('btn-overlay-cancel-dry').addEventListener('click', () => { ov.evaluation = null; host.innerHTML = ''; });
}

// ---------- one-click apply ----------
// ONE action, no gates: facts are fetched and judged AT CLICK TIME,
// will-apply items are composited and submitted in batches of ≤5
// under ONE client-chosen run id (start → append → finish), and the
// run record in History is the single record of the whole action.
// Cancel stops between batches: what is applied stays applied and
// recorded; the run closes as cancelled.
function newClientRunID() {
  const iso = new Date().toISOString().replace(/[:.]/g, '-');
  const rnd = Math.floor(Math.random() * 0xffffff).toString(16).padStart(6, '0');
  return `run-${iso}-${rnd}`;
}
function outcomeEntriesFromRecord(record, extra) {
  const entries = (record?.items || []).map((i) => ({
    ratingKey: i.ratingKey, title: i.title, outcome: i.outcome || (i.ok ? 'applied' : 'failed'), error: i.error || '',
    ...(i.level ? { level: i.level, parentTitle: i.parentTitle || '', ...(i.grandparentTitle ? { grandparentTitle: i.grandparentTitle } : {}) } : {}),
  }));
  return entries.concat(extra || []);
}
function summaryCardHTML(groups, opts) {
  const c = groups.counts;
  const mainLabel = opts.mainLabel || 'applied';
  const mainCount = opts.mainCount ?? c.applied;
  const reasonBlocks = groups.failedByReason.map((g) => `
    <details><summary class="err-text">${P().esc(g.reason)} (${g.items.length})</summary>
      <div class="dryrun-list">${g.items.map((i) => `<div class="dryrun-row"><span>${P().esc(displayTitle(i))}</span></div>`).join('')}</div>
    </details>`).join('');
  const expander = (label, items) => items.length ? `
    <details><summary class="muted">${label} (${items.length})</summary>
      <div class="dryrun-list">${items.map((i) => `<div class="dryrun-row"><span>${P().esc(displayTitle(i))}</span></div>`).join('')}</div>
    </details>` : '';
  return `
    <div class="dryrun-box">
      <h3>${P().esc(opts.title)}${groups.cancelledRun ? ' — cancelled' : ''}</h3>
      <p class="muted small">${mainCount} ${mainLabel} · ${c.failed} failed${c.noOverlay ? ` · ${c.noOverlay} earned no overlay` : ''}${c.unavailable ? ` · ${c.unavailable} couldn't be checked` : ''}${c.cancelled ? ` · ${c.cancelled} not attempted (cancelled)` : ''}${groups.cancelledRun ? ' — items already done stayed done and are recorded in the run.' : ''}</p>
      ${reasonBlocks}
      ${expander('No overlay earned', groups.noOverlay)}
      ${expander("Couldn't be checked", groups.unavailable)}
      <div class="btn-row">
        <button class="btn secondary small" data-summary-history="1">View in History</button>
        ${opts.runId ? `<button class="btn secondary small" data-summary-restore="${P().esc(opts.runId)}">Restore this run</button>` : ''}
      </div>
    </div>`;
}
function wireSummaryCard(host, runId) {
  host.querySelector('[data-summary-history]')?.addEventListener('click', () => switchView('history'));
  const rb = host.querySelector('[data-summary-restore]');
  if (rb) rb.addEventListener('click', async () => {
    try {
      const r = await P().call(window.pcm.overlayRestore({ runId }));
      P().toast(`Restored ${r.okCount}/${r.results.length} posters.`, r.okCount === r.results.length ? 'success' : 'error');
      renderHistory();
    } catch (e) { P().toastErr(e); }
  });
}
async function runApplyNow() {
  if (ov.applying || ov.evaluating) return;
  if (!enabledFamilyCount() && !ov.placements.length) { P().toast('Enable at least one family — or place a custom badge — first.', 'error'); return; }
  // Scope guard: the collection/library/tag scopes must pass their
  // one-line confirm first; the main button cannot bypass it.
  if (ov.scope !== 'selected' && ov.pendingScope) {
    $('overlay-scope-confirm').scrollIntoView({ block: 'nearest' });
    P().toast('Confirm the count first — one click on Apply in the line above.', 'error');
    return;
  }
  const items = currentScopeItems().slice();
  if (!items.length) { P().toast('Nothing to apply to — pick a target first.', 'error'); return; }
  const scopeTarget = ov.scope === 'selected'
    ? { type: 'item', count: items.length }
    : (ov.scopeTarget || { type: ov.scope, count: items.length });
  const layout = {
    families: JSON.parse(JSON.stringify(ov.families)),
    custom: JSON.parse(JSON.stringify(ov.placements)),
  };
  const badgeSet = { name: 'studio', placements: layout.custom, families: layout.families };
  const runId = newClientRunID();
  ov.applying = true;
  ov.runCancel = false;
  $('btn-overlay-cancel-run').hidden = false;
  updateApplyButton();
  $('overlay-results').innerHTML = '';
  $('overlay-eval').innerHTML = '';
  ov.evaluation = null;
  const prog = $('overlay-progress'), note = $('overlay-progress-note'), bar = $('overlay-bar-fill');
  prog.hidden = false;
  const outcomes = [];   // result-only entries: no-overlay / unavailable / failed composite
  let record = null;
  let processed = 0;
  const total = items.length;
  const showProgress = (title) => {
    bar.style.width = Math.round((processed / Math.max(1, total)) * 100) + '%';
    note.textContent = `Applying… ${processed} / ${total} — ${title}`;
  };
  try {
    // Phase 1: facts + judgement at click time, in small groups so
    // progress moves and Cancel stays responsive.
    const factsByKey = {};
    for (let i = 0; i < items.length; i++) {
      if (ov.runCancel) break;
      const it = items[i];
      note.textContent = `Checking ${i + 1}/${total}: ${it.title}`;
      bar.style.width = Math.round(((i + 1) / total) * 50) + '%';
      try {
        const { facts } = await P().call(window.pcm.overlayItemFacts(it.ratingKey));
        factsByKey[String(it.ratingKey)] = facts;
      } catch (e) {
        factsByKey[String(it.ratingKey)] = { __error: e.message || String(e) };
      }
    }
    const judged = CMP().evaluateRun(items, factsByKey, layout, famAspectOf);
    const willApply = judged.willApply;
    const levelOf = (it) => (it.level ? { level: it.level, parentTitle: it.parentTitle || '', ...(it.grandparentTitle ? { grandparentTitle: it.grandparentTitle } : {}) } : {});
    for (const sk of judged.skipped) outcomes.push({ ratingKey: String(sk.item.ratingKey), title: sk.item.title, outcome: 'no-overlay', error: '', ...levelOf(sk.item) });
    for (const ff of judged.failedFacts) outcomes.push({ ratingKey: String(ff.item.ratingKey), title: ff.item.title, outcome: 'unavailable', error: ff.error || '', ...levelOf(ff.item) });
    processed = judged.skipped.length + judged.failedFacts.length;
    // Phase 2: open the run, then composite + submit in batches of ≤5.
    const startRes = await P().call(window.pcm.overlayApply([], badgeSet, scopeTarget, { runId, runPhase: 'start' }));
    record = startRes.record;
    const BATCH = 5;
    let idx = 0;
    while (idx < willApply.length) {
      if (ov.runCancel) break;
      const slice = willApply.slice(idx, idx + BATCH);
      const jobs = [];
      for (const w of slice) {
        showProgress(w.item.title);
        try {
          const pngBase64 = await compositeFor(w.item, layout, factsByKey[String(w.item.ratingKey)]);
          jobs.push({ ratingKey: w.item.ratingKey, title: w.item.title, thumb: w.item.thumb, pngBase64, ...levelOf(w.item) });
        } catch (e) {
          outcomes.push({ ratingKey: String(w.item.ratingKey), title: w.item.title, outcome: 'failed', error: e.message || String(e), ...levelOf(w.item) });
        }
        processed++;
      }
      if (jobs.length) {
        const r = await P().call(window.pcm.overlayApply(jobs, badgeSet, scopeTarget, { runId, runPhase: 'append' }));
        record = r.record;
      }
      showProgress(slice[slice.length - 1]?.item.title || '');
      idx += BATCH;
    }
    // Anything never attempted because of a cancel is recorded as such.
    if (ov.runCancel) {
      const doneKeys = new Set([
        ...(record?.items || []).map((i) => String(i.ratingKey)),
        ...outcomes.map((o) => String(o.ratingKey)),
      ]);
      for (const w of willApply) {
        if (!doneKeys.has(String(w.item.ratingKey))) {
          outcomes.push({ ratingKey: String(w.item.ratingKey), title: w.item.title, outcome: 'cancelled', error: '', ...levelOf(w.item) });
        }
      }
      for (const it of items) {
        if (!factsByKey[String(it.ratingKey)] && !doneKeys.has(String(it.ratingKey))) {
          outcomes.push({ ratingKey: String(it.ratingKey), title: it.title, outcome: 'cancelled', error: '', ...levelOf(it) });
        }
      }
    }
    // Phase 3: close the run with the result-only outcomes attached.
    const fin = await P().call(window.pcm.overlayApply([], badgeSet, scopeTarget, {
      runId, runPhase: 'finish', cancelled: !!ov.runCancel, outcomes,
    }));
    record = fin.record;
  } catch (e) {
    P().toastErr(e);
  } finally {
    ov.applying = false;
    $('btn-overlay-cancel-run').hidden = true;
    prog.hidden = true;
    updateApplyButton();
  }
  if (record) {
    const entries = outcomeEntriesFromRecord(record, []);
    const groups = CMP().groupApplyOutcomes(entries, { cancelled: !!record.cancelled });
    const host = $('overlay-results');
    host.innerHTML = summaryCardHTML(groups, { title: `Run ${record.runId}`, runId: record.runId, mainLabel: 'applied' });
    wireSummaryCard(host, record.runId);
    renderHistory();
  }
}
function onApplyProgress(p) {
  // During a streaming run the apply loop drives the progress UI
  // itself (its counts are run-global; the SSE counts are per-batch).
  if (ov.applying) return;
  const bar = $('overlay-bar-fill'), note = $('overlay-progress-note');
  if (!bar) return;
  bar.style.width = Math.round((p.done / Math.max(1, p.total)) * 100) + '%';
  note.textContent = `${p.done}/${p.total}: ${p.title} — ${p.ok ? '✓' : '✗'}`;
}

// ---------- restore default Plex posters ----------
// Per library: enumerate top-level items, classify each from its
// posters list, and — after one count + confirm — switch the items
// currently showing an uploaded image to their clean provider poster.
// Every switch is backed up first and verified after (server-side);
// the run is recorded in History like an apply run.
async function loadRestoreSections() {
  const sel = $('restore-section');
  if (!sel) return;
  const note = $('restore-load-note');
  const prev = sel.value;
  try {
    const { sections } = await P().call(window.pcm.getSections());
    sel.innerHTML = (sections || []).map((x) => `<option value="${P().esc(x.key)}" data-type="${P().esc(x.type)}" data-title="${P().esc(x.title)}">${P().esc(x.title)}</option>`).join('');
    if (!sections?.length) sel.innerHTML = '<option value="">No libraries — connect first</option>';
    // A reload (Apply view opened, server switched) must not silently
    // reset a library the user already picked in this card.
    if (prev && [...sel.options].some((o) => o.value === prev)) sel.value = prev;
    if (note) { note.hidden = true; note.textContent = ''; }
  } catch (e) {
    sel.innerHTML = '<option value="">Could not load libraries</option>';
    // The failure is stated IN the card, not just as a dead option:
    // before this, a pre-connect init left the select reading "Could
    // not load libraries" forever with no reason and no retry.
    if (note) {
      note.textContent = `Couldn't load libraries: ${e.message || String(e)} — opening this view again, switching servers, or pressing Count retries.`;
      note.hidden = false;
    }
  }
}
function restoreSectionInfo() {
  const sel = $('restore-section');
  const opt = sel.selectedOptions[0];
  if (!opt || !sel.value) return null;
  return { key: sel.value, type: opt.dataset.type || '', title: opt.dataset.title || opt.textContent || '' };
}

// ---------- leaving soon (v1.12) ----------
// The manager for the app's own Leaving list: titles leaving this
// server and the dates the owner set for them — in THIS app, on the
// Apply page. The dates live in the app's data dir (per server); they
// are never read from Plex, never fetched from anywhere, and never
// invented: the only date this card sends is the one in the date
// input, and the server validates it as a real calendar date.
//
// What the card renders is always the server's answer — every
// mutation returns the fresh list and the card re-renders from it.
// The banner a poster carries is drawn from the list AS OF APPLY
// TIME (see the leaving family's note in composite.js): editing a
// date here does not redraw posters already badged.
const leavingState = { entries: [], collections: [] };

function leavingUsDate(iso) {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(String(iso || ''));
  return m ? `${m[2]}/${m[3]}/${m[1]}` : String(iso || '');
}
function leavingDaysText(e) {
  if (e.days == null) return 'stored date unreadable';
  if (e.days < 0) return `expired ${-e.days} ${-e.days === 1 ? 'day' : 'days'} ago`;
  if (e.days === 0) return 'leaves today';
  return `in ${e.days} ${e.days === 1 ? 'day' : 'days'}`;
}
// The date input's value is the ONLY date source: empty or not a
// real calendar date → null, and the caller asks instead of sending
// anything. (The server validates again — this check exists so the
// owner gets the message before a round trip, not as the only gate.)
function leavingDateInputValue() {
  const v = ($('leaving-date')?.value || '').trim();
  if (!/^\d{4}-\d{2}-\d{2}$/.test(v)) return null;
  const [y, m, d] = v.split('-').map(Number);
  const dt = new Date(Date.UTC(y, m - 1, d));
  if (dt.getUTCFullYear() !== y || dt.getUTCMonth() !== m - 1 || dt.getUTCDate() !== d) return null;
  return v;
}
// The picked items the "set on picked" action dates: the current
// Apply scope's items (currentScopeItems — the Studio pick when the
// scope is the picked item), TOP-LEVEL only. The pick pool can hold
// season/episode targets; dates belong to the movie or show, so
// drilled-down targets are filtered out and the count note says so.
function leavingPickedItems() {
  return currentScopeItems().filter((it) => it && !it.level);
}
function updateLeavingPickedNote() {
  const el = $('leaving-picked-note');
  if (!el) return;
  const all = currentScopeItems();
  const items = leavingPickedItems();
  const skipped = all.length - items.length;
  el.textContent = items.length
    ? `${items.length} picked top-level item${items.length === 1 ? '' : 's'}${skipped ? ` (${skipped} season/episode target${skipped === 1 ? '' : 's'} not dated — dates belong to the top-level title)` : ''}`
    : 'Nothing picked yet — pick a poster in the Studio, or set an Apply scope above.';
}

async function loadLeaving() {
  const note = $('leaving-load-note');
  try {
    const { entries } = await P().call(window.pcm.leavingList());
    leavingState.entries = entries || [];
    if (note) { note.hidden = true; note.textContent = ''; }
  } catch (e) {
    leavingState.entries = [];
    // Stated in the card (like the restore card's load note): a list
    // that failed to load must not read as "no dates set".
    if (note) {
      note.textContent = `Couldn't load the Leaving list: ${e.message || String(e)} — opening this view again retries.`;
      note.hidden = false;
    }
  }
  renderLeavingList();
  updateLeavingPickedNote();
  await loadLeavingCollections();
}

async function loadLeavingCollections() {
  const sel = $('leaving-collection');
  if (!sel) return;
  const prev = sel.value;
  try {
    const { sections } = await P().call(window.pcm.getSections());
    const all = [];
    for (const sec of sections || []) {
      try {
        const { collections } = await P().call(window.pcm.getCollections(sec.key));
        for (const c of collections || []) all.push({ ...c, sectionTitle: sec.title });
      } catch { /* one library's collections failing doesn't sink the picker */ }
    }
    leavingState.collections = all;
    sel.innerHTML = all.length
      ? all.map((c) => `<option value="${P().esc(c.ratingKey)}">${P().esc(c.title)} (${P().esc(c.sectionTitle || '')})</option>`).join('')
      : '<option value="">No collections on this server</option>';
    if (prev && [...sel.options].some((o) => o.value === prev)) sel.value = prev;
  } catch {
    sel.innerHTML = '<option value="">Could not load collections</option>';
  }
}

function renderLeavingList() {
  const host = $('leaving-list');
  if (!host) return;
  const entries = leavingState.entries || [];
  if (!entries.length) {
    host.innerHTML = '<p class="muted small">No leaving dates set on this server yet. Pick items (or a whole collection), choose a date above, and set it.</p>';
    return;
  }
  const row = (e) => `
    <div class="leaving-row${e.expired ? ' leaving-expired' : ''}">
      <span class="leaving-title">${P().esc(e.title || 'Untitled')}</span>
      <span class="leaving-when">${P().esc(leavingUsDate(e.date))} · ${P().esc(leavingDaysText(e))}${e.dateError ? ` · ${P().esc(e.dateError)}` : ''}</span>
      <input type="date" class="input leaving-edit" value="${P().esc(e.date || '')}">
      <button class="btn ghost small" data-leaving-save>Set date</button>
      <button class="btn ghost small" data-leaving-remove>Remove</button>
    </div>`;
  const live = entries.filter((e) => !e.expired);
  const gone = entries.filter((e) => e.expired);
  host.innerHTML = `
    ${live.map(row).join('')}
    ${gone.length ? `<p class="tray-label">Expired — these dates have passed; clear them out</p>${gone.map(row).join('')}` : ''}`;
  // Row actions are wired with the entry captured from the render
  // (the list is re-rendered from the server's fresh answer after
  // every mutation, so a handler can never act on a stale row).
  [...host.querySelectorAll('.leaving-row')].forEach((rowEl, i) => {
    const e = [...live, ...gone][i];
    if (!e) return;
    const saveBtn = rowEl.querySelector('[data-leaving-save]');
    const removeBtn = rowEl.querySelector('[data-leaving-remove]');
    const dateInput = rowEl.querySelector('.leaving-edit');
    removeBtn.addEventListener('click', async () => {
      removeBtn.disabled = true;
      try {
        const { entries } = await P().call(window.pcm.leavingRemove(String(e.ratingKey)));
        leavingState.entries = entries || [];
        renderLeavingList();
        P().toast(`“${e.title || 'Title'}” removed from the Leaving list.`);
      } catch (err) { P().toastErr(err); removeBtn.disabled = false; }
    });
    saveBtn.addEventListener('click', async () => {
      const v = (dateInput.value || '').trim();
      const [y, m, d] = v.split('-').map(Number);
      const dt = new Date(Date.UTC(y, m - 1, d));
      if (!/^\d{4}-\d{2}-\d{2}$/.test(v) || dt.getUTCFullYear() !== y || dt.getUTCMonth() !== m - 1 || dt.getUTCDate() !== d) {
        P().toast('Pick a real calendar date first.', 'error');
        return;
      }
      saveBtn.disabled = true;
      try {
        const { entries } = await P().call(window.pcm.leavingSet([{ ratingKey: String(e.ratingKey), title: e.title || '' }], v));
        leavingState.entries = entries || [];
        renderLeavingList();
        P().toast(`Leaving date for “${e.title || 'title'}” set to ${leavingUsDate(v)}.`);
      } catch (err) { P().toastErr(err); saveBtn.disabled = false; }
    });
  });
}

async function leavingSetOnPicked() {
  const date = leavingDateInputValue();
  if (!date) { P().toast('Pick a real calendar date first — the app never invents one.', 'error'); return; }
  const items = leavingPickedItems();
  if (!items.length) { P().toast('No top-level items are picked. Pick a movie or show in the Studio (or set an Apply scope above) first.', 'error'); return; }
  const btn = $('btn-leaving-set-picked');
  btn.disabled = true;
  try {
    const { entries } = await P().call(window.pcm.leavingSet(
      items.map((it) => ({ ratingKey: String(it.ratingKey), title: it.title || '' })), date));
    leavingState.entries = entries || [];
    renderLeavingList();
    P().toast(`Leaving date ${leavingUsDate(date)} set on ${items.length} item${items.length === 1 ? '' : 's'}.`);
  } catch (e) { P().toastErr(e); } finally { btn.disabled = false; }
}

async function leavingSetOnCollection() {
  const date = leavingDateInputValue();
  if (!date) { P().toast('Pick a real calendar date first — the app never invents one.', 'error'); return; }
  const sel = $('leaving-collection');
  const rk = sel ? sel.value : '';
  if (!rk) { P().toast('Pick a collection first.', 'error'); return; }
  const btn = $('btn-leaving-set-collection');
  btn.disabled = true;
  try {
    const r = await loadCollectionItems(rk);
    if (r.cancelled) { P().toast('Could not load that collection — the load was interrupted. Try again.', 'error'); return; }
    const members = (r.items || []).filter((it) => it && !it.level);
    if (!members.length) { P().toast('That collection has no top-level items to date.', 'error'); return; }
    const { entries } = await P().call(window.pcm.leavingSet(
      members.map((it) => ({ ratingKey: String(it.ratingKey), title: it.title || '' })), date));
    leavingState.entries = entries || [];
    renderLeavingList();
    P().toast(`Leaving date ${leavingUsDate(date)} set on all ${members.length} member${members.length === 1 ? '' : 's'} of the collection.`);
  } catch (e) { P().toastErr(e); } finally { btn.disabled = false; }
}
// How many classifications the restore Count runs at once. Each is
// a network round trip to the server (posters list + renditions), so
// on a remote-grade connection a serial count crawls; six in flight
// overlaps the latency without flooding the server.
const RESTORE_COUNT_PARALLEL = 6;

async function countRestoreDefaults() {
  let sec = restoreSectionInfo();
  if (!sec) {
    // The card's list may still be the failed init-time load (init can
    // run before a server is connected) — retry the load once here
    // before asking the user to pick a library that isn't listed.
    await loadRestoreSections();
    sec = restoreSectionInfo();
  }
  if (!sec) { P().toast('Pick a library first.', 'error'); return; }
  const note = $('restore-count-note');
  note.textContent = 'Counting…';
  $('restore-confirm').hidden = true;
  ov.restoreSet = null;
  try {
    const typ = sec.type === 'movie' ? 1 : sec.type === 'show' ? 2 : null;
    const { items } = await P().call(window.pcm.getSectionItems(sec.key, '', typ));
    const set = { section: sec, items: slimItems(items), restore: [], already: 0, noProvider: 0, failed: 0 };
    // Classify through a small worker pool (RESTORE_COUNT_PARALLEL
    // in flight) instead of strictly one at a time. Results land in
    // per-index slots and are aggregated IN ITEM ORDER once the pool
    // drains, so the counts, the failure accounting (a rejected call
    // is one 'failed'), and the restore list come out identical to
    // the serial count; only the progress titles interleave. A newer
    // Count supersedes this one via the countRun token: workers stop
    // taking items, in-flight results are discarded, and this run
    // writes nothing further to the card or ov.restoreSet.
    const runId = ++ov.countRun;
    const statuses = new Array(set.items.length);
    let nextItem = 0, doneCount = 0;
    const countWorker = async () => {
      while (runId === ov.countRun) {
        const i = nextItem++;
        if (i >= set.items.length) return;
        const it = set.items[i];
        try {
          const { classification } = await P().call(window.pcm.overlayClassifyPoster(it.ratingKey));
          statuses[i] = classification.status;
        } catch { statuses[i] = 'failed'; }
        if (runId !== ov.countRun) return;
        doneCount++;
        note.textContent = `Counting… ${doneCount}/${set.items.length}: ${it.title}`;
      }
    };
    await Promise.all(Array.from({ length: Math.min(RESTORE_COUNT_PARALLEL, set.items.length) }, () => countWorker()));
    if (runId !== ov.countRun) return; // superseded — the newer count owns the card
    for (let i = 0; i < set.items.length; i++) {
      const st = statuses[i];
      if (st === 'restore') set.restore.push(set.items[i]);
      else if (st === 'already-default') set.already++;
      else if (st === 'failed' || st === undefined) set.failed++;
      else set.noProvider++;
    }
    ov.restoreSet = set;
    note.textContent = `${set.restore.length} to restore · ${set.already} already default · ${set.noProvider} no provider poster${set.failed ? ` · ${set.failed} couldn't be checked` : ''}`;
    if (set.restore.length) {
      $('restore-confirm-text').textContent = `Restore ${set.restore.length} poster${set.restore.length === 1 ? '' : 's'} in “${sec.title}” to the clean Plex agent poster? Each current poster is backed up first.`;
      $('restore-confirm').hidden = false;
    }
  } catch (e) {
    note.textContent = '';
    P().toastErr(e);
  }
}
async function startRestoreDefaults() {
  const set = ov.restoreSet;
  if (!set || !set.restore.length) return;
  $('restore-confirm').hidden = true;
  ov.restoreCancel = false;
  $('btn-restore-cancel-run').hidden = false;
  const prog = $('restore-progress'), note = $('restore-progress-note'), bar = $('restore-bar-fill');
  prog.hidden = false;
  $('restore-results').innerHTML = '';
  const runId = newClientRunID();
  const badgeSet = { name: 'Restore default Plex posters', placements: [] };
  const target = { type: 'restore-defaults', sectionKey: String(set.section.key), title: set.section.title, count: set.restore.length };
  const outcomes = [];
  let record = null;
  try {
    const startRes = await P().call(window.pcm.overlayApply([], badgeSet, target, { runId, runPhase: 'start' }));
    record = startRes.record;
    for (let i = 0; i < set.restore.length; i++) {
      if (ov.restoreCancel) {
        for (let j = i; j < set.restore.length; j++) {
          outcomes.push({ ratingKey: String(set.restore[j].ratingKey), title: set.restore[j].title, outcome: 'cancelled', error: '' });
        }
        break;
      }
      const it = set.restore[i];
      bar.style.width = Math.round(((i + 1) / set.restore.length) * 100) + '%';
      note.textContent = `Restoring… ${i + 1} / ${set.restore.length} — ${it.title}`;
      try {
        const r = await P().call(window.pcm.overlayRestoreDefaultPoster({
          ratingKey: it.ratingKey, title: it.title, thumb: it.thumb,
          sectionKey: String(set.section.key), sectionType: set.section.type,
        }));
        outcomes.push({
          ratingKey: String(it.ratingKey), title: it.title,
          outcome: r.outcome, error: r.error || '', backupPath: r.backupPath || '',
        });
      } catch (e) {
        outcomes.push({ ratingKey: String(it.ratingKey), title: it.title, outcome: 'failed', error: e.message || String(e) });
      }
      // Stream outcomes into the run in small batches.
      if (outcomes.length >= 5) {
        const r = await P().call(window.pcm.overlayApply([], badgeSet, target, { runId, runPhase: 'append', outcomes: outcomes.splice(0) }));
        record = r.record;
      }
    }
    const fin = await P().call(window.pcm.overlayApply([], badgeSet, target, {
      runId, runPhase: 'finish', cancelled: !!ov.restoreCancel, outcomes: outcomes.splice(0),
    }));
    record = fin.record;
  } catch (e) {
    P().toastErr(e);
  } finally {
    prog.hidden = true;
    $('btn-restore-cancel-run').hidden = true;
  }
  ov.restoreSet = null;
  // The count line stays: its already-default / no-provider numbers are
  // part of this action's summary, and those items are not in the run.
  if (record) {
    const entries = outcomeEntriesFromRecord(record, []);
    const groups = CMP().groupApplyOutcomes(entries, { cancelled: !!record.cancelled });
    const host = $('restore-results');
    host.innerHTML = summaryCardHTML(groups, {
      title: `Restore defaults — ${set.section.title}`, runId: record.runId,
      mainLabel: 'restored to the clean agent poster', mainCount: groups.counts.restored,
    });
    wireSummaryCard(host, record.runId);
    renderHistory();
  }
}

// ---------- restore backed-up posters (v1.23) ----------
// Lists every item with a saved overlay backup — all levels — and
// restores originals through the shared gate. The list is re-fetched
// from the server after every mutation, so a handler can never act
// on a stale row. Items the server flags un-restorable (gone from
// the library, backup image missing) render disabled with the reason.
function backupContextLine(b) {
  if (b.level === 'season') return b.parentTitle ? `Season of ${b.parentTitle}` : 'Season';
  if (b.level === 'episode') {
    const show = b.grandparentTitle ? `${b.grandparentTitle} · ` : '';
    return `${show}${b.parentTitle || 'Episode'}`;
  }
  if (b.level === 'movie') return 'Movie';
  if (b.level === 'show') return 'Show';
  return 'Unknown level';
}
function backupRestorable(b) { return b.found && b.imageExists; }
function backupRestoredIds() { return ov.backupsRestored || (ov.backupsRestored = new Set()); }
function renderBackupList() {
  const host = $('backups-list');
  if (!host) return;
  const all = ov.backups || [];
  const level = $('backup-level') ? $('backup-level').value : '';
  const shown = level ? all.filter((b) => b.level === level) : all;
  const note = $('backups-note');
  if (note) note.textContent = all.length
    ? `${shown.length} of ${all.length} backed-up item${all.length === 1 ? '' : 's'} shown`
    : '';
  const allBtn = $('btn-backups-restore-all');
  if (allBtn) allBtn.disabled = !shown.some(backupRestorable);
  if (!all.length) {
    host.innerHTML = '<p class="muted small">No backed-up posters on this server yet. Apply an overlay and its original is saved here automatically.</p>';
    return;
  }
  if (!shown.length) {
    host.innerHTML = '<p class="muted small">No backed-up items at this level.</p>';
    return;
  }
  const restored = backupRestoredIds();
  host.innerHTML = shown.map((b) => {
    const disabled = !backupRestorable(b);
    const reason = !b.found ? 'No longer in this library' : !b.imageExists ? 'Backup image missing' : '';
    const done = restored.has(String(b.ratingKey));
    return `<div class="leaving-row">
      <span class="leaving-title">${P().esc(b.title || 'Untitled')}</span>
      <span class="leaving-when">${P().esc(backupContextLine(b))} · backed up ${P().esc(b.backedUpAt || 'unknown date')}${reason ? ` · ${P().esc(reason)}` : ''}${done ? ' · restored' : ''}</span>
      <button class="btn ghost small" data-backup-restore ${disabled || done ? 'disabled' : ''}>Restore</button>
    </div>`;
  }).join('');
  [...host.querySelectorAll('.leaving-row')].forEach((rowEl, i) => {
    const b = shown[i];
    if (!b) return;
    rowEl.querySelector('[data-backup-restore]').addEventListener('click', () => restoreBackups([b]));
  });
}
async function loadBackupList() {
  const host = $('backups-list');
  if (!host) return;
  host.innerHTML = '<p class="muted small">Loading backed-up posters…</p>';
  try {
    const { backups } = await P().call(window.pcm.overlayListBackups());
    ov.backups = backups || [];
    renderBackupList();
  } catch (e) {
    host.innerHTML = `<p class="muted small">Could not load backed-up posters: ${P().esc(e.message || String(e))}</p>`;
  }
}
async function restoreBackups(items) {
  const results = $('backups-results');
  if (results) results.innerHTML = '<p class="muted small">Restoring…</p>';
  try {
    const { outcomes } = await P().call(window.pcm.overlayRestoreBackups(items.map((b) => ({
      ratingKey: String(b.ratingKey), title: b.title || '',
      level: b.level || '', parentTitle: b.parentTitle || '', grandparentTitle: b.grandparentTitle || '',
    }))));
    const restored = backupRestoredIds();
    const lines = (outcomes || []).map((o) => {
      if (o.ok || o.outcome === 'restored') restored.add(String(o.ratingKey));
      const title = (items.find((b) => String(b.ratingKey) === String(o.ratingKey)) || {}).title || o.title || o.ratingKey;
      return `<p class="muted small">${P().esc(title)} — ${o.ok || o.outcome === 'restored' ? 'restored to its backed-up original' : `failed: ${P().esc(o.error || 'unknown error')}`}</p>`;
    }).join('');
    if (results) results.innerHTML = lines || '<p class="muted small">Nothing was restored.</p>';
    renderBackupList();
    renderHistory();
  } catch (e) {
    if (results) results.innerHTML = `<p class="muted small">Restore failed: ${P().esc(e.message || String(e))}</p>`;
    P().toastErr(e);
  }
}

// ---------- history + re-apply ----------
async function renderHistory() {
  const host = $('overlay-history');
  if (!host) return;
  let runs = [];
  try { ({ runs } = await P().call(window.pcm.overlayHistory())); } catch (e) { host.innerHTML = `<p class="muted">${P().esc(e.message)}</p>`; return; }
  const packs = ov.packs;
  host.innerHTML = `
    <div class="overlay-settings-strip">
      <span class="muted small">Custom badges folder:</span>
      <code class="small">${P().esc(packs?.customDir || '…')}</code>
      <button class="btn ghost small" id="btn-reload-packs">Reload packs</button>
      <label class="check small"><input type="checkbox" id="chk-reapply"> <span>Weekly re-apply check</span></label>
    </div>
    ${runs.length ? runs.map((r) => `
      <div class="run-card">
        <div class="run-head"><strong>${P().esc(r.runId)}</strong>
          <span class="muted small">${P().esc(r.timestamp || '')} · ${r.okCount} ok / ${r.failCount} failed${r.noOverlayCount ? ` · ${r.noOverlayCount} no overlay` : ''}${r.status ? ` · ${P().esc(r.status)}` : ''}${r.badgeSet?.name ? ` · ${P().esc(r.badgeSet.name)}` : ''}</span></div>
        <div class="btn-row">
          <button class="btn secondary small" data-restore-run="${P().esc(r.runId)}">Restore all originals from this run</button>
        </div>
      </div>`).join('') : '<p class="muted">No overlay runs yet. Design something in the Studio tab.</p>'}`;
  $('btn-reload-packs').addEventListener('click', async () => {
    try { await loadPacks(); renderTrays(); renderVerdicts(); P().toast('Badge packs reloaded.', 'success'); }
    catch (e) { P().toastErr(e); }
  });
  const chk = $('chk-reapply');
  try {
    const { settings } = await P().call(window.pcm.advisorGetSettings());
    chk.checked = !!settings.reapplyOverlays;
    chk.addEventListener('change', async () => {
      await P().call(window.pcm.advisorSetSettings({ reapplyOverlays: chk.checked }));
      P().toast(chk.checked ? 'Weekly re-apply check on.' : 'Weekly re-apply check off.', 'success');
    });
  } catch { /* settings unavailable — leave unchecked */ }
  host.querySelectorAll('[data-restore-run]').forEach((b) => b.addEventListener('click', async () => {
    const m = await P().openModal({
      title: 'Restore originals?', bodyHTML: `<p>Re-upload the backed-up original posters for every item in run <b>${P().esc(b.dataset.restoreRun)}</b>. This undoes the overlays, not the backup.</p>`,
      footHTML: '<button class="btn ghost" data-close="no">Cancel</button><button class="btn danger" data-close="yes">Restore all</button>',
    }).closed;
    if (m !== 'yes') return;
    try {
      const r = await P().call(window.pcm.overlayRestore({ runId: b.dataset.restoreRun }));
      P().toast(`Restored ${r.okCount}/${r.results.length} posters.`, r.okCount === r.results.length ? 'success' : 'error');
    } catch (e) { P().toastErr(e); }
  }));
}
async function reapplyCheck(loud) {
  const host = $('overlay-reapply');
  host.innerHTML = '<p class="muted small">Checking…</p>';
  try {
    const { items } = await P().call(window.pcm.overlayReapplyCheck());
    const need = items.filter((i) => i.needsReapply);
    host.innerHTML = need.length
      ? `<p><b>${need.length}</b> poster${need.length === 1 ? '' : 's'} changed since overlays were applied:</p>` +
        need.map((i) => `<div class="dryrun-row"><span>${P().esc(i.title)}</span><span class="muted small">run ${P().esc(i.runId || '')}</span></div>`).join('') +
        `<p class="muted small">Re-design from the Studio tab to re-apply — the stored badge set is in the run history sidecars.</p>`
      : '<p class="muted small">All overlaid posters still match. Nothing to re-apply.</p>';
    if (loud) P().toast(need.length ? `${need.length} poster(s) changed.` : 'All overlays intact.', need.length ? 'error' : 'success');
  } catch (e) { host.innerHTML = `<p class="muted small">${P().esc(e.message)}</p>`; }
}
async function maybeAutoReapplyCheck() {
  // In-app scheduler (SPEC default): weekly check, silent unless needed.
  try {
    const { settings } = await P().call(window.pcm.advisorGetSettings());
    if (!settings.reapplyOverlays) return;
    const last = Number(localStorage.getItem('pcm-reapply-last') || 0);
    if (Date.now() - last < 7 * 86400000) return;
    const { items } = await P().call(window.pcm.overlayReapplyCheck());
    localStorage.setItem('pcm-reapply-last', String(Date.now()));
    const need = items.filter((i) => i.needsReapply);
    if (need.length) P().toast(`${need.length} overlaid poster(s) changed since apply — see Overlays → History.`, 'error');
  } catch { /* never break boot for a background check */ }
}

// AI ribbon-text ideas (BYOK, off by default — proposals only).
async function aiRibbonIdeas(anchorEl, onPick) {
  try {
    const { proposal } = await P().call(window.pcm.aiProposeRibbon('movie poster ribbon, short punchy label'));
    if (!proposal?.ideas?.length) { P().toast('AI returned nothing usable.', 'error'); return; }
    const m = P().openModal({
      title: 'AI ribbon ideas', bodyHTML: proposal.ideas.map((t, i) => `<button class="btn ghost small" data-pick="${i}" style="margin:4px">${P().esc(t)}</button>`).join(''),
      footHTML: '<button class="btn ghost" data-close="cancel">Cancel</button>',
    });
    m.root.querySelectorAll('[data-pick]').forEach((b) => b.addEventListener('click', () => { onPick(proposal.ideas[Number(b.dataset.pick)]); m.close('picked'); }));
  } catch (e) { P().toastErr(e); }
}

document.addEventListener('DOMContentLoaded', init);
// v0.4 — called from the Collections tab ("Overlay" button on a collection
// card or detail view). Jumps to the Overlays tab, opens the Apply view,
// and pre-selects the collection scope with its members resolved and
// confirmed — this only picks the target; the one-click Apply (with
// the backup gate) does the work.
async function applyToCollection(ratingKey, title) {
  document.querySelector('[data-tab="overlays"]').click();
  switchView('apply');
  await setScope('collection');
  try {
    const r = await loadCollectionItems(ratingKey);
    if (r.cancelled) {
      $('overlay-target-detail').innerHTML = `<span class="err-text">Loading “${P().esc(title)}” was interrupted by a newer load — open the collection again.</span>`;
      return;
    }
    const items = r.items || [];
    askScopeConfirm(slimItems(items), `collection “${title}”`, { type: 'collection', ratingKey: String(ratingKey), title });
    P().toast(`Overlay target: “${title}” (${items.length} items) — design badges in the Studio, then confirm the count here to apply.`, 'success');
  } catch (e) {
    $('overlay-target-detail').innerHTML = `<span class="err-text">Couldn't load “${P().esc(title)}”: ${P().esc(e.message)}</span>`;
    P().toastErr(e);
  }
}
window.PCMOverlays = { aiRibbonIdeas, applyToCollection };
})();
