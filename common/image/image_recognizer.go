//go:build onnx

package image

import (
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

type ImageRecognizer struct {
	mu           sync.Mutex
	closed       bool
	session      *ort.Session[float32]
	labels       []string
	inputTensor  *ort.Tensor[float32]
	outputTensor *ort.Tensor[float32]
}

const (
	defaultInputName  = "data"
	defaultOutputName = "mobilenetv20_output_flatten0_reshape0"
)

var (
	initOnce               sync.Once
	initErr                error
	initializedRuntimePath string
)

// NewImageRecognizer creates a recognizer for the ONNX Model Zoo
// mobilenetv2-7 model. Empty model and label paths use project-relative
// defaults. GOPHERAI_ONNX_RUNTIME_PATH can override the native runtime DLL.
func NewImageRecognizer(modelPath, labelPath string, inputH, inputW int) (*ImageRecognizer, error) {
	if inputH <= 0 || inputW <= 0 {
		inputH, inputW = imageNetInputSize, imageNetInputSize
	}
	if inputH != imageNetInputSize || inputW != imageNetInputSize {
		return nil, fmt.Errorf(
			"MobileNetV2 expects a %dx%d input, got %dx%d",
			imageNetInputSize,
			imageNetInputSize,
			inputH,
			inputW,
		)
	}

	resolvedModelPath, err := resolveConfiguredAssetPath(modelPath, DefaultModelPath, "ONNX model")
	if err != nil {
		return nil, err
	}
	resolvedLabelPath, err := resolveConfiguredAssetPath(labelPath, DefaultLabelPath, "ImageNet label file")
	if err != nil {
		return nil, err
	}

	runtimePath := strings.TrimSpace(os.Getenv("GOPHERAI_ONNX_RUNTIME_PATH"))
	resolvedRuntimePath, err := resolveConfiguredAssetPath(runtimePath, DefaultRuntimePath, "ONNX Runtime library")
	if err != nil {
		return nil, fmt.Errorf("configure GOPHERAI_ONNX_RUNTIME_PATH: %w", err)
	}

	labels, err := loadLabels(resolvedLabelPath)
	if err != nil {
		return nil, err
	}
	if err := initializeONNXRuntime(resolvedRuntimePath); err != nil {
		return nil, err
	}

	inputShape := ort.NewShape(1, 3, imageNetInputSize, imageNetInputSize)
	inputTensor, err := ort.NewTensor(inputShape, make([]float32, inputShape.FlattenedSize()))
	if err != nil {
		return nil, fmt.Errorf("create MobileNetV2 input tensor: %w", err)
	}

	outputShape := ort.NewShape(1, imageNetClassCount)
	outputTensor, err := ort.NewEmptyTensor[float32](outputShape)
	if err != nil {
		_ = inputTensor.Destroy()
		return nil, fmt.Errorf("create MobileNetV2 output tensor: %w", err)
	}

	session, err := ort.NewSession[float32](
		resolvedModelPath,
		[]string{defaultInputName},
		[]string{defaultOutputName},
		[]*ort.Tensor[float32]{inputTensor},
		[]*ort.Tensor[float32]{outputTensor},
	)
	if err != nil {
		_ = inputTensor.Destroy()
		_ = outputTensor.Destroy()
		return nil, fmt.Errorf(
			"create MobileNetV2 ONNX session from %q (expected input %q [1,3,224,224] and output %q [1,1000]): %w",
			resolvedModelPath,
			defaultInputName,
			defaultOutputName,
			err,
		)
	}

	return &ImageRecognizer{
		session:      session,
		labels:       labels,
		inputTensor:  inputTensor,
		outputTensor: outputTensor,
	}, nil
}

func resolveConfiguredAssetPath(configuredPath, defaultPath, description string) (string, error) {
	path := strings.TrimSpace(configuredPath)
	if path == "" {
		path = defaultPath
	}
	resolvedPath, err := ResolveAssetPath(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s path %q: %w", description, path, err)
	}
	if err := requireRegularFile(resolvedPath, description); err != nil {
		return "", err
	}
	return resolvedPath, nil
}

func initializeONNXRuntime(runtimePath string) error {
	initOnce.Do(func() {
		initializedRuntimePath = runtimePath
		ort.SetSharedLibraryPath(runtimePath)
		initErr = ort.InitializeEnvironment()
	})

	if !samePath(initializedRuntimePath, runtimePath) {
		return fmt.Errorf(
			"ONNX Runtime is already initialized from %q and cannot be switched to %q in the same process",
			initializedRuntimePath,
			runtimePath,
		)
	}
	if initErr != nil {
		return fmt.Errorf("initialize ONNX Runtime from %q: %w; correct the runtime path and restart the process", runtimePath, initErr)
	}
	return nil
}

func samePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func (recognizer *ImageRecognizer) Close() {
	if recognizer == nil {
		return
	}
	recognizer.mu.Lock()
	defer recognizer.mu.Unlock()
	if recognizer.closed {
		return
	}
	recognizer.closed = true
	if recognizer.session != nil {
		_ = recognizer.session.Destroy()
		recognizer.session = nil
	}
	if recognizer.inputTensor != nil {
		_ = recognizer.inputTensor.Destroy()
		recognizer.inputTensor = nil
	}
	if recognizer.outputTensor != nil {
		_ = recognizer.outputTensor.Destroy()
		recognizer.outputTensor = nil
	}
}

func (recognizer *ImageRecognizer) PredictFromFile(imagePath string) (string, error) {
	file, err := os.Open(filepath.Clean(imagePath))
	if err != nil {
		return "", errors.New("open image file: unavailable")
	}
	defer file.Close()

	decoded, _, err := image.Decode(file)
	if err != nil {
		return "", errors.New("decode image file: invalid image")
	}
	return recognizer.PredictFromImage(decoded)
}

func (recognizer *ImageRecognizer) PredictFromBuffer(buffer []byte) (string, error) {
	decoded, err := DecodeUpload(buffer)
	if err != nil {
		return "", err
	}
	return recognizer.PredictFromImage(decoded)
}

func (recognizer *ImageRecognizer) PredictFromImage(source image.Image) (string, error) {
	data, err := preprocessImageNet(source)
	if err != nil {
		return "", err
	}

	recognizer.mu.Lock()
	defer recognizer.mu.Unlock()
	if recognizer.closed || recognizer.session == nil || recognizer.inputTensor == nil || recognizer.outputTensor == nil {
		return "", errors.New("image recognizer is closed")
	}

	inputData := recognizer.inputTensor.GetData()
	if len(inputData) != len(data) {
		return "", fmt.Errorf("MobileNetV2 input tensor has %d values, preprocessed image has %d", len(inputData), len(data))
	}
	copy(inputData, data)

	if err := recognizer.session.Run(); err != nil {
		return "", fmt.Errorf("run MobileNetV2 ONNX inference: %w", err)
	}

	outputData := recognizer.outputTensor.GetData()
	if len(outputData) == 0 {
		return "", errors.New("MobileNetV2 returned an empty output tensor")
	}
	if len(outputData) != len(recognizer.labels) {
		return "", fmt.Errorf("MobileNetV2 returned %d scores for %d labels", len(outputData), len(recognizer.labels))
	}

	maximumIndex := 0
	maximumValue := outputData[0]
	for index := 1; index < len(outputData); index++ {
		if outputData[index] > maximumValue {
			maximumValue = outputData[index]
			maximumIndex = index
		}
	}
	return recognizer.labels[maximumIndex], nil
}
