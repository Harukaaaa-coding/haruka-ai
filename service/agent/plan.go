package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	hub "GopherAI/common/mcphub"
	"GopherAI/model"

	"github.com/google/uuid"
)

var (
	sensitiveKeyPattern  = regexp.MustCompile(`(?i)(api[-_ ]?key|authorization|access[-_ ]?(?:token|key)|refresh[-_ ]?token|id[-_ ]?token|private[-_ ]?key|client[-_ ]?secret|secret|token|password|passwd|passphrase|credential|cookie|session[-_ ]?(?:id|token)|signature)`)
	sensitiveTextPattern = regexp.MustCompile(`(?i)\b(api[-_ ]?key|authorization|access[-_ ]?(?:token|key)|refresh[-_ ]?token|id[-_ ]?token|private[-_ ]?key|client[-_ ]?secret|secret|token|password|passwd|passphrase|credential|cookie|session[-_ ]?(?:id|token)|signature)(\s*[:=]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|bearer\s+[^\s,;]+|[^\s,;]+)`)
	bearerTextPattern    = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`)
	privateKeyPattern    = regexp.MustCompile(`(?is)-----BEGIN(?: [A-Z0-9]+)* PRIVATE KEY-----.*?-----END(?: [A-Z0-9]+)* PRIVATE KEY-----`)
)

func parsePlanJSON(content string) (*PlanDocument, error) {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		firstNewline := strings.IndexByte(content, '\n')
		lastFence := strings.LastIndex(content, "```")
		if firstNewline < 0 || lastFence <= firstNewline {
			return nil, fmt.Errorf("%w: malformed JSON code fence", ErrInvalidPlan)
		}
		language := strings.TrimSpace(content[3:firstNewline])
		if language != "" && !strings.EqualFold(language, "json") {
			return nil, fmt.Errorf("%w: planner code fence must contain JSON", ErrInvalidPlan)
		}
		if strings.TrimSpace(content[lastFence+3:]) != "" {
			return nil, fmt.Errorf("%w: planner returned content after JSON fence", ErrInvalidPlan)
		}
		content = strings.TrimSpace(content[firstNewline+1 : lastFence])
	}
	if content == "" || !strings.HasPrefix(content, "{") {
		return nil, fmt.Errorf("%w: planner must return one JSON object", ErrInvalidPlan)
	}

	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	plan := new(PlanDocument)
	if err := decoder.Decode(plan); err != nil {
		return nil, fmt.Errorf("%w: decode planner JSON: %v", ErrInvalidPlan, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, fmt.Errorf("%w: planner returned trailing JSON", ErrInvalidPlan)
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: planner returned trailing content", ErrInvalidPlan)
	}
	if len(plan.Steps) > maxPlanToolSteps {
		return nil, fmt.Errorf("%w: at most %d tool steps are allowed", ErrInvalidPlan, maxPlanToolSteps)
	}
	plan.Summary = truncateRunes(strings.TrimSpace(plan.Summary), maxPlanSummaryRunes)
	return plan, nil
}

func materializePlan(task *model.AgentTask, plan *PlanDocument, tools []hub.ToolDefinition) ([]model.AgentStep, error) {
	if task == nil || plan == nil {
		return nil, fmt.Errorf("%w: task and plan are required", ErrInvalidPlan)
	}
	definitions := make(map[string]hub.ToolDefinition, len(tools))
	for _, definition := range tools {
		definitions[definition.Name] = definition
	}

	steps := make([]model.AgentStep, 0, len(plan.Steps)+1)
	for index, planned := range plan.Steps {
		planned.ToolName = strings.TrimSpace(planned.ToolName)
		definition, exists := definitions[planned.ToolName]
		if !exists {
			return nil, fmt.Errorf("%w: step %d references an unavailable MCP tool", ErrInvalidPlan, index+1)
		}
		if planned.Arguments == nil {
			planned.Arguments = map[string]any{}
		}
		if err := definition.ValidateArguments(planned.Arguments); err != nil {
			return nil, fmt.Errorf("%w: step %d arguments failed MCP validation: %v", ErrInvalidPlan, index+1, err)
		}

		argumentsJSON, err := canonicalJSON(planned.Arguments)
		if err != nil {
			return nil, fmt.Errorf("%w: step %d arguments cannot be encoded", ErrInvalidPlan, index+1)
		}
		digest := sha256.Sum256(argumentsJSON)
		title := truncateRunes(strings.TrimSpace(planned.Title), maxTitleRunes)
		if title == "" {
			title = "调用 " + definition.Name
		}

		steps = append(steps, model.AgentStep{
			ID:                uuid.NewString(),
			TaskID:            task.ID,
			UserName:          task.UserName,
			Sequence:          index + 1,
			Kind:              model.AgentStepKindTool,
			NodeKey:           nodeTool,
			Title:             title,
			Instruction:       truncateRunes(strings.TrimSpace(planned.Instruction), maxInstructionRunes),
			Status:            model.AgentStepStatusPending,
			ToolName:          definition.Name,
			ToolArgumentsJSON: string(argumentsJSON),
			ArgumentsDigest:   hex.EncodeToString(digest[:]),
			ArgumentsPreview:  redactAndLimitJSON(planned.Arguments, maxArgumentsPreviewRunes),
			RiskLevel:         string(definition.Risk),
			RequiresApproval:  definition.RequiresApproval,
			ReadOnly:          definition.ReadOnly,
			Idempotent:        definition.Idempotent,
			Destructive:       definition.Destructive,
			ApprovalDecision:  model.AgentApprovalDecisionPending,
		})
	}

	steps = append(steps, model.AgentStep{
		ID:       uuid.NewString(),
		TaskID:   task.ID,
		UserName: task.UserName,
		Sequence: len(plan.Steps) + 1,
		Kind:     model.AgentStepKindFinal,
		NodeKey:  nodeFinalize,
		Title:    "生成最终结果",
		Status:   model.AgentStepStatusPending,
	})
	return steps, nil
}

func canonicalJSON(value any) ([]byte, error) {
	// encoding/json sorts string map keys, which gives us a stable digest for
	// the JSON-compatible MCP argument values accepted by ValidateArguments.
	return json.Marshal(value)
}

func redactAndLimitJSON(value any, maxRunes int) string {
	redacted := redactValue(value, "")
	encoded, err := json.Marshal(redacted)
	if err != nil {
		return "[unavailable]"
	}
	if utf8.RuneCount(encoded) <= maxRunes {
		return string(encoded)
	}
	// Keep persisted ToolOutputJSON valid JSON even when a large tool result
	// is bounded. One third leaves room for JSON escaping in the wrapper.
	previewLimit := maxRunes / 3
	if previewLimit < 1 {
		return `{"truncated":true}`
	}
	wrapped, marshalErr := json.Marshal(map[string]any{
		"truncated": true,
		"preview":   truncateRunes(string(encoded), previewLimit),
	})
	if marshalErr != nil {
		return `{"truncated":true}`
	}
	return string(wrapped)
}

func redactJSONBytes(raw []byte, maxRunes int) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return redactText(truncateRunes(string(raw), maxRunes))
	}
	return redactAndLimitJSON(value, maxRunes)
}

func redactValue(value any, key string) any {
	if sensitiveKeyPattern.MatchString(key) {
		return "[REDACTED]"
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		keys := make([]string, 0, len(typed))
		for childKey := range typed {
			keys = append(keys, childKey)
		}
		sort.Strings(keys)
		for _, childKey := range keys {
			out[childKey] = redactValue(typed[childKey], childKey)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = redactValue(typed[i], key)
		}
		return out
	case string:
		return redactText(typed)
	default:
		return value
	}
}

func redactText(value string) string {
	value = privateKeyPattern.ReplaceAllString(value, "[REDACTED PRIVATE KEY]")
	value = bearerTextPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	return sensitiveTextPattern.ReplaceAllString(value, "$1$2[REDACTED]")
}

func truncateRunes(value string, maximum int) string {
	if maximum <= 0 || value == "" {
		return ""
	}
	if utf8.RuneCountInString(value) <= maximum {
		return value
	}
	runes := []rune(value)
	return string(runes[:maximum]) + "…"
}
