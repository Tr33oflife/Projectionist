package api

// Live-manager E2E — SKIPPED unless PCM_LIVE_PLEX points at the
// throwaway sandbox PMS (see live_art_test.go for the rig). Exercises
// the v1.13 manager endpoints against the real server: the health
// read over the known sandbox collections, a verified metadata save
// on a fresh collection, bulk art (the REAL TPDb flow) on two fresh
// catalog-titled collections with read-back proof, and the
// poster-from-URL path (best effort: the sandbox server fetches the
// URL itself, so an unreachable image host skips rather than fails).
//
//	PCM_LIVE_PLEX=http://127.0.0.1:32400 PCM_LIVE_MACHINE=<id> \
//	  go test ./internal/api/ -run LiveManager -v

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func createSandboxCollection(t *testing.T, app *httptest.Server, title string) string {
	t.Helper()
	var out struct {
		OK         bool   `json:"ok"`
		Error      string `json:"error"`
		Collection struct {
			RatingKey string `json:"ratingKey"`
		} `json:"collection"`
	}
	rpc(t, app, "plex/createCollection", map[string]any{
		"sectionKey": "1", "type": 1, "title": title, "itemRatingKeys": []string{"1"},
	}, &out)
	if !out.OK || out.Collection.RatingKey == "" {
		t.Fatalf("create %q: %+v", title, out)
	}
	return out.Collection.RatingKey
}

func TestLiveManagerEndpoints(t *testing.T) {
	app, _ := liveAppServer(t)

	// ---- health over the real sandbox library -------------------------
	var health struct {
		OK          bool `json:"ok"`
		Total       int  `json:"total"`
		Collections []struct {
			RatingKey string   `json:"ratingKey"`
			HasPoster bool     `json:"hasPoster"`
			Flags     []string `json:"flags"`
		} `json:"collections"`
	}
	rpc(t, app, "plex/collectionHealth", map[string]any{"sectionKey": "1"}, &health)
	if !health.OK || health.Total == 0 {
		t.Fatalf("health: %+v", health)
	}
	byKey := map[string]int{}
	for i, c := range health.Collections {
		byKey[c.RatingKey] = i
	}
	for _, rk := range []string{"4", "5", "6"} {
		if _, ok := byKey[rk]; !ok {
			t.Fatalf("sandbox collection rk%s missing from health read", rk)
		}
	}
	// rk5/rk6 wear Plex composite thumbs — no custom poster.
	for _, rk := range []string{"5", "6"} {
		c := health.Collections[byKey[rk]]
		if c.HasPoster {
			t.Errorf("rk%s hasPoster = true, want false (composite)", rk)
		}
		found := false
		for _, f := range c.Flags {
			if f == "NO POSTER" {
				found = true
			}
		}
		if !found {
			t.Errorf("rk%s flags = %v, want NO POSTER among them", rk, c.Flags)
		}
	}
	t.Logf("health: %d collections read live", health.Total)

	// ---- detail: visibility unreported + labels unexposed -------------
	var detailOut struct {
		OK     bool `json:"ok"`
		Detail struct {
			Visibility      string   `json:"visibility"`
			VisibilityKnown bool     `json:"visibilityKnown"`
			Labels          []string `json:"labels"`
		} `json:"detail"`
	}
	rpc(t, app, "plex/getCollectionDetail", map[string]any{"ratingKey": "6"}, &detailOut)
	if !detailOut.OK {
		t.Fatalf("detail: %+v", detailOut)
	}
	if detailOut.Detail.VisibilityKnown || detailOut.Detail.Visibility != "unknown" {
		t.Errorf("rk6 visibility = %q known=%v, want unknown/false on PMS 1.43",
			detailOut.Detail.Visibility, detailOut.Detail.VisibilityKnown)
	}
	if detailOut.Detail.Labels != nil {
		t.Errorf("rk6 labels = %v, want null (not exposed by this server)", detailOut.Detail.Labels)
	}

	// ---- verified save on a fresh collection ---------------------------
	rk := createSandboxCollection(t, app, fmt.Sprintf("PCM Manager API Live %d", time.Now().Unix()))
	var saveOut struct {
		OK     bool `json:"ok"`
		Detail struct {
			Title     string `json:"title"`
			TitleSort string `json:"titleSort"`
			Summary   string `json:"summary"`
		} `json:"detail"`
		Checks []struct {
			Field string `json:"field"`
			OK    bool   `json:"ok"`
			Note  string `json:"note"`
		} `json:"checks"`
	}
	rpc(t, app, "plex/updateCollectionMeta", map[string]any{
		"ratingKey": rk,
		"fields": map[string]any{
			"summary":   "Written through the manager API.",
			"titleSort": "AAA Manager Sort",
			"labels":    []string{"LiveLabel"},
		},
	}, &saveOut)
	if !saveOut.OK {
		t.Fatalf("verified save: %+v", saveOut)
	}
	held := map[string]bool{}
	notes := map[string]string{}
	for _, c := range saveOut.Checks {
		held[c.Field] = c.OK
		notes[c.Field] = c.Note
	}
	if !held["summary"] || !held["titleSort"] {
		t.Errorf("checks = %v, want summary+titleSort held", held)
	}
	if held["labels"] {
		t.Logf("note: this server took labels after all")
	} else if notes["labels"] == "" {
		t.Errorf("labels check not held but carries no explanation: %v", saveOut.Checks)
	}
	if saveOut.Detail.Summary != "Written through the manager API." || saveOut.Detail.TitleSort != "AAA Manager Sort" {
		t.Errorf("held detail = %+v", saveOut.Detail)
	}
}

func TestLiveManagerBulkArt(t *testing.T) {
	app, _ := liveAppServer(t)
	// The bulk flow resolves art through the real TPDb suggestions;
	// if this machine cannot reach the art hosts there is nothing to
	// prove against — skip honestly rather than fail on the network.
	var sug struct {
		OK    bool             `json:"ok"`
		Items []map[string]any `json:"items"`
	}
	rpc(t, app, "art/suggestions", map[string]any{"title": "Alien"}, &sug)
	if !sug.OK || len(sug.Items) == 0 {
		t.Skip("no TPDb suggestions available from here — bulk art live proof needs the art hosts reachable")
	}
	rkA := createSandboxCollection(t, app, "Alien")
	rkB := createSandboxCollection(t, app, "Marvel")

	before := map[string][2]string{}
	for _, rk := range []string{rkA, rkB} {
		var d struct {
			OK     bool `json:"ok"`
			Detail struct {
				Thumb string `json:"thumb"`
				Art   string `json:"art"`
			} `json:"detail"`
		}
		rpc(t, app, "plex/getCollectionDetail", map[string]any{"ratingKey": rk}, &d)
		before[rk] = [2]string{d.Detail.Thumb, d.Detail.Art}
	}

	var bulk struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Results []struct {
			RatingKey string `json:"ratingKey"`
			Title     string `json:"title"`
			Poster    *struct {
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			} `json:"poster"`
			Backdrop *struct {
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			} `json:"backdrop"`
		} `json:"results"`
	}
	rpc(t, app, "plex/collectionBulkArt", map[string]any{
		"ratingKeys": []string{rkA, rkB}, "poster": true, "backdrop": true,
	}, &bulk)
	if !bulk.OK {
		t.Fatalf("bulk art: %+v", bulk)
	}
	if len(bulk.Results) != 2 {
		t.Fatalf("bulk results = %d, want 2", len(bulk.Results))
	}
	for _, res := range bulk.Results {
		if res.Poster == nil || !res.Poster.OK {
			t.Errorf("%s poster = %+v, want ok", res.Title, res.Poster)
		}
		if res.Backdrop == nil || !res.Backdrop.OK {
			t.Errorf("%s backdrop = %+v, want ok", res.Title, res.Backdrop)
		}
		// Read-back proof beyond the handler's own word: the detail
		// read now shows a changed thumb AND a set backdrop.
		var d struct {
			OK     bool `json:"ok"`
			Detail struct {
				Thumb string `json:"thumb"`
				Art   string `json:"art"`
			} `json:"detail"`
		}
		rpc(t, app, "plex/getCollectionDetail", map[string]any{"ratingKey": res.RatingKey}, &d)
		b := before[res.RatingKey]
		if d.Detail.Thumb == b[0] || d.Detail.Thumb == "" {
			t.Errorf("%s thumb unchanged after bulk poster: %q", res.Title, d.Detail.Thumb)
		}
		if d.Detail.Art == "" || d.Detail.Art == b[1] {
			t.Errorf("%s art after bulk backdrop = %q (before %q)", res.Title, d.Detail.Art, b[1])
		}
		t.Logf("%s: thumb %s · art %s", res.Title, d.Detail.Thumb, d.Detail.Art)
	}

	// ---- poster from URL (best effort: the SERVER fetches the URL) ----
	imgURL, _ := sug.Items[0]["url"].(string)
	var urlOut struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Changed bool   `json:"changed"`
	}
	rpc(t, app, "plex/setCollectionPosterURL", map[string]any{"ratingKey": rkA, "url": imgURL}, &urlOut)
	if !urlOut.OK {
		t.Skipf("sandbox PMS could not ingest the poster URL %q itself (%s) — the ingest path is covered by the fake-PMS tests", imgURL, urlOut.Error)
	}
	if !urlOut.Changed {
		t.Errorf("poster URL accepted but the served thumb did not change")
	}
}
