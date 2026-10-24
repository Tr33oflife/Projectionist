package meta

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// ---- Trakt: OOB --------------------------------------------------------------

func TestTraktOOBAuthURL(t *testing.T) {
	c := NewTrakt("my-client-id", "secret", nil)
	u := c.OOBAuthURL()
	if !strings.Contains(u, "redirect_uri=urn%3Aietf%3Awg%3Aoauth%3A2.0%3Aoob") {
		t.Errorf("OOBAuthURL missing URL-escaped OOB redirect: %s", u)
	}
	if !strings.Contains(u, "client_id=my-client-id") {
		t.Errorf("OOBAuthURL missing client_id: %s", u)
	}
	if strings.Contains(u, "localhost") || strings.Contains(u, "127.0.0.1") {
		t.Errorf("OOBAuthURL must never contain a localhost callback: %s", u)
	}
	if OOBRedirect != "urn:ietf:wg:oauth:2.0:oob" {
		t.Errorf("OOBRedirect = %q", OOBRedirect)
	}
}

func TestTraktExchangeCodePostsOOBRedirect(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","expires_in":7776000}`))
	}))
	defer srv.Close()
	c := NewTrakt("cid", "csecret", nil)
	c.baseURL = srv.URL
	tok, err := c.ExchangeCode(context.Background(), "  pasted-code  ")
	if err != nil {
		t.Fatal(err)
	}
	if got["redirect_uri"] != OOBRedirect {
		t.Errorf("posted redirect_uri = %q, want %q", got["redirect_uri"], OOBRedirect)
	}
	if got["grant_type"] != "authorization_code" || got["code"] != "pasted-code" || got["client_id"] != "cid" || got["client_secret"] != "csecret" {
		t.Errorf("posted body = %v, want code/client_id/client_secret/grant_type", got)
	}
	if tok.AccessToken != "at" || tok.RefreshToken != "rt" || tok.ExpiresAt <= 0 {
		t.Errorf("token = %+v, want access/refresh/expiresAt(ms)", tok)
	}
	raw, _ := json.Marshal(tok)
	for _, k := range []string{`"accessToken"`, `"refreshToken"`, `"expiresAt"`} {
		if !strings.Contains(string(raw), k) {
			t.Errorf("TraktToken JSON %s missing %s", raw, k)
		}
	}
}

func TestTraktRefreshKeepsOldRefreshTokenWhenNoneReturned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"at2","expires_in":100}`))
	}))
	defer srv.Close()
	c := NewTrakt("cid", "csecret", nil)
	c.baseURL = srv.URL
	tok, err := c.RefreshAccessToken(context.Background(), "old-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "at2" || tok.RefreshToken != "old-refresh" {
		t.Errorf("token = %+v, want new access + kept refresh", tok)
	}
}

func TestTraktTrendingAndWatchlistShapes(t *testing.T) {
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/movies/trending"):
			if r.Header.Get("trakt-api-key") != "cid" || r.Header.Get("trakt-api-version") != "2" {
				t.Errorf("missing trakt headers: %v", r.Header)
			}
			_, _ = w.Write([]byte(`[{"watchers":10,"movie":{"title":"Dune","year":2021,"ids":{"tmdb":438631,"imdb":"tt1160419","trakt":123}}}]`))
		case strings.HasPrefix(r.URL.Path, "/users/me/watchlist/movies"):
			if r.Header.Get("Authorization") != "Bearer tok" {
				t.Errorf("watchlist Authorization = %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`[{"type":"movie","movie":{"title":"Arrival","year":2016,"ids":{"tmdb":329865,"imdb":"tt2543164","trakt":999}}}]`))
		default:
			w.WriteHeader(404)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cache := newFakeCache()
	c := NewTrakt("cid", "", cache)
	c.baseURL = srv.URL

	trending, err := c.TrendingMovies(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(trending) != 1 || trending[0].TMDBID != "438631" || trending[0].TraktID != "123" || trending[0].MediaType != "movie" {
		t.Errorf("trending = %+v", trending)
	}
	if !cache.has("trakt", "lists/trending-movies") {
		t.Error("trending must cache under ns=trakt key=lists/trending-movies")
	}
	raw, _ := json.Marshal(trending[0])
	if !strings.Contains(string(raw), `"traktId"`) {
		t.Errorf("list entry JSON must carry traktId: %s", raw)
	}

	watch, err := c.Watchlist(context.Background(), "tok", "movies")
	if err != nil {
		t.Fatal(err)
	}
	if len(watch) != 1 || watch[0].Title != "Arrival" || watch[0].TMDBID != "329865" {
		t.Errorf("watchlist = %+v", watch)
	}
	raw, _ = json.Marshal(watch[0])
	if strings.Contains(string(raw), "traktId") {
		t.Errorf("watchlist entry JSON must NOT carry traktId (JS shape): %s", raw)
	}
}

// ---- OMDb: unconfigured tolerance + rating -----------------------------------

func TestOMDbUnconfiguredNoHTTPCall(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewOMDb("", nil)
	c.baseURL = srv.URL
	if c.Configured() {
		t.Error("empty key must be unconfigured")
	}
	r, err := c.GetRating(context.Background(), "tt0133093")
	if r != nil || err != nil {
		t.Errorf("unconfigured GetRating = (%v, %v), want (nil, nil)", r, err)
	}
	if calls.Load() != 0 {
		t.Errorf("HTTP calls = %d, want 0 when unconfigured", calls.Load())
	}
}

func TestOMDbRatingAndMiss(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("i") == "tt0133093" {
			_, _ = w.Write([]byte(`{"Response":"True","imdbRating":"8.8","Title":"Fight Club"}`))
			return
		}
		_, _ = w.Write([]byte(`{"Response":"False","Error":"Movie not found!"}`))
	}))
	defer srv.Close()
	cache := newFakeCache()
	c := NewOMDb("k", cache)
	c.baseURL = srv.URL

	r, err := c.GetRating(context.Background(), "tt0133093")
	if err != nil || r == nil {
		t.Fatalf("GetRating = (%v, %v)", r, err)
	}
	if r.IMDBRating != "8.8" || r.Title != "Fight Club" || r.Source != "OMDb" || r.IMDBID != "tt0133093" {
		t.Errorf("rating = %+v, want {tt0133093 8.8 Fight Club OMDb}", r)
	}
	miss, err := c.GetRating(context.Background(), "tt0000000")
	if miss != nil || err != nil {
		t.Errorf("not-found GetRating = (%v, %v), want (nil, nil)", miss, err)
	}
	if !cache.has("omdb", "rating/tt0000000") {
		t.Error("routine miss must be cached as null under ns=omdb")
	}
}

// ---- Fanart: unconfigured tolerance + sorting --------------------------------

func TestFanartUnconfiguredNoHTTPCall(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewFanart("", nil)
	c.baseURL = srv.URL
	logos, err := c.MovieLogos(context.Background(), 550)
	if err != nil || len(logos) != 0 {
		t.Errorf("unconfigured MovieLogos = (%v, %v), want (empty, nil)", logos, err)
	}
	tv, err := c.TVLogos(context.Background(), 123)
	if err != nil || len(tv) != 0 {
		t.Errorf("unconfigured TVLogos = (%v, %v), want (empty, nil)", tv, err)
	}
	if calls.Load() != 0 {
		t.Errorf("HTTP calls = %d, want 0 when unconfigured", calls.Load())
	}
}

func TestFanartLogosMostLikedFirst(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"hdmovielogo":[{"url":"https://x/a.png","lang":"en","likes":"3"}],
			"movielogo":[{"url":"https://x/b.png","lang":"en","likes":"17"}],
			"movieclearart":[{"url":"https://x/c.png","lang":"","likes":"9"}]
		}`))
	}))
	defer srv.Close()
	cache := newFakeCache()
	c := NewFanart("k", cache)
	c.baseURL = srv.URL
	logos, err := c.MovieLogos(context.Background(), 550)
	if err != nil {
		t.Fatal(err)
	}
	if len(logos) != 3 || logos[0].Likes != 17 || logos[1].Likes != 9 || logos[2].Likes != 3 {
		t.Errorf("logos = %+v, want most-liked first (17, 9, 3)", logos)
	}
	if logos[0].Kind != "movielogo" {
		t.Errorf("first logo kind = %q, want movielogo", logos[0].Kind)
	}
	if !cache.has("fanart", "movie/550") {
		t.Error("expected cache entry under ns=fanart key=movie/550")
	}
	// 404 → routine miss, empty slice, no error.
	srv404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer srv404.Close()
	c2 := NewFanart("k", nil)
	c2.baseURL = srv404.URL
	logos, err = c2.TVLogos(context.Background(), 42)
	if err != nil || len(logos) != 0 {
		t.Errorf("404 TVLogos = (%v, %v), want (empty, nil)", logos, err)
	}
}

// ---- AniList: relations filtering ---------------------------------------------

func TestAniListRelations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Variables["search"] != "Fate/stay night" {
			t.Errorf("search variable = %v", req.Variables["search"])
		}
		_, _ = w.Write([]byte(`{"data":{"Media":{"id":356,"title":{"romaji":"Fate/stay night","english":"","native":"x"},"relations":{"edges":[
			{"relationType":"SEQUEL","node":{"id":562,"type":"ANIME","title":{"romaji":"Fate Sequel","english":"","native":""},"startDate":{"year":2011}}},
			{"relationType":"ADAPTATION","node":{"id":999,"type":"ANIME","title":{"romaji":"Adaptation","english":"","native":""},"startDate":{"year":2012}}},
			{"relationType":"SIDE_STORY","node":{"id":777,"type":"MANGA","title":{"romaji":"Manga side","english":"","native":""},"startDate":{"year":2013}}}
		]}}}}`))
	}))
	defer srv.Close()
	cache := newFakeCache()
	c := NewAniList(cache)
	c.baseURL = srv.URL
	rel, err := c.RelationsFor(context.Background(), "Fate/stay night")
	if err != nil {
		t.Fatal(err)
	}
	if rel == nil || rel.Self.ID != "356" || rel.Self.Title != "Fate/stay night" || rel.QueryTitle != "Fate/stay night" {
		t.Fatalf("relations = %+v", rel)
	}
	// Only the SEQUEL anime edge survives: ADAPTATION is not a franchise
	// relation, and the SIDE_STORY node is MANGA, not ANIME.
	if len(rel.Related) != 1 || rel.Related[0].Relation != "SEQUEL" || rel.Related[0].Title != "Fate Sequel" {
		t.Errorf("related = %+v, want only the SEQUEL anime edge", rel.Related)
	}
	if rel.Related[0].Year == nil || *rel.Related[0].Year != 2011 {
		t.Errorf("related year = %v, want 2011", rel.Related[0].Year)
	}
	if !cache.has("anilist", "relations/fate-stay-night") {
		t.Error("expected cache entry under ns=anilist key=relations/fate-stay-night")
	}
	if !IsFranchiseRelation("SPIN_OFF") || IsFranchiseRelation("ADAPTATION") {
		t.Error("FranchiseRelations membership wrong")
	}

	// Unknown title → (nil, nil).
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"Media":null}}`))
	}))
	defer srv2.Close()
	c2 := NewAniList(nil)
	c2.baseURL = srv2.URL
	rel, err = c2.RelationsFor(context.Background(), "No Such Anime")
	if rel != nil || err != nil {
		t.Errorf("unknown title RelationsFor = (%v, %v), want (nil, nil)", rel, err)
	}
}
