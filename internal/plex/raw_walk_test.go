package plex

// Connection-walk tests for the raw poster paths (FetchServed GETs and
// the UploadPosterBytes write). The shape under test is the Docker
// one from the live report: the first advertised connection is a
// black hole (here: a listener that accepts and never answers, or a
// refused port), and a later candidate actually serves. These are
// httptest/net models only — no sandbox PMS models a multi-connection
// Docker server.

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// blackhole is a TCP listener that accepts connections and reads
// whatever arrives but NEVER responds — the reachable-but-dead shape
// a blackholed address presents once the dial succeeds.
type blackhole struct {
	uri     string
	accepts atomic.Int32
	bytesIn atomic.Int64

	mu    sync.Mutex
	conns []net.Conn
	ln    net.Listener
}

type countingWriter struct{ n *atomic.Int64 }

func (w countingWriter) Write(p []byte) (int, error) {
	w.n.Add(int64(len(p)))
	return len(p), nil
}

func startBlackhole(t *testing.T) *blackhole {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("blackhole listen: %v", err)
	}
	b := &blackhole{uri: "http://" + ln.Addr().String(), ln: ln}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			b.accepts.Add(1)
			b.mu.Lock()
			b.conns = append(b.conns, conn)
			b.mu.Unlock()
			go func() { _, _ = io.Copy(countingWriter{&b.bytesIn}, conn) }()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		b.mu.Lock()
		for _, conn := range b.conns {
			_ = conn.Close()
		}
		b.mu.Unlock()
	})
	return b
}

// refusedURI returns the URI of a port that was just closed, so dials
// fail fast with connection refused — a candidate that demonstrably
// never receives a single byte.
func refusedURI(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("refused listen: %v", err)
	}
	uri := "http://" + ln.Addr().String()
	_ = ln.Close()
	return uri
}

func posterServer(t *testing.T, hits *atomic.Int32, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/jpeg; charset=binary")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func walkClient() *Client {
	c := NewClient("P", "1", "test-client")
	c.rawWalkCap = 400 * time.Millisecond
	return c
}

// The first candidate blackholes; the fetch must fall through to the
// working candidate within the walk cap (not the caller's 30s), and
// the SECOND fetch must start at the remembered winner without
// paying the blackhole again.
func TestFetchServedWalkAndCache(t *testing.T) {
	bh := startBlackhole(t)
	var hits atomic.Int32
	good := posterServer(t, &hits, "poster-bytes")
	c := walkClient()
	server := &Server{Name: "S", Machine: "m-walk", Token: "tok", Connections: []Connection{
		{URI: bh.uri}, {URI: good.URL},
	}}

	start := time.Now()
	body, ct, status, err := c.FetchServed(context.Background(), server, "/library/metadata/1/thumb/2", 30*time.Second)
	if err != nil {
		t.Fatalf("FetchServed: %v", err)
	}
	if status != 200 || string(body) != "poster-bytes" || ct != "image/jpeg" {
		t.Fatalf("FetchServed = status %d ct %q body %q", status, ct, body)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("walk took %v — the per-attempt cap did not bound the blackhole", elapsed)
	}
	if bh.accepts.Load() != 1 {
		t.Fatalf("blackhole accepts = %d, want 1", bh.accepts.Load())
	}

	if _, _, _, err := c.FetchServed(context.Background(), server, "/library/metadata/1/thumb/2", 30*time.Second); err != nil {
		t.Fatalf("FetchServed (cached winner): %v", err)
	}
	if bh.accepts.Load() != 1 {
		t.Fatalf("blackhole accepts after cached fetch = %d, want still 1 (winner must be remembered)", bh.accepts.Load())
	}
	if hits.Load() != 2 {
		t.Fatalf("working server hits = %d, want 2", hits.Load())
	}
}

// The remembered winner dies; the next fetch re-walks the list and
// lands on the remaining working candidate.
func TestFetchServedCachedWinnerDiesRewalks(t *testing.T) {
	bh := startBlackhole(t)
	var hitsA, hitsB atomic.Int32
	srvA := posterServer(t, &hitsA, "from-a")
	srvB := posterServer(t, &hitsB, "from-b")
	c := walkClient()
	server := &Server{Name: "S", Machine: "m-rewalk", Token: "tok", Connections: []Connection{
		{URI: bh.uri}, {URI: srvA.URL}, {URI: srvB.URL},
	}}

	body, _, _, err := c.FetchServed(context.Background(), server, "/library/metadata/1/thumb/2", 30*time.Second)
	if err != nil || string(body) != "from-a" {
		t.Fatalf("first FetchServed = %q, %v", body, err)
	}
	srvA.Close()

	body, _, _, err = c.FetchServed(context.Background(), server, "/library/metadata/1/thumb/2", 30*time.Second)
	if err != nil || string(body) != "from-b" {
		t.Fatalf("second FetchServed = %q, %v — the walk must re-walk past the dead winner", body, err)
	}
	if bh.accepts.Load() != 2 {
		t.Fatalf("blackhole accepts = %d, want 2 (one per walk)", bh.accepts.Load())
	}
	if hitsB.Load() != 1 {
		t.Fatalf("server B hits = %d, want 1", hitsB.Load())
	}
}

// Every candidate fails: the error names each URI that was tried.
func TestFetchServedAllFailNamesAttempts(t *testing.T) {
	bh := startBlackhole(t)
	dead := refusedURI(t)
	c := walkClient()
	server := &Server{Name: "S", Machine: "m-dead", Token: "tok", Connections: []Connection{
		{URI: dead}, {URI: bh.uri},
	}}
	// A short caller timeout keeps the test fast: the LAST candidate
	// gets the caller's full timeout by design, and here it is the
	// blackhole.
	_, _, _, err := c.FetchServed(context.Background(), server, "/library/metadata/1/thumb/2", 2*time.Second)
	if err == nil {
		t.Fatal("FetchServed succeeded against two dead connections")
	}
	perr, ok := err.(*Error)
	if !ok {
		t.Fatalf("error type = %T, want *Error (%v)", err, err)
	}
	if !strings.Contains(perr.Message, "any connection") {
		t.Fatalf("message = %q", perr.Message)
	}
	if !strings.Contains(perr.Hint, dead) || !strings.Contains(perr.Hint, bh.uri) {
		t.Fatalf("hint = %q — both tried URIs must be named", perr.Hint)
	}
}

func uploadCounter(t *testing.T, uploads *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/library/metadata/42/posters" {
			uploads.Add(1)
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func uploadClient() *Client {
	c := walkClient()
	c.PosterBackupVerifier = func(*Server, string) bool { return true }
	return c
}

var testPNG = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 1, 2, 3, 4}

// The first candidate refuses the connection (provably unsent), so
// the upload is sent on the second — exactly once.
func TestUploadPosterBytesWalksOnConnectFailure(t *testing.T) {
	dead := refusedURI(t)
	var uploads atomic.Int32
	good := uploadCounter(t, &uploads)
	c := uploadClient()
	server := &Server{Name: "S", Machine: "m-up1", Token: "tok", Connections: []Connection{
		{URI: dead}, {URI: good.URL},
	}}
	if err := c.UploadPosterBytes(context.Background(), server, "42", testPNG); err != nil {
		t.Fatalf("UploadPosterBytes: %v", err)
	}
	if uploads.Load() != 1 {
		t.Fatalf("uploads received = %d, want exactly 1", uploads.Load())
	}
}

// The first candidate accepts and reads the request but never
// answers: the request demonstrably left this machine, so the outcome
// is UNKNOWN and the upload must NOT be replayed on the second
// candidate.
func TestUploadPosterBytesPostSendFailureUnknown(t *testing.T) {
	bh := startBlackhole(t)
	var uploads atomic.Int32
	good := uploadCounter(t, &uploads)
	c := uploadClient()
	server := &Server{Name: "S", Machine: "m-up2", Token: "tok", Connections: []Connection{
		{URI: bh.uri}, {URI: good.URL},
	}}
	err := c.UploadPosterBytes(context.Background(), server, "42", testPNG)
	if err == nil || !strings.Contains(err.Error(), "Write outcome unknown") {
		t.Fatalf("UploadPosterBytes err = %v, want a write-outcome-unknown error", err)
	}
	if uploads.Load() != 0 {
		t.Fatalf("uploads received = %d, want 0 (no replay after a possible send)", uploads.Load())
	}
	if bh.bytesIn.Load() == 0 {
		t.Fatal("blackhole received no bytes — the test premise (request was sent) does not hold")
	}
}

// Every candidate refuses: the failure names what was tried and no
// upload lands anywhere.
func TestUploadPosterBytesAllConnectFailures(t *testing.T) {
	dead1 := refusedURI(t)
	dead2 := refusedURI(t)
	c := uploadClient()
	server := &Server{Name: "S", Machine: "m-up3", Token: "tok", Connections: []Connection{
		{URI: dead1}, {URI: dead2},
	}}
	err := c.UploadPosterBytes(context.Background(), server, "42", testPNG)
	if err == nil || !strings.Contains(err.Error(), "Poster upload failed") {
		t.Fatalf("UploadPosterBytes err = %v", err)
	}
	perr, ok := err.(*Error)
	if !ok {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if !strings.Contains(perr.Hint, dead1) {
		t.Fatalf("hint = %q — the earlier tried URI must be named", perr.Hint)
	}
	if !strings.Contains(perr.Error(), dead2) && !strings.Contains(perr.Hint, dead2) {
		t.Fatalf("error = %q / hint %q — the final URI must be named too", perr.Message, perr.Hint)
	}
}
