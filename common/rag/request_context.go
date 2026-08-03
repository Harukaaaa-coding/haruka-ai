package rag

import (
	"context"
	"sync"

	"GopherAI/model"
)

type chatRequestKey struct{}

// ChatRequest carries per-request knowledge-base selection and captures the
// exact references used by the RAG model. It is concurrency-safe so streaming
// handlers can read the final references after generation completes.
type ChatRequest struct {
	KnowledgeBaseIDs        []string
	KnowledgeBasesSpecified bool

	mu         sync.RWMutex
	references []model.KnowledgeReference
}

func NewChatRequest(knowledgeBaseIDs []string) *ChatRequest {
	return NewChatRequestWithSelection(knowledgeBaseIDs, len(knowledgeBaseIDs) > 0)
}

// NewChatRequestWithSelection distinguishes an omitted legacy field from an
// explicit empty list, which means "search all of my knowledge bases" in the
// current API.
func NewChatRequestWithSelection(knowledgeBaseIDs []string, specified bool) *ChatRequest {
	return &ChatRequest{
		KnowledgeBaseIDs:        append([]string(nil), knowledgeBaseIDs...),
		KnowledgeBasesSpecified: specified,
	}
}

func WithChatRequest(ctx context.Context, request *ChatRequest) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if request == nil {
		return ctx
	}
	return context.WithValue(ctx, chatRequestKey{}, request)
}

func ChatRequestFromContext(ctx context.Context) *ChatRequest {
	if ctx == nil {
		return nil
	}
	request, _ := ctx.Value(chatRequestKey{}).(*ChatRequest)
	return request
}

func (r *ChatRequest) SetReferences(references []model.KnowledgeReference) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.references = append([]model.KnowledgeReference(nil), references...)
}

func (r *ChatRequest) References() []model.KnowledgeReference {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]model.KnowledgeReference(nil), r.references...)
}
