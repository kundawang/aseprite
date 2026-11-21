package aseprite

import (
	"image"
	"image/color"
	"testing"

	"github.com/askeladdk/aseprite/internal/require"
)

// makePanel returns a 12x12 rounded-corner panel with 4px edges.
// Corners are red, edges are green, the center is blue, and the
// corners are rounded off with transparent pixels.
func makePanel() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 12, 12))
	for y := 0; y < 12; y++ {
		for x := 0; x < 12; x++ {
			var c color.NRGBA
			var cx, cy int // rounded corner arc center
			switch {
			case x < 4 && y < 4:
				c, cx, cy = color.NRGBA{200, 60, 60, 255}, 4, 4
			case x >= 8 && y < 4:
				c, cx, cy = color.NRGBA{200, 60, 60, 255}, 7, 4
			case x < 4 && y >= 8:
				c, cx, cy = color.NRGBA{200, 60, 60, 255}, 4, 7
			case x >= 8 && y >= 8:
				c, cx, cy = color.NRGBA{200, 60, 60, 255}, 7, 7
			case y < 4 || y >= 8 || x < 4 || x >= 8:
				c = color.NRGBA{60, 200, 60, 255}
			default:
				c = color.NRGBA{60, 60, 200, 255}
			}
			if cx != 0 && (x-cx)*(x-cx)+(y-cy)*(y-cy) > 16 {
				c = color.NRGBA{}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

// makeCoordImage returns an image where each pixel encodes its own
// coordinates, so that scaling mappings can be verified exactly.
func makeCoordImage(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x * 16), uint8(y * 16), 100, 255})
		}
	}
	return img
}

// referenceNineSlice is an independent, deliberately simple nine-slice
// renderer used to cross-check the package implementation. Edges keep
// their natural size, the center fills the remainder, and anything
// outside the target rectangle is clipped.
func referenceNineSlice(dst *image.NRGBA, src image.Image, sb, center image.Rectangle, target image.Rectangle) {
	if center.Empty() {
		center = sb.Sub(sb.Min)
	}
	cabs := center.Add(sb.Min).Intersect(sb)

	sx := [4]int{sb.Min.X, cabs.Min.X, cabs.Max.X, sb.Max.X}
	sy := [4]int{sb.Min.Y, cabs.Min.Y, cabs.Max.Y, sb.Max.Y}
	dx := [4]int{target.Min.X, target.Min.X + sx[1] - sx[0], target.Max.X - (sx[3] - sx[2]), target.Max.X}
	dy := [4]int{target.Min.Y, target.Min.Y + sy[1] - sy[0], target.Max.Y - (sy[3] - sy[2]), target.Max.Y}

	clip := target.Intersect(dst.Bounds())

	for j := 0; j < 3; j++ {
		for i := 0; i < 3; i++ {
			sw, sh := sx[i+1]-sx[i], sy[j+1]-sy[j]
			dw, dh := dx[i+1]-dx[i], dy[j+1]-dy[j]
			if sw <= 0 || sh <= 0 || dw <= 0 || dh <= 0 {
				continue
			}
			for y := dy[j]; y < dy[j+1]; y++ {
				for x := dx[i]; x < dx[i+1]; x++ {
					if !image.Pt(x, y).In(clip) {
						continue
					}
					sxp := sx[i] + (x-dx[i])*sw/dw
					syp := sy[j] + (y-dy[j])*sh/dh
					dst.Set(x, y, src.At(sxp, syp))
				}
			}
		}
	}
}

func requirePixel(t *testing.T, img image.Image, x, y int, want color.NRGBA) {
	t.Helper()
	got := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
	require.True(t, got == want, "pixel", x, y, "got", got, "want", want)
}

func TestNineSlicePanel(t *testing.T) {
	panel := makePanel()
	backup := image.NewNRGBA(panel.Bounds())
	copy(backup.Pix, panel.Pix)

	s := Slice{Bounds: image.Rect(0, 0, 12, 12), Center: image.Rect(4, 4, 8, 8)}
	target := image.Rect(0, 0, 24, 20)

	dst := image.NewNRGBA(target)
	s.NineSlice(dst, panel, target, SliceOptions{})

	// Compare against the independent reference implementation.
	ref := image.NewNRGBA(target)
	referenceNineSlice(ref, panel, s.Bounds, s.Center, target)
	for y := 0; y < 20; y++ {
		for x := 0; x < 24; x++ {
			require.True(t, dst.NRGBAAt(x, y) == ref.NRGBAAt(x, y),
				"mismatch at", x, y, dst.NRGBAAt(x, y), ref.NRGBAAt(x, y))
		}
	}

	// Rounded corners are preserved: corner regions are drawn unscaled.
	requirePixel(t, dst, 0, 0, color.NRGBA{})
	requirePixel(t, dst, 2, 2, color.NRGBA{200, 60, 60, 255})
	requirePixel(t, dst, 23, 19, color.NRGBA{})
	requirePixel(t, dst, 21, 17, color.NRGBA{200, 60, 60, 255})

	// Stretched edges and center.
	requirePixel(t, dst, 12, 2, color.NRGBA{60, 200, 60, 255})
	requirePixel(t, dst, 2, 10, color.NRGBA{60, 200, 60, 255})
	requirePixel(t, dst, 12, 10, color.NRGBA{60, 60, 200, 255})

	// The source image is not modified.
	for i := range panel.Pix {
		require.True(t, panel.Pix[i] == backup.Pix[i], "source modified at byte", i)
	}
}

func TestNineSliceNarrowTarget(t *testing.T) {
	src := makeCoordImage(12, 12)
	s := Slice{Bounds: image.Rect(0, 0, 12, 12), Center: image.Rect(4, 4, 8, 8)}
	target := image.Rect(0, 0, 6, 12) // narrower than left+right edges

	// Clip: edges keep their natural size, the center vanishes and the
	// right edge is clipped by the target rectangle.
	dst := image.NewNRGBA(target)
	s.NineSlice(dst, src, target, SliceOptions{Overflow: Clip})
	requirePixel(t, dst, 0, 0, color.NRGBA{0, 0, 100, 255})    // src(0,0)
	requirePixel(t, dst, 3, 0, color.NRGBA{48, 0, 100, 255})   // src(3,0)
	requirePixel(t, dst, 4, 0, color.NRGBA{128, 0, 100, 255})  // src(8,0)
	requirePixel(t, dst, 5, 0, color.NRGBA{144, 0, 100, 255})  // src(9,0)
	requirePixel(t, dst, 0, 11, color.NRGBA{0, 176, 100, 255}) // src(0,11)

	// Compress: edges are proportionally shrunk to 3+3, center vanishes.
	dst2 := image.NewNRGBA(target)
	s.NineSlice(dst2, src, target, SliceOptions{Overflow: Compress})
	requirePixel(t, dst2, 0, 0, color.NRGBA{0, 0, 100, 255})   // src(0,0)
	requirePixel(t, dst2, 1, 0, color.NRGBA{32, 0, 100, 255})  // src(2,0)
	requirePixel(t, dst2, 2, 0, color.NRGBA{48, 0, 100, 255})  // src(3,0)
	requirePixel(t, dst2, 3, 0, color.NRGBA{128, 0, 100, 255}) // src(8,0)
	requirePixel(t, dst2, 4, 0, color.NRGBA{160, 0, 100, 255}) // src(10,0)
	requirePixel(t, dst2, 5, 0, color.NRGBA{176, 0, 100, 255}) // src(11,0)
}

func TestNineSlicePivot(t *testing.T) {
	src := makeCoordImage(12, 12)
	target := image.Rect(10, 10, 34, 30) // 24x20

	for _, tt := range []struct {
		Pivot image.Point
		Want  image.Point
	}{
		{image.Pt(0, 0), image.Pt(10, 10)},   // top-left corner
		{image.Pt(2, 2), image.Pt(12, 12)},   // inside corner
		{image.Pt(6, 6), image.Pt(22, 20)},   // center scales
		{image.Pt(10, 10), image.Pt(32, 28)}, // bottom-right edge
		{image.Pt(11, 11), image.Pt(33, 29)}, // corner edge
	} {
		s := Slice{
			Bounds: image.Rect(0, 0, 12, 12),
			Center: image.Rect(4, 4, 8, 8),
			Pivot:  tt.Pivot,
		}
		dst := image.NewNRGBA(image.Rect(0, 0, 40, 40))
		got := s.NineSlice(dst, src, target, SliceOptions{})
		require.True(t, got == tt.Want, "pivot", tt.Pivot, "got", got, "want", tt.Want)
	}
}

func TestNineSliceEmptyCenter(t *testing.T) {
	// A slice without a center is stretched as a whole.
	src := makeCoordImage(4, 4)
	s := Slice{Bounds: image.Rect(0, 0, 4, 4), Pivot: image.Pt(2, 2)}
	target := image.Rect(0, 0, 8, 8)

	dst := image.NewNRGBA(target)
	pivot := s.NineSlice(dst, src, target, SliceOptions{})

	requirePixel(t, dst, 0, 0, color.NRGBA{0, 0, 100, 255})
	requirePixel(t, dst, 1, 1, color.NRGBA{0, 0, 100, 255})
	requirePixel(t, dst, 2, 0, color.NRGBA{16, 0, 100, 255})
	requirePixel(t, dst, 3, 3, color.NRGBA{16, 16, 100, 255})
	requirePixel(t, dst, 7, 7, color.NRGBA{48, 48, 100, 255})
	require.True(t, pivot == image.Pt(4, 4), "pivot", pivot)
}

func TestNineSliceNegativeTarget(t *testing.T) {
	// A target partially outside dst is clipped, not distorted.
	src := makeCoordImage(4, 4)
	s := Slice{Bounds: image.Rect(0, 0, 4, 4)}
	target := image.Rect(-3, -3, 5, 5)

	dst := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	s.NineSlice(dst, src, target, SliceOptions{})

	requirePixel(t, dst, 0, 0, color.NRGBA{16, 16, 100, 255}) // src(1,1)
	requirePixel(t, dst, 4, 4, color.NRGBA{48, 48, 100, 255}) // src(3,3)
	// Pixels outside the target are untouched.
	requirePixel(t, dst, 5, 5, color.NRGBA{})
	requirePixel(t, dst, 7, 0, color.NRGBA{})
}

func TestNineSliceLinear(t *testing.T) {
	// Opaque gradient slice surrounded by transparent sentinel pixels.
	src := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	for y := 2; y < 8; y++ {
		for x := 2; x < 8; x++ {
			src.SetNRGBA(x, y, color.NRGBA{uint8(x * 30), uint8(y * 30), 0, 255})
		}
	}
	backup := image.NewNRGBA(src.Bounds())
	copy(backup.Pix, src.Pix)

	s := Slice{Bounds: image.Rect(2, 2, 8, 8), Center: image.Rect(2, 2, 4, 4)}
	target := image.Rect(0, 0, 10, 10)

	dst := image.NewNRGBA(target)
	s.NineSlice(dst, src, target, SliceOptions{Filter: Linear})

	// The transparent border never bleeds into the target.
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			require.True(t, dst.NRGBAAt(x, y).A == 255, "alpha at", x, y, dst.NRGBAAt(x, y))
		}
	}

	// A center pixel interpolates between its source neighbors.
	c := dst.NRGBAAt(4, 4)
	require.True(t, c.R > 120 && c.R < 150, "interpolated R", c.R)

	// Nearest samples exactly.
	dst2 := image.NewNRGBA(target)
	s.NineSlice(dst2, src, target, SliceOptions{Filter: Nearest})
	requirePixel(t, dst2, 4, 4, color.NRGBA{120, 120, 0, 255}) // src(4,4)

	// The source image is not modified.
	for i := range src.Pix {
		require.True(t, src.Pix[i] == backup.Pix[i], "source modified at byte", i)
	}
}

func TestNineSlicePaletted(t *testing.T) {
	pal := color.Palette{
		color.NRGBA{0, 0, 0, 0},
		color.NRGBA{255, 0, 0, 255},
		color.NRGBA{0, 255, 0, 255},
		color.NRGBA{0, 0, 255, 255},
	}
	src := image.NewPaletted(image.Rect(0, 0, 4, 4), pal)
	for i := range src.Pix {
		src.Pix[i] = uint8(i % 4)
	}

	s := Slice{Bounds: image.Rect(0, 0, 4, 4)}
	dst := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	s.NineSlice(dst, src, dst.Bounds(), SliceOptions{})

	requirePixel(t, dst, 0, 0, color.NRGBA{0, 0, 0, 0})
	requirePixel(t, dst, 2, 0, color.NRGBA{255, 0, 0, 255})
	requirePixel(t, dst, 0, 2, color.NRGBA{0, 0, 0, 0})
	requirePixel(t, dst, 6, 6, color.NRGBA{0, 0, 255, 255}) // src(3,3)
}

func TestDrawSliceMultiFrame(t *testing.T) {
	// Two 4x4 frames side by side on one atlas.
	atlas := image.NewNRGBA(image.Rect(0, 0, 8, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			atlas.SetNRGBA(4+x, y, color.NRGBA{uint8(x*40 + 1), uint8(y*40 + 1), 0, 255})
		}
	}
	backup := image.NewNRGBA(atlas.Bounds())
	copy(backup.Pix, atlas.Pix)

	spr := &Aseprite{
		Image: atlas,
		Frames: []Frame{
			{Bounds: image.Rect(0, 0, 4, 4)},
			{Bounds: image.Rect(4, 0, 8, 4)},
		},
	}

	s := Slice{
		Bounds: image.Rect(0, 0, 4, 4),
		Center: image.Rect(1, 1, 3, 3),
		Pivot:  image.Pt(1, 1),
		Frame:  1,
	}

	target := image.Rect(0, 0, 8, 8)
	dst := image.NewNRGBA(target)
	pivot := spr.DrawSlice(dst, s, target, SliceOptions{})

	// Pixels come from the second frame's atlas page, not the first.
	requirePixel(t, dst, 0, 0, color.NRGBA{1, 1, 0, 255})     // atlas(4,0)
	requirePixel(t, dst, 7, 7, color.NRGBA{121, 121, 0, 255}) // atlas(7,3)
	requirePixel(t, dst, 1, 1, color.NRGBA{41, 41, 0, 255})   // atlas(5,1)
	requirePixel(t, dst, 4, 4, color.NRGBA{81, 81, 0, 255})   // atlas(6,2)
	require.True(t, pivot == image.Pt(1, 1), "pivot", pivot)

	// The atlas is not modified.
	for i := range atlas.Pix {
		require.True(t, atlas.Pix[i] == backup.Pix[i], "atlas modified at byte", i)
	}
}

func TestNineSliceEmptyTarget(t *testing.T) {
	src := makeCoordImage(4, 4)
	s := Slice{Bounds: image.Rect(0, 0, 4, 4), Pivot: image.Pt(2, 2)}
	dst := image.NewNRGBA(image.Rect(0, 0, 4, 4))

	// Degenerate targets draw nothing but still return a pivot.
	p := s.NineSlice(dst, src, image.Rect(2, 2, 2, 2), SliceOptions{})
	require.True(t, p == image.Pt(2, 2), "pivot", p)
	for i, v := range dst.Pix {
		require.True(t, v == 0, "dst modified at byte", i)
	}
}
