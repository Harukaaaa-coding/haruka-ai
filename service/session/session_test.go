package session

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"GopherAI/common/aihelper"
	"GopherAI/common/code"
	"GopherAI/model"

	"github.com/cloudwego/eino/schema"
	"gorm.io/gorm"
)

type sessionContextKey struct{}

func replaceSessionDAOFunctions(
	t *testing.T,
	lookup func(context.Context, string, string) (*model.Session, error),
	load func(context.Context, string, string) ([]model.Message, error),
) {
	t.Helper()
	previousLookup := lookupOwnedSession
	previousLoad := loadOwnedMessagesPage
	lookupOwnedSession = lookup
	loadOwnedMessagesPage = func(ctx context.Context, userName, sessionID string, _ time.Time, _ uint, _ int) ([]model.Message, bool, error) {
		messages, err := load(ctx, userName, sessionID)
		return messages, false, err
	}
	t.Cleanup(func() {
		lookupOwnedSession = previousLookup
		loadOwnedMessagesPage = previousLoad
	})
}

func replaceSessionList(t *testing.T, list func(context.Context, string) ([]model.Session, error)) {
	t.Helper()
	previousList := listOwnedSessionsPage
	listOwnedSessionsPage = func(ctx context.Context, userName string, _ time.Time, _ string, _ int) ([]model.Session, bool, error) {
		sessions, err := list(ctx, userName)
		return sessions, false, err
	}
	t.Cleanup(func() { listOwnedSessionsPage = previousList })
}

func replaceRecentMessageLoader(t *testing.T, load func(context.Context, string, string, int) ([]model.Message, error)) {
	t.Helper()
	previousLoad := loadRecentOwnedMessages
	loadRecentOwnedMessages = load
	t.Cleanup(func() { loadRecentOwnedMessages = previousLoad })
}

type lazyHistoryTestModel struct{}

func (lazyHistoryTestModel) GenerateResponse(context.Context, []*schema.Message) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "test"}, nil
}

func (lazyHistoryTestModel) StreamResponse(context.Context, []*schema.Message, aihelper.StreamCallback) (string, error) {
	return "test", nil
}

func (lazyHistoryTestModel) GetModelType() string { return "p1-lazy-history" }

func TestWriteSSEEncodesStructuredChunk(t *testing.T) {
	recorder := httptest.NewRecorder()
	if err := writeSSE(recorder, recorder, map[string]string{"content": "hello\nworld"}); err != nil {
		t.Fatalf("write SSE: %v", err)
	}
	frame := recorder.Body.String()
	if !strings.Contains(frame, `"content":"hello\nworld"`) || !strings.HasSuffix(frame, "\n\n") {
		t.Fatalf("unexpected SSE frame: %q", frame)
	}
}

func TestSessionLockSerializesSameSession(t *testing.T) {
	unlock, err := lockSession(context.Background(), "user", "session")
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	acquired := make(chan struct{})
	go func() {
		release, lockErr := lockSession(context.Background(), "user", "session")
		if lockErr != nil {
			return
		}
		close(acquired)
		release()
	}()
	select {
	case <-acquired:
		t.Fatal("second request acquired the same session lock too early")
	case <-time.After(30 * time.Millisecond):
	}
	unlock()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second request did not acquire the released session lock")
	}
}

func TestSessionLockHonorsCancellation(t *testing.T) {
	unlock, err := lockSession(context.Background(), "user", "canceled-session")
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := lockSession(ctx, "user", "canceled-session"); err == nil {
		t.Fatal("expected canceled waiter to stop")
	}
}

func TestSessionGateReclaimsCanceledWaitersWithoutBreakingSerialization(t *testing.T) {
	const userName = "gate-cleanup-user"
	const sessionID = "gate-cleanup-session"
	baseline := sessionGateCountForTest()

	unlock, err := lockSession(context.Background(), userName, sessionID)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	if got := sessionGateCountForTest(); got != baseline+1 {
		t.Fatalf("active gates after lock = %d, want %d", got, baseline+1)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := lockSession(canceled, userName, sessionID); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v, want context cancellation", err)
	}
	if got := sessionGateCountForTest(); got != baseline+1 {
		t.Fatalf("canceled waiter reclaimed an active gate: count = %d, want %d", got, baseline+1)
	}

	acquired := make(chan struct{})
	go func() {
		release, lockErr := lockSession(context.Background(), userName, sessionID)
		if lockErr != nil {
			return
		}
		close(acquired)
		release()
	}()
	select {
	case <-acquired:
		t.Fatal("new lock bypassed the active session gate")
	case <-time.After(30 * time.Millisecond):
	}

	unlock()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("waiter did not acquire the released session gate")
	}
	if got := sessionGateCountForTest(); got != baseline {
		t.Fatalf("idle session gates = %d, want baseline %d", got, baseline)
	}
}

func sessionGateCountForTest() int {
	sessionRequestGates.mu.Lock()
	defer sessionRequestGates.mu.Unlock()
	return len(sessionRequestGates.gates)
}

func TestContextOrBackground(t *testing.T) {
	if contextOrBackground(nil) == nil {
		t.Fatal("nil context should be replaced")
	}
	valueContext := context.WithValue(context.Background(), struct{}{}, "value")
	if contextOrBackground(valueContext) != valueContext {
		t.Fatal("non-nil context should be preserved")
	}
}

func TestGetUserSessionsReadsDurableSessionList(t *testing.T) {
	ctx := context.WithValue(context.Background(), sessionContextKey{}, "session-list-context")
	replaceSessionList(t, func(received context.Context, userName string) ([]model.Session, error) {
		if received == nil || received.Value(sessionContextKey{}) != "session-list-context" {
			t.Fatal("session list did not receive the request context")
		}
		if userName != "alice" {
			t.Fatalf("session list user = %q, want alice", userName)
		}
		return []model.Session{
			{ID: "latest", UserName: userName, Title: "Latest"},
			{ID: "older", UserName: userName, Title: "Older"},
		}, nil
	})

	sessions, err := GetUserSessionsByUserNameWithContext(ctx, "  alice\t")
	if err != nil {
		t.Fatalf("GetUserSessionsByUserName: %v", err)
	}
	if len(sessions) != 2 || sessions[0].SessionID != "latest" || sessions[1].Title != "Older" {
		t.Fatalf("session list = %#v, want durable ordering and titles", sessions)
	}
}

func TestGetUserSessionsPageUsesOpaqueKeysetCursorAndBoundedLimit(t *testing.T) {
	previousList := listOwnedSessionsPage
	defer func() { listOwnedSessionsPage = previousList }()

	newestAt := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	pageTailAt := newestAt.Add(-time.Minute)
	firstPage := []model.Session{
		{ID: "newest", UserName: "alice", Title: "Newest", UpdatedAt: newestAt},
		{ID: "page-tail", UserName: "alice", Title: "Page tail", UpdatedAt: pageTailAt},
	}
	calls := 0
	listOwnedSessionsPage = func(_ context.Context, userName string, beforeUpdatedAt time.Time, beforeID string, limit int) ([]model.Session, bool, error) {
		calls++
		if userName != "alice" {
			t.Fatalf("session page user = %q, want alice", userName)
		}
		switch calls {
		case 1:
			if !beforeUpdatedAt.IsZero() || beforeID != "" {
				t.Fatalf("first page cursor = (%v, %q), want empty", beforeUpdatedAt, beforeID)
			}
			if limit != maxPageSize {
				t.Fatalf("first page limit = %d, want cap %d", limit, maxPageSize)
			}
			return firstPage, true, nil
		case 2:
			if !beforeUpdatedAt.Equal(pageTailAt) || beforeID != "page-tail" {
				t.Fatalf("second page cursor = (%v, %q), want (%v, %q)", beforeUpdatedAt, beforeID, pageTailAt, "page-tail")
			}
			if limit != 2 {
				t.Fatalf("second page limit = %d, want 2", limit)
			}
			return []model.Session{{ID: "older", UserName: "alice", Title: "Older", UpdatedAt: pageTailAt.Add(-time.Minute)}}, false, nil
		default:
			t.Fatalf("unexpected session page call %d", calls)
			return nil, false, nil
		}
	}

	page, err := GetUserSessionsPageWithContext(context.Background(), " alice ", maxPageSize+1, "")
	if err != nil {
		t.Fatalf("first session page: %v", err)
	}
	if !page.HasMore || page.NextCursor == "" || len(page.Sessions) != 2 || page.Sessions[0].SessionID != "newest" {
		t.Fatalf("unexpected first page: %#v", page)
	}
	secondPage, err := GetUserSessionsPageWithContext(context.Background(), "alice", 2, page.NextCursor)
	if err != nil {
		t.Fatalf("second session page: %v", err)
	}
	if secondPage.HasMore || secondPage.NextCursor != "" || len(secondPage.Sessions) != 1 || secondPage.Sessions[0].SessionID != "older" {
		t.Fatalf("unexpected second page: %#v", secondPage)
	}
}

func TestGetUserSessionsPageRejectsMalformedCursor(t *testing.T) {
	if _, err := GetUserSessionsPageWithContext(context.Background(), "alice", 10, "not-a-cursor"); !errors.Is(err, ErrInvalidPageCursor) {
		t.Fatalf("malformed cursor error = %v, want ErrInvalidPageCursor", err)
	}
}

func TestGetOrCreateLoadedHelperLoadsRecentTailOnlyOnce(t *testing.T) {
	const userName = "lazy-history-user"
	const sessionID = "lazy-history-session"
	const modelType = "p1-lazy-history"

	aihelper.GetGlobalFactory().RegisterModel(modelType, func(context.Context, map[string]interface{}) (aihelper.AIModel, error) {
		return lazyHistoryTestModel{}, nil
	})
	manager := aihelper.GetGlobalManager()
	manager.RemoveAIHelper(userName, sessionID)
	t.Cleanup(func() { manager.RemoveAIHelper(userName, sessionID) })

	loads := 0
	replaceRecentMessageLoader(t, func(received context.Context, receivedUser, receivedSession string, limit int) ([]model.Message, error) {
		loads++
		if received == nil || receivedUser != userName || receivedSession != sessionID {
			t.Fatalf("unexpected lazy history request: ctx=%v user=%q session=%q", received, receivedUser, receivedSession)
		}
		if limit != maxModelHistoryItems {
			t.Fatalf("lazy history limit = %d, want %d", limit, maxModelHistoryItems)
		}
		return []model.Message{
			{SessionID: sessionID, UserName: userName, Content: "recent user", IsUser: true},
			{SessionID: sessionID, UserName: userName, Content: "recent assistant"},
		}, nil
	})

	helper, err := getOrCreateLoadedHelper(context.Background(), userName, sessionID, ChatOptions{ModelType: modelType})
	if err != nil {
		t.Fatalf("first lazy helper load: %v", err)
	}
	if messages := helper.GetMessages(); len(messages) != 2 || messages[0].Content != "recent user" || messages[1].Content != "recent assistant" {
		t.Fatalf("hydrated messages = %#v", messages)
	}
	if again, err := getOrCreateLoadedHelper(context.Background(), userName, sessionID, ChatOptions{ModelType: modelType}); err != nil || again != helper {
		t.Fatalf("cached helper = (%p, %v), want original helper", again, err)
	}
	if loads != 1 {
		t.Fatalf("recent history loads = %d, want 1", loads)
	}
}

func TestExistingSessionOperationsRejectForeignAndUnknownIDs(t *testing.T) {
	replaceSessionDAOFunctions(t,
		func(_ context.Context, userName, sessionID string) (*model.Session, error) {
			if userName == "alice" && sessionID == "owned-session" {
				return &model.Session{ID: sessionID, UserName: userName}, nil
			}
			return nil, gorm.ErrRecordNotFound
		},
		func(context.Context, string, string) ([]model.Message, error) {
			t.Fatal("message history must not be queried for an unowned session")
			return nil, nil
		},
	)

	for _, sessionID := range []string{"bob-session", "unknown-session"} {
		t.Run(sessionID, func(t *testing.T) {
			if _, resultCode := ChatSendWithOptions(
				context.Background(), "alice", sessionID, "hello", ChatOptions{ModelType: "4"},
			); resultCode != code.CodeRecordNotFound {
				t.Fatalf("ChatSendWithOptions() code = %d, want %d", resultCode, code.CodeRecordNotFound)
			}

			recorder := httptest.NewRecorder()
			if resultCode := StreamMessageToExistingSessionWithOptions(
				context.Background(), "alice", sessionID, "hello", ChatOptions{ModelType: "4"}, recorder,
			); resultCode != code.CodeRecordNotFound {
				t.Fatalf("StreamMessageToExistingSessionWithOptions() code = %d, want %d", resultCode, code.CodeRecordNotFound)
			}
			if recorder.Body.Len() != 0 {
				t.Fatalf("unowned stream wrote a response body: %q", recorder.Body.String())
			}

			if _, resultCode := GetChatHistoryWithContext(context.Background(), "alice", sessionID); resultCode != code.CodeRecordNotFound {
				t.Fatalf("GetChatHistoryWithContext() code = %d, want %d", resultCode, code.CodeRecordNotFound)
			}
			if _, exists := aihelper.GetGlobalManager().GetAIHelper("alice", sessionID); exists {
				t.Fatal("authorization failure must not create an in-memory AI helper")
			}
		})
	}
}

func TestGetChatHistoryAllowsOwnerAndPropagatesContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), sessionContextKey{}, "request-context")
	loadCalls := 0
	replaceSessionDAOFunctions(t,
		func(received context.Context, userName, sessionID string) (*model.Session, error) {
			if received.Value(sessionContextKey{}) != "request-context" {
				t.Fatal("ownership lookup did not receive the request context")
			}
			if userName != "alice" || sessionID != "owned-history-session" {
				t.Fatalf("unexpected ownership lookup: user=%q session=%q", userName, sessionID)
			}
			return &model.Session{ID: sessionID, UserName: userName}, nil
		},
		func(received context.Context, userName, sessionID string) ([]model.Message, error) {
			loadCalls++
			if received.Value(sessionContextKey{}) != "request-context" {
				t.Fatal("message lookup did not receive the request context")
			}
			if userName != "alice" || sessionID != "owned-history-session" {
				t.Fatalf("unexpected message lookup: user=%q session=%q", userName, sessionID)
			}
			return []model.Message{{SessionID: sessionID, UserName: userName, Content: "owner message", IsUser: true}}, nil
		},
	)

	history, resultCode := GetChatHistoryWithContext(ctx, "alice", "owned-history-session")
	if resultCode != code.CodeSuccess {
		t.Fatalf("GetChatHistoryWithContext() code = %d, want %d", resultCode, code.CodeSuccess)
	}
	if loadCalls != 1 {
		t.Fatalf("owned message lookup calls = %d, want 1", loadCalls)
	}
	if len(history) != 1 || !history[0].IsUser || history[0].Content != "owner message" {
		t.Fatalf("unexpected owner history: %#v", history)
	}
}

func TestGetChatHistoryPageKeepsChronologicalOrderAndUsesOldestCursor(t *testing.T) {
	previousLookup := lookupOwnedSession
	previousLoad := loadOwnedMessagesPage
	defer func() {
		lookupOwnedSession = previousLookup
		loadOwnedMessagesPage = previousLoad
	}()

	oldestAt := time.Date(2026, time.July, 29, 9, 0, 0, 0, time.UTC)
	newestAt := oldestAt.Add(time.Minute)
	lookupOwnedSession = func(_ context.Context, userName, sessionID string) (*model.Session, error) {
		if userName != "alice" || sessionID != "history-session" {
			t.Fatalf("unexpected ownership lookup user=%q session=%q", userName, sessionID)
		}
		return &model.Session{ID: sessionID, UserName: userName}, nil
	}
	calls := 0
	loadOwnedMessagesPage = func(_ context.Context, userName, sessionID string, beforeCreatedAt time.Time, beforeID uint, limit int) ([]model.Message, bool, error) {
		calls++
		if userName != "alice" || sessionID != "history-session" {
			t.Fatalf("unexpected history lookup user=%q session=%q", userName, sessionID)
		}
		switch calls {
		case 1:
			if !beforeCreatedAt.IsZero() || beforeID != 0 || limit != maxPageSize {
				t.Fatalf("unexpected first history page cursor=(%v,%d), limit=%d", beforeCreatedAt, beforeID, limit)
			}
			return []model.Message{
				{ID: 11, MessageID: "oldest-page-message", CreatedAt: oldestAt, IsUser: true, Content: "older of newest page"},
				{ID: 12, MessageID: "newest-page-message", CreatedAt: newestAt, IsUser: false, Content: "newest"},
			}, true, nil
		case 2:
			if !beforeCreatedAt.Equal(oldestAt) || beforeID != 11 || limit != 2 {
				t.Fatalf("unexpected second history page cursor=(%v,%d), limit=%d", beforeCreatedAt, beforeID, limit)
			}
			return []model.Message{{ID: 10, MessageID: "older-message", CreatedAt: oldestAt.Add(-time.Minute), IsUser: true, Content: "older"}}, false, nil
		default:
			t.Fatalf("unexpected history page call %d", calls)
			return nil, false, nil
		}
	}

	page, resultCode := GetChatHistoryPageWithContext(context.Background(), "alice", "history-session", maxPageSize+1, "")
	if resultCode != code.CodeSuccess {
		t.Fatalf("first history page code = %d, want %d", resultCode, code.CodeSuccess)
	}
	if !page.HasMore || page.NextCursor == "" || len(page.History) != 2 || page.History[0].MessageID != "oldest-page-message" || page.History[1].MessageID != "newest-page-message" {
		t.Fatalf("unexpected first history page: %#v", page)
	}
	secondPage, resultCode := GetChatHistoryPageWithContext(context.Background(), "alice", "history-session", 2, page.NextCursor)
	if resultCode != code.CodeSuccess {
		t.Fatalf("second history page code = %d, want %d", resultCode, code.CodeSuccess)
	}
	if secondPage.HasMore || secondPage.NextCursor != "" || len(secondPage.History) != 1 || secondPage.History[0].MessageID != "older-message" {
		t.Fatalf("unexpected second history page: %#v", secondPage)
	}
}

func TestGetChatHistoryPageRejectsMalformedCursor(t *testing.T) {
	previousLookup := lookupOwnedSession
	previousLoad := loadOwnedMessagesPage
	defer func() {
		lookupOwnedSession = previousLookup
		loadOwnedMessagesPage = previousLoad
	}()
	lookupOwnedSession = func(context.Context, string, string) (*model.Session, error) {
		return &model.Session{ID: "history-session", UserName: "alice"}, nil
	}
	loadOwnedMessagesPage = func(context.Context, string, string, time.Time, uint, int) ([]model.Message, bool, error) {
		t.Fatal("history query must not run for malformed cursor")
		return nil, false, nil
	}

	if _, resultCode := GetChatHistoryPageWithContext(context.Background(), "alice", "history-session", 10, "not-a-cursor"); resultCode != code.CodeInvalidParams {
		t.Fatalf("malformed cursor result = %d, want %d", resultCode, code.CodeInvalidParams)
	}
}

func TestTouchSessionActivityDetachesClientCancellation(t *testing.T) {
	previousTouch := touchOwnedSession
	defer func() { touchOwnedSession = previousTouch }()

	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), sessionContextKey{}, "request-value"))
	cancel()
	touchOwnedSession = func(received context.Context, userName, sessionID string) error {
		if received.Err() != nil {
			t.Fatalf("touch context unexpectedly canceled: %v", received.Err())
		}
		if received.Value(sessionContextKey{}) != "request-value" {
			t.Fatal("touch context lost request values")
		}
		if deadline, ok := received.Deadline(); !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 3*time.Second {
			t.Fatalf("touch context deadline is not bounded: deadline=%v present=%v", deadline, ok)
		}
		if userName != "alice" || sessionID != "active-session" {
			t.Fatalf("unexpected touch target user=%q session=%q", userName, sessionID)
		}
		return nil
	}

	touchSessionActivity(ctx, "alice", "active-session")
}

func TestSessionAuthorizationReturnsStableDatabaseErrorCode(t *testing.T) {
	replaceSessionDAOFunctions(t,
		func(context.Context, string, string) (*model.Session, error) {
			return nil, errors.New("database unavailable")
		},
		func(context.Context, string, string) ([]model.Message, error) { return nil, nil },
	)
	if resultCode := authorizeSession(context.Background(), "alice", "session"); resultCode != code.CodeServerBusy {
		t.Fatalf("authorizeSession() code = %d, want %d", resultCode, code.CodeServerBusy)
	}
}

func TestCreateStreamSessionUsesRequestContext(t *testing.T) {
	previousCreate := createSession
	t.Cleanup(func() { createSession = previousCreate })
	ctx := context.WithValue(context.Background(), sessionContextKey{}, "create-context")
	createSession = func(received context.Context, session *model.Session) (*model.Session, error) {
		if received.Value(sessionContextKey{}) != "create-context" {
			t.Fatal("session create did not receive the request context")
		}
		return session, nil
	}

	sessionID, resultCode := CreateStreamSessionOnlyWithContext(ctx, "alice", "hello")
	if resultCode != code.CodeSuccess || sessionID == "" {
		t.Fatalf("CreateStreamSessionOnlyWithContext() = (%q, %d), want non-empty ID and %d", sessionID, resultCode, code.CodeSuccess)
	}
}

func TestNewSessionNormalizesUserNameBeforePersistence(t *testing.T) {
	previousCreate := createSession
	t.Cleanup(func() { createSession = previousCreate })

	createdUsers := make([]string, 0, 2)
	createSession = func(_ context.Context, session *model.Session) (*model.Session, error) {
		createdUsers = append(createdUsers, session.UserName)
		if len(createdUsers) == 1 {
			return nil, errors.New("stop after capturing non-stream session")
		}
		return session, nil
	}

	if _, _, resultCode := CreateSessionAndSendMessageWithOptions(
		context.Background(), "  alice\t", "hello", ChatOptions{ModelType: "4"},
	); resultCode != code.CodeServerBusy {
		t.Fatalf("non-stream create code = %d, want %d", resultCode, code.CodeServerBusy)
	}
	if _, resultCode := CreateStreamSessionOnlyWithContext(context.Background(), "  alice\t", "hello"); resultCode != code.CodeSuccess {
		t.Fatalf("stream create code = %d, want %d", resultCode, code.CodeSuccess)
	}
	if len(createdUsers) != 2 || createdUsers[0] != "alice" || createdUsers[1] != "alice" {
		t.Fatalf("persisted usernames = %#v, want two canonical usernames", createdUsers)
	}
}

func TestNewSessionRejectsInvalidUserNameBeforePersistence(t *testing.T) {
	previousCreate := createSession
	t.Cleanup(func() { createSession = previousCreate })
	createSession = func(context.Context, *model.Session) (*model.Session, error) {
		t.Fatal("invalid username must be rejected before session creation")
		return nil, nil
	}

	userNames := []string{
		" \t\r\n ",
		string([]byte{0xff}),
		strings.Repeat("界", maxUserNameRunes+1),
	}
	for index, userName := range userNames {
		if _, _, resultCode := CreateSessionAndSendMessageWithOptions(
			context.Background(), userName, "hello", ChatOptions{ModelType: "4"},
		); resultCode != code.CodeInvalidParams {
			t.Fatalf("new non-stream username %d code = %d, want %d", index, resultCode, code.CodeInvalidParams)
		}
		if _, resultCode := CreateStreamSessionOnlyWithContext(context.Background(), userName, "hello"); resultCode != code.CodeInvalidParams {
			t.Fatalf("new stream username %d code = %d, want %d", index, resultCode, code.CodeInvalidParams)
		}
	}
}

func TestNormalizeUserQuestionUnicodeBoundaries(t *testing.T) {
	maximum := strings.Repeat("界", maxUserQuestionRunes)
	if normalized, ok := normalizeUserQuestion("  " + maximum + "\n"); !ok || normalized != maximum {
		t.Fatalf("maximum-length Unicode question rejected or not trimmed: ok=%v runes=%d", ok, len([]rune(normalized)))
	}
	if _, ok := normalizeUserQuestion(strings.Repeat("界", maxUserQuestionRunes+1)); ok {
		t.Fatal("question above the Unicode character limit was accepted")
	}
	if _, ok := normalizeUserQuestion(" \t\r\n "); ok {
		t.Fatal("whitespace-only question was accepted")
	}
}

func TestSessionTitleDoesNotOverflowDatabaseColumn(t *testing.T) {
	question := strings.Repeat("界", maxSessionTitleRunes+25)
	title := sessionTitle(question)
	if got := len([]rune(title)); got != maxSessionTitleRunes {
		t.Fatalf("title rune count = %d, want %d", got, maxSessionTitleRunes)
	}
	if !strings.HasPrefix(question, title) {
		t.Fatal("session title is not a prefix of the normalized question")
	}
}

func TestAllChatEntryPointsRejectInvalidQuestionsBeforeIO(t *testing.T) {
	replaceSessionDAOFunctions(t,
		func(context.Context, string, string) (*model.Session, error) {
			t.Fatal("invalid question must be rejected before session authorization")
			return nil, nil
		},
		func(context.Context, string, string) ([]model.Message, error) {
			t.Fatal("invalid question must be rejected before message loading")
			return nil, nil
		},
	)
	previousCreate := createSession
	t.Cleanup(func() { createSession = previousCreate })
	createSession = func(context.Context, *model.Session) (*model.Session, error) {
		t.Fatal("invalid question must be rejected before session creation")
		return nil, nil
	}

	questions := []string{" \t\n ", strings.Repeat("问", maxUserQuestionRunes+1)}
	for index, question := range questions {
		if _, _, resultCode := CreateSessionAndSendMessageWithOptions(
			context.Background(), "alice", question, ChatOptions{ModelType: "4"},
		); resultCode != code.CodeInvalidParams {
			t.Fatalf("new non-stream question %d code = %d, want %d", index, resultCode, code.CodeInvalidParams)
		}
		if _, resultCode := CreateStreamSessionOnlyWithContext(context.Background(), "alice", question); resultCode != code.CodeInvalidParams {
			t.Fatalf("new stream question %d code = %d, want %d", index, resultCode, code.CodeInvalidParams)
		}
		if _, resultCode := ChatSendWithOptions(
			context.Background(), "alice", "owned-session", question, ChatOptions{ModelType: "4"},
		); resultCode != code.CodeInvalidParams {
			t.Fatalf("existing non-stream question %d code = %d, want %d", index, resultCode, code.CodeInvalidParams)
		}
		recorder := httptest.NewRecorder()
		if resultCode := StreamMessageToExistingSessionWithOptions(
			context.Background(), "alice", "owned-session", question, ChatOptions{ModelType: "4"}, recorder,
		); resultCode != code.CodeInvalidParams {
			t.Fatalf("existing stream question %d code = %d, want %d", index, resultCode, code.CodeInvalidParams)
		}
		if recorder.Body.Len() != 0 {
			t.Fatalf("invalid stream question %d wrote a response body: %q", index, recorder.Body.String())
		}
	}
}
