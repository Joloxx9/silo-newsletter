// Package images re-encodes Silo artwork (WebP) as email-safe JPEG.
package images

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// maxPixels guards against decompression bombs.
const maxPixels = 40_000_000

// ToJPEG decodes WebP, JPEG, or PNG data and returns a JPEG no wider than
// maxWidth, flattening transparency onto the newsletter background.
func ToJPEG(data []byte, maxWidth int) ([]byte, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width*config.Height > maxPixels {
		return nil, errors.New("image dimensions are not acceptable")
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if maxWidth > 0 && width > maxWidth {
		height = height * maxWidth / width
		width = maxWidth
	}
	target := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(target, target.Bounds(), &image.Uniform{C: color.RGBA{0x14, 0x16, 0x1d, 0xff}}, image.Point{}, draw.Src)
	draw.CatmullRom.Scale(target, target.Bounds(), source, bounds, draw.Over, nil)
	var output bytes.Buffer
	if err := jpeg.Encode(&output, target, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
