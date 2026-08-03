package observability

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// MetricsAccess limits metrics scraping to loopback by default. Deployments
// with an in-network scraper can set GOPHERAI_METRICS_TOKEN and send it as a
// standard Bearer token; this keeps dependency-state metrics from becoming a
// public reconnaissance or amplification endpoint.
func MetricsAccess(token string) gin.HandlerFunc {
	token = strings.TrimSpace(token)
	return func(c *gin.Context) {
		if isLoopbackClient(c.ClientIP()) || validMetricsBearerToken(c.GetHeader("Authorization"), token) {
			c.Next()
			return
		}
		// A missing route is less useful to unauthenticated scanners than an
		// explicit authorization response, while legitimate scrapers get a
		// documented configuration path.
		c.AbortWithStatus(http.StatusNotFound)
	}
}

func validMetricsBearerToken(authorization, expected string) bool {
	if expected == "" {
		return false
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(authorization, prefix) {
		return false
	}
	provided := strings.TrimSpace(strings.TrimPrefix(authorization, prefix))
	if len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func isLoopbackClient(value string) bool {
	address := net.ParseIP(strings.TrimSpace(value))
	return address != nil && address.IsLoopback()
}
