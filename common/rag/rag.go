package rag

import (
	redisPkg "GopherAI/common/redis"
	"GopherAI/config"
	"GopherAI/model"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	embeddingArk "github.com/cloudwego/eino-ext/components/embedding/ark"
	redisIndexer "github.com/cloudwego/eino-ext/components/indexer/redis"
	redisRetriever "github.com/cloudwego/eino-ext/components/retriever/redis"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
	redisCli "github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const defaultTopK = 5

type RAGIndexer struct {
	indexer *redisIndexer.Indexer
}

// KnowledgeBaseIndexer indexes multiple independently addressable documents
// into one user-owned knowledge base.
type KnowledgeBaseIndexer struct {
	indexer         *redisIndexer.Indexer
	keyPrefix       string
	knowledgeBaseID string
}

type RAGQuery struct {
	retrievers []retriever.Retriever
	topK       int
}

func newEmbedder(ctx context.Context, modelName string) (embedding.Embedder, error) {
	conf := config.GetConfig()
	apiKey, err := config.ResolveRAGAPIKey(conf.RagModelConfig.RagBaseUrl)
	if err != nil {
		return nil, fmt.Errorf("configure RAG embedding: %w", err)
	}
	if strings.TrimSpace(modelName) == "" {
		modelName = conf.RagModelConfig.RagEmbeddingModel
	}
	result, err := embeddingArk.NewEmbedder(ctx, &embeddingArk.EmbeddingConfig{
		BaseURL: conf.RagModelConfig.RagBaseUrl,
		APIKey:  apiKey,
		Model:   modelName,
	})
	if err != nil {
		return nil, fmt.Errorf("create RAG embedder: %w", err)
	}
	return result, nil
}

// NewRAGIndexer preserves the original single-file API while using the same
// Unicode-aware chunking and reference metadata as Knowledge Base 2.0.
func NewRAGIndexer(filename, embeddingModel string) (*RAGIndexer, error) {
	ctx := context.Background()
	embedder, err := newEmbedder(ctx, embeddingModel)
	if err != nil {
		return nil, err
	}
	conf := config.GetConfig()
	if err := redisPkg.InitRedisIndex(ctx, filename, conf.RagModelConfig.RagDimension); err != nil {
		return nil, fmt.Errorf("initialize legacy Redis index: %w", err)
	}
	indexer, err := redisIndexer.NewIndexer(ctx, &redisIndexer.IndexerConfig{
		Client:    redisPkg.Rdb,
		KeyPrefix: redisPkg.GenerateIndexNamePrefix(filename),
		BatchSize: 10,
		Embedding: embedder,
		DocumentToHashes: func(_ context.Context, doc *schema.Document) (*redisIndexer.Hashes, error) {
			metadata, err := json.Marshal(doc.MetaData)
			if err != nil {
				return nil, err
			}
			return &redisIndexer.Hashes{
				Key: doc.ID,
				Field2Value: map[string]redisIndexer.FieldValue{
					"content":  {Value: doc.Content, EmbedKey: "vector"},
					"metadata": {Value: string(metadata)},
				},
			}, nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create legacy Redis indexer: %w", err)
	}
	return &RAGIndexer{indexer: indexer}, nil
}

func (r *RAGIndexer) IndexFile(ctx context.Context, filePath string) error {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read RAG file: %w", err)
	}
	if !utf8.Valid(content) {
		return errors.New("document is not valid UTF-8 text")
	}
	documentID := filepath.Base(filePath)
	chunks := ChunkMarkdown(documentID, string(content), ChunkOptions{})
	if len(chunks) == 0 {
		return errors.New("document contains no indexable text")
	}
	docs := make([]*schema.Document, 0, len(chunks))
	for _, chunk := range chunks {
		docs = append(docs, &schema.Document{
			ID:      chunk.ID,
			Content: chunk.Content,
			MetaData: map[string]any{
				"source":      filePath,
				"chunk_id":    chunk.ID,
				"chunk_index": chunk.Index,
				"heading":     chunk.Heading,
				"start_rune":  chunk.StartRune,
				"end_rune":    chunk.EndRune,
			},
		})
	}
	if _, err := r.indexer.Store(ctx, docs); err != nil {
		return fmt.Errorf("store RAG chunks: %w", err)
	}
	return nil
}

func DeleteIndex(ctx context.Context, filename string) error {
	err := redisPkg.DeleteRedisIndex(ctx, filename)
	if err != nil && !isUnknownIndexError(err) {
		return fmt.Errorf("delete legacy Redis index: %w", err)
	}
	return nil
}

func knowledgeIndexToken(userName, knowledgeBaseID string) string {
	sum := sha256.Sum256([]byte(userName + "\x00" + knowledgeBaseID))
	return hex.EncodeToString(sum[:16])
}

func knowledgeIndexName(userName, knowledgeBaseID string) string {
	return "rag_kb_" + knowledgeIndexToken(userName, knowledgeBaseID) + "_idx"
}

func knowledgeKeyPrefix(userName, knowledgeBaseID string) string {
	return "rag_kb:" + knowledgeIndexToken(userName, knowledgeBaseID) + ":"
}

func ensureKnowledgeIndex(ctx context.Context, userName, knowledgeBaseID string, dimension int) error {
	if redisPkg.Rdb == nil {
		return errors.New("redis is not initialized")
	}
	indexName := knowledgeIndexName(userName, knowledgeBaseID)
	if _, err := redisPkg.Rdb.Do(ctx, "FT.INFO", indexName).Result(); err == nil {
		return nil
	} else if !isUnknownIndexError(err) {
		return fmt.Errorf("inspect knowledge index: %w", err)
	}
	if dimension <= 0 {
		return errors.New("RAG embedding dimension must be greater than zero")
	}
	args := []any{
		"FT.CREATE", indexName,
		"ON", "HASH",
		"PREFIX", "1", knowledgeKeyPrefix(userName, knowledgeBaseID),
		"SCHEMA",
		"content", "TEXT",
		"metadata", "TEXT", "NOINDEX",
		"vector", "VECTOR", "FLAT", "6",
		"TYPE", "FLOAT32",
		"DIM", dimension,
		"DISTANCE_METRIC", "COSINE",
	}
	if err := redisPkg.Rdb.Do(ctx, args...).Err(); err != nil {
		// Concurrent workers may both observe a missing index. A second info
		// check turns that benign race into success.
		if _, infoErr := redisPkg.Rdb.Do(ctx, "FT.INFO", indexName).Result(); infoErr == nil {
			return nil
		}
		return fmt.Errorf("create knowledge index: %w", err)
	}
	return nil
}

func isUnknownIndexError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unknown index") || strings.Contains(message, "no such index")
}

func NewKnowledgeBaseIndexer(ctx context.Context, userName, knowledgeBaseID, embeddingModel string) (*KnowledgeBaseIndexer, error) {
	if strings.TrimSpace(userName) == "" || strings.TrimSpace(knowledgeBaseID) == "" {
		return nil, errors.New("knowledge base owner and ID are required")
	}
	embedder, err := newEmbedder(ctx, embeddingModel)
	if err != nil {
		return nil, err
	}
	if err := ensureKnowledgeIndex(ctx, userName, knowledgeBaseID, config.GetConfig().RagModelConfig.RagDimension); err != nil {
		return nil, err
	}
	prefix := knowledgeKeyPrefix(userName, knowledgeBaseID)
	indexer, err := redisIndexer.NewIndexer(ctx, &redisIndexer.IndexerConfig{
		Client:    redisPkg.Rdb,
		KeyPrefix: prefix,
		BatchSize: 16,
		Embedding: embedder,
		DocumentToHashes: func(_ context.Context, doc *schema.Document) (*redisIndexer.Hashes, error) {
			documentID, ok := doc.MetaData["document_id"].(string)
			if !ok || documentID == "" {
				return nil, errors.New("document_id metadata is required")
			}
			metadata, err := json.Marshal(doc.MetaData)
			if err != nil {
				return nil, fmt.Errorf("encode chunk metadata: %w", err)
			}
			return &redisIndexer.Hashes{
				Key: documentID + ":" + doc.ID,
				Field2Value: map[string]redisIndexer.FieldValue{
					"content":  {Value: doc.Content, EmbedKey: "vector"},
					"metadata": {Value: string(metadata)},
				},
			}, nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create knowledge indexer: %w", err)
	}
	return &KnowledgeBaseIndexer{
		indexer:         indexer,
		keyPrefix:       prefix,
		knowledgeBaseID: knowledgeBaseID,
	}, nil
}

func (r *KnowledgeBaseIndexer) IndexFile(ctx context.Context, documentID, documentName, filePath string) (int, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return 0, fmt.Errorf("read knowledge document: %w", err)
	}
	if !utf8.Valid(content) {
		return 0, errors.New("document is not valid UTF-8 text")
	}
	chunks := ChunkMarkdown(documentID, string(content), ChunkOptions{})
	if len(chunks) == 0 {
		return 0, errors.New("document contains no indexable text")
	}
	existing, err := scanKeys(ctx, r.keyPrefix+documentID+":*")
	if err != nil {
		return 0, fmt.Errorf("list existing document chunks: %w", err)
	}

	docs := make([]*schema.Document, 0, len(chunks))
	retained := make(map[string]struct{}, len(chunks))
	for _, chunk := range chunks {
		reference := model.KnowledgeReference{
			ChunkID:         chunk.ID,
			KnowledgeBaseID: r.knowledgeBaseID,
			DocumentID:      documentID,
			DocumentName:    documentName,
			Heading:         chunk.Heading,
			ChunkIndex:      chunk.Index,
			StartRune:       chunk.StartRune,
			EndRune:         chunk.EndRune,
		}
		docs = append(docs, &schema.Document{
			ID:      chunk.ID,
			Content: chunk.Content,
			MetaData: map[string]any{
				"document_id": documentID,
				"reference":   reference,
			},
		})
		retained[r.keyPrefix+documentID+":"+chunk.ID] = struct{}{}
	}
	if _, err := r.indexer.Store(ctx, docs); err != nil {
		return 0, fmt.Errorf("embed and store document chunks: %w", err)
	}

	stale := make([]string, 0)
	for _, key := range existing {
		if _, ok := retained[key]; !ok {
			stale = append(stale, key)
		}
	}
	if len(stale) > 0 {
		if err := redisPkg.Rdb.Unlink(ctx, stale...).Err(); err != nil {
			return 0, fmt.Errorf("remove stale document chunks: %w", err)
		}
	}
	return len(chunks), nil
}

func scanKeys(ctx context.Context, pattern string) ([]string, error) {
	if redisPkg.Rdb == nil {
		return nil, errors.New("redis is not initialized")
	}
	var cursor uint64
	keys := make([]string, 0)
	for {
		batch, next, err := redisPkg.Rdb.Scan(ctx, cursor, pattern, 200).Result()
		if err != nil {
			return nil, err
		}
		keys = append(keys, batch...)
		cursor = next
		if cursor == 0 {
			return keys, nil
		}
	}
}

func DeleteKnowledgeDocumentIndex(ctx context.Context, userName, knowledgeBaseID, documentID string) error {
	keys, err := scanKeys(ctx, knowledgeKeyPrefix(userName, knowledgeBaseID)+documentID+":*")
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	return redisPkg.Rdb.Unlink(ctx, keys...).Err()
}

func DeleteKnowledgeBaseIndex(ctx context.Context, userName, knowledgeBaseID string) error {
	if redisPkg.Rdb == nil {
		return errors.New("redis is not initialized")
	}
	err := redisPkg.Rdb.Do(ctx, "FT.DROPINDEX", knowledgeIndexName(userName, knowledgeBaseID), "DD").Err()
	if err != nil && !isUnknownIndexError(err) {
		return fmt.Errorf("delete knowledge base index: %w", err)
	}
	return nil
}

func newRedisRetriever(ctx context.Context, embedder embedding.Embedder, indexName string, topK int) (retriever.Retriever, error) {
	return redisRetriever.NewRetriever(ctx, &redisRetriever.RetrieverConfig{
		Client:       redisPkg.Rdb,
		Index:        indexName,
		Dialect:      2,
		ReturnFields: []string{"content", "metadata", "distance"},
		TopK:         topK,
		VectorField:  "vector",
		Embedding:    embedder,
		DocumentConverter: func(_ context.Context, doc redisCli.Document) (*schema.Document, error) {
			result := &schema.Document{ID: doc.ID, MetaData: map[string]any{}}
			if content, ok := doc.Fields["content"]; ok {
				result.Content = content
			}
			if metadata, ok := doc.Fields["metadata"]; ok && metadata != "" {
				var decoded map[string]any
				if err := json.Unmarshal([]byte(metadata), &decoded); err == nil {
					for key, value := range decoded {
						result.MetaData[key] = value
					}
				} else {
					result.MetaData["source"] = metadata
				}
			}
			if distanceText, ok := doc.Fields["distance"]; ok {
				if distance, err := strconv.ParseFloat(distanceText, 64); err == nil {
					result.MetaData["distance"] = distance
					result.MetaData["score"] = max(0, 1-distance)
				}
			}
			if reference, ok := ReferenceFromDocument(result); ok {
				result.ID = reference.ChunkID
			}
			return result, nil
		},
	})
}

func NewKnowledgeBaseQuery(ctx context.Context, userName, knowledgeBaseID string, topK int) (*RAGQuery, error) {
	return NewKnowledgeBasesQuery(ctx, userName, []string{knowledgeBaseID}, topK)
}

// NewKnowledgeBasesQuery creates one logical query across selected indexes.
// Callers that accept IDs from an HTTP request must verify ownership first;
// service/knowledgebase.Retrieve performs that check.
func NewKnowledgeBasesQuery(ctx context.Context, userName string, knowledgeBaseIDs []string, topK int) (*RAGQuery, error) {
	if redisPkg.Rdb == nil {
		return nil, errors.New("redis is not initialized")
	}
	if strings.TrimSpace(userName) == "" || len(knowledgeBaseIDs) == 0 {
		return nil, errors.New("knowledge base owner and IDs are required")
	}
	if topK <= 0 {
		topK = defaultTopK
	}
	embedder, err := newEmbedder(ctx, config.GetConfig().RagModelConfig.RagEmbeddingModel)
	if err != nil {
		return nil, err
	}
	result := &RAGQuery{retrievers: make([]retriever.Retriever, 0, len(knowledgeBaseIDs)), topK: topK}
	seen := make(map[string]struct{}, len(knowledgeBaseIDs))
	for _, knowledgeBaseID := range knowledgeBaseIDs {
		knowledgeBaseID = strings.TrimSpace(knowledgeBaseID)
		if knowledgeBaseID == "" {
			return nil, errors.New("knowledge base ID is required")
		}
		if _, exists := seen[knowledgeBaseID]; exists {
			continue
		}
		seen[knowledgeBaseID] = struct{}{}
		rtr, err := newRedisRetriever(ctx, embedder, knowledgeIndexName(userName, knowledgeBaseID), topK)
		if err != nil {
			return nil, fmt.Errorf("create knowledge retriever: %w", err)
		}
		result.retrievers = append(result.retrievers, rtr)
	}
	return result, nil
}

// NewRAGQuery preserves the chat model's username-only contract by searching
// all ready knowledge bases owned by that user. Legacy per-file indexes are
// used only when no Knowledge Base 2.0 records exist.
func NewRAGQuery(ctx context.Context, userName string) (*RAGQuery, error) {
	if redisPkg.Rdb == nil {
		return nil, errors.New("redis is not initialized")
	}
	embedder, err := newEmbedder(ctx, config.GetConfig().RagModelConfig.RagEmbeddingModel)
	if err != nil {
		return nil, err
	}
	result := &RAGQuery{topK: defaultTopK}

	if bases, listErr := listReadyBases(userName); listErr == nil {
		for _, base := range bases {
			rtr, createErr := newRedisRetriever(ctx, embedder, knowledgeIndexName(userName, base.ID), defaultTopK)
			if createErr != nil {
				return nil, createErr
			}
			result.retrievers = append(result.retrievers, rtr)
		}
	}
	// Compatibility for files uploaded before the KB2 schema existed. Keep
	// these sources searchable during gradual migration, even after the user
	// creates a new knowledge base.
	if filepath.Base(userName) == userName {
		userDir := filepath.Join(config.GetConfig().RagModelConfig.RagDocDir, userName)
		files, readErr := os.ReadDir(userDir)
		if readErr == nil {
			for _, file := range files {
				if file.IsDir() {
					continue
				}
				rtr, createErr := newRedisRetriever(ctx, embedder, redisPkg.GenerateIndexName(file.Name()), defaultTopK)
				if createErr != nil {
					return nil, createErr
				}
				result.retrievers = append(result.retrievers, rtr)
			}
		}
	}
	return result, nil
}

func listReadyBases(userName string) ([]model.KnowledgeBase, error) {
	// Kept local to the RAG package to avoid coupling the chat model to service
	// code. mysql.DB is intentionally accessed through the shared initialized DB.
	db, err := currentDatabase()
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

var databaseProvider = func() *gorm.DB {
	// Imported lazily through a tiny bridge in database.go. This variable also
	// makes the no-database compatibility path straightforward to unit test.
	return ragDatabase()
}

func currentDatabase() (*gorm.DB, error) {
	db := databaseProvider()
	if db == nil {
		return nil, errors.New("mysql is not initialized")
	}
	return db, nil
}

func (r *RAGQuery) RetrieveDocuments(ctx context.Context, query string) ([]*schema.Document, error) {
	if strings.TrimSpace(query) == "" || len(r.retrievers) == 0 {
		return []*schema.Document{}, nil
	}
	all := make([]*schema.Document, 0, len(r.retrievers)*r.topK)
	for _, source := range r.retrievers {
		docs, err := source.Retrieve(ctx, query)
		if err != nil {
			if isUnknownIndexError(err) {
				continue
			}
			return nil, fmt.Errorf("retrieve knowledge documents: %w", err)
		}
		all = append(all, docs...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		return documentDistance(all[i]) < documentDistance(all[j])
	})
	if len(all) > r.topK {
		all = all[:r.topK]
	}
	return all, nil
}

func documentDistance(document *schema.Document) float64 {
	if document == nil || document.MetaData == nil {
		return 2
	}
	switch value := document.MetaData["distance"].(type) {
	case float64:
		return value
	case string:
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			return parsed
		}
	}
	return 2
}

func ReferenceFromDocument(document *schema.Document) (model.KnowledgeReference, bool) {
	if document == nil || document.MetaData == nil {
		return model.KnowledgeReference{}, false
	}
	value, exists := document.MetaData["reference"]
	var reference model.KnowledgeReference
	if exists {
		encoded, err := json.Marshal(value)
		if err == nil {
			_ = json.Unmarshal(encoded, &reference)
		}
	}
	if reference.ChunkID == "" {
		// Files indexed by the legacy upload endpoint predate the structured
		// reference object. Synthesize the same public shape from their metadata
		// so prompt numbers and returned citations remain one-to-one.
		reference = model.KnowledgeReference{
			ChunkID:         firstMetadataString(document.MetaData, "chunk_id", "id"),
			KnowledgeBaseID: firstMetadataString(document.MetaData, "knowledge_base_id"),
			DocumentID:      firstMetadataString(document.MetaData, "document_id"),
			Heading:         firstMetadataString(document.MetaData, "heading"),
			ChunkIndex:      metadataInt(document.MetaData["chunk_index"]),
			StartRune:       metadataInt(document.MetaData["start_rune"]),
			EndRune:         metadataInt(document.MetaData["end_rune"]),
		}
		if reference.ChunkID == "" {
			reference.ChunkID = document.ID
		}
		source := firstMetadataString(document.MetaData, "source")
		if source != "" {
			reference.DocumentName = filepath.Base(source)
		}
		if reference.DocumentName == "" {
			reference.DocumentName = firstMetadataString(document.MetaData, "document_name", "filename")
		}
		if reference.ChunkID == "" {
			return model.KnowledgeReference{}, false
		}
	}
	reference.Content = document.Content
	if score, ok := metadataFloat(document.MetaData["score"]); ok {
		reference.Score = score
	}
	return reference, true
}

func firstMetadataString(metadata map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func metadataInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := strconv.Atoi(typed.String())
		return parsed
	case string:
		parsed, _ := strconv.Atoi(typed)
		return parsed
	default:
		return 0
	}
}

func metadataFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func ReferencesFromDocuments(documents []*schema.Document) []model.KnowledgeReference {
	result := make([]model.KnowledgeReference, 0, len(documents))
	for _, document := range documents {
		if reference, ok := ReferenceFromDocument(document); ok {
			result = append(result, reference)
		}
	}
	return result
}

func BuildRAGPrompt(query string, documents []*schema.Document) string {
	if len(documents) == 0 {
		return query
	}
	var contextText strings.Builder
	for index, document := range documents {
		label := fmt.Sprintf("资料 %d", index+1)
		if reference, ok := ReferenceFromDocument(document); ok {
			label = reference.DocumentName
			if reference.Heading != "" {
				label += " / " + reference.Heading
			}
		}
		fmt.Fprintf(&contextText, "[%d] %s\n%s\n\n", index+1, label, document.Content)
	}
	return fmt.Sprintf(`请仅根据以下参考资料回答问题。引用事实时使用 [1]、[2] 这样的编号标明来源；资料不足时请明确说明。

参考资料：
%s
用户问题：%s`, contextText.String(), query)
}
