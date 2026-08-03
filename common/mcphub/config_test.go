package mcphub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateConfigNormalizesAndEnforcesAllowlists(t *testing.T) {
	configuration := Config{
		ServiceAllowlist: []string{"beta", "alpha", "alpha"},
		Servers: []ServerConfig{
			{
				ID:            "alpha",
				URL:           "http://127.0.0.1:8081/mcp",
				Enabled:       true,
				ToolAllowlist: []string{"zeta", "read", "read"},
				ReadOnlyTools: []string{"read"},
				RiskLevels:    map[string]RiskLevel{"read": RiskLow},
				Credential:    &CredentialReference{Env: "MCP_ALPHA_TOKEN"},
			},
			{
				ID: "beta", URL: "https://example.test/mcp", Enabled: false,
			},
		},
	}
	if err := ValidateConfig(&configuration); err != nil {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
	if got := strings.Join(configuration.ServiceAllowlist, ","); got != "alpha,beta" {
		t.Fatalf("service allowlist = %q", got)
	}
	if got := strings.Join(configuration.Servers[0].ToolAllowlist, ","); got != "read,zeta" {
		t.Fatalf("tool allowlist = %q", got)
	}
	credential := configuration.Servers[0].Credential
	if credential.Header != "Authorization" || credential.Scheme != "Bearer" {
		t.Fatalf("credential defaults = %#v", credential)
	}
}

func TestValidateConfigRejectsCredentialBearingURLAndUnallowlistedPolicy(t *testing.T) {
	tests := []struct {
		name   string
		server ServerConfig
	}{
		{
			name: "userinfo",
			server: ServerConfig{
				ID: "bad", URL: "https://user:password@example.test/mcp", Enabled: true, ToolAllowlist: []string{"read"},
			},
		},
		{
			name: "sensitive query",
			server: ServerConfig{
				ID: "bad", URL: "https://example.test/mcp?api_key=private", Enabled: true, ToolAllowlist: []string{"read"},
			},
		},
		{
			name: "external insecure URL",
			server: ServerConfig{
				ID: "bad", URL: "http://example.test/mcp", Enabled: true, ToolAllowlist: []string{"read"},
			},
		},
		{
			name: "read only tool not allowlisted",
			server: ServerConfig{
				ID: "bad", URL: "https://example.test/mcp", Enabled: true, ToolAllowlist: []string{"read"}, ReadOnlyTools: []string{"write"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configuration := Config{ServiceAllowlist: []string{"bad"}, Servers: []ServerConfig{test.server}}
			if err := ValidateConfig(&configuration); err == nil {
				t.Fatal("ValidateConfig() error = nil")
			}
		})
	}
}

func TestLoadConfigRejectsLiteralCredentialFieldsWithoutEchoingValue(t *testing.T) {
	const secret = "literal-secret-must-not-appear"
	path := filepath.Join(t.TempDir(), "registry.json")
	payload := `{
  "service_allowlist":["demo"],
  "servers":[{
    "id":"demo",
    "url":"https://example.test/mcp",
    "enabled":true,
    "tool_allowlist":["read"],
    "credential":{"env":"DEMO_TOKEN","api_key":"` + secret + `"}
  }]
}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig() error = nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error exposed literal secret: %v", err)
	}
}

func TestDefaultRegistryConfigurationPreservesWeatherService(t *testing.T) {
	path := filepath.Join("..", "..", "config", "mcp_servers.json")
	configuration, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(default): %v", err)
	}
	if len(configuration.Servers) != 1 || configuration.Servers[0].ID != "weather" {
		t.Fatalf("default servers = %#v", configuration.Servers)
	}
	if !configuration.serverAllowed("weather") || !configuration.Servers[0].toolAllowed("get_weather") {
		t.Fatal("weather service/tool is not allowlisted")
	}
}
