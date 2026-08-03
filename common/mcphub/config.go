package mcphub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	RegistryPathEnv       = "GOPHERAI_MCP_REGISTRY_PATH"
	DefaultRegistryPath   = "config/mcp_servers.json"
	defaultTimeoutMS      = 10_000
	defaultApprovalTTL    = 5 * time.Minute
	maxRegistryFileBytes  = 1 << 20
	maxServers            = 32
	maxToolsPerServer     = 256
	maxReadOnlyRetryCount = 2
)

var (
	serverIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)
	envNamePattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	schemePattern   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,31}$`)
)

type EnvLookup func(string) (string, bool)

func RegistryPath(lookup EnvLookup) string {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if configured, ok := lookup(RegistryPathEnv); ok && strings.TrimSpace(configured) != "" {
		return filepath.Clean(strings.TrimSpace(configured))
	}
	return filepath.FromSlash(DefaultRegistryPath)
}

func LoadConfig(path string) (Config, error) {
	cleanPath := filepath.Clean(path)
	file, err := os.Open(cleanPath)
	if err != nil {
		return Config{}, newError(ErrorInvalidConfig, "MCP registry configuration is unavailable", err)
	}
	defer file.Close()

	limited := io.LimitReader(file, maxRegistryFileBytes+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return Config{}, newError(ErrorInvalidConfig, "MCP registry configuration cannot be read", err)
	}
	if len(payload) > maxRegistryFileBytes {
		return Config{}, newError(ErrorInvalidConfig, "MCP registry configuration is too large", nil)
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, newError(ErrorInvalidConfig, "MCP registry configuration contains invalid JSON", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return Config{}, newError(ErrorInvalidConfig, "MCP registry configuration contains trailing data", nil)
	}
	if err := ValidateConfig(&config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func ValidateConfig(config *Config) error {
	if config == nil {
		return newError(ErrorInvalidConfig, "MCP registry configuration is required", nil)
	}
	if len(config.Servers) > maxServers {
		return newError(ErrorInvalidConfig, fmt.Sprintf("MCP registry supports at most %d servers", maxServers), nil)
	}
	if config.ApprovalTTLSeconds < 0 || config.ApprovalTTLSeconds > int((30*time.Minute)/time.Second) {
		return newError(ErrorInvalidConfig, "approval_ttl_seconds must be between 0 and 1800", nil)
	}

	allowedServices, err := normalizedSet(config.ServiceAllowlist, validateServerID, "service_allowlist")
	if err != nil {
		return err
	}
	config.ServiceAllowlist = sortedKeys(allowedServices)

	seenServers := make(map[string]struct{}, len(config.Servers))
	for index := range config.Servers {
		server := &config.Servers[index]
		server.ID = strings.TrimSpace(server.ID)
		if err := validateServerID(server.ID); err != nil {
			return newError(ErrorInvalidConfig, fmt.Sprintf("servers[%d].id is invalid", index), err)
		}
		if _, exists := seenServers[server.ID]; exists {
			return newError(ErrorInvalidConfig, fmt.Sprintf("duplicate MCP server id %q", server.ID), nil)
		}
		seenServers[server.ID] = struct{}{}

		if err := validateServerConfig(server); err != nil {
			return newError(ErrorInvalidConfig, fmt.Sprintf("MCP server %q is invalid", server.ID), err)
		}
	}
	for allowed := range allowedServices {
		if _, configured := seenServers[allowed]; !configured {
			return newError(ErrorInvalidConfig, "service_allowlist contains an unconfigured server", nil)
		}
	}
	return nil
}

func validateServerConfig(server *ServerConfig) error {
	if server.TimeoutMS == 0 {
		server.TimeoutMS = defaultTimeoutMS
	}
	if server.TimeoutMS < 100 || server.TimeoutMS > 120_000 {
		return fmt.Errorf("timeout_ms must be between 100 and 120000")
	}
	if server.MaxReadOnlyRetries < 0 || server.MaxReadOnlyRetries > maxReadOnlyRetryCount {
		return fmt.Errorf("max_read_only_retries must be between 0 and %d", maxReadOnlyRetryCount)
	}

	parsedURL, err := url.Parse(strings.TrimSpace(server.URL))
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return fmt.Errorf("url must be an absolute HTTP URL")
	}
	if parsedURL.Scheme != "https" && parsedURL.Scheme != "http" {
		return fmt.Errorf("url scheme must be http or https")
	}
	if parsedURL.User != nil {
		return fmt.Errorf("url must not contain credentials")
	}
	if parsedURL.Fragment != "" {
		return fmt.Errorf("url must not contain a fragment")
	}
	if parsedURL.Scheme == "http" && !server.AllowInsecure && !isLoopbackHost(parsedURL.Hostname()) {
		return fmt.Errorf("external plain HTTP requires allow_insecure=true")
	}
	for queryName := range parsedURL.Query() {
		if sensitiveKey(queryName) {
			return fmt.Errorf("url must not contain credential-like query parameters")
		}
	}
	server.URL = parsedURL.String()

	toolAllowlist, err := normalizedSet(server.ToolAllowlist, validateToolName, "tool_allowlist")
	if err != nil {
		return err
	}
	if server.Enabled && len(toolAllowlist) == 0 {
		return fmt.Errorf("enabled servers require a non-empty tool_allowlist")
	}
	if len(toolAllowlist) > maxToolsPerServer {
		return fmt.Errorf("tool_allowlist supports at most %d tools", maxToolsPerServer)
	}
	server.ToolAllowlist = sortedKeys(toolAllowlist)

	readOnly, err := normalizedSet(server.ReadOnlyTools, validateToolName, "read_only_tools")
	if err != nil {
		return err
	}
	requiresApproval, err := normalizedSet(server.ApprovalRequiredTools, validateToolName, "approval_required_tools")
	if err != nil {
		return err
	}
	for name := range readOnly {
		if _, ok := toolAllowlist[name]; !ok {
			return fmt.Errorf("read_only_tools contains a tool that is not allowlisted")
		}
	}
	for name := range requiresApproval {
		if _, ok := toolAllowlist[name]; !ok {
			return fmt.Errorf("approval_required_tools contains a tool that is not allowlisted")
		}
	}
	server.ReadOnlyTools = sortedKeys(readOnly)
	server.ApprovalRequiredTools = sortedKeys(requiresApproval)

	for name, risk := range server.RiskLevels {
		if err := validateToolName(name); err != nil {
			return fmt.Errorf("risk_levels contains an invalid tool name")
		}
		if _, ok := toolAllowlist[name]; !ok {
			return fmt.Errorf("risk_levels contains a tool that is not allowlisted")
		}
		if !risk.valid() {
			return fmt.Errorf("risk_levels contains an invalid risk level")
		}
	}

	if server.Credential != nil {
		server.Credential.Env = strings.TrimSpace(server.Credential.Env)
		if !envNamePattern.MatchString(server.Credential.Env) {
			return fmt.Errorf("credential.env must be an environment variable name")
		}
		if server.Credential.Header == "" {
			server.Credential.Header = "Authorization"
		}
		server.Credential.Header = http.CanonicalHeaderKey(strings.TrimSpace(server.Credential.Header))
		if !validHeaderName(server.Credential.Header) {
			return fmt.Errorf("credential.header is invalid")
		}
		server.Credential.Scheme = strings.TrimSpace(server.Credential.Scheme)
		if server.Credential.Scheme != "" && !schemePattern.MatchString(server.Credential.Scheme) {
			return fmt.Errorf("credential.scheme is invalid")
		}
		if server.Credential.Scheme == "" && strings.EqualFold(server.Credential.Header, "Authorization") {
			server.Credential.Scheme = "Bearer"
		}
	}

	return nil
}

func (config Config) ApprovalTTL() time.Duration {
	if config.ApprovalTTLSeconds <= 0 {
		return defaultApprovalTTL
	}
	return time.Duration(config.ApprovalTTLSeconds) * time.Second
}

func (server ServerConfig) Timeout() time.Duration {
	return time.Duration(server.TimeoutMS) * time.Millisecond
}

func (config Config) serverAllowed(id string) bool {
	return contains(config.ServiceAllowlist, id)
}

func (server ServerConfig) toolAllowed(name string) bool {
	return contains(server.ToolAllowlist, name)
}

func (server ServerConfig) toolReadOnly(name string) bool {
	return contains(server.ReadOnlyTools, name)
}

func (server ServerConfig) toolApprovalRequired(name string) bool {
	return contains(server.ApprovalRequiredTools, name)
}

func validateServerID(value string) error {
	if !serverIDPattern.MatchString(value) {
		return fmt.Errorf("must match %s", serverIDPattern.String())
	}
	return nil
}

func validateToolName(value string) error {
	if !toolNamePattern.MatchString(strings.TrimSpace(value)) {
		return fmt.Errorf("tool name is invalid")
	}
	return nil
}

func normalizedSet(values []string, validator func(string) error, field string) (map[string]struct{}, error) {
	set := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if err := validator(value); err != nil {
			return nil, newError(ErrorInvalidConfig, field+" contains an invalid value", err)
		}
		set[value] = struct{}{}
	}
	return set, nil
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func contains(values []string, target string) bool {
	index := sort.SearchStrings(values, target)
	return index < len(values) && values[index] == target
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') &&
			!(character >= 'A' && character <= 'Z') &&
			!(character >= '0' && character <= '9') &&
			!strings.ContainsRune("!#$%&'*+-.^_`|~", character) {
			return false
		}
	}
	return true
}
