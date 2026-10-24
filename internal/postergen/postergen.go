// Package postergen renders collection posters from a collection's
// own member art (v1.20). Three templates:
//
//   - Mosaic: member posters in a 2×2 / 3×3 grid — or, with the
//     "flower" grid, EVERY selected member arranged as a center
//     poster with radiating petal rings around it — plus an optional
//     title plate.
//   - Hero: one treated background image (blur / darken / focal
//     crop) under a large title.
//   - Title card: typography first — a gradient derived from the
//     member posters' own colors (or the user's own background art
//     under the same treatments) and a clean title.
//
// The package is pure rendering: callers hand it decoded images and
// a Spec; it returns canonical PNG bytes at the app's working poster
// size (1000×1500) plus honest statistics (how many posters were
// used, how many could not be fetched and were skipped, whether the
// flower cap trimmed the set). It never talks to Plex and never
// writes anywhere — applying a render is the caller's job, through
// the same backup-gated collection-art path as any other poster.
//
// Titles are drawn with the bundled Montserrat Bold (SIL Open Font
// License 1.1 — see fonts/OFL.txt), embedded in the binary, so the
// preview and the applied poster are rendered by the same code with
// the same font on every machine — never a system font lottery.
package postergen

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
	"sync"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Canvas is the app's working poster size.
const (
	CanvasW = 1000
	CanvasH = 1500
)

// FlowerCap is the most posters the flower layout will place. Past
// this the outer petals shrink below ~30px — decoration, not
// posters — so the render caps and SAYS SO (Result.CapApplied)
// instead of silently dropping members.
const FlowerCap = 169

// MemberImage is one collection member's decoded art. Poster is the
// member's poster (may be a downscaled working copy); Art is its
// backdrop, fetched only when a spec asks for it. Either may be nil
// when the fetch failed — Render counts nil Posters as skipped.
type MemberImage struct {
	Key    string
	Title  string
	Poster image.Image
	Art    image.Image
}

// Background selects what sits behind a Hero / Title card title.
type Background struct {
	Kind      string // "member" | "image" | "derived" (title card default)
	MemberKey string // Kind == "member": which member's art
	UseArt    bool   // Kind == "member": backdrop instead of poster
	Image     image.Image
}

// Spec is one generation request, fully resolved (images decoded).
type Spec struct {
	Template string // "mosaic" | "hero" | "titlecard"
	Title    string

	// Mosaic.
	Grid       string   // "2x2" | "3x3" | "flower" | "spiral"
	MemberKeys []string // ordered selection; empty = all members, in order
	Plate      bool     // title plate over the mosaic
	Scale      float64  // poster zoom within each cell, around the cell
	// center: 1 = cover-fill exactly; >1 zooms in (tighter crop);
	// <1 shrinks the poster inside its cell, the render background
	// showing in the gaps. 0 = default (1). Mosaic layouts only.

	// Title treatment (hero / title card; the mosaic plate uses its
	// own fixed treatment).
	TitleSize     int    // px cap height at canvas scale; 0 = default (110)
	TitlePosition string // "top" | "center" | "bottom"

	// Background treatment (hero / title card).
	Background Background
	Blur       int     // 0..100
	Darken     int     // 0..100 — how much brightness to take away
	Gradient   int     // 0..100 — title card overlay gradient strength
	FocalX     float64 // 0..1 cover-crop focal point (default 0.5)
	FocalY     float64 // 0..1
}

// Result is one render plus the honesty counters the UI shows.
type Result struct {
	PNG             []byte
	UsedMembers     int  // member posters visible in the render
	SkippedPosters  int  // selected members whose poster could not be fetched
	SelectedMembers int  // members in the (ordered) selection
	MemberTotal     int  // members handed in, before selection
	CapApplied      bool // flower: the selection exceeded FlowerCap
	CapLimit        int
	Notes           []string
}

// ---------------------------------------------------------------------------
// Font
// ---------------------------------------------------------------------------

//go:embed fonts/Montserrat-Bold.ttf
var titleFontTTF []byte

var (
	fontOnce   sync.Once
	fontParsed *opentype.Font
	fontErr    error
)

func titleFace(size float64) (font.Face, error) {
	fontOnce.Do(func() {
		fontParsed, fontErr = opentype.Parse(titleFontTTF)
	})
	if fontErr != nil {
		return nil, fmt.Errorf("bundled title font failed to parse: %w", fontErr)
	}
	return opentype.NewFace(fontParsed, &opentype.FaceOptions{
		Size: size, DPI: 72, Hinting: font.HintingFull,
	})
}

// ---------------------------------------------------------------------------
// Image helpers
// ---------------------------------------------------------------------------

// toRGBA returns img as an *image.RGBA copy (draw.Draw conversion).
func toRGBA(img image.Image) *image.RGBA {
	if img == nil {
		return nil
	}
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	return out
}

// drawCover paints src into rect on dst, scaled to COVER the rect and
// cropped around the focal point (fx, fy in 0..1). Scaling is
// CatmullRom — poster cells stay crisp when shrunk. The crop is
// clamped inside the scaled image; an extreme focal point slides the
// window fully to that edge.
func drawCover(dst *image.RGBA, src image.Image, rect image.Rectangle, fx, fy float64) {
	if src == nil {
		return
	}
	sb := src.Bounds()
	sw, sh := float64(sb.Dx()), float64(sb.Dy())
	if sw <= 0 || sh <= 0 {
		return
	}
	rw, rh := float64(rect.Dx()), float64(rect.Dy())
	scale := math.Max(rw/sw, rh/sh)
	scaledW := int(math.Ceil(sw * scale))
	scaledH := int(math.Ceil(sh * scale))
	if scaledW < 1 {
		scaledW = 1
	}
	if scaledH < 1 {
		scaledH = 1
	}
	scaled := image.NewRGBA(image.Rect(0, 0, scaledW, scaledH))
	draw.CatmullRom.Scale(scaled, scaled.Bounds(), src, sb, draw.Src, nil)
	ox := int(math.Round(float64(scaledW-rect.Dx()) * clamp01(fx)))
	oy := int(math.Round(float64(scaledH-rect.Dy()) * clamp01(fy)))
	ox = clampInt(ox, 0, scaledW-rect.Dx())
	oy = clampInt(oy, 0, scaledH-rect.Dy())
	draw.Draw(dst, rect, scaled, image.Pt(ox, oy), draw.Src)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func clampInt(v, lo, hi int) int {
	if hi < lo {
		hi = lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// boxBlur blurs an RGBA in place with three separable box passes —
// close enough to a gaussian for poster treatment, and O(n) per pass
// regardless of radius (sliding window). Alpha is left untouched
// (poster art is opaque; blurring alpha would halo the edges).
func boxBlur(img *image.RGBA, radius int) {
	if radius <= 0 {
		return
	}
	for pass := 0; pass < 3; pass++ {
		blurAxis(img, radius, true)
		blurAxis(img, radius, false)
	}
}

func blurAxis(img *image.RGBA, radius int, horizontal bool) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	src := make([]byte, len(img.Pix))
	copy(src, img.Pix)
	stride := img.Stride
	lines, length := h, w
	if !horizontal {
		lines, length = w, h
	}
	for line := 0; line < lines; line++ {
		var sumR, sumG, sumB int
		// Window over [-radius, +radius] clamped at the edges.
		count := 0
		for i := -radius; i <= radius; i++ {
			ci := clampInt(i, 0, length-1)
			off := pixOff(src, stride, w, line, ci, horizontal)
			sumR += int(src[off])
			sumG += int(src[off+1])
			sumB += int(src[off+2])
			count++
		}
		for x := 0; x < length; x++ {
			off := pixOff(img.Pix, stride, w, line, x, horizontal)
			img.Pix[off] = uint8(sumR / count)
			img.Pix[off+1] = uint8(sumG / count)
			img.Pix[off+2] = uint8(sumB / count)
			// Slide: drop x-radius, add x+radius+1.
			outI := clampInt(x-radius, 0, length-1)
			inI := clampInt(x+radius+1, 0, length-1)
			oOff := pixOff(src, stride, w, line, outI, horizontal)
			iOff := pixOff(src, stride, w, line, inI, horizontal)
			sumR += int(src[iOff]) - int(src[oOff])
			sumG += int(src[iOff+1]) - int(src[oOff+1])
			sumB += int(src[iOff+2]) - int(src[oOff+2])
		}
	}
}

func pixOff(pix []byte, stride, w, line, x int, horizontal bool) int {
	if horizontal {
		return line*stride + x*4
	}
	return x*stride + line*4
}

// darken multiplies RGB by (1 - amount), amount in 0..1.
func darken(img *image.RGBA, amount float64) {
	if amount <= 0 {
		return
	}
	f := 1 - amount
	for i := 0; i+3 < len(img.Pix); i += 4 {
		img.Pix[i] = uint8(float64(img.Pix[i]) * f)
		img.Pix[i+1] = uint8(float64(img.Pix[i+1]) * f)
		img.Pix[i+2] = uint8(float64(img.Pix[i+2]) * f)
	}
}

// verticalGradient fills rect with a top→bottom two-stop gradient.
func verticalGradient(dst *image.RGBA, rect image.Rectangle, top, bottom color.RGBA) {
	h := rect.Dy()
	for y := 0; y < h; y++ {
		t := 0.0
		if h > 1 {
			t = float64(y) / float64(h-1)
		}
		c := color.RGBA{
			R: uint8(float64(top.R) + (float64(bottom.R)-float64(top.R))*t),
			G: uint8(float64(top.G) + (float64(bottom.G)-float64(top.G))*t),
			B: uint8(float64(top.B) + (float64(bottom.B)-float64(top.B))*t),
			A: 255,
		}
		for x := rect.Min.X; x < rect.Max.X; x++ {
			dst.SetRGBA(x, rect.Min.Y+y, c)
		}
	}
}

// gradientOverlay blends a black gradient into rect: strongest at
// the edge named by `from` ("bottom" or "top"), fading to clear at
// the far side, with peak alpha = strength (0..1).
func gradientOverlay(dst *image.RGBA, rect image.Rectangle, from string, strength float64) {
	if strength <= 0 {
		return
	}
	h := rect.Dy()
	for y := 0; y < h; y++ {
		t := float64(y) / float64(maxInt(h-1, 1)) // 0 top → 1 bottom
		a := strength * t
		if from == "top" {
			a = strength * (1 - t)
		}
		if a <= 0 {
			continue
		}
		for x := rect.Min.X; x < rect.Max.X; x++ {
			off := dst.PixOffset(x, rect.Min.Y+y)
			dst.Pix[off] = uint8(float64(dst.Pix[off]) * (1 - a))
			dst.Pix[off+1] = uint8(float64(dst.Pix[off+1]) * (1 - a))
			dst.Pix[off+2] = uint8(float64(dst.Pix[off+2]) * (1 - a))
		}
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// averageColor returns the mean color of img (via a 16×16 downscale
// — exact enough for gradient derivation, fast at any source size).
func averageColor(img image.Image) color.RGBA {
	if img == nil {
		return color.RGBA{0x18, 0x18, 0x1e, 255}
	}
	small := image.NewRGBA(image.Rect(0, 0, 16, 16))
	draw.ApproxBiLinear.Scale(small, small.Bounds(), img, img.Bounds(), draw.Src, nil)
	var r, g, b int
	for i := 0; i+3 < len(small.Pix); i += 4 {
		r += int(small.Pix[i])
		g += int(small.Pix[i+1])
		b += int(small.Pix[i+2])
	}
	n := 256
	return color.RGBA{uint8(r / n), uint8(g / n), uint8(b / n), 255}
}

// regionAverage averages a horizontal band of img (y0..y1 in 0..1).
func regionAverage(img image.Image, y0, y1 float64) color.RGBA {
	if img == nil {
		return averageColor(nil)
	}
	b := img.Bounds()
	h := int(float64(b.Dy()) * (y1 - y0))
	if h < 1 {
		h = 1
	}
	strip := image.NewRGBA(image.Rect(0, 0, 16, 16))
	src := image.Rect(b.Min.X, b.Min.Y+int(float64(b.Dy())*y0), b.Max.X, b.Min.Y+int(float64(b.Dy())*y0)+h)
	draw.ApproxBiLinear.Scale(strip, strip.Bounds(), img, src, draw.Src, nil)
	var r, g, bl int
	for i := 0; i+3 < len(strip.Pix); i += 4 {
		r += int(strip.Pix[i])
		g += int(strip.Pix[i+1])
		bl += int(strip.Pix[i+2])
	}
	return color.RGBA{uint8(r / 256), uint8(g / 256), uint8(bl / 256), 255}
}

// mixBlack mixes c toward black by t (0..1).
func mixBlack(c color.RGBA, t float64) color.RGBA {
	f := 1 - t
	return color.RGBA{uint8(float64(c.R) * f), uint8(float64(c.G) * f), uint8(float64(c.B) * f), 255}
}

// ---------------------------------------------------------------------------
// Title drawing
// ---------------------------------------------------------------------------

// wrapTitle lays text out in lines of at most maxWidth at the given
// face, shrinking the face (by rebuilding it smaller) until the block
// fits maxLines. Returns the lines and the face that fits. The
// caller's startSize is an upper bound, never grown.
func wrapTitle(text string, maxWidth, startSize float64, maxLines int) ([]string, font.Face, error) {
	size := startSize
	for size >= 26 {
		face, err := titleFace(size)
		if err != nil {
			return nil, nil, err
		}
		lines := wrapWords(face, text, maxWidth)
		if len(lines) <= maxLines {
			return lines, face, nil
		}
		size *= 0.88
	}
	face, err := titleFace(26)
	if err != nil {
		return nil, nil, err
	}
	lines := wrapWords(face, text, maxWidth)
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return lines, face, nil
}

func wrapWords(face font.Face, text string, maxWidth float64) []string {
	d := &font.Drawer{Face: face}
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	lines := []string{}
	cur := ""
	for _, w := range words {
		try := w
		if cur != "" {
			try = cur + " " + w
		}
		if float64(d.MeasureString(try))/64 > maxWidth && cur != "" {
			lines = append(lines, cur)
			cur = w
		} else {
			cur = try
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// drawTitleBlock draws the wrapped title centered on cx, with the
// block's vertical center at cy. A soft dark shadow pass under the
// main pass keeps light titles legible on busy art. Returns the
// block's height in px.
func drawTitleBlock(dst *image.RGBA, text string, cx, cy, maxWidth, startSize float64, maxLines int, col color.RGBA) (float64, error) {
	lines, face, err := wrapTitle(text, maxWidth, startSize, maxLines)
	if err != nil {
		return 0, err
	}
	if len(lines) == 0 {
		return 0, nil
	}
	m := face.Metrics()
	lineH := float64(m.Height) / 64
	ascent := float64(m.Ascent) / 64
	blockH := lineH * float64(len(lines))
	top := cy - blockH/2
	shadow := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(color.RGBA{0, 0, 0, 150}),
		Face: face,
	}
	main := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(col),
		Face: face,
	}
	for i, line := range lines {
		w := float64(main.MeasureString(line)) / 64
		// Dot takes fixed-point coordinates directly — fixed.P would
		// scale integer pixels a second time and land the text far
		// off-canvas (the v1.20 first-run bug: titles silently drawn
		// ~2M px away, caught by the near-white pixel check).
		dot := fixed.Point26_6{
			X: fixed.Int26_6((cx - w/2) * 64),
			Y: fixed.Int26_6((top + ascent + float64(i)*lineH) * 64),
		}
		shadow.Dot = fixed.Point26_6{X: dot.X, Y: dot.Y + 2*64}
		shadow.DrawString(line)
		main.Dot = dot
		main.DrawString(line)
	}
	return blockH, nil
}

// titleY resolves a position name to the title block's center Y for
// a canvas of height h, given the block height bh.
func titleCenterY(position string, h, bh float64) float64 {
	switch position {
	case "top":
		return 90 + bh/2
	case "bottom":
		return h - 90 - bh/2
	default: // center
		return h / 2
	}
}

// ---------------------------------------------------------------------------
// Render dispatch
// ---------------------------------------------------------------------------

// selectMembers applies spec.MemberKeys ordering/selection to the
// member list. Unknown keys are ignored (the UI only sends real
// ones); an empty selection means "all, in order".
func selectMembers(members []MemberImage, keys []string) []MemberImage {
	if len(keys) == 0 {
		return members
	}
	byKey := make(map[string]MemberImage, len(members))
	for _, m := range members {
		byKey[m.Key] = m
	}
	out := make([]MemberImage, 0, len(keys))
	seen := map[string]bool{}
	for _, k := range keys {
		if m, ok := byKey[k]; ok && !seen[k] {
			out = append(out, m)
			seen[k] = true
		}
	}
	return out
}

// Render produces the poster PNG for spec over members. members is
// the collection's full membership (posters decoded; nil Poster =
// fetch failed). Errors are plain-language: the UI shows them
// verbatim in the generator.
func Render(spec Spec, members []MemberImage) (*Result, error) {
	title := strings.TrimSpace(spec.Title)
	sel := selectMembers(members, spec.MemberKeys)
	res := &Result{
		SelectedMembers: len(sel),
		MemberTotal:     len(members),
		CapLimit:        FlowerCap,
	}
	for _, m := range sel {
		if m.Poster == nil {
			res.SkippedPosters++
		}
	}
	canvas := image.NewRGBA(image.Rect(0, 0, CanvasW, CanvasH))
	var err error
	switch spec.Template {
	case "mosaic":
		err = renderMosaic(canvas, spec, sel, res)
	case "hero":
		err = renderHero(canvas, spec, sel, res, title)
	case "titlecard":
		err = renderTitleCard(canvas, spec, sel, res, title)
	default:
		err = fmt.Errorf("unknown generator template %q", spec.Template)
	}
	if err != nil {
		return nil, err
	}
	if res.SkippedPosters > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("%d poster%s couldn't be fetched and were skipped.",
			res.SkippedPosters, plural(res.SkippedPosters)))
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return nil, err
	}
	res.PNG = buf.Bytes()
	return res, nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// availablePosters returns the selected members whose poster exists,
// in selection order.
func availablePosters(sel []MemberImage) []MemberImage {
	out := []MemberImage{}
	for _, m := range sel {
		if m.Poster != nil {
			out = append(out, m)
		}
	}
	return out
}

// resolveBackgroundImage returns the image a hero/title-card spec
// treats as its background, plus whether it resolved at all. Derived
// is handled by the title-card renderer itself (no image).
func resolveBackgroundImage(spec Spec, sel []MemberImage) (image.Image, bool) {
	switch spec.Background.Kind {
	case "image":
		if spec.Background.Image != nil {
			return spec.Background.Image, true
		}
	case "member":
		for _, m := range sel {
			if m.Key != spec.Background.MemberKey {
				continue
			}
			if spec.Background.UseArt && m.Art != nil {
				return m.Art, true
			}
			if m.Poster != nil {
				return m.Poster, true
			}
			if m.Art != nil {
				return m.Art, true
			}
		}
		// The picked member's art didn't resolve — fall back to the
		// first member poster that exists rather than failing.
		for _, m := range sel {
			if m.Poster != nil {
				return m.Poster, true
			}
		}
	}
	return nil, false
}

// paintBackground covers the whole canvas with img (cover + focal),
// applies blur and darken, and returns the treated canvas layer.
// Focal (0,0) is a real focal (top-left), not "unset" — callers
// that want the center pass 0.5, 0.5 explicitly (the API layer
// defaults absent focal fields to 0.5).
func paintBackground(canvas *image.RGBA, spec Spec, img image.Image) {
	fx, fy := spec.FocalX, spec.FocalY
	drawCover(canvas, img, canvas.Bounds(), fx, fy)
	if spec.Blur > 0 {
		boxBlur(canvas, int(float64(spec.Blur)/100*22)+1)
	}
	if spec.Darken > 0 {
		darken(canvas, float64(spec.Darken)/100*0.85)
	}
}
