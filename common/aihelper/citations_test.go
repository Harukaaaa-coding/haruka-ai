package aihelper

import (
	"context"
	"testing"

	"GopherAI/common/rag"
	appmodel "GopherAI/model"

	"github.com/cloudwego/eino/schema"
)

type citationTestModel struct{}

func (*citationTestModel) GenerateResponse(context.Context, []*schema.Message) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "answer"}, nil
}

func (*citationTestModel) StreamResponse(context.Context, []*schema.Message, StreamCallback) (string, error) {
	return "answer", nil
}

func (*citationTestModel) GetModelType() string { return "test" }

func TestGenerateResponsePersistsRequestCitations(t *testing.T) {
	helper := NewAIHelper(&citationTestModel{}, "session")
	var saved []*appmodel.Message
	helper.SetSaveFunc(func(message *appmodel.Message) (*appmodel.Message, error) {
		copy := *message
		saved = append(saved, &copy)
		return message, nil
	})
	request := rag.NewChatRequestWithSelection([]string{"kb"}, true)
	request.SetReferences([]appmodel.KnowledgeReference{{ChunkID: "chunk", DocumentName: "guide.md"}})
	if _, err := helper.GenerateResponse("user", rag.WithChatRequest(context.Background(), request), "question"); err != nil {
		t.Fatalf("generate response: %v", err)
	}
	if len(saved) != 2 || len(saved[1].KnowledgeReferences()) != 1 || saved[1].KnowledgeReferences()[0].ChunkID != "chunk" {
		t.Fatalf("assistant citations were not persisted: %#v", saved)
	}
}

func TestRestoreMessageKeepsCitations(t *testing.T) {
	helper := NewAIHelper(&citationTestModel{}, "session")
	persisted := &appmodel.Message{SessionID: "session", Content: "answer", IsUser: false}
	if err := persisted.SetKnowledgeReferences([]appmodel.KnowledgeReference{{ChunkID: "restored"}}); err != nil {
		t.Fatalf("encode fixture citations: %v", err)
	}
	if err := helper.RestoreMessage(persisted); err != nil {
		t.Fatalf("restore message: %v", err)
	}
	persisted.Citations = ""
	messages := helper.GetMessages()
	if len(messages) != 1 || len(messages[0].KnowledgeReferences()) != 1 || messages[0].KnowledgeReferences()[0].ChunkID != "restored" {
		t.Fatalf("restored citations were lost: %#v", messages)
	}
}
