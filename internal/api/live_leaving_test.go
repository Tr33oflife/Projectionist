package api

// Live-PMS Leaving Soon E2E — SKIPPED unless PCM_LIVE_PLEX points at
// the throwaway sandbox PMS. The full v1.12 loop against the real
// server, through the app's own API:
//   1. leaving/set dates the sandbox item (10 days out).
//   2. overlay/itemFacts carries the entry (leavingSet / leavingDate
//      / leavingDays judged against the app's local date).
//   3. The REAL composite engine (web/composite.js under node +
//      skia-canvas, the renderer's own code) lays out the leaving
//      family from those live facts — the script fails unless a
//      LEAVING SOON drawable exists — and the rendered poster is
//      applied through overlay/apply; the poster bytes on the
//      server change.
//   4. Restore returns the original poster bytes.
//   5. leaving/remove returns the facts to unset.
//
//	PCM_LIVE_PLEX=http://127.0.0.1:32400 PCM_LIVE_MACHINE=<id> \
//	  PCM_LIVE_APPLY_KEY=<ratingKey> go test ./internal/api/ -run LiveLeaving -v

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// renderLeavingCompositeJS is the node side of step 3: the browser's
// own layout + draw code over the item's current poster, with ONLY
// the leaving family enabled (soon mode). argv: composite.js path,
// skia-canvas module dir, poster path, facts JSON path, output path.
// Exits non-zero unless the layout produces the LEAVING SOON banner.
const renderLeavingCompositeJS = `
global.document = {};
const fs = require('fs');
const skia = require(process.argv[3]);
const CMP = require(process.argv[2]);
(async () => {
  const poster = await skia.loadImage(process.argv[4]);
  const facts = JSON.parse(fs.readFileSync(process.argv[5], 'utf8'));
  const families = { leaving: { on: true, variant: 'signature', mode: 'soon', windowDays: 30 } };
  const laid = CMP.layoutFamilyDrawables(families, facts, () => 0.2857);
  const d = laid.drawables.find((x) => x.family === 'leaving');
  if (!d) { console.error('no leaving drawable; verdicts: ' + JSON.stringify(laid.verdicts)); process.exit(1); }
  if (d.label !== 'LEAVING SOON') { console.error('leaving drawable label: ' + JSON.stringify(d.label)); process.exit(1); }
  const W = poster.width, H = poster.height;
  const drawables = laid.drawables.map((x) => ({
    signature: x,
    rect: { x: x.x * W, y: x.y * H, w: x.wFrac * W, h: x.hFrac ? x.hFrac * H : x.wFrac * W * 0.286, opacity: 1 },
  }));
  const cv = new skia.Canvas(W, H);
  CMP.drawComposite(cv, poster, drawables);
  const buf = await cv.toBuffer('png');
  fs.writeFileSync(process.argv[6], buf);
  console.log('rendered ' + buf.length + ' bytes, banner at x=' + d.x.toFixed(3) + ' y=' + d.y.toFixed(3));
})().catch((e) => { console.error('RENDER FAILED: ' + (e.stack || e.message)); process.exit(1); });
`

func TestLiveLeavingSoon(t *testing.T) {
	ts, _ := liveAppServer(t)
	key := os.Getenv("PCM_LIVE_APPLY_KEY")
	if key == "" {
		t.Skip("PCM_LIVE_APPLY_KEY not set — live leaving test skipped")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available — the composite render step cannot run")
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
		t.Fatal("item has no poster on the server")
	}

	// 1 — set the date through the API, 10 days out from the app's
	// local date.
	date := time.Now().AddDate(0, 0, 10).Format("2006-01-02")
	var setEnv struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Entries []struct {
			RatingKey string `json:"ratingKey"`
			Date      string `json:"date"`
			Days      int    `json:"days"`
			Expired   bool   `json:"expired"`
		} `json:"entries"`
	}
	rpc(t, ts, "leaving/set", map[string]any{
		"items": []any{map[string]any{"ratingKey": key, "title": "Live Leaving"}},
		"date":  date,
	}, &setEnv)
	if !setEnv.OK {
		t.Fatalf("leaving/set failed: %s", setEnv.Error)
	}
	if len(setEnv.Entries) != 1 || setEnv.Entries[0].Date != date || setEnv.Entries[0].Days != 10 || setEnv.Entries[0].Expired {
		t.Fatalf("entries after set = %+v, want the one entry at %s, 10 days, not expired", setEnv.Entries, date)
	}

	// 2 — the facts carry the entry.
	facts := postFacts(t, ts, key)
	if facts["leavingSet"] != true || facts["leavingDate"] != date || facts["leavingDays"] != float64(10) {
		t.Fatalf("facts = set %v / date %v / days %v, want true / %s / 10",
			facts["leavingSet"], facts["leavingDate"], facts["leavingDays"], date)
	}

	// 3 — render the real composite from those facts and apply it.
	dir := t.TempDir()
	posterPath := filepath.Join(dir, "poster.png")
	if err := os.WriteFile(posterPath, before, 0o644); err != nil {
		t.Fatal(err)
	}
	factsRaw, _ := json.Marshal(facts)
	factsPath := filepath.Join(dir, "facts.json")
	if err := os.WriteFile(factsPath, factsRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(dir, "render.cjs")
	if err := os.WriteFile(scriptPath, []byte(renderLeavingCompositeJS), 0o644); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	outPath := filepath.Join(dir, "composite.png")
	absComposite, err := filepath.Abs(filepath.Join("..", "..", "web", "composite.js"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, scriptPath,
		absComposite,
		filepath.Join(home, "local", "sigproof", "node_modules", "skia-canvas"),
		posterPath, factsPath, outPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("composite render failed: %v\n%s", err, out)
	}
	t.Logf("composite render: %s", bytes.TrimSpace(out))
	composite, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(composite, before) {
		t.Fatal("rendered composite is byte-identical to the bare poster — the banner did not draw")
	}

	var applyEnv struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Record struct {
			RunID     string `json:"runId"`
			OKCount   int    `json:"okCount"`
			FailCount int    `json:"failCount"`
		} `json:"record"`
	}
	job := map[string]any{
		"ratingKey": key, "title": "Live Leaving", "thumb": "/library/metadata/" + key + "/thumb",
		"pngBase64": base64.StdEncoding.EncodeToString(composite),
	}
	rpc(t, ts, "overlay/apply", map[string]any{
		"jobs":     []any{job},
		"badgeSet": json.RawMessage(`{"name":"live-leaving","placements":[]}`),
		"target":   json.RawMessage(`{"type":"item","count":1}`),
	}, &applyEnv)
	if !applyEnv.OK || applyEnv.Record.OKCount != 1 {
		t.Fatalf("apply failed: ok=%v err=%s record=%+v", applyEnv.OK, applyEnv.Error, applyEnv.Record)
	}
	after := posterBytes()
	if bytes.Equal(before, after) {
		t.Fatal("poster bytes on the server did NOT change after applying the banner composite")
	}
	t.Logf("poster changed on server: %d -> %d bytes (composite %d bytes), run %s",
		len(before), len(after), len(composite), applyEnv.Record.RunID)

	// 4 — restore returns the original poster.
	var restoreEnv struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	rpc(t, ts, "overlay/restore", map[string]any{"ratingKey": key, "runId": applyEnv.Record.RunID}, &restoreEnv)
	if !restoreEnv.OK {
		t.Fatalf("restore failed: %s", restoreEnv.Error)
	}
	if restored := posterBytes(); !bytes.Equal(before, restored) {
		t.Fatalf("poster after restore differs from the original (%d vs %d bytes)", len(restored), len(before))
	}
	t.Logf("restore verified: original poster bytes are back")

	// 5 — removing the entry returns the facts to unset.
	var removeEnv struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	rpc(t, ts, "leaving/remove", map[string]any{"ratingKey": key}, &removeEnv)
	if !removeEnv.OK {
		t.Fatalf("leaving/remove failed: %s", removeEnv.Error)
	}
	facts = postFacts(t, ts, key)
	if facts["leavingSet"] != false || facts["leavingDate"] != nil || facts["leavingDays"] != nil {
		t.Fatalf("facts after remove = set %v / date %v / days %v, want the unset triple",
			facts["leavingSet"], facts["leavingDate"], facts["leavingDays"])
	}
	t.Logf("remove verified: facts are unset again")
}
