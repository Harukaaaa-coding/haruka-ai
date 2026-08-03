//go:build !onnx

package image

import (
	"errors"
	"image"
)

var errONNXDisabled = errors.New("image recognition is unavailable: rebuild with -tags onnx and enable CGO")

// ImageRecognizer is a build-time stub used when the optional ONNX feature is disabled.
type ImageRecognizer struct{}

func NewImageRecognizer(string, string, int, int) (*ImageRecognizer, error) {
	return nil, errONNXDisabled
}

func (r *ImageRecognizer) Close() {}

func (r *ImageRecognizer) PredictFromFile(string) (string, error) {
	return "", errONNXDisabled
}

func (r *ImageRecognizer) PredictFromBuffer([]byte) (string, error) {
	return "", errONNXDisabled
}

func (r *ImageRecognizer) PredictFromImage(image.Image) (string, error) {
	return "", errONNXDisabled
}
