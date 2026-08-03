package voice

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"GopherAI/common/asr"
	"GopherAI/common/code"
	"GopherAI/common/fishaudio"
	"GopherAI/common/sessionauth"
	"GopherAI/common/speechgateway"
	"GopherAI/common/voicecontrol"
	"GopherAI/config"
	"GopherAI/model"
	sessionservice "GopherAI/service/session"
	"GopherAI/service/voiceconversation"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

const (
	voiceTurnTimeout       = 5 * time.Minute
	voiceInitialReadWindow = 10 * time.Second
	voiceWriteTimeout      = 15 * time.Second
)

type RecognizeFunc func(context.Context, asr.RecognitionRequest) (string, error)
type CreateSessionFunc func(context.Context, string, string) (string, code.Code)
type StreamSessionFunc func(context.Context, string, string, string, sessionservice.ChatOptions, sessionservice.StreamSink) code.Code
type ProviderRegistryFunc func() (*speechgateway.Registry, error)

// Handler wires the vendor-neutral voice turn state to the existing chat
// session service. Dependencies are fields so integration tests can use fake
// ASR, TTS, and LLM stages without network services.
type Handler struct {
	Hub              *voiceconversation.Hub
	Recognize        RecognizeFunc
	CreateSession    CreateSessionFunc
	StreamSession    StreamSessionFunc
	ProviderRegistry ProviderRegistryFunc
}

func NewHandler(hub *voiceconversation.Hub) *Handler {
	if hub == nil {
		hub = voiceconversation.DefaultHub()
	}
	return &Handler{
		Hub:           hub,
		CreateSession: sessionservice.CreateStreamSessionOnlyWithContext,
		StreamSession: sessionservice.StreamMessageToExistingSessionWithSink,
		ProviderRegistry: func() (*speechgateway.Registry, error) {
			return defaultProviderRegistry()
		},
	}
}

// Realtime is the authenticated WebSocket endpoint.  JWT authentication is
// installed on the parent /api/v1/AI group; browser cookie sessions complete
// an additional CSRF check in the first start frame below.
func Realtime(c *gin.Context) {
	NewHandler(voiceconversation.DefaultHub()).Serve(c)
}

func (h *Handler) Serve(c *gin.Context) {
	if h == nil || c == nil || h.Hub == nil || !h.Hub.Accepting() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status_code": code.CodeServerBusy, "status_msg": "voice service is draining"})
		return
	}
	if !originAllowed(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"status_code": code.CodeForbidden, "status_msg": "voice origin is not allowed"})
		return
	}

	upgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin:     originAllowed,
	}
	connection, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	connection.SetReadLimit(voiceconversation.PCM16FrameMaxBytes)

	connectionCtx, cancelConnection := context.WithCancel(context.Background())
	release, err := h.Hub.Register(c.GetString("userName"), cancelConnection)
	if err != nil {
		_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "voice service unavailable"), time.Now().Add(voiceWriteTimeout))
		_ = connection.Close()
		return
	}
	defer release()
	defer cancelConnection()
	defer connection.Close()

	sink := newSocketSink(connection, cancelConnection)
	connectionDone := make(chan struct{})
	defer close(connectionDone)
	go func() {
		select {
		case <-connectionCtx.Done():
			_ = sink.close(websocket.CloseGoingAway, "voice service is stopping")
			// A close control frame is advisory: an unresponsive peer can ignore
			// it while ReadMessage remains blocked. Closing the underlying socket
			// makes the handler release its hub slot before the shutdown deadline.
			_ = connection.Close()
		case <-connectionDone:
		}
	}()

	state := &connectionState{}
	csrfVerified := c.GetString("authSource") != "cookie"
	started := false
	connection.SetReadDeadline(time.Now().Add(voiceInitialReadWindow))
	connection.SetPongHandler(func(string) error {
		return connection.SetReadDeadline(time.Now().Add(voiceTurnTimeout))
	})

	for {
		messageType, payload, readErr := connection.ReadMessage()
		if readErr != nil {
			return
		}

		switch messageType {
		case websocket.TextMessage:
			event, err := decodeControl(payload)
			if err != nil {
				_ = sink.event("error", "", map[string]interface{}{"code": "invalid_control", "message": "Invalid voice control frame."})
				if !started {
					_ = sink.close(websocket.ClosePolicyViolation, "voice start required")
					return
				}
				continue
			}
			if !started {
				// A connection has a short, non-renewable start window. In
				// particular, a bearer-token client must not be able to reserve a
				// hub slot indefinitely by sending ping frames before a turn.
				if event.Type != clientStart {
					_ = sink.event("error", event.TurnID, map[string]interface{}{"code": "start_required", "message": "The first voice control frame must start a turn."})
					_ = sink.close(websocket.ClosePolicyViolation, "voice start required")
					return
				}
				if !csrfVerified && !validStartCSRF(c.Request, event.CSRFToken) {
					_ = sink.event("error", "", map[string]interface{}{"code": "csrf_failed", "message": "Voice session verification failed."})
					_ = sink.close(websocket.ClosePolicyViolation, "voice session verification failed")
					return
				}
				csrfVerified = true
			}
			if !h.handleControl(connectionCtx, c.GetString("userName"), state, event, sink) {
				return
			}
			if !started && state.current() != nil {
				started = true
				// Only an accepted start may move this connection out of the
				// short initial handshake deadline.
				_ = connection.SetReadDeadline(time.Now().Add(voiceTurnTimeout))
			}
		case websocket.BinaryMessage:
			if !started {
				_ = sink.event("error", "", map[string]interface{}{"code": "start_required", "message": "Start a voice turn before sending audio."})
				_ = sink.close(websocket.ClosePolicyViolation, "voice start required")
				return
			}
			active := state.current()
			if active == nil || active.isProcessing() {
				_ = sink.event("error", "", map[string]interface{}{"code": "unexpected_audio", "message": "No voice turn is accepting audio."})
				continue
			}
			transition, err := active.turn.AppendPCM16LE(payload)
			if err != nil {
				_ = sink.event("error", active.id, map[string]interface{}{"code": "invalid_audio", "message": "Voice audio frame was rejected."})
				if errors.Is(err, voiceconversation.ErrAudioLimitExceeded) {
					active.turn.Cancel()
				}
				continue
			}
			switch transition {
			case voiceconversation.VADSpeechStart:
				_ = sink.event("vad.speech_started", active.id, nil)
			case voiceconversation.VADSpeechEnd:
				_ = sink.event("vad.speech_stopped", active.id, nil)
			}
		default:
			_ = sink.event("error", "", map[string]interface{}{"code": "invalid_frame", "message": "Unsupported voice frame."})
		}
	}
}

func (h *Handler) handleControl(connectionCtx context.Context, userName string, state *connectionState, event clientControl, sink *socketSink) bool {
	switch event.Type {
	case clientStart:
		if err := validateStart(event); err != nil {
			_ = sink.event("error", event.TurnID, map[string]interface{}{"code": "invalid_start", "message": "Invalid voice start event."})
			return true
		}
		prepared, err := h.prepareTurn(event)
		if err != nil {
			_ = sink.event("error", event.TurnID, map[string]interface{}{"code": "invalid_profile", "message": "Voice profile is unavailable."})
			return true
		}
		active := &activeTurn{
			id:        event.TurnID,
			sessionID: strings.TrimSpace(event.SessionID),
			options:   chatOptionsFromStart(event),
			turn:      voiceconversation.NewTurn(connectionCtx, event.TurnID, nil),
			profile:   prepared,
		}
		if !state.start(active) {
			_ = sink.event("error", event.TurnID, map[string]interface{}{"code": "turn_active", "message": "Finish or interrupt the active voice turn first."})
			return true
		}
		ready := map[string]interface{}{
			"sessionId":      active.sessionID,
			"asrProvider":    prepared.asrProviderID,
			"audioFormat":    "pcm16",
			"sampleRate":     16000,
			"channels":       1,
			"voiceProfileId": prepared.id,
			"ttsAvailable":   prepared.provider != nil,
		}
		_ = sink.event("ready", active.id, ready)
		if prepared.warning != "" {
			_ = sink.event("tts.unavailable", active.id, map[string]interface{}{"message": prepared.warning})
		}
		return true
	case clientCommit:
		if err := validateTurnControl(event); err != nil {
			_ = sink.event("error", "", map[string]interface{}{"code": "invalid_turn", "message": "Invalid voice turn ID."})
			return true
		}
		active := state.beginProcessing(event.TurnID)
		if active == nil {
			_ = sink.event("error", event.TurnID, map[string]interface{}{"code": "invalid_turn", "message": "Voice turn cannot be committed."})
			return true
		}
		audio, err := active.turn.Commit()
		if err != nil {
			state.stopProcessing(active)
			_ = sink.event("error", active.id, map[string]interface{}{"code": "invalid_audio", "message": "Voice turn has no valid audio."})
			return true
		}
		_ = sink.event("turn.processing", active.id, nil)
		go h.processTurn(userName, state, active, audio, sink)
		return true
	case clientInterrupt:
		if err := validateTurnControl(event); err != nil {
			_ = sink.event("error", "", map[string]interface{}{"code": "invalid_turn", "message": "Invalid voice turn ID."})
			return true
		}
		active := state.interrupt(event.TurnID)
		if active == nil {
			_ = sink.event("error", event.TurnID, map[string]interface{}{"code": "invalid_turn", "message": "Voice turn is not active."})
			return true
		}
		_ = sink.event("turn.cancelled", active.id, nil)
		return true
	case clientPing:
		_ = sink.event("pong", event.TurnID, nil)
		return true
	case clientClose:
		return false
	default:
		return true
	}
}

func (h *Handler) processTurn(userName string, state *connectionState, active *activeTurn, pcm []byte, sink *socketSink) {
	defer state.finish(active)
	turnCtx, cancel := context.WithTimeout(active.turn.Context(), voiceTurnTimeout)
	defer cancel()
	if turnCtx.Err() != nil {
		_ = sink.event("turn.cancelled", active.id, nil)
		return
	}

	wav, err := voiceconversation.PCM16LEToWAV(pcm, 16000)
	if err != nil {
		_ = sink.event("error", active.id, map[string]interface{}{"code": "invalid_audio", "message": "Voice audio could not be processed."})
		return
	}
	transcript, err := h.recognize(turnCtx, active.profile.asrProviderID, asr.RecognitionRequest{
		Audio:      wav,
		Format:     "wav",
		SampleRate: 16000,
		Username:   userName,
	})
	if err != nil {
		if turnCtx.Err() != nil {
			_ = sink.event("turn.cancelled", active.id, nil)
			return
		}
		log.Printf("voice ASR failed: %v", err)
		_ = sink.event("error", active.id, map[string]interface{}{"code": "asr_failed", "message": "Speech recognition failed."})
		return
	}
	transcript = strings.TrimSpace(transcript)
	if transcript == "" {
		_ = sink.event("error", active.id, map[string]interface{}{"code": "asr_empty", "message": "No speech was recognized."})
		return
	}
	if err := sink.event("asr.final", active.id, map[string]interface{}{"text": transcript}); err != nil {
		return
	}

	sessionID := active.sessionID
	if sessionID == "" {
		if h.CreateSession == nil {
			_ = sink.event("error", active.id, map[string]interface{}{"code": "session_unavailable", "message": "Voice session is unavailable."})
			return
		}
		var resultCode code.Code
		sessionID, resultCode = h.CreateSession(turnCtx, userName, transcript)
		if resultCode != code.CodeSuccess || strings.TrimSpace(sessionID) == "" {
			_ = sink.event("error", active.id, map[string]interface{}{"code": "session_failed", "message": "Voice session could not be created."})
			return
		}
		active.setSessionID(sessionID)
		if err := sink.event("session.created", active.id, map[string]interface{}{"sessionId": sessionID}); err != nil {
			return
		}
	}

	if h.StreamSession == nil {
		_ = sink.event("error", active.id, map[string]interface{}{"code": "chat_unavailable", "message": "Voice chat is unavailable."})
		return
	}
	h.streamResponse(turnCtx, userName, sessionID, transcript, active, sink)
}

func (h *Handler) streamResponse(ctx context.Context, userName, sessionID, transcript string, active *activeTurn, sink *socketSink) {
	buffer := voiceconversation.NewSentenceBuffer(180)
	var queue chan string
	var worker sync.WaitGroup
	if active.profile.provider != nil {
		queue = make(chan string, 4)
		worker.Add(1)
		go func() {
			defer worker.Done()
			h.synthesizeQueued(ctx, active, queue, sink)
		}()
	}
	queueSentence := func(sentence string) error {
		if queue == nil || strings.TrimSpace(sentence) == "" {
			return nil
		}
		select {
		case queue <- sentence:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	streamSink := sessionservice.StreamSinkFuncs{
		OnTextDelta: func(_ context.Context, content string) error {
			if err := sink.event("assistant.delta", active.id, map[string]interface{}{"text": content}); err != nil {
				return err
			}
			for _, sentence := range buffer.Push(content) {
				if err := queueSentence(sentence); err != nil {
					return err
				}
			}
			return nil
		},
		OnCitations: func(_ context.Context, citations []model.KnowledgeReference) error {
			return sink.event("assistant.citations", active.id, map[string]interface{}{"citations": citations})
		},
		OnComplete: func(context.Context) error { return nil },
	}

	resultCode := h.StreamSession(ctx, userName, sessionID, transcript, active.options, streamSink)
	if resultCode == code.CodeSuccess && ctx.Err() == nil {
		for _, sentence := range buffer.Flush() {
			if err := queueSentence(sentence); err != nil {
				resultCode = code.AIModelFail
				break
			}
		}
	}
	if queue != nil {
		close(queue)
		worker.Wait()
	}
	if ctx.Err() != nil {
		_ = sink.event("turn.cancelled", active.id, nil)
		return
	}
	if resultCode != code.CodeSuccess {
		_ = sink.event("error", active.id, map[string]interface{}{"code": "chat_failed", "message": "Voice response generation failed."})
		return
	}
	_ = sink.event("turn.completed", active.id, map[string]interface{}{"sessionId": sessionID})
}

func (h *Handler) synthesizeQueued(ctx context.Context, active *activeTurn, queue <-chan string, sink *socketSink) {
	segmentID := 0
	for sentence := range queue {
		if ctx.Err() != nil {
			return
		}
		plan := active.profile.plan
		plan.Text = sentence
		if err := sink.event("tts.start", active.id, map[string]interface{}{"segmentId": segmentID, "profile": active.profile.id}); err != nil {
			return
		}
		audio, err := active.profile.provider.Synthesize(ctx, plan)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("voice TTS failed: %v", err)
			_ = sink.event("tts.error", active.id, map[string]interface{}{"segmentId": segmentID, "message": "Speech synthesis for one segment failed."})
			segmentID++
			continue
		}
		if err := sink.audio(active.id, segmentID, audio); err != nil {
			return
		}
		_ = sink.event("tts.end", active.id, map[string]interface{}{"segmentId": segmentID})
		segmentID++
	}
}

type preparedProfile struct {
	id            string
	plan          voicecontrol.ExpressionPlan
	asrProviderID string
	provider      speechgateway.TTSProvider
	warning       string
}

func (h *Handler) prepareTurn(event clientControl) (preparedProfile, error) {
	profileID := strings.ToLower(strings.TrimSpace(event.VoiceProfileID))
	if profileID == "" {
		profileID = "fish-s2-natural"
	}
	plan, recognized := expressionPlanForProfile(profileID)
	if !recognized {
		return preparedProfile{}, errors.New("voice profile is unknown")
	}
	prepared := preparedProfile{id: profileID, plan: plan, asrProviderID: configuredASRProviderID()}
	if profileID == "text-only" {
		return prepared, nil
	}
	if h.ProviderRegistry == nil {
		prepared.warning = "Expressive TTS is not configured."
		return prepared, nil
	}
	registry, err := h.ProviderRegistry()
	if err != nil {
		log.Printf("voice provider registry: %v", err)
		prepared.warning = "Expressive TTS is unavailable."
		return prepared, nil
	}
	if provider, ok := registry.Get("fish-s2"); ok {
		prepared.provider = provider
		return prepared, nil
	}
	prepared.warning = "Expressive TTS is not configured."
	return prepared, nil
}

func (h *Handler) recognize(ctx context.Context, providerID string, request asr.RecognitionRequest) (string, error) {
	if h != nil && h.Recognize != nil {
		return h.Recognize(ctx, request)
	}
	if h == nil || h.ProviderRegistry == nil {
		return "", errors.New("speech recognition is unavailable")
	}
	registry, err := h.ProviderRegistry()
	if err != nil {
		return "", fmt.Errorf("load speech providers: %w", err)
	}
	provider, ok := registry.GetASR(providerID)
	if !ok || provider == nil {
		return "", errors.New("configured speech recognition provider is unavailable")
	}
	return provider.Recognize(ctx, request)
}

func configuredASRProviderID() string {
	conf := config.GetConfig()
	if conf != nil {
		if providerID := strings.ToLower(strings.TrimSpace(conf.VoiceRealtimeConfig.ASRProvider)); providerID != "" {
			return providerID
		}
	}
	return speechgateway.BaiduBatchProviderID
}

func expressionPlanForProfile(id string) (voicecontrol.ExpressionPlan, bool) {
	switch id {
	case "text-only", "fish-s2-natural":
		return voicecontrol.ExpressionPlan{Delivery: voicecontrol.DeliveryNatural}, true
	case "fish-s2-warm":
		return voicecontrol.ExpressionPlan{Emotion: voicecontrol.EmotionWarm, Delivery: voicecontrol.DeliveryGentle, Volume: voicecontrol.VolumeSoft}, true
	case "fish-s2-empathetic":
		return voicecontrol.ExpressionPlan{Emotion: voicecontrol.EmotionEmpathetic, Delivery: voicecontrol.DeliveryConversational, Pace: voicecontrol.PaceSlow}, true
	case "fish-s2-energetic":
		return voicecontrol.ExpressionPlan{Emotion: voicecontrol.EmotionExcited, Delivery: voicecontrol.DeliveryEnergetic, Pace: voicecontrol.PaceFast}, true
	default:
		return voicecontrol.ExpressionPlan{}, false
	}
}

func chatOptionsFromStart(event clientControl) sessionservice.ChatOptions {
	selector := strings.TrimSpace(event.ModelID)
	if selector == "" {
		selector = strings.TrimSpace(event.ModelType)
	}
	seen := make(map[string]struct{}, len(event.KnowledgeBaseIDs))
	ids := make([]string, 0, len(event.KnowledgeBaseIDs))
	for _, rawID := range event.KnowledgeBaseIDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return sessionservice.ChatOptions{
		ModelType:               selector,
		KnowledgeBaseIDs:        ids,
		KnowledgeBasesSpecified: event.KnowledgeBaseIDs != nil,
	}
}

func validStartCSRF(request *http.Request, token string) bool {
	if request == nil || strings.TrimSpace(token) == "" {
		return false
	}
	copyRequest := request.Clone(request.Context())
	copyRequest.Header = request.Header.Clone()
	copyRequest.Header.Set(sessionauth.CSRFHeader, strings.TrimSpace(token))
	return sessionauth.ValidCSRF(copyRequest)
}

func defaultProviderRegistry() (*speechgateway.Registry, error) {
	registry := speechgateway.NewRegistry()
	baiduASR, err := speechgateway.NewBaiduASRProvider(asr.NewService())
	if err != nil {
		return nil, err
	}
	if err := registry.RegisterASR(baiduASR); err != nil {
		return nil, err
	}
	conf := config.GetConfig()
	if conf == nil {
		return registry, nil
	}
	realtime := conf.VoiceRealtimeConfig
	if strings.TrimSpace(realtime.FishAudioAPIKey) == "" || strings.TrimSpace(realtime.FishAudioReferenceID) == "" {
		return registry, nil
	}
	client, err := fishaudio.NewClient(fishaudio.Config{
		APIKey:      realtime.FishAudioAPIKey,
		BaseURL:     realtime.FishAudioBaseURL,
		Model:       realtime.FishAudioModel,
		ReferenceID: realtime.FishAudioReferenceID,
	})
	if err != nil {
		// Fish is an optional output stage. A malformed optional TTS
		// configuration must not disable ASR and the text conversation path.
		log.Printf("Fish Audio provider disabled: %v", err)
		return registry, nil
	}
	provider, err := speechgateway.NewFishS2Provider("fish-s2", client)
	if err != nil {
		log.Printf("Fish Audio provider disabled: %v", err)
		return registry, nil
	}
	if err := registry.Register(provider); err != nil {
		log.Printf("Fish Audio provider disabled: %v", err)
		return registry, nil
	}
	return registry, nil
}

type activeTurn struct {
	id        string
	sessionID string
	options   sessionservice.ChatOptions
	turn      *voiceconversation.Turn
	profile   preparedProfile

	mu         sync.Mutex
	processing bool
}

func (t *activeTurn) isProcessing() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.processing
}

func (t *activeTurn) setSessionID(sessionID string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.sessionID = sessionID
	t.mu.Unlock()
}

type connectionState struct {
	mu     sync.Mutex
	active *activeTurn
}

func (s *connectionState) current() *activeTurn {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

func (s *connectionState) start(active *activeTurn) bool {
	if s == nil || active == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil {
		return false
	}
	s.active = active
	return true
}

func (s *connectionState) beginProcessing(turnID string) *activeTurn {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || s.active.id != turnID {
		return nil
	}
	s.active.mu.Lock()
	defer s.active.mu.Unlock()
	if s.active.processing {
		return nil
	}
	s.active.processing = true
	return s.active
}

func (s *connectionState) stopProcessing(active *activeTurn) {
	if active == nil {
		return
	}
	active.mu.Lock()
	active.processing = false
	active.mu.Unlock()
}

func (s *connectionState) interrupt(turnID string) *activeTurn {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.active == nil || s.active.id != turnID {
		s.mu.Unlock()
		return nil
	}
	active := s.active
	active.mu.Lock()
	processing := active.processing
	active.mu.Unlock()
	// A turn that has only been collecting microphone frames has no worker
	// that will call finish. Clear it immediately so the user can begin a new
	// turn after cancelling before commit. A processing turn remains owned by
	// its worker until that worker exits.
	if !processing {
		s.active = nil
	}
	s.mu.Unlock()
	active.turn.Cancel()
	return active
}

func (s *connectionState) finish(active *activeTurn) {
	if s == nil || active == nil {
		return
	}
	s.mu.Lock()
	if s.active == active {
		s.active = nil
	}
	s.mu.Unlock()
}

// socketSink serializes every output frame. Gorilla permits one concurrent
// writer, while model callbacks and the Fish synthesis worker intentionally
// run in separate goroutines.
type socketSink struct {
	connection *websocket.Conn
	cancel     context.CancelFunc
	mu         sync.Mutex
	writeErr   error
}

func newSocketSink(connection *websocket.Conn, cancel context.CancelFunc) *socketSink {
	return &socketSink{connection: connection, cancel: cancel}
}

func (s *socketSink) event(eventType, turnID string, data map[string]interface{}) error {
	payload := make(map[string]interface{}, len(data)+2)
	payload["type"] = eventType
	if turnID != "" {
		payload["turnId"] = turnID
	}
	for key, value := range data {
		payload[key] = value
	}
	return s.writeJSON(payload)
}

func (s *socketSink) audio(turnID string, segmentID int, audio speechgateway.Audio) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.errorLocked(); err != nil {
		return err
	}
	metadata := map[string]interface{}{
		"type":        "tts.audio",
		"turnId":      turnID,
		"segmentId":   segmentID,
		"contentType": audio.ContentType,
		"format":      audio.Format,
		"provider":    audio.ProviderID,
		"byteLength":  len(audio.Bytes),
	}
	if err := s.writeJSONLocked(metadata); err != nil {
		return err
	}
	if err := s.connection.WriteMessage(websocket.BinaryMessage, audio.Bytes); err != nil {
		s.rememberLocked(err)
		return err
	}
	return s.clearDeadlineLocked()
}

func (s *socketSink) writeJSON(payload map[string]interface{}) error {
	if s == nil {
		return errors.New("voice sink is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.errorLocked(); err != nil {
		return err
	}
	return s.writeJSONLocked(payload)
}

func (s *socketSink) writeJSONLocked(payload map[string]interface{}) error {
	if err := s.connection.SetWriteDeadline(time.Now().Add(voiceWriteTimeout)); err != nil {
		s.rememberLocked(err)
		return err
	}
	if err := s.connection.WriteJSON(payload); err != nil {
		s.rememberLocked(err)
		return err
	}
	return s.clearDeadlineLocked()
}

func (s *socketSink) close(closeCode int, text string) error {
	if s == nil || s.connection == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(closeCode, text), time.Now().Add(voiceWriteTimeout))
}

func (s *socketSink) errorLocked() error {
	if s.writeErr != nil {
		return s.writeErr
	}
	return nil
}

func (s *socketSink) clearDeadlineLocked() error {
	if err := s.connection.SetWriteDeadline(time.Time{}); err != nil {
		s.rememberLocked(err)
		return err
	}
	return nil
}

func (s *socketSink) rememberLocked(err error) {
	if err == nil || s.writeErr != nil {
		return
	}
	s.writeErr = err
	if s.cancel != nil {
		s.cancel()
	}
}
