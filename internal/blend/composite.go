package blend

import (
	"image"
	"image/color"
	"image/draw"
	"math"
)

// Options configures the color space and alpha handling used by Composite.
// The zero value selects gamma-encoded sRGB blending with straight
// (non-premultiplied) alpha.
type Options struct {
	// LinearLight blends and composites colors in linearized sRGB space
	// instead of gamma-encoded sRGB space. Alpha is always linear.
	LinearLight bool

	// Premultiplied treats colors as alpha-premultiplied: blend modes are
	// applied to the premultiplied channels as-is. Otherwise colors are
	// un-premultiplied before blending and the result is re-premultiplied.
	Premultiplied bool
}

// Composite blends src0 (top layer) over src1 (bottom layer) using the blend
// mode at index mode (see Modes) and stores the composited result in dst.
//
// Unlike Blend, Composite performs blending, layer opacity and source-over
// compositing in a single pass, so opacity scales the source alpha exactly
// once. Transparent source pixels leave the backdrop untouched. Modes are
// selected by index; out-of-range indices fall back to Normal. Blend results
// outside [0,1] (e.g. from Addition or Divide) are clamped and NaN results
// become 0, so transparent, semi-transparent, out-of-range and opaque colors
// all have stable, deterministic outcomes. Only dst is modified; src0, src1
// and the package-level mode tables are never mutated.
func Composite(dst draw.Image, r image.Rectangle, src0 image.Image, sp0 image.Point,
	src1 image.Image, sp1 image.Point, mode int, opacity uint8, opts Options) {
	if mode < 0 || mode >= len(kernels) {
		mode = 0 // unknown modes fall back to Normal
	}
	k := kernels[mode]

	clip(dst, &r, src0, &sp0, src1, &sp1)
	if r.Empty() {
		return
	}

	o := float64(opacity) / math.MaxUint8

	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			s := color2rgbaf64(src0.At(x+sp0.X, y+sp0.Y))
			if s.a == 0 {
				continue // transparent pixels are not blended
			}
			sa0 := s.a
			s.a *= o // layer opacity is applied exactly once
			if s.a == 0 {
				continue
			}
			d := color2rgbaf64(src1.At(x+sp1.X, y+sp1.Y))

			// Working colors in [0,1]. RGBA() returns premultiplied values.
			var ws, wd rgbaf64
			if opts.Premultiplied {
				ws = rgbaf64{s.r * o / max, s.g * o / max, s.b * o / max, s.a / max}
				wd = rgbaf64{d.r / max, d.g / max, d.b / max, d.a / max}
			} else {
				ws = rgbaf64{unpremul(s.r, sa0), unpremul(s.g, sa0), unpremul(s.b, sa0), s.a / max}
				wd = rgbaf64{unpremul(d.r, d.a), unpremul(d.g, d.a), unpremul(d.b, d.a), d.a / max}
			}

			if opts.LinearLight {
				ws.r = srgbToLinear(ws.r)
				ws.g = srgbToLinear(ws.g)
				ws.b = srgbToLinear(ws.b)
				wd.r = srgbToLinear(wd.r)
				wd.g = srgbToLinear(wd.g)
				wd.b = srgbToLinear(wd.b)
			}

			b := k(wd, ws)

			// Source-over compositing in the working space.
			outa := ws.a + wd.a*(1-ws.a)
			if outa <= 0 {
				dst.Set(x, y, color.RGBA{})
				continue
			}
			rr := (b.r*ws.a + wd.r*wd.a*(1-ws.a)) / outa
			rg := (b.g*ws.a + wd.g*wd.a*(1-ws.a)) / outa
			rb := (b.b*ws.a + wd.b*wd.a*(1-ws.a)) / outa

			if opts.LinearLight {
				rr = linearToSrgb(rr)
				rg = linearToSrgb(rg)
				rb = linearToSrgb(rb)
			}

			// Round exactly once, storing premultiplied colors.
			dst.Set(x, y, color.RGBA{
				R: clampToByte(clamp01(rr) * outa * math.MaxUint8 + 0.5),
				G: clampToByte(clamp01(rg) * outa * math.MaxUint8 + 0.5),
				B: clampToByte(clamp01(rb) * outa * math.MaxUint8 + 0.5),
				A: clampToByte(outa*math.MaxUint8 + 0.5),
			})
		}
	}
}

// kernel blends the working colors d (backdrop) and s (source), whose
// channels are in [0,1], and returns the blended color. The alpha channel
// is unused; compositing is handled by Composite. Results may lie outside
// [0,1]; Composite clamps them.
type kernel func(d, s rgbaf64) rgbaf64

func perChannel(blend func(d, s float64) float64) kernel {
	return func(d, s rgbaf64) rgbaf64 {
		return rgbaf64{r: blend(d.r, s.r), g: blend(d.g, s.g), b: blend(d.b, s.b)}
	}
}

// kernels mirrors Modes but operates on normalized [0,1] working colors
// without rounding, so Composite can keep full float64 precision until the
// final store. The legacy Modes table is left untouched.
var kernels = [len(Modes)]kernel{
	0:  func(d, s rgbaf64) rgbaf64 { return s }, // Normal
	1:  perChannel(func(d, s float64) float64 { return d * s }),
	2:  perChannel(func(d, s float64) float64 { return s + d - s*d }),
	3: perChannel(func(d, s float64) float64 {
		if d < 0.5 {
			return 2 * s * d
		}
		return 1 - 2*(1-s)*(1-d)
	}),
	4: perChannel(math.Min),
	5: perChannel(math.Max),
	6: perChannel(func(d, s float64) float64 {
		if s == 1 {
			return 1
		}
		return math.Min(1, d/(1-s))
	}),
	7: perChannel(func(d, s float64) float64 {
		if s == 0 {
			return 0
		}
		return math.Max(0, 1-(1-d)/s)
	}),
	8: perChannel(func(d, s float64) float64 {
		if s > 0.5 {
			return d + (1-d)*((s-0.5)/0.5)
		}
		return d * s / 0.5
	}),
	9:  perChannel(func(d, s float64) float64 { return d * (d + 2*s*(1-d)) }),
	10: perChannel(func(d, s float64) float64 { return math.Abs(s - d) }),
	11: perChannel(func(d, s float64) float64 { return s + d - 2*s*d }),
	12: kernelHue,
	13: kernelSaturation,
	14: kernelColor,
	15: kernelLuminosity,
	16: perChannel(func(d, s float64) float64 { return s + d }),
	17: perChannel(func(d, s float64) float64 { return d - s }),
	18: perChannel(func(d, s float64) float64 { return d / s }),
}

func kernelHue(d, s rgbaf64) rgbaf64 {
	sh, ss, _ := rgb2hslf(s.r, s.g, s.b)
	if ss == 0 {
		return d
	}
	_, ds, dl := rgb2hslf(d.r, d.g, d.b)
	r, g, b := hsl2rgbf(sh, ds, dl)
	return rgbaf64{r: r, g: g, b: b}
}

func kernelSaturation(d, s rgbaf64) rgbaf64 {
	dh, _, dl := rgb2hslf(d.r, d.g, d.b)
	_, ss, _ := rgb2hslf(s.r, s.g, s.b)
	r, g, b := hsl2rgbf(dh, ss, dl)
	return rgbaf64{r: r, g: g, b: b}
}

func kernelColor(d, s rgbaf64) rgbaf64 {
	sh, ss, _ := rgb2hslf(s.r, s.g, s.b)
	_, _, dl := rgb2hslf(d.r, d.g, d.b)
	r, g, b := hsl2rgbf(sh, ss, dl)
	return rgbaf64{r: r, g: g, b: b}
}

func kernelLuminosity(d, s rgbaf64) rgbaf64 {
	dh, ds, _ := rgb2hslf(d.r, d.g, d.b)
	_, _, sl := rgb2hslf(s.r, s.g, s.b)
	r, g, b := hsl2rgbf(dh, ds, sl)
	return rgbaf64{r: r, g: g, b: b}
}

// unpremul divides a premultiplied channel c by alpha a. Both c and a are
// in the same units; the result is in [0,1] for valid premultiplied colors.
func unpremul(c, a float64) float64 {
	if a == 0 {
		return 0
	}
	return c / a
}

// clamp01 clamps x to [0,1]. NaN becomes 0 and +Inf becomes 1 so that
// out-of-range blend results (Addition, Divide, ...) are deterministic.
func clamp01(x float64) float64 {
	if x < 0 || math.IsNaN(x) {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// srgbToLinear converts a gamma-encoded sRGB channel to linear light.
func srgbToLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// linearToSrgb converts a linear-light channel to gamma-encoded sRGB.
func linearToSrgb(c float64) float64 {
	if c <= 0.0031308 {
		return 12.92 * c
	}
	return 1.055*math.Pow(c, 1.0/2.4) - 0.055
}

// rgb2hslf converts an RGB color with channels in [0,1] to HSL.
func rgb2hslf(r, g, b float64) (h, s, l float64) {
	cmax := math.Max(math.Max(r, g), b)
	cmin := math.Min(math.Min(r, g), b)
	l = (cmax + cmin) / 2.0
	if cmax != cmin { // Chromatic, else Achromatic.
		delta := cmax - cmin
		if l > 0.5 {
			s = delta / (2.0 - cmax - cmin)
		} else {
			s = delta / (cmax + cmin)
		}
		switch cmax {
		case r:
			h = (g - b) / delta
			if g < b {
				h += 6.0
			}
		case g:
			h = (b-r)/delta + 2.0
		case b:
			h = (r-g)/delta + 4.0
		}
		h /= 6.0
	}
	return
}

// hsl2rgbf converts an HSL color to RGB with channels in [0,1].
func hsl2rgbf(h, s, l float64) (r, g, b float64) {
	if s == 0 {
		return l, l, l
	}
	var q float64
	if l < 0.5 {
		q = l * (1 + s)
	} else {
		q = l + s - s*l
	}
	p := 2*l - q
	return hue2rgb(p, q, h+1.0/3), hue2rgb(p, q, h), hue2rgb(p, q, h-1.0/3)
}
