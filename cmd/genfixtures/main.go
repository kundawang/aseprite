//go:build ignore

// Command genfixtures writes the deterministic binary fixtures used by the
// decoder determinism tests. The generated files are committed under
// testfiles/determinism and pinned by SHA-256 in fixtures_test.go, so this
// command only needs to be re-run when a fixture intentionally changes.
//
// Run it from the repository root:
//
//	go run cmd/genfixtures/main.go
//
// The fixtures are deliberately small, hand-built sprites that exercise the
// format features called out by the determinism suite: 8/16/32 bpp, linked
// cels, compressed and uncompressed cels, the old palette chunks (0x0004 and
// 0x0011), a non-zero transparent index, layers with blend modes, visible and
// invisible/reference layers, slices, tags, and empty user data chunks.
package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

const outDir = "testfiles/determinism"

func put16(b []byte, v uint16) { binary.LittleEndian.PutUint16(b, v) }
func put32(b []byte, v uint32) { binary.LittleEndian.PutUint32(b, v) }

func str(s string) []byte {
	b := make([]byte, 2+len(s))
	put16(b, uint16(len(s)))
	copy(b[2:], s)
	return b
}

func chunk(typ uint16, body []byte) []byte {
	b := make([]byte, 6+len(body))
	put32(b, uint32(len(b)))
	put16(b[4:], typ)
	copy(b[6:], body)
	return b
}

// layerChunk builds a 0x2004 chunk. flags bit 0 is visibility and bit 6 marks
// a reference layer; both are interpreted by the decoder.
func layerChunk(flags, typ, blend uint16, opacity byte, name string) []byte {
	body := make([]byte, 16)
	put16(body[0:], flags)
	put16(body[2:], typ)
	put16(body[4:], 0) // child level
	put16(body[6:], 0) // default width
	put16(body[8:], 0) // default height
	put16(body[10:], blend)
	body[12] = opacity
	return chunk(0x2004, append(body, str(name)...))
}

// userDataChunk builds a 0x2020 chunk. flags bit 0 is a text string and bit 1
// is a color. flags == 0 therefore encodes empty metadata.
func userDataChunk(flags uint32, text string, color []byte) []byte {
	body := make([]byte, 4)
	put32(body, flags)
	if flags&1 != 0 {
		body = append(body, str(text)...)
	}
	if flags&2 != 0 {
		if len(color) != 4 {
			panic("color must be RGBA")
		}
		body = append(body, color...)
	}
	return chunk(0x2020, body)
}

func celHeader(layer, x, y uint16, opacity byte, celType uint16) []byte {
	body := make([]byte, 16)
	put16(body[0:], layer)
	put16(body[2:], x)
	put16(body[4:], y)
	body[6] = opacity
	put16(body[7:], celType)
	put16(body[9:], 0) // z-index
	return body
}

// celBytes verifies that pix holds w*h pixels of bpp bits each.
func celBytes(w, h int, bpp int, pix []byte) {
	if want := w * h * bpp / 8; len(pix) != want {
		panic(fmt.Sprintf("cel %dx%d at %d bpp has %d bytes, want %d", w, h, bpp, len(pix), want))
	}
}

func rawCel(layer, x, y uint16, opacity byte, w, h int, bpp int, pix []byte) []byte {
	celBytes(w, h, bpp, pix)
	body := celHeader(layer, x, y, opacity, 0)
	wh := make([]byte, 4)
	put16(wh, uint16(w))
	put16(wh[2:], uint16(h))
	body = append(body, wh...)
	body = append(body, pix...)
	return chunk(0x2005, body)
}

func compressedCel(layer, x, y uint16, opacity byte, w, h int, bpp int, pix []byte) []byte {
	celBytes(w, h, bpp, pix)
	var zb bytes.Buffer
	zw := zlib.NewWriter(&zb)
	if _, err := zw.Write(pix); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	body := celHeader(layer, x, y, opacity, 2)
	wh := make([]byte, 4)
	put16(wh, uint16(w))
	put16(wh[2:], uint16(h))
	body = append(body, wh...)
	body = append(body, zb.Bytes()...)
	return chunk(0x2005, body)
}

func linkedCel(layer, x, y uint16, opacity byte, srcFrame uint16) []byte {
	body := celHeader(layer, x, y, opacity, 1)
	b := make([]byte, 2)
	put16(b, srcFrame)
	return chunk(0x2005, append(body, b...))
}

// palette2019 builds a new-style palette chunk describing entries
// [first, last] of a palette of size total.
func palette2019(total, first, last uint32, entries [][4]byte) []byte {
	body := make([]byte, 20)
	put32(body[0:], total)
	put32(body[4:], first)
	put32(body[8:], last)
	for _, e := range entries {
		entry := make([]byte, 6)
		copy(entry[2:], e[:])
		body = append(body, entry...)
	}
	return chunk(0x2019, body)
}

type oldPacket struct {
	skip   byte
	colors [][3]byte
}

// oldPaletteChunk builds a 0x0004 or 0x0011 chunk. A packet with zero colors
// encodes n == 0, which the format defines as 256 colors.
func oldPaletteChunk(typ uint16, packets []oldPacket) []byte {
	body := make([]byte, 2)
	put16(body, uint16(len(packets)))
	for _, p := range packets {
		body = append(body, p.skip, byte(len(p.colors)))
		for _, c := range p.colors {
			body = append(body, c[:]...)
		}
	}
	return chunk(typ, body)
}

type tag struct {
	name string
	lo   uint16
	hi   uint16
	dir  byte
	rep  uint16
}

func tagChunk(tags []tag) []byte {
	body := make([]byte, 10)
	put16(body, uint16(len(tags)))
	for _, t := range tags {
		e := make([]byte, 17)
		put16(e[0:], t.lo)
		put16(e[2:], t.hi)
		e[4] = t.dir
		put16(e[5:], t.rep)
		e = append(e, str(t.name)...)
		body = append(body, e...)
	}
	return chunk(0x2018, body)
}

type slice struct {
	frame  uint32
	x, y   int32
	w, h   uint32
	center [4]int32 // x, y, w, h
	pivot  [2]int32 // x, y
}

func sliceChunk(name string, flags uint32, slices []slice) []byte {
	body := make([]byte, 12)
	put32(body[0:], uint32(len(slices)))
	put32(body[4:], flags)
	body = append(body, str(name)...)
	for _, s := range slices {
		e := make([]byte, 20)
		put32(e[0:], s.frame)
		put32(e[4:], uint32(s.x))
		put32(e[8:], uint32(s.y))
		put32(e[12:], s.w)
		put32(e[16:], s.h)
		if flags&1 != 0 {
			c := make([]byte, 16)
			put32(c[0:], uint32(s.center[0]))
			put32(c[4:], uint32(s.center[1]))
			put32(c[8:], uint32(s.center[2]))
			put32(c[12:], uint32(s.center[3]))
			e = append(e, c...)
		}
		if flags&2 != 0 {
			p := make([]byte, 8)
			put32(p[0:], uint32(s.pivot[0]))
			put32(p[4:], uint32(s.pivot[1]))
			e = append(e, p...)
		}
		body = append(body, e...)
	}
	return chunk(0x2022, body)
}

func frameChunk(durationMS uint16, chunks ...[]byte) []byte {
	var body []byte
	for _, c := range chunks {
		body = append(body, c...)
	}
	h := make([]byte, 16)
	put32(h[0:], uint32(16+len(body)))
	put16(h[4:], 0xF1FA)
	put16(h[6:], 0) // old chunk count
	put16(h[8:], durationMS)
	put32(h[12:], uint32(len(chunks)))
	return append(h, body...)
}

type sprite struct {
	width, height int
	bpp           uint16
	flags         uint16
	transparent   byte
	numColors     uint16
	frames        [][]byte
}

func build(s sprite) []byte {
	out := make([]byte, 128)
	put16(out[4:], 0xA5E0)
	put16(out[6:], uint16(len(s.frames)))
	put16(out[8:], uint16(s.width))
	put16(out[10:], uint16(s.height))
	put16(out[12:], s.bpp)
	put16(out[14:], s.flags)
	out[28] = s.transparent
	put16(out[32:], s.numColors)
	out[34], out[35] = 1, 1 // pixel ratio 1:1
	for _, f := range s.frames {
		out = append(out, f...)
	}
	put32(out[0:], uint32(len(out)))
	return out
}

// pixels returns n indexed values that cycle through count entries, always
// including the transparent index.
func indexedPixels(n int, count, transparent int) []byte {
	pix := make([]byte, n)
	for i := range pix {
		pix[i] = byte((i*5 + 1) % count)
	}
	if n > 0 {
		pix[n/2] = byte(transparent)
	}
	return pix
}

// rgbaPixels returns deterministic RGBA data with varying alpha, including
// fully transparent and opaque pixels.
func rgbaPixels(n int) []byte {
	pix := make([]byte, n*4)
	for i := 0; i < n; i++ {
		pix[i*4+0] = byte((i * 11) & 0xFF)
		pix[i*4+1] = byte((i * 29) & 0xFF)
		pix[i*4+2] = byte((i * 47) & 0xFF)
		full := i%7 == 0
		clear := i%13 == 0
		switch {
		case clear:
			pix[i*4+3] = 0
		case full:
			pix[i*4+3] = 255
		default:
			pix[i*4+3] = byte((i * 17) & 0xFF)
		}
	}
	return pix
}

// grayAlphaPixels returns 16 bpp grey+alpha pairs.
func grayAlphaPixels(n int) []byte {
	pix := make([]byte, n*2)
	for i := 0; i < n; i++ {
		pix[i*2] = byte((i * 13) & 0xFF)
		if i%9 == 0 {
			pix[i*2+1] = 0
		} else {
			pix[i*2+1] = byte((i * 31) & 0xFF)
		}
	}
	return pix
}

func fixtureIndexed2019() []byte {
	const w, h = 16, 12
	// Full palette, indices 0..7. Index 3 is the file's transparent index.
	pal := [][4]byte{
		{10, 20, 30, 255},
		{200, 30, 40, 255},
		{30, 200, 40, 255},
		{40, 50, 220, 255}, // transparent by header
		{240, 220, 20, 255},
		{20, 220, 220, 255},
		{220, 20, 220, 255},
		{250, 250, 250, 255},
	}
	base := indexedPixels(w*h, len(pal), 3)
	accent := indexedPixels(6*5, len(pal), 3)

	frame0 := frameChunk(100,
		palette2019(uint32(len(pal)), 0, uint32(len(pal))-1, pal),
		layerChunk(1, 0, 0, 255, "base"),
		userDataChunk(1, "layer-base", nil),
		layerChunk(1, 0, 1, 180, "shade"),  // multiply
		userDataChunk(0, "", nil),          // empty metadata
		layerChunk(0, 0, 0, 255, "hidden"), // invisible
		layerChunk(1|64, 0, 0, 255, "reference"),
		tagChunk([]tag{
			{name: "walk", lo: 0, hi: 3, dir: 0, rep: 2},
			{name: "idle", lo: 2, hi: 4, dir: 2, rep: 0},
		}),
		sliceChunk("hitbox", 3, []slice{
			{frame: 0, x: 1, y: 2, w: 6, h: 5, center: [4]int32{2, 2, 2, 2}, pivot: [2]int32{4, 3}},
			{frame: 0, x: 8, y: 1, w: 4, h: 8, center: [4]int32{1, 1, 2, 2}, pivot: [2]int32{2, 2}},
		}),
		userDataChunk(1|2, "slice-note", []byte{1, 2, 3, 4}),
		compressedCel(0, 0, 0, 255, w, h, 8, base),
		userDataChunk(1, "cel-base", nil),
		rawCel(1, 2, 3, 255, 6, 5, 8, accent),
		rawCel(2, 0, 0, 255, 4, 4, 8, indexedPixels(16, len(pal), 3)),
		rawCel(3, 0, 0, 255, 3, 3, 8, indexedPixels(9, len(pal), 3)),
	)

	frame1 := frameChunk(125,
		linkedCel(0, 0, 0, 255, 0),
		rawCel(1, 3, 4, 200, 6, 5, 8, accent),
	)

	frame2 := frameChunk(150,
		compressedCel(0, 0, 0, 255, w, h, 8, base),
		linkedCel(1, 3, 4, 200, 1),
	)

	frame3 := frameChunk(175,
		rawCel(0, 0, 0, 255, w, h, 8, base),
	)

	frame4 := frameChunk(200,
		compressedCel(0, 0, 0, 255, w, h, 8, indexedPixels(w*h, len(pal), 3)),
		userDataChunk(2, "", []byte{9, 8, 7, 6}),
	)

	return build(sprite{
		width: w, height: h, bpp: 8, flags: 1,
		transparent: 3, numColors: uint16(len(pal)),
		frames: [][]byte{frame0, frame1, frame2, frame3, frame4},
	})
}

func fixtureIndexedOld0004() []byte {
	const w, h = 12, 10
	const paletteSize = 16

	colors := make([][3]byte, 6)
	for i := range colors {
		colors[i] = [3]byte{byte(i * 30), byte(255 - i*20), byte(i * 10)}
	}
	later := make([][3]byte, 4)
	for i := range later {
		later[i] = [3]byte{byte(100 + i), byte(50 + i*7), byte(200 - i)}
	}
	full := make([][3]byte, 256)
	for i := range full {
		full[i] = [3]byte{byte(i), byte(255 - i), byte(i / 2)}
	}

	frame0 := frameChunk(80,
		oldPaletteChunk(0x0004, []oldPacket{
			{skip: 0, colors: colors},
			{skip: 2, colors: later},
			{skip: 1, colors: full}, // n == 0 => 256 entries
		}),
		layerChunk(1, 0, 0, 255, "indexed"),
		userDataChunk(0, "", nil),
		rawCel(0, 0, 0, 255, w, h, 8, indexedPixels(w*h, paletteSize, 5)),
	)
	frame1 := frameChunk(90,
		linkedCel(0, 0, 0, 255, 0),
	)
	frame2 := frameChunk(110,
		rawCel(0, 1, 1, 128, w-2, h-2, 8, indexedPixels((w-2)*(h-2), paletteSize, 5)),
	)

	return build(sprite{
		width: w, height: h, bpp: 8, flags: 1,
		transparent: 5, numColors: paletteSize,
		frames: [][]byte{frame0, frame1, frame2},
	})
}

func fixtureIndexedOld0011() []byte {
	const w, h = 10, 8
	const paletteSize = 8

	colors := make([][3]byte, paletteSize)
	for i := range colors {
		// 6-bit components in the file; the decoder scales them by 4.
		colors[i] = [3]byte{byte(i * 7), byte(63 - i), byte(i * 3)}
	}

	frame0 := frameChunk(60,
		oldPaletteChunk(0x0011, []oldPacket{
			{skip: 0, colors: colors},
		}),
		tagChunk(nil),               // zero tags
		sliceChunk("empty", 0, nil), // zero slices
		layerChunk(1, 0, 0, 255, "legacy"),
		userDataChunk(0, "", nil),
		rawCel(0, 0, 0, 255, w, h, 8, indexedPixels(w*h, paletteSize, 0)),
	)
	frame1 := frameChunk(70,
		compressedCel(0, 0, 0, 255, w, h, 8, indexedPixels(w*h, paletteSize, 0)),
	)

	return build(sprite{
		width: w, height: h, bpp: 8, flags: 1,
		transparent: 0, numColors: paletteSize,
		frames: [][]byte{frame0, frame1},
	})
}

func fixtureGray16() []byte {
	const w, h = 12, 10
	gray := grayAlphaPixels(w * h)
	small := grayAlphaPixels(5 * 4)

	frame0 := frameChunk(40,
		layerChunk(1, 0, 0, 255, "grey"),
		userDataChunk(1, "grey-layer", nil),
		compressedCel(0, 0, 0, 255, w, h, 16, gray),
		userDataChunk(2, "", []byte{11, 22, 33, 44}),
	)
	frame1 := frameChunk(55,
		linkedCel(0, 0, 0, 255, 0),
	)
	frame2 := frameChunk(60,
		rawCel(0, 2, 2, 128, 5, 4, 16, small),
	)

	return build(sprite{
		// The decoder allocates the header palette even for non-indexed
		// sprites, so keep the same 256-entry palette real files carry.
		width: w, height: h, bpp: 16, flags: 1,
		transparent: 0, numColors: 256,
		frames: [][]byte{frame0, frame1, frame2},
	})
}

func fixtureRGBA32() []byte {
	const w, h = 10, 10
	full := rgbaPixels(w * h)
	small := rgbaPixels(4 * 4)

	frame0 := frameChunk(33,
		layerChunk(1, 0, 0, 255, "color"),
		userDataChunk(1, "color-layer", nil),
		layerChunk(1, 0, 1, 190, "multiply"),
		layerChunk(0, 0, 0, 255, "hidden"),
		tagChunk([]tag{
			{name: "loop", lo: 0, hi: 3, dir: 1, rep: 3},
		}),
		sliceChunk("sprite", 3, []slice{
			{frame: 0, x: 0, y: 0, w: 10, h: 10, center: [4]int32{3, 3, 4, 4}, pivot: [2]int32{5, 5}},
		}),
		userDataChunk(1|2, "slice-32", []byte{5, 6, 7, 8}),
		compressedCel(0, 0, 0, 255, w, h, 32, full),
		rawCel(1, 2, 2, 255, 4, 4, 32, small),
		rawCel(2, 0, 0, 255, 2, 2, 32, rgbaPixels(4)),
	)
	frame1 := frameChunk(44,
		linkedCel(0, 0, 0, 255, 0),
		compressedCel(1, 2, 2, 255, 4, 4, 32, small),
	)
	frame2 := frameChunk(55,
		rawCel(0, 1, 1, 200, w-2, h-2, 32, rgbaPixels((w-2)*(h-2))),
	)
	frame3 := frameChunk(66,
		compressedCel(0, 0, 0, 255, w, h, 32, full),
		userDataChunk(2, "", []byte{1, 1, 1, 1}),
	)

	return build(sprite{
		width: w, height: h, bpp: 32, flags: 1,
		transparent: 0, numColors: 256,
		frames: [][]byte{frame0, frame1, frame2, frame3},
	})
}

func main() {
	fixtures := map[string][]byte{
		"indexed_2019.aseprite":    fixtureIndexed2019(),
		"indexed_old0004.aseprite": fixtureIndexedOld0004(),
		"indexed_old0011.aseprite": fixtureIndexedOld0011(),
		"gray16.aseprite":          fixtureGray16(),
		"rgba32.aseprite":          fixtureRGBA32(),
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		panic(err)
	}
	for name, data := range fixtures {
		path := filepath.Join(outDir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			panic(err)
		}
		fmt.Printf("wrote %s (%d bytes)\n", path, len(data))
	}
}
