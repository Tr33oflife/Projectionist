package meta

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// Fanart.tv client — port of src/metadata/fanart.js. Transparent logos
// for the overlay studio's "Logos" tray.
//
//   - Movies: GET /v3/movies/{tmdb_id} → hdmovielogo / movielogo /
//     movieclearart. Keyed by TMDb id — no extra hop.
//   - TV: GET /v3/tv/{tvdb_id} → hdtvlogo / clearart / tvthumb. TV needs
//     the TVDB id, resolved via TMDb's TVExternalIDs first.
//   - Key-absent tolerant BY DESIGN (same contract as OMDb): not
//     configured → every lookup returns an empty slice, the Logos tray
//     hides, nothing breaks, and NO HTTP call is made.
//   - 30-day cache (namespace 'fanart') — logos don't change.
//
// VERIFICATION STATUS: UNVERIFIED live (same as the JS). Routine misses
// (including HTTP 404 — no art for this id) return an empty slice.

const fanartBase = "https://webservice.fanart.tv/v3"

// Logo is one normalized Fanart entry: {url, kind, lang, likes} in JS.
type Logo struct {
	URL   string `json:"url"`
	Kind  string `json:"kind"`
	Lang  string `json:"lang"`
	Likes int64  `json:"likes"`
}

// FanartClient is the Go port of the JS FanartClient. It constructs
// happily with an empty key (key-absent tolerance is the contract).
type FanartClient struct {
	apiKey     string
	cache      Cache
	baseURL    string
	httpClient *http.Client
}

// NewFanart builds a Fanart client; apiKey may be "" (see the type doc).
func NewFanart(apiKey string, cache Cache) *FanartClient {
	return &FanartClient{
		apiKey:     apiKey,
		cache:      cache,
		baseURL:    fanartBase,
		httpClient: &http.Client{},
	}
}

// Configured reports whether a key is set (JS `configured` getter).
func (c *FanartClient) Configured() bool { return c.apiKey != "" }

// get is the JS _get(): raw API object for path, or (nil, nil) when
// unconfigured or the id has no art (404).
func (c *FanartClient) get(ctx context.Context, path string, cacheKey string) (json.RawMessage, error) {
	if !c.Configured() {
		return nil, nil
	}
	if hit, ok := cacheGet(c.cache, "fanart", cacheKey); ok {
		return hit, nil
	}
	u := c.baseURL + path + "?api_key=" + url.QueryEscape(c.apiKey)
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &Error{Message: fmt.Sprintf("Could not reach Fanart.tv (%s)", err.Error())}
	}
	defer res.Body.Close()
	if res.StatusCode == 404 {
		return nil, nil // no art for this id — routine miss (JS)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		// JS: status only, no body snippet — preserved.
		return nil, &Error{
			Message: fmt.Sprintf("Fanart.tv returned HTTP %d", res.StatusCode),
			Status:  res.StatusCode,
		}
	}
	cacheSet(c.cache, "fanart", cacheKey, json.RawMessage(body))
	return json.RawMessage(body), nil
}

type fanartEntryRaw struct {
	URL   string          `json:"url"`
	Lang  string          `json:"lang"`
	Likes json.RawMessage `json:"likes"`
}

// parseLikes is JS Number(e.likes || 0): Fanart serves likes as a
// numeric string; a JSON number is accepted too. Anything else → 0.
func parseLikes(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return int64(f)
		}
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return int64(f)
	}
	return 0
}

// pick is the JS FanartClient._pick(): flatten the given kinds into one
// list, most-liked first (stable — JS Array.sort is stable).
func pick(data json.RawMessage, kinds []string) []Logo {
	out := []Logo{}
	if len(data) == 0 {
		return out
	}
	var obj map[string][]fanartEntryRaw
	if err := json.Unmarshal(data, &obj); err != nil {
		return out
	}
	for _, kind := range kinds {
		for _, e := range obj[kind] {
			if e.URL == "" {
				continue
			}
			out = append(out, Logo{URL: e.URL, Kind: kind, Lang: e.Lang, Likes: parseLikes(e.Likes)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Likes > out[j].Likes })
	return out
}

// MovieLogos is the JS movieLogos(tmdbId): logo/clearart candidates for
// a numeric TMDb movie id. Empty slice (never an error) when
// unconfigured, the id is 0, or Fanart has no art for it.
func (c *FanartClient) MovieLogos(ctx context.Context, tmdbID int64) ([]Logo, error) {
	if tmdbID == 0 {
		return []Logo{}, nil
	}
	data, err := c.get(ctx, fmt.Sprintf("/movies/%d", tmdbID), fmt.Sprintf("movie/%d", tmdbID))
	if err != nil {
		return nil, err
	}
	if data == nil {
		return []Logo{}, nil
	}
	return pick(data, []string{"hdmovielogo", "movielogo", "movieclearart"}), nil
}

// TVLogos is the JS tvLogos(tvdbId): logo candidates for a numeric TVDB
// id (resolve via TMDb TVExternalIDs first). Same empty-slice contract
// as MovieLogos.
func (c *FanartClient) TVLogos(ctx context.Context, tvdbID int64) ([]Logo, error) {
	if tvdbID == 0 {
		return []Logo{}, nil
	}
	data, err := c.get(ctx, fmt.Sprintf("/tv/%d", tvdbID), fmt.Sprintf("tv/%d", tvdbID))
	if err != nil {
		return nil, err
	}
	if data == nil {
		return []Logo{}, nil
	}
	return pick(data, []string{"hdtvlogo", "clearart", "tvthumb"}), nil
}
