package migrations

import (
	"context"
	"time"

	"GopherAI/model"
	"gorm.io/gorm"
)

// legacyMessageSchema intentionally freezes the messages table at the state
// immediately before MessageID was introduced. The following migration can
// therefore add the new column as nullable, repair historical rows, and only
// then enforce its unique NOT NULL invariant. Do not replace this with
// model.Message: doing so would make an existing table fail before its
// backfill migration gets a chance to run.
type legacyMessageSchema struct {
	ID        uint      `gorm:"primaryKey;autoIncrement"`
	SessionID string    `gorm:"index;index:idx_message_owner_session_created,priority:2;not null;type:varchar(36)"`
	UserName  string    `gorm:"type:varchar(50);not null;index:idx_message_owner_session_created,priority:1"`
	Content   string    `gorm:"type:text"`
	IsUser    bool      `gorm:"not null"`
	Citations string    `gorm:"type:longtext"`
	CreatedAt time.Time `gorm:"index:idx_message_owner_session_created,priority:3"`
}

func (legacyMessageSchema) TableName() string {
	return "messages"
}

// legacyGORMBaseline is an additive, one-time bridge for databases created
// before the migration runner. Future changes must be added as new migrations
// rather than relying on API startup.
func legacyGORMBaseline(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).AutoMigrate(
		new(model.User),
		new(model.Session),
		new(legacyMessageSchema),
		new(model.KnowledgeBase),
		new(model.KnowledgeDocument),
		new(model.KnowledgeIndexTask),
		new(model.MCPAuditRecord),
		new(model.AgentTask),
		new(model.AgentStep),
	)
}
