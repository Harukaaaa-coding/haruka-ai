package router

import (
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAgentRoutesAreRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	routes := InitRouter().Routes()
	registered := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		registered[route.Method+" "+route.Path] = struct{}{}
	}
	expected := []string{
		"POST /api/v1/agent/tasks",
		"GET /api/v1/agent/tasks",
		"GET /api/v1/agent/tasks/:taskID",
		"POST /api/v1/agent/tasks/:taskID/steps/:stepID/approve",
		"POST /api/v1/agent/tasks/:taskID/steps/:stepID/reject",
		"POST /api/v1/agent/tasks/:taskID/resume",
		"POST /api/v1/agent/tasks/:taskID/cancel",
	}
	for _, route := range expected {
		if _, ok := registered[route]; !ok {
			t.Errorf("route %q is not registered", route)
		}
	}
}
