package advisor

// Fixture-only tests (AGENTS.md: no real Plex account, no live APIs).
// Many cases are ports of the advisor regression tests in
// v0.4/regression.test.cjs; the rest cover the Go-specific store and the
// v0.4 Plex-native generators. Meta/plex clients are concrete types in
// the sibling packages, so network-backed generators are tested through
// their extracted pure helpers (franchiseCard, annotateTopEntries,
// top100Cards) and through nil-client paths in GenerateAll.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"plexcollectionmanager/internal/meta"
	"plexcollectionmanager/internal/plex"
)

func f64(v float64) *float64 { return &v }
func iptr(v int) *int        { return &v }
func i64ptr(v int64) *int64  { return &v }

func movieItem(rk, title string) Item {
	return Item{LibraryItem: plex.LibraryItem{
		RatingKey: rk, Title: title, Type: "movie", SectionKey: "1",
	}}
}

func findCard(cards []Card, id string) *Card {
	for i := range cards {
		if cards[i].ID == id {
			return &cards[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Store: TTL freshness (ported regression: 'cache TTL: isFresh ...')
// ---------------------------------------------------------------------------

func TestIsFreshPerNamespace(t *testing.T) {
	now := time.Now().UnixMilli()
	if !IsFresh(now-1000, TTLMS["tmdb"], now) {
		t.Fatal("tmdb 1s old should be fresh")
	}
	if IsFresh(now-31*DayMS, TTLMS["tmdb"], now) {
		t.Fatal("tmdb 31d old should be stale")
	}
	if IsFresh(now-25*60*60*1000, TTLMS["trakt"], now) {
		t.Fatal("trakt 25h old should be stale (24h TTL)")
	}
	if !IsFresh(now-23*60*60*1000, TTLMS["trakt"], now) {
		t.Fatal("trakt 23h old should be fresh")
	}
	if !IsFresh(now-89*DayMS, TTLMS["dub"], now) || IsFresh(now-91*DayMS, TTLMS["dub"], now) {
		t.Fatal("dub TTL should be 90d")
	}
	if TTLMS["tmdb-lists"] != DayMS || TTLMS["omdb"] != 30*DayMS || TTLMS["anilist"] != 30*DayMS {
		t.Fatalf("TTL map drifted: %v", TTLMS)
	}
}

func TestStoreCacheFreshVsExpiredPerNamespace(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Now()
	old := cacheEntry{FetchedAt: now.Add(-25 * time.Hour).UnixMilli(), Data: json.RawMessage(`{"x":1}`)}
	if err := atomicWrite(store.cachePath("trakt", "lists/test"), old); err != nil {
		t.Fatal(err)
	}
	if _, hit := store.Get("trakt", "lists/test"); hit {
		t.Fatal("trakt entry 25h old must miss (24h TTL)")
	}
	if err := atomicWrite(store.cachePath("tmdb", "movie/1"), old); err != nil {
		t.Fatal(err)
	}
	if _, hit := store.Get("tmdb", "movie/1"); !hit {
		t.Fatal("tmdb entry 25h old must hit (30d TTL)")
	}
	// Round-trip via Set/Get.
	store.Set("omdb", "rating/tt1", json.RawMessage(`{"imdbRating":"8.8"}`))
	raw, hit := store.Get("omdb", "rating/tt1")
	if !hit || !strings.Contains(string(raw), "8.8") {
		t.Fatalf("omdb round-trip failed: hit=%v raw=%s", hit, raw)
	}
	if _, hit := store.Get("nope-ns", "k"); hit {
		t.Fatal("unknown namespace must miss")
	}
	if _, hit := store.Get("dub", "movie/absent"); hit {
		t.Fatal("absent key must miss")
	}
}

func TestStoreDismissRoundTrip(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Dismiss("genre:1:Action"); err != nil {
		t.Fatal(err)
	}
	if err := store.Dismiss("genre:1:Action"); err != nil { // no duplicate
		t.Fatal(err)
	}
	if err := store.Dismiss("decade:1:1990"); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetDismissals()
	if err != nil || !reflect.DeepEqual(got, []string{"genre:1:Action", "decade:1:1990"}) {
		t.Fatalf("dismissals = %v, %v", got, err)
	}
	if err := store.Undismiss("genre:1:Action"); err != nil {
		t.Fatal(err)
	}
	got, _ = store.GetDismissals()
	if !reflect.DeepEqual(got, []string{"decade:1:1990"}) {
		t.Fatalf("after undismiss = %v", got)
	}
	if err := store.ResetDismissals(); err != nil {
		t.Fatal(err)
	}
	got, _ = store.GetDismissals()
	if len(got) != 0 {
		t.Fatalf("after reset = %v", got)
	}
}

func TestStoreDraftRoundTrip(t *testing.T) {
	store := NewStore(t.TempDir())
	saved, err := store.SaveDraft(Draft{Title: "Nolan", MemberRatingKeys: []string{"1", "2"}, SectionKey: "1", Type: "1", Reason: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(saved.ID, "draft-") {
		t.Fatalf("generated id = %q", saved.ID)
	}
	if saved.SavedAt == "" {
		t.Fatal("savedAt not stamped")
	}
	// Update in place under the same id.
	saved2, err := store.SaveDraft(Draft{ID: saved.ID, Title: "Nolan v2", MemberRatingKeys: []string{"3"}})
	if err != nil || saved2.Title != "Nolan v2" {
		t.Fatalf("update = %+v, %v", saved2, err)
	}
	drafts, _ := store.GetDrafts()
	if len(drafts) != 1 || drafts[0].Title != "Nolan v2" || !reflect.DeepEqual(drafts[0].MemberRatingKeys, []string{"3"}) {
		t.Fatalf("drafts = %+v", drafts)
	}
	if err := store.DeleteDraft(saved.ID); err != nil {
		t.Fatal(err)
	}
	drafts, _ = store.GetDrafts()
	if len(drafts) != 0 {
		t.Fatalf("after delete = %+v", drafts)
	}
}

func TestStoreSettingsDefaultsMerge(t *testing.T) {
	store := NewStore(t.TempDir())
	got, err := store.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, DefaultSettings()) {
		t.Fatalf("fresh settings = %+v, want defaults %+v", got, DefaultSettings())
	}
	merged, err := store.SetSettings(map[string]any{"genreThreshold": 3, "imdbBadges": false})
	if err != nil {
		t.Fatal(err)
	}
	if merged.GenreThreshold != 3 || merged.IMDBBadges != false {
		t.Fatalf("patched settings = %+v", merged)
	}
	if merged.DecadeThreshold != 8 || merged.HiddenGemRating != 7.5 || merged.ActorThreshold != 3 || merged.StudioThreshold != 4 {
		t.Fatalf("defaults not preserved through merge: %+v", merged)
	}
	// A second patch merges over the stored values, not over defaults.
	merged2, err := store.SetSettings(map[string]any{"actorThreshold": 9})
	if err != nil || merged2.ActorThreshold != 9 || merged2.GenreThreshold != 3 {
		t.Fatalf("second merge = %+v, %v", merged2, err)
	}
}

func TestStoreScanSnapshotRoundTrip(t *testing.T) {
	store := NewStore(t.TempDir())
	scan := &Scan{
		MachineID: "m1", ScannedAt: 123, ItemCount: 1,
		Sections:       []plex.Section{{Key: "1", Title: "Movies", Type: "movie"}},
		Items:          []Item{movieItem("7", "Seven")},
		TMDBKeyPresent: true,
	}
	if err := store.SetScan("m1", scan); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetScan("m1")
	if err != nil || got == nil {
		t.Fatalf("GetScan = %v, %v", got, err)
	}
	if got.MachineID != "m1" || got.ItemCount != 1 || len(got.Items) != 1 || got.Items[0].Title != "Seven" || !got.TMDBKeyPresent {
		t.Fatalf("scan round-trip drifted: %+v", got)
	}
	if missing, _ := store.GetScan("other"); missing != nil {
		t.Fatalf("unknown machine scan = %+v, want nil", missing)
	}
}

func TestStoreClearCachePreservesStoreFile(t *testing.T) {
	store := NewStore(t.TempDir())
	store.Set("tmdb", "movie/1", json.RawMessage(`{}`))
	if err := store.Dismiss("x:1"); err != nil {
		t.Fatal(err)
	}
	stats, _ := store.CacheSize()
	if stats.Files < 1 || stats.Bytes == 0 {
		t.Fatalf("cacheSize before clear = %+v", stats)
	}
	if err := store.ClearCache(); err != nil {
		t.Fatal(err)
	}
	stats, _ = store.CacheSize()
	if stats.Files != 0 {
		t.Fatalf("cacheSize after clear = %+v", stats)
	}
	dismissals, _ := store.GetDismissals()
	if !reflect.DeepEqual(dismissals, []string{"x:1"}) {
		t.Fatalf("clearCache must preserve dismissals, got %v", dismissals)
	}
	if _, hit := store.Get("tmdb", "movie/1"); hit {
		t.Fatal("cleared cache must miss")
	}
}

// ---------------------------------------------------------------------------
// Dub classification (ported regression tests)
// ---------------------------------------------------------------------------

func jaTrack() Stream { return Stream{StreamType: 2, LanguageCode: "jpn"} }
func enTrack() Stream { return Stream{StreamType: 2, LanguageCode: "eng"} }
func TestDubVerdicts(t *testing.T) {
	if v := VerdictForStreams([]Stream{jaTrack()}); v.Verdict != "subbed" {
		t.Fatalf("ja only = %s, want subbed", v.Verdict)
	}
	if v := VerdictForStreams([]Stream{jaTrack(), enTrack()}); v.Verdict != "dual" {
		t.Fatalf("ja+en = %s, want dual", v.Verdict)
	}
	if v := VerdictForStreams([]Stream{enTrack()}); v.Verdict != "dubbed" {
		t.Fatalf("en only = %s, want dubbed", v.Verdict)
	}
	// Commentary / descriptive English must not count.
	commentary := enTrack()
	commentary.Title = "Director's Commentary"
	if v := VerdictForStreams([]Stream{jaTrack(), commentary}); v.Verdict != "subbed" {
		t.Fatalf("commentary case = %s, want subbed", v.Verdict)
	}
	descriptive := enTrack()
	descriptive.Title = "Descriptive Audio Service"
	if v := VerdictForStreams([]Stream{jaTrack(), descriptive}); v.Verdict != "subbed" {
		t.Fatalf("descriptive case = %s, want subbed", v.Verdict)
	}
	// Free-text language fallback; no language info at all -> unknown.
	if v := VerdictForStreams([]Stream{{StreamType: 2, Language: "Japanese"}}); v.Verdict != "subbed" {
		t.Fatalf("language-name fallback = %s, want subbed", v.Verdict)
	}
	v := VerdictForStreams([]Stream{{StreamType: 2}})
	if v.Verdict != "unknown" || !v.Detail.TracksUnclear {
		t.Fatalf("no-language case = %+v, want unknown + tracksUnclear", v)
	}
	// Non-audio streams are ignored.
	video := Stream{StreamType: 1, LanguageCode: "eng"}
	if v := VerdictForStreams([]Stream{video, jaTrack()}); v.Verdict != "subbed" {
		t.Fatalf("video ignored case = %s, want subbed", v.Verdict)
	}
	// Detail counts.
	v = VerdictForStreams([]Stream{jaTrack(), enTrack(), {StreamType: 2, LanguageCode: "fra"}})
	if v.Detail.AudioTracks != 3 || !v.Detail.HasJapanese || !v.Detail.HasEnglish || v.Detail.UnclearTracks != 1 {
		t.Fatalf("detail = %+v", v.Detail)
	}
}

func TestSeriesVerdictLeastDubbed(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"dubbed", "dubbed"}, "dubbed"},
		{[]string{"dubbed", "subbed"}, "subbed"},
		{[]string{"dubbed", "unknown"}, "unknown"},
		{[]string{"dual", "dubbed"}, "dual"},
		{[]string{}, "unknown"},
	}
	for _, c := range cases {
		if got := SeriesVerdict(c.in); got.Verdict != c.want || got.Episodes != len(c.in) {
			t.Fatalf("SeriesVerdict(%v) = %+v, want %s", c.in, got, c.want)
		}
	}
}

func TestAudioStreamsOfDrills(t *testing.T) {
	// Raw Plex JSON shape, as GetFullItem returns it (numbers float64).
	stream := func(streamType float64, lang string) map[string]any {
		return map[string]any{"streamType": streamType, "languageCode": lang}
	}
	full := map[string]any{"Media": []any{
		map[string]any{"Part": []any{
			map[string]any{"Stream": []any{stream(2, "jpn"), stream(1, "eng")}},
		}},
		map[string]any{"Part": []any{}},
	}}
	got := AudioStreamsOf(full)
	if len(got) != 1 || got[0].LanguageCode != "jpn" {
		t.Fatalf("audioStreamsOf = %+v, want 1 jpn audio stream", got)
	}
	if got := AudioStreamsOf(nil); len(got) != 0 {
		t.Fatalf("nil full item = %d streams", len(got))
	}
	if got := AudioStreamsOf(map[string]any{}); len(got) != 0 {
		t.Fatalf("empty full item = %d streams", len(got))
	}
	if got := AudioStreamsOf(map[string]any{"Media": "garbage"}); len(got) != 0 {
		t.Fatalf("garbage shape = %d streams", len(got))
	}
}

func TestIsAnimeHeuristic(t *testing.T) {
	jaAnim := &meta.Details{OriginalLanguage: "ja", Genres: []meta.Genre{{ID: 16, Name: "Animation"}}}
	usAnim := &meta.Details{OriginalLanguage: "en", Genres: []meta.Genre{{ID: 16, Name: "Animation"}}}
	base := movieItem("1", "X")
	base.SectionKey = "2"

	item := base
	item.TMDB = jaAnim
	if !IsAnime(item, Settings{}) {
		t.Fatal("ja+Animation should be anime")
	}
	item.TMDB = usAnim
	if IsAnime(item, Settings{}) {
		t.Fatal("english-original animation is not anime")
	}
	item.TMDB = nil
	if !IsAnime(item, Settings{AnimeSections: []string{"2"}}) {
		t.Fatal("anime section membership should flag anime")
	}
	item.TMDB = jaAnim
	if IsAnime(item, Settings{AnimeOverrides: map[string]bool{"1": false}}) {
		t.Fatal("explicit false override wins")
	}
	item.TMDB = nil
	if !IsAnime(item, Settings{AnimeOverrides: map[string]bool{"1": true}}) {
		t.Fatal("explicit true override wins without tmdb")
	}
	// Genre matched by NAME when the id differs.
	item.TMDB = &meta.Details{OriginalLanguage: "ja", Genres: []meta.Genre{{ID: 99, Name: "Animation"}}}
	if !IsAnime(item, Settings{}) {
		t.Fatal("ja + genre named Animation should be anime")
	}
}

// ---------------------------------------------------------------------------
// Generators on fixture scans
// ---------------------------------------------------------------------------

func TestGenClustersPlexTagsZeroTMDB(t *testing.T) {
	// The v0.4 headline behavior: clusters fire from Plex genre tags
	// with ZERO TMDb data.
	a := movieItem("a", "A")
	a.Tags.Genre = []string{"Action"}
	b := movieItem("b", "B")
	b.Tags.Genre = []string{"Action"}
	scan := &Scan{
		Sections: []plex.Section{{Key: "1", Title: "Movies", Type: "movie"}},
		Items:    []Item{a, b},
	}
	cards := genClusters(scan, GenContext{Settings: Settings{GenreThreshold: 2, DecadeThreshold: 99}})
	if len(cards) != 1 || cards[0].Title != "Action (Movies)" {
		t.Fatalf("cards = %+v", cards)
	}
	if !reflect.DeepEqual(cards[0].MemberRatingKeys, []string{"a", "b"}) {
		t.Fatalf("members = %v", cards[0].MemberRatingKeys)
	}
}

func TestGenClustersTMDBFallbackAndDecades(t *testing.T) {
	mk := func(rk string, year int) Item {
		it := movieItem(rk, "T"+rk)
		it.Year = iptr(year)
		it.TMDB = &meta.Details{Genres: []meta.Genre{{Name: "Comedy"}}}
		return it
	}
	scan := &Scan{
		Sections: []plex.Section{{Key: "1", Title: "Movies", Type: "movie"}},
		Items:    []Item{mk("a", 1994), mk("b", 1999)},
	}
	cards := genClusters(scan, GenContext{Settings: Settings{GenreThreshold: 2, DecadeThreshold: 2}})
	if findCard(cards, "genre:1:Comedy") == nil {
		t.Fatalf("TMDb genre fallback card missing: %+v", cards)
	}
	decade := findCard(cards, "decade:1:1990")
	if decade == nil || decade.Title != "1990s (Movies)" || len(decade.MemberRatingKeys) != 2 {
		t.Fatalf("decade card = %+v", decade)
	}
	// Below threshold: no card.
	thin := genClusters(scan, GenContext{Settings: Settings{GenreThreshold: 3, DecadeThreshold: 3}})
	if len(thin) != 0 {
		t.Fatalf("below-threshold cards = %+v", thin)
	}
}

func TestGenPeopleStudiosContentRatingsUnwatched(t *testing.T) {
	mk := func(rk string, vc int, cr, studio string) Item {
		it := movieItem(rk, "T"+rk)
		it.ViewCount = vc
		it.ContentRating = cr
		it.Tags.Studio = []string{studio}
		it.Tags.Director = []string{"Nolan"}
		it.Tags.Actor = []string{"Bale"}
		return it
	}
	scan := &Scan{
		Sections: []plex.Section{{Key: "1", Title: "Movies", Type: "movie"}},
		Items: []Item{
			mk("a", 0, "PG-13", "Pixar"), mk("b", 0, "PG-13", "Pixar"),
			mk("c", 3, "R", "A24"), mk("d", 0, "PG-13", "Pixar"),
			mk("e", 0, "PG-13", "Pixar"), mk("f", 0, "PG", "Pixar"),
		},
	}
	gc := GenContext{Settings: Settings{DirectorThreshold: 2, ActorThreshold: 2, StudioThreshold: 2}}
	people := genPeople(scan, gc)
	if findCard(people, "director:1:Nolan") == nil || findCard(people, "actor:1:Bale") == nil {
		t.Fatalf("people cards = %+v", people)
	}
	studios := genStudios(scan, gc)
	st := findCard(studios, "studio:1:Pixar")
	if st == nil || len(st.MemberRatingKeys) != 5 {
		t.Fatalf("studio card = %+v", st)
	}
	crs := genContentRatings(scan)
	cr := findCard(crs, "contentrating:1:PG-13")
	if cr == nil || len(cr.MemberRatingKeys) != 4 {
		t.Fatalf("content rating card = %+v", cr)
	}
	uw := genUnwatched(scan)
	if len(uw) != 1 || len(uw[0].MemberRatingKeys) != 5 {
		t.Fatalf("unwatched = %+v", uw)
	}
	if !IsValidCard(uw[0]) {
		t.Fatal("unwatched card should satisfy IsValidCard")
	}
}

func TestGenRecentHighlyRatedShortFilms(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	scan := &Scan{Sections: []plex.Section{{Key: "1", Title: "Movies", Type: "movie"}}}
	for n := 0; n < 6; n++ {
		it := movieItem(string(rune('a'+n)), "T")
		it.AddedAt = now.Unix() - int64(n)*86400
		scan.Items = append(scan.Items, it)
	}
	old := movieItem("old", "Old")
	old.AddedAt = now.Unix() - 400*86400
	scan.Items = append(scan.Items, old)
	recent := genRecentAt(scan, now)
	if len(recent) != 1 || len(recent[0].MemberRatingKeys) != 6 || recent[0].MemberRatingKeys[0] != "a" {
		t.Fatalf("recent = %+v (newest first, 60d window)", recent)
	}

	rated := &Scan{Sections: []plex.Section{{Key: "1", Title: "Movies", Type: "movie"}}}
	for n := 0; n < 8; n++ {
		it := movieItem(string(rune('a'+n)), "H")
		it.PlexRating = f64(8.5)
		it.DurationMin = iptr(90)
		rated.Items = append(rated.Items, it)
	}
	low := movieItem("low", "L")
	low.PlexRating = f64(5.0)
	low.DurationMin = iptr(180)
	rated.Items = append(rated.Items, low)
	noRating := movieItem("nr", "N")
	rated.Items = append(rated.Items, noRating)

	hr := genHighlyRated(rated)
	if len(hr) != 1 || len(hr[0].MemberRatingKeys) != 8 {
		t.Fatalf("highlyRated = %+v", hr)
	}
	thinRated := &Scan{Sections: rated.Sections, Items: rated.Items[:7]}
	if got := genHighlyRated(thinRated); len(got) != 0 {
		t.Fatalf("highlyRated below count = %+v", got)
	}
	sf := genShortFilms(rated)
	if len(sf) != 1 || len(sf[0].MemberRatingKeys) != 8 {
		t.Fatalf("shortFilms = %+v", sf)
	}
	tvScan := &Scan{Sections: []plex.Section{{Key: "1", Title: "TV", Type: "show"}}, Items: rated.Items}
	if got := genShortFilms(tvScan); len(got) != 0 {
		t.Fatalf("shortFilms must be movies-only, got %+v", got)
	}
}

func TestGenHiddenGemsAndSkipWithoutTMDB(t *testing.T) {
	mk := func(rk string, va, pop float64, vc int) Item {
		it := movieItem(rk, "M"+rk)
		it.ViewCount = vc
		it.TMDB = &meta.Details{VoteAverage: f64(va), Popularity: f64(pop)}
		return it
	}
	scan := &Scan{
		Sections: []plex.Section{{Key: "1", Title: "Movies", Type: "movie"}},
		Items: []Item{
			mk("1", 8.5, 5, 0), mk("2", 9.0, 500, 0), mk("3", 8.0, 300, 2),
			mk("4", 6.0, 200, 0), mk("5", 8.2, 4, 0),
		},
	}
	cards := genHiddenGems(scan, GenContext{Settings: Settings{HiddenGemRating: 7.5}})
	if len(cards) != 1 {
		t.Fatalf("hiddenGems = %+v", cards)
	}
	got := append([]string{}, cards[0].MemberRatingKeys...)
	// Sorted by voteAverage desc: '1' (8.5) then '5' (8.2).
	if !reflect.DeepEqual(got, []string{"1", "5"}) {
		t.Fatalf("gem members = %v", got)
	}
	// No TMDb at all -> no gems, no crash.
	bare := &Scan{Sections: scan.Sections, Items: []Item{movieItem("x", "X")}}
	if got := genHiddenGems(bare, GenContext{}); len(got) != 0 {
		t.Fatalf("hiddenGems without tmdb = %+v", got)
	}
}

func TestFranchiseCardCompletionMath(t *testing.T) {
	// Ported regression: 2 owned of 3 parts.
	items := []Item{movieItem("1", "A"), movieItem("2", "B")}
	items[0].Guids = []string{"tmdb://a"}
	items[1].Guids = []string{"tmdb://b"}
	parts := []meta.CollectionPart{{TMDBID: "a", Title: "A"}, {TMDBID: "b", Title: "B"}, {TMDBID: "c", Title: "C"}}
	card, pct := franchiseCard("c1", "Saga One", items, parts)
	if !strings.Contains(card.Subtitle, "You own 2 of 3") {
		t.Fatalf("subtitle = %q", card.Subtitle)
	}
	if !strings.Contains(card.Subtitle, "missing: C") {
		t.Fatalf("subtitle missing list = %q", card.Subtitle)
	}
	if card.Confidence != "deterministic" || pct != 67 {
		t.Fatalf("card = %+v pct=%v", card, pct)
	}
	if !IsValidCard(card) {
		t.Fatal("franchise card should be valid")
	}
	// No parts data -> owned-only total, "complete!".
	card2, _ := franchiseCard("c1", "Saga One", items, nil)
	if !strings.Contains(card2.Subtitle, "You own 2 of 2 — complete!") {
		t.Fatalf("owned-only subtitle = %q", card2.Subtitle)
	}
}

func TestSeasonalOnlyInMatchingMonths(t *testing.T) {
	mk := func(rk string) Item {
		it := movieItem(rk, "Scary"+rk)
		it.TMDB = &meta.Details{Genres: []meta.Genre{{ID: 27, Name: "Horror"}}}
		return it
	}
	scan := &Scan{
		Sections: []plex.Section{{Key: "1", Title: "Movies", Type: "movie"}},
		Items:    []Item{mk("1"), mk("2"), mk("3")},
	}
	oct := genSeasonal(scan, time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC))
	if len(oct) != 1 || !strings.Contains(oct[0].Title, "Spooky season") {
		t.Fatalf("october seasonal = %+v", oct)
	}
	mar := genSeasonal(scan, time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC))
	if len(mar) != 0 {
		t.Fatalf("march seasonal = %+v", mar)
	}
}

func TestTvCompletenessTolerance(t *testing.T) {
	mk := func(rk string, seasons, tmdbSeasons, views int) Item {
		it := Item{LibraryItem: plex.LibraryItem{
			RatingKey: rk, Title: "S" + rk, Type: "show", SectionKey: "2",
			ViewCount: views, ChildCount: seasons,
		}}
		it.TMDB = &meta.Details{NumberOfSeasons: i64ptr(int64(tmdbSeasons))}
		return it
	}
	scan := &Scan{
		Sections: []plex.Section{{Key: "2", Title: "TV", Type: "show"}},
		Items:    []Item{mk("1", 5, 5, 0), mk("2", 4, 5, 0), mk("3", 2, 5, 3), mk("4", 1, 6, 5)},
	}
	cards := genTvCompleteness(scan)
	complete := findCard(cards, "tv:complete")
	binge := findCard(cards, "tv:binge-ready")
	prog := findCard(cards, "tv:in-progress")
	if complete == nil || binge == nil || prog == nil {
		t.Fatalf("cards = %+v", cards)
	}
	if !reflect.DeepEqual(complete.MemberRatingKeys, []string{"1", "2"}) {
		t.Fatalf("complete members = %v (4>=5-1 tolerance)", complete.MemberRatingKeys)
	}
	if !reflect.DeepEqual(prog.MemberRatingKeys, []string{"3", "4"}) {
		t.Fatalf("in-progress members = %v", prog.MemberRatingKeys)
	}
}

func TestTop100AnnotationAndOwnedCards(t *testing.T) {
	a := movieItem("a", "Owned Gem")
	a.Guids = []string{"tmdb://11"}
	b := movieItem("b", "Other")
	b.Guids = []string{"tmdb://99"}
	scan := &Scan{
		Sections: []plex.Section{{Key: "1", Title: "Movies", Type: "movie"}},
		Items:    []Item{a, b},
	}
	list := []meta.ListEntry{
		{TMDBID: "11", Title: "Owned Gem", PosterPath: "/x.jpg"},
		{TMDBID: "22", Title: "Not Owned", PosterPath: "/y.jpg"},
	}
	annotated := annotateTopEntries(scan, list)
	if len(annotated) != 2 || !annotated[0].Owned || annotated[0].RatingKey != "a" {
		t.Fatalf("annotated[0] = %+v", annotated)
	}
	if annotated[1].Owned || annotated[1].RatingKey != "" {
		t.Fatalf("annotated[1] = %+v, want unowned with empty ratingKey", annotated[1])
	}
	// Only 1 owned -> no card (JS needs >= 2).
	if cards := top100Cards(TopLists{Movies: annotated, TV: []TopEntry{}}); len(cards) != 0 {
		t.Fatalf("cards with 1 owned = %+v", cards)
	}
	// Add a second owned entry -> card with both keys.
	c := movieItem("c", "Owned2")
	c.Guids = []string{"tmdb://22"}
	scan2 := &Scan{Sections: scan.Sections, Items: []Item{a, b, c}}
	annotated2 := annotateTopEntries(scan2, list)
	cards := top100Cards(TopLists{Movies: annotated2, TV: []TopEntry{}})
	own := findCard(cards, "top100:movies")
	if own == nil {
		t.Fatalf("owned card missing: %+v", cards)
	}
	got := append([]string{}, own.MemberRatingKeys...)
	if !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Fatalf("owned card members = %v", got)
	}
	// Nil clients in GenerateAll -> empty (non-nil) top lists, no error.
	cards3, tl3, err3 := GenerateAll(context.Background(), scan, GenContext{Store: NewStore(t.TempDir())})
	if err3 != nil || tl3.Movies == nil || tl3.TV == nil {
		t.Fatalf("GenerateAll nil clients = %v, %+v", err3, tl3)
	}
	_ = cards3
}

func TestGenDubSubFromCachedVerdicts(t *testing.T) {
	store := NewStore(t.TempDir())
	store.Set("dub", "show/1", json.RawMessage(`{"ratingKey":"1","verdict":"subbed","perSeason":[]}`))
	store.Set("dub", "show/2", json.RawMessage(`{"ratingKey":"2","verdict":"dubbed","perSeason":[]}`))
	mk := func(rk string) Item {
		return Item{LibraryItem: plex.LibraryItem{RatingKey: rk, Title: "A" + rk, Type: "show", SectionKey: "9"}}
	}
	scan := &Scan{Items: []Item{mk("1"), mk("2"), mk("3")}}
	gc := GenContext{Store: store, Settings: Settings{AnimeSections: []string{"9"}}}
	cards := genDubSub(scan, gc)
	if findCard(cards, "dubsub:subbed") == nil || findCard(cards, "dubsub:dubbed") == nil {
		t.Fatalf("dubsub cards = %+v", cards)
	}
	if c := findCard(cards, "dubsub:subbed"); len(c.MemberRatingKeys) != 1 || c.MemberRatingKeys[0] != "1" {
		t.Fatalf("subbed card = %+v (item 3 has no verdict and must be skipped)", c)
	}
}

func TestIsValidCardContract(t *testing.T) {
	if !IsValidCard(Card{ID: "x", Kind: "genre", Title: "T", MemberRatingKeys: []string{"1"}, Confidence: "deterministic"}) {
		t.Fatal("well-formed card should be valid")
	}
	if IsValidCard(Card{ID: "x", Kind: "genre", Title: "T", MemberRatingKeys: []string{}, Confidence: "bogus"}) {
		t.Fatal("bogus confidence should be invalid")
	}
	if IsValidCard(Card{ID: "x"}) {
		t.Fatal("bare card should be invalid")
	}
	if IsValidCard(Card{}) {
		t.Fatal("zero card should be invalid")
	}
}

// ---------------------------------------------------------------------------
// GenerateAll: failure isolation + dismissal filtering (ported regressions)
// ---------------------------------------------------------------------------

func TestGenerateAllFailureIsolation(t *testing.T) {
	scan := &Scan{Sections: []plex.Section{}, Items: []Item{}}
	cards, _, err := GenerateAll(context.Background(), scan, GenContext{
		Store: NewStore(t.TempDir()),
		// All clients nil: trakt/top100/franchise paths must degrade,
		// never kill the run.
	})
	if err != nil || cards == nil {
		t.Fatalf("GenerateAll = %v cards, %v", len(cards), err)
	}
	// runGen isolates a panicking generator.
	got := runGen(func() []Card { panic("boom") })
	if len(got) != 0 {
		t.Fatalf("runGen panic = %+v", got)
	}
}

func TestGenerateAllFiltersDismissed(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Dismiss("genre:1:Action"); err != nil {
		t.Fatal(err)
	}
	mk := func(rk, title string) Item {
		it := movieItem(rk, title)
		it.TMDB = &meta.Details{Genres: []meta.Genre{{ID: 1, Name: "Action"}}}
		return it
	}
	scan := &Scan{
		Sections: []plex.Section{{Key: "1", Title: "Movies", Type: "movie"}},
		Items:    []Item{mk("1", "A"), mk("2", "B")},
	}
	cards, _, err := GenerateAll(context.Background(), scan, GenContext{
		Store:    store,
		Settings: Settings{GenreThreshold: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if findCard(cards, "genre:1:Action") != nil {
		t.Fatalf("dismissed card leaked: %+v", cards)
	}
	// Undismiss -> the card returns.
	if err := store.Undismiss("genre:1:Action"); err != nil {
		t.Fatal(err)
	}
	cards, _, _ = GenerateAll(context.Background(), scan, GenContext{
		Store:    store,
		Settings: Settings{GenreThreshold: 2},
	})
	if findCard(cards, "genre:1:Action") == nil {
		t.Fatalf("card should return after undismiss: %+v", cards)
	}
}

func TestCardJSONFieldNames(t *testing.T) {
	// The reused web frontend consumes cards verbatim — field names
	// must match the JS shape exactly.
	c := makeCard("genre", "1:Action", cardOpts{title: "Action (Movies)", subtitle: "2 titles", members: []string{"a", "a", "b", ""}, reason: "r"})
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{`"id":"genre:1:Action"`, `"kind":"genre"`, `"memberRatingKeys":["a","b"]`, `"confidence":"deterministic"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("card JSON %s missing %s", s, want)
		}
	}
	scan := &Scan{MachineID: "m", Sections: []plex.Section{}, Items: []Item{}}
	raw, _ = json.Marshal(scan)
	for _, want := range []string{`"machineId":"m"`, `"scannedAt":0`, `"tmdbKeyPresent":false`, `"omdbKeyPresent":false`, `"itemCount":0`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("scan JSON %s missing %s", raw, want)
		}
	}
}

// Regression (the owner, 2026-10-20): an Electron-era scan cache carries
// scannedAt as an ISO-8601 string (scan.js used toISOString). Loading it
// must NOT fail with "cannot unmarshal string into Go struct field
// Scan.scannedAt of type int64".
func TestScanUnmarshalElectronISO(t *testing.T) {
	raw := []byte(`{"machineId":"m1","scannedAt":"2026-10-19T18:30:00.000Z","sections":[],"items":[],"itemCount":0}`)
	var sc Scan
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatalf("ISO scannedAt must load: %v", err)
	}
	want := time.Date(2026, 10, 19, 18, 30, 0, 0, time.UTC).UnixMilli()
	if int64(sc.ScannedAt) != want {
		t.Fatalf("scannedAt = %d, want %d", int64(sc.ScannedAt), want)
	}
	// Numeric form still loads, and marshaling always emits a number.
	var sc2 Scan
	if err := json.Unmarshal([]byte(`{"machineId":"m1","scannedAt":123}`), &sc2); err != nil || int64(sc2.ScannedAt) != 123 {
		t.Fatalf("numeric scannedAt: err=%v val=%d", err, int64(sc2.ScannedAt))
	}
	out, err := json.Marshal(Scan{MachineID: "m1", ScannedAt: epochMs(123)})
	if err != nil || !strings.Contains(string(out), `"scannedAt":123`) {
		t.Fatalf("marshal must emit a number: %s err=%v", out, err)
	}
}

// Regression, store level (same report): an Electron-era scan cache FILE
// — wrapper {scannedAt: <ms number>, data: {...}} whose inner scan carries
// scannedAt as an ISO string — must load through Store.GetScan unchanged.
func TestGetScanLoadsElectronCacheFile(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	file := st.cachePath("scan", "machine-xyz")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	electron := `{"scannedAt":1760000000000,"data":{"machineId":"machine-xyz","scannedAt":"2026-10-19T18:30:00.000Z","sections":[],"items":[],"itemCount":0,"tmdbKeyPresent":true,"omdbKeyPresent":false}}`
	if err := os.WriteFile(file, []byte(electron), 0o644); err != nil {
		t.Fatal(err)
	}
	scan, err := st.GetScan("machine-xyz")
	if err != nil {
		t.Fatalf("GetScan on Electron cache file: %v", err)
	}
	if scan == nil || scan.MachineID != "machine-xyz" || !scan.TMDBKeyPresent {
		t.Fatalf("scan not loaded correctly: %+v", scan)
	}
}
