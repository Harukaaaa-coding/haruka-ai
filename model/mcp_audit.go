package model

import "time"

// MCPAuditRecord intentionally stores a redacted argument preview and a
// content-free result summary. Credentials, approval tokens, and raw tool
// output must never be persisted here.
type MCPAuditRecord struct {
	ID               uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	RequestID        string    `gorm:"type:varchar(36);uniqueIndex;not null" json:"request_id"`
	UserName         string    `gorm:"type:varchar(50);index;not null" json:"username"`
	ServerID         string    `gorm:"type:varchar(63);index;not null" json:"server_id"`
	ToolName         string    `gorm:"type:varchar(191);index;not null" json:"tool_name"`
	RiskLevel        string    `gorm:"type:varchar(16);index;not null" json:"risk"`
	Outcome          string    `gorm:"type:varchar(32);index;not null" json:"outcome"`
	ApprovalUsed     bool      `gorm:"not null" json:"approval_used"`
	Attempts         int       `gorm:"not null" json:"attempts"`
	DurationMS       int64     `gorm:"not null" json:"duration_ms"`
	ArgumentsDigest  string    `gorm:"type:char(64);index" json:"arguments_digest,omitempty"`
	ArgumentsPreview string    `gorm:"type:text" json:"arguments_preview,omitempty"`
	ResultSummary    string    `gorm:"type:text" json:"result_summary,omitempty"`
	ErrorCode        string    `gorm:"type:varchar(64);index" json:"error_code,omitempty"`
	ErrorMessage     string    `gorm:"type:text" json:"error_message,omitempty"`
	CreatedAt        time.Time `gorm:"index;not null" json:"created_at"`
}
