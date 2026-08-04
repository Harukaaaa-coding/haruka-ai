package agent

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"GopherAI/common/code"
	"GopherAI/controller"
	"GopherAI/model"
	agentservice "GopherAI/service/agent"

	"github.com/gin-gonic/gin"
)

const maxRequestBodyBytes = 1 << 20

type CreateTaskRequest struct {
	Goal    string `json:"goal" binding:"required"`
	ModelID string `json:"model_id" binding:"required"`
}

// ApproveStepRequest binds an approval to the arguments the client rendered.
// The digest is required so a stale page or a replayed request cannot authorize
// a payload the reviewer never actually saw.
type ApproveStepRequest struct {
	ExpectedDigest string `json:"expected_digest" binding:"required"`
}

type RejectStepRequest struct {
	Reason string `json:"reason,omitempty"`
}

type ResumeTaskRequest struct {
	RetryUnknown bool `json:"retry_unknown,omitempty"`
}

type TaskResponse struct {
	Task *model.AgentTask `json:"task"`
	controller.Response
}

type TasksResponse struct {
	Tasks  []model.AgentTask `json:"tasks"`
	Total  int64             `json:"total"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
	controller.Response
}

var getDefaultService = agentservice.Default

func CreateTask(c *gin.Context) {
	request := new(CreateTaskRequest)
	if err := bindLimitedJSON(c, request); err != nil {
		writeError(c, agentservice.ErrInvalidInput)
		return
	}
	service, err := getDefaultService()
	if err != nil {
		writeError(c, err)
		return
	}
	task, err := service.CreateTask(
		c.Request.Context(),
		c.GetString("userName"),
		strings.TrimSpace(request.Goal),
		strings.TrimSpace(request.ModelID),
	)
	if err != nil {
		writeError(c, err)
		return
	}
	response := &TaskResponse{Task: task}
	response.Success()
	c.JSON(http.StatusAccepted, response)
}

func ListTasks(c *gin.Context) {
	limit, err := queryInt(c, "limit", 30)
	if err != nil {
		writeError(c, agentservice.ErrInvalidInput)
		return
	}
	offset, err := queryInt(c, "offset", 0)
	if err != nil {
		writeError(c, agentservice.ErrInvalidInput)
		return
	}
	service, err := getDefaultService()
	if err != nil {
		writeError(c, err)
		return
	}
	tasks, total, err := service.ListTasks(
		c.Request.Context(),
		c.GetString("userName"),
		strings.TrimSpace(c.Query("status")),
		limit,
		offset,
	)
	if err != nil {
		writeError(c, err)
		return
	}
	if limit <= 0 {
		limit = 30
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	response := &TasksResponse{Tasks: tasks, Total: total, Limit: limit, Offset: offset}
	response.Success()
	c.JSON(http.StatusOK, response)
}

func GetTask(c *gin.Context) {
	service, err := getDefaultService()
	if err != nil {
		writeError(c, err)
		return
	}
	task, err := service.GetTask(c.Request.Context(), c.GetString("userName"), c.Param("taskID"))
	if err != nil {
		writeError(c, err)
		return
	}
	response := &TaskResponse{Task: task}
	response.Success()
	c.JSON(http.StatusOK, response)
}

func ApproveStep(c *gin.Context) {
	request := new(ApproveStepRequest)
	if err := bindLimitedJSON(c, request); err != nil {
		writeError(c, agentservice.ErrInvalidInput)
		return
	}
	service, err := getDefaultService()
	if err != nil {
		writeError(c, err)
		return
	}
	task, err := service.ApproveTask(
		c.Request.Context(), c.GetString("userName"), c.Param("taskID"), c.Param("stepID"),
		strings.TrimSpace(request.ExpectedDigest),
	)
	if err != nil {
		writeError(c, err)
		return
	}
	writeAcceptedTask(c, task)
}

func RejectStep(c *gin.Context) {
	request := new(RejectStepRequest)
	if err := bindOptionalJSON(c, request); err != nil {
		writeError(c, agentservice.ErrInvalidInput)
		return
	}
	service, err := getDefaultService()
	if err != nil {
		writeError(c, err)
		return
	}
	task, err := service.RejectTask(
		c.Request.Context(), c.GetString("userName"), c.Param("taskID"), c.Param("stepID"), request.Reason,
	)
	if err != nil {
		writeError(c, err)
		return
	}
	writeAcceptedTask(c, task)
}

func ResumeTask(c *gin.Context) {
	request := new(ResumeTaskRequest)
	if err := bindOptionalJSON(c, request); err != nil {
		writeError(c, agentservice.ErrInvalidInput)
		return
	}
	service, err := getDefaultService()
	if err != nil {
		writeError(c, err)
		return
	}
	task, err := service.ResumeTask(
		c.Request.Context(), c.GetString("userName"), c.Param("taskID"), request.RetryUnknown,
	)
	if err != nil {
		writeError(c, err)
		return
	}
	writeAcceptedTask(c, task)
}

func CancelTask(c *gin.Context) {
	service, err := getDefaultService()
	if err != nil {
		writeError(c, err)
		return
	}
	task, err := service.CancelTask(c.Request.Context(), c.GetString("userName"), c.Param("taskID"))
	if err != nil {
		writeError(c, err)
		return
	}
	writeAcceptedTask(c, task)
}

func writeAcceptedTask(c *gin.Context, task *model.AgentTask) {
	response := &TaskResponse{Task: task}
	response.Success()
	c.JSON(http.StatusAccepted, response)
}

func bindLimitedJSON(c *gin.Context, destination any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBodyBytes)
	return c.ShouldBindJSON(destination)
}

func bindOptionalJSON(c *gin.Context, destination any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBodyBytes)
	err := c.ShouldBindJSON(destination)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func queryInt(c *gin.Context, name string, fallback int) (int, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return fallback, nil
	}
	return strconv.Atoi(raw)
}

func writeError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	responseCode := code.CodeServerBusy
	message := "agent service is unavailable"
	switch {
	case errors.Is(err, agentservice.ErrInvalidInput):
		status = http.StatusBadRequest
		responseCode = code.CodeInvalidParams
		message = "invalid agent task request"
	case errors.Is(err, agentservice.ErrNotFound):
		status = http.StatusNotFound
		responseCode = code.CodeRecordNotFound
		message = "agent task was not found"
	case errors.Is(err, agentservice.ErrConflict):
		status = http.StatusConflict
		responseCode = code.CodeForbidden
		message = "agent task state does not allow this operation"
	}
	c.JSON(status, controller.Response{StatusCode: responseCode, StatusMsg: message})
}
