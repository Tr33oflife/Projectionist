package overlays

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"plexcollectionmanager/internal/plex"
)

// --- fixtures ---------------------------------------------------------------

func makePNG(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeJPEG(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// --- fake plex client (implements the internal plexClient interface) --------

type statusErr struct {
	status int
	msg    string
}

func (e *statusErr) Error() string   { return e.msg }
func (e *statusErr) StatusCode() int { return e.status }

type fakePlex struct {
	poster      []byte
	contentType string
	status      int
	fetchErr    error

	uploads      [][]byte
	uploadErr    error
	urlUploads   []string
	urlUploadErr error
	item         *plex.Item

	// Unknown-outcome upload modeling (the remote-server shape):
	// uploadAttempts counts every UploadPosterBytes call, including
	// ones that "never answer". When uploadUnknownErr is set, the
	// call returns it (an unknown-outcome error) — after applying
	// the modeled upload when uploadUnknownApplies is true (the
	// server ingested the poster; only the response was lost), or
	// without touching any state when it is false (the upload never
	// landed). Either way exactly one attempt is recorded: the code
	// under test must verify, never replay.
	uploadAttempts       int
	uploadUnknownErr     error
	uploadUnknownApplies bool

	// restore-default fixtures: a mutable posters list + an event log
	// so tests can prove backup-before-upload ordering.
	posters    []plex.PosterEntry
	postersErr error
	events     []string

	// Upload-mechanism modeling (restore-default tests set
	// modelUploads): on a real PMS an upload appends a NEW upload
	// entry and selects it, bumps the item's thumb version
	// (updatedAt — GetItem reflects it, and the new thumb path serves
	// the uploaded bytes). posterBytes serves distinct bytes per
	// posters-list entry key (fallback: f.poster). The flags break
	// exactly one behavior each so the verification reasons can be
	// pinned: uploadNoSelect (entry lands, selection stays),
	// uploadNoBump (selection moves, thumb address does not),
	// serveStale (address moves, served bytes do not).
	posterBytes    map[string][]byte
	fetchStatus    map[string]int
	modelUploads   bool
	thumbVersion   int
	lastUpload     []byte
	uploadNoSelect bool
	uploadNoBump   bool
	serveStale     bool

	// Rendition modeling (refine-by-rendition tests): FetchServed
	// answers /photo/:/transcode paths with a deterministic small
	// PNG derived from the ADDRESSED entry's file bytes (see
	// fakeRenditionPNG), so equal files yield equal renditions and
	// different files yield different ones — the real transcoder's
	// contract. renditionsOff makes every rendition request fail
	// (404) and renditionGarbage makes it answer non-image bytes;
	// both must push the refinement onto its full-byte fallback.
	// fullFetches counts FetchServed calls that are NOT rendition
	// requests; renditionFetches counts the rendition requests.
	renditionsOff    bool
	renditionGarbage bool
	fullFetches      int
	renditionFetches int
}

// fakeRenditionPNG derives a deterministic small PNG from a poster
// file's bytes: an 8×4 grayscale grid whose pixels are the file's
// SHA-256. Equal files produce byte-identical renditions; different
// files produce different ones (short of a hash collision) — the
// property the refinement's rendition comparison relies on.
func fakeRenditionPNG(src []byte) []byte {
	sum := sha256.Sum256(src)
	img := image.NewRGBA(image.Rect(0, 0, 8, 4))
	for i, b := range sum {
		c := color.RGBA{b, b, b, 255}
		img.Set(i%8, i/8, c)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func (f *fakePlex) FetchServed(ctx context.Context, s *plex.Server, path string, timeout time.Duration) ([]byte, string, int, error) {
	f.events = append(f.events, "fetch:"+path)
	if f.fetchErr != nil {
		return nil, "", 0, f.fetchErr
	}
	st := f.status
	if st == 0 {
		st = 200
	}
	ct := f.contentType
	if ct == "" {
		ct = "image/jpeg"
	}
	// Rendition requests (photo transcoder paths) are served from
	// the ADDRESSED entry's file bytes — resolved exactly the way a
	// direct fetch of that address resolves below (fetchStatus
	// first, then posterBytes, then the default poster) — and are
	// counted separately so tests can prove the refinement took the
	// rendition path and never touched a full poster.
	if strings.HasPrefix(path, "/photo/:/transcode") {
		f.renditionFetches++
		if f.renditionsOff {
			return nil, ct, http.StatusNotFound, nil
		}
		if f.renditionGarbage {
			return []byte("<html>not a rendition</html>"), "text/html", st, nil
		}
		addr := ""
		if u, err := url.Parse(path); err == nil {
			addr = u.Query().Get("url")
		}
		if st2, ok := f.fetchStatus[addr]; ok {
			return nil, ct, st2, nil
		}
		src, ok := f.posterBytes[addr]
		if !ok {
			src = f.poster
		}
		return fakeRenditionPNG(src), ct, st, nil
	}
	f.fullFetches++
	if st2, ok := f.fetchStatus[path]; ok {
		return nil, ct, st2, nil
	}
	if b, ok := f.posterBytes[path]; ok {
		return b, ct, st, nil
	}
	if f.modelUploads && strings.Contains(path, "/thumb/") {
		if f.serveStale || f.lastUpload == nil {
			return f.poster, ct, st, nil
		}
		return f.lastUpload, ct, st, nil
	}
	return f.poster, ct, st, nil
}

func (f *fakePlex) UploadPosterBytes(ctx context.Context, server *plex.Server, rk string, b []byte) error {
	f.uploadAttempts++
	if f.uploadUnknownErr != nil {
		if f.uploadUnknownApplies {
			f.recordUpload(b)
		}
		return f.uploadUnknownErr
	}
	if f.uploadErr != nil {
		return f.uploadErr
	}
	f.recordUpload(b)
	return nil
}

func (f *fakePlex) recordUpload(b []byte) {
	cp := append([]byte(nil), b...)
	f.uploads = append(f.uploads, cp)
	if f.modelUploads {
		f.lastUpload = cp
		n := len(f.uploads)
		entry := plex.PosterEntry{
			Key:       fmt.Sprintf("/library/metadata/7/file?url=upload%%3A%%2F%%2Fposters%%2Fup%d", n),
			RatingKey: fmt.Sprintf("upload://posters/up%d", n),
			Provider:  "upload",
			Selected:  !f.uploadNoSelect,
		}
		if !f.uploadNoSelect {
			for i := range f.posters {
				f.posters[i].Selected = false
			}
		}
		f.posters = append(f.posters, entry)
		if f.posterBytes == nil {
			f.posterBytes = map[string][]byte{}
		}
		f.posterBytes[entry.Key] = cp
		if f.thumbVersion < 1 {
			f.thumbVersion = 1
		}
		if !f.uploadNoBump {
			f.thumbVersion++
		}
	}
}

func (f *fakePlex) SetPosterFromURL(ctx context.Context, server *plex.Server, rk, url string) error {
	if f.urlUploadErr != nil {
		return f.urlUploadErr
	}
	f.urlUploads = append(f.urlUploads, url)
	return nil
}

func (f *fakePlex) GetItem(ctx context.Context, server *plex.Server, rk string) (*plex.Item, error) {
	if f.item != nil {
		return f.item, nil
	}
	v := f.thumbVersion
	if v < 1 {
		v = 1
	}
	return &plex.Item{RatingKey: rk, Title: "Item " + rk, Thumb: fmt.Sprintf("/library/metadata/%s/thumb/%d", rk, v)}, nil
}

func (f *fakePlex) GetPosters(ctx context.Context, server *plex.Server, rk string) ([]plex.PosterEntry, error) {
	f.events = append(f.events, "getposters:"+rk)
	if f.postersErr != nil {
		return nil, f.postersErr
	}
	out := make([]plex.PosterEntry, len(f.posters))
	copy(out, f.posters)
	return out, nil
}

func testServer() *plex.Server {
	return &plex.Server{
		Name:    "Test",
		Machine: "machine123",
		Token:   "tok",
		BestConnection: plex.Connection{
			URI: "http://127.0.0.1:32400", Local: true, Quality: "local",
		},
	}
}

func newFakeApplier(dir string, fake *fakePlex) *Applier {
	return &Applier{userDataDir: dir, client: fake}
}

// --- pure helpers -----------------------------------------------------------

func TestIsImageBytes(t *testing.T) {
	pngB := makePNG(t, 2, 2, color.Black)
	if ok, typ := IsImageBytes(pngB); !ok || typ != "png" {
		t.Errorf("PNG: got (%v,%q), want (true,png)", ok, typ)
	}
	jpgB := makeJPEG(t, 2, 2, color.Black)
	if ok, typ := IsImageBytes(jpgB); !ok || typ != "jpg" {
		t.Errorf("JPEG: got (%v,%q), want (true,jpg)", ok, typ)
	}
	webpB, err := os.ReadFile(filepath.Join("testdata", "poster.webp"))
	if err != nil {
		t.Fatalf("read WebP fixture: %v", err)
	}
	if ok, typ := IsImageBytes(webpB); !ok || typ != "webp" {
		t.Errorf("WebP: got (%v,%q), want (true,webp)", ok, typ)
	}
	gifB := append([]byte("GIF89a"), 0x01, 0x00, 0x01, 0x00)
	if ok, typ := IsImageBytes(gifB); !ok || typ != "gif" {
		t.Errorf("GIF: got (%v,%q), want (true,gif)", ok, typ)
	}
	avifB := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'a', 'v', 'i', 'f', 0x00, 0x00, 0x00, 0x00}
	if ok, typ := IsImageBytes(avifB); !ok || typ != "avif" {
		t.Errorf("AVIF: got (%v,%q), want (true,avif)", ok, typ)
	}
	heicB := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'h', 'e', 'i', 'c', 0x00, 0x00, 0x00, 0x00}
	if ok, typ := IsImageBytes(heicB); !ok || typ != "avif" {
		t.Errorf("HEIF-brand ftyp: got (%v,%q), want (true,avif)", ok, typ)
	}
	if ok, _ := IsImageBytes(nil); ok {
		t.Error("empty must be rejected")
	}
	if ok, _ := IsImageBytes([]byte("<html>404 Not Found</html>")); ok {
		t.Error("HTML error page must be rejected")
	}
	if ok, _ := IsImageBytes([]byte{0xff, 0xd8, 0xff}); ok {
		t.Error("truncated header (<8 bytes) must be rejected")
	}
	wavB := []byte{'R', 'I', 'F', 'F', 0x24, 0x00, 0x00, 0x00, 'W', 'A', 'V', 'E', 'f', 'm', 't', ' '}
	if ok, _ := IsImageBytes(wavB); ok {
		t.Error("RIFF/WAVE must be rejected — a RIFF container alone is not WebP")
	}
	bogusWebP := []byte{'R', 'I', 'F', 'F', 0x10, 0x00, 0x00, 0x00, 'W', 'E', 'B', 'P', 'X', 'X', 'X', 'X'}
	if ok, _ := IsImageBytes(bogusWebP); ok {
		t.Error("WEBP with a bogus first chunk must be rejected")
	}
	if ok, _ := IsImageBytes(webpB[:15]); ok {
		t.Error("truncated WebP header must be rejected")
	}
	if ok, _ := IsImageBytes(avifB[:11]); ok {
		t.Error("truncated AVIF ftyp must be rejected")
	}
}

func TestImageTypeExtensionAndContentTypeAgreement(t *testing.T) {
	for ct, want := range map[string]string{
		"image/png":                  "png",
		"image/jpeg":                 "jpg",
		"image/jpg":                  "jpg",
		"image/webp; charset=binary": "webp",
		"IMAGE/GIF":                  "gif",
		"image/avif":                 "avif",
		"application/octet-stream":   "jpg",
		"":                           "jpg",
	} {
		if got := extForContentType(ct); got != want {
			t.Errorf("extForContentType(%q) = %q, want %q", ct, got, want)
		}
	}
	for _, tc := range []struct {
		responseCT string
		typ        string
		want       string
	}{
		{"application/octet-stream", "webp", "image/webp"},
		{"", "avif", "image/avif"},
		{"image/png", "webp", "image/webp"},
		{"image/jpg", "jpg", "image/jpg"},
		{"image/webp; charset=binary", "webp", "image/webp"},
	} {
		got := storedContentType(tc.responseCT, tc.typ)
		if got != tc.want {
			t.Errorf("storedContentType(%q,%q) = %q, want %q", tc.responseCT, tc.typ, got, tc.want)
		}
		if extForContentType(got) != extForImageType(tc.typ) {
			t.Errorf("stored content type %q does not agree with sniffed type %q", got, tc.typ)
		}
	}
}

func TestSHA256Hex(t *testing.T) {
	// Well-known vector for "abc".
	if got := SHA256Hex([]byte("abc")); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("SHA256Hex(abc) = %s", got)
	}
}

func TestBackupPathsNamespacing(t *testing.T) {
	dir, img, side := BackupPaths("/ud", "machineA", "42", "png")
	if !strings.Contains(dir, filepath.Join("overlay-backups", "machineA")) {
		t.Errorf("dir not namespaced by machineId: %s", dir)
	}
	if filepath.Base(img) != "42.png" || filepath.Base(side) != "42.json" {
		t.Errorf("paths: img=%s side=%s", img, side)
	}
	_, img2, _ := BackupPaths("/ud", "machineA", "42", "")
	if filepath.Base(img2) != "42.jpg" {
		t.Errorf("default ext must be jpg, got %s", img2)
	}
	dirB, _, _ := BackupPaths("/ud", "machineB", "42", "png")
	if dir == dirB {
		t.Error("different machines must not share a backup dir")
	}
}

func TestSidecarShape(t *testing.T) {
	sc := SidecarFor("42", "Some Title", "/thumb/42", json.RawMessage(`{"name":"Set A","placements":[{"badgeId":"b1"}]}`), "deadbeef", "image/png", "run-1")
	for _, k := range []string{"version", "ratingKey", "title", "backedUpAt", "sourceUrl", "contentType", "posterHash", "badgeSet", "runId"} {
		if _, ok := sc[k]; !ok {
			t.Errorf("sidecar missing key %q", k)
		}
	}
	if sc["ratingKey"] != "42" || sc["posterHash"] != "deadbeef" || sc["runId"] != "run-1" || sc["sourceUrl"] != "/thumb/42" || sc["contentType"] != "image/png" {
		t.Errorf("sidecar values wrong: %v", sc)
	}
	bs, _ := sc["badgeSet"].(map[string]any)
	if bs == nil || bs["name"] != "Set A" {
		t.Errorf("badgeSet summary wrong: %v", sc["badgeSet"])
	}
	sc2 := SidecarFor("7", "", "", nil, "h", "", "")
	if sc2["badgeSet"] != nil {
		t.Errorf("nil badgeSet must stay null, got %v", sc2["badgeSet"])
	}
}

// --- backup gate ------------------------------------------------------------

func TestBackupGateRejectsGarbagePoster(t *testing.T) {
	dir := t.TempDir()
	fake := &fakePlex{poster: []byte("<html>not an image at all</html>"), contentType: "text/html"}
	a := newFakeApplier(dir, fake)
	composite := makePNG(t, 4, 4, color.White)
	rec, err := a.RunApplyJob(context.Background(), ApplyRequest{
		Server: testServer(),
		Jobs:   []Job{{RatingKey: "42", Title: "Bad Poster", Thumb: "/thumb/42", PNGBase64: base64.StdEncoding.EncodeToString(composite)}},
	})
	if err != nil {
		t.Fatalf("RunApplyJob err: %v", err)
	}
	if rec.FailCount != 1 || rec.OKCount != 0 {
		t.Fatalf("counts: ok=%d fail=%d, want 0/1", rec.OKCount, rec.FailCount)
	}
	if len(fake.uploads) != 0 {
		t.Fatalf("NO upload may be attempted when the backup gate fails; got %d uploads", len(fake.uploads))
	}
	if !strings.Contains(rec.Items[0].Error, "Backup verification failed") {
		t.Errorf("item error should record the gate failure, got %q", rec.Items[0].Error)
	}
	if !strings.Contains(rec.Items[0].Error, "content-type: text/html") || !strings.Contains(rec.Items[0].Error, "first bytes: 3c 68 74 6d 6c 3e 6e 6f") {
		t.Errorf("gate failure should be diagnosable with content type and first bytes, got %q", rec.Items[0].Error)
	}
	// The failure is recorded in the run on disk, too.
	runs := a.ListRuns()
	if len(runs) != 1 || runs[0].FailCount != 1 {
		t.Errorf("ListRuns should surface the failed run: %+v", runs)
	}
	// And no backup was left behind for the garbage bytes.
	if a.VerifyBackup(testServer(), "42") {
		t.Error("VerifyBackup must be false when no valid backup exists")
	}
}

func TestApplyHappyPathWriteOnceAndRestore(t *testing.T) {
	dir := t.TempDir()
	original := makeJPEG(t, 6, 9, color.RGBA{200, 10, 10, 255})
	fake := &fakePlex{poster: original, contentType: "image/jpeg"}
	a := newFakeApplier(dir, fake)
	srv := testServer()
	composite := makePNG(t, 6, 9, color.RGBA{10, 200, 10, 255})
	job := Job{RatingKey: "99", Title: "Film", Thumb: "/thumb/99", PNGBase64: base64.StdEncoding.EncodeToString(composite)}

	rec, err := a.RunApplyJob(context.Background(), ApplyRequest{Server: srv, Jobs: []Job{job}, BadgeSet: json.RawMessage(`{"name":"Set","placements":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if rec.OKCount != 1 || len(fake.uploads) != 1 || !bytes.Equal(fake.uploads[0], composite) {
		t.Fatalf("apply: ok=%d uploads=%d", rec.OKCount, len(fake.uploads))
	}
	if rec.Items[0].Via != "multipart" {
		t.Errorf("via = %q, want multipart", rec.Items[0].Via)
	}
	if rec.Version != "0.3.0" || rec.Machine != "machine123" || rec.RunID == "" {
		t.Errorf("run record header wrong: %+v", rec)
	}
	if !a.VerifyBackup(srv, "99") {
		t.Fatal("VerifyBackup must be true after a successful apply")
	}
	_, imgPath, sidePath := BackupPaths(dir, "machine123", "99", "jpg")
	sideBefore, _ := os.ReadFile(sidePath)
	imgBefore, _ := os.ReadFile(imgPath)
	if !bytes.Equal(imgBefore, original) {
		t.Fatal("backup must hold the ORIGINAL poster bytes")
	}

	// Second apply: Plex now serves a DIFFERENT poster (someone changed
	// it). The write-once backup must be reused untouched.
	fake.poster = makeJPEG(t, 6, 9, color.RGBA{10, 10, 200, 255})
	rec2, err := a.RunApplyJob(context.Background(), ApplyRequest{Server: srv, Jobs: []Job{job}})
	if err != nil {
		t.Fatal(err)
	}
	if rec2.OKCount != 1 {
		t.Fatalf("second apply failed: %+v", rec2.Items)
	}
	sideAfter, _ := os.ReadFile(sidePath)
	imgAfter, _ := os.ReadFile(imgPath)
	if !bytes.Equal(sideBefore, sideAfter) {
		t.Error("sidecar must be unchanged on write-once reuse (timestamp/hash preserved)")
	}
	if !bytes.Equal(imgBefore, imgAfter) {
		t.Error("backup image must NEVER be overwritten")
	}

	// Restore re-uploads the original backup bytes and clears state.
	out, err := a.RestorePoster(context.Background(), srv, "99")
	if err != nil {
		t.Fatal(err)
	}
	if out["restored"] != true || out["via"] != "multipart" {
		t.Errorf("restore result: %v", out)
	}
	last := fake.uploads[len(fake.uploads)-1]
	if !bytes.Equal(last, original) {
		t.Error("restore must upload the original backup bytes")
	}
	if _, err := os.Stat(filepath.Join(dir, "overlay-state", "machine123", "99.json")); !os.IsNotExist(err) {
		t.Error("restore must remove the applied-state file")
	}
}

func TestApplyWebPPosterBackupReuseAndRestore(t *testing.T) {
	dir := t.TempDir()
	webpB, err := os.ReadFile(filepath.Join("testdata", "poster.webp"))
	if err != nil {
		t.Fatalf("read WebP fixture: %v", err)
	}
	// The response type is deliberately generic: the sniffed type must
	// decide both the file extension and the sidecar content type.
	fake := &fakePlex{poster: webpB, contentType: "application/octet-stream"}
	a := newFakeApplier(dir, fake)
	srv := testServer()
	composite := makePNG(t, 4, 6, color.RGBA{10, 20, 200, 255})
	job := Job{RatingKey: "100", Title: "WebP Poster", Thumb: "/thumb/100", PNGBase64: base64.StdEncoding.EncodeToString(composite)}

	rec, err := a.RunApplyJob(context.Background(), ApplyRequest{Server: srv, Jobs: []Job{job}})
	if err != nil {
		t.Fatal(err)
	}
	if rec.OKCount != 1 {
		t.Fatalf("WebP poster apply failed: %+v", rec.Items)
	}
	_, imgPath, sidePath := BackupPaths(dir, "machine123", "100", "webp")
	if rec.Items[0].BackupPath != imgPath {
		t.Errorf("backup path = %q, want %q", rec.Items[0].BackupPath, imgPath)
	}
	backedUp, err := os.ReadFile(imgPath)
	if err != nil {
		t.Fatalf("WebP backup not stored at the sniffed extension: %v", err)
	}
	if !bytes.Equal(backedUp, webpB) {
		t.Fatal("WebP backup must hold the original poster bytes")
	}
	sideBefore, err := os.ReadFile(sidePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sideBefore), `"contentType": "image/webp"`) {
		t.Errorf("sidecar content type must match the sniffed WebP type: %s", sideBefore)
	}
	if !a.VerifyBackup(srv, "100") {
		t.Fatal("VerifyBackup must find and verify the WebP backup")
	}

	// A later run sees a different current poster, but must reuse the
	// original WebP backup through the sidecar-derived lookup.
	fake.poster = makeJPEG(t, 4, 6, color.RGBA{200, 20, 10, 255})
	fake.contentType = "image/jpeg"
	rec2, err := a.RunApplyJob(context.Background(), ApplyRequest{Server: srv, Jobs: []Job{job}})
	if err != nil {
		t.Fatal(err)
	}
	if rec2.OKCount != 1 || rec2.Items[0].BackupPath != imgPath {
		t.Fatalf("WebP backup reuse failed: %+v", rec2.Items)
	}
	sideAfter, _ := os.ReadFile(sidePath)
	imgAfter, _ := os.ReadFile(imgPath)
	if !bytes.Equal(sideBefore, sideAfter) || !bytes.Equal(imgAfter, webpB) {
		t.Error("write-once WebP backup and sidecar must be unchanged on reuse")
	}

	out, err := a.RestorePoster(context.Background(), srv, "100")
	if err != nil {
		t.Fatal(err)
	}
	if out["backupPath"] != imgPath {
		t.Errorf("restore backup path = %v, want %v", out["backupPath"], imgPath)
	}
	last := fake.uploads[len(fake.uploads)-1]
	if !bytes.Equal(last, webpB) {
		t.Error("restore must upload the original WebP backup bytes")
	}
}

func TestVerifyBackupDetectsTampering(t *testing.T) {
	dir := t.TempDir()
	fake := &fakePlex{poster: makeJPEG(t, 4, 4, color.Black), contentType: "image/jpeg"}
	a := newFakeApplier(dir, fake)
	srv := testServer()
	composite := makePNG(t, 4, 4, color.White)
	_, err := a.RunApplyJob(context.Background(), ApplyRequest{Server: srv, Jobs: []Job{{RatingKey: "5", Title: "T", Thumb: "/t/5", PNGBase64: base64.StdEncoding.EncodeToString(composite)}}})
	if err != nil {
		t.Fatal(err)
	}
	if !a.VerifyBackup(srv, "5") {
		t.Fatal("backup should verify")
	}
	_, imgPath, _ := BackupPaths(dir, "machine123", "5", "jpg")
	if err := os.WriteFile(imgPath, makeJPEG(t, 4, 4, color.White), 0o644); err != nil {
		t.Fatal(err)
	}
	if a.VerifyBackup(srv, "5") {
		t.Error("tampered backup (hash mismatch) must fail verification")
	}
	// Restore must also refuse a tampered backup.
	if _, err := a.RestorePoster(context.Background(), srv, "5"); err == nil {
		t.Error("restore of a tampered backup must fail")
	}
	// Non-numeric rating keys / missing items never verify.
	if a.VerifyBackup(srv, "../etc") || a.VerifyBackup(srv, "12345") {
		t.Error("VerifyBackup must be false for traversal-ish and unknown keys")
	}
}

func TestUploadFallbackTempURLOnlyOn404Local(t *testing.T) {
	dir := t.TempDir()
	fake := &fakePlex{
		poster:      makeJPEG(t, 4, 4, color.Black),
		contentType: "image/jpeg",
		uploadErr:   &statusErr{status: 404, msg: "HTTP 404"},
	}
	a := newFakeApplier(dir, fake)
	composite := makePNG(t, 4, 4, color.White)
	rec, err := a.RunApplyJob(context.Background(), ApplyRequest{Server: testServer(), Jobs: []Job{{RatingKey: "7", Title: "T", Thumb: "/t/7", PNGBase64: base64.StdEncoding.EncodeToString(composite)}}})
	if err != nil {
		t.Fatal(err)
	}
	if rec.OKCount != 1 || rec.Items[0].Via != "temp-url-fallback" || len(fake.urlUploads) != 1 {
		t.Fatalf("expected temp-url fallback success, got %+v urlUploads=%v", rec.Items, fake.urlUploads)
	}
	if !strings.HasPrefix(fake.urlUploads[0], "http://127.0.0.1:") {
		t.Errorf("fallback URL must be loopback: %s", fake.urlUploads[0])
	}

	// A 500 must NOT fall back — it fails loudly.
	fake2 := &fakePlex{poster: makeJPEG(t, 4, 4, color.Black), contentType: "image/jpeg", uploadErr: &statusErr{status: 500, msg: "HTTP 500 boom"}}
	a2 := newFakeApplier(t.TempDir(), fake2)
	rec2, _ := a2.RunApplyJob(context.Background(), ApplyRequest{Server: testServer(), Jobs: []Job{{RatingKey: "7", Title: "T", Thumb: "/t/7", PNGBase64: base64.StdEncoding.EncodeToString(composite)}}})
	if rec2.FailCount != 1 || !strings.Contains(rec2.Items[0].Error, "HTTP 500 boom") || len(fake2.urlUploads) != 0 {
		t.Errorf("500 must fail loudly without fallback: %+v", rec2.Items)
	}
}

func TestReapplyCheck(t *testing.T) {
	dir := t.TempDir()
	original := makeJPEG(t, 4, 4, color.Black)
	fake := &fakePlex{poster: original, contentType: "image/jpeg"}
	a := newFakeApplier(dir, fake)
	srv := testServer()
	composite := makePNG(t, 4, 4, color.White)
	_, err := a.RunApplyJob(context.Background(), ApplyRequest{Server: srv, Jobs: []Job{{RatingKey: "11", Title: "Film", Thumb: "/t/11", PNGBase64: base64.StdEncoding.EncodeToString(composite)}}})
	if err != nil {
		t.Fatal(err)
	}
	// Current poster == composite → no reapply needed.
	fake.poster = composite
	fake.contentType = "image/png"
	out, err := a.ReapplyCheck(context.Background(), srv)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0]["needsReapply"] != false {
		t.Fatalf("expected needsReapply=false, got %v", out)
	}
	// Current poster differs → reapply needed.
	fake.poster = original
	fake.contentType = "image/jpeg"
	out, err = a.ReapplyCheck(context.Background(), srv)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0]["needsReapply"] != true {
		t.Fatalf("expected needsReapply=true, got %v", out)
	}
}

func TestRestoreRunAndRunsHistory(t *testing.T) {
	dir := t.TempDir()
	fake := &fakePlex{poster: makeJPEG(t, 4, 4, color.Black), contentType: "image/jpeg"}
	a := newFakeApplier(dir, fake)
	srv := testServer()
	composite := makePNG(t, 4, 4, color.White)
	rec, err := a.RunApplyJob(context.Background(), ApplyRequest{Server: srv, Jobs: []Job{{RatingKey: "21", Title: "T", Thumb: "/t/21", PNGBase64: base64.StdEncoding.EncodeToString(composite)}}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.RestoreRun(context.Background(), srv, rec.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if out["okCount"] != 1 {
		t.Errorf("restore run okCount: %v", out)
	}
	if _, err := a.RestoreRun(context.Background(), srv, "run-nope"); err == nil {
		t.Error("missing run must error")
	}
	other := testServer()
	other.Machine = "different"
	if _, err := a.RestoreRun(context.Background(), other, rec.RunID); err == nil {
		t.Error("run from another server must error")
	}
}
