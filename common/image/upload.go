package image

import (
	"bytes"
	"errors"
	"fmt"
	stdimage "image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
)

const (
	// MaxUploadBytes is the largest encoded image accepted by the API.
	MaxUploadBytes int64 = 10 << 20
	// MaxImagePixels bounds the decoded allocation before image.Decode is called.
	MaxImagePixels int64 = 25_000_000
	// MaxImageDimension rejects malformed headers with unreasonable dimensions.
	MaxImageDimension = 10_000
	maxAspectRatio    = 50
)

var (
	ErrEmptyImage       = errors.New("uploaded image is empty")
	ErrImageTooLarge    = errors.New("uploaded image is too large")
	ErrUnsupportedImage = errors.New("uploaded image format is unsupported")
	ErrInvalidImage     = errors.New("uploaded image is invalid")
	ErrImageDimensions  = errors.New("uploaded image dimensions are not supported")
)

// ValidateUpload checks encoded size, actual format, and decoded dimensions
// without allocating the full decoded image.
func ValidateUpload(buffer []byte) error {
	if len(buffer) == 0 {
		return ErrEmptyImage
	}
	if int64(len(buffer)) > MaxUploadBytes {
		return ErrImageTooLarge
	}

	if !supportedMediaType(http.DetectContentType(buffer)) {
		return ErrUnsupportedImage
	}
	config, format, err := stdimage.DecodeConfig(bytes.NewReader(buffer))
	if err != nil {
		return ErrInvalidImage
	}
	if !supportedFormat(format) {
		return ErrUnsupportedImage
	}
	if config.Width <= 0 || config.Height <= 0 {
		return ErrImageDimensions
	}
	if config.Width > MaxImageDimension || config.Height > MaxImageDimension {
		return fmt.Errorf("%w: width and height must not exceed %d pixels", ErrImageDimensions, MaxImageDimension)
	}

	width, height := int64(config.Width), int64(config.Height)
	if width*height > MaxImagePixels {
		return fmt.Errorf("%w: decoded image must not exceed %d pixels", ErrImageDimensions, MaxImagePixels)
	}
	longEdge, shortEdge := width, height
	if longEdge < shortEdge {
		longEdge, shortEdge = shortEdge, longEdge
	}
	if longEdge > shortEdge*maxAspectRatio {
		return fmt.Errorf("%w: aspect ratio must not exceed %d:1", ErrImageDimensions, maxAspectRatio)
	}
	return nil
}

// DecodeUpload validates and fully decodes an uploaded image. Callers can pass
// the returned image directly to a recognizer so malformed pixel data is
// rejected before any model is initialized.
func DecodeUpload(buffer []byte) (stdimage.Image, error) {
	if err := ValidateUpload(buffer); err != nil {
		return nil, err
	}
	decoded, _, err := stdimage.Decode(bytes.NewReader(buffer))
	if err != nil {
		return nil, ErrInvalidImage
	}
	return decoded, nil
}

func IsUploadValidationError(err error) bool {
	return errors.Is(err, ErrEmptyImage) ||
		errors.Is(err, ErrImageTooLarge) ||
		errors.Is(err, ErrUnsupportedImage) ||
		errors.Is(err, ErrInvalidImage) ||
		errors.Is(err, ErrImageDimensions)
}

func supportedMediaType(mediaType string) bool {
	switch mediaType {
	case "image/jpeg", "image/png", "image/gif":
		return true
	default:
		return false
	}
}

func supportedFormat(format string) bool {
	switch format {
	case "jpeg", "png", "gif":
		return true
	default:
		return false
	}
}
