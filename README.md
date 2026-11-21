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

## Retiming tag timelines

Reusing one animation at different BPMs means changing frame durations without disturbing tag boundaries. `Retime` recomputes the `Frame.Duration` values inside a tag while leaving its `Lo`/`Hi` range, `Repeat` and loop direction untouched. All durations are rounded to whole milliseconds (`aseprite.TickDuration`), matching Aseprite's own storage.

Three transforms are available, one per `RetimeOptions`:

- `Ratio` scales every frame duration by a positive factor, rounding half away from zero. Zero-duration frames stay zero.
- `TargetDuration` fits the whole tag to an exact total. Each frame keeps its relative weight; the ticks lost in integer division are distributed with the largest-remainder method (ties go to the earlier frame), so the result always sums to the target.
- `TempoMap` plays the tag through a sequence of constant-tempo `TempoSegment` values. A frame crossing a ratio change is split and rounded per piece; the final segment extends to the end of the tag.

`Retime` returns a copy and never mutates the sprite; `RetimeInPlace` applies the result. All options are validated and computed before any frame is written, so an invalid input leaves the sprite unchanged. When two retimed tags share a frame they must agree on its new duration, otherwise `ErrOverlappingTags` is returned and nothing is changed.

```go
// Copy: frame durations for tag 0 at half speed.
durations, err := sprite.Retime(aseprite.RetimeOptions{TagIndex: 0, Ratio: 2})

// In place: squeeze tag 1's whole timeline into 500ms.
err = sprite.RetimeInPlace(aseprite.RetimeOptions{
    TagIndex: 1,
    TargetDuration: 500 * time.Millisecond,
})
```

The exported `Err*` sentinels (`ErrInvalidRatio`, `ErrInvalidDuration`, `ErrInvalidTempoMap`, `ErrOverlappingTags`, `ErrDurationOverflow`, …) can be matched with `errors.Is`.

## License

Package aseprite is released under the terms of the ISC license.

The internal blend package is released by Guillermo Estrada under the terms of the MIT license: http://github.com/phrozen/blend.
