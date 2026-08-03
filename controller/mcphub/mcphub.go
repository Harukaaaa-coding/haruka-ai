package mcphub

import (
	"GopherAI/common/code"
	hub "GopherAI/common/mcphub"
	"GopherAI/controller"
	"GopherAI/dao/mcpaudit"
	"GopherAI/model"
	hubservice "GopherAI/service/mcphub"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

const maxRequestBodyBytes = 1 << 20

type ServersResponse struct {
	Servers []hub.ServerStatus `json:"servers"`
	Warning string             `json:"warning,omitempty"`
	controller.Response
}

type ServerResponse struct {
	Server hub.ServerStatus `json:"server"`
	controller.Response
}

type ToolsResponse struct {
	Tools []hub.ToolDefinition `json:"tools"`
	controller.Response
}

type CallToolRequest struct {
	ToolName      string         `json:"tool_name" binding:"required"`
	Arguments     map[string]any `json:"arguments"`
	ApprovalToken string         `json:"approval_token,omitempty"`
}

type CallToolResponse struct {
	*hub.InvocationResult
	controller.Response
}

type CreateApprovalRequest struct {
	ToolName  string         `json:"tool_name" binding:"required"`
	Arguments map[string]any `json:"arguments"`
}

type ApprovalChallengeResponse struct {
	Approval *hub.ApprovalChallenge `json:"approval"`
	controller.Response
}

type ApprovalTokenResponse struct {
	Approval *hub.IssuedApproval `json:"approval"`
	controller.Response
}

type AuditResponse struct {
	Audits []model.MCPAuditRecord `json:"audits"`
	Total  int64                  `json:"total"`
	Limit  int                    `json:"limit"`
	Offset int                    `json:"offset"`
	controller.Response
}

type ErrorResponse struct {
	ErrorCode string        `json:"error_code"`
	ToolName  string        `json:"tool_name,omitempty"`
	Risk      hub.RiskLevel `json:"risk,omitempty"`
	controller.Response
}

var getDefaultService = hubservice.Default

func ListServers(c *gin.Context) {
	service, err := getDefaultService()
	if err != nil {
		writeError(c, err)
		return
	}
	response := new(ServersResponse)
	response.Success()
	response.Servers = service.Servers(c.Request.Context())
	c.JSON(http.StatusOK, response)
}

func ReloadServers(c *gin.Context) {
	service, err := getDefaultService()
	if err != nil {
		writeError(c, err)
		return
	}
	servers, reloadErr := service.Reload(c.Request.Context())
	response := new(ServersResponse)
	response.Success()
	response.Servers = servers
	if reloadErr != nil {
		response.Warning = hub.PublicErrorMessage(reloadErr)
	}
	c.JSON(http.StatusOK, response)
}

func RefreshServer(c *gin.Context) {
	service, err := getDefaultService()
	if err != nil {
		writeError(c, err)
		return
	}
	server, err := service.Refresh(c.Request.Context(), c.Param("serverID"))
	if err != nil {
		writeError(c, err)
		return
	}
	response := &ServerResponse{Server: server}
	response.Success()
	c.JSON(http.StatusOK, response)
}

func ListTools(c *gin.Context) {
	service, err := getDefaultService()
	if err != nil {
		writeError(c, err)
		return
	}
	refresh, err := queryBool(c, "refresh", false)
	if err != nil {
		writeError(c, hubInvalidRequest("refresh must be true or false", err))
		return
	}
	tools, err := service.Tools(c.Request.Context(), refresh)
	if err != nil {
		writeError(c, err)
		return
	}
	response := &ToolsResponse{Tools: tools}
	response.Success()
	c.JSON(http.StatusOK, response)
}

func CallTool(c *gin.Context) {
	service, err := getDefaultService()
	if err != nil {
		writeError(c, err)
		return
	}
	request := new(CallToolRequest)
	if err := bindLimitedJSON(c, request); err != nil {
		writeError(c, hubInvalidRequest("invalid tool call request", err))
		return
	}
	result, err := service.Call(c.Request.Context(), hub.InvokeRequest{
		UserName:      c.GetString("userName"),
		ToolName:      strings.TrimSpace(request.ToolName),
		Arguments:     request.Arguments,
		ApprovalToken: request.ApprovalToken,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	response := &CallToolResponse{InvocationResult: result}
	response.Success()
	c.JSON(http.StatusOK, response)
}

func CreateApproval(c *gin.Context) {
	service, err := getDefaultService()
	if err != nil {
		writeError(c, err)
		return
	}
	request := new(CreateApprovalRequest)
	if err := bindLimitedJSON(c, request); err != nil {
		writeError(c, hubInvalidRequest("invalid approval request", err))
		return
	}
	challenge, err := service.CreateApproval(
		c.Request.Context(),
		c.GetString("userName"),
		strings.TrimSpace(request.ToolName),
		request.Arguments,
	)
	if err != nil {
		writeError(c, err)
		return
	}
	response := &ApprovalChallengeResponse{Approval: challenge}
	response.Success()
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, response)
}

func Approve(c *gin.Context) {
	service, err := getDefaultService()
	if err != nil {
		writeError(c, err)
		return
	}
	issued, err := service.Approve(c.GetString("userName"), c.Param("approvalID"))
	if err != nil {
		writeError(c, err)
		return
	}
	response := &ApprovalTokenResponse{Approval: issued}
	response.Success()
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, response)
}

func ListAudits(c *gin.Context) {
	service, err := getDefaultService()
	if err != nil {
		writeError(c, err)
		return
	}
	limit, err := queryInt(c, "limit", 50)
	if err != nil {
		writeError(c, hubInvalidRequest("limit must be an integer", err))
		return
	}
	offset, err := queryInt(c, "offset", 0)
	if err != nil {
		writeError(c, hubInvalidRequest("offset must be an integer", err))
		return
	}
	audits, total, err := service.Audits(c.Request.Context(), mcpaudit.Query{
		UserName: c.GetString("userName"),
		ServerID: c.Query("server_id"),
		ToolName: c.Query("tool_name"),
		Outcome:  c.Query("outcome"),
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		writeError(c, &hub.Error{Code: hub.ErrorInternal, Message: "audit query failed", Cause: err})
		return
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	response := &AuditResponse{Audits: audits, Total: total, Limit: limit, Offset: offset}
	response.Success()
	c.JSON(http.StatusOK, response)
}

func bindLimitedJSON(c *gin.Context, destination any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBodyBytes)
	return c.ShouldBindJSON(destination)
}

func queryBool(c *gin.Context, name string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return fallback, nil
	}
	return strconv.ParseBool(raw)
}

func queryInt(c *gin.Context, name string, fallback int) (int, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return fallback, nil
	}
	return strconv.Atoi(raw)
}

func hubInvalidRequest(message string, cause error) error {
	return &hub.Error{Code: hub.ErrorInvalidArguments, Message: message, Cause: cause}
}

func writeError(c *gin.Context, err error) {
	errorCode := hub.ErrorCode(err)
	response := &ErrorResponse{
		ErrorCode: errorCode,
		Response: controller.Response{
			StatusCode: statusCode(errorCode),
			StatusMsg:  hub.PublicErrorMessage(err),
		},
	}
	var required *hub.ApprovalRequiredError
	if errors.As(err, &required) {
		response.ToolName = required.ToolName
		response.Risk = required.Risk
	}
	c.JSON(httpStatus(errorCode), response)
}

func statusCode(errorCode string) code.Code {
	switch errorCode {
	case hub.ErrorInvalidArguments, hub.ErrorInvalidConfig:
		return code.CodeInvalidParams
	case hub.ErrorToolNotFound:
		return code.CodeRecordNotFound
	case hub.ErrorServerNotAllowed, hub.ErrorToolNotAllowed, hub.ErrorApprovalInvalid:
		return code.CodeForbidden
	case hub.ErrorApprovalRequired, hub.ErrorApprovalNotRequired:
		return code.Code(7001)
	case hub.ErrorTimeout:
		return code.Code(7002)
	case hub.ErrorServerUnavailable, hub.ErrorProtocol, hub.ErrorToolExecution:
		return code.Code(7003)
	default:
		return code.CodeServerBusy
	}
}

func httpStatus(errorCode string) int {
	switch errorCode {
	case hub.ErrorInvalidArguments, hub.ErrorInvalidConfig:
		return http.StatusBadRequest
	case hub.ErrorToolNotFound:
		return http.StatusNotFound
	case hub.ErrorServerNotAllowed, hub.ErrorToolNotAllowed, hub.ErrorApprovalInvalid:
		return http.StatusForbidden
	case hub.ErrorApprovalRequired:
		return http.StatusPreconditionRequired
	case hub.ErrorApprovalNotRequired:
		return http.StatusConflict
	case hub.ErrorTimeout:
		return http.StatusGatewayTimeout
	case hub.ErrorCanceled:
		return 499
	case hub.ErrorServerUnavailable, hub.ErrorProtocol, hub.ErrorToolExecution:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
