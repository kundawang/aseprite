package aseprite

import (
	"bytes"
	"testing"

	"github.com/askeladdk/aseprite/internal/asetest"
)

// FuzzDecodeNoPanic is a property test layered on the hand-written matrix.
// Under `go test` it runs only the seed corpus; under `go test -fuzz` it
// explores nearby inputs. The property is narrow and absolute: no input, valid
// or not, may panic or return a nil error with a nil result.
func FuzzDecodeNoPanic(f *testing.F) {
	for _, depth := range []uint16{8, 16, 32} {
		f.Add(minimalSprite(depth).Bytes())
	}
	for _, tc := range matrix() {
		f.Add(tc.Bytes())
	}
	f.Add([]byte{})
	f.Add([]byte{0xA5, 0xE0})

	f.Fuzz(func(t *testing.T, data []byte) {
		var spr Aseprite
		n, err := spr.ReadFrom(bytes.NewReader(data))
		if err == nil {
			if n != int64(len(data)) {
				t.Fatalf("success reported %d bytes read of %d", n, len(data))
			}
			if spr.Atlas == nil {
				t.Fatal("success returned a nil atlas")
			}
		}

		// DecodeConfig deliberately stops after the header and palette, so a
		// successful config does not imply that the whole file decodes. The
		// invariant is narrower: whenever both succeed they must describe the
		// same image.
		cfg, cerr := DecodeConfig(bytes.NewReader(data))
		if cerr == nil {
			if cfg.Width <= 0 || cfg.Height <= 0 {
				t.Fatalf("success returned a %dx%d config", cfg.Width, cfg.Height)
			}
			if err == nil {
				if cfg.Width != spr.Bounds().Dx() || cfg.Height != spr.Bounds().Dy() {
					t.Fatalf("config %dx%d, image %dx%d", cfg.Width, cfg.Height,
						spr.Bounds().Dx(), spr.Bounds().Dy())
				}
				if cfg.ColorModel != spr.ColorModel() {
					t.Fatalf("config and image colour models differ")
				}
			}
		}
	})
}

var _ = asetest.TypeLayer
