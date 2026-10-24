package api

// handlers_import.go — list import preview (v1.14).
//
// POST /api/import/previewList fetches a TMDb or Trakt list and
// matches every entry against one library section. It performs NO
// Plex writes: the response is the import's dry-run — matched
// entries (in list order) become the Builder draft's members, and
// unmatched entries carry the reason they cannot import (not in the
// library / ambiguous / wrong kind for the target library /
// duplicate). The Builder's existing dry-run → confirm →
// createCollection flow is the only write path, exactly like every
// other Builder seed.
//
// Matching discipline (in order, per entry):
//  1. The entry's kind must fit the target library (a show cannot
//     import into a movie library).
//  2. Exact external id: the item's Guid elements carry tmdb:// and
//     imdb:// ids; an id that resolves to exactly ONE library item
//     wins outright.
//  3. Normalized title + year (lowercase, punctuation collapsed,
//     one leading article dropped). Exactly one candidate matches;
//     more than one is reported ambiguous, never guessed.
// An entry WITH a year never falls back to a year-blind title match
// (a same-title different-year item is a different work). An entry
// WITHOUT a year may title-match only when the title is unique in
// the library.

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"plexcollectionmanager/internal/meta"
	"plexcollectionmanager/internal/plex"
)

// importEntry is one list entry, normalized across both sources.
type importEntry struct {
	Title     string
	Year      *int
	MediaType string // as the source names it: "movie" | "tv" | "show" | other
	TMDBID    string
	IMDBID    string
}

// fetchedList is a fetched list in source-neutral form.
type fetchedList struct {
	Title     string
	ItemCount int // the source's own count; may exceed len(Entries)
	Entries   []importEntry
	// AuthUsed is true when the list was read with the connected
	// Trakt account's token rather than the public client-ID read.
	AuthUsed bool
}

// importItem is the slim Plex item an entry matched — the exact
// shape the Builder keeps in draft.members.
type importItem struct {
	RatingKey string `json:"ratingKey"`
	Title     string `json:"title"`
	Year      *int   `json:"year"`
	Thumb     string `json:"thumb"`
	Type      string `json:"type"`
}

// importEntryResult is one list entry's verdict.
type importEntryResult struct {
	Title     string      `json:"title"`
	Year      *int        `json:"year"`
	MediaType string      `json:"mediaType"`
	TMDBID    string      `json:"tmdbId,omitempty"`
	IMDBID    string      `json:"imdbId,omitempty"`
	Status    string      `json:"status"` // matched | unmatched | duplicate
	Via       string      `json:"via,omitempty"`
	Reason    string      `json:"reason,omitempty"`
	Item      *importItem `json:"item"`
}

func slimImportItem(it plex.LibraryItem) *importItem {
	return &importItem{RatingKey: it.RatingKey, Title: it.Title, Year: it.Year, Thumb: it.Thumb, Type: it.Type}
}

// normalizeEntryType folds source type names into the Plex section
// vocabulary: "movie" | "show" | anything else (kept verbatim so the
// mismatch reason can name it).
func normalizeEntryType(t string) string {
	switch t {
	case "movie":
		return "movie"
	case "tv", "show":
		return "show"
	}
	return t
}

var nonAlnumRun = regexp.MustCompile(`[^a-z0-9]+`)

// normalizeImportTitle is the conservative title key for the
// title+year fallback: lowercase, every non-alphanumeric run
// collapsed to one space, trimmed, ONE leading article dropped.
func normalizeImportTitle(t string) string {
	s := nonAlnumRun.ReplaceAllString(strings.ToLower(t), " ")
	s = strings.TrimSpace(s)
	for _, art := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(s, art) {
			s = s[len(art):]
			break
		}
	}
	return s
}

func yearKey(y *int) string {
	if y == nil {
		return ""
	}
	return strconv.Itoa(*y)
}

// matchImportEntries runs the matching discipline over one fetched
// list against the target section's items. Pure: no I/O, fully
// table-testable. Returns one result per list entry, in list order.
func matchImportEntries(entries []importEntry, items []plex.LibraryItem, sectionType string) []importEntryResult {
	byTMDB := map[string][]plex.LibraryItem{}
	byIMDB := map[string][]plex.LibraryItem{}
	byTitleYear := map[string][]plex.LibraryItem{}
	byTitle := map[string][]plex.LibraryItem{}
	for _, it := range items {
		ids := meta.ParseGuids(it.Guids)
		if ids.TMDB != 0 {
			k := strconv.FormatInt(ids.TMDB, 10)
			byTMDB[k] = append(byTMDB[k], it)
		}
		if ids.IMDB != "" {
			byIMDB[ids.IMDB] = append(byIMDB[ids.IMDB], it)
		}
		nt := normalizeImportTitle(it.Title)
		byTitleYear[nt+"|"+yearKey(it.Year)] = append(byTitleYear[nt+"|"+yearKey(it.Year)], it)
		byTitle[nt] = append(byTitle[nt], it)
	}

	results := make([]importEntryResult, 0, len(entries))
	matchedOnce := map[string]bool{}
	for _, e := range entries {
		res := importEntryResult{Title: e.Title, Year: e.Year, MediaType: e.MediaType, TMDBID: e.TMDBID, IMDBID: e.IMDBID, Status: "unmatched"}
		// (1) kind must fit the target library
		if nt := normalizeEntryType(e.MediaType); nt != sectionType {
			res.Reason = "list entry is a " + entryKindName(e.MediaType) + "; target library holds " + sectionType + "s"
			results = append(results, res)
			continue
		}
		// duplicate of an entry already matched from this list
		dupKey := "t:" + e.TMDBID
		if e.TMDBID == "" {
			dupKey = "n:" + normalizeImportTitle(e.Title) + "|" + yearKey(e.Year)
		}
		if matchedOnce[dupKey] {
			res.Status = "duplicate"
			res.Reason = "same title appears earlier in the list — imported once"
			results = append(results, res)
			continue
		}
		// (2) exact external ids
		var cands []plex.LibraryItem
		via := ""
		if e.TMDBID != "" {
			if c := byTMDB[e.TMDBID]; len(c) > 0 {
				cands, via = c, "tmdb"
			}
		}
		if cands == nil && e.IMDBID != "" {
			if c := byIMDB[e.IMDBID]; len(c) > 0 {
				cands, via = c, "imdb"
			}
		}
		if cands != nil {
			if len(cands) == 1 {
				res.Status, res.Via, res.Item = "matched", via, slimImportItem(cands[0])
				matchedOnce[dupKey] = true
			} else {
				res.Reason = "ambiguous — " + strconv.Itoa(len(cands)) + " library items carry this " + via + " id"
			}
			results = append(results, res)
			continue
		}
		// (3) normalized title (+year)
		nt := normalizeImportTitle(e.Title)
		if e.Year != nil {
			fc := byTitleYear[nt+"|"+yearKey(e.Year)]
			switch {
			case len(fc) == 1:
				res.Status, res.Via, res.Item = "matched", "title+year", slimImportItem(fc[0])
				matchedOnce[dupKey] = true
			case len(fc) > 1:
				res.Reason = "ambiguous — " + strconv.Itoa(len(fc)) + " library items match this title and year"
			default:
				res.Reason = "not in library"
			}
		} else {
			fc := byTitle[nt]
			switch {
			case len(fc) == 1:
				res.Status, res.Via, res.Item = "matched", "title", slimImportItem(fc[0])
				matchedOnce[dupKey] = true
			case len(fc) > 1:
				res.Reason = "ambiguous — " + strconv.Itoa(len(fc)) + " library items share this title and the list entry has no year to separate them"
			default:
				res.Reason = "not in library"
			}
		}
		results = append(results, res)
	}
	return results
}

func entryKindName(mediaType string) string {
	switch normalizeEntryType(mediaType) {
	case "movie":
		return "movie"
	case "show":
		return "show"
	}
	if mediaType == "" {
		return "entry of unknown kind"
	}
	return mediaType
}

// fetchTMDbListReal reads a TMDb user list with the stored key.
func (s *Server) fetchTMDbListReal(ctx context.Context, ref string) (fetchedList, error) {
	mc := s.buildMetaClients(s.getSecrets())
	if mc.tmdb == nil {
		return fetchedList{}, &plex.Error{Message: "No TMDb API key configured", Hint: "Paste your TMDb key in Settings → API keys to import TMDb lists."}
	}
	id, ok := meta.ParseTMDbListID(ref)
	if !ok {
		return fetchedList{}, &plex.Error{Message: "That doesn't look like a TMDb list — paste the list id or its themoviedb.org URL."}
	}
	list, err := mc.tmdb.UserList(ctx, id)
	if err != nil {
		return fetchedList{}, err
	}
	out := fetchedList{Title: list.Name, ItemCount: list.ItemCount, Entries: []importEntry{}}
	for _, e := range list.Entries {
		ie := importEntry{Title: e.Title, MediaType: e.MediaType, TMDBID: e.TMDBID}
		if len(e.ReleaseDate) >= 4 {
			if y, err := strconv.Atoi(e.ReleaseDate[:4]); err == nil {
				ie.Year = &y
			}
		}
		out.Entries = append(out.Entries, ie)
	}
	return out, nil
}

// fetchTraktListReal reads a Trakt user list. Public lists are
// served with the client ID alone; only when that read fails AND a
// Trakt account is connected does it retry with the account token
// (private lists), and AuthUsed reports that honestly.
func (s *Server) fetchTraktListReal(ctx context.Context, ref string) (fetchedList, error) {
	sec := s.getSecrets()
	mc := s.buildMetaClients(sec)
	if mc.trakt == nil {
		return fetchedList{}, &plex.Error{Message: "No Trakt client ID configured", Hint: "Add your Trakt client ID in Settings → API keys to import Trakt lists."}
	}
	user, listRef, ok := meta.ParseTraktListRef(ref)
	if !ok {
		return fetchedList{}, &plex.Error{Message: "That doesn't look like a Trakt list — paste the list URL (trakt.tv/users/…/lists/…) or user/list."}
	}
	list, err := mc.trakt.UserList(ctx, user, listRef, "")
	authUsed := false
	if err != nil {
		if token := s.getTraktToken(ctx, sec); token != "" {
			if list2, err2 := mc.trakt.UserList(ctx, user, listRef, token); err2 == nil {
				list, err, authUsed = list2, nil, true
			}
		}
	}
	if err != nil {
		return fetchedList{}, err
	}
	out := fetchedList{Title: list.Name, ItemCount: list.ItemCount, AuthUsed: authUsed, Entries: []importEntry{}}
	for _, e := range list.Entries {
		ie := importEntry{Title: e.Title, MediaType: e.MediaType, TMDBID: e.TMDBID, IMDBID: e.IMDBID}
		if e.Year != nil {
			y := int(*e.Year)
			ie.Year = &y
		}
		out.Entries = append(out.Entries, ie)
	}
	return out, nil
}

func (s *Server) handleImportPreviewList(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Source     string `json:"source"`
		ListRef    string `json:"listRef"`
		SectionKey string `json:"sectionKey"`
		Type       *int   `json:"type"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	if body.Type == nil || (*body.Type != 1 && *body.Type != 2) {
		failErr(w, &plex.Error{Message: "Import needs a target type: 1 (movies) or 2 (shows)."})
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	var list fetchedList
	switch body.Source {
	case "tmdb":
		fetch := s.fetchTMDbList
		if fetch == nil {
			fetch = s.fetchTMDbListReal
		}
		list, err = fetch(r.Context(), body.ListRef)
	case "trakt":
		fetch := s.fetchTraktList
		if fetch == nil {
			fetch = s.fetchTraktListReal
		}
		list, err = fetch(r.Context(), body.ListRef)
	default:
		failErr(w, &plex.Error{Message: "Unknown list source: " + body.Source + " (want tmdb or trakt)"})
		return
	}
	if err != nil {
		failErr(w, err)
		return
	}
	items, err := s.plexClient.GetSectionItemsAll(r.Context(), server, body.SectionKey, body.Type, nil)
	if err != nil {
		failErr(w, err)
		return
	}
	sectionType := "movie"
	if *body.Type == 2 {
		sectionType = "show"
	}
	results := matchImportEntries(list.Entries, items, sectionType)
	members := []*importItem{}
	matched, unmatched, duplicates := 0, 0, 0
	for _, res := range results {
		switch res.Status {
		case "matched":
			matched++
			members = append(members, res.Item)
		case "duplicate":
			duplicates++
		default:
			unmatched++
		}
	}
	// Title collision follows the Builder's existing behavior: the
	// dry-run refuses a title the library already holds, and Create
	// explains it. Surface the collision here so the UI can say so
	// before the owner even computes the diff.
	titleCollision := false
	if list.Title != "" {
		if cols, cerr := s.plexClient.GetCollections(r.Context(), server, body.SectionKey); cerr == nil {
			for _, c := range cols {
				if strings.EqualFold(c.Title, list.Title) {
					titleCollision = true
					break
				}
			}
		}
	}
	ok(w, envelope{
		"source":         body.Source,
		"listTitle":      list.Title,
		"authUsed":       list.AuthUsed,
		"itemCount":      list.ItemCount,
		"totalEntries":   len(list.Entries),
		"matchedCount":   matched,
		"unmatchedCount": unmatched,
		"duplicateCount": duplicates,
		"titleCollision": titleCollision,
		"entries":        results,
		"members":        members,
	})
}
