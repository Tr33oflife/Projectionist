package meta

// tmdb_origin_test.go — TitleOrigin: the Franchise + Origin facts
// (v1.11 badge library). The contract under test: values are exactly
// what TMDb states (collection name verbatim, original language,
// origin countries with the production_countries fallback), TV is
// never consulted for a collection, absent data yields empty facts
// (never an error, never a guess), and the lookup rides the SAME
// cache entry as MovieDetails/TVDetails — no extra HTTP traffic for
// a title whose details are already cached.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func tmdbOriginClient(t *testing.T, body string, calls *atomic.Int64) (*TMDbClient, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	c := NewTMDb("testkey", newFakeCache())
	c.baseURL = srv.URL
	return c, srv.Close
}

func TestTMDbTitleOriginMovie(t *testing.T) {
	c, done := tmdbOriginClient(t, `{
		"belongs_to_collection": {"id": 11, "name": "The Example Collection"},
		"original_language": "ja",
		"origin_country": ["JP"],
		"production_countries": [{"iso_3166_1": "JP", "name": "Japan"}]
	}`, nil)
	defer done()
	org, err := c.TitleOrigin(context.Background(), 11, "movie")
	if err != nil {
		t.Fatal(err)
	}
	if org.CollectionName != "The Example Collection" {
		t.Errorf("CollectionName = %q, want TMDb's name verbatim", org.CollectionName)
	}
	if org.CollectionID != 11 {
		t.Errorf("CollectionID = %d, want 11 (the saga-position facts key off it)", org.CollectionID)
	}
	if org.OriginalLanguage != "ja" {
		t.Errorf("OriginalLanguage = %q, want ja", org.OriginalLanguage)
	}
	if len(org.OriginCountries) != 1 || org.OriginCountries[0] != "JP" {
		t.Errorf("OriginCountries = %v, want [JP] (origin_country wins over production_countries)", org.OriginCountries)
	}
}

func TestTMDbTitleOriginProductionCountriesFallback(t *testing.T) {
	// A standalone film whose record carries no origin_country: the
	// production_countries are the fallback, in TMDb's order, and
	// belongs_to_collection: null means NO franchise fact.
	c, done := tmdbOriginClient(t, `{
		"belongs_to_collection": null,
		"original_language": "it",
		"production_countries": [{"iso_3166_1": "IT"}, {"iso_3166_1": "FR"}]
	}`, nil)
	defer done()
	org, err := c.TitleOrigin(context.Background(), 12, "movie")
	if err != nil {
		t.Fatal(err)
	}
	if org.CollectionName != "" {
		t.Errorf("CollectionName = %q, want empty (standalone film)", org.CollectionName)
	}
	if len(org.OriginCountries) != 2 || org.OriginCountries[0] != "IT" || org.OriginCountries[1] != "FR" {
		t.Errorf("OriginCountries = %v, want [IT FR] from production_countries", org.OriginCountries)
	}
}

func TestTMDbTitleOriginTVHasNoCollection(t *testing.T) {
	// TV origin comes from origin_country; a collection field on a
	// TV payload (TMDb does not send one) must never become a
	// franchise fact — movies only, by construction.
	c, done := tmdbOriginClient(t, `{
		"belongs_to_collection": {"id": 99, "name": "Bait Collection"},
		"original_language": "ko",
		"origin_country": ["KR"]
	}`, nil)
	defer done()
	org, err := c.TitleOrigin(context.Background(), 99, "tv")
	if err != nil {
		t.Fatal(err)
	}
	if org.CollectionName != "" {
		t.Errorf("CollectionName = %q, want empty for TV", org.CollectionName)
	}
	if org.CollectionID != 0 {
		t.Errorf("CollectionID = %d, want 0 for TV (no saga facts for shows)", org.CollectionID)
	}
	if org.OriginalLanguage != "ko" || len(org.OriginCountries) != 1 || org.OriginCountries[0] != "KR" {
		t.Errorf("origin = %q / %v, want ko / [KR]", org.OriginalLanguage, org.OriginCountries)
	}
}

func TestTMDbTitleOriginAbsentData(t *testing.T) {
	// A record that states nothing: empty facts, nil error — the
	// families hide on empty, so absence must not look like failure.
	c, done := tmdbOriginClient(t, `{"original_language": "", "belongs_to_collection": null}`, nil)
	defer done()
	org, err := c.TitleOrigin(context.Background(), 13, "movie")
	if err != nil {
		t.Fatal(err)
	}
	if org.CollectionName != "" || org.OriginalLanguage != "" || len(org.OriginCountries) != 0 {
		t.Errorf("origin = %+v, want all empty", org)
	}
}

func TestTMDbTitleOriginSharesDetailsCache(t *testing.T) {
	// TitleOrigin reads the same details response MovieDetails
	// caches (key movie/<id>): fetching details first makes the
	// origin lookup free — exactly one HTTP call for both.
	var calls atomic.Int64
	c, done := tmdbOriginClient(t, `{
		"title": "Cached Film", "runtime": 100, "vote_average": 7.5,
		"belongs_to_collection": {"id": 5, "name": "Cached Collection"},
		"original_language": "en", "origin_country": ["US"],
		"genres": [], "spoken_languages": []
	}`, &calls)
	defer done()
	if _, err := c.MovieDetails(context.Background(), 550); err != nil {
		t.Fatal(err)
	}
	org, err := c.TitleOrigin(context.Background(), 550, "movie")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Errorf("HTTP calls = %d, want 1 (TitleOrigin must hit the details cache)", calls.Load())
	}
	if org.CollectionName != "Cached Collection" {
		t.Errorf("CollectionName = %q, want the cached record's collection", org.CollectionName)
	}
}

func TestTMDbTitleOriginBadTypeAndHTTPError(t *testing.T) {
	c, done := tmdbOriginClient(t, `{}`, nil)
	defer done()
	if _, err := c.TitleOrigin(context.Background(), 1, "person"); err == nil {
		t.Error("unsupported media type must error")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c2 := NewTMDb("testkey", newFakeCache())
	c2.baseURL = srv.URL
	if _, err := c2.TitleOrigin(context.Background(), 1, "movie"); err == nil {
		t.Error("HTTP 500 must surface as an error (the facts layer degrades on it)")
	}
}
