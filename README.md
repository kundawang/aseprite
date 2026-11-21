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

## Nine-slice scaling

Slices with a center rectangle can be drawn scaled to any target rectangle using nine-slice (9-patch) scaling, so rounded corners and borders keep their shape. `DrawSlice` reads the slice image from the texture atlas at the frame the slice key applies to and returns the pivot mapped into destination coordinates:

```go
sprite, err := aseprite.Read(f)
dst := image.NewRGBA(image.Rect(0, 0, 200, 100))
pivot := sprite.DrawSlice(dst, sprite.Slices[0], dst.Bounds(), aseprite.SliceOptions{
    Filter:   aseprite.Nearest,  // or aseprite.Linear
    Overflow: aseprite.Clip,     // or aseprite.Compress
})
```

Slices without a center are stretched as a whole. When the target is smaller than the sum of the edge sizes, `Overflow` selects between clipping and proportionally compressing the edges.

Read the [documentation](https://pkg.go.dev/github.com/askeladdk/aseprite) for more information about what meta data is extracted.

## License

Package aseprite is released under the terms of the ISC license.

The internal blend package is released by Guillermo Estrada under the terms of the MIT license: http://github.com/phrozen/blend.
