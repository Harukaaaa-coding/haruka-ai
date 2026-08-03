package aihelper

import (
	"context"
	"testing"
	"time"

	"GopherAI/model"

	"github.com/cloudwego/eino/schema"
)

type managerTestModel struct {
	modelType string
}

func (m *managerTestModel) GenerateResponse(context.Context, []*schema.Message) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "test response"}, nil
}

func (m *managerTestModel) StreamResponse(context.Context, []*schema.Message, StreamCallback) (string, error) {
	return "test response", nil
}

func (m *managerTestModel) GetModelType() string {
	return m.modelType
}

func TestManagerSwitchesModelAndPreservesHistory(t *testing.T) {
	factory := GetGlobalFactory()
	factory.RegisterModel("manager-test-a", func(context.Context, map[string]interface{}) (AIModel, error) {
		return &managerTestModel{modelType: "manager-test-a"}, nil
	})
	factory.RegisterModel("manager-test-b", func(context.Context, map[string]interface{}) (AIModel, error) {
		return &managerTestModel{modelType: "manager-test-b"}, nil
	})

	manager := NewAIHelperManager()
	helper, err := manager.GetOrCreateAIHelper("test-user", "test-session", "manager-test-a", nil)
	if err != nil {
		t.Fatalf("create helper: %v", err)
	}
	if err := helper.AddMessage("existing history", "test-user", true, false); err != nil {
		t.Fatalf("add history: %v", err)
	}

	switched, err := manager.GetOrCreateAIHelper("test-user", "test-session", "manager-test-b", nil)
	if err != nil {
		t.Fatalf("switch model: %v", err)
	}
	if switched != helper {
		t.Fatal("model switch replaced the helper instead of reusing the session")
	}
	if got := switched.GetModelType(); got != "manager-test-b" {
		t.Fatalf("GetModelType() = %q, want manager-test-b", got)
	}

	messages := switched.GetMessages()
	if len(messages) != 1 || messages[0].Content != "existing history" {
		t.Fatalf("history after model switch = %#v, want the existing message", messages)
	}
}

func TestManagerEvictsExpiredIdleHelpersOnNextAccess(t *testing.T) {
	const modelType = "manager-idle-eviction-test"
	GetGlobalFactory().RegisterModel(modelType, func(context.Context, map[string]interface{}) (AIModel, error) {
		return &managerTestModel{modelType: modelType}, nil
	})

	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	manager := newEvictionTestManager(&now, time.Minute)
	if _, err := manager.GetOrCreateAIHelper("alice", "expired", modelType, nil); err != nil {
		t.Fatalf("create expired helper: %v", err)
	}

	now = now.Add(time.Minute)
	if _, err := manager.GetOrCreateAIHelper("bob", "trigger", modelType, nil); err != nil {
		t.Fatalf("trigger lazy eviction: %v", err)
	}

	if _, ok := manager.GetAIHelper("alice", "expired"); ok {
		t.Fatal("expired helper remained resident after lazy eviction")
	}
}

func TestManagerRefreshesIdleDeadlineWhenHelperIsReused(t *testing.T) {
	const modelType = "manager-idle-refresh-test"
	GetGlobalFactory().RegisterModel(modelType, func(context.Context, map[string]interface{}) (AIModel, error) {
		return &managerTestModel{modelType: modelType}, nil
	})

	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	manager := newEvictionTestManager(&now, time.Minute)
	helper, err := manager.GetOrCreateAIHelper("alice", "recent", modelType, nil)
	if err != nil {
		t.Fatalf("create helper: %v", err)
	}

	now = now.Add(30 * time.Second)
	reused, err := manager.GetOrCreateAIHelper("alice", "recent", modelType, nil)
	if err != nil {
		t.Fatalf("reuse helper: %v", err)
	}
	if reused != helper {
		t.Fatal("reused helper was unexpectedly replaced")
	}

	// This is past the original deadline but still inside the deadline renewed
	// by the reuse above.
	now = now.Add(40 * time.Second)
	if _, err := manager.GetOrCreateAIHelper("bob", "trigger", modelType, nil); err != nil {
		t.Fatalf("trigger lazy eviction: %v", err)
	}
	if _, ok := manager.GetAIHelper("alice", "recent"); !ok {
		t.Fatal("recently reused helper was evicted")
	}
}

func TestManagerDoesNotEvictInFlightHelper(t *testing.T) {
	const modelType = "manager-idle-inflight-test"
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	GetGlobalFactory().RegisterModel(modelType, func(context.Context, map[string]interface{}) (AIModel, error) {
		return &blockingManagerTestModel{started: started, release: release, modelType: modelType}, nil
	})

	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	manager := newEvictionTestManager(&now, time.Minute)
	helper, err := manager.GetOrCreateAIHelper("alice", "active", modelType, nil)
	if err != nil {
		t.Fatalf("create helper: %v", err)
	}
	helper.SetSaveFunc(func(message *model.Message) (*model.Message, error) { return message, nil })

	responseDone := make(chan error, 1)
	go func() {
		_, responseErr := helper.GenerateResponse("alice", context.Background(), "question")
		responseDone <- responseErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("model response did not start")
	}

	now = now.Add(2 * time.Minute)
	if _, err := manager.GetOrCreateAIHelper("bob", "trigger", modelType, nil); err != nil {
		t.Fatalf("trigger lazy eviction: %v", err)
	}
	if _, ok := manager.GetAIHelper("alice", "active"); !ok {
		t.Fatal("in-flight helper was evicted")
	}

	close(release)
	select {
	case responseErr := <-responseDone:
		if responseErr != nil {
			t.Fatalf("generate response: %v", responseErr)
		}
	case <-time.After(time.Second):
		t.Fatal("model response did not finish")
	}

	// GetAIHelper refreshed the last-used time while the response was active;
	// once the helper becomes idle, a later pass may reclaim it normally.
	now = now.Add(2 * time.Minute)
	if _, err := manager.GetOrCreateAIHelper("carol", "trigger", modelType, nil); err != nil {
		t.Fatalf("trigger post-response eviction: %v", err)
	}
	if _, ok := manager.GetAIHelper("alice", "active"); ok {
		t.Fatal("completed stale helper was not evicted")
	}
}

func newEvictionTestManager(now *time.Time, ttl time.Duration) *AIHelperManager {
	manager := NewAIHelperManager()
	manager.idleTTL = ttl
	manager.clock = func() time.Time { return *now }
	return manager
}

type blockingManagerTestModel struct {
	started   chan<- struct{}
	release   <-chan struct{}
	modelType string
}

func (m *blockingManagerTestModel) GenerateResponse(ctx context.Context, _ []*schema.Message) (*schema.Message, error) {
	select {
	case m.started <- struct{}{}:
	default:
	}
	select {
	case <-m.release:
		return &schema.Message{Role: schema.Assistant, Content: "test response"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *blockingManagerTestModel) StreamResponse(context.Context, []*schema.Message, StreamCallback) (string, error) {
	return "test response", nil
}

func (m *blockingManagerTestModel) GetModelType() string {
	return m.modelType
}
