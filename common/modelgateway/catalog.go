// Package modelgateway owns the runtime model catalog.  It deliberately keeps
// providers, upstream models and application pipelines as separate records so
// that a provider can serve several models and one model can back several
// pipelines (for example RAG and MCP).
package modelgateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"GopherAI/config"
)

const DefaultRequestTimeout = 90 * time.Second

// Capabilities describes behavior clients may rely on.  False fields are
// intentionally serialized so a UI does not have to infer missing values.
type Capabilities struct {
	Chat        bool `json:"chat"`
	Streaming   bool `json:"streaming"`
	ToolCalling bool `json:"toolCalling"`
	Retrieval   bool `json:"retrieval"`
	Local       bool `json:"local"`
}

// Provider is an adapter implementation, independent from credentials and a
// concrete upstream model.
type Provider struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Protocol    string `json:"protocol"`
	Implemented bool   `json:"implemented"`
}

// Model is an upstream model configured for one provider.  Name is useful to
// clients and is not a credential; endpoints and API keys are never cataloged.
type Model struct {
	ID                string `json:"id"`
	ProviderID        string `json:"provider"`
	Name              string `json:"name"`
	Configured        bool   `json:"configured"`
	UnavailableReason string `json:"-"`
}

// Pipeline adds application behavior to a model.  Selectors and legacy aliases
// are resolved by Registry, while callers should prefer ID for new requests.
type Pipeline struct {
	ID                string       `json:"id"`
	DisplayName       string       `json:"displayName"`
	ModelID           string       `json:"modelId"`
	Kind              string       `json:"pipeline"`
	Aliases           []string     `json:"aliases,omitempty"`
	Capabilities      Capabilities `json:"capabilities"`
	Enabled           bool         `json:"-"`
	UnavailableReason string       `json:"-"`
}

// CatalogModel is the public, joined view returned to API clients.
type CatalogModel struct {
	ID                string       `json:"id"`
	DisplayName       string       `json:"displayName"`
	Provider          string       `json:"provider"`
	ModelID           string       `json:"modelId"`
	Model             string       `json:"model"`
	Pipeline          string       `json:"pipeline"`
	Aliases           []string     `json:"aliases,omitempty"`
	Capabilities      Capabilities `json:"capabilities"`
	Available         bool         `json:"available"`
	Status            string       `json:"status"`
	UnavailableReason string       `json:"unavailableReason,omitempty"`
}

// ResolvedPipeline contains only non-secret routing metadata needed by the
// model factory.
type ResolvedPipeline struct {
	Pipeline  Pipeline
	Model     Model
	Provider  Provider
	Available bool
	Reason    string
}

var ErrSelectorNotFound = errors.New("model selector not found")

// Registry is safe for concurrent catalog reads and extension registration.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
	models    map[string]Model
	pipelines map[string]Pipeline
	aliases   map[string]string
	timeout   time.Duration
}

func NewRegistry(timeout time.Duration) *Registry {
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	return &Registry{
		providers: make(map[string]Provider),
		models:    make(map[string]Model),
		pipelines: make(map[string]Pipeline),
		aliases:   make(map[string]string),
		timeout:   timeout,
	}
}

func (r *Registry) Timeout() time.Duration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.timeout
}

func (r *Registry) RegisterProvider(provider Provider) error {
	provider.ID = normalizeID(provider.ID)
	if provider.ID == "" || strings.TrimSpace(provider.DisplayName) == "" || strings.TrimSpace(provider.Protocol) == "" {
		return errors.New("provider id, display name and protocol are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[provider.ID]; exists {
		return fmt.Errorf("provider %q is already registered", provider.ID)
	}
	r.providers[provider.ID] = provider
	return nil
}

func (r *Registry) RegisterModel(model Model) error {
	model.ID = normalizeID(model.ID)
	model.ProviderID = normalizeID(model.ProviderID)
	if model.ID == "" || model.ProviderID == "" || strings.TrimSpace(model.Name) == "" {
		return errors.New("model id, provider and name are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[model.ProviderID]; !exists {
		return fmt.Errorf("model %q references unknown provider %q", model.ID, model.ProviderID)
	}
	if _, exists := r.models[model.ID]; exists {
		return fmt.Errorf("model %q is already registered", model.ID)
	}
	r.models[model.ID] = model
	return nil
}

func (r *Registry) RegisterPipeline(pipeline Pipeline) error {
	pipeline.ID = normalizeID(pipeline.ID)
	pipeline.ModelID = normalizeID(pipeline.ModelID)
	if pipeline.ID == "" || pipeline.ModelID == "" || strings.TrimSpace(pipeline.DisplayName) == "" || strings.TrimSpace(pipeline.Kind) == "" {
		return errors.New("pipeline id, model, display name and kind are required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.models[pipeline.ModelID]; !exists {
		return fmt.Errorf("pipeline %q references unknown model %q", pipeline.ID, pipeline.ModelID)
	}
	if _, exists := r.pipelines[pipeline.ID]; exists {
		return fmt.Errorf("pipeline %q is already registered", pipeline.ID)
	}
	selectors := append([]string{pipeline.ID}, pipeline.Aliases...)
	for _, selector := range selectors {
		selector = normalizeID(selector)
		if selector == "" {
			return fmt.Errorf("pipeline %q contains an empty alias", pipeline.ID)
		}
		if owner, exists := r.aliases[selector]; exists {
			return fmt.Errorf("selector %q is already assigned to pipeline %q", selector, owner)
		}
	}
	pipeline.Aliases = normalizeAliases(pipeline.Aliases)
	r.pipelines[pipeline.ID] = pipeline
	r.aliases[pipeline.ID] = pipeline.ID
	for _, alias := range pipeline.Aliases {
		r.aliases[alias] = pipeline.ID
	}
	return nil
}

func (r *Registry) ResolvePipeline(selector string) (ResolvedPipeline, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	pipelineID, ok := r.aliases[normalizeID(selector)]
	if !ok {
		return ResolvedPipeline{}, fmt.Errorf("%w: %q", ErrSelectorNotFound, selector)
	}
	pipeline := r.pipelines[pipelineID]
	model := r.models[pipeline.ModelID]
	provider := r.providers[model.ProviderID]
	available, reason := availability(provider, model, pipeline)
	return ResolvedPipeline{
		Pipeline:  clonePipeline(pipeline),
		Model:     model,
		Provider:  provider,
		Available: available,
		Reason:    reason,
	}, nil
}

func (r *Registry) Providers() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	providers := make([]Provider, 0, len(r.providers))
	for _, provider := range r.providers {
		providers = append(providers, provider)
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].ID < providers[j].ID })
	return providers
}

func (r *Registry) Models() []CatalogModel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	models := make([]CatalogModel, 0, len(r.pipelines))
	for _, pipeline := range r.pipelines {
		model := r.models[pipeline.ModelID]
		provider := r.providers[model.ProviderID]
		available, reason := availability(provider, model, pipeline)
		status := "available"
		if !available {
			status = "unavailable"
		}
		models = append(models, CatalogModel{
			ID:                pipeline.ID,
			DisplayName:       pipeline.DisplayName,
			Provider:          provider.ID,
			ModelID:           model.ID,
			Model:             model.Name,
			Pipeline:          pipeline.Kind,
			Aliases:           append([]string(nil), pipeline.Aliases...),
			Capabilities:      pipeline.Capabilities,
			Available:         available,
			Status:            status,
			UnavailableReason: reason,
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}

func availability(provider Provider, model Model, pipeline Pipeline) (bool, string) {
	if !provider.Implemented {
		return false, "provider_not_implemented"
	}
	if !model.Configured {
		if model.UnavailableReason != "" {
			return false, model.UnavailableReason
		}
		return false, "missing_configuration"
	}
	if !pipeline.Enabled {
		if pipeline.UnavailableReason != "" {
			return false, pipeline.UnavailableReason
		}
		return false, "pipeline_disabled"
	}
	return true, ""
}

func clonePipeline(pipeline Pipeline) Pipeline {
	pipeline.Aliases = append([]string(nil), pipeline.Aliases...)
	return pipeline
}

func normalizeID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

func normalizeAliases(aliases []string) []string {
	result := make([]string, 0, len(aliases))
	seen := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		alias = normalizeID(alias)
		if _, exists := seen[alias]; exists {
			continue
		}
		seen[alias] = struct{}{}
		result = append(result, alias)
	}
	return result
}

type envLookup func(string) (string, bool)

var (
	defaultRegistry     *Registry
	defaultRegistryOnce sync.Once
)

func GetDefaultRegistry() *Registry {
	defaultRegistryOnce.Do(func() {
		defaultRegistry = newDefaultRegistry(config.GetConfig(), lookupEnvironment)
	})
	return defaultRegistry
}

func lookupEnvironment(name string) (string, bool) {
	// Kept behind a function for deterministic tests. Credential values are
	// checked only for presence and never retained in the public catalog.
	return os.LookupEnv(name)
}

func newDefaultRegistry(conf *config.Config, lookup envLookup) *Registry {
	timeout := durationFromEnvironment(lookup)
	registry := NewRegistry(timeout)
	mustRegisterProvider(registry, Provider{ID: "openai-compatible", DisplayName: "OpenAI Compatible", Protocol: "openai", Implemented: true})
	mustRegisterProvider(registry, Provider{ID: "ollama", DisplayName: "Ollama", Protocol: "ollama", Implemented: true})
	mustRegisterProvider(registry, Provider{ID: "ark", DisplayName: "Volcengine Ark / Doubao", Protocol: "responses", Implemented: true})

	openAIBaseURL := firstEnvironment(lookup, "OPENAI_BASE_URL")
	openAIModel := firstEnvironment(lookup, "OPENAI_MODEL_NAME")
	openAIConfigured := allPresent(openAIBaseURL, openAIModel, firstEnvironment(lookup, "OPENAI_API_KEY"))
	mustRegisterModel(registry, Model{
		ID:                "openai.default",
		ProviderID:        "openai-compatible",
		Name:              displayModelName(openAIModel, "OpenAI-compatible model"),
		Configured:        openAIConfigured,
		UnavailableReason: missingReason(openAIConfigured),
	})

	ollamaBaseURL, ollamaModel := "", ""
	ragBaseURL, ragModel := "", ""
	if conf != nil {
		ollamaBaseURL = conf.OllamaConfig.OllamaBaseURL
		ollamaModel = conf.OllamaConfig.OllamaModelName
		ragBaseURL = conf.RagModelConfig.RagBaseUrl
		ragModel = conf.RagModelConfig.RagChatModelName
	}
	ollamaConfigured := allPresent(ollamaBaseURL, ollamaModel)
	mustRegisterModel(registry, Model{
		ID:                "ollama.default",
		ProviderID:        "ollama",
		Name:              displayModelName(ollamaModel, "Ollama model"),
		Configured:        ollamaConfigured,
		UnavailableReason: missingReason(ollamaConfigured),
	})

	ragConfigured := allPresent(ragBaseURL, ragModel) && (isLoopbackURL(ragBaseURL) || anyEnvironment(lookup, "GOPHERAI_RAG_API_KEY", "OPENAI_API_KEY"))
	mustRegisterModel(registry, Model{
		ID:                "rag.default",
		ProviderID:        "openai-compatible",
		Name:              displayModelName(ragModel, "RAG chat model"),
		Configured:        ragConfigured,
		UnavailableReason: missingReason(ragConfigured),
	})

	arkBaseURL := firstEnvironment(lookup, "GOPHERAI_ARK_BASE_URL", "ARK_BASE_URL")
	arkModel := firstEnvironment(lookup, "GOPHERAI_ARK_MODEL", "ARK_MODEL_ID")
	arkConfigured := allPresent(arkBaseURL, arkModel, firstEnvironment(lookup, "GOPHERAI_ARK_API_KEY", "ARK_API_KEY"))
	mustRegisterModel(registry, Model{
		ID:                "ark.default",
		ProviderID:        "ark",
		Name:              displayModelName(arkModel, "Ark / Doubao model"),
		Configured:        arkConfigured,
		UnavailableReason: missingReason(arkConfigured),
	})

	mustRegisterPipeline(registry, Pipeline{
		ID: "openai", DisplayName: "OpenAI Compatible", ModelID: "openai.default", Kind: "chat", Aliases: []string{"1", "chat.openai"},
		Capabilities: Capabilities{Chat: true, Streaming: true, ToolCalling: true}, Enabled: true,
	})
	mustRegisterPipeline(registry, Pipeline{
		ID: "rag", DisplayName: "Knowledge Base RAG", ModelID: "rag.default", Kind: "rag", Aliases: []string{"2", "knowledge.rag"},
		Capabilities: Capabilities{Chat: true, Streaming: true, Retrieval: true}, Enabled: true,
	})
	mustRegisterPipeline(registry, Pipeline{
		ID: "mcp", DisplayName: "MCP Tools", ModelID: "rag.default", Kind: "mcp", Aliases: []string{"3", "tools.mcp"},
		Capabilities: Capabilities{Chat: true, Streaming: true, ToolCalling: true}, Enabled: true,
	})
	mustRegisterPipeline(registry, Pipeline{
		ID: "ollama", DisplayName: "Local Ollama", ModelID: "ollama.default", Kind: "chat", Aliases: []string{"4", "chat.ollama"},
		Capabilities: Capabilities{Chat: true, Streaming: true, ToolCalling: true, Local: true}, Enabled: true,
	})
	mustRegisterPipeline(registry, Pipeline{
		ID: "ark", DisplayName: "Ark / Doubao", ModelID: "ark.default", Kind: "chat", Aliases: []string{"doubao", "chat.ark"},
		Capabilities: Capabilities{Chat: true, Streaming: true}, Enabled: true,
	})
	return registry
}

func mustRegisterProvider(registry *Registry, provider Provider) {
	if err := registry.RegisterProvider(provider); err != nil {
		panic(err)
	}
}

func mustRegisterModel(registry *Registry, model Model) {
	if err := registry.RegisterModel(model); err != nil {
		panic(err)
	}
}

func mustRegisterPipeline(registry *Registry, pipeline Pipeline) {
	if err := registry.RegisterPipeline(pipeline); err != nil {
		panic(err)
	}
}

func firstEnvironment(lookup envLookup, names ...string) string {
	for _, name := range names {
		if value, ok := lookup(name); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func anyEnvironment(lookup envLookup, names ...string) bool {
	return firstEnvironment(lookup, names...) != ""
}

func allPresent(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func displayModelName(name, fallback string) string {
	if strings.TrimSpace(name) == "" {
		return fallback
	}
	return strings.TrimSpace(name)
}

func missingReason(configured bool) string {
	if configured {
		return ""
	}
	return "missing_configuration"
}

func durationFromEnvironment(lookup envLookup) time.Duration {
	raw := firstEnvironment(lookup, "GOPHERAI_MODEL_TIMEOUT_SECONDS")
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 1 || seconds > 600 {
		return DefaultRequestTimeout
	}
	return time.Duration(seconds) * time.Second
}

func isLoopbackURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
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

// WithTimeout applies the registry policy while preserving an earlier parent
// deadline through normal context derivation.
func WithTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	return context.WithTimeout(ctx, timeout)
}
