package overlays

// episode_apply_test.go — an episode flows through the SAME gated
// apply pipeline as a top-level item (v1.10 episode overlays, plan
// phase 2): its current still is downloaded and VERIFIED as the
// backup, the composite is uploaded to the episode's own ratingKey,
// the served bytes are verified, the run record identifies the item
// fully (level "episode" + season + show), and the per-item
// restore-from-backup (the History action) returns the episode's
// original still bytes. The fake still and composite are landscape
// (16:9), like real episode art — the pipeline treats them as
// opaque bytes either way; geometry is the client's business.

import (
	"bytes"
	"context"
	"encoding/base64"
	"image/color"
	"os"
	"testing"
)

func TestEpisodeApplyEndToEndFakePlex(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	srv := testServer()

	// The episode's current still on the fake server.
	original := makePNG(t, 320, 180, color.RGBA{30, 90, 160, 255})
	fake := &fakePlex{poster: original}
	a := newFakeApplier(dir, fake)

	// The composite the client rendered for the episode (landscape),
	// and the job carrying the episode's full identity.
	composite := makePNG(t, 320, 180, color.RGBA{200, 60, 40, 255})
	job := Job{
		RatingKey: "720", Title: "The One With the Test", Thumb: "/library/metadata/720/thumb/1001",
		PNGBase64:        base64.StdEncoding.EncodeToString(composite),
		Level:            "episode",
		ParentTitle:      "Season 1",
		GrandparentTitle: "Two Season Show",
	}

	// The phased path the UI actually drives: start with the job,
	// finish with no result-only outcomes.
	const runID = "run-episode-720"
	rec, err := a.RunApplyJob(ctx, ApplyRequest{
		Server: srv, RunID: runID, RunPhase: "start",
		Jobs:   []Job{job},
		Target: []byte(`{"type":"picked"}`),
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	rec, err = a.RunApplyJob(ctx, ApplyRequest{Server: srv, RunID: runID, RunPhase: "finish"})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if rec.OKCount != 1 || rec.FailCount != 0 || len(rec.Items) != 1 {
		t.Fatalf("record = %d ok / %d failed / %d items, want 1/0/1 (%+v)", rec.OKCount, rec.FailCount, len(rec.Items), rec.Items)
	}
	item := rec.Items[0]
	if !item.OK || item.Outcome != "applied" {
		t.Fatalf("episode item = %+v, want applied/ok", item)
	}
	if item.RatingKey != "720" || item.Title != "The One With the Test" {
		t.Fatalf("record item = %+v, want the episode itself", item)
	}
	if item.Level != "episode" || item.ParentTitle != "Season 1" || item.GrandparentTitle != "Two Season Show" {
		t.Errorf("record item identity = level %q parent %q grandparent %q, want episode / Season 1 / Two Season Show",
			item.Level, item.ParentTitle, item.GrandparentTitle)
	}

	// The composite reached the episode's ratingKey — exactly once.
	if len(fake.uploads) != 1 || !bytes.Equal(fake.uploads[0], composite) {
		t.Fatalf("uploads = %d, want exactly the composite bytes", len(fake.uploads))
	}

	// The backup gate ran first: the episode's original still is on
	// disk (write-once verified backup + sidecar), keyed by the
	// episode's ratingKey.
	_, imgPath, sidePath := BackupPaths(dir, "machine123", "720", "png")
	backedUp, err := os.ReadFile(imgPath)
	if err != nil {
		t.Fatalf("read episode backup: %v", err)
	}
	if !bytes.Equal(backedUp, original) {
		t.Fatal("backup must hold the episode's ORIGINAL still bytes")
	}
	if _, err := os.Stat(sidePath); err != nil {
		t.Fatalf("backup sidecar missing: %v", err)
	}
	if !a.VerifyBackup(srv, "720") {
		t.Fatal("VerifyBackup must be true for the episode after a successful apply")
	}

	// Per-item restore-from-backup (the History action): the
	// episode's original still bytes go back up to the episode's
	// ratingKey.
	out, err := a.RestorePoster(ctx, srv, "720")
	if err != nil {
		t.Fatalf("RestorePoster: %v", err)
	}
	if out["restored"] != true {
		t.Fatalf("restore result: %v", out)
	}
	last := fake.uploads[len(fake.uploads)-1]
	if !bytes.Equal(last, original) {
		t.Fatal("restore must upload the episode's original backup bytes")
	}
}
