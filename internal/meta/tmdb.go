package meta

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TMDb client — port of src/metadata/tmdb.js. See that file's header for
// the full rationale. The short version:
//
//   - Fetches FACTS (runtime, ratings, genres, franchise membership,
//     languages, poster paths, top-rated/trending lists) for items the owner
//     already owns. A failed lookup is an error, never invented data.
//   - Rate limit: TMDb allows ~40 requests / 10 seconds. The pacing below
//     caps at ~35/10s AND enforces a 300ms gap between calls. The 30-day
//     JSON cache (Cache, namespace 'tmdb') is the real protection.
//   - Auth: v3 `api_key` query param OR v4 Read Access Token as Bearer.
//     A key starting with "eyJ" (a JWT) goes in the Authorization header,
//     anything else goes in ?api_key=.
//
// VERIFICATION STATUS: UNVERIFIED live (same as the JS).

const tmdbBase = "https://api.themoviedb.org/3"

// Pacing constants — the JS names, kept verbatim.
const (
	PACING_MAX        = 35
	PACING_WINDOW_MS  = 10_000
	PACING_MIN_GAP_MS = 300
)

// Idiomatic aliases for the JS pacing constants.
const (
	PacingMax    = PACING_MAX
	PacingWindow = PACING_WINDOW_MS * time.Millisecond
	PacingMinGap = PACING_MIN_GAP_MS * time.Millisecond
)

// Genre is one TMDb genre entry: {id, name}.
type Genre struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// CollectionRef is TMDb's belongs_to_collection, normalized by the JS to
// {id: string, name: string}.
type CollectionRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Details mirrors the JS normalized movieDetails()/tvDetails() returns
// EXACTLY (camelCase field names: voteAverage, belongsToCollection,
// originalLanguage, numberOfSeasons, …). Movie and TV objects carry
// different key sets in JS, so MarshalJSON emits exactly the JS shape
// for the client's MediaType:
//
//	movie: tmdbId, mediaType, title, belongsToCollection, genres,
//	       runtime, voteAverage, popularity, originalLanguage,
//	       spokenLanguages, posterPath, releaseDate
//	tv:    tmdbId, mediaType, title, belongsToCollection, genres,
//	       runtime, episodeRunTime, voteAverage, popularity,
//	       originalLanguage, spokenLanguages, posterPath,
//	       numberOfSeasons, firstAirDate
type Details struct {
	TMDBID              string         `json:"tmdbId"`
	MediaType           string         `json:"mediaType"`
	Title               string         `json:"title"`
	BelongsToCollection *CollectionRef `json:"belongsToCollection"`
	Genres              []Genre        `json:"genres"`
	Runtime             *int64         `json:"runtime"`
	EpisodeRunTime      []int64        `json:"episodeRunTime"`
	VoteAverage         *float64       `json:"voteAverage"`
	Popularity          *float64       `json:"popularity"`
	OriginalLanguage    string         `json:"originalLanguage"`
	SpokenLanguages     []string       `json:"spokenLanguages"`
	PosterPath          string         `json:"posterPath"`
	ReleaseDate         string         `json:"releaseDate"`
	NumberOfSeasons     *int64         `json:"numberOfSeasons"`
	FirstAirDate        string         `json:"firstAirDate"`
}

// MarshalJSON emits the exact JS key set for the media type (see the
// Details doc comment), in JS key order. Value receiver so both Details
// and *Details marshal identically.
func (d Details) MarshalJSON() ([]byte, error) {
	genres := d.Genres
	if genres == nil {
		genres = []Genre{}
	}
	spoken := d.SpokenLanguages
	if spoken == nil {
		spoken = []string{}
	}
	if d.MediaType == "tv" {
		runtime := d.EpisodeRunTime
		if runtime == nil {
			runtime = []int64{}
		}
		return json.Marshal(struct {
			TMDBID              string         `json:"tmdbId"`
			MediaType           string         `json:"mediaType"`
			Title               string         `json:"title"`
			BelongsToCollection *CollectionRef `json:"belongsToCollection"`
			Genres              []Genre        `json:"genres"`
			Runtime             *int64         `json:"runtime"`
			EpisodeRunTime      []int64        `json:"episodeRunTime"`
			VoteAverage         *float64       `json:"voteAverage"`
			Popularity          *float64       `json:"popularity"`
			OriginalLanguage    string         `json:"originalLanguage"`
			SpokenLanguages     []string       `json:"spokenLanguages"`
			PosterPath          string         `json:"posterPath"`
			NumberOfSeasons     *int64         `json:"numberOfSeasons"`
			FirstAirDate        string         `json:"firstAirDate"`
		}{
			TMDBID: d.TMDBID, MediaType: d.MediaType, Title: d.Title,
			BelongsToCollection: d.BelongsToCollection, Genres: genres,
			Runtime: nil, EpisodeRunTime: runtime,
			VoteAverage: d.VoteAverage, Popularity: d.Popularity,
			OriginalLanguage: d.OriginalLanguage, SpokenLanguages: spoken,
			PosterPath: d.PosterPath, NumberOfSeasons: d.NumberOfSeasons,
			FirstAirDate: d.FirstAirDate,
		})
	}
	return json.Marshal(struct {
		TMDBID              string         `json:"tmdbId"`
		MediaType           string         `json:"mediaType"`
		Title               string         `json:"title"`
		BelongsToCollection *CollectionRef `json:"belongsToCollection"`
		Genres              []Genre        `json:"genres"`
		Runtime             *int64         `json:"runtime"`
		VoteAverage         *float64       `json:"voteAverage"`
		Popularity          *float64       `json:"popularity"`
		OriginalLanguage    string         `json:"originalLanguage"`
		SpokenLanguages     []string       `json:"spokenLanguages"`
		PosterPath          string         `json:"posterPath"`
		ReleaseDate         string         `json:"releaseDate"`
	}{
		TMDBID: d.TMDBID, MediaType: d.MediaType, Title: d.Title,
		BelongsToCollection: d.BelongsToCollection, Genres: genres,
		Runtime: d.Runtime, VoteAverage: d.VoteAverage, Popularity: d.Popularity,
		OriginalLanguage: d.OriginalLanguage, SpokenLanguages: spoken,
		PosterPath: d.PosterPath, ReleaseDate: d.ReleaseDate,
	})
}

// CollectionPart is one entry of CollectionParts.Parts:
// {tmdbId, title, releaseDate, posterPath} in the JS.
type CollectionPart struct {
	TMDBID      string `json:"tmdbId"`
	Title       string `json:"title"`
	ReleaseDate string `json:"releaseDate"`
	PosterPath  string `json:"posterPath"`
}

// CollectionParts mirrors the JS collectionParts() return:
// {id, name, parts}.
type CollectionParts struct {
	ID    string           `json:"id"`
	Name  string           `json:"name"`
	Parts []CollectionPart `json:"parts"`
}

// ListEntry is one entry of the JS list returns
// ({tmdbId, title, voteAverage, popularity, posterPath, releaseDate,
// mediaType}) AND of the JS search returns ({tmdbId, title,
// matchedByTitle: true}) — the port contract returns *ListEntry from the
// search methods too, so MarshalJSON emits the search shape when
// MatchedByTitle is set and the list shape otherwise.
type ListEntry struct {
	TMDBID         string   `json:"tmdbId"`
	Title          string   `json:"title"`
	VoteAverage    *float64 `json:"voteAverage"`
	Popularity     *float64 `json:"popularity"`
	PosterPath     string   `json:"posterPath"`
	ReleaseDate    string   `json:"releaseDate"`
	MediaType      string   `json:"mediaType"`
	MatchedByTitle bool     `json:"matchedByTitle,omitempty"`
}

// MarshalJSON emits the JS search shape for search results and the JS
// list shape for list results (see the ListEntry doc comment).
func (e ListEntry) MarshalJSON() ([]byte, error) {
	if e.MatchedByTitle {
		return json.Marshal(struct {
			TMDBID         string `json:"tmdbId"`
			Title          string `json:"title"`
			MatchedByTitle bool   `json:"matchedByTitle"`
		}{TMDBID: e.TMDBID, Title: e.Title, MatchedByTitle: true})
	}
	type listAlias ListEntry
	return json.Marshal(listAlias(e))
}

// ExternalIDs mirrors the JS tvExternalIds() return:
// {tmdbId, tvdbId, imdbId} — all strings.
type ExternalIDs struct {
	TMDBID string `json:"tmdbId"`
	TVDBID string `json:"tvdbId"`
	IMDBID string `json:"imdbId"`
}

// IDs is the ParseGuids output. The JS returns string values keyed
// tmdb/imdb/tvdb; the port contract fixes TMDB as int64 (a non-numeric
// tmdb guid yields 0), while IMDB/TVDB stay strings.
type IDs struct {
	TMDB int64  `json:"tmdb"`
	IMDB string `json:"imdb"`
	TVDB string `json:"tvdb"`
}

var guidRe = regexp.MustCompile(`^([a-z]+)://(.+)$`)

// ParseGuids parses Plex Guid strings (["tmdb://550", "tvdb://123",
// "imdb://tt0133093"]) into IDs. First occurrence wins — Plex lists the
// primary guid first; a later duplicate must not shadow it (JS behaviour).
// The TMDb join depends on this — the Advisor never fuzzy-matches titles
// for the join, it parses the guid (JS comment, carried over).
func ParseGuids(guids []string) IDs {
	found := map[string]string{}
	for _, g := range guids {
		m := guidRe.FindStringSubmatch(strings.TrimSpace(g))
		if m == nil {
			continue
		}
		if _, ok := found[m[1]]; !ok {
			found[m[1]] = m[2]
		}
	}
	var out IDs
	if s, ok := found["tmdb"]; ok {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			out.TMDB = n
		}
	}
	out.IMDB = found["imdb"]
	out.TVDB = found["tvdb"]
	return out
}

// TMDbClient is the Go port of the JS TMDbClient.
type TMDbClient struct {
	apiKey     string
	cache      Cache
	baseURL    string
	httpClient *http.Client

	mu           sync.Mutex
	requestTimes []time.Time
}

// NewTMDb builds a TMDb client.
//
// ADAPTATION (flagged): the JS constructor THREW TmdbError when no key
// was configured. This Go signature cannot return an error, so an empty
// key is stored and every method returns the same *Error the JS threw
// ("No TMDb API key configured" + the Settings hint) before any I/O.
func NewTMDb(apiKey string, cache Cache) *TMDbClient {
	return &TMDbClient{
		apiKey:     apiKey,
		cache:      cache,
		baseURL:    tmdbBase,
		httpClient: &http.Client{},
	}
}

// UseBearer reports whether the key is used as a v4 Bearer token: v4
// Read Access Tokens are JWTs (start with "eyJ"); v3 keys are 32-char
// hex. Heuristic, documented in the JS so the next AI doesn't "fix" it.
func (c *TMDbClient) UseBearer() bool {
	return strings.HasPrefix(c.apiKey, "eyJ")
}

// pace enforces PACING_MAX requests per PACING_WINDOW_MS plus a
// PACING_MIN_GAP_MS gap between consecutive requests — the JS _pace(),
// including its +100ms window overshoot. Request timestamps are recorded
// in get() after each fetch, exactly where the JS pushed them.
func (c *TMDbClient) pace(ctx context.Context) error {
	window := time.Duration(PACING_WINDOW_MS) * time.Millisecond
	minGap := time.Duration(PACING_MIN_GAP_MS) * time.Millisecond
	for {
		c.mu.Lock()
		now := time.Now()
		kept := c.requestTimes[:0]
		for _, t := range c.requestTimes {
			if now.Sub(t) < window {
				kept = append(kept, t)
			}
		}
		c.requestTimes = kept
		if len(c.requestTimes) >= PACING_MAX {
			wait := window - now.Sub(c.requestTimes[0]) + 100*time.Millisecond
			c.mu.Unlock()
			if err := sleepCtx(ctx, wait); err != nil {
				return err
			}
			continue
		}
		if n := len(c.requestTimes); n > 0 {
			if last := c.requestTimes[n-1]; now.Sub(last) < minGap {
				wait := minGap - now.Sub(last)
				c.mu.Unlock()
				if err := sleepCtx(ctx, wait); err != nil {
					return err
				}
				continue
			}
		}
		c.mu.Unlock()
		return nil
	}
}

// get is the JS _get(): cache first (namespace 'tmdb'), paced fetch,
// raw API JSON returned (and cached) for the caller to normalize —
// exactly the JS split.
func (c *TMDbClient) get(ctx context.Context, path string, params url.Values, useCache bool, cacheKey string) (json.RawMessage, error) {
	if c.apiKey == "" {
		return nil, &Error{
			Message: "No TMDb API key configured",
			Hint:    "Paste your TMDb key in Settings → API keys.",
		}
	}
	if useCache && cacheKey != "" {
		if hit, ok := cacheGet(c.cache, "tmdb", cacheKey); ok {
			return hit, nil
		}
	}
	if err := c.pace(ctx); err != nil {
		return nil, err
	}
	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	for k, vs := range params {
		for _, v := range vs {
			q.Set(k, v)
		}
	}
	if !c.UseBearer() {
		q.Set("api_key", c.apiKey)
	}
	u.RawQuery = q.Encode()

	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if c.UseBearer() {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &Error{
			Message: fmt.Sprintf("Could not reach TMDb (%s)", err.Error()),
			Hint:    "Check internet access.",
		}
	}
	defer res.Body.Close()
	// JS records the request time once fetch resolves, success or HTTP error.
	c.mu.Lock()
	c.requestTimes = append(c.requestTimes, time.Now())
	c.mu.Unlock()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		msg := fmt.Sprintf("TMDb returned HTTP %d", res.StatusCode)
		if len(body) > 0 {
			msg += ": " + bodySnippet(body, 160)
		}
		hint := ""
		if res.StatusCode == 401 {
			hint = "TMDb rejected the API key — check it in Settings → API keys."
		}
		return nil, &Error{Message: msg, Status: res.StatusCode, Hint: hint}
	}
	if useCache && cacheKey != "" {
		cacheSet(c.cache, "tmdb", cacheKey, json.RawMessage(body))
	}
	return json.RawMessage(body), nil
}

// ---- raw TMDb API shapes (pre-normalization) ---------------------------

type tmdbGenreRaw struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type tmdbSpokenRaw struct {
	ISO string `json:"iso_639_1"`
}

type tmdbDetailsRaw struct {
	Title         string `json:"title"`
	Name          string `json:"name"`
	BelongsToColl *struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"belongs_to_collection"`
	Genres           []tmdbGenreRaw  `json:"genres"`
	Runtime          *int64          `json:"runtime"`
	EpisodeRunTime   []int64         `json:"episode_run_time"`
	VoteAverage      *float64        `json:"vote_average"`
	Popularity       *float64        `json:"popularity"`
	OriginalLanguage string          `json:"original_language"`
	SpokenLanguages  []tmdbSpokenRaw `json:"spoken_languages"`
	PosterPath       string          `json:"poster_path"`
	ReleaseDate      string          `json:"release_date"`
	NumberOfSeasons  *int64          `json:"number_of_seasons"`
	FirstAirDate     string          `json:"first_air_date"`
}

func normalizeGenres(in []tmdbGenreRaw) []Genre {
	out := make([]Genre, 0, len(in))
	for _, g := range in {
		out = append(out, Genre{ID: g.ID, Name: g.Name})
	}
	return out
}

func normalizeSpoken(in []tmdbSpokenRaw) []string {
	out := make([]string, 0, len(in))
	for _, l := range in {
		if l.ISO != "" {
			out = append(out, l.ISO)
		}
	}
	return out
}

// MovieDetails is the JS movieDetails(): normalized facts for one movie.
func (c *TMDbClient) MovieDetails(ctx context.Context, id int64) (*Details, error) {
	raw, err := c.get(ctx, fmt.Sprintf("/movie/%d", id), nil, true, fmt.Sprintf("movie/%d", id))
	if err != nil {
		return nil, err
	}
	var data tmdbDetailsRaw
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	out := &Details{
		TMDBID:           strconv.FormatInt(id, 10),
		MediaType:        "movie",
		Title:            data.Title,
		Genres:           normalizeGenres(data.Genres),
		Runtime:          data.Runtime,
		VoteAverage:      data.VoteAverage,
		Popularity:       data.Popularity,
		OriginalLanguage: data.OriginalLanguage,
		SpokenLanguages:  normalizeSpoken(data.SpokenLanguages),
		PosterPath:       data.PosterPath,
		ReleaseDate:      data.ReleaseDate,
	}
	if data.BelongsToColl != nil {
		out.BelongsToCollection = &CollectionRef{
			ID:   strconv.FormatInt(data.BelongsToColl.ID, 10),
			Name: data.BelongsToColl.Name,
		}
	}
	return out, nil
}

// TVDetails is the JS tvDetails(): normalized facts for one show.
func (c *TMDbClient) TVDetails(ctx context.Context, id int64) (*Details, error) {
	raw, err := c.get(ctx, fmt.Sprintf("/tv/%d", id), nil, true, fmt.Sprintf("tv/%d", id))
	if err != nil {
		return nil, err
	}
	var data tmdbDetailsRaw
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	episodeRunTime := data.EpisodeRunTime
	if episodeRunTime == nil {
		episodeRunTime = []int64{}
	}
	return &Details{
		TMDBID:           strconv.FormatInt(id, 10),
		MediaType:        "tv",
		Title:            data.Name,
		Genres:           normalizeGenres(data.Genres),
		EpisodeRunTime:   episodeRunTime,
		VoteAverage:      data.VoteAverage,
		Popularity:       data.Popularity,
		OriginalLanguage: data.OriginalLanguage,
		SpokenLanguages:  normalizeSpoken(data.SpokenLanguages),
		PosterPath:       data.PosterPath,
		NumberOfSeasons:  data.NumberOfSeasons,
		FirstAirDate:     data.FirstAirDate,
		// BelongsToCollection stays nil and Runtime stays nil for TV,
		// exactly as the JS returns them.
	}, nil
}

// TVStatus maps a show's TMDb status onto the Kometa status-overlay
// vocabulary ("Returning" | "Ended" | "Canceled" | "Airing") for the
// Overlay Studio facts (v2). Returns "" when TMDb has no mappable status
// — the caller exposes null and the badge simply never fires (never
// guess a status). Reuses the TVDetails cache entry ('tv/<id>'), so a
// scan that already fetched the show costs no extra request.
//
// Mapping: TMDb "Ended" → Ended, "Canceled" → Canceled (TMDb's own US
// spelling), "Returning Series" → "Airing" when a next episode is
// scheduled OR the last episode aired within 30 days, else "Returning".
// FUTURE AI: the 30-day "currently airing" window mirrors Kometa's
// Airing-vs-Returning split conceptually; Kometa's exact internal
// threshold is UNVERIFIED — 30 days is a documented choice, not a claim.
// "In Production" / "Planned" / "Pilot" map to "" on purpose: an
// unaired show is none of the four overlay states.
func (c *TMDbClient) TVStatus(ctx context.Context, id int64) (string, error) {
	raw, err := c.get(ctx, fmt.Sprintf("/tv/%d", id), nil, true, fmt.Sprintf("tv/%d", id))
	if err != nil {
		return "", err
	}
	var data struct {
		Status      string          `json:"status"`
		NextEpisode json.RawMessage `json:"next_episode_to_air"`
		LastEpisode *struct {
			AirDate string `json:"air_date"`
		} `json:"last_episode_to_air"`
		LastAirDate string `json:"last_air_date"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", err
	}
	switch data.Status {
	case "Ended":
		return "Ended", nil
	case "Canceled":
		return "Canceled", nil
	case "Returning Series":
		if len(data.NextEpisode) > 0 && string(data.NextEpisode) != "null" {
			return "Airing", nil
		}
		last := data.LastAirDate
		if data.LastEpisode != nil && data.LastEpisode.AirDate != "" {
			last = data.LastEpisode.AirDate
		}
		if t, perr := time.Parse("2006-01-02", last); perr == nil {
			if time.Since(t) >= 0 && time.Since(t) <= 30*24*time.Hour {
				return "Airing", nil
			}
		}
		return "Returning", nil
	}
	return "", nil
}

// WatchProviders fetches TMDb's /{mediaType}/{id}/watch/providers
// (mediaType "movie" | "tv") and returns the FLATRATE (subscription)
// provider names for EVERY region TMDb answers, keyed by region code:
// { "US": ["Netflix", "Disney Plus"], … }. Facts are layout-
// independent, so the region choice happens later, in the overlay
// evaluator — this returns the whole parsed map from the one (cached)
// response. Names keep TMDb's order (their ranking) with duplicates
// removed; regions with no flatrate providers are omitted. Rent/buy
// listings are NOT streaming availability and are never read.
// Display-name cosmetics ("Disney Plus" → "Disney+") live in the
// evaluator, not here — this is the wire truth.
func (c *TMDbClient) WatchProviders(ctx context.Context, id int64, mediaType string) (map[string][]string, error) {
	if mediaType != "movie" && mediaType != "tv" {
		return nil, &Error{Message: fmt.Sprintf("WatchProviders: unsupported media type %q", mediaType)}
	}
	path := fmt.Sprintf("/%s/%d/watch/providers", mediaType, id)
	raw, err := c.get(ctx, path, nil, true, fmt.Sprintf("providers/%s/%d", mediaType, id))
	if err != nil {
		return nil, err
	}
	var data struct {
		Results map[string]struct {
			Flatrate []struct {
				ProviderName string `json:"provider_name"`
			} `json:"flatrate"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for region, entry := range data.Results {
		names := []string{}
		seen := map[string]bool{}
		for _, p := range entry.Flatrate {
			name := strings.TrimSpace(p.ProviderName)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
		if len(names) > 0 {
			out[region] = names
		}
	}
	return out, nil
}

// CollectionParts is the JS collectionParts(): "you own 5 of 6" data —
// every part TMDb knows in the franchise.
func (c *TMDbClient) CollectionParts(ctx context.Context, id int64) (*CollectionParts, error) {
	raw, err := c.get(ctx, fmt.Sprintf("/collection/%d", id), nil, true, fmt.Sprintf("collection/%d", id))
	if err != nil {
		return nil, err
	}
	var data struct {
		Name  string `json:"name"`
		Parts []struct {
			ID          int64  `json:"id"`
			Title       string `json:"title"`
			ReleaseDate string `json:"release_date"`
			PosterPath  string `json:"poster_path"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	out := &CollectionParts{
		ID:    strconv.FormatInt(id, 10),
		Name:  data.Name,
		Parts: make([]CollectionPart, 0, len(data.Parts)),
	}
	for _, p := range data.Parts {
		out.Parts = append(out.Parts, CollectionPart{
			TMDBID:      strconv.FormatInt(p.ID, 10),
			Title:       p.Title,
			ReleaseDate: p.ReleaseDate,
			PosterPath:  p.PosterPath,
		})
	}
	return out, nil
}

// SagaPosition answers the Saga-position family's question: where
// does THIS movie sit in its TMDb collection, counted by release
// date? parts is a CollectionParts payload (TMDb's own order is NOT
// trusted — the sort happens here, ascending by release_date, which
// ISO dates order correctly as strings; a part with no stated date
// sorts last, among its undated peers in TMDb's stated order).
// Returns the 1-based position of tmdbID and the total part count;
// found=false when the id is not among the parts (TMDb can name a
// collection on a movie its parts list doesn't carry — the family
// hides on found=false rather than inventing a position).
func SagaPosition(parts []CollectionPart, tmdbID int64) (position, total int, found bool) {
	total = len(parts)
	if total == 0 {
		return 0, 0, false
	}
	sorted := make([]CollectionPart, total)
	copy(sorted, parts)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i].ReleaseDate, sorted[j].ReleaseDate
		if (a == "") != (b == "") {
			return b == "" // a stated date always precedes an unstated one
		}
		return a < b
	})
	want := strconv.FormatInt(tmdbID, 10)
	for i, p := range sorted {
		if p.TMDBID == want {
			return i + 1, total, true
		}
	}
	return 0, total, false
}

// OriginFacts is the TMDb identity data the Franchise and Origin
// overlay families judge on — read from the SAME details response
// MovieDetails/TVDetails/TVStatus consume (same cache keys), so a
// title whose details are already cached costs no extra request.
// Every field is exactly what TMDb states; empty means TMDb stated
// nothing, and the families hide — nothing is derived or guessed.
type OriginFacts struct {
	CollectionName   string   // belongs_to_collection.name (movies only; "" = standalone or unstated)
	CollectionID     int64    // belongs_to_collection.id (movies only; 0 = standalone or unstated) — the SagaPosition lookup key
	OriginalLanguage string   // original_language, ISO 639-1 ("ja"); "" = unstated
	OriginCountries  []string // origin_country — production_countries fallback (movies) — ISO 3166-1 alpha-2, TMDb order
}

// TitleOrigin fetches a movie's or show's collection membership and
// origin data (mediaType "movie" | "tv"). TV records carry no
// belongs_to_collection, so CollectionName is always "" for shows —
// the Franchise family is a movie fact by construction, not by a
// special case downstream. A movie with no origin_country falls back
// to its production_countries (the older field TMDb populated first);
// the values are TMDb's own country codes either way.
func (c *TMDbClient) TitleOrigin(ctx context.Context, id int64, mediaType string) (*OriginFacts, error) {
	if mediaType != "movie" && mediaType != "tv" {
		return nil, &Error{Message: fmt.Sprintf("TitleOrigin: unsupported media type %q", mediaType)}
	}
	path := fmt.Sprintf("/%s/%d", mediaType, id)
	raw, err := c.get(ctx, path, nil, true, fmt.Sprintf("%s/%d", mediaType, id))
	if err != nil {
		return nil, err
	}
	var data struct {
		BelongsToColl *struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"belongs_to_collection"`
		OriginalLanguage    string   `json:"original_language"`
		OriginCountry       []string `json:"origin_country"`
		ProductionCountries []struct {
			ISO string `json:"iso_3166_1"`
		} `json:"production_countries"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	out := &OriginFacts{OriginalLanguage: strings.TrimSpace(data.OriginalLanguage)}
	// Collection membership is read for movies only — a TV payload
	// is never consulted for it, so CollectionName is "" for shows
	// by construction, not by a downstream check.
	if mediaType == "movie" && data.BelongsToColl != nil {
		out.CollectionName = strings.TrimSpace(data.BelongsToColl.Name)
		out.CollectionID = data.BelongsToColl.ID
	}
	for _, cc := range data.OriginCountry {
		if cc = strings.TrimSpace(cc); cc != "" {
			out.OriginCountries = append(out.OriginCountries, cc)
		}
	}
	if len(out.OriginCountries) == 0 {
		for _, pc := range data.ProductionCountries {
			if iso := strings.TrimSpace(pc.ISO); iso != "" {
				out.OriginCountries = append(out.OriginCountries, iso)
			}
		}
	}
	return out, nil
}

// TVExternalIDs is the JS tvExternalIds(): needed for the Fanart.tv TV
// pipeline (TV needs tvdb_id). Cached aggressively — ids don't change.
func (c *TMDbClient) TVExternalIDs(ctx context.Context, id int64) (*ExternalIDs, error) {
	raw, err := c.get(ctx, fmt.Sprintf("/tv/%d/external_ids", id), nil, true, fmt.Sprintf("tv-external/%d", id))
	if err != nil {
		return nil, err
	}
	var data struct {
		TVDBID *int64 `json:"tvdb_id"`
		IMDBID string `json:"imdb_id"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	out := &ExternalIDs{TMDBID: strconv.FormatInt(id, 10), IMDBID: data.IMDBID}
	if data.TVDBID != nil {
		out.TVDBID = strconv.FormatInt(*data.TVDBID, 10)
	}
	return out, nil
}

// ---- search fallback (flagged "matched by title" by callers) -----------

func (c *TMDbClient) search(ctx context.Context, path, titleField string, title string, year *int, yearParam string) (*ListEntry, error) {
	params := url.Values{}
	params.Set("query", title)
	if year != nil && *year != 0 { // JS: `if (year)` — 0 is falsy there too
		params.Set(yearParam, strconv.Itoa(*year))
	}
	raw, err := c.get(ctx, path, params, false, "")
	if err != nil {
		return nil, err
	}
	var data struct {
		Results []struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
			Name  string `json:"name"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	if len(data.Results) == 0 {
		return nil, nil // JS returns null when there is no match
	}
	first := data.Results[0]
	name := first.Title
	if titleField == "name" {
		name = first.Name
	}
	return &ListEntry{
		TMDBID:         strconv.FormatInt(first.ID, 10),
		Title:          name,
		MatchedByTitle: true,
	}, nil
}

// SearchMovie is the JS searchMovie(). Returns (nil, nil) when TMDb has
// no match — the caller flags title-matched results honestly in the UI.
func (c *TMDbClient) SearchMovie(ctx context.Context, title string, year *int) (*ListEntry, error) {
	return c.search(ctx, "/search/movie", "title", title, year, "year")
}

// SearchTV is the JS searchTv(). Returns (nil, nil) when no match.
func (c *TMDbClient) SearchTV(ctx context.Context, title string, year *int) (*ListEntry, error) {
	return c.search(ctx, "/search/tv", "name", title, year, "first_air_date_year")
}

// ---- lists: the "top 100s and what not" ---------------------------------
//
// JS note, carried over so nobody "optimizes" this away: lists BYPASS
// the module cache and let the CALLER cache them with a 24h TTL — the
// 'tmdb' bucket's 30-day TTL is too long for trending/top lists.

type tmdbListRaw struct {
	Results []struct {
		ID           int64    `json:"id"`
		Title        string   `json:"title"`
		Name         string   `json:"name"`
		VoteAverage  *float64 `json:"vote_average"`
		Popularity   *float64 `json:"popularity"`
		PosterPath   string   `json:"poster_path"`
		ReleaseDate  string   `json:"release_date"`
		FirstAirDate string   `json:"first_air_date"`
	} `json:"results"`
}

func (c *TMDbClient) list(ctx context.Context, path string, mediaType string) ([]ListEntry, error) {
	params := url.Values{}
	params.Set("page", "1")
	raw, err := c.get(ctx, path, params, false, "")
	if err != nil {
		return nil, err
	}
	var data tmdbListRaw
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	results := data.Results
	if len(results) > 100 {
		results = results[:100]
	}
	out := make([]ListEntry, 0, len(results))
	for _, r := range results {
		title := r.Title
		if title == "" {
			title = r.Name
		}
		release := r.ReleaseDate
		if release == "" {
			release = r.FirstAirDate
		}
		out = append(out, ListEntry{
			TMDBID:      strconv.FormatInt(r.ID, 10),
			Title:       title,
			VoteAverage: r.VoteAverage,
			Popularity:  r.Popularity,
			PosterPath:  r.PosterPath,
			ReleaseDate: release,
			MediaType:   mediaType,
		})
	}
	return out, nil
}

// TopRatedMovies is the JS topRatedMovies().
func (c *TMDbClient) TopRatedMovies(ctx context.Context) ([]ListEntry, error) {
	return c.list(ctx, "/movie/top_rated", "movie")
}

// ---- user lists (v1.14 list import) --------------------------------------
//
// A user-created TMDb list (GET /list/{id}) differs from the curated
// endpoints above in two ways that matter to the importer: the items
// ride an `items` array (not `results`), and each item carries its
// OWN media_type (a list can mix movies and shows). List items carry
// no IMDb id — per-item external-id lookups are deliberately out of
// scope, so the importer matches these by TMDb id, then title+year.

type tmdbUserListRaw struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ItemCount   int    `json:"item_count"`
	Items       []struct {
		ID           int64    `json:"id"`
		Title        string   `json:"title"`
		Name         string   `json:"name"`
		MediaType    string   `json:"media_type"`
		VoteAverage  *float64 `json:"vote_average"`
		Popularity   *float64 `json:"popularity"`
		PosterPath   string   `json:"poster_path"`
		ReleaseDate  string   `json:"release_date"`
		FirstAirDate string   `json:"first_air_date"`
	} `json:"items"`
}

// UserList is one user-created TMDb list, entries in list order.
type UserList struct {
	Name        string
	Description string
	// ItemCount is the server's own count for the list. It can
	// exceed len(Entries) when the API truncates the items array —
	// the importer reports both numbers rather than hiding the gap.
	ItemCount int
	Entries   []ListEntry
}

// UserList fetches a user-created list by id (see ParseTMDbListID
// for the accepted reference forms).
func (c *TMDbClient) UserList(ctx context.Context, listID string) (*UserList, error) {
	raw, err := c.get(ctx, "/list/"+url.PathEscape(listID), url.Values{}, false, "")
	if err != nil {
		return nil, err
	}
	var data tmdbUserListRaw
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	out := &UserList{Name: data.Name, Description: data.Description, ItemCount: data.ItemCount, Entries: []ListEntry{}}
	for _, r := range data.Items {
		title := r.Title
		if title == "" {
			title = r.Name
		}
		release := r.ReleaseDate
		if release == "" {
			release = r.FirstAirDate
		}
		out.Entries = append(out.Entries, ListEntry{
			TMDBID:      strconv.FormatInt(r.ID, 10),
			Title:       title,
			VoteAverage: r.VoteAverage,
			Popularity:  r.Popularity,
			PosterPath:  r.PosterPath,
			ReleaseDate: release,
			MediaType:   r.MediaType,
		})
	}
	return out, nil
}

var tmdbListURLRe = regexp.MustCompile(`themoviedb\.org/list/(\d+)`)

// ParseTMDbListID accepts a bare numeric list id ("12345") or a list
// URL ("https://www.themoviedb.org/list/12345-some-slug").
func ParseTMDbListID(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", false
	}
	if _, err := strconv.ParseInt(ref, 10, 64); err == nil {
		return ref, true
	}
	if m := tmdbListURLRe.FindStringSubmatch(ref); m != nil {
		return m[1], true
	}
	return "", false
}

// TopRatedTV is the JS topRatedTv().
func (c *TMDbClient) TopRatedTV(ctx context.Context) ([]ListEntry, error) {
	return c.list(ctx, "/tv/top_rated", "tv")
}

// TrendingMovies is the JS trendingMovies().
func (c *TMDbClient) TrendingMovies(ctx context.Context) ([]ListEntry, error) {
	return c.list(ctx, "/trending/movie/week", "movie")
}

// TrendingTV is the JS trendingTv().
func (c *TMDbClient) TrendingTV(ctx context.Context) ([]ListEntry, error) {
	return c.list(ctx, "/trending/tv/week", "tv")
}

// PopularMovies is the JS popularMovies().
func (c *TMDbClient) PopularMovies(ctx context.Context) ([]ListEntry, error) {
	return c.list(ctx, "/movie/popular", "movie")
}

// PopularTV is the JS popularTv().
func (c *TMDbClient) PopularTV(ctx context.Context) ([]ListEntry, error) {
	return c.list(ctx, "/tv/popular", "tv")
}

// PosterURL is the JS posterUrl(): JS default size is 'w342'; Go has no
// default arguments, so an empty size means 'w342' here.
func (c *TMDbClient) PosterURL(posterPath string, size string) string {
	if posterPath == "" {
		return ""
	}
	if size == "" {
		size = "w342"
	}
	return "https://image.tmdb.org/t/p/" + size + posterPath
}
