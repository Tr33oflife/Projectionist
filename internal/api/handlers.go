package api

// handlers.go — HTTP handlers mirroring Electron main.js's IPC handlers
// one-for-one. Every handler preserves the JS envelope contents exactly
// (same keys, same error strings) — the reused renderer depends on them.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"

	"plexcollectionmanager/internal/advisor"
	"plexcollectionmanager/internal/collectionart"
	"plexcollectionmanager/internal/overlays"
	"plexcollectionmanager/internal/plex"
)

// nextLoadCtx returns a fresh context for a collection-items load and
// cancels the previous load FOR THE SAME COLLECTION (Electron's
// nextLoadSignal, scoped per ratingKey since v1.20.1 — as a single
// global flight, an unrelated load anywhere in the app could cancel
// this one, and at least one caller read the cancelled answer as a
// genuinely empty collection). The renderer treats a cancelled load
// as a silent success with no items. The returned done func must be
// called when the load finishes; it releases the collection's slot.
func (s *Server) nextLoadCtx(ratingKey string) (context.Context, func()) {
	s.loadMu.Lock()
	if s.loadSlots == nil {
		s.loadSlots = map[string]*loadSlot{}
	}
	if prev, ok := s.loadSlots[ratingKey]; ok {
		prev.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	slot := &loadSlot{cancel: cancel}
	s.loadSlots[ratingKey] = slot
	s.loadMu.Unlock()
	done := func() {
		cancel()
		s.loadMu.Lock()
		if s.loadSlots[ratingKey] == slot {
			delete(s.loadSlots, ratingKey)
		}
		s.loadMu.Unlock()
	}
	return ctx, done
}

// ---- auth ---------------------------------------------------------------------

func (s *Server) handleStartLogin(w http.ResponseWriter, r *http.Request) {
	pin, err := s.plexClient.PinStart(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"pinId": pin.PinID, "authUrl": pin.AuthURL, "expiresIn": pin.ExpiresIn})
}

func (s *Server) handlePollLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PinID json.RawMessage `json:"pinId"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	token, err := s.plexClient.PinPoll(r.Context(), flexInt64(body.PinID))
	if err != nil {
		failErr(w, err)
		return
	}
	if token == "" {
		ok(w, envelope{"pending": true})
		return
	}
	user, err := s.plexClient.GetUser(r.Context(), token)
	if err != nil {
		failErr(w, err)
		return
	}
	if err := s.store.SetToken(token); err != nil {
		failErr(w, err)
		return
	}
	servers, err := s.plexClient.GetServers(r.Context(), token)
	if err != nil {
		failErr(w, err)
		return
	}
	selected := s.adoptSession(user, token, servers)
	ok(w, envelope{
		"pending": false, "user": user, "servers": s.publicServers(),
		"selectedMachine": selected, "storeBackend": s.store.Backend(),
	})
}

func (s *Server) handleRestoreSession(w http.ResponseWriter, r *http.Request) {
	token := s.store.GetToken()
	if token == "" {
		ok(w, envelope{"signedIn": false})
		return
	}
	// Any failure (dead token, servers unreachable) → signed-out state,
	// no crash — exactly the Electron behavior.
	user, err := s.plexClient.GetUser(r.Context(), token)
	if err != nil {
		ok(w, envelope{"signedIn": false})
		return
	}
	servers, err := s.plexClient.GetServers(r.Context(), token)
	if err != nil {
		ok(w, envelope{"signedIn": false})
		return
	}
	selected := s.adoptSession(user, token, servers)
	ok(w, envelope{
		"signedIn": true, "user": user, "servers": s.publicServers(),
		"selectedMachine": selected, "storeBackend": s.store.Backend(),
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.userToken = ""
	s.user = nil
	s.servers = nil
	s.selectedMachine = ""
	s.mu.Unlock()
	_ = s.store.ClearToken()
	ok(w, envelope{})
}

func (s *Server) handleSelectServer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MachineID string `json:"machineId"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	s.mu.Lock()
	found := false
	for _, srv := range s.servers {
		if srv.Machine == body.MachineID {
			found = true
			break
		}
	}
	if found {
		s.selectedMachine = body.MachineID
	}
	s.mu.Unlock()
	if !found {
		failErr(w, &plex.Error{Message: "Server not accessible to this Plex account"})
		return
	}
	_ = s.store.SetSettings(map[string]any{"defaultMachine": body.MachineID})
	ok(w, envelope{"selectedMachine": body.MachineID})
}

// ---- Plex reads -----------------------------------------------------------------

func (s *Server) handleGetSections(w http.ResponseWriter, r *http.Request) {
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	sections, err := s.plexClient.GetSections(r.Context(), server)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"sections": sections})
}

// handleGetSectionItems enumerates a whole section (optionally
// filtered), paged — the Apply view's library/tag scopes. Distinct
// from previewFilter, which caps at one 100-item page for previews.
func (s *Server) handleGetSectionItems(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SectionKey  string          `json:"sectionKey"`
		FilterQuery string          `json:"filterQuery"`
		Type        json.RawMessage `json:"type"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	items, err := s.plexClient.GetSectionItemsFiltered(r.Context(), server, body.SectionKey, body.FilterQuery, flexIntPtr(body.Type))
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"items": items})
}

func (s *Server) handleGetSectionTagTypes(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SectionKey string `json:"sectionKey"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	tagTypes, err := s.plexClient.ListSectionTagTypes(r.Context(), server, body.SectionKey)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"tagTypes": tagTypes})
}

func (s *Server) handleGetSectionTags(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SectionKey string `json:"sectionKey"`
		TagType    string `json:"tagType"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	tags, err := s.plexClient.ListSectionTagValues(r.Context(), server, body.SectionKey, body.TagType)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"tags": tags})
}

func (s *Server) handleGetCollections(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SectionKey string `json:"sectionKey"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	collections, err := s.plexClient.GetCollections(r.Context(), server, body.SectionKey)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"collections": collections})
}

func (s *Server) handleGetCollectionItems(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RatingKey string `json:"ratingKey"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	loadCtx, doneLoad := s.nextLoadCtx(body.RatingKey)
	defer doneLoad()
	items, err := s.plexClient.GetCollectionItems(loadCtx, server, body.RatingKey)
	if err != nil {
		if isCancelled(err) {
			ok(w, envelope{"cancelled": true, "items": []any{}})
			return
		}
		failErr(w, err)
		return
	}
	ok(w, envelope{"items": items})
}

func (s *Server) handleGetItem(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RatingKey string `json:"ratingKey"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	item, err := s.plexClient.GetItem(r.Context(), server, body.RatingKey)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"item": item})
}

// handleGetItemChildren lists an item's children (seasons of a show)
// for the Overlay Studio drill-down. Failure is a transport error on
// purpose: the browser renders it inline in the grid it belongs to.
func (s *Server) handleGetItemChildren(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RatingKey string `json:"ratingKey"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	items, err := s.plexClient.GetChildrenItems(r.Context(), server, body.RatingKey)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"items": items})
}

func (s *Server) handleSearchLibrary(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SectionKey string          `json:"sectionKey"`
		Query      string          `json:"query"`
		Type       json.RawMessage `json:"type"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	items, err := s.plexClient.SearchLibrary(r.Context(), server, body.SectionKey, body.Query, flexIntPtr(body.Type))
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"items": items})
}

// handleGetSectionItemsPage serves ONE page of a section listing for
// the Builder wizard's poster grid (v1.17) — read-only. The envelope
// carries the page plus the server's own total when it reports one
// (totalKnown), so the grid's loaded/total line is honest.
func (s *Server) handleGetSectionItemsPage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SectionKey  string          `json:"sectionKey"`
		FilterQuery string          `json:"filterQuery"`
		Type        json.RawMessage `json:"type"`
		Title       string          `json:"title"`
		Start       int             `json:"start"`
		Size        int             `json:"size"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	page, err := s.plexClient.GetSectionItemsPage(r.Context(), server, body.SectionKey, body.FilterQuery, flexIntPtr(body.Type), body.Title, body.Start, body.Size)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"items": page.Items, "total": page.Total, "totalKnown": page.TotalKnown, "start": page.Start, "size": page.Size, "hasMore": page.HasMore})
}

func (s *Server) handlePreviewFilter(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SectionKey  string          `json:"sectionKey"`
		FilterQuery string          `json:"filterQuery"`
		Type        json.RawMessage `json:"type"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	items, err := s.plexClient.PreviewFilter(r.Context(), server, body.SectionKey, body.FilterQuery, flexIntPtr(body.Type))
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"items": items})
}

// ---- Plex writes ------------------------------------------------------------------
// Every write below is preceded in the UI by a dry-run diff (renderer
// buildDryRun) and guarded server-side by internal/plex's write policy.
// Nothing here adds a path around either.

func (s *Server) handleCreateCollection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SectionKey     string          `json:"sectionKey"`
		Type           json.RawMessage `json:"type"`
		Title          string          `json:"title"`
		Smart          bool            `json:"smart"`
		ItemRatingKeys []string        `json:"itemRatingKeys"`
		FilterQuery    string          `json:"filterQuery"`
		TitleSort      string          `json:"titleSort"`
		Summary        string          `json:"summary"`
		ContentRating  string          `json:"contentRating"`
		VisHome        *bool           `json:"visHome"`
		VisShared      *bool           `json:"visShared"`
		VisRecommended *bool           `json:"visRecommended"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	typ := 0
	if p := flexIntPtr(body.Type); p != nil {
		typ = *p
	}
	created, err := s.plexClient.CreateCollection(r.Context(), server, plex.CreatePayload{
		SectionKey: body.SectionKey, Type: typ, Title: body.Title, Smart: body.Smart,
		ItemRatingKeys: body.ItemRatingKeys, FilterQuery: body.FilterQuery,
		TitleSort: body.TitleSort, Summary: body.Summary, ContentRating: body.ContentRating,
		VisHome: body.VisHome, VisShared: body.VisShared, VisRecommended: body.VisRecommended,
	})
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"collection": created})
}

func (s *Server) handleUpdateCollection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RatingKey string                     `json:"ratingKey"`
		Fields    map[string]json.RawMessage `json:"fields"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	var fields plex.MetaFields
	strField := func(key string) *string {
		raw, present := body.Fields[key]
		if !present || string(raw) == "null" {
			return nil
		}
		v := flexString(raw)
		return &v
	}
	boolField := func(key string) *bool {
		raw, present := body.Fields[key]
		if !present || string(raw) == "null" {
			return nil
		}
		var v bool
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil
		}
		return &v
	}
	fields.Title = strField("title")
	fields.TitleSort = strField("titleSort")
	fields.Summary = strField("summary")
	fields.ContentRating = strField("contentRating")
	fields.VisHome = boolField("visHome")
	fields.VisShared = boolField("visShared")
	fields.VisRecommended = boolField("visRecommended")
	if err := s.plexClient.UpdateCollectionMeta(r.Context(), server, body.RatingKey, fields); err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{})
}

func (s *Server) handleSetCollectionItems(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RatingKey  string   `json:"ratingKey"`
		RatingKeys []string `json:"ratingKeys"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	if err := s.plexClient.SetCollectionItems(r.Context(), server, body.RatingKey, body.RatingKeys); err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{})
}

func (s *Server) handleDeleteCollection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RatingKey string `json:"ratingKey"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	// The client ALWAYS returns the safety-guard error here — deletion is
	// disabled in this app (AGENTS.md). The route exists so the bridge
	// surface stays complete and the refusal is explicit, not a 404.
	if err := s.plexClient.DeleteCollection(r.Context(), server, body.RatingKey); err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{})
}

func (s *Server) handleSetPoster(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RatingKey string  `json:"ratingKey"`
		URL       *string `json:"url"`
		AssetID   *string `json:"assetId"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	item, err := s.plexClient.GetItem(r.Context(), server, body.RatingKey)
	if err != nil {
		failErr(w, err)
		return
	}
	if body.AssetID == nil || *body.AssetID == "" {
		// Electron behavior: a bare URL is NOT applied — the poster must
		// come through the art picker's preview/import so it is validated
		// and backed up like every other poster write.
		failMsg(w, "Preview or import the poster in the art picker before applying.", "")
		return
	}
	asset, found := s.art.GetAsset(*body.AssetID)
	if !found {
		failMsg(w, "That previewed poster is no longer cached.", "Preview or import it again in the art picker.")
		return
	}
	badgeSet, _ := json.Marshal(map[string]any{"name": "Collection art: " + asset.Source, "placements": []any{}})
	target, _ := json.Marshal(map[string]any{"type": "collection-art", "source": asset.Source, "sourceUrl": asset.URL})
	record, err := s.overlay.RunApplyJob(r.Context(), overlays.ApplyRequest{
		Server: server,
		Jobs: []overlays.Job{{
			RatingKey: body.RatingKey, Title: item.Title, Thumb: item.Thumb,
			PNGBase64: base64.StdEncoding.EncodeToString(asset.Bytes),
		}},
		BadgeSet: badgeSet,
		Target:   target,
	})
	if err != nil {
		failErr(w, err)
		return
	}
	if record.FailCount > 0 && len(record.Items) > 0 {
		failMsg(w, record.Items[0].Error, "")
		return
	}
	s.invalidateArt(server.Machine, item.Thumb)
	ok(w, envelope{})
}

// ---- collection art -----------------------------------------------------------------

func (s *Server) handleArtSuggestions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	res := s.art.Suggestions(truncStr(body.Title, 200))
	out := envelope{}
	for k, v := range res {
		out[k] = v
	}
	ok(w, out)
}

func (s *Server) handleArtPreview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Input json.RawMessage `json:"input"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	// The renderer passes the raw link string; the collectionart port
	// accepts the input map form.
	art, err := s.art.Preview(r.Context(), map[string]any{"input": flexString(body.Input)})
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"art": art})
}

func (s *Server) handleArtRemember(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title   string `json:"title"`
		AssetID string `json:"assetId"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	if err := s.art.Remember(truncStr(body.Title, 200), body.AssetID); err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{})
}

func (s *Server) handleArtOpenTPDb(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	// Electron opened the system browser from main; the Go build returns
	// the URL and bridge.js opens it in a new tab (same user experience).
	ok(w, envelope{"url": collectionart.TPDBSearchURL(body.Title)})
}

// ---- advisor -------------------------------------------------------------------------

func (s *Server) handleAdvisorScan(w http.ResponseWriter, r *http.Request) {
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	settings, _ := s.advisorStore.GetSettings()
	sec := s.getSecrets()
	scan, err := advisor.ScanLibrary(r.Context(), advisor.ScanDeps{
		Plex: s.plexClient, Server: server, Store: s.advisorStore,
		Settings: settings, TMDBKey: sec.tmdb, OMDbKey: sec.omdb,
		OnProgress: func(p advisor.Progress) { s.events.publish("scan-progress", p) },
	})
	if err != nil {
		failErr(w, err)
		return
	}
	s.mu.Lock()
	s.scanCache[server.Machine] = scan
	s.mu.Unlock()
	ok(w, envelope{"scan": slimScan(scan, settings)})
}

func (s *Server) handleAdvisorGetScan(w http.ResponseWriter, r *http.Request) {
	scan, err := s.getFullScan()
	if err != nil {
		failErr(w, err)
		return
	}
	if scan == nil {
		ok(w, envelope{"scan": nil})
		return
	}
	settings, _ := s.advisorStore.GetSettings()
	ok(w, envelope{"scan": slimScan(scan, settings)})
}

func (s *Server) handleAdvisorSuggestions(w http.ResponseWriter, r *http.Request) {
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	scan, err := s.getFullScan()
	if err != nil {
		failErr(w, err)
		return
	}
	if scan == nil {
		failErr(w, &plex.Error{
			Message: "No library scan yet",
			Hint:    "Run “Scan library” on the Advisor tab first.",
		})
		return
	}
	settings, _ := s.advisorStore.GetSettings()
	sec := s.getSecrets()
	mc := s.buildMetaClients(sec)
	cards, topLists, err := advisor.GenerateAll(r.Context(), scan, advisor.GenContext{
		Plex: s.plexClient, Server: server, Store: s.advisorStore, Settings: settings,
		TMDB: mc.tmdb, Trakt: mc.trakt, TraktToken: s.getTraktToken(r.Context(), sec),
		AniList: mc.anilist,
	})
	if err != nil {
		failErr(w, err)
		return
	}
	valid := make([]advisor.Card, 0, len(cards))
	for _, c := range cards {
		if advisor.IsValidCard(c) { // never let a malformed card reach the UI
			valid = append(valid, c)
		}
	}
	// v1.25: bundle-kind cards are suppressed when a near-duplicate
	// collection already exists (Jaccard >= 0.8, the AI mechanism).
	// A failed collections fetch degrades to NO dedupe rather than
	// failing the whole suggestions view — unlike the AI endpoint,
	// this is the Advisor's main list, and a collections hiccup must
	// not cost the owner every suggestion. One collection's members
	// failing degrades that collection to title-only, as in AI.
	existing := make([]advisor.ExistingCollection, 0)
	for _, section := range scan.Sections {
		colls, cerr := s.plexClient.GetCollections(r.Context(), server, section.Key)
		if cerr != nil {
			continue
		}
		for _, c := range colls {
			ec := advisor.ExistingCollection{Title: c.Title}
			if items, ierr := s.plexClient.GetCollectionItems(r.Context(), server, c.RatingKey); ierr == nil {
				for _, it := range items {
					ec.Keys = append(ec.Keys, it.RatingKey)
				}
			}
			existing = append(existing, ec)
		}
	}
	valid = advisor.DedupeCovered(valid, existing)
	ok(w, envelope{"cards": valid, "topLists": topLists})
}

func (s *Server) handleAdvisorDismiss(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CardID string `json:"cardId"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	if err := s.advisorStore.Dismiss(body.CardID); err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{})
}

func (s *Server) handleAdvisorUndismiss(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CardID string `json:"cardId"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	if err := s.advisorStore.Undismiss(body.CardID); err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{})
}

func (s *Server) handleAdvisorResetDismissals(w http.ResponseWriter, r *http.Request) {
	if err := s.advisorStore.ResetDismissals(); err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{})
}

func (s *Server) handleAdvisorGetDrafts(w http.ResponseWriter, r *http.Request) {
	drafts, err := s.advisorStore.GetDrafts()
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"drafts": drafts})
}

func (s *Server) handleAdvisorSaveDraft(w http.ResponseWriter, r *http.Request) {
	// Same sanitization main.js applied before the store saw the draft.
	var body struct {
		Title            string            `json:"title"`
		MemberRatingKeys []json.RawMessage `json:"memberRatingKeys"`
		SectionKey       json.RawMessage   `json:"sectionKey"`
		Type             json.RawMessage   `json:"type"`
		Reason           string            `json:"reason"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	title := body.Title
	if title == "" {
		title = "Untitled draft"
	}
	keys := make([]string, 0, len(body.MemberRatingKeys))
	for _, raw := range body.MemberRatingKeys {
		if v := flexString(raw); v != "" {
			keys = append(keys, v)
		}
	}
	typ := flexString(body.Type)
	if typ == "" {
		typ = "1"
	}
	saved, err := s.advisorStore.SaveDraft(advisor.Draft{
		Title:            truncStr(title, 120),
		MemberRatingKeys: keys,
		SectionKey:       flexString(body.SectionKey),
		Type:             typ,
		Reason:           truncStr(body.Reason, 300),
	})
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"draft": saved})
}

func (s *Server) handleAdvisorDeleteDraft(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID json.RawMessage `json:"id"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	if err := s.advisorStore.DeleteDraft(flexString(body.ID)); err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{})
}

func (s *Server) handleAdvisorDubVerdict(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RatingKey string `json:"ratingKey"`
		Force     bool   `json:"force"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	scan, err := s.getFullScan()
	if err != nil {
		failErr(w, err)
		return
	}
	var item *advisor.Item
	if scan != nil {
		for i := range scan.Items {
			if scan.Items[i].RatingKey == body.RatingKey {
				item = &scan.Items[i]
				break
			}
		}
	}
	if item == nil {
		failErr(w, &plex.Error{
			Message: "Title not in the last scan",
			Hint:    "Rescan the library first.",
		})
		return
	}
	settings, _ := s.advisorStore.GetSettings()
	verdict, err := advisor.ScanTitle(r.Context(), s.plexClient, server, *item, s.advisorStore, settings, body.Force)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"verdict": verdict})
}

func (s *Server) handleAdvisorGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.advisorStore.GetSettings()
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"settings": settings})
}

func (s *Server) handleAdvisorSetSettings(w http.ResponseWriter, r *http.Request) {
	var patch map[string]any
	if err := decodeBody(r, &patch); err != nil {
		failErr(w, err)
		return
	}
	settings, err := s.advisorStore.SetSettings(patch)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"settings": settings})
}

func (s *Server) handleAdvisorClearCache(w http.ResponseWriter, r *http.Request) {
	if err := s.advisorStore.ClearCache(); err != nil {
		failErr(w, err)
		return
	}
	s.mu.Lock()
	s.scanCache = map[string]*advisor.Scan{}
	s.mu.Unlock()
	stats, err := s.advisorStore.CacheSize()
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"bytes": stats.Bytes, "files": stats.Files})
}

func (s *Server) handleAdvisorCacheStatus(w http.ResponseWriter, r *http.Request) {
	stats, err := s.advisorStore.CacheSize()
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"bytes": stats.Bytes, "files": stats.Files})
}
