package aseprite

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"io"
	"math"
	"time"

	"github.com/askeladdk/aseprite/internal/blend"
)

var errInvalidMagic = errors.New("invalid magic number")

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
	chunkLen := binary.LittleEndian.Uint32(raw)
	c.typ = int(binary.LittleEndian.Uint16(raw[4:]))
	c.raw = raw[6:chunkLen]
	return raw[chunkLen:], nil
}

type frame struct {
	dur    time.Duration
	chunks []chunk
	cels   []cel
}

func (f *frame) Read(raw []byte) ([]byte, error) {
	if magic := binary.LittleEndian.Uint16(raw[4:]); magic != 0xF1FA {
		return nil, errInvalidMagic
	}

	// frameLen := binary.LittleEndian.Uint32(raw[0:])
	oldChunks := binary.LittleEndian.Uint16(raw[6:])
	durationMS := binary.LittleEndian.Uint16(raw[8:])
	newChunks := binary.LittleEndian.Uint32(raw[12:])

	f.dur = time.Millisecond * time.Duration(durationMS)

	nchunks := int(newChunks)
	if nchunks == 0 {
		nchunks = int(oldChunks)
	}

	f.chunks = make([]chunk, nchunks)

	raw = raw[16:]

	for i := 0; i < nchunks; i++ {
		var c chunk
		raw, _ = c.Read(raw)
		f.chunks[i] = c
	}

	return raw, nil
}

type file struct {
	framew      int
	frameh      int
	flags       uint16
	bpp         uint16
	transparent uint8
	padding     int
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
	f.palette[f.transparent] = color.Transparent

	fileSize := int64(binary.LittleEndian.Uint32(raw))
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

	return fileSize, nil
}

func (f *file) buildAtlas(opts Options) (pages []Page, placements []framePlacement) {
	sizes := make([]image.Point, len(f.frames))
	empty := make([]bool, len(f.frames))
	for i, fr := range f.frames {
		sizes[i] = image.Pt(f.framew, f.frameh)
		for _, c := range fr.cels {
			if c.image != nil {
				empty[i] = false
				break
			}
			empty[i] = true
		}
		if empty[i] {
			sizes[i] = image.Point{}
		}
	}

	placements, pageBounds := packFrames(sizes, opts)
	f.padding = opts.Padding

	// A sprite always exposes at least one usable page even when every
	// frame is empty, so Frame.Page remains a valid index.
	if len(pageBounds) == 0 || pageBounds[0].Empty() {
		pageBounds = []image.Rectangle{image.Rect(0, 0, f.framew, f.frameh)}
	}

	pageImages := make([]draw.Image, len(pageBounds))

	for i, r := range pageBounds {
		switch f.bpp {
		case 8:
			pageImages[i] = image.NewPaletted(r, f.palette)
		case 16:
			pageImages[i] = image.NewGray16(r)
		default:
			pageImages[i] = image.NewRGBA(r)
		}
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

		page := pageImages[placements[i].page]
		if empty[i] {
			continue
		}
		draw.Draw(page, placements[i].content, dst, image.Point{}, draw.Src)
	}

	// Extrude border texels into the padding ring so that linear
	// filtering never samples a neighboring frame or the page edge.
	if opts.Padding > 0 {
		for _, placement := range placements {
			if placement.content.Empty() {
				continue
			}
			extrudePadding(pageImages[placement.page], placement.content, opts.Padding)
		}
	}

	pages = make([]Page, len(pageImages))
	for i, img := range pageImages {
		pages[i] = Page{Image: img, Bounds: pageBounds[i]}
	}

	return
}

// extrudePadding copies the nearest border texel of r into every
// texel of the padding ring r.Inset(-padding). It works in raw pixel
// bytes so that paletted indices and grayscale values are preserved.
func extrudePadding(img draw.Image, r image.Rectangle, padding int) {
	padded := r.Inset(-padding)

	copyRing := func(bounds image.Rectangle, stride, pixelSize int, pix []byte) {
		offset := func(x, y int) int {
			return (y-bounds.Min.Y)*stride + (x-bounds.Min.X)*pixelSize
		}

		for y := padded.Min.Y; y < padded.Max.Y; y++ {
			sy := clampInt(y, r.Min.Y, r.Max.Y-1)
			for x := padded.Min.X; x < padded.Max.X; x++ {
				if r.Min.X <= x && x < r.Max.X && r.Min.Y <= y && y < r.Max.Y {
					continue
				}
				sx := clampInt(x, r.Min.X, r.Max.X-1)
				dst, src := offset(x, y), offset(sx, sy)
				copy(pix[dst:dst+pixelSize], pix[src:src+pixelSize])
			}
		}
	}

	switch a := img.(type) {
	case *image.RGBA:
		copyRing(a.Rect, a.Stride, 4, a.Pix)
	case *image.Gray16:
		copyRing(a.Rect, a.Stride, 2, a.Pix)
	case *image.Paletted:
		copyRing(a.Rect, a.Stride, 1, a.Pix)
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	} else if v > hi {
		return hi
	}
	return v
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

func (f *file) buildFrames(placements []framePlacement, userdata []byte) ([]Frame, []byte) {
	frames := make([]Frame, len(f.frames))

	for i, fr := range f.frames {
		placement := placements[i]
		frames[i].Duration = fr.dur
		frames[i].Page = placement.page
		frames[i].Padding = f.padding
		frames[i].Bounds = placement.content
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

// factorPowerOfTwo computes n=a*b, where a, b are powers of two and a >= b.
func factorPowerOfTwo(n int) (a, b int) {
	x := int(math.Ceil(math.Log2(float64(n))))
	a = 1 << (x - x/2)
	b = 1 << (x / 2)
	return
}
