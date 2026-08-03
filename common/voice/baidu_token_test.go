package voice

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestBaiduTokenProviderCachesToken(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("client_id") != "key" || r.Form.Get("client_secret") != "secret" {
			t.Fatal("credentials were not form encoded correctly")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"cached-token","expires_in":3600}`)
	}))
	defer server.Close()

	provider := NewBaiduTokenProvider("key", "secret", server.Client())
	provider.SetTokenURLForTest(server.URL)
	for i := 0; i < 2; i++ {
		token, err := provider.Token(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if token != "cached-token" {
			t.Fatalf("token = %q", token)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("token endpoint calls = %d, want 1", calls.Load())
	}
}

func TestBaiduTokenProviderRequiresCredentials(t *testing.T) {
	provider := NewBaiduTokenProvider("", "", nil)
	if _, err := provider.Token(context.Background()); err == nil {
		t.Fatal("expected missing credential error")
	}
}
