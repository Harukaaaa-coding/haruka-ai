package session

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const maxChatJSONBodyBytes int64 = 1 << 20

var (
	errChatJSONBodyTooLarge = errors.New("chat JSON body is too large")
	errChatJSONUnicode      = errors.New("chat JSON contains invalid Unicode")
)

// bindStrictJSON validates the original request bytes before encoding/json can
// replace malformed UTF-8 or an unpaired UTF-16 surrogate with U+FFFD. The
// cost guard normally supplies a replayable, bounded body; the local limit
// keeps this helper safe when a handler is mounted without that middleware.
func bindStrictJSON(c *gin.Context, destination interface{}) error {
	if c == nil || c.Request == nil || c.Request.Body == nil || c.Request.Body == http.NoBody {
		return io.EOF
	}
	request := c.Request
	if request.ContentLength > maxChatJSONBodyBytes {
		return errChatJSONBodyTooLarge
	}

	originalBody := request.Body
	payload, err := io.ReadAll(io.LimitReader(originalBody, maxChatJSONBodyBytes+1))
	_ = originalBody.Close()
	request.Body = io.NopCloser(bytes.NewReader(payload))
	request.ContentLength = int64(len(payload))
	if err != nil {
		return err
	}
	if int64(len(payload)) > maxChatJSONBodyBytes {
		return errChatJSONBodyTooLarge
	}
	if !utf8.Valid(payload) || !validJSONSurrogatePairs(payload) {
		return errChatJSONUnicode
	}
	return c.ShouldBindJSON(destination)
}

// validJSONSurrogatePairs scans JSON string escapes without decoding them.
// Syntax other than surrogate pairing remains the JSON decoder's job.
func validJSONSurrogatePairs(payload []byte) bool {
	inString := false
	for index := 0; index < len(payload); index++ {
		switch payload[index] {
		case '"':
			inString = !inString
		case '\\':
			if !inString {
				continue
			}
			index++
			if index >= len(payload) {
				return false
			}
			if payload[index] != 'u' {
				continue
			}
			if index+4 >= len(payload) {
				return false
			}
			value, ok := parseJSONHexQuad(payload[index+1 : index+5])
			if !ok {
				return false
			}
			index += 4
			if isLowSurrogate(value) {
				return false
			}
			if !isHighSurrogate(value) {
				continue
			}
			if index+6 >= len(payload) || payload[index+1] != '\\' || payload[index+2] != 'u' {
				return false
			}
			low, ok := parseJSONHexQuad(payload[index+3 : index+7])
			if !ok || !isLowSurrogate(low) {
				return false
			}
			index += 6
		}
	}
	return true
}

func parseJSONHexQuad(value []byte) (uint16, bool) {
	if len(value) != 4 {
		return 0, false
	}
	var parsed uint16
	for _, digit := range value {
		parsed <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			parsed += uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			parsed += uint16(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			parsed += uint16(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return parsed, true
}

func isHighSurrogate(value uint16) bool {
	return value >= 0xd800 && value <= 0xdbff
}

func isLowSurrogate(value uint16) bool {
	return value >= 0xdc00 && value <= 0xdfff
}
