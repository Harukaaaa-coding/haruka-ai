package rag

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode"
)

const (
	DefaultChunkMaxRunes = 1000
	DefaultChunkMinRunes = 400
	DefaultChunkOverlap  = 120
)

type ChunkOptions struct {
	MaxRunes     int
	MinRunes     int
	OverlapRunes int
}

type TextChunk struct {
	ID        string
	Content   string
	Heading   string
	Index     int
	StartRune int
	EndRune   int
}

type markdownHeading struct {
	offset int
	level  int
	title  string
}

// ChunkMarkdown splits by rune instead of byte, prefers Markdown paragraph
// boundaries, and keeps a small overlap between neighboring chunks. This
// avoids corrupting Chinese/emoji while retaining enough context at a split.
func ChunkMarkdown(documentID, content string, options ChunkOptions) []TextChunk {
	options = normalizedChunkOptions(options)
	runes := []rune(content)
	if len(runes) == 0 {
		return []TextChunk{}
	}
	headings := parseMarkdownHeadings(runes)

	chunks := make([]TextChunk, 0, len(runes)/options.MaxRunes+1)
	start := 0
	for start < len(runes) {
		rawEnd := chooseChunkEnd(runes, start, options)
		chunkStart, chunkEnd := trimRuneRange(runes, start, rawEnd)
		if chunkStart < chunkEnd {
			text := string(runes[chunkStart:chunkEnd])
			chunks = append(chunks, TextChunk{
				ID:        StableChunkID(documentID, chunkStart, chunkEnd, text),
				Content:   text,
				Heading:   headingPathAt(headings, chunkStart),
				Index:     len(chunks),
				StartRune: chunkStart,
				EndRune:   chunkEnd,
			})
		}
		if rawEnd >= len(runes) {
			break
		}
		next := rawEnd - options.OverlapRunes
		if next <= start {
			next = rawEnd
		}
		// Starting after whitespace keeps offsets exact but avoids every chunk
		// beginning with a blank line inherited from the overlap.
		for next < rawEnd && next < len(runes) && unicode.IsSpace(runes[next]) {
			next++
		}
		start = next
	}
	return chunks
}

func normalizedChunkOptions(options ChunkOptions) ChunkOptions {
	if options.MaxRunes <= 0 {
		options.MaxRunes = DefaultChunkMaxRunes
	}
	if options.MinRunes <= 0 || options.MinRunes >= options.MaxRunes {
		options.MinRunes = min(DefaultChunkMinRunes, options.MaxRunes/2)
	}
	if options.OverlapRunes < 0 {
		options.OverlapRunes = 0
	}
	if options.OverlapRunes == 0 {
		options.OverlapRunes = min(DefaultChunkOverlap, options.MaxRunes/5)
	}
	if options.OverlapRunes >= options.MaxRunes {
		options.OverlapRunes = options.MaxRunes / 5
	}
	return options
}

func chooseChunkEnd(runes []rune, start int, options ChunkOptions) int {
	maxEnd := min(len(runes), start+options.MaxRunes)
	if maxEnd == len(runes) {
		return maxEnd
	}
	minEnd := min(maxEnd, start+options.MinRunes)

	// Search boundary classes separately so a nearby space does not win over
	// a slightly earlier paragraph or Markdown section boundary.
	for end := maxEnd; end >= minEnd; end-- {
		if end >= 2 && runes[end-1] == '\n' && runes[end-2] == '\n' {
			return end
		}
	}
	for end := maxEnd; end >= minEnd; end-- {
		if end >= 1 && runes[end-1] == '\n' {
			return end
		}
	}
	for end := maxEnd; end >= minEnd; end-- {
		if end >= 1 && isSentenceBoundary(runes[end-1]) {
			return end
		}
	}
	for end := maxEnd; end >= minEnd; end-- {
		if end >= 1 && unicode.IsSpace(runes[end-1]) {
			return end
		}
	}
	return maxEnd
}

func isSentenceBoundary(r rune) bool {
	switch r {
	case '.', '!', '?', ';', '。', '！', '？', '；', '…':
		return true
	default:
		return false
	}
}

func trimRuneRange(runes []rune, start, end int) (int, int) {
	for start < end && unicode.IsSpace(runes[start]) {
		start++
	}
	for end > start && unicode.IsSpace(runes[end-1]) {
		end--
	}
	return start, end
}

func parseMarkdownHeadings(runes []rune) []markdownHeading {
	result := make([]markdownHeading, 0)
	lineStart := 0
	for lineStart < len(runes) {
		lineEnd := lineStart
		for lineEnd < len(runes) && runes[lineEnd] != '\n' {
			lineEnd++
		}
		line := runes[lineStart:lineEnd]
		level := 0
		for level < len(line) && level < 6 && line[level] == '#' {
			level++
		}
		if level > 0 && level < len(line) && unicode.IsSpace(line[level]) {
			title := strings.TrimSpace(string(line[level:]))
			title = strings.TrimSpace(strings.TrimRight(title, "#"))
			if title != "" {
				result = append(result, markdownHeading{offset: lineStart, level: level, title: title})
			}
		}
		lineStart = lineEnd + 1
	}
	return result
}

func headingPathAt(headings []markdownHeading, offset int) string {
	levels := make([]string, 6)
	for _, heading := range headings {
		if heading.offset > offset {
			break
		}
		levels[heading.level-1] = heading.title
		for i := heading.level; i < len(levels); i++ {
			levels[i] = ""
		}
	}
	path := make([]string, 0, len(levels))
	for _, title := range levels {
		if title != "" {
			path = append(path, title)
		}
	}
	return strings.Join(path, " > ")
}

// StableChunkID is deterministic for a document revision and exact rune range.
func StableChunkID(documentID string, startRune, endRune int, content string) string {
	hash := sha256.New()
	hash.Write([]byte(documentID))
	hash.Write([]byte{0})
	hash.Write([]byte(strconv.Itoa(startRune)))
	hash.Write([]byte{':'})
	hash.Write([]byte(strconv.Itoa(endRune)))
	hash.Write([]byte{0})
	hash.Write([]byte(content))
	return hex.EncodeToString(hash.Sum(nil))
}
