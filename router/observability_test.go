package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMetricsRouteIsRegisteredAndDoesNotUseRawRequestDataAsLabels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := InitRouter()

	first := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz?token=query-secret", nil)
	request.Header.Set("Authorization", "Bearer authorization-secret")
	router.ServeHTTP(first, request)

	metrics := httptest.NewRecorder()
	metricsRequest := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRequest.RemoteAddr = "127.0.0.1:12345"
	router.ServeHTTP(metrics, metricsRequest)
	if metrics.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want %d", metrics.Code, http.StatusOK)
	}
	output := metrics.Body.String()
	if !strings.Contains(output, `gopherai_http_requests_total{method="GET",route="/healthz",status="200"} 1`) {
		t.Fatalf("health request not represented by route template: %s", output)
	}
	if strings.Contains(output, "query-secret") || strings.Contains(output, "authorization-secret") {
		t.Fatalf("metrics exposed request data: %s", output)
	}
}

func TestMetricsRouteRequiresLoopbackOrConfiguredToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("GOPHERAI_METRICS_TOKEN", "metrics-test-token")
	router := InitRouter()

	denied := httptest.NewRecorder()
	deniedRequest := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	deniedRequest.RemoteAddr = "198.51.100.22:12345"
	router.ServeHTTP(denied, deniedRequest)
	if denied.Code != http.StatusNotFound {
		t.Fatalf("unauthorized metrics status = %d, want %d", denied.Code, http.StatusNotFound)
	}

	authorized := httptest.NewRecorder()
	authorizedRequest := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	authorizedRequest.RemoteAddr = "198.51.100.22:12345"
	authorizedRequest.Header.Set("Authorization", "Bearer metrics-test-token")
	router.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusOK || !strings.Contains(authorized.Body.String(), "gopherai_http_requests_total") {
		t.Fatalf("authorized metrics response = status %d body %s", authorized.Code, authorized.Body.String())
	}
}
