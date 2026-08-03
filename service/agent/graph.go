package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	hub "GopherAI/common/mcphub"
	"GopherAI/model"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const (
	nodePlan         = "plan"
	nodeDispatch     = "dispatch"
	nodeApprovalGate = "approval_gate"
	nodeTool         = "tool"
	nodeFinalize     = "finalize"
)

const (
	routeApproval = nodeApprovalGate
	routeTool     = nodeTool
	routeFinalize = nodeFinalize
)

type runVersionContextKey struct{}

func withRunVersion(ctx context.Context, runVersion uint64) context.Context {
	return context.WithValue(ctx, runVersionContextKey{}, runVersion)
}

func runVersionFromContext(ctx context.Context) (uint64, error) {
	version, ok := ctx.Value(runVersionContextKey{}).(uint64)
	if !ok || version == 0 {
		return 0, fmt.Errorf("%w: missing task fencing token", ErrConflict)
	}
	return version, nil
}

func init() {
	schema.RegisterName[GraphState]("gopherai_agent_graph_state_v1")
}

func (service *Service) buildGraph(ctx context.Context) (compose.Runnable[string, string], error) {
	graph := compose.NewGraph[string, string](compose.WithGenLocalState(func(context.Context) *GraphState {
		return &GraphState{}
	}))

	if err := graph.AddLambdaNode(nodePlan, compose.InvokableLambda(service.planNode)); err != nil {
		return nil, err
	}
	if err := graph.AddLambdaNode(nodeDispatch, compose.InvokableLambda(service.dispatchNode)); err != nil {
		return nil, err
	}
	if err := graph.AddLambdaNode(nodeApprovalGate, compose.InvokableLambda(service.approvalNode)); err != nil {
		return nil, err
	}
	if err := graph.AddLambdaNode(nodeTool, compose.InvokableLambda(service.toolNode)); err != nil {
		return nil, err
	}
	if err := graph.AddLambdaNode(nodeFinalize, compose.InvokableLambda(service.finalizeNode)); err != nil {
		return nil, err
	}

	if err := graph.AddEdge(compose.START, nodePlan); err != nil {
		return nil, err
	}
	if err := graph.AddEdge(nodePlan, nodeDispatch); err != nil {
		return nil, err
	}
	if err := graph.AddBranch(nodeDispatch, compose.NewGraphBranch(
		service.dispatchRoute,
		map[string]bool{routeApproval: true, routeTool: true, routeFinalize: true},
	)); err != nil {
		return nil, err
	}
	if err := graph.AddEdge(nodeApprovalGate, nodeDispatch); err != nil {
		return nil, err
	}
	if err := graph.AddEdge(nodeTool, nodeDispatch); err != nil {
		return nil, err
	}
	if err := graph.AddEdge(nodeFinalize, compose.END); err != nil {
		return nil, err
	}

	return graph.Compile(
		ctx,
		compose.WithGraphName("gopherai_task_agent_v1"),
		compose.WithNodeTriggerMode(compose.AnyPredecessor),
		compose.WithMaxRunSteps(service.maxRunSteps),
		compose.WithCheckPointStore(service.store),
		compose.WithInterruptAfterNodes([]string{nodePlan, nodeTool}),
	)
}

func (service *Service) planNode(ctx context.Context, taskID string) (string, error) {
	var err error
	taskID, err = graphTaskID(ctx, taskID)
	if err != nil {
		return "", err
	}

	task, err := service.store.GetTaskByID(ctx, taskID)
	if err != nil {
		return "", service.normalizeStoreError(err)
	}
	if taskTerminal(task.Status) {
		return "", ErrTaskStopped
	}
	if planAlreadySaved(task.Steps) {
		return taskID, nil
	}
	runVersion, err := runVersionFromContext(ctx)
	if err != nil {
		return "", err
	}
	planStep := findStepByKind(task.Steps, model.AgentStepKindPlan)
	if planStep == nil {
		return "", fmt.Errorf("%w: task has no planning step", ErrConflict)
	}
	now := service.clock.Now()
	if err := service.store.UpdateTaskFenced(ctx, task.ID, runVersion, map[string]any{
		"status":          model.AgentTaskStatusPlanning,
		"current_step_id": planStep.ID,
	}); err != nil {
		return "", service.normalizeStoreError(err)
	}
	if err := service.store.UpdateStepFenced(ctx, task.ID, planStep.ID, runVersion, map[string]any{
		"status":     model.AgentStepStatusRunning,
		"attempts":   planStep.Attempts + 1,
		"started_at": &now,
	}); err != nil {
		return "", service.normalizeStoreError(err)
	}

	tools, err := service.mcp.Tools(ctx)
	if err != nil {
		return "", fmt.Errorf("discover MCP tools: %w", err)
	}
	response, err := service.models.Generate(ctx, task.ModelID, task.UserName, plannerMessages(task.Goal, tools))
	if err != nil {
		return "", fmt.Errorf("generate task plan: %w", err)
	}
	if response == nil {
		return "", fmt.Errorf("%w: planner returned no response", ErrInvalidPlan)
	}
	plan, err := parsePlanJSON(response.Content)
	if err != nil {
		return "", err
	}
	steps, err := materializePlan(task, plan, tools)
	if err != nil {
		return "", err
	}
	if err := service.store.SavePlanWithSummary(ctx, task.ID, runVersion, plan.Summary, steps); err != nil {
		return "", service.normalizeStoreError(err)
	}
	return taskID, nil
}

func (service *Service) dispatchNode(ctx context.Context, taskID string) (string, error) {
	var err error
	taskID, err = graphTaskID(ctx, taskID)
	if err != nil {
		return "", err
	}
	if _, err := service.nextRoute(ctx, taskID, false); err != nil {
		return "", err
	}
	return taskID, nil
}

func (service *Service) dispatchRoute(ctx context.Context, taskID string) (string, error) {
	var err error
	taskID, err = graphTaskID(ctx, taskID)
	if err != nil {
		return "", err
	}
	return service.nextRoute(ctx, taskID, true)
}

func (service *Service) nextRoute(ctx context.Context, taskID string, persistCurrent bool) (string, error) {
	task, err := service.store.GetTaskByID(ctx, taskID)
	if err != nil {
		return "", service.normalizeStoreError(err)
	}
	switch task.Status {
	case model.AgentTaskStatusCancelled, model.AgentTaskStatusRejected, model.AgentTaskStatusSucceeded:
		return "", ErrTaskStopped
	case model.AgentTaskStatusRequiresReview:
		return "", ErrExecutionNeedsReview
	}

	var selected *model.AgentStep
	for index := range task.Steps {
		step := &task.Steps[index]
		if step.Kind == model.AgentStepKindPlan {
			continue
		}
		if step.Kind == model.AgentStepKindFinal {
			if selected == nil {
				selected = step
			}
			continue
		}
		switch step.Status {
		case model.AgentStepStatusSucceeded, model.AgentStepStatusRejected, model.AgentStepStatusSkipped:
			continue
		case model.AgentStepStatusFailed:
			return "", fmt.Errorf("tool step %s failed", step.ID)
		case model.AgentStepStatusExecutionUnknown:
			return "", ErrExecutionNeedsReview
		default:
			selected = step
		}
		break
	}
	if selected == nil {
		return "", fmt.Errorf("%w: task has no final step", ErrConflict)
	}

	route := routeFinalize
	if selected.Kind == model.AgentStepKindTool {
		if selected.RequiresApproval && selected.ApprovalDecision != model.AgentApprovalDecisionApproved {
			route = routeApproval
		} else {
			route = routeTool
		}
	}
	if persistCurrent && task.CurrentStepID != selected.ID {
		runVersion, versionErr := runVersionFromContext(ctx)
		if versionErr != nil {
			return "", versionErr
		}
		if err := service.store.UpdateTaskFenced(ctx, task.ID, runVersion, map[string]any{
			"current_step_id": selected.ID,
		}); err != nil {
			return "", service.normalizeStoreError(err)
		}
	}
	return route, nil
}

func (service *Service) approvalNode(ctx context.Context, taskID string) (string, error) {
	var err error
	taskID, err = graphTaskID(ctx, taskID)
	if err != nil {
		return "", err
	}
	task, step, err := service.currentToolStep(ctx, taskID)
	if err != nil {
		return "", err
	}
	if !step.RequiresApproval {
		return taskID, nil
	}
	switch step.ApprovalDecision {
	case model.AgentApprovalDecisionApproved:
		return taskID, nil
	case model.AgentApprovalDecisionRejected:
		return "", ErrTaskStopped
	}
	runVersion, err := runVersionFromContext(ctx)
	if err != nil {
		return "", err
	}
	if step.Status != model.AgentStepStatusWaitingApproval {
		if err := service.store.UpdateStepFenced(ctx, task.ID, step.ID, runVersion, map[string]any{
			"status": model.AgentStepStatusWaitingApproval,
		}); err != nil {
			return "", service.normalizeStoreError(err)
		}
	}
	if err := service.store.UpdateTaskFenced(ctx, task.ID, runVersion, map[string]any{
		"status":          model.AgentTaskStatusWaitingApproval,
		"current_step_id": step.ID,
		"lease_owner":     "",
		"lease_until":     nil,
		"heartbeat_at":    nil,
	}); err != nil {
		return "", service.normalizeStoreError(err)
	}

	extra := &ApprovalInterrupt{
		TaskID:           task.ID,
		StepID:           step.ID,
		ToolName:         step.ToolName,
		Risk:             step.RiskLevel,
		ArgumentsDigest:  step.ArgumentsDigest,
		ArgumentsPreview: truncateRunes(step.ArgumentsPreview, maxArgumentsPreviewRunes),
	}
	return "", compose.NewInterruptAndRerunErr(extra)
}

func (service *Service) toolNode(ctx context.Context, taskID string) (string, error) {
	var err error
	taskID, err = graphTaskID(ctx, taskID)
	if err != nil {
		return "", err
	}
	task, step, err := service.currentToolStep(ctx, taskID)
	if err != nil {
		return "", err
	}
	if step.RequiresApproval && step.ApprovalDecision != model.AgentApprovalDecisionApproved {
		return "", fmt.Errorf("%w: tool step reached execution without durable approval", ErrConflict)
	}
	runVersion, err := runVersionFromContext(ctx)
	if err != nil {
		return "", err
	}
	now := service.clock.Now()
	if err := service.store.UpdateStepFenced(ctx, task.ID, step.ID, runVersion, map[string]any{
		"status":        model.AgentStepStatusRunning,
		"attempts":      step.Attempts + 1,
		"started_at":    &now,
		"finished_at":   nil,
		"error_message": "",
	}); err != nil {
		return "", service.normalizeStoreError(err)
	}

	arguments := make(map[string]any)
	decoder := json.NewDecoder(strings.NewReader(step.ToolArgumentsJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&arguments); err != nil {
		return "", service.failToolStep(ctx, task, step, runVersion, fmt.Errorf("decode persisted tool arguments: %w", err))
	}
	canonicalArguments, err := canonicalJSON(arguments)
	if err != nil {
		return "", service.failToolStep(ctx, task, step, runVersion, fmt.Errorf("canonicalize persisted tool arguments: %w", err))
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(canonicalArguments))
	if !strings.EqualFold(digest, strings.TrimSpace(step.ArgumentsDigest)) {
		return "", service.failToolStep(ctx, task, step, runVersion, errors.New("persisted MCP arguments failed integrity verification"))
	}
	definitions, err := service.mcp.Tools(ctx)
	if err != nil {
		return "", service.failToolStep(ctx, task, step, runVersion, fmt.Errorf("refresh MCP tools: %w", err))
	}
	definition, exists := definitionByName(definitions, step.ToolName)
	if !exists {
		return "", service.failToolStep(ctx, task, step, runVersion, errors.New("planned MCP tool is no longer available"))
	}
	if err := definition.ValidateArguments(arguments); err != nil {
		return "", service.failToolStep(ctx, task, step, runVersion, fmt.Errorf("revalidate MCP arguments: %w", err))
	}
	if toolPolicyChanged(step, definition) {
		return "", service.requirePolicyReview(ctx, task, step, runVersion, definition)
	}

	request := hub.InvokeRequest{UserName: task.UserName, ToolName: step.ToolName, Arguments: arguments}
	if step.RequiresApproval {
		challenge, challengeErr := service.mcp.CreateApproval(ctx, task.UserName, step.ToolName, arguments)
		if challengeErr != nil {
			return "", service.failToolStep(ctx, task, step, runVersion, fmt.Errorf("create MCP approval: %w", challengeErr))
		}
		issued, approveErr := service.mcp.Approve(task.UserName, challenge.ID)
		if approveErr != nil {
			return "", service.failToolStep(ctx, task, step, runVersion, fmt.Errorf("issue MCP approval: %w", approveErr))
		}
		request.ApprovalToken = issued.Token
	}
	invocation, err := service.mcp.Call(ctx, request)
	request.ApprovalToken = ""
	if err != nil {
		return "", service.failInvokedTool(ctx, task, step, runVersion, fmt.Errorf("invoke MCP tool: %w", err))
	}
	if invocation == nil {
		return "", service.failInvokedTool(ctx, task, step, runVersion, errors.New("MCP tool returned no invocation result"))
	}
	if invocation.Result == nil {
		return "", service.failInvokedTool(ctx, task, step, runVersion, errors.New("MCP tool returned no result"))
	}
	if invocation.Result.IsError {
		return "", service.failInvokedTool(ctx, task, step, runVersion, errors.New("MCP tool returned an error result"))
	}
	rawResult, err := json.Marshal(invocation.Result)
	if err != nil {
		return "", service.failInvokedTool(ctx, task, step, runVersion, fmt.Errorf("encode MCP tool result: %w", err))
	}
	safeResult := redactJSONBytes(rawResult, maxToolOutputRunes)
	finished := service.clock.Now()
	if err := service.store.UpdateStepFenced(ctx, task.ID, step.ID, runVersion, map[string]any{
		"status":           model.AgentStepStatusSucceeded,
		"tool_output_json": safeResult,
		"result_summary":   truncateRunes(safeResult, maxPlanSummaryRunes),
		"mcp_request_id":   invocation.RequestID,
		"finished_at":      &finished,
		"error_message":    "",
	}); err != nil {
		return "", service.failInvokedTool(ctx, task, step, runVersion, fmt.Errorf("persist MCP tool result: %w", service.normalizeStoreError(err)))
	}
	if err := service.store.UpdateTaskFenced(ctx, task.ID, runVersion, map[string]any{
		"status":          model.AgentTaskStatusRunning,
		"current_step_id": "",
		"heartbeat_at":    &finished,
	}); err != nil {
		return "", service.normalizeStoreError(err)
	}
	return taskID, nil
}

func (service *Service) finalizeNode(ctx context.Context, taskID string) (string, error) {
	var err error
	taskID, err = graphTaskID(ctx, taskID)
	if err != nil {
		return "", err
	}
	task, err := service.store.GetTaskByID(ctx, taskID)
	if err != nil {
		return "", service.normalizeStoreError(err)
	}
	if task.Status == model.AgentTaskStatusCancelled || task.Status == model.AgentTaskStatusRejected {
		return "", ErrTaskStopped
	}
	runVersion, err := runVersionFromContext(ctx)
	if err != nil {
		return "", err
	}
	finalStep := findStepByKind(task.Steps, model.AgentStepKindFinal)
	if finalStep == nil {
		return "", fmt.Errorf("%w: task has no final step", ErrConflict)
	}
	now := service.clock.Now()
	if err := service.store.UpdateStepFenced(ctx, task.ID, finalStep.ID, runVersion, map[string]any{
		"status":     model.AgentStepStatusRunning,
		"attempts":   finalStep.Attempts + 1,
		"started_at": &now,
	}); err != nil {
		return "", service.normalizeStoreError(err)
	}
	response, err := service.models.Generate(ctx, task.ModelID, task.UserName, finalMessages(task))
	if err != nil {
		return "", service.failFinalStep(ctx, task, finalStep, runVersion, fmt.Errorf("generate final answer: %w", err))
	}
	if response == nil || strings.TrimSpace(response.Content) == "" {
		return "", service.failFinalStep(ctx, task, finalStep, runVersion, errors.New("final model returned no answer"))
	}
	answer := truncateRunes(redactText(strings.TrimSpace(response.Content)), maxFinalAnswerRunes)
	finished := service.clock.Now()
	if err := service.store.UpdateStepFenced(ctx, task.ID, finalStep.ID, runVersion, map[string]any{
		"status":         model.AgentStepStatusSucceeded,
		"result_summary": answer,
		"finished_at":    &finished,
		"error_message":  "",
	}); err != nil {
		return "", service.normalizeStoreError(err)
	}
	if err := service.store.UpdateTaskFenced(ctx, task.ID, runVersion, map[string]any{
		"status":          model.AgentTaskStatusSucceeded,
		"current_step_id": finalStep.ID,
		"final_answer":    answer,
		"finished_at":     &finished,
		"lease_owner":     "",
		"lease_until":     nil,
		"heartbeat_at":    nil,
		"error_message":   "",
	}); err != nil {
		return "", service.normalizeStoreError(err)
	}
	return taskID, nil
}

func (service *Service) currentToolStep(ctx context.Context, taskID string) (*model.AgentTask, *model.AgentStep, error) {
	task, err := service.store.GetTaskByID(ctx, taskID)
	if err != nil {
		return nil, nil, service.normalizeStoreError(err)
	}
	for index := range task.Steps {
		if task.Steps[index].ID == task.CurrentStepID && task.Steps[index].Kind == model.AgentStepKindTool {
			return task, &task.Steps[index], nil
		}
	}
	return nil, nil, fmt.Errorf("%w: current tool step is unavailable", ErrConflict)
}

func graphTaskID(ctx context.Context, input string) (string, error) {
	input = strings.TrimSpace(input)
	resolved := input
	err := compose.ProcessState[*GraphState](ctx, func(_ context.Context, state *GraphState) error {
		if state.TaskID == "" {
			if input == "" {
				return fmt.Errorf("%w: checkpoint has no task ID", ErrConflict)
			}
			state.TaskID = input
			resolved = input
			return nil
		}
		if input != "" && state.TaskID != input {
			return fmt.Errorf("%w: checkpoint task mismatch", ErrConflict)
		}
		resolved = state.TaskID
		return nil
	})
	if err != nil {
		return "", err
	}
	if resolved == "" {
		return "", fmt.Errorf("%w: task ID is required", ErrInvalidInput)
	}
	return resolved, nil
}

func (service *Service) failToolStep(ctx context.Context, task *model.AgentTask, step *model.AgentStep, runVersion uint64, cause error) error {
	message := truncateRunes(redactText(cause.Error()), 1000)
	finished := service.clock.Now()
	_ = service.store.UpdateStepFenced(ctx, task.ID, step.ID, runVersion, map[string]any{
		"status":        model.AgentStepStatusFailed,
		"finished_at":   &finished,
		"error_message": message,
	})
	_ = service.store.UpdateTaskFenced(ctx, task.ID, runVersion, map[string]any{
		"status":        model.AgentTaskStatusFailed,
		"finished_at":   &finished,
		"error_message": message,
		"lease_owner":   "",
		"lease_until":   nil,
	})
	return cause
}

func (service *Service) failFinalStep(ctx context.Context, task *model.AgentTask, step *model.AgentStep, runVersion uint64, cause error) error {
	return service.failToolStep(ctx, task, step, runVersion, cause)
}

func (service *Service) failInvokedTool(ctx context.Context, task *model.AgentTask, step *model.AgentStep, runVersion uint64, cause error) error {
	if toolReplaySafe(step) {
		return service.failToolStep(ctx, task, step, runVersion, cause)
	}
	message := truncateRunes(redactText(cause.Error()), 1000)
	if err := service.store.MarkExecutionUnknownFenced(ctx, task.ID, step.ID, runVersion, message); err != nil {
		return fmt.Errorf("persist uncertain MCP outcome: %w", service.normalizeStoreError(err))
	}
	return fmt.Errorf("%w: %s", ErrExecutionNeedsReview, message)
}

func (service *Service) requirePolicyReview(ctx context.Context, task *model.AgentTask, step *model.AgentStep, runVersion uint64, definition hub.ToolDefinition) error {
	message := "MCP tool safety policy changed after planning; explicit review is required"
	if err := service.store.MarkPolicyReviewFenced(
		ctx,
		task.ID,
		step.ID,
		runVersion,
		message,
		string(definition.Risk),
		definition.RequiresApproval,
		definition.ReadOnly,
		definition.Idempotent,
		definition.Destructive,
	); err != nil {
		return fmt.Errorf("persist MCP policy review: %w", service.normalizeStoreError(err))
	}
	return fmt.Errorf("%w: %s", ErrExecutionNeedsReview, message)
}

func toolReplaySafe(step *model.AgentStep) bool {
	return step != nil && !step.Destructive && (step.ReadOnly || step.Idempotent)
}

func toolPolicyChanged(step *model.AgentStep, definition hub.ToolDefinition) bool {
	return step == nil || step.RiskLevel != string(definition.Risk) ||
		step.RequiresApproval != definition.RequiresApproval ||
		step.ReadOnly != definition.ReadOnly ||
		step.Idempotent != definition.Idempotent ||
		step.Destructive != definition.Destructive
}

func plannerMessages(goal string, tools []hub.ToolDefinition) []*schema.Message {
	type plannerTool struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
	available := make([]plannerTool, 0, len(tools))
	for _, definition := range tools {
		available = append(available, plannerTool{
			Name:        definition.Name,
			Description: truncateRunes(definition.Description, 1000),
			InputSchema: definition.InputSchema,
		})
	}
	encoded, _ := json.Marshal(available)
	system := `你是任务规划器。只输出一个 JSON 对象，不要 Markdown，不要解释。格式：{"summary":"简短计划","steps":[{"title":"步骤标题","instruction":"执行目的","tool_name":"精确工具名","arguments":{}}]}。steps 只能使用给定 MCP 工具，最多 8 步；如果不需要工具，steps 返回空数组。不要输出风险、审批或密钥字段。`
	user := "用户目标：\n" + truncateRunes(goal, maxGoalRunes) + "\n\n可用 MCP 工具：\n" + truncateRunes(string(encoded), 48_000)
	return []*schema.Message{schema.SystemMessage(system), schema.UserMessage(user)}
}

func finalMessages(task *model.AgentTask) []*schema.Message {
	type safeStep struct {
		Title  string `json:"title"`
		Tool   string `json:"tool,omitempty"`
		Status string `json:"status"`
		Result string `json:"result,omitempty"`
	}
	steps := make([]safeStep, 0, len(task.Steps))
	for _, step := range task.Steps {
		if step.Kind != model.AgentStepKindTool {
			continue
		}
		steps = append(steps, safeStep{
			Title:  truncateRunes(step.Title, maxTitleRunes),
			Tool:   step.ToolName,
			Status: step.Status,
			Result: truncateRunes(redactText(step.ResultSummary), maxPlanSummaryRunes),
		})
	}
	encoded, _ := json.Marshal(steps)
	system := "你是任务总结器。根据目标和工具执行记录给出简洁、准确的最终答复。不得编造工具结果，不得泄露令牌、密码、API Key 或其他凭据。"
	user := "目标：\n" + truncateRunes(task.Goal, maxGoalRunes) + "\n\n计划摘要：\n" + truncateRunes(task.PlanSummary, maxPlanSummaryRunes) + "\n\n执行记录：\n" + truncateRunes(string(encoded), 48_000)
	return []*schema.Message{schema.SystemMessage(system), schema.UserMessage(user)}
}

func planAlreadySaved(steps []model.AgentStep) bool {
	for _, step := range steps {
		if step.Sequence > 0 {
			return true
		}
	}
	return false
}

func findStepByKind(steps []model.AgentStep, kind string) *model.AgentStep {
	for index := range steps {
		if steps[index].Kind == kind {
			copy := steps[index]
			return &copy
		}
	}
	return nil
}

func definitionByName(definitions []hub.ToolDefinition, name string) (hub.ToolDefinition, bool) {
	for _, definition := range definitions {
		if definition.Name == name {
			return definition, true
		}
	}
	return hub.ToolDefinition{}, false
}

func taskTerminal(status string) bool {
	switch status {
	case model.AgentTaskStatusSucceeded, model.AgentTaskStatusFailed, model.AgentTaskStatusRejected, model.AgentTaskStatusCancelled:
		return true
	default:
		return false
	}
}
