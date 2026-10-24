package plex

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

/* -- library writes ----------------------------------------------------------
 * Each method below carries its own verification note: the write
 * paths were exercised against real servers release by release
 * (manual create + ordering in the v1.0 live tests, metadata edits
 * pinned against the sandbox PMS 1.43 in v1.13, smart creation
 * probed live on the sandbox PMS 1.43.5 in v1.14). They are wired
 * end-to-end through the app and the Builder's dry-run -> confirm ->
 * apply flow, and they surface Plex's exact HTTP error verbatim.
 * Do NOT invent new endpoints by guessing; test or leave the flag.
 * ------------------------------------------------------------------------- */

// itemUrisFor builds the single metadata path containing comma-separated
// rating keys that Plex expects for membership writes.
// Source-reviewed against python-plexapi collection.py; still UNVERIFIED live.
func itemUrisFor(machine string, ratingKeys []string) string {
	escaped := make([]string, 0, len(ratingKeys))
	for _, k := range ratingKeys {
		escaped = append(escaped, encodeURIComponent(k))
	}
	return "server://" + machine + "/com.plexapp.plugins.library/library/metadata/" + strings.Join(escaped, ",")
}

// CreateCollection creates a collection.
// For MANUAL collections: pass ItemRatingKeys.
// For SMART collections: pass FilterQuery (e.g. "genre=35&decade=2020") —
// the SAME query string that PreviewFilter already validated live. The
// smart URI form is:
//
//	server://{machine}/com.plexapp.plugins.library/library/sections/{sectionKey}/all?{filters}
//
// SMART FORM VERIFIED LIVE on the sandbox PMS 1.43.5 (2026-10-23,
// v1.14 probe): POST /library/collections with the parameters in the
// QUERY STRING (the same params in the POST body are rejected with
// HTTP 400) created a working smart collection — the returned record
// self-described with smart="1" and `content` holding the filter URI
// verbatim, its children resolved from the filter via
// /library/collections/{id}/children, and membership tracked the
// library dynamically (editing a fixture's year into the rule's
// range added it with no collection write; restoring it removed it).
// The manual form was verified in earlier releases.
//
// Returns the created collection payload as raw JSON in the JS shape
// { ratingKey, title } (plus an honest `note` when Plex returns no id —
// the JS reports back honestly instead of fabricating an id).
func (c *Client) CreateCollection(ctx context.Context, s *Server, p CreatePayload) (json.RawMessage, error) {
	if p.Title == "" {
		return nil, &Error{Message: "Collection title is required"}
	}
	sectionID := ""
	if numericRe.MatchString(p.SectionKey) {
		sectionID = p.SectionKey
	}
	if sectionID == "" {
		return nil, &Error{Message: "A numeric library section id is required"}
	}
	var uri string
	if p.Smart {
		if p.FilterQuery == "" {
			return nil, &Error{Message: "Smart collections need at least one rule"}
		}
		uri = "server://" + s.Machine + "/com.plexapp.plugins.library/library/sections/" + sectionID + "/all?type=" + strconv.Itoa(p.Type) + "&" + p.FilterQuery
	} else {
		if len(p.ItemRatingKeys) == 0 {
			return nil, &Error{Message: "Refusing to create an empty manual collection"}
		}
		uri = itemUrisFor(s.Machine, p.ItemRatingKeys)
	}
	smartVal := "0"
	if p.Smart {
		smartVal = "1"
	}
	data, err := c.serverFetch(ctx, s, "/library/collections", fetchOptions{method: "POST", params: map[string]string{
		"type":      strconv.Itoa(p.Type), // 1 = movie, 2 = show
		"title":     p.Title,
		"smart":     smartVal,
		"sectionId": sectionID,
		"uri":       uri,
	}})
	if err != nil {
		return nil, err
	}
	cont := parseContainer(data)
	ratingKey := ""
	if len(cont.metadata) > 0 {
		ratingKey = jsString(cont.metadata[0]["ratingKey"])
	}
	if ratingKey == "" {
		// Plex sometimes returns 200 with an empty container on success; the
		// dashboard refresh will pick the collection up. We still report back
		// honestly instead of fabricating an id.
		return rawMessage(createdShape{RatingKey: "", Title: p.Title, Note: "Plex accepted the create call but returned no id — refresh the dashboard to confirm."})
	}
	// Surface partial creation so the user can recover without duplicating it.
	// JS passes { titleSort, summary, visHome, visShared, visRecommended } —
	// titleSort/summary are PRESENT even when empty (the JS destructuring
	// default ''), so they are sent as non-nil pointers here.
	titleSort := p.TitleSort
	summary := p.Summary
	meta := MetaFields{TitleSort: &titleSort, Summary: &summary, VisHome: p.VisHome, VisShared: p.VisShared, VisRecommended: p.VisRecommended}
	// Content rating rides the SAME verified metadata write, but only
	// when the wizard's Details step actually set one — an empty value
	// must not lock a blank rating onto a fresh collection.
	if p.ContentRating != "" {
		rating := p.ContentRating
		meta.ContentRating = &rating
	}
	if err := c.UpdateCollectionMeta(ctx, s, ratingKey, meta); err != nil {
		return nil, &Error{
			Message: fmt.Sprintf("Collection created (ID %s), but settings failed: %s", ratingKey, err.Error()),
			Hint:    "Refresh and edit this collection. Do not create it again.",
		}
	}
	if !p.Smart {
		if err := c.SetCollectionItems(ctx, s, ratingKey, p.ItemRatingKeys); err != nil {
			return nil, &Error{
				Message: fmt.Sprintf("Collection created (ID %s), but ordering failed: %s", ratingKey, err.Error()),
				Hint:    "Refresh and edit this collection; do not recreate it.",
			}
		}
	}
	return rawMessage(createdShape{RatingKey: ratingKey, Title: p.Title})
}

// UpdateCollectionMeta edits a collection's metadata: title, sort title,
// summary, content rating, labels, visibility flags.
// Uses Plex's generic metadata-edit endpoint. Field forms were pinned
// against the sandbox PMS 1.43 (2026-10-23, v1.13):
//   - title: plain `title=` sticks.
//   - summary: plain `summary=` sticks.
//   - titleSort: plain `titleSort=` returns 200 but NEVER sticks; the
//     `.value` + `.locked=1` field form sticks and reads back. (The
//     pre-v1.13 code sent the plain form — sort-title saves were a
//     silent no-op. The verified re-read in the manager editor is what
//     surfaced it.)
//   - contentRating: `contentRating.value` + `contentRating.locked=1`
//     sticks and reads back.
//   - labels: `label.value` (one comma-joined param) is the standard
//     form, but PMS 1.43 registers only the field lock and creates no
//     labels on collections. The write is sent as-is and the caller's
//     verified re-read reports what the server actually holds — a
//     not-taken labels write is surfaced, never hidden.
//
// Visibility mapping (shown in the Builder as plain checkboxes):
//
//	VisHome        -> promotedToOwnHome       (home screen, this user)
//	VisShared      -> promotedToSharedHome    (home screen, shared users)
//	VisRecommended -> promotedToRecommended   (Recommended row)
//
// FUTURE AI: if Plex ignores these params, the collection still updates —
// check the returned item / re-read it to confirm, and surface the gap.
func (c *Client) UpdateCollectionMeta(ctx context.Context, s *Server, ratingKey string, f MetaFields) error {
	params := map[string]string{}
	if f.Title != nil {
		params["title"] = *f.Title
	}
	if f.TitleSort != nil {
		params["titleSort.value"] = *f.TitleSort
		params["titleSort.locked"] = "1"
	}
	if f.Summary != nil {
		params["summary"] = *f.Summary
	}
	if f.ContentRating != nil {
		params["contentRating.value"] = *f.ContentRating
		params["contentRating.locked"] = "1"
	}
	if f.Labels != nil {
		params["label.value"] = strings.Join(*f.Labels, ",")
		params["label.locked"] = "1"
	}
	// PUT with no body, params in the query string — this is how Plex's own
	// web client performs edits.
	if _, err := c.serverFetch(ctx, s, "/library/metadata/"+encodeURIComponent(ratingKey), fetchOptions{method: "PUT", params: params}); err != nil {
		return err
	}
	if f.VisHome != nil || f.VisShared != nil || f.VisRecommended != nil {
		data, err := c.serverFetch(ctx, s, "/library/metadata/"+encodeURIComponent(ratingKey), fetchOptions{method: "GET"})
		if err != nil {
			return err
		}
		cont := parseContainer(data)
		page := cont.metadata
		if !cont.metadataPresent {
			page = cont.directory
		}
		var sectionVal any
		if len(page) > 0 {
			sectionVal = page[0]["librarySectionID"]
		}
		if !jsTruthy(sectionVal) {
			return &Error{Message: "Metadata saved, but Plex returned no library section for visibility settings"}
		}
		section := jsString(sectionVal)
		hubData, err := c.serverFetch(ctx, s, "/hubs/sections/"+encodeURIComponent(section)+"/manage", fetchOptions{method: "GET", params: map[string]string{"metadataItemId": ratingKey}})
		if err != nil {
			return err
		}
		hubs := parseContainer(hubData).hub
		var hub map[string]any
		wantID := "custom.collection." + section + "." + ratingKey
		for _, h := range hubs {
			if stringField(h, "identifier") == wantID {
				hub = h
				break
			}
		}
		visValue := func(flag *bool, key string) string {
			if flag == nil {
				if hub != nil {
					return strconv.Itoa(int(numOrZero(hub[key])))
				}
				return "0"
			}
			if *flag {
				return "1"
			}
			return "0"
		}
		visibility := map[string]string{
			"promotedToOwnHome":     visValue(f.VisHome, "promotedToOwnHome"),
			"promotedToSharedHome":  visValue(f.VisShared, "promotedToSharedHome"),
			"promotedToRecommended": visValue(f.VisRecommended, "promotedToRecommended"),
		}
		base := "/hubs/sections/" + encodeURIComponent(section) + "/manage"
		if hub != nil {
			if _, err := c.serverFetch(ctx, s, base+"/"+encodeURIComponent(stringField(hub, "identifier")), fetchOptions{method: "PUT", params: visibility}); err != nil {
				return err
			}
		} else {
			postParams := map[string]string{}
			for k, v := range visibility {
				postParams[k] = v
			}
			postParams["metadataItemId"] = ratingKey
			if _, err := c.serverFetch(ctx, s, base, fetchOptions{method: "POST", params: postParams}); err != nil {
				return err
			}
		}
	}
	return nil
}

// SetCollectionItems applies only membership differences, then explicit
// custom order.
// UNVERIFIED live. Add first: an unsuccessful add must never leave existing
// members removed.
// Source: python-plexapi collection.py addItems/removeItems/moveItem/sortUpdate.
func (c *Client) SetCollectionItems(ctx context.Context, s *Server, ratingKey string, keysInOrder []string) error {
	current, err := c.GetCollectionItems(ctx, s, ratingKey)
	if err != nil {
		return err
	}
	wanted := []string{}
	seen := map[string]bool{}
	for _, k := range keysInOrder {
		if !seen[k] {
			seen[k] = true
			wanted = append(wanted, k)
		}
	}
	existing := map[string]bool{}
	for _, item := range current {
		existing[item.RatingKey] = true
	}
	additions := []string{}
	for _, k := range wanted {
		if !existing[k] {
			additions = append(additions, k)
		}
	}
	if len(additions) > 0 {
		if err := c.AddCollectionItems(ctx, s, ratingKey, additions); err != nil {
			return err
		}
	}
	wantedSet := map[string]bool{}
	for _, k := range wanted {
		wantedSet[k] = true
	}
	for _, item := range current {
		if !wantedSet[item.RatingKey] {
			if err := c.RemoveCollectionItem(ctx, s, ratingKey, item.RatingKey); err != nil {
				return err
			}
		}
	}
	if _, err := c.serverFetch(ctx, s, "/library/metadata/"+encodeURIComponent(ratingKey)+"/prefs", fetchOptions{method: "PUT", params: map[string]string{"collectionSort": "2"}}); err != nil {
		return err
	}
	for i, k := range wanted {
		params := map[string]string{}
		if i > 0 {
			params["after"] = wanted[i-1]
		}
		// FUTURE-AI: the move endpoint lives under /library/collections/,
		// NOT /library/metadata/ — the metadata shape returns HTTP 404 on
		// a real server (hit live 2026-10-20; reproduced and the
		// fix verified against a throwaway PMS 1.43.5 in the sandbox: the
		// collections shape returns 200 and actually reorders). Do not
		// "normalize" this back to /library/metadata/.
		if _, err := c.serverFetch(ctx, s, "/library/collections/"+encodeURIComponent(ratingKey)+"/items/"+encodeURIComponent(k)+"/move", fetchOptions{method: "PUT", params: params}); err != nil {
			return err
		}
	}
	return nil
}

// AddCollectionItems adds items to a manual collection, preserving given order.
// UNVERIFIED.
func (c *Client) AddCollectionItems(ctx context.Context, s *Server, ratingKey string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	_, err := c.serverFetch(ctx, s, "/library/metadata/"+encodeURIComponent(ratingKey)+"/items", fetchOptions{method: "PUT", params: map[string]string{"uri": itemUrisFor(s.Machine, keys)}})
	return err
}

// RemoveCollectionItem removes one item from a manual collection. This
// removes only the collection membership edge — never the media itself —
// and serverFetch proves the parent is a collection before sending.
// UNVERIFIED.
func (c *Client) RemoveCollectionItem(ctx context.Context, s *Server, ratingKey, itemKey string) error {
	_, err := c.serverFetch(ctx, s, "/library/metadata/"+encodeURIComponent(ratingKey)+"/items/"+encodeURIComponent(itemKey), fetchOptions{method: "DELETE"})
	return err
}

// SetPosterFromURL sets a collection's poster from an image URL. Plex
// fetches the URL server-side. (Local-file upload is the overlay studio's
// concern; the Builder only needs URL-based posters in v0.1.)
// UNVERIFIED. Requires a verified original backup first (requirePosterBackup).
func (c *Client) SetPosterFromURL(ctx context.Context, s *Server, ratingKey, imageURL string) error {
	if err := c.requirePosterBackup(s, ratingKey); err != nil {
		return err
	}
	if !uriSchemeRe.MatchString(imageURL) {
		return &Error{Message: "Poster must be an http(s) URL"}
	}
	_, err := c.serverFetch(ctx, s, "/library/metadata/"+encodeURIComponent(ratingKey)+"/posters", fetchOptions{method: "POST", params: map[string]string{"url": imageURL}})
	return err
}

// UploadPosterBytes uploads raw poster bytes via multipart POST — the
// python-plexapi form for local poster files. This is the overlay studio's
// primary write path (composites are generated locally, so there is no URL
// for Plex to fetch).
// UNVERIFIED.
//
// FUTURE AI — verification notes for the live pass:
//   - Endpoint: POST /library/metadata/{id}/posters, multipart/form-data,
//     single file field. If Plex answers 404/405 the form is wrong — the
//     caller (the overlays package) falls back to a temp local URL +
//     SetPosterFromURL on local connections before reporting failure.
//   - serverFetch can't carry a body, so this method does its own fetch.
//     It walks the server's ranked connections (rawConnections) under
//     the write-safety rule, narrowed to what can be PROVEN: the next
//     candidate is tried only when the attempt demonstrably never
//     reached the server — httptrace's WroteRequest never fired, i.e.
//     the failure was DNS / dial / TLS, before any byte was written.
//     Once a request may have been sent, a failure is a WRITE OUTCOME
//     UNKNOWN error (typed: see Error.UnknownOutcome /
//     IsUnknownOutcome) and is never replayed (the v0.1 rule) — the
//     caller resolves it by verifying the server state afterwards.
//     The walk exists because a pinned "best" connection can be a
//     black hole (a Docker bridge address advertised via plex.direct)
//     that never answers at all — every attempt provably unsent,
//     every item in a run failing on a 30s dial timeout (live
//     report, 2026-10-22).
//   - The final (serving) candidate gets the long confirmation
//     window (defaultPosterUploadTimeout, 90s): on a remote server
//     the upload transfer itself plus slow ingestion can eat a
//     read-sized budget whole — three restores failed exactly that
//     way live (a remote Docker-hosted server, 2026-10-23) — while non-final candidates
//     keep the short walk cap. A longer window converts would-be
//     unknown outcomes into confirmed writes; it never licenses a
//     replay.
func (c *Client) UploadPosterBytes(ctx context.Context, s *Server, ratingKey string, png []byte) error {
	return c.uploadImageBytes(ctx, s, ratingKey, "posters", "Poster", "poster", png)
}

// UploadArtBytes uploads raw backdrop/art bytes via multipart POST to
// /library/metadata/{id}/arts — the backdrop twin of UploadPosterBytes
// (v1.13, for the collections manager's bulk art). Same discipline:
// the verified-poster-backup gate runs first (the manager flow takes
// the item's poster backup before any art write), the write policy
// must allow the path, and the connection walk never replays a
// possibly-sent request. Verified against the sandbox PMS 1.43: the
// multipart form (field "file") is accepted and the item's art path
// changes to /library/metadata/{id}/art/{ts}. (URL ingest to /arts
// was also probed and rejected by that server's fetcher — bytes are
// the reliable form, so no URL variant is offered.)
func (c *Client) UploadArtBytes(ctx context.Context, s *Server, ratingKey string, img []byte) error {
	return c.uploadImageBytes(ctx, s, ratingKey, "arts", "Backdrop", "backdrop", img)
}

// uploadImageBytes is the shared multipart upload walk behind
// UploadPosterBytes and UploadArtBytes: suffix is the endpoint tail
// ("posters" | "arts"), noun/filebase only shape messages and the
// multipart filename.
func (c *Client) uploadImageBytes(ctx context.Context, s *Server, ratingKey, suffix, noun, filebase string, png []byte) error {
	if err := c.requirePosterBackup(s, ratingKey); err != nil {
		return err
	}
	if _, err := WritePolicy("POST", "/library/metadata/"+ratingKey+"/"+suffix, map[string]string{}); err != nil {
		return &Error{Message: err.Error()}
	}
	endpoint := "/library/metadata/" + ratingKey + "/" + suffix
	if len(png) == 0 {
		return &Error{Message: "Refusing to upload an empty " + strings.ToLower(noun), Endpoint: endpoint}
	}
	conns := c.rawConnections(s)
	if len(conns) == 0 {
		return &Error{Message: "No usable connection to the Plex server", Hint: "Pick a server on the Connect tab first."}
	}
	boundary := "----pcm-overlay-" + strconv.FormatInt(time.Now().UnixNano(), 36) + randomBoundarySuffix()
	ct, ext := sniffPosterContentType(png)
	head := "--" + boundary + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"" + filebase + "." + ext + "\"\r\nContent-Type: " + ct + "\r\n\r\n"
	tail := "\r\n--" + boundary + "--\r\n"
	body := bytes.Join([][]byte{[]byte(head), png, []byte(tail)}, nil)
	var tried []string
	for i, conn := range conns {
		if ctx.Err() != nil {
			return cancelledErr(endpoint)
		}
		last := i == len(conns)-1
		attemptTimeout := c.uploadTimeout()
		if !last {
			attemptTimeout = 30000 * time.Millisecond
			if attemptTimeout > c.walkCap() {
				attemptTimeout = c.walkCap()
			}
		}
		reqURL := trimOneTrailingSlash(conn.URI) + "/library/metadata/" + encodeURIComponent(ratingKey) + "/" + suffix
		reqCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		var wrote int32
		trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) {
			atomic.StoreInt32(&wrote, 1)
		}}
		reqCtx = httptrace.WithClientTrace(reqCtx, trace)
		req, rerr := http.NewRequestWithContext(reqCtx, "POST", reqURL, bytes.NewReader(body))
		if rerr != nil {
			cancel()
			// The request was never even built; nothing was sent.
			if !last {
				tried = append(tried, fmt.Sprintf("%s (%s)", conn.URI, truncateRunes(rerr.Error(), 120)))
				continue
			}
			return &Error{Message: fmt.Sprintf("%s upload failed (%s)", noun, rerr.Error()), Endpoint: endpoint}
		}
		req.Header.Set("X-Plex-Token", s.Token)
		req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
		req.Header.Set("Accept", "application/json")
		res, ferr := c.httpC().Do(req)
		if ferr != nil {
			cancel()
			if ctx.Err() != nil {
				return cancelledErr(endpoint)
			}
			if atomic.LoadInt32(&wrote) == 0 {
				// Provably unsent — the next candidate may try.
				if !last {
					tried = append(tried, fmt.Sprintf("%s (%s)", conn.URI, truncateRunes(ferr.Error(), 120)))
					continue
				}
				hint := ""
				if len(tried) > 0 {
					hint = "Also tried: " + strings.Join(tried, "; ")
				}
				return &Error{Message: fmt.Sprintf("%s upload failed (%s)", noun, ferr.Error()), Endpoint: endpoint, Hint: hint}
			}
			// The request may have reached the server; its outcome is
			// unknown and the upload is NEVER replayed on another
			// connection.
			return &Error{Message: "Write outcome unknown: " + ferr.Error(), Endpoint: endpoint, Hint: "The upload may have reached the server on " + conn.URI + ". Check the item's " + strings.ToLower(noun) + " before retrying.", UnknownOutcome: true}
		}
		c.rememberGoodConn(s, conn.URI)
		status := res.StatusCode
		text, _ := io.ReadAll(res.Body)
		res.Body.Close()
		cancel()
		if status < 200 || status > 299 {
			msg := fmt.Sprintf("%s upload returned HTTP %d", noun, status)
			if len(text) > 0 {
				msg += ": " + truncateRunes(string(text), 200)
			}
			return &Error{Message: msg, Status: status, Endpoint: endpoint}
		}
		return nil
	}
	// Unreachable: the last candidate always returns. Kept so the
	// compiler sees a return on every path if the loop ever changes.
	return &Error{Message: noun + " upload failed (no connection attempted)", Endpoint: endpoint}
}

// sniffPosterContentType labels an upload by its actual image bytes,
// not by the caller's assumption: overlay composites are PNG by
// construction, but restore-defaults re-uploads the agent's own
// poster bytes, which are whatever the agent served (JPEG, WebP, …).
// Unknown bytes keep the historical PNG label — Plex sniffs the
// content itself on ingest.
func sniffPosterContentType(b []byte) (contentType, ext string) {
	switch {
	case len(b) >= 4 && b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G':
		return "image/png", "png"
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg", "jpg"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp", "webp"
	case len(b) >= 4 && string(b[:4]) == "GIF8":
		return "image/gif", "gif"
	case len(b) >= 8 && string(b[4:8]) == "ftyp":
		return "image/avif", "avif"
	}
	return "image/png", "png"
}

// randomBoundarySuffix returns a short random suffix for the multipart
// boundary (JS used Math.random().toString(36).slice(2, 10); hex is the Go
// equivalent for an opaque boundary token).
func randomBoundarySuffix() string {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// DeleteCollection ALWAYS returns the guard error. Deletion is DISABLED in
// this app — permanent product boundary (see AGENTS.md). Deleting a
// collection was never ordered; the v0.4 builder briefly enabled
// it and it was reverted in review (2026-10-19). Kept as a
// rejecting compatibility stub: old callers cannot delete. It performs NO
// network I/O.
func (c *Client) DeleteCollection(ctx context.Context, s *Server, ratingKey string) error {
	return &Error{Message: "Safety guard: deletion is disabled in this app"}
}
