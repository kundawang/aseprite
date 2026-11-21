// Package aseprite parses Aseprite sprite files.
//
// Layers are flattened, blending modes are applied,
// and frames are arranged on a single texture atlas.
// Invisible and reference layers are ignored.
// Tilesets and external files are not supported.
//
// For previews that only need one frame at a time, ReadFrames parses the
// sprite without building the atlas. Frame images are then rendered on
// demand by FrameImage and cached until ReleaseFrame is called, so only the
// frames that are actually used occupy memory.
package aseprite

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"sync"
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

// ErrFrameIndex is returned by FrameImage when the frame index is out of
// range. Index 0 is the first frame and the frame count is len(Frames).
var ErrFrameIndex = errors.New("aseprite: frame index out of range")

// ErrFrameAccess is returned by FrameImage when the sprite has neither a
// frame source nor an atlas, which happens only for a manually constructed
// Aseprite value. Sprites returned by Read, ReadFrom, ReadFrames and
// FrameImage-capable decoders all support FrameImage.
var ErrFrameAccess = errors.New("aseprite: per-frame access unavailable")

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

// Frame represents a single frame in the sprite.
type Frame struct {
	// Bounds is the image bounds of the frame. In atlas mode it specifies
	// where the frame image is located in the atlas. In frame mode (see
	// ReadFrames) it is the bounds of the frame's own image,
	// image.Rect(0, 0, width, height).
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
	//
	// Atlas is nil when the sprite was read with ReadFrames. In that mode
	// frame images are accessed one at a time through FrameImage.
	Atlas image.Image

	// Frames lists all frames that make up the sprite.
	Frames []Frame

	// Tags lists all animation tags.
	Tags []Tag

	// Slices lists all slices.
	Slices []Slice

	// LayerData lists the user data of all visible layers.
	LayerData [][]byte

	// src provides per-frame access in frame mode. It is nil in atlas mode,
	// where FrameImage copies out of Atlas instead. Value copies of
	// Aseprite share the same src and therefore the same frame cache.
	src *frameSource
}

// ReadFrom reads and parses an Aseprite file from a Reader and builds the
// texture atlas.
func (spr *Aseprite) ReadFrom(r io.Reader) (n int64, err error) {
	return spr.readFrom(r, false)
}

func (spr *Aseprite) readFrom(r io.Reader, framesOnly bool) (n int64, err error) {
	var f file

	f.deferCels = framesOnly

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

	userdata := f.buildUserData()

	if framesOnly {
		// Without the atlas every frame lives in its own image whose
		// origin is (0, 0), so its bounds are the canvas rectangle.
		framesr := make([]image.Rectangle, len(f.frames))
		for i := range framesr {
			framesr[i] = image.Rect(0, 0, f.framew, f.frameh)
		}

		spr.Atlas = nil
		spr.Frames, userdata = f.buildFrames(framesr, userdata)
		spr.src = newFrameSource(&f, len(f.frames))
	} else {
		var atlas image.Image
		var framesr []image.Rectangle

		if atlas, framesr, err = f.buildAtlas(); err != nil {
			return
		}

		spr.Atlas = atlas
		spr.Frames, userdata = f.buildFrames(framesr, userdata)
		// Do not retain the decoded file in atlas mode. The atlas is
		// self-contained and FrameImage copies out of it, so the source
		// pixels and input buffer can be reclaimed as before.
		spr.src = nil
	}

	spr.LayerData = f.buildLayerData(userdata)
	spr.Tags = f.buildTags()
	spr.Slices = f.buildSlices()
	return
}

// Read reads and parses an Aseprite file from the Reader and builds the
// texture atlas.
func Read(r io.Reader) (Aseprite, error) {
	var spr Aseprite
	_, err := spr.ReadFrom(r)
	return spr, err
}

// ReadFrames reads and parses an Aseprite file from the Reader without
// building the texture atlas. The returned sprite exposes the same metadata
// as Read, but frame pixels are rendered on demand by FrameImage. This keeps
// memory proportional to the frames that are actually accessed instead of to
// the whole animation.
func ReadFrames(r io.Reader) (*Aseprite, error) {
	var spr Aseprite
	if _, err := spr.readFrom(r, true); err != nil {
		return nil, err
	}
	return &spr, nil
}

// FrameImage returns the fully composited image of frame i, including layer
// blending, layer and cel opacity, and transparent pixels. The returned image
// is always anchored at the origin with bounds image.Rect(0, 0, width,
// height). It does not share pixel storage with the atlas, with the decoder's
// source data, or with any other frame.
//
// Sprites read with ReadFrames render frames on demand and cache them: repeat
// calls return the cached image until ReleaseFrame or ReleaseFrames is called.
// Sprites read with Read or ReadFrom copy the frame out of the atlas on every
// call and keep no cache. Callers must not modify the returned image.
//
// FrameImage returns an error satisfying errors.Is(err, ErrFrameIndex) if i
// is out of range, or an error satisfying errors.Is(err, ErrFrameAccess) if
// the sprite has no frame access. A frame that fails to render, such as one
// with a corrupt compressed cel, returns an error and is not cached.
func (spr *Aseprite) FrameImage(i int) (image.Image, error) {
	if i < 0 || i >= len(spr.Frames) {
		return nil, fmt.Errorf("%w: %d (frame count %d)", ErrFrameIndex, i, len(spr.Frames))
	}

	if spr.src == nil {
		// Atlas mode keeps no per-frame cache: the frame is copied out of
		// the atlas on each call, using the bounds recorded with it.
		if spr.Atlas == nil {
			return nil, ErrFrameAccess
		}
		return copyFrameFromAtlas(spr.Atlas, spr.Frames[i].Bounds), nil
	}

	return spr.src.frameImage(i)
}

// FrameCached reports whether the image of frame i is currently cached by
// FrameImage. It always reports false in atlas mode, which copies frames out
// of the atlas per call. It reports false for out-of-range indices.
func (spr *Aseprite) FrameCached(i int) bool {
	if spr.src == nil {
		return false
	}
	return spr.src.cached(i)
}

// ReleaseFrame drops the cached image of frame i so that its memory can be
// reclaimed. It is a no-op for out-of-range indices and in atlas mode, which
// keeps no per-frame cache. It does not affect the atlas or the decoder's
// source data.
func (spr *Aseprite) ReleaseFrame(i int) {
	if spr.src != nil {
		spr.src.release(i)
	}
}

// ReleaseFrames drops all cached frame images. It is a no-op in atlas mode.
func (spr *Aseprite) ReleaseFrames() {
	if spr.src != nil {
		spr.src.releaseAll()
	}
}

// frameSource renders and caches frame images in frame mode. It is nil in
// atlas mode.
type frameSource struct {
	f *file

	mu    sync.Mutex
	cache []image.Image
}

func newFrameSource(f *file, nframes int) *frameSource {
	return &frameSource{
		f:     f,
		cache: make([]image.Image, nframes),
	}
}

func (src *frameSource) frameImage(i int) (image.Image, error) {
	src.mu.Lock()
	defer src.mu.Unlock()

	if i < 0 || i >= len(src.cache) {
		return nil, fmt.Errorf("%w: %d (frame count %d)", ErrFrameIndex, i, len(src.cache))
	}

	if img := src.cache[i]; img != nil {
		return img, nil
	}

	// Only frame mode reaches here, so the source file is always present.
	img, err := src.f.buildFrameImage(i)
	if err != nil {
		return nil, err
	}

	src.cache[i] = img
	return img, nil
}

func (src *frameSource) cached(i int) bool {
	src.mu.Lock()
	defer src.mu.Unlock()
	return i >= 0 && i < len(src.cache) && src.cache[i] != nil
}

func (src *frameSource) release(i int) {
	src.mu.Lock()
	defer src.mu.Unlock()
	if i >= 0 && i < len(src.cache) {
		src.cache[i] = nil
	}
}

func (src *frameSource) releaseAll() {
	src.mu.Lock()
	defer src.mu.Unlock()
	for i := range src.cache {
		src.cache[i] = nil
	}
}

// copyFrameFromAtlas returns an independent copy of the frame located at r in
// the atlas. The copy is anchored at the origin.
func copyFrameFromAtlas(atlas image.Image, r image.Rectangle) image.Image {
	b := image.Rect(0, 0, r.Dx(), r.Dy())

	var dst draw.Image
	switch atlas.(type) {
	case *image.Gray16:
		dst = image.NewGray16(b)
	default:
		dst = image.NewRGBA(b)
	}

	draw.Draw(dst, b, atlas, r.Min, draw.Src)
	return dst
}
