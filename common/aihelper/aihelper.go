package aihelper

import (
	"GopherAI/common/rabbitmq"
	"GopherAI/common/rag"
	"GopherAI/model"
	"GopherAI/utils"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// maxInMemoryHistoryMessages bounds each active model context. Complete chat
// history remains durable in MySQL and is queried separately by the history
// API; keeping only the tail here avoids unbounded process memory and keeps
// model prompts within a predictable context window.
const maxInMemoryHistoryMessages = 64

// AIHelper binds one model and an in-memory message history to a session.
type AIHelper struct {
	model     AIModel
	messages  []*model.Message
	mu        sync.RWMutex
	SessionID string
	saveFunc  func(*model.Message) (*model.Message, error)
	responses atomic.Int64
}

func NewAIHelper(aiModel AIModel, sessionID string) *AIHelper {
	return &AIHelper{
		model:     aiModel,
		messages:  make([]*model.Message, 0),
		SessionID: sessionID,
		saveFunc:  rabbitmq.PersistMessage,
	}
}

// AddMessage appends to the in-memory history and optionally persists it.
func (a *AIHelper) AddMessage(content, userName string, isUser, save bool) error {
	msg := &model.Message{
		SessionID: a.SessionID,
		Content:   content,
		UserName:  userName,
		IsUser:    isUser,
	}
	return a.addMessage(msg, save)
}

// RestoreMessage appends a complete persisted message without saving it
// again. Keeping the full value is important for citation history after a
// backend restart.
func (a *AIHelper) RestoreMessage(message *model.Message) error {
	if message == nil {
		return errors.New("restored message cannot be nil")
	}
	if message.SessionID != "" && message.SessionID != a.SessionID {
		return errors.New("restored message belongs to another session")
	}
	clone := *message
	clone.SessionID = a.SessionID
	return a.addMessage(&clone, false)
}

func (a *AIHelper) addMessage(msg *model.Message, save bool) error {
	a.mu.Lock()
	a.messages = append(a.messages, msg)
	if overflow := len(a.messages) - maxInMemoryHistoryMessages; overflow > 0 {
		trimmed := make([]*model.Message, maxInMemoryHistoryMessages)
		copy(trimmed, a.messages[overflow:])
		a.messages = trimmed
	}
	saveFunc := a.saveFunc
	a.mu.Unlock()

	if !save {
		return nil
	}
	if saveFunc == nil {
		return errors.New("message persistence function is not configured")
	}
	if _, err := saveFunc(msg); err != nil {
		return err
	}
	return nil
}

func (a *AIHelper) SetSaveFunc(saveFunc func(*model.Message) (*model.Message, error)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.saveFunc = saveFunc
}

func (a *AIHelper) GetMessages() []*model.Message {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]*model.Message, len(a.messages))
	copy(out, a.messages)
	return out
}

// SetModel replaces the model used by this session without discarding its
// in-memory messages or persistence configuration.
func (a *AIHelper) SetModel(aiModel AIModel) error {
	if aiModel == nil {
		return errors.New("AI model cannot be nil")
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = aiModel
	return nil
}

func (a *AIHelper) getModel() AIModel {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.model
}

func (a *AIHelper) GenerateResponse(userName string, ctx context.Context, userQuestion string) (*model.Message, error) {
	a.responses.Add(1)
	defer a.responses.Add(-1)

	if err := a.AddMessage(userQuestion, userName, true, true); err != nil {
		return nil, fmt.Errorf("persist user message: %w", err)
	}

	aiModel := a.getModel()
	if aiModel == nil {
		return nil, errors.New("AI model is not configured")
	}
	messages := utils.ConvertToSchemaMessages(a.GetMessages())
	schemaMsg, err := aiModel.GenerateResponse(ctx, messages)
	if err != nil {
		return nil, err
	}

	modelMsg := utils.ConvertToModelMessage(a.SessionID, userName, schemaMsg)
	if request := rag.ChatRequestFromContext(ctx); request != nil {
		if err := modelMsg.SetKnowledgeReferences(request.References()); err != nil {
			return nil, fmt.Errorf("encode assistant citations: %w", err)
		}
	}
	if err := a.addMessage(modelMsg, true); err != nil {
		return nil, fmt.Errorf("persist assistant message: %w", err)
	}
	return modelMsg, nil
}

func (a *AIHelper) StreamResponse(userName string, ctx context.Context, cb StreamCallback, userQuestion string) (*model.Message, error) {
	a.responses.Add(1)
	defer a.responses.Add(-1)

	if err := a.AddMessage(userQuestion, userName, true, true); err != nil {
		return nil, fmt.Errorf("persist user message: %w", err)
	}

	aiModel := a.getModel()
	if aiModel == nil {
		return nil, errors.New("AI model is not configured")
	}
	messages := utils.ConvertToSchemaMessages(a.GetMessages())
	content, err := aiModel.StreamResponse(ctx, messages, cb)
	if err != nil {
		return nil, err
	}

	modelMsg := &model.Message{
		SessionID: a.SessionID,
		UserName:  userName,
		Content:   content,
		IsUser:    false,
	}
	if request := rag.ChatRequestFromContext(ctx); request != nil {
		if err := modelMsg.SetKnowledgeReferences(request.References()); err != nil {
			return nil, fmt.Errorf("encode assistant citations: %w", err)
		}
	}
	if err := a.addMessage(modelMsg, true); err != nil {
		return nil, fmt.Errorf("persist assistant message: %w", err)
	}
	return modelMsg, nil
}

func (a *AIHelper) GetModelType() string {
	aiModel := a.getModel()
	if aiModel == nil {
		return ""
	}
	return aiModel.GetModelType()
}

// isHandlingResponse reports whether a model response is in progress. It is
// deliberately internal to the manager package: callers retain the existing
// helper API while idle eviction can avoid removing a helper mid-request.
func (a *AIHelper) isHandlingResponse() bool {
	return a.responses.Load() > 0
}
