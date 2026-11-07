package images

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestPostersOfAnyShapeComeOutTheSameSize(t *testing.T) {
	for _, size := range []image.Point{{1000, 400}, {300, 900}, {680, 1000}, {50, 75}} {
		var source bytes.Buffer
		_ = png.Encode(&source, image.NewRGBA(image.Rect(0, 0, size.X, size.Y)))
		poster, err := ToPoster(source.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		config, err := jpeg.DecodeConfig(bytes.NewReader(poster))
		if err != nil || config.Width != PosterWidth || config.Height != PosterHeight {
			t.Errorf("%v source: poster %dx%d, %v", size, config.Width, config.Height, err)
		}
	}
}
