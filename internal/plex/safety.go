package plex

import (
	"regexp"
	"strings"
)

// WritePolicy is an EXACT port of plex-safety.js writePolicy — the write
// guard every non-GET serverFetch call must pass before any network I/O.
//
// Permanent product boundary: no media/collection deletion, file operations,
// library administration, or arbitrary mutation endpoints. No settings bypass.
// FUTURE AI: do not widen these lists, add an endpoint, or add a bypass —
// see AGENTS.md. Deletion is DISABLED in this app; the only DELETE allowed
// anywhere is the membership-edge removal below.
//
// Returns the collection id for /library/metadata/{id} writes ("" when the
// route carries none): the caller (serverFetch) must GET that id first and
// refuse the write unless its type is 'collection'.
var (
	metadataPathRe   = regexp.MustCompile(`^/library/metadata/(\d+)(.*)$`)
	collectionPathRe = regexp.MustCompile(`^/library/collections/(\d+)(.*)$`)
	moveSuffixRe     = regexp.MustCompile(`^/items/\d+/move$`)
	itemsSuffixRe    = regexp.MustCompile(`^/items/\d+$`)
	hubsManageRe     = regexp.MustCompile(`^/hubs/sections/\d+/manage(?:/custom\.collection\.\d+\.\d+)?$`)
)

func WritePolicy(method, endpoint string, params map[string]string) (collectionID string, err error) {
	verb := strings.ToUpper(method)
	if verb == "GET" {
		return "", nil
	}
	// JS also rejects a non-string endpoint here; Go's type system already
	// guarantees a string, so only the '?' / '%' checks remain.
	if strings.Contains(endpoint, "?") || strings.Contains(endpoint, "%") {
		return "", &Error{Message: "Safety guard: malformed write path"}
	}
	var allowed []string
	if verb == "POST" && endpoint == "/library/collections" {
		allowed = []string{"type", "title", "smart", "sectionId", "uri"}
	} else if verb == "POST" && postersPathRe.MatchString(endpoint) {
		allowed = []string{"url"}
	} else if verb == "POST" && artsPathRe.MatchString(endpoint) {
		// Backdrop/art upload (v1.13): multipart bytes only, no query
		// params at all. The caller (UploadArtBytes) enforces the same
		// verified-backup gate as poster writes before networking.
		allowed = []string{}
	} else if m := collectionPathRe.FindStringSubmatch(endpoint); m != nil {
		// The item-move (reorder) endpoint only exists under
		// /library/collections/{id}/items/{item}/move — verified against a
		// real PMS 1.43.5 (2026-10-20); the /library/metadata/ twin 404s.
		// This branch allows ONLY that shape; the caller still type-proofs
		// the id as a collection before the request is sent.
		collectionID = m[1]
		suffix := m[2]
		if verb == "PUT" && moveSuffixRe.MatchString(suffix) {
			allowed = []string{"after"}
		} else {
			return "", &Error{Message: "Safety guard: deletion or unsupported Plex write blocked"}
		}
	} else if m := metadataPathRe.FindStringSubmatch(endpoint); m != nil {
		collectionID = m[1]
		suffix := m[2]
		switch {
		case verb == "PUT" && suffix == "":
			// v1.13 widening, scoped and deliberate: the field-edit
			// forms (titleSort.value/.locked, contentRating.value/
			// .locked, label.value/.locked) are the forms PMS 1.43
			// actually honors for those fields (plain titleSort is a
			// silent no-op there — see UpdateCollectionMeta). This
			// branch only ever fires after serverFetch has proven the
			// target id is a collection, so the new params can land
			// on collections only — never on media items.
			allowed = []string{"title", "titleSort", "summary",
				"titleSort.value", "titleSort.locked",
				"contentRating.value", "contentRating.locked",
				"label.value", "label.locked"}
		case verb == "PUT" && suffix == "/prefs":
			allowed = []string{"collectionSort"}
		case verb == "PUT" && suffix == "/items":
			allowed = []string{"uri"}
		case verb == "PUT" && moveSuffixRe.MatchString(suffix):
			allowed = []string{"after"}
		// This endpoint removes only a collection membership edge. The parent
		// must be proven to be a collection before the request is sent.
		case verb == "DELETE" && itemsSuffixRe.MatchString(suffix):
			allowed = []string{}
		default:
			return "", &Error{Message: "Safety guard: deletion or unsupported Plex write blocked"}
		}
	} else if (verb == "POST" || verb == "PUT") && hubsManageRe.MatchString(endpoint) {
		allowed = []string{"metadataItemId", "promotedToOwnHome", "promotedToSharedHome", "promotedToRecommended"}
	} else {
		return "", &Error{Message: "Safety guard: unsupported Plex write blocked"}
	}
	for k := range params {
		found := false
		for _, a := range allowed {
			if k == a {
				found = true
				break
			}
		}
		if !found {
			return "", &Error{Message: "Safety guard: unsupported write parameter blocked"}
		}
	}
	return collectionID, nil
}

// TagList is the port of the JS PlexClient.tagList static: it parses Plex
// Tag[] values defensively. Plex returns tags as [{tag:'x'}], but a bare
// string or a differently-shaped object must degrade to being skipped —
// never crash the scan.
// UNVERIFIED live: exact Tag[] shape on /library/sections/{key}/all.
func TagList(raw []any) []string {
	return tagListValue(raw)
}

// tagListValue is TagList over an arbitrary decoded JSON value (the JS
// accepts a non-array single value and wraps it).
func tagListValue(v any) []string {
	out := []string{}
	if !jsTruthy(v) {
		return out
	}
	arr, ok := v.([]any)
	if !ok {
		arr = []any{v}
	}
	for _, t := range arr {
		var x any
		if m, isObj := t.(map[string]any); isObj {
			x = m["tag"]
		} else {
			x = t
		}
		s, isStr := x.(string)
		if !isStr {
			continue
		}
		if strings.TrimSpace(s) == "" {
			continue
		}
		out = append(out, strings.TrimSpace(s))
	}
	return out
}
