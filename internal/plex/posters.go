package plex

// posters.go — the poster-list reads behind the Apply view's
// "Restore default Plex posters" operation.
//
// Facts established live against the sandbox PMS 1.43 (2026-10-22):
//   - GET /library/metadata/{id}/posters returns Photo entries with key,
//     ratingKey, thumb, selected — and a provider field ONLY when the
//     poster came from a metadata agent. Local posters carry no provider
//     field at all; their source is the ratingKey URI scheme instead
//     (upload://…, media://…, metadata://…).
//   - POST /library/metadata/{id}/posters?url=… is the INGEST route
//     (Plex's RemoteAssetFetcher): it refuses non-http(s) values with
//     HTTP 400 ("not an http or https URL" in the PMS log) and, when
//     handed a form-body url, downloads the image and stores it as a
//     NEW upload:// poster. It never selects an existing entry.
//   - An item's thumb path ends in its updatedAt. UPLOADING a poster
//     bumps updatedAt (new thumb URL → fresh transcoder renditions).
//     SELECTING an existing entry (the section-edit thumb form, or
//     the item-level PUT — both measured) does NOT: the raw bytes at
//     the thumb path follow the selection, but clients fetch posters
//     through the photo transcoder keyed on the unchanged URL, whose
//     rendition stays frozen at the old poster. That is why
//     restore-defaults re-uploads the provider poster's own bytes
//     instead of selecting its entry: same picture, new address.
//     (The selection write was removed with that finding — nothing
//     in the app selects posters anymore.)

import (
	"context"
	"strings"
)

// PosterEntry is one entry of an item's posters list, normalised: the
// provider falls back to the ratingKey URI scheme when Plex omits the
// provider field (local posters), so IsUpload is always decidable.
type PosterEntry struct {
	Key       string `json:"key"`
	RatingKey string `json:"ratingKey"`
	Provider  string `json:"provider"`
	Selected  bool   `json:"selected"`
	// Thumb is the entry's own thumb address as Plex reports it. For
	// posters whose Key is an external (agent CDN) URL this is
	// typically a server-local /library/metadata/{id}/file?url=…
	// path through which Plex itself serves the image — the route
	// the overlays fetcher prefers for such entries, because Plex
	// retrieves its own agent posters regardless of how the CDN
	// treats other clients (see fetchEntryPosterBytes there).
	Thumb string `json:"thumb,omitempty"`
}

// IsUpload reports whether this poster is an uploaded image. Overlays —
// this app's or any other tool's — only ever exist as uploads; provider
// posters are never composited in place, so a non-upload poster is a
// clean poster by construction.
func (p PosterEntry) IsUpload() bool {
	return p.Provider == "upload" || strings.HasPrefix(p.RatingKey, "upload://")
}

// ProviderName is the entry's provider, falling back to the ratingKey
// URI scheme for entries parsed outside GetPosters (which fills
// Provider itself).
func (p PosterEntry) ProviderName() string {
	if p.Provider != "" {
		return p.Provider
	}
	if i := strings.Index(p.RatingKey, "://"); i > 0 {
		return p.RatingKey[:i]
	}
	return ""
}

// GetPosters returns the item's full posters list
// (GET /library/metadata/{id}/posters), in server order.
func (c *Client) GetPosters(ctx context.Context, s *Server, ratingKey string) ([]PosterEntry, error) {
	data, err := c.serverFetch(ctx, s, "/library/metadata/"+encodeURIComponent(ratingKey)+"/posters", fetchOptions{method: "GET"})
	if err != nil {
		return nil, err
	}
	cont := parseContainer(data)
	out := []PosterEntry{}
	for _, m := range cont.metadata {
		rk := stringField(m, "ratingKey")
		provider := stringField(m, "provider")
		if provider == "" {
			// Local posters carry no provider field; the ratingKey
			// scheme is the source (upload://, media://, metadata://).
			if i := strings.Index(rk, "://"); i > 0 {
				provider = rk[:i]
			}
		}
		out = append(out, PosterEntry{
			Key:       stringField(m, "key"),
			RatingKey: rk,
			Provider:  provider,
			Selected:  jsTruthy(m["selected"]),
			Thumb:     stringField(m, "thumb"),
		})
	}
	return out, nil
}

// PosterClassification is the restore-default verdict for one item.
// Status is one of:
//   - "restore": the item is currently showing an uploaded image and a
//     provider (non-upload) poster exists to switch to.
//   - "already-default": the selected poster is already a provider
//     poster — clean, nothing to do. (This pure verdict is refined
//     by the overlays layer: when ANY upload entry coexists, a
//     provider selection alone does not prove clients DISPLAY the
//     clean poster, and the verdict flips to "restore" — see
//     refineProviderSelectedWithUploads in the overlays package.)
//   - "no-provider": no provider poster exists (empty list, or uploads
//     only) — there is nothing clean to switch to.
type PosterClassification struct {
	Status            string `json:"status"`
	DefaultRatingKey  string `json:"defaultRatingKey,omitempty"`
	DefaultKey        string `json:"defaultKey,omitempty"`
	DefaultProvider   string `json:"defaultProvider,omitempty"`
	SelectedRatingKey string `json:"selectedRatingKey,omitempty"`
	SelectedIsUpload  bool   `json:"selectedIsUpload"`
	PosterCount       int    `json:"posterCount"`
}

// ClassifyPosters is the pure classification over a posters list. The
// DEFAULT is the first non-upload entry, in server order.
func ClassifyPosters(entries []PosterEntry) PosterClassification {
	out := PosterClassification{PosterCount: len(entries)}
	var def *PosterEntry
	for i := range entries {
		e := &entries[i]
		if def == nil && !e.IsUpload() {
			def = e
		}
		if e.Selected {
			out.SelectedRatingKey = e.RatingKey
			out.SelectedIsUpload = e.IsUpload()
		}
	}
	if def == nil {
		out.Status = "no-provider"
		return out
	}
	out.DefaultRatingKey = def.RatingKey
	out.DefaultKey = def.Key
	out.DefaultProvider = def.ProviderName()
	if out.SelectedRatingKey != "" && !out.SelectedIsUpload {
		// Already showing a provider poster — clean by construction,
		// whichever provider entry it is.
		out.Status = "already-default"
		return out
	}
	out.Status = "restore"
	return out
}
