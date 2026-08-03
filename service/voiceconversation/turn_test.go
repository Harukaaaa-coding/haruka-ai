package voiceconversation

import (
	"context"
	"errors"
	"testing"
	"time"
)

func pcmFrame(sample int16, count int) []byte {
	frame := make([]byte, count*2)
	for index := 0; index < count; index++ {
		frame[index*2] = byte(sample)
		frame[index*2+1] = byte(uint16(sample) >> 8)
	}
	return frame
}

func TestEnergyVADUsesStartAndEndHysteresis(t *testing.T) {
	detector := NewEnergyVAD(EnergyVADConfig{Threshold: 0.01, StartFrames: 2, EndFrames: 2})
	voice := pcmFrame(8000, 320)
	silence := pcmFrame(0, 320)

	if event, err := detector.ObservePCM16LE(voice); err != nil || event != VADNoTransition {
		t.Fatalf("first speech frame = %q, %v", event, err)
	}
	if event, err := detector.ObservePCM16LE(voice); err != nil || event != VADSpeechStart {
		t.Fatalf("second speech frame = %q, %v", event, err)
	}
	if event, err := detector.ObservePCM16LE(silence); err != nil || event != VADNoTransition {
		t.Fatalf("first silence frame = %q, %v", event, err)
	}
	if event, err := detector.ObservePCM16LE(silence); err != nil || event != VADSpeechEnd {
		t.Fatalf("second silence frame = %q, %v", event, err)
	}
}

func TestTurnBoundsAndCommit(t *testing.T) {
	turn := NewTurn(context.Background(), "turn-1", NewEnergyVAD(EnergyVADConfig{Threshold: 0.01, StartFrames: 1, EndFrames: 1}))
	frame := pcmFrame(1000, 320)
	if event, err := turn.AppendPCM16LE(frame); err != nil || event != VADSpeechStart {
		t.Fatalf("AppendPCM16LE() = %q, %v", event, err)
	}
	audio, err := turn.Commit()
	if err != nil {
		t.Fatalf("Commit(): %v", err)
	}
	if len(audio) != len(frame) {
		t.Fatalf("audio length = %d, want %d", len(audio), len(frame))
	}
	audio[0] = 0
	if _, err := turn.Commit(); !errors.Is(err, ErrTurnAlreadyCommitted) {
		t.Fatalf("second Commit() error = %v, want ErrTurnAlreadyCommitted", err)
	}
	if _, err := turn.AppendPCM16LE(frame); !errors.Is(err, ErrTurnAlreadyCommitted) {
		t.Fatalf("append after commit error = %v, want ErrTurnAlreadyCommitted", err)
	}
}

func TestPCM16LEToWAV(t *testing.T) {
	pcm := pcmFrame(42, 8)
	wav, err := PCM16LEToWAV(pcm, 16000)
	if err != nil {
		t.Fatalf("PCM16LEToWAV(): %v", err)
	}
	if got, want := string(wav[:4]), "RIFF"; got != want {
		t.Fatalf("header = %q, want %q", got, want)
	}
	if got, want := string(wav[8:12]), "WAVE"; got != want {
		t.Fatalf("format = %q, want %q", got, want)
	}
	if got, want := len(wav), 44+len(pcm); got != want {
		t.Fatalf("wav length = %d, want %d", got, want)
	}
}

func TestSentenceBufferKeepsIncompletePhraseUntilBoundary(t *testing.T) {
	buffer := NewSentenceBuffer(100)
	if parts := buffer.Push("第一句"); len(parts) != 0 {
		t.Fatalf("parts before punctuation = %#v", parts)
	}
	parts := buffer.Push("完成。第二句")
	if got, want := len(parts), 1; got != want || parts[0] != "第一句完成。" {
		t.Fatalf("parts = %#v", parts)
	}
	parts = buffer.Flush()
	if got, want := len(parts), 1; got != want || parts[0] != "第二句" {
		t.Fatalf("flush parts = %#v", parts)
	}
}

func TestHubDrainsAndWaits(t *testing.T) {
	hub := NewHub(2, 1)
	ctx, cancel := context.WithCancel(context.Background())
	release, err := hub.Register("alice", cancel)
	if err != nil {
		t.Fatalf("Register(): %v", err)
	}
	hub.BeginDrain()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("drain did not cancel active connection")
	}
	if _, err := hub.Register("bob", func() {}); !errors.Is(err, ErrHubDraining) {
		t.Fatalf("Register while draining = %v, want ErrHubDraining", err)
	}
	release()
	if err := hub.Wait(context.Background()); err != nil {
		t.Fatalf("Wait(): %v", err)
	}
}
