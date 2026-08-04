package model

import (
	"time"

	"gorm.io/gorm"
)

const (
	KnowledgeBaseStatusActive   = "active"
	KnowledgeBaseStatusDeleting = "deleting"

	DocumentStatusPending  = "pending"
	DocumentStatusIndexing = "indexing"
	DocumentStatusReady    = "ready"
	DocumentStatusFailed   = "failed"
	DocumentStatusDeleting = "deleting"

	IndexTaskTypeIndex  = "index"
	IndexTaskTypeDelete = "delete"

	IndexTaskStatusPending   = "pending"
	IndexTaskStatusRunning   = "running"
	IndexTaskStatusSucceeded = "succeeded"
	IndexTaskStatusFailed    = "failed"
	IndexTaskStatusCancelled = "cancelled"
)

// KnowledgeBase is a user-owned collection of independently indexed documents.
// Vector data lives in Redis Stack; this record is the durable source of truth
// for ownership and lifecycle state.
type KnowledgeBase struct {
	ID          string         `gorm:"primaryKey;type:varchar(36)" json:"id"`
	UserName    string         `gorm:"type:varchar(50);not null;index:idx_kb_owner_created,priority:1" json:"-"`
	Name        string         `gorm:"type:varchar(100);not null" json:"name"`
	Description string         `gorm:"type:varchar(1000)" json:"description,omitempty"`
	Status      string         `gorm:"type:varchar(20);not null;default:active" json:"status"`
	CreatedAt   time.Time      `gorm:"index:idx_kb_owner_created,priority:2" json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`

	Documents []KnowledgeDocument `gorm:"foreignKey:KnowledgeBaseID" json:"documents,omitempty"`
}

// KnowledgeDocument stores the durable metadata for an uploaded source file.
// StoragePath is deliberately excluded from JSON because it is a server-local
// implementation detail and can disclose filesystem layout.
type KnowledgeDocument struct {
	ID              string         `gorm:"primaryKey;type:varchar(36)" json:"id"`
	KnowledgeBaseID string         `gorm:"type:varchar(36);not null;index:idx_kb_document,priority:1" json:"knowledge_base_id"`
	UserName        string         `gorm:"type:varchar(50);not null;index:idx_document_owner" json:"-"`
	OriginalName    string         `gorm:"type:varchar(255);not null" json:"name"`
	StoragePath     string         `gorm:"type:varchar(1024);not null" json:"-"`
	ContentType     string         `gorm:"type:varchar(100)" json:"content_type,omitempty"`
	Size            int64          `gorm:"not null" json:"size"`
	Checksum        string         `gorm:"type:char(64);not null" json:"checksum"`
	Status          string         `gorm:"type:varchar(20);not null;default:pending;index" json:"status"`
	ChunkCount      int            `gorm:"not null;default:0" json:"chunk_count"`
	ErrorMessage    string         `gorm:"type:varchar(1000)" json:"error,omitempty"`
	CreatedAt       time.Time      `gorm:"index:idx_kb_document,priority:2" json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `gorm:"index" json:"-"`
}

// KnowledgeIndexTask is a database-backed queue item. Pending work survives a
// process restart and running work is recovered by the worker after a timeout.
type KnowledgeIndexTask struct {
	ID              string     `gorm:"primaryKey;type:varchar(36)" json:"id"`
	KnowledgeBaseID string     `gorm:"type:varchar(36);not null;index" json:"knowledge_base_id"`
	DocumentID      string     `gorm:"type:varchar(36);not null;index" json:"document_id"`
	UserName        string     `gorm:"type:varchar(50);not null;index" json:"-"`
	Type            string     `gorm:"type:varchar(20);not null" json:"type"`
	Status          string     `gorm:"type:varchar(20);not null;default:pending;index:idx_index_task_poll,priority:1" json:"status"`
	Attempts        int        `gorm:"not null;default:0" json:"attempts"`
	ErrorMessage    string     `gorm:"type:varchar(1000)" json:"error,omitempty"`
	StartedAt       *time.Time `gorm:"index:idx_index_task_poll,priority:2" json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// KnowledgeBaseSummary is returned by list/detail APIs without loading source
// paths or document bodies.
type KnowledgeBaseSummary struct {
	KnowledgeBase
	DocumentCount int64 `json:"document_count"`
	ChunkCount    int64 `json:"chunk_count"`
}

// KnowledgeReference identifies the exact source chunk used by retrieval.
// Rune offsets are used so values remain correct for Chinese and other Unicode
// text instead of being byte offsets.
type KnowledgeReference struct {
	ChunkID string `json:"chunk_id"`
	// MergedChunkIDs preserves every original chunk represented by a merged
	// retrieval context. ChunkID remains the canonical citation target while
	// evaluators and provenance UIs can account for the full source span.
	MergedChunkIDs  []string `json:"merged_chunk_ids,omitempty"`
	KnowledgeBaseID string   `json:"knowledge_base_id"`
	DocumentID      string   `json:"document_id"`
	DocumentName    string   `json:"document_name"`
	Heading         string   `json:"heading,omitempty"`
	ChunkIndex      int      `json:"chunk_index"`
	CitationIndex   int      `json:"citation_index,omitempty"`
	StartRune       int      `json:"start_rune"`
	EndRune         int      `json:"end_rune"`
	Score           float64  `json:"score,omitempty"`
	Content         string   `json:"content,omitempty"`
}
