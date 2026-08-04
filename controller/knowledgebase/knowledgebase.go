package knowledgebase

import (
	"GopherAI/common/code"
	"GopherAI/controller"
	"GopherAI/model"
	service "GopherAI/service/knowledgebase"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

type createRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

type createResponse struct {
	controller.Response
	KnowledgeBase *model.KnowledgeBase `json:"knowledge_base,omitempty"`
}

type listResponse struct {
	controller.Response
	KnowledgeBases []model.KnowledgeBaseSummary `json:"knowledge_bases"`
}

type detailResponse struct {
	controller.Response
	KnowledgeBase *model.KnowledgeBase      `json:"knowledge_base,omitempty"`
	Documents     []model.KnowledgeDocument `json:"documents"`
}

type uploadResponse struct {
	controller.Response
	Document *model.KnowledgeDocument  `json:"document,omitempty"`
	Task     *model.KnowledgeIndexTask `json:"index_task,omitempty"`
}

type documentsResponse struct {
	controller.Response
	Documents []model.KnowledgeDocument `json:"documents"`
}

type statusResponse struct {
	controller.Response
	Document *model.KnowledgeDocument  `json:"document,omitempty"`
	Task     *model.KnowledgeIndexTask `json:"index_task,omitempty"`
}

type searchRequest struct {
	Query string `json:"query" binding:"required"`
	TopK  int    `json:"top_k"`
}

type searchResponse struct {
	controller.Response
	References []model.KnowledgeReference `json:"references"`
}

type taskResponse struct {
	controller.Response
	Task *model.KnowledgeIndexTask `json:"index_task,omitempty"`
}

type deleteDocumentResponse struct {
	controller.Response
	Task *model.KnowledgeIndexTask `json:"index_task,omitempty"`
}

const multipartEnvelopeBytes int64 = 1 << 20

func Create(c *gin.Context) {
	response := new(createResponse)
	var request createRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, &response.Response, service.ErrInvalidInput)
		return
	}
	base, err := service.Create(c.GetString("userName"), request.Name, request.Description)
	if err != nil {
		writeError(c, &response.Response, err)
		return
	}
	response.Success()
	response.KnowledgeBase = base
	c.JSON(http.StatusOK, response)
}

func List(c *gin.Context) {
	response := new(listResponse)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	bases, err := service.List(c.GetString("userName"), page, pageSize)
	if err != nil {
		writeError(c, &response.Response, err)
		return
	}
	response.Success()
	response.KnowledgeBases = bases
	if response.KnowledgeBases == nil {
		response.KnowledgeBases = []model.KnowledgeBaseSummary{}
	}
	c.JSON(http.StatusOK, response)
}

func Get(c *gin.Context) {
	response := new(detailResponse)
	detail, err := service.Get(c.GetString("userName"), c.Param("id"))
	if err != nil {
		writeError(c, &response.Response, err)
		return
	}
	response.Success()
	response.KnowledgeBase = &detail.KnowledgeBase
	response.Documents = detail.Documents
	if response.Documents == nil {
		response.Documents = []model.KnowledgeDocument{}
	}
	c.JSON(http.StatusOK, response)
}

func Delete(c *gin.Context) {
	response := new(controller.Response)
	if err := service.Delete(c.Request.Context(), c.GetString("userName"), c.Param("id")); err != nil {
		writeError(c, response, err)
		return
	}
	response.Success()
	c.JSON(http.StatusOK, response)
}

func UploadDocument(c *gin.Context) {
	response := new(uploadResponse)
	header, err := controller.FormFileWithLimit(c, "file", service.MaxDocumentBytes+multipartEnvelopeBytes)
	if err != nil {
		if controller.IsRequestBodyTooLarge(err) {
			c.JSON(http.StatusRequestEntityTooLarge, response.CodeOf(code.CodeInvalidParams))
			return
		}
		writeError(c, &response.Response, service.ErrInvalidInput)
		return
	}
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	result, err := service.UploadDocument(c.Request.Context(), c.GetString("userName"), c.Param("id"), header)
	if err != nil {
		writeError(c, &response.Response, err)
		return
	}
	response.Success()
	response.Document = &result.Document
	response.Task = &result.Task
	c.JSON(http.StatusOK, response)
}

func ListDocuments(c *gin.Context) {
	response := new(documentsResponse)
	documents, err := service.ListDocuments(c.GetString("userName"), c.Param("id"))
	if err != nil {
		writeError(c, &response.Response, err)
		return
	}
	response.Success()
	response.Documents = documents
	if response.Documents == nil {
		response.Documents = []model.KnowledgeDocument{}
	}
	c.JSON(http.StatusOK, response)
}

func GetDocumentStatus(c *gin.Context) {
	response := new(statusResponse)
	status, err := service.GetDocumentStatus(c.GetString("userName"), c.Param("id"), c.Param("documentId"))
	if err != nil {
		writeError(c, &response.Response, err)
		return
	}
	response.Success()
	response.Document = &status.Document
	response.Task = status.Task
	c.JSON(http.StatusOK, response)
}

func DeleteDocument(c *gin.Context) {
	response := new(deleteDocumentResponse)
	task, err := service.ScheduleDeleteDocument(c.Request.Context(), c.GetString("userName"), c.Param("id"), c.Param("documentId"))
	if err != nil {
		writeError(c, &response.Response, err)
		return
	}
	response.Success()
	response.Task = task
	c.JSON(http.StatusAccepted, response)
}

func Search(c *gin.Context) {
	response := new(searchResponse)
	var request searchRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, &response.Response, service.ErrInvalidInput)
		return
	}
	references, err := service.Search(c.Request.Context(), c.GetString("userName"), c.Param("id"), request.Query, request.TopK)
	if err != nil {
		writeError(c, &response.Response, err)
		return
	}
	response.Success()
	response.References = references
	if response.References == nil {
		response.References = []model.KnowledgeReference{}
	}
	c.JSON(http.StatusOK, response)
}

func GetIndexTask(c *gin.Context) {
	response := new(taskResponse)
	task, err := service.GetIndexTask(c.GetString("userName"), c.Param("taskId"))
	if err != nil {
		writeError(c, &response.Response, err)
		return
	}
	response.Success()
	response.Task = task
	c.JSON(http.StatusOK, response)
}

func writeError(c *gin.Context, response *controller.Response, err error) {
	statusCode := code.CodeServerBusy
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		statusCode = code.CodeInvalidParams
	case errors.Is(err, service.ErrNotFound):
		statusCode = code.CodeRecordNotFound
	case errors.Is(err, service.ErrConflict):
		statusCode = code.CodeForbidden
	}
	c.JSON(http.StatusOK, response.CodeOf(statusCode))
}
