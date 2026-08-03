package voice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"GopherAI/config"
)

const defaultBaiduTokenURL = "https://aip.baidubce.com/oauth/2.0/token"

// TokenProvider supplies short-lived provider credentials without exposing
// API keys to callers or logs.
type TokenProvider interface {
	Token(ctx context.Context) (string, error)
}

// BaiduTokenProvider caches Baidu access tokens and refreshes them shortly
// before expiration. It is safe for concurrent use.
type BaiduTokenProvider struct {
	apiKey    string
	secretKey string
	client    *http.Client
	tokenURL  string

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func NewBaiduTokenProvider(apiKey, secretKey string, client *http.Client) *BaiduTokenProvider {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &BaiduTokenProvider{
		apiKey:    strings.TrimSpace(apiKey),
		secretKey: strings.TrimSpace(secretKey),
		client:    client,
		tokenURL:  defaultBaiduTokenURL,
	}
}

var (
	defaultProviderOnce sync.Once
	defaultProvider     *BaiduTokenProvider
)

// DefaultBaiduTokenProvider shares one cache between ASR and TTS.
func DefaultBaiduTokenProvider() *BaiduTokenProvider {
	defaultProviderOnce.Do(func() {
		cfg := config.GetConfig().VoiceServiceConfig
		defaultProvider = NewBaiduTokenProvider(cfg.VoiceServiceApiKey, cfg.VoiceServiceSecretKey, nil)
	})
	return defaultProvider
}

func (p *BaiduTokenProvider) Token(ctx context.Context) (string, error) {
	if p == nil || p.apiKey == "" || p.secretKey == "" {
		return "", fmt.Errorf("baidu voice credentials are not configured")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.token != "" && time.Until(p.expiresAt) > 5*time.Minute {
		return p.token, nil
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", p.apiKey)
	form.Set("client_secret", p.secretKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("create baidu token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request baidu access token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read baidu token response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("baidu token service returned HTTP %d", resp.StatusCode)
	}

	var payload struct {
		AccessToken      string `json:"access_token"`
		ExpiresIn        int64  `json:"expires_in"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode baidu token response: %w", err)
	}
	if payload.AccessToken == "" {
		if payload.Error != "" {
			return "", fmt.Errorf("baidu token service rejected credentials: %s", payload.Error)
		}
		return "", fmt.Errorf("baidu token response did not contain an access token")
	}

	expiresIn := time.Duration(payload.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = 24 * time.Hour
	}
	p.token = payload.AccessToken
	p.expiresAt = time.Now().Add(expiresIn)
	return p.token, nil
}

// SetTokenURLForTest allows package tests to use an httptest server without
// weakening production TLS settings.
func (p *BaiduTokenProvider) SetTokenURLForTest(rawURL string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokenURL = rawURL
	p.token = ""
	p.expiresAt = time.Time{}
}
