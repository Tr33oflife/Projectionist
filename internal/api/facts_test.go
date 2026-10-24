package api

// facts_test.go — facts v2 parser tests against fixture JSON mirroring
// REAL Plex full-metadata shapes (field spellings per
// kometa-reference/STUDY.md: DOVIPresent, colorTrc, extendedDisplayTitle,
// Rating[].image/value/type). No network, no Plex server: the parser is
// pure over the decoded metadata map, so fixtures ARE the contract.

import (
	"encoding/json"
	"strings"
	"testing"
)

func fixtureFull(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("fixture JSON invalid: %v", err)
	}
	return m
}

const fixtureDVAtmos = `{
  "type": "movie", "title": "Fixture DV", "contentRating": "R",
  "editionTitle": "Director's Cut",
  "Rating": [
    {"image": "imdb://image.rating", "value": 8.7, "type": "audience"},
    {"image": "themoviedb://image.rating", "value": 8.4, "type": "audience"},
    {"image": "rottentomatoes://image.rating", "value": 91, "type": "critic"},
    {"image": "rottentomatoes://image.rating", "value": 88, "type": "audience"}
  ],
  "Media": [{
    "width": 3840, "height": 2160, "videoResolution": "4k", "audioCodec": "truehd",
    "Part": [{"Stream": [
      {"streamType": 1, "codec": "hevc", "DOVIPresent": true, "DOVIProfile": 8,
       "colorTrc": "smpte2084", "displayTitle": "4K DoVi (HEVC Main 10)"},
      {"streamType": 2, "codec": "truehd", "channels": 8, "languageCode": "eng",
       "displayTitle": "TrueHD Atmos 7.1", "extendedDisplayTitle": "English (TrueHD Atmos 7.1)"},
      {"streamType": 2, "codec": "ac3", "channels": 6, "languageCode": "jpn",
       "displayTitle": "AC3 5.1", "extendedDisplayTitle": "Japanese (AC3 5.1)"},
      {"streamType": 3, "codec": "srt", "languageCode": "eng", "displayTitle": "English (SRT)"},
      {"streamType": 3, "codec": "srt", "languageCode": "spa", "displayTitle": "Spanish (SRT)"}
    ]}]
  }]
}`

func TestStreamFactsDVAtmos(t *testing.T) {
	full := fixtureFull(t, fixtureDVAtmos)
	sf := parseStreamFacts(full)
	if sf.ResolutionTier != "4K" {
		t.Errorf("tier = %q, want 4K", sf.ResolutionTier)
	}
	if !sf.DV {
		t.Error("DV = false, want true (DOVIPresent)")
	}
	if sf.HDR10 {
		t.Error("HDR10 = true, want false — task definition: PQ without DV; DV present here")
	}
	if sf.HLG {
		t.Error("HLG = true, want false")
	}
	if !sf.HDRAny {
		t.Error("HDRAny = false, want true")
	}
	if !sf.Atmos {
		t.Error("Atmos = false, want true (truehd + 'Atmos' in titles)")
	}
	if !sf.TrueHD || sf.DTSX || sf.DTSHDMA {
		t.Errorf("truehd/dtsx/dtshdma = %v/%v/%v, want true/false/false", sf.TrueHD, sf.DTSX, sf.DTSHDMA)
	}
	if sf.BestAudioLabel != "English (TrueHD Atmos 7.1)" {
		t.Errorf("bestAudioLabel = %q", sf.BestAudioLabel)
	}
	if got := sf.AudioLangs; len(got) != 2 || got[0] != "eng" || got[1] != "jpn" {
		t.Errorf("audioLangs = %v, want [eng jpn]", got)
	}
	if got := sf.SubtitleLangs; len(got) != 2 || got[0] != "eng" || got[1] != "spa" {
		t.Errorf("subtitleLangs = %v, want [eng spa]", got)
	}
	if dc := dubClassOf(full); dc != "dual" {
		t.Errorf("dubClass = %q, want dual (English + Japanese audio)", dc)
	}
	if sf.AudioTracks != 2 {
		t.Errorf("audioTracks = %d, want 2 (fixture carries eng + jpn audio)", sf.AudioTracks)
	}
	if sf.MediaCount != 1 {
		t.Errorf("mediaCount = %d, want 1 (fixture carries one Media entry)", sf.MediaCount)
	}
	// Best-audio detail: the TrueHD Atmos stream (8 ch, eng) outscores
	// the AC3 stream, so its channels + language ride along.
	if sf.BestAudioChannels != 8 {
		t.Errorf("bestAudioChannels = %d, want 8", sf.BestAudioChannels)
	}
	if sf.BestAudioLang != "eng" {
		t.Errorf("bestAudioLang = %q, want eng", sf.BestAudioLang)
	}
}

func TestPlexRatingsParse(t *testing.T) {
	pr := parsePlexRatings(fixtureFull(t, fixtureDVAtmos))
	if pr.IMDb == nil || *pr.IMDb != 8.7 {
		t.Errorf("imdb = %v, want 8.7", pr.IMDb)
	}
	if pr.TMDB == nil || *pr.TMDB != 8.4 {
		t.Errorf("tmdb = %v, want 8.4", pr.TMDB)
	}
	if pr.RTCritic == nil || *pr.RTCritic != 91 {
		t.Errorf("rtCritic = %v, want 91", pr.RTCritic)
	}
	if pr.RTAudience == nil || *pr.RTAudience != 88 {
		t.Errorf("rtAudience = %v, want 88", pr.RTAudience)
	}
	empty := parsePlexRatings(map[string]any{})
	if empty.IMDb != nil || empty.RTCritic != nil {
		t.Error("empty metadata must yield no ratings")
	}
}

const fixtureSDRDTSHDMA = `{
  "type": "movie",
  "Media": [{
    "width": 1920, "height": 1080, "videoResolution": "1080",
    "Part": [{"Stream": [
      {"streamType": 1, "codec": "h264", "colorTrc": "bt709", "displayTitle": "1080p (H.264)"},
      {"streamType": 2, "codec": "dca", "channels": 6, "languageCode": "eng",
       "profile": "DTS-HD MA", "displayTitle": "DTS-HD MA 5.1",
       "extendedDisplayTitle": "English (DTS-HD MA 5.1)"}
    ]}]
  }]
}`

func TestStreamFactsSDRDTSHDMA(t *testing.T) {
	full := fixtureFull(t, fixtureSDRDTSHDMA)
	sf := parseStreamFacts(full)
	if sf.ResolutionTier != "1080p" {
		t.Errorf("tier = %q, want 1080p", sf.ResolutionTier)
	}
	if sf.DV || sf.HDR10 || sf.HLG || sf.HDRAny {
		t.Errorf("SDR file must report no HDR facts: %+v", sf)
	}
	if !sf.DTSHDMA {
		t.Error("DTSHDMA = false, want true (DTS-HD MA in profile/titles)")
	}
	if sf.Atmos || sf.DTSX || sf.TrueHD {
		t.Errorf("atmos/dtsx/truehd = %v/%v/%v, want all false", sf.Atmos, sf.DTSX, sf.TrueHD)
	}
	if dc := dubClassOf(full); dc != "dub" {
		t.Errorf("dubClass = %q, want dub (English only)", dc)
	}
}

const fixtureHLGDTSX = `{
  "type": "movie",
  "Media": [{
    "width": 1280, "height": 720, "videoResolution": "720",
    "Part": [{"Stream": [
      {"streamType": 1, "codec": "hevc", "colorTrc": "arib-std-b67"},
      {"streamType": 2, "codec": "dca", "channels": 8, "languageCode": "eng",
       "displayTitle": "DTS:X 7.1", "extendedDisplayTitle": "English (DTS:X 7.1)"}
    ]}]
  }]
}`

func TestStreamFactsHLGDTSX(t *testing.T) {
	full := fixtureFull(t, fixtureHLGDTSX)
	sf := parseStreamFacts(full)
	if sf.ResolutionTier != "720p" {
		t.Errorf("tier = %q, want 720p", sf.ResolutionTier)
	}
	if !sf.HLG || !sf.HDRAny {
		t.Errorf("hlg/hdrAny = %v/%v, want true/true", sf.HLG, sf.HDRAny)
	}
	if sf.HDR10 || sf.DV {
		t.Errorf("hdr10/dv = %v/%v, want false/false", sf.HDR10, sf.DV)
	}
	if !sf.DTSX {
		t.Error("DTSX = false, want true (DTS:X in titles)")
	}
}

const fixtureEAC3AtmosSub = `{
  "type": "movie",
  "Media": [{
    "width": 3840, "height": 1600, "videoResolution": "4k",
    "Part": [{"Stream": [
      {"streamType": 1, "codec": "hevc", "colorTrc": "smpte2084"},
      {"streamType": 2, "codec": "eac3", "channels": 6, "languageCode": "jpn",
       "displayTitle": "EAC3 Atmos 5.1", "extendedDisplayTitle": "Japanese (EAC3 Atmos 5.1)"},
      {"streamType": 3, "codec": "srt", "languageCode": "eng"}
    ]}]
  }]
}`

func TestStreamFactsEAC3AtmosHDR10Sub(t *testing.T) {
	full := fixtureFull(t, fixtureEAC3AtmosSub)
	sf := parseStreamFacts(full)
	if !sf.Atmos {
		t.Error("Atmos = false, want true (eac3 + Atmos wording)")
	}
	if !sf.HDR10 {
		t.Error("HDR10 = false, want true (smpte2084, no DV)")
	}
	if dc := dubClassOf(full); dc != "sub" {
		t.Errorf("dubClass = %q, want sub (Japanese only)", dc)
	}
}

func TestAggregateEpisodeLanguages(t *testing.T) {
	ep := func(audio []string, subs []string) map[string]any {
		streams := ""
		for i, code := range audio {
			if i > 0 {
				streams += ","
			}
			streams += `{"streamType":2,"languageCode":"` + code + `"}`
		}
		for _, code := range subs {
			if streams != "" {
				streams += ","
			}
			streams += `{"streamType":3,"languageCode":"` + code + `"}`
		}
		return fixtureFull(t, `{"type":"episode","Media":[{"Part":[{"Stream":[`+streams+`]}]}]}`)
	}
	// Mixed-language episodes: unions are stable-sorted, the dub class
	// comes from the UNION set (jpn on ep.1 + eng on ep.2 = dual).
	agg := aggregateEpisodeLanguages([]map[string]any{
		ep([]string{"jpn"}, []string{"eng"}),
		ep([]string{"eng"}, nil),
		ep([]string{"fra"}, []string{"jpn"}),
	}, 3)
	if strings.Join(agg.AudioLangs, ",") != "eng,fra,jpn" {
		t.Errorf("audioLangs union = %v, want [eng fra jpn]", agg.AudioLangs)
	}
	if strings.Join(agg.SubtitleLangs, ",") != "eng,jpn" {
		t.Errorf("subtitleLangs union = %v, want [eng jpn]", agg.SubtitleLangs)
	}
	if !agg.HasSubs {
		t.Error("hasSubs must be true when any episode carries subtitles")
	}
	if agg.DubClass != "dual" {
		t.Errorf("dubClass = %q, want dual (en+ja across the union)", agg.DubClass)
	}
	if agg.Scanned != 3 || agg.Total != 3 || agg.Partial {
		t.Errorf("scanned/total/partial = %d/%d/%v, want 3/3/false", agg.Scanned, agg.Total, agg.Partial)
	}
	// Single-language unions keep the taxonomy's verdicts.
	if got := aggregateEpisodeLanguages([]map[string]any{ep([]string{"jpn"}, nil)}, 1).DubClass; got != "sub" {
		t.Errorf("jpn-only dubClass = %q, want sub", got)
	}
	if got := aggregateEpisodeLanguages([]map[string]any{ep([]string{"eng"}, nil)}, 1).DubClass; got != "dub" {
		t.Errorf("eng-only dubClass = %q, want dub", got)
	}
	// No language tags anywhere: unknown, empty unions — never guessed.
	none := aggregateEpisodeLanguages([]map[string]any{fixtureFull(t, `{"type":"episode","Media":[{"Part":[{"Stream":[{"streamType":2}]}]}]}`)}, 1)
	if none.DubClass != "unknown" || len(none.AudioLangs) != 0 {
		t.Errorf("untagged episodes must stay unknown/empty, got %q %v", none.DubClass, none.AudioLangs)
	}
	// Partial cap: 300 scanned of 512 total says so, plainly.
	many := make([]map[string]any, EpisodeLangCap)
	for i := range many {
		many[i] = ep([]string{"eng"}, nil)
	}
	part := aggregateEpisodeLanguages(many, 512)
	if !part.Partial || part.Scanned != EpisodeLangCap || part.Total != 512 {
		t.Errorf("partial = %v scanned %d total %d, want true/%d/512", part.Partial, part.Scanned, part.Total, EpisodeLangCap)
	}
}

func TestStreamFactsMediaCount(t *testing.T) {
	// Two Media entries = two file versions of one item — the versions
	// overlay family fires on exactly this count.
	two := fixtureFull(t, `{"type":"movie","Media":[
		{"width":3840,"videoResolution":"4k","Part":[{"Stream":[]}]},
		{"width":1920,"videoResolution":"1080","Part":[{"Stream":[]}]}
	]}`)
	if got := parseStreamFacts(two).MediaCount; got != 2 {
		t.Errorf("mediaCount = %d, want 2", got)
	}
	if got := parseStreamFacts(map[string]any{}).MediaCount; got != 0 {
		t.Errorf("mediaCount = %d, want 0 for metadata without Media", got)
	}
}

func TestStreamFactsEmptyAndFallbacks(t *testing.T) {
	sf := parseStreamFacts(map[string]any{})
	if sf.ResolutionTier != "" || sf.HDRAny || sf.Atmos || sf.BestAudioLabel != "" {
		t.Errorf("empty metadata must yield zero facts: %+v", sf)
	}
	if dc := dubClassOf(map[string]any{}); dc != "unknown" {
		t.Errorf("dubClass = %q, want unknown", dc)
	}
	// Tier fallback from dimensions when Plex left videoResolution blank.
	cases := []struct {
		vr   string
		w, h float64
		want string
	}{
		{"4k", 0, 0, "4K"},
		{"1080", 0, 0, "1080p"},
		{"", 3840, 2160, "4K"},
		{"", 1920, 1080, "1080p"},
		{"", 1280, 720, "720p"},
		{"", 720, 576, "576p"},
		{"", 720, 480, "480p"},
		{"", 640, 360, "SD"},
		{"", 0, 0, ""},
	}
	for _, c := range cases {
		if got := resolutionTierOf(c.vr, c.w, c.h); got != c.want {
			t.Errorf("resolutionTierOf(%q,%v,%v) = %q, want %q", c.vr, c.w, c.h, got, c.want)
		}
	}
}

// Source tag: a LITERAL filename reading — whole tokens only.
func TestSourceTagFromFilename(t *testing.T) {
	cases := []struct{ file, want string }{
		{"/movies/Dune (2024)/Dune.2024.2160p.BluRay.REMUX.HEVC.TrueHD.mkv", "REMUX"},
		{"Movie.2019.1080p.BluRay.x264.mkv", "Blu-ray"},
		{"Movie.2019.720p.BDRip.XviD.avi", "Blu-ray"},
		{"movie.2021.web-dl.1080p.mkv", "WEB-DL"},
		{"Movie.2022.WEBDL.DD5.1.mkv", "WEB-DL"},
		{"Movie.2020.WEBRip.x265.mkv", "WEBRIP"},
		{"Show.S01E01.HDTV.x264.mkv", "HDTV"},
		{"Old.Movie.1998.DVD.x264.mkv", "DVD"},
		// Token boundaries: the name must actually SAY the tag.
		{"Movie.2024.WEB.1080p.mkv", ""},  // "web" alone is not a tag
		{"Movie.SomeWEBDLthing.mkv", ""},  // substring inside a larger token
		{"Movie.2024.1080p.x264.mkv", ""}, // no source token at all
		{"", ""},                          // no file
	}
	for _, c := range cases {
		if got := sourceTagForFilename(c.file); got != c.want {
			t.Errorf("sourceTagForFilename(%q) = %q, want %q", c.file, got, c.want)
		}
	}
}

// Subtitle detail: EVERY streamType-3 stream across all Media/Parts
// counts; forced/SDH come only from the boolean fields Plex states —
// an absent field is false, and a stream TITLE saying "Forced" is
// not evidence.
func TestStreamFactsSubtitleDetail(t *testing.T) {
	full := fixtureFull(t, `{
	  "type": "movie",
	  "Media": [{
	    "Part": [
	      {"Stream": [
	        {"streamType": 3, "codec": "srt", "languageCode": "eng", "forced": true},
	        {"streamType": 3, "codec": "srt", "languageCode": "spa", "hearingImpaired": true}
	      ]},
	      {"Stream": [
	        {"streamType": 3, "codec": "srt", "languageCode": "jpn"},
	        {"streamType": 3, "codec": "srt", "languageCode": "fra", "forced": false, "hearingImpaired": false, "displayTitle": "French Forced (SRT)"}
	      ]}
	    ]
	  }]
	}`)
	sf := parseStreamFacts(full)
	if sf.SubtitleCount != 4 {
		t.Errorf("subtitleCount = %d, want 4 (every streamType-3 stream, all Parts)", sf.SubtitleCount)
	}
	if !sf.SubtitleForced {
		t.Error("subtitleForced = false, want true (one stream states forced: true)")
	}
	if !sf.SubtitleSDH {
		t.Error("subtitleSDH = false, want true (one stream states hearingImpaired: true)")
	}
	plain := parseStreamFacts(fixtureFull(t, `{"type":"movie","Media":[{"Part":[{"Stream":[
		{"streamType": 3, "codec": "srt", "languageCode": "eng", "displayTitle": "English Forced SDH (SRT)"}
	]}]}]}`))
	if plain.SubtitleCount != 1 || plain.SubtitleForced || plain.SubtitleSDH {
		t.Errorf("count/forced/sdh = %d/%v/%v, want 1/false/false (absent fields and title text are not evidence)",
			plain.SubtitleCount, plain.SubtitleForced, plain.SubtitleSDH)
	}
}

// Show aggregation SUMS subtitle counts across episodes and ORs the
// forced/SDH flags.
func TestAggregateEpisodeSubtitleDetail(t *testing.T) {
	ep1 := fixtureFull(t, `{"type":"episode","Media":[{"Part":[{"Stream":[
		{"streamType":3,"languageCode":"eng","forced":true},
		{"streamType":3,"languageCode":"spa"}
	]}]}]}`)
	ep2 := fixtureFull(t, `{"type":"episode","Media":[{"Part":[{"Stream":[
		{"streamType":3,"languageCode":"eng","hearingImpaired":true}
	]}]}]}`)
	agg := aggregateEpisodeLanguages([]map[string]any{ep1, ep2}, 2)
	if agg.SubtitleCount != 3 {
		t.Errorf("aggregated subtitleCount = %d, want 3 (2 + 1 across episodes)", agg.SubtitleCount)
	}
	if !agg.SubtitleForced || !agg.SubtitleSDH {
		t.Errorf("aggregated forced/sdh = %v/%v, want true/true", agg.SubtitleForced, agg.SubtitleSDH)
	}
}

// Video codec is TIER-LINKED: it names the codec of the Media entry
// whose resolution tier wins, not the first or last one seen. A
// multi-version item whose best file is HEVC must say HEVC even when
// a lesser H.264 version is listed first (and vice versa).
func TestStreamFactsVideoCodecTierLinked(t *testing.T) {
	two := fixtureFull(t, `{"type":"movie","Media":[
		{"width":1920,"videoResolution":"1080","Part":[{"Stream":[{"streamType":1,"codec":"h264"}]}]},
		{"width":3840,"videoResolution":"4k","Part":[{"Stream":[{"streamType":1,"codec":"hevc"}]}]}
	]}`)
	sf := parseStreamFacts(two)
	if sf.ResolutionTier != "4K" || sf.VideoCodec != "hevc" {
		t.Errorf("tier/codec = %q/%q, want 4K/hevc (the higher tier's own codec)", sf.ResolutionTier, sf.VideoCodec)
	}
	// Winner first: a lower tier listed after must not override.
	twoRev := fixtureFull(t, `{"type":"movie","Media":[
		{"width":3840,"videoResolution":"4k","Part":[{"Stream":[{"streamType":1,"codec":"hevc"}]}]},
		{"width":1920,"videoResolution":"1080","Part":[{"Stream":[{"streamType":1,"codec":"h264"}]}]}
	]}`)
	if sf := parseStreamFacts(twoRev); sf.VideoCodec != "hevc" {
		t.Errorf("codec = %q, want hevc (later lower tier must not override)", sf.VideoCodec)
	}
	// The FIRST video stream of the winning Media is its codec.
	multi := fixtureFull(t, `{"type":"movie","Media":[
		{"width":3840,"videoResolution":"4k","Part":[{"Stream":[{"streamType":1,"codec":"av1"},{"streamType":1,"codec":"hevc"}]}]}
	]}`)
	if sf := parseStreamFacts(multi); sf.VideoCodec != "av1" {
		t.Errorf("codec = %q, want av1 (first video stream of the winning Media)", sf.VideoCodec)
	}
	// Existing fixtures: DV/Atmos file is HEVC, the SDR one is H.264.
	if sf := parseStreamFacts(fixtureFull(t, fixtureDVAtmos)); sf.VideoCodec != "hevc" {
		t.Errorf("codec = %q, want hevc (DV fixture)", sf.VideoCodec)
	}
	if sf := parseStreamFacts(fixtureFull(t, fixtureSDRDTSHDMA)); sf.VideoCodec != "h264" {
		t.Errorf("codec = %q, want h264 (SDR fixture)", sf.VideoCodec)
	}
	if sf := parseStreamFacts(map[string]any{}); sf.VideoCodec != "" {
		t.Errorf("codec = %q, want empty for metadata without Media", sf.VideoCodec)
	}
}

// studioOf / genreTagsOf: verbatim readings from the item record —
// studio trimmed, genres in Plex's own order, empties dropped.
func TestStudioOfGenreTagsOf(t *testing.T) {
	full := fixtureFull(t, `{
		"studio": "  A24  ",
		"Genre": [{"tag":"Horror"},{"tag":"Thriller"},{"tag":"  "},{"tag":"Comedy"}]
	}`)
	if got := studioOf(full); got != "A24" {
		t.Errorf("studioOf = %q, want A24 (trimmed)", got)
	}
	genres := genreTagsOf(full)
	if strings.Join(genres, ",") != "Horror,Thriller,Comedy" {
		t.Errorf("genreTagsOf = %v, want [Horror Thriller Comedy] in Plex order, blanks dropped", genres)
	}
	empty := map[string]any{}
	if got := studioOf(empty); got != "" {
		t.Errorf("studioOf(empty) = %q, want empty", got)
	}
	if got := genreTagsOf(empty); len(got) != 0 {
		t.Errorf("genreTagsOf(empty) = %v, want empty", got)
	}
}

// The FIRST Part across Media supplies the name — later versions
// don't override it.
func TestStreamFactsSourceTagFirstPart(t *testing.T) {
	full := fixtureFull(t, `{
	  "type": "movie",
	  "Media": [
	    {"width": 3840, "Part": [{"file": "/m/Movie.2024.2160p.UHD.BluRay.REMUX.mkv", "Stream": []}]},
	    {"width": 1920, "Part": [{"file": "/m/Movie.2024.1080p.WEB-DL.mkv", "Stream": []}]}
	  ]
	}`)
	if got := parseStreamFacts(full).SourceTag; got != "REMUX" {
		t.Errorf("sourceTag = %q, want REMUX (first Part wins)", got)
	}
	noFile := fixtureFull(t, `{"type": "movie", "Media": [{"Part": [{"Stream": []}]}]}`)
	if got := parseStreamFacts(noFile).SourceTag; got != "" {
		t.Errorf("sourceTag = %q, want empty when no file name is present", got)
	}
}
