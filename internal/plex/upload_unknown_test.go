package plex

// Verify-after-unknown, client side: a poster upload whose
// response never arrives must surface the TYPED unknown-outcome
// signal (IsUnknownOutcome), exactly one request must reach the
// server (never a replay), and the final candidate's confirmation
// window is the long upload budget — package tests shorten it via
// posterUploadTimeout.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// hangUploadServer models the remote shape: the POST is received
// (and, when apply is true, the body read — the server ingested the
// poster) but the response never comes back. The handler unblocks
// at test cleanup (a handler that never reads its body cannot rely
// on the server noticing the client hang up promptly).
func hangUploadServer(t *testing.T, apply bool, hits, applied *atomic.Int32) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/library/metadata/42/posters" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		if apply {
			_, _ = io.Copy(io.Discard, r.Body)
			applied.Add(1)
		}
		// Never answer.
		select {
		case <-r.Context().Done():
		case <-release:
		case <-time.After(30 * time.Second):
		}
	}))
	// LIFO: release the handler first, then close the server.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv
}

func hangUploadClient() *Client {
	c := uploadClient()
	c.posterUploadTimeout = 400 * time.Millisecond
	return c
}

func TestUploadPosterBytesAppliedButUnansweredIsTypedUnknown(t *testing.T) {
	var hits, applied atomic.Int32
	srv := hangUploadServer(t, true, &hits, &applied)
	c := hangUploadClient()
	server := &Server{Name: "S", Machine: "m-hang1", Token: "tok", Connections: []Connection{{URI: srv.URL}}}
	start := time.Now()
	err := c.UploadPosterBytes(context.Background(), server, "42", testPNG)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("UploadPosterBytes succeeded against a server that never answers")
	}
	if !IsUnknownOutcome(err) {
		t.Fatalf("err = %v (%T), want the typed unknown-outcome signal", err, err)
	}
	if !strings.Contains(err.Error(), "Write outcome unknown") || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("err = %q, want the unknown-outcome message naming the deadline", err.Error())
	}
	if hits.Load() != 1 || applied.Load() != 1 {
		t.Fatalf("hits = %d applied = %d, want exactly one request, ingested once (no replay)", hits.Load(), applied.Load())
	}
	if elapsed > 5*time.Second {
		t.Fatalf("upload took %v; the (shortened) confirmation window should bound it", elapsed)
	}
}

func TestUploadPosterBytesDroppedAndUnansweredIsTypedUnknown(t *testing.T) {
	var hits, applied atomic.Int32
	srv := hangUploadServer(t, false, &hits, &applied)
	c := hangUploadClient()
	server := &Server{Name: "S", Machine: "m-hang2", Token: "tok", Connections: []Connection{{URI: srv.URL}}}
	err := c.UploadPosterBytes(context.Background(), server, "42", testPNG)
	if err == nil || !IsUnknownOutcome(err) {
		t.Fatalf("err = %v, want the typed unknown-outcome signal", err)
	}
	if hits.Load() != 1 || applied.Load() != 0 {
		t.Fatalf("hits = %d applied = %d, want one request that was never ingested", hits.Load(), applied.Load())
	}
}

func TestUnknownOutcomeSignalSpecificity(t *testing.T) {
	// The final upload budget is 90s by default; the walk cap
	// (non-final attempts) stays the short 6s. Reads are untouched
	// by either.
	c := NewClient("P", "1", "test-client")
	if got := c.uploadTimeout(); got != 90*time.Second {
		t.Fatalf("default upload confirmation window = %v, want 90s", got)
	}
	if got := c.walkCap(); got != 6*time.Second {
		t.Fatalf("default walk cap = %v, want 6s", got)
	}
	if IsUnknownOutcome(nil) || IsUnknownOutcome(errors.New("boom")) {
		t.Fatal("IsUnknownOutcome must be false for nil and unrelated errors")
	}
	typed := &Error{Message: "Write outcome unknown: x", UnknownOutcome: true}
	if !IsUnknownOutcome(typed) || !IsUnknownOutcome(fmt.Errorf("outer: %w", typed)) {
		t.Fatal("IsUnknownOutcome must see the signal through wrapping")
	}
	// An ordinary upload failure (every connection refused) is NOT
	// an unknown outcome: nothing was ever sent.
	dead := refusedURI(t)
	server := &Server{Name: "S", Machine: "m-hang3", Token: "tok", Connections: []Connection{{URI: dead}}}
	if err := uploadClient().UploadPosterBytes(context.Background(), server, "42", testPNG); err == nil || IsUnknownOutcome(err) {
		t.Fatalf("err = %v, want a plain failure, not unknown-outcome", err)
	}
}
