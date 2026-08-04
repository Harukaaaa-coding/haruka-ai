package agent

import (
	"GopherAI/model"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/compose"
)

var _ compose.CheckPointStore = (*GormStore)(nil)

func TestStepCanReplayAfterCrash(t *testing.T) {
	tests := []struct {
		name string
		step model.AgentStep
		want bool
	}{
		{name: "planner", step: model.AgentStep{Kind: model.AgentStepKindPlan}, want: true},
		{name: "finalizer", step: model.AgentStep{Kind: model.AgentStepKindFinal}, want: true},
		{name: "read only tool", step: model.AgentStep{Kind: model.AgentStepKindTool, ReadOnly: true}, want: true},
		{name: "idempotent tool", step: model.AgentStep{Kind: model.AgentStepKindTool, Idempotent: true}, want: true},
		{name: "non idempotent tool", step: model.AgentStep{Kind: model.AgentStepKindTool}, want: false},
		{name: "destructive tool", step: model.AgentStep{Kind: model.AgentStepKindTool, Destructive: true}, want: false},
		{name: "destructive read only claim", step: model.AgentStep{Kind: model.AgentStepKindTool, Destructive: true, ReadOnly: true}, want: false},
		{name: "destructive idempotent claim", step: model.AgentStep{Kind: model.AgentStepKindTool, Destructive: true, Idempotent: true}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := stepCanReplayAfterCrash(test.step); got != test.want {
				t.Fatalf("stepCanReplayAfterCrash() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestStaleRecoveryIncludesPlanningAndRunningTasks(t *testing.T) {
	statuses := staleRecoverableTaskStatuses()
	seen := make(map[string]bool, len(statuses))
	for _, status := range statuses {
		seen[status] = true
	}
	if !seen[model.AgentTaskStatusPlanning] || !seen[model.AgentTaskStatusRunning] {
		t.Fatalf("stale recovery statuses = %#v", statuses)
	}
	if len(seen) != 2 {
		t.Fatalf("unexpected stale recovery statuses = %#v", statuses)
	}
}

func TestLeaseRenewalOnlyAllowsActiveTaskStates(t *testing.T) {
	statuses := leaseRenewableTaskStatuses()
	seen := make(map[string]bool, len(statuses))
	for _, status := range statuses {
		seen[status] = true
	}
	if !seen[model.AgentTaskStatusRunning] || !seen[model.AgentTaskStatusPlanning] || len(seen) != 2 {
		t.Fatalf("lease-renewable statuses = %#v", statuses)
	}
	for _, terminal := range []string{
		model.AgentTaskStatusWaitingApproval,
		model.AgentTaskStatusSucceeded,
		model.AgentTaskStatusFailed,
		model.AgentTaskStatusCancelled,
	} {
		if seen[terminal] {
			t.Fatalf("terminal/non-running status %q can renew a lease", terminal)
		}
	}
}

func TestFailedPlanningStepCanResume(t *testing.T) {
	planStep := model.AgentStep{Kind: model.AgentStepKindPlan, Status: model.AgentStepStatusFailed}
	if !taskCanResume(model.AgentTaskStatusFailed, false) || !stepCanResume(planStep.Status, false) {
		t.Fatal("a failed planning step should be resumable")
	}
	if taskCanResume(model.AgentTaskStatusRequiresReview, false) || stepCanResume(model.AgentStepStatusExecutionUnknown, false) {
		t.Fatal("an unknown execution must require explicit retryUnknown")
	}
	if !taskCanResume(model.AgentTaskStatusRequiresReview, true) || !stepCanResume(model.AgentStepStatusExecutionUnknown, true) {
		t.Fatal("retryUnknown should unlock an explicitly reviewed unknown execution")
	}
}

func TestRejectDecisionInvalidatesRunButApproveDoesNot(t *testing.T) {
	if !decisionInvalidatesRun(model.AgentApprovalDecisionRejected) {
		t.Fatal("reject must invalidate the interrupted graph run")
	}
	if decisionInvalidatesRun(model.AgentApprovalDecisionApproved) {
		t.Fatal("approve must preserve the checkpoint run until the worker claims it")
	}
	if decisionInvalidatesRun(model.AgentApprovalDecisionPending) {
		t.Fatal("pending is not a terminal human decision")
	}
}

func TestNormalizePlanStepsOwnsAndResetsRuntimeState(t *testing.T) {
	now := time.Now()
	task := model.AgentTask{ID: "task-1", UserName: "alice"}
	steps := []model.AgentStep{{
		ID:                "step-1",
		TaskID:            "other-task",
		UserName:          "mallory",
		Sequence:          99,
		Kind:              model.AgentStepKindTool,
		Status:            model.AgentStepStatusSucceeded,
		Attempts:          3,
		ToolName:          "weather.get_weather",
		ToolArgumentsJSON: `{"city":"北京"}`,
		ToolOutputJSON:    "sensitive output",
		ResultSummary:     "old result",
		ApprovalDecision:  model.AgentApprovalDecisionApproved,
		ApprovalReason:    "old decision",
		ApprovalDecidedAt: &now,
		MCPRequestID:      "old-request",
		OperationID:       "old-operation",
		StartedAt:         &now,
		FinishedAt:        &now,
	}}

	normalized := normalizePlanSteps(task, steps)
	if len(normalized) != 1 {
		t.Fatalf("normalized length = %d", len(normalized))
	}
	step := normalized[0]
	if step.TaskID != task.ID || step.UserName != task.UserName || step.Sequence != 1 {
		t.Fatalf("ownership/sequence not normalized: %#v", step)
	}
	if step.Status != model.AgentStepStatusPending || step.Attempts != 0 {
		t.Fatalf("runtime status not reset: %#v", step)
	}
	if step.ToolArgumentsJSON == "" {
		t.Fatal("durable tool arguments were unexpectedly removed")
	}
	if step.ToolOutputJSON != "" || step.ResultSummary != "" || step.MCPRequestID != "" || step.OperationID != "" || step.StartedAt != nil || step.FinishedAt != nil {
		t.Fatalf("previous execution state was retained: %#v", step)
	}
	if step.ApprovalDecision != model.AgentApprovalDecisionPending || step.ApprovalDecidedAt != nil {
		t.Fatalf("approval state was not reset: %#v", step)
	}
}

// TestCheckpointEnvelopeIsolatesGraphVersions pins the upgrade behavior: a blob
// written by another graph version (or before envelopes existed) must read back
// as absent so the worker starts a fresh run instead of failing the task on a
// deserialization error.
func TestCheckpointEnvelopeIsolatesGraphVersions(t *testing.T) {
	raw := []byte{0x00, 0x01, '\n', 0xff, 'e', 'i', 'n', 'o'}
	decoded, ok := decodeCheckpointEnvelope(encodeCheckpointEnvelope(raw))
	if !ok || !bytes.Equal(decoded, raw) {
		t.Fatalf("round trip = (%v, %v), want the original blob", decoded, ok)
	}

	for _, test := range []struct {
		name   string
		stored []byte
	}{
		{name: "legacy blob without envelope", stored: raw},
		{name: "other graph version", stored: []byte("gopherai_task_agent_v2\npayload")},
		{name: "version without separator", stored: []byte(CheckpointSchemaVersion + "payload")},
		{name: "empty", stored: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, ok := decodeCheckpointEnvelope(test.stored); ok {
				t.Fatalf("decodeCheckpointEnvelope(%q) reported a decodable checkpoint", test.stored)
			}
		})
	}
}

func TestFencingTokenContext(t *testing.T) {
	ctx := WithFencingToken(context.Background(), "task-1", 7)
	taskID, version, ok := FencingTokenFromContext(ctx)
	if !ok || taskID != "task-1" || version != 7 {
		t.Fatalf("FencingTokenFromContext() = %q, %d, %v", taskID, version, ok)
	}
	if _, _, ok := FencingTokenFromContext(context.Background()); ok {
		t.Fatal("unexpected fencing token in an unmodified context")
	}
	workerCtx := WithWorkerFencingToken(context.Background(), "task-2", 9, "worker-a")
	taskID, version, workerID, ok := WorkerFencingTokenFromContext(workerCtx)
	if !ok || taskID != "task-2" || version != 9 || workerID != "worker-a" {
		t.Fatalf("WorkerFencingTokenFromContext() = %q, %d, %q, %v", taskID, version, workerID, ok)
	}
	_, _, legacyWorkerID, ok := WorkerFencingTokenFromContext(ctx)
	if !ok || legacyWorkerID != "" {
		t.Fatalf("legacy fencing token worker ID = %q, ok=%v", legacyWorkerID, ok)
	}
}

func TestActiveLeaseFenceRequiresMatchingWorkerToken(t *testing.T) {
	ctx := WithWorkerFencingToken(context.Background(), "task-1", 7, "worker-a")
	if workerID, active := activeLeaseFenceFromContext(ctx, "task-1", 7); !active || workerID != "worker-a" {
		t.Fatalf("activeLeaseFenceFromContext() = %q, %v", workerID, active)
	}
	for _, test := range []struct {
		name       string
		ctx        context.Context
		taskID     string
		runVersion uint64
	}{
		{name: "legacy token", ctx: WithFencingToken(context.Background(), "task-1", 7), taskID: "task-1", runVersion: 7},
		{name: "different task", ctx: ctx, taskID: "task-2", runVersion: 7},
		{name: "different run", ctx: ctx, taskID: "task-1", runVersion: 8},
		{name: "no token", ctx: context.Background(), taskID: "task-1", runVersion: 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			if workerID, active := activeLeaseFenceFromContext(test.ctx, test.taskID, test.runVersion); active || workerID != "" {
				t.Fatalf("activeLeaseFenceFromContext() = %q, %v", workerID, active)
			}
		})
	}
}

func TestSanitizedUpdatesRemoveImmutableFields(t *testing.T) {
	updates := sanitizedUpdates(map[string]any{
		"id":                 "replacement",
		"UserName":           "mallory",
		"run_version":        99,
		"checkpoint_version": 99,
		"status":             model.AgentTaskStatusSucceeded,
	}, immutableTaskColumns)
	if len(updates) != 1 || updates["status"] != model.AgentTaskStatusSucceeded {
		t.Fatalf("sanitized updates = %#v", updates)
	}
}

func TestAgentJSONHidesDurableSensitiveState(t *testing.T) {
	task := model.AgentTask{
		ID:         "task-1",
		UserName:   "private-owner",
		Goal:       "test",
		Checkpoint: []byte("private-checkpoint"),
		LeaseOwner: "private-worker",
		Steps: []model.AgentStep{{
			ID:                "step-1",
			TaskID:            "task-1",
			UserName:          "private-owner",
			ToolArgumentsJSON: "private-arguments",
			ToolOutputJSON:    "private-output",
			ArgumentsPreview:  `{"token":"[REDACTED]"}`,
		}},
	}
	encoded, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("Marshal(): %v", err)
	}
	text := string(encoded)
	for _, secret := range []string{"private-owner", "private-checkpoint", "private-worker", "private-arguments", "private-output"} {
		if strings.Contains(text, secret) {
			t.Fatalf("JSON leaked %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "[REDACTED]") {
		t.Fatalf("safe argument preview is missing: %s", text)
	}
}

func TestUninitializedStoreReturnsStableError(t *testing.T) {
	store := NewStore(nil)
	if _, err := store.ListPendingTaskIDs(context.Background(), 1); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("ListPendingTaskIDs() error = %v", err)
	}
	if err := store.MarkExecutionUnknownFenced(context.Background(), "task-1", "step-1", 1, "unknown"); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("MarkExecutionUnknownFenced() error = %v", err)
	}
	if err := store.MarkPolicyReviewFenced(
		context.Background(), "task-1", "step-1", 1, "changed", "high", true, false, false, true,
	); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("MarkPolicyReviewFenced() error = %v", err)
	}
}

func TestTruncateRunesDoesNotSplitChineseText(t *testing.T) {
	if got := truncateRunes("批准这个操作", 4); got != "批准这个" {
		t.Fatalf("truncateRunes() = %q", got)
	}
}
