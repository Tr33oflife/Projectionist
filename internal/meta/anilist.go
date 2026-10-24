package meta

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// AniList client — port of src/metadata/anilist.js. Anime relations,
// zero friction.
//
// TMDb has no anime-relation data worth using (no sequel/prequel links).
// AniList's free GraphQL API does: Media.relations edges typed SEQUEL,
// PREQUEL, SIDE_STORY, SPIN_OFF, ADAPTATION. The franchise generator in
// the Advisor uses this to auto-build anime franchise collections — the
// anime counterpart to TMDb's belongs_to_collection.
//
//   - No key, no auth. 90 requests/minute — the Advisor calls it rarely
//     (anime-heuristic items only) and the 30-day cache (namespace
//     'anilist') covers repeats.
//   - Plain HTTP POST with a JSON body — no GraphQL client dependency.
//
// VERIFICATION STATUS: UNVERIFIED live (same as the JS). Routine misses
// return (nil, nil); transport failures return *Error with the status.

const anilistAPI = "https://graphql.anilist.co"

// FranchiseRelations is the FRANCHISE_RELATIONS equivalent: the relation
// types that mean "same franchise, collect together". (JS: a Set with
// exactly these four members — ADAPTATION is deliberately NOT in it.)
var FranchiseRelations = []string{"SEQUEL", "PREQUEL", "SIDE_STORY", "SPIN_OFF"}

// IsFranchiseRelation reports whether a relationType is in
// FranchiseRelations (the JS Set.has check).
func IsFranchiseRelation(relationType string) bool {
	for _, r := range FranchiseRelations {
		if r == relationType {
			return true
		}
	}
	return false
}

// relationsQuery is RELATIONS_QUERY from the JS, verbatim.
const relationsQuery = `
query ($search: String) {
  Media(search: $search, type: ANIME) {
    id
    title { romaji english native }
    relations {
      edges {
        relationType(version: 2)
        node {
          id
          type
          format
          title { romaji english native }
          startDate { year }
        }
      }
    }
  }
}`

// AniListTitle mirrors AniList's title object; pickTitle prefers
// english, then romaji, then native (JS pickTitle).
type AniListTitle struct {
	Romaji  string `json:"romaji"`
	English string `json:"english"`
	Native  string `json:"native"`
}

func pickTitle(t AniListTitle) string {
	if t.English != "" {
		return t.English
	}
	if t.Romaji != "" {
		return t.Romaji
	}
	return t.Native
}

// RelatedTitle is the JS `self` object: {id, title}.
type RelatedTitle struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Relation is one JS related entry: {id, title, year, relation}.
type Relation struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Year     *int64 `json:"year"`
	Relation string `json:"relation"`
}

// Relations mirrors the JS relationsFor() return:
// {queryTitle, self: {id, title}, related: [...]}.
type Relations struct {
	QueryTitle string       `json:"queryTitle"`
	Self       RelatedTitle `json:"self"`
	Related    []Relation   `json:"related"`
}

// AniListClient is the Go port of the JS AniListClient.
type AniListClient struct {
	cache      Cache
	baseURL    string
	httpClient *http.Client
}

// NewAniList builds an AniList client (no key — the API is free).
func NewAniList(cache Cache) *AniListClient {
	return &AniListClient{
		cache:      cache,
		baseURL:    anilistAPI,
		httpClient: &http.Client{},
	}
}

// query is the JS _query(): POST, GraphQL-error surfacing, and the
// special-cased 429 (AniList's limit is 90/minute).
func (c *AniListClient) query(ctx context.Context, query string, variables map[string]any) (json.RawMessage, error) {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.baseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &Error{
			Message: fmt.Sprintf("Could not reach AniList (%s)", err.Error()),
			Hint:    "Check internet access.",
		}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == 429 {
		return nil, &Error{
			Message: "AniList rate limit hit (90/min)",
			Status:  429,
			Hint:    "The Advisor backs off automatically — wait a minute and rescan.",
		}
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		msg := fmt.Sprintf("AniList returned HTTP %d", res.StatusCode)
		if len(body) > 0 {
			msg += ": " + bodySnippet(body, 160)
		}
		return nil, &Error{Message: msg, Status: res.StatusCode}
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	// JS tolerated an unparseable body as {} (→ data undefined → miss).
	_ = json.Unmarshal(body, &envelope)
	if len(envelope.Errors) > 0 {
		return nil, &Error{
			Message: fmt.Sprintf("AniList GraphQL error: %s", envelope.Errors[0].Message),
			Hint:    "Usually a malformed query — check RELATIONS_QUERY.",
		}
	}
	return envelope.Data, nil
}

var anilistSlugNoise = regexp.MustCompile(`[^a-z0-9]+`)

// cacheKeyFor is the JS slug: title.toLowerCase() with every run of
// non-[a-z0-9] collapsed to '-', truncated to 80 chars. (JS truncated by
// UTF-16 code units; runes are the closest Go equivalent — identical for
// the ASCII titles this slug mostly sees.)
func anilistCacheKey(title string) string {
	slug := anilistSlugNoise.ReplaceAllString(lowerTitle(title), "-")
	return "relations/" + truncateRunes(slug, 80)
}

func lowerTitle(s string) string {
	// JS: title.toLowerCase() — Unicode-aware, as is strings.ToLower.
	return strings.ToLower(s)
}

// RelationsFor is the JS relationsFor(title): the franchise family of
// an anime title. Returns (nil, nil) when AniList knows nothing by that
// title (or the title is empty). Related holds only SEQUEL / PREQUEL /
// SIDE_STORY / SPIN_OFF edges to ANIME nodes with a usable title.
func (c *AniListClient) RelationsFor(ctx context.Context, title string) (*Relations, error) {
	if title == "" {
		return nil, nil
	}
	cacheKey := anilistCacheKey(title)
	if hit, ok := cacheGet(c.cache, "anilist", cacheKey); ok {
		if isJSONNull(hit) {
			return nil, nil // cached miss (JS cached null)
		}
		var out Relations
		if err := json.Unmarshal(hit, &out); err != nil {
			return nil, err
		}
		return &out, nil
	}
	raw, err := c.query(ctx, relationsQuery, map[string]any{"search": title})
	if err != nil {
		return nil, err
	}
	var data struct {
		Media *struct {
			ID        int64        `json:"id"`
			Title     AniListTitle `json:"title"`
			Relations struct {
				Edges []struct {
					RelationType string `json:"relationType"`
					Node         *struct {
						ID        int64        `json:"id"`
						Type      string       `json:"type"`
						Title     AniListTitle `json:"title"`
						StartDate *struct {
							Year *int64 `json:"year"`
						} `json:"startDate"`
					} `json:"node"`
				} `json:"edges"`
			} `json:"relations"`
		} `json:"Media"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &data); err != nil {
			return nil, err
		}
	}
	media := data.Media
	if media == nil {
		cacheSetNull(c.cache, "anilist", cacheKey)
		return nil, nil
	}
	related := make([]Relation, 0, len(media.Relations.Edges))
	for _, e := range media.Relations.Edges {
		if !IsFranchiseRelation(e.RelationType) || e.Node == nil || e.Node.Type != "ANIME" {
			continue
		}
		r := Relation{
			ID:       strconv.FormatInt(e.Node.ID, 10),
			Title:    pickTitle(e.Node.Title),
			Relation: e.RelationType,
		}
		if e.Node.StartDate != nil {
			r.Year = e.Node.StartDate.Year
		}
		if r.Title != "" {
			related = append(related, r)
		}
	}
	out := &Relations{
		QueryTitle: title,
		Self:       RelatedTitle{ID: strconv.FormatInt(media.ID, 10), Title: pickTitle(media.Title)},
		Related:    related,
	}
	cacheSetValue(c.cache, "anilist", cacheKey, out)
	return out, nil
}
