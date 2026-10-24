'use strict';

/* ============================================================================
 * app.js — all renderer logic. The UI is UNTRUSTED: no tokens, no node.
 * ----------------------------------------------------------------------------
 * FUTURE AI — read this before touching the UI:
 *   - Every Plex operation goes through window.pcm (the preload bridge).
 *     NEVER fetch() a Plex URL from here — you have no token and no right
 *     to one. If you need data, add an ipcMain handler in main.js first.
 *   - Artwork: use artSrc(thumb) -> "pleximg://{machine}{path}". The main
 *     process proxies with the token. <img onerror> falls back to a
 *     CSS initial-letter tile (see posterHTML).
 *   - Global `state` below is the single source of truth for the session
 *     (user, servers, selected machine, sections, collections, draft).
 *     Tab render functions read it; event handlers mutate it then re-render.
 *   - THE BUILDER CONTRACT (v0.1's heart): `draft` holds the working copy.
 *     buildDryRun() diffs draft vs. the live collection. applyDraft() is the
 *     ONLY path that calls a write IPC, and only after the user confirms
 *     the diff in a modal. Keep that order — preview, confirm, write.
 *   - Smart collections aren't edited in place. Since v1.14 the rule
 *     Plex stored IS readable (the record's content URI \u2014 the detail
 *     view and the health view show it in plain words), but "Edit" on
 *     a smart collection still opens the Builder as a NEW manual draft
 *     seeded with the current members rather than rewriting a live
 *     rule. Deliberate.
 * ========================================================================== */

// ---------- tiny utilities --------------------------------------------------
const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

// Throws {message, hint} on failure so callers can `catch(toastErr)`.
async function call(promise) {
  const r = await promise;
  if (!r || r.ok !== true) throw { message: (r && r.error) || 'Unknown error', hint: (r && r.hint) || '' };
  return r;
}
function toast(msg, kind = '', hint = '') {
  const el = document.createElement('div');
  el.className = 'toast ' + kind;
  el.innerHTML = esc(msg) + (hint ? `<span class="hint">${esc(hint)}</span>` : '');
  $('toast-root').appendChild(el);
  setTimeout(() => { el.style.opacity = '0'; el.style.transition = 'opacity .3s'; setTimeout(() => el.remove(), 320); }, kind === 'error' ? 9000 : 4200);
}
const toastErr = (e) => toast(e.message || 'Something went wrong', 'error', e.hint || '');

// Modal helper. Returns a handle SYNCHRONOUSLY (the modal stays open):
//   const m = openModal({...});   // access m.root for live wiring
//   const val = await m.closed;   // resolves when the modal closes
// For simple confirms: `await openModal({...}).closed`.
// FUTURE AI: do NOT make this await-first — the add-titles picker wires up
// live search input while the modal is open, which requires the handle
// before close.
function openModal({ title, bodyHTML, footHTML }) {
  const root = $('modal-root');
  let resolveClosed;
  const closed = new Promise((resolve) => { resolveClosed = resolve; });
  const close = (val) => { root.innerHTML = ''; resolveClosed(val); };
  root.innerHTML = `
    <div class="modal-backdrop">
      <div class="modal" role="dialog" aria-modal="true">
        <div class="modal-head"><h3>${esc(title)}</h3><button class="btn ghost small" data-x>✕</button></div>
        <div class="modal-body">${bodyHTML}</div>
        ${footHTML ? `<div class="modal-foot">${footHTML}</div>` : ''}
      </div>
    </div>`;
  root.querySelector('.modal-backdrop').addEventListener('mousedown', (e) => { if (e.target.classList.contains('modal-backdrop')) close(null); });
  root.querySelector('[data-x]').addEventListener('click', () => close(null));
  root.querySelectorAll('[data-close]').forEach((b) => b.addEventListener('click', () => close(b.dataset.close)));
  return { closed, close, root };
}

// ---------- global state -----------------------------------------------------
const state = {
  user: null,
  servers: [],
  machine: null,          // selected server machine id (for pleximg:// URLs)
  sections: [],
  sectionKey: null,
  collections: [],
  detail: null,           // { collection, items } when a collection is open
  loadGen: 0,             // v0.4.1: bumped on every server/section change; async
                          // loads capture it and drop their results if stale
  loginPoll: null,
  authUrl: null,
};

// Artwork URL via the privileged main-process protocol. thumb looks like
// "/library/metadata/42/thumb/1700000000". Empty thumb -> '' (caller renders
// a fallback tile).
// v0.4.4: `w` is the pixel width to ask Plex for (the proxy transcodes
// server-side). Grid cards are ~150px CSS, so 300 covers retina. w=0 means
// full size — the overlay designer composites at 1000x1500 and MUST pass 0.
// GO BUILD: pleximg:// became the server /art route (same sizing); the
// session token rides along because <img> cannot send headers.
function artSrc(thumb, w = 300) {
  if (!thumb || !state.machine) return '';
  return `/art?machine=${encodeURIComponent(state.machine)}&path=${encodeURIComponent(thumb)}&w=${w || 0}&t=${window.__PCM_TOKEN__ || ''}`;
}

// Poster card HTML shared by grids. `sub` is the small line under the title.
function posterHTML(item, sub = '') {
  const src = artSrc(item.thumb);
  const img = src
    ? `<img src="${esc(src)}" alt="" loading="lazy" onerror="this.parentNode.innerHTML='<div class=&quot;poster-fallback&quot;>${esc((item.title || '?')[0].toUpperCase())}</div>'">`
    : `<div class="poster-fallback">${esc((item.title || '?')[0].toUpperCase())}</div>`;
  return `<div class="poster-frame">${img}</div>
    <p class="poster-title">${esc(item.title)}</p>${sub ? `<p class="poster-sub">${esc(sub)}</p>` : ''}`;
}

// ---------- tab navigation ----------------------------------------------------
function switchTab(name) {
  document.querySelectorAll('.nav-item').forEach((b) => b.classList.toggle('active', b.dataset.tab === name));
  document.querySelectorAll('.tab').forEach((t) => t.classList.toggle('active', t.id === 'tab-' + name));
  if (name === 'collections' && state.machine && !state.sections.length) loadSections();
}
document.querySelectorAll('.nav-item').forEach((b) => b.addEventListener('click', () => switchTab(b.dataset.tab)));

// ---------- CONNECT tab -------------------------------------------------------
function setConnectView(which) {
  // which: 'signed-out' | 'pending' | 'servers'
  $('connect-signed-out').hidden = which !== 'signed-out';
  $('connect-pending').hidden = which !== 'pending';
  $('connect-servers').hidden = which !== 'servers';
}

// Ko-fi donate widget (v1.22, owner's exact implementation, toggle
// in v1.23): the floating-chat overlay widget. On app load, when the
// toggle is on and a username is set, the page attempts the Ko-fi
// script; on success it draws the widget with his config, verbatim.
// Toggle OFF means the script is never loaded and any drawn widget
// is removed outright — the same removal path as "no username".
// On load failure (offline, CDN unreachable) it fails SILENTLY: the
// script element is removed, no widget, no error toast, no broken
// UI. A later refresh re-attempts the load, so a transient outage
// self-heals.
let kofiDrawnFor = '';
function applyDonateWidget(username, enabled) {
  const scriptId = 'kofi-overlay-widget-script';
  const existing = document.getElementById(scriptId);
  if (!username || enabled === false) {
    // No username, or toggled off: remove the loader and anything
    // the widget drew. The widget marks its own nodes with kofi in
    // the id/class; this app's own settings elements carry no such
    // marker, so the sweep is safe. Clearing kofiDrawnFor means a
    // later toggle-on re-draws instead of hitting the already-drawn
    // early return below.
    if (existing) existing.remove();
    document.querySelectorAll('[id*="kofi"], [class*="kofi"]').forEach((el) => {
      if (el.id !== scriptId) el.remove();
    });
    kofiDrawnFor = '';
    return;
  }
  if (kofiDrawnFor === username && window.kofiWidgetOverlay) return; // already drawn for this user
  const draw = () => {
    if (!window.kofiWidgetOverlay) return;
    window.kofiWidgetOverlay.draw(username, {
      'type': 'floating-chat',
      'floating-chat.donateButton.text': 'Donate',
      'floating-chat.donateButton.background-color': '#323842',
      'floating-chat.donateButton.text-color': '#fff',
    });
    kofiDrawnFor = username;
  };
  if (window.kofiWidgetOverlay) { draw(); return; }
  if (existing) return; // load already in flight; its onload draws
  const s = document.createElement('script');
  s.id = scriptId;
  s.src = 'https://storage.ko-fi.com/cdn/scripts/overlay-widget.js';
  s.async = true;
  s.onload = draw;
  s.onerror = () => { s.remove(); }; // silent: no widget, no toast, retry on next refresh
  document.head.appendChild(s);
}

async function refreshDonateWidget() {
  try {
    const meta = await call(window.pcm.metaStatus());
    applyDonateWidget(meta.donateUsername ? String(meta.donateUsername) : 't4v3rn', meta.donateEnabled !== false);
  } catch { applyDonateWidget('', false); }
}

function renderServerPill() {
  const pill = $('server-pill');
  const srv = state.servers.find((s) => s.machine === state.machine);
  if (srv) {
    pill.className = 'server-pill online';
    $('server-pill-text').textContent = srv.name;
  } else {
    pill.className = 'server-pill signed-out';
    $('server-pill-text').textContent = state.user ? 'Pick a server' : 'Not signed in';
  }
}

function renderServers() {
  $('user-name').textContent = state.user ? state.user.name : '';
  $('servers-title').textContent = `Your servers (${state.servers.length})`;
  const list = $('server-list');
  list.innerHTML = state.servers.map((s) => `
    <div class="server-card ${s.machine === state.machine ? 'selected' : ''}" data-machine="${esc(s.machine)}" role="button" tabindex="0">
      <h3>${esc(s.name)}</h3>
      <span class="quality-badge q-${s.quality}"><span class="dot"></span>${esc(s.qualityLabel)}</span>
      <p class="server-meta">${s.owned ? 'Owned by you' : 'Shared with you'} · click to manage</p>
    </div>`).join('') || '<p class="muted">No servers found on this Plex account.</p>';
  list.querySelectorAll('.server-card').forEach((c) => {
    const pick = async () => {
      try {
        await call(window.pcm.selectServer(c.dataset.machine));
        state.machine = c.dataset.machine;
        state.loadGen++; // invalidate any in-flight loads from the old server
        state.sections = []; state.collections = []; state.detail = null; state.sectionKey = null;
        $('b-section').innerHTML = ''; openBuilderNew();
        await loadSections();
        renderServers(); renderServerPill();
        toast('Server selected', 'success');
      } catch (e) { toastErr(e); }
    };
    c.addEventListener('click', pick);
    c.addEventListener('keydown', (e) => { if (e.key === 'Enter' || e.key === ' ') pick(); });
  });
  renderServerPill();
}

function applySession({ user, servers, selectedMachine, storeBackend }) {
  state.user = user; state.servers = servers; state.machine = selectedMachine;
  // GO BUILD: the secret store reports 'dpapi' on Windows (DPAPI-encrypted
  // files — see internal/store) instead of Electron's 'keytar'.
  $('token-backend').textContent = storeBackend === 'keytar' ? 'Windows Credential Manager'
    : storeBackend === 'dpapi' ? 'Windows DPAPI (encrypted file)' : 'encrypted local file';
  renderServers();
  setConnectView('servers');
  if (selectedMachine) { state.sections = []; loadSections().catch(() => {}); }
}

async function boot() {
  // The donate widget is settings-driven, not session-driven: it
  // applies on every boot regardless of sign-in state (and attempts
  // nothing at all while the toggle is off or no username is set).
  refreshDonateWidget();
  // Try a silent restore first — returning users skip the login screen.
  try {
    const r = await call(window.pcm.restoreSession());
    if (r.signedIn) { applySession(r); return; }
  } catch (e) { console.warn('restore failed', e); }
  setConnectView('signed-out');
}

$('btn-signin').addEventListener('click', async () => {
  try {
    const { pinId, authUrl, expiresIn } = await call(window.pcm.startLogin());
    state.authUrl = authUrl;
    window.open(authUrl, '_blank'); // main.js routes plex.tv URLs to the system browser
    setConnectView('pending');
    const deadline = Date.now() + (expiresIn - 15) * 1000;
    clearInterval(state.loginPoll);
    state.loginPoll = setInterval(async () => {
      if (Date.now() > deadline) { clearInterval(state.loginPoll); setConnectView('signed-out'); toast('Login timed out — try again.', 'error'); return; }
      try {
        const r = await call(window.pcm.pollLogin(pinId));
        if (!r.pending) { clearInterval(state.loginPoll); applySession(r); toast(`Welcome back, ${r.user.name}`, 'success'); }
      } catch (e) { clearInterval(state.loginPoll); setConnectView('signed-out'); toastErr(e); }
    }, 2500);
  } catch (e) { toastErr(e); }
});
$('btn-reopen-auth').addEventListener('click', () => { if (state.authUrl) window.open(state.authUrl, '_blank'); });
$('btn-cancel-login').addEventListener('click', () => { clearInterval(state.loginPoll); setConnectView('signed-out'); });
$('btn-logout').addEventListener('click', async () => {
  await call(window.pcm.logout()).catch(() => {});
  Object.assign(state, { user: null, servers: [], machine: null, sections: [], sectionKey: null, collections: [], detail: null });
  $('b-section').innerHTML = ''; openBuilderNew();
  setConnectView('signed-out'); renderServerPill(); switchTab('connect');
});

// ---------- COLLECTIONS tab ----------------------------------------------------
async function loadSections() {
  const gen = state.loadGen;
  $('section-tabs').innerHTML = '<span class="muted">Loading libraries…</span>';
  const { sections, cancelled } = await call(window.pcm.getSections());
  if (cancelled || gen !== state.loadGen) return; // superseded — drop silently
  state.sections = sections;
  const tabs = $('section-tabs');
  tabs.innerHTML = sections.map((s) => `<button class="section-tab" data-key="${esc(s.key)}">${esc(s.title)}</button>`).join('');
  tabs.querySelectorAll('.section-tab').forEach((b) => b.addEventListener('click', () => {
    state.sectionKey = b.dataset.key; state.detail = null;
    state.loadGen++; // invalidate any in-flight load for the previous tab
    tabs.querySelectorAll('.section-tab').forEach((x) => x.classList.toggle('active', x === b));
    loadCollections();
  }));
  const first = tabs.querySelector('.section-tab');
  if (first) { first.classList.add('active'); state.sectionKey = first.dataset.key; await loadCollections(); }
  // Keep the Builder's library select in sync.
  $('b-section').innerHTML = sections.map((s) => `<option value="${esc(s.key)}" data-type="${s.type}">${esc(s.title)} (${s.type === 'movie' ? 'movies' : 'TV'})</option>`).join('');
  openBuilderNew();
}

async function loadCollections() {
  if (!state.sectionKey) return;
  const gen = state.loadGen;
  $('collection-detail').hidden = true;
  // Loading state: the grid never shows the previous tab's collections while
  // the new ones are in flight (that was the "wrong collections in the wrong
  // tab" bug — stale renders have nowhere to land now).
  $('collections-grid').innerHTML = '<p class="muted">Loading collections…</p>';
  $('collections-toolbar').hidden = true;
  const { collections, cancelled } = await call(window.pcm.getCollections(state.sectionKey));
  if (cancelled || gen !== state.loadGen) return; // superseded — drop silently
  state.collections = collections;
  $('collections-toolbar').hidden = false;
  renderCollectionsGrid();
  // v1.13: the collections manager panel (health/editor/bulk art)
  // renders alongside the grid for the same library.
  if (window.PCMCollectionsManager) window.PCMCollectionsManager.show(state.sectionKey);
}

function filteredCollections() {
  const q = $('collections-search').value.trim().toLowerCase();
  const kind = $('collections-kind').value;
  const sort = $('collections-sort').value;
  let list = state.collections.filter((c) =>
    (!q || c.title.toLowerCase().includes(q)) &&
    (kind === 'all' || (kind === 'smart' ? c.smart : !c.smart)));
  const by = {
    'title': (a, b) => a.title.localeCompare(b.title),
    'size-desc': (a, b) => b.childCount - a.childCount,
    'size-asc': (a, b) => a.childCount - b.childCount,
    'recent': (a, b) => b.updatedAt - a.updatedAt,
  }[sort];
  return list.sort(by);
}

function renderCollectionsGrid() {
  const list = filteredCollections();
  const grid = $('collections-grid');
  grid.innerHTML = list.map((c) => `
    <div class="poster-card" data-rk="${esc(c.ratingKey)}" role="button" tabindex="0" title="${esc(c.title)}">
      <div class="poster-frame">
        ${artSrc(c.thumb) ? `<img src="${esc(artSrc(c.thumb))}" alt="" loading="lazy" onerror="this.outerHTML='<div class=&quot;poster-fallback&quot;>${esc((c.title || '?')[0].toUpperCase())}</div>'">` : ''}
        <span class="kind-badge ${c.smart ? 'smart' : 'static'}">${c.smart ? 'SMART' : 'MANUAL'}</span>
        <span class="count-chip">${c.childCount} titles</span>
        ${artSrc(c.thumb) ? '' : `<div class="poster-fallback">${esc(c.title[0].toUpperCase())}</div>`}
      </div>
      <p class="poster-title">${esc(c.title)}</p>
      <div class="card-actions">
        <button class="btn ghost small" data-act="edit" title="Open this collection in the Builder">Edit</button>
        <button class="btn ghost small" data-act="overlay" title="Design poster overlays for every title in this collection">Overlay</button>
      </div>
    </div>`).join('');
  $('collections-empty').hidden = list.length > 0;
  grid.querySelectorAll('.poster-card').forEach((el) => {
    const open = () => openCollection(el.dataset.rk);
    el.addEventListener('click', open);
    el.addEventListener('keydown', (e) => { if (e.key === 'Enter') open(); });
  });
  // v0.4: per-card Edit / Overlay / Delete. stopPropagation so the buttons
  // don't also trigger the card's open-detail click.
  grid.querySelectorAll('.card-actions button').forEach((b) => b.addEventListener('click', (e) => {
    e.stopPropagation();
    const c = state.collections.find((x) => x.ratingKey === b.closest('.poster-card').dataset.rk);
    if (!c) return;
    if (b.dataset.act === 'edit') {
      // Card path: members aren't loaded yet — fetch, then seed the Builder.
      (async () => {
        try {
          let r = await call(window.pcm.getCollectionItems(c.ratingKey));
          // A cancelled answer carries an empty items list that is
          // NOT the member list (a newer load superseded ours) —
          // retry once, and never seed the Builder from it: an
          // empty seed applied later would read as removing everyone.
          if (r && r.cancelled) r = await call(window.pcm.getCollectionItems(c.ratingKey));
          if (!r || r.cancelled) {
            toast('Could not load that collection\'s members — the load was interrupted. Try Edit again.', 'error');
            return;
          }
          openBuilderEdit(c, r.items || []);
          switchTab('builder');
        } catch (e) { toastErr(e); }
      })();
    }
    else if (b.dataset.act === 'overlay') {
      if (window.PCMOverlays && window.PCMOverlays.applyToCollection) window.PCMOverlays.applyToCollection(c.ratingKey, c.title);
      else toast('Overlays tab is not ready yet.', 'error');
    }

  }));
}
['collections-search', 'collections-sort', 'collections-kind'].forEach((id) =>
  $(id).addEventListener('input', renderCollectionsGrid));

async function openCollection(ratingKey) {
  const collection = state.collections.find((c) => c.ratingKey === ratingKey);
  if (!collection) return;
  const gen = state.loadGen;
  // v0.4: show a loading state, then surface load failures IN the member
  // wall (error panel + Retry) instead of a silently empty grid. the owner
  // reported he "can't see what's inside" collections — the old catch
  // swallowed the error into an empty wall, which looks identical to an
  // empty collection.
  // v0.4.1: jump DOWN to the detail as soon as it opens (it used to render at
  // the bottom of the page with no scroll). "← All collections" jumps back up.
  state.detail = { collection, items: [] };
  $('collections-grid').hidden = true;
  $('collections-toolbar').hidden = true;
  $('collections-empty').hidden = true;
  const d0 = $('collection-detail');
  d0.hidden = false;
  $('collection-created-note').hidden = true;
  $('detail-title').textContent = collection.title;
  $('members-grid').innerHTML = '<div class="empty"><p class="muted">Loading members&#8230;</p></div>';
  d0.scrollIntoView({ behavior: 'smooth', block: 'start' });
  try {
    const { items, cancelled } = await call(window.pcm.getCollectionItems(ratingKey));
    if (cancelled || gen !== state.loadGen) return; // superseded — drop silently
    state.detail.items = items || [];
  } catch (e) {
    toastErr(e);
    $('members-grid').innerHTML = `<div class="empty">
      <p><strong>Couldn't load what's inside this collection.</strong></p>
      <p class="muted">${esc(e.message || String(e))}</p>
      <button class="btn secondary" id="btn-members-retry">Retry</button></div>`;
    $('btn-members-retry').addEventListener('click', () => openCollection(ratingKey));
    return; // keep the error panel — don't let renderMembers() overwrite it
  }
  if (gen !== state.loadGen) return; // superseded while rendering
  const d = $('collection-detail');
  const art = $('detail-art');
  if (artSrc(collection.thumb)) { art.src = artSrc(collection.thumb, 600); art.style.display = ''; }
  else { art.removeAttribute('src'); art.style.display = 'none'; }
  $('detail-kind').textContent = `${collection.smart ? 'Smart' : 'Manual'} collection`;
  // v1.14: a smart collection shows the rule Plex stored for it (the
  // server exposes it as the record's content URI; plex.SmartRuleSummary
  // renders it in plain words). Member wording says "current" —
  // smart membership is dynamic.
  $('detail-sub').textContent = collection.smart
    ? `${collection.childCount} current matches${collection.smartSummary ? ` · Rules: ${collection.smartSummary}` : ''}`
    : `${collection.childCount} titles`;
  $('detail-title').textContent = collection.title;
  $('detail-summary').textContent = collection.summary || '';
  $('members-search').value = '';
  renderMembers('');
  d.scrollIntoView({ behavior: 'smooth', block: 'start' });
}

function renderMembers(filter) {
  const q = filter.trim().toLowerCase();
  const items = (state.detail?.items || []).filter((i) => !q || i.title.toLowerCase().includes(q));
  $('members-grid').innerHTML = items.map((i) =>
    `<div class="poster-card"><div class="poster-frame">${
      artSrc(i.thumb) ? `<img src="${esc(artSrc(i.thumb))}" alt="" loading="lazy" onerror="this.outerHTML='<div class=&quot;poster-fallback&quot;>${esc((i.title || '?')[0].toUpperCase())}</div>'">` : ''
    }${artSrc(i.thumb) ? '' : `<div class="poster-fallback">${esc((i.title || '?')[0].toUpperCase())}</div>`}
    </div><p class="poster-title">${esc(i.title)}</p><p class="poster-sub">${esc([i.year, i.type].filter(Boolean).join(' · '))}</p></div>`
  ).join('') || '<p class="muted">No members match.</p>';
}
$('members-search').addEventListener('input', (e) => renderMembers(e.target.value));
$('btn-back-to-grid').addEventListener('click', () => {
  state.detail = null;
  $('collection-detail').hidden = true;
  $('collections-grid').hidden = false;
  $('collections-toolbar').hidden = false;
  renderCollectionsGrid();
});
$('btn-new-collection').addEventListener('click', () => { openBuilderNew(); switchTab('builder'); });
$('btn-empty-new').addEventListener('click', () => { openBuilderNew(); switchTab('builder'); });
$('btn-detail-edit').addEventListener('click', () => { openBuilderEdit(state.detail.collection, state.detail.items); switchTab('builder'); });
$('btn-detail-duplicate').addEventListener('click', () => { openBuilderDuplicate(state.detail.collection, state.detail.items); switchTab('builder'); });
// v0.4: jump to the Overlays tab with this collection pre-selected as the
// apply target (workstream 5's template flow feeds into the same view).
$('btn-detail-overlay').addEventListener('click', () => {
  const c = state.detail && state.detail.collection;
  if (!c) return;
  if (window.PCMOverlays && window.PCMOverlays.applyToCollection) window.PCMOverlays.applyToCollection(c.ratingKey, c.title);
  else toast('Overlays tab is not ready yet.', 'error');
});
// Deletion is DISABLED — permanent product boundary (AGENTS.md). The button
// stays in the DOM (hidden) for layout parity with v0.3.1; the guard in
// plex-client.js rejects any delete call.
$('btn-detail-delete').hidden = true;
$('btn-detail-delete').disabled = true;

// Shared by the detail-view Delete button and the per-card Delete button.
// Confirm dialog states plainly: the collection grouping is removed, media
// files and library items are NEVER deleted (Plex collections are virtual
// groupings — deleting one cannot touch media).
// ---------- BUILDER tab ----------------------------------------------------------
// draft = working copy. original = snapshot of the live collection for edit
// mode (null for new). The dry-run diffs draft against original.

const draft = {
  mode: 'new', ratingKey: null,
  original: null, // { members:[{ratingKey,title,thumb,year,type}], fields:{...} }
  members: [],    // [{ratingKey,title,thumb,year,type}]
  rules: [],      // [{field, op, value}]
  // importReport: the last list-import preview that seeded this
  // draft ({listTitle, source, matched, unmatched entries…}) — the
  // dry-run renders it so unmatched entries stay visible BEFORE any
  // write. Cleared whenever the draft is reset or re-targeted.
  importReport: null,
};

const RULE_FIELDS = {
  genre:          { label: 'Genre', ops: ['is'], hint: 'Exact genre name as shown in Plex, e.g. Action' },
  year:           { label: 'Year', ops: ['is', 'from', 'to'], hint: 'e.g. 1999' },
  decade:         { label: 'Decade', ops: ['is'], hint: 'e.g. 1980' },
  contentRating:  { label: 'Content rating', ops: ['is'], hint: 'e.g. PG-13, R' },
  studio:         { label: 'Studio', ops: ['is'], hint: 'e.g. Pixar' },
  actor:          { label: 'Actor', ops: ['is'], hint: 'e.g. Tom Hanks' },
  director:       { label: 'Director', ops: ['is'], hint: 'e.g. Denis Villeneuve' },
  audienceRating: { label: 'Audience rating ≥', ops: ['min'], hint: 'Field name verified on PMS 1.43 — Preview shows the true matches' },
  criticRating:   { label: 'Critic rating ≥', ops: ['min'], hint: "Plex's own critic score, e.g. 8" },
  durationMax:    { label: 'Runtime ≤ (minutes)', ops: ['atMost'], hint: 'e.g. 100' },
  addedAt:        { label: 'Added within (days)', ops: ['within'], hint: 'e.g. 60' },
  unwatched:      { label: 'Unwatched only', ops: ['yes'], hint: '' },
};

// Build the Plex advanced-filter query string from the rule rows.
// FUTURE AI: this string is shown verbatim in the UI (#filter-query-preview)
// and executed by the wizard's live match preview (getSectionItemsPage
// with this filter) BEFORE anything is created. If Plex changes
// filter syntax, THIS function is where you adapt — keep it pure (no I/O)
// so it's trivially testable.
function buildFilterQuery() {
  const parts = [];
  for (const r of draft.rules) {
    const v = (r.value || '').trim();
    if (r.field === 'unwatched') { parts.push('unwatched=1'); continue; }
    if (!v) continue;
    const enc = encodeURIComponent(v);
    switch (r.field) {
      case 'genre': parts.push(`genre=${enc}`); break;
      case 'contentRating': parts.push(`contentRating=${enc}`); break;
      case 'studio': parts.push(`studio=${enc}`); break;
      case 'actor': parts.push(`actor=${enc}`); break;
      case 'director': parts.push(`director=${enc}`); break;
      case 'decade': parts.push(`decade=${enc}`); break;
      case 'audienceRating': parts.push(`audienceRating>>=${enc}`); break; // probed valid on PMS 1.43.5
      case 'criticRating': parts.push(`rating>=${enc}`); break; // Plex's critic score field is `rating` (probed)
      case 'durationMax': { // Plex durations are milliseconds (probed)
        const mins = parseFloat(v);
        if (!Number.isNaN(mins) && mins > 0) parts.push(`duration<<=${Math.round(mins * 60000)}`);
        break;
      }
      case 'addedAt': { // relative window: addedAt>=-60d (probed)
        const days = parseInt(v, 10);
        if (!Number.isNaN(days) && days > 0) parts.push(`addedAt>>=-${days}d`);
        break;
      }
      case 'year':
        if (r.op === 'is') parts.push(`year=${enc}`);
        else if (r.op === 'from') parts.push(`year>>=${enc}`); // probed: >>= is STRICT (excludes the year itself)
        else if (r.op === 'to') parts.push(`year<<=${enc}`);   // probed valid on PMS 1.43.5
        break;
    }
  }
  return parts.join('&');
}

// Invalidate the reviewed snapshot whenever rules or members change.
function invalidateDryRun() {
  $('btn-apply').disabled = true;
  $('dryrun-output')._diff = null;
}

let validatedSmartPreview = null;

// --- Builder wizard (v1.17) --------------------------------------------------
// The Builder is a four-step wizard over the SAME draft + dry-run +
// apply discipline as before: 1 Start (source) → 2 Library & members →
// 3 Details → 4 Review & create. `wizard` holds only presentation and
// paging state; `draft` remains the creation state of record, so
// buildDryRun / renderDryRun / the apply handler keep their meaning.
// Abandoning mid-wizard writes nothing: the only write path is still
// the Step 4 apply, after the dry-run and the confirm modal.
const WZ_GRID_SIZE = 60;
const wizard = {
  step: 1,
  source: 'blank', // 'blank' | 'rules' | 'import'
  gridItems: [], gridTotal: 0, gridTotalKnown: false, gridHasMore: false,
  gridQuery: '', gridLoadedFor: '', gridRequest: 0,
  ruleItems: [], ruleTotal: 0, ruleTotalKnown: false, ruleRequest: 0,
  importShown: WZ_GRID_SIZE,
  materializedFor: '', // smartKey() the current draft.members were materialized from (rules → static)
  materializeError: '', // last materializeRuleMatches failure ('' = none) — read by getDraftPosterSource
};
let wzRuleTimer = null, wzGridTimer = null;

function slimWizardItem(m) {
  return { ratingKey: String(m.ratingKey), title: m.title, thumb: m.thumb || '', year: m.year ?? null, type: m.type || '' };
}
function wizardSectionKey() { return $('b-section').value || state.sectionKey || ''; }
function wizardType() { return $('b-type').value; }
function smartKey() { return JSON.stringify([state.machine, wizardSectionKey(), wizardType(), buildFilterQuery()]); }

// The static/smart choice (Details step). #b-smart stays the field of
// record — readBuilderFields and the mode text read it; the radios are
// its visible form, offered when the source is rules.
function syncKindControls() {
  const smart = $('b-smart').checked;
  const rs = $('wz-kind-static'), rm = $('wz-kind-smart');
  if (rs) rs.checked = !smart;
  if (rm) rm.checked = smart;
  const choice = $('wz-kind-choice'), note = $('wz-kind-note');
  if (choice && note) {
    const showChoice = wizard.source === 'rules' && draft.mode !== 'edit';
    choice.hidden = !showChoice;
    note.hidden = showChoice;
  }
}
function setSmartChecked(on) {
  $('b-smart').checked = !!on;
  syncKindControls();
  renderBuilderMode();
  invalidateDryRun();
  if (!on && wizard.source === 'rules') {
    // Static-from-rules members are the FULL match list, fetched when
    // the review is computed — never just the preview page.
    wizard.materializedFor = '';
  }
  if (on && wizard.materializedFor) {
    // A smart collection stores rules, not a member list: drop the
    // materialized matches so the create payload carries none.
    draft.members = [];
    wizard.materializedFor = '';
    renderDraftMembers();
  }
}

// --- wizard chrome + navigation ---------------------------------------------
function renderWizardSource() {
  document.querySelectorAll('input[name="wz-source"]').forEach((r) => {
    r.checked = r.value === wizard.source;
    r.disabled = draft.mode === 'edit';
  });
  $('wz-edit-note').hidden = draft.mode !== 'edit';
  $('wz-blank-panel').hidden = wizard.source !== 'blank';
  $('wz-rules-panel').hidden = wizard.source !== 'rules';
  $('wz-import-panel').hidden = wizard.source !== 'import';
  syncKindControls();
}
function renderWizardChrome() {
  document.querySelectorAll('#wizard-steps .wz-step-btn').forEach((b) => {
    const n = +b.dataset.wzGoto;
    b.classList.toggle('active', n === wizard.step);
    b.classList.toggle('done', n < wizard.step);
    if (n === wizard.step) b.setAttribute('aria-current', 'step');
    else b.removeAttribute('aria-current');
  });
  for (let n = 1; n <= 4; n++) $('wz-step-' + n).hidden = n !== wizard.step;
  $('btn-wizard-back').disabled = wizard.step === 1;
  $('btn-wizard-next').hidden = wizard.step === 4;
  $('btn-apply').textContent = draft.mode === 'edit' ? 'Apply changes' : 'Create collection';
  renderWizardSource();
}
function setWizardSource(source, { reset = false } = {}) {
  if (draft.mode === 'edit') source = 'blank';
  const changed = source !== wizard.source;
  wizard.source = source;
  if (reset && changed) {
    // Switching source by hand starts that path clean — members or
    // rules from another path must never leak into a create payload.
    draft.members = []; draft.rules = []; draft.importReport = null;
    draft.importSeedMembers = []; draft.importRemoved = [];
    wizard.materializedFor = ''; wizard.materializeError = ''; wizard.gridLoadedFor = '';
    const irb = $('import-results'); if (irb) irb.innerHTML = '';
    if (source !== 'rules') $('b-smart').checked = false;
    renderRules(); renderDraftMembers(); renderImportGrid();
  }
  renderWizardSource();
}
function wizardStepError(msg) {
  const el = $('wizard-step-error');
  el.hidden = !msg;
  el.textContent = msg || '';
}
async function wizardGoTo(target) {
  target = Math.max(1, Math.min(4, target));
  wizardStepError('');
  if (target > wizard.step) {
    // Gate: leaving Members with the rules source needs ≥1 real rule.
    if (wizard.step <= 2 && target >= 3 && wizard.source === 'rules' && draft.mode !== 'edit' && !buildFilterQuery()) {
      wizard.step = 2; renderWizardChrome();
      wizardStepError('Add at least one complete rule before continuing — a rules collection needs rules.');
      return;
    }
    // Gate: the title is required, validated at Details (Step 3).
    if (wizard.step <= 3 && target >= 4 && !$('b-title').value.trim()) {
      wizard.step = 3; renderWizardChrome();
      const de = $('wz-details-error');
      de.hidden = false;
      de.textContent = 'Give the collection a title — it is required before the review.';
      $('b-title').focus();
      return;
    }
  }
  wizard.step = target;
  renderWizardChrome();
  if (target === 2) await wizardEnterMembers();
  if (target === 4) await wizardEnterReview();
}
async function wizardEnterMembers() {
  if (wizard.source === 'blank') await loadWizardGrid(true);
  else if (wizard.source === 'rules') refreshRulePreview();
  else if (wizard.source === 'import') renderImportGrid();
}
async function wizardEnterReview() {
  const re = $('wz-review-error');
  re.hidden = true; re.textContent = '';
  if (wizard.source === 'rules' && draft.mode !== 'edit' && !$('b-smart').checked) {
    await materializeRuleMatches();
  }
  renderDryRun(buildDryRun());
}

// --- blank path: the library poster grid --------------------------------------
function wizardGridKey() { return JSON.stringify([state.machine, wizardSectionKey(), wizardType(), wizard.gridQuery]); }
async function loadWizardGrid(reset) {
  if (wizard.source !== 'blank') return;
  const key = wizardGridKey();
  if (reset && wizard.gridLoadedFor === key && wizard.gridItems.length) { renderWizardGrid(); return; }
  const start = reset ? 0 : wizard.gridItems.length;
  const req = ++wizard.gridRequest;
  $('wz-grid-counts').textContent = 'Loading…';
  try {
    const res = await call(window.pcm.getSectionItemsPage(wizardSectionKey(), {
      start, size: WZ_GRID_SIZE, title: wizard.gridQuery, type: wizardType(),
    }));
    if (req !== wizard.gridRequest) return; // superseded by a newer load
    const items = (res.items || []).map(slimWizardItem);
    wizard.gridItems = reset ? items
      : [...wizard.gridItems, ...items.filter((i) => !wizard.gridItems.some((g) => g.ratingKey === i.ratingKey))];
    wizard.gridTotal = res.total || 0;
    wizard.gridTotalKnown = !!res.totalKnown;
    wizard.gridHasMore = !!res.hasMore;
    wizard.gridLoadedFor = key;
    renderWizardGrid();
  } catch (e) {
    if (req !== wizard.gridRequest) return;
    $('wz-grid-counts').textContent = '';
    toastErr(e);
  }
}
function wizardCardHTML(i, selected) {
  return `<div class="poster-card wz-card ${selected ? 'selected' : ''}" data-wz-rk="${esc(String(i.ratingKey))}" role="button" tabindex="0" aria-pressed="${selected}" title="${esc(i.title)}">
    ${posterHTML(i, [i.year, i.type].filter(Boolean).join(' · '))}
    <span class="wz-check" aria-hidden="true">✓</span>
  </div>`;
}
function wireWizardCards(grid) {
  grid.querySelectorAll('[data-wz-rk]').forEach((el) => {
    const toggle = () => toggleWizardMember(el.dataset.wzRk);
    el.addEventListener('click', toggle);
    el.addEventListener('keydown', (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggle(); } });
  });
}
function renderWizardGrid() {
  const grid = $('wz-member-grid');
  const selected = new Set(draft.members.map((m) => String(m.ratingKey)));
  grid.innerHTML = wizard.gridItems.map((i) => wizardCardHTML(i, selected.has(i.ratingKey))).join('')
    || '<p class="muted">No titles found in this library.</p>';
  wireWizardCards(grid);
  $('wz-grid-counts').textContent = wizard.gridTotalKnown
    ? `Showing ${wizard.gridItems.length} of ${wizard.gridTotal} titles`
    : `Loaded ${wizard.gridItems.length} titles${wizard.gridHasMore ? ' — more available' : ''}`;
  $('wz-grid-more').hidden = !wizard.gridHasMore;
}

// --- selection model ------------------------------------------------------------
// draft.members IS the selection (in creation order). The grid, the
// tray and the selected list are three views of it, so a selection
// survives paging and searching by construction.
function findWizardItem(rk) {
  rk = String(rk);
  return wizard.gridItems.find((i) => i.ratingKey === rk)
    || wizard.ruleItems.find((i) => i.ratingKey === rk)
    || (draft.importSeedMembers || []).find((i) => String(i.ratingKey) === rk)
    || (draft.importRemoved || []).find((i) => String(i.ratingKey) === rk)
    || draft.members.find((i) => String(i.ratingKey) === rk);
}
function toggleWizardMember(rk) {
  rk = String(rk);
  const idx = draft.members.findIndex((m) => String(m.ratingKey) === rk);
  if (idx >= 0) {
    const [removed] = draft.members.splice(idx, 1);
    noteImportRemoval(removed, true);
  } else {
    const item = findWizardItem(rk);
    if (!item) return;
    draft.members.push(slimWizardItem(item));
    noteImportRemoval(item, false);
    if (wizard.source === 'import' && (draft.importSeedMembers || []).length) {
      // Import order is list order: restoring a removed match re-seats
      // it at its list position rather than the end of the draft.
      const order = new Map(draft.importSeedMembers.map((m, i) => [String(m.ratingKey), i]));
      draft.members.sort((a, b) => (order.get(String(a.ratingKey)) ?? 0) - (order.get(String(b.ratingKey)) ?? 0));
    }
  }
  renderDraftMembers();
  syncWizardSelectionUI();
}
// Keep the import report truthful as matched items are removed or
// restored in the grid: matchedCount/notImported move with the draft,
// and a removal shows in the report (and the Step 4 review) as a SKIP
// line with its reason.
function noteImportRemoval(item, removed) {
  const rep = draft.importReport;
  if (!rep || wizard.source !== 'import' || !item) return;
  const rk = String(item.ratingKey);
  if (!(draft.importSeedMembers || []).some((m) => String(m.ratingKey) === rk)) return;
  draft.importRemoved = draft.importRemoved || [];
  const already = draft.importRemoved.some((m) => String(m.ratingKey) === rk);
  if (removed && !already) {
    draft.importRemoved.push(slimWizardItem(item));
    rep.matchedCount = Math.max(0, (rep.matchedCount || 0) - 1);
    rep.notImported = (rep.notImported || 0) + 1;
    rep.skipped = [...(rep.skipped || []), { title: item.title, year: item.year ?? null, reason: 'Removed before creation', ratingKey: rk }];
  } else if (!removed && already) {
    draft.importRemoved = draft.importRemoved.filter((m) => String(m.ratingKey) !== rk);
    rep.matchedCount = (rep.matchedCount || 0) + 1;
    rep.notImported = Math.max(0, (rep.notImported || 0) - 1);
    rep.skipped = (rep.skipped || []).filter((e) => !(e.ratingKey === rk && e.reason === 'Removed before creation'));
  }
  renderImportReport();
}
function syncWizardSelectionUI() {
  const selected = new Set(draft.members.map((m) => String(m.ratingKey)));
  document.querySelectorAll('#wz-member-grid [data-wz-rk], #wz-import-grid [data-wz-rk]').forEach((el) => {
    const on = selected.has(el.dataset.wzRk);
    el.classList.toggle('selected', on);
    el.setAttribute('aria-pressed', String(on));
  });
}
function removeDraftMemberAt(idx) {
  const [removed] = draft.members.splice(idx, 1);
  if (removed) noteImportRemoval(removed, true);
  renderDraftMembers();
  syncWizardSelectionUI();
}

// --- rules path: live match preview (read-only) ---------------------------------
function scheduleRulePreview() {
  if (wizard.source !== 'rules' || wizard.step !== 2) return;
  clearTimeout(wzRuleTimer);
  wzRuleTimer = setTimeout(() => refreshRulePreview(), 350);
}
async function refreshRulePreview() {
  const q = buildFilterQuery();
  const grid = $('rule-preview-grid'), count = $('wz-rule-count');
  if (!q) {
    wizard.ruleItems = []; wizard.ruleTotal = 0;
    grid.innerHTML = '<p class="muted">Add a rule to see what Plex matches.</p>';
    count.textContent = '';
    return;
  }
  const req = ++wizard.ruleRequest;
  count.textContent = 'Checking matches on your server…';
  try {
    const res = await call(window.pcm.getSectionItemsPage(wizardSectionKey(), {
      start: 0, size: WZ_GRID_SIZE, filterQuery: q, type: wizardType(),
    }));
    if (req !== wizard.ruleRequest) return;
    wizard.ruleItems = (res.items || []).map(slimWizardItem);
    wizard.ruleTotal = res.total || 0;
    wizard.ruleTotalKnown = !!res.totalKnown;
    // A successful preview is what validates smart rules for the
    // dry-run — the same discipline as the old Preview button.
    validatedSmartPreview = smartKey();
    wizard.materializedFor = '';
    grid.innerHTML = wizard.ruleItems.map((i) =>
      `<div class="poster-card">${posterHTML(i, [i.year, i.type].filter(Boolean).join(' · '))}</div>`).join('')
      || '<p class="muted">No titles match these rules on your server.</p>';
    count.textContent = wizard.ruleTotalKnown
      ? `${wizard.ruleTotal} match${wizard.ruleTotal === 1 ? '' : 'es'} — showing the first ${wizard.ruleItems.length}`
      : `${wizard.ruleItems.length} matches shown${res.hasMore ? ' (more on the server)' : ''}`;
    invalidateDryRun();
  } catch (e) {
    if (req !== wizard.ruleRequest) return;
    count.textContent = '';
    toastErr(e);
  }
}
// Static-from-rules: the member list is EVERY current match, fetched
// through the existing full enumeration — never the preview page.
async function materializeRuleMatches() {
  const q = buildFilterQuery();
  if (!q) return;
  const key = smartKey();
  if (wizard.materializedFor === key) return;
  try {
    const { items } = await call(window.pcm.getSectionItems(wizardSectionKey(), q, wizardType()));
    draft.members = (items || []).map(slimWizardItem);
    wizard.materializedFor = key;
    wizard.materializeError = '';
    renderDraftMembers();
  } catch (e) { wizard.materializeError = (e && e.message) || String(e); toastErr(e); }
}

// getDraftPosterSource — the poster generator's ONE door to the
// Builder draft (v1.21 rider, owner's live report). The generator
// used to snapshot draft.members itself; for a RULES draft that
// array is empty by design — a smart collection stores rules, not
// members, and a static-from-rules draft materializes its matches
// only at review — so a draft whose preview read "203 matches"
// opened the generator with ZERO members. The member source now
// follows the draft kind:
//   - rules drafts (smart OR static): the FULL current match set,
//     enumerated by the same call the review step uses
//     (materializeRuleMatches → getSectionItems), never the 60-item
//     preview page in wizard.ruleItems;
//   - hand-picked / import / edit drafts: draft.members as-is.
// Zero matches, no rules yet, and a failed enumeration all come
// back as an honest sourceNote with an empty member list — the
// generator shows the note instead of presenting a memberless
// collection. Nothing here writes to Plex.
async function getDraftPosterSource() {
  const title = ($('b-title').value || '').trim();
  const slim = (m) => ({ ratingKey: m.ratingKey, title: m.title, thumb: m.thumb });
  if (wizard.source === 'rules' && draft.mode !== 'edit') {
    if (!buildFilterQuery()) {
      return { title, members: [], sourceNote: 'No rules yet — add a rule in the Builder and the generator will build from its matches.' };
    }
    await materializeRuleMatches();
    if (wizard.materializedFor !== smartKey()) {
      return { title, members: [], sourceNote: 'The matches for these rules could not be loaded' +
        (wizard.materializeError ? ` — ${wizard.materializeError}` : '') +
        '. Nothing was changed; fix that and reopen the generator.' };
    }
    if (!draft.members.length) {
      return { title, members: [], sourceNote: 'Your rules currently match nothing on the server, so there are no member posters to build from. Loosen a rule, or use an uploaded, linked, or AI-generated background instead.' };
    }
    const n = draft.members.length;
    return { title, members: draft.members.map(slim), sourceNote: `${n} match${n === 1 ? '' : 'es'} from your rules` };
  }
  return { title, members: draft.members.map(slim), sourceNote: '' };
}

// --- import path grid ---------------------------------------------------------
// Matched items render pre-selected; removed matches stay visible
// (unselected) so a removal can be undone with one click.
function renderImportGrid() {
  const grid = $('wz-import-grid');
  if (!grid) return;
  const all = [...draft.members, ...(draft.importRemoved || [])];
  const shown = all.slice(0, wizard.importShown);
  const selected = new Set(draft.members.map((m) => String(m.ratingKey)));
  grid.innerHTML = shown.map((i) => wizardCardHTML(i, selected.has(String(i.ratingKey)))).join('')
    || '<p class="muted">Preview an import above — matched titles appear here, pre-selected.</p>';
  wireWizardCards(grid);
  $('wz-import-more').hidden = all.length <= wizard.importShown;
}

// --- wizard control wiring ------------------------------------------------------
document.querySelectorAll('input[name="wz-source"]').forEach((r) =>
  r.addEventListener('change', () => { if (r.checked) { setWizardSource(r.value, { reset: true }); renderBuilderMode(); } }));
document.querySelectorAll('input[name="wz-kind"]').forEach((r) =>
  r.addEventListener('change', () => { if (r.checked) setSmartChecked(r.value === 'smart'); }));
$('btn-wizard-back').addEventListener('click', () => wizardGoTo(wizard.step - 1));
$('btn-wizard-next').addEventListener('click', () => wizardGoTo(wizard.step + 1));
document.querySelectorAll('#wizard-steps .wz-step-btn').forEach((b) =>
  b.addEventListener('click', () => wizardGoTo(+b.dataset.wzGoto)));
$('wz-grid-more').addEventListener('click', () => loadWizardGrid(false));
$('wz-import-more').addEventListener('click', () => { wizard.importShown += WZ_GRID_SIZE; renderImportGrid(); });
$('wz-grid-search').addEventListener('input', (e) => {
  clearTimeout(wzGridTimer);
  wzGridTimer = setTimeout(() => { wizard.gridQuery = e.target.value.trim(); wizard.gridLoadedFor = ''; loadWizardGrid(true); }, 280);
});
$('btn-clear-selected').addEventListener('click', () => {
  if (!draft.members.length) return;
  const removedAll = [...draft.members];
  draft.members = [];
  for (const m of removedAll) noteImportRemoval(m, true);
  renderDraftMembers();
  syncWizardSelectionUI();
});
$('b-title').addEventListener('input', () => { $('wz-details-error').hidden = true; });

// v0.4 — each smart-rule row explains itself in plain words, e.g.
// \u201cMatches movies where Genre is Action.\u201d Rules combine with AND.
// `typeLabelOverride` (v1.18) lets the Advisor's pop-out describe a
// card's rules against the CARD's library type instead of the
// Builder's current type select; omitted, behavior is unchanged.
function describeRule(r, typeLabelOverride) {
  const typeLabel = typeLabelOverride || ($('b-type').selectedOptions[0] || {}).textContent || '';
  const kind = /show/i.test(typeLabel) ? 'shows' : 'movies';
  if (r.field === 'addedAt') return `Matches ${kind} <strong>added in the last ${esc(r.value || '…')} days</strong>.`;
  if (r.field === 'durationMax') return `Matches ${kind} with a <strong>runtime of ${esc(r.value || '…')} minutes or less</strong>.`;
  if (r.field === 'criticRating') return `Matches ${kind} with a <strong>critic rating of ${esc(r.value || '…')} or more</strong>.`;
  const f = (RULE_FIELDS[r.field] || {}).label || r.field;
  const v = r.field === 'unwatched' ? '' : ` <strong>${esc(r.value || '\u2026')}</strong>`;
  return `Matches ${kind} where <strong>${esc(f)}</strong> ${esc(r.op)}${v}.`;
}

// v0.4 — Step 1 of the guided Builder flow: say in plain language what the
// user is making right now. Called whenever the builder opens or the smart
// toggle changes.
function renderBuilderMode() {
  const el = $('builder-mode-desc');
  if (!el) return;
  const smart = $('b-smart').checked;
  let html;
  if (draft.mode === 'edit') {
    html = `You're <strong>editing an existing collection</strong> \u2014 adjust the members in Step 2 or the details in Step 3, then <strong>Step 4 (Review &amp; create)</strong> shows exactly what will change before anything is applied.`;
  } else if (draft.seededFrom === 'advisor') {
    html = `You're making a <strong>new manual collection from an Advisor suggestion</strong> \u2014 the members are pre-selected in Step 2's grid. Adjust them, set the title in Step 3, then review in Step 4.`;
  } else if (draft.seededFrom === 'advisor-smart') {
    html = `You're making a <strong>new smart collection from an Advisor suggestion</strong> — the suggestion's rules are pre-filled in Step 2 and Plex will keep the collection in sync as your library changes (no fixed member list is stored). Check the live match preview, then review in Step 4.`;
  } else if (draft.seededFrom === 'ai-rules') {
    html = `You're making a <strong>new collection from AI-proposed rules</strong> \u2014 the rules are editable in Step 2. The live preview shows only matches Plex confirms are real.`;
  } else if (wizard.source === 'rules') {
    html = smart
      ? `You're making a <strong>new smart collection</strong> \u2014 the rules in Step 2 decide the members, and Plex keeps it updated as your library changes. The match preview updates live as you edit.`
      : `You're making a <strong>new collection from rules</strong> \u2014 Step 2's live preview shows what Plex matches; in Step 3 you choose a fixed (static) member list or a smart collection.`;
  } else if (wizard.source === 'import') {
    html = `You're making a <strong>new collection from a list</strong> \u2014 Step 2 previews the import and seeds the matched titles in list order; unmatched entries are reported with their reasons. Nothing is created until Step 4's review is confirmed.`;
  } else {
    html = `You're making a <strong>new manual collection</strong> \u2014 you pick every member from the poster grid in Step 2. Nothing is created in Plex until Step 4's review is confirmed.`;
  }
  el.innerHTML = html;
  syncKindControls();
}

function renderRules() {
  validatedSmartPreview = null;
  invalidateDryRun();
  const list = $('rule-list');
  list.innerHTML = draft.rules.map((r, i) => {
    const f = RULE_FIELDS[r.field];
    const ops = f.ops.map((o) => `<option value="${o}" ${o === r.op ? 'selected' : ''}>${o}</option>`).join('');
    const valInput = r.field === 'unwatched'
      ? `<input class="input" disabled value="—">`
      : r.field === 'decade'
        ? `<select class="input select" data-i="${i}" data-k="value">${[1950, 1960, 1970, 1980, 1990, 2000, 2010, 2020].map((d) => `<option ${String(d) === r.value ? 'selected' : ''}>${d}</option>`).join('')}</select>`
        : `<input class="input" data-i="${i}" data-k="value" value="${esc(r.value)}" placeholder="${esc(f.hint)}" autocomplete="off">`;
    return `<div class="rule-row-wrap">
      <div class="rule-row">
        <select class="input select" data-i="${i}" data-k="field">${Object.entries(RULE_FIELDS).map(([k, v]) => `<option value="${k}" ${k === r.field ? 'selected' : ''}>${v.label}</option>`).join('')}</select>
        <select class="input select" data-i="${i}" data-k="op">${ops}</select>
        ${valInput}
        <button class="btn ghost small" data-del="${i}" title="Remove rule">✕</button>
      </div>
      <p class="rule-desc">${describeRule(r)}</p>
    </div>`;
  }).join('') || '<p class="muted small">No rules yet. Add one and the live match preview shows what Plex matches.</p>';

  list.querySelectorAll('[data-i]').forEach((el) => el.addEventListener('change', () => {
    const i = +el.dataset.i, k = el.dataset.k;
    draft.rules[i][k] = el.value;
    if (k === 'field') draft.rules[i].op = RULE_FIELDS[el.value].ops[0]; // reset op to valid set
    renderRules();
  }));
  list.querySelectorAll('[data-del]').forEach((b) => b.addEventListener('click', () => { draft.rules.splice(+b.dataset.del, 1); renderRules(); }));
  $('filter-query-preview').textContent = buildFilterQuery() || '(no rules)';
  scheduleRulePreview();
}
$('btn-add-rule').addEventListener('click', () => { draft.rules.push({ field: 'genre', op: 'is', value: '' }); renderRules(); });

// The Preview button forces an immediate refresh of the same live
// preview that updates as rules change (v1.17). The preview is
// read-only: matches are computed by Plex, never picked by hand. A
// static collection from rules materializes the FULL match list at
// review time (materializeRuleMatches) — not this first page.
$('btn-preview-rules').addEventListener('click', () => {
  if (!buildFilterQuery()) { toast('Add at least one rule first.'); return; }
  refreshRulePreview();
});

// --- list import (v1.14) -----------------------------------------------------
// Preview a TMDb/Trakt list against the target library and seed the
// draft with the matches (in list order). NOTHING is written here —
// the preview endpoint is read-only; the seeded draft still goes
// through the normal dry-run → confirm → create flow, and the
// dry-run re-renders the import report so the unmatched entries
// (with reasons) are visible immediately before any write.
$('btn-import-preview').addEventListener('click', async () => {
  const source = $('imp-source').value;
  const ref = $('imp-ref').value.trim();
  const box = $('import-results');
  if (!ref) { toast('Paste a list id or URL first.'); return; }
  const sectionKey = $('b-section').value;
  const type = $('b-type').value;
  box.innerHTML = '<p class="muted small">Fetching the list and matching it against this library…</p>';
  try {
    const res = await call(window.pcm.importPreviewList(source, ref, sectionKey, type));
    draft.members = (res.members || []).map((m) => ({
      ratingKey: String(m.ratingKey), title: m.title, thumb: m.thumb || '',
      year: m.year || null, type: m.type || '',
    }));
    const skipped = (res.entries || []).filter((e) => e.status !== 'matched')
      .map((e) => ({ title: e.title, year: e.year, reason: e.reason || e.status }));
    draft.importReport = {
      listTitle: res.listTitle || '', source: res.source,
      matchedCount: res.matchedCount, totalEntries: res.totalEntries,
      notImported: skipped.length, skipped, titleCollision: !!res.titleCollision,
      authUsed: !!res.authUsed,
    };
    // Wizard grid bookkeeping (v1.17): the seed list is the removal /
    // restore ledger for the import grid — matched items render
    // pre-selected and can be removed (and restored) before creating.
    draft.importSeedMembers = draft.members.map((m) => ({ ...m }));
    draft.importRemoved = [];
    wizard.importShown = WZ_GRID_SIZE;
    if (!$('b-title').value.trim() && res.listTitle) $('b-title').value = String(res.listTitle).slice(0, 120);
    renderDraftMembers();
    renderImportReport();
    renderImportGrid();
    toast(`Matched ${res.matchedCount} of ${res.totalEntries} list entries — review the dry-run in Step 4 before applying.`, 'success');
  } catch (e) {
    box.innerHTML = '';
    toastErr(e);
  }
});

function renderImportReport() {
  const box = $('import-results');
  const rep = draft.importReport;
  if (!rep) { box.innerHTML = ''; return; }
  const rows = rep.skipped.map((e) =>
    `<div class="diff-line"><span class="diff-tag remove">SKIP</span><span>${esc(e.title)}${e.year ? ` (${e.year})` : ''} — ${esc(e.reason || '')}</span></div>`).join('');
  box.innerHTML = `
    <p class="muted small" style="margin-top:10px"><strong>${esc(rep.listTitle || 'List')}</strong> (${esc(rep.source)}): ${rep.matchedCount} of ${rep.totalEntries} entries matched your library and were added to the draft in list order.${rep.authUsed ? ' Read with your connected Trakt account (the list is not public).' : ''}</p>
    ${rows}
    ${rep.titleCollision ? '<p class="muted small">A collection with this title already exists in this library — applying creates a second one with the same name (the Collections health view flags duplicates).</p>' : ''}
    <p class="muted small">Nothing has been written to Plex. Step 4's review shows this same report again before you confirm.</p>`;
}

// --- draft members -----------------------------------------------------------
function renderDraftMembers() {
  invalidateDryRun();
  $('member-count').textContent = draft.members.length;
  $('builder-draft-pill').hidden = !draft.members.length;
  $('builder-draft-pill').textContent = draft.members.length;
  const list = $('draft-list');
  list.innerHTML = draft.members.map((m, i) => `
    <div class="draft-item" draggable="true" data-i="${i}">
      <span class="draft-pos">${i + 1}</span>
      ${artSrc(m.thumb) ? `<img src="${esc(artSrc(m.thumb))}" alt="" loading="lazy" onerror="this.outerHTML='<span class=&quot;thumb-fallback&quot;>${esc((m.title || '?')[0].toUpperCase())}</span>'">` : ''}
      <div class="t"><strong>${esc(m.title)}</strong><span>${esc([m.year, m.type].filter(Boolean).join(' · '))}</span></div>
      <button class="draft-remove" data-rm="${i}" title="Remove">✕</button>
    </div>`).join('') || '<div class="draft-empty">No members selected yet.<br>Click posters in the grid to add them.</div>';
  $('btn-apply').disabled = true; // any draft change invalidates the last diff
  $('dryrun-output').innerHTML = '<p class="muted">Draft changed — re-run the review before applying.</p>';

  list.querySelectorAll('[data-rm]').forEach((b) => b.addEventListener('click', (e) => {
    e.stopPropagation();
    removeDraftMemberAt(+b.dataset.rm);
  }));
  // HTML5 drag reorder. Order matters: it's the creation order for new
  // collections and part of the dry-run diff for edits.
  let dragIdx = null;
  list.querySelectorAll('.draft-item').forEach((el) => {
    el.addEventListener('dragstart', () => { dragIdx = +el.dataset.i; el.classList.add('dragging'); });
    el.addEventListener('dragend', () => el.classList.remove('dragging'));
    el.addEventListener('dragover', (e) => e.preventDefault());
    el.addEventListener('drop', (e) => {
      e.preventDefault();
      const to = +el.dataset.i;
      if (dragIdx === null || dragIdx === to) return;
      const [moved] = draft.members.splice(dragIdx, 1);
      draft.members.splice(to, 0, moved);
      renderDraftMembers();
    });
  });
  syncWizardSelectionUI();
}

// --- dry-run ------------------------------------------------------------------
// Pure diff: draft vs. original (edit mode) or draft vs. empty (new mode).
// FUTURE AI: keep this PURE — no I/O, no IPC. The day someone wants unit
// tests, this is the function they test.
function buildDryRun() {
  const fields = readBuilderFields();
  const origMembers = draft.original ? draft.original.members : [];
  const origKeys = origMembers.map((m) => m.ratingKey);
  const draftKeys = draft.members.map((m) => m.ratingKey);
  const origSet = new Set(origKeys), draftSet = new Set(draftKeys);

  const added = draft.members.filter((m) => !origSet.has(m.ratingKey));
  const removed = origMembers.filter((m) => !draftSet.has(m.ratingKey));
  // Moved = present in both but at a different index.
  const moved = draft.members.filter((m) => {
    if (!origSet.has(m.ratingKey)) return false;
    return origKeys.indexOf(m.ratingKey) !== draftKeys.indexOf(m.ratingKey);
  });

  const metaChanged = [];
  if (fields.posterUrl) metaChanged.push('Set poster from ' + fields.posterUrl);
  if (fields.smart) metaChanged.push('Smart filter: ' + fields.filterQuery);
  if (draft.mode === 'new') {
    metaChanged.unshift(`Create “${fields.title || '(untitled)'}” as a ${fields.smart ? 'smart' : 'manual'} collection`);
    if (fields.contentRating) metaChanged.push('Content rating: ' + fields.contentRating);
  } else if (draft.original) {
    const o = draft.original.fields;
    if (fields.title !== o.title) metaChanged.push(`Title: “${o.title}” → “${fields.title}”`);
    if (fields.titleSort !== o.titleSort) metaChanged.push(`Sort title: “${o.titleSort || '—'}” → “${fields.titleSort || '—'}”`);
    if (fields.summary !== o.summary) metaChanged.push('Summary text changed');
    if (fields.contentRating !== (o.contentRating || '')) metaChanged.push(`Content rating: “${o.contentRating || '—'}” → “${fields.contentRating || '—'}”`);
    if (fields.smart !== o.smart) metaChanged.push(`Kind: ${o.smart ? 'smart' : 'manual'} → ${fields.smart ? 'smart' : 'manual'}`);
    for (const k of ['visHome', 'visShared', 'visRecommended']) {
      if (!!fields[k] !== !!o[k]) metaChanged.push(`${k} → ${fields[k] ? 'on' : 'off'}`);
    }
  }
  const memberOrderChanged = added.length > 0 || removed.length > 0 || moved.length > 0;
  return { added, removed, moved, metaChanged, memberOrderChanged, fields, isNew: draft.mode === 'new' };
}

function readBuilderFields() {
  return {
    machine: state.machine,
    title: $('b-title').value.trim(),
    titleSort: $('b-titlesort').value.trim(),
    summary: $('b-summary').value.trim(),
    contentRating: $('b-content-rating').value.trim(),
    sectionKey: $('b-section').value,
    type: $('b-type').value,
    smart: $('b-smart').checked,
    visHome: $('b-vis-home').checked,
    visShared: $('b-vis-shared').checked,
    visRecommended: $('b-vis-recommended').checked,
    posterUrl: $('b-poster').value.trim(),
    posterAssetId: window.PCMArt?.selectedFor($('b-poster').value.trim()) || '',
    filterQuery: buildFilterQuery(),
  };
}

function renderDryRun(diff) {
  const out = $('dryrun-output');
  const posters = (items) => items.length
    ? `<div class="diff-posters">${items.slice(0, 12).map((m) => artSrc(m.thumb) ? `<img src="${esc(artSrc(m.thumb))}" alt="" loading="lazy" title="${esc(m.title)}" onerror="this.outerHTML='<span class=&quot;thumb-fallback&quot;>${esc((m.title || '?')[0].toUpperCase())}</span>'">` : '').join('')}${items.length > 12 ? `<span class="muted small">+${items.length - 12} more</span>` : ''}</div>` : '';
  let html = '';
  if (diff.isNew) html += `<div class="diff-line"><span class="diff-tag add">NEW</span><span>${esc(diff.metaChanged[0] || 'New collection')}</span></div>`;
  if (diff.added.length) html += `<div class="diff-line"><span class="diff-tag add">ADD ${diff.added.length}</span><span>${diff.added.slice(0, 6).map((m) => esc(m.title)).join(', ')}${diff.added.length > 6 ? `, +${diff.added.length - 6} more` : ''}</span></div>${posters(diff.added)}`;
  if (diff.removed.length) html += `<div class="diff-line"><span class="diff-tag remove">REMOVE ${diff.removed.length}</span><span>${diff.removed.slice(0, 6).map((m) => esc(m.title)).join(', ')}${diff.removed.length > 6 ? `, +${diff.removed.length - 6} more` : ''}</span></div>${posters(diff.removed)}`;
  if (diff.moved.length) html += `<div class="diff-line"><span class="diff-tag move">REORDER ${diff.moved.length}</span><span>member order changes</span></div>`;
  for (const mc of diff.metaChanged.slice(diff.isNew ? 1 : 0)) html += `<div class="diff-line"><span class="diff-tag meta">META</span><span>${esc(mc)}</span></div>`;
  if (!diff.added.length && !diff.removed.length && !diff.moved.length && !diff.metaChanged.length) {
    html = '<p class="muted">No changes — the draft matches the live collection.</p>';
  }
  if (draft.importReport) {
    const rep = draft.importReport;
    html += `<div class="diff-line"><span class="diff-tag meta">IMPORT</span><span>List “${esc(rep.listTitle || rep.source)}”: ${rep.matchedCount} of ${rep.totalEntries} entries matched — ${rep.notImported} not imported</span></div>`;
    for (const e of rep.skipped) {
      html += `<div class="diff-line"><span class="diff-tag remove">SKIP</span><span>${esc(e.title)}${e.year ? ` (${e.year})` : ''} — ${esc(e.reason || '')}</span></div>`;
    }
    if (rep.titleCollision) html += '<div class="diff-line"><span class="diff-tag remove">TITLE</span><span>A collection with this title already exists in this library — applying creates a second one with the same name.</span></div>';
  }
  out.innerHTML = html;
  const hasWork = diff.added.length + diff.removed.length + diff.moved.length + diff.metaChanged.length > 0;
  const smartValidated = !diff.fields.smart || validatedSmartPreview === JSON.stringify([state.machine, diff.fields.sectionKey, diff.fields.type, diff.fields.filterQuery]);
  const artValidated = !diff.fields.posterUrl || !!diff.fields.posterAssetId;
  // Wizard guardrail (v1.17): an empty STATIC creation is blocked here
  // at Review with the reason — a static collection needs members.
  const emptyStatic = diff.isNew && !diff.fields.smart && draft.members.length === 0;
  $('btn-apply').disabled = !hasWork || !diff.fields.title || !smartValidated || !artValidated || emptyStatic;
  if (emptyStatic) out.innerHTML += '<p class="wizard-error">A static collection needs at least one member — go back to Library &amp; members and pick at least one title.</p>';
  if (!artValidated) out.innerHTML += '<p class="muted">Use Find collection art to preview this poster before applying.</p>';
  if (!smartValidated) out.innerHTML += '<p class="muted">Preview these smart rules successfully on this server before applying.</p>';
  if (!diff.fields.title) out.innerHTML += '<p class="muted small" style="margin-top:8px">Give the collection a title before applying.</p>';
  // Mirror the blocking reason into the step's own error line so it
  // is visible even if the diff summary above is long.
  const re = $('wz-review-error');
  if (re) {
    const block = emptyStatic ? 'A static collection needs at least one member — go back to Library & members and pick at least one title.'
      : !diff.fields.title ? 'Give the collection a title in Details before creating.'
      : !smartValidated ? 'Preview these smart rules successfully on this server before creating.'
      : !artValidated ? 'Use Find collection art to preview this poster before creating.' : '';
    re.hidden = !block;
    re.textContent = block;
  }
  out._diff = diff; // stash for applyDraft()
}

$('btn-dryrun').addEventListener('click', async () => {
  // Recompute the review. An empty static draft renders its blocking
  // reason here rather than refusing to compute (v1.17).
  if (wizard.source === 'rules' && draft.mode !== 'edit' && !$('b-smart').checked) await materializeRuleMatches();
  renderDryRun(buildDryRun());
});

// --- apply (the ONLY write path) ------------------------------------------------
$('btn-apply').addEventListener('click', async () => {
  const diff = $('dryrun-output')._diff;
  if (!diff || JSON.stringify(readBuilderFields()) !== JSON.stringify(diff.fields)) { invalidateDryRun(); return; }
  $('btn-apply').disabled = true;
  const summaryBits = [];
  if (diff.added.length) summaryBits.push(`${diff.added.length} added`);
  if (diff.removed.length) summaryBits.push(`${diff.removed.length} removed`);
  if (diff.moved.length) summaryBits.push(`${diff.moved.length} reordered`);
  if (diff.metaChanged.length) summaryBits.push(`${diff.metaChanged.length} setting change(s)`);
  const m = await openModal({
    title: diff.isNew ? 'Create this collection?' : 'Apply these changes?',
    bodyHTML: `<p>This will write to your Plex server: <strong>${esc(summaryBits.join(' · ') || 'no changes')}</strong>.</p>
      <p class="muted small">Write endpoints are community-documented and will be verified against a live server. If Plex rejects a step, you'll see its exact error here — nothing is hidden.</p>`,
    footHTML: `<button class="btn ghost" data-close="no">Cancel</button><button class="btn primary" data-close="yes">${diff.isNew ? 'Create collection' : 'Apply changes'}</button>`,
  }).closed;
  if (m !== 'yes') return;

  if (JSON.stringify(readBuilderFields()) !== JSON.stringify(diff.fields)) { invalidateDryRun(); return; }
  invalidateDryRun();
  const f = diff.fields;
  let createdKey = '';
  try {
    if (diff.isNew) {
      // New collections are born in draft order — Plex keeps creation order
      // for static collections, which is why the draft list order matters.
      const payload = {
        sectionKey: f.sectionKey, type: f.type, title: f.title, smart: f.smart,
        itemRatingKeys: diff.added.map((x) => x.ratingKey),
        filterQuery: f.smart ? f.filterQuery : '',
        titleSort: f.titleSort, summary: f.summary, contentRating: f.contentRating,
        visHome: f.visHome, visShared: f.visShared, visRecommended: f.visRecommended,
      };
      const { collection } = await call(window.pcm.createCollection(payload));
      if (!collection.ratingKey) throw new Error(collection.note || 'Plex returned no collection ID. Refresh before retrying to avoid duplicates.');
      createdKey = collection.ratingKey;
      if (f.posterUrl) {
        try { await call(window.pcm.setPoster(collection.ratingKey, f.posterUrl, f.posterAssetId)); }
        catch (e) { toast('Collection created, but the poster could not be applied: ' + e.message, 'error', e.hint); }
      }
      toast(`Collection “${f.title}” created`, 'success');
    } else {
      const rk = draft.ratingKey;
      createdKey = rk;
      if (diff.metaChanged.length) {
        await call(window.pcm.updateCollection(rk, {
          title: f.title, titleSort: f.titleSort, summary: f.summary, contentRating: f.contentRating,
          visHome: f.visHome, visShared: f.visShared, visRecommended: f.visRecommended,
        }));
      }
      if (diff.memberOrderChanged) {
        await call(window.pcm.setCollectionItems(rk, draft.members.map((x) => x.ratingKey)));
      }
      if (f.posterUrl) {
        try { await call(window.pcm.setPoster(rk, f.posterUrl, f.posterAssetId)); }
        catch (e) { toast('Changes applied, but the poster could not be set: ' + e.message, 'error', e.hint); }
      }
      toast('Changes applied to Plex', 'success');
    }
    // Wizard success (v1.17): land on the created collection's detail
    // view with a plain confirmation — not back at a blank Builder.
    openBuilderNew(); // fresh draft for the next Builder visit
    await loadCollections(); // refresh the dashboard behind the scenes
    switchTab('collections');
    await openCollection(createdKey);
    const note = $('collection-created-note');
    if (note && state.detail && state.detail.collection.ratingKey === createdKey) {
      note.textContent = diff.isNew ? `Collection “${f.title}” was created.` : `Changes applied to “${f.title}”.`;
      note.hidden = false;
    }
  } catch (e) {
    // Failure stays in the step: the error shows at Review and nothing
    // claims success; the draft is left intact for a retry.
    const re = $('wz-review-error');
    if (re) { re.hidden = false; re.textContent = 'Nothing was applied: ' + (e.message || String(e)); }
    toastErr(e);
  }
});

// --- builder open modes ----------------------------------------------------------
function syncTypeToSection() {
  const sel = $('b-section').selectedOptions[0];
  if (sel) $('b-type').value = sel.dataset.type === 'show' ? '2' : '1';
}

function openBuilderNew() {
  window.PCMArt?.reset();
  $('b-section').disabled = false;
  $('b-smart').disabled = false;
  Object.assign(draft, { mode: 'new', ratingKey: null, original: null, members: [], rules: [] });
  draft.importReport = null;
  draft.importSeedMembers = [];
  draft.importRemoved = [];
  const irb = $('import-results'); if (irb) irb.innerHTML = '';
  $('builder-eyebrow').textContent = 'Builder';
  $('builder-title').textContent = 'New collection';
  draft.seededFrom = null;
  for (const [id, v] of [['b-title', ''], ['b-titlesort', ''], ['b-summary', ''], ['b-poster', ''], ['b-content-rating', '']]) $(id).value = v;
  $('b-smart').checked = false;
  $('b-vis-home').checked = true; $('b-vis-shared').checked = false; $('b-vis-recommended').checked = true;
  if (state.sectionKey) $('b-section').value = state.sectionKey;
  syncTypeToSection();
  // Wizard reset (v1.17): back to Step 1 / Blank, all paging, preview
  // and import state cleared. Nothing here writes — abandoning a
  // wizard mid-way is exactly this function plus a tab switch.
  wizard.step = 1;
  wizard.source = 'blank';
  wizard.gridItems = []; wizard.gridTotal = 0; wizard.gridTotalKnown = false; wizard.gridHasMore = false;
  wizard.gridQuery = ''; wizard.gridLoadedFor = '';
  wizard.ruleItems = []; wizard.ruleTotal = 0; wizard.materializedFor = '';
  wizard.importShown = WZ_GRID_SIZE;
  $('wz-grid-search').value = '';
  $('wz-grid-counts').textContent = '';
  $('wz-member-grid').innerHTML = '';
  $('wz-rule-count').textContent = '';
  $('imp-ref').value = '';
  $('wz-details-error').hidden = true;
  $('wz-review-error').hidden = true;
  wizardStepError('');
  renderWizardChrome();
  renderBuilderMode();
  $('rule-preview-grid').innerHTML = '';
  renderRules(); renderDraftMembers(); renderImportGrid();
  $('dryrun-output').innerHTML = '<p class="muted">No review computed yet. The review compares your draft against the live collection and shows exactly what applying would do — <strong>nothing is written to Plex until you confirm.</strong></p>';
  $('btn-apply').disabled = true;
}

function openBuilderEdit(collection, items) {
  if (collection.smart) {
    // Smart rules aren't edited in place (see the header note), so
    // "editing" one means seeding a NEW manual draft from its
    // current members.
    openBuilderNew();
    draft.members = items.map((i) => ({ ...i }));
    $('b-title').value = collection.title + ' (manual)';
    $('b-summary').value = collection.summary || '';
    $('b-content-rating').value = collection.contentRating || '';
    renderDraftMembers();
    wizard.source = 'blank';
    wizard.step = 2; renderWizardChrome(); loadWizardGrid(true);
    toast('Smart collection opened as a new manual draft — smart rules aren\'t edited in place here; the rule itself is shown on the collection\'s detail view.', '', '');
    return;
  }
  openBuilderNew();
  $('b-section').disabled = true;
  $('b-smart').disabled = true;
  Object.assign(draft, {
    mode: 'edit', ratingKey: collection.ratingKey,
    original: {
      members: items.map((i) => ({ ratingKey: i.ratingKey, title: i.title, thumb: i.thumb, year: i.year, type: i.type })),
      fields: {
        title: collection.title, titleSort: collection.titleSort || '', summary: collection.summary || '',
        contentRating: collection.contentRating || '',
        smart: false, visHome: collection.promotedToOwnHome, visShared: collection.promotedToSharedHome, visRecommended: collection.promotedToRecommended,
      },
    },
    members: items.map((i) => ({ ...i })), rules: [],
  });
  draft.seededFrom = null;
  $('builder-eyebrow').textContent = 'Builder · editing';
  $('builder-title').textContent = collection.title;
  renderBuilderMode();
  $('b-title').value = collection.title;
  $('b-titlesort').value = collection.titleSort || '';
  $('b-summary').value = collection.summary || '';
  $('b-content-rating').value = collection.contentRating || '';
  $('b-vis-home').checked = collection.promotedToOwnHome;
  $('b-vis-shared').checked = collection.promotedToSharedHome;
  $('b-vis-recommended').checked = collection.promotedToRecommended;
  renderRules(); renderDraftMembers();
  // Editing keeps its existing flow, presented in the wizard at the
  // members step with the source locked (v1.17).
  wizard.source = 'blank';
  wizard.step = 2; renderWizardChrome(); loadWizardGrid(true);
}

function openBuilderDuplicate(collection, items) {
  openBuilderNew();
  draft.members = items.map((i) => ({ ...i }));
  draft.seededFrom = null;
  $('b-title').value = collection.title + ' (copy)';
  renderBuilderMode();
  $('b-summary').value = collection.summary || '';
  $('b-content-rating').value = collection.contentRating || '';
  renderDraftMembers();
  wizard.source = 'blank';
  wizard.step = 2; renderWizardChrome(); loadWizardGrid(true);
  toast('Duplicated into a new draft — review and apply to create it.', 'success');
}

$('b-smart').addEventListener('change', renderBuilderMode);
$('b-section').addEventListener('change', () => {
  // Retargeting the wizard at another library clears every path's
  // state — members, rules or import matches from one library must
  // never be created into another.
  draft.members = []; draft.rules = [];
  draft.importReport = null;
  draft.importSeedMembers = []; draft.importRemoved = [];
  const irb = $('import-results'); if (irb) irb.innerHTML = '';
  wizard.gridLoadedFor = ''; wizard.gridItems = []; wizard.materializedFor = '';
  syncTypeToSection(); renderRules(); renderDraftMembers(); renderImportGrid();
  if (wizard.step === 2) wizardEnterMembers();
});
$('btn-builder-reset').addEventListener('click', openBuilderNew);
// Any identity/settings change invalidates the computed diff.
['b-title', 'b-titlesort', 'b-summary', 'b-content-rating', 'b-poster', 'b-smart', 'b-vis-home', 'b-vis-shared', 'b-vis-recommended'].forEach((id) =>
  $(id).addEventListener('input', invalidateDryRun));

// ---------- v0.2: Advisor handoff + shared namespace --------------------------------
// openBuilderFromAdvisor seeds a NEW manual draft from an Advisor suggestion
// card and hands it to the normal Builder flow (dry-run → confirm → apply).
// The Advisor NEVER writes to Plex itself — this handoff is the only path.
// `members` are slim scan items {ratingKey,title,thumb,year,type}.
function openBuilderFromAdvisor({ title, members, sectionKey, type }) {
  openBuilderNew();
  if (sectionKey) {
    const opt = [...$('b-section').options].find((o) => o.value === String(sectionKey));
    if (opt) $('b-section').value = String(sectionKey);
  }
  if (type) $('b-type').value = String(type);
  else syncTypeToSection();
  $('b-title').value = String(title || '').slice(0, 120);
  draft.members = (members || []).map((m) => ({
    ratingKey: String(m.ratingKey), title: m.title, thumb: m.thumb || '',
    year: m.year || null, type: m.type || '',
  }));
  renderDraftMembers();
  draft.seededFrom = 'advisor';
  renderBuilderMode();
  // Wizard landing (v1.17): a member-seeded draft opens at Step 2
  // (Library & members) with the seed pre-selected in the grid/tray.
  wizard.source = 'blank';
  wizard.step = 2; renderWizardChrome(); loadWizardGrid(true);
  switchTab('builder');
}

// openBuilderWithRules seeds the Builder's smart-rule editor from an AI
// proposal (or any rule set): rules appear as EDITABLE rows, and the user
// must still Preview them — the deterministic preview is the validation.
//
// With { smart: true } (the Advisor's "create as smart" choice, v1.14)
// the draft is born a smart collection: the Smart toggle is on, the
// title is seeded, and no member list is involved — CreateCollection
// stores the rules with Plex and Plex resolves members dynamically.
// `seededFrom` names the mode text ('ai-rules' default, 'advisor-smart'
// for Advisor cards).
function openBuilderWithRules({ rules, sectionKey, type, note, title, smart, seededFrom }) {
  openBuilderNew();
  if (sectionKey) {
    const opt = [...$('b-section').options].find((o) => o.value === String(sectionKey));
    if (opt) $('b-section').value = String(sectionKey);
  }
  if (type) $('b-type').value = String(type);
  else syncTypeToSection();
  draft.rules = (rules || []).map((r) => ({ field: r.field, op: r.op, value: r.value || '' }));
  if (title) $('b-title').value = String(title).slice(0, 120);
  if (smart) $('b-smart').checked = true;
  renderRules();
  draft.seededFrom = seededFrom || 'ai-rules';
  renderBuilderMode();
  // Wizard landing (v1.17): a rule-seeded draft opens at Step 2 with
  // the rules source active and the live match preview running; the
  // static/smart choice waits at Step 3, pre-set from the seed.
  wizard.source = 'rules';
  wizard.step = 2; renderWizardChrome(); refreshRulePreview();
  if (note) toast(note, '', smart ? 'Smart collection — check the rule preview, then continue to the review.' : 'AI proposal — check the rule preview, then continue to the review.');
  switchTab('builder');
}

// window.PCM — the shared surface for renderer/advisor.js (v0.2).
// FUTURE AI: advisor.js must NOT reach into app.js internals directly —
// everything it needs is exposed here. If advisor.js needs a new capability,
// add it to this namespace (and keep the list alphabetical-ish).
window.PCM = {
  $, esc, call, toast, toastErr, openModal, artSrc, posterHTML, state, switchTab,
  openBuilderFromAdvisor, openBuilderWithRules, describeRule, getDraftPosterSource,
};

// ---------- boot ------------------------------------------------------------------
openBuilderNew();
boot();
