package aihelper

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"GopherAI/common/modelgateway"
)

// ModelCreator creates an application model for a registered pipeline.
type ModelCreator func(ctx context.Context, config map[string]interface{}) (AIModel, error)

// AIModelFactory bridges the dynamic model catalog to the existing AIModel
// interface.  creators are keyed by canonical pipeline ID; legacy numeric
// modelType values are resolved by modelgateway.Registry.
type AIModelFactory struct {
	creators map[string]ModelCreator
	registry *modelgateway.Registry
	mu       sync.RWMutex
}

var (
	globalFactory *AIModelFactory
	factoryOnce   sync.Once
)

func GetGlobalFactory() *AIModelFactory {
	factoryOnce.Do(func() {
		globalFactory = &AIModelFactory{
			creators: make(map[string]ModelCreator),
			registry: modelgateway.GetDefaultRegistry(),
		}
		globalFactory.registerCreators()
	})
	return globalFactory
}

func (f *AIModelFactory) modelRegistry() *modelgateway.Registry {
	f.mu.RLock()
	registry := f.registry
	f.mu.RUnlock()
	return registry
}

func (f *AIModelFactory) registerCreators() {
	// OpenAI-compatible provider.
	f.creators["openai"] = func(ctx context.Context, _ map[string]interface{}) (AIModel, error) {
		return NewOpenAIModel(ctx)
	}

	// Knowledge-base RAG pipeline.
	f.creators["rag"] = func(ctx context.Context, config map[string]interface{}) (AIModel, error) {
		username, ok := config["username"].(string)
		if !ok || strings.TrimSpace(username) == "" {
			return nil, errors.New("RAG model requires username")
		}
		return NewAliRAGModel(ctx, username)
	}

	// MCP tool pipeline.
	f.creators["mcp"] = func(ctx context.Context, config map[string]interface{}) (AIModel, error) {
		username, ok := config["username"].(string)
		if !ok || strings.TrimSpace(username) == "" {
			return nil, errors.New("MCP model requires username")
		}
		return NewMCPModel(ctx, username)
	}

	// Local Ollama provider.
	f.creators["ollama"] = func(ctx context.Context, config map[string]interface{}) (AIModel, error) {
		baseURL, ok := config["baseURL"].(string)
		if !ok || strings.TrimSpace(baseURL) == "" {
			return nil, errors.New("Ollama model requires baseURL")
		}
		modelName, ok := config["modelName"].(string)
		if !ok || strings.TrimSpace(modelName) == "" {
			return nil, errors.New("Ollama model requires modelName")
		}
		return NewOllamaModel(ctx, baseURL, modelName)
	}

	// Ark/Doubao uses Ark's Responses API through the official runtime SDK.
	f.creators["ark"] = func(ctx context.Context, _ map[string]interface{}) (AIModel, error) {
		return NewArkModel(ctx)
	}

	// Constructor aliases keep explicitly instantiated factories backward
	// compatible.  The global factory still resolves these through Registry and
	// therefore enforces catalog availability.
	f.creators["1"] = f.creators["openai"]
	f.creators["2"] = f.creators["rag"]
	f.creators["3"] = f.creators["mcp"]
	f.creators["4"] = f.creators["ollama"]
	f.creators["doubao"] = f.creators["ark"]
}

// CreateAIModel resolves a canonical or legacy selector, rejects unavailable
// configurations before making a request, and applies the shared timeout and
// error-normalization policy.  Directly registered custom creators remain
// supported for extensions and tests.
func (f *AIModelFactory) CreateAIModel(ctx context.Context, modelType string, config map[string]interface{}) (AIModel, error) {
	registry := f.modelRegistry()
	canonicalID := strings.TrimSpace(modelType)
	providerID := "custom"
	timeout := modelgateway.DefaultRequestTimeout
	if registry != nil {
		timeout = registry.Timeout()
		resolved, resolveErr := registry.ResolvePipeline(modelType)
		if resolveErr == nil {
			canonicalID = resolved.Pipeline.ID
			providerID = resolved.Provider.ID
			if !resolved.Available {
				return nil, modelgateway.NewUnavailableError(providerID, "create", fmt.Errorf("pipeline %s: %s", canonicalID, resolved.Reason))
			}
		} else if !errors.Is(resolveErr, modelgateway.ErrSelectorNotFound) {
			return nil, modelgateway.NormalizeError(providerID, "resolve", resolveErr)
		}
	}

	f.mu.RLock()
	creator, ok := f.creators[canonicalID]
	f.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unsupported model type: %s", modelType)
	}
	model, err := creator(ctx, config)
	if err != nil {
		if registry == nil {
			return nil, err
		}
		return nil, modelgateway.NormalizeError(providerID, "create", err)
	}
	return newGatewayModel(model, strings.TrimSpace(modelType), providerID, timeout), nil
}

func (f *AIModelFactory) CreateAIHelper(ctx context.Context, modelType string, sessionID string, config map[string]interface{}) (*AIHelper, error) {
	model, err := f.CreateAIModel(ctx, modelType, config)
	if err != nil {
		return nil, err
	}
	return NewAIHelper(model, sessionID), nil
}

// RegisterModel preserves the existing extension point.  A catalog-visible
// model should additionally register its provider/model/pipeline metadata on
// modelgateway.GetDefaultRegistry().
func (f *AIModelFactory) RegisterModel(modelType string, creator ModelCreator) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creators[modelType] = creator
}
