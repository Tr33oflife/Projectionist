package plex

// Live-manager tests — SKIPPED unless PCM_LIVE_PLEX points at the
// throwaway sandbox PMS (see live_test.go). They pin the v1.13
// manager facts against the REAL server the fake-PMS tests imitate:
// health presence facts for the known sandbox collections (rk 4/5/6
// + the manager probe rk 79), a metadata edit round-trip on a fresh
// collection, and the backdrop bytes upload.
//
//	PCM_LIVE_PLEX=http://127.0.0.1:32400 PCM_LIVE_MACHINE=<id> \
//	  go test ./internal/plex/ -run LiveManager -v

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestLiveManagerHealthReads(t *testing.T) {
	server := liveServer(t)
	c := NewClient("PCM", "live", "live-manager")
	cols, err := c.GetCollections(context.Background(), server, "1")
	if err != nil {
		t.Fatalf("GetCollections: %v", err)
	}
	byKey := map[string]Collection{}
	for _, col := range cols {
		byKey[col.RatingKey] = col
	}
	for _, rk := range []string{"4", "5", "6"} {
		if _, ok := byKey[rk]; !ok {
			t.Fatalf("sandbox collection rk%s missing from the health read (have %d collections)", rk, len(cols))
		}
	}
	// rk5/rk6 never had a custom poster uploaded: their thumbs are
	// Plex composites, which the health read must call NO poster.
	for _, rk := range []string{"5", "6"} {
		if byKey[rk].HasPoster {
			t.Errorf("rk%s HasPoster = true, want false (composite thumb)", rk)
		}
	}
	// rk79 (the manager probe) carries a custom poster AND backdrop.
	if p, ok := byKey["79"]; ok {
		if !p.HasPoster || !p.HasBackdrop {
			t.Errorf("rk79 HasPoster=%v HasBackdrop=%v, want true/true", p.HasPoster, p.HasBackdrop)
		}
	}
	// PMS 1.43 retired the manage endpoint: visibility is unreported.
	if byKey["5"].VisibilityKnown {
		t.Errorf("VisibilityKnown = true on PMS 1.43, want false (manage endpoint 404s)")
	}
	// The sandbox holds several same-title live-test collections:
	// every one of them must carry DUPLICATE TITLE.
	counts := TitleCounts(cols)
	dups := 0
	for _, col := range cols {
		for _, f := range CollectionFlags(col, counts) {
			if f == HealthDuplicateName {
				dups++
			}
		}
	}
	if dups < 2 {
		t.Errorf("duplicate-title flags = %d, want >= 2 (the sandbox has repeated live-test titles)", dups)
	}
	t.Logf("health read: %d collections, %d duplicate-flagged", len(cols), dups)
}

func TestLiveManagerEditRoundTrip(t *testing.T) {
	server := liveServer(t)
	c := NewClient("PCM", "live", "live-manager")
	ctx := context.Background()
	title := fmt.Sprintf("PCM Manager Live %d", time.Now().Unix())
	raw, err := c.CreateCollection(ctx, server, CreatePayload{
		SectionKey: "1", Type: 1, Title: title, ItemRatingKeys: []string{"1"},
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	var created struct {
		RatingKey string `json:"ratingKey"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.RatingKey == "" {
		t.Fatalf("created shape: %s (%v)", raw, err)
	}
	rk := created.RatingKey
	d, err := c.GetCollectionDetail(ctx, server, rk)
	if err != nil {
		t.Fatalf("GetCollectionDetail: %v", err)
	}
	// This PMS exposes no Label array and no promotion state.
	if d.Labels != nil {
		t.Errorf("Labels = %v, want nil on PMS 1.43 (field not exposed)", *d.Labels)
	}
	if d.VisibilityKnown || d.Visibility != "unknown" {
		t.Errorf("visibility = %q known=%v, want unknown/false on PMS 1.43", d.Visibility, d.VisibilityKnown)
	}
	summary := "Live manager summary — written by the round-trip test."
	sort := "ZZ Manager Sort"
	rating := "PG"
	labels := []string{"LiveLabel"}
	if err := c.UpdateCollectionMeta(ctx, server, rk, MetaFields{
		Summary: &summary, TitleSort: &sort, ContentRating: &rating, Labels: &labels,
	}); err != nil {
		t.Fatalf("UpdateCollectionMeta: %v", err)
	}
	after, err := c.GetCollectionDetail(ctx, server, rk)
	if err != nil {
		t.Fatalf("GetCollectionDetail after edit: %v", err)
	}
	if after.Summary != summary {
		t.Errorf("summary held = %q, want %q", after.Summary, summary)
	}
	if after.TitleSort != sort {
		t.Errorf("titleSort held = %q, want %q (the .value form must stick)", after.TitleSort, sort)
	}
	if after.ContentRating != rating {
		t.Errorf("contentRating held = %q, want %q", after.ContentRating, rating)
	}
	// Labels: PMS 1.43 does not take them on collections. The honest
	// expectation is that they remain unexposed — the manager's
	// verified save reports exactly that instead of claiming success.
	if after.Labels != nil && len(*after.Labels) > 0 {
		t.Logf("note: this server DID take labels: %v", *after.Labels)
	} else {
		t.Logf("labels not taken by this server (as probed) — verified-save reports the mismatch")
	}
}

func TestLiveManagerArtUpload(t *testing.T) {
	server := liveServer(t)
	c := NewClient("PCM", "live", "live-manager")
	// The gate itself is unit-tested; here a stand-in verifier lets
	// the /arts write path itself be exercised live. (The full gate
	// runs in the API live bulk test via the real backup flow.)
	c.PosterBackupVerifier = func(s *Server, rk string) bool { return true }
	ctx := context.Background()
	before, err := c.GetCollectionDetail(ctx, server, "79")
	if err != nil {
		t.Skipf("rk79 probe collection unavailable: %v", err)
	}
	pngBytes, err := os.ReadFile("../../assets/badges/badge-4k.png")
	if err != nil {
		t.Fatalf("fixture badge: %v", err)
	}
	if err := c.UploadArtBytes(ctx, server, "79", pngBytes); err != nil {
		t.Fatalf("UploadArtBytes: %v", err)
	}
	after, err := c.GetCollectionDetail(ctx, server, "79")
	if err != nil {
		t.Fatalf("GetCollectionDetail after art upload: %v", err)
	}
	if after.Art == "" || after.Art == before.Art {
		t.Errorf("art path after upload = %q (before %q), want a changed backdrop path", after.Art, before.Art)
	}
	t.Logf("backdrop now served at %s", after.Art)
}
