package aseprite

import (
	"errors"
	"image"
	"image/color"
	"math"
	"sort"
)

// Resource limits and sentinel errors returned by the indexed color exporter.
var (
	// MaxPaletteSize is the largest palette that an 8 bits per pixel image can hold.
	MaxPaletteSize = 256

	// MaxImageDimension is the largest accepted image width or height.
	MaxImageDimension = 32768

	// MaxPixels is the largest accepted number of pixels in a single image.
	MaxPixels = 1 << 24

	// ErrEmptyImage is returned when the input image has an empty bounding rectangle.
	ErrEmptyImage = errors.New("aseprite: cannot quantize an empty image")

	// ErrImageTooLarge is returned when the input image exceeds MaxImageDimension
	// or MaxPixels.
	ErrImageTooLarge = errors.New("aseprite: image exceeds the maximum supported dimensions")

	// ErrGrayscale16 is returned when the input image is a 16 bits per pixel
	// grayscale image, which cannot be reduced to an indexed palette without
	// an explicit color conversion step.
	ErrGrayscale16 = errors.New("aseprite: 16 bpp grayscale images are not supported for indexed export")

	// ErrPaletteSize is returned when the requested palette size is outside of
	// the valid range or is too small to reserve a transparent entry.
	ErrPaletteSize = errors.New("aseprite: palette size must be between 1 and 256, and at least 2 when a transparent index is reserved")

	// ErrTransparentIndex is returned when the transparent index is neither -1
	// (disabled) nor a valid palette index.
	ErrTransparentIndex = errors.New("aseprite: transparent index must be -1 or a valid palette index")

	// ErrInvalidErrorMetric is returned when the error metric is not one of the
	// documented constants.
	ErrInvalidErrorMetric = errors.New("aseprite: invalid error metric")

	// ErrInvalidDitherMode is returned when the dithering mode is not one of
	// the documented constants.
	ErrInvalidDitherMode = errors.New("aseprite: invalid dithering mode")
)

// ErrorMetric selects the color distance function that is used when mapping
// pixels to their nearest palette entry.
type ErrorMetric uint8

const (
	// Euclidean measures the Euclidean distance in RGBA space.
	Euclidean ErrorMetric = iota

	// Manhattan measures the sum of absolute channel differences in RGBA space.
	Manhattan

	// RedMean measures the RedMean weighted distance, which approximates
	// perceived color difference better than plain Euclidean.
	RedMean
)

// DitherMode selects how quantization error is distributed between pixels.
type DitherMode uint8

const (
	// DitherNone disables dithering. Every pixel is mapped independently.
	DitherNone DitherMode = iota

	// FloydSteinberg enables Floyd-Steinberg error diffusion.
	FloydSteinberg
)

// QuantizeOptions configures an indexed color export.
//
// The zero value is valid: it requests a 256 entry palette, reserves palette
// index 0 for fully transparent pixels, uses the Euclidean error metric and
// disables dithering.
type QuantizeOptions struct {
	// PaletteSize is the number of palette entries, between 1 and MaxPaletteSize.
	// A zero value defaults to MaxPaletteSize.
	PaletteSize int

	// TransparentIndex reserves a palette index for fully transparent pixels.
	// Set it to -1 to disable the reservation and quantize transparent pixels
	// like any other pixel. The reserved entry is skipped when quantizing
	// non-transparent pixels, so opaque data is never mapped to transparency.
	TransparentIndex int

	// Metric selects the nearest-color error metric.
	Metric ErrorMetric

	// Dither selects the dithering mode.
	Dither DitherMode
}

// QuantizeReport summarizes how pixels were mapped onto the palette.
//
// Pixel counts satisfy Pixels == Exact + Quantized + Transparent.
// SemiTransparent counts pixels with an alpha value strictly between 0 and 255
// and is a subset of Exact + Quantized.
type QuantizeReport struct {
	// Palette is the palette that was used to build the paletted image.
	Palette color.Palette

	// TransparentIndex is the palette index reserved for transparency, or -1.
	TransparentIndex int

	// NumColors is the number of palette entries that are referenced by at
	// least one pixel.
	NumColors int

	// Pixels is the total number of pixels that were processed.
	Pixels int

	// Exact counts non-transparent pixels whose color is present in the
	// palette and were mapped without approximation.
	Exact int

	// Quantized counts non-transparent pixels that were approximated by their
	// nearest palette entry.
	Quantized int

	// SemiTransparent counts pixels with an alpha value strictly between 0 and 255.
	SemiTransparent int

	// Transparent counts fully transparent pixels.
	Transparent int

	// MeanError is the mean per-pixel normalized Euclidean RGBA error over
	// non-transparent pixels, in the range [0, 1].
	MeanError float64

	// RMSE is the root mean square of the per-pixel normalized Euclidean error.
	RMSE float64

	// MaxError is the largest per-pixel normalized Euclidean error observed.
	MaxError float64

	// IndexUsage records how many pixels were mapped to each palette index.
	IndexUsage []int
}

type colorCount struct {
	c     color.NRGBA
	count int
}

// Quantize converts a true color image (or a decoded Aseprite atlas) into an
// 8 bits per pixel paletted image. The input image is never modified.
//
// Fully transparent pixels (alpha 0) are mapped to TransparentIndex when
// transparency is reserved. Semi-transparent pixels (0 < alpha < 255) take
// part in palette construction together with opaque pixels, carrying their
// full alpha channel. Colors that do not fit in the palette are approximated
// deterministically by their nearest palette entry, and every decision is
// recorded in the returned report.
func Quantize(img image.Image, opts QuantizeOptions) (*image.Paletted, *QuantizeReport, error) {
	opts, err := opts.withDefaults()
	if err != nil {
		return nil, nil, err
	}

	rect, err := checkImage(img)
	if err != nil {
		return nil, nil, err
	}

	counts := collectColors(img, rect, opts.TransparentIndex)

	palette, exact := buildPalette(counts, opts)

	dst := image.NewPaletted(rect, palette)
	report := mapPixels(img, rect, dst, palette, exact, opts)
	report.Palette = palette
	return dst, &report, nil
}

// QuantizeFrames quantizes every frame of a decoded Aseprite sprite.
//
// When shared is true, all frames are quantized together into one palette and
// every returned image references the same color.Palette. When shared is
// false, each frame gets its own palette and report. The returned images keep
// their frame's atlas coordinates in Rect, so their rows can be copied
// straight into a texture atlas.
//
// The source sprite is never modified.
func QuantizeFrames(spr *Aseprite, opts QuantizeOptions, shared bool) ([]*image.Paletted, []QuantizeReport, error) {
	if spr == nil || len(spr.Frames) == 0 {
		return nil, nil, ErrEmptyImage
	}

	opts, err := opts.withDefaults()
	if err != nil {
		return nil, nil, err
	} else if err := rejectGray16(spr); err != nil {
		return nil, nil, err
	}

	n := len(spr.Frames)
	images := make([]*image.Paletted, n)
	reports := make([]QuantizeReport, n)

	if shared {
		counts := make(map[color.NRGBA]int)
		for _, frame := range spr.Frames {
			rect, err := checkRect(frame.Bounds)
			if err != nil {
				return nil, nil, err
			}
			mergeColors(counts, collectColors(spr, rect, opts.TransparentIndex))
		}

		palette, exact := buildPalette(counts, opts)
		for i, frame := range spr.Frames {
			dst := image.NewPaletted(frame.Bounds, palette)
			reports[i] = mapPixels(spr, frame.Bounds, dst, palette, exact, opts)
			reports[i].Palette = palette
			images[i] = dst
		}
	} else {
		for i, frame := range spr.Frames {
			rect, err := checkRect(frame.Bounds)
			if err != nil {
				return nil, nil, err
			}
			palette, exact := buildPalette(collectColors(spr, rect, opts.TransparentIndex), opts)
			dst := image.NewPaletted(rect, palette)
			reports[i] = mapPixels(spr, rect, dst, palette, exact, opts)
			reports[i].Palette = palette
			images[i] = dst
		}
	}

	return images, reports, nil
}

func (opts QuantizeOptions) withDefaults() (QuantizeOptions, error) {
	if opts.PaletteSize == 0 {
		opts.PaletteSize = MaxPaletteSize
	}

	if opts.PaletteSize < 1 || opts.PaletteSize > MaxPaletteSize {
		return opts, ErrPaletteSize
	}

	if opts.TransparentIndex != -1 &&
		(opts.TransparentIndex < 0 || opts.TransparentIndex >= opts.PaletteSize) {
		return opts, ErrTransparentIndex
	}

	if opts.TransparentIndex >= 0 && opts.PaletteSize < 2 {
		return opts, ErrPaletteSize
	}

	switch opts.Metric {
	case Euclidean, Manhattan, RedMean:
	default:
		return opts, ErrInvalidErrorMetric
	}

	switch opts.Dither {
	case DitherNone, FloydSteinberg:
	default:
		return opts, ErrInvalidDitherMode
	}

	return opts, nil
}

func rejectGray16(img image.Image) error {
	switch v := img.(type) {
	case *image.Gray16:
		return ErrGrayscale16
	case *Aseprite:
		// The decoded atlas is stored behind the embedded image field.
		if _, ok := v.Image.(*image.Gray16); ok {
			return ErrGrayscale16
		}
	}
	return nil
}

func checkRect(rect image.Rectangle) (image.Rectangle, error) {
	w, h := rect.Dx(), rect.Dy()
	if w <= 0 || h <= 0 {
		return rect, ErrEmptyImage
	}
	if w > MaxImageDimension || h > MaxImageDimension {
		return rect, ErrImageTooLarge
	}
	if int64(w)*int64(h) > int64(MaxPixels) {
		return rect, ErrImageTooLarge
	}
	return rect, nil
}

func checkImage(img image.Image) (image.Rectangle, error) {
	if img == nil {
		return image.Rectangle{}, ErrEmptyImage
	} else if err := rejectGray16(img); err != nil {
		return image.Rectangle{}, err
	}
	return checkRect(img.Bounds())
}

// readNRGBA reads a single pixel as a non-alpha-premultiplied color.
// Fast paths avoid the generic color model overhead for the image types that
// the decoder produces.
func readNRGBA(img image.Image, x, y int) color.NRGBA {
	switch src := img.(type) {
	case *image.NRGBA:
		i := src.PixOffset(x, y)
		return color.NRGBA{src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3]}
	case *image.Paletted:
		return color.NRGBAModel.Convert(src.Palette[src.ColorIndexAt(x, y)]).(color.NRGBA)
	default:
		return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
	}
}

func collectColors(img image.Image, rect image.Rectangle, transparentIndex int) map[color.NRGBA]int {
	counts := make(map[color.NRGBA]int)
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			c := readNRGBA(img, x, y)
			if c.A == 0 && transparentIndex >= 0 {
				continue
			}
			counts[c]++
		}
	}
	return counts
}

func mergeColors(dst, src map[color.NRGBA]int) {
	for c, n := range src {
		dst[c] += n
	}
}

// sortedColorCounts turns the color histogram into a slice sorted by
// (R, G, B, A), which makes every later palette decision deterministic.
func sortedColorCounts(counts map[color.NRGBA]int) []colorCount {
	colors := make([]colorCount, 0, len(counts))
	for c, n := range counts {
		colors = append(colors, colorCount{c, n})
	}
	sort.Slice(colors, func(i, j int) bool {
		a, b := colors[i].c, colors[j].c
		if a.R != b.R {
			return a.R < b.R
		} else if a.G != b.G {
			return a.G < b.G
		} else if a.B != b.B {
			return a.B < b.B
		}
		return a.A < b.A
	})
	return colors
}

// channelRange reports the min and max value of channel ch inside a box.
func channelRange(box []colorCount, ch int) (min, max int) {
	min = 256
	for _, cc := range box {
		var v int
		switch ch {
		case 0:
			v = int(cc.c.R)
		case 1:
			v = int(cc.c.G)
		case 2:
			v = int(cc.c.B)
		default:
			v = int(cc.c.A)
		}
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	return min, max
}

// splitBox splits a box at the weighted median of its widest channel.
// Ties between channels are broken in R, G, B, A order.
func splitBox(box []colorCount) (left, right []colorCount) {
	channel, widest := 0, -1
	for ch := 0; ch < 4; ch++ {
		min, max := channelRange(box, ch)
		if width := max - min; width > widest {
			channel, widest = ch, width
		}
	}

	sort.SliceStable(box, func(i, j int) bool {
		return channelValue(box[i].c, channel) < channelValue(box[j].c, channel)
	})

	total := 0
	for _, cc := range box {
		total += cc.count
	}

	cum, splitAt := 0, len(box)-1
	for i, cc := range box {
		cum += cc.count
		if cum*2 >= total {
			splitAt = i
			break
		}
	}

	// Keep both halves non-empty.
	if splitAt >= len(box)-1 {
		splitAt = len(box) - 2
	}

	return box[:splitAt+1], box[splitAt+1:]
}

func channelValue(c color.NRGBA, ch int) int {
	switch ch {
	case 0:
		return int(c.R)
	case 1:
		return int(c.G)
	case 2:
		return int(c.B)
	default:
		return int(c.A)
	}
}

func boxRepresentative(box []colorCount) color.NRGBA {
	var sum [4]int
	total := 0
	for _, cc := range box {
		sum[0] += int(cc.c.R) * cc.count
		sum[1] += int(cc.c.G) * cc.count
		sum[2] += int(cc.c.B) * cc.count
		sum[3] += int(cc.c.A) * cc.count
		total += cc.count
	}
	return color.NRGBA{
		R: byte(math.Round(float64(sum[0]) / float64(total))),
		G: byte(math.Round(float64(sum[1]) / float64(total))),
		B: byte(math.Round(float64(sum[2]) / float64(total))),
		A: byte(math.Round(float64(sum[3]) / float64(total))),
	}
}

// medianCut reduces the histogram to at most slots representative colors.
// Boxes are split widest-first and ties resolve to the earliest box, so the
// resulting palette is identical for identical inputs.
func medianCut(colors []colorCount, slots int) []color.NRGBA {
	if len(colors) <= slots {
		out := make([]color.NRGBA, len(colors))
		for i, cc := range colors {
			out[i] = cc.c
		}
		return out
	}

	boxes := [][]colorCount{colors}
	for len(boxes) < slots {
		target, widest := -1, -1
		for i, box := range boxes {
			if len(box) < 2 {
				continue
			}
			width := 0
			for ch := 0; ch < 4; ch++ {
				min, max := channelRange(box, ch)
				if w := max - min; w > width {
					width = w
				}
			}
			if width == 0 {
				continue
			}
			if width > widest {
				target, widest = i, width
			}
		}

		if target < 0 {
			break
		}

		left, right := splitBox(boxes[target])
		boxes = append(boxes[:target], append([][]colorCount{left, right}, boxes[target+1:]...)...)
	}

	seen := make(map[color.NRGBA]struct{}, len(boxes))
	reps := make([]color.NRGBA, 0, len(boxes))
	for _, box := range boxes {
		rep := boxRepresentative(box)
		if _, ok := seen[rep]; ok {
			continue
		}
		seen[rep] = struct{}{}
		reps = append(reps, rep)
	}
	return reps
}

// buildPalette assembles the final palette and an exact-match lookup table.
// Unused entries are filled with opaque black. The reserved transparent entry
// holds color.Transparent and is never assigned a quantized color.
func buildPalette(counts map[color.NRGBA]int, opts QuantizeOptions) (color.Palette, map[color.NRGBA]int) {
	palette := make(color.Palette, opts.PaletteSize)
	black := color.NRGBA{0, 0, 0, 255}
	for i := range palette {
		palette[i] = black
	}

	if opts.TransparentIndex >= 0 {
		palette[opts.TransparentIndex] = color.NRGBA{0, 0, 0, 0}
	}

	slots := opts.PaletteSize
	if opts.TransparentIndex >= 0 {
		slots--
	}

	reps := medianCut(sortedColorCounts(counts), slots)

	exact := make(map[color.NRGBA]int, len(reps)+1)
	slot := 0
	for _, rep := range reps {
		for slot == opts.TransparentIndex {
			slot++
		}
		if slot >= len(palette) {
			break
		}
		palette[slot] = rep
		exact[rep] = slot
		slot++
	}

	if opts.TransparentIndex >= 0 {
		exact[color.NRGBA{0, 0, 0, 0}] = opts.TransparentIndex
	}

	return palette, exact
}

// colorDistance computes the selected metric between two 8 bit colors.
// Euclidean and Manhattan results stay integer-accurate; RedMean is weighted.
func colorDistance(a, b color.NRGBA, metric ErrorMetric) float64 {
	dr := int(a.R) - int(b.R)
	dg := int(a.G) - int(b.G)
	db := int(a.B) - int(b.B)
	da := int(a.A) - int(b.A)

	switch metric {
	case Manhattan:
		return float64(absInt(dr) + absInt(dg) + absInt(db) + absInt(da))
	case RedMean:
		rmean := (int(a.R) + int(b.R)) / 2
		rw := 2.0 + float64(rmean)/256.0
		bw := 2.0 + float64(255-rmean)/256.0
		return math.Sqrt(float64(rw)*float64(dr*dr) + 4*float64(dg*dg) + float64(bw)*float64(db*db) + float64(da*da))
	default:
		return math.Sqrt(float64(dr*dr + dg*dg + db*db + da*da))
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func clampByte(v float64) byte {
	if v <= 0 {
		return 0
	} else if v >= 255 {
		return 255
	}
	return byte(math.Round(v))
}

// nearestEntry finds the closest palette entry for c. The reserved transparent
// entry is skipped so non-transparent pixels can never be erased. Ties are
// resolved in favor of the lowest palette index.
func nearestEntry(palette color.Palette, c color.NRGBA, transparentIndex int, metric ErrorMetric) int {
	best := -1
	bestDist := math.Inf(1)
	for i, p := range palette {
		if i == transparentIndex {
			continue
		}
		d := colorDistance(c, color.NRGBAModel.Convert(p).(color.NRGBA), metric)
		if d < bestDist {
			bestDist, best = d, i
		}
	}
	return best
}

// normalizedError is the Euclidean RGBA distance normalized by the maximum
// possible distance sqrt(4 * 255^2) = 510.
func normalizedError(a, b color.NRGBA) float64 {
	dr := int(a.R) - int(b.R)
	dg := int(a.G) - int(b.G)
	db := int(a.B) - int(b.B)
	da := int(a.A) - int(b.A)
	return math.Sqrt(float64(dr*dr+dg*dg+db*db+da*da)) / 510.0
}

func squaredErrorSum(a, b color.NRGBA) int {
	dr := int(a.R) - int(b.R)
	dg := int(a.G) - int(b.G)
	db := int(a.B) - int(b.B)
	da := int(a.A) - int(b.A)
	return dr*dr + dg*dg + db*db + da*da
}

type reportBuilder struct {
	report         *QuantizeReport
	palette        color.Palette
	exact          map[color.NRGBA]int
	transparentIdx int
	sumDist        float64
	sumSq          int
	maxErr         float64
}

func (rb *reportBuilder) record(src color.NRGBA, idx int) {
	rb.report.Pixels++
	rb.report.IndexUsage[idx]++

	if src.A == 0 && rb.transparentIdx >= 0 {
		rb.report.Transparent++
		return
	}

	if 0 < src.A && src.A < 255 {
		rb.report.SemiTransparent++
	}

	// Exact is based on the source color alone: if the source color has an
	// entry in the palette it could have been represented without
	// approximation, even if dithering diffused the pixel onto another entry.
	if _, ok := rb.exact[src]; ok {
		rb.report.Exact++
	} else {
		rb.report.Quantized++
	}

	out := color.NRGBAModel.Convert(rb.palette[idx]).(color.NRGBA)
	err := normalizedError(src, out)
	rb.sumDist += err
	rb.sumSq += squaredErrorSum(src, out)
	if err > rb.maxErr {
		rb.maxErr = err
	}
}

// finish computes aggregate errors and the number of used palette entries.
func (rb *reportBuilder) finish() QuantizeReport {
	report := rb.report
	if n := report.Pixels - report.Transparent; n > 0 {
		report.MeanError = rb.sumDist / float64(n)
		report.RMSE = math.Sqrt(float64(rb.sumSq)/float64(n)) / 510.0
		report.MaxError = rb.maxErr
	}
	for _, usage := range report.IndexUsage {
		if usage > 0 {
			report.NumColors++
		}
	}
	return *report
}

// mapPixels writes palette indices into dst and fills the mapping report.
// Dithering runs on two floating point error rows and never touches the
// source image.
func mapPixels(img image.Image, rect image.Rectangle, dst *image.Paletted,
	palette color.Palette, exact map[color.NRGBA]int, opts QuantizeOptions) QuantizeReport {

	rb := reportBuilder{
		report: &QuantizeReport{
			TransparentIndex: opts.TransparentIndex,
			IndexUsage:       make([]int, len(palette)),
		},
		palette:        palette,
		exact:          exact,
		transparentIdx: opts.TransparentIndex,
	}

	w := rect.Dx()

	if opts.Dither == DitherNone {
		for y := rect.Min.Y; y < rect.Max.Y; y++ {
			for x := rect.Min.X; x < rect.Max.X; x++ {
				c := readNRGBA(img, x, y)
				idx := mapOne(c, palette, exact, opts)
				dst.Pix[(y-rect.Min.Y)*w+(x-rect.Min.X)] = byte(idx)
				rb.record(c, idx)
			}
		}
		return rb.finish()
	}

	curR := make([]float64, w)
	curG := make([]float64, w)
	curB := make([]float64, w)
	curA := make([]float64, w)
	nextR := make([]float64, w)
	nextG := make([]float64, w)
	nextB := make([]float64, w)
	nextA := make([]float64, w)

	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		ly := y - rect.Min.Y
		for x := rect.Min.X; x < rect.Max.X; x++ {
			lx := x - rect.Min.X
			src := readNRGBA(img, x, y)

			// Fully transparent pixels keep their reserved index and absorb
			// no quantization error, so error diffusion cannot leak color
			// into transparent regions.
			if src.A == 0 && opts.TransparentIndex >= 0 {
				dst.Pix[ly*w+lx] = byte(opts.TransparentIndex)
				rb.record(src, opts.TransparentIndex)
				continue
			}

			adjusted := color.NRGBA{
				R: clampByte(float64(src.R) + curR[lx]),
				G: clampByte(float64(src.G) + curG[lx]),
				B: clampByte(float64(src.B) + curB[lx]),
				A: clampByte(float64(src.A) + curA[lx]),
			}

			idx := mapOne(adjusted, palette, exact, opts)
			dst.Pix[ly*w+lx] = byte(idx)
			rb.record(src, idx)

			chosen := color.NRGBAModel.Convert(palette[idx]).(color.NRGBA)
			er := float64(adjusted.R) - float64(chosen.R)
			eg := float64(adjusted.G) - float64(chosen.G)
			eb := float64(adjusted.B) - float64(chosen.B)
			ea := float64(adjusted.A) - float64(chosen.A)

			// Floyd-Steinberg distribution. The same-row neighbor lives in
			// the current error row.
			if lx+1 < w {
				curR[lx+1] += er * 7.0 / 16.0
				curG[lx+1] += eg * 7.0 / 16.0
				curB[lx+1] += eb * 7.0 / 16.0
				curA[lx+1] += ea * 7.0 / 16.0
			}
			if ly+1 < rect.Dy() {
				diffuse4(nextR, nextG, nextB, nextA, lx-1, er, eg, eb, ea, 3.0/16.0, w)
				diffuse4(nextR, nextG, nextB, nextA, lx, er, eg, eb, ea, 5.0/16.0, w)
				diffuse4(nextR, nextG, nextB, nextA, lx+1, er, eg, eb, ea, 1.0/16.0, w)
			}
		}

		// Rotate the rows: the accumulated next row becomes the current one
		// and fresh zeroed rows accumulate the following row's errors.
		curR, nextR = nextR, make([]float64, w)
		curG, nextG = nextG, make([]float64, w)
		curB, nextB = nextB, make([]float64, w)
		curA, nextA = nextA, make([]float64, w)
	}

	return rb.finish()
}

// diffuse4 adds factor-scaled RGBA error to column col of the next error row.
// Out-of-range columns are ignored, which handles the left and right image
// borders.
func diffuse4(r, g, b, a []float64, col int, er, eg, eb, ea, factor float64, w int) {
	if col < 0 || col >= w {
		return
	}
	r[col] += er * factor
	g[col] += eg * factor
	b[col] += eb * factor
	a[col] += ea * factor
}

// mapOne resolves a single pixel to a palette index using exact matching
// first and the configured metric second.
func mapOne(c color.NRGBA, palette color.Palette, exact map[color.NRGBA]int, opts QuantizeOptions) int {
	if c.A == 0 && opts.TransparentIndex >= 0 {
		return opts.TransparentIndex
	}
	if idx, ok := exact[c]; ok {
		return idx
	}
	return nearestEntry(palette, c, opts.TransparentIndex, opts.Metric)
}
