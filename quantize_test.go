package aseprite

import (
	"image"
	"image/color"
	"math"
	"os"
	"testing"

	"github.com/askeladdk/aseprite/internal/require"
)

// makeGradient builds a deterministic horizontal gradient whose pixels cycle
// through independently controllable RGBA ramps.
func makeGradient(w, h int, ramp func(x int) color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, ramp(x))
		}
	}
	return img
}

// makeCheckerboard builds a deterministic transparency checkerboard:
// every other pixel is fully transparent, the remaining pixels alternate
// between two opaque colors.
func makeCheckerboard(w, h int, a, b color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			switch (x + y) % 2 {
			case 0:
				img.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 0})
			default:
				if x%2 == 0 {
					img.SetNRGBA(x, y, a)
				} else {
					img.SetNRGBA(x, y, b)
				}
			}
		}
	}
	return img
}

// refNearest is an independent brute-force nearest-color reference. It scans
// every palette entry except the reserved transparent index and returns the
// minimum-distance index, breaking ties by the lowest index.
func refNearest(palette color.Palette, c color.NRGBA, transparentIndex int, metric ErrorMetric) int {
	best, bestDist := -1, math.Inf(1)
	for i, p := range palette {
		if i == transparentIndex {
			continue
		}
		pn := color.NRGBAModel.Convert(p).(color.NRGBA)
		d := colorDistance(c, pn, metric)
		if d < bestDist {
			best, bestDist = i, d
		}
	}
	return best
}

// assertNearestReference verifies that every pixel's mapped index equals the
// brute-force nearest palette index for the configured metric.
func assertNearestReference(t *testing.T, src image.Image, dst *image.Paletted,
	opts QuantizeOptions) {
	t.Helper()
	rect := dst.Bounds()
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			c := readNRGBA(src, x, y)
			idx := int(dst.ColorIndexAt(x, y))
			if c.A == 0 && opts.TransparentIndex >= 0 {
				require.True(t, idx == opts.TransparentIndex, "transparent pixel not mapped to reserved index")
				continue
			}
			want := refNearest(dst.Palette, c, opts.TransparentIndex, opts.Metric)
			require.True(t, idx == want, "nearest mapping mismatch at", x, y, "got", idx, "want", want)
		}
	}
}

func assertReportInvariants(t *testing.T, report *QuantizeReport, pixels int) {
	t.Helper()
	require.True(t, report.Pixels == pixels, "pixel count")
	require.True(t, report.Exact+report.Quantized+report.Transparent == pixels,
		"count partition", report.Exact, report.Quantized, report.Transparent)
	require.True(t, len(report.IndexUsage) == len(report.Palette), "usage length")

	used, sum := 0, 0
	for _, n := range report.IndexUsage {
		sum += n
		if n > 0 {
			used++
		}
	}
	require.True(t, sum == pixels, "usage sums to pixels")
	require.True(t, used == report.NumColors, "used colors")
	require.True(t, report.MeanError >= 0 && report.MeanError <= 1, "mean error range")
	require.True(t, report.RMSE >= 0 && report.RMSE <= 1, "rmse range")
	require.True(t, report.MaxError >= 0 && report.MaxError <= 1, "max error range")
	require.True(t, report.RMSE <= report.MaxError+1e-12, "rmse bounded by max")
}

func TestQuantizeGradient(t *testing.T) {
	// Fixed 256-step red ramp, 4 rows tall, quantized to 8 colors.
	src := makeGradient(256, 4, func(x int) color.NRGBA {
		return color.NRGBA{byte(x), 0, 0, 255}
	})

	opts := QuantizeOptions{PaletteSize: 8, TransparentIndex: 0}
	dst, report, err := Quantize(src, opts)
	require.NoError(t, err)
	require.True(t, len(dst.Palette) == 8, "palette size")
	require.True(t, dst.Bounds().Eq(src.Bounds()), "bounds")
	require.True(t, report.Transparent == 0, "no transparent pixels")
	require.True(t, report.SemiTransparent == 0, "no semi-transparent pixels")
	assertReportInvariants(t, report, 256*4)
	assertNearestReference(t, src, dst, opts)

	// Quantization must approximate the ramp: not every pixel is exact,
	// but error stays bounded.
	require.True(t, report.Quantized > 0, "ramp requires approximation")
	require.True(t, report.MaxError < 0.25, "max error too large", report.MaxError)

	// Determinism: quantizing twice yields identical indices and palette.
	dst2, report2, err := Quantize(src, opts)
	require.NoError(t, err)
	require.True(t, equalPalette(dst.Palette, dst2.Palette), "palette determinism")
	require.True(t, string(dst.Pix) == string(dst2.Pix), "index determinism")
	require.True(t, report.NumColors == report2.NumColors, "report determinism")

	// Every metric must select a valid nearest entry.
	for _, metric := range []ErrorMetric{Euclidean, Manhattan, RedMean} {
		opts := opts
		opts.Metric = metric
		dst, _, err := Quantize(src, opts)
		require.NoError(t, err)
		assertNearestReference(t, src, dst, opts)
	}
}

func TestQuantizeCheckerboard(t *testing.T) {
	a := color.NRGBA{10, 200, 30, 255}
	b := color.NRGBA{200, 40, 240, 255}
	src := makeCheckerboard(8, 8, a, b)

	t.Run("reserved", func(t *testing.T) {
		opts := QuantizeOptions{PaletteSize: 4, TransparentIndex: 0}
		dst, report, err := Quantize(src, opts)
		require.NoError(t, err)

		require.True(t, report.Transparent == 32, "transparent count")
		require.True(t, report.IndexUsage[0] == 32, "reserved index usage")
		require.True(t, report.Exact == 32, "opaque colors fit exactly")
		require.True(t, report.Quantized == 0, "no approximation needed")
		require.True(t, report.MeanError == 0, "exact mapping has zero error")
		require.True(t, report.MaxError == 0, "exact mapping has zero max error")
		assertReportInvariants(t, report, 64)
		assertNearestReference(t, src, dst, opts)

		pn := color.NRGBAModel.Convert(dst.Palette[0]).(color.NRGBA)
		require.True(t, pn.A == 0, "reserved entry is transparent")
	})

	t.Run("disabled", func(t *testing.T) {
		// With transparency disabled, transparent pixels are quantized too.
		opts := QuantizeOptions{PaletteSize: 4, TransparentIndex: -1}
		dst, report, err := Quantize(src, opts)
		require.NoError(t, err)
		require.True(t, report.Transparent == 0, "no reserved transparency")
		require.True(t, report.TransparentIndex == -1)
		assertReportInvariants(t, report, 64)
		assertNearestReference(t, src, dst, opts)

		// Determinism.
		dst2, _, err := Quantize(src, opts)
		require.NoError(t, err)
		require.True(t, string(dst.Pix) == string(dst2.Pix))
	})

	t.Run("custom index", func(t *testing.T) {
		opts := QuantizeOptions{PaletteSize: 16, TransparentIndex: 15}
		dst, report, err := Quantize(src, opts)
		require.NoError(t, err)
		require.True(t, report.IndexUsage[15] == 32, "custom transparent index")
		require.True(t, dst.ColorIndexAt(0, 0) == 15, "transparent pixel at origin")
		assertNearestReference(t, src, dst, opts)
	})
}

func TestQuantizeSemiTransparent(t *testing.T) {
	// Alpha ramp: every pixel carries a distinct alpha, none is fully
	// transparent. The palette must carry alpha information.
	src := makeGradient(16, 1, func(x int) color.NRGBA {
		return color.NRGBA{128, 64, 200, byte(1 + x*16)}
	})

	opts := QuantizeOptions{PaletteSize: 8, TransparentIndex: 0}
	dst, report, err := Quantize(src, opts)
	require.NoError(t, err)
	require.True(t, report.SemiTransparent == 16, "all pixels semi-transparent")
	require.True(t, report.Transparent == 0)
	assertReportInvariants(t, report, 16)
	assertNearestReference(t, src, dst, opts)

	hasAlpha := false
	for i, p := range dst.Palette {
		if i == opts.TransparentIndex {
			continue
		}
		if pn := color.NRGBAModel.Convert(p).(color.NRGBA); pn.A > 0 && pn.A < 255 {
			hasAlpha = true
		}
	}
	require.True(t, hasAlpha, "palette preserves semi-transparent alpha")
}

func TestQuantizeBoundaryColors(t *testing.T) {
	// The eight corners of the RGBA cube plus selected interior boundary
	// values. A palette large enough to hold them all must map each exactly.
	colors := []color.NRGBA{
		{0, 0, 0, 0},
		{255, 255, 255, 255},
		{0, 0, 0, 255},
		{255, 0, 0, 255},
		{0, 255, 0, 255},
		{0, 0, 255, 255},
		{255, 255, 0, 255},
		{255, 0, 255, 255},
		{0, 255, 255, 255},
		{1, 1, 1, 255},
		{254, 254, 254, 255},
		{127, 127, 127, 127},
	}
	src := image.NewNRGBA(image.Rect(0, 0, len(colors), 1))
	for i, c := range colors {
		src.SetNRGBA(i, 0, c)
	}

	opts := QuantizeOptions{PaletteSize: 16, TransparentIndex: 0}
	dst, report, err := Quantize(src, opts)
	require.NoError(t, err)
	require.True(t, dst.ColorIndexAt(0, 0) == 0, "fully transparent corner")
	require.True(t, report.Exact == len(colors)-1, "all non-transparent corners exact")
	require.True(t, report.MeanError == 0 && report.MaxError == 0, "zero error")
	assertReportInvariants(t, report, len(colors))
	assertNearestReference(t, src, dst, opts)
}

func TestQuantizeFloydSteinberg(t *testing.T) {
	src := makeGradient(64, 8, func(x int) color.NRGBA {
		return color.NRGBA{byte(x * 4), byte(255 - x*4), 128, 255}
	})
	checker := makeCheckerboard(8, 8, color.NRGBA{10, 200, 30, 255}, color.NRGBA{200, 40, 240, 255})

	for _, src := range []image.Image{src, checker} {
		plain, plainReport, err := Quantize(src, QuantizeOptions{
			PaletteSize: 4, TransparentIndex: 0, Dither: DitherNone,
		})
		require.NoError(t, err)

		dithered, ditherReport, err := Quantize(src, QuantizeOptions{
			PaletteSize: 4, TransparentIndex: 0, Dither: FloydSteinberg,
		})
		require.NoError(t, err)
		require.True(t, len(dithered.Palette) == 4)

		// Transparent pixels must remain transparent under dithering.
		rect := dithered.Bounds()
		for y := rect.Min.Y; y < rect.Max.Y; y++ {
			for x := rect.Min.X; x < rect.Max.X; x++ {
				if c := readNRGBA(src, x, y); c.A == 0 {
					require.True(t, dithered.ColorIndexAt(x, y) == 0,
						"dithering leaked into transparent region")
				}
			}
		}
		require.True(t, ditherReport.Transparent == plainReport.Transparent,
			"transparent count preserved by dithering")
		assertReportInvariants(t, ditherReport, rect.Dx()*rect.Dy())

		// Every output index still resolves to a real palette entry.
		for _, idx := range dithered.Pix {
			require.True(t, int(idx) < len(dithered.Palette), "index in range")
		}

		// Dithering is deterministic.
		again, _, err := Quantize(src, QuantizeOptions{
			PaletteSize: 4, TransparentIndex: 0, Dither: FloydSteinberg,
		})
		require.NoError(t, err)
		require.True(t, string(dithered.Pix) == string(again.Pix), "dither determinism")

		// On the smooth gradient, error diffusion redistributes indices, so
		// the output must differ from plain nearest-neighbor mapping.
		if _, ok := src.(*image.NRGBA); ok && src.Bounds().Dx() == 64 {
			require.True(t, string(plain.Pix) != string(dithered.Pix),
				"dithering redistributed gradient indices")
		}
	}
}

func TestQuantizeDoesNotModifySource(t *testing.T) {
	src := makeGradient(32, 4, func(x int) color.NRGBA {
		return color.NRGBA{byte(x * 8), byte(255 - x*8), 99, 255}
	})
	before := append([]byte(nil), src.Pix...)

	for _, dither := range []DitherMode{DitherNone, FloydSteinberg} {
		_, _, err := Quantize(src, QuantizeOptions{
			PaletteSize: 8, TransparentIndex: 3, Dither: dither,
		})
		require.NoError(t, err)
		require.True(t, string(before) == string(src.Pix), "source pixels modified")
	}
}

func TestQuantizeErrors(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4))

	t.Run("empty image", func(t *testing.T) {
		_, _, err := Quantize(image.NewNRGBA(image.Rectangle{}), QuantizeOptions{
			PaletteSize: 8,
		})
		require.True(t, err == ErrEmptyImage, "got", err)

		_, _, err = Quantize(nil, QuantizeOptions{PaletteSize: 8})
		require.True(t, err == ErrEmptyImage, "got", err)
	})

	t.Run("grayscale16", func(t *testing.T) {
		g := image.NewGray16(image.Rect(0, 0, 2, 2))
		_, _, err := Quantize(g, QuantizeOptions{PaletteSize: 8})
		require.True(t, err == ErrGrayscale16, "got", err)
	})

	t.Run("palette size", func(t *testing.T) {
		for _, size := range []int{-1, 257, 1000} {
			opts := QuantizeOptions{PaletteSize: size, TransparentIndex: -1}
			_, _, err := Quantize(src, opts)
			require.True(t, err == ErrPaletteSize, "size", size, "got", err)
		}

		// Reserving transparency needs at least two entries.
		_, _, err := Quantize(src, QuantizeOptions{PaletteSize: 1, TransparentIndex: 0})
		require.True(t, err == ErrPaletteSize, "got", err)
	})

	t.Run("transparent index", func(t *testing.T) {
		_, _, err := Quantize(src, QuantizeOptions{PaletteSize: 8, TransparentIndex: 8})
		require.True(t, err == ErrTransparentIndex, "got", err)
		_, _, err = Quantize(src, QuantizeOptions{PaletteSize: 8, TransparentIndex: -2})
		require.True(t, err == ErrTransparentIndex, "got", err)
	})

	t.Run("invalid metric and dither", func(t *testing.T) {
		_, _, err := Quantize(src, QuantizeOptions{
			PaletteSize: 8, TransparentIndex: -1, Metric: 99,
		})
		require.True(t, err == ErrInvalidErrorMetric, "got", err)

		_, _, err = Quantize(src, QuantizeOptions{
			PaletteSize: 8, TransparentIndex: -1, Dither: 99,
		})
		require.True(t, err == ErrInvalidDitherMode, "got", err)
	})

	t.Run("image too large", func(t *testing.T) {
		big := image.NewNRGBA(image.Rect(0, 0, MaxImageDimension+1, 2))
		_, _, err := Quantize(big, QuantizeOptions{PaletteSize: 8, TransparentIndex: -1})
		require.True(t, err == ErrImageTooLarge, "got", err)
	})

	t.Run("fully transparent image", func(t *testing.T) {
		// A valid edge case, not an error: every pixel lands on the reserved
		// index and unused slots stay opaque black.
		img := image.NewNRGBA(image.Rect(0, 0, 3, 3))
		dst, report, err := Quantize(img, QuantizeOptions{PaletteSize: 4, TransparentIndex: 2})
		require.NoError(t, err)
		require.True(t, report.Transparent == 9)
		require.True(t, report.NumColors == 1)
		for _, idx := range dst.Pix {
			require.True(t, idx == 2, "transparent index")
		}
	})
}

func TestQuantizeLargeImagePixelCap(t *testing.T) {
	// Dimensions are within MaxImageDimension but the pixel count exceeds
	// MaxPixels. Allocate a narrow strip so the test stays cheap.
	w := 1024
	h := MaxPixels/w + 1
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	_, _, err := Quantize(img, QuantizeOptions{PaletteSize: 8, TransparentIndex: -1})
	require.True(t, err == ErrImageTooLarge, "got", err)
}

func TestQuantizeDefaults(t *testing.T) {
	src := makeGradient(128, 2, func(x int) color.NRGBA {
		return color.NRGBA{byte(x * 2), byte(x), byte(255 - x*2), 255}
	})
	dst, report, err := Quantize(src, QuantizeOptions{})
	require.NoError(t, err)
	require.True(t, len(dst.Palette) == MaxPaletteSize, "default palette size")
	require.True(t, report.TransparentIndex == 0, "default transparent index")
	assertReportInvariants(t, report, 256)
}

func TestQuantizeDegenerateBoxes(t *testing.T) {
	// More distinct colors than slots, but every box becomes single-valued
	// before reaching the requested slot count. The quantizer must stop
	// cleanly instead of looping, and the output stays deterministic.
	src := image.NewNRGBA(image.Rect(0, 0, 3, 1))
	src.SetNRGBA(0, 0, color.NRGBA{10, 10, 10, 255})
	src.SetNRGBA(1, 0, color.NRGBA{12, 12, 12, 255})
	src.SetNRGBA(2, 0, color.NRGBA{14, 14, 14, 255})

	opts := QuantizeOptions{PaletteSize: 256, TransparentIndex: -1}
	dst, report, err := Quantize(src, opts)
	require.NoError(t, err)
	require.True(t, report.NumColors <= 3, "at most the distinct input colors")
	require.True(t, report.Pixels == 3)
	assertNearestReference(t, src, dst, opts)

	dst2, _, err := Quantize(src, opts)
	require.NoError(t, err)
	require.True(t, string(dst.Pix) == string(dst2.Pix), "degenerate case deterministic")

	// A single distinct color over a large palette leaves unused black slots.
	flat := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for i := range flat.Pix {
		if i%4 == 3 {
			flat.Pix[i] = 255
		} else {
			flat.Pix[i] = 77
		}
	}
	dst, report, err = Quantize(flat, QuantizeOptions{PaletteSize: 16, TransparentIndex: 0})
	require.NoError(t, err)
	require.True(t, report.NumColors == 1, "one used color")
	require.True(t, report.Exact == 16, "all exact")
	for _, idx := range dst.Pix {
		require.True(t, idx != 0, "opaque pixels never hit the reserved slot")
	}
}

func TestQuantizeOversubscribed(t *testing.T) {
	// 64 distinct colors forced into 4 slots: every non-transparent pixel is
	// approximated and every output index matches the independent reference.
	src := makeGradient(64, 1, func(x int) color.NRGBA {
		return color.NRGBA{
			R: byte((x * 37) % 256),
			G: byte((x * 73) % 256),
			B: byte((x * 151) % 256),
			A: 255,
		}
	})

	for _, metric := range []ErrorMetric{Euclidean, Manhattan, RedMean} {
		opts := QuantizeOptions{PaletteSize: 4, TransparentIndex: 0, Metric: metric}
		dst, report, err := Quantize(src, opts)
		require.NoError(t, err)
		require.True(t, report.Quantized+report.Exact == 64)
		require.True(t, report.Quantized > 0, "oversubscribed palette approximates")
		assertReportInvariants(t, report, 64)
		assertNearestReference(t, src, dst, opts)
	}
}

func TestSplitBoxWeightedMedianFallback(t *testing.T) {
	// Two distinct values where the weighted median lands on the final
	// element. splitBox must still return two non-empty boxes.
	box := []colorCount{
		{c: color.NRGBA{0, 0, 0, 255}, count: 1},
		{c: color.NRGBA{200, 0, 0, 255}, count: 100},
	}
	left, right := splitBox(box)
	require.True(t, len(left) > 0 && len(right) > 0, "both halves non-empty")
}

func TestQuantizeFramesIndependent(t *testing.T) {
	f, err := os.Open("./testfiles/slime_paletted.aseprite")
	require.NoError(t, err)
	defer f.Close()
	spr, err := Read(f)
	require.NoError(t, err)

	// Invalid options are rejected before any frame is processed.
	_, _, err = QuantizeFrames(spr, QuantizeOptions{PaletteSize: 8, Metric: 42}, false)
	require.True(t, err == ErrInvalidErrorMetric, "got", err)
}

func TestQuantizePalettedInput(t *testing.T) {
	// An already-paletted image is accepted through the fast path and maps
	// back onto itself when the palette is large enough.
	pal := color.Palette{
		color.NRGBA{0, 0, 0, 0},
		color.NRGBA{255, 0, 0, 255},
		color.NRGBA{0, 255, 0, 255},
		color.NRGBA{0, 0, 255, 255},
	}
	src := image.NewPaletted(image.Rect(0, 0, 4, 2), pal)
	for i := range src.Pix {
		src.Pix[i] = byte(i % 4)
	}

	dst, report, err := Quantize(src, QuantizeOptions{PaletteSize: 8, TransparentIndex: 0})
	require.NoError(t, err)
	require.True(t, len(dst.Palette) == 8)
	require.True(t, report.Exact == 6, "opaque paletted pixels map exactly")
	require.True(t, report.Transparent == 2, "transparent paletted pixels")
	require.True(t, report.Quantized == 0)
	assertReportInvariants(t, report, 8)
}

func TestQuantizeFrames(t *testing.T) {
	f, err := os.Open("./testfiles/slime_paletted.aseprite")
	require.NoError(t, err)
	defer f.Close()

	spr, err := Read(f)
	require.NoError(t, err)

	t.Run("shared", func(t *testing.T) {
		images, reports, err := QuantizeFrames(spr, QuantizeOptions{
			PaletteSize: 16, TransparentIndex: 0,
		}, true)
		require.NoError(t, err)
		require.True(t, len(images) == len(spr.Frames), "one image per frame")
		require.True(t, len(reports) == len(spr.Frames), "one report per frame")

		shared := images[0].Palette
		for i, img := range images {
			require.True(t, equalPalette(img.Palette, shared), "shared palette", i)
			require.True(t, img.Bounds().Eq(spr.Frames[i].Bounds), "frame bounds", i)
			assertReportInvariants(t, &reports[i], img.Bounds().Dx()*img.Bounds().Dy())
		}
	})

	t.Run("independent", func(t *testing.T) {
		images, reports, err := QuantizeFrames(spr, QuantizeOptions{
			PaletteSize: 8, TransparentIndex: 0,
		}, false)
		require.NoError(t, err)
		require.True(t, len(images) == len(spr.Frames))
		for i, img := range images {
			require.True(t, len(img.Palette) == 8, "per-frame palette size")
			require.True(t, img.Bounds().Eq(spr.Frames[i].Bounds))
			assertReportInvariants(t, &reports[i], img.Bounds().Dx()*img.Bounds().Dy())
		}
	})

	t.Run("grayscale rejected", func(t *testing.T) {
		f, err := os.Open("./testfiles/slime_grayscale.aseprite")
		require.NoError(t, err)
		defer f.Close()

		gray, err := Read(f)
		require.NoError(t, err)
		_, _, err = QuantizeFrames(gray, QuantizeOptions{PaletteSize: 8}, true)
		require.True(t, err == ErrGrayscale16, "frames got", err)

		// The wrapper hides the embedded Gray16 atlas, so Quantize must
		// unwrap it before the check.
		_, _, err = Quantize(gray, QuantizeOptions{PaletteSize: 8})
		require.True(t, err == ErrGrayscale16, "quantize got", err)
	})

	t.Run("nil rejected", func(t *testing.T) {
		_, _, err := QuantizeFrames(nil, QuantizeOptions{PaletteSize: 8}, true)
		require.True(t, err == ErrEmptyImage, "got", err)
	})
}

func TestQuantizeAtlas(t *testing.T) {
	// Quantizing the whole atlas in one call must succeed and keep bounds.
	f, err := os.Open("./testfiles/blendtest.aseprite")
	require.NoError(t, err)
	defer f.Close()

	spr, err := Read(f)
	require.NoError(t, err)

	dst, report, err := Quantize(spr, QuantizeOptions{
		PaletteSize: 32, TransparentIndex: 0, Metric: RedMean, Dither: FloydSteinberg,
	})
	require.NoError(t, err)
	require.True(t, dst.Bounds().Eq(spr.Bounds()))
	assertReportInvariants(t, report, spr.Bounds().Dx()*spr.Bounds().Dy())
}
