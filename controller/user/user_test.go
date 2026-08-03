package user

import (
	"GopherAI/common/code"
	"GopherAI/controller"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestWriteRateLimitSetsStatusAndRetryAfter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	writeRateLimit(ctx, new(controller.Response), 1500*time.Millisecond)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want 2", got)
	}
	var response controller.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != code.CodeTooManyRequests {
		t.Fatalf("business status = %d, want %d", response.StatusCode, code.CodeTooManyRequests)
	}
}
