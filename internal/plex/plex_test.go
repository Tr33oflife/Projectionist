package plex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// TestTagList mirrors the JS regression test 'v0.4 plex-client.tagList
// parses Tag[] defensively'.
func TestTagList(t *testing.T) {
	cases := []struct {
		name string
		raw  []any
		want []string
	}{
		{"tag objects", []any{map[string]any{"tag": "Action"}, map[string]any{"tag": "Drama"}}, []string{"Action", "Drama"}},
		{"plain string", []any{"Warner Bros."}, []string{"Warner Bros."}},
		{"nil", nil, []string{}},
		{"empty and missing entries skipped", []any{map[string]any{"tag": ""}, map[string]any{"nope": float64(1)}, "Sci-Fi"}, []string{"Sci-Fi"}},
		{"whitespace trimmed", []any{map[string]any{"tag": "  Horror  "}}, []string{"Horror"}},
		{"non-string tag values skipped", []any{map[string]any{"tag": float64(3)}, map[string]any{"tag": true}}, []string{}},
		{"whitespace-only strings skipped", []any{"   "}, []string{}},
	}
	for _, tc := range cases {
		if got := TagList(tc.raw); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: TagList = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRankConnectionsOrdering(t *testing.T) {
	in := []Connection{
		{URI: "https://relay.example/1", Relay: true},
		{URI: "https://203.0.113.9:32400", Local: false, Relay: false},
		{URI: "http://192.168.1.10:32400", Local: true},
	}
	out := RankConnections(in)
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3", len(out))
	}
	if out[0].URI != "http://192.168.1.10:32400" || out[1].URI != "https://203.0.113.9:32400" || out[2].URI != "https://relay.example/1" {
		t.Fatalf("order = %v, want local < remote < relay", []string{out[0].URI, out[1].URI, out[2].URI})
	}
	// Only the winner carries quality/label.
	if out[0].Quality != "local" || out[0].Label != "Local network" {
		t.Fatalf("winner quality/label = %q/%q", out[0].Quality, out[0].Label)
	}
	if out[1].Quality != "" || out[2].Quality != "" {
		t.Fatalf("non-winners must not carry quality: %q %q", out[1].Quality, out[2].Quality)
	}
	// Input is not mutated.
	if in[0].Quality != "" || in[2].Quality != "" {
		t.Fatalf("input mutated: %+v", in)
	}
	// Winner labels for remote and relay winners.
	remote := RankConnections([]Connection{{URI: "https://203.0.113.9:32400"}})
	if remote[0].Quality != "remote" || remote[0].Label != "Remote access" {
		t.Fatalf("remote winner = %q/%q", remote[0].Quality, remote[0].Label)
	}
	relay := RankConnections([]Connection{{URI: "https://relay.example/1", Relay: true}})
	if relay[0].Quality != "relay" || relay[0].Label != "Relay (slow)" {
		t.Fatalf("relay winner = %q/%q", relay[0].Quality, relay[0].Label)
	}
}

func TestRankConnectionsHeuristicFallback(t *testing.T) {
	// No local/relay flags at all: RFC1918 and plex.direct private hosts
	// count as local, relay in the URI counts as relay.
	in := []Connection{
		{URI: "https://plex-relay.example/abc"},
		{URI: "https://203.0.113.9:32400"},
		{URI: "https://abcdef1234.plex.direct:32400"},
		{URI: "http://192.168.1.8:32400"},
		{URI: "http://nas.local:32400"},
	}
	out := RankConnections(in)
	if !out[0].Local {
		t.Fatalf("winner should be heuristic-local, got %+v", out[0])
	}
	locals := 0
	for _, c := range out {
		if c.Local {
			locals++
		}
	}
	if locals != 3 {
		t.Fatalf("heuristic locals = %d, want 3 (plex.direct, 10.x, .local)", locals)
	}
	last := out[len(out)-1]
	if !last.Relay || !strings.Contains(last.URI, "relay") {
		t.Fatalf("last should be the heuristic relay, got %+v", last)
	}
	// A flagged-local connection skips the heuristic entirely: a "relay"
	// substring in its URI must NOT demote it.
	flagged := RankConnections([]Connection{
		{URI: "https://203.0.113.9:32400"},
		{URI: "http://relay-named-host:32400", Local: true},
	})
	if flagged[0].URI != "http://relay-named-host:32400" || flagged[0].Relay {
		t.Fatalf("flagged local must win and stay non-relay: %+v", flagged[0])
	}
}

// affinityTransport fails every request to the dead host at the network
// level and delegates everything else to the real transport, counting
// attempts per host key.
type affinityTransport struct {
	deadAttempts atomic.Int32
	liveAttempts atomic.Int32
	base         http.RoundTripper
}

func (a *affinityTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Host, "dead.invalid") {
		a.deadAttempts.Add(1)
		return nil, errors.New("unreachable")
	}
	a.liveAttempts.Add(1)
	return a.base.RoundTrip(req)
}

// TestConnectionAffinity mirrors the JS regression test 'v0.4.4 serverFetch
// remembers the winning connection (affinity)': with httptest fixtures —
// one dead URL, one live — the first call tries both, the second call goes
// straight to the winner.
func TestConnectionAffinity(t *testing.T) {
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"1","title":"Movies","type":"movie","uuid":"u1"}]}}`))
	}))
	defer live.Close()

	at := &affinityTransport{base: http.DefaultTransport}
	c := NewClient("P", "1", "test-client")
	c.httpClient = &http.Client{Transport: at}
	server := &Server{
		Name: "S", Machine: "m1", Token: "t",
		Connections: []Connection{
			{URI: "http://dead.invalid:32400", Local: true},
			{URI: live.URL, Local: false},
		},
	}
	sections, err := c.GetSections(context.Background(), server)
	if err != nil {
		t.Fatalf("first GetSections: %v", err)
	}
	if len(sections) != 1 || sections[0].Title != "Movies" {
		t.Fatalf("sections = %+v", sections)
	}
	if at.deadAttempts.Load() != 1 || at.liveAttempts.Load() != 1 {
		t.Fatalf("first call attempts dead=%d live=%d, want 1/1", at.deadAttempts.Load(), at.liveAttempts.Load())
	}
	if _, err := c.GetSections(context.Background(), server); err != nil {
		t.Fatalf("second GetSections: %v", err)
	}
	if at.deadAttempts.Load() != 1 {
		t.Fatalf("affinity broken: dead retried, attempts = %d", at.deadAttempts.Load())
	}
	if at.liveAttempts.Load() != 2 {
		t.Fatalf("second call live attempts = %d, want 2", at.liveAttempts.Load())
	}
}

// TestCancelledContext mirrors the JS regression test 'v0.4.1 serverFetch
// abort surfaces cancelled, not connection fallback'.
func TestCancelledContext(t *testing.T) {
	ct := &countingTransport{}
	c := NewClient("P", "1", "test-client")
	c.httpClient = &http.Client{Transport: ct}
	server := &Server{Name: "S", Machine: "m", Token: "t", Connections: []Connection{{URI: "http://127.0.0.1:9", Local: true}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already aborted before the request starts
	_, err := c.GetSections(ctx, server)
	var perr *Error
	if !errors.As(err, &perr) {
		t.Fatalf("err type = %T (%v), want *Error", err, err)
	}
	if !perr.Cancelled || perr.Message != "Request cancelled" {
		t.Fatalf("cancelled error = %+v", perr)
	}
	if ct.calls.Load() != 0 {
		t.Fatalf("cancelled request hit the transport %d times", ct.calls.Load())
	}
}

// TestUncertainWriteNeverReplayed mirrors the JS regression test
// 'uncertain write is never retried on another connection'.
func TestUncertainWriteNeverReplayed(t *testing.T) {
	ct := &countingTransport{tripErr: errors.New("timeout")}
	c := NewClient("P", "1", "test-client")
	c.httpClient = &http.Client{Transport: ct}
	server := &Server{Name: "S", Token: "secret", Connections: []Connection{{URI: "http://a.invalid"}, {URI: "http://b.invalid"}}}
	_, err := c.serverFetch(context.Background(), server, "/library/collections", fetchOptions{method: "POST", params: map[string]string{"type": "1", "title": "T", "smart": "0", "sectionId": "1", "uri": "u"}})
	if err == nil || !strings.Contains(err.Error(), "Write outcome unknown") {
		t.Fatalf("err = %v, want Write outcome unknown", err)
	}
	if ct.calls.Load() != 1 {
		t.Fatalf("write attempts = %d, want exactly 1", ct.calls.Load())
	}
}

// TestCollectionProofBlocksMediaItem mirrors the JS regression test
// 'safety: a media item cannot be edited through collection routes'.
func TestCollectionProofBlocksMediaItem(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"42","type":"movie"}]}}`))
	}))
	defer srv.Close()
	c := NewClient("P", "1", "test-client")
	server := &Server{Name: "S", Token: "fake", Connections: []Connection{{URI: srv.URL}}}
	title := "x"
	err := c.UpdateCollectionMeta(context.Background(), server, "42", MetaFields{Title: &title})
	if err == nil || !strings.Contains(err.Error(), "cannot target a media item") {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(methods, []string{"GET"}) {
		t.Fatalf("methods = %v, want only the GET proof", methods)
	}
}

// TestPosterWritesRequireBackup mirrors the JS regression test
// 'safety: poster uploads require a verified backup before networking'.
func TestPosterWritesRequireBackup(t *testing.T) {
	ct := &countingTransport{}
	c := NewClient("P", "1", "test-client") // no PosterBackupVerifier connected
	c.httpClient = &http.Client{Transport: ct}
	server := &Server{Name: "S", Token: "fake", BestConnection: Connection{URI: "http://fixture.invalid"}}
	if err := c.SetPosterFromURL(context.Background(), server, "42", "https://example.com/poster.png"); err == nil || !strings.Contains(err.Error(), "verified original backup") {
		t.Fatalf("SetPosterFromURL err = %v", err)
	}
	if err := c.UploadPosterBytes(context.Background(), server, "42", []byte("image")); err == nil || !strings.Contains(err.Error(), "verified original backup") {
		t.Fatalf("UploadPosterBytes err = %v", err)
	}
	if ct.calls.Load() != 0 {
		t.Fatalf("poster writes reached the network %d times", ct.calls.Load())
	}
	// A verifier that approves a NON-numeric key still cannot open the gate.
	c.PosterBackupVerifier = func(s *Server, rk string) bool { return true }
	if err := c.SetPosterFromURL(context.Background(), server, "abc", "https://example.com/p.png"); err == nil || !strings.Contains(err.Error(), "verified original backup") {
		t.Fatalf("non-numeric key err = %v", err)
	}
}

// TestPaginationBeyondFirstPage mirrors the JS regression test
// 'pagination includes members beyond first page'.
func TestPaginationBeyondFirstPage(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// v1.14: GetCollectionItems first reads the collection
		// record to learn whether it is smart (smart members resolve
		// through a different children path on PMS 1.43). That probe
		// is not a page call — count only requests carrying paging
		// parameters. The pagination contract this test pins (two
		// page calls, members beyond page one included) is unchanged.
		if r.URL.Query().Get("X-Plex-Container-Start") != "" {
			calls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("X-Plex-Container-Start") == "0" {
			_, _ = w.Write([]byte(`{"MediaContainer":{"totalSize":3,"Metadata":[{"ratingKey":1,"title":"A"},{"ratingKey":2,"title":"B"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"MediaContainer":{"totalSize":3,"Metadata":[{"ratingKey":3,"title":"C"}]}}`))
	}))
	defer srv.Close()
	c := NewClient("P", "1", "test-client")
	server := &Server{Name: "S", Token: "t", Connections: []Connection{{URI: srv.URL}}}
	items, err := c.GetCollectionItems(context.Background(), server, "9")
	if err != nil {
		t.Fatalf("GetCollectionItems: %v", err)
	}
	if len(items) != 3 || items[2].RatingKey != "3" {
		t.Fatalf("items = %+v", items)
	}
	if calls.Load() != 2 {
		t.Fatalf("page calls = %d, want 2", calls.Load())
	}
}

// TestGetServersFixture exercises discovery mapping against an httptest
// plex.tv fixture: non-servers and tokenless resources are filtered, and
// the winner connection is ranked + labelled.
func TestGetServersFixture(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/resources" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"name":"Player","provides":"client","accessToken":"x","clientIdentifier":"p1","connections":[{"uri":"http://192.168.1.9:32400","local":true}]},
			{"name":"NoToken","provides":"server","clientIdentifier":"s0","connections":[{"uri":"http://192.168.1.9:32400","local":true}]},
			{"name":"Main","provides":"server","accessToken":"srv-token","clientIdentifier":"m1","owned":true,"connections":[
				{"uri":"https://relay.example/abc","local":false,"relay":true},
				{"uri":"http://192.168.1.20:32400","local":true,"relay":false},
				{"uri":"ftp://bad-scheme","local":false,"relay":false}
			]}
		]`))
	}))
	defer srv.Close()
	c := NewClient("P", "1", "test-client")
	c.baseURL = srv.URL
	servers, err := c.GetServers(context.Background(), "user-token")
	if err != nil {
		t.Fatalf("GetServers: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("servers = %d, want 1", len(servers))
	}
	s := servers[0]
	if s.Name != "Main" || s.Machine != "m1" || s.Token != "srv-token" || !s.Owned {
		t.Fatalf("server = %+v", s)
	}
	if len(s.Connections) != 2 {
		t.Fatalf("connections = %+v, want 2 (bad scheme filtered)", s.Connections)
	}
	if s.BestConnection.URI != "http://192.168.1.20:32400" || s.BestConnection.Quality != "local" || s.BestConnection.Label != "Local network" {
		t.Fatalf("bestConnection = %+v", s.BestConnection)
	}
}

// TestFetchServed verifies the raw GET contract on a single-connection
// server (the walk degenerates to the one candidate): token header
// only, body / content-type / status passthrough, no throw on HTTP
// errors.
func TestFetchServed(t *testing.T) {
	var gotToken, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Plex-Token")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "image/png; charset=binary")
		w.WriteHeader(404)
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()
	c := NewClient("P", "1", "test-client")
	server := &Server{Name: "S", Token: "tok", Connections: []Connection{{URI: srv.URL}}}
	body, ct, status, err := c.FetchServed(context.Background(), server, "/library/metadata/1/thumb/2", 0)
	if err != nil {
		t.Fatalf("FetchServed: %v", err)
	}
	if status != 404 || string(body) != "nope" || ct != "image/png" {
		t.Fatalf("FetchServed = status %d ct %q body %q", status, ct, body)
	}
	if gotToken != "tok" || gotPath != "/library/metadata/1/thumb/2" {
		t.Fatalf("request token/path = %q %q", gotToken, gotPath)
	}
}
