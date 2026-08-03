package router

import (
	"GopherAI/controller/mcphub"
	"GopherAI/middleware/costguard"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

func MCPHubRouter(group *gin.RouterGroup, guard *costguard.Guard) {
	registerMCPHubRoutes(group, guard, mcpHubRouteHandlers{
		listServers:    mcphub.ListServers,
		reloadServers:  mcphub.ReloadServers,
		refreshServer:  mcphub.RefreshServer,
		listTools:      mcphub.ListTools,
		callTool:       mcphub.CallTool,
		createApproval: mcphub.CreateApproval,
		approve:        mcphub.Approve,
		listAudits:     mcphub.ListAudits,
	})
}

type mcpHubRouteHandlers struct {
	listServers    gin.HandlerFunc
	reloadServers  gin.HandlerFunc
	refreshServer  gin.HandlerFunc
	listTools      gin.HandlerFunc
	callTool       gin.HandlerFunc
	createApproval gin.HandlerFunc
	approve        gin.HandlerFunc
	listAudits     gin.HandlerFunc
}

func registerMCPHubRoutes(group *gin.RouterGroup, guard *costguard.Guard, handlers mcpHubRouteHandlers) {
	group.GET("/servers", handlers.listServers)
	group.POST("/servers/reload", guard.NoBody(costguard.ActionMCP), handlers.reloadServers)
	group.POST("/servers/:serverID/refresh", guard.NoBody(costguard.ActionMCP), handlers.refreshServer)
	group.GET("/tools", costguard.Conditional(mcpToolsRefreshRequested, guard.NoBody(costguard.ActionMCP)), handlers.listTools)
	group.POST("/tools/call", guard.JSON(costguard.ActionMCP), handlers.callTool)
	group.POST("/approvals", guard.JSON(costguard.ActionMCP), handlers.createApproval)
	group.POST("/approvals/:approvalID/approve", guard.NoBody(costguard.ActionMCP), handlers.approve)
	group.GET("/audits", handlers.listAudits)
}

func mcpToolsRefreshRequested(c *gin.Context) bool {
	raw := strings.TrimSpace(c.Query("refresh"))
	if raw == "" {
		return false
	}
	refresh, err := strconv.ParseBool(raw)
	return err == nil && refresh
}
