package store

// leaving_test.go — the Leaving list store contract (v1.12):
// round-trip through the file, per-server isolation, strict date
// validation, and expired entries surviving a save/load cycle
// (expiry is a display state, never a silent deletion).

import (
	"testing"
	"time"
)

func TestValidLeavingDate(t *testing.T) {
	valid := []string{"2026-03-05", "2024-02-29", "1999-12-31", "2030-01-01"}
	for _, d := range valid {
		if !ValidLeavingDate(d) {
			t.Errorf("ValidLeavingDate(%q) = false, want true", d)
		}
	}
	invalid := []string{
		"2026-02-30", // not a real calendar date
		"2025-02-29", // 2025 is not a leap year
		"2026-13-01", // no month 13
		"2026-00-10", // no month 0
		"2026-3-5",   // not the strict stored form
		"03/05/2026", // US display form is not the stored form
		"2026/03/05",
		"",
		"tomorrow",
		"2026-03-05T00:00:00Z",
	}
	for _, d := range invalid {
		if ValidLeavingDate(d) {
			t.Errorf("ValidLeavingDate(%q) = true, want false", d)
		}
	}
}

func TestLeavingRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	const machine = "machine-A"
	items := []LeavingItemRef{
		{RatingKey: "101", Title: "Alpha"},
		{RatingKey: "102", Title: "Beta"},
	}
	if err := s.SetLeaving(machine, items, "2026-11-15"); err != nil {
		t.Fatalf("SetLeaving: %v", err)
	}
	// A fresh Store over the same dir must see the same list — the
	// file is the record, not process memory.
	s2 := New(s.dir)
	entries, err := s2.LeavingEntries(machine)
	if err != nil {
		t.Fatalf("LeavingEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if e := entries["101"]; e.Title != "Alpha" || e.Date != "2026-11-15" {
		t.Errorf("entry 101 = %+v, want Alpha / 2026-11-15", e)
	}
	if e, ok := s2.LeavingEntryFor(machine, "102"); !ok || e.Date != "2026-11-15" {
		t.Errorf("LeavingEntryFor(102) = %+v, %v", e, ok)
	}
	// Editing a date replaces the entry in place.
	if err := s2.SetLeaving(machine, []LeavingItemRef{{RatingKey: "101", Title: "Alpha"}}, "2026-12-01"); err != nil {
		t.Fatalf("SetLeaving edit: %v", err)
	}
	if e, _ := s2.LeavingEntryFor(machine, "101"); e.Date != "2026-12-01" {
		t.Errorf("after edit, entry 101 date = %q, want 2026-12-01", e.Date)
	}
	// Remove deletes exactly that entry; removing again is a no-op.
	if err := s2.RemoveLeaving(machine, "101"); err != nil {
		t.Fatalf("RemoveLeaving: %v", err)
	}
	if _, ok := s2.LeavingEntryFor(machine, "101"); ok {
		t.Error("entry 101 still present after remove")
	}
	if err := s2.RemoveLeaving(machine, "101"); err != nil {
		t.Errorf("second RemoveLeaving should be a no-op, got %v", err)
	}
	if _, ok := s2.LeavingEntryFor(machine, "102"); !ok {
		t.Error("entry 102 was removed by another entry's remove")
	}
}

func TestLeavingPerServerIsolation(t *testing.T) {
	s := New(t.TempDir())
	// The SAME ratingKey on two machines is two different titles.
	if err := s.SetLeaving("machine-A", []LeavingItemRef{{RatingKey: "42", Title: "On A"}}, "2026-11-01"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLeaving("machine-B", []LeavingItemRef{{RatingKey: "42", Title: "On B"}}, "2026-12-25"); err != nil {
		t.Fatal(err)
	}
	a, _ := s.LeavingEntryFor("machine-A", "42")
	b, _ := s.LeavingEntryFor("machine-B", "42")
	if a.Date != "2026-11-01" || a.Title != "On A" {
		t.Errorf("machine-A entry = %+v, want On A / 2026-11-01", a)
	}
	if b.Date != "2026-12-25" || b.Title != "On B" {
		t.Errorf("machine-B entry = %+v, want On B / 2026-12-25", b)
	}
	// Removing on one server leaves the other's entry untouched.
	if err := s.RemoveLeaving("machine-A", "42"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.LeavingEntryFor("machine-A", "42"); ok {
		t.Error("machine-A entry survived its own remove")
	}
	if _, ok := s.LeavingEntryFor("machine-B", "42"); !ok {
		t.Error("machine-B entry was removed by machine-A's remove")
	}
}

func TestLeavingInvalidDatesRejected(t *testing.T) {
	s := New(t.TempDir())
	for _, d := range []string{"2026-02-30", "2026-13-40", "not-a-date", "", "03/05/2026"} {
		err := s.SetLeaving("machine-A", []LeavingItemRef{{RatingKey: "1", Title: "X"}}, d)
		if err == nil {
			t.Errorf("SetLeaving with date %q: no error, want rejection", d)
		}
	}
	// Nothing was written by any of the rejected sets.
	entries, err := s.LeavingEntries("machine-A")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("entries = %d after rejected sets, want 0", len(entries))
	}
}

func TestLeavingExpiredEntriesPreserved(t *testing.T) {
	s := New(t.TempDir())
	// A date far in the past: expired on any clock the tests run on.
	if err := s.SetLeaving("machine-A", []LeavingItemRef{{RatingKey: "7", Title: "Long Gone"}}, "2001-01-01"); err != nil {
		t.Fatal(err)
	}
	// An unrelated write must not sweep it away either.
	if err := s.SetLeaving("machine-A", []LeavingItemRef{{RatingKey: "8", Title: "Fresh"}}, "2999-01-01"); err != nil {
		t.Fatal(err)
	}
	s2 := New(s.dir)
	entries, err := s2.LeavingEntries("machine-A")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 (expired entry preserved)", len(entries))
	}
	if e := entries["7"]; e.Date != "2001-01-01" {
		t.Errorf("expired entry = %+v, want date 2001-01-01 intact", e)
	}
	days, ok := DaysUntilLeaving("2001-01-01", time.Now())
	if !ok || days >= 0 {
		t.Errorf("DaysUntilLeaving(2001-01-01) = %d, %v — want a negative count", days, ok)
	}
}

func TestDaysUntilLeaving(t *testing.T) {
	// A fixed "now": 2026-10-23 local, mid-day — the civil-date math
	// must ignore the time of day entirely.
	now := time.Date(2026, 10, 23, 15, 42, 0, 0, time.Local)
	cases := []struct {
		date string
		want int
	}{
		{"2026-10-23", 0},  // today
		{"2026-10-24", 1},  // tomorrow
		{"2026-10-22", -1}, // yesterday — past, signed
		{"2026-11-22", 30}, // the soon-window edge at the default 30
		{"2027-10-23", 365},
		{"2025-10-23", -365},
	}
	for _, c := range cases {
		got, ok := DaysUntilLeaving(c.date, now)
		if !ok || got != c.want {
			t.Errorf("DaysUntilLeaving(%q) = %d, %v — want %d, true", c.date, got, ok, c.want)
		}
	}
	if _, ok := DaysUntilLeaving("2026-02-30", now); ok {
		t.Error("DaysUntilLeaving accepted an impossible date")
	}
}
