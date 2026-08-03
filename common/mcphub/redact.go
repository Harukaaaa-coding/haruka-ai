package mcphub

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
)

const (
	maxAuditPreviewBytes = 4096
	maxAuditStringRunes  = 256
	maxAuditErrorRunes   = 512
)

var (
	sensitiveNamePattern = regexp.MustCompile(`(?i)(api[-_]?key|secret|token|password|passwd|authorization|cookie|credential|private[-_]?key|access[-_]?key)`)
	bearerPattern        = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/-]{8,}`)
	assignmentPattern    = regexp.MustCompile(`(?i)(api[-_]?key|secret|token|password|authorization)\s*[:=]\s*[^\s,;]+`)
)

func sensitiveKey(key string) bool {
	return sensitiveNamePattern.MatchString(strings.TrimSpace(key))
}

func argumentsDigest(arguments map[string]any) (string, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	canonical, err := json.Marshal(arguments)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func redactedArguments(arguments map[string]any) json.RawMessage {
	redacted := redactValue(arguments, "", 0)
	encoded, err := json.Marshal(redacted)
	if err != nil {
		return json.RawMessage(`{"redacted":true}`)
	}
	if len(encoded) > maxAuditPreviewBytes {
		return json.RawMessage(`{"truncated":true}`)
	}
	return encoded
}

func redactValue(value any, key string, depth int) any {
	if sensitiveKey(key) {
		return "[REDACTED]"
	}
	if depth > 12 {
		return "[MAX_DEPTH]"
	}
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			result[childKey] = redactValue(childValue, childKey, depth+1)
		}
		return result
	case []any:
		limit := len(typed)
		if limit > 50 {
			limit = 50
		}
		result := make([]any, 0, limit+1)
		for index := 0; index < limit; index++ {
			result = append(result, redactValue(typed[index], key, depth+1))
		}
		if len(typed) > limit {
			result = append(result, "[TRUNCATED]")
		}
		return result
	case string:
		return sanitizeText(typed, maxAuditStringRunes)
	default:
		return typed
	}
}

func sanitizeText(value string, maxRunes int) string {
	value = bearerPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	value = assignmentPattern.ReplaceAllStringFunc(value, func(match string) string {
		separator := strings.IndexAny(match, ":=")
		if separator < 0 {
			return "[REDACTED]"
		}
		return strings.TrimSpace(match[:separator]) + "=[REDACTED]"
	})
	value = redactSensitiveURL(value)
	value = strings.Map(func(character rune) rune {
		if character == '\n' || character == '\r' || character == '\t' {
			return ' '
		}
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxRunes]) + "…"
}

func redactSensitiveURL(value string) string {
	fields := strings.Fields(value)
	for index, field := range fields {
		candidate := strings.Trim(field, "()[]{}<>,;\"'")
		parsed, err := url.Parse(candidate)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			continue
		}
		if parsed.User != nil {
			parsed.User = url.User("[REDACTED]")
		}
		query := parsed.Query()
		changed := false
		for name := range query {
			if sensitiveKey(name) {
				query.Set(name, "[REDACTED]")
				changed = true
			}
		}
		if changed {
			parsed.RawQuery = query.Encode()
		}
		fields[index] = strings.Replace(field, candidate, parsed.String(), 1)
	}
	return strings.Join(fields, " ")
}

func summarizeResult(result *mcp.CallToolResult) string {
	if result == nil {
		return "no result"
	}
	contentTypes := make(map[string]int)
	approximateBytes := 0
	for _, content := range result.Content {
		switch typed := content.(type) {
		case mcp.TextContent:
			contentTypes["text"]++
			approximateBytes += len(typed.Text)
		case mcp.ImageContent:
			contentTypes["image"]++
			approximateBytes += len(typed.Data)
		case mcp.AudioContent:
			contentTypes["audio"]++
			approximateBytes += len(typed.Data)
		default:
			contentTypes[fmt.Sprintf("%T", content)]++
		}
	}
	summary := map[string]any{
		"is_error":          result.IsError,
		"content_types":     contentTypes,
		"approximate_bytes": approximateBytes,
		"structured":        result.StructuredContent != nil,
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		return "result summary unavailable"
	}
	return string(encoded)
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	var buffer bytes.Buffer
	buffer.WriteString(PublicErrorMessage(err))
	return sanitizeText(buffer.String(), maxAuditErrorRunes)
}
