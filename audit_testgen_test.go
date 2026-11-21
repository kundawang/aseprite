package aseprite

// testSprite builds minimal, valid Aseprite byte streams for the audit tests.
// It intentionally duplicates a tiny amount of the file layout instead of
// depending on the decoder under test: every field is written explicitly, so a
// parser regression cannot make a fixture "pass" by agreeing with itself.

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
)

const testHeaderSize = 128

type testChunk struct {
	typ  uint16
	data []byte
}

type testFrame struct {
	dur    uint16
	chunks []testChunk
}

type testSpriteSpec struct {
	w, h   int
	bpp    uint16
	frames []testFrame
}

func le16(v uint16) []byte {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], v)
	return b[:]
}

func le32(v uint32) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	return b[:]
}

func testString(s string) []byte {
	b := le16(uint16(len(s)))
	return append(b, s...)
}

func (c testChunk) encode() []byte {
	size := 6 + len(c.data)
	b := le32(uint32(size))
	b = append(b, le16(c.typ)...)
	return append(b, c.data...)
}

func (fr testFrame) encode() []byte {
	body := make([]byte, 0, 64)
	for _, c := range fr.chunks {
		body = append(body, c.encode()...)
	}
	size := 16 + len(body)
	b := le32(uint32(size))
	b = append(b, le16(0xF1FA)...)
	b = append(b, le16(uint16(len(fr.chunks)))...) // old chunk count
	b = append(b, le16(fr.dur)...)
	b = append(b, make([]byte, 2)...)              // reserved
	b = append(b, le32(uint32(len(fr.chunks)))...) // new chunk count
	// frame.Read expects the header fields at fixed offsets:
	// 0 size, 4 magic, 6 oldChunks, 8 duration, 10 reserved, 12 newChunks.
	// The append above writes newChunks at offset 12, so pad to 16.
	b = append(b, body...)
	return b
}

func (s testSpriteSpec) encode() []byte {
	var frames []byte
	for _, fr := range s.frames {
		frames = append(frames, fr.encode()...)
	}
	fileSize := testHeaderSize + len(frames)
	hdr := make([]byte, testHeaderSize)
	binary.LittleEndian.PutUint32(hdr[0:], uint32(fileSize))
	binary.LittleEndian.PutUint16(hdr[4:], 0xA5E0)
	binary.LittleEndian.PutUint16(hdr[6:], uint16(len(s.frames)))
	binary.LittleEndian.PutUint16(hdr[8:], uint16(s.w))
	binary.LittleEndian.PutUint16(hdr[10:], uint16(s.h))
	binary.LittleEndian.PutUint16(hdr[12:], s.bpp)
	binary.LittleEndian.PutUint16(hdr[14:], 1) // valid layer opacity flag
	binary.LittleEndian.PutUint32(hdr[32:], 256)
	hdr[28] = 0 // transparent index
	hdr[34], hdr[35] = 1, 1
	return append(hdr, frames...)
}

// testLayerChunk is a minimal layer record: flags=visible, type=normal,
// blend mode normal, opacity 255, name "L".
func testLayerChunk(name string) testChunk {
	var d []byte
	d = append(d, le16(1)...) // flags: visible
	d = append(d, le16(0)...) // layer type: normal
	d = append(d, le16(0)...) // child level
	d = append(d, le16(0)...) // default width
	d = append(d, le16(0)...) // default height
	d = append(d, le16(0)...) // blend mode: normal
	d = append(d, 255)        // opacity
	d = append(d, 0, 0, 0)    // reserved
	d = append(d, testString(name)...)
	return testChunk{typ: 0x2004, data: d}
}

// testCelChunkRaw builds an uncompressed image cel. The payload is `pix`
// interpreted row-major at the sprite's bit depth; w*h determines the size.
func testCelChunkRaw(layer int, x, y, w, h int, pix []byte) testChunk {
	var d []byte
	d = append(d, le16(uint16(layer))...)
	d = append(d, le16(uint16(x))...)
	d = append(d, le16(uint16(y))...)
	d = append(d, 255)        // opacity
	d = append(d, le16(0)...) // cel type: raw image
	d = append(d, le16(0)...) // z-index
	d = append(d, 0, 0, 0, 0, 0)
	d = append(d, le16(uint16(w))...)
	d = append(d, le16(uint16(h))...)
	d = append(d, pix...)
	return testChunk{typ: 0x2005, data: d}
}

// testCelChunkZlib builds a zlib-compressed image cel.
func testCelChunkZlib(layer, x, y, w, h int, pix []byte) testChunk {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	_, _ = zw.Write(pix)
	_ = zw.Close()
	var d []byte
	d = append(d, le16(uint16(layer))...)
	d = append(d, le16(uint16(x))...)
	d = append(d, le16(uint16(y))...)
	d = append(d, 255)
	d = append(d, le16(2)...) // cel type: compressed
	d = append(d, le16(0)...) // z-index
	d = append(d, 0, 0, 0, 0, 0)
	d = append(d, le16(uint16(w))...)
	d = append(d, le16(uint16(h))...)
	d = append(d, z.Bytes()...)
	return testChunk{typ: 0x2005, data: d}
}

func testUnknownChunk(typ uint16, payload []byte) testChunk {
	return testChunk{typ: typ, data: payload}
}

// testOpaquePixels returns w*h pixels of a single color for the given depth.
func testOpaquePixels(w, h int, bpp uint16) []byte {
	switch bpp {
	case 8:
		p := make([]byte, w*h)
		for i := range p {
			p[i] = 1
		}
		return p
	case 16:
		p := make([]byte, w*h*2)
		for i := 0; i < len(p); i += 2 {
			p[i] = 0x40 // gray
			p[i+1] = 0xFF
		}
		return p
	default:
		p := make([]byte, w*h*4)
		for i := 0; i < len(p); i += 4 {
			p[i] = 0x10
			p[i+1] = 0x20
			p[i+2] = 0x30
			p[i+3] = 0xFF
		}
		return p
	}
}

// testBasicSprite is a single 2x2 frame at the requested depth with one layer
// and one cel, plus the supplied extra chunks inserted after the cel.
func testBasicSprite(bpp uint16, extra ...testChunk) testSpriteSpec {
	chunks := []testChunk{
		testLayerChunk("L"),
		testCelChunkRaw(0, 0, 0, 2, 2, testOpaquePixels(2, 2, bpp)),
	}
	chunks = append(chunks, extra...)
	return testSpriteSpec{
		w: 2, h: 2, bpp: bpp,
		frames: []testFrame{{dur: 100, chunks: chunks}},
	}
}

// testChunkOffset returns the absolute offset of the i-th chunk appended by
// testBasicSprite (0 = layer, 1 = cel, 2+ = extras in order).
func testChunkOffset(bpp uint16, i int) int64 {
	chunks := []testChunk{
		testLayerChunk("L"),
		testCelChunkRaw(0, 0, 0, 2, 2, testOpaquePixels(2, 2, bpp)),
		testUnknownChunk(0, nil),
	}
	off := int64(testHeaderSize + 16)
	for j := 0; j < i; j++ {
		off += int64(len(chunks[j].encode()))
	}
	return off
}
