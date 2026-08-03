package aihelper

import (
	"fmt"
	"testing"

	"GopherAI/model"
)

func TestRestoreMessageKeepsBoundedRecentHistory(t *testing.T) {
	helper := NewAIHelper(nil, "session")
	for index := 0; index <= maxInMemoryHistoryMessages; index++ {
		if err := helper.RestoreMessage(&model.Message{
			SessionID: "session",
			Content:   fmt.Sprintf("message-%d", index),
		}); err != nil {
			t.Fatalf("RestoreMessage(%d): %v", index, err)
		}
	}

	messages := helper.GetMessages()
	if len(messages) != maxInMemoryHistoryMessages {
		t.Fatalf("message count = %d, want %d", len(messages), maxInMemoryHistoryMessages)
	}
	if messages[0].Content != "message-1" || messages[len(messages)-1].Content != fmt.Sprintf("message-%d", maxInMemoryHistoryMessages) {
		t.Fatalf("history tail = first %q last %q", messages[0].Content, messages[len(messages)-1].Content)
	}
}
