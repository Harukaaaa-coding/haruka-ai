package rag

import (
	"context"
	"testing"

	"GopherAI/model"
)

func TestChatRequestContextCopiesReferences(t *testing.T) {
	request := NewChatRequest([]string{"kb-1"})
	ctx := WithChatRequest(context.Background(), request)
	got := ChatRequestFromContext(ctx)
	if got == nil || len(got.KnowledgeBaseIDs) != 1 {
		t.Fatal("chat request was not stored in context")
	}
	references := []model.KnowledgeReference{{ChunkID: "chunk-1"}}
	got.SetReferences(references)
	references[0].ChunkID = "changed"
	if request.References()[0].ChunkID != "chunk-1" {
		t.Fatal("references were not copied")
	}
}

func TestExplicitAllKnowledgeBasesRemainsDistinguishable(t *testing.T) {
	request := NewChatRequestWithSelection([]string{}, true)
	if !request.KnowledgeBasesSpecified || len(request.KnowledgeBaseIDs) != 0 {
		t.Fatalf("explicit all selection was lost: %#v", request)
	}
	legacy := NewChatRequest(nil)
	if legacy.KnowledgeBasesSpecified {
		t.Fatal("omitted legacy selection should not become strict")
	}
}
