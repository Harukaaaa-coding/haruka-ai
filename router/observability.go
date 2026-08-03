package router

import (
	"GopherAI/common/health"
	"GopherAI/middleware/observability"
	"os"

	"github.com/gin-gonic/gin"
)

func registerObservabilityRoutes(engine *gin.Engine, checker *health.Checker, metrics *observability.Metrics) {
	engine.GET("/metrics", observability.MetricsAccess(os.Getenv("GOPHERAI_METRICS_TOKEN")), metrics.Handler(checker))
}
