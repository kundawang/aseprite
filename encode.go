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
	"sort"
	"time"
	"unicode/utf8"
)

// ASE chunk and header magic numbers, as defined by the Aseprite file format:
// https://github.com/aseprite/aseprite/blob/main/docs/ase-file-specs.md
const (
	aseHeaderMagic uint16 = 0xA5E0
	aseFrameMagic  uint16 = 0xF1FA

	aseChunkLayer    uint16 = 0x2004
	aseChunkCel      uint16 = 0x2005
	aseChunkTags     uint16 = 0x2018
	aseChunkPalette  uint16 = 0x2019
	aseChunkUserData uint16 = 0x2020
	aseChunkSlice    uint16 = 0x2022

	aseCelRaw        uint16 = 0
	aseCelLinked     uint16 = 1
	aseCelCompressed uint16 = 2

	aseFlagLayerOpacity uint32 = 1

	// maxASEDimension is the maximum canvas or cel dimension. Aseprite itself
	// limits sprites to 32768 pixels per axis, and the on-disk fields are
	// 16-bit unsigned.
	maxASEDimension = 32768

	// maxTextLen is the maximum length of a STRING payload. Strings are
	// prefixed by a 16-bit unsigned byte length.
	maxTextLen = 65535
)

// BPP is the color depth of a sprite, expressed in bits per pixel.
type BPP uint16

const (
	Indexed8    BPP = 8
	Grayscale16 BPP = 16
	RGBA32      BPP = 32
)

// SpriteLayer describes a single image layer. Only regular, flat image layers are
// supported; group and tilemap layers cannot be represented.
type SpriteLayer struct {
	// Name is the layer name. SpriteLayer names must be unique.
	Name string

	// Visible reports whether the layer is visible. Invisible layers are
	// still written to the file.
	Visible bool

	// Editable reports whether the layer is editable.
	Editable bool

	// Background marks the layer as the background layer.
	Background bool

	// Reference marks the layer as a reference layer.
	Reference bool

	// BlendMode is the Aseprite blend mode, in the range [0, 18].
	BlendMode uint16

	// Opacity is the layer opacity in the range [0, 255].
	Opacity uint8

	// Data is optional user data text attached to the layer.
	// It is written as a user data chunk immediately following the layer chunk.
	Data []byte
}

// SpriteCel describes a single cel on a single layer of a single frame.
//
// A cel is either a regular cel, in which case Image and Bounds must be set,
// or a linked cel, in which case LinkedFrame must be set and Image must be
// nil. A linked cel references the cel on the same layer in an earlier frame.
type SpriteCel struct {
	// Layer is the index of the layer that the cel belongs to.
	Layer int

	// Bounds is the position and size of the cel on the canvas.
	// For regular cels the pixel dimensions must match Image.
	Bounds image.Rectangle

	// Opacity is the cel opacity in the range [0, 255].
	Opacity uint8

	// LinkedFrame is the frame that a linked cel links to.
	// A negative value means that the cel is not linked.
	LinkedFrame int

	// Image holds the cel pixels. The concrete type depends on the
	// document's color depth:
	//
	//   - Indexed8:   *image.Paletted
	//   - Grayscale16: *image.Gray16
	//   - RGBA32:     *image.NRGBA
	//
	// The image's own origin is ignored; Bounds determines the on-canvas
	// position. It must be nil for linked cels.
	Image image.Image

	// Data is optional user data text attached to the cel.
	// Linked cels cannot carry user data.
	Data []byte
}

// SpriteFrame describes a single animation frame.
type SpriteFrame struct {
	// Duration is how long the frame is displayed. It is rounded down to
	// whole milliseconds and must fit in a uint16 (at most 65535ms).
	Duration time.Duration

	// Cels lists the cels that make up the frame, one cel per layer at most.
	// A frame must contain at least one regular (non-linked) cel.
	Cels []SpriteCel
}

// SliceKey describes a slice on a single frame. A slice with more than one
// key changes its geometry over the course of the animation.
type SliceKey struct {
	// Frame is the frame number from which the key is valid.
	Frame int

	// Bounds is the slice rectangle on the canvas.
	Bounds image.Rectangle

	// Center is the 9-patch center rectangle, relative to Bounds.Min.
	// It is only written when the parent slice has HasCenter set.
	Center image.Rectangle

	// Pivot is the pivot point, relative to Bounds.Min.
	// It is only written when the parent slice has HasPivot set.
	Pivot image.Point
}

// SpriteSlice describes a single slice.
type SpriteSlice struct {
	// Name is the slice name. SpriteSlice names must be unique.
	Name string

	// HasCenter reports whether the slice keys carry a 9-patch center.
	// Every key must provide Center when this is set.
	HasCenter bool

	// HasPivot reports whether the slice keys carry a pivot point.
	// Every key must provide Pivot when this is set.
	HasPivot bool

	// Keys lists the slice keys in ascending frame order.
	Keys []SliceKey

	// Data is optional user data text attached to the slice.
	Data []byte

	// Color is the optional user data color. It is written together with
	// Data. A nil color means that no color is written, in which case Data
	// alone is still written.
	Color color.Color
}

// SpriteTag describes an animation tag.
type SpriteTag struct {
	// Name is the tag name. SpriteTag names must be unique.
	Name string

	// Lo and Hi are the inclusive first and last frame of the tag.
	Lo, Hi int

	// Repeat is the repeat count. Zero means repeat forever.
	Repeat uint16

	// LoopDirection is the animation direction.
	LoopDirection LoopDirection

	// Data is optional user data text attached to the tag.
	Data []byte
}

// Sprite is an in-memory description of an Aseprite sprite. It is the
// controlled input of the writer: every field maps directly onto an Aseprite
// concept that the writer knows how to encode. Features that the writer does
// not support (tilemaps, layer groups, user data properties, color profiles,
// external files and so on) have no representation here and therefore cannot
// be silently dropped.
type Sprite struct {
	// Width and Height are the canvas dimensions in pixels.
	Width, Height int

	// BPP is the color depth: Indexed8, Grayscale16 or RGBA32.
	BPP BPP

	// Palette is the indexed color palette. It must be non-empty and contain
	// at most 256 entries when BPP is Indexed8; it must be nil otherwise.
	Palette color.Palette

	// TransparentIndex is the transparent palette index. It is only used
	// when BPP is Indexed8 and must be smaller than len(Palette).
	TransparentIndex int

	// Layers lists the sprite layers in rendering order.
	Layers []SpriteLayer

	// Frames lists the animation frames.
	Frames []SpriteFrame

	// Tags lists the animation tags.
	Tags []SpriteTag

	// Slices lists the slices.
	Slices []SpriteSlice
}

// Validation errors returned before any byte is written.
var (
	errInvalidBPP            = errors.New("aseprite: invalid color depth, want 8, 16 or 32 bpp")
	errInvalidDimension      = errors.New("aseprite: canvas width and height must be in [1, 32768]")
	errNoFrames              = errors.New("aseprite: document must contain at least one frame")
	errTooManyFrames         = errors.New("aseprite: too many frames, at most 65535")
	errDurationOutOfRange    = errors.New("aseprite: frame duration must be between 0 and 65535 milliseconds")
	errNoLayers              = errors.New("aseprite: document must contain at least one layer")
	errBlendModeOutOfRange   = errors.New("aseprite: layer blend mode must be in [0, 18]")
	errEmptyLayerName        = errors.New("aseprite: layer name must not be empty")
	errDuplicateLayerName    = errors.New("aseprite: duplicate layer name")
	errEmptyFrame            = errors.New("aseprite: frame contains no cels")
	errFrameWithoutRealCel   = errors.New("aseprite: every frame must contain at least one regular cel")
	errCelLayerOutOfRange    = errors.New("aseprite: cel layer index out of range")
	errDuplicateCel          = errors.New("aseprite: frame contains more than one cel on the same layer")
	errCelBoundsOutOfRange   = errors.New("aseprite: cel bounds are empty or outside the canvas")
	errLinkedFrameOutOfRange = errors.New("aseprite: linked cel must reference an earlier frame")
	errLinkedTargetMissing   = errors.New("aseprite: linked cel references a frame without a cel on that layer")
	errLinkedCelHasImage     = errors.New("aseprite: linked cel must not carry an image or user data")
	errRealCelMissingImage   = errors.New("aseprite: regular cel must carry an image")
	errCelImageType          = errors.New("aseprite: cel image type does not match the document color depth")
	errCelImageSize          = errors.New("aseprite: cel image pixel data does not match its bounds")
	errPaletteIndex          = errors.New("aseprite: cel pixel references a palette index that does not exist")
	errPaletteRequired       = errors.New("aseprite: indexed documents require a palette of 1 to 256 colors")
	errPaletteNotAllowed     = errors.New("aseprite: palette is only valid for indexed documents")
	errTransparentIndex      = errors.New("aseprite: transparent palette index out of range")
	errTagRange              = errors.New("aseprite: tag frame range out of bounds or reversed")
	errLoopDirection         = errors.New("aseprite: invalid tag loop direction")
	errEmptyTagName          = errors.New("aseprite: tag name must not be empty")
	errDuplicateTagName      = errors.New("aseprite: duplicate tag name")
	errEmptySliceName        = errors.New("aseprite: slice name must not be empty")
	errDuplicateSliceName    = errors.New("aseprite: duplicate slice name")
	errSliceNoKeys           = errors.New("aseprite: slice must contain at least one key")
	errSliceKeyFrame         = errors.New("aseprite: slice key frame out of range or not in ascending order")
	errSliceBounds           = errors.New("aseprite: slice key bounds are empty or outside the canvas")
	errSliceCenter           = errors.New("aseprite: slice center must lie within the slice bounds")
	errSlicePivot            = errors.New("aseprite: slice pivot must lie within the slice bounds")
	errTextTooLong           = errors.New("aseprite: user data text must be at most 65535 bytes")
	errInvalidText           = errors.New("aseprite: user data text and names must be valid UTF-8")
	errNameTooLong           = errors.New("aseprite: names must be at most 65535 bytes")
)

// validate checks the whole document before encoding starts. The document is
// never modified, so a validation failure leaves the caller's input intact.
func (doc *Sprite) validate() error {
	switch doc.BPP {
	case Indexed8, Grayscale16, RGBA32:
	default:
		return errInvalidBPP
	}

	if doc.Width < 1 || doc.Height < 1 || doc.Width > maxASEDimension || doc.Height > maxASEDimension {
		return errInvalidDimension
	}

	if len(doc.Frames) == 0 {
		return errNoFrames
	}
	if len(doc.Frames) > 65535 {
		return errTooManyFrames
	}

	if len(doc.Layers) == 0 {
		return errNoLayers
	}

	if doc.BPP == Indexed8 {
		if n := len(doc.Palette); n < 1 || n > 256 {
			return errPaletteRequired
		}
		if doc.TransparentIndex < 0 || doc.TransparentIndex >= len(doc.Palette) {
			return errTransparentIndex
		}
	} else {
		if len(doc.Palette) != 0 {
			return errPaletteNotAllowed
		}
		// The transparent index is only meaningful for indexed sprites;
		// reject a non-zero value rather than silently dropping it.
		if doc.TransparentIndex != 0 {
			return errTransparentIndex
		}
	}

	layerNames := make(map[string]struct{}, len(doc.Layers))
	for i := range doc.Layers {
		l := &doc.Layers[i]
		if l.BlendMode > 18 {
			return fmt.Errorf("%w: layer %d", errBlendModeOutOfRange, i)
		}
		if len(l.Name) == 0 {
			return fmt.Errorf("%w: layer %d", errEmptyLayerName, i)
		}
		if len(l.Name) > maxTextLen {
			return fmt.Errorf("%w: layer %d", errNameTooLong, i)
		}
		if !utf8.ValidString(l.Name) {
			return fmt.Errorf("%w: layer %q", errInvalidText, l.Name)
		}
		if _, ok := layerNames[l.Name]; ok {
			return fmt.Errorf("%w: %q", errDuplicateLayerName, l.Name)
		}
		layerNames[l.Name] = struct{}{}
		if len(l.Data) > maxTextLen {
			return fmt.Errorf("%w: layer %q", errTextTooLong, l.Name)
		}
		if !utf8.Valid(l.Data) {
			return fmt.Errorf("%w: layer %q data", errInvalidText, l.Name)
		}
	}

	for fi := range doc.Frames {
		fr := &doc.Frames[fi]
		durMS := fr.Duration.Milliseconds()
		if durMS < 0 || durMS > 65535 {
			return fmt.Errorf("%w: frame %d", errDurationOutOfRange, fi)
		}
		if len(fr.Cels) == 0 {
			return fmt.Errorf("%w: frame %d", errEmptyFrame, fi)
		}

		seen := make(map[int]struct{}, len(fr.Cels))
		hasRealCel := false
		for ci := range fr.Cels {
			c := &fr.Cels[ci]
			if c.Layer < 0 || c.Layer >= len(doc.Layers) {
				return fmt.Errorf("%w: frame %d cel %d", errCelLayerOutOfRange, fi, ci)
			}
			if _, ok := seen[c.Layer]; ok {
				return fmt.Errorf("%w: frame %d layer %d", errDuplicateCel, fi, c.Layer)
			}
			seen[c.Layer] = struct{}{}

			if c.LinkedFrame >= 0 {
				if c.LinkedFrame >= fi {
					return fmt.Errorf("%w: frame %d cel %d", errLinkedFrameOutOfRange, fi, ci)
				}
				if c.Image != nil || len(c.Data) != 0 {
					return fmt.Errorf("%w: frame %d cel %d", errLinkedCelHasImage, fi, ci)
				}
				// Bounds are not stored for linked cels (the position is
				// inherited from the source cel), but they must still be
				// sane if the caller provided them.
				if b := c.Bounds; !b.Empty() && (b.Min.X < 0 || b.Min.Y < 0 ||
					b.Max.X > doc.Width || b.Max.Y > doc.Height) {
					return fmt.Errorf("%w: frame %d cel %d", errCelBoundsOutOfRange, fi, ci)
				}
				continue
			}

			hasRealCel = true
			if c.Image == nil {
				return fmt.Errorf("%w: frame %d cel %d", errRealCelMissingImage, fi, ci)
			}
			if err := validateCelImage(doc, c, fi, ci); err != nil {
				return err
			}
			if len(c.Data) > maxTextLen {
				return fmt.Errorf("%w: frame %d cel %d", errTextTooLong, fi, ci)
			}
			if !utf8.Valid(c.Data) {
				return fmt.Errorf("%w: frame %d cel %d data", errInvalidText, fi, ci)
			}
		}
		if !hasRealCel {
			return fmt.Errorf("%w: frame %d", errFrameWithoutRealCel, fi)
		}
	}

	// Verify that every linked cel resolves, possibly transitively, to a
	// real cel on the same layer. Links always point at strictly earlier
	// frames, so the chains are acyclic and a simple walk terminates.
	celByFrameLayer := make([]map[int]*SpriteCel, len(doc.Frames))
	for fi := range doc.Frames {
		m := make(map[int]*SpriteCel, len(doc.Frames[fi].Cels))
		for ci := range doc.Frames[fi].Cels {
			m[doc.Frames[fi].Cels[ci].Layer] = &doc.Frames[fi].Cels[ci]
		}
		celByFrameLayer[fi] = m
	}
	for fi := range doc.Frames {
		for ci := range doc.Frames[fi].Cels {
			c := &doc.Frames[fi].Cels[ci]
			if c.LinkedFrame < 0 {
				continue
			}
			j := c.LinkedFrame
			for {
				target := celByFrameLayer[j][c.Layer]
				if target == nil {
					return fmt.Errorf("%w: frame %d cel %d", errLinkedTargetMissing, fi, ci)
				}
				if target.LinkedFrame < 0 {
					break // reached a real cel carrying pixels
				}
				j = target.LinkedFrame
			}
		}
	}

	tagNames := make(map[string]struct{}, len(doc.Tags))
	for i := range doc.Tags {
		t := &doc.Tags[i]
		if t.Lo < 0 || t.Hi < t.Lo || t.Hi >= len(doc.Frames) {
			return fmt.Errorf("%w: tag %d", errTagRange, i)
		}
		if t.LoopDirection > PingPongReverse {
			return fmt.Errorf("%w: tag %d", errLoopDirection, i)
		}
		if len(t.Name) == 0 {
			return fmt.Errorf("%w: tag %d", errEmptyTagName, i)
		}
		if len(t.Name) > maxTextLen {
			return fmt.Errorf("%w: tag %d", errNameTooLong, i)
		}
		if !utf8.ValidString(t.Name) {
			return fmt.Errorf("%w: tag %q", errInvalidText, t.Name)
		}
		if _, ok := tagNames[t.Name]; ok {
			return fmt.Errorf("%w: %q", errDuplicateTagName, t.Name)
		}
		tagNames[t.Name] = struct{}{}
		if len(t.Data) > maxTextLen {
			return fmt.Errorf("%w: tag %q", errTextTooLong, t.Name)
		}
		if !utf8.Valid(t.Data) {
			return fmt.Errorf("%w: tag %q data", errInvalidText, t.Name)
		}
	}

	sliceNames := make(map[string]struct{}, len(doc.Slices))
	for i := range doc.Slices {
		s := &doc.Slices[i]
		if len(s.Name) == 0 {
			return fmt.Errorf("%w: slice %d", errEmptySliceName, i)
		}
		if len(s.Name) > maxTextLen {
			return fmt.Errorf("%w: slice %d", errNameTooLong, i)
		}
		if !utf8.ValidString(s.Name) {
			return fmt.Errorf("%w: slice %q", errInvalidText, s.Name)
		}
		if _, ok := sliceNames[s.Name]; ok {
			return fmt.Errorf("%w: %q", errDuplicateSliceName, s.Name)
		}
		sliceNames[s.Name] = struct{}{}
		if len(s.Data) > maxTextLen {
			return fmt.Errorf("%w: slice %q", errTextTooLong, s.Name)
		}
		if !utf8.Valid(s.Data) {
			return fmt.Errorf("%w: slice %q data", errInvalidText, s.Name)
		}
		if len(s.Keys) == 0 {
			return fmt.Errorf("%w: slice %q", errSliceNoKeys, s.Name)
		}
		prevFrame := -1
		for ki := range s.Keys {
			k := &s.Keys[ki]
			if k.Frame < 0 || k.Frame >= len(doc.Frames) || k.Frame <= prevFrame {
				return fmt.Errorf("%w: slice %q key %d", errSliceKeyFrame, s.Name, ki)
			}
			prevFrame = k.Frame
			b := k.Bounds
			if b.Empty() || b.Min.X < 0 || b.Min.Y < 0 || b.Max.X > doc.Width || b.Max.Y > doc.Height {
				return fmt.Errorf("%w: slice %q key %d", errSliceBounds, s.Name, ki)
			}
			if s.HasCenter {
				c := k.Center
				if c.Empty() || c.Min.X < 0 || c.Min.Y < 0 ||
					c.Max.X > b.Dx() || c.Max.Y > b.Dy() {
					return fmt.Errorf("%w: slice %q key %d", errSliceCenter, s.Name, ki)
				}
			}
			if s.HasPivot {
				p := k.Pivot
				if p.X < 0 || p.Y < 0 || p.X > b.Dx() || p.Y > b.Dy() {
					return fmt.Errorf("%w: slice %q key %d", errSlicePivot, s.Name, ki)
				}
			}
		}
	}

	return nil
}

func validateCelImage(doc *Sprite, c *SpriteCel, fi, ci int) error {
	b := c.Bounds
	if b.Empty() || b.Min.X < 0 || b.Min.Y < 0 || b.Max.X > doc.Width || b.Max.Y > doc.Height {
		return fmt.Errorf("%w: frame %d cel %d", errCelBoundsOutOfRange, fi, ci)
	}
	w, h := b.Dx(), b.Dy()

	switch img := c.Image.(type) {
	case *image.Paletted:
		if doc.BPP != Indexed8 {
			return fmt.Errorf("%w: frame %d cel %d", errCelImageType, fi, ci)
		}
		if img.Stride != w || len(img.Pix) != w*h || img.Rect.Dx() != w || img.Rect.Dy() != h {
			return fmt.Errorf("%w: frame %d cel %d", errCelImageSize, fi, ci)
		}
		for _, idx := range img.Pix {
			if int(idx) >= len(doc.Palette) {
				return fmt.Errorf("%w: frame %d cel %d", errPaletteIndex, fi, ci)
			}
		}
	case *image.Gray16:
		if doc.BPP != Grayscale16 {
			return fmt.Errorf("%w: frame %d cel %d", errCelImageType, fi, ci)
		}
		if img.Stride != w*2 || len(img.Pix) != w*h*2 || img.Rect.Dx() != w || img.Rect.Dy() != h {
			return fmt.Errorf("%w: frame %d cel %d", errCelImageSize, fi, ci)
		}
	case *image.NRGBA:
		if doc.BPP != RGBA32 {
			return fmt.Errorf("%w: frame %d cel %d", errCelImageType, fi, ci)
		}
		if img.Stride != w*4 || len(img.Pix) != w*h*4 || img.Rect.Dx() != w || img.Rect.Dy() != h {
			return fmt.Errorf("%w: frame %d cel %d", errCelImageSize, fi, ci)
		}
	default:
		return fmt.Errorf("%w: frame %d cel %d", errCelImageType, fi, ci)
	}
	return nil
}

// encoder builds the file into an in-memory buffer. The buffer is only copied
// to the destination io.Writer once the entire document has been encoded, so a
// write failure can never produce a truncated file or alter the input.
type encoder struct {
	buf        bytes.Buffer
	doc        *Sprite
	compressed bool
}

func (e *encoder) write16(v uint16) {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], v)
	e.buf.Write(b[:])
}

func (e *encoder) write32(v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	e.buf.Write(b[:])
}

func (e *encoder) write8(v byte) {
	e.buf.WriteByte(v)
}

// beginChunk reserves the 6-byte common chunk header and returns the position
// at which the chunk size must be backfilled by endChunk.
func (e *encoder) beginChunk(typ uint16) int {
	pos := e.buf.Len()
	e.write32(0)
	e.write16(typ)
	return pos
}

func (e *encoder) endChunk(pos int) {
	binary.LittleEndian.PutUint32(e.buf.Bytes()[pos:], uint32(e.buf.Len()-pos))
}

func (e *encoder) writeString(s string) {
	e.write16(uint16(len(s)))
	e.buf.WriteString(s)
}

// beginFrame reserves the 16-byte frame header.
func (e *encoder) beginFrame() int {
	pos := e.buf.Len()
	e.write32(0) // frame length, backfilled by endFrame
	e.write16(aseFrameMagic)
	e.write16(0xFFFF) // old chunk count: use the new field
	e.write16(0)      // duration, overwritten by endFrame
	e.write16(0)      // reserved
	e.write32(0)      // new chunk count, overwritten by endFrame
	return pos
}

func (e *encoder) endFrame(pos int, durationMS uint16, nchunks uint32) {
	b := e.buf.Bytes()[pos:]
	binary.LittleEndian.PutUint32(b[0:], uint32(e.buf.Len()-pos))
	binary.LittleEndian.PutUint16(b[8:], durationMS)
	binary.LittleEndian.PutUint32(b[12:], nchunks)
}

// writeUserDataChunk always emits a user data chunk, even when both text and
// color are empty (flags 0). This is required after the tags chunk because
// the Aseprite reader pairs tag user data chunks positionally, one per tag.
func (e *encoder) writeUserDataChunk(text []byte, col color.Color) {
	pos := e.beginChunk(aseChunkUserData)
	var flags uint32
	if len(text) > 0 {
		flags |= 1 // has text
	}
	if col != nil {
		flags |= 2 // has color
	}
	e.write32(flags)
	if len(text) > 0 {
		e.write16(uint16(len(text)))
		e.buf.Write(text)
	}
	if col != nil {
		r, g, b, a := col.RGBA()
		e.write8(byte(r >> 8))
		e.write8(byte(g >> 8))
		e.write8(byte(b >> 8))
		e.write8(byte(a >> 8))
	}
	e.endChunk(pos)
}

// writeUserData emits a user data chunk only when it carries content. It is
// used after layer, cel and slice chunks, where no positional pairing applies.
func (e *encoder) writeUserData(text []byte, col color.Color) {
	if len(text) == 0 && col == nil {
		return
	}
	e.writeUserDataChunk(text, col)
}

func (e *encoder) writePaletteChunk() {
	pos := e.beginChunk(aseChunkPalette)
	n := uint32(len(e.doc.Palette))
	e.write32(n)     // total entries
	e.write32(0)     // first index
	e.write32(n - 1) // last index
	e.write32(0)     // reserved
	e.write32(0)     // reserved
	for _, c := range e.doc.Palette {
		r, g, b, a := c.RGBA()
		e.write16(0) // entry flags: no name
		e.write8(byte(r >> 8))
		e.write8(byte(g >> 8))
		e.write8(byte(b >> 8))
		e.write8(byte(a >> 8))
	}
	e.endChunk(pos)
}

func (e *encoder) writeTagsChunk() {
	pos := e.beginChunk(aseChunkTags)
	e.write16(uint16(len(e.doc.Tags)))
	e.write32(0) // 8 reserved bytes
	e.write32(0)
	for i := range e.doc.Tags {
		t := &e.doc.Tags[i]
		e.write16(uint16(t.Lo))
		e.write16(uint16(t.Hi))
		e.write8(byte(t.LoopDirection))
		e.write16(t.Repeat)
		e.write16(0) // 6 reserved bytes
		e.write32(0)
		e.write8(0) // deprecated RGB tag color
		e.write8(0)
		e.write8(0)
		e.write8(0)
		e.writeString(t.Name)
	}
	e.endChunk(pos)

	// One user data chunk per tag, unconditionally and in tag order. The
	// Aseprite reader pairs these with tags positionally, so tags without
	// data get an empty (flags 0) chunk.
	for i := range e.doc.Tags {
		e.writeUserDataChunk(e.doc.Tags[i].Data, nil)
	}
}

func (e *encoder) writeLayerChunks() {
	for i := range e.doc.Layers {
		l := &e.doc.Layers[i]
		pos := e.beginChunk(aseChunkLayer)
		var flags uint16
		if l.Visible {
			flags |= 1
		}
		if l.Editable {
			flags |= 2
		}
		if l.Background {
			flags |= 8
		}
		if l.Reference {
			flags |= 64
		}
		e.write16(flags)
		e.write16(0) // type: regular image layer
		e.write16(0) // child level: flat hierarchy
		e.write16(0) // default width, ignored
		e.write16(0) // default height, ignored
		e.write16(l.BlendMode)
		e.write8(l.Opacity)
		e.write8(0) // 3 reserved bytes
		e.write8(0)
		e.write8(0)
		e.writeString(l.Name)
		e.endChunk(pos)

		e.writeUserData(l.Data, nil)
	}
}

func (e *encoder) writeSliceChunks() {
	for i := range e.doc.Slices {
		s := &e.doc.Slices[i]
		pos := e.beginChunk(aseChunkSlice)
		var flags uint32
		if s.HasCenter {
			flags |= 1
		}
		if s.HasPivot {
			flags |= 2
		}
		e.write32(uint32(len(s.Keys)))
		e.write32(flags)
		e.write32(0) // reserved
		e.writeString(s.Name)
		for ki := range s.Keys {
			k := &s.Keys[ki]
			b := k.Bounds
			e.write32(uint32(k.Frame))
			e.write32(uint32(int32(b.Min.X)))
			e.write32(uint32(int32(b.Min.Y)))
			e.write32(uint32(b.Dx()))
			e.write32(uint32(b.Dy()))
			if s.HasCenter {
				c := k.Center
				e.write32(uint32(int32(c.Min.X)))
				e.write32(uint32(int32(c.Min.Y)))
				e.write32(uint32(c.Dx()))
				e.write32(uint32(c.Dy()))
			}
			if s.HasPivot {
				e.write32(uint32(int32(k.Pivot.X)))
				e.write32(uint32(int32(k.Pivot.Y)))
			}
		}
		e.endChunk(pos)

		e.writeUserData(s.Data, s.Color)
	}
}

// encodePixels converts a cel image into the on-disk pixel stream. The pixel
// packing follows the PIXEL definition in the Aseprite file format spec.
func (e *encoder) encodePixels(img image.Image) ([]byte, error) {
	switch v := img.(type) {
	case *image.Paletted:
		// Indexed: one palette index byte per pixel.
		return append([]byte(nil), v.Pix...), nil
	case *image.Gray16:
		// Grayscale: one Value byte followed by one Alpha byte per pixel.
		// The reader rebuilds a Gray16 word from the (V, A) pair as V|A<<8,
		// so the inverse convention is used here: the low byte of each
		// Gray16 word holds the value and the high byte holds the alpha.
		// This makes write/read round trips byte-exact.
		out := make([]byte, len(v.Pix))
		for i := 0; i < len(v.Pix); i += 2 {
			word := binary.BigEndian.Uint16(v.Pix[i:])
			out[i] = byte(word)        // value
			out[i+1] = byte(word >> 8) // alpha
		}
		return out, nil
	case *image.NRGBA:
		// RGBA: the on-disk order is Red, Green, Blue, Alpha; image.NRGBA
		// stores the same order, so the pixel stream is a straight copy.
		return append([]byte(nil), v.Pix...), nil
	default:
		return nil, errCelImageType
	}
}

func (e *encoder) writeCelChunk(c *SpriteCel) error {
	pos := e.beginChunk(aseChunkCel)
	e.write16(uint16(c.Layer))
	e.write16(uint16(int16(c.Bounds.Min.X)))
	e.write16(uint16(int16(c.Bounds.Min.Y)))
	e.write8(c.Opacity)

	if c.LinkedFrame >= 0 {
		e.write16(aseCelLinked)
		e.write16(0) // z-index
		e.write8(0)  // 5 reserved bytes
		e.write8(0)
		e.write8(0)
		e.write8(0)
		e.write8(0)
		e.write16(uint16(c.LinkedFrame))
		e.endChunk(pos)
		return nil
	}

	pix, err := e.encodePixels(c.Image)
	if err != nil {
		return err
	}
	w, h := c.Bounds.Dx(), c.Bounds.Dy()

	if e.compressed {
		e.write16(aseCelCompressed)
	} else {
		e.write16(aseCelRaw)
	}
	e.write16(0) // z-index
	e.write8(0)  // 5 reserved bytes
	e.write8(0)
	e.write8(0)
	e.write8(0)
	e.write8(0)
	e.write16(uint16(w))
	e.write16(uint16(h))

	if e.compressed {
		var zb bytes.Buffer
		zw := zlib.NewWriter(&zb)
		if _, err := zw.Write(pix); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
		e.buf.Write(zb.Bytes())
	} else {
		e.buf.Write(pix)
	}

	e.endChunk(pos)
	return nil
}

// countChunks returns the number of chunks in each frame. The count includes
// every user data chunk as its own chunk, matching Aseprite itself.
func (doc *Sprite) countChunks() []uint32 {
	counts := make([]uint32, len(doc.Frames))
	for fi := range doc.Frames {
		var n uint32
		for ci := range doc.Frames[fi].Cels {
			n++ // cel chunk
			if doc.Frames[fi].Cels[ci].LinkedFrame < 0 && len(doc.Frames[fi].Cels[ci].Data) > 0 {
				n++ // cel user data
			}
		}
		counts[fi] = n
	}

	// All structural chunks live in the first frame.
	var n uint32
	if e := doc.BPP; e == Indexed8 {
		n++ // palette
	}
	if len(doc.Tags) > 0 {
		n++                        // tags chunk
		n += uint32(len(doc.Tags)) // one user data chunk per tag, possibly empty
	}
	for i := range doc.Layers {
		n++ // layer chunk
		if len(doc.Layers[i].Data) > 0 {
			n++
		}
	}
	for i := range doc.Slices {
		n++ // slice chunk
		if len(doc.Slices[i].Data) > 0 || doc.Slices[i].Color != nil {
			n++
		}
	}
	counts[0] += n
	return counts
}

func (e *encoder) encode() error {
	headerPos := e.buf.Len()
	e.buf.Write(make([]byte, 128)) // file header, backfilled later

	chunkCounts := e.doc.countChunks()

	for fi := range e.doc.Frames {
		fr := &e.doc.Frames[fi]
		framePos := e.beginFrame()

		if fi == 0 {
			if e.doc.BPP == Indexed8 {
				e.writePaletteChunk()
			}
			if len(e.doc.Tags) > 0 {
				e.writeTagsChunk()
			}
			e.writeLayerChunks()
			e.writeSliceChunks()
		}

		// Emit cels in layer order regardless of the input order. The
		// document itself is never reordered.
		cels := append([]SpriteCel(nil), fr.Cels...)
		sort.SliceStable(cels, func(i, j int) bool { return cels[i].Layer < cels[j].Layer })
		for ci := range cels {
			if err := e.writeCelChunk(&cels[ci]); err != nil {
				return err
			}
			if cels[ci].LinkedFrame < 0 {
				e.writeUserData(cels[ci].Data, nil)
			}
		}

		e.endFrame(framePos, uint16(fr.Duration.Milliseconds()), chunkCounts[fi])
	}

	e.writeFileHeader(headerPos)
	return nil
}

func (e *encoder) writeFileHeader(pos int) {
	b := e.buf.Bytes()[pos:]
	binary.LittleEndian.PutUint32(b[0:], uint32(e.buf.Len()-pos))
	binary.LittleEndian.PutUint16(b[4:], aseHeaderMagic)
	binary.LittleEndian.PutUint16(b[6:], uint16(len(e.doc.Frames)))
	binary.LittleEndian.PutUint16(b[8:], uint16(e.doc.Width))
	binary.LittleEndian.PutUint16(b[10:], uint16(e.doc.Height))
	binary.LittleEndian.PutUint16(b[12:], uint16(e.doc.BPP))
	binary.LittleEndian.PutUint32(b[14:], aseFlagLayerOpacity)
	binary.LittleEndian.PutUint16(b[18:], uint16(e.doc.Frames[0].Duration.Milliseconds()))
	// b[20:24] and b[24:28] are zero
	b[28] = byte(e.doc.TransparentIndex)
	// b[29:32] ignored bytes are zero
	// Number of colors: the actual palette size for indexed sprites, and
	// 256 for grayscale/RGBA sprites, matching files written by Aseprite
	// itself. The field is ignored for non-indexed color depths.
	var ncolors uint16 = 256
	if e.doc.BPP == Indexed8 {
		ncolors = uint16(len(e.doc.Palette))
	}
	binary.LittleEndian.PutUint16(b[32:], ncolors)
	b[34] = 1 // pixel ratio width
	b[35] = 1 // pixel ratio height
	// grid x/y/width/height and the 84 reserved bytes stay zero.
}

// EncodeOption configures the writer.
type EncodeOption func(*encoder)

// WithCompressedCels compresses cel pixel data with zlib (cel type 2) instead
// of writing raw pixels (cel type 0).
func WithCompressedCels() EncodeOption {
	return func(e *encoder) {
		e.compressed = true
	}
}

// WriteTo encodes the document as a standard Aseprite file and writes it to w.
//
// The document is validated and fully encoded into an internal buffer before
// anything is written to w. On validation failure or a write error the input
// document is returned untouched and no partial file is produced.
func (doc *Sprite) WriteTo(w io.Writer, opts ...EncodeOption) (int64, error) {
	if err := doc.validate(); err != nil {
		return 0, err
	}

	e := encoder{doc: doc}
	for _, opt := range opts {
		opt(&e)
	}
	if err := e.encode(); err != nil {
		return 0, err
	}

	n, err := w.Write(e.buf.Bytes())
	if err != nil {
		return int64(n), err
	}
	return int64(e.buf.Len()), nil
}
