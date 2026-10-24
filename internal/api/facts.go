package api

// facts.go — Overlay Studio facts v2: stream-level truth parsed from ONE
// item's full Plex metadata (the same GetFullItem map the dub scanner
// consumes). This is what finally wires the tech badges to reality the
// way Kometa does (see kometa-reference/STUDY.md): every value below is
// READ from the item's own Media/Part/Stream data — nothing is inferred
// from titles, file names, or guesses. Missing stream data yields the
// zero value, and the composite rules treat that as "badge hidden".
//
// Field spellings are the ones Kometa/plexapi read from Plex (PROVEN —
// STUDY.md): Media[].videoResolution/width/height, video Stream
// DOVIPresent + colorTrc ("smpte2084" = PQ/HDR10 family,
// "arib-std-b67" = HLG), audio Stream codec/profile/displayTitle/
// extendedDisplayTitle. HDR10+ ("Plus") is NOT reliably exposed by Plex
// and is deliberately NEVER reported here.

import (
	"sort"
	"strings"

	"plexcollectionmanager/internal/advisor"
)

// streamFacts is the parsed per-item stream truth. Empty/false means
// "Plex didn't say" — never "we guessed no".
type streamFacts struct {
	ResolutionTier    string   // "4K"|"1080p"|"720p"|"576p"|"480p"|"SD"|"" (unknown)
	VideoCodec        string   // codec of the video stream on the Media whose tier WINS (raw Plex spelling, e.g. "hevc"); "" when unknown
	DV                bool     // any video stream with DOVIPresent
	HDR10             bool     // PQ (smpte2084) present AND no DV — the task/Kometa split
	HLG               bool     // any video stream with colorTrc arib-std-b67
	HDRAny            bool     // DV || PQ || HLG
	Atmos             bool     // best-audio detection below, per stream then OR'd
	DTSX              bool     //
	DTSHDMA           bool     //
	TrueHD            bool     //
	BestAudioLabel    string   // human label of the best audio stream; "" when none
	BestAudioCodec    string   // RAW codec of that same best-scored stream ("aac", "eac3", …); "" when none
	BestAudioChannels int      // channels on that same best-scored audio stream
	BestAudioLang     string   // languageCode (preferred) / language of that stream
	AspectRatio       float64  // aspect ratio of the WINNING version's Media (its aspectRatio field, else width/height); 0 = unstated
	SourceTag         string   // literal source token read from the first Part file name
	AudioLangs        []string // sorted unique, languageCode preferred
	SubtitleLangs     []string // sorted unique
	HasSubs           bool     // any subtitle stream at all, tagged or not
	SubtitleCount     int      // every subtitle stream across all Media/Parts
	SubtitleForced    bool     // any subtitle stream whose forced field is boolean true
	SubtitleSDH       bool     // any subtitle stream whose hearingImpaired field is boolean true
	AudioTracks       int      // audio streams seen (0 = none on this item — e.g. a show record)
	MediaCount        int      // Media[] entries on the item (0 = Plex didn't say; >1 = multiple versions)
}

// rawf reads a float64 from a decoded-JSON map (Plex numbers decode as
// float64 through encoding/json into map[string]any).
func rawf(m map[string]any, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}

func raws(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func rawList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}

func rawMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// tierRank orders resolution tiers so multi-version items report their
// BEST version (Kometa's dovetail picks the highest combo the same way).
var tierRank = map[string]int{"SD": 1, "480p": 2, "576p": 3, "720p": 4, "1080p": 5, "4K": 6}

// resolutionTierOf maps ONE Media entry to a tier: the videoResolution
// string Plex itself computed is authoritative; width/height thresholds
// are only a fallback for entries Plex left unlabelled.
func resolutionTierOf(videoResolution string, width, height float64) string {
	switch strings.ToLower(strings.TrimSpace(videoResolution)) {
	case "4k":
		return "4K"
	case "1080":
		return "1080p"
	case "720":
		return "720p"
	case "576":
		return "576p"
	case "480":
		return "480p"
	case "sd":
		return "SD"
	}
	switch {
	case width >= 3800:
		return "4K"
	case width >= 1800:
		return "1080p"
	case width >= 1200:
		return "720p"
	case height >= 570:
		return "576p"
	case height >= 470:
		return "480p"
	case width > 0 || height > 0:
		return "SD"
	}
	return ""
}

// audioStreamInfo classifies ONE audio stream.
type audioStreamInfo struct {
	atmos, dtsx, dtshdma, truehd bool
	label                        string
	codec                        string // raw Plex codec spelling, lowercased
	score                        int
	channels                     int
	lang                         string
}

func classifyAudioStream(st map[string]any) audioStreamInfo {
	codec := strings.ToLower(raws(st, "codec"))
	disp := raws(st, "displayTitle")
	ext := raws(st, "extendedDisplayTitle")
	profile := raws(st, "profile")
	hay := strings.ToLower(disp + " " + ext + " " + profile)
	info := audioStreamInfo{codec: codec}
	info.atmos = (codec == "eac3" || codec == "truehd") && strings.Contains(hay, "atmos")
	info.dtsx = strings.Contains(hay, "dts:x")
	info.dtshdma = strings.Contains(hay, "dts-hd ma") || strings.Contains(hay, "dts-hd master")
	info.truehd = codec == "truehd"
	channels := rawf(st, "channels")
	info.channels = int(channels)
	info.lang = langOfStream(st)
	switch {
	case info.atmos:
		info.score = 1000
	case info.dtsx:
		info.score = 800
	case info.dtshdma:
		info.score = 600
	case info.truehd:
		info.score = 400
	case codec == "eac3":
		info.score = 200
	case codec == "dca":
		info.score = 100
	}
	info.score += int(channels) * 10
	// Human label: Plex's own extended display title is the honest one
	// (e.g. "English (TrueHD Atmos 7.1)"); fall back to the short display
	// title, then to codec + channel layout.
	info.label = strings.TrimSpace(ext)
	if info.label == "" {
		info.label = strings.TrimSpace(disp)
	}
	if info.label == "" && codec != "" {
		layout := ""
		switch int(channels) {
		case 8:
			layout = "7.1"
		case 6:
			layout = "5.1"
		case 2:
			layout = "2.0"
		case 1:
			layout = "1.0"
		}
		info.label = strings.TrimSpace(codec + " " + layout)
	}
	return info
}

func langOfStream(st map[string]any) string {
	if c := strings.ToLower(strings.TrimSpace(raws(st, "languageCode"))); c != "" {
		return c
	}
	return strings.ToLower(strings.TrimSpace(raws(st, "language")))
}

// parseStreamFacts walks Media → Part → Stream once and derives every
// stream fact. Defensive by construction: any malformed node is skipped,
// exactly like the advisor's AudioStreamsOf.
func parseStreamFacts(full map[string]any) streamFacts {
	sf, _ := parseStreamFactsBest(full)
	return sf
}

// parseStreamFactsBest is parseStreamFacts plus the winning audio
// stream's own classification (score -1 when the item has no audio
// stream at all). The season aggregation merges per-episode facts and
// needs each episode's BEST stream — score included — to pick the
// season's best the same way this parser picks an item's.
func parseStreamFactsBest(full map[string]any) (streamFacts, audioStreamInfo) {
	out := streamFacts{AudioLangs: []string{}, SubtitleLangs: []string{}}
	// MediaCount feeds the Kometa-style "versions" overlay family:
	// Plex lists one Media entry per file version of the item.
	out.MediaCount = len(rawList(full["Media"]))
	audioLangs := map[string]bool{}
	subLangs := map[string]bool{}
	pq, hlg := false, false
	best := audioStreamInfo{score: -1}
	firstPartFile := ""
	for _, mediaV := range rawList(full["Media"]) {
		media := rawMap(mediaV)
		if media == nil {
			continue
		}
		// The video codec rides WITH the tier: when several Media
		// entries (versions) disagree, the badge names the codec of
		// the version whose resolution wins — the same version the
		// resolution badge describes. First video stream of THIS
		// Media entry is its codec; a later, lower tier never
		// overwrites it.
		tier := resolutionTierOf(raws(media, "videoResolution"), rawf(media, "width"), rawf(media, "height"))
		tierWins := tierRank[tier] > tierRank[out.ResolutionTier]
		if tierWins {
			out.ResolutionTier = tier
			// The aspect ratio rides WITH the winning version, like
			// the video codec below: Plex's own aspectRatio field on
			// the Media entry first; width/height of THAT entry as
			// the fallback. A later, lower-tier version never
			// overwrites it.
			out.AspectRatio = rawf(media, "aspectRatio")
			if out.AspectRatio <= 0 {
				if w, h := rawf(media, "width"), rawf(media, "height"); w > 0 && h > 0 {
					out.AspectRatio = w / h
				}
			}
		}
		mediaVideoCodec := ""
		for _, partV := range rawList(media["Part"]) {
			part := rawMap(partV)
			if firstPartFile == "" {
				firstPartFile = raws(part, "file")
			}
			for _, streamV := range rawList(part["Stream"]) {
				st := rawMap(streamV)
				if st == nil {
					continue
				}
				switch int(rawf(st, "streamType")) {
				case 1: // video
					if mediaVideoCodec == "" {
						mediaVideoCodec = raws(st, "codec")
					}
					if dv, ok := st["DOVIPresent"].(bool); ok && dv {
						out.DV = true
					}
					switch raws(st, "colorTrc") {
					case "smpte2084":
						pq = true
					case "arib-std-b67":
						hlg = true
					}
				case 2: // audio
					out.AudioTracks++
					info := classifyAudioStream(st)
					out.Atmos = out.Atmos || info.atmos
					out.DTSX = out.DTSX || info.dtsx
					out.DTSHDMA = out.DTSHDMA || info.dtshdma
					out.TrueHD = out.TrueHD || info.truehd
					if info.score > best.score {
						best = info
					}
					if l := langOfStream(st); l != "" {
						audioLangs[l] = true
					}
				case 3: // subtitle
					out.HasSubs = true
					out.SubtitleCount++
					// forced / hearingImpaired are read ONLY as the
					// boolean fields Plex states on the stream. An
					// absent or non-boolean field is "not stated" —
					// never inferred from the stream's title text.
					if fv, ok := st["forced"].(bool); ok && fv {
						out.SubtitleForced = true
					}
					if hv, ok := st["hearingImpaired"].(bool); ok && hv {
						out.SubtitleSDH = true
					}
					if l := langOfStream(st); l != "" {
						subLangs[l] = true
					}
				}
			}
		}
		if tierWins {
			out.VideoCodec = mediaVideoCodec
		}
	}
	out.HDR10 = pq && !out.DV
	out.HLG = hlg
	out.HDRAny = out.DV || pq || hlg
	if best.score >= 0 {
		out.BestAudioLabel = best.label
		out.BestAudioCodec = best.codec
		out.BestAudioChannels = best.channels
		out.BestAudioLang = best.lang
	}
	out.SourceTag = sourceTagForFilename(firstPartFile)
	for l := range audioLangs {
		out.AudioLangs = append(out.AudioLangs, l)
	}
	for l := range subLangs {
		out.SubtitleLangs = append(out.SubtitleLangs, l)
	}
	sort.Strings(out.AudioLangs)
	sort.Strings(out.SubtitleLangs)
	return out, best
}

// sourceTagForFilename reads a source tag from a file basename
// LITERALLY: it only reports a tag when a known representation appears
// as a whole token in the name (Kometa's video_format family works the
// same way — release-name tokens in, tag out; nothing is inferred from
// resolution, codec, bitrate, or size). "web" by itself is not a tag:
// the name must actually say WEB-DL/WEBDL or WEBRIP. The underscore /
// dot / bracket separators common in release names are token borders;
// the hyphen inside WEB-DL / BLU-RAY is part of the token.
func sourceTagForFilename(file string) string {
	base := file
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	upper := strings.ToUpper(base)
	hasToken := func(tag string) bool {
		for start := 0; ; {
			i := strings.Index(upper[start:], tag)
			if i < 0 {
				return false
			}
			i += start
			beforeOK := i == 0 || !isTagByte(upper[i-1])
			end := i + len(tag)
			afterOK := end == len(upper) || !isTagByte(upper[end])
			if beforeOK && afterOK {
				return true
			}
			start = i + 1
		}
	}
	switch {
	case hasToken("REMUX"):
		return "REMUX"
	case hasToken("BLURAY") || hasToken("BLU-RAY") || hasToken("BDREMUX") || hasToken("BDRIP"):
		return "Blu-ray"
	case hasToken("WEB-DL") || hasToken("WEBDL"):
		return "WEB-DL"
	case hasToken("WEBRIP"):
		return "WEBRIP"
	case hasToken("HDTV"):
		return "HDTV"
	case hasToken("DVD"):
		return "DVD"
	}
	return ""
}

func isTagByte(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// plexRatings is the item's own Rating[] array, split by source. Plex
// serves entries like {image: "imdb://image.rating", value: 8.7,
// type: "audience"} — the image URI is the ONLY honest source label, so
// bare rating/audienceRating numbers are never attributed to a source.
type plexRatings struct {
	IMDb       *float64
	TMDB       *float64
	RTCritic   *float64
	RTAudience *float64
}

func parsePlexRatings(full map[string]any) plexRatings {
	var out plexRatings
	for _, rv := range rawList(full["Rating"]) {
		r := rawMap(rv)
		if r == nil {
			continue
		}
		v, ok := r["value"].(float64)
		if !ok {
			continue
		}
		image := strings.ToLower(raws(r, "image"))
		typ := strings.ToLower(raws(r, "type"))
		val := v
		switch {
		case strings.Contains(image, "imdb://"):
			if out.IMDb == nil {
				out.IMDb = &val
			}
		case strings.Contains(image, "themoviedb://"):
			if out.TMDB == nil {
				out.TMDB = &val
			}
		case strings.Contains(image, "rottentomatoes://"):
			if typ == "audience" {
				if out.RTAudience == nil {
					out.RTAudience = &val
				}
			} else if out.RTCritic == nil {
				out.RTCritic = &val
			}
		}
	}
	return out
}

// dubClassOf reuses the advisor dubscan verdict on the item's own audio
// streams (English = dub, Japanese-only = sub, both = dual — STUDY.md
// point 5: overlays reuse the scan rule, they don't reinvent it) and
// maps its vocabulary onto the overlay one. No audio streams at all →
// "unknown" (a show's streams live on its episodes; never guessed).
func dubClassOf(full map[string]any) string {
	verdict := advisor.VerdictForStreams(advisor.AudioStreamsOf(full)).Verdict
	switch verdict {
	case "dubbed":
		return "dub"
	case "subbed":
		return "sub"
	case "dual":
		return "dual"
	}
	return "unknown"
}

// studioOf reads the item's studio/network string exactly as Plex
// states it ("A24" on a movie, "HBO" on a show), trimmed. "" when
// Plex states none — the studio badge then simply never fires.
func studioOf(full map[string]any) string {
	return strings.TrimSpace(raws(full, "studio"))
}

// genreTagsOf reads the item's Genre[] tag array in PLEX ORDER — the
// genre badge draws the first one (or two), so order is meaning, not
// an implementation detail. Empty-string tags are dropped; a missing
// array yields an empty slice, never a guess.
func genreTagsOf(full map[string]any) []string {
	out := []string{}
	for _, gv := range rawList(full["Genre"]) {
		g := rawMap(gv)
		if g == nil {
			continue
		}
		if tag := strings.TrimSpace(raws(g, "tag")); tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

// collectionTagsOf reads the item's Collection[] tag array in
// RECORD ORDER — the Plex collections this title belongs to on the
// server, named exactly as the server names them. The Collection
// family names the first and counts the rest, so order is meaning.
// Empty-string tags are dropped; a missing array yields nil.
func collectionTagsOf(full map[string]any) []string {
	var out []string
	for _, cv := range rawList(full["Collection"]) {
		c := rawMap(cv)
		if c == nil {
			continue
		}
		if tag := strings.TrimSpace(raws(c, "tag")); tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

// ---- show-level language aggregation --------------------------------------
// A show's own metadata carries NO streams — they live on its episodes —
// so every language verdict for a show used to be a shrug. the owner's ask
// (2026-10-22): "display on ALL my tv shows a badge that shows language
// options." The facts endpoint therefore aggregates episode languages
// for shows: the UNION of audio/subtitle languages across episodes, and
// a dubClass computed from the UNION language set under the same
// VerdictForStreams taxonomy (en+ja ⇒ dual, en ⇒ dub, ja ⇒ sub, else
// unknown). Bounded work: at most EpisodeLangCap episodes are aggregated
// (the client fetches one capped page); beyond that Partial says so
// instead of silently pretending full coverage.
//
// NOTE the differing meaning of the audioTracks fact for shows: it is
// the number of DISTINCT audio languages in the union (a show has no
// single stream list to count tracks from). The renderer treats it as
// "any audio known" for shows, and episodeCount/langsFromEpisodes on
// the facts disclose the aggregation wherever it matters.
const EpisodeLangCap = 300

type episodeLangFacts struct {
	AudioLangs     []string
	SubtitleLangs  []string
	HasSubs        bool
	SubtitleCount  int  // subtitle streams SUMMED across episodes
	SubtitleForced bool // any episode carries a forced subtitle stream
	SubtitleSDH    bool // any episode carries an SDH subtitle stream
	DubClass       string
	Scanned        int  // episodes aggregated (<= EpisodeLangCap)
	Total          int  // episodes the show has (== Scanned unless Partial)
	Partial        bool // Total > Scanned — the union covers only the first Scanned
}

func aggregateEpisodeLanguages(episodes []map[string]any, total int) episodeLangFacts {
	out := episodeLangFacts{
		AudioLangs: []string{}, SubtitleLangs: []string{}, DubClass: "unknown",
		Scanned: len(episodes), Total: total,
	}
	if out.Total < out.Scanned {
		out.Total = out.Scanned
	}
	out.Partial = out.Total > out.Scanned
	audioSet := map[string]bool{}
	subSet := map[string]bool{}
	for _, ep := range episodes {
		sf := parseStreamFacts(ep)
		for _, l := range sf.AudioLangs {
			audioSet[l] = true
		}
		for _, l := range sf.SubtitleLangs {
			subSet[l] = true
		}
		if sf.HasSubs {
			out.HasSubs = true
		}
		// Subtitle detail aggregates like the languages: the count
		// sums (tracks are per-episode things), the flags OR.
		out.SubtitleCount += sf.SubtitleCount
		out.SubtitleForced = out.SubtitleForced || sf.SubtitleForced
		out.SubtitleSDH = out.SubtitleSDH || sf.SubtitleSDH
	}
	for l := range audioSet {
		out.AudioLangs = append(out.AudioLangs, l)
	}
	for l := range subSet {
		out.SubtitleLangs = append(out.SubtitleLangs, l)
	}
	sort.Strings(out.AudioLangs)
	sort.Strings(out.SubtitleLangs)
	// Dub class from the UNION set, judged by the same taxonomy the
	// per-item path uses: one synthetic audio stream per union language.
	streams := make([]advisor.Stream, 0, len(out.AudioLangs))
	for _, code := range out.AudioLangs {
		streams = append(streams, advisor.Stream{StreamType: 2, LanguageCode: code})
	}
	switch advisor.VerdictForStreams(streams).Verdict {
	case "dubbed":
		out.DubClass = "dub"
	case "subbed":
		out.DubClass = "sub"
	case "dual":
		out.DubClass = "dual"
	}
	return out
}

// ---- season-level aggregation ---------------------------------------------
// A season record, like a show record, carries no streams — they live
// on its episodes. Season facts are therefore an aggregation over JUST
// that season's episodes (fetched by the facts handler via the
// season's children + the same batch-record fetch the show language
// aggregation uses), under the conventions the codebase already applies WITHIN one
// item: presence flags (DV/HDR/HLG, Atmos/DTS:X/…) OR across sources,
// the best audio stream wins by score, and languages union. The one
// convention that is new is needed because episodes can DISAGREE where
// an item's versions rarely do: the season's resolution — and the
// codec that rides with it — is the DOMINANT value, the one most of
// the season's episodes carry (ties break toward the higher tier,
// the same "best wins" spirit parseStreamFacts applies to versions).
//
// SourceTag and MediaCount stay zero BY DESIGN: the Source and
// Versions families are not season facts (episode file-name tokens
// and per-episode version counts say nothing about the season's
// poster), and a zero here is what keeps those families dark.

type seasonAggregation struct {
	SF             streamFacts      // merged per-episode stream facts (see above)
	Langs          episodeLangFacts // the language half, aggregateEpisodeLanguages verbatim
	RuntimeMinutes int              // episode durations summed (rounded once); 0 = none stated
	Unwatched      int              // aggregated episodes carrying no viewCount
	Scanned        int              // episodes aggregated (<= EpisodeLangCap)
}

func aggregateSeasonFacts(episodes []map[string]any, total int) seasonAggregation {
	out := seasonAggregation{Langs: aggregateEpisodeLanguages(episodes, total), Scanned: len(episodes)}
	sf := streamFacts{AudioLangs: out.Langs.AudioLangs, SubtitleLangs: out.Langs.SubtitleLangs}
	sf.HasSubs = out.Langs.HasSubs
	sf.SubtitleCount = out.Langs.SubtitleCount
	sf.SubtitleForced = out.Langs.SubtitleForced
	sf.SubtitleSDH = out.Langs.SubtitleSDH

	tierCount := map[string]int{}
	anyHDR10 := false
	best := audioStreamInfo{score: -1}
	var durationMs float64
	epFacts := make([]streamFacts, len(episodes))
	for i, ep := range episodes {
		esf, epBest := parseStreamFactsBest(ep)
		epFacts[i] = esf
		sf.AudioTracks += esf.AudioTracks
		if esf.ResolutionTier != "" {
			tierCount[esf.ResolutionTier]++
		}
		// Presence flags OR across episodes, exactly as they OR
		// across one item's Media entries. HDR10 keeps its item-level
		// meaning (PQ with no DV anywhere): when no episode carries
		// DV, an episode's HDR10 flag IS its PQ evidence, so the OR
		// below is exact — and when any episode does carry DV, the
		// merged HDR10 is false by the same rule either way.
		sf.DV = sf.DV || esf.DV
		anyHDR10 = anyHDR10 || esf.HDR10
		sf.HLG = sf.HLG || esf.HLG
		sf.Atmos = sf.Atmos || esf.Atmos
		sf.DTSX = sf.DTSX || esf.DTSX
		sf.DTSHDMA = sf.DTSHDMA || esf.DTSHDMA
		sf.TrueHD = sf.TrueHD || esf.TrueHD
		if epBest.score > best.score {
			best = epBest
		}
		if d := rawf(ep, "duration"); d > 0 {
			durationMs += d
		}
		if rawf(ep, "viewCount") <= 0 {
			out.Unwatched++
		}
	}
	sf.HDR10 = anyHDR10 && !sf.DV
	sf.HDRAny = sf.DV || anyHDR10 || sf.HLG

	// Dominant resolution tier: most episodes wins; a tie breaks to
	// the higher tier. Deterministic — tier ranks are distinct.
	winTier, winCount := "", 0
	for tier, n := range tierCount {
		if n > winCount || (n == winCount && tierRank[tier] > tierRank[winTier]) {
			winTier, winCount = tier, n
		}
	}
	sf.ResolutionTier = winTier
	// The codec rides with the winning tier (the same rule
	// parseStreamFacts applies within one item): the dominant codec
	// among episodes AT that tier, ties broken by first appearance in
	// episode order. Episodes at other tiers only vote if no
	// winning-tier episode names a codec at all.
	codecOf := func(onlyWinningTier bool) string {
		count := map[string]int{}
		firstSeen := map[string]int{}
		for i := range episodes {
			esf := epFacts[i]
			if esf.VideoCodec == "" || (onlyWinningTier && esf.ResolutionTier != winTier) {
				continue
			}
			if _, ok := firstSeen[esf.VideoCodec]; !ok {
				firstSeen[esf.VideoCodec] = i
			}
			count[esf.VideoCodec]++
		}
		winner, winnerCount := "", 0
		for codec, n := range count {
			if n > winnerCount || (n == winnerCount && firstSeen[codec] < firstSeen[winner]) {
				winner, winnerCount = codec, n
			}
		}
		return winner
	}
	if winTier != "" {
		sf.VideoCodec = codecOf(true)
		if sf.VideoCodec == "" {
			sf.VideoCodec = codecOf(false)
		}
	}
	if best.score >= 0 {
		sf.BestAudioLabel = best.label
		sf.BestAudioCodec = best.codec
		sf.BestAudioChannels = best.channels
		sf.BestAudioLang = best.lang
	}
	// AspectRatio is deliberately NOT aggregated: episodes can
	// disagree about their frame (a season that changed aspect
	// mid-run), and there is no honest dominant-value rule for it
	// here — the aspect family stays dark for seasons rather than
	// stating one episode's ratio as the season's.
	if durationMs > 0 {
		out.RuntimeMinutes = int((durationMs + 30000) / 60000)
	}
	out.SF = sf
	return out
}
