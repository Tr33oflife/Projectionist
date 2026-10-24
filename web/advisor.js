'use strict';

/* ============================================================================
 * advisor.js — the Advisor + Settings tabs (v0.2). The UI is UNTRUSTED.
 * ----------------------------------------------------------------------------
 * FUTURE AI — read this before touching the Advisor UI:
 *   - Everything shared with app.js comes from window.PCM (set at the
 *     bottom of app.js — $, esc, call, toast, toastErr, openModal, artSrc,
 *     state, switchTab, openBuilderFromAdvisor, openBuilderWithRules,
 *     describeRule).
 *     NEVER reach into app.js internals directly; if you need something new,
 *     add it to the PCM namespace in app.js.
 *   - The Advisor NEVER writes to Plex. Every "Create" path funnels through
 *     openBuilderFromAdvisor()/openBuilderWithRules(), which seed the v0.1
 *     Builder draft — the Builder's own dry-run → confirm → apply flow is
 *     the only write path. Keep that order.
 *   - Data flow: main process holds the FULL scan; the renderer gets a SLIM
 *     copy (slimScan in main.js). Cards reference ratingKeys; adv.byKey
 *     resolves them to slim items for display.
 *   - TMDb poster URLs (image.tmdb.org) are PUBLIC — no token needed, so
 *     they're plain https:// <img> tags, unlike Plex artwork which MUST go
 *     through artSrc() → pleximg://.
 *   - Confidence tags: 'deterministic' vs 'heuristic' come from the card —
 *     render them verbatim, never upgrade a heuristic to deterministic.
 * ========================================================================== */

(() => {
  const P = () => window.PCM;
  const $ = (id) => document.getElementById(id);

  const adv = {
    scan: null,          // slim scan from main
    byKey: new Map(),    // ratingKey -> slim item
    cards: [],
    topLists: { movies: [], tv: [] },
    drafts: [],
    meta: null,          // meta:status result (booleans only)
    settings: null,      // advisor settings
    view: 'suggestions',
    kindFilter: 'all',
    topListTab: 'movies',
    scanning: false,
    progressRegistered: false,
    verdicts: new Map(), // ratingKey -> 'dubbed'|'dual'|'subbed'|'unknown' (dub/sub view)
    selectedId: null,    // v1.18: card whose pop-out editor is/was open last (stays highlighted in the grid)
    popout: null,        // v1.18: the open pop-out editor's modal handle, if any
  };

  const KIND_LABELS = {
    franchise: 'Franchises', genre: 'Genres', decade: 'Decades',
    director: 'Directors', actor: 'Actors', studio: 'Studios',
    contentrating: 'Content ratings', unwatched: 'Unwatched',
    hiddengems: 'Hidden gems',
    trakt: 'Trakt', top100: 'Top 100s', seasonal: 'Seasonal',
    dubsub: 'Dub / Sub', tv: 'TV completeness',
    ai: 'AI suggestions',
  };
  const KIND_ORDER = ['ai', 'franchise', 'hiddengems', 'trakt', 'top100', 'genre', 'decade', 'director', 'actor', 'studio', 'contentrating', 'unwatched', 'seasonal', 'dubsub', 'tv'];

  const tmdbImg = (posterPath, size = 'w342') =>
    posterPath ? `https://image.tmdb.org/t/p/${size}${posterPath}` : '';

  // ---------- tab lifecycle ---------------------------------------------------
  document.querySelector('[data-tab="advisor"]').addEventListener('click', () => setTimeout(ensureAdvisor, 0));
  document.querySelector('[data-tab="settings"]').addEventListener('click', () => setTimeout(renderSettings, 0));

  async function ensureAdvisor() {
    if (!P().state.machine) {
      $('advisor-cards').innerHTML = '<div class="empty"><p>Sign in and pick a server on the Connect tab first.</p></div>';
      return;
    }
    if (!adv.progressRegistered) {
      window.pcm.onScanProgress(onScanProgress);
      adv.progressRegistered = true;
    }
    try {
      const [{ scan }, meta] = await Promise.all([
        P().call(window.pcm.advisorGetScan()),
        P().call(window.pcm.metaStatus()),
      ]);
      adv.meta = meta;
      renderAiStrip();
      if (scan) {
        setScan(scan, false);
        await loadSuggestions();
      } else {
        $('scan-meta').textContent = 'Not scanned yet';
        $('advisor-cards').innerHTML = '<div class="empty"><p>No scan yet.</p><p class="muted">Hit <strong>Scan library</strong> above — it reads your Plex library and joins it to TMDb (cached for 30 days, so this is a one-time cost per title).</p></div>';
        renderRail();
      }
    } catch (e) { P().toastErr(e); }
  }

  function setScan(scan, announce = true) {
    adv.scan = scan;
    adv.byKey = new Map(scan.items.map((i) => [String(i.ratingKey), i]));
    const when = new Date(scan.scannedAt);
    $('scan-meta').textContent = `${scan.itemCount} titles · scanned ${when.toLocaleString()}`;
    const pill = $('advisor-scan-pill');
    pill.hidden = false;
    pill.textContent = scan.itemCount;
    renderAiSuggestRow();
    if (announce) P().toast(`Scan complete — ${scan.itemCount} titles`, 'success');
  }

  // ---------- scan --------------------------------------------------------------
  $('btn-scan').addEventListener('click', async () => {
    if (adv.scanning) return;
    if (!P().state.machine) { P().toast('Pick a server on the Connect tab first.'); return; }
    adv.scanning = true;
    $('btn-scan').disabled = true;
    $('scan-progress').hidden = false;
    $('scan-bar-fill').style.width = '0%';
    try {
      const { scan } = await P().call(window.pcm.advisorScan());
      setScan(scan);
      await loadSuggestions();
    } catch (e) { P().toastErr(e); }
    finally {
      adv.scanning = false;
      $('btn-scan').disabled = false;
      setTimeout(() => { $('scan-progress').hidden = true; }, 1200);
    }
  });

  function onScanProgress(p) {
    const pct = p.total ? Math.min(100, Math.round((p.done / p.total) * 100)) : 0;
    $('scan-bar-fill').style.width = pct + '%';
    $('scan-note').textContent = `${p.phase === 'plex' ? 'Reading Plex library' : p.phase === 'tmdb' ? 'Matching TMDb' : p.phase === 'omdb' ? 'IMDb ratings' : 'Finishing'} — ${P().esc(p.note || '')}`;
  }

  // ---------- sub-views ------------------------------------------------------------
  document.querySelectorAll('#advisor-subnav .subnav-item').forEach((b) => b.addEventListener('click', () => {
    adv.view = b.dataset.view;
    document.querySelectorAll('#advisor-subnav .subnav-item').forEach((x) => x.classList.toggle('active', x === b));
    document.querySelectorAll('.advisor-view').forEach((v) => { v.hidden = v.id !== 'advisor-view-' + adv.view; });
    if (adv.view === 'suggestions') { renderRail(); renderCards(); }
    if (adv.view === 'top100') renderTop100();
    if (adv.view === 'dubsub') renderDubSub();
    if (adv.view === 'drafts') renderDrafts();
  }));

  // ---------- suggestions ------------------------------------------------------------
  async function loadSuggestions() {
    try {
      const { cards, topLists } = await P().call(window.pcm.advisorSuggestions());
      adv.cards = cards;
      adv.topLists = topLists || { movies: [], tv: [] };
      adv.kindFilter = 'all';
      clearCardSelection();
      renderRail();
      renderCards();
      renderAdvisorWhy();
    } catch (e) { P().toastErr(e); }
  }

  // v0.4 — empty-state honesty (complaint #3c). When suggestions are thin,
  // say WHY in plain language: which sources are active, what each unlocks,
  // and what to connect for more. Never a mysteriously thin list.
  function renderAdvisorWhy() {
    const el = $('advisor-why');
    if (!el) return;
    const meta = adv.meta || {};
    const kinds = new Set((adv.cards || []).map((c) => c.kind));
    const nativeKinds = ['genre', 'decade', 'director', 'actor', 'studio', 'contentrating', 'unwatched']
      .filter((k) => kinds.has(k)).map((k) => KIND_LABELS[k]);
    const keyed = [];
    if (meta.tmdb) keyed.push('TMDb (franchises, hidden gems, TV completeness, Top-100s)');
    if (meta.traktConnected) keyed.push('Trakt (trending, watchlist)');
    if (meta.omdb) keyed.push('OMDb (IMDb ratings)');
    const missing = [];
    if (!meta.tmdb) missing.push('a TMDb key for franchise, hidden-gem, TV-completeness and Top-100 suggestions');
    if (!meta.traktConnected) missing.push('Trakt for trending and watchlist suggestions');
    let html = `<strong>What you're seeing:</strong> ` +
      (nativeKinds.length
        ? `Plex-native suggestions (${nativeKinds.join(', ')}) \u2014 these come straight from your library and need no keys.`
        : `only Plex-native basics \u2014 your library's genre/director/actor tags came back empty, so there's little to cluster.`);
    if (keyed.length) html += ` Plus ${keyed.join(' + ')}.`;
    if (missing.length) html += ` <span class="muted">Add ${missing.join('; ')} in Settings \u2192 API keys to unlock more.</span>`;
    if (adv.scan && (adv.cards || []).length < 5) {
      html += `<br><strong>Why so few?</strong> Suggestions only appear when enough titles cluster together (for example, 8+ of one genre). A small or very diverse library means fewer clusters \u2014 that's normal, not a bug.`;
    }
    el.innerHTML = html;
    el.hidden = false;
  }

  function kindsPresent() {
    const counts = new Map();
    for (const c of adv.cards) counts.set(c.kind, (counts.get(c.kind) || 0) + 1);
    return KIND_ORDER.filter((k) => counts.has(k)).map((k) => ({ kind: k, count: counts.get(k) }));
  }

  function renderRail() {
    const rail = $('advisor-rail');
    const kinds = kindsPresent();
    const total = adv.cards.length;
    rail.innerHTML =
      `<button class="rail-item ${adv.kindFilter === 'all' ? 'active' : ''}" data-kind="all">All <span class="count-badge">${total}</span></button>` +
      kinds.map(({ kind, count }) =>
        `<button class="rail-item ${adv.kindFilter === kind ? 'active' : ''}" data-kind="${P().esc(kind)}">${P().esc(KIND_LABELS[kind] || kind)} <span class="count-badge">${count}</span></button>`
      ).join('') || '<p class="muted small">No suggestions yet — scan first.</p>';
    rail.querySelectorAll('.rail-item').forEach((b) => b.addEventListener('click', () => {
      adv.kindFilter = b.dataset.kind;
      clearCardSelection();
      renderRail(); renderCards();
    }));
  }

  function filteredCards() {
    return adv.kindFilter === 'all' ? adv.cards : adv.cards.filter((c) => c.kind === adv.kindFilter);
  }

  function confidenceTag(conf) {
    return conf === 'heuristic'
      ? '<span class="conf-tag heuristic" title="Based on heuristics (anime detection, title matching) — check the members">heuristic</span>'
      : '<span class="conf-tag deterministic" title="From Plex reads and API fields — no guessing">deterministic</span>';
  }

  function posterStrip(keys, n = 6) {
    const items = keys.map((k) => adv.byKey.get(String(k))).filter(Boolean).slice(0, n);
    return `<div class="card-strip">${items.map((i) => {
      const src = P().artSrc(i.thumb);
      return src
        ? `<img src="${P().esc(src)}" alt="" loading="lazy" title="${P().esc(i.title)}" onerror="this.remove()">`
        : `<div class="strip-fallback">${P().esc((i.title || '?')[0].toUpperCase())}</div>`;
    }).join('')}</div>`;
  }

  function renderCards() {
    const list = filteredCards();
    const el = $('advisor-cards');
    el.innerHTML = list.map((c) => `
      <div class="sugg-card${c.id === adv.selectedId ? ' selected' : ''}" data-id="${P().esc(c.id)}" role="button" tabindex="0">
        ${posterStrip(c.memberRatingKeys)}
        <div class="sugg-body">
          <div class="sugg-top"><span class="kind-chip">${P().esc(KIND_LABELS[c.kind] || c.kind)}</span>${confidenceTag(c.confidence)}</div>
          <h3>${P().esc(c.title)}</h3>
          <p class="sugg-sub">${P().esc(c.subtitle)}</p>
          <p class="sugg-reason">${P().esc(c.reason)}</p>
          ${c.kind === 'ai'
            ? `<p class="muted small">${c.memberRatingKeys.length} titles · ${c.aiVerified} of ${c.aiSuggested} suggested titles verified in your library →</p>`
            : `<p class="muted small">${c.memberRatingKeys.length} titles →</p>`}
        </div>
      </div>`).join('')
      || '<div class="empty"><p>No suggestions in this group.</p><p class="muted">Try “All”, or dismiss fewer cards.</p></div>';
    el.querySelectorAll('.sugg-card').forEach((node) => {
      const open = () => openCardPopout(adv.cards.find((c) => c.id === node.dataset.id), node);
      node.addEventListener('click', open);
      node.addEventListener('keydown', (e) => { if (e.key === 'Enter') open(); });
    });
  }

  // v1.18: there is no below-the-list detail region anymore — a card
  // click opens the pop-out editor (openCardPopout) and the list
  // never hides. Selection is a highlight on the card itself; this
  // clears it (fresh suggestion loads and rail filter changes start
  // unselected).
  function clearCardSelection() {
    adv.selectedId = null;
    document.querySelectorAll('.sugg-card.selected').forEach((n) => n.classList.remove('selected'));
  }

  function cardMembers(card) {
    return card.memberRatingKeys.map((k) => adv.byKey.get(String(k))).filter(Boolean);
  }

  // Majority section/type across members — the Builder needs one section.
  function cardTarget(card) {
    const members = cardMembers(card);
    const votes = new Map();
    for (const m of members) {
      const k = m.sectionKey + '|' + m.type;
      votes.set(k, (votes.get(k) || 0) + 1);
    }
    const [win] = [...votes.entries()].sort((a, b) => b[1] - a[1])[0] || [];
    if (!win) return { sectionKey: '', type: '1' };
    const [sectionKey, type] = win.split('|');
    return { sectionKey, type: type === 'show' ? '2' : '1' };
  }

  // v1.18 — option B: clicking a card opens a pop-out EDITOR card
  // (the app's one modal system — no second overlay mechanism) with
  // the full editor for the suggestion: an editable title, why it
  // was suggested, its kind/section, the member wall, and the
  // Create / Save-as-draft / Dismiss actions. v1.14: filter-shaped
  // suggestions carry their Builder rules, so the pop-out also
  // shows the rules summary and BOTH create choices (static member
  // list / smart collection Plex keeps in sync); the static/smart
  // choice itself still lands in the wizard exactly as before.
  // The Advisor never writes to Plex from the pop-out — Create
  // closes it and seeds the Builder wizard, whose review step
  // remains the only creation gate.
  function openCardPopout(card, node) {
    if (!card) return;
    const members = cardMembers(card);
    const t = cardTarget(card);
    const hasRules = Array.isArray(card.rules) && card.rules.length > 0;
    const typeWord = t.type === '2' ? 'TV shows' : 'Movies';
    const section = ((adv.scan && adv.scan.sections) || []).find((s) => String(s.key) === String(t.sectionKey))
      || (P().state.sections || []).find((s) => String(s.key) === String(t.sectionKey));
    // The click must visibly register: the card takes a selected
    // highlight that stays while its pop-out is open and after
    // returning to the list, until another card is picked or the
    // list reloads.
    adv.selectedId = card.id;
    document.querySelectorAll('.sugg-card.selected').forEach((n) => n.classList.remove('selected'));
    const cardNode = node || document.querySelector(`.sugg-card[data-id="${card.id}"]`);
    if (cardNode) cardNode.classList.add('selected');
    const m = P().openModal({
      title: `${KIND_LABELS[card.kind] || card.kind} suggestion`,
      bodyHTML: `
        <p class="adv-pop-meta">${confidenceTag(card.confidence)}<span class="muted small">${P().esc(section ? section.title + ' · ' : '')}${typeWord}</span></p>
        <label class="field">Collection title
          <input id="adv-pop-title" class="input" type="text" maxlength="120" value="${P().esc(card.title)}" autocomplete="off">
        </label>
        <p class="muted">${P().esc(card.subtitle)}</p>
        <p>${P().esc(card.reason)}</p>
        ${card.kind === 'ai' ? `<p class="adv-pop-ai"><span class="ai-badge">AI</span> <strong>AI suggestion</strong> · model <code>${P().esc(card.aiModel || 'unknown')}</code><br><span class="muted small">${card.aiVerified} of ${card.aiSuggested} suggested titles verified in your library — the members below are the verified ones. Anything the model named that is not in your library was dropped before this card was made.</span></p>` : ''}
        ${hasRules ? `<div class="adv-pop-rules"><h4>Rules</h4>${card.rules.map((r) => `<p class="rule-desc">${P().describeRule(r, typeWord)}</p>`).join('')}<p class="muted small"><strong>Static</strong> freezes today's member list. <strong>Smart</strong> stores the rule instead — Plex keeps the collection updated as your library changes.</p></div>` : ''}
        <h4>Members (${members.length})</h4>
        <div class="poster-grid small">${members.map((i) => `
          <div class="poster-card"><div class="poster-frame">${
            P().artSrc(i.thumb) ? `<img src="${P().esc(P().artSrc(i.thumb))}" alt="" loading="lazy" onerror="this.remove()">` : `<div class="poster-fallback">${P().esc((i.title || '?')[0].toUpperCase())}</div>`
          }</div><p class="poster-title">${P().esc(i.title)}</p><p class="poster-sub">${P().esc([i.year, i.voteAverage ? `★ ${i.voteAverage}` : ''].filter(Boolean).join(' · '))}${i.matchedByTitle ? ' <span class="heuristic-tag" title="TMDb match was by title+year, not guid">title-match</span>' : ''}</p></div>`).join('')}</div>`,
      footHTML: `
        <button class="btn ghost danger adv-pop-dismiss" id="adv-dismiss">Dismiss</button>
        <button class="btn ghost" data-close="cancel">Close</button>
        <button class="btn secondary" id="adv-draft">Save as draft</button>
        <button class="btn primary" id="adv-create">${hasRules ? 'Create as static collection' : 'Create in Builder'}</button>
        ${hasRules ? '<button class="btn secondary" id="adv-create-smart">Create as smart collection</button>' : ''}`,
    });
    adv.popout = m;
    m.closed.then(() => { if (adv.popout === m) adv.popout = null; });
    const editedTitle = () => (m.root.querySelector('#adv-pop-title').value.trim() || card.title);
    m.root.querySelector('#adv-create').addEventListener('click', () => {
      const title = editedTitle();
      m.close('create');
      P().openBuilderFromAdvisor({ title, members, sectionKey: t.sectionKey, type: t.type });
      P().toast('Draft seeded in the Builder — dry-run it before applying.', 'success');
    });
    if (hasRules) {
      m.root.querySelector('#adv-create-smart').addEventListener('click', () => {
        const title = editedTitle();
        m.close('create-smart');
        P().openBuilderWithRules({
          rules: card.rules, sectionKey: t.sectionKey, type: t.type,
          title, smart: true, seededFrom: 'advisor-smart',
        });
        P().toast('Smart draft seeded in the Builder — Preview the rules, then dry-run before applying.', 'success');
      });
    }
    m.root.querySelector('#adv-draft').addEventListener('click', async () => {
      try {
        await P().call(window.pcm.advisorSaveDraft({
          title: editedTitle(), memberRatingKeys: card.memberRatingKeys,
          sectionKey: t.sectionKey, type: t.type, reason: card.reason,
        }));
        P().toast('Draft saved — find it under Drafts.', 'success');
        refreshDraftsPill();
      } catch (e) { P().toastErr(e); }
    });
    m.root.querySelector('#adv-dismiss').addEventListener('click', async () => {
      try {
        await P().call(window.pcm.advisorDismiss(card.id));
        adv.cards = adv.cards.filter((c) => c.id !== card.id);
        if (adv.selectedId === card.id) adv.selectedId = null;
        m.close('dismiss');
        renderRail(); renderCards();
        P().toast('Suggestion dismissed. Reset them all in Settings.', '', '');
      } catch (e) { P().toastErr(e); }
    });
  }
  // ---------- top 100s -------------------------------------------------------------------
  document.querySelectorAll('#top100-tabs .section-tab').forEach((b) => b.addEventListener('click', () => {
    adv.topListTab = b.dataset.list;
    document.querySelectorAll('#top100-tabs .section-tab').forEach((x) => x.classList.toggle('active', x === b));
    renderTop100();
  }));
  // v0.4: owned-first. "You own" is the primary sub-tab; "Recommendations"
  // (not on your server) is secondary and clearly labeled.
  document.querySelectorAll('#top100-own-tabs .section-tab').forEach((b) => b.addEventListener('click', () => {
    adv.top100Own = b.dataset.own;
    document.querySelectorAll('#top100-own-tabs .section-tab').forEach((x) => x.classList.toggle('active', x === b));
    renderTop100();
  }));
  $('btn-top100-make').addEventListener('click', () => makeTop100Collection());

  function renderTop100() {
    // v0.4 — ownership first (the owner's words: "I want to see what I already
    // have"). The primary view is "Top 100 you OWN" with a one-click
    // "Make it a collection" button; the "not on your server"
    // recommendations are a clearly-labeled secondary sub-tab.
    const list = adv.topListTab === 'movies' ? adv.topLists.movies : adv.topLists.tv;
    const grid = $('top100-grid');
    if (!adv.top100Own) adv.top100Own = 'owned';
    if (!adv.scan) { grid.innerHTML = '<div class="empty"><p>Scan your library first.</p></div>'; return; }
    if (!list.length) {
      grid.innerHTML = '<div class="empty"><p>No list data yet.</p><p class="muted">Top-100 lists need a TMDb key (Settings \u2192 API keys) and a fresh Suggestions load.</p></div>';
      return;
    }
    const owned = list.filter((e) => e.owned);
    const recs = list.filter((e) => !e.owned);
    $('top100-owned-count').textContent = owned.length;
    $('top100-recs-count').textContent = recs.length;
    const showingOwned = adv.top100Own !== 'recs';
    const showing = showingOwned ? owned : recs;
    document.querySelectorAll('#top100-own-tabs .section-tab').forEach((x) => x.classList.toggle('active', x.dataset.own === adv.top100Own));
    $('top100-owned-bar').hidden = !showingOwned;
    if (showingOwned) {
      const label = adv.topListTab === 'movies' ? 'movies' : 'shows';
      $('top100-owned-note').textContent = owned.length
        ? `You own ${owned.length} of the TMDb Top 100 ${label} \u2014 one click turns them into a collection.`
        : `None of the TMDb Top 100 ${label} are on your server yet \u2014 try the Recommendations tab.`;
      $('btn-top100-make').disabled = owned.length < 1;
      $('btn-top100-make').title = owned.length < 1 ? 'Nothing to collect yet' : 'Send these titles to the Builder (dry-run first, as always)';
    }
    if (!showing.length) {
      grid.innerHTML = `<div class="empty"><p>${showingOwned ? 'None of the top 100 are on your server yet.' : 'Everything on the list is already yours. Nice.'}</p></div>`;
      return;
    }
    grid.innerHTML = showing.map((e, i) => `
      <div class="poster-card">
        <div class="poster-frame">
          ${tmdbImg(e.posterPath, 'w342') ? `<img src="${tmdbImg(e.posterPath, 'w342')}" alt="" loading="lazy" onerror="this.remove()">` : `<div class="poster-fallback">${i + 1}</div>`}
          <span class="rank-chip">#${i + 1}</span>
          ${e.owned ? '<span class="owned-chip">\u2713 Owned</span>' : ''}
        </div>
        <p class="poster-title">${P().esc(e.title)}</p>
        <p class="poster-sub">${P().esc([e.releaseDate ? e.releaseDate.slice(0, 4) : '', e.voteAverage ? `\u2605 ${e.voteAverage}` : ''].filter(Boolean).join(' \u00b7 '))}</p>
      </div>`).join('');
  }

  // v0.4 — "Make it a collection": hands the owned Top-100 members to the
  // Builder's normal dry-run flow. The Advisor never writes to Plex itself.
  function makeTop100Collection() {
    const list = adv.topListTab === 'movies' ? adv.topLists.movies : adv.topLists.tv;
    const owned = (list || []).filter((e) => e.owned);
    const members = owned.map((e) => adv.byKey.get(String(e.ratingKey))).filter(Boolean);
    if (!members.length) { P().toast('No library members found for these titles.', 'error'); return; }
    const label = adv.topListTab === 'movies' ? 'Movies' : 'Shows';
    const first = members[0];
    P().openBuilderFromAdvisor({
      title: `TMDb Top 100 ${label} \u2014 My Collection`,
      members,
      sectionKey: first.sectionKey,
      type: first.type === 'show' ? '2' : '1',
    });
    P().toast(`${members.length} titles sent to the Builder \u2014 review the dry-run, then apply.`, 'success');
  }

  // ---------- dub / sub ----------------------------------------------------------------------
  // Verdicts come from dubsub-kind cards (cached scan verdicts). Anything
  // anime-flagged without a verdict gets a "Scan audio" button (lazy).
  function rebuildVerdicts() {
    adv.verdicts.clear();
    for (const c of adv.cards) {
      if (c.kind !== 'dubsub') continue;
      const verdict = c.id.split(':')[1]; // dubsub:dubbed
      for (const k of c.memberRatingKeys) adv.verdicts.set(String(k), verdict);
    }
  }

  function renderDubSub() {
    const colNames = ['dubbed', 'dual', 'subbed', 'unknown'];
    if (!adv.scan) {
      for (const c of colNames) $('dub-col-' + c).innerHTML = '<p class="muted">Scan first.</p>';
      return;
    }
    rebuildVerdicts();
    const anime = adv.scan.items.filter((i) => i.anime);
    const cols = { dubbed: [], dual: [], subbed: [], unknown: [] };
    const unscanned = [];
    for (const i of anime) {
      const v = adv.verdicts.get(String(i.ratingKey));
      if (v && cols[v]) cols[v].push(i);
      else unscanned.push(i);
    }
    const rowHTML = (i, v) => `
      <div class="dub-row" data-rk="${P().esc(i.ratingKey)}">
        ${P().artSrc(i.thumb) ? `<img src="${P().esc(P().artSrc(i.thumb))}" alt="" loading="lazy" onerror="this.remove()">` : ''}
        <div class="t"><strong>${P().esc(i.title)}</strong><span>${P().esc(i.year || '')}</span></div>
        ${v ? `<span class="verdict-dot ${P().esc(v)}" title="${P().esc(v)}"></span>
               <button class="btn ghost small" data-drill="${P().esc(i.ratingKey)}">Detail</button>
               <button class="btn ghost small" data-rescan="${P().esc(i.ratingKey)}" title="Force re-scan of audio tracks">⟳</button>`
          : `<button class="btn secondary small" data-scan="${P().esc(i.ratingKey)}">Scan audio</button>`}
      </div>`;
    $('dub-col-dubbed').innerHTML = cols.dubbed.map((i) => rowHTML(i, 'dubbed')).join('') || '<p class="muted small">None yet.</p>';
    $('dub-col-dual').innerHTML = cols.dual.map((i) => rowHTML(i, 'dual')).join('') || '<p class="muted small">None yet.</p>';
    $('dub-col-subbed').innerHTML = cols.subbed.map((i) => rowHTML(i, 'subbed')).join('') || '<p class="muted small">None yet.</p>';
    $('dub-col-unknown').innerHTML =
      cols.unknown.map((i) => rowHTML(i, 'unknown')).join('') +
      unscanned.map((i) => rowHTML(i, null)).join('') || '<p class="muted small">None yet.</p>';
    $('dub-count-dubbed').textContent = cols.dubbed.length;
    $('dub-count-dual').textContent = cols.dual.length;
    $('dub-count-subbed').textContent = cols.subbed.length;
    $('dub-count-unknown').textContent = cols.unknown.length + unscanned.length;

    document.querySelectorAll('[data-scan]').forEach((b) => b.addEventListener('click', async () => {
      b.disabled = true;
      try {
        const { verdict } = await P().call(window.pcm.advisorDubVerdict(b.dataset.scan, false));
        adv.verdicts.set(String(b.dataset.scan), verdict.verdict);
        renderDubSub();
        await loadSuggestionsSilently();
      } catch (e) { P().toastErr(e); b.disabled = false; }
    }));
    document.querySelectorAll('[data-rescan]').forEach((b) => b.addEventListener('click', async () => {
      b.disabled = true;
      try {
        const { verdict } = await P().call(window.pcm.advisorDubVerdict(b.dataset.rescan, true));
        adv.verdicts.set(String(b.dataset.rescan), verdict.verdict);
        renderDubSub();
        await loadSuggestionsSilently();
      } catch (e) { P().toastErr(e); b.disabled = false; }
    }));
    document.querySelectorAll('[data-drill]').forEach((b) => b.addEventListener('click', async () => {
      try {
        const { verdict } = await P().call(window.pcm.advisorDubVerdict(b.dataset.drill, false));
        showDubDetail(verdict);
      } catch (e) { P().toastErr(e); }
    }));
  }

  async function loadSuggestionsSilently() {
    // Refresh cards after a dub scan without nuking the current view.
    try {
      const { cards, topLists } = await P().call(window.pcm.advisorSuggestions());
      adv.cards = cards; adv.topLists = topLists || adv.topLists;
      renderRail();
      if (adv.view === 'suggestions' && !adv.popout) renderCards();
    } catch { /* non-fatal */ }
  }

  function showDubDetail(verdict) {
    const item = adv.byKey.get(String(verdict.ratingKey));
    const seasons = (verdict.perSeason || []).map((s) => `
      <div class="dub-season">
        <div class="row-between"><strong>${P().esc(s.season)}</strong><span class="verdict-dot ${P().esc(s.verdict)}"></span></div>
        <p class="muted small">${s.episodes} episodes — ${P().esc(s.verdict)}</p>
        <div class="dub-eps">${(s.episodeVerdicts || []).map((e) =>
          `<span class="dub-ep ${P().esc(e.verdict)}" title="${P().esc(e.title)} — ${P().esc(e.verdict)}"></span>`).join('')}</div>
      </div>`).join('');
    const det = verdict.detail || {};
    P().openModal({
      title: `${item ? item.title : 'Audio verdict'} — ${verdict.verdict}`,
      bodyHTML: `
        <p>${P().esc(verdict.note || '')}</p>
        ${verdict.type === 'movie' && det.audioTracks != null ? `<p class="muted small">${det.audioTracks} audio track(s)${det.tracksUnclear ? ' — <span class="heuristic-tag">tracks unclear</span>' : ''}</p>` : ''}
        ${seasons || '<p class="muted">No season detail.</p>'}
        <p class="fineprint">Commentary/descriptive tracks are ignored (heuristic). A series takes its least-dubbed episode's verdict.</p>`,
      footHTML: `<button class="btn ghost" data-close="ok">Close</button>`,
    });
  }

  // ---------- drafts ----------------------------------------------------------------------------
  async function refreshDraftsPill() {
    try {
      const { drafts } = await P().call(window.pcm.advisorGetDrafts());
      adv.drafts = drafts;
      const pill = $('drafts-pill');
      pill.hidden = !drafts.length;
      pill.textContent = drafts.length;
    } catch { /* non-fatal */ }
  }

  async function renderDrafts() {
    await refreshDraftsPill();
    const el = $('drafts-list');
    el.innerHTML = adv.drafts.map((d) => `
      <div class="draft-card">
        <div><h3>${P().esc(d.title)}</h3>
        <p class="muted small">${d.memberRatingKeys.length} titles · saved ${P().esc(d.savedAt ? new Date(d.savedAt).toLocaleString() : '')}</p>
        ${d.reason ? `<p class="muted small">${P().esc(d.reason)}</p>` : ''}</div>
        <div class="draft-actions">
          <button class="btn primary small" data-load="${P().esc(d.id)}">Load in Builder</button>
          <button class="btn ghost small danger" data-del="${P().esc(d.id)}">Delete</button>
        </div>
      </div>`).join('') || '<div class="empty"><p>No drafts saved.</p><p class="muted">Open any suggestion and hit “Save as draft”.</p></div>';
    el.querySelectorAll('[data-load]').forEach((b) => b.addEventListener('click', () => {
      const d = adv.drafts.find((x) => x.id === b.dataset.load);
      if (!d) return;
      const members = d.memberRatingKeys.map((k) => adv.byKey.get(String(k))).filter(Boolean);
      P().openBuilderFromAdvisor({ title: d.title, members, sectionKey: d.sectionKey, type: d.type });
    }));
    el.querySelectorAll('[data-del]').forEach((b) => b.addEventListener('click', async () => {
      try { await P().call(window.pcm.advisorDeleteDraft(b.dataset.del)); renderDrafts(); }
      catch (e) { P().toastErr(e); }
    }));
  }

  // v0.4 — AI strip errors are ALSO shown inline (not just a toast), with
  // a "check Settings" hint. Silent nulls were the owner's complaint #4.
  function aiStripError(msg) {
    let el = $('ai-strip-error');
    if (!el) {
      el = document.createElement('p');
      el.id = 'ai-strip-error';
      el.className = 'ai-err';
      $('ai-strip').after(el);
    }
    el.hidden = !msg;
    el.innerHTML = msg ? `${P().esc(msg)} <button class="linklike" id="ai-err-settings">check Settings</button>` : '';
    const go = $('ai-err-settings');
    if (go) go.addEventListener('click', () => document.querySelector('[data-tab="settings"]').click());
  }

  // ---------- AI strip (BYOK, off by default) ---------------------------------------------------------
  function renderAiStrip() {
    const on = !!(adv.meta && adv.meta.ai);
    $('ai-strip').hidden = !on;
    $('ai-strip-hint').hidden = on;
    renderAiSuggestRow();
  }

  // v1.21: the AI collection-suggestions row (per section, built
  // from the most recent scan). Visible exactly when the AI strip
  // is; the section select mirrors the current scan (renderAiStrip
  // runs before setScan on tab entry, so setScan re-runs this too).
  function renderAiSuggestRow() {
    const row = $('ai-suggest-row');
    if (!row) return;
    const on = !!(adv.meta && adv.meta.ai);
    row.hidden = !on;
    if (!on) return;
    const sel = $('ai-section');
    const prev = sel.value;
    const sections = (adv.scan && adv.scan.sections) || [];
    sel.innerHTML = sections.map((s) => `<option value="${P().esc(s.key)}">${P().esc(s.title)}</option>`).join('');
    if (sections.some((s) => String(s.key) === String(prev))) sel.value = prev;
    sel.disabled = sections.length === 0;
    $('btn-ai-suggest').disabled = sections.length === 0;
  }

  // v1.21: ask the AI for collection suggestions for one section.
  // The server verifies every proposed member against the scan
  // before a card exists (hallucinated keys never render); the
  // returned cards join adv.cards as kind 'ai' and flow through the
  // same rail, pop-out editor, Builder seeding, drafts and dismissal
  // store as every other suggestion. Re-running for a section
  // replaces that section's previous AI cards.
  $('btn-ai-suggest').addEventListener('click', async () => {
    if (!adv.scan) { P().toast('Scan your library first — AI suggestions are built from the scan.'); return; }
    const sel = $('ai-section');
    const sectionKey = sel.value;
    if (!sectionKey) return;
    const sectionTitle = sel.options[sel.selectedIndex] ? sel.options[sel.selectedIndex].textContent : 'this section';
    const btn = $('btn-ai-suggest');
    const status = $('ai-suggest-status');
    btn.disabled = true;
    status.hidden = false;
    status.textContent = 'Asking the AI to survey ' + sectionTitle + '… (large sections are sent in batches — this can take a minute)';
    try {
      const res = await P().call(window.pcm.aiSuggestCollections(sectionKey));
      const fresh = res.cards || [];
      adv.cards = adv.cards.filter((c) => !(c.kind === 'ai' && c.id.startsWith('ai:' + sectionKey + ':')));
      adv.cards = adv.cards.concat(fresh);
      adv.cards.sort((a, b) => (b.memberRatingKeys.length - a.memberRatingKeys.length)
        || a.title.toLowerCase().localeCompare(b.title.toLowerCase()));
      clearCardSelection();
      renderRail(); renderCards();
      const calls = res.calls === 1 ? '1 AI call' : res.calls + ' AI calls';
      if (fresh.length) {
        status.textContent = 'AI suggested ' + fresh.length + (fresh.length === 1 ? ' collection' : ' collections')
          + ' for ' + sectionTitle + ' · ' + calls + ' · model ' + (res.model || '')
          + (res.sampled ? ' · the section has ' + res.totalItems + ' titles; the AI saw a spread of ' + res.sawItems : '');
        P().toast('AI suggestions added — find them under “AI suggestions”.', 'success');
      } else {
        status.textContent = 'The AI proposed nothing that verified against your library for ' + sectionTitle + ' · ' + calls + '.';
        P().toast('No AI suggestions survived verification.', '');
      }
    } catch (e) {
      status.hidden = true;
      P().toastErr(e);
    } finally {
      btn.disabled = false;
    }
  });

  $('btn-ai-goto-settings').addEventListener('click', () => {
    document.querySelector('[data-tab="settings"]').click();
  });

  $('btn-ai-rules').addEventListener('click', async () => {
    const text = $('ai-nl-input').value.trim();
    if (!text) { P().toast('Describe the collection first.'); return; }
    aiStripError('');
    try {
      const { proposal } = await P().call(window.pcm.aiProposeRules(text));
      if (!proposal || !proposal.rules.length) {
        P().toast('The AI came back empty — try rephrasing, or build the rules by hand.', 'error');
        return;
      }
      const m = await P().openModal({
        title: 'AI-proposed rules',
        bodyHTML: `<p class="muted small">${P().esc(proposal.note || '')} <span class="ai-badge">AI</span></p>
          <p class="fineprint">These open as <strong>editable</strong> rules in the Builder. Nothing is valid until you <strong>Preview</strong> it — the deterministic preview is the check.</p>
          <div class="rule-proposal">${proposal.rules.map((r) => `<code>${P().esc(r.field)} ${P().esc(r.op)} ${P().esc(r.value)}</code>`).join('')}</div>`,
        footHTML: `<button class="btn ghost" data-close="no">Cancel</button><button class="btn primary" data-close="yes">Send to Builder</button>`,
      }).closed;
      if (m === 'yes') {
        const t = adv.scan ? cardTargetLike(adv.scan.items) : { sectionKey: '', type: '1' };
        P().openBuilderWithRules({ rules: proposal.rules, sectionKey: t.sectionKey, type: t.type, note: proposal.note });
      }
    } catch (e) { P().toastErr(e); aiStripError(e.message || String(e)); }
  });

  function cardTargetLike(items) {
    const first = items.find((i) => i.sectionKey);
    return { sectionKey: first ? first.sectionKey : '', type: first && first.type === 'show' ? '2' : '1' };
  }

  $('btn-ai-merge').addEventListener('click', async () => {
    const names = adv.cards.filter((c) => c.kind === 'franchise').map((c) => c.title);
    if (names.length < 2) { P().toast('Need at least 2 franchise suggestions to tidy.'); return; }
    try {
      const { proposal } = await P().call(window.pcm.aiProposeFranchiseMerge(names));
      if (!proposal || !proposal.merges.length) {
        P().toast(proposal && proposal.note ? proposal.note : 'Nothing looked mergeable.', '', '');
        return;
      }
      const body = proposal.merges.map((mg, i) => `
        <div class="merge-row">
          <div><strong>${P().esc(mg.suggestedTitle || 'Merged saga')}</strong><p class="muted small">${mg.names.map(P().esc).join(' + ')}</p></div>
          <button class="btn secondary small" data-merge="${i}">Create draft</button>
        </div>`).join('');
      const m = P().openModal({
        title: 'AI franchise tidy-up',
        bodyHTML: `<p class="fineprint"><span class="ai-badge">AI</span> ${P().esc(proposal.note || '')} — you confirm each merge; nothing writes to Plex.</p>${body}`,
        footHTML: `<button class="btn ghost" data-close="x">Close</button>`,
      });
      m.root.querySelectorAll('[data-merge]').forEach((b) => b.addEventListener('click', () => {
        const mg = proposal.merges[+b.dataset.merge];
        const keys = new Set();
        for (const n of mg.names) {
          const c = adv.cards.find((x) => x.kind === 'franchise' && x.title === n);
          if (c) c.memberRatingKeys.forEach((k) => keys.add(String(k)));
        }
        const members = [...keys].map((k) => adv.byKey.get(k)).filter(Boolean);
        const t = cardTargetLike(members);
        m.close('x');
        P().openBuilderFromAdvisor({ title: mg.suggestedTitle || 'Merged saga', members, sectionKey: t.sectionKey, type: t.type });
      }));
      await m.closed;
    } catch (e) { P().toastErr(e); }
  });

  // ---------- settings -------------------------------------------------------------------------------
  const KEY_DEFS = [
    { which: 'tmdb', label: 'TMDb API key', desc: 'The workhorse — franchises, runtime, ratings, languages, top-100s. You have one from the Media Hub days.', flag: 'tmdb' },
    { which: 'traktClientId', label: 'Trakt client ID', desc: 'Create an app at trakt.tv/oauth/applications (~2 min). Enables trending lists; watchlist needs OAuth below.', flag: 'traktClientId' },
    { which: 'traktClientSecret', label: 'Trakt client secret', desc: 'From the same Trakt app. Needed to finish OAuth.', flag: 'traktClientSecret' },
    { which: 'omdb', label: 'OMDb API key', desc: 'IMDb ratings via OMDb — IMDb has no public API, and the badge says so wherever it appears. Free tier: 1,000/day.', flag: 'omdb' },
    { which: 'fanart', label: 'Fanart.tv API key', desc: 'For the v0.3 overlay studio (transparent logos). Free personal key.', flag: 'fanart' },
    { which: 'aiKey', label: 'AI API key', desc: 'Optional — enables the AI strip on the Advisor tab. Any OpenAI-compatible endpoint.', flag: 'ai' },
  ];

  // v1.20 hotfix — the AI base URL / model fields save on 'change'
  // (asynchronous), but renderSettings() used to rewrite both fields
  // from adv.meta unconditionally. When a re-render raced a field
  // save still in flight — classically: edit both fields, then click
  // an API key's Save, whose handler refreshes adv.meta BEFORE the
  // field saves land — the fields were rewritten with the PRE-save
  // values and their dirty state reset, so the panel showed the old
  // provider for the rest of the session even though the save had
  // landed. Two guards: (1) a field the user is editing (focused or
  // dirty) is never overwritten by a render; (2) the change handlers
  // refresh adv.meta from the server once their save completes, so
  // later renders carry the saved values, not stale ones.
  const aiFieldDirty = { 'ai-base-url': false, 'ai-model': false };
  function writeAiField(id, value) {
    const el = $(id);
    if (!el) return;
    if (document.activeElement === el || aiFieldDirty[id]) return;
    el.value = value;
  }

  async function renderSettings() {
    if (!adv.meta) {
      try { adv.meta = (await P().call(window.pcm.metaStatus())); }
      catch (e) { P().toastErr(e); return; }
    }
    // key rows
    $('keys-list').innerHTML = KEY_DEFS.map((k) => `
      <div class="key-row">
        <div><strong>${P().esc(k.label)}</strong>
          <p class="muted small">${P().esc(k.desc)}</p>
          <span class="key-status ${adv.meta[k.flag] ? 'set' : ''}">${adv.meta[k.flag] ? '● Set' : '○ Not set'}</span>
        </div>
        <div class="key-actions">
          <input class="input" type="password" data-key="${P().esc(k.which)}" placeholder="${adv.meta[k.flag] ? '•••••••• (paste new to replace)' : 'Paste key…'}" autocomplete="off">
          <button class="btn secondary small" data-save="${P().esc(k.which)}">Save</button>
          ${adv.meta[k.flag] ? `<button class="btn ghost small danger" data-clear="${P().esc(k.which)}">Clear</button>` : ''}
        </div>
      </div>`).join('');
    $('keys-list').querySelectorAll('[data-save]').forEach((b) => b.addEventListener('click', async () => {
      const which = b.dataset.save;
      const input = $('keys-list').querySelector(`[data-key="${which}"]`);
      try {
        await P().call(window.pcm.metaSetKey(which, input.value.trim()));
        adv.meta = await P().call(window.pcm.metaStatus());
        renderAiStrip();
        renderSettings();
        P().toast('Key saved.', 'success');
      } catch (e) { P().toastErr(e); }
    }));
    $('keys-list').querySelectorAll('[data-clear]').forEach((b) => b.addEventListener('click', async () => {
      try {
        await P().call(window.pcm.metaSetKey(b.dataset.clear, ''));
        adv.meta = await P().call(window.pcm.metaStatus());
        renderAiStrip();
        renderSettings();
      } catch (e) { P().toastErr(e); }
    }));
    // trakt oauth row
    const oauthBtn = $('btn-trakt-oauth');
    oauthBtn.textContent = adv.meta.traktConnected ? 'Disconnect Trakt' : 'Connect Trakt';
    oauthBtn.disabled = !adv.meta.traktConnected && !adv.meta.traktClientId;
    oauthBtn.title = oauthBtn.disabled ? 'Paste the Trakt client ID first' : '';
    // ai base url + model (v0.4: model is a setting — required for Google AI Studio)
    writeAiField('ai-base-url', adv.meta.aiBaseUrl || '');
    writeAiField('ai-model', adv.meta.aiModel || '');
    // donate username (v1.22): prefilled with the author's public
    // Ko-fi identity when nothing is stored yet; never overwritten
    // while focused — the same discipline as the AI fields above.
    // The toggle (v1.23) defaults ON when nothing is stored, and is
    // likewise left alone while it holds focus, so an uncommitted
    // click is never reverted by a settings re-render.
    if ($('donate-username') && document.activeElement !== $('donate-username')) $('donate-username').value = adv.meta.donateUsername || 't4v3rn';
    if ($('donate-enabled') && document.activeElement !== $('donate-enabled')) $('donate-enabled').checked = adv.meta.donateEnabled !== false;
    if (typeof refreshDonateWidget === 'function') refreshDonateWidget();
    // advisor tuning form
    try {
      const { settings } = await P().call(window.pcm.advisorGetSettings());
      adv.settings = settings;
      const sections = P().state.sections || [];
      $('advisor-settings-form').innerHTML = `
        <div class="field-row">
          <label class="field">Genre threshold <input class="input" type="number" min="2" max="50" data-s="genreThreshold" value="${settings.genreThreshold}"></label>
          <label class="field">Decade threshold <input class="input" type="number" min="2" max="50" data-s="decadeThreshold" value="${settings.decadeThreshold}"></label>
        </div>
        <div class="field-row">
          <label class="field">Director threshold <input class="input" type="number" min="2" max="20" data-s="directorThreshold" value="${settings.directorThreshold}"></label>
          <label class="field">Actor threshold <input class="input" type="number" min="2" max="20" data-s="actorThreshold" value="${settings.actorThreshold}"></label>
          <label class="field">Studio threshold <input class="input" type="number" min="2" max="20" data-s="studioThreshold" value="${settings.studioThreshold}"></label>
        </div>
        <label class="field">Hidden-gem min rating <input class="input" type="number" min="0" max="10" step="0.5" data-s="hiddenGemRating" value="${settings.hiddenGemRating}"></label>
        <label class="check"><input type="checkbox" data-s="imdbBadges" ${settings.imdbBadges ? 'checked' : ''}> <span>Fetch IMDb ratings via OMDb during scans</span></label>
        <div class="field">Anime sections <span class="hint">(always treated as anime for dub/sub)</span>
          ${sections.map((s) => `<label class="check"><input type="checkbox" data-asec="${P().esc(s.key)}" ${(settings.animeSections || []).includes(s.key) ? 'checked' : ''}> <span>${P().esc(s.title)}</span></label>`).join('') || '<p class="muted small">Sign in first — sections load from your server.</p>'}
        </div>
        <button class="btn primary small" id="btn-save-advisor-settings">Save advisor settings</button>`;
      $('btn-save-advisor-settings').addEventListener('click', async () => {
        const obj = {};
        $('advisor-settings-form').querySelectorAll('[data-s]').forEach((el) => {
          obj[el.dataset.s] = el.type === 'checkbox' ? el.checked : Number(el.value);
        });
        obj.animeSections = [...$('advisor-settings-form').querySelectorAll('[data-asec]:checked')].map((el) => el.dataset.asec);
        try {
          const r = await P().call(window.pcm.advisorSetSettings(obj));
          adv.settings = r.settings;
          P().toast('Advisor settings saved.', 'success');
        } catch (e) { P().toastErr(e); }
      });
    } catch (e) { $('advisor-settings-form').innerHTML = `<p class="muted">${P().esc(e.message)}</p>`; }
    // cache panel
    try {
      const { bytes, files } = await P().call(window.pcm.advisorCacheStatus());
      $('cache-meta').textContent = `Cache: ${files} files · ${(bytes / 1024).toFixed(0)} KB under advisor-cache/ (30d TMDb/OMDb, 24h lists, 90d dub verdicts)`;
    } catch { $('cache-meta').textContent = 'Cache: unavailable'; }
  }

  $('btn-trakt-oauth').addEventListener('click', async () => {
    const btn = $('btn-trakt-oauth');
    try {
      if (adv.meta && adv.meta.traktConnected) {
        await P().call(window.pcm.metaTraktDisconnect());
        P().toast('Trakt disconnected.', '', '');
      } else {
        // OOB flow: main opens the Trakt approval in the browser; Trakt
        // shows the owner a code, which he pastes here. No localhost server.
        btn.disabled = true;
        btn.textContent = 'Opening browser…';
        await P().call(window.pcm.metaTraktOAuthStart());
        const m = P().openModal({
          title: 'Connect Trakt',
          bodyHTML: '<p>Approve the app in the browser tab that just opened. Trakt will show you a <b>code</b> — paste it below.</p><input id="trakt-code" class="input" placeholder="Paste Trakt code" autocomplete="off" spellcheck="false">',
          footHTML: '<button class="btn ghost" data-close="cancel">Cancel</button><button class="btn primary" id="trakt-code-go">Connect</button>',
        });
        m.root.querySelector('#trakt-code-go').addEventListener('click', async () => {
          const code = m.root.querySelector('#trakt-code').value.trim();
          if (!code) return;
          try {
            await P().call(window.pcm.metaTraktOAuthFinish(code));
            m.close('done');
          } catch (e) { P().toastErr(e); }
        });
        const res = await m.closed;
        if (res === 'done') P().toast('Trakt connected.', 'success');
      }
      adv.meta = await P().call(window.pcm.metaStatus());
      renderSettings();
    } catch (e) { P().toastErr(e); renderSettings(); }
  });

  document.querySelector('#tab-settings').addEventListener('input', (e) => {
    if (e.target.id === 'ai-base-url' || e.target.id === 'ai-model') aiFieldDirty[e.target.id] = true;
  });

  document.querySelector('#tab-settings').addEventListener('change', async (e) => {
    if (e.target.id === 'ai-base-url') {
      try {
        await P().call(window.pcm.metaSetAiBaseUrl(e.target.value.trim()));
        aiFieldDirty['ai-base-url'] = false;
        // Refresh adv.meta from the server's fresh state, so a later
        // renderSettings() can never resurrect the pre-save value.
        adv.meta = await P().call(window.pcm.metaStatus());
      } catch (err) { P().toastErr(err); }
    }
    // v0.4: the model must exist on the configured base URL (e.g.
    // gemini-2.0-flash for Google AI Studio — 'gpt-4o-mini' 404s there).
    if (e.target.id === 'ai-model') {
      try {
        await P().call(window.pcm.metaSetAiModel(e.target.value.trim()));
        aiFieldDirty['ai-model'] = false;
        adv.meta = await P().call(window.pcm.metaStatus());
      } catch (err) { P().toastErr(err); }
    }
  });

  // Explicit Save for the AI fields (v1.21.2, owner's order): the two
  // fields have always saved on 'change', but a provider edit should
  // not depend on blur. One click commits BOTH through the same
  // bridge calls the change handlers use, then refreshes adv.meta —
  // the same anti-revert discipline: dirty flags clear only as each
  // save lands, and later renders read the saved values.
  $('btn-ai-save').addEventListener('click', async () => {
    const btn = $('btn-ai-save');
    btn.disabled = true;
    try {
      await P().call(window.pcm.metaSetAiBaseUrl($('ai-base-url').value.trim()));
      aiFieldDirty['ai-base-url'] = false;
      await P().call(window.pcm.metaSetAiModel($('ai-model').value.trim()));
      aiFieldDirty['ai-model'] = false;
      adv.meta = await P().call(window.pcm.metaStatus());
      renderAiStrip();
      P().toast('AI settings saved.', 'success');
    } catch (e) { P().toastErr(e); }
    finally { btn.disabled = false; }
  });

  // v0.4 — "Test connection": sends a tiny prompt, shows the RAW reply or
  // the EXACT error (HTTP status + body snippet). No more silent AI failure.
  $('btn-ai-test').addEventListener('click', async () => {
    const box = $('ai-test-result');
    const btn = $('btn-ai-test');
    btn.disabled = true;
    box.hidden = false;
    box.innerHTML = '<p class="muted">Sending a tiny prompt&hellip;</p>';
    try {
      const { result } = await P().call(window.pcm.aiTestConnection());
      box.innerHTML = `<p class="ai-ok"><strong>OK</strong> &mdash; ${P().esc(result.ms)}ms</p>
        <p class="muted small">model <code>${P().esc(result.model)}</code> &middot; <code>${P().esc(result.baseUrl)}</code></p>
        <p>Reply: <code>${P().esc(result.reply || '(empty reply — the endpoint answered, but said nothing)')}</code></p>`;
    } catch (e) {
      // Surface the EXACT failure: status + message + hint. This is what
      // the owner needs to diagnose his AI Studio endpoint.
      box.innerHTML = `<p class="ai-err"><strong>Failed:</strong> ${P().esc(e.message || String(e))}</p>
        ${e.hint ? `<p class="muted small">${P().esc(e.hint)}</p>` : ''}
        <p class="muted small">Check the base URL, the model name, and the key &mdash; the message above is what the endpoint actually returned.</p>`;
    } finally { btn.disabled = false; }
  });

  $('btn-clear-cache').addEventListener('click', async () => {
    const m = await P().openModal({
      title: 'Clear API cache?',
      bodyHTML: '<p>This re-fetches TMDb / Trakt / OMDb data on the next scan (rate limits apply). Your dismissals, drafts and settings are kept.</p>',
      footHTML: '<button class="btn ghost" data-close="no">Cancel</button><button class="btn danger" data-close="yes">Clear cache</button>',
    }).closed;
    if (m !== 'yes') return;
    try { await P().call(window.pcm.advisorClearCache()); renderSettings(); P().toast('Cache cleared.', 'success'); }
    catch (e) { P().toastErr(e); }
  });

  $('btn-reset-dismissals').addEventListener('click', async () => {
    try { await P().call(window.pcm.advisorResetDismissals()); P().toast('Dismissed suggestions restored.', 'success'); }
    catch (e) { P().toastErr(e); }
  });

  // ---- Backup & restore (v1.22) -------------------------------------------
  // Export downloads the server-assembled document as a file (same
  // Blob+anchor pattern the theme exporter uses). Import reads the file
  // the user picked, sends the parsed document for server-side
  // validation, then re-renders Settings from the fresh store — the
  // success line names the actual counts the server returned.
  const backupStatus = $('backup-status');
  const setBackupStatus = (text) => { if (backupStatus) backupStatus.textContent = text; };
  $('btn-backup-export').addEventListener('click', async () => {
    const btn = $('btn-backup-export');
    btn.disabled = true;
    try {
      const { backup } = await P().call(window.pcm.settingsExportBackup($('backup-include-keys').checked));
      const text = JSON.stringify(backup, null, 2);
      const blob = new Blob([text], { type: 'application/json' });
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = 'projectionist-settings-backup.json';
      a.click();
      setTimeout(() => URL.revokeObjectURL(a.href), 4000);
      setBackupStatus('Settings exported as projectionist-settings-backup.json.');
    } catch (e) { P().toastErr(e); setBackupStatus(''); }
    finally { btn.disabled = false; }
  });
  $('btn-backup-import').addEventListener('click', async () => {
    const file = $('backup-file').files && $('backup-file').files[0];
    if (!file) { setBackupStatus('Pick a backup file first.'); return; }
    const btn = $('btn-backup-import');
    btn.disabled = true;
    try {
      const text = await file.text();
      let doc;
      try { doc = JSON.parse(text); } catch { setBackupStatus('That file is not valid JSON — nothing was changed.'); return; }
      const r = await P().call(window.pcm.settingsImportBackup(doc, $('backup-import-keys').checked));
      const bits = [];
      if (r.settingsApplied) bits.push(`${r.settingsApplied} setting${r.settingsApplied === 1 ? '' : 's'}`);
      if (r.advisorApplied) bits.push('Advisor tuning');
      if (r.templatesImported) bits.push(`${r.templatesImported} template${r.templatesImported === 1 ? '' : 's'}`);
      if (r.leavingDates) bits.push(`${r.leavingDates} Leaving Soon date${r.leavingDates === 1 ? '' : 's'}`);
      if (r.keysImported) bits.push(`${r.keysImported} key${r.keysImported === 1 ? '' : 's'}`);
      let line = bits.length ? `Imported ${bits.join(', ')}.` : 'Import applied — nothing in the file changed this install.';
      if (r.keysSkipped) line += ' Keys in the file were left out: tick the import-keys box to include them.';
      if (r.dropped && r.dropped.length) line += ` Left out as machine-bound: ${r.dropped.join(', ')}.`;
      setBackupStatus(line);
      adv.meta = await P().call(window.pcm.metaStatus());
      renderSettings();
      P().toast('Settings imported.', 'success');
    } catch (e) { P().toastErr(e); }
    finally { btn.disabled = false; }
  });

  // ---- Donate button (v1.22 rider, toggle in v1.23) ------------------------
  // The username is the user's own Ko-fi identity, stored as a plain
  // portable setting, alongside the show/hide toggle (default ON).
  // Saving re-applies the widget immediately: toggle off removes it
  // outright (no script loads at all); with a username and the toggle
  // on it loads (or re-draws); without a username it disappears.
  const donateStatus = $('donate-status');
  $('btn-donate-save').addEventListener('click', async () => {
    const btn = $('btn-donate-save');
    btn.disabled = true;
    try {
      const enabled = $('donate-enabled') ? $('donate-enabled').checked : true;
      const r = await P().call(window.pcm.metaSetDonate($('donate-username').value.trim(), enabled));
      if (donateStatus) {
        donateStatus.textContent = !enabled ? 'Donate button turned off — no widget loads.'
          : r.username ? `Donate button saved — it appears when the Ko-fi widget loads for ${r.username}.`
          : 'Donate button removed (no username set).';
      }
      if (typeof refreshDonateWidget === 'function') refreshDonateWidget();
    } catch (e) { P().toastErr(e); }
    finally { btn.disabled = false; }
  });

  // refresh settings-dependent bits when returning from a scan
  refreshDraftsPill();
})();
