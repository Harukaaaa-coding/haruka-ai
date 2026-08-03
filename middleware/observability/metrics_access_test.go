package observability

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMetricsAccessAllowsOnlyLoopbackOrBearerToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/metrics", MetricsAccess("metrics-secret"), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	for _, testCase := range []struct {
		name       string
		remoteAddr string
		authorize  string
		wantStatus int
	}{
		{name: "remote denied", remoteAddr: "198.51.100.10:9090", wantStatus: http.StatusNotFound},
		{name: "loopback allowed", remoteAddr: "127.0.0.1:9090", wantStatus: http.StatusNoContent},
		{name: "token allowed", remoteAddr: "198.51.100.10:9090", authorize: "Bearer metrics-secret", wantStatus: http.StatusNoContent},
		{name: "wrong token denied", remoteAddr: "198.51.100.10:9090", authorize: "Bearer wrong", wantStatus: http.StatusNotFound},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			request.RemoteAddr = testCase.remoteAddr
			if testCase.authorize != "" {
				request.Header.Set("Authorization", testCase.authorize)
			}
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)
			if response.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, testCase.wantStatus)
			}
		})
	}
}
