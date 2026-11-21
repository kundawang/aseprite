package aseprite

import (
	"image"
)

// UV is a normalized texture coordinate.
// Values lie in the half-open interval [0, 1].
type UV struct {
	U, V float32
}

// UVRect is a half-open normalized texture-coordinate rectangle.
// Min is the upper-left corner and Max is the exclusive lower-right
// corner. The texel at pixel r.Max.X-1 is the last texel covered by
// the rectangle and r.Max.X maps to its right edge. An empty pixel
// rectangle maps to an empty UVRect where Min == Max.
type UVRect struct {
	Min UV
	Max UV
}

// Inset returns uv shrunk by d.U on the horizontal axis and d.V on the
// vertical axis. It is used to apply the half-texel convention for
// linear sampling:
//
//	d := aseprite.UV{0.5 / float32(page.Bounds.Dx()), 0.5 / float32(page.Bounds.Dy())}
//	uv := frame.UV.Inset(d)
//
// The inset keeps every filtered tap on the frame's own texels instead
// of reaching the texel boundary. Frames that rely on extrusion padding
// do not need the inset, the two techniques are equivalent alternatives.
func (uv UVRect) Inset(d UV) UVRect {
	return UVRect{
		Min: UV{uv.Min.U + d.U, uv.Min.V + d.V},
		Max: UV{uv.Max.U - d.U, uv.Max.V - d.V},
	}
}

// Page describes one texture page of a multi-page atlas.
type Page struct {
	// Image is the rendered page image.
	Image image.Image

	// Bounds is the pixel rectangle of the page in page-local
	// coordinates. It is typically image.Rect(0, 0, w, h) but may
	// carry a non-zero, including negative, origin when the page
	// represents a sub-region of a larger texture. Frame UVs are
	// normalized relative to this rectangle, not to the origin.
	Bounds image.Rectangle
}

// Options controls atlas construction in ReadWith.
type Options struct {
	// Padding is the number of transparent pixels reserved around
	// every frame. The padding ring is filled by extruding the
	// frame's border texels so that linear filtering cannot sample
	// neighboring frames ("page bleeding"). Padding defaults to zero
	// and the zero value reproduces the compact atlas of Read.
	Padding int

	// MaxPageWidth and MaxPageHeight cap the size of a single page.
	// Frames that do not fit start a new page, so the atlas may span
	// multiple pages. A zero or negative value means that the axis is
	// uncapped and all frames are packed onto a single page. A frame
	// larger than the cap grows the page instead of overflowing it.
	MaxPageWidth  int
	MaxPageHeight int
}

// framePlacement records where one frame landed in the atlas.
// Rectangles are page-local pixel coordinates.
type framePlacement struct {
	page    int
	content image.Rectangle
}

// shelfPage is a running shelf-packer state for one page.
type shelfPage struct {
	bounds            image.Rectangle
	x, y, shelfHeight int
}

func (p *shelfPage) place(width, height int) image.Rectangle {
	r := image.Rect(p.x, p.y, p.x+width, p.y+height)
	p.x = r.Max.X

	if height > p.shelfHeight {
		p.shelfHeight = height
	}

	if r.Max.X > p.bounds.Max.X {
		p.bounds.Max.X = r.Max.X
	}

	if r.Max.Y > p.bounds.Max.Y {
		p.bounds.Max.Y = r.Max.Y
	}

	return r
}

func (p *shelfPage) newShelf() {
	p.x = 0
	p.y += p.shelfHeight
	p.shelfHeight = 0
}

// packCells shelf-packs cell rectangles onto pages capped by maxWidth
// and maxHeight. Cells never leave their page bounds: an oversized
// cell grows the page it lands on instead of overflowing.
func packCells(cells []image.Point, maxWidth, maxHeight int) ([]framePlacement, []image.Rectangle) {
	placements := make([]framePlacement, 0, len(cells))
	pageBounds := make([]image.Rectangle, 0, 1)

	var page *shelfPage

	for _, cell := range cells {
		if cell.X <= 0 || cell.Y <= 0 {
			placements = append(placements, framePlacement{
				page:    len(pageBounds),
				content: image.Rectangle{},
			})
			continue
		}

		if page == nil {
			page = &shelfPage{}
		}

		// Wrap to a new shelf when the cell no longer fits the current row.
		if page.x > 0 && page.x+cell.X > maxWidth {
			page.newShelf()
		}

		// Wrap to a new page when the cell no longer fits the current shelf.
		if (page.x > 0 || page.y > 0) && page.y+cell.Y > maxHeight {
			pageBounds = append(pageBounds, page.bounds)
			page = &shelfPage{}
		}

		cellRect := page.place(cell.X, cell.Y)
		placements = append(placements, framePlacement{
			page:    len(pageBounds),
			content: cellRect,
		})
	}

	if page != nil {
		pageBounds = append(pageBounds, page.bounds)
	}

	return placements, pageBounds
}

// packFrames assigns every frame a page-local content rectangle.
// The input order is preserved: placements[i] belongs to sizes[i]
// and frames stay in frame order on every page. Frames with a zero
// size receive an empty placement and consume no page space.
//
// With no page cap the frames are arranged on a single page using the
// historical power-of-two grid, so a zero Options value reproduces the
// exact legacy layout. Otherwise frames are shelf-packed across pages.
func packFrames(sizes []image.Point, opts Options) ([]framePlacement, []image.Rectangle) {
	padding := opts.Padding

	if opts.MaxPageWidth <= 0 || opts.MaxPageHeight <= 0 {
		width, height := 0, 0
		for _, size := range sizes {
			if size.X > 0 && size.Y > 0 {
				width, height = size.X, size.Y
				break
			}
		}

		gridW, gridH := factorPowerOfTwo(len(sizes))
		if width > height {
			gridW, gridH = gridH, gridW
		}

		pitchX, pitchY := width+2*padding, height+2*padding
		placements := make([]framePlacement, len(sizes))
		pageBounds := image.Rect(0, 0, gridW*pitchX, gridH*pitchY)

		for i, size := range sizes {
			if size.X <= 0 || size.Y <= 0 {
				continue // empty frame: zero-value placement, no page space
			}
			col, row := i%gridW, i/gridW
			cell := image.Rect(
				col*pitchX, row*pitchY,
				col*pitchX+size.X+2*padding,
				row*pitchY+size.Y+2*padding,
			)
			placements[i] = framePlacement{page: 0, content: cell.Inset(padding)}
		}

		return placements, []image.Rectangle{pageBounds}
	}

	cells := make([]image.Point, len(sizes))
	for i, size := range sizes {
		if size.X <= 0 || size.Y <= 0 {
			cells[i] = image.Point{} // empty frame: skipped by the packer
			continue
		}
		cells[i] = image.Pt(size.X+2*padding, size.Y+2*padding)
	}

	placements, pageBounds := packCells(cells, opts.MaxPageWidth, opts.MaxPageHeight)

	for i := range placements {
		if sizes[i].X <= 0 || sizes[i].Y <= 0 {
			placements[i] = framePlacement{}
			continue
		}
		placements[i].content = placements[i].content.Inset(padding)
	}

	return placements, pageBounds
}

// FrameUV converts a page-local pixel rectangle r into the half-open
// normalized UV rectangle within page.
//
// Coordinates are first made relative to page.Min, so pages with a
// negative texture origin are handled correctly. Non-square pages are
// normalized per axis by page.Dx and page.Dy independently. The result
// is clamped to nothing: callers must pass a rectangle inside page.
// An empty page or an empty r yields the zero UVRect.
func FrameUV(page, r image.Rectangle) UVRect {
	width, height := page.Dx(), page.Dy()

	if width <= 0 || height <= 0 || r.Empty() {
		return UVRect{}
	}

	normalize := func(coord, origin, size int) float32 {
		return float32(coord-origin) / float32(size)
	}

	return UVRect{
		Min: UV{
			U: normalize(r.Min.X, page.Min.X, width),
			V: normalize(r.Min.Y, page.Min.Y, height),
		},
		Max: UV{
			U: normalize(r.Max.X, page.Min.X, width),
			V: normalize(r.Max.Y, page.Min.Y, height),
		},
	}
}
