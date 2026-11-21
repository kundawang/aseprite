# Aseprite image loader

[![GoDoc](https://godoc.org/github.com/askeladdk/aseprite?status.png)](https://godoc.org/github.com/askeladdk/aseprite)
[![Go Report Card](https://goreportcard.com/badge/github.com/askeladdk/aseprite)](https://goreportcard.com/report/github.com/askeladdk/aseprite)
[![Coverage Status](https://coveralls.io/repos/github/askeladdk/aseprite/badge.svg?branch=master)](https://coveralls.io/github/askeladdk/aseprite?branch=master)

## Overview

Package aseprite implements a decoder for [Aseprite sprite files](https://github.com/aseprite/aseprite/blob/main/docs/ase-file-specs.md) (`.ase` and `.aseprite` files).

Layers are flattened, blending modes are applied, and frames are arranged on a single texture atlas. Invisible and reference layers are ignored.

Limitations:
- Tilemaps are not supported.
- External files are not supported.
- Old aseprite format is not supported.
- Color profiles are ignored.

## Install

```
go get -u github.com/askeladdk/aseprite
```

## Quickstart

Use `image.Decode` to decode an aseprite sprite file to an `image.Image`:

```go
import (
    _ "github.com/askeladdk/aseprite"
)

img, imgformat, err := image.Decode("test.aseprite")
```

This is enough to decode single frame images. Multiple frames are arranged as a texture atlas in a single image. Type cast the image to `aseprite.Aseprite` to access the frame data, as well as other meta data extracted from the sprite file:

```go
if imgformat == "aseprite" {
    sprite := img.(*aseprite.Aseprite)
    for _, frame := range sprite.Frames {
        // etc ...
    }
}
```

Alternatively, use the `Read` function to directly decode an image to `aseprite.Aseprite`:

```go
sprite, err := aseprite.Read(f)
```

Read the [documentation](https://pkg.go.dev/github.com/askeladdk/aseprite) for more information about what meta data is extracted.

## Atlas metadata

Each `Frame` carries everything a renderer needs to sample it from the
texture atlas:

- `Frame.Page` indexes into `Aseprite.Pages`, allowing the atlas to be
  split over multiple texture pages.
- `Frame.Bounds` is the page-local pixel rectangle of the frame content.
- `Frame.Padding` is the number of extruded padding pixels around the
  content. `Frame.UV` always describes the content, never the ring.
- `Frame.UV` is the half-open normalized coordinate rectangle of the
  content within its page.
- `Frame.Rotated`, `Frame.FlipH` and `Frame.FlipV` are reserved
  transform markers. The decoder never rotates or flips frames, so they
  are always false.

`Read` keeps the historical compact single-page layout. Use `ReadWith`
to control the layout:

```go
sprite, err := aseprite.ReadWith(f, aseprite.Options{
    Padding:       2, // extrude a 2px border ring against page bleeding
    MaxPageWidth:  1024,
    MaxPageHeight: 1024,
})
```

### UV semantics

`UVRect` is half-open: `Min` is the upper-left corner and `Max` is the
exclusive lower-right edge, so every normalized coordinate stays inside
`[0, 1]` and adjacent frames never share an edge. Coordinates are
normalized per axis by the page dimensions, so non-square pages are
handled correctly, and normalization is relative to `Page.Bounds.Min`
to support pages with a negative texture origin. Empty frames map to an
empty `UVRect` (`Min == Max`).

There are two equivalent ways to keep linear filtering from bleeding
into neighboring texels:

1. Extruded padding (`Options.Padding`): border texels are replicated
   around every frame, so sampling just outside the content still
   returns the frame's own pixels. Use the frame's `UV` unchanged.
2. Half-texel inset on a padding-free atlas: shrink the UV rectangle by
   half a texel using `UVRect.Inset`.

```go
page := sprite.Pages[frame.Page]
d := aseprite.UV{
    U: 0.5 / float32(page.Bounds.Dx()),
    V: 0.5 / float32(page.Bounds.Dy()),
}
uv := frame.UV.Inset(d)
```

## License

Package aseprite is released under the terms of the ISC license.

The internal blend package is released by Guillermo Estrada under the terms of the MIT license: http://github.com/phrozen/blend.
