package advisor

// suggest.go — port of src/advisor/suggest.js: the suggestion generators.
//
// Input: a Scan plus GenContext. Output: cards + TopLists. Every card
// is { id, kind, title, subtitle, memberRatingKeys, reason, confidence }
// with a STABLE id (kind + key) — dismissals key off it. Confidence is a
// contract: 'deterministic' = Plex reads + TMDb fields + rules;
// 'heuristic' = anime detection, title-fallback matches, AniList.
//
// Generators are independent and failure-isolated (GenerateAll recovers
// per generator — one broken generator never kills the run, as in JS).
//
// Go adaptations (flagged to the parent):
//   - Thresholds: a zero Settings field falls back to the JS ?? default
//     (genre 8, decade 8, director 4, studio 4, actor 3, hiddenGem 7.5).
//     A typed Go struct cannot distinguish "unset" from an explicit 0
//     the way JS ?? distinguishes undefined; Store.GetSettings always
//     returns defaults-merged settings, so this only affects callers
//     that hand-build a zero Settings.
//   - byTitle: JS localeCompare is approximated with a case-insensitive
//     byte compare (identical for ASCII titles).
//
// Sibling contracts (verified against internal/meta as landed):
// meta.Details / Genre{ID int64} / CollectionRef as in meta/tmdb.go;
// CollectionParts takes an int64 id; TopRated* return []meta.ListEntry;
// TraktEntry fields are TMDBID/IMDBID strings; AniList RelationsFor
// returns *meta.Relations{Self RelatedTitle, Related []Relation}.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"plexcollectionmanager/internal/meta"
	"plexcollectionmanager/internal/plex"
)

// Card is one suggestion card (suggest.js card shape, JSON verbatim).
type Card struct {
	ID               string   `json:"id"`
	Kind             string   `json:"kind"`
	Title            string   `json:"title"`
	Subtitle         string   `json:"subtitle"`
	MemberRatingKeys []string `json:"memberRatingKeys"`
	Reason           string   `json:"reason"`
	Confidence       string   `json:"confidence"`
	// Rules is the card's suggestion expressed as Builder smart-rule
	// rows ({field, op, value} — the exact vocabulary of the
	// Builder's RULE_FIELDS), present only when the suggestion IS a
	// library filter (genre, decade, content rating, recently added,
	// highly rated, short films, unwatched). It powers the Builder's
	// "create as SMART collection" choice (v1.14): the same
	// suggestion, kept in sync by Plex instead of frozen as a member
	// list. Membership-shaped suggestions (franchises, people,
	// studios-as-lists, hidden gems, top lists) carry none — a smart
	// collection cannot express them, and the field stays absent
	// rather than approximated.
	Rules []CardRule `json:"rules,omitempty"`
	// AI provenance (v1.21, kind "ai" only): the model that proposed
	// the card, how many member keys it returned (AISuggested) and
	// how many of them verified against the real library (AIVerified;
	// MemberRatingKeys holds exactly the verified ones). Absent on
	// deterministic cards.
	AIModel     string `json:"aiModel,omitempty"`
	AISuggested int    `json:"aiSuggested,omitempty"`
	AIVerified  int    `json:"aiVerified,omitempty"`
}

// CardRule is one Builder smart-rule row on a suggestion card.
type CardRule struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// TopEntry is one genTop100 annotated entry: the TMDb list entry plus
// owned + ratingKey (ratingKey "" and owned false when not owned).
type TopEntry struct {
	TMDBID      string   `json:"tmdbId"`
	Title       string   `json:"title"`
	VoteAverage *float64 `json:"voteAverage"`
	Popularity  *float64 `json:"popularity"`
	PosterPath  string   `json:"posterPath"`
	ReleaseDate string   `json:"releaseDate"`
	MediaType   string   `json:"mediaType"`
	Owned       bool     `json:"owned"`
	RatingKey   string   `json:"ratingKey"`
}

// TopLists is genTop100's display payload: { movies, tv }.
type TopLists struct {
	Movies []TopEntry `json:"movies"`
	TV     []TopEntry `json:"tv"`
}

// GenContext is the generator context bag (JS ctx).
type GenContext struct {
	Plex       *plex.Client
	Server     *plex.Server
	Store      *Store
	Settings   Settings
	TMDB       *meta.TMDbClient
	Trakt      *meta.TraktClient
	TraktToken string
	AniList    *meta.AniListClient
}

// IsValidCard validates the card contract (suggest.js isValidCard): id,
// kind and title present as strings, memberRatingKeys an array, and
// confidence in the contract set. In Go an absent string is "", which
// JS typeof would also accept as a string; the discriminating checks
// are the member slice being non-nil and the confidence value.
func IsValidCard(c Card) bool {
	return c.MemberRatingKeys != nil &&
		(c.Confidence == "deterministic" || c.Confidence == "heuristic")
}

// -- helpers ------------------------------------------------------------------

type cardOpts struct {
	title      string
	subtitle   string
	members    []string
	reason     string
	confidence string
	rules      []CardRule
	sortKey    float64 // franchise-only internal sort (JS _sort), stripped after
}

func makeCard(kind, key string, o cardOpts) Card {
	conf := o.confidence
	if conf == "" {
		conf = "deterministic"
	}
	seen := map[string]bool{}
	members := []string{}
	for _, m := range o.members {
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		members = append(members, m)
	}
	return Card{
		ID:               kind + ":" + key,
		Kind:             kind,
		Title:            o.title,
		Subtitle:         o.subtitle,
		MemberRatingKeys: members,
		Reason:           o.reason,
		Confidence:       conf,
		Rules:            o.rules,
	}
}

func byTitle(a, b Card) bool {
	la, lb := strings.ToLower(a.Title), strings.ToLower(b.Title)
	if la != lb {
		return la < lb
	}
	return a.Title < b.Title
}

func sortByTitle(cards []Card) {
	sort.SliceStable(cards, func(i, j int) bool { return byTitle(cards[i], cards[j]) })
}

// jsNum renders a float the way JS `${number}` does: 8 -> "8", 7.5 -> "7.5".
func jsNum(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func ratingKeysOf(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, i := range items {
		out = append(out, i.RatingKey)
	}
	return out
}

func itemsInSection(scan *Scan, sectionKey string) []Item {
	out := []Item{}
	for _, i := range scan.Items {
		if i.SectionKey == sectionKey {
			out = append(out, i)
		}
	}
	return out
}

// ParseTmdbOf returns the tmdb id from an item's guids ("" when absent),
// as suggest.js parseTmdbOf.
func ParseTmdbOf(item Item) string {
	for _, g := range item.Guids {
		if strings.HasPrefix(g, "tmdb://") {
			return strings.TrimPrefix(g, "tmdb://")
		}
	}
	return ""
}

func parseImdbOf(item Item) string {
	for _, g := range item.Guids {
		if strings.HasPrefix(g, "imdb://") {
			return strings.TrimPrefix(g, "imdb://")
		}
	}
	return ""
}

// itemGenres: Plex-native tags FIRST (works with zero keys), TMDb only
// as fallback — the v0.4 source order from suggest.js.
func itemGenres(item Item) []string {
	if len(item.Tags.Genre) > 0 {
		return item.Tags.Genre
	}
	if item.TMDB == nil {
		return nil
	}
	out := make([]string, 0, len(item.TMDB.Genres))
	for _, g := range item.TMDB.Genres {
		out = append(out, g.Name)
	}
	return out
}

func threshold(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

func floatThreshold(v, def float64) float64 {
	if v == 0 {
		return def
	}
	return v
}

// countEntries keeps insertion order (JS Map order) alongside counts.
type countEntries struct {
	order   []string
	counts  map[string]int
	members map[string][]string
}

func newCountEntries() *countEntries {
	return &countEntries{counts: map[string]int{}, members: map[string][]string{}}
}

func (c *countEntries) add(name, ratingKey string) {
	if _, ok := c.counts[name]; !ok {
		c.order = append(c.order, name)
	}
	c.counts[name]++
	c.members[name] = append(c.members[name], ratingKey)
}

// sorted returns names by count desc (stable, JS sort semantics),
// optionally truncated to the top n BEFORE threshold filtering, as JS.
func (c *countEntries) sorted(topN int) []string {
	names := append([]string{}, c.order...)
	sort.SliceStable(names, func(i, j int) bool { return c.counts[names[i]] > c.counts[names[j]] })
	if topN > 0 && len(names) > topN {
		names = names[:topN]
	}
	return names
}

// -- 1. franchise completion ----------------------------------------------------

func genFranchise(ctx context.Context, scan *Scan, gc GenContext) []Card {
	type scored struct {
		card Card
		sort float64
	}
	scoredCards := []scored{}
	type group struct {
		name  string
		items []Item
	}
	groups := map[string]*group{}
	order := []string{}
	for _, item := range scan.Items {
		if item.TMDB == nil || item.TMDB.BelongsToCollection == nil {
			continue
		}
		c := item.TMDB.BelongsToCollection
		if c.ID == "" {
			continue
		}
		g, ok := groups[c.ID]
		if !ok {
			g = &group{name: c.Name}
			groups[c.ID] = g
			order = append(order, c.ID)
		}
		g.items = append(g.items, item)
	}
	for _, cid := range order {
		g := groups[cid]
		items := g.items
		if len(items) < 2 {
			continue
		}
		var parts []meta.CollectionPart
		if gc.TMDB != nil {
			// meta.CollectionParts takes the numeric id; a non-numeric
			// collection id (never produced by meta's normalizer) just
			// yields the owned-only card, like a detail miss in JS.
			if cidNum, perr := strconv.ParseInt(cid, 10, 64); perr == nil {
				if res, err := gc.TMDB.CollectionParts(ctx, cidNum); err == nil && res != nil {
					parts = res.Parts
				}
			}
			// Franchise detail miss — card still works with owned-only info.
		}
		c, pct := franchiseCard(cid, g.name, items, parts)
		scoredCards = append(scoredCards, scored{card: c, sort: pct})
	}

	// Anime orphans: AniList relations (heuristic, capped at 30 queries).
	animeOrphans := []Item{}
	for _, i := range scan.Items {
		noCollection := i.TMDB == nil || i.TMDB.BelongsToCollection == nil
		if i.Type == "show" && noCollection && IsAnime(i, gc.Settings) {
			animeOrphans = append(animeOrphans, i)
		}
	}
	if gc.AniList != nil && len(animeOrphans) > 0 {
		type family struct {
			title   string
			members []Item
		}
		families := map[string]*family{}
		famOrder := []string{}
		norm := func(s string) string {
			return nonAlnumRe.ReplaceAllString(strings.ToLower(s), "")
		}
		byNormTitle := map[string]Item{}
		for _, i := range scan.Items {
			byNormTitle[norm(i.Title)] = i // JS Map: later entries win
		}
		queries := 0
		limit := animeOrphans
		if len(limit) > 30 {
			limit = limit[:30]
		}
		for _, item := range limit {
			if queries >= 30 {
				break
			}
			queries++
			rel, err := gc.AniList.RelationsFor(ctx, item.Title)
			if err != nil || rel == nil || len(rel.Related) == 0 {
				continue
			}
			familyTitles := []string{rel.Self.Title}
			for _, r := range rel.Related {
				familyTitles = append(familyTitles, r.Title)
			}
			members := []Item{}
			for _, t := range familyTitles {
				if m, ok := byNormTitle[norm(t)]; ok {
					members = append(members, m)
				}
			}
			if len(members) >= 2 {
				key := "anilist:" + rel.Self.ID
				fam, ok := families[key]
				if !ok {
					fam = &family{title: rel.Self.Title + " saga"}
					families[key] = fam
					famOrder = append(famOrder, key)
				}
				for _, m := range members {
					dup := false
					for _, x := range fam.members {
						if x.RatingKey == m.RatingKey {
							dup = true
							break
						}
					}
					if !dup {
						fam.members = append(fam.members, m)
					}
				}
			}
		}
		for _, key := range famOrder {
			fam := families[key]
			if len(fam.members) < 2 {
				continue
			}
			c := makeCard("franchise", key, cardOpts{
				title:      fam.title,
				subtitle:   fmt.Sprintf("%d related anime in your library", len(fam.members)),
				members:    ratingKeysOf(fam.members),
				reason:     "Grouped by AniList relations (sequels / side stories). Title-matched — check the members.",
				confidence: "heuristic",
			})
			scoredCards = append(scoredCards, scored{card: c, sort: 50})
		}
	}

	sort.SliceStable(scoredCards, func(i, j int) bool {
		if scoredCards[i].sort != scoredCards[j].sort {
			return scoredCards[i].sort > scoredCards[j].sort
		}
		return byTitle(scoredCards[i].card, scoredCards[j].card)
	})
	cards := make([]Card, 0, len(scoredCards))
	for _, s := range scoredCards {
		cards = append(cards, s.card)
	}
	return cards
}

// franchiseCard builds one franchise card from the owned items and the
// TMDb collection parts (empty parts = owned-only card, as in JS when
// the collection lookup fails). It also returns the completion percent
// genFranchise sorts by.
func franchiseCard(cid, name string, items []Item, parts []meta.CollectionPart) (Card, float64) {
	ownedTmdb := map[string]bool{}
	for _, i := range items {
		ownedTmdb[ParseTmdbOf(i)] = true
	}
	missing := []meta.CollectionPart{}
	for _, p := range parts {
		if !ownedTmdb[p.TMDBID] {
			missing = append(missing, p)
		}
	}
	total := len(parts)
	if total == 0 {
		total = len(items)
	}
	pct := math.Round(float64(len(items)) / float64(total) * 100)
	subtitle := fmt.Sprintf("You own %d of %d", len(items), total)
	if len(missing) > 0 {
		names := []string{}
		for _, m := range missing[:min(3, len(missing))] {
			names = append(names, m.Title)
		}
		subtitle += " — missing: " + strings.Join(names, ", ")
		if len(missing) > 3 {
			subtitle += fmt.Sprintf(" +%d more", len(missing)-3)
		}
	} else {
		subtitle += " — complete!"
	}
	return makeCard("franchise", cid, cardOpts{
		title:      name,
		subtitle:   subtitle,
		members:    ratingKeysOf(items),
		reason:     fmt.Sprintf("Grouped by TMDb collection \u201c%s\u201d.", name),
		confidence: "deterministic",
	}), pct
}

var nonAlnumRe = regexp.MustCompile(`[^a-z0-9]+`)

// -- 2/3. genre + decade clusters -------------------------------------------------

func genClusters(scan *Scan, gc GenContext) []Card {
	cards := []Card{}
	gTh := threshold(gc.Settings.GenreThreshold, 8)
	dTh := threshold(gc.Settings.DecadeThreshold, 8)
	for _, section := range scan.Sections {
		inSection := itemsInSection(scan, section.Key)
		// genres
		genreCounts := newCountEntries()
		for _, item := range inSection {
			for _, name := range itemGenres(item) {
				genreCounts.add(name, item.RatingKey)
			}
		}
		for _, name := range genreCounts.sorted(10) {
			count := genreCounts.counts[name]
			if count < gTh {
				continue
			}
			cards = append(cards, makeCard("genre", section.Key+":"+name, cardOpts{
				title:    fmt.Sprintf("%s (%s)", name, section.Title),
				subtitle: fmt.Sprintf("%d titles", count),
				members:  genreCounts.members[name],
				reason:   fmt.Sprintf("You have %d %s titles in %s (from your Plex tags — no key needed).", count, strings.ToLower(name), section.Title),
				rules:    []CardRule{{Field: "genre", Op: "is", Value: name}},
			}))
		}
		// decades
		decadeCounts := &struct {
			order   []int
			counts  map[int]int
			members map[int][]string
		}{counts: map[int]int{}, members: map[int][]string{}}
		for _, item := range inSection {
			if item.Year == nil {
				continue
			}
			decade := (*item.Year / 10) * 10
			if _, ok := decadeCounts.counts[decade]; !ok {
				decadeCounts.order = append(decadeCounts.order, decade)
			}
			decadeCounts.counts[decade]++
			decadeCounts.members[decade] = append(decadeCounts.members[decade], item.RatingKey)
		}
		decades := append([]int{}, decadeCounts.order...)
		sort.SliceStable(decades, func(i, j int) bool { return decadeCounts.counts[decades[i]] > decadeCounts.counts[decades[j]] })
		for _, decade := range decades {
			count := decadeCounts.counts[decade]
			if count < dTh {
				continue
			}
			cards = append(cards, makeCard("decade", fmt.Sprintf("%s:%d", section.Key, decade), cardOpts{
				title:    fmt.Sprintf("%ds (%s)", decade, section.Title),
				subtitle: fmt.Sprintf("%d titles", count),
				members:  decadeCounts.members[decade],
				reason:   fmt.Sprintf("%d titles from the %ds in %s.", count, decade, section.Title),
				rules:    []CardRule{{Field: "decade", Op: "is", Value: fmt.Sprintf("%d", decade)}},
			}))
		}
	}
	return cards
}

// -- 4. directors / actors (Plex-native tags) --------------------------------------

func genPeople(scan *Scan, gc GenContext) []Card {
	cards := []Card{}
	dTh := threshold(gc.Settings.DirectorThreshold, 4)
	aTh := threshold(gc.Settings.ActorThreshold, 3)
	type role struct {
		kind      string
		threshold int
		tagsOf    func(Item) []string
	}
	roles := []role{
		{"director", dTh, func(i Item) []string { return i.Tags.Director }},
		{"actor", aTh, func(i Item) []string { return i.Tags.Actor }},
	}
	for _, section := range scan.Sections {
		inSection := itemsInSection(scan, section.Key)
		for _, r := range roles {
			counts := newCountEntries()
			for _, item := range inSection {
				for _, name := range r.tagsOf(item) {
					counts.add(name, item.RatingKey)
				}
			}
			for _, name := range counts.sorted(10) {
				n := counts.counts[name]
				if n < r.threshold {
					continue
				}
				cards = append(cards, makeCard(r.kind, section.Key+":"+name, cardOpts{
					title:    fmt.Sprintf("%s (%s)", name, section.Title),
					subtitle: fmt.Sprintf("%d titles", n),
					members:  counts.members[name],
					reason:   fmt.Sprintf("%d titles with %s %s in %s — from your Plex tags, no key needed.", n, r.kind, name, section.Title),
				}))
			}
		}
	}
	return cards
}

// -- 4b. studios (Plex-native) -------------------------------------------------------

func genStudios(scan *Scan, gc GenContext) []Card {
	cards := []Card{}
	sTh := threshold(gc.Settings.StudioThreshold, 4)
	for _, section := range scan.Sections {
		inSection := itemsInSection(scan, section.Key)
		counts := newCountEntries()
		for _, item := range inSection {
			for _, name := range item.Tags.Studio {
				counts.add(name, item.RatingKey)
			}
		}
		for _, name := range counts.sorted(10) {
			n := counts.counts[name]
			if n < sTh {
				continue
			}
			cards = append(cards, makeCard("studio", section.Key+":"+name, cardOpts{
				title:    fmt.Sprintf("%s (%s)", name, section.Title),
				subtitle: fmt.Sprintf("%d titles", n),
				members:  counts.members[name],
				reason:   fmt.Sprintf("%d %s titles in %s — from your Plex studio tags.", n, name, section.Title),
			}))
		}
	}
	return cards
}

// -- 4c. content ratings (Plex-native) --------------------------------------------------

func genContentRatings(scan *Scan) []Card {
	cards := []Card{}
	for _, section := range scan.Sections {
		type grp struct {
			cr      string
			members []Item
		}
		groups := map[string]*grp{}
		order := []string{}
		for _, item := range scan.Items {
			if item.SectionKey != section.Key || item.ContentRating == "" {
				continue
			}
			g, ok := groups[item.ContentRating]
			if !ok {
				g = &grp{cr: item.ContentRating}
				groups[item.ContentRating] = g
				order = append(order, item.ContentRating)
			}
			g.members = append(g.members, item)
		}
		entries := make([]*grp, 0, len(order))
		for _, k := range order {
			entries = append(entries, groups[k])
		}
		sort.SliceStable(entries, func(i, j int) bool { return len(entries[i].members) > len(entries[j].members) })
		for _, g := range entries {
			if len(g.members) < 3 {
				continue
			}
			cards = append(cards, makeCard("contentrating", section.Key+":"+g.cr, cardOpts{
				title:    fmt.Sprintf("%s (%s)", g.cr, section.Title),
				subtitle: fmt.Sprintf("%d titles", len(g.members)),
				members:  ratingKeysOf(g.members),
				reason:   fmt.Sprintf("%d %s-rated titles in %s — from your Plex metadata.", len(g.members), g.cr, section.Title),
				rules:    []CardRule{{Field: "contentRating", Op: "is", Value: g.cr}},
			}))
		}
	}
	return cards
}

// -- 4d. unwatched backlog (Plex-native) --------------------------------------------------

func genUnwatched(scan *Scan) []Card {
	cards := []Card{}
	for _, section := range scan.Sections {
		unwatched := []Item{}
		for _, i := range scan.Items {
			if i.SectionKey == section.Key && i.ViewCount == 0 {
				unwatched = append(unwatched, i)
			}
		}
		if len(unwatched) < 5 {
			continue
		}
		cards = append(cards, makeCard("unwatched", section.Key, cardOpts{
			title:    fmt.Sprintf("Unwatched backlog (%s)", section.Title),
			subtitle: fmt.Sprintf("%d titles you haven't started", len(unwatched)),
			members:  ratingKeysOf(unwatched),
			reason:   fmt.Sprintf("%d titles in %s with zero plays — your backlog, from Plex play history.", len(unwatched), section.Title),
			rules:    []CardRule{{Field: "unwatched", Op: "yes", Value: ""}},
		}))
	}
	return cards
}

// -- recently added (Plex-native: addedAt, last 60 days) --------------------------------------

func genRecent(scan *Scan) []Card {
	return genRecentAt(scan, time.Now())
}

func genRecentAt(scan *Scan, now time.Time) []Card {
	cards := []Card{}
	const days = 60
	const minCount = 5
	cutoff := now.Unix() - days*86400
	for _, section := range scan.Sections {
		fresh := []Item{}
		for _, i := range scan.Items {
			if i.SectionKey == section.Key && i.AddedAt >= cutoff {
				fresh = append(fresh, i)
			}
		}
		if len(fresh) < minCount {
			continue
		}
		sort.SliceStable(fresh, func(a, b int) bool { return fresh[a].AddedAt > fresh[b].AddedAt })
		cards = append(cards, makeCard("recent", section.Key, cardOpts{
			title:    fmt.Sprintf("Recently added (%s)", section.Title),
			subtitle: fmt.Sprintf("%d titles added in the last %d days", len(fresh), days),
			members:  ratingKeysOf(fresh),
			reason:   fmt.Sprintf("Everything added to %s in the last %d days, newest first — from Plex itself.", section.Title, days),
			rules:    []CardRule{{Field: "addedAt", Op: "within", Value: fmt.Sprintf("%d", days)}},
		}))
	}
	return cards
}

// -- highly rated by Plex metadata (plexRating >= 8.0, 8+ titles) ------------------------------

func genHighlyRated(scan *Scan) []Card {
	cards := []Card{}
	const minRating = 8.0
	const minCount = 8
	for _, section := range scan.Sections {
		rated := []Item{}
		for _, i := range scan.Items {
			if i.SectionKey == section.Key && i.PlexRating != nil && *i.PlexRating >= minRating {
				rated = append(rated, i)
			}
		}
		if len(rated) < minCount {
			continue
		}
		sort.SliceStable(rated, func(a, b int) bool { return *rated[a].PlexRating > *rated[b].PlexRating })
		cards = append(cards, makeCard("highly-rated", section.Key, cardOpts{
			title:    fmt.Sprintf("Critics' picks (%s)", section.Title),
			subtitle: fmt.Sprintf("%d titles rated %s+", len(rated), jsNum(minRating)),
			members:  ratingKeysOf(rated),
			reason:   fmt.Sprintf("The highest-rated titles in %s by Plex's own critic score — no TMDb needed.", section.Title),
			rules:    []CardRule{{Field: "criticRating", Op: "min", Value: jsNum(minRating)}},
		}))
	}
	return cards
}

// -- weeknight movies (durationMin <= 100, movies only, 8+ titles) --------------------------------

func genShortFilms(scan *Scan) []Card {
	cards := []Card{}
	const maxMin = 100
	const minCount = 8
	for _, section := range scan.Sections {
		if section.Type != "movie" {
			continue
		}
		shorts := []Item{}
		for _, i := range scan.Items {
			if i.SectionKey == section.Key && i.DurationMin != nil && *i.DurationMin <= maxMin && *i.DurationMin > 0 {
				shorts = append(shorts, i)
			}
		}
		if len(shorts) < minCount {
			continue
		}
		sort.SliceStable(shorts, func(a, b int) bool { return *shorts[a].DurationMin < *shorts[b].DurationMin })
		cards = append(cards, makeCard("short-films", section.Key, cardOpts{
			title:    fmt.Sprintf("Under %d minutes (%s)", maxMin, section.Title),
			subtitle: fmt.Sprintf("%d weeknight-friendly movies", len(shorts)),
			members:  ratingKeysOf(shorts),
			reason:   fmt.Sprintf("Movies in %s that fit a weeknight — from Plex runtimes.", section.Title),
			rules:    []CardRule{{Field: "durationMax", Op: "atMost", Value: fmt.Sprintf("%d", maxMin)}},
		}))
	}
	return cards
}

// -- 5. hidden gems ------------------------------------------------------------------------------

func genHiddenGems(scan *Scan, gc GenContext) []Card {
	cards := []Card{}
	minRating := floatThreshold(gc.Settings.HiddenGemRating, 7.5)
	for _, section := range scan.Sections {
		inSection := []Item{}
		for _, i := range scan.Items {
			if i.SectionKey == section.Key && i.TMDB != nil && i.TMDB.Popularity != nil {
				inSection = append(inSection, i)
			}
		}
		if len(inSection) == 0 {
			continue
		}
		pops := make([]float64, 0, len(inSection))
		for _, i := range inSection {
			pops = append(pops, *i.TMDB.Popularity)
		}
		sort.Float64s(pops)
		median := pops[len(pops)/2]
		gems := []Item{}
		for _, i := range inSection {
			va := 0.0
			if i.TMDB.VoteAverage != nil {
				va = *i.TMDB.VoteAverage
			}
			if va >= minRating && i.ViewCount == 0 && *i.TMDB.Popularity < median {
				gems = append(gems, i)
			}
		}
		if len(gems) < 2 {
			continue
		}
		sort.SliceStable(gems, func(a, b int) bool {
			return *gems[a].TMDB.VoteAverage > *gems[b].TMDB.VoteAverage
		})
		cards = append(cards, makeCard("hiddengems", section.Key, cardOpts{
			title:    fmt.Sprintf("Hidden gems (%s)", section.Title),
			subtitle: fmt.Sprintf("%d highly-rated, unwatched, overlooked titles", len(gems)),
			members:  ratingKeysOf(gems),
			reason:   fmt.Sprintf("Rated ≥ %s on TMDb, unwatched, and less popular than your library's median — the good stuff you missed.", jsNum(minRating)),
		}))
	}
	return cards
}

// -- 6. Trakt-powered ------------------------------------------------------------------------------

func genTrakt(ctx context.Context, scan *Scan, gc GenContext) []Card {
	cards := []Card{}
	if gc.Trakt == nil {
		return cards
	}
	ownedTmdb := map[string]bool{}
	ownedImdb := map[string]bool{}
	byTmdb := map[string]Item{}
	for _, i := range scan.Items {
		if t := ParseTmdbOf(i); t != "" {
			ownedTmdb[t] = true
			if _, ok := byTmdb[t]; !ok {
				byTmdb[t] = i
			}
		}
		if im := parseImdbOf(i); im != "" {
			ownedImdb[im] = true
		}
	}
	genreCounts := newCountEntries()
	for _, i := range scan.Items {
		for _, name := range itemGenres(i) {
			genreCounts.add(name, i.RatingKey)
		}
	}
	topGenres := genreCounts.sorted(3)

	if trending, err := gc.Trakt.TrendingMovies(ctx); err == nil {
		owned := []meta.TraktEntry{}
		for _, t := range trending {
			if t.TMDBID != "" && ownedTmdb[t.TMDBID] {
				owned = append(owned, t)
			}
		}
		if len(owned) >= 2 {
			members := make([]string, 0, len(owned))
			for _, t := range owned {
				members = append(members, byTmdb[t.TMDBID].RatingKey)
			}
			cards = append(cards, makeCard("trakt", "trending-movies-owned", cardOpts{
				title:    "Trending movies you own",
				subtitle: fmt.Sprintf("%d of the Trakt trending 100 are in your library", len(owned)),
				members:  members,
				reason:   "Trakt trending intersected with your library. Your top genres: " + strings.Join(topGenres, ", ") + ".",
			}))
		}
	}
	// Trakt down — no cards, no crash (JS catch).

	if gc.TraktToken != "" {
		// JS uses Promise.all: either list failing skips the whole card.
		movies, errMovies := gc.Trakt.Watchlist(ctx, gc.TraktToken, "movies")
		shows, errShows := gc.Trakt.Watchlist(ctx, gc.TraktToken, "shows")
		if errMovies == nil && errShows == nil {
			all := append(append([]meta.TraktEntry{}, movies...), shows...)
			owned := []meta.TraktEntry{}
			for _, w := range all {
				if (w.TMDBID != "" && ownedTmdb[w.TMDBID]) || (w.IMDBID != "" && ownedImdb[w.IMDBID]) {
					owned = append(owned, w)
				}
			}
			if len(owned) >= 1 {
				keys := []string{}
				for _, w := range owned {
					var item *Item
					if w.TMDBID != "" {
						if it, ok := byTmdb[w.TMDBID]; ok {
							item = &it
						}
					}
					if item == nil && w.IMDBID != "" {
						needle := "imdb://" + w.IMDBID
						for idx := range scan.Items {
							for _, g := range scan.Items[idx].Guids {
								if g == needle {
									item = &scan.Items[idx]
									break
								}
							}
							if item != nil {
								break
							}
						}
					}
					if item != nil {
						keys = append(keys, item.RatingKey)
					}
				}
				if len(keys) > 0 {
					cards = append(cards, makeCard("trakt", "watchlist-owned", cardOpts{
						title:    "Your Trakt watchlist — on Plex",
						subtitle: fmt.Sprintf("%d watchlist titles already in your library", len(keys)),
						members:  keys,
						reason:   "Your Trakt watchlist intersected with your Plex library.",
					}))
				}
			}
		}
	}
	return cards
}

// -- 7. Top 100s (display + ownership — never acquisition) --------------------------------------------

func genTop100(ctx context.Context, scan *Scan, gc GenContext) ([]Card, TopLists) {
	cards := []Card{}
	topLists := TopLists{Movies: []TopEntry{}, TV: []TopEntry{}}
	if gc.TMDB == nil {
		return cards, topLists
	}
	// JS Promise.all: a failure of either list leaves topLists empty.
	movies, errM := gc.TMDB.TopRatedMovies(ctx)
	tv, errT := gc.TMDB.TopRatedTV(ctx)
	if errM != nil || errT != nil {
		return cards, topLists
	}
	topLists.Movies = annotateTopEntries(scan, movies)
	topLists.TV = annotateTopEntries(scan, tv)
	return top100Cards(topLists), topLists
}

// annotateTopEntries spreads each TMDb list entry and flags ownership
// against the scan (owned + the owned item's ratingKey), as genTop100.
func annotateTopEntries(scan *Scan, list []meta.ListEntry) []TopEntry {
	ownedTmdb := map[string]bool{}
	byTmdb := map[string]Item{}
	for _, i := range scan.Items {
		if t := ParseTmdbOf(i); t != "" {
			ownedTmdb[t] = true
			if _, ok := byTmdb[t]; !ok {
				byTmdb[t] = i
			}
		}
	}
	out := make([]TopEntry, 0, len(list))
	for _, e := range list {
		entry := TopEntry{
			TMDBID:      e.TMDBID,
			Title:       e.Title,
			VoteAverage: e.VoteAverage,
			Popularity:  e.Popularity,
			PosterPath:  e.PosterPath,
			ReleaseDate: e.ReleaseDate,
			MediaType:   e.MediaType,
			Owned:       ownedTmdb[e.TMDBID],
			RatingKey:   "",
		}
		if entry.Owned {
			entry.RatingKey = byTmdb[e.TMDBID].RatingKey
		}
		out = append(out, entry)
	}
	return out
}

// top100Cards builds the owned-only Top-100 cards (>= 2 owned each).
func top100Cards(topLists TopLists) []Card {
	cards := []Card{}
	ownedMovies := []TopEntry{}
	for _, m := range topLists.Movies {
		if m.Owned {
			ownedMovies = append(ownedMovies, m)
		}
	}
	ownedTv := []TopEntry{}
	for _, m := range topLists.TV {
		if m.Owned {
			ownedTv = append(ownedTv, m)
		}
	}
	if len(ownedMovies) >= 2 {
		members := make([]string, 0, len(ownedMovies))
		for _, m := range ownedMovies {
			members = append(members, m.RatingKey)
		}
		cards = append(cards, makeCard("top100", "movies", cardOpts{
			title:    "TMDb Top 100 Movies — yours",
			subtitle: fmt.Sprintf("You own %d of the top 100", len(ownedMovies)),
			members:  members,
			reason:   "TMDb top-rated movies intersected with your library. Display + ownership only — the app never downloads media.",
		}))
	}
	if len(ownedTv) >= 2 {
		members := make([]string, 0, len(ownedTv))
		for _, m := range ownedTv {
			members = append(members, m.RatingKey)
		}
		cards = append(cards, makeCard("top100", "tv", cardOpts{
			title:    "TMDb Top 100 Shows — yours",
			subtitle: fmt.Sprintf("You own %d of the top 100", len(ownedTv)),
			members:  members,
			reason:   "TMDb top-rated shows intersected with your library.",
		}))
	}
	return cards
}

// -- 8. seasonal -----------------------------------------------------------------------------------------

var seasonalTable = []struct {
	months   []int
	genreSub string
	label    string
}{
	{[]int{9, 10}, "Horror", "Spooky season"},
	{[]int{11}, "Thriller", "Dark November"},
	{[]int{12}, "Family", "Holiday watch"},
	{[]int{12}, "Comedy", "Holiday watch"},
	{[]int{2}, "Romance", "Valentine's picks"},
	{[]int{6, 7}, "Adventure", "Summer blockbusters"},
	{[]int{6, 7}, "Action", "Summer blockbusters"},
}

func genSeasonal(scan *Scan, now time.Time) []Card {
	month := int(now.Month())
	cards := []Card{}
	for _, entry := range seasonalTable {
		matched := false
		for _, m := range entry.months {
			if m == month {
				matched = true
			}
		}
		if !matched {
			continue
		}
		for _, section := range scan.Sections {
			members := []Item{}
			for _, i := range scan.Items {
				if i.SectionKey != section.Key {
					continue
				}
				for _, g := range itemGenres(i) {
					if strings.Contains(strings.ToLower(g), strings.ToLower(entry.genreSub)) {
						members = append(members, i)
						break
					}
				}
			}
			if len(members) < 3 {
				continue
			}
			key := fmt.Sprintf("%d-%d:%s:%s", now.Year(), month, entry.genreSub, section.Key)
			cards = append(cards, makeCard("seasonal", key, cardOpts{
				title:    fmt.Sprintf("%s (%s)", entry.label, section.Title),
				subtitle: fmt.Sprintf("%d %s titles", len(members), strings.ToLower(entry.genreSub)),
				members:  ratingKeysOf(members),
				reason:   fmt.Sprintf("It's %s — time for %s.", now.Format("January"), strings.ToLower(entry.genreSub)),
			}))
		}
	}
	return cards
}

// -- 9. anime dub/sub groupings ------------------------------------------------------------------------------

func genDubSub(scan *Scan, gc GenContext) []Card {
	groups := map[string][]Item{"dubbed": {}, "subbed": {}, "dual": {}, "unknown": {}}
	for _, item := range scan.Items {
		if !IsAnime(item, gc.Settings) {
			continue
		}
		nsKey := "show/" + item.RatingKey
		if item.Type == "movie" {
			nsKey = "movie/" + item.RatingKey
		}
		verdict := ""
		if gc.Store != nil {
			if raw, hit := gc.Store.Get("dub", nsKey); hit {
				var entry struct {
					Verdict string `json:"verdict"`
				}
				if err := json.Unmarshal(raw, &entry); err == nil {
					verdict = entry.Verdict
				}
			}
		}
		if verdict == "" {
			continue
		}
		if _, ok := groups[verdict]; !ok {
			continue
		}
		groups[verdict] = append(groups[verdict], item)
	}
	labels := map[string]string{
		"dubbed": "Dubbed anime", "subbed": "Subbed-only anime",
		"dual": "Dual-audio anime", "unknown": "Anime — audio unknown",
	}
	cards := []Card{}
	for _, verdict := range []string{"dubbed", "subbed", "dual", "unknown"} {
		members := groups[verdict]
		if len(members) < 1 {
			continue
		}
		reason := "Verdicts from your Plex audio tracks (English present = dubbed, Japanese-only = subbed). Open the Dub/Sub view for per-season detail."
		confidence := "deterministic"
		if verdict == "unknown" {
			reason = "Audio tracks couldn't be identified — scan these shows for a verdict."
			confidence = "heuristic"
		}
		cards = append(cards, makeCard("dubsub", verdict, cardOpts{
			title:      labels[verdict],
			subtitle:   fmt.Sprintf("%d shows/movies", len(members)),
			members:    ratingKeysOf(members),
			reason:     reason,
			confidence: confidence,
		}))
	}
	return cards
}

// -- 10. TV completeness ----------------------------------------------------------------------------------------

func genTvCompleteness(scan *Scan) []Card {
	cards := []Card{}
	complete := []Item{}
	inProgress := []Item{}
	for _, item := range scan.Items {
		if item.Type != "show" || item.TMDB == nil || item.TMDB.NumberOfSeasons == nil {
			continue
		}
		plexSeasons := item.ChildCount
		tmdbSeasons := int(*item.TMDB.NumberOfSeasons)
		if plexSeasons >= tmdbSeasons-1 && tmdbSeasons > 0 {
			complete = append(complete, item)
		} else if plexSeasons > 0 {
			inProgress = append(inProgress, item)
		}
	}
	if len(complete) >= 2 {
		binge := []Item{}
		for _, i := range complete {
			if i.ViewCount == 0 {
				binge = append(binge, i)
			}
		}
		cards = append(cards, makeCard("tv", "complete", cardOpts{
			title:    "Complete series",
			subtitle: fmt.Sprintf("%d shows with all aired seasons", len(complete)),
			members:  ratingKeysOf(complete),
			reason:   "Plex season count matches TMDb's aired season count (±1 for specials).",
		}))
		if len(binge) >= 2 {
			cards = append(cards, makeCard("tv", "binge-ready", cardOpts{
				title:    "Binge-ready",
				subtitle: fmt.Sprintf("%d complete, unwatched series", len(binge)),
				members:  ratingKeysOf(binge),
				reason:   "Complete series you haven't started.",
			}))
		}
	}
	if len(inProgress) >= 2 {
		cards = append(cards, makeCard("tv", "in-progress", cardOpts{
			title:    "Still airing / incomplete",
			subtitle: fmt.Sprintf("%d shows missing seasons", len(inProgress)),
			members:  ratingKeysOf(inProgress),
			reason:   "Your season count is below TMDb's aired count — still airing or missing seasons.",
		}))
	}
	return cards
}

// -- orchestration -----------------------------------------------------------------------------------------------

// runGen failure-isolates one generator: a panic contributes zero
// cards, exactly as the JS try/catch isolates a throwing generator.
func runGen(fn func() []Card) (out []Card) {
	out = []Card{}
	defer func() {
		if recover() != nil {
			out = []Card{}
		}
	}()
	if got := fn(); got != nil {
		out = got
	}
	return out
}

// GenerateAll runs all generators, failure-isolated, filters dismissed
// cards, and returns cards sorted by member count desc then title —
// plus genTop100's TopLists. The error return is part of the required
// Go API; generator failures are isolated (JS behavior), so it is nil.
func GenerateAll(ctx context.Context, scan *Scan, gc GenContext) ([]Card, TopLists, error) {
	dismissed := map[string]bool{}
	if gc.Store != nil {
		if list, err := gc.Store.GetDismissals(); err == nil {
			for _, id := range list {
				dismissed[id] = true
			}
		}
	}
	if scan == nil {
		scan = &Scan{Sections: []plex.Section{}, Items: []Item{}}
	}

	cards := []Card{}
	topLists := TopLists{Movies: []TopEntry{}, TV: []TopEntry{}}

	cards = append(cards, runGen(func() []Card { return genFranchise(ctx, scan, gc) })...)
	cards = append(cards, runGen(func() []Card { return genClusters(scan, gc) })...)
	cards = append(cards, runGen(func() []Card { return genPeople(scan, gc) })...)
	cards = append(cards, runGen(func() []Card { return genStudios(scan, gc) })...)
	cards = append(cards, runGen(func() []Card { return genContentRatings(scan) })...)
	cards = append(cards, runGen(func() []Card { return genUnwatched(scan) })...)
	cards = append(cards, runGen(func() []Card { return genRecent(scan) })...)
	cards = append(cards, runGen(func() []Card { return genHighlyRated(scan) })...)
	cards = append(cards, runGen(func() []Card { return genShortFilms(scan) })...)
	cards = append(cards, runGen(func() []Card { return genHiddenGems(scan, gc) })...)
	cards = append(cards, runGen(func() []Card { return genTrakt(ctx, scan, gc) })...)
	func() {
		defer func() { _ = recover() }()
		c, tl := genTop100(ctx, scan, gc)
		cards = append(cards, c...)
		topLists = tl
	}()
	cards = append(cards, runGen(func() []Card { return genSeasonal(scan, time.Now()) })...)
	cards = append(cards, runGen(func() []Card { return genDubSub(scan, gc) })...)
	cards = append(cards, runGen(func() []Card { return genTvCompleteness(scan) })...)

	filtered := make([]Card, 0, len(cards))
	for _, c := range cards {
		if len(c.MemberRatingKeys) == 0 {
			continue
		}
		if dismissed[c.ID] {
			continue
		}
		filtered = append(filtered, c)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if len(filtered[i].MemberRatingKeys) != len(filtered[j].MemberRatingKeys) {
			return len(filtered[i].MemberRatingKeys) > len(filtered[j].MemberRatingKeys)
		}
		return byTitle(filtered[i], filtered[j])
	})
	return filtered, topLists, nil
}
