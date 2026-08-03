package aihelper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestArkResponsesModelGenerateUsesResponsesAPI(t *testing.T) {
	const apiKey = "unit-test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/responses" {
			t.Errorf("request = %s %s, want POST /responses", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Errorf("Authorization header was not set")
		}

		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if payload["model"] != "test-model" {
			t.Errorf("model = %#v", payload["model"])
		}
		if _, exists := payload["messages"]; exists {
			t.Errorf("Responses request unexpectedly contains Chat Completions messages")
		}
		input, ok := payload["input"].([]any)
		if !ok || len(input) != 2 {
			t.Errorf("input = %#v", payload["input"])
		} else {
			system, _ := input[0].(map[string]any)
			if system["role"] != "system" || system["content"] != "be concise" {
				t.Errorf("system input = %#v", system)
			}
			user, _ := input[1].(map[string]any)
			parts, _ := user["content"].([]any)
			if user["role"] != "user" || len(parts) != 2 {
				t.Errorf("multimodal user input = %#v", user)
			} else {
				image, _ := parts[0].(map[string]any)
				text, _ := parts[1].(map[string]any)
				if image["type"] != "input_image" || image["image_url"] != "https://example.test/image.png" {
					t.Errorf("image input = %#v", image)
				}
				if text["type"] != "input_text" || text["text"] != "describe it" {
					t.Errorf("text input = %#v", text)
				}
			}
		}

		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"id":"resp_test","object":"response","created_at":1,
			"model":"test-model","status":"completed",
			"output":[{"type":"message","role":"assistant","status":"completed","id":"msg_test",
				"content":[{"type":"output_text","text":"ARK_OK","annotations":[]}]}],
			"usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10}
		}`))
	}))
	defer server.Close()

	model, err := newArkResponsesModel(server.URL, "test-model", apiKey)
	if err != nil {
		t.Fatal(err)
	}
	imageURL := "https://example.test/image.png"
	response, err := model.GenerateResponse(context.Background(), []*schema.Message{
		{Role: schema.System, Content: "be concise"},
		{
			Role: schema.User,
			UserInputMultiContent: []schema.MessageInputPart{
				{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &imageURL}}},
				{Type: schema.ChatMessagePartTypeText, Text: "describe it"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Role != schema.Assistant || response.Content != "ARK_OK" {
		t.Fatalf("response = %#v", response)
	}
	if response.ResponseMeta == nil || response.ResponseMeta.FinishReason != "stop" || response.ResponseMeta.Usage == nil || response.ResponseMeta.Usage.TotalTokens != 10 {
		t.Fatalf("response meta = %#v", response.ResponseMeta)
	}
}

func TestArkResponsesModelStreamAggregatesDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if payload["stream"] != true {
			t.Errorf("stream = %#v, want true", payload["stream"])
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := writer.(http.Flusher)
		for _, event := range []string{
			`{"type":"response.output_text.delta","content_index":0,"delta":"ARK_","item_id":"msg_test","output_index":0,"sequence_number":1}`,
			`{"type":"response.output_text.delta","content_index":0,"delta":"OK","item_id":"msg_test","output_index":0,"sequence_number":2}`,
			`{"type":"response.output_text.done","content_index":0,"text":"ARK_OK","item_id":"msg_test","output_index":0,"sequence_number":3}`,
			`{"type":"response.completed","sequence_number":4,"response":{"id":"resp_test","object":"response","created_at":1,"model":"test-model","status":"completed","output":[]}}`,
		} {
			_, _ = fmt.Fprintf(writer, "data: %s\n\n", event)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	model, err := newArkResponsesModel(server.URL, "test-model", "unit-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	var callback strings.Builder
	response, err := model.StreamResponse(
		context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "test"}},
		func(delta string) { callback.WriteString(delta) },
	)
	if err != nil {
		t.Fatal(err)
	}
	if response != "ARK_OK" || callback.String() != "ARK_OK" {
		t.Fatalf("response = %q, callback = %q", response, callback.String())
	}
}

func TestArkResponsesModelStreamRejectsTruncatedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"content_index\":0,\"delta\":\"partial\",\"item_id\":\"msg_test\",\"output_index\":0,\"sequence_number\":1}\n\n")
	}))
	defer server.Close()

	model, err := newArkResponsesModel(server.URL, "test-model", "unit-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.StreamResponse(
		context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "test"}},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "before completion") {
		t.Fatalf("response = %q, error = %v", response, err)
	}
}

func TestArkResponsesModelErrorsDoNotLeakAPIKey(t *testing.T) {
	const apiKey = "do-not-leak-this-key"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":{"code":"Unauthorized","message":"invalid API key","type":"Unauthorized"}}`))
	}))
	defer server.Close()

	model, err := newArkResponsesModel(server.URL, "test-model", apiKey)
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.GenerateResponse(context.Background(), []*schema.Message{{Role: schema.User, Content: "test"}})
	if err == nil {
		t.Fatal("expected authentication error")
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("error leaked API key: %v", err)
	}
}

func TestArkResponsesModelRejectsToolMessages(t *testing.T) {
	model, err := newArkResponsesModel("https://ark.example.test/api/v3", "test-model", "unit-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.request([]*schema.Message{{Role: schema.Tool, Content: "result"}})
	if err == nil || !strings.Contains(err.Error(), "function_call_output") {
		t.Fatalf("tool message error = %v", err)
	}
}
