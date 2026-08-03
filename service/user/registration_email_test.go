package user

import (
	"context"
	"testing"
	"time"
)

func TestRegistrationEmailRetryDelayIsBounded(t *testing.T) {
	if got := registrationEmailRetryDelay(1); got != time.Minute {
		t.Fatalf("first retry delay = %s, want 1m", got)
	}
	if got := registrationEmailRetryDelay(4); got != 8*time.Minute {
		t.Fatalf("fourth retry delay = %s, want 8m", got)
	}
	if got := registrationEmailRetryDelay(100); got != 64*time.Minute {
		t.Fatalf("bounded retry delay = %s, want 64m", got)
	}
}

func TestRegistrationEmailWorkerStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	StartRegistrationEmailWorker(ctx)
	waitCtx, stopWaiting := context.WithTimeout(context.Background(), time.Second)
	defer stopWaiting()
	if err := WaitRegistrationEmailWorker(waitCtx); err != nil {
		t.Fatalf("worker did not stop: %v", err)
	}
	if RegistrationEmailWorkerRunning() {
		t.Fatal("worker still reports running after cancellation")
	}
}
