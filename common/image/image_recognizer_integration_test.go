//go:build onnx

package image

import (
	"image"
	"image/color"
	"os"
	"strings"
	"testing"

	ort "github.com/yalue/onnxruntime_go"
)

func TestMobileNetV2Integration(t *testing.T) {
	if os.Getenv("GOPHERAI_RUN_ONNX_INTEGRATION") != "1" {
		t.Skip("set GOPHERAI_RUN_ONNX_INTEGRATION=1 to run the native ONNX integration test")
	}

	modelPath, err := resolveConfiguredAssetPath(
		os.Getenv("GOPHERAI_ONNX_MODEL_PATH"),
		DefaultModelPath,
		"ONNX model",
	)
	if err != nil {
		t.Fatalf("resolve model: %v", err)
	}
	labelPath, err := resolveConfiguredAssetPath(
		os.Getenv("GOPHERAI_ONNX_LABEL_PATH"),
		DefaultLabelPath,
		"ImageNet label file",
	)
	if err != nil {
		t.Fatalf("resolve labels: %v", err)
	}
	runtimePath, err := resolveConfiguredAssetPath(
		os.Getenv("GOPHERAI_ONNX_RUNTIME_PATH"),
		DefaultRuntimePath,
		"ONNX Runtime library",
	)
	if err != nil {
		t.Fatalf("resolve runtime: %v", err)
	}
	if err := initializeONNXRuntime(runtimePath); err != nil {
		t.Fatalf("initialize runtime: %v", err)
	}

	inputs, outputs, err := ort.GetInputOutputInfo(modelPath)
	if err != nil {
		t.Fatalf("inspect model input/output metadata: %v", err)
	}
	assertTensorInfo(t, "input", inputs, defaultInputName, []int64{1, 3, 224, 224})
	assertTensorInfo(t, "output", outputs, defaultOutputName, []int64{1, imageNetClassCount})

	recognizer, err := NewImageRecognizer(modelPath, labelPath, imageNetInputSize, imageNetInputSize)
	if err != nil {
		t.Fatalf("NewImageRecognizer returned an error: %v", err)
	}
	defer recognizer.Close()

	source := image.NewNRGBA(image.Rect(0, 0, 320, 240))
	for y := 0; y < source.Bounds().Dy(); y++ {
		for x := 0; x < source.Bounds().Dx(); x++ {
			source.SetNRGBA(x, y, color.NRGBA{
				R: uint8(x % 256),
				G: uint8(y % 256),
				B: uint8((x + y) % 256),
				A: 255,
			})
		}
	}

	label, err := recognizer.PredictFromImage(source)
	if err != nil {
		t.Fatalf("PredictFromImage returned an error: %v", err)
	}
	if strings.TrimSpace(label) == "" {
		t.Fatal("PredictFromImage returned an empty label")
	}
	t.Logf("MobileNetV2 prediction: %s", label)

	if testImagePath := strings.TrimSpace(os.Getenv("GOPHERAI_ONNX_TEST_IMAGE")); testImagePath != "" {
		fileLabel, err := recognizer.PredictFromFile(testImagePath)
		if err != nil {
			t.Fatalf("PredictFromFile(%q) returned an error: %v", testImagePath, err)
		}
		if strings.TrimSpace(fileLabel) == "" {
			t.Fatalf("PredictFromFile(%q) returned an empty label", testImagePath)
		}
		t.Logf("MobileNetV2 file prediction for %s: %s", testImagePath, fileLabel)
	}
}

func assertTensorInfo(t *testing.T, kind string, values []ort.InputOutputInfo, wantName string, wantDimensions []int64) {
	t.Helper()
	if len(values) != 1 {
		t.Fatalf("model has %d %ss, want 1: %v", len(values), kind, values)
	}
	got := values[0]
	if got.Name != wantName {
		t.Errorf("%s name = %q, want %q", kind, got.Name, wantName)
	}
	if got.OrtValueType != ort.ONNXTypeTensor {
		t.Errorf("%s ONNX type = %v, want tensor", kind, got.OrtValueType)
	}
	if got.DataType != ort.TensorElementDataTypeFloat {
		t.Errorf("%s data type = %v, want float32", kind, got.DataType)
	}
	if len(got.Dimensions) != len(wantDimensions) {
		t.Fatalf("%s dimensions = %v, want %v", kind, got.Dimensions, wantDimensions)
	}
	for index := range wantDimensions {
		if got.Dimensions[index] != wantDimensions[index] {
			t.Errorf("%s dimensions = %v, want %v", kind, got.Dimensions, wantDimensions)
			break
		}
	}
}
