# aseprite

Aseprite sprite file decoder for Go.

Layers are flattened, blending modes are applied, and frames are arranged on a
single texture atlas. Invisible and reference layers are ignored. Tilesets and
external files are not supported.

## Atlas mode (default)

`Read`, `ReadFrom`, `Decode` and `image.Decode` build the texture atlas. Every
frame is decoded up front and `Frame.Bounds` points at its region of `Atlas`.

```go
sprite, err := aseprite.Read(f)
if err != nil {
    return err
}
for _, frame := range sprite.Frames {
    // frame.Bounds is the frame's rectangle in sprite.Atlas.
}
```

## Per-frame mode

UI previews usually need one frame at a time. Building the atlas decodes and
stores the whole animation, which can be many times the memory of the frame
being shown. `ReadFrames` parses the same metadata without building the atlas,
and `FrameImage` renders a single frame on demand:

```go
sprite, err := aseprite.ReadFrames(f)
if err != nil {
    return err
}

// Returns an image.Rect(0, 0, w, h) image composited exactly like the atlas.
img, err := sprite.FrameImage(currentFrame)
if err != nil {
    return err
}

// Later, free frames that are off screen. Calling FrameImage again
// re-renders and caches the frame.
sprite.ReleaseFrame(previousFrame)
```

In per-frame mode:

- `Atlas` is nil and `Frame.Bounds` is `image.Rect(0, 0, width, height)`.
- `FrameImage` decodes only the compressed cels of the requested frame. Other
  frames are neither decompressed nor cached.
- Rendered frames are cached; `FrameCached`, `ReleaseFrame` and `ReleaseFrames`
  control the cache. A cached image is returned again on repeat access.
- Returned images never alias the decoder's source bytes, the atlas, or each
  other. Modifying one has no effect on later renders.
- An out-of-range index returns an error matching `ErrFrameIndex`. A frame
  whose data cannot be decoded, such as a corrupt compressed cel, returns an
  error and is not cached.
- Linked cels are resolved once at parse time; forward links are allowed and
  link cycles are reported instead of recursing.

`FrameImage` also works on atlas-mode sprites, copying the frame out of the
atlas per call, so it is safe to switch modes without changing call sites.
The returned image is always anchored at the origin. In atlas mode there is no
per-frame cache; `FrameCached` always reports false and the release methods are
no-ops.

The `testfiles/frames` fixtures cover 8, 16 and 32 bits per pixel, empty
frames, linked cels and corrupt input. Regenerate them with:

```
go run ./cmd/genfixtures
```
