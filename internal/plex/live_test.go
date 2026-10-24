// Live-PMS integration test — FUTURE-AI: this test is SKIPPED unless
// PCM_LIVE_PLEX points at a THROWAWAY Plex Media Server (e.g. the sandbox
// PMS stood up 2026-10-20 for exactly this purpose). It exists because
// the owner (correctly) does not want write paths debugged against his real
// server: collection create + ordering 404'd live for him while every
// mocked test passed. Run with:
//
//	PCM_LIVE_PLEX=http://127.0.0.1:32400 PCM_LIVE_MACHINE=<machineId> \
//	  go test ./internal/plex/ -run LivePlex -v
//
// The test creates one collection named "PCM Live Test …" on that server
// and leaves it there for inspection; delete it by hand (the client
// deliberately cannot delete).
package plex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func liveServer(t *testing.T) *Server {
	t.Helper()
	base := os.Getenv("PCM_LIVE_PLEX")
	if base == "" {
		t.Skip("PCM_LIVE_PLEX not set — live Plex test skipped")
	}
	machine := os.Getenv("PCM_LIVE_MACHINE")
	if machine == "" {
		t.Fatal("PCM_LIVE_MACHINE must be set alongside PCM_LIVE_PLEX")
	}
	return &Server{
		Name:    "LiveTest",
		Machine: machine,
		Owned:   true,
		// The client refuses an empty token; an UNCLAIMED server ignores
		// the token for local no-auth networks, so a dummy works here.
		Token: "livetest-dummy-token",
		Connections: []Connection{
			{URI: strings.TrimSuffix(base, "/"), Local: true},
		},
	}
}

func rawStatus(t *testing.T, method, url string) int {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

// findCollectionByTitle lists the sandbox section's collections via raw
// HTTP and returns the ratingKey of the one with the given title ("" if
// absent). Raw on purpose: the client cannot delete, and the partial-
// failure path returns no id.
func findCollectionByTitle(t *testing.T, base, title string) string {
	t.Helper()
	req, err := http.NewRequest("GET", base+"/library/sections/1/collections", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var doc struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey string `json:"ratingKey"`
				Title     string `json:"title"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	for _, m := range doc.MediaContainer.Metadata {
		if m.Title == title {
			return m.RatingKey
		}
	}
	return ""
}

// probeCollection runs the endpoint-shape probes against a real
// collection and logs the resulting member order via the client.
func probeCollection(t *testing.T, c *Client, ctx context.Context, s *Server, base, cid string, want []string) {
	t.Helper()
	probes := []struct{ name, url string }{
		{"move via /library/collections", fmt.Sprintf("%s/library/collections/%s/items/%s/move?after=%s", base, cid, want[3], want[0])},
		{"move via /library/metadata", fmt.Sprintf("%s/library/metadata/%s/items/%s/move?after=%s", base, cid, want[3], want[0])},
		{"prefs via /library/metadata", fmt.Sprintf("%s/library/metadata/%s/prefs?collectionSort=2", base, cid)},
		{"prefs via /library/collections", fmt.Sprintf("%s/library/collections/%s/prefs?collectionSort=2", base, cid)},
	}
	for _, p := range probes {
		t.Logf("PROBE %-34s -> HTTP %d", p.name, rawStatus(t, "PUT", p.url))
	}
	items, gerr := c.GetCollectionItems(ctx, s, cid)
	if gerr != nil {
		t.Fatalf("GetCollectionItems after create: %v", gerr)
	}
	got := []string{}
	for _, it := range items {
		got = append(got, it.RatingKey)
	}
	t.Logf("collection %s order now: %v (wanted %v)", cid, got, want)
}

// sectionMovieKeys lists a section's item ratingKeys in server order.
func sectionMovieKeys(t *testing.T, base, section string) []string {
	t.Helper()
	req, err := http.NewRequest("GET", base+"/library/sections/"+section+"/all", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var doc struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey string `json:"ratingKey"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, m := range doc.MediaContainer.Metadata {
		out = append(out, m.RatingKey)
	}
	return out
}

// TestLivePlexCollectionOrdering reproduces the owner's 2026-10-20 report:
// create succeeds, then "ordering failed: HTTP 404". It runs the client's
// real CreateCollection (which internally runs UpdateCollectionMeta and
// SetCollectionItems incl. the prefs + move calls), then probes the
// candidate move/prefs endpoints RAW so the correct shapes are on record.
func TestLivePlexCollectionOrdering(t *testing.T) {
	s := liveServer(t)
	base := s.Connections[0].URI
	c := NewClient("Plex Collection Manager", "1.0.1", "pcm-live-test")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Discover the sandbox section's movie keys at runtime — ratingKeys
	// are server-assigned and shift as collections consume IDs, so a
	// hardcoded set rots (it did: the 2026-10-22 sandbox rebuild gave
	// Delta key 9, and the stale "4" pointed at a collection, whose
	// move 404 looked exactly like the owner's original bug).
	keys := sectionMovieKeys(t, base, "1")
	if len(keys) < 4 {
		t.Fatalf("sandbox section needs >= 4 movies, found %v", keys)
	}
	keys = keys[:4]
	want := []string{keys[2], keys[0], keys[3], keys[1]} // deliberate non-default order
	raw, err := c.CreateCollection(ctx, s, CreatePayload{
		SectionKey:     "1",
		Type:           1,
		Title:          "PCM Live Test Order",
		ItemRatingKeys: want,
	})
	if raw != nil {
		t.Logf("create payload: %s", raw)
	}
	if err != nil {
		// CreateCollection returns raw=nil on partial failure, so find the
		// just-created collection by title and probe against it instead.
		t.Logf("create error: %v", err)
		cid := findCollectionByTitle(t, base, "PCM Live Test Order")
		if cid == "" {
			t.Fatalf("could not find the created collection to probe")
		}
		t.Logf("found created collection id %s — probing endpoints", cid)
		probeCollection(t, c, ctx, s, base, cid, want)
		t.Fatalf("CreateCollection error (REPRODUCES THE REPORTED FAILURE if 404): %v", err)
	}

	// Post-fix assertions: the create must have produced EXACTLY the
	// wanted order, and a follow-up SetCollectionItems re-order of the
	// existing collection must land exactly too.
	var created struct {
		RatingKey string `json:"ratingKey"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.RatingKey == "" {
		t.Fatalf("create returned no ratingKey: %s (err %v)", raw, err)
	}
	assertOrder := func(stage string, wantKeys []string) {
		items, gerr := c.GetCollectionItems(ctx, s, created.RatingKey)
		if gerr != nil {
			t.Fatalf("%s: GetCollectionItems: %v", stage, gerr)
		}
		got := []string{}
		for _, it := range items {
			got = append(got, it.RatingKey)
		}
		if strings.Join(got, ",") != strings.Join(wantKeys, ",") {
			t.Fatalf("%s: order = %v, want %v", stage, got, wantKeys)
		}
		t.Logf("%s: order verified %v", stage, got)
	}
	assertOrder("after create", want)
	reorder := []string{keys[1], keys[3], keys[0], keys[2]}
	if err := c.SetCollectionItems(ctx, s, created.RatingKey, reorder); err != nil {
		t.Fatalf("SetCollectionItems re-order: %v", err)
	}
	assertOrder("after re-order", reorder)
}
