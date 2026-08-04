# RAG retrieval evaluation

Copy `docs/rag-eval.example.jsonl` to a private dataset such as
`docs/rag-eval.jsonl`. Each line contains a real user query, the knowledge
bases to search, and every chunk ID that directly supports the expected answer.
For deliberately unanswerable queries, use `"expected_refusal": true` instead
of `relevant_chunk_ids`. Do not commit private document text or user queries.

Run the evaluator against an initialized MySQL and Redis environment:

```powershell
go run ./cmd/rageval -dataset docs/rag-eval.jsonl -user USERNAME -top-k 5
```

The command reports Recall@K, MRR and NDCG@K for answerable cases, plus refusal
precision/recall and false-answer rate for negative cases. Use the same frozen
dataset when calibrating `minScore`, `candidateFactor`, `rrfK` and
`rerankEnabled`; compare quality together with retrieval latency and the
`rag_retrieval_trace` logs.

Runtime trace logs contain a hash of the query rather than its plaintext. They
include vector and BM25 candidates, rejected and merged counts, final chunks,
and duration. `rag_groundedness` logs sentence-level citation coverage and
invalid or unsupported citations.

## Redis index upgrade note

New knowledge-base indexes store a per-document `language` field so Redis can
tokenize Chinese documents with its Chinese analyzer during BM25 retrieval.
An index created before this change keeps its existing schema; re-upload or
explicitly reindex its source documents before using BM25 quality metrics as a
baseline for that knowledge base.
