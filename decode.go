package aseprite

import (
	"image"
	"image/color"
	"io"
)

// Read decodes an Aseprite image from r.
func Read(r io.Reader) (*Aseprite, error) {
	return ReadWithOptions(r, Options{})
}

// Decode decodes an Aseprite image from r and returns it as an image.Image.
func Decode(r io.Reader) (image.Image, error) {
	return Read(r)
}

// DecodeWithOptions decodes an Aseprite image from r and returns it as an
// image.Image, using opts to control the unknown chunk audit.
func DecodeWithOptions(r io.Reader, opts Options) (image.Image, error) {
	return ReadWithOptions(r, opts)
}

// DecodeConfig returns the color model and dimensions of an Aseprite image
// without decoding the entire image.
func DecodeConfig(r io.Reader) (image.Config, error) {
	return DecodeConfigWithOptions(r, Options{})
}

// DecodeConfigWithOptions is like DecodeConfig but applies opts. The audit
// options are validated exactly as during a full decode, so configuration and
// decode agree on which files are rejected.
func DecodeConfigWithOptions(r io.Reader, opts Options) (image.Config, error) {
	var f file

	if _, err := f.ReadFromWithOptions(r, opts); err != nil {
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

func init() {
	image.RegisterFormat("aseprite", "????\xE0\xA5", Decode, DecodeConfig)
}
