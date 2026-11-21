package aseprite

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"testing"

	"github.com/askeladdk/aseprite/internal/require"
)

func equalPalette(a, b color.Palette) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// put16 and put32 append little-endian integers.
func put16(b *bytes.Buffer, v uint16) {
	var raw [2]byte
	binary.LittleEndian.PutUint16(raw[:], v)
	b.Write(raw[:])
}

func put32(b *bytes.Buffer, v uint32) {
	var raw [4]byte
	binary.LittleEndian.PutUint32(raw[:], v)
	b.Write(raw[:])
}

// writeAseprite synthesizes a minimal valid 8bpp paletted Aseprite
// file. Frame i is filled with a checkerboard of palette indices 1 and
// 2; frames listed in emptyFrames contain no cel and decode to fully
// transparent frames. Palette index 0 is transparent.
func writeAseprite(buf *bytes.Buffer, framew, frameh, nframes int, emptyFrames map[int]bool) {
	paletteSize := 3

	// File header (128 bytes), field layout follows the Aseprite spec.
	put32(buf, 0) // file size, patched later
	put16(buf, 0xA5E0)
	put16(buf, uint16(nframes))
	put16(buf, uint16(framew))
	put16(buf, uint16(frameh))
	put16(buf, 8)    // color depth
	put32(buf, 1)    // flags: layer has a valid transparent index
	put16(buf, 0)    // frame speed
	put32(buf, 0)    // reserved
	put32(buf, 0)    // reserved
	buf.WriteByte(0) // transparent palette index
	buf.Write(make([]byte, 3))
	put16(buf, uint16(paletteSize))
	buf.WriteByte(1) // pixel width
	buf.WriteByte(1) // pixel height
	put16(buf, 0)    // grid x
	put16(buf, 0)    // grid y
	put16(buf, 0)    // grid width
	put16(buf, 0)    // grid height
	buf.Write(make([]byte, 84))

	// Layer chunk (one visible layer), shared in frame 0.
	layerChunkData := func() []byte {
		var c bytes.Buffer
		put16(&c, 1)     // flags: visible
		put16(&c, 0)     // type: normal layer
		put16(&c, 0)     // child level
		put16(&c, 0)     // default layer width
		put16(&c, 0)     // default layer height
		put16(&c, 0)     // blend mode: normal
		c.WriteByte(255) // opacity
		c.Write(make([]byte, 3))
		put16(&c, 0) // name length
		return c.Bytes()
	}()

	// Palette chunk (3 colors).
	paletteChunkData := func() []byte {
		var c bytes.Buffer
		put32(&c, uint32(paletteSize))
		put32(&c, 0) // first index
		put32(&c, 0) // last index
		c.Write(make([]byte, 8))
		colors := [][4]byte{{0, 0, 0, 0}, {255, 0, 0, 255}, {0, 0, 255, 255}}
		for _, col := range colors {
			put16(&c, 0) // flags
			c.Write(col[:])
		}
		return c.Bytes()
	}()

	celChunkData := func(frame int) []byte {
		var c bytes.Buffer
		put16(&c, 0)     // layer index
		put16(&c, 0)     // x
		put16(&c, 0)     // y
		c.WriteByte(255) // opacity
		put16(&c, 0)     // cel type: uncompressed
		put16(&c, 0)     // z index
		c.Write(make([]byte, 5))
		put16(&c, uint16(framew))
		put16(&c, uint16(frameh))
		pix := make([]byte, framew*frameh)
		for y := 0; y < frameh; y++ {
			for x := 0; x < framew; x++ {
				// Only opaque indices are used so that fully transparent
				// texels can only come from genuinely empty frames.
				if (x/2+y/2)%2 == 0 {
					pix[y*framew+x] = 1
				} else {
					pix[y*framew+x] = 2
				}
			}
		}
		c.Write(pix)
		return c.Bytes()
	}

	for frame := 0; frame < nframes; frame++ {
		var chunks [][]byte
		var chunkTypes []uint16
		if frame == 0 {
			chunks = append(chunks, layerChunkData, paletteChunkData)
			chunkTypes = append(chunkTypes, 0x2004, 0x2019)
		}
		if !emptyFrames[frame] {
			chunks = append(chunks, celChunkData(frame))
			chunkTypes = append(chunkTypes, 0x2005)
		}

		var frameData bytes.Buffer
		put32(&frameData, 0) // frame length, patched below
		put16(&frameData, 0xF1FA)
		put16(&frameData, uint16(len(chunks)))
		put16(&frameData, 100) // duration ms
		frameData.Write(make([]byte, 2))
		put32(&frameData, uint32(len(chunks)))

		for i, chunk := range chunks {
			put32(&frameData, uint32(len(chunk)+6))
			put16(&frameData, chunkTypes[i])
			frameData.Write(chunk)
		}

		raw := frameData.Bytes()
		binary.LittleEndian.PutUint32(raw, uint32(len(raw)))
		buf.Write(raw)
	}

	out := buf.Bytes()
	binary.LittleEndian.PutUint32(out, uint32(len(out)))
}

func TestDecode(t *testing.T) {
	for _, tt := range []struct {
		Name     string
		Filename string
		Outfile  string
		Frames   int
		Tags     int
	}{
		{
			Name:     "paletted",
			Filename: "./testfiles/slime_paletted.aseprite",
			Outfile:  "slime_paletted.png",
			Frames:   10,
			Tags:     2,
		},
		{
			Name:     "grayscale",
			Filename: "./testfiles/slime_grayscale.aseprite",
			Outfile:  "slime_grayscale.png",
			Frames:   10,
			Tags:     2,
		},
		{
			Name:     "blendtest",
			Filename: "./testfiles/blendtest.aseprite",
			Outfile:  "blendtest.png",
			Frames:   1,
			Tags:     0,
		},
	} {
		t.Run(tt.Name, func(t *testing.T) {
			f, err := os.Open(tt.Filename)
			require.NoError(t, err)
			defer f.Close()

			img, imgformat, err := image.Decode(f)
			require.NoError(t, err)

			if imgformat != "aseprite" {
				t.Fatal(imgformat)
			}

			aspr, ok := img.(*Aseprite)
			require.True(t, ok)
			require.True(t, len(aspr.Frames) == tt.Frames, "frames", len(aspr.Frames))
			require.True(t, len(aspr.Tags) == tt.Tags, "tags", len(aspr.Tags))

			out, err := os.Create(tt.Outfile)
			require.NoError(t, err)
			require.NoError(t, png.Encode(out, img))
		})
	}
}

func TestDecodeConfig(t *testing.T) {
	for _, tt := range []struct {
		Name        string
		Filename    string
		ImageConfig image.Config
	}{
		{
			Name:     "paletted",
			Filename: "./testfiles/slime_paletted.aseprite",
			ImageConfig: image.Config{
				ColorModel: &color.Palette{
					0: color.Alpha16{0},
					1: color.NRGBA{247, 231, 198, 255},
					2: color.NRGBA{214, 142, 73, 255},
					3: color.NRGBA{166, 55, 37, 255},
					4: color.NRGBA{51, 30, 80, 255},
				},
				Width:  128,
				Height: 256,
			},
		},
		{
			Name:     "grayscale",
			Filename: "./testfiles/slime_grayscale.aseprite",
			ImageConfig: image.Config{
				ColorModel: color.Gray16Model,
				Width:      128,
				Height:     256,
			},
		},
		{
			Name:     "blendtest",
			Filename: "./testfiles/blendtest.aseprite",
			ImageConfig: image.Config{
				ColorModel: color.RGBAModel,
				Width:      640,
				Height:     360,
			},
		},
	} {
		t.Run(tt.Name, func(t *testing.T) {
			f, err := os.Open(tt.Filename)
			require.NoError(t, err)
			defer f.Close()

			conf, imgformat, err := image.DecodeConfig(f)
			require.NoError(t, err)
			require.True(t, imgformat == "aseprite", "image format", imgformat)

			require.True(t, conf.Height == tt.ImageConfig.Height, "height")
			require.True(t, conf.Width == tt.ImageConfig.Width, "width")

			if pal, ok := conf.ColorModel.(color.Palette); ok {
				imgpal := *tt.ImageConfig.ColorModel.(*color.Palette)
				require.True(t, equalPalette(pal, imgpal), "palette")
			} else {
				require.True(t, conf.ColorModel == tt.ImageConfig.ColorModel, "color model")
			}
		})
	}
}

// approxEqual compares floats with an absolute tolerance.
func approxEqual(a, b float32) bool {
	return math.Abs(float64(a-b)) < 1e-6
}

// independentUV recomputes the expected normalized UV from first
// principles so that the test does not share FrameUV's arithmetic.
func independentUV(page, r image.Rectangle) UVRect {
	pw := float32(page.Dx())
	ph := float32(page.Dy())
	return UVRect{
		Min: UV{
			U: float32(r.Min.X-page.Min.X) / pw,
			V: float32(r.Min.Y-page.Min.Y) / ph,
		},
		Max: UV{
			U: float32(r.Max.X-page.Min.X) / pw,
			V: float32(r.Max.Y-page.Min.Y) / ph,
		},
	}
}

// assertUVInUnit verifies the half-open [0,1] UV invariant and that
// Min is strictly below Max for non-empty frames.
func assertUVInUnit(t *testing.T, uv UVRect, empty bool) {
	t.Helper()
	for _, v := range []float32{uv.Min.U, uv.Min.V, uv.Max.U, uv.Max.V} {
		require.True(t, v >= 0 && v <= 1, "uv outside [0,1]", v)
	}
	if empty {
		require.True(t, uv.Min == uv.Max, "empty frame must have empty uv")
	} else {
		require.True(t, uv.Min.U < uv.Max.U && uv.Min.V < uv.Max.V, "uv must be half-open and non-degenerate")
	}
}

// assertInPage verifies that the frame's content plus its padding ring
// stays inside its page bounds, which is what prevents out-of-bounds UV.
// Empty frames occupy no space and are skipped.
func assertInPage(t *testing.T, fr Frame, pageBounds image.Rectangle) {
	t.Helper()
	if fr.Bounds.Empty() {
		return
	}
	ring := fr.Bounds.Inset(-fr.Padding)
	require.True(t, ring.In(pageBounds), "frame padding ring leaves page", ring, pageBounds)
}

func decodeSynthetic(t *testing.T, opts Options, framew, frameh, nframes int, empty map[int]bool) *Aseprite {
	t.Helper()
	var buf bytes.Buffer
	writeAseprite(&buf, framew, frameh, nframes, empty)
	spr, err := ReadWith(&buf, opts)
	require.NoError(t, err)
	return spr
}

// TestFrameMetadataDefault verifies the backwards-compatible zero
// Options result: a single page, the legacy grid layout, no padding,
// no rotation or flips, and independently recomputed UVs.
func TestFrameMetadataDefault(t *testing.T) {
	const framew, frameh, nframes = 8, 8, 6
	spr := decodeSynthetic(t, Options{}, framew, frameh, nframes, nil)

	require.True(t, len(spr.Pages) == 1, "pages", len(spr.Pages))
	require.True(t, len(spr.Frames) == nframes, "frames", len(spr.Frames))

	page := spr.Pages[0]
	gridW, gridH := factorPowerOfTwo(nframes)
	require.True(t, page.Bounds == image.Rect(0, 0, gridW*framew, gridH*frameh), "legacy grid bounds", page.Bounds)

	for i, fr := range spr.Frames {
		require.True(t, fr.Page == 0, "page")
		require.True(t, fr.Padding == 0, "padding")
		require.True(t, !fr.Rotated && !fr.FlipH && !fr.FlipV, "transform flags must default to false")

		col, row := i%gridW, i/gridW
		want := image.Rect(col*framew, row*frameh, (col+1)*framew, (row+1)*frameh)
		require.True(t, fr.Bounds == want, "bounds", fr.Bounds, want)

		wantUV := independentUV(page.Bounds, want)
		require.True(t, approxEqual(fr.UV.Min.U, wantUV.Min.U), "u min", fr.UV.Min.U, wantUV.Min.U)
		require.True(t, approxEqual(fr.UV.Min.V, wantUV.Min.V), "v min")
		require.True(t, approxEqual(fr.UV.Max.U, wantUV.Max.U), "u max")
		require.True(t, approxEqual(fr.UV.Max.V, wantUV.Max.V), "v max")
		assertUVInUnit(t, fr.UV, false)
		assertInPage(t, fr, page.Bounds)
	}
}

// TestFrameMetadataPadding checks padding=2 extrusion: frame content
// keeps its checkerboard pixels while the padding ring is made of
// replicated border texels, and UVs describe only the content rect.
func TestFrameMetadataPadding(t *testing.T) {
	const framew, frameh, nframes, padding = 8, 8, 4, 2
	spr := decodeSynthetic(t, Options{Padding: padding}, framew, frameh, nframes, nil)

	require.True(t, len(spr.Pages) == 1, "pages")
	page := spr.Pages[0].Image
	gridW, gridH := factorPowerOfTwo(nframes)
	pitch := framew + 2*padding
	require.True(t, spr.Pages[0].Bounds == image.Rect(0, 0, gridW*pitch, gridH*pitch), "padded grid bounds")

	for _, fr := range spr.Frames {
		require.True(t, fr.Padding == padding, "padding")
		assertInPage(t, fr, spr.Pages[0].Bounds)
		assertUVInUnit(t, fr.UV, false)

		wantUV := independentUV(spr.Pages[0].Bounds, fr.Bounds)
		require.True(t, approxEqual(fr.UV.Min.U, wantUV.Min.U), "uv")
		require.True(t, approxEqual(fr.UV.Max.V, wantUV.Max.V), "uv")

		// Content texels still hold the checkerboard palette indices.
		for y := fr.Bounds.Min.Y; y < fr.Bounds.Max.Y; y++ {
			for x := fr.Bounds.Min.X; x < fr.Bounds.Max.X; x++ {
				idx := page.(*image.Paletted).ColorIndexAt(x, y)
				require.True(t, idx == 1 || idx == 2, "content must keep checkerboard texel", idx)
			}
		}

		// Every padding texel equals its nearest border texel.
		ring := fr.Bounds.Inset(-padding)
		for y := ring.Min.Y; y < ring.Max.Y; y++ {
			for x := ring.Min.X; x < ring.Max.X; x++ {
				if fr.Bounds.Min.X <= x && x < fr.Bounds.Max.X && fr.Bounds.Min.Y <= y && y < fr.Bounds.Max.Y {
					continue
				}
				sx := clampInt(x, fr.Bounds.Min.X, fr.Bounds.Max.X-1)
				sy := clampInt(y, fr.Bounds.Min.Y, fr.Bounds.Max.Y-1)
				idx := page.(*image.Paletted).ColorIndexAt(x, y)
				want := page.(*image.Paletted).ColorIndexAt(sx, sy)
				require.True(t, idx == want, "extruded padding texel mismatch", x, y, idx, want)
			}
		}
	}
}

// TestFrameMetadataMultipage forces several pages with a tight cap and
// verifies page assignment, stable frame order and in-page bounds.
func TestFrameMetadataMultipage(t *testing.T) {
	const framew, frameh, nframes = 8, 8, 6
	// Cell is 8x8 plus a 1px padding ring, so a 10x10 cap fits one
	// frame per page, producing six pages.
	spr := decodeSynthetic(t, Options{Padding: 1, MaxPageWidth: 10, MaxPageHeight: 10}, framew, frameh, nframes, nil)

	require.True(t, len(spr.Pages) == nframes, "pages", len(spr.Pages))
	require.True(t, len(spr.Frames) == nframes, "frames")

	seen := make(map[int]bool)
	for i, fr := range spr.Frames {
		require.True(t, fr.Page == i, "frame order must match page order", i, fr.Page)
		seen[fr.Page] = true
		require.True(t, fr.Bounds == image.Rect(1, 1, 1+framew, 1+frameh), "bounds", fr.Bounds)
		require.True(t, spr.Pages[fr.Page].Bounds == image.Rect(0, 0, 10, 10), "page bounds")
		assertInPage(t, fr, spr.Pages[fr.Page].Bounds)
		assertUVInUnit(t, fr.UV, false)

		wantUV := independentUV(spr.Pages[fr.Page].Bounds, fr.Bounds)
		require.True(t, approxEqual(fr.UV.Max.U, wantUV.Max.U), "uv")
		require.True(t, approxEqual(fr.UV.Min.V, wantUV.Min.V), "uv")
	}
	require.True(t, len(seen) == nframes, "each frame must land on its own page")
}

// TestFrameMetadataEmptyFrame covers frames without visible cels:
// they keep frame order, stay in bounds and map to an empty UVRect.
func TestFrameMetadataEmptyFrame(t *testing.T) {
	const framew, frameh, nframes = 8, 8, 4
	empty := map[int]bool{1: true, 3: true}
	spr := decodeSynthetic(t, Options{Padding: 2, MaxPageWidth: 20, MaxPageHeight: 20}, framew, frameh, nframes, empty)

	require.True(t, len(spr.Frames) == nframes, "frames")
	for i, fr := range spr.Frames {
		if empty[i] {
			require.True(t, fr.Bounds.Empty(), "empty frame must have empty bounds")
		} else {
			require.True(t, !fr.Bounds.Empty(), "non-empty frame must have bounds")
		}
		assertUVInUnit(t, fr.UV, fr.Bounds.Empty())
		assertInPage(t, fr, spr.Pages[fr.Page].Bounds)
	}
}

// TestFrameMetadataOversized verifies that a frame larger than the page
// cap grows the page instead of producing out-of-bounds UVs.
func TestFrameMetadataOversized(t *testing.T) {
	const framew, frameh, nframes = 16, 16, 3
	spr := decodeSynthetic(t, Options{Padding: 1, MaxPageWidth: 8, MaxPageHeight: 8}, framew, frameh, nframes, nil)

	require.True(t, len(spr.Pages) == nframes, "each oversized frame gets its own page", len(spr.Pages))
	for i, fr := range spr.Frames {
		require.True(t, fr.Page == i, "page order")
		require.True(t, spr.Pages[i].Bounds.Dx() >= framew+2 && spr.Pages[i].Bounds.Dy() >= frameh+2, "page must grow to fit the frame", spr.Pages[i].Bounds)
		assertInPage(t, fr, spr.Pages[i].Bounds)
		assertUVInUnit(t, fr.UV, false)
	}
}

// TestFrameMetadataAllEmpty verifies that a sprite without any visible
// cel still exposes a valid page and every frame has an empty UVRect.
func TestFrameMetadataAllEmpty(t *testing.T) {
	const nframes = 3
	empty := map[int]bool{0: true, 1: true, 2: true}
	spr := decodeSynthetic(t, Options{Padding: 2, MaxPageWidth: 20, MaxPageHeight: 20}, 8, 8, nframes, empty)

	require.True(t, len(spr.Pages) >= 1, "at least one fallback page")
	for i, fr := range spr.Frames {
		require.True(t, fr.Page < len(spr.Pages), "page index must be valid", i)
		require.True(t, fr.Bounds.Empty(), "empty bounds")
		require.True(t, fr.UV == UVRect{}, "empty uv")
	}
}

// TestFrameMetadataEmptyFirstFrame checks the default grid path when
// the first frame carries no cel: the grid pitch must come from the
// first non-empty frame instead of collapsing to zero.
func TestFrameMetadataEmptyFirstFrame(t *testing.T) {
	const framew, frameh, nframes = 8, 8, 4
	empty := map[int]bool{0: true}

	for _, padding := range []int{0, 2} {
		spr := decodeSynthetic(t, Options{Padding: padding}, framew, frameh, nframes, empty)
		require.True(t, len(spr.Pages) == 1, "single grid page")
		require.True(t, !spr.Pages[0].Bounds.Empty(), "page must have a usable size")

		for i, fr := range spr.Frames {
			if i == 0 {
				require.True(t, fr.Bounds.Empty(), "first frame empty")
				require.True(t, fr.UV == UVRect{}, "empty uv")
				continue
			}
			assertInPage(t, fr, spr.Pages[0].Bounds)
			assertUVInUnit(t, fr.UV, false)
			wantUV := independentUV(spr.Pages[0].Bounds, fr.Bounds)
			require.True(t, approxEqual(fr.UV.Min.U, wantUV.Min.U), "uv", padding)
			require.True(t, approxEqual(fr.UV.Max.V, wantUV.Max.V), "uv", padding)
		}
	}
}

// TestFrameMetadataPaddingZeroVsTwo cross-checks the same checkerboard
// sprite decoded with padding 0 and 2: frame content UVs must map back
// to identical pixel values, and only the ring differs.
func TestFrameMetadataPaddingZeroVsTwo(t *testing.T) {
	const framew, frameh, nframes = 8, 8, 4
	noPadding := decodeSynthetic(t, Options{}, framew, frameh, nframes, nil)
	padded := decodeSynthetic(t, Options{Padding: 2}, framew, frameh, nframes, nil)

	require.True(t, len(noPadding.Pages) == 1 && len(padded.Pages) == 1, "single page each")

	img0 := noPadding.Pages[0].Image.(*image.Paletted)
	img2 := padded.Pages[0].Image.(*image.Paletted)

	for i := 0; i < nframes; i++ {
		fr0, fr2 := noPadding.Frames[i], padded.Frames[i]
		require.True(t, fr0.Bounds.Size() == fr2.Bounds.Size(), "content size must be identical")

		// Every content texel must be identical between the two atlases.
		for y := 0; y < frameh; y++ {
			for x := 0; x < framew; x++ {
				a := img0.ColorIndexAt(fr0.Bounds.Min.X+x, fr0.Bounds.Min.Y+y)
				b := img2.ColorIndexAt(fr2.Bounds.Min.X+x, fr2.Bounds.Min.Y+y)
				require.True(t, a == b, "content texel mismatch", i, x, y, a, b)
			}
		}
	}
}

// TestFrameMetadataShelfWrapping verifies that shelf packing wraps to a
// new shelf and then a new page while keeping every cell in bounds.
func TestFrameMetadataShelfWrapping(t *testing.T) {
	const framew, frameh, nframes = 8, 8, 6
	// 8x8 cells, no padding: two fit per row of a 16-wide page and two
	// rows fit per 16-high page, so six frames span two pages.
	spr := decodeSynthetic(t, Options{MaxPageWidth: 16, MaxPageHeight: 16}, framew, frameh, nframes, nil)

	require.True(t, len(spr.Pages) == 2, "pages", len(spr.Pages))
	page0, page1 := 0, 0
	for i, fr := range spr.Frames {
		require.True(t, fr.Page >= 0 && fr.Page < 2, "valid page", fr.Page)
		if fr.Page == 0 {
			page0++
		} else {
			page1++
		}
		require.True(t, fr.Bounds.In(spr.Pages[fr.Page].Bounds), "bounds in page", i)
		assertUVInUnit(t, fr.UV, false)
	}
	require.True(t, page0 == 4 && page1 == 2, "shelf distribution", page0, page1)
}

// TestFrameUVNegativeOrigin exercises pages that are sub-regions of a
// larger texture, including negative origins and non-square pages.
func TestFrameUVNegativeOrigin(t *testing.T) {
	page := image.Rect(-32, -16, 16, 48) // 48 wide, 64 high, non-square
	r := image.Rect(-24, 0, -8, 32)
	uv := FrameUV(page, r)
	want := independentUV(page, r)

	require.True(t, approxEqual(uv.Min.U, want.Min.U), "u min")
	require.True(t, approxEqual(uv.Min.V, want.Min.V), "v min")
	require.True(t, approxEqual(uv.Max.U, want.Max.U), "u max")
	require.True(t, approxEqual(uv.Max.V, want.Max.V), "v max")
	require.True(t, approxEqual(uv.Min.U, float32(8)/48), "explicit negative-origin u")
	require.True(t, approxEqual(uv.Max.V, float32(48)/64), "explicit v")
	assertUVInUnit(t, uv, false)

	// Edge texel maps to a UV strictly below one; the half-open edge
	// never collapses onto the neighboring page's origin.
	require.True(t, uv.Max.U < 1 && uv.Max.V < 1, "half-open max edge")
	require.True(t, FrameUV(image.Rect(0, 0, 0, 4), r) == UVRect{}, "empty page yields zero uv")
	require.True(t, FrameUV(page, image.Rectangle{}) == UVRect{}, "empty rect yields zero uv")
}

// TestHalfPixelInset documents the half-texel convention used with
// linear sampling on a padding-free atlas.
func TestHalfPixelInset(t *testing.T) {
	page := image.Rect(0, 0, 64, 32)
	r := image.Rect(0, 0, 16, 16)
	uv := FrameUV(page, r)
	inset := uv.Inset(UV{0.5 / 64, 0.5 / 32})

	require.True(t, approxEqual(inset.Min.U, 0.5/64), "inset min u")
	require.True(t, approxEqual(inset.Min.V, 0.5/32), "inset min v")
	require.True(t, approxEqual(inset.Max.U, 16.0/64-0.5/64), "inset max u")
	require.True(t, approxEqual(inset.Max.V, 16.0/32-0.5/32), "inset max v")
}

// TestReadNegativePadding rejects invalid options before rendering.
func TestReadNegativePadding(t *testing.T) {
	var buf bytes.Buffer
	writeAseprite(&buf, 8, 8, 1, nil)
	_, err := ReadWith(&buf, Options{Padding: -1})
	require.True(t, err != nil, "negative padding must be rejected")
}

// TestMetadataReadOnlyAndOrderStable verifies that repeated decoding
// yields equal metadata and that frames retain file frame order.
func TestMetadataReadOnlyAndOrderStable(t *testing.T) {
	const nframes = 5
	var buf bytes.Buffer
	writeAseprite(&buf, 8, 8, nframes, map[int]bool{2: true})
	raw := buf.Bytes()

	first, err := ReadWith(bytes.NewReader(raw), Options{Padding: 1, MaxPageWidth: 24, MaxPageHeight: 24})
	require.NoError(t, err)
	second, err := ReadWith(bytes.NewReader(raw), Options{Padding: 1, MaxPageWidth: 24, MaxPageHeight: 24})
	require.NoError(t, err)

	require.True(t, len(first.Frames) == nframes && len(second.Frames) == nframes, "frames")
	for i := range first.Frames {
		a, b := first.Frames[i], second.Frames[i]
		require.True(t, a.Page == b.Page && a.Padding == b.Padding, "stable metadata")
		require.True(t, a.Bounds == b.Bounds, "stable bounds")
		require.True(t, a.UV == b.UV, "stable uv")
		require.True(t, a.Rotated == b.Rotated && a.FlipH == b.FlipH && a.FlipV == b.FlipV, "stable flags")
		require.True(t, first.Pages[a.Page].Bounds == second.Pages[b.Page].Bounds, "stable pages")
	}
}
