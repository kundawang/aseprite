# aseprite

Package aseprite parses Aseprite sprite files. Layers are flattened,
blending modes are applied, and frames are arranged on a single texture
atlas.

## Eager decoding

```go
f, _ := os.Open("sprite.aseprite")
defer f.Close()
spr, _ := aseprite.Read(f) // decodes every frame into spr.Atlas
```

## Frame index and random access

For large animations, decoding every frame up front is wasteful when only
a few frames are needed (e.g. showing frame 120 at game startup).
`OpenIndex` / `ScanIndex` scan the file header and frame boundaries only —
recording each frame's offset, duration and chunk summary — and decompress
a frame's cels only when that frame is requested:

```go
f, _ := os.Open("sprite.aseprite")
ix, _ := aseprite.OpenIndex(f) // reads headers only; f must stay open
img, _ := ix.Frame(120)        // seeks to and decodes frame 120 only
dur := ix.Frames[120].Duration
```

Two explicit modes:

- **`OpenIndex(rs io.ReadSeeker)`** — random-access mode. Only the file
  header, frame headers and chunk headers are read during the scan (plus
  layer/palette metadata from the first frame). The caller keeps ownership
  of `rs`: it must remain open and must not be repositioned by other code
  until the last `Frame` call. Calling `Frame` after the source has been
  closed returns the source's error wrapped in a `*FrameError`.
- **`ScanIndex(r io.Reader)`** — one-shot stream mode. The stream is
  consumed completely and the raw, still-compressed frame records are
  buffered. After `ScanIndex` returns, the source is no longer needed and
  may be closed; `Frame` decodes from the buffers.

Decoded cels are cached, so repeated `Frame` calls — and frames whose
linked cels reference already-decoded frames — reuse earlier work.

Corrupt files (truncated frames, bad chunk lengths, linked cels with
self/forward references, oversized frames) are reported as `*FrameError`
carrying the 0-based frame number, from both the index and the eager
decoder.
