package blend

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"testing"
)

// ---- independent floating-point reference ----
//
// The reference below re-derives the documented compositing rules from
// scratch (sRGB EOTF, source-over, single rounding) so that the
// implementation is checked against an independent formulation.

func refToLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func refToSRGB(c float64) float64 {
	if c <= 0.0031308 {
		return 12.92 * c
	}
	return 1.055*math.Pow(c, 1.0/2.4) - 0.055
}

// refBlend applies a per-channel blend mode to straight colors in [0,1].
func refBlend(mode int, d, s float64) float64 {
	switch mode {
	case 0: // Normal
		return s
	case 1: // Multiply
		return d * s
	case 2: // Screen
		return s + d - s*d
	case 3: // Overlay
		if d < 0.5 {
			return 2 * s * d
		}
		return 1 - 2*(1-s)*(1-d)
	case 16: // Addition
		return s + d
	case 17: // Subtract
		return d - s
	}
	panic("mode not covered by reference")
}

func refClamp(x float64) float64 {
	if math.IsNaN(x) || x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// refComposite composites straight sRGB colors d (backdrop) and s (source)
// with channels in [0,1] and returns premultiplied 8-bit RGBA.
func refComposite(d, s [4]float64, mode int, opacity float64, linear bool) [4]uint8 {
	sa := s[3] * opacity // opacity applied exactly once
	da := d[3]
	ws, wd := s, d
	if linear {
		for i := 0; i < 3; i++ {
			ws[i] = refToLinear(ws[i])
			wd[i] = refToLinear(wd[i])
		}
	}
	outa := sa + da*(1-sa)
	var out [4]uint8
	if outa == 0 {
		return out
	}
	for i := 0; i < 3; i++ {
		b := refBlend(mode, wd[i], ws[i])
		v := (b*sa + wd[i]*da*(1-sa)) / outa
		if linear {
			v = refToSRGB(v)
		}
		out[i] = uint8(refClamp(v)*outa*255 + 0.5)
	}
	out[3] = uint8(outa*255 + 0.5)
	return out
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// TestCompositeGradient composites a horizontal black-to-white gradient
// (backdrop) with a vertical one (source) and checks every pixel against
// the independent float reference.
func TestCompositeGradient(t *testing.T) {
	const n = 256
	for _, mode := range []int{0, 1, 2, 3, 16, 17} {
		for _, linear := range []bool{false, true} {
			dst := image.NewRGBA(image.Rect(0, 0, n, n))
			src := image.NewNRGBA(image.Rect(0, 0, n, n))
			for y := 0; y < n; y++ {
				for x := 0; x < n; x++ {
					dst.SetRGBA(x, y, color.RGBA{uint8(x), uint8(x), uint8(x), 255})
					src.SetNRGBA(x, y, color.NRGBA{uint8(y), uint8(y), uint8(y), 255})
				}
			}
			Composite(dst, dst.Bounds(), src, image.Point{}, dst, image.Point{},
				mode, 255, Options{LinearLight: linear})

			maxErr := 0
			for y := 0; y < n; y++ {
				for x := 0; x < n; x++ {
					d := float64(x) / 255
					s := float64(y) / 255
					want := refComposite([4]float64{d, d, d, 1}, [4]float64{s, s, s, 1}, mode, 1, linear)
					got := dst.RGBAAt(x, y)
					for i, g := range [4]uint8{got.R, got.G, got.B, got.A} {
						if e := abs(int(g) - int(want[i])); e > maxErr {
							maxErr = e
						}
					}
				}
			}
			if maxErr > 1 {
				t.Errorf("mode %d linear=%v: max per-pixel error %d > 1", mode, linear, maxErr)
			}
		}
	}
}

// TestCompositeSemiTransparentGlow checks a 50% bright glow over a dark
// background: the linear-light result must be brighter than the legacy
// gamma-encoded result, and both must match the reference.
func TestCompositeSemiTransparentGlow(t *testing.T) {
	dcol := [4]float64{20.0 / 255, 20.0 / 255, 20.0 / 255, 1}
	scol := [4]float64{200.0 / 255, 220.0 / 255, 1, 128.0 / 255}

	var gotR [2]int
	for i, linear := range []bool{false, true} {
		dst := image.NewRGBA(image.Rect(0, 0, 4, 4))
		draw.Draw(dst, dst.Bounds(), &image.Uniform{color.RGBA{20, 20, 20, 255}}, image.Point{}, draw.Src)
		src := &image.Uniform{color.NRGBA{200, 220, 255, 128}}

		Composite(dst, dst.Bounds(), src, image.Point{}, dst, image.Point{},
			0, 255, Options{LinearLight: linear})

		want := refComposite(dcol, scol, 0, 1, linear)
		got := dst.RGBAAt(1, 1)
		gotR[i] = int(got.R)
		for j, g := range [4]uint8{got.R, got.G, got.B, got.A} {
			if e := abs(int(g) - int(want[j])); e > 1 {
				t.Errorf("linear=%v channel %d: got %d, want %d (err %d)", linear, j, g, want[j], e)
			}
		}
	}
	if gotR[1] <= gotR[0] {
		t.Errorf("linear-light glow (%d) should be brighter than gamma-encoded glow (%d)", gotR[1], gotR[0])
	}
}

// TestCompositeOpacityAppliedOnce verifies that layer opacity scales the
// source alpha a single time: an opaque red cel at opacity 128 over a
// transparent backdrop must yield alpha 128, not 64.
func TestCompositeOpacityAppliedOnce(t *testing.T) {
	for _, linear := range []bool{false, true} {
		dst := image.NewRGBA(image.Rect(0, 0, 1, 1)) // transparent
		src := &image.Uniform{color.NRGBA{255, 0, 0, 255}}
		Composite(dst, dst.Bounds(), src, image.Point{}, dst, image.Point{},
			0, 128, Options{LinearLight: linear})
		got := dst.RGBAAt(0, 0)
		if got.A != 128 {
			t.Errorf("linear=%v: alpha = %d, want 128 (opacity applied once)", linear, got.A)
		}
		if got.R != 128 || got.G != 0 || got.B != 0 {
			t.Errorf("linear=%v: got premul %v, want {128 0 0 128}", linear, got)
		}
	}
}

// TestCompositeEdgeCases pins down the deterministic rules for transparent,
// opaque, out-of-range (HDR) and degenerate colors.
func TestCompositeEdgeCases(t *testing.T) {
	rgba := func(r, g, b, a uint8) *image.Uniform { return &image.Uniform{color.RGBA{r, g, b, a}} }
	nrgba := func(r, g, b, a uint8) *image.Uniform { return &image.Uniform{color.NRGBA{r, g, b, a}} }

	composite1px := func(dst, src *image.Uniform, mode int, opacity uint8, opts Options) color.RGBA {
		img := image.NewRGBA(image.Rect(0, 0, 1, 1))
		draw.Draw(img, img.Bounds(), dst, image.Point{}, draw.Src)
		Composite(img, img.Bounds(), src, image.Point{}, img, image.Point{}, mode, opacity, opts)
		return img.RGBAAt(0, 0)
	}

	for _, linear := range []bool{false, true} {
		opts := Options{LinearLight: linear}

		// Transparent source leaves the backdrop untouched.
		if got := composite1px(rgba(10, 20, 30, 255), nrgba(0, 0, 0, 0), 0, 255, opts); got != (color.RGBA{10, 20, 30, 255}) {
			t.Errorf("linear=%v transparent src: got %v", linear, got)
		}
		// Zero opacity leaves the backdrop untouched.
		if got := composite1px(rgba(10, 20, 30, 255), nrgba(200, 200, 200, 255), 0, 0, opts); got != (color.RGBA{10, 20, 30, 255}) {
			t.Errorf("linear=%v zero opacity: got %v", linear, got)
		}
		// Opaque Normal replaces the backdrop exactly.
		if got := composite1px(rgba(10, 20, 30, 255), nrgba(1, 2, 3, 255), 0, 255, opts); got != (color.RGBA{1, 2, 3, 255}) {
			t.Errorf("linear=%v opaque normal: got %v", linear, got)
		}
		// Addition clamps HDR overflow to 255.
		if got := composite1px(rgba(200, 0, 0, 255), nrgba(200, 0, 0, 255), 16, 255, opts); got.R != 255 {
			t.Errorf("linear=%v addition overflow: got %v", linear, got)
		}
		// Subtract clamps below zero to 0.
		if got := composite1px(rgba(50, 0, 0, 255), nrgba(200, 0, 0, 255), 17, 255, opts); got.R != 0 {
			t.Errorf("linear=%v subtract underflow: got %v", linear, got)
		}
		// Divide by zero saturates to 255; 0/0 (NaN) becomes 0.
		if got := composite1px(rgba(100, 0, 0, 255), nrgba(0, 0, 0, 255), 18, 255, opts); got.R != 255 {
			t.Errorf("linear=%v divide by zero: got %v", linear, got)
		}
		if got := composite1px(rgba(0, 0, 0, 255), nrgba(0, 0, 0, 255), 18, 255, opts); got.R != 0 {
			t.Errorf("linear=%v divide 0/0: got %v", linear, got)
		}
		// Fully transparent result over transparent backdrop.
		if got := composite1px(rgba(0, 0, 0, 0), nrgba(0, 0, 0, 0), 0, 255, opts); got != (color.RGBA{}) {
			t.Errorf("linear=%v transparent over transparent: got %v", linear, got)
		}
	}
}

// TestCompositePremultiplied checks the premultiplied-alpha mode against an
// independent reference computed in premultiplied form.
func TestCompositePremultiplied(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 1, 1))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{color.RGBA{40, 40, 40, 255}}, image.Point{}, draw.Src)
	src := &image.Uniform{color.RGBA{100, 150, 200, 200}} // already premultiplied

	Composite(dst, dst.Bounds(), src, image.Point{}, dst, image.Point{},
		0, 255, Options{Premultiplied: true})

	// Reference: working colors are the premultiplied values as-is.
	sa, da := 200.0/255, 1.0
	outa := sa + da*(1-sa)
	want := [3]uint8{}
	for i, v := range [3]float64{100.0 / 255, 150.0 / 255, 200.0 / 255} {
		d := 40.0 / 255
		out := v*sa + d*da*(1-sa) // Normal: blended color is the source
		want[i] = uint8(out/outa*outa*255 + 0.5)
	}
	got := dst.RGBAAt(0, 0)
	for i, g := range [3]uint8{got.R, got.G, got.B} {
		if e := abs(int(g) - int(want[i])); e > 1 {
			t.Errorf("channel %d: got %d, want %d", i, g, want[i])
		}
	}
	if got.A != 255 {
		t.Errorf("alpha: got %d, want 255", got.A)
	}
}

// TestCompositeUnknownMode verifies that out-of-range blend mode indices
// deterministically fall back to Normal.
func TestCompositeUnknownMode(t *testing.T) {
	for _, mode := range []int{-1, 19, 99} {
		dstA := image.NewRGBA(image.Rect(0, 0, 2, 2))
		dstB := image.NewRGBA(image.Rect(0, 0, 2, 2))
		draw.Draw(dstA, dstA.Bounds(), &image.Uniform{color.RGBA{30, 60, 90, 255}}, image.Point{}, draw.Src)
		draw.Draw(dstB, dstB.Bounds(), &image.Uniform{color.RGBA{30, 60, 90, 255}}, image.Point{}, draw.Src)
		src := &image.Uniform{color.NRGBA{200, 100, 50, 128}}

		Composite(dstA, dstA.Bounds(), src, image.Point{}, dstA, image.Point{}, mode, 255, Options{LinearLight: true})
		Composite(dstB, dstB.Bounds(), src, image.Point{}, dstB, image.Point{}, 0, 255, Options{LinearLight: true})

		if dstA.RGBAAt(1, 1) != dstB.RGBAAt(1, 1) {
			t.Errorf("mode %d: got %v, want Normal fallback %v", mode, dstA.RGBAAt(1, 1), dstB.RGBAAt(1, 1))
		}
	}
}

// TestCompositeDeterministicAndPure verifies that compositing the same
// inputs twice gives identical pixels and that the source images are not
// modified.
func TestCompositeDeterministicAndPure(t *testing.T) {
	const n = 16
	newBackdrop := func() *image.RGBA {
		img := image.NewRGBA(image.Rect(0, 0, n, n))
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				img.SetRGBA(x, y, color.RGBA{uint8(x * 16), uint8(y * 16), 128, 255})
			}
		}
		return img
	}
	src := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			src.SetNRGBA(x, y, color.NRGBA{uint8(255 - x*16), uint8(255 - y*16), 200, uint8(x*8 + y*8)})
		}
	}
	snap := make([]byte, len(src.Pix))
	copy(snap, src.Pix)

	opts := Options{LinearLight: true, Premultiplied: true}
	a, b := newBackdrop(), newBackdrop()
	for _, mode := range []int{0, 1, 2, 3, 6, 7, 12, 14, 16, 18} {
		Composite(a, a.Bounds(), src, image.Point{}, a, image.Point{}, mode, 200, opts)
		Composite(b, b.Bounds(), src, image.Point{}, b, image.Point{}, mode, 200, opts)
	}
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			t.Fatalf("non-deterministic result at byte %d", i)
		}
	}
	for i := range src.Pix {
		if src.Pix[i] != snap[i] {
			t.Fatalf("source image modified at byte %d", i)
		}
	}
}
