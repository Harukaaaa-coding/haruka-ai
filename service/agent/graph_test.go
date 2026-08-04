package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	hub "GopherAI/common/mcphub"
	"GopherAI/common/modelgateway"
	agentdao "GopherAI/dao/agent"
	"GopherAI/model"

	"github.com/cloudwego/eino/schema"
	"github.com/mark3labs/mcp-go/mcp"
	"gorm.io/gorm"
)

type graphTestClock struct {
	now time.Time
}

func (clock graphTestClock) Now() time.Time { return clock.now }

func (graphTestClock) After(time.Duration) <-chan time.Time {
	return make(chan time.Time)
}

type graphTestReply struct {
	content string
	err     error
}

type graphTestModel struct {
	mu      sync.Mutex
	replies []graphTestReply
	calls   int
}

func (runtime *graphTestModel) Generate(_ context.Context, _, _ string, _ []*schema.Message) (*schema.Message, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.calls >= len(runtime.replies) {
		return nil, fmt.Errorf("unexpected model call %d", runtime.calls+1)
	}
	reply := runtime.replies[runtime.calls]
	runtime.calls++
	if reply.err != nil {
		return nil, reply.err
	}
	return &schema.Message{Role: schema.Assistant, Content: reply.content}, nil
}

type graphTestMCP struct {
	mu                  sync.Mutex
	definition          hub.ToolDefinition
	refreshedDefinition *hub.ToolDefinition
	toolListCalls       int
	callErr             error
	resultIsError       bool
	calls               int
	tokenSeen           bool
	operationIDs        []string
}

func (gateway *graphTestMCP) Tools(context.Context) ([]hub.ToolDefinition, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.toolListCalls++
	definition := gateway.definition
	if gateway.toolListCalls > 1 && gateway.refreshedDefinition != nil {
		definition = *gateway.refreshedDefinition
	}
	if definition.Name == "" {
		return nil, nil
	}
	return []hub.ToolDefinition{definition}, nil
}

func (gateway *graphTestMCP) Call(_ context.Context, request hub.InvokeRequest) (*hub.InvocationResult, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.calls++
	gateway.tokenSeen = gateway.tokenSeen || request.ApprovalToken != ""
	gateway.operationIDs = append(gateway.operationIDs, request.OperationID)
	if gateway.callErr != nil {
		return nil, gateway.callErr
	}
	return &hub.InvocationResult{
		RequestID: "request-1",
		ToolName:  request.ToolName,
		Result:    &mcp.CallToolResult{IsError: gateway.resultIsError},
	}, nil
}

func (*graphTestMCP) CreateApproval(_ context.Context, _, toolName string, _ map[string]any) (*hub.ApprovalChallenge, error) {
	return &hub.ApprovalChallenge{ID: "challenge-1", ToolName: toolName}, nil
}

func (*graphTestMCP) Approve(_, challengeID string) (*hub.IssuedApproval, error) {
	return &hub.IssuedApproval{ChallengeID: challengeID, Token: "ephemeral-test-token"}, nil
}

type graphTestStore struct {
	mu          sync.Mutex
	tasks       map[string]*model.AgentTask
	checkpoints map[string][]byte
	setCalls    int
	now         time.Time
}

func newGraphTestStore(now time.Time) *graphTestStore {
	return &graphTestStore{
		tasks:       make(map[string]*model.AgentTask),
		checkpoints: make(map[string][]byte),
		now:         now,
	}
}

func (store *graphTestStore) CreateTaskWithInitialStep(_ context.Context, task *model.AgentTask, step *model.AgentStep) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if task == nil || step == nil || task.ID == "" || task.UserName == "" {
		return agentdao.ErrInvalidInput
	}
	copyTask := cloneGraphTestTask(task)
	copyStep := *step
	copyTask.Steps = []model.AgentStep{copyStep}
	copyTask.CreatedAt = store.now
	copyTask.UpdatedAt = store.now
	store.tasks[copyTask.ID] = copyTask
	return nil
}

func (store *graphTestStore) GetOwnedTask(_ context.Context, userName, taskID string) (*model.AgentTask, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	task := store.tasks[taskID]
	if task == nil || task.UserName != userName {
		return nil, gorm.ErrRecordNotFound
	}
	return cloneGraphTestTask(task), nil
}

func (store *graphTestStore) GetTaskByID(_ context.Context, taskID string) (*model.AgentTask, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	task := store.tasks[taskID]
	if task == nil {
		return nil, fmt.Errorf("missing task %q: %w", taskID, gorm.ErrRecordNotFound)
	}
	return cloneGraphTestTask(task), nil
}

func (store *graphTestStore) ListOwnedTasks(_ context.Context, userName string, offset, limit int) ([]model.AgentTask, int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	all := make([]model.AgentTask, 0)
	for _, task := range store.tasks {
		if task.UserName == userName {
			all = append(all, *cloneGraphTestTask(task))
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	total := int64(len(all))
	if offset >= len(all) {
		return []model.AgentTask{}, total, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], total, nil
}

func (store *graphTestStore) ListPendingTasks(_ context.Context, limit int) ([]model.AgentTask, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]model.AgentTask, 0, limit)
	for _, task := range store.tasks {
		if task.Status == model.AgentTaskStatusPending && len(result) < limit {
			result = append(result, *cloneGraphTestTask(task))
		}
	}
	return result, nil
}

func (store *graphTestStore) ClaimTask(_ context.Context, taskID, workerID string, lease time.Duration) (*model.AgentTask, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	task := store.tasks[taskID]
	if task == nil {
		return nil, false, gorm.ErrRecordNotFound
	}
	if task.Status != model.AgentTaskStatusPending {
		return nil, false, nil
	}
	until := store.now.Add(lease)
	task.Status = model.AgentTaskStatusRunning
	task.RunVersion++
	task.Attempts++
	task.LeaseOwner = workerID
	task.LeaseUntil = &until
	task.HeartbeatAt = &store.now
	return cloneGraphTestTask(task), true, nil
}

func (store *graphTestStore) RenewLeaseFenced(_ context.Context, taskID string, runVersion uint64, workerID string, lease time.Duration) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	task := store.tasks[taskID]
	if task == nil || task.RunVersion != runVersion || task.LeaseOwner != workerID ||
		(task.Status != model.AgentTaskStatusRunning && task.Status != model.AgentTaskStatusPlanning) {
		return agentdao.ErrConflict
	}
	until := store.now.Add(lease)
	task.LeaseUntil = &until
	task.HeartbeatAt = &store.now
	return nil
}

func (store *graphTestStore) SavePlanWithSummary(_ context.Context, taskID string, runVersion uint64, summary string, steps []model.AgentStep) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	task, err := store.fencedTask(taskID, runVersion)
	if err != nil {
		return err
	}
	kept := make([]model.AgentStep, 0, len(steps)+1)
	for _, step := range task.Steps {
		if step.Kind == model.AgentStepKindPlan {
			step.Status = model.AgentStepStatusSucceeded
			finished := store.now
			step.FinishedAt = &finished
			kept = append(kept, step)
		}
	}
	for index := range steps {
		step := steps[index]
		step.TaskID = task.ID
		step.UserName = task.UserName
		step.Sequence = index + 1
		step.Status = model.AgentStepStatusPending
		kept = append(kept, step)
	}
	task.Steps = kept
	task.PlanSummary = summary
	task.Status = model.AgentTaskStatusRunning
	if len(steps) > 0 {
		task.CurrentStepID = steps[0].ID
	}
	return nil
}

func (store *graphTestStore) UpdateTaskFenced(_ context.Context, taskID string, runVersion uint64, updates map[string]any) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	task, err := store.fencedTask(taskID, runVersion)
	if err != nil {
		return err
	}
	applyGraphTestTaskUpdates(task, updates)
	return nil
}

func (store *graphTestStore) UpdateStepFenced(_ context.Context, taskID, stepID string, runVersion uint64, updates map[string]any) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	task, err := store.fencedTask(taskID, runVersion)
	if err != nil {
		return err
	}
	step := graphTestStep(task, stepID)
	if step == nil {
		return gorm.ErrRecordNotFound
	}
	applyGraphTestStepUpdates(step, updates)
	return nil
}

func (store *graphTestStore) MarkExecutionUnknownFenced(_ context.Context, taskID, stepID string, runVersion uint64, message string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.markUnknown(taskID, stepID, runVersion, message, nil)
}

func (store *graphTestStore) MarkPolicyReviewFenced(_ context.Context, taskID, stepID string, runVersion uint64, message, risk string, requiresApproval, readOnly, idempotent, destructive bool) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	policy := &model.AgentStep{
		RiskLevel:        risk,
		RequiresApproval: requiresApproval,
		ReadOnly:         readOnly,
		Idempotent:       idempotent,
		Destructive:      destructive,
		ApprovalDecision: model.AgentApprovalDecisionPending,
	}
	return store.markUnknown(taskID, stepID, runVersion, message, policy)
}

func (store *graphTestStore) markUnknown(taskID, stepID string, runVersion uint64, message string, policy *model.AgentStep) error {
	task, err := store.fencedTask(taskID, runVersion)
	if err != nil {
		return err
	}
	step := graphTestStep(task, stepID)
	if step == nil || step.Kind != model.AgentStepKindTool || step.Status != model.AgentStepStatusRunning {
		return agentdao.ErrConflict
	}
	step.Status = model.AgentStepStatusExecutionUnknown
	step.ErrorMessage = message
	finished := store.now
	step.FinishedAt = &finished
	if policy != nil {
		step.RiskLevel = policy.RiskLevel
		step.RequiresApproval = policy.RequiresApproval
		step.ReadOnly = policy.ReadOnly
		step.Idempotent = policy.Idempotent
		step.Destructive = policy.Destructive
		step.ApprovalDecision = model.AgentApprovalDecisionPending
		step.ApprovalReason = ""
		step.ApprovalDecidedAt = nil
	}
	task.Status = model.AgentTaskStatusRequiresReview
	task.CurrentStepID = step.ID
	task.ErrorMessage = message
	task.LeaseOwner = ""
	task.LeaseUntil = nil
	task.HeartbeatAt = nil
	task.RunVersion++
	return nil
}

func (store *graphTestStore) UpdateOwnedTask(_ context.Context, userName, taskID string, updates map[string]any) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	task := store.tasks[taskID]
	if task == nil || task.UserName != userName {
		return gorm.ErrRecordNotFound
	}
	applyGraphTestTaskUpdates(task, updates)
	return nil
}

func (store *graphTestStore) UpdateOwnedStepDecision(_ context.Context, userName, taskID, stepID, decision, reason string) (*model.AgentStep, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	task := store.tasks[taskID]
	if task == nil || task.UserName != userName {
		return nil, gorm.ErrRecordNotFound
	}
	if task.Status != model.AgentTaskStatusWaitingApproval {
		return nil, agentdao.ErrConflict
	}
	step := graphTestStep(task, stepID)
	if step == nil || step.Status != model.AgentStepStatusWaitingApproval {
		return nil, agentdao.ErrConflict
	}
	step.ApprovalDecision = decision
	step.ApprovalReason = reason
	if decision == model.AgentApprovalDecisionApproved {
		step.Status = model.AgentStepStatusApproved
		task.Status = model.AgentTaskStatusPending
	} else {
		step.Status = model.AgentStepStatusRejected
		task.Status = model.AgentTaskStatusRejected
	}
	copyStep := *step
	return &copyStep, nil
}

func (store *graphTestStore) ResumeOwnedTask(_ context.Context, userName, taskID string, retryUnknown bool) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	task := store.tasks[taskID]
	if task == nil || task.UserName != userName {
		return gorm.ErrRecordNotFound
	}
	if task.Status != model.AgentTaskStatusFailed && !(task.Status == model.AgentTaskStatusRequiresReview && retryUnknown) {
		return agentdao.ErrConflict
	}
	step := graphTestStep(task, task.CurrentStepID)
	if step == nil || (step.Status != model.AgentStepStatusFailed && !(step.Status == model.AgentStepStatusExecutionUnknown && retryUnknown)) {
		return agentdao.ErrConflict
	}
	step.Status = model.AgentStepStatusPending
	step.ErrorMessage = ""
	step.StartedAt = nil
	step.FinishedAt = nil
	task.Status = model.AgentTaskStatusPending
	task.ErrorMessage = ""
	task.FinishedAt = nil
	task.RunVersion++
	delete(store.checkpoints, taskID)
	return nil
}

func (store *graphTestStore) CancelOwnedTask(_ context.Context, userName, taskID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	task := store.tasks[taskID]
	if task == nil || task.UserName != userName {
		return gorm.ErrRecordNotFound
	}
	task.Status = model.AgentTaskStatusCancelled
	task.RunVersion++
	task.LeaseOwner = ""
	task.LeaseUntil = nil
	return nil
}

func (*graphTestStore) RecoverStaleTasks(context.Context, time.Time) error { return nil }

func (store *graphTestStore) Get(_ context.Context, checkpointID string) ([]byte, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.tasks[checkpointID] == nil {
		return nil, false, gorm.ErrRecordNotFound
	}
	checkpoint := store.checkpoints[checkpointID]
	return append([]byte(nil), checkpoint...), len(checkpoint) > 0, nil
}

func (store *graphTestStore) Set(ctx context.Context, checkpointID string, checkpoint []byte) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	taskID, runVersion, ok := agentdao.FencingTokenFromContext(ctx)
	task := store.tasks[checkpointID]
	if !ok || taskID != checkpointID || task == nil || task.RunVersion != runVersion {
		return agentdao.ErrConflict
	}
	store.checkpoints[checkpointID] = append([]byte(nil), checkpoint...)
	store.setCalls++
	return nil
}

func (store *graphTestStore) fencedTask(taskID string, runVersion uint64) (*model.AgentTask, error) {
	task := store.tasks[taskID]
	if task == nil {
		return nil, gorm.ErrRecordNotFound
	}
	if task.RunVersion != runVersion {
		return nil, agentdao.ErrConflict
	}
	return task, nil
}

func cloneGraphTestTask(task *model.AgentTask) *model.AgentTask {
	if task == nil {
		return nil
	}
	copyTask := *task
	copyTask.Steps = append([]model.AgentStep(nil), task.Steps...)
	return &copyTask
}

func graphTestStep(task *model.AgentTask, stepID string) *model.AgentStep {
	for index := range task.Steps {
		if task.Steps[index].ID == stepID {
			return &task.Steps[index]
		}
	}
	return nil
}

func applyGraphTestTaskUpdates(task *model.AgentTask, updates map[string]any) {
	for key, value := range updates {
		switch key {
		case "status":
			task.Status, _ = value.(string)
		case "current_step_id":
			task.CurrentStepID, _ = value.(string)
		case "plan_summary":
			task.PlanSummary, _ = value.(string)
		case "final_answer":
			task.FinalAnswer, _ = value.(string)
		case "error_message":
			task.ErrorMessage, _ = value.(string)
		case "lease_owner":
			task.LeaseOwner, _ = value.(string)
		case "lease_until":
			task.LeaseUntil = graphTestTimePointer(value)
		case "heartbeat_at":
			task.HeartbeatAt = graphTestTimePointer(value)
		case "started_at":
			task.StartedAt = graphTestTimePointer(value)
		case "finished_at":
			task.FinishedAt = graphTestTimePointer(value)
		}
	}
}

func applyGraphTestStepUpdates(step *model.AgentStep, updates map[string]any) {
	for key, value := range updates {
		switch key {
		case "status":
			step.Status, _ = value.(string)
		case "attempts":
			step.Attempts, _ = value.(int)
		case "started_at":
			step.StartedAt = graphTestTimePointer(value)
		case "finished_at":
			step.FinishedAt = graphTestTimePointer(value)
		case "error_message":
			step.ErrorMessage, _ = value.(string)
		case "tool_output_json":
			step.ToolOutputJSON, _ = value.(string)
		case "result_summary":
			step.ResultSummary, _ = value.(string)
		case "mcp_request_id":
			step.MCPRequestID, _ = value.(string)
		case "operation_id":
			step.OperationID, _ = value.(string)
		}
	}
}

func graphTestTimePointer(value any) *time.Time {
	if value == nil {
		return nil
	}
	pointer, _ := value.(*time.Time)
	return pointer
}

func graphTestRegistry(t *testing.T) *modelgateway.Registry {
	t.Helper()
	registry := modelgateway.NewRegistry(time.Second)
	if err := registry.RegisterProvider(modelgateway.Provider{ID: "test", DisplayName: "Test", Protocol: "test", Implemented: true}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterModel(modelgateway.Model{ID: "test.model", ProviderID: "test", Name: "Test model", Configured: true}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterPipeline(modelgateway.Pipeline{
		ID:           "test.chat",
		DisplayName:  "Test chat",
		ModelID:      "test.model",
		Kind:         "chat",
		Enabled:      true,
		Capabilities: modelgateway.Capabilities{Chat: true},
	}); err != nil {
		t.Fatal(err)
	}
	return registry
}

func graphTestDefinition(name string, risk hub.RiskLevel, approval, readOnly, idempotent, destructive bool) hub.ToolDefinition {
	return hub.ToolDefinition{
		Name:             name,
		InputSchema:      json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`),
		Risk:             risk,
		RequiresApproval: approval,
		ReadOnly:         readOnly,
		Idempotent:       idempotent,
		Destructive:      destructive,
	}
}

func newGraphTestService(t *testing.T, definition hub.ToolDefinition, replies []graphTestReply) (*Service, *graphTestStore, *graphTestModel, *graphTestMCP) {
	t.Helper()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	store := newGraphTestStore(now)
	runtime := &graphTestModel{replies: replies}
	gateway := &graphTestMCP{definition: definition}
	service, err := NewService(
		store,
		runtime,
		gateway,
		graphTestClock{now: now},
		WithRegistry(graphTestRegistry(t)),
		WithWorkerOptions(WorkerOptions{ID: "test-worker", Lease: time.Hour}),
	)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service, store, runtime, gateway
}

func graphTestToolPlan(toolName string) string {
	return fmt.Sprintf(`{"summary":"use a tool","steps":[{"title":"run tool","tool_name":%q,"arguments":{"value":"x"}}]}`, toolName)
}

func graphTestToolStep(task *model.AgentTask) *model.AgentStep {
	for index := range task.Steps {
		if task.Steps[index].Kind == model.AgentStepKindTool {
			return &task.Steps[index]
		}
	}
	return nil
}

func TestGraphNoToolPlanCompletesAcrossAutomaticCheckpoint(t *testing.T) {
	service, store, runtime, gateway := newGraphTestService(t, hub.ToolDefinition{}, []graphTestReply{
		{content: `{"summary":"answer directly","steps":[]}`},
		{content: "final answer"},
	})
	task, err := service.CreateTask(context.Background(), "alice", "answer this", "test.chat")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("ProcessTask: %v", err)
	}
	completed, err := service.GetTask(context.Background(), "alice", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != model.AgentTaskStatusSucceeded || completed.FinalAnswer != "final answer" {
		t.Fatalf("completed task = %#v", completed)
	}
	if runtime.calls != 2 || gateway.calls != 0 || store.setCalls == 0 {
		t.Fatalf("model calls=%d MCP calls=%d checkpoint sets=%d", runtime.calls, gateway.calls, store.setCalls)
	}
}

func TestPlanningFailureCanResumeFromDurablePlanStep(t *testing.T) {
	service, _, runtime, _ := newGraphTestService(t, hub.ToolDefinition{}, []graphTestReply{
		{err: errors.New("planner temporarily unavailable")},
		{content: `{"summary":"retry succeeded","steps":[]}`},
		{content: "answer after retry"},
	})
	task, err := service.CreateTask(context.Background(), "alice", "retry planning", "test.chat")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ProcessTask(context.Background(), task.ID); err == nil {
		t.Fatal("planning failure unexpectedly succeeded")
	}
	failed, err := service.GetTask(context.Background(), "alice", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != model.AgentTaskStatusFailed || len(failed.Steps) != 1 || failed.Steps[0].Status != model.AgentStepStatusFailed {
		t.Fatalf("planning failure was not durably recorded: %#v", failed)
	}
	if _, err := service.ResumeTask(context.Background(), "alice", task.ID, false); err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("resumed ProcessTask: %v", err)
	}
	completed, err := service.GetTask(context.Background(), "alice", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != model.AgentTaskStatusSucceeded || completed.FinalAnswer != "answer after retry" || runtime.calls != 3 {
		t.Fatalf("resumed planning task = %#v, model calls=%d", completed, runtime.calls)
	}
}

func TestGraphApprovalInterruptAndCheckpointResume(t *testing.T) {
	definition := graphTestDefinition("danger.write", hub.RiskHigh, true, false, false, true)
	service, store, _, gateway := newGraphTestService(t, definition, []graphTestReply{
		{content: graphTestToolPlan(definition.Name)},
		{content: "approved result"},
	})
	task, err := service.CreateTask(context.Background(), "alice", "perform approved action", "test.chat")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("first ProcessTask: %v", err)
	}
	waiting, err := service.GetTask(context.Background(), "alice", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	step := graphTestToolStep(waiting)
	if waiting.Status != model.AgentTaskStatusWaitingApproval || step == nil || step.Status != model.AgentStepStatusWaitingApproval {
		t.Fatalf("task did not stop at approval: %#v", waiting)
	}
	if gateway.calls != 0 || store.setCalls == 0 {
		t.Fatalf("tool ran before approval or checkpoint missing: calls=%d sets=%d", gateway.calls, store.setCalls)
	}
	if _, err := service.ApproveTask(context.Background(), "alice", task.ID, step.ID); err != nil {
		t.Fatalf("ApproveTask: %v", err)
	}
	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("resumed ProcessTask: %v", err)
	}
	completed, err := service.GetTask(context.Background(), "alice", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != model.AgentTaskStatusSucceeded || gateway.calls != 1 || !gateway.tokenSeen {
		t.Fatalf("approval resume failed: task=%#v calls=%d token=%v", completed, gateway.calls, gateway.tokenSeen)
	}
}

func TestGraphRejectNeverCallsTool(t *testing.T) {
	definition := graphTestDefinition("danger.write", hub.RiskHigh, true, false, false, true)
	service, _, _, gateway := newGraphTestService(t, definition, []graphTestReply{{content: graphTestToolPlan(definition.Name)}})
	task, err := service.CreateTask(context.Background(), "alice", "do not actually run", "test.chat")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	waiting, _ := service.GetTask(context.Background(), "alice", task.ID)
	step := graphTestToolStep(waiting)
	if _, err := service.RejectTask(context.Background(), "alice", task.ID, step.ID, "not allowed"); err != nil {
		t.Fatal(err)
	}
	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	if gateway.calls != 0 {
		t.Fatalf("rejected tool was called %d times", gateway.calls)
	}
}

func TestGraphNonReplayableFailuresRequireExplicitReview(t *testing.T) {
	for _, test := range []struct {
		name          string
		callErr       error
		resultIsError bool
	}{
		{name: "transport error", callErr: errors.New("transport timeout")},
		{name: "MCP error result", resultIsError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition := graphTestDefinition("sideeffect.create", hub.RiskLow, false, false, false, false)
			service, _, _, gateway := newGraphTestService(t, definition, []graphTestReply{{content: graphTestToolPlan(definition.Name)}})
			gateway.callErr = test.callErr
			gateway.resultIsError = test.resultIsError
			task, err := service.CreateTask(context.Background(), "alice", "create side effect", "test.chat")
			if err != nil {
				t.Fatal(err)
			}
			if err := service.ProcessTask(context.Background(), task.ID); err != nil {
				t.Fatalf("ProcessTask should surface review as durable state: %v", err)
			}
			review, err := service.GetTask(context.Background(), "alice", task.ID)
			if err != nil {
				t.Fatal(err)
			}
			step := graphTestToolStep(review)
			if review.Status != model.AgentTaskStatusRequiresReview || step == nil || step.Status != model.AgentStepStatusExecutionUnknown {
				t.Fatalf("uncertain call was not fenced for review: %#v", review)
			}
			if _, err := service.ResumeTask(context.Background(), "alice", task.ID, false); !errors.Is(err, ErrConflict) {
				t.Fatalf("ordinary resume error = %v, want ErrConflict", err)
			}
			resumed, err := service.ResumeTask(context.Background(), "alice", task.ID, true)
			if err != nil || resumed.Status != model.AgentTaskStatusPending {
				t.Fatalf("explicit retry_unknown failed: task=%#v err=%v", resumed, err)
			}
		})
	}
}

func TestGraphRetryReusesDurableOperationID(t *testing.T) {
	definition := graphTestDefinition("safe.write", hub.RiskLow, false, false, true, false)
	service, _, _, gateway := newGraphTestService(t, definition, []graphTestReply{
		{content: graphTestToolPlan(definition.Name)},
		{content: "recovered answer"},
	})
	gateway.callErr = errors.New("temporary upstream failure")
	task, err := service.CreateTask(context.Background(), "alice", "retry a safe operation", "test.chat")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ProcessTask(context.Background(), task.ID); err == nil {
		t.Fatal("first invocation unexpectedly succeeded")
	}
	failed, err := service.GetTask(context.Background(), "alice", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	step := graphTestToolStep(failed)
	if step == nil || step.OperationID == "" || len(gateway.operationIDs) != 1 || gateway.operationIDs[0] != step.OperationID {
		t.Fatalf("first durable operation ID was not persisted and forwarded: step=%#v calls=%#v", step, gateway.operationIDs)
	}

	gateway.callErr = nil
	if _, err := service.ResumeTask(context.Background(), "alice", task.ID, false); err != nil {
		t.Fatalf("resume failed task: %v", err)
	}
	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("process resumed task: %v", err)
	}
	if len(gateway.operationIDs) != 2 || gateway.operationIDs[1] != step.OperationID {
		t.Fatalf("retry operation IDs = %#v, want the original %q", gateway.operationIDs, step.OperationID)
	}
}

func TestGraphPolicyDriftRequiresReviewAndFreshApproval(t *testing.T) {
	planned := graphTestDefinition("mutable.write", hub.RiskLow, false, true, true, false)
	refreshed := graphTestDefinition("mutable.write", hub.RiskHigh, true, false, false, true)
	service, _, _, gateway := newGraphTestService(t, planned, []graphTestReply{
		{content: graphTestToolPlan(planned.Name)},
		{content: "result after policy review"},
	})
	gateway.refreshedDefinition = &refreshed
	task, err := service.CreateTask(context.Background(), "alice", "run changing tool", "test.chat")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("policy review ProcessTask: %v", err)
	}
	review, err := service.GetTask(context.Background(), "alice", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	step := graphTestToolStep(review)
	if review.Status != model.AgentTaskStatusRequiresReview || step == nil ||
		step.Status != model.AgentStepStatusExecutionUnknown || !step.RequiresApproval || !step.Destructive || gateway.calls != 0 {
		t.Fatalf("policy drift did not stop execution: task=%#v calls=%d", review, gateway.calls)
	}
	if _, err := service.ResumeTask(context.Background(), "alice", task.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("review resume: %v", err)
	}
	waiting, _ := service.GetTask(context.Background(), "alice", task.ID)
	step = graphTestToolStep(waiting)
	if waiting.Status != model.AgentTaskStatusWaitingApproval || step.Status != model.AgentStepStatusWaitingApproval || gateway.calls != 0 {
		t.Fatalf("refreshed high-risk policy did not require approval: %#v", waiting)
	}
	if _, err := service.ApproveTask(context.Background(), "alice", task.ID, step.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	completed, _ := service.GetTask(context.Background(), "alice", task.ID)
	if completed.Status != model.AgentTaskStatusSucceeded || gateway.calls != 1 || !gateway.tokenSeen {
		t.Fatalf("approved refreshed policy did not finish: task=%#v calls=%d token=%v", completed, gateway.calls, gateway.tokenSeen)
	}
}

func TestResumeRebuildsFromDurableStateWhenCheckpointIsCorrupt(t *testing.T) {
	service, store, _, _ := newGraphTestService(t, hub.ToolDefinition{}, []graphTestReply{
		{content: `{"summary":"answer directly","steps":[]}`},
		{err: errors.New("temporary finalizer failure")},
		{content: "recovered answer"},
	})
	task, err := service.CreateTask(context.Background(), "alice", "recover this", "test.chat")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ProcessTask(context.Background(), task.ID); err == nil {
		t.Fatal("first run unexpectedly succeeded")
	}

	store.mu.Lock()
	store.checkpoints[task.ID] = []byte("corrupt Eino checkpoint")
	store.mu.Unlock()
	if _, err := service.ResumeTask(context.Background(), "alice", task.ID, false); err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("ProcessTask after rebuilding checkpoint: %v", err)
	}
	recovered, err := service.GetTask(context.Background(), "alice", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != model.AgentTaskStatusSucceeded || recovered.FinalAnswer != "recovered answer" {
		t.Fatalf("recovered task = %#v", recovered)
	}
}

func TestServiceOwnerIsolation(t *testing.T) {
	service, _, _, _ := newGraphTestService(t, hub.ToolDefinition{}, nil)
	task, err := service.CreateTask(context.Background(), "alice", "private goal", "test.chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetTask(context.Background(), "mallory", task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner GetTask error = %v, want ErrNotFound", err)
	}
}
