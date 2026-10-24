package advisor

// v1.25 bundle tests: the six new deterministic generators fire exactly
// on their qualifying sets, stay silent below threshold, and skip items
// missing a required field. DedupeCovered is pinned separately.

import (
	"context"
	"testing"

	"plexcollectionmanager/internal/plex"
)

func bundleScan() *Scan {
	mk := func(rk, title string, year int, genres []string, rating, audience *float64, views int, dur *int) Item {
		it := movieItem(rk, title)
		y := year
		it.Year = &y
		it.Tags.Genre = genres
		it.PlexRating = rating
		it.AudienceRating = audience
		it.ViewCount = views
		it.DurationMin = dur
		return it
	}
	dur := func(m int) *int { return &m }
	items := []Item{
		// Decade x genre: 4x 1994 Action (fires), 3x 1994 Comedy (silent),
		// 1x Action with no year (never counts).
		mk("d1", "Action A", 1994, []string{"Action"}, nil, nil, 0, dur(120)),
		mk("d2", "Action B", 1994, []string{"Action"}, nil, nil, 0, dur(110)),
		mk("d3", "Action C", 1994, []string{"Action"}, nil, nil, 0, dur(100)),
		mk("d4", "Action D", 1994, []string{"Action"}, nil, nil, 0, dur(130)),
		mk("c1", "Comedy A", 1994, []string{"Comedy"}, nil, nil, 0, dur(95)),
		mk("c2", "Comedy B", 1994, []string{"Comedy"}, nil, nil, 0, dur(90)),
		mk("c3", "Comedy C", 1994, []string{"Comedy"}, nil, nil, 0, dur(85)),
		{LibraryItem: plex.LibraryItem{RatingKey: "ny", Title: "No Year", Type: "movie", SectionKey: "1",
			Tags: plex.Tags{Genre: []string{"Action"}}}}, // no year, no duration
		// Unwatched gems: 3x rated 8+ never played (fires); 1x rated 8+
		// played once (excluded); 1x unrated never played (excluded).
		mk("g1", "Gem A", 2010, []string{"Drama"}, f64(8.5), nil, 0, dur(140)),
		mk("g2", "Gem B", 2011, []string{"Drama"}, f64(9.0), nil, 0, dur(150)),
		mk("g3", "Gem C", 2012, []string{"Drama"}, f64(8.1), nil, 0, dur(135)),
		mk("g4", "Played Gem", 2013, []string{"Drama"}, f64(8.5), nil, 1, dur(145)),
		mk("g5", "Unrated", 2014, []string{"Drama"}, nil, nil, 0, dur(125)),
		// Rewatchables: 3x played 2+ (fires); 1x played once (excluded).
		mk("r1", "Rewatch A", 2000, []string{"Thriller"}, nil, nil, 2, dur(110)),
		mk("r2", "Rewatch B", 2001, []string{"Thriller"}, nil, nil, 3, dur(115)),
		mk("r3", "Rewatch C", 2002, []string{"Thriller"}, nil, nil, 5, dur(105)),
		mk("r4", "Once", 2003, []string{"Thriller"}, nil, nil, 1, dur(100)),
		// Runtime bands: 5x <= 90 (fires quick); 1x 95 (excluded from
		// quick); 3x >= 180 (fires epics); 1x 170 (excluded); 1x no
		// duration (excluded from both).
		mk("q1", "Quick A", 2015, []string{"Family"}, nil, nil, 0, dur(80)),
		mk("q2", "Quick B", 2016, []string{"Family"}, nil, nil, 0, dur(85)),
		mk("q3", "Quick C", 2017, []string{"Family"}, nil, nil, 0, dur(90)),
		mk("q4", "Quick D", 2018, []string{"Family"}, nil, nil, 0, dur(75)),
		mk("q5", "Quick E", 2019, []string{"Family"}, nil, nil, 0, dur(88)),
		mk("q6", "Mid", 2020, []string{"Family"}, nil, nil, 0, dur(95)),
		mk("e1", "Epic A", 1990, []string{"Adventure"}, nil, nil, 0, dur(180)),
		mk("e2", "Epic B", 1991, []string{"Adventure"}, nil, nil, 0, dur(200)),
		mk("e3", "Epic C", 1992, []string{"Adventure"}, nil, nil, 0, dur(240)),
		mk("e4", "Long", 1993, []string{"Adventure"}, nil, nil, 0, dur(170)),
		{LibraryItem: plex.LibraryItem{RatingKey: "nd", Title: "No Duration", Type: "movie", SectionKey: "1"}},
		// Divergence: 3x critics-high (fires critics card); 3x
		// audiences-high (fires audiences card); 1x small gap
		// (excluded); 1x critic-only (excluded).
		mk("v1", "Critics Pick A", 2005, []string{"Sci-Fi"}, f64(9.0), f64(6.0), 1, dur(120)),
		mk("v2", "Critics Pick B", 2006, []string{"Sci-Fi"}, f64(8.5), f64(6.0), 1, dur(125)),
		mk("v3", "Critics Pick C", 2007, []string{"Sci-Fi"}, f64(9.5), f64(7.0), 1, dur(130)),
		mk("v4", "Crowd Pick A", 2008, []string{"Sci-Fi"}, f64(5.0), f64(8.0), 1, dur(115)),
		mk("v5", "Crowd Pick B", 2009, []string{"Sci-Fi"}, f64(6.0), f64(8.5), 1, dur(118)),
		mk("v6", "Crowd Pick C", 2010, []string{"Sci-Fi"}, f64(5.5), f64(9.0), 1, dur(122)),
		mk("v7", "Agreement", 2011, []string{"Sci-Fi"}, f64(8.0), f64(7.0), 1, dur(110)),
		mk("v8", "Critic Only", 2012, []string{"Sci-Fi"}, f64(9.0), nil, 1, dur(112)),
	}
	// Director / actor / studio bundle members (already-shipped
	// generators, pinned here as bundle membership): 4x one director,
	// 3x one actor, 4x one studio.
	dir := func(rk, title string) Item {
		it := movieItem(rk, title)
		it.Tags.Director = []string{"Jane Auteur"}
		return it
	}
	act := func(rk, title string) Item {
		it := movieItem(rk, title)
		it.Tags.Actor = []string{"Sam Star"}
		return it
	}
	stu := func(rk, title string) Item {
		it := movieItem(rk, title)
		it.Tags.Studio = []string{"Indie House"}
		return it
	}
	items = append(items,
		dir("p1", "Auteur A"), dir("p2", "Auteur B"), dir("p3", "Auteur C"), dir("p4", "Auteur D"),
		act("a1", "Star A"), act("a2", "Star B"), act("a3", "Star C"),
		stu("s1", "Studio A"), stu("s2", "Studio B"), stu("s3", "Studio C"), stu("s4", "Studio D"),
	)
	return &Scan{
		Sections: []plex.Section{{Key: "1", Title: "Movies", Type: "movie"}},
		Items:    items,
	}
}

func TestBundleGenerators(t *testing.T) {
	cards, _, err := GenerateAll(context.Background(), bundleScan(), GenContext{Settings: DefaultSettings()})
	if err != nil {
		t.Fatal(err)
	}
	eq := func(id string, want []string) {
		t.Helper()
		c := findCard(cards, id)
		if c == nil {
			t.Fatalf("missing card %s (have %v)", id, idsOf(cards))
		}
		if len(c.MemberRatingKeys) != len(want) {
			t.Fatalf("card %s members = %v, want %v", id, c.MemberRatingKeys, want)
		}
		for i, k := range want {
			if c.MemberRatingKeys[i] != k {
				t.Fatalf("card %s members = %v, want %v", id, c.MemberRatingKeys, want)
			}
		}
	}
	// Decade x genre: 90s Action fires with exactly the 4; 90s Comedy
	// (3 < 4) stays silent; the no-year Action title never counts.
	eq("decade-genre:1:1990:Action", []string{"d1", "d2", "d3", "d4"})
	if c := findCard(cards, "decade-genre:1:1990:Comedy"); c != nil {
		t.Fatalf("below-threshold 90s Comedy card must not fire: %v", c.MemberRatingKeys)
	}
	// Unwatched gems: exactly the 3 rated-8+ never-played titles.
	eq("unwatched-gems:1", []string{"g2", "g1", "g3"}) // rating desc
	// Rewatchables: most-played first.
	eq("rewatchables:1", []string{"r3", "r2", "r1"})
	// Runtime bands: quick sorted shortest-first, epics longest-first.
	// (The 85/90-minute comedy items legitimately join quick-picks.)
	eq("quick-picks:1", []string{"q4", "q1", "c3", "q2", "q5", "c2", "q3"})
	eq("epics:1", []string{"e3", "e2", "e1"})
	// Divergence: one card per direction, gap desc within each
	// (ties keep scan order: critics v2/v3 both gap 2.5).
	eq("divergence:1:critics", []string{"v1", "v2", "v3"})
	eq("divergence:1:audiences", []string{"v6", "v4", "v5"})
	// Already-shipped bundle members still fire.
	if c := findCard(cards, "director:1:Jane Auteur"); c == nil || len(c.MemberRatingKeys) != 4 {
		t.Fatalf("director card missing/wrong: %v", c)
	}
	if c := findCard(cards, "actor:1:Sam Star"); c == nil || len(c.MemberRatingKeys) != 3 {
		t.Fatalf("actor card missing/wrong: %v", c)
	}
	if c := findCard(cards, "studio:1:Indie House"); c == nil || len(c.MemberRatingKeys) != 4 {
		t.Fatalf("studio card missing/wrong: %v", c)
	}
}

func idsOf(cards []Card) []string {
	out := make([]string, 0, len(cards))
	for _, c := range cards {
		out = append(out, c.ID)
	}
	return out
}

func TestDedupeCovered(t *testing.T) {
	mk := func(kind, id string, members ...string) Card {
		return Card{ID: id, Kind: kind, Title: id, MemberRatingKeys: members, Confidence: "deterministic"}
	}
	existing := []ExistingCollection{
		{Title: "Owned set", Keys: []string{"1", "2", "3", "4", "5", "6"}},
	}
	cards := []Card{
		// |{1..5} ∩ {1..6}| / |union| = 5/6 ≈ 0.833 >= 0.8: suppressed.
		mk("rewatchables", "rewatchables:1", "1", "2", "3", "4", "5"),
		// 2/6 ≈ 0.333: survives.
		mk("epics", "epics:1", "1", "2", "9"),
		// Identical set, but a NON-bundle kind: untouched by design.
		mk("genre", "genre:1:Action", "1", "2", "3", "4", "5", "6"),
	}
	got := DedupeCovered(cards, existing)
	if findCard(got, "rewatchables:1") != nil {
		t.Fatal("near-duplicate bundle card must be suppressed")
	}
	if findCard(got, "epics:1") == nil {
		t.Fatal("below-threshold-overlap bundle card must survive")
	}
	if findCard(got, "genre:1:Action") == nil {
		t.Fatal("non-bundle kinds must pass through dedupe untouched")
	}
	if got := DedupeCovered(cards, nil); len(got) != len(cards) {
		t.Fatal("no existing collections must leave cards untouched")
	}
}
