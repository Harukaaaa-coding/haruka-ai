package rag

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	redisPkg "GopherAI/common/redis"
	"GopherAI/config"
	"GopherAI/model"

	"github.com/cloudwego/eino/schema"
	redisCli "github.com/redis/go-redis/v9"
)

type rankedDocument struct {
	document      *schema.Document
	source        string
	vectorRank    int
	lexicalRank   int
	rrfScore      float64
	semanticScore float64
	bm25Score     float64
}

type retrievalTrace struct {
	QueryHash      string                 `json:"query_hash"`
	TopK           int                    `json:"top_k"`
	StartedAt      time.Time              `json:"started_at"`
	DurationMS     int64                  `json:"duration_ms"`
	Stages         []retrievalTraceStage  `json:"stages"`
	FinalChunks    []retrievalTraceResult `json:"final_chunks"`
	Rejected       int                    `json:"rejected"`
	StatusFiltered int                    `json:"status_filtered"`
	Merged         int                    `json:"merged"`
}

type retrievalTraceStage struct {
	Name       string                 `json:"name"`
	Source     string                 `json:"source"`
	Candidates []retrievalTraceResult `json:"candidates,omitempty"`
	Error      string                 `json:"error,omitempty"`
}

type retrievalTraceResult struct {
	ChunkID     string  `json:"chunk_id"`
	DocumentID  string  `json:"document_id,omitempty"`
	Rank        int     `json:"rank"`
	VectorScore float64 `json:"vector_score,omitempty"`
	BM25Score   float64 `json:"bm25_score,omitempty"`
	RRFScore    float64 `json:"rrf_score,omitempty"`
	RerankScore float64 `json:"rerank_score,omitempty"`
	Distance    float64 `json:"distance,omitempty"`
}

func newRetrievalTrace(query string, topK int) *retrievalTrace {
	sum := sha256.Sum256([]byte(strings.TrimSpace(query)))
	return &retrievalTrace{QueryHash: hex.EncodeToString(sum[:8]), TopK: topK, StartedAt: time.Now()}
}

func (t *retrievalTrace) addStage(name, source string, docs []*schema.Document) {
	stage := retrievalTraceStage{Name: name, Source: source, Candidates: make([]retrievalTraceResult, 0, len(docs))}
	for i, doc := range docs {
		ref, _ := ReferenceFromDocument(doc)
		stage.Candidates = append(stage.Candidates, traceResultForDocument(ref, i+1, doc))
	}
	t.Stages = append(t.Stages, stage)
}

func (t *retrievalTrace) addError(name, source string, err error) {
	t.Stages = append(t.Stages, retrievalTraceStage{Name: name, Source: source, Error: err.Error()})
}

func (t *retrievalTrace) finish(docs []*schema.Document) {
	t.DurationMS = time.Since(t.StartedAt).Milliseconds()
	for i, doc := range docs {
		ref, _ := ReferenceFromDocument(doc)
		t.FinalChunks = append(t.FinalChunks, traceResultForDocument(ref, i+1, doc))
	}
}

func traceResultForDocument(ref model.KnowledgeReference, rank int, doc *schema.Document) retrievalTraceResult {
	return retrievalTraceResult{
		ChunkID: ref.ChunkID, DocumentID: ref.DocumentID, Rank: rank,
		VectorScore: metadataNumber(doc, "score"), BM25Score: metadataNumber(doc, "bm25_score"),
		RRFScore: metadataNumber(doc, "rrf_score"), RerankScore: metadataNumber(doc, "rerank_score"),
		Distance: documentDistance(doc),
	}
}

func (t *retrievalTrace) log() {
	if encoded, err := json.Marshal(t); err == nil {
		log.Printf("rag_retrieval_trace=%s", encoded)
	}
}

func retrievalCandidateK(topK int) int {
	factor := config.GetConfig().RagModelConfig.RagCandidateFactor
	if factor <= 0 {
		factor = 4
	}
	return max(topK, topK*factor)
}

func rrfConstant() float64 {
	value := config.GetConfig().RagModelConfig.RagRRFK
	if value <= 0 {
		value = 60
	}
	return float64(value)
}

func minRetrievalScore() float64 {
	value := config.GetConfig().RagModelConfig.RagMinScore
	// Zero explicitly disables vector score filtering. Deployments that want a
	// floor set it in config.toml (the project default is 0.25).
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	return min(value, 1)
}

func retrieveBM25(ctx context.Context, indexName, query string, topK int) ([]*schema.Document, error) {
	terms := bm25Terms(query)
	if len(terms) == 0 || redisPkg.Rdb == nil {
		return nil, nil
	}
	// Use OR for first-stage lexical recall. Requiring every natural-language
	// term (the RediSearch default for a space-separated query) made BM25 empty
	// for otherwise relevant questions.
	searchQuery := "@content:(" + strings.Join(terms, " | ") + ")"
	args := []any{"FT.SEARCH", indexName, searchQuery, "WITHSCORES", "SCORER", "BM25STD", "RETURN", "2", "content", "metadata"}
	if containsHan(query) {
		args = append(args, "LANGUAGE", "chinese")
	}
	args = append(args, "LIMIT", "0", topK, "DIALECT", "2")
	value, err := redisPkg.Rdb.Do(ctx, args...).Result()
	if err != nil {
		return nil, err
	}
	return parseSearchDocuments(value), nil
}

// bm25Terms follows RediSearch TEXT tokenization: whitespace and punctuation
// are separators. Building the query from tokens avoids accidentally turning
// a user-provided '-', '$', or quote into query syntax.
func bm25Terms(query string) []string {
	seen := make(map[string]struct{})
	terms := make([]string, 0)
	var token []rune
	flush := func() {
		if len(token) == 0 {
			return
		}
		term := strings.ToLower(string(token))
		if _, exists := seen[term]; !exists {
			seen[term] = struct{}{}
			terms = append(terms, term)
		}
		token = token[:0]
	}
	for _, char := range []rune(query) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) || char == '_' {
			token = append(token, char)
		} else {
			flush()
		}
	}
	flush()
	return terms
}

func containsHan(text string) bool {
	for _, char := range text {
		if unicode.Is(unicode.Han, char) {
			return true
		}
	}
	return false
}

func retrieveVector(ctx context.Context, indexName string, vector []float64, topK int) ([]*schema.Document, error) {
	if redisPkg.Rdb == nil {
		return nil, fmt.Errorf("redis is not initialized")
	}
	searchQuery := fmt.Sprintf("(*)=>[KNN %d @vector $vector AS distance]", topK)
	result, err := redisPkg.Rdb.FTSearchWithArgs(ctx, indexName, searchQuery, &redisCli.FTSearchOptions{
		Return: []redisCli.FTSearchReturn{{FieldName: "content"}, {FieldName: "metadata"}, {FieldName: "distance"}},
		SortBy: []redisCli.FTSearchSortBy{{FieldName: "distance", Asc: true}}, Limit: topK, DialectVersion: 2,
		Params: map[string]any{"vector": vectorBytes(vector)},
	}).Result()
	if err != nil {
		return nil, err
	}
	docs := make([]*schema.Document, 0, len(result.Docs))
	for _, raw := range result.Docs {
		docs = append(docs, redisDocumentToSchema(raw))
	}
	return docs, nil
}

func vectorBytes(vector []float64) []byte {
	result := make([]byte, len(vector)*4)
	for i, value := range vector {
		binary.LittleEndian.PutUint32(result[i*4:], math.Float32bits(float32(value)))
	}
	return result
}

func parseSearchDocuments(value any) []*schema.Document {
	items, ok := value.([]any)
	if !ok || len(items) < 4 {
		return nil
	}
	result := make([]*schema.Document, 0, (len(items)-1)/3)
	for i := 1; i+2 < len(items); i += 3 {
		doc := &schema.Document{ID: anyString(items[i]), MetaData: map[string]any{}}
		doc.MetaData["bm25_score"], _ = strconv.ParseFloat(anyString(items[i+1]), 64)
		fields, _ := items[i+2].([]any)
		for j := 0; j+1 < len(fields); j += 2 {
			switch anyString(fields[j]) {
			case "content":
				doc.Content = anyString(fields[j+1])
			case "metadata":
				var decoded map[string]any
				if json.Unmarshal([]byte(anyString(fields[j+1])), &decoded) == nil {
					for key, field := range decoded {
						doc.MetaData[key] = field
					}
				}
			}
		}
		if ref, ok := ReferenceFromDocument(doc); ok {
			doc.ID = ref.ChunkID
		}
		result = append(result, doc)
	}
	return result
}

func anyString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return fmt.Sprint(value)
	}
}

func fuseFilterMergeRerank(query string, candidates []rankedDocument, topK int, trace *retrievalTrace) []*schema.Document {
	byChunk := make(map[string]*rankedDocument)
	for _, candidate := range candidates {
		if candidate.document == nil {
			continue
		}
		ref, ok := ReferenceFromDocument(candidate.document)
		if !ok {
			continue
		}
		key := candidate.source + "\x00" + ref.ChunkID
		existing := byChunk[key]
		if existing == nil {
			copy := candidate
			existing = &copy
			byChunk[key] = existing
		}
		if candidate.vectorRank > 0 {
			existing.vectorRank = candidate.vectorRank
			existing.document = candidate.document
			existing.semanticScore = max(existing.semanticScore, metadataNumber(candidate.document, "score"))
		}
		if candidate.lexicalRank > 0 {
			existing.lexicalRank = candidate.lexicalRank
			if existing.document == nil {
				existing.document = candidate.document
			}
			existing.bm25Score = max(existing.bm25Score, metadataNumber(candidate.document, "bm25_score"))
		}
	}

	// A per-KB TopK is only a recall over-fetch. Before RRF, rank candidates
	// globally by their comparable cosine scores so a tiny, weak knowledge base
	// cannot receive the same vector rank-1 credit as the best hit in a large
	// knowledge base. Lexical-only candidates are admitted only when their KB
	// has independently passed the semantic source gate.
	sourceBestSemantic := make(map[string]float64)
	entries := make([]*rankedDocument, 0, len(byChunk))
	for _, candidate := range byChunk {
		if candidate.vectorRank > 0 {
			sourceBestSemantic[candidate.source] = max(sourceBestSemantic[candidate.source], candidate.semanticScore)
		}
		entries = append(entries, candidate)
	}

	minimumScore := minRetrievalScore()
	accepted := make([]*rankedDocument, 0, len(entries))
	for _, candidate := range entries {
		if candidate.vectorRank > 0 && candidate.semanticScore < minimumScore {
			trace.Rejected++
			continue
		}
		if candidate.vectorRank == 0 && minimumScore > 0 && sourceBestSemantic[candidate.source] < minimumScore {
			trace.Rejected++
			continue
		}
		accepted = append(accepted, candidate)
	}
	assignGlobalFusionRanks(accepted, sourceBestSemantic)

	rrfK := rrfConstant()
	fused := make([]rankedDocument, 0, len(accepted))
	for _, candidate := range accepted {
		if candidate.vectorRank > 0 {
			candidate.rrfScore += 1 / (rrfK + float64(candidate.vectorRank))
		}
		if candidate.lexicalRank > 0 {
			candidate.rrfScore += 1 / (rrfK + float64(candidate.lexicalRank))
		}
		if candidate.document.MetaData == nil {
			candidate.document.MetaData = map[string]any{}
		}
		if candidate.vectorRank > 0 {
			candidate.document.MetaData["score"] = candidate.semanticScore
		}
		if candidate.lexicalRank > 0 {
			candidate.document.MetaData["bm25_score"] = candidate.bm25Score
		}
		candidate.document.MetaData["rrf_score"] = candidate.rrfScore
		fused = append(fused, *candidate)
	}
	// RRF ranks from separate per-KB lists can tie frequently. Break ties using
	// the comparable cosine score, then a stable identity; map iteration must
	// never decide which knowledge base occupies the final TopK.
	sort.SliceStable(fused, func(i, j int) bool {
		if math.Abs(fused[i].rrfScore-fused[j].rrfScore) > 1e-12 {
			return fused[i].rrfScore > fused[j].rrfScore
		}
		if math.Abs(fused[i].semanticScore-fused[j].semanticScore) > 1e-12 {
			return fused[i].semanticScore > fused[j].semanticScore
		}
		return stableDocumentIdentity(fused[i].document, fused[i].source) < stableDocumentIdentity(fused[j].document, fused[j].source)
	})
	docs := make([]*schema.Document, 0, len(fused))
	for _, item := range fused {
		docs = append(docs, item.document)
	}
	docs, trace.Merged = mergeAdjacentDocuments(docs)
	if config.GetConfig().RagModelConfig.RagRerankEnabled {
		rerankDocuments(query, docs)
	}
	if len(docs) > topK {
		docs = docs[:topK]
	}
	return docs
}

// assignGlobalFusionRanks replaces per-index ranks once the candidate pool has
// been collected. Cosine similarity is comparable because every KB uses the
// same embedding model and COSINE distance. BM25 scores are corpus-dependent,
// so lexical ranking first follows the source's best semantic evidence and
// then the lexical rank within that source instead of treating every KB's
// local rank 1 as globally equivalent.
func assignGlobalFusionRanks(entries []*rankedDocument, sourceBestSemantic map[string]float64) {
	vectors := make([]*rankedDocument, 0, len(entries))
	lexical := make([]*rankedDocument, 0, len(entries))
	for _, entry := range entries {
		if entry.vectorRank > 0 {
			vectors = append(vectors, entry)
		}
		if entry.lexicalRank > 0 {
			lexical = append(lexical, entry)
		}
	}
	sort.Slice(vectors, func(i, j int) bool {
		if math.Abs(vectors[i].semanticScore-vectors[j].semanticScore) > 1e-12 {
			return vectors[i].semanticScore > vectors[j].semanticScore
		}
		return stableDocumentIdentity(vectors[i].document, vectors[i].source) < stableDocumentIdentity(vectors[j].document, vectors[j].source)
	})
	for index, entry := range vectors {
		entry.vectorRank = index + 1
	}
	sort.Slice(lexical, func(i, j int) bool {
		leftSource, rightSource := sourceBestSemantic[lexical[i].source], sourceBestSemantic[lexical[j].source]
		if math.Abs(leftSource-rightSource) > 1e-12 {
			return leftSource > rightSource
		}
		if lexical[i].lexicalRank != lexical[j].lexicalRank {
			return lexical[i].lexicalRank < lexical[j].lexicalRank
		}
		return stableDocumentIdentity(lexical[i].document, lexical[i].source) < stableDocumentIdentity(lexical[j].document, lexical[j].source)
	})
	for index, entry := range lexical {
		entry.lexicalRank = index + 1
	}
}

func mergeAdjacentDocuments(docs []*schema.Document) ([]*schema.Document, int) {
	// Preserve fusion order while safely grouping only chunks with a stable
	// document identity. Legacy records that lack document_id are not merged:
	// otherwise two unrelated files with chunk 0 can leak into one context.
	result := make([]*schema.Document, 0, len(docs))
	merged := 0
	for _, doc := range docs {
		ref, ok := ReferenceFromDocument(doc)
		if !ok {
			result = append(result, doc)
			continue
		}
		found := -1
		for i, current := range result {
			currentRef, currentOK := ReferenceFromDocument(current)
			if currentOK && sameMergeDocument(currentRef, ref) && rangesTouch(currentRef, ref) {
				found = i
				break
			}
		}
		if found < 0 {
			result = append(result, doc)
			continue
		}
		mergeDocumentContent(result[found], doc)
		merged++
	}
	return result, merged
}

func sameMergeDocument(a, b model.KnowledgeReference) bool {
	return a.DocumentID != "" && a.DocumentID == b.DocumentID && a.KnowledgeBaseID == b.KnowledgeBaseID
}

func rangesTouch(a, b model.KnowledgeReference) bool {
	return a.EndRune >= b.StartRune && b.EndRune >= a.StartRune
}

func mergeDocumentContent(target, addition *schema.Document) {
	a, okA := ReferenceFromDocument(target)
	b, okB := ReferenceFromDocument(addition)
	if !okA || !okB {
		return
	}
	if b.StartRune < a.StartRune {
		target.Content, addition.Content = addition.Content, target.Content
		a, b = b, a
	}
	overlap := max(0, a.EndRune-b.StartRune)
	additionRunes := []rune(addition.Content)
	if overlap < len(additionRunes) {
		target.Content += string(additionRunes[overlap:])
	}
	a.EndRune = max(a.EndRune, b.EndRune)
	a.StartRune = min(a.StartRune, b.StartRune)
	a.ChunkIndex = min(a.ChunkIndex, b.ChunkIndex)
	a.MergedChunkIDs = mergedChunkIDs(a, b)
	target.MetaData["reference"] = a
}

func mergedChunkIDs(first, second model.KnowledgeReference) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, len(first.MergedChunkIDs)+len(second.MergedChunkIDs)+1)
	appendID := func(id string) {
		if id == "" || id == first.ChunkID {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	for _, id := range first.MergedChunkIDs {
		appendID(id)
	}
	appendID(second.ChunkID)
	for _, id := range second.MergedChunkIDs {
		appendID(id)
	}
	return result
}

func rerankDocuments(query string, docs []*schema.Document) {
	queryTerms := textTerms(query)
	for _, doc := range docs {
		base := metadataNumber(doc, "rrf_score")
		heading := ""
		if reference, ok := ReferenceFromDocument(doc); ok {
			heading = reference.Heading
		}
		overlap := termOverlap(queryTerms, textTerms(doc.Content+" "+heading))
		doc.MetaData["rerank_score"] = base + 0.01*overlap
	}
	sort.SliceStable(docs, func(i, j int) bool {
		return metadataNumber(docs[i], "rerank_score") > metadataNumber(docs[j], "rerank_score")
	})
}

func stableDocumentIdentity(doc *schema.Document, source string) string {
	if ref, ok := ReferenceFromDocument(doc); ok {
		return source + "\x00" + ref.DocumentID + "\x00" + ref.ChunkID
	}
	if doc == nil {
		return source
	}
	return source + "\x00" + doc.ID
}

func textTerms(text string) map[string]struct{} {
	result := make(map[string]struct{})
	var word []rune
	flush := func() {
		if len(word) > 1 {
			result[strings.ToLower(string(word))] = struct{}{}
		}
		word = word[:0]
	}
	var han []rune
	for _, r := range []rune(text) {
		if unicode.Is(unicode.Han, r) {
			flush()
			han = append(han, r)
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			word = append(word, r)
		} else {
			flush()
		}
	}
	flush()
	for i := 0; i+1 < len(han); i++ {
		result[string(han[i:i+2])] = struct{}{}
	}
	return result
}

func termOverlap(a, b map[string]struct{}) float64 {
	if len(a) == 0 {
		return 0
	}
	matched := 0
	for term := range a {
		if _, ok := b[term]; ok {
			matched++
		}
	}
	return float64(matched) / math.Sqrt(float64(len(a)*max(1, len(b))))
}

func metadataNumber(doc *schema.Document, key string) float64 {
	if doc == nil || doc.MetaData == nil {
		return 0
	}
	value, _ := metadataFloat(doc.MetaData[key])
	return value
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
