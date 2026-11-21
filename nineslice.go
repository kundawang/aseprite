package aseprite

import (
	"image"
	"image/color"
	"image/draw"
	"math"
)

// Filter specifies the interpolation filter used when scaling
// the regions of a nine-slice.
type Filter int

const (
	// Nearest applies nearest-neighbor interpolation. It is the default
	// and preserves hard pixel edges.
	Nearest Filter = iota

	// Linear applies bilinear interpolation for smoother scaling.
	// Samples are clamped to the source region, so transparent borders
	// never bleed in from neighboring pixels.
	Linear
)

// Overflow specifies how a nine-slice is drawn when the target rectangle
// is smaller than the sum of the edge and center sizes.
type Overflow int

const (
	// Clip draws the edges at their natural size and clips whatever does
	// not fit the target rectangle. The center shrinks first, then the
	// bottom and right edges are clipped. It is the default.
	Clip Overflow = iota

	// Compress proportionally shrinks the edges to fit the target
	// rectangle. The center is drawn only if room remains.
	Compress
)

// SliceOptions configures nine-slice drawing.
type SliceOptions struct {
	// Filter is the interpolation filter used to scale the regions.
	// The zero value is Nearest.
	Filter Filter

	// Overflow determines the behavior when the target rectangle is
	// smaller than the sum of the edge and center sizes.
	// The zero value is Clip.
	Overflow Overflow
}

// NineSlice draws the slice image read from src at s.Bounds into dst,
// scaled to the target rectangle using nine-slice scaling, and returns
// the slice pivot mapped into dst coordinates.
//
// The source image is split by s.Center into four corners, four edges
// and a center. The corners are drawn unscaled, the edges are scaled
// along one axis and the center is scaled along both axes to fill the
// remainder of the target rectangle. If s.Center is empty, the slice
// has no nine-slice information and the entire Bounds image is
// stretched to the target rectangle.
//
// The target rectangle may be positioned anywhere, including partially
// or fully outside dst; only the visible part is drawn. src is never
// modified. Use Aseprite.DrawSlice to draw multi-frame slices directly
// from the texture atlas.
func (s Slice) NineSlice(dst draw.Image, src image.Image, target image.Rectangle, opts SliceOptions) image.Point {
	return nineSlice(dst, src, s.Bounds, s.Center, s.Pivot, target, opts)
}

// DrawSlice draws slice s into dst, scaled to the target rectangle using
// nine-slice scaling, and returns the slice pivot mapped into dst
// coordinates. The slice image is read from the texture atlas at the
// location of the frame the slice key applies to (Slice.Frame).
// See Slice.NineSlice for details.
func (spr *Aseprite) DrawSlice(dst draw.Image, s Slice, target image.Rectangle, opts SliceOptions) image.Point {
	bounds := s.Bounds
	if s.Frame >= 0 && s.Frame < len(spr.Frames) {
		bounds = bounds.Add(spr.Frames[s.Frame].Bounds.Min)
	}
	return nineSlice(dst, spr.Image, bounds, s.Center, s.Pivot, target, opts)
}

func nineSlice(dst draw.Image, src image.Image, bounds, center image.Rectangle, pivot image.Point, target image.Rectangle, opts SliceOptions) image.Point {
	ob := bounds
	sb := bounds.Intersect(src.Bounds())

	// A slice without a center is stretched as a whole.
	cabs := sb
	if !center.Empty() {
		cabs = center.Add(ob.Min).Intersect(sb)
	}

	left := cabs.Min.X - sb.Min.X
	top := cabs.Min.Y - sb.Min.Y
	right := sb.Max.X - cabs.Max.X
	bottom := sb.Max.Y - cabs.Max.Y

	tw, th := target.Dx(), target.Dy()

	dl, dr := left, right
	dt, db := top, bottom

	if opts.Overflow == Compress {
		dl, dr = fitEdges(dl, dr, tw)
		dt, db = fitEdges(dt, db, th)
	}

	cw := maxInt(tw-dl-dr, 0)
	ch := maxInt(th-dt-db, 0)

	// Destination segment boundaries.
	dx := [4]int{target.Min.X, target.Min.X + dl, target.Min.X + dl + cw, target.Min.X + dl + cw + dr}
	dy := [4]int{target.Min.Y, target.Min.Y + dt, target.Min.Y + dt + ch, target.Min.Y + dt + ch + db}

	// Source segment boundaries.
	sx := [4]int{sb.Min.X, cabs.Min.X, cabs.Max.X, sb.Max.X}
	sy := [4]int{sb.Min.Y, cabs.Min.Y, cabs.Max.Y, sb.Max.Y}

	clip := target.Intersect(dst.Bounds())

	for j := 0; j < 3; j++ {
		for i := 0; i < 3; i++ {
			sr := image.Rect(sx[i], sy[j], sx[i+1], sy[j+1])
			dr := image.Rect(dx[i], dy[j], dx[i+1], dy[j+1])
			scaleRegion(dst, dr, src, sr, opts.Filter, clip)
		}
	}

	// Map the pivot into dst coordinates.
	ap := pivot.Add(ob.Min)
	return image.Pt(
		mapPivot(ap.X, sb.Min.X, cabs.Min.X, cabs.Max.X, sb.Max.X, dx[0], dx[1], dx[2], dx[3]),
		mapPivot(ap.Y, sb.Min.Y, cabs.Min.Y, cabs.Max.Y, sb.Max.Y, dy[0], dy[1], dy[2], dy[3]),
	)
}

// fitEdges proportionally shrinks edges a and b when their sum exceeds
// size, so that their sum equals size.
func fitEdges(a, b, size int) (int, int) {
	if size < 0 {
		size = 0
	}
	if a+b <= size || a+b == 0 {
		return a, b
	}
	na := int(math.Round(float64(a) * float64(size) / float64(a+b)))
	return na, size - na
}

// mapPivot maps pivot coordinate p from source segments
// (smin, cmin, cmax, smax) to destination segments (d0, d1, d2, d3).
// A pivot in an edge keeps its edge offset, a pivot in the center
// scales with the center.
func mapPivot(p, smin, cmin, cmax, smax, d0, d1, d2, d3 int) int {
	switch {
	case p <= cmin:
		return d0 + (p - smin)
	case p >= cmax:
		return d3 - (smax - p)
	case cmax == cmin:
		return d1
	default:
		return d1 + int(math.Round(float64(p-cmin)*float64(d2-d1)/float64(cmax-cmin)))
	}
}

// scaleRegion scales source rectangle sr to destination rectangle dr.
// Only pixels inside both dr and clip are written. The mapping from dr
// to sr is computed from the full (unclipped) rectangles, so clipping
// never distorts the image.
func scaleRegion(dst draw.Image, dr image.Rectangle, src image.Image, sr image.Rectangle, filter Filter, clip image.Rectangle) {
	if sr.Empty() || dr.Empty() {
		return
	}

	r := dr.Intersect(clip)
	if r.Empty() {
		return
	}

	scaleX := float64(sr.Dx()) / float64(dr.Dx())
	scaleY := float64(sr.Dy()) / float64(dr.Dy())

	for y := r.Min.Y; y < r.Max.Y; y++ {
		fy := (float64(y-dr.Min.Y)+0.5)*scaleY + float64(sr.Min.Y)
		for x := r.Min.X; x < r.Max.X; x++ {
			fx := (float64(x-dr.Min.X)+0.5)*scaleX + float64(sr.Min.X)
			if filter == Linear {
				dst.Set(x, y, sampleLinear(src, sr, fx, fy))
			} else {
				dst.Set(x, y, sampleNearest(src, sr, fx, fy))
			}
		}
	}
}

// sampleNearest returns the nearest-neighbor sample at (fx, fy),
// clamped to source rectangle sr.
func sampleNearest(src image.Image, sr image.Rectangle, fx, fy float64) color.Color {
	ix := clampInt(int(math.Floor(fx)), sr.Min.X, sr.Max.X-1)
	iy := clampInt(int(math.Floor(fy)), sr.Min.Y, sr.Max.Y-1)
	return src.At(ix, iy)
}

// sampleLinear returns the bilinear sample at (fx, fy). The four taps
// are clamped to source rectangle sr, so pixels outside the slice
// (including transparent borders) never contribute. Interpolation is
// done in premultiplied space.
func sampleLinear(src image.Image, sr image.Rectangle, fx, fy float64) color.Color {
	gx, gy := fx-0.5, fy-0.5
	x0 := clampInt(int(math.Floor(gx)), sr.Min.X, sr.Max.X-1)
	y0 := clampInt(int(math.Floor(gy)), sr.Min.Y, sr.Max.Y-1)
	x1 := clampInt(x0+1, sr.Min.X, sr.Max.X-1)
	y1 := clampInt(y0+1, sr.Min.Y, sr.Max.Y-1)
	tx := gx - math.Floor(gx)
	ty := gy - math.Floor(gy)

	r00, g00, b00, a00 := src.At(x0, y0).RGBA()
	r10, g10, b10, a10 := src.At(x1, y0).RGBA()
	r01, g01, b01, a01 := src.At(x0, y1).RGBA()
	r11, g11, b11, a11 := src.At(x1, y1).RGBA()

	lerp := func(c00, c10, c01, c11 uint32) uint16 {
		lo := float64(c00)*(1-tx) + float64(c10)*tx
		hi := float64(c01)*(1-tx) + float64(c11)*tx
		return uint16(lo*(1-ty) + hi*ty + 0.5)
	}

	return color.RGBA64{
		R: lerp(r00, r10, r01, r11),
		G: lerp(g00, g10, g01, g11),
		B: lerp(b00, b10, b01, b11),
		A: lerp(a00, a10, a01, a11),
	}
}

func clampInt(x, lo, hi int) int {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
