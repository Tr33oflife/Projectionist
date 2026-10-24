package overlays

// Streaming (phased) run tests: the one-click apply submits batches
// under ONE client-chosen run id, and History must see exactly one
// record with every batch's items plus the result-only outcomes.

import (
	"context"
	"encoding/base64"
	"image/color"
	"strings"
	"testing"
)

func pngJob(t *testing.T, rk string) Job {
	t.Helper()
	return Job{
		RatingKey: rk, Title: "Item " + rk, Thumb: "/library/metadata/" + rk + "/thumb/1",
		PNGBase64: base64.StdEncoding.EncodeToString(makePNG(t, 4, 6, color.RGBA{10, 20, 30, 255})),
	}
}

func TestPhasedRunAppendSemantics(t *testing.T) {
	dir := t.TempDir()
	fake := &fakePlex{poster: makePNG(t, 4, 6, color.White)}
	a := newFakeApplier(dir, fake)
	srv := testServer()
	ctx := context.Background()
	const runID = "run-2026-10-22T27-00-00-000Z-abc123"

	rec, err := a.RunApplyJob(ctx, ApplyRequest{
		Server: srv, RunID: runID, RunPhase: "start",
		Jobs:   []Job{pngJob(t, "11")},
		Target: []byte(`{"type":"section","sectionKey":"1"}`),
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if rec.Status != "running" || len(rec.Items) != 1 || rec.Items[0].Outcome != "applied" || !rec.Items[0].OK {
		t.Fatalf("after start: %+v", rec)
	}

	rec, err = a.RunApplyJob(ctx, ApplyRequest{
		Server: srv, RunID: runID, RunPhase: "append",
		Jobs: []Job{pngJob(t, "12")},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if len(rec.Items) != 2 {
		t.Fatalf("after append: %d items, want 2", len(rec.Items))
	}

	rec, err = a.RunApplyJob(ctx, ApplyRequest{
		Server: srv, RunID: runID, RunPhase: "finish",
		Outcomes: []RunItem{
			{RatingKey: "13", Title: "Item 13", Outcome: "no-overlay"},
			{RatingKey: "14", Title: "Item 14", Outcome: "unavailable", Error: "metadata unavailable"},
			{RatingKey: "15", Title: "Item 15", Outcome: "failed", Error: "composite failed"},
			// An "applied" claim without an upload is not evidence and
			// must be dropped, not recorded.
			{RatingKey: "16", Title: "Item 16", Outcome: "applied"},
		},
	})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if rec.Status != "complete" || rec.Cancelled {
		t.Fatalf("finish status: %+v", rec)
	}
	if len(rec.Items) != 5 {
		t.Fatalf("final items = %d, want 5 (2 jobs + 3 outcomes; the fake applied outcome dropped)", len(rec.Items))
	}
	if rec.OKCount != 2 || rec.FailCount != 1 || rec.NoOverlayCount != 1 || rec.UnavailableCount != 1 {
		t.Fatalf("counts: ok=%d fail=%d noOverlay=%d unavailable=%d", rec.OKCount, rec.FailCount, rec.NoOverlayCount, rec.UnavailableCount)
	}
	runs := a.ListRuns()
	if len(runs) != 1 || runs[0].RunID != runID {
		t.Fatalf("ListRuns = %+v, want exactly the one shared run", runs)
	}
	// The bogus outcome must not overwrite a real applied item either.
	_, err = a.RunApplyJob(ctx, ApplyRequest{Server: srv, RunID: runID, RunPhase: "append", Jobs: []Job{pngJob(t, "17")}})
	if err == nil || !strings.Contains(err.Error(), "already finished") {
		t.Fatalf("append after finish: err = %v, want already-finished", err)
	}
}

func TestPhasedRunCancelledFlagPersisted(t *testing.T) {
	dir := t.TempDir()
	fake := &fakePlex{poster: makePNG(t, 4, 6, color.White)}
	a := newFakeApplier(dir, fake)
	srv := testServer()
	ctx := context.Background()
	const runID = "run-cancel-test-1"

	if _, err := a.RunApplyJob(ctx, ApplyRequest{Server: srv, RunID: runID, RunPhase: "start", Jobs: []Job{pngJob(t, "21")}}); err != nil {
		t.Fatalf("start: %v", err)
	}
	rec, err := a.RunApplyJob(ctx, ApplyRequest{
		Server: srv, RunID: runID, RunPhase: "finish", Cancelled: true,
		Outcomes: []RunItem{{RatingKey: "22", Title: "Item 22", Outcome: "cancelled"}},
	})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if rec.Status != "cancelled" || !rec.Cancelled {
		t.Fatalf("cancelled flags: %+v", rec)
	}
	if rec.CancelledCount != 1 || rec.OKCount != 1 {
		t.Fatalf("counts: cancelled=%d ok=%d", rec.CancelledCount, rec.OKCount)
	}
	got := a.getRun(runID)
	if got == nil || got.Status != "cancelled" || !got.Cancelled {
		t.Fatalf("persisted record: %+v", got)
	}
}

func TestPhasedRunGuards(t *testing.T) {
	dir := t.TempDir()
	fake := &fakePlex{poster: makePNG(t, 4, 6, color.White)}
	a := newFakeApplier(dir, fake)
	srv := testServer()
	ctx := context.Background()

	if _, err := a.RunApplyJob(ctx, ApplyRequest{Server: srv, RunID: "run-x", RunPhase: "append"}); err == nil {
		t.Fatal("append without start must fail")
	}
	if _, err := a.RunApplyJob(ctx, ApplyRequest{Server: srv, RunID: "../evil", RunPhase: "start"}); err == nil {
		t.Fatal("path-shaped run id must be rejected")
	}
	if _, err := a.RunApplyJob(ctx, ApplyRequest{Server: srv, RunID: "run-dup", RunPhase: "start"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := a.RunApplyJob(ctx, ApplyRequest{Server: srv, RunID: "run-dup", RunPhase: "start"}); err == nil {
		t.Fatal("duplicate start must fail")
	}
}

// A restore of a streaming run must skip the items the run never wrote
// (no-overlay / unavailable / verdicts) instead of re-uploading an
// older backup over posters the run did not touch.
func TestRestoreRunSkipsNonWrittenItems(t *testing.T) {
	dir := t.TempDir()
	fake := &fakePlex{poster: makePNG(t, 4, 6, color.White)}
	a := newFakeApplier(dir, fake)
	srv := testServer()
	ctx := context.Background()
	const runID = "run-restore-skip-1"

	if _, err := a.RunApplyJob(ctx, ApplyRequest{Server: srv, RunID: runID, RunPhase: "start", Jobs: []Job{pngJob(t, "31")}}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := a.RunApplyJob(ctx, ApplyRequest{
		Server: srv, RunID: runID, RunPhase: "finish",
		Outcomes: []RunItem{{RatingKey: "32", Title: "Item 32", Outcome: "no-overlay"}},
	}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	res, err := a.RestoreRun(ctx, srv, runID)
	if err != nil {
		t.Fatalf("RestoreRun: %v", err)
	}
	results, _ := res["results"].([]map[string]any)
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	var skipped, restored int
	for _, r := range results {
		if r["skipped"] == true {
			skipped++
			if r["ratingKey"] != "32" {
				t.Fatalf("skipped the wrong item: %+v", r)
			}
		}
		if r["ok"] == true {
			restored++
		}
	}
	if skipped != 1 || restored != 1 {
		t.Fatalf("skipped=%d restored=%d, want 1/1", skipped, restored)
	}
}
