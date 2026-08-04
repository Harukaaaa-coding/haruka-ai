package knowledgebase

import (
	"GopherAI/common/mysql"
	"GopherAI/model"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrDatabaseUnavailable = errors.New("mysql is not initialized")
	// ErrTaskSuperseded means a recovered worker or deletion operation owns a
	// newer lifecycle attempt. The stale worker must not publish any result.
	ErrTaskSuperseded = errors.New("knowledge index task was superseded")
	// ErrDocumentLifecycleChanged means this task still owns its attempt, but
	// another operation changed the document state (most commonly deletion).
	// Callers must compensate or cancel the task rather than treating it as a
	// successful completion.
	ErrDocumentLifecycleChanged = errors.New("knowledge document lifecycle changed")
	ErrDeleteAlreadyScheduled   = errors.New("knowledge document deletion is already scheduled")
)

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

// CreateDeleteDocumentTask makes the document immediately non-searchable in
// the durable source of truth and records restart-safe cleanup work atomically.
func CreateDeleteDocumentTask(userName, knowledgeBaseID, documentID string, task *model.KnowledgeIndexTask) error {
	db, err := database()
	if err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var document model.KnowledgeDocument
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND knowledge_base_id = ? AND user_name = ?", documentID, knowledgeBaseID, userName).First(&document).Error; err != nil {
			return err
		}
		var active model.KnowledgeIndexTask
		activeErr := tx.Where("document_id = ? AND type = ? AND status IN ?", documentID, model.IndexTaskTypeDelete, []string{model.IndexTaskStatusPending, model.IndexTaskStatusRunning}).First(&active).Error
		if activeErr == nil {
			return ErrDeleteAlreadyScheduled
		}
		if activeErr != nil && !errors.Is(activeErr, gorm.ErrRecordNotFound) {
			return activeErr
		}
		if err := tx.Model(&model.KnowledgeDocument{}).Where("id = ?", documentID).
			Updates(map[string]any{"status": model.DocumentStatusDeleting, "error_message": ""}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.KnowledgeIndexTask{}).
			Where("document_id = ? AND type = ? AND status = ?", documentID, model.IndexTaskTypeIndex, model.IndexTaskStatusPending).
			Updates(map[string]any{"status": model.IndexTaskStatusFailed, "finished_at": time.Now(), "error_message": "superseded by document deletion"}).Error; err != nil {
			return err
		}
		return tx.Create(task).Error
	})
}

func CompleteDeleteTask(taskID, documentID string, attempt int) error {
	db, err := database()
	if err != nil {
		return err
	}
	now := time.Now()
	return db.Transaction(func(tx *gorm.DB) error {
		result := tx.Where("id = ? AND status = ?", documentID, model.DocumentStatusDeleting).Delete(&model.KnowledgeDocument{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrDocumentLifecycleChanged
		}
		result = tx.Model(&model.KnowledgeIndexTask{}).Where("id = ? AND type = ? AND status = ? AND attempts = ?", taskID, model.IndexTaskTypeDelete, model.IndexTaskStatusRunning, attempt).Updates(map[string]any{
			"status": model.IndexTaskStatusSucceeded, "finished_at": &now, "error_message": "",
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrTaskSuperseded
		}
		return nil
	})
}

func FailDeleteTask(taskID, documentID, message string, retry bool, attempt int) error {
	db, err := database()
	if err != nil {
		return err
	}
	if len(message) > 1000 {
		message = message[:1000]
	}
	taskStatus, documentStatus := model.IndexTaskStatusFailed, model.DocumentStatusDeleting
	var finishedAt any = time.Now()
	if retry {
		taskStatus, documentStatus, finishedAt = model.IndexTaskStatusPending, model.DocumentStatusDeleting, nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var document model.KnowledgeDocument
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status = ?", documentID, model.DocumentStatusDeleting).First(&document).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrDocumentLifecycleChanged
			}
			return err
		}
		result := tx.Model(&model.KnowledgeDocument{}).Where("id = ?", documentID).Updates(map[string]any{"status": documentStatus, "error_message": message})
		if result.Error != nil {
			return result.Error
		}
		result = tx.Model(&model.KnowledgeIndexTask{}).Where("id = ? AND type = ? AND status = ? AND attempts = ?", taskID, model.IndexTaskTypeDelete, model.IndexTaskStatusRunning, attempt).
			Updates(map[string]any{"status": taskStatus, "finished_at": finishedAt, "error_message": message})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrTaskSuperseded
		}
		return nil
	})
}

// RequeueDeleteTask repairs a document state that was changed by a stale index
// worker while this delete attempt was running. The task attempt must still be
// current; otherwise a later worker owns the lifecycle and no mutation occurs.
func RequeueDeleteTask(taskID, documentID, message string, attempt int) error {
	db, err := database()
	if err != nil {
		return err
	}
	if len(message) > 1000 {
		message = message[:1000]
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var task model.KnowledgeIndexTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND type = ? AND status = ? AND attempts = ?", taskID, model.IndexTaskTypeDelete, model.IndexTaskStatusRunning, attempt).First(&task).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTaskSuperseded
			}
			return err
		}
		var document model.KnowledgeDocument
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", documentID).First(&document).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrDocumentLifecycleChanged
			}
			return err
		}
		result := tx.Model(&model.KnowledgeDocument{}).Where("id = ?", documentID).
			Updates(map[string]any{"status": model.DocumentStatusDeleting, "error_message": message})
		if result.Error != nil {
			return result.Error
		}
		result = tx.Model(&model.KnowledgeIndexTask{}).Where("id = ? AND type = ? AND status = ? AND attempts = ?", taskID, model.IndexTaskTypeDelete, model.IndexTaskStatusRunning, attempt).
			Updates(map[string]any{"status": model.IndexTaskStatusPending, "started_at": nil, "finished_at": nil, "error_message": message})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrTaskSuperseded
		}
		return nil
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

func GetActiveDeleteTask(userName, knowledgeBaseID, documentID string) (*model.KnowledgeIndexTask, error) {
	db, err := database()
	if err != nil {
		return nil, err
	}
	var task model.KnowledgeIndexTask
	err = db.Where("user_name = ? AND knowledge_base_id = ? AND document_id = ? AND type = ? AND status IN ?", userName, knowledgeBaseID, documentID, model.IndexTaskTypeDelete, []string{model.IndexTaskStatusPending, model.IndexTaskStatusRunning}).
		Order("created_at DESC, id DESC").First(&task).Error
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

// ListFailedDeleteTasks returns durable deletion work that exhausted its
// immediate retries. The worker applies a bounded exponential delay before
// putting these tasks back into pending, so a transient Redis or filesystem
// outage cannot leave a document tombstoned forever.
func ListFailedDeleteTasks(limit int) ([]model.KnowledgeIndexTask, error) {
	db, err := database()
	if err != nil {
		return nil, err
	}
	var tasks []model.KnowledgeIndexTask
	err = db.Where("type = ? AND status = ?", model.IndexTaskTypeDelete, model.IndexTaskStatusFailed).
		Order("updated_at ASC").Limit(limit).Find(&tasks).Error
	return tasks, err
}

// RequeueFailedDeleteTask changes only a still-failed delete task back to
// pending and reasserts the document tombstone in the same transaction.
func RequeueFailedDeleteTask(taskID string) (bool, error) {
	db, err := database()
	if err != nil {
		return false, err
	}
	claimed := false
	err = db.Transaction(func(tx *gorm.DB) error {
		var task model.KnowledgeIndexTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND type = ? AND status = ?", taskID, model.IndexTaskTypeDelete, model.IndexTaskStatusFailed).First(&task).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		var document model.KnowledgeDocument
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", task.DocumentID).First(&document).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrDocumentLifecycleChanged
			}
			return err
		}
		if err := tx.Model(&model.KnowledgeDocument{}).Where("id = ?", task.DocumentID).
			Updates(map[string]any{"status": model.DocumentStatusDeleting, "error_message": "retrying delete cleanup"}).Error; err != nil {
			return err
		}
		result := tx.Model(&model.KnowledgeIndexTask{}).Where("id = ? AND status = ?", task.ID, model.IndexTaskStatusFailed).
			Updates(map[string]any{"status": model.IndexTaskStatusPending, "started_at": nil, "finished_at": nil, "error_message": "retrying delete cleanup"})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		claimed = true
		return nil
	})
	return claimed, err
}

// ClaimTask atomically changes pending to running. The returned false value is
// normal when another worker claimed the same task first.
func ClaimTask(taskID string) (*model.KnowledgeIndexTask, bool, error) {
	db, err := database()
	if err != nil {
		return nil, false, err
	}
	now := time.Now()
	var task model.KnowledgeIndexTask
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status = ?", taskID, model.IndexTaskStatusPending).First(&task).Error; err != nil {
			return err
		}
		result := tx.Model(&model.KnowledgeIndexTask{}).Where("id = ? AND status = ?", taskID, model.IndexTaskStatusPending).
			Updates(map[string]any{
				"status":        model.IndexTaskStatusRunning,
				"attempts":      gorm.Expr("attempts + 1"),
				"started_at":    &now,
				"finished_at":   nil,
				"error_message": "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrTaskSuperseded
		}
		task.Status = model.IndexTaskStatusRunning
		task.Attempts++
		task.StartedAt = &now
		task.FinishedAt = nil
		task.ErrorMessage = ""
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, ErrTaskSuperseded) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &task, true, nil
}

func CompleteIndexTask(taskID, documentID string, chunkCount, attempt int) error {
	db, err := database()
	if err != nil {
		return err
	}
	now := time.Now()
	return db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.KnowledgeDocument{}).Where("id = ? AND status = ?", documentID, model.DocumentStatusIndexing).
			Updates(map[string]any{
				"status":        model.DocumentStatusReady,
				"chunk_count":   chunkCount,
				"error_message": "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrDocumentLifecycleChanged
		}
		result = tx.Model(&model.KnowledgeIndexTask{}).Where("id = ? AND type = ? AND status = ? AND attempts = ?", taskID, model.IndexTaskTypeIndex, model.IndexTaskStatusRunning, attempt).
			Updates(map[string]any{
				"status":        model.IndexTaskStatusSucceeded,
				"finished_at":   &now,
				"error_message": "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrTaskSuperseded
		}
		return nil
	})
}

func FailIndexTask(taskID, documentID, message string, retry bool, attempt int) error {
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
		result := tx.Model(&model.KnowledgeDocument{}).Where("id = ? AND status = ?", documentID, model.DocumentStatusIndexing).
			Updates(map[string]any{
				"status":        documentStatus,
				"error_message": message,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrDocumentLifecycleChanged
		}
		result = tx.Model(&model.KnowledgeIndexTask{}).Where("id = ? AND type = ? AND status = ? AND attempts = ?", taskID, model.IndexTaskTypeIndex, model.IndexTaskStatusRunning, attempt).
			Updates(map[string]any{
				"status":        taskStatus,
				"finished_at":   finishedAt,
				"error_message": message,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrTaskSuperseded
		}
		return nil
	})
}

func CancelIndexTask(taskID string, attempt int, message string) error {
	db, err := database()
	if err != nil {
		return err
	}
	result := db.Model(&model.KnowledgeIndexTask{}).Where("id = ? AND type = ? AND status = ? AND attempts = ?", taskID, model.IndexTaskTypeIndex, model.IndexTaskStatusRunning, attempt).
		Updates(map[string]any{"status": model.IndexTaskStatusCancelled, "finished_at": time.Now(), "error_message": message})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrTaskSuperseded
	}
	return nil
}

func MarkDocumentIndexing(documentID string) error {
	db, err := database()
	if err != nil {
		return err
	}
	result := db.Model(&model.KnowledgeDocument{}).Where("id = ? AND status = ?", documentID, model.DocumentStatusPending).
		Updates(map[string]any{"status": model.DocumentStatusIndexing, "error_message": ""})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrDocumentLifecycleChanged
	}
	return nil
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
			var document model.KnowledgeDocument
			documentErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", task.DocumentID).First(&document).Error
			canRetry := documentErr == nil
			if canRetry && task.Type == model.IndexTaskTypeIndex {
				canRetry = document.Status == model.DocumentStatusPending || document.Status == model.DocumentStatusIndexing
			}
			if canRetry && task.Type == model.IndexTaskTypeDelete {
				canRetry = document.Status == model.DocumentStatusDeleting
			}
			if documentErr != nil && !errors.Is(documentErr, gorm.ErrRecordNotFound) {
				return documentErr
			}
			if !canRetry {
				result := tx.Model(&model.KnowledgeIndexTask{}).Where("id = ? AND status = ?", task.ID, model.IndexTaskStatusRunning).
					Updates(map[string]any{"status": model.IndexTaskStatusCancelled, "finished_at": time.Now(), "error_message": "superseded by document lifecycle change"})
				if result.Error != nil {
					return result.Error
				}
				continue
			}
			if task.Type == model.IndexTaskTypeIndex && document.Status == model.DocumentStatusIndexing {
				if err := tx.Model(&model.KnowledgeDocument{}).Where("id = ? AND status = ?", task.DocumentID, model.DocumentStatusIndexing).
					Updates(map[string]any{"status": model.DocumentStatusPending, "error_message": "recovered stale indexing task"}).Error; err != nil {
					return err
				}
			}
			result := tx.Model(&model.KnowledgeIndexTask{}).Where("id = ? AND status = ?", task.ID, model.IndexTaskStatusRunning).
				Updates(map[string]any{"status": model.IndexTaskStatusPending, "started_at": nil, "finished_at": nil})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return ErrTaskSuperseded
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
