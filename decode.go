package aseprite

import (
	"image"
	"image/color"
	"io"
)

// Decode reads an Aseprite file from r and returns it as an image.Image.
func Decode(r io.Reader) (image.Image, error) {
	spr, err := Read(r)
	if err != nil {
		return nil, err
	}
	return &spr, nil
}

// DecodeConfig returns the color model and dimensions of an Aseprite image
// without building the atlas. The returned config describes exactly what
// Decode produces for the same input: Width and Height equal the decoded
// image's bounds and ColorModel equals its color model. Since frames are
// flattened onto an RGBA atlas, the model is color.RGBAModel.
func DecodeConfig(r io.Reader) (image.Config, error) {
	var f file

	if _, err := f.ReadFrom(r); err != nil {
		return image.Config{}, err
	}

	// Parse the palette even though the atlas is RGBA, so that a malformed
	// palette makes DecodeConfig fail for the same inputs Decode fails on.
	if err := f.initPalette(); err != nil {
		return image.Config{}, err
	}

	w, h, err := atlasDimensions(len(f.frames), f.framew, f.frameh)
	if err != nil {
		return image.Config{}, err
	}

	return image.Config{
		ColorModel: color.RGBAModel,
		Width:      w,
		Height:     h,
	}, nil
}

func init() {
	image.RegisterFormat("aseprite", "????\xE0\xA5", Decode, DecodeConfig)
}
