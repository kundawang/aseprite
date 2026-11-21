package aseprite

// Fault injection and adversarial read order for the decoder.
//
// The goal is not coverage of every malformed input but a guarantee that a
// malformed input fails the same way every time and never panics, never
// consumes unbounded memory, and never mutates the caller's bytes.
//
// Two fault models are used:
//
//  2. Mutation: deterministic seeded multi-byte corruptions, plus targeted
//     corruption of the fields most likely to cause an allocation or an
//     out-of-range index (file size, frame length, chunk count, cel
//     width/height, palette size, transparent index).
//
// Random read order is modelled with readers that deliver the same bytes one
// byte at a time and in pseudo-random chunk sizes. The decoder must be
// independent of how the stream is delivered.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// entryResult is the outcome of one decoder call: the returned error, or the
// panic value if the call panicked.
type entryResult struct {
	err   error
	panic any
}

// callEntry runs fn and reports its error, converting a panic into a result
// rather than letting it abort the test binary.
func callEntry(fn func() error) (res entryResult) {
	defer func() {
		if p := recover(); p != nil {
			res.panic = p
		}
	}()
	res.err = fn()
	return res
}

// runAllEntryPoints calls Read, Decode and DecodeConfig on the same bytes.
func runAllEntryPoints(input []byte) (read, decode, config entryResult) {
	read = callEntry(func() error {
		_, err := Read(bytes.NewReader(input))
		return err
	})
	decode = callEntry(func() error {
		_, err := Decode(bytes.NewReader(input))
		return err
	})
	config = callEntry(func() error {
		_, err := DecodeConfig(bytes.NewReader(input))
		return err
	})
	return
}

// truncationOffsets returns the prefix lengths to test for one fixture. Every
// byte of the 128-byte header and of the first 256 payload bytes is covered,
// as is every structural boundary (frame start/end, chunk start/end, payload
// start). Beyond that a bounded deterministic sample is used so the sweep
// stays linear in the fixture size: feeding every prefix of an 800 KiB file
// through a full decode would be quadratic.
func truncationOffsets(input []byte) []int {
	seen := map[int]struct{}{}
	add := func(n int) {
		if n >= 0 && n <= len(input) {
			seen[n] = struct{}{}
		}
	}
	for n := 0; n <= 128 && n <= len(input); n++ {
		add(n)
	}
	for n := 128; n <= 384 && n <= len(input); n++ {
		add(n)
	}

	// Walk the valid structure for frame/chunk boundaries. Bounds checks
	// mirror the decoder's, so a malformed fixture simply ends the walk.
	if len(input) > 128 {
		off := 128
		for off+16 <= len(input) {
			add(off)
			frameLen := int(binary.LittleEndian.Uint32(input[off:]))
			if frameLen < 16 || off+frameLen > len(input) {
				break
			}
			add(off + 16)
			add(off + frameLen)
			nchunks := int(binary.LittleEndian.Uint32(input[off+12:]))
			if nchunks == 0 {
				nchunks = int(binary.LittleEndian.Uint16(input[off+6:]))
			}
			coff := off + 16
			for i := 0; i < nchunks && coff+6 <= len(input); i++ {
				add(coff)
				chunkLen := int(binary.LittleEndian.Uint32(input[coff:]))
				if chunkLen < 6 || coff+chunkLen > len(input) {
					break
				}
				add(coff + 6)
				add(coff + chunkLen)
				add(coff + chunkLen - 1)
				coff += chunkLen
			}
			off += frameLen
		}
	}

	if len(input) <= 4096 {
		for n := 0; n <= len(input); n++ {
			add(n)
		}
	} else {
		// Deterministic spread over the remainder.
		const samples = 192
		for i := 0; i <= samples; i++ {
			add(384 + i*(len(input)-384)/samples)
		}
	}

	out := make([]int, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// TestFaultInjectionTruncation feeds prefixes of every fixture through all
// entry points. It asserts no panics and that both full decoders agree on
// whether a prefix decodes.
func TestFaultInjectionTruncation(t *testing.T) {
	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			input := readFixture(t, fx)
			offsets := truncationOffsets(input)
			for _, n := range offsets {
				prefix := append([]byte(nil), input[:n]...)
				wantHash := sha256Hex(prefix)
				ctx := fmt.Sprintf("%s prefix %d/%d", fx.Name, n, len(input))

				read, decode, config := runAllEntryPoints(prefix)
				if read.panic != nil {
					t.Fatalf("%s: Read panic: %v", ctx, read.panic)
				}
				if decode.panic != nil {
					t.Fatalf("%s: Decode panic: %v", ctx, decode.panic)
				}
				if config.panic != nil {
					t.Fatalf("%s: DecodeConfig panic: %v", ctx, config.panic)
				}
				if got := sha256Hex(prefix); got != wantHash {
					t.Fatalf("%s: input mutated by a failing decode", ctx)
				}

				// Read and Decode must agree about success. (Both wrap the
				// same parser, so they can only disagree if one path skips a
				// check the other performs.)
				if (read.err == nil) != (decode.err == nil) {
					t.Fatalf("%s: Read error = %v, Decode error = %v", ctx, read.err, decode.err)
				}
				if n == len(input) && read.err != nil {
					t.Fatalf("%s: full fixture rejected: %v", ctx, read.err)
				}
			}
		})
	}
}

// TestFaultInjectionTargetedFields corrupts the fields that steer allocation
// and indexing. Each case must produce an error (or a clean decode), never a
// panic, an OOM, or mutated input.
func TestFaultInjectionTargetedFields(t *testing.T) {
	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			base := readFixture(t, fx)
			cases := []struct {
				name   string
				offset int
				width  int // bytes to overwrite
				value  []byte
			}{
				{"file-size-zero", 0, 4, []byte{0, 0, 0, 0}},
				{"file-size-huge", 0, 4, []byte{0xFF, 0xFF, 0xFF, 0xFF}},
				{"file-size-short", 0, 4, []byte{128, 0, 0, 0}},
				{"frame-count-huge", 6, 2, []byte{0xFF, 0xFF}},
				{"frame-count-zero", 6, 2, []byte{0, 0}},
				{"width-huge", 8, 2, []byte{0xFF, 0xFF}},
				{"height-huge", 10, 2, []byte{0xFF, 0xFF}},
				{"bpp-invalid", 12, 2, []byte{0x01, 0x00}},
				{"palette-size-huge", 32, 2, []byte{0xFF, 0xFF}},
				{"palette-size-zero", 32, 2, []byte{0, 0}},
				{"transparent-out-of-range", 28, 1, []byte{0xFF}},
				{"pixel-ratio", 34, 2, []byte{2, 1}},
				{"frame-len-zero", 128, 4, []byte{0, 0, 0, 0}},
				{"frame-len-huge", 128, 4, []byte{0xFF, 0xFF, 0xFF, 0x7F}},
				{"frame-chunk-count-huge", 140, 4, []byte{0xFF, 0xFF, 0xFF, 0x7F}},
			}
			for _, tc := range cases {
				if tc.offset+tc.width > len(base) {
					continue
				}
				input := append([]byte(nil), base...)
				copy(input[tc.offset:], tc.value)
				hash := sha256Hex(input)
				ctx := fmt.Sprintf("%s %s", fx.Name, tc.name)

				read, decode, config := runAllEntryPoints(input)
				for _, r := range []entryResult{read, decode, config} {
					if r.panic != nil {
						t.Fatalf("%s: panic: %v", ctx, r.panic)
					}
				}
				if got := sha256Hex(input); got != hash {
					t.Fatalf("%s: input mutated", ctx)
				}
			}
		})
	}
}

// TestFaultInjectionMutations applies deterministic random corruptions and
// asserts that every outcome is a clean error or a clean decode. The number of
// mutations shrinks with fixture size so a large sprite does not dominate the
// suite runtime; the small, structurally diverse fixtures each get the full
// sweep.
func TestFaultInjectionMutations(t *testing.T) {
	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			base := readFixture(t, fx)
			iterations := 400
			if n := faultMutationBudget / len(base); n < iterations {
				iterations = n
			}
			if iterations < 24 {
				iterations = 24
			}
			rng := rand.New(rand.NewSource(int64(len(base)) * 2654435761))
			decoded, failed := 0, 0
			for i := 0; i < iterations; i++ {
				input := append([]byte(nil), base...)
				corruptions := 1 + rng.Intn(3)
				var detail strings.Builder
				for k := 0; k < corruptions; k++ {
					off := rng.Intn(len(input))
					bit := byte(1 << uint(rng.Intn(8)))
					input[off] ^= bit
					fmt.Fprintf(&detail, " off=%d^%#x", off, bit)
				}
				ctx := fmt.Sprintf("%s mutation %d (seed %d):%s", fx.Name, i, len(base)*2654435761, detail.String())

				read := callEntry(func() error {
					_, err := Read(bytes.NewReader(input))
					return err
				})
				if read.panic != nil {
					t.Fatalf("%s: panic: %v", ctx, read.panic)
				}
				cfg := callEntry(func() error {
					_, err := DecodeConfig(bytes.NewReader(input))
					return err
				})
				if cfg.panic != nil {
					t.Fatalf("%s: DecodeConfig panic: %v", ctx, cfg.panic)
				}
				if read.err != nil {
					failed++
				} else {
					decoded++
				}
			}
			// Sanity: the sweep must exercise both outcomes, otherwise the
			// mutations are not reaching the parser.
			if failed == 0 || decoded == 0 {
				t.Fatalf("%s: mutation sweep decoded %d and failed %d; expected both", fx.Name, decoded, failed)
			}
		})
	}
}

// TestFaultInjectionErrorsAreStableFails compares the error text produced by
// the same corrupt input across repeated decodes and across goroutines. A
// decode must not depend on map iteration order or any other nondeterminism.
func TestFaultInjectionErrorsAreStable(t *testing.T) {
	const workers = 4
	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			base := readFixture(t, fx)
			rng := rand.New(rand.NewSource(99))
			// Collect a few corrupt inputs that fail to decode.
			var failing [][]byte
			for tries := 0; tries < 1000 && len(failing) < 8; tries++ {
				input := append([]byte(nil), base...)
				for k := 0; k < 1+rng.Intn(3); k++ {
					input[rng.Intn(len(input))] ^= byte(1 + rng.Intn(255))
				}
				if _, err := Read(bytes.NewReader(input)); err != nil {
					failing = append(failing, input)
				}
			}
			if len(failing) == 0 {
				t.Skip("no failing corruption found")
			}
			for i, input := range failing {
				ctx := fmt.Sprintf("%s corrupt input %d", fx.Name, i)
				_, wantErr := Read(bytes.NewReader(input))
				if wantErr == nil {
					t.Fatalf("%s: expected an error", ctx)
				}
				// Stable across repeats.
				for k := 0; k < 8; k++ {
					if _, err := Read(bytes.NewReader(input)); err == nil || err.Error() != wantErr.Error() {
						t.Fatalf("%s: repeat %d error = %v, want %q", ctx, k, err, wantErr.Error())
					}
				}
				// Stable across goroutines.
				errs := make(chan string, workers)
				var done = make(chan struct{})
				for w := 0; w < workers; w++ {
					go func() {
						_, err := Read(bytes.NewReader(input))
						if err == nil {
							errs <- "<nil>"
						} else {
							errs <- err.Error()
						}
						done <- struct{}{}
					}()
				}
				for w := 0; w < workers; w++ {
					<-done
				}
				close(errs)
				for got := range errs {
					if got != wantErr.Error() {
						t.Fatalf("%s: concurrent error = %q, want %q", ctx, got, wantErr.Error())
					}
				}
			}
		})
	}
}

// byteAtATimeReader delivers its payload one byte per Read call.
type byteAtATimeReader struct {
	data []byte
	pos  int
}

func (r *byteAtATimeReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = r.data[r.pos]
	r.pos++
	return 1, nil
}

// chunkedReader delivers its payload in pseudo-random chunk sizes.
type chunkedReader struct {
	data []byte
	pos  int
	rng  *rand.Rand
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	n := 1 + r.rng.Intn(37)
	if n > len(p) {
		n = len(p)
	}
	if n > len(r.data)-r.pos {
		n = len(r.data) - r.pos
	}
	copy(p, r.data[r.pos:r.pos+n])
	r.pos += n
	return n, nil
}

// TestRandomReadOrder decodes through readers that deliver the same bytes in
// unusual chunk sizes and asserts the result matches a normal decode.
func TestRandomReadOrder(t *testing.T) {
	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			input := readFixture(t, fx)
			want, err := snapshotByRead(t, fx.Name+" baseline", input)
			if err != nil {
				t.Fatalf("%s: baseline Read: %v", fx.Name, err)
			}

			readers := map[string]func() io.Reader{
				"byte-at-a-time": func() io.Reader { return &byteAtATimeReader{data: input} },
			}
			for seed := int64(0); seed < 4; seed++ {
				seed := seed
				readers[fmt.Sprintf("chunked-seed-%d", seed)] = func() io.Reader {
					return &chunkedReader{data: input, rng: rand.New(rand.NewSource(seed))}
				}
			}
			// A reader that returns error after a random prefix length.
			names := make([]string, 0, len(readers))
			for name := range readers {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				spr, err := Read(readers[name]())
				if err != nil {
					t.Fatalf("%s %s: Read: %v", fx.Name, name, err)
				}
				got := snapshotSprite(t, fx.Name+" "+name, spr)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%s %s: decode differs from normal read\nfirst difference: %s",
						fx.Name, name, firstSnapshotDiff(want, got))
				}
			}
		})
	}
}

// TestPeakMemoryRecord decodes every fixture and records peak heap allocation.
// The bound is deliberately generous (the fixtures are small, but blendtest
// allocates two full-frame RGBA buffers) and exists to catch accidental
// quadratic allocation, not to benchmark.
func TestPeakMemoryRecord(t *testing.T) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	totalPixels := 0
	for _, fx := range fixtures {
		input := readFixture(t, fx)
		for i := 0; i < 4; i++ {
			spr, err := Read(bytes.NewReader(input))
			if err != nil {
				t.Fatalf("%s: Read: %v", fx.Name, err)
			}
			r := spr.Bounds()
			totalPixels += r.Dx() * r.Dy()
			runtime.KeepAlive(spr)
		}
	}

	runtime.GC()
	runtime.ReadMemStats(&after)

	const budget = peakMemoryBudget
	if after.TotalAlloc-before.TotalAlloc > budget {
		t.Fatalf("decoding all fixtures allocated %d bytes total, budget %d",
			after.TotalAlloc-before.TotalAlloc, budget)
	}
	// HeapAlloc after GC should be a small multiple of the largest atlas.
	if after.HeapAlloc > before.HeapAlloc+64<<20 {
		t.Fatalf("heap after decoding grew from %d to %d bytes; expected the decodes to be collectable",
			before.HeapAlloc, after.HeapAlloc)
	}
	t.Logf("fixtures=%d decodes=%d atoms pixels=%d totalAlloc=%d heapAlloc=%d sys=%d",
		len(fixtures), len(fixtures)*4, totalPixels,
		after.TotalAlloc-before.TotalAlloc, after.HeapAlloc, after.Sys)
}

// TestCorruptInputErrorContext asserts that errors from corrupt input carry
// stable context: either they wrap ErrInvalidFormat or they are a sentinel
// error, and the message is non-empty and single-line.
func TestCorruptInputErrorContext(t *testing.T) {
	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			base := readFixture(t, fx)
			rng := rand.New(rand.NewSource(7))
			checked := 0
			for tries := 0; tries < 2000 && checked < 32; tries++ {
				input := append([]byte(nil), base...)
				for k := 0; k < 1+rng.Intn(3); k++ {
					input[rng.Intn(len(input))] ^= byte(1 + rng.Intn(255))
				}
				_, err := Read(bytes.NewReader(input))
				if err == nil {
					continue
				}
				checked++
				msg := err.Error()
				if msg == "" {
					t.Fatalf("%s: empty error message", fx.Name)
				}
				if strings.Contains(msg, "\n") {
					t.Fatalf("%s: error message spans lines: %q", fx.Name, msg)
				}
				// ErrInvalidFormat or one of the stable sentinels/legacy
				// messages. io errors are acceptable for truncated input.
				switch {
				case errors.Is(err, ErrInvalidFormat):
				case errors.Is(err, errInvalidMagic):
				case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
				case msg == "invalid color depth",
					msg == "unsupported pixel ratio",
					msg == "unsupported cel type",
					msg == "tilemap layers not supported":
				default:
					t.Fatalf("%s: unstable error type %T: %q", fx.Name, err, msg)
				}
			}
			if checked == 0 {
				t.Fatalf("%s: no corrupt input was rejected", fx.Name)
			}
		})
	}
}

// TestHeaderValidation documents the bounds checks on the header, which are
// what keep a corrupt file size from turning into a huge allocation.
func TestHeaderValidation(t *testing.T) {
	base := readFixture(t, fixtures[0])

	// A header that declares an absurd file size must be rejected before any
	// payload is read or allocated.
	header := append([]byte(nil), base[:128]...)
	binary.LittleEndian.PutUint32(header[0:], 0x7FFFFFFF)
	if _, err := DecodeConfig(bytes.NewReader(header)); !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("huge file size: error = %v, want ErrInvalidFormat", err)
	}

	// A header-only file with a valid size but no frames must be rejected
	// rather than panicking in frame 0 handling.
	header = append([]byte(nil), base[:128]...)
	binary.LittleEndian.PutUint32(header[0:], 128)
	binary.LittleEndian.PutUint16(header[6:], 0)
	if _, err := DecodeConfig(bytes.NewReader(header)); !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("no frames: error = %v, want ErrInvalidFormat", err)
	}

	// An out-of-range transparent index must not panic.
	whole := append([]byte(nil), base...)
	whole[28] = 0xFF
	if _, err := DecodeConfig(bytes.NewReader(whole)); err != nil {
		t.Fatalf("out-of-range transparent index: %v", err)
	}
	if _, err := Read(bytes.NewReader(whole)); err != nil {
		t.Fatalf("out-of-range transparent index (full decode): %v", err)
	}
}
