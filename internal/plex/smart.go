package plex

// smart.go — smart collection rule display (v1.14).
//
// A smart collection's record carries its rule as a filter URI in
// the `content` field:
//
//	server://<machine>/com.plexapp.plugins.library/library/sections/1/all?type=1&decade=2000
//
// PMS 1.43 exposes that field on both the section collection listing
// and the single-record read (verified live on the sandbox PMS
// 1.43.5, 2026-10-23 — the created record returned `content`
// verbatim, and its children resolved from the filter). The app
// never has to guess: it shows the rule the SERVER stored,
// translated to plain words by SmartRuleSummary. Anything the
// translator does not recognize is passed through in a readable raw
// form rather than dropped — the summary must never understate the
// rule.

import (
	"net/url"
	"strconv"
	"strings"
)

// smartFieldLabels names the filter fields the Builder can produce.
// Fields outside this map still summarize (generic fallback below).
var smartFieldLabels = map[string]string{
	"genre":          "Genre",
	"decade":         "Decade",
	"year":           "Year",
	"contentRating":  "Content rating",
	"studio":         "Studio",
	"actor":          "Actor",
	"director":       "Director",
	"audienceRating": "Audience rating",
	"rating":         "Critic rating",
	"duration":       "Runtime",
	"addedAt":        "Added",
	"unwatched":      "Unwatched",
}

// SmartRuleSummary renders a smart collection's stored content URI
// as a plain-words rule summary, e.g.
// "Decade is 2000 · Unwatched only". Returns "" when the content is
// empty or carries no filter parameters (nothing honest to show).
func SmartRuleSummary(content string) string {
	if content == "" {
		return ""
	}
	q := content
	if i := strings.Index(q, "?"); i >= 0 {
		q = q[i+1:]
	} else {
		return ""
	}
	type pair struct{ key, val string }
	pairs := []pair{}
	for _, part := range strings.Split(q, "&") {
		if part == "" {
			continue
		}
		if dec, err := url.QueryUnescape(part); err == nil {
			part = dec
		}
		// The key ends at the first operator character: filter keys
		// are alphanumeric, operators are runs of '>', '<', '=' —
		// "year>=2001" is key "year", op ">=", value "2001" (a naive
		// split at the first '=' would eat the operator).
		i := strings.IndexAny(part, "><=")
		if i < 0 {
			continue
		}
		j := i
		for j < len(part) && strings.ContainsRune("><=", rune(part[j])) {
			j++
		}
		pairs = append(pairs, pair{part[:i] + part[i:j], part[j:]})
	}
	// Group consecutive repeats of the same key (Plex repeats a
	// parameter for OR-within-a-field: genre=Action&genre=Drama).
	phrases := []string{}
	for i := 0; i < len(pairs); {
		j := i + 1
		vals := []string{pairs[i].val}
		for j < len(pairs) && pairs[j].key == pairs[i].key {
			vals = append(vals, pairs[j].val)
			j++
		}
		if p := smartPhrase(pairs[i].key, vals); p != "" {
			phrases = append(phrases, p)
		}
		i = j
	}
	return strings.Join(phrases, " · ")
}

// smartPhrase renders one filter parameter (key carries any operator
// suffix, e.g. "year>=" or "duration<<=") with its value(s).
func smartPhrase(key string, vals []string) string {
	field, op := key, ""
	for _, suffix := range []string{">>=", "<<=", ">=", "<=", ">>", "<<", "="} {
		if strings.HasSuffix(key, suffix) {
			field, op = key[:len(key)-len(suffix)], suffix
			break
		}
	}
	if op == "=" {
		op = "" // bare equality
	}
	joined := strings.Join(vals, ", ")
	switch field {
	case "type":
		// The item-type selector (1=movie, 2=show) is not a rule.
		return ""
	case "unwatched":
		if len(vals) == 1 && vals[0] == "1" {
			return "Unwatched only"
		}
	case "addedAt":
		// Relative window: addedAt>=-60d.
		if len(vals) == 1 && strings.HasPrefix(vals[0], "-") && strings.HasSuffix(vals[0], "d") {
			if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(vals[0], "-"), "d")); err == nil {
				if n == 1 {
					return "Added in the last day"
				}
				return "Added in the last " + strconv.Itoa(n) + " days"
			}
		}
	case "duration":
		// Plex durations are milliseconds; the Builder writes
		// duration<<={minutes*60000}.
		if len(vals) == 1 {
			if ms, err := strconv.ParseInt(vals[0], 10, 64); err == nil && ms > 0 {
				mins := ms / 60000
				if ms%60000 == 0 {
					return smartOpPhrase("Runtime", op, strconv.FormatInt(mins, 10)+" min")
				}
				return smartOpPhrase("Runtime", op, "~"+strconv.FormatInt(mins, 10)+" min")
			}
		}
	}
	label, known := smartFieldLabels[field]
	if !known {
		label = field
	}
	if field == "year" && op == ">>=" {
		return "Year after " + joined // strict: the year itself is excluded
	}
	return smartOpPhrase(label, op, joined)
}

// smartOpPhrase renders "Label <op-glyph> value"; a bare equality
// reads "Label is value". Plex's doubled operators are strict
// (probed on PMS 1.43.5: year>>=2002 excludes 2002 itself), so they
// render as plain > / < rather than ≥ / ≤.
func smartOpPhrase(label, op, value string) string {
	switch op {
	case "":
		return label + " is " + value
	case ">=":
		return label + " ≥ " + value
	case "<=":
		return label + " ≤ " + value
	case ">>=":
		return label + " > " + value
	case "<<=":
		return label + " ≤ " + value
	case ">>":
		return label + " > " + value
	case "<<":
		return label + " < " + value
	}
	return label + " " + op + " " + value
}
