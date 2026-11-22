# Binary quality assurance for the Aseprite decoder

This document records how the corrupt-input tests are built, what they assert,
how the fault injection proves they are meaningful, and the measured resource
and regression bounds.

## 1. Independently constructed fixtures

`internal/asetest` is a writer for the Aseprite binary format. It never reads a
real `.aseprite` file: every header field, frame, chunk and pixel buffer is
emitted field by field from the format specification. A test therefore states
exactly which byte range it is corrupting instead of patching an opaque
fixture.

Generation rules:

- The file header is fixed at 128 bytes; the 32-bit size at offset 0 is
  computed from the body unless a case overrides it (to model a lying size).
- Each frame is a 16-byte header (`0xF1FA` magic, old/new chunk counts,
  duration) followed by chunks. Each chunk is a 6-byte header (32-bit length,
  16-bit type) followed by its body. Chunk framing is deliberately independent
  of the frame length field, so a test can make a chunk overrun its frame.
- Fixed chunk bodies are built by typed helpers: `LayerBody`, `CelBody`,
  `LinkedCelBody`, `PaletteBody`, `TagsBody`, `SliceBody`, `UserDataBody`.
  Each helper mirrors the documented layout, including the reserved bytes.
- Pixel data is generated, not sampled: `SolidPix(n, depth)` yields `n` pixels
  in 8-, 16- or 32-bit depth. Compressed cels are zlib-compressed by the
  builder; raw cels are written verbatim, which lets a test declare 4 pixels
  and supply 3.
- No fixture depends on Aseprite being installed; everything is bytes in
  memory. `go test ./...` is sufficient.
- Every corruption case is checked against `decodeAll`, so a case cannot
  silently start from an already-broken base; the valid control rows assert
  that the unmutated form decodes.

`fixture_selftest_test.go` decodes minimal, fully independent samples at all
three colour depths and checks that `ReadFrom`, `Decode` and `DecodeConfig`
agree on the atlas dimensions and colour model.

## 2. The Type/length matrix

`corrupt_matrix_test.go` enumerates every bad input as a row with the expected
category and context. Coverage by layer:

| Layer        | Cases |
|--------------|-------|
| Header       | empty, 127-byte prefix, bad magic, bad depth, bad pixel ratio, zero frames, zero width, size below header, size over limit, size under/over-stating the body |
| Frame        | truncated header, truncated body, bad magic, length below header, length past EOF, impossible chunk count, trailing bytes, second frame truncated |
| Chunk        | length below the 6-byte minimum, length past the physical end, length overrunning its frame, count overrunning the frame, truncated body |
| Palette      | entries outside the palette, truncated header, truncated entry, truncated name |
| Linked cel   | valid link, source frame out of range, truncated body |
| Zlib cel     | corrupt stream, truncated stream, short output, long output, non-zlib data |
| Raw cel      | truncated pixels, valid pixels, unknown cel type, layer out of range, truncated header, pixel size over the limit |
| Layer        | truncated body, tilemap layer (unsupported) |
| User data    | truncated flags, truncated text, truncated colour, valid text |
| Tag          | valid, truncated header, truncated name, count beyond the data |
| Slice        | valid, truncated header, truncated bounds, truncated centre, count beyond the data |

Each bad row asserts three things:

1. **Stable category** — `errors.Is(err, ErrTruncated | ErrMalformed |
   ErrUnsupported)`. The category does not depend on message text.
2. **Context** — `errors.As(err, *ParseError)` and, where applicable, the exact
   frame, layer and chunk index. A failure inside chunk 2 of frame 1 of layer 0
   reports all three.
3. **No panic** — the decode runs under a `recover` guard; a panic fails the
   row with the recovered value.

Rows marked `DecodeConfigToo` additionally require `DecodeConfig` to fail with
the same category, so the configuration path cannot become more permissive than
the decoder.

Valid control rows (`linked/valid_source`, `cel/raw_valid`,
`userdata/valid_layer_text`, `tag/valid`, `slice/valid`) assert success, which
keeps the matrix honest: it distinguishes good from bad input rather than
rejecting everything.

Current count: **57 cases** — 25 truncated, 21 malformed, 6 unsupported, plus 5
valid controls.

### Property check

`fuzz_test.go` (`FuzzDecodeNoPanic`) seeds the matrix rows plus minimal valid
samples and asserts, for any input, that the decoder never panics and never
reports success with a nil atlas or an inconsistent byte count. When both
`DecodeConfig` and the full decode succeed, it also requires the reported
dimensions and colour model to match. Under `go test` the seeds run as ordinary
cases; `go test -fuzz=FuzzDecodeNoPanic` explores further. A 26-second run of
~2.0 million executions found no panic.

## 3. Valid-sample consistency

`TestValidConfigMatchesDecode` builds a valid sprite at 8, 16 and 32 bits with
two frames, layer and cel user data, a tag, a nine-slice, a linked cel and both
raw and compressed cels. For each depth it asserts:

- `DecodeConfig`'s width and height equal the bounds of the `image.Image`
  returned by `Decode`;
- the colour models are identical;
- `Decode` and a direct `ReadFrom` agree on every pixel.

`TestRejectDoesNotRetainData` asserts a failed decode leaves the receiver's
atlas empty, and `TestDecodeDoesNotModifyInput` asserts neither a successful
nor a failed decode mutates the caller's byte slice.

## 4. Fault injection

`fault_injection_test.go` proves the tests guard real boundaries by
reintroducing defects and requiring the matrix to fail. Each mutation is a
single source substitution that removes one guard. The harness copies the
module to a temporary directory, applies the mutation, and reruns the named
case:

| Mutation | Guard removed | Case that must fail |
|----------|---------------|---------------------|
| `chunk_length_ignores_frame_bound` | chunk length vs. bytes left in frame | `chunk/length_overruns_frame` |
| `link_source_index_unchecked` | linked-cel source frame bound | `linked/frame_out_of_range` |
| `zlib_output_length_unchecked` | decoded pixel count vs. declared size | `zlib/short_output` |
| `palette_range_unchecked` | palette index range | `palette/entries_past_palette` |

`TestFaultInjectionBaseline` reruns the unmutated copy first, so a failure is
attributable to the mutation rather than to the copy step. The test refuses to
pass with fewer than two successful mutations. It needs the Go toolchain and a
full checkout; it skips (rather than silently passing) when those are absent,
and honours `-short`.

Observed result: all four mutations are detected; the baseline passes.

## 5. Resource bounds

Hostile inputs are rejected before any large allocation:

- declared file size is capped at 512 MiB (`maxFileSize`);
- one cel is capped at 64 MiB of pixels (`maxCelBytes`);
- the atlas is capped at 2^26 pixels (`maxAtlasPixels`);
- a zlib cel is inflated at most to its declared pixel size, so a compression
  bomb is reported instead of expanded.

`TestResourceBounds` measures heap growth and wall-clock time per case and
fails if allocation exceeds 32 MiB or a decode takes longer than 2 seconds.
Observed on this machine:

| Case | Time | Allocated |
|------|------|-----------|
| `atlas_over_limit` | ~21 µs | ~3.3 KB |
| `cel_over_limit` | ~8 µs | ~3.2 KB |
| `declared_size_over_limit` | ~5 µs | ~3.0 KB |
| `declared_size_lies_above_content` | ~7 µs | ~4.7 KB (would be 256 MiB if allocated up front) |
| `compression_bomb` | < 1 ms | bounded by the 4-byte cel |

The lying-size row is why the body is read with `io.CopyN` into a growing
buffer instead of allocating the declared size first: the buffer tracks the
bytes that actually arrive.

The whole package suite runs in about 1.4 s wall clock with ~94 MB peak RSS,
of which roughly 1 s is the fault-injection rebuilds. `go test -short ./...`
skips those and completes in about 0.4 s.

## 6. Regression record

The three original tests (`TestAnimation`, `TestGrayscale`, `TestBlend`)
continue to pass. The PNGs they emit were byte-for-byte identical before and
after the decoder changes (SHA-256 unchanged for `slime_paletted.png`,
`slime_grayscale.png` and `blendtest.png`), so valid-file output did not drift.

Command used for the record:

```
go test ./...
```

Result:

```
ok  	github.com/askeladdk/aseprite
ok  	github.com/askeladdk/aseprite/internal/blend
```

`go vet ./...` is clean.

## 7. Running the suite

```
go test ./...                 # everything, including fault injection
go test -short ./...          # skips the rebuild-based fault injection
go test -run TestCorruptMatrix -v .   # the matrix alone, one row per case
```
