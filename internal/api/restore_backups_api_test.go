package api

// restore_backups_api_test.go — Restore backed-up posters, all levels
// (v1.23 phase 3), end to end through the HTTP layer against a fake
// PMS: badge a season and an episode through the real overlay apply
// path, list their backups with parent context, restore both to
// their originals, and prove the failure rules — a never-backed-up
// item is never listed, and a restore whose backup image is gone
// errors without touching the current poster.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type rbItem struct {
	id      string
	typ     string
	title   string
	parent  string
	grand   string
	orig    []byte
	current []byte
}

type rbFakePMS struct {
	mu    sync.Mutex
	items map[string]*rbItem
}

func (f *rbFakePMS) record(m *rbItem) map[string]any {
	r := map[string]any{
		"ratingKey": m.id, "type": m.typ, "title": m.title,
		"thumb": "/library/metadata/" + m.id + "/thumb/1",
	}
	if m.parent != "" {
		r["parentTitle"] = m.parent
	}
	if m.grand != "" {
		r["grandparentTitle"] = m.grand
	}
	return r
}

func (f *rbFakePMS) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.Path
	if !strings.HasPrefix(path, "/library/metadata/") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	rest := strings.TrimPrefix(path, "/library/metadata/")
	parts := strings.Split(rest, "/")
	id := parts[0]
	meta := func(list []any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{
			"size": len(list), "totalSize": len(list), "Metadata": list}})
	}
	// Batch records (GetItemsBatch joins keys with commas).
	if strings.Contains(id, ",") && len(parts) == 1 {
		list := []any{}
		for _, k := range strings.Split(id, ",") {
			if m, ok := f.items[k]; ok {
				list = append(list, f.record(m))
			}
		}
		meta(list)
		return
	}
	m, ok := f.items[id]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		meta([]any{f.record(m)})
	case len(parts) == 3 && parts[1] == "thumb" && r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(m.current)
	case len(parts) == 2 && parts[1] == "posters" && r.Method == http.MethodPost:
		body, _ := io.ReadAll(r.Body)
		up := extractMultipartFile(body)
		m.current = up
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newRbFakePMS(t *testing.T) (*rbFakePMS, *httptest.Server) {
	t.Helper()
	f := &rbFakePMS{items: map[string]*rbItem{}}
	mk := func(id, typ, title, parent, grand string, c color.RGBA) {
		png := genSolidPNG(t, 200, 300, c)
		f.items[id] = &rbItem{id: id, typ: typ, title: title, parent: parent, grand: grand, orig: png, current: png}
	}
	mk("100", "show", "Test Show", "", "", color.RGBA{20, 20, 20, 255})
	mk("110", "season", "Season 1", "Test Show", "", color.RGBA{30, 90, 160, 255})
	mk("111", "episode", "S1 E1", "Season 1", "Test Show", color.RGBA{160, 90, 30, 255})
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return f, srv
}

func rbApply(t *testing.T, ts *httptest.Server, runID, jobs string) {
	t.Helper()
	_, out := post(t, ts.URL+"/api/overlay/apply", testToken,
		fmt.Sprintf(`{"jobs":%s,"badgeSet":{"name":"Test","placements":[]},"target":{"type":"picked"},"runId":%q,"runPhase":"start"}`, jobs, runID))
	if out["ok"] != true {
		t.Fatalf("apply start: %v", out)
	}
	_, out = post(t, ts.URL+"/api/overlay/apply", testToken,
		fmt.Sprintf(`{"runId":%q,"runPhase":"finish"}`, runID))
	if out["ok"] != true {
		t.Fatalf("apply finish: %v", out)
	}
	rec := out["record"].(map[string]any)
	if rec["okCount"] != float64(2) {
		t.Fatalf("apply record = %v, want 2 ok", rec)
	}
}

func rbList(t *testing.T, ts *httptest.Server) []any {
	t.Helper()
	_, out := post(t, ts.URL+"/api/overlay/listBackups", testToken, `{}`)
	if out["ok"] != true {
		t.Fatalf("listBackups: %v", out)
	}
	list, _ := out["backups"].([]any)
	return list
}

func rbRow(list []any, rk string) map[string]any {
	for _, v := range list {
		if m, _ := v.(map[string]any); m["ratingKey"] == rk {
			return m
		}
	}
	return nil
}

func TestRestoreBackupsSeasonAndEpisodeEndToEnd(t *testing.T) {
	fake, pms := newRbFakePMS(t)
	ts, dataDir := newGenTestServer(t, pms.URL)

	// Badge the season and the episode through the real apply path.
	comp := func(c color.RGBA) string {
		return base64.StdEncoding.EncodeToString(genSolidPNG(t, 200, 300, c))
	}
	rbApply(t, ts, "run-rb-1", fmt.Sprintf(`[
		{"ratingKey":"110","title":"Season 1","thumb":"/library/metadata/110/thumb/1","pngBase64":%q,"level":"season","parentTitle":"Test Show"},
		{"ratingKey":"111","title":"S1 E1","thumb":"/library/metadata/111/thumb/1","pngBase64":%q,"level":"episode","parentTitle":"Season 1","grandparentTitle":"Test Show"}
	]`, comp(color.RGBA{200, 60, 40, 255}), comp(color.RGBA{40, 200, 60, 255})))
	if !bytes.Equal(fake.items["110"].current, genSolidPNG(t, 200, 300, color.RGBA{200, 60, 40, 255})) {
		t.Fatalf("season was not badged on the fake")
	}

	// The list shows both backed-up items with parent context and a
	// backup date — and NOT the show, which was never badged.
	list := rbList(t, ts)
	if len(list) != 2 {
		t.Fatalf("list = %d rows, want 2: %v", len(list), list)
	}
	season := rbRow(list, "110")
	episode := rbRow(list, "111")
	if season == nil || episode == nil {
		t.Fatalf("list missing season/episode: %v", list)
	}
	if season["level"] != "season" || season["parentTitle"] != "Test Show" || season["backedUpAt"] == "" {
		t.Fatalf("season row = %v", season)
	}
	if episode["level"] != "episode" || episode["parentTitle"] != "Season 1" || episode["grandparentTitle"] != "Test Show" {
		t.Fatalf("episode row = %v", episode)
	}
	if season["found"] != true || season["imageExists"] != true {
		t.Fatalf("season flags = %v", season)
	}

	// Bulk restore: both items serve their originals again.
	_, out := post(t, ts.URL+"/api/overlay/restoreBackups", testToken, `{"items":[
		{"ratingKey":"110","title":"Season 1","level":"season","parentTitle":"Test Show"},
		{"ratingKey":"111","title":"S1 E1","level":"episode","parentTitle":"Season 1","grandparentTitle":"Test Show"}
	]}`)
	if out["ok"] != true {
		t.Fatalf("restoreBackups: %v", out)
	}
	if !bytes.Equal(fake.items["110"].current, fake.items["110"].orig) ||
		!bytes.Equal(fake.items["111"].current, fake.items["111"].orig) {
		t.Fatalf("restored bytes are not the originals")
	}
	// The batch is one History run, and its items carry their levels.
	_, hist := post(t, ts.URL+"/api/overlay/history", testToken, `{}`)
	runs, _ := hist["runs"].([]any)
	found := false
	for _, v := range runs {
		rec, _ := v.(map[string]any)
		tgt, _ := rec["target"].(map[string]any)
		if tgt["type"] != "restore-backups" {
			continue
		}
		found = true
		items, _ := rec["items"].([]any)
		if len(items) != 2 {
			t.Fatalf("restore run items = %d, want 2", len(items))
		}
		levels := map[string]bool{}
		for _, iv := range items {
			im, _ := iv.(map[string]any)
			levels[im["level"].(string)] = true
			if im["ok"] != true {
				t.Fatalf("restore run item not ok: %v", im)
			}
		}
		if !levels["season"] || !levels["episode"] {
			t.Fatalf("restore run levels = %v, want season+episode", levels)
		}
	}
	if !found {
		t.Fatalf("no restore-backups run in history: %v", hist)
	}

	// Failure rule: delete the season's backup image by hand (sidecar
	// survives), re-badge it, then restore — the error is reported
	// and the current (badged) poster stays put.
	rbApply(t, ts, "run-rb-2", fmt.Sprintf(`[
		{"ratingKey":"110","title":"Season 1","thumb":"/library/metadata/110/thumb/1","pngBase64":%q,"level":"season","parentTitle":"Test Show"},
		{"ratingKey":"111","title":"S1 E1","thumb":"/library/metadata/111/thumb/1","pngBase64":%q,"level":"episode","parentTitle":"Season 1","grandparentTitle":"Test Show"}
	]`, comp(color.RGBA{200, 60, 40, 255}), comp(color.RGBA{40, 200, 60, 255})))
	// NOTE: backups are write-once, so the second apply reuses the
	// first backup; remove its image to model hand-deletion.
	badged := fake.items["110"].current
	imgPath := filepath.Join(dataDir, "overlay-backups", "fake-machine", "110.png")
	if err := os.Remove(imgPath); err != nil {
		t.Fatalf("remove backup image: %v", err)
	}
	_, out = post(t, ts.URL+"/api/overlay/restoreBackups", testToken, `{"items":[
		{"ratingKey":"110","title":"Season 1","level":"season","parentTitle":"Test Show"}
	]}`)
	if out["ok"] != false {
		t.Fatalf("restore with missing image should fail, got: %v", out)
	}
	if !bytes.Equal(fake.items["110"].current, badged) {
		t.Fatalf("failed restore changed the current poster")
	}
	// And the list now flags the image as missing instead of
	// offering a restore that cannot work.
	list = rbList(t, ts)
	if row := rbRow(list, "110"); row == nil || row["imageExists"] != false {
		t.Fatalf("season row after deletion = %v, want imageExists false", row)
	}
}
