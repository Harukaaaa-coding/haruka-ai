package mcpaudit

import (
	"GopherAI/common/mysql"
	"GopherAI/model"
	"context"
	"errors"
	"strings"
)

type Query struct {
	UserName string
	ServerID string
	ToolName string
	Outcome  string
	Limit    int
	Offset   int
}

func Create(ctx context.Context, record *model.MCPAuditRecord) error {
	if mysql.DB == nil {
		return errors.New("mysql is not initialized")
	}
	return mysql.DB.WithContext(ctx).Create(record).Error
}

func List(ctx context.Context, query Query) ([]model.MCPAuditRecord, int64, error) {
	if mysql.DB == nil {
		return nil, 0, errors.New("mysql is not initialized")
	}
	database := mysql.DB.WithContext(ctx).Model(&model.MCPAuditRecord{}).
		Where("user_name = ?", query.UserName)
	if value := strings.TrimSpace(query.ServerID); value != "" {
		database = database.Where("server_id = ?", value)
	}
	if value := strings.TrimSpace(query.ToolName); value != "" {
		database = database.Where("tool_name = ?", value)
	}
	if value := strings.TrimSpace(query.Outcome); value != "" {
		database = database.Where("outcome = ?", value)
	}

	var total int64
	if err := database.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var records []model.MCPAuditRecord
	err := database.Order("created_at DESC, id DESC").
		Limit(query.Limit).
		Offset(query.Offset).
		Find(&records).Error
	return records, total, err
}
