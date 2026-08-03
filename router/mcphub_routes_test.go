package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	redisstore "GopherAI/common/redis"
	"GopherAI/middleware/costguard"

	"github.com/gin-gonic/gin"
)

func TestMCPHubRoutesAreRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	routes := InitRouter().Routes()
	registered := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		registered[route.Method+" "+route.Path] = struct{}{}
	}
	expected := []string{
		"GET /api/v1/mcp-hub/servers",
		"POST /api/v1/mcp-hub/servers/reload",
		"POST /api/v1/mcp-hub/servers/:serverID/refresh",
		"GET /api/v1/mcp-hub/tools",
		"POST /api/v1/mcp-hub/tools/call",
		"POST /api/v1/mcp-hub/approvals",
		"POST /api/v1/mcp-hub/approvals/:approvalID/approve",
		"GET /api/v1/mcp-hub/audits",
	}
	for _, route := range expected {
		if _, ok := registered[route]; !ok {
			t.Errorf("route %q is not registered", route)
		}
	}
}

func TestMCPRefreshRoutesUseCostGuardConditionally(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var limiterCalls atomic.Int64
	guard := costguard.New(costguard.DefaultConfig(), costguard.LimiterFunc(func(context.Context, ...redisstore.RateLimitRule) (bool, time.Duration, error) {
		limiterCalls.Add(1)
		return true, 0, nil
	}))
	stub := func(c *gin.Context) { c.Status(http.StatusNoContent) }
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("userName", "alice")
		c.Next()
	})
	registerMCPHubRoutes(engine.Group("/mcp"), guard, mcpHubRouteHandlers{
		listServers:    stub,
		reloadServers:  stub,
		refreshServer:  stub,
		listTools:      stub,
		callTool:       stub,
		createApproval: stub,
		approve:        stub,
		listAudits:     stub,
	})

	tests := []struct {
		name      string
		method    string
		path      string
		wantGuard bool
	}{
		{name: "pure tools list", method: http.MethodGet, path: "/mcp/tools"},
		{name: "explicit false", method: http.MethodGet, path: "/mcp/tools?refresh=false"},
		{name: "invalid refresh", method: http.MethodGet, path: "/mcp/tools?refresh=invalid"},
		{name: "refresh true", method: http.MethodGet, path: "/mcp/tools?refresh=true", wantGuard: true},
		{name: "refresh numeric true", method: http.MethodGet, path: "/mcp/tools?refresh=1", wantGuard: true},
		{name: "registry reload", method: http.MethodPost, path: "/mcp/servers/reload", wantGuard: true},
		{name: "server refresh", method: http.MethodPost, path: "/mcp/servers/weather/refresh", wantGuard: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := limiterCalls.Load()
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))
			if recorder.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
			}
			guarded := limiterCalls.Load() == before+1
			if guarded != test.wantGuard {
				t.Fatalf("guarded = %t, want %t", guarded, test.wantGuard)
			}
		})
	}
}
