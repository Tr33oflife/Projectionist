package api

// badge_library_facts_test.go — the v1.11 badge-library facts, end
// to end through the REAL facts handler against a fake PMS (plus
// parser-level pins). The facts under test:
//   - aspectRatio rides WITH the winning-resolution Media entry
//     (Plex's own aspectRatio field; width/height of that entry as
//     the fallback) — a losing version's ratio never leaks in.
//   - bestAudioCodec: the raw codec of the best-scored audio stream
//     (the audio family's plain-codec resolution reads it).
//   - collections: the item's Plex Collection tags, movies/shows
//     only — episodes and seasons never carry membership.
//   - franchiseName / originalLanguage / originCountries stay
//     ABSENT when no TMDb key is configured, even for an item whose
//     Guid carries a TMDb id (the lookups are gated on the key).
//   - seasons: the aggregation carries the HDR flags, the best
//     stream's codec and channels — and deliberately NO aspect
//     ratio (asserted against the existing season fake).

import (
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fakeBadgeLibraryPMS(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	serve := func(key, record string) {
		mux.HandleFunc("/library/metadata/"+key, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"MediaContainer":{"Metadata":[`+record+`]}}`)
		})
	}
	// Movie 900: the 4K version is listed SECOND and wins the tier —
	// its aspect (2.39) and its HEVC codec are the facts; the 1080p
	// version's 1.78 must not leak. Best audio is a plain AAC track
	// (the audio-fix case). Two Plex collections; a TMDb Guid that
	// must NOT produce TMDb facts (no key configured).
	serve("900", `{"ratingKey":"900","type":"movie","title":"Scope Movie","year":2021,
		"Guid":[{"id":"tmdb://12345"}],
		"Collection":[{"tag":"Halloween"},{"tag":"Comfort Binges"}],
		"Media":[
			{"videoResolution":"1080","width":1920,"height":1080,"aspectRatio":1.78,
			 "Part":[{"file":"/m/scope-1080.mkv","Stream":[{"streamType":1,"codec":"h264"}]}]},
			{"videoResolution":"4k","width":3840,"height":1600,"aspectRatio":2.39,
			 "Part":[{"file":"/m/scope-4k.mkv","Stream":[
				{"streamType":1,"codec":"hevc"},
				{"streamType":2,"codec":"aac","channels":6,"languageCode":"eng","extendedDisplayTitle":"English (AAC 5.1)"}
			 ]}]}
		]}`)
	// Movie 903: the 4K version listed FIRST — a later, lower-tier
	// version must not overwrite the winner's aspect either.
	serve("903", `{"ratingKey":"903","type":"movie","title":"Scope First","year":2020,"Guid":[],
		"Media":[
			{"videoResolution":"4k","width":3840,"height":1600,"aspectRatio":2.39,
			 "Part":[{"file":"/m/first-4k.mkv","Stream":[{"streamType":1,"codec":"hevc"}]}]},
			{"videoResolution":"1080","width":1920,"height":1080,"aspectRatio":1.78,
			 "Part":[{"file":"/m/first-1080.mkv","Stream":[{"streamType":1,"codec":"h264"}]}]}
		]}`)
	// Movie 901: no aspectRatio field — width/height of the winning
	// entry is the fallback (1920/804 ≈ 2.388). No Collection tags.
	serve("901", `{"ratingKey":"901","type":"movie","title":"Fallback Ratio","year":2019,"Guid":[],
		"Media":[{"videoResolution":"1080","width":1920,"height":804,
			"Part":[{"file":"/m/fallback.mkv","Stream":[
				{"streamType":1,"codec":"h264"},
				{"streamType":2,"codec":"eac3","channels":6,"languageCode":"eng","extendedDisplayTitle":"English (E-AC-3 5.1)"}
			]}]}]}`)
	// Movie 902: Plex states no ratio and no dimensions — no fact.
	serve("902", `{"ratingKey":"902","type":"movie","title":"No Ratio","year":2018,"Guid":[],
		"Media":[{"videoResolution":"1080","Part":[{"file":"/m/noratio.mkv","Stream":[{"streamType":1,"codec":"h264"}]}]}]}`)
	// Show 910: one Plex collection; TMDb Guid, still no key.
	serve("910", `{"ratingKey":"910","type":"show","title":"Collected Show","year":2022,
		"Guid":[{"id":"tmdb://999"}],
		"Collection":[{"tag":"Star Trek"}]}`)
	// Episode 920: baited with a Collection tag — membership must
	// NOT surface on an episode; its own aspect and codec must.
	serve("920", `{"ratingKey":"920","type":"episode","title":"Baited Episode","index":1,"parentIndex":1,
		"Guid":[],"Collection":[{"tag":"Bait"}],
		"Media":[{"videoResolution":"1080","width":1920,"height":1080,"aspectRatio":1.78,
			"Part":[{"file":"/m/ep920.mkv","Stream":[
				{"streamType":1,"codec":"h264"},
				{"streamType":2,"codec":"aac","channels":2,"languageCode":"eng","extendedDisplayTitle":"English (AAC 2.0)"}
			]}]}]}`)
	return httptest.NewServer(mux)
}

func TestBadgeLibraryFactsMovies(t *testing.T) {
	pms := fakeBadgeLibraryPMS(t)
	defer pms.Close()
	app := appForFakePMS(t, pms.URL)
	defer app.Close()

	f := postFacts(t, app, "900")
	if f["aspectRatio"] != 2.39 {
		t.Errorf("aspectRatio = %v, want 2.39 (the winning 4K version's own field)", f["aspectRatio"])
	}
	if f["videoCodec"] != "hevc" {
		t.Errorf("videoCodec = %v, want hevc (same winning version)", f["videoCodec"])
	}
	if f["bestAudioCodec"] != "aac" || f["bestAudioLabel"] != "English (AAC 5.1)" || f["bestAudioChannels"] != float64(6) {
		t.Errorf("best audio = codec %v label %v ch %v, want aac / English (AAC 5.1) / 6",
			f["bestAudioCodec"], f["bestAudioLabel"], f["bestAudioChannels"])
	}
	cols, _ := f["collections"].([]any)
	if len(cols) != 2 || cols[0] != "Halloween" || cols[1] != "Comfort Binges" {
		t.Errorf("collections = %v, want [Halloween Comfort Binges] in record order", f["collections"])
	}
	// No TMDb key configured: the TMDb facts are absent even though
	// the Guid carries a TMDb id — the families hide, by the gate.
	if f["franchiseName"] != nil || f["originalLanguage"] != nil || f["originCountries"] != nil {
		t.Errorf("TMDb facts must be absent without a key: franchise=%v lang=%v countries=%v",
			f["franchiseName"], f["originalLanguage"], f["originCountries"])
	}

	f = postFacts(t, app, "903")
	if f["aspectRatio"] != 2.39 {
		t.Errorf("aspectRatio = %v, want 2.39 (first-listed winner; the 1080p version must not overwrite)", f["aspectRatio"])
	}

	f = postFacts(t, app, "901")
	if got, _ := f["aspectRatio"].(float64); math.Abs(got-1920.0/804.0) > 0.001 {
		t.Errorf("aspectRatio = %v, want ≈2.388 (width/height fallback)", f["aspectRatio"])
	}
	if f["bestAudioCodec"] != "eac3" {
		t.Errorf("bestAudioCodec = %v, want eac3", f["bestAudioCodec"])
	}
	if f["collections"] != nil {
		t.Errorf("collections = %v, want nil (no Collection tags on the record)", f["collections"])
	}

	f = postFacts(t, app, "902")
	if f["aspectRatio"] != nil {
		t.Errorf("aspectRatio = %v, want nil (Plex stated neither ratio nor dimensions)", f["aspectRatio"])
	}
}

func TestBadgeLibraryFactsShowAndEpisode(t *testing.T) {
	pms := fakeBadgeLibraryPMS(t)
	defer pms.Close()
	app := appForFakePMS(t, pms.URL)
	defer app.Close()

	f := postFacts(t, app, "910")
	cols, _ := f["collections"].([]any)
	if len(cols) != 1 || cols[0] != "Star Trek" {
		t.Errorf("collections = %v, want [Star Trek]", f["collections"])
	}
	if f["aspectRatio"] != nil {
		t.Errorf("aspectRatio = %v, want nil (a show record carries no Media)", f["aspectRatio"])
	}
	if f["franchiseName"] != nil || f["originalLanguage"] != nil || f["originCountries"] != nil {
		t.Errorf("TMDb facts must be absent without a key for shows too: %v / %v / %v",
			f["franchiseName"], f["originalLanguage"], f["originCountries"])
	}

	f = postFacts(t, app, "920")
	if f["collections"] != nil {
		t.Errorf("collections = %v, want nil (membership is a movie/show fact, never an episode's)", f["collections"])
	}
	if f["aspectRatio"] != 1.78 {
		t.Errorf("aspectRatio = %v, want 1.78 (the episode's own file)", f["aspectRatio"])
	}
	if f["bestAudioCodec"] != "aac" {
		t.Errorf("bestAudioCodec = %v, want aac (the episode's own best stream)", f["bestAudioCodec"])
	}
}

func TestBadgeLibraryFactsSeasonAggregation(t *testing.T) {
	// Against the existing season fake: season 720's episodes are
	// 4K/DV with DTS-HD MA audio — the aggregation must carry the
	// HDR flags and the best stream's codec + channels (the hdr and
	// channels families judge seasons on exactly this basis), and
	// must NOT carry an aspect ratio, collections, or TMDb facts.
	pms := fakeSeasonPMS(t)
	defer pms.Close()
	app := appForFakePMS(t, pms.URL)
	defer app.Close()

	f := postFacts(t, app, "720")
	if f["dv"] != true {
		t.Errorf("dv = %v, want true (aggregated from the season's episodes)", f["dv"])
	}
	if f["bestAudioCodec"] != "dca" || f["bestAudioChannels"] != float64(6) {
		t.Errorf("best audio = codec %v ch %v, want dca / 6 (the season's best stream)", f["bestAudioCodec"], f["bestAudioChannels"])
	}
	if f["aspectRatio"] != nil {
		t.Errorf("aspectRatio = %v, want nil — season aspect is deliberately never aggregated", f["aspectRatio"])
	}
	if f["collections"] != nil || f["franchiseName"] != nil || f["originalLanguage"] != nil || f["originCountries"] != nil {
		t.Errorf("season must not carry membership/TMDb facts: collections=%v franchise=%v lang=%v countries=%v",
			f["collections"], f["franchiseName"], f["originalLanguage"], f["originCountries"])
	}

	f = postFacts(t, app, "710")
	if f["bestAudioCodec"] != "truehd" || f["bestAudioChannels"] != float64(8) {
		t.Errorf("season 710 best audio = codec %v ch %v, want truehd / 8", f["bestAudioCodec"], f["bestAudioChannels"])
	}
}

// Parser-level pins for the two new stream facts, independent of
// the handler: aspect from the winning entry only; the codec of the
// stream that WINS the best-audio score, not the first stream seen.
func TestStreamFactsAspectAndBestCodec(t *testing.T) {
	full := fixtureFull(t, `{
		"type": "movie",
		"Media": [
			{"videoResolution": "1080", "width": 1920, "height": 1080, "aspectRatio": 1.78,
			 "Part": [{"Stream": [{"streamType": 1, "codec": "h264"}]}]},
			{"videoResolution": "4k", "width": 3840, "height": 1600, "aspectRatio": 2.39,
			 "Part": [{"Stream": [
				{"streamType": 1, "codec": "hevc"},
				{"streamType": 2, "codec": "aac", "channels": 2, "languageCode": "eng"},
				{"streamType": 2, "codec": "eac3", "channels": 6, "languageCode": "eng"}
			 ]}]}
		]}`)
	sf := parseStreamFacts(full)
	if sf.AspectRatio != 2.39 {
		t.Errorf("AspectRatio = %v, want 2.39 (winning entry)", sf.AspectRatio)
	}
	if sf.BestAudioCodec != "eac3" {
		t.Errorf("BestAudioCodec = %q, want eac3 (the 6-channel stream outscores the 2-channel AAC)", sf.BestAudioCodec)
	}

	// collectionTagsOf: record order preserved, blanks dropped,
	// missing array → nil.
	if got := collectionTagsOf(full); got != nil {
		t.Errorf("collectionTagsOf(no Collection) = %v, want nil", got)
	}
	tagged := fixtureFull(t, `{"Collection": [{"tag": "  First  "}, {"tag": ""}, {"tag": "Second"}]}`)
	got := collectionTagsOf(tagged)
	if len(got) != 2 || got[0] != "First" || got[1] != "Second" {
		t.Errorf("collectionTagsOf = %v, want [First Second] (trimmed, blanks dropped)", got)
	}
}
