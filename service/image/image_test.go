package image

import (
	commonimage "GopherAI/common/image"
	"bytes"
	"context"
	"errors"
	stdimage "image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnvironmentOrDefault(t *testing.T) {
	t.Setenv("GOPHERAI_TEST_ASSET_PATH", "  models/example.onnx  ")
	if got := environmentOrDefault("GOPHERAI_TEST_ASSET_PATH", "fallback"); got != "models/example.onnx" {
		t.Fatalf("environmentOrDefault = %q, want trimmed environment value", got)
	}
}

func TestEnvironmentOrDefaultFallsBackForWhitespace(t *testing.T) {
	t.Setenv("GOPHERAI_TEST_ASSET_PATH", "   ")
	if got := environmentOrDefault("GOPHERAI_TEST_ASSET_PATH", "fallback"); got != "fallback" {
		t.Fatalf("environmentOrDefault = %q, want fallback", got)
	}
}

func TestServiceReusesRecognizerAndSerializesInference(t *testing.T) {
	var factoryCalls atomic.Int32
	recognizer := &trackingRecognizer{delay: 5 * time.Millisecond}
	service := NewService(func(string, string, int, int) (Recognizer, error) {
		factoryCalls.Add(1)
		return recognizer, nil
	})

	const requests = 12
	var waitGroup sync.WaitGroup
	errorsChannel := make(chan error, requests)
	for index := 0; index < requests; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if _, err := service.predict(testImage()); err != nil {
				errorsChannel <- err
			}
		}()
	}
	waitGroup.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Errorf("predict returned an error: %v", err)
	}

	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("factory called %d times, want 1", got)
	}
	if got := recognizer.calls.Load(); got != requests {
		t.Fatalf("PredictFromImage called %d times, want %d", got, requests)
	}
	if got := recognizer.maxActive.Load(); got != 1 {
		t.Fatalf("maximum concurrent predictions = %d, want 1", got)
	}
}

func TestServiceCloseIsIdempotentAndPreventsRecreation(t *testing.T) {
	var factoryCalls atomic.Int32
	recognizer := &trackingRecognizer{}
	service := NewService(func(string, string, int, int) (Recognizer, error) {
		factoryCalls.Add(1)
		return recognizer, nil
	})

	if _, err := service.predict(testImage()); err != nil {
		t.Fatalf("initial predict returned an error: %v", err)
	}
	service.Close()
	service.Close()

	if got := recognizer.closeCalls.Load(); got != 1 {
		t.Fatalf("Close called %d times, want 1", got)
	}
	if _, err := service.predict(testImage()); !errors.Is(err, ErrRecognizerClosed) {
		t.Fatalf("predict after Close error = %v, want ErrRecognizerClosed", err)
	}
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("factory called %d times after Close, want 1", got)
	}
}

func TestServiceWaitsForInFlightPredictionBeforeClose(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	recognizer := &trackingRecognizer{entered: entered, release: release}
	service := NewService(func(string, string, int, int) (Recognizer, error) { return recognizer, nil })

	predictDone := make(chan error, 1)
	go func() {
		_, err := service.predict(testImage())
		predictDone <- err
	}()
	<-entered

	closeDone := make(chan struct{})
	go func() {
		service.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
		t.Fatal("Close returned while inference was still in flight")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-predictDone; err != nil {
		t.Fatalf("predict returned an error: %v", err)
	}
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after inference completed")
	}
}

func TestServiceCloseContextReturnsAtDeadlineAndCloseContinues(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	recognizer := &trackingRecognizer{entered: entered, release: release}
	service := NewService(func(string, string, int, int) (Recognizer, error) { return recognizer, nil })

	predictDone := make(chan error, 1)
	go func() {
		_, err := service.predict(testImage())
		predictDone <- err
	}()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := service.CloseContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseContext error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("CloseContext returned after %v, want a bounded wait", elapsed)
	}
	if recognizer.closeCalls.Load() != 0 {
		t.Fatal("recognizer was closed while inference was still in flight")
	}
	if _, err := service.predict(testImage()); !errors.Is(err, ErrRecognizerClosed) {
		t.Fatalf("new prediction while closing error = %v, want ErrRecognizerClosed", err)
	}

	close(release)
	if err := <-predictDone; err != nil {
		t.Fatalf("predict returned an error: %v", err)
	}
	service.Close()
	if got := recognizer.closeCalls.Load(); got != 1 {
		t.Fatalf("Close called %d times, want 1", got)
	}
}

func TestServiceActualReadLimitCannotBeBypassedByHeaderSize(t *testing.T) {
	fileHeader := multipartFileHeader(t, bytes.Repeat([]byte{'x'}, int(MaxImageBytes)+1), "image.png")
	fileHeader.Size = 1
	var factoryCalls atomic.Int32
	service := NewService(func(string, string, int, int) (Recognizer, error) {
		factoryCalls.Add(1)
		return &trackingRecognizer{}, nil
	})

	_, err := service.RecognizeImage(fileHeader)
	if !errors.Is(err, commonimage.ErrImageTooLarge) {
		t.Fatalf("RecognizeImage error = %v, want ErrImageTooLarge", err)
	}
	if factoryCalls.Load() != 0 {
		t.Fatal("recognizer factory was called for an oversized upload")
	}
}

func TestServiceValidatesBeforeCreatingRecognizer(t *testing.T) {
	fileHeader := multipartFileHeader(t, []byte("not an image"), "image.png")
	var factoryCalls atomic.Int32
	service := NewService(func(string, string, int, int) (Recognizer, error) {
		factoryCalls.Add(1)
		return &trackingRecognizer{}, nil
	})

	_, err := service.RecognizeImage(fileHeader)
	if !errors.Is(err, commonimage.ErrUnsupportedImage) {
		t.Fatalf("RecognizeImage error = %v, want ErrUnsupportedImage", err)
	}
	if factoryCalls.Load() != 0 {
		t.Fatal("recognizer factory was called for an invalid upload")
	}
}

func TestServiceFullyDecodesBeforeCreatingRecognizer(t *testing.T) {
	complete := validPNG(t)
	if len(complete) < 33 {
		t.Fatalf("encoded PNG is unexpectedly short: %d", len(complete))
	}
	fileHeader := multipartFileHeader(t, complete[:33], "truncated.png")
	var factoryCalls atomic.Int32
	service := NewService(func(string, string, int, int) (Recognizer, error) {
		factoryCalls.Add(1)
		return &trackingRecognizer{}, nil
	})

	_, err := service.RecognizeImage(fileHeader)
	if !errors.Is(err, commonimage.ErrInvalidImage) {
		t.Fatalf("RecognizeImage error = %v, want ErrInvalidImage", err)
	}
	if factoryCalls.Load() != 0 {
		t.Fatal("recognizer factory was called before the image was fully decoded")
	}
}

func TestServiceDoesNotExposeFactoryPaths(t *testing.T) {
	fileHeader := multipartFileHeader(t, validPNG(t), "image.png")
	service := NewService(func(string, string, int, int) (Recognizer, error) {
		return nil, errors.New(`open C:\\private\\models\\secret.onnx: access denied`)
	})

	_, err := service.RecognizeImage(fileHeader)
	if !errors.Is(err, ErrRecognizerUnavailable) {
		t.Fatalf("RecognizeImage error = %v, want ErrRecognizerUnavailable", err)
	}
	if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "secret.onnx") {
		t.Fatalf("RecognizeImage leaked a configured path: %v", err)
	}
}

type trackingRecognizer struct {
	delay      time.Duration
	entered    chan<- struct{}
	release    <-chan struct{}
	active     atomic.Int32
	maxActive  atomic.Int32
	calls      atomic.Int32
	closeCalls atomic.Int32
}

func (recognizer *trackingRecognizer) PredictFromImage(stdimage.Image) (string, error) {
	recognizer.calls.Add(1)
	active := recognizer.active.Add(1)
	defer recognizer.active.Add(-1)
	for {
		maximum := recognizer.maxActive.Load()
		if active <= maximum || recognizer.maxActive.CompareAndSwap(maximum, active) {
			break
		}
	}
	if recognizer.entered != nil {
		recognizer.entered <- struct{}{}
	}
	if recognizer.release != nil {
		<-recognizer.release
	}
	if recognizer.delay > 0 {
		time.Sleep(recognizer.delay)
	}
	return "class", nil
}

func testImage() stdimage.Image {
	return stdimage.NewNRGBA(stdimage.Rect(0, 0, 2, 2))
}

func (recognizer *trackingRecognizer) Close() {
	recognizer.closeCalls.Add(1)
}

func multipartFileHeader(t *testing.T, contents []byte, name string) *multipart.FileHeader {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("image", name)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(contents); err != nil {
		t.Fatalf("write multipart file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	request := httptest.NewRequest("POST", "/", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if err := request.ParseMultipartForm(MaxImageBytes + 1); err != nil {
		t.Fatalf("ParseMultipartForm: %v", err)
	}
	t.Cleanup(func() { _ = request.MultipartForm.RemoveAll() })
	files := request.MultipartForm.File["image"]
	if len(files) != 1 {
		t.Fatalf("multipart file count = %d, want 1", len(files))
	}
	return files[0]
}

func validPNG(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	picture := stdimage.NewNRGBA(stdimage.Rect(0, 0, 2, 2))
	picture.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	if err := png.Encode(&buffer, picture); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	return buffer.Bytes()
}
