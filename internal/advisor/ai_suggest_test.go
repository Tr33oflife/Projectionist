package advisor

// ai_suggest_test.go — v1.21: the AI suggestion run's verification
// contract. A scripted fake proposer stands in for the AI client;
// the scan is synthetic. The rules under test are the feature's
// honesty guarantees: hallucinated keys are dropped and counted,
// below-minimum suggestions are suppressed, near-duplicates are
// deduped deterministically, dismissed ids never resurrect, and a
// malformed batch aborts the whole run with no cards.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"plexcollectionmanager/internal/meta"
	"plexcollectionmanager/internal/plex"
)

// fakeProposer returns scripted replies per call and records the
// prompts it was given.
type fakeProposer struct {
	replies [][]meta.CollectionProposal
	errs    map[int]error
	prompts []string
}

func (f *fakeProposer) ProposeCollections(ctx context.Context, user string) ([]meta.CollectionProposal, error) {
	f.prompts = append(f.prompts, user)
	i := len(f.prompts) - 1
	if f.errs != nil {
		if err, bad := f.errs[i]; bad {
			return nil, err
		}
	}
	if i < len(f.replies) {
		return f.replies[i], nil
	}
	return []meta.CollectionProposal{}, nil
}

// aiScan builds a scan with n movie items in section "1", keys
// "1".."n", titles sorted-friendly ("Title 001" ...).
func aiScan(n int) *Scan {
	yr := 2000
	items := make([]Item, 0, n)
	for i := 1; i <= n; i++ {
		y := yr + (i % 20)
		items = append(items, Item{LibraryItem: plex.LibraryItem{
			RatingKey:  fmt.Sprintf("%d", i),
			Title:      fmt.Sprintf("Title %03d", i),
			Year:       &y,
			Type:       "movie",
			SectionKey: "1",
		}})
	}
	return &Scan{
		MachineID: "m1",
		Sections:  []plex.Section{{Key: "1", Title: "Movies", Type: "movie"}},
		Items:     items,
		ItemCount: n,
	}
}

func keysOf(cards []Card) map[string][]string {
	out := map[string][]string{}
	for _, c := range cards {
		out[c.Title] = c.MemberRatingKeys
	}
	return out
}

func TestAISuggestCleanProposals(t *testing.T) {
	fp := &fakeProposer{replies: [][]meta.CollectionProposal{{
		{Title: "First Shelf", Summary: "s1", Rationale: "r1", MemberRatingKeys: []string{"1", "2", "3", "4", "5", "6"}},
		{Title: "Second Shelf", Summary: "s2", Rationale: "r2", MemberRatingKeys: []string{"10", "11", "12", "13", "14"}},
	}}}
	out, err := SuggestCollections(context.Background(), aiScan(20), "1", fp, "test-model", nil, nil)
	if err != nil {
		t.Fatalf("SuggestCollections: %v", err)
	}
	if out.Calls != 1 || len(out.Cards) != 2 {
		t.Fatalf("calls=%d cards=%d, want 1 call / 2 cards", out.Calls, len(out.Cards))
	}
	byTitle := keysOf(out.Cards)
	if len(byTitle["First Shelf"]) != 6 || len(byTitle["Second Shelf"]) != 5 {
		t.Fatalf("members = %v", byTitle)
	}
	for _, c := range out.Cards {
		if c.Kind != "ai" || c.AIModel != "test-model" || c.AISuggested != c.AIVerified || c.AIVerified != len(c.MemberRatingKeys) {
			t.Fatalf("card provenance wrong: %+v", c)
		}
		if !IsValidCard(c) {
			t.Fatalf("AI card must satisfy the shared card contract: %+v", c)
		}
		if !strings.HasPrefix(c.ID, "ai:1:") {
			t.Fatalf("card id = %q, want ai:1: prefix", c.ID)
		}
	}
}

func TestAISuggestHallucinatedKeysDroppedAndCounted(t *testing.T) {
	fp := &fakeProposer{replies: [][]meta.CollectionProposal{{
		// 7 distinct keys returned; "998"/"999" are not in the library → 5 verified.
		{Title: "Partly Real", MemberRatingKeys: []string{"1", "2", "3", "4", "5", "998", "999"}},
		// Duplicate keys in one reply count once.
		{Title: "Dupe Keys", MemberRatingKeys: []string{"6", "6", "7", "8", "9", "10"}},
	}}}
	out, err := SuggestCollections(context.Background(), aiScan(20), "1", fp, "m", nil, nil)
	if err != nil {
		t.Fatalf("SuggestCollections: %v", err)
	}
	if len(out.Cards) != 2 {
		t.Fatalf("cards = %d, want 2", len(out.Cards))
	}
	for _, c := range out.Cards {
		switch c.Title {
		case "Partly Real":
			if c.AISuggested != 7 || c.AIVerified != 5 || len(c.MemberRatingKeys) != 5 {
				t.Fatalf("Partly Real counts: suggested=%d verified=%d members=%d", c.AISuggested, c.AIVerified, len(c.MemberRatingKeys))
			}
		case "Dupe Keys":
			if c.AISuggested != 5 || c.AIVerified != 5 {
				t.Fatalf("Dupe Keys counts: suggested=%d verified=%d", c.AISuggested, c.AIVerified)
			}
		}
	}
}

func TestAISuggestBelowMinimumSuppressed(t *testing.T) {
	fp := &fakeProposer{replies: [][]meta.CollectionProposal{{
		{Title: "Too Small", MemberRatingKeys: []string{"1", "2", "3", "4"}},       // 4 verified < 5
		{Title: "Mostly Fake", MemberRatingKeys: []string{"1", "2", "900", "901"}}, // 2 verified
		{Title: "Just Right", MemberRatingKeys: []string{"1", "2", "3", "4", "5"}},
	}}}
	out, err := SuggestCollections(context.Background(), aiScan(20), "1", fp, "m", nil, nil)
	if err != nil {
		t.Fatalf("SuggestCollections: %v", err)
	}
	if len(out.Cards) != 1 || out.Cards[0].Title != "Just Right" {
		t.Fatalf("cards = %+v", out.Cards)
	}
	if out.Suppressed != 2 {
		t.Fatalf("suppressed = %d, want 2", out.Suppressed)
	}
}

func TestAISuggestDuplicatePairKeepsLarger(t *testing.T) {
	// B shares 5 of A's 6 members and adds none of its own beyond:
	// |A∩B| = 5, |A∪B| = 6 → J = 0.833 ≥ 0.8 → duplicate; the larger set (A) wins.
	fp := &fakeProposer{replies: [][]meta.CollectionProposal{{
		{Title: "Shelf A", MemberRatingKeys: []string{"1", "2", "3", "4", "5", "6"}},
		{Title: "Shelf B", MemberRatingKeys: []string{"1", "2", "3", "4", "5"}},
		// Small first, larger second: the larger replaces the kept small one.
		{Title: "Shelf C", MemberRatingKeys: []string{"10", "11", "12", "13", "14"}},
		{Title: "Shelf D", MemberRatingKeys: []string{"10", "11", "12", "13", "14", "15"}},
	}}}
	out, err := SuggestCollections(context.Background(), aiScan(30), "1", fp, "m", nil, nil)
	if err != nil {
		t.Fatalf("SuggestCollections: %v", err)
	}
	byTitle := keysOf(out.Cards)
	if _, ok := byTitle["Shelf B"]; ok {
		t.Fatalf("Shelf B should have been deduped against Shelf A: %v", byTitle)
	}
	if len(byTitle["Shelf A"]) != 6 {
		t.Fatalf("Shelf A members = %v", byTitle["Shelf A"])
	}
	if _, ok := byTitle["Shelf C"]; ok {
		t.Fatalf("Shelf C should have been replaced by the larger Shelf D: %v", byTitle)
	}
	if len(byTitle["Shelf D"]) != 6 {
		t.Fatalf("Shelf D members = %v", byTitle["Shelf D"])
	}
}

func TestAISuggestExistingCollectionGuards(t *testing.T) {
	existing := []ExistingCollection{
		{Title: "Already There", Keys: []string{"1", "2", "3", "4", "5", "6"}},
		{Title: "Title Twin", Keys: nil}, // title-only detection
	}
	fp := &fakeProposer{replies: [][]meta.CollectionProposal{{
		{Title: "Copy Shelf", MemberRatingKeys: []string{"1", "2", "3", "4", "5", "6"}}, // J=1.0 vs existing
		{Title: "title twin!", MemberRatingKeys: []string{"7", "8", "9", "10", "11"}},   // normalized title match
		{Title: "Fresh Shelf", MemberRatingKeys: []string{"7", "8", "9", "10", "11", "12"}},
	}}}
	out, err := SuggestCollections(context.Background(), aiScan(20), "1", fp, "m", existing, nil)
	if err != nil {
		t.Fatalf("SuggestCollections: %v", err)
	}
	if len(out.Cards) != 1 || out.Cards[0].Title != "Fresh Shelf" {
		t.Fatalf("cards = %+v", out.Cards)
	}
	// The prompt must carry the existing titles so the model can avoid them.
	if len(fp.prompts) != 1 || !strings.Contains(fp.prompts[0], "Already There") {
		t.Fatalf("prompt missing existing titles: %q", fp.prompts)
	}
}

func TestAISuggestDismissedNeverResurrects(t *testing.T) {
	fp := &fakeProposer{replies: [][]meta.CollectionProposal{{
		{Title: "Waved Off", MemberRatingKeys: []string{"1", "2", "3", "4", "5"}},
		{Title: "Still Welcome", MemberRatingKeys: []string{"6", "7", "8", "9", "10"}},
	}}}
	dismissed := []string{aiCardID("1", "Waved Off")}
	out, err := SuggestCollections(context.Background(), aiScan(20), "1", fp, "m", nil, dismissed)
	if err != nil {
		t.Fatalf("SuggestCollections: %v", err)
	}
	if len(out.Cards) != 1 || out.Cards[0].Title != "Still Welcome" {
		t.Fatalf("cards = %+v", out.Cards)
	}
	// …and the dismissed title is in the prompt's avoid list (slug un-slugged).
	if !strings.Contains(fp.prompts[0], "waved off") {
		t.Fatalf("prompt missing dismissed title: %q", fp.prompts[0])
	}
}

func TestAISuggestMalformedAbortsWholeRun(t *testing.T) {
	boom := errors.New("The AI's reply could not be parsed as collection suggestions.")
	fp := &fakeProposer{
		replies: [][]meta.CollectionProposal{{
			{Title: "Good Batch", MemberRatingKeys: []string{"1", "2", "3", "4", "5"}},
		}},
		errs: map[int]error{1: boom}, // second batch fails
	}
	// 250 items → two batches under the 200-item cap.
	out, err := SuggestCollections(context.Background(), aiScan(250), "1", fp, "m", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "could not be parsed") {
		t.Fatalf("err = %v, want the parse error", err)
	}
	if out != nil {
		t.Fatalf("outcome = %+v, want nil — no partial state", out)
	}
}

func TestAISuggestBatchingCaps(t *testing.T) {
	// 450 items → 3 calls at the 200-item cap, every item seen once.
	fp := &fakeProposer{}
	out, err := SuggestCollections(context.Background(), aiScan(450), "1", fp, "m", nil, nil)
	if err != nil {
		t.Fatalf("SuggestCollections: %v", err)
	}
	if out.Calls != 3 || len(fp.prompts) != 3 {
		t.Fatalf("calls = %d prompts = %d, want 3", out.Calls, len(fp.prompts))
	}
	if out.SawItems != 450 || out.TotalItems != 450 || out.Sampled {
		t.Fatalf("saw=%d total=%d sampled=%v", out.SawItems, out.TotalItems, out.Sampled)
	}
	for i, p := range fp.prompts {
		lines := strings.Count(p, "Title ")
		if lines > AISuggestBatchItems {
			t.Fatalf("batch %d carries %d item lines, cap is %d", i, lines, AISuggestBatchItems)
		}
	}
	// Over-budget section: sampled evenly, disclosed, calls capped.
	big := &fakeProposer{}
	out, err = SuggestCollections(context.Background(), aiScan(AISuggestBatchItems*AISuggestMaxBatches+500), "1", big, "m", nil, nil)
	if err != nil {
		t.Fatalf("SuggestCollections big: %v", err)
	}
	if !out.Sampled || out.Calls != AISuggestMaxBatches || out.SawItems != AISuggestBatchItems*AISuggestMaxBatches {
		t.Fatalf("big run: sampled=%v calls=%d saw=%d", out.Sampled, out.Calls, out.SawItems)
	}
}

func TestAISuggestEmptySectionErrors(t *testing.T) {
	scan := aiScan(5)
	fp := &fakeProposer{}
	if _, err := SuggestCollections(context.Background(), scan, "99", fp, "m", nil, nil); err == nil {
		t.Fatal("unknown/empty section must error")
	}
	if len(fp.prompts) != 0 {
		t.Fatalf("no AI call may happen for an empty section, got %d", len(fp.prompts))
	}
}
