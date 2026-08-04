package rag

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"regexp"
	"strconv"
	"strings"

	"GopherAI/model"
	"github.com/cloudwego/eino/schema"
)

type GroundednessReport struct {
	Sentences            int      `json:"sentences"`
	FactualSentences     int      `json:"factual_sentences"`
	SupportedSentences   int      `json:"supported_sentences"`
	CitationCoverage     float64  `json:"citation_coverage"`
	InvalidCitations     []int    `json:"invalid_citations,omitempty"`
	UnsupportedSentences []string `json:"unsupported_sentences,omitempty"`
}

type groundednessLogRecord struct {
	Sentences          int      `json:"sentences"`
	FactualSentences   int      `json:"factual_sentences"`
	SupportedSentences int      `json:"supported_sentences"`
	CitationCoverage   float64  `json:"citation_coverage"`
	InvalidCitations   []int    `json:"invalid_citations,omitempty"`
	UnsupportedCount   int      `json:"unsupported_count"`
	UnsupportedHashes  []string `json:"unsupported_hashes,omitempty"`
}

var citationPattern = regexp.MustCompile(`\[(\d+)]`)
var sentencePattern = regexp.MustCompile(`[^。！？!?\n]+[。！？!?]?(?:\s*\[\d+])*`)
var factualNumberPattern = regexp.MustCompile(`\d+(?:[.:/-]\d+)*`)

// ValidateGroundedAnswer checks citation existence and lightweight lexical
// entailment. It is deliberately conservative: it reports failures for
// observability but never invents or silently rewrites an answer.
func ValidateGroundedAnswer(answer string, documents []*schema.Document) GroundednessReport {
	report := GroundednessReport{}
	seenInvalid := make(map[int]struct{})
	for _, raw := range sentencePattern.FindAllString(answer, -1) {
		sentence := strings.TrimSpace(raw)
		if sentence == "" {
			continue
		}
		report.Sentences++
		if !looksFactual(sentence) {
			continue
		}
		report.FactualSentences++
		matches := citationPattern.FindAllStringSubmatch(sentence, -1)
		supported := false
		for _, match := range matches {
			index, _ := strconv.Atoi(match[1])
			if index < 1 || index > len(documents) {
				if _, exists := seenInvalid[index]; !exists {
					report.InvalidCitations = append(report.InvalidCitations, index)
					seenInvalid[index] = struct{}{}
				}
				continue
			}
			plain := citationPattern.ReplaceAllString(sentence, "")
			if referenceSupportsSentence(plain, documents[index-1].Content) {
				supported = true
			}
		}
		if supported {
			report.SupportedSentences++
		} else {
			report.UnsupportedSentences = append(report.UnsupportedSentences, sentence)
		}
	}
	if report.FactualSentences > 0 {
		report.CitationCoverage = float64(report.SupportedSentences) / float64(report.FactualSentences)
	}
	return report
}

// referenceSupportsSentence is intentionally a lightweight guard, not a
// substitute for NLI. It catches the common high-impact error where an answer
// changes a cited number or reverses a cited negation while retaining a shared
// topic word (for example, "port 8080" cited to "port 443").
func referenceSupportsSentence(sentence, source string) bool {
	if termOverlap(textTerms(sentence), textTerms(source)) == 0 {
		return false
	}
	sentenceNumbers := factualNumberPattern.FindAllString(sentence, -1)
	if len(sentenceNumbers) > 0 {
		sourceNumbers := make(map[string]struct{})
		for _, value := range factualNumberPattern.FindAllString(source, -1) {
			sourceNumbers[value] = struct{}{}
		}
		for _, value := range sentenceNumbers {
			if _, exists := sourceNumbers[value]; !exists {
				return false
			}
		}
	}
	return hasNegation(sentence) == hasNegation(source)
}

func hasNegation(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{"不", "无", "未", "禁止", "不能", "not ", " no ", "never"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func LogGroundedness(report GroundednessReport) {
	record := groundednessLogRecord{
		Sentences: report.Sentences, FactualSentences: report.FactualSentences,
		SupportedSentences: report.SupportedSentences, CitationCoverage: report.CitationCoverage,
		InvalidCitations: report.InvalidCitations, UnsupportedCount: len(report.UnsupportedSentences),
	}
	for _, sentence := range report.UnsupportedSentences {
		sum := sha256.Sum256([]byte(sentence))
		record.UnsupportedHashes = append(record.UnsupportedHashes, hex.EncodeToString(sum[:8]))
	}
	if encoded, err := json.Marshal(record); err == nil {
		log.Printf("rag_groundedness=%s", encoded)
	}
}

// IsGroundedAnswerAccepted is the output gate for a response generated with
// retrieved sources. It intentionally accepts non-factual acknowledgements,
// but never lets a factual sentence through when its cited evidence is absent,
// invalid, or contradicted by the lightweight checks above.
func IsGroundedAnswerAccepted(report GroundednessReport) bool {
	return len(report.InvalidCitations) == 0 && len(report.UnsupportedSentences) == 0
}

// ReferencesUsedInAnswer maps only valid prompt citation numbers back to the
// sources provided to the model. Persisting this set prevents an unused
// retrieval candidate from being displayed as an answer citation.
func ReferencesUsedInAnswer(answer string, candidates []model.KnowledgeReference) []model.KnowledgeReference {
	used := make([]model.KnowledgeReference, 0, len(candidates))
	seen := make(map[int]struct{})
	for _, match := range citationPattern.FindAllStringSubmatch(answer, -1) {
		index, err := strconv.Atoi(match[1])
		if err != nil || index < 1 || index > len(candidates) {
			continue
		}
		if _, exists := seen[index]; exists {
			continue
		}
		seen[index] = struct{}{}
		reference := candidates[index-1]
		reference.CitationIndex = index
		used = append(used, reference)
	}
	return used
}

func looksFactual(sentence string) bool {
	plain := strings.TrimSpace(citationPattern.ReplaceAllString(sentence, ""))
	if len([]rune(plain)) < 6 {
		return false
	}
	for _, marker := range []string{"资料不足", "无法确定", "未找到", "不知道", "请提供"} {
		if strings.Contains(plain, marker) {
			return false
		}
	}
	return true
}
