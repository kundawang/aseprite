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

## Encoding

Build a `Sprite` and call its `WriteTo` method to write a standard Aseprite
file. The encoder supports 8/16/32 bpp, frame durations, layer/cel/tag/slice
user data, tags, slices (9-patch centers and pivots), the transparent palette
index, linked cels and optional zlib-compressed cels:

```go
doc := &aseprite.Sprite{
    Width:  8,
    Height: 8,
    BPP:    aseprite.Indexed8,
    Palette: color.Palette{color.Transparent, color.NRGBA{255, 0, 0, 255}},
    TransparentIndex: 0,
    Layers: []aseprite.SpriteLayer{
        {Name: "base", Visible: true, Opacity: 255},
    },
    Frames: []aseprite.SpriteFrame{{
        Duration: 100 * time.Millisecond,
        Cels: []aseprite.SpriteCel{{
            Layer:       0,
            LinkedFrame: -1,
            Bounds:      image.Rect(0, 0, 8, 8),
            Opacity:     255,
            Image:       paletted, // *image.Paletted, *image.Gray16 or *image.NRGBA
        }},
    }},
}

// Raw cels by default; pass aseprite.WithCompressedCels() for zlib cels.
if _, err := doc.WriteTo(f); err != nil {
    // Handle validation or write error. The input document is never
    // modified and no partial file is produced.
}
```

## License

Package aseprite is released under the terms of the ISC license.

The internal blend package is released by Guillermo Estrada under the terms of the MIT license: http://github.com/phrozen/blend.
