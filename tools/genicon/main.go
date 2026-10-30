// Command genicon renders the app icon (purple tile with a speaker) as PNGs
// for go-winres: go run ./tools/genicon
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// The shapes match the logo in ui/index.html, in a 64x64 coordinate space.
func inside(x, y float64) (bg, fg bool) {
	bg = inRoundRect(x, y, 0, 0, 64, 64, 15)
	if !bg {
		return
	}
	// speaker: box + cone
	if x >= 14 && x <= 22 && y >= 26 && y <= 38 {
		return bg, true
	}
	if x >= 22 && x <= 33 {
		t := (x - 22) / 11
		top, bottom := 26-9*t, 38+9*t
		if y >= top && y <= bottom {
			return bg, true
		}
	}
	// Sound waves: SVG arcs "M39 24a11 11 0 0 1 0 16" and "M44 18a19 19 0 0 1 0 28",
	// stroke 4.5 with round caps.
	for _, w := range []struct{ endX, halfChord, r float64 }{{39, 8, 11}, {44, 14, 19}} {
		cx := w.endX - math.Sqrt(w.r*w.r-w.halfChord*w.halfChord)
		span := math.Atan2(w.halfChord, w.endX-cx)
		dx, dy := x-cx, y-32
		if math.Abs(math.Hypot(dx, dy)-w.r) <= 2.25 && math.Abs(math.Atan2(dy, dx)) <= span {
			return bg, true
		}
		for _, ey := range []float64{32 - w.halfChord, 32 + w.halfChord} {
			if math.Hypot(x-w.endX, y-ey) <= 2.25 {
				return bg, true
			}
		}
	}
	return bg, false
}

func inRoundRect(x, y, x0, y0, x1, y1, r float64) bool {
	if x < x0 || y < y0 || x > x1 || y > y1 {
		return false
	}
	cx := math.Max(x0+r, math.Min(x, x1-r))
	cy := math.Max(y0+r, math.Min(y, y1-r))
	return math.Hypot(x-cx, y-cy) <= r
}

func render(size int) *image.NRGBA {
	const ss = 8 // supersampling per axis
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	purple := [3]float64{0x91, 0x47, 0xff}
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var bgN, fgN int
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) * 64 / float64(size)
					y := (float64(py) + (float64(sy)+0.5)/ss) * 64 / float64(size)
					bg, fg := inside(x, y)
					if bg {
						bgN++
					}
					if fg {
						fgN++
					}
				}
			}
			if bgN == 0 {
				continue
			}
			f := float64(fgN) / float64(bgN)
			mix := func(c float64) uint8 { return uint8(math.Round(c*(1-f) + 255*f)) }
			img.SetNRGBA(px, py, color.NRGBA{
				R: mix(purple[0]), G: mix(purple[1]), B: mix(purple[2]),
				A: uint8(math.Round(255 * float64(bgN) / (ss * ss))),
			})
		}
	}
	return img
}

func main() {
	for _, size := range []int{16, 20, 24, 32, 40, 48, 64, 256} {
		path := filepath.Join("winres", fmt.Sprintf("icon_%d.png", size))
		f, err := os.Create(path)
		if err != nil {
			panic(err)
		}
		if err := png.Encode(f, render(size)); err != nil {
			panic(err)
		}
		f.Close()
		fmt.Println("wrote", path)
	}
}
