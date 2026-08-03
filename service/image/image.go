package image

import (
	commonimage "GopherAI/common/image"
	"context"
	"errors"
	stdimage "image"
	"io"
	"mime/multipart"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

const MaxImageBytes = commonimage.MaxUploadBytes

var (
	ErrReadImage             = errors.New("read uploaded image")
	ErrRecognizerUnavailable = errors.New("image recognizer is unavailable")
	ErrRecognizerClosed      = errors.New("image recognizer is closed")
	ErrRecognitionFailed     = errors.New("image recognition failed")
)

type Recognizer interface {
	PredictFromImage(stdimage.Image) (string, error)
	Close()
}

type RecognizerFactory func(modelPath, labelPath string, inputH, inputW int) (Recognizer, error)

type Service struct {
	mu         sync.Mutex
	factory    RecognizerFactory
	recognizer Recognizer
	closed     bool
	closing    atomic.Bool
	closeOnce  sync.Once
	closeDone  chan struct{}
}

func NewService(factory RecognizerFactory) *Service {
	if factory == nil {
		factory = func(modelPath, labelPath string, inputH, inputW int) (Recognizer, error) {
			return commonimage.NewImageRecognizer(modelPath, labelPath, inputH, inputW)
		}
	}
	return &Service{factory: factory, closeDone: make(chan struct{})}
}

var defaultService = NewService(nil)

func RecognizeImage(file *multipart.FileHeader) (string, error) {
	return defaultService.RecognizeImage(file)
}

// Close releases the shared recognizer. It is idempotent and waits for an
// in-flight inference before destroying the session and its shared tensors.
func Close() {
	defaultService.Close()
}

// CloseContext starts the same idempotent close operation as Close, but only
// waits until ctx expires. The close continues in the background so callers
// can let process shutdown reclaim a stuck native inference safely.
func CloseContext(ctx context.Context) error {
	return defaultService.CloseContext(ctx)
}

func (service *Service) RecognizeImage(file *multipart.FileHeader) (string, error) {
	if file == nil || file.Size <= 0 {
		return "", commonimage.ErrEmptyImage
	}
	if file.Size > MaxImageBytes {
		return "", commonimage.ErrImageTooLarge
	}

	source, err := file.Open()
	if err != nil {
		return "", ErrReadImage
	}
	defer source.Close()

	buffer, err := io.ReadAll(io.LimitReader(source, MaxImageBytes+1))
	if err != nil {
		return "", ErrReadImage
	}
	if len(buffer) == 0 {
		return "", commonimage.ErrEmptyImage
	}
	if int64(len(buffer)) > MaxImageBytes {
		return "", commonimage.ErrImageTooLarge
	}
	decoded, err := commonimage.DecodeUpload(buffer)
	if err != nil {
		return "", err
	}
	return service.predict(decoded)
}

func (service *Service) predict(source stdimage.Image) (string, error) {
	if service == nil || service.closing.Load() {
		return "", ErrRecognizerClosed
	}
	service.mu.Lock()
	defer service.mu.Unlock()

	if service.closed || service.closing.Load() {
		return "", ErrRecognizerClosed
	}
	if service.recognizer == nil {
		modelPath := environmentOrDefault("GOPHERAI_ONNX_MODEL_PATH", commonimage.DefaultModelPath)
		labelPath := environmentOrDefault("GOPHERAI_ONNX_LABEL_PATH", commonimage.DefaultLabelPath)
		recognizer, err := service.factory(modelPath, labelPath, 224, 224)
		if err != nil || recognizer == nil {
			// Factory errors can contain configured local paths. Keep them out of
			// errors returned through the HTTP layer.
			return "", ErrRecognizerUnavailable
		}
		service.recognizer = recognizer
	}

	label, err := service.recognizer.PredictFromImage(source)
	if err != nil {
		if commonimage.IsUploadValidationError(err) {
			return "", err
		}
		return "", ErrRecognitionFailed
	}
	return label, nil
}

func (service *Service) Close() {
	_ = service.CloseContext(context.Background())
}

func (service *Service) CloseContext(ctx context.Context) error {
	if service == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	service.closeOnce.Do(func() {
		if service.closeDone == nil {
			service.closeDone = make(chan struct{})
		}
		service.closing.Store(true)
		go func() {
			service.mu.Lock()
			if !service.closed {
				service.closed = true
				if service.recognizer != nil {
					service.recognizer.Close()
					service.recognizer = nil
				}
			}
			service.mu.Unlock()
			close(service.closeDone)
		}()
	})

	select {
	case <-service.closeDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func IsInvalidUpload(err error) bool {
	return commonimage.IsUploadValidationError(err)
}

func IsTooLarge(err error) bool {
	return errors.Is(err, commonimage.ErrImageTooLarge)
}

func environmentOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
