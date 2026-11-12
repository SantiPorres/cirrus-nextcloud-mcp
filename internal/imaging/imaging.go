// Package imaging prepares images for a model: it decodes the common
// formats, applies the EXIF orientation, scales large pictures down and
// re-encodes them compactly. It is pure Go and has no network access.
package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	_ "image/gif" // registers the GIF decoder
	"image/jpeg"
	"image/png"

	_ "golang.org/x/image/bmp" // registers the BMP decoder
	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff" // registers the TIFF decoder
	_ "golang.org/x/image/webp" // registers the WebP decoder
)

// MaxPixels bounds the size of an image that will be decoded, to keep memory use predictable.
const MaxPixels = 80_000_000

// ErrUnsupported is returned when the data is not an image this package can decode.
var ErrUnsupported = errors.New("unsupported image format")

// ErrTooLarge is returned when the image has more than MaxPixels pixels.
var ErrTooLarge = errors.New("image has too many pixels")

// Result is a processed image.
type Result struct {
	Data     []byte
	MIMEType string
	// Width and Height are the dimensions of Data.
	Width, Height int
	// OriginalWidth and OriginalHeight are the dimensions of the input as displayed.
	OriginalWidth, OriginalHeight int
	// Format is the input format: "jpeg", "png", "gif", "webp", "bmp" or "tiff".
	Format  string
	Resized bool
}

// Options controls Process.
type Options struct {
	// MaxSide is the largest allowed width or height, in pixels.
	MaxSide int
	// PreferPNG keeps opaque images as PNG instead of JPEG (better for text and line art).
	PreferPNG bool
}

var passthroughMIME = map[string]string{"jpeg": "image/jpeg", "png": "image/png", "gif": "image/gif", "webp": "image/webp"}

// Process returns the image ready to be sent: unchanged when it is already a
// web format within MaxSide, otherwise oriented, scaled down and re-encoded
// (JPEG for opaque pictures, PNG when there is transparency).
func Process(data []byte, opts Options) (Result, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Result{}, ErrUnsupported
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width*config.Height > MaxPixels {
		return Result{}, ErrTooLarge
	}
	orientation := 1
	if format == "jpeg" {
		orientation = exifOrientation(data)
	}
	width, height := config.Width, config.Height
	if orientation >= 5 {
		width, height = height, width
	}
	result := Result{Format: format, OriginalWidth: width, OriginalHeight: height, Width: width, Height: height}

	fits := width <= opts.MaxSide && height <= opts.MaxSide
	if mime, ok := passthroughMIME[format]; ok && fits && orientation == 1 {
		result.Data, result.MIMEType = data, mime
		return result, nil
	}

	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Result{}, ErrUnsupported
	}
	decoded = orient(decoded, orientation)
	if !fits {
		scale := float64(opts.MaxSide) / float64(max(width, height))
		target := image.Rect(0, 0, max(1, int(float64(width)*scale+0.5)), max(1, int(float64(height)*scale+0.5)))
		scaled := image.NewNRGBA(target)
		draw.CatmullRom.Scale(scaled, target, decoded, decoded.Bounds(), draw.Src, nil)
		decoded = scaled
		result.Resized = true
	}
	result.Width, result.Height = decoded.Bounds().Dx(), decoded.Bounds().Dy()

	var buffer bytes.Buffer
	if opts.PreferPNG || !opaque(decoded) {
		err = (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buffer, decoded)
		result.MIMEType = "image/png"
	} else {
		err = jpeg.Encode(&buffer, decoded, &jpeg.Options{Quality: 85})
		result.MIMEType = "image/jpeg"
	}
	if err != nil {
		return Result{}, err
	}
	result.Data = buffer.Bytes()
	return result, nil
}

func opaque(img image.Image) bool {
	if checker, ok := img.(interface{ Opaque() bool }); ok {
		return checker.Opaque()
	}
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if _, _, _, alpha := img.At(x, y).RGBA(); alpha != 0xffff {
				return false
			}
		}
	}
	return true
}

// orient applies an EXIF orientation (1-8) so the picture is upright.
func orient(src image.Image, orientation int) image.Image {
	if orientation < 2 || orientation > 8 {
		return src
	}
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	outW, outH := w, h
	if orientation >= 5 {
		outW, outH = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, outW, outH))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch orientation {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, color.NRGBAModel.Convert(src.At(bounds.Min.X+x, bounds.Min.Y+y)))
		}
	}
	return dst
}

// exifOrientation reads the orientation tag of a JPEG; 1 when absent or unreadable.
func exifOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return 1
	}
	for pos := 2; pos+4 <= len(data); {
		if data[pos] != 0xff {
			return 1
		}
		marker := data[pos+1]
		if marker == 0xda || marker == 0xd9 {
			return 1 // image data starts: no EXIF before it
		}
		length := int(binary.BigEndian.Uint16(data[pos+2:]))
		if length < 2 || pos+2+length > len(data) {
			return 1
		}
		segment := data[pos+4 : pos+2+length]
		if marker == 0xe1 && bytes.HasPrefix(segment, []byte("Exif\x00\x00")) {
			return tiffOrientation(segment[6:])
		}
		pos += 2 + length
	}
	return 1
}

func tiffOrientation(tiffData []byte) int {
	if len(tiffData) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(tiffData[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	offset := int(order.Uint32(tiffData[4:]))
	if offset < 8 || offset+2 > len(tiffData) {
		return 1
	}
	count := int(order.Uint16(tiffData[offset:]))
	for i := 0; i < count; i++ {
		entry := offset + 2 + i*12
		if entry+12 > len(tiffData) {
			return 1
		}
		if order.Uint16(tiffData[entry:]) == 0x0112 {
			if value := int(order.Uint16(tiffData[entry+8:])); value >= 1 && value <= 8 {
				return value
			}
			return 1
		}
	}
	return 1
}
