package api

// item_facts_new_test.go — the badge-pack facts end to end through the
// REAL facts handler against a fake PMS: videoCodec (tier-linked),
// studio, genres, and the EPISODE unwatchedCount (viewCount 0 → 1,
// watched → 0), which the watch-status family judges against.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakePMSBadgePack(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	serve := func(key, record string) {
		mux.HandleFunc("/library/metadata/"+key, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"MediaContainer":{"Metadata":[`+record+`]}}`)
		})
	}
	serve("700", `{"ratingKey":"700","type":"movie","title":"Codec Movie","year":2019,
		"studio":"  A24  ","viewCount":0,"Guid":[],
		"Genre":[{"tag":"Horror"},{"tag":"Thriller"}],
		"Media":[
			{"width":1920,"videoResolution":"1080","Part":[{"Stream":[{"streamType":1,"codec":"h264"}]}]},
			{"width":3840,"videoResolution":"4k","Part":[{"Stream":[
				{"streamType":1,"codec":"hevc"},
				{"streamType":2,"codec":"aac","channels":2,"languageCode":"eng"}
			]}]}
		]}`)
	serve("701", `{"ratingKey":"701","type":"episode","title":"Unwatched Ep","viewCount":0,"Guid":[]}`)
	serve("702", `{"ratingKey":"702","type":"episode","title":"Watched Ep","viewCount":3,"Guid":[]}`)
	return httptest.NewServer(mux)
}

func TestItemFactsBadgePackEndToEnd(t *testing.T) {
	pms := fakePMSBadgePack(t)
	defer pms.Close()
	app := appForFakePMS(t, pms.URL)
	defer app.Close()

	f := postFacts(t, app, "700")
	// Two versions: the 4K HEVC file wins the tier, so its codec is
	// the fact — not the H.264 of the 1080p version listed first.
	if f["videoCodec"] != "hevc" {
		t.Errorf("videoCodec = %v, want hevc (tier-linked)", f["videoCodec"])
	}
	if f["resolutionTier"] != "4K" {
		t.Errorf("resolutionTier = %v, want 4K", f["resolutionTier"])
	}
	if f["studio"] != "A24" {
		t.Errorf("studio = %v, want A24 (trimmed)", f["studio"])
	}
	genres, _ := f["genres"].([]any)
	gs := make([]string, 0, len(genres))
	for _, g := range genres {
		gs = append(gs, g.(string))
	}
	if strings.Join(gs, ",") != "Horror,Thriller" {
		t.Errorf("genres = %v, want [Horror Thriller] in Plex order", f["genres"])
	}
	if f["unwatchedCount"] != float64(1) {
		t.Errorf("movie unwatchedCount = %v, want 1 (viewCount 0)", f["unwatchedCount"])
	}
	// No OMDb/TMDb keys are configured in the test server: the
	// key-gated facts stay null rather than erroring the whole call.
	if f["awardsText"] != nil {
		t.Errorf("awardsText = %v, want nil without an OMDb key", f["awardsText"])
	}
	if f["streamingByRegion"] != nil {
		t.Errorf("streamingByRegion = %v, want nil without a TMDb key", f["streamingByRegion"])
	}

	// Episodes carry the same watched arithmetic as movies.
	if f := postFacts(t, app, "701"); f["unwatchedCount"] != float64(1) {
		t.Errorf("episode unwatchedCount = %v, want 1 (never played)", f["unwatchedCount"])
	}
	if f := postFacts(t, app, "702"); f["unwatchedCount"] != float64(0) {
		t.Errorf("episode unwatchedCount = %v, want 0 (played)", f["unwatchedCount"])
	}
}
