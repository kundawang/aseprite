package aseprite

import (
	"image"
	"image/color"
	"os"
	"testing"

	"github.com/askeladdk/aseprite/internal/require"
)

// nrgbaAt reads an NRGBA color from the atlas rectangle of a frame.
func nrgbaAt(img image.Image, x, y int) color.NRGBA {
	return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
}

// TestPaletteAnimation decodes the three-frame fixture in which the pixel
// indices are identical on every frame and palette entry 1 is repainted per
// frame (red, green, blue). It checks the raw indices, the effective
// per-frame palettes and the colors that end up in the texture atlas.
func TestPaletteAnimation(t *testing.T) {
	f, err := os.Open("./testfiles/palette_anim.aseprite")
	require.NoError(t, err)
	defer f.Close()

	spr, err := Read(f)
	require.NoError(t, err)
	require.True(t, len(spr.Frames) == 3, "frames", len(spr.Frames))

	wantIndex1 := []color.NRGBA{
		{R: 255, G: 0, B: 0, A: 255},
		{R: 0, G: 255, B: 0, A: 255},
		{R: 0, G: 0, B: 255, A: 255},
	}

	// Indices 2 and 3 are declared once on frame 0 and inherited afterwards.
	wantInherited := map[uint8]color.NRGBA{
		2: {R: 255, G: 255, B: 0, A: 255},
		3: {R: 255, G: 0, B: 255, A: 255},
	}

	// All frames share the same pixel indices; the animation is palette-only.
	wantPixels := []byte{0, 1, 2, 3}

	// Parse the raw cels once to inspect pixel indices independently of the
	// palette that is bound to them.
	var p file
	pf, err := os.Open("./testfiles/palette_anim.aseprite")
	require.NoError(t, err)
	_, err = p.ReadFrom(pf)
	pf.Close()
	require.NoError(t, err)
	require.NoError(t, p.initLayers())
	require.NoError(t, p.initCels())

	for frame := range spr.Frames {
		fr := spr.Frames[frame]
		require.True(t, len(fr.Palette) == 4, "palette length", len(fr.Palette))

		// Exposed effective palette of the frame.
		require.True(t,
			fr.Palette[1] == wantIndex1[frame],
			"frame palette index 1", frame, fr.Palette[1])
		for idx, want := range wantInherited {
			require.True(t, fr.Palette[idx] == want,
				"inherited palette index", frame, idx, fr.Palette[idx])
		}
		// The transparent index stays transparent on every frame.
		_, _, _, a0 := fr.Palette[0].RGBA()
		require.True(t, a0 == 0, "transparent index", frame)

		// Raw pixel indices are unchanged and shared through the linked cel.
		cel := p.frames[frame].cels[0].image.(*image.Paletted)
		require.True(t, len(cel.Pix) == len(wantPixels), "pixel count")
		for i, idx := range wantPixels {
			require.True(t, cel.Pix[i] == idx, "pixel index", frame, i, cel.Pix[i])
		}

		// Atlas colors of this frame match its effective palette.
		r := fr.Bounds
		got := []color.NRGBA{
			nrgbaAt(spr, r.Min.X, r.Min.Y),
			nrgbaAt(spr, r.Min.X+1, r.Min.Y),
			nrgbaAt(spr, r.Min.X, r.Min.Y+1),
			nrgbaAt(spr, r.Min.X+1, r.Min.Y+1),
		}
		require.True(t, got[0].A == 0, "atlas transparent pixel", frame)
		require.True(t, got[1] == wantIndex1[frame], "atlas animated pixel", frame)
		require.True(t, got[2] == wantInherited[2], "atlas inherited pixel 2", frame)
		require.True(t, got[3] == wantInherited[3], "atlas inherited pixel 3", frame)
	}
}

// TestParseChunk2019 covers the deterministic handling of incremental palette
// chunks: partial ranges, repeated updates, out of range indices, truncated
// data and named entries.
func TestParseChunk2019(t *testing.T) {
	chunk := func(fields ...[]byte) []byte {
		raw := make([]byte, 20)
		for _, b := range fields {
			raw = append(raw, b...)
		}
		return raw
	}
	entry := func(rgba [4]byte, name string) []byte {
		b := []byte{0, 0, rgba[0], rgba[1], rgba[2], rgba[3]}
		if name != "" {
			b[0] = 1 // entry flag: has name
			b = append(b, byte(len(name)), 0)
			b = append(b, name...)
		}
		return b
	}
	setRange := func(raw []byte, size, first, last uint32) []byte {
		copy(raw[0:], u32(size))
		copy(raw[4:], u32(first))
		copy(raw[8:], u32(last))
		return raw
	}

	t.Run("partial range leaves other indices untouched", func(t *testing.T) {
		pal := color.Palette{
			color.Transparent,
			color.NRGBA{R: 1, G: 1, B: 1, A: 255},
			color.NRGBA{R: 2, G: 2, B: 2, A: 255},
		}
		raw := setRange(chunk(entry([4]byte{9, 9, 9, 255}, "")), 3, 1, 1)
		parseChunk2019(pal, raw)
		require.True(t, pal[1] == color.NRGBA{R: 9, G: 9, B: 9, A: 255}, "updated")
		require.True(t, pal[2] == color.NRGBA{R: 2, G: 2, B: 2, A: 255}, "inherited")
	})

	t.Run("same index updated repeatedly across chunks wins last", func(t *testing.T) {
		pal := make(color.Palette, 2)
		first := setRange(chunk(entry([4]byte{10, 0, 0, 255}, "")), 2, 0, 0)
		second := setRange(chunk(entry([4]byte{20, 0, 0, 255}, "")), 2, 0, 0)
		parseChunk2019(pal, first)
		parseChunk2019(pal, second)
		require.True(t, pal[0] == color.NRGBA{R: 20, G: 0, B: 0, A: 255}, "last write")
		require.True(t, pal[1] == nil, "untouched index")
	})

	t.Run("out of range indices are ignored", func(t *testing.T) {
		pal := make(color.Palette, 2)
		raw := setRange(chunk(entry([4]byte{5, 5, 5, 255}, "")), 2, 5, 5)
		parseChunk2019(pal, raw) // must not panic and must not write
		require.True(t, pal[0] == nil && pal[1] == nil, "no writes")
	})

	t.Run("truncated chunks stop without panic", func(t *testing.T) {
		pal := make(color.Palette, 4)
		// header claims four entries but only one is present
		raw := setRange(chunk(entry([4]byte{1, 2, 3, 255}, "")), 4, 0, 3)
		parseChunk2019(pal, raw)
		require.True(t, pal[0] == color.NRGBA{R: 1, G: 2, B: 3, A: 255}, "first entry")
		require.True(t, pal[3] == nil, "missing entry untouched")

		// truncated even before the fixed header
		parseChunk2019(pal, make([]byte, 4))
		// truncated inside a named entry
		named := setRange(chunk([]byte{1, 0, 1, 2, 3, 4, 50, 0}), 4, 0, 0)
		parseChunk2019(pal, named)
	})

	t.Run("named entries are skipped", func(t *testing.T) {
		pal := make(color.Palette, 2)
		raw := setRange(chunk(
			entry([4]byte{7, 7, 7, 255}, "red"),
			entry([4]byte{8, 8, 8, 255}, ""),
		), 2, 0, 1)
		parseChunk2019(pal, raw)
		require.True(t, pal[0] == color.NRGBA{R: 7, G: 7, B: 7, A: 255}, "named entry color")
		require.True(t, pal[1] == color.NRGBA{R: 8, G: 8, B: 8, A: 255}, "next entry")
	})

	t.Run("empty palette is a no-op", func(t *testing.T) {
		raw := setRange(chunk(entry([4]byte{1, 1, 1, 255}, "")), 1, 0, 0)
		parseChunk2019(color.Palette{}, raw)
	})
}

// TestParseOldPaletteChunks exercises the packet parser shared by 0x0004 and
// 0x0011, including skips, the 0-means-256 packet size, truncation and bounds.
func TestParseOldPaletteChunks(t *testing.T) {
	t.Run("0x0004 full 8-bit colors with skips", func(t *testing.T) {
		raw := []byte{
			2, 0, // packet count (WORD)
			// packet 1: skip 1, one color
			1, 1, 10, 20, 30,
			// packet 2: skip 0, two colors
			0, 2, 1, 2, 3, 4, 5, 6,
		}
		pal := make(color.Palette, 4)
		parseChunk0004(pal, raw)
		require.True(t, pal[1] == color.NRGBA{R: 10, G: 20, B: 30, A: 255}, "index 1")
		require.True(t, pal[2] == color.NRGBA{R: 1, G: 2, B: 3, A: 255}, "index 2")
		require.True(t, pal[3] == color.NRGBA{R: 4, G: 5, B: 6, A: 255}, "index 3")
		require.True(t, pal[0] == nil, "skipped index untouched")
	})

	t.Run("0x0011 scales 6-bit components", func(t *testing.T) {
		raw := []byte{
			1, 0, // packet count (WORD)
			0, 1, 63, 63, 63,
		}
		pal := make(color.Palette, 1)
		parseChunk0011(pal, raw)
		require.True(t, pal[0] == color.NRGBA{R: 252, G: 252, B: 252, A: 255}, "scaled color")
	})

	t.Run("packet running past the palette is bounded", func(t *testing.T) {
		// skip 250 then a 256-sized packet: indices run past a 4-entry palette
		raw := []byte{1, 0, 0, 250, 0}
		for i := 0; i < 256; i++ {
			raw = append(raw, 9, 9, 9)
		}
		pal := make(color.Palette, 4)
		parseChunk0004(pal, raw) // must not panic
		require.True(t, pal[3] == color.NRGBA{R: 9, G: 9, B: 9, A: 255}, "last in-range write")
	})

	t.Run("truncated packets stop without panic", func(t *testing.T) {
		parseChunk0004(make(color.Palette, 4), []byte{1, 0})
		parseChunk0011(make(color.Palette, 4), []byte{1, 0, 0, 0, 2, 1})
		parseChunk0004(color.Palette{}, []byte{1, 0, 0, 0, 1, 1, 2, 3})
	})
}

// TestInitPalettesIncremental verifies frame-by-frame accumulation, including
// inheritance when a frame has no palette chunk and a forced transparent
// index even when a chunk repaints it.
func TestInitPalettes(t *testing.T) {
	pal2019 := func(first, last uint32, colors ...[4]byte) chunk {
		raw := make([]byte, 20)
		copy(raw[0:], u32(uint32(len(colors))))
		copy(raw[4:], u32(first))
		copy(raw[8:], u32(last))
		for _, c := range colors {
			raw = append(raw, 0, 0, c[0], c[1], c[2], c[3])
		}
		return chunk{typ: 0x2019, raw: raw}
	}

	f := file{
		bpp:         8,
		palette:     make(color.Palette, 3),
		transparent: 0,
		frames: []frame{
			{chunks: []chunk{pal2019(0, 2,
				[4]byte{0, 0, 0, 0}, [4]byte{255, 0, 0, 255}, [4]byte{0, 255, 0, 255})}},
			{}, // no palette chunk: inherit frame 0
			{chunks: []chunk{pal2019(1, 1, [4]byte{0, 0, 255, 255})}},
			{chunks: []chunk{pal2019(0, 0, [4]byte{123, 123, 123, 255})}}, // repaint transparent
		},
	}

	f.initPalettes()
	require.True(t, len(f.framePalettes) == 4, "frame palette count")

	// frame 1 inherits frame 0 wholesale
	require.True(t, palettesEqual(f.framePalettes[0], f.framePalettes[1]), "inheritance")

	// frame 2 changes only index 1
	require.True(t, f.framePalettes[2][1] == color.NRGBA{R: 0, G: 0, B: 255, A: 255}, "incremental update")
	require.True(t, f.framePalettes[2][2] == color.NRGBA{R: 0, G: 255, B: 0, A: 255}, "sibling inherited")

	// frame 3's attempt to repaint the transparent index is overridden
	_, _, _, a := f.framePalettes[3][0].RGBA()
	require.True(t, a == 0, "transparent index forced")

	// stored palettes are independent copies; mutating one must not affect others
	f.framePalettes[2][1] = color.Black
	require.True(t, f.framePalettes[1][1] == color.NRGBA{R: 255, G: 0, B: 0, A: 255}, "palette copies are independent")
}

// TestNonIndexedSpritesHaveNoFramePalettes confirms that grayscale and RGBA
// sprites do not expose frame palettes and still decode.
func TestNonIndexedSpritesHaveNoFramePalettes(t *testing.T) {
	for _, name := range []string{
		"./testfiles/slime_grayscale.aseprite",
		"./testfiles/blendtest.aseprite",
	} {
		f, err := os.Open(name)
		require.NoError(t, err)
		spr, err := Read(f)
		f.Close()
		require.NoError(t, err)
		for i, fr := range spr.Frames {
			require.True(t, fr.Palette == nil, name, "frame", i)
		}
	}
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
	return b
}
