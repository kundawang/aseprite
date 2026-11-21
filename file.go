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
	image  image.Image
	mask   image.Uniform
	data   []byte
	bounds image.Rectangle
}

func makeCelImage8(f *file, bounds image.Rectangle, opacity byte, pix []byte) cel {
	img := image.Paletted{
		Pix:     pix,
		Stride:  bounds.Dx(),
		Rect:    bounds,
		Palette: f.palette,
	}

	mask := image.Uniform{color.Alpha{opacity}}

	return cel{image: &img, mask: mask, bounds: bounds}
}

func makeCelImage16(f *file, bounds image.Rectangle, opacity byte, pix []byte) cel {
	img := image.Gray16{
		Pix:    pix,
		Stride: bounds.Dx() * 2,
		Rect:   bounds,
	}

	mask := image.Uniform{color.Alpha{opacity}}

	return cel{image: &img, mask: mask, bounds: bounds}
}

func makeCelImage32(f *file, bounds image.Rectangle, opacity byte, pix []byte) cel {
	img := image.NRGBA{
		Pix:    pix,
		Stride: bounds.Dx() * 4,
		Rect:   bounds,
	}

	mask := image.Uniform{color.Alpha{opacity}}

	return cel{image: &img, mask: mask, bounds: bounds}
}

type layer struct {
	flags     uint16
	typ       uint16
	blendMode uint16
	opacity   byte
	name      string
	data      []byte
	userData  UserData
}

func (l *layer) Parse(raw []byte) error {
	if len(raw) < 13 {
		return errors.New("truncated layer chunk")
	}
	l.typ = binary.LittleEndian.Uint16(raw[2:])
	if l.typ == 2 {
		return errors.New("tilemap layers not supported")
	}
	l.flags = binary.LittleEndian.Uint16(raw)
	l.blendMode = binary.LittleEndian.Uint16(raw[10:])
	l.opacity = raw[12]
	// The layer name is a length-prefixed string at offset 16. A truncated
	// name does not prevent loading the layer; it is left empty.
	if len(raw) >= 18 {
		if n := int(binary.LittleEndian.Uint16(raw[16:])); n <= len(raw)-18 {
			l.name = string(raw[18 : 18+n])
		}
	}
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

	// A chunk length is untrusted. Clamp it to the available bytes instead of
	// slicing out of range so a truncated chunk (for example a truncated user
	// data chunk at the end of a frame) is preserved as far as possible and
	// does not prevent the rest of the frame from being decoded.
	end := int(chunkLen)
	if end < 6 {
		end = 6
	}
	if end > len(raw) {
		end = len(raw)
	}

	c.raw = raw[6:end]
	return raw[end:], nil
}

type frame struct {
	dur    time.Duration
	chunks []chunk
	cels   []cel
	// celsInfo lists the metadata of every cel chunk in file order, including
	// cels that were not composited.
	celsInfo []Cel
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

	nchunks := int(newChunks)
	if nchunks == 0 {
		nchunks = int(oldChunks)
	}

	f.chunks = make([]chunk, 0, nchunks)

	raw = raw[16:]

	for i := 0; i < nchunks; i++ {
		var c chunk
		var err error
		raw, err = c.Read(raw)
		if err != nil {
			// A frame that declares more chunks than are present is
			// truncated. Keep the chunks that were read and stop parsing the
			// file at this point: user data and other metadata parsing is
			// best-effort and must not prevent pixels that are present from
			// being decoded.
			return nil, nil
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

func (f *file) buildAtlas() (atlas draw.Image, framesr []image.Rectangle) {
	var atlasr image.Rectangle
	atlasr, framesr = makeAtlasFrames(len(f.frames), f.framew, f.frameh)

	if f.bpp == 16 {
		atlas = image.NewGray16(atlasr)
	} else {
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
		for _, c := range fr.celsInfo {
			cel := c
			cel.UserData = cloneUserData(c.UserData)
			frames[i].Cels = append(frames[i].Cels, cel)
		}
	}

	return frames, userdata
}

// buildLayers returns the structured metadata of all layers, in file order.
func (f *file) buildLayers() []Layer {
	layers := make([]Layer, len(f.layers))
	for i, l := range f.layers {
		layers[i] = Layer{
			Index:     i,
			Name:      l.name,
			Flags:     l.flags,
			Type:      l.typ,
			BlendMode: l.blendMode,
			Opacity:   l.opacity,
			Visible:   l.flags&1 != 0,
			Reference: l.flags&64 != 0,
			UserData:  cloneUserData(l.userData),
		}
	}
	return layers
}

// cloneUserData deep-copies the byte slices in u so the structured metadata
// does not alias the decoder's internal buffers. It also marks an absent chunk
// with a ChunkIndex of -1.
func cloneUserData(u UserData) UserData {
	if !u.Present {
		u.ChunkIndex = -1
		return u
	}
	u.Raw = append([]byte{}, u.Raw...)
	if u.TextBytes != nil {
		u.TextBytes = append([]byte{}, u.TextBytes...)
	}
	return u
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
