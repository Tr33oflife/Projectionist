/* Collection artwork discovery. Selecting art changes only the Builder draft. */
(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const P = () => window.PCM;
  let selected = null;

  function reset() {
    selected = null;
    $('collection-art-selection').textContent = 'No replacement poster selected.';
  }
  function selectedFor(value) {
    return selected?.display === value ? selected.assetId : '';
  }
  function useArt(art) {
    const display = art.url || 'Imported image: ' + art.label;
    $('b-poster').value = display;
    $('b-poster').dispatchEvent(new Event('input'));
    selected = { ...art, display };
    $('collection-art-selection').innerHTML = `<div class="chosen-art"><img src="${P().esc(art.preview)}" alt="Selected replacement poster"><span>${P().esc(art.source)} · ${art.width} × ${art.height}<br>Selected in draft. Apply still requires a dry-run and confirmation.<br>Restore is available in Overlays → History after applying.</span></div>`;
  }
  async function openPicker() {
    const title = $('b-title').value.trim();
    const m = P().openModal({
      title: 'Find collection art',
      bodyHTML: `<p class="muted small">Known artwork mappings and your saved choices. TPDb browsing opens your browser; paste a title-page link to use its first poster, an image address (right-click a poster on TPDb → Copy image address) for a specific one, or import a downloaded image.</p>
        <p class="muted small">Art comes from ThePosterDB. If art requests suddenly fail as blocked, their anti-bot protection has tripped on your connection — it usually clears on its own; wait a while and try again.</p>
        <div class="field-row"><input id="art-query" class="input" value="${P().esc(title)}" placeholder="Collection name"><button id="art-find" class="btn secondary small">Suggestions</button><button id="art-browse" class="btn ghost small">Browse TPDb ↗</button></div>
        <div id="art-suggestions" class="art-suggestions"></div>
        <label class="field">TPDb page link, image address, or poster ID<input id="art-link" class="input" value="${P().esc(selected?.url || $('b-poster').value)}" placeholder="https://theposterdb.com/posters/12345"></label>
        <div class="btn-row"><button id="art-preview" class="btn secondary small">Preview link</button><button id="art-import" class="btn ghost small">Import PNG / JPEG</button></div>
        <p id="art-error" class="muted small" role="status" aria-live="polite"></p>
        <div id="art-preview-area"></div>
        <label class="check small"><input id="art-remember" type="checkbox" checked ${title ? '' : 'disabled'}><span>Remember this choice for ${P().esc(title || 'this collection (give it a name first)')}</span></label>`,
      footHTML: '<button class="btn ghost" data-close="cancel">Cancel</button><button id="art-use" class="btn primary" disabled>Use in draft</button>',
    });
    let art = null, closed = false, sequence = 0;
    m.closed.then(() => { closed = true; });
    const find = id => m.root.querySelector('#' + id);
    const error = find('art-error'), use = find('art-use');
    async function preview(load) {
      const request = ++sequence; art = null; use.disabled = true;
      error.textContent = 'Loading and validating poster…'; find('art-preview-area').innerHTML = '';
      try {
        const result = await P().call(load());
        if (closed || request !== sequence) return;
        if (result.canceled) { error.textContent = 'Import canceled.'; return; }
        art = result.art;
        find('art-preview-area').innerHTML = `<div class="art-preview"><img src="${P().esc(art.preview)}" alt="Replacement poster preview"><p>${P().esc(art.source)} · ${art.width} × ${art.height}</p></div>`;
        // Decode in Chromium as well, so only a viewable preview can be selected.
        await find('art-preview-area').querySelector('img').decode();
        if (closed || request !== sequence) return;
        find('art-preview-area').scrollIntoView({ block: 'nearest' });
        use.disabled = false; error.textContent = 'Preview only. Your Plex artwork has not changed.';
      } catch (e) { if (!closed && request === sequence) { art = null; error.textContent = e.message; } }
    }
    async function suggestions() {
      const host = find('art-suggestions'); host.textContent = 'Finding known mappings…';
      try {
        const result = await P().call(window.pcm.artSuggestions(find('art-query').value));
        if (closed) return;
        host.innerHTML = result.items.length ? result.items.map((item, i) => `<button class="art-option" data-art="${i}"><img src="${P().esc(item.url)}" crossorigin="anonymous" referrerpolicy="no-referrer" loading="lazy" alt=""><strong>${P().esc(item.title)}</strong><span>${P().esc(item.source)}${item.score === 100 ? ' · name match' : ''}</span></button>`).join('') : '<p class="muted small">No known mapping for this name. Browse TPDb or import your own image.</p>';
        host.querySelectorAll('[data-art]').forEach(button => button.addEventListener('click', () => preview(() => window.pcm.artPreview(result.items[Number(button.dataset.art)].url))));
      } catch (e) { if (!closed) host.textContent = e.message; }
    }
    find('art-find').addEventListener('click', suggestions);
    find('art-browse').addEventListener('click', () => P().call(window.pcm.artOpenTPDb(find('art-query').value)).catch(P().toastErr));
    find('art-preview').addEventListener('click', () => preview(() => window.pcm.artPreview(find('art-link').value)));
    find('art-import').addEventListener('click', () => preview(() => window.pcm.artImport()));
    find('art-link').addEventListener('input', () => { sequence++; art = null; use.disabled = true; });
    use.addEventListener('click', async () => {
      if (!art) return;
      useArt(art);
      if (title && find('art-remember').checked) {
        try { await P().call(window.pcm.artRemember(title, art.assetId)); } catch (e) { P().toastErr(e); }
      }
      m.close('selected');
    });
    suggestions();
  }
  $('btn-find-art').addEventListener('click', openPicker);
  $('btn-clear-art').addEventListener('click', () => { $('b-poster').value = ''; $('b-poster').dispatchEvent(new Event('input')); });
  $('b-poster').addEventListener('input', reset);
  // useArt is exported for the poster generator (v1.20): a generated
  // render lands in the draft through the exact same slot as a
  // picked/imported poster, so the apply flow can't tell them apart.
  window.PCMArt = { reset, selectedFor, useArt };
})();
