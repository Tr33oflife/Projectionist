// Package plex is a faithful Go port of the Electron app's plex-client.js —
// the ONLY module that talks to Plex. Read this first.
//
// FUTURE AI — the 30-second orientation:
//   - This package is the single choke point for every Plex API call the app
//     makes. The web frontend NEVER sees a token. If you need a new Plex
//     endpoint, ADD IT HERE as a method on Client — do not fetch Plex from
//     anywhere else.
//   - Two API surfaces are used:
//     plex.tv — identity + discovery (PIN auth, /api/v2/user,
//     /api/v2/resources). Always https://plex.tv.
//     server  — the user's own Plex Media Server, reached at one of its
//     connection URIs with X-Plex-Token: <server accessToken>.
//   - Auth pattern is copied from the proven Media Hub v3.5.9 worker:
//     PIN OAuth, then server discovery via
//     /api/v2/resources?includeHttps=1&includeRelay=1.
//
// VERIFICATION STATUS — read before trusting a write path:
// READ methods (sections, collections, items, search, filter preview) use
// long-stable Plex endpoints also exercised by the Media Hub reference
// code. Treat them as VERIFIED patterns.
// WRITE methods (CreateCollection, AddCollectionItems, RemoveCollectionItem,
// UpdateCollectionMeta, SetPosterFromURL, DeleteCollection) use
// community-documented Plex endpoints that were NOT live-tested while
// writing the JS original (no Plex server in the build sandbox). Each one is
// marked UNVERIFIED in its doc comment. They surface Plex's exact HTTP
// error to the UI so the failure can be iterated on in precisely this
// package.
// Do NOT "fix" them by guessing new endpoints — test against a real server
// or leave the flag in place.
//
// Response shape: Plex returns { MediaContainer: { Metadata: [...],
// Directory: [...], ... } } when Accept: application/json. Helpers below
// normalize the bits the UI needs.
package plex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const plexTVBase = "https://plex.tv"

// Client identity sent on every plex.tv call. Plex ties PIN approvals to the
// client identifier, so this MUST be stable across restarts for a given
// install. The app generates one and persists it in settings; it is passed in
// here. FUTURE AI: do not randomize this per launch or users will have to
// re-approve the app on plex.tv every start.
const (
	defaultProduct = "Plex Collection Manager"
	defaultVersion = "0.2.0"
)

// Error is the structured error so the UI can show WHAT failed and WHERE,
// without leaking the token. Endpoint is the path (never includes the token —
// tokens only ever travel in headers, see serverFetch).
// Port of the JS PlexError class. Cancelled marks an aborted read (server
// switch / tab change) so the UI can drop it silently instead of toasting an
// error (v0.4.1).
type Error struct {
	Message   string `json:"message"`
	Status    int    `json:"status"`
	Endpoint  string `json:"endpoint"`
	Hint      string `json:"hint"`
	Cancelled bool   `json:"cancelled"`
	// UnknownOutcome marks a WRITE whose request may have reached
	// the server but whose response was never received: the write
	// may or may not have been applied, and it must never be
	// blindly replayed. Callers detect it with IsUnknownOutcome
	// (never by matching the message text) and resolve the
	// ambiguity by READING the server state, not by writing again.
	UnknownOutcome bool `json:"unknownOutcome,omitempty"`
}

// IsUnknownOutcome reports whether err is (or wraps) an Error
// carrying the unknown-write-outcome signal.
func IsUnknownOutcome(err error) bool {
	var perr *Error
	if errors.As(err, &perr) {
		return perr.UnknownOutcome
	}
	return false
}

// Error implements the error interface, returning just the message exactly
// like the JS Error base class.
func (e *Error) Error() string { return e.Message }

// Client is the port of the JS PlexClient class.
type Client struct {
	// PosterBackupVerifier is connected by the app to the backup verifier
	// (see requirePosterBackup). Absent verifier = deny poster writes.
	PosterBackupVerifier func(server *Server, ratingKey string) bool

	product  string
	version  string
	clientID string

	// baseURL is the plex.tv base; unexported so package tests can point it
	// at an httptest fixture. Production code never changes it.
	baseURL string

	// httpClient is unexported so package tests can substitute a counting /
	// failing transport. It never follows redirects (JS redirect:'manual').
	httpClient *http.Client

	// v0.4.4 — connection affinity: machineId -> uri of the last connection
	// that actually answered. Static ranking picks a "local" address first,
	// but on the wrong network that address hangs until timeout on EVERY
	// call (the "app is slow" complaint). Once any connection proves itself,
	// it goes first for the rest of the session.
	mu       sync.Mutex
	goodConn map[string]string

	// rawWalkCap bounds ONE connection attempt while the raw poster
	// paths (FetchServed, UploadPosterBytes) walk a server's connection
	// list: a blackholed candidate (a Docker bridge address advertised
	// via plex.direct, unreachable from this machine) must cost
	// seconds, not the caller's whole timeout. Zero means
	// defaultRawWalkCap. Package tests shorten it.
	rawWalkCap time.Duration

	// posterUploadTimeout is the budget for the FINAL (serving)
	// candidate of a poster upload: the window in which the server
	// must confirm the write. Uploads cross remote links on a thin
	// uplink and a busy server ingests slowly, so this window is
	// deliberately much longer than a read timeout — a confirmation
	// that arrives late is still a confirmation, while one that
	// never arrives leaves an unknown outcome the caller resolves
	// by verification, never by replay. Zero means
	// defaultPosterUploadTimeout. Package tests shorten it.
	posterUploadTimeout time.Duration
}

// NewClient builds a Client. Empty product/version fall back to the JS
// defaults. FUTURE AI: the JS constructor throws when clientId is missing
// because a stable id is required for PIN approvals; this Go signature has
// no error return, so the caller is responsible for passing the persisted id.
func NewClient(product, version, clientID string) *Client {
	if product == "" {
		product = defaultProduct
	}
	if version == "" {
		version = defaultVersion
	}
	return &Client{
		product:  product,
		version:  version,
		clientID: clientID,
		baseURL:  plexTVBase,
		httpClient: &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		goodConn: make(map[string]string),
	}
}

// httpC returns the effective HTTP client (defensive: a zero-value Client
// still works).
func (c *Client) httpC() *http.Client {
	if c.httpClient != nil {
		return c.httpClient
	}
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// defaultHeaders builds the plex.tv headers exactly as the JS
// defaultHeaders() helper: identity fields on every call, the token only
// when one is supplied.
func (c *Client) defaultHeaders(token string) http.Header {
	h := http.Header{}
	h.Set("Accept", "application/json")
	h.Set("X-Plex-Product", c.product)
	h.Set("X-Plex-Version", c.version)
	h.Set("X-Plex-Client-Identifier", c.clientID)
	// FUTURE AI: Plex is lenient here, but platform/device fields help if
	// Plex ever rate-limits by client. Keep them truthful.
	h.Set("X-Plex-Platform", "Windows")
	h.Set("X-Plex-Device", "Windows")
	if token != "" {
		h.Set("X-Plex-Token", token)
	}
	return h
}

// cancelledErr is the single construction site for the v0.4.1 cancellation
// error: message 'Request cancelled', Cancelled:true.
func cancelledErr(endpoint string) *Error {
	return &Error{Message: "Request cancelled", Endpoint: endpoint, Cancelled: true}
}

// plexTV calls the plex.tv API (identity/discovery). Never used for library
// data. Returns the raw response body; callers decode it.
// FUTURE AI: every plex.tv call funnels through here — keep the error
// messages/hints identical to the JS, the UI matches on them.
func (c *Client) plexTV(ctx context.Context, path string, method string, token string, timeoutMs int) ([]byte, error) {
	if method == "" {
		method = "GET"
	}
	if timeoutMs <= 0 {
		timeoutMs = 15000
	}
	if ctx.Err() != nil {
		return nil, cancelledErr(path)
	}
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, method, c.baseURL+path, nil)
	if err != nil {
		return nil, &Error{Message: fmt.Sprintf("Could not reach plex.tv (%s)", err.Error()), Endpoint: path, Hint: "Check internet access. plex.tv must be reachable for sign-in and server discovery."}
	}
	req.Header = c.defaultHeaders(token)
	res, err := c.httpC().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, cancelledErr(path)
		}
		return nil, &Error{Message: fmt.Sprintf("Could not reach plex.tv (%s)", err.Error()), Endpoint: path, Hint: "Check internet access. plex.tv must be reachable for sign-in and server discovery."}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil, &Error{Message: fmt.Sprintf("plex.tv returned HTTP %d", res.StatusCode), Status: res.StatusCode, Endpoint: path}
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		if ctx.Err() != nil {
			return nil, cancelledErr(path)
		}
		return nil, &Error{Message: fmt.Sprintf("Could not reach plex.tv (%s)", err.Error()), Endpoint: path, Hint: "Check internet access. plex.tv must be reachable for sign-in and server discovery."}
	}
	return body, nil
}

type fetchOptions struct {
	method    string
	params    map[string]string
	timeoutMs int
}

// serverFetch calls the user's Plex Media Server. It tries each connection
// URI in order (local -> remote -> relay as ranked by RankConnections) and
// returns the first success. params becomes the URL query string.
//
// FUTURE AI: if you add retry/backoff, do it HERE — every server call
// funnels through this method, so one place covers the whole app.
// v0.4.1 — ctx lets the caller cancel in-flight reads (server switch /
// section change). An aborted read returns Error{Cancelled:true} so the UI
// can drop it silently; it must NOT fall through to the next connection like
// a normal network failure.
// Uncertain writes are NEVER replayed on another connection: a network-level
// failure on a non-GET throws 'Write outcome unknown' after ONE attempt.
// HTTP errors from the server are a real answer and surface verbatim.
func (c *Client) serverFetch(ctx context.Context, server *Server, path string, opts fetchOptions) ([]byte, error) {
	method := opts.method
	if method == "" {
		method = "GET"
	}
	method = strings.ToUpper(method)
	timeoutMs := opts.timeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 20000
	}
	params := opts.params
	if params == nil {
		params = map[string]string{}
	}

	policyCollectionID, err := WritePolicy(method, path, params)
	if err != nil {
		// JS: the guard's plain Error is re-thrown as a PlexError carrying
		// only its message.
		return nil, &Error{Message: err.Error()}
	}
	if method == "POST" && postersPathRe.MatchString(path) {
		parts := strings.Split(path, "/")
		ratingKey := ""
		if len(parts) > 3 {
			ratingKey = parts[3]
		}
		if err := c.requirePosterBackup(server, ratingKey); err != nil {
			return nil, err
		}
	}
	if policyCollectionID != "" {
		// The policy proved only the SHAPE of the write; before any write to
		// a /library/metadata/{id} route, prove the parent is a collection.
		check, err := c.serverFetch(ctx, server, "/library/metadata/"+policyCollectionID, fetchOptions{method: "GET"})
		if err != nil {
			return nil, err
		}
		cont := parseContainer(check)
		page := cont.metadata
		if !cont.metadataPresent {
			page = cont.directory
		}
		var itemType string
		if len(page) > 0 {
			itemType = stringField(page[0], "type")
		}
		if itemType != "collection" {
			return nil, &Error{Message: "Safety guard: collection edits cannot target a media item"}
		}
	}
	if server == nil || server.Token == "" {
		return nil, &Error{Message: "No Plex server selected", Hint: "Sign in and pick a server first."}
	}
	conns := c.rankedConnections(server)
	if len(conns) == 0 {
		return nil, &Error{Message: fmt.Sprintf("Server %q has no usable connections", server.Name), Hint: "The server may be offline or unreachable from this network."}
	}
	if ctx.Err() != nil {
		return nil, cancelledErr(path)
	}

	qs := encodeParams(params)
	full := path
	if qs != "" {
		if strings.Contains(path, "?") {
			full += "&" + qs
		} else {
			full += "?" + qs
		}
	}

	var lastErr error
	for _, conn := range conns {
		if ctx.Err() != nil {
			return nil, cancelledErr(path)
		}
		reqURL := trimOneTrailingSlash(conn.URI) + full
		reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
		req, rerr := http.NewRequestWithContext(reqCtx, method, reqURL, nil)
		if rerr != nil {
			cancel()
			if method != "GET" {
				return nil, &Error{Message: "Write outcome unknown: " + rerr.Error(), Endpoint: path, Hint: "Refresh the collection before retrying.", UnknownOutcome: true}
			}
			lastErr = rerr
			continue
		}
		// NOTE: server token travels ONLY in the header, never in the URL
		// (URLs end up in logs; headers don't).
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Plex-Token", server.Token)
		res, ferr := c.httpC().Do(req)
		if ferr != nil {
			cancel()
			if ctx.Err() != nil {
				return nil, cancelledErr(path)
			}
			// A timed-out write may already have succeeded; never replay it.
			if method != "GET" {
				return nil, &Error{Message: "Write outcome unknown: " + ferr.Error(), Endpoint: path, Hint: "Refresh the collection before retrying.", UnknownOutcome: true}
			}
			lastErr = ferr // network-level: try next connection
			continue
		}
		c.rememberGoodConn(server, conn.URI)
		body, berr := io.ReadAll(res.Body)
		status := res.StatusCode
		res.Body.Close()
		cancel()
		if berr != nil {
			if ctx.Err() != nil {
				return nil, cancelledErr(path)
			}
			if method != "GET" {
				return nil, &Error{Message: "Write outcome unknown: " + berr.Error(), Endpoint: path, Hint: "Refresh the collection before retrying.", UnknownOutcome: true}
			}
			lastErr = berr
			continue
		}
		if status < 200 || status > 299 {
			// 4xx/5xx from the server itself is a real answer — don't try the
			// next connection, surface it. Network failures (above) DO fall
			// through to the next connection.
			msg := fmt.Sprintf("Plex server returned HTTP %d", status)
			if len(body) > 0 {
				msg += ": " + truncateRunes(string(body), 200)
			}
			return nil, &Error{Message: msg, Status: status, Endpoint: path}
		}
		// Some write endpoints return empty bodies — tolerate that (JS
		// returns {}), so hand callers an empty JSON object.
		if len(bytes.TrimSpace(body)) == 0 {
			return []byte("{}"), nil
		}
		return body, nil
	}
	return nil, &Error{
		Message:  fmt.Sprintf("Could not reach %q on any connection", server.Name),
		Endpoint: path,
		Hint:     fmt.Sprintf("Tried %d connection(s). Last error: %s", len(conns), errMessage(lastErr)),
	}
}

// pagedMetadata reads all pages of a metadata/directory endpoint before the
// caller constructs anything destructive (e.g. a membership diff).
// Port of the JS pagedMetadata helper (default page size 500 there; the
// caller here always passes a size explicitly, as the JS call sites do).
func (c *Client) pagedMetadata(ctx context.Context, server *Server, endpoint string, pageSize int) ([]map[string]any, error) {
	var items []map[string]any
	offset := 0
	for {
		data, err := c.serverFetch(ctx, server, endpoint, fetchOptions{method: "GET", params: map[string]string{
			"X-Plex-Container-Start": strconv.Itoa(offset),
			"X-Plex-Container-Size":  strconv.Itoa(pageSize),
		}})
		if err != nil {
			return nil, err
		}
		cont := parseContainer(data)
		page := cont.metadata
		if !cont.metadataPresent {
			page = cont.directory
		}
		items = append(items, page...)
		offset += len(page)
		if cont.hasTotal && float64(offset) >= cont.total {
			break
		}
		if len(page) == 0 || (!cont.hasTotal && len(page) < pageSize) {
			break
		}
		if offset > 100000 {
			return nil, &Error{Message: "Plex pagination exceeded the safety limit"}
		}
	}
	return items, nil
}

// requirePosterBackup is the poster-write gate. Main connects
// PosterBackupVerifier to the backup verifier. Absent verifier = deny write.
// JS: requirePosterBackup(server, ratingKey) — also rejects non-numeric keys.
func (c *Client) requirePosterBackup(server *Server, ratingKey string) error {
	if !numericRe.MatchString(ratingKey) || c.PosterBackupVerifier == nil || !c.PosterBackupVerifier(server, ratingKey) {
		return &Error{Message: "Safety guard: a verified original backup is required before any poster write"}
	}
	return nil
}

// defaultRawWalkCap is the per-attempt cap while the raw poster paths
// walk a server's connection list (see Client.rawWalkCap).
const defaultRawWalkCap = 6 * time.Second

func (c *Client) walkCap() time.Duration {
	if c.rawWalkCap > 0 {
		return c.rawWalkCap
	}
	return defaultRawWalkCap
}

// defaultPosterUploadTimeout is the confirmation window for the
// serving candidate of a poster upload (see
// Client.posterUploadTimeout). It is NOT a read timeout: reads keep
// their existing budgets.
const defaultPosterUploadTimeout = 90 * time.Second

func (c *Client) uploadTimeout() time.Duration {
	if c.posterUploadTimeout > 0 {
		return c.posterUploadTimeout
	}
	return defaultPosterUploadTimeout
}

// rankedConnections is the order serverFetch tries a server's
// connections: RankConnections order with the session's proven
// connection (the goodConn affinity) promoted to the front. Shared by
// serverFetch and the raw poster paths so every kind of traffic
// resolves a server's addresses the same way.
func (c *Client) rankedConnections(server *Server) []Connection {
	if server == nil {
		return nil
	}
	conns := RankConnections(server.Connections)
	if server.Machine != "" {
		c.mu.Lock()
		goodURI := c.goodConn[server.Machine]
		c.mu.Unlock()
		if goodURI != "" {
			for i, conn := range conns {
				if conn.URI == goodURI && i > 0 {
					g := conns[i]
					rest := make([]Connection, 0, len(conns)-1)
					rest = append(rest, conns[:i]...)
					rest = append(rest, conns[i+1:]...)
					conns = append([]Connection{g}, rest...)
					break
				}
			}
		}
	}
	return conns
}

// rawConnections is rankedConnections plus the BestConnection fallback
// for servers carrying no connection list (older saved state, test
// fixtures) — the same fallback the /art route applies.
func (c *Client) rawConnections(server *Server) []Connection {
	conns := c.rankedConnections(server)
	if len(conns) == 0 && server != nil && server.BestConnection.URI != "" {
		return []Connection{server.BestConnection}
	}
	return conns
}

// rememberGoodConn records uri as the connection that most recently
// answered for this server (the affinity serverFetch maintains).
func (c *Client) rememberGoodConn(server *Server, uri string) {
	if server == nil || server.Machine == "" || uri == "" {
		return
	}
	c.mu.Lock()
	if c.goodConn == nil {
		c.goodConn = make(map[string]string)
	}
	c.goodConn[server.Machine] = uri
	c.mu.Unlock()
}

// FetchServed performs a raw GET (server token in the header ONLY)
// walking the server's ranked connections in order until one SERVES a
// response — the connection resolution the JSON transport (serverFetch)
// has always done, applied to the poster byte paths. It exists because
// a pinned "best" connection is not always reachable: a Docker-hosted
// server advertises its container bridge address via plex.direct, and
// that address is a black hole from any other machine. The JSON calls
// survived it by falling through the list; a pinned raw fetch burned
// its whole timeout on the dead address (live report, 2026-10-22).
//
// Semantics mirror the contracts it serves:
//   - any HTTP response is a real answer: body / content-type (';'
//     stripped) / status are returned and non-2xx does NOT throw —
//     callers judge the status, exactly like the old single-connection
//     raw fetch;
//   - network-level failures fall through to the next candidate;
//   - while candidates remain, one attempt is capped at the walk cap
//     (defaultRawWalkCap) so a blackhole costs seconds, not the whole
//     caller timeout; the last candidate gets the caller's full
//     timeout;
//   - the serving connection is remembered (goodConn affinity), so
//     the dead-address cost is paid once per session — and a
//     remembered connection that later fails simply re-walks the
//     list on the next call.
//
// When every candidate fails, the returned error names each URI and
// its failure. A single-connection server behaves exactly as the old
// pinned fetch did. GET-only; no write policy is consulted.
func (c *Client) FetchServed(ctx context.Context, s *Server, path string, timeout time.Duration) (body []byte, contentType string, status int, err error) {
	if s == nil || s.Token == "" {
		return nil, "", 0, &Error{Message: "No Plex server selected", Hint: "Sign in and pick a server first."}
	}
	conns := c.rawConnections(s)
	if len(conns) == 0 {
		return nil, "", 0, &Error{Message: "No usable connection to the Plex server", Hint: "Pick a server on the Connect tab first."}
	}
	if ctx.Err() != nil {
		return nil, "", 0, cancelledErr(path)
	}
	var tried []string
	for i, conn := range conns {
		if ctx.Err() != nil {
			return nil, "", 0, cancelledErr(path)
		}
		attemptTimeout := timeout
		if i < len(conns)-1 && (attemptTimeout <= 0 || attemptTimeout > c.walkCap()) {
			attemptTimeout = c.walkCap()
		}
		b, ct, st, aerr := c.fetchRawOnce(ctx, s, conn.URI, path, attemptTimeout)
		if aerr == nil {
			c.rememberGoodConn(s, conn.URI)
			return b, ct, st, nil
		}
		if ctx.Err() != nil {
			return nil, "", 0, cancelledErr(path)
		}
		tried = append(tried, fmt.Sprintf("%s (%s)", conn.URI, truncateRunes(errMessage(aerr), 120)))
	}
	return nil, "", 0, &Error{
		Message:  fmt.Sprintf("Could not reach %q on any connection", s.Name),
		Endpoint: path,
		Hint:     "Tried: " + strings.Join(tried, "; "),
	}
}

// fetchRawOnce is ONE raw GET attempt against an explicit connection
// URI: token only in the header, HTTP status returned rather than
// thrown, content-type stripped at ';'.
func (c *Client) fetchRawOnce(ctx context.Context, s *Server, connURI, path string, timeout time.Duration) (body []byte, contentType string, status int, err error) {
	if ctx.Err() != nil {
		return nil, "", 0, cancelledErr(path)
	}
	reqCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	req, rerr := http.NewRequestWithContext(reqCtx, "GET", trimOneTrailingSlash(connURI)+path, nil)
	if rerr != nil {
		return nil, "", 0, rerr
	}
	req.Header.Set("X-Plex-Token", s.Token)
	req.Header.Set("Accept", "image/*")
	res, ferr := c.httpC().Do(req)
	if ferr != nil {
		if ctx.Err() != nil {
			return nil, "", 0, cancelledErr(path)
		}
		return nil, "", 0, ferr
	}
	defer res.Body.Close()
	b, berr := io.ReadAll(res.Body)
	if berr != nil {
		if ctx.Err() != nil {
			return nil, "", 0, cancelledErr(path)
		}
		return nil, "", res.StatusCode, berr
	}
	ct := res.Header.Get("Content-Type")
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return b, ct, res.StatusCode, nil
}

/* -- module-level helpers (pure; no I/O) ---------------------------------- */

var (
	numericRe     = regexp.MustCompile(`^\d+$`)
	postersPathRe = regexp.MustCompile(`^/library/metadata/\d+/posters$`)
	artsPathRe    = regexp.MustCompile(`^/library/metadata/\d+/arts$`)
	uriSchemeRe   = regexp.MustCompile(`^https?://`)
	uriHostRe     = regexp.MustCompile(`^https?://([^/:]+)`)
	privateIPRe   = regexp.MustCompile(`(?i)^(10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|[0-9a-f-]+\.plex\.direct)`)
	dotLocalRe    = regexp.MustCompile(`(?i)\.local$`)
	relayRe       = regexp.MustCompile(`(?i)relay`)
)

// RankConnections ranks a server's connection URIs: local first, then
// non-relay remote, then relay. It uses the local/relay flags Plex returns
// on each connection; falls back to URI heuristics if they're absent (older
// servers).
//
// FUTURE AI: connection quality labels shown in the UI come from the WINNER
// here (see Server.BestConnection). If a wrong label is ever reported, this
// is the function to inspect.
// Port note: exactly like the JS, the returned connections are fresh values
// carrying only uri/local/relay, and only the winner (index 0) gets Quality
// and Label attached.
func RankConnections(conns []Connection) []Connection {
	scored := make([]Connection, 0, len(conns))
	for _, conn := range conns {
		uri := conn.URI
		local := conn.Local
		relay := conn.Relay
		if !conn.Local && !conn.Relay {
			// Heuristic fallback: plex.direct hostnames embedding a private
			// IP, or plain RFC1918 / .local addresses, count as local.
			host := ""
			if m := uriHostRe.FindStringSubmatch(uri); m != nil {
				host = m[1]
			}
			if privateIPRe.MatchString(host) || dotLocalRe.MatchString(host) || host == "localhost" {
				local = true
			}
			if relayRe.MatchString(uri) {
				relay = true
			}
		}
		scored = append(scored, Connection{URI: uri, Local: local, Relay: relay})
	}
	rank := func(conn Connection) int {
		if conn.Local {
			return 0
		}
		if conn.Relay {
			return 2
		}
		return 1
	}
	sorted := make([]Connection, len(scored))
	copy(sorted, scored)
	sort.SliceStable(sorted, func(i, j int) bool { return rank(sorted[i]) < rank(sorted[j]) })
	// Attach the winner's quality/label for the UI + art proxy.
	if len(sorted) > 0 {
		best := &sorted[0]
		switch {
		case best.Local:
			best.Quality = "local"
			best.Label = "Local network"
		case best.Relay:
			best.Quality = "relay"
			best.Label = "Relay (slow)"
		default:
			best.Quality = "remote"
			best.Label = "Remote access"
		}
	}
	return sorted
}

// enrichServer ranks a server's connections and attaches BestConnection.
// Called inside GetServers before returning (JS enrichServer).
func enrichServer(s *Server) {
	ranked := RankConnections(s.Connections)
	s.Connections = ranked
	if len(ranked) > 0 {
		s.BestConnection = ranked[0]
	}
}

// encodeURIComponent mirrors JS encodeURIComponent: everything except
// A-Z a-z 0-9 - _ . ! ~ * ' ( ) is percent-encoded.
func encodeURIComponent(s string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.!~*'()"
	var b strings.Builder
	for _, by := range []byte(s) {
		if strings.IndexByte(unreserved, by) >= 0 {
			b.WriteByte(by)
		} else {
			fmt.Fprintf(&b, "%%%02X", by)
		}
	}
	return b.String()
}

// encodeParams encodes query params the way JS URLSearchParams does
// (url.Values.Encode also sorts keys; parameter order is not semantic).
func encodeParams(params map[string]string) string {
	if len(params) == 0 {
		return ""
	}
	v := url.Values{}
	for k, val := range params {
		v.Set(k, val)
	}
	return v.Encode()
}

// trimOneTrailingSlash removes exactly one trailing '/' (JS .replace(/\/$/,”)).
func trimOneTrailingSlash(s string) string {
	return strings.TrimSuffix(s, "/")
}

func errMessage(err error) string {
	if err == nil {
		return "unknown"
	}
	return err.Error()
}

// truncateRunes truncates s to n runes (JS body.slice(0, 200) counts
// characters, not bytes).
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

/* -- JS-semantics helpers over decoded JSON maps --------------------------- */

// jsTruthy mirrors JS truthiness for JSON-decoded values.
func jsTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case float64:
		return t != 0 && !math.IsNaN(t)
	case string:
		return t != ""
	default:
		// Slices, maps and other objects are always truthy in JS.
		return true
	}
}

// jsNumber mirrors JS Number(v) for JSON-decoded values; ok=false means NaN.
func jsNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case nil:
		return 0, true // Number(null) === 0
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case float64:
		return t, true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, true // Number("") === 0
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// numOrZero is JS Number(v || 0) as used throughout the client: falsy
// values (and unparseable ones, which Plex never sends) collapse to 0.
func numOrZero(v any) float64 {
	if !jsTruthy(v) {
		return 0
	}
	f, ok := jsNumber(v)
	if !ok {
		return 0
	}
	return f
}

// jsString mirrors JS String(v) for the scalar JSON values Plex sends, with
// the JS `v || ”` call sites in mind: nil yields "".
func jsString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

// stringField is JS `m[key] || ”` for string-ish fields.
func stringField(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || !jsTruthy(v) {
		return ""
	}
	return jsString(v)
}

// intField is JS Number(m[key] || 0) truncated to an integer.
func intField(m map[string]any, key string) int64 {
	return int64(numOrZero(m[key]))
}

// parseContainer decodes a Plex response body into its MediaContainer
// Metadata/Directory/Hub slices plus totalSize. metadataPresent matters:
// in JS `container.Metadata || container.Directory` an EMPTY Metadata array
// is truthy, so Directory is only consulted when Metadata is absent.
type containerData struct {
	metadata        []map[string]any
	metadataPresent bool
	directory       []map[string]any
	hub             []map[string]any
	total           float64
	hasTotal        bool
}

func toMapSlice(v any) []map[string]any {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, el := range arr {
		if m, ok := el.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func parseContainer(body []byte) containerData {
	var out containerData
	var root map[string]any
	if len(body) == 0 || json.Unmarshal(body, &root) != nil {
		return out
	}
	mc, _ := root["MediaContainer"].(map[string]any)
	if mc == nil {
		return out
	}
	if v, present := mc["Metadata"]; present {
		out.metadataPresent = true
		out.metadata = toMapSlice(v)
	}
	out.directory = toMapSlice(mc["Directory"])
	out.hub = toMapSlice(mc["Hub"])
	if v, present := mc["totalSize"]; present {
		if f, ok := jsNumber(v); ok {
			out.total = f
			out.hasTotal = true
		}
	}
	return out
}

// decodeObject decodes a JSON object body (plex.tv pin/user responses).
func decodeObject(body []byte) map[string]any {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil {
		return map[string]any{}
	}
	return root
}

// normalizeItem normalizes one Plex Metadata entry to the shape the
// renderer expects. thumb is a RELATIVE path ("/library/metadata/…/thumb/…")
// — the renderer turns it into a pleximg:// URL via artSrc(); it never
// touches the network.
// FUTURE AI: this shape is a contract with the web frontend — field names
// (camelCase) and the year:null-when-missing behaviour must not drift.
func normalizeItem(m map[string]any) Item {
	title := stringField(m, "title")
	if title == "" {
		title = "Untitled"
	}
	var year *int
	if jsTruthy(m["year"]) {
		y := int(numOrZero(m["year"]))
		year = &y
	}
	return Item{
		RatingKey:  jsString(m["ratingKey"]),
		Title:      title,
		Type:       stringField(m, "type"),
		Year:       year,
		Summary:    stringField(m, "summary"),
		Thumb:      stringField(m, "thumb"),
		Duration:   int64(numOrZero(m["duration"])),
		ViewOffset: int64(numOrZero(m["viewOffset"])),
		AddedAt:    intField(m, "addedAt"),
		Key:        stringField(m, "key"),
	}
}
