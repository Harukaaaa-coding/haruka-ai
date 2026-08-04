package rag

import "strings"

// EvaluationCase is one labeled retrieval query. RelevantChunkIDs may contain
// more than one answer-supporting chunk for multi-hop questions.
type EvaluationCase struct {
	Query            string   `json:"query"`
	KnowledgeBaseIDs []string `json:"knowledge_base_ids"`
	RelevantChunkIDs []string `json:"relevant_chunk_ids"`
	// ExpectedRefusal marks a deliberately unanswerable query. It lets the
	// offline set calibrate retrieval rejection instead of treating a correct
	// empty result as a retrieval failure.
	ExpectedRefusal bool `json:"expected_refusal,omitempty"`
}

type EvaluationMetrics struct {
	Cases                 int     `json:"cases"`
	RecallAtK             float64 `json:"recall_at_k"`
	MRR                   float64 `json:"mrr"`
	NDCGAtK               float64 `json:"ndcg_at_k"`
	RefusalCases          int     `json:"refusal_cases"`
	RefusalTruePositives  int     `json:"refusal_true_positives"`
	RefusalFalsePositives int     `json:"refusal_false_positives"`
	RefusalFalseNegatives int     `json:"refusal_false_negatives"`
	RefusalPrecision      float64 `json:"refusal_precision"`
	RefusalRecall         float64 `json:"refusal_recall"`
	FalseAnswerRate       float64 `json:"false_answer_rate"`
}

// ObserveRefusal records whether retrieval correctly withheld context for a
// labeled negative query. expectedRefusal=false samples also count false
// refusals, which keeps a high threshold from looking artificially good.
func (m *EvaluationMetrics) ObserveRefusal(expectedRefusal, actualRefusal bool) {
	if expectedRefusal {
		m.RefusalCases++
		if actualRefusal {
			m.RefusalTruePositives++
		} else {
			m.RefusalFalseNegatives++
		}
		return
	}
	if actualRefusal {
		m.RefusalFalsePositives++
	}
}

func (m *EvaluationMetrics) FinalizeRefusalMetrics() {
	if denominator := m.RefusalTruePositives + m.RefusalFalsePositives; denominator > 0 {
		m.RefusalPrecision = float64(m.RefusalTruePositives) / float64(denominator)
	}
	if m.RefusalCases > 0 {
		m.RefusalRecall = float64(m.RefusalTruePositives) / float64(m.RefusalCases)
		m.FalseAnswerRate = float64(m.RefusalFalseNegatives) / float64(m.RefusalCases)
	}
}

// ScoreRanking computes the retrieval metrics used by the offline evaluator.
// rankedChunkIDs must be ordered from most to least relevant.
func ScoreRanking(relevantChunkIDs, rankedChunkIDs []string, k int) EvaluationMetrics {
	relevant := make(map[string]struct{}, len(relevantChunkIDs))
	for _, id := range relevantChunkIDs {
		if id = strings.TrimSpace(id); id != "" {
			relevant[id] = struct{}{}
		}
	}
	if len(relevant) == 0 {
		return EvaluationMetrics{Cases: 1}
	}
	// A merged or faulty retriever can surface the same chunk more than once.
	// Metrics must evaluate a ranked *set*: duplicate IDs neither add recall nor
	// consume an extra rank position.
	ranked := make([]string, 0, len(rankedChunkIDs))
	seen := make(map[string]struct{}, len(rankedChunkIDs))
	for _, id := range rankedChunkIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ranked = append(ranked, id)
	}
	if k <= 0 || k > len(ranked) {
		k = len(ranked)
	}
	hits, reciprocal, dcg := 0, 0.0, 0.0
	for rank, id := range ranked[:k] {
		if _, ok := relevant[id]; !ok {
			continue
		}
		hits++
		if reciprocal == 0 {
			reciprocal = 1 / float64(rank+1)
		}
		dcg += 1 / log2(float64(rank+2))
	}
	idealHits := min(k, len(relevant))
	idcg := 0.0
	for rank := 0; rank < idealHits; rank++ {
		idcg += 1 / log2(float64(rank+2))
	}
	ndcg := 0.0
	if idcg > 0 {
		ndcg = dcg / idcg
	}
	return EvaluationMetrics{Cases: 1, RecallAtK: float64(hits) / float64(len(relevant)), MRR: reciprocal, NDCGAtK: ndcg}
}

func log2(value float64) float64 {
	// change of base avoids exporting an implementation detail to callers.
	return mathLog(value) / mathLog(2)
}
