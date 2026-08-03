package modelgateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"GopherAI/config"
)

func TestRegistrySeparatesProviderModelAndPipeline(t *testing.T) {
	registry := NewRegistry(3 * time.Second)
	if err := registry.RegisterProvider(Provider{ID: "provider", DisplayName: "Provider", Protocol: "test", Implemented: true}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterModel(Model{ID: "model", ProviderID: "provider", Name: "model-v1", Configured: true}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterPipeline(Pipeline{
		ID: "chat", DisplayName: "Chat", ModelID: "model", Kind: "chat", Aliases: []string{"9"},
		Capabilities: Capabilities{Chat: true, Streaming: true}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	resolved, err := registry.ResolvePipeline("9")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Pipeline.ID != "chat" || resolved.Model.ID != "model" || resolved.Provider.ID != "provider" || !resolved.Available {
		t.Fatalf("unexpected resolution: %+v", resolved)
	}
	models := registry.Models()
	if len(models) != 1 || models[0].Model != "model-v1" || !models[0].Capabilities.Streaming {
		t.Fatalf("unexpected catalog: %+v", models)
	}
}

func TestDefaultRegistryKeepsArkUnavailableWithoutCredentials(t *testing.T) {
	lookup := func(string) (string, bool) { return "", false }
	conf := &config.Config{}
	conf.OllamaConfig.OllamaBaseURL = "http://127.0.0.1:11434"
	conf.OllamaConfig.OllamaModelName = "local-model"
	conf.RagModelConfig.RagBaseUrl = "http://127.0.0.1:11434/v1"
	conf.RagModelConfig.RagChatModelName = "rag-model"
	registry := newDefaultRegistry(conf, lookup)

	ark, err := registry.ResolvePipeline("doubao")
	if err != nil {
		t.Fatal(err)
	}
	if ark.Available || ark.Reason != "missing_configuration" {
		t.Fatalf("Ark availability = %v, reason = %q", ark.Available, ark.Reason)
	}
	ollama, err := registry.ResolvePipeline("4")
	if err != nil || !ollama.Available {
		t.Fatalf("Ollama resolution = %+v, err = %v", ollama, err)
	}
	rag, err := registry.ResolvePipeline("2")
	if err != nil || !rag.Available {
		t.Fatalf("local RAG resolution = %+v, err = %v", rag, err)
	}
}

func TestDefaultRegistryArkConfiguredViaPrefixedVariables(t *testing.T) {
	values := map[string]string{
		"GOPHERAI_ARK_BASE_URL": "https://ark.example.test/v1",
		"GOPHERAI_ARK_MODEL":    "endpoint-id",
		"GOPHERAI_ARK_API_KEY":  "secret-not-cataloged",
	}
	registry := newDefaultRegistry(&config.Config{}, func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	})
	resolved, err := registry.ResolvePipeline("ark")
	if err != nil || !resolved.Available {
		t.Fatalf("Ark resolution = %+v, err = %v", resolved, err)
	}
	if !resolved.Pipeline.Capabilities.Streaming || resolved.Pipeline.Capabilities.ToolCalling {
		t.Fatalf("Ark capabilities = %+v", resolved.Pipeline.Capabilities)
	}
	for _, model := range registry.Models() {
		if model.ID == "ark" && model.Model != "endpoint-id" {
			t.Fatalf("Ark model = %q", model.Model)
		}
	}
}

func TestNormalizeErrorUsesStableKinds(t *testing.T) {
	tests := []struct {
		err  error
		kind ErrorKind
	}{
		{context.DeadlineExceeded, ErrorTimeout},
		{context.Canceled, ErrorCanceled},
		{errors.New("upstream returned status code: 401"), ErrorAuthentication},
		{errors.New("Error code: 401"), ErrorAuthentication},
		{errors.New("too many requests"), ErrorRateLimited},
		{errors.New("Error code: 429"), ErrorRateLimited},
		{errors.New("dial tcp: connection refused"), ErrorUnavailable},
		{errors.New("Error code: 503"), ErrorUnavailable},
		{errors.New("ModelNotOpen"), ErrorUnavailable},
		{errors.New("Error code: 400"), ErrorInvalidRequest},
	}
	for _, test := range tests {
		err := NormalizeError("provider", "generate", test.err)
		var gatewayErr *GatewayError
		if !errors.As(err, &gatewayErr) || gatewayErr.Kind != test.kind {
			t.Fatalf("NormalizeError(%v) = %v", test.err, err)
		}
	}
}

func TestWithTimeout(t *testing.T) {
	ctx, cancel := WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 30*time.Millisecond {
		t.Fatalf("unexpected deadline: %v, ok=%v", deadline, ok)
	}
}
