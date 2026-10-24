package overlays

// Restore-default with URL-keyed provider posters: on a real server
// the agent's posters (TMDB / TheTVDB) live on the agent's image CDN,
// so the provider entry's KEY is a full https URL, not a server-local
// path. The download of the clean poster — and the byte comparison
// that settles a restored item as already-default — must fetch those
// URLs directly (never with the Plex token) instead of feeding them
// to the server-path backup helper, whose "no poster path" refusal
// failed 276 of 401 items live. The CDN here is an httptest server;
// the sandbox PMS has no external agent posters, so this path is
// covered here and NOT by the live battery.

import (
	"bytes"
	"context"
	"image/color"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"plexcollectionmanager/internal/plex"
)

func urlProviderEntry(selected bool, url string) plex.PosterEntry {
	return plex.PosterEntry{Key: url, RatingKey: url, Provider: "tmdb", Selected: selected}
}

// posterCDN serves body (or status) for every request and records
// hits plus whether any request carried the Plex token header.
type posterCDN struct {
	server    *httptest.Server
	hits      int32
	tokenSeen int32
}

func newPosterCDN(t *testing.T, status int, body []byte) *posterCDN {
	t.Helper()
	c := &posterCDN{}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&c.hits, 1)
		if r.Header.Get("X-Plex-Token") != "" {
			atomic.AddInt32(&c.tokenSeen, 1)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(c.server.Close)
	return c
}

func (c *posterCDN) url() string          { return c.server.URL + "/t/p/original/clean.jpg" }
func (c *posterCDN) client() *http.Client { return c.server.Client() }

// (1) Restore succeeds with an https-keyed provider entry: the
// uploaded bytes are exactly the bytes the CDN served, the backup
// gate still ran first — and (3) afterwards the item settles at
// already-default instead of looping back to "restore" on every run
// (the selected entry is now the app's upload while the provider key
// is still the external URL; the byte comparison must fetch it).
func TestRestoreDefaultURLKeyedProviderRestoresAndSettles(t *testing.T) {
	dir := t.TempDir()
	bakedBytes := makePNG(t, 4, 6, color.White)
	cleanBytes := makePNG(t, 4, 6, color.Black)
	cdn := newPosterCDN(t, http.StatusOK, cleanBytes)
	providerURL := cdn.url()
	fake := &fakePlex{
		poster:       bakedBytes,
		posters:      []plex.PosterEntry{uploadEntry(true), urlProviderEntry(false, providerURL)},
		modelUploads: true,
	}
	a := newFakeApplier(dir, fake)
	a.posterHTTPClient = cdn.client()
	srv := testServer()
	ctx := context.Background()

	class, err := a.ClassifyItemPosters(ctx, srv, "7")
	if err != nil {
		t.Fatalf("ClassifyItemPosters: %v", err)
	}
	if class.Status != "restore" || class.DefaultKey != providerURL {
		t.Fatalf("classification = %+v, want restore with the URL as default key", class)
	}

	fake.events = nil
	hitsBefore := atomic.LoadInt32(&cdn.hits)
	outcome, backupPath, errMsg, err := a.RestoreDefaultPoster(ctx, srv, "1", 1, "7", "Item 7", restoreThumb)
	if err != nil {
		t.Fatalf("RestoreDefaultPoster: %v", err)
	}
	if outcome != "restored" || errMsg != "" {
		t.Fatalf("outcome = %q errMsg = %q, want restored", outcome, errMsg)
	}
	if len(fake.uploads) != 1 || !bytes.Equal(fake.uploads[0], cleanBytes) {
		t.Fatalf("uploads = %d, want exactly the CDN-served clean bytes uploaded once", len(fake.uploads))
	}
	if backupPath == "" || !a.VerifyBackup(srv, "7") {
		t.Fatalf("backup missing after restore (path %q)", backupPath)
	}
	// Server-side order: classify read → selected-entry fetch
	// (server) → backup fetch → verify read → served fetch. The two
	// provider fetches (classification refinement + the download)
	// went to the CDN instead — exactly two hits during the run.
	var seq []string
	for _, e := range fake.events {
		seq = append(seq, strings.SplitN(e, ":", 2)[0])
	}
	want := []string{"getposters", "fetch", "fetch", "getposters", "fetch"}
	if strings.Join(seq, ",") != strings.Join(want, ",") {
		t.Fatalf("event order = %v, want %v", seq, want)
	}
	if delta := atomic.LoadInt32(&cdn.hits) - hitsBefore; delta != 2 {
		t.Fatalf("CDN hits during restore = %d, want 2 (refine + download)", delta)
	}

	// (3) The loop case: re-classification settles at
	// already-default — the selected upload's bytes are compared
	// against the provider bytes fetched from the URL.
	after, err := a.ClassifyItemPosters(ctx, srv, "7")
	if err != nil {
		t.Fatalf("ClassifyItemPosters after restore: %v", err)
	}
	if after.Status != "already-default" {
		t.Fatalf("status after restore = %q, want already-default", after.Status)
	}
	if len(fake.uploads) != 1 {
		t.Fatalf("settled item must not be re-uploaded; uploads = %d", len(fake.uploads))
	}
	if atomic.LoadInt32(&cdn.tokenSeen) != 0 {
		t.Fatal("the Plex token was sent to the external poster host")
	}
}

// (2) The CDN answers 404: the item fails at the clean-poster
// download step, the reason names that step and the HTTP status, the
// backup helper's wording never appears, and nothing is uploaded.
func TestRestoreDefaultURLKeyedProviderHTTPError(t *testing.T) {
	dir := t.TempDir()
	cdn := newPosterCDN(t, http.StatusNotFound, []byte("<html>404 Not Found</html>"))
	fake := &fakePlex{
		poster:       makePNG(t, 4, 6, color.White),
		posters:      []plex.PosterEntry{uploadEntry(true), urlProviderEntry(false, cdn.url())},
		modelUploads: true,
	}
	a := newFakeApplier(dir, fake)
	a.posterHTTPClient = cdn.client()
	outcome, backupPath, errMsg, err := a.RestoreDefaultPoster(context.Background(), testServer(), "1", 1, "7", "Item 7", restoreThumb)
	if err != nil {
		t.Fatalf("RestoreDefaultPoster: %v", err)
	}
	if outcome != "failed" {
		t.Fatalf("outcome = %q, want failed", outcome)
	}
	if !strings.Contains(errMsg, "Could not download the clean provider poster") || !strings.Contains(errMsg, "HTTP 404") {
		t.Fatalf("errMsg = %q, want the download step named with the HTTP status", errMsg)
	}
	if strings.Contains(errMsg, "back up") {
		t.Fatalf("errMsg = %q, the backup helper's wording must not surface from the download step", errMsg)
	}
	if len(fake.uploads) != 0 {
		t.Fatalf("nothing may be uploaded when the clean poster cannot be downloaded; uploads = %d", len(fake.uploads))
	}
	if backupPath == "" {
		t.Fatal("the backup gate ran before the failed download and must be reported")
	}
}

// (4) An entry key that is neither a server path nor a web URL fails
// with the specific scheme error — never the backup wording.
func TestRestoreDefaultEntryKeyUnknownForm(t *testing.T) {
	dir := t.TempDir()
	weird := plex.PosterEntry{Key: "ftp://images.example.test/poster.jpg", RatingKey: "ftp://images.example.test/poster.jpg", Provider: "tmdb", Selected: false}
	fake := &fakePlex{
		poster:       makePNG(t, 4, 6, color.White),
		posters:      []plex.PosterEntry{uploadEntry(true), weird},
		modelUploads: true,
	}
	a := newFakeApplier(dir, fake)
	outcome, _, errMsg, err := a.RestoreDefaultPoster(context.Background(), testServer(), "1", 1, "7", "Item 7", restoreThumb)
	if err != nil {
		t.Fatalf("RestoreDefaultPoster: %v", err)
	}
	if outcome != "failed" || !strings.Contains(errMsg, "neither a server path nor a web URL") || !strings.Contains(errMsg, `"ftp"`) {
		t.Fatalf("outcome = %q errMsg = %q, want failed with the scheme named", outcome, errMsg)
	}
	if strings.Contains(errMsg, "back up") {
		t.Fatalf("errMsg = %q, the backup helper's wording must not surface", errMsg)
	}
	if len(fake.uploads) != 0 {
		t.Fatalf("uploads = %d, want none", len(fake.uploads))
	}
}

// The external fetcher itself: redirects are followed, non-image
// bytes are refused, and an oversized body is refused — each with a
// reason naming the download.
func TestFetchEntryPosterBytesExternal(t *testing.T) {
	cleanBytes := makePNG(t, 4, 6, color.Black)
	mux := http.NewServeMux()
	mux.HandleFunc("/old.jpg", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/clean.jpg", http.StatusFound)
	})
	mux.HandleFunc("/clean.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(cleanBytes)
	})
	mux.HandleFunc("/notimage.jpg", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>definitely not a poster</html>"))
	})
	mux.HandleFunc("/huge.jpg", func(w http.ResponseWriter, r *http.Request) {
		body := append([]byte{}, cleanBytes...)
		body = append(body, make([]byte, maxEntryPosterBytes)...)
		_, _ = w.Write(body)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	a := &Applier{userDataDir: t.TempDir(), posterHTTPClient: ts.Client()}
	ctx := context.Background()

	got, _, err := a.fetchEntryPosterBytes(ctx, testServer(), plex.PosterEntry{Key: ts.URL + "/old.jpg"})
	if err != nil {
		t.Fatalf("redirected fetch: %v", err)
	}
	if !bytes.Equal(got, cleanBytes) {
		t.Fatalf("redirected fetch returned %d bytes, want the clean poster's %d", len(got), len(cleanBytes))
	}
	if _, _, err := a.fetchEntryPosterBytes(ctx, testServer(), plex.PosterEntry{Key: ts.URL + "/notimage.jpg"}); err == nil || !strings.Contains(errMsg(err), "not a supported image") {
		t.Fatalf("non-image fetch err = %v, want a not-a-supported-image refusal", err)
	}
	if _, _, err := a.fetchEntryPosterBytes(ctx, testServer(), plex.PosterEntry{Key: ts.URL + "/huge.jpg"}); err == nil || !strings.Contains(errMsg(err), "too large") {
		t.Fatalf("oversize fetch err = %v, want a too-large refusal", err)
	}
	if _, _, err := a.fetchEntryPosterBytes(ctx, testServer(), plex.PosterEntry{}); err == nil || !strings.Contains(errMsg(err), "neither a server path nor a web URL") {
		t.Fatalf("empty key err = %v, want the form error", err)
	}
}
