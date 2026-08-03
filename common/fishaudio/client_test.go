package fishaudio

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientSynthesizeBuildsFishS2Request(t *testing.T) {
	t.Parallel()

	apiKey := "fish-test-key"
	var received providerRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", request.Method)
		}
		if request.URL.Path != "/v1/tts" {
			t.Errorf("path = %s, want /v1/tts", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Errorf("Authorization = %q", got)
		}
		if got := request.Header.Get("model"); got != "s2-pro" {
			t.Errorf("model = %q, want s2-pro", got)
		}
		if got := request.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "audio/ogg; charset=binary")
		_, _ = writer.Write([]byte("opus-audio"))
	}))
	defer server.Close()

	client, err := NewClient(Config{
		APIKey:      apiKey,
		BaseURL:     server.URL,
		Model:       "s2-pro",
		ReferenceID: "voice-model-id",
		HTTPClient:  server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	response, err := client.Synthesize(context.Background(), Request{
		Text: "[warm] 你好，欢迎回来。",
		Options: Options{
			Speed:       Float64(1.15),
			Volume:      Float64(-2),
			Temperature: Float64(0.8),
			TopP:        Float64(0.9),
			Format:      "OpUs",
			Latency:     "low",
		},
	})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if got, want := string(response.Audio), "opus-audio"; got != want {
		t.Errorf("audio = %q, want %q", got, want)
	}
	if got, want := response.ContentType, "audio/ogg"; got != want {
		t.Errorf("ContentType = %q, want %q", got, want)
	}
	if got, want := response.Format, "opus"; got != want {
		t.Errorf("Format = %q, want %q", got, want)
	}

	if got, want := received.Text, "[warm] 你好，欢迎回来。"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	if got, want := received.ReferenceID, "voice-model-id"; got != want {
		t.Errorf("reference_id = %q, want %q", got, want)
	}
	if got, want := received.Temperature, 0.8; got != want {
		t.Errorf("temperature = %v, want %v", got, want)
	}
	if got, want := received.TopP, 0.9; got != want {
		t.Errorf("top_p = %v, want %v", got, want)
	}
	if got, want := received.Prosody.Speed, 1.15; got != want {
		t.Errorf("prosody.speed = %v, want %v", got, want)
	}
	if got, want := received.Prosody.Volume, -2.0; got != want {
		t.Errorf("prosody.volume = %v, want %v", got, want)
	}
	if got, want := received.Format, "opus"; got != want {
		t.Errorf("format = %q, want %q", got, want)
	}
	if got, want := received.Latency, "low"; got != want {
		t.Errorf("latency = %q, want %q", got, want)
	}
}

func TestClientSynthesizeDoesNotExposeAPIKeyOrProviderBody(t *testing.T) {
	t.Parallel()

	const apiKey = "secret-fish-api-key"
	const providerBody = "upstream diagnostic secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(providerBody))
	}))
	defer server.Close()

	client, err := NewClient(Config{
		APIKey:      apiKey,
		BaseURL:     server.URL,
		ReferenceID: "voice-model-id",
		HTTPClient:  server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.Synthesize(context.Background(), Request{Text: "hello"})
	if err == nil {
		t.Fatal("Synthesize() error = nil, want error")
	}
	var httpError *HTTPError
	if !errors.As(err, &httpError) || httpError.StatusCode != http.StatusUnauthorized {
		t.Errorf("error = %v, want HTTPError(401)", err)
	}
	if strings.Contains(err.Error(), apiKey) || strings.Contains(err.Error(), providerBody) {
		t.Errorf("error leaked sensitive data: %q", err)
	}
}

func TestClientSynthesizeRejectsOversizedResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("this audio is deliberately too large"))
	}))
	defer server.Close()

	client, err := NewClient(Config{
		APIKey:           "secret-fish-api-key",
		BaseURL:          server.URL,
		ReferenceID:      "voice-model-id",
		HTTPClient:       server.Client(),
		MaxResponseBytes: 8,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.Synthesize(context.Background(), Request{Text: "hello"})
	if err == nil || !strings.Contains(err.Error(), "exceeded 8 byte limit") {
		t.Errorf("Synthesize() error = %v, want size-limit error", err)
	}
	if err != nil && strings.Contains(err.Error(), "secret-fish-api-key") {
		t.Errorf("error leaked API key: %q", err)
	}
}

func TestClientValidatesConfigurationAndOptions(t *testing.T) {
	t.Parallel()

	if _, err := NewClient(Config{ReferenceID: "voice-model-id"}); err == nil {
		t.Error("NewClient() accepted a missing API key")
	}
	if _, err := NewClient(Config{APIKey: "key", BaseURL: "https://example.test/?token=secret", ReferenceID: "voice-model-id"}); err == nil {
		t.Error("NewClient() accepted a base URL query")
	}

	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	client, err := NewClient(Config{
		APIKey:      "key",
		BaseURL:     server.URL,
		ReferenceID: "voice-model-id",
		HTTPClient:  server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	testCases := []struct {
		name    string
		request Request
	}{
		{name: "empty text", request: Request{Text: " \t "}},
		{name: "speed", request: Request{Text: "hello", Options: Options{Speed: Float64(2.1)}}},
		{name: "volume", request: Request{Text: "hello", Options: Options{Volume: Float64(21)}}},
		{name: "temperature", request: Request{Text: "hello", Options: Options{Temperature: Float64(1.1)}}},
		{name: "top p", request: Request{Text: "hello", Options: Options{TopP: Float64(-0.1)}}},
		{name: "format", request: Request{Text: "hello", Options: Options{Format: "flac"}}},
		{name: "latency", request: Request{Text: "hello", Options: Options{Latency: "fastest"}}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := client.Synthesize(context.Background(), testCase.request); err == nil {
				t.Error("Synthesize() error = nil, want validation error")
			}
		})
	}
}
