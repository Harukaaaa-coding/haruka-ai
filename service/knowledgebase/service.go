package knowledgebase

import (
	"GopherAI/common/rag"
	"GopherAI/config"
	dao "GopherAI/dao/knowledgebase"
	"GopherAI/model"
	"GopherAI/utils"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	DefaultKnowledgeBaseName = "默认知识库"
	MaxDocumentBytes         = 20 << 20
	MaxPageSize              = 100
	MaxSearchQueryRunes      = 16_000
)

var (
	ErrInvalidInput = errors.New("invalid knowledge base input")
	ErrNotFound     = errors.New("knowledge base resource not found")
	ErrConflict     = errors.New("knowledge base resource is not active")
)

type KnowledgeBaseDetail struct {
	KnowledgeBase model.KnowledgeBase       `json:"knowledge_base"`
	Documents     []model.KnowledgeDocument `json:"documents"`
}

type DocumentStatus struct {
	Document model.KnowledgeDocument   `json:"document"`
	Task     *model.KnowledgeIndexTask `json:"index_task,omitempty"`
}

type UploadResult struct {
	Document model.KnowledgeDocument  `json:"document"`
	Task     model.KnowledgeIndexTask `json:"index_task"`
}

// RetrievalResult is an internal chat integration contract: Documents feed
// rag.BuildRAGPrompt while References can be returned to the client as citations.
type RetrievalResult struct {
	Documents  []*schema.Document
	References []model.KnowledgeReference
}

var documentLocks [64]sync.Mutex

func withDocumentLock(documentID string, fn func() error) error {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(documentID))
	lock := &documentLocks[int(hash.Sum32())%len(documentLocks)]
	lock.Lock()
	defer lock.Unlock()
	return fn()
}

func Create(userName, name, description string) (*model.KnowledgeBase, error) {
	userName = strings.TrimSpace(userName)
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if userName == "" || name == "" || utf8.RuneCountInString(name) > 100 || utf8.RuneCountInString(description) > 1000 {
		return nil, ErrInvalidInput
	}
	base := &model.KnowledgeBase{
		ID:          uuid.NewString(),
		UserName:    userName,
		Name:        name,
		Description: description,
		Status:      model.KnowledgeBaseStatusActive,
	}
	if err := dao.CreateKnowledgeBase(base); err != nil {
		return nil, err
	}
	return base, nil
}

func List(userName string, page, pageSize int) ([]model.KnowledgeBaseSummary, error) {
	if strings.TrimSpace(userName) == "" {
		return nil, ErrInvalidInput
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > MaxPageSize {
		pageSize = MaxPageSize
	}
	return dao.ListKnowledgeBases(userName, (page-1)*pageSize, pageSize)
}

func Get(userName, knowledgeBaseID string) (*KnowledgeBaseDetail, error) {
	base, err := ownedBase(userName, knowledgeBaseID)
	if err != nil {
		return nil, err
	}
	documents, err := dao.ListDocuments(userName, base.ID)
	if err != nil {
		return nil, err
	}
	return &KnowledgeBaseDetail{KnowledgeBase: *base, Documents: documents}, nil
}

func ListDocuments(userName, knowledgeBaseID string) ([]model.KnowledgeDocument, error) {
	base, err := ownedBase(userName, knowledgeBaseID)
	if err != nil {
		return nil, err
	}
	return dao.ListDocuments(userName, base.ID)
}

func GetDocumentStatus(userName, knowledgeBaseID, documentID string) (*DocumentStatus, error) {
	if _, err := ownedBase(userName, knowledgeBaseID); err != nil {
		return nil, err
	}
	document, err := dao.GetDocument(userName, knowledgeBaseID, documentID)
	if err != nil {
		return nil, normalizeNotFound(err)
	}
	task, err := dao.GetLatestDocumentTask(userName, knowledgeBaseID, documentID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		task = nil
	} else if err != nil {
		return nil, err
	}
	return &DocumentStatus{Document: *document, Task: task}, nil
}

func GetIndexTask(userName, taskID string) (*model.KnowledgeIndexTask, error) {
	if strings.TrimSpace(userName) == "" || strings.TrimSpace(taskID) == "" {
		return nil, ErrInvalidInput
	}
	task, err := dao.GetIndexTask(userName, taskID)
	if err != nil {
		return nil, normalizeNotFound(err)
	}
	return task, nil
}

func UploadDocument(ctx context.Context, userName, knowledgeBaseID string, header *multipart.FileHeader) (*UploadResult, error) {
	return uploadDocument(ctx, userName, knowledgeBaseID, header, true)
}

func uploadDocument(ctx context.Context, userName, knowledgeBaseID string, header *multipart.FileHeader, async bool) (*UploadResult, error) {
	if header == nil || header.Size == 0 || header.Size > MaxDocumentBytes {
		return nil, ErrInvalidInput
	}
	if err := utils.ValidateFile(header); err != nil {
		return nil, fmt.Errorf("%w: unsupported document type", ErrInvalidInput)
	}
	base, err := ownedBase(userName, knowledgeBaseID)
	if err != nil {
		return nil, err
	}
	if base.Status != model.KnowledgeBaseStatusActive {
		return nil, ErrConflict
	}

	documentID := uuid.NewString()
	path, size, checksum, err := persistUpload(header, userName, base.ID, documentID)
	if err != nil {
		return nil, err
	}
	document := model.KnowledgeDocument{
		ID:              documentID,
		KnowledgeBaseID: base.ID,
		UserName:        userName,
		OriginalName:    safeOriginalName(header.Filename),
		StoragePath:     path,
		ContentType:     header.Header.Get("Content-Type"),
		Size:            size,
		Checksum:        checksum,
		Status:          model.DocumentStatusPending,
	}
	task := model.KnowledgeIndexTask{
		ID:              uuid.NewString(),
		KnowledgeBaseID: base.ID,
		DocumentID:      document.ID,
		UserName:        userName,
		Type:            model.IndexTaskTypeIndex,
		Status:          model.IndexTaskStatusPending,
	}
	if err := dao.CreateDocumentAndTask(&document, &task); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	result := &UploadResult{Document: document, Task: task}
	if async {
		EnqueueIndexTask(task.ID)
		return result, nil
	}
	if err := ProcessIndexTaskNow(ctx, task.ID); err != nil {
		return result, err
	}
	updated, err := GetDocumentStatus(userName, base.ID, document.ID)
	if err == nil {
		result.Document = updated.Document
		if updated.Task != nil {
			result.Task = *updated.Task
		}
	}
	return result, nil
}

func UploadLegacy(ctx context.Context, userName string, header *multipart.FileHeader) (string, error) {
	base, err := dao.FindKnowledgeBaseByName(userName, DefaultKnowledgeBaseName)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		base, err = Create(userName, DefaultKnowledgeBaseName, "由兼容上传接口自动创建")
	}
	if err != nil {
		return "", err
	}
	result, err := uploadDocument(ctx, userName, base.ID, header, false)
	if err != nil {
		return "", err
	}
	return result.Document.StoragePath, nil
}

func Search(ctx context.Context, userName, knowledgeBaseID, query string, topK int) ([]model.KnowledgeReference, error) {
	result, err := Retrieve(ctx, userName, []string{knowledgeBaseID}, query, topK)
	if err != nil {
		return nil, err
	}
	return result.References, nil
}

// Retrieve searches selected knowledge bases after verifying every ID belongs
// to the authenticated user. It is the preferred integration point for chat
// requests carrying knowledgeBaseIds.
func Retrieve(ctx context.Context, userName string, knowledgeBaseIDs []string, query string, topK int) (*RetrievalResult, error) {
	query = strings.TrimSpace(query)
	if query == "" || !utf8.ValidString(query) || utf8.RuneCountInString(query) > MaxSearchQueryRunes || len(knowledgeBaseIDs) == 0 || len(knowledgeBaseIDs) > 20 {
		return nil, ErrInvalidInput
	}
	ownedIDs := make([]string, 0, len(knowledgeBaseIDs))
	seen := make(map[string]struct{}, len(knowledgeBaseIDs))
	for _, knowledgeBaseID := range knowledgeBaseIDs {
		if _, exists := seen[knowledgeBaseID]; exists {
			continue
		}
		base, err := ownedBase(userName, knowledgeBaseID)
		if err != nil {
			return nil, err
		}
		if base.Status != model.KnowledgeBaseStatusActive {
			return nil, ErrConflict
		}
		seen[knowledgeBaseID] = struct{}{}
		ownedIDs = append(ownedIDs, base.ID)
	}
	if topK <= 0 {
		topK = 5
	}
	if topK > 20 {
		topK = 20
	}
	queryEngine, err := rag.NewKnowledgeBasesQuery(ctx, userName, ownedIDs, topK)
	if err != nil {
		return nil, err
	}
	documents, err := queryEngine.RetrieveDocuments(ctx, query)
	if err != nil {
		return nil, err
	}
	return &RetrievalResult{
		Documents:  documents,
		References: rag.ReferencesFromDocuments(documents),
	}, nil
}

func DeleteDocument(ctx context.Context, userName, knowledgeBaseID, documentID string) error {
	if _, err := ownedBase(userName, knowledgeBaseID); err != nil {
		return err
	}
	return withDocumentLock(documentID, func() error {
		document, err := dao.GetDocument(userName, knowledgeBaseID, documentID)
		if err != nil {
			return normalizeNotFound(err)
		}
		if err := rag.DeleteKnowledgeDocumentIndex(ctx, userName, knowledgeBaseID, documentID); err != nil {
			return err
		}
		if err := dao.DeleteDocumentRecords(userName, knowledgeBaseID, documentID); err != nil {
			return normalizeNotFound(err)
		}
		if err := os.Remove(document.StoragePath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove source document: %w", err)
		}
		return nil
	})
}

func Delete(ctx context.Context, userName, knowledgeBaseID string) error {
	base, err := ownedBase(userName, knowledgeBaseID)
	if err != nil {
		return err
	}
	if err := dao.MarkKnowledgeBaseDeleting(userName, base.ID); err != nil {
		return normalizeNotFound(err)
	}
	documents, err := dao.ListDocuments(userName, base.ID)
	if err != nil {
		_ = dao.RestoreKnowledgeBaseActive(userName, base.ID)
		return err
	}
	for _, document := range documents {
		document := document
		if err := withDocumentLock(document.ID, func() error {
			if err := rag.DeleteKnowledgeDocumentIndex(ctx, userName, base.ID, document.ID); err != nil {
				return err
			}
			if err := os.Remove(document.StoragePath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove source document: %w", err)
			}
			return nil
		}); err != nil {
			_ = dao.RestoreKnowledgeBaseActive(userName, base.ID)
			return err
		}
	}
	if err := rag.DeleteKnowledgeBaseIndex(ctx, userName, base.ID); err != nil {
		_ = dao.RestoreKnowledgeBaseActive(userName, base.ID)
		return err
	}
	if err := dao.DeleteKnowledgeBaseRecords(userName, base.ID); err != nil {
		_ = dao.RestoreKnowledgeBaseActive(userName, base.ID)
		return normalizeNotFound(err)
	}
	return nil
}

func ownedBase(userName, knowledgeBaseID string) (*model.KnowledgeBase, error) {
	if strings.TrimSpace(userName) == "" || strings.TrimSpace(knowledgeBaseID) == "" {
		return nil, ErrInvalidInput
	}
	base, err := dao.GetKnowledgeBase(userName, knowledgeBaseID)
	if err != nil {
		return nil, normalizeNotFound(err)
	}
	return base, nil
}

func normalizeNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

func persistUpload(header *multipart.FileHeader, userName, knowledgeBaseID, documentID string) (path string, size int64, checksum string, err error) {
	root := strings.TrimSpace(config.GetConfig().RagModelConfig.RagDocDir)
	if root == "" {
		root = "uploads"
	}
	ownerHash := sha256.Sum256([]byte(userName))
	ownerDirectory := hex.EncodeToString(ownerHash[:8])
	directory := filepath.Join(root, "knowledge-bases", ownerDirectory, knowledgeBaseID)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return "", 0, "", fmt.Errorf("create document directory: %w", err)
	}
	extension := strings.ToLower(filepath.Ext(header.Filename))
	path = filepath.Join(directory, documentID+extension)
	temporaryPath := path + ".uploading"

	source, err := header.Open()
	if err != nil {
		return "", 0, "", fmt.Errorf("open uploaded document: %w", err)
	}
	defer source.Close()
	destination, err := os.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return "", 0, "", fmt.Errorf("create uploaded document: %w", err)
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(source, MaxDocumentBytes+1))
	closeErr := destination.Close()
	if copyErr != nil || closeErr != nil || written == 0 || written > MaxDocumentBytes {
		_ = os.Remove(temporaryPath)
		if written > MaxDocumentBytes || written == 0 {
			return "", 0, "", ErrInvalidInput
		}
		if copyErr != nil {
			return "", 0, "", fmt.Errorf("store uploaded document: %w", copyErr)
		}
		return "", 0, "", fmt.Errorf("close uploaded document: %w", closeErr)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return "", 0, "", fmt.Errorf("finalize uploaded document: %w", err)
	}
	return path, written, hex.EncodeToString(hash.Sum(nil)), nil
}

func safeOriginalName(name string) string {
	name = strings.TrimSpace(filepath.Base(name))
	if name == "" || name == "." {
		return "document.txt"
	}
	runes := []rune(name)
	if len(runes) > 255 {
		name = string(runes[:255])
	}
	return name
}
