package overlays

// Restore-default tests, upload mechanism: classification outcomes
// (including the byte-equality already-default), the backup-before-
// upload order, and the three post-write verifications — new entry
// selected, poster address refreshed, served bytes are the clean
// poster's. The fake models real PMS behavior: an upload appends a
// selected upload entry and bumps the item's thumb version; flags
// break one behavior each.

import (
	"bytes"
	"context"
	"image/color"
	"strings"
	"testing"

	"plexcollectionmanager/internal/plex"
)

func uploadEntry(selected bool) plex.PosterEntry {
	return plex.PosterEntry{Key: "/library/metadata/7/file?url=upload%3A%2F%2Fposters%2Faaa", RatingKey: "upload://posters/aaa", Provider: "upload", Selected: selected}
}

func providerEntry(selected bool) plex.PosterEntry {
	return plex.PosterEntry{Key: "/library/metadata/7/file?url=metadata%3A%2F%2Fposters%2Fxyz", RatingKey: "metadata://posters/xyz", Provider: "tmdb", Selected: selected}
}

const restoreThumb = "/library/metadata/7/thumb/1"

func TestRestoreDefaultUploadsAndVerifies(t *testing.T) {
	dir := t.TempDir()
	oldBytes := makePNG(t, 4, 6, color.White)
	cleanBytes := makePNG(t, 4, 6, color.Black)
	fake := &fakePlex{
		poster:       oldBytes,
		posters:      []plex.PosterEntry{uploadEntry(true), providerEntry(false)},
		posterBytes:  map[string][]byte{providerEntry(false).Key: cleanBytes},
		modelUploads: true,
	}
	a := newFakeApplier(dir, fake)
	srv := testServer()

	outcome, backupPath, errMsg, err := a.RestoreDefaultPoster(context.Background(), srv, "1", 1, "7", "Item 7", restoreThumb)
	if err != nil {
		t.Fatalf("RestoreDefaultPoster: %v", err)
	}
	if outcome != "restored" || errMsg != "" {
		t.Fatalf("outcome = %q errMsg = %q, want restored", outcome, errMsg)
	}
	if backupPath == "" || !a.VerifyBackup(srv, "7") {
		t.Fatalf("backup missing after restore (path %q, verify %v)", backupPath, a.VerifyBackup(srv, "7"))
	}
	// The upload carried the PROVIDER poster's bytes, byte-identical.
	if len(fake.uploads) != 1 || !bytes.Equal(fake.uploads[0], cleanBytes) {
		t.Fatalf("uploads = %d, want exactly the provider poster's bytes uploaded once", len(fake.uploads))
	}
	// Order: classify read → refine fetches (selected, provider —
	// renditions when both entries are server-local, else full bytes)
	// → backup fetch → provider download → verify read → served
	// fetch. The backup gate still precedes the write.
	var seq []string
	for _, e := range fake.events {
		seq = append(seq, strings.SplitN(e, ":", 2)[0])
	}
	want := []string{"getposters", "fetch", "fetch", "fetch", "fetch", "getposters", "fetch"}
	if strings.Join(seq, ",") != strings.Join(want, ",") {
		t.Fatalf("event order = %v, want %v", seq, want)
	}
	// The fake's list now shows the NEW upload entry selected.
	last := fake.posters[len(fake.posters)-1]
	if !last.Selected || !last.IsUpload() || last.RatingKey == "upload://posters/aaa" {
		t.Fatalf("posters after restore: %+v", fake.posters)
	}
}

func TestRestoreDefaultAlreadyDefault(t *testing.T) {
	dir := t.TempDir()
	// Provider selected and NO upload entries: nothing was ever
	// baked onto this item, so the run is a no-op. (With any upload
	// present the same selection classifies as "restore" — see the
	// provider-selected cases below.)
	fake := &fakePlex{
		poster:  makePNG(t, 4, 6, color.White),
		posters: []plex.PosterEntry{providerEntry(true)},
	}
	a := newFakeApplier(dir, fake)
	outcome, _, _, err := a.RestoreDefaultPoster(context.Background(), testServer(), "1", 1, "7", "Item 7", restoreThumb)
	if err != nil {
		t.Fatalf("RestoreDefaultPoster: %v", err)
	}
	if outcome != "already-default" {
		t.Fatalf("outcome = %q, want already-default", outcome)
	}
	if len(fake.uploads) != 0 {
		t.Fatalf("already-default must not upload; uploads = %d", len(fake.uploads))
	}
	for _, e := range fake.events {
		if e == "fetch:"+restoreThumb {
			t.Fatalf("already-default must not back up; events = %v", fake.events)
		}
	}
}

// The state an upload-based restore leaves behind: the selected
// entry is an upload whose bytes ARE the provider poster's bytes.
// It must classify as already-default — never as "to restore" again.
func TestRestoreDefaultAlreadyDefaultByBytes(t *testing.T) {
	dir := t.TempDir()
	cleanBytes := makePNG(t, 4, 6, color.White)
	fake := &fakePlex{
		poster:      cleanBytes,
		posters:     []plex.PosterEntry{uploadEntry(true), providerEntry(false)},
		posterBytes: map[string][]byte{providerEntry(false).Key: cleanBytes},
	}
	a := newFakeApplier(dir, fake)
	outcome, backupPath, _, err := a.RestoreDefaultPoster(context.Background(), testServer(), "1", 1, "7", "Item 7", restoreThumb)
	if err != nil {
		t.Fatalf("RestoreDefaultPoster: %v", err)
	}
	if outcome != "already-default" {
		t.Fatalf("outcome = %q, want already-default (selected upload is byte-equal to the provider poster)", outcome)
	}
	if len(fake.uploads) != 0 || backupPath != "" {
		t.Fatalf("byte-equal already-default must not back up or upload (uploads %d, backup %q)", len(fake.uploads), backupPath)
	}
}

func TestRestoreDefaultNoProvider(t *testing.T) {
	for name, posters := range map[string][]plex.PosterEntry{
		"uploads only": {uploadEntry(true)},
		"empty list":   {},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			fake := &fakePlex{poster: makePNG(t, 4, 6, color.White), posters: posters}
			a := newFakeApplier(dir, fake)
			outcome, _, _, err := a.RestoreDefaultPoster(context.Background(), testServer(), "1", 1, "7", "Item 7", restoreThumb)
			if err != nil {
				t.Fatalf("RestoreDefaultPoster: %v", err)
			}
			if outcome != "no-provider" {
				t.Fatalf("outcome = %q, want no-provider", outcome)
			}
			if len(fake.uploads) != 0 {
				t.Fatalf("no-provider must never upload; uploads = %d", len(fake.uploads))
			}
		})
	}
}

// The safety invariant: when the backup gate fails, the item is a
// failure and the upload NEVER happens.
func TestRestoreDefaultBackupFailureBlocksUpload(t *testing.T) {
	dir := t.TempDir()
	fake := &fakePlex{
		poster:      []byte("<html>not an image</html>"),
		posters:     []plex.PosterEntry{uploadEntry(true), providerEntry(false)},
		posterBytes: map[string][]byte{providerEntry(false).Key: makePNG(t, 4, 6, color.White)},
	}
	a := newFakeApplier(dir, fake)
	outcome, _, errMsg, err := a.RestoreDefaultPoster(context.Background(), testServer(), "1", 1, "7", "Item 7", restoreThumb)
	if err != nil {
		t.Fatalf("RestoreDefaultPoster: %v", err)
	}
	if outcome != "failed" || errMsg == "" {
		t.Fatalf("outcome = %q errMsg = %q, want failed with a reason", outcome, errMsg)
	}
	if len(fake.uploads) != 0 {
		t.Fatalf("upload happened despite the backup failure: %d", len(fake.uploads))
	}
}

// Verification (a): the upload landed but Plex did not select the
// new entry — a failure, not a restore.
func TestRestoreDefaultUploadNotSelectedFails(t *testing.T) {
	dir := t.TempDir()
	fake := &fakePlex{
		poster:         makePNG(t, 4, 6, color.White),
		posters:        []plex.PosterEntry{uploadEntry(true), providerEntry(false)},
		posterBytes:    map[string][]byte{providerEntry(false).Key: makePNG(t, 4, 6, color.Black)},
		modelUploads:   true,
		uploadNoSelect: true,
	}
	a := newFakeApplier(dir, fake)
	outcome, backupPath, errMsg, err := a.RestoreDefaultPoster(context.Background(), testServer(), "1", 1, "7", "Item 7", restoreThumb)
	if err != nil {
		t.Fatalf("RestoreDefaultPoster: %v", err)
	}
	if outcome != "failed" || !strings.Contains(errMsg, "did not select the uploaded clean poster") {
		t.Fatalf("outcome = %q errMsg = %q, want failed / did-not-select", outcome, errMsg)
	}
	if backupPath == "" {
		t.Fatal("the backup still happened and must be reported")
	}
}

// Verification (b): the upload was selected but the item's poster
// address did not move — clients keyed on the old address would keep
// the old image, so the item fails with the address reason.
func TestRestoreDefaultNoAddressRefreshFails(t *testing.T) {
	dir := t.TempDir()
	fake := &fakePlex{
		poster:       makePNG(t, 4, 6, color.White),
		posters:      []plex.PosterEntry{uploadEntry(true), providerEntry(false)},
		posterBytes:  map[string][]byte{providerEntry(false).Key: makePNG(t, 4, 6, color.Black)},
		modelUploads: true,
		uploadNoBump: true,
	}
	a := newFakeApplier(dir, fake)
	outcome, backupPath, errMsg, err := a.RestoreDefaultPoster(context.Background(), testServer(), "1", 1, "7", "Item 7", restoreThumb)
	if err != nil {
		t.Fatalf("RestoreDefaultPoster: %v", err)
	}
	if outcome != "failed" || !strings.Contains(errMsg, "did not refresh the poster address") {
		t.Fatalf("outcome = %q errMsg = %q, want failed / address-not-refreshed", outcome, errMsg)
	}
	if backupPath == "" {
		t.Fatal("the backup still happened and must be reported")
	}
}

// Verification (c): selection and address moved, but the bytes
// served at the new address are still the old poster's.
func TestRestoreDefaultStaleServedBytesFails(t *testing.T) {
	dir := t.TempDir()
	fake := &fakePlex{
		poster:       makePNG(t, 4, 6, color.White),
		posters:      []plex.PosterEntry{uploadEntry(true), providerEntry(false)},
		posterBytes:  map[string][]byte{providerEntry(false).Key: makePNG(t, 4, 6, color.Black)},
		modelUploads: true,
		serveStale:   true,
	}
	a := newFakeApplier(dir, fake)
	outcome, backupPath, errMsg, err := a.RestoreDefaultPoster(context.Background(), testServer(), "1", 1, "7", "Item 7", restoreThumb)
	if err != nil {
		t.Fatalf("RestoreDefaultPoster: %v", err)
	}
	if outcome != "failed" || !strings.Contains(errMsg, "still serves the old image bytes") {
		t.Fatalf("outcome = %q errMsg = %q, want failed / still-serves-old-bytes", outcome, errMsg)
	}
	if backupPath == "" || !a.VerifyBackup(testServer(), "7") {
		t.Fatalf("backup missing after stale-bytes failure (path %q)", backupPath)
	}
}

// --- provider selected, uploads present: never already-default ---
//
// The pure classification trusts the posters list (provider selected
// → already-default), but a selection flip does not move the item's
// poster address, and the transcoder caches renditions per size at
// that unchanged address — so with any upload entry present, the
// displayed image cannot be proven clean from the list (or from
// any single sampled rendition). These cases pin the conservative
// rule: provider selection + ≥1 upload entry → "restore"; only the
// upload-based run settles it.

// Case 1: provider selected and NO upload entries — nothing was
// ever baked onto the item; the fast path stands.
func TestClassifyProviderSelectedNoUploadsIsDefault(t *testing.T) {
	dir := t.TempDir()
	fake := &fakePlex{
		poster:  makePNG(t, 4, 6, color.White),
		posters: []plex.PosterEntry{providerEntry(true)},
	}
	a := newFakeApplier(dir, fake)
	class, err := a.ClassifyItemPosters(context.Background(), testServer(), "7")
	if err != nil {
		t.Fatalf("ClassifyItemPosters: %v", err)
	}
	if class.Status != "already-default" {
		t.Fatalf("status = %q, want already-default", class.Status)
	}
}

// Case 2: provider selected, an upload present — restore, EVEN in
// the friendliest configuration (this fixture deliberately mirrors
// the old rendition-matches setup, where a single-size rendition
// comparison would have acquitted the item). The rule consults no
// rendition at all: the coexisting upload alone flips the verdict.
func TestClassifyProviderSelectedUploadPresentRestores(t *testing.T) {
	dir := t.TempDir()
	cleanBytes := makePNG(t, 4, 6, color.Black)
	bakedBytes := makePNG(t, 4, 6, color.White)
	fake := &fakePlex{
		poster:  bakedBytes,
		posters: []plex.PosterEntry{uploadEntry(false), providerEntry(true)},
		posterBytes: map[string][]byte{
			providerEntry(false).Key: cleanBytes,
			uploadEntry(false).Key:   bakedBytes,
		},
	}
	a := newFakeApplier(dir, fake)
	class, err := a.ClassifyItemPosters(context.Background(), testServer(), "7")
	if err != nil {
		t.Fatalf("ClassifyItemPosters: %v", err)
	}
	if class.Status != "restore" {
		t.Fatalf("status = %q, want restore (provider selected but an upload coexists)", class.Status)
	}
	if class.DefaultKey != providerEntry(false).Key {
		t.Fatalf("default key = %q, want the provider entry's key", class.DefaultKey)
	}
}

// Case 3 (the live report): the clean poster is SELECTED while the
// baked poster remains as an unselected upload — "selected, not
// applied". Classification must say "restore", the upload-based
// run must then fix it, and afterwards classification settles at
// already-default (via the byte-equal selected upload the run
// leaves behind — no loop).
func TestClassifyProviderSelectedStuckStateRestoresAndSettles(t *testing.T) {
	dir := t.TempDir()
	cleanBytes := makePNG(t, 4, 6, color.Black)
	bakedBytes := makePNG(t, 4, 6, color.White)
	fake := &fakePlex{
		poster:  bakedBytes,
		posters: []plex.PosterEntry{uploadEntry(false), providerEntry(true)},
		posterBytes: map[string][]byte{
			providerEntry(false).Key: cleanBytes,
			uploadEntry(false).Key:   bakedBytes,
		},
		modelUploads: true,
	}
	a := newFakeApplier(dir, fake)
	srv := testServer()
	class, err := a.ClassifyItemPosters(context.Background(), srv, "7")
	if err != nil {
		t.Fatalf("ClassifyItemPosters: %v", err)
	}
	if class.Status != "restore" {
		t.Fatalf("status = %q, want restore (selected clean, upload still present)", class.Status)
	}
	if class.DefaultKey != providerEntry(false).Key {
		t.Fatalf("default key = %q, want the provider entry's key", class.DefaultKey)
	}
	outcome, _, errMsg, err := a.RestoreDefaultPoster(context.Background(), srv, "1", 1, "7", "Item 7", restoreThumb)
	if err != nil {
		t.Fatalf("RestoreDefaultPoster: %v", err)
	}
	if outcome != "restored" || errMsg != "" {
		t.Fatalf("outcome = %q errMsg = %q, want restored", outcome, errMsg)
	}
	if len(fake.uploads) != 1 || !bytes.Equal(fake.uploads[0], cleanBytes) {
		t.Fatalf("uploads = %d, want the provider poster's bytes uploaded once", len(fake.uploads))
	}
	after, err := a.ClassifyItemPosters(context.Background(), srv, "7")
	if err != nil {
		t.Fatalf("ClassifyItemPosters after restore: %v", err)
	}
	if after.Status != "already-default" {
		t.Fatalf("status after restore = %q, want already-default", after.Status)
	}
}

func TestRestoreDefaultUploadErrorFails(t *testing.T) {
	dir := t.TempDir()
	fake := &fakePlex{
		poster:      makePNG(t, 4, 6, color.White),
		posters:     []plex.PosterEntry{uploadEntry(true), providerEntry(false)},
		posterBytes: map[string][]byte{providerEntry(false).Key: makePNG(t, 4, 6, color.Black)},
		uploadErr:   &statusErr{status: 500, msg: "server exploded"},
	}
	a := newFakeApplier(dir, fake)
	outcome, _, errMsg, err := a.RestoreDefaultPoster(context.Background(), testServer(), "1", 1, "7", "Item 7", restoreThumb)
	if err != nil {
		t.Fatalf("RestoreDefaultPoster: %v", err)
	}
	if outcome != "failed" || !strings.Contains(errMsg, "server exploded") {
		t.Fatalf("outcome = %q errMsg = %q", outcome, errMsg)
	}
}
