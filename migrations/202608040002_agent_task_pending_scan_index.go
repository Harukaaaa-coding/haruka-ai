package migrations

import (
	"context"

	"gorm.io/gorm"
)

// agentTaskPendingScanIndex serves the worker's hot path. ListPendingTasks
// filters on status and orders by created_at, id, but the existing
// idx_agent_task_poll is (status, lease_until) and therefore only satisfies the
// equality predicate, leaving MySQL to filesort every poll. Ordering the index
// the way the query reads it lets InnoDB stop at the LIMIT instead.
func agentTaskPendingScanIndex(ctx context.Context, db *gorm.DB) error {
	const indexName = "idx_agent_task_pending_scan"

	var indexCount int64
	if err := db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'agent_tasks' AND INDEX_NAME = ?`, indexName).Scan(&indexCount).Error; err != nil {
		return err
	}
	if indexCount > 0 {
		return nil
	}
	return db.WithContext(ctx).Exec(
		"CREATE INDEX idx_agent_task_pending_scan ON agent_tasks (status, created_at, id)",
	).Error
}