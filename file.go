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

// Resource limits. They exist so that a small, hostile file cannot make the
// decoder allocate unbounded memory. Values are deliberately generous for
// real sprites: a 4096x4096 RGBA cel and a 8192x8192 RGBA atlas both fit.
const (
	// maxFileSize bounds the body size declared by the file header.
	maxFileSize = 512 << 20
	// maxCelBytes bounds the decoded pixel buffer of a single cel.
	maxCelBytes = 64 << 20
	// maxAtlasPixels bounds the total number of pixels in the atlas.
	maxAtlasPixels = 1 << 26
)

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

func (l *layer) Parse(raw []byte, ctx context) error {
	if len(raw) < 16 {
		return ctx.err(ErrMalformed, "layer chunk: need 16 bytes, have %d", len(raw))
	}
	if typ := binary.LittleEndian.Uint16(raw[2:]); typ == 2 {
		return ctx.err(ErrUnsupported, "tilemap layers not supported")
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

func (c chunk) Reader() io.Reader {
	return bytes.NewReader(c.raw)
}

// Read consumes one chunk. limit is the number of bytes left in the enclosing
// frame; raw may extend past the frame into the rest of the file, which lets
// Read tell a truncated file apart from a chunk that overruns its frame.
func (c *chunk) Read(raw []byte, limit int, ctx context) ([]byte, error) {
	if len(raw) < 6 {
		return nil, ctx.err(ErrTruncated, "chunk header: need 6 bytes, have %d", len(raw))
	}

	chunkLen := int(binary.LittleEndian.Uint32(raw))
	typ := binary.LittleEndian.Uint16(raw[4:])

	if chunkLen < 6 {
		return nil, ctx.err(ErrMalformed, "chunk type 0x%04x has length %d, minimum is 6", typ, chunkLen)
	}
	if chunkLen > len(raw) {
		return nil, ctx.err(ErrTruncated, "chunk type 0x%04x declares %d bytes, only %d remain", typ, chunkLen, len(raw))
	}
	if chunkLen > limit {
		return nil, ctx.err(ErrMalformed, "chunk type 0x%04x length %d exceeds the %d bytes left in the frame", typ, chunkLen, limit)
	}

	c.typ = int(typ)
	c.raw = raw[6:chunkLen]
	c.offset = ctx.offset
	return raw[chunkLen:], nil
}

type frame struct {
	dur    time.Duration
	chunks []chunk
	cels   []cel
}

// Read consumes one frame from the first limit bytes of raw. offset is the
// absolute file offset of the frame header and index is its position, both
// used for error context. raw may hold bytes beyond the frame's declared
// length, which are reported by the caller.
func (f *frame) Read(raw []byte, limit int, offset int64, index int) ([]byte, error) {
	ctx := newContext().frameAt(index, offset)

	if limit < 16 {
		return nil, ctx.err(ErrTruncated, "frame header: need 16 bytes, have %d", limit)
	}
	if len(raw) < 16 {
		return nil, ctx.err(ErrTruncated, "frame header: need 16 bytes, have %d", len(raw))
	}

	frameLen := int(binary.LittleEndian.Uint32(raw))
	if frameLen < 16 {
		return nil, ctx.err(ErrMalformed, "frame length %d is smaller than the 16-byte header", frameLen)
	}
	if frameLen > limit {
		return nil, ctx.err(ErrTruncated, "frame declares %d bytes, only %d remain", frameLen, limit)
	}

	if magic := binary.LittleEndian.Uint16(raw[4:]); magic != 0xF1FA {
		return nil, ctx.err(ErrMalformed, "invalid frame magic 0x%04x", magic)
	}

	oldChunks := int(binary.LittleEndian.Uint16(raw[6:]))
	durationMS := binary.LittleEndian.Uint16(raw[8:])
	newChunks := int(binary.LittleEndian.Uint32(raw[12:]))

	nchunks := newChunks
	if nchunks == 0 {
		nchunks = oldChunks
	}

	// Every chunk needs at least a six-byte header. Rejecting an impossible
	// chunk count before allocating keeps a corrupt 32-bit count from
	// exhausting memory.
	if nchunks < 0 || nchunks > (frameLen-16)/6 {
		return nil, ctx.err(ErrMalformed, "chunk count %d cannot fit in a %d-byte frame", nchunks, frameLen)
	}

	f.dur = time.Millisecond * time.Duration(durationMS)
	f.chunks = make([]chunk, 0, nchunks)

	pos := 16
	for i := 0; i < nchunks; i++ {
		chunkOffset := offset + int64(pos)
		cctx := ctx.chunkAt(i, chunkOffset)
		var c chunk
		rest, err := c.Read(raw[pos:], frameLen-pos, cctx)
		if err != nil {
			return nil, err
		}
		pos = len(raw) - len(rest)
		f.chunks = append(f.chunks, c)
	}

	if pos != frameLen {
		return nil, ctx.at(offset+int64(pos)).err(
			ErrMalformed, "%d unread bytes at the end of the frame", frameLen-pos)
	}

	return raw[frameLen:], nil
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
	nframes     int
	fileSize    int64
}

// readHeader reads and validates the fixed 128-byte header. It does not touch
// the frame data, which lets DecodeConfig stop early.
func (f *file) readHeader(r io.Reader) (int64, error) {
	var hdr [128]byte

	n, err := io.ReadFull(r, hdr[:])
	if err != nil {
		return int64(n), newContext().err(ErrTruncated, "header: need 128 bytes, read %d: %v", n, err)
	}

	ctx := newContext()

	if magic := binary.LittleEndian.Uint16(hdr[4:]); magic != 0xA5E0 {
		return 128, ctx.err(ErrMalformed, "invalid magic 0x%04x, want 0xa5e0", magic)
	}

	if pixw, pixh := hdr[34], hdr[35]; pixw != pixh {
		return 128, ctx.err(ErrUnsupported, "pixel ratio %d:%d", pixw, pixh)
	}

	f.bpp = binary.LittleEndian.Uint16(hdr[12:])
	switch f.bpp {
	case 8:
		f.makeCel = makeCelImage8
	case 16:
		f.makeCel = makeCelImage16
	case 32:
		f.makeCel = makeCelImage32
	default:
		return 128, ctx.err(ErrUnsupported, "color depth %d", f.bpp)
	}

	f.nframes = int(binary.LittleEndian.Uint16(hdr[6:]))
	if f.nframes == 0 {
		return 128, ctx.err(ErrMalformed, "file declares no frames")
	}

	f.flags = binary.LittleEndian.Uint16(hdr[14:])
	f.framew = int(binary.LittleEndian.Uint16(hdr[8:]))
	f.frameh = int(binary.LittleEndian.Uint16(hdr[10:]))
	if f.framew == 0 || f.frameh == 0 {
		return 128, ctx.err(ErrMalformed, "sprite dimensions %dx%d", f.framew, f.frameh)
	}

	ncolors := int(binary.LittleEndian.Uint16(hdr[32:]))
	f.palette = make(color.Palette, ncolors)
	for i := range f.palette {
		f.palette[i] = color.Black
	}
	f.transparent = hdr[28]
	if int(f.transparent) < len(f.palette) {
		f.palette[f.transparent] = color.Transparent
	}

	f.fileSize = int64(binary.LittleEndian.Uint32(hdr[:]))
	if f.fileSize < 128 {
		return 128, ctx.err(ErrMalformed, "declared file size %d is smaller than the header", f.fileSize)
	}
	if f.fileSize > maxFileSize {
		return 128, ctx.err(ErrUnsupported, "declared file size %d exceeds the %d byte limit", f.fileSize, int64(maxFileSize))
	}

	// Reject an impossible atlas before reading the body so a corrupt header
	// cannot cause a huge allocation later.
	if _, _, err := atlasDimensions(f.nframes, f.framew, f.frameh); err != nil {
		return 128, err
	}

	f.frames = make([]frame, 0, f.nframes)
	return 128, nil
}

func (f *file) ReadFrom(r io.Reader) (int64, error) {
	n, err := f.readHeader(r)
	if err != nil {
		return n, err
	}

	bodyLen := f.fileSize - 128

	// Grow the buffer with the data that actually arrives rather than
	// allocating the declared size up front. A file that claims hundreds of
	// megabytes but supplies a few bytes is therefore rejected while only the
	// bytes it really sent have been allocated.
	var buf bytes.Buffer
	if bodyLen > 0 && bodyLen < 1<<20 {
		buf.Grow(int(bodyLen))
	}

	read, err := io.CopyN(&buf, r, bodyLen)
	if err != nil {
		return 128 + read, newContext().err(ErrTruncated, "body: need %d bytes, read %d: %v", bodyLen, read, err)
	}

	if err := f.parseFrames(buf.Bytes()); err != nil {
		return f.fileSize, err
	}

	return f.fileSize, nil
}

func (f *file) parseFrames(raw []byte) error {
	var consumed int64

	for i := 0; i < f.nframes; i++ {
		if consumed >= int64(len(raw)) {
			return newContext().frameAt(i, int64(128)+consumed).err(
				ErrTruncated, "the header declares %d frames, but the data holds %d", f.nframes, i)
		}

		offset := int64(128) + consumed
		var fr frame
		rest, err := fr.Read(raw[consumed:], len(raw)-int(consumed), offset, i)
		if err != nil {
			return err
		}
		consumed += int64(len(raw[consumed:]) - len(rest))
		f.frames = append(f.frames, fr)
	}

	// Bytes past the declared frame count are not silently accepted. They are
	// usually the result of a corrupted frame length, so they are reported as
	// an error rather than parsed as a phantom frame.
	if consumed < int64(len(raw)) {
		return newContext().frameAt(f.nframes, int64(128)+consumed).err(
			ErrMalformed, "%d trailing bytes after %d frames", int64(len(raw))-consumed, f.nframes)
	}

	return nil
}

func (f *file) buildAtlas() (draw.Image, []image.Rectangle, error) {
	atlasr, framesr, err := makeAtlasFrames(len(f.frames), f.framew, f.frameh)
	if err != nil {
		return nil, nil, err
	}

	atlas := image.NewRGBA(atlasr)

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

			draw.DrawMask(dst, sr, src, sp, c.mask, image.Point{}, draw.Over)
		}

		draw.Draw(atlas, framesr[i], dst, image.Point{}, draw.Src)
	}

	return atlas, framesr, nil
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

func makeAtlasFrames(nframes, framew, frameh int) (atlasr image.Rectangle, framesr []image.Rectangle, err error) {
	w, h, err := atlasDimensions(nframes, framew, frameh)
	if err != nil {
		return image.Rectangle{}, nil, err
	}

	fw, fh := factorPowerOfTwo(nframes)
	if framew > frameh {
		fw, fh = fh, fw
	}

	atlasr = image.Rect(0, 0, w, h)

	for i := 0; i < nframes; i++ {
		x, y := i%fw, i/fw
		framesr = append(framesr, image.Rectangle{
			Min: image.Pt(x*framew, y*frameh),
			Max: image.Pt((x+1)*framew, (y+1)*frameh),
		})
	}

	return atlasr, framesr, nil
}

// atlasDimensions computes the width and height of the atlas and validates
// that it is representable and within the memory limit.
func atlasDimensions(nframes, framew, frameh int) (int, int, error) {
	ctx := newContext()

	if nframes <= 0 {
		return 0, 0, ctx.err(ErrMalformed, "atlas: %d frames", nframes)
	}
	if framew <= 0 || frameh <= 0 {
		return 0, 0, ctx.err(ErrMalformed, "atlas: frame size %dx%d", framew, frameh)
	}

	fw, fh := factorPowerOfTwo(nframes)
	if framew > frameh {
		fw, fh = fh, fw
	}

	w := int64(fw) * int64(framew)
	h := int64(fh) * int64(frameh)
	if w <= 0 || h <= 0 || w > math.MaxInt32 || h > math.MaxInt32 {
		return 0, 0, ctx.err(ErrUnsupported, "atlas dimensions %dx%d", w, h)
	}
	if w*h > maxAtlasPixels {
		return 0, 0, ctx.err(ErrUnsupported, "atlas of %dx%d pixels exceeds the %d pixel limit", w, h, maxAtlasPixels)
	}

	return int(w), int(h), nil
}

// factorPowerOfTwo computes n=a*b, where a, b are powers of two and a >= b.
func factorPowerOfTwo(n int) (a, b int) {
	if n <= 1 {
		return 1, 1
	}
	x := int(math.Ceil(math.Log2(float64(n))))
	a = 1 << (x - x/2)
	b = 1 << (x / 2)
	return
}
