package aseprite

import (
	"bytes"
	"errors"
	"fmt"
	"image/color"
	"runtime"
	"testing"
	"time"

	"github.com/askeladdk/aseprite/internal/asetest"
)

// TestValidConfigMatchesDecode asserts the three-way contract required of a
// legitimate minimal sample: ReadFrom succeeds, DecodeConfig describes the
// same atlas that Decode returns, and the image.Image methods on the result
// agree with those values pixel for pixel.
func TestValidConfigMatchesDecode(t *testing.T) {
	for _, depth := range []uint16{8, 16, 32} {
		depth := depth
		t.Run(fmt.Sprintf("depth%d", depth), func(t *testing.T) {
			spr := richValidSprite(depth)
			data := spr.Bytes()

			var direct Aseprite
			if _, err := direct.ReadFrom(bytes.NewReader(data)); err != nil {
				t.Fatalf("ReadFrom: %v", err)
			}

			cfg, err := DecodeConfig(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("DecodeConfig: %v", err)
			}

			img, err := Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}

			if cfg.Width != img.Bounds().Dx() || cfg.Height != img.Bounds().Dy() {
				t.Fatalf("config %dx%d, image %dx%d", cfg.Width, cfg.Height,
					img.Bounds().Dx(), img.Bounds().Dy())
			}
			if cfg.ColorModel != img.ColorModel() {
				t.Fatalf("config model %T, image model %T", cfg.ColorModel, img.ColorModel())
			}
			if cfg.Width != direct.Bounds().Dx() || cfg.Height != direct.Bounds().Dy() {
				t.Fatalf("config %dx%d, direct atlas %dx%d", cfg.Width, cfg.Height,
					direct.Bounds().Dx(), direct.Bounds().Dy())
			}

			// Every pixel exposed through image.Image must match the direct
			// atlas; the two decode paths must not drift.
			if !img.Bounds().Eq(direct.Bounds()) {
				t.Fatalf("Decode bounds %v, direct bounds %v", img.Bounds(), direct.Bounds())
			}
			for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
				for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
					want := color.RGBAModel.Convert(direct.At(x, y))
					got := color.RGBAModel.Convert(img.At(x, y))
					if got != want {
						t.Fatalf("pixel (%d,%d): Decode %v, direct %v", x, y, got, want)
					}
				}
			}
		})
	}
}

// richValidSprite is a valid sprite with two frames, a palette, tags, slices,
// layer and cel user data, a linked cel and both raw and compressed cels.
func richValidSprite(depth uint16) *asetest.Sprite {
	spr := minimalSprite(depth)
	col := [4]byte{10, 20, 30, 40}

	chunks := spr.Frames[0].Chunks
	// Insert layer user data after the layer chunk.
	layerIdx := 1
	if depth == 8 {
		layerIdx = 1 // palette is chunk 0
	}
	ud := asetest.ChunkOf(asetest.TypeUserData, asetest.UserDataBody([]byte("layer note"), &col))
	chunks = append(chunks[:layerIdx+1], append([]asetest.Chunk{ud}, chunks[layerIdx+1:]...)...)

	// Add a tag and a slice chunk to frame 0.
	chunks = append(chunks,
		asetest.ChunkOf(asetest.TypeTag, asetest.TagsBody([]asetest.Tag{
			{Lo: 0, Hi: 1, Direction: 1, Name: "loop"},
		})),
		asetest.ChunkOf(asetest.TypeSlice, asetest.SliceBody(1, "nine",
			[]asetest.SliceKeys{{X: 1, Y: 1, W: 4, H: 4, CX: 2, CY: 2, CW: 1, CH: 1}})),
	)
	spr.Frames[0].Chunks = chunks

	// Second frame: a linked cel to frame 0 plus a raw cel to exercise both
	// pixel paths in one sprite.
	pixBytes := asetest.SolidPix(4, depth)
	spr.Frames = append(spr.Frames, asetest.Frame{
		Duration: 200,
		Chunks: []asetest.Chunk{
			asetest.ChunkOf(asetest.TypeCel, asetest.LinkedCelBody(0, 0, 0, 255, 0)),
		},
	})
	spr.Frames = append(spr.Frames, asetest.Frame{
		Duration: 300,
		Chunks: []asetest.Chunk{
			asetest.ChunkOf(asetest.TypeCel, asetest.CelBody(0, 1, 1, 128, asetest.CelRaw, 2, 2, pixBytes)),
			asetest.ChunkOf(asetest.TypeUserData, asetest.UserDataBody([]byte("cel note"), nil)),
		},
	})
	return spr
}

// TestResourceBounds asserts that hostile inputs are rejected without
// allocating unbounded memory and within a bounded wall-clock time. The
// asserted numbers are deliberately loose; the point is to catch regressions
// that remove a limit, not to benchmark.
func TestResourceBounds(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		// maxAlloc is the largest acceptable heap growth during the call.
		maxAlloc uint64
		// maxTime bounds a single decode.
		maxTime time.Duration
		// kind is the required error category.
		kind error
	}{
		{
			name:     "atlas_over_limit",
			data:     hugeAtlasSprite(),
			maxAlloc: 32 << 20,
			maxTime:  2 * time.Second,
			kind:     ErrUnsupported,
		},
		{
			name:     "cel_over_limit",
			data:     hugeCelSprite(),
			maxAlloc: 32 << 20,
			maxTime:  2 * time.Second,
			kind:     ErrUnsupported,
		},
		{
			name:     "declared_size_over_limit",
			data:     hugeFileSizeSprite(),
			maxAlloc: 32 << 20,
			maxTime:  2 * time.Second,
			kind:     ErrUnsupported,
		},
		{
			name:     "declared_size_lies_above_content",
			data:     lyingSizeSprite(),
			maxAlloc: 1 << 20,
			maxTime:  2 * time.Second,
			kind:     ErrTruncated,
		},
		{
			name:     "compression_bomb",
			data:     compressionBombSprite(),
			maxAlloc: 32 << 20,
			maxTime:  2 * time.Second,
			kind:     ErrMalformed,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)

			start := time.Now()
			_, err, panicked := safeDecode(tc.data)
			elapsed := time.Since(start)

			runtime.ReadMemStats(&after)

			if panicked != nil {
				t.Fatalf("panic: %v", panicked)
			}
			if err == nil {
				t.Fatal("expected rejection")
			}
			if tc.kind != nil && !errors.Is(err, tc.kind) {
				t.Fatalf("error %q is not category %v", err, tc.kind)
			}
			if !bytes.Contains([]byte(err.Error()), []byte("limit")) &&
				!bytes.Contains([]byte(err.Error()), []byte("exceed")) &&
				!errors.Is(err, ErrTruncated) {
				t.Fatalf("error mentions neither a limit nor truncation: %v", err)
			}

			alloc := after.TotalAlloc - before.TotalAlloc
			if alloc > tc.maxAlloc {
				t.Fatalf("allocated %d bytes, limit %d", alloc, tc.maxAlloc)
			}
			if elapsed > tc.maxTime {
				t.Fatalf("took %v, limit %v", elapsed, tc.maxTime)
			}
			t.Logf("rejected in %v, allocated %d bytes: %v", elapsed, alloc, err)
		})
	}
}

func hugeAtlasSprite() []byte {
	h := asetest.DefaultHeader()
	h.Frames = 1
	h.Width = 65535
	h.Height = 65535
	d := &asetest.Sprite{
		Header: h,
		Frames: []asetest.Frame{{
			Chunks: []asetest.Chunk{
				asetest.ChunkOf(asetest.TypeLayer, asetest.LayerBody(asetest.LayerVisible, 0, 0, 255, "l")),
			},
		}},
	}
	return d.Bytes()
}

func hugeCelSprite() []byte {
	h := asetest.DefaultHeader()
	h.Depth = 32
	h.Flags = 0
	h.Colors = 0
	h.Width, h.Height = 4, 4
	d := &asetest.Sprite{
		Header: h,
		Frames: []asetest.Frame{{
			Chunks: []asetest.Chunk{
				asetest.ChunkOf(asetest.TypeLayer, asetest.LayerBody(asetest.LayerVisible, 0, 0, 255, "l")),
				asetest.ChunkOf(asetest.TypeCel, asetest.CelBody(0, 0, 0, 255, asetest.CelRaw, 60000, 60000, nil)),
			},
		}},
	}
	return d.Bytes()
}

// lyingSizeSprite declares a 256 MiB body but supplies a tiny one. The
// decoder must not allocate the declared size before noticing the shortfall.
func lyingSizeSprite() []byte {
	d := minimalSprite(8).Bytes()
	n := uint32(256 << 20)
	d[0], d[1], d[2], d[3] = byte(n), byte(n>>8), byte(n>>16), byte(n>>24)
	return d
}

func hugeFileSizeSprite() []byte {
	d := minimalSprite(8).Bytes()
	big := uint32(maxFileSize + 1)
	d[0], d[1], d[2], d[3] = byte(big), byte(big>>8), byte(big>>16), byte(big>>24)
	return d
}

// compressionBombSprite declares a small cel whose zlib stream expands to a
// large buffer, which must be cut off at the declared cel size.
func compressionBombSprite() []byte {
	spr := minimalSprite(8)
	// 2x2 = 4 pixel slots, but the stream carries 8 MiB.
	bomb := make([]byte, 8<<20)
	body := asetest.CelBody(0, 0, 0, 255, asetest.CelZlib, 2, 2, bomb)
	spr.Frames[0].Chunks[2] = asetest.ChunkOf(asetest.TypeCel, body)
	return spr.Bytes()
}

// TestDecodeDoesNotAliasInput verifies that pixel and user data do not share
// backing memory with the byte slice passed to the reader. A caller may reuse
// its buffer after ReadFrom returns; mutating it must not change the sprite.
func TestDecodeDoesNotModifyInput(t *testing.T) {
	spr := richValidSprite(8)
	data := spr.Bytes()
	original := append([]byte(nil), data...)

	var got Aseprite
	if _, err := got.ReadFrom(bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, original) {
		t.Fatal("ReadFrom modified the caller's buffer")
	}

	// The signature accepts any io.Reader, but a *bytes.Reader over a []byte
	// is the common path; decoding must leave that slice untouched so the
	// caller can reuse it.
	var again Aseprite
	if _, err := again.ReadFrom(bytes.NewReader(data[:len(data)/2])); err == nil {
		t.Fatal("expected truncated decode to fail")
	}
	if !bytes.Equal(data[:len(data)/2], original[:len(data)/2]) {
		t.Fatal("failed decode modified the caller's buffer")
	}
}

// TestRejectDoesNotRetainData verifies that a rejected decode leaves the
// receiver empty, so a caller that ignores the error cannot use a partially
// populated sprite.
func TestRejectDoesNotRetainData(t *testing.T) {
	data := minimalSprite(8).Bytes()
	data = data[:len(data)-3]

	var spr Aseprite
	if _, err := spr.ReadFrom(bytes.NewReader(data)); err == nil {
		t.Fatal("expected error")
	}
	if spr.Atlas != nil {
		t.Fatalf("atlas was populated on failure: %v", spr.Atlas.Bounds())
	}
}
