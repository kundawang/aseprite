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
	"unicode/utf8"
)

// ErrTruncatedUserData reports a user data chunk that ends before a field its
// flags word announces. User data errors are reported on UserData.Err and
// never abort decoding, because user data is independent of pixel data.
var ErrTruncatedUserData = errors.New("aseprite: truncated user data chunk")

func skipString(raw []byte) []byte {
	if len(raw) < 2 {
		return raw[len(raw):]
	}
	n := int(binary.LittleEndian.Uint16(raw))
	if n > len(raw)-2 {
		n = len(raw) - 2
	}
	return raw[2+n:]
}

// parseString reads a length-prefixed string. A truncated string yields the
// bytes that are present rather than a panic, so a malformed chunk cannot
// abort decoding of unrelated pixel data.
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
	return color.NRGBA{
		R: raw[0],
		G: raw[1],
		B: raw[2],
		A: raw[3],
	}
}

// parseUserData parses a user data chunk. It never panics on truncated input:
// whatever fields can be recovered are returned and the problem is reported on
// UserData.Err. Text is kept byte-for-byte even when it is not valid UTF-8, and
// unknown flag bits are preserved in UserData.UnknownFlags.
func parseUserData(raw []byte) UserData {
	u := UserData{
		Present:    true,
		ChunkIndex: -1,
		Raw:        append([]byte(nil), raw...),
	}

	if len(raw) < 4 {
		u.Err = fmt.Errorf("%w: missing flags word", ErrTruncatedUserData)
		return u
	}

	u.Flags = binary.LittleEndian.Uint32(raw)
	u.UnknownFlags = u.Flags &^ 3
	raw = raw[4:]

	if u.Flags&1 != 0 {
		if len(raw) < 2 {
			u.Err = fmt.Errorf("%w: missing text length", ErrTruncatedUserData)
			return u
		}

		n := int(binary.LittleEndian.Uint16(raw))
		raw = raw[2:]

		u.HasText = true
		if n > len(raw) {
			// Keep the bytes that are present. A color, if flagged, cannot be
			// located reliably after a truncated text field.
			u.TextBytes = append([]byte{}, raw...)
			u.Text = string(u.TextBytes)
			u.ValidUTF8 = utf8.Valid(u.TextBytes)
			u.Err = fmt.Errorf("%w: text length %d exceeds %d available bytes", ErrTruncatedUserData, n, len(raw))
			return u
		}

		u.TextBytes = append([]byte{}, raw[:n]...)
		u.Text = string(u.TextBytes)
		u.ValidUTF8 = utf8.Valid(u.TextBytes)
		raw = raw[n:]
	}

	if u.Flags&2 != 0 {
		if len(raw) < 4 {
			u.Err = fmt.Errorf("%w: missing color", ErrTruncatedUserData)
			return u
		}

		u.HasColor = true
		u.Color = color.NRGBA{R: raw[0], G: raw[1], B: raw[2], A: raw[3]}
	}

	return u
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
					l.userData = parseUserData(ch2.raw)
					l.userData.ChunkIndex = i + 1
					l.data = l.userData.TextBytes
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
		return nil, fmt.Errorf("aseprite: frame %d cel: truncated cel header", frame)
	}

	layer := binary.LittleEndian.Uint16(raw)
	xpos := int(binary.LittleEndian.Uint16(raw[2:]))
	ypos := int(binary.LittleEndian.Uint16(raw[4:]))
	opacity := raw[6]
	celtype := binary.LittleEndian.Uint16(raw[7:])

	// Record the cel metadata before deciding whether it is composited so
	// invisible and reference layers are still represented in Frame.Cels.
	meta := Cel{
		Frame:  frame,
		Layer:  int(layer),
		Type:   int(celtype),
		Bounds: image.Rect(xpos, ypos, xpos, ypos),
	}

	if int(layer) >= len(f.layers) {
		return nil, fmt.Errorf("aseprite: frame %d cel: layer %d out of range (have %d layers)", frame, layer, len(f.layers))
	}

	raw = raw[16:]

	// Metadata is still reported for cels that are not composited, so parse
	// the declared dimensions when the cel carries an image header.
	if (celtype == 0 || celtype == 2) && len(raw) >= 4 {
		width := int(binary.LittleEndian.Uint16(raw))
		height := int(binary.LittleEndian.Uint16(raw[2:]))
		meta.Bounds = image.Rect(xpos, ypos, xpos+width, ypos+height)
	}

	// invisible layer
	if f.layers[layer].flags&1 == 0 {
		f.frames[frame].celsInfo = append(f.frames[frame].celsInfo, meta)
		return nil, nil
	}

	// reference layer
	if f.layers[layer].flags&64 != 0 {
		f.frames[frame].celsInfo = append(f.frames[frame].celsInfo, meta)
		return nil, nil
	}

	opacity = byte((int(opacity) * int(f.layers[layer].opacity)) / 255)

	switch celtype {
	case 0: // uncompressed image
		if len(raw) < 4 {
			return nil, fmt.Errorf("aseprite: frame %d layer %d cel: truncated image header", frame, layer)
		}
		width := int(binary.LittleEndian.Uint16(raw))
		height := int(binary.LittleEndian.Uint16(raw[2:]))
		pix := raw[4:]
		bounds := image.Rect(xpos, ypos, xpos+width, ypos+height)
		cel := f.makeCel(f, bounds, opacity, pix)
		f.frames[frame].cels[layer] = cel
		meta.Composited = true
	case 1: // linked cel
		if len(raw) < 2 {
			return nil, fmt.Errorf("aseprite: frame %d layer %d cel: truncated linked cel header", frame, layer)
		}
		srcFrame := int(binary.LittleEndian.Uint16(raw))
		if srcFrame >= len(f.frames) {
			return nil, fmt.Errorf("aseprite: frame %d layer %d cel: linked frame %d out of range", frame, layer, srcFrame)
		}
		srcCel := f.frames[srcFrame].cels[layer]
		f.frames[frame].cels[layer] = srcCel
		meta.Bounds = srcCel.bounds
		meta.Composited = srcCel.image != nil
	case 2: // compressed image
		if len(raw) < 4 {
			return nil, fmt.Errorf("aseprite: frame %d layer %d cel: truncated image header", frame, layer)
		}
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
		meta.Composited = true
	default:
		return nil, errors.New("unsupported cel type")
	}

	f.frames[frame].celsInfo = append(f.frames[frame].celsInfo, meta)
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
				}

				// Attach cel metadata and any user data chunk that follows.
				// The metadata is recorded for every cel, including ones that
				// were not composited.
				k := len(f.frames[i].celsInfo) - 1
				if k >= 0 {
					f.frames[i].celsInfo[k].Chunk = j
				}

				if j < (len(chunks) - 1) {
					if ch2 := chunks[j+1]; ch2.typ == 0x2020 {
						ud := parseUserData(ch2.raw)
						ud.ChunkIndex = j + 1
						if k >= 0 {
							f.frames[i].celsInfo[k].UserData = ud
						}
						if cel != nil {
							cel.data = ud.TextBytes
						}
					}
				}
			}
		}
	}

	return nil
}

func parseTag(t *Tag, raw []byte) []byte {
	if len(raw) < 19 {
		return raw[len(raw):]
	}
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
			if len(raw) < 10 {
				return nil
			}
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
	if len(raw) < 20 {
		return raw[len(raw):]
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
		if len(raw) < 16 {
			return raw[len(raw):]
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
			return raw[len(raw):]
		}
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
			if len(raw) < 12 {
				continue
			}
			nslices := int(binary.LittleEndian.Uint32(raw))
			flags := binary.LittleEndian.Uint32(raw[4:])
			name := parseString(raw[12:])

			// parse each slice
			raw = raw[14+len(name):]
			for j := 0; len(raw) > 0 && j < nslices; j++ {
				var s Slice
				s.Name = name
				s.Index = len(slices)
				s.Chunk = i
				s.UserData.ChunkIndex = -1
				raw = parseSlice(&s, flags, raw)
				slices = append(slices, s)
			}

			// check for user data chunk
			if i < len(chunks)-1 {
				if ud := chunks[i+1]; ud.typ == 0x2020 {
					data := parseUserData(ud.raw)
					data.ChunkIndex = i + 1
					for j := ofs; j < len(slices); j++ {
						slices[j].UserData = cloneUserData(data)
						slices[j].Data = append([]byte(nil), data.TextBytes...)
						slices[j].Color = data.ColorValue()
					}
				}
			}
		}
	}

	return
}
