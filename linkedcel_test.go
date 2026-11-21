package aseprite

import (
	"bytes"
	"encoding/binary"
	"image"
	"strings"
	"testing"
)

// Minimal in-memory .aseprite fixtures exercising linked cel parsing.

type testChunk struct {
	typ uint16
	raw []byte
}

func testChunkBytes(typ uint16, raw []byte) []byte {
	b := make([]byte, 6+len(raw))
	binary.LittleEndian.PutUint32(b[0:], uint32(len(b)))
	binary.LittleEndian.PutUint16(b[4:], typ)
	copy(b[6:], raw)
	return b
}

// testLayerChunk returns a visible, normal layer chunk.
func testLayerChunk(opacity byte) testChunk {
	raw := make([]byte, 18)
	binary.LittleEndian.PutUint16(raw[0:], 1) // flags: visible
	// type 0 (normal) at [2:], blend mode 0 at [10:]
	raw[12] = opacity
	// empty layer name at [16:]
	return testChunk{0x2004, raw}
}

// testImageCel returns an uncompressed 32bpp image cel chunk.
func testImageCel(layer int, w, h int, pix []byte) testChunk {
	raw := make([]byte, 16)
	binary.LittleEndian.PutUint16(raw[0:], uint16(layer))
	raw[6] = 255 // opacity
	// cel type 0 at [7:]
	raw = append(raw, 0, 0, 0, 0)
	binary.LittleEndian.PutUint16(raw[16:], uint16(w))
	binary.LittleEndian.PutUint16(raw[18:], uint16(h))
	return testChunk{0x2005, append(raw, pix...)}
}

// testLinkedCel returns a linked cel chunk pointing at srcFrame.
func testLinkedCel(layer, srcFrame int) testChunk {
	raw := make([]byte, 18)
	binary.LittleEndian.PutUint16(raw[0:], uint16(layer))
	raw[6] = 255 // opacity
	binary.LittleEndian.PutUint16(raw[7:], 1)  // cel type: linked
	binary.LittleEndian.PutUint16(raw[16:], uint16(srcFrame))
	return testChunk{0x2005, raw}
}

// testBuildFile assembles a minimal 32bpp .aseprite file.
func testBuildFile(w, h int, frameChunks ...[]testChunk) []byte {
	var body []byte
	for _, chunks := range frameChunks {
		var cb []byte
		for _, c := range chunks {
			cb = append(cb, testChunkBytes(c.typ, c.raw)...)
		}
		fh := make([]byte, 16)
		binary.LittleEndian.PutUint32(fh[0:], uint32(16+len(cb)))
		binary.LittleEndian.PutUint16(fh[4:], 0xF1FA)
		binary.LittleEndian.PutUint16(fh[6:], uint16(len(chunks)))
		binary.LittleEndian.PutUint16(fh[8:], 100) // duration ms
		body = append(body, fh...)
		body = append(body, cb...)
	}

	hdr := make([]byte, 128)
	binary.LittleEndian.PutUint32(hdr[0:], uint32(128+len(body)))
	binary.LittleEndian.PutUint16(hdr[4:], 0xA5E0)
	binary.LittleEndian.PutUint16(hdr[6:], uint16(len(frameChunks)))
	binary.LittleEndian.PutUint16(hdr[8:], uint16(w))
	binary.LittleEndian.PutUint16(hdr[10:], uint16(h))
	binary.LittleEndian.PutUint16(hdr[12:], 32)  // bpp
	binary.LittleEndian.PutUint16(hdr[32:], 256) // palette entries
	hdr[34], hdr[35] = 1, 1                      // pixel ratio
	return append(hdr, body...)
}

func testRedPix(n int) []byte {
	pix := make([]byte, n*4)
	for i := 0; i < n; i++ {
		pix[i*4+0] = 200 // R
		pix[i*4+3] = 255 // A
	}
	return pix
}

// A linked cel pointing past the last frame used to panic with an
// index out of range. It must now return an error naming frame and layer.
func TestLinkedCelFrameOutOfRange(t *testing.T) {
	for _, src := range []int{2, 0xFFFF} {
		raw := testBuildFile(2, 2,
			[]testChunk{testLayerChunk(255), testImageCel(0, 2, 2, testRedPix(4))},
			[]testChunk{testLinkedCel(0, src)},
		)

		_, err := Decode(bytes.NewReader(raw))
		if err == nil {
			t.Fatalf("src frame %d: expected error, got nil", src)
		}
		if !strings.Contains(err.Error(), "frame") || !strings.Contains(err.Error(), "layer") {
			t.Fatalf("src frame %d: error should name frame and layer: %v", src, err)
		}
	}
}

// A cel on a layer that does not exist must error, not panic.
func TestLinkedCelLayerOutOfRange(t *testing.T) {
	raw := testBuildFile(2, 2,
		[]testChunk{testLayerChunk(255), testImageCel(0, 2, 2, testRedPix(4))},
		[]testChunk{testLinkedCel(7, 0)},
	)

	_, err := Decode(bytes.NewReader(raw))
	if err == nil || !strings.Contains(err.Error(), "layer") {
		t.Fatalf("expected layer error, got %v", err)
	}
}

// Forward references and self-references (cycles) are invalid.
func TestLinkedCelForwardAndCyclic(t *testing.T) {
	t.Run("self", func(t *testing.T) {
		raw := testBuildFile(2, 2,
			[]testChunk{testLayerChunk(255), testLinkedCel(0, 0)},
		)
		if _, err := Decode(bytes.NewReader(raw)); err == nil {
			t.Fatal("expected error for self-referencing linked cel")
		}
	})

	t.Run("forward", func(t *testing.T) {
		raw := testBuildFile(2, 2,
			[]testChunk{testLayerChunk(255), testImageCel(0, 2, 2, testRedPix(4))},
			[]testChunk{testLinkedCel(0, 2)}, // frame 2 exists but comes later
			[]testChunk{testImageCel(0, 2, 2, testRedPix(4))},
		)
		if _, err := Decode(bytes.NewReader(raw)); err == nil {
			t.Fatal("expected error for forward-referencing linked cel")
		}
	})
}

// Legal backward links share pixels across frames and layers, and the
// source cel's opacity semantics are preserved by the copy.
func TestLinkedCelValidSharing(t *testing.T) {
	raw := testBuildFile(2, 2,
		[]testChunk{
			testLayerChunk(128), // layer 0: half opacity
			testLayerChunk(255), // layer 1: full opacity
			testImageCel(0, 2, 2, testRedPix(4)),
			testImageCel(1, 2, 2, testRedPix(4)),
		},
		[]testChunk{
			testLinkedCel(0, 0), // both layers link back to frame 0
			testLinkedCel(1, 0),
		},
	)

	img, err := Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}

	spr := img.(*Aseprite)
	if len(spr.Frames) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(spr.Frames))
	}

	r0, r1 := spr.Frames[0].Bounds, spr.Frames[1].Bounds
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			c0 := spr.At(r0.Min.X+x, r0.Min.Y+y)
			c1 := spr.At(r1.Min.X+x, r1.Min.Y+y)
			if c0 != c1 {
				t.Fatalf("pixel (%d,%d): frame 0 %v != frame 1 %v", x, y, c0, c1)
			}
		}
	}

	// Layer 0 has opacity 128; the topmost layer 1 is fully opaque, so
	// the flattened pixel is opaque. Decode a single-layer variant to
	// verify the linked copy keeps the source cel's opacity mask.
	raw = testBuildFile(2, 2,
		[]testChunk{testLayerChunk(128), testImageCel(0, 2, 2, testRedPix(4))},
		[]testChunk{testLinkedCel(0, 0)},
	)

	img, err = Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}

	spr = img.(*Aseprite)
	for i, r := range []image.Rectangle{spr.Frames[0].Bounds, spr.Frames[1].Bounds} {
		_, _, _, a := spr.At(r.Min.X, r.Min.Y).RGBA()
		if got := a >> 8; got != 128 {
			t.Fatalf("frame %d: expected alpha 128, got %d", i, got)
		}
	}
}

// A link to a frame with no cel on that layer decodes as transparent.
func TestLinkedCelEmptySource(t *testing.T) {
	raw := testBuildFile(2, 2,
		[]testChunk{testLayerChunk(255)}, // frame 0: no cels
		[]testChunk{testLinkedCel(0, 0)},
	)

	img, err := Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}

	spr := img.(*Aseprite)
	_, _, _, a := spr.At(spr.Frames[1].Bounds.Min.X, spr.Frames[1].Bounds.Min.Y).RGBA()
	if a != 0 {
		t.Fatalf("expected transparent pixel, got alpha %d", a>>8)
	}
}

// A long chain of links resolves iteratively without recursion.
func TestLinkedCelDeepChain(t *testing.T) {
	const n = 512

	frames := make([][]testChunk, n)
	frames[0] = []testChunk{testLayerChunk(255), testImageCel(0, 1, 1, testRedPix(1))}
	for i := 1; i < n; i++ {
		frames[i] = []testChunk{testLinkedCel(0, i - 1)}
	}

	img, err := Decode(bytes.NewReader(testBuildFile(1, 1, frames...)))
	if err != nil {
		t.Fatal(err)
	}

	spr := img.(*Aseprite)
	first := spr.At(spr.Frames[0].Bounds.Min.X, spr.Frames[0].Bounds.Min.Y)
	last := spr.At(spr.Frames[n-1].Bounds.Min.X, spr.Frames[n-1].Bounds.Min.Y)
	if first != last {
		t.Fatalf("chain end %v != chain start %v", last, first)
	}
}

// Truncated cel chunks must error, not panic.
func TestCelChunkTruncated(t *testing.T) {
	for _, raw := range []testChunk{
		{0x2005, make([]byte, 8)},  // header too short
		{0x2005, make([]byte, 17)}, // linked cel missing frame number
	} {
		data := testBuildFile(2, 2,
			[]testChunk{testLayerChunk(255), testImageCel(0, 2, 2, testRedPix(4))},
			[]testChunk{raw},
		)
		if _, err := Decode(bytes.NewReader(data)); err == nil {
			t.Fatalf("expected error for %d-byte cel chunk", len(raw.raw))
		}
	}
}
