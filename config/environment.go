package config

import (
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

func applyEnvironmentOverrides(conf *Config, lookup envLookup) error {
	stringOverrides := []struct {
		name   string
		target *string
	}{
		{"GOPHERAI_APP_HOST", &conf.MainConfig.Host},
		{"GOPHERAI_EMAIL", &conf.EmailConfig.Email},
		{"GOPHERAI_EMAIL_AUTHCODE", &conf.EmailConfig.Authcode},
		{"GOPHERAI_MYSQL_HOST", &conf.MysqlConfig.MysqlHost},
		{"GOPHERAI_MYSQL_USER", &conf.MysqlConfig.MysqlUser},
		{"GOPHERAI_MYSQL_PASSWORD", &conf.MysqlConfig.MysqlPassword},
		{"GOPHERAI_MYSQL_DATABASE", &conf.MysqlConfig.MysqlDatabaseName},
		{"GOPHERAI_REDIS_HOST", &conf.RedisConfig.RedisHost},
		{"GOPHERAI_REDIS_PASSWORD", &conf.RedisConfig.RedisPassword},
		{"GOPHERAI_RABBITMQ_HOST", &conf.Rabbitmq.RabbitmqHost},
		{"GOPHERAI_RABBITMQ_USERNAME", &conf.Rabbitmq.RabbitmqUsername},
		{"GOPHERAI_RABBITMQ_PASSWORD", &conf.Rabbitmq.RabbitmqPassword},
		{"GOPHERAI_RABBITMQ_VHOST", &conf.Rabbitmq.RabbitmqVhost},
		{"GOPHERAI_JWT_KEY", &conf.JwtConfig.Key},
		{"GOPHERAI_RAG_BASE_URL", &conf.RagModelConfig.RagBaseUrl},
		{"GOPHERAI_RAG_EMBEDDING_MODEL", &conf.RagModelConfig.RagEmbeddingModel},
		{"GOPHERAI_RAG_CHAT_MODEL", &conf.RagModelConfig.RagChatModelName},
		{"GOPHERAI_RAG_DOC_DIR", &conf.RagModelConfig.RagDocDir},
		{"GOPHERAI_OLLAMA_BASE_URL", &conf.OllamaConfig.OllamaBaseURL},
		{"GOPHERAI_OLLAMA_MODEL", &conf.OllamaConfig.OllamaModelName},
		{"GOPHERAI_BAIDU_TTS_API_KEY", &conf.VoiceServiceConfig.VoiceServiceApiKey},
		{"GOPHERAI_BAIDU_TTS_SECRET_KEY", &conf.VoiceServiceConfig.VoiceServiceSecretKey},
		{"GOPHERAI_VOICE_ASR_PROVIDER", &conf.VoiceRealtimeConfig.ASRProvider},
		{"GOPHERAI_FISH_AUDIO_API_KEY", &conf.VoiceRealtimeConfig.FishAudioAPIKey},
		{"GOPHERAI_FISH_AUDIO_BASE_URL", &conf.VoiceRealtimeConfig.FishAudioBaseURL},
		{"GOPHERAI_FISH_AUDIO_MODEL", &conf.VoiceRealtimeConfig.FishAudioModel},
		{"GOPHERAI_FISH_AUDIO_REFERENCE_ID", &conf.VoiceRealtimeConfig.FishAudioReferenceID},
		{"GOPHERAI_VOICE_ALLOWED_ORIGINS", &conf.VoiceRealtimeConfig.AllowedOrigins},
	}

	for _, override := range stringOverrides {
		if value, ok := lookup(override.name); ok {
			*override.target = value
		}
	}

	portOverrides := []struct {
		name   string
		target *int
	}{
		{"GOPHERAI_APP_PORT", &conf.MainConfig.Port},
		{"GOPHERAI_MYSQL_PORT", &conf.MysqlConfig.MysqlPort},
		{"GOPHERAI_REDIS_PORT", &conf.RedisConfig.RedisPort},
		{"GOPHERAI_RABBITMQ_PORT", &conf.Rabbitmq.RabbitmqPort},
	}
	for _, override := range portOverrides {
		if err := applyIntegerOverride(override.name, override.target, 1, 65535, lookup); err != nil {
			return err
		}
	}

	if err := applyIntegerOverride("GOPHERAI_REDIS_DB", &conf.RedisConfig.RedisDb, 0, int(^uint(0)>>1), lookup); err != nil {
		return err
	}
	if err := applyIntegerOverride("GOPHERAI_JWT_EXPIRE_HOURS", &conf.JwtConfig.ExpireDuration, 1, int(^uint(0)>>1), lookup); err != nil {
		return err
	}
	if err := applyIntegerOverride("GOPHERAI_RAG_DIMENSION", &conf.RagModelConfig.RagDimension, 1, int(^uint(0)>>1), lookup); err != nil {
		return err
	}
	if err := applyIntegerOverride("GOPHERAI_RAG_CANDIDATE_FACTOR", &conf.RagModelConfig.RagCandidateFactor, 1, 20, lookup); err != nil {
		return err
	}
	if err := applyIntegerOverride("GOPHERAI_RAG_RRF_K", &conf.RagModelConfig.RagRRFK, 1, 1000, lookup); err != nil {
		return err
	}
	if raw, ok := lookup("GOPHERAI_RAG_MIN_SCORE"); ok {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 || parsed > 1 {
			return fmt.Errorf("environment variable GOPHERAI_RAG_MIN_SCORE must be a number between 0 and 1")
		}
		conf.RagModelConfig.RagMinScore = parsed
	}
	if raw, ok := lookup("GOPHERAI_RAG_RERANK_ENABLED"); ok {
		parsed, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("environment variable GOPHERAI_RAG_RERANK_ENABLED must be a boolean")
		}
		conf.RagModelConfig.RagRerankEnabled = parsed
	}

	return nil
}

func applyIntegerOverride(name string, target *int, minimum, maximum int, lookup envLookup) error {
	rawValue, ok := lookup(name)
	if !ok {
		return nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(rawValue))
	if err != nil || parsed < minimum || parsed > maximum {
		return fmt.Errorf("environment variable %s must be an integer between %d and %d", name, minimum, maximum)
	}
	*target = parsed
	return nil
}

func validateRAGConfiguration(conf *Config) error {
	if conf == nil {
		return fmt.Errorf("RAG configuration is unavailable")
	}
	value := conf.RagModelConfig.RagMinScore
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
		return fmt.Errorf("RAG minScore must be a finite number between 0 and 1")
	}
	return nil
}

// ResolveRAGAPIKey resolves credentials for the OpenAI-compatible clients used
// by RAG and MCP. Local loopback services receive a non-secret placeholder
// because some clients require a non-empty key even when Ollama ignores it.
func ResolveRAGAPIKey(baseURL string) (string, error) {
	return resolveRAGAPIKey(baseURL, os.LookupEnv)
}

func resolveRAGAPIKey(baseURL string, lookup envLookup) (string, error) {
	for _, name := range []string{"GOPHERAI_RAG_API_KEY", "OPENAI_API_KEY"} {
		if value, ok := lookup(name); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), nil
		}
	}
	if isLoopbackBaseURL(baseURL) {
		return "ollama", nil
	}
	return "", fmt.Errorf("RAG API key is required for a non-local base URL; set GOPHERAI_RAG_API_KEY or OPENAI_API_KEY")
}

func isLoopbackBaseURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
