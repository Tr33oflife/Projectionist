package api

// episode_facts_test.go — episode-level facts, end to end through
// the REAL facts handler against a fake PMS (v1.10 episode
// overlays, EPISODE-SEASON-OVERLAYS-PLAN phase 2). Unlike seasons,
// an episode's facts need NO aggregation: the streams live on the
// episode record itself, and the generic per-item parse reads them.
// Two episodes with different streams must judge differently, and
// identity inputs on the episode record (studio, genres, edition
// title, a source tag in its file name) must be suppressed at the
// facts layer. The episode's own Rating[] is NOT suppressed — the
// ratings stay in the facts as the record's own values; the
// composite layer's episode gate is what keeps the ratings family
// dark for episodes (asserted in web/composite.test.cjs).

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeEpisodePMS(t *testing.T) *httptest.Server {
	t.Helper()
	video := func(codec string) string {
		return `{"streamType":1,"codec":"` + codec + `"}`
	}
	audio := func(codec, extTitle string, channels int, lang string) string {
		return `{"streamType":2,"codec":"` + codec + `","extendedDisplayTitle":"` + extTitle +
			`","channels":` + itoa(channels) + `,"languageCode":"` + lang + `"}`
	}
	sub := func(lang string, forced bool) string {
		s := `{"streamType":3,"languageCode":"` + lang + `"`
		if forced {
			s += `,"forced":true`
		}
		return s + `}`
	}
	episode := func(rk, title, res string, width, durMs, viewCount int, file string, extra string, streams ...string) string {
		return `{"ratingKey":"` + rk + `","type":"episode","title":"` + title +
			`","index":1,"parentIndex":1,"duration":` + itoa(durMs) + `,"viewCount":` + itoa(viewCount) +
			`,"year":2024` + extra +
			`,"Media":[{"videoResolution":"` + res + `","width":` + itoa(width) +
			`,"Part":[{"file":"` + file + `","Stream":[` + strings.Join(streams, ",") + `]}]}]}`
	}
	meta := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"MediaContainer":{"Metadata":[`+body+`]}}`)
	}
	mux := http.NewServeMux()

	// Episode 810: the strong one, baited with identity fields — a
	// studio, genre tags, an edition title, a content rating, its
	// own IMDb rating, and a source token in the file name.
	mux.HandleFunc("/library/metadata/810", func(w http.ResponseWriter, r *http.Request) {
		meta(w, episode("810", "The Strong One", "1080", 1920, 2520000, 0,
			"/media/Two Season Show - S01E01.WEB-DL.mkv",
			`,"studio":"Episode Studio","contentRating":"TV-MA","editionTitle":"Extended",`+
				`"Genre":[{"tag":"Drama"}],`+
				`"Rating":[{"image":"imdb://image.rating","type":"audience","value":9.1}],"Guid":[]`,
			video("hevc"),
			audio("truehd", "English (TrueHD Atmos 7.1)", 8, "eng"),
			audio("aac", "Japanese (AAC 2.0)", 2, "jpn"),
			sub("eng", true)))
	})
	// Episode 820: a plain one — weaker video, one audio track, no
	// subtitles, already watched.
	mux.HandleFunc("/library/metadata/820", func(w http.ResponseWriter, r *http.Request) {
		meta(w, episode("820", "The Plain One", "720", 1280, 1440000, 2,
			"/media/ep820.mkv", `,"Guid":[]`,
			video("h264"),
			audio("aac", "English (AAC 2.0)", 2, "eng")))
	})
	return httptest.NewServer(mux)
}

func TestEpisodeFactsReadOwnStreams(t *testing.T) {
	pms := fakeEpisodePMS(t)
	defer pms.Close()
	app := appForFakePMS(t, pms.URL)
	defer app.Close()

	f := postFacts(t, app, "810")
	if f["type"] != "episode" {
		t.Fatalf("type = %v, want episode", f["type"])
	}
	// The episode's OWN streams, via the generic per-item parse —
	// no aggregation involved (langsFromEpisodes stays unset).
	if f["resolutionTier"] != "1080p" || f["videoCodec"] != "hevc" {
		t.Errorf("tier/codec = %v/%v, want 1080p/hevc (the episode's own video stream)", f["resolutionTier"], f["videoCodec"])
	}
	if f["atmos"] != true || f["truehd"] != true {
		t.Errorf("atmos/truehd = %v/%v, want true/true", f["atmos"], f["truehd"])
	}
	if f["bestAudioLabel"] != "English (TrueHD Atmos 7.1)" || f["bestAudioChannels"] != float64(8) {
		t.Errorf("best audio = %v / %v, want the episode's TrueHD Atmos stream", f["bestAudioLabel"], f["bestAudioChannels"])
	}
	langs, _ := f["audioLangs"].([]any)
	if len(langs) != 2 || langs[0] != "eng" || langs[1] != "jpn" {
		t.Errorf("audioLangs = %v, want [eng jpn] (the episode's own two audio streams)", f["audioLangs"])
	}
	if f["dubClass"] != "dual" || f["audioTracks"] != float64(2) {
		t.Errorf("dubClass/audioTracks = %v/%v, want dual/2", f["dubClass"], f["audioTracks"])
	}
	if f["hasSubs"] != true || f["subtitleCount"] != float64(1) || f["subtitleForced"] != true {
		t.Errorf("subs = has %v count %v forced %v, want true/1/true", f["hasSubs"], f["subtitleCount"], f["subtitleForced"])
	}
	// Runtime and watch state from the episode record itself.
	if f["runtimeMinutes"] != float64(42) {
		t.Errorf("runtimeMinutes = %v, want 42 (2520000 ms rounded)", f["runtimeMinutes"])
	}
	if f["unwatchedCount"] != float64(1) {
		t.Errorf("unwatchedCount = %v, want 1 (viewCount 0)", f["unwatchedCount"])
	}
	// Identity inputs, suppressed even though the record carries
	// them — the file name alone would otherwise yield WEB-DL.
	if f["studio"] != nil {
		t.Errorf("studio = %v, want nil (identity — excluded for episodes)", f["studio"])
	}
	if genres, _ := f["genres"].([]any); len(genres) != 0 {
		t.Errorf("genres = %v, want [] (excluded for episodes)", f["genres"])
	}
	if f["editionTitle"] != nil || f["sourceTag"] != nil || f["awardsText"] != nil || f["streamingByRegion"] != nil || f["tvStatus"] != nil {
		t.Errorf("identity facts must be nil for an episode: edition=%v source=%v awards=%v streaming=%v status=%v",
			f["editionTitle"], f["sourceTag"], f["awardsText"], f["streamingByRegion"], f["tvStatus"])
	}
	// The episode's OWN rating stays in the facts (the record's own
	// value, sourced from Plex). It never becomes a badge: the
	// composite episode gate holds the ratings family dark by rule.
	if f["imdbRating"] != 9.1 || f["imdbRatingSource"] != "plex" {
		t.Errorf("imdbRating = %v (source %v), want 9.1 via plex (present in facts; gated in composite)", f["imdbRating"], f["imdbRatingSource"])
	}
}

func TestEpisodeFactsSecondEpisodeJudgesDifferently(t *testing.T) {
	pms := fakeEpisodePMS(t)
	defer pms.Close()
	app := appForFakePMS(t, pms.URL)
	defer app.Close()

	f := postFacts(t, app, "820")
	if f["resolutionTier"] != "720p" || f["videoCodec"] != "h264" {
		t.Errorf("tier/codec = %v/%v, want 720p/h264 (this episode's own streams, not ep 810's)", f["resolutionTier"], f["videoCodec"])
	}
	if f["atmos"] != false || f["truehd"] != false {
		t.Errorf("atmos/truehd = %v/%v, want false/false", f["atmos"], f["truehd"])
	}
	langs, _ := f["audioLangs"].([]any)
	if len(langs) != 1 || langs[0] != "eng" {
		t.Errorf("audioLangs = %v, want [eng] only", f["audioLangs"])
	}
	if f["dubClass"] != "dub" {
		t.Errorf("dubClass = %v, want dub (single eng track)", f["dubClass"])
	}
	if f["hasSubs"] != false {
		t.Errorf("hasSubs = %v, want false (no subtitle streams on this episode)", f["hasSubs"])
	}
	if f["runtimeMinutes"] != float64(24) {
		t.Errorf("runtimeMinutes = %v, want 24", f["runtimeMinutes"])
	}
	if f["unwatchedCount"] != float64(0) {
		t.Errorf("unwatchedCount = %v, want 0 (viewCount 2 — watched)", f["unwatchedCount"])
	}
}
