package rag

import (
	"GopherAI/model"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestKnowledgeIndexNamespaceIsOwnerScopedAndStable(t *testing.T) {
	first := knowledgeIndexToken("alice", "kb-1")
	if first != knowledgeIndexToken("alice", "kb-1") {
		t.Fatal("knowledge index token is not stable")
	}
	if first == knowledgeIndexToken("bob", "kb-1") {
		t.Fatal("different owners share an index token")
	}
	if first == knowledgeIndexToken("alice", "kb-2") {
		t.Fatal("different knowledge bases share an index token")
	}
}

func TestReferencesAndPromptExposeSourceLocation(t *testing.T) {
	reference := model.KnowledgeReference{
		ChunkID:         "chunk-1",
		KnowledgeBaseID: "kb-1",
		DocumentID:      "doc-1",
		DocumentName:    "guide.md",
		Heading:         "安装 > Windows",
		ChunkIndex:      2,
		StartRune:       120,
		EndRune:         240,
	}
	document := &schema.Document{
		ID:      "chunk-1",
		Content: "运行安装器。",
		MetaData: map[string]any{
			"reference": reference,
			"score":     0.92,
		},
	}

	result := ReferencesFromDocuments([]*schema.Document{document})
	if len(result) != 1 {
		t.Fatalf("expected one reference, got %d", len(result))
	}
	if result[0].Content != document.Content || result[0].Score != 0.92 {
		t.Fatalf("reference did not carry retrieval data: %+v", result[0])
	}
	prompt := BuildRAGPrompt("怎么安装？", []*schema.Document{document})
	for _, expected := range []string{"[1]", "guide.md", "安装 > Windows", "运行安装器。", "怎么安装？"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt does not contain %q: %s", expected, prompt)
		}
	}
}

func TestLegacyDocumentGetsPositionPreservingReference(t *testing.T) {
	document := &schema.Document{
		ID:      "legacy-chunk",
		Content: "旧索引内容",
		MetaData: map[string]any{
			"source":      `C:\\uploads\\legacy.md`,
			"chunk_id":    "legacy-chunk",
			"chunk_index": float64(1),
			"start_rune":  "12",
			"end_rune":    float64(24),
			"score":       "0.8",
		},
	}
	references := ReferencesFromDocuments([]*schema.Document{document})
	if len(references) != 1 {
		t.Fatalf("legacy document was dropped from citations: %#v", references)
	}
	if references[0].ChunkID != "legacy-chunk" || references[0].ChunkIndex != 1 || references[0].StartRune != 12 || references[0].Score != 0.8 {
		t.Fatalf("legacy metadata was not preserved: %#v", references[0])
	}
	if !strings.Contains(BuildRAGPrompt("问题", []*schema.Document{document}), "[1]") {
		t.Fatal("legacy document lost its prompt position")
	}
}
