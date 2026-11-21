// Command genfixtures writes the binary .aseprite fixtures used by the
// per-frame access tests. The fixtures are deterministic: running the command
// again reproduces byte-identical files.
//
// Usage:
//
//	go run ./cmd/genfixtures
package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// The fixtures share a small canvas so that invoking them is cheap and the
// per-frame vs atlas pixel comparison is easy to reason about.
const (
	canvasW = 8
	canvasH = 6
)

type chunk struct {
	typ uint16
	raw []byte
}

func (c chunk) bytes() []byte {
	b := make([]byte, 6+len(c.raw))
	binary.LittleEndian.PutUint32(b, uint32(len(b)))
	binary.LittleEndian.PutUint16(b[4:], c.typ)
	copy(b[6:], c.raw)
	return b
}

// layerChunk returns a layer chunk with the given flags, blend mode and
// opacity. A visible normal layer uses flags 1, opacity 255, blend mode 0.
func layerChunk(flags uint16, blendMode uint16, opacity byte) chunk {
	raw := make([]byte, 18)
	binary.LittleEndian.PutUint16(raw[0:], flags)
	binary.LittleEndian.PutUint16(raw[10:], blendMode)
	raw[12] = opacity
	// empty name: length 0 at [16:]
	return chunk{0x2004, raw}
}

// celChunk returns an uncompressed image cel chunk. pix is the raw pixel data
// (rows top to bottom, no padding) for the given bpp. The 16 byte cel header
// is layer(2) x(2) y(2) opacity(1) type(2) z-index(2) reserved(5), followed by
// width(2) height(2) and the pixel data.
func celChunk(layer int, x, y, w, h int, celOpacity byte, pix []byte) chunk {
	raw := make([]byte, 16)
	binary.LittleEndian.PutUint16(raw[0:], uint16(layer))
	binary.LittleEndian.PutUint16(raw[2:], uint16(x))
	binary.LittleEndian.PutUint16(raw[4:], uint16(y))
	raw[6] = celOpacity
	// cel type 0 (uncompressed) at [7:]
	var dims [4]byte
	binary.LittleEndian.PutUint16(dims[0:], uint16(w))
	binary.LittleEndian.PutUint16(dims[2:], uint16(h))
	raw = append(raw, dims[:]...)
	return chunk{0x2005, append(raw, pix...)}
}

// compressedCelChunk is celChunk with zlib-compressed pixel data.
func compressedCelChunk(layer int, x, y, w, h int, celOpacity byte, pix []byte) chunk {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(pix); err != nil {
		log.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		log.Fatal(err)
	}

	raw := make([]byte, 16)
	binary.LittleEndian.PutUint16(raw[0:], uint16(layer))
	binary.LittleEndian.PutUint16(raw[2:], uint16(x))
	binary.LittleEndian.PutUint16(raw[4:], uint16(y))
	raw[6] = celOpacity
	binary.LittleEndian.PutUint16(raw[7:], 2) // cel type 2 (compressed)
	var dims [4]byte
	binary.LittleEndian.PutUint16(dims[0:], uint16(w))
	binary.LittleEndian.PutUint16(dims[2:], uint16(h))
	raw = append(raw, dims[:]...)
	return chunk{0x2005, append(raw, buf.Bytes()...)}
}

// linkedCelChunk returns a cel chunk that links to the cel of srcFrame on the
// same layer.
func linkedCelChunk(layer, srcFrame int) chunk {
	raw := make([]byte, 18)
	binary.LittleEndian.PutUint16(raw[0:], uint16(layer))
	raw[6] = 255
	binary.LittleEndian.PutUint16(raw[7:], 1) // cel type 1 (linked)
	binary.LittleEndian.PutUint16(raw[16:], uint16(srcFrame))
	return chunk{0x2005, raw}
}

func frameBytes(chunks ...chunk) []byte {
	var body []byte
	for _, c := range chunks {
		body = append(body, c.bytes()...)
	}

	hdr := make([]byte, 16)
	binary.LittleEndian.PutUint32(hdr[0:], uint32(16+len(body)))
	binary.LittleEndian.PutUint16(hdr[4:], 0xF1FA)
	binary.LittleEndian.PutUint16(hdr[6:], uint16(len(chunks)))
	binary.LittleEndian.PutUint16(hdr[8:], 100) // 100 ms duration
	return append(hdr, body...)
}

func fileBytes(bpp uint16, frameBodies ...[]byte) []byte {
	hdr := make([]byte, 128)
	binary.LittleEndian.PutUint16(hdr[4:], 0xA5E0)
	binary.LittleEndian.PutUint16(hdr[6:], uint16(len(frameBodies)))
	binary.LittleEndian.PutUint16(hdr[8:], canvasW)
	binary.LittleEndian.PutUint16(hdr[10:], canvasH)
	binary.LittleEndian.PutUint16(hdr[12:], bpp)
	binary.LittleEndian.PutUint16(hdr[32:], 256) // palette entries
	hdr[34], hdr[35] = 1, 1                      // pixel ratio

	var body []byte
	for _, b := range frameBodies {
		body = append(body, b...)
	}

	binary.LittleEndian.PutUint32(hdr[0:], uint32(128+len(body)))
	return append(hdr, body...)
}

// pix8 builds an 8 bpp pixel buffer. idx(i,j) returns the palette index.
func pix8(w, h int, idx func(x, y int) byte) []byte {
	pix := make([]byte, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			pix[y*w+x] = idx(x, y)
		}
	}
	return pix
}

// pix16 builds a 16 bpp grayscale+alpha buffer.
func pix16(w, h int, px func(x, y int) (byte, byte)) []byte {
	pix := make([]byte, w*h*2)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			g, a := px(x, y)
			pix[(y*w+x)*2+0] = g
			pix[(y*w+x)*2+1] = a
		}
	}
	return pix
}

// pix32 builds a 32 bpp RGBA buffer.
func pix32(w, h int, px func(x, y int) (byte, byte, byte, byte)) []byte {
	pix := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, a := px(x, y)
			i := (y*w + x) * 4
			pix[i+0] = r
			pix[i+1] = g
			pix[i+2] = b
			pix[i+3] = a
		}
	}
	return pix
}

// paletteChunk sets entries starting at first to the given colors.
func paletteChunk(first int, colors ...[4]byte) chunk {
	raw := make([]byte, 20)
	binary.LittleEndian.PutUint32(raw[0:], uint32(len(colors)))
	binary.LittleEndian.PutUint32(raw[4:], uint32(first))
	for _, c := range colors {
		// Each entry is a 2 byte flags field followed by 4 byte RGBA.
		raw = append(raw, 0, 0, c[0], c[1], c[2], c[3])
	}
	return chunk{0x2019, raw}
}

var palette = [][4]byte{
	{0, 0, 0, 0},         // transparent index 0
	{255, 0, 0, 255},     // 1 opaque red
	{0, 255, 0, 255},     // 2 opaque green
	{0, 0, 255, 128},     // 3 half transparent blue
	{255, 255, 0, 255},   // 4 opaque yellow
	{255, 0, 255, 255},   // 5 opaque magenta
	{0, 255, 255, 255},   // 6 opaque cyan
	{255, 255, 255, 255}, // 7 opaque white
}

// make8 builds an 8 bpp fixture. Frame 0 owns a palette chunk. Frame 1 draws
// two overlapping layers with compressed cels. Frame 2 is linked to frame 1.
// Frame 3 is empty, so it must render fully transparent.
func make8() []byte {
	// Two layers: an opaque background and a half-opacity foreground that
	// uses the Multiply blend mode, so frame images must apply both layer
	// opacity and blending.
	l0 := layerChunk(1, 0, 255) // visible, normal, opaque
	l1 := layerChunk(1, 1, 128) // visible, multiply, half opacity

	f0 := frameBytes(append([]chunk{paletteChunk(0, palette...)}, l0, l1)...)
	// Frame 0 has no cels: a fully transparent frame.
	f1 := frameBytes(
		compressedCelChunk(0, 1, 1, 5, 4, 255, pix8(5, 4, func(x, y int) byte {
			return byte(1 + (x+y)%3) // 1..3
		})),
		compressedCelChunk(1, 2, 0, 4, 4, 200, pix8(4, 4, func(x, y int) byte {
			if (x+y)%2 == 0 {
				return 0 // transparent index
			}
			return byte(4 + (x*y)%4) // 4..7
		})),
	)
	// Frame 2 is linked to frame 1's cels.
	f2 := frameBytes(linkedCelChunk(0, 1), linkedCelChunk(1, 1))
	// Frame 3 is fully transparent too.
	f3 := frameBytes()
	return fileBytes(8, f0, f1, f2, f3)
}

// make16 builds a 16 bpp fixture with 4 frames, an empty first frame, an
// uncompressed cel and a linked cel.
func make16() []byte {
	l0 := layerChunk(1, 0, 255)
	f0 := frameBytes(l0)
	f1 := frameBytes(celChunk(0, 0, 0, canvasW, canvasH, 255, pix16(canvasW, canvasH, func(x, y int) (byte, byte) {
		// A vertical gradient with a transparent column.
		if x == 0 {
			return 0, 0
		}
		return byte(16 * x), byte(255 - 16*y)
	})))
	f2 := frameBytes(compressedCelChunk(0, 2, 2, 3, 2, 128, pix16(3, 2, func(x, y int) (byte, byte) {
		return byte(200 - 20*x), byte(255 - 40*y)
	})))
	f3 := frameBytes(linkedCelChunk(0, 2))
	return fileBytes(16, f0, f1, f2, f3)
}

// make32 builds a 32 bpp fixture with several frames exercising blend modes,
// partial transparency, an uncompressed cel and an empty frame. It also
// contains a forward link: frame 0 links to frame 1.
func make32() []byte {
	l0 := layerChunk(1, 0, 255) // visible, normal
	l1 := layerChunk(1, 2, 255) // visible, screen
	l2 := layerChunk(0, 0, 255) // invisible: must be ignored

	// Frame 0 links forward to frame 1. Layer chunks must live in frame 0.
	f0 := frameBytes(l0, l1, l2, linkedCelChunk(0, 1), linkedCelChunk(1, 1))
	f1 := frameBytes(
		compressedCelChunk(0, 0, 0, canvasW, canvasH, 255, pix32(canvasW, canvasH, func(x, y int) (byte, byte, byte, byte) {
			return byte(10 * x), byte(10 * y), 40, 255
		})),
		compressedCelChunk(1, 1, 1, 4, 3, 255, pix32(4, 3, func(x, y int) (byte, byte, byte, byte) {
			if (x+y)%3 == 0 {
				return 0, 0, 0, 0 // fully transparent
			}
			return 250, byte(60 * x), byte(60 * y), 200
		})),
		celChunk(2, 0, 0, canvasW, canvasH, 255, pix32(canvasW, canvasH, func(x, y int) (byte, byte, byte, byte) {
			return 255, 255, 255, 255 // must not appear
		})),
	)
	f2 := frameBytes(celChunk(0, 3, 2, 2, 2, 255, pix32(2, 2, func(x, y int) (byte, byte, byte, byte) {
		return 255, 0, 0, 128
	})))
	f3 := frameBytes()                     // empty
	f4 := frameBytes(linkedCelChunk(0, 2)) // link to frame 2
	return fileBytes(32, f0, f1, f2, f3, f4)
}

// makeBadLink builds a sprite whose linked cel references a frame that does
// not exist.
func makeBadLink() []byte {
	l0 := layerChunk(1, 0, 255)
	f0 := frameBytes(l0, celChunk(0, 0, 0, 2, 2, 255, pix32(2, 2, func(x, y int) (byte, byte, byte, byte) {
		return 255, 0, 0, 255
	})))
	f1 := frameBytes(linkedCelChunk(0, 99))
	return fileBytes(32, f0, f1)
}

// makeCycle builds a sprite whose linked cels form a cycle.
func makeCycle() []byte {
	l0 := layerChunk(1, 0, 255)
	f0 := frameBytes(l0, linkedCelChunk(0, 1))
	f1 := frameBytes(linkedCelChunk(0, 0))
	return fileBytes(32, f0, f1)
}

// makeBadLayer builds a sprite whose cel references a layer that does not
// exist.
func makeBadLayer() []byte {
	l0 := layerChunk(1, 0, 255)
	f0 := frameBytes(l0, celChunk(3, 0, 0, 2, 2, 255, pix32(2, 2, func(x, y int) (byte, byte, byte, byte) {
		return 255, 0, 0, 255
	})))
	return fileBytes(32, f0)
}

// makeCorrupt builds a sprite with a compressed cel whose payload is not
// valid zlib data. Frame mode detects this when the frame is rendered; atlas
// mode detects it while reading.
func makeCorrupt() []byte {
	l0 := layerChunk(1, 0, 255)
	bad := celChunk(0, 0, 0, 2, 2, 255, nil)
	binary.LittleEndian.PutUint16(bad.raw[7:], 2) // cel type 2 (compressed)
	// celChunk wrote the dimension bytes at raw[16:20]; append a payload
	// that is not valid zlib data.
	bad.raw = append(bad.raw, []byte("not zlib data")...)

	f0 := frameBytes(l0, bad)
	return fileBytes(32, f0)
}

func write(dir string, name string, data []byte) {
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		log.Fatalf("write %s: %v", p, err)
	}
	fmt.Printf("wrote %s (%d bytes)\n", p, len(data))
}

func main() {
	dir := filepath.Join("testfiles", "frames")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatal(err)
	}

	write(dir, "frames8.aseprite", make8())
	write(dir, "frames16.aseprite", make16())
	write(dir, "frames32.aseprite", make32())
	write(dir, "bad_link.aseprite", makeBadLink())
	write(dir, "link_cycle.aseprite", makeCycle())
	write(dir, "bad_layer.aseprite", makeBadLayer())
	write(dir, "corrupt_cel.aseprite", makeCorrupt())
}
