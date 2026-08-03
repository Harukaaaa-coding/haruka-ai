package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func lookupFrom(values map[string]string) envLookup {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func TestLoadConfigAppliesLocalThenEnvironmentOverrides(t *testing.T) {
	loaded, err := loadConfig(
		filepath.Join("testdata", "base.toml"),
		filepath.Join("testdata", "local.toml"),
		lookupFrom(map[string]string{
			"GOPHERAI_EMAIL":                 "environment@example.test",
			"GOPHERAI_MYSQL_PASSWORD":        "environment-mysql-password",
			"GOPHERAI_RAG_BASE_URL":          "http://127.0.0.1:11434/v1",
			"GOPHERAI_RAG_DIMENSION":         "768",
			"GOPHERAI_OLLAMA_MODEL":          "deepseek-r1:1.5b",
			"GOPHERAI_BAIDU_TTS_SECRET_KEY":  "environment-tts-secret",
			"GOPHERAI_VOICE_ASR_PROVIDER":    "test-asr",
			"GOPHERAI_FISH_AUDIO_API_KEY":    "environment-fish-key",
			"GOPHERAI_VOICE_ALLOWED_ORIGINS": "https://voice.example.test",
		}),
	)
	if err != nil {
		t.Fatalf("loadConfig(): %v", err)
	}

	if got := loaded.EmailConfig.Email; got != "environment@example.test" {
		t.Fatalf("email = %q, want environment override", got)
	}
	if got := loaded.EmailConfig.Authcode; got != "local-auth" {
		t.Fatalf("authcode = %q, want local override", got)
	}
	if got := loaded.MysqlConfig.MysqlPassword; got != "environment-mysql-password" {
		t.Fatalf("MySQL password = %q, want environment override", got)
	}
	if got := loaded.RagModelConfig.RagChatModelName; got != "local-chat" {
		t.Fatalf("RAG chat model = %q, want local override", got)
	}
	if got := loaded.RagModelConfig.RagDimension; got != 768 {
		t.Fatalf("RAG dimension = %d, want 768", got)
	}
	if got := loaded.OllamaConfig.OllamaModelName; got != "deepseek-r1:1.5b" {
		t.Fatalf("Ollama model = %q, want environment override", got)
	}
	if got := loaded.VoiceServiceConfig.VoiceServiceApiKey; got != "local-tts-key" {
		t.Fatalf("TTS API key = %q, want local override", got)
	}
	if got := loaded.VoiceServiceConfig.VoiceServiceSecretKey; got != "environment-tts-secret" {
		t.Fatalf("TTS secret = %q, want environment override", got)
	}
	if got := loaded.VoiceRealtimeConfig.FishAudioAPIKey; got != "environment-fish-key" {
		t.Fatalf("Fish Audio API key = %q, want environment override", got)
	}
	if got := loaded.VoiceRealtimeConfig.ASRProvider; got != "test-asr" {
		t.Fatalf("voice ASR provider = %q, want environment override", got)
	}
	if got := loaded.VoiceRealtimeConfig.AllowedOrigins; got != "https://voice.example.test" {
		t.Fatalf("voice allowed origins = %q, want environment override", got)
	}
}

func TestCustomConfigPathDisablesAutomaticLocalOverlay(t *testing.T) {
	custom := filepath.Join("testdata", "base.toml")
	mainPath, localPath := resolveConfigPaths(lookupFrom(map[string]string{
		"GOPHERAI_CONFIG_PATH": custom,
	}))
	if mainPath != filepath.Clean(custom) {
		t.Fatalf("main path = %q, want %q", mainPath, filepath.Clean(custom))
	}
	if localPath != "" {
		t.Fatalf("local path = %q, want disabled", localPath)
	}
}

func TestInvalidIntegerOverridesDoNotExposeValues(t *testing.T) {
	const sensitiveInvalidValue = "not-a-number-private-value"
	for _, variable := range []string{"GOPHERAI_APP_PORT", "GOPHERAI_MYSQL_PORT", "GOPHERAI_RAG_DIMENSION"} {
		t.Run(variable, func(t *testing.T) {
			err := applyEnvironmentOverrides(new(Config), lookupFrom(map[string]string{
				variable: sensitiveInvalidValue,
			}))
			if err == nil {
				t.Fatal("applyEnvironmentOverrides() error = nil, want validation error")
			}
			if strings.Contains(err.Error(), sensitiveInvalidValue) {
				t.Fatalf("validation error exposed the environment value: %v", err)
			}
			if !strings.Contains(err.Error(), variable) {
				t.Fatalf("validation error %q does not identify %s", err, variable)
			}
		})
	}
}

func TestResolveRAGAPIKey(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		env     map[string]string
		want    string
		wantErr bool
	}{
		{
			name:    "dedicated key wins",
			baseURL: "https://api.example.test/v1",
			env: map[string]string{
				"GOPHERAI_RAG_API_KEY": "rag-key",
				"OPENAI_API_KEY":       "general-key",
			},
			want: "rag-key",
		},
		{
			name:    "OpenAI key fallback",
			baseURL: "https://api.example.test/v1",
			env:     map[string]string{"OPENAI_API_KEY": "general-key"},
			want:    "general-key",
		},
		{
			name:    "localhost placeholder",
			baseURL: "http://localhost:11434/v1",
			want:    "ollama",
		},
		{
			name:    "loopback IP placeholder",
			baseURL: "http://127.0.0.1:11434/v1",
			want:    "ollama",
		},
		{
			name:    "external service requires key",
			baseURL: "https://api.example.test/v1",
			wantErr: true,
		},
		{
			name:    "lookalike localhost is external",
			baseURL: "https://localhost.example.test/v1",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveRAGAPIKey(tt.baseURL, lookupFrom(tt.env))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveRAGAPIKey() = %q, nil; want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveRAGAPIKey(): %v", err)
			}
			if got != tt.want {
				t.Fatalf("resolveRAGAPIKey() = %q, want %q", got, tt.want)
			}
		})
	}
}
