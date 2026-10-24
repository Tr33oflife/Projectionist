package store

// custompins_test.go — v1.24 Tier 1 free-text pins: create/list/
// remove, validation, machine separation, and whole-file backup
// round-trip.

import (
	"testing"
)

func TestCustomPinsRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	refs := []CustomPinRef{{RatingKey: "1", Title: "Alpha"}, {RatingKey: "3", Title: "Gamma"}}
	created, err := s.SetCustomPins("machine-1", refs, "STAFF PICK", "#fbbf24", "tr", "signature", "Staff Shelf")
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 {
		t.Fatalf("created = %d pins, want 2 (one per item)", len(created))
	}
	if created[0].ID == "" || created[0].ID == created[1].ID {
		t.Error("each pin needs a distinct non-empty id")
	}
	// List is sorted by title, and pins are per-machine.
	list, err := s.CustomPins("machine-1")
	if err != nil || len(list) != 2 || list[0].Title != "Alpha" || list[1].Title != "Gamma" {
		t.Fatalf("list wrong: %v %v", list, err)
	}
	other, _ := s.CustomPins("machine-2")
	if len(other) != 0 {
		t.Error("pins must never leak across machine identities")
	}
	// Per-item lookup is what the apply path reads.
	forItem := s.CustomPinsForItem("machine-1", "3")
	if len(forItem) != 1 || forItem[0].Text != "STAFF PICK" || forItem[0].Source != "Staff Shelf" {
		t.Fatalf("pins for item 3 wrong: %v", forItem)
	}
	// Remove is by pin id, and removing a missing pin is a no-op.
	if err := s.RemoveCustomPin("machine-1", created[0].ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.CustomPins("machine-1"); len(list) != 1 {
		t.Fatalf("after remove: %d pins, want 1", len(list))
	}
	if err := s.RemoveCustomPin("machine-1", "pin-does-not-exist"); err != nil {
		t.Errorf("removing a missing pin must be a no-op, got %v", err)
	}
}

func TestCustomPinsValidation(t *testing.T) {
	s := New(t.TempDir())
	refs := []CustomPinRef{{RatingKey: "1", Title: "Alpha"}}
	if _, err := s.SetCustomPins("m", refs, "   ", "#fbbf24", "bl", "signature", ""); err == nil {
		t.Error("blank text must be refused")
	}
	if _, err := s.SetCustomPins("m", refs, "this text is far longer than forty characters, obviously", "#fbbf24", "bl", "signature", ""); err == nil {
		t.Error("over-long text must be refused, not silently truncated")
	}
	if _, err := s.SetCustomPins("m", nil, "OK", "#fbbf24", "bl", "signature", ""); err == nil {
		t.Error("no items must be refused")
	}
	if _, err := s.SetCustomPins("", refs, "OK", "#fbbf24", "bl", "signature", ""); err == nil {
		t.Error("no machine must be refused")
	}
	// Bad color/corner/variant fall back — presentation, not content.
	created, err := s.SetCustomPins("m", refs, "OK", "#nope", "middle", "neon", "")
	if err != nil {
		t.Fatal(err)
	}
	if created[0].Color != "#e2e8f0" || created[0].Corner != "bl" || created[0].Variant != "signature" {
		t.Errorf("fallbacks wrong: %+v", created[0])
	}
}

func TestCustomPinsAllRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.SetCustomPins("m1", []CustomPinRef{{RatingKey: "9", Title: "Delta"}}, "DELTA", "#22d3ee", "br", "aurora", ""); err != nil {
		t.Fatal(err)
	}
	all, err := s.CustomPinsAll()
	if err != nil || len(all["m1"]) != 1 {
		t.Fatalf("CustomPinsAll wrong: %v %v", all, err)
	}
	// Whole-file replace validates before writing: a bad pin changes
	// nothing.
	bad := map[string]map[string]CustomPin{
		"m1": {"pin-x": {ID: "pin-x", RatingKey: "9", Title: "Delta", Text: "", Color: "#22d3ee", Corner: "br"}},
	}
	if err := s.SetCustomPinsAll(bad); err == nil {
		t.Error("SetCustomPinsAll must refuse an empty-text pin")
	}
	if list, _ := s.CustomPins("m1"); len(list) != 1 {
		t.Error("failed whole-file import must leave the stored pins untouched")
	}
	// A good whole-file replace normalizes presentation fields.
	good := map[string]map[string]CustomPin{
		"m1": {"pin-y": {ID: "pin-y", RatingKey: "9", Title: "Delta", Text: "NEW", Color: "#nope", Corner: "xx", Variant: "neon"}},
	}
	if err := s.SetCustomPinsAll(good); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.CustomPins("m1"); len(list) != 1 || list[0].Text != "NEW" || list[0].Color != "#e2e8f0" {
		t.Errorf("normalized import wrong: %v", list)
	}
}
