// Package asetest constructs Aseprite binaries from scratch for tests.
//
// Nothing here reads or derives from an existing .aseprite file. A Sprite is
// serialised field by field from the format specification so that a test can
// vary one field at a time and say exactly which byte range it is exercising.
// Fixtures are built in memory and need no external Aseprite installation.
package asetest

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
)

// Chunk type identifiers used by the builder.
const (
	TypeOldPalette = 0x0004
	TypeLayer      = 0x2004
	TypeCel        = 0x2005
	TypePalette    = 0x2019
	TypeTag        = 0x2018
	TypeSlice      = 0x2022
	TypeUserData   = 0x2020
)

// Layer flag bits.
const (
	LayerVisible   = 0x01
	LayerReference = 0x40
)

// Cel types.
const (
	CelRaw    = 0
	CelLinked = 1
	CelZlib   = 2
)

// User data flag bits.
const (
	UserDataText  = 0x01
	UserDataColor = 0x02
)

// Header is the fixed 128-byte file header.
type Header struct {
	Magic       uint16
	Frames      uint16
	Width       uint16
	Height      uint16
	Depth       uint16
	Flags       uint16
	Transparent uint8
	Colors      uint16
	PixelWidth  uint8
	PixelHeight uint8
	// DeclaredSize overrides the file-size field when ForceSize is set.
	DeclaredSize uint32
	ForceSize    bool
}

// DefaultHeader returns a header for a small, valid 8-bit sprite.
func DefaultHeader() Header {
	return Header{
		Magic:       0xA5E0,
		Frames:      1,
		Width:       4,
		Height:      4,
		Depth:       8,
		Flags:       1,
		Transparent: 0,
		Colors:      8,
		PixelWidth:  1,
		PixelHeight: 1,
	}
}

// Chunk is one frame chunk.
type Chunk struct {
	Type int
	// Body is the chunk payload, without the six-byte chunk header.
	Body []byte
	// DeclaredLen overrides the length field written into the chunk header
	// when ForceLen is set. Otherwise Type+len(Body) is written.
	DeclaredLen int
	ForceLen    bool
}

// Frame is one animation frame.
type Frame struct {
	// Duration is written to the frame duration field, in milliseconds.
	Duration uint16
	Chunks   []Chunk
	// Magic overrides the frame magic when non-zero.
	Magic uint16
	// DeclaredLen overrides the frame length field when ForceLen is set.
	DeclaredLen int
	ForceLen    bool
	// DeclaredChunks overrides the "new chunk count" field when ForceChunks is
	// set. Otherwise len(Chunks) is written.
	DeclaredChunks int
	ForceChunks    bool
}

// Sprite is a complete file description.
type Sprite struct {
	Header Header
	Frames []Frame
}

// Bytes serialises the sprite. It performs no validation: the point is to be
// able to emit deliberately broken files.
func (s *Sprite) Bytes() []byte {
	var body bytes.Buffer
	for _, fr := range s.Frames {
		body.Write(frameBytes(fr))
	}

	var h [128]byte
	binary.LittleEndian.PutUint16(h[4:], s.Header.Magic)
	binary.LittleEndian.PutUint16(h[6:], uint16(len(s.Frames)))
	binary.LittleEndian.PutUint16(h[8:], s.Header.Width)
	binary.LittleEndian.PutUint16(h[10:], s.Header.Height)
	binary.LittleEndian.PutUint16(h[12:], s.Header.Depth)
	binary.LittleEndian.PutUint16(h[14:], s.Header.Flags)
	h[28] = s.Header.Transparent
	binary.LittleEndian.PutUint16(h[32:], s.Header.Colors)
	h[34] = s.Header.PixelWidth
	h[35] = s.Header.PixelHeight

	total := uint32(128 + body.Len())
	if s.Header.ForceSize {
		total = s.Header.DeclaredSize
	}
	binary.LittleEndian.PutUint32(h[0:], total)

	out := make([]byte, 0, 128+body.Len())
	out = append(out, h[:]...)
	out = append(out, body.Bytes()...)
	return out
}

func frameBytes(fr Frame) []byte {
	var chunks bytes.Buffer
	for _, c := range fr.Chunks {
		l := 6 + len(c.Body)
		if c.ForceLen {
			l = c.DeclaredLen
		}
		var ch [6]byte
		binary.LittleEndian.PutUint32(ch[0:], uint32(l))
		binary.LittleEndian.PutUint16(ch[4:], uint16(c.Type))
		chunks.Write(ch[:])
		chunks.Write(c.Body)
	}

	nchunks := len(fr.Chunks)
	if fr.ForceChunks {
		nchunks = fr.DeclaredChunks
	}

	// Chunk framing is independent of the frame length field: a frame may
	// declare chunks that lie outside its declared length, which is exactly
	// the kind of corruption the tests need to construct.
	frameLen := 16 + chunks.Len()
	if fr.ForceLen {
		frameLen = fr.DeclaredLen
	}

	var h [16]byte
	binary.LittleEndian.PutUint32(h[0:], uint32(frameLen))
	magic := fr.Magic
	if magic == 0 {
		magic = 0xF1FA
	}
	binary.LittleEndian.PutUint16(h[4:], magic)
	binary.LittleEndian.PutUint16(h[6:], uint16(len(fr.Chunks)))
	binary.LittleEndian.PutUint16(h[8:], fr.Duration)
	binary.LittleEndian.PutUint32(h[12:], uint32(nchunks))

	out := make([]byte, 0, 16+chunks.Len())
	out = append(out, h[:]...)
	out = append(out, chunks.Bytes()...)
	return out
}

// LayerBody builds a layer chunk body (0x2004).
func LayerBody(flags uint16, layerType, blendMode uint16, opacity byte, name string) []byte {
	var b bytes.Buffer
	writeU16(&b, flags)
	writeU16(&b, layerType)
	writeU16(&b, 0) // child level
	writeU16(&b, 0) // default width
	writeU16(&b, 0) // default height
	writeU16(&b, blendMode)
	writeU16(&b, 0) // reserved
	b.WriteByte(opacity)
	b.Write([]byte{0, 0, 0}) // reserved
	writeString(&b, name)
	return b.Bytes()
}

// UserDataBody builds a user data chunk body (0x2020).
func UserDataBody(text []byte, col *[4]byte) []byte {
	var b bytes.Buffer
	flags := uint32(0)
	if text != nil {
		flags |= UserDataText
	}
	if col != nil {
		flags |= UserDataColor
	}
	writeU32(&b, flags)
	if text != nil {
		writeU16(&b, uint16(len(text)))
		b.Write(text)
	}
	if col != nil {
		b.Write(col[:])
	}
	return b.Bytes()
}

// PaletteBody builds a 32-bit palette chunk body (0x2019). Each entry is
// RGBA; names[i] may be empty. lo is the index of entries[0].
func PaletteBody(lo uint32, entries [][4]byte, names []string) []byte {
	var b bytes.Buffer
	writeU32(&b, uint32(len(entries)))
	writeU32(&b, lo)
	writeU32(&b, 0) // last index changed
	writeU32(&b, 0) // first index changed
	writeU32(&b, 0) // last index changed for transparency
	for i, e := range entries {
		flags := uint16(0)
		name := ""
		if i < len(names) {
			name = names[i]
		}
		if name != "" {
			flags |= 1
		}
		writeU16(&b, flags)
		b.Write(e[:])
		if name != "" {
			writeString(&b, name)
		}
	}
	return b.Bytes()
}

// CelBody builds a cel chunk body (0x2005).
//
// width and height describe the pixel rectangle. pix must already be in the
// file's colour depth: for CelZlib it is compressed here, for CelRaw it is
// copied verbatim (pass a short buffer to make a truncated raw cel), and for
// CelLinked it is ignored (use LinkedCelBody instead).
func CelBody(layer, x, y uint16, opacity byte, celType, width, height uint16, pix []byte) []byte {
	var b bytes.Buffer
	writeU16(&b, layer)
	writeU16(&b, x)
	writeU16(&b, y)
	b.WriteByte(opacity)
	writeU16(&b, celType)
	writeU16(&b, 0) // z-index
	b.Write([]byte{0, 0, 0, 0, 0})
	writeU16(&b, width)
	writeU16(&b, height)
	if celType == CelZlib {
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		zw.Write(pix)
		zw.Close()
		b.Write(z.Bytes())
	} else {
		b.Write(pix)
	}
	return b.Bytes()
}

// LinkedCelBody builds a linked cel chunk body (0x2005) whose pixels are taken
// from srcFrame.
func LinkedCelBody(layer, x, y uint16, opacity byte, srcFrame uint16) []byte {
	var b bytes.Buffer
	writeU16(&b, layer)
	writeU16(&b, x)
	writeU16(&b, y)
	b.WriteByte(opacity)
	writeU16(&b, CelLinked)
	writeU16(&b, 0) // z-index
	b.Write([]byte{0, 0, 0, 0, 0})
	writeU16(&b, srcFrame)
	writeU16(&b, 0)
	return b.Bytes()
}

// Tag is one entry in a tag chunk.
type Tag struct {
	Lo, Hi    uint16
	Direction uint8
	Name      string
}

// TagsBody builds a tag chunk body (0x2018).
func TagsBody(tags []Tag) []byte {
	var b bytes.Buffer
	writeU16(&b, uint16(len(tags)))
	b.Write(make([]byte, 8)) // reserved
	for _, t := range tags {
		writeU16(&b, t.Lo)
		writeU16(&b, t.Hi)
		b.WriteByte(t.Direction)
		writeU16(&b, 0)           // repeat
		b.Write(make([]byte, 10)) // reserved; entry name starts at offset 17
		writeString(&b, t.Name)
	}
	return b.Bytes()
}

// SliceKeys is the per-slice geometry in a slice chunk.
type SliceKeys struct {
	Frame  uint32
	X, Y   int32
	W, H   uint32
	CX, CY int32
	CW, CH uint32
	PX, PY int32
}

// SliceBody builds a slice chunk body (0x2022). flags selects which optional
// key fields are present in every slice entry: bit 0 adds the 9-slice centre,
// bit 1 adds the pivot. The same flags apply to every slice, as the format
// requires.
func SliceBody(flags uint32, name string, keys []SliceKeys) []byte {
	var b bytes.Buffer
	writeU32(&b, uint32(len(keys)))
	writeU32(&b, flags)
	writeU32(&b, 0) // reserved
	writeString(&b, name)
	for _, k := range keys {
		writeU32(&b, k.Frame)
		writeU32(&b, uint32(k.X))
		writeU32(&b, uint32(k.Y))
		writeU32(&b, k.W)
		writeU32(&b, k.H)
		if flags&1 != 0 {
			writeU32(&b, uint32(k.CX))
			writeU32(&b, uint32(k.CY))
			writeU32(&b, k.CW)
			writeU32(&b, k.CH)
		}
		if flags&2 != 0 {
			writeU32(&b, uint32(k.PX))
			writeU32(&b, uint32(k.PY))
		}
	}
	return b.Bytes()
}

// SolidPix returns n pixels in the given colour depth: indices for 8bpp,
// 16-bit grey+alpha for 16bpp and RGBA for 32bpp.
func SolidPix(n int, depth uint16) []byte {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		switch depth {
		case 8:
			b.WriteByte(byte(1 + i%3))
		case 16:
			g := byte(32 + (i*17)%224)
			b.Write([]byte{g, 255})
		case 32:
			b.Write([]byte{byte(1 + i%3), 0, 0, 255})
		default:
			b.WriteByte(0)
		}
	}
	return b.Bytes()
}

// ChunkOf wraps a body into a chunk with the given type.
func ChunkOf(typ int, body []byte) Chunk { return Chunk{Type: typ, Body: body} }

// FormatHeader is a convenience for tests that need a printable header.
func FormatHeader(h Header) string {
	return fmt.Sprintf("magic=0x%04x frames=%d %dx%d depth=%d", h.Magic, h.Frames, h.Width, h.Height, h.Depth)
}

func writeU16(b *bytes.Buffer, v uint16) {
	var x [2]byte
	binary.LittleEndian.PutUint16(x[:], v)
	b.Write(x[:])
}

func writeU32(b *bytes.Buffer, v uint32) {
	var x [4]byte
	binary.LittleEndian.PutUint32(x[:], v)
	b.Write(x[:])
}

func writeString(b *bytes.Buffer, s string) {
	writeU16(b, uint16(len(s)))
	b.WriteString(s)
}
