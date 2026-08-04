package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"GopherAI/common/mysql"
	"GopherAI/common/rag"
	redisPkg "GopherAI/common/redis"
	"GopherAI/config"
	"GopherAI/service/knowledgebase"
)

func main() {
	dataset := flag.String("dataset", "docs/rag-eval.jsonl", "JSONL evaluation dataset")
	user := flag.String("user", "", "knowledge-base owner")
	topK := flag.Int("top-k", 5, "retrieval cutoff")
	flag.Parse()
	if *user == "" {
		fail("-user is required")
	}
	if err := config.InitConfig(); err != nil {
		fail(err.Error())
	}
	if err := mysql.InitMysql(); err != nil {
		fail(err.Error())
	}
	if err := redisPkg.Init(context.Background()); err != nil {
		fail(err.Error())
	}
	file, err := os.Open(*dataset)
	if err != nil {
		fail(err.Error())
	}
	defer file.Close()

	total := rag.EvaluationMetrics{}
	scanner := bufio.NewScanner(file)
	// A labeled case can contain a long natural-language query or many expected
	// chunks. The Scanner default (64 KiB) is too small for a practical JSONL
	// evaluation set.
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var testCase rag.EvaluationCase
		if err := json.Unmarshal(line, &testCase); err != nil {
			fail(err.Error())
		}
		if !testCase.ExpectedRefusal && len(testCase.RelevantChunkIDs) == 0 {
			fail("evaluation case must include relevant_chunk_ids or expected_refusal=true")
		}
		result, err := knowledgebase.Retrieve(context.Background(), *user, testCase.KnowledgeBaseIDs, testCase.Query, *topK)
		if err != nil {
			fail(err.Error())
		}
		ids := make([]string, 0, len(result.References))
		for _, ref := range result.References {
			ids = append(ids, ref.ChunkID)
			ids = append(ids, ref.MergedChunkIDs...)
		}
		actualRefusal := len(result.Documents) == 0
		total.ObserveRefusal(testCase.ExpectedRefusal, actualRefusal)
		if testCase.ExpectedRefusal {
			continue
		}
		score := rag.ScoreRanking(testCase.RelevantChunkIDs, ids, *topK)
		total.Cases++
		total.RecallAtK += score.RecallAtK
		total.MRR += score.MRR
		total.NDCGAtK += score.NDCGAtK
	}
	if err := scanner.Err(); err != nil {
		fail(err.Error())
	}
	if total.Cases > 0 {
		total.RecallAtK /= float64(total.Cases)
		total.MRR /= float64(total.Cases)
		total.NDCGAtK /= float64(total.Cases)
	}
	total.FinalizeRefusalMetrics()
	encoded, _ := json.MarshalIndent(total, "", "  ")
	fmt.Println(string(encoded))
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
