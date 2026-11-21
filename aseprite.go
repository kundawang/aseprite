// Package aseprite implements a decoder for Aseprite sprite files.
//
// Layers are flattened, blending modes are applied,
// and frames are arranged on a single texture atlas.
// Invisible and reference layers are ignored.
// Tilesets and external files are not supported.
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
type LoopDirection uint8

const (
	Forward LoopDirection = iota
	Reverse
	PingPong
	PingPongReverse
)

// Tag is an animation tag.
type Tag struct {
	// Name is the name of the tag. Can be duplicate.
	Name string

	// Lo is the first frame in the animation.
	Lo uint16

	// Hi is the last frame in the animation.
	Hi uint16

	// Repeat specifies how many times to repeat the animation.
	Repeat uint16

	// LoopDirection is the looping direction of the animation.
	LoopDirection LoopDirection
}

// Frame represents a single frame in the sprite.
type Frame struct {
	// Bounds is the image bounds of the frame's content in its page.
	// Coordinates are page-local and match the page in Frame.Page.
	// A frame without any visible cels has an empty rectangle.
	Bounds image.Rectangle

	// Page is the zero-based index of the atlas page that contains
	// the frame, corresponding to an entry in Aseprite.Pages.
	Page int

	// Padding is the number of padding pixels extruded around Bounds
	// on every side. It matches Options.Padding and is zero when the
	// sprite was decoded without padding.
	Padding int

	// UV is the half-open normalized texture-coordinate rectangle of
	// Bounds within its page. See FrameUV and UVRect for the exact
	// half-open, non-square-page and negative-origin semantics.
	UV UVRect

	// Rotated reports whether the frame is stored rotated 90 degrees
	// clockwise on the page. The decoder never rotates frames, so it
	// is always false; the field is reserved for exporters and custom
	// packers and documented so renderers handle it explicitly.
	Rotated bool

	// FlipH and FlipV report whether the frame is stored mirrored
	// horizontally or vertically. The decoder never flips frames, so
	// both are always false; the fields are reserved for exporters
	// and custom packers.
	FlipH bool
	FlipV bool

	// Duration is the time in seconds that the frame should be displayed for
	// in a tag animation loop.
	Duration time.Duration

	// Data lists all optional user data set in the cels that make up the frame.
	// The data of invisible and reference layers is not included.
	Data [][]byte
}

// Slice represents a single slice.
type Slice struct {
	// Bounds is the bounds of the image in the texture atlas.
	Bounds image.Rectangle

	// Center is the 9-slices center relative to Bounds.
	Center image.Rectangle

	// Pivot is the pivot point relative to Bounds.
	Pivot image.Point

	// Name is the name of the slice. Can be duplicate.
	Name string

	// Data is optional user data.
	Data []byte

	// Color is the slice color.
	Color color.Color
}

// Aseprite holds the results of a parsed Aseprite image file.
type Aseprite struct {
	// Image contains the first atlas page for backwards
	// compatibility. Single-page sprites hold the whole atlas here.
	// Prefer Pages when iterating frames.
	image.Image

	// Pages lists the atlas pages in page order. Frame.Page indexes
	// into this slice. The slice is never empty for a successfully
	// decoded sprite and its order is stable.
	Pages []Page

	// Frames lists all frames that make up the sprite.
	// The order is the frame order stored in the sprite file.
	Frames []Frame

	// Tags lists all animation tags.
	Tags []Tag

	// Slices lists all slices.
	Slices []Slice

	// LayerData lists the user data of all visible layers.
	LayerData [][]byte
}

func (spr *Aseprite) readFrom(r io.Reader, opts Options) error {
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

	userdata := f.buildUserData()
	var placements []framePlacement
	spr.Pages, placements = f.buildAtlas(opts)
	spr.Image = spr.Pages[0].Image
	spr.Frames, userdata = f.buildFrames(placements, userdata)
	for i := range spr.Frames {
		spr.Frames[i].UV = FrameUV(spr.Pages[spr.Frames[i].Page].Bounds, spr.Frames[i].Bounds)
	}
	spr.LayerData = f.buildLayerData(userdata)
	spr.Tags = f.buildTags()
	spr.Slices = f.buildSlices()
	return nil
}
