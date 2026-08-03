package config

import (
	"fmt"
	"math"
	"os"
	"strings"
	"unicode/utf8"
)

const (
	environmentVariable = "GOPHERAI_ENV"
	minimumJWTKeyBytes  = 32
	defaultJWTKey       = "dev-only-change-before-production"
)

// validateSecurityConfiguration rejects development credentials when the
// process is explicitly configured for a non-local environment. Keeping this
// opt-in preserves the zero-setup local development experience while making a
// production deployment fail closed instead of silently using example values.
func validateSecurityConfiguration(conf *Config, lookup envLookup) error {
	production, err := isProductionEnvironment(lookup)
	if err != nil || !production {
		return err
	}
	if conf == nil {
		return fmt.Errorf("production configuration is unavailable")
	}
	if err := validateProductionJWTKey(conf.JwtConfig.Key); err != nil {
		return err
	}
	if err := validateProductionInfrastructureCredentials(conf); err != nil {
		return err
	}
	return nil
}

func isProductionEnvironment(lookup envLookup) (bool, error) {
	raw, configured := lookup(environmentVariable)
	if !configured || strings.TrimSpace(raw) == "" {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "development", "dev", "local", "test":
		return false, nil
	case "production", "prod", "staging", "stage":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be one of development, test, staging, or production", environmentVariable)
	}
}

// IsProduction reports whether the explicitly configured runtime mode requires
// production-safe behavior. Configuration initialization still returns an
// error for an unknown GOPHERAI_ENV value; this helper intentionally returns
// false in that invalid state so callers cannot accidentally weaken security.
func IsProduction() bool {
	production, err := isProductionEnvironment(os.LookupEnv)
	return err == nil && production
}

func validateProductionJWTKey(key string) error {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return fmt.Errorf("production JWT key is required; set GOPHERAI_JWT_KEY to a random secret of at least %d bytes", minimumJWTKeyBytes)
	}
	if strings.EqualFold(trimmed, defaultJWTKey) || isKnownWeakJWTKey(trimmed) {
		return fmt.Errorf("production JWT key uses a known development or placeholder value; set GOPHERAI_JWT_KEY to a random secret of at least %d bytes", minimumJWTKeyBytes)
	}
	if len([]byte(trimmed)) < minimumJWTKeyBytes {
		return fmt.Errorf("production JWT key must be at least %d bytes", minimumJWTKeyBytes)
	}
	if !hasSufficientSecretEntropy(trimmed) {
		return fmt.Errorf("production JWT key appears low entropy; set GOPHERAI_JWT_KEY to a securely generated random secret")
	}
	return nil
}

func validateProductionInfrastructureCredentials(conf *Config) error {
	mysqlPassword := strings.TrimSpace(conf.MysqlConfig.MysqlPassword)
	if mysqlPassword == "" {
		return fmt.Errorf("production MySQL password is required; set GOPHERAI_MYSQL_PASSWORD")
	}
	if strings.EqualFold(mysqlPassword, "gopherai_dev_password") {
		return fmt.Errorf("production MySQL password uses the development default; set GOPHERAI_MYSQL_PASSWORD")
	}

	redisPassword := strings.TrimSpace(conf.RedisConfig.RedisPassword)
	if redisPassword == "" {
		return fmt.Errorf("production Redis password is required; set GOPHERAI_REDIS_PASSWORD")
	}
	if strings.EqualFold(redisPassword, "gopherai_redis_dev_password") {
		return fmt.Errorf("production Redis password uses the development default; set GOPHERAI_REDIS_PASSWORD")
	}

	rabbitPassword := strings.TrimSpace(conf.Rabbitmq.RabbitmqPassword)
	if rabbitPassword == "" {
		return fmt.Errorf("production RabbitMQ password is required; set GOPHERAI_RABBITMQ_PASSWORD")
	}
	if strings.EqualFold(rabbitPassword, "gopherai_dev_password") ||
		(strings.EqualFold(strings.TrimSpace(conf.Rabbitmq.RabbitmqUsername), "guest") && strings.EqualFold(rabbitPassword, "guest")) {
		return fmt.Errorf("production RabbitMQ credentials use a development or default value; set GOPHERAI_RABBITMQ_USERNAME and GOPHERAI_RABBITMQ_PASSWORD")
	}
	return nil
}

func isKnownWeakJWTKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "change-me", "changeme", "secret", "jwt-secret", "jwt_secret", "your-secret-key", "your-jwt-secret":
		return true
	default:
		return false
	}
}

func hasSufficientSecretEntropy(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}

	counts := make(map[rune]int)
	length := 0
	for _, r := range value {
		counts[r]++
		length++
	}
	if length == 0 || len(counts) < 8 || containsPredictableSequence(strings.ToLower(value)) {
		return false
	}

	// A random hexadecimal secret has roughly four bits of entropy per
	// character. Requiring three permits standard random encodings while
	// rejecting repeated/simple patterns and low-variety passphrases.
	entropy := 0.0
	for _, count := range counts {
		probability := float64(count) / float64(length)
		entropy -= probability * math.Log2(probability)
	}
	return entropy >= 3.0
}

func containsPredictableSequence(value string) bool {
	for _, sequence := range []string{
		"0123456789",
		"9876543210",
		"abcdefghijklmnopqrstuvwxyz",
		"zyxwvutsrqponmlkjihgfedcba",
		"qwertyuiopasdfghjklzxcvbnm",
		"mnbvcxzlkjhgfdsaqpoiuytrewq",
	} {
		for start := 0; start+5 <= len(sequence); start++ {
			if strings.Contains(value, sequence[start:start+5]) {
				return true
			}
		}
	}
	return false
}
