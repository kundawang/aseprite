# Aseprite image loader

[![GoDoc](https://godoc.org/github.com/askeladdk/aseprite?status.png)](https://godoc.org/github.com/askeladdk/aseprite)
[![Go Report Card](https://goreportcard.com/badge/github.com/askeladdk/aseprite)](https://goreportcard.com/report/github.com/askeladdk/aseprite)

## Overview

Package aseprite implements a decoder for Aseprite sprite files (`.ase` and `.aseprite` files).

Layers are flattened, blending modes are applied, and frames are arranged on a single texture atlas. Invisible and reference layers are ignored. Tilesets and external files are not supported.

## Install

```
go get -u github.com/askeladdk/aseprite
```

## Quickstart

Use `image.Decode` to decode an aseprite sprite file to an `image.Image`:

```go
img, imgformat, err := image.Decode("test.aseprite")
```

This works fine when loading single images, but if the sprite contains multiple frames they will be organized as a texture atlas. In that case, type cast the image to a `aseprite.Aseprite` to access the metadata:

```go
if imgformat == "aseprite" {
    sprite := img.(*aseprite.Aseprite)
    for _, frame := range sprite.Frames {
        // etc ...
    }
}
```

## Blend modes

Each layer stores a blend mode value as defined by the Aseprite file format. The 19 built-in modes (`Normal` through `Divide`, values 0–18) are registered in `aseprite.DefaultRegistry()`, which maps stable names, file values and blending functions. The registry is immutable: accessors return copies and registries contain no mutable state, so any number of decoders may share one concurrently. Extend it with `Registry.With` to add custom modes:

```go
registry, err := aseprite.DefaultRegistry().With(aseprite.ModeInfo{
    Mode: aseprite.Mode(100),
    Name: "Studio Overlay",
    Func: func(dst, src color.Color) color.Color {
        // Blend in premultiplied color space; return an opaque color.
        // Must be pure and safe for concurrent use.
    },
})
```

A custom `BlendFunc` is only called on opaque source pixels (transparent pixels are copied through), must return an opaque color, and must not apply layer or cel opacity itself — the decoder applies the opacity mask after blending.

When a file references a mode that the active registry does not implement, `DecodeWithOptions` either fails strictly with an `*UnsupportedModeError` that names the layer and frame, or records a `Diagnostic` (`KindUnknown`, `KindReserved` for values 19–255, or `KindOutOfRange` for values 256–65535) and composites the cel with an explicit fallback mode instead:

```go
sprite, err := aseprite.DecodeWithOptions(f, aseprite.DecodeOptions{
    Registry: registry,
    Strict:   true,            // or false with Fallback: aseprite.Normal
})
```

Plain `Decode` keeps its historical behavior: unsupported modes fall back to `Normal` and diagnostics are available on `Aseprite.Diagnostics`.

## License

Package aseprite is released under the terms of the ISC license.
