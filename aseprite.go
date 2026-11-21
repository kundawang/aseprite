// Package aseprite implements a decoder for Aseprite sprite files.
//
// Layers are flattened, blending modes are applied,
// and frames are arranged on a single texture atlas.
// Invisible and reference layers are not composited, but their metadata is
// still reported in Layers and Frame.Cels.
// Tilesets and external files are not supported.
//
// Layer, cel and slice user data is exposed in structured form through
// UserData, Layers, Frame.Cels and Slice.UserData. Text is preserved
// byte-for-byte (including invalid UTF-8), colors keep their alpha channel,
// and a malformed user data chunk is reported on UserData.Err without
// affecting pixel decoding.
//
// Aseprite file format spec: https://github.com/aseprite/aseprite/blob/main/docs/ase-file-specs.md
package aseprite

import (
	"image"
	"image/color"
	"io"
	"time"
)

// LoopDirection enumerates all loop animation directions.
type LoopDirection int

const (
	Forward LoopDirection = 0
	Reverse
	PingPong
	PingPongReverse
)

// Tag is an animation tag.
type Tag struct {
	// Name is the name of the tag. Can be duplicate.
	Name string

	// Flags is a bitset of flags.
	Flags int

	// Repeat specifies how many times to repeat the animation.
	Repeat int

	// Lo is the first frame in the animation.
	Lo int

	// Hi is the last frame in the animation.
	Hi int
}

// LoopDirection returns the looping direction of the animation.
func (t *Tag) LoopDirection() LoopDirection {
	return LoopDirection(t.Flags & 3)
}

// UserData is the optional user data attached to a layer, cel, or slice.
//
// Aseprite stores user data in a chunk (0x2020) with a flag word that says
// which optional fields are present. HasText and HasColor distinguish a field
// that is absent from a field that is present but empty/zero.
//
// Text is kept byte-for-byte. A file may contain text that is not valid UTF-8;
// such text is preserved in Text and TextBytes and reported by ValidUTF8. A
// malformed or truncated chunk never fails the sprite decode. As much data as
// possible is preserved and Err describes the problem.
//
// The zero value means that no user data chunk was attached.
type UserData struct {
	// Present reports whether a user data chunk was attached at all.
	// An attached chunk can still carry neither text nor a color.
	Present bool

	// Flags is the raw user data flags word, including unknown bits.
	Flags uint32

	// UnknownFlags contains the flag bits that this package does not
	// understand (Flags &^ 3). They are preserved, and the known fields are
	// parsed as usual, so a newer file with extra flags still decodes.
	UnknownFlags uint32

	// HasText reports whether the text field is present. It is true for an
	// empty text field, which is different from a missing text field.
	HasText bool

	// Text is the text field interpreted as a Go string. It may contain
	// invalid UTF-8; see ValidUTF8.
	Text string

	// TextBytes is a copy of the raw text field bytes. It is non-nil when
	// HasText is true, including when the text is empty.
	TextBytes []byte

	// ValidUTF8 reports whether TextBytes is valid UTF-8. Invalid bytes are
	// preserved rather than replaced.
	ValidUTF8 bool

	// HasColor reports whether the color field is present. It is true even
	// when the color is fully transparent.
	HasColor bool

	// Color is the color field. It is meaningful only when HasColor is true.
	// The color is kept as NRGBA so that its alpha channel is not quantized
	// or premultiplied.
	Color color.NRGBA

	// Raw is a copy of the complete user data chunk, including the flags word
	// and any bytes that could not be parsed.
	Raw []byte

	// ChunkIndex is the index of the user data chunk within its frame's chunk
	// list. It is -1 when no chunk was attached.
	ChunkIndex int

	// Err reports a malformed chunk. Errors wrap ErrTruncatedUserData where
	// applicable. A non-nil Err does not prevent the sprite from being
	// decoded; the pixel data is independent of user data.
	Err error
}

// ColorValue returns Color as a color.Color, or nil when no color field is
// present. It is provided for compatibility with APIs that use color.Color.
func (u UserData) ColorValue() color.Color {
	if !u.HasColor {
		return nil
	}
	return u.Color
}

// Layer is the metadata of a layer, including its optional user data.
type Layer struct {
	// Index is the layer index in the file. It is the index referenced by
	// cel chunks and by Aseprite.Layers.
	Index int

	// Name is the layer name. Can be duplicate.
	Name string

	// Flags is the raw layer flags word. Bit 0 is visibility, bit 6 marks a
	// reference layer, and bit 3 marks a background layer.
	Flags uint16

	// Type is the raw layer type: 0 for a normal layer, 1 for a group and 2
	// for a tilemap layer.
	Type uint16

	// BlendMode is the raw blend mode value.
	BlendMode uint16

	// Opacity is the layer opacity in the range 0-255.
	Opacity byte

	// Visible reports whether the layer is visible (flags bit 0).
	Visible bool

	// Reference reports whether the layer is a reference layer (flags bit 6).
	Reference bool

	// UserData is the layer's optional user data.
	UserData UserData
}

// Cel is the metadata of a single cel chunk. It exists in addition to the
// flattened image so callers can associate user data with the layer and frame
// that produced it, including for cels that were not composited.
type Cel struct {
	// Frame is the frame index in Aseprite.Frames.
	Frame int

	// Layer is the layer index in Aseprite.Layers.
	Layer int

	// Chunk is the index of the cel chunk within this frame's chunk list.
	Chunk int

	// Type is the raw cel type: 0 for an uncompressed image, 1 for a linked
	// cel and 2 for a compressed image.
	Type int

	// Bounds is the cel image bounds in sprite coordinates. Linked cels take
	// the bounds of the cel they link to.
	Bounds image.Rectangle

	// Composited reports whether the cel contributed pixels to the atlas.
	// Invisible and reference layers are not composited, but their metadata
	// (including user data) is still reported.
	Composited bool

	// UserData is the cel's optional user data. Linked cels cannot carry
	// their own user data in the Aseprite format; their metadata is reported
	// with Present false.
	UserData UserData
}

// Frame represents a single frame in the sprite.
type Frame struct {
	// Bounds is the image bounds of the frame in the sprite's atlas.
	Bounds image.Rectangle

	// Duration is the time in seconds that the frame should be displayed for
	// in a tag animation loop.
	Duration time.Duration

	// Data lists all optional user data set in the cels that make up the frame.
	// The data of invisible and reference layers is not included. It is kept
	// for compatibility; Cels provides the structured equivalent with source
	// indices, text and colors.
	Data [][]byte

	// Cels lists the metadata of every cel chunk in this frame, in file order.
	// It includes cels on invisible and reference layers, which are not
	// composited into the image (see Cel.Composited).
	Cels []Cel
}

// Slice represents a single slice.
type Slice struct {
	// Bounds is the bounds of the image in the texture atlas.
	Bounds image.Rectangle

	// Center is the 9-slices center relative to Bounds.
	Center image.Rectangle

	// Pivot is the pivot point relative to Bounds.
	Pivot image.Point

	// Name is name of the slice. Can be duplicate.
	Name string

	// Index is the slice index in Aseprite.Slices. Slices with the same Name
	// can be told apart by their Index.
	Index int

	// Chunk is the index of the slice chunk within the first frame's chunk
	// list.
	Chunk int

	// Data is optional user data text bytes. It is the same as
	// UserData.TextBytes and is kept for compatibility.
	Data []byte

	// Color is the slice color, or nil if no color field is present. It is
	// the same as UserData.Color and is kept for compatibility.
	Color color.Color

	// UserData is the slice's structured optional user data.
	UserData UserData
}

// Aseprite holds the results of a parsed .ase or .aseprite file.
type Aseprite struct {
	// Image contains all frame images in a single image.
	// Frame bounds specify where the frame images are located.
	image.Image

	// Frames lists all frames that make up the sprite.
	Frames []Frame

	// Tags lists all animation tags.
	Tags []Tag

	// Slices lists all slices.
	Slices []Slice

	// LayerData lists the user data of all visible layers. It is kept for
	// compatibility; Layers provides the structured equivalent.
	LayerData [][]byte

	// Layers lists the metadata of every layer, in file order, including
	// invisible and reference layers.
	Layers []Layer
}

func (spr *Aseprite) readFrom(r io.Reader) error {
	var f file

	if _, err := f.ReadFrom(r); err != nil {
		return err
	}

	f.initPalette()

	if err := f.initLayers(); err != nil {
		return err
	}

	if err := f.initCels(); err != nil {
		return err
	}

	var framesr []image.Rectangle
	spr.Image, framesr = f.buildAtlas()
	userdata := f.buildUserData()
	spr.Frames, userdata = f.buildFrames(framesr, userdata)
	spr.LayerData = f.buildLayerData(userdata)
	spr.Layers = f.buildLayers()
	spr.Tags = f.buildTags()
	spr.Slices = f.buildSlices()
	return nil
}
