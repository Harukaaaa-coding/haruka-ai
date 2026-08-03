package tts

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

func TestNormalizeOptionsDefaultsAndValidation(t *testing.T) {
	got, err := normalizeOptions(SynthesisOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Voice != 4194 || got.Format != "mp3-16k" || got.Speed != 5 {
		t.Fatalf("unexpected defaults: %+v", got)
	}
	if _, err := normalizeOptions(SynthesisOptions{Format: "exe"}); err == nil {
		t.Fatal("expected invalid format error")
	}
}

func TestTTSOwnershipAndRedactedFlow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/create":
			fmt.Fprint(w, `{"task_id":"task-1"}`)
		case "/query":
			fmt.Fprint(w, `{"log_id":1,"tasks_info":[{"task_id":"task-1","task_status":"Success","task_result":{"speech_url":"https://example.invalid/audio.mp3"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service := newTTSService(staticToken("token"), server.Client(), server.URL+"/create", server.URL+"/query")
	taskID, err := service.CreateTTSForUser(context.Background(), "alice", "你好", DefaultSynthesisOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.QueryTTSForUser(context.Background(), "bob", taskID); err == nil {
		t.Fatal("expected cross-user query to fail")
	}
	result, err := service.QueryTTSForUser(context.Background(), "alice", taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.TasksInfo) != 1 || result.TasksInfo[0].TaskResult == nil {
		t.Fatalf("unexpected query result: %+v", result)
	}
}
