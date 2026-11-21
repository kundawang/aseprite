// Package aseprite parses Aseprite sprite files.
//
// Layers are parsed into a tree of normal and group layers.
// Frames can be composited from the whole tree or from a
// selection of its nodes, honoring group opacity and blend modes.
// The flattened atlas results are retained for compatibility.
// Tilesets and external files are not supported.
package aseprite

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"time"
)

// ErrLayerTreeCycle is returned when a layer is its own ancestor.
var ErrLayerTreeCycle = errors.New("cycle in layer tree")

// ErrNoParentGroup is returned when a layer's child level implies
// a parent group that does not exist in the file.
var ErrNoParentGroup = errors.New("layer has no parent group")

// Layer is a node in the sprite's layer tree.
type Layer struct {
	// Name is the name of the layer. Can be duplicate.
	Name string

	// Index is the layer's position in the file's flat layer list,
	// ordered bottom to top.
	Index int

	// Group reports whether the layer is a group layer.
	Group bool

	// Visible reports whether the layer is visible in the editor.
	Visible bool

	// Reference reports whether the layer is a reference layer.
	Reference bool

	// Opacity is the layer's own opacity, 0-255. For groups it applies
	// to the composited result of all children.
	Opacity uint8

	// BlendMode is the layer's blend mode; 0 is normal.
	BlendMode uint16

	// Parent is the enclosing group, or nil for top-level layers.
	Parent *Layer

	// Children lists the group's members, ordered bottom to top.
	// It is nil for normal layers and empty groups.
	Children []*Layer

	// Data is optional user data attached to the layer.
	Data []byte
}

// LoopDirection enumerates all loop animation directions.
type LoopDirection int

const (
	Forward LoopDirection = 0
	Reverse
	PingPong
)

// Tag is an animation tag.
type Tag struct {
	// Name is the name of the tag. Can be duplicate.
	Name string

	// Flags is a bitset of flags.
	Flags uint32

	// Lo is the first frame in the animation.
	Lo uint16

	// Hi is the last frame in the animation.
	Hi uint16
}

// LoopDirection returns the looping direction of the animation.
func (t *Tag) LoopDirection() LoopDirection {
	return LoopDirection(t.Flags & 3)
}

// Frame represents a single frame in the sprite.
type Frame struct {
	// Bounds is the image bounds of the frame in the sprite's atlas.
	Bounds image.Rectangle

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

	// Name is name of the slice. Can be duplicate.
	Name string

	// Data is optional user data.
	Data []byte

	// Color is the slice color.
	Color color.Color
}

// Aseprite holds the results of a parsed .ase or .aseprite file.
type Aseprite struct {
	// Atlas contains all frame images in a single image.
	// Frame bounds specify where the frame images are located.
	Atlas image.Image

	// Frames lists all frames that make up the sprite.
	Frames []Frame

	// Tags lists all animation tags.
	Tags []Tag

	// Slices lists all slices.
	Slices []Slice

	// LayerData lists the user data of all visible layers.
	LayerData [][]byte

	// Layers lists the top-level nodes of the layer tree,
	// ordered bottom to top.
	Layers []*Layer

	// file retains the parsed contents for FrameImage.
	file *file
}

// buildLayerTree links the file's flat layer list into a tree using
// each layer's child level. In the file, layers are ordered bottom to
// top and a group's children immediately precede the group, so the tree
// is built by scanning the list from top to bottom.
func buildLayerTree(layers []layer) ([]*Layer, error) {
	nodes := make([]*Layer, len(layers))
	for i, l := range layers {
		nodes[i] = &Layer{
			Name:      l.name,
			Index:     i,
			Group:     l.typ == 1,
			Visible:   l.flags&1 != 0,
			Reference: l.flags&64 != 0,
			Opacity:   l.opacity,
			BlendMode: l.blendMode,
			Data:      l.data,
		}
	}

	var roots []*Layer
	var stack []*Layer // ancestor groups by child level

	for i := len(nodes) - 1; i >= 0; i-- {
		node := nodes[i]
		level := int(layers[i].childLevel)

		if level > len(stack) {
			return nil, fmt.Errorf("aseprite: layer %d (%q): %w", i, node.Name, ErrNoParentGroup)
		}

		stack = stack[:level]

		if level == 0 {
			roots = append(roots, node)
		} else {
			parent := stack[level-1]
			node.Parent = parent
			parent.Children = append(parent.Children, node)
		}

		if node.Group {
			stack = append(stack, node)
		}
	}

	// The scan visits nodes top to bottom; reverse to restore
	// the file's bottom-to-top stacking order.
	for i, j := 0, len(roots)-1; i < j; i, j = i+1, j-1 {
		roots[i], roots[j] = roots[j], roots[i]
	}
	for _, node := range nodes {
		for i, j := 0, len(node.Children)-1; i < j; i, j = i+1, j-1 {
			node.Children[i], node.Children[j] = node.Children[j], node.Children[i]
		}
	}

	return roots, nil
}

// ReadFrom reads and parses an Aseprite file from a Reader.
func (spr *Aseprite) ReadFrom(r io.Reader) (n int64, err error) {
	var f file

	if n, err = f.ReadFrom(r); err != nil {
		return
	}

	f.initPalette()

	if err = f.initLayers(); err != nil {
		return
	}

	if err = f.initCels(); err != nil {
		return
	}

	if spr.Layers, err = buildLayerTree(f.layers); err != nil {
		return
	}

	spr.file = &f

	var framesr []image.Rectangle
	spr.Atlas, framesr = f.buildAtlas()
	userdata := f.buildUserData()
	spr.Frames, userdata = f.buildFrames(framesr, userdata)
	spr.LayerData = f.buildLayerData(userdata)
	spr.Tags = f.buildTags()
	spr.Slices = f.buildSlices()
	return
}

// Read reads and parses an Aseprite file from the Reader.
func Read(r io.Reader) (Aseprite, error) {
	var spr Aseprite
	_, err := spr.ReadFrom(r)
	return spr, err
}

// FrameImage composites the given frame into a single image by walking
// the layer tree. Group subtrees are composited first and then drawn with
// the group's own opacity and blend mode, and children are drawn in the
// file's bottom-to-top stacking order.
//
// If include is nil, all visible, non-reference layers are drawn.
// Otherwise include fully decides which nodes are drawn: returning false
// for a node skips it and its entire subtree. Note that cels of layers
// that are invisible or reference layers in the file are discarded while
// parsing, so including them draws nothing.
//
// Unlike the flattened Atlas, hidden groups hide their whole subtree.
func (spr *Aseprite) FrameImage(frame int, include func(*Layer) bool) (image.Image, error) {
	f := spr.file
	if f == nil {
		return nil, errors.New("aseprite: sprite was not parsed from a file")
	}

	if frame < 0 || frame >= len(f.frames) {
		return nil, fmt.Errorf("aseprite: frame %d out of range [0, %d)", frame, len(f.frames))
	}

	bounds := image.Rect(0, 0, f.framew, f.frameh)
	scratch := image.NewRGBA(bounds)
	dst := image.NewRGBA(bounds)

	for _, node := range spr.Layers {
		if err := f.composeNode(dst, scratch, bounds, &f.frames[frame], node, include, 0); err != nil {
			return nil, err
		}
	}

	if f.bpp == 16 {
		gray := image.NewGray16(bounds)
		draw.Draw(gray, bounds, dst, image.Point{}, draw.Src)
		return gray, nil
	}

	return dst, nil
}
