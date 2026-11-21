package aseprite

// This file contains an independent byte-level checker for encoded Aseprite
// files. It parses the raw bytes with the standard library only and never
// calls the package's own reader or encoder helpers, so an encoder bug that
// the reader happens to tolerate is still caught here.

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"image"
	"image/color"
	"io"
	"testing"
)

type aseParser struct {
	t            *testing.T
	raw          []byte
	off          int
	nextLayer    int
	nextSlice    int
	celCounts    map[int]int
	pendingTagUD [][]byte // expected tag user data, consumed positionally
}

func newASEParser(t *testing.T, raw []byte) *aseParser {
	return &aseParser{t: t, raw: raw, celCounts: make(map[int]int)}
}

func u16(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }
func u32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
func s16(b []byte) int16  { return int16(u16(b)) }
func s32(b []byte) int32  { return int32(u32(b)) }

func (p *aseParser) fail(format string, args ...any) {
	p.t.Helper()
	p.t.Fatalf("byte checker: "+format, args...)
}

func (p *aseParser) take(n int) []byte {
	p.t.Helper()
	if p.off+n > len(p.raw) {
		p.fail("unexpected end of file at %d (want %d bytes)", p.off, n)
	}
	b := p.raw[p.off : p.off+n]
	p.off += n
	return b
}

func (p *aseParser) readString() string {
	n := u16(p.take(2))
	return string(p.take(int(n)))
}

func nrgbaBytes(c color.Color) [4]byte {
	r, g, b, a := c.RGBA()
	return [4]byte{byte(r >> 8), byte(g >> 8), byte(b >> 8), byte(a >> 8)}
}

// expectedPixelStream re-derives the on-disk pixel bytes directly from the
// input image, independently of the encoder code.
func expectedPixelStream(t *testing.T, img image.Image) []byte {
	t.Helper()
	switch v := img.(type) {
	case *image.Paletted:
		return append([]byte(nil), v.Pix...)
	case *image.Gray16:
		out := make([]byte, len(v.Pix))
		for i := 0; i < len(v.Pix); i += 2 {
			word := binary.BigEndian.Uint16(v.Pix[i:])
			out[i] = byte(word)
			out[i+1] = byte(word >> 8)
		}
		return out
	case *image.NRGBA:
		return append([]byte(nil), v.Pix...)
	default:
		t.Fatalf("unexpected image type %T", img)
		return nil
	}
}

// checkASEBytes independently parses raw and verifies every field against the
// source document and the Aseprite file format spec.
func checkASEBytes(t *testing.T, raw []byte, doc *Sprite, compressed bool) {
	t.Helper()
	p := newASEParser(t, raw)

	// ---- File header (128 bytes) ----
	hdr := p.take(128)
	if u32(hdr[0:]) != uint32(len(raw)) {
		p.fail("file size field %d != actual %d", u32(hdr[0:]), len(raw))
	}
	if u16(hdr[4:]) != 0xA5E0 {
		p.fail("bad header magic %04x", u16(hdr[4:]))
	}
	if u16(hdr[6:]) != uint16(len(doc.Frames)) {
		p.fail("frame count mismatch")
	}
	if u16(hdr[8:]) != uint16(doc.Width) || u16(hdr[10:]) != uint16(doc.Height) {
		p.fail("canvas dimensions mismatch")
	}
	if u16(hdr[12:]) != uint16(doc.BPP) {
		p.fail("color depth mismatch: %d", u16(hdr[12:]))
	}
	if u32(hdr[14:]) != 1 {
		p.fail("header flags must be 1 (layer opacity valid), got %d", u32(hdr[14:]))
	}
	if u16(hdr[18:]) != uint16(doc.Frames[0].Duration.Milliseconds()) {
		p.fail("header speed mismatch")
	}
	if u32(hdr[20:]) != 0 || u32(hdr[24:]) != 0 {
		p.fail("header DWORDs at 20/24 must be zero")
	}
	if hdr[28] != byte(doc.TransparentIndex) {
		p.fail("transparent index mismatch")
	}
	if hdr[29] != 0 || hdr[30] != 0 || hdr[31] != 0 {
		p.fail("ignored bytes 29-31 must be zero")
	}
	wantNColors := uint16(256)
	if doc.BPP == Indexed8 {
		wantNColors = uint16(len(doc.Palette))
	}
	if u16(hdr[32:]) != wantNColors {
		p.fail("ncolors mismatch: got %d want %d", u16(hdr[32:]), wantNColors)
	}
	if hdr[34] != 1 || hdr[35] != 1 {
		p.fail("pixel ratio must be 1:1")
	}
	if s16(hdr[36:]) != 0 || s16(hdr[38:]) != 0 || u16(hdr[40:]) != 0 || u16(hdr[42:]) != 0 {
		p.fail("grid fields must be zero")
	}
	for i := 44; i < 128; i++ {
		if hdr[i] != 0 {
			p.fail("reserved header byte %d must be zero, got %d", i, hdr[i])
		}
	}

	// ---- Frames ----
	for fi := range doc.Frames {
		frameStart := p.off
		fh := p.take(16)
		frameLen := int(u32(fh[0:]))
		if u16(fh[4:]) != 0xF1FA {
			p.fail("frame %d bad magic", fi)
		}
		if u16(fh[6:]) != 0xFFFF {
			p.fail("frame %d old chunk count must be 0xFFFF, got %d", fi, u16(fh[6:]))
		}
		if u16(fh[8:]) != uint16(doc.Frames[fi].Duration.Milliseconds()) {
			p.fail("frame %d duration mismatch", fi)
		}
		if u16(fh[10:]) != 0 {
			p.fail("frame %d reserved bytes must be zero", fi)
		}
		frameEnd := frameStart + frameLen

		var gotChunks []uint16
		for p.off < frameEnd {
			chunkStart := p.off
			chead := p.take(6)
			chunkLen := int(u32(chead[0:]))
			chunkType := u16(chead[4:])
			if chunkLen < 6 {
				p.fail("chunk length %d too small", chunkLen)
			}
			body := p.take(chunkLen - 6)
			if p.off > frameEnd {
				p.fail("chunk overruns frame %d", fi)
			}
			gotChunks = append(gotChunks, chunkType)
			p.parseChunk(fi, chunkType, body, doc, compressed)
			_ = chunkStart
		}
		if p.off != frameEnd {
			p.fail("frame %d length mismatch: off=%d end=%d", fi, p.off, frameEnd)
		}
		if u32(fh[12:]) != uint32(len(gotChunks)) {
			p.fail("frame %d new chunk count %d != actual %d", fi, u32(fh[12:]), len(gotChunks))
		}
		p.checkFrameOrder(fi, gotChunks, doc)
		if fi == 0 && len(p.pendingTagUD) != 0 {
			p.fail("missing %d tag user data chunks", len(p.pendingTagUD))
		}
	}

	if p.off != len(raw) {
		p.fail("trailing bytes after last frame: %d", len(raw)-p.off)
	}
}

func (p *aseParser) parseChunk(fi int, typ uint16, body []byte, doc *Sprite, compressed bool) {
	switch typ {
	case 0x2019:
		p.parsePalette(body, doc)
	case 0x2018:
		p.parseTags(body, doc)
		// The chunks immediately following are one user data chunk per tag,
		// in tag order. Queue the expected payloads for positional checks.
		p.pendingTagUD = make([][]byte, len(doc.Tags))
		for i := range doc.Tags {
			p.pendingTagUD[i] = doc.Tags[i].Data
		}
	case 0x2004:
		p.parseLayer(body, doc)
	case 0x2022:
		p.parseSlice(body, doc)
	case 0x2005:
		p.parseCel(fi, body, doc, compressed)
	case 0x2020:
		flags := u32(body[0:])
		if flags&^3 != 0 {
			p.fail("user data flags %d use unsupported bits", flags)
		}
		// Tag user data is paired positionally, one chunk per tag.
		if len(p.pendingTagUD) > 0 {
			want := p.pendingTagUD[0]
			p.pendingTagUD = p.pendingTagUD[1:]
			var got string
			if flags&1 != 0 {
				n := int(u16(body[4:]))
				got = string(body[6 : 6+n])
			}
			if got != string(want) {
				p.fail("tag user data positional mismatch: got %q want %q", got, want)
			}
			if flags&2 != 0 {
				p.fail("tag user data must not carry a color")
			}
		}
	default:
		p.fail("unexpected chunk type %04x in frame %d", typ, fi)
	}
}

// checkFrameOrder verifies the canonical chunk ordering used by the encoder
// and matches the modern Aseprite writer.
func (p *aseParser) checkFrameOrder(fi int, types []uint16, doc *Sprite) {
	var want []uint16
	if fi == 0 {
		if doc.BPP == Indexed8 {
			want = append(want, 0x2019)
		}
		if len(doc.Tags) > 0 {
			want = append(want, 0x2018)
			// One user data chunk per tag, unconditionally (possibly empty).
			for range doc.Tags {
				want = append(want, 0x2020)
			}
		}
		for i := range doc.Layers {
			want = append(want, 0x2004)
			if len(doc.Layers[i].Data) > 0 {
				want = append(want, 0x2020)
			}
		}
		for i := range doc.Slices {
			want = append(want, 0x2022)
			if len(doc.Slices[i].Data) > 0 || doc.Slices[i].Color != nil {
				want = append(want, 0x2020)
			}
		}
	}
	// Cels are emitted in layer order, one cel (plus optional user data) each.
	cels := append([]SpriteCel(nil), doc.Frames[fi].Cels...)
	for ci := range cels {
		_ = ci
	}
	// Re-derive the ordered layer indexes from the document.
	ordered := make([]int, len(cels))
	for i := range cels {
		ordered[i] = cels[i].Layer
	}
	// simple insertion sort to mirror the encoder's stable sort
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && ordered[j-1] > ordered[j]; j-- {
			ordered[j-1], ordered[j] = ordered[j], ordered[j-1]
		}
	}
	for _, layer := range ordered {
		want = append(want, 0x2005)
		var c *SpriteCel
		for i := range cels {
			if cels[i].Layer == layer {
				c = &cels[i]
				break
			}
		}
		if c.LinkedFrame < 0 && len(c.Data) > 0 {
			want = append(want, 0x2020)
		}
	}

	if len(types) != len(want) {
		p.fail("frame %d chunk sequence length mismatch: got %v want %v", fi, types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			p.fail("frame %d chunk order mismatch at %d: got %04x want %04x (got %v want %v)",
				fi, i, types[i], want[i], types, want)
		}
	}
}

func (p *aseParser) parsePalette(body []byte, doc *Sprite) {
	entries := u32(body[0:])
	first := u32(body[4:])
	last := u32(body[8:])
	if entries != uint32(len(doc.Palette)) || first != 0 || last != entries-1 {
		p.fail("palette header mismatch: %d %d %d", entries, first, last)
	}
	if u32(body[12:]) != 0 || u32(body[16:]) != 0 {
		p.fail("palette reserved bytes must be zero")
	}
	pos := 20
	for i := uint32(0); i < entries; i++ {
		entryFlags := u16(body[pos:])
		if entryFlags != 0 {
			p.fail("palette entry %d flags must be 0", i)
		}
		got := [4]byte{body[pos+2], body[pos+3], body[pos+4], body[pos+5]}
		want := nrgbaBytes(doc.Palette[i])
		if got != want {
			p.fail("palette entry %d mismatch: got %v want %v", i, got, want)
		}
		pos += 6
	}
	if pos != len(body) {
		p.fail("palette chunk has %d trailing bytes", len(body)-pos)
	}
}

func (p *aseParser) parseTags(body []byte, doc *Sprite) {
	count := u16(body[0:])
	if int(count) != len(doc.Tags) {
		p.fail("tag count mismatch")
	}
	if u32(body[2:]) != 0 || u32(body[6:]) != 0 {
		p.fail("tags reserved bytes must be zero")
	}
	pos := 10
	for i := range doc.Tags {
		tg := &doc.Tags[i]
		if u16(body[pos:]) != uint16(tg.Lo) || u16(body[pos+2:]) != uint16(tg.Hi) {
			p.fail("tag %d range mismatch", i)
		}
		if body[pos+4] != byte(tg.LoopDirection) {
			p.fail("tag %d direction mismatch", i)
		}
		if u16(body[pos+5:]) != tg.Repeat {
			p.fail("tag %d repeat mismatch", i)
		}
		for j := 7; j < 13; j++ {
			if body[pos+j] != 0 {
				p.fail("tag %d reserved byte %d must be zero", i, j)
			}
		}
		if body[pos+13] != 0 || body[pos+14] != 0 || body[pos+15] != 0 || body[pos+16] != 0 {
			p.fail("tag %d deprecated color/extra must be zero", i)
		}
		n := int(u16(body[pos+17:]))
		name := string(body[pos+19 : pos+19+n])
		if name != tg.Name {
			p.fail("tag %d name mismatch: %q != %q", i, name, tg.Name)
		}
		pos += 19 + n
	}
	if pos != len(body) {
		p.fail("tags chunk has %d trailing bytes", len(body)-pos)
	}
}

func (p *aseParser) parseLayer(body []byte, doc *Sprite) {
	// Layers are parsed in order across frame 0; track index via a small
	// counter stored on the parser.
	li := p.nextLayer
	p.nextLayer++
	if li >= len(doc.Layers) {
		p.fail("more layer chunks than document layers")
	}
	l := &doc.Layers[li]

	flags := u16(body[0:])
	wantFlags := uint16(0)
	if l.Visible {
		wantFlags |= 1
	}
	if l.Editable {
		wantFlags |= 2
	}
	if l.Background {
		wantFlags |= 8
	}
	if l.Reference {
		wantFlags |= 64
	}
	if flags != wantFlags {
		p.fail("layer %d flags mismatch: got %d want %d", li, flags, wantFlags)
	}
	if u16(body[2:]) != 0 { // type: regular image layer
		p.fail("layer %d type must be 0", li)
	}
	if u16(body[4:]) != 0 { // child level
		p.fail("layer %d child level must be 0", li)
	}
	if u16(body[6:]) != 0 || u16(body[8:]) != 0 {
		p.fail("layer %d default size must be 0", li)
	}
	if u16(body[10:]) != l.BlendMode {
		p.fail("layer %d blend mode mismatch", li)
	}
	if body[12] != l.Opacity {
		p.fail("layer %d opacity mismatch", li)
	}
	if body[13] != 0 || body[14] != 0 || body[15] != 0 {
		p.fail("layer %d reserved bytes must be zero", li)
	}
	n := int(u16(body[16:]))
	name := string(body[18 : 18+n])
	if name != l.Name {
		p.fail("layer %d name mismatch: %q != %q", li, name, l.Name)
	}
	if 18+n != len(body) {
		p.fail("layer %d chunk has trailing bytes", li)
	}
}

func (p *aseParser) parseSlice(body []byte, doc *Sprite) {
	si := p.nextSlice
	p.nextSlice++
	if si >= len(doc.Slices) {
		p.fail("more slice chunks than document slices")
	}
	s := &doc.Slices[si]

	nkeys := u32(body[0:])
	flags := u32(body[4:])
	wantFlags := uint32(0)
	if s.HasCenter {
		wantFlags |= 1
	}
	if s.HasPivot {
		wantFlags |= 2
	}
	if nkeys != uint32(len(s.Keys)) || flags != wantFlags {
		p.fail("slice %d header mismatch", si)
	}
	if u32(body[8:]) != 0 {
		p.fail("slice %d reserved DWORD must be zero", si)
	}
	pos := 12
	n := int(u16(body[pos:]))
	name := string(body[pos+2 : pos+2+n])
	if name != s.Name {
		p.fail("slice %d name mismatch: %q != %q", si, name, s.Name)
	}
	pos += 2 + n

	for ki := range s.Keys {
		k := &s.Keys[ki]
		if u32(body[pos:]) != uint32(k.Frame) {
			p.fail("slice %d key %d frame mismatch", si, ki)
		}
		if s32(body[pos+4:]) != int32(k.Bounds.Min.X) || s32(body[pos+8:]) != int32(k.Bounds.Min.Y) {
			p.fail("slice %d key %d origin mismatch", si, ki)
		}
		if u32(body[pos+12:]) != uint32(k.Bounds.Dx()) || u32(body[pos+16:]) != uint32(k.Bounds.Dy()) {
			p.fail("slice %d key %d size mismatch", si, ki)
		}
		pos += 20
		if s.HasCenter {
			if s32(body[pos:]) != int32(k.Center.Min.X) || s32(body[pos+4:]) != int32(k.Center.Min.Y) ||
				u32(body[pos+8:]) != uint32(k.Center.Dx()) || u32(body[pos+12:]) != uint32(k.Center.Dy()) {
				p.fail("slice %d key %d center mismatch", si, ki)
			}
			pos += 16
		}
		if s.HasPivot {
			if s32(body[pos:]) != int32(k.Pivot.X) || s32(body[pos+4:]) != int32(k.Pivot.Y) {
				p.fail("slice %d key %d pivot mismatch", si, ki)
			}
			pos += 8
		}
	}
	if pos != len(body) {
		p.fail("slice %d chunk has trailing bytes", si)
	}
}

func (p *aseParser) parseCel(fi int, body []byte, doc *Sprite, compressed bool) {
	ci := p.celCounts[fi]
	p.celCounts[fi]++

	cels := doc.Frames[fi].Cels
	// Cels are emitted in layer order; find the ci-th ordered cel.
	order := make([]int, len(cels))
	for i := range cels {
		order[i] = cels[i].Layer
	}
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && order[j-1] > order[j]; j-- {
			order[j-1], order[j] = order[j], order[j-1]
		}
	}
	if ci >= len(order) {
		p.fail("more cel chunks than cels in frame %d", fi)
	}
	var c *SpriteCel
	for i := range cels {
		if cels[i].Layer == order[ci] {
			c = &cels[i]
			break
		}
	}

	if u16(body[0:]) != uint16(c.Layer) {
		p.fail("frame %d cel %d layer index mismatch", fi, ci)
	}
	if s16(body[2:]) != int16(c.Bounds.Min.X) || s16(body[4:]) != int16(c.Bounds.Min.Y) {
		p.fail("frame %d cel %d position mismatch", fi, ci)
	}
	if body[6] != c.Opacity {
		p.fail("frame %d cel %d opacity mismatch", fi, ci)
	}
	celType := u16(body[7:])
	if s16(body[9:]) != 0 {
		p.fail("frame %d cel %d z-index must be 0", fi, ci)
	}
	for j := 11; j < 16; j++ {
		if body[j] != 0 {
			p.fail("frame %d cel %d reserved byte %d must be zero", fi, ci, j)
		}
	}

	if c.LinkedFrame >= 0 {
		if celType != 1 {
			p.fail("frame %d cel %d expected linked cel type 1, got %d", fi, ci, celType)
		}
		if len(body) != 18 {
			p.fail("linked cel body length must be 18, got %d", len(body))
		}
		if u16(body[16:]) != uint16(c.LinkedFrame) {
			p.fail("frame %d cel %d linked frame mismatch", fi, ci)
		}
		return
	}

	wantType := uint16(0)
	if compressed {
		wantType = 2
	}
	if celType != wantType {
		p.fail("frame %d cel %d cel type mismatch: got %d want %d", fi, ci, celType, wantType)
	}
	w, h := c.Bounds.Dx(), c.Bounds.Dy()
	if u16(body[16:]) != uint16(w) || u16(body[18:]) != uint16(h) {
		p.fail("frame %d cel %d dimensions mismatch", fi, ci)
	}
	payload := body[20:]
	wantPix := expectedPixelStream(p.t, c.Image)

	if compressed {
		if payload[0] != 0x78 {
			p.fail("frame %d cel %d payload is not zlib", fi, ci)
		}
		zr, err := zlib.NewReader(bytes.NewReader(payload))
		if err != nil {
			p.fail("frame %d cel %d zlib open: %v", fi, ci, err)
		}
		got, err := io.ReadAll(zr)
		if err != nil {
			p.fail("frame %d cel %d zlib read: %v", fi, ci, err)
		}
		if !bytes.Equal(got, wantPix) {
			p.fail("frame %d cel %d decompressed pixels mismatch (got %d want %d bytes)",
				fi, ci, len(got), len(wantPix))
		}
	} else {
		if !bytes.Equal(payload, wantPix) {
			p.fail("frame %d cel %d raw pixels mismatch (got %d want %d bytes)",
				fi, ci, len(payload), len(wantPix))
		}
	}
}
