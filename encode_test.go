package aseprite

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"reflect"
	"testing"
	"time"

	"github.com/askeladdk/aseprite/internal/require"
)

// testPalette mirrors the palette of the slime_paletted test file.
var testPalette = color.Palette{
	color.Transparent,               // 0: transparent
	color.NRGBA{247, 231, 198, 255}, // 1
	color.NRGBA{214, 142, 73, 255},  // 2
	color.NRGBA{166, 55, 37, 255},   // 3
}

const (
	testW = 8
	testH = 6
)

func palettedTestImage(w, h int, fill func(x, y int) uint8) *image.Paletted {
	img := image.NewPaletted(image.Rect(0, 0, w, h), testPalette)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Pix[y*img.Stride+x] = fill(x, y)
		}
	}
	return img
}

func gray16TestImage(w, h int) *image.Gray16 {
	img := image.NewGray16(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// Low byte = value, high byte = alpha (the inverse of the
			// reader's V|A<<8 reconstruction).
			v := uint16((x*17 + y*13) & 0xff)
			a := uint16(255)
			if (x+y)%5 == 0 {
				a = 0
			}
			binary.BigEndian.PutUint16(img.Pix[(y*w+x)*2:], v|a<<8)
		}
	}
	return img
}

func rgbaTestImage(w, h int, seed byte) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			img.Pix[i+0] = seed + byte(x)
			img.Pix[i+1] = seed + byte(y)
			img.Pix[i+2] = seed + byte(x*y)
			img.Pix[i+3] = 255 - byte(x%3)
		}
	}
	return img
}

// buildTestSprite constructs a sprite exercising every supported feature:
// two layers, three frames (one linked cel), frame durations, layer and cel
// user data, two tags (one with data), two slices (one with center, pivot,
// data and color) and a transparent palette index.
func buildTestSprite() *Sprite {
	base0 := palettedTestImage(testW, testH, func(x, y int) uint8 {
		if x == 0 || y == 0 {
			return 0 // transparent border
		}
		return uint8(1 + (x+y)%3)
	})
	top0 := palettedTestImage(4, 3, func(x, y int) uint8 { return uint8(1 + (x*2+y)%3) })
	base1 := palettedTestImage(testW, testH, func(x, y int) uint8 {
		if x == testW-1 {
			return 0
		}
		return uint8(1 + (x*3+y)%3)
	})
	base2 := palettedTestImage(testW, testH, func(x, y int) uint8 {
		return uint8(1 + (x+y*2)%3)
	})

	return &Sprite{
		Width:            testW,
		Height:           testH,
		BPP:              Indexed8,
		Palette:          testPalette,
		TransparentIndex: 0,
		Layers: []SpriteLayer{
			{Name: "base", Visible: true, Opacity: 255, BlendMode: 0, Data: []byte("layer base data")},
			{Name: "top", Visible: true, Opacity: 200, BlendMode: 1, Data: []byte("layer top data")},
		},
		Frames: []SpriteFrame{
			{
				Duration: 100 * time.Millisecond,
				Cels: []SpriteCel{
					{Layer: 0, LinkedFrame: -1, Bounds: image.Rect(0, 0, testW, testH), Opacity: 255, Image: base0, Data: []byte("cel f0 l0")},
					{Layer: 1, LinkedFrame: -1, Bounds: image.Rect(2, 1, 6, 4), Opacity: 255, Image: top0, Data: []byte("cel f0 l1")},
				},
			},
			{
				Duration: 120 * time.Millisecond,
				Cels: []SpriteCel{
					{Layer: 0, LinkedFrame: -1, Bounds: image.Rect(0, 0, testW, testH), Opacity: 255, Image: base1, Data: []byte("cel f1 l0")},
					{Layer: 1, LinkedFrame: 0, Bounds: image.Rect(2, 1, 6, 4), Opacity: 255},
				},
			},
			{
				Duration: 140 * time.Millisecond,
				Cels: []SpriteCel{
					{Layer: 0, LinkedFrame: -1, Bounds: image.Rect(0, 0, testW, testH), Opacity: 255, Image: base2, Data: []byte("cel f2 l0")},
				},
			},
		},
		Tags: []SpriteTag{
			{Name: "Up", Lo: 0, Hi: 1, Repeat: 3, LoopDirection: Forward, Data: []byte("tag up data")},
			{Name: "Down", Lo: 1, Hi: 2, Repeat: 0, LoopDirection: PingPong},
		},
		Slices: []SpriteSlice{
			{
				Name:      "slice-1",
				HasCenter: true,
				HasPivot:  true,
				Keys: []SliceKey{{
					Frame:  0,
					Bounds: image.Rect(1, 1, 5, 5),
					Center: image.Rect(1, 1, 3, 3),
					Pivot:  image.Pt(2, 2),
				}},
				Data:  []byte("slice one data"),
				Color: color.NRGBA{R: 1, G: 2, B: 3, A: 255},
			},
			{
				Name: "slice-2",
				Keys: []SliceKey{{
					Frame:  2,
					Bounds: image.Rect(0, 0, 2, 2),
				}},
			},
		},
	}
}

func buildDepthSprite(bpp BPP) *Sprite {
	doc := &Sprite{
		Width:  4,
		Height: 4,
		BPP:    bpp,
		Layers: []SpriteLayer{{Name: "only", Visible: true, Opacity: 255}},
	}
	switch bpp {
	case Indexed8:
		doc.Palette = testPalette
		doc.TransparentIndex = 0
	case Grayscale16:
	case RGBA32:
	}
	for fi := 0; fi < 2; fi++ {
		fr := SpriteFrame{Duration: time.Duration(70+fi*10) * time.Millisecond}
		var img image.Image
		switch bpp {
		case Indexed8:
			img = palettedTestImage(4, 4, func(x, y int) uint8 { return uint8((x + y + fi) % 4) })
		case Grayscale16:
			img = gray16TestImage(4, 4)
		case RGBA32:
			img = rgbaTestImage(4, 4, byte(fi*10))
		}
		fr.Cels = []SpriteCel{{Layer: 0, LinkedFrame: -1, Bounds: image.Rect(0, 0, 4, 4), Opacity: 255, Image: img}}
		doc.Frames = append(doc.Frames, fr)
	}
	return doc
}

func encodeBytes(t *testing.T, doc *Sprite, compressed bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	var err error
	if compressed {
		_, err = doc.WriteTo(&buf, WithCompressedCels())
	} else {
		_, err = doc.WriteTo(&buf)
	}
	require.NoError(t, err)
	require.True(t, len(buf.Bytes()) > 128, "file too small")
	return buf.Bytes()
}

func TestEncodeRoundTrip(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		name := "raw"
		if compressed {
			name = "compressed"
		}
		t.Run(name, func(t *testing.T) {
			doc := buildTestSprite()
			raw := encodeBytes(t, doc, compressed)

			// The independent byte checker validates layout, lengths, order,
			// metadata and exact pixel bytes without using the package reader.
			checkASEBytes(t, raw, doc, compressed)

			spr, err := Read(bytes.NewReader(raw))
			require.NoError(t, err)

			require.True(t, len(spr.Frames) == 3, "frames", len(spr.Frames))
			require.True(t, spr.Frames[0].Duration == 100*time.Millisecond, "dur0")
			require.True(t, spr.Frames[1].Duration == 120*time.Millisecond, "dur1")
			require.True(t, spr.Frames[2].Duration == 140*time.Millisecond, "dur2")

			require.True(t, len(spr.Tags) == 2, "tags", len(spr.Tags))
			require.True(t, spr.Tags[0].Name == "Up" && spr.Tags[0].Lo == 0 && spr.Tags[0].Hi == 1 &&
				spr.Tags[0].Repeat == 3 && spr.Tags[0].LoopDirection == Forward, "tag0")
			require.True(t, spr.Tags[1].Name == "Down" && spr.Tags[1].Lo == 1 && spr.Tags[1].Hi == 2 &&
				spr.Tags[1].LoopDirection == PingPong, "tag1")

			require.True(t, len(spr.Slices) == 2, "slices", len(spr.Slices))
			s0 := spr.Slices[0]
			require.True(t, s0.Name == "slice-1", "slice0 name")
			require.True(t, s0.Bounds == image.Rect(1, 1, 5, 5), "slice0 bounds")
			require.True(t, s0.Center == image.Rect(1, 1, 3, 3), "slice0 center")
			require.True(t, s0.Pivot == image.Pt(2, 2), "slice0 pivot")
			require.True(t, string(s0.Data) == "slice one data", "slice0 data")
			nrgba, ok := s0.Color.(color.NRGBA)
			require.True(t, ok && nrgba == (color.NRGBA{1, 2, 3, 255}), "slice0 color")
			require.True(t, spr.Slices[1].Name == "slice-2" &&
				spr.Slices[1].Bounds == image.Rect(0, 0, 2, 2), "slice1")

			require.True(t, len(spr.LayerData) == 2, "layer data", len(spr.LayerData))
			require.True(t, string(spr.LayerData[0]) == "layer base data", "layer0 data")
			require.True(t, string(spr.LayerData[1]) == "layer top data", "layer1 data")

			require.True(t, len(spr.Frames[0].Data) == 2, "frame0 data", len(spr.Frames[0].Data))
			require.True(t, string(spr.Frames[0].Data[0]) == "cel f0 l0", "cel data order")
			require.True(t, string(spr.Frames[0].Data[1]) == "cel f0 l1", "cel data order")
			require.True(t, len(spr.Frames[2].Data) == 1, "frame2 data", len(spr.Frames[2].Data))

			// The transparent border must decode as fully transparent pixels
			// and the interior as opaque, regardless of the atlas image type.
			b0 := spr.Frames[0].Bounds
			_, _, _, trA := spr.At(b0.Min.X, b0.Min.Y).RGBA()
			require.True(t, trA == 0, "transparent pixel alpha", trA)
			_, _, _, opA := spr.At(b0.Min.X+1, b0.Min.Y+1).RGBA()
			require.True(t, opA == 0xffff, "opaque pixel alpha", opA)
		})
	}
}

func TestEncodeColorDepths(t *testing.T) {
	for _, bpp := range []BPP{Indexed8, Grayscale16, RGBA32} {
		for _, compressed := range []bool{false, true} {
			doc := buildDepthSprite(bpp)
			raw := encodeBytes(t, doc, compressed)
			checkASEBytes(t, raw, doc, compressed)

			spr, err := Read(bytes.NewReader(raw))
			require.NoError(t, err)
			require.True(t, len(spr.Frames) == 2, "frames")
			require.True(t, spr.Frames[1].Duration == 80*time.Millisecond, "duration")
		}
	}
}

func TestEncodeDeterministic(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		doc1 := buildTestSprite()
		doc2 := buildTestSprite()
		a := encodeBytes(t, doc1, compressed)
		b := encodeBytes(t, doc2, compressed)
		require.True(t, bytes.Equal(a, b), "encoding must be deterministic")
	}
}

func TestEncodeCelOrderIndependent(t *testing.T) {
	doc := buildTestSprite()
	// Reverse the cel order in frame 0; output bytes must be identical
	// because cels are emitted in layer order.
	doc.Frames[0].Cels[0], doc.Frames[0].Cels[1] = doc.Frames[0].Cels[1], doc.Frames[0].Cels[0]
	reversed := encodeBytes(t, doc, false)
	normal := encodeBytes(t, buildTestSprite(), false)
	require.True(t, bytes.Equal(reversed, normal), "cel input order must not affect output")
}

// failingWriter fails after accepting failAfter bytes.
type failingWriter struct {
	failAfter int
	n         int
}

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.n >= w.failAfter {
		return 0, errors.New("disk full")
	}
	k := len(p)
	if w.n+k > w.failAfter {
		k = w.failAfter - w.n
	}
	w.n += k
	if w.n >= w.failAfter {
		return k, errors.New("disk full")
	}
	return k, nil
}

func TestEncodeWriteFailure(t *testing.T) {
	doc := buildTestSprite()
	snap1 := snapshotSprite(t, doc)

	w := &failingWriter{failAfter: 37}
	n, err := doc.WriteTo(w)
	require.True(t, err != nil, "expected write error")
	require.True(t, n == int64(w.n), "reported byte count")

	snap2 := snapshotSprite(t, doc)
	require.True(t, reflect.DeepEqual(snap1, snap2), "input must be unchanged after write failure")
}

func TestEncodeValidation(t *testing.T) {
	type tc struct {
		name string
		mut  func(*Sprite)
		want error
	}
	cases := []tc{
		{"bad bpp", func(s *Sprite) { s.BPP = 12 }, errInvalidBPP},
		{"zero width", func(s *Sprite) { s.Width = 0 }, errInvalidDimension},
		{"huge height", func(s *Sprite) { s.Height = maxASEDimension + 1 }, errInvalidDimension},
		{"huge width", func(s *Sprite) { s.Width = 70000 }, errInvalidDimension},
		{"linked cel bad bounds", func(s *Sprite) {
			s.Frames[1].Cels[1].Bounds = image.Rect(0, 0, testW+1, testH)
		}, errCelBoundsOutOfRange},
		{"no frames", func(s *Sprite) { s.Frames = nil }, errNoFrames},
		{"no layers", func(s *Sprite) { s.Layers = nil }, errNoLayers},
		{"bad blend mode", func(s *Sprite) { s.Layers[0].BlendMode = 19 }, errBlendModeOutOfRange},
		{"empty layer name", func(s *Sprite) { s.Layers[0].Name = "" }, errEmptyLayerName},
		{"duplicate layer name", func(s *Sprite) { s.Layers[1].Name = "base" }, errDuplicateLayerName},
		{"empty frame", func(s *Sprite) { s.Frames[0].Cels = nil }, errEmptyFrame},
		{"all linked frame", func(s *Sprite) {
			s.Frames[0].Cels = []SpriteCel{{Layer: 0, LinkedFrame: -1, Bounds: image.Rect(0, 0, 1, 1)}}
		}, errRealCelMissingImage},
		{"no real cel", func(s *Sprite) {
			// Frame 1 can legally be all-linked, so replace its real cel
			// with a link to frame 0 on layer 0 as well.
			s.Frames[1].Cels = []SpriteCel{
				{Layer: 0, LinkedFrame: 0, Bounds: image.Rect(0, 0, 1, 1)},
			}
		}, errFrameWithoutRealCel},
		{"link to future", func(s *Sprite) {
			s.Frames[1].Cels[1].LinkedFrame = 2
			s.Frames[1].Cels[1].Bounds = image.Rect(2, 1, 6, 4)
		}, errLinkedFrameOutOfRange},
		{"dangling link target", func(s *Sprite) {
			// Remove the source cel on layer 1 of frame 0, so the link in
			// frame 1 no longer resolves to any cel.
			s.Frames[0].Cels = []SpriteCel{s.Frames[0].Cels[0]}
		}, errLinkedTargetMissing},
		{"linked with image", func(s *Sprite) {
			s.Frames[1].Cels[1].Image = image.NewPaletted(image.Rect(0, 0, 1, 1), testPalette)
		}, errLinkedCelHasImage},
		{"missing image", func(s *Sprite) { s.Frames[0].Cels[0].Image = nil }, errRealCelMissingImage},
		{"wrong image type", func(s *Sprite) { s.Frames[0].Cels[0].Image = image.NewNRGBA(image.Rect(0, 0, testW, testH)) }, errCelImageType},
		{"image size mismatch", func(s *Sprite) {
			s.Frames[0].Cels[0].Image = palettedTestImage(testW, testH-1, func(x, y int) uint8 { return 1 })
		}, errCelImageSize},
		{"palette index out of range", func(s *Sprite) {
			img := palettedTestImage(testW, testH, func(x, y int) uint8 { return 4 })
			s.Frames[0].Cels[0].Image = img
		}, errPaletteIndex},
		{"cel outside canvas", func(s *Sprite) { s.Frames[0].Cels[0].Bounds = image.Rect(1, 0, testW+1, testH) }, errCelBoundsOutOfRange},
		{"duplicate cel layer", func(s *Sprite) {
			s.Frames[0].Cels = append(s.Frames[0].Cels, SpriteCel{Layer: 0, LinkedFrame: -1, Bounds: image.Rect(0, 0, 1, 1), Image: palettedTestImage(1, 1, func(x, y int) uint8 { return 1 })})
		}, errDuplicateCel},
		{"cel layer out of range", func(s *Sprite) { s.Frames[0].Cels[0].Layer = 9 }, errCelLayerOutOfRange},
		{"duration too large", func(s *Sprite) { s.Frames[0].Duration = 66 * time.Second }, errDurationOutOfRange},
		{"negative duration", func(s *Sprite) { s.Frames[0].Duration = -time.Millisecond }, errDurationOutOfRange},
		{"tag range", func(s *Sprite) { s.Tags[0].Hi = 99 }, errTagRange},
		{"tag reversed", func(s *Sprite) { s.Tags[0].Lo, s.Tags[0].Hi = 2, 1 }, errTagRange},
		{"bad loop direction", func(s *Sprite) { s.Tags[0].LoopDirection = LoopDirection(99) }, errLoopDirection},
		{"duplicate tag name", func(s *Sprite) { s.Tags[1].Name = "Up" }, errDuplicateTagName},
		{"empty tag name", func(s *Sprite) { s.Tags[0].Name = "" }, errEmptyTagName},
		{"duplicate slice name", func(s *Sprite) { s.Slices[1].Name = "slice-1" }, errDuplicateSliceName},
		{"slice no keys", func(s *Sprite) { s.Slices[0].Keys = nil }, errSliceNoKeys},
		{"slice key bad frame", func(s *Sprite) { s.Slices[0].Keys[0].Frame = 99 }, errSliceKeyFrame},
		{"slice key unsorted", func(s *Sprite) {
			s.Slices[0].Keys = append(s.Slices[0].Keys, SliceKey{Frame: 0, Bounds: image.Rect(0, 0, 1, 1)})
		}, errSliceKeyFrame},
		{"slice bounds outside", func(s *Sprite) { s.Slices[0].Keys[0].Bounds = image.Rect(0, 0, testW+1, testH) }, errSliceBounds},
		{"slice center outside", func(s *Sprite) { s.Slices[0].Keys[0].Center = image.Rect(0, 0, 9, 9) }, errSliceCenter},
		{"slice pivot outside", func(s *Sprite) { s.Slices[0].Keys[0].Pivot = image.Pt(99, 1) }, errSlicePivot},
		{"indexed without palette", func(s *Sprite) { s.Palette = nil }, errPaletteRequired},
		{"palette too large", func(s *Sprite) { s.Palette = make(color.Palette, 257) }, errPaletteRequired},
		{"bad transparent index", func(s *Sprite) { s.TransparentIndex = 99 }, errTransparentIndex},
		{"palette on rgba", func(s *Sprite) {
			d := buildDepthSprite(RGBA32)
			d.Palette = testPalette
			*s = *d
		}, errPaletteNotAllowed},
		{"transparent index on rgba", func(s *Sprite) {
			d := buildDepthSprite(RGBA32)
			d.TransparentIndex = 7
			*s = *d
		}, errTransparentIndex},
		{"text too long", func(s *Sprite) { s.Layers[0].Data = make([]byte, maxTextLen+1) }, errTextTooLong},
		{"invalid utf8 data", func(s *Sprite) { s.Layers[0].Data = []byte{0xff, 0xfe} }, errInvalidText},
		{"invalid utf8 name", func(s *Sprite) { s.Layers[0].Name = string([]byte{0xff}) }, errInvalidText},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := buildTestSprite()
			c.mut(doc)
			before := snapshotSprite(t, doc)
			n, err := doc.WriteTo(&bytes.Buffer{})
			if c.want == nil {
				require.NoError(t, err)
				require.True(t, n > 0, "expected encoded bytes")
			} else {
				if !errors.Is(err, c.want) {
					t.Fatalf("%s: want error %v, got %v", c.name, c.want, err)
				}
				require.True(t, n == 0, "no bytes on validation error")
			}
			after := snapshotSprite(t, doc)
			require.True(t, reflect.DeepEqual(before, after), "input must be unchanged on validation error")
		})
	}
}

func TestEncodeDoesNotMutateInput(t *testing.T) {
	doc := buildTestSprite()
	before := snapshotSprite(t, doc)
	_ = encodeBytes(t, doc, true)
	after := snapshotSprite(t, doc)
	require.True(t, reflect.DeepEqual(before, after), "successful encode must not mutate the input")
}

// TestEncodeCelImageValidation exercises the per-depth image type and size
// checks for all three color depths.
func TestEncodeCelImageValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		bpp  BPP
		img  image.Image
		want error
	}{
		{"gray on indexed", Indexed8, image.NewGray16(image.Rect(0, 0, 2, 2)), errCelImageType},
		{"rgba on indexed", Indexed8, image.NewNRGBA(image.Rect(0, 0, 2, 2)), errCelImageType},
		{"paletted on gray", Grayscale16, image.NewPaletted(image.Rect(0, 0, 2, 2), testPalette), errCelImageType},
		{"rgba on gray", Grayscale16, image.NewNRGBA(image.Rect(0, 0, 2, 2)), errCelImageType},
		{"gray wrong size", Grayscale16, image.NewGray16(image.Rect(0, 0, 2, 3)), errCelImageSize},
		{"paletted on rgba", RGBA32, image.NewPaletted(image.Rect(0, 0, 2, 2), testPalette), errCelImageType},
		{"gray on rgba", RGBA32, image.NewGray16(image.Rect(0, 0, 2, 2)), errCelImageType},
		{"rgba wrong size", RGBA32, image.NewNRGBA(image.Rect(0, 0, 2, 3)), errCelImageSize},
		{"generic image", RGBA32, image.NewRGBA(image.Rect(0, 0, 2, 2)), errCelImageType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := buildDepthSprite(tc.bpp)
			doc.Frames[0].Cels[0].Image = tc.img
			doc.Frames[0].Cels[0].Bounds = image.Rect(0, 0, 4, 4)
			_, err := doc.WriteTo(&bytes.Buffer{})
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

// TestEncodeLayerFlags verifies every layer flag round trips through bytes.
func TestEncodeLayerFlags(t *testing.T) {
	doc := buildDepthSprite(Indexed8)
	doc.Layers[0].Visible = false
	doc.Layers[0].Editable = true
	doc.Layers[0].Background = true
	doc.Layers[0].Reference = true
	raw := encodeBytes(t, doc, false)
	checkASEBytes(t, raw, doc, false)

	spr, err := Read(bytes.NewReader(raw))
	require.NoError(t, err)
	require.True(t, len(spr.Frames) == 2, "frames")
}

// snapshotSprite serializes every input-relevant field into a deterministic
// byte digest so two snapshots can be compared with reflect.DeepEqual.
func snapshotSprite(t *testing.T, doc *Sprite) []byte {
	t.Helper()
	var b bytes.Buffer
	write := func(v any) { binary.Write(&b, binary.LittleEndian, v) }

	write(int32(doc.Width))
	write(int32(doc.Height))
	write(uint16(doc.BPP))
	write(int32(doc.TransparentIndex))
	for _, c := range doc.Palette {
		if c == nil {
			write(uint64(0xdeadbeef))
			continue
		}
		r, g, bl, a := c.RGBA()
		write(uint16(r))
		write(uint16(g))
		write(uint16(bl))
		write(uint16(a))
	}
	for _, l := range doc.Layers {
		b.WriteString(l.Name)
		write(uint8(l.Opacity))
		write(uint16(l.BlendMode))
		write(boolByte(l.Visible))
		write(boolByte(l.Background))
		write(boolByte(l.Reference))
		b.Write(l.Data)
	}
	for _, fr := range doc.Frames {
		write(int64(fr.Duration))
		for _, c := range fr.Cels {
			write(int32(c.Layer))
			write(int32(c.LinkedFrame))
			write(int32(c.Opacity))
			write(int32(c.Bounds.Min.X))
			write(int32(c.Bounds.Min.Y))
			write(int32(c.Bounds.Max.X))
			write(int32(c.Bounds.Max.Y))
			b.Write(c.Data)
			switch img := c.Image.(type) {
			case *image.Paletted:
				b.Write(img.Pix)
			case *image.Gray16:
				b.Write(img.Pix)
			case *image.NRGBA:
				b.Write(img.Pix)
			case nil:
			default:
				t.Fatalf("unexpected image type %T", img)
			}
		}
	}
	for _, tg := range doc.Tags {
		b.WriteString(tg.Name)
		write(int32(tg.Lo))
		write(int32(tg.Hi))
		write(tg.Repeat)
		write(uint8(tg.LoopDirection))
		b.Write(tg.Data)
	}
	for _, s := range doc.Slices {
		b.WriteString(s.Name)
		write(boolByte(s.HasCenter))
		write(boolByte(s.HasPivot))
		b.Write(s.Data)
		for _, k := range s.Keys {
			write(int32(k.Frame))
			write(int32(k.Bounds.Min.X))
			write(int32(k.Bounds.Min.Y))
			write(int32(k.Bounds.Max.X))
			write(int32(k.Bounds.Max.Y))
			write(int32(k.Center.Min.X))
			write(int32(k.Center.Min.Y))
			write(int32(k.Center.Max.X))
			write(int32(k.Center.Max.Y))
			write(int32(k.Pivot.X))
			write(int32(k.Pivot.Y))
		}
	}
	return b.Bytes()
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}
