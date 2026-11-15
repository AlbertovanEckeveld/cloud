package identity

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // register the PNG decoder for image.Decode
)

const (
	avatarSize      = 256              // pixels, square
	maxAvatarUpload = 5 << 20          // bytes
	maxAvatarSide   = 8000             // pixels
	maxAvatarPixels = 40 * 1000 * 1000 // decoded size cap, against decompression bombs
)

// ErrInvalidImage is returned for uploads that are not a usable PNG or JPEG.
var ErrInvalidImage = errors.New("invalid image")

// processAvatar turns an uploaded PNG or JPEG into a square 256×256 JPEG.
// Re-encoding drops all metadata, including EXIF data such as GPS position.
func processAvatar(data []byte) ([]byte, error) {
	if len(data) > maxAvatarUpload {
		return nil, ErrInvalidImage
	}
	// Check the declared size before decoding: a tiny file can claim
	// billions of pixels and exhaust memory when decoded.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") ||
		cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxAvatarSide || cfg.Height > maxAvatarSide ||
		cfg.Width*cfg.Height > maxAvatarPixels {
		return nil, ErrInvalidImage
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrInvalidImage
	}

	// Centre crop to a square, then scale down by averaging each block of
	// source pixels (a box filter), which avoids aliasing when shrinking.
	b := src.Bounds()
	side := min(b.Dx(), b.Dy())
	x0, y0 := b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2
	out := image.NewRGBA(image.Rect(0, 0, avatarSize, avatarSize))
	for y := range avatarSize {
		sy0, sy1 := y0+y*side/avatarSize, y0+max((y+1)*side/avatarSize, y*side/avatarSize+1)
		for x := range avatarSize {
			sx0, sx1 := x0+x*side/avatarSize, x0+max((x+1)*side/avatarSize, x*side/avatarSize+1)
			var r, g, bl, n uint64
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					cr, cg, cb, _ := src.At(sx, sy).RGBA()
					r, g, bl, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), n+1
				}
			}
			out.Set(x, y, color.RGBA64{R: uint16(r / n), G: uint16(g / n), B: uint16(bl / n), A: 0xffff})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
