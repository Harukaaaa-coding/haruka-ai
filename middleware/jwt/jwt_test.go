package jwt

import (
	"GopherAI/common/sessionauth"
	"GopherAI/config"
	"GopherAI/utils/myjwt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAuthRejectsTokenInQueryString(t *testing.T) {
	gin.SetMode(gin.TestMode)
	called := false
	router := gin.New()
	router.GET("/private", Auth(), func(c *gin.Context) {
		called = true
		c.Status(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/private?token=secret", nil))

	if called {
		t.Fatal("protected handler was called with a query-string token")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected compatibility response status: got %d", response.Code)
	}
}

func TestAuthAcceptsCookieSessionAndEnforcesCSRF(t *testing.T) {
	configureJWTForTest(t)
	token, err := myjwt.GenerateToken(1, "cookie-user")
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}

	gin.SetMode(gin.TestMode)
	called := 0
	router := gin.New()
	router.GET("/private", Auth(), func(c *gin.Context) {
		called++
		c.Status(http.StatusNoContent)
	})
	router.POST("/private", Auth(), func(c *gin.Context) {
		called++
		c.Status(http.StatusNoContent)
	})

	getRequest := httptest.NewRequest(http.MethodGet, "/private", nil)
	getRequest.AddCookie(&http.Cookie{Name: sessionauth.SessionCookieName, Value: token})
	getResponse := httptest.NewRecorder()
	router.ServeHTTP(getResponse, getRequest)
	if getResponse.Code != http.StatusNoContent || called != 1 {
		t.Fatalf("cookie GET status=%d called=%d, want %d/1", getResponse.Code, called, http.StatusNoContent)
	}

	postRequest := httptest.NewRequest(http.MethodPost, "/private", nil)
	postRequest.AddCookie(&http.Cookie{Name: sessionauth.SessionCookieName, Value: token})
	postResponse := httptest.NewRecorder()
	router.ServeHTTP(postResponse, postRequest)
	if postResponse.Code != http.StatusForbidden || called != 1 {
		t.Fatalf("cookie POST without CSRF status=%d called=%d, want %d/1", postResponse.Code, called, http.StatusForbidden)
	}

	csrf := "csrf-token"
	validPost := httptest.NewRequest(http.MethodPost, "/private", nil)
	validPost.AddCookie(&http.Cookie{Name: sessionauth.SessionCookieName, Value: token})
	validPost.AddCookie(&http.Cookie{Name: sessionauth.CSRFCookieName, Value: csrf})
	validPost.Header.Set(sessionauth.CSRFHeader, csrf)
	validResponse := httptest.NewRecorder()
	router.ServeHTTP(validResponse, validPost)
	if validResponse.Code != http.StatusNoContent || called != 2 {
		t.Fatalf("cookie POST with CSRF status=%d called=%d, want %d/2", validResponse.Code, called, http.StatusNoContent)
	}
}

func configureJWTForTest(t *testing.T) {
	t.Helper()
	configPath, err := filepath.Abs("../../config/config.toml")
	if err != nil {
		t.Fatalf("resolve config path: %v", err)
	}
	t.Setenv("GOPHERAI_CONFIG_PATH", configPath)
	t.Setenv("GOPHERAI_ENV", "development")
	if err := config.InitConfig(); err != nil {
		t.Fatalf("InitConfig() error = %v", err)
	}
}
