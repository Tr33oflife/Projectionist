/* ============================================================================
 * bridge.js — Plex Collection Manager (Go build)
 * ----------------------------------------------------------------------------
 * FUTURE AI: this file replaces Electron's preload.js. It defines
 * window.pcm with the EXACT same method surface the renderer was written
 * against, implemented over fetch() to the Go server's /api/* routes.
 *
 * Contract (identical to the Electron envelope contract):
 *   - Every method returns a Promise resolving to { ok:true, ... } on
 *     success or { ok:false, error, hint } on failure. The renderer's
 *     call() helper throws {message, hint} from that envelope — never
 *     reject these promises for ordinary API failures.
 *   - Progress events: onScanProgress / onOverlayProgress register
 *     callbacks; the Go server pushes them over one SSE stream
 *     (/api/events). Electron used webContents.send; SSE is the web
 *     equivalent and needs no renderer changes.
 *   - Session token: main.go serves /session.js which sets
 *     window.__PCM_TOKEN__ before this file runs. The token is per-launch
 *     and travels as the X-PCM-Token header (and as ?t= on <img>/SSE URLs,
 *     where headers are impossible). The server is loopback-only; the
 *     token stops OTHER local processes / stray tabs from driving the API.
 *
 * Differences from Electron, deliberately (documented in HANDOFF.md):
 *   - artImport(): Electron opened an OS file dialog. Here the bridge
 *     opens an <input type=file> and uploads the chosen file to
 *     /api/art/importImage. Cancel resolves { ok:true, canceled:true },
 *     exactly the shape the art picker's cancel path already handles.
 *   - artOpenTPDb(): the server returns the TPDb search URL and the
 *     bridge opens it in a new browser tab (window.open). Same UX as
 *     Electron's shell.openExternal for a browser-hosted UI.
 * ========================================================================== */
(() => {
  'use strict';
  const TOKEN = window.__PCM_TOKEN__ || '';

  async function rpc(path, body) {
    let res;
    try {
      res = await fetch('/api/' + path, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-PCM-Token': TOKEN },
        body: JSON.stringify(body === undefined ? {} : body),
      });
    } catch (e) {
      return { ok: false, error: 'Cannot reach the Plex Collection Manager server: ' + e.message, hint: 'Is the app still running?' };
    }
    try {
      return await res.json();
    } catch {
      return { ok: false, error: 'Server returned HTTP ' + res.status + ' with no JSON body', hint: '' };
    }
  }

  // ---- progress events (SSE) ----------------------------------------------
  const scanListeners = new Set();
  const overlayListeners = new Set();
  function connectEvents() {
    let es;
    try {
      es = new EventSource('/api/events?t=' + encodeURIComponent(TOKEN));
    } catch { return; }
    es.onmessage = (ev) => {
      let msg;
      try { msg = JSON.parse(ev.data); } catch { return; }
      const targets = msg.kind === 'scan-progress' ? scanListeners
        : msg.kind === 'overlay-progress' ? overlayListeners : null;
      if (!targets) return;
      for (const cb of [...targets]) { try { cb(msg.payload); } catch { /* listener errors never break the stream */ } }
    };
    es.onerror = () => { /* EventSource auto-reconnects; nothing to do */ };
  }
  connectEvents();

  // ---- art import via file input (see header note) --------------------------
  function artImport() {
    return new Promise((resolve) => {
      const input = document.createElement('input');
      input.type = 'file';
      input.accept = 'image/png,image/jpeg';
      input.style.display = 'none';
      document.body.appendChild(input);
      let settled = false;
      const done = (v) => { if (!settled) { settled = true; input.remove(); resolve(v); } };
      input.addEventListener('change', async () => {
        const file = input.files && input.files[0];
        if (!file) { done({ ok: true, canceled: true }); return; }
        try {
          const form = new FormData();
          form.append('file', file, file.name);
          const res = await fetch('/api/art/importImage', {
            method: 'POST',
            headers: { 'X-PCM-Token': TOKEN },
            body: form,
          });
          done(await res.json());
        } catch (e) {
          done({ ok: false, error: 'Import upload failed: ' + e.message, hint: '' });
        }
      });
      // No reliable 'cancel' event on file inputs in every browser; the
      // picker in collection-art.js also guards with its own sequencing.
      input.click();
    });
  }

  window.pcm = {
    // -- auth / server selection -------------------------------------------
    startLogin: () => rpc('auth/startLogin'),
    pollLogin: (pinId) => rpc('auth/pollLogin', { pinId }),
    restoreSession: () => rpc('auth/restoreSession'),
    logout: () => rpc('auth/logout'),
    selectServer: (machineId) => rpc('auth/selectServer', { machineId }),
    // -- Plex reads ----------------------------------------------------------
    getSections: () => rpc('plex/getSections'),
    getSectionItems: (sectionKey, filterQuery, type) => rpc('plex/getSectionItems', { sectionKey, filterQuery: filterQuery || '', type: type ?? null }),
    // v1.17 wizard grid: ONE read-only page of a section listing —
    // { items, total, totalKnown, start, size, hasMore }. opts:
    // { filterQuery, type, title, start, size }.
    getSectionItemsPage: (sectionKey, opts = {}) => rpc('plex/getSectionItemsPage', { sectionKey, filterQuery: opts.filterQuery || '', type: opts.type ?? null, title: opts.title || '', start: opts.start || 0, size: opts.size || 50 }),
    getSectionTagTypes: (sectionKey) => rpc('plex/getSectionTagTypes', { sectionKey }),
    getSectionTags: (sectionKey, tagType) => rpc('plex/getSectionTags', { sectionKey, tagType }),
    getCollections: (sectionKey) => rpc('plex/getCollections', { sectionKey }),
    getCollectionItems: (ratingKey) => rpc('plex/getCollectionItems', { ratingKey }),
    getItem: (ratingKey) => rpc('plex/getItem', { ratingKey }),
    getItemChildren: (ratingKey) => rpc('plex/getItemChildren', { ratingKey }),
    searchLibrary: (sectionKey, query, type) => rpc('plex/searchLibrary', { sectionKey, query, type: type ?? null }),
    previewFilter: (sectionKey, filterQuery, type) => rpc('plex/previewFilter', { sectionKey, filterQuery, type: type ?? null }),
    // -- Plex writes (ALL guarded server-side by the write policy) -----------
    createCollection: (payload) => rpc('plex/createCollection', payload),
    importPreviewList: (source, listRef, sectionKey, type) => rpc('import/previewList', { source, listRef, sectionKey, type: type ?? null }),
    updateCollection: (ratingKey, fields) => rpc('plex/updateCollection', { ratingKey, fields }),
    setCollectionItems: (ratingKey, ratingKeys) => rpc('plex/setCollectionItems', { ratingKey, ratingKeys }),
    deleteCollection: (ratingKey) => rpc('plex/deleteCollection', { ratingKey }), // server ALWAYS rejects: deletion is disabled
    setPoster: (ratingKey, url, assetId) => rpc('plex/setPoster', { ratingKey, url: url ?? null, assetId: assetId ?? null }),
    // -- collections manager (v1.13) ------------------------------------------
    collectionHealth: (sectionKey) => rpc('plex/collectionHealth', { sectionKey }),
    getCollectionDetail: (ratingKey) => rpc('plex/getCollectionDetail', { ratingKey }),
    updateCollectionMeta: (ratingKey, fields) => rpc('plex/updateCollectionMeta', { ratingKey, fields }),
    collectionBulkArt: (ratingKeys, poster, backdrop) => rpc('plex/collectionBulkArt', { ratingKeys, poster, backdrop }),
    setCollectionPosterURL: (ratingKey, url) => rpc('plex/setCollectionPosterURL', { ratingKey, url }),
    // -- poster generator (v1.20) ------------------------------------------------
    // Render-only: the server composites a poster from member art and
    // registers it as a collection-art asset; applying rides the
    // normal setPoster path (backup gate) or the Builder's art slot.
    posterGenPreview: (payload) => rpc('postergen/preview', payload),
    // One AI-generated background via the Settings AI provider
    // (Cloudflare Workers AI or any OpenAI-shaped images endpoint);
    // the image model is chosen in the generator panel.
    posterGenAIImage: (prompt, model) => rpc('postergen/aiImage', { prompt, model }),
    // -- collection art finder -------------------------------------------------
    artSuggestions: (title) => rpc('art/suggestions', { title }),
    artPreview: (input) => rpc('art/preview', { input }),
    artImport,
    artRemember: (title, assetId) => rpc('art/remember', { title, assetId }),
    artOpenTPDb: async (title) => {
      const r = await rpc('art/openTPDb', { title });
      if (r && r.ok && r.url) window.open(r.url, '_blank', 'noopener');
      return r;
    },
    // -- advisor ---------------------------------------------------------------
    advisorScan: () => rpc('advisor/scan'),
    advisorGetScan: () => rpc('advisor/getScan'),
    advisorSuggestions: () => rpc('advisor/suggestions'),
    advisorDismiss: (cardId) => rpc('advisor/dismiss', { cardId }),
    advisorUndismiss: (cardId) => rpc('advisor/undismiss', { cardId }),
    advisorResetDismissals: () => rpc('advisor/resetDismissals'),
    advisorGetDrafts: () => rpc('advisor/getDrafts'),
    advisorSaveDraft: (draft) => rpc('advisor/saveDraft', draft),
    advisorDeleteDraft: (id) => rpc('advisor/deleteDraft', { id }),
    advisorDubVerdict: (args) => rpc('advisor/dubVerdict', args),
    advisorGetSettings: () => rpc('advisor/getSettings'),
    advisorSetSettings: (obj) => rpc('advisor/setSettings', obj),
    advisorClearCache: () => rpc('advisor/clearCache'),
    advisorCacheStatus: () => rpc('advisor/cacheStatus'),
    onScanProgress: (cb) => { scanListeners.add(cb); return () => scanListeners.delete(cb); },
    // -- metadata APIs / AI ------------------------------------------------------
    metaStatus: () => rpc('meta/status'),
    metaSetKey: (which, value) => rpc('meta/setKey', { which, value }),
    metaSetAiBaseUrl: (url) => rpc('meta/setAiBaseUrl', { url }),
    metaSetAiModel: (model) => rpc('meta/setAiModel', { model }),
    metaSetDonate: (username, enabled) => rpc('meta/setDonate', { username, ...(enabled !== undefined ? { enabled } : {}) }),
    settingsExportBackup: (includeKeys) => rpc('settings/exportBackup', { includeKeys }),
    settingsImportBackup: (backup, confirmKeys) => rpc('settings/importBackup', { backup, confirmKeys }),
    metaTraktOAuthStart: async () => {
      // Electron's main opened the Trakt approval via shell.openExternal;
      // here the server returns the URL and the bridge opens the tab —
      // the Settings UI copy ("the browser tab that just opened") holds.
      const r = await rpc('meta/traktOAuthStart');
      if (r && r.ok && r.authUrl) window.open(r.authUrl, '_blank', 'noopener');
      return r;
    },
    metaTraktOAuthFinish: (code) => rpc('meta/traktOAuthFinish', { code }),
    metaTraktDisconnect: () => rpc('meta/traktDisconnect'),
    aiProposeRules: (text) => rpc('ai/proposeRules', { text }),
    aiProposeFranchiseMerge: (names) => rpc('ai/proposeFranchiseMerge', { names }),
    aiProposeRibbon: (context) => rpc('ai/proposeRibbon', { context }),
    aiSuggestCollections: (sectionKey) => rpc('ai/suggestCollections', { sectionKey }),
    aiTestConnection: () => rpc('ai/testConnection'),
    // -- overlay studio ------------------------------------------------------------
    overlayBadgePacks: () => rpc('overlay/badgePacks'),
    overlayFanartLogos: (tmdbId, mediaType) => rpc('overlay/fanartLogos', { tmdbId, mediaType }),
    overlayItemFacts: (ratingKey) => rpc('overlay/itemFacts', { ratingKey }),
    // overlayApply(jobs, badgeSet, target, runCtx?) — runCtx is the
    // streaming form: { runId, runPhase: 'start'|'append'|'finish',
    // cancelled, outcomes } submits one batch of a shared run (one
    // History record per user action). Without runCtx it is the legacy
    // single-shot apply.
    overlayApply: (jobs, badgeSet, target, runCtx) => rpc('overlay/apply', { jobs, badgeSet, target, ...(runCtx || {}) }),
    overlayClassifyPoster: (ratingKey) => rpc('overlay/classifyPoster', { ratingKey }),
    overlayRestoreDefaultPoster: (args) => rpc('overlay/restoreDefaultPoster', args),
    overlayRestore: (args) => rpc('overlay/restore', args),
    overlayListBackups: () => rpc('overlay/listBackups'),
    overlayRestoreBackups: (items) => rpc('overlay/restoreBackups', { items }),
    overlayHistory: () => rpc('overlay/history'),
    overlayReapplyCheck: () => rpc('overlay/reapplyCheck'),
    onOverlayProgress: (cb) => { overlayListeners.add(cb); return () => overlayListeners.delete(cb); },
    overlayTemplatesList: () => rpc('overlay/templatesList'),
    overlayTemplateSave: (name, placements, families) => rpc('overlay/templateSave', { name, placements, families: families || null }),
    overlayTemplateLoad: (name) => rpc('overlay/templateLoad', { name }),
    overlayTemplateDelete: (name) => rpc('overlay/templateDelete', { name }),
    // -- appearance / themes (v1.19) ----------------------------------------------
    // Theme choice + custom palette persist in the app settings store
    // (plain settings, not secrets). themeSet validates server-side:
    // unknown names and incomplete palettes are rejected whole.
    themeGet: () => rpc('theme/get'),
    themeSet: (obj) => rpc('theme/set', obj),
    // -- leaving soon (v1.12) -------------------------------------------------------
    // The app's own per-server Leaving list: dates set here, in the
    // app — never read from Plex or any external source. Mutations
    // answer with the fresh list.
    leavingList: () => rpc('leaving/list'),
    leavingSet: (items, date) => rpc('leaving/set', { items, date }),
    leavingRemove: (ratingKey) => rpc('leaving/remove', { ratingKey }),
  };

  // ---- app version ----------------------------------------------------
  // Single source of truth: the server's own build version (main.go's
  // version const, reported by GET /api/health). The sidebar brand
  // slot (#app-version) ships version-free and is filled from that
  // report, so the UI can never again display a stale hardcoded
  // number. If the report is unreachable the label simply stays
  // version-free — showing no version beats showing a wrong one.
  const fillAppVersion = async () => {
    const el = document.getElementById('app-version');
    if (!el) return;
    try {
      const res = await fetch('/api/health');
      const env = await res.json();
      if (env && env.version) el.textContent = 'Manager · v' + env.version;
    } catch { /* keep the version-free label */ }
  };
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', fillAppVersion, { once: true });
  } else {
    fillAppVersion();
  }
})();
