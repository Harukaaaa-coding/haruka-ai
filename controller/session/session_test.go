package session

import (
	"bytes"
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"GopherAI/common/code"
	"GopherAI/model"
	sessionservice "GopherAI/service/session"

	"github.com/gin-gonic/gin"
)

func TestNewChatOptionsPrefersCatalogModelIDAndDeduplicatesKnowledgeBases(t *testing.T) {
	options, ok := newChatOptions(" rag ", "2", []string{" kb-1 ", "", "kb-1", "kb-2"})
	if !ok {
		t.Fatal("expected valid chat options")
	}
	if options.ModelType != "rag" {
		t.Fatalf("expected catalog model ID, got %q", options.ModelType)
	}
	if len(options.KnowledgeBaseIDs) != 2 || options.KnowledgeBaseIDs[0] != "kb-1" || options.KnowledgeBaseIDs[1] != "kb-2" {
		t.Fatalf("unexpected knowledge-base IDs: %#v", options.KnowledgeBaseIDs)
	}
}

func TestNewChatOptionsFallsBackToLegacySelector(t *testing.T) {
	options, ok := newChatOptions("", " 2 ", nil)
	if !ok || options.ModelType != "2" {
		t.Fatalf("expected legacy selector, got %#v, valid=%v", options, ok)
	}
	if _, ok := newChatOptions(" ", " ", nil); ok {
		t.Fatal("expected empty selectors to be rejected")
	}
	explicitAll, ok := newChatOptions("rag", "2", []string{})
	if !ok || !explicitAll.KnowledgeBasesSpecified {
		t.Fatal("explicit empty knowledge-base list should mean strict all")
	}
}

func TestParsePageLimit(t *testing.T) {
	tests := []struct {
		raw   string
		limit int
		valid bool
	}{
		{raw: "", limit: 0, valid: true},
		{raw: " 25 ", limit: 25, valid: true},
		{raw: "0", valid: false},
		{raw: "-1", valid: false},
		{raw: "not-a-number", valid: false},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			limit, valid := parsePageLimit(test.raw)
			if limit != test.limit || valid != test.valid {
				t.Fatalf("parsePageLimit(%q) = (%d, %v), want (%d, %v)", test.raw, limit, valid, test.limit, test.valid)
			}
		})
	}
}

func TestEmptySessionResponseKeepsArrayShape(t *testing.T) {
	encoded, err := json.Marshal(GetUserSessionsResponse{Sessions: []model.SessionInfo{}})
	if err != nil {
		t.Fatalf("marshal session response: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	sessions, ok := payload["sessions"].([]interface{})
	if !ok || len(sessions) != 0 {
		t.Fatalf("sessions shape = %#v, want empty array", payload["sessions"])
	}
}

func TestGetUserSessionsReturnsCursorPage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := getUserSessionsPageWithContext
	t.Cleanup(func() { getUserSessionsPageWithContext = previous })
	getUserSessionsPageWithContext = func(ctx context.Context, userName string, limit int, cursor string) (sessionservice.SessionPage, error) {
		if ctx == nil || userName != "alice" || limit != 2 || cursor != "cursor-1" {
			t.Fatalf("unexpected page request: ctx=%v user=%q limit=%d cursor=%q", ctx, userName, limit, cursor)
		}
		return sessionservice.SessionPage{
			Sessions:   []model.SessionInfo{{SessionID: "session-1", Title: "First"}},
			HasMore:    true,
			NextCursor: "cursor-2",
		}, nil
	}

	engine := gin.New()
	engine.GET("/", func(c *gin.Context) {
		c.Set("userName", "alice")
		GetUserSessionsByUserName(c)
	})
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/?limit=2&cursor=cursor-1", nil))

	var response struct {
		StatusCode code.Code           `json:"status_code"`
		Sessions   []model.SessionInfo `json:"sessions"`
		HasMore    bool                `json:"hasMore"`
		NextCursor string              `json:"nextCursor"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, recorder.Body.String())
	}
	if response.StatusCode != code.CodeSuccess || len(response.Sessions) != 1 || response.Sessions[0].SessionID != "session-1" || !response.HasMore || response.NextCursor != "cursor-2" {
		t.Fatalf("unexpected page response: %#v", response)
	}
}

func TestChatHistoryReturnsCursorPage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := getChatHistoryPageWithContext
	t.Cleanup(func() { getChatHistoryPageWithContext = previous })
	getChatHistoryPageWithContext = func(ctx context.Context, userName, sessionID string, limit int, cursor string) (sessionservice.HistoryPage, code.Code) {
		if ctx == nil || userName != "alice" || sessionID != "session-1" || limit != 2 || cursor != "older-cursor" {
			t.Fatalf("unexpected history request: ctx=%v user=%q session=%q limit=%d cursor=%q", ctx, userName, sessionID, limit, cursor)
		}
		return sessionservice.HistoryPage{
			History:    []model.History{{MessageID: "message-1", IsUser: true, Content: "hello"}},
			HasMore:    true,
			NextCursor: "oldest-cursor",
		}, code.CodeSuccess
	}

	engine := gin.New()
	engine.POST("/", func(c *gin.Context) {
		c.Set("userName", "alice")
		ChatHistory(c)
	})
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"sessionId":"session-1","limit":2,"cursor":"older-cursor"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	var response struct {
		StatusCode code.Code       `json:"status_code"`
		History    []model.History `json:"history"`
		HasMore    bool            `json:"hasMore"`
		NextCursor string          `json:"nextCursor"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, recorder.Body.String())
	}
	if response.StatusCode != code.CodeSuccess || len(response.History) != 1 || response.History[0].MessageID != "message-1" || !response.HasMore || response.NextCursor != "oldest-cursor" {
		t.Fatalf("unexpected history page response: %#v", response)
	}
}

func TestWriteSSEUsesJSONPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	if err := writeSSE(context, map[string]string{"content": "line one\nline two"}); err != nil {
		t.Fatalf("write SSE: %v", err)
	}
	body := recorder.Body.String()
	if !strings.HasPrefix(body, "data: {") || !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("invalid SSE frame: %q", body)
	}
	if !strings.Contains(body, `"content":"line one\nline two"`) {
		t.Fatalf("content was not safely JSON encoded: %q", body)
	}
}

func TestWriteSSEErrorPreservesBusinessCodeAndCompatibilityFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	writeSSEError(ginContext, code.CodeRecordNotFound, "Failed to send message")

	body := recorder.Body.String()
	if !strings.Contains(body, "event:error\n") {
		t.Fatalf("missing SSE error event: %q", body)
	}
	payload := decodeSSEPayload(t, body)
	assertStreamErrorPayload(t, payload, code.CodeRecordNotFound, "Failed to send message")
}

func TestChatStreamSendPreservesServiceBusinessCodes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := chatStreamSendWithOptions
	t.Cleanup(func() { chatStreamSendWithOptions = previous })

	for _, resultCode := range []code.Code{
		code.CodeInvalidParams,
		code.CodeRecordNotFound,
		code.CodeServerBusy,
		code.AIModelFail,
	} {
		t.Run(resultCode.Msg(), func(t *testing.T) {
			chatStreamSendWithOptions = func(
				context.Context,
				string,
				string,
				string,
				sessionservice.ChatOptions,
				http.ResponseWriter,
			) code.Code {
				return resultCode
			}

			engine := gin.New()
			engine.POST("/", func(c *gin.Context) {
				c.Set("userName", "alice")
				ChatStreamSend(c)
			})
			request := httptest.NewRequest(
				http.MethodPost,
				"/",
				strings.NewReader(`{"question":"hello","modelId":"4","sessionId":"session"}`),
			)
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)

			mediaType, parameters, err := mime.ParseMediaType(recorder.Header().Get("Content-Type"))
			if err != nil || mediaType != "text/event-stream" || parameters["charset"] != "utf-8" {
				t.Fatalf("content type = %q, want text/event-stream with utf-8 charset", recorder.Header().Get("Content-Type"))
			}
			payload := decodeSSEPayload(t, recorder.Body.String())
			assertStreamErrorPayload(t, payload, resultCode, "Failed to send message")
		})
	}
}

func TestChatHTTPHandlersRejectMalformedUnicodeBeforeJSONDecoding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handlers := []struct {
		name     string
		handler  gin.HandlerFunc
		existing bool
	}{
		{name: "new non-stream", handler: CreateSessionAndSendMessage},
		{name: "new stream", handler: CreateStreamSessionAndSendMessage},
		{name: "existing non-stream", handler: ChatSend, existing: true},
		{name: "existing stream", handler: ChatStreamSend, existing: true},
	}
	invalidQuestions := []struct {
		name  string
		value []byte
	}{
		{name: "invalid UTF-8", value: []byte{0xff}},
		{name: "isolated high surrogate", value: []byte(`\ud800`)},
		{name: "isolated low surrogate", value: []byte(`\udc00`)},
	}

	for _, handler := range handlers {
		for _, question := range invalidQuestions {
			t.Run(handler.name+"/"+question.name, func(t *testing.T) {
				engine := gin.New()
				engine.POST("/", func(c *gin.Context) {
					c.Set("userName", "alice")
					handler.handler(c)
				})
				request := httptest.NewRequest(
					http.MethodPost,
					"/",
					bytes.NewReader(chatRequestPayload(question.value, handler.existing)),
				)
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				engine.ServeHTTP(recorder, request)

				var response struct {
					StatusCode code.Code `json:"status_code"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
					t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
				}
				if response.StatusCode != code.CodeInvalidParams {
					t.Fatalf("status code = %d, want %d; body=%s", response.StatusCode, code.CodeInvalidParams, recorder.Body.String())
				}
			})
		}
	}
}

func TestStrictJSONBindingAllowsValidSurrogatePairsAndEscapedLiterals(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "paired surrogate",
			body: `{"question":"\ud83d\ude00","modelId":"4"}`,
			want: "😀",
		},
		{
			name: "escaped literal",
			body: `{"question":"\\ud800","modelId":"4"}`,
			want: `\ud800`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			ginContext.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			ginContext.Request.Header.Set("Content-Type", "application/json")
			request := new(CreateSessionAndSendMessageRequest)
			if err := bindStrictJSON(ginContext, request); err != nil {
				t.Fatalf("bind valid JSON: %v", err)
			}
			if request.UserQuestion != test.want {
				t.Fatalf("question = %q, want %q", request.UserQuestion, test.want)
			}
		})
	}
}

func chatRequestPayload(question []byte, existing bool) []byte {
	payload := append([]byte(`{"question":"`), question...)
	payload = append(payload, []byte(`","modelId":"4"`)...)
	if existing {
		payload = append(payload, []byte(`,"sessionId":"session"`)...)
	}
	return append(payload, '}')
}

func decodeSSEPayload(t *testing.T, body string) map[string]interface{} {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &payload); err != nil {
			t.Fatalf("decode SSE data %q: %v", line, err)
		}
		return payload
	}
	t.Fatalf("missing SSE data frame: %q", body)
	return nil
}

func assertStreamErrorPayload(t *testing.T, payload map[string]interface{}, resultCode code.Code, message string) {
	t.Helper()
	if got := int64(payload["status_code"].(float64)); got != resultCode.Code() {
		t.Fatalf("status_code = %d, want %d; payload=%#v", got, resultCode, payload)
	}
	if payload["status_msg"] != resultCode.Msg() {
		t.Fatalf("status_msg = %q, want %q", payload["status_msg"], resultCode.Msg())
	}
	if payload["message"] != message || payload["error"] != message {
		t.Fatalf("compatibility fields missing from payload: %#v", payload)
	}
}
