package advisor

// rules_test.go — v1.14: filter-shaped suggestion cards carry their
// Builder smart-rule rows (the "create as smart" choice); membership-
// shaped cards carry none. The rule values must mirror each
// generator's own constants — if a generator's threshold moves, its
// rule must move with it (both read the same constants).

import (
	"reflect"
	"testing"
	"time"

	"plexcollectionmanager/internal/meta"
	"plexcollectionmanager/internal/plex"
)

func TestCardRulesFilterShaped(t *testing.T) {
	// genre + decade from clusters
	a := movieItem("a", "A")
	a.Tags.Genre = []string{"Action"}
	a.Year = iptr(1994)
	b := movieItem("b", "B")
	b.Tags.Genre = []string{"Action"}
	b.Year = iptr(1999)
	scan := &Scan{
		Sections: []plex.Section{{Key: "1", Title: "Movies", Type: "movie"}},
		Items:    []Item{a, b},
	}
	cards := genClusters(scan, GenContext{Settings: Settings{GenreThreshold: 2, DecadeThreshold: 2}})
	genre := findCard(cards, "genre:1:Action")
	if genre == nil || !reflect.DeepEqual(genre.Rules, []CardRule{{Field: "genre", Op: "is", Value: "Action"}}) {
		t.Errorf("genre card rules = %+v", genre)
	}
	decade := findCard(cards, "decade:1:1990")
	if decade == nil || !reflect.DeepEqual(decade.Rules, []CardRule{{Field: "decade", Op: "is", Value: "1990"}}) {
		t.Errorf("decade card rules = %+v", decade)
	}

	// content rating + unwatched
	mk := func(rk string, vc int) Item {
		it := movieItem(rk, "T"+rk)
		it.ViewCount = vc
		it.ContentRating = "PG-13"
		return it
	}
	scan2 := &Scan{
		Sections: []plex.Section{{Key: "1", Title: "Movies", Type: "movie"}},
		Items:    []Item{mk("a", 0), mk("b", 0), mk("c", 0), mk("d", 0), mk("e", 0), mk("f", 1)},
	}
	crs := genContentRatings(scan2)
	cr := findCard(crs, "contentrating:1:PG-13")
	if cr == nil || !reflect.DeepEqual(cr.Rules, []CardRule{{Field: "contentRating", Op: "is", Value: "PG-13"}}) {
		t.Errorf("content rating card rules = %+v", cr)
	}
	uw := genUnwatched(scan2)
	if len(uw) != 1 || !reflect.DeepEqual(uw[0].Rules, []CardRule{{Field: "unwatched", Op: "yes", Value: ""}}) {
		t.Errorf("unwatched card rules = %+v", uw)
	}

	// recent (60 days), highly rated (critic ≥ 8), short films (≤ 100 min)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	scan3 := &Scan{Sections: []plex.Section{{Key: "1", Title: "Movies", Type: "movie"}}}
	for n := 0; n < 8; n++ {
		it := movieItem(string(rune('a'+n)), "T")
		it.AddedAt = now.Unix() - int64(n)*86400
		it.PlexRating = f64(8.5)
		it.DurationMin = iptr(90)
		scan3.Items = append(scan3.Items, it)
	}
	recent := genRecentAt(scan3, now)
	if len(recent) != 1 || !reflect.DeepEqual(recent[0].Rules, []CardRule{{Field: "addedAt", Op: "within", Value: "60"}}) {
		t.Errorf("recent card rules = %+v", recent)
	}
	hr := genHighlyRated(scan3)
	if len(hr) != 1 || !reflect.DeepEqual(hr[0].Rules, []CardRule{{Field: "criticRating", Op: "min", Value: "8"}}) {
		t.Errorf("highly-rated card rules = %+v", hr)
	}
	sf := genShortFilms(scan3)
	if len(sf) != 1 || !reflect.DeepEqual(sf[0].Rules, []CardRule{{Field: "durationMax", Op: "atMost", Value: "100"}}) {
		t.Errorf("short-films card rules = %+v", sf)
	}
}

func TestCardRulesAbsentForMembershipShaped(t *testing.T) {
	mk := func(rk string) Item {
		it := movieItem(rk, "T"+rk)
		it.Tags.Studio = []string{"Pixar"}
		it.Tags.Director = []string{"Nolan"}
		it.TMDB = &meta.Details{Genres: []meta.Genre{{Name: "Drama"}}}
		return it
	}
	scan := &Scan{
		Sections: []plex.Section{{Key: "1", Title: "Movies", Type: "movie"}},
		Items:    []Item{mk("a"), mk("b"), mk("c"), mk("d"), mk("e")},
	}
	gc := GenContext{Settings: Settings{DirectorThreshold: 2, ActorThreshold: 2, StudioThreshold: 2}}
	for _, c := range genPeople(scan, gc) {
		if len(c.Rules) != 0 {
			t.Errorf("people card %s carries rules %+v — membership-shaped cards must not", c.ID, c.Rules)
		}
	}
	for _, c := range genStudios(scan, gc) {
		if len(c.Rules) != 0 {
			t.Errorf("studio card %s carries rules %+v — membership-shaped cards must not", c.ID, c.Rules)
		}
	}
}
