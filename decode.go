package aseprite

import (
	"image"
	"image/color"
	"io"
)

// Options configures how layers are composited during decoding.
// The zero value reproduces the historical gamma-encoded compositing
// bit-for-bit.
type Options struct {
	// LinearLight blends and composites layers in linearized sRGB space
	// instead of gamma-encoded sRGB space. Semi-transparent bright layers
	// over dark backgrounds come out brighter, matching how the layers
	// would blend physically. Alpha is always treated as linear.
	LinearLight bool

	// PremultipliedAlpha treats layer colors as alpha-premultiplied when
	// blending: blend modes are applied to the premultiplied channels
	// as-is. Otherwise colors are treated as straight (non-premultiplied)
	// alpha and un-premultiplied before blending.
	PremultipliedAlpha bool
}

// DecodeAseprite decodes an Aseprite image from r.
func DecodeAseprite(r io.Reader) (*Aseprite, error) {
	return DecodeAsepriteWithOptions(r, Options{})
}

// DecodeAsepriteWithOptions decodes an Aseprite image from r using opts.
func DecodeAsepriteWithOptions(r io.Reader, opts Options) (*Aseprite, error) {
	var spr Aseprite
	if err := spr.readFrom(r, opts); err != nil {
		return nil, err
	}

	return &spr, nil
}

// Decode decodes an Aseprite image from r and returns it as an image.Image.
func Decode(r io.Reader) (image.Image, error) {
	return DecodeAseprite(r)
}

// DecodeConfig returns the color model and dimensions of an Aseprite image
// without decoding the entire image.
func DecodeConfig(r io.Reader) (image.Config, error) {
	var f file

	if _, err := f.ReadFrom(r); err != nil {
		return image.Config{}, err
	}

	fw, fh := factorPowerOfTwo(len(f.frames))
	if f.framew > f.frameh {
		fw, fh = fh, fw
	}

	var colorModel color.Model

	switch f.bpp {
	case 8:
		f.initPalette()
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

func init() {
	image.RegisterFormat("aseprite", "????\xE0\xA5", Decode, DecodeConfig)
}
