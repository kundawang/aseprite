package aseprite

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"math"
	"time"

	"github.com/askeladdk/aseprite/internal/blend"
)

var errInvalidMagic = errors.New("invalid magic number")

// ErrInvalidFormat is returned, possibly wrapped, when a sprite file is
// malformed. Errors carry stable context (frame, chunk and layer indices
// relative to the file) so that a failing input can be identified
// reproducibly.
var ErrInvalidFormat = errors.New("invalid aseprite format")

// maxFileSize bounds the payload a file header may declare. The decoder does
// not allocate this in advance (see file.ReadFrom), but rejecting absurd
// sizes keeps corrupted headers from turning into multi-gigabyte reads.
const maxFileSize = 1 << 30

func formatError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidFormat, fmt.Sprintf(format, args...))
}

type cel struct {
	image image.Image
	mask  image.Uniform
	data  []byte
}

// clampIndices returns pix unchanged when every index is inside the palette.
// Otherwise it returns a copy with out-of-range indices remapped to the
// transparent index. Copying only when needed guarantees that cel pixel data
// aliasing the caller's input buffer is never written to.
func (f *file) clampIndices(pix []byte) []byte {
	if len(f.palette) == 0 {
		return pix
	}
	remapped := false
	for _, v := range pix {
		if int(v) >= len(f.palette) {
			remapped = true
			break
		}
	}
	if !remapped {
		return pix
	}
	fallback := 0
	if int(f.transparent) < len(f.palette) {
		fallback = int(f.transparent)
	}
	out := make([]byte, len(pix))
	for i, v := range pix {
		if int(v) >= len(f.palette) {
			v = byte(fallback)
		}
		out[i] = v
	}
	return out
}

func makeCelImage8(f *file, bounds image.Rectangle, opacity byte, pix []byte) cel {
	mask := image.Uniform{color.Alpha{opacity}}

	// An indexed cel without a palette cannot be sampled: image.Paletted.At
	// returns nil for an empty palette and blending would dereference it. The
	// cel is treated as fully transparent instead.
	if len(f.palette) == 0 {
		return cel{nil, mask, nil}
	}

	img := image.Paletted{
		Pix:     f.clampIndices(pix),
		Stride:  bounds.Dx(),
		Rect:    bounds,
		Palette: f.palette,
	}

	return cel{&img, mask, nil}
}

func makeCelImage16(f *file, bounds image.Rectangle, opacity byte, pix []byte) cel {
	img := image.Gray16{
		Pix:    pix,
		Stride: bounds.Dx() * 2,
		Rect:   bounds,
	}

	mask := image.Uniform{color.Alpha{opacity}}

	return cel{&img, mask, nil}
}

func makeCelImage32(f *file, bounds image.Rectangle, opacity byte, pix []byte) cel {
	img := image.NRGBA{
		Pix:    pix,
		Stride: bounds.Dx() * 4,
		Rect:   bounds,
	}

	mask := image.Uniform{color.Alpha{opacity}}

	return cel{&img, mask, nil}
}

type layer struct {
	flags     uint16
	blendMode uint16
	opacity   byte
	data      []byte
}

func (l *layer) Parse(raw []byte) error {
	if len(raw) < 16 {
		return formatError("layer chunk: truncated")
	}
	if typ := binary.LittleEndian.Uint16(raw[2:]); typ == 2 {
		return errors.New("tilemap layers not supported")
	}
	l.flags = binary.LittleEndian.Uint16(raw)
	l.blendMode = binary.LittleEndian.Uint16(raw[10:])
	l.opacity = raw[12]
	return nil
}

type chunk struct {
	typ int
	raw []byte
}

func (c chunk) Reader() io.Reader {
	return bytes.NewReader(c.raw)
}

func (c *chunk) Read(raw []byte) ([]byte, error) {
	if len(raw) < 6 {
		return nil, formatError("chunk header: truncated")
	}
	chunkLen := int(binary.LittleEndian.Uint32(raw))
	c.typ = int(binary.LittleEndian.Uint16(raw[4:]))
	if chunkLen < 6 {
		return nil, formatError("chunk length %d is smaller than its header", chunkLen)
	}
	if chunkLen > len(raw) {
		return nil, formatError("chunk length %d exceeds the %d remaining bytes", chunkLen, len(raw))
	}
	c.raw = raw[6:chunkLen]
	return raw[chunkLen:], nil
}

type frame struct {
	dur    time.Duration
	chunks []chunk
	cels   []cel
}

func (f *frame) Read(raw []byte) ([]byte, error) {
	if len(raw) < 16 {
		return nil, formatError("frame header: truncated")
	}

	if magic := binary.LittleEndian.Uint16(raw[4:]); magic != 0xF1FA {
		return nil, errInvalidMagic
	}

	frameLen := int(binary.LittleEndian.Uint32(raw[0:]))
	if frameLen < 16 {
		return nil, formatError("frame length %d is smaller than its header", frameLen)
	}
	if frameLen > len(raw) {
		return nil, formatError("frame length %d exceeds the %d remaining bytes", frameLen, len(raw))
	}

	oldChunks := binary.LittleEndian.Uint16(raw[6:])
	durationMS := binary.LittleEndian.Uint16(raw[8:])
	newChunks := binary.LittleEndian.Uint32(raw[12:])

	f.dur = time.Millisecond * time.Duration(durationMS)

	nchunks := int(newChunks)
	if nchunks == 0 {
		nchunks = int(oldChunks)
	}
	// Each chunk occupies at least its 6-byte header, so a count that cannot
	// fit in the frame is corrupt. Reject it before allocating.
	if nchunks > (frameLen-16)/6 {
		return nil, formatError("frame declares %d chunks but has room for at most %d", nchunks, (frameLen-16)/6)
	}

	f.chunks = make([]chunk, nchunks)

	body := raw[16:frameLen]
	rest := raw[frameLen:]

	for i := 0; i < nchunks; i++ {
		var c chunk
		var err error
		if body, err = c.Read(body); err != nil {
			return nil, formatError("frame chunk %d: %v", i, err)
		}
		f.chunks[i] = c
	}

	return rest, nil
}

type file struct {
	framew      int
	frameh      int
	flags       uint16
	bpp         uint16
	transparent uint8
	palette     color.Palette
	frames      []frame
	layers      []layer
	makeCel     func(f *file, bounds image.Rectangle, opacity byte, pix []byte) cel
}

func (f *file) ReadFrom(r io.Reader) (int64, error) {
	var hdr [128]byte

	raw := hdr[:]

	if n, err := io.ReadFull(r, raw); err != nil {
		return int64(n), err
	}

	if magic := binary.LittleEndian.Uint16(raw[4:]); magic != 0xA5E0 {
		return 128, errInvalidMagic
	}

	if pixw, pixh := raw[34], raw[35]; pixw != pixh {
		return 128, errors.New("unsupported pixel ratio")
	}

	f.bpp = binary.LittleEndian.Uint16(raw[12:])
	f.flags = binary.LittleEndian.Uint16(raw[14:])
	f.frames = make([]frame, 0, binary.LittleEndian.Uint16(raw[6:]))
	f.framew = int(binary.LittleEndian.Uint16(raw[8:]))
	f.frameh = int(binary.LittleEndian.Uint16(raw[10:]))
	f.palette = make(color.Palette, binary.LittleEndian.Uint16(raw[32:]))
	f.transparent = raw[28]

	switch f.bpp {
	case 8:
		// Indexed sprites sample the palette for every pixel; an empty
		// palette makes image.Paletted.At return nil and every consumer
		// dereference it. Real indexed sprites always carry a palette.
		if len(f.palette) == 0 {
			return 128, formatError("indexed sprite declares an empty palette")
		}
		f.makeCel = makeCelImage8
	case 16:
		f.makeCel = makeCelImage16
	case 32:
		f.makeCel = makeCelImage32
	default:
		return 0, errors.New("invalid color depth")
	}

	for i := range f.palette {
		f.palette[i] = color.Black
	}
	if int(f.transparent) < len(f.palette) {
		f.palette[f.transparent] = color.Transparent
	}

	fileSize := int64(binary.LittleEndian.Uint32(raw))
	if fileSize < 128 {
		return 128, formatError("file size %d is smaller than the 128-byte header", fileSize)
	}
	if fileSize > maxFileSize {
		return 128, formatError("file size %d exceeds the %d-byte limit", fileSize, maxFileSize)
	}

	// Read the payload incrementally instead of allocating the declared file
	// size up front: the header is untrusted and a corrupted size would
	// otherwise exhaust memory before any validation runs.
	raw = make([]byte, fileSize-128)

	if n, err := io.ReadFull(r, raw); err != nil {
		return int64(128 + n), err
	}

	for len(raw) > 0 {
		var fr frame
		var err error
		if raw, err = fr.Read(raw); err != nil {
			return fileSize, err
		}

		f.frames = append(f.frames, fr)
	}

	if len(f.frames) == 0 {
		return fileSize, formatError("sprite contains no frames")
	}

	return fileSize, nil
}

func (f *file) buildAtlas() (atlas draw.Image, framesr []image.Rectangle) {
	var atlasr image.Rectangle
	atlasr, framesr = makeAtlasFrames(len(f.frames), f.framew, f.frameh)

	switch f.bpp {
	case 8:
		atlas = image.NewPaletted(atlasr, f.palette)
	case 16:
		atlas = image.NewGray16(atlasr)
	default:
		atlas = image.NewRGBA(atlasr)
	}

	framebounds := image.Rect(0, 0, f.framew, f.frameh)

	dstblend := image.NewRGBA(framebounds)
	dst := image.NewRGBA(framebounds)

	transparent := &image.Uniform{color.Transparent}

	for i, fr := range f.frames {
		draw.Draw(dst, framebounds, transparent, image.Point{}, draw.Src)
		for layer, c := range fr.cels {
			if c.image == nil {
				continue
			}

			src := c.image
			sr := src.Bounds()
			sp := sr.Min

			if mode := f.layers[layer].blendMode; mode > 0 && int(mode) < len(blend.Modes) {
				draw.Draw(dstblend, framebounds, transparent, image.Point{}, draw.Src)
				blend.Blend(dstblend, sr.Sub(sp), src, sp, dst, sp, blend.Modes[mode])
				src = dstblend
				sp = image.Point{}
			}

			draw.DrawMask(dst, sr, src, sp, &c.mask, image.Point{}, draw.Over)
		}

		draw.Draw(atlas, framesr[i], dst, image.Point{}, draw.Src)
	}

	return
}

func (f *file) buildUserData() []byte {
	n := 0

	for _, l := range f.layers {
		if l.flags&1 != 0 {
			n += len(l.data)
		}
	}

	for _, fr := range f.frames {
		for _, c := range fr.cels {
			n += len(c.data)
		}
	}

	return make([]byte, 0, n)
}

func (f *file) buildLayerData(userdata []byte) [][]byte {
	ld := make([][]byte, 0, len(f.layers))
	for _, l := range f.layers {
		if l.flags&1 != 0 && len(l.data) > 0 {
			ofs := len(userdata)
			userdata = append(userdata, l.data...)
			ld = append(ld, userdata[ofs:])
		}
	}
	return ld
}

func (f *file) buildFrames(framesr []image.Rectangle, userdata []byte) ([]Frame, []byte) {
	frames := make([]Frame, len(f.frames))

	for i, fr := range f.frames {
		frames[i].Duration = fr.dur
		frames[i].Bounds = framesr[i]
		frames[i].Data = make([][]byte, 0, len(fr.cels))
		for _, c := range fr.cels {
			if nd := len(c.data); nd > 0 {
				ofs := len(userdata)
				userdata = append(userdata, c.data...)
				frames[i].Data = append(frames[i].Data, userdata[ofs:])
			}
		}
	}

	return frames, userdata
}

func makeAtlasFrames(nframes, framew, frameh int) (atlasr image.Rectangle, framesr []image.Rectangle) {
	fw, fh := factorPowerOfTwo(nframes)
	if framew > frameh {
		fw, fh = fh, fw
	}

	atlasr = image.Rect(0, 0, fw*framew, fh*frameh)

	for i := 0; i < nframes; i++ {
		x, y := i%fw, i/fw
		framesr = append(framesr, image.Rectangle{
			Min: image.Pt(x*framew, y*frameh),
			Max: image.Pt((x+1)*framew, (y+1)*frameh),
		})
	}

	return
}

// factorPowerOfTwo computes n=a*b, where a, b are powers of two and a >= b.
func factorPowerOfTwo(n int) (a, b int) {
	x := int(math.Ceil(math.Log2(float64(n))))
	a = 1 << (x - x/2)
	b = 1 << (x / 2)
	return
}
