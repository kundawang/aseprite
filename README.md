# Aseprite image loader

[![GoDoc](https://godoc.org/github.com/askeladdk/aseprite?status.png)](https://godoc.org/github.com/askeladdk/aseprite)
[![Go Report Card](https://goreportcard.com/badge/github.com/askeladdk/aseprite)](https://goreportcard.com/report/github.com/askeladdk/aseprite)

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

Read the [documentation](https://pkg.go.dev/github.com/askeladdk/aseprite) for more information about what meta data is extracted.

## Linear-light compositing

By default, layers are blended and composited in gamma-encoded sRGB space, reproducing the historical output bit-for-bit. `DecodeAsepriteWithOptions` can instead composite in linearized sRGB, which keeps semi-transparent bright layers (glows, lights) over dark backgrounds from coming out darker than intended:

```go
spr, err := aseprite.DecodeAsepriteWithOptions(r, aseprite.Options{
    LinearLight: true, // blend in linearized sRGB instead of gamma-encoded sRGB
    // PremultipliedAlpha: true, // treat colors as alpha-premultiplied
})
```

Blending, layer opacity and source-over compositing happen in a single pass, so layer opacity is applied exactly once. Out-of-range blend results (e.g. from Addition or Divide) are clamped deterministically.

## License

Package aseprite is released under the terms of the ISC license.

The internal blend package is released by Guillermo Estrada under the terms of the MIT license: http://github.com/phrozen/blend.
