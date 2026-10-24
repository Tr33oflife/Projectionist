package plex

// health.go — collection health flags (v1.13 collections manager).
//
// Flags are computed from REAL reads only: the member count, poster /
// backdrop presence, and summary presence all come off the collection
// record GetCollections already fetched, and DUPLICATE TITLE compares
// titles within the same library listing. Nothing here guesses: a
// collection whose facts are all present earns no flags, and every
// flag names the fact that produced it (the UI shows the counts next
// to the flags, so the basis is visible, not implied).

// Health flag names — the wire contract with the manager UI.
const (
	HealthEmpty         = "EMPTY"
	HealthNoPoster      = "NO POSTER"
	HealthNoBackdrop    = "NO BACKDROP"
	HealthNoSummary     = "NO SUMMARY"
	HealthDuplicateName = "DUPLICATE TITLE"
	// HealthNoCurrentMatches replaces EMPTY for a SMART collection
	// with zero current matches (v1.14): its membership is computed
	// from its rule, so "empty right now" is a different fact from a
	// manual collection with no members — it can fill itself as the
	// library changes, and the health view must say so.
	HealthNoCurrentMatches = "NO CURRENT MATCHES"
)

// CollectionFlags returns the health flags for one collection given
// the title histogram of its whole library (title → how many
// collections carry it). Order is stable: EMPTY / NO CURRENT
// MATCHES, NO POSTER, NO BACKDROP, NO SUMMARY, DUPLICATE TITLE.
//
// A SMART collection with 0 current matches is flagged NO CURRENT
// MATCHES, never EMPTY (v1.14): the membership is rule-computed and
// can change on its own as the library changes — "empty" would
// misstate the fact and invite a pointless manual fill.
func CollectionFlags(c Collection, titleCounts map[string]int) []string {
	flags := []string{}
	if c.ChildCount == 0 {
		if c.Smart {
			flags = append(flags, HealthNoCurrentMatches)
		} else {
			flags = append(flags, HealthEmpty)
		}
	}
	if !c.HasPoster {
		flags = append(flags, HealthNoPoster)
	}
	if !c.HasBackdrop {
		flags = append(flags, HealthNoBackdrop)
	}
	if c.Summary == "" {
		flags = append(flags, HealthNoSummary)
	}
	if titleCounts[c.Title] > 1 {
		flags = append(flags, HealthDuplicateName)
	}
	return flags
}

// TitleCounts histograms collection titles for duplicate detection.
func TitleCounts(cols []Collection) map[string]int {
	counts := map[string]int{}
	for _, c := range cols {
		counts[c.Title]++
	}
	return counts
}

// HealthFlagCounts totals each flag across a library listing — the
// numbers the health view's summary line and filter chips show.
func HealthFlagCounts(cols []Collection) map[string]int {
	counts := TitleCounts(cols)
	out := map[string]int{}
	for _, c := range cols {
		for _, f := range CollectionFlags(c, counts) {
			out[f]++
		}
	}
	return out
}
