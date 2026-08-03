package file

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"GopherAI/common/code"
	"GopherAI/controller"
	knowledgebaseservice "GopherAI/service/knowledgebase"

	"github.com/gin-gonic/gin"
)

func TestUploadRagFileRejectsOversizedMultipartRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("body is not read"))
	request.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	request.ContentLength = knowledgebaseservice.MaxDocumentBytes + multipartEnvelopeBytes + 1
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request

	UploadRagFile(context)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
	response := new(controller.Response)
	if err := json.Unmarshal(recorder.Body.Bytes(), response); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != code.CodeInvalidParams {
		t.Fatalf("business status = %d, want %d", response.StatusCode, code.CodeInvalidParams)
	}
}
