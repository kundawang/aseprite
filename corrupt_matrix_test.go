package aseprite

import (
	"bytes"
	"errors"
	"image"
	"testing"

	"github.com/askeladdk/aseprite/internal/asetest"
)

// expect describes the outcome a corrupted fixture must produce.
//
// Every corrupt input must fail with a non-nil error whose category (via
// errors.Is) is Kind. When a frame, layer or chunk index is specified, the
// error must be a *ParseError carrying that context. Tests additionally run
// under a recover guard so a panic fails the case instead of the process.
type expect struct {
	Kind  error
	Frame int // -1 to skip the check
	Layer int // -1 to skip the check
	Chunk int // -1 to skip the check
	// DecodeConfigToo requires DecodeConfig to fail as well.
	DecodeConfigToo bool
}

// matrixCase is one row of the Type/length matrix.
type matrixCase struct {
	Name  string
	Bytes func() []byte
	Want  expect
}

// valid encodes a sprite and panics if it is not valid, so that a corruption
// case can never silently start from an already-broken fixture.
func valid(t *testing.T, spr *asetest.Sprite) []byte {
	t.Helper()
	data := spr.Bytes()
	if _, err := decodeAll(data); err != nil {
		t.Fatalf("base fixture is not valid: %v", err)
	}
	return data
}

// decodeAll runs both entry points. ReadFrom is the primary decoder; Decode is
// exercised so that the image.Image path cannot drift from it.
func decodeAll(data []byte) (*Aseprite, error) {
	var spr Aseprite
	if _, err := spr.ReadFrom(bytes.NewReader(data)); err != nil {
		return nil, err
	}
	if _, err := Decode(bytes.NewReader(data)); err != nil {
		return nil, err
	}
	return &spr, nil
}

// oneFrame returns a valid 8-bit sprite whose single frame contains exactly
// the given chunks, prefixed by a palette until one is supplied.
func oneFrame(extra ...asetest.Chunk) *asetest.Sprite {
	spr := minimalPaletteSprite()
	spr.Frames[0].Chunks = append(spr.Frames[0].Chunks[:1], extra...)
	return spr
}

func minimalPaletteSprite() *asetest.Sprite {
	spr := validBase(8)
	return spr
}

// validBase builds a valid sprite at the given depth without decoding it.
func validBase(depth uint16) *asetest.Sprite {
	return minimalSprite(depth)
}

// matrix returns the full set of corrupted fixtures. Fixtures are grouped by
// the format layer they attack: header, frame, chunk framing, palette, linked
// cel, zlib cel, user data, tag and slice.
func matrix() []matrixCase {
	base := func() []byte { return minimalSprite(8).Bytes() }

	var cases []matrixCase
	add := func(name string, f func() []byte, w expect) {
		cases = append(cases, matrixCase{Name: name, Bytes: f, Want: w})
	}

	// ---- header -----------------------------------------------------------
	add("header/truncated_127", func() []byte { return base()[:127] },
		expect{Kind: ErrTruncated, Frame: -1, Layer: -1, Chunk: -1, DecodeConfigToo: true})
	add("header/empty", func() []byte { return nil },
		expect{Kind: ErrTruncated, Frame: -1, Layer: -1, Chunk: -1, DecodeConfigToo: true})
	add("header/bad_magic", func() []byte {
		d := base()
		d[4], d[5] = 0x00, 0x00
		return d
	}, expect{Kind: ErrMalformed, Frame: -1, Layer: -1, Chunk: -1, DecodeConfigToo: true})
	add("header/bad_depth", func() []byte {
		d := base()
		d[12], d[13] = 7, 0
		return d
	}, expect{Kind: ErrUnsupported, Frame: -1, Layer: -1, Chunk: -1, DecodeConfigToo: true})
	add("header/bad_pixel_ratio", func() []byte {
		d := base()
		d[34], d[35] = 2, 1
		return d
	}, expect{Kind: ErrUnsupported, Frame: -1, Layer: -1, Chunk: -1, DecodeConfigToo: true})
	add("header/zero_frames", func() []byte {
		d := base()
		d[6], d[7] = 0, 0
		return d
	}, expect{Kind: ErrMalformed, Frame: -1, Layer: -1, Chunk: -1, DecodeConfigToo: true})
	add("header/zero_width", func() []byte {
		d := base()
		d[8], d[9] = 0, 0
		return d
	}, expect{Kind: ErrMalformed, Frame: -1, Layer: -1, Chunk: -1, DecodeConfigToo: true})
	add("header/size_below_header", func() []byte {
		d := base()
		d[0], d[1], d[2], d[3] = 64, 0, 0, 0
		return d
	}, expect{Kind: ErrMalformed, Frame: -1, Layer: -1, Chunk: -1, DecodeConfigToo: true})
	add("header/size_over_limit", func() []byte {
		d := base()
		big := uint32(maxFileSize + 1)
		d[0], d[1], d[2], d[3] = byte(big), byte(big>>8), byte(big>>16), byte(big>>24)
		return d
	}, expect{Kind: ErrUnsupported, Frame: -1, Layer: -1, Chunk: -1, DecodeConfigToo: true})
	add("header/size_understates_body", func() []byte {
		d := base()
		d[0] = byte(len(d) - 1)
		d[1] = byte((len(d) - 1) >> 8)
		return d
	}, expect{Kind: ErrTruncated, Frame: 0, Layer: -1, Chunk: -1, DecodeConfigToo: true})
	add("header/size_overstates_body", func() []byte {
		d := base()
		n := len(d) + 10
		d[0], d[1], d[2], d[3] = byte(n), byte(n>>8), byte(n>>16), byte(n>>24)
		return d
	}, expect{Kind: ErrTruncated, Frame: -1, Layer: -1, Chunk: -1, DecodeConfigToo: true})

	// ---- frame ------------------------------------------------------------
	add("frame/truncated_header", func() []byte {
		// The header declares two frames but the first frame ends exactly at
		// EOF, so the second frame's 16-byte header is never present.
		spr := minimalSprite(8)
		spr.Frames = append(spr.Frames, asetest.Frame{Duration: 100})
		d := spr.Bytes()
		first := putU32leRead(d[128:])
		d = d[:128+first]
		putU32le(d, uint32(len(d)))
		return d
	}, expect{Kind: ErrTruncated, Frame: 1, Layer: -1, Chunk: -1, DecodeConfigToo: true})
	add("frame/truncated_length", func() []byte {
		// Keep the header's file size but cut bytes from the end of the body,
		// so the frame's declared length can no longer be satisfied.
		spr := minimalSprite(8)
		d := spr.Bytes()
		frameLen := putU32leRead(d[128:])
		putU32le(d[128:], frameLen+4)
		return d
	}, expect{Kind: ErrTruncated, Frame: 0, Layer: -1, Chunk: -1, DecodeConfigToo: true})
	add("frame/bad_magic", func() []byte {
		d := base()
		d[128+4], d[128+5] = 0xFA, 0xF2
		return d
	}, expect{Kind: ErrMalformed, Frame: 0, Layer: -1, Chunk: -1, DecodeConfigToo: true})
	add("frame/length_below_header", func() []byte {
		d := base()
		putU32le(d[128:], 12)
		return d
	}, expect{Kind: ErrMalformed, Frame: 0, Layer: -1, Chunk: -1, DecodeConfigToo: true})
	add("frame/length_past_eof", func() []byte {
		d := base()
		putU32le(d[128:], uint32(len(d)))
		return d
	}, expect{Kind: ErrTruncated, Frame: 0, Layer: -1, Chunk: -1, DecodeConfigToo: true})
	add("frame/chunk_count_too_large", func() []byte {
		d := base()
		putU32le(d[128+12:], 0x00FFFFFF)
		return d
	}, expect{Kind: ErrMalformed, Frame: 0, Layer: -1, Chunk: -1, DecodeConfigToo: true})
	add("frame/trailing_bytes", func() []byte {
		// One extra byte after the last frame, with the file size grown to
		// match so the body is read in full and the leftover is detected.
		d := append(base(), 0x00)
		putU32le(d, uint32(len(d)))
		return d
	}, expect{Kind: ErrMalformed, Frame: 1, Layer: -1, Chunk: -1, DecodeConfigToo: true})
	add("frame/second_frame_truncated", func() []byte {
		spr := minimalSprite(8)
		second := spr.Frames[0]
		second.Chunks = nil
		spr.Frames = append(spr.Frames, second)
		d := spr.Bytes()
		// Cut inside the second frame's header.
		return d[:len(d)-20]
	}, expect{Kind: ErrTruncated, Frame: -1, Layer: -1, Chunk: -1, DecodeConfigToo: true})

	// ---- chunk framing ----------------------------------------------------
	add("chunk/length_below_min", func() []byte {
		// Palette chunk is chunk 0 in the base fixture.
		d := base()
		putU32le(d[128+16:], 5)
		return d
	}, expect{Kind: ErrMalformed, Frame: 0, Layer: -1, Chunk: 0, DecodeConfigToo: true})
	add("chunk/length_past_frame", func() []byte {
		d := base()
		putU32le(d[128+16:], 0x7FFFFFFF)
		return d
	}, expect{Kind: ErrTruncated, Frame: 0, Layer: -1, Chunk: 0, DecodeConfigToo: true})
	add("chunk/length_overruns_frame", func() []byte {
		// The chunk fits inside the file but not inside its frame: the frame
		// length is one byte shorter than the chunk header claims.
		spr := minimalSprite(8)
		d := spr.Bytes()
		// Keep the file size honest so every chunk is fully present in the
		// buffer; shrink only the frame length. The decoder walks chunks until
		// the final 42-byte cel, which then overruns the one-byte-shorter
		// frame and must be reported rather than read into the next frame.
		frameLen := putU32leRead(d[128:])
		putU32le(d[128:], frameLen-1)
		return d
	}, expect{Kind: ErrMalformed, Frame: 0, Layer: -1, Chunk: 2, DecodeConfigToo: true})
	add("chunk/count_overruns_frame", func() []byte {
		// Two chunks declared; the bytes of the second lie past the frame's
		// declared length and must not be read.
		spr := minimalSprite(8)
		spr.Frames[0].ForceChunks = true
		spr.Frames[0].DeclaredChunks = 2
		return spr.Bytes()
	}, expect{Kind: ErrMalformed, Frame: 0, Layer: -1, Chunk: -1, DecodeConfigToo: true})
	add("chunk/truncated_body", func() []byte {
		// Truncate the layer chunk body itself (it is chunk 1).
		spr := minimalSprite(8)
		spr.Frames[0].Chunks[1].Body = spr.Frames[0].Chunks[1].Body[:4]
		return spr.Bytes()
	}, expect{Kind: ErrMalformed, Frame: 0, Layer: -1, Chunk: 1})

	// ---- palette ----------------------------------------------------------
	add("palette/entries_past_palette", func() []byte {
		spr := minimalSprite(8)
		pal := [][4]byte{{1, 2, 3, 255}, {4, 5, 6, 255}}
		spr.Frames[0].Chunks[0] = asetest.ChunkOf(asetest.TypePalette, asetest.PaletteBody(7, pal, nil))
		return spr.Bytes()
	}, expect{Kind: ErrMalformed, Frame: 0, Layer: -1, Chunk: 0})
	add("palette/truncated_header", func() []byte {
		spr := minimalSprite(8)
		spr.Frames[0].Chunks[0].Body = spr.Frames[0].Chunks[0].Body[:12]
		return spr.Bytes()
	}, expect{Kind: ErrTruncated, Frame: 0, Layer: -1, Chunk: 0})
	add("palette/truncated_entry", func() []byte {
		spr := minimalSprite(8)
		b := spr.Frames[0].Chunks[0].Body
		spr.Frames[0].Chunks[0].Body = b[:len(b)-2]
		return spr.Bytes()
	}, expect{Kind: ErrTruncated, Frame: 0, Layer: -1, Chunk: 0})
	add("palette/truncated_name", func() []byte {
		spr := minimalSprite(8)
		pal := [][4]byte{{1, 2, 3, 255}}
		body := asetest.PaletteBody(0, pal, []string{"named"})
		// Drop the name bytes but keep the length prefix and header.
		spr.Frames[0].Chunks[0].Body = body[:20+6+2]
		return spr.Bytes()
	}, expect{Kind: ErrTruncated, Frame: 0, Layer: -1, Chunk: 0})

	// ---- linked cel -------------------------------------------------------
	linkedBase := func() *asetest.Sprite {
		spr := minimalSprite(8)
		spr.Frames = append(spr.Frames, asetest.Frame{
			Duration: 100,
			Chunks:   []asetest.Chunk{asetest.ChunkOf(asetest.TypeCel, asetest.LinkedCelBody(0, 0, 0, 255, 0))},
		})
		return spr
	}
	add("linked/valid_source", func() []byte { return linkedBase().Bytes() },
		expect{Kind: nil, Frame: -1, Layer: -1, Chunk: -1})
	add("linked/frame_out_of_range", func() []byte {
		spr := linkedBase()
		spr.Frames[1].Chunks[0] = asetest.ChunkOf(asetest.TypeCel, asetest.LinkedCelBody(0, 0, 0, 255, 99))
		return spr.Bytes()
	}, expect{Kind: ErrMalformed, Frame: 1, Layer: 0, Chunk: 0})
	add("linked/truncated", func() []byte {
		spr := linkedBase()
		spr.Frames[1].Chunks[0].Body = []byte{0, 0, 0, 0, 0, 0, 255, 1, 0, 0, 0, 0, 0}
		return spr.Bytes()
	}, expect{Kind: ErrTruncated, Frame: 1, Layer: -1, Chunk: 0})

	// ---- zlib cel ---------------------------------------------------------
	add("zlib/corrupt_stream", func() []byte {
		spr := minimalSprite(8)
		body := spr.Frames[0].Chunks[2].Body
		body[len(body)-3] ^= 0xFF
		return spr.Bytes()
	}, expect{Kind: ErrMalformed, Frame: 0, Layer: 0, Chunk: 2})
	add("zlib/truncated_stream", func() []byte {
		spr := minimalSprite(8)
		body := spr.Frames[0].Chunks[2].Body
		spr.Frames[0].Chunks[2].Body = body[:len(body)-5]
		return spr.Bytes()
	}, expect{Kind: ErrMalformed, Frame: 0, Layer: 0, Chunk: 2})
	add("zlib/short_output", func() []byte {
		spr := minimalSprite(8)
		pix := asetest.SolidPix(3, 8) // 4 pixels declared below
		spr.Frames[0].Chunks[2] = asetest.ChunkOf(asetest.TypeCel,
			asetest.CelBody(0, 0, 0, 255, asetest.CelZlib, 2, 2, pix))
		return spr.Bytes()
	}, expect{Kind: ErrMalformed, Frame: 0, Layer: 0, Chunk: 2})
	add("zlib/long_output", func() []byte {
		spr := minimalSprite(8)
		pix := asetest.SolidPix(9, 8) // 4 pixels declared below
		spr.Frames[0].Chunks[2] = asetest.ChunkOf(asetest.TypeCel,
			asetest.CelBody(0, 0, 0, 255, asetest.CelZlib, 2, 2, pix))
		return spr.Bytes()
	}, expect{Kind: ErrMalformed, Frame: 0, Layer: 0, Chunk: 2})
	add("zlib/not_zlib", func() []byte {
		spr := minimalSprite(8)
		body := spr.Frames[0].Chunks[2].Body
		for i := 20; i < len(body); i++ {
			body[i] = 0
		}
		return spr.Bytes()
	}, expect{Kind: ErrMalformed, Frame: 0, Layer: 0, Chunk: 2})

	// ---- raw cel ----------------------------------------------------------
	add("cel/raw_truncated", func() []byte {
		spr := minimalSprite(8)
		spr.Frames[0].Chunks[2] = asetest.ChunkOf(asetest.TypeCel,
			asetest.CelBody(0, 0, 0, 255, asetest.CelRaw, 2, 2, []byte{1, 2, 3}))
		return spr.Bytes()
	}, expect{Kind: ErrTruncated, Frame: 0, Layer: 0, Chunk: 2})
	add("cel/raw_valid", func() []byte {
		spr := minimalSprite(8)
		spr.Frames[0].Chunks[2] = asetest.ChunkOf(asetest.TypeCel,
			asetest.CelBody(0, 0, 0, 255, asetest.CelRaw, 2, 2, asetest.SolidPix(4, 8)))
		return spr.Bytes()
	}, expect{Kind: nil, Frame: -1, Layer: -1, Chunk: -1})
	add("cel/unknown_type", func() []byte {
		spr := minimalSprite(8)
		spr.Frames[0].Chunks[2] = asetest.ChunkOf(asetest.TypeCel,
			asetest.CelBody(0, 0, 0, 255, 9, 2, 2, asetest.SolidPix(4, 8)))
		return spr.Bytes()
	}, expect{Kind: ErrUnsupported, Frame: 0, Layer: 0, Chunk: 2})
	add("cel/layer_out_of_range", func() []byte {
		spr := minimalSprite(8)
		spr.Frames[0].Chunks[2] = asetest.ChunkOf(asetest.TypeCel,
			asetest.CelBody(5, 0, 0, 255, asetest.CelZlib, 2, 2, asetest.SolidPix(4, 8)))
		return spr.Bytes()
	}, expect{Kind: ErrMalformed, Frame: 0, Layer: 5, Chunk: 2})
	add("cel/truncated_header", func() []byte {
		spr := minimalSprite(8)
		spr.Frames[0].Chunks[2].Body = spr.Frames[0].Chunks[2].Body[:9]
		return spr.Bytes()
	}, expect{Kind: ErrTruncated, Frame: 0, Layer: -1, Chunk: 2})
	add("cel/pixel_size_over_limit", func() []byte {
		spr := minimalSprite(32)
		// 60000x60000 RGBA is far past the cel limit; the pixel buffer itself
		// stays tiny because the limit is checked from the dimensions.
		spr.Frames[0].Chunks[1] = asetest.ChunkOf(asetest.TypeCel,
			asetest.CelBody(0, 0, 0, 255, asetest.CelRaw, 60000, 60000, nil))
		return spr.Bytes()
	}, expect{Kind: ErrUnsupported, Frame: 0, Layer: 0, Chunk: 1})

	// ---- layers -----------------------------------------------------------
	add("layer/truncated", func() []byte {
		spr := minimalSprite(8)
		spr.Frames[0].Chunks[1].Body = spr.Frames[0].Chunks[1].Body[:8]
		return spr.Bytes()
	}, expect{Kind: ErrMalformed, Frame: 0, Layer: -1, Chunk: 1})
	add("layer/tilemap_unsupported", func() []byte {
		spr := minimalSprite(8)
		spr.Frames[0].Chunks[1] = asetest.ChunkOf(asetest.TypeLayer,
			asetest.LayerBody(asetest.LayerVisible, 2, 0, 255, "tiles"))
		return spr.Bytes()
	}, expect{Kind: ErrUnsupported, Frame: 0, Layer: -1, Chunk: 1})

	// ---- user data --------------------------------------------------------
	add("userdata/truncated_flags", func() []byte {
		spr := minimalSprite(8)
		spr.Frames[0].Chunks = append(spr.Frames[0].Chunks[:2],
			append([]asetest.Chunk{asetest.ChunkOf(asetest.TypeUserData, []byte{1, 0})},
				spr.Frames[0].Chunks[2:]...)...)
		return spr.Bytes()
	}, expect{Kind: ErrTruncated, Frame: 0, Layer: -1, Chunk: 2})
	add("userdata/truncated_text", func() []byte {
		spr := minimalSprite(8)
		ud := asetest.UserDataBody([]byte("hello"), nil)
		spr.Frames[0].Chunks = append(spr.Frames[0].Chunks[:2],
			append([]asetest.Chunk{asetest.ChunkOf(asetest.TypeUserData, ud[:7])},
				spr.Frames[0].Chunks[2:]...)...)
		return spr.Bytes()
	}, expect{Kind: ErrTruncated, Frame: 0, Layer: -1, Chunk: 2})
	add("userdata/truncated_color", func() []byte {
		spr := minimalSprite(8)
		col := [4]byte{9, 8, 7, 6}
		ud := asetest.UserDataBody(nil, &col)
		spr.Frames[0].Chunks = append(spr.Frames[0].Chunks[:2],
			append([]asetest.Chunk{asetest.ChunkOf(asetest.TypeUserData, ud[:6])},
				spr.Frames[0].Chunks[2:]...)...)
		return spr.Bytes()
	}, expect{Kind: ErrTruncated, Frame: 0, Layer: -1, Chunk: 2})
	add("userdata/valid_layer_text", func() []byte {
		spr := minimalSprite(8)
		ud := asetest.UserDataBody([]byte("layer note"), nil)
		spr.Frames[0].Chunks = append(spr.Frames[0].Chunks[:2],
			append([]asetest.Chunk{asetest.ChunkOf(asetest.TypeUserData, ud)},
				spr.Frames[0].Chunks[2:]...)...)
		return spr.Bytes()
	}, expect{Kind: nil, Frame: -1, Layer: -1, Chunk: -1})

	// ---- tag --------------------------------------------------------------
	add("tag/valid", func() []byte { return withTags(minimalSprite(8)).Bytes() },
		expect{Kind: nil, Frame: -1, Layer: -1, Chunk: -1})
	add("tag/truncated_header", func() []byte {
		spr := minimalSprite(8)
		spr.Frames[0].Chunks = append(spr.Frames[0].Chunks,
			asetest.ChunkOf(asetest.TypeTag, []byte{1, 0, 0, 0}))
		return spr.Bytes()
	}, expect{Kind: ErrTruncated, Frame: 0, Layer: -1, Chunk: -1})
	add("tag/truncated_name", func() []byte {
		spr := minimalSprite(8)
		body := asetest.TagsBody([]asetest.Tag{{Lo: 0, Hi: 0, Name: "walk"}})
		spr.Frames[0].Chunks = append(spr.Frames[0].Chunks,
			asetest.ChunkOf(asetest.TypeTag, body[:len(body)-2]))
		return spr.Bytes()
	}, expect{Kind: ErrTruncated, Frame: 0, Layer: -1, Chunk: -1})
	add("tag/count_exceeds_data", func() []byte {
		spr := minimalSprite(8)
		body := asetest.TagsBody([]asetest.Tag{{Lo: 0, Hi: 0, Name: "walk"}})
		putU16le(body, 5)
		spr.Frames[0].Chunks = append(spr.Frames[0].Chunks,
			asetest.ChunkOf(asetest.TypeTag, body))
		return spr.Bytes()
	}, expect{Kind: ErrTruncated, Frame: 0, Layer: -1, Chunk: -1})

	// ---- slice ------------------------------------------------------------
	add("slice/valid", func() []byte { return withSlice(minimalSprite(8), 3).Bytes() },
		expect{Kind: nil, Frame: -1, Layer: -1, Chunk: -1})
	add("slice/truncated_header", func() []byte {
		spr := minimalSprite(8)
		spr.Frames[0].Chunks = append(spr.Frames[0].Chunks,
			asetest.ChunkOf(asetest.TypeSlice, []byte{1, 0, 0, 0, 1, 0}))
		return spr.Bytes()
	}, expect{Kind: ErrTruncated, Frame: 0, Layer: -1, Chunk: -1})
	add("slice/truncated_bounds", func() []byte {
		spr := minimalSprite(8)
		body := asetest.SliceBody(0, "s", []asetest.SliceKeys{{W: 4, H: 4}})
		spr.Frames[0].Chunks = append(spr.Frames[0].Chunks,
			asetest.ChunkOf(asetest.TypeSlice, body[:len(body)-6]))
		return spr.Bytes()
	}, expect{Kind: ErrTruncated, Frame: 0, Layer: -1, Chunk: -1})
	add("slice/truncated_center", func() []byte {
		spr := minimalSprite(8)
		body := asetest.SliceBody(1, "s", []asetest.SliceKeys{{W: 4, H: 4}})
		spr.Frames[0].Chunks = append(spr.Frames[0].Chunks,
			asetest.ChunkOf(asetest.TypeSlice, body[:len(body)-9]))
		return spr.Bytes()
	}, expect{Kind: ErrTruncated, Frame: 0, Layer: -1, Chunk: -1})
	add("slice/count_exceeds_data", func() []byte {
		spr := minimalSprite(8)
		body := asetest.SliceBody(0, "s", []asetest.SliceKeys{{W: 4, H: 4}})
		putU32le(body, 1000)
		spr.Frames[0].Chunks = append(spr.Frames[0].Chunks,
			asetest.ChunkOf(asetest.TypeSlice, body))
		return spr.Bytes()
	}, expect{Kind: ErrTruncated, Frame: 0, Layer: -1, Chunk: -1})

	return cases
}

func withTags(spr *asetest.Sprite) *asetest.Sprite {
	spr.Frames[0].Chunks = append(spr.Frames[0].Chunks,
		asetest.ChunkOf(asetest.TypeTag, asetest.TagsBody([]asetest.Tag{
			{Lo: 0, Hi: 0, Direction: 1, Name: "idle"},
		})))
	return spr
}

func withSlice(spr *asetest.Sprite, flags uint32) *asetest.Sprite {
	spr.Frames[0].Chunks = append(spr.Frames[0].Chunks,
		asetest.ChunkOf(asetest.TypeSlice, asetest.SliceBody(flags, "s",
			[]asetest.SliceKeys{{W: 4, H: 4, CX: 1, CY: 1, CW: 2, CH: 2, PX: 2, PY: 2}})))
	return spr
}

func putU16le(dst []byte, v uint16) {
	dst[0], dst[1] = byte(v), byte(v>>8)
}

func putU32le(dst []byte, v uint32) {
	dst[0], dst[1], dst[2], dst[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

func putU32leRead(dst []byte) uint32 {
	return uint32(dst[0]) | uint32(dst[1])<<8 | uint32(dst[2])<<16 | uint32(dst[3])<<24
}

// TestCorruptMatrix runs every corrupted fixture through the decoder and
// checks the stable category, the frame/layer/chunk context and the absence
// of a panic. Valid control rows assert the matrix can tell good input from
// bad.
func TestCorruptMatrix(t *testing.T) {
	for _, tc := range matrix() {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			data := tc.Bytes()
			report := func(format string, args ...any) {
				t.Helper()
				t.Fatalf(format, args...)
			}

			spr, readErr, panicked := safeDecode(data)
			if panicked != nil {
				report("panic: %v", panicked)
			}

			if tc.Want.Kind == nil {
				if readErr != nil {
					report("expected success, got %v", readErr)
				}
				if spr == nil {
					report("expected sprite, got nil")
				}
				return
			}

			if readErr == nil {
				report("expected %v, got success", tc.Want.Kind)
			}
			if !errors.Is(readErr, tc.Want.Kind) {
				report("error %q is not category %v", readErr, tc.Want.Kind)
			}

			var pe *ParseError
			if !errors.As(readErr, &pe) {
				report("error %q is not a *ParseError", readErr)
			} else {
				if tc.Want.Frame >= 0 && pe.Frame != tc.Want.Frame {
					report("frame context = %d, want %d (%v)", pe.Frame, tc.Want.Frame, pe)
				}
				if tc.Want.Layer >= 0 && pe.Layer != tc.Want.Layer {
					report("layer context = %d, want %d (%v)", pe.Layer, tc.Want.Layer, pe)
				}
				if tc.Want.Chunk >= 0 && pe.Chunk != tc.Want.Chunk {
					report("chunk context = %d, want %d (%v)", pe.Chunk, tc.Want.Chunk, pe)
				}
			}

			if tc.Want.DecodeConfigToo {
				_, cfgErr, cfgPanic := safeConfig(data)
				if cfgPanic != nil {
					report("DecodeConfig panic: %v", cfgPanic)
				}
				if cfgErr == nil {
					report("DecodeConfig succeeded on corrupt input")
				}
				if !errors.Is(cfgErr, tc.Want.Kind) {
					report("DecodeConfig error %q is not category %v", cfgErr, tc.Want.Kind)
				}
			}
		})
	}
}

// safeDecode runs ReadFrom under a recover guard.
func safeDecode(data []byte) (spr *Aseprite, err error, panicked any) {
	defer func() {
		if r := recover(); r != nil {
			panicked = r
		}
	}()
	var s Aseprite
	_, err = s.ReadFrom(bytes.NewReader(data))
	if err != nil {
		return nil, err, nil
	}
	return &s, nil, nil
}

// safeConfig runs DecodeConfig under a recover guard.
func safeConfig(data []byte) (cfg image.Config, err error, panicked any) {
	defer func() {
		if r := recover(); r != nil {
			panicked = r
		}
	}()
	cfg, err = DecodeConfig(bytes.NewReader(data))
	return cfg, err, nil
}

// TestCorruptMatrixCategories verifies that every bad-input row produces one
// of the three documented categories and gives the category a name, so the
// matrix's coverage of the taxonomy is visible in one place.
func TestCorruptMatrixCategories(t *testing.T) {
	counts := map[error]int{}
	names := map[error]string{
		ErrTruncated:   "truncated",
		ErrMalformed:   "malformed",
		ErrUnsupported: "unsupported",
	}
	for _, tc := range matrix() {
		if tc.Want.Kind == nil {
			continue
		}
		if _, ok := names[tc.Want.Kind]; !ok {
			t.Fatalf("%s: unknown category %v", tc.Name, tc.Want.Kind)
		}
		counts[tc.Want.Kind]++
	}
	for _, kind := range []error{ErrTruncated, ErrMalformed, ErrUnsupported} {
		if counts[kind] == 0 {
			t.Fatalf("matrix has no %s case", names[kind])
		}
		t.Logf("%s: %d cases", names[kind], counts[kind])
	}
}
