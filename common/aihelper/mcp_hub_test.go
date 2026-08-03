package aihelper

import (
	"context"
	"errors"
	"testing"

	hub "GopherAI/common/mcphub"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/mark3labs/mcp-go/mcp"
)

type scriptedToolModel struct {
	responses  []*schema.Message
	generateAt int
	boundTools []*schema.ToolInfo
	inputs     [][]*schema.Message
}

func (model *scriptedToolModel) Generate(_ context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	model.inputs = append(model.inputs, append([]*schema.Message(nil), input...))
	if model.generateAt >= len(model.responses) {
		return nil, errors.New("unexpected generate call")
	}
	response := model.responses[model.generateAt]
	model.generateAt++
	return response, nil
}

func (*scriptedToolModel) Stream(context.Context, []*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("unused")
}

func (model *scriptedToolModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	model.boundTools = tools
	return model, nil
}

func TestGenerateWithHubToolsUsesNamespacedAutomaticTool(t *testing.T) {
	hubToolName := "weather.get_weather"
	providerToolName := safeEinoToolName(hubToolName)
	model := &scriptedToolModel{responses: []*schema.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call-1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      providerToolName,
					Arguments: `{"city":"上海"}`,
				},
			}},
		},
		{Role: schema.Assistant, Content: "上海天气晴朗"},
	}}

	originalList := listAutomaticMCPTools
	originalInvoke := invokeAutomaticMCPTool
	t.Cleanup(func() {
		listAutomaticMCPTools = originalList
		invokeAutomaticMCPTool = originalInvoke
	})
	listAutomaticMCPTools = func(context.Context) ([]*schema.ToolInfo, error) {
		return []*schema.ToolInfo{{Name: hubToolName, Desc: "查询天气"}}, nil
	}
	invoked := false
	invokeAutomaticMCPTool = func(_ context.Context, userName, toolName string, arguments map[string]any) (*hub.InvocationResult, error) {
		invoked = true
		if userName != "user" || toolName != hubToolName || arguments["city"] != "上海" {
			t.Fatalf("unexpected invocation: user=%q tool=%q args=%#v", userName, toolName, arguments)
		}
		return &hub.InvocationResult{Result: mcp.NewToolResultText("sunny")}, nil
	}

	mcpModel := &MCPModel{llm: model, username: "user"}
	response, err := mcpModel.generateWithHubTools(context.Background(), []*schema.Message{{Role: schema.User, Content: "上海天气"}})
	if err != nil {
		t.Fatalf("generate with tools: %v", err)
	}
	if response.Content != "上海天气晴朗" || !invoked {
		t.Fatalf("unexpected response=%#v invoked=%v", response, invoked)
	}
	if len(model.boundTools) != 1 || model.boundTools[0].Name != providerToolName {
		t.Fatalf("tool was not safely bound: %#v", model.boundTools)
	}
	if len(model.inputs) != 2 || len(model.inputs[1]) != 3 || model.inputs[1][2].Role != schema.Tool {
		t.Fatalf("tool result was not returned to the model: %#v", model.inputs)
	}
}

func TestSafeEinoToolNameIsProviderCompatibleAndStable(t *testing.T) {
	first := safeEinoToolName("server.with.punctuation:tool/name")
	if first != safeEinoToolName("server.with.punctuation:tool/name") {
		t.Fatal("tool name mapping must be stable")
	}
	if first == safeEinoToolName("server.with.punctuation:other/name") {
		t.Fatal("different hub tools must not collide")
	}
	if len(first) > 64 {
		t.Fatalf("provider tool name is too long: %q", first)
	}
	for _, character := range first {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-') {
			t.Fatalf("provider tool name contains invalid character %q: %q", character, first)
		}
	}
}
