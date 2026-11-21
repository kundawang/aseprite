package aseprite

import (
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

var opacityMasks = func() (masks []image.Uniform) {
	masks = make([]image.Uniform, 256)
	for i := range masks {
		masks[i] = image.Uniform{color.Alpha{byte(i)}}
	}
	return masks
}()

type cel struct {
	image image.Image
	mask  image.Image
	data  []byte
}

func makeCelImage8(f *file, bounds image.Rectangle, opacity byte, pix []byte) cel {
	img := image.Paletted{
		Pix:     pix,
		Stride:  bounds.Dx(),
		Rect:    bounds,
		Palette: f.palette,
	}

	mask := &opacityMasks[opacity]

	return cel{&img, mask, nil}
}

func makeCelImage16(f *file, bounds image.Rectangle, opacity byte, pix []byte) cel {
	img := image.Gray16{
		Pix:    pix,
		Stride: bounds.Dx() * 2,
		Rect:   bounds,
	}

	mask := &opacityMasks[opacity]

	return cel{&img, mask, nil}
}

func makeCelImage32(f *file, bounds image.Rectangle, opacity byte, pix []byte) cel {
	img := image.NRGBA{
		Pix:    pix,
		Stride: bounds.Dx() * 4,
		Rect:   bounds,
	}

	mask := &opacityMasks[opacity]

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
		return errors.New("layer chunk too short")
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

// parseFrame validates and splits a single raw frame record. raw must start
// at the frame header and may extend past the end of the frame; the
// remainder is returned as rest. frameIdx is only used for error reporting.
func parseFrame(raw []byte, frameIdx int) (fr frame, rest []byte, err error) {
	if len(raw) < 16 {
		return fr, nil, &FrameError{frameIdx, io.ErrUnexpectedEOF}
	}

	if magic := binary.LittleEndian.Uint16(raw[4:]); magic != 0xF1FA {
		return fr, nil, &FrameError{frameIdx, errInvalidMagic}
	}

	frameLen := int(binary.LittleEndian.Uint32(raw[0:]))
	if frameLen < 16 || frameLen > len(raw) {
		return fr, nil, &FrameError{frameIdx, fmt.Errorf("invalid frame length %d (%d bytes remaining)", frameLen, len(raw))}
	}

	oldChunks := binary.LittleEndian.Uint16(raw[6:])
	durationMS := binary.LittleEndian.Uint16(raw[8:])
	newChunks := binary.LittleEndian.Uint32(raw[12:])

	fr.dur = time.Millisecond * time.Duration(durationMS)

	nchunks := int(newChunks)
	if nchunks == 0 {
		nchunks = int(oldChunks)
	}

	if nchunks > (frameLen-16)/6 {
		return fr, nil, &FrameError{frameIdx, fmt.Errorf("chunk count %d exceeds frame length %d", nchunks, frameLen)}
	}

	fr.chunks = make([]chunk, 0, nchunks)

	pos := 16
	for i := 0; i < nchunks; i++ {
		chunkLen := int(binary.LittleEndian.Uint32(raw[pos:]))
		if chunkLen < 6 || pos+chunkLen > frameLen {
			return fr, nil, &FrameError{frameIdx, fmt.Errorf("chunk %d: invalid chunk length %d", i, chunkLen)}
		}
		fr.chunks = append(fr.chunks, chunk{
			typ: int(binary.LittleEndian.Uint16(raw[pos+4:])),
			raw: raw[pos+6 : pos+chunkLen],
		})
		pos += chunkLen
	}

	if pos != frameLen {
		return fr, nil, &FrameError{frameIdx, fmt.Errorf("%d trailing bytes after last chunk", frameLen-pos)}
	}

	return fr, raw[frameLen:], nil
}

type frame struct {
	dur    time.Duration
	chunks []chunk
	cels   []cel
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

// readHeader parses the 128-byte file header and returns the file size
// declared in it.
func (f *file) readHeader(raw []byte) (int64, error) {
	if magic := binary.LittleEndian.Uint16(raw[4:]); magic != 0xA5E0 {
		return 0, errInvalidMagic
	}

	if pixw, pixh := raw[34], raw[35]; pixw != pixh {
		return 0, errors.New("unsupported pixel ratio")
	}

	f.bpp = binary.LittleEndian.Uint16(raw[12:])
	f.flags = binary.LittleEndian.Uint16(raw[14:])
	f.framew = int(binary.LittleEndian.Uint16(raw[8:]))
	f.frameh = int(binary.LittleEndian.Uint16(raw[10:]))
	f.palette = make(color.Palette, binary.LittleEndian.Uint16(raw[32:]))
	f.transparent = raw[28]

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

	for i := range f.palette {
		f.palette[i] = color.Black
	}
	if int(f.transparent) < len(f.palette) {
		f.palette[f.transparent] = color.Transparent
	}

	return int64(binary.LittleEndian.Uint32(raw)), nil
}

func (f *file) ReadFrom(r io.Reader) (int64, error) {
	var hdr [128]byte

	if n, err := io.ReadFull(r, hdr[:]); err != nil {
		return int64(n), err
	}

	fileSize, err := f.readHeader(hdr[:])
	if err != nil {
		return 128, err
	}

	if fileSize < 128 {
		return 128, errors.New("invalid file size")
	}

	f.frames = make([]frame, 0, binary.LittleEndian.Uint16(hdr[6:]))

	raw := make([]byte, fileSize-128)

	if n, err := io.ReadFull(r, raw); err != nil {
		return int64(128 + n), err
	}

	for len(raw) > 0 {
		var fr frame
		var err error
		if fr, raw, err = parseFrame(raw, len(f.frames)); err != nil {
			return fileSize, err
		}

		f.frames = append(f.frames, fr)
	}

	return fileSize, nil
}

// drawCels composites a frame's cels onto dst. scratch is used as a
// temporary for blend modes and must have the same bounds as dst.
func drawCels(dst, scratch *image.RGBA, layers []layer, cels []cel) {
	framebounds := dst.Bounds()
	transparent := &image.Uniform{color.Transparent}

	draw.Draw(dst, framebounds, transparent, image.Point{}, draw.Src)

	for layer, c := range cels {
		if c.image == nil {
			continue
		}

		src := c.image
		sr := src.Bounds()
		sp := sr.Min

		if mode := layers[layer].blendMode; mode > 0 && int(mode) < len(blend.Modes) {
			draw.Draw(scratch, framebounds, transparent, image.Point{}, draw.Src)
			blend.Blend(scratch, sr.Sub(sp), src, sp, dst, sp, blend.Modes[mode])
			src = scratch
			sp = image.Point{}
		}

		draw.DrawMask(dst, sr, src, sp, c.mask, image.Point{}, draw.Over)
	}
}

func (f *file) buildAtlas() (atlas draw.Image, framesr []image.Rectangle) {
	var atlasr image.Rectangle
	atlasr, framesr = makeAtlasFrames(len(f.frames), f.framew, f.frameh)
	atlas = image.NewRGBA(atlasr)

	framebounds := image.Rect(0, 0, f.framew, f.frameh)

	dstblend := image.NewRGBA(framebounds)
	dst := image.NewRGBA(framebounds)

	for i, fr := range f.frames {
		drawCels(dst, dstblend, f.layers, fr.cels)
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
