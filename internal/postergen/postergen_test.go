package postergen

// Pixel- and layout-level verification for the generator. Fixtures
// are solid-color posters with DISTINCT colors, so "the render uses
// this member" is directly observable: the member's exact color must
// appear in the output in meaningful quantity.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// distinctColor spreads hues so no two fixture colors collide.
func distinctColor(i int) color.RGBA {
	// Golden-angle hue walk in RGB via a cheap HSV→RGB.
	h := math.Mod(float64(i)*137.508, 360) / 60
	c := 0.85
	x := c * (1 - math.Abs(math.Mod(h, 2)-1))
	var r, g, b float64
	switch {
	case h < 1:
		r, g, b = c, x, 0
	case h < 2:
		r, g, b = x, c, 0
	case h < 3:
		r, g, b = 0, c, x
	case h < 4:
		r, g, b = 0, x, c
	case h < 5:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	m := 0.08
	return color.RGBA{uint8((r + m) * 255), uint8((g + m) * 255), uint8((b + m) * 255), 255}
}

func solidPoster(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i+3 < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, 255
	}
	return img
}

// patternedArt is member 0's backdrop: four quadrants of strong
// colors, so blur (boundary blending) and focal crops (quadrant
// selection) measurably change the render — a solid backdrop would
// hide both.
func patternedArt() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	quads := [4]color.RGBA{
		{230, 40, 40, 255}, {40, 200, 60, 255},
		{40, 60, 230, 255}, {240, 220, 40, 255},
	}
	for y := 0; y < 720; y++ {
		for x := 0; x < 1280; x++ {
			q := quads[(y/360)*2+(x/640)]
			img.SetRGBA(x, y, q)
		}
	}
	return img
}

// members builds n members with distinct solid posters (+ a wide
// patterned backdrop for member 0).
func members(n int) []MemberImage {
	out := make([]MemberImage, 0, n)
	for i := 0; i < n; i++ {
		m := MemberImage{
			Key:    fmt.Sprint(1000 + i),
			Title:  fmt.Sprintf("Fixture %d", i),
			Poster: solidPoster(400, 600, distinctColor(i)),
		}
		if i == 0 {
			m.Art = patternedArt()
		}
		out = append(out, m)
	}
	return out
}

func decode(t *testing.T, pngBytes []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("render is not a valid PNG: %v", err)
	}
	return img
}

func hash(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

// colorPixelCount counts pixels within ±10 of c per channel.
func colorPixelCount(img image.Image, c color.RGBA) int {
	b := img.Bounds()
	n := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if abs(int(r>>8)-int(c.R)) <= 10 && abs(int(g>>8)-int(c.G)) <= 10 && abs(int(bl>>8)-int(c.B)) <= 10 {
				n++
			}
		}
	}
	return n
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// ---- templates differ / basic shape ------------------------------------------

func TestTemplatesRenderAndDiffer(t *testing.T) {
	ms := members(9)
	specs := map[string]Spec{
		"mosaic2":   {Template: "mosaic", Title: "Test Shelf", Grid: "2x2", Plate: true},
		"mosaic3":   {Template: "mosaic", Title: "Test Shelf", Grid: "3x3", Plate: true},
		"flower":    {Template: "mosaic", Title: "Test Shelf", Grid: "flower", Plate: true},
		"spiral":    {Template: "mosaic", Title: "Test Shelf", Grid: "spiral", Plate: true},
		"hero":      {Template: "hero", Title: "Test Shelf", Background: Background{Kind: "member", MemberKey: "1000"}},
		"titlecard": {Template: "titlecard", Title: "Test Shelf"},
	}
	hashes := map[string]string{}
	for name, spec := range specs {
		res, err := Render(spec, ms)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		img := decode(t, res.PNG)
		if img.Bounds().Dx() != CanvasW || img.Bounds().Dy() != CanvasH {
			t.Fatalf("%s: size = %v, want %dx%d", name, img.Bounds(), CanvasW, CanvasH)
		}
		hashes[name] = hash(res.PNG)
	}
	seen := map[string]string{}
	for name, h := range hashes {
		if other, dup := seen[h]; dup {
			t.Errorf("templates %s and %s rendered identically", other, name)
		}
		seen[h] = name
	}
}

// ---- honesty: shortfalls, skips, caps -----------------------------------------

func TestMosaicShortfallIsAnError(t *testing.T) {
	_, err := Render(Spec{Template: "mosaic", Title: "X", Grid: "3x3"}, members(4))
	if err == nil || !strings.Contains(err.Error(), "needs 9 posters") {
		t.Fatalf("err = %v, want a plain shortfall naming the need", err)
	}
}

func TestSkippedPostersCountedAndNoted(t *testing.T) {
	ms := members(9)
	ms[3].Poster = nil // fetch failure
	ms[7].Poster = nil
	res, err := Render(Spec{Template: "mosaic", Title: "X", Grid: "flower"}, ms)
	if err != nil {
		t.Fatal(err)
	}
	if res.SkippedPosters != 2 || res.UsedMembers != 7 {
		t.Fatalf("skipped/used = %d/%d, want 2/7", res.SkippedPosters, res.UsedMembers)
	}
	found := false
	for _, n := range res.Notes {
		if strings.Contains(n, "2 posters couldn't be fetched") {
			found = true
		}
	}
	if !found {
		t.Fatalf("notes = %v, want the skip note", res.Notes)
	}
}

func TestFlowerCapIsHonest(t *testing.T) {
	res, err := Render(Spec{Template: "mosaic", Title: "Big", Grid: "flower"}, members(FlowerCap+31))
	if err != nil {
		t.Fatal(err)
	}
	if !res.CapApplied || res.UsedMembers != FlowerCap {
		t.Fatalf("cap = %v used = %d, want applied at %d", res.CapApplied, res.UsedMembers, FlowerCap)
	}
	joined := strings.Join(res.Notes, " ")
	if !strings.Contains(joined, fmt.Sprint(FlowerCap)) || !strings.Contains(joined, fmt.Sprint(FlowerCap+31)) {
		t.Fatalf("cap note must name both numbers: %v", res.Notes)
	}
}

func TestSpiralCapIsHonest(t *testing.T) {
	res, err := Render(Spec{Template: "mosaic", Title: "Big", Grid: "spiral"}, members(SpiralCap+31))
	if err != nil {
		t.Fatal(err)
	}
	if !res.CapApplied || res.UsedMembers != SpiralCap {
		t.Fatalf("cap = %v used = %d, want applied at %d", res.CapApplied, res.UsedMembers, SpiralCap)
	}
	joined := strings.Join(res.Notes, " ")
	if !strings.Contains(joined, fmt.Sprint(SpiralCap)) || !strings.Contains(joined, fmt.Sprint(SpiralCap+31)) {
		t.Fatalf("cap note must name both numbers: %v", res.Notes)
	}
}

// ---- uses-every-member, pixel level --------------------------------------------

func usesEveryMember(t *testing.T, grid string, n int) {
	t.Helper()
	ms := members(n)
	res, err := Render(Spec{Template: "mosaic", Title: "All Of Them", Grid: grid, Plate: true}, ms)
	if err != nil {
		t.Fatalf("%s n=%d: %v", grid, n, err)
	}
	if res.UsedMembers != n {
		t.Fatalf("%s n=%d: used = %d", grid, n, res.UsedMembers)
	}
	img := decode(t, res.PNG)
	for i := 0; i < n; i++ {
		if got := colorPixelCount(img, distinctColor(i)); got < 40 {
			t.Errorf("%s n=%d: member %d color appears on %d px (< 40) — not visibly used", grid, n, i, got)
		}
	}
}

func TestFlowerUsesEveryMember(t *testing.T) {
	usesEveryMember(t, "flower", 25)
	usesEveryMember(t, "flower", 150) // the large fixture
}

func TestSpiralUsesEveryMember(t *testing.T) {
	usesEveryMember(t, "spiral", 25)
	usesEveryMember(t, "spiral", 150) // the large fixture
}

// ---- layout invariants -----------------------------------------------------------

func TestFlowerLayoutInvariants(t *testing.T) {
	area := image.Rect(0, 0, CanvasW, CanvasH-plateHeight)
	for _, n := range []int{1, 2, 7, 19, 25, 60, 169} {
		p := flowerLayout(n, area)
		if len(p) != n {
			t.Fatalf("n=%d: %d placements", n, len(p))
		}
		for i, a := range p {
			if a.cx-a.r < -1 || a.cy-a.r < -1 || a.cx+a.r > float64(area.Dx())+1 || a.cy+a.r > float64(area.Dy())+1 {
				t.Errorf("n=%d petal %d escapes the area: %+v", n, i, a)
			}
			for j := i + 1; j < len(p); j++ {
				b := p[j]
				d := math.Hypot(a.cx-b.cx, a.cy-b.cy)
				if d < a.r+b.r-1.5 {
					t.Errorf("n=%d petals %d,%d overlap: dist %.1f < r %.1f+%.1f", n, i, j, d, a.r, b.r)
				}
			}
		}
	}
}

func TestSpiralLayoutInvariants(t *testing.T) {
	area := image.Rect(0, 0, CanvasW, CanvasH-plateHeight)
	for _, n := range []int{1, 2, 9, 25, 60, 150, 169} {
		p := spiralLayout(n, area)
		if len(p) != n {
			t.Fatalf("n=%d: %d placements, want all", n, len(p))
		}
		for i, a := range p {
			if a.cx-a.w/2 < -1 || a.cy-a.h/2 < -1 || a.cx+a.w/2 > float64(area.Dx())+1 || a.cy+a.h/2 > float64(area.Dy())+1 {
				t.Errorf("n=%d poster %d escapes the area: %+v", n, i, a)
			}
			for j := i + 1; j < len(p); j++ {
				b := p[j]
				ox := math.Max(0, math.Min(a.cx+a.w/2, b.cx+b.w/2)-math.Max(a.cx-a.w/2, b.cx-b.w/2))
				oy := math.Max(0, math.Min(a.cy+a.h/2, b.cy+b.h/2)-math.Max(a.cy-a.h/2, b.cy-b.h/2))
				overlap := ox * oy
				smaller := math.Min(a.w*a.h, b.w*b.h)
				if overlap > 0.06*smaller {
					t.Errorf("n=%d posters %d,%d overlap %.0fpx (%.0f%% of the smaller)", n, i, j, overlap, 100*overlap/smaller)
				}
			}
		}
		// Shrinking outward: the last poster's height never exceeds
		// the center poster's.
		if n > 2 && p[n-1].h > p[0].h {
			t.Errorf("n=%d: outer poster taller than center (%0.f > %.0f)", n, p[n-1].h, p[0].h)
		}
	}
}

// ---- mosaic poster zoom (v1.21.2) ------------------------------------------------

// centerPatchPoster is a poster of color A with a centered patch of
// color B (middle 40% × 40%), so zooming observably changes B's
// share of a cell: at 100% the cell shows almost the whole poster
// (B ≈ 16% of it); at 200% the cell shows the poster's center
// quarter (B ≈ 64% of that window).
func centerPatchPoster(a, b color.RGBA) *image.RGBA {
	img := solidPoster(400, 600, a)
	for y := 180; y < 420; y++ {
		for x := 120; x < 280; x++ {
			img.SetRGBA(x, y, b)
		}
	}
	return img
}

func TestMosaicScaleOneIsByteIdentical(t *testing.T) {
	// Scale absent (0) and Scale = 1 must be the pre-zoom render,
	// byte for byte, on every mosaic layout — the slider at 100%
	// is not allowed to change a single pixel.
	for _, grid := range []string{"2x2", "3x3", "flower", "spiral"} {
		base, err := Render(Spec{Template: "mosaic", Title: "Zoom", Grid: grid, Plate: true}, members(9))
		if err != nil {
			t.Fatalf("%s: %v", grid, err)
		}
		one, err := Render(Spec{Template: "mosaic", Title: "Zoom", Grid: grid, Plate: true, Scale: 1}, members(9))
		if err != nil {
			t.Fatalf("%s: %v", grid, err)
		}
		if hash(base.PNG) != hash(one.PNG) {
			t.Errorf("%s: scale 1.0 render differs from scale-absent render", grid)
		}
	}
}

func TestMosaicScaleHalfGrid(t *testing.T) {
	ms := members(4)
	spec := Spec{Template: "mosaic", Grid: "2x2"} // no plate, no title
	full, err := Render(spec, ms)
	if err != nil {
		t.Fatal(err)
	}
	spec.Scale = 0.5
	half, err := Render(spec, ms)
	if err != nil {
		t.Fatal(err)
	}
	imgF, imgH := decode(t, full.PNG), decode(t, half.PNG)
	// Cell 0 is (14,14)-(495,745), center (254,379).
	c0 := distinctColor(0)
	// At 50%: the cell's corner is the render background (the
	// vertical gradient's near-black top), not the poster.
	r, g, b, _ := imgH.At(15, 15).RGBA()
	if int(r>>8) > 60 || int(g>>8) > 60 || int(b>>8) > 70 {
		t.Errorf("scale 0.5 cell corner = (%d,%d,%d), want the dark background — the poster should have shrunk away from the corner", r>>8, g>>8, b>>8)
	}
	// The center of the cell is still the first poster's color…
	if got := colorPixelCount(imgH, c0); got < 1000 {
		t.Errorf("scale 0.5: member 0 color on %d px — the shrunk poster should sit at the cell center", got)
	}
	// …occupying roughly a quarter of its full-size area.
	ratio := float64(colorPixelCount(imgH, c0)) / float64(colorPixelCount(imgF, c0))
	if ratio < 0.15 || ratio > 0.40 {
		t.Errorf("scale 0.5/full member-0 area ratio = %.2f, want ≈0.25", ratio)
	}
}

func TestMosaicScaleDoubleGrid(t *testing.T) {
	a := color.RGBA{30, 90, 200, 255}
	patch := color.RGBA{230, 40, 230, 255}
	ms := members(4)
	ms[0].Poster = centerPatchPoster(a, patch)
	spec := Spec{Template: "mosaic", Grid: "2x2"}
	one, err := Render(spec, ms)
	if err != nil {
		t.Fatal(err)
	}
	spec.Scale = 2
	two, err := Render(spec, ms)
	if err != nil {
		t.Fatal(err)
	}
	img1, img2 := decode(t, one.PNG), decode(t, two.PNG)
	p1, p2 := colorPixelCount(img1, patch), colorPixelCount(img2, patch)
	if p2 < 2*p1 {
		t.Errorf("scale 2 patch pixels = %d vs scale 1 %d — zooming in must at least double the center patch's share", p2, p1)
	}
	// Clipping: the gutter between cells (x≈500, y = cell-0 middle)
	// stays background at 200% — the zoomed poster must not bleed
	// out of its cell.
	r, g, b, _ := img2.At(500, 379).RGBA()
	if int(r>>8) > 60 || int(g>>8) > 60 || int(b>>8) > 70 {
		t.Errorf("scale 2 gutter pixel = (%d,%d,%d), want the dark background — the zoom bled out of its cell", r>>8, g>>8, b>>8)
	}
	// And the neighbour cell still shows its own member.
	if got := colorPixelCount(img2, distinctColor(1)); got < 1000 {
		t.Errorf("scale 2: member 1 color on %d px — the neighbour cell lost its poster", got)
	}
}

func TestMosaicScaleFlower(t *testing.T) {
	ms := members(1)
	spec := Spec{Template: "mosaic", Grid: "flower"}
	one, err := Render(spec, ms)
	if err != nil {
		t.Fatal(err)
	}
	spec.Scale = 0.5
	half, err := Render(spec, ms)
	if err != nil {
		t.Fatal(err)
	}
	spec.Scale = 2
	two, err := Render(spec, ms)
	if err != nil {
		t.Fatal(err)
	}
	c := distinctColor(0)
	n1 := colorPixelCount(decode(t, one.PNG), c)
	nh := colorPixelCount(decode(t, half.PNG), c)
	n2 := colorPixelCount(decode(t, two.PNG), c)
	if ratio := float64(nh) / float64(n1); ratio < 0.18 || ratio > 0.33 {
		t.Errorf("flower scale 0.5/1 area ratio = %.2f, want ≈0.25 (petal %d vs %d px)", ratio, nh, n1)
	}
	if n2 < n1*13/10 {
		t.Errorf("flower scale 2 petal = %d px vs %d at scale 1 — the zoomed petal should cover clearly more", n2, n1)
	}
}

// ---- hero / title card behavior ----------------------------------------------------

func TestHeroControlsChangeTheRender(t *testing.T) {
	ms := members(4)
	base := Spec{Template: "hero", Title: "Hero Test", FocalX: 0.5, FocalY: 0.5, Background: Background{Kind: "member", MemberKey: "1000", UseArt: true}}
	r0, err := Render(base, ms)
	if err != nil {
		t.Fatal(err)
	}
	blurred := base
	blurred.Blur = 90
	rB, err := Render(blurred, ms)
	if err != nil {
		t.Fatal(err)
	}
	if hash(r0.PNG) == hash(rB.PNG) {
		t.Error("blur 90 rendered identically to blur 0")
	}
	dark := base
	dark.Darken = 90
	rD, err := Render(dark, ms)
	if err != nil {
		t.Fatal(err)
	}
	if hash(r0.PNG) == hash(rD.PNG) {
		t.Error("darken 90 rendered identically to darken 0")
	}
	if mean := meanBrightness(decode(t, rD.PNG)); mean >= meanBrightness(decode(t, r0.PNG)) {
		t.Errorf("darken did not darken: %.3f vs %.3f", mean, meanBrightness(decode(t, r0.PNG)))
	}
	focal := base
	focal.FocalX, focal.FocalY = 0, 0
	rF, err := Render(focal, ms)
	if err != nil {
		t.Fatal(err)
	}
	if hash(r0.PNG) == hash(rF.PNG) {
		t.Error("focal (0,0) rendered identically to centered — focal crop is not wired")
	}
}

func meanBrightness(img image.Image) float64 {
	b := img.Bounds()
	var sum, n float64
	for y := b.Min.Y; y < b.Max.Y; y += 7 {
		for x := b.Min.X; x < b.Max.X; x += 7 {
			r, g, bl, _ := img.At(x, y).RGBA()
			sum += float64(r+g+bl) / 3 / 65535
			n++
		}
	}
	return sum / n
}

func TestHeroNeedsBackground(t *testing.T) {
	_, err := Render(Spec{Template: "hero", Title: "X"}, nil)
	if err == nil || !strings.Contains(err.Error(), "needs a background") {
		t.Fatalf("err = %v", err)
	}
	// …but with the user's own art and zero members it renders.
	res, err := Render(Spec{Template: "hero", Title: "X",
		Background: Background{Kind: "image", Image: solidPoster(800, 500, distinctColor(7))}}, nil)
	if err != nil {
		t.Fatalf("user-art hero with no members: %v", err)
	}
	if res.UsedMembers != 0 {
		t.Fatalf("used = %d", res.UsedMembers)
	}
}

func TestTitleCardDerivedAndTitlePresence(t *testing.T) {
	ms := members(6)
	withTitle, err := Render(Spec{Template: "titlecard", Title: "Spooky Season"}, ms)
	if err != nil {
		t.Fatal(err)
	}
	img := decode(t, withTitle.PNG)
	// Title ink is near-white; the derived gradient is deepened, so
	// a healthy count of near-white pixels proves type was drawn.
	nearWhite := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r>>8 > 235 && g>>8 > 235 && bl>>8 > 235 {
				nearWhite++
			}
		}
	}
	if nearWhite < 800 {
		t.Errorf("near-white pixels = %d — the title does not look drawn", nearWhite)
	}
	// Empty title errors (no silent blank card)…
	if _, err := Render(Spec{Template: "titlecard", Title: "  "}, ms); err == nil {
		t.Error("title card with empty title should refuse")
	}
	// …a zero-member derived card still renders, with the note.
	res, err := Render(Spec{Template: "titlecard", Title: "Empty Shelf"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Notes, " "), "neutral house gradient") {
		t.Errorf("notes = %v", res.Notes)
	}
	// A custom background changes the card.
	custom, err := Render(Spec{Template: "titlecard", Title: "Spooky Season",
		Background: Background{Kind: "image", Image: solidPoster(900, 900, distinctColor(3))},
		Gradient:   40}, ms)
	if err != nil {
		t.Fatal(err)
	}
	if hash(custom.PNG) == hash(withTitle.PNG) {
		t.Error("custom background did not change the title card")
	}
}

// ---- URL fetch discipline -----------------------------------------------------------

func pngBytes(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, solidPoster(w, h, c)); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFetchImageURL(t *testing.T) {
	good := pngBytes(t, 60, 90, distinctColor(1))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(good)
		case "/text":
			_, _ = w.Write([]byte("not an image"))
		case "/redir":
			w.Header().Set("Location", "/ok.png")
			w.WriteHeader(http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	if _, err := FetchImageURL(ctx, srv.URL+"/ok.png"); err != nil {
		t.Errorf("ok.png: %v", err)
	}
	if _, err := FetchImageURL(ctx, srv.URL+"/text"); err == nil {
		t.Error("text body should be rejected")
	}
	if _, err := FetchImageURL(ctx, srv.URL+"/redir"); err == nil {
		t.Error("redirect should not be followed")
	}
	if _, err := FetchImageURL(ctx, "ftp://example.test/x.png"); err == nil {
		t.Error("non-http(s) scheme should be rejected")
	}
	if _, err := FetchImageURL(ctx, srv.URL+"/missing"); err == nil {
		t.Error("404 should be an error naming the status")
	}
}
