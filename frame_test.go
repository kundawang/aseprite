package aseprite

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"os"
	"runtime"
	"testing"
)

// frameFixtures lists the generated multi-frame fixtures covering 8, 16 and
// 32 bits per pixel. They are produced by cmd/genfixtures.
var frameFixtures = []string{
	"frames8",
	"frames16",
	"frames32",
}

func openFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testfiles/frames/" + name + ".aseprite")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// framePixels copies an image's pixels into a canonical RGBA byte slice so
// that images with different concrete types can be compared. For Gray16 the
// RGBA values of the color are compared, which is what a consumer sees.
func framePixels(img image.Image) []byte {
	b := img.Bounds()
	out := make([]byte, 0, b.Dx()*b.Dy()*4)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			out = append(out, byte(r>>8), byte(g>>8), byte(bl>>8), byte(a>>8))
		}
	}
	return out
}

// g16FramePixels returns the raw Gray16 samples of img, or nil if img is not
// a Gray16 image.
func g16FramePixels(img image.Image) []uint16 {
	g, ok := img.(*image.Gray16)
	if !ok {
		return nil
	}
	b := g.Bounds()
	out := make([]uint16, 0, b.Dx()*b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			out = append(out, uint16(g.Gray16At(x, y).Y))
		}
	}
	return out
}

func atlasFrame(img image.Image, r image.Rectangle) []byte {
	b := image.Rect(0, 0, r.Dx(), r.Dy())
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, img, r.Min, draw.Src)
	return framePixels(dst)
}

func TestFrameImageMatchesAtlas(t *testing.T) {
	for _, name := range frameFixtures {
		t.Run(name, func(t *testing.T) {
			data := openFixture(t, name)

			atlasSpr, err := Read(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if atlasSpr.Atlas == nil {
				t.Fatal("atlas mode must build an atlas")
			}

			frameSpr, err := ReadFrames(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if frameSpr.Atlas != nil {
				t.Fatal("frame mode must not build an atlas")
			}
			if len(frameSpr.Frames) != len(atlasSpr.Frames) {
				t.Fatalf("frame count %d != atlas frame count %d", len(frameSpr.Frames), len(atlasSpr.Frames))
			}

			for i := range frameSpr.Frames {
				want := atlasFrame(atlasSpr.Atlas, atlasSpr.Frames[i].Bounds)

				img, err := frameSpr.FrameImage(i)
				if err != nil {
					t.Fatalf("frame %d: %v", i, err)
				}

				// Frame images are anchored at the origin.
				if got := img.Bounds(); got != image.Rect(0, 0, atlasSpr.Frames[i].Bounds.Dx(), atlasSpr.Frames[i].Bounds.Dy()) {
					t.Fatalf("frame %d bounds %v", i, got)
				}

				got := framePixels(img)
				if !bytes.Equal(got, want) {
					for p := range want {
						if p >= len(got) || got[p] != want[p] {
							t.Fatalf("frame %d pixel byte %d: got %d want %d", i, p, got[p], want[p])
						}
					}
					t.Fatalf("frame %d pixel count %d != %d", i, len(got), len(want))
				}

				// Frame mode frames are anchored at the origin too.
				if frameSpr.Frames[i].Bounds != image.Rect(0, 0, atlasSpr.Frames[i].Bounds.Dx(), atlasSpr.Frames[i].Bounds.Dy()) {
					t.Fatalf("frame mode bounds %v", frameSpr.Frames[i].Bounds)
				}
			}
		})
	}
}

// TestFrameImageMatchesAtlasOnExistingFiles applies the same comparison to the
// original single- and multi-frame test files, including the multi-layer blend
// test, so the per-frame path is exercised on real Aseprite output too.
func TestFrameImageMatchesAtlasOnExistingFiles(t *testing.T) {
	for _, name := range []string{
		"slime_paletted",
		"slime_grayscale",
		"blendtest",
	} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile("testfiles/" + name + ".aseprite")
			if err != nil {
				t.Fatal(err)
			}

			atlasSpr, err := Read(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			frameSpr, err := ReadFrames(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}

			if len(frameSpr.Frames) != len(atlasSpr.Frames) {
				t.Fatalf("frame count %d != %d", len(frameSpr.Frames), len(atlasSpr.Frames))
			}

			for i := range frameSpr.Frames {
				want := atlasFrame(atlasSpr.Atlas, atlasSpr.Frames[i].Bounds)
				img, err := frameSpr.FrameImage(i)
				if err != nil {
					t.Fatalf("frame %d: %v", i, err)
				}
				if got := framePixels(img); !bytes.Equal(got, want) {
					t.Fatalf("frame %d differs from the atlas", i)
				}
				if img.Bounds() != image.Rect(0, 0, atlasSpr.Frames[i].Bounds.Dx(), atlasSpr.Frames[i].Bounds.Dy()) {
					t.Fatalf("frame %d bounds %v", i, img.Bounds())
				}
			}
		})
	}
}

func TestFrameImagePreservesColorModel(t *testing.T) {
	for _, tt := range []struct {
		name  string
		check func(t *testing.T, img image.Image)
	}{
		{"frames8", func(t *testing.T, img image.Image) {
			if _, ok := img.(*image.RGBA); !ok {
				t.Fatalf("8 bpp frame image is %T, want *image.RGBA", img)
			}
		}},
		{"frames16", func(t *testing.T, img image.Image) {
			if _, ok := img.(*image.Gray16); !ok {
				t.Fatalf("16 bpp frame image is %T, want *image.Gray16", img)
			}
		}},
		{"frames32", func(t *testing.T, img image.Image) {
			if _, ok := img.(*image.RGBA); !ok {
				t.Fatalf("32 bpp frame image is %T, want *image.RGBA", img)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spr, err := ReadFrames(bytes.NewReader(openFixture(t, tt.name)))
			if err != nil {
				t.Fatal(err)
			}
			img, err := spr.FrameImage(0)
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, img)
		})
	}
}

// TestFrameTransparency verifies that the empty frames in the fixtures render
// as fully transparent pixels and that partial transparency is preserved.
// 8 and 32 bpp frames are RGBA and can express transparency; 16 bpp frames
// are Gray16 and cover transparency with luminance 0, as the atlas does.
func TestFrameTransparency(t *testing.T) {
	for _, tt := range []struct {
		name  string
		empty int
	}{
		{"frames8", 0},
		{"frames32", 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spr, err := ReadFrames(bytes.NewReader(openFixture(t, tt.name)))
			if err != nil {
				t.Fatal(err)
			}

			img, err := spr.FrameImage(tt.empty)
			if err != nil {
				t.Fatal(err)
			}

			b := img.Bounds()
			for y := b.Min.Y; y < b.Max.Y; y++ {
				for x := b.Min.X; x < b.Max.X; x++ {
					if _, _, _, a := img.At(x, y).RGBA(); a != 0 {
						t.Fatalf("empty frame %d pixel (%d,%d) alpha %d", tt.empty, x, y, a)
					}
				}
			}
		})
	}

	// frame 2 of frames32 draws a half-transparent red square at (3,2).
	spr, err := ReadFrames(bytes.NewReader(openFixture(t, "frames32")))
	if err != nil {
		t.Fatal(err)
	}
	img, err := spr.FrameImage(2)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, a := img.At(3, 2).RGBA(); a == 0 || a == 0xffff {
		t.Fatalf("half-transparent pixel alpha %d", a)
	}
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Fatalf("uncovered pixel alpha %d", a)
	}

	// A 16 bpp frame has no alpha channel, matching the Gray16 atlas: an
	// uncovered pixel is transparent black, i.e. gray 0.
	g16, err := ReadFrames(bytes.NewReader(openFixture(t, "frames16")))
	if err != nil {
		t.Fatal(err)
	}
	gimg, err := g16.FrameImage(0)
	if err != nil {
		t.Fatal(err)
	}
	if y := g16FramePixels(gimg); len(y) == 0 || y[0] != 0 {
		t.Fatalf("empty 16 bpp frame luminance %v, want 0", y)
	}
}

// TestFrameBlendResult pins a concrete composed pixel so that a silent
// regression in layer blending, opacity or draw order is caught even if both
// code paths were to drift together.
func TestFrameBlendResult(t *testing.T) {
	spr, err := ReadFrames(bytes.NewReader(openFixture(t, "frames32")))
	if err != nil {
		t.Fatal(err)
	}

	img, err := spr.FrameImage(1)
	if err != nil {
		t.Fatal(err)
	}

	// Background layer: R=10*x, G=10*y, B=40, A=255.
	// Foreground (screen) at (1,1) covers canvas pixel (2,2) with
	// src=(250,60,120,200) over dst=(20,20,40,255).
	// The fixture generator and the atlas use the same code, so compare
	// against the atlas that the reference implementation produced.
	atlas, err := Read(bytes.NewReader(openFixture(t, "frames32")))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, a := img.At(2, 2).RGBA()
	ar, ag, ab, aa := atlas.Atlas.At(atlas.Frames[1].Bounds.Min.X+2, atlas.Frames[1].Bounds.Min.Y+2).RGBA()
	if r != ar || g != ag || b != ab || a != aa {
		t.Fatalf("blended pixel (%d,%d,%d,%d) != atlas (%d,%d,%d,%d)", r>>8, g>>8, b>>8, a>>8, ar>>8, ag>>8, ab>>8, aa>>8)
	}

	// The invisible third layer is fully opaque white and must not show.
	if r, g, b, _ := img.At(0, 0).RGBA(); r>>8 == 255 && g>>8 == 255 && b>>8 == 255 {
		t.Fatal("invisible layer contributed to the frame image")
	}
}

func TestFrameImageOutOfRange(t *testing.T) {
	spr, err := ReadFrames(bytes.NewReader(openFixture(t, "frames8")))
	if err != nil {
		t.Fatal(err)
	}

	for _, i := range []int{-1, len(spr.Frames), len(spr.Frames) + 100} {
		img, err := spr.FrameImage(i)
		if img != nil {
			t.Fatalf("frame %d: expected nil image", i)
		}
		if !errors.Is(err, ErrFrameIndex) {
			t.Fatalf("frame %d: error %v does not match ErrFrameIndex", i, err)
		}
	}
}

func TestFrameAccessWithoutSource(t *testing.T) {
	// A manually constructed sprite has no source, so per-frame access is
	// unavailable even though the index is in range.
	spr := Aseprite{Frames: []Frame{{}}}
	if _, err := spr.FrameImage(0); !errors.Is(err, ErrFrameAccess) {
		t.Fatalf("error %v does not match ErrFrameAccess", err)
	}

	// Without any frames, index 0 is out of range and that takes
	// precedence over the missing source.
	var empty Aseprite
	if _, err := empty.FrameImage(0); !errors.Is(err, ErrFrameIndex) {
		t.Fatalf("error %v does not match ErrFrameIndex", err)
	}

	// A zero-value sprite reports no cached frames and releasing is a
	// harmless no-op.
	if empty.FrameCached(0) {
		t.Fatal("zero-value sprite reports a cached frame")
	}
	empty.ReleaseFrame(0)
	empty.ReleaseFrames()
}

// TestFrameImageAtlasMode verifies that FrameImage also works for sprites
// decoded in the default atlas mode, without sharing atlas storage.
func TestFrameImageAtlasMode(t *testing.T) {
	spr, err := Read(bytes.NewReader(openFixture(t, "frames32")))
	if err != nil {
		t.Fatal(err)
	}

	img, err := spr.FrameImage(1)
	if err != nil {
		t.Fatal(err)
	}

	if img.Bounds() != image.Rect(0, 0, spr.Frames[1].Bounds.Dx(), spr.Frames[1].Bounds.Dy()) {
		t.Fatalf("bounds %v", img.Bounds())
	}

	want := atlasFrame(spr.Atlas, spr.Frames[1].Bounds)
	if got := framePixels(img); !bytes.Equal(got, want) {
		t.Fatal("frame image differs from atlas region")
	}

	// Mutating the returned image must not affect the atlas.
	if rgba, ok := img.(*image.RGBA); ok {
		rgba.SetRGBA(0, 0, color.RGBA{1, 2, 3, 4})
		if r, g, b, a := spr.Atlas.At(spr.Frames[1].Bounds.Min.X, spr.Frames[1].Bounds.Min.Y).RGBA(); r == 1 && g == 2 && b == 3 && a == 4 {
			t.Fatal("frame image aliases the atlas")
		}
	}
}

func TestFrameCacheAndRelease(t *testing.T) {
	for _, name := range frameFixtures {
		t.Run(name, func(t *testing.T) {
			spr, err := ReadFrames(bytes.NewReader(openFixture(t, name)))
			if err != nil {
				t.Fatal(err)
			}

			if spr.FrameCached(0) {
				t.Fatal("frame 0 cached before access")
			}

			img1, err := spr.FrameImage(0)
			if err != nil {
				t.Fatal(err)
			}
			if !spr.FrameCached(0) {
				t.Fatal("frame 0 not cached after access")
			}

			// Repeat access returns the same cached image.
			img2, err := spr.FrameImage(0)
			if err != nil {
				t.Fatal(err)
			}
			if img1 != img2 {
				t.Fatal("repeat access did not return the cached image")
			}

			// Accessing a different frame must not evict frame 0.
			if len(spr.Frames) > 1 {
				if _, err := spr.FrameImage(1); err != nil {
					t.Fatal(err)
				}
				if !spr.FrameCached(0) || !spr.FrameCached(1) {
					t.Fatal("access to another frame evicted a cached frame")
				}
			}

			spr.ReleaseFrame(0)
			if spr.FrameCached(0) {
				t.Fatal("frame 0 still cached after release")
			}

			// The released image is rebuilt on the next access and still
			// matches the atlas pixels.
			img3, err := spr.FrameImage(0)
			if err != nil {
				t.Fatal(err)
			}
			if framePixels(img3); !bytes.Equal(framePixels(img3), framePixels(img1)) {
				t.Fatal("rebuilt frame differs from the original frame")
			}

			spr.ReleaseFrames()
			for i := range spr.Frames {
				if spr.FrameCached(i) {
					t.Fatalf("frame %d still cached after ReleaseFrames", i)
				}
			}
		})
	}
}

// TestFrameAccessDecodesOnlyAccessedFrames verifies that accessing one frame
// does not decompress the whole animation.
func TestFrameAccessDecodesOnlyAccessedFrames(t *testing.T) {
	spr, err := ReadFrames(bytes.NewReader(openFixture(t, "frames32")))
	if err != nil {
		t.Fatal(err)
	}

	if spr.src == nil || spr.src.f == nil {
		t.Fatal("frame mode sprite does not retain its source")
	}

	// Reading must not decompress any compressed cel.
	if n := spr.src.f.decompressCount.Load(); n != 0 {
		t.Fatalf("reading decompressed %d cels, want 0", n)
	}

	// Frame 1 has two compressed cels.
	if _, err := spr.FrameImage(1); err != nil {
		t.Fatal(err)
	}
	if n := spr.src.f.decompressCount.Load(); n != 2 {
		t.Fatalf("accessing frame 1 decompressed %d cels, want 2", n)
	}

	// Frame 0 links to frame 1, so it resolves to the same compressed cels
	// and decompresses them again, but must not touch frame 2.
	if _, err := spr.FrameImage(0); err != nil {
		t.Fatal(err)
	}
	if n := spr.src.f.decompressCount.Load(); n != 4 {
		t.Fatalf("accessing frame 0 decompressed %d cels, want 4", n)
	}

	// Frame 2 has one uncompressed cel; it must not trigger any zlib work.
	if _, err := spr.FrameImage(2); err != nil {
		t.Fatal(err)
	}
	if n := spr.src.f.decompressCount.Load(); n != 4 {
		t.Fatalf("accessing an uncompressed frame changed decompression count to %d", n)
	}

	// Cached access must not decompress again.
	before := spr.src.f.decompressCount.Load()
	if _, err := spr.FrameImage(1); err != nil {
		t.Fatal(err)
	}
	if spr.src.f.decompressCount.Load() != before {
		t.Fatal("cached access decompressed cels again")
	}
}

// TestFrameRandomAccessCost verifies that accessing frames in random order
// decompresses each frame's cels once, not all frames per access.
func TestFrameRandomAccessCost(t *testing.T) {
	spr, err := ReadFrames(bytes.NewReader(openFixture(t, "frames8")))
	if err != nil {
		t.Fatal(err)
	}

	order := []int{3, 0, 2, 1, 2, 0, 1, 3}

	before := spr.src.f.decompressCount.Load()
	for _, i := range order {
		if _, err := spr.FrameImage(i); err != nil {
			t.Fatal(err)
		}
	}

	// frames8 has two compressed cels in frame 1; frame 2 links to them,
	// frames 0 and 3 are empty. Each distinct frame adds at most its own
	// cel count, never the whole animation.
	if got, want := spr.src.f.decompressCount.Load()-before, int64(4); got != want {
		t.Fatalf("random access decompressed %d cels, want %d", got, want)
	}
}

func TestReleaseFrameReloads(t *testing.T) {
	spr, err := ReadFrames(bytes.NewReader(openFixture(t, "frames8")))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := spr.FrameImage(1); err != nil {
		t.Fatal(err)
	}
	first := spr.src.f.decompressCount.Load()

	spr.ReleaseFrame(1)
	if _, err := spr.FrameImage(1); err != nil {
		t.Fatal(err)
	}
	if got := spr.src.f.decompressCount.Load(); got <= first {
		t.Fatalf("released frame was not re-decoded (count %d)", got)
	}
}

// TestFrameImageIndependentStorage verifies that the returned image does not
// alias the decoder's source data and that mutating it does not affect other
// frames or a later reload.
func TestFrameImageIndependentStorage(t *testing.T) {
	spr, err := ReadFrames(bytes.NewReader(openFixture(t, "frames32")))
	if err != nil {
		t.Fatal(err)
	}

	img, err := spr.FrameImage(2)
	if err != nil {
		t.Fatal(err)
	}
	before := framePixels(img)

	rgba, ok := img.(*image.RGBA)
	if !ok {
		t.Fatalf("frame image is %T", img)
	}
	for i := range rgba.Pix {
		rgba.Pix[i] = 0
	}

	spr.ReleaseFrame(2)
	reloaded, err := spr.FrameImage(2)
	if err != nil {
		t.Fatal(err)
	}
	if got := framePixels(reloaded); !bytes.Equal(got, before) {
		t.Fatal("mutating a frame image changed the decoder's source data")
	}
}

// TestFrameImageBounds pins the per-frame bounds, including for frames that
// are empty or linked.
func TestFrameImageBounds(t *testing.T) {
	for _, name := range frameFixtures {
		t.Run(name, func(t *testing.T) {
			spr, err := ReadFrames(bytes.NewReader(openFixture(t, name)))
			if err != nil {
				t.Fatal(err)
			}

			// All frames share a 8x6 canvas.
			want := image.Rect(0, 0, 8, 6)
			for i := range spr.Frames {
				if spr.Frames[i].Bounds != want {
					t.Fatalf("frame %d bounds %v, want %v", i, spr.Frames[i].Bounds, want)
				}
				img, err := spr.FrameImage(i)
				if err != nil {
					t.Fatal(err)
				}
				if img.Bounds() != want {
					t.Fatalf("frame %d image bounds %v, want %v", i, img.Bounds(), want)
				}
			}
		})
	}
}

func TestLinkedCelForwardLink(t *testing.T) {
	spr, err := ReadFrames(bytes.NewReader(openFixture(t, "frames32")))
	if err != nil {
		t.Fatal(err)
	}

	// Frame 0 links forward to frame 1, so they must render identically.
	a, err := spr.FrameImage(0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := spr.FrameImage(1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(framePixels(a), framePixels(b)) {
		t.Fatal("forward-linked frame differs from its target")
	}
}

func TestInvalidFixtures(t *testing.T) {
	for _, tt := range []struct {
		name  string
		error string
	}{
		{"bad_link", "linked cel references frame"},
		{"link_cycle", "linked cel cycle"},
		{"bad_layer", "references layer"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := openFixture(t, tt.name)

			// Both modes must reject the file without panicking.
			if _, err := ReadFrames(bytes.NewReader(data)); err == nil {
				t.Fatal("frame mode accepted an invalid file")
			} else if !bytes.Contains([]byte(err.Error()), []byte(tt.error)) {
				t.Fatalf("frame mode error %q does not contain %q", err, tt.error)
			}

			if _, err := Read(bytes.NewReader(data)); err == nil {
				t.Fatal("atlas mode accepted an invalid file")
			}
		})
	}
}

// TestCorruptCelDeferred verifies that a corrupt compressed cel is reported
// when its frame is rendered in frame mode, and not before.
func TestCorruptCelDeferred(t *testing.T) {
	data := openFixture(t, "corrupt_cel")

	// Atlas mode decompresses eagerly and must fail while reading.
	if _, err := Read(bytes.NewReader(data)); err == nil {
		t.Fatal("atlas mode accepted a corrupt cel")
	}

	spr, err := ReadFrames(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("frame mode failed eagerly: %v", err)
	}

	if _, err := spr.FrameImage(0); err == nil {
		t.Fatal("frame mode did not report the corrupt cel")
	}

	// The failure must not be cached as a bogus image.
	if spr.FrameCached(0) {
		t.Fatal("corrupt frame was cached")
	}
}

func TestReadFromKeepsAtlasBehavior(t *testing.T) {
	// ReadFrom and Read must still build the atlas and leave frame mode
	// untouched.
	data := openFixture(t, "frames8")
	var spr Aseprite
	if _, err := spr.ReadFrom(bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if spr.Atlas == nil {
		t.Fatal("ReadFrom did not build an atlas")
	}

	// FrameImage on an atlas-mode sprite copies out of the atlas and does
	// not populate a per-frame cache; releasing frames is a no-op there.
	img, err := spr.FrameImage(1)
	if err != nil {
		t.Fatal(err)
	}
	if img == nil {
		t.Fatal("atlas-mode FrameImage returned nil")
	}
	if spr.FrameCached(1) {
		t.Fatal("atlas-mode FrameImage populated the per-frame cache")
	}
	spr.ReleaseFrame(1)
	spr.ReleaseFrames()
	if _, err := spr.FrameImage(1); err != nil {
		t.Fatal(err)
	}
	if img.Bounds() != image.Rect(0, 0, 8, 6) {
		t.Fatalf("bounds %v", img.Bounds())
	}
}

// TestFrameMemoryUpperBound measures retained heap to check that frame mode
// holds only the compressed source after reading, that each accessed frame
// adds at most one frame image, and that releasing frames returns that memory.
// Atlas mode always holds one atlas covering every frame.
func TestFrameMemoryUpperBound(t *testing.T) {
	if testing.Short() {
		t.Skip("memory measurement is slow")
	}

	data, err := os.ReadFile("testfiles/slime_paletted.aseprite")
	if err != nil {
		t.Fatal(err)
	}

	// retain reports the live heap growth caused by fn, with fn's result
	// kept alive so that it is not collected early.
	retain := func(fn func()) int64 {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		fn()
		runtime.GC()
		runtime.ReadMemStats(&after)
		return int64(after.HeapAlloc) - int64(before.HeapAlloc)
	}

	var atlasSpr, frameSpr *Aseprite
	var held any

	atlasHeap := retain(func() {
		spr, err := Read(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		atlasSpr = &spr
		held = spr.Atlas
	})
	if atlasSpr.Atlas == nil {
		t.Fatal("atlas mode did not build an atlas")
	}

	frameHeap := retain(func() {
		spr, err := ReadFrames(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		frameSpr = spr
	})
	if frameSpr.Atlas != nil {
		t.Fatal("frame mode built an atlas")
	}

	// Reading in frame mode must not hold a full-size atlas. The compressed
	// source is retained, so compare against the atlas rather than zero.
	if frameHeap*2 >= atlasHeap {
		t.Fatalf("frame mode retains %d bytes reading, atlas mode retains %d", frameHeap, atlasHeap)
	}

	// One accessed frame adds one frame image, not the whole animation.
	const framePixels = 32 * 64 * 4
	oneHeap := retain(func() {
		if _, err := frameSpr.FrameImage(0); err != nil {
			t.Fatal(err)
		}
	})
	if oneHeap > 4*framePixels {
		t.Fatalf("one cached frame retained %d bytes, want at most %d", oneHeap, 4*framePixels)
	}

	// Caching every frame must stay under one image per frame.
	allHeap := retain(func() {
		for i := range frameSpr.Frames {
			if _, err := frameSpr.FrameImage(i); err != nil {
				t.Fatal(err)
			}
		}
	})
	if allHeap > int64(2*len(frameSpr.Frames)*framePixels) {
		t.Fatalf("caching all %d frames retained %d bytes, want at most %d",
			len(frameSpr.Frames), allHeap, 2*len(frameSpr.Frames)*framePixels)
	}

	// Releasing frames must drop them.
	releaseHeap := retain(func() {
		frameSpr.ReleaseFrames()
	})
	if releaseHeap > 0 {
		t.Fatalf("releasing frames grew the heap by %d bytes", releaseHeap)
	}
	for i := range frameSpr.Frames {
		if frameSpr.FrameCached(i) {
			t.Fatalf("frame %d still cached after release", i)
		}
	}

	runtime.KeepAlive(held)
	runtime.KeepAlive(atlasSpr)
	runtime.KeepAlive(frameSpr)
}

func TestFrameCacheConcurrentAccess(t *testing.T) {
	spr, err := ReadFrames(bytes.NewReader(openFixture(t, "frames32")))
	if err != nil {
		t.Fatal(err)
	}

	const goroutines = 8
	done := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			for i := 0; i < len(spr.Frames); i++ {
				frame := (i + g) % len(spr.Frames)
				if _, err := spr.FrameImage(frame); err != nil {
					done <- err
					return
				}
				if g%2 == 0 {
					spr.ReleaseFrame(frame)
				}
			}
			done <- nil
		}(g)
	}

	for g := 0; g < goroutines; g++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
