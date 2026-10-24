package collectionart

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{10, 20, 30, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{30, 20, 10, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const testCatalog = `{
  "repository": "Kometa-Team/Default-Images",
  "commit": "222a717e85e077a328ab0687b59441b2850c3124",
  "items": [
    {"title": "Star Wars", "category": "franchise", "path": "franchise/color/Star Wars.jpg", "source": "Kometa Default Images"},
    {"title": "Star Wars Rebels", "category": "franchise", "path": "franchise/color/Star Wars Rebels.jpg", "source": "Kometa Default Images"},
    {"title": "Unrelated Thing", "category": "other", "path": "other/Unrelated.jpg", "source": "Kometa Default Images"}
  ]
}`

func newTestService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(t.TempDir(), []byte(testCatalog))
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestNormalizePosterLink(t *testing.T) {
	good := map[string]string{
		"12345":   "https://theposterdb.com/posters/12345",
		"  678  ": "https://theposterdb.com/posters/678",
		// Legacy singular and current plural page links canonicalize alike.
		"https://theposterdb.com/poster/987":       "https://theposterdb.com/posters/987",
		"https://theposterdb.com/posters/987":      "https://theposterdb.com/posters/987",
		"https://www.theposterdb.com/posters/987/": "https://theposterdb.com/posters/987",
		"https://images.theposterdb.com/prod/public/images/posters/optimized/movies/1010875/ABCdef123.jpg":                        "https://images.theposterdb.com/prod/public/images/posters/optimized/movies/1010875/ABCdef123.jpg",
		"https://raw.githubusercontent.com/Kometa-Team/Default-Images/222a717e85e077a328ab0687b59441b2850c3124/chart/color/X.jpg": "https://raw.githubusercontent.com/Kometa-Team/Default-Images/222a717e85e077a328ab0687b59441b2850c3124/chart/color/X.jpg",
	}
	for in, want := range good {
		got, ok := NormalizePosterLink(in)
		if !ok || got != want {
			t.Errorf("NormalizePosterLink(%q) = (%q,%v), want (%q,true)", in, got, ok, want)
		}
	}
	bad := []string{
		"", "not a url",
		"https://theposterdb.com/set/123",                                                       // set link — lists many titles
		"https://theposterdb.com/api/assets/42",                                                 // retired format — TPDb 404s it now
		"https://theposterdb.com/movie/some-title",                                              // not a /posters/ page
		"https://images.theposterdb.com/prod/public/images/posters/optimized/movies/1/ABC.webp", // WebP — undecodable here
		"http://theposterdb.com/posters/123",                                                    // non-HTTPS
		"https://user:pass@theposterdb.com/posters/123",                                         // credentials
		"https://theposterdb.com:8443/posters/123",                                              // explicit port
		"https://example.com/posters/123",                                                       // other host
		"https://raw.githubusercontent.com/Kometa-Team/Default-Images/main/chart/X.jpg",         // unpinned ref
		"https://raw.githubusercontent.com/Kometa-Team/Default-Images/222a717e85e077a328ab0687b59441b2850c3124/chart/X.jpg?dl=1", // query string
	}
	for _, in := range bad {
		if got, ok := NormalizePosterLink(in); ok {
			t.Errorf("NormalizePosterLink(%q) must be rejected, got %q", in, got)
		}
	}
}

func TestTitleKey(t *testing.T) {
	cases := map[string]string{
		"Star Wars Movies":        "star wars",
		"Tom & Jerry Collection":  "tom and jerry",
		"Spider-Man: No Way Home": "spider man no way home",
		"THE   FILMS  of  Studio": "the of studio",
		"":                        "",
		"Collection":              "",
	}
	for in, want := range cases {
		if got := TitleKey(in); got != want {
			t.Errorf("TitleKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTPDBSearchURL(t *testing.T) {
	got := TPDBSearchURL("a b&c")
	want := "https://theposterdb.com/search?term=a%20b%26c"
	if got != want {
		t.Errorf("TPDBSearchURL = %q, want %q", got, want)
	}
}

// rewriteTransport points allowlisted URLs at an httptest server so the
// fetch path can be tested without the live TPDb/Kometa hosts.
type rewriteTransport struct{ base string }

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = strings.TrimPrefix(rt.base, "http://")
	return http.DefaultTransport.RoundTrip(clone)
}

func testClientFor(srv *httptest.Server) *http.Client {
	return &http.Client{
		Transport:     rewriteTransport{base: srv.URL},
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// tpdbPage is a minimal stand-in for a TPDb title page: the resolver
// only needs the CDN poster JPEG in page order.
func tpdbPage(imgPath string) string {
	return `<html><body><picture><source type="image/jpeg" srcset="https://images.theposterdb.com` + imgPath + `"></picture></body></html>`
}

func TestPreviewRejectsNonImageAndOversized(t *testing.T) {
	mux := http.NewServeMux()
	img := func(id, name string) string {
		return "/prod/public/images/posters/optimized/movies/" + id + "/" + name + ".jpg"
	}
	mux.HandleFunc("/posters/1", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tpdbPage(img("1", "AAA")))) })
	mux.HandleFunc(img("1", "AAA"), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>definitely not an image</html>"))
	})
	mux.HandleFunc("/posters/2", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tpdbPage(img("2", "BBB")))) })
	mux.HandleFunc(img("2", "BBB"), func(w http.ResponseWriter, r *http.Request) {
		// Content-Length lie above the cap: must be rejected up front.
		w.Header().Set("Content-Length", "13000000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("x"))
	})
	mux.HandleFunc("/posters/3", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tpdbPage(img("3", "CCC")))) })
	mux.HandleFunc(img("3", "CCC"), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://evil.example/x.png")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/posters/4", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><body>no posters here</body></html>"))
	})
	mux.HandleFunc("/posters/5", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	svc := newTestService(t)
	svc.httpClient = testClientFor(srv)

	if _, err := svc.Preview(context.Background(), map[string]any{"url": "https://theposterdb.com/poster/1"}); err == nil || !strings.Contains(err.Error(), "not a PNG or JPEG") {
		t.Errorf("non-image preview must be rejected, got %v", err)
	}
	if _, err := svc.Preview(context.Background(), map[string]any{"url": "https://theposterdb.com/poster/2"}); err == nil || !strings.Contains(err.Error(), "exceeds 12 MB") {
		t.Errorf("oversized preview must be rejected, got %v", err)
	}
	if _, err := svc.Preview(context.Background(), map[string]any{"url": "https://theposterdb.com/poster/3"}); err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Errorf("redirect must be rejected as HTTP 302, got %v", err)
	}
	if _, err := svc.Preview(context.Background(), map[string]any{"url": "https://theposterdb.com/posters/4"}); err == nil || !strings.Contains(err.Error(), "No poster image was found") {
		t.Errorf("imageless page must fail honestly, got %v", err)
	}
	// A 403 must name the status AND the exact URL that was refused.
	if _, err := svc.Preview(context.Background(), map[string]any{"url": "https://theposterdb.com/posters/5"}); err == nil || !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), "https://theposterdb.com/posters/5") {
		t.Errorf("403 must name status and URL, got %v", err)
	}
	if _, err := svc.Preview(context.Background(), map[string]any{"url": "https://theposterdb.com/set/9"}); err == nil {
		t.Error("set link must be rejected before any fetch")
	}
}

func TestPreviewHappyPathCanonicalPNG(t *testing.T) {
	jpg := makeJPEG(t, 5, 7)
	const cdnPath = "/prod/public/images/posters/optimized/movies/555/EEE.jpg"
	mux := http.NewServeMux()
	mux.HandleFunc("/posters/555", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tpdbPage(cdnPath))) })
	mux.HandleFunc(cdnPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpg)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	svc := newTestService(t)
	svc.httpClient = testClientFor(srv)

	// Legacy singular page link resolves page → first poster image.
	out, err := svc.Preview(context.Background(), map[string]any{"url": "https://theposterdb.com/poster/555"})
	if err != nil {
		t.Fatal(err)
	}
	if out["source"] != "The Poster Database" || out["width"] != 5 || out["height"] != 7 {
		t.Errorf("preview handle wrong: %v", out)
	}
	preview, _ := out["preview"].(string)
	if !strings.HasPrefix(preview, "data:image/png;base64,") {
		t.Errorf("preview must be a PNG data URL, got %.40q", preview)
	}
	asset, ok := svc.GetAsset(out["assetId"].(string))
	if !ok {
		t.Fatal("GetAsset must find the previewed asset")
	}
	if len(asset.Bytes) < 8 || !bytes.Equal(asset.Bytes[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		t.Error("asset bytes must be the canonical PNG conversion of the JPEG")
	}
	if asset.URL != "https://theposterdb.com/posters/555" || asset.Source != "The Poster Database" {
		t.Errorf("asset metadata wrong: %+v", asset)
	}
	if _, ok := svc.GetAsset("nope"); ok {
		t.Error("unknown asset id must not resolve")
	}

	// A direct CDN image address (copied from TPDb) fetches in one step
	// and records itself as the asset URL.
	const direct = "https://images.theposterdb.com" + cdnPath
	out2, err := svc.Preview(context.Background(), map[string]any{"url": direct})
	if err != nil {
		t.Fatal(err)
	}
	asset2, ok := svc.GetAsset(out2["assetId"].(string))
	if !ok || asset2.URL != direct {
		t.Errorf("direct CDN preview metadata wrong: %+v", asset2)
	}
}

func TestImportBytes(t *testing.T) {
	svc := newTestService(t)
	pngB := makePNG(t, 3, 2)
	out, err := svc.ImportBytes("poster final.png", pngB)
	if err != nil {
		t.Fatal(err)
	}
	if out["width"] != 3 || out["height"] != 2 || out["label"] != "poster final.png" || out["source"] != "Imported image" {
		t.Errorf("import result wrong: %v", out)
	}
	asset, ok := svc.GetAsset(out["assetId"].(string))
	if !ok || !bytes.Equal(asset.Bytes, pngB) {
		t.Error("imported PNG must round-trip byte-identically (already canonical)")
	}
	if _, err := svc.ImportBytes("bad.png", []byte("garbage bytes here")); err == nil {
		t.Error("garbage bytes must be rejected")
	}
	if _, err := svc.ImportBytes("big.png", make([]byte, MaxBytes+1)); err == nil {
		t.Error("oversized import must be rejected")
	}
	if _, err := svc.ImportFile("/nonexistent/poster.png"); err == nil {
		t.Error("missing file import must error")
	}
}

func TestSuggestionsAndRemember(t *testing.T) {
	svc := newTestService(t)
	out := svc.Suggestions("Star Wars")
	items, _ := out["items"].([]map[string]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 suggestions, got %v", out)
	}
	if items[0]["score"] != 100 || items[1]["score"] != 40 {
		t.Errorf("scores wrong: %v / %v", items[0]["score"], items[1]["score"])
	}
	url0, _ := items[0]["url"].(string)
	if !strings.HasPrefix(url0, "https://raw.githubusercontent.com/Kometa-Team/Default-Images/222a717e85e077a328ab0687b59441b2850c3124/franchise/color/Star%20Wars.jpg") {
		t.Errorf("catalog URL wrong: %s", url0)
	}
	if out["searchUrl"] != "https://theposterdb.com/search?term=Star%20Wars" {
		t.Errorf("searchUrl wrong: %v", out["searchUrl"])
	}
	if got := svc.Suggestions("Nothing Matching"); len(got["items"].([]map[string]any)) != 0 {
		t.Errorf("unrelated title must have no suggestions: %v", got)
	}

	// Remember a URL-backed asset, then it leads the suggestions.
	// Bare ID 777 resolves to a TPDb title page; the page names the
	// poster image, every other path serves the image itself.
	jpg := makeJPEG(t, 4, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/posters/777" {
			_, _ = w.Write([]byte(tpdbPage("/prod/public/images/posters/optimized/movies/777/DDD.jpg")))
			return
		}
		_, _ = w.Write(jpg)
	}))
	defer srv.Close()
	svc.httpClient = testClientFor(srv)
	art, err := svc.Preview(context.Background(), map[string]any{"url": "777"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Remember("My Picks", art["assetId"].(string)); err != nil {
		t.Fatal(err)
	}
	out2 := svc.Suggestions("My Picks")
	items2, _ := out2["items"].([]map[string]any)
	if len(items2) == 0 || items2[0]["source"] != "Your saved choice" || items2[0]["score"] != 200 {
		t.Errorf("saved choice must lead suggestions: %v", out2)
	}
	// Remembering an unknown asset errors (JS getAsset throws).
	if err := svc.Remember("X", "deadbeef"); err == nil {
		t.Error("remember with unknown asset must error")
	}
}
