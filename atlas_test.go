package aseprite

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"os"
	"testing"

	"github.com/askeladdk/aseprite/internal/require"
)

// checkPacking verifies the packing invariants: every frame keeps its full
// area, stays inside its page, and never overlaps another frame on the
// same page.
func checkPacking(t *testing.T, framew, frameh int, pages, framesr []image.Rectangle, pageOf []int) {
	t.Helper()

	total := 0
	for i, r := range framesr {
		require.True(t, r.Dx() == framew && r.Dy() == frameh, "frame size", i, r)
		require.True(t, r.In(pages[pageOf[i]]), "frame outside page", i, r, pages[pageOf[i]])
		require.True(t, pageOf[i] >= 0 && pageOf[i] < len(pages), "page index", i, pageOf[i])
		total += r.Dx() * r.Dy()
		for j := 0; j < i; j++ {
			if pageOf[j] == pageOf[i] {
				require.True(t, !r.Overlaps(framesr[j]), "frames overlap", i, j, r, framesr[j])
			}
		}
	}
	require.True(t, total == len(framesr)*framew*frameh, "total frame area", total)
}

func TestPackFrames(t *testing.T) {
	for _, tt := range []struct {
		Name             string
		NFrames          int
		FrameW, FrameH   int
		Opts             AtlasOptions
		WantPages        int
		WantPageOf       []int
		WantFirstBounds  image.Rectangle
		WantSecondBounds image.Rectangle
	}{
		{
			Name:    "single_page_mobile_limit",
			NFrames: 10, FrameW: 32, FrameH: 64,
			Opts:      AtlasOptions{MaxWidth: 1024, MaxHeight: 1024},
			WantPages: 1,
			// cols = 1024/32 = 32 > 10, so all frames in one row.
			WantFirstBounds:  image.Rect(0, 0, 32, 64),
			WantSecondBounds: image.Rect(32, 0, 64, 64),
		},
		{
			Name:    "multi_page_with_padding",
			NFrames: 10, FrameW: 32, FrameH: 64,
			Opts: AtlasOptions{MaxWidth: 96, MaxHeight: 96, MaxPages: 8, Padding: 2},
			// cell 34x66, cols = 94/34 = 2, rows = 94/66 = 1 -> 2 per page.
			WantPages:        5,
			WantPageOf:       []int{0, 0, 1, 1, 2, 2, 3, 3, 4, 4},
			WantFirstBounds:  image.Rect(2, 2, 34, 66),
			WantSecondBounds: image.Rect(36, 2, 68, 66),
		},
		{
			Name:    "non_square_frames",
			NFrames: 4, FrameW: 3, FrameH: 5,
			Opts: AtlasOptions{MaxWidth: 16, MaxHeight: 16, Padding: 1},
			// cell 4x6, cols = 15/4 = 3, rows = 15/6 = 2 -> 6 per page.
			WantPages:        1,
			WantFirstBounds:  image.Rect(1, 1, 4, 6),
			WantSecondBounds: image.Rect(5, 1, 8, 6),
		},
		{
			Name:    "single_frame",
			NFrames: 1, FrameW: 7, FrameH: 9,
			Opts:            AtlasOptions{MaxWidth: 16, MaxHeight: 16},
			WantPages:       1,
			WantFirstBounds: image.Rect(0, 0, 7, 9),
		},
		{
			Name:    "exact_fit",
			NFrames: 4, FrameW: 16, FrameH: 16,
			Opts:      AtlasOptions{MaxWidth: 32, MaxHeight: 32, MaxPages: 1},
			WantPages: 1,
			// 2x2 grid.
			WantFirstBounds:  image.Rect(0, 0, 16, 16),
			WantSecondBounds: image.Rect(16, 0, 32, 16),
		},
	} {
		t.Run(tt.Name, func(t *testing.T) {
			pages, framesr, pageOf, err := packFrames(tt.NFrames, tt.FrameW, tt.FrameH, tt.Opts)
			require.NoError(t, err)
			require.True(t, len(pages) == tt.WantPages, "pages", len(pages))
			require.True(t, len(framesr) == tt.NFrames, "frames", len(framesr))
			require.True(t, framesr[0] == tt.WantFirstBounds, "first bounds", framesr[0])
			if tt.NFrames > 1 {
				require.True(t, framesr[1] == tt.WantSecondBounds, "second bounds", framesr[1])
			}
			if tt.WantPageOf != nil {
				for i, p := range tt.WantPageOf {
					require.True(t, pageOf[i] == p, "page of frame", i, pageOf[i])
				}
			}
			checkPacking(t, tt.FrameW, tt.FrameH, pages, framesr, pageOf)
		})
	}
}

func TestPackFramesPageTrimming(t *testing.T) {
	// 10 frames of 32x64 with padding 2 in 96x96 pages: 2 per page,
	// every page trimmed to the used 70x68 area.
	pages, framesr, pageOf, err := packFrames(10, 32, 64, AtlasOptions{MaxWidth: 96, MaxHeight: 96, MaxPages: 8, Padding: 2})
	require.NoError(t, err)
	require.True(t, len(pages) == 5, "pages", len(pages))
	for i, p := range pages {
		require.True(t, p == image.Rect(0, 0, 70, 68), "page bounds", i, p)
	}
	checkPacking(t, 32, 64, pages, framesr, pageOf)
}

func TestPackFramesErrors(t *testing.T) {
	for _, tt := range []struct {
		Name           string
		NFrames        int
		FrameW, FrameH int
		Opts           AtlasOptions
		WantErr        error
	}{
		{
			Name:    "frame_too_large",
			NFrames: 1, FrameW: 64, FrameH: 64,
			Opts:    AtlasOptions{MaxWidth: 32, MaxHeight: 32},
			WantErr: ErrFrameTooLarge,
		},
		{
			Name:    "frame_too_tall",
			NFrames: 1, FrameW: 16, FrameH: 2048,
			Opts:    AtlasOptions{MaxWidth: 1024, MaxHeight: 1024},
			WantErr: ErrFrameTooLarge,
		},
		{
			Name:    "padding_too_large",
			NFrames: 1, FrameW: 64, FrameH: 64,
			Opts:    AtlasOptions{MaxWidth: 66, MaxHeight: 66, Padding: 2},
			WantErr: ErrPaddingTooLarge,
		},
		{
			Name:    "too_many_pages",
			NFrames: 10, FrameW: 32, FrameH: 64,
			Opts:    AtlasOptions{MaxWidth: 96, MaxHeight: 96, MaxPages: 3, Padding: 2},
			WantErr: ErrTooManyPages,
		},
		{
			Name:    "default_single_page_overflow",
			NFrames: 10, FrameW: 32, FrameH: 64,
			Opts:    AtlasOptions{MaxWidth: 96, MaxHeight: 96, Padding: 2},
			WantErr: ErrTooManyPages,
		},
		{
			Name:    "dimension_overflow",
			NFrames: 1, FrameW: 1, FrameH: 1,
			Opts:    AtlasOptions{MaxWidth: 1 << 40, MaxHeight: 1 << 40},
			WantErr: ErrAtlasOverflow,
		},
		{
			Name:    "padding_overflow",
			NFrames: 1, FrameW: 1, FrameH: 1,
			Opts:    AtlasOptions{MaxWidth: 1024, MaxHeight: 1024, Padding: int(^uint(0)>>1) - 1},
			WantErr: ErrAtlasOverflow,
		},
		{
			Name:    "negative_padding",
			NFrames: 1, FrameW: 1, FrameH: 1,
			Opts:    AtlasOptions{MaxWidth: 16, MaxHeight: 16, Padding: -1},
			WantErr: ErrInvalidOptions,
		},
		{
			Name:    "missing_limits",
			NFrames: 1, FrameW: 1, FrameH: 1,
			Opts:    AtlasOptions{MaxPages: 4},
			WantErr: ErrInvalidOptions,
		},
	} {
		t.Run(tt.Name, func(t *testing.T) {
			_, _, _, err := packFrames(tt.NFrames, tt.FrameW, tt.FrameH, tt.Opts)
			require.True(t, err != nil, "expected error")
			require.True(t, errors.Is(err, tt.WantErr), "error", err)
		})
	}
}

func readFixture(t *testing.T, filename string) *Aseprite {
	t.Helper()
	f, err := os.Open(filename)
	require.NoError(t, err)
	defer f.Close()
	spr, err := Read(f)
	require.NoError(t, err)
	return spr
}

func readFixtureWithOptions(t *testing.T, filename string, opts AtlasOptions) *Aseprite {
	t.Helper()
	f, err := os.Open(filename)
	require.NoError(t, err)
	defer f.Close()
	spr, err := ReadWithOptions(f, opts)
	require.NoError(t, err)
	return spr
}

// framePixels extracts frame i of spr as NRGBA pixels for comparison.
func framePixels(spr *Aseprite, i int) []byte {
	fr := spr.Frames[i]
	dst := image.NewNRGBA(image.Rect(0, 0, fr.Bounds.Dx(), fr.Bounds.Dy()))
	draw.Draw(dst, dst.Bounds(), spr.Pages[fr.Page], fr.Bounds.Min, draw.Src)
	return dst.Pix
}

func TestReadWithOptionsPixelConsistency(t *testing.T) {
	for _, tt := range []struct {
		Name     string
		Filename string
		Opts     AtlasOptions
		Frames   int
		Pages    int
	}{
		{
			Name:     "paletted_8bpp_multi_page",
			Filename: "./testfiles/slime_paletted.aseprite",
			Opts:     AtlasOptions{MaxWidth: 96, MaxHeight: 96, MaxPages: 8, Padding: 2},
			Frames:   10,
			Pages:    5,
		},
		{
			Name:     "grayscale_16bpp_multi_page",
			Filename: "./testfiles/slime_grayscale.aseprite",
			Opts:     AtlasOptions{MaxWidth: 96, MaxHeight: 96, MaxPages: 8, Padding: 2},
			Frames:   10,
			Pages:    5,
		},
		{
			Name:     "rgba_32bpp_single_frame",
			Filename: "./testfiles/blendtest.aseprite",
			Opts:     AtlasOptions{MaxWidth: 1024, MaxHeight: 1024, Padding: 4},
			Frames:   1,
			Pages:    1,
		},
		{
			Name:     "paletted_single_frame",
			Filename: "./testfiles/index_error.aseprite",
			Opts:     AtlasOptions{MaxWidth: 1024, MaxHeight: 1024, Padding: 1},
			Frames:   1,
			Pages:    1,
		},
		{
			Name:     "paletted_single_page_no_padding",
			Filename: "./testfiles/slime_paletted.aseprite",
			Opts:     AtlasOptions{MaxWidth: 1024, MaxHeight: 1024},
			Frames:   10,
			Pages:    1,
		},
	} {
		t.Run(tt.Name, func(t *testing.T) {
			ref := readFixture(t, tt.Filename)
			spr := readFixtureWithOptions(t, tt.Filename, tt.Opts)

			require.True(t, len(spr.Pages) == tt.Pages, "pages", len(spr.Pages))
			require.True(t, len(spr.Frames) == tt.Frames, "frames", len(spr.Frames))
			require.True(t, len(ref.Frames) == tt.Frames, "ref frames", len(ref.Frames))

			// Every frame must be pixel-identical to the default layout.
			for i := range ref.Frames {
				require.True(t,
					bytes.Equal(framePixels(ref, i), framePixels(spr, i)),
					"frame pixels differ", i)
			}

			// Frames must stay inside their pages and not overlap.
			pages := make([]image.Rectangle, len(spr.Pages))
			for i, p := range spr.Pages {
				pages[i] = p.Bounds()
			}
			framesr := make([]image.Rectangle, len(spr.Frames))
			pageOf := make([]int, len(spr.Frames))
			for i, fr := range spr.Frames {
				framesr[i] = fr.Bounds
				pageOf[i] = fr.Page
			}
			checkPacking(t, framesr[0].Dx(), framesr[0].Dy(), pages, framesr, pageOf)
		})
	}
}

func TestReadWithOptionsDefaultLayoutCompatible(t *testing.T) {
	// The zero value must reproduce the legacy single-atlas layout exactly.
	for _, filename := range []string{
		"./testfiles/slime_paletted.aseprite",
		"./testfiles/slime_grayscale.aseprite",
		"./testfiles/blendtest.aseprite",
		"./testfiles/index_error.aseprite",
	} {
		ref := readFixture(t, filename)
		spr := readFixtureWithOptions(t, filename, AtlasOptions{})

		require.True(t, len(spr.Pages) == 1, "pages", filename)
		require.True(t, spr.Image.Bounds() == ref.Image.Bounds(), "atlas bounds", filename)
		require.True(t, len(spr.Frames) == len(ref.Frames), "frames", filename)
		for i := range ref.Frames {
			require.True(t, spr.Frames[i].Bounds == ref.Frames[i].Bounds, "bounds", filename, i)
			require.True(t, spr.Frames[i].Page == 0, "page", filename, i)
		}
		require.True(t, bytes.Equal(framePixels(ref, 0), framePixels(spr, 0)), "pixels", filename)
	}
}

func TestReadWithOptionsError(t *testing.T) {
	// slime frames are 32x64 and do not fit in a 48x48 page.
	f, err := os.Open("./testfiles/slime_paletted.aseprite")
	require.NoError(t, err)
	defer f.Close()

	_, err = ReadWithOptions(f, AtlasOptions{MaxWidth: 48, MaxHeight: 48})
	require.True(t, errors.Is(err, ErrFrameTooLarge), "error", err)
}

// makeSyntheticFile builds a 3-frame file of 2x2 frames at the given color
// depth. The middle frame is empty (no cels); frames 0 and 2 hold one cel
// filled with a solid color.
func makeSyntheticFile(bpp uint16) *file {
	f := &file{
		framew: 2,
		frameh: 2,
		bpp:    bpp,
		layers: []layer{{flags: 1}},
	}
	bounds := image.Rect(0, 0, 2, 2)

	var pix []byte
	switch bpp {
	case 8:
		f.palette = color.Palette{color.Transparent, color.NRGBA{200, 100, 50, 255}}
		f.makeCel = makeCelImage8
		pix = []byte{1, 1, 1, 1}
	case 16:
		f.makeCel = makeCelImage16
		pix = []byte{0x12, 0x34, 0x12, 0x34, 0x12, 0x34, 0x12, 0x34}
	default:
		f.makeCel = makeCelImage32
		pix = []byte{
			10, 20, 30, 255,
			10, 20, 30, 255,
			10, 20, 30, 255,
			10, 20, 30, 255,
		}
	}

	f.frames = make([]frame, 3)
	for i := range f.frames {
		f.frames[i].cels = make([]cel, 1)
		if i != 1 { // frame 1 stays empty
			f.frames[i].cels[0] = f.makeCel(f, bounds, 255, append([]byte{}, pix...))
		}
	}
	return f
}

func TestBuildAtlasEmptyFrames(t *testing.T) {
	for _, bpp := range []uint16{8, 16, 32} {
		// Reference: legacy single-atlas layout.
		refPages, refFramesr, refPageOf, err := makeSyntheticFile(bpp).buildAtlas(AtlasOptions{})
		require.NoError(t, err)
		require.True(t, len(refPages) == 1, "ref pages", bpp)
		for _, p := range refPageOf {
			require.True(t, p == 0, "ref page", bpp)
		}

		pages, framesr, pageOf, err := makeSyntheticFile(bpp).buildAtlas(AtlasOptions{
			MaxWidth: 8, MaxHeight: 8, MaxPages: 2, Padding: 1,
		})
		require.NoError(t, err)

		// cell 3x3, cols = 7/3 = 2, rows = 7/3 = 2 -> 4 per page -> 1 page.
		require.True(t, len(pages) == 1, "pages", bpp, len(pages))
		require.True(t, len(framesr) == 3, "frames", bpp)
		checkPacking(t, 2, 2, []image.Rectangle{pages[0].Bounds()}, framesr, pageOf)

		page, refPage := pages[0], refPages[0]
		for i := 0; i < 3; i++ {
			for y := 0; y < 2; y++ {
				for x := 0; x < 2; x++ {
					got := page.At(framesr[i].Min.X+x, framesr[i].Min.Y+y)
					want := refPage.At(refFramesr[i].Min.X+x, refFramesr[i].Min.Y+y)
					// Packed frames must be pixel-identical to the
					// legacy layout, whatever the color depth.
					require.True(t, got == want, "pixel", bpp, i, x, y, got, want)
					if i == 1 {
						// The empty frame must be blank: zero pixels.
						// (Gray16 pages have no alpha channel, so check
						// the pixel value instead of transparency.)
						if bpp == 16 {
							require.True(t, got == color.Gray16{0}, "empty frame not blank", bpp, x, y, got)
						} else {
							_, _, _, a := got.RGBA()
							require.True(t, a == 0, "empty frame not transparent", bpp, x, y, got)
						}
					} else {
						// Non-empty frames must not be transparent.
						_, _, _, a := got.RGBA()
						require.True(t, a != 0, "frame lost its pixels", bpp, i, x, y, got)
					}
				}
			}
		}
	}
}
