package overlays

// Verify-after-unknown: a poster upload whose confirmation never
// arrives (the remote-server shape — the request was sent, the
// response wasn't) must NOT be replayed and must NOT be reported
// on a guess. The code polls the same acceptance checks a
// confirmed write passes: verified landed → restored / applied,
// with the note saying how that is known; never verified → the
// failure keeps the unknown-outcome message plus the checks'
// observation. The fake models both server states behind the same
// client-visible error: ingested-but-unanswered, and dropped.

import (
	"context"
	"image/color"
	"strings"
	"testing"
	"time"

	"plexcollectionmanager/internal/plex"
)

// unknownUploadError is the client-visible error of the live
// Remote-server failures: a typed unknown outcome (the only signal the
// code may key on — never the text).
func unknownUploadError() *plex.Error {
	return &plex.Error{
		Message:        `Write outcome unknown: Post "https://remote-plex.example:15175/library/metadata/7/posters": context deadline exceeded`,
		Endpoint:       "/library/metadata/7/posters",
		Hint:           "The upload may have reached the server. Check the item's poster before retrying.",
		UnknownOutcome: true,
	}
}

// fastLandingApplier shrinks the verification poll so the dropped
// case resolves in milliseconds instead of the 30s budget.
func fastLandingApplier(dir string, fake *fakePlex) *Applier {
	a := newFakeApplier(dir, fake)
	a.landingPollInterval = 5 * time.Millisecond
	a.landingPollBudget = 300 * time.Millisecond
	return a
}

// The upload landed (the server ingested the clean poster,
// selected it, moved the address) but its response never came
// back. Restore must verify that state and count the item
// restored — with exactly ONE upload received server-side.
func TestRestoreDefaultUnknownUploadVerifiedRestores(t *testing.T) {
	dir := t.TempDir()
	oldBytes := makePNG(t, 4, 6, color.White)
	cleanBytes := makePNG(t, 4, 6, color.Black)
	fake := &fakePlex{
		poster:       oldBytes,
		posters:      []plex.PosterEntry{uploadEntry(true), providerEntry(false)},
		posterBytes:  map[string][]byte{providerEntry(false).Key: cleanBytes},
		modelUploads: true,

		uploadUnknownErr:     unknownUploadError(),
		uploadUnknownApplies: true,
	}
	a := fastLandingApplier(dir, fake)
	srv := testServer()

	outcome, backupPath, errMsg, err := a.RestoreDefaultPoster(context.Background(), srv, "1", 1, "7", "Item 7", restoreThumb)
	if err != nil {
		t.Fatalf("RestoreDefaultPoster: %v", err)
	}
	if outcome != "restored" {
		t.Fatalf("outcome = %q errMsg = %q, want restored via post-upload verification", outcome, errMsg)
	}
	// The record must be honest about HOW this is known: the note
	// names the unconfirmed write and the verification — it is not
	// presented as a normally confirmed write.
	if errMsg != restoreVerifiedUnknownNote || !strings.Contains(errMsg, "Write outcome unknown") || !strings.Contains(errMsg, "post-upload check verified") {
		t.Fatalf("errMsg = %q, want the verified-after-unknown note", errMsg)
	}
	if fake.uploadAttempts != 1 || len(fake.uploads) != 1 {
		t.Fatalf("upload attempts = %d, received = %d, want exactly 1 (verify, never replay)", fake.uploadAttempts, len(fake.uploads))
	}
	if backupPath == "" || !a.VerifyBackup(srv, "7") {
		t.Fatalf("backup missing after verified restore (path %q)", backupPath)
	}
}

// The upload never landed: the same client-visible error, but the
// server state never changes. The item stays failed, the failure
// keeps the unknown-outcome message AND gains the verification's
// observation; still exactly one upload attempt.
func TestRestoreDefaultUnknownUploadDroppedStaysFailed(t *testing.T) {
	dir := t.TempDir()
	oldBytes := makePNG(t, 4, 6, color.White)
	cleanBytes := makePNG(t, 4, 6, color.Black)
	fake := &fakePlex{
		poster:       oldBytes,
		posters:      []plex.PosterEntry{uploadEntry(true), providerEntry(false)},
		posterBytes:  map[string][]byte{providerEntry(false).Key: cleanBytes},
		modelUploads: true,

		uploadUnknownErr:     unknownUploadError(),
		uploadUnknownApplies: false,
	}
	a := fastLandingApplier(dir, fake)

	outcome, backupPath, errMsg, err := a.RestoreDefaultPoster(context.Background(), testServer(), "1", 1, "7", "Item 7", restoreThumb)
	if err != nil {
		t.Fatalf("RestoreDefaultPoster: %v", err)
	}
	if outcome != "failed" {
		t.Fatalf("outcome = %q, want failed (the upload never landed)", outcome)
	}
	if !strings.Contains(errMsg, "Write outcome unknown") {
		t.Fatalf("errMsg = %q, want the original unknown-outcome message kept", errMsg)
	}
	// The observation: after the dropped upload the posters list is
	// unchanged — the previously selected upload is still selected,
	// so the uploaded clean poster is not the selected one.
	if !strings.Contains(errMsg, "Post-upload verification: the uploaded poster is not the selected one.") {
		t.Fatalf("errMsg = %q, want the verification observation appended", errMsg)
	}
	if fake.uploadAttempts != 1 || len(fake.uploads) != 0 {
		t.Fatalf("upload attempts = %d, received = %d, want exactly 1 attempt and no landed upload", fake.uploadAttempts, len(fake.uploads))
	}
	if backupPath == "" {
		t.Fatal("the backup still happened and must be reported")
	}
}

// Apply path, landed: the composite upload's confirmation never
// arrives, but the server serves the composite afterwards. The
// job counts as applied, via multipart, with the honesty note on
// the item — and exactly one upload attempt.
func TestApplyUnknownUploadVerifiedApplies(t *testing.T) {
	dir := t.TempDir()
	fake := &fakePlex{
		poster:       makePNG(t, 4, 6, color.White),
		modelUploads: true,

		uploadUnknownErr:     unknownUploadError(),
		uploadUnknownApplies: true,
	}
	a := fastLandingApplier(dir, fake)
	rec, err := a.RunApplyJob(context.Background(), ApplyRequest{
		Server: testServer(),
		Jobs:   []Job{pngJob(t, "7")},
	})
	if err != nil {
		t.Fatalf("RunApplyJob: %v", err)
	}
	if len(rec.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(rec.Items))
	}
	it := rec.Items[0]
	if !it.OK || it.Via != "multipart" {
		t.Fatalf("item = %+v, want applied via multipart", it)
	}
	if it.Error != applyVerifiedUnknownNote {
		t.Fatalf("item note = %q, want the verified-after-unknown note", it.Error)
	}
	if rec.OKCount != 1 || rec.FailCount != 0 {
		t.Fatalf("counts ok=%d fail=%d, want 1/0", rec.OKCount, rec.FailCount)
	}
	if fake.uploadAttempts != 1 || len(fake.uploads) != 1 {
		t.Fatalf("upload attempts = %d, received = %d, want exactly 1", fake.uploadAttempts, len(fake.uploads))
	}
}

// Apply path, dropped: the composite never lands, the served bytes
// stay the old poster's. The job fails with the unknown-outcome
// message plus the served-bytes observation; one attempt only.
func TestApplyUnknownUploadDroppedStaysFailed(t *testing.T) {
	dir := t.TempDir()
	fake := &fakePlex{
		poster:       makePNG(t, 4, 6, color.White),
		modelUploads: true,

		uploadUnknownErr:     unknownUploadError(),
		uploadUnknownApplies: false,
	}
	a := fastLandingApplier(dir, fake)
	rec, err := a.RunApplyJob(context.Background(), ApplyRequest{
		Server: testServer(),
		Jobs:   []Job{pngJob(t, "7")},
	})
	if err != nil {
		t.Fatalf("RunApplyJob: %v", err)
	}
	if len(rec.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(rec.Items))
	}
	it := rec.Items[0]
	if it.OK {
		t.Fatalf("item = %+v, want failed (the upload never landed)", it)
	}
	if !strings.Contains(it.Error, "Write outcome unknown") ||
		!strings.Contains(it.Error, "Post-upload verification: the served image still differs from the uploaded poster.") {
		t.Fatalf("item error = %q, want unknown-outcome + served-bytes observation", it.Error)
	}
	if rec.FailCount != 1 {
		t.Fatalf("failCount = %d, want 1", rec.FailCount)
	}
	if fake.uploadAttempts != 1 || len(fake.uploads) != 0 {
		t.Fatalf("upload attempts = %d, received = %d, want exactly 1 attempt and no landed upload", fake.uploadAttempts, len(fake.uploads))
	}
}

// The note channel end to end: a restored outcome reported by the
// client carries its note into the persisted run record (the
// history), while other non-failure outcomes still have any text
// cleared, exactly as before.
func TestPhasedRestoredOutcomeKeepsItsNote(t *testing.T) {
	dir := t.TempDir()
	fake := &fakePlex{poster: makePNG(t, 4, 6, color.White)}
	a := newFakeApplier(dir, fake)
	srv := testServer()
	ctx := context.Background()
	const runID = "run-note-test-1"

	if _, err := a.RunApplyJob(ctx, ApplyRequest{Server: srv, RunID: runID, RunPhase: "start"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	rec, err := a.RunApplyJob(ctx, ApplyRequest{
		Server: srv, RunID: runID, RunPhase: "finish",
		Outcomes: []RunItem{
			{RatingKey: "31", Title: "Item 31", Outcome: "restored", Error: restoreVerifiedUnknownNote, BackupPath: "/backups/31.jpg"},
			{RatingKey: "32", Title: "Item 32", Outcome: "already-default", Error: "stale text"},
		},
	})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if len(rec.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(rec.Items))
	}
	if !rec.Items[0].OK || rec.Items[0].Error != restoreVerifiedUnknownNote {
		t.Fatalf("restored item = %+v, want OK with its note preserved", rec.Items[0])
	}
	if rec.Items[1].Error != "" {
		t.Fatalf("already-default item error = %q, want cleared as before", rec.Items[1].Error)
	}
	got := a.getRun(runID)
	if got == nil || got.Items[0].Error != restoreVerifiedUnknownNote {
		t.Fatalf("persisted record = %+v, want the note in history", got)
	}
}
