package tts

import (
	"GopherAI/common/voice"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	defaultCreateURL = "https://aip.baidubce.com/rpc/2.0/tts/v1/create"
	defaultQueryURL  = "https://aip.baidubce.com/rpc/2.0/tts/v1/query"
	maxResponseBytes = 2 << 20
	maxTextRunes     = 20000
)

type taskOwner struct {
	username  string
	createdAt time.Time
}

// TTSService is a concurrency-safe Baidu long-text synthesis client.
type TTSService struct {
	tokenProvider voice.TokenProvider
	client        *http.Client
	createURL     string
	queryURL      string

	ownerMu sync.RWMutex
	owners  map[string]taskOwner
}

func NewTTSService() *TTSService {
	return newTTSService(voice.DefaultBaiduTokenProvider(), &http.Client{Timeout: 30 * time.Second}, defaultCreateURL, defaultQueryURL)
}

func newTTSService(tokenProvider voice.TokenProvider, client *http.Client, createURL, queryURL string) *TTSService {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &TTSService{
		tokenProvider: tokenProvider,
		client:        client,
		createURL:     createURL,
		queryURL:      queryURL,
		owners:        make(map[string]taskOwner),
	}
}

type SynthesisOptions struct {
	Format         string `json:"format"`
	Voice          int    `json:"voice"`
	Language       string `json:"lang"`
	Speed          int    `json:"speed"`
	Pitch          int    `json:"pitch"`
	Volume         int    `json:"volume"`
	EnableSubtitle int    `json:"enable_subtitle"`
}

func DefaultSynthesisOptions() SynthesisOptions {
	return SynthesisOptions{
		Format:         "mp3-16k",
		Voice:          4194,
		Language:       "zh",
		Speed:          5,
		Pitch:          5,
		Volume:         5,
		EnableSubtitle: 0,
	}
}

func normalizeOptions(options SynthesisOptions) (SynthesisOptions, error) {
	defaults := DefaultSynthesisOptions()
	if strings.TrimSpace(options.Format) == "" {
		options.Format = defaults.Format
	}
	if options.Voice == 0 {
		options.Voice = defaults.Voice
	}
	if strings.TrimSpace(options.Language) == "" {
		options.Language = defaults.Language
	}
	if options.Speed == 0 {
		options.Speed = defaults.Speed
	}
	if options.Pitch == 0 {
		options.Pitch = defaults.Pitch
	}
	if options.Volume == 0 {
		options.Volume = defaults.Volume
	}
	if options.Format != "mp3-16k" && options.Format != "mp3-48k" && options.Format != "wav" {
		return SynthesisOptions{}, fmt.Errorf("unsupported TTS format")
	}
	if options.Voice < 1 || options.Speed < 0 || options.Speed > 15 || options.Pitch < 0 || options.Pitch > 15 || options.Volume < 0 || options.Volume > 15 {
		return SynthesisOptions{}, fmt.Errorf("TTS voice parameters are out of range")
	}
	if options.EnableSubtitle != 0 && options.EnableSubtitle != 1 {
		return SynthesisOptions{}, fmt.Errorf("enable_subtitle must be 0 or 1")
	}
	return options, nil
}

type TTSRequest struct {
	Text           string `json:"text"`
	Format         string `json:"format"`
	Voice          int    `json:"voice"`
	Lang           string `json:"lang"`
	Speed          int    `json:"speed"`
	Pitch          int    `json:"pitch"`
	Volume         int    `json:"volume"`
	EnableSubtitle int    `json:"enable_subtitle"`
}

type TTSCreateResponse struct {
	TaskID    string `json:"task_id"`
	ErrorCode int    `json:"error_code"`
	ErrorMsg  string `json:"error_msg"`
}

// CreateTTS preserves the original API for internal callers that do not need
// per-user task authorization.
func (s *TTSService) CreateTTS(ctx context.Context, text string) (string, error) {
	return s.CreateTTSForUser(ctx, "", text, DefaultSynthesisOptions())
}

func (s *TTSService) CreateTTSForUser(ctx context.Context, username, text string, options SynthesisOptions) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > maxTextRunes {
		return "", fmt.Errorf("TTS text is empty, invalid, or too long")
	}
	normalized, err := normalizeOptions(options)
	if err != nil {
		return "", err
	}
	accessToken, err := s.tokenProvider.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("get TTS access token: %w", err)
	}

	payload := TTSRequest{
		Text:           text,
		Format:         normalized.Format,
		Voice:          normalized.Voice,
		Lang:           normalized.Language,
		Speed:          normalized.Speed,
		Pitch:          normalized.Pitch,
		Volume:         normalized.Volume,
		EnableSubtitle: normalized.EnableSubtitle,
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode TTS request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, withAccessToken(s.createURL, accessToken), bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create TTS request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	respBody, err := s.doJSON(req)
	if err != nil {
		return "", err
	}
	var result TTSCreateResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("decode TTS create response: %w", err)
	}
	if result.TaskID == "" {
		if result.ErrorCode != 0 {
			return "", fmt.Errorf("TTS provider rejected request with code %d", result.ErrorCode)
		}
		return "", fmt.Errorf("TTS provider returned an empty task ID")
	}
	if username != "" {
		s.rememberOwner(result.TaskID, username)
	}
	return result.TaskID, nil
}

type TTSTaskResult struct {
	SpeechURL string `json:"speech_url,omitempty"`
}

type TTSTask struct {
	TaskID     string         `json:"task_id"`
	TaskStatus string         `json:"task_status"`
	TaskResult *TTSTaskResult `json:"task_result,omitempty"`
}

type TTSQueryResponse struct {
	LogID     string    `json:"log_id"`
	TasksInfo []TTSTask `json:"tasks_info"`
}

func (s *TTSService) QueryTTSFull(ctx context.Context, taskID string) (*TTSQueryResponse, error) {
	return s.QueryTTSForUser(ctx, "", taskID)
}

func (s *TTSService) QueryTTSForUser(ctx context.Context, username, taskID string) (*TTSQueryResponse, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, fmt.Errorf("task ID is required")
	}
	if username != "" && !s.isOwner(taskID, username) {
		return nil, fmt.Errorf("TTS task does not belong to current user")
	}
	accessToken, err := s.tokenProvider.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("get TTS access token: %w", err)
	}

	bodyBytes, err := json.Marshal(map[string][]string{"task_ids": {taskID}})
	if err != nil {
		return nil, fmt.Errorf("encode TTS query: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, withAccessToken(s.queryURL, accessToken), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create TTS query: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	respBody, err := s.doJSON(req)
	if err != nil {
		return nil, err
	}
	var rawResp struct {
		LogID     json.Number `json:"log_id"`
		ErrorCode int         `json:"error_code"`
		TasksInfo []struct {
			TaskID     string          `json:"task_id"`
			TaskStatus string          `json:"task_status"`
			TaskResult json.RawMessage `json:"task_result,omitempty"`
		} `json:"tasks_info"`
	}
	decoder := json.NewDecoder(bytes.NewReader(respBody))
	decoder.UseNumber()
	if err := decoder.Decode(&rawResp); err != nil {
		return nil, fmt.Errorf("decode TTS query response: %w", err)
	}
	if rawResp.ErrorCode != 0 {
		return nil, fmt.Errorf("TTS provider query failed with code %d", rawResp.ErrorCode)
	}

	result := &TTSQueryResponse{LogID: rawResp.LogID.String(), TasksInfo: make([]TTSTask, 0, len(rawResp.TasksInfo))}
	for _, upstreamTask := range rawResp.TasksInfo {
		task := TTSTask{TaskID: upstreamTask.TaskID, TaskStatus: upstreamTask.TaskStatus}
		if upstreamTask.TaskStatus == "Success" && len(upstreamTask.TaskResult) > 0 && string(upstreamTask.TaskResult) != "null" {
			var parsed TTSTaskResult
			if err := json.Unmarshal(upstreamTask.TaskResult, &parsed); err != nil {
				return nil, fmt.Errorf("decode TTS task result: %w", err)
			}
			if parsed.SpeechURL == "" {
				return nil, fmt.Errorf("TTS task succeeded without an audio URL")
			}
			task.TaskResult = &parsed
		}
		result.TasksInfo = append(result.TasksInfo, task)
	}
	return result, nil
}

func (s *TTSService) doJSON(req *http.Request) ([]byte, error) {
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request TTS provider: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read TTS provider response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("TTS provider response exceeded size limit")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("TTS provider returned HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func withAccessToken(baseURL, token string) string {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return baseURL
	}
	query := parsed.Query()
	query.Set("access_token", token)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func (s *TTSService) rememberOwner(taskID, username string) {
	now := time.Now()
	s.ownerMu.Lock()
	defer s.ownerMu.Unlock()
	for id, owner := range s.owners {
		if now.Sub(owner.createdAt) > 2*time.Hour {
			delete(s.owners, id)
		}
	}
	s.owners[taskID] = taskOwner{username: username, createdAt: now}
}

func (s *TTSService) isOwner(taskID, username string) bool {
	s.ownerMu.RLock()
	defer s.ownerMu.RUnlock()
	owner, ok := s.owners[taskID]
	return ok && owner.username == username && time.Since(owner.createdAt) <= 2*time.Hour
}
