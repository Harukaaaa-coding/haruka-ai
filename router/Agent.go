package router

import (
	"GopherAI/controller/agent"
	"GopherAI/middleware/costguard"

	"github.com/gin-gonic/gin"
)

func AgentRouter(group *gin.RouterGroup, guard *costguard.Guard) {
	group.POST("/tasks", guard.JSON(costguard.ActionAgent), agent.CreateTask)
	group.GET("/tasks", agent.ListTasks)
	group.GET("/tasks/:taskID", agent.GetTask)
	group.POST("/tasks/:taskID/steps/:stepID/approve", guard.JSON(costguard.ActionAgent), agent.ApproveStep)
	group.POST("/tasks/:taskID/steps/:stepID/reject", agent.RejectStep)
	group.POST("/tasks/:taskID/resume", guard.JSON(costguard.ActionAgent), agent.ResumeTask)
	group.POST("/tasks/:taskID/cancel", agent.CancelTask)
}
