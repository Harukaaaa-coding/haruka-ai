package rag

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkMarkdownUnicodeAndStableIDs(t *testing.T) {
	content := "# 概览\n\n" + strings.Repeat("这是中文内容🙂，用于验证 Unicode 切块。", 40) +
		"\n\n## 细节\n\n" + strings.Repeat("第二部分包含更多资料。", 30)
	options := ChunkOptions{MaxRunes: 180, MinRunes: 90, OverlapRunes: 24}

	first := ChunkMarkdown("document-1", content, options)
	second := ChunkMarkdown("document-1", content, options)
	if len(first) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(first))
	}
	if len(first) != len(second) {
		t.Fatalf("chunk count is not deterministic: %d != %d", len(first), len(second))
	}
	for i, chunk := range first {
		if !utf8.ValidString(chunk.Content) {
			t.Fatalf("chunk %d is not valid UTF-8", i)
		}
		if got := len([]rune(chunk.Content)); got > options.MaxRunes {
			t.Fatalf("chunk %d exceeds max runes: %d", i, got)
		}
		if chunk.ID != second[i].ID {
			t.Fatalf("chunk %d ID changed between runs", i)
		}
		if chunk.StartRune < 0 || chunk.EndRune <= chunk.StartRune {
			t.Fatalf("chunk %d has invalid offsets: %d..%d", i, chunk.StartRune, chunk.EndRune)
		}
		if string([]rune(content)[chunk.StartRune:chunk.EndRune]) != chunk.Content {
			t.Fatalf("chunk %d offsets do not reproduce content", i)
		}
	}
	if first[0].Heading != "概览" {
		t.Fatalf("unexpected first heading: %q", first[0].Heading)
	}
}

func TestChunkMarkdownUsesHeadingPath(t *testing.T) {
	content := "# 产品\n\n## 安装\n\n步骤一。步骤二。\n\n### Windows\n\n运行安装器。"
	chunks := ChunkMarkdown("doc", content, ChunkOptions{MaxRunes: 35, MinRunes: 15, OverlapRunes: 5})
	if len(chunks) < 2 {
		t.Fatalf("expected at least two chunks, got %d", len(chunks))
	}
	foundNested := false
	for _, chunk := range chunks {
		if strings.Contains(chunk.Heading, "产品 > 安装") {
			foundNested = true
		}
	}
	if !foundNested {
		t.Fatalf("expected a nested heading path, chunks=%+v", chunks)
	}
}

func TestChunkMarkdownEmpty(t *testing.T) {
	if chunks := ChunkMarkdown("doc", "", ChunkOptions{}); len(chunks) != 0 {
		t.Fatalf("expected no chunks, got %d", len(chunks))
	}
}
