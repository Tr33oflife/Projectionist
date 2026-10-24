package postergen

// Proof renders for the owner — NOT part of the normal suite.
// Run with:
//
//	PCM_PROOF=1 go test ./internal/postergen/ -run ProofRenders -v
//
// Writes generator-proof-{mosaic,flower,spiral,hero,titlecard}.png
// into the project root (~/workspace/plex-collection-manager/),
// rendered from vivid synthetic posters (per-member gradients with
// their index set in the bundled font), so the layouts read clearly.

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

func proofPoster(i int) *image.RGBA {
	c1 := distinctColor(i)
	c2 := distinctColor(i + 40)
	img := image.NewRGBA(image.Rect(0, 0, 400, 600))
	verticalGradient(img, img.Bounds(), c1, mixBlack(c2, 0.35))
	_, _ = drawTitleBlock(img, fmt.Sprint(i+1), 200, 300, 300, 150, 1,
		color.RGBA{255, 255, 255, 255})
	return img
}

func TestProofRenders(t *testing.T) {
	if os.Getenv("PCM_PROOF") == "" {
		t.Skip("proof renders only (set PCM_PROOF=1)")
	}
	root := filepath.Join("..", "..", "..")
	mk := func(n int) []MemberImage {
		out := make([]MemberImage, 0, n)
		for i := 0; i < n; i++ {
			m := MemberImage{Key: fmt.Sprint(1000 + i), Title: fmt.Sprintf("Proof %d", i+1), Poster: proofPoster(i)}
			if i == 0 {
				m.Art = patternedArt()
			}
			out = append(out, m)
		}
		return out
	}
	jobs := []struct {
		file string
		spec Spec
		n    int
	}{
		{"generator-proof-mosaic.png", Spec{Template: "mosaic", Title: "Spooky Season", Grid: "3x3", Plate: true}, 12},
		{"generator-proof-flower.png", Spec{Template: "mosaic", Title: "Spooky Season", Grid: "flower", Plate: true}, 25},
		{"generator-proof-spiral.png", Spec{Template: "mosaic", Title: "Spooky Season", Grid: "spiral", Plate: true}, 25},
		{"generator-proof-hero.png", Spec{Template: "hero", Title: "Halloween Classics", TitlePosition: "bottom",
			FocalX: 0.5, FocalY: 0.5, Background: Background{Kind: "member", MemberKey: "1000", UseArt: true}}, 12},
		{"generator-proof-titlecard.png", Spec{Template: "titlecard", Title: "Spooky Season", TitlePosition: "center"}, 12},
	}
	for _, j := range jobs {
		res, err := Render(j.spec, mk(j.n))
		if err != nil {
			t.Fatalf("%s: %v", j.file, err)
		}
		p := filepath.Join(root, j.file)
		if err := os.WriteFile(p, res.PNG, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes, used %d members)", p, len(res.PNG), res.UsedMembers)
	}
}
