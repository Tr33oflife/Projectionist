package api

// handlers_collections_manager.go — the collections MANAGER (v1.13).
//
// This deepens the Collections surface without touching what already
// works: creation, membership (add-before-remove, preserve-on-
// failure), ordering, and the TPDb art picker stay exactly as they
// were. What the manager adds:
//
//   - plex/collectionHealth    — every collection in a library with
//     health flags computed from real reads (member count, poster /
//     backdrop / summary presence, duplicate titles), each flag's
//     basis carried as counts the UI shows next to the flags.
//   - plex/getCollectionDetail — one collection's full editable
//     metadata as the server actually exposes it (fields the server
//     doesn't return stay absent — labels are a pointer for exactly
//     this reason).
//   - plex/updateCollectionMeta — ONE write through the same
//     section-edit mechanics as everything else, then a VERIFIED
//     re-read: the response lists, per field sent, what the server
//     actually holds afterwards. A mismatch is reported in the
//     result, never hidden behind a success toast.
//   - plex/collectionBulkArt   — best-match TPDb art for many
//     collections at once (poster and/or backdrop). Every write is
//     verified by read-back (the served thumb/art path must change)
//     and the result reports per collection, per target — no silent
//     partial success.
//   - plex/setCollectionPosterURL — poster from a custom image URL
//     for ONE collection, through the existing poster-ingest path
//     (backup gate first, read-back after).
//
// The deletion boundary is unchanged and total: nothing here deletes
// a collection or any media — the client still refuses both, and the
// health view's remedy for an empty collection is membership and
// metadata management, never removal.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"plexcollectionmanager/internal/collectionart"
	"plexcollectionmanager/internal/overlays"
	"plexcollectionmanager/internal/plex"
)

// ---- health -----------------------------------------------------------------

type healthEntry struct {
	plex.Collection
	Flags []string `json:"flags"`
}

func (s *Server) handleCollectionHealth(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SectionKey string `json:"sectionKey"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	cols, err := s.plexClient.GetCollections(r.Context(), server, body.SectionKey)
	if err != nil {
		failErr(w, err)
		return
	}
	counts := plex.TitleCounts(cols)
	entries := make([]healthEntry, 0, len(cols))
	for _, c := range cols {
		entries = append(entries, healthEntry{Collection: c, Flags: plex.CollectionFlags(c, counts)})
	}
	ok(w, envelope{
		"collections": entries,
		"flagCounts":  plex.HealthFlagCounts(cols),
		"total":       len(cols),
	})
}

// ---- detail -----------------------------------------------------------------

func (s *Server) handleGetCollectionDetail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RatingKey string `json:"ratingKey"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	detail, err := s.plexClient.GetCollectionDetail(r.Context(), server, body.RatingKey)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, envelope{"detail": detail})
}

// ---- verified metadata save ---------------------------------------------------

// fieldCheck is one line of the verified-save report: what was sent,
// what the server holds after the write, and whether they agree.
type fieldCheck struct {
	Field string `json:"field"`
	Sent  any    `json:"sent"`
	Held  any    `json:"held"`
	OK    bool   `json:"ok"`
	Note  string `json:"note,omitempty"`
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string{}, a...)
	bs := append([]string{}, b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

func (s *Server) handleUpdateCollectionMeta(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RatingKey string                     `json:"ratingKey"`
		Fields    map[string]json.RawMessage `json:"fields"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	var fields plex.MetaFields
	strField := func(key string) *string {
		raw, present := body.Fields[key]
		if !present || string(raw) == "null" {
			return nil
		}
		v := flexString(raw)
		return &v
	}
	boolField := func(key string) *bool {
		raw, present := body.Fields[key]
		if !present || string(raw) == "null" {
			return nil
		}
		var v bool
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil
		}
		return &v
	}
	fields.Title = strField("title")
	fields.TitleSort = strField("titleSort")
	fields.Summary = strField("summary")
	fields.ContentRating = strField("contentRating")
	if raw, present := body.Fields["labels"]; present && string(raw) != "null" {
		var labels []string
		if err := json.Unmarshal(raw, &labels); err == nil {
			fields.Labels = &labels
		}
	}
	fields.VisHome = boolField("visHome")
	fields.VisShared = boolField("visShared")
	fields.VisRecommended = boolField("visRecommended")

	if err := s.plexClient.UpdateCollectionMeta(r.Context(), server, body.RatingKey, fields); err != nil {
		failErr(w, err)
		return
	}
	// THE VERIFIED SAVE: re-read and report what the server holds.
	detail, err := s.plexClient.GetCollectionDetail(r.Context(), server, body.RatingKey)
	if err != nil {
		// The write went out but the proof read failed — say exactly
		// that instead of claiming success.
		failMsg(w, "The edit was sent, but the verification re-read failed: "+err.Error(),
			"Reopen the collection to see what the server holds before retrying.")
		return
	}
	checks := []fieldCheck{}
	if fields.Title != nil {
		checks = append(checks, fieldCheck{Field: "title", Sent: *fields.Title, Held: detail.Title, OK: detail.Title == *fields.Title})
	}
	if fields.TitleSort != nil {
		chk := fieldCheck{Field: "titleSort", Sent: *fields.TitleSort, Held: detail.TitleSort, OK: detail.TitleSort == *fields.TitleSort}
		if !chk.OK {
			chk.Note = "The server did not keep the sort title that was sent."
		}
		checks = append(checks, chk)
	}
	if fields.Summary != nil {
		checks = append(checks, fieldCheck{Field: "summary", Sent: *fields.Summary, Held: detail.Summary, OK: detail.Summary == *fields.Summary})
	}
	if fields.ContentRating != nil {
		checks = append(checks, fieldCheck{Field: "contentRating", Sent: *fields.ContentRating, Held: detail.ContentRating, OK: detail.ContentRating == *fields.ContentRating})
	}
	if fields.Labels != nil {
		chk := fieldCheck{Field: "labels", Sent: *fields.Labels}
		if detail.Labels == nil {
			chk.Held = nil
			chk.OK = false
			chk.Note = "This server does not expose collection labels, so the labels write cannot be confirmed — on current Plex versions the label edit is not taken for collections."
		} else {
			chk.Held = *detail.Labels
			chk.OK = sameStringSet(*fields.Labels, *detail.Labels)
			if !chk.OK {
				chk.Note = "The server holds different labels than the ones sent."
			}
		}
		checks = append(checks, chk)
	}
	if fields.VisHome != nil || fields.VisShared != nil || fields.VisRecommended != nil {
		chk := fieldCheck{Field: "visibility", Held: detail.Visibility}
		sent := map[string]any{}
		if fields.VisHome != nil {
			sent["visHome"] = *fields.VisHome
		}
		if fields.VisShared != nil {
			sent["visShared"] = *fields.VisShared
		}
		if fields.VisRecommended != nil {
			sent["visRecommended"] = *fields.VisRecommended
		}
		chk.Sent = sent
		if !detail.VisibilityKnown {
			chk.OK = false
			chk.Note = detail.VisibilityNote
		} else {
			want := "library"
			if fields.VisHome != nil && *fields.VisHome {
				want = "home"
			} else if fields.VisShared != nil && *fields.VisShared {
				want = "shared"
			}
			chk.OK = detail.Visibility == want
			if !chk.OK {
				chk.Note = "The server reports a different visibility than the one requested."
			}
		}
		checks = append(checks, chk)
	}
	ok(w, envelope{"detail": detail, "checks": checks})
}

// ---- bulk art -----------------------------------------------------------------

// bestBulkAsset resolves a collection title to its best TPDb art
// match: the first suggestion, previewed (validated + decoded +
// cached) exactly like the single-collection picker flow. It is a
// Server field so tests can substitute a fixture source — production
// always uses the real flow.
func (s *Server) bestBulkAsset(ctx context.Context, title string) (collectionart.Asset, error) {
	res := s.art.Suggestions(title)
	items, _ := res["items"].([]map[string]any)
	if len(items) == 0 {
		return collectionart.Asset{}, fmt.Errorf("no TPDb art match for %q", title)
	}
	prev, err := s.art.Preview(ctx, items[0])
	if err != nil {
		return collectionart.Asset{}, err
	}
	assetID, _ := prev["assetId"].(string)
	asset, found := s.art.GetAsset(assetID)
	if !found || asset == nil {
		return collectionart.Asset{}, fmt.Errorf("the previewed art for %q is no longer cached", title)
	}
	return *asset, nil
}

type bulkTargetResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type bulkArtResult struct {
	RatingKey string            `json:"ratingKey"`
	Title     string            `json:"title"`
	Poster    *bulkTargetResult `json:"poster,omitempty"`
	Backdrop  *bulkTargetResult `json:"backdrop,omitempty"`
}

func (s *Server) handleCollectionBulkArt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RatingKeys []string `json:"ratingKeys"`
		Poster     bool     `json:"poster"`
		Backdrop   bool     `json:"backdrop"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	if len(body.RatingKeys) == 0 {
		failMsg(w, "No collections selected.", "Tick at least one collection in the health list first.")
		return
	}
	if !body.Poster && !body.Backdrop {
		failMsg(w, "Nothing to apply.", "Choose poster, backdrop, or both.")
		return
	}
	resolve := s.bulkArtAsset
	if resolve == nil {
		resolve = s.bestBulkAsset
	}
	results := make([]bulkArtResult, 0, len(body.RatingKeys))
	for _, rk := range body.RatingKeys {
		results = append(results, s.bulkArtOne(r.Context(), server, rk, body.Poster, body.Backdrop, resolve))
	}
	ok(w, envelope{"results": results})
}

// bulkArtOne applies the art flow to ONE collection and verifies each
// requested target by read-back. A failure at any step is recorded on
// the affected target(s) with its reason; other collections in the
// bulk run are unaffected.
func (s *Server) bulkArtOne(ctx context.Context, server *plex.Server, ratingKey string, wantPoster, wantBackdrop bool, resolve func(context.Context, string) (collectionart.Asset, error)) bulkArtResult {
	res := bulkArtResult{RatingKey: ratingKey}
	failBoth := func(err error) bulkArtResult {
		if wantPoster {
			res.Poster = &bulkTargetResult{Error: err.Error()}
		}
		if wantBackdrop {
			res.Backdrop = &bulkTargetResult{Error: err.Error()}
		}
		return res
	}
	// The detail read (not GetItem) because the backdrop read-back
	// needs the record's art path, which the slim item shape lacks.
	item, err := s.plexClient.GetCollectionDetail(ctx, server, ratingKey)
	if err != nil {
		return failBoth(err)
	}
	res.Title = item.Title
	// The backup gate covers BOTH writes (poster upload and backdrop
	// upload alike refuse to run without a verified backup of the
	// collection's current poster).
	if _, err := s.overlay.BackupCurrentPoster(ctx, server, ratingKey, item.Title, item.Thumb); err != nil {
		return failBoth(fmt.Errorf("backup gate: %w", err))
	}
	asset, err := resolve(ctx, item.Title)
	if err != nil {
		return failBoth(err)
	}
	if wantPoster {
		res.Poster = &bulkTargetResult{}
		badgeSet, _ := json.Marshal(map[string]any{"name": "Collection art (bulk): " + asset.Source, "placements": []any{}})
		target, _ := json.Marshal(map[string]any{"type": "collection-art-bulk", "source": asset.Source, "sourceUrl": asset.URL})
		record, aerr := s.overlay.RunApplyJob(ctx, overlays.ApplyRequest{
			Server: server,
			Jobs: []overlays.Job{{
				RatingKey: ratingKey, Title: item.Title, Thumb: item.Thumb,
				PNGBase64: base64.StdEncoding.EncodeToString(asset.Bytes),
			}},
			BadgeSet: badgeSet,
			Target:   target,
		})
		switch {
		case aerr != nil:
			res.Poster.Error = aerr.Error()
		case record != nil && record.FailCount > 0 && len(record.Items) > 0:
			res.Poster.Error = record.Items[0].Error
		default:
			after, gerr := s.plexClient.GetCollectionDetail(ctx, server, ratingKey)
			switch {
			case gerr != nil:
				res.Poster.Error = "uploaded, but the verification re-read failed: " + gerr.Error()
			case after.Thumb == item.Thumb:
				res.Poster.Error = "the served poster did not change after the upload"
			default:
				res.Poster.OK = true
				s.invalidateArt(server.Machine, item.Thumb)
			}
		}
	}
	if wantBackdrop {
		res.Backdrop = &bulkTargetResult{}
		if uerr := s.plexClient.UploadArtBytes(ctx, server, ratingKey, asset.Bytes); uerr != nil {
			res.Backdrop.Error = uerr.Error()
		} else {
			after, gerr := s.plexClient.GetCollectionDetail(ctx, server, ratingKey)
			switch {
			case gerr != nil:
				res.Backdrop.Error = "uploaded, but the verification re-read failed: " + gerr.Error()
			case after.Art == item.Art:
				res.Backdrop.Error = "the served backdrop did not change after the upload"
			default:
				res.Backdrop.OK = true
				s.invalidateArt(server.Machine, item.Art)
			}
		}
	}
	return res
}

// ---- single-collection poster from URL ---------------------------------------

func (s *Server) handleSetCollectionPosterURL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RatingKey string `json:"ratingKey"`
		URL       string `json:"url"`
	}
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	body.URL = strings.TrimSpace(body.URL)
	if body.URL == "" {
		failMsg(w, "No image URL given.", "Paste an http(s) image address first.")
		return
	}
	item, err := s.plexClient.GetItem(r.Context(), server, body.RatingKey)
	if err != nil {
		failErr(w, err)
		return
	}
	// Same gate as every poster write: a verified backup of the
	// current poster exists before anything changes.
	if _, err := s.overlay.BackupCurrentPoster(r.Context(), server, body.RatingKey, item.Title, item.Thumb); err != nil {
		failErr(w, err)
		return
	}
	if err := s.plexClient.SetPosterFromURL(r.Context(), server, body.RatingKey, body.URL); err != nil {
		failErr(w, err)
		return
	}
	after, err := s.plexClient.GetItem(r.Context(), server, body.RatingKey)
	if err != nil {
		failMsg(w, "The poster URL was sent, but the verification re-read failed: "+err.Error(), "")
		return
	}
	changed := after.Thumb != item.Thumb
	if changed {
		s.invalidateArt(server.Machine, item.Thumb)
	}
	ok(w, envelope{"changed": changed, "thumbBefore": item.Thumb, "thumbAfter": after.Thumb})
}
