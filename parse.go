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

// maxCelPixelBytes is the maximum number of uncompressed bytes a single cel
// may declare. It bounds allocations made from dimensions read out of an
// untrusted file and rejects absurd or zip-bomb-like declarations before any
// memory is allocated for them.
const maxCelPixelBytes = 1 << 30 // 1 GiB

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
	if len(raw) < 20 {
		return
	}

	entries := binary.LittleEndian.Uint32(raw[0:])
	lo := binary.LittleEndian.Uint32(raw[4:])

	raw = raw[20:]

	for i := uint32(0); i < entries; i++ {
		if len(raw) < 6 {
			return
		}
		idx := uint64(lo) + uint64(i)
		if idx >= uint64(len(f.palette)) {
			return
		}

		flags := binary.LittleEndian.Uint16(raw)
		f.palette[idx] = parseColor(raw[2:])
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

func (f *file) initPalette() {
	if len(f.frames) == 0 {
		return
	}

	for _, ch := range f.frames[0].chunks {
		if ch.typ == 0x2019 {
			f.parseChunk2019(ch.raw)
			break
		}
	}

	if f.flags&1 != 0 && int(f.transparent) < len(f.palette) {
		f.palette[f.transparent] = color.Transparent
	}
}

func (f *file) initLayers() error {
	if len(f.frames) == 0 {
		return errors.New("sprite has no frames")
	}

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

// celPixelBytes returns the exact number of uncompressed bytes a cel of the
// given dimensions occupies for the file's color depth. It rejects empty and
// absurdly large declarations instead of trusting dimensions from the file.
func celPixelBytes(bpp uint16, width, height int) (int, error) {
	if width <= 0 || height <= 0 {
		return 0, fmt.Errorf("invalid cel dimensions %dx%d", width, height)
	}

	bytesPerPixel := int64(bpp) / 8
	if bytesPerPixel <= 0 {
		return 0, fmt.Errorf("invalid color depth %d", bpp)
	}

	n := int64(width) * int64(height) * bytesPerPixel
	if n <= 0 || n > maxCelPixelBytes {
		return 0, fmt.Errorf("cel dimensions %dx%d at %d bpp exceed the %d byte limit", width, height, bpp, maxCelPixelBytes)
	}

	return int(n), nil
}

// decompressCel inflates exactly want bytes from a zlib stream. The read is
// bounded to want+1 bytes, so a stream that declares a small size cannot cause
// a large allocation. It rejects streams that end early, inflate past the
// declared size, or fail the zlib checksum. The zlib reader is closed on every
// path.
func decompressCel(r io.Reader, want int) ([]byte, error) {
	zr, err := zlib.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("zlib: %w", err)
	}
	defer zr.Close()

	// Reading at most want+1 bytes bounds memory use and is enough to detect
	// data that inflates past the declared size. When the stream is exactly
	// want bytes, ReadAll reaches the end of the stream and verifies the
	// checksum.
	pix, err := io.ReadAll(io.LimitReader(zr, int64(want)+1))
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("truncated pixel data: got %d bytes, want %d", len(pix), want)
		}
		return nil, fmt.Errorf("corrupt compressed pixel data: %w", err)
	}
	if len(pix) < want {
		return nil, fmt.Errorf("truncated pixel data: got %d bytes, want %d", len(pix), want)
	}
	if len(pix) > want {
		return nil, fmt.Errorf("decompressed pixel data exceeds the declared %d bytes", want)
	}

	return pix, nil
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

	if int(layer) >= len(f.layers) {
		return nil, fmt.Errorf("aseprite: frame %d cel: layer %d out of range (have %d layers)", frame, layer, len(f.layers))
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

	celErr := func(err error) error {
		return fmt.Errorf("aseprite: frame %d layer %d cel: %w", frame, layer, err)
	}

	switch celtype {
	case 0: // uncompressed image
		if len(raw) < 4 {
			return nil, celErr(errors.New("truncated cel image header"))
		}

		width := int(binary.LittleEndian.Uint16(raw))
		height := int(binary.LittleEndian.Uint16(raw[2:]))

		want, err := celPixelBytes(f.bpp, width, height)
		if err != nil {
			return nil, celErr(err)
		}

		pix := raw[4:]
		if len(pix) < want {
			return nil, celErr(fmt.Errorf("truncated pixel data: got %d bytes, want %d", len(pix), want))
		}
		if len(pix) > want {
			return nil, celErr(fmt.Errorf("trailing pixel data: got %d bytes, want %d", len(pix), want))
		}

		bounds := image.Rect(xpos, ypos, xpos+width, ypos+height)
		cel := f.makeCel(f, bounds, opacity, pix)
		f.frames[frame].cels[layer] = cel
	case 1: // linked cel
		if len(raw) < 2 {
			return nil, celErr(errors.New("truncated linked cel"))
		}
		srcFrame := int(binary.LittleEndian.Uint16(raw))
		if srcFrame >= len(f.frames) {
			return nil, celErr(fmt.Errorf("linked cel references frame %d out of range (have %d frames)", srcFrame, len(f.frames)))
		}
		srcCel := f.frames[srcFrame].cels[layer]
		f.frames[frame].cels[layer] = srcCel
	case 2: // compressed image
		if len(raw) < 4 {
			return nil, celErr(errors.New("truncated cel image header"))
		}

		width := int(binary.LittleEndian.Uint16(raw))
		height := int(binary.LittleEndian.Uint16(raw[2:]))

		want, err := celPixelBytes(f.bpp, width, height)
		if err != nil {
			return nil, celErr(err)
		}

		compressed := bytes.NewReader(raw[4:])
		pix, err := decompressCel(compressed, want)
		if err != nil {
			return nil, celErr(err)
		}
		if compressed.Len() != 0 {
			return nil, celErr(fmt.Errorf("trailing data: %d bytes after zlib stream", compressed.Len()))
		}

		bounds := image.Rect(xpos, ypos, xpos+width, ypos+height)
		cel := f.makeCel(f, bounds, opacity, pix)
		f.frames[frame].cels[layer] = cel
	default:
		return nil, celErr(errors.New("unsupported cel type"))
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
	if len(f.frames) == 0 {
		return nil
	}

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
	if len(f.frames) == 0 {
		return nil
	}

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
