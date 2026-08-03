package message

import (
	"GopherAI/common/mysql"
	"GopherAI/model"
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func GetMessagesBySessionID(ctx context.Context, userName, sessionID string) ([]model.Message, error) {
	if mysql.DB == nil {
		return nil, errors.New("mysql is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var msgs []model.Message
	err := mysql.DB.WithContext(ctx).
		Where("session_id = ? AND user_name = ?", sessionID, userName).
		Order("created_at ASC, id ASC").
		Find(&msgs).Error
	return msgs, err
}

// GetRecentMessagesBySessionID returns the tail of a session in chronological
// order. It is used to hydrate an active model context without loading every
// historical message into process memory.
func GetRecentMessagesBySessionID(ctx context.Context, userName, sessionID string, limit int) ([]model.Message, error) {
	if mysql.DB == nil {
		return nil, errors.New("mysql is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit < 1 {
		return []model.Message{}, nil
	}

	var newestFirst []model.Message
	err := mysql.DB.WithContext(ctx).
		Where("session_id = ? AND user_name = ?", sessionID, userName).
		Order("created_at DESC, id DESC").
		Limit(limit).
		Find(&newestFirst).Error
	if err != nil {
		return nil, err
	}
	for left, right := 0, len(newestFirst)-1; left < right; left, right = left+1, right-1 {
		newestFirst[left], newestFirst[right] = newestFirst[right], newestFirst[left]
	}
	return newestFirst, nil
}

// GetMessagesPageBySessionID returns a reverse-keyset page for the history
// API. It fetches newest-first for an efficient composite-index scan, then
// restores chronological order before returning so clients can prepend older
// pages without re-sorting messages locally.
func GetMessagesPageBySessionID(ctx context.Context, userName, sessionID string, beforeCreatedAt time.Time, beforeID uint, limit int) ([]model.Message, bool, error) {
	if mysql.DB == nil {
		return nil, false, errors.New("mysql is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit < 1 {
		return []model.Message{}, false, nil
	}

	query := mysql.DB.WithContext(ctx).Where("session_id = ? AND user_name = ?", sessionID, userName)
	if !beforeCreatedAt.IsZero() {
		query = query.Where("(created_at < ?) OR (created_at = ? AND id < ?)", beforeCreatedAt, beforeCreatedAt, beforeID)
	}
	var newestFirst []model.Message
	if err := query.Order("created_at DESC, id DESC").Limit(limit + 1).Find(&newestFirst).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(newestFirst) > limit
	if hasMore {
		newestFirst = newestFirst[:limit]
	}
	for left, right := 0, len(newestFirst)-1; left < right; left, right = left+1, right-1 {
		newestFirst[left], newestFirst[right] = newestFirst[right], newestFirst[left]
	}
	return newestFirst, hasMore, nil
}

func GetMessagesBySessionIDs(ctx context.Context, userName string, sessionIDs []string) ([]model.Message, error) {
	var msgs []model.Message
	if len(sessionIDs) == 0 {
		return msgs, nil
	}
	if mysql.DB == nil {
		return nil, errors.New("mysql is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := mysql.DB.WithContext(ctx).
		Where("session_id IN ? AND user_name = ?", sessionIDs, userName).
		Order("created_at ASC, id ASC").
		Find(&msgs).Error
	return msgs, err
}

// CreateMessage inserts a message exactly once for its MessageID. RabbitMQ is
// at-least-once by design: a publisher can lose its confirmation after the
// broker accepted the delivery, and a consumer can lose its acknowledgement
// after MySQL committed. Returning the existing row on a unique-key conflict
// turns both cases into a safe retry.
func CreateMessage(message *model.Message) (*model.Message, error) {
	return CreateMessageWithContext(context.Background(), message)
}

func CreateMessageWithContext(ctx context.Context, message *model.Message) (*model.Message, error) {
	if mysql.DB == nil {
		return nil, errors.New("mysql is not initialized")
	}
	if message == nil {
		return nil, errors.New("message is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	message.EnsureMessageID()

	result := mysql.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "message_id"}},
		DoNothing: true,
	}).Create(message)
	if result.Error != nil {
		return message, result.Error
	}
	if result.RowsAffected > 0 {
		return message, nil
	}

	// MySQL's ON DUPLICATE KEY syntax does not return the pre-existing row.
	// Fetch it explicitly so callers receive a normal persisted Message, and
	// so an unrelated conflict is not silently treated as a successful retry.
	existing := new(model.Message)
	err := mysql.DB.WithContext(ctx).Where("message_id = ?", message.MessageID).First(existing).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return message, fmt.Errorf("message insert reported a duplicate but message_id %q was not found: %w", message.MessageID, err)
		}
		return message, err
	}
	*message = *existing
	return message, nil
}

func GetAllMessages() ([]model.Message, error) {
	return GetAllMessagesWithContext(context.Background())
}

// GetAllMessagesWithContext is used only to rebuild the in-memory cache at
// startup. The join deliberately excludes orphaned or historically polluted
// rows whose message owner does not match the authoritative session owner.
func GetAllMessagesWithContext(ctx context.Context) ([]model.Message, error) {
	if mysql.DB == nil {
		return nil, errors.New("mysql is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var msgs []model.Message
	err := mysql.DB.WithContext(ctx).
		Model(&model.Message{}).
		Select("messages.*").
		Joins("JOIN sessions ON sessions.id = messages.session_id AND sessions.user_name = messages.user_name AND sessions.deleted_at IS NULL").
		Order("messages.created_at ASC, messages.id ASC").
		Find(&msgs).Error
	return msgs, err
}
