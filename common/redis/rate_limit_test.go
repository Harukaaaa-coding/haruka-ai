package redis

import (
	"strings"
	"testing"
)

func TestRateLimitKeyNormalizesAndHidesIdentifier(t *testing.T) {
	first := rateLimitKey("captcha", "email", " User@Example.COM ")
	second := rateLimitKey("captcha", "email", "user@example.com")
	if first != second {
		t.Fatalf("normalized identifiers produced different keys: %q != %q", first, second)
	}
	if strings.Contains(first, "user@example.com") {
		t.Fatalf("rate-limit key exposes the identifier: %q", first)
	}
}

func TestRateLimitKeyCanonicalizesIP(t *testing.T) {
	first := rateLimitKey("login", "ip", "2001:0db8:0:0:0:0:0:1")
	second := rateLimitKey("login", "ip", "2001:db8::1")
	if first != second {
		t.Fatalf("equivalent IPs produced different keys: %q != %q", first, second)
	}
}
