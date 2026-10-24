package meta

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// OMDb client — port of src/metadata/omdb.js. Exactly one job: the IMDb
// rating number.
//
// IMDb has NO public API. OMDb (omdbapi.com) is the stand-in: it serves
// the actual imdbRating value. Everywhere the UI shows this number it
// must be labeled as the IMDb rating via OMDb — the Rating.Source field
// carries 'OMDb' for exactly that honesty (JS behaviour).
//
//   - Key-absent tolerant BY DESIGN: GetRating returns (nil, nil) when
//     no key is configured. Nothing may hard-require OMDb.
//   - Free tier: 1,000 requests/day. The 30-day cache (namespace 'omdb')
//     is what keeps a library scan from burning the quota.
//
// VERIFICATION STATUS: UNVERIFIED live (same as the JS).

const omdbBase = "https://www.omdbapi.com/"

// Rating mirrors the JS getRating() return:
// {imdbId, imdbRating, title, source: 'OMDb'}. IMDBRating stays a string
// — that is the type OMDb serves and the type the JS propagated.
type Rating struct {
	IMDBID     string `json:"imdbId"`
	IMDBRating string `json:"imdbRating"`
	Title      string `json:"title"`
	Source     string `json:"source"`
}

// OMDbClient is the Go port of the JS OMDbClient. It constructs happily
// with an empty key — do NOT add a key check to the constructor; key-
// absent tolerance is the whole point of this module's contract (JS).
type OMDbClient struct {
	apiKey     string
	cache      Cache
	baseURL    string
	httpClient *http.Client
}

// NewOMDb builds an OMDb client; apiKey may be "" (see the type doc).
func NewOMDb(apiKey string, cache Cache) *OMDbClient {
	return &OMDbClient{
		apiKey:     apiKey,
		cache:      cache,
		baseURL:    omdbBase,
		httpClient: &http.Client{},
	}
}

// Configured reports whether a key is set (JS `configured` getter).
func (c *OMDbClient) Configured() bool { return c.apiKey != "" }

// GetRating looks up the IMDb rating for an imdb id like "tt0133093"
// (parsed from Plex's imdb:// guid). Returns (nil, nil) for: no key, no
// id, title not found, or OMDb answering "False"/"N/A" — routine misses
// are never errors, exactly as the JS. Only transport/HTTP failures
// return an error, and callers treat those as "rating unavailable".
func (c *OMDbClient) GetRating(ctx context.Context, imdbID string) (*Rating, error) {
	if !c.Configured() || imdbID == "" {
		return nil, nil
	}
	key := "rating/" + imdbID
	if hit, ok := cacheGet(c.cache, "omdb", key); ok {
		if isJSONNull(hit) {
			return nil, nil // cached routine miss (JS cached null)
		}
		var out Rating
		if err := json.Unmarshal(hit, &out); err != nil {
			return nil, err
		}
		return &out, nil
	}
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("apikey", c.apiKey)
	q.Set("i", imdbID)
	u.RawQuery = q.Encode()

	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &Error{
			Message: fmt.Sprintf("Could not reach OMDb (%s)", err.Error()),
			Hint:    "Check internet access.",
		}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		// JS: status only, no body snippet — preserved.
		return nil, &Error{
			Message: fmt.Sprintf("OMDb returned HTTP %d", res.StatusCode),
			Status:  res.StatusCode,
		}
	}
	var data struct {
		Response   string `json:"Response"`
		IMDBRating string `json:"imdbRating"`
		Title      string `json:"Title"`
	}
	// JS tolerated an unparseable body as {} (→ routine miss below).
	_ = json.Unmarshal(body, &data)
	if data.Response == "False" || data.IMDBRating == "" || data.IMDBRating == "N/A" {
		cacheSetNull(c.cache, "omdb", key)
		return nil, nil
	}
	out := &Rating{IMDBID: imdbID, IMDBRating: data.IMDBRating, Title: data.Title, Source: "OMDb"}
	cacheSetValue(c.cache, "omdb", key, out)
	return out, nil
}

// OMDbDetails is the extended OMDb record the Overlay Studio ratings chain
// needs (facts v2): the IMDb number PLUS the Rotten Tomatoes tomatometer
// and the Metascore, all from the SAME /?i= response's Ratings[] array.
// Pointer fields: nil means OMDb genuinely has no value — never zero-
// filled, because the overlay rules treat "no value" as "badge hidden".
// Awards is the response's own awards SENTENCE ("Won 2 Oscars. 45 wins
// & 32 nominations total") carried verbatim — the ribbon's awards
// honor reads it literally; "N/A" normalizes to "".
type OMDbDetails struct {
	IMDBRating *float64 `json:"imdbRating"`
	RTRating   *float64 `json:"rtRating"` // 0–100 (tomatometer percent)
	Metascore  *int     `json:"metascore"`
	Awards     string   `json:"awards"`
}

// GetDetails fetches the full OMDb record for an imdb id. Same tolerance
// contract as GetRating: (nil, nil) for no key / no id / not found / all
// values "N/A"; errors only for transport/HTTP failures. Cached under
// its own key ('details/…') so the Rating cache shape is untouched.
//
// FUTURE AI: OMDb's Ratings[] carries RT as a PERCENT STRING ("87%") and
// Metascore as a bare number string ("74") at the top level. Parse
// defensively — any unparseable member is simply absent from the result.
func (c *OMDbClient) GetDetails(ctx context.Context, imdbID string) (*OMDbDetails, error) {
	if !c.Configured() || imdbID == "" {
		return nil, nil
	}
	key := "details/" + imdbID
	if hit, ok := cacheGet(c.cache, "omdb", key); ok {
		if isJSONNull(hit) {
			return nil, nil
		}
		var out OMDbDetails
		if err := json.Unmarshal(hit, &out); err != nil {
			return nil, err
		}
		return &out, nil
	}
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("apikey", c.apiKey)
	q.Set("i", imdbID)
	u.RawQuery = q.Encode()

	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &Error{
			Message: fmt.Sprintf("Could not reach OMDb (%s)", err.Error()),
			Hint:    "Check internet access.",
		}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, &Error{
			Message: fmt.Sprintf("OMDb returned HTTP %d", res.StatusCode),
			Status:  res.StatusCode,
		}
	}
	var data struct {
		Response   string `json:"Response"`
		IMDBRating string `json:"imdbRating"`
		Metascore  string `json:"Metascore"`
		Awards     string `json:"Awards"`
		Ratings    []struct {
			Source string `json:"Source"`
			Value  string `json:"Value"`
		} `json:"Ratings"`
	}
	_ = json.Unmarshal(body, &data)
	if data.Response == "False" {
		cacheSetNull(c.cache, "omdb", key)
		return nil, nil
	}
	out := &OMDbDetails{}
	if f, ferr := strconv.ParseFloat(data.IMDBRating, 64); ferr == nil {
		out.IMDBRating = &f
	}
	for _, r := range data.Ratings {
		if r.Source == "Rotten Tomatoes" {
			if f, ferr := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(r.Value), "%"), 64); ferr == nil {
				out.RTRating = &f
			}
		}
	}
	if n, nerr := strconv.Atoi(strings.TrimSpace(data.Metascore)); nerr == nil {
		out.Metascore = &n
	}
	// The awards sentence counts toward "this record has something":
	// a title whose ratings are all N/A but which HAS an awards line
	// is still a real record (the awards ribbon needs it).
	if awards := strings.TrimSpace(data.Awards); awards != "" && awards != "N/A" {
		out.Awards = awards
	}
	if out.IMDBRating == nil && out.RTRating == nil && out.Metascore == nil && out.Awards == "" {
		cacheSetNull(c.cache, "omdb", key)
		return nil, nil
	}
	cacheSetValue(c.cache, "omdb", key, out)
	return out, nil
}
