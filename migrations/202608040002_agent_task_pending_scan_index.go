package migrations

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// agentTaskPendingScanIndex serves the worker's hot path. ListPendingTasks
// filters on status and orders by created_at, id, but the existing
// idx_agent_task_poll is (status, lease_until) and therefore only satisfies the
// equality predicate, leaving MySQL to filesort every poll. Ordering the index
// the way the query reads it lets InnoDB stop at the LIMIT instead.
func agentTaskPendingScanIndex(ctx context.Context, db *gorm.DB) error {
	const (
		tableName = "agent_tasks"
		indexName = "idx_agent_task_pending_scan"
		columns   = "status, created_at, id"
	)

	var indexCount int64
	if err := db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ?`, tableName, indexName).Scan(&indexCount).Error; err != nil {
		return err
	}
	if indexCount > 0 {
		return nil
	}
	// Identifiers cannot be bound as parameters; every value here is a
	// compile-time constant, so the formatted statement carries no user input.
	return db.WithContext(ctx).Exec(
		fmt.Sprintf("CREATE INDEX %s ON %s (%s)", indexName, tableName, columns),
	).Error
}
