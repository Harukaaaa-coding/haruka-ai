package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPromptPasswordFallsBackWithoutEchoingTheValue(t *testing.T) {
	var output bytes.Buffer
	app := terminalApp{
		input:  bufio.NewReader(strings.NewReader("secret-password\n")),
		output: &output,
	}

	password, err := app.promptPassword("Password")
	if err != nil {
		t.Fatalf("promptPassword: %v", err)
	}
	if password != "secret-password" {
		t.Fatalf("password = %q", password)
	}
	if strings.Contains(output.String(), password) {
		t.Fatalf("fallback prompt echoed password: %q", output.String())
	}
}

func TestPromptPasswordAcceptsFinalPipedLine(t *testing.T) {
	app := terminalApp{
		input:  bufio.NewReader(strings.NewReader("final-password")),
		output: &bytes.Buffer{},
	}
	password, err := app.promptPassword("Password")
	if err != nil || password != "final-password" {
		t.Fatalf("promptPassword() = (%q, %v)", password, err)
	}
}

func TestShowSessionsLoadsMoreWithoutKeepingEarlierPages(t *testing.T) {
	var cursors []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/AI/chat/sessions" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if got := request.URL.Query().Get("limit"); got != "50" {
			t.Fatalf("limit = %q", got)
		}
		cursor := request.URL.Query().Get("cursor")
		cursors = append(cursors, cursor)
		switch cursor {
		case "":
			_, _ = writer.Write([]byte(`{"status_code":1000,"status_msg":"success","hasMore":true,"nextCursor":"sessions-2","sessions":[{"sessionId":"s1","name":"first"}]}`))
		case "sessions-2":
			_, _ = writer.Write([]byte(`{"status_code":1000,"status_msg":"success","hasMore":false,"sessions":[{"sessionId":"s2","name":"second"}]}`))
		default:
			t.Fatalf("unexpected cursor %q", cursor)
		}
	}))
	defer server.Close()

	client, err := newClient(server.URL, "token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	app := terminalApp{
		client: client,
		input:  bufio.NewReader(strings.NewReader("n\n")),
		output: &output,
	}
	lastPage := app.showSessions(context.Background())
	if len(lastPage) != 1 || lastPage[0].ID != "s2" {
		t.Fatalf("last page = %#v", lastPage)
	}
	if len(cursors) != 2 || cursors[0] != "" || cursors[1] != "sessions-2" {
		t.Fatalf("cursors = %#v", cursors)
	}
	if !strings.Contains(output.String(), "s1") || !strings.Contains(output.String(), "s2") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestChooseSessionCanLoadMoreThenSelect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		cursor := request.URL.Query().Get("cursor")
		if cursor == "" {
			_, _ = writer.Write([]byte(`{"status_code":1000,"status_msg":"success","hasMore":true,"nextCursor":"sessions-2","sessions":[{"sessionId":"s1","name":"first"}]}`))
			return
		}
		if cursor != "sessions-2" {
			t.Fatalf("cursor = %q", cursor)
		}
		_, _ = writer.Write([]byte(`{"status_code":1000,"status_msg":"success","sessions":[{"sessionId":"s2","name":"second"}]}`))
	}))
	defer server.Close()

	client, err := newClient(server.URL, "token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	app := terminalApp{
		client: client,
		input:  bufio.NewReader(strings.NewReader("n\n1\n")),
		output: &bytes.Buffer{},
	}
	sessionID, ok := app.chooseSession(context.Background())
	if !ok || sessionID != "s2" {
		t.Fatalf("chooseSession() = (%q, %t)", sessionID, ok)
	}
}

func TestShowHistoryLoadsOlderPageOnDemand(t *testing.T) {
	var historyCursors []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/AI/chat/sessions":
			_, _ = writer.Write([]byte(`{"status_code":1000,"status_msg":"success","sessions":[{"sessionId":"s1","name":"first"}]}`))
		case "/api/v1/AI/chat/history":
			var body historyRequest
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatalf("decode history request: %v", err)
			}
			if body.SessionID != "s1" || body.Limit != tuiPageSize {
				t.Fatalf("history request = %#v", body)
			}
			historyCursors = append(historyCursors, body.Cursor)
			switch body.Cursor {
			case "":
				_, _ = writer.Write([]byte(`{"status_code":1000,"status_msg":"success","hasMore":true,"nextCursor":"history-older","history":[{"is_user":true,"content":"new message"}]}`))
			case "history-older":
				_, _ = writer.Write([]byte(`{"status_code":1000,"status_msg":"success","history":[{"is_user":false,"content":"old message"}]}`))
			default:
				t.Fatalf("history cursor = %q", body.Cursor)
			}
		default:
			t.Fatalf("path = %q", request.URL.Path)
		}
	}))
	defer server.Close()

	client, err := newClient(server.URL, "token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	app := terminalApp{
		client: client,
		input:  bufio.NewReader(strings.NewReader("1\nn\n")),
		output: &output,
	}
	app.showHistory(context.Background())
	if len(historyCursors) != 2 || historyCursors[0] != "" || historyCursors[1] != "history-older" {
		t.Fatalf("history cursors = %#v", historyCursors)
	}
	if !strings.Contains(output.String(), "new message") || !strings.Contains(output.String(), "old message") {
		t.Fatalf("output = %q", output.String())
	}
}
