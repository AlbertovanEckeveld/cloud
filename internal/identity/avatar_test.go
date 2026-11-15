package identity

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		img.Set(x, 0, color.RGBA{R: 255, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestProcessAvatar(t *testing.T) {
	for name, input := range map[string][]byte{
		"landscape png": encodePNG(t, 1200, 400),
		"tiny png":      encodePNG(t, 10, 10),
	} {
		t.Run(name, func(t *testing.T) {
			out, err := processAvatar(input)
			if err != nil {
				t.Fatal(err)
			}
			img, format, err := image.Decode(bytes.NewReader(out))
			if err != nil || format != "jpeg" || img.Bounds().Dx() != avatarSize || img.Bounds().Dy() != avatarSize {
				t.Fatalf("output %s %v, err %v", format, img.Bounds(), err)
			}
		})
	}
}

func TestProcessAvatarStripsMetadata(t *testing.T) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 50, 50)), nil); err != nil {
		t.Fatal(err)
	}
	// Insert an EXIF segment (APP1) with a fake GPS marker after the SOI marker.
	exif := append([]byte("Exif\x00\x00"), []byte("GPS-52.0907,5.1214")...)
	seg := append([]byte{0xFF, 0xE1, byte((len(exif) + 2) >> 8), byte(len(exif) + 2)}, exif...)
	withExif := append(append([]byte{}, buf.Bytes()[:2]...), append(seg, buf.Bytes()[2:]...)...)

	out, err := processAvatar(withExif)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("Exif")) || bytes.Contains(out, []byte("GPS-52")) {
		t.Fatal("metadata survived re-encoding")
	}
}

func TestProcessAvatarRejects(t *testing.T) {
	gif := []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;")
	for name, input := range map[string][]byte{
		"not an image": []byte("<svg onload=alert(1)>"),
		"gif":          gif,
		"too wide":     encodePNG(t, maxAvatarSide+1, 1),
		"too large":    bytes.Repeat([]byte{0}, maxAvatarUpload+1),
		"truncated":    encodePNG(t, 100, 100)[:60],
	} {
		if _, err := processAvatar(input); !errors.Is(err, ErrInvalidImage) {
			t.Errorf("%s: err = %v, want ErrInvalidImage", name, err)
		}
	}
}
