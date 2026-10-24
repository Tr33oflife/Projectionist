package plex

// Classification tests for the restore-default posters operation,
// plus GetPosters parsing against an httptest fake PMS.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClassifyPosters(t *testing.T) {
	up := func(sel bool) PosterEntry {
		return PosterEntry{RatingKey: "upload://posters/a", Provider: "upload", Selected: sel}
	}
	prov := func(sel bool) PosterEntry {
		return PosterEntry{RatingKey: "metadata://posters/x", Provider: "tmdb", Selected: sel}
	}
	// These pin the PURE verdicts. The overlays layer refines them:
	// notably "already default with an upload present" flips to
	// "restore" there, because a coexisting upload means the
	// displayed image cannot be proven clean from the list alone.
	cases := []struct {
		name    string
		entries []PosterEntry
		want    string
		defKey  string
	}{
		{"provider selected, no uploads", []PosterEntry{prov(true)}, "already-default", "metadata://posters/x"},
		{"upload first then provider, upload selected", []PosterEntry{up(true), prov(false)}, "restore", "metadata://posters/x"},
		{"provider first, upload selected", []PosterEntry{prov(false), up(true)}, "restore", "metadata://posters/x"},
		{"uploads only", []PosterEntry{up(true), {RatingKey: "upload://posters/b", Provider: "upload"}}, "no-provider", ""},
		{"already default with an upload present", []PosterEntry{up(false), prov(true)}, "already-default", "metadata://posters/x"},
		{"empty list", nil, "no-provider", ""},
		{"nothing selected, provider exists", []PosterEntry{up(false), prov(false)}, "restore", "metadata://posters/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyPosters(tc.entries)
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q (%+v)", got.Status, tc.want, got)
			}
			if got.DefaultRatingKey != tc.defKey {
				t.Fatalf("default = %q, want %q", got.DefaultRatingKey, tc.defKey)
			}
		})
	}
}

// Local posters carry no provider field on a real PMS (verified live,
// PMS 1.43): the ratingKey scheme decides. A media:// poster is a
// provider-side poster (not an upload); an upload:// poster is one.
func TestClassifyPostersSchemeFallback(t *testing.T) {
	entries := []PosterEntry{
		{RatingKey: "upload://posters/a", Selected: true},
		{RatingKey: "media://3/bundle/Contents/Thumbnails/thumb1.jpg"},
	}
	got := ClassifyPosters(entries)
	if got.Status != "restore" || got.DefaultProvider != "media" {
		t.Fatalf("got %+v, want restore with media default", got)
	}
	if !entries[0].IsUpload() || entries[1].IsUpload() {
		t.Fatalf("IsUpload: upload=%v media=%v", entries[0].IsUpload(), entries[1].IsUpload())
	}
}

func fakePostersPMS(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/library/metadata/7/posters":
			_, _ = w.Write([]byte(`{"MediaContainer":{"Metadata":[
				{"key":"/library/metadata/7/file?url=upload%3A%2F%2Fposters%2Fa","ratingKey":"upload://posters/a","selected":true,"thumb":"/library/metadata/7/thumb/1"},
				{"key":"https://image.example.test/t/p/original/x.jpg","ratingKey":"media://3/x","thumb":"/library/metadata/7/file?url=media%3A%2F%2F3%2Fx"}
			]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestGetPostersParsing(t *testing.T) {
	srv := fakePostersPMS(t)
	defer srv.Close()
	c := NewClient("P", "1", "test-client")
	server := &Server{Name: "S", Token: "fake", Connections: []Connection{{URI: srv.URL}}}
	entries, err := c.GetPosters(context.Background(), server, "7")
	if err != nil {
		t.Fatalf("GetPosters: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Provider != "upload" || !entries[0].Selected || !entries[0].IsUpload() {
		t.Fatalf("entry 0 = %+v", entries[0])
	}
	if entries[1].Provider != "media" || entries[1].IsUpload() {
		t.Fatalf("entry 1 = %+v (provider must fall back to the ratingKey scheme)", entries[1])
	}
	// The thumb attribute is captured verbatim: for an entry whose
	// Key is an external URL it is the server-local path Plex serves
	// the image through.
	if entries[0].Thumb != "/library/metadata/7/thumb/1" || entries[1].Thumb != "/library/metadata/7/file?url=media%3A%2F%2F3%2Fx" {
		t.Fatalf("thumbs = %q / %q", entries[0].Thumb, entries[1].Thumb)
	}
	class := ClassifyPosters(entries)
	if class.Status != "restore" || class.DefaultRatingKey != "media://3/x" {
		t.Fatalf("classification = %+v", class)
	}
}
