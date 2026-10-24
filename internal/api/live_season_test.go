package api

// live_season_test.go — LIVE season overlay round trip (v1.9, plan
// phase 1): the sandbox show's Season 1 is fetched through the
// children RPC, its facts come from its own episodes, an overlay
// apply lands on the SEASON's ratingKey (thumb bytes change on the
// server, the run record identifies level + parent show), and the
// per-item restore-from-backup returns the season's original poster
// bytes. Runs only with the PCM_LIVE_* env (sandbox PMS); skipped
// otherwise.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"testing"
)

func TestLiveSeasonOverlayApplyRestore(t *testing.T) {
	ts, _ := liveAppServer(t)
	showKey := os.Getenv("PCM_LIVE_SHOW_KEY")
	if showKey == "" {
		t.Skip("PCM_LIVE_SHOW_KEY not set — live season test skipped")
	}
	base := os.Getenv("PCM_LIVE_PLEX")

	// The show's seasons, through the children RPC.
	var childEnv struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		Items []struct {
			RatingKey   string `json:"ratingKey"`
			Type        string `json:"type"`
			Title       string `json:"title"`
			Thumb       string `json:"thumb"`
			Index       int    `json:"index"`
			LeafCount   int    `json:"leafCount"`
			ParentTitle string `json:"parentTitle"`
		} `json:"items"`
	}
	rpc(t, ts, "plex/getItemChildren", map[string]any{"ratingKey": showKey}, &childEnv)
	if !childEnv.OK {
		t.Fatalf("children RPC failed: %s", childEnv.Error)
	}
	var season struct {
		RatingKey   string `json:"ratingKey"`
		Type        string `json:"type"`
		Title       string `json:"title"`
		Thumb       string `json:"thumb"`
		Index       int    `json:"index"`
		LeafCount   int    `json:"leafCount"`
		ParentTitle string `json:"parentTitle"`
	}
	found := false
	for _, it := range childEnv.Items {
		if it.Type == "season" {
			season = it
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("show %s has no season children in the sandbox (items: %+v)", showKey, childEnv.Items)
	}
	parentTitle := season.ParentTitle
	if parentTitle == "" {
		parentTitle = "Test Show" // the sandbox fixture's show title
	}
	t.Logf("season: key %s index %d leafCount %d parent %q", season.RatingKey, season.Index, season.LeafCount, parentTitle)

	// Facts for the season: computed from ITS episodes only.
	var factsEnv struct {
		OK    bool           `json:"ok"`
		Error string         `json:"error"`
		Facts map[string]any `json:"facts"`
	}
	rpc(t, ts, "overlay/itemFacts", map[string]any{"ratingKey": season.RatingKey}, &factsEnv)
	if !factsEnv.OK {
		t.Fatalf("season facts RPC failed: %s", factsEnv.Error)
	}
	f := factsEnv.Facts
	if f["type"] != "season" {
		t.Fatalf("facts type = %v, want season", f["type"])
	}
	if f["langsFromEpisodes"] != true {
		t.Fatalf("langsFromEpisodes = %v (langsError=%v), want true", f["langsFromEpisodes"], f["langsError"])
	}
	if season.LeafCount > 0 && f["episodeCount"] != float64(season.LeafCount) {
		t.Errorf("episodeCount = %v, want %d (the season's leafCount)", f["episodeCount"], season.LeafCount)
	}
	langs, _ := f["audioLangs"].([]any)
	seen := map[string]bool{}
	for _, l := range langs {
		if s, ok := l.(string); ok {
			seen[s] = true
		}
	}
	if !seen["eng"] || !seen["jpn"] {
		t.Errorf("audioLangs = %v, want the union [eng jpn] of the sandbox season's episodes", f["audioLangs"])
	}
	if f["dubClass"] != "dual" {
		t.Errorf("dubClass = %v, want dual", f["dubClass"])
	}
	if f["unwatchedCount"] != float64(season.LeafCount) {
		t.Errorf("unwatchedCount = %v, want %d (nothing watched in the sandbox)", f["unwatchedCount"], season.LeafCount)
	}
	// Show identity must not ride along on a season.
	if f["studio"] != nil || f["tvStatus"] != nil || f["streamingByRegion"] != nil {
		t.Errorf("season facts carry show identity: studio=%v status=%v streaming=%v", f["studio"], f["tvStatus"], f["streamingByRegion"])
	}

	// The season's CURRENT poster bytes, fetched at the thumb path
	// the season currently reports. (Measured on the sandbox: while
	// a season's poster is inherited from the show, the bare
	// /library/metadata/{season}/thumb address 404s — the reported
	// thumb path is the address that serves, and it moves to the
	// season's own address once a poster is uploaded to it.)
	currentSeasonThumb := func() string {
		var env struct {
			OK    bool `json:"ok"`
			Items []struct {
				RatingKey string `json:"ratingKey"`
				Thumb     string `json:"thumb"`
			} `json:"items"`
		}
		rpc(t, ts, "plex/getItemChildren", map[string]any{"ratingKey": showKey}, &env)
		for _, it := range env.Items {
			if it.RatingKey == season.RatingKey {
				return it.Thumb
			}
		}
		t.Fatalf("season %s vanished from the show's children", season.RatingKey)
		return ""
	}
	posterBytes := func() []byte {
		res, err := http.Get(base + currentSeasonThumb())
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("season poster fetch returned HTTP %d", res.StatusCode)
		}
		return b
	}
	before := posterBytes()
	if len(before) == 0 {
		t.Fatal("season has no poster on the server to back up")
	}

	// Composite stand-in + apply, with the season identity on the job.
	img := image.NewRGBA(image.Rect(0, 0, 100, 150))
	for y := 0; y < 150; y++ {
		for x := 0; x < 100; x++ {
			img.Set(x, y, color.RGBA{40, 120, 200, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	job := map[string]any{
		"ratingKey": season.RatingKey, "title": season.Title, "thumb": season.Thumb,
		"pngBase64":   base64.StdEncoding.EncodeToString(buf.Bytes()),
		"level":       "season",
		"parentTitle": parentTitle,
	}
	var applyEnv struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Record struct {
			RunID     string `json:"runId"`
			OKCount   int    `json:"okCount"`
			FailCount int    `json:"failCount"`
			Items     []struct {
				RatingKey   string `json:"ratingKey"`
				OK          bool   `json:"ok"`
				Error       string `json:"error"`
				Level       string `json:"level"`
				ParentTitle string `json:"parentTitle"`
			} `json:"items"`
		} `json:"record"`
	}
	rpc(t, ts, "overlay/apply", map[string]any{
		"jobs":     []any{job},
		"badgeSet": json.RawMessage(`{"name":"live-test-season","placements":[]}`),
		"target":   json.RawMessage(`{"type":"item","count":1}`),
	}, &applyEnv)
	if !applyEnv.OK {
		t.Fatalf("season apply RPC failed: %s", applyEnv.Error)
	}
	if applyEnv.Record.OKCount != 1 || len(applyEnv.Record.Items) != 1 {
		t.Fatalf("season apply okCount = %d (items: %+v), want 1", applyEnv.Record.OKCount, applyEnv.Record.Items)
	}
	rit := applyEnv.Record.Items[0]
	if rit.Level != "season" || rit.ParentTitle != parentTitle {
		t.Errorf("record item identity = level %q parent %q, want season / %q", rit.Level, rit.ParentTitle, parentTitle)
	}
	after := posterBytes()
	if bytes.Equal(before, after) {
		t.Fatal("season poster bytes on the server did NOT change after a successful apply")
	}
	if !bytes.Equal(after, buf.Bytes()) {
		t.Fatalf("season poster after apply is not the composite (%d vs %d bytes)", len(after), len(buf.Bytes()))
	}
	t.Logf("season poster changed on server: %d -> %d bytes, run %s", len(before), len(after), applyEnv.Record.RunID)

	// Restore-from-backup returns the season's original poster bytes.
	var restoreEnv struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	rpc(t, ts, "overlay/restore", map[string]any{"ratingKey": season.RatingKey, "runId": applyEnv.Record.RunID}, &restoreEnv)
	if !restoreEnv.OK {
		t.Fatalf("season restore RPC failed: %s", restoreEnv.Error)
	}
	restored := posterBytes()
	if !bytes.Equal(before, restored) {
		t.Fatalf("season poster after restore differs from the original (%d vs %d bytes)", len(restored), len(before))
	}
	t.Logf("season restore verified: original poster bytes are back")
}
