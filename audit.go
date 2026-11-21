package aseprite

import (
	"errors"
	"fmt"
	"io"
)

// NoLayer is used as the Layer value of an UnknownChunk that cannot be
// attributed to a layer. This happens when an extension chunk appears before
// any layer or cel chunk in its frame.
const NoLayer = -1

// Errors returned while auditing chunks that the decoder does not understand.
// They are wrapped together with the frame, chunk, type and file offset of the
// offending chunk, so callers can use errors.Is and errors.As to identify the
// failure and locate it in the input.
var (
	// ErrTruncated reports that the input ended before a declared header,
	// frame or chunk was complete.
	ErrTruncated = errors.New("aseprite: truncated input")

	// ErrInvalidFormat reports a structurally malformed sprite file.
	ErrInvalidFormat = errors.New("aseprite: invalid format")

	// ErrUnknownChunkTooLarge reports an unknown chunk whose payload exceeds
	// Options.MaxUnknownChunkSize.
	ErrUnknownChunkTooLarge = errors.New("aseprite: unknown chunk exceeds the size limit")

	// ErrTooManyUnknownChunks reports that more unknown chunks were found
	// than Options.MaxUnknownChunks permits.
	ErrTooManyUnknownChunks = errors.New("aseprite: too many unknown chunks")

	// ErrDuplicateUnknownChunk reports that the same unknown chunk type
	// occurred more than once in a single frame.
	ErrDuplicateUnknownChunk = errors.New("aseprite: duplicate unknown chunk type")

	// ErrUnknownChunkContext reports that an unknown chunk could not be
	// attributed to a layer while Options.RequireUnknownChunkContext was set.
	ErrUnknownChunkContext = errors.New("aseprite: unknown chunk has no layer context")

	// ErrUnknownChunkFrame reports that an unknown chunk belongs to a frame
	// outside the frame count declared in the sprite header.
	ErrUnknownChunkFrame = errors.New("aseprite: unknown chunk is outside every declared frame")

	// ErrAtlasTooLarge reports that the sprite's declared dimensions and
	// frame count would require an atlas larger than Options.MaxAtlasSize.
	ErrAtlasTooLarge = errors.New("aseprite: texture atlas exceeds the size limit")
)

// Default audit limits. They bound the memory a single file can make the
// decoder retain for extension data. A negative Options field disables the
// corresponding limit.
const (
	// DefaultMaxUnknownChunkSize is the default payload limit per unknown
	// chunk (16 MiB).
	DefaultMaxUnknownChunkSize int64 = 16 << 20

	// DefaultMaxUnknownChunks is the default number of unknown chunks a
	// sprite may contain (1024).
	DefaultMaxUnknownChunks = 1024

	// DefaultMaxFileSize is the largest declared sprite file accepted
	// (1 GiB).
	DefaultMaxFileSize int64 = 1 << 30

	// DefaultMaxAtlasSize is the largest texture atlas allocated (1 GiB).
	DefaultMaxAtlasSize int64 = 1 << 30
)

// Options controls decoding and the unknown chunk audit.
//
// The zero value is safe and keeps the raw bytes of every unknown chunk (up to
// DefaultMaxUnknownChunkSize). Use ReadWithOptions, DecodeWithOptions or
// DecodeConfigWithOptions to override the defaults without changing the
// behaviour of Read, Decode and DecodeConfig.
type Options struct {
	// SkipUnknownChunkData records unknown chunk metadata but does not copy
	// the payload into UnknownChunk.Data. This keeps auditing a large file
	// cheap when only type, frame, layer and offsets are needed.
	SkipUnknownChunkData bool

	// MaxUnknownChunkSize is the maximum payload size in bytes of a single
	// unknown chunk. Zero means DefaultMaxUnknownChunkSize; a negative value
	// disables the limit.
	MaxUnknownChunkSize int64

	// MaxUnknownChunks is the maximum number of unknown chunks recorded for
	// one sprite. Zero means DefaultMaxUnknownChunks; a negative value
	// disables the limit.
	MaxUnknownChunks int

	// RejectDuplicateUnknownChunkTypes turns a repeated undocumented unknown
	// chunk type within one frame into ErrDuplicateUnknownChunk. By default a
	// repeat is recorded with UnknownChunk.Duplicate set and decoding
	// continues, so an extension that emits one record per layer or cel does
	// not change whether the sprite can be read. Chunk types the Aseprite
	// specification defines itself are never rejected here, matching their
	// documented per-cel/per-layer use.
	RejectDuplicateUnknownChunkTypes bool

	// RequireUnknownChunkContext rejects unknown chunks that appear before
	// any layer or cel chunk in their frame, and therefore cannot be
	// attributed to a layer. Frame attribution is always validated, so this
	// option only tightens the layer part of the audit.
	RequireUnknownChunkContext bool

	// MaxFileSize is the largest file size a sprite header may declare. Zero
	// means DefaultMaxFileSize; a negative value disables the limit.
	MaxFileSize int64

	// MaxAtlasSize is the largest texture atlas in bytes the decoder will
	// allocate. Sprite dimensions and the frame count come from the header,
	// so a tiny malformed file can otherwise request an enormous image. Zero
	// means DefaultMaxAtlasSize; a negative value disables the limit.
	MaxAtlasSize int64
}

// DecodeOptions is an alias for Options.
type DecodeOptions = Options

// DefaultOptions returns the options used by Read, Decode and DecodeConfig.
func DefaultOptions() Options {
	return Options{}
}

func (o Options) withDefaults() Options {
	if o.MaxUnknownChunkSize == 0 {
		o.MaxUnknownChunkSize = DefaultMaxUnknownChunkSize
	}
	if o.MaxUnknownChunks == 0 {
		o.MaxUnknownChunks = DefaultMaxUnknownChunks
	}
	if o.MaxFileSize == 0 {
		o.MaxFileSize = DefaultMaxFileSize
	}
	if o.MaxAtlasSize == 0 {
		o.MaxAtlasSize = DefaultMaxAtlasSize
	}
	return o
}

// UnknownChunk describes a chunk whose type is not understood by this
// package. Such chunks never influence the decoded image; they are recorded in
// file order for inspection.
//
// Values returned by UnknownChunks and its helpers are snapshots: their Data
// slice is a copy, so mutating it cannot alter the decoder's buffers or any
// other returned value.
type UnknownChunk struct {
	// Type is the 16-bit chunk type from the file.
	Type uint16

	// Frame is the zero-based frame index the chunk belongs to.
	Frame int

	// Chunk is the zero-based index of the chunk within its frame.
	Chunk int

	// Layer is the zero-based layer the chunk is associated with, or NoLayer
	// when no preceding layer/cel chunk supplied a context.
	Layer int

	// Offset is the absolute byte offset of the chunk header in the file.
	Offset int64

	// DataOffset is the absolute byte offset of the chunk payload (Offset+6).
	DataOffset int64

	// Size is the total chunk size including the 6-byte header.
	Size int

	// PayloadSize is the size of the chunk payload in bytes.
	PayloadSize int

	// Data is a copy of the chunk payload. It is nil when
	// Options.SkipUnknownChunkData is set.
	Data []byte

	// DataSkipped reports that the payload was intentionally not copied.
	DataSkipped bool

	// Duplicate reports that this chunk type already occurred earlier in the
	// same frame. The audit records such repeats; it only becomes an error
	// when Options.RejectDuplicateUnknownChunkTypes is set.
	Duplicate bool

	// SpecKnown reports that the type is documented by the Aseprite format
	// specification but is not interpreted by this package (for example the
	// color profile or external files chunks). It distinguishes "newer
	// extension" from "known but unsupported" without hiding either case.
	SpecKnown bool
}

// Raw returns a copy of the chunk payload. It is nil when the payload was not
// retained. Raw is safe to call on the zero value.
func (c UnknownChunk) Raw() []byte {
	if c.Data == nil {
		return nil
	}
	out := make([]byte, len(c.Data))
	copy(out, c.Data)
	return out
}

func (c UnknownChunk) clone() UnknownChunk {
	c.Data = c.Raw()
	return c
}

// formatError builds an ErrInvalidFormat error. Callers that know the chunk
// location wrap it with chunkError so the failure carries frame/offset
// context.
func formatError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidFormat, fmt.Sprintf(format, args...))
}

// chunkError attaches the location of ch, the index-th chunk of frame, to err.
// It leaves an error that already knows its location unchanged.
func chunkError(ch chunk, frame, index int, err error) error {
	var ce *ChunkError
	if errors.As(err, &ce) {
		return err
	}
	return &ChunkError{
		Type:   uint16(ch.typ),
		Frame:  frame,
		Chunk:  index,
		Layer:  NoLayer,
		Offset: ch.offset,
		Size:   ch.size,
		Err:    err,
	}
}

// ChunkError adds the location of an unknown chunk to an error returned by the
// decoder. It unwraps to a sentinel such as ErrUnknownChunkTooLarge,
// ErrTooManyUnknownChunks, ErrDuplicateUnknownChunk, ErrUnknownChunkContext,
// ErrUnknownChunkFrame, ErrTruncated or ErrInvalidFormat.
type ChunkError struct {
	Type   uint16
	Frame  int
	Chunk  int
	Layer  int
	Offset int64
	Size   int
	Err    error
}

// Error renders the sentinel plus the location context.
func (e *ChunkError) Error() string {
	layer := "no layer"
	if e.Layer != NoLayer {
		layer = fmt.Sprintf("layer %d", e.Layer)
	}
	return fmt.Sprintf("aseprite: frame %d chunk %d (type 0x%04X, offset %d, size %d, %s): %v",
		e.Frame, e.Chunk, e.Type, e.Offset, e.Size, layer, e.Err)
}

// Unwrap returns the sentinel error describing the audit failure.
func (e *ChunkError) Unwrap() error { return e.Err }

// UnknownChunks returns a snapshot of all unknown chunks in file order. Each
// returned Data slice is a copy, so editing the result cannot modify the
// decoder or subsequent queries.
func (spr *Aseprite) UnknownChunks() []UnknownChunk {
	if spr == nil || len(spr.unknownChunks) == 0 {
		return nil
	}
	out := make([]UnknownChunk, len(spr.unknownChunks))
	for i, c := range spr.unknownChunks {
		out[i] = c.clone()
	}
	return out
}

// NumUnknownChunks reports how many unknown chunks were recorded.
func (spr *Aseprite) NumUnknownChunks() int {
	if spr == nil {
		return 0
	}
	return len(spr.unknownChunks)
}

// UnknownChunkAt returns the unknown chunk at index i, in file order. The
// boolean is false when i is out of range. The returned Data slice is a copy.
func (spr *Aseprite) UnknownChunkAt(i int) (UnknownChunk, bool) {
	if spr == nil || i < 0 || i >= len(spr.unknownChunks) {
		return UnknownChunk{}, false
	}
	return spr.unknownChunks[i].clone(), true
}

// UnknownChunksByType returns all unknown chunks with the given 16-bit type.
func (spr *Aseprite) UnknownChunksByType(typ uint16) []UnknownChunk {
	return spr.filterUnknownChunks(func(c UnknownChunk) bool { return c.Type == typ })
}

// UnknownChunksByFrame returns all unknown chunks belonging to the given
// zero-based frame, in file order.
func (spr *Aseprite) UnknownChunksByFrame(frame int) []UnknownChunk {
	return spr.filterUnknownChunks(func(c UnknownChunk) bool { return c.Frame == frame })
}

// UnknownChunksByLayer returns all unknown chunks associated with the given
// zero-based layer, in file order.
func (spr *Aseprite) UnknownChunksByLayer(layer int) []UnknownChunk {
	return spr.filterUnknownChunks(func(c UnknownChunk) bool { return c.Layer == layer })
}

func (spr *Aseprite) filterUnknownChunks(keep func(UnknownChunk) bool) []UnknownChunk {
	if spr == nil {
		return nil
	}
	var out []UnknownChunk
	for _, c := range spr.unknownChunks {
		if keep(c) {
			out = append(out, c.clone())
		}
	}
	return out
}

// ReadWithOptions decodes an Aseprite image from r using opts.
func ReadWithOptions(r io.Reader, opts Options) (*Aseprite, error) {
	var spr Aseprite
	if err := spr.readFromOptions(r, opts); err != nil {
		return nil, err
	}
	return &spr, nil
}

// CheckUnknownChunks validates r with opts and returns the audit trail without
// building the texture atlas. It is useful for validating an asset pipeline
// without paying for image decoding; the returned chunks are snapshots exactly
// as ReadWithOptions would produce them.
func CheckUnknownChunks(r io.Reader, opts Options) ([]UnknownChunk, error) {
	var f file
	if _, err := f.ReadFromWithOptions(r, opts); err != nil {
		return nil, err
	}
	out := make([]UnknownChunk, len(f.unknownChunks))
	for i, c := range f.unknownChunks {
		out[i] = c.clone()
	}
	return out, nil
}
