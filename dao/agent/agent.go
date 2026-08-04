package agent

import (
	"GopherAI/common/mysql"
	"GopherAI/model"
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrDatabaseUnavailable = errors.New("mysql is not initialized")
	ErrInvalidInput        = errors.New("invalid agent persistence input")
	ErrConflict            = errors.New("agent state conflict")
)

const (
	defaultTaskListLimit   = 50
	maxTaskListLimit       = 200
	defaultPendingLimit    = 20
	defaultLeaseDuration   = 2 * time.Minute
	maxApprovalReasonRunes = 500

	// recoverStaleBatchSize bounds how many abandoned tasks are locked and
	// recovered between scans, keeping any single transaction's lock scope small.
	recoverStaleBatchSize = 50
	// recoverStaleMaxRounds bounds how many batches one call drains, so a large
	// backlog clears without letting a poll run unboundedly long.
	recoverStaleMaxRounds = 20
)

// GormStore contains all durable Agent state operations. Supplying the DB
// explicitly keeps service and graph tests independent from the package-level
// MySQL connection.
type GormStore struct {
	db  *gorm.DB
	now func() time.Time
}

func NewStore(db *gorm.DB) *GormStore {
	return &GormStore{db: db, now: time.Now}
}

func DefaultStore() (*GormStore, error) {
	if mysql.DB == nil {
		return nil, ErrDatabaseUnavailable
	}
	return NewStore(mysql.DB), nil
}

func (store *GormStore) database() (*gorm.DB, error) {
	if store == nil || store.db == nil {
		return nil, ErrDatabaseUnavailable
	}
	return store.db, nil
}

func (store *GormStore) currentTime() time.Time {
	if store != nil && store.now != nil {
		return store.now().UTC()
	}
	return time.Now().UTC()
}

func (store *GormStore) CreateTaskWithInitialStep(ctx context.Context, task *model.AgentTask, initialStep *model.AgentStep) error {
	db, err := store.database()
	if err != nil {
		return err
	}
	if task == nil || initialStep == nil || strings.TrimSpace(task.ID) == "" || strings.TrimSpace(task.UserName) == "" {
		return ErrInvalidInput
	}
	if initialStep.TaskID != "" && initialStep.TaskID != task.ID {
		return ErrConflict
	}
	if initialStep.UserName != "" && initialStep.UserName != task.UserName {
		return ErrConflict
	}

	task.Status = model.AgentTaskStatusPending
	task.CurrentStepID = ""
	task.PlanSummary = ""
	task.FinalAnswer = ""
	task.ErrorMessage = ""
	task.Checkpoint = nil
	task.CheckpointVersion = 0
	task.RunVersion = 0
	task.Attempts = 0
	task.LeaseOwner = ""
	task.LeaseUntil = nil
	task.HeartbeatAt = nil
	task.StartedAt = nil
	task.FinishedAt = nil
	if initialStep.ID == "" {
		initialStep.ID = uuid.NewString()
	}
	initialStep.TaskID = task.ID
	initialStep.UserName = task.UserName
	initialStep.Sequence = 0
	initialStep.Kind = model.AgentStepKindPlan
	initialStep.Status = model.AgentStepStatusPending
	initialStep.Attempts = 0
	initialStep.ErrorMessage = ""
	initialStep.ToolName = ""
	initialStep.ToolArgumentsJSON = ""
	initialStep.ArgumentsDigest = ""
	initialStep.ArgumentsPreview = ""
	initialStep.ToolOutputJSON = ""
	initialStep.ResultSummary = ""
	initialStep.RiskLevel = ""
	initialStep.RequiresApproval = false
	initialStep.ReadOnly = false
	initialStep.Idempotent = false
	initialStep.Destructive = false
	initialStep.ApprovalDecision = model.AgentApprovalDecisionPending
	initialStep.ApprovalReason = ""
	initialStep.ApprovalDecidedAt = nil
	initialStep.MCPRequestID = ""
	initialStep.OperationID = ""
	initialStep.StartedAt = nil
	initialStep.FinishedAt = nil
	task.CurrentStepID = initialStep.ID

	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Omit("Steps").Create(task).Error; err != nil {
			return err
		}
		return tx.Create(initialStep).Error
	}); err != nil {
		return err
	}
	task.Steps = []model.AgentStep{*initialStep}
	return nil
}

func orderedSteps(db *gorm.DB) *gorm.DB {
	return db.Order("sequence ASC, created_at ASC, id ASC")
}

func (store *GormStore) GetOwnedTask(ctx context.Context, userName, taskID string) (*model.AgentTask, error) {
	db, err := store.database()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(userName) == "" || strings.TrimSpace(taskID) == "" {
		return nil, ErrInvalidInput
	}
	var task model.AgentTask
	err = db.WithContext(ctx).
		Preload("Steps", orderedSteps).
		Where("id = ? AND user_name = ?", taskID, userName).
		First(&task).Error
	return &task, err
}

func (store *GormStore) GetTaskByID(ctx context.Context, taskID string) (*model.AgentTask, error) {
	db, err := store.database()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(taskID) == "" {
		return nil, ErrInvalidInput
	}
	var task model.AgentTask
	err = db.WithContext(ctx).
		Preload("Steps", orderedSteps).
		Where("id = ?", taskID).
		First(&task).Error
	return &task, err
}

func (store *GormStore) ListOwnedTasks(ctx context.Context, userName string, offset, limit int) ([]model.AgentTask, int64, error) {
	db, err := store.database()
	if err != nil {
		return nil, 0, err
	}
	userName = strings.TrimSpace(userName)
	if userName == "" {
		return nil, 0, ErrInvalidInput
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = defaultTaskListLimit
	}
	if limit > maxTaskListLimit {
		limit = maxTaskListLimit
	}

	query := db.WithContext(ctx).Model(&model.AgentTask{}).Where("user_name = ?", userName)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var tasks []model.AgentTask
	err = query.Order("created_at DESC, id DESC").Offset(offset).Limit(limit).Find(&tasks).Error
	return tasks, total, err
}

// ListPendingTaskIDs returns identifiers rather than rows on purpose. The
// worker only ever claims by ID, and projecting to it keeps
// idx_agent_task_pending_scan covering — otherwise every poll pays a primary key
// lookup per row and ships the checkpoint longblob it never reads. Returning
// []model.AgentTask here would hand callers a struct whose every other field is
// silently zero.
func (store *GormStore) ListPendingTaskIDs(ctx context.Context, limit int) ([]string, error) {
	db, err := store.database()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = defaultPendingLimit
	}
	if limit > maxTaskListLimit {
		limit = maxTaskListLimit
	}
	var taskIDs []string
	err = db.WithContext(ctx).Model(&model.AgentTask{}).
		Where("status = ?", model.AgentTaskStatusPending).
		Order("created_at ASC, id ASC").
		Limit(limit).
		Pluck("id", &taskIDs).Error
	return taskIDs, err
}

// ClaimTask atomically acquires a pending task. RunVersion is a fencing token:
// every graph/checkpoint write from this run must use the returned value.
func (store *GormStore) ClaimTask(ctx context.Context, taskID, workerID string, leaseDuration time.Duration) (*model.AgentTask, bool, error) {
	db, err := store.database()
	if err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(taskID) == "" || strings.TrimSpace(workerID) == "" {
		return nil, false, ErrInvalidInput
	}
	if leaseDuration <= 0 {
		leaseDuration = defaultLeaseDuration
	}
	now := store.currentTime()
	leaseUntil := now.Add(leaseDuration)
	result := db.WithContext(ctx).Model(&model.AgentTask{}).
		Where("id = ? AND status = ?", taskID, model.AgentTaskStatusPending).
		Updates(map[string]any{
			"status":        model.AgentTaskStatusRunning,
			"attempts":      gorm.Expr("attempts + 1"),
			"run_version":   gorm.Expr("run_version + 1"),
			"lease_owner":   workerID,
			"lease_until":   &leaseUntil,
			"heartbeat_at":  &now,
			"started_at":    gorm.Expr("COALESCE(started_at, ?)", now),
			"finished_at":   nil,
			"error_message": "",
			"updated_at":    now,
		})
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, false, nil
	}
	task, err := store.GetTaskByID(ctx, taskID)
	return task, err == nil, err
}

// RenewLeaseFenced keeps a live graph run from being recovered by another
// worker while it is waiting on a long model or tool call. A changed worker or
// RunVersion means ownership has been lost and is reported as ErrConflict.
func (store *GormStore) RenewLeaseFenced(ctx context.Context, taskID string, runVersion uint64, workerID string, leaseDuration time.Duration) error {
	db, err := store.database()
	if err != nil {
		return err
	}
	if strings.TrimSpace(taskID) == "" || runVersion == 0 || strings.TrimSpace(workerID) == "" {
		return ErrInvalidInput
	}
	if leaseDuration <= 0 {
		leaseDuration = defaultLeaseDuration
	}
	now := store.currentTime()
	leaseUntil := now.Add(leaseDuration)
	result := db.WithContext(ctx).Model(&model.AgentTask{}).
		Where("id = ? AND run_version = ? AND lease_owner = ? AND lease_until > ? AND status IN ?",
			taskID, runVersion, workerID, now, leaseRenewableTaskStatuses()).
		Updates(map[string]any{
			"lease_until":  &leaseUntil,
			"heartbeat_at": &now,
			"updated_at":   now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrConflict
	}
	return nil
}

func leaseRenewableTaskStatuses() []string {
	return []string{model.AgentTaskStatusRunning, model.AgentTaskStatusPlanning}
}

// SavePlan replaces every non-planner step in one transaction and completes
// the sequence-zero planning step. Existing execution history is intentionally
// replaced only while the caller still owns the current RunVersion.
func (store *GormStore) SavePlan(ctx context.Context, taskID string, runVersion uint64, steps []model.AgentStep) error {
	return store.savePlan(ctx, taskID, runVersion, nil, steps)
}

func (store *GormStore) SavePlanWithSummary(ctx context.Context, taskID string, runVersion uint64, summary string, steps []model.AgentStep) error {
	return store.savePlan(ctx, taskID, runVersion, &summary, steps)
}

func (store *GormStore) savePlan(ctx context.Context, taskID string, runVersion uint64, summary *string, steps []model.AgentStep) error {
	db, err := store.database()
	if err != nil {
		return err
	}
	if strings.TrimSpace(taskID) == "" || runVersion == 0 {
		return ErrInvalidInput
	}
	now := store.currentTime()
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task model.AgentTask
		taskQuery := store.activeLeaseFencedTaskQuery(tx.Clauses(clause.Locking{Strength: "UPDATE"}), ctx, taskID, runVersion, now)
		if err := taskQuery.
			First(&task).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConflict
			}
			return err
		}
		if task.Status != model.AgentTaskStatusRunning && task.Status != model.AgentTaskStatusPlanning {
			return ErrConflict
		}
		if err := tx.Where("task_id = ? AND sequence > 0", taskID).Delete(&model.AgentStep{}).Error; err != nil {
			return err
		}
		planResult := tx.Model(&model.AgentStep{}).
			Where("task_id = ? AND sequence = ? AND kind = ?", taskID, 0, model.AgentStepKindPlan).
			Updates(map[string]any{
				"status":        model.AgentStepStatusSucceeded,
				"started_at":    gorm.Expr("COALESCE(started_at, ?)", now),
				"finished_at":   &now,
				"error_message": "",
				"updated_at":    now,
			})
		if planResult.Error != nil {
			return planResult.Error
		}
		if planResult.RowsAffected == 0 {
			return ErrConflict
		}

		normalized := normalizePlanSteps(task, steps)
		if len(normalized) > 0 {
			if err := tx.Create(&normalized).Error; err != nil {
				return err
			}
		}
		currentStepID := ""
		if len(normalized) > 0 {
			currentStepID = normalized[0].ID
		}
		taskUpdates := map[string]any{
			"status":          model.AgentTaskStatusRunning,
			"current_step_id": currentStepID,
			"error_message":   "",
			"updated_at":      now,
		}
		if summary != nil {
			taskUpdates["plan_summary"] = *summary
		}
		result := store.activeLeaseFencedTaskQuery(tx.Model(&model.AgentTask{}), ctx, taskID, runVersion, now).
			Updates(taskUpdates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrConflict
		}
		return nil
	})
}

func normalizePlanSteps(task model.AgentTask, steps []model.AgentStep) []model.AgentStep {
	normalized := make([]model.AgentStep, len(steps))
	for index := range steps {
		step := steps[index]
		if step.ID == "" {
			step.ID = uuid.NewString()
		}
		step.TaskID = task.ID
		step.UserName = task.UserName
		step.Sequence = index + 1
		if step.Kind == "" {
			step.Kind = model.AgentStepKindTool
		}
		step.Status = model.AgentStepStatusPending
		step.Attempts = 0
		step.ErrorMessage = ""
		step.ApprovalDecision = model.AgentApprovalDecisionPending
		step.ApprovalReason = ""
		step.ApprovalDecidedAt = nil
		step.MCPRequestID = ""
		step.OperationID = ""
		step.ToolOutputJSON = ""
		step.ResultSummary = ""
		step.StartedAt = nil
		step.FinishedAt = nil
		step.CreatedAt = time.Time{}
		step.UpdatedAt = time.Time{}
		normalized[index] = step
	}
	return normalized
}

func (store *GormStore) CreateStepsFenced(ctx context.Context, taskID string, runVersion uint64, steps []model.AgentStep) error {
	db, err := store.database()
	if err != nil {
		return err
	}
	if strings.TrimSpace(taskID) == "" || runVersion == 0 || len(steps) == 0 {
		return ErrInvalidInput
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task model.AgentTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND run_version = ?", taskID, runVersion).
			First(&task).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConflict
			}
			return err
		}
		var maximumSequence int
		if err := tx.Model(&model.AgentStep{}).Where("task_id = ?", taskID).
			Select("COALESCE(MAX(sequence), 0)").Scan(&maximumSequence).Error; err != nil {
			return err
		}
		normalized := normalizePlanSteps(task, steps)
		for index := range normalized {
			normalized[index].Sequence = maximumSequence + index + 1
		}
		return tx.Create(&normalized).Error
	})
}

func (store *GormStore) GetSteps(ctx context.Context, taskID string) ([]model.AgentStep, error) {
	db, err := store.database()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(taskID) == "" {
		return nil, ErrInvalidInput
	}
	var steps []model.AgentStep
	err = db.WithContext(ctx).Where("task_id = ?", taskID).
		Order("sequence ASC, created_at ASC, id ASC").Find(&steps).Error
	return steps, err
}

func (store *GormStore) GetOwnedSteps(ctx context.Context, userName, taskID string) ([]model.AgentStep, error) {
	db, err := store.database()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(userName) == "" || strings.TrimSpace(taskID) == "" {
		return nil, ErrInvalidInput
	}
	var steps []model.AgentStep
	err = db.WithContext(ctx).
		Where("task_id = ? AND user_name = ?", taskID, userName).
		Order("sequence ASC, created_at ASC, id ASC").
		Find(&steps).Error
	return steps, err
}

func (store *GormStore) UpdateTaskFenced(ctx context.Context, taskID string, runVersion uint64, updates map[string]any) error {
	db, err := store.database()
	if err != nil {
		return err
	}
	if strings.TrimSpace(taskID) == "" || runVersion == 0 || len(updates) == 0 {
		return ErrInvalidInput
	}
	updates = sanitizedUpdates(updates, immutableTaskColumns)
	if len(updates) == 0 {
		return ErrInvalidInput
	}
	now := store.currentTime()
	updates["updated_at"] = now
	result := store.activeLeaseFencedTaskQuery(db.WithContext(ctx).Model(&model.AgentTask{}), ctx, taskID, runVersion, now).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrConflict
	}
	return nil
}

func (store *GormStore) UpdateStepFenced(ctx context.Context, taskID, stepID string, runVersion uint64, updates map[string]any) error {
	db, err := store.database()
	if err != nil {
		return err
	}
	if strings.TrimSpace(taskID) == "" || strings.TrimSpace(stepID) == "" || runVersion == 0 || len(updates) == 0 {
		return ErrInvalidInput
	}
	updates = sanitizedUpdates(updates, immutableStepColumns)
	if len(updates) == 0 {
		return ErrInvalidInput
	}
	now := store.currentTime()
	updates["updated_at"] = now
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task model.AgentTask
		taskQuery := store.activeLeaseFencedTaskQuery(tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id"), ctx, taskID, runVersion, now)
		if err := taskQuery.
			First(&task).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConflict
			}
			return err
		}
		result := tx.Model(&model.AgentStep{}).
			Where("id = ? AND task_id = ?", stepID, taskID).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

// MarkExecutionUnknownFenced atomically records an ambiguous tool outcome and
// pauses the owning task for human review. RunVersion is incremented so the
// worker that observed the ambiguity cannot subsequently overwrite this state.
func (store *GormStore) MarkExecutionUnknownFenced(ctx context.Context, taskID, stepID string, runVersion uint64, message string) error {
	db, err := store.database()
	if err != nil {
		return err
	}
	if strings.TrimSpace(taskID) == "" || strings.TrimSpace(stepID) == "" || runVersion == 0 {
		return ErrInvalidInput
	}
	now := store.currentTime()
	message = truncateRunes(strings.TrimSpace(message), 1000)
	if message == "" {
		message = "tool execution outcome is unknown; manual review is required"
	}

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task model.AgentTask
		taskQuery := store.activeLeaseFencedTaskQuery(tx.Clauses(clause.Locking{Strength: "UPDATE"}), ctx, taskID, runVersion, now).
			Where("status = ?", model.AgentTaskStatusRunning)
		if err := taskQuery.
			First(&task).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConflict
			}
			return err
		}

		stepResult := tx.Model(&model.AgentStep{}).
			Where("id = ? AND task_id = ? AND kind = ? AND status = ?",
				stepID, taskID, model.AgentStepKindTool, model.AgentStepStatusRunning).
			Updates(map[string]any{
				"status":        model.AgentStepStatusExecutionUnknown,
				"finished_at":   &now,
				"error_message": message,
				"updated_at":    now,
			})
		if stepResult.Error != nil {
			return stepResult.Error
		}
		if stepResult.RowsAffected == 0 {
			return ErrConflict
		}

		taskResult := store.activeLeaseFencedTaskQuery(tx.Model(&model.AgentTask{}), ctx, taskID, runVersion, now).
			Where("status = ?", task.Status).
			Updates(map[string]any{
				"status":          model.AgentTaskStatusRequiresReview,
				"current_step_id": stepID,
				"run_version":     gorm.Expr("run_version + 1"),
				"lease_owner":     "",
				"lease_until":     nil,
				"heartbeat_at":    nil,
				"finished_at":     nil,
				"error_message":   message,
				"updated_at":      now,
			})
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected == 0 {
			return ErrConflict
		}
		return nil
	})
}

// MarkPolicyReviewFenced atomically replaces a stale planned policy snapshot
// with the currently trusted MCP definition and pauses execution. Resetting the
// durable approval decision guarantees that a newly approval-required tool
// passes through the approval gate again after explicit resume.
func (store *GormStore) MarkPolicyReviewFenced(
	ctx context.Context,
	taskID, stepID string,
	runVersion uint64,
	message, risk string,
	requiresApproval, readOnly, idempotent, destructive bool,
) error {
	db, err := store.database()
	if err != nil {
		return err
	}
	if strings.TrimSpace(taskID) == "" || strings.TrimSpace(stepID) == "" || runVersion == 0 {
		return ErrInvalidInput
	}
	now := store.currentTime()
	message = truncateRunes(strings.TrimSpace(message), 1000)
	if message == "" {
		message = "MCP tool safety policy changed; manual review is required"
	}
	risk = truncateRunes(strings.TrimSpace(risk), 16)

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task model.AgentTask
		taskQuery := store.activeLeaseFencedTaskQuery(tx.Clauses(clause.Locking{Strength: "UPDATE"}), ctx, taskID, runVersion, now).
			Where("status = ?", model.AgentTaskStatusRunning)
		if err := taskQuery.
			First(&task).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConflict
			}
			return err
		}

		stepResult := tx.Model(&model.AgentStep{}).
			Where("id = ? AND task_id = ? AND kind = ? AND status = ?",
				stepID, taskID, model.AgentStepKindTool, model.AgentStepStatusRunning).
			Updates(map[string]any{
				"status":              model.AgentStepStatusExecutionUnknown,
				"risk_level":          risk,
				"requires_approval":   requiresApproval,
				"read_only":           readOnly,
				"idempotent":          idempotent,
				"destructive":         destructive,
				"approval_decision":   model.AgentApprovalDecisionPending,
				"approval_reason":     "",
				"approval_decided_at": nil,
				"finished_at":         &now,
				"error_message":       message,
				"updated_at":          now,
			})
		if stepResult.Error != nil {
			return stepResult.Error
		}
		if stepResult.RowsAffected == 0 {
			return ErrConflict
		}

		taskResult := store.activeLeaseFencedTaskQuery(tx.Model(&model.AgentTask{}), ctx, taskID, runVersion, now).
			Where("status = ?", task.Status).
			Updates(map[string]any{
				"status":          model.AgentTaskStatusRequiresReview,
				"current_step_id": stepID,
				"run_version":     gorm.Expr("run_version + 1"),
				"lease_owner":     "",
				"lease_until":     nil,
				"heartbeat_at":    nil,
				"finished_at":     nil,
				"error_message":   message,
				"updated_at":      now,
			})
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected == 0 {
			return ErrConflict
		}
		return nil
	})
}

func (store *GormStore) UpdateOwnedTask(ctx context.Context, userName, taskID string, updates map[string]any) error {
	db, err := store.database()
	if err != nil {
		return err
	}
	if strings.TrimSpace(userName) == "" || strings.TrimSpace(taskID) == "" || len(updates) == 0 {
		return ErrInvalidInput
	}
	updates = sanitizedUpdates(updates, immutableTaskColumns)
	if len(updates) == 0 {
		return ErrInvalidInput
	}
	updates["updated_at"] = store.currentTime()
	result := db.WithContext(ctx).Model(&model.AgentTask{}).
		Where("id = ? AND user_name = ?", taskID, userName).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// UpdateOwnedStepDecision is the durable human-in-the-loop decision. It does
// not create or persist an MCP approval token. The worker creates and consumes
// that short-lived token only immediately before the approved invocation.
//
// expectedDigest binds an approval to the exact arguments the human reviewed.
// It is mandatory for approve and is folded into the same CAS predicate as the
// status check, so a replayed or stale approval cannot authorize a payload the
// reviewer never saw. Reject deliberately does not require it: stopping a task
// must stay possible from a stale UI.
func (store *GormStore) UpdateOwnedStepDecision(ctx context.Context, userName, taskID, stepID, decision, reason, expectedDigest string) (*model.AgentStep, error) {
	db, err := store.database()
	if err != nil {
		return nil, err
	}
	userName = strings.TrimSpace(userName)
	decision = strings.TrimSpace(strings.ToLower(decision))
	// Digests are always written as lower-case hex, so a normalized equality
	// predicate stays index-friendly instead of forcing LOWER() on the column.
	expectedDigest = strings.ToLower(strings.TrimSpace(expectedDigest))
	if userName == "" || strings.TrimSpace(taskID) == "" || strings.TrimSpace(stepID) == "" ||
		(decision != model.AgentApprovalDecisionApproved && decision != model.AgentApprovalDecisionRejected) {
		return nil, ErrInvalidInput
	}
	if decision == model.AgentApprovalDecisionApproved && expectedDigest == "" {
		return nil, ErrInvalidInput
	}
	now := store.currentTime()
	reason = truncateRunes(strings.TrimSpace(reason), maxApprovalReasonRunes)
	var decided model.AgentStep
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task model.AgentTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_name = ?", taskID, userName).
			First(&task).Error; err != nil {
			return err
		}
		if task.Status != model.AgentTaskStatusWaitingApproval {
			return ErrConflict
		}

		stepStatus := model.AgentStepStatusApproved
		taskStatus := model.AgentTaskStatusPending
		var finishedAt any
		taskError := ""
		if decision == model.AgentApprovalDecisionRejected {
			stepStatus = model.AgentStepStatusRejected
			taskStatus = model.AgentTaskStatusRejected
			finishedAt = &now
			taskError = "tool execution was rejected"
		}
		stepQuery := tx.Model(&model.AgentStep{}).
			Where("id = ? AND task_id = ? AND user_name = ? AND status = ? AND approval_decision IN ?",
				stepID, taskID, userName, model.AgentStepStatusWaitingApproval,
				[]string{"", model.AgentApprovalDecisionPending})
		// Keyed on the decision rather than on a non-empty digest so the binding
		// fails closed: an approve that somehow lost its digest matches no row
		// instead of silently dropping the predicate from the UPDATE.
		if decision == model.AgentApprovalDecisionApproved {
			stepQuery = stepQuery.Where("arguments_digest = ?", expectedDigest)
		}
		stepResult := stepQuery.
			Updates(map[string]any{
				"status":              stepStatus,
				"approval_decision":   decision,
				"approval_reason":     reason,
				"approval_decided_at": &now,
				"finished_at":         finishedAt,
				"updated_at":          now,
			})
		if stepResult.Error != nil {
			return stepResult.Error
		}
		if stepResult.RowsAffected == 0 {
			return ErrConflict
		}

		taskUpdates := map[string]any{
			"status":          taskStatus,
			"current_step_id": stepID,
			"lease_owner":     "",
			"lease_until":     nil,
			"heartbeat_at":    nil,
			"finished_at":     finishedAt,
			"error_message":   taskError,
			"updated_at":      now,
			// Approve was the only transition that resumed a task without
			// invalidating its checkpoint, which is why a graph upgrade could
			// strand tasks parked here. Dropping the cursor makes this path
			// identical to stale recovery and resume: the next claim starts a
			// fresh run and nextRoute skips whatever MySQL records as done.
			"checkpoint":         nil,
			"checkpoint_version": gorm.Expr("checkpoint_version + 1"),
		}
		if decisionInvalidatesRun(decision) {
			taskUpdates["run_version"] = gorm.Expr("run_version + 1")
		}
		taskResult := tx.Model(&model.AgentTask{}).
			Where("id = ? AND user_name = ? AND status = ?", taskID, userName, model.AgentTaskStatusWaitingApproval).
			Updates(taskUpdates)
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected == 0 {
			return ErrConflict
		}
		return tx.Where("id = ? AND task_id = ? AND user_name = ?", stepID, taskID, userName).First(&decided).Error
	})
	if err != nil {
		return nil, err
	}
	return &decided, nil
}

func decisionInvalidatesRun(decision string) bool {
	return decision == model.AgentApprovalDecisionRejected
}

// ResumeOwnedTask requeues a failed task. A step whose outcome is unknown is
// reset only after the caller explicitly opts into retryUnknown; this is the
// user-visible safety boundary for replaying a potentially side-effecting
// operation. The old Eino blob is discarded because MySQL task/step rows are
// the source of truth; a fresh graph run can therefore recover from a corrupt
// checkpoint or an incompatible graph serialization after an upgrade.
func (store *GormStore) ResumeOwnedTask(ctx context.Context, userName, taskID string, retryUnknown bool) error {
	db, err := store.database()
	if err != nil {
		return err
	}
	userName = strings.TrimSpace(userName)
	if userName == "" || strings.TrimSpace(taskID) == "" {
		return ErrInvalidInput
	}
	now := store.currentTime()
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task model.AgentTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_name = ?", taskID, userName).
			First(&task).Error; err != nil {
			return err
		}
		if !taskCanResume(task.Status, retryUnknown) {
			return ErrConflict
		}

		stepStatuses := []string{model.AgentStepStatusFailed}
		if retryUnknown {
			stepStatuses = append(stepStatuses, model.AgentStepStatusExecutionUnknown)
		}
		stepQuery := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("task_id = ? AND user_name = ? AND status IN ?", taskID, userName, stepStatuses)
		if task.CurrentStepID != "" {
			stepQuery = stepQuery.Where("id = ?", task.CurrentStepID)
		}
		var step model.AgentStep
		if err := stepQuery.Order("sequence ASC, id ASC").First(&step).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConflict
			}
			return err
		}
		if !stepCanResume(step.Status, retryUnknown) {
			return ErrConflict
		}

		stepResult := tx.Model(&model.AgentStep{}).
			Where("id = ? AND task_id = ? AND user_name = ? AND status = ?", step.ID, taskID, userName, step.Status).
			Updates(map[string]any{
				"status":        model.AgentStepStatusPending,
				"started_at":    nil,
				"finished_at":   nil,
				"error_message": "",
				"updated_at":    now,
			})
		if stepResult.Error != nil {
			return stepResult.Error
		}
		if stepResult.RowsAffected == 0 {
			return ErrConflict
		}

		taskResult := tx.Model(&model.AgentTask{}).
			Where("id = ? AND user_name = ? AND status = ? AND run_version = ?", taskID, userName, task.Status, task.RunVersion).
			Updates(map[string]any{
				"status":             model.AgentTaskStatusPending,
				"current_step_id":    step.ID,
				"run_version":        gorm.Expr("run_version + 1"),
				"checkpoint":         nil,
				"checkpoint_version": gorm.Expr("checkpoint_version + 1"),
				"lease_owner":        "",
				"lease_until":        nil,
				"heartbeat_at":       nil,
				"finished_at":        nil,
				"error_message":      "",
				"updated_at":         now,
			})
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected == 0 {
			return ErrConflict
		}
		return nil
	})
}

func taskCanResume(status string, retryUnknown bool) bool {
	return status == model.AgentTaskStatusFailed ||
		(status == model.AgentTaskStatusRequiresReview && retryUnknown)
}

func stepCanResume(status string, retryUnknown bool) bool {
	return status == model.AgentStepStatusFailed ||
		(status == model.AgentStepStatusExecutionUnknown && retryUnknown)
}

// CancelOwnedTask is a fencing transition, not a plain status update. Bumping
// RunVersion prevents an already-running graph from committing a later success
// or checkpoint after the user has cancelled the task.
func (store *GormStore) CancelOwnedTask(ctx context.Context, userName, taskID string) error {
	db, err := store.database()
	if err != nil {
		return err
	}
	userName = strings.TrimSpace(userName)
	if userName == "" || strings.TrimSpace(taskID) == "" {
		return ErrInvalidInput
	}
	now := store.currentTime()
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task model.AgentTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_name = ?", taskID, userName).
			First(&task).Error; err != nil {
			return err
		}
		switch task.Status {
		case model.AgentTaskStatusCancelled:
			return nil
		case model.AgentTaskStatusSucceeded, model.AgentTaskStatusRejected:
			return ErrConflict
		}

		var activeSteps []model.AgentStep
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("task_id = ? AND user_name = ? AND status IN ?", taskID, userName, []string{
				model.AgentStepStatusPending,
				model.AgentStepStatusRunning,
				model.AgentStepStatusWaitingApproval,
				model.AgentStepStatusApproved,
			}).Find(&activeSteps).Error; err != nil {
			return err
		}
		uncertainExecution := false
		for index := range activeSteps {
			step := activeSteps[index]
			status := model.AgentStepStatusSkipped
			errorMessage := ""
			if step.Status == model.AgentStepStatusRunning && !stepCanReplayAfterCrash(step) {
				status = model.AgentStepStatusExecutionUnknown
				errorMessage = "tool execution outcome is unknown after cancellation"
				uncertainExecution = true
			}
			if err := tx.Model(&model.AgentStep{}).
				Where("id = ? AND task_id = ? AND user_name = ? AND status = ?", step.ID, taskID, userName, step.Status).
				Updates(map[string]any{
					"status":        status,
					"finished_at":   &now,
					"error_message": errorMessage,
					"updated_at":    now,
				}).Error; err != nil {
				return err
			}
		}

		taskError := ""
		if uncertainExecution {
			taskError = "task was cancelled while a non-idempotent tool was running; its outcome may be unknown"
		}
		result := tx.Model(&model.AgentTask{}).
			Where("id = ? AND user_name = ? AND status = ? AND run_version = ?", taskID, userName, task.Status, task.RunVersion).
			Updates(map[string]any{
				"status":        model.AgentTaskStatusCancelled,
				"run_version":   gorm.Expr("run_version + 1"),
				"lease_owner":   "",
				"lease_until":   nil,
				"heartbeat_at":  nil,
				"finished_at":   &now,
				"error_message": taskError,
				"updated_at":    now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrConflict
		}
		return nil
	})
}

// RecoverStaleTasks invalidates the abandoned RunVersion. Planner, finalizer,
// read-only, and explicitly idempotent steps can be replayed. A possibly sent
// non-idempotent tool call is never replayed automatically because its side
// effect may already have happened upstream.
func (store *GormStore) RecoverStaleTasks(ctx context.Context, before time.Time) error {
	db, err := store.database()
	if err != nil {
		return err
	}
	if before.IsZero() {
		return ErrInvalidInput
	}
	now := store.currentTime()
	var firstErr error

	// Batches are drained in rounds rather than one per call: a fleet restart can
	// abandon far more than one batch of tasks, and the caller has no way to ask
	// for more. The round cap keeps a single poll's work bounded, and a task that
	// keeps failing to recover cannot spin here forever.
	for round := 0; round < recoverStaleMaxRounds; round++ {
		// Candidates are scanned without row locks so a large backlog cannot turn
		// recovery into one long transaction holding a lock on every stale row.
		var candidates []model.AgentTask
		if err := staleTaskQuery(db.WithContext(ctx).Select("id"), now, before).
			Order("updated_at ASC, id ASC").
			Limit(recoverStaleBatchSize).
			Find(&candidates).Error; err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return firstErr
		}
		if len(candidates) == 0 {
			return firstErr
		}
		recovered, err := store.recoverCandidates(ctx, db, candidates, now, before, &firstErr)
		if err != nil {
			return err
		}
		// Forward progress, not batch fullness, decides whether to scan again.
		// A batch that recovers nothing returns the identical candidate list on
		// the next scan, so without this a poison row would burn every round and
		// starve the pending-task claim that follows recovery in the same poll.
		if recovered == 0 || len(candidates) < recoverStaleBatchSize {
			return firstErr
		}
	}
	return firstErr
}

// recoverCandidates recovers one scanned batch and reports how many tasks it
// actually transitioned. A per-task error is recorded in firstErr and the batch
// continues; only a cancelled context aborts the round.
func (store *GormStore) recoverCandidates(
	ctx context.Context,
	db *gorm.DB,
	candidates []model.AgentTask,
	now, before time.Time,
	firstErr *error,
) (int, error) {
	recovered := 0
	for index := range candidates {
		if ctx.Err() != nil {
			// Shutdown is not a recovery failure, so it is reported on its own
			// rather than conflated with whatever firstErr happens to hold.
			return recovered, ctx.Err()
		}
		// One transaction per task: a row that another worker already claimed,
		// or that fails to recover, must not roll back the rest of the batch.
		transitioned := false
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var task model.AgentTask
			// Staleness is re-checked under the row lock because the scan above
			// held none; the task may have been claimed or recovered since.
			// Projected down to the columns recoverLockedTask reads: the row lock
			// is taken regardless, and a stale task's checkpoint is the largest
			// blob in the schema — one this transaction is about to NULL anyway.
			// SKIP LOCKED: every worker scans the same deterministic candidate
			// list, so without it N-1 workers serialize behind each row lock for
			// no recoveries. A locked row is already being recovered by someone.
			locked := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
				Select("id", "status", "run_version", "current_step_id")
			if err := staleTaskQuery(locked, now, before).
				Where("id = ?", candidates[index].ID).
				First(&task).Error; err != nil {
				// Not found here means already claimed, already recovered, or
				// locked by a peer thanks to SKIP LOCKED — all benign.
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			if err := store.recoverLockedTask(tx, task, now); err != nil {
				return err
			}
			transitioned = true
			return nil
		})
		// ErrConflict just means another worker won the race, which is the
		// expected outcome of concurrent recovery rather than a failure.
		if err != nil && !errors.Is(err, ErrConflict) && *firstErr == nil {
			*firstErr = err
		}
		if err == nil && transitioned {
			recovered++
		}
	}
	return recovered, nil
}

func staleTaskQuery(query *gorm.DB, now, before time.Time) *gorm.DB {
	return query.Model(&model.AgentTask{}).
		Where("status IN ?", staleRecoverableTaskStatuses()).
		Where("(lease_until IS NOT NULL AND lease_until < ?) OR (lease_until IS NULL AND COALESCE(heartbeat_at, started_at, updated_at) < ?)", now, before.UTC())
}

func (store *GormStore) recoverLockedTask(tx *gorm.DB, task model.AgentTask, now time.Time) error {
	var runningSteps []model.AgentStep
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("task_id = ? AND status = ?", task.ID, model.AgentStepStatusRunning).
		Order("sequence ASC, id ASC").Find(&runningSteps).Error; err != nil {
		return err
	}

	taskStatus := model.AgentTaskStatusPending
	taskError := ""
	currentStepID := task.CurrentStepID
	unsafeFound := false
	for index := range runningSteps {
		step := runningSteps[index]
		if currentStepID == "" {
			currentStepID = step.ID
		}
		if stepCanReplayAfterCrash(step) {
			if err := tx.Model(&model.AgentStep{}).Where("id = ? AND task_id = ? AND status = ?", step.ID, task.ID, model.AgentStepStatusRunning).
				Updates(map[string]any{
					"status":        model.AgentStepStatusPending,
					"started_at":    nil,
					"finished_at":   nil,
					"error_message": "",
					"updated_at":    now,
				}).Error; err != nil {
				return err
			}
			continue
		}

		unsafeFound = true
		currentStepID = step.ID
		if err := tx.Model(&model.AgentStep{}).Where("id = ? AND task_id = ? AND status = ?", step.ID, task.ID, model.AgentStepStatusRunning).
			Updates(map[string]any{
				"status":        model.AgentStepStatusExecutionUnknown,
				"finished_at":   &now,
				"error_message": "tool execution outcome is unknown after worker interruption",
				"updated_at":    now,
			}).Error; err != nil {
			return err
		}
	}
	if unsafeFound {
		taskStatus = model.AgentTaskStatusRequiresReview
		taskError = "a non-idempotent tool may have executed; manual review is required"
	}

	result := tx.Model(&model.AgentTask{}).
		Where("id = ? AND status = ? AND run_version = ?", task.ID, task.Status, task.RunVersion).
		Updates(map[string]any{
			"status":          taskStatus,
			"current_step_id": currentStepID,
			"run_version":     gorm.Expr("run_version + 1"),
			// Recovery has already normalized every running step from the
			// durable MySQL state. Keeping the abandoned Eino cursor here would
			// create a second, stale source of truth and can also fail after a
			// graph upgrade. The next claim therefore starts a fresh graph run;
			// nextRoute skips steps that MySQL records as completed.
			"checkpoint":         nil,
			"checkpoint_version": gorm.Expr("checkpoint_version + 1"),
			"lease_owner":        "",
			"lease_until":        nil,
			"heartbeat_at":       nil,
			"error_message":      taskError,
			"updated_at":         now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrConflict
	}
	return nil
}

func staleRecoverableTaskStatuses() []string {
	return []string{model.AgentTaskStatusRunning, model.AgentTaskStatusPlanning}
}

func stepCanReplayAfterCrash(step model.AgentStep) bool {
	if step.Kind == model.AgentStepKindPlan || step.Kind == model.AgentStepKindFinal {
		return true
	}
	return !step.Destructive && (step.ReadOnly || step.Idempotent)
}

type fencingToken struct {
	taskID     string
	runVersion uint64
	workerID   string
}

type fencingTokenContextKey struct{}

// WithFencingToken binds checkpoint writes to a claimed task generation.
func WithFencingToken(ctx context.Context, taskID string, runVersion uint64) context.Context {
	return WithWorkerFencingToken(ctx, taskID, runVersion, "")
}

// WithWorkerFencingToken records the worker that owns an active lease as well
// as the monotonically increasing run version. Store writes made with this
// token must prove both ownership and that the lease has not expired.
func WithWorkerFencingToken(ctx context.Context, taskID string, runVersion uint64, workerID string) context.Context {
	return context.WithValue(ctx, fencingTokenContextKey{}, fencingToken{
		taskID:     taskID,
		runVersion: runVersion,
		workerID:   strings.TrimSpace(workerID),
	})
}

func FencingTokenFromContext(ctx context.Context) (taskID string, runVersion uint64, ok bool) {
	if ctx == nil {
		return "", 0, false
	}
	token, ok := ctx.Value(fencingTokenContextKey{}).(fencingToken)
	if !ok || token.taskID == "" || token.runVersion == 0 {
		return "", 0, false
	}
	return token.taskID, token.runVersion, true
}

// WorkerFencingTokenFromContext returns the optional lease owner carried by a
// fencing token. Tokens created by older callers intentionally return an
// empty worker ID and retain run-version-only behavior for compatibility.
func WorkerFencingTokenFromContext(ctx context.Context) (taskID string, runVersion uint64, workerID string, ok bool) {
	if ctx == nil {
		return "", 0, "", false
	}
	token, ok := ctx.Value(fencingTokenContextKey{}).(fencingToken)
	if !ok || token.taskID == "" || token.runVersion == 0 {
		return "", 0, "", false
	}
	return token.taskID, token.runVersion, token.workerID, true
}

// activeLeaseFencedTaskQuery applies the normal RunVersion fence and, when a
// worker-aware token is present, requires that worker's lease to still be
// valid. This closes the interval after a lease expires but before stale-task
// recovery has incremented RunVersion. Human transitions do not carry a
// worker token and deliberately use their own transactional preconditions.
func (store *GormStore) activeLeaseFencedTaskQuery(query *gorm.DB, ctx context.Context, taskID string, runVersion uint64, now time.Time) *gorm.DB {
	query = query.Where("id = ? AND run_version = ?", taskID, runVersion)
	workerID, active := activeLeaseFenceFromContext(ctx, taskID, runVersion)
	if !active {
		return query
	}
	return query.Where("lease_owner = ? AND lease_until > ?", workerID, now)
}

func activeLeaseFenceFromContext(ctx context.Context, taskID string, runVersion uint64) (workerID string, active bool) {
	tokenTaskID, tokenRunVersion, workerID, ok := WorkerFencingTokenFromContext(ctx)
	if !ok || tokenTaskID != taskID || tokenRunVersion != runVersion || workerID == "" {
		return "", false
	}
	return workerID, true
}

// CheckpointSchemaVersion identifies the compiled graph that produced a stored
// Eino blob. Bump it whenever service/agent/graph.go changes its node set,
// edges, interrupt points or GraphState shape. Blobs carrying any other version
// are reported as absent rather than handed to a graph that cannot decode them.
const CheckpointSchemaVersion = "gopherai_task_agent_v1"

var checkpointEnvelopePrefix = []byte(CheckpointSchemaVersion + "\n")

// encodeCheckpointEnvelope tags a raw Eino blob with the schema that wrote it.
// An empty checkpoint stays empty: wrapping it would make GetCheckpoint report
// a present-but-empty blob, which suppresses WithForceNewRun and hands Eino
// nothing to deserialize. "No checkpoint" must keep round-tripping as absent.
func encodeCheckpointEnvelope(checkpoint []byte) []byte {
	if len(checkpoint) == 0 {
		return nil
	}
	envelope := make([]byte, 0, len(checkpointEnvelopePrefix)+len(checkpoint))
	envelope = append(envelope, checkpointEnvelopePrefix...)
	return append(envelope, checkpoint...)
}

// decodeCheckpointEnvelope returns the raw Eino blob only when it was written
// by the current schema. Legacy rows stored before envelopes existed have no
// prefix and are therefore correctly reported as undecodable. The payload
// aliases stored, whose only caller owns it for the length of the call.
func decodeCheckpointEnvelope(stored []byte) ([]byte, bool) {
	if !bytes.HasPrefix(stored, checkpointEnvelopePrefix) {
		return nil, false
	}
	payload := stored[len(checkpointEnvelopePrefix):]
	if len(payload) == 0 {
		return nil, false
	}
	return payload, true
}

func (store *GormStore) GetCheckpoint(ctx context.Context, taskID string) ([]byte, bool, error) {
	db, err := store.database()
	if err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(taskID) == "" {
		return nil, false, ErrInvalidInput
	}
	var task model.AgentTask
	if err := db.WithContext(ctx).Select("id", "checkpoint").Where("id = ?", taskID).First(&task).Error; err != nil {
		return nil, false, err
	}
	if len(task.Checkpoint) == 0 {
		return nil, false, nil
	}
	checkpoint, decodable := decodeCheckpointEnvelope(task.Checkpoint)
	if !decodable {
		// Written by a different graph version, so the current graph cannot
		// resume from it. Reporting it as absent makes the worker start a fresh
		// run instead of failing the task on a deserialization error. This is
		// safe because MySQL step rows stay authoritative and already skip work
		// that completed durably.
		return nil, false, nil
	}
	return checkpoint, true, nil
}

func (store *GormStore) SetCheckpoint(ctx context.Context, taskID string, checkpoint []byte) error {
	db, err := store.database()
	if err != nil {
		return err
	}
	if strings.TrimSpace(taskID) == "" {
		return ErrInvalidInput
	}
	query := db.WithContext(ctx).Model(&model.AgentTask{}).Where("id = ?", taskID)
	fenced := false
	if fencedTaskID, runVersion, ok := FencingTokenFromContext(ctx); ok {
		if fencedTaskID != taskID {
			return ErrConflict
		}
		query = query.Where("run_version = ?", runVersion)
		fenced = true
	}
	// Checkpoints are normally written while the worker owns an active lease.
	// The approval node is the narrow exception: it intentionally releases its
	// lease before Eino persists the approval interrupt checkpoint. RunVersion
	// remains the fence here so a later claim, cancellation, or recovery still
	// rejects a stale checkpoint write.
	result := query.Updates(map[string]any{
		"checkpoint":         encodeCheckpointEnvelope(checkpoint),
		"checkpoint_version": gorm.Expr("checkpoint_version + 1"),
		"updated_at":         store.currentTime(),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		if fenced {
			return ErrConflict
		}
		return gorm.ErrRecordNotFound
	}
	return nil
}

// Get and Set make GormStore directly usable as compose.CheckPointStore.
func (store *GormStore) Get(ctx context.Context, checkPointID string) ([]byte, bool, error) {
	return store.GetCheckpoint(ctx, checkPointID)
}

func (store *GormStore) Set(ctx context.Context, checkPointID string, checkpoint []byte) error {
	return store.SetCheckpoint(ctx, checkPointID, checkpoint)
}

var immutableTaskColumns = map[string]struct{}{
	"id": {}, "user_name": {}, "username": {}, "created_at": {}, "createdat": {},
	"run_version": {}, "runversion": {}, "checkpoint": {}, "checkpoint_version": {}, "checkpointversion": {},
}

// immutableStepColumns also fences the reviewed payload and the trusted policy
// snapshot. UpdateStepFenced takes a caller-supplied map, so without this the
// approval digest binding would hold only because no current caller happens to
// pass those keys. savePlan writes them at creation and MarkPolicyReviewFenced
// replaces them through its own statement; neither needs the generic path.
var immutableStepColumns = map[string]struct{}{
	"id": {}, "task_id": {}, "taskid": {}, "user_name": {}, "username": {},
	"sequence": {}, "created_at": {}, "createdat": {},
	"tool_name": {}, "toolname": {},
	"tool_arguments_json": {}, "toolargumentsjson": {},
	"arguments_digest": {}, "argumentsdigest": {},
	"risk_level": {}, "risklevel": {},
	"requires_approval": {}, "requiresapproval": {},
	"read_only": {}, "readonly": {},
	"idempotent": {}, "destructive": {},
	"approval_decision": {}, "approvaldecision": {},
}

func sanitizedUpdates(source map[string]any, immutable map[string]struct{}) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if normalized == "" {
			continue
		}
		if _, denied := immutable[normalized]; denied {
			continue
		}
		result[key] = value
	}
	return result
}

func truncateRunes(value string, maximum int) string {
	if maximum <= 0 || value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
}

func (store *GormStore) String() string {
	if store == nil || store.db == nil {
		return "GormStore(uninitialized)"
	}
	return fmt.Sprintf("GormStore(%p)", store.db)
}
