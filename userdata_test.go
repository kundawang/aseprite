package aseprite

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"testing"
)

// This file builds a small but structurally complete .aseprite file so the
// metadata tests do not depend on an external art tool. The fixture mixes
// layers, frames, cels and slices and exercises every user data shape:
// text-only, text+color, empty text, color-only, unknown flag bits, invalid
// UTF-8 and a truncated chunk.

type wbuf struct{ b []byte }

func (w *wbuf) u16(v uint16) { w.b = binary.LittleEndian.AppendUint16(w.b, v) }
func (w *wbuf) u32(v uint32) { w.b = binary.LittleEndian.AppendUint32(w.b, v) }
func (w *wbuf) byte(v byte)  { w.b = append(w.b, v) }
func (w *wbuf) bytes(p []byte) {
	w.b = append(w.b, p...)
}
func (w *wbuf) str(s string) {
	w.u16(uint16(len(s)))
	w.b = append(w.b, s...)
}

func aseChunk(typ uint16, data []byte) []byte {
	w := &wbuf{}
	w.u32(uint32(6 + len(data)))
	w.u16(typ)
	w.bytes(data)
	return w.b
}

// userDataChunk builds a 0x2020 chunk. flags is written verbatim so unknown
// bits can be tested.
func userDataChunk(flags uint32, text []byte, hasText bool, col color.NRGBA, hasColor bool) []byte {
	w := &wbuf{}
	w.u32(flags)
	if hasText {
		w.u16(uint16(len(text)))
		w.bytes(text)
	}
	if hasColor {
		w.bytes([]byte{col.R, col.G, col.B, col.A})
	}
	return aseChunk(0x2020, w.b)
}

func layerChunk(flags uint16, name string, blend uint16, opacity byte) []byte {
	w := &wbuf{}
	w.u16(flags)    // flags
	w.u16(0)        // layer type (normal)
	w.u16(0)        // child level
	w.u16(0)        // default width
	w.u16(0)        // default height
	w.u16(blend)    // blend mode
	w.byte(opacity) // opacity
	w.bytes([]byte{0, 0, 0})
	w.str(name)
	return aseChunk(0x2004, w.b)
}

func rawCelChunk(layer, x, y, width, height int, pix []byte) []byte {
	w := &wbuf{}
	w.u16(uint16(layer))
	w.u16(uint16(x))
	w.u16(uint16(y))
	w.byte(255) // cel opacity
	w.u16(0)    // cel type: uncompressed
	w.u16(0)    // z-index
	w.bytes([]byte{0, 0, 0, 0, 0})
	w.u16(uint16(width))
	w.u16(uint16(height))
	w.bytes(pix)
	return aseChunk(0x2005, w.b)
}

func zlibCelChunk(layer, x, y, width, height int, pix []byte) []byte {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write(pix); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}

	w := &wbuf{}
	w.u16(uint16(layer))
	w.u16(uint16(x))
	w.u16(uint16(y))
	w.byte(255)
	w.u16(2) // cel type: compressed
	w.u16(0)
	w.bytes([]byte{0, 0, 0, 0, 0})
	w.u16(uint16(width))
	w.u16(uint16(height))
	w.bytes(z.Bytes())
	return aseChunk(0x2005, w.b)
}

func sliceChunk(name string, keys int, flags uint32) []byte {
	w := &wbuf{}
	w.u32(uint32(keys))
	w.u32(flags)
	w.bytes(make([]byte, 4))
	w.str(name)
	for i := 0; i < keys; i++ {
		w.u32(0)             // frame number
		w.u32(0)             // x
		w.u32(0)             // y
		w.u32(uint32(1 + i)) // width
		w.u32(1)             // height
		if flags&1 != 0 {
			w.u32(0)
			w.u32(0)
			w.u32(0)
			w.u32(0)
		}
		if flags&2 != 0 {
			w.u32(0)
			w.u32(0)
		}
	}
	return aseChunk(0x2022, w.b)
}

func frameBytes(duration uint16, chunks ...[]byte) []byte {
	total := 16
	for _, c := range chunks {
		total += len(c)
	}
	w := &wbuf{}
	w.u32(uint32(total))
	w.u16(0xF1FA)
	w.u16(uint16(len(chunks))) // old chunk count (used when new is 0)
	w.u16(duration)
	w.u16(0)
	w.u32(uint32(len(chunks)))
	for _, c := range chunks {
		w.bytes(c)
	}
	return w.b
}

func spriteBytes(width, height int, frames ...[]byte) []byte {
	total := 128
	for _, f := range frames {
		total += len(f)
	}

	hdr := make([]byte, 128)
	binary.LittleEndian.PutUint32(hdr[0:], uint32(total))
	binary.LittleEndian.PutUint16(hdr[4:], 0xA5E0)
	binary.LittleEndian.PutUint16(hdr[6:], uint16(len(frames)))
	binary.LittleEndian.PutUint16(hdr[8:], uint16(width))
	binary.LittleEndian.PutUint16(hdr[10:], uint16(height))
	binary.LittleEndian.PutUint16(hdr[12:], 32) // color depth
	binary.LittleEndian.PutUint16(hdr[14:], 0)  // flags
	hdr[28] = 0                                 // transparent index
	binary.LittleEndian.PutUint16(hdr[32:], 1)  // palette size
	hdr[34], hdr[35] = 1, 1                     // pixel ratio

	out := append([]byte(nil), hdr...)
	for _, f := range frames {
		out = append(out, f...)
	}
	return out
}

var (
	testRed   = []byte{255, 0, 0, 255, 255, 0, 0, 255, 255, 0, 0, 255, 255, 0, 0, 255}
	testBlue  = []byte{0, 0, 255, 255}
	testWhite = []byte{255, 255, 255, 255}
	testGreen = []byte{0, 255, 0, 255}
)

// mixedFixture returns a sprite with four layers and two frames. Frame 0 has
// three cels (one on a hidden layer) and two slice chunks that share the name
// "dup". Frame 1 has four cels carrying the trickier user data shapes.
func mixedFixture() []byte {
	hiddenColor := color.NRGBA{R: 10, G: 20, B: 30, A: 40}
	colorOnly := color.NRGBA{R: 1, G: 2, B: 3, A: 4}
	sliceColor := color.NRGBA{R: 5, G: 6, B: 7, A: 8}
	sliceColor2 := color.NRGBA{R: 9, G: 10, B: 11, A: 12}
	celColor := color.NRGBA{R: 200, G: 100, B: 50, A: 40}

	frame0 := frameBytes(100,
		layerChunk(1, "base", 0, 255),
		userDataChunk(1, []byte("layer-base"), true, color.NRGBA{}, false),

		layerChunk(0, "hidden", 0, 255),
		userDataChunk(3, []byte("layer-hidden"), true, hiddenColor, true),

		layerChunk(1, "empty", 0, 255),
		userDataChunk(1, nil, true, color.NRGBA{}, false),

		layerChunk(1, "coloronly", 0, 255),
		userDataChunk(2, nil, false, colorOnly, true),

		zlibCelChunk(0, 0, 0, 2, 2, testRed),
		userDataChunk(1, []byte("cel-a"), true, color.NRGBA{}, false),

		// Invisible layer: its cel is not composited but its user data and
		// layer association must still be reported.
		rawCelChunk(1, 0, 0, 1, 1, testGreen),
		userDataChunk(1, []byte("cel-hidden"), true, color.NRGBA{}, false),

		rawCelChunk(2, 1, 0, 1, 1, testBlue),
		userDataChunk(1, nil, true, color.NRGBA{}, false),

		sliceChunk("dup", 2, 0),
		userDataChunk(3, []byte("slice-a"), true, sliceColor, true),

		sliceChunk("dup", 1, 0),
		userDataChunk(2, nil, false, sliceColor2, true),
	)

	// Craft the truncated chunk by hand: the text length says 100 but only
	// three bytes follow. userDataChunk cannot express that, so build it.
	truncated := &wbuf{}
	truncated.u32(1)
	truncated.u16(100)
	truncated.bytes([]byte{1, 2, 3})
	frame1 := frameBytes(200,
		rawCelChunk(0, 0, 0, 1, 1, testWhite),
		userDataChunk(3, []byte("cel-f1"), true, celColor, true),

		// Invalid UTF-8 must be preserved byte for byte.
		rawCelChunk(1, 0, 0, 1, 1, testGreen),
		userDataChunk(1, []byte{0xff, 0xfe, 'A'}, true, color.NRGBA{}, false),

		// An unknown flag bit must be preserved while known fields parse.
		rawCelChunk(2, 1, 0, 1, 1, testBlue),
		userDataChunk(1|4, []byte("unknown-flag"), true, color.NRGBA{}, false),

		// A user data chunk whose declared text length runs past the chunk.
		rawCelChunk(3, 0, 1, 1, 1, testGreen),
		aseChunk(0x2020, truncated.b),
	)

	return spriteBytes(2, 2, frame0, frame1)
}

func decodeMixedFixture(t *testing.T) *Aseprite {
	t.Helper()
	img, format, err := image.Decode(bytes.NewReader(mixedFixture()))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if format != "aseprite" {
		t.Fatalf("format = %q, want aseprite", format)
	}
	spr, ok := img.(*Aseprite)
	if !ok {
		t.Fatalf("image type = %T, want *Aseprite", img)
	}
	return spr
}

func TestUserDataLayersAndCels(t *testing.T) {
	spr := decodeMixedFixture(t)

	if len(spr.Layers) != 4 {
		t.Fatalf("layers = %d, want 4", len(spr.Layers))
	}

	base := spr.Layers[0]
	if base.Index != 0 || base.Name != "base" || !base.Visible || base.Reference {
		t.Fatalf("layer 0 = %+v", base)
	}
	if !base.UserData.Present || !base.UserData.HasText || base.UserData.HasColor {
		t.Fatalf("layer 0 user data = %+v", base.UserData)
	}
	if base.UserData.Text != "layer-base" || string(base.UserData.TextBytes) != "layer-base" {
		t.Fatalf("layer 0 text = %q / %q", base.UserData.Text, base.UserData.TextBytes)
	}
	if !base.UserData.ValidUTF8 {
		t.Fatal("layer 0 text should be valid UTF-8")
	}

	hidden := spr.Layers[1]
	if hidden.Visible {
		t.Fatal("layer 1 should be invisible")
	}
	if !hidden.UserData.HasText || !hidden.UserData.HasColor {
		t.Fatalf("layer 1 user data = %+v", hidden.UserData)
	}
	if got := hidden.UserData.Color; got != (color.NRGBA{R: 10, G: 20, B: 30, A: 40}) {
		t.Fatalf("layer 1 color = %#v, want exact NRGBA", got)
	}

	// Empty text is present but empty, which must be distinguishable from the
	// absent text of the color-only layer.
	empty := spr.Layers[2]
	if !empty.UserData.HasText || empty.UserData.Text != "" || empty.UserData.HasColor {
		t.Fatalf("layer 2 user data = %+v", empty.UserData)
	}
	if empty.UserData.TextBytes == nil || len(empty.UserData.TextBytes) != 0 {
		t.Fatalf("layer 2 TextBytes = %#v, want non-nil empty", empty.UserData.TextBytes)
	}

	colorOnly := spr.Layers[3]
	if colorOnly.UserData.HasText || !colorOnly.UserData.HasColor {
		t.Fatalf("layer 3 user data = %+v", colorOnly.UserData)
	}
	if got := colorOnly.UserData.Color; got != (color.NRGBA{R: 1, G: 2, B: 3, A: 4}) {
		t.Fatalf("layer 3 color = %#v", got)
	}

	// Legacy LayerData still lists visible layers with non-empty text.
	if len(spr.LayerData) != 1 || string(spr.LayerData[0]) != "layer-base" {
		t.Fatalf("LayerData = %q", spr.LayerData)
	}

	// Frame 0 has three cels on three layers; the hidden one is reported but
	// not composited.
	f0 := spr.Frames[0].Cels
	if len(f0) != 3 {
		t.Fatalf("frame 0 cels = %d, want 3: %+v", len(f0), f0)
	}
	for i, wantLayer := range []int{0, 1, 2} {
		if f0[i].Layer != wantLayer {
			t.Fatalf("frame 0 cel %d layer = %d, want %d", i, f0[i].Layer, wantLayer)
		}
	}
	if !f0[0].Composited || f0[1].Composited || !f0[2].Composited {
		t.Fatalf("composited flags = %v %v %v", f0[0].Composited, f0[1].Composited, f0[2].Composited)
	}
	if f0[0].UserData.Text != "cel-a" {
		t.Fatalf("frame 0 cel 0 text = %q", f0[0].UserData.Text)
	}
	if f0[1].UserData.Text != "cel-hidden" || f0[1].Layer != 1 {
		t.Fatalf("hidden cel metadata lost: %+v", f0[1])
	}
	if !f0[2].UserData.HasText || f0[2].UserData.Text != "" {
		t.Fatalf("empty cel text = %+v", f0[2].UserData)
	}

	// Legacy Frame.Data keeps only visible, non-empty cel text.
	if len(spr.Frames[0].Data) != 1 || string(spr.Frames[0].Data[0]) != "cel-a" {
		t.Fatalf("frame 0 Data = %q", spr.Frames[0].Data)
	}

	// Frame 1: text+color, invalid UTF-8, unknown flags, truncated chunk.
	f1 := spr.Frames[1].Cels
	if len(f1) != 4 {
		t.Fatalf("frame 1 cels = %d, want 4: %+v", len(f1), f1)
	}
	if got := f1[0].UserData.Color; got != (color.NRGBA{R: 200, G: 100, B: 50, A: 40}) {
		t.Fatalf("frame 1 cel 0 color = %#v", got)
	}
	if f1[0].UserData.Text != "cel-f1" || !f1[0].UserData.HasColor {
		t.Fatalf("frame 1 cel 0 user data = %+v", f1[0].UserData)
	}

	raw := f1[1].UserData
	if raw.ValidUTF8 {
		t.Fatal("invalid UTF-8 reported as valid")
	}
	if !bytes.Equal(raw.TextBytes, []byte{0xff, 0xfe, 'A'}) {
		t.Fatalf("non-UTF-8 bytes = %v", raw.TextBytes)
	}
	if raw.Text != string([]byte{0xff, 0xfe, 'A'}) {
		t.Fatalf("non-UTF-8 text = %q", raw.Text)
	}

	unknown := f1[2].UserData
	if unknown.Flags&4 == 0 || unknown.UnknownFlags&4 == 0 {
		t.Fatalf("unknown flags lost: %+v", unknown)
	}
	if unknown.Text != "unknown-flag" {
		t.Fatalf("known field not parsed with unknown flag: %+v", unknown)
	}

	truncated := f1[3].UserData
	if !errors.Is(truncated.Err, ErrTruncatedUserData) {
		t.Fatalf("truncated err = %v", truncated.Err)
	}
	if !truncated.Present || !bytes.Equal(truncated.Raw, mustUserDataRaw()) {
		t.Fatalf("truncated chunk not preserved: %+v", truncated)
	}
	if string(truncated.TextBytes) != string([]byte{1, 2, 3}) {
		t.Fatalf("truncated partial text = %v", truncated.TextBytes)
	}
}

func mustUserDataRaw() []byte {
	w := &wbuf{}
	w.u32(1)
	w.u16(100)
	w.bytes([]byte{1, 2, 3})
	return w.b
}

func TestUserDataSlices(t *testing.T) {
	spr := decodeMixedFixture(t)

	if len(spr.Slices) != 3 {
		t.Fatalf("slices = %d, want 3", len(spr.Slices))
	}
	for i, s := range spr.Slices {
		if s.Name != "dup" {
			t.Fatalf("slice %d name = %q", i, s.Name)
		}
		if s.Index != i {
			t.Fatalf("slice %d index = %d", i, s.Index)
		}
	}

	// The first chunk holds two slices that share one user data chunk; the
	// second chunk holds a third slice with a duplicate name and different
	// user data. Index and Chunk tell them apart.
	if spr.Slices[0].Chunk == spr.Slices[2].Chunk {
		t.Fatalf("duplicate slices share chunk %d", spr.Slices[0].Chunk)
	}
	if spr.Slices[0].Chunk != spr.Slices[1].Chunk {
		t.Fatalf("slices from one chunk got different chunk indices")
	}

	for i := 0; i < 2; i++ {
		s := spr.Slices[i]
		if s.UserData.Text != "slice-a" || !s.UserData.HasText {
			t.Fatalf("slice %d user data = %+v", i, s.UserData)
		}
		if got := s.UserData.Color; got != (color.NRGBA{R: 5, G: 6, B: 7, A: 8}) {
			t.Fatalf("slice %d color = %#v", i, got)
		}
		// Legacy fields stay in sync.
		if string(s.Data) != "slice-a" {
			t.Fatalf("slice %d Data = %q", i, s.Data)
		}
		nrgba, ok := s.Color.(color.NRGBA)
		if !ok || nrgba != (color.NRGBA{R: 5, G: 6, B: 7, A: 8}) {
			t.Fatalf("slice %d Color = %#v", i, s.Color)
		}
	}

	third := spr.Slices[2]
	if third.UserData.HasText || !third.UserData.HasColor {
		t.Fatalf("slice 2 user data = %+v", third.UserData)
	}
	if got := third.UserData.Color; got != (color.NRGBA{R: 9, G: 10, B: 11, A: 12}) {
		t.Fatalf("slice 2 color = %#v", got)
	}
	if len(third.Data) != 0 {
		t.Fatalf("slice 2 Data = %q, want empty", third.Data)
	}
	if third.Color == nil || third.Color != (color.NRGBA{R: 9, G: 10, B: 11, A: 12}) {
		t.Fatalf("slice 2 Color = %#v", third.Color)
	}
}

// TestUserDataDoesNotAffectPixels verifies the atlas is unaffected by user
// data, including the invalid UTF-8, unknown-flag and truncated chunks.
func TestUserDataDoesNotAffectPixels(t *testing.T) {
	spr := decodeMixedFixture(t)

	r, g, b, a := spr.At(0, 0).RGBA()
	if r>>8 != 255 || g>>8 != 0 || b>>8 != 0 || a>>8 != 255 {
		t.Fatalf("frame 0 pixel (0,0) = %d,%d,%d,%d, want red", r>>8, g>>8, b>>8, a>>8)
	}

	r, g, b, a = spr.At(1, 0).RGBA()
	if r>>8 != 0 || g>>8 != 0 || b>>8 != 255 || a>>8 != 255 {
		t.Fatalf("frame 0 pixel (1,0) = %d,%d,%d,%d, want blue", r>>8, g>>8, b>>8, a>>8)
	}

	// Frame 1 starts at x=2 in the atlas.
	r, g, b, a = spr.At(2, 0).RGBA()
	if r>>8 != 255 || g>>8 != 255 || b>>8 != 255 || a>>8 != 255 {
		t.Fatalf("frame 1 pixel (0,0) = %d,%d,%d,%d, want white", r>>8, g>>8, b>>8, a>>8)
	}

	// The cel carrying the truncated user data still rendered.
	r, g, b, a = spr.At(2, 1).RGBA()
	if r>>8 != 0 || g>>8 != 255 || b>>8 != 0 || a>>8 != 255 {
		t.Fatalf("frame 1 pixel (0,1) = %d,%d,%d,%d, want green", r>>8, g>>8, b>>8, a>>8)
	}
}

func TestParseUserDataTruncated(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{"no flags", []byte{}},
		{"flags only", []byte{1, 0, 0, 0}},
		{"text length missing", []byte{1, 0, 0, 0, 3}},
		{"text runs past end", []byte{1, 0, 0, 0, 10, 0, 'a'}},
		{"color missing", []byte{2, 0, 0, 0, 1, 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := parseUserData(tt.raw)
			if !u.Present {
				t.Fatal("chunk should be reported present")
			}
			if !errors.Is(u.Err, ErrTruncatedUserData) {
				t.Fatalf("err = %v, want ErrTruncatedUserData", u.Err)
			}
			if !bytes.Equal(u.Raw, tt.raw) {
				t.Fatalf("raw = %v, want %v", u.Raw, tt.raw)
			}
		})
	}
}

func TestParseUserDataEmptyAndUnknown(t *testing.T) { // Empty text is present, an absent text is not.
	empty := parseUserData([]byte{1, 0, 0, 0, 0, 0})
	if !empty.HasText || empty.TextBytes == nil || len(empty.TextBytes) != 0 {
		t.Fatalf("empty text = %+v", empty)
	}
	if empty.Err != nil {
		t.Fatalf("empty text err = %v", empty.Err)
	}

	// Unknown flag bit does not hide the known fields.
	unknown := parseUserData([]byte{0xFD, 0xFF, 0xFF, 0xFF, 1, 0, 'x'})
	if unknown.UnknownFlags != 0xFFFFFFFC {
		t.Fatalf("unknown flags = %#x", unknown.UnknownFlags)
	}
	if unknown.Text != "x" || !unknown.HasText {
		t.Fatalf("text = %q", unknown.Text)
	}
	if unknown.Err != nil {
		t.Fatalf("err = %v", unknown.Err)
	}

	// A present color with zero alpha is still present.
	transparent := parseUserData([]byte{2, 0, 0, 0, 0, 0, 0, 0})
	if !transparent.HasColor || transparent.Color != (color.NRGBA{}) {
		t.Fatalf("transparent color = %+v", transparent)
	}
	if transparent.ColorValue() == nil {
		t.Fatal("ColorValue should be non-nil for a present transparent color")
	}
	if parseUserData([]byte{0, 0, 0, 0}).ColorValue() != nil {
		t.Fatal("ColorValue should be nil when no color is present")
	}
}

func TestChunkLengthClamping(t *testing.T) {
	// A chunk header whose declared length exceeds the bytes present must not
	// panic or hide the bytes that are present.
	body := &wbuf{}
	body.u32(1)
	body.u16(50)
	body.bytes([]byte("short"))
	raw := &wbuf{}
	raw.u32(4096) // absurd declared length
	raw.u16(0x2020)
	raw.bytes(body.b)

	var c chunk
	rest, err := c.Read(raw.b)
	if err != nil {
		t.Fatalf("chunk read: %v", err)
	}
	if len(rest) != 0 {
		t.Fatalf("rest = %d bytes, want 0", len(rest))
	}
	if !bytes.Equal(c.raw, body.b) {
		t.Fatalf("chunk body = %v, want %v", c.raw, body.b)
	}

	u := parseUserData(c.raw)
	if !errors.Is(u.Err, ErrTruncatedUserData) {
		t.Fatalf("err = %v, want ErrTruncatedUserData", u.Err)
	}
	if !bytes.Equal(u.TextBytes, []byte("short")) {
		t.Fatalf("partial text = %q", u.TextBytes)
	}
	if !bytes.Equal(u.Raw, body.b) {
		t.Fatalf("raw = %v, want %v", u.Raw, body.b)
	}

	// A chunk shorter than its own header yields an empty body, not a panic.
	var c2 chunk
	short := []byte{1, 0, 0, 0, 0x20, 0x20}
	if _, err := c2.Read(short); err != nil {
		t.Fatalf("short chunk read: %v", err)
	}
	if len(c2.raw) != 0 {
		t.Fatalf("short chunk body = %v", c2.raw)
	}

	// A header that is itself truncated reports an error.
	var c3 chunk
	if _, err := c3.Read([]byte{1, 2, 3}); err == nil {
		t.Fatal("truncated chunk header did not report an error")
	}
}

// TestTruncatedFrameKeepsEarlierChunks checks that a frame declaring more
// chunks than are present still contributes the chunks that were read,
// including their user data, and does not abort the decode.
func TestTruncatedFrameKeepsEarlierChunks(t *testing.T) {
	cel := rawCelChunk(0, 0, 0, 1, 1, testRed)
	ud := userDataChunk(1, []byte("kept"), true, color.NRGBA{}, false)

	frame := &wbuf{}
	frame.u32(0) // frame length is ignored by the decoder
	frame.u16(0xF1FA)
	frame.u16(0)
	frame.u16(100)
	frame.u16(0)
	frame.u32(4) // declare four chunks, supply two
	frame.bytes(layerChunk(1, "base", 0, 255))
	frame.bytes(cel)
	frame.bytes(ud)

	data := spriteBytes(1, 1, frame.b)

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	spr := img.(*Aseprite)
	if len(spr.Frames[0].Cels) != 1 {
		t.Fatalf("cels = %d, want 1", len(spr.Frames[0].Cels))
	}
	if spr.Frames[0].Cels[0].UserData.Text != "kept" {
		t.Fatalf("user data lost: %+v", spr.Frames[0].Cels[0].UserData)
	}
	r, g, b, a := spr.At(0, 0).RGBA()
	if r>>8 != 255 || g>>8 != 0 || b>>8 != 0 || a>>8 != 255 {
		t.Fatalf("pixel = %d,%d,%d,%d, want red", r>>8, g>>8, b>>8, a>>8)
	}
}
