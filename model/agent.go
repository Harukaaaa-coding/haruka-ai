package model

import "time"

const (
	AgentTaskStatusPending         = "pending"
	AgentTaskStatusPlanning        = "planning"
	AgentTaskStatusRunning         = "running"
	AgentTaskStatusWaitingApproval = "waiting_approval"
	AgentTaskStatusRequiresReview  = "requires_review"
	AgentTaskStatusSucceeded       = "succeeded"
	AgentTaskStatusFailed          = "failed"
	AgentTaskStatusRejected        = "rejected"
	AgentTaskStatusCancelled       = "cancelled"
)

const (
	AgentStepKindPlan  = "plan"
	AgentStepKindTool  = "tool"
	AgentStepKindFinal = "final"
)

const (
	AgentStepStatusPending          = "pending"
	AgentStepStatusRunning          = "running"
	AgentStepStatusWaitingApproval  = "waiting_approval"
	AgentStepStatusApproved         = "approved"
	AgentStepStatusRejected         = "rejected"
	AgentStepStatusSucceeded        = "succeeded"
	AgentStepStatusFailed           = "failed"
	AgentStepStatusExecutionUnknown = "execution_unknown"
	AgentStepStatusSkipped          = "skipped"
)

const (
	AgentApprovalDecisionPending  = "pending"
	AgentApprovalDecisionApproved = "approved"
	AgentApprovalDecisionRejected = "rejected"
)

// AgentTask is the durable source of truth for an Agent graph run. Checkpoint
// contains Eino's opaque serialized state and must never be returned by an API.
type AgentTask struct {
	ID        string `gorm:"primaryKey;type:varchar(36)" json:"id"`
	UserName  string `gorm:"type:varchar(50);not null;index:idx_agent_task_owner_created,priority:1" json:"-"`
	SessionID string `gorm:"type:varchar(36);index" json:"session_id,omitempty"`
	Goal      string `gorm:"type:text;not null" json:"goal"`
	ModelID   string `gorm:"type:varchar(100);not null" json:"model_id"`

	Status        string `gorm:"type:varchar(32);not null;default:pending;index:idx_agent_task_poll,priority:1;index" json:"status"`
	CurrentStepID string `gorm:"type:varchar(36);index" json:"current_step_id,omitempty"`
	PlanSummary   string `gorm:"type:text" json:"plan_summary,omitempty"`
	FinalAnswer   string `gorm:"type:longtext" json:"final_answer,omitempty"`
	ErrorMessage  string `gorm:"type:varchar(1000)" json:"error,omitempty"`

	Checkpoint        []byte `gorm:"type:longblob" json:"-"`
	CheckpointVersion uint64 `gorm:"not null;default:0" json:"-"`
	RunVersion        uint64 `gorm:"not null;default:0" json:"-"`
	Attempts          int    `gorm:"not null;default:0" json:"attempts"`

	LeaseOwner  string     `gorm:"type:varchar(100)" json:"-"`
	LeaseUntil  *time.Time `gorm:"index:idx_agent_task_poll,priority:2" json:"-"`
	HeartbeatAt *time.Time `json:"-"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	CreatedAt   time.Time  `gorm:"index:idx_agent_task_owner_created,priority:2" json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`

	Steps []AgentStep `gorm:"foreignKey:TaskID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"steps,omitempty"`
}

// AgentStep is a normalized, durable graph step. ToolArgumentsJSON and
// ToolOutputJSON are needed for exact restart semantics, but can contain
// sensitive user/tool data and therefore are deliberately excluded from JSON.
type AgentStep struct {
	ID       string `gorm:"primaryKey;type:varchar(36)" json:"id"`
	TaskID   string `gorm:"type:varchar(36);not null;uniqueIndex:idx_agent_step_sequence,priority:1;index;index:idx_agent_step_task_status,priority:1" json:"task_id"`
	UserName string `gorm:"type:varchar(50);not null;index:idx_agent_step_owner" json:"-"`
	Sequence int    `gorm:"not null;uniqueIndex:idx_agent_step_sequence,priority:2" json:"sequence"`
	Kind     string `gorm:"type:varchar(16);not null;index" json:"kind"`
	NodeKey  string `gorm:"type:varchar(100)" json:"node_key,omitempty"`

	Title        string `gorm:"type:varchar(255)" json:"title,omitempty"`
	Instruction  string `gorm:"type:text" json:"instruction,omitempty"`
	Status       string `gorm:"type:varchar(32);not null;default:pending;index:idx_agent_step_task_status,priority:2" json:"status"`
	Attempts     int    `gorm:"not null;default:0" json:"attempts"`
	ErrorMessage string `gorm:"type:varchar(1000)" json:"error,omitempty"`

	ToolName          string     `gorm:"type:varchar(191);index" json:"tool_name,omitempty"`
	ToolArgumentsJSON string     `gorm:"type:longtext" json:"-"`
	ArgumentsDigest   string     `gorm:"type:char(64);index" json:"arguments_digest,omitempty"`
	ArgumentsPreview  string     `gorm:"type:text" json:"arguments_preview,omitempty"`
	ToolOutputJSON    string     `gorm:"type:longtext" json:"-"`
	ResultSummary     string     `gorm:"type:text" json:"result_summary,omitempty"`
	RiskLevel         string     `gorm:"type:varchar(16);index" json:"risk,omitempty"`
	RequiresApproval  bool       `gorm:"not null;default:false" json:"requires_approval"`
	ReadOnly          bool       `gorm:"not null;default:false" json:"read_only"`
	Idempotent        bool       `gorm:"not null;default:false" json:"idempotent"`
	Destructive       bool       `gorm:"not null;default:false" json:"destructive"`
	ApprovalDecision  string     `gorm:"type:varchar(16);not null;default:pending;index" json:"approval_decision,omitempty"`
	ApprovalReason    string     `gorm:"type:varchar(500)" json:"approval_reason,omitempty"`
	ApprovalDecidedAt *time.Time `json:"approval_decided_at,omitempty"`
	MCPRequestID      string     `gorm:"type:varchar(36);index" json:"mcp_request_id,omitempty"`

	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	CreatedAt  time.Time  `gorm:"index:idx_agent_step_task_status,priority:3" json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}
