package aseprite

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"image"
	"image/color"
	"io"
)

func skipString(raw []byte) []byte {
	if len(raw) < 2 {
		return nil
	}
	n := int(binary.LittleEndian.Uint16(raw))
	if n > len(raw)-2 {
		return nil
	}
	return raw[2+n:]
}

func parseString(raw []byte) string {
	if len(raw) < 2 {
		return ""
	}
	n := int(binary.LittleEndian.Uint16(raw))
	if n > len(raw)-2 {
		n = len(raw) - 2
	}
	return string(raw[2 : 2+n])
}

func parseColor(raw []byte) color.Color {
	if len(raw) < 4 {
		return color.NRGBA{}
	}
	return color.NRGBA{
		R: raw[0],
		G: raw[1],
		B: raw[2],
		A: raw[3],
	}
}

func parseUserData(raw []byte) (data []byte, color color.Color) {
	if len(raw) < 4 {
		return nil, nil
	}
	flags := binary.LittleEndian.Uint32(raw)
	raw = raw[4:]

	if flags&1 != 0 {
		if len(raw) < 2 {
			return
		}
		n := int(binary.LittleEndian.Uint16(raw))
		if n > len(raw)-2 {
			n = len(raw) - 2
		}
		data, raw = raw[2:2+n], raw[2+n:]
	}

	if flags&2 != 0 {
		color = parseColor(raw)
	}

	return
}

func (f *file) parseChunk2019(raw []byte) error {
	if len(raw) < 20 {
		return formatError("palette chunk: truncated header")
	}
	entries := binary.LittleEndian.Uint32(raw[0:])
	lo := binary.LittleEndian.Uint32(raw[4:])

	raw = raw[20:]

	for i := uint32(0); i < entries; i++ {
		if len(raw) < 6 {
			return formatError("palette chunk: truncated at entry %d", i)
		}
		flags := binary.LittleEndian.Uint16(raw)
		index := uint64(lo) + uint64(i)
		if index < uint64(len(f.palette)) {
			f.palette[index] = parseColor(raw[2:])
		}
		raw = raw[6:]

		if flags&1 != 0 {
			raw = skipString(raw)
			if raw == nil && i+1 < entries {
				return formatError("palette chunk: truncated entry name at %d", i)
			}
		}
	}

	return nil
}

// https://github.com/aseprite/aseprite/blob/main/docs/ase-file-specs.md#old-palette-chunk-0x0011
func (f *file) parseChunk0011(raw []byte) error {
	if len(raw) < 2 {
		return formatError("old palette chunk 0x0011: truncated")
	}
	packets := binary.LittleEndian.Uint16(raw)
	raw = raw[2:]

	currentIndex := 0

	for i := 0; i < int(packets); i++ {
		if len(raw) < 2 {
			return formatError("old palette chunk 0x0011: truncated packet header at %d", i)
		}
		skip := int(raw[0])
		currentIndex += skip

		n := int(raw[1])
		if n == 0 {
			n = 256
		}
		raw = raw[2:]

		for j := 0; j < n && currentIndex < len(f.palette); j++ {
			if len(raw) < 3 {
				return formatError("old palette chunk 0x0011: truncated packet %d", i)
			}
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
	if len(raw) < 2 {
		return formatError("old palette chunk 0x0004: truncated")
	}
	packets := binary.LittleEndian.Uint16(raw)
	raw = raw[2:]

	currentIndex := 0

	for i := 0; i < int(packets); i++ {
		if len(raw) < 2 {
			return formatError("old palette chunk 0x0004: truncated packet header at %d", i)
		}
		skip := int(raw[0])
		currentIndex += skip

		n := int(raw[1])
		if n == 0 {
			n = 256
		}
		raw = raw[2:]

		for j := 0; j < n && currentIndex < len(f.palette); j++ {
			if len(raw) < 3 {
				return formatError("old palette chunk 0x0004: truncated packet %d", i)
			}
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
	if len(f.frames) == 0 {
		return nil
	}

	var chunk0004 []byte
	var chunk0004Index int
	var chunk0011 []byte
	var chunk0011Index int
	found2019 := false

	for i, ch := range f.frames[0].chunks {
		if ch.typ == 0x2019 {
			if err := f.parseChunk2019(ch.raw); err != nil {
				return chunkError(ch, 0, i, err)
			}
			found2019 = true
			break
		}
		if ch.typ == 0x0004 {
			chunk0004 = ch.raw
			chunk0004Index = i
		}
		if ch.typ == 0x0011 {
			chunk0011 = ch.raw
			chunk0011Index = i
		}
	}

	if !found2019 {
		if chunk0004 != nil {
			if err := f.parseChunk0004(chunk0004); err != nil {
				return chunkError(f.frames[0].chunks[chunk0004Index], 0, chunk0004Index, err)
			}
		} else if chunk0011 != nil {
			if err := f.parseChunk0011(chunk0011); err != nil {
				return chunkError(f.frames[0].chunks[chunk0011Index], 0, chunk0011Index, err)
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
				return chunkError(ch, 0, i, err)
			}

			if i < len(chunks)-1 {
				if ch2 := chunks[i+1]; ch2.typ == 0x2020 {
					l.data, _ = parseUserData(ch2.raw)
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

func (f *file) parseChunk2005(frame int, raw []byte) (*cel, error) {
	if len(raw) < 16 {
		return nil, formatError("cel chunk: truncated header")
	}
	layer := binary.LittleEndian.Uint16(raw)
	xpos := int(int16(binary.LittleEndian.Uint16(raw[2:])))
	ypos := int(int16(binary.LittleEndian.Uint16(raw[4:])))
	opacity := raw[6]
	celtype := binary.LittleEndian.Uint16(raw[7:])

	if int(layer) >= len(f.layers) {
		return nil, formatError("cel chunk: layer index %d out of range (%d layers)", layer, len(f.layers))
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
		if len(raw) < 4 {
			return nil, formatError("cel chunk: truncated image header")
		}
		width := int(binary.LittleEndian.Uint16(raw))
		height := int(binary.LittleEndian.Uint16(raw[2:]))
		pix := raw[4:]
		if len(pix) < celPixelBytes(width, height, f.bpp) {
			return nil, formatError("cel chunk: image needs %d bytes, %d present", celPixelBytes(width, height, f.bpp), len(pix))
		}
		bounds := image.Rect(xpos, ypos, xpos+width, ypos+height)
		cel := f.makeCel(f, bounds, opacity, pix[:celPixelBytes(width, height, f.bpp)])
		f.frames[frame].cels[layer] = cel
	case 1: // linked cel
		if len(raw) < 2 {
			return nil, formatError("cel chunk: truncated linked cel")
		}
		srcFrame := int(binary.LittleEndian.Uint16(raw))
		if srcFrame >= len(f.frames) {
			return nil, formatError("cel chunk: linked frame %d out of range (%d frames)", srcFrame, len(f.frames))
		}
		srcCel := f.frames[srcFrame].cels[layer]
		f.frames[frame].cels[layer] = srcCel
	case 2: // compressed image
		if len(raw) < 4 {
			return nil, formatError("cel chunk: truncated image header")
		}
		width := int(binary.LittleEndian.Uint16(raw))
		height := int(binary.LittleEndian.Uint16(raw[2:]))
		zr, err := zlib.NewReader(bytes.NewReader(raw[4:]))
		if err != nil {
			return nil, formatError("cel chunk: invalid zlib stream: %v", err)
		}
		pix, err := io.ReadAll(zr)
		if err != nil {
			return nil, formatError("cel chunk: zlib stream: %v", err)
		}
		if len(pix) < celPixelBytes(width, height, f.bpp) {
			return nil, formatError("cel chunk: compressed image needs %d bytes, %d decoded", celPixelBytes(width, height, f.bpp), len(pix))
		}
		bounds := image.Rect(xpos, ypos, xpos+width, ypos+height)
		cel := f.makeCel(f, bounds, opacity, pix[:celPixelBytes(width, height, f.bpp)])
		f.frames[frame].cels[layer] = cel
	default:
		return nil, formatError("cel chunk: unsupported cel type %d", celtype)
	}

	return &f.frames[frame].cels[layer], nil
}

// celPixelBytes is the number of bytes a width x height cel payload must
// contain at the given bit depth.
func celPixelBytes(width, height int, bpp uint16) int {
	if width <= 0 || height <= 0 {
		return 0
	}
	return width * height * int(bpp/8)
}

func (f *file) initCels() error {
	for i := range f.frames {
		chunks := f.frames[i].chunks
		for j, ch := range chunks {
			if ch.typ == 0x2005 {
				cel, err := f.parseChunk2005(i, ch.raw)
				if err != nil {
					return chunkError(ch, i, j, err)
				} else if cel != nil && j < (len(chunks)-1) {
					// user data chunk
					if ch2 := chunks[j+1]; ch2.typ == 0x2020 {
						cel.data, _ = parseUserData(ch2.raw)
					}
				}
			}
		}
	}

	return nil
}

func parseTag(t *Tag, raw []byte) ([]byte, error) {
	if len(raw) < 19 {
		return nil, formatError("tags chunk: truncated tag header")
	}
	t.Lo = binary.LittleEndian.Uint16(raw)
	t.Hi = binary.LittleEndian.Uint16(raw[2:])
	t.LoopDirection = LoopDirection(raw[4])
	t.Repeat = binary.LittleEndian.Uint16(raw[5:])
	nameRaw := raw[17:]
	if len(nameRaw) < 2 {
		return nil, formatError("tags chunk: truncated tag name")
	}
	nameLen := int(binary.LittleEndian.Uint16(nameRaw))
	if nameLen > len(nameRaw)-2 {
		nameLen = len(nameRaw) - 2
	}
	t.Name = string(nameRaw[2 : 2+nameLen])
	return raw[19+nameLen:], nil
}

func (f *file) buildTags() ([]Tag, error) {
	if len(f.frames) == 0 {
		return nil, nil
	}
	for i, chunk := range f.frames[0].chunks {
		if chunk.typ == 0x2018 {
			raw := chunk.raw
			if len(raw) < 10 {
				return nil, chunkError(chunk, 0, i, formatError("tags chunk: truncated header"))
			}
			ntags := int(binary.LittleEndian.Uint16(raw))
			tags := make([]Tag, ntags)
			raw = raw[10:]
			for k := range tags {
				var err error
				if raw, err = parseTag(&tags[k], raw); err != nil {
					return nil, chunkError(chunk, 0, i, err)
				}
			}
			return tags, nil
		}
	}

	return nil, nil
}

func parseSlice(s *Slice, flags uint32, raw []byte) ([]byte, error) {
	// framenum := binary.LittleEndian.Uint32(raw)
	if len(raw) < 20 {
		return nil, formatError("slice chunk: truncated slice")
	}
	x := int32(binary.LittleEndian.Uint32(raw[4:]))
	y := int32(binary.LittleEndian.Uint32(raw[8:]))
	w := binary.LittleEndian.Uint32(raw[12:])
	h := binary.LittleEndian.Uint32(raw[16:])
	raw = raw[20:]

	var cx, cy int32
	var cw, ch uint32

	if flags&1 != 0 {
		if len(raw) < 16 {
			return nil, formatError("slice chunk: truncated 9-slice bounds")
		}
		cx = int32(binary.LittleEndian.Uint32(raw))
		cy = int32(binary.LittleEndian.Uint32(raw[4:]))
		cw = binary.LittleEndian.Uint32(raw[8:])
		ch = binary.LittleEndian.Uint32(raw[12:])
		raw = raw[16:]
	}

	var px, py int32

	if flags&2 != 0 {
		if len(raw) < 8 {
			return nil, formatError("slice chunk: truncated pivot")
		}
		px = int32(binary.LittleEndian.Uint32(raw))
		py = int32(binary.LittleEndian.Uint32(raw[4:]))
		raw = raw[8:]
	}

	s.Bounds = image.Rect(int(x), int(y), int(x)+int(w), int(y)+int(h))
	s.Center = image.Rect(int(cx), int(cy), int(cx)+int(cw), int(cy)+int(ch))
	s.Pivot = image.Pt(int(px), int(py))

	return raw, nil
}

func (f *file) buildSlices() (slices []Slice, err error) {
	if len(f.frames) == 0 {
		return nil, nil
	}
	chunks := f.frames[0].chunks
	for i, chunk := range chunks {
		if chunk.typ == 0x2022 {
			ofs := len(slices)
			raw := chunk.raw
			if len(raw) < 12 {
				return nil, chunkError(chunk, 0, i, formatError("slice chunk: truncated header"))
			}
			nslices := int(binary.LittleEndian.Uint32(raw))
			flags := binary.LittleEndian.Uint32(raw[4:])
			name := parseString(raw[12:])

			// parse each slice
			if len(raw) < 14+len(name) {
				return nil, chunkError(chunk, 0, i, formatError("slice chunk: truncated name"))
			}
			raw = raw[14+len(name):]
			for k := 0; len(raw) > 0 && k < nslices; k++ {
				var s Slice
				s.Name = name
				var err error
				if raw, err = parseSlice(&s, flags, raw); err != nil {
					return nil, chunkError(chunk, 0, i, err)
				}
				slices = append(slices, s)
			}

			// check for user data chunk
			if i < len(chunks)-1 {
				if ud := chunks[i+1]; ud.typ == 0x2020 {
					data, col := parseUserData(ud.raw)
					data = append([]byte{}, data...) // copy
					for j := ofs; j < len(slices); j++ {
						slices[j].Data = data
						slices[j].Color = col
					}
				}
			}
		}
	}

	return slices, nil
}
