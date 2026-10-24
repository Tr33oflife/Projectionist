package meta

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Trakt client — port of src/metadata/trakt.js. See that file's header
// for the full rationale. The short version:
//
//   - Public lists (trending / popular / anticipated) need only the
//     client ID in the `trakt-api-key` header — no OAuth.
//   - Watchlist needs OAuth ("turn my Trakt watchlist into a Plex
//     collection" — the killer feature; the Advisor intersects the ids
//     against the library scan).
//
// OAuth flow (OUT-OF-BAND code paste ONLY):
//  1. OOBAuthURL() → Trakt authorize URL with
//     redirect_uri=urn:ietf:wg:oauth:2.0:oob. Opened in the system browser.
//  2. the owner approves on trakt.tv; Trakt shows him a CODE; he pastes it in.
//  3. ExchangeCode(code) → POST /oauth/token → tokens.
//
// Do NOT reintroduce a localhost callback: Trakt validates redirect_uri
// against the value registered on the Trakt app page, and a localhost
// callback on a random port cannot be pre-registered — a previous
// version tried it and failed authorize with redirect_uri_mismatch.
// There is deliberately no local server anywhere in this file.
//
// VERIFICATION STATUS: UNVERIFIED live (same as the JS).

const traktBase = "https://api.trakt.tv"

// OOBRedirect is the ONLY redirect URI this app uses. the owner registers
// exactly this string on his Trakt app page. Fixed, well-known, never a
// localhost port (see the file header).
const OOBRedirect = "urn:ietf:wg:oauth:2.0:oob"

// TraktToken mirrors the JS token object stored by the app:
// {accessToken, refreshToken, expiresAt} with expiresAt in ms epoch.
type TraktToken struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    int64  `json:"expiresAt"`
}

// TraktEntry is one normalized list/watchlist entry. List methods return
// {title, year, tmdbId, imdbId, traktId, mediaType}; the watchlist
// returns the same minus traktId — MarshalJSON preserves that split.
type TraktEntry struct {
	Title     string `json:"title"`
	Year      *int64 `json:"year"`
	TMDBID    string `json:"tmdbId"`
	IMDBID    string `json:"imdbId"`
	TraktID   string `json:"traktId"`
	MediaType string `json:"mediaType"`

	watchlist bool // JS watchlist entries carry no traktId key
}

// MarshalJSON emits traktId for list entries and omits it for watchlist
// entries, matching the two JS object shapes.
func (e TraktEntry) MarshalJSON() ([]byte, error) {
	if e.watchlist {
		return json.Marshal(struct {
			Title     string `json:"title"`
			Year      *int64 `json:"year"`
			TMDBID    string `json:"tmdbId"`
			IMDBID    string `json:"imdbId"`
			MediaType string `json:"mediaType"`
		}{Title: e.Title, Year: e.Year, TMDBID: e.TMDBID, IMDBID: e.IMDBID, MediaType: e.MediaType})
	}
	type listAlias TraktEntry
	return json.Marshal(listAlias(e))
}

// TraktClient is the Go port of the JS TraktClient.
type TraktClient struct {
	clientID     string
	clientSecret string
	cache        Cache
	baseURL      string
	httpClient   *http.Client
}

// NewTrakt builds a Trakt client.
//
// ADAPTATION (flagged): the JS constructor THREW TraktError when no
// client ID was configured. This Go signature cannot return an error,
// so an empty ID is stored and the data methods return the same *Error
// the JS threw before any I/O. (OOBAuthURL still builds its URL, as the
// JS would have before construction failed — callers should not use a
// client with no ID.)
func NewTrakt(clientID, clientSecret string, cache Cache) *TraktClient {
	return &TraktClient{
		clientID:     clientID,
		clientSecret: clientSecret,
		cache:        cache,
		baseURL:      traktBase,
		httpClient:   &http.Client{},
	}
}

func (c *TraktClient) headers(token string) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("trakt-api-version", "2")
	h.Set("trakt-api-key", c.clientID)
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	return h
}

func (c *TraktClient) noClientID() error {
	return &Error{
		Message: "No Trakt client ID configured",
		Hint:    "Create an app at trakt.tv/oauth/applications, then paste the client ID in Settings → API keys.",
	}
}

// get is the JS _get(): optional cache (namespace 'trakt'), exact HTTP
// status + body snippet on failure.
func (c *TraktClient) get(ctx context.Context, path string, token string, cacheKey string) (json.RawMessage, error) {
	if c.clientID == "" {
		return nil, c.noClientID()
	}
	if cacheKey != "" {
		if hit, ok := cacheGet(c.cache, "trakt", cacheKey); ok {
			return hit, nil
		}
	}
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header = c.headers(token)
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &Error{
			Message: fmt.Sprintf("Could not reach Trakt (%s)", err.Error()),
			Hint:    "Check internet access.",
		}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		msg := fmt.Sprintf("Trakt returned HTTP %d", res.StatusCode)
		if len(body) > 0 {
			msg += ": " + bodySnippet(body, 160)
		}
		hint := ""
		if res.StatusCode == 401 {
			hint = "Trakt rejected the credentials — check the client ID / OAuth token in Settings."
		}
		return nil, &Error{Message: msg, Status: res.StatusCode, Hint: hint}
	}
	if cacheKey != "" {
		cacheSet(c.cache, "trakt", cacheKey, json.RawMessage(body))
	}
	return json.RawMessage(body), nil
}

// ---- OAuth (out-of-band only) ------------------------------------------

// OOBAuthURL returns the Trakt approval URL (JS oobAuthUrl()). the owner
// approves in his browser, Trakt shows him a code, he pastes it into the
// app, and the caller runs ExchangeCode. No local server, ever.
func (c *TraktClient) OOBAuthURL() string {
	return "https://trakt.tv/oauth/authorize?response_type=code" +
		"&client_id=" + url.QueryEscape(c.clientID) +
		"&redirect_uri=" + url.QueryEscape(OOBRedirect)
}

type traktTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

func (t *traktTokenResponse) expiresAt() int64 {
	expiresIn := t.ExpiresIn
	if expiresIn == 0 {
		expiresIn = 7776000 // JS default: data.expires_in || 7776000 (~3 months)
	}
	return time.Now().UnixMilli() + expiresIn*1000
}

func (c *TraktClient) postToken(ctx context.Context, payload map[string]string) (*traktTokenResponse, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.baseURL+"/oauth/token", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, &Error{
			Message: fmt.Sprintf("Trakt token request returned HTTP %d", res.StatusCode),
			Status:  res.StatusCode,
		}
	}
	var data traktTokenResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

// ExchangeCode exchanges the pasted OOB code for tokens. The
// redirect_uri sent is the same OOB constant the owner registered.
func (c *TraktClient) ExchangeCode(ctx context.Context, code string) (*TraktToken, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, &Error{
			Message: "No Trakt code pasted",
			Hint:    "Approve the app on trakt.tv, then paste the code it shows you.",
		}
	}
	if c.clientSecret == "" {
		return nil, &Error{
			Message: "Trakt client secret is required to finish OAuth",
			Hint:    "Paste it in Settings → API keys next to the client ID.",
		}
	}
	data, err := c.postToken(ctx, map[string]string{
		"code":          code,
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
		"redirect_uri":  OOBRedirect,
		"grant_type":    "authorization_code",
	})
	if err != nil {
		// The JS exchange wrapped transport failures as
		// "Trakt token exchange failed (...)" and surfaced HTTP failures
		// as "Trakt token exchange returned HTTP <status>" with NO body
		// snippet — preserved here (this endpoint's error bodies are not
		// part of the JS surface).
		if e, ok := err.(*Error); ok && e.Status != 0 {
			return nil, &Error{Message: fmt.Sprintf("Trakt token exchange returned HTTP %d", e.Status), Status: e.Status}
		}
		return nil, &Error{Message: fmt.Sprintf("Trakt token exchange failed (%s)", err.Error())}
	}
	if data.AccessToken == "" {
		return nil, &Error{Message: "Trakt did not return an access token"}
	}
	return &TraktToken{
		AccessToken:  data.AccessToken,
		RefreshToken: data.RefreshToken,
		ExpiresAt:    data.expiresAt(),
	}, nil
}

// RefreshAccessToken renews an expired token (JS refreshAccessToken()).
// A failed refresh keeps the JS message shape: status only, no body.
func (c *TraktClient) RefreshAccessToken(ctx context.Context, refreshToken string) (*TraktToken, error) {
	if c.clientSecret == "" || refreshToken == "" {
		return nil, &Error{Message: "Cannot refresh Trakt token without client secret + refresh token"}
	}
	data, err := c.postToken(ctx, map[string]string{
		"refresh_token": refreshToken,
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
		"redirect_uri":  OOBRedirect,
		"grant_type":    "refresh_token",
	})
	if err != nil {
		if e, ok := err.(*Error); ok && e.Status != 0 {
			return nil, &Error{Message: fmt.Sprintf("Trakt token refresh returned HTTP %d", e.Status), Status: e.Status}
		}
		return nil, err
	}
	refresh := data.RefreshToken
	if refresh == "" {
		refresh = refreshToken // JS: data.refresh_token || refreshToken
	}
	return &TraktToken{
		AccessToken:  data.AccessToken,
		RefreshToken: refresh,
		ExpiresAt:    data.expiresAt(),
	}, nil
}

// ---- public lists (client ID only — no OAuth needed) --------------------
//
// Each entry: {title, year, tmdbId, imdbId, traktId, mediaType}. The ids
// are what the Advisor intersects against the library scan.
//
// UNVERIFIED nuance preserved from the JS: _normalizeList reads the
// wrapper object (e.movie || e.show) only. Trakt list endpoints that
// return bare media objects (no wrapper) therefore normalize to nothing
// in the JS — and here too. This is a faithful port, not a fix; revisit
// only with live evidence.

type traktMediaRaw struct {
	Title string `json:"title"`
	Year  *int64 `json:"year"`
	IDs   struct {
		TMDB  *int64 `json:"tmdb"`
		IMDB  string `json:"imdb"`
		Trakt *int64 `json:"trakt"`
	} `json:"ids"`
}

type traktEntryRaw struct {
	Movie *traktMediaRaw `json:"movie"`
	Show  *traktMediaRaw `json:"show"`
}

func (r *traktEntryRaw) media() *traktMediaRaw {
	if r.Movie != nil {
		return r.Movie
	}
	if r.Show != nil {
		return r.Show
	}
	return &traktMediaRaw{}
}

func idString(p *int64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatInt(*p, 10)
}

func normalizeTraktList(entries []traktEntryRaw, mediaType string) []TraktEntry {
	out := make([]TraktEntry, 0, len(entries))
	for i := range entries {
		m := entries[i].media()
		e := TraktEntry{
			Title:     m.Title,
			Year:      m.Year,
			TMDBID:    idString(m.IDs.TMDB),
			IMDBID:    m.IDs.IMDB,
			TraktID:   idString(m.IDs.Trakt),
			MediaType: mediaType,
		}
		if e.Title != "" {
			out = append(out, e)
		}
	}
	return out
}

func (c *TraktClient) list(ctx context.Context, path, cacheKey, mediaType string) ([]TraktEntry, error) {
	raw, err := c.get(ctx, path, "", cacheKey)
	if err != nil {
		return nil, err
	}
	var data []traktEntryRaw
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return normalizeTraktList(data, mediaType), nil
}

// TrendingMovies is the JS trendingMovies().
func (c *TraktClient) TrendingMovies(ctx context.Context) ([]TraktEntry, error) {
	return c.list(ctx, "/movies/trending?limit=100", "lists/trending-movies", "movie")
}

// PopularMovies is the JS popularMovies().
func (c *TraktClient) PopularMovies(ctx context.Context) ([]TraktEntry, error) {
	return c.list(ctx, "/movies/popular?limit=100", "lists/popular-movies", "movie")
}

// AnticipatedMovies is the JS anticipatedMovies().
func (c *TraktClient) AnticipatedMovies(ctx context.Context) ([]TraktEntry, error) {
	return c.list(ctx, "/movies/anticipated?limit=100", "lists/anticipated-movies", "movie")
}

// TrendingShows is the JS trendingShows().
func (c *TraktClient) TrendingShows(ctx context.Context) ([]TraktEntry, error) {
	return c.list(ctx, "/shows/trending?limit=100", "lists/trending-shows", "show")
}

// PopularShows is the JS popularShows().
func (c *TraktClient) PopularShows(ctx context.Context) ([]TraktEntry, error) {
	return c.list(ctx, "/shows/popular?limit=100", "lists/popular-shows", "show")
}

// ---- watchlist (OAuth token required) -----------------------------------

// Watchlist is the JS watchlist(token, mediaType). mediaType is the JS
// string: "movies" (the JS default) or "shows". Entries carry no
// traktId key, exactly as the JS returned them.
func (c *TraktClient) Watchlist(ctx context.Context, token string, mediaType string) ([]TraktEntry, error) {
	if mediaType == "" {
		mediaType = "movies" // JS default parameter
	}
	if token == "" {
		return nil, &Error{
			Message: "Trakt watchlist needs an OAuth token",
			Hint:    "Connect Trakt in Settings → API keys first.",
		}
	}
	raw, err := c.get(ctx, "/users/me/watchlist/"+mediaType+"?limit=500", token, "")
	if err != nil {
		return nil, err
	}
	var data []traktEntryRaw
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	singular := "show"
	if mediaType == "movies" {
		singular = "movie"
	}
	out := make([]TraktEntry, 0, len(data))
	for i := range data {
		// JS picks e[mediaType === 'movies' ? 'movie' : 'show'].
		var m *traktMediaRaw
		if mediaType == "movies" {
			m = data[i].Movie
		} else {
			m = data[i].Show
		}
		if m == nil {
			m = &traktMediaRaw{}
		}
		e := TraktEntry{
			Title:     m.Title,
			Year:      m.Year,
			TMDBID:    idString(m.IDs.TMDB),
			IMDBID:    m.IDs.IMDB,
			MediaType: singular,
			watchlist: true,
		}
		if e.Title != "" {
			out = append(out, e)
		}
	}
	return out, nil
}

// ---- user lists (v1.14 list import) --------------------------------------
//
// Public Trakt lists are readable with the client ID alone — no OAuth
// token (Trakt serves public list metadata + items to any app key).
// The importer therefore tries token "" first; when that fails (a
// private list), the API layer retries with the connected account's
// token and reports which credential actually read the list
// (authUsed), so the UI never implies a public read that was really
// an account read.

// TraktListEntry is one item of a user list, in list order. Rank is
// the list's own ordering (1-based); entries arrive pre-sorted by it.
type TraktListEntry struct {
	Rank      int
	Title     string
	Year      *int64
	TMDBID    string
	IMDBID    string
	MediaType string // the item's own type: "movie" | "show" | "season" | "episode" | "person" | ...
}

// TraktUserList is one user's list: display metadata + its items.
type TraktUserList struct {
	Name      string
	ItemCount int // the server's own count; may exceed len(Entries)
	Entries   []TraktListEntry
}

type traktListMetaRaw struct {
	Name      string `json:"name"`
	ItemCount int    `json:"item_count"`
}

type traktListItemRaw struct {
	Rank int    `json:"rank"`
	Type string `json:"type"`
	traktEntryRaw
}

// UserList reads a user's list: metadata first (validates the list
// exists and yields its display name), then its items in rank order.
// token may be "" for a public read.
func (c *TraktClient) UserList(ctx context.Context, user, listRef, token string) (*TraktUserList, error) {
	if c.clientID == "" {
		return nil, c.noClientID()
	}
	base := "/users/" + url.PathEscape(user) + "/lists/" + url.PathEscape(listRef)
	raw, err := c.get(ctx, base, token, "")
	if err != nil {
		return nil, err
	}
	var meta traktListMetaRaw
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, err
	}
	raw, err = c.get(ctx, base+"/items?limit=1000", token, "")
	if err != nil {
		return nil, err
	}
	var items []traktListItemRaw
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	out := &TraktUserList{Name: meta.Name, ItemCount: meta.ItemCount, Entries: []TraktListEntry{}}
	for i := range items {
		m := items[i].media()
		out.Entries = append(out.Entries, TraktListEntry{
			Rank:      items[i].Rank,
			Title:     m.Title,
			Year:      m.Year,
			TMDBID:    idString(m.IDs.TMDB),
			IMDBID:    m.IDs.IMDB,
			MediaType: items[i].Type,
		})
	}
	return out, nil
}

var traktListURLRe = regexp.MustCompile(`trakt\.tv/users/([^/]+)/lists/([^/?#]+)`)

// ParseTraktListRef accepts a list URL
// ("https://trakt.tv/users/{user}/lists/{slug-or-id}") or the short
// "{user}/{list}" form.
func ParseTraktListRef(ref string) (user, list string, ok bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", false
	}
	if m := traktListURLRe.FindStringSubmatch(ref); m != nil {
		return m[1], m[2], true
	}
	parts := strings.Split(ref, "/")
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], parts[1], true
	}
	return "", "", false
}
