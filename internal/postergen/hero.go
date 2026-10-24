package postergen

// hero.go — the Hero and Title card templates. Both paint a treated
// background (the user's own art, a member's art, or — title card
// only — a gradient derived from the member posters' own colors)
// and set the collection title over it in the bundled font.

import (
	"fmt"
	"image"
	"image/color"
)

var titleInk = color.RGBA{246, 246, 250, 255}

func titleSizeOr(spec Spec, dflt float64) float64 {
	if spec.TitleSize > 0 {
		return float64(spec.TitleSize)
	}
	return dflt
}

// blockHeight measures the wrapped title block without drawing it
// (wrapTitle is deterministic, so the draw pass lays out the same).
func blockHeight(text string, maxWidth, startSize float64, maxLines int) float64 {
	lines, face, err := wrapTitle(text, maxWidth, startSize, maxLines)
	if err != nil || len(lines) == 0 {
		return 0
	}
	return float64(face.Metrics().Height) / 64 * float64(len(lines))
}

func renderHero(canvas *image.RGBA, spec Spec, sel []MemberImage, res *Result, title string) error {
	bg, ok := resolveBackgroundImage(spec, sel)
	if !ok {
		return fmt.Errorf("the Hero layout needs a background — pick a member's art or add your own image")
	}
	paintBackground(canvas, spec, bg)
	b := canvas.Bounds()
	// Legibility veil on the title's edge.
	size := titleSizeOr(spec, 118)
	bh := blockHeight(title, 880, size, 3)
	switch spec.TitlePosition {
	case "top":
		gradientOverlay(canvas, b, "top", 0.5)
	case "bottom":
		gradientOverlay(canvas, b, "bottom", 0.55)
	default:
		gradientOverlay(canvas, b, "bottom", 0.3)
		gradientOverlay(canvas, b, "top", 0.3)
	}
	if title == "" {
		res.Notes = append(res.Notes, "No title text — the poster is background art only.")
		return nil
	}
	cy := titleCenterY(spec.TitlePosition, float64(b.Dy()), bh)
	_, err := drawTitleBlock(canvas, title, float64(b.Dx())/2, cy, 880, size, 3, titleInk)
	return err
}

func renderTitleCard(canvas *image.RGBA, spec Spec, sel []MemberImage, res *Result, title string) error {
	b := canvas.Bounds()
	accent := color.RGBA{0xe5, 0xa0, 0x0d, 255} // neutral warm fallback
	if bg, ok := resolveBackgroundImage(spec, sel); ok {
		// The user's own art (or a member's): same treatments as
		// Hero, plus the gradient-strength veil.
		paintBackground(canvas, spec, bg)
		g := float64(spec.Gradient) / 100
		if g > 0 {
			darken(canvas, g*0.35)
			gradientOverlay(canvas, b, "bottom", g*0.6)
			gradientOverlay(canvas, b, "top", g*0.4)
		}
	} else {
		// Derived: a gradient from the member posters' own colors —
		// the first poster's upper half down to the last poster's
		// lower half, deepened so type sits on it.
		avail := availablePosters(sel)
		var top, bottom color.RGBA
		if len(avail) > 0 {
			top = mixBlack(regionAverage(avail[0].Poster, 0, 0.5), 0.15)
			bottom = mixBlack(regionAverage(avail[len(avail)-1].Poster, 0.5, 1), 0.5)
			accent = mostSaturated(avail)
		} else {
			top = color.RGBA{0x26, 0x26, 0x2e, 255}
			bottom = color.RGBA{0x0d, 0x0d, 0x10, 255}
			res.Notes = append(res.Notes, "No member art to derive colors from — the card uses the neutral house gradient.")
		}
		verticalGradient(canvas, b, top, bottom)
		gradientOverlay(canvas, b, "bottom", 0.25)
	}
	if title == "" {
		return fmt.Errorf("the Title card needs title text — add the collection's name (or your own wording) and render again")
	}
	size := titleSizeOr(spec, 124)
	bh := blockHeight(title, 860, size, 3)
	cy := titleCenterY(spec.TitlePosition, float64(b.Dy()), bh)
	if _, err := drawTitleBlock(canvas, title, float64(b.Dx())/2, cy, 860, size, 3, titleInk); err != nil {
		return err
	}
	// Accent rule under the title block.
	ruleW, ruleH := 132, 5
	y := int(cy + bh/2 + 34)
	if spec.TitlePosition == "bottom" {
		y = int(cy - bh/2 - 34 - float64(ruleH))
	}
	rect := image.Rect(b.Dx()/2-ruleW/2, y, b.Dx()/2+ruleW/2, y+ruleH)
	for yy := rect.Min.Y; yy < rect.Max.Y; yy++ {
		for xx := rect.Min.X; xx < rect.Max.X; xx++ {
			if image.Pt(xx, yy).In(b) {
				canvas.SetRGBA(xx, yy, accent)
			}
		}
	}
	return nil
}

// mostSaturated returns the average color with the strongest chroma
// among the given members' posters — the palette's accent.
func mostSaturated(avail []MemberImage) color.RGBA {
	best := color.RGBA{0xe5, 0xa0, 0x0d, 255}
	bestScore := -1.0
	for _, m := range avail {
		c := averageColor(m.Poster)
		mx := float64(max3(c.R, c.G, c.B))
		mn := float64(min3(c.R, c.G, c.B))
		score := mx - mn
		if score > bestScore {
			bestScore = score
			best = c
		}
	}
	return best
}

func max3(a, b, c uint8) uint8 {
	if a >= b && a >= c {
		return a
	}
	if b >= c {
		return b
	}
	return c
}

func min3(a, b, c uint8) uint8 {
	if a <= b && a <= c {
		return a
	}
	if b <= c {
		return b
	}
	return c
}
