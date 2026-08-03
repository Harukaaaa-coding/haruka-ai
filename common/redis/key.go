package redis

import (
	"GopherAI/config"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
)

// key:特定邮箱-> 验证码
func GenerateCaptcha(email string) string {
	return fmt.Sprintf(config.DefaultRedisKeyConfig.CaptchaPrefix, identifierDigest(email))
}

func rateLimitKey(action, dimension, identifier string) string {
	identifier = strings.TrimSpace(strings.ToLower(identifier))
	if dimension == "ip" {
		if parsed := net.ParseIP(identifier); parsed != nil {
			identifier = parsed.String()
		}
	}
	return fmt.Sprintf("rate_limit:%s:%s:%s", action, dimension, identifierDigest(identifier))
}

func identifierDigest(identifier string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(strings.ToLower(identifier))))
	return hex.EncodeToString(digest[:16])
}

func GenerateIndexName(filename string) string {
	indexName := fmt.Sprintf(config.DefaultRedisKeyConfig.IndexName, filename)
	return indexName
}

func GenerateIndexNamePrefix(filename string) string {
	prefix := fmt.Sprintf(config.DefaultRedisKeyConfig.IndexNamePrefix, filename)
	return prefix
}
