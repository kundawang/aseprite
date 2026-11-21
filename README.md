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

## Deterministic decoding

Decoding is a pure function of the input bytes. `Read`, `Decode` and
`DecodeConfig`:

- return byte-identical results across repeated and concurrent calls, for
  pixels, frame bounds and durations, tags, slices and user data;
- never write to the caller's input buffer, and never alias it from a
  returned image or `[]byte`;
- return an error instead of panicking on truncated or corrupt input, and
  wrap structural errors in `aseprite.ErrInvalidFormat` with the frame, chunk
  and layer indices that failed.

This is enforced by `determinism_test.go`, `faultinject_test.go` and
`public_api_test.go`. The tests decode a set of pinned binary fixtures in
`testfiles/determinism` covering 8/16/32 bpp, linked cels, compressed cels,
the old palette chunks (0x0004 and 0x0011), a non-zero transparent index, and
empty metadata. To regenerate the fixtures:

```
go run cmd/genfixtures/main.go
```

The fixture files are pinned by SHA-256 in `determinism_test.go`, so an
intentional regeneration must update the pins in the same change.

Run the determinism suite with the race detector:

```
go test -race ./...
```

## License

Package aseprite is released under the terms of the ISC license.

The internal blend package is released by Guillermo Estrada under the terms of the MIT license: http://github.com/phrozen/blend.
