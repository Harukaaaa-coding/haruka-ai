package utils

import (
	"GopherAI/model"
	"crypto/md5"
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

// SecureRandomNumbers returns a decimal string generated with crypto/rand.
// It uses rejection sampling so the last digit is not biased by a byte modulo
// operation. Use this for credentials, OTPs, captchas and public identifiers.
func SecureRandomNumbers(num int) (string, error) {
	return secureRandomNumbersFromReader(cryptorand.Reader, num)
}

func secureRandomNumbersFromReader(reader io.Reader, num int) (string, error) {
	if num < 1 {
		return "", fmt.Errorf("random number length must be positive")
	}
	if reader == nil {
		return "", fmt.Errorf("secure random source is unavailable")
	}

	const digits = "0123456789"
	const largestUnbiasedByte = 250 // 250 is evenly divisible by len(digits).
	result := make([]byte, 0, num)
	buffer := make([]byte, num)
	for len(result) < num {
		if _, err := io.ReadFull(reader, buffer); err != nil {
			return "", err
		}
		for _, value := range buffer {
			if value >= largestUnbiasedByte {
				continue
			}
			result = append(result, digits[int(value)%len(digits)])
			if len(result) == num {
				break
			}
		}
	}
	return string(result), nil
}

// GetRandomNumbers is kept for compatibility with existing callers. New
// security-sensitive code should use SecureRandomNumbers so failures can be
// handled instead of silently producing an empty value.
func GetRandomNumbers(num int) string {
	value, err := SecureRandomNumbers(num)
	if err != nil {
		return ""
	}
	return value
}

// MD5 returns an MD5 digest for legacy password-hash migration only.
func MD5(str string) string {
	m := md5.New()
	_, _ = m.Write([]byte(str))
	return hex.EncodeToString(m.Sum(nil))
}

func GenerateUUID() string {
	return uuid.New().String()
}

func ConvertToModelMessage(sessionID string, userName string, msg *schema.Message) *model.Message {
	return &model.Message{
		SessionID: sessionID,
		UserName:  userName,
		Content:   msg.Content,
	}
}

func ConvertToSchemaMessages(msgs []*model.Message) []*schema.Message {
	schemaMsgs := make([]*schema.Message, 0, len(msgs))
	for _, m := range msgs {
		role := schema.Assistant
		if m.IsUser {
			role = schema.User
		}
		schemaMsgs = append(schemaMsgs, &schema.Message{
			Role:    role,
			Content: m.Content,
		})
	}
	return schemaMsgs
}

func RemoveAllFilesInDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			filePath := filepath.Join(dir, entry.Name())
			if err := os.Remove(filePath); err != nil {
				return err
			}
		}
	}
	return nil
}

func ValidateFile(file *multipart.FileHeader) error {
	ext := strings.ToLower(filepath.Ext(file.Filename))
	if ext != ".md" && ext != ".txt" {
		return fmt.Errorf("unsupported file type: only .md and .txt files are allowed (got %s)", ext)
	}
	return nil
}
