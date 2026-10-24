package plex

// manager_test.go — v1.13 collections manager: health flags, the
// detail read (labels absent vs empty, visibility known vs unknown),
// the metadata write's parameter forms, and the backdrop upload's
// gate. All against httptest fakes — no network, no real server.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// TestCollectionFlagsExact pins each flag to its fact, plus the clean
// collection that earns none, plus the aggregate counts the health
// view's summary line shows.
func TestCollectionFlagsExact(t *testing.T) {
	clean := Collection{RatingKey: "1", Title: "Clean", ChildCount: 3, HasPoster: true, HasBackdrop: true, Summary: "Written."}
	empty := Collection{RatingKey: "2", Title: "Bare", ChildCount: 0, HasPoster: false, HasBackdrop: false, Summary: ""}
	dupA := Collection{RatingKey: "3", Title: "Twin", ChildCount: 1, HasPoster: true, HasBackdrop: true, Summary: "s"}
	dupB := Collection{RatingKey: "4", Title: "Twin", ChildCount: 2, HasPoster: true, HasBackdrop: false, Summary: "s"}
	cols := []Collection{clean, empty, dupA, dupB}
	counts := TitleCounts(cols)
	if got := CollectionFlags(clean, counts); len(got) != 0 {
		t.Errorf("clean collection flags = %v, want none", got)
	}
	wantEmpty := []string{HealthEmpty, HealthNoPoster, HealthNoBackdrop, HealthNoSummary}
	if got := CollectionFlags(empty, counts); !reflect.DeepEqual(got, wantEmpty) {
		t.Errorf("bare collection flags = %v, want %v", got, wantEmpty)
	}
	if got := CollectionFlags(dupA, counts); !reflect.DeepEqual(got, []string{HealthDuplicateName}) {
		t.Errorf("dup A flags = %v, want [DUPLICATE TITLE]", got)
	}
	if got := CollectionFlags(dupB, counts); !reflect.DeepEqual(got, []string{HealthNoBackdrop, HealthDuplicateName}) {
		t.Errorf("dup B flags = %v, want [NO BACKDROP DUPLICATE TITLE]", got)
	}
	fc := HealthFlagCounts(cols)
	wantCounts := map[string]int{HealthEmpty: 1, HealthNoPoster: 1, HealthNoBackdrop: 2, HealthNoSummary: 1, HealthDuplicateName: 2}
	if !reflect.DeepEqual(fc, wantCounts) {
		t.Errorf("flag counts = %v, want %v", fc, wantCounts)
	}
}

// fakeManagerPMS serves the manager reads: a collections list and a
// 404ing manage endpoint (the PMS 1.43 shape).
func fakeManagerPMS(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/hubs/"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"MediaContainer":{}}`))
		case r.URL.Path == "/library/sections/1/collections":
			_, _ = w.Write([]byte(`{"MediaContainer":{"size":2,"Metadata":[
				{"ratingKey":4,"type":"collection","title":"Poster Uploaded A","childCount":1,"smart":0,
				 "thumb":"/library/metadata/4/thumb/1792708508","art":"/library/metadata/4/art/1792708508","summary":"Has one."},
				{"ratingKey":6,"type":"collection","title":"No Poster C","childCount":0,"smart":0,
				 "thumb":"/library/collections/6/composite/1792708508?width=400&height=600","summary":""}
			]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestGetCollectionsHealthFacts(t *testing.T) {
	srv := fakeManagerPMS(t)
	defer srv.Close()
	c := NewClient("P", "1", "test-client")
	server := &Server{Name: "S", Token: "t", Connections: []Connection{{URI: srv.URL}}}
	cols, err := c.GetCollections(context.Background(), server, "1")
	if err != nil {
		t.Fatalf("GetCollections: %v", err)
	}
	if len(cols) != 2 {
		t.Fatalf("collections = %d, want 2", len(cols))
	}
	a, n := cols[0], cols[1]
	if !a.HasPoster || !a.HasBackdrop {
		t.Errorf("custom-art collection: HasPoster=%v HasBackdrop=%v, want true/true", a.HasPoster, a.HasBackdrop)
	}
	if n.HasPoster || n.HasBackdrop {
		t.Errorf("composite collection: HasPoster=%v HasBackdrop=%v, want false/false (composite thumb is not a poster)", n.HasPoster, n.HasBackdrop)
	}
	// The manage endpoint 404'd: visibility is NOT KNOWN on either.
	if a.VisibilityKnown || n.VisibilityKnown {
		t.Errorf("VisibilityKnown = %v/%v, want false/false when the manage read 404s", a.VisibilityKnown, n.VisibilityKnown)
	}
	flags := CollectionFlags(n, TitleCounts(cols))
	want := []string{HealthEmpty, HealthNoPoster, HealthNoBackdrop, HealthNoSummary}
	if !reflect.DeepEqual(flags, want) {
		t.Errorf("rk6 flags = %v, want %v", flags, want)
	}
}

// detailPMS serves one collection metadata record; the record body is
// supplied by the test so label/visibility variants share the rig.
func detailPMS(t *testing.T, record string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/hubs/"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"MediaContainer":{}}`))
		case r.URL.Path == "/library/metadata/42":
			_, _ = w.Write([]byte(record))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestGetCollectionDetailLabelsAbsentVsEmpty(t *testing.T) {
	c := NewClient("P", "1", "test-client")
	// No Label key at all (the PMS 1.43 collection shape): labels must
	// stay UNKNOWN (nil), and visibility unknown with the manage 404.
	srv := detailPMS(t, `{"MediaContainer":{"Metadata":[{"ratingKey":42,"type":"collection","title":"C","titleSort":"C sort","summary":"s","contentRating":"PG","childCount":2,"librarySectionID":1,"thumb":"/library/metadata/42/thumb/1"}]}}`)
	defer srv.Close()
	server := &Server{Name: "S", Token: "t", Connections: []Connection{{URI: srv.URL}}}
	d, err := c.GetCollectionDetail(context.Background(), server, "42")
	if err != nil {
		t.Fatalf("GetCollectionDetail: %v", err)
	}
	if d.Labels != nil {
		t.Errorf("Labels = %v, want nil (server returned no Label array — never invent one)", *d.Labels)
	}
	if d.Visibility != "unknown" || d.VisibilityKnown || d.VisibilityNote == "" {
		t.Errorf("visibility = %q known=%v note=%q, want unknown/false/with reason", d.Visibility, d.VisibilityKnown, d.VisibilityNote)
	}
	if d.TitleSort != "C sort" || d.ContentRating != "PG" || d.ChildCount != 2 {
		t.Errorf("detail fields wrong: %+v", d)
	}
	// Label present but empty: a different fact — non-nil, zero labels.
	srv2 := detailPMS(t, `{"MediaContainer":{"Metadata":[{"ratingKey":42,"type":"collection","title":"C","librarySectionID":1,"Label":[]}]}}`)
	defer srv2.Close()
	server2 := &Server{Name: "S", Token: "t", Connections: []Connection{{URI: srv2.URL}}}
	d2, err := c.GetCollectionDetail(context.Background(), server2, "42")
	if err != nil {
		t.Fatalf("GetCollectionDetail (empty labels): %v", err)
	}
	if d2.Labels == nil || len(*d2.Labels) != 0 {
		t.Errorf("Labels = %v, want non-nil empty slice", d2.Labels)
	}
	// Promotion flags ON the record: visibility known from the record.
	srv3 := detailPMS(t, `{"MediaContainer":{"Metadata":[{"ratingKey":42,"type":"collection","title":"C","librarySectionID":1,"promotedToOwnHome":1,"promotedToSharedHome":0}]}}`)
	defer srv3.Close()
	server3 := &Server{Name: "S", Token: "t", Connections: []Connection{{URI: srv3.URL}}}
	d3, err := c.GetCollectionDetail(context.Background(), server3, "42")
	if err != nil {
		t.Fatalf("GetCollectionDetail (promoted): %v", err)
	}
	if !d3.VisibilityKnown || d3.Visibility != "home" {
		t.Errorf("visibility = %q known=%v, want home/true", d3.Visibility, d3.VisibilityKnown)
	}
}

// TestUpdateCollectionMetaParamForms pins the wire forms the sandbox
// PMS proved: plain title/summary, .value+.locked for titleSort and
// contentRating (plain titleSort never sticks there), comma-joined
// label.value.
func TestUpdateCollectionMetaParamForms(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			gotQuery = r.URL.Query()
		}
		_, _ = w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":42,"type":"collection","librarySectionID":1}]}}`))
	}))
	defer srv.Close()
	c := NewClient("P", "1", "test-client")
	server := &Server{Name: "S", Token: "t", Connections: []Connection{{URI: srv.URL}}}
	title, sort, summary, rating := "New Title", "New Sort", "New summary", "PG"
	labels := []string{"Alpha", "Beta"}
	err := c.UpdateCollectionMeta(context.Background(), server, "42", MetaFields{
		Title: &title, TitleSort: &sort, Summary: &summary, ContentRating: &rating, Labels: &labels,
	})
	if err != nil {
		t.Fatalf("UpdateCollectionMeta: %v", err)
	}
	want := map[string]string{
		"title": "New Title", "summary": "New summary",
		"titleSort.value": "New Sort", "titleSort.locked": "1",
		"contentRating.value": "PG", "contentRating.locked": "1",
		"label.value": "Alpha,Beta", "label.locked": "1",
	}
	for k, v := range want {
		if gotQuery.Get(k) != v {
			t.Errorf("param %s = %q, want %q (full query: %s)", k, gotQuery.Get(k), v, gotQuery.Encode())
		}
	}
	if _, present := gotQuery["titleSort"]; present {
		t.Errorf("plain titleSort must not be sent — it is a silent no-op on PMS 1.43 (query: %s)", gotQuery.Encode())
	}
}

// TestUploadArtBytesGateAndPath: the backdrop upload refuses to touch
// the network without a verified backup, and with one it posts
// multipart to /arts.
func TestUploadArtBytesGateAndPath(t *testing.T) {
	ct := &countingTransport{}
	c := NewClient("P", "1", "test-client")
	c.httpClient = &http.Client{Transport: ct}
	server := &Server{Name: "S", Token: "fake", Connections: []Connection{{URI: "http://fixture.invalid"}}}
	if err := c.UploadArtBytes(context.Background(), server, "42", []byte("image")); err == nil || !strings.Contains(err.Error(), "verified original backup") {
		t.Fatalf("UploadArtBytes without verifier err = %v", err)
	}
	if ct.calls.Load() != 0 {
		t.Fatalf("gated upload reached the network %d times", ct.calls.Load())
	}

	var gotPath, gotCT string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c2 := NewClient("P", "1", "test-client")
	c2.PosterBackupVerifier = func(s *Server, rk string) bool { return true }
	server2 := &Server{Name: "S", Token: "t", Connections: []Connection{{URI: srv.URL}}}
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 1, 2, 3}
	if err := c2.UploadArtBytes(context.Background(), server2, "42", png); err != nil {
		t.Fatalf("UploadArtBytes: %v", err)
	}
	if gotPath != "/library/metadata/42/arts" {
		t.Errorf("upload path = %q, want /library/metadata/42/arts", gotPath)
	}
	if !strings.HasPrefix(gotCT, "multipart/form-data; boundary=") || !strings.Contains(string(gotBody), `name="file"; filename="backdrop.png"`) {
		t.Errorf("upload was not the expected multipart file form: ct=%q body=%q", gotCT, gotBody)
	}
}
