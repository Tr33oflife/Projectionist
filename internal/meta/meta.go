// Package meta is the Go port of the Electron app's src/metadata/*.js
// clients (TMDb, Trakt, OMDb, AniList, Fanart.tv, and the BYOK AI hook).
//
// Port notes for the next AI worker:
//   - FAITHFUL port: same endpoints, pacing, caching namespaces/keys,
//     normalization, and error surfacing as the JS originals. Where a JS
//     behaviour cannot fit the Go contract (constructors that threw in JS
//     cannot return an error here), the deviation is flagged in a comment
//     on the constructor itself.
//   - AGENTS.md safety rules bind this package too: these clients only
//     READ third-party metadata; nothing here touches Plex, deletes
//     anything, or changes poster/collection state.
//   - VERIFICATION STATUS: UNVERIFIED live — exactly as the JS headers say.
//     Endpoint shapes come from the providers' public docs and the JS
//     port source; nothing here was exercised against a live API with a
//     real key. Tests use fixtures/httptest only.
package meta

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// Cache is the advisor store's cache, exactly as fixed by the port spec:
// the advisor store implements it. Get returns (data, true) on a fresh
// hit. The JS store returned {hit, data}; a cached JSON null is a real
// hit in the JS ("we already looked this up, it was a miss"), so callers
// in this package check for the literal bytes `null` where the JS did.
//
// Namespaces are the JS ones: 'tmdb', 'omdb', 'anilist', 'trakt',
// 'fanart'. (The AI client has no cache, as in JS.)
type Cache interface {
	Get(ns, key string) (json.RawMessage, bool) // bool = fresh hit
	Set(ns, key string, data json.RawMessage)
}

// Error is the single error type every client in this package returns.
// It mirrors the JS *Error classes (TmdbError, TraktError, OmdbError,
// AniListError, FanartError, AiError), which all carried {message,
// status, hint}. Error() returns the message verbatim — the JS scan/UI
// read err.message, and several messages embed the HTTP status and a
// snippet of the provider's response body, never swallowed.
type Error struct {
	Message string
	Status  int
	Hint    string
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// truncateRunes truncates s to at most n characters. The JS used
// String.prototype.slice, which counts UTF-16 code units; counting runes
// is the closest Go equivalent and identical for ASCII provider bodies.
func truncateRunes(s string, n int) string {
	if n < 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// bodySnippet returns the first n characters of a response body for
// embedding in an Error message (JS: body.slice(0, n)).
func bodySnippet(body []byte, n int) string {
	return truncateRunes(string(body), n)
}

// isJSONNull reports whether raw is the literal JSON null the JS cached
// for routine misses (OMDb ratings, AniList relations).
func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

// cacheGet is a nil-safe Cache.Get.
func cacheGet(c Cache, ns, key string) (json.RawMessage, bool) {
	if c == nil {
		return nil, false
	}
	return c.Get(ns, key)
}

// cacheSet is a nil-safe Cache.Set.
func cacheSet(c Cache, ns, key string, data json.RawMessage) {
	if c == nil {
		return
	}
	c.Set(ns, key, data)
}

// cacheSetValue marshals v and stores it. Used where the JS cached the
// NORMALIZED object (OMDb, AniList) rather than the raw API payload.
func cacheSetValue(c Cache, ns, key string, v any) {
	if c == nil {
		return
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	c.Set(ns, key, raw)
}

// cacheSetNull stores the literal JSON null (a cached routine miss).
func cacheSetNull(c Cache, ns, key string) {
	cacheSet(c, ns, key, json.RawMessage("null"))
}

// sleepCtx sleeps for d, aborting early if ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
