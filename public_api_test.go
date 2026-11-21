package aseprite_test

// External tests exercise the public API only, the way a consumer uses it:
// image.Decode / image.DecodeConfig with a caller-owned []byte, concurrent
// decodes, and equality of the values returned to different callers. Keeping
// these in package aseprite_test ensures the determinism guarantees do not
// depend on unexported internals.

import (
	"bytes"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/askeladdk/aseprite"
)

func fixtureNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("testfiles", "determinism"))
	if err != nil {
		t.Fatalf("read fixture dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, filepath.Join("testfiles", "determinism", e.Name()))
		}
	}
	names = append(names,
		"testfiles/slime_paletted.aseprite",
		"testfiles/slime_grayscale.aseprite",
		"testfiles/blendtest.aseprite",
	)
	return names
}

// summarize flattens a decoded sprite into comparable values.
func summarize(t *testing.T, path string, img image.Image) []byte {
	t.Helper()
	spr, ok := img.(*aseprite.Aseprite)
	if !ok {
		t.Fatalf("%s: image.Decode returned %T", path, img)
	}
	var out bytes.Buffer
	r := img.Bounds()
	b := []byte{
		byte(r.Min.X >> 8), byte(r.Min.X), byte(r.Min.Y >> 8), byte(r.Min.Y),
		byte(r.Max.X >> 8), byte(r.Max.X), byte(r.Max.Y >> 8), byte(r.Max.Y),
	}
	out.Write(b)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cr, cg, cb, ca := img.At(x, y).RGBA()
			out.Write([]byte{byte(cr >> 8), byte(cg >> 8), byte(cb >> 8), byte(ca >> 8)})
		}
	}
	for _, f := range spr.Frames {
		br := f.Bounds
		out.Write([]byte{
			byte(br.Min.X), byte(br.Min.Y), byte(br.Max.X), byte(br.Max.Y),
			byte(f.Duration >> 24), byte(f.Duration >> 16), byte(f.Duration >> 8), byte(f.Duration),
		})
		for _, d := range f.Data {
			out.Write(d)
			out.WriteByte(0)
		}
	}
	for _, tag := range spr.Tags {
		out.WriteString(tag.Name)
		out.Write([]byte{byte(tag.Lo >> 8), byte(tag.Lo), byte(tag.Hi >> 8), byte(tag.Hi), byte(tag.Repeat), byte(tag.LoopDirection)})
	}
	for _, s := range spr.Slices {
		out.WriteString(s.Name)
		br := s.Bounds
		out.Write([]byte{
			byte(br.Min.X), byte(br.Min.Y), byte(br.Max.X), byte(br.Max.Y),
			byte(s.Center.Min.X), byte(s.Center.Min.Y), byte(s.Center.Max.X), byte(s.Center.Max.Y),
			byte(s.Pivot.X), byte(s.Pivot.Y),
		})
		out.Write(s.Data)
	}
	for _, d := range spr.LayerData {
		out.Write(d)
		out.WriteByte(0)
	}
	return out.Bytes()
}

// TestImageDecodeDeterministic uses the registered image format path, which is
// what most consumers call.
func TestImageDecodeDeterministic(t *testing.T) {
	for _, path := range fixtureNames(t) {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			input, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			img, format, err := image.Decode(bytes.NewReader(input))
			if err != nil {
				t.Fatalf("image.Decode: %v", err)
			}
			if format != "aseprite" {
				t.Fatalf("format = %q", format)
			}
			want := summarize(t, path, img)

			// Repeat serially.
			for i := 0; i < 4; i++ {
				img, _, err := image.Decode(bytes.NewReader(input))
				if err != nil {
					t.Fatalf("repeat %d: %v", i, err)
				}
				if got := summarize(t, path, img); !bytes.Equal(got, want) {
					t.Fatalf("repeat %d: decoded value changed", i)
				}
			}

			// Concurrently, each goroutine decoding its own copy.
			const workers = 8
			var wg sync.WaitGroup
			errs := make(chan string, workers)
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					img, _, err := image.Decode(bytes.NewReader(input))
					if err != nil {
						errs <- err.Error()
						return
					}
					if got := summarize(t, path, img); !bytes.Equal(got, want) {
						errs <- "decoded value changed under concurrency"
					}
				}()
			}
			wg.Wait()
			close(errs)
			for e := range errs {
				t.Fatal(e)
			}
		})
	}
}

// TestDecodeConfigMatchesDecode checks the public config path agrees with a
// full decode.
func TestDecodeConfigMatchesDecode(t *testing.T) {
	for _, path := range fixtureNames(t) {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			input, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			cfg, format, err := image.DecodeConfig(bytes.NewReader(input))
			if err != nil {
				t.Fatalf("image.DecodeConfig: %v", err)
			}
			if format != "aseprite" {
				t.Fatalf("format = %q", format)
			}
			img, _, err := image.Decode(bytes.NewReader(input))
			if err != nil {
				t.Fatalf("image.Decode: %v", err)
			}
			r := img.Bounds()
			if cfg.Width != r.Dx() || cfg.Height != r.Dy() {
				t.Fatalf("config %dx%d, atlas %dx%d", cfg.Width, cfg.Height, r.Dx(), r.Dy())
			}
			// color.Palette is a slice, so compare its entries rather than
			// using ==. Any other model is a comparable singleton value.
			if cp, ok := cfg.ColorModel.(color.Palette); ok {
				ip, ok := img.ColorModel().(color.Palette)
				if !ok || !reflect.DeepEqual(cp, ip) {
					t.Fatalf("palette color models differ: %T vs %T", cfg.ColorModel, img.ColorModel())
				}
			} else if cfg.ColorModel != img.ColorModel() {
				t.Fatalf("color models differ: %T vs %T", cfg.ColorModel, img.ColorModel())
			}
		})
	}
}
