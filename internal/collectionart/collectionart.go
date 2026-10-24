// Package collectionart is a faithful Go port of the Electron app's
// Collection-Art finder (v0.4/src/collection-art.js).
//
// Discovery/download only. This package has no Plex client and no Plex
// mutation capability: it finds candidate collection posters (the bundled
// Kometa Default Images catalog + the user's saved choices), downloads and
// validates artwork from allowlisted HTTPS hosts, and hands back a
// canonical-PNG asset that the overlay safety core (internal/overlays)
// applies under its backup gate.
//
// Image fetch rules:
//   - Allowlisted HTTPS hosts only: TPDb title pages
//     (theposterdb.com/posters/{id}) and TPDb CDN image links
//     (images.theposterdb.com), plus raw.githubusercontent.com
//     Kometa-Team/Default-Images pinned-commit image URLs. A bare
//     numeric ID is treated as a TPDb title-page ID.
//   - Redirects are rejected (manual redirect handling; a redirect
//     response is an HTTP error here).
//   - 12 MB cap (MaxBytes), enforced on Content-Length and while streaming.
//   - PNG/JPEG magic-byte check, then a real decode; the asset bytes are
//     the canonical PNG re-encode (JS used Electron nativeImage.toPNG();
//     stdlib image/png.Encode is the equivalent).
//   - The cache file name is the SHA-256 of the canonical bytes, and
//     GetAsset re-verifies the hash before handing bytes out.
//
// FUTURE-AI — TPDb repair (2026-10-20). The original scheme (inherited
// verbatim from the Electron collection-art.js) fetched
// https://theposterdb.com/api/assets/{id} with the HTTP client's default
// User-Agent. Live checks from the build sandbox on 2026-10-20 showed
// BOTH halves of that dead:
//   - Cloudflare answers 403 to Go's default UA ("Go-http-client/1.1")
//     on theposterdb.com AND images.theposterdb.com; a browser UA gets
//     200. That 403 is what the owner saw as "TPDb is failing to connect".
//   - /api/assets/{id} returns 404 even with a browser UA, the singular
//     /poster/{id} page is 404, and no api.theposterdb.com host answers.
//     Poster pages now live at /posters/{id} — a title page listing
//     poster variants whose images are served from
//     images.theposterdb.com with hash file names that cannot be
//     derived from any ID.
//
// So Preview now resolves a TPDb title page by fetching it with a
// browser UA and taking the first poster JPEG in page order (the
// preview step shows the user exactly which poster was picked), and it
// accepts a direct images.theposterdb.com image address for users who
// want one specific variant (right-click → Copy image address on TPDb).
// If TPDb escalates to a JavaScript/Cloudflare challenge, the UA stops
// being enough and there is no honest programmatic fix — the error
// messages say so and point at the download + Import path instead.
// Live-verified from the sandbox 2026-10-20; UNVERIFIED on the owner's
// Windows machine/network.
package collectionart

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg" // register JPEG decoder for image.Decode
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MaxBytes is the poster size cap from collection-art.js (12 MB).
const MaxBytes = 12 * 1024 * 1024

// tpdbPageMaxBytes caps the TPDb title-page HTML read (pages observed
// around 100 KB; 4 MB is generous headroom, not a target).
const tpdbPageMaxBytes = 4 * 1024 * 1024

// browserUA is sent on every artwork fetch. FUTURE-AI: this is load-
// bearing, not cosmetic — Cloudflare 403s Go's default UA on both TPDb
// hosts (live-verified 2026-10-20, see the package doc). The exact
// string is the one verified live; any current Chrome UA should do,
// but do not "simplify" this back to the default UA.
const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36"

var pngMagic = []byte{137, 80, 78, 71, 13, 10, 26, 10}

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

var (
	wordDropRe = regexp.MustCompile(`\b(collection|movies|films)\b`)
	nonAlnumRe = regexp.MustCompile(`[^a-z0-9]+`)
	digitsRe   = regexp.MustCompile(`^\d+$`)
	// TPDb title pages: /posters/{id} today, /poster/{id} before the
	// site restructure — both accepted, canonical form is plural.
	tpdbPageRe = regexp.MustCompile(`^/posters?/(\d+)/?$`)
	// The retired /api/assets/{id} format gets its own explicit error.
	tpdbAPIRe = regexp.MustCompile(`^/api/assets/\d+/?$`)
	// Direct CDN image links (right-click → Copy image address on TPDb).
	// JPG/PNG only: WebP is offered too, but the decoder here cannot
	// read it, so accepting it would just fail later and vaguer.
	tpdbCDNPathRe = regexp.MustCompile(`(?i)^/\S+\.(jpg|jpeg|png)$`)
	// Poster JPEGs referenced by a TPDb title page, in page order.
	tpdbImgRe    = regexp.MustCompile(`https://images\.theposterdb\.com/[^"'\s<>]+\.jpg`)
	kometaPathRe = regexp.MustCompile(`(?i)^/Kometa-Team/Default-Images/[a-f0-9]{40}/.+\.(jpg|png)$`)
)

// TitleKey normalizes a title for saved-choice lookup and catalog
// matching (JS titleKey): lowercase, '&' → ' and ', drop the words
// collection/movies/films, collapse non-alphanumerics to single spaces.
//
// NOTE: JS first applies Unicode NFKC normalization. Go's stdlib has no
// NFKC tables, so compatibility characters (fullwidth forms, ligatures)
// are only partially folded here (strings.ToLower + the rules above);
// plain ASCII titles — the catalog's domain — match JS exactly.
func TitleKey(s string) string {
	v := strings.ToLower(s)
	v = strings.ReplaceAll(v, "&", " and ")
	v = wordDropRe.ReplaceAllString(v, "")
	v = nonAlnumRe.ReplaceAllString(v, " ")
	return strings.TrimSpace(v)
}

// normalizePosterLinkErr resolves user input to the canonical fetch
// target (JS normalizePosterLink kept its exact error strings where the
// situation is unchanged; the TPDb cases were rewritten for the site's
// current structure — see the package doc's 2026-10-20 note). The
// exported NormalizePosterLink wraps it into (string, bool).
func normalizePosterLinkErr(input string) (string, error) {
	value := strings.TrimSpace(input)
	if digitsRe.MatchString(value) {
		return "https://theposterdb.com/posters/" + value, nil
	}
	u, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("Paste a TPDb poster link or numeric poster ID.")
	}
	if u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return "", fmt.Errorf("An HTTPS image link without credentials is required.")
	}
	switch u.Hostname() {
	case "theposterdb.com", "www.theposterdb.com":
		if m := tpdbPageRe.FindStringSubmatch(u.Path); m != nil {
			return "https://theposterdb.com/posters/" + m[1], nil
		}
		if tpdbAPIRe.MatchString(u.Path) {
			return "", fmt.Errorf("That is an old-style TPDb asset link — TPDb retired that format. Open the title on theposterdb.com and paste its /posters/ page link, or right-click a poster there and paste its image address instead.")
		}
		return "", fmt.Errorf("Paste a TPDb title-page link (theposterdb.com/posters/…), an image address copied from TPDb, or a numeric poster ID. Set-page links list many titles — open one title from the set first.")
	case "images.theposterdb.com":
		if tpdbCDNPathRe.MatchString(u.Path) && u.RawQuery == "" && u.Fragment == "" {
			return u.String(), nil
		}
		return "", fmt.Errorf("That TPDb image link is not a JPG or PNG poster file. On TPDb, right-click the poster and copy its image address.")
	case "raw.githubusercontent.com":
		if kometaPathRe.MatchString(u.Path) && u.RawQuery == "" && u.Fragment == "" {
			return u.String(), nil
		}
	}
	return "", fmt.Errorf("Use a TPDb poster link, a suggested Kometa image, or import a downloaded image.")
}

// NormalizePosterLink resolves a TPDb title-page link / bare title ID /
// direct TPDb CDN image link / allowlisted Kometa image URL to the
// canonical fetch target. ok=false for anything else (set-page links,
// retired /api/assets/ links, non-HTTPS, credentials, ports, other
// hosts). Error strings are preserved internally for Preview.
func NormalizePosterLink(s string) (string, bool) {
	u, err := normalizePosterLinkErr(s)
	return u, err == nil
}

// encodeURIComponent is JS encodeURIComponent.
func encodeURIComponent(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			strings.ContainsRune("-_.!~*'()", rune(c)) {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// TPDBSearchURL is JS tpdbSearchUrl (title trimmed, max 200 chars).
func TPDBSearchURL(title string) string {
	t := strings.TrimSpace(title)
	if r := []rune(t); len(r) > 200 {
		t = string(r[:200])
	}
	return "https://theposterdb.com/search?term=" + encodeURIComponent(t)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// Service
// ---------------------------------------------------------------------------

type catalog struct {
	Repository string           `json:"repository"`
	Commit     string           `json:"commit"`
	Items      []map[string]any `json:"items"`
}

type assetRecord struct {
	ID     string
	File   string
	URL    string
	Source string
	Label  string
	Width  int
	Height int
	Type   string
}

// Asset is a validated poster ready for the overlay apply path. Bytes is
// the canonical PNG (see package doc).
type Asset struct {
	Bytes  []byte
	Source string
	URL    string
}

// Service is JS createCollectionArt, minus the injected Electron
// decodeImage/fetchImage (replaced by stdlib image decoding and an
// *http.Client that refuses redirects).
type Service struct {
	userDataDir string
	cacheDir    string
	mappingFile string
	catalog     catalog
	assets      map[string]*assetRecord

	// httpClient is internal so same-package tests can substitute a
	// transport (e.g. pointing at an httptest server); production code
	// uses the redirect-refusing client from NewService.
	httpClient *http.Client
}

// NewService parses the bundled catalog (assets/collection-art.json).
func NewService(userDataDir string, catalogJSON []byte) (*Service, error) {
	var cat catalog
	if err := json.Unmarshal(catalogJSON, &cat); err != nil {
		return nil, fmt.Errorf("collection-art catalog: %w", err)
	}
	cacheDir := filepath.Join(userDataDir, "collection-art")
	return &Service{
		userDataDir: userDataDir,
		cacheDir:    cacheDir,
		mappingFile: filepath.Join(cacheDir, "choices.json"),
		catalog:     cat,
		assets:      map[string]*assetRecord{},
		httpClient: &http.Client{
			// redirect: 'manual' (JS) — never follow; the caller sees the
			// redirect response itself and treats it as not-ok.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (s *Service) choices() map[string]map[string]any {
	out := map[string]map[string]any{}
	raw, err := os.ReadFile(s.mappingFile)
	if err != nil {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]map[string]any{}
	}
	if out == nil {
		return map[string]map[string]any{}
	}
	return out
}

// Suggestions mirrors JS suggestions(): saved choice first (score 200),
// then catalog matches (100 exact key / 60 catalog-key inside title-key /
// 40 title-key inside catalog-key), sorted by score desc then title,
// capped at 12. Return shape: { items: [...], searchUrl }.
func (s *Service) Suggestions(title string) map[string]any {
	key := TitleKey(title)
	saved := s.choices()[key]
	var savedURL string
	if saved != nil {
		savedURL, _ = saved["url"].(string)
	}
	type scored struct {
		item  map[string]any
		score int
		title string
	}
	matches := []scored{}
	if key != "" {
		for _, item := range s.catalog.Items {
			itemTitle, _ := item["title"].(string)
			itemPath, _ := item["path"].(string)
			candidate := TitleKey(itemTitle)
			score := 0
			switch {
			case candidate == key:
				score = 100
			case len([]rune(candidate)) > 2 && strings.Contains(" "+key+" ", " "+candidate+" "):
				score = 60
			case len([]rune(key)) > 2 && strings.Contains(" "+candidate+" ", " "+key+" "):
				score = 40
			}
			if score == 0 {
				continue
			}
			cp := map[string]any{}
			for k, v := range item {
				cp[k] = v
			}
			segs := strings.Split(itemPath, "/")
			for i, seg := range segs {
				segs[i] = encodeURIComponent(seg)
			}
			cp["score"] = score
			cp["url"] = "https://raw.githubusercontent.com/" + s.catalog.Repository + "/" + s.catalog.Commit + "/" + strings.Join(segs, "/")
			matches = append(matches, scored{item: cp, score: score, title: itemTitle})
		}
		sort.SliceStable(matches, func(i, j int) bool {
			if matches[i].score != matches[j].score {
				return matches[i].score > matches[j].score
			}
			return matches[i].title < matches[j].title
		})
		if len(matches) > 12 {
			matches = matches[:12]
		}
	}
	items := []map[string]any{}
	if saved != nil {
		items = append(items, map[string]any{"title": title, "url": savedURL, "source": "Your saved choice", "score": 200})
	}
	for _, m := range matches {
		if u, _ := m.item["url"].(string); saved != nil && u == savedURL {
			continue
		}
		items = append(items, m.item)
	}
	return map[string]any{"items": items, "searchUrl": TPDBSearchURL(title)}
}

// cacheImage validates/decodes before exposing preview bytes or creating
// an upload handle (JS cacheImage). Returns the preview handle shape:
// { assetId, url, source, label, width, height, preview(data URL) }.
func (s *Service) cacheImage(data []byte, urlStr, source, label string) (map[string]any, error) {
	if len(data) == 0 || len(data) > MaxBytes {
		return nil, fmt.Errorf("Poster must be between 1 byte and 12 MB.")
	}
	isPNG := len(data) >= 8 && bytes.Equal(data[:8], pngMagic)
	isJPG := len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff
	if !isPNG && !isJPG {
		return nil, fmt.Errorf("The response is not a PNG or JPEG image. Download it in your browser and import the image instead.")
	}
	// Real decode (JS: nativeImage.createFromBuffer). The canonical asset
	// bytes are the PNG re-encode, as with nativeImage.toPNG().
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("The image could not be decoded.")
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("The image could not be decoded.")
	}
	canonical := buf.Bytes()
	if len(canonical) > MaxBytes {
		return nil, fmt.Errorf("Decoded poster exceeds 12 MB.")
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width == 0 || height == 0 {
		return nil, fmt.Errorf("The image could not be decoded.")
	}
	id := sha256Hex(canonical)
	if err := os.MkdirAll(s.cacheDir, 0o755); err != nil {
		return nil, err
	}
	file := filepath.Join(s.cacheDir, id+".image")
	if err := os.WriteFile(file, canonical, 0o644); err != nil {
		return nil, err
	}
	rec := &assetRecord{ID: id, File: file, URL: urlStr, Source: source, Label: label, Width: width, Height: height, Type: "image/png"}
	s.assets[id] = rec
	return map[string]any{
		"assetId": id,
		"url":     urlStr,
		"source":  source,
		"label":   label,
		"width":   width,
		"height":  height,
		"preview": "data:image/png;base64," + base64.StdEncoding.EncodeToString(canonical),
	}, nil
}

// previewInput extracts the poster link/ID from the input map. CONTRACT
// ADAPTATION: in JS, preview(input) takes the raw string (link or ID).
// The Go contract passes a map; the link is read from the first present
// string key among url, input, link, href, poster, value, id.
func previewInput(input map[string]any) string {
	for _, k := range []string{"url", "input", "link", "href", "poster", "value", "id"} {
		if v, ok := input[k].(string); ok && v != "" {
			return v
		}
		if v, ok := input[k].(float64); ok {
			return strconv.FormatFloat(v, 'f', -1, 64)
		}
	}
	return ""
}

// statusError is the honest failure for a non-2xx artwork response: it
// names the status AND the exact URL, and for a 403 it says what a 403
// from these hosts actually means (bot filtering) instead of letting
// the user guess at a "connection failure".
func statusError(rawURL string, status int) error {
	if status == http.StatusForbidden && strings.Contains(rawURL, "theposterdb.com") {
		return fmt.Errorf("TPDb returned HTTP 403 for %s — its bot filter refused this app's request. That filter can tighten without warning; if it keeps happening, open the page in your browser, download the poster, and use Import instead.", rawURL)
	}
	return fmt.Errorf("Artwork host returned HTTP %d for %s. Open TPDb in your browser, download the poster and import it.", status, rawURL)
}

// fetchURL GETs rawURL with the browser UA (see browserUA) and returns
// the body, capped at limit bytes. Every failure names the URL — the
// user should never have to guess which request died.
func (s *Service) fetchURL(ctx context.Context, rawURL, accept string, limit int64, tooBig string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", accept)
	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Could not reach %s (%v). Check your connection; if the page opens in your browser, download the poster there and use Import instead.", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError(rawURL, resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return nil, fmt.Errorf("%s", tooBig)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s", tooBig)
	}
	return data, nil
}

// isTPDBPage reports whether a normalized link is a TPDb title page
// (as opposed to a direct image link): those need the page-resolution
// step in Preview.
func isTPDBPage(link string) bool {
	u, err := url.Parse(link)
	return err == nil && u.Hostname() == "theposterdb.com" && strings.HasPrefix(u.Path, "/posters/")
}

// firstTPDBPosterImage returns the first poster JPEG (document order =
// the site's own ranking) referenced by a TPDb title page, or "" when
// the page carries none. Only /images/posters/ paths qualify, so
// logos/avatars/backdrops on the same CDN never win by accident.
func firstTPDBPosterImage(page []byte) string {
	for _, m := range tpdbImgRe.FindAll(page, -1) {
		if u := string(m); strings.Contains(u, "/images/posters/") {
			return u
		}
	}
	return ""
}

// Preview downloads, validates and decodes a poster from an allowlisted
// host (JS preview). No credentials of any kind are sent to artwork
// hosts. A TPDb title-page link is resolved in two fetches — page HTML,
// then the first poster image on it (see the package doc's 2026-10-20
// note); direct image links and Kometa links fetch in one.
func (s *Service) Preview(ctx context.Context, input map[string]any) (map[string]any, error) {
	link, err := normalizePosterLinkErr(previewInput(input))
	if err != nil {
		return nil, err
	}
	// 30s total for up to two sequential fetches (was 20s for one).
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	source := "Kometa Default Images"
	if strings.Contains(link, "theposterdb.com") {
		source = "The Poster Database"
	}
	imageURL := link
	if isTPDBPage(link) {
		page, err := s.fetchURL(ctx, link, "text/html,application/xhtml+xml", tpdbPageMaxBytes, "That TPDb page is unexpectedly large and was not read.")
		if err != nil {
			return nil, err
		}
		imageURL = firstTPDBPosterImage(page)
		if imageURL == "" {
			return nil, fmt.Errorf("No poster image was found on %s. TPDb may have changed its page layout, or that page has no posters yet. Open it in your browser, right-click the poster you want, copy its image address, and paste that here — or download it and use Import.", link)
		}
	}
	data, err := s.fetchURL(ctx, imageURL, "image/png,image/jpeg", MaxBytes, "Poster exceeds 12 MB.")
	if err != nil {
		return nil, err
	}
	// The recorded asset URL stays the normalized input link, so a
	// remembered choice re-resolves the same way next time.
	return s.cacheImage(data, link, source, "Selected collection poster")
}

// ImportFile validates and caches a local image file (JS importFile).
func (s *Service) ImportFile(filePath string) (map[string]any, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxBytes {
		return nil, fmt.Errorf("Poster exceeds 12 MB.")
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	return s.cacheImage(data, "", "Imported image", path.Base(filepath.ToSlash(filePath)))
}

// ImportBytes is ImportFile for the Go web-upload flow: identical
// validation, bytes supplied directly. filename supplies the label
// (its base name), as ImportFile uses the file's base name.
func (s *Service) ImportBytes(filename string, data []byte) (map[string]any, error) {
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("Poster exceeds 12 MB.")
	}
	label := path.Base(filepath.ToSlash(filename))
	if label == "." || label == "/" {
		label = ""
	}
	return s.cacheImage(data, "", "Imported image", label)
}

// ImportRendered registers an app-generated PNG (the v1.20 poster
// generator's output) as a cached asset, through the same validation
// + canonical-PNG path as every other asset. From here on the render
// is indistinguishable from a previewed/imported poster: the
// Builder's setPoster flow and its backup gate take it as-is.
func (s *Service) ImportRendered(data []byte, label string) (map[string]any, error) {
	return s.ImportGenerated(data, "Generated poster", label)
}

// ImportGenerated is ImportRendered with a caller-chosen source, so
// an AI-generated background (v1.20) is labeled as one wherever the
// asset surfaces, instead of masquerading as a generator render.
func (s *Service) ImportGenerated(data []byte, source, label string) (map[string]any, error) {
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("Poster exceeds 12 MB.")
	}
	if label == "" {
		label = source
	}
	return s.cacheImage(data, "", source, label)
}

// GetAsset returns a previously previewed/imported asset, re-verifying
// the cached file's hash first (JS getAsset throws on a missing or
// corrupt asset; here that is the ok=false result).
func (s *Service) GetAsset(id string) (*Asset, bool) {
	rec := s.assets[id]
	if rec == nil {
		return nil, false
	}
	data, err := os.ReadFile(rec.File)
	if err != nil {
		return nil, false
	}
	if sha256Hex(data) != rec.ID {
		return nil, false
	}
	return &Asset{Bytes: data, Source: rec.Source, URL: rec.URL}, true
}

// Remember persists title → asset URL as the saved choice (JS remember).
// Local imports (no URL) are not portable mappings and are skipped.
func (s *Service) Remember(title, assetID string) error {
	rec := s.assets[assetID]
	if rec == nil {
		return fmt.Errorf("Preview the poster again before applying.")
	}
	data, err := os.ReadFile(rec.File)
	if err != nil || sha256Hex(data) != rec.ID {
		return fmt.Errorf("Cached poster failed integrity verification. Preview it again.")
	}
	if rec.URL == "" {
		return nil
	}
	choices := s.choices()
	choices[TitleKey(title)] = map[string]any{"url": rec.URL}
	if err := os.MkdirAll(s.cacheDir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(choices, "", "  ")
	tmp := s.mappingFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.mappingFile)
}
