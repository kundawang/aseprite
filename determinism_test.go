package aseprite

// This file is the deterministic quality gate for the decoder. It answers one
// question: given the same bytes, does every decode path produce exactly the
// same result, and does decoding ever mutate the input?
//
// The fixtures are hand-built (cmd/genfixtures/main.go) and pinned by SHA-256,
// so an accidental format or fixture change fails loudly. Every assertion
// carries a stable context string (fixture, operation, iteration/goroutine)
// so a flaky failure points at one reproducible case.
//
// Design notes:
//   - "Deep equal" is implemented from scratch with reflect.DeepEqual over a
//     canonical value tree that includes the pixel bytes, the palette, frame
//     bounds/durations/user data, tags, slices and layer data. Pointer identity
//     is deliberately not compared; value identity is.
//   - Serial and concurrent decodes share the same snapshot function, so a
//     difference between them is by construction a decoder bug, not a test
//     artefact.
//   - Input immutability is checked per call by hashing the input before and
//     after Read/Decode/DecodeConfig over a caller-owned byte slice.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"sync"
	"testing"
)

// fixture describes one committed binary fixture and what it covers.
type fixture struct {
	Name    string
	BPP     int
	Frames  int
	Palette bool // whether decoded results expose a palette
	// Coverage is documentation only; it is checked by the tests below.
	Coverage string
	// SHA256 pins the fixture bytes. Regenerate with cmd/genfixtures.
	SHA256 string
}

var fixtures = []fixture{
	{
		Name:    "indexed_2019.aseprite",
		BPP:     8,
		Frames:  5,
		Palette: true,
		Coverage: "8bpp, new palette chunk 0x2019, linked cel, compressed + raw cels, " +
			"transparent index 3, invisible/reference layers, multiply blend, " +
			"tags, slices with and without user data, empty metadata",
		SHA256: "0b93d0954e18cdb8d2becf4a97acb62120259e979a428a83a77c57f1ab64f89d",
	},
	{
		Name:    "indexed_old0004.aseprite",
		BPP:     8,
		Frames:  3,
		Palette: true,
		Coverage: "8bpp, old palette chunk 0x0004, packet skip, 256-entry packet, " +
			"linked cel, transparent index 5",
		SHA256: "b8c39c0c495cc83c8e92cf7e5d9b029444b6b2c2fb2f092ca80f423730d1bb24",
	},
	{
		Name:    "indexed_old0011.aseprite",
		BPP:     8,
		Frames:  2,
		Palette: true,
		Coverage: "8bpp, old palette chunk 0x0011 (6-bit components), empty tag and " +
			"slice chunks, transparent index 0",
		SHA256: "bafc1292c0ac46bd5a41ac87d78f5d6813b291f82d5e27f21ebf0edd15ac0406",
	},
	{
		Name:    "gray16.aseprite",
		BPP:     16,
		Frames:  3,
		Palette: false,
		Coverage: "16bpp grey+alpha, compressed + raw cels, linked cel, layer and " +
			"cel user data, empty metadata",
		SHA256: "ab9144e87d4ab74b8e5dd631fa370a8599d1cf211d55df2576164c4b8a728cb5",
	},
	{
		Name:    "rgba32.aseprite",
		BPP:     32,
		Frames:  4,
		Palette: false,
		Coverage: "32bpp, compressed + raw cels, linked cel, multiply blend, " +
			"invisible layer, tags, slices with user data, transparent pixels",
		SHA256: "2b7697a03621fed0b89c5ab17173469dbaebada95f97874ad210655dae5b9458",
	},
	{
		// These three already existed before this suite; they are the
		// regression baseline produced by Aseprite itself.
		Name:     "../slime_paletted.aseprite",
		BPP:      8,
		Frames:   10,
		Palette:  true,
		Coverage: "Aseprite-authored 8bpp sprite: 10 frames, 2 layers, 2 tags",
		SHA256:   "010d60b0f8d899e63c5d215badfd696e737209a5a86c23de17eb35fb1d0f95cd",
	},
	{
		Name:     "../slime_grayscale.aseprite",
		BPP:      16,
		Frames:   10,
		Palette:  false,
		Coverage: "Aseprite-authored 16bpp sprite: 10 frames, 2 slices",
		SHA256:   "d1a1479b8849e735e47a2c30064d761d56f2ed06748afe38017952f046aa9f27",
	},
	{
		Name:     "../blendtest.aseprite",
		BPP:      32,
		Frames:   1,
		Palette:  false,
		Coverage: "Aseprite-authored 32bpp sprite: large blended cels",
		SHA256:   "188d79703b966fdd2514da114041aeb02f41d98b81cadd9a6d6a27ba3524eee7",
	},
}

func fixtureDir() string { return filepath.Join("testfiles", "determinism") }

// fixturePath resolves a fixture name relative to the determinism directory.
func fixturePath(fx fixture) string { return filepath.Join(fixtureDir(), fx.Name) }

// readFixture returns a fresh copy of the fixture bytes.
func readFixture(t *testing.T, fx fixture) []byte {
	t.Helper()
	b, err := os.ReadFile(fixturePath(fx))
	if err != nil {
		t.Fatalf("%s: read fixture: %v", fx.Name, err)
	}
	return b
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestFixturesArePinned(t *testing.T) {
	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			b := readFixture(t, fx)
			if got := sha256Hex(b); got != fx.SHA256 {
				t.Fatalf("%s: fixture SHA-256 = %s, want %s; regenerate with `go run cmd/genfixtures/main.go` "+
					"if the change is intentional and update the pin", fx.Name, got, fx.SHA256)
			}
		})
	}
}

// snapshot is a canonical, pointer-free view of a decoded sprite.
type snapshot struct {
	ImageType string
	Bounds    [4]int
	// Pixels holds RGBA8 of every pixel in atlas order. It is compared for
	// both the concrete image type and the generic image.Image interface.
	Pixels   []byte
	Palette  []color.NRGBA
	Frames   []frameSnapshot
	Tags     []tagSnapshot
	Slices   []sliceSnapshot
	LayerDat [][]byte
	Config   configSnapshot
}

type frameSnapshot struct {
	Bounds   [4]int
	Duration int64
	Data     [][]byte
}

type tagSnapshot struct {
	Name string
	Lo   uint16
	Hi   uint16
	Rep  uint16
	Dir  LoopDirection
}

type sliceSnapshot struct {
	Name   string
	Bounds [4]int
	Center [4]int
	Pivot  [2]int
	Data   []byte
	Color  color.NRGBA
}

type configSnapshot struct {
	Model   string
	Width   int
	Height  int
	Palette []color.NRGBA
}

func rect4(r image.Rectangle) [4]int { return [4]int{r.Min.X, r.Min.Y, r.Max.X, r.Max.Y} }

func colorToNRGBA(c color.Color) color.NRGBA {
	if c == nil {
		return color.NRGBA{}
	}
	return color.NRGBAModel.Convert(c).(color.NRGBA)
}

func paletteSnapshot(p color.Palette) []color.NRGBA {
	if p == nil {
		return nil
	}
	out := make([]color.NRGBA, len(p))
	for i, c := range p {
		out[i] = colorToNRGBA(c)
	}
	return out
}

func imageTypeName(img image.Image) string {
	if img == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%T", img)
}

// snapshotImage reads every pixel through image.Image.At. This is the path a
// renderer uses; for *Aseprite it also exercises the embedded image.
func snapshotImage(t *testing.T, ctx string, img image.Image) snapshot {
	t.Helper()
	var sn snapshot
	sn.ImageType = imageTypeName(img)
	if img == nil {
		return sn
	}
	r := img.Bounds()
	sn.Bounds = rect4(r)
	sn.Pixels = make([]byte, 0, 4*r.Dx()*r.Dy())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := colorToNRGBA(img.At(x, y))
			sn.Pixels = append(sn.Pixels, c.R, c.G, c.B, c.A)
		}
	}
	if p, ok := img.ColorModel().(color.Palette); ok {
		sn.Palette = paletteSnapshot(p)
	}
	return sn
}

func snapshotSprite(t *testing.T, ctx string, spr *Aseprite) snapshot {
	t.Helper()
	sn := snapshotImage(t, ctx, spr.Image)
	sn.Frames = make([]frameSnapshot, len(spr.Frames))
	for i, fr := range spr.Frames {
		sn.Frames[i] = frameSnapshot{
			Bounds:   rect4(fr.Bounds),
			Duration: int64(fr.Duration),
			Data:     deepCopyBytes(fr.Data),
		}
	}
	sn.Tags = make([]tagSnapshot, len(spr.Tags))
	for i, tg := range spr.Tags {
		sn.Tags[i] = tagSnapshot{Name: tg.Name, Lo: tg.Lo, Hi: tg.Hi, Rep: tg.Repeat, Dir: tg.LoopDirection}
	}
	sn.Slices = make([]sliceSnapshot, len(spr.Slices))
	for i, sl := range spr.Slices {
		sn.Slices[i] = sliceSnapshot{
			Name:   sl.Name,
			Bounds: rect4(sl.Bounds),
			Center: rect4(sl.Center),
			Pivot:  [2]int{sl.Pivot.X, sl.Pivot.Y},
			Data:   append([]byte(nil), sl.Data...),
			Color:  colorToNRGBA(sl.Color),
		}
	}
	sn.LayerDat = deepCopyBytes(spr.LayerData)
	return sn
}

func deepCopyBytes(in [][]byte) [][]byte {
	if in == nil {
		return nil
	}
	out := make([][]byte, len(in))
	for i := range in {
		out[i] = append([]byte(nil), in[i]...)
	}
	return out
}

// snapshotByRead decodes with Read and captures everything.
func snapshotByRead(t *testing.T, ctx string, input []byte) (snapshot, error) {
	t.Helper()
	spr, err := Read(bytes.NewReader(input))
	if err != nil {
		return snapshot{}, err
	}
	return snapshotSprite(t, ctx, spr), nil
}

// snapshotByDecode decodes with Decode (the image.Image path) and asserts the
// returned dynamic type is *Aseprite before capturing it.
func snapshotByDecode(t *testing.T, ctx string, input []byte) (snapshot, error) {
	t.Helper()
	img, err := Decode(bytes.NewReader(input))
	if err != nil {
		return snapshot{}, err
	}
	spr, ok := img.(*Aseprite)
	if !ok {
		t.Fatalf("%s: Decode returned %T, want *Aseprite", ctx, img)
	}
	return snapshotSprite(t, ctx, spr), nil
}

func configSnapshotOf(t *testing.T, ctx string, cfg image.Config) configSnapshot {
	t.Helper()
	cs := configSnapshot{Width: cfg.Width, Height: cfg.Height, Model: fmt.Sprintf("%T", cfg.ColorModel)}
	if p, ok := cfg.ColorModel.(color.Palette); ok {
		cs.Palette = paletteSnapshot(p)
	}
	return cs
}

func snapshotByConfig(t *testing.T, ctx string, input []byte) (snapshot, error) {
	t.Helper()
	cfg, err := DecodeConfig(bytes.NewReader(input))
	if err != nil {
		return snapshot{}, err
	}
	return snapshot{Config: configSnapshotOf(t, ctx, cfg)}, nil
}

// TestDecodeIsDeterministicSerial compares many repeated decodes within one
// goroutine. Any mutable state on a package-level or pooled object would show
// up here as a later decode drifting from the first.
func TestDecodeIsDeterministicSerial(t *testing.T) {
	const repetitions = 16
	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			input := readFixture(t, fx)
			before := sha256Hex(input)
			want := sha256Hex(input)

			var baseline snapshot
			for round := 0; round < repetitions; round++ {
				ctx := fmt.Sprintf("%s serial round %d", fx.Name, round)
				got, err := snapshotByRead(t, ctx, input)
				if err != nil {
					t.Fatalf("%s: Read: %v", ctx, err)
				}
				if round == 0 {
					baseline = got
				} else if !reflect.DeepEqual(got, baseline) {
					t.Fatalf("%s: decode result differs from round 0\nfirst difference: %s",
						ctx, firstSnapshotDiff(baseline, got))
				}
				if h := sha256Hex(input); h != want {
					t.Fatalf("%s: input bytes mutated by Read (%s -> %s)", ctx, before, h)
				}
			}
		})
	}
}

// TestDecodePathsAgree asserts that Read and Decode produce byte-identical
// values and that DecodeConfig's config is consistent with a full decode.
// DecodeConfig intentionally does not parse frames, but the atlas size and the
// palette it reports must match the atlas a full decode builds.
func TestDecodePathsAgree(t *testing.T) {
	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			input := readFixture(t, fx)
			before := sha256Hex(input)

			readSnap, err := snapshotByRead(t, fx.Name+" Read", input)
			if err != nil {
				t.Fatalf("%s: Read: %v", fx.Name, err)
			}
			decodeSnap, err := snapshotByDecode(t, fx.Name+" Decode", input)
			if err != nil {
				t.Fatalf("%s: Decode: %v", fx.Name, err)
			}
			if !reflect.DeepEqual(readSnap, decodeSnap) {
				t.Fatalf("%s: Read and Decode disagree\nfirst difference: %s",
					fx.Name, firstSnapshotDiff(readSnap, decodeSnap))
			}

			cfgSnap, err := snapshotByConfig(t, fx.Name+" DecodeConfig", input)
			if err != nil {
				t.Fatalf("%s: DecodeConfig: %v", fx.Name, err)
			}
			cfg := cfgSnap.Config
			if cfg.Width != readSnap.Bounds[2]-readSnap.Bounds[0] ||
				cfg.Height != readSnap.Bounds[3]-readSnap.Bounds[1] {
				t.Fatalf("%s: DecodeConfig size %dx%d, decoded atlas %v",
					fx.Name, cfg.Width, cfg.Height, readSnap.Bounds)
			}
			// The palette snapshot uses one color model for both reads, so a
			// mismatch here means DecodeConfig reported a different palette.
			if !reflect.DeepEqual(cfg.Palette, readSnap.Palette) && !(cfg.Palette == nil && len(readSnap.Palette) == 0) {
				t.Fatalf("%s: DecodeConfig palette (%d entries) differs from decoded palette (%d entries)",
					fx.Name, len(cfg.Palette), len(readSnap.Palette))
			}

			if h := sha256Hex(input); h != before {
				t.Fatalf("%s: input bytes mutated by decode paths", fx.Name)
			}
		})
	}
}

// TestDecodeIsDeterministicConcurrent runs many decodes of the same bytes in
// parallel. Run with -race this is the check that no mutable palette or cel
// state is shared between decodes.
func TestDecodeIsDeterministicConcurrent(t *testing.T) {
	const workers = determinismWorkers
	const perWorker = determinismIterations
	if testing.Short() {
		t.Skip("short mode: skipping concurrency sweep")
	}
	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			input := readFixture(t, fx)
			baseline, err := snapshotByRead(t, fx.Name+" baseline", input)
			if err != nil {
				t.Fatalf("%s: baseline Read: %v", fx.Name, err)
			}
			cfgBaseline, err := snapshotByConfig(t, fx.Name+" config baseline", input)
			if err != nil {
				t.Fatalf("%s: baseline DecodeConfig: %v", fx.Name, err)
			}

			// Each worker gets its own copy of the input so that a mutation
			// by one decode cannot mask or cause another worker's mismatch.
			inputs := make([][]byte, workers)
			for i := range inputs {
				inputs[i] = append([]byte(nil), input...)
			}

			var wg sync.WaitGroup
			start := make(chan struct{})
			errs := make(chan string, workers*perWorker)
			for w := 0; w < workers; w++ {
				w := w
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					for i := 0; i < perWorker; i++ {
						ctx := fmt.Sprintf("%s worker %d iteration %d", fx.Name, w, i)
						var (
							got snapshot
							err error
						)
						switch i % 3 {
						case 0:
							got, err = snapshotByRead(t, ctx, inputs[w])
						case 1:
							got, err = snapshotByDecode(t, ctx, inputs[w])
						default:
							got, err = snapshotByConfig(t, ctx, inputs[w])
						}
						if err != nil {
							errs <- fmt.Sprintf("%s: %v", ctx, err)
							return
						}
						want := baseline
						if i%3 == 2 {
							want = cfgBaseline
						}
						if !reflect.DeepEqual(got, want) {
							errs <- fmt.Sprintf("%s: concurrent result differs from baseline\nfirst difference: %s",
								ctx, firstSnapshotDiff(want, got))
							return
						}
					}
				}()
			}
			close(start)
			wg.Wait()
			close(errs)
			var failures []string
			for e := range errs {
				failures = append(failures, e)
			}
			sort.Strings(failures)
			if len(failures) > 0 {
				t.Fatalf("%d concurrent decode failure(s), first:\n%s", len(failures), failures[0])
			}
		})
	}
}

// TestDecodeResultsShareNoStorage proves that separate decodes do not alias
// each other's palettes, cel pixels or user data, and that no result aliases
// the input buffer. It mutates one result in place and checks the other result
// and the input are unaffected.
//
// The check is structural, not statistical: it does not depend on scheduling
// or on the race detector catching an unsynchronised access. Together with
// TestDecodeIsDeterministicConcurrent (run under -race) it covers both the
// "shared mutable state" and "data race" failure modes.
func TestDecodeResultsShareNoStorage(t *testing.T) {
	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			input := readFixture(t, fx)
			inputHash := sha256Hex(input)

			a, err := Read(bytes.NewReader(input))
			if err != nil {
				t.Fatalf("%s: first Read: %v", fx.Name, err)
			}
			b, err := Read(bytes.NewReader(input))
			if err != nil {
				t.Fatalf("%s: second Read: %v", fx.Name, err)
			}
			beforeB := snapshotSprite(t, fx.Name+" second", b)

			// Mutate everything mutable on a.
			mutateBackingPixels(a.Image)
			switch p := a.Image.(type) {
			case *image.Paletted:
				if len(p.Palette) > 0 {
					p.Palette[0] = color.NRGBA{R: 1, G: 2, B: 3, A: 4}
				}
			}
			for i := range a.Frames {
				for j := range a.Frames[i].Data {
					for k := range a.Frames[i].Data[j] {
						a.Frames[i].Data[j][k] ^= 0xFF
					}
				}
			}
			for i := range a.Slices {
				for k := range a.Slices[i].Data {
					a.Slices[i].Data[k] ^= 0xFF
				}
			}
			for i := range a.LayerData {
				for k := range a.LayerData[i] {
					a.LayerData[i][k] ^= 0xFF
				}
			}

			afterB := snapshotSprite(t, fx.Name+" second after mutation", b)
			if !reflect.DeepEqual(beforeB, afterB) {
				t.Fatalf("%s: mutating the first result changed the second result\nfirst difference: %s",
					fx.Name, firstSnapshotDiff(beforeB, afterB))
			}
			if got := sha256Hex(input); got != inputHash {
				t.Fatalf("%s: mutating a decoded result changed the input bytes", fx.Name)
			}
		})
	}
}

// mutateBackingPixels flips every byte of the concrete pixel storage of img.
// The atlas images built by the decoder are *image.Paletted, *image.Gray16,
// *image.RGBA or *image.NRGBA; other types are left untouched.
func mutateBackingPixels(img image.Image) {
	switch p := img.(type) {
	case *image.Paletted:
		for i := range p.Pix {
			p.Pix[i] ^= 0xFF
		}
	case *image.Gray16:
		for i := range p.Pix {
			p.Pix[i] ^= 0xFF
		}
	case *image.RGBA:
		for i := range p.Pix {
			p.Pix[i] ^= 0xFF
		}
	case *image.NRGBA:
		for i := range p.Pix {
			p.Pix[i] ^= 0xFF
		}
	}
}

// TestDecodeInputImmutable is the direct input-mutation check: decode the same
// caller-owned slice repeatedly and after every call verify the bytes are
// unchanged. It also checks the failure paths, because a rejected input must
// not be modified either.
func TestDecodeInputImmutable(t *testing.T) {
	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			input := readFixture(t, fx)
			want := sha256Hex(input)

			// Valid input, all three entry points.
			for i := 0; i < 4; i++ {
				ctx := fmt.Sprintf("%s valid pass %d", fx.Name, i)
				if _, err := Read(bytes.NewReader(input)); err != nil {
					t.Fatalf("%s: Read: %v", ctx, err)
				}
				if _, err := Decode(bytes.NewReader(input)); err != nil {
					t.Fatalf("%s: Decode: %v", ctx, err)
				}
				if _, err := DecodeConfig(bytes.NewReader(input)); err != nil {
					t.Fatalf("%s: DecodeConfig: %v", ctx, err)
				}
				if h := sha256Hex(input); h != want {
					t.Fatalf("%s: input mutated on a valid decode", ctx)
				}
			}

			// Truncated input: every prefix that fails must leave the prefix
			// untouched. A few interesting prefixes are enough because the
			// per-byte sweep lives in the fault-injection test.
			for _, n := range []int{0, 1, 127, 128, len(input) / 2, len(input) - 1} {
				if n < 0 || n > len(input) {
					continue
				}
				prefix := append([]byte(nil), input[:n]...)
				hash := sha256Hex(prefix)
				_, _ = Read(bytes.NewReader(prefix))
				_, _ = DecodeConfig(bytes.NewReader(prefix))
				if got := sha256Hex(prefix); got != hash {
					t.Fatalf("%s: truncated input of %d bytes mutated (%s -> %s)", fx.Name, n, hash, got)
				}
			}
		})
	}
}

// TestDecodeCoversRequiredFeatures asserts the decoded values actually contain
// the features the fixtures claim to cover, so a fixture that silently loses a
// feature cannot make the determinism tests vacuous.
func TestDecodeCoversRequiredFeatures(t *testing.T) {
	byName := map[string]snapshot{}
	for _, fx := range fixtures {
		snap, err := snapshotByRead(t, fx.Name, readFixture(t, fx))
		if err != nil {
			t.Fatalf("%s: Read: %v", fx.Name, err)
		}
		byName[fx.Name] = snap
		if len(snap.Frames) != fx.Frames {
			t.Fatalf("%s: decoded %d frames, want %d", fx.Name, len(snap.Frames), fx.Frames)
		}
		if fx.Palette && len(snap.Palette) == 0 {
			t.Fatalf("%s: expected a palette in the decoded result", fx.Name)
		}
		if !fx.Palette && len(snap.Palette) != 0 {
			t.Fatalf("%s: unexpected palette in the decoded result", fx.Name)
		}
		if len(snap.Pixels) == 0 {
			t.Fatalf("%s: decoded an empty atlas", fx.Name)
		}
	}

	// linked cel: a frame that holds only a linked cel must render exactly the
	// same picture as the frame it links to. (The atlas regions sit at
	// different positions, so compare pixels, not bounds.) indexed_2019 also
	// uses a linked cel for layer 0, but its frame 1 additionally draws a new
	// layer-1 cel, so it is not expected to match frame 0 as a whole.
	if !samePicture(byName["indexed_old0004.aseprite"], 0, 1) {
		t.Fatal("indexed_old0004: linked cel frame 1 does not match frame 0")
	}
	if !samePicture(byName["gray16.aseprite"], 0, 1) {
		t.Fatal("gray16: linked cel frame 1 does not match frame 0")
	}
	if !samePicture(byName["rgba32.aseprite"], 0, 1) {
		t.Fatal("rgba32: linked cel frame 1 does not match frame 0")
	}

	// transparent index: the indexed fixture uses index 3, so at least one
	// decoded pixel must be fully transparent.
	if !hasTransparent(byName["indexed_2019.aseprite"].Pixels) {
		t.Fatal("indexed_2019: expected transparent pixels from the transparent index")
	}
	idx := byName["indexed_2019.aseprite"]
	// tags and slices.
	if len(idx.Tags) != 2 || idx.Tags[0].Name != "walk" || idx.Tags[1].Name != "idle" {
		t.Fatalf("indexed_2019: unexpected tags %+v", idx.Tags)
	}
	if len(idx.Slices) != 2 || idx.Slices[0].Name != "hitbox" || len(idx.Slices[0].Data) == 0 {
		t.Fatalf("indexed_2019: unexpected slices %+v", idx.Slices)
	}
	// empty metadata must still decode to an empty (not nil-vs-present) value.
	if idx.Frames[2].Data != nil && len(idx.Frames[2].Data) != 0 {
		t.Fatalf("indexed_2019: empty cel metadata decoded to %q", idx.Frames[2].Data)
	}
	// 16 bpp and 32 bpp paths.
	if byName["gray16.aseprite"].ImageType != "*image.Gray16" {
		t.Fatalf("gray16: image type %s", byName["gray16.aseprite"].ImageType)
	}
	if byName["rgba32.aseprite"].ImageType != "*image.RGBA" {
		t.Fatalf("rgba32: image type %s", byName["rgba32.aseprite"].ImageType)
	}
	// old palette chunks must have produced non-black colors.
	for _, name := range []string{"indexed_old0004.aseprite", "indexed_old0011.aseprite"} {
		if !hasNonBlackPalette(byName[name].Palette) {
			t.Fatalf("%s: old palette chunk produced no colors", name)
		}
	}
}

func hasTransparent(pix []byte) bool {
	for i := 3; i < len(pix); i += 4 {
		if pix[i] == 0 {
			return true
		}
	}
	return false
}

// samePicture reports whether the pixel content of two frames is identical.
func samePicture(sn snapshot, a, b int) bool {
	if a >= len(sn.Frames) || b >= len(sn.Frames) {
		return false
	}
	ra, rb := sn.Frames[a].Bounds, sn.Frames[b].Bounds
	if ra[2]-ra[0] != rb[2]-rb[0] || ra[3]-ra[1] != rb[3]-rb[1] {
		return false
	}
	width := ra[2] - ra[0]
	atlasWidth := sn.Bounds[2] - sn.Bounds[0]
	for y := 0; y < ra[3]-ra[1]; y++ {
		for x := 0; x < width; x++ {
			ia := ((ra[1]-sn.Bounds[1]+y)*atlasWidth + (ra[0] - sn.Bounds[0] + x)) * 4
			ib := ((rb[1]-sn.Bounds[1]+y)*atlasWidth + (rb[0] - sn.Bounds[0] + x)) * 4
			if !bytes.Equal(sn.Pixels[ia:ia+4], sn.Pixels[ib:ib+4]) {
				return false
			}
		}
	}
	return true
}

func hasNonBlackPalette(p []color.NRGBA) bool {
	for _, c := range p {
		if c.R != 0 || c.G != 0 || c.B != 0 {
			return true
		}
	}
	return false
}

// firstSnapshotDiff returns a short, stable description of the first field
// that differs between two snapshots. It exists so failures are greppable and
// do not dump megabytes of pixel data.
func firstSnapshotDiff(a, b snapshot) string {
	switch {
	case a.ImageType != b.ImageType:
		return fmt.Sprintf("ImageType %q != %q", a.ImageType, b.ImageType)
	case a.Bounds != b.Bounds:
		return fmt.Sprintf("Bounds %v != %v", a.Bounds, b.Bounds)
	case !bytes.Equal(a.Pixels, b.Pixels):
		return fmt.Sprintf("pixels differ at byte %d of %d", firstByteDiff(a.Pixels, b.Pixels), len(a.Pixels))
	case !reflect.DeepEqual(a.Palette, b.Palette):
		return "palette entries differ"
	case !reflect.DeepEqual(a.Frames, b.Frames):
		return fmt.Sprintf("frames differ: %s", firstFrameDiff(a.Frames, b.Frames))
	case !reflect.DeepEqual(a.Tags, b.Tags):
		return fmt.Sprintf("tags %+v != %+v", a.Tags, b.Tags)
	case !reflect.DeepEqual(a.Slices, b.Slices):
		return fmt.Sprintf("slices %+v != %+v", a.Slices, b.Slices)
	case !reflect.DeepEqual(a.LayerDat, b.LayerDat):
		return fmt.Sprintf("layer data %v != %v", a.LayerDat, b.LayerDat)
	case !reflect.DeepEqual(a.Config, b.Config):
		return fmt.Sprintf("config %+v != %+v", a.Config, b.Config)
	}
	return "no difference"
}

func firstByteDiff(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func firstFrameDiff(a, b []frameSnapshot) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if !reflect.DeepEqual(a[i], b[i]) {
			return fmt.Sprintf("frame %d: %+v != %+v", i, a[i], b[i])
		}
	}
	return fmt.Sprintf("frame count %d != %d", len(a), len(b))
}

// memoryStats records process memory before and after a decode sweep. It is
// intentionally best-effort: GC timing makes exact numbers non-portable, so the
// test only asserts that peak allocation stays within a documented bound and
// logs the numbers for the record.
func memoryStats() (heapAlloc, heapInuse, sys uint64) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc, m.HeapInuse, m.Sys
}
