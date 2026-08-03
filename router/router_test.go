package router

import (
	"GopherAI/common/health"
	"GopherAI/common/sessionauth"
	"GopherAI/config"
	"GopherAI/utils/myjwt"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestHealthAndUnknownAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := InitRouter()

	health := httptest.NewRecorder()
	r.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), `"status":"ok"`) {
		t.Fatalf("unexpected health response: status=%d body=%s", health.Code, health.Body.String())
	}

	missingAPI := httptest.NewRecorder()
	r.ServeHTTP(missingAPI, httptest.NewRequest(http.MethodGet, "/api/v1/missing", nil))
	if missingAPI.Code != http.StatusNotFound || !strings.Contains(missingAPI.Body.String(), "API route not found") {
		t.Fatalf("unexpected API 404 response: status=%d body=%s", missingAPI.Code, missingAPI.Body.String())
	}
}

func TestCookieSessionRoutes(t *testing.T) {
	configureRouterJWT(t)
	token, err := myjwt.GenerateToken(9, "browser-user")
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := InitRouter()
	sessionRequest := httptest.NewRequest(http.MethodGet, "/api/v1/user/session", nil)
	sessionRequest.AddCookie(&http.Cookie{Name: sessionauth.SessionCookieName, Value: token})
	sessionResponse := httptest.NewRecorder()
	router.ServeHTTP(sessionResponse, sessionRequest)
	if sessionResponse.Code != http.StatusOK || !strings.Contains(sessionResponse.Body.String(), `"username":"browser-user"`) {
		t.Fatalf("unexpected cookie session response: status=%d body=%s", sessionResponse.Code, sessionResponse.Body.String())
	}

	csrf := "test-csrf"
	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/v1/user/logout", nil)
	logoutRequest.AddCookie(&http.Cookie{Name: sessionauth.SessionCookieName, Value: token})
	logoutRequest.AddCookie(&http.Cookie{Name: sessionauth.CSRFCookieName, Value: csrf})
	logoutRequest.Header.Set(sessionauth.CSRFHeader, csrf)
	logoutResponse := httptest.NewRecorder()
	router.ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusOK {
		t.Fatalf("logout status = %d, body=%s", logoutResponse.Code, logoutResponse.Body.String())
	}
	if len(logoutResponse.Result().Cookies()) != 2 {
		t.Fatalf("logout cookies = %#v, want two deletion cookies", logoutResponse.Result().Cookies())
	}
}

func configureRouterJWT(t *testing.T) {
	t.Helper()
	configPath, err := filepath.Abs("../config/config.toml")
	if err != nil {
		t.Fatalf("resolve config path: %v", err)
	}
	t.Setenv("GOPHERAI_CONFIG_PATH", configPath)
	t.Setenv("GOPHERAI_ENV", "development")
	if err := config.InitConfig(); err != nil {
		t.Fatalf("InitConfig() error = %v", err)
	}
}

func TestReadinessStatusAndLivenessDuringDrain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	checker := health.NewChecker(time.Second,
		health.Check{Name: "mysql", Required: true, Run: func(context.Context) error { return nil }},
		health.Check{Name: "rabbitmq", Run: func(context.Context) error { return errors.New("connection secret") }},
	)
	checker.MarkReady()
	r := InitRouter(checker)

	ready := httptest.NewRecorder()
	r.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK || !strings.Contains(ready.Body.String(), `"status":"degraded"`) {
		t.Fatalf("unexpected degraded response: status=%d body=%s", ready.Code, ready.Body.String())
	}
	if strings.Contains(ready.Body.String(), "connection secret") {
		t.Fatalf("readiness leaked an internal error: %s", ready.Body.String())
	}

	checker.MarkNotReady()
	draining := httptest.NewRecorder()
	r.ServeHTTP(draining, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if draining.Code != http.StatusServiceUnavailable || !strings.Contains(draining.Body.String(), `"error_code":"stopped"`) {
		t.Fatalf("unexpected draining readiness: status=%d body=%s", draining.Code, draining.Body.String())
	}
	live := httptest.NewRecorder()
	r.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if live.Code != http.StatusOK || !strings.Contains(live.Body.String(), `"phase":"draining"`) {
		t.Fatalf("unexpected draining liveness: status=%d body=%s", live.Code, live.Body.String())
	}
}

func TestSPAFallbackAndStaticFile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	distDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(distDir, "index.html"), []byte("<html>spa</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(distDir, "app.js"), []byte("console.log('ok')"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := gin.New()
	configureSPA(r, distDir)

	asset := httptest.NewRecorder()
	r.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if asset.Code != http.StatusOK || asset.Body.String() != "console.log('ok')" {
		t.Fatalf("unexpected static response: status=%d body=%s", asset.Code, asset.Body.String())
	}

	spa := httptest.NewRecorder()
	r.ServeHTTP(spa, httptest.NewRequest(http.MethodGet, "/chat/session-1", nil))
	if spa.Code != http.StatusOK || spa.Body.String() != "<html>spa</html>" {
		t.Fatalf("unexpected SPA response: status=%d body=%s", spa.Code, spa.Body.String())
	}
}
