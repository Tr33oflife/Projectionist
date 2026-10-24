package meta

// tmdb_saga_test.go — SagaPosition (v1.11.1 layman pack): a movie's
// place in its TMDb collection, counted by release date among ALL
// the parts TMDb lists (the saga as TMDb states it, owned or not).
// The contract under test: parts sort by release date ascending,
// parts with no stated date sort LAST (never invented a date),
// matching is by TMDb id, and every absence (no parts, not found)
// comes back as data — never an error, never a guessed position.

import (
	"context"
	"testing"
)

func part(id, date string) CollectionPart {
	return CollectionPart{TMDBID: id, Title: "Part " + id, ReleaseDate: date}
}

func TestSagaPositionSortsByReleaseDate(t *testing.T) {
	// Parts arrive in TMDb's own order (not necessarily release
	// order); the position counts by release date.
	parts := []CollectionPart{
		part("303", "2003-05-01"),
		part("101", "1999-03-31"),
		part("202", "2001-07-04"),
	}
	pos, total, found := SagaPosition(parts, 202)
	if !found || pos != 2 || total != 3 {
		t.Errorf("SagaPosition = %d of %d (found %v), want 2 of 3", pos, total, found)
	}
	pos, _, found = SagaPosition(parts, 101)
	if !found || pos != 1 {
		t.Errorf("earliest part = %d (found %v), want 1", pos, found)
	}
	pos, _, found = SagaPosition(parts, 303)
	if !found || pos != 3 {
		t.Errorf("latest part = %d (found %v), want 3", pos, found)
	}
	// The caller's slice is not reordered under it.
	if parts[0].TMDBID != "303" {
		t.Errorf("input slice was mutated: first part is now %s", parts[0].TMDBID)
	}
}

func TestSagaPositionUndatedPartsSortLast(t *testing.T) {
	parts := []CollectionPart{
		part("900", ""), // announced, no date stated
		part("101", "1999-03-31"),
		part("202", "2001-07-04"),
	}
	pos, total, found := SagaPosition(parts, 900)
	if !found || pos != 3 || total != 3 {
		t.Errorf("undated part = %d of %d (found %v), want 3 of 3 (undated sorts last)", pos, total, found)
	}
	pos, _, _ = SagaPosition(parts, 202)
	if pos != 2 {
		t.Errorf("dated part = %d, want 2 (undated part must not displace dated ones)", pos)
	}
}

func TestSagaPositionNotFoundAndEmpty(t *testing.T) {
	parts := []CollectionPart{part("101", "1999-03-31"), part("202", "2001-07-04")}
	pos, total, found := SagaPosition(parts, 555)
	if found || pos != 0 || total != 2 {
		t.Errorf("not-found = %d of %d (found %v), want 0 of 2, found=false (total still reported)", pos, total, found)
	}
	pos, total, found = SagaPosition(nil, 101)
	if found || pos != 0 || total != 0 {
		t.Errorf("empty parts = %d of %d (found %v), want 0 of 0, found=false", pos, total, found)
	}
}

func TestSagaPositionSinglePart(t *testing.T) {
	// A single-part collection HAS a position mechanically (1 of
	// 1); the evaluator is what refuses to state it — the helper
	// reports the facts.
	pos, total, found := SagaPosition([]CollectionPart{part("101", "1999-03-31")}, 101)
	if !found || pos != 1 || total != 1 {
		t.Errorf("single part = %d of %d (found %v), want 1 of 1", pos, total, found)
	}
}

func TestCollectionPartsFeedsSagaPosition(t *testing.T) {
	// End to end through the fake TMDb server: the collection's
	// parts list parses (ids as strings, dates verbatim) and
	// SagaPosition counts over it.
	c, done := tmdbOriginClient(t, `{
		"name": "The Example Collection",
		"parts": [
			{"id": 303, "title": "Third", "release_date": "2003-05-01", "poster_path": "/c.jpg"},
			{"id": 101, "title": "First", "release_date": "1999-03-31", "poster_path": "/a.jpg"},
			{"id": 202, "title": "Second", "release_date": "2001-07-04", "poster_path": "/b.jpg"}
		]
	}`, nil)
	defer done()
	cp, err := c.CollectionParts(context.Background(), 11)
	if err != nil {
		t.Fatal(err)
	}
	if cp.Name != "The Example Collection" || len(cp.Parts) != 3 {
		t.Fatalf("CollectionParts = %q with %d parts, want the name and 3 parts", cp.Name, len(cp.Parts))
	}
	pos, total, found := SagaPosition(cp.Parts, 202)
	if !found || pos != 2 || total != 3 {
		t.Errorf("SagaPosition over fetched parts = %d of %d (found %v), want 2 of 3", pos, total, found)
	}
}
