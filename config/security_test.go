package config

import (
	"strings"
	"testing"
)

const testProductionJWTKey = "M2u4xN7pQ9vB5kL8rT1wY6cD0fG3hJ7sV2zA4eR8"

func productionReadyConfig() *Config {
	return &Config{
		JwtConfig: JwtConfig{Key: testProductionJWTKey},
		MysqlConfig: MysqlConfig{
			MysqlPassword: "mysql-production-password",
		},
		RedisConfig: RedisConfig{RedisPassword: "redis-production-password"},
		Rabbitmq: Rabbitmq{
			RabbitmqUsername: "production-worker",
			RabbitmqPassword: "rabbit-production-password",
		},
	}
}

func TestValidateSecurityConfigurationAllowsLocalDevelopmentDefaults(t *testing.T) {
	conf := &Config{
		JwtConfig:   JwtConfig{Key: defaultJWTKey},
		MysqlConfig: MysqlConfig{MysqlPassword: "gopherai_dev_password"},
		RedisConfig: RedisConfig{},
		Rabbitmq:    Rabbitmq{RabbitmqPassword: "gopherai_dev_password"},
	}
	if err := validateSecurityConfiguration(conf, lookupFrom(map[string]string{
		environmentVariable: "development",
	})); err != nil {
		t.Fatalf("development validation failed: %v", err)
	}
}

func TestValidateSecurityConfigurationRejectsProductionDefaults(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*Config)
		wantInError string
	}{
		{
			name: "default JWT key",
			mutate: func(conf *Config) {
				conf.JwtConfig.Key = defaultJWTKey
			},
			wantInError: "JWT key",
		},
		{
			name: "low entropy JWT key",
			mutate: func(conf *Config) {
				conf.JwtConfig.Key = strings.Repeat("a", minimumJWTKeyBytes)
			},
			wantInError: "low entropy",
		},
		{
			name: "development MySQL password",
			mutate: func(conf *Config) {
				conf.MysqlConfig.MysqlPassword = "gopherai_dev_password"
			},
			wantInError: "MySQL",
		},
		{
			name: "missing Redis password",
			mutate: func(conf *Config) {
				conf.RedisConfig.RedisPassword = ""
			},
			wantInError: "Redis",
		},
		{
			name: "development Redis password",
			mutate: func(conf *Config) {
				conf.RedisConfig.RedisPassword = "gopherai_redis_dev_password"
			},
			wantInError: "Redis",
		},
		{
			name: "default RabbitMQ password",
			mutate: func(conf *Config) {
				conf.Rabbitmq.RabbitmqPassword = "gopherai_dev_password"
			},
			wantInError: "RabbitMQ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf := productionReadyConfig()
			tt.mutate(conf)
			err := validateSecurityConfiguration(conf, lookupFrom(map[string]string{
				environmentVariable: "production",
			}))
			if err == nil {
				t.Fatal("production validation error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantInError) {
				t.Fatalf("error = %q, want %q", err, tt.wantInError)
			}
			if strings.Contains(err.Error(), conf.JwtConfig.Key) || strings.Contains(err.Error(), conf.MysqlConfig.MysqlPassword) {
				t.Fatalf("error exposed a configured secret: %q", err)
			}
		})
	}
}

func TestValidateSecurityConfigurationAcceptsProductionSecrets(t *testing.T) {
	if err := validateSecurityConfiguration(productionReadyConfig(), lookupFrom(map[string]string{
		environmentVariable: "production",
	})); err != nil {
		t.Fatalf("production validation failed: %v", err)
	}
}

func TestProductionEnvironmentValidation(t *testing.T) {
	tests := []struct {
		value    string
		wantProd bool
		wantErr  bool
	}{
		{value: "", wantProd: false},
		{value: "dev", wantProd: false},
		{value: "staging", wantProd: true},
		{value: "production", wantProd: true},
		{value: "unexpected", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			prod, err := isProductionEnvironment(lookupFrom(map[string]string{
				environmentVariable: tt.value,
			}))
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, want error=%t", err, tt.wantErr)
			}
			if prod != tt.wantProd {
				t.Fatalf("production = %t, want %t", prod, tt.wantProd)
			}
		})
	}
}

func TestIsProduction(t *testing.T) {
	t.Setenv(environmentVariable, "production")
	if !IsProduction() {
		t.Fatal("IsProduction() = false, want true")
	}
	t.Setenv(environmentVariable, "development")
	if IsProduction() {
		t.Fatal("IsProduction() = true, want false")
	}
}

func TestLoadConfigRejectsProductionDevelopmentDefaults(t *testing.T) {
	_, err := loadConfig("config.toml", "", lookupFrom(map[string]string{
		environmentVariable: "production",
	}))
	if err == nil {
		t.Fatal("loadConfig() error = nil, want production security error")
	}
	if !strings.Contains(err.Error(), "JWT key") {
		t.Fatalf("loadConfig() error = %q, want JWT key validation", err)
	}
}
