package session

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

const (
	maxSSEStreamDuration = 5 * time.Minute
	sseWriteTimeout      = 15 * time.Second
)

// sseStreamWriter places a deadline around every streaming write while
// preserving the http.ResponseWriter/http.Flusher contract expected by the
// session service. The deadline is best-effort for test recorders and custom
// writers, and is enforced by net/http's response writer in production.
type sseStreamWriter struct {
	writer       http.ResponseWriter
	ctx          context.Context
	cancel       context.CancelFunc
	writeTimeout time.Duration

	mu       sync.Mutex
	writeErr error
}

func newSSEStreamWriter(ctx context.Context, cancel context.CancelFunc, writer http.ResponseWriter) *sseStreamWriter {
	if ctx == nil {
		ctx = context.Background()
	}
	return &sseStreamWriter{
		writer:       writer,
		ctx:          ctx,
		cancel:       cancel,
		writeTimeout: sseWriteTimeout,
	}
}

func withSSEStreamTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, maxSSEStreamDuration)
}

func (w *sseStreamWriter) Header() http.Header {
	return w.writer.Header()
}

func (w *sseStreamWriter) WriteHeader(statusCode int) {
	w.writer.WriteHeader(statusCode)
}

func (w *sseStreamWriter) Write(payload []byte) (int, error) {
	if err := w.errorOrCancellation(); err != nil {
		return 0, err
	}
	if err := w.setWriteDeadline(time.Now().Add(w.writeTimeout)); err != nil {
		w.rememberError(err)
		return 0, err
	}
	n, err := w.writer.Write(payload)
	if err != nil {
		w.rememberError(err)
	}
	return n, err
}

func (w *sseStreamWriter) Flush() {
	if err := w.errorOrCancellation(); err != nil {
		return
	}
	if err := w.setWriteDeadline(time.Now().Add(w.writeTimeout)); err != nil {
		w.rememberError(err)
		return
	}
	// ResponseController walks through Gin's Unwrap implementation to reach
	// the native net/http writer, where a slow-client deadline is meaningful.
	if err := http.NewResponseController(w.writer).Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		w.rememberError(err)
	}
	// Write deadlines are connection-wide. Clear the successful frame's
	// deadline so an idle, otherwise healthy SSE connection is not poisoned.
	if err := w.setWriteDeadline(time.Time{}); err != nil {
		w.rememberError(err)
	}
}

// Unwrap lets http.ResponseController reach the original response writer if
// this wrapper is passed to code that uses controller-level operations.
func (w *sseStreamWriter) Unwrap() http.ResponseWriter {
	return w.writer
}

func (w *sseStreamWriter) errorOrCancellation() error {
	w.mu.Lock()
	err := w.writeErr
	w.mu.Unlock()
	if err != nil {
		return err
	}
	return w.ctx.Err()
}

func (w *sseStreamWriter) rememberError(err error) {
	if err == nil {
		return
	}
	shouldCancel := false
	w.mu.Lock()
	if w.writeErr == nil {
		w.writeErr = err
		shouldCancel = true
	}
	w.mu.Unlock()
	// A failed SSE frame means the client can no longer consume the model
	// stream. Propagate cancellation immediately so the upstream model request
	// and the per-session gate can finish instead of running until its broader
	// five-minute stream deadline.
	if shouldCancel && w.cancel != nil {
		w.cancel()
	}
}

func (w *sseStreamWriter) setWriteDeadline(deadline time.Time) error {
	err := http.NewResponseController(w.writer).SetWriteDeadline(deadline)
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}
