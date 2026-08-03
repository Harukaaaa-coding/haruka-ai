package sessionauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIssueCreatesSeparatedSessionAndCSRFCookies(t *testing.T) {
	recorder := httptest.NewRecorder()
	if err := Issue(recorder, "signed-token", time.Hour, true); err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	response := recorder.Result()
	cookies := response.Cookies()
	if len(cookies) != 2 {
		t.Fatalf("cookie count = %d, want 2", len(cookies))
	}

	var session, csrf *http.Cookie
	for _, cookie := range cookies {
		switch cookie.Name {
		case SessionCookieName:
			session = cookie
		case CSRFCookieName:
			csrf = cookie
		}
	}
	if session == nil || csrf == nil {
		t.Fatalf("cookies = %#v, want session and CSRF cookies", cookies)
	}
	if !session.HttpOnly || !session.Secure || session.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie security = %#v", session)
	}
	if csrf.HttpOnly || !csrf.Secure || csrf.Value == "" || csrf.Value == session.Value {
		t.Fatalf("CSRF cookie security = %#v", csrf)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/AI/chat", nil)
	req.AddCookie(csrf)
	req.Header.Set(CSRFHeader, csrf.Value)
	if !ValidCSRF(req) {
		t.Fatal("ValidCSRF() = false, want true")
	}
	req.Header.Set(CSRFHeader, "different")
	if ValidCSRF(req) {
		t.Fatal("ValidCSRF() = true for mismatched header")
	}
}

func TestCookieSessionRequestRules(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(CookieSessionHeader, "COOKIE")
	if !WantsCookieSession(request) {
		t.Fatal("WantsCookieSession() = false")
	}
	if RequiresCSRF(request) {
		t.Fatal("GET should not require CSRF")
	}
	request.Method = http.MethodPost
	if !RequiresCSRF(request) {
		t.Fatal("POST should require CSRF")
	}
	if ShouldUseSecureCookie(request, false) {
		t.Fatal("plain development request unexpectedly requires secure cookie")
	}
	if !ShouldUseSecureCookie(request, true) {
		t.Fatal("production request must require secure cookie")
	}
}
