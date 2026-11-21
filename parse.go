package aseprite

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
)

func skipString(raw []byte) []byte {
	n := binary.LittleEndian.Uint16(raw)
	return raw[2+n:]
}

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

func (f *file) parseChunk2019(raw []byte) {
	entries := binary.LittleEndian.Uint32(raw[0:])
	lo := binary.LittleEndian.Uint32(raw[4:])

	raw = raw[20:]

	for i := uint32(0); i < entries; i++ {
		flags := binary.LittleEndian.Uint16(raw)
		f.palette[lo+i] = parseColor(raw[2:])
		raw = raw[6:]

		if flags&1 != 0 {
			raw = skipString(raw)
		}
	}
}

func (f *file) initPalette() {
	for _, ch := range f.frames[0].chunks {
		if ch.typ == 0x2019 {
			f.parseChunk2019(ch.raw)
			break
		}
	}

	if f.flags&1 != 0 {
		f.palette[f.transparent] = color.Transparent
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
	if len(raw) < 16 {
		return nil, fmt.Errorf("aseprite: frame %d: cel chunk too short (%d bytes)", frame, len(raw))
	}

	layer := int(binary.LittleEndian.Uint16(raw))
	xpos := int(binary.LittleEndian.Uint16(raw[2:]))
	ypos := int(binary.LittleEndian.Uint16(raw[4:]))
	opacity := raw[6]
	celtype := binary.LittleEndian.Uint16(raw[7:])

	if layer < 0 || layer >= len(f.layers) {
		return nil, fmt.Errorf("aseprite: frame %d: cel references layer %d, but the file has only %d layers", frame, layer, len(f.layers))
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
			return nil, fmt.Errorf("aseprite: frame %d layer %d: image cel chunk too short (%d bytes)", frame, layer, len(raw))
		}
		width := int(binary.LittleEndian.Uint16(raw))
		height := int(binary.LittleEndian.Uint16(raw[2:]))
		pix := raw[4:]
		bounds := image.Rect(xpos, ypos, xpos+width, ypos+height)
		if n := bounds.Dx() * bounds.Dy() * int(f.bpp) / 8; len(pix) < n {
			return nil, fmt.Errorf("aseprite: frame %d layer %d: cel has %d bytes of pixel data, expected %d", frame, layer, len(pix), n)
		}
		cel := f.makeCel(f, bounds, opacity, pix)
		f.frames[frame].cels[layer] = cel
	case 1: // linked cel
		if len(raw) < 2 {
			return nil, fmt.Errorf("aseprite: frame %d layer %d: linked cel chunk too short (%d bytes)", frame, layer, len(raw))
		}
		srcFrame := int(binary.LittleEndian.Uint16(raw))
		// Resolve links after all frames have been parsed so that forward
		// links work and cyclic links are reported instead of recursing
		// forever.
		f.frames[frame].cels[layer] = cel{
			linked:    true,
			linkFrame: srcFrame,
		}
	case 2: // compressed image
		if len(raw) < 4 {
			return nil, fmt.Errorf("aseprite: frame %d layer %d: compressed cel chunk too short (%d bytes)", frame, layer, len(raw))
		}
		width := int(binary.LittleEndian.Uint16(raw))
		height := int(binary.LittleEndian.Uint16(raw[2:]))
		bounds := image.Rect(xpos, ypos, xpos+width, ypos+height)

		if f.deferCels {
			// Do not decompress now. Keep a reference to the compressed
			// bytes in the decoder's input buffer, which is already
			// retained, and decompress only when the frame is rendered.
			f.frames[frame].cels[layer] = cel{
				pix:        raw[4:],
				bounds:     bounds,
				opacity:    opacity,
				compressed: true,
			}
			break
		}

		zr, err := zlib.NewReader(bytes.NewReader(raw[4:]))
		if err != nil {
			return nil, err
		}
		pix, err := io.ReadAll(zr)
		if err != nil {
			return nil, err
		}
		if n := bounds.Dx() * bounds.Dy() * int(f.bpp) / 8; len(pix) < n {
			return nil, fmt.Errorf("aseprite: frame %d layer %d: cel has %d bytes of pixel data, expected %d", frame, layer, len(pix), n)
		}
		cel := f.makeCel(f, bounds, opacity, pix)
		f.frames[frame].cels[layer] = cel
	default:
		return nil, errors.New("unsupported cel type")
	}

	return &f.frames[frame].cels[layer], nil
}

// resolveLinkedCels replaces linked cels with their target cels once every
// frame has been parsed. A link to an empty cel yields an empty cel, which
// renders as a fully transparent frame. Out-of-range links and link cycles
// are reported as errors.
func (f *file) resolveLinkedCels() error {
	for frame := range f.frames {
		for layer := range f.frames[frame].cels {
			if err := f.resolveLinkedCel(frame, layer); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *file) resolveLinkedCel(frame, layer int) error {
	// Follow the chain iteratively and remember the frames on it so that a
	// cycle can be detected without recursion over an attacker-controlled
	// chain length.
	chain := make(map[int]bool)

	for {
		c := &f.frames[frame].cels[layer]
		if !c.linked {
			break
		}

		if chain[frame] {
			return fmt.Errorf("aseprite: frame %d layer %d: linked cel cycle", frame, layer)
		}
		chain[frame] = true

		if c.linkFrame < 0 || c.linkFrame >= len(f.frames) {
			return fmt.Errorf("aseprite: frame %d layer %d: linked cel references frame %d, but the file has only %d frames", frame, layer, c.linkFrame, len(f.frames))
		}

		frame = c.linkFrame
	}

	// frame now holds the resolved cel. Point every cel on the chain at a
	// copy of it, preserving each link's own user data, as if the link had
	// copied the source cel at parse time.
	target := f.frames[frame].cels[layer]

	for link := range chain {
		c := &f.frames[link].cels[layer]
		own := c.data
		*c = target
		if len(own) > 0 {
			c.data = own
		}
	}

	return nil
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

	return f.resolveLinkedCels()
}

func parseTag(t *Tag, raw []byte) []byte {
	t.Lo = int(binary.LittleEndian.Uint16(raw))
	t.Hi = int(binary.LittleEndian.Uint16(raw[2:]))
	t.Flags = int(raw[4])
	t.Repeat = int(binary.LittleEndian.Uint16(raw[5:]))
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
