// Package images re-encodes artwork (WebP, JPEG, PNG) as email-safe JPEG or PNG.
package images

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// maxPixels guards against decompression bombs.
const maxPixels = 40_000_000

// Poster size: every poster is cropped to the same 2:3 shape, so posters in
// a grid row line up whatever the source artwork's proportions.
const (
	PosterWidth  = 300
	PosterHeight = 450
)

func decode(data []byte) (image.Image, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width*config.Height > maxPixels {
		return nil, errors.New("image dimensions are not acceptable")
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	return source, err
}

// ToPoster decodes WebP, JPEG, or PNG data, crops its center to 2:3, and
// returns a PosterWidth x PosterHeight JPEG, flattening transparency onto the
// newsletter background.
func ToPoster(data []byte) ([]byte, error) {
	source, err := decode(data)
	if err != nil {
		return nil, err
	}
	crop := source.Bounds()
	width, height := crop.Dx(), crop.Dy()
	if width*PosterHeight > height*PosterWidth {
		// Too wide: keep the middle columns.
		keep := height * PosterWidth / PosterHeight
		crop.Min.X += (width - keep) / 2
		crop.Max.X = crop.Min.X + keep
	} else {
		keep := width * PosterHeight / PosterWidth
		crop.Min.Y += (height - keep) / 2
		crop.Max.Y = crop.Min.Y + keep
	}
	target := image.NewRGBA(image.Rect(0, 0, PosterWidth, PosterHeight))
	draw.Draw(target, target.Bounds(), &image.Uniform{C: color.RGBA{0x14, 0x16, 0x1d, 0xff}}, image.Point{}, draw.Src)
	draw.CatmullRom.Scale(target, target.Bounds(), source, crop, draw.Over, nil)
	var output bytes.Buffer
	if err := jpeg.Encode(&output, target, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// ToPNG decodes WebP, JPEG, or PNG data and returns a PNG no wider than
// maxWidth, keeping transparency (for logos on any background).
func ToPNG(data []byte, maxWidth int) ([]byte, error) {
	source, err := decode(data)
	if err != nil {
		return nil, err
	}
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if maxWidth > 0 && width > maxWidth {
		height = height * maxWidth / width
		width = maxWidth
	}
	target := image.NewNRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(target, target.Bounds(), source, bounds, draw.Src, nil)
	var output bytes.Buffer
	if err := png.Encode(&output, target); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
