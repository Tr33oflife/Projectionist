package overlays

// Restore-default with URL-keyed provider posters, continued
// (v1.8.6): five shows failed live with "Poster download returned
// HTTP 302" — TheTVDB's CDN answers the app's bare direct GET with
// a redirect that never lands. Plex itself retrieves those posters
// for the picker, so the entry fetcher now tries the entry's own
// server-local thumb path through Plex FIRST (Plex proxies its own
// agent posters), and only then fetches directly — with a browser
// User-Agent, and errors that name the final address a redirect
// chain landed on. The CDN here is an httptest server; the sandbox
// PMS has no external agent posters, so these paths are covered
// here and NOT by the live battery.

import (
	"bytes"
	"context"
	"image/color"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"plexcollectionmanager/internal/plex"
)

func urlProviderEntryWithThumb(url, thumb string) plex.PosterEntry {
	return plex.PosterEntry{Key: url, RatingKey: url, Provider: "tvdb", Thumb: thumb, Selected: false}
}

const proxiedThumbPath = "/library/metadata/7/file?url=metadata%3A%2F%2Fposters%2Fxyz"

// (1) External key + server-local thumb: the bytes come from Plex's
// own serving path and the CDN is never contacted — proven by the
// CDN serving DIFFERENT bytes than the proxied path does. The
// post-restore settle also stays on the server path.
func TestRestoreDefaultURLKeyedProviderViaPlexThumb(t *testing.T) {
	dir := t.TempDir()
	bakedBytes := makePNG(t, 4, 6, color.White)
	cleanBytes := makePNG(t, 4, 6, color.Black)
	cdnBytes := makePNG(t, 4, 6, color.RGBA{0, 0, 255, 255})
	cdn := newPosterCDN(t, http.StatusOK, cdnBytes)
	fake := &fakePlex{
		poster:       bakedBytes,
		posters:      []plex.PosterEntry{uploadEntry(true), urlProviderEntryWithThumb(cdn.url(), proxiedThumbPath)},
		posterBytes:  map[string][]byte{proxiedThumbPath: cleanBytes},
		modelUploads: true,
	}
	a := newFakeApplier(dir, fake)
	a.posterHTTPClient = cdn.client()
	srv := testServer()
	ctx := context.Background()

	outcome, _, errMsg, err := a.RestoreDefaultPoster(ctx, srv, "1", 2, "7", "Item 7", restoreThumb)
	if err != nil {
		t.Fatalf("RestoreDefaultPoster: %v", err)
	}
	if outcome != "restored" || errMsg != "" {
		t.Fatalf("outcome = %q errMsg = %q, want restored", outcome, errMsg)
	}
	if len(fake.uploads) != 1 || !bytes.Equal(fake.uploads[0], cleanBytes) {
		t.Fatalf("uploads = %d, want exactly the Plex-served clean bytes uploaded once", len(fake.uploads))
	}
	if atomic.LoadInt32(&cdn.hits) != 0 {
		t.Fatalf("CDN was contacted %d times; the proxied thumb path must serve the poster", cdn.hits)
	}
	proxied := false
	for _, e := range fake.events {
		if e == "fetch:"+proxiedThumbPath {
			proxied = true
		}
	}
	if !proxied {
		t.Fatalf("the entry's thumb path was never fetched through the server; events = %v", fake.events)
	}
	after, err := a.ClassifyItemPosters(ctx, srv, "7")
	if err != nil {
		t.Fatalf("ClassifyItemPosters after restore: %v", err)
	}
	if after.Status != "already-default" {
		t.Fatalf("status after restore = %q, want already-default", after.Status)
	}
	if len(fake.uploads) != 1 || atomic.LoadInt32(&cdn.hits) != 0 {
		t.Fatalf("settle must not re-upload or touch the CDN; uploads = %d, CDN hits = %d", len(fake.uploads), cdn.hits)
	}
}

// (2) External key, no usable thumb (the entry's thumb is the same
// external URL): the direct fetch succeeds through a 302 → 200
// chain, carrying the browser User-Agent — the bare Go default is
// what such CDNs bounce.
func TestRestoreDefaultURLKeyedProviderDirectRedirectWithUA(t *testing.T) {
	dir := t.TempDir()
	bakedBytes := makePNG(t, 4, 6, color.White)
	cleanBytes := makePNG(t, 4, 6, color.Black)
	var mu sync.Mutex
	var uas []string
	mux := http.NewServeMux()
	record := func(r *http.Request) {
		mu.Lock()
		uas = append(uas, r.UserAgent())
		mu.Unlock()
	}
	mux.HandleFunc("/t/p/original/clean.jpg", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		http.Redirect(w, r, "/real/clean.jpg", http.StatusFound)
	})
	mux.HandleFunc("/real/clean.jpg", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(cleanBytes)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	providerURL := ts.URL + "/t/p/original/clean.jpg"
	provider := urlProviderEntryWithThumb(providerURL, providerURL)
	fake := &fakePlex{
		poster:       bakedBytes,
		posters:      []plex.PosterEntry{uploadEntry(true), provider},
		modelUploads: true,
	}
	a := newFakeApplier(dir, fake)
	a.posterHTTPClient = ts.Client()

	outcome, _, errMsg, err := a.RestoreDefaultPoster(context.Background(), testServer(), "1", 2, "7", "Item 7", restoreThumb)
	if err != nil {
		t.Fatalf("RestoreDefaultPoster: %v", err)
	}
	if outcome != "restored" || errMsg != "" {
		t.Fatalf("outcome = %q errMsg = %q, want restored through the redirect chain", outcome, errMsg)
	}
	if len(fake.uploads) != 1 || !bytes.Equal(fake.uploads[0], cleanBytes) {
		t.Fatalf("uploads = %d, want exactly the redirect chain's final bytes uploaded once", len(fake.uploads))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(uas) == 0 {
		t.Fatal("the CDN saw no direct requests")
	}
	for _, ua := range uas {
		if ua != externalPosterUserAgent {
			t.Fatalf("CDN saw User-Agent %q, want the browser UA %q", ua, externalPosterUserAgent)
		}
	}
}

// (3) A redirect loop: the item fails, the reason names the HTTP
// 302 AND the final address the chain kept landing on, and the
// backup helper's wording never appears.
func TestRestoreDefaultURLKeyedProviderRedirectLoop(t *testing.T) {
	dir := t.TempDir()
	mux := http.NewServeMux()
	mux.HandleFunc("/loop.jpg", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop.jpg", http.StatusFound)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	loopURL := ts.URL + "/loop.jpg"
	fake := &fakePlex{
		poster:       makePNG(t, 4, 6, color.White),
		posters:      []plex.PosterEntry{uploadEntry(true), urlProviderEntry(false, loopURL)},
		modelUploads: true,
	}
	a := newFakeApplier(dir, fake)
	a.posterHTTPClient = ts.Client()
	outcome, backupPath, errMsg, err := a.RestoreDefaultPoster(context.Background(), testServer(), "1", 2, "7", "Item 7", restoreThumb)
	if err != nil {
		t.Fatalf("RestoreDefaultPoster: %v", err)
	}
	if outcome != "failed" {
		t.Fatalf("outcome = %q, want failed", outcome)
	}
	for _, want := range []string{"Could not download the clean provider poster", "HTTP 302", loopURL} {
		if !strings.Contains(errMsg, want) {
			t.Fatalf("errMsg = %q, want it to name %q", errMsg, want)
		}
	}
	if strings.Contains(errMsg, "back up") {
		t.Fatalf("errMsg = %q, the backup helper's wording must not surface from the download step", errMsg)
	}
	if len(fake.uploads) != 0 {
		t.Fatalf("uploads = %d, want none", len(fake.uploads))
	}
	if backupPath == "" {
		t.Fatal("the backup gate ran before the failed download and must be reported")
	}
}

// (4) The proxied thumb path fails (Plex answers 404 for it): the
// fetcher falls through to the direct fetch and the restore
// succeeds.
func TestRestoreDefaultURLKeyedProviderThumbFailsOverToDirect(t *testing.T) {
	dir := t.TempDir()
	bakedBytes := makePNG(t, 4, 6, color.White)
	cleanBytes := makePNG(t, 4, 6, color.Black)
	cdn := newPosterCDN(t, http.StatusOK, cleanBytes)
	fake := &fakePlex{
		poster:       bakedBytes,
		posters:      []plex.PosterEntry{uploadEntry(true), urlProviderEntryWithThumb(cdn.url(), proxiedThumbPath)},
		fetchStatus:  map[string]int{proxiedThumbPath: http.StatusNotFound},
		modelUploads: true,
	}
	a := newFakeApplier(dir, fake)
	a.posterHTTPClient = cdn.client()

	outcome, _, errMsg, err := a.RestoreDefaultPoster(context.Background(), testServer(), "1", 2, "7", "Item 7", restoreThumb)
	if err != nil {
		t.Fatalf("RestoreDefaultPoster: %v", err)
	}
	if outcome != "restored" || errMsg != "" {
		t.Fatalf("outcome = %q errMsg = %q, want restored via the direct failover", outcome, errMsg)
	}
	if len(fake.uploads) != 1 || !bytes.Equal(fake.uploads[0], cleanBytes) {
		t.Fatalf("uploads = %d, want exactly the CDN-served clean bytes uploaded once", len(fake.uploads))
	}
	if got := atomic.LoadInt32(&cdn.hits); got != 2 {
		t.Fatalf("CDN hits = %d, want 2 (classification refine + download, each failing over)", got)
	}
	attempted := false
	for _, e := range fake.events {
		if e == "fetch:"+proxiedThumbPath {
			attempted = true
		}
	}
	if !attempted {
		t.Fatalf("the proxied thumb path was never attempted; events = %v", fake.events)
	}
}
