package overlays

// Restore-default classification, rendition path (v1.8.8): the
// byte-equality refinement compares small transcoder renditions of
// the two entries instead of downloading both full posters — on a
// remote-grade connection the full downloads were the count's whole
// cost (two posters per candidate). Renditions are generated from
// the poster FILES at the entries' own addresses, so equality here
// is the same verdict the full-byte comparison reaches; whenever a
// rendition cannot be obtained, the refinement falls back to the
// full-byte comparison unchanged. These tests pin the fetch SHAPE
// (renditions only, zero full fetches) as well as the verdicts, and
// the fallback's shape (full fetches, zero reliance on renditions).

import (
	"context"
	"image/color"
	"net/http"
	"testing"

	"plexcollectionmanager/internal/plex"
)

// Equal files behind both entries → already-default, decided by
// two rendition fetches and ZERO full-poster fetches.
func TestClassifyRefineByRenditionEqual(t *testing.T) {
	dir := t.TempDir()
	cleanBytes := makePNG(t, 4, 6, color.Black)
	fake := &fakePlex{
		poster:  makePNG(t, 4, 6, color.White),
		posters: []plex.PosterEntry{uploadEntry(true), providerEntry(false)},
		posterBytes: map[string][]byte{
			uploadEntry(false).Key:   cleanBytes,
			providerEntry(false).Key: cleanBytes,
		},
	}
	a := newFakeApplier(dir, fake)
	srv := testServer()
	class, err := a.ClassifyItemPosters(context.Background(), srv, "7")
	if err != nil {
		t.Fatalf("ClassifyItemPosters: %v", err)
	}
	if class.Status != "already-default" {
		t.Fatalf("status = %q, want already-default (renditions equal)", class.Status)
	}
	if fake.renditionFetches != 2 {
		t.Fatalf("rendition fetches = %d, want 2 (selected + provider)", fake.renditionFetches)
	}
	if fake.fullFetches != 0 {
		t.Fatalf("full fetches = %d, want 0 — the rendition path must not download full posters", fake.fullFetches)
	}
	// The full restore entry point agrees, and does not back up or
	// upload for a rendition-proven already-default.
	outcome, backupPath, _, err := a.RestoreDefaultPoster(context.Background(), srv, "1", 1, "7", "Item 7", restoreThumb)
	if err != nil {
		t.Fatalf("RestoreDefaultPoster: %v", err)
	}
	if outcome != "already-default" || backupPath != "" || len(fake.uploads) != 0 {
		t.Fatalf("outcome = %q backup = %q uploads = %d, want already-default with no backup or upload", outcome, backupPath, len(fake.uploads))
	}
}

// Different files behind the entries → the rendition comparison
// DECIDES "not equal" (verdict stays restore) — again with zero
// full-poster fetches: a decided difference never falls back.
func TestClassifyRefineByRenditionDiffer(t *testing.T) {
	dir := t.TempDir()
	fake := &fakePlex{
		poster:  makePNG(t, 4, 6, color.White),
		posters: []plex.PosterEntry{uploadEntry(true), providerEntry(false)},
		posterBytes: map[string][]byte{
			uploadEntry(false).Key:   makePNG(t, 4, 6, color.White),
			providerEntry(false).Key: makePNG(t, 4, 6, color.Black),
		},
	}
	a := newFakeApplier(dir, fake)
	class, err := a.ClassifyItemPosters(context.Background(), testServer(), "7")
	if err != nil {
		t.Fatalf("ClassifyItemPosters: %v", err)
	}
	if class.Status != "restore" {
		t.Fatalf("status = %q, want restore (renditions differ)", class.Status)
	}
	if fake.renditionFetches != 2 {
		t.Fatalf("rendition fetches = %d, want 2", fake.renditionFetches)
	}
	if fake.fullFetches != 0 {
		t.Fatalf("full fetches = %d, want 0 — a decided rendition difference must not fall back", fake.fullFetches)
	}
}

// Renditions unavailable (the transcoder errors, or answers
// non-image bytes): the refinement falls back to the full-byte
// comparison and reaches the same verdicts it always did — equal
// bytes settle at already-default on two FULL fetches.
func TestClassifyRefineRenditionUnavailableFallsBack(t *testing.T) {
	cleanBytes := makePNG(t, 4, 6, color.Black)
	for name, mutate := range map[string]func(*fakePlex){
		"transcoder errors":    func(f *fakePlex) { f.renditionsOff = true },
		"transcoder non-image": func(f *fakePlex) { f.renditionGarbage = true },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			fake := &fakePlex{
				poster:  makePNG(t, 4, 6, color.White),
				posters: []plex.PosterEntry{uploadEntry(true), providerEntry(false)},
				posterBytes: map[string][]byte{
					uploadEntry(false).Key:   cleanBytes,
					providerEntry(false).Key: cleanBytes,
				},
			}
			mutate(fake)
			a := newFakeApplier(dir, fake)
			class, err := a.ClassifyItemPosters(context.Background(), testServer(), "7")
			if err != nil {
				t.Fatalf("ClassifyItemPosters: %v", err)
			}
			if class.Status != "already-default" {
				t.Fatalf("status = %q, want already-default via the full-byte fallback", class.Status)
			}
			if fake.fullFetches != 2 {
				t.Fatalf("full fetches = %d, want 2 (selected + provider, full bytes)", fake.fullFetches)
			}
		})
	}
}

// One entry lives only at an off-server address (no server-local
// thumb or key): the pair is undecidable by rendition, NO rendition
// is fetched at all, and the full-byte arbiter decides — here the
// provider's CDN answers 404, so equality is unproven and the
// verdict stays restore.
func TestClassifyRefineNoLocalAddressSkipsRenditions(t *testing.T) {
	dir := t.TempDir()
	cdn := newPosterCDN(t, http.StatusNotFound, []byte("<html>404 Not Found</html>"))
	external := urlProviderEntry(false, cdn.url())
	fake := &fakePlex{
		poster:  makePNG(t, 4, 6, color.White),
		posters: []plex.PosterEntry{uploadEntry(true), external},
	}
	a := newFakeApplier(dir, fake)
	a.posterHTTPClient = cdn.client()
	class, err := a.ClassifyItemPosters(context.Background(), testServer(), "7")
	if err != nil {
		t.Fatalf("ClassifyItemPosters: %v", err)
	}
	if class.Status != "restore" {
		t.Fatalf("status = %q, want restore (provider bytes unfetchable, verdict unproven)", class.Status)
	}
	if fake.renditionFetches != 0 {
		t.Fatalf("rendition fetches = %d, want 0 — no rendition is attempted when an entry has no local address", fake.renditionFetches)
	}
}
