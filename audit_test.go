package aseprite

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"os"
	"reflect"
	"testing"
)

// rgbaBytes flattens an image so two decodes can be compared pixel by pixel.
func rgbaBytes(img image.Image) []byte {
	r := img.Bounds()
	out := make([]byte, 0, r.Dx()*r.Dy()*4)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := img.At(x, y)
			cr, cg, cb, ca := c.RGBA()
			out = append(out, byte(cr>>8), byte(cg>>8), byte(cb>>8), byte(ca>>8))
		}
	}
	return out
}

func mustDecodeBytes(t *testing.T, data []byte) *Aseprite {
	t.Helper()
	spr, err := Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return spr
}

// testUnknownOffset is the absolute offset of the first unknown chunk appended
// by testBasicSprite.
func testUnknownOffset(bpp uint16) int64 {
	return testChunkOffset(bpp, 2)
}

func TestUnknownChunkAuditMinimal(t *testing.T) {
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x01, 0x02}
	spec := testBasicSprite(32, testUnknownChunk(0xF00D, payload))
	data := spec.encode()

	spr := mustDecodeBytes(t, data)
	got := spr.UnknownChunks()
	if len(got) != 1 {
		t.Fatalf("got %d unknown chunks, want 1", len(got))
	}

	c := got[0]
	wantOffset := testUnknownOffset(32)
	if c.Type != 0xF00D {
		t.Errorf("Type = %#04x, want 0xf00d", c.Type)
	}
	if c.Frame != 0 {
		t.Errorf("Frame = %d, want 0", c.Frame)
	}
	if c.Chunk != 2 {
		t.Errorf("Chunk = %d, want 2", c.Chunk)
	}
	if c.Layer != 0 {
		t.Errorf("Layer = %d, want 0", c.Layer)
	}
	if c.Offset != wantOffset {
		t.Errorf("Offset = %d, want %d", c.Offset, wantOffset)
	}
	if c.DataOffset != c.Offset+6 {
		t.Errorf("DataOffset = %d, want %d", c.DataOffset, c.Offset+6)
	}
	if c.PayloadSize != len(payload) || c.Size != len(payload)+6 {
		t.Errorf("size = %d payload = %d, want %d/%d", c.Size, c.PayloadSize, len(payload)+6, len(payload))
	}
	if !bytes.Equal(c.Data, payload) {
		t.Errorf("Data = %x, want %x", c.Data, payload)
	}
	if c.SpecKnown || c.Duplicate || c.DataSkipped {
		t.Errorf("unexpected flags: %+v", c)
	}
	// The recorded bytes must come from the file at the reported offset.
	fileData := data[c.DataOffset : c.DataOffset+int64(c.PayloadSize)]
	if !bytes.Equal(fileData, c.Data) {
		t.Errorf("recorded data does not match bytes at the reported offset")
	}
}

func TestUnknownChunkPixelInvariance(t *testing.T) {
	for _, bpp := range []uint16{8, 16, 32} {
		bpp := bpp
		t.Run(colorDepthName(bpp), func(t *testing.T) {
			plain := mustDecodeBytes(t, testBasicSprite(bpp).encode())
			with := mustDecodeBytes(t, testBasicSprite(bpp,
				testUnknownChunk(0xF1A1, []byte{1, 2, 3}),
				testUnknownChunk(0xF1A2, []byte{4, 5, 6, 7}),
			).encode())

			if !bytes.Equal(rgbaBytes(plain), rgbaBytes(with)) {
				t.Fatal("pixels changed when unknown chunks were present")
			}
			if len(with.UnknownChunks()) != 2 {
				t.Fatalf("got %d unknown chunks, want 2", len(with.UnknownChunks()))
			}
			if plain.NumUnknownChunks() != 0 {
				t.Fatalf("plain sprite reported %d unknown chunks", plain.NumUnknownChunks())
			}
		})
	}
}

func colorDepthName(bpp uint16) string {
	switch bpp {
	case 8:
		return "8bpp"
	case 16:
		return "16bpp"
	default:
		return "32bpp"
	}
}

func TestUnknownChunkSkipData(t *testing.T) {
	payload := []byte{9, 8, 7, 6}
	data := testBasicSprite(32, testUnknownChunk(0xABCD, payload)).encode()

	spr, err := ReadWithOptions(bytes.NewReader(data), Options{SkipUnknownChunkData: true})
	if err != nil {
		t.Fatal(err)
	}
	got := spr.UnknownChunks()
	if len(got) != 1 {
		t.Fatalf("got %d unknown chunks", len(got))
	}
	c := got[0]
	if c.Data != nil {
		t.Errorf("Data = %v, want nil", c.Data)
	}
	if !c.DataSkipped {
		t.Error("DataSkipped = false, want true")
	}
	if c.PayloadSize != len(payload) {
		t.Errorf("PayloadSize = %d, want %d", c.PayloadSize, len(payload))
	}
	if c.Offset != testUnknownOffset(32) {
		t.Errorf("Offset = %d, want %d", c.Offset, testUnknownOffset(32))
	}
}

func TestUnknownChunkSizeLimit(t *testing.T) {
	payload := make([]byte, 32)
	data := testBasicSprite(32, testUnknownChunk(0xF00D, payload)).encode()

	_, err := ReadWithOptions(bytes.NewReader(data), Options{MaxUnknownChunkSize: 16})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrUnknownChunkTooLarge) {
		t.Fatalf("error = %v, want ErrUnknownChunkTooLarge", err)
	}
	var ce *ChunkError
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a *ChunkError", err)
	}
	if ce.Type != 0xF00D || ce.Frame != 0 || ce.Chunk != 2 || ce.Layer != 0 || ce.Offset != testUnknownOffset(32) {
		t.Errorf("unexpected context: %+v", ce)
	}

	// A limit exactly equal to the payload must be accepted.
	if _, err := ReadWithOptions(bytes.NewReader(data), Options{MaxUnknownChunkSize: 32}); err != nil {
		t.Fatalf("payload at the limit rejected: %v", err)
	}

	// Negative disables the limit.
	if _, err := ReadWithOptions(bytes.NewReader(data), Options{MaxUnknownChunkSize: -1}); err != nil {
		t.Fatalf("disabled limit rejected: %v", err)
	}
}

func TestUnknownChunkCountLimit(t *testing.T) {
	data := testBasicSprite(32,
		testUnknownChunk(0x1111, []byte{1}),
		testUnknownChunk(0x2222, []byte{2}),
	).encode()

	_, err := ReadWithOptions(bytes.NewReader(data), Options{MaxUnknownChunks: 1})
	if !errors.Is(err, ErrTooManyUnknownChunks) {
		t.Fatalf("error = %v, want ErrTooManyUnknownChunks", err)
	}
	var ce *ChunkError
	if errors.As(err, &ce) {
		if ce.Type != 0x2222 {
			t.Errorf("error type = %#04x, want 0x2222", ce.Type)
		}
	} else {
		t.Fatal("error is not a *ChunkError")
	}
}

func TestUnknownChunkDuplicate(t *testing.T) {
	data := testBasicSprite(32,
		testUnknownChunk(0xF00D, []byte{1}),
		testUnknownChunk(0xF00D, []byte{2}),
	).encode()

	// By default a repeat is recorded rather than rejected: whether an
	// extension may repeat is not knowable to the decoder, so auditing must
	// not change whether the sprite can be read.
	spr := mustDecodeBytes(t, data)
	got := spr.UnknownChunks()
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2", len(got))
	}
	if got[0].Duplicate || !got[1].Duplicate {
		t.Errorf("Duplicate flags = %v/%v, want false/true", got[0].Duplicate, got[1].Duplicate)
	}

	// Strict callers can ask for repeats to be an error with context.
	_, err := ReadWithOptions(bytes.NewReader(data), Options{RejectDuplicateUnknownChunkTypes: true})
	if !errors.Is(err, ErrDuplicateUnknownChunk) {
		t.Fatalf("error = %v, want ErrDuplicateUnknownChunk", err)
	}
	var ce *ChunkError
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a *ChunkError", err)
	}
	if ce.Type != 0xF00D || ce.Frame != 0 || ce.Chunk != 3 || ce.Layer != 0 {
		t.Errorf("unexpected context: %+v", ce)
	}
}

func TestUnknownChunkSpecDefinedMayRepeat(t *testing.T) {
	// 0x2006 is documented (Cel Extra) but not interpreted. It is allowed to
	// appear once per cel, so two occurrences must not trip the duplicate
	// guard even when strict duplicate rejection is requested.
	data := testBasicSprite(32,
		testUnknownChunk(0x2006, []byte{1}),
		testUnknownChunk(0x2006, []byte{2}),
	).encode()

	spr, err := ReadWithOptions(bytes.NewReader(data), Options{RejectDuplicateUnknownChunkTypes: true})
	if err != nil {
		t.Fatal(err)
	}
	got := spr.UnknownChunks()
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2", len(got))
	}
	for _, c := range got {
		if !c.SpecKnown {
			t.Errorf("type %#04x not marked SpecKnown", c.Type)
		}
	}
}

// TestUnknownChunkLayerAttributionAcrossLayers covers a frame with two layers
// and an unknown chunk after each one, plus a later frame whose chunks have no
// preceding layer chunk and must inherit context from their cel.
func TestUnknownChunkLayerAttributionAcrossLayers(t *testing.T) {
	spec := testSpriteSpec{
		w: 2, h: 2, bpp: 32,
		frames: []testFrame{
			{dur: 100, chunks: []testChunk{
				testLayerChunk("A"),
				testUnknownChunk(0x0A0A, []byte{1}),
				testLayerChunk("B"),
				testUnknownChunk(0x0B0B, []byte{2}),
				testCelChunkRaw(1, 0, 0, 2, 2, testOpaquePixels(2, 2, 32)),
			}},
			{dur: 100, chunks: []testChunk{
				testCelChunkRaw(0, 0, 0, 2, 2, testOpaquePixels(2, 2, 32)),
				testUnknownChunk(0x0C0C, []byte{3}),
			}},
		},
	}
	spr := mustDecodeBytes(t, spec.encode())

	got := spr.UnknownChunks()
	if len(got) != 3 {
		t.Fatalf("got %d chunks, want 3", len(got))
	}
	if got[0].Layer != 0 {
		t.Errorf("chunk after layer A: Layer = %d, want 0", got[0].Layer)
	}
	if got[1].Layer != 1 {
		t.Errorf("chunk after layer B: Layer = %d, want 1", got[1].Layer)
	}
	if got[2].Frame != 1 || got[2].Layer != 0 {
		t.Errorf("frame 1 chunk: Frame/Layer = %d/%d, want 1/0", got[2].Frame, got[2].Layer)
	}
}

func TestUnknownChunkFrameAttribution(t *testing.T) {
	spec := testSpriteSpec{
		w: 2, h: 2, bpp: 32,
		frames: []testFrame{
			{dur: 100, chunks: []testChunk{
				testLayerChunk("L"),
				testCelChunkRaw(0, 0, 0, 2, 2, testOpaquePixels(2, 2, 32)),
				testUnknownChunk(0xAAAA, []byte("first")),
			}},
			{dur: 100, chunks: []testChunk{
				testCelChunkRaw(0, 0, 0, 2, 2, testOpaquePixels(2, 2, 32)),
				testUnknownChunk(0xBBBB, []byte("second")),
			}},
		},
	}
	spr := mustDecodeBytes(t, spec.encode())

	all := spr.UnknownChunks()
	if len(all) != 2 {
		t.Fatalf("got %d chunks, want 2", len(all))
	}
	if all[0].Frame != 0 || all[1].Frame != 1 {
		t.Errorf("frames = %d/%d, want 0/1", all[0].Frame, all[1].Frame)
	}
	if all[1].Layer != 0 {
		t.Errorf("frame 1 chunk layer = %d, want 0 (from preceding cel)", all[1].Layer)
	}
	if all[0].Offset >= all[1].Offset {
		t.Errorf("offsets not increasing: %d then %d", all[0].Offset, all[1].Offset)
	}

	if g := spr.UnknownChunksByFrame(1); len(g) != 1 || g[0].Type != 0xBBBB {
		t.Errorf("UnknownChunksByFrame(1) = %+v", g)
	}
	if g := spr.UnknownChunksByType(0xAAAA); len(g) != 1 || g[0].Frame != 0 {
		t.Errorf("UnknownChunksByType = %+v", g)
	}
	if g := spr.UnknownChunksByLayer(0); len(g) != 2 {
		t.Errorf("UnknownChunksByLayer(0) has %d chunks, want 2", len(g))
	}
	if g := spr.UnknownChunksByLayer(NoLayer); len(g) != 0 {
		t.Errorf("UnknownChunksByLayer(NoLayer) = %+v", g)
	}
}

func TestUnknownChunkRequiresContext(t *testing.T) {
	// Unknown chunk before any layer or cel in its frame.
	spec := testSpriteSpec{
		w: 2, h: 2, bpp: 32,
		frames: []testFrame{{dur: 100, chunks: []testChunk{
			testUnknownChunk(0xF00D, []byte{1}),
			testLayerChunk("L"),
			testCelChunkRaw(0, 0, 0, 2, 2, testOpaquePixels(2, 2, 32)),
		}}},
	}
	data := spec.encode()

	spr, err := ReadWithOptions(bytes.NewReader(data), Options{})
	if err != nil {
		t.Fatalf("lenient: %v", err)
	}
	if got := spr.UnknownChunks(); got[0].Layer != NoLayer {
		t.Errorf("Layer = %d, want NoLayer", got[0].Layer)
	}

	_, err = ReadWithOptions(bytes.NewReader(data), Options{RequireUnknownChunkContext: true})
	if !errors.Is(err, ErrUnknownChunkContext) {
		t.Fatalf("error = %v, want ErrUnknownChunkContext", err)
	}
}

func TestUnknownChunkOutsideDeclaredFrames(t *testing.T) {
	spec := testSpriteSpec{
		w: 2, h: 2, bpp: 32,
		frames: []testFrame{
			{dur: 100, chunks: []testChunk{
				testLayerChunk("L"),
				testCelChunkRaw(0, 0, 0, 2, 2, testOpaquePixels(2, 2, 32)),
			}},
			{dur: 100, chunks: []testChunk{
				testCelChunkRaw(0, 0, 0, 2, 2, testOpaquePixels(2, 2, 32)),
				testUnknownChunk(0xF00D, []byte{1}),
			}},
		},
	}
	data := spec.encode()
	// Patch the header frame count down to one: the second frame still
	// parses, but an audit record for it would name a nonexistent frame.
	binary.LittleEndian.PutUint16(data[6:], 1)

	_, err := Read(bytes.NewReader(data))
	if !errors.Is(err, ErrUnknownChunkFrame) {
		t.Fatalf("error = %v, want ErrUnknownChunkFrame", err)
	}
}

func TestUnknownChunkTruncated(t *testing.T) {
	spec := testBasicSprite(32, testUnknownChunk(0xF00D, []byte{1, 2, 3, 4}))
	data := spec.encode()

	// Cut the last two payload bytes and shrink both the declared file and
	// frame sizes so the decoder reaches the chunk and finds its declared
	// size larger than the bytes that remain.
	oldFrameLen := binary.LittleEndian.Uint32(data[testHeaderSize:])
	truncated := append([]byte(nil), data[:len(data)-2]...)
	binary.LittleEndian.PutUint32(truncated, uint32(len(truncated)))
	binary.LittleEndian.PutUint32(truncated[testHeaderSize:], oldFrameLen-2)

	_, err := Read(bytes.NewReader(truncated))
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("error = %v, want ErrTruncated", err)
	}
	var ce *ChunkError
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a *ChunkError", err)
	}
	if ce.Type != 0xF00D {
		t.Errorf("error type = %#04x, want 0xf00d", ce.Type)
	}
	if ce.Frame != 0 || ce.Chunk != 2 {
		t.Errorf("error context = frame %d chunk %d, want frame 0 chunk 2", ce.Frame, ce.Chunk)
	}

	// A header shorter than 128 bytes is also a truncation, not a panic.
	_, err = Read(bytes.NewReader(data[:64]))
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("short header error = %v, want ErrTruncated", err)
	}
}

func TestUnknownChunkSnapshotsAreIndependent(t *testing.T) {
	data := testBasicSprite(32, testUnknownChunk(0xF00D, []byte{1, 2, 3})).encode()

	spr := mustDecodeBytes(t, data)

	first := spr.UnknownChunks()
	first[0].Data[0] = 0xFF
	first[0].Type = 0xFFFF
	first[0] = UnknownChunk{}

	second := spr.UnknownChunks()
	if second[0].Type != 0xF00D {
		t.Fatalf("Type changed after mutating a previous snapshot: %#04x", second[0].Type)
	}
	if !bytes.Equal(second[0].Data, []byte{1, 2, 3}) {
		t.Fatalf("Data changed after mutating a previous snapshot: %x", second[0].Data)
	}

	// The same applies to the single-item and filtered helpers.
	one, ok := spr.UnknownChunkAt(0)
	if !ok {
		t.Fatal("UnknownChunkAt(0) missing")
	}
	one.Data[0] = 0xAA
	again, _ := spr.UnknownChunkAt(0)
	if !bytes.Equal(again.Data, []byte{1, 2, 3}) {
		t.Fatalf("UnknownChunkAt Data changed: %x", again.Data)
	}
	if _, ok := spr.UnknownChunkAt(1); ok {
		t.Fatal("UnknownChunkAt(1) should be out of range")
	}
	if _, ok := spr.UnknownChunkAt(-1); ok {
		t.Fatal("UnknownChunkAt(-1) should be out of range")
	}

	byType := spr.UnknownChunksByType(0xF00D)
	byType[0].Data[1] = 0xBB
	if got := spr.UnknownChunksByType(0xF00D); !bytes.Equal(got[0].Data, []byte{1, 2, 3}) {
		t.Fatalf("filtered snapshot aliased decoder state: %x", got[0].Data)
	}
}

func TestReDecodeDoesNotShareAuditState(t *testing.T) {
	data := testBasicSprite(32, testUnknownChunk(0xF00D, []byte{1, 2, 3})).encode()

	a, err := Read(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Read(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	// Mutating a snapshot only changes the snapshot itself; a fresh query
	// still sees the bytes recorded during decoding, and the other decode is
	// unaffected.
	aSnap := a.UnknownChunks()
	aSnap[0].Data[0] = 0x99
	if !bytes.Equal(aSnap[0].Data, []byte{0x99, 2, 3}) {
		t.Fatal("mutation of a snapshot did not apply to that snapshot")
	}
	if got := a.UnknownChunks()[0].Data[0]; got != 1 {
		t.Fatalf("a.UnknownChunks() changed to %#x after mutating a snapshot", got)
	}
	if got := b.UnknownChunks()[0].Data[0]; got != 1 {
		t.Fatalf("b.UnknownChunks() changed to %#x; decodes share storage", got)
	}
}

func TestDecodeWithOptionsAndConfig(t *testing.T) {
	payload := make([]byte, 64)
	data := testBasicSprite(32, testUnknownChunk(0xF00D, payload)).encode()

	img, err := DecodeWithOptions(bytes.NewReader(data), Options{MaxUnknownChunkSize: 8})
	if err == nil {
		t.Fatal("DecodeWithOptions ignored the size limit")
	}
	if !errors.Is(err, ErrUnknownChunkTooLarge) {
		t.Fatalf("error = %v", err)
	}
	_ = img

	// DecodeConfig validates the same audit rules, so config and decode do
	// not disagree about which files are readable.
	_, err = DecodeConfigWithOptions(bytes.NewReader(data), Options{MaxUnknownChunkSize: 8})
	if !errors.Is(err, ErrUnknownChunkTooLarge) {
		t.Fatalf("DecodeConfigWithOptions error = %v", err)
	}
}

func TestUnknownChunkRaw(t *testing.T) {
	var zero UnknownChunk
	if zero.Raw() != nil {
		t.Fatal("zero UnknownChunk.Raw() should be nil")
	}

	data := testBasicSprite(32, testUnknownChunk(0xF00D, []byte{1, 2})).encode()
	c := mustDecodeBytes(t, data).UnknownChunks()[0]
	raw := c.Raw()
	raw[0] = 0xFF
	if c.Data[0] != 1 {
		t.Fatal("UnknownChunk.Raw returned an alias")
	}
}

func TestCheckUnknownChunks(t *testing.T) {
	data := testBasicSprite(32,
		testUnknownChunk(0xAAAA, []byte{1, 2}),
		testUnknownChunk(0xBBBB, []byte{3}),
	).encode()

	got, err := CheckUnknownChunks(bytes.NewReader(data), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Type != 0xAAAA || got[1].Type != 0xBBBB {
		t.Fatalf("CheckUnknownChunks = %+v", got)
	}
	if !bytes.Equal(got[0].Data, []byte{1, 2}) {
		t.Fatalf("Data = %x", got[0].Data)
	}

	// The same limits apply.
	if _, err := CheckUnknownChunks(bytes.NewReader(data), Options{MaxUnknownChunks: 1}); !errors.Is(err, ErrTooManyUnknownChunks) {
		t.Fatalf("error = %v, want ErrTooManyUnknownChunks", err)
	}
}

func TestUnknownChunkAtlasLimit(t *testing.T) {
	// A 2x2 32-bit sprite needs a tiny atlas; a 4-byte limit must reject it
	// before the atlas is allocated, carrying frame-independent context.
	data := testBasicSprite(32).encode()
	_, err := ReadWithOptions(bytes.NewReader(data), Options{MaxAtlasSize: 4})
	if !errors.Is(err, ErrAtlasTooLarge) {
		t.Fatalf("error = %v, want ErrAtlasTooLarge", err)
	}
	var ce *ChunkError
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a *ChunkError", err)
	}

	// The default limit leaves a small sprite alone.
	if _, err := ReadWithOptions(bytes.NewReader(data), Options{}); err != nil {
		t.Fatalf("default atlas limit rejected a 2x2 sprite: %v", err)
	}

	// A negative limit disables the check.
	if _, err := ReadWithOptions(bytes.NewReader(data), Options{MaxAtlasSize: -1}); err != nil {
		t.Fatalf("disabled atlas limit rejected a small sprite: %v", err)
	}
}

func TestFileSizeLimit(t *testing.T) {
	data := testBasicSprite(32).encode()
	_, err := ReadWithOptions(bytes.NewReader(data), Options{MaxFileSize: int64(len(data) - 1)})
	if !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("error = %v, want ErrInvalidFormat", err)
	}
}

func TestOutOfRangePaletteIndexDoesNotPanic(t *testing.T) {
	// A 2x2 8-bit cel whose pixels reference index 255 while the header
	// declares only a 4-entry palette. The decoder must clamp rather than
	// panic inside image/draw.
	data := testSpriteSpec{
		w: 2, h: 2, bpp: 8,
		frames: []testFrame{{dur: 100, chunks: []testChunk{
			testLayerChunk("L"),
			testCelChunkRaw(0, 0, 0, 2, 2, []byte{0xFF, 0xFF, 0xFF, 0xFF}),
			testUnknownChunk(0xF00D, []byte{1}),
		}}},
	}.encode()
	binary.LittleEndian.PutUint16(data[32:], 4) // palette entry count

	spr, err := Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// Out-of-range indices map to the transparent index (0), so the cel is
	// fully transparent and the unknown chunk is still audited.
	if got := spr.UnknownChunks(); len(got) != 1 || got[0].Type != 0xF00D {
		t.Fatalf("audit = %+v", got)
	}
}

func TestTruncatedCelPayloadIsAnError(t *testing.T) {
	// Declare a 4x1 cel but supply only two pixels.
	cel := testCelChunkRaw(0, 0, 0, 4, 1, []byte{1, 2})
	data := testSpriteSpec{
		w: 4, h: 1, bpp: 8,
		frames: []testFrame{{dur: 100, chunks: []testChunk{
			testLayerChunk("L"),
			cel,
		}}},
	}.encode()

	_, err := Read(bytes.NewReader(data))
	if !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("error = %v, want ErrInvalidFormat", err)
	}
	var ce *ChunkError
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a *ChunkError", err)
	}
	if ce.Type != 0x2005 || ce.Chunk != 1 {
		t.Errorf("error context = type %#04x chunk %d", ce.Type, ce.Chunk)
	}
}

func TestTruncationNeverPanics(t *testing.T) {
	// Every prefix of a valid file with unknown chunks must fail cleanly
	// (or, for complete headers, succeed) without panicking.
	seed := testBasicSprite(32, testUnknownChunk(0xF00D, []byte{1, 2, 3, 4})).encode()
	for n := 0; n <= len(seed); n++ {
		n := n
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("prefix %d panicked: %v", n, r)
				}
			}()
			_, _ = Read(bytes.NewReader(seed[:n]))
		}()
	}
}

// TestExistingFixturesHaveNoUnknownChunks guards the default path: the
// repository fixtures only use chunk types this package understands, so the
// audit must stay silent for them and the original decoder behaviour is
// unchanged.
func TestExistingFixturesHaveNoUnknownChunks(t *testing.T) {
	for _, name := range []string{
		"testfiles/slime_paletted.aseprite",
		"testfiles/slime_grayscale.aseprite",
		"testfiles/blendtest.aseprite",
	} {
		name := name
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(name)
			if err != nil {
				t.Skipf("fixture not available: %v", err)
			}
			defer f.Close()

			spr, err := Read(f)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if n := spr.NumUnknownChunks(); n != 0 {
				t.Fatalf("%d unknown chunks in %s", n, name)
			}
		})
	}
}

// TestUnknownChunksEqualAcrossFrames checks the ordering contract: snapshots
// are returned in file order, which is also increasing offset order.
func TestUnknownChunksEqualAcrossFrames(t *testing.T) {
	spec := testSpriteSpec{
		w: 2, h: 2, bpp: 8,
		frames: []testFrame{
			{dur: 100, chunks: []testChunk{
				testLayerChunk("L"),
				testCelChunkRaw(0, 0, 0, 2, 2, testOpaquePixels(2, 2, 8)),
				testUnknownChunk(0x1001, []byte{1}),
				testUnknownChunk(0x1002, []byte{2}),
			}},
			{dur: 100, chunks: []testChunk{
				testCelChunkRaw(0, 0, 0, 2, 2, testOpaquePixels(2, 2, 8)),
				testUnknownChunk(0x1001, []byte{3}),
			}},
		},
	}
	spr := mustDecodeBytes(t, spec.encode())
	got := spr.UnknownChunks()
	if len(got) != 3 {
		t.Fatalf("got %d chunks", len(got))
	}
	want := []struct {
		typ   uint16
		frame int
	}{{0x1001, 0}, {0x1002, 0}, {0x1001, 1}}
	for i, w := range want {
		if got[i].Type != w.typ || got[i].Frame != w.frame {
			t.Errorf("chunk %d = (%#04x, frame %d), want (%#04x, frame %d)", i, got[i].Type, got[i].Frame, w.typ, w.frame)
		}
	}
	if !reflect.DeepEqual(spr.UnknownChunksByFrame(0), got[:2]) {
		t.Error("UnknownChunksByFrame(0) does not match the first two snapshots")
	}
}
