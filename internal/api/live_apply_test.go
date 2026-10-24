package api

// Live-PMS overlay APPLY E2E — SKIPPED unless PCM_LIVE_PLEX points at
// the throwaway sandbox PMS. Built 2026-10-22 for the Overlay Studio
// overhaul: this is the full write path a browser drives — POST
// /api/overlay/apply with one composited PNG for the SDR test item —
// asserting (a) the run reports success, (b) the item's poster bytes
// ON THE SERVER actually changed, and (c) restore puts the original
// back. Handler-level facts tests can't prove any of that.
//
//	PCM_LIVE_PLEX=http://127.0.0.1:32400 PCM_LIVE_MACHINE=<id> \
//	  PCM_LIVE_APPLY_KEY=<ratingKey> go test ./internal/api/ -run LiveOverlayApply -v

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"plexcollectionmanager/internal/overlays"
	"plexcollectionmanager/internal/plex"
)

func TestLiveOverlayApplyRestore(t *testing.T) {
	ts, _ := liveAppServer(t)
	key := os.Getenv("PCM_LIVE_APPLY_KEY")
	if key == "" {
		t.Skip("PCM_LIVE_APPLY_KEY not set — live apply test skipped")
	}
	base := os.Getenv("PCM_LIVE_PLEX")

	posterBytes := func() []byte {
		res, err := http.Get(base + "/library/metadata/" + key + "/thumb")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return b
	}
	before := posterBytes()
	if len(before) == 0 {
		t.Fatal("item has no poster on the server to back up")
	}

	// A real PNG composite stand-in (solid 100x150) — the apply path
	// only cares that the bytes are a valid poster image.
	img := image.NewRGBA(image.Rect(0, 0, 100, 150))
	for y := 0; y < 150; y++ {
		for x := 0; x < 100; x++ {
			img.Set(x, y, color.RGBA{180, 40, 40, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	job := map[string]any{
		"ratingKey": key, "title": "Live Apply Test", "thumb": "/library/metadata/" + key + "/thumb",
		"pngBase64": base64.StdEncoding.EncodeToString(buf.Bytes()),
	}
	var applyEnv struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Record struct {
			RunID     string `json:"runId"`
			OKCount   int    `json:"okCount"`
			FailCount int    `json:"failCount"`
			Items     []struct {
				RatingKey string `json:"ratingKey"`
				OK        bool   `json:"ok"`
				Error     string `json:"error"`
			} `json:"items"`
		} `json:"record"`
	}
	rpc(t, ts, "overlay/apply", map[string]any{
		"jobs":     []any{job},
		"badgeSet": json.RawMessage(`{"name":"live-test","placements":[]}`),
		"target":   json.RawMessage(`{"type":"item","count":1}`),
	}, &applyEnv)
	if !applyEnv.OK {
		t.Fatalf("apply RPC failed: %s", applyEnv.Error)
	}
	if applyEnv.Record.OKCount != 1 {
		t.Fatalf("apply okCount = %d, want 1 (items: %+v)", applyEnv.Record.OKCount, applyEnv.Record.Items)
	}
	after := posterBytes()
	if bytes.Equal(before, after) {
		t.Fatalf("poster bytes on the server did NOT change after a successful apply")
	}
	t.Logf("poster changed on server: %d -> %d bytes, run %s", len(before), len(after), applyEnv.Record.RunID)

	// Restore must bring the original back (the backup gate's promise).
	var restoreEnv struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	rpc(t, ts, "overlay/restore", map[string]any{"ratingKey": key, "runId": applyEnv.Record.RunID}, &restoreEnv)
	if !restoreEnv.OK {
		t.Fatalf("restore RPC failed: %s", restoreEnv.Error)
	}
	restored := posterBytes()
	if !bytes.Equal(before, restored) {
		t.Fatalf("poster after restore differs from the original (%d vs %d bytes)", len(restored), len(before))
	}
	t.Logf("restore verified: original poster bytes are back")
}

func TestLiveOverlayApplyRestoreWebP(t *testing.T) {
	ts, machine := liveAppServer(t)
	key := os.Getenv("PCM_LIVE_APPLY_KEY")
	if key == "" {
		t.Skip("PCM_LIVE_APPLY_KEY not set — live WebP apply test skipped")
	}
	base := os.Getenv("PCM_LIVE_PLEX")
	ctx := context.Background()

	posterBytes := func() []byte {
		res, err := http.Get(base + "/library/metadata/" + key + "/thumb")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("poster fetch returned HTTP %d", res.StatusCode)
		}
		return b
	}
	prior := posterBytes()
	if len(prior) == 0 {
		t.Fatal("item has no poster on the server to restore after the WebP test")
	}

	webpPath := filepath.Join(t.TempDir(), "poster.webp")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=0x336699:s=600x900", "-frames:v", "1", webpPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate WebP fixture: %v — %s", err, out)
	}
	webpBytes, err := os.ReadFile(webpPath)
	if err != nil {
		t.Fatal(err)
	}
	if ok, typ := overlays.IsImageBytes(webpBytes); !ok || typ != "webp" {
		t.Fatalf("generated fixture is not WebP: ok=%v typ=%q", ok, typ)
	}

	// A separate client seeds the fixture poster and provides an
	// emergency restore path. Its verifier is deliberately permissive
	// for test setup only; the apply below still runs through the real
	// app server and its real backup gate.
	setupServer := &plex.Server{
		Name:           "LiveTest",
		Machine:        machine,
		Owned:          true,
		Token:          "livetest-dummy-token",
		Connections:    []plex.Connection{{URI: base, Local: true}},
		BestConnection: plex.Connection{URI: base, Local: true, Quality: "local"},
	}
	setupClient := plex.NewClient("Plex Collection Manager", "live-test", "pcm-live-webp-test")
	setupClient.PosterBackupVerifier = func(*plex.Server, string) bool { return true }
	priorRestored := false
	defer func() {
		if !priorRestored {
			if err := setupClient.UploadPosterBytes(ctx, setupServer, key, prior); err != nil {
				t.Errorf("restore prior poster after WebP test: %v", err)
			}
		}
	}()
	if err := setupClient.UploadPosterBytes(ctx, setupServer, key, webpBytes); err != nil {
		t.Fatalf("seed WebP poster: %v", err)
	}
	seeded := posterBytes()
	if !bytes.Equal(seeded, webpBytes) {
		t.Fatalf("seeded poster fetch = %d bytes, want the exact %d WebP bytes", len(seeded), len(webpBytes))
	}

	img := image.NewRGBA(image.Rect(0, 0, 100, 150))
	for y := 0; y < 150; y++ {
		for x := 0; x < 100; x++ {
			img.Set(x, y, color.RGBA{40, 180, 80, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	job := map[string]any{
		"ratingKey": key, "title": "Live WebP Apply Test", "thumb": "/library/metadata/" + key + "/thumb",
		"pngBase64": base64.StdEncoding.EncodeToString(buf.Bytes()),
	}
	var applyEnv struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Record struct {
			RunID     string `json:"runId"`
			OKCount   int    `json:"okCount"`
			FailCount int    `json:"failCount"`
			Items     []struct {
				RatingKey string `json:"ratingKey"`
				OK        bool   `json:"ok"`
				Error     string `json:"error"`
			} `json:"items"`
		} `json:"record"`
	}
	rpc(t, ts, "overlay/apply", map[string]any{
		"jobs":     []any{job},
		"badgeSet": json.RawMessage(`{"name":"live-test-webp","placements":[]}`),
		"target":   json.RawMessage(`{"type":"item","count":1}`),
	}, &applyEnv)
	if !applyEnv.OK {
		t.Fatalf("WebP apply RPC failed: %s", applyEnv.Error)
	}
	if applyEnv.Record.OKCount != 1 {
		t.Fatalf("WebP apply okCount = %d, want 1 (items: %+v)", applyEnv.Record.OKCount, applyEnv.Record.Items)
	}
	after := posterBytes()
	if bytes.Equal(webpBytes, after) {
		t.Fatal("poster bytes on the server did NOT change after the WebP apply")
	}

	var restoreEnv struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	rpc(t, ts, "overlay/restore", map[string]any{"ratingKey": key, "runId": applyEnv.Record.RunID}, &restoreEnv)
	if !restoreEnv.OK {
		t.Fatalf("WebP restore RPC failed: %s", restoreEnv.Error)
	}
	restored := posterBytes()
	if !bytes.Equal(webpBytes, restored) {
		t.Fatalf("poster after app restore differs from the WebP original (%d vs %d bytes)", len(restored), len(webpBytes))
	}
	t.Logf("WebP apply/restore verified: %d-byte WebP backed up, applied over, and restored exactly", len(webpBytes))

	if err := setupClient.UploadPosterBytes(ctx, setupServer, key, prior); err != nil {
		t.Fatalf("restore prior poster: %v", err)
	}
	priorRestored = true
	if got := posterBytes(); !bytes.Equal(got, prior) {
		t.Fatalf("prior poster was not restored after the WebP test (%d vs %d bytes)", len(got), len(prior))
	}
}

// TestLiveOverlayApplyStreaming drives the v1.8 streaming form: two
// batches under ONE client-chosen run id plus a finish carrying a
// result-only outcome must land as exactly ONE History record with all
// three items, and the per-run restore must cover the applied items.
func TestLiveOverlayApplyStreaming(t *testing.T) {
	ts, _ := liveAppServer(t)
	keyA := os.Getenv("PCM_LIVE_APPLY_KEY")
	keyB := os.Getenv("PCM_LIVE_HDR_KEY") // a second, distinct item (APPLY and SDR keys are the same fixture)
	if keyA == "" || keyB == "" || keyA == keyB {
		t.Skip("PCM_LIVE_APPLY_KEY / PCM_LIVE_HDR_KEY not set or identical — live streaming test skipped")
	}
	solidPNG := func(r, g, b uint8) string {
		img := image.NewRGBA(image.Rect(0, 0, 100, 150))
		for y := 0; y < 150; y++ {
			for x := 0; x < 100; x++ {
				img.Set(x, y, color.RGBA{r, g, b, 255})
			}
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(buf.Bytes())
	}
	job := func(key string, r, g, b uint8) map[string]any {
		return map[string]any{
			"ratingKey": key, "title": "Live Streaming Test " + key,
			"thumb": "/library/metadata/" + key + "/thumb", "pngBase64": solidPNG(r, g, b),
		}
	}
	const runID = "run-live-streaming-test-1"
	badgeSet := json.RawMessage(`{"name":"live-stream","placements":[]}`)
	target := json.RawMessage(`{"type":"section","sectionKey":"1"}`)
	var env struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Record struct {
			RunID          string `json:"runId"`
			Status         string `json:"status"`
			OKCount        int    `json:"okCount"`
			NoOverlayCount int    `json:"noOverlayCount"`
			Items          []struct {
				RatingKey string `json:"ratingKey"`
				Outcome   string `json:"outcome"`
				OK        bool   `json:"ok"`
			} `json:"items"`
		} `json:"record"`
	}
	rpc(t, ts, "overlay/apply", map[string]any{
		"jobs": []any{job(keyA, 200, 30, 30)}, "badgeSet": badgeSet, "target": target,
		"runId": runID, "runPhase": "start",
	}, &env)
	if !env.OK || env.Record.Status != "running" || env.Record.OKCount != 1 {
		t.Fatalf("start: ok=%v err=%s record=%+v", env.OK, env.Error, env.Record)
	}
	rpc(t, ts, "overlay/apply", map[string]any{
		"jobs": []any{job(keyB, 30, 200, 30)}, "badgeSet": badgeSet, "target": target,
		"runId": runID, "runPhase": "append",
	}, &env)
	if !env.OK || len(env.Record.Items) != 2 {
		t.Fatalf("append: ok=%v err=%s items=%+v", env.OK, env.Error, env.Record.Items)
	}
	rpc(t, ts, "overlay/apply", map[string]any{
		"jobs": []any{}, "badgeSet": badgeSet, "target": target,
		"runId": runID, "runPhase": "finish",
		"outcomes": []any{map[string]any{"ratingKey": "999999", "title": "Ghost", "outcome": "no-overlay"}},
	}, &env)
	if !env.OK || env.Record.Status != "complete" || env.Record.OKCount != 2 || env.Record.NoOverlayCount != 1 || len(env.Record.Items) != 3 {
		t.Fatalf("finish: ok=%v err=%s record=%+v", env.OK, env.Error, env.Record)
	}
	// History shows ONE run for the whole action.
	var histEnv struct {
		OK   bool `json:"ok"`
		Runs []struct {
			RunID string `json:"runId"`
		} `json:"runs"`
	}
	rpc(t, ts, "overlay/history", map[string]any{}, &histEnv)
	found := 0
	for _, r := range histEnv.Runs {
		if r.RunID == runID {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("history contains the streaming run %d times, want exactly 1", found)
	}
	// Per-run restore covers the two applied items and skips the
	// no-overlay entry (it was never written).
	var restoreEnv struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		OKCount int    `json:"okCount"`
		Results []struct {
			RatingKey string `json:"ratingKey"`
			OK        bool   `json:"ok"`
			Skipped   bool   `json:"skipped"`
		} `json:"results"`
	}
	rpc(t, ts, "overlay/restore", map[string]any{"runId": runID}, &restoreEnv)
	if !restoreEnv.OK || restoreEnv.OKCount != 2 || len(restoreEnv.Results) != 3 {
		t.Fatalf("restore run: ok=%v err=%s res=%+v", restoreEnv.OK, restoreEnv.Error, restoreEnv)
	}
	skipped := 0
	for _, r := range restoreEnv.Results {
		if r.Skipped {
			skipped++
		}
	}
	if skipped != 1 {
		t.Fatalf("restore results skipped = %d, want 1 (the no-overlay entry)", skipped)
	}
}

// TestLiveRestoreDefaultPoster is the restore-default E2E: seed an
// uploaded poster (which becomes the selected one), classify through
// the app, restore through the app, and verify the UPLOAD mechanism
// on the server itself: the selected entry is the restore's new
// upload, the item's thumb path moved (updatedAt bumped), the raw
// bytes at the new address are the provider poster's, and the
// transcoder rendition changed. (A selection flip would move none
// of the address/rendition checks — that was the live failure this
// mechanism replaced.) The PCM backup is proven by restoring it
// afterwards: the seeded upload's exact bytes come back. If the
// fixture item has no provider poster, the test proves the honest
// skip instead (classification API must say so) — the sandbox's
// local posters are media:// entries, which ARE the non-upload side
// of the list.
func TestLiveRestoreDefaultPoster(t *testing.T) {
	ts, machine := liveAppServer(t)
	key := os.Getenv("PCM_LIVE_APPLY_KEY")
	if key == "" {
		t.Skip("PCM_LIVE_APPLY_KEY not set — live restore-default test skipped")
	}
	base := os.Getenv("PCM_LIVE_PLEX")
	ctx := context.Background()

	posters := func() []plex.PosterEntry {
		req, _ := http.NewRequest("GET", base+"/library/metadata/"+key+"/posters", nil)
		req.Header.Set("Accept", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var env struct {
			MediaContainer struct {
				Metadata []plex.PosterEntry `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
			t.Fatal(err)
		}
		// The wire shape has no provider field for local posters; the
		// client's scheme fallback is what classifies them, so mirror
		// it here for the skip check only.
		out := env.MediaContainer.Metadata
		for i := range out {
			if out[i].Provider == "" {
				if j := indexOfScheme(out[i].RatingKey); j > 0 {
					out[i].Provider = out[i].RatingKey[:j]
				}
			}
		}
		return out
	}
	selectedOf := func(list []plex.PosterEntry) *plex.PosterEntry {
		for i := range list {
			if list[i].Selected {
				return &list[i]
			}
		}
		return nil
	}

	// Seed: upload a distinctive poster via a permissive setup client
	// (test setup only — the restore itself runs through the real app
	// and its real backup gate). Uploads become the selected poster.
	img := image.NewRGBA(image.Rect(0, 0, 60, 90))
	for y := 0; y < 90; y++ {
		for x := 0; x < 60; x++ {
			img.Set(x, y, color.RGBA{200, 120, 20, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	seedBytes := buf.Bytes()
	setupServer := &plex.Server{
		Name: "LiveTest", Machine: machine, Owned: true, Token: "livetest-dummy-token",
		Connections:    []plex.Connection{{URI: base, Local: true}},
		BestConnection: plex.Connection{URI: base, Local: true, Quality: "local"},
	}
	setupClient := plex.NewClient("Plex Collection Manager", "live-test", "pcm-live-restore-test")
	setupClient.PosterBackupVerifier = func(*plex.Server, string) bool { return true }
	if err := setupClient.UploadPosterBytes(ctx, setupServer, key, seedBytes); err != nil {
		t.Fatalf("seed upload poster: %v", err)
	}
	before := posters()
	sel := selectedOf(before)
	if sel == nil || !sel.IsUpload() {
		t.Fatalf("after seeding, the selected poster should be the upload; list = %+v", before)
	}
	class := plex.ClassifyPosters(before)
	if class.Status == "no-provider" {
		// Honest-skip path: prove the app's classification agrees.
		var classEnv struct {
			OK             bool `json:"ok"`
			Classification struct {
				Status string `json:"status"`
			} `json:"classification"`
		}
		rpc(t, ts, "overlay/classifyPoster", map[string]any{"ratingKey": key}, &classEnv)
		if !classEnv.OK || classEnv.Classification.Status != "no-provider" {
			t.Fatalf("classifyPoster = %+v, want no-provider", classEnv)
		}
		t.Skip("fixture item has no provider poster — honest skip proven via classification")
	}
	if class.Status != "restore" {
		t.Fatalf("classification = %q, want restore (selected is an upload, provider exists)", class.Status)
	}

	// Classify + restore through the real app server.
	var classEnv struct {
		OK             bool   `json:"ok"`
		Error          string `json:"error"`
		Classification struct {
			Status           string `json:"status"`
			DefaultRatingKey string `json:"defaultRatingKey"`
		} `json:"classification"`
	}
	rpc(t, ts, "overlay/classifyPoster", map[string]any{"ratingKey": key}, &classEnv)
	if !classEnv.OK || classEnv.Classification.Status != "restore" || classEnv.Classification.DefaultRatingKey != class.DefaultRatingKey {
		t.Fatalf("classifyPoster = %+v, want restore → %s", classEnv, class.DefaultRatingKey)
	}

	// The item's real thumb path (metadata form, with its timestamp) —
	// the URL clients hold across poster changes.
	var metaEnv struct {
		MediaContainer struct {
			Metadata []struct {
				Thumb string `json:"thumb"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	{
		req, _ := http.NewRequest("GET", base+"/library/metadata/"+key, nil)
		req.Header.Set("Accept", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if err := json.NewDecoder(res.Body).Decode(&metaEnv); err != nil {
			t.Fatal(err)
		}
	}
	if len(metaEnv.MediaContainer.Metadata) == 0 || metaEnv.MediaContainer.Metadata[0].Thumb == "" {
		t.Fatal("item metadata carries no thumb path")
	}
	thumbPath := metaEnv.MediaContainer.Metadata[0].Thumb
	fetchRaw := func(path string) []byte {
		res, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return b
	}
	artURL := ts.URL + "/art?machine=" + machine + "&path=" + url.QueryEscape(thumbPath) + "&w=0&t=" + testToken
	fetchArt := func() []byte {
		res, err := http.Get(artURL)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return b
	}
	// Prime the app's art cache with the SEEDED poster (currently
	// selected), so the post-restore /art fetch can only return the
	// clean poster if the restore evicted the cache entry.
	if primed := fetchArt(); !bytes.Equal(primed, seedBytes) {
		t.Fatalf("art prime: /art served %d bytes, want the seeded poster's %d", len(primed), len(seedBytes))
	}
	// The transcoder rendition clients currently receive, for the
	// post-restore change assertion.
	tcPath := func(p string) string {
		return "/photo/:/transcode?width=240&height=360&minSize=1&upscale=1&url=" + url.QueryEscape(p)
	}
	tcBefore := fetchRaw(tcPath(thumbPath))
	var restoreEnv struct {
		OK         bool   `json:"ok"`
		Outcome    string `json:"outcome"`
		BackupPath string `json:"backupPath"`
		// The envelope's "error" carries the per-item failure reason
		// (empty on success); transport failures share the field.
		ErrText string `json:"error"`
	}
	rpc(t, ts, "overlay/restoreDefaultPoster", map[string]any{
		"ratingKey": key, "title": "Live Restore Test", "thumb": "/library/metadata/" + key + "/thumb",
		"sectionKey": "1", "sectionType": "movie",
	}, &restoreEnv)
	if !restoreEnv.OK {
		t.Fatalf("restoreDefaultPoster RPC failed: %s", restoreEnv.ErrText)
	}
	if restoreEnv.Outcome != "restored" {
		t.Fatalf("outcome = %q (err %q), want restored", restoreEnv.Outcome, restoreEnv.ErrText)
	}
	if restoreEnv.BackupPath == "" {
		t.Fatal("restore reported no backup path — the backup invariant is unproven")
	}
	// Verify from the posters list itself: the selected entry is the
	// NEW upload the restore just made (not the provider entry, and
	// not the seeded upload) — the upload mechanism, proven on the
	// server.
	after := posters()
	selAfter := selectedOf(after)
	if selAfter == nil || !selAfter.IsUpload() || selAfter.RatingKey == sel.RatingKey {
		t.Fatalf("after restore, selected = %+v, want a NEW uploaded entry (seed was %s)", selAfter, sel.RatingKey)
	}
	t.Logf("upload mechanism verified on the server: selected %s → %s", sel.RatingKey, selAfter.RatingKey)

	// The provider poster's own bytes, from its posters-list entry.
	var providerEntry *plex.PosterEntry
	for i := range after {
		if after[i].RatingKey == class.DefaultRatingKey {
			providerEntry = &after[i]
		}
	}
	if providerEntry == nil {
		t.Fatalf("provider entry %s vanished from the posters list", class.DefaultRatingKey)
	}
	providerBytes := fetchRaw(providerEntry.Key)

	// The poster ADDRESS must have moved: re-read the item metadata
	// and compare thumb paths (upload bumps updatedAt; a selection
	// flip would have left this unchanged).
	thumbAfter := ""
	{
		var env2 struct {
			MediaContainer struct {
				Metadata []struct {
					Thumb string `json:"thumb"`
				} `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		req, _ := http.NewRequest("GET", base+"/library/metadata/"+key, nil)
		req.Header.Set("Accept", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if err := json.NewDecoder(res.Body).Decode(&env2); err != nil {
			t.Fatal(err)
		}
		if len(env2.MediaContainer.Metadata) > 0 {
			thumbAfter = env2.MediaContainer.Metadata[0].Thumb
		}
	}
	if thumbAfter == "" || thumbAfter == thumbPath {
		t.Fatalf("poster address did not refresh: before %q after %q", thumbPath, thumbAfter)
	}
	// Raw bytes at the NEW address are the provider poster's bytes.
	if served := fetchRaw(thumbAfter); !bytes.Equal(served, providerBytes) {
		t.Fatalf("after restore, served thumb bytes (%d) differ from the provider poster's (%d)", len(served), len(providerBytes))
	}
	// And the transcoder rendition — what grids actually receive —
	// changed across the restore.
	if tcAfter := fetchRaw(tcPath(thumbAfter)); bytes.Equal(tcAfter, tcBefore) {
		t.Fatalf("transcoder rendition did not change across the restore (%d bytes both times)", len(tcAfter))
	}
	// The app's own art cache: primed with the seed under the OLD
	// address; the item's art at its new address is the clean poster.
	artAfterURL := ts.URL + "/art?machine=" + machine + "&path=" + url.QueryEscape(thumbAfter) + "&w=0&t=" + testToken
	{
		res, err := http.Get(artAfterURL)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if !bytes.Equal(b, providerBytes) {
			t.Fatalf("/art after restore served %d bytes, want the provider poster's %d", len(b), len(providerBytes))
		}
	}

	// The PCM backup must be the seeded poster's exact bytes: restore
	// it through the app and compare the served thumb.
	var backEnv struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	rpc(t, ts, "overlay/restore", map[string]any{"ratingKey": key}, &backEnv)
	if !backEnv.OK {
		t.Fatalf("restore backup RPC failed: %s", backEnv.Error)
	}
	res, err := http.Get(base + "/library/metadata/" + key + "/thumb")
	if err != nil {
		t.Fatal(err)
	}
	thumbBytes, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !bytes.Equal(thumbBytes, seedBytes) {
		t.Fatalf("restored thumb = %d bytes, want the seeded poster's exact %d bytes", len(thumbBytes), len(seedBytes))
	}
	t.Logf("backup verified: the pre-restore poster came back byte-exact")
}

// TestLiveRestoreDefaultStuckState is the E2E for the live failure
// behind the conservative restore classification: an item whose
// clean provider poster is SELECTED while an uploaded (baked)
// poster remains in its list — the state the old selection-based
// restore left behind ("selected, not applied"). The posters list
// alone calls that already-default, but the item's thumb address
// never moved, so the renditions clients receive at that address
// cannot be proven clean. The test constructs exactly that state
// on the sandbox (seed upload → flip the selection to the provider
// entry with a raw section-edit PUT, the selection mechanism
// itself), then proves: the app's classification says "restore",
// the restore run fixes it, and classification then settles at
// "already-default".
func TestLiveRestoreDefaultStuckState(t *testing.T) {
	ts, machine := liveAppServer(t)
	key := os.Getenv("PCM_LIVE_APPLY_KEY")
	if key == "" {
		t.Skip("PCM_LIVE_APPLY_KEY not set — live stuck-state test skipped")
	}
	base := os.Getenv("PCM_LIVE_PLEX")
	ctx := context.Background()

	posters := func() []plex.PosterEntry {
		req, _ := http.NewRequest("GET", base+"/library/metadata/"+key+"/posters", nil)
		req.Header.Set("Accept", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var env struct {
			MediaContainer struct {
				Metadata []plex.PosterEntry `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
			t.Fatal(err)
		}
		out := env.MediaContainer.Metadata
		for i := range out {
			if out[i].Provider == "" {
				if j := indexOfScheme(out[i].RatingKey); j > 0 {
					out[i].Provider = out[i].RatingKey[:j]
				}
			}
		}
		return out
	}
	itemMeta := func() (thumb string, sectionID int, typ string) {
		req, _ := http.NewRequest("GET", base+"/library/metadata/"+key, nil)
		req.Header.Set("Accept", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var env struct {
			MediaContainer struct {
				Metadata []struct {
					Thumb            string `json:"thumb"`
					LibrarySectionID int    `json:"librarySectionID"`
					Type             string `json:"type"`
				} `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
			t.Fatal(err)
		}
		if len(env.MediaContainer.Metadata) == 0 {
			t.Fatal("item metadata empty")
		}
		m := env.MediaContainer.Metadata[0]
		return m.Thumb, m.LibrarySectionID, m.Type
	}
	classifyViaApp := func() string {
		var classEnv struct {
			OK             bool   `json:"ok"`
			Error          string `json:"error"`
			Classification struct {
				Status string `json:"status"`
			} `json:"classification"`
		}
		rpc(t, ts, "overlay/classifyPoster", map[string]any{"ratingKey": key}, &classEnv)
		if !classEnv.OK {
			t.Fatalf("classifyPoster RPC failed: %s", classEnv.Error)
		}
		return classEnv.Classification.Status
	}

	// Seed: upload a distinctive "baked" poster through a permissive
	// setup client (test setup only). The upload becomes the selected
	// poster and moves the item's thumb address.
	img := image.NewRGBA(image.Rect(0, 0, 60, 90))
	for y := 0; y < 90; y++ {
		for x := 0; x < 60; x++ {
			img.Set(x, y, color.RGBA{30, 60, 200, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	setupServer := &plex.Server{
		Name: "LiveTest", Machine: machine, Owned: true, Token: "livetest-dummy-token",
		Connections:    []plex.Connection{{URI: base, Local: true}},
		BestConnection: plex.Connection{URI: base, Local: true, Quality: "local"},
	}
	setupClient := plex.NewClient("Plex Collection Manager", "live-test", "pcm-live-stuck-test")
	setupClient.PosterBackupVerifier = func(*plex.Server, string) bool { return true }

	// Seed: upload the baked poster — it lands selected and moves
	// the item's thumb address. Retry the upload while the address
	// does not move (measured on this PMS: rapid successive writes
	// can leave the address momentarily unmoved, and the flip below
	// is only meaningful against a freshly seeded address).
	preThumb, _, _ := itemMeta()
	upload := func() {
		if err := setupClient.UploadPosterBytes(ctx, setupServer, key, buf.Bytes()); err != nil {
			t.Fatalf("seed upload poster: %v", err)
		}
	}
	upload()
	var thumbSeeded string
	var sectionID int
	var itemType string
	for tries := 0; ; tries++ {
		var secID int
		var typ string
		thumbSeeded, secID, typ = itemMeta()
		if thumbSeeded != "" && thumbSeeded != preThumb && secID != 0 {
			sectionID, itemType = secID, typ
			break
		}
		if tries >= 5 {
			t.Fatal("seed upload never moved the item's poster address")
		}
		time.Sleep(1100 * time.Millisecond)
		upload()
	}
	list := posters()
	var provider *plex.PosterEntry
	seedSelected := false
	seedUploads := 0
	for i := range list {
		if list[i].IsUpload() {
			seedUploads++
			if list[i].Selected {
				seedSelected = true
			}
		} else if provider == nil {
			p := list[i]
			provider = &p
		}
	}
	if provider == nil {
		t.Skip("fixture item has no provider poster — stuck state cannot be constructed")
	}
	if seedUploads == 0 || !seedSelected {
		t.Fatalf("seed upload did not land selected: %+v", list)
	}
	typeNum := map[string]string{"movie": "1", "show": "2"}[itemType]
	if typeNum == "" {
		t.Fatalf("unsupported fixture type %q", itemType)
	}

	// Flip the selection to the provider entry with a raw
	// section-edit PUT — the selection mechanism itself (the app no
	// longer ships a select call; this is test-side construction of
	// the state that mechanism leaves behind).
	{
		form := url.Values{}
		form.Set("type", typeNum)
		form.Set("id", key)
		form.Set("thumb.value", provider.RatingKey)
		form.Set("thumb.locked", "1")
		u := base + "/library/sections/" + strconv.Itoa(sectionID) + "/all?" + form.Encode()
		req2, _ := http.NewRequest("PUT", u, nil)
		req2.Header.Set("X-Plex-Token", "livetest-dummy-token")
		res, err := http.DefaultClient.Do(req2)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode > 299 {
			t.Fatalf("selection flip PUT returned HTTP %d", res.StatusCode)
		}
	}

	// Prove the stuck state on the server itself: provider entry
	// selected, uploads still present, thumb address UNCHANGED.
	after := posters()
	var selAfter *plex.PosterEntry
	uploadsAfter := 0
	for i := range after {
		if after[i].Selected {
			s := after[i]
			selAfter = &s
		}
		if after[i].IsUpload() {
			uploadsAfter++
		}
	}
	if selAfter == nil || selAfter.IsUpload() {
		t.Fatalf("after the flip, selected = %+v, want the provider entry", selAfter)
	}
	if uploadsAfter == 0 {
		t.Fatal("after the flip, no upload entries remain — not the stuck state")
	}
	if pure := plex.ClassifyPosters(after); pure.Status != "already-default" {
		t.Fatalf("pure classification of the stuck state = %q, want already-default (that is the blind spot)", pure.Status)
	}
	thumbAfterFlip, _, _ := itemMeta()
	if thumbAfterFlip != thumbSeeded {
		t.Fatalf("selection flip moved the poster address (%q → %q) — the stuck state did not form", thumbSeeded, thumbAfterFlip)
	}
	t.Logf("stuck state proven: provider selected, %d upload(s) present, address %s unchanged", uploadsAfter, thumbAfterFlip)

	// The app's classification must not acquit the item on the
	// selection alone while an upload coexists.
	if got := classifyViaApp(); got != "restore" {
		t.Fatalf("app classification of the stuck state = %q, want restore", got)
	}
	// The restore run fixes it…
	var restoreEnv struct {
		OK      bool   `json:"ok"`
		Outcome string `json:"outcome"`
		ErrText string `json:"error"`
	}
	rpc(t, ts, "overlay/restoreDefaultPoster", map[string]any{
		"ratingKey": key, "title": "Live Stuck-State Test", "thumb": "/library/metadata/" + key + "/thumb",
		"sectionKey": strconv.Itoa(sectionID), "sectionType": itemType,
	}, &restoreEnv)
	if !restoreEnv.OK {
		t.Fatalf("restoreDefaultPoster RPC failed: %s", restoreEnv.ErrText)
	}
	if restoreEnv.Outcome != "restored" {
		t.Fatalf("outcome = %q (err %q), want restored", restoreEnv.Outcome, restoreEnv.ErrText)
	}
	// …and classification settles at already-default.
	if got := classifyViaApp(); got != "already-default" {
		t.Fatalf("app classification after restore = %q, want already-default", got)
	}
	t.Logf("stuck state restored through the app; classification settled at already-default")
}

func indexOfScheme(s string) int {
	for i := 0; i+2 < len(s); i++ {
		if s[i] == ':' && s[i+1] == '/' && s[i+2] == '/' {
			return i
		}
	}
	return -1
}
