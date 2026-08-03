package modelcatalog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGetModelsContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/models", GetModels)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/models", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, field := range []string{`"status_code":1000`, `"defaultTimeoutMs":`, `"providers":`, `"models":`, `"capabilities":`, `"available":`} {
		if !strings.Contains(body, field) {
			t.Fatalf("response does not contain %s: %s", field, body)
		}
	}
	if strings.Contains(strings.ToLower(body), "api_key") || strings.Contains(strings.ToLower(body), "apikey") {
		t.Fatalf("response contains a credential field: %s", body)
	}
}
