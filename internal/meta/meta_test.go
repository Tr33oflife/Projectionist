package meta

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeCache is an in-memory Cache implementing the fixed interface.
type fakeCache struct {
	m map[string]json.RawMessage
}

func newFakeCache() *fakeCache { return &fakeCache{m: map[string]json.RawMessage{}} }

func (f *fakeCache) Get(ns, key string) (json.RawMessage, bool) {
	v, ok := f.m[ns+"\x00"+key]
	return v, ok
}

func (f *fakeCache) Set(ns, key string, data json.RawMessage) {
	cp := make(json.RawMessage, len(data))
	copy(cp, data)
	f.m[ns+"\x00"+key] = cp
}

func (f *fakeCache) has(ns, key string) bool {
	_, ok := f.m[ns+"\x00"+key]
	return ok
}

// ---- ParseGuids ---------------------------------------------------------

func TestParseGuids(t *testing.T) {
	tests := []struct {
		name  string
		guids []string
		want  IDs
	}{
		{"all three", []string{"tmdb://550", "tvdb://123", "imdb://tt0133093"}, IDs{TMDB: 550, IMDB: "tt0133093", TVDB: "123"}},
		{"tmdb only", []string{"tmdb://550"}, IDs{TMDB: 550}},
		{"imdb only", []string{"imdb://tt0133093"}, IDs{IMDB: "tt0133093"}},
		{"missing/nil", nil, IDs{}},
		{"empty slice", []string{}, IDs{}},
		{"first occurrence wins", []string{"tmdb://550", "tmdb://999"}, IDs{TMDB: 550}},
		{"junk entries skipped", []string{"", "nope", "tmdb://42"}, IDs{TMDB: 42}},
		{"unrelated agents ignored", []string{"tvdb://77", "com.plexapp.agents.thetvdb://77"}, IDs{TVDB: "77"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseGuids(tc.guids); got != tc.want {
				t.Errorf("ParseGuids(%v) = %+v, want %+v", tc.guids, got, tc.want)
			}
		})
	}
}

// ---- TMDb: pacing constants, auth, cache, normalization -----------------

func TestTMDbPacingConstants(t *testing.T) {
	if PACING_MAX != 35 || PACING_WINDOW_MS != 10_000 || PACING_MIN_GAP_MS != 300 {
		t.Errorf("pacing constants = %d/%d/%d, want 35/10000/300", PACING_MAX, PACING_WINDOW_MS, PACING_MIN_GAP_MS)
	}
	if PacingMax != PACING_MAX || PacingWindow.Milliseconds() != PACING_WINDOW_MS || PacingMinGap.Milliseconds() != PACING_MIN_GAP_MS {
		t.Error("idiomatic pacing aliases disagree with the JS constants")
	}
}

func TestTMDbCacheHitAvoidsSecondHTTPCall(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"title":"Fight Club","runtime":139,"vote_average":8.4,"popularity":61.4,"original_language":"en","spoken_languages":[{"iso_639_1":"en"}],"poster_path":"/p.jpg","release_date":"1999-10-15","belongs_to_collection":null,"genres":[{"id":18,"name":"Drama"}]}`))
	}))
	defer srv.Close()

	cache := newFakeCache()
	c := NewTMDb("testkey", cache)
	c.baseURL = srv.URL

	d1, err := c.MovieDetails(context.Background(), 550)
	if err != nil {
		t.Fatalf("first MovieDetails: %v", err)
	}
	d2, err := c.MovieDetails(context.Background(), 550)
	if err != nil {
		t.Fatalf("second MovieDetails: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("HTTP calls = %d, want 1 (second call must be a cache hit)", calls.Load())
	}
	if !cache.has("tmdb", "movie/550") {
		t.Error("expected cache entry under ns=tmdb key=movie/550")
	}
	if d1.Title != "Fight Club" || d2.Title != "Fight Club" {
		t.Errorf("titles = %q / %q, want Fight Club", d1.Title, d2.Title)
	}
	// Exact normalized shape (JS field names) survives a JSON round trip.
	raw, _ := json.Marshal(d1)
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(raw, &keys)
	for _, k := range []string{"tmdbId", "mediaType", "title", "belongsToCollection", "genres", "runtime", "voteAverage", "popularity", "originalLanguage", "spokenLanguages", "posterPath", "releaseDate"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("movie Details JSON missing key %q: %s", k, raw)
		}
	}
	if _, ok := keys["numberOfSeasons"]; ok {
		t.Errorf("movie Details must not carry numberOfSeasons: %s", raw)
	}
	if d1.VoteAverage == nil || *d1.VoteAverage != 8.4 {
		t.Errorf("voteAverage = %v, want 8.4", d1.VoteAverage)
	}
}

func TestTMDbTVDetailsShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"Breaking Bad","episode_run_time":[45],"number_of_seasons":5,"first_air_date":"2008-01-20","vote_average":8.9,"popularity":100.1,"original_language":"en","spoken_languages":[{"iso_639_1":"en"}],"poster_path":"/bb.jpg","genres":[{"id":18,"name":"Drama"}]}`))
	}))
	defer srv.Close()
	c := NewTMDb("k", nil)
	c.baseURL = srv.URL
	d, err := c.TVDetails(context.Background(), 1396)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(d)
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(raw, &keys)
	for _, k := range []string{"episodeRunTime", "numberOfSeasons", "firstAirDate", "belongsToCollection"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("tv Details JSON missing key %q: %s", k, raw)
		}
	}
	if _, ok := keys["releaseDate"]; ok {
		t.Errorf("tv Details must not carry releaseDate: %s", raw)
	}
	if string(keys["runtime"]) != "null" || string(keys["belongsToCollection"]) != "null" {
		t.Errorf("tv runtime/belongsToCollection must be null: %s", raw)
	}
	if d.NumberOfSeasons == nil || *d.NumberOfSeasons != 5 {
		t.Errorf("numberOfSeasons = %v, want 5", d.NumberOfSeasons)
	}
}

func TestTMDbAuthModes(t *testing.T) {
	var gotAuth, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotKey = r.URL.Query().Get("api_key")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	v3 := NewTMDb("abc123", nil)
	v3.baseURL = srv.URL
	if v3.UseBearer() {
		t.Error("v3 key must not use Bearer")
	}
	if _, err := v3.TopRatedMovies(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotKey != "abc123" || gotAuth != "" {
		t.Errorf("v3 auth: api_key=%q Authorization=%q, want api_key only", gotKey, gotAuth)
	}

	jwt := NewTMDb("eyJhbGciOiJIUzI1NiJ9.payload.sig", nil)
	jwt.baseURL = srv.URL
	if !jwt.UseBearer() {
		t.Error("eyJ key must use Bearer")
	}
	if _, err := jwt.TopRatedMovies(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig" || gotKey != "" {
		t.Errorf("JWT auth: Authorization=%q api_key=%q, want Bearer only", gotAuth, gotKey)
	}
}

func TestTMDbSearchNoMatchReturnsNilNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()
	c := NewTMDb("k", nil)
	c.baseURL = srv.URL
	year := 1999
	got, err := c.SearchMovie(context.Background(), "Nope", &year)
	if err != nil || got != nil {
		t.Errorf("SearchMovie no match = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestTMDbErrorSurfacesStatusAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"status_message":"Invalid API key"}`))
	}))
	defer srv.Close()
	c := NewTMDb("bad", nil)
	c.baseURL = srv.URL
	_, err := c.MovieDetails(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error")
	}
	e, ok := err.(*Error)
	if !ok || e.Status != 401 {
		t.Fatalf("err = %#v, want *Error with Status 401", err)
	}
	if !contains(e.Message, "401") || !contains(e.Message, "Invalid API key") {
		t.Errorf("error must surface status + body snippet, got %q", e.Message)
	}
	if e.Hint == "" {
		t.Error("401 must carry the Settings hint")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
