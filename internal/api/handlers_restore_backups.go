package api

import (
	"net/http"

	"plexcollectionmanager/internal/overlays"
)

// ---- Restore backed-up posters, all levels (v1.23) ---------------------------
//
// The Overlays tab's "Restore default Plex posters" card restores
// TOP-LEVEL items to their clean PROVIDER poster. This pair restores
// ANY backed-up item — movie, show, season, episode — to its ORIGINAL
// poster, the bytes saved before an overlay was applied. The two
// destinations are different and the UI keeps them in separate cards
// so one is never mistaken for the other.
//
// Listing rule (honesty, enforced here): only items WITH a backup
// sidecar are listed. An item with no backup never appears, and no
// provider poster is ever offered as a substitute "original". Rows
// whose item no longer exists on the server, or whose backup image
// bytes are gone, are listed flagged with restore disabled — the
// user sees the stale backup instead of a restore that would fail.

// backupRow is one row of the listBackups response.
type backupRow struct {
	RatingKey        string `json:"ratingKey"`
	Title            string `json:"title"`
	Level            string `json:"level"` // movie | show | season | episode, or "" when unknown
	ParentTitle      string `json:"parentTitle,omitempty"`
	GrandparentTitle string `json:"grandparentTitle,omitempty"`
	BackedUpAt       string `json:"backedUpAt"`
	Found            bool   `json:"found"` // still exists on this server
	ImageExists      bool   `json:"imageExists"`
}

// handleOverlayListBackups enumerates the server's backed-up items
// and resolves each against live Plex metadata for its current title,
// level, and parent context. Metadata resolution is best-effort per
// item: a failed batch read fails the whole call (the list would be
// misleading without levels), but an individual item missing from
// Plex is listed flagged, not dropped — its backup is real even
// though the item is gone.
func (s *Server) handleOverlayListBackups(w http.ResponseWriter, r *http.Request) {
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	infos := s.overlay.ListBackups(server)
	rows := make([]backupRow, 0, len(infos))
	if len(infos) > 0 {
		keys := make([]string, 0, len(infos))
		for _, in := range infos {
			keys = append(keys, in.RatingKey)
		}
		batch, err := s.plexClient.GetItemsBatch(r.Context(), server, keys)
		if err != nil {
			failErr(w, err)
			return
		}
		meta := map[string]map[string]any{}
		for _, m := range batch {
			if k, _ := m["ratingKey"].(string); k != "" {
				meta[k] = m
			}
		}
		for _, in := range infos {
			row := backupRow{
				RatingKey: in.RatingKey, Title: in.Title,
				BackedUpAt: in.BackedUpAt, ImageExists: in.ImageExists,
			}
			if m, ok := meta[in.RatingKey]; ok {
				row.Found = true
				if t := strOf(m["title"], ""); t != "" {
					row.Title = t
				}
				row.Level = strOf(m["type"], "")
				row.ParentTitle = strOf(m["parentTitle"], "")
				row.GrandparentTitle = strOf(m["grandparentTitle"], "")
			}
			rows = append(rows, row)
		}
	}
	ok(w, envelope{"backups": rows})
}

// handleOverlayRestoreBackups restores each given item to its backed-up
// original through the shared gate and records the batch as one
// History run (items carry their levels). Items the caller marks
// un-restorable (not found, image missing) are refused here too —
// the server never trusts the client's buttons alone. A restore that
// fails mid-way reports a failed outcome for that item; other items
// still proceed, and no failure is ever reported as success.
func (s *Server) handleOverlayRestoreBackups(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []struct {
			RatingKey        string `json:"ratingKey"`
			Title            string `json:"title"`
			Level            string `json:"level"`
			ParentTitle      string `json:"parentTitle"`
			GrandparentTitle string `json:"grandparentTitle"`
		} `json:"items"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	if len(body.Items) == 0 {
		failMsg(w, "Nothing to restore.", "")
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	// Refuse items the list itself flagged un-restorable, before any
	// upload happens: re-read the backup store, don't trust the call.
	infos := s.overlay.ListBackups(server)
	byKey := map[string]overlays.BackupInfo{}
	for _, in := range infos {
		byKey[in.RatingKey] = in
	}
	items := make([]overlays.RestoreBackupItem, 0, len(body.Items))
	for _, in := range body.Items {
		info, ok := byKey[in.RatingKey]
		if !ok {
			failMsg(w, "Item "+in.RatingKey+" has no backup — nothing to restore for it.", "")
			return
		}
		if !info.ImageExists {
			failMsg(w, "The backup image for item "+in.RatingKey+" is missing — its current poster was left untouched.", "")
			return
		}
		items = append(items, overlays.RestoreBackupItem{
			RatingKey: in.RatingKey, Title: in.Title, Level: in.Level,
			ParentTitle: in.ParentTitle, GrandparentTitle: in.GrandparentTitle,
		})
	}
	record, err := s.overlay.RestoreBackups(r.Context(), server, items)
	if err != nil {
		failErr(w, err)
		return
	}
	// Restored items serve their backup bytes at the same thumb path —
	// evict the cached composite so grids show the restored poster
	// (same rule as the single-item restore handler).
	for _, it := range record.Items {
		if it.OK {
			if item, ierr := s.plexClient.GetItem(r.Context(), server, it.RatingKey); ierr == nil {
				s.invalidateArt(server.Machine, item.Thumb)
			}
		}
	}
	ok(w, envelope{"record": record, "outcomes": record.Items})
}
