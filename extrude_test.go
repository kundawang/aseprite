package aseprite

import (
	"image"
	"image/color"
	"testing"

	"github.com/askeladdk/aseprite/internal/require"
)

// pixelAt reads the raw pixel value of x,y as a uint32.
func pixelAt(t *testing.T, img image.Image, x, y int) uint32 {
	t.Helper()
	switch a := img.(type) {
	case *image.RGBA:
		i := a.PixOffset(x, y)
		return uint32(a.Pix[i])
	case *image.Gray16:
		i := a.PixOffset(x, y)
		return uint32(a.Pix[i])
	case *image.Paletted:
		return uint32(a.ColorIndexAt(x, y))
	default:
		t.Fatalf("unsupported image type %T", img)
		return 0
	}
}

// TestExtrudePaddingFormats verifies border extrusion for every pixel
// format the atlas can use, so palette indices, gray values and RGBA
// bytes are all preserved rather than converted through color.Model.
func TestExtrudePaddingFormats(t *testing.T) {
	const padding = 1
	content := image.Rect(padding, padding, padding+4, padding+4)
	padded := content.Inset(-padding)

	for _, tt := range []struct {
		name string
		make func(r image.Rectangle) drawImage
	}{
		{
			name: "rgba",
			make: func(r image.Rectangle) drawImage { return newRGBA(r) },
		},
		{
			name: "gray16",
			make: func(r image.Rectangle) drawImage { return newGray16(r) },
		},
		{
			name: "paletted",
			make: func(r image.Rectangle) drawImage { return newPaletted(r) },
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			img := tt.make(padded)
			ncolors := 0
			if p, ok := img.(*image.Paletted); ok {
				ncolors = len(p.Palette)
			}

			// Give content texels distinct values based on position.
			for y := content.Min.Y; y < content.Max.Y; y++ {
				for x := content.Min.X; x < content.Max.X; x++ {
					v := uint32((x-content.Min.X)*16 + (y - content.Min.Y) + 1)
					if ncolors > 0 {
						v = v % uint32(ncolors)
					}
					setPixel(img, x, y, v)
				}
			}

			extrudePadding(img, content, padding)

			// Every padding texel must equal its clamped source texel.
			for y := padded.Min.Y; y < padded.Max.Y; y++ {
				for x := padded.Min.X; x < padded.Max.X; x++ {
					if x >= content.Min.X && x < content.Max.X &&
						y >= content.Min.Y && y < content.Max.Y {
						continue
					}
					sx := clampInt(x, content.Min.X, content.Max.X-1)
					sy := clampInt(y, content.Min.Y, content.Max.Y-1)
					got := pixelAt(t, img, x, y)
					want := pixelAt(t, img, sx, sy)
					require.True(t, got == want, "padding texel", tt.name, x, y, got, want)
				}
			}

			// Content texels must be untouched by extrusion.
			for y := content.Min.Y; y < content.Max.Y; y++ {
				for x := content.Min.X; x < content.Max.X; x++ {
					want := uint32((x-content.Min.X)*16 + (y - content.Min.Y) + 1)
					if ncolors > 0 {
						want = want % uint32(ncolors)
					}
					require.True(t, pixelAt(t, img, x, y) == want, "content texel preserved")
				}
			}
		})
	}
}

// drawImage is the draw.Image interface alias used by the tests.
type drawImage = interface {
	image.Image
	Set(x, y int, c color.Color)
}

func newRGBA(r image.Rectangle) drawImage   { return image.NewRGBA(r) }
func newGray16(r image.Rectangle) drawImage { return image.NewGray16(r) }
func newPaletted(r image.Rectangle) drawImage {
	return image.NewPaletted(r, color.Palette{
		color.Transparent,
		color.NRGBA{R: 255, A: 255},
	})
}

func setPixel(img drawImage, x, y int, v uint32) {
	switch a := img.(type) {
	case *image.RGBA:
		i := a.PixOffset(x, y)
		a.Pix[i] = byte(v)
		a.Pix[i+1] = byte(v)
		a.Pix[i+2] = byte(v)
		a.Pix[i+3] = 255
	case *image.Gray16:
		i := a.PixOffset(x, y)
		a.Pix[i] = byte(v)
		a.Pix[i+1] = 0
	case *image.Paletted:
		a.SetColorIndex(x, y, byte(v%uint32(len(a.Palette))))
	}
}
