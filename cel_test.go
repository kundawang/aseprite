package aseprite

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"image/color"
	"strings"
	"testing"

	"github.com/askeladdk/aseprite/internal/require"
)

// The helpers below build small but structurally valid Aseprite files so that
// malformed cels can be exercised without shipping binary fixtures.

func makeChunk(typ uint16, data []byte) []byte {
	c := make([]byte, 6+len(data))
	binary.LittleEndian.PutUint32(c[0:], uint32(len(c)))
	binary.LittleEndian.PutUint16(c[4:], typ)
	copy(c[6:], data)
	return c
}

func makeLayerChunk() []byte {
	data := make([]byte, 16)
	binary.LittleEndian.PutUint16(data[0:], 1)  // visible
	binary.LittleEndian.PutUint16(data[2:], 0)  // normal layer
	binary.LittleEndian.PutUint16(data[10:], 0) // normal blend mode
	data[12] = 255
	return makeChunk(0x2004, data)
}

func makePaletteChunk(colors []color.NRGBA) []byte {
	data := make([]byte, 20+6*len(colors))
	binary.LittleEndian.PutUint32(data[0:], uint32(len(colors)))
	binary.LittleEndian.PutUint32(data[4:], 0)
	for i, c := range colors {
		off := 20 + 6*i
		data[off+2] = c.R
		data[off+3] = c.G
		data[off+4] = c.B
		data[off+5] = c.A
	}
	return makeChunk(0x2019, data)
}

func celHeader(layer, celtype uint16, x, y int16, opacity byte) []byte {
	data := make([]byte, 16)
	binary.LittleEndian.PutUint16(data[0:], layer)
	binary.LittleEndian.PutUint16(data[2:], uint16(x))
	binary.LittleEndian.PutUint16(data[4:], uint16(y))
	data[6] = opacity
	binary.LittleEndian.PutUint16(data[7:], celtype)
	return data
}

func makeCelChunk(layer, celtype uint16, x, y int16, opacity byte, w, h uint16, pix []byte) []byte {
	data := celHeader(layer, celtype, x, y, opacity)
	data = append(data, make([]byte, 4)...) // zlib fields, unused by this decoder
	binary.LittleEndian.PutUint16(data[16:], w)
	binary.LittleEndian.PutUint16(data[18:], h)
	data = append(data, pix...)
	return makeChunk(0x2005, data)
}

func makeLinkedCelChunk(layer, srcFrame uint16, x, y int16) []byte {
	data := celHeader(layer, 1, x, y, 255)
	data = append(data, make([]byte, 2)...)
	binary.LittleEndian.PutUint16(data[16:], srcFrame)
	return makeChunk(0x2005, data)
}

func makeCompressedCelChunk(layer uint16, x, y int16, opacity byte, w, h uint16, compressed []byte) []byte {
	return makeCelChunk(layer, 2, x, y, opacity, w, h, compressed)
}

func makeFrame(chunks ...[]byte) []byte {
	var body []byte
	for _, c := range chunks {
		body = append(body, c...)
	}

	f := make([]byte, 16)
	binary.LittleEndian.PutUint32(f[0:], uint32(16+len(body)))
	binary.LittleEndian.PutUint16(f[4:], 0xF1FA)
	binary.LittleEndian.PutUint16(f[6:], uint16(len(chunks)))
	binary.LittleEndian.PutUint16(f[8:], 100)
	binary.LittleEndian.PutUint32(f[12:], uint32(len(chunks)))

	return append(f, body...)
}

func makeSprite(w, h, bpp uint16, frames ...[]byte) []byte {
	hdr := make([]byte, 128)
	binary.LittleEndian.PutUint16(hdr[4:], 0xA5E0)
	binary.LittleEndian.PutUint16(hdr[6:], uint16(len(frames)))
	binary.LittleEndian.PutUint16(hdr[8:], w)
	binary.LittleEndian.PutUint16(hdr[10:], h)
	binary.LittleEndian.PutUint16(hdr[12:], bpp)
	binary.LittleEndian.PutUint16(hdr[32:], 256)
	hdr[34], hdr[35] = 1, 1 // pixel ratio

	file := hdr
	for _, fr := range frames {
		file = append(file, fr...)
	}
	binary.LittleEndian.PutUint32(file[0:], uint32(len(file)))
	return file
}

func zlibCompress(t *testing.T, pix []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(pix); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// pixelBytes returns n opaque pixels for the given color depth.
func pixelBytes(bpp int, n int) []byte {
	pix := make([]byte, n*bpp/8)
	for i := 0; i < n; i++ {
		switch bpp {
		case 8:
			pix[i] = byte(i + 1)
		case 16:
			// 8-bit gray followed by 8-bit alpha.
			pix[2*i] = byte(i*17 + 3)
			pix[2*i+1] = 255
		case 32:
			pix[4*i] = byte(i*11 + 1)
			pix[4*i+1] = byte(i*7 + 2)
			pix[4*i+2] = byte(i*5 + 3)
			pix[4*i+3] = 255
		}
	}
	return pix
}

func TestCompressedCelValid(t *testing.T) {
	for _, tt := range []struct {
		Name string
		Bpp  uint16
	}{
		{"indexed8", 8},
		{"grayscale16", 16},
		{"rgba32", 32},
	} {
		t.Run(tt.Name, func(t *testing.T) {
			const w, h = 4, 3
			pix := pixelBytes(int(tt.Bpp), w*h)

			chunks := [][]byte{makeLayerChunk()}
			if tt.Bpp == 8 {
				palette := make([]color.NRGBA, 256)
				for i := range palette {
					palette[i] = color.NRGBA{R: byte(i), G: byte(255 - i), B: byte(i * 3), A: 255}
				}
				chunks = append(chunks, makePaletteChunk(palette))
			}
			chunks = append(chunks, makeCompressedCelChunk(0, 0, 0, 255, w, h, zlibCompress(t, pix)))

			spr, err := Read(bytes.NewReader(makeSprite(w, h, tt.Bpp, makeFrame(chunks...))))
			require.NoError(t, err)
			require.True(t, len(spr.Frames) == 1, "frames", len(spr.Frames))

			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					i := y*w + x
					got := spr.At(x, y)

					var want color.Color
					switch tt.Bpp {
					case 8:
						want = color.NRGBA{R: pix[i], G: byte(255 - int(pix[i])), B: pix[i] * 3, A: 255}
					case 16:
						g := pix[2*i]
						want = color.Gray16{Y: uint16(g) * 0x101}
					case 32:
						want = color.RGBA{R: pix[4*i], G: pix[4*i+1], B: pix[4*i+2], A: 255}
					}

					require.True(t, got == want, "pixel", x, y, got, want)
				}
			}
		})
	}
}

func TestCompressedCelTransparency(t *testing.T) {
	const w, h = 2, 1
	// First pixel fully transparent, second fully opaque.
	pix := []byte{
		10, 20, 30, 0,
		40, 50, 60, 255,
	}

	chunks := [][]byte{
		makeLayerChunk(),
		makeCompressedCelChunk(0, 0, 0, 255, w, h, zlibCompress(t, pix)),
	}

	spr, err := Read(bytes.NewReader(makeSprite(w, h, 32, makeFrame(chunks...))))
	require.NoError(t, err)

	require.True(t, color.NRGBAModel.Convert(spr.At(0, 0)).(color.NRGBA) == (color.NRGBA{}), "transparent pixel")
	require.True(t, color.NRGBAModel.Convert(spr.At(1, 0)).(color.NRGBA) == (color.NRGBA{40, 50, 60, 255}), "opaque pixel")
}

func TestLinkedCelPreserved(t *testing.T) {
	const w, h = 2, 2
	pix := pixelBytes(32, w*h)

	frame0 := makeFrame(
		makeLayerChunk(),
		makeCompressedCelChunk(0, 0, 0, 255, w, h, zlibCompress(t, pix)),
	)
	frame1 := makeFrame(makeLinkedCelChunk(0, 0, 0, 0))

	spr, err := Read(bytes.NewReader(makeSprite(w, h, 32, frame0, frame1)))
	require.NoError(t, err)
	require.True(t, len(spr.Frames) == 2, "frames", len(spr.Frames))

	// Frame 1 is a linked cel referencing frame 0, so both atlas regions must
	// be identical.
	r0, r1 := spr.Frames[0].Bounds, spr.Frames[1].Bounds
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c0 := spr.At(r0.Min.X+x, r0.Min.Y+y)
			c1 := spr.At(r1.Min.X+x, r1.Min.Y+y)
			require.True(t, c0 == c1, "linked pixel", x, y, c0, c1)
		}
	}
}

func TestUncompressedCelValid(t *testing.T) {
	const w, h = 2, 2
	pix := pixelBytes(32, w*h)

	chunks := [][]byte{
		makeLayerChunk(),
		makeCelChunk(0, 0, 0, 0, 255, w, h, pix),
	}

	spr, err := Read(bytes.NewReader(makeSprite(w, h, 32, makeFrame(chunks...))))
	require.NoError(t, err)
	require.True(t, len(spr.Frames) == 1, "frames", len(spr.Frames))
}

func TestCompressedCelErrors(t *testing.T) {
	pix := pixelBytes(32, 2*2)
	valid := zlibCompress(t, pix)

	corruptChecksum := append([]byte{}, valid...)
	corruptChecksum[len(corruptChecksum)-1] ^= 0xFF

	// A stream that inflates to more bytes than the declared 2x2 pixels.
	inflated := zlibCompress(t, pixelBytes(32, 4*4))

	for _, tt := range []struct {
		Name    string
		W, H    uint16
		Payload []byte
		Want    string
	}{
		{"truncated", 2, 2, valid[:len(valid)-5], "truncated"},
		{"inflated", 2, 2, inflated, "exceeds"},
		{"trailing_bytes", 2, 2, append(append([]byte{}, valid...), 0xDE, 0xAD, 0xBE, 0xEF), "trailing"},
		{"bad_checksum", 2, 2, corruptChecksum, "checksum"},
		{"zero_width", 0, 2, valid, "dimensions"},
		{"zero_height", 2, 0, valid, "dimensions"},
		{"huge_dimensions", 0xFFFF, 0xFFFF, valid, "limit"},
	} {
		t.Run(tt.Name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("decoding malformed cel panicked: %v", r)
				}
			}()

			chunks := [][]byte{
				makeLayerChunk(),
				makeCompressedCelChunk(0, 0, 0, 255, tt.W, tt.H, tt.Payload),
			}

			spr, err := Read(bytes.NewReader(makeSprite(2, 2, 32, makeFrame(chunks...))))
			require.True(t, err != nil, "expected an error, got sprite", spr)
			require.True(t, strings.Contains(err.Error(), tt.Want), "error", err)
			require.True(t, strings.Contains(err.Error(), "frame 0"), "frame context", err)
			require.True(t, strings.Contains(err.Error(), "layer 0"), "layer context", err)
			require.True(t, strings.Contains(err.Error(), "cel"), "cel context", err)
		})
	}
}

func TestUncompressedCelErrors(t *testing.T) {
	const w, h = 2, 2
	pix := pixelBytes(32, w*h)

	for _, tt := range []struct {
		Name string
		Pix  []byte
		Want string
	}{
		{"truncated", pix[:len(pix)-1], "truncated"},
		{"trailing", append(append([]byte{}, pix...), 0, 0), "trailing"},
	} {
		t.Run(tt.Name, func(t *testing.T) {
			chunks := [][]byte{
				makeLayerChunk(),
				makeCelChunk(0, 0, 0, 0, 255, w, h, tt.Pix),
			}

			_, err := Read(bytes.NewReader(makeSprite(w, h, 32, makeFrame(chunks...))))
			require.True(t, err != nil, "expected an error")
			require.True(t, strings.Contains(err.Error(), tt.Want), "error", err)
			require.True(t, strings.Contains(err.Error(), "frame 0"), "frame context", err)
			require.True(t, strings.Contains(err.Error(), "layer 0"), "layer context", err)
		})
	}
}

func TestTruncatedInputDoesNotPanic(t *testing.T) {
	const w, h = 2, 2
	pix := pixelBytes(32, w*h)

	file := makeSprite(w, h, 32, makeFrame(
		makeLayerChunk(),
		makeCompressedCelChunk(0, 0, 0, 255, w, h, zlibCompress(t, pix)),
	))

	for _, n := range []int{0, 1, 16, 40, 128, len(file) - 1} {
		t.Run(fmt.Sprintf("truncate_%d", n), func(t *testing.T) {
			if n > len(file) {
				n = len(file)
			}

			_, err := Read(bytes.NewReader(file[:n]))
			require.True(t, err != nil, "expected an error")
		})
	}
}

func TestHugeFileSizeRejected(t *testing.T) {
	file := makeSprite(1, 1, 32, makeFrame(makeLayerChunk()))
	binary.LittleEndian.PutUint32(file[0:], 0xFFFFFFFF)

	_, err := Read(bytes.NewReader(file))
	require.True(t, err != nil, "expected an error")
}

func TestLinkedCelOutOfRange(t *testing.T) {
	frame := makeFrame(makeLayerChunk(), makeLinkedCelChunk(0, 5, 0, 0))

	_, err := Read(bytes.NewReader(makeSprite(1, 1, 32, frame)))
	require.True(t, err != nil, "expected an error")
	require.True(t, strings.Contains(err.Error(), "out of range"), "error", err)
}
