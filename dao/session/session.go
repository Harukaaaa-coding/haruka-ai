package session

import (
	"GopherAI/common/mysql"
	"GopherAI/model"
	"context"
	"errors"
	"strings"
	"time"
)

var ErrDatabaseUnavailable = errors.New("mysql is not initialized")

func GetSessionsByUserName(UserName int64) ([]model.Session, error) {
	var sessions []model.Session
	err := mysql.DB.Where("user_name = ?", UserName).Find(&sessions).Error
	return sessions, err
}

func CreateSession(session *model.Session) (*model.Session, error) {
	return CreateSessionWithContext(context.Background(), session)
}

func CreateSessionWithContext(ctx context.Context, session *model.Session) (*model.Session, error) {
	if mysql.DB == nil {
		return nil, ErrDatabaseUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := mysql.DB.WithContext(ctx).Create(session).Error
	return session, err
}

func GetSessionByID(sessionID string) (*model.Session, error) {
	if mysql.DB == nil {
		return nil, ErrDatabaseUnavailable
	}
	var session model.Session
	err := mysql.DB.Where("id = ?", sessionID).First(&session).Error
	return &session, err
}

// GetOwnedSession returns a session only when both its ID and authenticated
// owner match. Callers intentionally receive gorm.ErrRecordNotFound for both
// an unknown ID and another user's ID so the ownership boundary does not leak
// whether a session exists.
func GetOwnedSession(ctx context.Context, userName, sessionID string) (*model.Session, error) {
	if mysql.DB == nil {
		return nil, ErrDatabaseUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var session model.Session
	err := mysql.DB.WithContext(ctx).
		Where("id = ? AND user_name = ?", sessionID, userName).
		First(&session).Error
	return &session, err
}

// ListSessionsByUserName reads session metadata directly from the durable
// store. The in-memory AI helper cache intentionally contains only active
// sessions, so it must not be used as the source of truth for a user's list.
func ListSessionsByUserName(ctx context.Context, userName string) ([]model.Session, error) {
	if mysql.DB == nil {
		return nil, ErrDatabaseUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	userName = strings.TrimSpace(userName)
	if userName == "" {
		return []model.Session{}, nil
	}

	var sessions []model.Session
	err := mysql.DB.WithContext(ctx).
		Where("user_name = ?", userName).
		Order("updated_at DESC, id DESC").
		Find(&sessions).Error
	return sessions, err
}

// ListSessionsPageByUserName returns one newest-first page. The cursor is the
// last session from the previous page, ordered by (updated_at DESC, id DESC),
// so callers avoid offset scans as a user's session count grows.
func ListSessionsPageByUserName(ctx context.Context, userName string, beforeUpdatedAt time.Time, beforeID string, limit int) ([]model.Session, bool, error) {
	if mysql.DB == nil {
		return nil, false, ErrDatabaseUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	userName = strings.TrimSpace(userName)
	if userName == "" || limit < 1 {
		return []model.Session{}, false, nil
	}

	query := mysql.DB.WithContext(ctx).Where("user_name = ?", userName)
	if !beforeUpdatedAt.IsZero() {
		query = query.Where("(updated_at < ?) OR (updated_at = ? AND id < ?)", beforeUpdatedAt, beforeUpdatedAt, beforeID)
	}
	var sessions []model.Session
	if err := query.Order("updated_at DESC, id DESC").Limit(limit + 1).Find(&sessions).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(sessions) > limit
	if hasMore {
		sessions = sessions[:limit]
	}
	return sessions, hasMore, nil
}

// TouchOwnedSession moves a session to the top of its owner's activity list
// after a successful conversation turn. It deliberately scopes the update to
// both owner and ID so it cannot alter another user's session.
func TouchOwnedSession(ctx context.Context, userName, sessionID string) error {
	if mysql.DB == nil {
		return ErrDatabaseUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return mysql.DB.WithContext(ctx).
		Model(&model.Session{}).
		Where("id = ? AND user_name = ?", sessionID, userName).
		Update("updated_at", time.Now()).Error
}
