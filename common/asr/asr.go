package asr

import (
	"GopherAI/common/voice"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultEndpoint = "https://vop.baidu.com/server_api"
	MaxAudioBytes   = 10 << 20
	maxResponseSize = 2 << 20
)

type Service struct {
	tokenProvider voice.TokenProvider
	client        *http.Client
	endpoint      string
}

func NewService() *Service {
	return newService(voice.DefaultBaiduTokenProvider(), &http.Client{Timeout: 30 * time.Second}, defaultEndpoint)
}

func newService(tokenProvider voice.TokenProvider, client *http.Client, endpoint string) *Service {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Service{tokenProvider: tokenProvider, client: client, endpoint: endpoint}
}

type RecognitionRequest struct {
	Audio      []byte
	Format     string
	SampleRate int
	Username   string
}

type providerRequest struct {
	Format  string `json:"format"`
	Rate    int    `json:"rate"`
	Channel int    `json:"channel"`
	CUID    string `json:"cuid"`
	Token   string `json:"token"`
	Speech  string `json:"speech"`
	Length  int    `json:"len"`
	DevPID  int    `json:"dev_pid"`
}

type providerResponse struct {
	ErrorNumber int      `json:"err_no"`
	ErrorMsg    string   `json:"err_msg"`
	Result      []string `json:"result"`
}

func DetectFormat(filename, contentType string) (string, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".wav":
		return "wav", nil
	case ".pcm":
		return "pcm", nil
	case ".m4a":
		return "m4a", nil
	case ".amr":
		return "amr", nil
	}
	switch strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0])) {
	case "audio/wav", "audio/wave", "audio/x-wav":
		return "wav", nil
	case "audio/pcm", "audio/l16":
		return "pcm", nil
	case "audio/mp4", "audio/x-m4a":
		return "m4a", nil
	case "audio/amr":
		return "amr", nil
	default:
		return "", fmt.Errorf("unsupported audio format; use WAV, PCM, M4A, or AMR")
	}
}

func (s *Service) Recognize(ctx context.Context, input RecognitionRequest) (string, error) {
	if len(input.Audio) == 0 || len(input.Audio) > MaxAudioBytes {
		return "", fmt.Errorf("audio is empty or exceeds %d bytes", MaxAudioBytes)
	}
	format := strings.ToLower(strings.TrimSpace(input.Format))
	if format != "wav" && format != "pcm" && format != "m4a" && format != "amr" {
		return "", fmt.Errorf("unsupported ASR audio format")
	}
	if format == "wav" && (len(input.Audio) < 12 || string(input.Audio[:4]) != "RIFF" || string(input.Audio[8:12]) != "WAVE") {
		return "", fmt.Errorf("invalid WAV header")
	}
	rate := input.SampleRate
	if rate == 0 {
		rate = 16000
	}
	if rate != 8000 && rate != 16000 {
		return "", fmt.Errorf("ASR sample rate must be 8000 or 16000 Hz")
	}

	token, err := s.tokenProvider.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("get ASR access token: %w", err)
	}
	payload := providerRequest{
		Format:  format,
		Rate:    rate,
		Channel: 1,
		CUID:    anonymousCUID(input.Username),
		Token:   token,
		Speech:  base64.StdEncoding.EncodeToString(input.Audio),
		Length:  len(input.Audio),
		DevPID:  1537,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode ASR request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create ASR request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request ASR provider: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return "", fmt.Errorf("read ASR response: %w", err)
	}
	if len(responseBody) > maxResponseSize {
		return "", fmt.Errorf("ASR response exceeded size limit")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("ASR provider returned HTTP %d", resp.StatusCode)
	}
	var result providerResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return "", fmt.Errorf("decode ASR response: %w", err)
	}
	if result.ErrorNumber != 0 {
		return "", fmt.Errorf("ASR provider rejected request with code %d", result.ErrorNumber)
	}
	if len(result.Result) == 0 || strings.TrimSpace(result.Result[0]) == "" || !utf8.ValidString(result.Result[0]) {
		return "", fmt.Errorf("ASR provider returned no transcription")
	}
	return strings.TrimSpace(strings.Join(result.Result, " ")), nil
}

func anonymousCUID(username string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(username)))
	return "gopherai-" + hex.EncodeToString(digest[:8])
}
