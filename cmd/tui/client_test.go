package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientLoginAddsTokenToAuthenticatedRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/user/login":
			if request.Header.Get("Authorization") != "" {
				t.Fatal("login request unexpectedly carried an authorization header")
			}
			_, _ = writer.Write([]byte(`{"status_code":1000,"status_msg":"success","token":"test-token"}`))
		case "/api/v1/AI/chat/sessions":
			if request.Header.Get("Authorization") != "Bearer test-token" {
				t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
			}
			if got := request.URL.Query().Get("limit"); got != "50" {
				t.Fatalf("limit = %q", got)
			}
			// This intentionally omits pagination fields to cover older servers.
			_, _ = writer.Write([]byte(`{"status_code":1000,"status_msg":"success","sessions":[{"sessionId":"s1","name":"title"}]}`))
		default:
			t.Fatalf("unexpected path %q", request.URL.Path)
		}
	}))
	defer server.Close()

	client, err := newClient(server.URL, "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.login(context.Background(), "alice", "password"); err != nil {
		t.Fatal(err)
	}
	sessions, err := client.sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "s1" {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestClientSessionPageUsesCursorAndReadsPaginationMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/AI/chat/sessions" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if got := request.URL.Query().Get("limit"); got != "50" {
			t.Fatalf("limit = %q", got)
		}
		if got := request.URL.Query().Get("cursor"); got != "next page" {
			t.Fatalf("cursor = %q", got)
		}
		_, _ = writer.Write([]byte(`{"status_code":1000,"status_msg":"success","hasMore":true,"nextCursor":"cursor-2","sessions":[{"sessionId":"s2","name":"next"}]}`))
	}))
	defer server.Close()

	client, err := newClient(server.URL, "token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.sessionsPage(context.Background(), "  next page  ")
	if err != nil {
		t.Fatal(err)
	}
	if !page.HasMore || page.NextCursor != "cursor-2" || len(page.Sessions) != 1 || page.Sessions[0].ID != "s2" {
		t.Fatalf("page = %#v", page)
	}
}

func TestClientHistoryPageUsesBoundedBodyAndReadsLegacyMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/AI/chat/history" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		var body historyRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.SessionID != "session-1" || body.Limit != tuiPageSize || body.Cursor != "older" {
			t.Fatalf("body = %#v", body)
		}
		_, _ = writer.Write([]byte(`{"status_code":1000,"status_msg":"success","has_more":true,"next_cursor":"cursor-older","history":[{"is_user":true,"content":"hello"}]}`))
	}))
	defer server.Close()

	client, err := newClient(server.URL, "token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.historyPage(context.Background(), "session-1", " older ")
	if err != nil {
		t.Fatal(err)
	}
	if !page.HasMore || page.NextCursor != "cursor-older" || len(page.History) != 1 || !page.History[0].IsUser {
		t.Fatalf("page = %#v", page)
	}
}

func TestClientHistoryKeepsOldResponseCompatibility(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body historyRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.SessionID != "session-legacy" || body.Limit != tuiPageSize || body.Cursor != "" {
			t.Fatalf("body = %#v", body)
		}
		// No pagination fields: this is the response shape from pre-pagination
		// servers, which ignore the additional request fields.
		_, _ = writer.Write([]byte(`{"status_code":1000,"status_msg":"success","history":[{"is_user":false,"content":"answer"}]}`))
	}))
	defer server.Close()

	client, err := newClient(server.URL, "token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	history, err := client.history(context.Background(), "session-legacy")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].IsUser || history[0].Content != "answer" {
		t.Fatalf("history = %#v", history)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestClientRejectsChunkedOversizedResponse(t *testing.T) {
	client, err := newClient("https://example.test", "", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), int(maxAPIResponseBytes+1)))),
			ContentLength: -1,
		}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.live(context.Background()); err == nil {
		t.Fatal("live unexpectedly succeeded")
	} else if !strings.Contains(err.Error(), "exceeds 2 MiB limit") {
		t.Fatalf("live error = %v", err)
	}
}

func TestClientReturnsBusinessAndHTTPErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/AI/chat/sessions" {
			_, _ = writer.Write([]byte(`{"status_code":2007,"status_msg":"用户未登录"}`))
			return
		}
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"status_code":4001,"status_msg":"服务繁忙"}`))
	}))
	defer server.Close()

	client, err := newClient(server.URL, "token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.sessions(context.Background()); err == nil {
		t.Fatal("sessions unexpectedly succeeded")
	} else {
		var apiErr *apiError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 2007 {
			t.Fatalf("sessions error = %v", err)
		}
	}
	if _, err := client.live(context.Background()); err == nil {
		t.Fatal("live unexpectedly succeeded")
	} else {
		var apiErr *apiError
		if !errors.As(err, &apiErr) || apiErr.HTTPStatus != http.StatusServiceUnavailable || apiErr.StatusCode != 4001 {
			t.Fatalf("live error = %v", err)
		}
	}
}
