package advisor

// dubscan.go — port of src/advisor/dubscan.js: subbed vs dubbed
// detection. DETERMINISTIC, no AI, no API.
//
// The rule, exactly as specced:
//   - Audio tracks: streamType == 2, commentary/descriptive titles ignored.
//   - Language from languageCode (preferred), falling back to the
//     free-text language field mapped through commonLanguages.
//   - Japanese only -> subbed; English present -> dubbed; both -> dual;
//     neither identifiable -> unknown.
//   - Series verdict = the LEAST-dubbed episode:
//     subbed(0) < unknown(1) < dual(2) < dubbed(3); series takes the MIN.
//   - Anime scope guard: TMDb originalLanguage 'ja' + Animation genre,
//     OR the section is in settings.animeSections, OR per-item override.
//
// ScanTitle signature choice (FLAGGED): JS takes (item, tmdb, settings)
// for isAnime and scanTitle; this port's required API is
// IsAnime(item, settings) and ScanTitle(..., item, ...) — the TMDb data
// travels on item.TMDB, so the JS tmdb parameter is item.TMDB here.
//
// Sibling contracts (verified against internal/plex as landed):
// GetFullItem returns the raw Plex metadata object as map[string]any
// (that's the JS "raw Metadata[0], un-normalized, ON PURPOSE"), and
// GetChildrenRaw returns []map[string]any of {ratingKey, title, index,
// type}. The Stream type below is the advisor's view of one raw Plex
// Stream object (streamType / languageCode / language / title).

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"plexcollectionmanager/internal/plex"
)

// Stream is one Plex audio-capable stream as dubscan.js sees it:
// { streamType, languageCode, language, title } (+ codec, unused here).
type Stream struct {
	StreamType   int    `json:"streamType"`
	LanguageCode string `json:"languageCode"`
	Language     string `json:"language"`
	Title        string `json:"title"`
	Codec        string `json:"codec"`
}

var commentaryRe = regexp.MustCompile(`(?i)commentary|descriptive`)

// commonLanguages maps language spellings (code or free-text name) to
// the two codes that matter, exactly as dubscan.js COMMON_LANGUAGES.
var commonLanguages = map[string]string{
	"japanese": "ja", "jap": "ja", "jpn": "ja", "ja": "ja",
	"english": "en", "eng": "en", "en": "en",
}

func streamLang(s Stream) string {
	code := strings.ToLower(s.LanguageCode)
	if mapped, ok := commonLanguages[code]; ok {
		return mapped
	}
	if len(code) == 3 {
		// Bare ISO-639-2 we don't map: return as-is; only ja/en matter.
		return code
	}
	name := strings.ToLower(strings.TrimSpace(s.Language))
	if mapped, ok := commonLanguages[name]; ok {
		return mapped
	}
	return ""
}

func isCommentary(s Stream) bool {
	return commentaryRe.MatchString(s.Title)
}

// DubDetail is the detail object of a stream verdict.
type DubDetail struct {
	AudioTracks   int  `json:"audioTracks"`
	HasJapanese   bool `json:"hasJapanese"`
	HasEnglish    bool `json:"hasEnglish"`
	UnclearTracks int  `json:"unclearTracks"`
	TracksUnclear bool `json:"tracksUnclear"`
}

// StreamVerdict is verdictForStreams' result: { verdict, detail }.
type StreamVerdict struct {
	Verdict string    `json:"verdict"`
	Detail  DubDetail `json:"detail"`
}

// VerdictForStreams is the pure heart of dubscan.js: audio streams ->
// verdict. Non-audio and commentary streams are ignored.
func VerdictForStreams(streams []Stream) StreamVerdict {
	audio := make([]Stream, 0, len(streams))
	for _, s := range streams {
		if s.StreamType == 2 && !isCommentary(s) {
			audio = append(audio, s)
		}
	}
	hasJa, hasEn, unclear := false, false, 0
	for _, s := range audio {
		lang := streamLang(s)
		if lang == "ja" || lang == "jpn" {
			hasJa = true
		} else if lang == "en" || lang == "eng" {
			hasEn = true
		} else {
			unclear++
		}
	}
	verdict := "unknown"
	if hasEn && hasJa {
		verdict = "dual"
	} else if hasEn {
		verdict = "dubbed"
	} else if hasJa {
		verdict = "subbed"
	}
	return StreamVerdict{
		Verdict: verdict,
		Detail: DubDetail{
			AudioTracks:   len(audio),
			HasJapanese:   hasJa,
			HasEnglish:    hasEn,
			UnclearTracks: unclear,
			TracksUnclear: verdict == "unknown" && len(audio) > 0,
		},
	}
}

// VerdictRank orders verdicts least- to most-dubbed (JS VERDICT_RANK).
var VerdictRank = map[string]int{"subbed": 0, "unknown": 1, "dual": 2, "dubbed": 3}

// SeriesResult is seriesVerdict's result: { verdict, episodes }.
type SeriesResult struct {
	Verdict  string `json:"verdict"`
	Episodes int    `json:"episodes"`
}

// SeriesVerdict rolls episode verdicts up to a series verdict — the
// least-dubbed episode wins.
func SeriesVerdict(episodeVerdicts []string) SeriesResult {
	if len(episodeVerdicts) == 0 {
		return SeriesResult{Verdict: "unknown", Episodes: 0}
	}
	worst := "dubbed"
	for _, v := range episodeVerdicts {
		if VerdictRank[v] < VerdictRank[worst] {
			worst = v
		}
	}
	return SeriesResult{Verdict: worst, Episodes: len(episodeVerdicts)}
}

var animationNameRe = regexp.MustCompile(`(?i)animation`)

// IsAnime is the dubscan.js isAnime heuristic. The JS signature is
// isAnime(item, tmdb, settings); here tmdb is item.TMDB (see the file
// header for the flagged signature choice).
func IsAnime(item Item, s Settings) bool {
	rk := item.RatingKey
	if s.AnimeOverrides != nil {
		if v, ok := s.AnimeOverrides[rk]; ok {
			return v // the owner's explicit call wins
		}
	}
	for _, k := range s.AnimeSections {
		if k == item.SectionKey {
			return true
		}
	}
	tmdb := item.TMDB
	if tmdb == nil {
		return false
	}
	isJa := tmdb.OriginalLanguage == "ja"
	isAnim := false
	for _, g := range tmdb.Genres {
		if g.ID == 16 || animationNameRe.MatchString(g.Name) {
			isAnim = true
			break
		}
	}
	return isJa && isAnim
}

// rawList / rawMap defensively walk the raw Plex JSON maps
// GetFullItem / GetChildrenRaw return (any non-matching shape -> empty).
func rawList(v any) []any {
	if list, ok := v.([]any); ok {
		return list
	}
	return nil
}

func rawMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func rawString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatInt(int64(t), 10)
	case json.Number:
		return t.String()
	default:
		return ""
	}
}

func rawInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	default:
		return 0
	}
}

func streamFromRaw(m map[string]any) Stream {
	return Stream{
		StreamType:   rawInt(m["streamType"]),
		LanguageCode: rawString(m["languageCode"]),
		Language:     rawString(m["language"]),
		Title:        rawString(m["title"]),
		Codec:        rawString(m["codec"]),
	}
}

// AudioStreamsOf collects audio Stream entries from a raw Plex full
// item (Media[] -> Part[] -> Stream[], streamType == 2). Defensive:
// any shape -> whatever was collected so far ([] for nil/garbage).
func AudioStreamsOf(metadata map[string]any) []Stream {
	out := []Stream{}
	for _, mediaV := range rawList(metadata["Media"]) {
		media := rawMap(mediaV)
		for _, partV := range rawList(media["Part"]) {
			part := rawMap(partV)
			for _, sV := range rawList(part["Stream"]) {
				s := streamFromRaw(rawMap(sV))
				if s.StreamType == 2 {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// childInfo is the advisor's view of one raw child entry from
// GetChildrenRaw: { ratingKey, title, index, type } in the JS.
type childInfo struct {
	RatingKey string
	Title     string
	Index     *int
	Type      string
}

func childFromRaw(m map[string]any) childInfo {
	c := childInfo{
		RatingKey: rawString(m["ratingKey"]),
		Title:     rawString(m["title"]),
		Type:      rawString(m["type"]),
	}
	if _, ok := m["index"]; ok && m["index"] != nil {
		idx := rawInt(m["index"])
		c.Index = &idx
	}
	return c
}

// EpisodeVerdict is one episode's entry in a season's verdict list.
type EpisodeVerdict struct {
	RatingKey string `json:"ratingKey"`
	Title     string `json:"title"`
	Verdict   string `json:"verdict"`
}

// SeasonVerdict is one season's roll-up inside a show Verdict.
type SeasonVerdict struct {
	Season          string           `json:"season"`
	SeasonRatingKey string           `json:"seasonRatingKey,omitempty"`
	Verdict         string           `json:"verdict"`
	Episodes        int              `json:"episodes"`
	EpisodeVerdicts []EpisodeVerdict `json:"episodeVerdicts,omitempty"`
}

// Verdict is scanTitle's return shape, exactly as dubscan.js:
// { ratingKey, type, verdict, detail (movies), anime, perSeason, note,
// cached (only when served from cache) }.
type Verdict struct {
	RatingKey string          `json:"ratingKey"`
	Type      string          `json:"type"`
	Verdict   string          `json:"verdict"`
	Detail    *DubDetail      `json:"detail,omitempty"`
	Anime     bool            `json:"anime"`
	PerSeason []SeasonVerdict `json:"perSeason"`
	Note      string          `json:"note"`
	Cached    bool            `json:"cached,omitempty"`
}

// episodeCacheEntry is the per-episode cache record: { ratingKey,
// verdict, detail } in the 'dub' namespace (90d TTL).
type episodeCacheEntry struct {
	RatingKey string    `json:"ratingKey"`
	Verdict   string    `json:"verdict"`
	Detail    DubDetail `json:"detail"`
}

// showCacheEntry is the cached series roll-up genDubSub reads:
// { ratingKey, verdict, perSeason: [{ season, verdict, episodes }] }.
type showCacheEntry struct {
	RatingKey string `json:"ratingKey"`
	Verdict   string `json:"verdict"`
	PerSeason []struct {
		Season   string `json:"season"`
		Verdict  string `json:"verdict"`
		Episodes int    `json:"episodes"`
	} `json:"perSeason"`
}

func storeSetJSON(store *Store, ns, key string, v any) {
	if store == nil {
		return
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	store.Set(ns, key, raw)
}

func seasonTitleOf(season childInfo) string {
	if season.Title != "" {
		return season.Title
	}
	if season.Index != nil {
		return "Season " + strconv.Itoa(*season.Index)
	}
	return "Specials"
}

// ScanTitle lazily scans one show (or movie) for its dub verdict,
// caching per episode/movie ratingKey in the store's 'dub' namespace
// (90d TTL). force rescans instead of reading the cache.
func ScanTitle(ctx context.Context, plexClient *plex.Client, server *plex.Server, item Item, store *Store, settings Settings, force bool) (*Verdict, error) {
	ratingKey := item.RatingKey
	anime := IsAnime(item, settings)

	if item.Type == "movie" {
		if !force && store != nil {
			if raw, hit := store.Get("dub", "movie/"+ratingKey); hit {
				var cached Verdict
				if err := json.Unmarshal(raw, &cached); err == nil {
					cached.Cached = true
					return &cached, nil
				}
			}
		}
		full, err := plexClient.GetFullItem(ctx, server, ratingKey)
		if err != nil {
			return nil, err
		}
		r := VerdictForStreams(AudioStreamsOf(full))
		detail := r.Detail
		out := &Verdict{
			RatingKey: ratingKey,
			Type:      "movie",
			Verdict:   r.Verdict,
			Detail:    &detail,
			Anime:     anime,
			PerSeason: []SeasonVerdict{},
			Note:      "",
		}
		storeSetJSON(store, "dub", "movie/"+ratingKey, out)
		return out, nil
	}

	// show: drill seasons -> episodes (children are batched per season,
	// NOT one call per episode for the list; one GetFullItem per
	// uncached episode, as in JS).
	seasonsRaw, err := plexClient.GetChildrenRaw(ctx, server, ratingKey)
	if err != nil {
		return nil, err
	}
	perSeason := []SeasonVerdict{}
	allEpisodeVerdicts := []string{}
	for _, seasonRaw := range seasonsRaw {
		season := childFromRaw(seasonRaw)
		seasonRk := season.RatingKey
		seasonTitle := season.Title
		if seasonTitle == "" {
			seasonTitle = seasonTitleOf(season)
		}
		episodesRaw, err := plexClient.GetChildrenRaw(ctx, server, seasonRk)
		if err != nil {
			return nil, err
		}
		episodeVerdicts := []EpisodeVerdict{}
		for _, epRaw := range episodesRaw {
			ep := childFromRaw(epRaw)
			epRk := ep.RatingKey
			v := ""
			if !force && store != nil {
				if raw, hit := store.Get("dub", "ep/"+epRk); hit {
					var entry episodeCacheEntry
					if err := json.Unmarshal(raw, &entry); err == nil {
						v = entry.Verdict
					}
				}
			}
			if v == "" {
				full, err := plexClient.GetFullItem(ctx, server, epRk)
				if err != nil {
					return nil, err
				}
				r := VerdictForStreams(AudioStreamsOf(full))
				v = r.Verdict
				storeSetJSON(store, "dub", "ep/"+epRk, episodeCacheEntry{RatingKey: epRk, Verdict: v, Detail: r.Detail})
			}
			episodeVerdicts = append(episodeVerdicts, EpisodeVerdict{RatingKey: epRk, Title: ep.Title, Verdict: v})
			allEpisodeVerdicts = append(allEpisodeVerdicts, v)
		}
		verdicts := make([]string, len(episodeVerdicts))
		for i, e := range episodeVerdicts {
			verdicts[i] = e.Verdict
		}
		sv := SeriesVerdict(verdicts)
		perSeason = append(perSeason, SeasonVerdict{
			Season:          seasonTitle,
			SeasonRatingKey: seasonRk,
			Verdict:         sv.Verdict,
			Episodes:        len(episodeVerdicts),
			EpisodeVerdicts: episodeVerdicts,
		})
	}
	series := SeriesVerdict(allEpisodeVerdicts)
	note := ""
	if !anime {
		note = "Not flagged as anime — verdict shown for reference; excluded from dub/sub groupings."
	}
	out := &Verdict{
		RatingKey: ratingKey,
		Type:      "show",
		Verdict:   series.Verdict,
		Anime:     anime,
		PerSeason: perSeason,
		Note:      note,
	}
	// Cache the series roll-up too — genDubSub reads show/{rk} without
	// re-drilling seasons.
	if store != nil {
		rollup := showCacheEntry{RatingKey: ratingKey, Verdict: series.Verdict}
		for _, ps := range perSeason {
			rollup.PerSeason = append(rollup.PerSeason, struct {
				Season   string `json:"season"`
				Verdict  string `json:"verdict"`
				Episodes int    `json:"episodes"`
			}{Season: ps.Season, Verdict: ps.Verdict, Episodes: ps.Episodes})
		}
		storeSetJSON(store, "dub", "show/"+ratingKey, rollup)
	}
	return out, nil
}
