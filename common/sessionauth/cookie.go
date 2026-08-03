// Package sessionauth contains the browser-facing portion of the otherwise
// bearer-compatible authentication scheme. Native clients can keep using an
// Authorization header, while the bundled SPA opts into an HttpOnly cookie.
package sessionauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	SessionCookieName       = "gopherai_session"
	CSRFCookieName          = "gopherai_csrf"
	CookieSessionHeader     = "X-GopherAI-Session"
	CSRFHeader              = "X-CSRF-Token"
	CookieSessionPreference = "cookie"
)

// WantsCookieSession is an explicit opt-in so JSON API and terminal clients
// continue to receive the bearer token response they already understand.
func WantsCookieSession(request *http.Request) bool {
	if request == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(request.Header.Get(CookieSessionHeader)), CookieSessionPreference)
}

func SessionToken(request *http.Request) string {
	if request == nil {
		return ""
	}
	cookie, err := request.Cookie(SessionCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

// Issue stores the credential in an HttpOnly cookie and emits an unrelated
// random CSRF value in a readable cookie for double-submit verification.
func Issue(response http.ResponseWriter, token string, ttl time.Duration, secure bool) error {
	if response == nil {
		return fmt.Errorf("session response writer is unavailable")
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("session token is empty")
	}
	if ttl <= 0 {
		return fmt.Errorf("session lifetime must be positive")
	}

	csrf, err := newCSRFToken()
	if err != nil {
		return err
	}
	expiresAt := time.Now().Add(ttl)
	maxAge := int(ttl / time.Second)
	if maxAge < 1 {
		maxAge = 1
	}

	http.SetCookie(response, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(response, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    csrf,
		Path:     "/",
		MaxAge:   maxAge,
		Expires:  expiresAt,
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
	return nil
}

func Clear(response http.ResponseWriter, secure bool) {
	if response == nil {
		return
	}
	for _, name := range []string{SessionCookieName, CSRFCookieName} {
		http.SetCookie(response, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			Expires:  time.Unix(1, 0),
			HttpOnly: name == SessionCookieName,
			Secure:   secure,
			SameSite: http.SameSiteStrictMode,
		})
	}
}

func RequiresCSRF(request *http.Request) bool {
	if request == nil {
		return false
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	default:
		return true
	}
}

func ValidCSRF(request *http.Request) bool {
	if request == nil {
		return false
	}
	cookie, err := request.Cookie(CSRFCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	header := strings.TrimSpace(request.Header.Get(CSRFHeader))
	if header == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) == 1
}

// ShouldUseSecureCookie fails closed for production/staging deployments, even
// when TLS terminates at a reverse proxy before reaching the Go process.
func ShouldUseSecureCookie(request *http.Request, production bool) bool {
	if production || (request != nil && request.TLS != nil) {
		return true
	}
	return request != nil && strings.EqualFold(strings.TrimSpace(request.Header.Get("X-Forwarded-Proto")), "https")
}

func newCSRFToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate CSRF token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}
