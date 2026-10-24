package postergen

// mosaic.go — the Mosaic template: fixed grids (2×2, 3×3), the
// Flower (every selected member as a center poster + radiating
// petal rings), and the Spiral (every selected member along an
// expanding spiral, shrinking as it winds outward). All layouts are
// deterministic functions of the member count — same input, same
// poster, on every machine.

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"

	"golang.org/x/image/draw"
)

// SpiralCap is the most posters the spiral layout promises to
// place. The layout simulation (spiralLayout) fits at least this
// many at its tightest density with posters still ≥ ~34px tall;
// beyond the cap the render says so instead of silently dropping
// members (Result.CapApplied, same rule as Flower).
const SpiralCap = 169

const plateHeight = 216

// renderMosaic draws the mosaic into canvas. sel is the ordered
// selection; res collects the honesty counters.
func renderMosaic(canvas *image.RGBA, spec Spec, sel []MemberImage, res *Result) error {
	title := strings.TrimSpace(spec.Title)
	plate := spec.Plate && title != ""
	full := canvas.Bounds()
	verticalGradient(canvas, full, color.RGBA{0x11, 0x11, 0x15, 255}, color.RGBA{0x07, 0x07, 0x09, 255})
	area := full
	if plate {
		area.Max.Y -= plateHeight
	}
	avail := availablePosters(sel)
	// Poster zoom: 0 (absent) means 1 — cover-fill, today's render.
	scale := spec.Scale
	if scale <= 0 {
		scale = 1
	}
	switch spec.Grid {
	case "2x2", "3x3":
		n := 2
		if spec.Grid == "3x3" {
			n = 3
		}
		need := n * n
		if len(avail) < need {
			return fmt.Errorf("a %s mosaic needs %d posters, but only %d of the %d selected members' posters could be fetched — add members, or check the ones that failed",
				spec.Grid, need, len(avail), len(sel))
		}
		drawGrid(canvas, area, avail[:need], n, scale)
		res.UsedMembers = need
		if len(avail) > need {
			res.Notes = append(res.Notes, fmt.Sprintf("The %s grid shows the first %d of %d selected posters — reorder or trim the member list to choose which.",
				spec.Grid, need, len(avail)))
		}
	case "flower":
		if len(avail) == 0 {
			return fmt.Errorf("the Flower layout needs at least one member poster — this selection has none that could be fetched")
		}
		use := avail
		if len(use) > FlowerCap {
			use = use[:FlowerCap]
			res.CapApplied = true
			res.CapLimit = FlowerCap
			res.Notes = append(res.Notes, fmt.Sprintf("Flower shows the first %d of %d selected posters — past %d the outer petals shrink below poster size.",
				FlowerCap, len(avail), FlowerCap))
		}
		for _, p := range flowerLayout(len(use), area) {
			drawPetal(canvas, use[p.index].Poster, p.cx, p.cy, p.r*scale)
		}
		res.UsedMembers = len(use)
	case "spiral":
		if len(avail) == 0 {
			return fmt.Errorf("the Spiral layout needs at least one member poster — this selection has none that could be fetched")
		}
		use := avail
		if len(use) > SpiralCap {
			use = use[:SpiralCap]
			res.CapApplied = true
			res.CapLimit = SpiralCap
		}
		placements := spiralLayout(len(use), area)
		if len(placements) < len(use) {
			res.CapApplied = true
			res.CapLimit = len(placements)
			res.Notes = append(res.Notes, fmt.Sprintf("The spiral fits %d posters at readable size — %d selected members beyond that aren't shown.",
				len(placements), len(use)-len(placements)))
		} else if res.CapApplied {
			res.Notes = append(res.Notes, fmt.Sprintf("Spiral shows the first %d of %d selected posters — past %d the winding posters shrink below readable size.",
				SpiralCap, len(avail), SpiralCap))
		}
		for _, p := range placements {
			sp := p
			sp.w *= scale
			sp.h *= scale
			drawSpiralPoster(canvas, use[p.index].Poster, sp)
		}
		res.UsedMembers = len(placements)
	default:
		return fmt.Errorf("unknown mosaic layout %q", spec.Grid)
	}
	if plate {
		drawPlate(canvas, title)
	}
	return nil
}

// drawGrid paints an n×n cover-fit grid of posters into area with a
// fixed gutter. Each poster is scaled within its cell around the
// cell center by scale (1 = cover-fill).
func drawGrid(canvas *image.RGBA, area image.Rectangle, posters []MemberImage, n int, scale float64) {
	const margin = 14
	const gutter = 10
	w := area.Dx() - 2*margin - (n-1)*gutter
	h := area.Dy() - 2*margin - (n-1)*gutter
	cw, ch := w/n, h/n
	for i, m := range posters {
		col, row := i%n, i/n
		x := area.Min.X + margin + col*(cw+gutter)
		y := area.Min.Y + margin + row*(ch+gutter)
		drawScaledCover(canvas, m.Poster, image.Rect(x, y, x+cw, y+ch), scale)
	}
}

// drawScaledCover draws src into cell scaled by scale around the
// cell's center. scale == 1 takes the exact drawCover path (the
// pre-zoom render, byte for byte). Otherwise the cover-fit render
// is made at the scaled size and pasted centered, clipped to the
// cell: >1 shows the cell-sized center of a bigger render (tighter
// crop, never bleeding into the gutter or a neighbour), <1 leaves
// the render background showing around a smaller poster.
func drawScaledCover(dst *image.RGBA, src image.Image, cell image.Rectangle, scale float64) {
	if scale == 1 {
		drawCover(dst, src, cell, 0.5, 0.5)
		return
	}
	if scale < 0.1 {
		scale = 0.1
	}
	if scale > 4 {
		scale = 4
	}
	w := int(math.Round(float64(cell.Dx()) * scale))
	h := int(math.Round(float64(cell.Dy()) * scale))
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	temp := image.NewRGBA(image.Rect(0, 0, w, h))
	drawCover(temp, src, temp.Bounds(), 0.5, 0.5)
	// Temp's top-left lands at (ox, oy) in dst space — the cell
	// center minus half the scaled size. Paste the part of temp
	// that falls inside the cell (the whole temp when zoomed out,
	// the centered cell-sized window when zoomed in).
	ox := cell.Min.X + (cell.Dx()-w)/2
	oy := cell.Min.Y + (cell.Dy()-h)/2
	sx0 := clampInt(cell.Min.X-ox, 0, w)
	sy0 := clampInt(cell.Min.Y-oy, 0, h)
	sx1 := clampInt(cell.Max.X-ox, 0, w)
	sy1 := clampInt(cell.Max.Y-oy, 0, h)
	if sx1 <= sx0 || sy1 <= sy0 {
		return
	}
	draw.Draw(dst, image.Rect(ox+sx0, oy+sy0, ox+sx1, oy+sy1), temp, image.Pt(sx0, sy0), draw.Src)
}

// drawPlate paints the title band across the canvas bottom and the
// title inside it.
func drawPlate(canvas *image.RGBA, title string) {
	b := canvas.Bounds()
	band := image.Rect(b.Min.X, b.Max.Y-plateHeight, b.Max.X, b.Max.Y)
	// Translucent near-black band (manual blend — the canvas below is
	// the poster's own bottom edge).
	for y := band.Min.Y; y < band.Max.Y; y++ {
		for x := band.Min.X; x < band.Max.X; x++ {
			off := canvas.PixOffset(x, y)
			canvas.Pix[off] = uint8(float64(canvas.Pix[off]) * 0.16)
			canvas.Pix[off+1] = uint8(float64(canvas.Pix[off+1]) * 0.16)
			canvas.Pix[off+2] = uint8(float64(canvas.Pix[off+2]) * 0.16)
		}
	}
	for x := band.Min.X; x < band.Max.X; x++ {
		canvas.SetRGBA(x, band.Min.Y, color.RGBA{255, 255, 255, 60})
	}
	cx := float64(b.Min.X+b.Max.X) / 2
	cy := float64(band.Min.Y+band.Max.Y) / 2
	_, _ = drawTitleBlock(canvas, title, cx, cy, 900, 92, 2, color.RGBA{245, 245, 248, 255})
}

// ---------------------------------------------------------------------------
// Flower layout
// ---------------------------------------------------------------------------

type discPlacement struct {
	index  int
	cx, cy float64
	r      float64
}

// flowerLayout places n posters: index 0 at the center, the rest in
// rings of 6, 12, 18, … around it. Petal radii shrink per ring by a
// fixed weight curve, the whole flower is scaled to the area, and a
// final pass shrinks any petal that would crowd its ring-neighbours
// (chord limit). Invariants the tests pin: every placement inside
// the area, and no two discs overlap by more than a hair.
func flowerLayout(n int, area image.Rectangle) []discPlacement {
	if n <= 0 {
		return nil
	}
	cx := float64(area.Min.X) + float64(area.Dx())/2
	cy := float64(area.Min.Y) + float64(area.Dy())/2
	maxR := math.Min(float64(area.Dx()), float64(area.Dy()))/2 - 20
	if maxR < 60 {
		maxR = 60
	}
	// Ring membership: ring 0 = center (1), ring k takes up to 6k.
	type ring struct {
		count  int // capacity
		actual int
	}
	rings := []ring{{count: 1, actual: 1}}
	remaining := n - 1
	for k := 1; remaining > 0; k++ {
		take := 6 * k
		if take > remaining {
			take = remaining
		}
		rings = append(rings, ring{count: 6 * k, actual: take})
		remaining -= take
	}
	K := len(rings) - 1
	const gap = 10.0
	weight := func(k int) float64 { return 1 / (1 + 0.30*float64(k)) }
	// Scale: outer radius (unit weights, gaps excluded) → maxR with
	// the K radial gaps (K × gap px) reserved.
	outerUnit := weight(0)
	{
		d := 0.0
		for k := 1; k <= K; k++ {
			d += weight(k-1) + weight(k)
		}
		outerUnit = d + weight(K)
	}
	scale := (maxR - gap*float64(K)) / outerUnit
	radii := make([]float64, K+1)
	for k := 0; k <= K; k++ {
		radii[k] = weight(k) * scale
	}
	// Distances from center, with the chord-limit shrink per ring.
	dists := make([]float64, K+1)
	for k := 1; k <= K; k++ {
		dists[k] = dists[k-1] + radii[k-1] + gap + radii[k]
		if rings[k].actual >= 2 {
			lim := dists[k] * math.Sin(math.Pi/float64(rings[k].actual)) * 0.97
			if radii[k] > lim {
				radii[k] = lim
			}
		}
	}
	out := make([]discPlacement, 0, n)
	out = append(out, discPlacement{index: 0, cx: cx, cy: cy, r: radii[0]})
	idx := 1
	for k := 1; k <= K; k++ {
		offset := -math.Pi/2 + float64(k)*0.35
		for i := 0; i < rings[k].actual; i++ {
			a := offset + float64(i)*(2*math.Pi/float64(rings[k].actual))
			out = append(out, discPlacement{
				index: idx,
				cx:    cx + dists[k]*math.Cos(a),
				cy:    cy + dists[k]*math.Sin(a),
				r:     radii[k],
			})
			idx++
		}
	}
	return out
}

// drawPetal draws one poster as a circle-cropped disc with a thin
// light rim: a slightly larger pale disc behind, then the poster
// masked to its circle with an anti-aliased edge.
func drawPetal(dst *image.RGBA, img image.Image, cx, cy, r float64) {
	fillDisc(dst, cx, cy, r+2.5, color.RGBA{232, 232, 240, 255}, 0.85)
	size := int(math.Ceil(2 * r))
	if size < 2 {
		size = 2
	}
	temp := image.NewRGBA(image.Rect(0, 0, size, size))
	drawCover(temp, img, temp.Bounds(), 0.5, 0.5)
	x0, y0 := int(math.Round(cx-r)), int(math.Round(cy-r))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)+0.5-r, float64(y)+0.5-r
			dist := math.Hypot(dx, dy)
			f := clamp01(r + 0.5 - dist)
			if f <= 0 {
				continue
			}
			so := temp.PixOffset(x, y)
			blendPx(dst, x0+x, y0+y, temp.Pix[so], temp.Pix[so+1], temp.Pix[so+2], f)
		}
	}
}

// fillDisc fills a circle with col at the given strength (edge
// anti-aliased over ~1px).
func fillDisc(dst *image.RGBA, cx, cy, r float64, col color.RGBA, strength float64) {
	x0, x1 := int(math.Floor(cx-r-1)), int(math.Ceil(cx+r+1))
	y0, y1 := int(math.Floor(cy-r-1)), int(math.Ceil(cy+r+1))
	b := dst.Bounds()
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			if !image.Pt(x, y).In(b) {
				continue
			}
			dist := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			f := clamp01(r + 0.5 - dist)
			if f <= 0 {
				continue
			}
			blendPx(dst, x, y, col.R, col.G, col.B, f*strength)
		}
	}
}

// blendPx blends an opaque source pixel over dst at (x, y) with
// coverage f (0..1). Out-of-bounds writes are dropped.
func blendPx(dst *image.RGBA, x, y int, sr, sg, sb uint8, f float64) {
	if !image.Pt(x, y).In(dst.Bounds()) || f <= 0 {
		return
	}
	off := dst.PixOffset(x, y)
	dst.Pix[off] = uint8(float64(sr)*f + float64(dst.Pix[off])*(1-f))
	dst.Pix[off+1] = uint8(float64(sg)*f + float64(dst.Pix[off+1])*(1-f))
	dst.Pix[off+2] = uint8(float64(sb)*f + float64(dst.Pix[off+2])*(1-f))
	dst.Pix[off+3] = 255
}

// ---------------------------------------------------------------------------
// Spiral layout
// ---------------------------------------------------------------------------

type rectPlacement struct {
	index  int
	cx, cy float64
	w, h   float64
}

// spiralLayout places n posters along an Archimedean spiral that
// starts at the center poster (largest) and winds outward, posters
// shrinking as the radius grows — sizes are capped both by a shrink
// curve and by the spiral's own turn spacing, so consecutive turns
// never collide. The density (turn spacing) is the LARGEST from a
// fixed ladder that still fits all n posters; if even the tightest
// density runs out of canvas, the placements it managed are
// returned and the caller reports the shortfall.
func spiralLayout(n int, area image.Rectangle) []rectPlacement {
	if n <= 0 {
		return nil
	}
	cx := float64(area.Min.X) + float64(area.Dx())/2
	cy := float64(area.Min.Y) + float64(area.Dy())/2
	if n == 1 {
		h := math.Min(float64(area.Dy())*0.8, float64(area.Dx())*1.28)
		w := h * 2 / 3
		return []rectPlacement{{index: 0, cx: cx, cy: cy, w: w, h: h}}
	}
	for _, b := range []float64{30, 25, 21, 17.5, 14.5, 12, 10, 8.5} {
		if placed, ok := spiralSimulate(n, area, b); ok {
			return placed
		}
	}
	placed, _ := spiralSimulate(n, area, 8.5)
	return placed
}

// spiralSimulate runs one density. ok=false when the spiral ran out
// of canvas before placing all n.
func spiralSimulate(n int, area image.Rectangle, b float64) ([]rectPlacement, bool) {
	cx := float64(area.Min.X) + float64(area.Dx())/2
	cy := float64(area.Min.Y) + float64(area.Dy())/2
	maxR := math.Min(float64(area.Dx()), float64(area.Dy()))/2 - 14
	const hMax = 300.0
	out := make([]rectPlacement, 0, n)
	out = append(out, rectPlacement{index: 0, cx: cx, cy: cy, w: hMax * 2 / 3, h: hMax})
	turnSpacing := 2 * math.Pi * b
	const theta0 = -math.Pi / 2
	theta := theta0
	// The spiral's start radius is SOLVED, not fixed: the first
	// winding poster sits straight above the center poster and must
	// clear its top edge. Grow rStart until poster 1's rect clears
	// the center poster's (they share the vertical axis, so this is
	// a pure height sum plus a gap).
	rStart := 196.0
	for {
		h1 := hMax * (1 - 0.48*(rStart/maxR))
		if cap := turnSpacing * 0.86; h1 > cap {
			h1 = cap
		}
		if h1 < 30 {
			h1 = 30
		}
		p1 := rectPlacement{
			cx: cx + rStart*math.Cos(theta0),
			cy: cy + rStart*math.Sin(theta0),
			w:  h1 * 2 / 3, h: h1,
		}
		if !rectsOverlap(p1, out[0], 0.02) || rStart > maxR*0.85 {
			break
		}
		rStart += 6
	}
	r := rStart
	prevW := hMax * 2 / 3
	for i := 1; i < n; i++ {
		h := hMax * (1 - 0.48*(r/maxR))
		if cap := turnSpacing * 0.86; h > cap {
			h = cap
		}
		if h < 30 {
			h = 30
		}
		w := h * 2 / 3
		if r+0.62*h > maxR {
			return out, false
		}
		out = append(out, rectPlacement{
			index: i,
			cx:    cx + r*math.Cos(theta),
			cy:    cy + r*math.Sin(theta),
			w:     w, h: h,
		})
		// Advance along the spiral. The nominal step is an arc
		// length, but consecutive centers sit on a CHORD (shorter),
		// and big axis-aligned posters on a tight curve swing their
		// corners into each other — so grow the step until the NEXT
		// poster's rect clears the last two placed (2% tolerance),
		// up to a bounded search. Deterministic: same n, same walk.
		step := (prevW+w)/2 + 12
		for grow := 0; grow < 24; grow++ {
			nTheta := theta + step/r
			nR := rStart + b*(nTheta-theta0)
			nh := hMax * (1 - 0.48*(nR/maxR))
			if cap := turnSpacing * 0.86; nh > cap {
				nh = cap
			}
			if nh < 30 {
				nh = 30
			}
			next := rectPlacement{
				cx: cx + nR*math.Cos(nTheta),
				cy: cy + nR*math.Sin(nTheta),
				w:  nh * 2 / 3, h: nh,
			}
			if !rectsOverlap(next, out[len(out)-1], 0.02) &&
				(len(out) < 2 || !rectsOverlap(next, out[len(out)-2], 0.02)) {
				break
			}
			step *= 1.12
		}
		theta += step / r
		r = rStart + b*(theta-theta0)
		prevW = w
	}
	return out, true
}

// rectsOverlap reports whether two axis-aligned poster rects overlap
// by more than tol (a fraction of the smaller rect's area).
func rectsOverlap(a, b rectPlacement, tol float64) bool {
	ox := math.Min(a.cx+a.w/2, b.cx+b.w/2) - math.Max(a.cx-a.w/2, b.cx-b.w/2)
	oy := math.Min(a.cy+a.h/2, b.cy+b.h/2) - math.Max(a.cy-a.h/2, b.cy-b.h/2)
	if ox <= 0 || oy <= 0 {
		return false
	}
	return ox*oy > tol*math.Min(a.w*a.h, b.w*b.h)
}

// drawSpiralPoster draws one upright poster rect with a hairline
// pale border (a slightly larger pale rect behind it).
func drawSpiralPoster(dst *image.RGBA, img image.Image, p rectPlacement) {
	x0 := int(math.Round(p.cx - p.w/2))
	y0 := int(math.Round(p.cy - p.h/2))
	x1 := int(math.Round(p.cx + p.w/2))
	y1 := int(math.Round(p.cy + p.h/2))
	border := image.Rect(x0-2, y0-2, x1+2, y1+2)
	b := dst.Bounds()
	for y := border.Min.Y; y < border.Max.Y; y++ {
		for x := border.Min.X; x < border.Max.X; x++ {
			if image.Pt(x, y).In(b) {
				dst.SetRGBA(x, y, color.RGBA{225, 225, 235, 200})
			}
		}
	}
	drawCover(dst, img, image.Rect(x0, y0, x1, y1), 0.5, 0.5)
}
