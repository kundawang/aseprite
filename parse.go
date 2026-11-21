package aseprite

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"io"
)

// skipString advances raw past a length-prefixed UTF-8 string. The two-byte
// length prefix must already be known to be in range.
func skipString(raw []byte) ([]byte, bool) {
	n, ok := u16at(raw, 0)
	if !ok || !at(raw, 2, int(n)) {
		return nil, false
	}
	return raw[2+int(n):], true
}

func parseString(raw []byte) (string, bool) {
	n, ok := u16at(raw, 0)
	if !ok || !at(raw, 2, int(n)) {
		return "", false
	}
	return string(raw[2 : 2+int(n)]), true
}

func parseColor(raw []byte) color.Color {
	return color.NRGBA{
		R: raw[0],
		G: raw[1],
		B: raw[2],
		A: raw[3],
	}
}

func parseUserData(raw []byte) (data []byte, col color.Color, ok bool) {
	flags, ok := u32at(raw, 0)
	if !ok {
		return nil, nil, false
	}
	raw = raw[4:]

	if flags&1 != 0 {
		n, good := u16at(raw, 0)
		if !good || !at(raw, 2, int(n)) {
			return nil, nil, false
		}
		data, raw = raw[2:2+int(n)], raw[2+int(n):]
	}

	if flags&2 != 0 {
		if !at(raw, 0, 4) {
			return nil, nil, false
		}
		col = parseColor(raw)
	}

	return data, col, true
}

func (f *file) parseChunk2019(raw []byte) error {
	entries, ok := u32at(raw, 0)
	if !ok {
		return &syntaxError{msg: "truncated palette chunk", offset: -1}
	}
	lo, ok := u32at(raw, 4)
	if !ok {
		return &syntaxError{msg: "truncated palette chunk", offset: -1}
	}

	raw, ok = sub(raw, 20, len(raw))
	if !ok {
		return &syntaxError{msg: "truncated palette chunk", offset: -1}
	}

	for i := uint32(0); i < entries; i++ {
		flags, good := u16at(raw, 0)
		if !good || !at(raw, 2, 4) {
			return &syntaxError{msg: "truncated palette entry", offset: -1}
		}
		idx := int(lo) + int(i)
		if idx >= 0 && idx < len(f.palette) {
			f.palette[idx] = parseColor(raw[2:])
		}
		raw = raw[6:]

		if flags&1 != 0 {
			var good bool
			raw, good = skipString(raw)
			if !good {
				return &syntaxError{msg: "truncated palette entry name", offset: -1}
			}
		}
	}

	return nil
}

// https://github.com/aseprite/aseprite/blob/main/docs/ase-file-specs.md#old-palette-chunk-0x0011
func (f *file) parseChunk0011(raw []byte) error {
	packets, ok := u16at(raw, 0)
	if !ok {
		return &syntaxError{msg: "truncated old palette chunk", offset: -1}
	}
	raw = raw[2:]

	currentIndex := 0

	for i := 0; i < int(packets); i++ {
		if !at(raw, 0, 2) {
			return &syntaxError{msg: "truncated old palette packet", offset: -1}
		}
		skip := int(raw[0])
		currentIndex += skip

		n := int(raw[1])
		if n == 0 {
			n = 256
		}
		raw = raw[2:]

		if !at(raw, 0, n*3) {
			return &syntaxError{msg: "truncated old palette colors", offset: -1}
		}

		for j := 0; j < n && currentIndex < len(f.palette); j++ {
			f.palette[currentIndex] = color.NRGBA{
				R: raw[0] * 4,
				G: raw[1] * 4,
				B: raw[2] * 4,
				A: 255,
			}
			raw = raw[3:]
			currentIndex++
		}
	}

	return nil
}

// https://github.com/aseprite/aseprite/blob/main/docs/ase-file-specs.md#old-palette-chunk-0x0004
func (f *file) parseChunk0004(raw []byte) error {
	packets, ok := u16at(raw, 0)
	if !ok {
		return &syntaxError{msg: "truncated old palette chunk", offset: -1}
	}
	raw = raw[2:]

	currentIndex := 0

	for i := 0; i < int(packets); i++ {
		if !at(raw, 0, 2) {
			return &syntaxError{msg: "truncated old palette packet", offset: -1}
		}
		skip := int(raw[0])
		currentIndex += skip

		n := int(raw[1])
		if n == 0 {
			n = 256
		}
		raw = raw[2:]

		if !at(raw, 0, n*3) {
			return &syntaxError{msg: "truncated old palette colors", offset: -1}
		}

		for j := 0; j < n && currentIndex < len(f.palette); j++ {
			f.palette[currentIndex] = color.NRGBA{
				R: raw[0],
				G: raw[1],
				B: raw[2],
				A: 255,
			}
			raw = raw[3:]
			currentIndex++
		}
	}

	return nil
}

func (f *file) initPalette() error {
	var chunk0004 []byte
	var chunk0011 []byte
	found2019 := false

	for _, ch := range f.frames[0].chunks {
		if ch.typ == 0x2019 {
			if err := f.parseChunk2019(ch.raw); err != nil {
				return err
			}
			found2019 = true
			break
		}
		if ch.typ == 0x0004 {
			chunk0004 = ch.raw
		}
		if ch.typ == 0x0011 {
			chunk0011 = ch.raw
		}
	}

	if !found2019 {
		if chunk0004 != nil {
			if err := f.parseChunk0004(chunk0004); err != nil {
				return err
			}
		} else if chunk0011 != nil {
			if err := f.parseChunk0011(chunk0011); err != nil {
				return err
			}
		}
	}

	if f.flags&1 != 0 && int(f.transparent) < len(f.palette) {
		f.palette[f.transparent] = color.Transparent
	}

	return nil
}

func (f *file) initLayers() error {
	chunks := f.frames[0].chunks
	for i, ch := range chunks {
		if ch.typ == 0x2004 {
			var l layer
			if err := l.Parse(ch.raw); err != nil {
				return err
			}

			if i < len(chunks)-1 {
				if ch2 := chunks[i+1]; ch2.typ == 0x2020 {
					if data, _, ok := parseUserData(ch2.raw); ok {
						l.data = data
					}
				}
			}

			f.layers = append(f.layers, l)
		}
	}

	nlayers := len(f.layers)
	for i := range f.frames {
		f.frames[i].cels = make([]cel, nlayers)
	}

	return nil
}

// readAllBounded reads exactly want bytes from r into a freshly allocated
// buffer. It rejects streams shorter or longer than want and never holds more
// than want+1 bytes, which is what makes a zlib bomb fail cheaply.
func readAllBounded(r io.Reader, want int64) ([]byte, error) {
	// LimitReader caps the amount ReadAll can retain, so a malicious stream
	// cannot inflate memory past the declared cel dimensions.
	out, err := io.ReadAll(io.LimitReader(r, want+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) != want {
		if int64(len(out)) > want {
			return nil, errors.New("decompressed cel data exceeds declared dimensions")
		}
		return nil, io.ErrUnexpectedEOF
	}
	return out, nil
}

// celSourceBytes is the size of a cel's pixel payload in the file for the
// given dimensions, using the file's bits per pixel.
func (f *file) celSourceBytes(width, height int) (int, bool) {
	return mulInt(int(width)*int(f.bpp)/8, int(height))
}

// celAllocBytes is the size of the pixel buffer that the decoder retains for
// a cel. 16bpp grayscale cels are expanded into a 4 bytes/pixel NRGBA image,
// while 8bpp and 32bpp cels alias or allocate at their source size.
func (f *file) celAllocBytes(width, height int) (int64, bool) {
	ppix := int64(f.bpp) / 8
	if f.bpp == 16 {
		ppix = 4
	}
	p, ok := mulInt64(int64(width)*ppix, int64(height))
	return p, ok
}

func (f *file) parseChunk2005(frame int, ch chunk) (*cel, error) {
	raw := ch.raw
	if !at(raw, 0, 16) {
		return nil, &syntaxError{msg: "truncated cel chunk header", offset: ch.offset}
	}

	layer := int(binary.LittleEndian.Uint16(raw))
	xpos := int(binary.LittleEndian.Uint16(raw[2:]))
	ypos := int(binary.LittleEndian.Uint16(raw[4:]))
	opacity := raw[6]
	celtype := binary.LittleEndian.Uint16(raw[7:])

	if layer < 0 || layer >= len(f.layers) {
		return nil, &syntaxError{msg: "cel references unknown layer", offset: ch.offset}
	}

	// invisible layer
	if f.layers[layer].flags&1 == 0 {
		return nil, nil
	}

	// reference layer
	if f.layers[layer].flags&64 != 0 {
		return nil, nil
	}

	raw = raw[16:]

	opacity = byte((int(opacity) * int(f.layers[layer].opacity)) / 255)

	switch celtype {
	case 0: // uncompressed image
		width, ok := u16at(raw, 0)
		if !ok {
			return nil, &syntaxError{msg: "truncated cel dimensions", offset: ch.offset}
		}
		height, ok := u16at(raw, 2)
		if !ok {
			return nil, &syntaxError{msg: "truncated cel dimensions", offset: ch.offset}
		}
		if width == 0 || height == 0 {
			return nil, &syntaxError{msg: "cel dimensions must be greater than zero", offset: ch.offset}
		}
		want, ok := f.celSourceBytes(int(width), int(height))
		if !ok {
			return nil, &syntaxError{msg: "cel size overflows", offset: ch.offset}
		}
		alloc, ok := f.celAllocBytes(int(width), int(height))
		if !ok {
			return nil, &syntaxError{msg: "cel size overflows", offset: ch.offset}
		}
		// The payload is a sub-slice of the already-budgeted frame buffer;
		// verify it actually holds the declared pixels.
		pix, ok := sub(raw, 4, 4+want)
		if !ok {
			return nil, &syntaxError{msg: "cel pixel data shorter than declared", offset: ch.offset}
		}
		if err := f.chargeCelBytes(alloc, ch.offset); err != nil {
			return nil, err
		}
		bounds, err := rectFrom(xpos, ypos, int(width), int(height))
		if err != nil {
			return nil, err
		}
		c := f.makeCel(f, bounds, opacity, pix)
		f.frames[frame].cels[layer] = c
	case 1: // linked cel
		srcFrame, ok := u16at(raw, 0)
		if !ok {
			return nil, &syntaxError{msg: "truncated linked cel", offset: ch.offset}
		}
		if int(srcFrame) >= len(f.frames) || int(srcFrame) >= frame {
			return nil, &syntaxError{msg: "linked cel references unknown frame", offset: ch.offset}
		}
		f.frames[frame].cels[layer] = f.frames[srcFrame].cels[layer]
	case 2: // compressed image
		width, ok := u16at(raw, 0)
		if !ok {
			return nil, &syntaxError{msg: "truncated cel dimensions", offset: ch.offset}
		}
		height, ok := u16at(raw, 2)
		if !ok {
			return nil, &syntaxError{msg: "truncated cel dimensions", offset: ch.offset}
		}
		if width == 0 || height == 0 {
			return nil, &syntaxError{msg: "cel dimensions must be greater than zero", offset: ch.offset}
		}
		want, ok := f.celSourceBytes(int(width), int(height))
		if !ok {
			return nil, &syntaxError{msg: "cel size overflows", offset: ch.offset}
		}
		alloc, ok := f.celAllocBytes(int(width), int(height))
		if !ok {
			return nil, &syntaxError{msg: "cel size overflows", offset: ch.offset}
		}

		// Enforce the decompressed budget before allocating the output buffer
		// or touching the zlib stream.
		if err := f.chargeCelBytes(alloc, ch.offset); err != nil {
			return nil, err
		}

		zr, err := zlib.NewReader(bytes.NewReader(raw[4:]))
		if err != nil {
			return nil, err
		}
		pix, err := readAllBounded(zr, int64(want))
		zr.Close()
		if err != nil {
			return nil, err
		}
		bounds, err := rectFrom(xpos, ypos, int(width), int(height))
		if err != nil {
			return nil, err
		}
		c := f.makeCel(f, bounds, opacity, pix)
		f.frames[frame].cels[layer] = c
	default:
		return nil, &syntaxError{msg: "unsupported cel type", offset: ch.offset}
	}

	return &f.frames[frame].cels[layer], nil
}

// chargeCelBytes accounts for n decompressed cel bytes against the cel budget.
func (f *file) chargeCelBytes(n, offset int64) error {
	f.celBytes += n
	return checkBudget(int64(f.budget.MaxCelBytes), "MaxCelBytes", "cel",
		f.celBytes, offset)
}

// rectFrom builds a rectangle from a position and size, rejecting integer
// overflow in the max coordinates.
func rectFrom(x, y, w, h int) (image.Rectangle, error) {
	xmax, okx := addInt(x, w)
	ymax, oky := addInt(y, h)
	if !okx || !oky {
		return image.Rectangle{}, errors.New("cel position overflows canvas")
	}
	return image.Rect(x, y, xmax, ymax), nil
}

func (f *file) initCels() error {
	for i := range f.frames {
		chunks := f.frames[i].chunks
		for j, ch := range chunks {
			if ch.typ == 0x2005 {
				c, err := f.parseChunk2005(i, ch)
				if err != nil {
					return err
				} else if c != nil && j < (len(chunks)-1) {
					// user data chunk
					if ch2 := chunks[j+1]; ch2.typ == 0x2020 {
						if data, _, ok := parseUserData(ch2.raw); ok {
							c.data = data
						}
					}
				}
			}
		}
	}

	return nil
}

func parseTag(t *Tag, raw []byte) ([]byte, bool) {
	if !at(raw, 0, 17) {
		return nil, false
	}
	t.Lo = binary.LittleEndian.Uint16(raw)
	t.Hi = binary.LittleEndian.Uint16(raw[2:])
	t.LoopDirection = LoopDirection(raw[4])
	t.Repeat = binary.LittleEndian.Uint16(raw[5:])
	name, ok := parseString(raw[17:])
	if !ok {
		return nil, false
	}
	t.Name = name
	return raw[19+len(t.Name):], true
}

func (f *file) buildTags() []Tag {
	for _, chunk := range f.frames[0].chunks {
		if chunk.typ == 0x2018 {
			raw := chunk.raw
			ntags, ok := u16at(raw, 0)
			if !ok {
				return nil
			}
			tags := make([]Tag, ntags)
			if !at(raw, 0, 10) {
				return nil
			}
			raw = raw[10:]
			for i := range tags {
				var good bool
				raw, good = parseTag(&tags[i], raw)
				if !good {
					return nil
				}
			}
			return tags
		}
	}

	return nil
}

func parseSlice(s *Slice, flags uint32, raw []byte) ([]byte, bool) {
	if !at(raw, 0, 20) {
		return nil, false
	}
	// framenum := binary.LittleEndian.Uint32(raw)
	x := int32(binary.LittleEndian.Uint32(raw[4:]))
	y := int32(binary.LittleEndian.Uint32(raw[8:]))
	w := binary.LittleEndian.Uint32(raw[12:])
	h := binary.LittleEndian.Uint32(raw[16:])
	raw = raw[20:]

	var cx, cy int32
	var cw, ch uint32

	if flags&1 != 0 {
		if !at(raw, 0, 16) {
			return nil, false
		}
		cx = int32(binary.LittleEndian.Uint32(raw))
		cy = int32(binary.LittleEndian.Uint32(raw[4:]))
		cw = binary.LittleEndian.Uint32(raw[8:])
		ch = binary.LittleEndian.Uint32(raw[12:])
		raw = raw[16:]
	}

	var px, py int32

	if flags&2 != 0 {
		if !at(raw, 0, 8) {
			return nil, false
		}
		px = int32(binary.LittleEndian.Uint32(raw))
		py = int32(binary.LittleEndian.Uint32(raw[4:]))
		raw = raw[8:]
	}

	s.Bounds = image.Rect(int(x), int(y), int(x)+int(w), int(y)+int(h))
	s.Center = image.Rect(int(cx), int(cy), int(cx)+int(cw), int(cy)+int(ch))
	s.Pivot = image.Pt(int(px), int(py))

	return raw, true
}

func (f *file) buildSlices() (slices []Slice) {
	chunks := f.frames[0].chunks
	for i, chunk := range chunks {
		if chunk.typ == 0x2022 {
			ofs := len(slices)
			raw := chunk.raw
			nslices, ok := u32at(raw, 0)
			if !ok {
				return slices
			}
			flags, ok := u32at(raw, 4)
			if !ok {
				return slices
			}
			name, ok := parseString(raw[12:])
			if !ok {
				return slices
			}

			// parse each slice
			if !at(raw, 0, 14+len(name)) {
				return slices
			}
			raw = raw[14+len(name):]
			for k := uint32(0); len(raw) > 0 && k < nslices; k++ {
				var s Slice
				s.Name = name
				var good bool
				raw, good = parseSlice(&s, flags, raw)
				if !good {
					return slices
				}
				slices = append(slices, s)
			}

			// check for user data chunk
			if i < len(chunks)-1 {
				if ud := chunks[i+1]; ud.typ == 0x2020 {
					data, col, ok := parseUserData(ud.raw)
					if ok {
						data = append([]byte{}, data...) // copy
						for j := ofs; j < len(slices); j++ {
							slices[j].Data = data
							slices[j].Color = col
						}
					}
				}
			}
		}
	}

	return
}
