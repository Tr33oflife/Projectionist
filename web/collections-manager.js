/* ============================================================================
 * collections-manager.js — the Collections MANAGER (v1.13).
 * ----------------------------------------------------------------------------
 * Deepens the Collections tab without replacing it: the grid in app.js
 * stays the browse surface; this module renders the manager panel into
 * #cm-root (health view + full metadata editor + bulk art) and is fed
 * by app.js's loadCollections() via PCMCollectionsManager.show().
 *
 * Ground rules, mirrored from the server side:
 *  - Health flags come from the server's real reads (member count,
 *    poster/backdrop/summary presence, duplicate titles). This module
 *    only filters/sorts what it was given — it never invents a fact.
 *  - The editor shows what the server ACTUALLY holds: fields the
 *    server doesn't expose (labels on current Plex versions) render
 *    disabled with the reason, and visibility renders read-only when
 *    the server doesn't report promotion state.
 *  - Saving is one verified write: the server re-reads the collection
 *    and answers per-field what it holds now; this module renders
 *    those verdicts verbatim — a mismatch is shown, never smoothed
 *    over.
 *  - NOTHING here deletes a collection or any media. The app's
 *    deletion boundary is total; an empty collection's remedy is
 *    membership/metadata management, and the panel says so.
 * ========================================================================== */
(() => {
  'use strict';
  const { $, esc, call, toast, toastErr, openModal, artSrc, state } = window.PCM;

  const FLAG_FILTERS = ['EMPTY', 'NO CURRENT MATCHES', 'NO POSTER', 'NO BACKDROP', 'NO SUMMARY', 'DUPLICATE TITLE'];
  let view = null;            // { entries, flagCounts, total, sectionKey }
  const selected = new Set(); // ratingKeys ticked for bulk art
  let bulkResultsHTML = '';   // survives health refreshes (a refresh
                              // re-renders the panel; the last bulk
                              // outcome must not be wiped by it)

  function visibilityText(c) {
    if (!c.visibilityKnown) return 'Visibility: not reported by this server';
    if (c.promotedToOwnHome) return 'Visibility: on Home';
    if (c.promotedToSharedHome) return 'Visibility: on shared users’ Home';
    return 'Visibility: library only';
  }

  function filteredEntries() {
    if (!view) return [];
    const f = $('cm-filter') ? $('cm-filter').value : 'all';
    const sort = $('cm-sort') ? $('cm-sort').value : 'title';
    let list = view.entries.slice();
    if (f === 'attention') list = list.filter((e) => e.flags.length > 0);
    else if (f !== 'all') list = list.filter((e) => e.flags.includes(f));
    if (sort === 'title') list.sort((a, b) => a.title.localeCompare(b.title));
    if (sort === 'size-desc') list.sort((a, b) => b.childCount - a.childCount);
    if (sort === 'size-asc') list.sort((a, b) => a.childCount - b.childCount);
    return list;
  }

  function renderRows() {
    const mount = $('cm-rows');
    if (!mount || !view) return;
    const list = filteredEntries();
    if (!list.length) {
      mount.innerHTML = '<p class="muted">No collections match this filter.</p>';
    } else {
      mount.innerHTML = list.map((e) => {
        const src = artSrc(e.thumb, 100);
        const img = src
          ? `<img src="${esc(src)}" alt="" loading="lazy">`
          : `<div class="poster-fallback">${esc((e.title || '?')[0].toUpperCase())}</div>`;
        const flags = e.flags.map((f) => `<span class="cm-flag">${esc(f)}</span>`).join(' ');
        // v1.14: smart collections are marked as such, their member
        // count reads as CURRENT matches (membership is dynamic),
        // and the rule Plex stored is shown in plain words. A smart
        // collection at 0 matches carries NO CURRENT MATCHES (never
        // a bare EMPTY) plus the rule-based explanation.
        const smartChip = e.smart ? '<span class="cm-flag">SMART</span> ' : '';
        const memberText = e.smart
          ? `currently ${e.childCount} match${e.childCount === 1 ? '' : 'es'}`
          : `${e.childCount} member${e.childCount === 1 ? '' : 's'}`;
        const smartText = e.smart
          ? ` · Smart rules: ${esc(e.smartSummary || 'not reported by the server')}${e.flags.includes('NO CURRENT MATCHES') ? ' — rule-based: it fills itself as the library changes' : ''}`
          : '';
        return `<div class="cm-row" data-rk="${esc(e.ratingKey)}">
          <label class="cm-pick"><input type="checkbox" class="cm-check" data-rk="${esc(e.ratingKey)}" ${selected.has(e.ratingKey) ? 'checked' : ''}></label>
          <div class="cm-thumb">${img}</div>
          <div class="cm-main">
            <p class="cm-title">${esc(e.title)} ${smartChip}${flags}</p>
            <p class="muted cm-meta">${memberText} · Poster: ${e.hasPoster ? 'custom' : 'Plex composite'} · Backdrop: ${e.hasBackdrop ? 'set' : 'none'} · Summary: ${e.summary ? 'written' : 'empty'} · ${esc(visibilityText(e))}${smartText}</p>
          </div>
          <button class="btn ghost small" data-cm-edit="${esc(e.ratingKey)}">Edit details</button>
          <button class="btn ghost small" data-cm-gen="${esc(e.ratingKey)}">Generate poster</button>
        </div>`;
      }).join('');
    }
    mount.querySelectorAll('.cm-check').forEach((cb) => cb.addEventListener('change', () => {
      if (cb.checked) selected.add(cb.dataset.rk); else selected.delete(cb.dataset.rk);
      renderBulkBar();
    }));
    mount.querySelectorAll('[data-cm-edit]').forEach((b) => b.addEventListener('click', () => openEditor(b.dataset.cmEdit)));
    mount.querySelectorAll('[data-cm-gen]').forEach((b) => b.addEventListener('click', () => {
      const e = list.find((x) => String(x.ratingKey) === String(b.dataset.cmGen));
      openGenerator(b.dataset.cmGen, e ? e.title : '');
    }));
    renderBulkBar();
  }

  function renderBulkBar() {
    const bar = $('cm-bulkbar');
    if (!bar) return;
    const n = selected.size;
    bar.hidden = n === 0;
    const label = $('cm-bulk-count');
    if (label) label.textContent = `${n} selected`;
  }

  function render() {
    const root = $('cm-root');
    if (!root) return;
    if (!view) { root.innerHTML = ''; return; }
    const fc = view.flagCounts || {};
    const attention = view.entries.filter((e) => e.flags.length > 0).length;
    const chip = (name) => (fc[name] ? ` · ${fc[name]} ${name.toLowerCase()}` : '');
    root.innerHTML = `
    <div class="panel cm-panel">
      <div class="row-between">
        <h3>Collection health</h3>
        <button class="btn ghost small" id="cm-refresh">Refresh</button>
      </div>
      <p class="muted" id="cm-summary-line">${view.total} collections · ${attention} need attention${FLAG_FILTERS.map(chip).join('')}</p>
      <div class="toolbar cm-toolbar">
        <select id="cm-filter" class="input select">
          <option value="all">All collections</option>
          <option value="attention">Needs attention</option>
          ${FLAG_FILTERS.map((f) => `<option value="${esc(f)}">${esc(f)}</option>`).join('')}
        </select>
        <select id="cm-sort" class="input select">
          <option value="title">Sort: Title A–Z</option>
          <option value="size-desc">Sort: Most members</option>
          <option value="size-asc">Sort: Fewest members</option>
        </select>
      </div>
      <div id="cm-rows"></div>
      <div id="cm-bulkbar" class="cm-bulkbar" hidden>
        <span id="cm-bulk-count"></span>
        <label><input type="checkbox" id="cm-bulk-poster" checked> Poster</label>
        <label><input type="checkbox" id="cm-bulk-backdrop"> Backdrop</label>
        <button class="btn primary small" id="cm-bulk-apply">Apply best-match art</button>
      </div>
      <p class="muted small" id="cm-art-note">Art comes from ThePosterDB. If art requests suddenly fail as blocked, their anti-bot protection has tripped on your connection — it usually clears on its own; wait a while and try again.</p>
      <div id="cm-bulk-results">${bulkResultsHTML}</div>
      <p class="fineprint">Collections are never deleted in this app — an empty collection is fixed by adding members or editing its details, never by removing it.</p>
    </div>`;
    $('cm-refresh').addEventListener('click', () => show(view.sectionKey));
    $('cm-filter').addEventListener('change', renderRows);
    $('cm-sort').addEventListener('change', renderRows);
    $('cm-bulk-apply').addEventListener('click', runBulkArt);
    renderRows();
  }

  async function show(sectionKey) {
    const root = $('cm-root');
    if (!root) return;
    if (!sectionKey || !state.machine) { view = null; root.innerHTML = ''; return; }
    try {
      const r = await call(window.pcm.collectionHealth(sectionKey));
      view = { entries: r.collections || [], flagCounts: r.flagCounts || {}, total: r.total || 0, sectionKey };
      // Drop selections that no longer exist in this library.
      const alive = new Set(view.entries.map((e) => e.ratingKey));
      for (const rk of [...selected]) if (!alive.has(rk)) selected.delete(rk);
      render();
    } catch (e) {
      root.innerHTML = `<div class="panel cm-panel"><h3>Collection health</h3><p class="muted">Could not load collection health: ${esc(e.message || e)}</p></div>`;
    }
  }

  // ---- bulk art ---------------------------------------------------------------

  async function runBulkArt() {
    const poster = $('cm-bulk-poster').checked;
    const backdrop = $('cm-bulk-backdrop').checked;
    if (!poster && !backdrop) { toast('Choose poster, backdrop, or both.', 'error'); return; }
    bulkResultsHTML = '<p class="muted">Applying art… each collection is verified by read-back.</p>';
    if ($('cm-bulk-results')) $('cm-bulk-results').innerHTML = bulkResultsHTML;
    try {
      const r = await call(window.pcm.collectionBulkArt([...selected], poster, backdrop));
      const line = (t) => t == null ? '' : t.ok
        ? '<span class="cm-ok">✓ applied &amp; verified</span>'
        : `<span class="cm-bad">✗ ${esc(t.error || 'failed')}</span>`;
      bulkResultsHTML = '<h4>Bulk art results</h4>' + (r.results || []).map((res) =>
        `<p class="cm-result"><strong>${esc(res.title || res.ratingKey)}</strong>${res.poster ? ` · Poster: ${line(res.poster)}` : ''}${res.backdrop ? ` · Backdrop: ${line(res.backdrop)}` : ''}</p>`
      ).join('');
      if ($('cm-bulk-results')) $('cm-bulk-results').innerHTML = bulkResultsHTML;
      show(view.sectionKey); // art presence changed — refresh the health facts
    } catch (e) { toastErr(e); bulkResultsHTML = ''; if ($('cm-bulk-results')) $('cm-bulk-results').innerHTML = ''; }
  }

  // ---- full metadata editor -----------------------------------------------------

  // v1.20: the poster generator (poster-generator.js). Applying from
  // it refreshes the health view so the row shows the new poster.
  function openGenerator(ratingKey, title) {
    if (!window.PCMGenerator || typeof window.PCMGenerator.open !== 'function') {
      toast('The poster generator is unavailable.', 'error');
      return;
    }
    window.PCMGenerator.open({
      mode: 'collection',
      ratingKey,
      title: title || '',
      onApplied: () => show(view && view.sectionKey),
    });
  }

  async function openEditor(ratingKey) {
    let detail;
    try {
      detail = (await call(window.pcm.getCollectionDetail(ratingKey))).detail;
    } catch (e) { toastErr(e); return; }
    const labelsKnown = detail.labels !== null && detail.labels !== undefined;
    const visKnown = !!detail.visibilityKnown;
    const visChoice = detail.visibility === 'home' ? 'home' : detail.visibility === 'shared' ? 'shared' : 'library';
    const bodyHTML = `
      <div class="form-grid">
        <label>Title <input id="cm-f-title" class="input" type="text" value="${esc(detail.title)}"></label>
        <label>Sort title <input id="cm-f-sort" class="input" type="text" value="${esc(detail.titleSort || '')}"></label>
        <label>Content rating <input id="cm-f-rating" class="input" type="text" value="${esc(detail.contentRating || '')}" placeholder="e.g. PG-13"></label>
        <label>Labels ${labelsKnown
          ? `<input id="cm-f-labels" class="input" type="text" value="${esc((detail.labels || []).join(', '))}" placeholder="Comma-separated">`
          : `<input class="input" type="text" value="" disabled placeholder="Not exposed by this server">
             <span class="fineprint">This Plex server does not expose collection labels, so they can't be read or edited here.</span>`}
        </label>
      </div>
      <label class="cm-summary-label">Summary <textarea id="cm-f-summary" class="input" rows="4">${esc(detail.summary || '')}</textarea></label>
      <div class="cm-vis">
        <h4>Visibility</h4>
        ${visKnown ? `
        <label><input type="radio" name="cm-vis" value="library" ${visChoice === 'library' ? 'checked' : ''}> Library only</label>
        <label><input type="radio" name="cm-vis" value="home" ${visChoice === 'home' ? 'checked' : ''}> Visible on Home</label>
        <label><input type="radio" name="cm-vis" value="shared" ${visChoice === 'shared' ? 'checked' : ''}> Visible on shared users’ Home</label>`
        : `<p class="muted" id="cm-vis-note">Current state: not reported by this server. ${esc(detail.visibilityNote || '')}</p>`}
      </div>
      <div class="cm-generate">
        <h4>Generate a poster</h4>
        <p class="muted small">Build one from this collection's own posters — a mosaic grid, the all-members Flower or Spiral, a Hero, or a Title card — preview it at full size, and apply it through the same backup gate as any other poster.</p>
        <button class="btn secondary small" id="cm-generate-poster" type="button">Generate poster…</button>
      </div>
      <div class="cm-poster-url">
        <h4>Poster from a URL</h4>
        <div class="cm-url-row">
          <input id="cm-f-posterurl" class="input" type="url" placeholder="https://… image address">
          <button class="btn ghost small" id="cm-set-poster-url">Set poster</button>
        </div>
        <p class="muted small">Art comes from ThePosterDB. If art requests suddenly fail as blocked, their anti-bot protection has tripped on your connection — it usually clears on its own; wait a while and try again.</p>
        <p class="muted" id="cm-poster-url-result"></p>
      </div>
      <div id="cm-save-results"></div>`;
    const m = openModal({
      title: `Edit collection — ${detail.title}`,
      bodyHTML,
      footHTML: `<button class="btn ghost" data-close="cancel">Close</button>
                 <button class="btn primary" id="cm-save">Save changes</button>`,
    });
    const root = m.root;
    root.querySelector('#cm-generate-poster').addEventListener('click', () => {
      // One modal at a time: the editor closes, the generator opens.
      m.close('generate');
      openGenerator(ratingKey, detail.title);
    });
    root.querySelector('#cm-set-poster-url').addEventListener('click', async () => {
      const url = root.querySelector('#cm-f-posterurl').value.trim();
      const slot = root.querySelector('#cm-poster-url-result');
      if (!url) { slot.textContent = 'Paste an image address first.'; return; }
      slot.textContent = 'Setting poster…';
      try {
        const r = await call(window.pcm.setCollectionPosterURL(ratingKey, url));
        slot.textContent = r.changed
          ? 'Poster set — the server now serves the new poster.'
          : 'The server accepted the URL but its served poster did not change.';
        if (r.changed) show(view && view.sectionKey);
      } catch (e) { slot.textContent = (e && e.message) || 'Could not set the poster.'; }
    });
    root.querySelector('#cm-save').addEventListener('click', async () => {
      const fields = {};
      const put = (key, val, orig) => { if (val !== orig) fields[key] = val; };
      put('title', root.querySelector('#cm-f-title').value, detail.title);
      put('titleSort', root.querySelector('#cm-f-sort').value, detail.titleSort || '');
      put('summary', root.querySelector('#cm-f-summary').value, detail.summary || '');
      put('contentRating', root.querySelector('#cm-f-rating').value, detail.contentRating || '');
      if (labelsKnown) {
        const labels = root.querySelector('#cm-f-labels').value.split(',').map((s) => s.trim()).filter(Boolean);
        if (JSON.stringify(labels) !== JSON.stringify(detail.labels || [])) fields.labels = labels;
      }
      if (visKnown) {
        const choice = (root.querySelector('input[name="cm-vis"]:checked') || {}).value || 'library';
        if (choice !== visChoice) {
          fields.visHome = choice === 'home';
          fields.visShared = choice === 'shared';
          fields.visRecommended = false;
        }
      }
      const slot = root.querySelector('#cm-save-results');
      if (!Object.keys(fields).length) { slot.innerHTML = '<p class="muted">Nothing changed.</p>'; return; }
      slot.innerHTML = '<p class="muted">Saving, then re-reading what the server holds…</p>';
      try {
        const r = await call(window.pcm.updateCollectionMeta(ratingKey, fields));
        const names = { title: 'Title', titleSort: 'Sort title', summary: 'Summary', contentRating: 'Content rating', labels: 'Labels', visibility: 'Visibility' };
        const heldText = (c) => c.held === null || c.held === undefined ? '(not exposed)'
          : Array.isArray(c.held) ? (c.held.length ? c.held.join(', ') : '(none)') : String(c.held);
        slot.innerHTML = '<h4>Saved — verified against the server</h4>' + (r.checks || []).map((c) =>
          `<p class="cm-result">${c.ok ? '<span class="cm-ok">✓</span>' : '<span class="cm-bad">✗</span>'} <strong>${esc(names[c.field] || c.field)}</strong> — server holds: ${esc(heldText(c))}${c.ok ? '' : ` (sent: ${esc(heldText({ held: c.sent }))})`}${c.note ? ` — ${esc(c.note)}` : ''}</p>`
        ).join('');
        detail = r.detail || detail;
        show(view && view.sectionKey);
      } catch (e) { toastErr(e); slot.innerHTML = ''; }
    });
  }

  window.PCMCollectionsManager = { show };
})();
