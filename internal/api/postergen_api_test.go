package api

// postergen_api_test.go — the v1.20 poster generator, end to end
// through the HTTP layer against a stateful fake PMS that HAS
// members with real (distinct-color) poster bytes: preview renders
// and registers an asset without writing anything; applying that
// asset through the EXISTING plex/setPoster path lands the upload
// on the fake, leaves a verified backup on disk, and restores.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"plexcollectionmanager/internal/plex"
)

type genMember struct {
	id        string
	title     string
	col       color.RGBA
	posterPNG []byte
	artPNG    []byte
}

type genFakePMS struct {
	mu            sync.Mutex
	members       []genMember
	byID          map[string]genMember
	failThumb     map[string]bool
	thumbOverride map[string]string // member id → replacement thumb path (a "changed poster")
	collThumb     string
	collBytes     []byte // currently-served collection poster
	uploads       [][]byte
	origColl      []byte
}

// setThumbPath points one member's record at a new thumb path, the
// way Plex does when that item's poster changes (the path embeds
// updatedAt). The bytes served at the new path are the member's.
func (f *genFakePMS) setThumbPath(id, path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.thumbOverride[id] = path
}

func genSolidPNG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i+3 < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func memberColor(i int) color.RGBA {
	return color.RGBA{uint8(30 + i*8), uint8(200 - i*5), uint8(60 + i*3), 255}
}

func newGenFakePMS(t *testing.T, n int) (*genFakePMS, *httptest.Server) {
	t.Helper()
	f := &genFakePMS{byID: map[string]genMember{}, failThumb: map[string]bool{}, thumbOverride: map[string]string{}, collThumb: "/library/metadata/900/thumb/1"}
	f.origColl = genSolidPNG(t, 200, 300, color.RGBA{90, 90, 96, 255})
	f.collBytes = f.origColl
	for i := 0; i < n; i++ {
		m := genMember{id: fmt.Sprint(2001 + i), title: fmt.Sprintf("Member %02d", i+1), col: memberColor(i)}
		m.posterPNG = genSolidPNG(t, 200, 300, m.col)
		m.artPNG = genSolidPNG(t, 640, 360, m.col)
		f.members = append(f.members, m)
		f.byID[m.id] = m
	}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *genFakePMS) memberRecord(m genMember) map[string]any {
	thumb := "/library/metadata/" + m.id + "/thumb/1"
	if o := f.thumbOverride[m.id]; o != "" {
		thumb = o
	}
	return map[string]any{
		"ratingKey": m.id, "type": "movie", "title": m.title, "year": 2001,
		"thumb": thumb,
		"art":   "/library/metadata/" + m.id + "/art/1",
	}
}

func (f *genFakePMS) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.Path
	meta := func(list []any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{
			"size": len(list), "totalSize": len(list), "Metadata": list}})
	}
	if !strings.HasPrefix(path, "/library/metadata/") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	rest := strings.TrimPrefix(path, "/library/metadata/")
	parts := strings.Split(rest, "/")
	id := parts[0]

	// Batch records (GetItemsBatch joins keys with commas).
	if strings.Contains(id, ",") && len(parts) == 1 {
		list := []any{}
		for _, k := range strings.Split(id, ",") {
			if m, ok := f.byID[k]; ok {
				list = append(list, f.memberRecord(m))
			}
		}
		meta(list)
		return
	}
	// Collection record / children / poster IO.
	if id == "900" {
		switch {
		case len(parts) == 1 && r.Method == http.MethodGet:
			meta([]any{map[string]any{
				"ratingKey": "900", "type": "collection", "title": "Spooky Season",
				"childCount": len(f.members), "smart": 0, "librarySectionID": 1,
				"thumb": f.collThumb,
			}})
		case len(parts) == 2 && parts[1] == "children":
			list := []any{}
			for _, m := range f.members {
				list = append(list, f.memberRecord(m))
			}
			meta(list)
		case len(parts) == 3 && parts[1] == "thumb" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(f.collBytes)
		case len(parts) == 2 && parts[1] == "posters" && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			up := extractMultipartFile(body)
			f.uploads = append(f.uploads, up)
			f.collBytes = up
			f.collThumb = "/library/metadata/900/thumb/2"
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
		return
	}
	// Member records + images.
	if m, ok := f.byID[id]; ok {
		switch {
		case len(parts) == 1 && r.Method == http.MethodGet:
			meta([]any{f.memberRecord(m)})
		case len(parts) == 3 && parts[1] == "thumb" && r.Method == http.MethodGet:
			if f.failThumb[id] {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(m.posterPNG)
		case len(parts) == 3 && parts[1] == "art" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(m.artPNG)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func newGenTestServer(t *testing.T, pmsURL string) (*httptest.Server, string) {
	t.Helper()
	root := filepath.Join("..", "..")
	webFS := os.DirFS(filepath.Join(root, "web"))
	badgesFS := os.DirFS(filepath.Join(root, "assets", "badges"))
	catalog, err := os.ReadFile(filepath.Join(root, "assets", "collection-art.json"))
	if err != nil {
		t.Fatal(err)
	}
	webSub, _ := fs.Sub(webFS, ".")
	dataDir := t.TempDir()
	srv, err := NewServer(Config{
		DataDir:      dataDir,
		Version:      "1.0.0-test",
		SessionToken: testToken,
		WebFS:        webSub,
		BadgesFS:     badgesFS,
		CatalogJSON:  catalog,
	})
	if err != nil {
		t.Fatal(err)
	}
	fake := &plex.Server{
		Name: "Fake", Machine: "fake-machine", Token: "t", Owned: true,
		Connections:    []plex.Connection{{URI: pmsURL, Local: true}},
		BestConnection: plex.Connection{URI: pmsURL, Local: true},
	}
	srv.servers = []*plex.Server{fake}
	srv.selectedMachine = "fake-machine"
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, dataDir
}

func genPreview(t *testing.T, ts *httptest.Server, body string) map[string]any {
	t.Helper()
	_, out := post(t, ts.URL+"/api/postergen/preview", testToken, body)
	if out["ok"] != true {
		t.Fatalf("preview failed: %v", out)
	}
	return out
}

func TestPosterGenPreviewFlowerAndApplyThroughBackupGate(t *testing.T) {
	fake, pms := newGenFakePMS(t, 25)
	ts, dataDir := newGenTestServer(t, pms.URL)

	out := genPreview(t, ts, `{"ratingKey":"900","template":"mosaic","grid":"flower","plate":true}`)
	art := out["art"].(map[string]any)
	if art["assetId"] == "" || !strings.HasPrefix(art["preview"].(string), "data:image/png;base64,") {
		t.Fatalf("art = %v", art)
	}
	stats := out["stats"].(map[string]any)
	if stats["usedMembers"] != float64(25) || stats["memberTotal"] != float64(25) || stats["skippedPosters"] != float64(0) {
		t.Fatalf("stats = %v", stats)
	}
	// Preview wrote NOTHING to Plex.
	if len(fake.uploads) != 0 {
		t.Fatalf("preview uploaded %d posters — preview must be write-free", len(fake.uploads))
	}
	wantBytes, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(art["preview"].(string), "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}

	// Apply through the EXISTING setPoster path (the backup gate).
	_, applied := post(t, ts.URL+"/api/plex/setPoster", testToken,
		fmt.Sprintf(`{"ratingKey":"900","assetId":%q}`, art["assetId"]))
	if applied["ok"] != true {
		t.Fatalf("setPoster: %v", applied)
	}
	if len(fake.uploads) != 1 {
		t.Fatalf("uploads = %d, want 1", len(fake.uploads))
	}
	if !bytes.Equal(fake.uploads[0], wantBytes) {
		t.Fatalf("the fake received %d bytes that differ from the previewed render (%d)", len(fake.uploads[0]), len(wantBytes))
	}
	// A verified backup of the ORIGINAL poster exists on disk.
	backupImg := filepath.Join(dataDir, "overlay-backups", "fake-machine", "900.png")
	if _, err := os.Stat(backupImg); err != nil {
		backupImg = filepath.Join(dataDir, "overlay-backups", "fake-machine", "900.jpg")
		if _, err2 := os.Stat(backupImg); err2 != nil {
			t.Fatalf("no backup image at %s (%v / %v)", backupImg, err, err2)
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "overlay-backups", "fake-machine", "900.json")); err != nil {
		t.Fatalf("backup sidecar missing: %v", err)
	}
	// Restore puts the original bytes back.
	_, restored := post(t, ts.URL+"/api/overlay/restore", testToken, `{"ratingKey":"900"}`)
	if restored["ok"] != true {
		t.Fatalf("restore: %v", restored)
	}
	if !bytes.Equal(fake.collBytes, fake.origColl) {
		t.Fatalf("after restore the served poster is not the original (%d vs %d bytes)", len(fake.collBytes), len(fake.origColl))
	}
}

func TestPosterGenDraftModeAndSkips(t *testing.T) {
	fake, pms := newGenFakePMS(t, 12)
	ts, _ := newGenTestServer(t, pms.URL)

	// Draft mode: explicit member keys, 3×3 from the first 9.
	keys := []string{}
	for _, m := range fake.members[:9] {
		keys = append(keys, m.id)
	}
	kb, _ := json.Marshal(keys)
	out := genPreview(t, ts, fmt.Sprintf(`{"memberKeys":%s,"title":"Draft Picks","template":"mosaic","grid":"3x3","plate":true}`, kb))
	stats := out["stats"].(map[string]any)
	if stats["usedMembers"] != float64(9) || stats["selectedMembers"] != float64(9) {
		t.Fatalf("draft stats = %v", stats)
	}

	// One dead poster: the flower render must skip it, count it, and
	// say so — not fail, not render a gap. The failed member is one
	// whose poster has NOT been fetched yet this session (index 10,
	// outside the draft's nine): since v1.21.1 a poster already
	// fetched successfully is served from the preview image cache —
	// correctly, it came from the same thumb path — so failing an
	// already-fetched member would no longer exercise the skip.
	fake.failThumb[fake.members[10].id] = true
	out = genPreview(t, ts, `{"ratingKey":"900","template":"mosaic","grid":"flower"}`)
	stats = out["stats"].(map[string]any)
	if stats["skippedPosters"] != float64(1) || stats["usedMembers"] != float64(11) {
		t.Fatalf("skip stats = %v", stats)
	}
	notes, _ := stats["notes"].([]any)
	joined := fmt.Sprint(notes)
	if !strings.Contains(joined, "couldn't be fetched") {
		t.Fatalf("notes = %v", notes)
	}

	// Shortfall: a 3×3 over a 4-member draft refuses plainly.
	out2 := func() map[string]any {
		_, o := post(t, ts.URL+"/api/postergen/preview", testToken,
			fmt.Sprintf(`{"memberKeys":%s,"title":"Tiny","template":"mosaic","grid":"3x3"}`,
				func() []byte { b, _ := json.Marshal(keys[:4]); return b }()))
		return o
	}()
	if out2["ok"] != false || !strings.Contains(fmt.Sprint(out2["error"]), "needs 9 posters") {
		t.Fatalf("shortfall = %v", out2)
	}
}

func TestPosterGenPreviewScalePlumbing(t *testing.T) {
	// v1.21.2: the preview request's "scale" reaches the mosaic
	// render (a 0.5 render differs from the default), and an
	// out-of-range value clamps instead of erroring.
	_, pms := newGenFakePMS(t, 9)
	ts, _ := newGenTestServer(t, pms.URL)
	plain := genPreview(t, ts, `{"ratingKey":"900","template":"mosaic","grid":"3x3"}`)
	half := genPreview(t, ts, `{"ratingKey":"900","template":"mosaic","grid":"3x3","scale":0.5}`)
	if plain["art"].(map[string]any)["preview"] == half["art"].(map[string]any)["preview"] {
		t.Fatal("scale 0.5 preview rendered identically to the default — scale is not plumbed")
	}
	clamped := genPreview(t, ts, `{"ratingKey":"900","template":"mosaic","grid":"3x3","scale":99}`)
	if clamped["art"].(map[string]any)["assetId"] == "" {
		t.Fatal("an out-of-range scale should clamp and still render")
	}
}

func TestPosterGenBackgroundsUploadAndURL(t *testing.T) {
	_, pms := newGenFakePMS(t, 6)
	ts, _ := newGenTestServer(t, pms.URL)

	// Upload → asset → hero background.
	up := uploadTestImage(t, ts, "bg.png", genSolidPNG(t, 640, 400, color.RGBA{120, 40, 160, 255}))
	assetID := up["art"].(map[string]any)["assetId"].(string)
	out := genPreview(t, ts, fmt.Sprintf(
		`{"ratingKey":"900","template":"hero","title":"Uploaded BG","background":{"kind":"asset","assetId":%q},"focalX":0.5,"focalY":0.5}`, assetID))
	if out["art"].(map[string]any)["assetId"] == "" {
		t.Fatalf("hero from upload: %v", out)
	}

	// URL background (the user-art fetch discipline), title card.
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(genSolidPNG(t, 500, 500, color.RGBA{20, 120, 90, 255}))
	}))
	defer imgSrv.Close()
	out = genPreview(t, ts, fmt.Sprintf(
		`{"ratingKey":"900","template":"titlecard","title":"URL Card","background":{"kind":"url","url":%q},"gradient":40,"focalX":0.5,"focalY":0.5}`, imgSrv.URL+"/bg.png"))
	if out["art"].(map[string]any)["assetId"] == "" {
		t.Fatalf("title card from URL: %v", out)
	}

	// Spiral renders through the endpoint too.
	out = genPreview(t, ts, `{"ratingKey":"900","template":"mosaic","grid":"spiral","plate":true}`)
	if out["stats"].(map[string]any)["usedMembers"] != float64(6) {
		t.Fatalf("spiral stats = %v", out["stats"])
	}
}

// uploadTestImage posts a file to the art import endpoint (the same
// upload the generator's "your own image" button uses).
func uploadTestImage(t *testing.T, ts *httptest.Server, name string, data []byte) map[string]any {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/art/importImage", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-PCM-Token", testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out["ok"] != true {
		t.Fatalf("importImage: %v", out)
	}
	return out
}
