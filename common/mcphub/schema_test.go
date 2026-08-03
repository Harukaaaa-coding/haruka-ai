package mcphub

import (
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestConvertToolNamespacesSchemaAndConvertsToEino(t *testing.T) {
	server := ServerConfig{
		ID:            "demo",
		ToolAllowlist: []string{"lookup"},
		ReadOnlyTools: []string{"lookup"},
		RiskLevels:    map[string]RiskLevel{"lookup": RiskLow},
	}
	tool := mcp.NewTool(
		"lookup",
		mcp.WithDescription("Look up a record"),
		mcp.WithString("id", mcp.Required(), mcp.MinLength(2)),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
	)
	converted, err := convertTool(server, tool)
	if err != nil {
		t.Fatalf("convertTool(): %v", err)
	}
	if converted.Name != "demo.lookup" || converted.UpstreamName != "lookup" {
		t.Fatalf("converted names = %#v", converted)
	}
	if !converted.ReadOnly || !converted.Idempotent || converted.Risk != RiskLow || converted.RequiresApproval {
		t.Fatalf("converted policy = %#v", converted)
	}
	if err := converted.ValidateArguments(map[string]any{"id": "42"}); err != nil {
		t.Fatalf("ValidateArguments(valid): %v", err)
	}
	if err := converted.ValidateArguments(map[string]any{}); ErrorCode(err) != ErrorInvalidArguments {
		t.Fatalf("missing required argument error = %v", err)
	}
	einoTool, err := converted.EinoToolInfo()
	if err != nil {
		t.Fatalf("EinoToolInfo(): %v", err)
	}
	if einoTool.Name != converted.Name || einoTool.ParamsOneOf == nil {
		t.Fatalf("Eino tool = %#v", einoTool)
	}
}

func TestValidateArgumentsRejectsNestedTypeAndAdditionalProperty(t *testing.T) {
	schema := json.RawMessage(`{
  "type":"object",
  "properties":{
    "options":{
      "type":"object",
      "properties":{"count":{"type":"integer","minimum":1}},
      "required":["count"],
      "additionalProperties":false
    }
  },
  "required":["options"],
  "additionalProperties":false
}`)
	tool := ToolDefinition{Name: "demo.run", InputSchema: schema}
	tests := []map[string]any{
		{"options": map[string]any{"count": 1.5}},
		{"options": map[string]any{"count": 1, "secret_extra": true}},
		{"options": map[string]any{"count": 1}, "other": true},
	}
	for _, arguments := range tests {
		if err := tool.ValidateArguments(arguments); ErrorCode(err) != ErrorInvalidArguments {
			t.Fatalf("ValidateArguments(%#v) = %v", arguments, err)
		}
	}
	if err := tool.ValidateArguments(map[string]any{"options": map[string]any{"count": 2}}); err != nil {
		t.Fatalf("ValidateArguments(valid): %v", err)
	}
}

func TestNormalizeSchemaRejectsInvalidRequiredReferenceAndRemoteRef(t *testing.T) {
	for _, raw := range []string{
		`{"type":"object","properties":{},"required":["missing"]}`,
		`{"type":"object","properties":{"x":{"$ref":"https://example.test/schema"}}}`,
	} {
		if _, _, err := normalizeSchema([]byte(raw), true); err == nil {
			t.Fatalf("normalizeSchema(%s) error = nil", raw)
		}
	}
}
