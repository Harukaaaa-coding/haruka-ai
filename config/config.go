package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

const (
	defaultConfigPath      = "config/config.toml"
	defaultLocalConfigPath = "config/config.local.toml"
)

type envLookup func(string) (string, bool)

type MainConfig struct {
	Port    int    `toml:"port"`
	AppName string `toml:"appName"`
	Host    string `toml:"host"`
}

type EmailConfig struct {
	Authcode string `toml:"authcode"`
	Email    string `toml:"email"`
}

type RedisConfig struct {
	RedisPort     int    `toml:"port"`
	RedisDb       int    `toml:"db"`
	RedisHost     string `toml:"host"`
	RedisPassword string `toml:"password"`
}

type MysqlConfig struct {
	MysqlPort         int    `toml:"port"`
	MysqlHost         string `toml:"host"`
	MysqlUser         string `toml:"user"`
	MysqlPassword     string `toml:"password"`
	MysqlDatabaseName string `toml:"databaseName"`
	MysqlCharset      string `toml:"charset"`
}

type JwtConfig struct {
	ExpireDuration int    `toml:"expire_duration"`
	Issuer         string `toml:"issuer"`
	Subject        string `toml:"subject"`
	Key            string `toml:"key"`
}

type Rabbitmq struct {
	RabbitmqPort     int    `toml:"port"`
	RabbitmqHost     string `toml:"host"`
	RabbitmqUsername string `toml:"username"`
	RabbitmqPassword string `toml:"password"`
	RabbitmqVhost    string `toml:"vhost"`
}

type RagModelConfig struct {
	RagEmbeddingModel  string  `toml:"embeddingModel"`
	RagChatModelName   string  `toml:"chatModelName"`
	RagDocDir          string  `toml:"docDir"`
	RagBaseUrl         string  `toml:"baseUrl"`
	RagDimension       int     `toml:"dimension"`
	RagMinScore        float64 `toml:"minScore"`
	RagCandidateFactor int     `toml:"candidateFactor"`
	RagRRFK            int     `toml:"rrfK"`
	RagRerankEnabled   bool    `toml:"rerankEnabled"`
}

type OllamaConfig struct {
	OllamaBaseURL   string `toml:"baseUrl"`
	OllamaModelName string `toml:"modelName"`
}

type VoiceServiceConfig struct {
	VoiceServiceApiKey    string `toml:"voiceServiceApiKey"`
	VoiceServiceSecretKey string `toml:"voiceServiceSecretKey"`
}

// VoiceRealtimeConfig contains server-side credentials and policy for the
// realtime voice path.  It intentionally has its own block instead of
// extending the legacy Baidu batch-ASR/TTS configuration: a realtime profile
// can be enabled or disabled without changing existing voice endpoints.
type VoiceRealtimeConfig struct {
	ASRProvider          string `toml:"asrProvider"`
	FishAudioAPIKey      string `toml:"fishAudioApiKey"`
	FishAudioBaseURL     string `toml:"fishAudioBaseUrl"`
	FishAudioModel       string `toml:"fishAudioModel"`
	FishAudioReferenceID string `toml:"fishAudioReferenceId"`
	AllowedOrigins       string `toml:"allowedOrigins"`
}

type Config struct {
	EmailConfig         `toml:"emailConfig"`
	RedisConfig         `toml:"redisConfig"`
	MysqlConfig         `toml:"mysqlConfig"`
	JwtConfig           `toml:"jwtConfig"`
	MainConfig          `toml:"mainConfig"`
	Rabbitmq            `toml:"rabbitmqConfig"`
	RagModelConfig      `toml:"ragModelConfig"`
	OllamaConfig        `toml:"ollamaConfig"`
	VoiceServiceConfig  `toml:"voiceServiceConfig"`
	VoiceRealtimeConfig `toml:"voiceRealtimeConfig"`
}

type RedisKeyConfig struct {
	CaptchaPrefix   string
	IndexName       string
	IndexNamePrefix string
}

var DefaultRedisKeyConfig = RedisKeyConfig{
	CaptchaPrefix:   "captcha:%s",
	IndexName:       "rag_docs:%s:idx",
	IndexNamePrefix: "rag_docs:%s:",
}

var (
	config            *Config
	configErr         error
	configInitialized bool
	configMu          sync.RWMutex
	configInitMu      sync.Mutex
)

// InitConfig loads the configured TOML files and applies environment
// overrides. GOPHERAI_CONFIG_PATH selects a complete standalone main file and
// disables the automatic config/config.local.toml overlay.
func InitConfig() error {
	configInitMu.Lock()
	defer configInitMu.Unlock()

	loaded, err := loadConfiguredConfig(os.LookupEnv)

	configMu.Lock()
	defer configMu.Unlock()
	configInitialized = true
	configErr = err
	if err != nil {
		config = new(Config)
		return err
	}
	config = loaded
	return nil
}

func GetConfig() *Config {
	configMu.RLock()
	if configInitialized {
		loaded := config
		configMu.RUnlock()
		return loaded
	}
	configMu.RUnlock()

	if err := InitConfig(); err != nil {
		log.Printf("configuration initialization failed: %v", err)
	}

	configMu.RLock()
	defer configMu.RUnlock()
	return config
}

// GetConfigError reports the most recent initialization error without
// exposing configuration values.
func GetConfigError() error {
	configMu.RLock()
	defer configMu.RUnlock()
	return configErr
}

func loadConfiguredConfig(lookup envLookup) (*Config, error) {
	mainPath, localPath := resolveConfigPaths(lookup)
	return loadConfig(mainPath, localPath, lookup)
}

func resolveConfigPaths(lookup envLookup) (string, string) {
	if configuredPath, ok := lookup("GOPHERAI_CONFIG_PATH"); ok {
		configuredPath = strings.TrimSpace(configuredPath)
		if configuredPath != "" {
			return filepath.Clean(configuredPath), ""
		}
	}
	return filepath.FromSlash(defaultConfigPath), filepath.FromSlash(defaultLocalConfigPath)
}

func loadConfig(mainPath, localPath string, lookup envLookup) (*Config, error) {
	loaded := new(Config)
	if err := decodeConfigFile(mainPath, loaded, false); err != nil {
		return nil, err
	}
	if localPath != "" {
		if err := decodeConfigFile(localPath, loaded, true); err != nil {
			return nil, err
		}
	}
	if err := applyEnvironmentOverrides(loaded, lookup); err != nil {
		return nil, err
	}
	if err := validateRAGConfiguration(loaded); err != nil {
		return nil, err
	}
	if err := validateSecurityConfiguration(loaded, lookup); err != nil {
		return nil, err
	}
	return loaded, nil
}

func decodeConfigFile(path string, destination *Config, optional bool) error {
	cleanPath := filepath.Clean(path)
	if _, err := os.Stat(cleanPath); err != nil {
		if optional && os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("configuration file %q is unavailable: %w", cleanPath, err)
	}
	if _, err := toml.DecodeFile(cleanPath, destination); err != nil {
		// Do not include the parser's source excerpt because it may contain a
		// password or API key from the malformed line.
		return fmt.Errorf("configuration file %q contains invalid TOML", cleanPath)
	}
	return nil
}
