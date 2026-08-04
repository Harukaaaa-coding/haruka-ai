package migrations

import (
	"context"

	"gorm.io/gorm"
)

// agentStepOperationID adds the durable correlation key used for Agent tool
// retries. Existing rows intentionally remain NULL: an ID is generated and
// persisted immediately before the next external invocation.
func agentStepOperationID(ctx context.Context, db *gorm.DB) error {
	const (
		tableName  = "agent_steps"
		columnName = "operation_id"
		indexName  = "idx_agent_step_operation_id"
	)

	var columnCount int64
	if err := db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`, tableName, columnName).Scan(&columnCount).Error; err != nil {
		return err
	}
	if columnCount == 0 {
		if err := db.WithContext(ctx).Exec("ALTER TABLE " + tableName + " ADD COLUMN " + columnName + " varchar(36) NULL").Error; err != nil {
			return err
		}
	}

	var indexCount int64
	if err := db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ?`, tableName, indexName).Scan(&indexCount).Error; err != nil {
		return err
	}
	if indexCount > 0 {
		return nil
	}
	return db.WithContext(ctx).Exec("CREATE INDEX " + indexName + " ON " + tableName + " (" + columnName + ")").Error
}
