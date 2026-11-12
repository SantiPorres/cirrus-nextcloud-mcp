package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"golang.org/x/image/bmp"
)

// picture draws a w x h image whose left half is red and right half blue.
func picture(w, h int, alpha uint8) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{R: 255, A: alpha}
			if x >= w/2 {
				c = color.NRGBA{B: 255, A: alpha}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// withOrientation inserts an EXIF segment with the given orientation after the JPEG start marker.
func withOrientation(data []byte, orientation uint16, order binary.ByteOrder) []byte {
	tiff := make([]byte, 8+2+12+4)
	if order == binary.LittleEndian {
		copy(tiff, "II")
	} else {
		copy(tiff, "MM")
	}
	order.PutUint16(tiff[2:], 42)
	order.PutUint32(tiff[4:], 8)
	order.PutUint16(tiff[8:], 1)
	order.PutUint16(tiff[10:], 0x0112)
	order.PutUint16(tiff[12:], 3)
	order.PutUint32(tiff[14:], 1)
	order.PutUint16(tiff[18:], orientation)
	payload := append([]byte("Exif\x00\x00"), tiff...)
	segment := []byte{0xff, 0xe1, 0, 0}
	binary.BigEndian.PutUint16(segment[2:], uint16(len(payload)+2))
	out := append([]byte{0xff, 0xd8}, segment...)
	out = append(out, payload...)
	return append(out, data[2:]...)
}

func decode(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func isRed(c color.Color) bool {
	r, _, b, _ := c.RGBA()
	return r > 0xa000 && b < 0x6000
}

func TestSmallWebImagesPassThroughUntouched(t *testing.T) {
	original := encodePNG(t, picture(40, 20, 255))
	result, err := Process(original, Options{MaxSide: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result.Data, original) || result.MIMEType != "image/png" || result.Resized || result.Width != 40 || result.Height != 20 || result.Format != "png" {
		t.Errorf("result = %+v", result)
	}
}

func TestLargeImagesAreScaledDown(t *testing.T) {
	result, err := Process(encodePNG(t, picture(400, 100, 255)), Options{MaxSide: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resized || result.Width != 100 || result.Height != 25 || result.OriginalWidth != 400 || result.MIMEType != "image/jpeg" {
		t.Errorf("result = %+v", result)
	}
	scaled := decode(t, result.Data)
	if scaled.Bounds().Dx() != 100 || !isRed(scaled.At(10, 10)) || isRed(scaled.At(90, 10)) {
		t.Error("the scaled picture does not look like the original")
	}

	tall, err := Process(encodePNG(t, picture(50, 1000, 255)), Options{MaxSide: 100})
	if err != nil || tall.Width != 5 || tall.Height != 100 {
		t.Errorf("tall = %+v %v", tall, err)
	}
}

func TestTransparencyIsKeptAsPNG(t *testing.T) {
	result, err := Process(encodePNG(t, picture(300, 300, 128)), Options{MaxSide: 100})
	if err != nil || result.MIMEType != "image/png" || result.Width != 100 {
		t.Fatalf("result = %+v %v", result, err)
	}
	if _, _, _, alpha := decode(t, result.Data).At(5, 5).RGBA(); alpha == 0xffff {
		t.Error("transparency was lost")
	}
	preferred, err := Process(encodePNG(t, picture(300, 300, 255)), Options{MaxSide: 100, PreferPNG: true})
	if err != nil || preferred.MIMEType != "image/png" {
		t.Errorf("PreferPNG = %+v %v", preferred, err)
	}
}

func TestOtherFormatsAreConverted(t *testing.T) {
	var buf bytes.Buffer
	if err := bmp.Encode(&buf, picture(20, 10, 255)); err != nil {
		t.Fatal(err)
	}
	result, err := Process(buf.Bytes(), Options{MaxSide: 100})
	if err != nil || result.Format != "bmp" || result.MIMEType != "image/jpeg" || result.Resized || result.Width != 20 {
		t.Errorf("bmp = %+v %v", result, err)
	}

	buf.Reset()
	paletted := image.NewPaletted(image.Rect(0, 0, 8, 8), color.Palette{color.White, color.Black})
	if err := gif.Encode(&buf, paletted, nil); err != nil {
		t.Fatal(err)
	}
	result, err = Process(buf.Bytes(), Options{MaxSide: 100})
	if err != nil || result.MIMEType != "image/gif" || !bytes.Equal(result.Data, buf.Bytes()) {
		t.Errorf("gif = %+v %v", result, err)
	}
}

func TestExifOrientationIsApplied(t *testing.T) {
	// The stored pixels are 40x20 with red on the left.
	base := encodeJPEG(t, picture(40, 20, 255))
	if exifOrientation(base) != 1 {
		t.Error("a JPEG without EXIF has orientation 1")
	}
	cases := []struct {
		orientation   uint16
		width, height int
		redX, redY    int // a point that must be red once upright
	}{
		{1, 40, 20, 5, 10},
		{2, 40, 20, 35, 10}, // mirrored
		{3, 40, 20, 35, 10}, // rotated 180
		{6, 20, 40, 10, 5},  // rotate 90 clockwise: left edge goes to the top
		{8, 20, 40, 10, 35}, // rotate 90 counter-clockwise: left edge goes to the bottom
	}
	for _, c := range cases {
		for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
			data := withOrientation(base, c.orientation, order)
			if got := exifOrientation(data); got != int(c.orientation) {
				t.Fatalf("orientation %d read as %d", c.orientation, got)
			}
			result, err := Process(data, Options{MaxSide: 100})
			if err != nil {
				t.Fatal(err)
			}
			if result.Width != c.width || result.Height != c.height || result.OriginalWidth != c.width {
				t.Errorf("orientation %d: %dx%d", c.orientation, result.Width, result.Height)
			}
			if !isRed(decode(t, result.Data).At(c.redX, c.redY)) {
				t.Errorf("orientation %d: the picture is not upright", c.orientation)
			}
		}
	}
}

func TestBadInput(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("plain text"), []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), {0xff, 0xd8, 0xff}} {
		if _, err := Process(data, Options{MaxSide: 100}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%q: expected ErrUnsupported, got %v", data, err)
		}
	}
	// A PNG header that announces a gigantic image must be refused before decoding.
	huge := encodePNG(t, picture(1, 1, 255))
	binary.BigEndian.PutUint32(huge[16:], 60000)
	binary.BigEndian.PutUint32(huge[20:], 60000)
	binary.BigEndian.PutUint32(huge[29:], crc32.ChecksumIEEE(huge[12:29]))
	if _, err := Process(huge, Options{MaxSide: 100}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("expected ErrTooLarge, got %v", err)
	}
	// Truncated EXIF data must not panic.
	for cut := 2; cut < 40; cut++ {
		_ = exifOrientation(withOrientation(encodeJPEG(t, picture(4, 4, 255)), 6, binary.BigEndian)[:cut])
	}
}
