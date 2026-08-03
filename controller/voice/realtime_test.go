package voice

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"GopherAI/common/asr"
	"GopherAI/common/code"
	"GopherAI/common/sessionauth"
	"GopherAI/common/speechgateway"
	"GopherAI/common/voicecontrol"
	"GopherAI/config"
	"GopherAI/middleware/jwt"
	sessionservice "GopherAI/service/session"
	"GopherAI/service/voiceconversation"
	"GopherAI/utils/myjwt"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func configureVoiceJWT(t *testing.T) {
	t.Helper()
	configPath, err := filepath.Abs("../../config/config.toml")
	if err != nil {
		t.Fatalf("config path: %v", err)
	}
	t.Setenv("GOPHERAI_CONFIG_PATH", configPath)
	t.Setenv("GOPHERAI_ENV", "development")
	if err := config.InitConfig(); err != nil {
		t.Fatalf("InitConfig(): %v", err)
	}
}

func TestRealtimeVoiceTurnCoordinatesASRChatAndTTS(t *testing.T) {
	configureVoiceJWT(t)
	token, err := myjwt.GenerateToken(101, "voice-user")
	if err != nil {
		t.Fatalf("GenerateToken(): %v", err)
	}

	provider := &testTTSProvider{}
	registry := speechgateway.NewRegistry()
	if err := registry.Register(provider); err != nil {
		t.Fatalf("Register(): %v", err)
	}
	handler := NewHandler(voiceconversation.NewHub(4, 1))
	handler.Recognize = func(_ context.Context, request asr.RecognitionRequest) (string, error) {
		if request.Format != "wav" || len(request.Audio) < 44 || string(request.Audio[:4]) != "RIFF" {
			t.Fatalf("unexpected ASR request: %#v", request)
		}
		return "用户语音", nil
	}
	handler.CreateSession = func(_ context.Context, userName, question string) (string, code.Code) {
		if userName != "voice-user" || question != "用户语音" {
			t.Fatalf("CreateSession(%q, %q)", userName, question)
		}
		return "voice-session", code.CodeSuccess
	}
	handler.StreamSession = func(ctx context.Context, userName, sessionID, question string, options sessionservice.ChatOptions, sink sessionservice.StreamSink) code.Code {
		if userName != "voice-user" || sessionID != "voice-session" || question != "用户语音" || options.ModelType != "4" {
			t.Fatalf("unexpected chat stream args: user=%q session=%q question=%q options=%#v", userName, sessionID, question, options)
		}
		if err := sink.TextDelta(ctx, "[ignored] 这是回答。"); err != nil {
			return code.AIModelFail
		}
		if err := sink.Complete(ctx); err != nil {
			return code.AIModelFail
		}
		return code.CodeSuccess
	}
	handler.ProviderRegistry = func() (*speechgateway.Registry, error) { return registry, nil }

	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api/v1/AI")
	group.Use(jwt.Auth())
	group.GET("/voice/realtime", handler.Serve)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/AI/voice/realtime"
	headers := http.Header{}
	headers.Set("Origin", server.URL)
	headers.Set("Cookie", fmt.Sprintf("%s=%s; %s=%s", sessionauth.SessionCookieName, token, sessionauth.CSRFCookieName, "csrf-value"))
	connection, response, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		if response != nil {
			t.Fatalf("Dial(): %v, response=%s", err, response.Status)
		}
		t.Fatalf("Dial(): %v", err)
	}
	defer connection.Close()

	start := map[string]interface{}{
		"type":           "start",
		"turnId":         "turn-1",
		"modelId":        "4",
		"voiceProfileId": "fish-s2-warm",
		"csrfToken":      "csrf-value",
		"audioFormat":    "pcm16",
		"sampleRate":     16000,
		"channels":       1,
	}
	if err := connection.WriteJSON(start); err != nil {
		t.Fatalf("Write start: %v", err)
	}
	if event := readJSONEvent(t, connection); event["type"] != "ready" || event["ttsAvailable"] != true {
		t.Fatalf("ready event = %#v", event)
	}
	if err := connection.WriteMessage(websocket.BinaryMessage, pcmTestFrame(1200, 320)); err != nil {
		t.Fatalf("Write audio: %v", err)
	}
	if err := connection.WriteJSON(map[string]string{"type": "commit", "turnId": "turn-1"}); err != nil {
		t.Fatalf("Write commit: %v", err)
	}

	seen := make(map[string]bool)
	var gotAudio []byte
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = connection.SetReadDeadline(deadline)
		messageType, payload, readErr := connection.ReadMessage()
		if readErr != nil {
			t.Fatalf("ReadMessage(): %v; seen=%v", readErr, seen)
		}
		if messageType == websocket.BinaryMessage {
			gotAudio = append([]byte(nil), payload...)
			continue
		}
		var event map[string]interface{}
		if err := json.Unmarshal(payload, &event); err != nil {
			t.Fatalf("decode event %q: %v", payload, err)
		}
		eventType, _ := event["type"].(string)
		seen[eventType] = true
		if eventType == "turn.completed" {
			break
		}
	}
	for _, eventType := range []string{"turn.processing", "asr.final", "session.created", "assistant.delta", "tts.start", "tts.audio", "tts.end", "turn.completed"} {
		if !seen[eventType] {
			t.Fatalf("missing %s event; got %v", eventType, seen)
		}
	}
	if got, want := string(gotAudio), "fake-audio"; got != want {
		t.Fatalf("audio = %q, want %q", got, want)
	}
	if got := provider.lastPlan(); got.Emotion != voicecontrol.EmotionWarm || got.Text != "[ignored] 这是回答。" {
		t.Fatalf("TTS plan = %#v", got)
	}
}

func TestRealtimeVoiceRejectsCookieStartWithoutCSRF(t *testing.T) {
	configureVoiceJWT(t)
	token, err := myjwt.GenerateToken(102, "voice-user")
	if err != nil {
		t.Fatalf("GenerateToken(): %v", err)
	}
	handler := NewHandler(voiceconversation.NewHub(2, 1))
	handler.ProviderRegistry = func() (*speechgateway.Registry, error) { return speechgateway.NewRegistry(), nil }

	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api/v1/AI")
	group.Use(jwt.Auth())
	group.GET("/voice/realtime", handler.Serve)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/AI/voice/realtime"
	headers := http.Header{}
	headers.Set("Origin", server.URL)
	headers.Set("Cookie", fmt.Sprintf("%s=%s; %s=%s", sessionauth.SessionCookieName, token, sessionauth.CSRFCookieName, "csrf-value"))
	connection, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("Dial(): %v", err)
	}
	defer connection.Close()
	if err := connection.WriteJSON(map[string]string{"type": "start", "turnId": "turn-1", "modelId": "4", "csrfToken": "wrong"}); err != nil {
		t.Fatalf("Write start: %v", err)
	}
	event := readJSONEvent(t, connection)
	if event["type"] != "error" || event["code"] != "csrf_failed" {
		t.Fatalf("event = %#v", event)
	}
}

func TestOriginAllowedRejectsForeignProductionOrigin(t *testing.T) {
	configureVoiceJWT(t)
	t.Setenv("GOPHERAI_ENV", "production")
	request := httptest.NewRequest(http.MethodGet, "http://api.example.test/api/v1/AI/voice/realtime", nil)
	request.Host = "api.example.test"
	request.Header.Set("Origin", "https://evil.example.test")
	if originAllowed(request) {
		t.Fatal("originAllowed() accepted foreign production origin")
	}
}

func TestRealtimeVoiceRequiresStartFrame(t *testing.T) {
	configureVoiceJWT(t)
	token, err := myjwt.GenerateToken(103, "voice-user")
	if err != nil {
		t.Fatalf("GenerateToken(): %v", err)
	}
	handler := NewHandler(voiceconversation.NewHub(2, 1))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api/v1/AI")
	group.Use(jwt.Auth())
	group.GET("/voice/realtime", handler.Serve)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/AI/voice/realtime"
	headers := http.Header{}
	headers.Set("Origin", server.URL)
	headers.Set("Cookie", fmt.Sprintf("%s=%s; %s=%s", sessionauth.SessionCookieName, token, sessionauth.CSRFCookieName, "csrf-value"))
	connection, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("Dial(): %v", err)
	}
	defer connection.Close()

	if err := connection.WriteJSON(map[string]string{"type": "ping"}); err != nil {
		t.Fatalf("Write ping: %v", err)
	}
	event := readJSONEvent(t, connection)
	if event["type"] != "error" || event["code"] != "start_required" {
		t.Fatalf("event = %#v", event)
	}
}

func TestConnectionStateInterruptBeforeCommitAllowsNewTurn(t *testing.T) {
	state := &connectionState{}
	first := &activeTurn{
		id:   "first",
		turn: voiceconversation.NewTurn(context.Background(), "first", nil),
	}
	if !state.start(first) {
		t.Fatal("first turn was not started")
	}
	if got := state.interrupt("first"); got != first {
		t.Fatalf("interrupt() = %#v, want first turn", got)
	}
	if current := state.current(); current != nil {
		t.Fatalf("current turn after interrupt = %#v, want nil", current)
	}
	select {
	case <-first.turn.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("interrupt did not cancel first turn")
	}

	second := &activeTurn{
		id:   "second",
		turn: voiceconversation.NewTurn(context.Background(), "second", nil),
	}
	if !state.start(second) {
		t.Fatal("second turn was rejected after pre-commit interrupt")
	}
}

func TestRealtimeVoiceDrainClosesLiveConnection(t *testing.T) {
	configureVoiceJWT(t)
	token, err := myjwt.GenerateToken(104, "voice-user")
	if err != nil {
		t.Fatalf("GenerateToken(): %v", err)
	}
	hub := voiceconversation.NewHub(2, 1)
	handler := NewHandler(hub)
	handler.ProviderRegistry = func() (*speechgateway.Registry, error) { return speechgateway.NewRegistry(), nil }

	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api/v1/AI")
	group.Use(jwt.Auth())
	group.GET("/voice/realtime", handler.Serve)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/AI/voice/realtime"
	headers := http.Header{}
	headers.Set("Origin", server.URL)
	headers.Set("Cookie", fmt.Sprintf("%s=%s; %s=%s", sessionauth.SessionCookieName, token, sessionauth.CSRFCookieName, "csrf-value"))
	connection, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("Dial(): %v", err)
	}
	defer connection.Close()
	if err := connection.WriteJSON(map[string]interface{}{
		"type":           "start",
		"turnId":         "turn-drain",
		"modelId":        "4",
		"voiceProfileId": "text-only",
		"csrfToken":      "csrf-value",
		"audioFormat":    "pcm16",
		"sampleRate":     16000,
		"channels":       1,
	}); err != nil {
		t.Fatalf("Write start: %v", err)
	}
	if event := readJSONEvent(t, connection); event["type"] != "ready" {
		t.Fatalf("ready event = %#v", event)
	}

	hub.BeginDrain()
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err = connection.ReadMessage()
	closeError, ok := err.(*websocket.CloseError)
	if !ok || closeError.Code != websocket.CloseGoingAway {
		t.Fatalf("drain read error = %v, want close code %d", err, websocket.CloseGoingAway)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := hub.Wait(waitCtx); err != nil {
		t.Fatalf("hub.Wait(): %v", err)
	}
}

func TestHandlerRecognizeUsesConfiguredProviderRegistry(t *testing.T) {
	registry := speechgateway.NewRegistry()
	provider := &testASRProvider{id: "test-asr", transcript: "recognized"}
	if err := registry.RegisterASR(provider); err != nil {
		t.Fatalf("RegisterASR(): %v", err)
	}
	handler := NewHandler(voiceconversation.NewHub(2, 1))
	handler.ProviderRegistry = func() (*speechgateway.Registry, error) { return registry, nil }

	request := asr.RecognitionRequest{Audio: []byte{1, 2}, Format: "pcm", SampleRate: 16000, Username: "voice-user"}
	transcript, err := handler.recognize(context.Background(), "test-asr", request)
	if err != nil {
		t.Fatalf("recognize(): %v", err)
	}
	if transcript != "recognized" || !provider.called {
		t.Fatalf("recognize() = %q, called=%v", transcript, provider.called)
	}
}

func readJSONEvent(t *testing.T, connection *websocket.Conn) map[string]interface{} {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	messageType, payload, err := connection.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage(): %v", err)
	}
	if messageType != websocket.TextMessage {
		t.Fatalf("message type = %d, want text", messageType)
	}
	var event map[string]interface{}
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	return event
}

func pcmTestFrame(sample int16, count int) []byte {
	frame := make([]byte, count*2)
	for index := 0; index < count; index++ {
		frame[index*2] = byte(sample)
		frame[index*2+1] = byte(uint16(sample) >> 8)
	}
	return frame
}

type testTTSProvider struct {
	mu   sync.Mutex
	plan voicecontrol.ExpressionPlan
}

type testASRProvider struct {
	id         string
	transcript string
	called     bool
}

func (p *testASRProvider) ID() string { return p.id }

func (p *testASRProvider) Recognize(_ context.Context, _ asr.RecognitionRequest) (string, error) {
	p.called = true
	return p.transcript, nil
}

func (p *testTTSProvider) ID() string { return "fish-s2" }

func (p *testTTSProvider) Synthesize(_ context.Context, plan voicecontrol.ExpressionPlan) (speechgateway.Audio, error) {
	p.mu.Lock()
	p.plan = plan
	p.mu.Unlock()
	return speechgateway.Audio{
		Bytes:       []byte("fake-audio"),
		ContentType: "audio/mpeg",
		Format:      "mp3",
		ProviderID:  "fish-s2",
	}, nil
}

func (p *testTTSProvider) lastPlan() voicecontrol.ExpressionPlan {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.plan
}
