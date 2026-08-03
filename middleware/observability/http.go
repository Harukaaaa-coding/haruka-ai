// Package observability contains low-dependency HTTP instrumentation that is
// safe to use at the edge of the application.  In particular, it deliberately
// never serializes request headers, query strings, or bodies into access logs.
package observability

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	// RequestIDHeader is echoed on every HTTP response so callers can include it
	// when reporting a failure.
	RequestIDHeader = "X-Request-ID"

	requestIDGinKey = "request_id"
	maxRequestIDLen = 128
)

type requestIDContextKey struct{}

var generatedRequestID atomic.Uint64

// NewJSONLogger returns a logger whose output consists of one JSON document per
// line.  It is intentionally separate from the process-wide logger so existing
// application logs do not need to be changed all at once.
func NewJSONLogger(writer io.Writer) *log.Logger {
	if writer == nil {
		writer = io.Discard
	}
	return log.New(writer, "", 0)
}

// HTTP adds a request correlation ID, records a redacted structured access log,
// and updates the supplied Prometheus-compatible metrics collector.  It must be
// registered before Recovery so recovered panics are recorded as HTTP 500s.
func HTTP(metrics *Metrics, logger *log.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := incomingRequestID(c.GetHeader(RequestIDHeader))
		c.Header(RequestIDHeader, requestID)
		c.Set(requestIDGinKey, requestID)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestIDContextKey{}, requestID))

		if metrics != nil {
			metrics.startRequest()
			defer metrics.finishRequest()
		}

		started := time.Now()
		c.Next()

		duration := time.Since(started)
		route := routeName(c)
		status := c.Writer.Status()
		responseBytes := c.Writer.Size()
		if responseBytes < 0 {
			responseBytes = 0
		}
		if metrics != nil {
			metrics.Observe(c.Request.Method, route, status, duration)
		}
		writeJSON(logger, accessLog{
			Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
			Level:         "info",
			Event:         "http_request",
			RequestID:     requestID,
			Method:        c.Request.Method,
			Route:         route,
			Status:        status,
			DurationMS:    float64(duration) / float64(time.Millisecond),
			ResponseBytes: responseBytes,
		})
	}
}

// Recovery converts panics to a generic 500 response without emitting Gin's
// request dump.  Gin's default debug recovery dump can include Cookie headers;
// this variant logs only metadata that is safe for central log collection.
func Recovery(logger *log.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				writeJSON(logger, panicLog{
					Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
					Level:     "error",
					Event:     "http_panic",
					RequestID: RequestID(c),
					Method:    c.Request.Method,
					Route:     routeName(c),
					PanicType: fmt.Sprintf("%T", recovered),
				})
				c.AbortWithStatus(http.StatusInternalServerError)
			}
		}()
		c.Next()
	}
}

// RequestID returns the correlation ID assigned by HTTP.  It can be used by
// handlers and service adapters that only have a Gin context.
func RequestID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if value, ok := c.Get(requestIDGinKey); ok {
		if requestID, ok := value.(string); ok {
			return requestID
		}
	}
	return RequestIDFromContext(c.Request.Context())
}

// RequestIDFromContext returns the correlation ID propagated on the standard
// request context.  This makes the value available to code below Gin without a
// dependency on gin.Context.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}

type accessLog struct {
	Timestamp     string  `json:"timestamp"`
	Level         string  `json:"level"`
	Event         string  `json:"event"`
	RequestID     string  `json:"request_id"`
	Method        string  `json:"method"`
	Route         string  `json:"route"`
	Status        int     `json:"status"`
	DurationMS    float64 `json:"duration_ms"`
	ResponseBytes int     `json:"response_bytes"`
}

type panicLog struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Event     string `json:"event"`
	RequestID string `json:"request_id"`
	Method    string `json:"method"`
	Route     string `json:"route"`
	PanicType string `json:"panic_type"`
}

func writeJSON(logger *log.Logger, value any) {
	if logger == nil {
		return
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return
	}
	logger.Println(string(payload))
}

func incomingRequestID(value string) string {
	if validRequestID(value) {
		return value
	}
	return newRequestID()
}

func validRequestID(value string) bool {
	if len(value) == 0 || len(value) > maxRequestIDLen {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func newRequestID() string {
	var bytes [16]byte
	if _, err := cryptorand.Read(bytes[:]); err == nil {
		return hex.EncodeToString(bytes[:])
	}
	// Crypto randomness should always be available on supported platforms.  This
	// fallback preserves correlation if the operating system entropy source is
	// temporarily unavailable; it is not used as a credential.
	return fmt.Sprintf("fallback-%x-%x", time.Now().UnixNano(), generatedRequestID.Add(1))
}

func routeName(c *gin.Context) string {
	if c == nil {
		return "unmatched"
	}
	if route := c.FullPath(); route != "" {
		return route
	}
	return "unmatched"
}
