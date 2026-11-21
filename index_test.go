package aseprite

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"image"
	"image/draw"
	"io"
	"math/rand"
	"os"
	"runtime"
	"testing"
)

// countingReadSeeker counts the number of bytes read from the source.
type countingReadSeeker struct {
	rs    io.ReadSeeker
	nread int64
}

func (c *countingReadSeeker) Read(p []byte) (int, error) {
	n, err := c.rs.Read(p)
	c.nread += int64(n)
	return n, err
}

func (c *countingReadSeeker) Seek(offset int64, whence int) (int64, error) {
	return c.rs.Seek(offset, whence)
}

// buildSprite constructs a synthetic Aseprite file with nframes frames of
// wxh 32bpp pixels on a single layer. Every third frame is a linked cel
// pointing at the previous frame; all other frames carry zlib-compressed
// cels filled with deterministic noise.
func buildSprite(nframes, w, h int) []byte {
	var out bytes.Buffer
	out.Write(make([]byte, 128)) // header placeholder

	writeChunk := func(typ uint16, payload []byte) {
		var hdr [6]byte
		binary.LittleEndian.PutUint32(hdr[0:], uint32(len(payload)+6))
		binary.LittleEndian.PutUint16(hdr[4:], typ)
		out.Write(hdr[:])
		out.Write(payload)
	}

	writeFrame := func(frame int) {
		start := out.Len()
		out.Write(make([]byte, 16)) // frame header placeholder

		nchunks := 1

		if frame == 0 {
			// layer chunk: visible, blend normal, opaque, empty name
			p := make([]byte, 18)
			binary.LittleEndian.PutUint16(p[0:], 1) // flags: visible
			p[12] = 255                             // opacity
			writeChunk(0x2004, p)
			nchunks++
		}

		// cel chunk
		p := make([]byte, 16)
		p[6] = 255 // opacity
		if frame%3 == 2 {
			binary.LittleEndian.PutUint16(p[7:], 1) // linked cel
			p = append(p, 0, 0)
			binary.LittleEndian.PutUint16(p[16:], uint16(frame-1))
		} else {
			binary.LittleEndian.PutUint16(p[7:], 2) // compressed image
			pix := make([]byte, w*h*4)
			rng := rand.New(rand.NewSource(int64(frame)))
			rng.Read(pix)
			pix[0] = byte(frame)
			var zb bytes.Buffer
			zw := zlib.NewWriter(&zb)
			zw.Write(pix)
			zw.Close()
			wh := make([]byte, 4)
			binary.LittleEndian.PutUint16(wh[0:], uint16(w))
			binary.LittleEndian.PutUint16(wh[2:], uint16(h))
			p = append(p, wh...)
			p = append(p, zb.Bytes()...)
		}
		writeChunk(0x2005, p)

		// patch frame header
		end := out.Len()
		b := out.Bytes()[start:end]
		binary.LittleEndian.PutUint32(b[0:], uint32(end-start))
		binary.LittleEndian.PutUint16(b[4:], 0xF1FA)
		binary.LittleEndian.PutUint16(b[8:], 100) // duration ms
		binary.LittleEndian.PutUint32(b[12:], uint32(nchunks))
	}

	for i := 0; i < nframes; i++ {
		writeFrame(i)
	}

	raw := out.Bytes()
	binary.LittleEndian.PutUint32(raw[0:], uint32(len(raw)))
	binary.LittleEndian.PutUint16(raw[4:], 0xA5E0)
	binary.LittleEndian.PutUint16(raw[6:], uint16(nframes))
	binary.LittleEndian.PutUint16(raw[8:], uint16(w))
	binary.LittleEndian.PutUint16(raw[10:], uint16(h))
	binary.LittleEndian.PutUint16(raw[12:], 32) // bpp
	return raw
}

// frameFromAtlas extracts frame i of a fully decoded sprite.
func frameFromAtlas(spr *Aseprite, i int) *image.RGBA {
	r := spr.Frames[i].Bounds
	dst := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(dst, dst.Bounds(), spr.Atlas, r.Min, draw.Src)
	return dst
}

func sameImage(a, b image.Image) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	bounds := a.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if a.At(x, y) != b.At(x, y) {
				return false
			}
		}
	}
	return true
}

func decodeFull(t *testing.T, raw []byte) Aseprite {
	t.Helper()
	var spr Aseprite
	if _, err := spr.ReadFrom(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	return spr
}

// TestIndexRandomAccess compares random-access frames against the eager
// decoder for the real multi-frame compressed samples.
func TestIndexRandomAccess(t *testing.T) {
	for _, name := range []string{"slime_paletted", "slime_grayscale"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile("./testfiles/" + name + ".aseprite")
			assertNoError(t, err)

			spr := decodeFull(t, raw)

			ix, err := OpenIndex(bytes.NewReader(raw))
			assertNoError(t, err)

			if len(ix.Frames) != len(spr.Frames) {
				t.Fatalf("index has %d frames, full decode has %d", len(ix.Frames), len(spr.Frames))
			}

			for i := range ix.Frames {
				if d := ix.Frames[i].Duration; d != spr.Frames[i].Duration {
					t.Errorf("frame %d: duration %v, want %v", i, d, spr.Frames[i].Duration)
				}
				if len(ix.Frames[i].Chunks) == 0 {
					t.Errorf("frame %d: empty chunk summary", i)
				}
			}

			// access frames in random (here: reverse) order
			for i := len(ix.Frames) - 1; i >= 0; i-- {
				img, err := ix.Frame(i)
				assertNoError(t, err)
				if !sameImage(img, frameFromAtlas(&spr, i)) {
					t.Errorf("frame %d: image mismatch", i)
				}
			}
		})
	}
}

// TestIndexStreamMode checks that the one-shot stream mode decodes the
// same frames as the seekable mode, and that repeated access is served
// from the cache.
func TestIndexStreamMode(t *testing.T) {
	raw, err := os.ReadFile("./testfiles/slime_paletted.aseprite")
	assertNoError(t, err)

	spr := decodeFull(t, raw)

	// hide the Seeker to prove only io.Reader is used
	ix, err := ScanIndex(struct{ io.Reader }{bytes.NewReader(raw)})
	assertNoError(t, err)

	for _, i := range []int{9, 0, 5, 9, 0} {
		img, err := ix.Frame(i)
		assertNoError(t, err)
		if !sameImage(img, frameFromAtlas(&spr, i)) {
			t.Errorf("frame %d: image mismatch", i)
		}
	}
}

// TestIndexFrame120 builds a 150-frame compressed sprite and decodes only
// frame 120, comparing cost and result against the eager decoder.
func TestIndexFrame120(t *testing.T) {
	raw := buildSprite(150, 64, 64)

	spr := decodeFull(t, raw)
	if len(spr.Frames) != 150 {
		t.Fatalf("full decode has %d frames, want 150", len(spr.Frames))
	}

	crs := &countingReadSeeker{rs: bytes.NewReader(raw)}
	ix, err := OpenIndex(crs)
	assertNoError(t, err)

	scanBytes := crs.nread
	t.Logf("file size: %d bytes; index scan read: %d bytes", len(raw), scanBytes)
	if scanBytes >= int64(len(raw)) {
		t.Errorf("scan read %d bytes, want less than file size %d", scanBytes, len(raw))
	}

	// decode frame 120 only
	img, err := ix.Frame(120)
	assertNoError(t, err)
	if !sameImage(img, frameFromAtlas(&spr, 120)) {
		t.Error("frame 120: image mismatch")
	}

	after := crs.nread
	t.Logf("bytes read after decoding frame 120: %d", after)
	if after >= int64(len(raw)) {
		t.Errorf("read %d bytes for one frame, whole file is %d", after, len(raw))
	}

	// repeated access reuses the cache: no more reads
	if _, err := ix.Frame(120); err != nil {
		t.Fatal(err)
	}
	if crs.nread != after {
		t.Errorf("repeated Frame(120) read %d more bytes", crs.nread-after)
	}

	// every frame matches the eager decoder
	for i := 0; i < 150; i++ {
		img, err := ix.Frame(i)
		assertNoError(t, err)
		if !sameImage(img, frameFromAtlas(&spr, i)) {
			t.Fatalf("frame %d: image mismatch", i)
		}
	}
}

func heapInUse() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapInuse
}

// TestIndexFirstReadCost compares first-read memory and bytes read between
// the eager decoder and the index scan on a multi-frame compressed sample.
func TestIndexFirstReadCost(t *testing.T) {
	raw := buildSprite(150, 64, 64)

	before := heapInUse()
	spr := decodeFull(t, raw)
	fullMem := heapInUse() - before

	before = heapInUse()
	ix, err := OpenIndex(bytes.NewReader(raw))
	assertNoError(t, err)
	scanMem := heapInUse() - before

	t.Logf("eager decode: %d bytes heap; index scan: %d bytes heap", fullMem, scanMem)
	if scanMem >= fullMem {
		t.Errorf("index scan used %d bytes heap, eager decode used %d", scanMem, fullMem)
	}

	runtime.KeepAlive(spr)
	runtime.KeepAlive(ix)
}

func asFrameError(t *testing.T, err error, wantFrame int) {
	t.Helper()
	var fe *FrameError
	if !errors.As(err, &fe) {
		t.Fatalf("error %v is not a FrameError", err)
	}
	if fe.Frame != wantFrame {
		t.Fatalf("FrameError.Frame = %d, want %d (err: %v)", fe.Frame, wantFrame, err)
	}
	t.Logf("frame %d error: %v", fe.Frame, err)
}

func TestIndexErrors(t *testing.T) {
	raw, err := os.ReadFile("./testfiles/slime_paletted.aseprite")
	assertNoError(t, err)

	t.Run("truncated frame", func(t *testing.T) {
		trunc := raw[:len(raw)-5]
		_, err := OpenIndex(bytes.NewReader(trunc))
		asFrameError(t, err, 9) // slime_paletted has 10 frames
	})

	t.Run("bad chunk length", func(t *testing.T) {
		corrupt := bytes.Clone(raw)
		binary.LittleEndian.PutUint32(corrupt[144:], 3) // first chunk of frame 0
		_, err := OpenIndex(bytes.NewReader(corrupt))
		asFrameError(t, err, 0)

		// the eager decoder rejects it too
		var spr Aseprite
		_, err = spr.ReadFrom(bytes.NewReader(corrupt))
		asFrameError(t, err, 0)
	})

	t.Run("oversized frame", func(t *testing.T) {
		corrupt := bytes.Clone(raw)
		binary.LittleEndian.PutUint32(corrupt[128:], 0x7FFFFFFF) // frame 0 length
		_, err := OpenIndex(bytes.NewReader(corrupt))
		asFrameError(t, err, 0)
	})

	t.Run("huge chunk count", func(t *testing.T) {
		corrupt := bytes.Clone(raw)
		binary.LittleEndian.PutUint32(corrupt[128+12:], 0xFFFFFFFF)
		_, err := OpenIndex(bytes.NewReader(corrupt))
		asFrameError(t, err, 0)
	})

	t.Run("forward linked cel", func(t *testing.T) {
		fwd := buildSprite(3, 4, 4)

		// locate frame 2's linked cel via the index and patch its target
		// from frame 1 to frame 2 (a self/forward reference)
		ix, err := OpenIndex(bytes.NewReader(fwd))
		assertNoError(t, err)
		if len(ix.Frames) != 3 || len(ix.Frames[2].Chunks) != 1 {
			t.Fatalf("unexpected index: %+v", ix.Frames)
		}
		linkOfs := ix.Frames[2].Offset + 16 + 6 + 16
		binary.LittleEndian.PutUint16(fwd[linkOfs:], 2)

		// scan still succeeds; decoding frame 2 fails
		ix, err = OpenIndex(bytes.NewReader(fwd))
		assertNoError(t, err)
		_, err = ix.Frame(2)
		asFrameError(t, err, 2)

		// the eager decoder rejects it too
		var spr Aseprite
		_, err = spr.ReadFrom(bytes.NewReader(fwd))
		asFrameError(t, err, 2)
	})

	t.Run("out of range", func(t *testing.T) {
		ix, err := OpenIndex(bytes.NewReader(raw))
		assertNoError(t, err)
		if _, err := ix.Frame(len(ix.Frames)); err == nil {
			t.Fatal("expected error for out-of-range frame")
		}
	})
}
