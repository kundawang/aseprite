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

## Indexed color export

Retro hardware often only accepts 8 bits per pixel texture atlases. Use
`Quantize` to reduce a true color frame or atlas to a paletted image:

```go
img, report, err := aseprite.Quantize(sprite, aseprite.QuantizeOptions{
    PaletteSize:      16,    // number of palette entries (1-256, default 256)
    TransparentIndex: 0,     // reserved index for fully transparent pixels, or -1
    Metric:           aseprite.Euclidean, // Euclidean, Manhattan or RedMean
    Dither:           aseprite.FloydSteinberg, // or DitherNone
})
```

The returned `*image.Paletted` is a standard library image and the
`QuantizeReport` records how every pixel was mapped: exact matches,
approximated colors, fully transparent and semi-transparent pixel counts,
per-index usage and normalized error statistics.

Deterministic mapping rules:

- Fully transparent pixels (alpha 0) always land on the reserved
  `TransparentIndex`; the reserved entry is never assigned quantized colors.
- Semi-transparent pixels keep their full alpha channel and take part in
  palette construction together with opaque pixels.
- Colors that do not fit in the palette are mapped to their nearest palette
  entry using the selected error metric, with ties resolved by the lowest
  palette index. Identical inputs always produce identical outputs.

`QuantizeFrames` quantizes every frame of a sprite, optionally sharing one
palette across all frames so frame colors stay consistent in the atlas.

The input image is never modified. Empty images, 16 bpp grayscale images,
invalid palette parameters and images exceeding the documented resource
limits (`MaxImageDimension`, `MaxPixels`) return explicit errors.

## License

Package aseprite is released under the terms of the ISC license.

The internal blend package is released by Guillermo Estrada under the terms of the MIT license: http://github.com/phrozen/blend.
