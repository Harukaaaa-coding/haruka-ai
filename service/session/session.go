package session

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"GopherAI/common/aihelper"
	"GopherAI/common/code"
	"GopherAI/common/rag"
	appconfig "GopherAI/config"
	messagedao "GopherAI/dao/message"
	sessiondao "GopherAI/dao/session"
	"GopherAI/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ChatOptions carries request-scoped routing and retrieval choices. ModelType
// accepts both the catalog pipeline ID and the legacy numeric selector.
type ChatOptions struct {
	ModelType               string
	KnowledgeBaseIDs        []string
	KnowledgeBasesSpecified bool
}

type ChatResult struct {
	Content   string                     `json:"content"`
	Citations []model.KnowledgeReference `json:"citations,omitempty"`
}

// StreamSink is deliberately transport-neutral.  The chat service owns
// session authorization, history hydration, model invocation and persistence;
// callers own how a text delta, citation, and completion signal reach the
// client.  SSE and realtime voice/WebSocket transports can therefore share
// one conversation path instead of duplicating model/session logic.
type StreamSink interface {
	TextDelta(context.Context, string) error
	Citations(context.Context, []model.KnowledgeReference) error
	Complete(context.Context) error
}

// StreamSinkFuncs is a small adapter useful for transports and tests that do
// not need a concrete sink type.
type StreamSinkFuncs struct {
	OnTextDelta func(context.Context, string) error
	OnCitations func(context.Context, []model.KnowledgeReference) error
	OnComplete  func(context.Context) error
}

func (s StreamSinkFuncs) TextDelta(ctx context.Context, content string) error {
	if s.OnTextDelta == nil {
		return nil
	}
	return s.OnTextDelta(ctx, content)
}

func (s StreamSinkFuncs) Citations(ctx context.Context, citations []model.KnowledgeReference) error {
	if s.OnCitations == nil {
		return nil
	}
	return s.OnCitations(ctx, citations)
}

func (s StreamSinkFuncs) Complete(ctx context.Context) error {
	if s.OnComplete == nil {
		return nil
	}
	return s.OnComplete(ctx)
}

const (
	maxUserQuestionRunes = 16_000
	maxSessionTitleRunes = 100
	maxUserNameRunes     = 50
	maxModelHistoryItems = 64

	defaultSessionPageSize = 50
	defaultHistoryPageSize = 50
	maxPageSize            = 100
	maxPageCursorLength    = 512
)

// ErrInvalidPageCursor is returned only for malformed or mismatched cursors.
// The cursor is intentionally opaque to callers so the ordering keys remain an
// implementation detail and can be extended in a future cursor version.
var ErrInvalidPageCursor = errors.New("page cursor is invalid")

type SessionPage struct {
	Sessions   []model.SessionInfo `json:"sessions"`
	HasMore    bool                `json:"hasMore"`
	NextCursor string              `json:"nextCursor,omitempty"`
}

type HistoryPage struct {
	History    []model.History `json:"history"`
	HasMore    bool            `json:"hasMore"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

type sessionPageCursor struct {
	Version   int       `json:"v"`
	UpdatedAt time.Time `json:"updatedAt"`
	ID        string    `json:"id"`
}

type historyPageCursor struct {
	Version   int       `json:"v"`
	CreatedAt time.Time `json:"createdAt"`
	ID        uint      `json:"id"`
}

type sessionGate struct {
	token chan struct{}
	refs  int
}

type sessionGateRegistry struct {
	mu    sync.Mutex
	gates map[string]*sessionGate
}

var (
	sessionRequestGates     = sessionGateRegistry{gates: make(map[string]*sessionGate)}
	lookupOwnedSession      = sessiondao.GetOwnedSession
	loadOwnedMessagesPage   = messagedao.GetMessagesPageBySessionID
	loadRecentOwnedMessages = messagedao.GetRecentMessagesBySessionID
	listOwnedSessionsPage   = sessiondao.ListSessionsPageByUserName
	createSession           = sessiondao.CreateSessionWithContext
	touchOwnedSession       = sessiondao.TouchOwnedSession
)

func lockSession(ctx context.Context, userName, sessionID string) (func(), error) {
	ctx = contextOrBackground(ctx)
	key := userName + "\x00" + sessionID
	sessionRequestGates.mu.Lock()
	gate := sessionRequestGates.gates[key]
	if gate == nil {
		gate = &sessionGate{token: make(chan struct{}, 1)}
		sessionRequestGates.gates[key] = gate
	}
	// refs includes both the current owner and every waiter. A gate may be
	// deleted only after the last of them leaves; otherwise a new caller could
	// create a second gate and bypass serialization for the same session.
	gate.refs++
	sessionRequestGates.mu.Unlock()

	select {
	case gate.token <- struct{}{}:
		var releaseOnce sync.Once
		return func() {
			releaseOnce.Do(func() {
				<-gate.token
				releaseSessionGate(key, gate)
			})
		}, nil
	case <-ctx.Done():
		releaseSessionGate(key, gate)
		return nil, ctx.Err()
	}
}

func releaseSessionGate(key string, gate *sessionGate) {
	if gate == nil {
		return
	}
	sessionRequestGates.mu.Lock()
	defer sessionRequestGates.mu.Unlock()
	if sessionRequestGates.gates[key] != gate {
		return
	}
	gate.refs--
	if gate.refs == 0 {
		delete(sessionRequestGates.gates, key)
	}
}

func modelConfig(userName string) map[string]interface{} {
	conf := appconfig.GetConfig()
	return map[string]interface{}{
		"username":  userName,
		"baseURL":   conf.OllamaConfig.OllamaBaseURL,
		"modelName": conf.OllamaConfig.OllamaModelName,
	}
}

func normalizeUserQuestion(question string) (string, bool) {
	question = strings.TrimSpace(question)
	if question == "" || !utf8.ValidString(question) || utf8.RuneCountInString(question) > maxUserQuestionRunes {
		return "", false
	}
	return question, true
}

func normalizeUserName(userName string) (string, bool) {
	userName = strings.TrimSpace(userName)
	if userName == "" || !utf8.ValidString(userName) || utf8.RuneCountInString(userName) > maxUserNameRunes {
		return "", false
	}
	return userName, true
}

func sessionTitle(question string) string {
	runes := []rune(question)
	if len(runes) > maxSessionTitleRunes {
		runes = runes[:maxSessionTitleRunes]
	}
	return string(runes)
}

func authorizeSession(ctx context.Context, userName, sessionID string) code.Code {
	var valid bool
	userName, valid = normalizeUserName(userName)
	sessionID = strings.TrimSpace(sessionID)
	if !valid || sessionID == "" {
		return code.CodeInvalidParams
	}
	if _, err := lookupOwnedSession(contextOrBackground(ctx), userName, sessionID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Unknown and foreign-owned IDs intentionally share one result so the
			// authorization check does not become a session-enumeration oracle.
			return code.CodeRecordNotFound
		}
		log.Printf("authorize chat session: %v", err)
		return code.CodeServerBusy
	}
	return code.CodeSuccess
}

func GetUserSessionsByUserName(userName string) ([]model.SessionInfo, error) {
	return GetUserSessionsByUserNameWithContext(context.Background(), userName)
}

// GetUserSessionsByUserNameWithContext retains the legacy list-shaped result
// while applying the same bounded default page as the HTTP API. New callers
// should use GetUserSessionsPageWithContext to follow the cursor.
func GetUserSessionsByUserNameWithContext(ctx context.Context, userName string) ([]model.SessionInfo, error) {
	page, err := GetUserSessionsPageWithContext(ctx, userName, defaultSessionPageSize, "")
	if err != nil {
		return nil, err
	}
	return page.Sessions, nil
}

// GetUserSessionsPageWithContext loads a newest-first, keyset-paginated slice
// of a user's durable sessions. It keeps cancellation/deadlines on the
// database query and never materializes the user's complete history in memory.
func GetUserSessionsPageWithContext(ctx context.Context, userName string, limit int, cursor string) (SessionPage, error) {
	userName, valid := normalizeUserName(userName)
	if !valid {
		return SessionPage{}, errors.New("session user name is invalid")
	}
	beforeUpdatedAt, beforeID, err := decodeSessionPageCursor(cursor)
	if err != nil {
		return SessionPage{}, err
	}
	sessions, hasMore, err := listOwnedSessionsPage(
		contextOrBackground(ctx), userName, beforeUpdatedAt, beforeID, normalizePageLimit(limit, defaultSessionPageSize),
	)
	if err != nil {
		return SessionPage{}, err
	}

	sessionInfos := make([]model.SessionInfo, 0, len(sessions))
	for _, session := range sessions {
		sessionInfos = append(sessionInfos, model.SessionInfo{
			SessionID: session.ID,
			Title:     session.Title,
		})
	}
	page := SessionPage{Sessions: sessionInfos, HasMore: hasMore}
	if hasMore && len(sessions) > 0 {
		page.NextCursor = encodeSessionPageCursor(sessions[len(sessions)-1])
	}
	return page, nil
}

func normalizePageLimit(limit, fallback int) int {
	if limit < 1 {
		return fallback
	}
	if limit > maxPageSize {
		return maxPageSize
	}
	return limit
}

func encodeSessionPageCursor(session model.Session) string {
	return encodePageCursor(sessionPageCursor{
		Version:   1,
		UpdatedAt: session.UpdatedAt,
		ID:        session.ID,
	})
}

func encodeHistoryPageCursor(message model.Message) string {
	return encodePageCursor(historyPageCursor{
		Version:   1,
		CreatedAt: message.CreatedAt,
		ID:        message.ID,
	})
}

func encodePageCursor(value interface{}) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		// The only call sites use fixed structs containing JSON-safe values. Do
		// not issue a malformed cursor if that invariant is ever broken.
		log.Printf("encode page cursor: %v", err)
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeSessionPageCursor(raw string) (time.Time, string, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, "", nil
	}
	var cursor sessionPageCursor
	if err := decodePageCursor(raw, &cursor); err != nil || cursor.Version != 1 || cursor.UpdatedAt.IsZero() || strings.TrimSpace(cursor.ID) == "" {
		return time.Time{}, "", ErrInvalidPageCursor
	}
	return cursor.UpdatedAt, cursor.ID, nil
}

func decodeHistoryPageCursor(raw string) (time.Time, uint, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, 0, nil
	}
	var cursor historyPageCursor
	if err := decodePageCursor(raw, &cursor); err != nil || cursor.Version != 1 || cursor.CreatedAt.IsZero() || cursor.ID == 0 {
		return time.Time{}, 0, ErrInvalidPageCursor
	}
	return cursor.CreatedAt, cursor.ID, nil
}

func decodePageCursor(raw string, target interface{}) error {
	raw = strings.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > maxPageCursorLength {
		return ErrInvalidPageCursor
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return fmt.Errorf("%w: decode", ErrInvalidPageCursor)
	}
	if err := json.Unmarshal(decoded, target); err != nil {
		return fmt.Errorf("%w: parse", ErrInvalidPageCursor)
	}
	return nil
}

// CreateSessionAndSendMessage preserves the legacy service contract.
func CreateSessionAndSendMessage(userName, userQuestion, modelType string) (string, string, code.Code) {
	sessionID, result, resultCode := CreateSessionAndSendMessageWithOptions(
		context.Background(), userName, userQuestion, ChatOptions{ModelType: modelType},
	)
	return sessionID, result.Content, resultCode
}

func CreateSessionAndSendMessageWithOptions(ctx context.Context, userName, userQuestion string, options ChatOptions) (string, ChatResult, code.Code) {
	var valid bool
	userName, valid = normalizeUserName(userName)
	if !valid {
		return "", ChatResult{}, code.CodeInvalidParams
	}
	userQuestion, valid = normalizeUserQuestion(userQuestion)
	if !valid {
		return "", ChatResult{}, code.CodeInvalidParams
	}
	newSession := &model.Session{
		ID:       uuid.NewString(),
		UserName: userName,
		Title:    sessionTitle(userQuestion),
	}
	createdSession, err := createSession(contextOrBackground(ctx), newSession)
	if err != nil {
		log.Printf("create chat session: %v", err)
		return "", ChatResult{}, code.CodeServerBusy
	}

	result, resultCode := generateResponse(ctx, userName, createdSession.ID, userQuestion, options)
	if resultCode != code.CodeSuccess {
		return createdSession.ID, ChatResult{}, resultCode
	}
	return createdSession.ID, result, code.CodeSuccess
}

func CreateStreamSessionOnly(userName, userQuestion string) (string, code.Code) {
	return CreateStreamSessionOnlyWithContext(context.Background(), userName, userQuestion)
}

func CreateStreamSessionOnlyWithContext(ctx context.Context, userName, userQuestion string) (string, code.Code) {
	var valid bool
	userName, valid = normalizeUserName(userName)
	if !valid {
		return "", code.CodeInvalidParams
	}
	userQuestion, valid = normalizeUserQuestion(userQuestion)
	if !valid {
		return "", code.CodeInvalidParams
	}
	newSession := &model.Session{
		ID:       uuid.NewString(),
		UserName: userName,
		Title:    sessionTitle(userQuestion),
	}
	createdSession, err := createSession(contextOrBackground(ctx), newSession)
	if err != nil {
		log.Printf("create stream chat session: %v", err)
		return "", code.CodeServerBusy
	}
	return createdSession.ID, code.CodeSuccess
}

// getOrCreateLoadedHelper hydrates a model context only when the user opens or
// resumes that session. The process no longer reconstructs every historical
// chat at startup; only the recent tail is needed for the next model prompt.
// Callers hold the per-session gate before invoking this function, so a newly
// created helper cannot be observed half-hydrated by another chat request.
func getOrCreateLoadedHelper(ctx context.Context, userName, sessionID string, options ChatOptions) (*aihelper.AIHelper, error) {
	manager := aihelper.GetGlobalManager()
	if _, exists := manager.GetAIHelper(userName, sessionID); exists {
		return manager.GetOrCreateAIHelper(userName, sessionID, options.ModelType, modelConfig(userName))
	}

	persisted, err := loadRecentOwnedMessages(contextOrBackground(ctx), userName, sessionID, maxModelHistoryItems)
	if err != nil {
		return nil, fmt.Errorf("load recent chat context: %w", err)
	}
	helper, err := manager.GetOrCreateAIHelper(userName, sessionID, options.ModelType, modelConfig(userName))
	if err != nil {
		return nil, err
	}
	for index := range persisted {
		if err := helper.RestoreMessage(&persisted[index]); err != nil {
			manager.RemoveAIHelper(userName, sessionID)
			return nil, fmt.Errorf("restore chat context: %w", err)
		}
	}
	return helper, nil
}

// StreamMessageToExistingSession preserves the legacy service contract.
func StreamMessageToExistingSession(userName, sessionID, userQuestion, modelType string, writer http.ResponseWriter) code.Code {
	return StreamMessageToExistingSessionWithOptions(
		context.Background(), userName, sessionID, userQuestion, ChatOptions{ModelType: modelType}, writer,
	)
}

func StreamMessageToExistingSessionWithOptions(ctx context.Context, userName, sessionID, userQuestion string, options ChatOptions, writer http.ResponseWriter) code.Code {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		log.Printf("stream chat response is unsupported")
		return code.CodeServerBusy
	}
	return StreamMessageToExistingSessionWithSink(ctx, userName, sessionID, userQuestion, options, sseStreamSink{
		writer:  writer,
		flusher: flusher,
	})
}

// StreamMessageToExistingSessionWithSink streams one already-authorized
// session turn through a caller-provided transport.  It is the canonical
// streaming path used by HTTP SSE and realtime voice transports.
func StreamMessageToExistingSessionWithSink(ctx context.Context, userName, sessionID, userQuestion string, options ChatOptions, sink StreamSink) code.Code {
	if sink == nil {
		return code.CodeServerBusy
	}
	var valid bool
	userQuestion, valid = normalizeUserQuestion(userQuestion)
	if !valid {
		return code.CodeInvalidParams
	}
	userName = strings.TrimSpace(userName)
	sessionID = strings.TrimSpace(sessionID)
	if resultCode := authorizeSession(ctx, userName, sessionID); resultCode != code.CodeSuccess {
		return resultCode
	}
	unlock, err := lockSession(ctx, userName, sessionID)
	if err != nil {
		log.Printf("wait for stream session: %v", err)
		return code.AIModelFail
	}
	defer unlock()

	helper, err := getOrCreateLoadedHelper(ctx, userName, sessionID, options)
	if err != nil {
		log.Printf("create AI helper for stream: %v", err)
		return code.AIModelFail
	}

	request := rag.NewChatRequestWithSelection(options.KnowledgeBaseIDs, options.KnowledgeBasesSpecified)
	requestContext, cancelRequest := context.WithCancel(rag.WithChatRequest(contextOrBackground(ctx), request))
	defer cancelRequest()
	var writeErr error
	var writeMu sync.Mutex
	callback := func(content string) {
		writeMu.Lock()
		defer writeMu.Unlock()
		if writeErr != nil || requestContext.Err() != nil {
			return
		}
		if err := sink.TextDelta(requestContext, content); err != nil {
			writeErr = err
			// aihelper.StreamCallback cannot return an error. Cancel the model
			// context here so a failed SSE/WebSocket consumer does not leave an
			// upstream model running until its broader request deadline.
			cancelRequest()
		}
	}

	if _, err := helper.StreamResponse(userName, requestContext, callback, userQuestion); err != nil {
		log.Printf("stream AI response: %v", err)
		return code.AIModelFail
	}
	writeMu.Lock()
	streamWriteErr := writeErr
	writeMu.Unlock()
	if streamWriteErr != nil {
		log.Printf("write stream AI response: %v", streamWriteErr)
		return code.AIModelFail
	}
	touchSessionActivity(ctx, userName, sessionID)
	if citations := request.References(); len(citations) > 0 {
		if err := sink.Citations(requestContext, citations); err != nil {
			log.Printf("write stream citations: %v", err)
			return code.AIModelFail
		}
	}
	if err := sink.Complete(requestContext); err != nil {
		log.Printf("write stream completion: %v", err)
		return code.AIModelFail
	}
	return code.CodeSuccess
}

type sseStreamSink struct {
	writer  http.ResponseWriter
	flusher http.Flusher
}

func (s sseStreamSink) TextDelta(_ context.Context, content string) error {
	return writeSSE(s.writer, s.flusher, map[string]string{"content": content})
}

func (s sseStreamSink) Citations(_ context.Context, citations []model.KnowledgeReference) error {
	return writeSSE(s.writer, s.flusher, map[string]interface{}{"citations": citations})
}

func (s sseStreamSink) Complete(_ context.Context) error {
	if _, err := s.writer.Write([]byte("data: [DONE]\n\n")); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func CreateStreamSessionAndSendMessage(userName, userQuestion, modelType string, writer http.ResponseWriter) (string, code.Code) {
	return CreateStreamSessionAndSendMessageWithOptions(
		context.Background(), userName, userQuestion, ChatOptions{ModelType: modelType}, writer,
	)
}

func CreateStreamSessionAndSendMessageWithOptions(ctx context.Context, userName, userQuestion string, options ChatOptions, writer http.ResponseWriter) (string, code.Code) {
	sessionID, resultCode := CreateStreamSessionOnlyWithContext(ctx, userName, userQuestion)
	if resultCode != code.CodeSuccess {
		return "", resultCode
	}
	resultCode = StreamMessageToExistingSessionWithOptions(ctx, userName, sessionID, userQuestion, options, writer)
	return sessionID, resultCode
}

// ChatSend preserves the legacy service contract.
func ChatSend(userName, sessionID, userQuestion, modelType string) (string, code.Code) {
	result, resultCode := ChatSendWithOptions(
		context.Background(), userName, sessionID, userQuestion, ChatOptions{ModelType: modelType},
	)
	return result.Content, resultCode
}

func ChatSendWithOptions(ctx context.Context, userName, sessionID, userQuestion string, options ChatOptions) (ChatResult, code.Code) {
	return generateResponse(ctx, userName, sessionID, userQuestion, options)
}

func generateResponse(ctx context.Context, userName, sessionID, userQuestion string, options ChatOptions) (ChatResult, code.Code) {
	var valid bool
	userQuestion, valid = normalizeUserQuestion(userQuestion)
	if !valid {
		return ChatResult{}, code.CodeInvalidParams
	}
	userName = strings.TrimSpace(userName)
	sessionID = strings.TrimSpace(sessionID)
	if resultCode := authorizeSession(ctx, userName, sessionID); resultCode != code.CodeSuccess {
		return ChatResult{}, resultCode
	}
	unlock, err := lockSession(ctx, userName, sessionID)
	if err != nil {
		log.Printf("wait for chat session: %v", err)
		return ChatResult{}, code.AIModelFail
	}
	defer unlock()

	helper, err := getOrCreateLoadedHelper(ctx, userName, sessionID, options)
	if err != nil {
		log.Printf("create AI helper: %v", err)
		return ChatResult{}, code.AIModelFail
	}

	request := rag.NewChatRequestWithSelection(options.KnowledgeBaseIDs, options.KnowledgeBasesSpecified)
	requestContext := rag.WithChatRequest(contextOrBackground(ctx), request)
	aiResponse, err := helper.GenerateResponse(userName, requestContext, userQuestion)
	if err != nil {
		log.Printf("generate AI response: %v", err)
		return ChatResult{}, code.AIModelFail
	}
	touchSessionActivity(ctx, userName, sessionID)
	return ChatResult{Content: aiResponse.Content, Citations: request.References()}, code.CodeSuccess
}

// touchSessionActivity is best-effort. A response may have been generated and
// persisted even when the client has just disconnected, so use the request
// values but detach its cancellation and bound the database write separately.
func touchSessionActivity(ctx context.Context, userName, sessionID string) {
	touchContext, cancel := context.WithTimeout(context.WithoutCancel(contextOrBackground(ctx)), 3*time.Second)
	defer cancel()
	if err := touchOwnedSession(touchContext, userName, sessionID); err != nil {
		log.Printf("touch chat session activity: %v", err)
	}
}

func GetChatHistory(userName, sessionID string) ([]model.History, code.Code) {
	return GetChatHistoryWithContext(context.Background(), userName, sessionID)
}

// GetChatHistoryWithContext retains the legacy slice-shaped result while
// bounding the response to the most recent page. New callers should use
// GetChatHistoryPageWithContext and follow NextCursor for older messages.
func GetChatHistoryWithContext(ctx context.Context, userName, sessionID string) ([]model.History, code.Code) {
	page, resultCode := GetChatHistoryPageWithContext(ctx, userName, sessionID, defaultHistoryPageSize, "")
	if resultCode != code.CodeSuccess {
		return nil, resultCode
	}
	return page.History, code.CodeSuccess
}

// GetChatHistoryPageWithContext returns one chronological page of a session's
// history. The first request returns the newest messages; a cursor requests an
// older page, which is still returned in chronological order for direct UI
// prepend. Authorization happens before any message query.
func GetChatHistoryPageWithContext(ctx context.Context, userName, sessionID string, limit int, cursor string) (HistoryPage, code.Code) {
	userName = strings.TrimSpace(userName)
	sessionID = strings.TrimSpace(sessionID)
	if resultCode := authorizeSession(ctx, userName, sessionID); resultCode != code.CodeSuccess {
		return HistoryPage{}, resultCode
	}
	beforeCreatedAt, beforeID, err := decodeHistoryPageCursor(cursor)
	if err != nil {
		return HistoryPage{}, code.CodeInvalidParams
	}

	persisted, hasMore, err := loadOwnedMessagesPage(
		contextOrBackground(ctx), userName, sessionID, beforeCreatedAt, beforeID, normalizePageLimit(limit, defaultHistoryPageSize),
	)
	if err != nil {
		log.Printf("load chat history: %v", err)
		return HistoryPage{}, code.CodeServerBusy
	}

	history := make([]model.History, 0, len(persisted))
	for index := range persisted {
		message := &persisted[index]
		history = append(history, model.History{
			MessageID: message.MessageID,
			IsUser:    message.IsUser,
			Content:   message.Content,
			Citations: message.KnowledgeReferences(),
		})
	}
	page := HistoryPage{History: history, HasMore: hasMore}
	if hasMore && len(persisted) > 0 {
		page.NextCursor = encodeHistoryPageCursor(persisted[0])
	}
	return page, code.CodeSuccess
}

func ChatStreamSend(userName, sessionID, userQuestion, modelType string, writer http.ResponseWriter) code.Code {
	return ChatStreamSendWithOptions(
		context.Background(), userName, sessionID, userQuestion, ChatOptions{ModelType: modelType}, writer,
	)
}

func ChatStreamSendWithOptions(ctx context.Context, userName, sessionID, userQuestion string, options ChatOptions, writer http.ResponseWriter) code.Code {
	return StreamMessageToExistingSessionWithOptions(ctx, userName, sessionID, userQuestion, options, writer)
}

func writeSSE(writer http.ResponseWriter, flusher http.Flusher, payload interface{}) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := writer.Write(append(append([]byte("data: "), encoded...), []byte("\n\n")...)); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
