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

type cel struct {
	image image.Image
	mask  image.Uniform
	data  []byte
}

func makeCelImage8(f *file, bounds image.Rectangle, opacity byte, pix []byte) cel {
	mask := image.Uniform{color.Alpha{opacity}}

	// An indexed image without a palette cannot be sampled at all:
	// image.Paletted.At returns nil, which every drawing consumer
	// dereferences. Treat the cel as empty instead of panicking.
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

// clampIndices returns pix unchanged when every index is within the palette.
// Otherwise it returns a copy with out-of-range indices remapped to the
// transparent index. Copying only when needed keeps the usual path allocation
// free and never writes to a buffer shared with the caller's input.
func (f *file) clampIndices(pix []byte) []byte {
	bad := false
	for _, v := range pix {
		if int(v) >= len(f.palette) {
			bad = true
			break
		}
	}
	if !bad {
		return pix
	}

	fallback := byte(0)
	if int(f.transparent) < len(f.palette) {
		fallback = f.transparent
	}
	out := make([]byte, len(pix))
	for i, v := range pix {
		if int(v) >= len(f.palette) {
			v = fallback
		}
		out[i] = v
	}
	return out
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
		return formatError("layer chunk: truncated header")
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

	// offset is the absolute byte offset of the chunk header in the input.
	offset int64

	// payloadOffset is the absolute byte offset of the chunk payload.
	payloadOffset int64

	// size is the total chunk size including the 6-byte header.
	size int
}

func (c chunk) Reader() io.Reader {
	return bytes.NewReader(c.raw)
}

func (c *chunk) Read(raw []byte, offset int64, frameIndex, chunkIndex int) ([]byte, error) {
	if len(raw) < 6 {
		return nil, &ChunkError{
			Frame:  frameIndex,
			Chunk:  chunkIndex,
			Layer:  NoLayer,
			Offset: offset,
			Err:    fmt.Errorf("%w: chunk header needs 6 bytes, %d remain", ErrTruncated, len(raw)),
		}
	}

	n := binary.LittleEndian.Uint32(raw)
	c.typ = int(binary.LittleEndian.Uint16(raw[4:]))
	c.offset = offset

	if n < 6 {
		return nil, &ChunkError{
			Type:   uint16(c.typ),
			Frame:  frameIndex,
			Chunk:  chunkIndex,
			Layer:  NoLayer,
			Offset: offset,
			Size:   int(n),
			Err:    fmt.Errorf("%w: chunk size %d is smaller than its 6-byte header", ErrInvalidFormat, n),
		}
	}
	if uint64(n) > uint64(len(raw)) {
		return nil, &ChunkError{
			Type:   uint16(c.typ),
			Frame:  frameIndex,
			Chunk:  chunkIndex,
			Layer:  NoLayer,
			Offset: offset,
			Size:   int(n),
			Err:    fmt.Errorf("%w: chunk size %d exceeds the %d bytes remaining", ErrTruncated, n, len(raw)),
		}
	}

	c.size = int(n)
	c.raw = raw[6:c.size]
	c.payloadOffset = offset + 6
	return raw[c.size:], nil
}

type frame struct {
	dur    time.Duration
	chunks []chunk
	cels   []cel
}

func (f *frame) Read(raw []byte, baseOffset int64, frameIndex int) ([]byte, error) {
	if len(raw) < 16 {
		return nil, &ChunkError{
			Frame:  frameIndex,
			Layer:  NoLayer,
			Offset: baseOffset,
			Err:    fmt.Errorf("%w: frame header needs 16 bytes, %d remain", ErrTruncated, len(raw)),
		}
	}

	if magic := binary.LittleEndian.Uint16(raw[4:]); magic != 0xF1FA {
		return nil, errInvalidMagic
	}

	frameLen := int(binary.LittleEndian.Uint32(raw))
	if frameLen < 16 {
		return nil, &ChunkError{
			Frame:  frameIndex,
			Layer:  NoLayer,
			Offset: baseOffset,
			Size:   frameLen,
			Err:    fmt.Errorf("%w: frame size %d is smaller than its 16-byte header", ErrInvalidFormat, frameLen),
		}
	}
	if frameLen > len(raw) {
		return nil, &ChunkError{
			Frame:  frameIndex,
			Layer:  NoLayer,
			Offset: baseOffset,
			Size:   frameLen,
			Err:    fmt.Errorf("%w: frame size %d exceeds the %d bytes remaining", ErrTruncated, frameLen, len(raw)),
		}
	}

	oldChunks := binary.LittleEndian.Uint16(raw[6:])
	durationMS := binary.LittleEndian.Uint16(raw[8:])
	newChunks := binary.LittleEndian.Uint32(raw[12:])

	f.dur = time.Millisecond * time.Duration(durationMS)

	// The new 32-bit chunk count supersedes the old 16-bit one. Old-format
	// frames leave the new field zero and use the old count instead. Every
	// chunk occupies at least its 6-byte header, so an implausible count is
	// rejected before allocating the chunk slice: it comes straight from
	// untrusted input.
	nchunks := int(newChunks)
	if nchunks == 0 {
		nchunks = int(oldChunks)
	}
	if nchunks > (frameLen-16)/6 {
		return nil, &ChunkError{
			Frame:  frameIndex,
			Layer:  NoLayer,
			Offset: baseOffset,
			Size:   frameLen,
			Err: fmt.Errorf("%w: frame declares %d chunks but has room for at most %d",
				ErrInvalidFormat, nchunks, (frameLen-16)/6),
		}
	}

	f.chunks = make([]chunk, nchunks)

	body := raw[16:frameLen]
	rest := raw[frameLen:]
	pos := int64(16)

	for i := 0; i < nchunks; i++ {
		var c chunk
		var err error
		body, err = c.Read(body, baseOffset+pos, frameIndex, i)
		if err != nil {
			return nil, err
		}
		f.chunks[i] = c
		pos += int64(c.size)
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

	// opts controls the unknown chunk audit. Audit state collected while
	// reading lives here until readFrom copies it onto the Aseprite value.
	opts Options

	unknownChunks []UnknownChunk

	// unknownCounts tracks per-frame unknown chunk types seen so far, so a
	// duplicate type can be reported with its frame context.
	unknownCounts []map[uint16]int

	// layerOrdinal counts Layer chunks across the whole file, giving each
	// layer its index in the order the decoder appends them. layerSeen
	// records whether any layer chunk has been read yet.
	layerOrdinal int
	layerSeen    bool

	// declaredFrames is the frame count from the file header. It bounds the
	// frames an unknown chunk may be attributed to. Zero disables the check,
	// as some writers do not fill the field in.
	declaredFrames int
}

// ReadFrom reads a sprite file using the default options.
func (f *file) ReadFrom(r io.Reader) (int64, error) {
	return f.ReadFromWithOptions(r, Options{})
}

// ReadFromWithOptions reads a sprite file and audits chunks that the decoder
// does not understand.
func (f *file) ReadFromWithOptions(r io.Reader, opts Options) (int64, error) {
	f.opts = opts.withDefaults()
	f.unknownChunks = nil
	f.unknownCounts = nil
	f.layerOrdinal = 0
	f.layerSeen = false
	var hdr [128]byte

	raw := hdr[:]

	if n, err := io.ReadFull(r, raw); err != nil {
		return int64(n), &ChunkError{
			Layer:  NoLayer,
			Offset: int64(n),
			Err:    fmt.Errorf("%w: sprite header needs 128 bytes: %v", ErrTruncated, err),
		}
	}

	if magic := binary.LittleEndian.Uint16(raw[4:]); magic != 0xA5E0 {
		return 128, errInvalidMagic
	}

	if pixw, pixh := raw[34], raw[35]; pixw != pixh {
		return 128, errors.New("unsupported pixel ratio")
	}

	f.bpp = binary.LittleEndian.Uint16(raw[12:])
	f.flags = binary.LittleEndian.Uint16(raw[14:])
	f.declaredFrames = int(binary.LittleEndian.Uint16(raw[6:]))
	f.frames = make([]frame, 0, f.declaredFrames)
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

	fileSize := int64(binary.LittleEndian.Uint32(raw))
	if fileSize < 128 {
		return 128, &ChunkError{
			Layer:  NoLayer,
			Offset: 0,
			Size:   int(fileSize),
			Err:    fmt.Errorf("%w: declared file size %d is smaller than the 128-byte header", ErrInvalidFormat, fileSize),
		}
	}
	if f.opts.MaxFileSize >= 0 && fileSize > f.opts.MaxFileSize {
		return 128, &ChunkError{
			Layer:  NoLayer,
			Offset: 0,
			Size:   int(fileSize),
			Err:    fmt.Errorf("%w: declared file size %d exceeds the %d-byte limit", ErrInvalidFormat, fileSize, f.opts.MaxFileSize),
		}
	}

	raw = make([]byte, fileSize-128)

	if n, err := io.ReadFull(r, raw); err != nil {
		return int64(128 + n), &ChunkError{
			Layer:  NoLayer,
			Offset: 128 + int64(n),
			Err:    fmt.Errorf("%w: %v", ErrTruncated, err),
		}
	}

	base := int64(128)
	for len(raw) > 0 {
		var fr frame
		var err error
		n := len(raw)
		if raw, err = fr.Read(raw, base, len(f.frames)); err != nil {
			return fileSize, err
		}

		// Record extension chunks for this frame before moving on. The frame
		// index and offsets are known here; layer attribution is resolved by
		// replaying the frame's known chunks.
		if err := f.auditFrame(len(f.frames), fr); err != nil {
			return fileSize, err
		}

		f.frames = append(f.frames, fr)
		base += int64(n - len(raw))
	}

	if len(f.frames) == 0 {
		return fileSize, &ChunkError{
			Layer: NoLayer,
			Err:   fmt.Errorf("%w: sprite contains no frames", ErrInvalidFormat),
		}
	}

	return fileSize, nil
}

// interpretedChunk reports whether the decoder consumes the chunk type as part
// of image or metadata reconstruction.
func interpretedChunk(typ int) bool {
	switch typ {
	case 0x0004, 0x0011, 0x2004, 0x2005, 0x2018, 0x2019, 0x2020, 0x2022:
		return true
	}
	return false
}

// documentedChunk reports whether the type appears in the Aseprite file format
// specification, even though this package does not interpret it.
func documentedChunk(typ int) bool {
	switch typ {
	case 0x2006, 0x2007, 0x2008, 0x2016, 0x2017, 0x2023:
		return true
	}
	return false
}

// hasUnknownChunk reports whether fr contains a chunk the decoder does not
// interpret.
func hasUnknownChunk(fr frame) bool {
	for _, ch := range fr.chunks {
		if !interpretedChunk(ch.typ) {
			return true
		}
	}
	return false
}

// auditFrame records the chunks of frame fr that the decoder does not
// interpret. Chunks are walked in file order so layer attribution follows the
// preceding layer or cel record, matching how Aseprite associates user data.
func (f *file) auditFrame(frameIndex int, fr frame) error {
	if f.declaredFrames > 0 && frameIndex >= f.declaredFrames && hasUnknownChunk(fr) {
		// The chunk exists, but the header says the sprite has fewer frames.
		// Reporting it would attribute audit data to a frame the file claims
		// does not exist, so reject it with frame context instead. Files
		// without extension chunks in the excess frame keep decoding exactly
		// as before.
		offset := int64(128)
		if len(fr.chunks) > 0 {
			offset = fr.chunks[0].offset
		}
		return &ChunkError{
			Frame:  frameIndex,
			Layer:  NoLayer,
			Offset: offset,
			Err:    fmt.Errorf("%w: frame %d is outside the %d frames declared by the header", ErrUnknownChunkFrame, frameIndex, f.declaredFrames),
		}
	}

	if len(f.unknownCounts) <= frameIndex {
		f.unknownCounts = append(f.unknownCounts, make([]map[uint16]int, frameIndex+1-len(f.unknownCounts))...)
	}
	counts := f.unknownCounts[frameIndex]
	if counts == nil {
		counts = make(map[uint16]int)
		f.unknownCounts[frameIndex] = counts
	}

	layer := NoLayer
	layerSeen := f.layerSeen
	// Layer chunks are emitted once (in the first frame) and, per the
	// specification, before anything that references them. The decoder
	// appends layers in that order, so a global running ordinal identifies
	// the layer owning the chunks that follow it. Later frames usually have
	// no layer chunks; their context comes from each cel's explicit layer
	// index.
	for chunkIndex, ch := range fr.chunks {
		typ := uint16(ch.typ)

		if interpretedChunk(ch.typ) {
			switch ch.typ {
			case 0x2004:
				layer = f.layerOrdinal
				f.layerOrdinal++
				f.layerSeen = true
				layerSeen = true
			case 0x2005:
				if len(ch.raw) >= 2 {
					// A cel names its layer explicitly; that index is the
					// file's own attribution even if it later turns out to be
					// out of range and the cel is rejected by the parser.
					layer = int(binary.LittleEndian.Uint16(ch.raw))
					layerSeen = true
				}
			}
			continue
		}

		if !layerSeen {
			layer = NoLayer
		}

		if f.opts.RequireUnknownChunkContext && layer == NoLayer {
			return &ChunkError{
				Type:   typ,
				Frame:  frameIndex,
				Chunk:  chunkIndex,
				Layer:  layer,
				Offset: ch.offset,
				Size:   ch.size,
				Err:    fmt.Errorf("%w: type 0x%04X at offset %d precedes any layer or cel chunk", ErrUnknownChunkContext, typ, ch.offset),
			}
		}

		payloadSize := ch.size - 6
		if f.opts.MaxUnknownChunkSize >= 0 && int64(payloadSize) > f.opts.MaxUnknownChunkSize {
			return &ChunkError{
				Type:   typ,
				Frame:  frameIndex,
				Chunk:  chunkIndex,
				Layer:  layer,
				Offset: ch.offset,
				Size:   ch.size,
				Err: fmt.Errorf("%w: payload is %d bytes, limit is %d", ErrUnknownChunkTooLarge,
					payloadSize, f.opts.MaxUnknownChunkSize),
			}
		}

		if f.opts.MaxUnknownChunks >= 0 && len(f.unknownChunks) >= f.opts.MaxUnknownChunks {
			return &ChunkError{
				Type:   typ,
				Frame:  frameIndex,
				Chunk:  chunkIndex,
				Layer:  layer,
				Offset: ch.offset,
				Size:   ch.size,
				Err: fmt.Errorf("%w: already recorded %d chunks, limit is %d", ErrTooManyUnknownChunks,
					len(f.unknownChunks), f.opts.MaxUnknownChunks),
			}
		}

		duplicate := counts[typ] > 0
		// Only undocumented extension types are treated strictly, and only
		// on request. The specification allows some of its own chunks (for
		// example 0x2006 Cel Extra) to occur once per cel or layer, so those
		// remain accepted even though this package does not interpret them.
		if duplicate && f.opts.RejectDuplicateUnknownChunkTypes && !documentedChunk(ch.typ) {
			return &ChunkError{
				Type:   typ,
				Frame:  frameIndex,
				Chunk:  chunkIndex,
				Layer:  layer,
				Offset: ch.offset,
				Size:   ch.size,
				Err:    fmt.Errorf("%w: type 0x%04X already occurred in frame %d", ErrDuplicateUnknownChunk, typ, frameIndex),
			}
		}
		counts[typ]++

		uc := UnknownChunk{
			Type:        typ,
			Frame:       frameIndex,
			Chunk:       chunkIndex,
			Layer:       layer,
			Offset:      ch.offset,
			DataOffset:  ch.payloadOffset,
			Size:        ch.size,
			PayloadSize: payloadSize,
			Duplicate:   duplicate,
			SpecKnown:   documentedChunk(ch.typ),
		}

		if f.opts.SkipUnknownChunkData {
			uc.DataSkipped = true
		} else {
			// Copy: the chunk points into the caller's input buffer. The audit
			// must survive Decode returning and must never alias decoder state.
			uc.Data = append([]byte{}, ch.raw...)
		}

		f.unknownChunks = append(f.unknownChunks, uc)
	}

	return nil
}

func (f *file) buildAtlas() (atlas draw.Image, framesr []image.Rectangle, err error) {
	var atlasr image.Rectangle
	atlasr, framesr = makeAtlasFrames(len(f.frames), f.framew, f.frameh)

	var bpp int
	switch f.bpp {
	case 8:
		bpp = 1
	case 16:
		bpp = 2
	default:
		bpp = 4
	}

	// Bound the atlas before allocating it: dimensions and frame count come
	// from the untrusted header, so a tiny file could otherwise request an
	// enormous image. Zero means the limit is disabled (config-only path).
	if f.opts.MaxAtlasSize > 0 {
		need := int64(atlasr.Dx()) * int64(atlasr.Dy()) * int64(bpp)
		if need > f.opts.MaxAtlasSize {
			return nil, nil, &ChunkError{
				Layer: NoLayer,
				Err: fmt.Errorf("%w: atlas is %dx%d (%d bytes), limit is %d",
					ErrAtlasTooLarge, atlasr.Dx(), atlasr.Dy(), need, f.opts.MaxAtlasSize),
			}
		}
	}

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
			// A cel whose declared size exceeds its data would otherwise make
			// image/draw index out of bounds while sampling. Clip to the frame,
			// matching what draw already does for cels outside the bounds.
			clip := sr.Intersect(framebounds)
			if clip.Empty() {
				continue
			}
			sp = sp.Add(clip.Min.Sub(sr.Min))
			sr = clip

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
