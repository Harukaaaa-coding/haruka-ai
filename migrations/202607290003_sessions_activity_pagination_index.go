package migrations

import (
	"context"

	"gorm.io/gorm"
)

// sessionsActivityPaginationIndex supplies the equality filters and ordering
// used by the session keyset API. InnoDB can scan this ascending B-tree in
// reverse for updated_at DESC, id DESC; leaving the definition direction-free
// also keeps the migration compatible with MySQL versions before descending
// index syntax was fully supported.
func sessionsActivityPaginationIndex(ctx context.Context, db *gorm.DB) error {
	const indexName = "idx_sessions_user_activity"

	var indexCount int64
	if err := db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'sessions' AND INDEX_NAME = ?`, indexName).Scan(&indexCount).Error; err != nil {
		return err
	}
	if indexCount > 0 {
		return nil
	}
	return db.WithContext(ctx).Exec(
		"CREATE INDEX idx_sessions_user_activity ON sessions (user_name, deleted_at, updated_at, id)",
	).Error
}
