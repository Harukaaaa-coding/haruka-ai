package mcphub

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

type fakeProtocolClient struct {
	mu             sync.Mutex
	tools          []mcp.Tool
	call           func(context.Context, string, map[string]any, *mcp.Meta) (*mcp.CallToolResult, error)
	initializeCall int
	listCall       int
	callCount      int
	closed         bool
}

func (client *fakeProtocolClient) Initialize(context.Context) (ProtocolInfo, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.initializeCall++
	return ProtocolInfo{ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION, ServerName: "fake", ServerVersion: "1"}, nil
}

func (client *fakeProtocolClient) ListTools(context.Context) ([]mcp.Tool, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.listCall++
	return append([]mcp.Tool(nil), client.tools...), nil
}

func (client *fakeProtocolClient) CallTool(ctx context.Context, name string, arguments map[string]any, metadata *mcp.Meta) (*mcp.CallToolResult, error) {
	client.mu.Lock()
	client.callCount++
	call := client.call
	client.mu.Unlock()
	if call == nil {
		return mcp.NewToolResultText("ok"), nil
	}
	return call(ctx, name, arguments, metadata)
}

func (client *fakeProtocolClient) Close() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.closed = true
	return nil
}

func (client *fakeProtocolClient) calls() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.callCount
}

type memoryAuditSink struct {
	mu     sync.Mutex
	events []AuditEvent
}

func (sink *memoryAuditSink) RecordMCPAudit(_ context.Context, event AuditEvent) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.events = append(sink.events, event)
	return nil
}

func (sink *memoryAuditSink) last() AuditEvent {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.events[len(sink.events)-1]
}

func testRegistryConfig() Config {
	return Config{
		ServiceAllowlist: []string{"demo"},
		Servers: []ServerConfig{
			{
				ID:                 "demo",
				URL:                "http://127.0.0.1:9999/mcp",
				Enabled:            true,
				TimeoutMS:          500,
				ToolAllowlist:      []string{"read", "write"},
				ReadOnlyTools:      []string{"read"},
				RiskLevels:         map[string]RiskLevel{"read": RiskLow, "write": RiskMedium},
				MaxReadOnlyRetries: 1,
			},
		},
	}
}

func testTools() []mcp.Tool {
	return []mcp.Tool{
		mcp.NewTool("read", mcp.WithString("query", mcp.Required()), mcp.WithReadOnlyHintAnnotation(true)),
		mcp.NewTool("write", mcp.WithString("value", mcp.Required())),
		mcp.NewTool("hidden", mcp.WithString("value")),
	}
}

func newTestRegistry(t *testing.T, fake *fakeProtocolClient, audit AuditSink) *Registry {
	t.Helper()
	registry, err := NewRegistry(
		testRegistryConfig(),
		WithProtocolClientFactory(func(ServerConfig, func()) (ProtocolClient, error) { return fake, nil }),
		WithAuditSink(audit),
	)
	if err != nil {
		t.Fatalf("NewRegistry(): %v", err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	return registry
}

func TestRegistryDiscoversNamespacedAllowlistedTools(t *testing.T) {
	fake := &fakeProtocolClient{tools: testTools()}
	registry := newTestRegistry(t, fake, &memoryAuditSink{})
	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize(): %v", err)
	}
	tools := registry.ListTools()
	if len(tools) != 2 || tools[0].Name != "demo.read" || tools[1].Name != "demo.write" {
		t.Fatalf("tools = %#v", tools)
	}
	if _, err := registry.Tool("demo.hidden"); ErrorCode(err) != ErrorToolNotAllowed {
		t.Fatalf("hidden tool error = %v", err)
	}
	if infos, err := registry.EinoToolInfos(); err != nil || len(infos) != 2 {
		t.Fatalf("EinoToolInfos() = %#v, %v", infos, err)
	}
}

func TestRegistryRetriesOnlyReadOnlyTool(t *testing.T) {
	var readCalls int
	fake := &fakeProtocolClient{tools: testTools()}
	fake.call = func(_ context.Context, name string, _ map[string]any, _ *mcp.Meta) (*mcp.CallToolResult, error) {
		if name == "read" {
			readCalls++
			if readCalls == 1 {
				return nil, errors.New("temporary transport failure")
			}
			return mcp.NewToolResultText("read ok"), nil
		}
		return nil, errors.New("write transport failure")
	}
	audit := &memoryAuditSink{}
	registry := newTestRegistry(t, fake, audit)
	result, err := registry.Invoke(context.Background(), InvokeRequest{
		UserName: "alice", ToolName: "demo.read", Arguments: map[string]any{"query": "status", "api_key": "private-value"},
	})
	if err != nil {
		t.Fatalf("Invoke(read): %v", err)
	}
	if result.Attempts != 2 || readCalls != 2 {
		t.Fatalf("read attempts = %d, calls = %d", result.Attempts, readCalls)
	}
	event := audit.last()
	if event.Outcome != "success" || strings.Contains(event.ArgumentsPreview, "private-value") || !strings.Contains(event.ArgumentsPreview, "[REDACTED]") {
		t.Fatalf("audit event = %#v", event)
	}

	challenge, err := registry.CreateApproval(context.Background(), "alice", "demo.write", map[string]any{"value": "change"})
	if err != nil {
		t.Fatalf("CreateApproval(write): %v", err)
	}
	issued, err := registry.Approve("alice", challenge.ID)
	if err != nil {
		t.Fatalf("Approve(write): %v", err)
	}
	beforeWrite := fake.calls()
	_, err = registry.Invoke(context.Background(), InvokeRequest{
		UserName: "alice", ToolName: "demo.write", Arguments: map[string]any{"value": "change"}, ApprovalToken: issued.Token,
	})
	if ErrorCode(err) != ErrorProtocol {
		t.Fatalf("Invoke(write) = %v", err)
	}
	if writeCalls := fake.calls() - beforeWrite; writeCalls != 1 {
		t.Fatalf("write calls = %d, want 1", writeCalls)
	}
}

func TestRegistryApprovalRequiredAndTokenCannotBeReused(t *testing.T) {
	fake := &fakeProtocolClient{tools: testTools()}
	audit := &memoryAuditSink{}
	registry := newTestRegistry(t, fake, audit)
	arguments := map[string]any{"value": "change"}
	_, err := registry.Invoke(context.Background(), InvokeRequest{UserName: "alice", ToolName: "demo.write", Arguments: arguments})
	if ErrorCode(err) != ErrorApprovalRequired || fake.calls() != 0 {
		t.Fatalf("unapproved Invoke() = %v, calls=%d", err, fake.calls())
	}
	var required *ApprovalRequiredError
	if !errors.As(err, &required) || required.ToolName != "demo.write" {
		t.Fatalf("approval error metadata = %#v", required)
	}

	challenge, _ := registry.CreateApproval(context.Background(), "alice", "demo.write", arguments)
	issued, _ := registry.Approve("alice", challenge.ID)
	if _, err := registry.Invoke(context.Background(), InvokeRequest{
		UserName: "alice", ToolName: "demo.write", Arguments: arguments, ApprovalToken: issued.Token,
	}); err != nil {
		t.Fatalf("approved Invoke(): %v", err)
	}
	if _, err := registry.Invoke(context.Background(), InvokeRequest{
		UserName: "alice", ToolName: "demo.write", Arguments: arguments, ApprovalToken: issued.Token,
	}); ErrorCode(err) != ErrorApprovalInvalid {
		t.Fatalf("reused token Invoke() = %v", err)
	}
	if fake.calls() != 1 {
		t.Fatalf("upstream calls = %d, want 1", fake.calls())
	}
}

func TestRegistryValidatesBeforeCallingAndAppliesTimeout(t *testing.T) {
	fake := &fakeProtocolClient{tools: testTools()}
	fake.call = func(ctx context.Context, _ string, _ map[string]any, _ *mcp.Meta) (*mcp.CallToolResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	configuration := testRegistryConfig()
	configuration.Servers[0].TimeoutMS = 100
	registry, err := NewRegistry(
		configuration,
		WithProtocolClientFactory(func(ServerConfig, func()) (ProtocolClient, error) { return fake, nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()

	_, err = registry.Invoke(context.Background(), InvokeRequest{
		UserName: "alice", ToolName: "demo.read", Arguments: map[string]any{},
	})
	if ErrorCode(err) != ErrorInvalidArguments || fake.calls() != 0 {
		t.Fatalf("invalid Invoke() = %v, calls=%d", err, fake.calls())
	}
	started := time.Now()
	_, err = registry.Invoke(context.Background(), InvokeRequest{
		UserName: "alice", ToolName: "demo.read", Arguments: map[string]any{"query": "slow"},
	})
	if ErrorCode(err) != ErrorTimeout {
		t.Fatalf("timeout Invoke() = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}
}
