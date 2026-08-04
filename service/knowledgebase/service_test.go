package knowledgebase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRetrieveRejectsOversizedAndInvalidUTF8QueriesBeforeDependencies(t *testing.T) {
	queries := []string{
		strings.Repeat("问", MaxSearchQueryRunes+1),
		string([]byte{0xff, 0xfe}),
	}
	for _, query := range queries {
		if _, err := Retrieve(context.Background(), "alice", []string{"kb"}, query, 5); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Retrieve() error = %v, want ErrInvalidInput", err)
		}
	}
}

func TestDeleteRetryDelayIsBoundedExponential(t *testing.T) {
	if got := deleteRetryDelay(0); got != time.Minute {
		t.Fatalf("attempt 0 delay = %s", got)
	}
	if got := deleteRetryDelay(3); got != 4*time.Minute {
		t.Fatalf("attempt 3 delay = %s", got)
	}
	if got := deleteRetryDelay(100); got != 30*time.Minute {
		t.Fatalf("large attempt delay = %s", got)
	}
}
