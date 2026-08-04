package mcphub

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestHTTPProtocolInitializeListAndCall(t *testing.T) {
	var operationID string
	upstream := server.NewMCPServer("hub-protocol-test", "1.0.0", server.WithToolCapabilities(true))
	upstream.AddTool(
		mcp.NewTool(
			"echo",
			mcp.WithString("text", mcp.Required()),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if request.Params.Meta != nil {
				operationID, _ = request.Params.Meta.AdditionalFields["gopherai/operation_id"].(string)
			}
			return mcp.NewToolResultText(request.GetString("text", "")), nil
		},
	)
	httpServer := httptest.NewServer(server.NewStreamableHTTPServer(upstream))
	defer httpServer.Close()

	configuration := Config{
		ServiceAllowlist: []string{"test"},
		Servers: []ServerConfig{
			{
				ID: "test", URL: httpServer.URL, Enabled: true, TimeoutMS: 2_000,
				ToolAllowlist: []string{"echo"}, ReadOnlyTools: []string{"echo"},
				RiskLevels: map[string]RiskLevel{"echo": RiskLow},
			},
		},
	}
	registry, err := NewRegistry(configuration)
	if err != nil {
		t.Fatalf("NewRegistry(): %v", err)
	}
	defer registry.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := registry.Initialize(ctx); err != nil {
		t.Fatalf("Initialize(): %v", err)
	}
	tools := registry.ListTools()
	if len(tools) != 1 || tools[0].Name != "test.echo" {
		t.Fatalf("tools = %#v", tools)
	}
	result, err := registry.Invoke(ctx, InvokeRequest{
		UserName: "alice", ToolName: "test.echo", Arguments: map[string]any{"text": "hello hub"}, OperationID: "operation-123",
	})
	if err != nil {
		t.Fatalf("Invoke(): %v", err)
	}
	if result.Result == nil || result.Result.IsError || len(result.Result.Content) != 1 {
		t.Fatalf("result = %#v", result)
	}
	text, ok := result.Result.Content[0].(mcp.TextContent)
	if !ok || text.Text != "hello hub" {
		t.Fatalf("content = %#v", result.Result.Content)
	}
	if operationID != "operation-123" || result.OperationID != operationID {
		t.Fatalf("MCP operation metadata = %q, invocation operation ID = %q", operationID, result.OperationID)
	}
}
