package migrations

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// messagesMessageIDBackfill makes RabbitMQ redelivery safe for persisted
// messages. It does not depend on the Go model so it also repairs legacy rows
// created before MessageID was added to model.Message.
func messagesMessageIDBackfill(ctx context.Context, db *gorm.DB) error {
	// MySQL has no ADD COLUMN IF NOT EXISTS (that is MariaDB/PostgreSQL syntax),
	// so existence is probed the same way this file already probes the unique
	// index below. Editing this released migration is safe precisely because the
	// old statement was invalid on MySQL: no database can have recorded it.
	var columnCount int64
	if err := db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'messages' AND COLUMN_NAME = 'message_id'`).Scan(&columnCount).Error; err != nil {
		return err
	}
	if columnCount == 0 {
		if err := db.WithContext(ctx).Exec(
			"ALTER TABLE messages ADD COLUMN message_id VARCHAR(36) NULL",
		).Error; err != nil {
			return err
		}
	}
	if err := db.WithContext(ctx).Exec(
		"UPDATE messages SET message_id = UUID() WHERE message_id IS NULL OR TRIM(message_id) = ''",
	).Error; err != nil {
		return err
	}

	var duplicateCount int64
	if err := db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM (
  SELECT message_id FROM messages GROUP BY message_id HAVING COUNT(*) > 1
) AS duplicate_message_ids`).Scan(&duplicateCount).Error; err != nil {
		return err
	}
	if duplicateCount > 0 {
		return errors.New("messages contains duplicate message_id values; resolve duplicates before adding the unique index")
	}

	var uniqueIndexCount int64
	if err := db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'messages' AND COLUMN_NAME = 'message_id' AND NON_UNIQUE = 0`).Scan(&uniqueIndexCount).Error; err != nil {
		return err
	}
	if uniqueIndexCount == 0 {
		if err := db.WithContext(ctx).Exec("CREATE UNIQUE INDEX idx_messages_message_id ON messages (message_id)").Error; err != nil {
			return err
		}
	}
	return db.WithContext(ctx).Exec("ALTER TABLE messages MODIFY COLUMN message_id VARCHAR(36) NOT NULL").Error
}
