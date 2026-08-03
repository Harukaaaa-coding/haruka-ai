package router

import (
	"GopherAI/controller/file"
	"GopherAI/controller/knowledgebase"
	"GopherAI/middleware/costguard"

	"github.com/gin-gonic/gin"
)

func FileRouter(r *gin.RouterGroup, guard *costguard.Guard) {
	r.POST("/upload", guard.Multipart(costguard.ActionRAG), file.UploadRagFile)

	r.POST("/knowledge-bases", knowledgebase.Create)
	r.GET("/knowledge-bases", knowledgebase.List)
	r.GET("/knowledge-bases/:id", knowledgebase.Get)
	r.DELETE("/knowledge-bases/:id", knowledgebase.Delete)
	r.POST("/knowledge-bases/:id/documents", guard.Multipart(costguard.ActionRAG), knowledgebase.UploadDocument)
	r.GET("/knowledge-bases/:id/documents", knowledgebase.ListDocuments)
	r.GET("/knowledge-bases/:id/documents/:documentId/status", knowledgebase.GetDocumentStatus)
	r.DELETE("/knowledge-bases/:id/documents/:documentId", knowledgebase.DeleteDocument)
	r.POST("/knowledge-bases/:id/search", guard.JSON(costguard.ActionRAG), knowledgebase.Search)
	r.GET("/index-tasks/:taskId", knowledgebase.GetIndexTask)
}
