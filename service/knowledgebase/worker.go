package knowledgebase

import (
	"GopherAI/common/rag"
	"GopherAI/config"
	dao "GopherAI/dao/knowledgebase"
	"GopherAI/model"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"gorm.io/gorm"
)

const (
	maxIndexAttempts = 3
	workerPollPeriod = 2 * time.Second
	staleTaskAge     = 10 * time.Minute
	deleteRetryBase  = time.Minute
	deleteRetryMax   = 30 * time.Minute
)

var (
	workerMu      sync.Mutex
	workerStarted bool
	workerDone    chan struct{}
	workerQueue   = make(chan string, 128)
)

// StartIndexWorker starts a database-backed worker. The channel is only a
// latency optimization: polling pending rows makes queued work restart-safe.
func StartIndexWorker(contexts ...context.Context) error {
	workerMu.Lock()
	defer workerMu.Unlock()
	if workerStarted {
		return nil
	}
	if _, err := dao.ListPendingTasks(1); err != nil {
		return err
	}
	if err := dao.RecoverStaleTasks(time.Now().Add(-staleTaskAge)); err != nil {
		return err
	}
	workerStarted = true
	workerDone = make(chan struct{})
	workerCtx := context.Background()
	if len(contexts) > 0 && contexts[0] != nil {
		workerCtx = contexts[0]
	}
	done := workerDone
	go func() {
		defer func() {
			workerMu.Lock()
			workerStarted = false
			close(done)
			workerMu.Unlock()
		}()
		runIndexWorker(workerCtx)
	}()
	return nil
}

func IndexWorkerRunning() bool {
	workerMu.Lock()
	defer workerMu.Unlock()
	return workerStarted
}

func WaitIndexWorker(ctx context.Context) error {
	workerMu.Lock()
	done := workerDone
	workerMu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func EnqueueIndexTask(taskID string) {
	select {
	case workerQueue <- taskID:
	default:
		// The durable DB poller will pick it up when the hint channel is full.
	}
}

func runIndexWorker(ctx context.Context) {
	ticker := time.NewTicker(workerPollPeriod)
	defer ticker.Stop()
	recoveryTicker := time.NewTicker(time.Minute)
	defer recoveryTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case taskID := <-workerQueue:
			if err := ProcessIndexTaskNow(ctx, taskID); err != nil {
				log.Printf("knowledge index task failed: task=%s error=%v", taskID, err)
			}
		case <-ticker.C:
			tasks, err := dao.ListPendingTasks(10)
			if err != nil {
				log.Printf("knowledge index poll failed: %v", err)
				continue
			}
			for _, task := range tasks {
				if err := ProcessIndexTaskNow(ctx, task.ID); err != nil {
					log.Printf("knowledge index task failed: task=%s error=%v", task.ID, err)
				}
			}
		case <-recoveryTicker.C:
			if err := dao.RecoverStaleTasks(time.Now().Add(-staleTaskAge)); err != nil {
				log.Printf("knowledge index recovery failed: %v", err)
			}
			if err := requeueFailedDeleteTasks(time.Now()); err != nil {
				log.Printf("knowledge delete retry recovery failed: %v", err)
			}
		}
	}
}

func requeueFailedDeleteTasks(now time.Time) error {
	tasks, err := dao.ListFailedDeleteTasks(20)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if task.UpdatedAt.Add(deleteRetryDelay(task.Attempts)).After(now) {
			continue
		}
		requeued, err := dao.RequeueFailedDeleteTask(task.ID)
		if err != nil {
			if errors.Is(err, dao.ErrDocumentLifecycleChanged) {
				log.Printf("knowledge delete task cannot be retried because its document is gone: task=%s", task.ID)
				continue
			}
			return err
		}
		if requeued {
			EnqueueIndexTask(task.ID)
		}
	}
	return nil
}

func deleteRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		return deleteRetryBase
	}
	delay := deleteRetryBase
	for retry := 1; retry < attempts && delay < deleteRetryMax; retry++ {
		delay *= 2
	}
	if delay > deleteRetryMax {
		return deleteRetryMax
	}
	return delay
}

func ProcessIndexTaskNow(ctx context.Context, taskID string) error {
	task, claimed, err := dao.ClaimTask(taskID)
	if err != nil {
		return err
	}
	if !claimed {
		current, lookupErr := dao.GetIndexTaskByID(taskID)
		if lookupErr != nil {
			return lookupErr
		}
		if current.Status == model.IndexTaskStatusSucceeded {
			return nil
		}
		return fmt.Errorf("index task is %s", current.Status)
	}
	if task.Type == model.IndexTaskTypeDelete {
		err = withDocumentLock(task.DocumentID, func() error {
			document, lookupErr := dao.GetDocument(task.UserName, task.KnowledgeBaseID, task.DocumentID)
			if lookupErr != nil {
				return lookupErr
			}
			if deleteErr := rag.DeleteKnowledgeDocumentIndex(ctx, task.UserName, task.KnowledgeBaseID, task.DocumentID); deleteErr != nil {
				return deleteErr
			}
			if removeErr := os.Remove(document.StoragePath); removeErr != nil && !os.IsNotExist(removeErr) {
				return fmt.Errorf("remove source document: %w", removeErr)
			}
			return dao.CompleteDeleteTask(task.ID, task.DocumentID, task.Attempts)
		})
		if err == nil {
			return nil
		}
		if errors.Is(err, dao.ErrTaskSuperseded) {
			return nil
		}
		if errors.Is(err, dao.ErrDocumentLifecycleChanged) {
			// The delete task still owns this attempt, but a stale index worker
			// changed the document state between cleanup and DB finalization.
			// Reassert the tombstone and retry the idempotent physical cleanup.
			if requeueErr := dao.RequeueDeleteTask(task.ID, task.DocumentID, "retrying delete after document lifecycle conflict", task.Attempts); requeueErr != nil && !errors.Is(requeueErr, dao.ErrTaskSuperseded) {
				return fmt.Errorf("repair delete lifecycle: %w", requeueErr)
			}
			EnqueueIndexTask(task.ID)
			return nil
		}
		// The first attempts retry immediately. Once that budget is exhausted,
		// FailDeleteTask leaves a durable failed task which the recovery loop
		// retries with exponential backoff until all three stores converge.
		retry := task.Attempts < maxIndexAttempts && !errors.Is(err, gorm.ErrRecordNotFound)
		if updateErr := dao.FailDeleteTask(task.ID, task.DocumentID, "删除文档失败，请稍后重试", retry, task.Attempts); updateErr != nil && !errors.Is(updateErr, dao.ErrTaskSuperseded) {
			return fmt.Errorf("%v; update delete task failure state: %w", err, updateErr)
		}
		return err
	}

	err = withDocumentLock(task.DocumentID, func() error {
		base, err := dao.GetKnowledgeBase(task.UserName, task.KnowledgeBaseID)
		if err != nil {
			return err
		}
		if base.Status != model.KnowledgeBaseStatusActive {
			return ErrConflict
		}
		document, err := dao.GetDocument(task.UserName, task.KnowledgeBaseID, task.DocumentID)
		if err != nil {
			return err
		}
		if document.Status == model.DocumentStatusDeleting {
			return ErrConflict
		}
		if err := dao.MarkDocumentIndexing(document.ID); err != nil {
			return err
		}
		indexer, err := rag.NewKnowledgeBaseIndexer(ctx, task.UserName, task.KnowledgeBaseID, config.GetConfig().RagModelConfig.RagEmbeddingModel)
		if err != nil {
			return err
		}
		chunkCount, err := indexer.IndexFile(ctx, document.ID, document.OriginalName, document.StoragePath)
		if err != nil {
			return err
		}
		return dao.CompleteIndexTask(task.ID, document.ID, chunkCount, task.Attempts)
	})
	if err == nil {
		return nil
	}
	if errors.Is(err, dao.ErrTaskSuperseded) || errors.Is(err, dao.ErrDocumentLifecycleChanged) || errors.Is(err, ErrConflict) {
		// A delete or newer retry won the lifecycle race. Only deletion owns the
		// right to remove Redis hashes: an old stale retry must not erase a newer
		// successful index attempt for the same document.
		cleanup := errors.Is(err, ErrConflict)
		if document, lookupErr := dao.GetDocument(task.UserName, task.KnowledgeBaseID, task.DocumentID); lookupErr != nil {
			cleanup = errors.Is(lookupErr, gorm.ErrRecordNotFound)
		} else if document.Status == model.DocumentStatusDeleting {
			cleanup = true
		}
		if cleanup {
			if cleanupErr := rag.DeleteKnowledgeDocumentIndex(ctx, task.UserName, task.KnowledgeBaseID, task.DocumentID); cleanupErr != nil {
				return fmt.Errorf("cleanup superseded document index: %w", cleanupErr)
			}
		}
		if cancelErr := dao.CancelIndexTask(task.ID, task.Attempts, "superseded by document lifecycle change"); cancelErr != nil && !errors.Is(cancelErr, dao.ErrTaskSuperseded) {
			return cancelErr
		}
		return nil
	}

	// Keep provider details in server logs and expose only a bounded generic
	// state through the task API.
	publicMessage := "索引失败，请检查 embedding 服务配置或稍后重试"
	if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, ErrNotFound) {
		publicMessage = "知识库或文档已不存在"
	}
	retry := task.Attempts < maxIndexAttempts && !errors.Is(err, ErrConflict) && !errors.Is(err, gorm.ErrRecordNotFound)
	if updateErr := dao.FailIndexTask(task.ID, task.DocumentID, publicMessage, retry, task.Attempts); updateErr != nil && !errors.Is(updateErr, dao.ErrTaskSuperseded) {
		return fmt.Errorf("%v; update task failure state: %w", err, updateErr)
	}
	return err
}
