package aseprite

import "io"

// boundedReader reads from R but caps the total number of bytes that can be
// consumed at N. It exists so the parser never allocates a buffer sized by an
// untrusted length field: callers read fixed-size structures through it and
// grow their working buffers only as real bytes arrive.
type boundedReader struct {
	R io.Reader
	N int64 // remaining bytes allowed
}

// Read passes through to the underlying reader while enforcing the cap. A read
// past the cap returns io.EOF so callers distinguish truncation from a clean
// end of stream via ReadFull.
func (b *boundedReader) Read(p []byte) (int, error) {
	if b.N <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > b.N {
		p = p[:b.N]
	}
	n, err := b.R.Read(p)
	b.N -= int64(n)
	return n, err
}

// readFull fills buf from r, returning a syntax error that carries the stream
// position if the stream ends early. offset is the absolute position of the
// first byte being read.
func readFull(r io.Reader, buf []byte, offset int64) error {
	if _, err := io.ReadFull(r, buf); err != nil {
		return &syntaxError{msg: "unexpected end of file", offset: offset}
	}
	return nil
}
