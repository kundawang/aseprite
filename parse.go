package aseprite

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"image"
	"image/color"
	"io"
)

// binary helpers. Each takes the current context so a caller that forgets a
// bounds check still produces a located, categorised error rather than a
// runtime panic that cannot be attributed to a frame, layer or chunk.

func need(ctx context, field string, raw []byte, n int) error {
	if len(raw) < n {
		return ctx.err(ErrTruncated, "%s: need %d bytes, have %d", field, n, len(raw))
	}
	return nil
}

func u16(ctx context, field string, raw []byte) (uint16, error) {
	if err := need(ctx, field, raw, 2); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(raw), nil
}

func u32(ctx context, field string, raw []byte) (uint32, error) {
	if err := need(ctx, field, raw, 4); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(raw), nil
}

// parseString reads a length-prefixed UTF-8 string. It returns the string and
// the bytes that follow it.
func parseString(ctx context, field string, raw []byte) (string, []byte, error) {
	n, err := u16(ctx, field+" length", raw)
	if err != nil {
		return "", nil, err
	}
	if err := need(ctx, field, raw[2:], int(n)); err != nil {
		return "", nil, err
	}
	return string(raw[2 : 2+n]), raw[2+n:], nil
}

func parseColor(ctx context, field string, raw []byte) (color.Color, error) {
	if err := need(ctx, field, raw, 4); err != nil {
		return nil, err
	}
	return color.NRGBA{
		R: raw[0],
		G: raw[1],
		B: raw[2],
		A: raw[3],
	}, nil
}

// parseUserData parses a user data chunk body.
func parseUserData(ctx context, raw []byte) (data []byte, col color.Color, err error) {
	flags, err := u32(ctx, "user data flags", raw)
	if err != nil {
		return nil, nil, err
	}
	raw = raw[4:]

	if flags&1 != 0 {
		var n uint16
		n, err = u16(ctx, "user data text length", raw)
		if err != nil {
			return nil, nil, err
		}
		if err = need(ctx, "user data text", raw[2:], int(n)); err != nil {
			return nil, nil, err
		}
		// Own a copy: the returned sprite must not alias the input buffer.
		data = append([]byte(nil), raw[2:2+n]...)
		raw = raw[2+n:]
	}

	if flags&2 != 0 {
		col, err = parseColor(ctx, "user data color", raw)
		if err != nil {
			return nil, nil, err
		}
	}

	return data, col, nil
}

func (f *file) parseChunk2019(raw []byte, ctx context) error {
	entries, err := u32(ctx, "palette entry count", raw)
	if err != nil {
		return err
	}
	lo, err := u32(ctx, "palette first index", raw[4:])
	if err != nil {
		return err
	}

	if err := need(ctx, "palette header", raw, 20); err != nil {
		return err
	}
	raw = raw[20:]

	if uint64(lo)+uint64(entries) > uint64(len(f.palette)) {
		return ctx.err(ErrMalformed, "palette entries [%d,%d) exceed the %d-colour palette",
			lo, uint64(lo)+uint64(entries), len(f.palette))
	}

	for i := uint32(0); i < entries; i++ {
		if err := need(ctx, "palette entry", raw, 6); err != nil {
			return err
		}
		flags := binary.LittleEndian.Uint16(raw)
		col, err := parseColor(ctx, "palette entry color", raw[2:])
		if err != nil {
			return err
		}
		f.palette[lo+i] = col
		raw = raw[6:]

		if flags&1 != 0 {
			var n uint16
			n, err = u16(ctx, "palette entry name length", raw)
			if err != nil {
				return err
			}
			if err = need(ctx, "palette entry name", raw[2:], int(n)); err != nil {
				return err
			}
			raw = raw[2+n:]
		}
	}

	return nil
}

func (f *file) initPalette() error {
	for i, ch := range f.frames[0].chunks {
		if ch.typ == 0x2019 {
			ctx := newContext().frameAt(0, ch.offset).chunkAt(i, ch.offset)
			if err := f.parseChunk2019(ch.raw, ctx); err != nil {
				return err
			}
			break
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
			ctx := newContext().frameAt(0, ch.offset).chunkAt(i, ch.offset)
			if err := l.Parse(ch.raw, ctx); err != nil {
				return err
			}

			if i < len(chunks)-1 {
				if ch2 := chunks[i+1]; ch2.typ == 0x2020 {
					uctx := ctx.chunkAt(i+1, ch2.offset)
					data, _, err := parseUserData(uctx, ch2.raw)
					if err != nil {
						return err
					}
					l.data = data
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

func (f *file) parseChunk2005(frame int, raw []byte, ctx context) (*cel, error) {
	layer, err := u16(ctx, "cel layer index", raw)
	if err != nil {
		return nil, err
	}
	xpos, err := u16(ctx, "cel x", raw[2:])
	if err != nil {
		return nil, err
	}
	ypos, err := u16(ctx, "cel y", raw[4:])
	if err != nil {
		return nil, err
	}
	if err := need(ctx, "cel header", raw, 16); err != nil {
		return nil, err
	}
	opacity := raw[6]
	celtype := binary.LittleEndian.Uint16(raw[7:])

	if int(layer) >= len(f.layers) {
		return nil, ctx.layerAt(int(layer)).err(ErrMalformed,
			"cel references layer %d, but the file defines %d layers", layer, len(f.layers))
	}

	ctx = ctx.layerAt(int(layer))

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
		return f.parseCelPixels(frame, layer, xpos, ypos, opacity, raw, ctx, false)
	case 1: // linked cel
		src, err := u16(ctx, "linked cel source frame", raw)
		if err != nil {
			return nil, err
		}
		if int(src) >= len(f.frames) {
			return nil, ctx.err(ErrMalformed,
				"linked cel references frame %d, but the file has %d frames", src, len(f.frames))
		}
		srcCel := f.frames[src].cels[layer]
		f.frames[frame].cels[layer] = srcCel
		return &f.frames[frame].cels[layer], nil
	case 2: // compressed image
		return f.parseCelPixels(frame, layer, xpos, ypos, opacity, raw, ctx, true)
	default:
		return nil, ctx.err(ErrUnsupported, "cel type %d", celtype)
	}
}

// parseCelPixels parses the dimensions and pixel buffer common to both the
// compressed and uncompressed cel types.
func (f *file) parseCelPixels(frame int, layer, xpos, ypos uint16, opacity byte, raw []byte, ctx context, compressed bool) (*cel, error) {
	width, err := u16(ctx, "cel width", raw)
	if err != nil {
		return nil, err
	}
	height, err := u16(ctx, "cel height", raw[2:])
	if err != nil {
		return nil, err
	}
	if err := need(ctx, "cel dimensions", raw, 4); err != nil {
		return nil, err
	}
	raw = raw[4:]

	bounds := image.Rect(int(xpos), int(ypos), int(xpos)+int(width), int(ypos)+int(height))

	bytesPerPixel := int64(f.bpp) / 8
	want := int64(width) * int64(height) * bytesPerPixel
	if want > maxCelBytes {
		return nil, ctx.err(ErrUnsupported, "cel of %dx%d pixels needs %d bytes, limit is %d",
			width, height, want, int64(maxCelBytes))
	}

	var pix []byte

	if compressed {
		zr, err := zlib.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, ctx.err(ErrMalformed, "cel zlib stream: %v", err)
		}
		// Read at most one byte more than the declared cel can hold, so a
		// compression bomb is reported instead of being inflated in full.
		pix, err = io.ReadAll(io.LimitReader(zr, want+1))
		zr.Close()
		if err != nil {
			return nil, ctx.err(ErrMalformed, "cel zlib stream: %v", err)
		}
		if int64(len(pix)) > want {
			return nil, ctx.err(ErrMalformed, "cel zlib stream exceeds the %d byte limit declared by %dx%d",
				want, width, height)
		}
	} else {
		if int64(len(raw)) < want {
			return nil, ctx.err(ErrTruncated, "uncompressed cel: need %d bytes, have %d", want, len(raw))
		}
		// Copy rather than alias the stream buffer, so the decoded sprite does
		// not share memory with input the caller may reuse or mutate.
		pix = append([]byte(nil), raw[:want]...)
	}

	if int64(len(pix)) != want {
		return nil, ctx.err(ErrMalformed, "cel of %dx%d needs %d pixel bytes, decoded %d",
			width, height, want, len(pix))
	}

	c := f.makeCel(f, bounds, opacity, pix)
	f.frames[frame].cels[layer] = c
	return &f.frames[frame].cels[layer], nil
}

func (f *file) initCels() error {
	for i := range f.frames {
		chunks := f.frames[i].chunks
		for j, ch := range chunks {
			if ch.typ == 0x2005 {
				ctx := newContext().frameAt(i, ch.offset).chunkAt(j, ch.offset)
				cel, err := f.parseChunk2005(i, ch.raw, ctx)
				if err != nil {
					return err
				} else if cel != nil && j < (len(chunks)-1) {
					// user data chunk
					if ch2 := chunks[j+1]; ch2.typ == 0x2020 {
						uctx := ctx.chunkAt(j+1, ch2.offset)
						data, _, err := parseUserData(uctx, ch2.raw)
						if err != nil {
							return err
						}
						cel.data = data
					}
				}
			}
		}
	}

	return nil
}

func parseTag(t *Tag, raw []byte, ctx context) ([]byte, error) {
	lo, err := u16(ctx, "tag first frame", raw)
	if err != nil {
		return nil, err
	}
	hi, err := u16(ctx, "tag last frame", raw[2:])
	if err != nil {
		return nil, err
	}
	if err := need(ctx, "tag header", raw, 19); err != nil {
		return nil, err
	}

	t.Lo = lo
	t.Hi = hi
	t.Flags = uint32(raw[4])
	name, rest, err := parseString(ctx, "tag name", raw[17:])
	if err != nil {
		return nil, err
	}
	t.Name = name
	return rest, nil
}

func (f *file) buildTags() ([]Tag, error) {
	for ci, chunk := range f.frames[0].chunks {
		if chunk.typ == 0x2018 {
			ctx := newContext().frameAt(0, chunk.offset).chunkAt(ci, chunk.offset)
			raw := chunk.raw
			ntags, err := u16(ctx, "tag count", raw)
			if err != nil {
				return nil, err
			}
			if err := need(ctx, "tag chunk header", raw, 10); err != nil {
				return nil, err
			}
			tags := make([]Tag, ntags)
			raw = raw[10:]
			for i := range tags {
				if raw, err = parseTag(&tags[i], raw, ctx); err != nil {
					return nil, err
				}
			}
			if len(raw) > 0 {
				return nil, ctx.err(ErrMalformed, "%d trailing bytes in tag chunk", len(raw))
			}
			return tags, nil
		}
	}

	return nil, nil
}

func parseSlice(s *Slice, flags uint32, raw []byte, ctx context) ([]byte, error) {
	// framenum := binary.LittleEndian.Uint32(raw)
	if err := need(ctx, "slice header", raw, 20); err != nil {
		return nil, err
	}
	x := int32(binary.LittleEndian.Uint32(raw[4:]))
	y := int32(binary.LittleEndian.Uint32(raw[8:]))
	w := binary.LittleEndian.Uint32(raw[12:])
	h := binary.LittleEndian.Uint32(raw[16:])
	raw = raw[20:]

	var cx, cy int32
	var cw, ch uint32

	if flags&1 != 0 {
		if err := need(ctx, "slice center", raw, 16); err != nil {
			return nil, err
		}
		cx = int32(binary.LittleEndian.Uint32(raw))
		cy = int32(binary.LittleEndian.Uint32(raw[4:]))
		cw = binary.LittleEndian.Uint32(raw[8:])
		ch = binary.LittleEndian.Uint32(raw[12:])
		raw = raw[16:]
	}

	var px, py int32

	if flags&2 != 0 {
		if err := need(ctx, "slice pivot", raw, 8); err != nil {
			return nil, err
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

func (f *file) buildSlices() ([]Slice, error) {
	chunks := f.frames[0].chunks
	var slices []Slice
	for i, chunk := range chunks {
		if chunk.typ == 0x2022 {
			ctx := newContext().frameAt(0, chunk.offset).chunkAt(i, chunk.offset)
			ofs := len(slices)
			raw := chunk.raw
			nslices, err := u32(ctx, "slice count", raw)
			if err != nil {
				return nil, err
			}
			flags, err := u32(ctx, "slice flags", raw[4:])
			if err != nil {
				return nil, err
			}
			if err := need(ctx, "slice chunk header", raw, 12); err != nil {
				return nil, err
			}
			name, raw, err := parseString(ctx, "slice name", raw[12:])
			if err != nil {
				return nil, err
			}

			// parse each slice. parseSlice validates that the fixed and
			// optional key fields are present, so an oversized count ends in
			// a located truncation error.
			for j := uint32(0); j < nslices; j++ {
				var s Slice
				s.Name = name
				if raw, err = parseSlice(&s, flags, raw, ctx); err != nil {
					return nil, err
				}
				slices = append(slices, s)
			}
			if len(raw) > 0 {
				return nil, ctx.err(ErrMalformed, "%d trailing bytes in slice chunk", len(raw))
			}

			// check for user data chunk
			if i < len(chunks)-1 {
				if ud := chunks[i+1]; ud.typ == 0x2020 {
					uctx := ctx.chunkAt(i+1, ud.offset)
					data, col, err := parseUserData(uctx, ud.raw)
					if err != nil {
						return nil, err
					}
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
