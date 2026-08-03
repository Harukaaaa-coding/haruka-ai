package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	successCode         int64 = 1000
	tuiPageSize               = 50
	maxAPIResponseBytes int64 = 2 << 20
)

type apiError struct {
	HTTPStatus int
	StatusCode int64
	Message    string
}

func (err *apiError) Error() string {
	if err.StatusCode != 0 {
		return fmt.Sprintf("API error %d: %s", err.StatusCode, err.Message)
	}
	return fmt.Sprintf("HTTP error %d: %s", err.HTTPStatus, err.Message)
}

type client struct {
	serverURL  string
	httpClient *http.Client
	token      string
}

type responseMeta struct {
	StatusCode int64  `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
}

type healthComponent struct {
	Status    string `json:"status"`
	Required  bool   `json:"required"`
	LatencyMS int64  `json:"latency_ms"`
	ErrorCode string `json:"error_code"`
}

type healthReport struct {
	Status string                     `json:"status"`
	Phase  string                     `json:"phase"`
	Checks map[string]healthComponent `json:"checks"`
}

type loginResponse struct {
	responseMeta
	Token string `json:"token"`
}

type sessionInfo struct {
	ID    string `json:"sessionId"`
	Title string `json:"name"`
}

// paginationMetadata accepts both the current camelCase fields and the
// snake_case variants used by some older API deployments.
type paginationMetadata struct {
	HasMore       *bool  `json:"hasMore"`
	NextCursor    string `json:"nextCursor"`
	LegacyHasMore *bool  `json:"has_more"`
	LegacyCursor  string `json:"next_cursor"`
}

func (metadata paginationMetadata) pageInfo() (hasMore bool, nextCursor string) {
	if metadata.HasMore != nil {
		hasMore = *metadata.HasMore
	} else if metadata.LegacyHasMore != nil {
		hasMore = *metadata.LegacyHasMore
	}
	nextCursor = strings.TrimSpace(metadata.NextCursor)
	if nextCursor == "" {
		nextCursor = strings.TrimSpace(metadata.LegacyCursor)
	}
	return hasMore, nextCursor
}

type sessionsResponse struct {
	responseMeta
	paginationMetadata
	Sessions []sessionInfo `json:"sessions"`
}

type sessionsPage struct {
	Sessions   []sessionInfo
	HasMore    bool
	NextCursor string
}

type catalogModel struct {
	ID           string `json:"id"`
	DisplayName  string `json:"displayName"`
	Available    bool   `json:"available"`
	Capabilities struct {
		Chat bool `json:"chat"`
	} `json:"capabilities"`
}

type modelsResponse struct {
	responseMeta
	Models []catalogModel `json:"models"`
}

type chatResponse struct {
	responseMeta
	SessionID string `json:"sessionId"`
	Content   string `json:"Information"`
}

type historyItem struct {
	IsUser  bool   `json:"is_user"`
	Content string `json:"content"`
}

type historyResponse struct {
	responseMeta
	paginationMetadata
	History []historyItem `json:"history"`
}

type historyPage struct {
	History    []historyItem
	HasMore    bool
	NextCursor string
}

type historyRequest struct {
	SessionID string `json:"sessionId"`
	Limit     int    `json:"limit"`
	Cursor    string `json:"cursor,omitempty"`
}

func newClient(serverURL, token string, httpClient *http.Client) (*client, error) {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	parsed, err := url.Parse(serverURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("server must be an absolute HTTP(S) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("server URL must use http or https")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &client{serverURL: serverURL, token: strings.TrimSpace(token), httpClient: httpClient}, nil
}

func (client *client) live(ctx context.Context) (healthReport, error) {
	var report healthReport
	return report, client.request(ctx, http.MethodGet, "/livez", nil, false, false, &report)
}

func (client *client) ready(ctx context.Context) (healthReport, error) {
	var report healthReport
	return report, client.request(ctx, http.MethodGet, "/readyz", nil, false, false, &report)
}

func (client *client) login(ctx context.Context, username, password string) error {
	var response loginResponse
	err := client.request(ctx, http.MethodPost, "/api/v1/user/login", map[string]string{
		"username": username,
		"password": password,
	}, false, true, &response)
	if err != nil {
		return err
	}
	if strings.TrimSpace(response.Token) == "" {
		return errors.New("login succeeded without a token")
	}
	client.token = response.Token
	return nil
}

func (client *client) sessions(ctx context.Context) ([]sessionInfo, error) {
	page, err := client.sessionsPage(ctx, "")
	return page.Sessions, err
}

// sessionsPage loads one bounded page. Calling sessions keeps older TUI code
// compatible while new callers can follow NextCursor deliberately.
func (client *client) sessionsPage(ctx context.Context, cursor string) (sessionsPage, error) {
	var response sessionsResponse
	err := client.request(ctx, http.MethodGet, pagedPath("/api/v1/AI/chat/sessions", cursor), nil, true, true, &response)
	hasMore, nextCursor := response.pageInfo()
	return sessionsPage{
		Sessions:   response.Sessions,
		HasMore:    hasMore,
		NextCursor: nextCursor,
	}, err
}

func (client *client) models(ctx context.Context) ([]catalogModel, error) {
	var response modelsResponse
	err := client.request(ctx, http.MethodGet, "/api/v1/AI/models", nil, true, true, &response)
	return response.Models, err
}

func (client *client) createChat(ctx context.Context, question, modelID string) (chatResponse, error) {
	var response chatResponse
	err := client.request(ctx, http.MethodPost, "/api/v1/AI/chat/send-new-session", map[string]string{
		"question": question,
		"modelId":  modelID,
	}, true, true, &response)
	return response, err
}

func (client *client) sendChat(ctx context.Context, sessionID, question, modelID string) (chatResponse, error) {
	var response chatResponse
	err := client.request(ctx, http.MethodPost, "/api/v1/AI/chat/send", map[string]string{
		"sessionId": sessionID,
		"question":  question,
		"modelId":   modelID,
	}, true, true, &response)
	return response, err
}

func (client *client) history(ctx context.Context, sessionID string) ([]historyItem, error) {
	page, err := client.historyPage(ctx, sessionID, "")
	return page.History, err
}

// historyPage returns the newest bounded history page. Later pages are older
// messages and can be fetched with NextCursor when the UI chooses to expose a
// "load more" action.
func (client *client) historyPage(ctx context.Context, sessionID, cursor string) (historyPage, error) {
	var response historyResponse
	err := client.request(ctx, http.MethodPost, "/api/v1/AI/chat/history", historyRequest{
		SessionID: sessionID,
		Limit:     tuiPageSize,
		Cursor:    strings.TrimSpace(cursor),
	}, true, true, &response)
	hasMore, nextCursor := response.pageInfo()
	return historyPage{
		History:    response.History,
		HasMore:    hasMore,
		NextCursor: nextCursor,
	}, err
}

func pagedPath(path, cursor string) string {
	query := url.Values{}
	query.Set("limit", strconv.Itoa(tuiPageSize))
	if cursor = strings.TrimSpace(cursor); cursor != "" {
		query.Set("cursor", cursor)
	}
	return path + "?" + query.Encode()
}

func (client *client) request(ctx context.Context, method, path string, body any, authenticated, requireSuccess bool, output any) error {
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		requestBody = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, client.serverURL+path, requestBody)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if authenticated {
		if client.token == "" {
			return errors.New("login is required")
		}
		request.Header.Set("Authorization", "Bearer "+client.token)
	}

	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer response.Body.Close()
	if response.ContentLength > maxAPIResponseBytes {
		return fmt.Errorf("response body exceeds %d MiB limit", maxAPIResponseBytes>>20)
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if int64(len(encoded)) > maxAPIResponseBytes {
		return fmt.Errorf("response body exceeds %d MiB limit", maxAPIResponseBytes>>20)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return responseError(response.StatusCode, encoded)
	}
	if output == nil || len(encoded) == 0 {
		return nil
	}
	if err := json.Unmarshal(encoded, output); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if !requireSuccess {
		return nil
	}
	var meta responseMeta
	if err := json.Unmarshal(encoded, &meta); err != nil {
		return fmt.Errorf("decode response status: %w", err)
	}
	if meta.StatusCode != successCode {
		return &apiError{HTTPStatus: response.StatusCode, StatusCode: meta.StatusCode, Message: meta.StatusMsg}
	}
	return nil
}

func responseError(status int, encoded []byte) error {
	var meta responseMeta
	if err := json.Unmarshal(encoded, &meta); err == nil && meta.StatusMsg != "" {
		return &apiError{HTTPStatus: status, StatusCode: meta.StatusCode, Message: meta.StatusMsg}
	}
	message := strings.TrimSpace(string(encoded))
	if message == "" {
		message = http.StatusText(status)
	}
	return &apiError{HTTPStatus: status, Message: message}
}
