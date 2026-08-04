package rag

import (
	"testing"

	"GopherAI/config"
	"GopherAI/model"
	"github.com/cloudwego/eino/schema"
)

func TestMergeAdjacentDocumentsRemovesRuneOverlap(t *testing.T) {
	first := testReferenceDocument("a", 0, 0, 10, "0123456789")
	second := testReferenceDocument("b", 1, 8, 15, "89abcde")
	merged, count := mergeAdjacentDocuments([]*schema.Document{first, second})
	if count != 1 || len(merged) != 1 || merged[0].Content != "0123456789abcde" {
		t.Fatalf("unexpected merge: count=%d docs=%#v", count, merged)
	}
	ref, _ := ReferenceFromDocument(merged[0])
	if ref.StartRune != 0 || ref.EndRune != 15 {
		t.Fatalf("unexpected merged reference: %+v", ref)
	}
	if len(ref.MergedChunkIDs) != 1 || ref.MergedChunkIDs[0] != "b" {
		t.Fatalf("merged provenance lost: %+v", ref)
	}
}

func TestMergeAdjacentDocumentsMergesEntireOverlappingRun(t *testing.T) {
	one := testReferenceDocument("one", 0, 0, 10, "0123456789")
	two := testReferenceDocument("two", 1, 8, 18, "89abcdefgh")
	three := testReferenceDocument("three", 2, 16, 24, "ghijklmn")
	merged, count := mergeAdjacentDocuments([]*schema.Document{one, two, three})
	if count != 2 || len(merged) != 1 || merged[0].Content != "0123456789abcdefghijklmn" {
		t.Fatalf("unexpected complete run merge: count=%d docs=%#v", count, merged)
	}
	ref, _ := ReferenceFromDocument(merged[0])
	if len(ref.MergedChunkIDs) != 2 || ref.MergedChunkIDs[0] != "two" || ref.MergedChunkIDs[1] != "three" {
		t.Fatalf("complete merge provenance lost: %+v", ref)
	}
}

func TestMergeAdjacentDocumentsDoesNotMergeLegacySources(t *testing.T) {
	first := testReferenceDocument("a", 0, 0, 10, "first")
	second := testReferenceDocument("b", 1, 8, 15, "second")
	first.MetaData["reference"] = model.KnowledgeReference{ChunkID: "a", ChunkIndex: 0, StartRune: 0, EndRune: 10, DocumentName: "one.txt"}
	second.MetaData["reference"] = model.KnowledgeReference{ChunkID: "b", ChunkIndex: 1, StartRune: 8, EndRune: 15, DocumentName: "two.txt"}
	merged, count := mergeAdjacentDocuments([]*schema.Document{first, second})
	if count != 0 || len(merged) != 2 {
		t.Fatalf("legacy chunks were merged: %#v", merged)
	}
}

func TestBM25TermsFollowTextTokenBoundaries(t *testing.T) {
	terms := bm25Terms("ERR-42, C++ costs $100")
	want := []string{"err", "42", "c", "costs", "100"}
	if len(terms) != len(want) {
		t.Fatalf("terms=%q, want=%q", terms, want)
	}
	for i := range want {
		if terms[i] != want[i] {
			t.Fatalf("terms=%q, want=%q", terms, want)
		}
	}
}

func TestFusionBreaksRRFScoreTiesWithVectorScore(t *testing.T) {
	low := testReferenceDocument("low", 0, 0, 5, "low")
	high := testReferenceDocument("high", 0, 0, 5, "high")
	low.MetaData["reference"] = model.KnowledgeReference{ChunkID: "low", DocumentID: "doc-low", ChunkIndex: 0, StartRune: 0, EndRune: 5}
	high.MetaData["reference"] = model.KnowledgeReference{ChunkID: "high", DocumentID: "doc-high", ChunkIndex: 0, StartRune: 0, EndRune: 5}
	low.MetaData["score"] = 0.51
	high.MetaData["score"] = 0.91
	trace := newRetrievalTrace("query", 2)
	result := fuseFilterMergeRerank("query", []rankedDocument{
		{document: low, source: "kb-a", vectorRank: 1, lexicalRank: 1},
		{document: high, source: "kb-b", vectorRank: 1, lexicalRank: 1},
	}, 2, trace)
	if len(result) != 2 || result[0].ID != "high" {
		t.Fatalf("unexpected fused ranking: %#v", result)
	}
}

func TestFusionUsesGlobalRanksAcrossKnowledgeBases(t *testing.T) {
	weak := testReferenceDocument("weak", 0, 0, 5, "same terms")
	strong := testReferenceDocument("strong", 1, 5, 10, "same terms")
	weak.MetaData["reference"] = model.KnowledgeReference{ChunkID: "weak", DocumentID: "doc-weak", KnowledgeBaseID: "small", ChunkIndex: 0, StartRune: 0, EndRune: 5}
	strong.MetaData["reference"] = model.KnowledgeReference{ChunkID: "strong", DocumentID: "doc-strong", KnowledgeBaseID: "large", ChunkIndex: 1, StartRune: 5, EndRune: 10}
	weak.MetaData["score"] = 0.26
	strong.MetaData["score"] = 0.91

	result := fuseFilterMergeRerank("same terms", []rankedDocument{
		// These are local ranks returned by separate KB indexes. The small KB's
		// local rank 1 must not outrank the globally stronger result.
		{document: weak, source: "small", vectorRank: 1, lexicalRank: 1},
		{document: strong, source: "large", vectorRank: 2, lexicalRank: 2},
	}, 2, newRetrievalTrace("query", 2))
	if len(result) != 2 || result[0].ID != "strong" {
		t.Fatalf("local KB rank inflated a weak result: %#v", result)
	}
}

func TestFusionRejectsLowVectorScoreEvenWithLexicalHit(t *testing.T) {
	conf := config.GetConfig()
	old := conf.RagModelConfig.RagMinScore
	conf.RagModelConfig.RagMinScore = 0.25
	t.Cleanup(func() { conf.RagModelConfig.RagMinScore = old })

	low := testReferenceDocument("low", 0, 0, 5, "matching keyword")
	low.MetaData["reference"] = model.KnowledgeReference{ChunkID: "low", DocumentID: "doc-low", KnowledgeBaseID: "kb", ChunkIndex: 0, StartRune: 0, EndRune: 5}
	low.MetaData["score"] = 0.1
	trace := newRetrievalTrace("query", 1)
	result := fuseFilterMergeRerank("matching keyword", []rankedDocument{{document: low, source: "kb", vectorRank: 1, lexicalRank: 1}}, 1, trace)
	if len(result) != 0 || trace.Rejected != 1 {
		t.Fatalf("low hybrid candidate bypassed min score: result=%#v trace=%+v", result, trace)
	}
}

func TestFusionCarriesBM25ScoreIntoFinalTrace(t *testing.T) {
	vector := testReferenceDocument("chunk", 0, 0, 5, "body")
	lexical := testReferenceDocument("chunk", 0, 0, 5, "body")
	vector.MetaData["reference"] = model.KnowledgeReference{ChunkID: "chunk", DocumentID: "doc", KnowledgeBaseID: "kb", ChunkIndex: 0, StartRune: 0, EndRune: 5}
	lexical.MetaData["reference"] = vector.MetaData["reference"]
	vector.MetaData["score"] = 0.9
	lexical.MetaData["bm25_score"] = 3.5
	trace := newRetrievalTrace("query", 1)
	result := fuseFilterMergeRerank("body", []rankedDocument{
		{document: vector, source: "kb", vectorRank: 1},
		{document: lexical, source: "kb", lexicalRank: 1},
	}, 1, trace)
	trace.finish(result)
	if len(trace.FinalChunks) != 1 || trace.FinalChunks[0].BM25Score != 3.5 {
		t.Fatalf("final trace lost BM25 score: %+v", trace.FinalChunks)
	}
}

func TestVectorBytesUsesFloat32LittleEndian(t *testing.T) {
	got := vectorBytes([]float64{1})
	want := []byte{0, 0, 128, 63}
	if string(got) != string(want) {
		t.Fatalf("bytes=%v, want=%v", got, want)
	}
}

func TestParseSearchDocumentsPreservesReference(t *testing.T) {
	metadata := `{"reference":{"chunk_id":"chunk","document_id":"doc","chunk_index":2}}`
	value := []any{int64(1), "redis-key", "3.5", []any{"content", "body", "metadata", metadata}}
	docs := parseSearchDocuments(value)
	if len(docs) != 1 || docs[0].ID != "chunk" || docs[0].Content != "body" || metadataNumber(docs[0], "bm25_score") != 3.5 {
		t.Fatalf("unexpected parsed documents: %#v", docs)
	}
}

func testReferenceDocument(chunkID string, chunkIndex, start, end int, content string) *schema.Document {
	return &schema.Document{ID: chunkID, Content: content, MetaData: map[string]any{"reference": model.KnowledgeReference{
		ChunkID: chunkID, DocumentID: "doc", ChunkIndex: chunkIndex, StartRune: start, EndRune: end,
	}}}
}
