package plex

// live_smart_test.go — v1.14 smart collections, end to end against
// the throwaway sandbox PMS (same gating as live_test.go: skipped
// unless PCM_LIVE_PLEX is set). Proves the whole claim set on a
// REAL server:
//   1. CreateCollection(smart) stores the rule (record reads back
//      smart with the filter URI in `content`);
//   2. the listing surfaces the rule summary;
//   3. members resolve through GetCollectionItems (the collections
//      children path — the metadata path is empty for smart
//      collections on PMS 1.43);
//   4. membership is DYNAMIC: editing a fixture's year into the
//      rule's range adds it with NO collection write, and restoring
//      the year removes it again.
// The created collection is left on the sandbox for inspection,
// like the other live tests (the client deliberately cannot
// delete). The fixture's year is always restored, even on failure.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func liveRawYear(t *testing.T, base, key string, year int) {
	t.Helper()
	u := fmt.Sprintf("%s/library/metadata/%s?year.value=%d&year.locked=1&X-Plex-Token=livetest-dummy-token",
		strings.TrimSuffix(base, "/"), key, year)
	req, err := http.NewRequest(http.MethodPut, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		t.Fatalf("PUT year=%d on %s: HTTP %d", year, key, res.StatusCode)
	}
}

func liveItemKeys(items []Item) map[string]bool {
	out := map[string]bool{}
	for _, it := range items {
		out[it.RatingKey] = true
	}
	return out
}

func TestLiveSmartCollection(t *testing.T) {
	server := liveServer(t)
	base := os.Getenv("PCM_LIVE_PLEX")
	c := NewClient("PCM", "1", "live-smart-test")
	ctx := context.Background()
	title := "PCM Live Smart " + time.Now().Format("20060102-150405")

	created, err := c.CreateCollection(ctx, server, CreatePayload{
		SectionKey: "1", Type: 1, Title: title, Smart: true,
		FilterQuery: "decade=2000",
	})
	if err != nil {
		t.Fatalf("CreateCollection(smart): %v", err)
	}
	var shape struct {
		RatingKey string `json:"ratingKey"`
	}
	if err := json.Unmarshal(created, &shape); err != nil || shape.RatingKey == "" {
		t.Fatalf("create result = %s (err %v), want a ratingKey", created, err)
	}
	rk := shape.RatingKey
	t.Logf("created smart collection rk=%s title=%q", rk, title)

	// (1)+(2) read back through the listing: smart, rule, summary.
	cols, err := c.GetCollections(ctx, server, "1")
	if err != nil {
		t.Fatalf("GetCollections: %v", err)
	}
	var mine *Collection
	for i := range cols {
		if cols[i].RatingKey == rk {
			mine = &cols[i]
			break
		}
	}
	if mine == nil {
		t.Fatalf("created collection %s not in the section listing", rk)
	}
	if !mine.Smart {
		t.Errorf("listing entry smart = false, want true: %+v", *mine)
	}
	if !strings.Contains(mine.Content, "decade=2000") {
		t.Errorf("listing content = %q, want the stored filter URI with decade=2000", mine.Content)
	}
	if mine.SmartSummary != "Decade is 2000" {
		t.Errorf("listing smartSummary = %q, want 'Decade is 2000'", mine.SmartSummary)
	}

	// (3) members resolve: the sandbox fixtures from the 2000s
	// decade are rk 1 (2001), 2 (2002), 9 (2004). rk3 (2025) is out.
	items, err := c.GetCollectionItems(ctx, server, rk)
	if err != nil {
		t.Fatalf("GetCollectionItems(smart): %v", err)
	}
	keys := liveItemKeys(items)
	for _, want := range []string{"1", "2", "9"} {
		if !keys[want] {
			t.Errorf("smart members = %v, want fixture %s present", keys, want)
		}
	}
	if keys["3"] {
		t.Errorf("smart members include rk3 (year 2025) under decade=2000: %v", keys)
	}

	// (4) dynamic membership: move rk3's year into the decade.
	liveRawYear(t, base, "3", 2003)
	defer liveRawYear(t, base, "3", 2025) // always restore the fixture
	deadline := time.Now().Add(15 * time.Second)
	joined := false
	for time.Now().Before(deadline) {
		items, err = c.GetCollectionItems(ctx, server, rk)
		if err == nil && liveItemKeys(items)["3"] {
			joined = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !joined {
		t.Fatalf("rk3 never joined the smart collection after its year moved to 2003 — membership is not dynamic")
	}
	t.Log("rk3 joined dynamically after the year edit (no collection write)")

	// Restore and confirm it drops back out.
	liveRawYear(t, base, "3", 2025)
	deadline = time.Now().Add(15 * time.Second)
	left := false
	for time.Now().Before(deadline) {
		items, err = c.GetCollectionItems(ctx, server, rk)
		if err == nil && !liveItemKeys(items)["3"] {
			left = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !left {
		t.Fatalf("rk3 still in the smart collection after its year was restored to 2025")
	}
	// The deferred restore above is now redundant but harmless;
	// confirm the fixture's year really is 2025 again for the next
	// run.
	item, err := c.GetItem(ctx, server, "3")
	if err != nil {
		t.Fatalf("GetItem(3): %v", err)
	}
	if item.Year == nil || *item.Year != 2025 {
		t.Errorf("fixture rk3 year = %v after restore, want 2025", item.Year)
	}
}
