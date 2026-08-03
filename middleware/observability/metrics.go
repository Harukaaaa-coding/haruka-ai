package observability

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"GopherAI/common/health"

	"github.com/gin-gonic/gin"
)

const prometheusContentType = "text/plain; version=0.0.4; charset=utf-8"

const healthSnapshotTTL = 5 * time.Second

var defaultDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Metrics is a small, dependency-free Prometheus collector.  Its route labels
// are templates supplied by Gin (for example /api/v1/file/:id), never raw URLs,
// so callers cannot create unbounded or sensitive label values.
type Metrics struct {
	mu       sync.RWMutex
	http     map[httpMetricKey]*httpHistogram
	inFlight atomic.Int64

	healthMu        sync.Mutex
	healthCollected time.Time
	healthChecker   *health.Checker
	healthAccepting bool
	healthReport    health.Report
	healthReady     bool
}

type httpMetricKey struct {
	method string
	route  string
	status string
}

type httpHistogram struct {
	count   uint64
	sum     float64
	buckets []uint64
}

// NewMetrics creates an isolated collector.  Keeping it explicit allows tests
// and future multi-server processes to avoid accidentally sharing counters.
func NewMetrics() *Metrics {
	return &Metrics{http: make(map[httpMetricKey]*httpHistogram)}
}

func (metrics *Metrics) startRequest() {
	if metrics != nil {
		metrics.inFlight.Add(1)
	}
}

func (metrics *Metrics) finishRequest() {
	if metrics != nil {
		metrics.inFlight.Add(-1)
	}
}

// Observe records a completed HTTP request.  It is exported for adapters that
// serve HTTP outside of Gin, though normal application traffic uses HTTP.
func (metrics *Metrics) Observe(method, route string, status int, duration time.Duration) {
	if metrics == nil {
		return
	}
	if route == "" {
		route = "unmatched"
	}
	if duration < 0 {
		duration = 0
	}

	seconds := duration.Seconds()
	key := httpMetricKey{method: metricMethod(method), route: route, status: strconv.Itoa(status)}
	metrics.mu.Lock()
	if metrics.http == nil {
		metrics.http = make(map[httpMetricKey]*httpHistogram)
	}
	histogram := metrics.http[key]
	if histogram == nil {
		histogram = &httpHistogram{buckets: make([]uint64, len(defaultDurationBuckets))}
		metrics.http[key] = histogram
	}
	histogram.count++
	histogram.sum += seconds
	for index, bound := range defaultDurationBuckets {
		if seconds <= bound {
			histogram.buckets[index]++
		}
	}
	metrics.mu.Unlock()
}

// Handler exposes metrics in the Prometheus 0.0.4 text format.  Health gauges
// intentionally expose only component names, requiredness, and up/down state;
// internal error messages are never exported.
func (metrics *Metrics) Handler(checker *health.Checker) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, prometheusContentType, metrics.RenderPrometheus(c.Request.Context(), checker))
	}
}

// RenderPrometheus creates a stable metrics snapshot for tests and for the
// HTTP handler.  It is safe to call concurrently with Observe.
func (metrics *Metrics) RenderPrometheus(ctx context.Context, checker *health.Checker) []byte {
	if metrics == nil {
		metrics = NewMetrics()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var output bytes.Buffer
	output.WriteString("# HELP gopherai_http_requests_total Total number of completed HTTP requests.\n")
	output.WriteString("# TYPE gopherai_http_requests_total counter\n")

	metrics.writeHTTPSnapshot(&output)

	output.WriteString("# HELP gopherai_http_request_duration_seconds HTTP request duration in seconds.\n")
	output.WriteString("# TYPE gopherai_http_request_duration_seconds histogram\n")
	metrics.writeHTTPHistogram(&output)

	output.WriteString("# HELP gopherai_http_requests_in_flight HTTP requests currently being served.\n")
	output.WriteString("# TYPE gopherai_http_requests_in_flight gauge\n")
	fmt.Fprintf(&output, "gopherai_http_requests_in_flight %d\n", metrics.inFlight.Load())

	metrics.writeHealthSnapshot(&output, ctx, checker)
	return output.Bytes()
}

type httpSnapshot struct {
	key     httpMetricKey
	count   uint64
	sum     float64
	buckets []uint64
}

func (metrics *Metrics) snapshotHTTP() []httpSnapshot {
	if metrics == nil {
		return nil
	}
	metrics.mu.RLock()
	snapshot := make([]httpSnapshot, 0, len(metrics.http))
	for key, histogram := range metrics.http {
		snapshot = append(snapshot, httpSnapshot{
			key:     key,
			count:   histogram.count,
			sum:     histogram.sum,
			buckets: append([]uint64(nil), histogram.buckets...),
		})
	}
	metrics.mu.RUnlock()
	sort.Slice(snapshot, func(left, right int) bool {
		if snapshot[left].key.method != snapshot[right].key.method {
			return snapshot[left].key.method < snapshot[right].key.method
		}
		if snapshot[left].key.route != snapshot[right].key.route {
			return snapshot[left].key.route < snapshot[right].key.route
		}
		return snapshot[left].key.status < snapshot[right].key.status
	})
	return snapshot
}

func (metrics *Metrics) writeHTTPSnapshot(output *bytes.Buffer) {
	for _, snapshot := range metrics.snapshotHTTP() {
		labels := httpLabels(snapshot.key)
		fmt.Fprintf(output, "gopherai_http_requests_total{%s} %d\n", labels, snapshot.count)
	}
}

func (metrics *Metrics) writeHTTPHistogram(output *bytes.Buffer) {
	for _, snapshot := range metrics.snapshotHTTP() {
		labels := httpLabels(snapshot.key)
		for index, bound := range defaultDurationBuckets {
			fmt.Fprintf(output, "gopherai_http_request_duration_seconds_bucket{%s,le=%q} %d\n", labels, formatFloat(bound), snapshot.buckets[index])
		}
		fmt.Fprintf(output, "gopherai_http_request_duration_seconds_bucket{%s,le=\"+Inf\"} %d\n", labels, snapshot.count)
		fmt.Fprintf(output, "gopherai_http_request_duration_seconds_sum{%s} %s\n", labels, formatFloat(snapshot.sum))
		fmt.Fprintf(output, "gopherai_http_request_duration_seconds_count{%s} %d\n", labels, snapshot.count)
	}
}

func (metrics *Metrics) writeHealthSnapshot(output *bytes.Buffer, ctx context.Context, checker *health.Checker) {
	if checker == nil {
		return
	}
	output.WriteString("# HELP gopherai_application_accepting Whether the process is accepting new requests.\n")
	output.WriteString("# TYPE gopherai_application_accepting gauge\n")
	accepting := 0
	if checker.Accepting() {
		accepting = 1
	}
	fmt.Fprintf(output, "gopherai_application_accepting %d\n", accepting)

	report, ready := metrics.probeHealth(ctx, checker)
	output.WriteString("# HELP gopherai_health_ready Whether required health checks are ready.\n")
	output.WriteString("# TYPE gopherai_health_ready gauge\n")
	readyValue := 0
	if ready {
		readyValue = 1
	}
	fmt.Fprintf(output, "gopherai_health_ready %d\n", readyValue)

	output.WriteString("# HELP gopherai_health_component_up Health state of each application component.\n")
	output.WriteString("# TYPE gopherai_health_component_up gauge\n")
	componentNames := make([]string, 0, len(report.Checks))
	for name := range report.Checks {
		componentNames = append(componentNames, name)
	}
	sort.Strings(componentNames)
	for _, name := range componentNames {
		component := report.Checks[name]
		up := 0
		if component.Status == "up" {
			up = 1
		}
		fmt.Fprintf(output, "gopherai_health_component_up{component=%s,required=%s} %d\n", prometheusQuote(name), prometheusQuote(strconv.FormatBool(component.Required)), up)
	}
}

// probeHealth serializes and briefly caches dependency probes for /metrics.
// Metrics scrapes are often more frequent than readiness checks; rerunning
// MySQL/Redis probes for every scrape would let a monitoring spike amplify
// load on those dependencies. The cached report is intentionally short-lived
// and contains only the sanitized health fields rendered below.
func (metrics *Metrics) probeHealth(ctx context.Context, checker *health.Checker) (health.Report, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if metrics == nil {
		return checker.Probe(context.WithoutCancel(ctx))
	}
	metrics.healthMu.Lock()
	defer metrics.healthMu.Unlock()
	now := time.Now()
	accepting := checker.Accepting()
	if metrics.healthChecker == checker && metrics.healthAccepting == accepting && !metrics.healthCollected.IsZero() && now.Sub(metrics.healthCollected) < healthSnapshotTTL {
		return metrics.healthReport, metrics.healthReady
	}
	// A scraper disconnecting must not poison the short cache with a synthetic
	// all-down report. Checker applies its own bounded timeout, so retain values
	// but detach the probe from the individual HTTP request's cancellation.
	report, ready := checker.Probe(context.WithoutCancel(ctx))
	metrics.healthChecker = checker
	metrics.healthAccepting = accepting
	metrics.healthCollected = time.Now()
	metrics.healthReport = report
	metrics.healthReady = ready
	return report, ready
}

func httpLabels(key httpMetricKey) string {
	return "method=" + prometheusQuote(key.method) +
		",route=" + prometheusQuote(key.route) +
		",status=" + prometheusQuote(key.status)
}

func prometheusQuote(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\"", "\\\"")
	return "\"" + replacer.Replace(value) + "\""
}

func metricMethod(method string) string {
	switch strings.ToUpper(method) {
	case http.MethodGet:
		return http.MethodGet
	case http.MethodHead:
		return http.MethodHead
	case http.MethodPost:
		return http.MethodPost
	case http.MethodPut:
		return http.MethodPut
	case http.MethodPatch:
		return http.MethodPatch
	case http.MethodDelete:
		return http.MethodDelete
	case http.MethodOptions:
		return http.MethodOptions
	case http.MethodConnect:
		return http.MethodConnect
	case http.MethodTrace:
		return http.MethodTrace
	default:
		return "OTHER"
	}
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}
