package aseprite

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"sync"
	"time"
)

// FrameError describes an error encountered while scanning or decoding a
// specific frame of an Aseprite file.
type FrameError struct {
	// Frame is the 0-based index of the frame that caused the error.
	Frame int

	// Err is the underlying error.
	Err error
}

func (e *FrameError) Error() string {
	return fmt.Sprintf("aseprite: frame %d: %v", e.Frame, e.Err)
}

func (e *FrameError) Unwrap() error { return e.Err }

// ChunkInfo summarizes a single chunk inside a frame.
type ChunkInfo struct {
	// Type is the chunk type, e.g. 0x2004 (layer), 0x2005 (cel),
	// 0x2018 (tags), 0x2019 (palette).
	Type uint16

	// Bytes is the size of the chunk payload in bytes,
	// excluding the 6-byte chunk header.
	Bytes int64
}

// FrameInfo describes a single frame located by an Index scan.
type FrameInfo struct {
	// Offset is the absolute byte offset of the frame record in the source.
	Offset int64

	// Bytes is the total size of the frame record in bytes,
	// including its 16-byte header.
	Bytes int64

	// Duration is the time the frame should be displayed for.
	Duration time.Duration

	// Chunks summarizes the chunks inside the frame, in file order.
	Chunks []ChunkInfo
}

// Index is a frame index over an Aseprite file that decodes frames lazily.
//
// The index is built by a cheap scan that validates the file header and
// every frame boundary and records each frame's offset, duration and chunk
// summary. No cel is decompressed during the scan; individual frames are
// decoded on demand by Frame.
//
// An Index is created in one of two explicit modes:
//
//   - OpenIndex (random-access mode) indexes an io.ReadSeeker. Only the
//     file header, frame headers and chunk headers are read during the
//     scan (plus layer and palette metadata from the first frame); frame
//     payloads are seeked to and read on demand. The caller retains
//     ownership of the source: it must remain open and must not be
//     repositioned by other code until the last Frame call. Calling Frame
//     after the source has been closed returns the source's error wrapped
//     in a *FrameError.
//   - ScanIndex (stream mode) consumes a one-shot io.Reader completely and
//     buffers the raw, still-compressed frame records. After ScanIndex
//     returns, the source is no longer needed and may be closed; Frame
//     decodes from the buffers.
//
// Decoded cels are cached, so repeated Frame calls — and frames whose
// linked cels reference already-decoded frames — reuse earlier work.
// An Index is safe for concurrent use.
type Index struct {
	// Frames describes every frame in file order, as located by the scan.
	Frames []FrameInfo

	f    *file
	size int64

	rs  io.ReadSeeker // random-access mode
	raw [][]byte      // stream mode: buffered raw frame records

	havePalette bool
	mu          sync.Mutex
}

func newIndex() *Index {
	return &Index{f: &file{}}
}

// OpenIndex builds a frame index over a seekable Aseprite source without
// decoding any frame. See the Index documentation for the ownership and
// lifetime rules of rs.
func OpenIndex(rs io.ReadSeeker) (*Index, error) {
	ix := newIndex()

	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	if err := ix.readHeader(rs); err != nil {
		return nil, err
	}

	// Clamp the scan to the actual source size so that a file truncated
	// with respect to its header is reported at the truncated frame.
	if end, err := rs.Seek(0, io.SeekEnd); err != nil {
		return nil, err
	} else if end < ix.size {
		ix.size = end
	}

	for off := int64(128); off < ix.size; {
		fi, err := ix.scanFrame(rs, off, len(ix.Frames))
		if err != nil {
			return nil, err
		}
		ix.Frames = append(ix.Frames, fi)
		off += fi.Bytes
	}

	ix.f.frames = make([]frame, len(ix.Frames))
	ix.rs = rs
	return ix, nil
}

// ScanIndex builds a frame index from a one-shot stream. The stream is
// consumed completely and the raw (still compressed) frame records are
// buffered, so the source may be closed as soon as ScanIndex returns.
// Use OpenIndex to avoid buffering when the source is seekable.
func ScanIndex(r io.Reader) (*Index, error) {
	ix := newIndex()

	if err := ix.readHeader(r); err != nil {
		return nil, err
	}

	for off := int64(128); off < ix.size; {
		idx := len(ix.Frames)

		var hdr [16]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return nil, &FrameError{idx, fmt.Errorf("reading frame header: %w", err)}
		}

		frameLen, err := ix.checkFrameHeader(hdr[:], off, idx)
		if err != nil {
			return nil, err
		}

		raw := make([]byte, frameLen)
		copy(raw, hdr[:])
		if _, err := io.ReadFull(r, raw[16:]); err != nil {
			return nil, &FrameError{idx, fmt.Errorf("frame truncated: %w", err)}
		}

		fr, _, err := parseFrame(raw, idx)
		if err != nil {
			return nil, err
		}

		fi := FrameInfo{Offset: off, Bytes: frameLen, Duration: fr.dur}
		for _, ch := range fr.chunks {
			fi.Chunks = append(fi.Chunks, ChunkInfo{Type: uint16(ch.typ), Bytes: int64(len(ch.raw))})
			if idx == 0 {
				if err := ix.parseMetaChunk(ch.typ, ch.raw); err != nil {
					return nil, &FrameError{0, err}
				}
			}
		}

		ix.Frames = append(ix.Frames, fi)
		ix.raw = append(ix.raw, raw)
		off += frameLen
	}

	ix.f.frames = make([]frame, len(ix.Frames))
	return ix, nil
}

// Size returns the frame dimensions in pixels.
func (ix *Index) Size() (width, height int) {
	return ix.f.framew, ix.f.frameh
}

// Frame decodes and composites frame n (0-based) and returns its image.
// Linked cels are resolved by decoding the referenced (earlier) frame
// first; decoded cels are cached, so repeated calls reuse earlier work.
// The returned image is newly allocated on each call and may be retained
// and modified by the caller.
func (ix *Index) Frame(n int) (image.Image, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	if n < 0 || n >= len(ix.Frames) {
		return nil, fmt.Errorf("aseprite: frame index %d out of range [0, %d)", n, len(ix.Frames))
	}

	if err := ix.decodeFrame(n); err != nil {
		return nil, err
	}

	bounds := image.Rect(0, 0, ix.f.framew, ix.f.frameh)
	dst := image.NewRGBA(bounds)
	drawCels(dst, image.NewRGBA(bounds), ix.f.layers, ix.f.frames[n].cels)
	return dst, nil
}

func (ix *Index) readHeader(r io.Reader) error {
	var hdr [128]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}

	size, err := ix.f.readHeader(hdr[:])
	if err != nil {
		return err
	}

	if size < 128 {
		return errors.New("aseprite: invalid file size in header")
	}

	ix.size = size
	return nil
}

// checkFrameHeader validates a 16-byte frame header at offset off and
// returns the frame length declared in it.
func (ix *Index) checkFrameHeader(hdr []byte, off int64, idx int) (int64, error) {
	if magic := binary.LittleEndian.Uint16(hdr[4:]); magic != 0xF1FA {
		return 0, &FrameError{idx, errInvalidMagic}
	}

	frameLen := int64(binary.LittleEndian.Uint32(hdr[0:]))
	if frameLen < 16 || off+frameLen > ix.size {
		return 0, &FrameError{idx, fmt.Errorf("invalid frame length %d at offset %d (file size %d)", frameLen, off, ix.size)}
	}

	return frameLen, nil
}

// scanFrame reads the frame and chunk headers of the frame at offset off
// from a seekable source, skipping over chunk payloads (except layer and
// palette metadata in the first frame).
func (ix *Index) scanFrame(rs io.ReadSeeker, off int64, idx int) (FrameInfo, error) {
	fi := FrameInfo{Offset: off}

	if _, err := rs.Seek(off, io.SeekStart); err != nil {
		return fi, err
	}

	var hdr [16]byte
	if _, err := io.ReadFull(rs, hdr[:]); err != nil {
		return fi, &FrameError{idx, fmt.Errorf("reading frame header: %w", err)}
	}

	frameLen, err := ix.checkFrameHeader(hdr[:], off, idx)
	if err != nil {
		return fi, err
	}

	fi.Bytes = frameLen
	fi.Duration = time.Millisecond * time.Duration(binary.LittleEndian.Uint16(hdr[8:]))

	nchunks := int64(binary.LittleEndian.Uint32(hdr[12:]))
	if nchunks == 0 {
		nchunks = int64(binary.LittleEndian.Uint16(hdr[6:]))
	}
	if nchunks > (frameLen-16)/6 {
		return fi, &FrameError{idx, fmt.Errorf("chunk count %d exceeds frame length %d", nchunks, frameLen)}
	}

	pos := off + 16
	var chdr [6]byte
	for i := int64(0); i < nchunks; i++ {
		if _, err := io.ReadFull(rs, chdr[:]); err != nil {
			return fi, &FrameError{idx, fmt.Errorf("chunk %d: %w", i, err)}
		}

		chunkLen := int64(binary.LittleEndian.Uint32(chdr[0:]))
		typ := int(binary.LittleEndian.Uint16(chdr[4:]))
		if chunkLen < 6 || pos+chunkLen > off+frameLen {
			return fi, &FrameError{idx, fmt.Errorf("chunk %d: invalid chunk length %d", i, chunkLen)}
		}

		payload := chunkLen - 6
		if idx == 0 && (typ == 0x2004 || typ == 0x2019) {
			raw := make([]byte, payload)
			if _, err := io.ReadFull(rs, raw); err != nil {
				return fi, &FrameError{idx, fmt.Errorf("chunk %d: %w", i, err)}
			}
			if err := ix.parseMetaChunk(typ, raw); err != nil {
				return fi, &FrameError{idx, err}
			}
		} else if _, err := rs.Seek(payload, io.SeekCurrent); err != nil {
			return fi, err
		}

		fi.Chunks = append(fi.Chunks, ChunkInfo{Type: uint16(typ), Bytes: payload})
		pos += chunkLen
	}

	if pos != off+frameLen {
		return fi, &FrameError{idx, fmt.Errorf("%d trailing bytes after last chunk", off+frameLen-pos)}
	}

	return fi, nil
}

// parseMetaChunk parses the first frame's layer and palette chunks, which
// are needed to decode any frame.
func (ix *Index) parseMetaChunk(typ int, raw []byte) error {
	switch typ {
	case 0x2004: // layer
		var l layer
		if err := l.Parse(raw); err != nil {
			return err
		}
		ix.f.layers = append(ix.f.layers, l)
	case 0x2019: // palette
		if !ix.havePalette {
			ix.havePalette = true
			if err := ix.f.parseChunk2019(raw); err != nil {
				return err
			}
			ix.f.applyTransparent()
		}
	}
	return nil
}

// decodeFrame decodes the cels of frame idx into the cache.
func (ix *Index) decodeFrame(idx int) error {
	if ix.f.frames[idx].cels != nil {
		return nil
	}

	var raw []byte
	if ix.raw != nil {
		raw = ix.raw[idx]
	} else {
		fi := ix.Frames[idx]
		buf := make([]byte, fi.Bytes)
		if _, err := ix.rs.Seek(fi.Offset, io.SeekStart); err != nil {
			return &FrameError{idx, err}
		}
		if _, err := io.ReadFull(ix.rs, buf); err != nil {
			return &FrameError{idx, fmt.Errorf("reading frame data: %w", err)}
		}
		raw = buf
	}

	fr, _, err := parseFrame(raw, idx)
	if err != nil {
		return err
	}

	// Decode frames referenced by linked cels first. Links may only point
	// backwards, so the recursion always terminates.
	for _, ch := range fr.chunks {
		if ch.typ != 0x2005 || len(ch.raw) < 18 {
			continue
		}
		if binary.LittleEndian.Uint16(ch.raw[7:]) != 1 { // linked cel
			continue
		}
		srcFrame := int(binary.LittleEndian.Uint16(ch.raw[16:]))
		if srcFrame >= idx {
			return &FrameError{idx, fmt.Errorf("linked cel references frame %d (self or forward reference)", srcFrame)}
		}
		if err := ix.decodeFrame(srcFrame); err != nil {
			return err
		}
	}

	ix.f.frames[idx].cels = make([]cel, len(ix.f.layers))
	for _, ch := range fr.chunks {
		if ch.typ != 0x2005 {
			continue
		}
		if _, err := ix.f.parseChunk2005(idx, ch.raw); err != nil {
			return &FrameError{idx, err}
		}
	}

	return nil
}
