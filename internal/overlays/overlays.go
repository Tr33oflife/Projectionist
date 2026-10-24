// Package overlays is a faithful Go port of the Electron app's overlay
// safety core (v0.4/src/overlays/apply.js) plus the pure helpers it uses
// from v0.4/src/renderer/composite.js (isImageBytes, sidecarFor).
//
// THE NON-NEGOTIABLE ORDER, per item, no exceptions, no skip flags:
//
//  1. Download the CURRENT poster bytes.
//  2. VERIFY the backup: non-empty AND valid image magic bytes
//     (PNG/JPEG/WebP/GIF/AVIF).
//     A failed backup ABORTS THAT ITEM — loudly, recorded in the run, never
//     silently skipped. No backup = no write. This gate is architectural.
//  3. Write backup + JSON sidecar under
//     userData/overlay-backups/{machineId}/{ratingKey}.{ext}.
//     Backups are WRITE-ONCE: an existing backup is never overwritten.
//  4. Upload the composite: multipart first (UNVERIFIED live) → on 404/405
//     AND a local connection only, fall back to a one-shot temp HTTP
//     server + the ?url= form. Anything else fails loudly.
//  5. Record the run: userData/overlay-runs/{runId}.json.
//  6. Per-item applied state under overlay-state/{machineId}/{ratingKey}.json
//     for ReapplyCheck hash diffing.
//
// Restore is the inverse: read backup bytes (integrity-checked against the
// sidecar hash) → upload via the same path. There is NO skip-backup path
// and none may be added.
package overlays

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"plexcollectionmanager/internal/plex"
)

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// Error mirrors JS OverlayError: a message plus the ratingKey it belongs to
// and a human hint. Error() returns only the message, matching how the JS
// run log records err.message.
type Error struct {
	Message   string
	RatingKey string
	Hint      string
}

func (e *Error) Error() string { return e.Message }

func errMsg(err error) string {
	if err == nil {
		return ""
	}
	var oe *Error
	if errors.As(err, &oe) {
		return oe.Message
	}
	return err.Error()
}

// ---------------------------------------------------------------------------
// Pure helpers (ported from renderer/composite.js)
// ---------------------------------------------------------------------------

// SHA256Hex returns the lowercase hex SHA-256 of b (JS sha256hex).
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// IsImageBytes is the magic-byte verification for the backup gate
// (composite.js isImageBytes). Buffers shorter than 8 bytes are rejected.
// PNG: 89 50 4E 47. JPEG: FF D8 FF. WebP: a RIFF/WEBP container whose
// first chunk is VP8 , VP8L, or VP8X. GIF: GIF87a or GIF89a. AVIF/HEIF:
// an ISO-BMFF "ftyp" box with an avif/avis/mif1/msf1/heic/heix major
// brand — those formats share the ftyp box, so the brand is what this
// sniff can honestly claim. typ is "png", "jpg", "webp", "gif",
// "avif", or "".
func IsImageBytes(b []byte) (ok bool, typ string) {
	if len(b) < 8 {
		return false, ""
	}
	if b[0] == 0x89 && b[1] == 0x50 && b[2] == 0x4e && b[3] == 0x47 {
		return true, "png"
	}
	if b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff {
		return true, "jpg"
	}
	if len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a") {
		return true, "gif"
	}
	if len(b) >= 16 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
		switch string(b[12:16]) {
		case "VP8 ", "VP8L", "VP8X":
			return true, "webp"
		}
	}
	if len(b) >= 12 && string(b[4:8]) == "ftyp" {
		switch string(b[8:12]) {
		case "avif", "avis", "mif1", "msf1", "heic", "heix":
			return true, "avif"
		}
	}
	return false, ""
}

func extForImageType(typ string) string {
	switch typ {
	case "png", "webp", "gif", "avif":
		return typ
	default:
		return "jpg"
	}
}

func contentTypeForImageType(typ string) string {
	switch typ {
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	case "avif":
		return "image/avif"
	default:
		return "image/jpeg"
	}
}

// storedContentType picks the sidecar content type for a poster whose
// bytes sniffed as typ. The sniffed type wins whenever the response
// content type is absent, generic, or names a different extension, so
// the stored type and the stored file extension can never disagree.
func storedContentType(responseCT, typ string) string {
	normalized := strings.ToLower(strings.TrimSpace(strings.Split(responseCT, ";")[0]))
	if normalized == "" || normalized == "application/octet-stream" || extForContentType(normalized) != extForImageType(typ) {
		return contentTypeForImageType(typ)
	}
	return normalized
}

func firstBytesHex(b []byte) string {
	n := len(b)
	if n > 8 {
		n = 8
	}
	parts := make([]string, 0, n)
	for _, c := range b[:n] {
		parts = append(parts, fmt.Sprintf("%02x", c))
	}
	out := strings.Join(parts, " ")
	if len(b) > n {
		out += " …"
	}
	return out
}

// badgeSetSummary reduces a badgeSet (raw JSON as received from the caller)
// to the summary shape the JS code stores everywhere:
// { name: string, placements: [...], families?: {...} } or nil for a
// null/absent badgeSet. "families" is included only when the caller
// sent a family layout (Kometa-style recreation, 2026-10-22): the run
// record and sidecars stay an honest description of what was applied.
func badgeSetSummary(raw json.RawMessage) map[string]any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil
	}
	name, _ := m["name"].(string)
	placements, _ := m["placements"].([]any)
	if placements == nil {
		placements = []any{}
	}
	out := map[string]any{"name": name, "placements": placements}
	if fam, ok := m["families"]; ok && fam != nil {
		out["families"] = fam
	}
	return out
}

func badgeSetSummaryRaw(raw json.RawMessage) json.RawMessage {
	s := badgeSetSummary(raw)
	if s == nil {
		return json.RawMessage("null")
	}
	b, _ := json.Marshal(s)
	return b
}

// SidecarFor builds the .json sidecar stored next to every backup
// (composite.js sidecarFor). badgeSet is the raw badgeSet JSON (or nil);
// it is stored in the same {name, placements} summary shape as in JS.
func SidecarFor(ratingKey, title, sourceUrl string, badgeSet json.RawMessage, posterHash, contentType, runId string) map[string]any {
	var bs any
	if s := badgeSetSummary(badgeSet); s != nil {
		bs = s
	}
	return map[string]any{
		"version":     1,
		"ratingKey":   ratingKey,
		"title":       title,
		"backedUpAt":  time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"sourceUrl":   sourceUrl,
		"contentType": contentType,
		"posterHash":  posterHash,
		"badgeSet":    bs,
		"runId":       runId,
	}
}

// BackupPaths is JS backupPaths(): where a backup lives. Deterministic,
// no I/O. ext defaults to "jpg" when empty.
func BackupPaths(userDataDir, machineID, ratingKey, ext string) (dir, imagePath, sidecarPath string) {
	if ext == "" {
		ext = "jpg"
	}
	dir = filepath.Join(userDataDir, "overlay-backups", machineID)
	base := filepath.Join(dir, ratingKey)
	return dir, base + "." + ext, base + ".json"
}

func runsDir(userDataDir string) string { return filepath.Join(userDataDir, "overlay-runs") }

func stateDir(userDataDir, machineID string) string {
	return filepath.Join(userDataDir, "overlay-state", machineID)
}

// ---------------------------------------------------------------------------
// Plex client contract
// ---------------------------------------------------------------------------

// plexClient is the internal interface for the three/four plex.Client calls
// the applier needs. Applier holds this interface (defaulting to the real
// *plex.Client) so tests can substitute a fake without a live server.
// FLAG: NewApplier takes *plex.Client concretely per the parent contract;
// the interface indirection is internal only.
type plexClient interface {
	FetchServed(ctx context.Context, s *plex.Server, path string, timeout time.Duration) ([]byte, string, int, error)
	UploadPosterBytes(ctx context.Context, server *plex.Server, ratingKey string, png []byte) error
	SetPosterFromURL(ctx context.Context, server *plex.Server, ratingKey string, url string) error
	GetItem(ctx context.Context, server *plex.Server, ratingKey string) (*plex.Item, error)
	GetPosters(ctx context.Context, server *plex.Server, ratingKey string) ([]plex.PosterEntry, error)
}

// statusFromError extracts an HTTP status from an upload error, if the
// plex client exposes one (JS read err.status). Several accessor shapes
// and exported field names are accepted.
func statusFromError(err error) int {
	if err == nil {
		return 0
	}
	var sc interface{ StatusCode() int }
	if errors.As(err, &sc) {
		return sc.StatusCode()
	}
	var st interface{ Status() int }
	if errors.As(err, &st) {
		return st.Status()
	}
	var hs interface{ HTTPStatus() int }
	if errors.As(err, &hs) {
		return hs.HTTPStatus()
	}
	// Exported int field named Status / StatusCode / HTTPStatus on a
	// (possibly wrapped) error struct.
	for cur := err; cur != nil; cur = errors.Unwrap(cur) {
		v := reflect.ValueOf(cur)
		if v.Kind() == reflect.Pointer && !v.IsNil() {
			v = v.Elem()
		}
		if v.Kind() == reflect.Struct {
			for _, name := range []string{"Status", "StatusCode", "HTTPStatus"} {
				if f := v.FieldByName(name); f.IsValid() && f.Kind() == reflect.Int {
					return int(f.Int())
				}
			}
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// Applier
// ---------------------------------------------------------------------------

// Applier is the overlay safety core (JS createOverlayApplier).
type Applier struct {
	userDataDir string
	client      plexClient
	// posterHTTPClient fetches poster bytes hosted OFF the Plex
	// server (agent posters on image CDNs — TMDB, TheTVDB). It must
	// never carry the Plex token; fetchExternalPosterBytes builds its
	// requests bare. Nil means http.DefaultClient. Tests point it at
	// an httptest server.
	posterHTTPClient *http.Client
	// landingPollInterval / landingPollBudget bound the
	// verify-after-unknown polling (see pollPosterLanded): how often
	// the server state is re-probed after an unconfirmed poster
	// write, and for how long before the item is honestly reported
	// unconfirmed. Zero means the defaults. Tests shrink them.
	landingPollInterval time.Duration
	landingPollBudget   time.Duration
	// mu serialises phased (streaming) run writes: batches of one run
	// arrive as separate calls and must never interleave their
	// read-modify-write of the run record.
	mu sync.Mutex
}

// NewApplier wires the applier to the real Plex client.
func NewApplier(userDataDir string, plexClient *plex.Client) *Applier {
	a := &Applier{userDataDir: userDataDir}
	if plexClient != nil {
		a.client = plexClient
	}
	return a
}

// fetchPosterBytes downloads raw poster bytes from the server (JS
// fetchPosterBytes). The plex client's FetchServed walks the server's
// ranked connections until one serves — a pinned "best" connection is
// not always reachable (a Docker-hosted server advertises its
// container bridge address, a black hole from this machine). The
// token header is applied inside FetchServed, per the
// sibling-package contract.
func (a *Applier) fetchPosterBytes(ctx context.Context, server *plex.Server, thumbPath string) ([]byte, string, error) {
	if thumbPath == "" || !strings.HasPrefix(thumbPath, "/") {
		return nil, "", &Error{Message: "Item has no poster path to back up", Hint: "The item may have no artwork set."}
	}
	if a.client == nil {
		return nil, "", &Error{Message: "No Plex client configured", Hint: "Backup failed — the item will not be touched."}
	}
	if server == nil || (len(server.Connections) == 0 && server.BestConnection.URI == "") {
		return nil, "", &Error{Message: "No usable connection to the Plex server", Hint: "Pick a server on the Connect tab first."}
	}
	body, contentType, status, err := a.client.FetchServed(ctx, server, thumbPath, 30*time.Second)
	if err != nil {
		return nil, "", &Error{Message: fmt.Sprintf("Could not download current poster (%s)", errMsg(err)), Hint: "Backup failed — the item will not be touched."}
	}
	if status < 200 || status >= 300 {
		return nil, "", &Error{Message: fmt.Sprintf("Poster download returned HTTP %d", status), Hint: "Backup failed — the item will not be touched."}
	}
	ct := strings.Split(contentType, ";")[0]
	return body, ct, nil
}

// externalPosterTimeout bounds one off-server poster fetch, and
// maxEntryPosterBytes caps its body: agent posters are a few hundred
// KB in practice; 32 MiB is generous headroom, not a target.
const (
	externalPosterTimeout = 30 * time.Second
	maxEntryPosterBytes   = 32 << 20
)

// fetchEntryPosterBytes downloads the bytes of ONE posters-list
// ENTRY. Entry keys come in two forms: a server-local path (the
// poster lives in the server's own media store — uploads, and
// providers on servers whose agents cache locally) and a full http(s)
// URL (the poster lives on the agent's image CDN — TMDB / TheTVDB
// posters on a real server). Local paths go through the same
// token-carrying FetchServed as fetchPosterBytes.
//
// URLs are fetched WITHOUT the Plex token (it never leaves the
// server's own API), in two steps:
//  1. Through Plex itself, when the entry carries a server-local
//     thumb address (the usual shape for agent posters): Plex
//     retrieves its own agent posters for the picker regardless of
//     how the CDN treats direct clients.
//  2. Directly from the URL, with a browser User-Agent — some CDNs
//     answer a bare default client with a redirect that never lands
//     (five shows failed live with a terminal HTTP 302).
//
// Bytes from either route are sniff-verified as a supported image
// before use. Any other key form is an error naming the form — the
// backup helper's "no poster path" text must never speak for this
// step. Item THUMB paths are always server-local and keep using
// fetchPosterBytes; this fetcher is only for posters-list entries.
func (a *Applier) fetchEntryPosterBytes(ctx context.Context, server *plex.Server, entry plex.PosterEntry) ([]byte, string, error) {
	key := entry.Key
	switch {
	case strings.HasPrefix(key, "/"):
		return a.fetchPosterBytes(ctx, server, key)
	case strings.HasPrefix(key, "http://") || strings.HasPrefix(key, "https://"):
		var viaPlexErr error
		if strings.HasPrefix(entry.Thumb, "/") {
			body, ct, err := a.fetchPosterBytes(ctx, server, entry.Thumb)
			if err == nil {
				if ok, _ := IsImageBytes(body); !ok {
					err = &Error{Message: "the image Plex served for this poster is not a supported image (PNG/JPEG/WebP/GIF/AVIF)"}
				}
			}
			if err == nil {
				return body, ct, nil
			}
			viaPlexErr = err
		}
		body, ct, err := a.fetchExternalPosterBytes(ctx, key)
		if err == nil {
			return body, ct, nil
		}
		if viaPlexErr != nil {
			return nil, "", &Error{Message: fmt.Sprintf("Poster download failed both ways — via Plex: %s; directly: %s", errMsg(viaPlexErr), errMsg(err)), Hint: "The item will not be touched."}
		}
		return nil, "", err
	default:
		scheme := posterKeyScheme(key)
		if scheme == "" {
			scheme = "none"
		}
		return nil, "", &Error{Message: fmt.Sprintf("The poster's address is neither a server path nor a web URL (scheme %q)", scheme), Hint: "The item will not be touched."}
	}
}

// posterKeyScheme extracts the scheme of a posters-list entry key for
// error messages: the part before "://" (or before ":" for scheme-only
// forms), "" when the key carries no scheme at all.
func posterKeyScheme(key string) string {
	if i := strings.Index(key, "://"); i > 0 {
		return key[:i]
	}
	if i := strings.IndexByte(key, ':'); i > 0 {
		return key[:i]
	}
	return ""
}

// externalPosterUserAgent is the User-Agent sent with off-server
// poster downloads: a plain current-browser string, on purpose. Some
// image CDNs (TheTVDB's, in the live report) answer Go's bare default
// User-Agent with a redirect that never lands on an image.
const externalPosterUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

// finalPosterURL is the address a (possibly redirected) poster
// response actually came from, falling back to the requested URL.
func finalPosterURL(resp *http.Response, fallback string) string {
	if resp != nil && resp.Request != nil && resp.Request.URL != nil {
		return resp.Request.URL.String()
	}
	return fallback
}

// fetchExternalPosterBytes GETs a poster from an off-server http(s)
// URL: bare request (no Plex token, ever) with a browser User-Agent,
// redirects followed by the client, 2xx required, body capped,
// bytes sniff-verified as an image. Failures name the final address
// the chain landed on, so a CDN bounce is diagnosable from the
// message alone.
func (a *Applier) fetchExternalPosterBytes(ctx context.Context, url string) ([]byte, string, error) {
	client := a.posterHTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	reqCtx, cancel := context.WithTimeout(ctx, externalPosterTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", &Error{Message: fmt.Sprintf("Poster download from %s could not be started (%s)", url, errMsg(err)), Hint: "The item will not be touched."}
	}
	req.Header.Set("User-Agent", externalPosterUserAgent)
	req.Header.Set("Accept", "image/*,*/*")
	resp, err := client.Do(req)
	if err != nil {
		// A redirect chain that never lands (a loop, or over the
		// client's follow limit) returns the LAST response alongside
		// the error: its status and address name the bounce.
		if resp != nil {
			final := finalPosterURL(resp, url)
			status := resp.StatusCode
			resp.Body.Close()
			return nil, "", &Error{Message: fmt.Sprintf("Poster download from %s returned HTTP %d (final address %s)", url, status, final), Hint: "The item will not be touched."}
		}
		return nil, "", &Error{Message: fmt.Sprintf("Poster download from %s failed (%s)", url, errMsg(err)), Hint: "The item will not be touched."}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := fmt.Sprintf("Poster download from %s returned HTTP %d", url, resp.StatusCode)
		if final := finalPosterURL(resp, url); final != url {
			msg += fmt.Sprintf(" (final address %s)", final)
		}
		return nil, "", &Error{Message: msg, Hint: "The item will not be touched."}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxEntryPosterBytes+1))
	if err != nil {
		return nil, "", &Error{Message: fmt.Sprintf("Poster download from %s failed while reading (%s)", url, errMsg(err)), Hint: "The item will not be touched."}
	}
	if len(body) > maxEntryPosterBytes {
		return nil, "", &Error{Message: fmt.Sprintf("Poster download from %s is too large (over %d bytes)", url, maxEntryPosterBytes), Hint: "The item will not be touched."}
	}
	ok, typ := IsImageBytes(body)
	if !ok {
		return nil, "", &Error{Message: fmt.Sprintf("Poster download from %s returned %d bytes that are not a supported image (PNG/JPEG/WebP/GIF/AVIF)", url, len(body)), Hint: "The item will not be touched."}
	}
	ct := strings.Split(resp.Header.Get("Content-Type"), ";")[0]
	if strings.TrimSpace(ct) == "" {
		ct = contentTypeForImageType(typ)
	}
	return body, ct, nil
}

var (
	ratingKeyRe = regexp.MustCompile(`^\d+$`)
	machineRe   = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
)

type sidecar struct {
	Version     int             `json:"version"`
	RatingKey   string          `json:"ratingKey"`
	Title       string          `json:"title"`
	BackedUpAt  string          `json:"backedUpAt"`
	SourceURL   string          `json:"sourceUrl"`
	ContentType string          `json:"contentType"`
	PosterHash  string          `json:"posterHash"`
	BadgeSet    json.RawMessage `json:"badgeSet"`
	RunID       string          `json:"runId"`
}

func extForContentType(ct string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0])) {
	case "image/png":
		return "png"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	case "image/avif":
		return "avif"
	default:
		return "jpg"
	}
}

// VerifyBackup reports whether a verified backup exists for the item:
// sidecar present, image bytes are a real supported image, and their SHA-256
// matches the sidecar. JS verifyBackup — main wires this as the Plex
// client's PosterBackupVerifier so even direct poster writes are gated.
func (a *Applier) VerifyBackup(server *plex.Server, ratingKey string) bool {
	if server == nil {
		return false
	}
	rk := ratingKey
	if !ratingKeyRe.MatchString(rk) || !machineRe.MatchString(server.Machine) {
		return false
	}
	dir, _, sidecarPath := BackupPaths(a.userDataDir, server.Machine, rk, "")
	raw, err := os.ReadFile(sidecarPath)
	if err != nil {
		return false
	}
	var sc sidecar
	if err := json.Unmarshal(raw, &sc); err != nil {
		return false
	}
	image := filepath.Join(dir, rk+"."+extForContentType(sc.ContentType))
	bytes, err := os.ReadFile(image)
	if err != nil {
		return false
	}
	ok, _ := IsImageBytes(bytes)
	return ok && SHA256Hex(bytes) == sc.PosterHash
}

type backupResult struct {
	BackupPath  string
	Hash        string
	ContentType string
	Reused      bool
}

// backupPoster is THE GATE. Returns the backup info or an error — the
// caller records it and moves on; the item is never uploaded without it.
func (a *Applier) backupPoster(ctx context.Context, server *plex.Server, job Job, badgeSet json.RawMessage, runID string) (*backupResult, error) {
	rk := job.RatingKey
	bytes, contentType, err := a.fetchPosterBytes(ctx, server, job.Thumb)
	if err != nil {
		return nil, err
	}
	ok, typ := IsImageBytes(bytes)
	if !ok {
		title := job.Title
		if title == "" {
			title = rk
		}
		displayCT := strings.TrimSpace(contentType)
		if displayCT == "" {
			displayCT = "unknown"
		}
		return nil, &Error{
			Message:   fmt.Sprintf("Backup verification failed for %q: downloaded %d bytes (content-type: %s, first bytes: %s) but they are not a supported image (PNG/JPEG/WebP/GIF/AVIF).", title, len(bytes), displayCT, firstBytesHex(bytes)),
			RatingKey: rk,
			Hint:      "The item was NOT modified. Check the server connection and try again.",
		}
	}
	ext := extForImageType(typ)
	sidecarContentType := storedContentType(contentType, typ)
	dir, imagePath, sidecarPath := BackupPaths(a.userDataDir, server.Machine, rk, ext)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// Write-once: never overwrite an existing backup — the FIRST backup is
	// the true original. Later runs reference the same file.
	if _, err := os.Stat(sidecarPath); err == nil {
		raw, err := os.ReadFile(sidecarPath)
		if err != nil {
			return nil, err
		}
		var existing sidecar
		if err := json.Unmarshal(raw, &existing); err != nil {
			return nil, err
		}
		original := filepath.Join(dir, rk+"."+extForContentType(existing.ContentType))
		originalBytes, err := os.ReadFile(original)
		if err != nil {
			return nil, err
		}
		bok, _ := IsImageBytes(originalBytes)
		if !bok || SHA256Hex(originalBytes) != existing.PosterHash {
			return nil, &Error{Message: "Existing original backup failed integrity verification; item was not modified", RatingKey: rk}
		}
		return &backupResult{BackupPath: original, Hash: existing.PosterHash, ContentType: existing.ContentType, Reused: true}, nil
	}
	if _, err := os.Stat(imagePath); err == nil {
		return nil, &Error{Message: "Backup image has no sidecar; refusing to overwrite it", RatingKey: rk}
	}
	hash := SHA256Hex(bytes)
	sc := SidecarFor(rk, job.Title, job.Thumb, badgeSet, hash, sidecarContentType, runID)
	scJSON, _ := json.MarshalIndent(sc, "", "  ")
	tmpImg := imagePath + ".tmp"
	tmpSide := sidecarPath + ".tmp"
	if err := os.WriteFile(tmpImg, bytes, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(tmpSide, scJSON, 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpImg, imagePath); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpSide, sidecarPath); err != nil {
		return nil, err
	}
	return &backupResult{BackupPath: imagePath, Hash: hash, ContentType: sidecarContentType, Reused: false}, nil
}

// uploadComposite uploads composite bytes. Primary: multipart
// (UNVERIFIED live). Fallback: if Plex rejects the form (404/405) AND the
// server is local, serve the PNG from a one-shot temp HTTP server and use
// the ?url= form (the server can reach 127.0.0.1 only when PMS runs on
// this machine).
func (a *Applier) uploadComposite(ctx context.Context, server *plex.Server, ratingKey string, png []byte) (string, error) {
	if a.client == nil {
		return "", &Error{Message: "No Plex client configured", RatingKey: ratingKey}
	}
	err := a.client.UploadPosterBytes(ctx, server, ratingKey, png)
	if err == nil {
		return "multipart", nil
	}
	status := statusFromError(err)
	local := server != nil && server.BestConnection.Quality == "local"
	if (status == 404 || status == 405) && local {
		return a.uploadViaTempURL(ctx, server, ratingKey, png, err)
	}
	return "", err // honest failure — surfaced verbatim to the run log
}

// listenLoopback is the Go equivalent of JS srv.listen(0, '127.0.0.1').
func listenLoopback() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }

func (a *Applier) uploadViaTempURL(ctx context.Context, server *plex.Server, ratingKey string, png []byte, primaryErr error) (string, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", fmt.Sprint(len(png)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(png)
	})
	srv := &http.Server{Handler: mux}
	ln, err := listenLoopback()
	if err != nil {
		return "", err
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	port := ln.Addr().String()
	// Extract the port from 127.0.0.1:PORT.
	if i := strings.LastIndex(port, ":"); i >= 0 {
		port = port[i+1:]
	}
	url := fmt.Sprintf("http://127.0.0.1:%s/poster-%s.png", port, ratingKey)
	if err := a.client.SetPosterFromURL(ctx, server, ratingKey, url); err != nil {
		return "", &Error{
			Message:   fmt.Sprintf("Poster upload failed (multipart: %s; temp-URL fallback: %s)", errMsg(primaryErr), errMsg(err)),
			RatingKey: ratingKey,
			Hint:      "Neither upload form worked against this server. The original poster is untouched (backup intact).",
		}
	}
	return "temp-url-fallback", nil
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// Job is one composited poster to apply (JS jobs[] entries).
// Level/ParentTitle ride along for sub-item targets (v1.9 season
// overlays, v1.10 episodes): Level is "season" or "episode" and
// ParentTitle the parent's title (the show for a season; the season
// for an episode), so the run record can identify the item as a
// season of its show. GrandparentTitle names the show for episode
// targets. All three are empty for top-level items and never affect
// the safety flow — the gate and the upload key on RatingKey alone.
type Job struct {
	RatingKey        string `json:"ratingKey"`
	Title            string `json:"title"`
	Thumb            string `json:"thumb"`
	PNGBase64        string `json:"pngBase64"`
	Level            string `json:"level,omitempty"`
	ParentTitle      string `json:"parentTitle,omitempty"`
	GrandparentTitle string `json:"grandparentTitle,omitempty"`
}

// ApplyRequest is one runApplyJob call.
//
// The streaming form (RunPhase set) lets the client submit one user
// action as several small batches under ONE run record: "start"
// creates the record, "append" adds batch results, "finish" closes it
// (and may carry the final batch plus the run's non-upload outcomes).
// RunID is then client-chosen and must match clientRunIDRe. With
// RunPhase empty the call is the legacy single-shot form: the server
// generates the run id and writes the record once, exactly as before.
type ApplyRequest struct {
	Server     *plex.Server
	Jobs       []Job
	BadgeSet   json.RawMessage
	Target     json.RawMessage
	OnProgress func(Progress)

	RunID     string    // streaming only: the shared run id
	RunPhase  string    // "", "start", "append", or "finish"
	Cancelled bool      // finish only: the user cancelled between batches
	Outcomes  []RunItem // result-only entries (no upload): no-overlay, unavailable, restore-default results
}

// Progress is one per-item progress event (JS onProgress payload).
type Progress struct {
	Done      int    `json:"done"`
	Total     int    `json:"total"`
	RatingKey string `json:"ratingKey"`
	Title     string `json:"title"`
	OK        bool   `json:"ok"`
}

// RunItem is one item in a run record (JS record.items[] entries).
// Outcome names HOW the item ended, when the caller knows it:
// "applied" / "restored" (a poster write happened, OK), "failed"
// (attempted, with Error saying why), "no-overlay" (judged, earned
// nothing — not a failure), "already-default" / "no-provider"
// (restore-default verdicts — not failures), "unavailable" (facts
// could not be fetched — not a failure), "cancelled" (never attempted
// because the user cancelled). Legacy single-shot records leave it
// empty; OK + Error carry the whole story there.
type RunItem struct {
	RatingKey        string `json:"ratingKey"`
	Title            string `json:"title"`
	BackupPath       string `json:"backupPath"`
	OK               bool   `json:"ok"`
	Error            string `json:"error"`
	Via              string `json:"via,omitempty"`
	Outcome          string `json:"outcome,omitempty"`
	Level            string `json:"level,omitempty"`            // "season"/"episode" for sub-item targets (see Job)
	ParentTitle      string `json:"parentTitle,omitempty"`      // the parent's title, for sub-item targets
	GrandparentTitle string `json:"grandparentTitle,omitempty"` // the show's title, for episode targets
}

// RunRecord is the exact run-record shape from apply.js, also written to
// overlay-runs/{runId}.json. The streaming fields (Status, Cancelled,
// UpdatedAt, the extra counts) are omitempty so legacy records keep
// their exact shape.
type RunRecord struct {
	RunID     string          `json:"runId"`
	Machine   string          `json:"machine"`
	Timestamp string          `json:"timestamp"`
	Version   string          `json:"version"`
	Target    json.RawMessage `json:"target"`
	BadgeSet  json.RawMessage `json:"badgeSet"`
	Items     []RunItem       `json:"items"`
	OKCount   int             `json:"okCount"`
	FailCount int             `json:"failCount"`

	Status           string `json:"status,omitempty"` // streaming: "running", then "complete" or "cancelled"
	Cancelled        bool   `json:"cancelled,omitempty"`
	UpdatedAt        string `json:"updatedAt,omitempty"`
	NoOverlayCount   int    `json:"noOverlayCount,omitempty"`
	UnavailableCount int    `json:"unavailableCount,omitempty"`
	CancelledCount   int    `json:"cancelledCount,omitempty"`
}

func newRunID(now time.Time) string {
	iso := now.UTC().Format("2006-01-02T15:04:05.000Z")
	iso = strings.NewReplacer(":", "-", ".", "-").Replace(iso)
	var rnd [3]byte
	_, _ = rand.Read(rnd[:])
	return "run-" + iso + "-" + hex.EncodeToString(rnd[:])
}

func isoNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

type appliedState struct {
	RatingKey     string          `json:"ratingKey"`
	Title         string          `json:"title"`
	CompositeHash string          `json:"compositeHash"`
	BadgeSet      json.RawMessage `json:"badgeSet"`
	RunID         string          `json:"runId"`
	AppliedAt     string          `json:"appliedAt"`
}

var clientRunIDRe = regexp.MustCompile(`^run-[A-Za-z0-9_-]+$`)

// processJob runs ONE job through the gate and the upload, returning
// its run item. Shared by the legacy single-shot run and the streaming
// batches — the safety order is identical in both.
func (a *Applier) processJob(ctx context.Context, server *plex.Server, job Job, badgeSet json.RawMessage, runID string) RunItem {
	rk := job.RatingKey
	rec := RunItem{RatingKey: rk, Title: job.Title, BackupPath: "", OK: false, Error: "", Level: job.Level, ParentTitle: job.ParentTitle, GrandparentTitle: job.GrandparentTitle}
	// GATE FIRST — backup or abort this item.
	bk, err := a.backupPoster(ctx, server, job, badgeSet, runID)
	if err != nil {
		rec.Error = errMsg(err)
		return rec
	}
	rec.BackupPath = bk.BackupPath
	png, derr := base64.StdEncoding.DecodeString(job.PNGBase64)
	if derr != nil {
		// JS Buffer.from(_, 'base64') is lenient; a decode failure
		// here means the payload was not usable base64 at all.
		png = nil
	}
	if len(png) == 0 {
		rec.Error = errMsg(&Error{Message: "Empty composite image from designer", RatingKey: rk})
		return rec
	}
	if ok, _ := IsImageBytes(png); !ok {
		rec.Error = errMsg(&Error{Message: "Designer produced invalid PNG bytes", RatingKey: rk})
		return rec
	}
	via, err := a.uploadComposite(ctx, server, rk, png)
	note := ""
	if err != nil && plex.IsUnknownOutcome(err) {
		// Unconfirmed write — never replay it. Poll the apply
		// path's acceptance (the composite is what the server now
		// serves): verified counts as applied, with the note saying
		// how that is known; anything else stays failed, with the
		// checks' observation attached as evidence.
		verified, probe := a.pollPosterLanded(ctx, server, rk, job.Thumb, "", png, false)
		if verified {
			via, err, note = "multipart", nil, applyVerifiedUnknownNote
		} else {
			err = &Error{Message: errMsg(err) + " Post-upload verification: " + probe.observation() + ".", RatingKey: rk}
		}
	}
	if err != nil {
		rec.Error = errMsg(err)
		return rec
	}
	rec.OK = true
	rec.Via = via
	rec.Error = note
	// Applied state for re-apply detection.
	sdir := stateDir(a.userDataDir, server.Machine)
	_ = os.MkdirAll(sdir, 0o755)
	st := appliedState{
		RatingKey:     rk,
		Title:         job.Title,
		CompositeHash: SHA256Hex(png),
		BadgeSet:      badgeSetSummaryRaw(badgeSet),
		RunID:         runID,
		AppliedAt:     isoNow(),
	}
	stJSON, _ := json.MarshalIndent(st, "", "  ")
	_ = os.WriteFile(filepath.Join(sdir, rk+".json"), stJSON, 0o644)
	return rec
}

func (a *Applier) saveRun(record *RunRecord) error {
	rdir := runsDir(a.userDataDir)
	if err := os.MkdirAll(rdir, 0o755); err != nil {
		return err
	}
	recJSON, _ := json.MarshalIndent(record, "", "  ")
	return os.WriteFile(filepath.Join(rdir, record.RunID+".json"), recJSON, 0o644)
}

// recomputeCounts derives every count from the item list, so a
// streaming record can never disagree with its own items. FailCount
// counts real failures only: an explicit "failed" outcome, or a legacy
// outcome-less item that is not OK. No-overlay / unavailable /
// already-default / no-provider / cancelled are NOT failures.
func recomputeCounts(record *RunRecord) {
	record.OKCount, record.FailCount = 0, 0
	record.NoOverlayCount, record.UnavailableCount, record.CancelledCount = 0, 0, 0
	for _, it := range record.Items {
		if it.OK {
			record.OKCount++
		}
		switch it.Outcome {
		case "failed":
			record.FailCount++
		case "no-overlay":
			record.NoOverlayCount++
		case "unavailable":
			record.UnavailableCount++
		case "cancelled":
			record.CancelledCount++
		case "":
			if !it.OK {
				record.FailCount++
			}
		}
	}
}

// RunApplyJob runs one apply job: for each job, GATE FIRST (backup or
// abort that item), then upload, then record applied state. Returns the
// run record (also written to disk). Per-item failures are recorded in
// the record, never returned as the job's error (JS behavior).
func (a *Applier) RunApplyJob(ctx context.Context, req ApplyRequest) (*RunRecord, error) {
	if req.RunPhase != "" {
		return a.runApplyJobPhased(ctx, req)
	}
	server := req.Server
	runID := newRunID(time.Now())
	items := []RunItem{}
	done := 0
	for _, job := range req.Jobs {
		rec := a.processJob(ctx, server, job, req.BadgeSet, runID)
		items = append(items, rec)
		done++
		if req.OnProgress != nil {
			// Progress must never break the run (JS wraps in try/catch;
			// a panicking callback is recovered the same way).
			func() {
				defer func() { _ = recover() }()
				req.OnProgress(Progress{Done: done, Total: len(req.Jobs), RatingKey: job.RatingKey, Title: job.Title, OK: rec.OK})
			}()
		}
	}
	target := req.Target
	if len(target) == 0 {
		target = json.RawMessage("{}")
	}
	machine := ""
	if server != nil {
		machine = server.Machine
	}
	record := &RunRecord{
		RunID:     runID,
		Machine:   machine,
		Timestamp: isoNow(),
		Version:   "0.3.0",
		Target:    target,
		BadgeSet:  badgeSetSummaryRaw(req.BadgeSet),
		Items:     items,
	}
	for _, it := range items {
		if it.OK {
			record.OKCount++
		} else {
			record.FailCount++
		}
	}
	if err := a.saveRun(record); err != nil {
		return nil, err
	}
	return record, nil
}

// validOutcome reports whether o is a result-only outcome a client may
// report without an upload. "applied" is deliberately NOT one: an
// applied poster must come through Jobs, where the backup gate runs.
func validOutcome(o string) bool {
	switch o {
	case "failed", "no-overlay", "unavailable", "cancelled",
		"restored", "already-default", "no-provider":
		return true
	}
	return false
}

// mergeOutcome folds one result-only outcome into the record. An item
// that already has a poster write recorded (OK, or outcome applied /
// restored) is never rewritten by an outcome — the write is the truth.
func mergeOutcome(record *RunRecord, oc RunItem) {
	for i := range record.Items {
		if record.Items[i].RatingKey != oc.RatingKey {
			continue
		}
		if record.Items[i].OK || record.Items[i].Outcome == "applied" || record.Items[i].Outcome == "restored" {
			return
		}
		record.Items[i] = oc
		return
	}
	record.Items = append(record.Items, oc)
}

// runApplyJobPhased implements the streaming form: one user action, one
// run record, several calls. "start" creates the record (and may carry
// the first batch), "append" adds batches, "finish" closes the record
// as complete or cancelled. Jobs in any phase go through the same gate
// as the legacy path; Outcomes carry the items that needed no upload.
func (a *Applier) runApplyJobPhased(ctx context.Context, req ApplyRequest) (*RunRecord, error) {
	if !clientRunIDRe.MatchString(req.RunID) {
		return nil, &Error{Message: "Invalid run id for a streaming apply", Hint: "Run ids are generated by the app; reload and try again."}
	}
	if req.RunPhase != "start" && req.RunPhase != "append" && req.RunPhase != "finish" {
		return nil, &Error{Message: "Unknown apply phase: " + req.RunPhase}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	server := req.Server
	machine := ""
	if server != nil {
		machine = server.Machine
	}
	record := a.getRun(req.RunID)
	switch req.RunPhase {
	case "start":
		if record != nil {
			return nil, &Error{Message: "A run with this id already exists", Hint: "Reload and try again."}
		}
		target := req.Target
		if len(target) == 0 {
			target = json.RawMessage("{}")
		}
		record = &RunRecord{
			RunID:     req.RunID,
			Machine:   machine,
			Timestamp: isoNow(),
			Version:   "0.3.0",
			Target:    target,
			BadgeSet:  badgeSetSummaryRaw(req.BadgeSet),
			Items:     []RunItem{},
			Status:    "running",
			UpdatedAt: isoNow(),
		}
		if err := a.saveRun(record); err != nil {
			return nil, err
		}
	default: // append / finish
		if record == nil {
			return nil, &Error{Message: "Streaming apply run not found: " + req.RunID, Hint: "The run was never started."}
		}
		if record.Machine != machine {
			return nil, &Error{Message: "This run belongs to another server. Select its original server."}
		}
		if record.Status == "complete" || record.Status == "cancelled" {
			return nil, &Error{Message: "This run is already finished", Hint: "Start a new apply for more items."}
		}
	}
	done := 0
	for _, job := range req.Jobs {
		rec := a.processJob(ctx, server, job, req.BadgeSet, req.RunID)
		if rec.OK {
			rec.Outcome = "applied"
		} else {
			rec.Outcome = "failed"
		}
		merged := false
		for i := range record.Items {
			if record.Items[i].RatingKey == rec.RatingKey {
				record.Items[i] = rec
				merged = true
				break
			}
		}
		if !merged {
			record.Items = append(record.Items, rec)
		}
		done++
		if req.OnProgress != nil {
			func() {
				defer func() { _ = recover() }()
				req.OnProgress(Progress{Done: done, Total: len(req.Jobs), RatingKey: job.RatingKey, Title: job.Title, OK: rec.OK})
			}()
		}
	}
	for _, oc := range req.Outcomes {
		if !validOutcome(oc.Outcome) {
			continue // an "applied" claim without an upload is not evidence
		}
		oc.OK = oc.Outcome == "restored"
		if oc.Outcome != "failed" && oc.Outcome != "restored" {
			// A restored item's Error slot carries its note, not a
			// failure — e.g. a write whose confirmation never
			// arrived but which post-upload verification proved
			// landed. The note is part of the item's history and
			// is kept; other non-failure outcomes carry no text.
			oc.Error = ""
		}
		oc.Via = ""
		mergeOutcome(record, oc)
	}
	if req.RunPhase == "finish" {
		if req.Cancelled {
			record.Status = "cancelled"
			record.Cancelled = true
		} else {
			record.Status = "complete"
		}
	}
	record.UpdatedAt = isoNow()
	recomputeCounts(record)
	if err := a.saveRun(record); err != nil {
		return nil, err
	}
	return record, nil
}

// RestorePoster restores one item's original poster from its backup.
// Never needs the backup re-backed-up: restore reads, it doesn't rotate.
// Returns {restored, via, backupPath} (JS shape).
func (a *Applier) RestorePoster(ctx context.Context, server *plex.Server, ratingKey string) (map[string]any, error) {
	rk := ratingKey
	dir, _, _ := BackupPaths(a.userDataDir, server.Machine, rk, "")
	sidecarPath := filepath.Join(dir, rk+".json")
	if _, err := os.Stat(sidecarPath); err != nil {
		return nil, &Error{Message: fmt.Sprintf("No backup found for item %s", rk), RatingKey: rk, Hint: "Nothing was ever backed up for this item — nothing to restore."}
	}
	raw, err := os.ReadFile(sidecarPath)
	if err != nil {
		return nil, err
	}
	var sc sidecar
	if err := json.Unmarshal(raw, &sc); err != nil {
		return nil, err
	}
	imagePath := filepath.Join(dir, rk+"."+extForContentType(sc.ContentType))
	candidates := []string{imagePath, filepath.Join(dir, rk+".png"), filepath.Join(dir, rk+".jpg"), filepath.Join(dir, rk+".webp"), filepath.Join(dir, rk+".gif"), filepath.Join(dir, rk+".avif")}
	found := ""
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			found = p
			break
		}
	}
	if found == "" {
		return nil, &Error{Message: fmt.Sprintf("Backup file missing for item %s", rk), RatingKey: rk, Hint: "The sidecar exists but the image bytes are gone."}
	}
	bytes, err := os.ReadFile(found)
	if err != nil {
		return nil, err
	}
	ok, _ := IsImageBytes(bytes)
	if !ok || SHA256Hex(bytes) != sc.PosterHash {
		return nil, &Error{Message: fmt.Sprintf("Backup integrity check failed for item %s", rk), RatingKey: rk}
	}
	via, err := a.uploadComposite(ctx, server, rk, bytes)
	if err != nil {
		return nil, err
	}
	// Absent state is already restored — ignore the error, as in JS.
	_ = os.Remove(filepath.Join(stateDir(a.userDataDir, server.Machine), rk+".json"))
	return map[string]any{"restored": true, "via": via, "backupPath": found}, nil
}

// ListRuns returns all run records, newest first (JS listRuns).
func (a *Applier) ListRuns() []RunRecord {
	dir := runsDir(a.userDataDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []RunRecord{}
	}
	out := []RunRecord{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var rec RunRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Timestamp > out[j].Timestamp })
	return out
}

func (a *Applier) getRun(runID string) *RunRecord {
	p := filepath.Join(runsDir(a.userDataDir), runID+".json")
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var rec RunRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil
	}
	return &rec
}

// RestoreRun restores every item in a run. Returns
// {runId, results, okCount} (JS shape).
func (a *Applier) RestoreRun(ctx context.Context, server *plex.Server, runID string) (map[string]any, error) {
	record := a.getRun(runID)
	if record == nil {
		return nil, &Error{Message: fmt.Sprintf("Run %s not found", runID), Hint: "It may have been deleted."}
	}
	if record.Machine != server.Machine {
		return nil, &Error{Message: "This run belongs to another server or has no server identity. Select its original server."}
	}
	results := []map[string]any{}
	okCount := 0
	for _, it := range record.Items {
		// Items the run recorded WITHOUT a poster write (no overlay
		// earned, facts unavailable, a restore-default verdict) have
		// nothing to undo — restoring them would re-upload an older
		// backup over a poster this run never touched.
		if it.Outcome != "" && it.Outcome != "applied" && it.Outcome != "restored" {
			results = append(results, map[string]any{"ratingKey": it.RatingKey, "ok": false, "skipped": true, "error": "This run did not change this poster — nothing to restore"})
			continue
		}
		r, err := a.RestorePoster(ctx, server, it.RatingKey)
		if err != nil {
			results = append(results, map[string]any{"ratingKey": it.RatingKey, "ok": false, "error": errMsg(err)})
			continue
		}
		via, _ := r["via"].(string)
		results = append(results, map[string]any{"ratingKey": it.RatingKey, "ok": true, "via": via})
		okCount++
	}
	return map[string]any{"runId": runID, "results": results, "okCount": okCount}, nil
}

// ReapplyCheck diffs each applied item's CURRENT poster hash against the
// composite hash recorded at apply time. Read-only. Returns the JS shape:
// [{ratingKey, title, needsReapply, badgeSetName?, runId?, note?}].
func (a *Applier) ReapplyCheck(ctx context.Context, server *plex.Server) ([]map[string]any, error) {
	dir := stateDir(a.userDataDir, server.Machine)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []map[string]any{}, nil
	}
	names := []string{}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := []map[string]any{}
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		var st appliedState
		if err := json.Unmarshal(raw, &st); err != nil {
			// JS parses state outside its per-item try — a corrupt state
			// file aborts the check rather than being silently skipped.
			return nil, err
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					out = append(out, map[string]any{"ratingKey": st.RatingKey, "title": st.Title, "needsReapply": false, "note": fmt.Sprint(r)})
				}
			}()
			var item *plex.Item
			if a.client != nil {
				it, ierr := a.client.GetItem(ctx, server, st.RatingKey)
				if ierr == nil {
					item = it
				}
			}
			if item == nil || item.Thumb == "" {
				out = append(out, map[string]any{"ratingKey": st.RatingKey, "title": st.Title, "needsReapply": false, "note": "item unreadable"})
				return
			}
			bytes, _, ferr := a.fetchPosterBytes(ctx, server, item.Thumb)
			if ferr != nil {
				out = append(out, map[string]any{"ratingKey": st.RatingKey, "title": st.Title, "needsReapply": false, "note": errMsg(ferr)})
				return
			}
			cur := SHA256Hex(bytes)
			badgeName := ""
			if s := badgeSetSummary(st.BadgeSet); s != nil {
				badgeName, _ = s["name"].(string)
			}
			out = append(out, map[string]any{
				"ratingKey":    st.RatingKey,
				"title":        st.Title,
				"needsReapply": cur != st.CompositeHash,
				"badgeSetName": badgeName,
				"runId":        st.RunID,
			})
		}()
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Restore default Plex posters
// ---------------------------------------------------------------------------

// restoreDefaultBadgeSet is the badge-set summary recorded on backups
// taken by the restore-default operation (it applies no badges; the
// summary exists so sidecars and run records name the operation).
var restoreDefaultBadgeSet = json.RawMessage(`{"name":"Restore default Plex posters","placements":[]}`)

// BackupCurrentPoster runs THE GATE for an item without uploading
// anything itself: download the current poster, verify it, write the
// write-once backup + sidecar. Exported for operations (like
// restore-default) whose write comes after the gate — the backup
// invariant is the same: no verified backup, no change.
func (a *Applier) BackupCurrentPoster(ctx context.Context, server *plex.Server, ratingKey, title, thumb string) (string, error) {
	bk, err := a.backupPoster(ctx, server, Job{RatingKey: ratingKey, Title: title, Thumb: thumb}, restoreDefaultBadgeSet, "")
	if err != nil {
		return "", err
	}
	return bk.BackupPath, nil
}

// ClassifyItemPosters reads an item's posters list and classifies it
// for the restore-default operation (see plex.ClassifyPosters),
// refined by two checks: refineRestoredByBytes (a selected upload
// whose bytes are already the clean poster's → already-default) and
// refineProviderSelectedWithUploads (a selected provider poster that
// coexists with any upload entry → restore, because the displayed
// image cannot be proven clean). This is the single classification
// path: the Apply view's Count and the restore run both come
// through here.
func (a *Applier) ClassifyItemPosters(ctx context.Context, server *plex.Server, ratingKey string) (*plex.PosterClassification, error) {
	_, class, err := a.classifyItemPosters(ctx, server, ratingKey)
	return class, err
}

// classifyItemPosters is ClassifyItemPosters plus the posters list
// itself, for callers (RestoreDefaultPoster) that go on to download
// the default poster's bytes and need its full entry — an entry's
// thumb address can matter as much as its key (see
// fetchEntryPosterBytes).
func (a *Applier) classifyItemPosters(ctx context.Context, server *plex.Server, ratingKey string) ([]plex.PosterEntry, *plex.PosterClassification, error) {
	if a.client == nil {
		return nil, nil, &Error{Message: "No Plex client configured", RatingKey: ratingKey}
	}
	entries, err := a.client.GetPosters(ctx, server, ratingKey)
	if err != nil {
		return nil, nil, err
	}
	class := plex.ClassifyPosters(entries)
	a.refineRestoredByBytes(ctx, server, entries, &class)
	a.refineProviderSelectedWithUploads(entries, &class)
	return entries, &class, nil
}

// defaultPosterEntry finds the posters-list entry the classification
// chose as the item's default (provider) poster — the entry whose
// key and rating key ClassifyPosters recorded.
func defaultPosterEntry(entries []plex.PosterEntry, class *plex.PosterClassification) *plex.PosterEntry {
	for i := range entries {
		if entries[i].Key == class.DefaultKey && entries[i].RatingKey == class.DefaultRatingKey {
			return &entries[i]
		}
	}
	for i := range entries {
		if entries[i].Key == class.DefaultKey || (class.DefaultRatingKey != "" && entries[i].RatingKey == class.DefaultRatingKey) {
			return &entries[i]
		}
	}
	return nil
}

// refineProviderSelectedWithUploads downgrades an "already-default"
// verdict to "restore" when the verdict rests on a provider entry
// being selected AND the posters list contains at least one upload
// entry (selected or not). The pure classification trusts the list:
// provider selected → clean. That is the owner's phrase for the
// live failure — the clean poster ends up "selected, not applied".
// A selection flip (the old selection-based restore, or a poster
// pick in another client) does not move the item's thumb address,
// and the photo transcoder caches its renditions PER SIZE at that
// unchanged address: sizes primed while the baked poster was
// displayed keep serving it indefinitely, while unprimed sizes
// generate fresh from the selected poster. So no rendition
// comparison can acquit the item — whichever size is sampled may
// be unprimed and answer "clean" while the sizes clients actually
// use stay baked (a single-size check at 240×360 did exactly that
// live, acquitting ~165 stuck shows). An upload's mere presence
// therefore means the displayed image cannot be proven clean, and
// only the upload-based restore — which changes the address and
// then byte-verifies what is served there — can settle it. Its
// cost here is a needless-but-harmless re-upload of the identical
// clean poster for the rare item whose renditions happen to be
// clean at every size.
//
// Provider-selected with NO upload entries keeps the fast path:
// nothing was ever baked onto the item, so the selection is the
// display truth. A verdict produced by refineRestoredByBytes (the
// selected entry is an UPLOAD byte-equal to the provider poster —
// exactly the state a successful restore leaves behind) is not
// touched: SelectedIsUpload excludes it, so restored items never
// loop back to "restore".
func (a *Applier) refineProviderSelectedWithUploads(entries []plex.PosterEntry, class *plex.PosterClassification) {
	if class.Status != "already-default" || class.SelectedIsUpload {
		return
	}
	for i := range entries {
		if entries[i].IsUpload() {
			class.Status = "restore"
			return
		}
	}
}

// Rendition geometry for the equality check in
// refineRestoredByBytes: large enough that two different posters
// cannot plausibly produce the same rendition, small enough that a
// rendition costs a few KB where a full poster costs hundreds.
const (
	renditionWidth  = 120
	renditionHeight = 180
)

// entryRenditionAddress returns the server-local address a small
// rendition of this entry's poster is fetched by: the entry's thumb
// when it is server-local (uploads, and provider posters Plex
// proxies), else the entry's key when that is server-local, else ""
// — the entry's poster cannot be rendered by the server (it lives
// only at an off-server address), and the caller falls back to
// comparing full bytes.
func entryRenditionAddress(entry plex.PosterEntry) string {
	if strings.HasPrefix(entry.Thumb, "/") {
		return entry.Thumb
	}
	if strings.HasPrefix(entry.Key, "/") {
		return entry.Key
	}
	return ""
}

// fetchEntryRendition fetches the entry's small transcoder
// rendition (photo/:/transcode at the fixed geometry above) through
// the connection-walking raw fetch. ok is false whenever no
// rendition can be obtained — no local address, a transport
// failure, a non-2xx status, or bytes that are not an image — and
// the caller falls back to the full-byte comparison.
func (a *Applier) fetchEntryRendition(ctx context.Context, server *plex.Server, entry plex.PosterEntry) (body []byte, ok bool) {
	addr := entryRenditionAddress(entry)
	if addr == "" || a.client == nil {
		return nil, false
	}
	if server == nil || (len(server.Connections) == 0 && server.BestConnection.URI == "") {
		return nil, false
	}
	path := fmt.Sprintf("/photo/:/transcode?width=%d&height=%d&minSize=1&upscale=1&url=%s",
		renditionWidth, renditionHeight, url.QueryEscape(addr))
	body, _, status, err := a.client.FetchServed(ctx, server, path, 30*time.Second)
	if err != nil || status < 200 || status >= 300 {
		return nil, false
	}
	if isImg, _ := IsImageBytes(body); !isImg {
		return nil, false
	}
	return body, true
}

// entriesEqualByRendition compares two posters-list entries by
// their small renditions. decided is false when either rendition
// cannot be obtained — no verdict either way, and the caller falls
// back to full bytes. Soundness: both renditions are produced by
// the same deterministic transcoder from the poster FILES
// themselves, addressed by the entries' own addresses — an entry
// address names one immutable file (an upload creates a new entry
// at a new address; nothing rewrites a file under its address), so
// the per-size stale-rendition trap cannot apply here. That trap
// was about ITEM THUMB addresses, which stay fixed while the
// SELECTION behind them changes; entry renditions key on the file,
// not on any selection. Equal files therefore yield equal
// renditions, and different posters yield different renditions at
// this size for any realistic poster pair.
func (a *Applier) entriesEqualByRendition(ctx context.Context, server *plex.Server, x, y plex.PosterEntry) (equal, decided bool) {
	// Both entries must have a server-local rendition address
	// before any fetch: if either poster lives only off-server, the
	// pair is undecidable by rendition and the caller goes straight
	// to full bytes without a wasted rendition fetch.
	if entryRenditionAddress(x) == "" || entryRenditionAddress(y) == "" {
		return false, false
	}
	xb, ok := a.fetchEntryRendition(ctx, server, x)
	if !ok {
		return false, false
	}
	yb, ok := a.fetchEntryRendition(ctx, server, y)
	if !ok {
		return false, false
	}
	return SHA256Hex(xb) == SHA256Hex(yb), true
}

// refineRestoredByBytes upgrades a "restore" verdict to
// "already-default" when the selected upload's bytes already equal
// the provider poster's bytes: the item IS showing the clean image,
// just in uploaded form. That is exactly the state an upload-based
// restore leaves behind — without this check every restored item
// would reappear as "to restore" forever.
//
// The comparison runs on small transcoder renditions of the two
// entries (≈ a few KB each) whenever both entries have server-local
// addresses — see entriesEqualByRendition for why that is sound —
// and falls back to downloading both FULL posters only when a
// rendition cannot be obtained for either entry. (On the Count path
// the refinement runs over the whole restore candidate set; with
// full downloads that was two posters per candidate, which over a
// remote-grade connection made counting a large library crawl.)
// Any failure leaves the verdict at "restore": the run itself will
// surface the real error per item.
func (a *Applier) refineRestoredByBytes(ctx context.Context, server *plex.Server, entries []plex.PosterEntry, class *plex.PosterClassification) {
	if class.Status != "restore" || class.DefaultKey == "" {
		return
	}
	var sel *plex.PosterEntry
	for i := range entries {
		if entries[i].Selected {
			sel = &entries[i]
			break
		}
	}
	if sel == nil || sel.Key == "" {
		return
	}
	def := defaultPosterEntry(entries, class)
	if def == nil {
		return
	}
	if equal, decided := a.entriesEqualByRendition(ctx, server, *sel, *def); decided {
		if equal {
			class.Status = "already-default"
		}
		return
	}
	selBytes, _, err := a.fetchEntryPosterBytes(ctx, server, *sel)
	if err != nil {
		return
	}
	defBytes, _, err := a.fetchEntryPosterBytes(ctx, server, *def)
	if err != nil {
		return
	}
	if SHA256Hex(selBytes) == SHA256Hex(defBytes) {
		class.Status = "already-default"
	}
}

// ---------------------------------------------------------------------------
// Post-upload landing verification (verify after an unknown outcome)
// ---------------------------------------------------------------------------

// A poster upload can end UNKNOWN: the request demonstrably left
// this machine, but the confirmation never came back — on a remote
// server the upload transfer itself plus slow ingestion can outlast
// the confirmation window (three restores failed exactly that way
// live, 2026-10-23: the write was sent, the receipt wasn't). The
// write-safety rule forbids replaying such a write, so the
// ambiguity is resolved by READING: a bounded poll of the same
// acceptance checks a confirmed write passes. Verified at any poll
// → the item counts as written, and its record carries the note
// saying the confirmation came from verification, not from the
// write itself. Budget exhausted → the failure keeps the
// unknown-outcome message, extended with what the checks actually
// observed. The unknown outcome is detected via
// plex.IsUnknownOutcome — never by matching message text.

// landingState is what one acceptance probe found.
type landingState int

const (
	landingVerified landingState = iota
	landingListUnreadable
	landingNotSelected
	landingItemUnreadable
	landingNoThumb
	landingThumbUnchanged
	landingServedUnreadable
	landingServedMismatch
)

// landingProbe is one probe's outcome: the state, plus the read
// error behind the *Unreadable states.
type landingProbe struct {
	state landingState
	err   error
}

// probePosterLanded runs the post-upload acceptance checks ONCE.
// With requireSelection (restore-default) that is the full restore
// acceptance: the newly uploaded entry is now the selected one
// (selected, an upload, and not the entry that was selected before
// the write), the item's poster address moved off preThumb (an
// unmoved address means clients keyed on it keep the old image),
// and the bytes served at the new address hash-equal want.
// Without it (overlay apply) the acceptance is the apply path's
// own: the bytes served at the item's current poster address
// hash-equal the uploaded composite — the same test ReapplyCheck
// judges applied posters by.
func (a *Applier) probePosterLanded(ctx context.Context, server *plex.Server, ratingKey, preThumb, preSelectedRK string, want []byte, requireSelection bool) landingProbe {
	if requireSelection {
		entries, err := a.client.GetPosters(ctx, server, ratingKey)
		if err != nil {
			return landingProbe{state: landingListUnreadable, err: err}
		}
		after := plex.ClassifyPosters(entries)
		if !after.SelectedIsUpload || after.SelectedRatingKey == "" || after.SelectedRatingKey == preSelectedRK {
			return landingProbe{state: landingNotSelected}
		}
	}
	item, err := a.client.GetItem(ctx, server, ratingKey)
	if err != nil {
		return landingProbe{state: landingItemUnreadable, err: err}
	}
	if item == nil || item.Thumb == "" {
		return landingProbe{state: landingNoThumb}
	}
	if requireSelection && item.Thumb == preThumb {
		return landingProbe{state: landingThumbUnchanged}
	}
	served, _, ferr := a.fetchPosterBytes(ctx, server, item.Thumb)
	if ferr != nil {
		return landingProbe{state: landingServedUnreadable, err: ferr}
	}
	if SHA256Hex(served) != SHA256Hex(want) {
		return landingProbe{state: landingServedMismatch}
	}
	return landingProbe{state: landingVerified}
}

// restoreFailureMessage is the confirmed-write failure text for a
// probe state — the exact wording the restore verification has
// always produced, so a confirmed write that fails acceptance
// reads exactly as it did before the probe was factored out.
func (p landingProbe) restoreFailureMessage() string {
	switch p.state {
	case landingListUnreadable:
		return "The clean poster was uploaded but could not be verified: " + errMsg(p.err)
	case landingNotSelected:
		return "Plex did not select the uploaded clean poster"
	case landingItemUnreadable:
		return "The clean poster was uploaded but the item could not be re-read: " + errMsg(p.err)
	case landingNoThumb:
		return "The clean poster was uploaded but the item reports no poster address"
	case landingThumbUnchanged:
		return "Plex did not refresh the poster address"
	case landingServedUnreadable:
		return "The clean poster was uploaded but the served image could not be verified: " + errMsg(p.err)
	default: // landingServedMismatch
		return "Plex selected the clean poster but still serves the old image bytes"
	}
}

// observation phrases a probe state for the unknown-write failure:
// what the post-upload checks actually saw, so the failure is
// evidence instead of a shrug.
func (p landingProbe) observation() string {
	switch p.state {
	case landingListUnreadable:
		return "the posters list could not be re-read during the checks (" + errMsg(p.err) + ")"
	case landingNotSelected:
		return "the uploaded poster is not the selected one"
	case landingItemUnreadable:
		return "the item could not be re-read during the checks (" + errMsg(p.err) + ")"
	case landingNoThumb:
		return "the item reports no poster address"
	case landingThumbUnchanged:
		return "the poster address did not change"
	case landingServedUnreadable:
		return "the served image could not be fetched during the checks (" + errMsg(p.err) + ")"
	case landingServedMismatch:
		return "the served image still differs from the uploaded poster"
	default:
		return "the upload could not be confirmed"
	}
}

// Poll cadence for verify-after-unknown: a probe every ~2.5s for
// up to ~30s. The write in question was sent seconds ago; a server
// that is going to show it does so quickly, and thirty seconds of
// checking is already generous next to the item's other steps.
const (
	defaultLandingPollInterval = 2500 * time.Millisecond
	defaultLandingPollBudget   = 30 * time.Second
)

func (a *Applier) landingInterval() time.Duration {
	if a.landingPollInterval > 0 {
		return a.landingPollInterval
	}
	return defaultLandingPollInterval
}

func (a *Applier) landingBudget() time.Duration {
	if a.landingPollBudget > 0 {
		return a.landingPollBudget
	}
	return defaultLandingPollBudget
}

// pollPosterLanded probes until the upload verifies or the budget
// runs out, returning the last probe either way. The first probe is
// immediate: in the common failure the server applied the write
// before its response was lost, so the landed state is already
// there to be seen.
func (a *Applier) pollPosterLanded(ctx context.Context, server *plex.Server, ratingKey, preThumb, preSelectedRK string, want []byte, requireSelection bool) (bool, landingProbe) {
	deadline := time.Now().Add(a.landingBudget())
	for {
		probe := a.probePosterLanded(ctx, server, ratingKey, preThumb, preSelectedRK, want, requireSelection)
		if probe.state == landingVerified {
			return true, probe
		}
		if ctx.Err() != nil || !time.Now().Add(a.landingInterval()).Before(deadline) {
			return false, probe
		}
		select {
		case <-ctx.Done():
			return false, probe
		case <-time.After(a.landingInterval()):
		}
	}
}

// The notes recorded on items whose write was unconfirmed but
// verified landed: the tally says restored / applied (that is what
// the evidence supports), and the item's record says HOW that is
// known — never presented as a normally confirmed write.
const (
	restoreVerifiedUnknownNote = "Write outcome unknown — the upload's confirmation never arrived and the write was not retried; a post-upload check verified the clean poster is selected and served, so this counts as restored."
	applyVerifiedUnknownNote   = "Write outcome unknown — the upload's confirmation never arrived and the write was not retried; a post-upload check verified the uploaded poster is the one being served, so this counts as applied."
)

// RestoreDefaultPoster puts ONE item back on its default Plex poster:
// the first provider (non-upload) poster in its posters list — the
// clean agent poster, with no overlay baked in (overlays only ever
// exist as uploads).
//
// THE MECHANISM IS AN UPLOAD, deliberately. Re-selecting the provider
// entry (the section-edit thumb form) flips the posters-list flag and
// even the raw bytes at the item's thumb path — but the thumb path
// ends in the item's updatedAt, selection does NOT bump updatedAt,
// and Plex clients fetch posters through the photo transcoder keyed
// on that unchanged URL: the transcoded rendition stays frozen at
// the old poster forever (measured end-to-end on PMS 1.43, and
// confirmed on an independent client). UPLOADING the provider
// poster's own bytes as a fresh poster bumps updatedAt → a new thumb
// URL → fresh renditions everywhere. The uploaded image is
// byte-identical to the agent's poster; nothing about the picture
// changes, only its address. The full safety order:
//
//  1. Classify (byte- and upload-presence-refined). Already showing the
//     clean image, or no provider poster → the verdict is returned
//     as the outcome; nothing is backed up or written.
//  2. Back up the CURRENT poster through the verified gate. A backup
//     failure is a failed outcome — the item is NEVER written.
//  3. Download the provider poster's bytes and upload them through
//     the same gated upload path overlay apply uses.
//  4. VERIFY, all three or the item fails: (a) the posters list now
//     shows the newly uploaded entry selected; (b) the item's thumb
//     path DIFFERS from before the run — no bump means clients would
//     keep the old image; (c) the raw bytes served at the new thumb
//     path hash-equal the provider poster's bytes. The write's
//     success alone is not proof of any of them.
//     If the upload itself ends UNKNOWN (sent, never confirmed —
//     see pollPosterLanded), the write is NOT replayed; the same
//     three checks are polled instead. Verified → restored, with
//     the returned note saying the confirmation came from the
//     checks. Unverified → failed, the unknown-outcome message
//     extended with what the checks observed.
//
// The sectionKey/itemType parameters are retained from the former
// selection mechanism for the API handler's contract; the upload
// path does not need them.
//
// Returns (outcome, backupPath, errorMessage): outcome is "restored",
// "already-default", "no-provider", or "failed" (with the reason in
// errorMessage). A transport-level failure of the classification read
// itself is returned as a Go error instead.
func (a *Applier) RestoreDefaultPoster(ctx context.Context, server *plex.Server, sectionKey string, itemType int, ratingKey, title, thumb string) (string, string, string, error) {
	entries, class, err := a.classifyItemPosters(ctx, server, ratingKey)
	if err != nil {
		return "", "", "", err
	}
	if class.Status != "restore" {
		return class.Status, "", "", nil
	}
	// The address baseline comes from a fresh item read (its thumb
	// path carries the current updatedAt); the caller's thumb is the
	// fallback if that read fails.
	thumbPath := thumb
	if a.client != nil {
		if it, ierr := a.client.GetItem(ctx, server, ratingKey); ierr == nil && it != nil && it.Thumb != "" {
			thumbPath = it.Thumb
		}
	}
	backupPath, berr := a.BackupCurrentPoster(ctx, server, ratingKey, title, thumbPath)
	if berr != nil {
		return "failed", "", errMsg(berr), nil
	}
	defEntry := defaultPosterEntry(entries, class)
	if defEntry == nil {
		return "failed", backupPath, "Could not download the clean provider poster: the provider poster is missing from the item's posters list", nil
	}
	clean, _, ferr := a.fetchEntryPosterBytes(ctx, server, *defEntry)
	if ferr != nil {
		return "failed", backupPath, "Could not download the clean provider poster: " + errMsg(ferr), nil
	}
	if _, uerr := a.uploadComposite(ctx, server, ratingKey, clean); uerr != nil {
		if !plex.IsUnknownOutcome(uerr) {
			return "failed", backupPath, errMsg(uerr), nil
		}
		// Unconfirmed write — never replay it. Poll the acceptance
		// checks: verified landed counts as restored (with the note
		// saying how that is known); anything else stays failed,
		// with the checks' observation attached as evidence.
		verified, probe := a.pollPosterLanded(ctx, server, ratingKey, thumbPath, class.SelectedRatingKey, clean, true)
		if verified {
			return "restored", backupPath, restoreVerifiedUnknownNote, nil
		}
		return "failed", backupPath, errMsg(uerr) + " Post-upload verification: " + probe.observation() + ".", nil
	}
	probe := a.probePosterLanded(ctx, server, ratingKey, thumbPath, class.SelectedRatingKey, clean, true)
	if probe.state != landingVerified {
		return "failed", backupPath, probe.restoreFailureMessage(), nil
	}
	return "restored", backupPath, "", nil
}
