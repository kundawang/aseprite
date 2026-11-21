package aseprite

import (
	"image"
	"io"
)

// decode adapts the variadic, budget-aware Decode to the image.Format
// signature. Registered formats always run without a budget.
func decode(r io.Reader) (image.Image, error) {
	return Decode(r)
}

// decodeConfig adapts the variadic, budget-aware DecodeConfig to the
// image.Format signature. Registered formats always run without a budget.
func decodeConfig(r io.Reader) (image.Config, error) {
	return DecodeConfig(r)
}

func init() {
	image.RegisterFormat("aseprite", "????\xE0\xA5", decode, decodeConfig)
}
