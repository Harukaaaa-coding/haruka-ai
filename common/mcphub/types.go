package mcphub

import (
	"context"
	"encoding/json"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// RiskLevel describes the potential side effects of an MCP tool. The ordering
// is intentional and is used when deriving approval requirements.
type RiskLevel string

const (
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

func (r RiskLevel) valid() bool {
	switch r {
	case RiskLow, RiskMedium, RiskHigh, RiskCritical:
		return true
	default:
		return false
	}
}

func (r RiskLevel) rank() int {
	switch r {
	case RiskLow:
		return 1
	case RiskMedium:
		return 2
	case RiskHigh:
		return 3
	case RiskCritical:
		return 4
	default:
		return 0
	}
}

func maxRisk(left, right RiskLevel) RiskLevel {
	if right.rank() > left.rank() {
		return right
	}
	return left
}

// RequiresApproval deliberately defaults to approval for every tool that is
// not explicitly classified as low risk.
func (r RiskLevel) RequiresApproval() bool {
	return r.rank() >= RiskMedium.rank()
}

type ServerState string

const (
	ServerDisabled   ServerState = "disabled"
	ServerBlocked    ServerState = "blocked"
	ServerConnecting ServerState = "connecting"
	ServerReady      ServerState = "ready"
	ServerDegraded   ServerState = "degraded"
	ServerError      ServerState = "error"
	ServerClosed     ServerState = "closed"
)

// CredentialReference never stores a credential. Env names the process
// environment variable whose value is injected into an outbound HTTP header.
type CredentialReference struct {
	Env    string `json:"env"`
	Header string `json:"header,omitempty"`
	Scheme string `json:"scheme,omitempty"`
}

type ServerConfig struct {
	ID                    string               `json:"id"`
	URL                   string               `json:"url"`
	Enabled               bool                 `json:"enabled"`
	TimeoutMS             int                  `json:"timeout_ms"`
	AllowInsecure         bool                 `json:"allow_insecure,omitempty"`
	Credential            *CredentialReference `json:"credential,omitempty"`
	ToolAllowlist         []string             `json:"tool_allowlist"`
	ReadOnlyTools         []string             `json:"read_only_tools,omitempty"`
	ApprovalRequiredTools []string             `json:"approval_required_tools,omitempty"`
	RiskLevels            map[string]RiskLevel `json:"risk_levels,omitempty"`
	MaxReadOnlyRetries    int                  `json:"max_read_only_retries,omitempty"`
}

type Config struct {
	ServiceAllowlist   []string       `json:"service_allowlist"`
	ApprovalTTLSeconds int            `json:"approval_ttl_seconds,omitempty"`
	Servers            []ServerConfig `json:"servers"`
}

// ProtocolInfo captures the negotiated MCP protocol and upstream identity.
type ProtocolInfo struct {
	ProtocolVersion string `json:"protocol_version,omitempty"`
	ServerName      string `json:"server_name,omitempty"`
	ServerVersion   string `json:"server_version,omitempty"`
}

// ToolDefinition is the sanitized, namespaced representation exposed by the
// hub. Name is always serverID.toolName; UpstreamName is sent over MCP.
type ToolDefinition struct {
	Name             string          `json:"name"`
	ServerID         string          `json:"server_id"`
	UpstreamName     string          `json:"upstream_name"`
	Description      string          `json:"description,omitempty"`
	InputSchema      json.RawMessage `json:"input_schema"`
	OutputSchema     json.RawMessage `json:"output_schema,omitempty"`
	ReadOnly         bool            `json:"read_only"`
	Idempotent       bool            `json:"idempotent"`
	Destructive      bool            `json:"destructive"`
	OpenWorld        bool            `json:"open_world"`
	Risk             RiskLevel       `json:"risk"`
	RequiresApproval bool            `json:"requires_approval"`
}

type ServerStatus struct {
	ID                   string       `json:"id"`
	URL                  string       `json:"url"`
	Enabled              bool         `json:"enabled"`
	Allowed              bool         `json:"allowed"`
	State                ServerState  `json:"state"`
	TimeoutMS            int          `json:"timeout_ms"`
	CredentialEnv        string       `json:"credential_env,omitempty"`
	CredentialConfigured bool         `json:"credential_configured"`
	ToolCount            int          `json:"tool_count"`
	Protocol             ProtocolInfo `json:"protocol,omitempty"`
	LastRefreshAt        *time.Time   `json:"last_refresh_at,omitempty"`
	LastErrorCode        string       `json:"last_error_code,omitempty"`
	LastError            string       `json:"last_error,omitempty"`
}

type InvokeRequest struct {
	UserName      string         `json:"-"`
	ToolName      string         `json:"tool_name"`
	Arguments     map[string]any `json:"arguments"`
	ApprovalToken string         `json:"approval_token,omitempty"`
	// OperationID is an internal, stable idempotency key. It is forwarded in
	// MCP's _meta object rather than injected into tool arguments, preserving
	// each tool's declared JSON Schema.
	OperationID string `json:"-"`
}

type InvocationResult struct {
	RequestID   string              `json:"request_id"`
	OperationID string              `json:"operation_id,omitempty"`
	ToolName    string              `json:"tool_name"`
	ServerID    string              `json:"server_id"`
	Risk        RiskLevel           `json:"risk"`
	Attempts    int                 `json:"attempts"`
	DurationMS  int64               `json:"duration_ms"`
	Result      *mcp.CallToolResult `json:"result"`
}

type ApprovalChallenge struct {
	ID               string          `json:"id"`
	ToolName         string          `json:"tool_name"`
	Risk             RiskLevel       `json:"risk"`
	ArgumentsDigest  string          `json:"arguments_digest"`
	ArgumentsPreview json.RawMessage `json:"arguments_preview"`
	ExpiresAt        time.Time       `json:"expires_at"`
	ApprovedAt       *time.Time      `json:"approved_at,omitempty"`
	ConsumedAt       *time.Time      `json:"consumed_at,omitempty"`

	userName string
}

type IssuedApproval struct {
	ChallengeID string    `json:"challenge_id"`
	Token       string    `json:"token"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type AuditEvent struct {
	RequestID        string
	UserName         string
	ServerID         string
	ToolName         string
	Risk             RiskLevel
	Outcome          string
	ApprovalUsed     bool
	Attempts         int
	DurationMS       int64
	ArgumentsDigest  string
	ArgumentsPreview string
	ResultSummary    string
	ErrorCode        string
	ErrorMessage     string
	CreatedAt        time.Time
}

type AuditSink interface {
	RecordMCPAudit(context.Context, AuditEvent) error
}

type noopAuditSink struct{}

func (noopAuditSink) RecordMCPAudit(context.Context, AuditEvent) error { return nil }
