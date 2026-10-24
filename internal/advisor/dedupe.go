package advisor

// dedupe.go — v1.25: near-duplicate suppression for the collection
// template bundle, using the SAME mechanism as the AI suggestions
// (ai_suggest.go): Jaccard >= 0.8 over member rating-key sets means
// "this collection already exists — do not suggest it again".
//
// Scope note: only the bundle kinds are deduped here. Pre-existing
// deterministic kinds (genre, decade, franchise, ...) never deduped
// against existing collections, and changing their shipped behavior
// is out of this bundle's scope — the suggestions handler applies
// DedupeCovered to BundleDedupeKinds only.

// BundleDedupeKinds is every kind in the v1.25 template bundle: the
// three already-shipped generators (director, actor, studio) plus the
// six new kinds.
var BundleDedupeKinds = map[string]bool{
	"director": true, "actor": true, "studio": true,
	"decade-genre": true, "unwatched-gems": true, "rewatchables": true,
	"quick-picks": true, "epics": true, "divergence": true,
}

// DedupeCovered drops bundle-kind cards whose member set is a
// near-duplicate (Jaccard >= aiSuggestOverlap) of any existing
// collection's member set. Non-bundle kinds pass through untouched,
// and a card with no members is left for GenerateAll's own filter.
func DedupeCovered(cards []Card, existing []ExistingCollection) []Card {
	if len(existing) == 0 {
		return cards
	}
	sets := make([]map[string]bool, 0, len(existing))
	for _, ec := range existing {
		if len(ec.Keys) == 0 {
			continue
		}
		sets = append(sets, keySetOf(ec.Keys))
	}
	if len(sets) == 0 {
		return cards
	}
	out := make([]Card, 0, len(cards))
	for _, c := range cards {
		if !BundleDedupeKinds[c.Kind] || len(c.MemberRatingKeys) == 0 {
			out = append(out, c)
			continue
		}
		set := keySetOf(c.MemberRatingKeys)
		covered := false
		for _, es := range sets {
			if jaccard(set, es) >= aiSuggestOverlap {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, c)
		}
	}
	return out
}
