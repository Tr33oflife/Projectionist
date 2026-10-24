package advisor

// scan.go — port of src/advisor/scan.js: the library analysis pass.
//   1. Plex pull: every movie/show section, paginated.
//   2. TMDb join: tmdb:// guid first (NEVER fuzzy for the join); items
//      with no tmdb guid get ONE title+year search fallback, flagged
//      matchedByTitle. Per-item TMDb errors are recorded (TMDBError),
//      never thrown.
//   3. OMDb pass: only if imdbBadges is on AND an OMDb key exists.
//   4. Persisted via Store.SetScan. (The dub/sub pass is NOT here —
//      dubscan.go runs lazily per title.)
//
// Sibling contracts (verified against internal/plex + internal/meta
// as landed): GetSections / GetSectionItemsAll as used below;
// meta.NewTMDb / meta.NewOMDb constructors; TMDb ids are int64
// (meta.ParseGuids returns meta.IDs{TMDB int64, IMDB, TVDB string});
// search returns *meta.ListEntry; OMDb Rating.IMDBRating is the OMDb
// string (the Go Item stores it parsed as *float64 per the required
// API, where JS stored the string — flagged in the port report).

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"plexcollectionmanager/internal/meta"
	"plexcollectionmanager/internal/plex"
)

// Item is one enriched scan item: the Plex library item plus the TMDb
// join, the OMDb rating and the honest-match flags. JSON field names
// match scan.js exactly (tmdb, imdbRating, matchedByTitle, tmdbError).
type Item struct {
	plex.LibraryItem
	TMDB           *meta.Details `json:"tmdb"`
	IMDBRating     *float64      `json:"imdbRating"`
	MatchedByTitle bool          `json:"matchedByTitle"`
	TMDBError      string        `json:"tmdbError,omitempty"`
}

// epochMs is a millisecond Unix timestamp that tolerates BOTH on-disk
// shapes. The Go app writes a JSON number, but the Electron app wrote the
// scan's scannedAt as an ISO-8601 STRING (scan.js used
// new Date().toISOString()), and both apps share one data dir — the owner's
// Electron-era cache files carry the string form. A plain int64 fails to
// unmarshal them and the whole advisor cache read errors out (his
// 2026-10-20 report: "cannot unmarshal string into Go struct field
// Scan.scannedAt of type int64"). Unmarshal accepts a number, an RFC3339
// string, or a numeric string; Marshal always emits the number.
type epochMs int64

// UnmarshalJSON implements json.Unmarshaler for epochMs.
func (e *epochMs) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		*e = 0
		return nil
	}
	if s[0] != '"' {
		var n int64
		if err := json.Unmarshal(b, &n); err != nil {
			return err
		}
		*e = epochMs(n)
		return nil
	}
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return err
	}
	if t, err := time.Parse(time.RFC3339Nano, str); err == nil {
		*e = epochMs(t.UnixMilli())
		return nil
	}
	if n, err := strconv.ParseInt(str, 10, 64); err == nil {
		*e = epochMs(n)
		return nil
	}
	return fmt.Errorf("epochMs: cannot parse timestamp %q", str)
}

// MarshalJSON implements json.Marshaler — always the numeric form.
func (e epochMs) MarshalJSON() ([]byte, error) {
	return json.Marshal(int64(e))
}

// Scan is the scan snapshot shape scan.js returns and persists.
// scannedAt marshals as milliseconds since the epoch (see epochMs for why
// reads also accept the Electron app's ISO-string form).
type Scan struct {
	MachineID      string         `json:"machineId"`
	ScannedAt      epochMs        `json:"scannedAt"`
	Sections       []plex.Section `json:"sections"`
	Items          []Item         `json:"items"`
	ItemCount      int            `json:"itemCount"`
	TMDBKeyPresent bool           `json:"tmdbKeyPresent"`
	OMDbKeyPresent bool           `json:"omdbKeyPresent"`
}

// Progress is one scan progress event: { phase, done, total, note }.
type Progress struct {
	Phase string `json:"phase"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
	Note  string `json:"note"`
}

// ScanDeps carries everything ScanLibrary needs.
type ScanDeps struct {
	Plex     *plex.Client
	Server   *plex.Server
	Store    *Store
	Settings Settings
	TMDBKey  string
	OMDbKey  string
	// Optional pre-built clients. When set they take precedence over the
	// keys (the JS builds clients from the keys; a caller that already
	// built them — e.g. shared pacing state — may pass them instead).
	TMDB *meta.TMDbClient
	OMDb *meta.OMDbClient

	OnProgress func(Progress)
}

func reportProgress(fn func(Progress), p Progress) {
	if fn == nil {
		return
	}
	// A UI callback must never break the scan (JS wraps it in try/catch).
	defer func() { _ = recover() }()
	fn(p)
}

// ScanLibrary runs the full analysis pass and persists the result.
func ScanLibrary(ctx context.Context, d ScanDeps) (*Scan, error) {
	machineID := d.Server.Machine
	onProgress := d.OnProgress

	var tmdb *meta.TMDbClient
	if d.TMDB != nil {
		tmdb = d.TMDB
	} else if d.TMDBKey != "" {
		tmdb = meta.NewTMDb(d.TMDBKey, d.Store)
	}
	var omdb *meta.OMDbClient
	if d.OMDb != nil {
		omdb = d.OMDb
	} else {
		omdb = meta.NewOMDb(d.OMDbKey, d.Store)
	}
	doIMDb := d.Settings.IMDBBadges && omdb.Configured()

	// -- phase 1: Plex pull ---------------------------------------------------
	reportProgress(onProgress, Progress{Phase: "plex", Done: 0, Total: 0, Note: "Reading your library…"})
	sections, err := d.Plex.GetSections(ctx, d.Server)
	if err != nil {
		return nil, err
	}
	movieShow := make([]plex.Section, 0, len(sections))
	for _, s := range sections {
		if s.Type == "movie" || s.Type == "show" {
			movieShow = append(movieShow, s)
		}
	}
	items := []Item{}
	for _, section := range movieShow {
		typ := 1
		if section.Type == "show" {
			typ = 2
		}
		sectionKey := section.Key
		sectionTitle := section.Title
		sectionItems, err := d.Plex.GetSectionItemsAll(ctx, d.Server, sectionKey, &typ, func(done int) {
			reportProgress(onProgress, Progress{Phase: "plex", Done: done, Total: 0, Note: fmt.Sprintf("%s: %d titles…", sectionTitle, done)})
		})
		if err != nil {
			return nil, err
		}
		for _, li := range sectionItems {
			items = append(items, Item{LibraryItem: li})
		}
		reportProgress(onProgress, Progress{Phase: "plex", Done: len(items), Total: 0, Note: fmt.Sprintf("%s done — %d titles", sectionTitle, len(sectionItems))})
	}
	// Defensive: drop anything without a ratingKey.
	kept := items[:0]
	for _, it := range items {
		if it.RatingKey != "" {
			kept = append(kept, it)
		}
	}
	items = kept
	if items == nil {
		items = []Item{}
	}

	// -- phase 2: TMDb join -----------------------------------------------------
	joined, titleMatched, unmatched := 0, 0, 0
	for idx := range items {
		item := &items[idx]
		reportProgress(onProgress, Progress{Phase: "tmdb", Done: joined, Total: len(items), Note: fmt.Sprintf("Matching %s…", item.Title)})
		ids := meta.ParseGuids(item.Guids)
		item.TMDB = nil
		item.MatchedByTitle = false
		if tmdb != nil && ids.TMDB != 0 {
			var det *meta.Details
			var derr error
			if item.Type == "movie" {
				det, derr = tmdb.MovieDetails(ctx, ids.TMDB)
			} else {
				det, derr = tmdb.TVDetails(ctx, ids.TMDB)
			}
			if derr != nil {
				// One bad TMDb id must not kill the scan — mark and move on.
				msg := derr.Error()
				if len(msg) > 120 {
					msg = msg[:120]
				}
				item.TMDBError = msg
			} else {
				item.TMDB = det
			}
		} else if tmdb != nil && item.Title != "" {
			found, serr := searchDetails(ctx, tmdb, item)
			if serr != nil {
				unmatched++
			} else if found != nil {
				item.TMDB = found
				item.MatchedByTitle = true
				titleMatched++
			} else {
				unmatched++
			}
		} else {
			unmatched++
		}
		joined++
	}
	note := fmt.Sprintf("Matched %d of %d", len(items)-unmatched, len(items))
	if titleMatched > 0 {
		note += fmt.Sprintf(" (%d by title)", titleMatched)
	}
	reportProgress(onProgress, Progress{Phase: "tmdb", Done: len(items), Total: len(items), Note: note})

	// -- phase 3: OMDb pass (key-gated) -----------------------------------------
	if doIMDb {
		done := 0
		for idx := range items {
			item := &items[idx]
			ids := meta.ParseGuids(item.Guids)
			item.IMDBRating = nil
			if ids.IMDB != "" {
				if r, rerr := omdb.GetRating(ctx, ids.IMDB); rerr == nil && r != nil {
					if v, perr := strconv.ParseFloat(strings.TrimSpace(r.IMDBRating), 64); perr == nil {
						vv := v
						item.IMDBRating = &vv
					}
				}
				// A rating miss is not a scan failure (JS: catch -> null).
			}
			done++
			if done%25 == 0 {
				reportProgress(onProgress, Progress{Phase: "omdb", Done: done, Total: len(items), Note: fmt.Sprintf("IMDb ratings: %d/%d", done, len(items))})
			}
		}
	} else {
		for idx := range items {
			items[idx].IMDBRating = nil
		}
	}
	omdbNote := "IMDb skipped (no key)"
	if doIMDb {
		omdbNote = "IMDb ratings done"
	}
	reportProgress(onProgress, Progress{Phase: "omdb", Done: len(items), Total: len(items), Note: omdbNote})

	scan := &Scan{
		MachineID:      machineID,
		ScannedAt:      epochMs(time.Now().UnixMilli()),
		Sections:       movieShow,
		Items:          items,
		ItemCount:      len(items),
		TMDBKeyPresent: d.TMDBKey != "" || d.TMDB != nil,
		OMDbKeyPresent: omdb.Configured(),
	}
	if d.Store != nil {
		if err := d.Store.SetScan(machineID, scan); err != nil {
			return nil, err
		}
	}
	reportProgress(onProgress, Progress{Phase: "done", Done: len(items), Total: len(items), Note: fmt.Sprintf("Scan complete — %d titles", len(items))})
	return scan, nil
}

// searchDetails is the title+year fallback: search, then fetch details
// for the first hit. Returns (nil, nil) when the search finds nothing;
// any error (search OR details) is returned so the caller counts the
// item unmatched, exactly as the JS try/catch does.
func searchDetails(ctx context.Context, tmdb *meta.TMDbClient, item *Item) (*meta.Details, error) {
	if item.Type == "movie" {
		found, err := tmdb.SearchMovie(ctx, item.Title, item.Year)
		if err != nil || found == nil || found.TMDBID == "" {
			return nil, err
		}
		id, perr := strconv.ParseInt(found.TMDBID, 10, 64)
		if perr != nil {
			return nil, perr
		}
		return tmdb.MovieDetails(ctx, id)
	}
	found, err := tmdb.SearchTV(ctx, item.Title, item.Year)
	if err != nil || found == nil || found.TMDBID == "" {
		return nil, err
	}
	id, perr := strconv.ParseInt(found.TMDBID, 10, 64)
	if perr != nil {
		return nil, perr
	}
	return tmdb.TVDetails(ctx, id)
}
