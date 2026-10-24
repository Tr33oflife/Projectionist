package api

// handlers_postergen.go — the collection poster GENERATOR (v1.20).
//
// One endpoint: postergen/preview. It resolves the member set (a
// collection's members in order, or an explicit member-key list from
// a Builder draft), fetches the members' posters through the same
// connection-walking raw fetch the overlay engine uses, renders one
// of the three templates server-side (internal/postergen), and
// registers the render as a collection-art asset. From that moment
// the generated poster is an ordinary previewed asset: the manager
// applies it with plex/setPoster and the Builder selects it in the
// art picker — both through the existing verified backup gate. This
// handler writes NOTHING to Plex; preview is render-only.
//
// Honesty rules (mirrored in postergen.Result and shown verbatim):
// members whose posters cannot be fetched are skipped and counted;
// the flower/spiral caps name themselves; a mosaic that cannot fill
// its grid fails with the shortfall instead of rendering gaps.

import (
	"context"
	"fmt"
	"image"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"

	"plexcollectionmanager/internal/plex"
	"plexcollectionmanager/internal/postergen"
)

// maxGeneratorMembers bounds one preview's fetch fan-out. The
// layouts cap at 169 visible posters; 400 leaves room for skips
// while keeping a pathological selection from fetching forever.
const maxGeneratorMembers = 400

type genBackgroundIn struct {
	Kind      string `json:"kind"`      // member | asset | url | derived
	RatingKey string `json:"ratingKey"` // member
	UseArt    bool   `json:"useArt"`    // member: backdrop instead of poster
	AssetID   string `json:"assetId"`   // asset
	URL       string `json:"url"`       // url
}

type genPreviewIn struct {
	RatingKey     string          `json:"ratingKey"`  // collection mode
	MemberKeys    []string        `json:"memberKeys"` // draft mode / explicit selection
	Title         string          `json:"title"`
	Template      string          `json:"template"`
	Grid          string          `json:"grid"`
	Plate         bool            `json:"plate"`
	Scale         *float64        `json:"scale"` // mosaic poster zoom; absent = 1
	TitleSize     int             `json:"titleSize"`
	TitlePosition string          `json:"titlePosition"`
	Background    genBackgroundIn `json:"background"`
	Blur          int             `json:"blur"`
	Darken        int             `json:"darken"`
	Gradient      int             `json:"gradient"`
	FocalX        *float64        `json:"focalX"`
	FocalY        *float64        `json:"focalY"`
}

func (s *Server) handlePosterGenPreview(w http.ResponseWriter, r *http.Request) {
	var body genPreviewIn
	if err := decodeBody(r, &body); err != nil {
		failErr(w, err)
		return
	}
	server, err := s.requireServer()
	if err != nil {
		failErr(w, err)
		return
	}
	ctx := r.Context()

	// ---- resolve the member set ------------------------------------
	title := strings.TrimSpace(body.Title)
	var keys []string
	memberTotal := 0
	if body.RatingKey != "" {
		// TTL-cached across the burst of renders one editing
		// session produces (v1.21.1, postergen_cache.go).
		collTitle, items, err := s.previewCollection(ctx, server, body.RatingKey)
		if err != nil {
			failErr(w, err)
			return
		}
		if title == "" {
			title = collTitle
		}
		memberTotal = len(items)
		if len(body.MemberKeys) > 0 {
			keys = body.MemberKeys
		} else {
			for _, it := range items {
				keys = append(keys, it.RatingKey)
			}
		}
	} else {
		keys = body.MemberKeys
		memberTotal = len(keys)
	}
	notes := []string{}
	if len(keys) > maxGeneratorMembers {
		keys = keys[:maxGeneratorMembers]
		notes = append(notes, fmt.Sprintf("Only the first %d members were considered for this render.", maxGeneratorMembers))
	}

	// ---- member records (thumb / art paths) -------------------------
	var records map[string]map[string]any
	if len(keys) > 0 {
		records, err = s.previewRecords(ctx, server, keys)
		if err != nil {
			failErr(w, err)
			return
		}
	} else {
		records = map[string]map[string]any{}
	}
	str := func(m map[string]any, key string) string {
		v, _ := m[key].(string)
		return v
	}

	// ---- fetch + decode posters (bounded fan-out) -------------------
	type fetched struct {
		poster image.Image
	}
	results := make([]fetched, len(keys))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	fetchOne := func(idx int, path string) {
		defer wg.Done()
		sem <- struct{}{}
		defer func() { <-sem }()
		// Image-cache backed (v1.21.1): a repeat render in the
		// same editing burst performs no download at all.
		results[idx].poster = s.fetchPreviewImage(ctx, server, path, false)
	}
	for i, k := range keys {
		rec := records[k]
		if rec == nil {
			continue
		}
		if thumb := str(rec, "thumb"); thumb != "" {
			wg.Add(1)
			go fetchOne(i, thumb)
		}
	}
	wg.Wait()

	members := make([]postergen.MemberImage, len(keys))
	for i, k := range keys {
		m := postergen.MemberImage{Key: k}
		if rec := records[k]; rec != nil {
			m.Title = str(rec, "title")
		}
		m.Poster = results[i].poster
		members[i] = m
	}

	// ---- background ---------------------------------------------------
	bg := postergen.Background{Kind: body.Background.Kind, MemberKey: body.Background.RatingKey, UseArt: body.Background.UseArt}
	switch body.Background.Kind {
	case "asset":
		asset, found := s.art.GetAsset(body.Background.AssetID)
		if !found || asset == nil {
			failMsg(w, "That background image is no longer cached.", "Upload or preview it again, then re-render.")
			return
		}
		img, err := postergen.DecodeImage(asset.Bytes)
		if err != nil {
			failErr(w, err)
			return
		}
		bg.Kind = "image"
		bg.Image = img
	case "url":
		img, err := postergen.FetchImageURL(ctx, body.Background.URL)
		if err != nil {
			failErr(w, err)
			return
		}
		bg.Kind = "image"
		bg.Image = img
	case "member":
		// The background member wants FULL-resolution art (the grid
		// copies are downscaled) — fetch its poster/backdrop path
		// directly and hand it over as the member's full image.
		for i, k := range keys {
			if k != body.Background.RatingKey {
				continue
			}
			rec := records[k]
			if rec == nil {
				break
			}
			path := str(rec, "thumb")
			if body.Background.UseArt && str(rec, "art") != "" {
				path = str(rec, "art")
			}
			if img := s.fetchPreviewImage(ctx, server, path, true); img != nil {
				if body.Background.UseArt {
					members[i].Art = img
				} else {
					members[i].Poster = img // full-size replaces the grid copy
				}
			}
			break
		}
	case "", "derived":
		bg.Kind = "derived"
	default:
		failMsg(w, "Unknown background kind "+body.Background.Kind, "")
		return
	}

	// ---- render -------------------------------------------------------
	fx, fy := 0.5, 0.5
	if body.FocalX != nil {
		fx = *body.FocalX
	}
	if body.FocalY != nil {
		fy = *body.FocalY
	}
	// Mosaic poster zoom: absent = 1 (today's cover-fill); the UI
	// slider sends 0.5–2. Clamp rather than reject — a stray value
	// should still render, at the nearest sane zoom.
	scale := 1.0
	if body.Scale != nil {
		scale = *body.Scale
		if scale < 0.5 {
			scale = 0.5
		}
		if scale > 2 {
			scale = 2
		}
	}
	spec := postergen.Spec{
		Template:      body.Template,
		Title:         title,
		Grid:          body.Grid,
		Plate:         body.Plate,
		Scale:         scale,
		TitleSize:     body.TitleSize,
		TitlePosition: body.TitlePosition,
		Background:    bg,
		Blur:          clampPct(body.Blur),
		Darken:        clampPct(body.Darken),
		Gradient:      clampPct(body.Gradient),
		FocalX:        fx,
		FocalY:        fy,
	}
	res, err := postergen.Render(spec, members)
	if err != nil {
		failErr(w, err)
		return
	}
	label := "Generated poster"
	if title != "" {
		label = "Generated poster — " + title
	}
	art, err := s.art.ImportRendered(res.PNG, label)
	if err != nil {
		failErr(w, err)
		return
	}
	allNotes := append(notes, res.Notes...)
	ok(w, envelope{
		"art": art,
		"stats": envelope{
			"usedMembers":     res.UsedMembers,
			"skippedPosters":  res.SkippedPosters,
			"selectedMembers": res.SelectedMembers,
			"memberTotal":     memberTotal,
			"capApplied":      res.CapApplied,
			"capLimit":        res.CapLimit,
			"notes":           allNotes,
		},
	})
}

// fetchMemberImage fetches one Plex image path (connection-walked,
// token in header) and decodes it. Any failure returns nil — the
// caller counts it as a skipped poster, never a hard error: one dead
// poster must not sink a 100-member render.
func (s *Server) fetchMemberImage(ctx context.Context, server *plex.Server, path string) image.Image {
	if path == "" {
		return nil
	}
	body, _, status, err := s.plexClient.FetchServed(ctx, server, path, 25*time.Second)
	if err != nil || status < 200 || status >= 300 || len(body) == 0 {
		return nil
	}
	img, err := postergen.DecodeImage(body)
	if err != nil {
		return nil
	}
	return img
}

// downscaleTo shrinks img so its longest side is at most maxDim
// (mosaic/spiral cells never display larger; hero backgrounds are
// fetched separately at full size).
func downscaleTo(img image.Image, maxDim int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxDim && h <= maxDim {
		return img
	}
	scale := float64(maxDim) / float64(max(w, h))
	nw, nh := int(float64(w)*scale), int(float64(h)*scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	out := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.ApproxBiLinear.Scale(out, out.Bounds(), img, b, draw.Src, nil)
	return out
}

func clampPct(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
