package router

import (
	"GopherAI/common/health"
	"GopherAI/middleware/costguard"
	"GopherAI/middleware/jwt"
	"GopherAI/middleware/observability"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	frontendDistDir    = "vue-frontend/dist"
	maxMultipartMemory = 8 << 20
)

func InitRouter(checkers ...*health.Checker) *gin.Engine {
	// Gin's default logger renders URLs directly and its debug recovery handler
	// may dump Cookie headers.  The observability middlewares use route templates
	// and metadata-only JSON records instead.
	accessLogger := observability.NewJSONLogger(gin.DefaultWriter)
	metrics := observability.NewMetrics()
	r := gin.New()
	r.Use(observability.HTTP(metrics, accessLogger), observability.Recovery(accessLogger))
	r.MaxMultipartMemory = maxMultipartMemory
	// Do not trust client-supplied forwarding headers by default. Deployments
	// behind a reverse proxy should configure only that proxy's exact CIDR.
	_ = r.SetTrustedProxies(nil)
	checker := selectHealthChecker(checkers)
	registerHealthRoutes(r, checker)
	registerObservabilityRoutes(r, checker, metrics)

	api := r.Group("/api/v1")
	RegisterUserRouter(api.Group("/user"))
	userSessionGroup := api.Group("/user")
	userSessionGroup.Use(jwt.Auth())
	RegisterAuthenticatedUserRouter(userSessionGroup)
	costGuard := costguard.NewDefault()

	aiGroup := api.Group("/AI")
	aiGroup.Use(jwt.Auth())
	AIRouter(aiGroup, costGuard)

	imageGroup := api.Group("/image")
	imageGroup.Use(jwt.Auth())
	ImageRouter(imageGroup, costGuard)

	fileGroup := api.Group("/file")
	fileGroup.Use(jwt.Auth())
	FileRouter(fileGroup, costGuard)

	mcpHubGroup := api.Group("/mcp-hub")
	mcpHubGroup.Use(jwt.Auth())
	MCPHubRouter(mcpHubGroup, costGuard)

	agentGroup := api.Group("/agent")
	agentGroup.Use(jwt.Auth())
	AgentRouter(agentGroup, costGuard)

	configureSPA(r, frontendDistDir)
	return r
}

func selectHealthChecker(checkers []*health.Checker) *health.Checker {
	if len(checkers) > 0 && checkers[0] != nil {
		return checkers[0]
	}
	checker := health.NewChecker(time.Second)
	checker.MarkReady()
	return checker
}

func registerHealthRoutes(r *gin.Engine, checker *health.Checker) {
	r.GET("/livez", func(c *gin.Context) {
		phase := "serving"
		if !checker.Accepting() {
			phase = "draining"
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"status": "ok", "phase": phase})
	})
	readiness := func(c *gin.Context) {
		report, ready := checker.Probe(c.Request.Context())
		status := http.StatusOK
		if !ready {
			status = http.StatusServiceUnavailable
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(status, report)
	}
	r.GET("/readyz", readiness)
	r.GET("/healthz", readiness)
}

func configureSPA(r *gin.Engine, distDir string) {
	distPath, err := filepath.Abs(distDir)
	if err != nil {
		distPath = distDir
	}
	indexPath := filepath.Join(distPath, "index.html")
	indexInfo, indexErr := os.Stat(indexPath)
	frontendAvailable := indexErr == nil && indexInfo.Mode().IsRegular()

	r.NoRoute(func(c *gin.Context) {
		requestPath := c.Request.URL.Path
		if requestPath == "/api" || strings.HasPrefix(requestPath, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"status_code": http.StatusNotFound, "status_msg": "API route not found"})
			return
		}
		if !frontendAvailable || (c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead) {
			c.JSON(http.StatusNotFound, gin.H{"status_code": http.StatusNotFound, "status_msg": "route not found"})
			return
		}

		cleanPath := strings.TrimPrefix(path.Clean("/"+requestPath), "/")
		candidate := filepath.Join(distPath, filepath.FromSlash(cleanPath))
		if pathWithinRoot(distPath, candidate) {
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				c.File(candidate)
				return
			}
		}
		c.File(indexPath)
	})
}

func pathWithinRoot(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
