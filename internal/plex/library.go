package plex

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
)

/* -- auth: Plex PIN OAuth ---------------------------------------------------
 * Flow (same as Media Hub v3.5.9):
 *   1. PinStart()  -> POST /api/v2/pins?strong=true  gives { pinId, code }
 *   2. Open AuthURL in the system browser; user approves THERE.
 *   3. PinPoll(pinId) -> GET /api/v2/pins/{id}; returns authToken once the
 *      user approves, "" while still pending.
 *   4. GetUser(token) -> GET /api/v2/user confirms identity.
 * The app owns the whole flow; the frontend only sees { pinId, authUrl } and
 * later the user — never the token.
 * ------------------------------------------------------------------------- */

// PinStart starts PIN OAuth against plex.tv.
// FUTURE AI: ExpiresIn is the fixed 600 the JS reports, not a server value.
func (c *Client) PinStart(ctx context.Context) (*PinResult, error) {
	body, err := c.plexTV(ctx, "/api/v2/pins?strong=true", "POST", "", 15000)
	if err != nil {
		return nil, err
	}
	pin := decodeObject(body)
	idVal, hasID := pin["id"]
	code := stringField(pin, "code")
	if !hasID || !jsTruthy(idVal) || code == "" {
		return nil, &Error{Message: "Plex did not issue a login PIN", Endpoint: "/api/v2/pins"}
	}
	pinID, _ := strconv.ParseInt(jsString(idVal), 10, 64)
	if f, ok := jsNumber(idVal); ok {
		pinID = int64(f)
	}
	authURL := "https://app.plex.tv/auth#?" +
		"clientID=" + encodeURIComponent(c.clientID) +
		"&code=" + encodeURIComponent(code) +
		"&context%5Bdevice%5D%5Bproduct%5D=" + encodeURIComponent(c.product)
	return &PinResult{PinID: pinID, AuthURL: authURL, ExpiresIn: 600}, nil
}

// PinPoll returns the authToken string once approved, or "" while pending
// (the JS returns null; the Go contract represents pending as "").
func (c *Client) PinPoll(ctx context.Context, pinID int64) (string, error) {
	body, err := c.plexTV(ctx, "/api/v2/pins/"+encodeURIComponent(strconv.FormatInt(pinID, 10)), "GET", "", 15000)
	if err != nil {
		return "", err
	}
	pin := decodeObject(body)
	return stringField(pin, "authToken"), nil
}

// GetUser confirms identity for a plex.tv token.
// The JS maps the response to { id, name, thumb } (name falls back
// username -> title -> 'Plex user'); this Go signature carries that mapped
// shape marshalled as json.RawMessage rather than the raw plex.tv payload.
func (c *Client) GetUser(ctx context.Context, token string) (json.RawMessage, error) {
	body, err := c.plexTV(ctx, "/api/v2/user", "GET", token, 15000)
	if err != nil {
		return nil, err
	}
	user := decodeObject(body)
	idVal, hasID := user["id"]
	if !hasID || !jsTruthy(idVal) {
		return nil, &Error{Message: "Could not verify Plex identity", Endpoint: "/api/v2/user"}
	}
	name := stringField(user, "username")
	if name == "" {
		name = stringField(user, "title")
	}
	if name == "" {
		name = "Plex user"
	}
	return rawMessage(userShape{ID: jsString(idVal), Name: name, Thumb: stringField(user, "thumb")})
}

/* -- servers --------------------------------------------------------------- */

// GetServers discovers the user's servers via plex.tv /api/v2/resources.
// Returns servers only (provides includes 'server'), each with a per-server
// accessToken and ranked connections + BestConnection attached.
func (c *Client) GetServers(ctx context.Context, userToken string) ([]*Server, error) {
	body, err := c.plexTV(ctx, "/api/v2/resources?includeHttps=1&includeRelay=1", "GET", userToken, 15000)
	if err != nil {
		return nil, err
	}
	var rawArr []any
	_ = json.Unmarshal(body, &rawArr)
	servers := []*Server{}
	for _, el := range rawArr {
		r, ok := el.(map[string]any)
		if !ok {
			continue
		}
		provides := strings.Split(stringField(r, "provides"), ",")
		isServer := false
		for _, p := range provides {
			if p == "server" {
				isServer = true
			}
		}
		if !isServer || stringField(r, "accessToken") == "" || stringField(r, "clientIdentifier") == "" {
			continue
		}
		name := stringField(r, "name")
		if name == "" {
			name = "Plex server"
		}
		s := &Server{
			Name:    name,
			Machine: jsString(r["clientIdentifier"]),
			Token:   stringField(r, "accessToken"),
			Owned:   jsTruthy(r["owned"]),
		}
		for _, cel := range toRawSlice(r["connections"]) {
			cm, ok := cel.(map[string]any)
			if !ok {
				continue
			}
			conn := Connection{
				URI:   stringField(cm, "uri"),
				Local: jsTruthy(cm["local"]),
				Relay: jsTruthy(cm["relay"]),
			}
			if !uriSchemeRe.MatchString(conn.URI) {
				continue
			}
			s.Connections = append(s.Connections, conn)
		}
		if len(s.Connections) == 0 {
			continue
		}
		// Rank connections (local -> remote -> relay) and attach
		// bestConnection for the UI quality badge + the art proxy.
		enrichServer(s)
		servers = append(servers, s)
	}
	return servers, nil
}

// toRawSlice returns v as []any (nil when it isn't an array).
func toRawSlice(v any) []any {
	arr, _ := v.([]any)
	return arr
}

/* -- library reads (VERIFIED patterns) ------------------------------------- */

// GetSections lists library sections (Movies / Shows / ...). Only
// numeric-keyed movie+show sections are returned.
func (c *Client) GetSections(ctx context.Context, s *Server) ([]Section, error) {
	data, err := c.serverFetch(ctx, s, "/library/sections", fetchOptions{method: "GET"})
	if err != nil {
		return nil, err
	}
	cont := parseContainer(data)
	out := []Section{}
	for _, d := range cont.directory {
		typ := stringField(d, "type")
		if typ != "movie" && typ != "show" {
			continue
		}
		key := jsString(d["key"])
		if !numericRe.MatchString(key) {
			continue
		}
		out = append(out, Section{Key: key, Title: stringField(d, "title"), Type: typ, UUID: stringField(d, "uuid")})
	}
	return out, nil
}

// GetCollections lists all collections in one section. `smart` is a real
// Plex field (true for rule-based collections); ChildCount is the member
// count. Visibility (promotedTo*) is joined from the section's manage hubs.
func (c *Client) GetCollections(ctx context.Context, s *Server, sectionKey string) ([]Collection, error) {
	entries, err := c.pagedMetadata(ctx, s, "/library/sections/"+encodeURIComponent(sectionKey)+"/collections", 500)
	if err != nil {
		return nil, err
	}
	// Visibility enrichment is OPTIONAL: /hubs/sections/{key}/manage
	// 404s on PMS 1.43+ (the manage-hubs endpoint was retired), and a
	// missing hub must never cost the owner his whole collections list —
	// degrade to all-false visibility flags instead of failing the call.
	// (Found live 2026-10-22: GetCollections died on the sandbox PMS
	// for exactly this reason while the list itself was fine.)
	hubsData, herr := c.serverFetch(ctx, s, "/hubs/sections/"+encodeURIComponent(sectionKey)+"/manage", fetchOptions{method: "GET"})
	var hubs []map[string]any
	visKnown := herr == nil
	if herr == nil {
		hubs = parseContainer(hubsData).hub
	}
	out := []Collection{}
	for _, coll := range entries {
		ratingKey := jsString(coll["ratingKey"])
		var hub map[string]any
		wantID := "custom.collection." + sectionKey + "." + ratingKey
		for _, h := range hubs {
			if stringField(h, "identifier") == wantID {
				hub = h
				break
			}
		}
		title := stringField(coll, "title")
		if title == "" {
			title = "Untitled collection"
		}
		var ownHome, sharedHome, recommended bool
		if hub != nil {
			ownHome = numOrZero(hub["promotedToOwnHome"]) != 0
			sharedHome = numOrZero(hub["promotedToSharedHome"]) != 0
			recommended = numOrZero(hub["promotedToRecommended"]) != 0
		}
		thumb := stringField(coll, "thumb")
		art := stringField(coll, "art")
		smart := numOrZero(coll["smart"]) != 0
		content := ""
		summary := ""
		if smart {
			content = stringField(coll, "content")
			summary = SmartRuleSummary(content)
		}
		out = append(out, Collection{
			RatingKey:             ratingKey,
			Title:                 title,
			ChildCount:            int(numOrZero(coll["childCount"])),
			Smart:                 smart,
			Content:               content,
			SmartSummary:          summary,
			Thumb:                 thumb,
			Art:                   art,
			Summary:               stringField(coll, "summary"),
			TitleSort:             stringField(coll, "titleSort"),
			AddedAt:               intField(coll, "addedAt"),
			UpdatedAt:             intField(coll, "updatedAt"),
			PromotedToOwnHome:     ownHome,
			PromotedToSharedHome:  sharedHome,
			PromotedToRecommended: recommended,
			// Health facts (v1.13): a composite thumb is Plex's
			// auto-mosaic — the collection has NO custom poster.
			HasPoster:       thumb != "" && !strings.Contains(thumb, "/composite/"),
			HasBackdrop:     art != "",
			VisibilityKnown: visKnown,
		})
	}
	return out, nil
}

// GetCollectionDetail reads ONE collection's full editable metadata
// (v1.13 manager editor): title, sort title, summary, content rating,
// labels, and its promotion/visibility state. Only fields the server
// actually returns are populated — Labels stays nil when the payload
// carries no Label array (PMS 1.43 omits it for collections), and
// visibility reports "unknown" with a note when the server cannot
// report promotion state at all (see CollectionDetail).
func (c *Client) GetCollectionDetail(ctx context.Context, s *Server, ratingKey string) (*CollectionDetail, error) {
	data, err := c.serverFetch(ctx, s, "/library/metadata/"+encodeURIComponent(ratingKey), fetchOptions{method: "GET"})
	if err != nil {
		return nil, err
	}
	cont := parseContainer(data)
	if len(cont.metadata) == 0 {
		return nil, &Error{Message: "Collection not found (ratingKey " + ratingKey + ")"}
	}
	m := cont.metadata[0]
	if stringField(m, "type") != "collection" {
		return nil, &Error{Message: "Item " + ratingKey + " is not a collection — the manager only edits collections."}
	}
	d := &CollectionDetail{
		RatingKey:        ratingKey,
		Title:            stringField(m, "title"),
		TitleSort:        stringField(m, "titleSort"),
		Summary:          stringField(m, "summary"),
		ContentRating:    stringField(m, "contentRating"),
		ChildCount:       int(numOrZero(m["childCount"])),
		Smart:            numOrZero(m["smart"]) != 0,
		Content:          stringField(m, "content"),
		Thumb:            stringField(m, "thumb"),
		Art:              stringField(m, "art"),
		LibrarySectionID: jsString(m["librarySectionID"]),
	}
	if raw, present := m["Label"]; present {
		if arr, ok := raw.([]any); ok {
			labels := TagList(arr)
			d.Labels = &labels
		}
	}
	if d.Smart {
		d.SmartSummary = SmartRuleSummary(d.Content)
	}
	// Visibility: some servers carry the promoted* flags on the
	// metadata record itself; otherwise ask the section's manage hubs
	// (the same join GetCollections does). When neither source answers
	// the state is UNKNOWN — never silently "library only".
	ownHome := numOrZero(m["promotedToOwnHome"]) != 0
	sharedHome := numOrZero(m["promotedToSharedHome"]) != 0
	known := false
	if _, present := m["promotedToOwnHome"]; present {
		known = true
	} else if d.LibrarySectionID != "" {
		hubsData, herr := c.serverFetch(ctx, s, "/hubs/sections/"+encodeURIComponent(d.LibrarySectionID)+"/manage",
			fetchOptions{method: "GET", params: map[string]string{"metadataItemId": ratingKey}})
		if herr == nil {
			known = true
			wantID := "custom.collection." + d.LibrarySectionID + "." + ratingKey
			for _, h := range parseContainer(hubsData).hub {
				if stringField(h, "identifier") == wantID || jsString(h["metadataItemId"]) == ratingKey {
					ownHome = numOrZero(h["promotedToOwnHome"]) != 0
					sharedHome = numOrZero(h["promotedToSharedHome"]) != 0
					break
				}
			}
		}
	}
	if known {
		d.VisibilityKnown = true
		switch {
		case ownHome:
			d.Visibility = "home"
		case sharedHome:
			d.Visibility = "shared"
		default:
			d.Visibility = "library"
		}
	} else {
		d.Visibility = "unknown"
		d.VisibilityNote = "This server does not report collection visibility (the hub manage endpoint is unavailable on this Plex version), so visibility is shown read-only."
	}
	return d, nil
}

// GetCollectionItems returns the members of one collection, in the
// collection's order.
//
// v1.14 — smart collections resolve through a DIFFERENT path than
// manual ones (verified live on the sandbox PMS 1.43.5, 2026-10-23):
// GET /library/metadata/{id}/children returns EMPTY for a smart
// collection even when it has matches, while
// GET /library/collections/{id}/children returns the rule-resolved
// members. Manual collections answer identically on both paths. So
// the record is read first and the path chosen by its smart flag; a
// failed record read falls back to the long-standing metadata path
// rather than failing a read that used to work.
func (c *Client) GetCollectionItems(ctx context.Context, s *Server, ratingKey string) ([]Item, error) {
	endpoint := "/library/metadata/" + encodeURIComponent(ratingKey) + "/children"
	if data, err := c.serverFetch(ctx, s, "/library/metadata/"+encodeURIComponent(ratingKey), fetchOptions{method: "GET"}); err == nil {
		if cont := parseContainer(data); len(cont.metadata) > 0 && numOrZero(cont.metadata[0]["smart"]) != 0 {
			endpoint = "/library/collections/" + encodeURIComponent(ratingKey) + "/children"
		}
	}
	entries, err := c.pagedMetadata(ctx, s, endpoint, 500)
	if err != nil {
		return nil, err
	}
	out := []Item{}
	for _, e := range entries {
		out = append(out, normalizeItem(e))
	}
	return out, nil
}

// GetChildrenItems returns an item's children as full Items — seasons
// of a show for the Overlay Studio drill-down (episodes of a season ride
// the same shape when a later phase needs them). Beyond normalizeItem,
// each child carries the fields a season target needs: its index
// (season number), leaf/viewed-leaf counts, and its parent's title +
// ratingKey, so the client can name the season's show without a second
// lookup. Paged like the other full enumerations.
func (c *Client) GetChildrenItems(ctx context.Context, s *Server, ratingKey string) ([]Item, error) {
	entries, err := c.pagedMetadata(ctx, s, "/library/metadata/"+encodeURIComponent(ratingKey)+"/children", 500)
	if err != nil {
		return nil, err
	}
	out := []Item{}
	for _, e := range entries {
		it := normalizeItem(e)
		it.Index = int(numOrZero(e["index"]))
		it.LeafCount = int(numOrZero(e["leafCount"]))
		it.ViewedLeafCount = int(numOrZero(e["viewedLeafCount"]))
		it.ParentTitle = stringField(e, "parentTitle")
		it.ParentRatingKey = jsString(e["parentRatingKey"])
		out = append(out, it)
	}
	return out, nil
}

// GetItem returns a single item (movie/show/collection) with editable
// metadata fields: normalizeItem plus tagline/titleSort/contentRating.
func (c *Client) GetItem(ctx context.Context, s *Server, ratingKey string) (*Item, error) {
	data, err := c.serverFetch(ctx, s, "/library/metadata/"+encodeURIComponent(ratingKey), fetchOptions{method: "GET"})
	if err != nil {
		return nil, err
	}
	cont := parseContainer(data)
	if len(cont.metadata) == 0 {
		return nil, &Error{Message: "Item not found on server", Endpoint: "/library/metadata/" + ratingKey}
	}
	m := cont.metadata[0]
	item := normalizeItem(m)
	item.Tagline = stringField(m, "tagline")
	item.TitleSort = stringField(m, "titleSort")
	item.ContentRating = stringField(m, "contentRating")
	return &item, nil
}

/* -- v0.2 Advisor reads ------------------------------------------------------
 * Added for the collection advisor's library scan. All READs (no writes),
 * reusing the pagedMetadata helper so large libraries don't blow up.
 * ------------------------------------------------------------------------- */

// GetSectionItemsAll returns every movie/show in a section, paginated, WITH
// Guid[] (includeGuids=1) so the Advisor can join to TMDb without fuzzy
// title matching. onPage (may be nil) is called after each page with the
// running count — the JS passes { done }; this Go contract passes the count.
// v0.4 — Plex-native Tag[] pull: Plex's own Genre/Director/Writer/Role/
// Studio/Country/Collection tags and contentRating are included so the
// zero-key Advisor generators fire without a TMDb key.
func (c *Client) GetSectionItemsAll(ctx context.Context, s *Server, sectionKey string, typ *int, onPage func(done int)) ([]LibraryItem, error) {
	params := map[string]string{"includeGuids": "1"}
	if typ != nil {
		params["type"] = strconv.Itoa(*typ)
	}
	endpoint := "/library/sections/" + encodeURIComponent(sectionKey) + "/all"
	items := []LibraryItem{}
	offset := 0
	const pageSize = 200 // smaller pages: each entry carries Guid[] + tags
	for {
		pageParams := map[string]string{}
		for k, v := range params {
			pageParams[k] = v
		}
		pageParams["X-Plex-Container-Start"] = strconv.Itoa(offset)
		pageParams["X-Plex-Container-Size"] = strconv.Itoa(pageSize)
		data, err := c.serverFetch(ctx, s, endpoint, fetchOptions{method: "GET", params: pageParams})
		if err != nil {
			return nil, err
		}
		cont := parseContainer(data)
		page := cont.metadata
		for _, m := range page {
			guids := []string{}
			for _, g := range toRawSlice(m["Guid"]) {
				gm, ok := g.(map[string]any)
				if !ok {
					continue
				}
				if id := stringField(gm, "id"); id != "" {
					guids = append(guids, id)
				}
			}
			var year *int
			if jsTruthy(m["year"]) {
				y := int(numOrZero(m["year"]))
				year = &y
			}
			// Defensive: not every Plex version includes these on /all.
			// Generators that need them skip items where they're missing.
			var plexRating *float64
			if v, present := m["rating"]; present && v != nil {
				f := numOrZero(v)
				plexRating = &f
			}
			var durationMin *int
			if v, present := m["duration"]; present && v != nil {
				d := int(math.Round(numOrZero(v) / 60000))
				durationMin = &d
			}
			// JS: m.Studio || m.studio — an empty ARRAY is truthy in JS
			// (and in jsTruthy), an empty string/missing value is not.
			studioRaw := m["Studio"]
			if !jsTruthy(studioRaw) {
				studioRaw = m["studio"]
			}
			items = append(items, LibraryItem{
				RatingKey:  jsString(m["ratingKey"]),
				Title:      titleOrUntitled(m),
				Year:       year,
				Type:       stringField(m, "type"),
				Guids:      guids,
				ViewCount:  int(numOrZero(m["viewCount"])),
				ViewOffset: int(numOrZero(m["viewOffset"])),
				Thumb:      stringField(m, "thumb"),
				ChildCount: int(numOrZero(m["childCount"])), // shows: season count (TV completeness generator)
				AddedAt:    intField(m, "addedAt"),
				PlexRating: plexRating,
				// v0.4 Plex-native tags — powers the zero-key Advisor
				// generators (genres, directors, actors, studios, content
				// ratings). TMDb is now only a fallback for genres.
				DurationMin: durationMin,
				SectionKey:  sectionKey,
				Tags: Tags{
					Genre:      tagListValue(m["Genre"]),
					Director:   tagListValue(m["Director"]),
					Writer:     tagListValue(m["Writer"]),
					Actor:      tagListValue(m["Role"]),
					Studio:     tagListValue(studioRaw),
					Country:    tagListValue(m["Country"]),
					Collection: tagListValue(m["Collection"]),
				},
				ContentRating: stringField(m, "contentRating"),
			})
		}
		offset += len(page)
		if onPage != nil {
			onPage(len(items))
		}
		if cont.hasTotal && float64(offset) >= cont.total {
			break
		}
		if len(page) == 0 || (!cont.hasTotal && len(page) < pageSize) {
			break
		}
		if offset > 100000 {
			return nil, &Error{Message: "Plex pagination exceeded the safety limit"}
		}
	}
	return items, nil
}

// titleOrUntitled is JS `m.title || 'Untitled'`.
func titleOrUntitled(m map[string]any) string {
	if t := stringField(m, "title"); t != "" {
		return t
	}
	return "Untitled"
}

// GetFullItem returns the raw full metadata for ONE item — includes
// Media[]/Part[]/Stream[] and Guid[]. Used by the dub scanner (audio tracks)
// and nothing else. Returns the raw Metadata[0] object, un-normalized, ON
// PURPOSE.
func (c *Client) GetFullItem(ctx context.Context, s *Server, ratingKey string) (map[string]any, error) {
	data, err := c.serverFetch(ctx, s, "/library/metadata/"+encodeURIComponent(ratingKey), fetchOptions{method: "GET", params: map[string]string{"includeGuids": "1"}})
	if err != nil {
		return nil, err
	}
	cont := parseContainer(data)
	if len(cont.metadata) == 0 {
		return nil, &Error{Message: "Item not found on server", Endpoint: "/library/metadata/" + ratingKey}
	}
	return cont.metadata[0], nil
}

// GetChildrenRaw returns raw children (seasons of a show, episodes of a
// season) for the dub-scan drill-down, projected to the JS shape
// [{ ratingKey, title, index, type }] carried as maps (index is the raw
// decoded number, or nil when Plex omits it — JS `m.index ?? null`).
func (c *Client) GetChildrenRaw(ctx context.Context, s *Server, ratingKey string) ([]map[string]any, error) {
	data, err := c.serverFetch(ctx, s, "/library/metadata/"+encodeURIComponent(ratingKey)+"/children", fetchOptions{method: "GET", params: map[string]string{"X-Plex-Container-Size": "500"}})
	if err != nil {
		return nil, err
	}
	cont := parseContainer(data)
	out := []map[string]any{}
	for _, m := range cont.metadata {
		var index any
		if v, present := m["index"]; present {
			index = v
		}
		out = append(out, map[string]any{
			"ratingKey": jsString(m["ratingKey"]),
			"title":     stringField(m, "title"),
			"index":     index,
			"type":      stringField(m, "type"),
		})
	}
	return out, nil
}

// GetAllLeavesCapped returns up to `limit` leaf episodes of a show
// (GET /library/metadata/{key}/allLeaves, a single container page of
// limit+1) plus the show's total leaf count — from totalSize when Plex
// reports it, else limit+1 as a floor when the page came back full
// (enough for the caller to know the result is partial). Backs the
// overlay facts endpoint's show-level language aggregation. NOTE
// (verified live on PMS 1.43, 2026-10-22): allLeaves entries carry NO
// Media/Part/Stream elements — only identity fields. Streams live on
// the item records themselves; callers aggregate from GetItemsBatch
// over the leaf ratingKeys, never from the leaves payload.
func (c *Client) GetAllLeavesCapped(ctx context.Context, s *Server, ratingKey string, limit int) ([]map[string]any, int, error) {
	data, err := c.serverFetch(ctx, s, "/library/metadata/"+encodeURIComponent(ratingKey)+"/allLeaves", fetchOptions{method: "GET", params: map[string]string{
		"X-Plex-Container-Start": "0",
		"X-Plex-Container-Size":  strconv.Itoa(limit + 1),
	}})
	if err != nil {
		return nil, 0, err
	}
	cont := parseContainer(data)
	total := len(cont.metadata)
	if cont.hasTotal {
		total = int(cont.total)
	}
	eps := cont.metadata
	if len(eps) > limit {
		eps = eps[:limit]
		if total < len(cont.metadata) {
			total = len(cont.metadata)
		}
	}
	return eps, total, nil
}

// GetItemsBatch fetches full item records (WITH Media/Part/Stream)
// for many ratingKeys in comma-joined batch requests
// (GET /library/metadata/{k1,k2,...}, chunks of 50). Used by the
// show-level language aggregation: allLeaves gives keys only, the
// stream data is on the episode records. Returns the parsed Metadata
// maps in server order.
func (c *Client) GetItemsBatch(ctx context.Context, s *Server, ratingKeys []string) ([]map[string]any, error) {
	out := []map[string]any{}
	for start := 0; start < len(ratingKeys); start += 50 {
		end := start + 50
		if end > len(ratingKeys) {
			end = len(ratingKeys)
		}
		joined := ""
		for i, k := range ratingKeys[start:end] {
			if i > 0 {
				joined += ","
			}
			joined += encodeURIComponent(k)
		}
		data, err := c.serverFetch(ctx, s, "/library/metadata/"+joined, fetchOptions{method: "GET"})
		if err != nil {
			return nil, err
		}
		out = append(out, parseContainer(data).metadata...)
	}
	return out, nil
}

// ListSectionTags lists tag directories for a section (director/actor/
// genre...).
// UNVERIFIED. Endpoint form /library/sections/{key}/director is
// community-documented; the response is parsed DEFENSIVELY — entries may
// carry the tag as `tag` or `title` and the count as `count` or `size`.
// Returns [{ name, count }] sorted by count desc (marshalled as raw JSON,
// per this Go contract), or [] if the endpoint 404s (the Advisor then skips
// the directors/actors generator silently).
// FUTURE AI: if the owner reports "no director suggestions", check here first
// — this is the weakest-confidence read in v0.2.
func (c *Client) ListSectionTags(ctx context.Context, s *Server, sectionKey, tag string) (json.RawMessage, error) {
	data, err := c.serverFetch(ctx, s, "/library/sections/"+encodeURIComponent(sectionKey)+"/"+encodeURIComponent(tag), fetchOptions{method: "GET"})
	if err != nil {
		if perr, ok := err.(*Error); ok && perr.Status == 404 {
			return json.RawMessage("[]"), nil
		}
		return nil, err
	}
	cont := parseContainer(data)
	out := []tagCountShape{}
	for _, d := range cont.directory {
		name := stringField(d, "tag")
		if name == "" {
			name = stringField(d, "title")
		}
		var count float64
		if v, present := d["count"]; present && v != nil {
			count = numOrZero(v)
		} else {
			count = numOrZero(d["size"])
		}
		if name == "" {
			continue
		}
		out = append(out, tagCountShape{Name: name, Count: int(count)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return rawMessage(out)
}

// SearchLibrary is the library search for the Builder's "add items" picker.
// typ: 1 = movie, 2 = show (Plex numeric types); a nil typ omits the type
// parameter (the JS always receives one from its callers).
// Queries shorter than 2 characters return an empty list without a call.
func (c *Client) SearchLibrary(ctx context.Context, s *Server, sectionKey, query string, typ *int) ([]Item, error) {
	q := strings.TrimSpace(query)
	if len([]rune(q)) < 2 {
		return []Item{}, nil
	}
	params := map[string]string{"title": q, "X-Plex-Container-Size": "50"}
	if typ != nil {
		params["type"] = strconv.Itoa(*typ)
	}
	data, err := c.serverFetch(ctx, s, "/library/sections/"+encodeURIComponent(sectionKey)+"/all", fetchOptions{method: "GET", params: params})
	if err != nil {
		return nil, err
	}
	cont := parseContainer(data)
	out := []Item{}
	for _, m := range cont.metadata {
		out = append(out, normalizeItem(m))
	}
	return out, nil
}

// PreviewFilter runs an advanced-filter query against a section — powers
// BOTH the smart rule Builder's live Preview AND smart-collection creation.
//
// FUTURE AI: this is why the smart Builder is trustworthy: whatever
// query string the rule builder produces is executed here first. If
// Preview returns the right titles, the filter syntax is valid on
// THIS server. Smart-collection CREATION with that same query was
// verified live on the sandbox PMS 1.43.5 (v1.14, 2026-10-23) — see
// CreateCollection in writes.go.
//
// filterQuery is a raw query string like "genre=35&decade=2020&unwatched=1"
// (built by the frontend's filter builder, shown to the user verbatim).
// A nil typ omits the type= segment (the JS interpolates its caller's type).
// sectionAllPath builds a /library/sections/{key}/all path with the
// given type + raw filter segments. Shared by PreviewFilter and
// GetSectionItemsFiltered.
// FUTURE AI: never leave a trailing "?" on the path — serverFetch
// appends "&"+params after an existing "?", producing "?&X-Plex-…",
// and real PMS answers that with HTTP 400. That one character is why
// the overlay picker's item grid was always empty against a live
// server (the renderer swallowed the error): browse mode passes an
// empty filter and no type, i.e. zero segments.
func sectionAllPath(sectionKey, filterQuery string, typ *int) string {
	segments := []string{}
	if typ != nil {
		segments = append(segments, "type="+strconv.Itoa(*typ))
	}
	if filterQuery != "" {
		segments = append(segments, filterQuery)
	}
	path := "/library/sections/" + encodeURIComponent(sectionKey) + "/all"
	if len(segments) > 0 {
		path += "?" + strings.Join(segments, "&")
	}
	return path
}

func (c *Client) PreviewFilter(ctx context.Context, s *Server, sectionKey, filterQuery string, typ *int) ([]Item, error) {
	path := sectionAllPath(sectionKey, filterQuery, typ)
	data, err := c.serverFetch(ctx, s, path, fetchOptions{method: "GET", params: map[string]string{"X-Plex-Container-Size": "100"}})
	if err != nil {
		return nil, err
	}
	cont := parseContainer(data)
	out := []Item{}
	for _, m := range cont.metadata {
		out = append(out, normalizeItem(m))
	}
	return out, nil
}

// SectionItemsPage is ONE page of a section listing, in the shape the
// Builder wizard's poster grid consumes (v1.17). Total is the server's
// own totalSize for the (possibly filtered/searched) listing; when a
// server omits totalSize, TotalKnown is false and Total carries only
// the count through the end of this page — the UI must not present it
// as an exact library total in that case.
type SectionItemsPage struct {
	Items      []Item `json:"items"`
	Total      int    `json:"total"`
	TotalKnown bool   `json:"totalKnown"`
	Start      int    `json:"start"`
	Size       int    `json:"size"`
	HasMore    bool   `json:"hasMore"`
}

// GetSectionItemsPage fetches a single page of a section's items —
// READ-ONLY. It backs the Builder wizard's poster grid: blank-source
// browsing (optionally title-searched server-side via the same
// `title=` parameter SearchLibrary uses) and the rules source's live
// match preview (filterQuery is the Builder's raw advanced-filter
// string, exactly as PreviewFilter executes it). One serverFetch per
// call: the grid pages explicitly instead of pulling a whole library
// into the renderer, so multi-thousand-item libraries stay usable.
func (c *Client) GetSectionItemsPage(ctx context.Context, s *Server, sectionKey, filterQuery string, typ *int, title string, start, size int) (SectionItemsPage, error) {
	if start < 0 {
		start = 0
	}
	if size <= 0 {
		size = 50
	}
	if size > 200 {
		size = 200
	}
	params := map[string]string{
		"X-Plex-Container-Start": strconv.Itoa(start),
		"X-Plex-Container-Size":  strconv.Itoa(size),
	}
	if t := strings.TrimSpace(title); t != "" {
		params["title"] = t
	}
	data, err := c.serverFetch(ctx, s, sectionAllPath(sectionKey, filterQuery, typ), fetchOptions{method: "GET", params: params})
	if err != nil {
		return SectionItemsPage{}, err
	}
	cont := parseContainer(data)
	entries := cont.metadata
	if !cont.metadataPresent {
		entries = cont.directory
	}
	out := []Item{}
	for _, m := range entries {
		out = append(out, normalizeItem(m))
	}
	page := SectionItemsPage{Items: out, Start: start, Size: size}
	if cont.hasTotal {
		page.Total = int(cont.total)
		page.TotalKnown = true
		page.HasMore = start+len(out) < int(cont.total)
	} else {
		// No server total: report only what this page proves. A full
		// page means more MAY exist; a short page means the listing
		// ended here (Total is then exact through this page, but the
		// UI still labels it as loaded-count, not a server total).
		page.Total = start + len(out)
		page.HasMore = len(out) == size
	}
	return page, nil
}

// GetSectionItemsFiltered returns EVERY item of a section matching a
// raw filter query, paged — the enumeration behind the Apply view's
// library and tag scopes. PreviewFilter caps at one 100-item page,
// which would silently truncate a library-wide apply; this pages by
// 200 through pagedMetadata like the other full enumerations.
func (c *Client) GetSectionItemsFiltered(ctx context.Context, s *Server, sectionKey, filterQuery string, typ *int) ([]Item, error) {
	entries, err := c.pagedMetadata(ctx, s, sectionAllPath(sectionKey, filterQuery, typ), 200)
	if err != nil {
		return nil, err
	}
	out := []Item{}
	for _, e := range entries {
		out = append(out, normalizeItem(e))
	}
	return out, nil
}

// TagType is one filterable tag type a section offers
// (GET /library/sections/{key}/filters): Filter is the query/filter
// name ("genre", "year", "collection", …), Title the display name.
type TagType struct {
	Filter string `json:"filter"`
	Title  string `json:"title"`
}

// ListSectionTagTypes lists the tag types usable for filtered
// enumeration of a section, in server order.
func (c *Client) ListSectionTagTypes(ctx context.Context, s *Server, sectionKey string) ([]TagType, error) {
	data, err := c.serverFetch(ctx, s, "/library/sections/"+encodeURIComponent(sectionKey)+"/filters", fetchOptions{method: "GET"})
	if err != nil {
		return nil, err
	}
	cont := parseContainer(data)
	entries := cont.directory
	if len(entries) == 0 {
		entries = cont.metadata
	}
	out := []TagType{}
	for _, d := range entries {
		f := stringField(d, "filter")
		if f == "" {
			continue
		}
		title := stringField(d, "title")
		if title == "" {
			title = f
		}
		out = append(out, TagType{Filter: f, Title: title})
	}
	return out, nil
}

// TagValue is one value of a section tag type
// (GET /library/sections/{key}/{tagType}): Value is the opaque filter
// value Plex expects back in the filtered /all query (the entry's
// `key`, e.g. "5" for a collection or "2025" for a year), Name the
// display title. Verified live on PMS 1.43 (2026-10-22): these entries
// carry key + title + fastKey and NO counts.
type TagValue struct {
	Value string `json:"value"`
	Name  string `json:"name"`
}

// ListSectionTagValues lists the values of one tag type in a section.
func (c *Client) ListSectionTagValues(ctx context.Context, s *Server, sectionKey, tagType string) ([]TagValue, error) {
	data, err := c.serverFetch(ctx, s, "/library/sections/"+encodeURIComponent(sectionKey)+"/"+encodeURIComponent(tagType), fetchOptions{method: "GET"})
	if err != nil {
		return nil, err
	}
	cont := parseContainer(data)
	entries := cont.directory
	if len(entries) == 0 {
		entries = cont.metadata
	}
	out := []TagValue{}
	for _, d := range entries {
		v := stringField(d, "key")
		name := stringField(d, "title")
		if name == "" {
			name = stringField(d, "tag")
		}
		if v == "" {
			v = name
		}
		if name == "" {
			continue
		}
		out = append(out, TagValue{Value: v, Name: name})
	}
	return out, nil
}
