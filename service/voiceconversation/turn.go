// Package voiceconversation contains the transport-independent state used by
// a realtime voice turn.  It deliberately does not know about a browser,
// WebSocket implementation, or a particular speech vendor.
package voiceconversation

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	// PCM16FrameMaxBytes allows a little over two seconds at 16 kHz mono while
	// keeping a malicious client from submitting an unbounded WebSocket frame.
	PCM16FrameMaxBytes = 64 << 10
	MaxTurnAudioBytes  = 10 << 20
)

var (
	ErrTurnCancelled        = errors.New("voice turn was cancelled")
	ErrTurnAlreadyCommitted = errors.New("voice turn was already committed")
	ErrAudioFrameInvalid    = errors.New("voice audio frame is invalid")
	ErrAudioLimitExceeded   = errors.New("voice audio exceeds turn limit")
)

// VADTransition is emitted only when the lightweight fallback VAD changes
// state.  Deployments can replace this detector with a model-backed VAD later
// without changing the realtime wire protocol.
type VADTransition string

const (
	VADNoTransition VADTransition = ""
	VADSpeechStart  VADTransition = "speech_started"
	VADSpeechEnd    VADTransition = "speech_stopped"
)

// EnergyVADConfig describes a deterministic PCM16 fallback.  It is not
// intended to replace Silero/FSMN VAD in noisy production environments, but
// it provides bounded server-side turn detection for the first integration.
type EnergyVADConfig struct {
	Threshold   float64
	StartFrames int
	EndFrames   int
}

func DefaultEnergyVADConfig() EnergyVADConfig {
	return EnergyVADConfig{
		Threshold:   0.018,
		StartFrames: 2,
		EndFrames:   25, // approximately 500 ms for 20 ms input frames
	}
}

// EnergyVAD uses RMS energy with start/end hysteresis.  It accepts PCM16LE
// mono frames and is safe to keep inside a single Turn.
type EnergyVAD struct {
	config        EnergyVADConfig
	speaking      bool
	speechFrames  int
	silenceFrames int
}

func NewEnergyVAD(config EnergyVADConfig) *EnergyVAD {
	defaults := DefaultEnergyVADConfig()
	if config.Threshold <= 0 || math.IsNaN(config.Threshold) || math.IsInf(config.Threshold, 0) {
		config.Threshold = defaults.Threshold
	}
	if config.StartFrames < 1 {
		config.StartFrames = defaults.StartFrames
	}
	if config.EndFrames < 1 {
		config.EndFrames = defaults.EndFrames
	}
	return &EnergyVAD{config: config}
}

func (d *EnergyVAD) ObservePCM16LE(frame []byte) (VADTransition, error) {
	if d == nil {
		return VADNoTransition, nil
	}
	if len(frame) == 0 || len(frame)%2 != 0 {
		return VADNoTransition, ErrAudioFrameInvalid
	}
	if len(frame) > PCM16FrameMaxBytes {
		return VADNoTransition, ErrAudioFrameInvalid
	}

	var sum float64
	for offset := 0; offset < len(frame); offset += 2 {
		sample := int16(uint16(frame[offset]) | uint16(frame[offset+1])<<8)
		value := float64(sample) / 32768.0
		sum += value * value
	}
	rms := math.Sqrt(sum / float64(len(frame)/2))
	if rms >= d.config.Threshold {
		d.speechFrames++
		d.silenceFrames = 0
		if !d.speaking && d.speechFrames >= d.config.StartFrames {
			d.speaking = true
			return VADSpeechStart, nil
		}
		return VADNoTransition, nil
	}

	d.silenceFrames++
	d.speechFrames = 0
	if d.speaking && d.silenceFrames >= d.config.EndFrames {
		d.speaking = false
		return VADSpeechEnd, nil
	}
	return VADNoTransition, nil
}

// Turn owns one cancellable PCM16 input buffer and its VAD state.  The
// controller can use a transition to show activity to the client, but only a
// received commit starts ASR: this avoids accidentally submitting partial
// speech after a temporary pause.
type Turn struct {
	ID string

	ctx    context.Context
	cancel context.CancelFunc
	vad    *EnergyVAD

	mu        sync.Mutex
	audio     []byte
	committed bool
}

func NewTurn(parent context.Context, id string, detector *EnergyVAD) *Turn {
	if parent == nil {
		parent = context.Background()
	}
	if detector == nil {
		detector = NewEnergyVAD(DefaultEnergyVADConfig())
	}
	ctx, cancel := context.WithCancel(parent)
	return &Turn{ID: strings.TrimSpace(id), ctx: ctx, cancel: cancel, vad: detector}
}

func (t *Turn) Context() context.Context {
	if t == nil || t.ctx == nil {
		return context.Background()
	}
	return t.ctx
}

func (t *Turn) AppendPCM16LE(frame []byte) (VADTransition, error) {
	if t == nil {
		return VADNoTransition, ErrTurnCancelled
	}
	if err := t.Context().Err(); err != nil {
		return VADNoTransition, ErrTurnCancelled
	}
	if len(frame) == 0 || len(frame)%2 != 0 || len(frame) > PCM16FrameMaxBytes {
		return VADNoTransition, ErrAudioFrameInvalid
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.committed {
		return VADNoTransition, ErrTurnAlreadyCommitted
	}
	if len(t.audio)+len(frame) > MaxTurnAudioBytes {
		return VADNoTransition, ErrAudioLimitExceeded
	}
	transition, err := t.vad.ObservePCM16LE(frame)
	if err != nil {
		return VADNoTransition, err
	}
	t.audio = append(t.audio, frame...)
	return transition, nil
}

// Commit returns a defensive copy so a subsequent cancellation cannot mutate
// the data being sent to ASR.
func (t *Turn) Commit() ([]byte, error) {
	if t == nil || t.Context().Err() != nil {
		return nil, ErrTurnCancelled
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.committed {
		return nil, ErrTurnAlreadyCommitted
	}
	if len(t.audio) == 0 {
		return nil, ErrAudioFrameInvalid
	}
	t.committed = true
	audio := make([]byte, len(t.audio))
	copy(audio, t.audio)
	return audio, nil
}

func (t *Turn) Cancel() {
	if t != nil && t.cancel != nil {
		t.cancel()
	}
}

// PCM16LEToWAV adds the exact header required by the legacy Baidu recognizer.
// Realtime providers can consume the original PCM directly; keeping the
// conversion here makes the batch-ASR fallback explicit and testable.
func PCM16LEToWAV(pcm []byte, sampleRate int) ([]byte, error) {
	if len(pcm) == 0 || len(pcm)%2 != 0 || len(pcm) > MaxTurnAudioBytes {
		return nil, ErrAudioFrameInvalid
	}
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	if sampleRate != 16000 {
		return nil, ErrAudioFrameInvalid
	}
	dataLength := len(pcm)
	wav := make([]byte, 44+dataLength)
	copy(wav[0:4], "RIFF")
	putUint32LE(wav[4:8], uint32(36+dataLength))
	copy(wav[8:12], "WAVE")
	copy(wav[12:16], "fmt ")
	putUint32LE(wav[16:20], 16)
	putUint16LE(wav[20:22], 1)
	putUint16LE(wav[22:24], 1)
	putUint32LE(wav[24:28], uint32(sampleRate))
	putUint32LE(wav[28:32], uint32(sampleRate*2))
	putUint16LE(wav[32:34], 2)
	putUint16LE(wav[34:36], 16)
	copy(wav[36:40], "data")
	putUint32LE(wav[40:44], uint32(dataLength))
	copy(wav[44:], pcm)
	return wav, nil
}

func putUint16LE(target []byte, value uint16) {
	target[0] = byte(value)
	target[1] = byte(value >> 8)
}

func putUint32LE(target []byte, value uint32) {
	target[0] = byte(value)
	target[1] = byte(value >> 8)
	target[2] = byte(value >> 16)
	target[3] = byte(value >> 24)
}

// SentenceBuffer converts token deltas into safe TTS-sized phrases.  It
// flushes only after sentence punctuation (or a conservative rune ceiling),
// so Fish receives meaningful context and never sees a partial control tag.
type SentenceBuffer struct {
	maxRunes int
	buffer   strings.Builder
}

func NewSentenceBuffer(maxRunes int) *SentenceBuffer {
	if maxRunes < 40 {
		maxRunes = 180
	}
	return &SentenceBuffer{maxRunes: maxRunes}
}

func (b *SentenceBuffer) Push(delta string) []string {
	if b == nil || delta == "" {
		return nil
	}
	b.buffer.WriteString(strings.ToValidUTF8(delta, ""))
	return b.drain(false)
}

func (b *SentenceBuffer) Flush() []string {
	if b == nil {
		return nil
	}
	return b.drain(true)
}

func (b *SentenceBuffer) drain(force bool) []string {
	raw := b.buffer.String()
	if raw == "" {
		return nil
	}
	runes := []rune(raw)
	start := 0
	parts := make([]string, 0, 1)
	for index, r := range runes {
		length := index - start + 1
		if isSentenceBoundary(r) || length >= b.maxRunes {
			if segment := normalizeSentence(string(runes[start : index+1])); segment != "" {
				parts = append(parts, segment)
			}
			start = index + 1
		}
	}
	if force && start < len(runes) {
		if segment := normalizeSentence(string(runes[start:])); segment != "" {
			parts = append(parts, segment)
		}
		start = len(runes)
	}
	b.buffer.Reset()
	if start < len(runes) {
		b.buffer.WriteString(string(runes[start:]))
	}
	return parts
}

func isSentenceBoundary(r rune) bool {
	switch r {
	case '。', '！', '？', '!', '?', '\n':
		return true
	default:
		return false
	}
}

func normalizeSentence(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) {
		return ""
	}
	return value
}
