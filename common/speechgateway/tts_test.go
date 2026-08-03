package speechgateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"GopherAI/common/fishaudio"
	"GopherAI/common/voicecontrol"
)

func TestFishS2ProviderRendersWhitelistedPlan(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization = %q", got)
		}
		body, _ := io.ReadAll(request.Body)
		if !strings.Contains(string(body), "[warm]") || strings.Contains(string(body), "[malicious]") {
			t.Fatalf("unexpected rendered Fish request: %s", body)
		}
		writer.Header().Set("Content-Type", "audio/mpeg")
		_, _ = writer.Write([]byte("audio"))
	}))
	defer server.Close()

	client, err := fishaudio.NewClient(fishaudio.Config{
		APIKey:      "test-key",
		BaseURL:     server.URL,
		ReferenceID: "authorized-voice",
	})
	if err != nil {
		t.Fatalf("NewClient(): %v", err)
	}
	provider, err := NewFishS2Provider("fish-s2", client)
	if err != nil {
		t.Fatalf("NewFishS2Provider(): %v", err)
	}
	audio, err := provider.Synthesize(context.Background(), voicecontrol.ExpressionPlan{
		Text:    "[malicious] 你好。",
		Emotion: voicecontrol.EmotionWarm,
	})
	if err != nil {
		t.Fatalf("Synthesize(): %v", err)
	}
	if got, want := string(audio.Bytes), "audio"; got != want {
		t.Fatalf("audio = %q, want %q", got, want)
	}
	if got, want := audio.ProviderID, "fish-s2"; got != want {
		t.Fatalf("provider ID = %q, want %q", got, want)
	}
}

func TestRegistryRejectsDuplicateAndNormalizesID(t *testing.T) {
	registry := NewRegistry()
	provider := &fakeProvider{id: "Fish-S2"}
	if err := registry.Register(provider); err != nil {
		t.Fatalf("Register(): %v", err)
	}
	if got, ok := registry.Get(" fish-s2 "); !ok || got != provider {
		t.Fatalf("Get() = %#v, %v", got, ok)
	}
	if err := registry.Register(provider); err == nil {
		t.Fatal("duplicate Register() error = nil")
	}
}

type fakeProvider struct{ id string }

func (f *fakeProvider) ID() string { return f.id }
func (f *fakeProvider) Synthesize(context.Context, voicecontrol.ExpressionPlan) (Audio, error) {
	return Audio{}, nil
}
