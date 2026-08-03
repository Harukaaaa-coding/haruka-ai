package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	agentdao "GopherAI/dao/agent"
	"GopherAI/model"

	"github.com/cloudwego/eino/compose"
)

// StartWorker starts the durable pending-task worker and returns immediately.
// The supplied context controls its lifetime.
func (service *Service) StartWorker(ctx context.Context) error {
	if service == nil || service.runner == nil {
		return fmt.Errorf("%w: Agent service is not initialized", ErrInvalidInput)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	service.workerMu.Lock()
	if service.workerStarted {
		service.workerMu.Unlock()
		return nil
	}
	service.workerStarted = true
	service.workerDone = make(chan struct{})
	done := service.workerDone
	service.workerMu.Unlock()
	go func() {
		defer func() {
			service.workerMu.Lock()
			service.workerStarted = false
			close(done)
			service.workerMu.Unlock()
		}()
		service.workerLoop(ctx)
	}()
	return nil
}

func (service *Service) WorkerRunning() bool {
	if service == nil {
		return false
	}
	service.workerMu.Lock()
	defer service.workerMu.Unlock()
	return service.workerStarted
}

func (service *Service) WaitWorker(ctx context.Context) error {
	if service == nil {
		return nil
	}
	service.workerMu.Lock()
	done := service.workerDone
	service.workerMu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (service *Service) workerLoop(ctx context.Context) {
	service.pollPending(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case taskID := <-service.hints:
			service.processTaskID(ctx, taskID)
		case <-service.clock.After(service.workerOptions.PollInterval):
			service.pollPending(ctx)
		}
	}
}

func (service *Service) pollPending(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	_ = service.store.RecoverStaleTasks(ctx, service.clock.Now().Add(-service.workerOptions.StaleAfter))
	tasks, err := service.store.ListPendingTasks(ctx, service.workerOptions.BatchSize)
	if err != nil {
		return
	}
	for index := range tasks {
		if ctx.Err() != nil {
			return
		}
		service.processTaskID(ctx, tasks[index].ID)
	}
}

// ProcessTask is useful for deterministic tests and operational recovery. It
// still claims the task, so concurrent workers cannot execute it twice.
func (service *Service) ProcessTask(ctx context.Context, taskID string) error {
	return service.processTaskID(ctx, taskID)
}

func (service *Service) processTaskID(parent context.Context, taskID string) error {
	if parent == nil {
		parent = context.Background()
	}
	task, claimed, err := service.store.ClaimTask(parent, taskID, service.workerOptions.ID, service.workerOptions.Lease)
	if err != nil {
		return service.normalizeStoreError(err)
	}
	if !claimed || task == nil {
		return nil
	}

	ctx, cancel := context.WithCancel(parent)
	service.registerRunning(task.ID, cancel)
	stopHeartbeat := service.startLeaseHeartbeat(ctx, cancel, task)
	defer func() {
		cancel()
		stopHeartbeat()
		service.unregisterRunning(task.ID)
	}()
	ctx = withRunVersion(ctx, task.RunVersion)
	ctx = agentdao.WithFencingToken(ctx, task.ID, task.RunVersion)

	err = service.runClaimedTask(ctx, task)
	if err == nil || errors.Is(err, ErrApprovalPending) || errors.Is(err, ErrTaskStopped) ||
		errors.Is(err, context.Canceled) || errors.Is(err, ErrExecutionNeedsReview) {
		return nil
	}
	service.markRunFailed(ctx, task, err)
	return err
}

func (service *Service) startLeaseHeartbeat(ctx context.Context, cancel context.CancelFunc, task *model.AgentTask) func() {
	heartbeatCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	interval := service.workerOptions.Lease / 3
	if interval <= 0 {
		interval = time.Second
	}
	go func() {
		defer close(done)
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-service.clock.After(interval):
			}

			err := service.store.RenewLeaseFenced(
				heartbeatCtx,
				task.ID,
				task.RunVersion,
				service.workerOptions.ID,
				service.workerOptions.Lease,
			)
			if err == nil {
				continue
			}

			// The approval node deliberately releases its lease before Eino
			// persists the dynamic-interrupt checkpoint. Do not cancel that
			// narrow checkpoint window. Terminal states need no heartbeat either.
			current, readErr := service.store.GetTaskByID(heartbeatCtx, task.ID)
			if readErr == nil && current.RunVersion == task.RunVersion {
				switch current.Status {
				case model.AgentTaskStatusWaitingApproval,
					model.AgentTaskStatusSucceeded,
					model.AgentTaskStatusFailed,
					model.AgentTaskStatusRejected,
					model.AgentTaskStatusCancelled:
					return
				case model.AgentTaskStatusPlanning, model.AgentTaskStatusRunning:
					// A transient renew error while ownership is still current is
					// retried on the next interval.
					continue
				}
			}

			// Ownership or RunVersion changed. Cancel expensive upstream work;
			// fencing still prevents every stale DB/checkpoint write.
			cancel()
			return
		}
	}()
	return func() {
		stop()
		<-done
	}
}

func (service *Service) runClaimedTask(ctx context.Context, task *model.AgentTask) error {
	_, checkpointExists, err := service.store.Get(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("load Agent checkpoint: %w", err)
	}
	options := []compose.Option{compose.WithCheckPointID(task.ID)}
	if !checkpointExists {
		options = append(options, compose.WithForceNewRun())
	}

	input := task.ID
	for automaticResumes := 0; automaticResumes <= service.maxRunSteps; automaticResumes++ {
		_, runErr := service.runner.Invoke(ctx, input, options...)
		if runErr == nil {
			return nil
		}
		info, interrupted := compose.ExtractInterruptInfo(runErr)
		if !interrupted {
			return runErr
		}
		if approvalFromInterrupt(info) != nil {
			return ErrApprovalPending
		}
		if len(info.AfterNodes) == 0 {
			return runErr
		}

		// A static plan/tool interrupt is an automatic durable boundary. Reload
		// the just-written checkpoint with the same ID and continue immediately.
		input = ""
		options = []compose.Option{compose.WithCheckPointID(task.ID)}
	}
	return fmt.Errorf("Agent graph exceeded automatic checkpoint resume limit")
}

func approvalFromInterrupt(info *compose.InterruptInfo) *ApprovalInterrupt {
	if info == nil {
		return nil
	}
	if extra, exists := info.RerunNodesExtra[nodeApprovalGate]; exists {
		switch typed := extra.(type) {
		case *ApprovalInterrupt:
			return typed
		case ApprovalInterrupt:
			copy := typed
			return &copy
		}
	}
	for _, child := range info.SubGraphs {
		if approval := approvalFromInterrupt(child); approval != nil {
			return approval
		}
	}
	return nil
}

func (service *Service) markRunFailed(ctx context.Context, claimed *model.AgentTask, cause error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	message := truncateRunes(redactText(cause.Error()), 1000)
	now := service.clock.Now()
	current, err := service.store.GetTaskByID(cleanupCtx, claimed.ID)
	if err != nil || current.RunVersion != claimed.RunVersion {
		return
	}
	switch current.Status {
	case model.AgentTaskStatusWaitingApproval, model.AgentTaskStatusRequiresReview,
		model.AgentTaskStatusSucceeded, model.AgentTaskStatusRejected, model.AgentTaskStatusCancelled:
		return
	}

	step := currentFailureStep(current)
	taskStatus := model.AgentTaskStatusFailed
	stepStatus := model.AgentStepStatusFailed
	var finishedAt any = &now
	if step != nil && step.Kind == model.AgentStepKindTool && step.Status == model.AgentStepStatusRunning &&
		(step.Destructive || (!step.ReadOnly && !step.Idempotent)) {
		// Once a non-replayable tool has started, cancellation/persistence
		// failure cannot prove whether its side effect happened. Never expose
		// that state as an ordinary retryable failure.
		_ = service.store.MarkExecutionUnknownFenced(cleanupCtx, current.ID, step.ID, claimed.RunVersion, message)
		// On an incidental database failure, keep the task fenced/running. The
		// stale-lease recovery path performs the same atomic unknown transition;
		// never downgrade an uncertain side effect to ordinary failed/retryable.
		return
	}
	currentStepID := current.CurrentStepID
	if step != nil {
		currentStepID = step.ID
		if step.Status != model.AgentStepStatusFailed && step.Status != model.AgentStepStatusExecutionUnknown {
			_ = service.store.UpdateStepFenced(cleanupCtx, current.ID, step.ID, claimed.RunVersion, map[string]any{
				"status":        stepStatus,
				"finished_at":   &now,
				"error_message": message,
			})
		}
	}
	_ = service.store.UpdateTaskFenced(cleanupCtx, claimed.ID, claimed.RunVersion, map[string]any{
		"status":          taskStatus,
		"current_step_id": currentStepID,
		"finished_at":     finishedAt,
		"error_message":   message,
		"lease_owner":     "",
		"lease_until":     nil,
		"heartbeat_at":    nil,
	})
}

func currentFailureStep(task *model.AgentTask) *model.AgentStep {
	if task == nil {
		return nil
	}
	for index := range task.Steps {
		step := &task.Steps[index]
		if step.ID == task.CurrentStepID && !stepFinished(step.Status) {
			copy := *step
			return &copy
		}
	}
	// A checkpoint write can fail after a node durably completed and cleared
	// CurrentStepID. Mark the next pending step so ResumeOwnedTask has a safe,
	// concrete point from which to continue.
	for index := range task.Steps {
		if !stepFinished(task.Steps[index].Status) {
			copy := task.Steps[index]
			return &copy
		}
	}
	// The final model step may have been saved as succeeded just before the
	// task-row update failed. Re-running a finalizer is side-effect free and
	// gives ResumeOwnedTask a concrete recovery point.
	for index := range task.Steps {
		if task.Steps[index].Kind == model.AgentStepKindFinal {
			copy := task.Steps[index]
			return &copy
		}
	}
	return nil
}

func stepFinished(status string) bool {
	switch status {
	case model.AgentStepStatusSucceeded, model.AgentStepStatusRejected, model.AgentStepStatusSkipped:
		return true
	default:
		return false
	}
}

func (service *Service) registerRunning(taskID string, cancel context.CancelFunc) {
	service.runningMu.Lock()
	service.running[taskID] = cancel
	service.runningMu.Unlock()
}

func (service *Service) unregisterRunning(taskID string) {
	service.runningMu.Lock()
	delete(service.running, taskID)
	service.runningMu.Unlock()
}

func (service *Service) cancelRunning(taskID string) {
	service.runningMu.Lock()
	cancel := service.running[taskID]
	service.runningMu.Unlock()
	if cancel != nil {
		cancel()
	}
}
