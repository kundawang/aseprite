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

## Unknown chunk audit

Newer Aseprite versions and third-party tools write extension chunks that this
decoder does not interpret. They never change the decoded image, but they used
to be dropped silently. Use the audit API to inspect them:

```go
sprite, err := aseprite.ReadWithOptions(f, aseprite.Options{
    SkipUnknownChunkData: false, // keep a copy of each payload
})
if err != nil {
    // Audit failures are *aseprite.ChunkError values carrying frame, chunk,
    // type, layer and file offset context.
    var ce *aseprite.ChunkError
    if errors.As(err, &ce) {
        log.Printf("frame %d chunk %d type %#04x at %d", ce.Frame, ce.Chunk, ce.Type, ce.Offset)
    }
}

for _, c := range sprite.UnknownChunks() {
    log.Printf("frame %d layer %d type %#04x offset %d data %x",
        c.Frame, c.Layer, c.Type, c.Offset, c.Data)
}
```

Queries return snapshots in file order, so mutating them cannot affect the
decoder, and separate `Read` calls never share audit state. `UnknownChunks`,
`UnknownChunkAt`, `UnknownChunksByType`, `UnknownChunksByFrame` and
`UnknownChunksByLayer` cover the common lookups; `CheckUnknownChunks` runs the
audit without building the texture atlas.

Limits guard against untrusted input:

- `MaxUnknownChunkSize` rejects an oversized extension payload
  (`ErrUnknownChunkTooLarge`).
- `MaxUnknownChunks` bounds how many are recorded (`ErrTooManyUnknownChunks`).
- `RejectDuplicateUnknownChunkTypes` rejects a repeated undocumented type
  within a frame (`ErrDuplicateUnknownChunk`); by default repeats are recorded
  with `Duplicate` set.
- `RequireUnknownChunkContext` rejects chunks that precede any layer or cel
  (`ErrUnknownChunkContext`), and chunks outside the declared frame count are
  rejected with `ErrUnknownChunkFrame`.
- `MaxFileSize` and `MaxAtlasSize` bound the buffers a malformed header can
  request. Truncated input yields `ErrTruncated` with the failing location
  rather than a panic.

Unknown chunk types the Aseprite specification itself documents (for example
the color profile or external files chunks) are marked `SpecKnown`, so a
"newer extension" is distinguishable from "known but unsupported".

## License

Package aseprite is released under the terms of the ISC license.

The internal blend package is released by Guillermo Estrada under the terms of the MIT license: http://github.com/phrozen/blend.
