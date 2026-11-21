package aseprite

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"strings"
	"testing"
)

// fixtureLayer describes one layer chunk in a generated test file.
type fixtureLayer struct {
	name    string
	typ     uint16
	level   uint16
	flags   uint16
	blend   uint16
	opacity byte
}

// fixtureCel describes one raw (uncompressed) cel chunk in a generated
// test file. pix must hold w*h*4 bytes of NRGBA pixel data.
type fixtureCel struct {
	layer   uint16
	x, y    int16
	opacity byte
	w, h    uint16
	pix     []byte
}

func put16(b *bytes.Buffer, v uint16) {
	var raw [2]byte
	binary.LittleEndian.PutUint16(raw[:], v)
	b.Write(raw[:])
}

func put32(b *bytes.Buffer, v uint32) {
	var raw [4]byte
	binary.LittleEndian.PutUint32(raw[:], v)
	b.Write(raw[:])
}

func writeChunk(b *bytes.Buffer, typ uint16, payload []byte) {
	put32(b, uint32(6+len(payload)))
	put16(b, typ)
	b.Write(payload)
}

// buildFixture generates a minimal single-frame 32bpp .aseprite file.
func buildFixture(w, h uint16, layers []fixtureLayer, cels []fixtureCel) []byte {
	var chunks bytes.Buffer
	nchunks := 0

	for _, l := range layers {
		var c bytes.Buffer
		put16(&c, l.flags)
		put16(&c, l.typ)
		put16(&c, l.level)
		put16(&c, 0) // default width
		put16(&c, 0) // default height
		put16(&c, l.blend)
		c.WriteByte(l.opacity)
		c.Write([]byte{0, 0, 0}) // reserved
		put16(&c, uint16(len(l.name)))
		c.WriteString(l.name)
		writeChunk(&chunks, 0x2004, c.Bytes())
		nchunks++
	}

	for _, cl := range cels {
		var c bytes.Buffer
		put16(&c, cl.layer)
		put16(&c, uint16(cl.x))
		put16(&c, uint16(cl.y))
		c.WriteByte(cl.opacity)
		put16(&c, 0) // raw cel
		c.Write([]byte{0, 0, 0, 0, 0, 0, 0})
		put16(&c, cl.w)
		put16(&c, cl.h)
		c.Write(cl.pix)
		writeChunk(&chunks, 0x2005, c.Bytes())
		nchunks++
	}

	var frame bytes.Buffer
	put32(&frame, uint32(16+chunks.Len()))
	put16(&frame, 0xF1FA)
	put16(&frame, uint16(nchunks))
	put16(&frame, 100) // duration ms
	put16(&frame, 0)   // reserved
	put32(&frame, uint32(nchunks))
	frame.Write(chunks.Bytes())

	var hdr bytes.Buffer
	put32(&hdr, uint32(128+frame.Len()))
	put16(&hdr, 0xA5E0)
	put16(&hdr, 1) // one frame
	put16(&hdr, w)
	put16(&hdr, h)
	put16(&hdr, 32) // rgba
	put32(&hdr, 1)  // flags: layer opacity valid
	put16(&hdr, 100)
	put32(&hdr, 0)
	put32(&hdr, 0)
	hdr.WriteByte(0)           // transparent palette index
	hdr.Write([]byte{0, 0, 0}) // reserved
	put16(&hdr, 1)             // number of colors
	hdr.WriteByte(1)           // pixel width
	hdr.WriteByte(1)           // pixel height
	hdr.Write(make([]byte, 128-hdr.Len()))

	return append(hdr.Bytes(), frame.Bytes()...)
}

// solidPix returns w*h pixels of one NRGBA color.
func solidPix(w, h int, c color.NRGBA) []byte {
	pix := make([]byte, w*h*4)
	for i := 0; i < len(pix); i += 4 {
		pix[i+0] = c.R
		pix[i+1] = c.G
		pix[i+2] = c.B
		pix[i+3] = c.A
	}
	return pix
}

func readFixture(t *testing.T, raw []byte) Aseprite {
	t.Helper()
	spr, err := Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return spr
}

func layerNames(layers []*Layer) []string {
	names := make([]string, len(layers))
	for i, l := range layers {
		names[i] = l.Name
	}
	return names
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// nestedFixture builds a file with nested groups, a hidden layer and a
// reference layer:
//
//	top        (normal)
//	ref        (normal, reference)
//	outer      (group)
//	  mid      (normal)
//	  inner    (group, opacity 128)
//	    leaf2  (normal, multiply, opacity 200)
//	    leaf1  (normal)
//	ghost      (normal, hidden)
//	bg         (normal)
func nestedFixture() []byte {
	return buildFixture(1, 1,
		[]fixtureLayer{
			{name: "bg", typ: 0, level: 0, flags: 1, opacity: 255},
			{name: "ghost", typ: 0, level: 0, flags: 0, opacity: 255},
			{name: "leaf1", typ: 0, level: 2, flags: 1, opacity: 255},
			{name: "leaf2", typ: 0, level: 2, flags: 1, blend: 1, opacity: 200},
			{name: "inner", typ: 1, level: 1, flags: 1, opacity: 128},
			{name: "mid", typ: 0, level: 1, flags: 1, opacity: 255},
			{name: "outer", typ: 1, level: 0, flags: 1, opacity: 255},
			{name: "ref", typ: 0, level: 0, flags: 1 | 64, opacity: 255},
			{name: "top", typ: 0, level: 0, flags: 1, opacity: 255},
		},
		[]fixtureCel{
			{layer: 0, opacity: 255, w: 1, h: 1, pix: solidPix(1, 1, color.NRGBA{255, 0, 0, 255})},
		},
	)
}

func TestLayerTree(t *testing.T) {
	spr := readFixture(t, nestedFixture())

	wantRoots := []string{"bg", "ghost", "outer", "ref", "top"}
	if got := layerNames(spr.Layers); !equalStrings(got, wantRoots) {
		t.Fatalf("roots = %v, want %v", got, wantRoots)
	}

	byName := make(map[string]*Layer)
	var walk func(l *Layer)
	walk = func(l *Layer) {
		byName[l.Name] = l
		for _, c := range l.Children {
			walk(c)
		}
	}
	for _, l := range spr.Layers {
		walk(l)
	}

	outer := byName["outer"]
	if outer == nil || !outer.Group {
		t.Fatal("outer is not a group")
	}
	if got := layerNames(outer.Children); !equalStrings(got, []string{"inner", "mid"}) {
		t.Fatalf("outer children = %v", got)
	}

	inner := byName["inner"]
	if inner == nil || !inner.Group {
		t.Fatal("inner is not a group")
	}
	if got := layerNames(inner.Children); !equalStrings(got, []string{"leaf1", "leaf2"}) {
		t.Fatalf("inner children = %v", got)
	}
	if inner.Opacity != 128 {
		t.Fatalf("inner opacity = %d, want 128", inner.Opacity)
	}
	if inner.Parent != outer {
		t.Fatal("inner.Parent != outer")
	}

	leaf2 := byName["leaf2"]
	if leaf2.Parent != inner {
		t.Fatal("leaf2.Parent != inner")
	}
	if leaf2.BlendMode != 1 || leaf2.Opacity != 200 {
		t.Fatalf("leaf2 blend/opacity = %d/%d", leaf2.BlendMode, leaf2.Opacity)
	}
	if leaf2.Index != 3 {
		t.Fatalf("leaf2 index = %d, want 3", leaf2.Index)
	}

	if byName["ghost"].Visible {
		t.Fatal("ghost should be hidden")
	}
	if !byName["ref"].Reference {
		t.Fatal("ref should be a reference layer")
	}
	if byName["bg"].Group || byName["bg"].Parent != nil {
		t.Fatal("bg should be a top-level normal layer")
	}
	if len(byName) != 9 {
		t.Fatalf("tree holds %d layers, want 9", len(byName))
	}
}

func assertPixel(t *testing.T, img image.Image, want color.NRGBA, tol int) {
	t.Helper()
	r, g, b, a := img.At(0, 0).RGBA()
	got := []int{int(r >> 8), int(g >> 8), int(b >> 8), int(a >> 8)}
	exp := []int{int(want.R), int(want.G), int(want.B), int(want.A)}
	for i := range got {
		if d := got[i] - exp[i]; d < -tol || d > tol {
			t.Fatalf("pixel = %v, want %v (±%d)", got, exp, tol)
		}
	}
}

func TestFrameImageGroupOpacity(t *testing.T) {
	// bg is opaque red. Group g (opacity 128) holds two opaque children:
	// green below, blue on top. The group composites to opaque blue first,
	// then is drawn over red at 50%%: (127, 0, 128). Applying the group
	// opacity per child instead would give a grayish (63, 63, 128).
	raw := buildFixture(1, 1,
		[]fixtureLayer{
			{name: "bg", typ: 0, level: 0, flags: 1, opacity: 255},
			{name: "c1", typ: 0, level: 1, flags: 1, opacity: 255},
			{name: "c2", typ: 0, level: 1, flags: 1, opacity: 255},
			{name: "g", typ: 1, level: 0, flags: 1, opacity: 128},
		},
		[]fixtureCel{
			{layer: 0, opacity: 255, w: 1, h: 1, pix: solidPix(1, 1, color.NRGBA{255, 0, 0, 255})},
			{layer: 1, opacity: 255, w: 1, h: 1, pix: solidPix(1, 1, color.NRGBA{0, 255, 0, 255})},
			{layer: 2, opacity: 255, w: 1, h: 1, pix: solidPix(1, 1, color.NRGBA{0, 0, 255, 255})},
		},
	)
	spr := readFixture(t, raw)

	img, err := spr.FrameImage(0, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPixel(t, img, color.NRGBA{127, 0, 128, 255}, 1)

	// Excluding the group by name leaves only the background.
	img, err = spr.FrameImage(0, func(l *Layer) bool { return l.Name != "g" })
	if err != nil {
		t.Fatal(err)
	}
	assertPixel(t, img, color.NRGBA{255, 0, 0, 255}, 0)

	// Excluding only the top child composites green at group opacity.
	img, err = spr.FrameImage(0, func(l *Layer) bool { return l.Name != "c2" })
	if err != nil {
		t.Fatal(err)
	}
	assertPixel(t, img, color.NRGBA{127, 128, 0, 255}, 1)
}

func TestFrameImageBlendMode(t *testing.T) {
	raw := buildFixture(1, 1,
		[]fixtureLayer{
			{name: "base", typ: 0, level: 0, flags: 1, opacity: 255},
			{name: "add", typ: 0, level: 0, flags: 1, blend: 16, opacity: 255},
		},
		[]fixtureCel{
			{layer: 0, opacity: 255, w: 1, h: 1, pix: solidPix(1, 1, color.NRGBA{10, 20, 30, 255})},
			{layer: 1, opacity: 255, w: 1, h: 1, pix: solidPix(1, 1, color.NRGBA{1, 2, 3, 255})},
		},
	)
	spr := readFixture(t, raw)

	img, err := spr.FrameImage(0, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPixel(t, img, color.NRGBA{11, 22, 33, 255}, 0)
}

func TestFrameImageHiddenAndReference(t *testing.T) {
	raw := buildFixture(1, 1,
		[]fixtureLayer{
			{name: "vis", typ: 0, level: 0, flags: 1, opacity: 255},
			{name: "hid", typ: 0, level: 0, flags: 0, opacity: 255},
			{name: "ref", typ: 0, level: 0, flags: 1 | 64, opacity: 255},
			{name: "child", typ: 0, level: 1, flags: 1, opacity: 255},
			{name: "hidgrp", typ: 1, level: 0, flags: 0, opacity: 255},
		},
		[]fixtureCel{
			{layer: 0, opacity: 255, w: 1, h: 1, pix: solidPix(1, 1, color.NRGBA{0, 255, 0, 255})},
			{layer: 1, opacity: 255, w: 1, h: 1, pix: solidPix(1, 1, color.NRGBA{255, 0, 0, 255})},
			{layer: 2, opacity: 255, w: 1, h: 1, pix: solidPix(1, 1, color.NRGBA{0, 0, 255, 255})},
			{layer: 3, opacity: 255, w: 1, h: 1, pix: solidPix(1, 1, color.NRGBA{255, 255, 0, 255})},
		},
	)
	spr := readFixture(t, raw)

	// "child" is visible itself but lives under the hidden group,
	// so the whole subtree is skipped.
	img, err := spr.FrameImage(0, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPixel(t, img, color.NRGBA{0, 255, 0, 255}, 0)
}

func TestFrameImageEmptyGroup(t *testing.T) {
	raw := buildFixture(1, 1,
		[]fixtureLayer{
			{name: "empty", typ: 1, level: 0, flags: 1, opacity: 255},
			{name: "bg", typ: 0, level: 0, flags: 1, opacity: 255},
		},
		[]fixtureCel{
			{layer: 1, opacity: 255, w: 1, h: 1, pix: solidPix(1, 1, color.NRGBA{9, 8, 7, 255})},
		},
	)
	spr := readFixture(t, raw)

	if got := layerNames(spr.Layers); !equalStrings(got, []string{"empty", "bg"}) {
		t.Fatalf("roots = %v", got)
	}
	if spr.Layers[0].Children != nil {
		t.Fatal("empty group should have no children")
	}

	img, err := spr.FrameImage(0, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPixel(t, img, color.NRGBA{9, 8, 7, 255}, 0)

	// A file with only an empty group composites to transparent.
	raw2 := buildFixture(1, 1,
		[]fixtureLayer{{name: "empty", typ: 1, level: 0, flags: 1, opacity: 255}},
		nil,
	)
	spr2 := readFixture(t, raw2)
	img, err = spr2.FrameImage(0, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPixel(t, img, color.NRGBA{0, 0, 0, 0}, 0)
}

func TestLayerTreeMissingParent(t *testing.T) {
	raw := buildFixture(1, 1,
		[]fixtureLayer{{name: "orphan", typ: 0, level: 1, flags: 1, opacity: 255}},
		nil,
	)
	_, err := Read(bytes.NewReader(raw))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrNoParentGroup) {
		t.Fatalf("err = %v, want ErrNoParentGroup", err)
	}
	if !strings.Contains(err.Error(), `"orphan"`) {
		t.Fatalf("error lacks layer name: %v", err)
	}
}

func TestLayerTreeLevelJump(t *testing.T) {
	// A level-2 layer whose only ancestor candidate is at level 0
	// has no parent group at level 1.
	raw := buildFixture(1, 1,
		[]fixtureLayer{
			{name: "deep", typ: 0, level: 2, flags: 1, opacity: 255},
			{name: "grp", typ: 1, level: 0, flags: 1, opacity: 255},
		},
		nil,
	)
	_, err := Read(bytes.NewReader(raw))
	if !errors.Is(err, ErrNoParentGroup) {
		t.Fatalf("err = %v, want ErrNoParentGroup", err)
	}
}

func TestLayerUnknownType(t *testing.T) {
	raw := buildFixture(1, 1,
		[]fixtureLayer{{name: "mystery", typ: 9, level: 0, flags: 1, opacity: 255}},
		nil,
	)
	_, err := Read(bytes.NewReader(raw))
	if err == nil || !strings.Contains(err.Error(), "unknown layer type 9") {
		t.Fatalf("err = %v", err)
	}
}

func TestLayerTilemapType(t *testing.T) {
	raw := buildFixture(1, 1,
		[]fixtureLayer{{name: "tiles", typ: 2, level: 0, flags: 1, opacity: 255}},
		nil,
	)
	_, err := Read(bytes.NewReader(raw))
	if err == nil || !strings.Contains(err.Error(), "tilemap") {
		t.Fatalf("err = %v", err)
	}
}

func TestComposeNodeCycle(t *testing.T) {
	a := &Layer{Name: "a", Group: true, Visible: true}
	b := &Layer{Name: "b", Group: true, Visible: true}
	a.Children = []*Layer{b}
	b.Children = []*Layer{a}

	f := &file{framew: 1, frameh: 1, layers: make([]layer, 2)}
	fr := &frame{}
	bounds := image.Rect(0, 0, 1, 1)
	dst := image.NewRGBA(bounds)
	scratch := image.NewRGBA(bounds)

	err := f.composeNode(dst, scratch, bounds, fr, a, nil, 0)
	if !errors.Is(err, ErrLayerTreeCycle) {
		t.Fatalf("err = %v, want ErrLayerTreeCycle", err)
	}
}

func TestFrameImageErrors(t *testing.T) {
	var zero Aseprite
	if _, err := zero.FrameImage(0, nil); err == nil {
		t.Fatal("expected error for unparsed sprite")
	}

	spr := readFixture(t, nestedFixture())
	if _, err := spr.FrameImage(1, nil); err == nil {
		t.Fatal("expected error for out-of-range frame")
	}
	if _, err := spr.FrameImage(-1, nil); err == nil {
		t.Fatal("expected error for negative frame")
	}
}

// TestFlatResultsUnchanged verifies the legacy flat atlas ignores group
// structure and still composites every parsed cel in file order.
func TestFlatResultsUnchanged(t *testing.T) {
	raw := buildFixture(1, 1,
		[]fixtureLayer{
			{name: "bg", typ: 0, level: 0, flags: 1, opacity: 255},
			{name: "c1", typ: 0, level: 1, flags: 1, opacity: 255},
			{name: "g", typ: 1, level: 0, flags: 1, opacity: 128},
		},
		[]fixtureCel{
			{layer: 0, opacity: 255, w: 1, h: 1, pix: solidPix(1, 1, color.NRGBA{255, 0, 0, 255})},
			{layer: 1, opacity: 255, w: 1, h: 1, pix: solidPix(1, 1, color.NRGBA{0, 255, 0, 255})},
		},
	)
	spr := readFixture(t, raw)

	if len(spr.Frames) != 1 {
		t.Fatalf("frames = %d", len(spr.Frames))
	}
	// The flat atlas draws c1 directly over bg without group opacity.
	assertPixel(t, spr.Atlas, color.NRGBA{0, 255, 0, 255}, 0)
}
