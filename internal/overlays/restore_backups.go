package overlays

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"plexcollectionmanager/internal/plex"
)

// ---- Restore backed-up posters, all levels (v1.23) ---------------------------
//
// Two restore destinations exist in this app and they must not be
// confused:
//
//   - Restore DEFAULT posters (the Overlays tab's "Restore default
//     Plex posters" card): puts an item back on its clean PROVIDER
//     poster. Its card enumerates top-level library items only.
//   - Restore BACKED-UP posters (this file): puts an item back on the
//     ORIGINAL poster that was saved before an overlay was applied.
//     The backup store is keyed by ratingKey alone —
//     overlay-backups/{machine}/{ratingKey}.{ext} + {ratingKey}.json —
//     and every apply job, at every level, passes through the same
//     backupPoster gate in processJob, so seasons and episodes already
//     land here in exactly the top-level shape. No apply-path change
//     was needed for phase 3; what was missing was only the listing
//     and the bulk path, both built here.
//
// Honesty rules for this view, enforced below and in the API handler:
//   - An item with no backup record is NEVER listed and NEVER
//     restorable here. There is no fallback to a provider poster
//     dressed up as "the original".
//   - A backup whose image bytes are gone (sidecar survives, image
//     deleted by hand) is listed flagged, with restore disabled — and
//     if the file vanishes between list and restore, RestorePoster
//     fails BEFORE any upload, so the current poster stays untouched.
//   - RestorePoster verifies backup integrity (SHA-256 vs the sidecar)
//     before uploading anything; a corrupt backup is an error, never
//     a write.

// BackupInfo is one backed-up item: the sidecar's own record plus
// whether the image bytes still exist next to it. Title here is the
// sidecar's title at backup time; the API handler replaces it with
// fresh Plex metadata when the item still exists.
type BackupInfo struct {
	RatingKey   string
	Title       string
	BackedUpAt  string
	BackupPath  string // server-local; never sent to clients
	ImageExists bool
}

// ListBackups enumerates every verified-backup sidecar for the
// server's machine, newest first. Corrupt or unreadable sidecars are
// skipped, never listed — a row must never promise a restore its
// record cannot describe.
func (a *Applier) ListBackups(server *plex.Server) []BackupInfo {
	machine := ""
	if server != nil {
		machine = server.Machine
	}
	dir, _, _ := BackupPaths(a.userDataDir, machine, "", "")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []BackupInfo{}
	}
	out := []BackupInfo{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		rk := strings.TrimSuffix(e.Name(), ".json")
		if rk == "" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var sc sidecar
		if err := json.Unmarshal(raw, &sc); err != nil {
			continue
		}
		if sc.RatingKey != "" && sc.RatingKey != rk {
			continue // sidecar does not describe its own file; never list it
		}
		info := BackupInfo{
			RatingKey:  rk,
			Title:      sc.Title,
			BackedUpAt: sc.BackedUpAt,
		}
		for _, p := range backupImageCandidates(dir, rk, sc.ContentType) {
			if _, err := os.Stat(p); err == nil {
				info.ImageExists = true
				info.BackupPath = p
				break
			}
		}
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].BackedUpAt != out[j].BackedUpAt {
			return out[i].BackedUpAt > out[j].BackedUpAt
		}
		return out[i].RatingKey < out[j].RatingKey
	})
	return out
}

// backupImageCandidates mirrors RestorePoster's search order: the
// sidecar's content-type extension first, then every supported image
// extension. A backup is restorable only when one of these exists.
func backupImageCandidates(dir, rk, contentType string) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	add(filepath.Join(dir, rk+"."+extForContentType(contentType)))
	for _, ext := range []string{"png", "jpg", "webp", "gif", "avif"} {
		add(filepath.Join(dir, rk+"."+ext))
	}
	return out
}

// RestoreBackupItem is one item of a bulk backup-restore request.
// Level/ParentTitle/GrandparentTitle come from the list response the
// server itself produced, so the run record stays truthful; they are
// display metadata only — RestorePoster needs nothing but the key.
type RestoreBackupItem struct {
	RatingKey        string
	Title            string
	Level            string
	ParentTitle      string
	GrandparentTitle string
}

// RestoreBackups restores every given item to its backed-up original
// poster through the existing RestorePoster gate (integrity-checked
// before any upload; a failure leaves that item's current poster
// untouched and is reported as a failed outcome, never as success),
// then records the whole batch as ONE History run so the run's items
// carry their levels. Per-item restores in the UI ride this same path
// with a single item, so every backup restore lands in History.
func (a *Applier) RestoreBackups(ctx context.Context, server *plex.Server, items []RestoreBackupItem) (*RunRecord, error) {
	machine := ""
	if server != nil {
		machine = server.Machine
	}
	record := &RunRecord{
		RunID:     newRunID(time.Now()),
		Machine:   machine,
		Timestamp: isoNow(),
		Version:   "0.3.0",
		Target:    json.RawMessage(`{"type":"restore-backups"}`),
		BadgeSet:  json.RawMessage(`{"name":"Restore backed-up posters","placements":[]}`),
		Status:    "complete",
		UpdatedAt: isoNow(),
		Items:     []RunItem{},
	}
	for _, it := range items {
		rec := RunItem{
			RatingKey: it.RatingKey, Title: it.Title,
			Level: it.Level, ParentTitle: it.ParentTitle, GrandparentTitle: it.GrandparentTitle,
		}
		res, err := a.RestorePoster(ctx, server, it.RatingKey)
		if err != nil {
			rec.Error = errMsg(err)
		} else {
			rec.OK = true
			rec.Outcome = "restored"
			if v, _ := res["via"].(string); v != "" {
				rec.Via = v
			}
			if p, _ := res["backupPath"].(string); p != "" {
				rec.BackupPath = p
			}
		}
		record.Items = append(record.Items, rec)
		if rec.OK {
			record.OKCount++
		} else {
			record.FailCount++
		}
	}
	if err := a.saveRun(record); err != nil {
		return nil, err
	}
	return record, nil
}
