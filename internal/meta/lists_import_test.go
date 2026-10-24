package meta

// lists_import_test.go — v1.14 list import reads: TMDb user lists
// (the `items` array shape, per-item media_type) and Trakt user
// lists (metadata + ranked items), plus the reference parsers the
// import UI accepts. All against httptest fakes in the clients_test
// pattern — no network.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTMDbUserList(t *testing.T) {
	var gotPath, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.URL.Query().Get("api_key")
		_, _ = w.Write([]byte(`{
			"name":"Weekend Picks","description":"d","item_count":3,
			"items":[
				{"id":550,"title":"Fight Club","media_type":"movie","release_date":"1999-10-15","poster_path":"/p.jpg","vote_average":8.4},
				{"id":1399,"name":"Game of Thrones","media_type":"tv","first_air_date":"2011-04-17"},
				{"id":603,"title":"The Matrix","media_type":"movie","release_date":"1999-03-31"}
			]}`))
	}))
	defer srv.Close()
	c := NewTMDb("testkey", nil)
	c.baseURL = srv.URL
	list, err := c.UserList(context.Background(), "12345")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/list/12345" || gotKey != "testkey" {
		t.Errorf("request = %s key=%s, want /list/12345 with the configured key", gotPath, gotKey)
	}
	if list.Name != "Weekend Picks" || list.ItemCount != 3 || len(list.Entries) != 3 {
		t.Fatalf("list = %+v", list)
	}
	e := list.Entries[0]
	if e.TMDBID != "550" || e.Title != "Fight Club" || e.MediaType != "movie" || e.ReleaseDate != "1999-10-15" {
		t.Errorf("entry 0 = %+v", e)
	}
	e = list.Entries[1]
	if e.Title != "Game of Thrones" || e.MediaType != "tv" || e.ReleaseDate != "2011-04-17" {
		t.Errorf("entry 1 (tv, name/first_air_date) = %+v", e)
	}
	// Order is the list's order — the importer depends on it.
	if list.Entries[2].TMDBID != "603" {
		t.Errorf("entry 2 = %+v, want The Matrix last (list order preserved)", list.Entries[2])
	}
}

func TestParseTMDbListID(t *testing.T) {
	for ref, want := range map[string]string{
		"12345":    "12345",
		"  82931 ": "82931",
		"https://www.themoviedb.org/list/12345-weekend-picks": "12345",
		"https://www.themoviedb.org/list/777":                 "777",
	} {
		if got, ok := ParseTMDbListID(ref); !ok || got != want {
			t.Errorf("ParseTMDbListID(%q) = %q,%v want %q,true", ref, got, ok, want)
		}
	}
	for _, ref := range []string{"", "abc", "https://example.com/list/1", "12a45"} {
		if _, ok := ParseTMDbListID(ref); ok {
			t.Errorf("ParseTMDbListID(%q) ok, want not ok", ref)
		}
	}
}

func TestTraktUserList(t *testing.T) {
	var metaAuth, itemsAuth, itemsKey string
	mux := http.NewServeMux()
	mux.HandleFunc("/users/someuser/lists/favorites", func(w http.ResponseWriter, r *http.Request) {
		metaAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"name":"Favorites","item_count":2,"ids":{"trakt":555,"slug":"favorites"}}`))
	})
	mux.HandleFunc("/users/someuser/lists/favorites/items", func(w http.ResponseWriter, r *http.Request) {
		itemsAuth = r.Header.Get("Authorization")
		itemsKey = r.Header.Get("trakt-api-key")
		_, _ = w.Write([]byte(`[
			{"rank":1,"type":"movie","movie":{"title":"Arrival","year":2016,"ids":{"tmdb":329865,"imdb":"tt2543164","trakt":999}}},
			{"rank":2,"type":"show","show":{"title":"Dark","year":2017,"ids":{"tmdb":70523,"imdb":"tt5753856","trakt":888}}}
		]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := NewTrakt("cid", "", nil)
	c.baseURL = srv.URL

	// Public read: no token — only the client ID header.
	list, err := c.UserList(context.Background(), "someuser", "favorites", "")
	if err != nil {
		t.Fatal(err)
	}
	if metaAuth != "" || itemsAuth != "" {
		t.Errorf("public read sent Authorization (meta=%q items=%q), want none", metaAuth, itemsAuth)
	}
	if itemsKey != "cid" {
		t.Errorf("trakt-api-key = %q, want the client ID", itemsKey)
	}
	if list.Name != "Favorites" || list.ItemCount != 2 || len(list.Entries) != 2 {
		t.Fatalf("list = %+v", list)
	}
	e := list.Entries[0]
	if e.Rank != 1 || e.Title != "Arrival" || e.TMDBID != "329865" || e.IMDBID != "tt2543164" || e.MediaType != "movie" || e.Year == nil || *e.Year != 2016 {
		t.Errorf("entry 0 = %+v", e)
	}
	e = list.Entries[1]
	if e.Rank != 2 || e.Title != "Dark" || e.MediaType != "show" {
		t.Errorf("entry 1 = %+v, want Dark (show) in rank order", e)
	}

	// Token read: the account retry path sends the Bearer token.
	if _, err := c.UserList(context.Background(), "someuser", "favorites", "tok123"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(itemsAuth, "Bearer ") || !strings.Contains(itemsAuth, "tok123") {
		t.Errorf("token read Authorization = %q, want Bearer tok123", itemsAuth)
	}
}

func TestParseTraktListRef(t *testing.T) {
	for ref, want := range map[string][2]string{
		"https://trakt.tv/users/someuser/lists/favorites": {"someuser", "favorites"},
		"https://trakt.tv/users/u2/lists/12345":           {"u2", "12345"},
		"someuser/favorites":                              {"someuser", "favorites"},
	} {
		u, l, ok := ParseTraktListRef(ref)
		if !ok || u != want[0] || l != want[1] {
			t.Errorf("ParseTraktListRef(%q) = %q,%q,%v want %v", ref, u, l, ok, want)
		}
	}
	for _, ref := range []string{"", "justonepart", "https://trakt.tv/movies", "a/b/c"} {
		if _, _, ok := ParseTraktListRef(ref); ok {
			t.Errorf("ParseTraktListRef(%q) ok, want not ok", ref)
		}
	}
}
