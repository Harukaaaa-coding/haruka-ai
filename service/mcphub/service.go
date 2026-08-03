package mcphub

import (
	hub "GopherAI/common/mcphub"
	"GopherAI/dao/mcpaudit"
	"GopherAI/model"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	einoschema "github.com/cloudwego/eino/schema"
)

const (
	defaultAuditLimit = 50
	maxAuditLimit     = 200
)

type Service struct {
	registry   *hub.Registry
	configPath string
}

func New(configPath string, options ...hub.RegistryOption) (*Service, error) {
	configuration, err := hub.LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	options = append(options, hub.WithAuditSink(databaseAuditSink{}))
	registry, err := hub.NewRegistry(configuration, options...)
	if err != nil {
		return nil, err
	}
	return &Service{registry: registry, configPath: configPath}, nil
}

func NewWithRegistry(registry *hub.Registry) (*Service, error) {
	if registry == nil {
		return nil, fmt.Errorf("MCP registry is required")
	}
	return &Service{registry: registry}, nil
}

var (
	defaultService     *Service
	defaultServiceErr  error
	defaultServiceOnce sync.Once
)

func Default() (*Service, error) {
	defaultServiceOnce.Do(func() {
		path := hub.RegistryPath(os.LookupEnv)
		defaultService, defaultServiceErr = New(path)
	})
	return defaultService, defaultServiceErr
}

// DefaultRegistry is the stable integration point for model orchestration.
// It exposes the policy-enforcing registry, never controller or DAO details.
func DefaultRegistry() (*hub.Registry, error) {
	service, err := Default()
	if err != nil {
		return nil, err
	}
	return service.registry, nil
}

// ListTools is a convenience entry point for chat-model tool binding.
func ListTools(ctx context.Context) ([]hub.ToolDefinition, error) {
	service, err := Default()
	if err != nil {
		return nil, err
	}
	return service.Tools(ctx, false)
}

// AutomaticTools returns only tools that the chat model may execute without a
// human approval round trip.
func AutomaticTools(ctx context.Context) ([]hub.ToolDefinition, error) {
	tools, err := ListTools(ctx)
	if err != nil {
		return nil, err
	}
	allowed := make([]hub.ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		if tool.Risk == hub.RiskLow && !tool.RequiresApproval {
			allowed = append(allowed, tool)
		}
	}
	return allowed, nil
}

// AutomaticEinoTools is the preferred chat-model binding entry point.
func AutomaticEinoTools(ctx context.Context) ([]*einoschema.ToolInfo, error) {
	tools, err := AutomaticTools(ctx)
	if err != nil {
		return nil, err
	}
	converted := make([]*einoschema.ToolInfo, 0, len(tools))
	for _, tool := range tools {
		info, convertErr := tool.EinoToolInfo()
		if convertErr != nil {
			return nil, convertErr
		}
		converted = append(converted, info)
	}
	return converted, nil
}

// Invoke routes a namespaced tool call through allowlists, schema validation,
// approval enforcement, timeout/retry policy, and audit recording. Therefore a
// caller without an approval token can only execute tools that do not require
// approval (the intended automatic-chat boundary).
func Invoke(ctx context.Context, request hub.InvokeRequest) (*hub.InvocationResult, error) {
	service, err := Default()
	if err != nil {
		return nil, err
	}
	return service.Call(ctx, request)
}

// InvokeAutomatic is the preferred chat-model invocation entry point. The
// explicit pre-check is defense in depth on top of Registry.Invoke's approval
// enforcement.
func InvokeAutomatic(ctx context.Context, userName, toolName string, arguments map[string]any) (*hub.InvocationResult, error) {
	service, err := Default()
	if err != nil {
		return nil, err
	}
	if _, err := service.Tools(ctx, false); err != nil {
		return nil, err
	}
	tool, err := service.registry.Tool(toolName)
	if err != nil {
		return nil, err
	}
	if tool.Risk != hub.RiskLow || tool.RequiresApproval {
		required := &hub.ApprovalRequiredError{ToolName: tool.Name, Risk: tool.Risk}
		return nil, &hub.Error{Code: hub.ErrorApprovalRequired, Message: required.Error(), Cause: required}
	}
	return service.Call(ctx, hub.InvokeRequest{
		UserName:  userName,
		ToolName:  toolName,
		Arguments: arguments,
	})
}

func (service *Service) Initialize(ctx context.Context) error {
	return service.registry.Initialize(ctx)
}

func (service *Service) Servers(ctx context.Context) []hub.ServerStatus {
	_ = service.registry.Initialize(ctx)
	return service.registry.ListServers()
}

func (service *Service) Reload(ctx context.Context) ([]hub.ServerStatus, error) {
	if strings.TrimSpace(service.configPath) == "" {
		return nil, fmt.Errorf("MCP registry reload is unavailable")
	}
	configuration, err := hub.LoadConfig(service.configPath)
	if err != nil {
		return nil, err
	}
	if err := service.registry.ApplyConfig(configuration); err != nil {
		return nil, err
	}
	initializeErr := service.registry.Initialize(ctx)
	return service.registry.ListServers(), initializeErr
}

func (service *Service) Refresh(ctx context.Context, serverID string) (hub.ServerStatus, error) {
	if err := service.registry.Refresh(ctx, serverID); err != nil {
		status, statusErr := service.registry.Server(serverID)
		if statusErr == nil {
			return status, err
		}
		return hub.ServerStatus{}, err
	}
	return service.registry.Server(serverID)
}

func (service *Service) Tools(ctx context.Context, refresh bool) ([]hub.ToolDefinition, error) {
	var err error
	if refresh {
		err = service.registry.RefreshAll(ctx)
	} else {
		err = service.registry.Initialize(ctx)
	}
	tools := service.registry.ListTools()
	if err != nil && len(tools) == 0 {
		return nil, err
	}
	return tools, nil
}

func (service *Service) EinoTools(ctx context.Context) ([]*einoschema.ToolInfo, error) {
	if err := service.registry.Initialize(ctx); err != nil && len(service.registry.ListTools()) == 0 {
		return nil, err
	}
	return service.registry.EinoToolInfos()
}

func (service *Service) Call(ctx context.Context, request hub.InvokeRequest) (*hub.InvocationResult, error) {
	return service.registry.Invoke(ctx, request)
}

func (service *Service) CreateApproval(ctx context.Context, userName, toolName string, arguments map[string]any) (*hub.ApprovalChallenge, error) {
	return service.registry.CreateApproval(ctx, userName, toolName, arguments)
}

func (service *Service) Approve(userName, challengeID string) (*hub.IssuedApproval, error) {
	return service.registry.Approve(userName, challengeID)
}

func (service *Service) Audits(ctx context.Context, query mcpaudit.Query) ([]model.MCPAuditRecord, int64, error) {
	query.UserName = strings.TrimSpace(query.UserName)
	if query.UserName == "" {
		return nil, 0, fmt.Errorf("authenticated username is required")
	}
	if query.Limit <= 0 {
		query.Limit = defaultAuditLimit
	}
	if query.Limit > maxAuditLimit {
		query.Limit = maxAuditLimit
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	return mcpaudit.List(ctx, query)
}

func (service *Service) Close() error {
	return service.registry.Close()
}

func CloseDefault() error {
	if defaultService == nil {
		return nil
	}
	return defaultService.Close()
}

type databaseAuditSink struct{}

func (databaseAuditSink) RecordMCPAudit(ctx context.Context, event hub.AuditEvent) error {
	record := &model.MCPAuditRecord{
		RequestID:        event.RequestID,
		UserName:         event.UserName,
		ServerID:         event.ServerID,
		ToolName:         event.ToolName,
		RiskLevel:        string(event.Risk),
		Outcome:          event.Outcome,
		ApprovalUsed:     event.ApprovalUsed,
		Attempts:         event.Attempts,
		DurationMS:       event.DurationMS,
		ArgumentsDigest:  event.ArgumentsDigest,
		ArgumentsPreview: event.ArgumentsPreview,
		ResultSummary:    event.ResultSummary,
		ErrorCode:        event.ErrorCode,
		ErrorMessage:     event.ErrorMessage,
		CreatedAt:        event.CreatedAt,
	}
	return mcpaudit.Create(ctx, record)
}
