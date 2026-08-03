package voice

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const maxControlFrameBytes = 8 << 10

const (
	clientStart     = "start"
	clientCommit    = "commit"
	clientInterrupt = "interrupt"
	clientClose     = "close"
	clientPing      = "ping"
)

type clientControl struct {
	Type             string   `json:"type"`
	TurnID           string   `json:"turnId,omitempty"`
	SessionID        string   `json:"sessionId,omitempty"`
	ModelID          string   `json:"modelId,omitempty"`
	ModelType        string   `json:"modelType,omitempty"`
	KnowledgeBaseIDs []string `json:"knowledgeBaseIds,omitempty"`
	VoiceProfileID   string   `json:"voiceProfileId,omitempty"`
	CSRFToken        string   `json:"csrfToken,omitempty"`
	AudioFormat      string   `json:"audioFormat,omitempty"`
	SampleRate       int      `json:"sampleRate,omitempty"`
	Channels         int      `json:"channels,omitempty"`
}

func decodeControl(payload []byte) (clientControl, error) {
	if len(payload) == 0 || len(payload) > maxControlFrameBytes {
		return clientControl{}, errors.New("voice control frame is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var event clientControl
	if err := decoder.Decode(&event); err != nil {
		return clientControl{}, errors.New("voice control frame is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return clientControl{}, errors.New("voice control frame is invalid")
	}
	event.Type = strings.ToLower(strings.TrimSpace(event.Type))
	switch event.Type {
	case clientStart, clientCommit, clientInterrupt, clientClose, clientPing:
	default:
		return clientControl{}, errors.New("voice control type is invalid")
	}
	return event, nil
}

func validateStart(event clientControl) error {
	if event.Type != clientStart || !validPublicID(event.TurnID, 96) {
		return errors.New("voice start is invalid")
	}
	if event.SessionID != "" && !validPublicID(event.SessionID, 128) {
		return errors.New("voice session ID is invalid")
	}
	selector := strings.TrimSpace(event.ModelID)
	if selector == "" {
		selector = strings.TrimSpace(event.ModelType)
	}
	if !validPublicID(selector, 128) {
		return errors.New("voice model ID is invalid")
	}
	if event.AudioFormat != "" && !strings.EqualFold(strings.TrimSpace(event.AudioFormat), "pcm16") {
		return errors.New("voice audio format is invalid")
	}
	if event.SampleRate != 0 && event.SampleRate != 16000 {
		return errors.New("voice sample rate is invalid")
	}
	if event.Channels != 0 && event.Channels != 1 {
		return errors.New("voice channel count is invalid")
	}
	if len(event.KnowledgeBaseIDs) > 32 {
		return errors.New("too many voice knowledge bases")
	}
	for _, id := range event.KnowledgeBaseIDs {
		if !validPublicID(id, 128) {
			return errors.New("voice knowledge base ID is invalid")
		}
	}
	if event.VoiceProfileID != "" && !validPublicID(event.VoiceProfileID, 64) {
		return errors.New("voice profile ID is invalid")
	}
	return nil
}

func validateTurnControl(event clientControl) error {
	if !validPublicID(event.TurnID, 96) {
		return fmt.Errorf("voice turn ID is invalid")
	}
	return nil
}

func validPublicID(value string, maximum int) bool {
	value = strings.TrimSpace(value)
	if value == "" || maximum < 1 || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maximum {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}
