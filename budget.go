package aseprite

import (
	"image"
	"image/color"
	"io"
)

// hardResourceCap is an unconditional ceiling applied even when no budget is
// configured. It is far above any legitimate sprite file but small enough that
// a malformed length field cannot trigger a multi-gigabyte allocation. Callers
// that need a tighter limit set the corresponding Budget field.
const hardResourceCap int64 = 64 << 20 // 64 MiB

// Budget caps the resources that decoding an Aseprite file may consume.
//
// A Budget is optional. The zero value disables every limit and preserves the
// behavior of the unbudgeted decoders. A field set to a positive value enables
// the corresponding limit; zero and negative values mean unlimited.
//
// The limits are enforced while the file is being parsed, as early as the
// relevant information becomes available, so an over-budget or malformed file
// is rejected before the offending memory is allocated.
//
// FileBytes and ChunkLength measure bytes as they appear in the input stream
// (compressed). CelBytes measures the size of cel pixel data after zlib
// decompression, so a compression bomb cannot inflate past its budget.
type Budget struct {
	// MaxFileBytes is the maximum declared file size in bytes.
	MaxFileBytes int64

	// MaxFrames is the maximum number of frames.
	MaxFrames int

	// MaxLayers is the maximum number of layers.
	MaxLayers int

	// MaxPixels is the maximum declared canvas size measured in pixels,
	// that is frame width multiplied by frame height.
	MaxPixels int64

	// MaxChunkLength is the maximum declared length of a single chunk in bytes.
	MaxChunkLength int

	// MaxCelBytes is the maximum total number of decompressed cel pixel bytes.
	// The counter accumulates across every cel of every frame and is checked
	// before each decompressed buffer is allocated.
	MaxCelBytes int64
}

// Option configures a budgeted decoder.
type Option func(*decodeOptions)

type decodeOptions struct {
	budget Budget
}

// WithBudget returns an Option that enforces b while decoding.
// The zero Budget leaves all limits disabled.
func WithBudget(b Budget) Option {
	return func(o *decodeOptions) {
		o.budget = b
	}
}

// LimitError reports that an input file exceeded a resource budget.
//
// Offset is the byte position in the input stream where the limit was
// detected, or -1 when the position is not associated with a stream offset.
type LimitError struct {
	// Limit names the budget field that was exceeded.
	Limit string

	// Value is the amount that the file requested.
	Value int64

	// Budget is the configured limit.
	Budget int64

	// Offset is the byte position in the input stream, or -1.
	Offset int64

	// Position is a human readable location within the structure.
	Position string
}

func (e *LimitError) Error() string {
	if e.Position != "" {
		return "aseprite: " + e.Limit + " exceeded at " + e.Position +
			": " + itoa(e.Value) + " > " + itoa(e.Budget)
	}
	return "aseprite: " + e.Limit + " exceeded: " +
		itoa(e.Value) + " > " + itoa(e.Budget)
}

// Is reports whether the target is any LimitError. Limits are matched by
// type so callers can detect budget rejection with errors.Is(err, &LimitError{}).
func (e *LimitError) Is(target error) bool {
	_, ok := target.(*LimitError)
	return ok
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// syntaxError is a malformed-file error located at a stream offset.
type syntaxError struct {
	msg    string
	offset int64
}

func (e *syntaxError) Error() string {
	if e.offset < 0 {
		return "aseprite: " + e.msg
	}
	return "aseprite: " + e.msg + " at offset " + itoa(e.offset)
}

func checkBudget(budget int64, name, position string, value, offset int64) error {
	// The hard cap always applies, so the zero (unlimited) Budget still cannot
	// be coaxed into an unbounded allocation by attacker-controlled lengths.
	if value > hardResourceCap {
		return &LimitError{
			Limit:    name,
			Value:    value,
			Budget:   hardResourceCap,
			Offset:   offset,
			Position: position,
		}
	}
	if budget > 0 && value > budget {
		return &LimitError{
			Limit:    name,
			Value:    value,
			Budget:   budget,
			Offset:   offset,
			Position: position,
		}
	}
	return nil
}

// Read decodes an Aseprite image from r.
//
// Without options this is identical to the package-level Read function. Pass
// WithBudget to enforce a resource budget.
func Read(r io.Reader, opts ...Option) (*Aseprite, error) {
	var spr Aseprite
	if err := spr.readFrom(r, opts...); err != nil {
		return nil, err
	}
	return &spr, nil
}

// Decode decodes an Aseprite image from r and returns it as an image.Image.
//
// Without options this is identical to the package-level Decode function.
// Pass WithBudget to enforce a resource budget.
func Decode(r io.Reader, opts ...Option) (image.Image, error) {
	return Read(r, opts...)
}

// DecodeConfig returns the color model and dimensions of an Aseprite image
// without decoding the entire image.
//
// Without options this is identical to the package-level DecodeConfig
// function. Pass WithBudget to enforce a resource budget.
func DecodeConfig(r io.Reader, opts ...Option) (image.Config, error) {
	o := decodeOptions{}
	for _, opt := range opts {
		opt(&o)
	}

	var f file
	if _, err := f.readFile(r, o.budget); err != nil {
		return image.Config{}, err
	}

	fw, fh := factorPowerOfTwo(len(f.frames))
	if f.framew > f.frameh {
		fw, fh = fh, fw
	}

	var colorModel color.Model

	switch f.bpp {
	case 8:
		if err := f.initPalette(); err != nil {
			return image.Config{}, err
		}
		colorModel = f.palette
	case 16:
		colorModel = color.Gray16Model
	default:
		colorModel = color.RGBAModel
	}

	return image.Config{
		ColorModel: colorModel,
		Width:      f.framew * fw,
		Height:     f.frameh * fh,
	}, nil
}
