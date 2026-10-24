package advisor

// ai_suggest.go — v1.21: AI collection SUGGESTIONS. The deterministic
// generators in suggest.go mine the scan for patterns; this file lets
// the configured AI propose collections itself from the same scan.
// The honesty rules are the feature:
//
//   - The model only ever sees compact lines built from the most
//     recent scan (ratingKey, title, year, genres, content rating,
//     a rating where one is present) — never anything that did not
//     come from the owner's own library.
//   - EVERY key the model returns is intersected with the scan's
//     real items for the section. Hallucinated keys are dropped and
//     counted (Card.AISuggested vs Card.AIVerified); a suggestion
//     needs >= AISuggestMinMembers VERIFIED members to render.
//   - Near-duplicates are dropped deterministically: a normalized
//     title match against an existing collection, or member-overlap
//     Jaccard >= 0.8 against an existing collection's members or an
//     already-kept suggestion (the larger set wins among AI pairs).
//   - A malformed batch reply aborts the whole run with the error —
//     no cards, no partial state.
//   - Dismissals persist as card ids "ai:<sectionKey>:<title-slug>"
//     in the shared dismissal store; a dismissed id is filtered here
//     AND its title is fed back into the prompt's avoid list, so a
//     re-run does not resurrect what the owner already waved off.
//
// Batching (stated caps, enforced by aiBatches): at most
// AISuggestBatchItems item lines and at most AISuggestBatchChars
// characters of item lines per AI call, and at most
// AISuggestMaxBatches calls per run. A section larger than the whole
// budget is sampled EVENLY (title-sorted, fixed stride) and the
// outcome says so (Sampled + SawItems), so the UI can disclose that
// the AI saw a spread rather than the whole section.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"plexcollectionmanager/internal/meta"
)

const (
	AISuggestBatchItems = 200   // max item lines per AI call
	AISuggestBatchChars = 30000 // max characters of item lines per AI call
	AISuggestMaxBatches = 10    // max AI calls per run
	AISuggestMinMembers = 5     // verified members a suggestion needs to render
	AISuggestMaxCards   = 12    // cards kept per run, largest first

	aiSuggestOverlap = 0.8 // Jaccard at/above which two sets are the same collection
)

// CollectionProposer is the slice of *meta.AIClient this feature
// needs — one batch call. A fake stands in for it in tests.
type CollectionProposer interface {
	ProposeCollections(ctx context.Context, user string) ([]meta.CollectionProposal, error)
}

// ExistingCollection is one collection already in the section: its
// title and, when the caller could load them, its member ratingKeys
// (empty Keys means title-only duplicate detection for that one).
type ExistingCollection struct {
	Title string
	Keys  []string
}

// AISuggestOutcome is one run's result: the verified cards plus the
// cost/coverage facts the UI reports (calls made, items seen).
type AISuggestOutcome struct {
	Cards      []Card `json:"cards"`
	Calls      int    `json:"calls"`
	SawItems   int    `json:"sawItems"`
	TotalItems int    `json:"totalItems"`
	Sampled    bool   `json:"sampled"`
	Suppressed int    `json:"suppressed"` // proposals dropped by verification/dedupe
}

// --- ids and titles -----------------------------------------------------------

// aiTitleSlug normalizes a title into the id-safe slug used in AI
// card ids: lowercase, runs of non [a-z0-9] become one '-'.
func aiTitleSlug(title string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	if s == "" {
		s = "untitled"
	}
	return s
}

// aiCardID is the stable dismissal id for one AI suggestion:
// derived from the section and the normalized title so the same
// proposal on a re-run maps to the same id (and stays dismissed).
func aiCardID(sectionKey, title string) string {
	return "ai:" + sectionKey + ":" + aiTitleSlug(title)
}

// normalizeAITitle is the comparison form for title-level duplicate
// detection: lowercase, punctuation dropped, whitespace collapsed.
func normalizeAITitle(title string) string {
	return strings.Join(strings.Fields(aiTitleSlug(title)), " ")
}

// DismissedAITitles recovers approximate titles from dismissed AI
// card ids for one section, for the prompt's avoid list. The store
// keeps ids only, so the slug is un-slugged back to words — close
// enough for "do not re-suggest this" (the exact id filter in
// SuggestCollections is the hard guarantee).
func DismissedAITitles(dismissedIDs []string, sectionKey string) []string {
	prefix := "ai:" + sectionKey + ":"
	seen := map[string]bool{}
	out := []string{}
	for _, id := range dismissedIDs {
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		title := strings.TrimSpace(strings.ReplaceAll(strings.TrimPrefix(id, prefix), "-", " "))
		if title != "" && !seen[title] {
			seen[title] = true
			out = append(out, title)
		}
	}
	sort.Strings(out)
	return out
}

// --- batching -----------------------------------------------------------------

// aiItemLine renders one scan item as the compact line the model
// sees: key|title|year|genres|content rating|score. The score is the
// first rating actually present (TMDB vote average, then IMDb, then
// the Plex audience rating) — absent facts stay absent.
func aiItemLine(it Item) string {
	year := ""
	if it.Year != nil {
		year = fmt.Sprintf("%d", *it.Year)
	}
	score := ""
	switch {
	case it.TMDB != nil && it.TMDB.VoteAverage != nil:
		score = fmt.Sprintf("%.1f", *it.TMDB.VoteAverage)
	case it.IMDBRating != nil:
		score = fmt.Sprintf("%.1f", *it.IMDBRating)
	case it.PlexRating != nil:
		score = fmt.Sprintf("%.1f", *it.PlexRating)
	}
	genres := itemGenres(it)
	parts := make([]string, 0, len(genres))
	for _, g := range genres {
		parts = append(parts, strings.ReplaceAll(g, "|", "/"))
	}
	return strings.Join([]string{
		it.RatingKey,
		strings.ReplaceAll(it.Title, "|", "/"),
		year,
		strings.Join(parts, ", "),
		it.ContentRating,
		score,
	}, "|")
}

// aiBatches splits section items into call-sized batches under the
// stated caps. Items are title-sorted first (deterministic across
// runs); a section bigger than the whole budget is sampled evenly
// across that order. Returns the batches and whether sampling cut
// the section down.
func aiBatches(items []Item) ([][]Item, bool) {
	sorted := make([]Item, len(items))
	copy(sorted, items)
	sort.SliceStable(sorted, func(i, j int) bool {
		ti, tj := strings.ToLower(sorted[i].Title), strings.ToLower(sorted[j].Title)
		if ti != tj {
			return ti < tj
		}
		return sorted[i].RatingKey < sorted[j].RatingKey
	})
	sampled := false
	if budget := AISuggestBatchItems * AISuggestMaxBatches; len(sorted) > budget {
		stride := float64(len(sorted)) / float64(budget)
		picked := make([]Item, 0, budget)
		for i := 0; i < budget; i++ {
			picked = append(picked, sorted[int(float64(i)*stride)])
		}
		sorted = picked
		sampled = true
	}
	batches := [][]Item{}
	cur := []Item{}
	curChars := 0
	for _, it := range sorted {
		lineLen := len(aiItemLine(it)) + 1
		if len(cur) > 0 && (len(cur) >= AISuggestBatchItems || curChars+lineLen > AISuggestBatchChars) {
			batches = append(batches, cur)
			cur = []Item{}
			curChars = 0
		}
		cur = append(cur, it)
		curChars += lineLen
	}
	if len(cur) > 0 {
		batches = append(batches, cur)
	}
	return batches, sampled
}

// --- validation helpers -------------------------------------------------------

func keySetOf(keys []string) map[string]bool {
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		set[k] = true
	}
	return set
}

// jaccard is |A∩B| / |A∪B| over two key sets (0 when either is empty).
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func truncateRunesLocal(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// --- the run ------------------------------------------------------------------

// keptSuggestion is one proposal that has survived verification so
// far, with the facts its card will carry.
type keptSuggestion struct {
	title     string
	summary   string
	rationale string
	keys      []string // verified, in the model's order
	keySet    map[string]bool
	suggested int // distinct keys the model returned
}

// SuggestCollections runs one AI suggestion pass over one section of
// the scan. existing carries the section's current collections for
// duplicate detection; dismissedIDs is the shared dismissal store's
// list (AI ids are filtered, and their titles join the prompt's
// avoid list). Any batch error — including a malformed reply —
// aborts the run: the caller shows the error and NO cards.
func SuggestCollections(ctx context.Context, scan *Scan, sectionKey string, proposer CollectionProposer, model string, existing []ExistingCollection, dismissedIDs []string) (*AISuggestOutcome, error) {
	items := itemsInSection(scan, sectionKey)
	if len(items) == 0 {
		return nil, fmt.Errorf("that section has no scanned titles — run a library scan first")
	}
	sectionTitle, sectionType := sectionKey, ""
	for _, s := range scan.Sections {
		if s.Key == sectionKey {
			sectionTitle, sectionType = s.Title, s.Type
			break
		}
	}

	realKeys := make(map[string]bool, len(items))
	for _, it := range items {
		realKeys[it.RatingKey] = true
	}
	dismissed := keySetOf(dismissedIDs)
	dismissedTitles := DismissedAITitles(dismissedIDs, sectionKey)

	existingTitles := map[string]bool{}
	existingSets := []map[string]bool{}
	existingNames := make([]string, 0, len(existing))
	for _, ec := range existing {
		if t := strings.TrimSpace(ec.Title); t != "" {
			existingTitles[normalizeAITitle(t)] = true
			existingNames = append(existingNames, t)
		}
		if len(ec.Keys) > 0 {
			existingSets = append(existingSets, keySetOf(ec.Keys))
		}
	}
	sort.Strings(existingNames)

	batches, sampled := aiBatches(items)
	saw := 0
	for _, b := range batches {
		saw += len(b)
	}

	outcome := &AISuggestOutcome{TotalItems: len(items), SawItems: saw, Sampled: sampled}
	kept := []keptSuggestion{}

	for _, batch := range batches {
		var prompt strings.Builder
		fmt.Fprintf(&prompt, "Library section: %s (%s)\n", sectionTitle, sectionType)
		if len(existingNames) > 0 {
			fmt.Fprintf(&prompt, "Collections that already exist in this section (do not duplicate these): %s\n", strings.Join(existingNames, "; "))
		}
		if len(dismissedTitles) > 0 {
			fmt.Fprintf(&prompt, "Suggestions the owner already dismissed (do not re-suggest these or close variants): %s\n", strings.Join(dismissedTitles, "; "))
		}
		if len(kept) > 0 {
			names := make([]string, 0, len(kept))
			for _, k := range kept {
				names = append(names, k.title)
			}
			fmt.Fprintf(&prompt, "Already proposed in this run (do not repeat): %s\n", strings.Join(names, "; "))
		}
		prompt.WriteString("\nTitles in this batch (ratingKey|title|year|genres|content rating|score):\n")
		for _, it := range batch {
			prompt.WriteString(aiItemLine(it))
			prompt.WriteString("\n")
		}

		proposals, err := proposer.ProposeCollections(ctx, prompt.String())
		if err != nil {
			return nil, err // plain error, no partial state
		}
		outcome.Calls++

		for _, prop := range proposals {
			title := strings.TrimSpace(prop.Title)
			if title == "" {
				outcome.Suppressed++
				continue
			}
			if existingTitles[normalizeAITitle(title)] {
				outcome.Suppressed++
				continue
			}
			if dismissed[aiCardID(sectionKey, title)] {
				outcome.Suppressed++
				continue
			}
			// Verify: dedupe the model's keys, then intersect with
			// the section's real items. Hallucinations are dropped
			// and counted via suggested vs verified on the card.
			seenKeys := map[string]bool{}
			suggested := 0
			verified := []string{}
			for _, k := range prop.MemberRatingKeys {
				k = strings.TrimSpace(k)
				if k == "" || seenKeys[k] {
					continue
				}
				seenKeys[k] = true
				suggested++
				if realKeys[k] {
					verified = append(verified, k)
				}
			}
			if len(verified) < AISuggestMinMembers {
				outcome.Suppressed++
				continue
			}
			set := keySetOf(verified)
			// Near-duplicate of an existing collection?
			dup := false
			for _, es := range existingSets {
				if jaccard(set, es) >= aiSuggestOverlap {
					dup = true
					break
				}
			}
			if dup {
				outcome.Suppressed++
				continue
			}
			cand := keptSuggestion{
				title:     truncateRunesLocal(title, 120),
				summary:   truncateRunesLocal(strings.TrimSpace(prop.Summary), 200),
				rationale: truncateRunesLocal(strings.TrimSpace(prop.Rationale), 400),
				keys:      verified,
				keySet:    set,
				suggested: suggested,
			}
			// Near-duplicate of an already-kept suggestion? Keep the
			// larger set; a replaced one simply never renders.
			replaced := false
			skip := false
			for i, k := range kept {
				if jaccard(set, k.keySet) >= aiSuggestOverlap {
					if len(verified) > len(k.keys) {
						kept[i] = cand
						replaced = true
					} else {
						outcome.Suppressed++
						skip = true
					}
					break
				}
			}
			if skip {
				continue
			}
			if !replaced {
				kept = append(kept, cand)
			}
		}
	}

	sort.SliceStable(kept, func(i, j int) bool {
		if len(kept[i].keys) != len(kept[j].keys) {
			return len(kept[i].keys) > len(kept[j].keys)
		}
		return strings.ToLower(kept[i].title) < strings.ToLower(kept[j].title)
	})
	if len(kept) > AISuggestMaxCards {
		outcome.Suppressed += len(kept) - AISuggestMaxCards
		kept = kept[:AISuggestMaxCards]
	}
	outcome.Cards = []Card{}
	for _, k := range kept {
		reason := k.rationale
		if reason == "" {
			reason = "Suggested by AI from your library."
		}
		subtitle := k.summary
		if subtitle == "" {
			subtitle = fmt.Sprintf("%d titles", len(k.keys))
		}
		outcome.Cards = append(outcome.Cards, Card{
			ID:               aiCardID(sectionKey, k.title),
			Kind:             "ai",
			Title:            k.title,
			Subtitle:         subtitle,
			MemberRatingKeys: k.keys,
			Reason:           reason,
			Confidence:       "heuristic",
			AIModel:          model,
			AISuggested:      k.suggested,
			AIVerified:       len(k.keys),
		})
	}
	return outcome, nil
}
