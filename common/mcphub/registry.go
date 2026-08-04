package mcphub

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
)

type RegistryOption func(*Registry)

func WithProtocolClientFactory(factory ProtocolClientFactory) RegistryOption {
	return func(registry *Registry) {
		if factory != nil {
			registry.clientFactory = factory
		}
	}
}

func WithAuditSink(sink AuditSink) RegistryOption {
	return func(registry *Registry) {
		if sink != nil {
			registry.auditSink = sink
		}
	}
}

func WithEnvLookup(lookup EnvLookup) RegistryOption {
	return func(registry *Registry) {
		if lookup != nil {
			registry.lookup = lookup
		}
	}
}

func WithApprovalManager(manager *ApprovalManager) RegistryOption {
	return func(registry *Registry) {
		if manager != nil {
			registry.approvals = manager
		}
	}
}

type serverRuntime struct {
	mu     sync.Mutex
	config ServerConfig
	client ProtocolClient
	status ServerStatus
}

type Registry struct {
	mu            sync.RWMutex
	config        Config
	servers       map[string]*serverRuntime
	tools         map[string]ToolDefinition
	lookup        EnvLookup
	clientFactory ProtocolClientFactory
	auditSink     AuditSink
	approvals     *ApprovalManager
	closed        bool
}

func NewRegistry(config Config, options ...RegistryOption) (*Registry, error) {
	if err := ValidateConfig(&config); err != nil {
		return nil, err
	}
	registry := &Registry{
		lookup:    os.LookupEnv,
		auditSink: noopAuditSink{},
		servers:   make(map[string]*serverRuntime),
		tools:     make(map[string]ToolDefinition),
	}
	for _, option := range options {
		option(registry)
	}
	if registry.clientFactory == nil {
		registry.clientFactory = DefaultProtocolClientFactory(registry.lookup)
	}
	if registry.approvals == nil {
		registry.approvals = NewApprovalManager(config.ApprovalTTL())
	}
	if err := registry.applyConfig(config, false); err != nil {
		return nil, err
	}
	return registry, nil
}

// ApplyConfig replaces the registry definition without accepting credential
// values. Unchanged clients are retained; removed or changed clients are
// closed after the new allowlist becomes active.
func (registry *Registry) ApplyConfig(config Config) error {
	if err := ValidateConfig(&config); err != nil {
		return err
	}
	return registry.applyConfig(config, true)
}

func (registry *Registry) applyConfig(config Config, closeChanged bool) error {
	registry.mu.Lock()
	if registry.closed {
		registry.mu.Unlock()
		return newError(ErrorInternal, "MCP registry is closed", nil)
	}

	oldServers := registry.servers
	newServers := make(map[string]*serverRuntime, len(config.Servers))
	retained := make(map[string]struct{})
	for _, server := range config.Servers {
		allowed := config.serverAllowed(server.ID)
		oldAllowed := registry.config.serverAllowed(server.ID)
		if existing, ok := oldServers[server.ID]; ok && oldAllowed == allowed && reflect.DeepEqual(existing.config, server) {
			newServers[server.ID] = existing
			retained[server.ID] = struct{}{}
			continue
		}
		state := ServerDisabled
		if server.Enabled && !allowed {
			state = ServerBlocked
		} else if server.Enabled && allowed {
			state = ServerConnecting
		}
		newServers[server.ID] = &serverRuntime{
			config: server,
			status: ServerStatus{
				ID:                   server.ID,
				URL:                  publicServerURL(server.URL),
				Enabled:              server.Enabled,
				Allowed:              allowed,
				State:                state,
				TimeoutMS:            server.TimeoutMS,
				CredentialEnv:        credentialEnv(server),
				CredentialConfigured: registry.credentialConfigured(server),
			},
		}
	}

	registry.config = config
	registry.servers = newServers
	for toolName, tool := range registry.tools {
		if _, exists := newServers[tool.ServerID]; !exists {
			delete(registry.tools, toolName)
			continue
		}
		if _, keep := retained[tool.ServerID]; !keep {
			delete(registry.tools, toolName)
		}
	}
	registry.mu.Unlock()

	if closeChanged {
		for id, runtime := range oldServers {
			if _, keep := retained[id]; keep {
				continue
			}
			runtime.close()
		}
	}
	return nil
}

func (registry *Registry) Initialize(ctx context.Context) error {
	registry.mu.RLock()
	if registry.closed {
		registry.mu.RUnlock()
		return newError(ErrorInternal, "MCP registry is closed", nil)
	}
	runtimes := make([]*serverRuntime, 0, len(registry.servers))
	for _, runtime := range registry.servers {
		if runtime.config.Enabled && registry.config.serverAllowed(runtime.config.ID) {
			runtimes = append(runtimes, runtime)
		}
	}
	registry.mu.RUnlock()

	var wait sync.WaitGroup
	errChannel := make(chan error, len(runtimes))
	for _, runtime := range runtimes {
		wait.Add(1)
		go func(current *serverRuntime) {
			defer wait.Done()
			if err := registry.connectAndDiscover(ctx, current, false); err != nil {
				errChannel <- err
			}
		}(runtime)
	}
	wait.Wait()
	close(errChannel)
	errorsFound := make([]error, 0, len(errChannel))
	for err := range errChannel {
		errorsFound = append(errorsFound, err)
	}
	return errors.Join(errorsFound...)
}

func (registry *Registry) Refresh(ctx context.Context, serverID string) error {
	runtime, err := registry.runtime(serverID)
	if err != nil {
		return err
	}
	return registry.connectAndDiscover(ctx, runtime, true)
}

func (registry *Registry) RefreshAll(ctx context.Context) error {
	registry.mu.RLock()
	ids := make([]string, 0, len(registry.servers))
	for id, runtime := range registry.servers {
		if runtime.config.Enabled && registry.config.serverAllowed(id) {
			ids = append(ids, id)
		}
	}
	registry.mu.RUnlock()
	sort.Strings(ids)
	errorsFound := make([]error, 0)
	for _, id := range ids {
		if err := registry.Refresh(ctx, id); err != nil {
			errorsFound = append(errorsFound, err)
		}
	}
	return errors.Join(errorsFound...)
}

func (registry *Registry) connectAndDiscover(parent context.Context, runtime *serverRuntime, force bool) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()

	server := runtime.config
	registry.mu.RLock()
	allowed := registry.config.serverAllowed(server.ID)
	closed := registry.closed
	registry.mu.RUnlock()
	if closed {
		return newError(ErrorInternal, "MCP registry is closed", nil)
	}
	if !server.Enabled {
		runtime.status.State = ServerDisabled
		return newError(ErrorServerUnavailable, "MCP server is disabled", nil)
	}
	if !allowed {
		runtime.status.State = ServerBlocked
		return newError(ErrorServerNotAllowed, "MCP server is not allowlisted", nil)
	}
	if runtime.client != nil && !force && (runtime.status.State == ServerReady || runtime.status.State == ServerDegraded) {
		return nil
	}

	runtime.status.State = ServerConnecting
	runtime.status.LastError = ""
	runtime.status.LastErrorCode = ""
	ctx, cancel := context.WithTimeout(parent, server.Timeout())
	defer cancel()

	created := false
	if runtime.client == nil {
		client, err := registry.clientFactory(server, func() {
			go registry.refreshFromNotification(server.ID)
		})
		if err != nil {
			registry.recordServerError(runtime, err)
			return err
		}
		runtime.client = client
		created = true
		info, err := client.Initialize(ctx)
		if err != nil {
			_ = client.Close()
			runtime.client = nil
			hubErr := newError(ErrorServerUnavailable, "MCP server initialization failed", err)
			registry.recordServerError(runtime, hubErr)
			return hubErr
		}
		runtime.status.Protocol = info
	}

	upstreamTools, err := runtime.client.ListTools(ctx)
	if err != nil {
		if created {
			_ = runtime.client.Close()
			runtime.client = nil
		}
		hubErr := classifyProtocolError("MCP tools/list failed", err)
		registry.recordServerError(runtime, hubErr)
		return hubErr
	}

	tools := make([]ToolDefinition, 0, len(upstreamTools))
	conversionErrors := make([]error, 0)
	for _, upstream := range upstreamTools {
		if !server.toolAllowed(upstream.Name) {
			continue
		}
		converted, convertErr := convertTool(server, upstream)
		if convertErr != nil {
			conversionErrors = append(conversionErrors, fmt.Errorf("tool %s schema is invalid: %w", upstream.Name, convertErr))
			continue
		}
		tools = append(tools, converted)
	}
	sort.Slice(tools, func(left, right int) bool { return tools[left].Name < tools[right].Name })
	registry.replaceServerTools(server.ID, tools)
	now := time.Now().UTC()
	runtime.status.LastRefreshAt = &now
	runtime.status.ToolCount = len(tools)
	runtime.status.CredentialConfigured = registry.credentialConfigured(server)
	if len(conversionErrors) > 0 {
		runtime.status.State = ServerDegraded
		runtime.status.LastErrorCode = ErrorProtocol
		runtime.status.LastError = "one or more allowlisted tool schemas were rejected"
		return newError(ErrorProtocol, runtime.status.LastError, errors.Join(conversionErrors...))
	}
	runtime.status.State = ServerReady
	return nil
}

func (registry *Registry) refreshFromNotification(serverID string) {
	runtime, err := registry.runtime(serverID)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), runtime.config.Timeout())
	defer cancel()
	_ = registry.connectAndDiscover(ctx, runtime, true)
}

func (registry *Registry) replaceServerTools(serverID string, tools []ToolDefinition) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for name, existing := range registry.tools {
		if existing.ServerID == serverID {
			delete(registry.tools, name)
		}
	}
	for _, tool := range tools {
		registry.tools[tool.Name] = tool
	}
}

func (registry *Registry) recordServerError(runtime *serverRuntime, err error) {
	runtime.status.State = ServerError
	runtime.status.LastErrorCode = ErrorCode(err)
	runtime.status.LastError = safeError(err)
}

func (registry *Registry) ListServers() []ServerStatus {
	registry.mu.RLock()
	runtimes := make([]*serverRuntime, 0, len(registry.servers))
	for _, runtime := range registry.servers {
		runtimes = append(runtimes, runtime)
	}
	registry.mu.RUnlock()
	statuses := make([]ServerStatus, 0, len(runtimes))
	for _, runtime := range runtimes {
		runtime.mu.Lock()
		status := runtime.status
		runtime.mu.Unlock()
		statuses = append(statuses, status)
	}
	sort.Slice(statuses, func(left, right int) bool { return statuses[left].ID < statuses[right].ID })
	return statuses
}

func (registry *Registry) Server(serverID string) (ServerStatus, error) {
	runtime, err := registry.runtime(serverID)
	if err != nil {
		return ServerStatus{}, err
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.status, nil
}

func (registry *Registry) ListTools() []ToolDefinition {
	registry.mu.RLock()
	tools := make([]ToolDefinition, 0, len(registry.tools))
	for _, tool := range registry.tools {
		tools = append(tools, cloneToolDefinition(tool))
	}
	registry.mu.RUnlock()
	sort.Slice(tools, func(left, right int) bool { return tools[left].Name < tools[right].Name })
	return tools
}

func (registry *Registry) EinoToolInfos() ([]*einoschema.ToolInfo, error) {
	definitions := registry.ListTools()
	tools := make([]*einoschema.ToolInfo, 0, len(definitions))
	for _, definition := range definitions {
		converted, err := definition.EinoToolInfo()
		if err != nil {
			return nil, err
		}
		tools = append(tools, converted)
	}
	return tools, nil
}

func (registry *Registry) Tool(name string) (ToolDefinition, error) {
	serverID, upstreamName, valid := splitNamespacedTool(name)
	if !valid {
		return ToolDefinition{}, newError(ErrorToolNotFound, "tool name must use serverID.toolName", nil)
	}
	registry.mu.RLock()
	runtime, serverExists := registry.servers[serverID]
	tool, toolExists := registry.tools[name]
	allowed := registry.config.serverAllowed(serverID)
	registry.mu.RUnlock()
	if !serverExists || !allowed {
		return ToolDefinition{}, newError(ErrorServerNotAllowed, "MCP server is not allowlisted", nil)
	}
	if !runtime.config.toolAllowed(upstreamName) {
		return ToolDefinition{}, newError(ErrorToolNotAllowed, "MCP tool is not allowlisted", nil)
	}
	if !toolExists {
		return ToolDefinition{}, newError(ErrorToolNotFound, "MCP tool was not discovered", nil)
	}
	return cloneToolDefinition(tool), nil
}

func (registry *Registry) CreateApproval(ctx context.Context, userName, toolName string, arguments map[string]any) (*ApprovalChallenge, error) {
	tool, err := registry.ensureTool(ctx, toolName)
	if err != nil {
		return nil, err
	}
	return registry.approvals.Create(userName, tool, arguments)
}

func (registry *Registry) Approve(userName, challengeID string) (*IssuedApproval, error) {
	return registry.approvals.Approve(userName, challengeID)
}

func (registry *Registry) Invoke(ctx context.Context, request InvokeRequest) (result *InvocationResult, returnedErr error) {
	startedAt := time.Now()
	requestID := uuid.NewString()
	serverID, _, _ := splitNamespacedTool(request.ToolName)
	event := AuditEvent{
		RequestID: requestID,
		UserName:  request.UserName,
		ServerID:  serverID,
		ToolName:  request.ToolName,
		Outcome:   "rejected",
		CreatedAt: startedAt.UTC(),
	}
	if digest, err := argumentsDigest(request.Arguments); err == nil {
		event.ArgumentsDigest = digest
	}
	event.ArgumentsPreview = string(redactedArguments(request.Arguments))
	defer func() {
		event.DurationMS = time.Since(startedAt).Milliseconds()
		if returnedErr != nil {
			event.ErrorCode = ErrorCode(returnedErr)
			event.ErrorMessage = safeError(returnedErr)
		}
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = registry.auditSink.RecordMCPAudit(auditCtx, event)
	}()

	if strings.TrimSpace(request.UserName) == "" {
		return nil, newError(ErrorApprovalInvalid, "tool invocation requires an authenticated user", nil)
	}
	tool, err := registry.ensureTool(ctx, request.ToolName)
	if err != nil {
		return nil, err
	}
	event.ServerID = tool.ServerID
	event.Risk = tool.Risk
	if err := tool.ValidateArguments(request.Arguments); err != nil {
		return nil, err
	}
	operationID := strings.TrimSpace(request.OperationID)
	if len(operationID) > 64 {
		return nil, newError(ErrorInvalidArguments, "operation ID is too long", nil)
	}
	if tool.RequiresApproval {
		if request.ApprovalToken == "" {
			required := &ApprovalRequiredError{ToolName: tool.Name, Risk: tool.Risk}
			return nil, newError(ErrorApprovalRequired, required.Error(), required)
		}
		if err := registry.approvals.Consume(request.UserName, tool.Name, request.Arguments, request.ApprovalToken); err != nil {
			return nil, err
		}
		event.ApprovalUsed = true
	}

	runtime, err := registry.runtime(tool.ServerID)
	if err != nil {
		return nil, err
	}
	callResult, attempts, err := registry.invokeUpstream(ctx, runtime, tool, request.Arguments, operationID)
	event.Attempts = attempts
	event.ResultSummary = summarizeResult(callResult)
	if err != nil {
		event.Outcome = outcomeForError(err)
		return nil, err
	}
	if callResult == nil {
		event.Outcome = "protocol_error"
		return nil, newError(ErrorProtocol, "MCP tool returned no result", nil)
	}
	if callResult.IsError {
		event.Outcome = "tool_error"
	} else {
		event.Outcome = "success"
	}
	duration := time.Since(startedAt).Milliseconds()
	return &InvocationResult{
		RequestID:   requestID,
		OperationID: operationID,
		ToolName:    tool.Name,
		ServerID:    tool.ServerID,
		Risk:        tool.Risk,
		Attempts:    attempts,
		DurationMS:  duration,
		Result:      callResult,
	}, nil
}

func (registry *Registry) invokeUpstream(parent context.Context, runtime *serverRuntime, tool ToolDefinition, arguments map[string]any, operationID string) (*mcp.CallToolResult, int, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.client == nil {
		return nil, 0, newError(ErrorServerUnavailable, "MCP server is not initialized", nil)
	}
	ctx, cancel := context.WithTimeout(parent, runtime.config.Timeout())
	defer cancel()

	maxAttempts := 1
	if tool.ReadOnly {
		maxAttempts += runtime.config.MaxReadOnlyRetries
	}
	var lastErr error
	var metadata *mcp.Meta
	if operationID != "" {
		metadata = mcp.NewMetaFromMap(map[string]any{"gopherai/operation_id": operationID})
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, err := runtime.client.CallTool(ctx, tool.UpstreamName, arguments, metadata)
		if err == nil {
			return result, attempt, nil
		}
		lastErr = err
		if attempt == maxAttempts || !tool.ReadOnly || !retryableProtocolError(err) {
			return nil, attempt, classifyProtocolError("MCP tool invocation failed", err)
		}
		backoff := time.Duration(25*(1<<(attempt-1))) * time.Millisecond
		select {
		case <-ctx.Done():
			return nil, attempt, classifyProtocolError("MCP tool invocation failed", ctx.Err())
		case <-time.After(backoff):
		}
	}
	return nil, maxAttempts, classifyProtocolError("MCP tool invocation failed", lastErr)
}

func (registry *Registry) ensureTool(ctx context.Context, name string) (ToolDefinition, error) {
	tool, err := registry.Tool(name)
	if err == nil {
		return tool, nil
	}
	if ErrorCode(err) != ErrorToolNotFound {
		return ToolDefinition{}, err
	}
	serverID, _, valid := splitNamespacedTool(name)
	if !valid {
		return ToolDefinition{}, err
	}
	if refreshErr := registry.Refresh(ctx, serverID); refreshErr != nil {
		return ToolDefinition{}, refreshErr
	}
	return registry.Tool(name)
}

func (registry *Registry) runtime(serverID string) (*serverRuntime, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	if registry.closed {
		return nil, newError(ErrorInternal, "MCP registry is closed", nil)
	}
	runtime, exists := registry.servers[serverID]
	if !exists {
		return nil, newError(ErrorServerNotAllowed, "MCP server is not configured", nil)
	}
	if !registry.config.serverAllowed(serverID) {
		return nil, newError(ErrorServerNotAllowed, "MCP server is not allowlisted", nil)
	}
	return runtime, nil
}

func (registry *Registry) Close() error {
	registry.mu.Lock()
	if registry.closed {
		registry.mu.Unlock()
		return nil
	}
	registry.closed = true
	runtimes := make([]*serverRuntime, 0, len(registry.servers))
	for _, runtime := range registry.servers {
		runtimes = append(runtimes, runtime)
	}
	registry.tools = make(map[string]ToolDefinition)
	registry.mu.Unlock()
	errorsFound := make([]error, 0)
	for _, runtime := range runtimes {
		if err := runtime.close(); err != nil {
			errorsFound = append(errorsFound, err)
		}
	}
	return errors.Join(errorsFound...)
}

func (runtime *serverRuntime) close() error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.status.State = ServerClosed
	if runtime.client == nil {
		return nil
	}
	err := runtime.client.Close()
	runtime.client = nil
	return err
}

func (registry *Registry) credentialConfigured(server ServerConfig) bool {
	if server.Credential == nil {
		return true
	}
	value, ok := registry.lookup(server.Credential.Env)
	return ok && value != "" && !strings.ContainsAny(value, "\r\n")
}

func credentialEnv(server ServerConfig) string {
	if server.Credential == nil {
		return ""
	}
	return server.Credential.Env
}

func publicServerURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func splitNamespacedTool(name string) (serverID, upstreamName string, valid bool) {
	serverID, upstreamName, valid = strings.Cut(strings.TrimSpace(name), ".")
	if !valid || validateServerID(serverID) != nil || validateToolName(upstreamName) != nil {
		return "", "", false
	}
	return serverID, upstreamName, true
}

func cloneToolDefinition(tool ToolDefinition) ToolDefinition {
	tool.InputSchema = append([]byte(nil), tool.InputSchema...)
	tool.OutputSchema = append([]byte(nil), tool.OutputSchema...)
	return tool
}

func retryableProtocolError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return true
	}
	// MCP protocol/transport errors occur before a CallToolResult is received.
	// Retrying remains restricted by the explicit read-only policy boundary.
	return true
}

func classifyProtocolError(message string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return newError(ErrorTimeout, message+" due to timeout", err)
	}
	if errors.Is(err, context.Canceled) {
		return newError(ErrorCanceled, message+" because the request was canceled", err)
	}
	return newError(ErrorProtocol, message, err)
}

func outcomeForError(err error) string {
	switch ErrorCode(err) {
	case ErrorTimeout:
		return "timeout"
	case ErrorCanceled:
		return "canceled"
	case ErrorApprovalRequired, ErrorApprovalInvalid, ErrorInvalidArguments, ErrorToolNotAllowed, ErrorToolNotFound, ErrorServerNotAllowed:
		return "rejected"
	default:
		return "protocol_error"
	}
}
