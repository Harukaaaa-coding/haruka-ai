package knowledgebase

import (
	"context"
	"errors"
	"strings"
	"testing"
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
