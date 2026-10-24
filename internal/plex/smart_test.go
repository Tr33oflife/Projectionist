package plex

// smart_test.go — v1.14 smart collections against an httptest fake
// that mirrors the sandbox PMS 1.43.5 shapes the live probe pinned
// (2026-10-23):
//   - create takes its parameters in the QUERY STRING;
//   - the created record self-describes: smart="1" (a string) and
//     `content` holding the filter URI verbatim;
//   - a smart collection's members resolve ONLY via
//     /library/collections/{id}/children — the metadata children
//     path returns empty for it;
//   - manual collections answer on the metadata path as always.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestSmartRuleSummary(t *testing.T) {
	const base = "server://m/com.plexapp.plugins.library/library/sections/1/all?"
	cases := []struct{ content, want string }{
		{"", ""},
		{"server://m/no-query", ""},
		{base + "type=1&decade=2000", "Decade is 2000"},
		{base + "type=1&genre=Action", "Genre is Action"},
		{base + "type=1&genre=Action&genre=Drama", "Genre is Action, Drama"},
		{base + "type=2&year>=2001", "Year ≥ 2001"},
		{base + "type=1&year>>=2001", "Year after 2001"},
		{base + "type=1&year<=1999", "Year ≤ 1999"},
		{base + "type=1&contentRating=PG-13", "Content rating is PG-13"},
		{base + "type=1&audienceRating>>=8", "Audience rating > 8"}, // >>= is strict (probed)
		{base + "type=1&rating>=8", "Critic rating ≥ 8"},
		{base + "type=1&duration<<=6000000", "Runtime ≤ 100 min"},
		{base + "type=1&addedAt>>=-60d", "Added in the last 60 days"},
		{base + "type=1&addedAt>>=-1d", "Added in the last day"},
		{base + "type=1&unwatched=1", "Unwatched only"},
		{base + "type=1&decade=2000&unwatched=1", "Decade is 2000 · Unwatched only"},
		{base + "type=1&studio=Pixar", "Studio is Pixar"},
		// Unknown parameters pass through readably, never dropped.
		{base + "type=1&resolution=1080", "resolution is 1080"},
	}
	for _, tc := range cases {
		if got := SmartRuleSummary(tc.content); got != tc.want {
			t.Errorf("SmartRuleSummary(%q) = %q, want %q", tc.content, got, tc.want)
		}
	}
}

func TestCollectionFlagsSmart(t *testing.T) {
	smartEmpty := Collection{RatingKey: "9", Title: "Smart Bare", ChildCount: 0, Smart: true, HasPoster: false, HasBackdrop: false, Summary: ""}
	counts := TitleCounts([]Collection{smartEmpty})
	want := []string{HealthNoCurrentMatches, HealthNoPoster, HealthNoBackdrop, HealthNoSummary}
	if got := CollectionFlags(smartEmpty, counts); !reflect.DeepEqual(got, want) {
		t.Errorf("smart empty flags = %v, want %v (never a bare EMPTY)", got, want)
	}
	smartFull := Collection{RatingKey: "10", Title: "Smart Full", ChildCount: 3, Smart: true, HasPoster: true, HasBackdrop: true, Summary: "s"}
	if got := CollectionFlags(smartFull, TitleCounts([]Collection{smartFull})); len(got) != 0 {
		t.Errorf("smart with matches flags = %v, want none", got)
	}
	fc := HealthFlagCounts([]Collection{smartEmpty})
	if fc[HealthNoCurrentMatches] != 1 || fc[HealthEmpty] != 0 {
		t.Errorf("flag counts = %v, want NO CURRENT MATCHES=1 and no EMPTY", fc)
	}
}

// smartFakePMS records every request and serves the PMS 1.43 smart
// shapes described in the file header.
type smartFakePMS struct {
	mu       sync.Mutex
	createQ  url.Values
	pathsHit []string
}

func (f *smartFakePMS) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.pathsHit = append(f.pathsHit, r.Method+" "+r.URL.Path)
	if r.URL.Path == "/library/collections" && r.Method == http.MethodPost {
		f.createQ = r.URL.Query()
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/library/collections" && r.Method == http.MethodPost:
		_, _ = w.Write([]byte(`{"MediaContainer":{"size":1,"Metadata":[{"ratingKey":500,"type":"collection","title":"Smart One","smart":"1","childCount":2}]}}`))
	case r.URL.Path == "/library/metadata/500" && r.Method == http.MethodGet:
		_, _ = w.Write([]byte(`{"MediaContainer":{"size":1,"Metadata":[{"ratingKey":500,"type":"collection","title":"Smart One","smart":"1","childCount":2,"librarySectionID":1,"content":"server://fake-machine/com.plexapp.plugins.library/library/sections/1/all?type=1&decade=2000"}]}}`))
	case r.URL.Path == "/library/metadata/500" && r.Method == http.MethodPut:
		_, _ = w.Write([]byte(`{"MediaContainer":{}}`))
	case r.URL.Path == "/library/collections/500/children":
		_, _ = w.Write([]byte(`{"MediaContainer":{"size":2,"Metadata":[{"ratingKey":1,"type":"movie","title":"Alpha Movie","year":2001},{"ratingKey":2,"type":"movie","title":"Beta Movie","year":2002}]}}`))
	case r.URL.Path == "/library/metadata/500/children":
		// The PMS 1.43 trap: smart members do NOT resolve here.
		_, _ = w.Write([]byte(`{"MediaContainer":{"size":0,"Metadata":[]}}`))
	case r.URL.Path == "/library/metadata/501" && r.Method == http.MethodGet:
		_, _ = w.Write([]byte(`{"MediaContainer":{"size":1,"Metadata":[{"ratingKey":501,"type":"collection","title":"Manual One","smart":0,"childCount":1,"librarySectionID":1}]}}`))
	case r.URL.Path == "/library/metadata/501/children":
		_, _ = w.Write([]byte(`{"MediaContainer":{"size":1,"Metadata":[{"ratingKey":7,"type":"movie","title":"Manual Member","year":1999}]}}`))
	case r.URL.Path == "/library/sections/1/collections":
		_, _ = w.Write([]byte(`{"MediaContainer":{"size":2,"Metadata":[
			{"ratingKey":500,"type":"collection","title":"Smart One","childCount":2,"smart":"1","thumb":"/library/metadata/500/thumb/1","art":"/library/metadata/500/art/1","summary":"s","content":"server://fake-machine/com.plexapp.plugins.library/library/sections/1/all?type=1&decade=2000"},
			{"ratingKey":501,"type":"collection","title":"Manual One","childCount":1,"smart":0,"thumb":"/library/metadata/501/thumb/1","summary":"s"}
		]}}`))
	case strings.HasPrefix(r.URL.Path, "/hubs/"):
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"MediaContainer":{}}`))
	default:
		_, _ = w.Write([]byte(`{"MediaContainer":{}}`))
	}
}

func smartTestClient(t *testing.T) (*Client, *Server, *smartFakePMS) {
	t.Helper()
	f := &smartFakePMS{}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	c := NewClient("P", "1", "test-client")
	server := &Server{Name: "S", Machine: "fake-machine", Token: "t", Connections: []Connection{{URI: srv.URL, Local: true}}}
	return c, server, f
}

func TestCreateCollectionSmartShape(t *testing.T) {
	c, server, f := smartTestClient(t)
	out, err := c.CreateCollection(context.Background(), server, CreatePayload{
		SectionKey: "1", Type: 1, Title: "Smart One", Smart: true,
		FilterQuery: "decade=2000",
	})
	if err != nil {
		t.Fatalf("CreateCollection(smart): %v", err)
	}
	if !strings.Contains(string(out), `"ratingKey":"500"`) {
		t.Errorf("create result = %s, want ratingKey 500", out)
	}
	q := f.createQ
	if q.Get("smart") != "1" || q.Get("type") != "1" || q.Get("sectionId") != "1" || q.Get("title") != "Smart One" {
		t.Errorf("create query = %v, want smart=1 type=1 sectionId=1 title", q)
	}
	wantURI := "server://fake-machine/com.plexapp.plugins.library/library/sections/1/all?type=1&decade=2000"
	if q.Get("uri") != wantURI {
		t.Errorf("create uri = %q, want %q", q.Get("uri"), wantURI)
	}
	// A smart create stores NO member list: no membership write of
	// any kind may follow it.
	for _, p := range f.pathsHit {
		if strings.Contains(p, "/items") {
			t.Errorf("smart create issued a membership write: %s (all hits: %v)", p, f.pathsHit)
		}
	}
}

func TestGetCollectionItemsSmartBranch(t *testing.T) {
	c, server, _ := smartTestClient(t)
	items, err := c.GetCollectionItems(context.Background(), server, "500")
	if err != nil {
		t.Fatalf("GetCollectionItems(smart): %v", err)
	}
	if len(items) != 2 || items[0].RatingKey != "1" || items[1].RatingKey != "2" {
		t.Errorf("smart members = %+v, want rk 1,2 via the collections path", items)
	}
	manual, err := c.GetCollectionItems(context.Background(), server, "501")
	if err != nil {
		t.Fatalf("GetCollectionItems(manual): %v", err)
	}
	if len(manual) != 1 || manual[0].RatingKey != "7" {
		t.Errorf("manual members = %+v, want rk 7 via the metadata path", manual)
	}
}

func TestGetCollectionsSmartSummary(t *testing.T) {
	c, server, _ := smartTestClient(t)
	cols, err := c.GetCollections(context.Background(), server, "1")
	if err != nil {
		t.Fatalf("GetCollections: %v", err)
	}
	if len(cols) != 2 {
		t.Fatalf("collections = %d, want 2", len(cols))
	}
	smart := cols[0]
	if !smart.Smart || smart.Content == "" || smart.SmartSummary != "Decade is 2000" {
		t.Errorf("smart listing entry = %+v, want smart + content + summary 'Decade is 2000'", smart)
	}
	if cols[1].Smart || cols[1].Content != "" || cols[1].SmartSummary != "" {
		t.Errorf("manual listing entry = %+v, want no smart fields", cols[1])
	}
	d, err := c.GetCollectionDetail(context.Background(), server, "500")
	if err != nil {
		t.Fatalf("GetCollectionDetail: %v", err)
	}
	if !d.Smart || d.SmartSummary != "Decade is 2000" {
		t.Errorf("detail = %+v, want smart + summary", d)
	}
}
