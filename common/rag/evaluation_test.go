package rag

import "testing"

func TestScoreRanking(t *testing.T) {
	metrics := ScoreRanking([]string{"a", "c"}, []string{"x", "a", "b", "c"}, 3)
	if metrics.RecallAtK != 0.5 || metrics.MRR != 0.5 || metrics.NDCGAtK <= 0 {
		t.Fatalf("unexpected metrics: %+v", metrics)
	}
}

func TestScoreRankingIgnoresDuplicateRetrievedChunks(t *testing.T) {
	metrics := ScoreRanking([]string{"a", "b"}, []string{"a", "a", "b"}, 2)
	if metrics.RecallAtK != 1 || metrics.MRR != 1 || metrics.NDCGAtK != 1 {
		t.Fatalf("duplicates distorted metrics: %+v", metrics)
	}
}

func TestRefusalMetrics(t *testing.T) {
	metrics := EvaluationMetrics{}
	metrics.ObserveRefusal(true, true)
	metrics.ObserveRefusal(true, false)
	metrics.ObserveRefusal(false, true)
	metrics.FinalizeRefusalMetrics()
	if metrics.RefusalPrecision != 0.5 || metrics.RefusalRecall != 0.5 || metrics.FalseAnswerRate != 0.5 {
		t.Fatalf("unexpected refusal metrics: %+v", metrics)
	}
}
