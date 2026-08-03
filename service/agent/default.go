package agent

import (
	"context"
	"fmt"
	"sync"

	"GopherAI/common/aihelper"
	hub "GopherAI/common/mcphub"
	"GopherAI/config"
	agentdao "GopherAI/dao/agent"
	mcphubservice "GopherAI/service/mcphub"

	"github.com/cloudwego/eino/schema"
)

// factoryRuntime is the production model boundary. It deliberately uses the
// same catalog-aware factory as chat, so Ark's Responses adapter and every
// other available chat pipeline follow one timeout/error policy.
type factoryRuntime struct {
	factory *aihelper.AIModelFactory
}

func (runtime *factoryRuntime) Generate(ctx context.Context, selector, userName string, messages []*schema.Message) (*schema.Message, error) {
	if runtime == nil || runtime.factory == nil {
		return nil, fmt.Errorf("Agent model factory is unavailable")
	}
	configuration := config.GetConfig()
	modelConfig := map[string]interface{}{
		"username": userName,
	}
	if configuration != nil {
		// These values are required only by the Ollama creator and are ignored
		// by OpenAI-compatible and Ark creators.
		modelConfig["baseURL"] = configuration.OllamaBaseURL
		modelConfig["modelName"] = configuration.OllamaModelName
	}
	chatModel, err := runtime.factory.CreateAIModel(ctx, selector, modelConfig)
	if err != nil {
		return nil, err
	}
	return chatModel.GenerateResponse(ctx, messages)
}

type mcpServiceGateway struct {
	service *mcphubservice.Service
}

func (gateway *mcpServiceGateway) Tools(ctx context.Context) ([]hub.ToolDefinition, error) {
	return gateway.service.Tools(ctx, false)
}

func (gateway *mcpServiceGateway) Call(ctx context.Context, request hub.InvokeRequest) (*hub.InvocationResult, error) {
	return gateway.service.Call(ctx, request)
}

func (gateway *mcpServiceGateway) CreateApproval(ctx context.Context, userName, toolName string, arguments map[string]any) (*hub.ApprovalChallenge, error) {
	return gateway.service.CreateApproval(ctx, userName, toolName, arguments)
}

func (gateway *mcpServiceGateway) Approve(userName, challengeID string) (*hub.IssuedApproval, error) {
	return gateway.service.Approve(userName, challengeID)
}

var (
	defaultService     *Service
	defaultServiceErr  error
	defaultServiceOnce sync.Once

	defaultWorkerOnce sync.Once
	defaultWorkerErr  error
)

// Default wires the durable MySQL/checkpoint store, catalog-aware model
// factory and the policy-enforcing MCP Hub service.
func Default() (*Service, error) {
	defaultServiceOnce.Do(func() {
		store, err := agentdao.DefaultStore()
		if err != nil {
			defaultServiceErr = err
			return
		}
		mcpService, err := mcphubservice.Default()
		if err != nil {
			defaultServiceErr = err
			return
		}
		defaultService, defaultServiceErr = NewService(
			store,
			&factoryRuntime{factory: aihelper.GetGlobalFactory()},
			&mcpServiceGateway{service: mcpService},
			realClock{},
		)
	})
	return defaultService, defaultServiceErr
}

// StartWorker starts the process-wide Agent worker. The worker uses durable
// polling, so a lost in-memory hint never loses a task.
func StartWorker(contexts ...context.Context) error {
	defaultWorkerOnce.Do(func() {
		service, err := Default()
		if err != nil {
			defaultWorkerErr = err
			return
		}
		workerCtx := context.Background()
		if len(contexts) > 0 && contexts[0] != nil {
			workerCtx = contexts[0]
		}
		defaultWorkerErr = service.StartWorker(workerCtx)
	})
	return defaultWorkerErr
}

func WaitWorker(ctx context.Context) error {
	service, err := Default()
	if err != nil {
		return err
	}
	return service.WaitWorker(ctx)
}

func WorkerRunning() bool {
	service, err := Default()
	return err == nil && service.WorkerRunning()
}
