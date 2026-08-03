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
	"sync"
	"time"

	"gorm.io/gorm"
)

const (
	maxIndexAttempts = 3
	workerPollPeriod = 2 * time.Second
	staleTaskAge     = 10 * time.Minute
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
		}
	}
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
		return dao.CompleteIndexTask(task.ID, document.ID, chunkCount)
	})
	if err == nil {
		return nil
	}

	// Keep provider details in server logs and expose only a bounded generic
	// state through the task API.
	publicMessage := "索引失败，请检查 embedding 服务配置或稍后重试"
	if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, ErrNotFound) {
		publicMessage = "知识库或文档已不存在"
	}
	retry := task.Attempts < maxIndexAttempts && !errors.Is(err, ErrConflict) && !errors.Is(err, gorm.ErrRecordNotFound)
	if updateErr := dao.FailIndexTask(task.ID, task.DocumentID, publicMessage, retry); updateErr != nil {
		return fmt.Errorf("%v; update task failure state: %w", err, updateErr)
	}
	return err
}
