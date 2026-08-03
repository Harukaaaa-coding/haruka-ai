package asr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

func TestDetectFormat(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		contentType string
		want        string
	}{
		{name: "recording.wav", want: "wav"},
		{name: "recording", contentType: "audio/pcm", want: "pcm"},
		{name: "recording.m4a", want: "m4a"},
	} {
		got, err := DetectFormat(testCase.name, testCase.contentType)
		if err != nil || got != testCase.want {
			t.Fatalf("DetectFormat(%q, %q) = %q, %v", testCase.name, testCase.contentType, got, err)
		}
	}
	if _, err := DetectFormat("recording.webm", "audio/webm"); err == nil {
		t.Fatal("expected WebM to be rejected")
	}
}

func TestRecognizeWAV(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload providerRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Token != "token" || payload.Format != "wav" || payload.Rate != 16000 || payload.Length == 0 {
			t.Fatalf("unexpected request: %+v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"err_no":0,"result":["你好，GopherAI。"]}`)
	}))
	defer server.Close()

	service := newService(staticToken("token"), server.Client(), server.URL)
	wav := append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 44)...)
	text, err := service.Recognize(context.Background(), RecognitionRequest{Audio: wav, Format: "wav", Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if text != "你好，GopherAI。" {
		t.Fatalf("text = %q", text)
	}
}

func TestRecognizeRejectsInvalidWAV(t *testing.T) {
	service := newService(staticToken("token"), http.DefaultClient, "https://example.invalid")
	if _, err := service.Recognize(context.Background(), RecognitionRequest{Audio: []byte("not-a-wave"), Format: "wav"}); err == nil {
		t.Fatal("expected invalid WAV error")
	}
}
