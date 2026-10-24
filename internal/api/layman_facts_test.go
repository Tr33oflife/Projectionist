package api

// layman_facts_test.go — saga facts at the facts layer (v1.11.1
// layman pack). The saga position/total ride the SAME TMDb gate as
// the v1.11 franchise fact: no TMDb key configured → both facts are
// ABSENT from the facts map (never zero, never guessed), so the
// family hides with its reason. The positive path (key configured,
// collection present) is proven at the meta layer against a fake
// TMDb server (tmdb_saga_test.go) and at the evaluator in
// composite.test.cjs — the TMDb base URL is a package constant in
// meta, so the api harness cannot substitute a fake TMDb; what the
// api layer owns, and what is pinned here, is the gate.

import "testing"

func TestLaymanSagaFactsAbsentWithoutTMDbKey(t *testing.T) {
	pms := fakeBadgeLibraryPMS(t)
	defer pms.Close()
	app := appForFakePMS(t, pms.URL)
	defer app.Close()

	// Movie 900's Guid carries a TMDb id, but no key is configured:
	// franchise was already pinned absent for exactly this case;
	// the saga facts must be absent beside it.
	f := postFacts(t, app, "900")
	if f["sagaPosition"] != nil || f["sagaTotal"] != nil {
		t.Errorf("saga facts must be absent without a TMDb key: position=%v total=%v",
			f["sagaPosition"], f["sagaTotal"])
	}

	// Shows never carry saga facts either.
	f = postFacts(t, app, "910")
	if f["sagaPosition"] != nil || f["sagaTotal"] != nil {
		t.Errorf("shows must not carry saga facts: position=%v total=%v",
			f["sagaPosition"], f["sagaTotal"])
	}
}
