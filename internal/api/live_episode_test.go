package api

// live_episode_test.go — LIVE episode overlay round trip (v1.10,
// plan phase 2): the sandbox show's Season 1 episodes are fetched
// through the children RPC (show → season → episodes, the drill
// path), an episode's facts come from ITS OWN streams (no
// aggregation), an overlay apply lands on the EPISODE's ratingKey
// (served still bytes change on the server and equal the composite,
// the run record identifies level "episode" + season + show), and
// the per-item restore-from-backup returns the episode's original
// still bytes. Runs only with the PCM_LIVE_* env (sandbox PMS);
// skipped otherwise.

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

type liveChildItem struct {
	RatingKey   string `json:"ratingKey"`
	Type        string `json:"type"`
	Title       string `json:"title"`
	Thumb       string `json:"thumb"`
	Index       int    `json:"index"`
	LeafCount   int    `json:"leafCount"`
	ParentTitle string `json:"parentTitle"`
}

func TestLiveEpisodeOverlayApplyRestore(t *testing.T) {
	ts, _ := liveAppServer(t)
	showKey := os.Getenv("PCM_LIVE_SHOW_KEY")
	if showKey == "" {
		t.Skip("PCM_LIVE_SHOW_KEY not set — live episode test skipped")
	}
	base := os.Getenv("PCM_LIVE_PLEX")

	childrenOf := func(key string) []liveChildItem {
		var env struct {
			OK    bool            `json:"ok"`
			Error string          `json:"error"`
			Items []liveChildItem `json:"items"`
		}
		rpc(t, ts, "plex/getItemChildren", map[string]any{"ratingKey": key}, &env)
		if !env.OK {
			t.Fatalf("children RPC for %s failed: %s", key, env.Error)
		}
		return env.Items
	}
	factsOf := func(key string) map[string]any {
		var env struct {
			OK    bool           `json:"ok"`
			Error string         `json:"error"`
			Facts map[string]any `json:"facts"`
		}
		rpc(t, ts, "overlay/itemFacts", map[string]any{"ratingKey": key}, &env)
		if !env.OK {
			t.Fatalf("facts RPC for %s failed: %s", key, env.Error)
		}
		return env.Facts
	}

	// Drill: show → its season → the season's episodes.
	var season *liveChildItem
	showChildren := childrenOf(showKey)
	for i := range showChildren {
		if showChildren[i].Type == "season" {
			season = &showChildren[i]
			break
		}
	}
	if season == nil {
		t.Fatalf("show %s has no season children in the sandbox", showKey)
	}
	showTitle := season.ParentTitle
	if showTitle == "" {
		showTitle = "Test Show" // the sandbox fixture's show title
	}
	var ep1, epOther *liveChildItem
	eps := childrenOf(season.RatingKey)
	for i := range eps {
		if eps[i].Type != "episode" {
			continue
		}
		if eps[i].Index == 1 && ep1 == nil {
			ep1 = &eps[i]
		} else if epOther == nil {
			epOther = &eps[i]
		}
	}
	if ep1 == nil {
		t.Fatalf("season %s has no episode children in the sandbox (items: %+v)", season.RatingKey, eps)
	}
	t.Logf("episode: key %s index %d title %q (season %s of %q)", ep1.RatingKey, ep1.Index, ep1.Title, season.Title, showTitle)

	// Facts: the episode's OWN streams. The sandbox's episode 1 is
	// SD/h264 with eng+jpn audio (dual) and nothing watched.
	f := factsOf(ep1.RatingKey)
	if f["type"] != "episode" {
		t.Fatalf("facts type = %v, want episode", f["type"])
	}
	if f["resolutionTier"] != "SD" || f["videoCodec"] != "h264" {
		t.Errorf("tier/codec = %v/%v, want SD/h264 (episode 1's own video stream)", f["resolutionTier"], f["videoCodec"])
	}
	langs, _ := f["audioLangs"].([]any)
	seen := map[string]bool{}
	for _, l := range langs {
		if s, ok := l.(string); ok {
			seen[s] = true
		}
	}
	if !seen["eng"] || !seen["jpn"] || len(langs) != 2 {
		t.Errorf("audioLangs = %v, want [eng jpn] (episode 1's own two audio streams)", f["audioLangs"])
	}
	if f["dubClass"] != "dual" {
		t.Errorf("dubClass = %v, want dual", f["dubClass"])
	}
	if f["unwatchedCount"] != float64(1) {
		t.Errorf("unwatchedCount = %v, want 1 (the episode's own view state)", f["unwatchedCount"])
	}
	// Identity stays off an episode even on a real server.
	if f["studio"] != nil || f["sourceTag"] != nil || f["editionTitle"] != nil || f["awardsText"] != nil || f["streamingByRegion"] != nil || f["tvStatus"] != nil {
		t.Errorf("episode facts carry identity: studio=%v source=%v edition=%v awards=%v streaming=%v status=%v",
			f["studio"], f["sourceTag"], f["editionTitle"], f["awardsText"], f["streamingByRegion"], f["tvStatus"])
	}
	if genres, _ := f["genres"].([]any); len(genres) != 0 {
		t.Errorf("genres = %v, want [] for an episode", f["genres"])
	}
	// A sibling episode judges DIFFERENTLY on its own streams: the
	// sandbox's other episodes carry a single eng audio track.
	if epOther != nil {
		fo := factsOf(epOther.RatingKey)
		if fo["dubClass"] == f["dubClass"] {
			t.Errorf("sibling episode %s dubClass = %v, same as episode 1 — episodes must judge on their own streams", epOther.RatingKey, fo["dubClass"])
		}
		if ol, _ := fo["audioLangs"].([]any); len(ol) != 1 {
			t.Errorf("sibling episode audioLangs = %v, want a single language", fo["audioLangs"])
		}
	}

	// The episode's CURRENT still bytes, fetched at the thumb path
	// the episode currently reports (re-read per fetch via the
	// season's children: an upload moves the address).
	currentEpisodeThumb := func() string {
		for _, it := range childrenOf(season.RatingKey) {
			if it.RatingKey == ep1.RatingKey {
				return it.Thumb
			}
		}
		t.Fatalf("episode %s vanished from the season's children", ep1.RatingKey)
		return ""
	}
	stillBytes := func() []byte {
		res, err := http.Get(base + currentEpisodeThumb())
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("episode still fetch returned HTTP %d", res.StatusCode)
		}
		return b
	}
	before := stillBytes()
	if len(before) == 0 {
		t.Fatal("episode has no still on the server to back up")
	}

	// Composite stand-in (landscape, like a real episode composite)
	// + apply, with the full episode identity on the job.
	img := image.NewRGBA(image.Rect(0, 0, 160, 90))
	for y := 0; y < 90; y++ {
		for x := 0; x < 160; x++ {
			img.Set(x, y, color.RGBA{200, 120, 40, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	job := map[string]any{
		"ratingKey": ep1.RatingKey, "title": ep1.Title, "thumb": ep1.Thumb,
		"pngBase64":        base64.StdEncoding.EncodeToString(buf.Bytes()),
		"level":            "episode",
		"parentTitle":      season.Title,
		"grandparentTitle": showTitle,
	}
	var applyEnv struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Record struct {
			RunID     string `json:"runId"`
			OKCount   int    `json:"okCount"`
			FailCount int    `json:"failCount"`
			Items     []struct {
				RatingKey        string `json:"ratingKey"`
				OK               bool   `json:"ok"`
				Error            string `json:"error"`
				Level            string `json:"level"`
				ParentTitle      string `json:"parentTitle"`
				GrandparentTitle string `json:"grandparentTitle"`
			} `json:"items"`
		} `json:"record"`
	}
	rpc(t, ts, "overlay/apply", map[string]any{
		"jobs":     []any{job},
		"badgeSet": json.RawMessage(`{"name":"live-test-episode","placements":[]}`),
		"target":   json.RawMessage(`{"type":"item","count":1}`),
	}, &applyEnv)
	if !applyEnv.OK {
		t.Fatalf("episode apply RPC failed: %s", applyEnv.Error)
	}
	if applyEnv.Record.OKCount != 1 || len(applyEnv.Record.Items) != 1 {
		t.Fatalf("episode apply okCount = %d (items: %+v), want 1", applyEnv.Record.OKCount, applyEnv.Record.Items)
	}
	rit := applyEnv.Record.Items[0]
	if rit.Level != "episode" || rit.ParentTitle != season.Title || rit.GrandparentTitle != showTitle {
		t.Errorf("record item identity = level %q parent %q grandparent %q, want episode / %q / %q",
			rit.Level, rit.ParentTitle, rit.GrandparentTitle, season.Title, showTitle)
	}
	after := stillBytes()
	if bytes.Equal(before, after) {
		t.Fatal("episode still bytes on the server did NOT change after a successful apply")
	}
	if !bytes.Equal(after, buf.Bytes()) {
		t.Fatalf("episode still after apply is not the composite (%d vs %d bytes)", len(after), len(buf.Bytes()))
	}
	t.Logf("episode still changed on server: %d -> %d bytes, run %s", len(before), len(after), applyEnv.Record.RunID)

	// Restore-from-backup returns the episode's original still bytes.
	var restoreEnv struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	rpc(t, ts, "overlay/restore", map[string]any{"ratingKey": ep1.RatingKey, "runId": applyEnv.Record.RunID}, &restoreEnv)
	if !restoreEnv.OK {
		t.Fatalf("episode restore RPC failed: %s", restoreEnv.Error)
	}
	restored := stillBytes()
	if !bytes.Equal(before, restored) {
		t.Fatalf("episode still after restore differs from the original (%d vs %d bytes)", len(restored), len(before))
	}
	t.Logf("episode restore verified: original still bytes are back")
}
