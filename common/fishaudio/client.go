// Package fishaudio provides a small, dependency-free client for the Fish
// Audio text-to-speech HTTP API.
//
// The client deliberately accepts credentials in Config rather than reading
// process environment variables. This makes provider selection explicit at
// the application boundary and keeps credentials out of requests made by
// browsers.
package fishaudio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// DefaultBaseURL is the Fish Audio API root. BaseURL values supplied in
	// Config are also roots; the client appends /v1/tts itself.
	DefaultBaseURL = "https://api.fish.audio"
	DefaultModel   = "s2-pro"

	DefaultFormat  = "mp3"
	DefaultLatency = "normal"

	DefaultSpeed       = 1.0
	DefaultVolume      = 0.0
	DefaultTemperature = 0.7
	DefaultTopP        = 0.7

	// MaxTextRunes bounds memory use and prevents accidental use of the
	// batch endpoint for an unbounded transcript.
	MaxTextRunes = 20000

	// DefaultMaxResponseBytes limits a complete synthesized audio response.
	// Streaming callers should use Fish Audio's WebSocket endpoint through a
	// separate adapter instead of increasing this limit indefinitely.
	DefaultMaxResponseBytes int64 = 32 << 20
)

// Config contains all provider-specific values required by Client. Values are
// supplied by the server-side caller and are never taken from an HTTP request.
type Config struct {
	APIKey      string
	BaseURL     string
	Model       string
	ReferenceID string

	// HTTPClient is optional. A client with a bounded timeout is used when it
	// is nil. Supplying a client is useful for observability and tests.
	HTTPClient *http.Client

	// MaxResponseBytes is optional. A positive value overrides
	// DefaultMaxResponseBytes.
	MaxResponseBytes int64
}

// Client is safe for concurrent use after construction.
type Client struct {
	apiKey           string
	model            string
	referenceID      string
	endpoint         *url.URL
	httpClient       *http.Client
	maxResponseBytes int64
}

// Request is a single Fish Audio TTS request. Text may include Fish Audio
// control tags (for example, "[warm] hello") and is passed through unchanged
// after whitespace validation.
type Request struct {
	Text    string
	Options Options
}

// Options controls speech prosody and generation. Pointer-valued numeric
// fields distinguish an omitted value from a valid zero such as
// Temperature: Float64(0). Nil fields use Fish Audio-compatible defaults.
type Options struct {
	Speed       *float64
	Volume      *float64
	Temperature *float64
	TopP        *float64
	Format      string
	Latency     string
}

// Float64 returns a pointer suitable for an optional numeric Options field.
func Float64(value float64) *float64 {
	return &value
}

// DefaultOptions returns explicit default values suitable for callers that
// prefer to modify a value in place.
func DefaultOptions() Options {
	return Options{
		Speed:       Float64(DefaultSpeed),
		Volume:      Float64(DefaultVolume),
		Temperature: Float64(DefaultTemperature),
		TopP:        Float64(DefaultTopP),
		Format:      DefaultFormat,
		Latency:     DefaultLatency,
	}
}

// Response contains synthesized audio. The response is fully buffered and
// bounded by Config.MaxResponseBytes.
type Response struct {
	Audio       []byte
	ContentType string
	Format      string
}

type normalizedOptions struct {
	speed       float64
	volume      float64
	temperature float64
	topP        float64
	format      string
	latency     string
}

type providerRequest struct {
	Text        string  `json:"text"`
	ReferenceID string  `json:"reference_id"`
	Temperature float64 `json:"temperature"`
	TopP        float64 `json:"top_p"`
	Prosody     prosody `json:"prosody"`
	Format      string  `json:"format"`
	Latency     string  `json:"latency"`
}

type prosody struct {
	Speed  float64 `json:"speed"`
	Volume float64 `json:"volume"`
}

// NewClient validates configuration and returns a Fish Audio S2 TTS client.
// It intentionally returns generic configuration errors rather than echoing
// supplied URLs or credentials, so an API key cannot appear in logs.
func NewClient(cfg Config) (*Client, error) {
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" || strings.ContainsAny(apiKey, "\r\n") {
		return nil, errors.New("fish audio API key is required")
	}

	baseURL, err := parseBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}

	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = DefaultModel
	}
	if !validHeaderValue(model) {
		return nil, errors.New("fish audio model is invalid")
	}

	referenceID := strings.TrimSpace(cfg.ReferenceID)
	if referenceID == "" || !utf8.ValidString(referenceID) || utf8.RuneCountInString(referenceID) > 256 || containsControlCharacter(referenceID) {
		return nil, errors.New("fish audio reference ID is invalid")
	}

	maxResponseBytes := cfg.MaxResponseBytes
	if maxResponseBytes == 0 {
		maxResponseBytes = DefaultMaxResponseBytes
	}
	if maxResponseBytes < 1 {
		return nil, errors.New("fish audio response size limit must be positive")
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	return &Client{
		apiKey:           apiKey,
		model:            model,
		referenceID:      referenceID,
		endpoint:         baseURL,
		httpClient:       httpClient,
		maxResponseBytes: maxResponseBytes,
	}, nil
}

func parseBaseURL(rawBaseURL string) (*url.URL, error) {
	baseURL := strings.TrimSpace(rawBaseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("fish audio base URL is invalid")
	}

	// Config.BaseURL is an API root rather than an endpoint. Preserving an
	// optional path makes private compatible gateways possible.
	parsed.Path = path.Join(parsed.Path, "v1", "tts")
	parsed.RawPath = ""
	return parsed, nil
}

// Synthesize sends a bounded, non-streaming TTS request to Fish Audio's
// /v1/tts endpoint. Provider response bodies are never included in errors,
// which prevents accidental leakage of provider details or credentials.
func (c *Client) Synthesize(ctx context.Context, request Request) (Response, error) {
	if c == nil || c.httpClient == nil || c.endpoint == nil {
		return Response{}, errors.New("fish audio client is not configured")
	}
	if ctx == nil {
		return Response{}, errors.New("fish audio context is required")
	}

	text, err := normalizeText(request.Text)
	if err != nil {
		return Response{}, err
	}
	options, err := normalizeOptions(request.Options)
	if err != nil {
		return Response{}, err
	}

	body, err := json.Marshal(providerRequest{
		Text:        text,
		ReferenceID: c.referenceID,
		Temperature: options.temperature,
		TopP:        options.topP,
		Prosody: prosody{
			Speed:  options.speed,
			Volume: options.volume,
		},
		Format:  options.format,
		Latency: options.latency,
	})
	if err != nil {
		// This should not be reachable for the primitive request shape, but
		// keep a safe error boundary if the shape changes later.
		return Response{}, errors.New("fish audio request could not be encoded")
	}

	endpoint := *c.endpoint
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return Response{}, errors.New("fish audio request could not be created")
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "audio/*")
	httpRequest.Header.Set("model", c.model)

	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return Response{}, normalizeRequestError(err)
	}
	defer response.Body.Close()

	if response.ContentLength > c.maxResponseBytes {
		return Response{}, fmt.Errorf("fish audio response exceeded %d byte limit", c.maxResponseBytes)
	}

	audio, err := io.ReadAll(io.LimitReader(response.Body, c.maxResponseBytes+1))
	if err != nil {
		return Response{}, errors.New("fish audio response could not be read")
	}
	if int64(len(audio)) > c.maxResponseBytes {
		return Response{}, fmt.Errorf("fish audio response exceeded %d byte limit", c.maxResponseBytes)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Response{}, &HTTPError{StatusCode: response.StatusCode}
	}
	if len(audio) == 0 {
		return Response{}, errors.New("fish audio returned empty audio")
	}

	return Response{
		Audio:       audio,
		ContentType: mediaType(response.Header.Get("Content-Type")),
		Format:      options.format,
	}, nil
}

// HTTPError reports only a response status. It intentionally does not retain
// the provider response body, which can contain sensitive diagnostic data.
type HTTPError struct {
	StatusCode int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("fish audio TTS provider returned HTTP %d", e.StatusCode)
}

func normalizeText(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > MaxTextRunes {
		return "", errors.New("fish audio text is empty, invalid, or too long")
	}
	return text, nil
}

func normalizeOptions(options Options) (normalizedOptions, error) {
	normalized := normalizedOptions{
		speed:       DefaultSpeed,
		volume:      DefaultVolume,
		temperature: DefaultTemperature,
		topP:        DefaultTopP,
		format:      DefaultFormat,
		latency:     DefaultLatency,
	}
	if options.Speed != nil {
		normalized.speed = *options.Speed
	}
	if options.Volume != nil {
		normalized.volume = *options.Volume
	}
	if options.Temperature != nil {
		normalized.temperature = *options.Temperature
	}
	if options.TopP != nil {
		normalized.topP = *options.TopP
	}
	if strings.TrimSpace(options.Format) != "" {
		normalized.format = strings.ToLower(strings.TrimSpace(options.Format))
	}
	if strings.TrimSpace(options.Latency) != "" {
		normalized.latency = strings.ToLower(strings.TrimSpace(options.Latency))
	}

	if !inRange(normalized.speed, 0.5, 2.0) {
		return normalizedOptions{}, errors.New("fish audio speed must be between 0.5 and 2.0")
	}
	if !inRange(normalized.volume, -20, 20) {
		return normalizedOptions{}, errors.New("fish audio volume must be between -20 and 20")
	}
	if !inRange(normalized.temperature, 0, 1) {
		return normalizedOptions{}, errors.New("fish audio temperature must be between 0 and 1")
	}
	if !inRange(normalized.topP, 0, 1) {
		return normalizedOptions{}, errors.New("fish audio top_p must be between 0 and 1")
	}
	switch normalized.format {
	case "wav", "pcm", "mp3", "opus":
	default:
		return normalizedOptions{}, errors.New("fish audio format must be wav, pcm, mp3, or opus")
	}
	switch normalized.latency {
	case "low", "normal", "balanced":
	default:
		return normalizedOptions{}, errors.New("fish audio latency must be low, normal, or balanced")
	}
	return normalized, nil
}

func inRange(value, lower, upper float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= lower && value <= upper
}

func normalizeRequestError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("fish audio TTS request canceled: %w", context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("fish audio TTS request timed out: %w", context.DeadlineExceeded)
	default:
		// Do not wrap net/url errors. Their text can include a configured URL,
		// and a malformed configuration could otherwise expose a secret.
		return errors.New("fish audio TTS request failed")
	}
}

func validHeaderValue(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func containsControlCharacter(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func mediaType(value string) string {
	if separator := strings.IndexByte(value, ';'); separator >= 0 {
		value = value[:separator]
	}
	return strings.TrimSpace(value)
}
