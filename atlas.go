package aseprite

import (
	"errors"
	"fmt"
	"image"
)

// AtlasOptions configures how frames are packed into texture atlas pages.
// The zero value selects the legacy single-atlas layout and is fully
// backward compatible: one page, no padding, no size limits.
type AtlasOptions struct {
	// MaxWidth and MaxHeight limit the size of each atlas page in pixels,
	// e.g. 1024 for mobile GPUs. Both must be positive when any other
	// field is set.
	MaxWidth  int
	MaxHeight int

	// MaxPages limits the number of atlas pages. Zero means a single page.
	MaxPages int

	// Padding is the number of transparent pixels added around every frame
	// to prevent texture bleeding.
	Padding int
}

// Packing errors returned by ReadWithOptions. Use errors.Is to match them;
// the wrapped messages carry the offending dimensions for diagnosis.
var (
	ErrFrameTooLarge   = errors.New("aseprite: frame does not fit in atlas page")
	ErrPaddingTooLarge = errors.New("aseprite: padding too large for atlas page")
	ErrTooManyPages    = errors.New("aseprite: frames do not fit in the page limit")
	ErrAtlasOverflow   = errors.New("aseprite: atlas dimensions overflow")
	ErrInvalidOptions  = errors.New("aseprite: invalid atlas options")
)

// isZero reports whether o selects the legacy single-atlas layout.
func (o AtlasOptions) isZero() bool {
	return o.MaxWidth == 0 && o.MaxHeight == 0 && o.MaxPages == 0 && o.Padding == 0
}

func (o AtlasOptions) validate() error {
	if o.MaxWidth <= 0 || o.MaxHeight <= 0 {
		return fmt.Errorf("%w: MaxWidth and MaxHeight must be positive, got %dx%d",
			ErrInvalidOptions, o.MaxWidth, o.MaxHeight)
	}
	if o.MaxPages < 0 {
		return fmt.Errorf("%w: MaxPages must not be negative, got %d",
			ErrInvalidOptions, o.MaxPages)
	}
	if o.Padding < 0 {
		return fmt.Errorf("%w: Padding must not be negative, got %d",
			ErrInvalidOptions, o.Padding)
	}
	return nil
}

// packFrames lays out nframes frames of framew x frameh pixels on one or
// more atlas pages. Frames are placed in order, row by row, page by page,
// so the layout is stable for a given input. It returns the bounds of each
// page (trimmed to the used area), the bounds of every frame within its
// page, and the page index of every frame. Nothing is ever clipped: any
// frame that does not fit produces an error.
func packFrames(nframes, framew, frameh int, opts AtlasOptions) (pages, framesr []image.Rectangle, pageOf []int, err error) {
	if err = opts.validate(); err != nil {
		return nil, nil, nil, err
	}
	if framew <= 0 || frameh <= 0 {
		return nil, nil, nil, fmt.Errorf("%w: frame size must be positive, got %dx%d",
			ErrInvalidOptions, framew, frameh)
	}

	const maxInt = int64(^uint(0) >> 1)

	pad := int64(opts.Padding)
	if pad > maxInt/2 {
		return nil, nil, nil, fmt.Errorf("%w: padding %d is too large", ErrAtlasOverflow, opts.Padding)
	}

	fw, fh := int64(framew), int64(frameh)
	maxw, maxh := int64(opts.MaxWidth), int64(opts.MaxHeight)

	// A page is allocated as width*height*4 bytes at most; reject limits
	// whose area cannot be represented before any multiplication overflows.
	if maxw > maxInt/4/maxh {
		return nil, nil, nil, fmt.Errorf("%w: page limit %dx%d is too large to allocate",
			ErrAtlasOverflow, opts.MaxWidth, opts.MaxHeight)
	}

	if availw, availh := maxw-2*pad, maxh-2*pad; fw > availw || fh > availh {
		if fw <= maxw && fh <= maxh {
			return nil, nil, nil, fmt.Errorf("%w: padding %d leaves only %dx%d of a %dx%d page for a %dx%d frame",
				ErrPaddingTooLarge, opts.Padding, availw, availh, opts.MaxWidth, opts.MaxHeight, framew, frameh)
		}
		return nil, nil, nil, fmt.Errorf("%w: frame is %dx%d but page is %dx%d",
			ErrFrameTooLarge, framew, frameh, opts.MaxWidth, opts.MaxHeight)
	}

	cellw, cellh := fw+pad, fh+pad
	cols := (maxw - pad) / cellw
	rows := (maxh - pad) / cellh
	perPage := cols * rows

	maxPages := opts.MaxPages
	if maxPages <= 0 {
		maxPages = 1
	}

	npages := int64(1)
	if nframes > 0 {
		npages = (int64(nframes) + perPage - 1) / perPage
	}
	if npages > int64(maxPages) {
		return nil, nil, nil, fmt.Errorf("%w: %d frames of %dx%d with padding %d need %d pages of %dx%d, limit is %d",
			ErrTooManyPages, nframes, framew, frameh, opts.Padding, npages, opts.MaxWidth, opts.MaxHeight, maxPages)
	}

	pages = make([]image.Rectangle, npages)
	framesr = make([]image.Rectangle, nframes)
	pageOf = make([]int, nframes)

	for p := int64(0); p < npages; p++ {
		count := perPage
		if rem := int64(nframes) - p*perPage; rem < count {
			count = rem
		}
		c, r := cols, (count+cols-1)/cols
		if count < c {
			c = count
		}
		pages[p] = image.Rect(0, 0, int(pad+c*cellw), int(pad+r*cellh))
	}

	for i := 0; i < nframes; i++ {
		idx := int64(i) % perPage
		col, row := idx%cols, idx/cols
		x, y := pad+col*cellw, pad+row*cellh
		framesr[i] = image.Rect(int(x), int(y), int(x+fw), int(y+fh))
		pageOf[i] = int(int64(i) / perPage)
	}

	return pages, framesr, pageOf, nil
}
