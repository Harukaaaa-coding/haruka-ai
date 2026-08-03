package aihelper

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/volcengine/volcengine-go-sdk/service/arkruntime"
	"github.com/volcengine/volcengine-go-sdk/service/arkruntime/model/responses"
)

// ArkResponsesModel adapts Volcengine Ark's Responses API to GopherAI's
// provider-neutral AIModel interface. Ark's current prebuilt model IDs use
// /responses; treating them as Chat Completions can incorrectly return
// ModelNotOpen even when the model is activated.
type ArkResponsesModel struct {
	client    *arkruntime.Client
	modelName string
}

func newArkResponsesModel(baseURL, modelName, key string) (*ArkResponsesModel, error) {
	baseURL = strings.TrimSpace(baseURL)
	modelName = strings.TrimSpace(modelName)
	key = strings.TrimSpace(key)
	if baseURL == "" || modelName == "" || key == "" {
		return nil, fmt.Errorf("ark model configuration is incomplete")
	}

	return &ArkResponsesModel{
		client: arkruntime.NewClientWithApiKey(
			key,
			arkruntime.WithBaseUrl(baseURL),
		),
		modelName: modelName,
	}, nil
}

func (model *ArkResponsesModel) GenerateResponse(ctx context.Context, messages []*schema.Message) (*schema.Message, error) {
	request, err := model.request(messages)
	if err != nil {
		return nil, err
	}
	response, err := model.client.CreateResponses(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("ark responses generate failed: %w", err)
	}
	content, err := arkResponseText(response)
	if err != nil {
		return nil, err
	}

	message := &schema.Message{Role: schema.Assistant, Content: content}
	if usage := response.GetUsage(); usage != nil {
		message.ResponseMeta = &schema.ResponseMeta{
			FinishReason: arkFinishReason(response),
			Usage: &schema.TokenUsage{
				PromptTokens:     int(usage.GetInputTokens()),
				CompletionTokens: int(usage.GetOutputTokens()),
				TotalTokens:      int(usage.GetTotalTokens()),
			},
		}
	}
	return message, nil
}

func (model *ArkResponsesModel) StreamResponse(ctx context.Context, messages []*schema.Message, cb StreamCallback) (string, error) {
	request, err := model.request(messages)
	if err != nil {
		return "", err
	}
	stream, err := model.client.CreateResponsesStream(ctx, request)
	if err != nil {
		return "", fmt.Errorf("ark responses stream failed: %w", err)
	}
	defer stream.Close()

	var content strings.Builder
	completed := false
	for {
		event, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return "", fmt.Errorf("ark responses stream recv failed: %w", recvErr)
		}

		switch event.GetEventType() {
		case responses.EventType_response_output_text_delta.String():
			delta := event.GetText().GetDelta()
			if delta != "" {
				content.WriteString(delta)
				if cb != nil {
					cb(delta)
				}
			}
		case responses.EventType_response_output_text_done.String():
			// Ark normally emits deltas followed by the aggregated done event.
			// Use the done text only when an upstream sent no deltas, avoiding
			// duplicate output in the frontend.
			if content.Len() == 0 {
				text := event.GetText().GetText()
				if text != "" {
					content.WriteString(text)
					if cb != nil {
						cb(text)
					}
				}
			}
		case responses.EventType_response_completed.String():
			responseEvent := event.GetResponse()
			if responseEvent == nil || responseEvent.GetResponse() == nil {
				return "", fmt.Errorf("ark responses stream completed without response details")
			}
			responseObject := responseEvent.GetResponse()
			if responseObject.GetStatus() != responses.ResponseStatus_completed {
				return "", arkResponseFailure(responseObject)
			}
			completed = true
		case responses.EventType_response_failed.String(), responses.EventType_response_incomplete.String():
			responseEvent := event.GetResponse()
			if responseEvent == nil {
				return "", fmt.Errorf("ark responses stream ended with %s", event.GetEventType())
			}
			return "", arkResponseFailure(responseEvent.GetResponse())
		case responses.EventType_error.String():
			streamError := event.GetError()
			if streamError == nil {
				return "", fmt.Errorf("ark responses stream returned an error event")
			}
			return "", fmt.Errorf("ark responses stream error: %s", streamError.GetMessage())
		}
	}

	if !completed {
		return "", fmt.Errorf("ark responses stream ended before completion")
	}
	if content.Len() == 0 {
		return "", fmt.Errorf("ark responses stream returned no text")
	}
	return content.String(), nil
}

func (*ArkResponsesModel) GetModelType() string { return "ark" }

func (model *ArkResponsesModel) request(messages []*schema.Message) (*responses.ResponsesRequest, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("ark responses requires at least one message")
	}

	items := make([]*responses.InputItem, 0, len(messages))
	for index, message := range messages {
		if message == nil {
			return nil, fmt.Errorf("ark responses message %d is nil", index)
		}
		item, err := arkInputItem(message)
		if err != nil {
			return nil, fmt.Errorf("ark responses message %d: %w", index, err)
		}
		items = append(items, item)
	}

	return &responses.ResponsesRequest{
		Model: model.modelName,
		Input: &responses.ResponsesInput{
			Union: &responses.ResponsesInput_ListValue{
				ListValue: &responses.InputItemList{ListValue: items},
			},
		},
	}, nil
}

func arkInputItem(message *schema.Message) (*responses.InputItem, error) {
	role, err := arkMessageRole(message.Role)
	if err != nil {
		return nil, err
	}

	if len(message.UserInputMultiContent) > 0 {
		if message.Role != schema.User {
			return nil, fmt.Errorf("multimodal input is only supported for user messages")
		}
		content, contentErr := arkInputContent(message.UserInputMultiContent)
		if contentErr != nil {
			return nil, contentErr
		}
		return &responses.InputItem{
			Union: &responses.InputItem_InputMessage{
				InputMessage: &responses.ItemInputMessage{
					Role:    role,
					Content: content,
				},
			},
		}, nil
	}

	content := message.Content
	if content == "" && len(message.AssistantGenMultiContent) > 0 {
		var text strings.Builder
		for _, part := range message.AssistantGenMultiContent {
			if part.Type == schema.ChatMessagePartTypeText {
				text.WriteString(part.Text)
			}
		}
		content = text.String()
	}
	return &responses.InputItem{
		Union: &responses.InputItem_EasyMessage{
			EasyMessage: &responses.ItemEasyMessage{
				Role: role,
				Content: &responses.MessageContent{
					Union: &responses.MessageContent_StringValue{StringValue: content},
				},
			},
		},
	}, nil
}

func arkInputContent(parts []schema.MessageInputPart) ([]*responses.ContentItem, error) {
	content := make([]*responses.ContentItem, 0, len(parts))
	for index, part := range parts {
		switch part.Type {
		case schema.ChatMessagePartTypeText:
			content = append(content, &responses.ContentItem{
				Union: &responses.ContentItem_Text{
					Text: &responses.ContentItemText{
						Type: responses.ContentItemType_input_text,
						Text: part.Text,
					},
				},
			})
		case schema.ChatMessagePartTypeImageURL:
			if part.Image == nil || part.Image.URL == nil || strings.TrimSpace(*part.Image.URL) == "" {
				return nil, fmt.Errorf("multimodal part %d has no image URL", index)
			}
			detail := responses.ContentItemImageDetail_auto
			switch part.Image.Detail {
			case schema.ImageURLDetailHigh:
				detail = responses.ContentItemImageDetail_high
			case schema.ImageURLDetailLow:
				detail = responses.ContentItemImageDetail_low
			}
			content = append(content, &responses.ContentItem{
				Union: &responses.ContentItem_Image{
					Image: &responses.ContentItemImage{
						Type:     responses.ContentItemType_input_image,
						Detail:   detail.Enum(),
						ImageUrl: strings.TrimSpace(*part.Image.URL),
					},
				},
			})
		default:
			return nil, fmt.Errorf("multimodal part %d has unsupported type %q", index, part.Type)
		}
	}
	if len(content) == 0 {
		return nil, fmt.Errorf("multimodal message has no supported content")
	}
	return content, nil
}

func arkMessageRole(role schema.RoleType) (responses.MessageRole_Enum, error) {
	switch role {
	case schema.User:
		return responses.MessageRole_user, nil
	case schema.System:
		return responses.MessageRole_system, nil
	case schema.Assistant:
		return responses.MessageRole_assistant, nil
	case schema.Tool:
		return responses.MessageRole_unspecified, fmt.Errorf("tool messages require a Responses function_call_output and are not supported by the chat adapter")
	default:
		return responses.MessageRole_unspecified, fmt.Errorf("unsupported role %q", role)
	}
}

func arkResponseText(response *responses.ResponseObject) (string, error) {
	if response == nil {
		return "", fmt.Errorf("ark responses returned an empty response")
	}
	if response.GetStatus() == responses.ResponseStatus_failed || response.GetStatus() == responses.ResponseStatus_incomplete {
		return "", arkResponseFailure(response)
	}

	var content strings.Builder
	for _, output := range response.GetOutput() {
		message := output.GetOutputMessage()
		if message == nil {
			continue
		}
		for _, part := range message.GetContent() {
			if text := part.GetText(); text != nil {
				content.WriteString(text.GetText())
			}
		}
	}
	if content.Len() == 0 {
		return "", fmt.Errorf("ark responses returned no text")
	}
	return content.String(), nil
}

func arkResponseFailure(response *responses.ResponseObject) error {
	if response == nil {
		return fmt.Errorf("ark responses failed without response details")
	}
	if responseError := response.GetError(); responseError != nil {
		return fmt.Errorf("ark responses failed (%s): %s", responseError.GetCode(), responseError.GetMessage())
	}
	if details := response.GetIncompleteDetails(); details != nil && details.GetReason() != "" {
		return fmt.Errorf("ark responses incomplete: %s", details.GetReason())
	}
	return fmt.Errorf("ark responses ended with status %s", response.GetStatus().String())
}

func arkFinishReason(response *responses.ResponseObject) string {
	if response.GetStatus() == responses.ResponseStatus_completed {
		return "stop"
	}
	return response.GetStatus().String()
}
