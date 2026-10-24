package api

// handlers_meta.go — metadata keys, Trakt OAuth, AI plug points, and the
// Overlay Studio handlers. Mirrors the corresponding Electron main.js
// IPC handlers one-for-one (same envelopes, same error strings).

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"plexcollectionmanager/internal/meta"
	"plexcollectionmanager/internal/overlays"
	"plexcollectionmanager/internal/plex"
	"plexcollectionmanager/internal/store"
)

// ---- metadata keys -----------------------------------------------------------

// handleMetaStatus returns BOOLEANS ONLY — key values never cross to the
// renderer (Electron rule, kept).
func (s *Server) handleMetaStatus(w http.ResponseWriter, r *http.Request) {
	sec := s.getSecrets()
	base := s.store.SettingString("aiBaseUrl")
	if base == "" {
		base = meta.DefaultBaseURL
	}
	model := s.store.SettingString("aiModel")
	if model == "" {
		model = meta.DefaultModel
	}
	ok(w, envelope{
		"tmdb":              sec.tmdb != "",
		"traktClientId":     sec.traktClientID != "",
		"traktClientSecret": sec.traktClientSecret != "",
		"traktConnected":    s.getTraktToken(r.Context(), sec) != "",
		"omdb":              sec.omdb != "",
		"fanart":            sec.fanart != "",
		"ai":                sec.aiKey != "",
		"aiBaseUrl":         base,
		"aiModel":           model,
		"donateUsername":    s.store.SettingString("donateUsername"),
		"donateEnabled":     donateEnabledSetting(s.store),
		"storeBackend":      s.store.Backend(),
	})
}

func (s *Server) handleMetaSetKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Which string `json:"which"`
		Value string `json:"value"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	name, known := secretFor[body.Which]
	if !known {
		failErr(w, &plex.Error{Message: "Unknown key slot: " + body.Which})
		return
	}
	if err := s.store.SetSecret(name, body.Value); err != nil { // empty value clears
		failErr(w, err)
		return
	}
	if body.Which == "traktClientId" || body.Which == "traktClientSecret" {
		_ = s.store.ClearSecret("trakt:oauth") // credentials changed → re-OAuth
	}
	ok(w, envelope{"set": body.Value != ""})
}

func (s *Server) handleMetaSetAiBaseURL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	// JS: String(url || '').replace(/\/$/, '') || DEFAULT_BASE_URL
	v := strings.TrimSuffix(body.URL, "/")
	if v == "" {
		v = meta.DefaultBaseURL
	}
	if err := s.store.SetSettings(map[string]any{"aiBaseUrl": v}); err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{})
}

func (s *Server) handleMetaSetAiModel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model string `json:"model"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	v := strings.TrimSpace(body.Model)
	if v == "" {
		v = meta.DefaultModel
	}
	if err := s.store.SetSettings(map[string]any{"aiModel": v}); err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{})
}

// ---- Trakt OAuth (OUT-OF-BAND) -------------------------------------------------
// Step 1 returns the authorize URL; bridge.js opens it in a new tab (the
// Electron main process used shell.openExternal). The user pastes the
// code Trakt shows; step 2 exchanges it. The redirect URI is the OOB
// constant inside internal/meta — do NOT reintroduce a localhost flow.

func (s *Server) handleTraktOAuthStart(w http.ResponseWriter, r *http.Request) {
	sec := s.getSecrets()
	if sec.traktClientID == "" {
		failErr(w, &plex.Error{
			Message: "Trakt client ID required first",
			Hint:    "Paste it in Settings → API keys, then connect.",
		})
		return
	}
	client := meta.NewTrakt(sec.traktClientID, sec.traktClientSecret, s.advisorStore)
	ok(w, envelope{"authUrl": client.OOBAuthURL()})
}

func (s *Server) handleTraktOAuthFinish(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	sec := s.getSecrets()
	if sec.traktClientID == "" {
		failErr(w, &plex.Error{
			Message: "Trakt client ID required first",
			Hint:    "Paste it in Settings → API keys, then connect.",
		})
		return
	}
	client := meta.NewTrakt(sec.traktClientID, sec.traktClientSecret, s.advisorStore)
	token, err := client.ExchangeCode(r.Context(), body.Code)
	if err != nil {
		failErr(w, err)
		return
	}
	packed, err := json.Marshal(token)
	if err != nil {
		failErr(w, err)
		return
	}
	if err := s.store.SetSecret("trakt:oauth", string(packed)); err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"connected": true})
}

func (s *Server) handleTraktDisconnect(w http.ResponseWriter, r *http.Request) {
	_ = s.store.ClearSecret("trakt:oauth")
	ok(w, envelope{})
}

// ---- AI plug points (proposals only — never executed, never facts) ---------------

const (
	aiNotConfiguredMsg = "AI is not configured"
	aiHintSuggestions  = "Add an API key in Settings → API keys to enable AI suggestions."
	aiHintPlain        = "Add an API key in Settings → API keys."
)

func (s *Server) handleAIProposeRules(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	ai := s.buildAIClient(s.getSecrets())
	if !ai.Configured() {
		failErr(w, &plex.Error{Message: aiNotConfiguredMsg, Hint: aiHintSuggestions})
		return
	}
	proposal, err := ai.ProposeRules(r.Context(), body.Text)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"proposal": proposal}) // nil → null; renderer falls back
}

func (s *Server) handleAIProposeFranchiseMerge(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Names []string `json:"names"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	ai := s.buildAIClient(s.getSecrets())
	if !ai.Configured() {
		failErr(w, &plex.Error{Message: aiNotConfiguredMsg, Hint: aiHintPlain})
		return
	}
	proposal, err := ai.ProposeFranchiseMerge(r.Context(), body.Names)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"proposal": proposal})
}

func (s *Server) handleAIProposeRibbon(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Context string `json:"context"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	ai := s.buildAIClient(s.getSecrets())
	if !ai.Configured() {
		failErr(w, &plex.Error{Message: aiNotConfiguredMsg, Hint: aiHintSuggestions})
		return
	}
	res, err := ai.SuggestRibbonText(r.Context(), body.Context)
	if err != nil {
		failErr(w, err)
		return
	}
	var proposal any
	if res != nil {
		proposal = map[string]any{"ideas": res.Suggestions}
	}
	ok(w, envelope{"proposal": proposal})
}

func (s *Server) handleAITestConnection(w http.ResponseWriter, r *http.Request) {
	ai := s.buildAIClient(s.getSecrets())
	result, err := ai.TestConnection(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"result": result})
}

// ---- overlay studio ---------------------------------------------------------------

var customBadgeFileRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*\.png$`)

// customBadgeDir is the Electron customBadgeDir(): <dataDir>/badges,
// created on demand. the owner drops custom PNGs there.
func (s *Server) customBadgeDir() string {
	dir := filepath.Join(s.cfg.DataDir, "badges")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

func (s *Server) handleOverlayBadgePacks(w http.ResponseWriter, r *http.Request) {
	dir := s.customBadgeDir()
	custom := []map[string]any{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !customBadgeFileRe.MatchString(strings.ToLower(name)) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() > 2*1024*1024 {
			continue
		}
		base := strings.TrimSuffix(name, filepath.Ext(name))
		custom = append(custom, map[string]any{
			"id": "custom-" + base, "file": name,
			"label":    strings.NewReplacer("-", " ", "_", " ").Replace(base),
			"category": "custom",
		})
	}
	ok(w, envelope{"bundle": s.badgeManifest, "custom": custom, "customDir": dir})
}

func (s *Server) handleOverlayFanartLogos(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TMDBID    json.RawMessage `json:"tmdbId"`
		MediaType string          `json:"mediaType"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	sec := s.getSecrets()
	if sec.fanart == "" {
		ok(w, envelope{"logos": []any{}, "configured": false})
		return
	}
	fanart := meta.NewFanart(sec.fanart, s.advisorStore)
	tmdbID := flexInt64(body.TMDBID)
	var logos []meta.Logo
	var err error
	if body.MediaType == "show" {
		var tvdbID int64
		if sec.tmdb != "" && tmdbID != 0 {
			tmdb := meta.NewTMDb(sec.tmdb, s.advisorStore)
			if ids, ierr := tmdb.TVExternalIDs(r.Context(), tmdbID); ierr == nil && ids != nil {
				tvdbID, _ = strconv.ParseInt(ids.TVDBID, 10, 64)
			}
		}
		if tvdbID != 0 {
			logos, err = fanart.TVLogos(r.Context(), tvdbID)
		}
	} else if tmdbID != 0 {
		logos, err = fanart.MovieLogos(r.Context(), tmdbID)
	}
	if err != nil {
		failErr(w, err)
		return
	}
	if logos == nil {
		logos = []meta.Logo{}
	}
	if len(logos) > 12 {
		logos = logos[:12]
	}
	ok(w, envelope{"logos": logos, "configured": true})
}

// handleOverlayItemFacts ports overlay:itemFacts: facts for conditional
// rules on ONE item, preferring the advisor scan cache, with a live OMDb
// fallback. Field computation is identical to the JS.
//
// FACTS V2 (Kometa parity — kometa-reference/STUDY.md): the facts now
// also carry the item's real stream truth (see facts.go) — resolution
// tier, DV/HDR10/HLG, Atmos/DTS:X/DTS-HD MA/TrueHD, languages, dub
// class — plus contentRating, editionTitle, TV status (TMDb), and the
// ratings source chain Plex Rating[] → OMDb → scan cache. Anything Plex
// doesn't state stays null; rules treat null as "badge hidden".
func (s *Server) handleOverlayItemFacts(w http.ResponseWriter, r *http.Request) {
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
	sec := s.getSecrets()
	scan, _ := s.getFullScan()
	var scannedImdb any
	var scannedTMDBVote any
	var scannedTMDBID, scannedTitle, scannedType, scannedThumb string
	if scan != nil {
		for i := range scan.Items {
			if scan.Items[i].RatingKey == body.RatingKey {
				it := scan.Items[i]
				scannedTitle, scannedType, scannedThumb = it.Title, it.Type, it.Thumb
				if it.IMDBRating != nil {
					scannedImdb = *it.IMDBRating
				}
				if it.TMDB != nil {
					scannedTMDBID = it.TMDB.TMDBID
					if it.TMDB.VoteAverage != nil {
						scannedTMDBVote = *it.TMDB.VoteAverage
					}
				}
				break
			}
		}
	}
	full, err := s.plexClient.GetFullItem(r.Context(), server, body.RatingKey)
	if err != nil {
		failErr(w, err)
		return
	}

	// ---- facts v2: stream truth + ratings chains, all from `full` ------
	sf := parseStreamFacts(full)
	pr := parsePlexRatings(full)
	guidIDs := meta.ParseGuids(guidStringsOf(full))
	itemType := strOf(full["type"], scannedType)

	// IMDb chain: the item's own Plex Rating[] (imdb:// entry) first,
	// then OMDb, then the scan-cached (OMDb-sourced) value. The OMDb
	// call is GetDetails because the same response also feeds the RT /
	// Metascore facts; it is 30-day cached, so this is not a per-render
	// network hit in practice.
	var imdbVal any
	imdbSource := ""
	var rtRating, rtAudience, metascore any
	var awardsText any
	if pr.IMDb != nil {
		imdbVal = *pr.IMDb
		imdbSource = "plex"
	}
	rtRating = floatPtrAny(pr.RTCritic)
	rtAudience = floatPtrAny(pr.RTAudience)
	// The GetDetails call runs whenever the key + IMDb id exist, not
	// only when Plex lacks the rating: the SAME cached response feeds
	// the RT / Metascore gaps AND the awards line (facts.awardsText —
	// the awards ribbon's only source), and an awards line matters
	// even for titles whose IMDb number Plex already carries. Every
	// field still only fills a gap.
	if sec.omdb != "" && guidIDs.IMDB != "" {
		omdb := meta.NewOMDb(sec.omdb, s.advisorStore)
		if det, derr := omdb.GetDetails(r.Context(), guidIDs.IMDB); derr == nil && det != nil {
			if imdbVal == nil && det.IMDBRating != nil {
				imdbVal = *det.IMDBRating
				imdbSource = "omdb"
			}
			if rtRating == nil {
				rtRating = floatPtrAny(det.RTRating)
			}
			if det.Metascore != nil {
				metascore = *det.Metascore
			}
			awardsText = strOrNil(det.Awards)
		}
	}
	if imdbVal == nil && scannedImdb != nil {
		imdbVal = scannedImdb
		imdbSource = "omdb" // the scan's value is itself OMDb-sourced
	}
	// Legacy live-OMDb fallback (GetRating) kept for the exact case the
	// JS handled: no chain value at all. GetDetails above normally covers
	// it; this stays as belt-and-braces parity, not a second source.
	if imdbVal == nil && sec.omdb != "" && guidIDs.IMDB != "" {
		omdb := meta.NewOMDb(sec.omdb, s.advisorStore)
		if rating, rerr := omdb.GetRating(r.Context(), guidIDs.IMDB); rerr == nil && rating != nil && rating.IMDBRating != "" {
			if f, ferr := strconv.ParseFloat(rating.IMDBRating, 64); ferr == nil {
				imdbVal = f
				imdbSource = "omdb"
			}
		}
	}
	tmdbVal := scannedTMDBVote
	if tmdbVal == nil {
		tmdbVal = floatPtrAny(pr.TMDB)
	}

	// The item's TMDb id: its own guid first, the advisor scan cache
	// second — shared by the status and streaming lookups below.
	tmdbID := guidIDs.TMDB
	if tmdbID == 0 && scannedTMDBID != "" {
		tmdbID, _ = strconv.ParseInt(scannedTMDBID, 10, 64)
	}

	// TV status (shows only): TMDb is the only honest source. No key,
	// no TMDb match, or an unmappable status → null, never a guess.
	var tvStatus any
	if itemType == "show" && sec.tmdb != "" && tmdbID != 0 {
		tmdb := meta.NewTMDb(sec.tmdb, s.advisorStore)
		if status, serr := tmdb.TVStatus(r.Context(), tmdbID); serr == nil && status != "" {
			tvStatus = status
		}
	}

	// Streaming providers (movies + shows): TMDb watch/providers, the
	// FULL per-region map — facts are layout-independent, so the
	// streaming family picks its configured region in the evaluator.
	// No key, no TMDb match, or a failed lookup → fact absent, never
	// an error on the whole facts call.
	var streamingByRegion any
	if sec.tmdb != "" && tmdbID != 0 && (itemType == "movie" || itemType == "show") {
		mediaType := "movie"
		if itemType == "show" {
			mediaType = "tv"
		}
		tmdb := meta.NewTMDb(sec.tmdb, s.advisorStore)
		if provs, perr := tmdb.WatchProviders(r.Context(), tmdbID, mediaType); perr == nil && len(provs) > 0 {
			streamingByRegion = provs
		}
	}

	// Plex Collection membership (v1.11 Collection family): the
	// item's own Collection tags on THIS server — the server's own
	// truth, no external key involved. Membership is a movie/show
	// fact: seasons and episodes belong through their parent title,
	// so their facts never carry it. Titles are stated exactly as
	// the server names them, in the record's order.
	var collectionsFact any
	if itemType == "movie" || itemType == "show" {
		if names := collectionTagsOf(full); len(names) > 0 {
			collectionsFact = names
		}
	}

	// Leaving facts (v1.12, Leaving Soon family): the app's own
	// Leaving list entry for this item on THIS server — dates are set
	// in the app (the Leaving soon card on the Apply page), never read
	// from Plex or an external source. leavingDays is the signed count
	// of whole days from the APP's local date (the clock of the
	// machine running this app — store.DaysUntilLeaving) to the
	// stored date; negative once the date has passed. Like collection
	// membership this is a movie/show fact: the manager dates
	// top-level titles, so seasons and episodes never carry it (the
	// evaluator gates them with its own note). No entry — or a stored
	// date that no longer parses — leaves all three facts unset: the
	// family stays dark and its verdict says no date is set.
	var leavingSet any
	var leavingDate, leavingDays any
	if itemType == "movie" || itemType == "show" {
		if entry, found := s.store.LeavingEntryFor(server.Machine, body.RatingKey); found {
			if days, okDays := store.DaysUntilLeaving(entry.Date, time.Now()); okDays {
				leavingSet = true
				leavingDate = entry.Date
				leavingDays = days
			}
		}
	}
	if leavingSet == nil {
		leavingSet = false
	}

	// Franchise + Origin facts (v1.11): TMDb collection membership
	// (movies) and original language / origin countries (movies and
	// shows), from the same cached details response the status and
	// streaming lookups use. Same doctrine as streaming: no key, no
	// TMDb match, or a failed lookup → facts absent, never an error
	// on the whole facts call, never a guessed value.
	var franchiseName, originalLanguage, originCountries any
	var sagaPosition, sagaTotal any
	if sec.tmdb != "" && tmdbID != 0 && (itemType == "movie" || itemType == "show") {
		mediaType := "movie"
		if itemType == "show" {
			mediaType = "tv"
		}
		tmdb := meta.NewTMDb(sec.tmdb, s.advisorStore)
		if org, oerr := tmdb.TitleOrigin(r.Context(), tmdbID, mediaType); oerr == nil && org != nil {
			if itemType == "movie" && org.CollectionName != "" {
				franchiseName = org.CollectionName
			}
			if org.OriginalLanguage != "" {
				originalLanguage = org.OriginalLanguage
			}
			if len(org.OriginCountries) > 0 {
				originCountries = org.OriginCountries
			}
			// Saga position (v1.11.1): where this movie sits in its
			// TMDb collection by release date. The parts call rides
			// the same cached collection endpoint the advisor uses;
			// a failed lookup leaves both facts absent (the family
			// hides), never a partial guess. sagaTotal is set
			// whenever the parts list exists, sagaPosition only when
			// this movie is actually among them — the evaluator
			// distinguishes "no saga" from "not found among parts".
			if itemType == "movie" && org.CollectionID != 0 {
				if cp, cerr := tmdb.CollectionParts(r.Context(), org.CollectionID); cerr == nil && cp != nil && len(cp.Parts) > 0 {
					if pos, total, found := meta.SagaPosition(cp.Parts, tmdbID); total > 0 {
						sagaTotal = total
						if found {
							sagaPosition = pos
						}
					}
				}
			}
		}
	}

	facts := map[string]any{
		"ratingKey":        body.RatingKey,
		"title":            strOf(full["title"], scannedTitle),
		"type":             itemType,
		"thumb":            strOf(full["thumb"], scannedThumb),
		"tmdbRating":       tmdbVal,
		"imdbRating":       imdbVal,
		"imdbRatingSource": strOrNil(imdbSource),
		"rtRating":         rtRating,
		"rtAudience":       rtAudience,
		"metascore":        metascore,
		"hasOmdbKey":       sec.omdb != "",
		"width":            nil,
		"unwatchedCount":   nil,
		"addedAtDays":      nil,
		// facts v2 — stream truth (facts.go)
		"resolutionTier":    strOrNil(sf.ResolutionTier),
		"dv":                sf.DV,
		"hdr10":             sf.HDR10,
		"hlg":               sf.HLG,
		"hdrAny":            sf.HDRAny,
		"atmos":             sf.Atmos,
		"dtsx":              sf.DTSX,
		"dtshdma":           sf.DTSHDMA,
		"truehd":            sf.TrueHD,
		"bestAudioLabel":    strOrNil(sf.BestAudioLabel),
		"bestAudioCodec":    strOrNil(sf.BestAudioCodec),
		"bestAudioChannels": sf.BestAudioChannels,
		"bestAudioLang":     strOrNil(sf.BestAudioLang),
		"aspectRatio":       floatOrNil(sf.AspectRatio),
		"franchiseName":     franchiseName,
		"originalLanguage":  originalLanguage,
		"originCountries":   originCountries,
		"sagaPosition":      sagaPosition,
		"sagaTotal":         sagaTotal,
		"collections":       collectionsFact,
		"leavingSet":        leavingSet,
		"leavingDate":       leavingDate,
		"leavingDays":       leavingDays,
		"sourceTag":         strOrNil(sf.SourceTag),
		"videoCodec":        strOrNil(sf.VideoCodec),
		"studio":            strOrNil(studioOf(full)),
		"genres":            genreTagsOf(full),
		"awardsText":        awardsText,
		"streamingByRegion": streamingByRegion,
		"year":              nil,
		"runtimeMinutes":    nil,
		"seasonCount":       nil,
		"showEpisodeCount":  nil,
		"audioLangs":        sf.AudioLangs,
		"subtitleLangs":     sf.SubtitleLangs,
		"hasSubs":           sf.HasSubs,
		"subtitleCount":     sf.SubtitleCount,
		"subtitleForced":    sf.SubtitleForced,
		"subtitleSDH":       sf.SubtitleSDH,
		"dubClass":          dubClassOf(full),
		"audioTracks":       sf.AudioTracks,
		"mediaCount":        sf.MediaCount,
		"contentRating":     strOrNil(strings.TrimSpace(strOf(full["contentRating"], ""))),
		"editionTitle":      strOrNil(strings.TrimSpace(strOf(full["editionTitle"], ""))),
		"tvStatus":          tvStatus,
	}
	if media, ok := full["Media"].([]any); ok && len(media) > 0 {
		if m0, ok := media[0].(map[string]any); ok {
			if wdt, ok := m0["width"].(float64); ok && wdt != 0 {
				facts["width"] = int(wdt)
			}
		}
	}
	if added, ok := full["addedAt"].(float64); ok && added != 0 {
		facts["addedAtDays"] = (float64(time.Now().UnixMilli()) - added*1000) / 86400000.0
	}
	// Details/runtime facts: the item's own year; a movie's or an
	// episode's own runtime (Plex duration is milliseconds — rounded
	// to the nearest minute); and, for shows, the season/episode
	// counts Plex states on the show record itself (childCount /
	// leafCount). A SHOW gets no runtimeMinutes: it has no single
	// duration, and none is derived from its episodes. These are
	// read directly, never derived from episode scans.
	if y := numOf(full["year"]); y > 0 {
		facts["year"] = y
	}
	if itemType == "movie" || itemType == "episode" {
		if d := numOf(full["duration"]); d > 0 {
			facts["runtimeMinutes"] = (d + 30000) / 60000
		}
	}
	if itemType == "show" {
		if c := numOf(full["childCount"]); c > 0 {
			facts["seasonCount"] = c
		}
		if c := numOf(full["leafCount"]); c > 0 {
			facts["showEpisodeCount"] = c
		}
	}
	switch itemType {
	case "movie", "episode":
		if numOf(full["viewCount"]) > 0 {
			facts["unwatchedCount"] = 0
		} else {
			facts["unwatchedCount"] = 1
		}
	case "show":
		leaf, viewed := numOf(full["leafCount"]), numOf(full["viewedLeafCount"])
		if leaf >= viewed {
			facts["unwatchedCount"] = leaf - viewed
		}
		// Show-level language aggregation (facts.go): the show record
		// has no streams, so union the languages of its episodes
		// (capped). A failed leaves fetch degrades ONLY the language
		// fields — langsError names it, and ratings/status facts for
		// the show stay live. Nothing is guessed in its place.
		leaves, total, lerr := s.plexClient.GetAllLeavesCapped(r.Context(), server, body.RatingKey, EpisodeLangCap)
		var episodes []map[string]any
		if lerr == nil {
			// allLeaves carries NO streams on a real PMS (verified
			// live 2026-10-22) — the episode RECORDS do. Batch-fetch
			// them by key and aggregate from those maps.
			keys := make([]string, 0, len(leaves))
			for _, leaf := range leaves {
				if k := ratingKeyOf(leaf); k != "" {
					keys = append(keys, k)
				}
			}
			episodes, lerr = s.plexClient.GetItemsBatch(r.Context(), server, keys)
		}
		if lerr != nil {
			facts["langsFromEpisodes"] = false
			facts["langsError"] = lerr.Error()
		} else {
			agg := aggregateEpisodeLanguages(episodes, total)
			facts["audioLangs"] = agg.AudioLangs
			facts["subtitleLangs"] = agg.SubtitleLangs
			facts["hasSubs"] = agg.HasSubs
			facts["subtitleCount"] = agg.SubtitleCount
			facts["subtitleForced"] = agg.SubtitleForced
			facts["subtitleSDH"] = agg.SubtitleSDH
			facts["dubClass"] = agg.DubClass
			// For shows audioTracks = DISTINCT audio languages in the
			// union (see aggregateEpisodeLanguages' note) — not a
			// stream count.
			facts["audioTracks"] = len(agg.AudioLangs)
			facts["episodeCount"] = agg.Scanned
			facts["episodeTotal"] = agg.Total
			facts["langsFromEpisodes"] = true
			facts["langsPartial"] = agg.Partial
		}
	}
	if itemType == "season" {
		// Season facts (v1.9, EPISODE-SEASON-OVERLAYS-PLAN phase 1).
		// A season is an overlay target of its own, with facts from
		// ITS episodes only — never the show's other seasons.
		//
		// Identity families are excluded BY DESIGN (they describe
		// the show, not the season): their inputs are suppressed
		// here, at the facts layer, so no layout can fire them for a
		// season even if the season record happens to carry the
		// fields. Ratings are NOT suppressed: the chain above read
		// the season record's OWN Rating[] — the only ratings a
		// season may ever show (present → shown, absent → hidden,
		// never inherited). Content rating likewise stays the
		// record's own field, read generically above.
		facts["studio"] = nil
		facts["genres"] = []string{}
		facts["awardsText"] = nil
		facts["editionTitle"] = nil
		facts["sourceTag"] = nil // the record parse yields none; stated so the exclusion is explicit
		facts["mediaCount"] = 0  // Versions counts an item's files; a season has none of its own
		// Ratings rebuilt from the season record's OWN Rating[]
		// only: the chain above may have filled gaps from OMDb /
		// the scan cache (both keyed on external ids, not on this
		// item), and a season shows a rating solely when Plex
		// states one on the season itself.
		facts["imdbRating"] = floatPtrAny(pr.IMDb)
		if pr.IMDb != nil {
			facts["imdbRatingSource"] = "plex"
		} else {
			facts["imdbRatingSource"] = nil
		}
		facts["tmdbRating"] = floatPtrAny(pr.TMDB)
		facts["rtRating"] = floatPtrAny(pr.RTCritic)
		facts["rtAudience"] = floatPtrAny(pr.RTAudience)
		facts["metascore"] = nil
		// Watch status: the season record's own leaf arithmetic
		// first (exact, and the same computation the show branch
		// uses); the episode aggregation below only fills the gap
		// when the record states no counts.
		leaf, viewed := numOf(full["leafCount"]), numOf(full["viewedLeafCount"])
		recordWatch := false
		if leaf > 0 && leaf >= viewed {
			facts["unwatchedCount"] = leaf - viewed
			recordWatch = true
		}
		// The season's episodes come from its CHILDREN — measured on
		// a real PMS, allLeaves flattens SHOWS only and answers empty
		// for a season. Children give the episode keys; the batch
		// fetch gives the records carrying the streams. Bounded by
		// the same cap as shows, disclosed the same way.
		children, lerr := s.plexClient.GetChildrenItems(r.Context(), server, body.RatingKey)
		var episodes []map[string]any
		total := 0
		if lerr == nil {
			keys := make([]string, 0, len(children))
			for _, ch := range children {
				if ch.Type == "episode" && ch.RatingKey != "" {
					keys = append(keys, ch.RatingKey)
				}
			}
			total = len(keys)
			if len(keys) > EpisodeLangCap {
				keys = keys[:EpisodeLangCap]
			}
			episodes, lerr = s.plexClient.GetItemsBatch(r.Context(), server, keys)
		}
		if lerr != nil {
			// Degrade like a show whose leaves fail: the episode-
			// derived fields stay at their record values (empty —
			// never guessed), the failure is named, and the
			// record's own facts (ratings, watch counts, dates)
			// stay live.
			facts["langsFromEpisodes"] = false
			facts["langsError"] = lerr.Error()
		} else {
			agg := aggregateSeasonFacts(episodes, total)
			asf := agg.SF
			facts["resolutionTier"] = strOrNil(asf.ResolutionTier)
			facts["dv"] = asf.DV
			facts["hdr10"] = asf.HDR10
			facts["hlg"] = asf.HLG
			facts["hdrAny"] = asf.HDRAny
			facts["atmos"] = asf.Atmos
			facts["dtsx"] = asf.DTSX
			facts["dtshdma"] = asf.DTSHDMA
			facts["truehd"] = asf.TrueHD
			facts["bestAudioLabel"] = strOrNil(asf.BestAudioLabel)
			facts["bestAudioCodec"] = strOrNil(asf.BestAudioCodec)
			facts["bestAudioChannels"] = asf.BestAudioChannels
			facts["bestAudioLang"] = strOrNil(asf.BestAudioLang)
			facts["videoCodec"] = strOrNil(asf.VideoCodec)
			// aspectRatio stays nil for a season: the aggregation
			// deliberately never derives one (episodes can disagree
			// about their frame), so the aspect family stays dark.
			facts["audioLangs"] = agg.Langs.AudioLangs
			facts["subtitleLangs"] = agg.Langs.SubtitleLangs
			facts["hasSubs"] = agg.Langs.HasSubs
			facts["subtitleCount"] = agg.Langs.SubtitleCount
			facts["subtitleForced"] = agg.Langs.SubtitleForced
			facts["subtitleSDH"] = agg.Langs.SubtitleSDH
			facts["dubClass"] = agg.Langs.DubClass
			// Show convention: audioTracks = distinct audio
			// languages in the union, not a stream count.
			facts["audioTracks"] = len(agg.Langs.AudioLangs)
			facts["episodeCount"] = agg.Langs.Scanned
			facts["episodeTotal"] = agg.Langs.Total
			facts["langsFromEpisodes"] = true
			facts["langsPartial"] = agg.Langs.Partial
			// Season runtime = the season's total: its episodes'
			// durations summed (their own Plex values — nothing is
			// estimated). Zero stated durations → fact stays null.
			if agg.RuntimeMinutes > 0 {
				facts["runtimeMinutes"] = agg.RuntimeMinutes
			}
			if !recordWatch && agg.Scanned > 0 {
				facts["unwatchedCount"] = agg.Unwatched
			}
		}
	}
	if itemType == "episode" {
		// Episode facts (v1.10, EPISODE-SEASON-OVERLAYS-PLAN phase 2).
		// An episode is judged on its OWN record: the stream facts
		// above came from THIS episode's Media/Part streams via the
		// generic per-item parse, its runtime from its own duration,
		// its watch state from its own viewCount — nothing is
		// aggregated from the show or the season, and nothing needs
		// to be: the streams live on the episode.
		//
		// Identity families are excluded BY DESIGN (they describe
		// the movie or the show, not the episode): their inputs are
		// suppressed here, at the facts layer, mirroring the season
		// branch. An episode record CAN carry a studio and genres,
		// its file name can carry a source tag, and the OMDb lookup
		// on the episode's own IMDb id can fill awards text — none
		// of it may fire a badge on an episode still. Ratings are
		// NOT suppressed: the chain read the episode record's own
		// values, and the composite layer's episode gate keeps the
		// ratings family dark for episodes by rule (episode ratings
		// are sparse agent data, never the show's identity).
		facts["studio"] = nil
		facts["genres"] = []string{}
		facts["awardsText"] = nil
		facts["editionTitle"] = nil
		facts["sourceTag"] = nil
	}
	ok(w, envelope{"facts": facts})
}

// ratingKeyOf reads a ratingKey from a parsed metadata map, accepting
// the string form Plex JSON uses and a bare number defensively.
func ratingKeyOf(m map[string]any) string {
	switch v := m["ratingKey"].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	}
	return ""
}

// guidStringsOf extracts the Guid[].id strings of a full item (the
// "imdb://tt…" / "tmdb://…" forms meta.ParseGuids consumes).
func guidStringsOf(full map[string]any) []string {
	ids := []string{}
	if guids, ok := full["Guid"].([]any); ok {
		for _, g := range guids {
			if gm, ok := g.(map[string]any); ok {
				if id, ok := gm["id"].(string); ok {
					ids = append(ids, id)
				}
			}
		}
	}
	return ids
}

// strOrNil: "" → nil (JSON null), anything else passes through.
func strOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// floatPtrAny: nil pointer → nil, else the dereferenced value.
func floatPtrAny(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

// floatOrNil: non-positive → nil (JSON null), else the value. For
// facts where 0 is the parser's "Plex didn't state it" (aspectRatio).
func floatOrNil(f float64) any {
	if f <= 0 {
		return nil
	}
	return f
}

func strOf(v any, fallback string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}

func numOf(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

func (s *Server) handleOverlayApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Jobs      []overlays.Job     `json:"jobs"`
		BadgeSet  json.RawMessage    `json:"badgeSet"`
		Target    json.RawMessage    `json:"target"`
		RunID     string             `json:"runId"`
		RunPhase  string             `json:"runPhase"`
		Cancelled bool               `json:"cancelled"`
		Outcomes  []overlays.RunItem `json:"outcomes"`
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
	record, err := s.overlay.RunApplyJob(r.Context(), overlays.ApplyRequest{
		Server: server, Jobs: body.Jobs, BadgeSet: body.BadgeSet, Target: body.Target,
		RunID: body.RunID, RunPhase: body.RunPhase, Cancelled: body.Cancelled, Outcomes: body.Outcomes,
		OnProgress: func(p overlays.Progress) { s.events.publish("overlay-progress", p) },
	})
	if err != nil {
		failErr(w, err)
		return
	}
	// Applied items have new bytes behind unchanged thumb paths —
	// evict their cached art (all sizes) so grids/stage re-fetch.
	thumbs := map[string]string{}
	for _, j := range body.Jobs {
		thumbs[j.RatingKey] = j.Thumb
	}
	for _, it := range record.Items {
		if it.OK || it.Outcome == "applied" {
			s.invalidateArt(server.Machine, thumbs[it.RatingKey])
		}
	}
	ok(w, envelope{"record": record})
}

// handleOverlayClassifyPoster returns one item's restore-default
// classification (read-only): whether it is currently showing an
// uploaded image and has a provider poster to switch to.
func (s *Server) handleOverlayClassifyPoster(w http.ResponseWriter, r *http.Request) {
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
	class, err := s.overlay.ClassifyItemPosters(r.Context(), server, body.RatingKey)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"classification": class})
}

// handleOverlayRestoreDefaultPoster performs ONE item's restore-default:
// verified backup of the current poster first, then a fresh upload of
// the provider poster's own bytes (an upload — not a re-selection —
// because only an upload refreshes the poster address clients fetch),
// then the three-part verification in overlays.RestoreDefaultPoster.
// Per-item outcomes (including failures) are data in the envelope, not
// transport errors, so the client's batched run can record them as-is.
func (s *Server) handleOverlayRestoreDefaultPoster(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RatingKey   string `json:"ratingKey"`
		Title       string `json:"title"`
		Thumb       string `json:"thumb"`
		SectionKey  string `json:"sectionKey"`
		SectionType string `json:"sectionType"`
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
	itemType := 0
	switch body.SectionType {
	case "movie":
		itemType = 1
	case "show":
		itemType = 2
	default:
		failErr(w, &plex.Error{Message: "Restore default posters needs a movie or show library", Hint: "Pick a movie or TV library."})
		return
	}
	outcome, backupPath, errMsg, err := s.overlay.RestoreDefaultPoster(r.Context(), server, body.SectionKey, itemType, body.RatingKey, body.Title, body.Thumb)
	if err != nil {
		failErr(w, err)
		return
	}
	if outcome == "restored" {
		s.invalidateArt(server.Machine, body.Thumb)
	}
	ok(w, envelope{"outcome": outcome, "backupPath": backupPath, "error": errMsg})
}

func (s *Server) handleOverlayRestore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RatingKey string `json:"ratingKey"`
		RunID     string `json:"runId"`
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
	var res map[string]any
	switch {
	case body.RunID != "":
		res, err = s.overlay.RestoreRun(r.Context(), server, body.RunID)
	case body.RatingKey != "":
		res, err = s.overlay.RestorePoster(r.Context(), server, body.RatingKey)
	default:
		failErr(w, &plex.Error{Message: "Nothing to restore", Hint: "Pass a ratingKey or a runId."})
		return
	}
	if err != nil {
		failErr(w, err)
		return
	}
	// Restored items serve their backup bytes at the same thumb path —
	// evict the cached composite so the grids show the restored poster.
	// The current thumb path comes from a fresh item read (run records
	// do not carry it).
	invalidateRestored := func(ratingKey string) {
		if item, ierr := s.plexClient.GetItem(r.Context(), server, ratingKey); ierr == nil {
			s.invalidateArt(server.Machine, item.Thumb)
		}
	}
	switch {
	case body.RunID != "":
		if results, ok := res["results"].([]map[string]any); ok {
			for _, r := range results {
				if okFlag, _ := r["ok"].(bool); okFlag {
					if rk, _ := r["ratingKey"].(string); rk != "" {
						invalidateRestored(rk)
					}
				}
			}
		}
	case body.RatingKey != "":
		invalidateRestored(body.RatingKey)
	}
	out := envelope{}
	for k, v := range res {
		out[k] = v
	}
	ok(w, out)
}

func (s *Server) handleOverlayHistory(w http.ResponseWriter, r *http.Request) {
	ok(w, envelope{"runs": s.overlay.ListRuns()})
}

func (s *Server) handleOverlayReapplyCheck(w http.ResponseWriter, r *http.Request) {
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	items, err := s.overlay.ReapplyCheck(r.Context(), server)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"items": items})
}

// ---- overlay templates ---------------------------------------------------------------

func (s *Server) handleOverlayTemplatesList(w http.ResponseWriter, r *http.Request) {
	ok(w, envelope{"templates": s.templates.List()})
}

func (s *Server) handleOverlayTemplateSave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name       string          `json:"name"`
		Placements json.RawMessage `json:"placements"`
		Families   json.RawMessage `json:"families"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	res, err := s.templates.SaveLayout(body.Name, body.Placements, body.Families)
	if err != nil {
		failErr(w, err)
		return
	}
	out := envelope{}
	for k, v := range res {
		out[k] = v
	}
	ok(w, out)
}

func (s *Server) handleOverlayTemplateLoad(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	res, err := s.templates.Load(body.Name)
	if err != nil {
		failErr(w, err)
		return
	}
	out := envelope{}
	for k, v := range res {
		out[k] = v
	}
	ok(w, out)
}

func (s *Server) handleOverlayTemplateDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	res, err := s.templates.Remove(body.Name)
	if err != nil {
		failErr(w, err)
		return
	}
	out := envelope{}
	for k, v := range res {
		out[k] = v
	}
	ok(w, out)
}
