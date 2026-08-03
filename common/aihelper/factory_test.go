package aihelper

import (
	"GopherAI/common/modelgateway"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

type factoryPolicyModel struct{}

func (*factoryPolicyModel) GenerateResponse(ctx context.Context, _ []*schema.Message) (*schema.Message, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*factoryPolicyModel) StreamResponse(context.Context, []*schema.Message, StreamCallback) (string, error) {
	return "", nil
}

func (*factoryPolicyModel) GetModelType() string { return "internal" }

func TestOllamaFactoryRequiresConfiguration(t *testing.T) {
	factory := &AIModelFactory{creators: make(map[string]ModelCreator)}
	factory.registerCreators()

	tests := []struct {
		name      string
		config    map[string]interface{}
		wantInErr string
	}{
		{
			name:      "missing base URL",
			config:    map[string]interface{}{"modelName": "deepseek-r1:1.5b"},
			wantInErr: "baseURL",
		},
		{
			name:      "missing model name",
			config:    map[string]interface{}{"baseURL": "http://127.0.0.1:11434"},
			wantInErr: "modelName",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := factory.CreateAIModel(context.Background(), "4", tt.config)
			if err == nil || !strings.Contains(err.Error(), tt.wantInErr) {
				t.Fatalf("CreateAIModel() error = %v, want an error containing %q", err, tt.wantInErr)
			}
		})
	}

	aiModel, err := factory.CreateAIModel(context.Background(), "4", map[string]interface{}{
		"baseURL":   "http://127.0.0.1:11434",
		"modelName": "deepseek-r1:1.5b",
	})
	if err != nil {
		t.Fatalf("CreateAIModel() with valid Ollama config: %v", err)
	}
	if got := aiModel.GetModelType(); got != "4" {
		t.Fatalf("GetModelType() = %q, want 4", got)
	}
}

func TestFactoryResolvesCatalogAliasAndAppliesTimeout(t *testing.T) {
	registry := modelgateway.NewRegistry(10 * time.Millisecond)
	if err := registry.RegisterProvider(modelgateway.Provider{ID: "test", DisplayName: "Test", Protocol: "test", Implemented: true}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterModel(modelgateway.Model{ID: "test.model", ProviderID: "test", Name: "test-model", Configured: true}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterPipeline(modelgateway.Pipeline{ID: "test-chat", DisplayName: "Test Chat", ModelID: "test.model", Kind: "chat", Aliases: []string{"88"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	factory := &AIModelFactory{
		registry: registry,
		creators: map[string]ModelCreator{
			"test-chat": func(context.Context, map[string]interface{}) (AIModel, error) {
				return &factoryPolicyModel{}, nil
			},
		},
	}
	model, err := factory.CreateAIModel(context.Background(), "88", nil)
	if err != nil {
		t.Fatal(err)
	}
	if model.GetModelType() != "88" {
		t.Fatalf("GetModelType() = %q, want caller selector", model.GetModelType())
	}
	_, err = model.GenerateResponse(context.Background(), nil)
	var gatewayErr *modelgateway.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr.Kind != modelgateway.ErrorTimeout {
		t.Fatalf("GenerateResponse() error = %v, want timeout GatewayError", err)
	}
}
