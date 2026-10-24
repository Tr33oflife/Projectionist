package api

// services.go — service construction + the shared helpers the handlers
// use. This is the Go counterpart of the Electron main.js module-level
// wiring (boot(), getSecrets(), buildMetaClients(), buildAiClient(),
// getTraktToken(), getClientId(), slimScan(), getFullScan(),
// publicServers(), requireServer()). Ported behavior-for-behavior.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"plexcollectionmanager/internal/advisor"
	"plexcollectionmanager/internal/collectionart"
	"plexcollectionmanager/internal/meta"
	"plexcollectionmanager/internal/overlays"
	"plexcollectionmanager/internal/plex"
)

// initServices builds the Plex client and the disk-backed services once
// at startup, mirroring Electron's boot(). The overlay applier's
// VerifyBackup is wired into the Plex client as its PosterBackupVerifier
// — the exact cross-wire main.js did (plex.posterBackupVerifier =
// overlay.verifyBackup) so even direct poster writes are backup-gated.
func (s *Server) initServices() error {
	clientID, err := s.clientID()
	if err != nil {
		return err
	}
	s.plexClient = plex.NewClient("Plex Collection Manager", s.cfg.Version, clientID)
	s.advisorStore = advisor.NewStore(s.cfg.DataDir)
	s.overlay = overlays.NewApplier(s.cfg.DataDir, s.plexClient)
	s.plexClient.PosterBackupVerifier = s.overlay.VerifyBackup
	s.templates = overlays.NewTemplateStore(s.cfg.DataDir)

	art, err := collectionart.NewService(s.cfg.DataDir, s.cfg.CatalogJSON)
	if err != nil {
		return fmt.Errorf("collection art catalog: %w", err)
	}
	s.art = art

	// Bundled badge manifest — served verbatim by overlay:badgePacks.
	// A missing/corrupt manifest degrades to the same empty pack the
	// Electron boot used, never a startup crash.
	manifest, err := fs.ReadFile(s.cfg.BadgesFS, "badges.json")
	if err != nil || !json.Valid(manifest) {
		manifest = []byte(`{"name":"Default pack","badges":[]}`)
	}
	s.badgeManifest = manifest
	return nil
}

// clientID returns the stable per-install Plex client identifier,
// generated once and persisted in settings (Electron getClientId).
// FUTURE AI: never regenerate this on boot — Plex ties PIN approvals to
// it; rotating it silently signs the user out of the Plex-side approval.
func (s *Server) clientID() (string, error) {
	if id := s.store.SettingString("clientId"); id != "" {
		return id, nil
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := "pcm-" + hex.EncodeToString(b)
	if err := s.store.SetSettings(map[string]any{"clientId": id}); err != nil {
		return "", err
	}
	return id, nil
}

// ---- errors -----------------------------------------------------------------

// errorParts extracts (message, hint) from the ported error types — the
// Electron fail() read PlexError.message/.hint the same way.
func errorParts(err error) (string, string) {
	var pe *plex.Error
	if errors.As(err, &pe) {
		return pe.Message, pe.Hint
	}
	var me *meta.Error
	if errors.As(err, &me) {
		return me.Message, me.Hint
	}
	var oe *overlays.Error
	if errors.As(err, &oe) {
		return oe.Message, oe.Hint
	}
	return err.Error(), ""
}

// isCancelled reports whether err is a Plex "request cancelled" — the
// getCollectionItems handler turns those into { cancelled:true }
// successes exactly like Electron's isCancelled() path.
func isCancelled(err error) bool {
	var pe *plex.Error
	return errors.As(err, &pe) && pe.Cancelled
}

// ---- secrets + metadata clients --------------------------------------------

// secretFor maps Settings field names to token-store secret names
// (Electron SECRET_FOR).
var secretFor = map[string]string{
	"tmdb":              "tmdb:api-key",
	"traktClientId":     "trakt:client-id",
	"traktClientSecret": "trakt:client-secret",
	"omdb":              "omdb:api-key",
	"fanart":            "fanart:api-key",
	"aiKey":             "ai:api-key",
}

type secrets struct {
	tmdb              string
	traktClientID     string
	traktClientSecret string
	omdb              string
	fanart            string
	aiKey             string
}

func (s *Server) getSecrets() secrets {
	return secrets{
		tmdb:              s.store.GetSecret(secretFor["tmdb"]),
		traktClientID:     s.store.GetSecret(secretFor["traktClientId"]),
		traktClientSecret: s.store.GetSecret(secretFor["traktClientSecret"]),
		omdb:              s.store.GetSecret(secretFor["omdb"]),
		fanart:            s.store.GetSecret(secretFor["fanart"]),
		aiKey:             s.store.GetSecret(secretFor["aiKey"]),
	}
}

// metaClients mirrors buildMetaClients: nil where unconfigured (except
// OMDb/AniList, which are key-tolerant by design).
type metaClients struct {
	tmdb    *meta.TMDbClient
	trakt   *meta.TraktClient
	omdb    *meta.OMDbClient
	anilist *meta.AniListClient
}

func (s *Server) buildMetaClients(sec secrets) metaClients {
	var mc metaClients
	if sec.tmdb != "" {
		mc.tmdb = meta.NewTMDb(sec.tmdb, s.advisorStore)
	}
	if sec.traktClientID != "" {
		mc.trakt = meta.NewTrakt(sec.traktClientID, sec.traktClientSecret, s.advisorStore)
	}
	mc.omdb = meta.NewOMDb(sec.omdb, s.advisorStore)
	mc.anilist = meta.NewAniList(s.advisorStore)
	return mc
}

// buildAIClient mirrors buildAiClient: always constructs (empty key =
// unconfigured), with base URL + model from settings.
func (s *Server) buildAIClient(sec secrets) *meta.AIClient {
	base := s.store.SettingString("aiBaseUrl")
	if base == "" {
		base = meta.DefaultBaseURL
	}
	model := s.store.SettingString("aiModel")
	if model == "" {
		model = meta.DefaultModel
	}
	return meta.NewAI(sec.aiKey, base, model)
}

// getTraktToken returns a valid Trakt access token, refreshing first
// when the stored token is within a minute of expiry (Electron
// getTraktToken). Any failure yields "" — Trakt features quietly absent.
func (s *Server) getTraktToken(ctx context.Context, sec secrets) string {
	raw := s.store.GetSecret("trakt:oauth")
	if raw == "" {
		return ""
	}
	var tok meta.TraktToken
	if err := json.Unmarshal([]byte(raw), &tok); err != nil {
		return ""
	}
	if tok.ExpiresAt != 0 && time.Now().UnixMilli() > tok.ExpiresAt-60000 {
		if sec.traktClientID == "" || sec.traktClientSecret == "" || tok.RefreshToken == "" {
			return ""
		}
		client := meta.NewTrakt(sec.traktClientID, sec.traktClientSecret, s.advisorStore)
		nt, err := client.RefreshAccessToken(ctx, tok.RefreshToken)
		if err != nil || nt == nil {
			return ""
		}
		if packed, err := json.Marshal(nt); err == nil {
			_ = s.store.SetSecret("trakt:oauth", string(packed))
		}
		return nt.AccessToken
	}
	return tok.AccessToken
}

// ---- session state ------------------------------------------------------------

func (s *Server) selectedServer() *plex.Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, srv := range s.servers {
		if srv.Machine == s.selectedMachine {
			return srv
		}
	}
	return nil
}

// requireServer is Electron's requireServer(): the selected server or
// the exact "pick a server first" error the UI shows.
func (s *Server) requireServer() (*plex.Server, error) {
	srv := s.selectedServer()
	if srv == nil {
		return nil, &plex.Error{
			Message: "No Plex server selected",
			Hint:    "Pick a server on the Connect tab first.",
		}
	}
	return srv, nil
}

// publicServers is Electron's publicServers(): NO tokens, NO raw
// connection URIs — artwork flows through the /art proxy route, so the
// renderer never needs either.
func (s *Server) publicServers() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, 0, len(s.servers))
	for _, srv := range s.servers {
		out = append(out, map[string]any{
			"name":         srv.Name,
			"machine":      srv.Machine,
			"owned":        srv.Owned,
			"quality":      srv.BestConnection.Quality,
			"qualityLabel": srv.BestConnection.Label,
		})
	}
	return out
}

// adoptSession is the shared tail of pollLogin/restoreSession: remember
// the token + servers and re-select the previous default server when it
// is still accessible, else the first server (Electron behavior).
func (s *Server) adoptSession(user json.RawMessage, userToken string, servers []*plex.Server) string {
	s.mu.Lock()
	s.user = user
	s.userToken = userToken
	s.servers = servers
	selected := ""
	def := s.store.SettingString("defaultMachine")
	for _, srv := range servers {
		if def != "" && srv.Machine == def {
			selected = def
			break
		}
	}
	if selected == "" && len(servers) > 0 {
		selected = servers[0].Machine
	}
	s.selectedMachine = selected
	s.mu.Unlock()
	return selected
}

// ---- scan helpers ---------------------------------------------------------------

// getFullScan mirrors Electron's getFullScan(): in-memory cache first,
// advisor store snapshot second. nil (no error) = no scan yet.
func (s *Server) getFullScan() (*advisor.Scan, error) {
	server, err := s.requireServer()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	cached := s.scanCache[server.Machine]
	s.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	scan, err := s.advisorStore.GetScan(server.Machine)
	if err != nil || scan == nil {
		return scan, err
	}
	s.mu.Lock()
	s.scanCache[server.Machine] = scan
	s.mu.Unlock()
	return scan, nil
}

// slimScan mirrors Electron's slimScan(): the renderer-facing scan
// summary — full TMDb objects are stripped to the handful of fields the
// Advisor UI actually renders (voteAverage, genre names, anime flag).
func slimScan(scan *advisor.Scan, settings advisor.Settings) map[string]any {
	items := make([]map[string]any, 0, len(scan.Items))
	for _, it := range scan.Items {
		var vote any
		genres := []string{}
		if it.TMDB != nil {
			if it.TMDB.VoteAverage != nil {
				vote = *it.TMDB.VoteAverage
			}
			for _, g := range it.TMDB.Genres {
				genres = append(genres, g.Name)
			}
		}
		var imdb any
		if it.IMDBRating != nil {
			imdb = *it.IMDBRating
		}
		var year any
		if it.Year != nil {
			year = *it.Year
		}
		items = append(items, map[string]any{
			"ratingKey":      it.RatingKey,
			"title":          it.Title,
			"year":           year,
			"type":           it.Type,
			"sectionKey":     it.SectionKey,
			"thumb":          it.Thumb,
			"viewCount":      it.ViewCount,
			"childCount":     it.ChildCount,
			"matchedByTitle": it.MatchedByTitle,
			"voteAverage":    vote,
			"imdbRating":     imdb,
			"genres":         genres,
			"anime":          advisor.IsAnime(it, settings),
		})
	}
	return map[string]any{
		"machineId":      scan.MachineID,
		"scannedAt":      scan.ScannedAt,
		"sections":       scan.Sections,
		"itemCount":      scan.ItemCount,
		"tmdbKeyPresent": scan.TMDBKeyPresent,
		"omdbKeyPresent": scan.OMDbKeyPresent,
		"items":          items,
	}
}

// ---- flexible JSON coercion -----------------------------------------------------

// flexIntPtr coerces a JSON value that may be a number, a numeric string
// ("1" from a <select>.value — the Builder sends those), or null/absent
// into a *int for the Plex client. JS interpolated these loosely; Go must
// not reject the renderer's strings.
func flexIntPtr(raw json.RawMessage) *int {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return &n
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil && str != "" {
		var v int
		if _, err := fmt.Sscanf(str, "%d", &v); err == nil {
			return &v
		}
	}
	return nil
}

// flexInt64 is flexIntPtr for int64 targets (fanart tmdbId etc.).
func flexInt64(raw json.RawMessage) int64 {
	if p := flexIntPtr(raw); p != nil {
		return int64(*p)
	}
	return 0
}

// flexString coerces numbers/bools to their string form (JS String()).
func flexString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return str
	}
	return string(raw)
}

// truncStr truncates to n runes (JS .slice(0, n) counts UTF-16 units;
// rune counting matches for the BMP text this app handles).
func truncStr(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
