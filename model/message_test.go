package model

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestMessageKnowledgeReferencesRoundTrip(t *testing.T) {
	message := new(Message)
	want := []KnowledgeReference{{ChunkID: "chunk-1", DocumentName: "guide.md", Content: "content"}}
	if err := message.SetKnowledgeReferences(want); err != nil {
		t.Fatalf("encode references: %v", err)
	}
	got := message.KnowledgeReferences()
	if len(got) != 1 || got[0].ChunkID != want[0].ChunkID || got[0].DocumentName != want[0].DocumentName {
		t.Fatalf("unexpected decoded references: %#v", got)
	}
	got[0].ChunkID = "changed"
	if message.KnowledgeReferences()[0].ChunkID != "chunk-1" {
		t.Fatal("decoded references should not mutate persisted JSON")
	}
}

func TestMessageEnsureMessageIDIsStableAndCanonical(t *testing.T) {
	message := &Message{}
	first := message.EnsureMessageID()
	if first == "" {
		t.Fatal("expected a generated message ID")
	}
	if _, err := uuid.Parse(first); err != nil {
		t.Fatalf("generated message ID is not a UUID: %v", err)
	}
	if second := message.EnsureMessageID(); second != first {
		t.Fatalf("message ID changed across retries: first=%q second=%q", first, second)
	}

	message.MessageID = strings.ToUpper(uuid.MustParse(first).String())
	if got := message.EnsureMessageID(); got != first {
		t.Fatalf("canonical message ID = %q, want %q", got, first)
	}
}

func TestMessageBeforeCreateRepairsInvalidMessageID(t *testing.T) {
	message := &Message{MessageID: "not-a-uuid"}
	if err := message.BeforeCreate(nil); err != nil {
		t.Fatalf("before create: %v", err)
	}
	if _, err := uuid.Parse(message.MessageID); err != nil {
		t.Fatalf("invalid ID was not repaired: %q", message.MessageID)
	}
}
