package aseprite

import (
	"errors"
	"fmt"
)

// ErrTruncated reports that the input ended before a field, frame, chunk or
// pixel buffer declared by the file could be read in full.
//
// The error is stable across inputs: callers may test for it with errors.Is
// and rely on the wrapped ParseError for the location of the failure.
var ErrTruncated = errors.New("aseprite: truncated input")

// ErrMalformed reports that the input is structurally invalid even though it
// was fully readable: bad magic numbers, impossible lengths, indices outside
// their table, corrupt compressed streams, and similar defects.
//
// The error is stable across inputs: callers may test for it with errors.Is
// and rely on the wrapped ParseError for the location of the failure.
var ErrMalformed = errors.New("aseprite: malformed input")

// ErrUnsupported reports that the input is well formed but uses a feature
// that this decoder does not implement, such as tilemap layers or an invalid
// color depth.
var ErrUnsupported = errors.New("aseprite: unsupported feature")

// ParseError describes where in the file a failure occurred.
//
// Frame, Layer, Chunk and Offset are set to -1 when the failure happened
// before that part of the file was reached: a header problem has Frame, Layer
// and Chunk equal to -1, while a chunk-level problem carries the frame and
// chunk indices (and a layer index when the failing chunk belongs to a layer).
// Offset is the byte offset of the failing element from the start of the file,
// or -1 if unknown.
type ParseError struct {
	// Frame is the index of the frame being decoded, or -1.
	Frame int
	// Layer is the layer index being decoded, or -1 when not applicable.
	Layer int
	// Chunk is the index of the chunk within the frame, or -1.
	Chunk int
	// Offset is the byte offset of the failing element, or -1 if unknown.
	Offset int64
	// Kind is one of ErrTruncated, ErrMalformed or ErrUnsupported.
	Kind error
	// Detail is a human-readable description of the failure.
	Detail string
}

func (e *ParseError) Error() string {
	var where string
	if e.Frame >= 0 {
		where = fmt.Sprintf("frame %d", e.Frame)
	}
	if e.Layer >= 0 {
		where += fmt.Sprintf(", layer %d", e.Layer)
	}
	if e.Chunk >= 0 {
		where += fmt.Sprintf(", chunk %d", e.Chunk)
	}
	if e.Offset >= 0 {
		where += fmt.Sprintf(" at offset %d", e.Offset)
	}
	if where == "" {
		where = "header"
	}
	return fmt.Sprintf("%s (%s): %s", e.Kind, where, e.Detail)
}

// Unwrap returns the stable error category.
func (e *ParseError) Unwrap() error { return e.Kind }

// parseErr builds a ParseError with every context field set.
func parseErr(kind error, frame, layer, chunk int, ofs int64, format string, args ...any) *ParseError {
	return &ParseError{
		Frame:  frame,
		Layer:  layer,
		Chunk:  chunk,
		Offset: ofs,
		Kind:   kind,
		Detail: fmt.Sprintf(format, args...),
	}
}

// context carries the parse position used to decorate errors. The zero value
// is ready to use and describes a header-level error.
type context struct {
	frame  int
	layer  int
	chunk  int
	offset int64
}

func newContext() context {
	return context{frame: -1, layer: -1, chunk: -1, offset: -1}
}

func (c context) err(kind error, format string, args ...any) *ParseError {
	return parseErr(kind, c.frame, c.layer, c.chunk, c.offset, format, args...)
}

func (c context) at(offset int64) context {
	c.offset = offset
	return c
}

func (c context) frameAt(i int, offset int64) context {
	c.frame = i
	c.layer = -1
	c.chunk = -1
	c.offset = offset
	return c
}

func (c context) chunkAt(i int, offset int64) context {
	c.chunk = i
	c.offset = offset
	return c
}

func (c context) layerAt(i int) context {
	c.layer = i
	return c
}
