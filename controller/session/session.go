package session

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"GopherAI/common/code"
	"GopherAI/controller"
	"GopherAI/model"
	sessionservice "GopherAI/service/session"

	"github.com/gin-gonic/gin"
)

type (
	GetUserSessionsResponse struct {
		controller.Response
		Sessions   []model.SessionInfo `json:"sessions"`
		HasMore    bool                `json:"hasMore"`
		NextCursor string              `json:"nextCursor,omitempty"`
	}
	CreateSessionAndSendMessageRequest struct {
		UserQuestion     string   `json:"question" binding:"required"`
		ModelID          string   `json:"modelId,omitempty"`
		ModelType        string   `json:"modelType,omitempty"`
		KnowledgeBaseIDs []string `json:"knowledgeBaseIds,omitempty"`
	}
	CreateSessionAndSendMessageResponse struct {
		AIInformation string                     `json:"Information,omitempty"`
		SessionID     string                     `json:"sessionId,omitempty"`
		Citations     []model.KnowledgeReference `json:"citations,omitempty"`
		controller.Response
	}
	ChatSendRequest struct {
		UserQuestion     string   `json:"question" binding:"required"`
		ModelID          string   `json:"modelId,omitempty"`
		ModelType        string   `json:"modelType,omitempty"`
		SessionID        string   `json:"sessionId" binding:"required"`
		KnowledgeBaseIDs []string `json:"knowledgeBaseIds,omitempty"`
	}
	ChatSendResponse struct {
		AIInformation string                     `json:"Information,omitempty"`
		Citations     []model.KnowledgeReference `json:"citations,omitempty"`
		controller.Response
	}
	ChatHistoryRequest struct {
		SessionID string `json:"sessionId" binding:"required"`
		Limit     int    `json:"limit,omitempty"`
		Cursor    string `json:"cursor,omitempty"`
	}
	ChatHistoryResponse struct {
		History    []model.History `json:"history"`
		HasMore    bool            `json:"hasMore"`
		NextCursor string          `json:"nextCursor,omitempty"`
		controller.Response
	}
)

var (
	createStreamSessionOnlyWithContext = sessionservice.CreateStreamSessionOnlyWithContext
	streamMessageToExistingSession     = sessionservice.StreamMessageToExistingSessionWithOptions
	chatStreamSendWithOptions          = sessionservice.ChatStreamSendWithOptions
	getUserSessionsPageWithContext     = sessionservice.GetUserSessionsPageWithContext
	getChatHistoryPageWithContext      = sessionservice.GetChatHistoryPageWithContext
)

func GetUserSessionsByUserName(c *gin.Context) {
	res := new(GetUserSessionsResponse)
	limit, valid := parsePageLimit(c.Query("limit"))
	if !valid {
		c.JSON(http.StatusOK, res.CodeOf(code.CodeInvalidParams))
		return
	}
	userName := c.GetString("userName")
	page, err := getUserSessionsPageWithContext(c.Request.Context(), userName, limit, c.Query("cursor"))
	if err != nil {
		if errors.Is(err, sessionservice.ErrInvalidPageCursor) {
			c.JSON(http.StatusOK, res.CodeOf(code.CodeInvalidParams))
			return
		}
		c.JSON(http.StatusOK, res.CodeOf(code.CodeServerBusy))
		return
	}
	res.Success()
	res.Sessions = page.Sessions
	res.HasMore = page.HasMore
	res.NextCursor = page.NextCursor
	c.JSON(http.StatusOK, res)
}

func CreateSessionAndSendMessage(c *gin.Context) {
	req := new(CreateSessionAndSendMessageRequest)
	res := new(CreateSessionAndSendMessageResponse)
	if err := bindStrictJSON(c, req); err != nil {
		c.JSON(http.StatusOK, res.CodeOf(code.CodeInvalidParams))
		return
	}
	options, ok := newChatOptions(req.ModelID, req.ModelType, req.KnowledgeBaseIDs)
	if !ok {
		c.JSON(http.StatusOK, res.CodeOf(code.CodeInvalidParams))
		return
	}

	sessionID, result, resultCode := sessionservice.CreateSessionAndSendMessageWithOptions(
		c.Request.Context(), c.GetString("userName"), req.UserQuestion, options,
	)
	if resultCode != code.CodeSuccess {
		c.JSON(http.StatusOK, res.CodeOf(resultCode))
		return
	}
	res.Success()
	res.AIInformation = result.Content
	res.SessionID = sessionID
	res.Citations = result.Citations
	c.JSON(http.StatusOK, res)
}

func CreateStreamSessionAndSendMessage(c *gin.Context) {
	req := new(CreateSessionAndSendMessageRequest)
	if err := bindStrictJSON(c, req); err != nil {
		writeStreamJSONError(c, code.CodeInvalidParams, "Invalid parameters")
		return
	}
	options, ok := newChatOptions(req.ModelID, req.ModelType, req.KnowledgeBaseIDs)
	if !ok {
		writeStreamJSONError(c, code.CodeInvalidParams, "Invalid model selector")
		return
	}
	setSSEHeaders(c)
	streamContext, cancel := withSSEStreamTimeout(c.Request.Context())
	defer cancel()
	if streamContext.Err() != nil {
		return
	}
	streamWriter := newSSEStreamWriter(streamContext, cancel, c.Writer)

	sessionID, resultCode := createStreamSessionOnlyWithContext(streamContext, c.GetString("userName"), req.UserQuestion)
	if resultCode != code.CodeSuccess {
		writeSSEErrorTo(streamWriter, resultCode, "Failed to create session")
		return
	}
	if err := writeSSETo(streamWriter, map[string]string{"sessionId": sessionID}); err != nil {
		return
	}
	resultCode = streamMessageToExistingSession(
		streamContext, c.GetString("userName"), sessionID, req.UserQuestion, options, streamWriter,
	)
	if resultCode != code.CodeSuccess && streamContext.Err() == nil {
		writeSSEErrorTo(streamWriter, resultCode, "Failed to send message")
	}
}

func ChatSend(c *gin.Context) {
	req := new(ChatSendRequest)
	res := new(ChatSendResponse)
	if err := bindStrictJSON(c, req); err != nil {
		c.JSON(http.StatusOK, res.CodeOf(code.CodeInvalidParams))
		return
	}
	options, ok := newChatOptions(req.ModelID, req.ModelType, req.KnowledgeBaseIDs)
	if !ok {
		c.JSON(http.StatusOK, res.CodeOf(code.CodeInvalidParams))
		return
	}
	result, resultCode := sessionservice.ChatSendWithOptions(
		c.Request.Context(), c.GetString("userName"), req.SessionID, req.UserQuestion, options,
	)
	if resultCode != code.CodeSuccess {
		c.JSON(http.StatusOK, res.CodeOf(resultCode))
		return
	}
	res.Success()
	res.AIInformation = result.Content
	res.Citations = result.Citations
	c.JSON(http.StatusOK, res)
}

func ChatStreamSend(c *gin.Context) {
	req := new(ChatSendRequest)
	if err := bindStrictJSON(c, req); err != nil {
		writeStreamJSONError(c, code.CodeInvalidParams, "Invalid parameters")
		return
	}
	options, ok := newChatOptions(req.ModelID, req.ModelType, req.KnowledgeBaseIDs)
	if !ok {
		writeStreamJSONError(c, code.CodeInvalidParams, "Invalid model selector")
		return
	}
	setSSEHeaders(c)
	streamContext, cancel := withSSEStreamTimeout(c.Request.Context())
	defer cancel()
	if streamContext.Err() != nil {
		return
	}
	streamWriter := newSSEStreamWriter(streamContext, cancel, c.Writer)
	resultCode := chatStreamSendWithOptions(
		streamContext, c.GetString("userName"), req.SessionID, req.UserQuestion, options, streamWriter,
	)
	if resultCode != code.CodeSuccess && streamContext.Err() == nil {
		writeSSEErrorTo(streamWriter, resultCode, "Failed to send message")
	}
}

func ChatHistory(c *gin.Context) {
	req := new(ChatHistoryRequest)
	res := new(ChatHistoryResponse)
	if err := bindStrictJSON(c, req); err != nil || req.Limit < 0 {
		c.JSON(http.StatusOK, res.CodeOf(code.CodeInvalidParams))
		return
	}
	page, resultCode := getChatHistoryPageWithContext(
		c.Request.Context(), c.GetString("userName"), req.SessionID, req.Limit, req.Cursor,
	)
	if resultCode != code.CodeSuccess {
		c.JSON(http.StatusOK, res.CodeOf(resultCode))
		return
	}
	res.Success()
	res.History = page.History
	res.HasMore = page.HasMore
	res.NextCursor = page.NextCursor
	c.JSON(http.StatusOK, res)
}

// parsePageLimit distinguishes an omitted limit (the service default applies)
// from malformed or negative values. Oversized positive values are safely
// capped by the service so clients can use a stable desired page size.
func parsePageLimit(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 {
		return 0, false
	}
	return limit, true
}

func newChatOptions(modelID, modelType string, knowledgeBaseIDs []string) (sessionservice.ChatOptions, bool) {
	selector := strings.TrimSpace(modelID)
	if selector == "" {
		selector = strings.TrimSpace(modelType)
	}
	if selector == "" {
		return sessionservice.ChatOptions{}, false
	}
	ids := make([]string, 0, len(knowledgeBaseIDs))
	seen := make(map[string]struct{}, len(knowledgeBaseIDs))
	for _, id := range knowledgeBaseIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return sessionservice.ChatOptions{
		ModelType:               selector,
		KnowledgeBaseIDs:        ids,
		KnowledgeBasesSpecified: knowledgeBaseIDs != nil,
	}, true
}

func setSSEHeaders(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("X-Accel-Buffering", "no")
}

func streamErrorPayload(resultCode code.Code, message string) gin.H {
	return gin.H{
		"error":       message,
		"message":     message,
		"status_code": resultCode,
		"status_msg":  resultCode.Msg(),
	}
}

func writeStreamJSONError(c *gin.Context, resultCode code.Code, message string) {
	c.JSON(http.StatusOK, streamErrorPayload(resultCode, message))
}

func writeSSEError(c *gin.Context, resultCode code.Code, message string) {
	writeSSEErrorTo(c.Writer, resultCode, message)
}

func writeSSE(c *gin.Context, payload interface{}) error {
	return writeSSETo(c.Writer, payload)
}

func writeSSEErrorTo(writer http.ResponseWriter, resultCode code.Code, message string) {
	_ = writeSSEFrame(writer, "error", streamErrorPayload(resultCode, message))
}

func writeSSETo(writer http.ResponseWriter, payload interface{}) error {
	return writeSSEFrame(writer, "", payload)
}

func writeSSEFrame(writer http.ResponseWriter, event string, payload interface{}) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	frame := make([]byte, 0, len(event)+len(encoded)+16)
	if event != "" {
		frame = append(frame, "event:"...)
		frame = append(frame, event...)
		frame = append(frame, '\n')
	}
	frame = append(frame, "data: "...)
	frame = append(frame, encoded...)
	frame = append(frame, '\n', '\n')
	if _, err := writer.Write(frame); err != nil {
		return err
	}
	flusher, ok := writer.(http.Flusher)
	if !ok {
		return http.ErrNotSupported
	}
	flusher.Flush()
	return nil
}
