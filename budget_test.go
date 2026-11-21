package aseprite

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"os"
	"runtime"
	"testing"

	"github.com/askeladdk/aseprite/internal/require"
)

// ---- in-memory aseprite file builder -------------------------------------

func put16(b []byte, v uint16) { binary.LittleEndian.PutUint16(b, v) }
func put32(b []byte, v uint32) { binary.LittleEndian.PutUint32(b, v) }

func buildChunk(typ uint16, payload []byte) []byte {
	b := make([]byte, 6+len(payload))
	put32(b, uint32(len(b)))
	put16(b[4:], typ)
	copy(b[6:], payload)
	return b
}

func buildFrame(chunks ...[]byte) []byte {
	n := 16
	for _, c := range chunks {
		n += len(c)
	}
	b := make([]byte, n)
	put32(b, uint32(n))
	put16(b[4:], 0xF1FA)
	put16(b[8:], 100) // duration
	put32(b[12:], uint32(len(chunks)))
	off := 16
	for _, c := range chunks {
		copy(b[off:], c)
		off += len(c)
	}
	return b
}

func buildLayerChunk() []byte {
	p := make([]byte, 18)
	put16(p, 1) // visible flag
	p[12] = 255 // opacity
	return buildChunk(0x2004, p)
}

// buildCelChunk builds a 32bpp cel chunk. body holds the dimension-specific
// payload (dims + pixels or dims + zlib stream).
func buildCelChunk(celType uint16, body []byte) []byte {
	p := make([]byte, 16)
	p[6] = 255 // opacity
	put16(p[7:], celType)
	p = append(p, body...)
	return buildChunk(0x2005, p)
}

func uncompressedCelBody(w, h int, pix []byte) []byte {
	b := make([]byte, 4+len(pix))
	put16(b, uint16(w))
	put16(b[2:], uint16(h))
	copy(b[4:], pix)
	return b
}

func compressedCelBody(w, h int, compressed []byte) []byte {
	b := make([]byte, 4+len(compressed))
	put16(b, uint16(w))
	put16(b[2:], uint16(h))
	copy(b[4:], compressed)
	return b
}

func zlibCompress(t *testing.T, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	_, err := w.Write(data)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return b.Bytes()
}

// buildFile assembles a complete file from a 128-byte header and frame bodies.
func buildFile(t *testing.T, w, h, bpp uint16, frameBodies ...[]byte) []byte {
	if t != nil {
		t.Helper()
	}
	hdr := make([]byte, 128)
	put16(hdr[4:], 0xA5E0)
	put16(hdr[6:], uint16(len(frameBodies)))
	put16(hdr[8:], w)
	put16(hdr[10:], h)
	put16(hdr[12:], bpp)
	if bpp == 8 {
		put16(hdr[32:], 256)
	}
	var body bytes.Buffer
	for _, fb := range frameBodies {
		body.Write(fb)
	}
	put32(hdr, uint32(128+body.Len()))
	return append(hdr, body.Bytes()...)
}

// validFileSeeds returns a minimal decodable file without needing a *testing.T.
func validFileSeeds() []byte {
	pix := make([]byte, 2*2*4)
	frame := buildFrame(buildLayerChunk(), buildCelChunk(0, uncompressedCelBody(2, 2, pix)))
	return buildFile(nil, 2, 2, 32, frame)
}

// validFile returns a minimal decodable 32bpp file: one 2x2 frame with one
// visible layer and one uncompressed cel.
func validFile(t *testing.T) []byte {
	t.Helper()
	return validFileSeeds()
}

func asLimitError(t *testing.T, err error) *LimitError {
	t.Helper()
	require.True(t, err != nil, "expected an error")
	var le *LimitError
	require.True(t, errors.As(err, &le), "expected LimitError, got", err)
	return le
}

// ---- budget rejection ------------------------------------------------------

func TestBudgetRejections(t *testing.T) {
	good := validFile(t)
	twoLayers := buildFile(t, 2, 2, 32, buildFrame(
		buildLayerChunk(), buildLayerChunk(),
		buildCelChunk(0, uncompressedCelBody(2, 2, make([]byte, 16))),
	))
	twoFrames := buildFile(t, 2, 2, 32,
		buildFrame(buildLayerChunk(), buildCelChunk(0, uncompressedCelBody(2, 2, make([]byte, 16)))),
		buildFrame(buildCelChunk(1, func() []byte {
			b := make([]byte, 2)
			put16(b, 0) // link to frame 0
			return b
		}())),
	)

	for _, tt := range []struct {
		name   string
		file   []byte
		budget Budget
		limit  string
	}{
		{"file bytes", good, Budget{MaxFileBytes: int64(len(good)) - 1}, "MaxFileBytes"},
		{"frames", twoFrames, Budget{MaxFrames: 1}, "MaxFrames"},
		{"layers", twoLayers, Budget{MaxLayers: 1}, "MaxLayers"},
		{"pixels", good, Budget{MaxPixels: 3}, "MaxPixels"},
		{"chunk length", good, Budget{MaxChunkLength: 10}, "MaxChunkLength"},
		{"cel bytes", good, Budget{MaxCelBytes: 15}, "MaxCelBytes"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Read(bytes.NewReader(tt.file), WithBudget(tt.budget))
			le := asLimitError(t, err)
			require.True(t, le.Limit == tt.limit, "limit name", le.Limit)
			require.True(t, le.Budget > 0 && le.Value > le.Budget, "value over budget")
			require.True(t, le.Offset >= 0, "offset is recorded", le.Offset)
		})
	}
}

func TestBudgetRejectionOffsets(t *testing.T) {
	good := validFile(t)

	// Header-level limits are detected at the header offsets before the body
	// is read.
	_, err := Read(bytes.NewReader(good), WithBudget(Budget{MaxFileBytes: 1}))
	le := asLimitError(t, err)
	require.True(t, le.Offset == 0, "file size offset", le.Offset)

	_, err = Read(bytes.NewReader(good), WithBudget(Budget{MaxPixels: 1}))
	le = asLimitError(t, err)
	require.True(t, le.Offset == 8, "canvas offset", le.Offset)

	// A limit between the layer chunk (24) and the cel chunk (42) trips on
	// the cel chunk specifically and reports its exact offset.
	celOffset := firstChunkOffset(good, 0x2005)
	require.True(t, celOffset == 168, "cel chunk offset", celOffset)
	_, err = Read(bytes.NewReader(good), WithBudget(Budget{MaxChunkLength: 30}))
	le = asLimitError(t, err)
	require.True(t, le.Limit == "MaxChunkLength", "limit", le.Limit)
	require.True(t, le.Offset == celOffset, "chunk offset", le.Offset)
}

// ---- boundary (critically legal) files are accepted ------------------------

func TestBudgetExactBoundaryAccepted(t *testing.T) {
	good := validFile(t)

	// The cel chunk is 6 chunk header + 16 cel header + (4 dims + 16 pixels).
	celChunkLen := 6 + 16 + 4 + 2*2*4
	require.True(t, celChunkLen == 42, "cel chunk length", celChunkLen)
	budget := Budget{
		MaxFileBytes:   int64(len(good)),
		MaxFrames:      1,
		MaxLayers:      1,
		MaxPixels:      4,
		MaxChunkLength: celChunkLen,
		MaxCelBytes:    16,
	}

	spr, err := Read(bytes.NewReader(good), WithBudget(budget))
	require.NoError(t, err)
	require.True(t, len(spr.Frames) == 1, "frames")
	require.True(t, spr.Bounds().Dx() == 2 && spr.Bounds().Dy() == 2, "bounds")
}

// ---- Read, Decode and DecodeConfig count consistently ----------------------

func TestBudgetConsistencyAcrossEntryPoints(t *testing.T) {
	good := validFile(t)

	for _, tt := range []struct {
		name   string
		budget Budget
		limit  string
	}{
		{"file bytes", Budget{MaxFileBytes: 1}, "MaxFileBytes"},
		{"frames", Budget{MaxFrames: 0 /* also rejected: zero count */}, ""},
		{"pixels", Budget{MaxPixels: 1}, "MaxPixels"},
		{"chunk length", Budget{MaxChunkLength: 10}, "MaxChunkLength"},
		{"layers", Budget{MaxLayers: 0}, ""},
	} {
		// Zero-valued limits mean unlimited, so skip those rows.
		if tt.limit == "" {
			continue
		}
		t.Run(tt.name, func(t *testing.T) {
			_, errRead := Read(bytes.NewReader(good), WithBudget(tt.budget))
			_, errDecode := Decode(bytes.NewReader(good), WithBudget(tt.budget))
			_, errConfig := DecodeConfig(bytes.NewReader(good), WithBudget(tt.budget))

			for _, err := range []error{errRead, errDecode, errConfig} {
				le := asLimitError(t, err)
				require.True(t, le.Limit == tt.limit, "limit", le.Limit)
			}
		})
	}

	// The cel budget only applies where cels are decoded; DecodeConfig never
	// touches cel data and must not be charged for it.
	_, err := DecodeConfig(bytes.NewReader(good), WithBudget(Budget{MaxCelBytes: 1}))
	require.NoError(t, err)

	_, err = Read(bytes.NewReader(good), WithBudget(Budget{MaxCelBytes: 1}))
	asLimitError(t, err)
}

// ---- zero budget means unlimited and stays backward compatible -------------

func TestZeroBudgetUnlimited(t *testing.T) {
	good := validFile(t)

	spr, err := Read(bytes.NewReader(good), WithBudget(Budget{}))
	require.NoError(t, err)
	require.True(t, len(spr.Frames) == 1, "frames")

	// The image.RegisterFormat adapters run without a budget.
	img, format, err := image.Decode(bytes.NewReader(good))
	require.NoError(t, err)
	require.True(t, format == "aseprite", "format")
	require.True(t, img.Bounds().Dx() == 2, "registered decode")
}

func TestExistingFilesUnderBudget(t *testing.T) {
	for _, name := range []string{
		"./testfiles/slime_paletted.aseprite",
		"./testfiles/slime_grayscale.aseprite",
		"./testfiles/blendtest.aseprite",
	} {
		t.Run(name, func(t *testing.T) {
			raw := readFileBytes(t, name)
			budget := Budget{
				MaxFileBytes:   int64(len(raw)),
				MaxFrames:      100,
				MaxLayers:      100,
				MaxPixels:      640 * 360,
				MaxChunkLength: 1 << 20,
				MaxCelBytes:    256 << 20,
			}
			spr, err := Read(bytes.NewReader(raw), WithBudget(budget))
			require.NoError(t, err)
			require.True(t, len(spr.Frames) > 0, "frames decoded")
		})
	}
}

func readFileBytes(t *testing.T, name string) []byte {
	t.Helper()
	data, err := io.ReadAll(mustOpen(t, name))
	require.NoError(t, err)
	return data
}

func mustOpen(t *testing.T, name string) io.Reader {
	t.Helper()
	f, err := os.Open(name)
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })
	return f
}

// ---- malformed inputs never panic ------------------------------------------

func TestMalformedFilesRejected(t *testing.T) {
	good := validFile(t)

	for _, tt := range []struct {
		name string
		file []byte
	}{
		{"truncated header", make([]byte, 64)},
		{"bad magic", patch(validFile(t), 4, []byte{0, 0})},
		{"zero width", patch(good, 8, []byte{0, 0})},
		{"zero height", patch(good, 10, []byte{0, 0})},
		{"file size below header", patch(good, 0, []byte{127, 0, 0, 0})},
		{"bad frame magic", patch(good, 128+4, []byte{0, 0})},
		{"frame length past eof", patch(good, 128, u32b(0xffff))},
		{"chunk length past frame", patch(good, 144, u32b(0xffff))},
		{"chunk length below header", patch(good, 144, u32b(4))},
		{"unknown cel type", patchCelType(t, good, 9)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Read(bytes.NewReader(tt.file))
			require.True(t, err != nil, "expected rejection")
			require.True(t, !errors.Is(err, &LimitError{}), "syntax errors are not limit errors")
		})
	}
}

func TestTruncatedCelAndBadLink(t *testing.T) {
	// Cel chunk whose payload is too short for its 16-byte cel header.
	frame := buildFrame(buildLayerChunk(), buildChunk(0x2005, make([]byte, 4)))
	file := buildFile(t, 2, 2, 32, frame)
	_, err := Read(bytes.NewReader(file))
	require.True(t, err != nil, "truncated cel rejected")

	// Linked cel pointing at a frame that does not exist.
	linkBody := make([]byte, 2)
	put16(linkBody, 42)
	badLink := buildFile(t, 2, 2, 32, buildFrame(
		buildLayerChunk(), buildCelChunk(1, linkBody)))
	_, err = Read(bytes.NewReader(badLink))
	require.True(t, err != nil, "bad link rejected")
}

func patch(file []byte, off int, b []byte) []byte {
	out := append([]byte(nil), file...)
	copy(out[off:], b)
	return out
}

func firstChunkOffset(file []byte, typ uint16) int64 {
	const frameStart = 128
	pos := frameStart + 16
	for pos+6 <= len(file) {
		chunkLen := int(binary.LittleEndian.Uint32(file[pos:]))
		if chunkLen <= 0 {
			return -1
		}
		if binary.LittleEndian.Uint16(file[pos+4:]) == typ {
			return int64(pos)
		}
		pos += chunkLen
	}
	return -1
}

func patchCelType(t *testing.T, file []byte, typ uint16) []byte {
	out := append([]byte(nil), file...)
	// Walk the chunks of the first frame to find the 0x2005 cel chunk.
	pos := 128 + 16
	for pos+6 <= len(out) {
		chunkLen := int(binary.LittleEndian.Uint32(out[pos:]))
		chunkTyp := binary.LittleEndian.Uint16(out[pos+4:])
		if chunkTyp == 0x2005 {
			put16(out[pos+6+7:], typ) // cel type is 7 bytes into the payload
			return out
		}
		pos += chunkLen
	}
	t.Fatal("no cel chunk found")
	return out
}

func u32b(v uint32) []byte {
	b := make([]byte, 4)
	put32(b, v)
	return b
}

// ---- zlib bomb and decompression accounting --------------------------------

func TestZlibBombRejectedByBudgetBeforeAllocation(t *testing.T) {
	// A 2x2 cel that declares 16 output bytes but whose zlib stream inflates
	// to 16 MiB of zeros.
	compressed := zlibCompress(t, make([]byte, 16<<20))
	frame := buildFrame(buildLayerChunk(),
		buildCelChunk(2, compressedCelBody(2, 2, compressed)))
	bomb := buildFile(t, 2, 2, 32, frame)

	// The cel budget trips before the zlib reader is even created.
	_, err := Read(bytes.NewReader(bomb), WithBudget(Budget{MaxCelBytes: 8}))
	le := asLimitError(t, err)
	require.True(t, le.Limit == "MaxCelBytes", "limit", le.Limit)
}

func TestZlibBombInflationCapped(t *testing.T) {
	compressed := zlibCompress(t, make([]byte, 16<<20))
	frame := buildFrame(buildLayerChunk(),
		buildCelChunk(2, compressedCelBody(2, 2, compressed)))
	bomb := buildFile(t, 2, 2, 32, frame)

	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)

	_, err := Read(bytes.NewReader(bomb)) // no cel budget: dimensions cap it
	require.True(t, err != nil, "oversized stream rejected")

	runtime.ReadMemStats(&m1)
	// Without the bounded reader the decoder would retain the full 16 MiB
	// inflation. The bounded path must keep cumulative new allocation far
	// below that (flate's own window is at most a few tens of KiB).
	if delta := m1.TotalAlloc - m0.TotalAlloc; delta >= 1<<20 {
		t.Fatalf("decompression allocated %d bytes, expected < 1 MiB", delta)
	}
}

func TestTruncatedZlibRejected(t *testing.T) {
	good := zlibCompress(t, make([]byte, 16))
	frame := buildFrame(buildLayerChunk(),
		buildCelChunk(2, compressedCelBody(2, 2, good[:len(good)-3])))
	file := buildFile(t, 2, 2, 32, frame)

	_, err := Read(bytes.NewReader(file))
	require.True(t, err != nil, "truncated zlib stream rejected")
}

func TestValidCompressedCelAccepted(t *testing.T) {
	pix := make([]byte, 2*2*4)
	for i := range pix {
		pix[i] = byte(i)
	}
	compressed := zlibCompress(t, pix)
	frame := buildFrame(buildLayerChunk(),
		buildCelChunk(2, compressedCelBody(2, 2, compressed)))
	file := buildFile(t, 2, 2, 32, frame)

	spr, err := Read(bytes.NewReader(file), WithBudget(Budget{MaxCelBytes: 16}))
	require.NoError(t, err)
	require.True(t, spr.Bounds().Dx() == 2, "decoded width")
}

func TestGrayscaleCelBudget(t *testing.T) {
	// 16bpp cels store 2 bytes/pixel in the file but expand to 4 bytes/pixel
	// NRGBA in memory, so the cel budget must charge the retained allocation.
	pix := make([]byte, 2*2*2) // 2x2 grayscale+alpha payload
	frame := buildFrame(buildLayerChunk(), buildCelChunk(0, uncompressedCelBody(2, 2, pix)))
	file := buildFile(t, 2, 2, 16, frame)

	// 4 pixels * 4 retained bytes = 16 bytes charged despite 8 source bytes.
	_, err := Read(bytes.NewReader(file), WithBudget(Budget{MaxCelBytes: 15}))
	asLimitError(t, err)

	spr, err := Read(bytes.NewReader(file), WithBudget(Budget{MaxCelBytes: 16}))
	require.NoError(t, err)
	require.True(t, spr.Bounds().Dx() == 2, "grayscale decoded")
}

func TestHugeFileSizeDoesNotAllocate(t *testing.T) {
	// A header that claims a near-4GiB file but supplies no body. Even with no
	// budget configured this must fail quickly and must not allocate the
	// declared size (regression test for a whole-file pre-allocation).
	hdr := make([]byte, 128)
	put16(hdr[4:], 0xA5E0)
	put16(hdr[6:], 1)
	put16(hdr[8:], 2)
	put16(hdr[10:], 2)
	put16(hdr[12:], 32)
	put32(hdr, 0xFFFFFFFF)

	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)

	_, err := Read(bytes.NewReader(hdr))
	require.True(t, err != nil, "truncated huge file rejected")

	runtime.ReadMemStats(&m1)
	if delta := int64(m1.TotalAlloc - m0.TotalAlloc); delta >= 1<<20 {
		t.Fatalf("declared 4GiB file allocated %d bytes", delta)
	}
}

func TestHugeChunkRejectedBeforeAllocation(t *testing.T) {
	// A valid-sized frame whose single chunk claims a huge payload. The frame
	// bounded reader cannot supply that many bytes, so the chunk is rejected
	// before its payload buffer is allocated.
	frame := make([]byte, 16)
	put32(frame, 128)
	put16(frame[4:], 0xF1FA)
	put16(frame[6:], 1)
	chunkHdr := make([]byte, 6)
	put32(chunkHdr, 0x7FFFFFFF)
	put16(chunkHdr[4:], 0x2004)
	file := buildFile(t, 2, 2, 32, append(frame, chunkHdr...))

	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)

	_, err := Read(bytes.NewReader(file))
	require.True(t, err != nil, "oversized chunk rejected")

	runtime.ReadMemStats(&m1)
	if delta := int64(m1.TotalAlloc - m0.TotalAlloc); delta >= 1<<20 {
		t.Fatalf("oversized chunk allocated %d bytes", delta)
	}
}

func TestHardCapAppliesWithoutBudget(t *testing.T) {
	// The unconditional hard ceiling trips even for a zero Budget, so an
	// attacker cannot opt out of resource limits by omitting WithBudget.
	hdr := make([]byte, 128)
	put16(hdr[4:], 0xA5E0)
	put16(hdr[6:], 1)
	put16(hdr[8:], 2)
	put16(hdr[10:], 2)
	put16(hdr[12:], 32)
	put32(hdr, uint32(hardResourceCap+1))

	_, err := Read(bytes.NewReader(hdr))
	le := asLimitError(t, err)
	require.True(t, le.Limit == "MaxFileBytes", "hard cap limit", le.Limit)
	require.True(t, le.Budget == hardResourceCap, "hard cap value", le.Budget)
}

func TestHugeCelDimensionsRejectedCheaply(t *testing.T) {
	// Declare a 40000x40000 cel (6.4 GiB) but supply a trivial zlib stream.
	compressed := zlibCompress(t, []byte{0})
	frame := buildFrame(buildLayerChunk(),
		buildCelChunk(2, compressedCelBody(40000, 40000, compressed)))
	file := buildFile(t, 2, 2, 32, frame)

	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)

	_, err := Read(bytes.NewReader(file), WithBudget(Budget{MaxCelBytes: 1 << 20}))
	le := asLimitError(t, err)
	require.True(t, le.Limit == "MaxCelBytes", "limit", le.Limit)

	runtime.ReadMemStats(&m1)
	if delta := m1.TotalAlloc - m0.TotalAlloc; delta >= 1<<20 {
		t.Fatalf("huge cel allocated %d bytes before budget rejection", delta)
	}
}
