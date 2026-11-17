//go:build ignore

// Command genpaletteanim writes a deterministic indexed Aseprite fixture with
// three frames that share identical pixel indices while the palette entry at
// index 1 changes color on every frame. Run it from the repository root:
//
//	go run cmd/genpaletteanim/main.go
//
// The generated file is testfiles/palette_anim.aseprite and is committed to
// the repository, so this command only has to be re-run when the fixture must
// be regenerated. It carries a build-ignore tag so that it is not compiled
// into the package or its tests.
package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"os"
)

const (
	width       = 2
	height      = 2
	nframes     = 3
	npalette    = 4
	transparent = 0
)

var frameColors = [][4]byte{
	{255, 0, 0, 255}, // frame 0: red
	{0, 255, 0, 255}, // frame 1: green
	{0, 0, 255, 255}, // frame 2: blue
}

// pixels are identical on every frame: the animation is palette-only.
var pixels = []byte{0, 1, 2, 3}

func put16(b []byte, v uint16) { binary.LittleEndian.PutUint16(b, v) }
func put32(b []byte, v uint32) { binary.LittleEndian.PutUint32(b, v) }

// paletteChunk builds a 0x2019 chunk. When first==last it contains exactly one
// entry, which models an incremental single-index palette update.
func paletteChunk(first, last uint32, rgba [4]byte) []byte {
	body := make([]byte, 20)
	put32(body[0:], npalette) // total palette size stays constant
	put32(body[4:], first)
	put32(body[8:], last)
	for i := first; i <= last; i++ {
		entry := make([]byte, 6) // flags WORD (no name) + RGBA
		copy(entry[2:], rgba[:])
		body = append(body, entry...)
	}

	chunk := make([]byte, 6+len(body))
	put32(chunk[0:], uint32(len(chunk)))
	put16(chunk[4:], 0x2019)
	copy(chunk[6:], body)
	return chunk
}

// fullPaletteChunk builds a 0x2019 chunk that declares the whole palette.
func fullPaletteChunk(colors [npalette][4]byte) []byte {
	body := make([]byte, 20)
	put32(body[0:], npalette)
	put32(body[4:], 0)
	put32(body[8:], npalette-1)
	for _, rgba := range colors {
		entry := make([]byte, 6)
		copy(entry[2:], rgba[:])
		body = append(body, entry...)
	}

	chunk := make([]byte, 6+len(body))
	put32(chunk[0:], uint32(len(chunk)))
	put16(chunk[4:], 0x2019)
	copy(chunk[6:], body)
	return chunk
}

func layerChunk() []byte {
	body := make([]byte, 18)
	put16(body[0:], 1) // flags: visible
	put16(body[2:], 0) // normal layer
	put16(body[4:], 0) // child level
	put16(body[6:], 0) // default width/height ignored
	body[8] = 0        // blend mode: normal
	body[9] = 0
	body[12] = 255 // opacity
	chunk := make([]byte, 6+len(body))
	put32(chunk[0:], uint32(len(chunk)))
	put16(chunk[4:], 0x2004)
	copy(chunk[6:], body)
	return chunk
}

func compressedCelChunk() []byte {
	var zbuf bytes.Buffer
	zw := zlib.NewWriter(&zbuf)
	_, _ = zw.Write(pixels)
	_ = zw.Close()
	zdata := zbuf.Bytes()

	body := make([]byte, 20)
	put16(body[0:], 0) // layer index
	put16(body[2:], 0) // x
	put16(body[4:], 0) // y
	body[6] = 255      // opacity
	put16(body[7:], 2) // cel type: compressed image
	put16(body[9:], 0) // reserved
	put16(body[16:], width)
	put16(body[18:], height)
	body = append(body, zdata...)

	chunk := make([]byte, 6+len(body))
	put32(chunk[0:], uint32(len(chunk)))
	put16(chunk[4:], 0x2005)
	copy(chunk[6:], body)
	return chunk
}

func linkedCelChunk(srcFrame uint16) []byte {
	body := make([]byte, 18)
	put16(body[0:], 0) // layer index
	put16(body[2:], 0) // x
	put16(body[4:], 0) // y
	body[6] = 255      // opacity
	put16(body[7:], 1) // cel type: linked cel
	put16(body[9:], 0) // reserved
	put16(body[16:], srcFrame)

	chunk := make([]byte, 6+len(body))
	put32(chunk[0:], uint32(len(chunk)))
	put16(chunk[4:], 0x2005)
	copy(chunk[6:], body)
	return chunk
}

func frame(frameIndex int, chunks [][]byte) []byte {
	body := make([]byte, 0)
	for _, c := range chunks {
		body = append(body, c...)
	}

	header := make([]byte, 16)
	put32(header[0:], uint32(16+len(body)))
	put16(header[4:], 0xF1FA)
	put16(header[6:], 0)   // old chunk count (unused)
	put16(header[8:], 100) // duration 100 ms
	put32(header[12:], uint32(len(chunks)))
	return append(header, body...)
}

func main() {
	header := make([]byte, 128)
	put32(header[0:], 0) // file size patched below
	put16(header[4:], 0xA5E0)
	put16(header[6:], nframes)
	put16(header[8:], width)
	put16(header[10:], height)
	put16(header[12:], 8) // color depth: indexed
	put16(header[14:], 1) // flags: layer opacity valid
	header[28] = transparent
	put16(header[32:], npalette)
	header[34], header[35] = 1, 1 // pixel ratio 1:1

	var out bytes.Buffer
	out.Write(header)

	compressed := compressedCelChunk()
	for i := 0; i < nframes; i++ {
		var chunks [][]byte
		chunks = append(chunks, layerChunk())

		if i == 0 {
			// Frame 0 declares the full base palette.
			base := [npalette][4]byte{
				{0, 0, 0, 0},       // transparent index
				frameColors[0],     // index 1: red, repainted on later frames
				{255, 255, 0, 255}, // index 2: yellow
				{255, 0, 255, 255}, // index 3: magenta
			}
			chunks = append(chunks, fullPaletteChunk(base))
		} else {
			// Later frames only repaint index 1; other indices are inherited.
			chunks = append(chunks, paletteChunk(1, 1, frameColors[i]))
		}

		if i == 0 {
			chunks = append(chunks, compressed)
		} else {
			chunks = append(chunks, linkedCelChunk(0))
		}

		out.Write(frame(i, chunks))
	}

	// Patch the total file size.
	put32(out.Bytes()[0:], uint32(out.Len()))

	if err := os.WriteFile("testfiles/palette_anim.aseprite", out.Bytes(), 0o644); err != nil {
		panic(err)
	}
}
