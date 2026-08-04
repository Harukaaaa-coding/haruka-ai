package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"GopherAI/common/modelgateway"
	agentdao "GopherAI/dao/agent"
	"GopherAI/model"

	"github.com/cloudwego/eino/compose"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ServiceOption func(*Service)

func WithRegistry(registry *modelgateway.Registry) ServiceOption {
	return func(service *Service) { service.registry = registry }
}

func WithWorkerOptions(options WorkerOptions) ServiceOption {
	return func(service *Service) { service.workerOptions = options }
}

func WithMaxGraphSteps(maximum int) ServiceOption {
	return func(service *Service) {
		if maximum > 0 {
			service.maxRunSteps = maximum
		}
	}
}

type Service struct {
	store  Store
	models ModelRuntime
	mcp    MCPGateway
	clock  Clock

	registry      *modelgateway.Registry
	runner        compose.Runnable[string, string]
	maxRunSteps   int
	workerOptions WorkerOptions
	hints         chan string

	runningMu sync.Mutex
	running   map[string]context.CancelFunc

	workerMu      sync.Mutex
	workerStarted bool
	workerDone    chan struct{}
}

func NewService(store Store, models ModelRuntime, gateway MCPGateway, clock Clock, options ...ServiceOption) (*Service, error) {
	if store == nil || models == nil || gateway == nil {
		return nil, fmt.Errorf("%w: store, model runtime and MCP gateway are required", ErrInvalidInput)
	}
	if clock == nil {
		clock = realClock{}
	}
	service := &Service{
		store:         store,
		models:        models,
		mcp:           gateway,
		clock:         clock,
		maxRunSteps:   defaultMaxRunSteps,
		hints:         make(chan string, 128),
		running:       make(map[string]context.CancelFunc),
		workerOptions: WorkerOptions{},
	}
	for _, option := range options {
		option(service)
	}
	if service.registry == nil {
		service.registry = modelgateway.GetDefaultRegistry()
	}
	service.normalizeWorkerOptions()

	runner, err := service.buildGraph(context.Background())
	if err != nil {
		return nil, fmt.Errorf("compile Agent graph: %w", err)
	}
	service.runner = runner
	return service, nil
}

func (service *Service) CreateTask(ctx context.Context, userName, goal, modelID string) (*model.AgentTask, error) {
	userName = strings.TrimSpace(userName)
	goal = strings.TrimSpace(goal)
	modelID = strings.TrimSpace(modelID)
	if userName == "" || goal == "" || modelID == "" || len([]rune(goal)) > maxGoalRunes {
		return nil, fmt.Errorf("%w: username, goal and model ID are required and goal is limited to %d characters", ErrInvalidInput, maxGoalRunes)
	}
	canonical, err := service.validateModelSelector(modelID)
	if err != nil {
		return nil, err
	}

	taskID := uuid.NewString()
	task := &model.AgentTask{
		ID:       taskID,
		UserName: userName,
		Goal:     goal,
		ModelID:  canonical,
		Status:   model.AgentTaskStatusPending,
	}
	planStep := &model.AgentStep{
		ID:               uuid.NewString(),
		TaskID:           taskID,
		UserName:         userName,
		Sequence:         0,
		Kind:             model.AgentStepKindPlan,
		NodeKey:          nodePlan,
		Title:            "制定任务计划",
		Status:           model.AgentStepStatusPending,
		ApprovalDecision: model.AgentApprovalDecisionPending,
	}
	if err := service.store.CreateTaskWithInitialStep(ctx, task, planStep); err != nil {
		return nil, service.normalizeStoreError(err)
	}
	service.hint(task.ID)
	return task, nil
}

func (service *Service) GetTask(ctx context.Context, userName, taskID string) (*model.AgentTask, error) {
	if strings.TrimSpace(userName) == "" || strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("%w: username and task ID are required", ErrInvalidInput)
	}
	task, err := service.store.GetOwnedTask(ctx, userName, taskID)
	if err != nil {
		return nil, service.normalizeStoreError(err)
	}
	return task, nil
}

func (service *Service) ListTasks(ctx context.Context, userName, status string, limit, offset int) ([]model.AgentTask, int64, error) {
	userName = strings.TrimSpace(userName)
	status = strings.TrimSpace(strings.ToLower(status))
	if userName == "" || offset < 0 {
		return nil, 0, fmt.Errorf("%w: invalid list query", ErrInvalidInput)
	}
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	if status == "" {
		tasks, total, err := service.store.ListOwnedTasks(ctx, userName, offset, limit)
		return tasks, total, service.normalizeStoreError(err)
	}
	if !validTaskStatus(status) {
		return nil, 0, fmt.Errorf("%w: unknown task status", ErrInvalidInput)
	}

	// The persistence API intentionally stays simple. Walk owner pages so the
	// status filter has correct total/offset semantics rather than filtering a
	// single already-paginated page.
	all := make([]model.AgentTask, 0)
	for pageOffset := 0; ; pageOffset += maxListLimit {
		page, total, err := service.store.ListOwnedTasks(ctx, userName, pageOffset, maxListLimit)
		if err != nil {
			return nil, 0, service.normalizeStoreError(err)
		}
		for _, task := range page {
			if task.Status == status {
				all = append(all, task)
			}
		}
		if pageOffset+len(page) >= int(total) || len(page) == 0 {
			break
		}
	}
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

// ApproveTask requires the caller to echo back the arguments digest it
// displayed to the human. The digest is re-checked here for a precise error and
// again inside the store's CAS predicate, which is what actually makes a stale
// or replayed approval fail instead of authorizing arguments nobody reviewed.
func (service *Service) ApproveTask(ctx context.Context, userName, taskID, stepID, expectedDigest string) (*model.AgentTask, error) {
	expectedDigest = strings.ToLower(strings.TrimSpace(expectedDigest))
	if expectedDigest == "" {
		return nil, fmt.Errorf("%w: expected arguments digest is required to approve a step", ErrInvalidInput)
	}
	task, step, err := service.ownedApprovalStep(ctx, userName, taskID, stepID)
	if err != nil {
		return nil, err
	}
	if !step.RequiresApproval || task.Status != model.AgentTaskStatusWaitingApproval || step.Status != model.AgentStepStatusWaitingApproval {
		return nil, fmt.Errorf("%w: step is not waiting for approval", ErrConflict)
	}
	if !strings.EqualFold(strings.TrimSpace(step.ArgumentsDigest), expectedDigest) {
		return nil, fmt.Errorf("%w: approved arguments no longer match the pending step", ErrConflict)
	}
	if _, err := service.store.UpdateOwnedStepDecision(ctx, userName, taskID, stepID, model.AgentApprovalDecisionApproved, "", expectedDigest); err != nil {
		return nil, service.normalizeStoreError(err)
	}
	service.hint(taskID)
	return service.GetTask(ctx, userName, taskID)
}

func (service *Service) RejectTask(ctx context.Context, userName, taskID, stepID, reason string) (*model.AgentTask, error) {
	task, step, err := service.ownedApprovalStep(ctx, userName, taskID, stepID)
	if err != nil {
		return nil, err
	}
	if !step.RequiresApproval || task.Status != model.AgentTaskStatusWaitingApproval || step.Status != model.AgentStepStatusWaitingApproval {
		return nil, fmt.Errorf("%w: step is not waiting for approval", ErrConflict)
	}
	// Rejection carries no digest on purpose: halting a task must remain
	// possible from a stale UI, and refusing to run something is always safe.
	if _, err := service.store.UpdateOwnedStepDecision(ctx, userName, taskID, stepID, model.AgentApprovalDecisionRejected, truncateRunes(strings.TrimSpace(reason), 500), ""); err != nil {
		return nil, service.normalizeStoreError(err)
	}
	return service.GetTask(ctx, userName, taskID)
}

func (service *Service) ResumeTask(ctx context.Context, userName, taskID string, retryUnknown bool) (*model.AgentTask, error) {
	if _, err := service.GetTask(ctx, userName, taskID); err != nil {
		return nil, err
	}
	if err := service.store.ResumeOwnedTask(ctx, userName, taskID, retryUnknown); err != nil {
		return nil, service.normalizeStoreError(err)
	}
	service.hint(taskID)
	return service.GetTask(ctx, userName, taskID)
}

func (service *Service) CancelTask(ctx context.Context, userName, taskID string) (*model.AgentTask, error) {
	task, err := service.GetTask(ctx, userName, taskID)
	if err != nil {
		return nil, err
	}
	if task.Status == model.AgentTaskStatusCancelled {
		return task, nil
	}
	if taskTerminal(task.Status) {
		return nil, fmt.Errorf("%w: terminal task cannot be cancelled", ErrConflict)
	}
	if err := service.store.CancelOwnedTask(ctx, userName, taskID); err != nil {
		return nil, service.normalizeStoreError(err)
	}
	service.cancelRunning(taskID)
	return service.GetTask(ctx, userName, taskID)
}

func (service *Service) ownedApprovalStep(ctx context.Context, userName, taskID, stepID string) (*model.AgentTask, *model.AgentStep, error) {
	task, err := service.GetTask(ctx, userName, taskID)
	if err != nil {
		return nil, nil, err
	}
	for index := range task.Steps {
		if task.Steps[index].ID == stepID {
			return task, &task.Steps[index], nil
		}
	}
	return nil, nil, ErrNotFound
}

func (service *Service) validateModelSelector(selector string) (string, error) {
	if service.registry == nil {
		return "", fmt.Errorf("%w: model catalog is unavailable", ErrInvalidInput)
	}
	resolved, err := service.registry.ResolvePipeline(selector)
	if err != nil {
		return "", fmt.Errorf("%w: unknown model selector", ErrInvalidInput)
	}
	if !resolved.Available {
		return "", fmt.Errorf("%w: selected model is unavailable", ErrInvalidInput)
	}
	if !strings.EqualFold(strings.TrimSpace(resolved.Pipeline.Kind), "chat") {
		return "", fmt.Errorf("%w: Agent planning requires an available chat pipeline", ErrInvalidInput)
	}
	return resolved.Pipeline.ID, nil
}

func (service *Service) normalizeStoreError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	case errors.Is(err, agentdao.ErrInvalidInput):
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	case errors.Is(err, agentdao.ErrConflict):
		return fmt.Errorf("%w: %v", ErrConflict, err)
	default:
		return err
	}
}

func (service *Service) hint(taskID string) {
	select {
	case service.hints <- taskID:
	default:
		// The durable pending row is the source of truth; a full hint channel is
		// harmless because the two-second poll will discover the task.
	}
}

func validTaskStatus(status string) bool {
	switch status {
	case model.AgentTaskStatusPending, model.AgentTaskStatusPlanning, model.AgentTaskStatusRunning,
		model.AgentTaskStatusWaitingApproval, model.AgentTaskStatusRequiresReview,
		model.AgentTaskStatusSucceeded, model.AgentTaskStatusFailed,
		model.AgentTaskStatusRejected, model.AgentTaskStatusCancelled:
		return true
	default:
		return false
	}
}

func (service *Service) normalizeWorkerOptions() {
	if strings.TrimSpace(service.workerOptions.ID) == "" {
		service.workerOptions.ID = "agent-worker-" + uuid.NewString()
	}
	if service.workerOptions.PollInterval <= 0 {
		service.workerOptions.PollInterval = defaultPollInterval
	}
	if service.workerOptions.Lease <= 0 {
		service.workerOptions.Lease = defaultLeaseDuration
	}
	if service.workerOptions.StaleAfter <= 0 {
		service.workerOptions.StaleAfter = defaultStaleAfter
	}
	if service.workerOptions.BatchSize <= 0 || service.workerOptions.BatchSize > maxListLimit {
		service.workerOptions.BatchSize = 20
	}
}
