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

// maxFileSize bounds the declared file size a single sprite may claim. The
// file size is read from untrusted input and used to allocate a buffer, so an
// absurd declaration must be rejected before the allocation is made.
const maxFileSize = 1 << 30 // 1 GiB

type cel struct {
	image image.Image
	mask  image.Uniform
	data  []byte
}

func makeCelImage8(f *file, bounds image.Rectangle, opacity byte, pix []byte) cel {
	img := image.Paletted{
		Pix:     pix,
		Stride:  bounds.Dx(),
		Rect:    bounds,
		Palette: f.palette,
	}

	mask := image.Uniform{color.Alpha{opacity}}

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
	if len(raw) < 13 {
		return errors.New("truncated layer chunk")
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
		return nil, errors.New("truncated chunk header")
	}

	chunkLen := binary.LittleEndian.Uint32(raw)
	c.typ = int(binary.LittleEndian.Uint16(raw[4:]))

	if chunkLen < 6 {
		return nil, fmt.Errorf("invalid chunk length %d: shorter than its header", chunkLen)
	}
	if uint64(chunkLen) > uint64(len(raw)) {
		return nil, fmt.Errorf("chunk length %d exceeds the %d bytes remaining", chunkLen, len(raw))
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
		return nil, errors.New("truncated frame header")
	}

	if magic := binary.LittleEndian.Uint16(raw[4:]); magic != 0xF1FA {
		return nil, errInvalidMagic
	}

	// frameLen := binary.LittleEndian.Uint32(raw[0:])
	oldChunks := binary.LittleEndian.Uint16(raw[6:])
	durationMS := binary.LittleEndian.Uint16(raw[8:])
	newChunks := binary.LittleEndian.Uint32(raw[12:])

	f.dur = time.Millisecond * time.Duration(durationMS)

	nchunks := newChunks
	if nchunks == 0 {
		nchunks = uint32(oldChunks)
	}
	if uint64(nchunks) > uint64(len(raw))/6 {
		return nil, fmt.Errorf("frame declares %d chunks but only %d bytes remain", nchunks, len(raw))
	}

	f.chunks = make([]chunk, 0, nchunks)

	raw = raw[16:]

	var err error
	for i := uint32(0); i < nchunks; i++ {
		var c chunk
		raw, err = c.Read(raw)
		if err != nil {
			return nil, fmt.Errorf("frame chunk %d: %w", i, err)
		}
		f.chunks = append(f.chunks, c)
	}

	return raw, nil
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

// maxSpriteBytes bounds the size of the atlas allocated for a sprite. Sprite
// dimensions and the frame count come from untrusted input, and the atlas is
// allocated before any cel is decoded, so an absurd declaration must be
// rejected up front instead of attempting the allocation.
const maxSpriteBytes = 1 << 30 // 1 GiB

func spriteAtlasBytes(framew, frameh, nframes, bpp int) (int64, error) {
	if framew <= 0 || frameh <= 0 || nframes <= 0 {
		return 0, fmt.Errorf("invalid sprite dimensions %dx%d with %d frames", framew, frameh, nframes)
	}

	bytesPerPixel := int64(bpp) / 8
	if bytesPerPixel <= 0 {
		return 0, fmt.Errorf("invalid color depth %d", bpp)
	}

	// Frames are laid out in a power-of-two atlas, so the number of cells is
	// at most twice the frame count.
	cells := int64(nframes) * 2
	n := cells * int64(framew) * int64(frameh) * bytesPerPixel
	if n <= 0 || n > maxSpriteBytes {
		return 0, fmt.Errorf("sprite dimensions %dx%d with %d frames exceed the %d byte limit", framew, frameh, nframes, maxSpriteBytes)
	}

	return n, nil
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
	nframes := binary.LittleEndian.Uint16(raw[6:])
	if nframes == 0 {
		return 128, errors.New("sprite has no frames")
	}
	f.frames = make([]frame, 0, nframes)
	f.framew = int(binary.LittleEndian.Uint16(raw[8:]))
	f.frameh = int(binary.LittleEndian.Uint16(raw[10:]))
	if f.framew <= 0 || f.frameh <= 0 {
		return 128, fmt.Errorf("invalid sprite dimensions %dx%d", f.framew, f.frameh)
	}
	f.palette = make(color.Palette, binary.LittleEndian.Uint16(raw[32:]))
	f.transparent = raw[28]

	if f.bpp == 8 && len(f.palette) == 0 {
		return 128, errors.New("indexed sprite has an empty palette")
	}
	if len(f.palette) > 0 && int(f.transparent) >= len(f.palette) {
		return 128, fmt.Errorf("transparent color index %d out of range for palette of %d colors", f.transparent, len(f.palette))
	}

	switch f.bpp {
	case 8:
		f.makeCel = makeCelImage8
	case 16:
		f.makeCel = makeCelImage16
	case 32:
		f.makeCel = makeCelImage32
	default:
		return 0, errors.New("invalid color depth")
	}

	if _, err := spriteAtlasBytes(f.framew, f.frameh, int(nframes), int(f.bpp)); err != nil {
		return 128, err
	}

	for i := range f.palette {
		f.palette[i] = color.Black
	}
	if len(f.palette) > 0 {
		f.palette[f.transparent] = color.Transparent
	}

	fileSize := int64(binary.LittleEndian.Uint32(raw))
	if fileSize < 128 || fileSize > maxFileSize {
		return 128, fmt.Errorf("invalid file size %d", fileSize)
	}
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

	// The header's frame count is only a capacity hint; the actual count comes
	// from the data. Re-check the atlas bound for the real number of frames so
	// a file with more frame records than declared cannot force a huge atlas.
	if _, err := spriteAtlasBytes(f.framew, f.frameh, len(f.frames), int(f.bpp)); err != nil {
		return fileSize, err
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
