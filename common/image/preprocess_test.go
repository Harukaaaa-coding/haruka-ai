package image

import (
	"image"
	"image/color"
	"math"
	"testing"
)

func TestResizeDimensionsPreservesAspectRatio(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		wantWidth     int
		wantHeight    int
	}{
		{name: "landscape", width: 400, height: 200, wantWidth: 512, wantHeight: 256},
		{name: "portrait", width: 200, height: 400, wantWidth: 256, wantHeight: 512},
		{name: "square", width: 300, height: 300, wantWidth: 256, wantHeight: 256},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			width, height, err := resizeDimensions(test.width, test.height, imageNetResizeShortEdge)
			if err != nil {
				t.Fatalf("resizeDimensions returned an error: %v", err)
			}
			if width != test.wantWidth || height != test.wantHeight {
				t.Fatalf("resizeDimensions = %dx%d, want %dx%d", width, height, test.wantWidth, test.wantHeight)
			}
		})
	}
}

func TestResizeDimensionsRejectsEmptyImage(t *testing.T) {
	if _, _, err := resizeDimensions(0, 100, imageNetResizeShortEdge); err == nil {
		t.Fatal("resizeDimensions unexpectedly accepted an empty image")
	}
}

func TestPreprocessImageNetNormalizesNCHW(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 400, 200))
	constantColor := color.NRGBA{R: 255, G: 128, B: 0, A: 255}
	for y := 0; y < source.Bounds().Dy(); y++ {
		for x := 0; x < source.Bounds().Dx(); x++ {
			source.SetNRGBA(x, y, constantColor)
		}
	}

	data, err := preprocessImageNet(source)
	if err != nil {
		t.Fatalf("preprocessImageNet returned an error: %v", err)
	}
	planeSize := imageNetInputSize * imageNetInputSize
	if len(data) != 3*planeSize {
		t.Fatalf("preprocessImageNet returned %d values, want %d", len(data), 3*planeSize)
	}

	want := [3]float32{
		(1.0 - imageNetMean[0]) / imageNetStd[0],
		(float32(128)/255.0 - imageNetMean[1]) / imageNetStd[1],
		(0.0 - imageNetMean[2]) / imageNetStd[2],
	}
	for channel := 0; channel < 3; channel++ {
		for _, offset := range []int{0, planeSize / 2, planeSize - 1} {
			got := data[channel*planeSize+offset]
			if math.Abs(float64(got-want[channel])) > 1e-5 {
				t.Errorf("channel %d offset %d = %f, want %f", channel, offset, got, want[channel])
			}
		}
	}
}

func TestPreprocessImageNetRejectsNilImage(t *testing.T) {
	if _, err := preprocessImageNet(nil); err == nil {
		t.Fatal("preprocessImageNet unexpectedly accepted a nil image")
	}
}
