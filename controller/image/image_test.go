package image

import (
	imageservice "GopherAI/service/image"
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRecognizeImageRejectsFileLargerThanLimit(t *testing.T) {
	response := performUpload(t, bytes.Repeat([]byte{'x'}, int(imageservice.MaxImageBytes)+1), false)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestRecognizeImageMaxBytesReaderRejectsOversizedMultipartBody(t *testing.T) {
	contents := bytes.Repeat([]byte{'x'}, int(imageservice.MaxImageBytes+multipartOverhead)+1)
	response := performUpload(t, contents, true)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestRecognizeImageRejectsEmptyAndUnsupportedFiles(t *testing.T) {
	tests := []struct {
		name     string
		contents []byte
	}{
		{name: "empty", contents: nil},
		{name: "unsupported", contents: []byte("not an image")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := performUpload(t, test.contents, false)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
			}
		})
	}
}

func performUpload(t *testing.T, contents []byte, hideContentLength bool) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("image", "upload.png")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(contents); err != nil {
		t.Fatalf("write multipart file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/recognize", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if hideContentLength {
		request.ContentLength = -1
	}
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = request
	RecognizeImage(context)
	return response
}
