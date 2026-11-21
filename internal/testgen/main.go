// Command testgen generates aseprite fixtures whose cels extend beyond the
// canvas, including negative coordinates and malformed dimensions. The
// fixtures are written to testdata/ for the decode tests.
//
// Run from the repository root:
//
//	go run ./internal/testgen
package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"os"
	"path/filepath"
)

const (
	canvasW = 16
	canvasH = 16
)

func u16(b []byte, v uint16) { binary.LittleEndian.PutUint16(b, v) }
func u32(b []byte, v uint32) { binary.LittleEndian.PutUint32(b, v) }

// frameHeader builds the frame header. This decoder reads chunks starting at
// offset 16, matching the layout of the existing test fixtures.
func frameHeader(nchunks uint32) []byte {
	b := make([]byte, 16)
	u16(b[4:], 0xF1FA)
	u16(b[8:], 100) // duration in ms
	u32(b[12:], nchunks)
	return b
}

// chunk wraps a chunk body with its 6-byte header.
func chunk(typ uint16, body []byte) []byte {
	b := make([]byte, 6+len(body))
	u32(b[0:], uint32(len(b)))
	u16(b[4:], typ)
	copy(b[6:], body)
	return b
}

// layerChunk builds a 0x2004 layer chunk. blendMode 0 is normal.
func layerChunk(blendMode uint16) []byte {
	b := make([]byte, 18)
	u16(b[0:], 1) // visible
	u16(b[2:], 0) // normal layer (not a group/tilemap)
	u16(b[8:], blendMode)
	b[12] = 255    // opacity
	u16(b[14:], 0) // flags
	return chunk(0x2004, b)
}

// celChunk builds a 0x2005 image cel chunk. x and y are signed; w and h
// unsigned, matching the aseprite file specification.
func celChunk(layer, x, y, w, h, bpp int, pix []byte, compressed bool) []byte {
	body := make([]byte, 20)
	u16(body[0:], uint16(layer))
	u16(body[2:], uint16(int16(x)))
	u16(body[4:], uint16(int16(y)))
	body[6] = 255 // opacity
	if compressed {
		body[7] = 2 // compressed image
	} else {
		body[7] = 0 // uncompressed image
	}
	u16(body[16:], uint16(w))
	u16(body[18:], uint16(h))
	body = append(body, pix...)
	return chunk(0x2005, body)
}

// linkedCelChunk builds a 0x2005 linked cel chunk (type 1).
func linkedCelChunk(layer, x, y, srcFrame int) []byte {
	body := make([]byte, 18)
	u16(body[0:], uint16(layer))
	u16(body[2:], uint16(int16(x)))
	u16(body[4:], uint16(int16(y)))
	body[6] = 255
	body[7] = 1 // linked cel
	u16(body[16:], uint16(srcFrame))
	return chunk(0x2005, body)
}

// zeroDimCelChunk builds a well-formed cel chunk claiming zero width.
func zeroDimCelChunk(layer, x, y int) []byte {
	body := make([]byte, 20)
	u16(body[0:], uint16(layer))
	u16(body[2:], uint16(int16(x)))
	u16(body[4:], uint16(int16(y)))
	body[6] = 255
	// width and height remain zero
	return chunk(0x2005, body)
}

// paletteChunk builds a 0x2019 palette chunk with ncolors entries.
func paletteChunk(colors [][4]byte) []byte {
	body := make([]byte, 20)
	u32(body[0:], uint32(len(colors)))
	u32(body[4:], 0) // first index
	for _, c := range colors {
		entry := make([]byte, 6)
		u16(entry[0:], 0) // no name
		copy(entry[2:], c[:])
		body = append(body, entry...)
	}
	return chunk(0x2019, body)
}

// frame assembles chunks into a frame.
func frame(chunks ...[]byte) []byte {
	var b bytes.Buffer
	b.Write(frameHeader(uint32(len(chunks))))
	for _, c := range chunks {
		b.Write(c)
	}
	out := b.Bytes()
	u32(out[0:], uint32(len(out))) // patch total frame length
	return out
}

// header builds the 128-byte aseprite header.
func header(nframes, bpp int, transparentIndex uint8) []byte {
	h := make([]byte, 128)
	u32(h[0:], 128) // patched later
	u16(h[4:], 0xA5E0)
	u16(h[6:], uint16(nframes))
	u16(h[8:], canvasW)
	u16(h[10:], canvasH)
	u16(h[12:], uint16(bpp))
	u16(h[14:], 1) // layer opacity flag
	h[28] = transparentIndex
	u16(h[32:], 16) // number of palette entries
	return h
}

func zlibCompress(pix []byte) []byte {
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	_, _ = w.Write(pix)
	_ = w.Close()
	return b.Bytes()
}

// fill8 produces w*h palette indices, all set to idx.
func fill8(w, h, idx int) []byte {
	p := make([]byte, w*h)
	for i := range p {
		p[i] = byte(idx)
	}
	return p
}

// fill16 produces w*h grayscale pixels in the aseprite 16 bpp format:
// one value byte followed by one alpha byte per pixel.
func fill16(w, h int, v, a byte) []byte {
	p := make([]byte, w*h*2)
	for i := 0; i < w*h; i++ {
		p[2*i] = v
		p[2*i+1] = a
	}
	return p
}

// fill32 produces w*h NRGBA pixels.
func fill32(w, h int, c [4]byte) []byte {
	p := make([]byte, w*h*4)
	for i := 0; i < w*h; i++ {
		copy(p[4*i:], c[:])
	}
	return p
}

func writeFile(name string, hdr []byte, frames ...[]byte) {
	var b bytes.Buffer
	b.Write(hdr)
	for _, fr := range frames {
		b.Write(fr)
	}
	raw := b.Bytes()
	u32(raw[0:], uint32(len(raw)))

	if err := os.MkdirAll("testdata", 0o755); err != nil {
		panic(err)
	}

	if err := os.WriteFile(filepath.Join("testdata", name), raw, 0o644); err != nil {
		panic(err)
	}
}

func main() {
	gen8()
	gen16()
	gen32()
}

// gen8 builds a paletted (8 bpp) fixture.
//
// Palette: 0 transparent, 1 red, 2 green, 3 blue, 4 yellow, 5 magenta.
func gen8() {
	pal := [][4]byte{
		{0, 0, 0, 0},       // 0 transparent
		{255, 0, 0, 255},   // 1 red
		{0, 255, 0, 255},   // 2 green
		{0, 0, 255, 255},   // 3 blue
		{255, 255, 0, 255}, // 4 yellow
		{255, 0, 255, 255}, // 5 magenta
	}

	frames := []struct {
		chunks [][]byte
	}{}
	add := func(chunks ...[]byte) { frames = append(frames, struct{ chunks [][]byte }{chunks}) }

	// Frame 0: layer chunks + palette, then four cels crossing each corner.
	add([][]byte{
		layerChunk(0),
		layerChunk(0),
		paletteChunk(pal),
		celChunk(0, -4, -4, 8, 8, 1, fill8(8, 8, 1), false),        // top-left, red
		celChunk(1, canvasW-4, -4, 8, 8, 1, fill8(8, 8, 2), false), // top-right, green
	}...)
	// Frame 1: bottom-left and bottom-right cels.
	add(
		celChunk(0, -4, canvasH-4, 8, 8, 1, fill8(8, 8, 3), false),        // blue
		celChunk(1, canvasW-4, canvasH-4, 8, 8, 1, fill8(8, 8, 4), false), // yellow
	)
	// Frame 2: cels fully outside the canvas (negative and positive).
	add(
		celChunk(0, -24, -24, 8, 8, 1, fill8(8, 8, 1), false),
		celChunk(1, canvasW+8, canvasH+8, 8, 8, 1, fill8(8, 8, 2), false),
	)
	// Frame 3: zlib compressed cel crossing the top-left corner.
	add(
		celChunk(0, -4, -4, 8, 8, 1, zlibCompress(fill8(8, 8, 5)), true), // magenta
	)
	// Frame 4: linked cel pointing at frame 0's layer 0 cel.
	add(
		linkedCelChunk(0, -4, -4, 0),
	)
	// Frame 5: malformed cels — zero width and a truncated huge cel.
	add(
		zeroDimCelChunk(0, -4, -4),
		celChunk(0, -4, -4, 65535, 65535, 1, fill8(2, 2, 1), false),
	)

	hdr := header(len(frames), 8, 0)
	out := make([][]byte, len(frames))
	for i := range frames {
		out[i] = frame(frames[i].chunks...)
	}
	writeFile("negative_cels_8.aseprite", hdr, out...)
}

// gen16 builds a grayscale (16 bpp) fixture with two layers.
func gen16() {
	frames := make([][][]byte, 0, 4)

	// Frame 0 declares both layers; corner cels occupy distinct layers so
	// they never overwrite each other.
	frames = append(frames, [][]byte{
		layerChunk(0),
		layerChunk(0),
		celChunk(0, -4, -4, 8, 8, 2, fill16(8, 8, 200, 255), false),
		celChunk(1, canvasW-4, -4, 8, 8, 2, fill16(8, 8, 100, 255), false),
	})
	frames = append(frames, [][]byte{
		celChunk(0, -4, canvasH-4, 8, 8, 2, fill16(8, 8, 60, 255), false),
		celChunk(1, canvasW-4, canvasH-4, 8, 8, 2, fill16(8, 8, 160, 255), false),
	})
	frames = append(frames, [][]byte{
		celChunk(0, -24, -24, 8, 8, 2, fill16(8, 8, 200, 255), false),
		celChunk(1, canvasW+8, 0, 8, 8, 2, fill16(8, 8, 100, 255), false),
	})
	frames = append(frames, [][]byte{
		zeroDimCelChunk(0, -4, -4),
		celChunk(0, -4, -4, 65535, 65535, 2, fill16(2, 2, 200, 255), false),
	})

	hdr := header(len(frames), 16, 0)
	out := make([][]byte, len(frames))
	for i := range frames {
		out[i] = frame(frames[i]...)
	}
	writeFile("negative_cels_16.aseprite", hdr, out...)
}

// gen32 builds a color (32 bpp) fixture, including a blend-mode layer.
func gen32() {
	red := [4]byte{255, 0, 0, 255}
	green := [4]byte{0, 255, 0, 255}
	blue := [4]byte{0, 0, 255, 255}
	white := [4]byte{255, 255, 255, 255}
	gray := [4]byte{128, 128, 128, 255}

	frames := make([][][]byte, 0, 5)

	// Layers must be declared in the first frame: layer 0 is normal,
	// layer 1 is multiply (blend mode 1).
	frames = append(frames, [][]byte{
		layerChunk(0),
		layerChunk(1),
		celChunk(0, -4, -4, 8, 8, 4, fill32(8, 8, red), false),
		celChunk(1, canvasW-4, -4, 8, 8, 4, fill32(8, 8, green), false),
	})
	frames = append(frames, [][]byte{
		celChunk(0, -4, canvasH-4, 8, 8, 4, fill32(8, 8, blue), false),
		celChunk(1, canvasW-4, canvasH-4, 8, 8, 4, fill32(8, 8, white), false),
	})
	frames = append(frames, [][]byte{
		celChunk(0, -24, -24, 8, 8, 4, fill32(8, 8, red), false),
		celChunk(1, canvasW+8, canvasH+8, 8, 8, 4, fill32(8, 8, green), false),
	})
	// Blend frame: layer 0 fully covers the canvas in gray, layer 1 uses
	// multiply and crosses the top-left corner with red.
	frames = append(frames, [][]byte{
		celChunk(0, 0, 0, canvasW, canvasH, 4, fill32(canvasW, canvasH, gray), false),
		celChunk(1, -4, -4, 8, 8, 4, fill32(8, 8, red), false),
	})
	// Malformed frame: zero dimension and a truncated huge cel.
	frames = append(frames, [][]byte{
		zeroDimCelChunk(0, -4, -4),
		celChunk(0, -4, -4, 65535, 65535, 4, fill32(2, 2, red), false),
	})

	hdr := header(len(frames), 32, 0)
	out := make([][]byte, len(frames))
	for i := range frames {
		out[i] = frame(frames[i]...)
	}
	writeFile("negative_cels_32.aseprite", hdr, out...)
}
