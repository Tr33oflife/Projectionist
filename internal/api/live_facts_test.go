package api

// Live-PMS facts test — SKIPPED unless PCM_LIVE_PLEX points at a
// throwaway Plex Media Server (the sandbox PMS stood up 2026-10-20).
// Verifies overlay facts v2 against REAL Plex stream JSON: the crafted
// "HDR Test Movie" (x265, colorTrc smpte2084, EAC3 5.1) must produce
// resolutionTier "4K", hdr10 true, dv false, atmos false — the exact
// detections the owner's overlay badges now depend on. Mocked fixtures can
// prove the parser; only a real server proves Plex really spells the
// fields the way the parser (and Kometa) expects.
//
//	PCM_LIVE_PLEX=http://127.0.0.1:32400 PCM_LIVE_MACHINE=<id> \
//	  PCM_LIVE_HDR_KEY=<ratingKey of the crafted HDR item> \
//	  go test ./internal/api/ -run LiveFacts -v

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"plexcollectionmanager/internal/plex"
)

func TestLiveFactsHDR(t *testing.T) {
	base := os.Getenv("PCM_LIVE_PLEX")
	machine := os.Getenv("PCM_LIVE_MACHINE")
	hdrKey := os.Getenv("PCM_LIVE_HDR_KEY")
	if base == "" || machine == "" || hdrKey == "" {
		t.Skip("PCM_LIVE_PLEX / PCM_LIVE_MACHINE / PCM_LIVE_HDR_KEY not set — live facts test skipped")
	}
	root := filepath.Join("..", "..")
	catalog, err := os.ReadFile(filepath.Join(root, "assets", "collection-art.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(Config{
		DataDir:      t.TempDir(),
		Version:      "live-test",
		SessionToken: testToken,
		WebFS:        os.DirFS(filepath.Join(root, "web")),
		BadgesFS:     os.DirFS(filepath.Join(root, "assets", "badges")),
		CatalogJSON:  catalog,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.servers = []*plex.Server{{
		Name: "LiveTest", Machine: machine, Owned: true, Token: "livetest-dummy-token",
		Connections: []plex.Connection{{URI: base, Local: true}},
	}}
	srv.selectedMachine = machine
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/overlay/itemFacts", bytes.NewReader([]byte(`{"ratingKey":"`+hdrKey+`"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-PCM-Token", testToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var env struct {
		OK    bool           `json:"ok"`
		Error string         `json:"error"`
		Facts map[string]any `json:"facts"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode: %v — body: %s", err, raw)
	}
	if !env.OK {
		t.Fatalf("itemFacts failed: %s", env.Error)
	}
	f := env.Facts
	t.Logf("facts: %v", f)
	if f["resolutionTier"] != "4K" {
		t.Errorf("resolutionTier = %v, want 4K", f["resolutionTier"])
	}
	if f["hdr10"] != true {
		t.Errorf("hdr10 = %v, want true (colorTrc smpte2084)", f["hdr10"])
	}
	if f["hdrAny"] != true {
		t.Errorf("hdrAny = %v, want true", f["hdrAny"])
	}
	if f["dv"] != false {
		t.Errorf("dv = %v, want false (no DOVIPresent in stream)", f["dv"])
	}
	if f["atmos"] != false {
		t.Errorf("atmos = %v, want false (plain EAC3 5.1, no Atmos title)", f["atmos"])
	}
	if f["width"] == nil {
		t.Errorf("width fact missing")
	}
	// videoCodec (verified live 2026-10-22): the HDR fixture file is
	// x265, and PMS reports the winning Media's video codec as "hevc".
	if f["videoCodec"] != "hevc" {
		t.Errorf("videoCodec = %v, want hevc (x265 fixture, PMS-reported)", f["videoCodec"])
	}

	// SDR control (optional): a plain x264 file must NOT trip any HDR/DV
	// detection and must land in a low resolution tier.
	if sdrKey := os.Getenv("PCM_LIVE_SDR_KEY"); sdrKey != "" {
		req2, _ := http.NewRequest("POST", ts.URL+"/api/overlay/itemFacts", bytes.NewReader([]byte(`{"ratingKey":"`+sdrKey+`"}`)))
		req2.Header.Set("Content-Type", "application/json")
		req2.Header.Set("X-PCM-Token", testToken)
		res2, err := http.DefaultClient.Do(req2)
		if err != nil {
			t.Fatal(err)
		}
		defer res2.Body.Close()
		raw2, _ := io.ReadAll(res2.Body)
		var env2 struct {
			OK    bool           `json:"ok"`
			Error string         `json:"error"`
			Facts map[string]any `json:"facts"`
		}
		if err := json.Unmarshal(raw2, &env2); err != nil || !env2.OK {
			t.Fatalf("SDR itemFacts: err=%v ok=%v body=%s", err, env2.OK, raw2)
		}
		sf := env2.Facts
		t.Logf("SDR facts: tier=%v hdr10=%v hdrAny=%v dv=%v", sf["resolutionTier"], sf["hdr10"], sf["hdrAny"], sf["dv"])
		if sf["hdr10"] != false || sf["hdrAny"] != false || sf["dv"] != false {
			t.Errorf("SDR item must have no HDR/DV facts: hdr10=%v hdrAny=%v dv=%v", sf["hdr10"], sf["hdrAny"], sf["dv"])
		}
		if sf["resolutionTier"] == "4K" {
			t.Errorf("SDR item must not be tier 4K")
		}
		// The SDR control is an x264 file: PMS reports "h264".
		if sf["videoCodec"] != "h264" {
			t.Errorf("SDR videoCodec = %v, want h264 (x264 fixture, PMS-reported)", sf["videoCodec"])
		}
	}
}
