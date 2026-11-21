package aseprite

import (
	"encoding/binary"
	"image"
	"image/color"
	"os"
	"testing"

	"github.com/askeladdk/aseprite/internal/require"
)

// canvas is the shared canvas size of the generated negative-cel fixtures.
const canvas = 16

// quadrantCels describes the four cels that cross each canvas corner.
// Coordinates are the cel's original sprite position; cels are 8x8 and
// overlap the canvas by 4 pixels on every crossed edge.
var quadrantCels = []image.Rectangle{
	image.Rect(-4, -4, 4, 4),                           // top-left
	image.Rect(canvas-4, -4, canvas+4, 4),              // top-right
	image.Rect(-4, canvas-4, 4, canvas+4),              // bottom-left
	image.Rect(canvas-4, canvas-4, canvas+4, canvas+4), // bottom-right
}

// expectedViewport independently computes the canvas-clipped rectangle and
// the offset of the clipped image's top-left pixel within the original cel,
// using only the image package's Rectangle arithmetic.
func expectedViewport(cel image.Rectangle) (viewport image.Rectangle, offset image.Point) {
	frame := image.Rect(0, 0, canvas, canvas)
	viewport = cel.Intersect(frame)
	offset = viewport.Min.Sub(cel.Min)
	return
}

func loadFixture(t *testing.T, name string) *Aseprite {
	t.Helper()

	f, err := os.Open(name)
	require.NoError(t, err)
	defer f.Close()

	spr, err := Read(f)
	require.NoError(t, err)
	return spr
}

// rgbaAt returns the 8-bit NRGBA channels of a frame-local pixel.
func rgbaAt(spr *Aseprite, frame, x, y int) color.NRGBA {
	o := spr.Frames[frame].Bounds.Min
	r, g, b, a := spr.At(o.X+x, o.Y+y).RGBA()
	return color.NRGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
}

func requirePixel(t *testing.T, spr *Aseprite, frame, x, y int, want color.NRGBA) {
	t.Helper()

	if got := rgbaAt(spr, frame, x, y); got != want {
		t.Fatalf("frame %d pixel (%d,%d) = %v, want %v", frame, x, y, got, want)
	}
}

// TestQuadrantViewports verifies the clipping geometry of the four
// corner-crossing cels against an independent image.Rectangle reference.
func TestQuadrantViewports(t *testing.T) {
	frame := image.Rect(0, 0, canvas, canvas)

	for _, cel := range quadrantCels {
		viewport, offset := expectedViewport(cel)

		// The intersection must stay within the canvas and be non-empty.
		require.True(t, viewport.In(frame), "viewport inside canvas")
		require.True(t, !viewport.Empty(), "viewport non-empty")

		// Each corner cel overlaps a 4x4 quadrant of the canvas.
		require.True(t, viewport.Dx() == 4 && viewport.Dy() == 4, "viewport is 4x4")

		// Offset maps the clipped image back into cel-local coordinates.
		require.True(t, cel.Min.Add(offset) == viewport.Min, "offset maps cel to viewport")
	}

	// A fully outside cel must clip to an empty rectangle instead, in every
	// direction beyond the canvas.
	for _, outside := range []image.Rectangle{
		image.Rect(-24, -24, -16, -16),                       // top-left
		image.Rect(canvas+8, canvas+8, canvas+16, canvas+16), // bottom-right
		image.Rect(-8, canvas, 0, canvas+8),                  // below-left
	} {
		require.True(t, outside.Intersect(frame).Empty(), "outside cel clips empty")
	}
}

// TestNegativeCels8 exercises the 8 bpp path: quadrant clipping, fully
// outside cels, zlib compression, linked cels and malformed cels.
func TestNegativeCels8(t *testing.T) {
	spr := loadFixture(t, "testdata/negative_cels_8.aseprite")

	pal := color.Palette{
		color.NRGBA{0, 0, 0, 0},       // 0 transparent
		color.NRGBA{255, 0, 0, 255},   // 1 red
		color.NRGBA{0, 255, 0, 255},   // 2 green
		color.NRGBA{0, 0, 255, 255},   // 3 blue
		color.NRGBA{255, 255, 0, 255}, // 4 yellow
		color.NRGBA{255, 0, 255, 255}, // 5 magenta
	}

	require.True(t, len(spr.Frames) == 6, "frame count")

	for i := range spr.Frames {
		require.True(t, spr.Frames[i].Bounds == image.Rect(
			(i%4)*canvas, (i/4)*canvas, (i%4)*canvas+canvas, (i/4)*canvas+canvas),
			"frame bounds")
	}

	// Frame 0: red top-left, green top-right.
	requirePixel(t, spr, 0, 0, 0, pal[1].(color.NRGBA))
	requirePixel(t, spr, 0, 3, 3, pal[1].(color.NRGBA))
	requirePixel(t, spr, 0, 4, 4, pal[0].(color.NRGBA))
	requirePixel(t, spr, 0, 15, 0, pal[2].(color.NRGBA))
	requirePixel(t, spr, 0, 12, 3, pal[2].(color.NRGBA))
	requirePixel(t, spr, 0, 11, 0, pal[0].(color.NRGBA))

	// Frame 1: blue bottom-left, yellow bottom-right.
	requirePixel(t, spr, 1, 0, 15, pal[3].(color.NRGBA))
	requirePixel(t, spr, 1, 3, 12, pal[3].(color.NRGBA))
	requirePixel(t, spr, 1, 15, 15, pal[4].(color.NRGBA))
	requirePixel(t, spr, 1, 12, 12, pal[4].(color.NRGBA))
	requirePixel(t, spr, 1, 8, 8, pal[0].(color.NRGBA))

	// Frame 2: both cels fully outside, the whole frame stays transparent.
	for y := 0; y < canvas; y++ {
		for x := 0; x < canvas; x++ {
			requirePixel(t, spr, 2, x, y, pal[0].(color.NRGBA))
		}
	}

	// Frame 3: zlib compressed magenta cel crossing the top-left.
	requirePixel(t, spr, 3, 0, 0, pal[5].(color.NRGBA))
	requirePixel(t, spr, 3, 3, 3, pal[5].(color.NRGBA))
	requirePixel(t, spr, 3, 4, 4, pal[0].(color.NRGBA))

	// Frame 4: linked cel reuses frame 0's red cel, same placement.
	requirePixel(t, spr, 4, 0, 0, pal[1].(color.NRGBA))
	requirePixel(t, spr, 4, 3, 3, pal[1].(color.NRGBA))
	requirePixel(t, spr, 4, 4, 4, pal[0].(color.NRGBA))

	// Frame 5: zero-dimension and truncated huge cels contribute nothing.
	for y := 0; y < canvas; y++ {
		for x := 0; x < canvas; x++ {
			requirePixel(t, spr, 5, x, y, pal[0].(color.NRGBA))
		}
	}
}

// TestNegativeCels16 exercises the 16 bpp grayscale path with the same
// coordinate semantics.
func TestNegativeCels16(t *testing.T) {
	spr := loadFixture(t, "testdata/negative_cels_16.aseprite")
	require.True(t, len(spr.Frames) == 4, "frame count")

	gray16 := func(v byte) color.NRGBA {
		// The decoder stores the aseprite value byte in a Gray16 pixel,
		// which widens it to the 16-bit little-endian channel v|v<<8.
		g := uint16(v) | uint16(v)<<8
		return color.NRGBA{uint8(g >> 8), uint8(g >> 8), uint8(g >> 8), 255}
	}

	// Frame 0: value 200 crosses the top-left, 100 the top-right.
	requirePixel(t, spr, 0, 0, 0, gray16(200))
	requirePixel(t, spr, 0, 3, 3, gray16(200))
	requirePixel(t, spr, 0, 12, 0, gray16(100))
	requirePixel(t, spr, 0, 15, 3, gray16(100))

	// Frame 1: bottom corners.
	requirePixel(t, spr, 1, 0, 15, gray16(60))
	requirePixel(t, spr, 1, 15, 15, gray16(160))

	// Frame 2: fully outside cels leave the frame at its cleared zero value.
	for y := 0; y < canvas; y++ {
		for x := 0; x < canvas; x++ {
			r, g, b, _ := rgbaAt(spr, 2, x, y).RGBA()
			require.True(t, r == 0 && g == 0 && b == 0, "outside cel is zero")
		}
	}

	// Frame 3: malformed cels contribute nothing, frame stays cleared.
	for y := 0; y < canvas; y++ {
		for x := 0; x < canvas; x++ {
			r, g, b, _ := rgbaAt(spr, 3, x, y).RGBA()
			require.True(t, r == 0 && g == 0 && b == 0, "malformed cel is zero")
		}
	}
}

// TestNegativeCels32 exercises the 32 bpp color path, including a blend-mode
// layer whose cel crosses the canvas origin.
func TestNegativeCels32(t *testing.T) {
	spr := loadFixture(t, "testdata/negative_cels_32.aseprite")
	require.True(t, len(spr.Frames) == 5, "frame count")

	red := color.NRGBA{255, 0, 0, 255}
	green := color.NRGBA{0, 255, 0, 255}
	blue := color.NRGBA{0, 0, 255, 255}
	white := color.NRGBA{255, 255, 255, 255}
	transparent := color.NRGBA{0, 0, 0, 0}

	// Frame 0: red on layer 0 crosses the top-left, green on layer 1 the
	// top-right. The regions do not overlap.
	requirePixel(t, spr, 0, 0, 0, red)
	requirePixel(t, spr, 0, 3, 3, red)
	requirePixel(t, spr, 0, 15, 0, green)
	requirePixel(t, spr, 0, 12, 3, green)
	requirePixel(t, spr, 0, 4, 4, transparent)

	// Frame 1: blue bottom-left, white bottom-right.
	requirePixel(t, spr, 1, 0, 15, blue)
	requirePixel(t, spr, 1, 15, 15, white)

	// Frame 2: fully outside.
	for y := 0; y < canvas; y++ {
		for x := 0; x < canvas; x++ {
			requirePixel(t, spr, 2, x, y, transparent)
		}
	}

	// Frame 3: gray background with a red multiply cel crossing the origin.
	// The visible 4x4 region is blended; the rest of the canvas stays gray.
	blended := rgbaAt(spr, 3, 0, 0)
	require.True(t, blended.A == 255, "blended alpha")
	require.True(t, blended.G == 0 && blended.B == 0, "multiply kills green/blue")
	requirePixel(t, spr, 3, 3, 3, blended)
	requirePixel(t, spr, 3, 4, 4, color.NRGBA{128, 128, 128, 255})
	requirePixel(t, spr, 3, 15, 15, color.NRGBA{128, 128, 128, 255})

	// Frame 4: malformed cels contribute nothing.
	for y := 0; y < canvas; y++ {
		for x := 0; x < canvas; x++ {
			requirePixel(t, spr, 4, x, y, transparent)
		}
	}
}

// TestNoFrameLeakage verifies that drawing a frame never writes beyond its
// own atlas rectangle into a neighboring frame. Each frame rectangle is
// scanned fully: a cel clipped to the canvas can only paint inside it.
func TestNoFrameLeakage(t *testing.T) {
	for _, name := range []string{
		"testdata/negative_cels_8.aseprite",
		"testdata/negative_cels_16.aseprite",
		"testdata/negative_cels_32.aseprite",
	} {
		spr := loadFixture(t, name)

		// Build a mask of pixels owned by some frame.
		owned := image.NewRGBA(spr.Bounds())
		for _, fr := range spr.Frames {
			for y := fr.Bounds.Min.Y; y < fr.Bounds.Max.Y; y++ {
				for x := fr.Bounds.Min.X; x < fr.Bounds.Max.X; x++ {
					owned.Set(x, y, color.Opaque)
				}
			}
		}

		// Every opaque pixel in the atlas must belong to a frame rectangle.
		for y := spr.Bounds().Min.Y; y < spr.Bounds().Max.Y; y++ {
			for x := spr.Bounds().Min.X; x < spr.Bounds().Max.X; x++ {
				if _, _, _, a := spr.At(x, y).RGBA(); a == 0 {
					continue
				}

				if _, _, _, oa := owned.At(x, y).RGBA(); oa == 0 {
					t.Fatalf("%s: opaque pixel %v lies outside every frame rectangle", name, image.Pt(x, y))
				}
			}
		}
	}
}

// TestParseImageCelGuards feeds malformed cel chunks directly to the parser
// and asserts they are skipped without panicking or corrupting the frame.
func TestParseImageCelGuards(t *testing.T) {
	mkFile := func(bpp uint16) *file {
		return &file{
			bpp:    bpp,
			frames: []frame{{cels: make([]cel, 1)}},
		}
	}

	t.Run("zero dimensions", func(t *testing.T) {
		f := mkFile(32)
		raw := make([]byte, 20) // width=0, height=0
		c, err := f.parseImageCel(0, 0, -4, -4, 255, raw, false)
		require.NoError(t, err)
		require.True(t, c == nil, "zero-dimension cel is skipped")
		require.True(t, f.frames[0].cels[0].image == nil, "frame cel untouched")
	})

	t.Run("truncated huge cel", func(t *testing.T) {
		f := mkFile(32)
		raw := make([]byte, 4+8) // claims 65535x65535 but holds 8 bytes
		binary.LittleEndian.PutUint16(raw[0:], 65535)
		binary.LittleEndian.PutUint16(raw[2:], 65535)
		c, err := f.parseImageCel(0, 0, -4, -4, 255, raw, false)
		require.NoError(t, err)
		require.True(t, c == nil, "truncated huge cel is skipped")
	})

	t.Run("huge coordinates valid cel", func(t *testing.T) {
		// A modest cel placed at a large positive coordinate must parse and
		// keep its bounds; clipping to the canvas happens later.
		f := mkFile(32)
		f.framew, f.frameh = canvas, canvas
		f.makeCel = makeCelImage32
		raw := make([]byte, 4+2*2*4) // 2x2 RGBA
		binary.LittleEndian.PutUint16(raw[0:], 2)
		binary.LittleEndian.PutUint16(raw[2:], 2)
		c, err := f.parseImageCel(0, 0, 30000, 30000, 255, raw, false)
		require.NoError(t, err)
		require.True(t, c != nil, "valid cel at huge coordinate parses")
		require.True(t, c.image.Bounds() == image.Rect(30000, 30000, 30002, 30002),
			"huge coordinate bounds preserved")
	})

	t.Run("negative origin valid cel", func(t *testing.T) {
		f := mkFile(8)
		f.palette = color.Palette{color.Transparent, color.NRGBA{255, 0, 0, 255}}
		f.makeCel = makeCelImage8
		raw := make([]byte, 4+4) // 2x2 indexed
		binary.LittleEndian.PutUint16(raw[0:], 2)
		binary.LittleEndian.PutUint16(raw[2:], 2)
		for i := 4; i < 8; i++ {
			raw[i] = 1
		}
		c, err := f.parseImageCel(0, 0, -2, -2, 255, raw, false)
		require.NoError(t, err)
		require.True(t, c.image.Bounds() == image.Rect(-2, -2, 0, 0), "negative bounds kept")
	})
}

// TestOriginalCelBoundsPreserved verifies that clipping never mutates the
// source cel: its stored bounds retain the original negative coordinates,
// and linked cels keep sharing their source cel.
func TestOriginalCelBoundsPreserved(t *testing.T) {
	f, err := os.Open("testdata/negative_cels_8.aseprite")
	require.NoError(t, err)
	defer f.Close()

	var file file
	_, err = file.ReadFrom(f)
	require.NoError(t, err)
	file.initPalette()
	require.NoError(t, file.initLayers())
	require.NoError(t, file.initCels())

	want := quadrantCels[0]

	// Frame 0's layer-0 cel keeps its original negative-coordinate bounds.
	srcCel := file.frames[0].cels[0].image
	require.True(t, srcCel.Bounds() == want, "cel bounds preserved")

	// Frame 4 links back to that cel: same underlying image, same bounds.
	linked := file.frames[4].cels[0].image
	require.True(t, linked == srcCel, "linked cel shares its source image")
	require.True(t, linked.Bounds() == want, "linked cel bounds preserved")

	// Snapshot a source pixel, build the atlas, then confirm it is unchanged.
	before := linked.At(want.Min.X, want.Min.Y)
	_, _ = file.buildAtlas()
	after := file.frames[0].cels[0].image.At(want.Min.X, want.Min.Y)
	require.True(t, before == after, "source pixel unchanged after atlas build")
	require.True(t, file.frames[0].cels[0].image.Bounds() == want,
		"cel bounds unchanged after atlas build")
}
