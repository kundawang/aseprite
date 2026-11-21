package aseprite

import (
	"errors"
	"image"
	"image/color"
	"io"
)

// Read decodes an Aseprite image from r.
// The atlas uses a single page with no padding.
func Read(r io.Reader) (*Aseprite, error) {
	return ReadWith(r, Options{})
}

// ReadWith decodes an Aseprite image from r using opts to control the
// atlas layout. opts.Padding adds extruded padding around every frame
// and opts.MaxPageWidth/opts.MaxPageHeight enable multi-page packing.
func ReadWith(r io.Reader, opts Options) (*Aseprite, error) {
	if opts.Padding < 0 {
		return nil, errors.New("aseprite: padding must not be negative")
	}

	var spr Aseprite
	if err := spr.readFrom(r, opts); err != nil {
		return nil, err
	}

	return &spr, nil
}

// Decode decodes an Aseprite image from r and returns it as an image.Image.
func Decode(r io.Reader) (image.Image, error) {
	return Read(r)
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
