// Package api is the HTTP layer of the Go build: it serves the embedded
// web UI and implements one REST endpoint per window.pcm method (see
// web/bridge.js), translating them into the same handler logic the
// Electron app ran in src/main.js.
//
// FUTURE AI — the contract that must not drift:
//   - Every /api/* handler answers the Electron envelope: HTTP 200 with
//     { ok:true, ... } on success or { ok:false, error, hint } on failure.
//     The renderer's call() helper depends on it; transport-level errors
//     (bad token, bad JSON) also use the envelope so the UI can show them.
//   - Errors surface honestly: Plex/API error messages and hints pass
//     through verbatim (plex.Error carries both). Never swallow, never
//     invent friendlier text that hides the real status/body.
//   - Secrets NEVER leave this process: meta/status returns booleans,
//     exactly like the Electron meta:status handler.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sync"

	"plexcollectionmanager/internal/advisor"
	"plexcollectionmanager/internal/collectionart"
	"plexcollectionmanager/internal/overlays"
	"plexcollectionmanager/internal/plex"
	"plexcollectionmanager/internal/store"
)

// Config wires main.go's resources into the server.
type Config struct {
	DataDir      string
	Version      string
	SessionToken string
	WebFS        fs.FS // embedded web/ (index.html, *.js, styles.css)
	BadgesFS     fs.FS // embedded assets/badges (badges.json + PNGs)
	CatalogJSON  []byte
}

// loadSlot is one in-flight collection-items load. Pointer identity
// lets a finished load remove only its own slot from Server.loadSlots
// (a superseding load for the same collection has already replaced
// it and must not be evicted by the stale load's cleanup).
type loadSlot struct{ cancel context.CancelFunc }

// Server holds the app's session state — the direct equivalent of the
// module-level state in Electron's main.js (userToken, servers,
// selectedMachine, scanCache). Guarded by mu where handlers race.
type Server struct {
	cfg   Config
	store *store.Store
	mux   *http.ServeMux

	// services (built by initServices in services.go)
	plexClient    *plex.Client
	advisorStore  *advisor.Store
	overlay       *overlays.Applier
	templates     *overlays.TemplateStore
	art           *collectionart.Service
	badgeManifest json.RawMessage // assets/badges/badges.json, served verbatim

	// bulkArtAsset overrides the bulk-art title→asset resolution in
	// tests; nil in production (handlers fall back to bestBulkAsset,
	// the real TPDb suggestions+preview flow).
	bulkArtAsset func(ctx context.Context, title string) (collectionart.Asset, error)

	// fetchTMDbList / fetchTraktList override the v1.14 list-import
	// fetches in tests; nil in production (handlers use the real
	// meta clients built from the stored keys).
	fetchTMDbList  func(ctx context.Context, ref string) (fetchedList, error)
	fetchTraktList func(ctx context.Context, ref string) (fetchedList, error)

	mu              sync.Mutex
	userToken       string
	user            json.RawMessage // plex user object, passed through verbatim
	servers         []*plex.Server
	selectedMachine string
	scanCache       map[string]*advisor.Scan

	// loadMu + loadSlots implement the load-cancellation: a new
	// getCollectionItems call cancels the previous one FOR THE SAME
	// COLLECTION ONLY (v0.4.4 semantics, scoped per ratingKey in
	// v1.20.1 — as a single global flight, any overlapping load
	// anywhere in the app could cancel any other, and a cancelled
	// load answers { ok:true, cancelled:true } instead of an error.
	loadMu    sync.Mutex
	loadSlots map[string]*loadSlot

	// art proxy state (v0.4.4): winner memory + bounded LRU, see art.go.
	artMu       sync.Mutex
	goodArtConn map[string]string
	artCache    *artLRU

	// poster generator preview caches (v1.21.1), see postergen_cache.go.
	genImages  *previewImageCache
	genMembers *previewMembersCache

	events *eventHub
}

// NewServer builds the store, Plex client and all package services, and
// registers every route. It does NOT listen; main.go owns the listener.
func NewServer(cfg Config) (*Server, error) {
	s := &Server{
		cfg:         cfg,
		store:       store.New(cfg.DataDir),
		mux:         http.NewServeMux(),
		scanCache:   map[string]*advisor.Scan{},
		loadSlots:   map[string]*loadSlot{},
		goodArtConn: map[string]string{},
		artCache:    newArtLRU(400),
		genImages:   newPreviewImageCache(),
		genMembers:  newPreviewMembersCache(),
		events:      newEventHub(),
	}
	if err := s.initServices(); err != nil {
		return nil, err
	}
	s.routes()
	return s, nil
}

// Handler returns the root http.Handler (token guard applied to /api/*).
func (s *Server) Handler() http.Handler { return s.mux }

// ---- envelope helpers -------------------------------------------------------

type envelope map[string]any

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

func ok(w http.ResponseWriter, data envelope) {
	if data == nil {
		data = envelope{}
	}
	data["ok"] = true
	writeJSON(w, http.StatusOK, data)
}

// failErr mirrors Electron's fail(): plex.Error contributes message+hint,
// anything else contributes its message with an empty hint.
func failErr(w http.ResponseWriter, err error) {
	msg, hint := errorParts(err)
	log.Printf("[api] %s", msg)
	writeJSON(w, http.StatusOK, envelope{"ok": false, "error": msg, "hint": hint})
}

func failMsg(w http.ResponseWriter, msg, hint string) {
	writeJSON(w, http.StatusOK, envelope{"ok": false, "error": msg, "hint": hint})
}

// decodeBody reads a JSON request body into v. Bodies are small (the
// largest legit payload is overlay apply jobs with base64 PNGs — capped
// generously at 64MB, matching poster sizes the Electron app handled).
func decodeBody(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<20))
	return dec.Decode(v)
}

// ---- auth guard ---------------------------------------------------------------

// requireToken enforces the per-launch session token on /api/* routes.
// The header is the normal path; ?t= is accepted for EventSource and
// <img> URLs, which cannot set headers.
func (s *Server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-PCM-Token")
		if got == "" {
			got = r.URL.Query().Get("t")
		}
		if got != s.cfg.SessionToken {
			writeJSON(w, http.StatusForbidden, envelope{
				"ok": false, "error": "Invalid session token", "hint": "Reload the page from the URL the app printed at launch.",
			})
			return
		}
		next(w, r)
	}
}

// ---- routes -------------------------------------------------------------------

func (s *Server) routes() {
	// Session bootstrap + health are served without the token (the UI
	// needs /session.js to learn the token in the first place; health is
	// a plain liveness probe with no data in it).
	s.mux.HandleFunc("GET /session.js", s.handleSessionJS)
	s.mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, envelope{"ok": true, "version": s.cfg.Version, "app": "plex-collection-manager"})
	})

	// Token-guarded API surface — one route per window.pcm method.
	api := s.requireToken
	s.mux.HandleFunc("POST /api/auth/startLogin", api(s.handleStartLogin))
	s.mux.HandleFunc("POST /api/auth/pollLogin", api(s.handlePollLogin))
	s.mux.HandleFunc("POST /api/auth/restoreSession", api(s.handleRestoreSession))
	s.mux.HandleFunc("POST /api/auth/logout", api(s.handleLogout))
	s.mux.HandleFunc("POST /api/auth/selectServer", api(s.handleSelectServer))

	s.mux.HandleFunc("POST /api/plex/getSections", api(s.handleGetSections))
	s.mux.HandleFunc("POST /api/plex/getSectionItems", api(s.handleGetSectionItems))
	s.mux.HandleFunc("POST /api/plex/getSectionItemsPage", api(s.handleGetSectionItemsPage))
	s.mux.HandleFunc("POST /api/plex/getSectionTagTypes", api(s.handleGetSectionTagTypes))
	s.mux.HandleFunc("POST /api/plex/getSectionTags", api(s.handleGetSectionTags))
	s.mux.HandleFunc("POST /api/plex/getCollections", api(s.handleGetCollections))
	s.mux.HandleFunc("POST /api/plex/getCollectionItems", api(s.handleGetCollectionItems))
	s.mux.HandleFunc("POST /api/plex/getItem", api(s.handleGetItem))
	s.mux.HandleFunc("POST /api/plex/getItemChildren", api(s.handleGetItemChildren))
	s.mux.HandleFunc("POST /api/plex/searchLibrary", api(s.handleSearchLibrary))
	s.mux.HandleFunc("POST /api/plex/previewFilter", api(s.handlePreviewFilter))
	s.mux.HandleFunc("POST /api/plex/createCollection", api(s.handleCreateCollection))
	s.mux.HandleFunc("POST /api/plex/updateCollection", api(s.handleUpdateCollection))
	s.mux.HandleFunc("POST /api/plex/setCollectionItems", api(s.handleSetCollectionItems))
	s.mux.HandleFunc("POST /api/plex/deleteCollection", api(s.handleDeleteCollection))
	s.mux.HandleFunc("POST /api/plex/setPoster", api(s.handleSetPoster))
	s.mux.HandleFunc("POST /api/plex/collectionHealth", api(s.handleCollectionHealth))
	s.mux.HandleFunc("POST /api/plex/getCollectionDetail", api(s.handleGetCollectionDetail))
	s.mux.HandleFunc("POST /api/plex/updateCollectionMeta", api(s.handleUpdateCollectionMeta))
	s.mux.HandleFunc("POST /api/plex/collectionBulkArt", api(s.handleCollectionBulkArt))
	s.mux.HandleFunc("POST /api/plex/setCollectionPosterURL", api(s.handleSetCollectionPosterURL))
	s.mux.HandleFunc("POST /api/import/previewList", api(s.handleImportPreviewList))
	s.mux.HandleFunc("POST /api/postergen/preview", api(s.handlePosterGenPreview))
	s.mux.HandleFunc("POST /api/postergen/aiImage", api(s.handlePosterGenAIImage))

	s.mux.HandleFunc("POST /api/art/suggestions", api(s.handleArtSuggestions))
	s.mux.HandleFunc("POST /api/art/preview", api(s.handleArtPreview))
	s.mux.HandleFunc("POST /api/art/importImage", api(s.handleArtImportImage))
	s.mux.HandleFunc("POST /api/art/remember", api(s.handleArtRemember))
	s.mux.HandleFunc("POST /api/art/openTPDb", api(s.handleArtOpenTPDb))

	s.mux.HandleFunc("POST /api/advisor/scan", api(s.handleAdvisorScan))
	s.mux.HandleFunc("POST /api/advisor/getScan", api(s.handleAdvisorGetScan))
	s.mux.HandleFunc("POST /api/advisor/suggestions", api(s.handleAdvisorSuggestions))
	s.mux.HandleFunc("POST /api/advisor/dismiss", api(s.handleAdvisorDismiss))
	s.mux.HandleFunc("POST /api/advisor/undismiss", api(s.handleAdvisorUndismiss))
	s.mux.HandleFunc("POST /api/advisor/resetDismissals", api(s.handleAdvisorResetDismissals))
	s.mux.HandleFunc("POST /api/advisor/getDrafts", api(s.handleAdvisorGetDrafts))
	s.mux.HandleFunc("POST /api/advisor/saveDraft", api(s.handleAdvisorSaveDraft))
	s.mux.HandleFunc("POST /api/advisor/deleteDraft", api(s.handleAdvisorDeleteDraft))
	s.mux.HandleFunc("POST /api/advisor/dubVerdict", api(s.handleAdvisorDubVerdict))
	s.mux.HandleFunc("POST /api/advisor/getSettings", api(s.handleAdvisorGetSettings))
	s.mux.HandleFunc("POST /api/advisor/setSettings", api(s.handleAdvisorSetSettings))
	s.mux.HandleFunc("POST /api/advisor/clearCache", api(s.handleAdvisorClearCache))
	s.mux.HandleFunc("POST /api/advisor/cacheStatus", api(s.handleAdvisorCacheStatus))

	s.mux.HandleFunc("POST /api/meta/status", api(s.handleMetaStatus))
	s.mux.HandleFunc("POST /api/meta/setKey", api(s.handleMetaSetKey))
	s.mux.HandleFunc("POST /api/meta/setAiBaseUrl", api(s.handleMetaSetAiBaseURL))
	s.mux.HandleFunc("POST /api/meta/setAiModel", api(s.handleMetaSetAiModel))
	s.mux.HandleFunc("POST /api/meta/setDonate", api(s.handleMetaSetDonate))
	s.mux.HandleFunc("POST /api/settings/exportBackup", api(s.handleSettingsExportBackup))
	s.mux.HandleFunc("POST /api/settings/importBackup", api(s.handleSettingsImportBackup))
	s.mux.HandleFunc("POST /api/meta/traktOAuthStart", api(s.handleTraktOAuthStart))
	s.mux.HandleFunc("POST /api/meta/traktOAuthFinish", api(s.handleTraktOAuthFinish))
	s.mux.HandleFunc("POST /api/meta/traktDisconnect", api(s.handleTraktDisconnect))

	s.mux.HandleFunc("POST /api/ai/proposeRules", api(s.handleAIProposeRules))
	s.mux.HandleFunc("POST /api/ai/suggestCollections", api(s.handleAISuggestCollections))
	s.mux.HandleFunc("POST /api/ai/proposeFranchiseMerge", api(s.handleAIProposeFranchiseMerge))
	s.mux.HandleFunc("POST /api/ai/proposeRibbon", api(s.handleAIProposeRibbon))
	s.mux.HandleFunc("POST /api/ai/testConnection", api(s.handleAITestConnection))

	s.mux.HandleFunc("POST /api/overlay/badgePacks", api(s.handleOverlayBadgePacks))
	s.mux.HandleFunc("POST /api/overlay/fanartLogos", api(s.handleOverlayFanartLogos))
	s.mux.HandleFunc("POST /api/overlay/itemFacts", api(s.handleOverlayItemFacts))
	s.mux.HandleFunc("POST /api/overlay/apply", api(s.handleOverlayApply))
	s.mux.HandleFunc("POST /api/overlay/classifyPoster", api(s.handleOverlayClassifyPoster))
	s.mux.HandleFunc("POST /api/overlay/restoreDefaultPoster", api(s.handleOverlayRestoreDefaultPoster))
	s.mux.HandleFunc("POST /api/overlay/restore", api(s.handleOverlayRestore))
	s.mux.HandleFunc("POST /api/overlay/listBackups", api(s.handleOverlayListBackups))
	s.mux.HandleFunc("POST /api/overlay/restoreBackups", api(s.handleOverlayRestoreBackups))
	s.mux.HandleFunc("POST /api/overlay/history", api(s.handleOverlayHistory))
	s.mux.HandleFunc("POST /api/overlay/reapplyCheck", api(s.handleOverlayReapplyCheck))
	s.mux.HandleFunc("POST /api/overlay/templatesList", api(s.handleOverlayTemplatesList))
	s.mux.HandleFunc("POST /api/overlay/templateSave", api(s.handleOverlayTemplateSave))
	s.mux.HandleFunc("POST /api/overlay/templateLoad", api(s.handleOverlayTemplateLoad))
	s.mux.HandleFunc("POST /api/overlay/templateDelete", api(s.handleOverlayTemplateDelete))

	s.mux.HandleFunc("POST /api/theme/get", api(s.handleThemeGet))
	s.mux.HandleFunc("POST /api/theme/set", api(s.handleThemeSet))

	s.mux.HandleFunc("POST /api/leaving/list", api(s.handleLeavingList))
	s.mux.HandleFunc("POST /api/leaving/set", api(s.handleLeavingSet))
	s.mux.HandleFunc("POST /api/leaving/remove", api(s.handleLeavingRemove))

	s.mux.HandleFunc("GET /api/events", api(s.handleEvents))
	s.mux.HandleFunc("GET /art", api(s.handleArt))
	s.mux.HandleFunc("GET /badges/bundle/{file}", api(s.handleBadgeFile("bundle")))
	s.mux.HandleFunc("GET /badges/custom/{file}", api(s.handleBadgeFile("custom")))

	// Embedded web UI (index.html at /, static files beside it).
	s.mux.Handle("GET /", http.FileServerFS(s.cfg.WebFS))
}

// handleSessionJS hands the UI its session token. Served only on
// loopback like everything else; the token still gates the API itself.
func (s *Server) handleSessionJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, "window.__PCM_TOKEN__ = %q;\n", s.cfg.SessionToken)
}

// ---- SSE event hub --------------------------------------------------------------

// eventHub fans progress events out to connected browsers. Two kinds:
// 'scan-progress' (advisor scans) and 'overlay-progress' (poster applies)
// — the same two channels Electron pushed over webContents.send.
type eventHub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func newEventHub() *eventHub { return &eventHub{subs: map[chan []byte]struct{}{}} }

func (h *eventHub) publish(kind string, payload any) {
	raw, err := json.Marshal(map[string]any{"kind": kind, "payload": payload})
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- raw:
		default: // slow consumer: drop the event, never block a scan/apply
		}
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ch := make(chan []byte, 32)
	s.events.mu.Lock()
	s.events.subs[ch] = struct{}{}
	s.events.mu.Unlock()
	defer func() {
		s.events.mu.Lock()
		delete(s.events.subs, ch)
		s.events.mu.Unlock()
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			fl.Flush()
		}
	}
}
