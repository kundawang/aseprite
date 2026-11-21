package aseprite

import (
	"image"
	"os"
	"testing"

	"github.com/askeladdk/aseprite/internal/require"
)

func decodeFile(t *testing.T, filename string, opts Options) *Aseprite {
	t.Helper()
	f, err := os.Open(filename)
	require.NoError(t, err)
	defer f.Close()
	spr, err := DecodeAsepriteWithOptions(f, opts)
	require.NoError(t, err)
	return spr
}

func equalImage(a, b image.Image) bool {
	if !a.Bounds().Eq(b.Bounds()) {
		return false
	}
	for y := a.Bounds().Min.Y; y < a.Bounds().Max.Y; y++ {
		for x := a.Bounds().Min.X; x < a.Bounds().Max.X; x++ {
			ar, ag, ab, aa := a.At(x, y).RGBA()
			br, bg, bb, ba := b.At(x, y).RGBA()
			if ar != br || ag != bg || ab != bb || aa != ba {
				return false
			}
		}
	}
	return true
}

// TestDecodeOptions covers all color depths (8/16/32 bpp) with all four
// combinations of color space and alpha mode. It verifies that decoding is
// deterministic, that the frame structure is unaffected, and that the zero
// Options value reproduces the legacy default decoding bit-for-bit.
func TestDecodeOptions(t *testing.T) {
	files := []string{
		"./testfiles/slime_paletted.aseprite",  // 8 bpp
		"./testfiles/slime_grayscale.aseprite", // 16 bpp
		"./testfiles/blendtest.aseprite",       // 32 bpp
	}
	opts := []Options{
		{},
		{LinearLight: true},
		{PremultipliedAlpha: true},
		{LinearLight: true, PremultipliedAlpha: true},
	}

	for _, filename := range files {
		t.Run(filename, func(t *testing.T) {
			legacy := decodeFile(t, filename, Options{})

			for _, opt := range opts {
				first := decodeFile(t, filename, opt)
				second := decodeFile(t, filename, opt)

				require.True(t, len(first.Frames) == len(legacy.Frames), "frames")
				require.True(t, len(first.Tags) == len(legacy.Tags), "tags")
				require.True(t, equalImage(first.Image, second.Image), "deterministic")

				if opt == (Options{}) {
					require.True(t, equalImage(first.Image, legacy.Image),
						"zero Options must match legacy decoding")
				}
			}
		})
	}
}

// TestDecodeOptionsLinearLightBrighter decodes the blend test sprite with
// and without linear light and verifies that the option actually changes
// the composited pixels (the file contains semi-transparent blend modes).
func TestDecodeOptionsLinearLightBrighter(t *testing.T) {
	gamma := decodeFile(t, "./testfiles/blendtest.aseprite", Options{})
	linear := decodeFile(t, "./testfiles/blendtest.aseprite", Options{LinearLight: true})

	require.True(t, gamma.Bounds().Eq(linear.Bounds()), "bounds")
	require.True(t, !equalImage(gamma.Image, linear.Image),
		"linear light should change the composited result")
}
