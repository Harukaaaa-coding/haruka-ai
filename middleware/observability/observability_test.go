package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"GopherAI/common/health"

	"github.com/gin-gonic/gin"
)

func TestHTTPCorrelationLoggingAndMetricsAreRedacted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	metrics := NewMetrics()
	var logs bytes.Buffer
	engine := gin.New()
	engine.Use(HTTP(metrics, NewJSONLogger(&logs)), Recovery(NewJSONLogger(&logs)))
	engine.POST("/users/:id", func(c *gin.Context) {
		if got := RequestID(c); got != "trace-123" {
			t.Errorf("RequestID() = %q, want trace-123", got)
		}
		if got := RequestIDFromContext(c.Request.Context()); got != "trace-123" {
			t.Errorf("RequestIDFromContext() = %q, want trace-123", got)
		}
		c.Status(http.StatusCreated)
	})

	request := httptest.NewRequest(http.MethodPost, "/users/secret-user?token=query-secret", strings.NewReader("body-secret"))
	request.Header.Set(RequestIDHeader, "trace-123")
	request.Header.Set("Authorization", "Bearer authorization-secret")
	request.Header.Set("Cookie", "session=cookie-secret")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusCreated)
	}
	if got := response.Header().Get(RequestIDHeader); got != "trace-123" {
		t.Fatalf("response request ID = %q, want trace-123", got)
	}

	line := strings.TrimSpace(logs.String())
	if line == "" {
		t.Fatal("expected access log")
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		t.Fatalf("parse access log: %v; line=%q", err, line)
	}
	if event["event"] != "http_request" || event["route"] != "/users/:id" || event["status"] != float64(http.StatusCreated) {
		t.Fatalf("unexpected access event: %#v", event)
	}
	for _, secret := range []string{"secret-user", "query-secret", "body-secret", "authorization-secret", "cookie-secret"} {
		if strings.Contains(line, secret) {
			t.Fatalf("access log leaked %q: %s", secret, line)
		}
	}

	output := string(metrics.RenderPrometheus(context.Background(), nil))
	if !strings.Contains(output, `gopherai_http_requests_total{method="POST",route="/users/:id",status="201"} 1`) {
		t.Fatalf("missing request counter: %s", output)
	}
	if !strings.Contains(output, `gopherai_http_request_duration_seconds_count{method="POST",route="/users/:id",status="201"} 1`) {
		t.Fatalf("missing duration count: %s", output)
	}
	for _, secret := range []string{"secret-user", "query-secret", "body-secret", "authorization-secret", "cookie-secret"} {
		if strings.Contains(output, secret) {
			t.Fatalf("metrics leaked %q: %s", secret, output)
		}
	}
}

func TestHTTPRejectsUnsafeRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(HTTP(NewMetrics(), nil), Recovery(nil))
	engine.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(RequestIDHeader, "unsafe request id")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	requestID := response.Header().Get(RequestIDHeader)
	if requestID == "" || requestID == "unsafe request id" || !validRequestID(requestID) {
		t.Fatalf("unsafe ID was not replaced: %q", requestID)
	}
}

func TestRecoveryDoesNotLeakPanicValueAndProducesFiveHundred(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var logs bytes.Buffer
	engine := gin.New()
	logger := NewJSONLogger(&logs)
	engine.Use(HTTP(NewMetrics(), logger), Recovery(logger))
	engine.GET("/panic", func(*gin.Context) { panic("panic-secret") })

	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic?token=query-secret", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	output := logs.String()
	if !strings.Contains(output, `"event":"http_panic"`) || !strings.Contains(output, `"status":500`) {
		t.Fatalf("expected panic and access logs, got: %s", output)
	}
	for _, secret := range []string{"panic-secret", "query-secret"} {
		if strings.Contains(output, secret) {
			t.Fatalf("panic log leaked %q: %s", secret, output)
		}
	}
}

func TestMetricsHandlerIncludesHealthWithoutErrorDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	checker := health.NewChecker(time.Second,
		health.Check{Name: "database", Required: true, Run: func(context.Context) error { return nil }},
		health.Check{Name: "optional", Run: func(context.Context) error { return errSecretHealth }},
	)
	checker.MarkReady()
	metrics := NewMetrics()
	metrics.Observe(http.MethodGet, "/api/v1/items/:id", http.StatusOK, 10*time.Millisecond)

	engine := gin.New()
	engine.GET("/metrics", metrics.Handler(checker))
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Content-Type"); !strings.Contains(got, "text/plain") {
		t.Fatalf("Content-Type = %q, want Prometheus text", got)
	}
	output := response.Body.String()
	for _, expected := range []string{
		"gopherai_application_accepting 1",
		"gopherai_health_ready 1",
		`gopherai_health_component_up{component="database",required="true"} 1`,
		`gopherai_health_component_up{component="optional",required="false"} 0`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %q in: %s", expected, output)
		}
	}
	if strings.Contains(output, errSecretHealth.Error()) {
		t.Fatalf("health metrics leaked error detail: %s", output)
	}
}

func TestMetricsCachesHealthProbeAcrossRapidScrapes(t *testing.T) {
	var calls atomic.Int32
	checker := health.NewChecker(time.Second, health.Check{
		Name: "database", Required: true, Run: func(context.Context) error {
			calls.Add(1)
			return nil
		},
	})
	checker.MarkReady()
	metrics := NewMetrics()

	metrics.RenderPrometheus(context.Background(), checker)
	metrics.RenderPrometheus(context.Background(), checker)
	if got := calls.Load(); got != 1 {
		t.Fatalf("health probe calls = %d, want 1 within cache TTL", got)
	}
	checker.MarkNotReady()
	output := string(metrics.RenderPrometheus(context.Background(), checker))
	if !strings.Contains(output, "gopherai_health_ready 0") {
		t.Fatalf("lifecycle transition reused a stale ready snapshot: %s", output)
	}
}

var errSecretHealth = testError("health-secret")

type testError string

func (err testError) Error() string { return string(err) }
