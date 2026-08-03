package knowledgebase

import (
	"GopherAI/common/mysql"
	"GopherAI/model"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrDatabaseUnavailable = errors.New("mysql is not initialized")

func database() (*gorm.DB, error) {
	if mysql.DB == nil {
		return nil, ErrDatabaseUnavailable
	}
	return mysql.DB, nil
}

func CreateKnowledgeBase(base *model.KnowledgeBase) error {
	db, err := database()
	if err != nil {
		return err
	}
	return db.Create(base).Error
}

func GetKnowledgeBase(userName, id string) (*model.KnowledgeBase, error) {
	db, err := database()
	if err != nil {
		return nil, err
	}
	var base model.KnowledgeBase
	err = db.Where("id = ? AND user_name = ?", id, userName).First(&base).Error
	return &base, err
}

func FindKnowledgeBaseByName(userName, name string) (*model.KnowledgeBase, error) {
	db, err := database()
	if err != nil {
		return nil, err
	}
	var base model.KnowledgeBase
	err = db.Where("user_name = ? AND name = ?", userName, name).
		Order("created_at ASC").First(&base).Error
	return &base, err
}

func ListKnowledgeBases(userName string, offset, limit int) ([]model.KnowledgeBaseSummary, error) {
	db, err := database()
	if err != nil {
		return nil, err
	}
	var bases []model.KnowledgeBase
	if err := db.Where("user_name = ?", userName).
		Order("created_at DESC").Offset(offset).Limit(limit).Find(&bases).Error; err != nil {
		return nil, err
	}

	result := make([]model.KnowledgeBaseSummary, 0, len(bases))
	for _, base := range bases {
		var documentCount int64
		var chunkCount int64
		if err := db.Model(&model.KnowledgeDocument{}).
			Where("knowledge_base_id = ? AND user_name = ?", base.ID, userName).
			Count(&documentCount).Error; err != nil {
			return nil, err
		}
		if err := db.Model(&model.KnowledgeDocument{}).
			Where("knowledge_base_id = ? AND user_name = ? AND status = ?", base.ID, userName, model.DocumentStatusReady).
			Select("COALESCE(SUM(chunk_count), 0)").Scan(&chunkCount).Error; err != nil {
			return nil, err
		}
		result = append(result, model.KnowledgeBaseSummary{
			KnowledgeBase: base,
			DocumentCount: documentCount,
			ChunkCount:    chunkCount,
		})
	}
	return result, nil
}

func ListReadyKnowledgeBases(userName string) ([]model.KnowledgeBase, error) {
	db, err := database()
	if err != nil {
		return nil, err
	}
	var bases []model.KnowledgeBase
	err = db.Model(&model.KnowledgeBase{}).
		Joins("JOIN knowledge_documents ON knowledge_documents.knowledge_base_id = knowledge_bases.id AND knowledge_documents.deleted_at IS NULL").
		Where("knowledge_bases.user_name = ? AND knowledge_bases.status = ? AND knowledge_documents.status = ?", userName, model.KnowledgeBaseStatusActive, model.DocumentStatusReady).
		Distinct("knowledge_bases.*").Find(&bases).Error
	return bases, err
}

func MarkKnowledgeBaseDeleting(userName, knowledgeBaseID string) error {
	db, err := database()
	if err != nil {
		return err
	}
	result := db.Model(&model.KnowledgeBase{}).
		Where("id = ? AND user_name = ? AND status = ?", knowledgeBaseID, userName, model.KnowledgeBaseStatusActive).
		Update("status", model.KnowledgeBaseStatusDeleting)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func RestoreKnowledgeBaseActive(userName, knowledgeBaseID string) error {
	db, err := database()
	if err != nil {
		return err
	}
	return db.Model(&model.KnowledgeBase{}).
		Where("id = ? AND user_name = ?", knowledgeBaseID, userName).
		Update("status", model.KnowledgeBaseStatusActive).Error
}

func CreateDocumentAndTask(document *model.KnowledgeDocument, task *model.KnowledgeIndexTask) error {
	db, err := database()
	if err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(document).Error; err != nil {
			return err
		}
		return tx.Create(task).Error
	})
}

func GetDocument(userName, knowledgeBaseID, documentID string) (*model.KnowledgeDocument, error) {
	db, err := database()
	if err != nil {
		return nil, err
	}
	var document model.KnowledgeDocument
	err = db.Where("id = ? AND knowledge_base_id = ? AND user_name = ?", documentID, knowledgeBaseID, userName).
		First(&document).Error
	return &document, err
}

func ListDocuments(userName, knowledgeBaseID string) ([]model.KnowledgeDocument, error) {
	db, err := database()
	if err != nil {
		return nil, err
	}
	var documents []model.KnowledgeDocument
	err = db.Where("knowledge_base_id = ? AND user_name = ?", knowledgeBaseID, userName).
		Order("created_at DESC").Find(&documents).Error
	return documents, err
}

func GetIndexTask(userName, taskID string) (*model.KnowledgeIndexTask, error) {
	db, err := database()
	if err != nil {
		return nil, err
	}
	var task model.KnowledgeIndexTask
	err = db.Where("id = ? AND user_name = ?", taskID, userName).First(&task).Error
	return &task, err
}

func GetIndexTaskByID(taskID string) (*model.KnowledgeIndexTask, error) {
	db, err := database()
	if err != nil {
		return nil, err
	}
	var task model.KnowledgeIndexTask
	err = db.Where("id = ?", taskID).First(&task).Error
	return &task, err
}

func GetLatestDocumentTask(userName, knowledgeBaseID, documentID string) (*model.KnowledgeIndexTask, error) {
	db, err := database()
	if err != nil {
		return nil, err
	}
	var task model.KnowledgeIndexTask
	err = db.Where("user_name = ? AND knowledge_base_id = ? AND document_id = ?", userName, knowledgeBaseID, documentID).
		Order("created_at DESC").First(&task).Error
	return &task, err
}

func ListPendingTasks(limit int) ([]model.KnowledgeIndexTask, error) {
	db, err := database()
	if err != nil {
		return nil, err
	}
	var tasks []model.KnowledgeIndexTask
	err = db.Where("status = ?", model.IndexTaskStatusPending).
		Order("created_at ASC").Limit(limit).Find(&tasks).Error
	return tasks, err
}

// ClaimTask atomically changes pending to running. The returned false value is
// normal when another worker claimed the same task first.
func ClaimTask(taskID string) (*model.KnowledgeIndexTask, bool, error) {
	db, err := database()
	if err != nil {
		return nil, false, err
	}
	now := time.Now()
	result := db.Model(&model.KnowledgeIndexTask{}).
		Where("id = ? AND status = ?", taskID, model.IndexTaskStatusPending).
		Updates(map[string]any{
			"status":        model.IndexTaskStatusRunning,
			"attempts":      gorm.Expr("attempts + 1"),
			"started_at":    &now,
			"finished_at":   nil,
			"error_message": "",
		})
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, false, nil
	}
	task, err := GetIndexTaskByID(taskID)
	return task, err == nil, err
}

func CompleteIndexTask(taskID, documentID string, chunkCount int) error {
	db, err := database()
	if err != nil {
		return err
	}
	now := time.Now()
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.KnowledgeDocument{}).Where("id = ?", documentID).
			Updates(map[string]any{
				"status":        model.DocumentStatusReady,
				"chunk_count":   chunkCount,
				"error_message": "",
			}).Error; err != nil {
			return err
		}
		return tx.Model(&model.KnowledgeIndexTask{}).Where("id = ?", taskID).
			Updates(map[string]any{
				"status":        model.IndexTaskStatusSucceeded,
				"finished_at":   &now,
				"error_message": "",
			}).Error
	})
}

func FailIndexTask(taskID, documentID, message string, retry bool) error {
	db, err := database()
	if err != nil {
		return err
	}
	if len(message) > 1000 {
		message = message[:1000]
	}
	taskStatus := model.IndexTaskStatusFailed
	documentStatus := model.DocumentStatusFailed
	var finishedAt any = time.Now()
	if retry {
		taskStatus = model.IndexTaskStatusPending
		documentStatus = model.DocumentStatusPending
		finishedAt = nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.KnowledgeDocument{}).Where("id = ?", documentID).
			Updates(map[string]any{
				"status":        documentStatus,
				"error_message": message,
			}).Error; err != nil {
			return err
		}
		return tx.Model(&model.KnowledgeIndexTask{}).Where("id = ?", taskID).
			Updates(map[string]any{
				"status":        taskStatus,
				"finished_at":   finishedAt,
				"error_message": message,
			}).Error
	})
}

func MarkDocumentIndexing(documentID string) error {
	db, err := database()
	if err != nil {
		return err
	}
	return db.Model(&model.KnowledgeDocument{}).Where("id = ?", documentID).
		Updates(map[string]any{"status": model.DocumentStatusIndexing, "error_message": ""}).Error
}

func RecoverStaleTasks(before time.Time) error {
	db, err := database()
	if err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var tasks []model.KnowledgeIndexTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("status = ? AND started_at < ?", model.IndexTaskStatusRunning, before).
			Find(&tasks).Error; err != nil {
			return err
		}
		for _, task := range tasks {
			if err := tx.Model(&model.KnowledgeIndexTask{}).Where("id = ?", task.ID).
				Updates(map[string]any{"status": model.IndexTaskStatusPending, "started_at": nil}).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.KnowledgeDocument{}).Where("id = ?", task.DocumentID).
				Update("status", model.DocumentStatusPending).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func DeleteDocumentRecords(userName, knowledgeBaseID, documentID string) error {
	db, err := database()
	if err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("document_id = ? AND user_name = ?", documentID, userName).
			Delete(&model.KnowledgeIndexTask{}).Error; err != nil {
			return err
		}
		result := tx.Where("id = ? AND knowledge_base_id = ? AND user_name = ?", documentID, knowledgeBaseID).
			Delete(&model.KnowledgeDocument{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

func DeleteKnowledgeBaseRecords(userName, knowledgeBaseID string) error {
	db, err := database()
	if err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("knowledge_base_id = ? AND user_name = ?", knowledgeBaseID, userName).
			Delete(&model.KnowledgeIndexTask{}).Error; err != nil {
			return err
		}
		if err := tx.Where("knowledge_base_id = ? AND user_name = ?", knowledgeBaseID, userName).
			Delete(&model.KnowledgeDocument{}).Error; err != nil {
			return err
		}
		result := tx.Where("id = ? AND user_name = ?", knowledgeBaseID, userName).
			Delete(&model.KnowledgeBase{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}
