package aihelper

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestPrepareRAGMessagesSeparatesSystemInstruction(t *testing.T) {
	messages := []*schema.Message{{Role: schema.User, Content: "question"}}
	prepared := prepareRAGMessages(messages, "grounded prompt")
	if len(prepared) != 2 || prepared[0].Role != schema.System || !strings.Contains(prepared[0].Content, "不可信") {
		t.Fatalf("unexpected system prompt: %#v", prepared)
	}
	if prepared[1].Role != schema.User || prepared[1].Content != "grounded prompt" {
		t.Fatalf("unexpected grounded user prompt: %#v", prepared)
	}
}
