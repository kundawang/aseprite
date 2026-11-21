package aseprite

import (
	"bytes"
	"encoding/binary"
	"image"
	"os"
	"sync"
	"testing"
)

const blendtestFile = "./testfiles/blendtest.aseprite"

// patchLayerBlendMode returns a copy of the fixture with the blend mode
// WORD of the layer chunk at layerIndex replaced. Frame and chunk framing
// follows the Aseprite file format specification.
func patchLayerBlendMode(t *testing.T, layerIndex int, mode uint16) []byte {
	t.Helper()

	raw, err := os.ReadFile(blendtestFile)
	if err != nil {
		t.Fatal(err)
	}

	nframes := int(binary.LittleEndian.Uint16(raw[6:]))
	p := 128
	layer := 0
	for f := 0; f < nframes; f++ {
		frameLen := int(binary.LittleEndian.Uint32(raw[p:]))
		nchunks := int(binary.LittleEndian.Uint32(raw[p+12:]))
		if nchunks == 0 {
			nchunks = int(binary.LittleEndian.Uint16(raw[p+6:]))
		}
		q := p + 16
		for c := 0; c < nchunks; c++ {
			chunkLen := int(binary.LittleEndian.Uint32(raw[q:]))
			chunkType := binary.LittleEndian.Uint16(raw[q+4:])
			if chunkType == 0x2004 {
				if layer == layerIndex {
					binary.LittleEndian.PutUint16(raw[q+6+10:], mode)
					return raw
				}
				layer++
			}
			q += chunkLen
		}
		p += frameLen
	}

	t.Fatalf("layer %d not found", layerIndex)
	return nil
}

func decodeBytes(t *testing.T, raw []byte, opts DecodeOptions) *Aseprite {
	t.Helper()
	spr, err := DecodeWithOptions(bytes.NewReader(raw), opts)
	if err != nil {
		t.Fatal(err)
	}
	return spr
}

func TestDecodeKnownModesKeepPixels(t *testing.T) {
	// Strict decoding with the default registry must accept the fixture as
	// is and reproduce every pixel produced by the plain Decode path.
	raw, err := os.ReadFile(blendtestFile)
	if err != nil {
		t.Fatal(err)
	}

	plain, err := Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	strict := decodeBytes(t, raw, DecodeOptions{
		Registry: DefaultRegistry(), Strict: true, Fallback: Normal,
	})
	if len(strict.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", strict.Diagnostics)
	}
	assertImagesEqual(t, plain.(*Aseprite).Image, strict.Image)
}

func TestDecodeUnknownModeFallback(t *testing.T) {
	// Layer 2 uses Lighten (5); replace it with a reserved value.
	raw := patchLayerBlendMode(t, 2, 19)

	spr := decodeBytes(t, raw, DecodeOptions{
		Registry: DefaultRegistry(), Fallback: Normal,
	})
	if len(spr.Diagnostics) != 1 {
		t.Fatalf("expected 1 diagnostic, got %+v", spr.Diagnostics)
	}
	d := spr.Diagnostics[0]
	if d.Frame != 0 || d.Layer != 2 || d.Mode != Mode(19) || d.Kind != KindReserved {
		t.Fatalf("unexpected diagnostic: %+v", d)
	}

	// Explicit fallback to Lighten reproduces the original pixel result.
	fb := decodeBytes(t, raw, DecodeOptions{
		Registry: DefaultRegistry(), Fallback: Lighten,
	})
	orig, err := Decode(bytes.NewReader(mustReadFile(t, blendtestFile)))
	if err != nil {
		t.Fatal(err)
	}
	assertImagesEqual(t, orig.(*Aseprite).Image, fb.Image)
}

func TestDecodeReservedAndOutOfRangeKinds(t *testing.T) {
	for _, tt := range []struct {
		name string
		mode uint16
		kind DiagnosticKind
	}{
		{"reserved", 19, KindReserved},
		{"reserved-boundary", 255, KindReserved},
		{"out-of-range", 256, KindOutOfRange},
		{"out-of-range-max", 65535, KindOutOfRange},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := patchLayerBlendMode(t, 2, tt.mode)
			spr := decodeBytes(t, raw, DecodeOptions{
				Registry: DefaultRegistry(), Fallback: Normal,
			})
			if len(spr.Diagnostics) != 1 {
				t.Fatalf("expected 1 diagnostic, got %+v", spr.Diagnostics)
			}
			d := spr.Diagnostics[0]
			if d.Kind != tt.kind || d.Mode != Mode(tt.mode) || d.Layer != 2 || d.Frame != 0 {
				t.Fatalf("unexpected diagnostic: %+v", d)
			}
		})
	}
}

func TestDecodeStrictFailsWithLayerAndFrame(t *testing.T) {
	raw := patchLayerBlendMode(t, 2, 256)
	_, err := DecodeWithOptions(bytes.NewReader(raw), DecodeOptions{
		Registry: DefaultRegistry(), Strict: true, Fallback: Normal,
	})
	var umerr *UnsupportedModeError
	if err == nil {
		t.Fatal("expected UnsupportedModeError")
	}
	if ok := asUnsupportedModeError(err, &umerr); !ok {
		t.Fatalf("expected *UnsupportedModeError, got %T: %v", err, err)
	}
	if umerr.Layer != 2 || umerr.Frame != 0 || umerr.Mode != Mode(256) || umerr.Kind != KindOutOfRange {
		t.Fatalf("unexpected error diagnostic: %+v", umerr.Diagnostic)
	}
}

func TestDecodeUnknownKindWithCustomRegistry(t *testing.T) {
	// A registry that lacks a built-in mode reports it as unknown.
	modes := make([]ModeInfo, 0, len(DefaultRegistry().All()))
	for _, info := range DefaultRegistry().All() {
		if info.Mode != Lighten {
			modes = append(modes, info)
		}
	}
	custom, err := NewRegistry(modes...)
	if err != nil {
		t.Fatal(err)
	}

	raw := patchLayerBlendMode(t, 2, uint16(Lighten))
	spr := decodeBytes(t, raw, DecodeOptions{
		Registry: custom, Fallback: Normal,
	})
	if len(spr.Diagnostics) != 1 {
		t.Fatalf("expected 1 diagnostic, got %+v", spr.Diagnostics)
	}
	if spr.Diagnostics[0].Kind != KindUnknown {
		t.Fatalf("expected KindUnknown, got %v", spr.Diagnostics[0].Kind)
	}
}

func TestDecodeCustomModeFunc(t *testing.T) {
	const customMode Mode = 100
	custom, err := NewRegistry(append(DefaultRegistry().All(), ModeInfo{
		Mode: customMode, Name: "Fixture Red", Func: customFunc(),
	})...)
	if err != nil {
		t.Fatal(err)
	}

	raw := patchLayerBlendMode(t, 2, uint16(customMode))
	spr := decodeBytes(t, raw, DecodeOptions{
		Registry: custom, Strict: true, Fallback: Normal,
	})
	if len(spr.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", spr.Diagnostics)
	}

	b := spr.Bounds()
	var red int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := spr.At(x, y).RGBA()
			if r>>8 == 17 && g>>8 == 209 && bl>>8 == 31 {
				red++
			}
		}
	}
	if red == 0 {
		t.Fatal("custom blend function never applied")
	}
}

func TestDecodeConcurrent(t *testing.T) {
	raw := patchLayerBlendMode(t, 2, 19)
	opts := DecodeOptions{Registry: DefaultRegistry(), Fallback: Normal}

	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			spr, err := DecodeWithOptions(bytes.NewReader(append([]byte(nil), raw...)), opts)
			if err != nil {
				errs <- err
				return
			}
			if len(spr.Diagnostics) != 1 {
				errs <- errDiagnosticsMismatch
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

var errDiagnosticsMismatch = diagErr("diagnostics mismatch")

type diagErr string

func (e diagErr) Error() string { return string(e) }

func mustReadFile(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func asUnsupportedModeError(err error, target **UnsupportedModeError) bool {
	e, ok := err.(*UnsupportedModeError)
	if ok {
		*target = e
	}
	return ok
}

func assertImagesEqual(t *testing.T, want, got image.Image) {
	t.Helper()
	wb, gb := want.Bounds(), got.Bounds()
	if wb != gb {
		t.Fatalf("bounds differ: %v != %v", wb, gb)
	}
	for y := wb.Min.Y; y < wb.Max.Y; y++ {
		for x := wb.Min.X; x < wb.Max.X; x++ {
			if want.At(x, y) != got.At(x, y) {
				t.Fatalf("pixel at (%d,%d) differs: %v != %v", x, y, want.At(x, y), got.At(x, y))
			}
		}
	}
}
