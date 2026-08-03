package session

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"GopherAI/common/code"
	sessionservice "GopherAI/service/session"

	"github.com/gin-gonic/gin"
)

type deadlineTrackingWriter struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
	flushes   int
}

type failingSSEWriter struct {
	header http.Header
	err    error
}

func (w *failingSSEWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *failingSSEWriter) WriteHeader(int) {}

func (w *failingSSEWriter) Write([]byte) (int, error) { return 0, w.err }

func (w *deadlineTrackingWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

func (w *deadlineTrackingWriter) Flush() {
	w.flushes++
	w.ResponseRecorder.Flush()
}

func TestSSEStreamWriterBoundsAndClearsEachFrameDeadline(t *testing.T) {
	recorder := &deadlineTrackingWriter{ResponseRecorder: httptest.NewRecorder()}
	writer := newSSEStreamWriter(context.Background(), nil, recorder)
	if err := writeSSETo(writer, map[string]string{"content": "hello"}); err != nil {
		t.Fatalf("writeSSETo() error = %v", err)
	}
	if !strings.Contains(recorder.Body.String(), `"content":"hello"`) || recorder.flushes != 1 {
		t.Fatalf("unexpected SSE output: body=%q flushes=%d", recorder.Body.String(), recorder.flushes)
	}
	if len(recorder.deadlines) < 3 {
		t.Fatalf("deadline calls = %#v, want write + flush + clear", recorder.deadlines)
	}
	if recorder.deadlines[0].IsZero() || recorder.deadlines[len(recorder.deadlines)-1].IsZero() == false {
		t.Fatalf("deadline sequence = %#v, want active deadline followed by clear", recorder.deadlines)
	}
}

func TestSSEStreamWriterStopsAfterRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	recorder := &deadlineTrackingWriter{ResponseRecorder: httptest.NewRecorder()}
	writer := newSSEStreamWriter(ctx, nil, recorder)
	if _, err := writer.Write([]byte("data: ignored\n\n")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Write() error = %v, want canceled context", err)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("canceled writer wrote data: %q", recorder.Body.String())
	}
}

func TestSSEStreamWriterCancelsModelContextAfterClientWriteFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientGone := errors.New("client connection closed")
	writer := newSSEStreamWriter(ctx, cancel, &failingSSEWriter{err: clientGone})

	if _, err := writer.Write([]byte("data: ignored\n\n")); !errors.Is(err, clientGone) {
		t.Fatalf("Write() error = %v, want client write error", err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("stream context error = %v, want cancellation", ctx.Err())
	}
}

func TestChatStreamSendUsesBoundedContextAndSSEWriter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := chatStreamSendWithOptions
	t.Cleanup(func() { chatStreamSendWithOptions = previous })
	chatStreamSendWithOptions = func(
		ctx context.Context,
		_ string,
		_ string,
		_ string,
		_ sessionservice.ChatOptions,
		writer http.ResponseWriter,
	) code.Code {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("stream service did not receive a total deadline")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > maxSSEStreamDuration {
			t.Fatalf("stream deadline remaining = %s", remaining)
		}
		if _, ok := writer.(*sseStreamWriter); !ok {
			t.Fatalf("stream writer type = %T, want bounded SSE writer", writer)
		}
		if err := writeSSETo(writer, map[string]string{"content": "stream chunk"}); err != nil {
			t.Fatalf("write stream chunk: %v", err)
		}
		return code.CodeSuccess
	}

	engine := gin.New()
	engine.POST("/", func(c *gin.Context) {
		c.Set("userName", "alice")
		ChatStreamSend(c)
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"question":"hello","modelId":"4","sessionId":"session"}`))
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, request)
	if !strings.Contains(recorder.Body.String(), `"content":"stream chunk"`) {
		t.Fatalf("stream body = %q", recorder.Body.String())
	}
}

func TestChatStreamSendSkipsCanceledRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := chatStreamSendWithOptions
	t.Cleanup(func() { chatStreamSendWithOptions = previous })
	called := false
	chatStreamSendWithOptions = func(context.Context, string, string, string, sessionservice.ChatOptions, http.ResponseWriter) code.Code {
		called = true
		return code.CodeSuccess
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	engine := gin.New()
	engine.POST("/", func(c *gin.Context) {
		c.Set("userName", "alice")
		ChatStreamSend(c)
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"question":"hello","modelId":"4","sessionId":"session"}`)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, request)
	if called {
		t.Fatal("canceled request entered stream service")
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("canceled request wrote a stream body: %q", recorder.Body.String())
	}
}
