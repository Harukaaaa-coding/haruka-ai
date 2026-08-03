package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	hub "GopherAI/common/mcphub"
	"GopherAI/model"
)

func TestParsePlanJSONStrictAndBounded(t *testing.T) {
	valid := `{"summary":"check weather","steps":[{"title":"lookup","tool_name":"weather.current","arguments":{"city":"Beijing"}}]}`
	plan, err := parsePlanJSON(valid)
	if err != nil {
		t.Fatalf("parse valid plan: %v", err)
	}
	if plan.Summary != "check weather" || len(plan.Steps) != 1 {
		t.Fatalf("unexpected plan: %#v", plan)
	}

	for name, content := range map[string]string{
		"unknown field":    `{"summary":"x","steps":[],"approval":true}`,
		"second value":     `{"summary":"x","steps":[]} {"summary":"y","steps":[]}`,
		"trailing garbage": `{"summary":"x","steps":[]} definitely-not-json`,
		"after code fence": "```json\n{\"summary\":\"x\",\"steps\":[]}\n``` trailing",
	} {
		t.Run(name, func(t *testing.T) {
			if _, parseErr := parsePlanJSON(content); !errors.Is(parseErr, ErrInvalidPlan) {
				t.Fatalf("parsePlanJSON() error = %v, want ErrInvalidPlan", parseErr)
			}
		})
	}

	tooMany := PlanDocument{Summary: "too many", Steps: make([]PlannedToolStep, maxPlanToolSteps+1)}
	encoded, err := json.Marshal(tooMany)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parsePlanJSON(string(encoded)); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("nine-step plan error = %v, want ErrInvalidPlan", err)
	}
}

func TestMaterializePlanTrustsMCPDefinition(t *testing.T) {
	definition := hub.ToolDefinition{
		Name:             "files.delete",
		Description:      "delete a file",
		InputSchema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`),
		Risk:             hub.RiskHigh,
		RequiresApproval: true,
		ReadOnly:         false,
		Idempotent:       false,
		Destructive:      true,
	}
	task := &model.AgentTask{ID: "task-1", UserName: "alice"}
	plan := &PlanDocument{Steps: []PlannedToolStep{{
		Title:     "delete",
		ToolName:  definition.Name,
		Arguments: map[string]any{"path": "/tmp/example"},
	}}}

	steps, err := materializePlan(task, plan, []hub.ToolDefinition{definition})
	if err != nil {
		t.Fatalf("materializePlan: %v", err)
	}
	if len(steps) != 2 || steps[0].Kind != model.AgentStepKindTool || steps[1].Kind != model.AgentStepKindFinal {
		t.Fatalf("unexpected materialized steps: %#v", steps)
	}
	tool := steps[0]
	if tool.RiskLevel != string(hub.RiskHigh) || !tool.RequiresApproval || !tool.Destructive || tool.ReadOnly || tool.Idempotent {
		t.Fatalf("MCP policy metadata was not preserved: %#v", tool)
	}
	if tool.ArgumentsDigest == "" || strings.Contains(tool.ArgumentsPreview, "approval") {
		t.Fatalf("unexpected argument metadata: %#v", tool)
	}

	plan.Steps[0].ToolName = "missing.tool"
	if _, err := materializePlan(task, plan, []hub.ToolDefinition{definition}); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("unknown tool error = %v, want ErrInvalidPlan", err)
	}
	plan.Steps[0].ToolName = definition.Name
	plan.Steps[0].Arguments = map[string]any{}
	if _, err := materializePlan(task, plan, []hub.ToolDefinition{definition}); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("invalid argument error = %v, want ErrInvalidPlan", err)
	}
}

func TestRedactionCoversCredentialsAndBearerValues(t *testing.T) {
	preview := redactAndLimitJSON(map[string]any{
		"private_key": "private-value",
		"access_key":  "access-value",
		"passphrase":  "passphrase-value",
		"nested": map[string]any{
			"message": "Authorization: Bearer abc.def-123 client_secret=client-value",
		},
	}, maxArgumentsPreviewRunes)
	for _, secret := range []string{"private-value", "access-value", "passphrase-value", "abc.def-123", "client-value"} {
		if strings.Contains(preview, secret) {
			t.Fatalf("redacted preview leaked %q: %s", secret, preview)
		}
	}

	pem := "-----BEGIN PRIVATE KEY-----\nprivate-material\n-----END PRIVATE KEY-----"
	if redacted := redactText(pem); strings.Contains(redacted, "private-material") {
		t.Fatalf("PEM private key leaked: %s", redacted)
	}
}
