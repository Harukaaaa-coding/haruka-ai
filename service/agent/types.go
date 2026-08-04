package agent

import (
	"context"
	"errors"
	"time"

	hub "GopherAI/common/mcphub"
	"GopherAI/model"

	"github.com/cloudwego/eino/schema"
)

const (
	maxPlanToolSteps         = 8
	maxGoalRunes             = 12_000
	maxTitleRunes            = 200
	maxInstructionRunes      = 4_000
	maxPlanSummaryRunes      = 4_000
	maxToolOutputRunes       = 16_000
	maxFinalAnswerRunes      = 24_000
	maxArgumentsPreviewRunes = 4_000
	// maxMCPRequestIDRunes matches agent_steps.mcp_request_id. A gateway that
	// returns something wider must not fail the step: for a non-idempotent tool
	// that would surface as a false execution_unknown and pull in a human.
	maxMCPRequestIDRunes = 36

	defaultListLimit = 30
	maxListLimit     = 200

	defaultPollInterval  = 2 * time.Second
	defaultLeaseDuration = 30 * time.Second
	defaultStaleAfter    = 10 * time.Minute
	defaultMaxRunSteps   = 40
)

var (
	ErrInvalidInput         = errors.New("invalid agent input")
	ErrNotFound             = errors.New("agent task not found")
	ErrConflict             = errors.New("agent task conflict")
	ErrInvalidRequest       = ErrInvalidInput
	ErrInvalidPlan          = errors.New("invalid agent plan")
	ErrTaskStateConflict    = ErrConflict
	ErrApprovalPending      = errors.New("agent approval is pending")
	ErrTaskStopped          = errors.New("agent task has stopped")
	ErrExecutionNeedsReview = errors.New("agent tool execution requires review")
)

// ModelRuntime is the deliberately small model boundary used by the graph.
// The production implementation delegates to aihelper.AIModelFactory, while
// tests can provide a deterministic scripted model without network access.
type ModelRuntime interface {
	Generate(ctx context.Context, selector, userName string, messages []*schema.Message) (*schema.Message, error)
}

// MCPGateway keeps every tool call behind MCP Hub's validation, allowlist,
// approval and audit boundary.
type MCPGateway interface {
	Tools(ctx context.Context) ([]hub.ToolDefinition, error)
	Call(ctx context.Context, request hub.InvokeRequest) (*hub.InvocationResult, error)
	CreateApproval(ctx context.Context, userName, toolName string, arguments map[string]any) (*hub.ApprovalChallenge, error)
	Approve(userName, challengeID string) (*hub.IssuedApproval, error)
}

// Store is the persistence contract needed by the graph and worker. GormStore
// implements it; keeping the interface here makes state transitions testable.
type Store interface {
	CreateTaskWithInitialStep(ctx context.Context, task *model.AgentTask, step *model.AgentStep) error
	GetOwnedTask(ctx context.Context, userName, taskID string) (*model.AgentTask, error)
	GetTaskByID(ctx context.Context, taskID string) (*model.AgentTask, error)
	ListOwnedTasks(ctx context.Context, userName string, offset, limit int) ([]model.AgentTask, int64, error)
	ListPendingTaskIDs(ctx context.Context, limit int) ([]string, error)
	ClaimTask(ctx context.Context, taskID, workerID string, lease time.Duration) (*model.AgentTask, bool, error)
	RenewLeaseFenced(ctx context.Context, taskID string, runVersion uint64, workerID string, lease time.Duration) error
	SavePlanWithSummary(ctx context.Context, taskID string, runVersion uint64, summary string, steps []model.AgentStep) error
	UpdateTaskFenced(ctx context.Context, taskID string, runVersion uint64, updates map[string]any) error
	UpdateStepFenced(ctx context.Context, taskID, stepID string, runVersion uint64, updates map[string]any) error
	MarkExecutionUnknownFenced(ctx context.Context, taskID, stepID string, runVersion uint64, message string) error
	MarkPolicyReviewFenced(ctx context.Context, taskID, stepID string, runVersion uint64, message, risk string, requiresApproval, readOnly, idempotent, destructive bool) error
	UpdateOwnedTask(ctx context.Context, userName, taskID string, updates map[string]any) error
	UpdateOwnedStepDecision(ctx context.Context, userName, taskID, stepID, decision, reason, expectedDigest string) (*model.AgentStep, error)
	ResumeOwnedTask(ctx context.Context, userName, taskID string, retryUnknown bool) error
	CancelOwnedTask(ctx context.Context, userName, taskID string) error
	RecoverStaleTasks(ctx context.Context, staleBefore time.Time) error

	// Eino compose.CheckPointStore.
	Get(ctx context.Context, checkpointID string) ([]byte, bool, error)
	Set(ctx context.Context, checkpointID string, checkpoint []byte) error
}

// Clock is sufficient to deterministically test leases, recovery and terminal
// timestamps. After is used by the polling worker.
type Clock interface {
	Now() time.Time
	After(duration time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                                { return time.Now().UTC() }
func (realClock) After(duration time.Duration) <-chan time.Time { return time.After(duration) }

// GraphState is intentionally tiny. All durable task/step data stays in MySQL;
// an Eino checkpoint only needs to identify which task its nodes should reload.
type GraphState struct {
	TaskID string
}

// PlannedToolStep is the only step shape accepted from the planning model.
// Risk and approval fields are deliberately absent: they always come from the
// trusted MCP ToolDefinition.
type PlannedToolStep struct {
	Title       string         `json:"title"`
	Instruction string         `json:"instruction,omitempty"`
	ToolName    string         `json:"tool_name"`
	Arguments   map[string]any `json:"arguments"`
}

type PlanDocument struct {
	Summary string            `json:"summary"`
	Steps   []PlannedToolStep `json:"steps"`
}

// ApprovalInterrupt is safe to return to an API. It contains a redacted
// preview and digest, never raw arguments or an MCP approval token.
type ApprovalInterrupt struct {
	TaskID           string `json:"task_id"`
	StepID           string `json:"step_id"`
	ToolName         string `json:"tool_name"`
	Risk             string `json:"risk"`
	ArgumentsDigest  string `json:"arguments_digest"`
	ArgumentsPreview string `json:"arguments_preview"`
}

// WorkerOptions can be shortened in tests without changing production safety
// defaults.
type WorkerOptions struct {
	ID           string
	PollInterval time.Duration
	Lease        time.Duration
	StaleAfter   time.Duration
	BatchSize    int
}
