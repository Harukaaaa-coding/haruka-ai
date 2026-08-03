package router

import (
	"testing"

	"github.com/gin-gonic/gin"
)

func TestKnowledgeBaseRoutesAreRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	routes := InitRouter().Routes()
	registered := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		registered[route.Method+" "+route.Path] = struct{}{}
	}
	expected := []string{
		"POST /api/v1/file/knowledge-bases",
		"GET /api/v1/file/knowledge-bases",
		"GET /api/v1/file/knowledge-bases/:id",
		"DELETE /api/v1/file/knowledge-bases/:id",
		"POST /api/v1/file/knowledge-bases/:id/documents",
		"GET /api/v1/file/knowledge-bases/:id/documents",
		"GET /api/v1/file/knowledge-bases/:id/documents/:documentId/status",
		"DELETE /api/v1/file/knowledge-bases/:id/documents/:documentId",
		"POST /api/v1/file/knowledge-bases/:id/search",
		"GET /api/v1/file/index-tasks/:taskId",
	}
	for _, route := range expected {
		if _, ok := registered[route]; !ok {
			t.Errorf("route %q is not registered", route)
		}
	}
}
