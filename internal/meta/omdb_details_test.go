package meta

// omdb_details_test.go — GetDetails + TMDb TVStatus against httptest
// servers with REAL response shapes (OMDb Ratings[] percent strings,
// TMDb /tv status vocabulary). No live network.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func omdbDetailsServer(t *testing.T, body string) (*OMDbClient, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("i") == "" || r.URL.Query().Get("apikey") == "" {
			t.Errorf("OMDb request missing i/apikey: %s", r.URL)
		}
		_, _ = w.Write([]byte(body))
	}))
	c := NewOMDb("test-key", nil)
	c.baseURL = srv.URL + "/"
	return c, srv.Close
}

func TestOMDbGetDetailsParsesRatingsChain(t *testing.T) {
	c, done := omdbDetailsServer(t, `{
		"Title": "Fixture", "imdbRating": "8.7", "Metascore": "74",
		"Ratings": [
			{"Source": "Internet Movie Database", "Value": "8.7/10"},
			{"Source": "Rotten Tomatoes", "Value": "91%"},
			{"Source": "Metacritic", "Value": "74/100"}
		],
		"Response": "True"
	}`)
	defer done()
	d, err := c.GetDetails(context.Background(), "tt0133093")
	if err != nil {
		t.Fatal(err)
	}
	if d == nil {
		t.Fatal("details nil")
	}
	if d.IMDBRating == nil || *d.IMDBRating != 8.7 {
		t.Errorf("imdb = %v, want 8.7", d.IMDBRating)
	}
	if d.RTRating == nil || *d.RTRating != 91 {
		t.Errorf("rt = %v, want 91 (percent string parsed)", d.RTRating)
	}
	if d.Metascore == nil || *d.Metascore != 74 {
		t.Errorf("metascore = %v, want 74", d.Metascore)
	}
}

func TestOMDbGetDetailsNAAndMiss(t *testing.T) {
	c, done := omdbDetailsServer(t, `{"imdbRating":"N/A","Metascore":"N/A","Ratings":[],"Response":"True"}`)
	defer done()
	d, err := c.GetDetails(context.Background(), "tt0000000")
	if err != nil {
		t.Fatal(err)
	}
	if d != nil {
		t.Errorf("all-N/A record must be a routine miss (nil), got %+v", d)
	}

	c2, done2 := omdbDetailsServer(t, `{"Response":"False","Error":"Movie not found!"}`)
	defer done2()
	d2, err := c2.GetDetails(context.Background(), "tt9999999")
	if err != nil || d2 != nil {
		t.Errorf("not-found: d=%v err=%v, want nil/nil", d2, err)
	}

	// No key → (nil, nil) without any request.
	d3, err := NewOMDb("", nil).GetDetails(context.Background(), "tt0133093")
	if err != nil || d3 != nil {
		t.Errorf("no key: d=%v err=%v, want nil/nil", d3, err)
	}
}

func TestOMDbGetDetailsAwards(t *testing.T) {
	// The awards sentence rides along verbatim with the ratings.
	c, done := omdbDetailsServer(t, `{
		"Title": "Fixture", "imdbRating": "8.7", "Metascore": "74",
		"Awards": "Won 2 Oscars. 45 wins & 32 nominations total",
		"Ratings": [{"Source": "Rotten Tomatoes", "Value": "91%"}],
		"Response": "True"
	}`)
	defer done()
	d, err := c.GetDetails(context.Background(), "tt0133093")
	if err != nil {
		t.Fatal(err)
	}
	if d == nil {
		t.Fatal("details nil")
	}
	if d.Awards != "Won 2 Oscars. 45 wins & 32 nominations total" {
		t.Errorf("awards = %q, want the response's sentence verbatim", d.Awards)
	}

	// Awards-ONLY record: every rating N/A, but the awards line is
	// real data — the record must survive GetDetails (the awards
	// ribbon has no other source).
	c2, done2 := omdbDetailsServer(t, `{
		"imdbRating": "N/A", "Metascore": "N/A", "Ratings": [],
		"Awards": "Nominated for 1 Oscar. 3 wins & 5 nominations total",
		"Response": "True"
	}`)
	defer done2()
	d2, err := c2.GetDetails(context.Background(), "tt0000001")
	if err != nil {
		t.Fatal(err)
	}
	if d2 == nil {
		t.Fatal("awards-only record must NOT be a routine miss (nil)")
	}
	if d2.Awards != "Nominated for 1 Oscar. 3 wins & 5 nominations total" {
		t.Errorf("awards = %q, want the nomination sentence", d2.Awards)
	}
	if d2.IMDBRating != nil || d2.RTRating != nil || d2.Metascore != nil {
		t.Errorf("ratings must stay nil on an awards-only record: %+v", d2)
	}

	// "N/A" awards normalize to "" — and with no ratings either, the
	// record is the usual routine miss.
	c3, done3 := omdbDetailsServer(t, `{"imdbRating":"N/A","Metascore":"N/A","Ratings":[],"Awards":"N/A","Response":"True"}`)
	defer done3()
	d3, err := c3.GetDetails(context.Background(), "tt0000002")
	if err != nil {
		t.Fatal(err)
	}
	if d3 != nil {
		t.Errorf("all-N/A record (incl. awards) must be a routine miss, got %+v", d3)
	}
}

func tmdbStatusServer(t *testing.T, body string) (*TMDbClient, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	c := NewTMDb("test-key", nil)
	c.baseURL = srv.URL
	return c, srv.Close
}

func TestTMDbTVStatusMapping(t *testing.T) {
	old := time.Now().AddDate(-3, 0, 0).Format("2006-01-02")
	recent := time.Now().AddDate(0, 0, -5).Format("2006-01-02")
	cases := []struct {
		name string
		body string
		want string
	}{
		{"ended", `{"status":"Ended"}`, "Ended"},
		{"canceled", `{"status":"Canceled"}`, "Canceled"},
		{"returning with next episode", `{"status":"Returning Series","next_episode_to_air":{"air_date":"2099-01-01"}}`, "Airing"},
		{"returning recently aired", fmt.Sprintf(`{"status":"Returning Series","next_episode_to_air":null,"last_episode_to_air":{"air_date":%q}}`, recent), "Airing"},
		{"returning on hiatus", fmt.Sprintf(`{"status":"Returning Series","next_episode_to_air":null,"last_episode_to_air":{"air_date":%q}}`, old), "Returning"},
		{"in production is unmapped", `{"status":"In Production"}`, ""},
		{"pilot is unmapped", `{"status":"Pilot"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, done := tmdbStatusServer(t, tc.body)
			defer done()
			got, err := c.TVStatus(context.Background(), 42)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("TVStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTMDbWatchProviders(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{
			"results": {
				"US": {"link": "https://example.test/us", "flatrate": [
					{"provider_name": "Netflix"},
					{"provider_name": "Disney Plus"},
					{"provider_name": "Netflix"}
				]},
				"GB": {"flatrate": [{"provider_name": "Netflix"}]},
				"DE": {"rent": [{"provider_name": "Apple TV"}], "buy": [{"provider_name": "Amazon Video"}]}
			}
		}`))
	}))
	defer srv.Close()
	c := NewTMDb("test-key", nil)
	c.baseURL = srv.URL

	provs, err := c.WatchProviders(context.Background(), 550, "movie")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/movie/550/watch/providers" {
		t.Errorf("path = %q, want /movie/550/watch/providers", gotPath)
	}
	// TMDb order preserved, duplicates removed.
	us := provs["US"]
	if len(us) != 2 || us[0] != "Netflix" || us[1] != "Disney Plus" {
		t.Errorf("US = %v, want [Netflix Disney Plus] (order kept, dup dropped)", us)
	}
	if gb := provs["GB"]; len(gb) != 1 || gb[0] != "Netflix" {
		t.Errorf("GB = %v, want [Netflix]", gb)
	}
	// Rent/buy-only regions are NOT streaming: DE must not appear.
	if _, ok := provs["DE"]; ok {
		t.Errorf("DE = %v, want omitted (rent/buy is not flatrate)", provs["DE"])
	}
	// Bad media type is an error, never a silent movie lookup.
	if _, err := c.WatchProviders(context.Background(), 550, "show"); err == nil {
		t.Error("WatchProviders(mediaType=show) must error — the client speaks movie|tv")
	}
}
