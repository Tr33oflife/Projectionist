/* ============================================================================
 * poster-generator.js — the collection poster GENERATOR (v1.20).
 * ----------------------------------------------------------------------------
 * Builds a poster for a collection that has no good one, from three
 * templates rendered server-side at full poster size (1000×1500):
 *
 *   Mosaic    — member posters in a 2×2 / 3×3 grid, or EVERY selected
 *               member as a Flower (center + petal rings) or a Spiral
 *               (expanding, shrinking outward). Optional title plate.
 *   Hero      — one treated background image (member art, an upload,
 *               an image URL, or an AI-generated image) + a big title.
 *   Title card— typography first: a background derived from member
 *               colors, or the same treated-image options as Hero.
 *
 * Preview writes NOTHING: the server renders and registers the PNG
 * as a session art asset. Applying rides the ordinary paths — the
 * manager mode calls setPoster (the verified backup gate), the
 * Builder mode drops the render into the draft's art slot through
 * PCMArt.useArt, indistinguishable from a picked poster.
 *
 * Honesty rules, mirrored from the server: the preview's stats and
 * notes render verbatim (members used / skipped / any layout cap),
 * and a shortfall (too few posters for a grid) shows as an error,
 * never as a poster with gaps.
 * ========================================================================== */
(() => {
  'use strict';
  const P = () => window.PCM;

  const GRID_HINTS = {
    '2x2': 'Fills 4 cells with the first selected members, in collection order. Tick different members below to swap who appears.',
    '3x3': 'Fills 9 cells with the first selected members, in collection order. Tick different members below to swap who appears.',
    flower: 'Uses every selected member — one in the center, the rest as radiating rings of petals. Very large sets stop at 169 posters; if that ever happens, the preview says so.',
    spiral: 'Uses every selected member along an expanding spiral, shrinking as it winds outward. Very large sets stop at 169 posters; if that ever happens, the preview says so.',
  };
  const BG_MISSING = {
    upload: 'Choose an image first — it is used for this poster only, never stored in your library.',
    ai: 'Generate an image first — write a prompt and press Generate image.',
    url: 'Paste an image address first.',
  };

  function open(opts) {
    const { $, esc, call, toast, toastErr, openModal, artSrc } = P();
    const mode = opts.mode === 'draft' ? 'draft' : 'collection';
    const st = {
      template: 'mosaic', grid: 'flower', plate: true, zoom: 100,
      title: opts.title || '', titleSize: 118, titlePosition: 'center',
      bgKind: 'member', bgTouched: false, bgMember: '', useArt: false,
      bgAsset: null, bgUrl: '', aiAsset: null, aiChecked: false,
      blur: 0, darken: 15, gradient: 50, focal: [0.5, 0.5],
      members: [], selected: new Set(), lastArt: null, seq: 0,
      loadState: 'ready', loadError: null,
      // Where the members came from, in words ("75 matches from your
      // rules") — supplied by the caller (the Builder bridge) and
      // shown at the members header; for an empty rules draft it IS
      // the members-pane message, so an empty list is never dressed
      // up as a memberless collection (v1.21 rider).
      sourceNote: opts.sourceNote || '',
    };
    let closed = false;
    let debounce = null;
    // Member-load retry budget (see fetchMembers) — declared before
    // load() first runs: a cancelled load is retried, never believed.
    const LOAD_ATTEMPTS = 4;
    const LOAD_BACKOFF_MS = [250, 600, 1200];

    const modal = openModal({
      title: 'Generate a poster',
      bodyHTML: '<p class="muted">Loading members…</p>',
      footHTML: `<button class="btn ghost" data-close="cancel">Cancel</button>
                 <button class="btn primary" id="gen-apply" disabled>${mode === 'draft' ? 'Use in draft' : 'Apply to collection'}</button>`,
    });
    const root = modal.root;
    const q = (sel) => root.querySelector(sel);
    const qa = (sel) => [...root.querySelectorAll(sel)];
    const applyBtn = q('#gen-apply');
    modal.closed.then(() => {
      closed = true;
      if (debounce) { clearTimeout(debounce); debounce = null; }
    });
    applyBtn.addEventListener('click', onApply);

    load();

    async function load() {
      let members = opts.members || null;
      if (!members && mode === 'collection' && opts.ratingKey) {
        // Build the shell first: the fetch below retries on its own,
        // and its progress and failure render into the members pane —
        // a failed load is never presented as an empty collection.
        st.loadState = 'loading';
        buildShell();
        members = await fetchMembers();
        if (closed) return;
        if (members === null) { buildMembers(); return; } // error pane + Retry
        st.loadState = 'ready';
        applyMembers(members);
        buildMembers();
        scheduleRender(true);
        return;
      }
      if (closed) return;
      applyMembers(members || []);
      buildShell();
      scheduleRender(true);
    }

    function applyMembers(members) {
      st.members = members;
      st.selected = new Set(members.map((mm) => String(mm.ratingKey)));
      if (members.length) st.bgMember = String(members[0].ratingKey);
    }

    // A { cancelled:true } answer is NOT an empty collection: a newer
    // load elsewhere in the app superseded this one (the server's
    // load-cancellation rule), and it arrives carrying an empty items
    // list that reads exactly like a memberless collection — on a
    // slow remote server that race is the common case, not an edge.
    // Retry a bounded few times with a short backoff, on
    // cancellations and outright failures alike; only a completed,
    // non-cancelled answer may be read as the member list. Returns
    // null when every attempt failed, with st.loadError saying why.
    async function fetchMembers() {
      let lastErr = null;
      for (let attempt = 0; attempt < LOAD_ATTEMPTS; attempt++) {
        if (closed) return null;
        try {
          const r = await call(window.pcm.getCollectionItems(opts.ratingKey));
          if (r && r.cancelled) {
            lastErr = new Error('the load was interrupted by a newer load elsewhere in the app');
          } else {
            return (r && r.items) || [];
          }
        } catch (e) {
          lastErr = e;
        }
        if (attempt < LOAD_ATTEMPTS - 1) {
          await new Promise((res) => setTimeout(res, LOAD_BACKOFF_MS[attempt]));
        }
      }
      st.loadError = lastErr;
      st.loadState = 'error';
      return null;
    }

    async function retryLoad() {
      st.loadState = 'loading';
      st.loadError = null;
      buildMembers();
      const members = await fetchMembers();
      if (closed) return;
      if (members === null) { buildMembers(); return; }
      st.loadState = 'ready';
      applyMembers(members);
      buildMembers();
      scheduleRender(true);
    }

    function buildShell() {
      const body = root.querySelector('.modal-body');
      body.innerHTML = `
      <div class="gen-layout">
        <div class="gen-controls">
          <div class="gen-tabs">
            <button type="button" class="gen-tab" data-gen-template="mosaic">Mosaic<span>members, arranged</span></button>
            <button type="button" class="gen-tab" data-gen-template="hero">Hero<span>one image, treated</span></button>
            <button type="button" class="gen-tab" data-gen-template="titlecard">Title card<span>type first</span></button>
          </div>

          <div class="gen-pane" id="gen-pane-mosaic">
            <div class="gen-chips">
              <button type="button" data-gen-grid="2x2">2 × 2</button>
              <button type="button" data-gen-grid="3x3">3 × 3</button>
              <button type="button" data-gen-grid="flower">All — Flower</button>
              <button type="button" data-gen-grid="spiral">All — Spiral</button>
            </div>
            <p class="muted small" id="gen-grid-hint"></p>
            <label class="check"><input type="checkbox" id="gen-plate" checked> <span>Title plate along the bottom</span></label>
            <div class="gen-slider-row"><label for="gen-zoom">Poster zoom</label><input type="range" id="gen-zoom" min="50" max="200" value="100"><span id="gen-zoom-v">100%</span></div>
            <div class="gen-members-head">
              <strong>Members</strong><span class="muted small" id="gen-mem-count"></span>
              <span class="gen-members-actions">
                <button type="button" class="btn ghost small" id="gen-mem-all">All</button>
                <button type="button" class="btn ghost small" id="gen-mem-none">None</button>
              </span>
            </div>
            <p class="muted small" id="gen-source-note" hidden></p>
            <div class="gen-members" id="gen-members"></div>
          </div>

          <div class="gen-pane" id="gen-pane-art" hidden>
            <div class="gen-bg-srcs">
              <label><input type="radio" name="gen-bg" value="member"> A member's art</label>
              <label><input type="radio" name="gen-bg" value="upload"> Upload an image</label>
              <label><input type="radio" name="gen-bg" value="url"> Image URL</label>
              <label><input type="radio" name="gen-bg" value="ai"> Generate with AI</label>
              <label id="gen-bg-derived-label"><input type="radio" name="gen-bg" value="derived"> Colors from the members</label>
            </div>
            <div class="gen-bg-pane" id="gen-bg-member">
              <select id="gen-bg-member-sel" class="input"></select>
              <label class="check"><input type="checkbox" id="gen-bg-useart"> <span>Use its backdrop instead of the poster (falls back to the poster when a title has no backdrop)</span></label>
            </div>
            <div class="gen-bg-pane" id="gen-bg-upload" hidden>
              <button type="button" class="btn secondary small" id="gen-upload-btn">Choose an image…</button>
              <span class="muted small" id="gen-upload-name">Nothing uploaded yet — used for this poster only, never stored in your library.</span>
            </div>
            <div class="gen-bg-pane" id="gen-bg-url" hidden>
              <input id="gen-url" class="input" type="url" placeholder="https://… image address">
            </div>
            <div class="gen-bg-pane" id="gen-bg-ai" hidden>
              <div id="gen-ai-guidance" hidden>
                <p class="muted">No AI provider is configured yet. Add an AI base URL and API key in <strong>Settings → API keys &amp; AI</strong>, then reopen this panel — generation uses that provider. Cloudflare Workers AI works with the account base URL (<code>…/accounts/&lt;id&gt;/ai/v1</code>).</p>
              </div>
              <div id="gen-ai-form">
                <textarea id="gen-ai-prompt" class="input" rows="3" placeholder="Describe the background — e.g. a foggy pumpkin patch at dusk, painterly, no text"></textarea>
                <label class="field">Image model <input id="gen-ai-model" class="input" type="text" value="@cf/black-forest-labs/flux-1-schnell" spellcheck="false"></label>
                <div class="gen-ai-row">
                  <button type="button" class="btn secondary small" id="gen-ai-go">Generate image</button>
                  <span class="muted small" id="gen-ai-status"></span>
                </div>
                <div id="gen-ai-thumb"></div>
                <p class="muted small">AI images come back square or landscape from most models — the fill + focus controls below crop them to poster shape, exactly like an upload.</p>
              </div>
            </div>
            <div class="gen-sliders">
              <div class="gen-slider-row"><label for="gen-blur">Blur</label><input type="range" id="gen-blur" min="0" max="100" value="0"><span id="gen-blur-v">0</span></div>
              <div class="gen-slider-row"><label for="gen-darken">Darken</label><input type="range" id="gen-darken" min="0" max="100" value="15"><span id="gen-darken-v">15</span></div>
              <div class="gen-slider-row" id="gen-gradient-row"><label for="gen-gradient">Gradient veil</label><input type="range" id="gen-gradient" min="0" max="100" value="50"><span id="gen-gradient-v">50</span></div>
            </div>
            <div class="gen-focal-row">
              <span class="muted small">Focus — where the crop centers</span>
              <div class="gen-focal" id="gen-focal">${[0, 0.5, 1].map((fy) => [0, 0.5, 1].map((fx) => `<button type="button" data-fx="${fx}" data-fy="${fy}" aria-label="focus ${fx},${fy}"></button>`).join('')).join('')}</div>
            </div>
          </div>

          <div class="gen-titlebox">
            <label class="field">Title <input id="gen-title" class="input" type="text" value="${esc(st.title)}" placeholder="Collection title"></label>
            <div class="gen-slider-row" id="gen-titlesize-row">
              <label for="gen-titlesize">Title size</label><input type="range" id="gen-titlesize" min="48" max="180" value="118"><span id="gen-titlesize-v">118</span>
            </div>
            <div class="gen-seg" id="gen-titlepos-row">
              <span class="muted small">Position</span>
              <button type="button" data-gen-pos="top">Top</button>
              <button type="button" data-gen-pos="center">Center</button>
              <button type="button" data-gen-pos="bottom">Bottom</button>
            </div>
          </div>
        </div>

        <div class="gen-preview-pane">
          <div class="gen-frame"><img id="gen-img" alt="Generated poster preview" hidden><p class="muted" id="gen-empty">Your preview appears here.</p></div>
          <p class="muted small" id="gen-stats"></p>
          <div id="gen-notes"></div>
          <p class="gen-error" id="gen-error" role="alert"></p>
          <button type="button" class="btn secondary small" id="gen-update">Update preview</button>
          <p class="muted small">Rendered at full poster size (1000 × 1500). Nothing is written to Plex until you ${mode === 'draft' ? 'use the poster here and create the collection' : 'apply'}.</p>
        </div>
      </div>`;
      wire();
      buildMembers();
      syncPanes();
    }

    function buildMembers() {
      const mount = q('#gen-members');
      if (!mount) return;
      if (st.loadState === 'loading') {
        mount.innerHTML = '<p class="muted small">Loading members…</p>';
      } else if (st.loadState === 'error') {
        // An interrupted/failed load is not an empty collection: say
        // what happened and offer the retry. The genuine-empty
        // message below is reserved for a completed load of zero.
        mount.innerHTML = `<p class="gen-error">Couldn't load this collection's members${st.loadError && st.loadError.message ? ` — ${esc(st.loadError.message)}` : ''}. Nothing was changed.</p>
          <button type="button" class="btn secondary small" id="gen-load-retry">Retry</button>`;
        const retryBtn = q('#gen-load-retry');
        if (retryBtn) retryBtn.addEventListener('click', retryLoad);
      } else if (!st.members.length) {
        mount.innerHTML = st.sourceNote
          ? `<p class="muted small">${esc(st.sourceNote)}</p>`
          : '<p class="muted small">This collection has no members to arrange. Hero and Title card still work — with an uploaded, linked, or AI-generated background.</p>';
      } else {
        mount.innerHTML = st.members.map((mm) => {
          const src = artSrc(mm.thumb, 60);
          return `<label class="gen-member-row">
            <input type="checkbox" data-gen-member="${esc(String(mm.ratingKey))}" checked>
            ${src ? `<img src="${esc(src)}" alt="" loading="lazy">` : '<span class="gen-member-fallback"></span>'}
            <span>${esc(mm.title || 'Untitled')}</span>
          </label>`;
        }).join('');
        mount.querySelectorAll('[data-gen-member]').forEach((cb) => cb.addEventListener('change', () => {
          if (cb.checked) st.selected.add(cb.dataset.genMember);
          else st.selected.delete(cb.dataset.genMember);
          updateMemberCount();
          scheduleRender();
        }));
      }
      const srcNote = q('#gen-source-note');
      if (srcNote) {
        // The source line belongs with a real member list; an empty
        // list already shows the note as its pane message.
        srcNote.hidden = !(st.sourceNote && st.members.length);
        srcNote.textContent = st.sourceNote || '';
      }
      const sel = q('#gen-bg-member-sel');
      if (sel) {
        sel.innerHTML = st.members.length
          ? st.members.map((mm) => `<option value="${esc(String(mm.ratingKey))}">${esc(mm.title || 'Untitled')}</option>`).join('')
          : `<option value="">${st.loadState === 'error' ? "Couldn't load members" : st.loadState === 'loading' ? 'Loading…' : 'No members'}</option>`;
        sel.value = st.bgMember;
      }
      updateMemberCount();
    }

    function updateMemberCount() {
      const el = q('#gen-mem-count');
      if (el) el.textContent = st.members.length ? ` — ${st.selected.size} of ${st.members.length} selected` : '';
    }

    function syncPanes() {
      qa('[data-gen-template]').forEach((b) => b.classList.toggle('active', b.dataset.genTemplate === st.template));
      q('#gen-pane-mosaic').hidden = st.template !== 'mosaic';
      q('#gen-pane-art').hidden = st.template === 'mosaic';
      q('#gen-titlesize-row').hidden = st.template === 'mosaic';
      q('#gen-titlepos-row').hidden = st.template === 'mosaic';
      q('#gen-gradient-row').hidden = st.template !== 'titlecard';
      qa('[data-gen-grid]').forEach((b) => b.classList.toggle('active', b.dataset.genGrid === st.grid));
      q('#gen-grid-hint').textContent = GRID_HINTS[st.grid] || '';
      q('#gen-plate').checked = st.plate;
      q('#gen-bg-derived-label').hidden = st.template !== 'titlecard';
      qa('input[name="gen-bg"]').forEach((r) => { r.checked = r.value === st.bgKind; });
      q('#gen-bg-member').hidden = st.bgKind !== 'member';
      q('#gen-bg-upload').hidden = st.bgKind !== 'upload';
      q('#gen-bg-url').hidden = st.bgKind !== 'url';
      q('#gen-bg-ai').hidden = st.bgKind !== 'ai';
      qa('#gen-focal button').forEach((b) => b.classList.toggle('active',
        Number(b.dataset.fx) === st.focal[0] && Number(b.dataset.fy) === st.focal[1]));
      qa('[data-gen-pos]').forEach((b) => b.classList.toggle('active', b.dataset.genPos === st.titlePosition));
    }

    function wire() {
      qa('[data-gen-template]').forEach((b) => b.addEventListener('click', () => {
        st.template = b.dataset.genTemplate;
        if (!st.bgTouched) st.bgKind = st.template === 'titlecard' ? 'derived' : 'member';
        if (st.template === 'hero' && st.bgKind === 'derived') st.bgKind = 'member';
        syncPanes();
        scheduleRender(true);
      }));
      qa('[data-gen-grid]').forEach((b) => b.addEventListener('click', () => {
        st.grid = b.dataset.genGrid;
        syncPanes();
        scheduleRender(true);
      }));
      q('#gen-plate').addEventListener('change', (e) => { st.plate = e.target.checked; scheduleRender(); });
      q('#gen-zoom').addEventListener('input', (e) => {
        st.zoom = Number(e.target.value);
        q('#gen-zoom-v').textContent = `${e.target.value}%`;
        scheduleRender();
      });
      q('#gen-mem-all').addEventListener('click', () => {
        st.selected = new Set(st.members.map((mm) => String(mm.ratingKey)));
        qa('[data-gen-member]').forEach((cb) => { cb.checked = true; });
        updateMemberCount(); scheduleRender();
      });
      q('#gen-mem-none').addEventListener('click', () => {
        st.selected = new Set();
        qa('[data-gen-member]').forEach((cb) => { cb.checked = false; });
        updateMemberCount(); scheduleRender();
      });
      qa('input[name="gen-bg"]').forEach((r) => r.addEventListener('change', () => {
        st.bgKind = r.value; st.bgTouched = true;
        syncPanes();
        if (st.bgKind === 'ai') checkAiConfig();
        scheduleRender();
      }));
      q('#gen-bg-member-sel').addEventListener('change', (e) => { st.bgMember = e.target.value; scheduleRender(); });
      q('#gen-bg-useart').addEventListener('change', (e) => { st.useArt = e.target.checked; scheduleRender(); });
      q('#gen-upload-btn').addEventListener('click', async () => {
        const res = await window.pcm.artImport();
        if (closed || !res || res.canceled) return;
        if (!res.ok || !res.art) { showError({ message: (res && res.error) || 'That file could not be imported as an image.' }); return; }
        st.bgAsset = res.art;
        q('#gen-upload-name').textContent = `${res.art.label || 'Uploaded image'} · ${res.art.width} × ${res.art.height} — used for this poster only.`;
        scheduleRender(true);
      });
      q('#gen-url').addEventListener('input', (e) => { st.bgUrl = e.target.value.trim(); scheduleRender(); });
      q('#gen-ai-go').addEventListener('click', onAiGenerate);
      const slider = (id, key, label) => {
        q(id).addEventListener('input', (e) => {
          st[key] = Number(e.target.value);
          q(label).textContent = e.target.value;
          scheduleRender();
        });
      };
      slider('#gen-blur', 'blur', '#gen-blur-v');
      slider('#gen-darken', 'darken', '#gen-darken-v');
      slider('#gen-gradient', 'gradient', '#gen-gradient-v');
      slider('#gen-titlesize', 'titleSize', '#gen-titlesize-v');
      qa('#gen-focal button').forEach((b) => b.addEventListener('click', () => {
        st.focal = [Number(b.dataset.fx), Number(b.dataset.fy)];
        syncPanes();
        scheduleRender();
      }));
      qa('[data-gen-pos]').forEach((b) => b.addEventListener('click', () => {
        st.titlePosition = b.dataset.genPos;
        syncPanes();
        scheduleRender();
      }));
      q('#gen-title').addEventListener('input', (e) => { st.title = e.target.value; scheduleRender(); });
      q('#gen-update').addEventListener('click', () => scheduleRender(true));
    }

    async function checkAiConfig() {
      if (st.aiChecked) return;
      st.aiChecked = true;
      try {
        const meta = await call(window.pcm.metaStatus());
        if (closed) return;
        if (!meta || !meta.ai) {
          q('#gen-ai-guidance').hidden = false;
          q('#gen-ai-form').hidden = true;
        }
      } catch { /* the form stays; the server answers plainly if unconfigured */ }
    }

    async function onAiGenerate() {
      const { call, toastErr } = P();
      const prompt = q('#gen-ai-prompt').value.trim();
      const status = q('#gen-ai-status');
      const btn = q('#gen-ai-go');
      if (!prompt) { status.textContent = 'Write a prompt first.'; return; }
      btn.disabled = true;
      status.textContent = 'Generating… image models can take a while.';
      try {
        const r = await call(window.pcm.posterGenAIImage(prompt, q('#gen-ai-model').value.trim()));
        if (closed) return;
        st.aiAsset = r.art;
        q('#gen-ai-thumb').innerHTML = `<img src="${esc(r.art.preview)}" alt="AI-generated background">`;
        status.textContent = 'Generated — the blur, darken and focus controls treat it like any other background.';
        scheduleRender(true);
      } catch (e) {
        if (closed) return;
        status.textContent = (e && e.message) || 'Generation failed.';
        toastErr(e);
      } finally { btn.disabled = false; }
    }

    function bgPayload() {
      switch (st.bgKind) {
        case 'member': return { kind: 'member', ratingKey: st.bgMember, useArt: st.useArt };
        case 'upload': return st.bgAsset ? { kind: 'asset', assetId: st.bgAsset.assetId } : null;
        case 'ai': return st.aiAsset ? { kind: 'asset', assetId: st.aiAsset.assetId } : null;
        case 'url': return st.bgUrl ? { kind: 'url', url: st.bgUrl } : null;
        case 'derived': return { kind: 'derived' };
        default: return null;
      }
    }

    function buildPayload() {
      const memberKeys = st.members
        .filter((mm) => st.selected.has(String(mm.ratingKey)))
        .map((mm) => String(mm.ratingKey));
      const payload = { template: st.template, title: st.title, memberKeys };
      if (mode === 'collection') payload.ratingKey = opts.ratingKey;
      if (st.template === 'mosaic') {
        payload.grid = st.grid;
        payload.plate = st.plate;
        payload.scale = st.zoom / 100;
      } else {
        payload.titleSize = st.titleSize;
        payload.titlePosition = st.titlePosition;
        payload.blur = st.blur;
        payload.darken = st.darken;
        payload.gradient = st.gradient;
        payload.focalX = st.focal[0];
        payload.focalY = st.focal[1];
        payload.background = bgPayload();
      }
      return payload;
    }

    function scheduleRender(immediate) {
      if (debounce) clearTimeout(debounce);
      if (immediate) { renderPreview(); return; }
      debounce = setTimeout(renderPreview, 550);
    }

    async function renderPreview() {
      if (closed) return; // a debounced render must not outlive the modal
      const { call } = P();
      const req = ++st.seq;
      q('#gen-error').textContent = '';
      const payload = buildPayload();
      if (st.template !== 'mosaic' && !payload.background) {
        st.lastArt = null;
        applyBtn.disabled = true;
        q('#gen-stats').textContent = '';
        q('#gen-notes').innerHTML = '';
        showError({ message: BG_MISSING[st.bgKind] || 'Pick a background first.' });
        return;
      }
      applyBtn.disabled = true;
      try {
        const r = await call(window.pcm.posterGenPreview(payload));
        if (closed || req !== st.seq) return;
        st.lastArt = r.art;
        const img = q('#gen-img');
        img.src = r.art.preview;
        img.hidden = false;
        q('#gen-empty').hidden = true;
        const s = r.stats || {};
        let line = `Uses ${s.usedMembers} of ${s.memberTotal} members`;
        if (s.skippedPosters) line += ` · ${s.skippedPosters} poster${s.skippedPosters === 1 ? '' : 's'} couldn't be fetched and were skipped`;
        if (s.capApplied) line += ` · layout cap ${s.capLimit} reached`;
        q('#gen-stats').textContent = line;
        q('#gen-notes').innerHTML = (s.notes && s.notes.length)
          ? `<ul class="gen-notes-list">${s.notes.map((n) => `<li>${esc(n)}</li>`).join('')}</ul>`
          : '';
        applyBtn.disabled = false;
      } catch (e) {
        if (closed || req !== st.seq) return;
        st.lastArt = null;
        applyBtn.disabled = true;
        showError(e);
      }
    }

    function showError(e) {
      const el = q('#gen-error');
      if (!el) return;
      el.textContent = ((e && e.message) || 'Something went wrong.') + ((e && e.hint) ? ` ${e.hint}` : '');
    }

    async function onApply() {
      if (!st.lastArt) return;
      if (mode === 'draft') {
        if (window.PCMArt && typeof window.PCMArt.useArt === 'function') window.PCMArt.useArt(st.lastArt);
        modal.close('used');
        return;
      }
      applyBtn.disabled = true;
      try {
        await call(window.pcm.setPoster(opts.ratingKey, null, st.lastArt.assetId));
        toast('Generated poster applied. The previous poster is backed up — it can be restored from History.', 'success');
        modal.close('applied');
        if (typeof opts.onApplied === 'function') opts.onApplied();
      } catch (e) {
        toastErr(e);
        applyBtn.disabled = false;
      }
    }
  }

  window.PCMGenerator = { open };

  // Builder wizard: the Generate button beside Find art. The member
  // source comes from app.js's getDraftPosterSource bridge — a rules
  // draft's members are its full rule-match set (enumerated the same
  // way the review step does), NOT draft.members, which is empty by
  // design for smart drafts until review materializes them (v1.21
  // rider: the old lexical snapshot here opened the generator
  // memberless for exactly the drafts with the most members). A
  // large match set is one enumeration call — the button waits it
  // out disabled rather than opening early and empty.
  const wizBtn = document.getElementById('btn-generate-art');
  if (wizBtn) {
    wizBtn.addEventListener('click', async () => {
      const bridge = P() && P().getDraftPosterSource;
      if (typeof bridge !== 'function') return;
      const label = wizBtn.textContent;
      wizBtn.disabled = true;
      wizBtn.textContent = 'Loading…';
      try {
        const src = await bridge();
        open({ mode: 'draft', title: src.title || '', members: src.members || [], sourceNote: src.sourceNote || '' });
      } catch (e) {
        P().toastErr(e);
      } finally {
        wizBtn.disabled = false;
        wizBtn.textContent = label;
      }
    });
  }
})();
