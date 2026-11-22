package aseprite

import (
	"bytes"
	"strconv"
	"testing"

	"github.com/askeladdk/aseprite/internal/asetest"
)

// minimalSprite returns a valid one-frame 8-bit sprite with one visible layer,
// a 4x4 palette, one compressed cel, a tag and a slice. Every field is built
// by asetest from scratch.
func minimalSprite(depth uint16) *asetest.Sprite {
	h := asetest.DefaultHeader()
	h.Depth = depth
	if depth != 8 {
		h.Colors = 0
		h.Flags = 0
	}

	chunks := []asetest.Chunk{
		asetest.ChunkOf(asetest.TypeLayer, asetest.LayerBody(asetest.LayerVisible, 0, 0, 255, "layer")),
		asetest.ChunkOf(asetest.TypeCel, asetest.CelBody(0, 0, 0, 255, asetest.CelZlib, 2, 2, asetest.SolidPix(4, depth))),
	}
	if depth == 8 {
		pal := [][4]byte{
			{0, 0, 0, 0},
			{255, 0, 0, 255},
			{0, 255, 0, 255},
			{0, 0, 255, 255},
		}
		chunks = append([]asetest.Chunk{
			asetest.ChunkOf(asetest.TypePalette, asetest.PaletteBody(0, pal, nil)),
		}, chunks...)
	}

	return &asetest.Sprite{
		Header: h,
		Frames: []asetest.Frame{{
			Duration: 100,
			Chunks:   chunks,
		}},
	}
}

func TestMinimalFixtureDecodes(t *testing.T) {
	for _, depth := range []uint16{8, 16, 32} {
		t.Run(strconv.Itoa(int(depth)), func(t *testing.T) {
			spr := minimalSprite(depth)
			data := spr.Bytes()

			var got Aseprite
			n, err := got.ReadFrom(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("ReadFrom: %v", err)
			}
			if n != int64(len(data)) {
				t.Fatalf("ReadFrom returned %d bytes, want %d", n, len(data))
			}
			if len(got.Frames) != 1 {
				t.Fatalf("frames = %d, want 1", len(got.Frames))
			}
			if got.Frames[0].Duration.Milliseconds() != 100 {
				t.Fatalf("duration = %v, want 100ms", got.Frames[0].Duration)
			}
			if len(got.LayerData) != 0 {
				t.Fatalf("layer data = %d, want 0", len(got.LayerData))
			}

			// Config and image.Image must agree with the decoded atlas.
			cfg, err := DecodeConfig(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("DecodeConfig: %v", err)
			}
			if cfg.Width != got.Bounds().Dx() || cfg.Height != got.Bounds().Dy() {
				t.Fatalf("config %dx%d, atlas %dx%d", cfg.Width, cfg.Height, got.Bounds().Dx(), got.Bounds().Dy())
			}
			if cfg.ColorModel != got.ColorModel() {
				t.Fatalf("config model %T, atlas model %T", cfg.ColorModel, got.ColorModel())
			}

			img, err := Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if !img.Bounds().Eq(got.Bounds()) {
				t.Fatalf("Decode bounds %v, atlas bounds %v", img.Bounds(), got.Bounds())
			}
		})
	}
}
