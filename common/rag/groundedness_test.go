package rag

import (
	"GopherAI/model"
	"github.com/cloudwego/eino/schema"
	"testing"
)

func TestValidateGroundedAnswer(t *testing.T) {
	docs := []*schema.Document{{Content: "安装程序需要管理员权限。"}}
	report := ValidateGroundedAnswer("安装程序需要管理员权限。[1] 端口固定为8080。[2]", docs)
	if report.FactualSentences != 2 || report.SupportedSentences != 1 || len(report.InvalidCitations) != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestReferencesUsedInAnswerOnlyReturnsValidCitations(t *testing.T) {
	candidates := []model.KnowledgeReference{{ChunkID: "one"}, {ChunkID: "two"}}
	used := ReferencesUsedInAnswer("first [2], repeat [2], invalid [9], then [1]", candidates)
	if len(used) != 2 || used[0].ChunkID != "two" || used[0].CitationIndex != 2 || used[1].ChunkID != "one" || used[1].CitationIndex != 1 {
		t.Fatalf("used citations=%+v", used)
	}
}

func TestValidateGroundedAnswerRejectsContradictoryNumber(t *testing.T) {
	docs := []*schema.Document{{Content: "服务端口固定为 443。"}}
	report := ValidateGroundedAnswer("服务端口固定为 8080。[1]", docs)
	if report.SupportedSentences != 0 || report.CitationCoverage != 0 {
		t.Fatalf("numeric contradiction was accepted: %+v", report)
	}
}

func TestIsGroundedAnswerAcceptedRejectsUnsupportedFacts(t *testing.T) {
	report := GroundednessReport{FactualSentences: 1, UnsupportedSentences: []string{"unsupported [1]"}}
	if IsGroundedAnswerAccepted(report) {
		t.Fatal("unsupported factual answer was accepted")
	}
	if !IsGroundedAnswerAccepted(GroundednessReport{}) {
		t.Fatal("non-factual answer should remain allowed")
	}
}
