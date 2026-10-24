package api

// season_facts_test.go — season-level facts, end to end through the
// REAL facts handler against a fake PMS (v1.9 season overlays,
// EPISODE-SEASON-OVERLAYS-PLAN phase 1). The fake mimics a real PMS:
// season records carry NO streams — streams exist only on the episode
// records, reached via the season's children (keys only) + the
// comma-batch record fetch. (Measured on a real PMS, allLeaves
// flattens shows only and answers empty for a season — the fake
// mirrors the real shape: episodes under /children.)
//
// Fixture — one show, two seasons whose episodes DISAGREE, so any
// leak between seasons is visible in the assertions:
//   Season 710 (of show 700): 3 eps — two 1080p/hevc + one 720p/h264;
//     best audio TrueHD Atmos eng; subs eng (one forced); runtimes
//     60+60+50 min; the season record itself carries an IMDb rating,
//     a content rating, a studio and genre tags (the last two are
//     show identity and must be suppressed).
//   Season 720: 2 eps — both 4K/hevc with Dolby Vision, jpn audio
//     only; the record carries NO ratings at all.
//   Season 730: codec tie at the winning tier (h264 vs hevc, one
//     episode each) — first episode wins.
//   Season 740: tier tie (720p vs 4K, one each) — higher tier wins.
//   Season 760: leaves endpoint 404s — degrade like a broken show,
//     with the record's own facts intact.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeSeasonPMS(t *testing.T) *httptest.Server {
	t.Helper()
	video := func(codec string, dv bool) string {
		s := `{"streamType":1,"codec":"` + codec + `"`
		if dv {
			s += `,"DOVIPresent":true,"colorTrc":"smpte2084"`
		}
		return s + `}`
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
	episode := func(rk, res string, width int, durMs, viewCount int, streams ...string) string {
		return `{"ratingKey":"` + rk + `","type":"episode","title":"Ep ` + rk +
			`","duration":` + itoa(durMs) + `,"viewCount":` + itoa(viewCount) +
			`,"Media":[{"videoResolution":"` + res + `","width":` + itoa(width) +
			`,"Part":[{"file":"/media/ep` + rk + `.mkv","Stream":[` + strings.Join(streams, ",") + `]}]}]}`
	}
	leaves := func(keys ...string) string {
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, `{"ratingKey":"`+k+`","type":"episode","title":"Ep `+k+`"}`)
		}
		return `{"MediaContainer":{"totalSize":` + itoa(len(keys)) + `,"Metadata":[` + strings.Join(parts, ",") + `]}}`
	}
	meta := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"MediaContainer":{"Metadata":[`+body+`]}}`)
	}
	mux := http.NewServeMux()

	// ---- Season 710 ------------------------------------------------------
	mux.HandleFunc("/library/metadata/710", func(w http.ResponseWriter, r *http.Request) {
		meta(w, `{"ratingKey":"710","type":"season","title":"Season 1","parentTitle":"Two Season Show","index":1,`+
			`"leafCount":3,"viewedLeafCount":1,"year":2019,"contentRating":"TV-MA","studio":"Show Studio",`+
			`"addedAt":1790000000,"Genre":[{"tag":"Drama"}],`+
			`"Rating":[{"image":"imdb://image.rating","type":"audience","value":8.4}],"Guid":[]}`)
	})
	mux.HandleFunc("/library/metadata/710/children", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, leaves("711", "712", "713"))
	})
	mux.HandleFunc("/library/metadata/711,712,713", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		eps := []string{
			episode("711", "1080", 1920, 3600000, 1,
				video("hevc", false),
				audio("truehd", "English (TrueHD Atmos 7.1)", 8, "eng"),
				sub("eng", false)),
			episode("712", "1080", 1920, 3600000, 0,
				video("hevc", false),
				audio("eac3", "English (EAC3 5.1)", 6, "eng"),
				audio("aac", "Japanese (AAC 2.0)", 2, "jpn"),
				sub("eng", true)),
			episode("713", "720", 1280, 3000000, 0,
				video("h264", false),
				audio("aac", "Japanese (AAC 2.0)", 2, "jpn")),
		}
		io.WriteString(w, `{"MediaContainer":{"size":3,"Metadata":[`+strings.Join(eps, ",")+`]}}`)
	})

	// ---- Season 720 ------------------------------------------------------
	mux.HandleFunc("/library/metadata/720", func(w http.ResponseWriter, r *http.Request) {
		meta(w, `{"ratingKey":"720","type":"season","title":"Season 2","parentTitle":"Two Season Show","index":2,`+
			`"leafCount":2,"viewedLeafCount":2,"year":2021,"addedAt":1790500000,"Guid":[]}`)
	})
	mux.HandleFunc("/library/metadata/720/children", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, leaves("721", "722"))
	})
	mux.HandleFunc("/library/metadata/721,722", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		eps := []string{
			episode("721", "4k", 3840, 1800000, 3,
				video("hevc", true),
				audio("dca", "Japanese (DTS-HD MA 5.1)", 6, "jpn")),
			episode("722", "4k", 3840, 1800000, 1,
				video("hevc", true),
				audio("aac", "Japanese (AAC 2.0)", 2, "jpn")),
		}
		io.WriteString(w, `{"MediaContainer":{"size":2,"Metadata":[`+strings.Join(eps, ",")+`]}}`)
	})

	// ---- Season 730: codec tie at the winning tier -----------------------
	mux.HandleFunc("/library/metadata/730", func(w http.ResponseWriter, r *http.Request) {
		meta(w, `{"ratingKey":"730","type":"season","title":"Season 1","index":1,"leafCount":2,"viewedLeafCount":0,"Guid":[]}`)
	})
	mux.HandleFunc("/library/metadata/730/children", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, leaves("731", "732"))
	})
	mux.HandleFunc("/library/metadata/731,732", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		eps := []string{
			episode("731", "1080", 1920, 1200000, 0, video("h264", false), audio("aac", "English (AAC 2.0)", 2, "eng")),
			episode("732", "1080", 1920, 1200000, 0, video("hevc", false), audio("aac", "English (AAC 2.0)", 2, "eng")),
		}
		io.WriteString(w, `{"MediaContainer":{"size":2,"Metadata":[`+strings.Join(eps, ",")+`]}}`)
	})

	// ---- Season 740: tier tie --------------------------------------------
	mux.HandleFunc("/library/metadata/740", func(w http.ResponseWriter, r *http.Request) {
		meta(w, `{"ratingKey":"740","type":"season","title":"Season 1","index":1,"leafCount":2,"viewedLeafCount":0,"Guid":[]}`)
	})
	mux.HandleFunc("/library/metadata/740/children", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, leaves("741", "742"))
	})
	mux.HandleFunc("/library/metadata/741,742", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		eps := []string{
			episode("741", "720", 1280, 1200000, 0, video("h264", false), audio("aac", "English (AAC 2.0)", 2, "eng")),
			episode("742", "4k", 3840, 1200000, 0, video("hevc", false), audio("aac", "English (AAC 2.0)", 2, "eng")),
		}
		io.WriteString(w, `{"MediaContainer":{"size":2,"Metadata":[`+strings.Join(eps, ",")+`]}}`)
	})

	// ---- Season 760: leaves broken ---------------------------------------
	mux.HandleFunc("/library/metadata/760", func(w http.ResponseWriter, r *http.Request) {
		meta(w, `{"ratingKey":"760","type":"season","title":"Season 3","index":3,"leafCount":9,"viewedLeafCount":4,`+
			`"Rating":[{"image":"imdb://image.rating","type":"audience","value":7.1}],"Guid":[]}`)
	})
	mux.HandleFunc("/library/metadata/760/children", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	})
	return httptest.NewServer(mux)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	b := [12]byte{}
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func TestSeasonFactsAggregateOwnEpisodesOnly(t *testing.T) {
	pms := fakeSeasonPMS(t)
	defer pms.Close()
	app := appForFakePMS(t, pms.URL)
	defer app.Close()

	f := postFacts(t, app, "710")
	if f["type"] != "season" {
		t.Fatalf("type = %v, want season", f["type"])
	}
	if f["langsFromEpisodes"] != true {
		t.Fatalf("langsFromEpisodes = %v, want true (langsError=%v)", f["langsFromEpisodes"], f["langsError"])
	}
	// Dominant aggregation over THIS season's episodes only:
	// two 1080p beat one 720p; season 720's 4K/DV must not leak in.
	if f["resolutionTier"] != "1080p" {
		t.Errorf("resolutionTier = %v, want 1080p (dominant of 1080/1080/720)", f["resolutionTier"])
	}
	if f["videoCodec"] != "hevc" {
		t.Errorf("videoCodec = %v, want hevc (codec of the winning tier)", f["videoCodec"])
	}
	if f["dv"] != false || f["hdrAny"] != false {
		t.Errorf("dv/hdrAny = %v/%v, want false/false (no HDR in season 1; season 2's DV must not leak)", f["dv"], f["hdrAny"])
	}
	if f["atmos"] != true || f["truehd"] != true {
		t.Errorf("atmos/truehd = %v/%v, want true/true (episode 711's TrueHD Atmos)", f["atmos"], f["truehd"])
	}
	if f["bestAudioLabel"] != "English (TrueHD Atmos 7.1)" || f["bestAudioChannels"] != float64(8) {
		t.Errorf("best audio = %v / %v, want the TrueHD Atmos stream of ep 711", f["bestAudioLabel"], f["bestAudioChannels"])
	}
	langs, _ := f["audioLangs"].([]any)
	if len(langs) != 2 || langs[0] != "eng" || langs[1] != "jpn" {
		t.Errorf("audioLangs = %v, want [eng jpn] (union across the season)", f["audioLangs"])
	}
	if f["dubClass"] != "dual" {
		t.Errorf("dubClass = %v, want dual", f["dubClass"])
	}
	if f["hasSubs"] != true || f["subtitleCount"] != float64(2) || f["subtitleForced"] != true {
		t.Errorf("subs = has %v count %v forced %v, want true/2/true", f["hasSubs"], f["subtitleCount"], f["subtitleForced"])
	}
	// Runtime = the season's total: (60+60+50 min) rounded once = 170.
	if f["runtimeMinutes"] != float64(170) {
		t.Errorf("runtimeMinutes = %v, want 170 (summed episode durations)", f["runtimeMinutes"])
	}
	// Watch status from the RECORD's leaf arithmetic (3 − 1).
	if f["unwatchedCount"] != float64(2) {
		t.Errorf("unwatchedCount = %v, want 2 (leafCount − viewedLeafCount)", f["unwatchedCount"])
	}
	if f["episodeCount"] != float64(3) || f["episodeTotal"] != float64(3) {
		t.Errorf("episodeCount/Total = %v/%v, want 3/3", f["episodeCount"], f["episodeTotal"])
	}
	// The season's OWN ratings: present, and sourced from Plex.
	if f["imdbRating"] != 8.4 || f["imdbRatingSource"] != "plex" {
		t.Errorf("imdbRating = %v (source %v), want 8.4 via plex (the season record's own Rating[])", f["imdbRating"], f["imdbRatingSource"])
	}
	if f["contentRating"] != "TV-MA" {
		t.Errorf("contentRating = %v, want TV-MA (the season record's own field)", f["contentRating"])
	}
	// Show identity, suppressed even though the record carries it.
	if f["studio"] != nil {
		t.Errorf("studio = %v, want nil (show identity — excluded for seasons)", f["studio"])
	}
	if genres, _ := f["genres"].([]any); len(genres) != 0 {
		t.Errorf("genres = %v, want [] (excluded for seasons)", f["genres"])
	}
	if f["awardsText"] != nil || f["editionTitle"] != nil || f["sourceTag"] != nil || f["tvStatus"] != nil || f["streamingByRegion"] != nil {
		t.Errorf("identity facts must all be nil for a season: awards=%v edition=%v source=%v status=%v streaming=%v",
			f["awardsText"], f["editionTitle"], f["sourceTag"], f["tvStatus"], f["streamingByRegion"])
	}
	if f["mediaCount"] != float64(0) {
		t.Errorf("mediaCount = %v, want 0 (Versions is not a season fact)", f["mediaCount"])
	}
}

func TestSeasonFactsSecondSeasonAndNoRatings(t *testing.T) {
	pms := fakeSeasonPMS(t)
	defer pms.Close()
	app := appForFakePMS(t, pms.URL)
	defer app.Close()

	f := postFacts(t, app, "720")
	if f["resolutionTier"] != "4K" || f["dv"] != true || f["hdr10"] != false {
		t.Errorf("tier/dv/hdr10 = %v/%v/%v, want 4K/true/false (both episodes are DV)", f["resolutionTier"], f["dv"], f["hdr10"])
	}
	if f["atmos"] != false {
		t.Errorf("atmos = %v, want false (season 1's Atmos must not leak into season 2)", f["atmos"])
	}
	if f["dtshdma"] != true {
		t.Errorf("dtshdma = %v, want true (episode 721's DTS-HD MA)", f["dtshdma"])
	}
	langs, _ := f["audioLangs"].([]any)
	if len(langs) != 1 || langs[0] != "jpn" {
		t.Errorf("audioLangs = %v, want [jpn] only", f["audioLangs"])
	}
	if f["dubClass"] != "sub" {
		t.Errorf("dubClass = %v, want sub (jpn only)", f["dubClass"])
	}
	if f["runtimeMinutes"] != float64(60) {
		t.Errorf("runtimeMinutes = %v, want 60 (30+30)", f["runtimeMinutes"])
	}
	if f["unwatchedCount"] != float64(0) {
		t.Errorf("unwatchedCount = %v, want 0 (fully watched season)", f["unwatchedCount"])
	}
	// The record carries NO ratings: every rating fact stays null —
	// hidden, never inherited from the show or the other season.
	for _, k := range []string{"imdbRating", "tmdbRating", "rtRating", "rtAudience", "metascore", "contentRating"} {
		if f[k] != nil {
			t.Errorf("%s = %v, want nil (season 720's record states none)", k, f[k])
		}
	}
}

func TestSeasonFactsDominanceTies(t *testing.T) {
	pms := fakeSeasonPMS(t)
	defer pms.Close()
	app := appForFakePMS(t, pms.URL)
	defer app.Close()

	// Codec tie at the winning tier: one h264 + one hevc episode at
	// 1080p — the FIRST episode's codec wins, deterministically.
	f := postFacts(t, app, "730")
	if f["resolutionTier"] != "1080p" || f["videoCodec"] != "h264" {
		t.Errorf("730: tier/codec = %v/%v, want 1080p/h264 (first-seen wins a codec tie)", f["resolutionTier"], f["videoCodec"])
	}
	// Tier tie: one 720p + one 4K — the higher tier wins.
	f = postFacts(t, app, "740")
	if f["resolutionTier"] != "4K" || f["videoCodec"] != "hevc" {
		t.Errorf("740: tier/codec = %v/%v, want 4K/hevc (higher tier wins a tie)", f["resolutionTier"], f["videoCodec"])
	}
}

func TestSeasonFactsLeavesFailureDegrades(t *testing.T) {
	pms := fakeSeasonPMS(t)
	defer pms.Close()
	app := appForFakePMS(t, pms.URL)
	defer app.Close()

	f := postFacts(t, app, "760")
	if f["langsFromEpisodes"] != false {
		t.Errorf("langsFromEpisodes = %v, want false when leaves fail", f["langsFromEpisodes"])
	}
	if errStr, _ := f["langsError"].(string); errStr == "" {
		t.Errorf("langsError must name the failure, got %v", f["langsError"])
	}
	// Episode-derived facts stay empty (never guessed)…
	if f["resolutionTier"] != nil || f["dubClass"] != "unknown" {
		t.Errorf("resolutionTier/dubClass = %v/%v, want nil/unknown on degradation", f["resolutionTier"], f["dubClass"])
	}
	// …while the season record's OWN facts survive: its IMDb rating
	// and its leaf-arithmetic watch count.
	if f["imdbRating"] != 7.1 {
		t.Errorf("imdbRating = %v, want 7.1 (the record's own Rating[])", f["imdbRating"])
	}
	if f["unwatchedCount"] != float64(5) {
		t.Errorf("unwatchedCount = %v, want 5 (9 − 4 from the record)", f["unwatchedCount"])
	}
}
