package overlays

// season_apply_test.go — a season flows through the SAME gated apply
// pipeline as a top-level item (v1.9 season overlays, plan phase 1):
// its current poster is downloaded and VERIFIED as the backup, the
// composite is uploaded to the season's own ratingKey, the served
// bytes are verified, the run record identifies the item as a season
// of its show (level + parent title), and the per-item
// restore-from-backup (the History action) returns the season's
// original poster bytes.

import (
	"bytes"
	"context"
	"encoding/base64"
	"image/color"
	"os"
	"testing"
)

func TestSeasonApplyEndToEndFakePlex(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	srv := testServer()

	// The season's current poster on the fake server.
	original := makePNG(t, 60, 90, color.RGBA{30, 90, 160, 255})
	fake := &fakePlex{poster: original}
	a := newFakeApplier(dir, fake)

	// The composite the client rendered for the season, and the job
	// carrying the season's identity (level + parent show).
	composite := makePNG(t, 200, 300, color.RGBA{200, 60, 40, 255})
	job := Job{
		RatingKey: "710", Title: "Season 1", Thumb: "/library/metadata/710/thumb/1001",
		PNGBase64:   base64.StdEncoding.EncodeToString(composite),
		Level:       "season",
		ParentTitle: "Two Season Show",
	}

	// The phased path the UI actually drives: start with the job,
	// finish with no result-only outcomes.
	const runID = "run-season-710"
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
		t.Fatalf("season item = %+v, want applied/ok", item)
	}
	if item.RatingKey != "710" || item.Title != "Season 1" {
		t.Fatalf("record item = %+v, want the season itself", item)
	}
	if item.Level != "season" || item.ParentTitle != "Two Season Show" {
		t.Errorf("record item identity = level %q parent %q, want season / Two Season Show", item.Level, item.ParentTitle)
	}

	// The composite reached the season's ratingKey — exactly once.
	if len(fake.uploads) != 1 || !bytes.Equal(fake.uploads[0], composite) {
		t.Fatalf("uploads = %d, want exactly the composite bytes", len(fake.uploads))
	}

	// The backup gate ran first: the season's original poster is on
	// disk (write-once verified backup + sidecar), keyed by the
	// season's ratingKey.
	_, imgPath, sidePath := BackupPaths(dir, "machine123", "710", "png")
	backedUp, err := os.ReadFile(imgPath)
	if err != nil {
		t.Fatalf("read season backup: %v", err)
	}
	if !bytes.Equal(backedUp, original) {
		t.Fatal("backup must hold the season's ORIGINAL poster bytes")
	}
	if _, err := os.Stat(sidePath); err != nil {
		t.Fatalf("backup sidecar missing: %v", err)
	}
	if !a.VerifyBackup(srv, "710") {
		t.Fatal("VerifyBackup must be true for the season after a successful apply")
	}

	// Per-item restore-from-backup (the History action): the season's
	// original bytes go back up to the season's ratingKey.
	out, err := a.RestorePoster(ctx, srv, "710")
	if err != nil {
		t.Fatalf("RestorePoster: %v", err)
	}
	if out["restored"] != true {
		t.Fatalf("restore result: %v", out)
	}
	last := fake.uploads[len(fake.uploads)-1]
	if !bytes.Equal(last, original) {
		t.Fatal("restore must upload the season's original backup bytes")
	}
}
