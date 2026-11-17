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

func parseString(raw []byte) string {
	n := binary.LittleEndian.Uint16(raw)
	return string(raw[2 : 2+n])
}

func parseColor(raw []byte) color.Color {
	return color.NRGBA{
		R: raw[0],
		G: raw[1],
		B: raw[2],
		A: raw[3],
	}
}

func parseUserData(raw []byte) (data []byte, color color.Color) {
	flags := binary.LittleEndian.Uint32(raw)
	raw = raw[4:]

	if flags&1 != 0 {
		n := binary.LittleEndian.Uint16(raw)
		data, raw = raw[2:2+n], raw[2+n:]
	}

	if flags&2 != 0 {
		color = parseColor(raw)
	}

	return
}

// parseChunk2019 applies a new palette chunk (0x2019) to pal.
//
// Only the entries in the [first, last] range are present in the chunk and
// only those entries are overwritten; every other index keeps its inherited
// value. Writes outside the palette and truncated chunks are ignored without
// panicking. Multiple entries targeting the same index are applied in order,
// so the last one wins.
//
// https://github.com/aseprite/aseprite/blob/main/docs/ase-file-specs.md#palette-chunk-0x2019
func parseChunk2019(pal color.Palette, raw []byte) {
	if len(raw) < 20 || len(pal) == 0 {
		return
	}

	first := binary.LittleEndian.Uint32(raw[4:])
	last := binary.LittleEndian.Uint32(raw[8:])
	raw = raw[20:]

	if last < first {
		return
	}

	for i := first; i <= last; i++ {
		// Each entry is a WORD of flags followed by four color bytes.
		if len(raw) < 6 {
			return
		}

		flags := binary.LittleEndian.Uint16(raw)
		if int(i) < len(pal) {
			pal[i] = parseColor(raw[2:])
		}
		raw = raw[6:]

		if flags&1 != 0 {
			if len(raw) < 2 {
				return
			}
			n := int(binary.LittleEndian.Uint16(raw))
			if len(raw) < 2+n {
				return
			}
			raw = raw[2+n:]
		}
	}
}

// applyOldPalettePacket parses the shared packet structure of the old palette
// chunks 0x0004 and 0x0011. rgb converts the three stored components into a
// color. Indices outside the palette and truncated chunks are ignored.
func applyOldPalettePacket(pal color.Palette, raw []byte, rgb func(r, g, b byte) color.NRGBA) []byte {
	if len(raw) < 2 || len(pal) == 0 {
		return nil
	}

	packets := int(binary.LittleEndian.Uint16(raw))
	raw = raw[2:]

	currentIndex := 0

	for i := 0; i < packets; i++ {
		if len(raw) < 2 {
			return nil
		}

		// entries to skip from the previous packet
		currentIndex += int(raw[0])

		n := int(raw[1])
		if n == 0 {
			n = 256
		}
		raw = raw[2:]

		for j := 0; j < n; j++ {
			if len(raw) < 3 {
				return nil
			}

			if currentIndex < len(pal) {
				pal[currentIndex] = rgb(raw[0], raw[1], raw[2])
			}
			raw = raw[3:]
			currentIndex++
		}
	}

	return raw
}

// https://github.com/aseprite/aseprite/blob/main/docs/ase-file-specs.md#old-palette-chunk-0x0011
func parseChunk0011(pal color.Palette, raw []byte) {
	// Components are stored in 6 bits and scaled back to 8 bits.
	applyOldPalettePacket(pal, raw, func(r, g, b byte) color.NRGBA {
		return color.NRGBA{R: r * 4, G: g * 4, B: b * 4, A: 255}
	})
}

// https://github.com/aseprite/aseprite/blob/main/docs/ase-file-specs.md#old-palette-chunk-0x0004
func parseChunk0004(pal color.Palette, raw []byte) {
	applyOldPalettePacket(pal, raw, func(r, g, b byte) color.NRGBA {
		return color.NRGBA{R: r, G: g, B: b, A: 255}
	})
}

// initPalettes builds the effective palette of every frame by applying the
// palette chunks frame by frame, starting from the black palette with the
// transparent index set in the file header.
//
// A frame without a palette chunk inherits the previous frame's palette.
// Within a frame, a 0x2019 chunk takes precedence over the old 0x0004 and
// 0x0011 chunks. Chunks only modify the indices they declare; undeclared
// indices are inherited. The transparent index is forced transparent after
// every frame, so palette updates can never make it opaque.
func (f *file) initPalettes() {
	f.framePalettes = make([]color.Palette, len(f.frames))

	// Only indexed sprites carry meaningful palette data; grayscale and RGBA
	// sprites leave the per-frame palettes unset (nil).
	if f.bpp != 8 || len(f.palette) == 0 {
		return
	}

	current := make(color.Palette, len(f.palette))
	for i := range current {
		current[i] = color.Black
	}
	current[f.transparent] = color.Transparent

	for frame, fr := range f.frames {
		var chunk0004, chunk0011 []byte
		has2019 := false

		for _, ch := range fr.chunks {
			switch ch.typ {
			case 0x2019:
				parseChunk2019(current, ch.raw)
				has2019 = true
			case 0x0004:
				chunk0004 = ch.raw
			case 0x0011:
				chunk0011 = ch.raw
			}
		}

		// Old chunks are ignored when a 0x2019 chunk is present in the frame.
		if !has2019 {
			if chunk0004 != nil {
				parseChunk0004(current, chunk0004)
			} else if chunk0011 != nil {
				parseChunk0011(current, chunk0011)
			}
		}

		// The transparent index is defined by the header and always stays
		// transparent, even when a palette chunk assigns it a color.
		current[f.transparent] = color.Transparent

		f.framePalettes[frame] = append(color.Palette(nil), current...)
	}
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
	layer := binary.LittleEndian.Uint16(raw)
	xpos := int(binary.LittleEndian.Uint16(raw[2:]))
	ypos := int(binary.LittleEndian.Uint16(raw[4:]))
	opacity := raw[6]
	celtype := binary.LittleEndian.Uint16(raw[7:])

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
		width := int(binary.LittleEndian.Uint16(raw))
		height := int(binary.LittleEndian.Uint16(raw[2:]))
		pix := raw[4:]
		bounds := image.Rect(xpos, ypos, xpos+width, ypos+height)
		cel := f.makeCel(f, bounds, opacity, pix)
		f.frames[frame].cels[layer] = cel
	case 1: // linked cel
		srcFrame := int(binary.LittleEndian.Uint16(raw))
		srcCel := f.frames[srcFrame].cels[layer]
		f.frames[frame].cels[layer] = srcCel
	case 2: // compressed image
		width := int(binary.LittleEndian.Uint16(raw))
		height := int(binary.LittleEndian.Uint16(raw[2:]))
		zr, err := zlib.NewReader(bytes.NewReader(raw[4:]))
		if err != nil {
			return nil, err
		}
		pix, err := io.ReadAll(zr)
		if err != nil {
			return nil, err
		}
		bounds := image.Rect(xpos, ypos, xpos+width, ypos+height)
		cel := f.makeCel(f, bounds, opacity, pix)
		f.frames[frame].cels[layer] = cel
	default:
		return nil, errors.New("unsupported cel type")
	}

	return &f.frames[frame].cels[layer], nil
}

func (f *file) initCels() error {
	for i := range f.frames {
		chunks := f.frames[i].chunks
		for j, ch := range chunks {
			if ch.typ == 0x2005 {
				cel, err := f.parseChunk2005(i, ch.raw)
				if err != nil {
					return err
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

func parseTag(t *Tag, raw []byte) []byte {
	t.Lo = binary.LittleEndian.Uint16(raw)
	t.Hi = binary.LittleEndian.Uint16(raw[2:])
	t.LoopDirection = LoopDirection(raw[4])
	t.Repeat = binary.LittleEndian.Uint16(raw[5:])
	t.Name = parseString(raw[17:])
	return raw[19+len(t.Name):]
}

func (f *file) buildTags() []Tag {
	for _, chunk := range f.frames[0].chunks {
		if chunk.typ == 0x2018 {
			raw := chunk.raw
			ntags := binary.LittleEndian.Uint16(raw)
			tags := make([]Tag, ntags)
			raw = raw[10:]
			for i := range tags {
				raw = parseTag(&tags[i], raw)
			}
			return tags
		}
	}

	return nil
}

func parseSlice(s *Slice, flags uint32, raw []byte) []byte {
	// framenum := binary.LittleEndian.Uint32(raw)
	x := int32(binary.LittleEndian.Uint32(raw[4:]))
	y := int32(binary.LittleEndian.Uint32(raw[8:]))
	w := binary.LittleEndian.Uint32(raw[12:])
	h := binary.LittleEndian.Uint32(raw[16:])
	raw = raw[20:]

	var cx, cy int32
	var cw, ch uint32

	if flags&1 != 0 {
		cx = int32(binary.LittleEndian.Uint32(raw))
		cy = int32(binary.LittleEndian.Uint32(raw[4:]))
		cw = binary.LittleEndian.Uint32(raw[8:])
		ch = binary.LittleEndian.Uint32(raw[12:])
		raw = raw[16:]
	}

	var px, py int32

	if flags&2 != 0 {
		px = int32(binary.LittleEndian.Uint32(raw))
		py = int32(binary.LittleEndian.Uint32(raw[4:]))
		raw = raw[8:]
	}

	s.Bounds = image.Rect(int(x), int(y), int(x)+int(w), int(y)+int(h))
	s.Center = image.Rect(int(cx), int(cy), int(cx)+int(cw), int(cy)+int(ch))
	s.Pivot = image.Pt(int(px), int(py))

	return raw
}

func (f *file) buildSlices() (slices []Slice) {
	chunks := f.frames[0].chunks
	for i, chunk := range chunks {
		if chunk.typ == 0x2022 {
			ofs := len(slices)
			raw := chunk.raw
			nslices := int(binary.LittleEndian.Uint32(raw))
			flags := binary.LittleEndian.Uint32(raw[4:])
			name := parseString(raw[12:])

			// parse each slice
			raw = raw[14+len(name):]
			for i := 0; len(raw) > 0 && i < nslices; i++ {
				var s Slice
				s.Name = name
				raw = parseSlice(&s, flags, raw)
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

	return
}
