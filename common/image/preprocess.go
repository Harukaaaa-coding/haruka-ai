package image

import (
	"fmt"
	"image"
	stddraw "image/draw"

	xdraw "golang.org/x/image/draw"
)

const (
	imageNetInputSize       = 224
	imageNetResizeShortEdge = 256
)

var (
	imageNetMean = [3]float32{0.485, 0.456, 0.406}
	imageNetStd  = [3]float32{0.229, 0.224, 0.225}
)

// preprocessImageNet applies the preprocessing used to validate the ONNX
// Model Zoo MobileNetV2 model: preserve aspect ratio while resizing the short
// edge to 256, take a centered 224x224 crop, then normalize RGB into a NCHW
// float32 tensor.
func preprocessImageNet(source image.Image) ([]float32, error) {
	cropped, err := resizeAndCenterCropImageNet(source)
	if err != nil {
		return nil, err
	}

	planeSize := imageNetInputSize * imageNetInputSize
	data := make([]float32, 3*planeSize)
	for y := 0; y < imageNetInputSize; y++ {
		for x := 0; x < imageNetInputSize; x++ {
			pixelOffset := cropped.PixOffset(x, y)
			outputOffset := y*imageNetInputSize + x
			for channel := 0; channel < 3; channel++ {
				value := float32(cropped.Pix[pixelOffset+channel]) / 255.0
				data[channel*planeSize+outputOffset] = (value - imageNetMean[channel]) / imageNetStd[channel]
			}
		}
	}
	return data, nil
}

func resizeAndCenterCropImageNet(source image.Image) (*image.NRGBA, error) {
	if source == nil {
		return nil, fmt.Errorf("preprocess ImageNet input: image is nil")
	}

	sourceBounds := source.Bounds()
	sourceWidth, sourceHeight := sourceBounds.Dx(), sourceBounds.Dy()
	resizedWidth, resizedHeight, err := resizeDimensions(sourceWidth, sourceHeight, imageNetResizeShortEdge)
	if err != nil {
		return nil, fmt.Errorf("preprocess ImageNet input: %w", err)
	}

	resized := image.NewNRGBA(image.Rect(0, 0, resizedWidth, resizedHeight))
	xdraw.CatmullRom.Scale(resized, resized.Bounds(), source, sourceBounds, stddraw.Src, nil)

	cropX := (resizedWidth - imageNetInputSize) / 2
	cropY := (resizedHeight - imageNetInputSize) / 2
	if cropX < 0 || cropY < 0 {
		return nil, fmt.Errorf("resized image %dx%d is smaller than the %dx%d center crop", resizedWidth, resizedHeight, imageNetInputSize, imageNetInputSize)
	}

	cropped := image.NewNRGBA(image.Rect(0, 0, imageNetInputSize, imageNetInputSize))
	stddraw.Draw(cropped, cropped.Bounds(), resized, image.Pt(cropX, cropY), stddraw.Src)
	return cropped, nil
}

func resizeDimensions(width, height, shortEdge int) (int, int, error) {
	if width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("image has invalid dimensions %dx%d", width, height)
	}
	if shortEdge <= 0 {
		return 0, 0, fmt.Errorf("resize short edge must be positive, got %d", shortEdge)
	}

	if width <= height {
		resizedHeight := (height*shortEdge + width/2) / width
		return shortEdge, resizedHeight, nil
	}
	resizedWidth := (width*shortEdge + height/2) / height
	return resizedWidth, shortEdge, nil
}
