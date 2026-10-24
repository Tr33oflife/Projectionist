package plex

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// countingTransport records every attempted request and never touches the
// network: GETs get an empty container, everything else succeeds silently.
// A non-nil tripErr makes every attempt fail at the network level.
type countingTransport struct {
	calls   atomic.Int32
	methods []string
	tripErr error
}

func (t *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	t.methods = append(t.methods, req.Method)
	if t.tripErr != nil {
		return nil, t.tripErr
	}
	return jsonResponse(req, 200, `{"MediaContainer":{}}`), nil
}

// jsonResponse builds a minimal *http.Response for a fake transport.
func jsonResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       &readCloser{strings.NewReader(body)},
		Request:    req,
	}
}

type readCloser struct{ *strings.Reader }

func (r *readCloser) Close() error { return nil }

func TestWritePolicyGetPasses(t *testing.T) {
	// GET always passes — even with odd params or a query in the path
	// (PreviewFilter builds a path containing '?').
	for _, ep := range []string{"/library/sections", "/library/sections/1/all?type=1&genre=35", "/anything%20else"} {
		id, err := WritePolicy("GET", ep, map[string]string{"whatever": "1"})
		if err != nil {
			t.Fatalf("GET %s: unexpected error %v", ep, err)
		}
		if id != "" {
			t.Fatalf("GET %s: collectionID = %q, want \"\"", ep, id)
		}
	}
}

func TestWritePolicyCreateCollection(t *testing.T) {
	allowed := map[string]string{"type": "1", "title": "T", "smart": "0", "sectionId": "1", "uri": "server://m/x"}
	id, err := WritePolicy("POST", "/library/collections", allowed)
	if err != nil {
		t.Fatalf("allowed create rejected: %v", err)
	}
	if id != "" {
		t.Fatalf("create collectionID = %q, want \"\"", id)
	}
	bad := map[string]string{"type": "1", "title": "T", "smart": "0", "sectionId": "1", "uri": "u", "evil": "1"}
	if _, err := WritePolicy("POST", "/library/collections", bad); err == nil || !strings.Contains(err.Error(), "unsupported write parameter") {
		t.Fatalf("unknown param: err = %v, want unsupported write parameter", err)
	}
}

func TestWritePolicyDeleteRules(t *testing.T) {
	// Bare DELETE on a metadata item is deletion — always blocked.
	if _, err := WritePolicy("DELETE", "/library/metadata/123", nil); err == nil || !strings.Contains(err.Error(), "deletion or unsupported") {
		t.Fatalf("bare DELETE: err = %v", err)
	}
	// The membership-edge DELETE is the ONLY allowed DELETE, and it must
	// hand back the parent collection id for the type proof.
	id, err := WritePolicy("DELETE", "/library/metadata/9/items/3", nil)
	if err != nil {
		t.Fatalf("items-edge DELETE rejected: %v", err)
	}
	if id != "9" {
		t.Fatalf("items-edge collectionID = %q, want \"9\"", id)
	}
	// DELETE on posters stays blocked.
	if _, err := WritePolicy("DELETE", "/library/metadata/123/posters", nil); err == nil || !strings.Contains(err.Error(), "deletion or unsupported") {
		t.Fatalf("posters DELETE: err = %v", err)
	}
	// DELETE on sections is unsupported.
	if _, err := WritePolicy("DELETE", "/library/sections/1", nil); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("sections DELETE: err = %v", err)
	}
}

func TestWritePolicyPutAndHubs(t *testing.T) {
	// PUT title/summary on a collection.
	id, err := WritePolicy("PUT", "/library/metadata/42", map[string]string{"title": "x", "titleSort": "y", "summary": "z"})
	if err != nil || id != "42" {
		t.Fatalf("PUT meta: id=%q err=%v", id, err)
	}
	// PUT with an unknown param is blocked.
	if _, err := WritePolicy("PUT", "/library/metadata/42", map[string]string{"title": "x", "delete": "1"}); err == nil || !strings.Contains(err.Error(), "unsupported write parameter") {
		t.Fatalf("PUT bad param: err = %v", err)
	}
	// PUT prefs / items / move.
	if _, err := WritePolicy("PUT", "/library/metadata/42/prefs", map[string]string{"collectionSort": "2"}); err != nil {
		t.Fatalf("PUT prefs: %v", err)
	}
	if _, err := WritePolicy("PUT", "/library/metadata/42/items", map[string]string{"uri": "u"}); err != nil {
		t.Fatalf("PUT items: %v", err)
	}
	if _, err := WritePolicy("PUT", "/library/metadata/42/items/7/move", map[string]string{"after": "3"}); err != nil {
		t.Fatalf("PUT move: %v", err)
	}
	if _, err := WritePolicy("PUT", "/library/metadata/42/items/7/move", nil); err != nil {
		t.Fatalf("PUT move no params: %v", err)
	}
	// Hubs manage: base and per-hub forms, allowed visibility params only.
	if _, err := WritePolicy("POST", "/hubs/sections/1/manage", map[string]string{"metadataItemId": "9", "promotedToOwnHome": "1"}); err != nil {
		t.Fatalf("POST hubs manage: %v", err)
	}
	if _, err := WritePolicy("PUT", "/hubs/sections/1/manage/custom.collection.1.9", map[string]string{"promotedToOwnHome": "1", "promotedToSharedHome": "0", "promotedToRecommended": "1"}); err != nil {
		t.Fatalf("PUT hubs manage hub: %v", err)
	}
	if _, err := WritePolicy("PUT", "/hubs/sections/1/manage/custom.collection.1.9", map[string]string{"title": "x"}); err == nil || !strings.Contains(err.Error(), "unsupported write parameter") {
		t.Fatalf("hubs bad param: err = %v", err)
	}
	// Poster POST allows only `url`.
	if _, err := WritePolicy("POST", "/library/metadata/42/posters", map[string]string{"url": "https://x/y.png"}); err != nil {
		t.Fatalf("POST posters: %v", err)
	}
	if _, err := WritePolicy("POST", "/library/metadata/42/posters", map[string]string{"file": "x"}); err == nil || !strings.Contains(err.Error(), "unsupported write parameter") {
		t.Fatalf("posters bad param: err = %v", err)
	}
}

func TestWritePolicyMalformedPath(t *testing.T) {
	for _, ep := range []string{"/library/metadata/42?title=x", "/library/metadata%2f42", "/library/collections?x=1"} {
		if _, err := WritePolicy("PUT", ep, nil); err == nil || !strings.Contains(err.Error(), "malformed write path") {
			t.Fatalf("PUT %s: err = %v, want malformed write path", ep, err)
		}
	}
}

func TestDeleteCollectionAlwaysGuardError(t *testing.T) {
	ct := &countingTransport{}
	c := NewClient("P", "1", "test-client")
	c.httpClient = &http.Client{Transport: ct}
	server := &Server{Name: "S", Machine: "m", Token: "t", Connections: []Connection{{URI: "http://fixture.invalid:32400", Local: true}}}
	for _, rk := range []string{"123", "abc"} {
		err := c.DeleteCollection(context.Background(), server, rk)
		if err == nil || err.Error() != "Safety guard: deletion is disabled in this app" {
			t.Fatalf("DeleteCollection(%q): err = %v", rk, err)
		}
	}
	if got := ct.calls.Load(); got != 0 {
		t.Fatalf("DeleteCollection made %d HTTP calls, want 0", got)
	}
}

// The reorder endpoint lives ONLY under /library/collections/{id} —
// verified against a real PMS (2026-10-20). The guard must allow exactly
// that shape (with the collection id returned for the type-proof) and
// keep blocking everything else under /library/collections/.
func TestWritePolicyCollectionsMove(t *testing.T) {
	id, err := WritePolicy("PUT", "/library/collections/42/items/7/move", map[string]string{"after": "3"})
	if err != nil {
		t.Fatalf("PUT collections move: %v", err)
	}
	if id != "42" {
		t.Fatalf("collections move must return collection id 42, got %q", id)
	}
	if _, err := WritePolicy("PUT", "/library/collections/42/items/7/move", nil); err != nil {
		t.Fatalf("PUT collections move no params: %v", err)
	}
	// Still blocked: deletes, item removal, prefs, unknown params.
	for _, tc := range []struct {
		method, path string
		params       map[string]string
	}{
		{"DELETE", "/library/collections/42", nil},
		{"DELETE", "/library/collections/42/items/7", nil},
		{"PUT", "/library/collections/42/prefs", map[string]string{"collectionSort": "2"}},
		{"PUT", "/library/collections/42/items/7/move", map[string]string{"bogus": "1"}},
		{"POST", "/library/collections/42/items", map[string]string{"uri": "x"}},
	} {
		if _, err := WritePolicy(tc.method, tc.path, tc.params); err == nil {
			t.Fatalf("WritePolicy(%s %s) must be blocked", tc.method, tc.path)
		}
	}
}
