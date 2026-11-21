package aseprite

import (
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
	img := image.NewNRGBA(bounds)

	// 16 bpp grayscale+alpha -> NRGBA
	stride := bounds.Dx() * 2
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			i := (y-bounds.Min.Y)*stride + (x-bounds.Min.X)*2
			grayValue := pix[i]    // 8-bit grey
			alphaValue := pix[i+1] // 8-bit alpha

			finalAlpha := uint16(alphaValue) * uint16(opacity) / 255

			img.SetNRGBA(x, y, color.NRGBA{
				R: grayValue,
				G: grayValue,
				B: grayValue,
				A: byte(finalAlpha),
			})
		}
	}
	mask := image.Uniform{color.Alpha{opacity}}
	return cel{img, mask, nil}
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
	if !at(raw, 0, 13) {
		return &syntaxError{msg: "truncated layer chunk", offset: -1}
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
	typ    int
	raw    []byte
	offset int64
}

// readChunk reads one chunk from r. r must be a bounded reader whose remaining
// capacity is the bytes left in the enclosing frame. base is the stream offset
// of the chunk's first byte. The declared chunk length is validated against
// both that remaining capacity and the chunk budget before the payload buffer
// is allocated, so an oversized chunk never triggers a large allocation.
func (c *chunk) readChunk(r *boundedReader, base int64, b Budget) error {
	var hdr [6]byte
	if err := readFull(r, hdr[:], base); err != nil {
		return err
	}

	chunkLen := int64(binary.LittleEndian.Uint32(hdr[:]))
	if chunkLen < 6 {
		return &syntaxError{msg: "chunk length smaller than chunk header", offset: base}
	}
	// Reject before allocating: a chunk cannot extend past the frame it is in.
	if chunkLen-6 > r.N {
		return &syntaxError{msg: "chunk extends past end of frame", offset: base}
	}
	if err := checkBudget(int64(b.MaxChunkLength), "MaxChunkLength", "chunk", chunkLen, base); err != nil {
		return err
	}

	payloadLen := chunkLen - 6
	c.typ = int(binary.LittleEndian.Uint16(hdr[4:]))
	c.offset = base
	c.raw = make([]byte, payloadLen)
	if err := readFull(r, c.raw, base+6); err != nil {
		return err
	}
	return nil
}

type frame struct {
	dur    time.Duration
	chunks []chunk
	cels   []cel
}

// readFrame reads one frame and all of its chunks from r. r must be the
// file-level bounded reader; base is the stream offset of the frame header.
func (fr *frame) readFrame(r *boundedReader, base int64, b Budget) error {
	var hdr [16]byte
	if err := readFull(r, hdr[:], base); err != nil {
		return err
	}

	frameLen := int64(binary.LittleEndian.Uint32(hdr[:]))
	if frameLen < 16 {
		return &syntaxError{msg: "frame length smaller than frame header", offset: base}
	}
	// A frame cannot extend past the bytes remaining in the file section.
	if frameLen-16 > r.N {
		return &syntaxError{msg: "frame extends past end of file", offset: base}
	}
	if magic := binary.LittleEndian.Uint16(hdr[4:]); magic != 0xF1FA {
		return &syntaxError{msg: "invalid frame magic number", offset: base + 4}
	}

	oldChunks := binary.LittleEndian.Uint16(hdr[6:])
	durationMS := binary.LittleEndian.Uint16(hdr[8:])
	newChunks := binary.LittleEndian.Uint32(hdr[12:])

	fr.dur = time.Millisecond * time.Duration(durationMS)

	nchunks := int64(newChunks)
	if nchunks == 0 {
		nchunks = int64(oldChunks)
	}
	if nchunks <= 0 {
		return &syntaxError{msg: "frame declares no chunks", offset: base + 12}
	}
	// Every chunk occupies at least its 6-byte header within the frame body,
	// so the declared count cannot exceed what the frame can physically hold.
	if nchunks > (frameLen-16)/6 {
		return &syntaxError{msg: "chunk count exceeds frame length", offset: base + 12}
	}

	// Read the frame body through a reader bounded to the declared frame size
	// so a malformed chunk length cannot read into the next frame.
	body := &boundedReader{R: r, N: frameLen - 16}
	fr.chunks = make([]chunk, 0, nchunks)

	pos := base + 16
	for i := int64(0); i < nchunks; i++ {
		var c chunk
		if err := c.readChunk(body, pos, b); err != nil {
			return err
		}
		pos += 6 + int64(len(c.raw))
		fr.chunks = append(fr.chunks, c)
	}

	// The declared frame length must account exactly for the chunks consumed;
	// any leftover bytes mean the chunk lengths or count were inconsistent.
	if body.N != 0 {
		return &syntaxError{msg: "frame length does not match its chunks", offset: base}
	}
	return nil
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
	budget      Budget
	celBytes    int64
}

// readFile reads and parses the entire serialized file. Every limit in b is
// enforced before the resource it guards is allocated. The returned byte count
// is the number of bytes consumed from r.
func (f *file) readFile(r io.Reader, b Budget) (int64, error) {
	f.budget = b

	var hdr [128]byte

	raw := hdr[:]

	n, err := io.ReadFull(r, raw)
	if err != nil {
		return int64(n), err
	}

	if magic := binary.LittleEndian.Uint16(raw[4:]); magic != 0xA5E0 {
		return 128, &syntaxError{msg: "invalid magic number", offset: 4}
	}

	if pixw, pixh := raw[34], raw[35]; pixw != pixh {
		return 128, &syntaxError{msg: "unsupported pixel ratio", offset: 34}
	}

	f.bpp = binary.LittleEndian.Uint16(raw[12:])
	f.flags = binary.LittleEndian.Uint16(raw[14:])

	declaredFrames := int64(binary.LittleEndian.Uint16(raw[6:]))
	if declaredFrames <= 0 {
		return 128, &syntaxError{msg: "frame count must be greater than zero", offset: 6}
	}
	if err := checkBudget(int64(b.MaxFrames), "MaxFrames", "header", declaredFrames, 6); err != nil {
		return 128, err
	}

	f.framew = int(binary.LittleEndian.Uint16(raw[8:]))
	f.frameh = int(binary.LittleEndian.Uint16(raw[10:]))
	if f.framew <= 0 || f.frameh <= 0 {
		return 128, &syntaxError{msg: "canvas dimensions must be greater than zero", offset: 8}
	}
	npixels, ok := mulInt64(int64(f.framew), int64(f.frameh))
	if !ok {
		return 128, &syntaxError{msg: "canvas pixel count overflows", offset: 8}
	}
	if err := checkBudget(int64(b.MaxPixels), "MaxPixels", "header", npixels, 8); err != nil {
		return 128, err
	}

	paletteSize := int(binary.LittleEndian.Uint16(raw[32:]))
	f.transparent = raw[28]

	switch f.bpp {
	case 8:
		f.makeCel = makeCelImage8
	case 16:
		f.makeCel = makeCelImage16
	case 32:
		f.makeCel = makeCelImage32
	default:
		return 128, &syntaxError{msg: "invalid color depth", offset: 12}
	}

	f.palette = make(color.Palette, paletteSize)
	for i := range f.palette {
		f.palette[i] = color.Black
	}
	if int(f.transparent) < len(f.palette) {
		f.palette[f.transparent] = color.Transparent
	}

	// The declared file size bounds every subsequent read. It is validated
	// against the budget up front, and the body is streamed frame by frame
	// rather than buffered into a single file-size allocation.
	fileSize := int64(binary.LittleEndian.Uint32(raw))
	if fileSize < 128 {
		return 128, &syntaxError{msg: "file size smaller than header", offset: 0}
	}
	if err := checkBudget(int64(b.MaxFileBytes), "MaxFileBytes", "header", fileSize, 0); err != nil {
		return 128, err
	}

	// All reads of the frame section are capped at the declared size, so a
	// length field can never drive an allocation beyond MaxFileBytes.
	body := &boundedReader{R: r, N: fileSize - 128}
	f.frames = make([]frame, 0, declaredFrames)

	offset := int64(128)
	for body.N > 0 {
		if err := checkBudget(int64(b.MaxFrames), "MaxFrames", "frame",
			int64(len(f.frames))+1, offset); err != nil {
			return offset, err
		}

		var fr frame
		if err := fr.readFrame(body, offset, b); err != nil {
			return offset, err
		}

		// Layer chunks only appear in the first frame. Count them here so that
		// DecodeConfig, which never initializes cels, enforces the same layer
		// budget as Read and Decode.
		if len(f.frames) == 0 {
			nlayers := 0
			for _, ch := range fr.chunks {
				if ch.typ == 0x2004 {
					nlayers++
				}
			}
			if err := checkBudget(int64(b.MaxLayers), "MaxLayers", "first frame",
				int64(nlayers), offset); err != nil {
				return offset, err
			}
		}

		f.frames = append(f.frames, fr)
		offset = fileSize - body.N
	}

	if int64(len(f.frames)) != declaredFrames {
		return fileSize, &syntaxError{
			msg:    "frame count does not match header",
			offset: 6,
		}
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
		atlas = image.NewNRGBA(atlasr)
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
