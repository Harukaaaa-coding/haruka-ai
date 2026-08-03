package costguard

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"GopherAI/common/code"
	redisstore "GopherAI/common/redis"
	"GopherAI/controller"

	"github.com/gin-gonic/gin"
)

func TestJSONRejectsKnownAndChunkedOversizedBodies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var limiterCalls atomic.Int64
	guard := New(testConfig(), LimiterFunc(func(context.Context, ...redisstore.RateLimitRule) (bool, time.Duration, error) {
		limiterCalls.Add(1)
		return true, 0, nil
	}))
	engine := testEngine(guard.JSON(ActionChat), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	tests := []struct {
		name    string
		request *http.Request
	}{
		{
			name:    "known content length",
			request: httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345")),
		},
		{
			name: "chunked body",
			request: func() *http.Request {
				request := httptest.NewRequest(http.MethodPost, "/", nil)
				request.Body = io.NopCloser(strings.NewReader("12345"))
				request.ContentLength = -1
				return request
			}(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, test.request)
			if recorder.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusRequestEntityTooLarge, recorder.Body.String())
			}
			assertResponseCode(t, recorder, code.CodeInvalidParams)
		})
	}
	if limiterCalls.Load() != int64(len(tests)) {
		t.Fatalf("limiter calls = %d, want %d", limiterCalls.Load(), len(tests))
	}
}

func TestQuotaReservedBeforeBodyReadAndInFlightSlot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := new(trackingBody)
	var guard *Guard
	observedInFlight := -1
	guard = New(testConfig(), LimiterFunc(func(context.Context, ...redisstore.RateLimitRule) (bool, time.Duration, error) {
		guard.mu.Lock()
		observedInFlight = guard.inFlight
		guard.mu.Unlock()
		return false, time.Second, nil
	}))
	engine := testEngine(guard.JSON(ActionChat), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Body = body
	request.ContentLength = -1
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if body.reads.Load() != 0 {
		t.Fatalf("body reads = %d, want 0", body.reads.Load())
	}
	if observedInFlight != 0 {
		t.Fatalf("in-flight during quota check = %d, want 0", observedInFlight)
	}
}

func TestJSONPreservesBodyAndBuildsAtomicUserRules(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var captured []redisstore.RateLimitRule
	guard := New(testConfig(), LimiterFunc(func(_ context.Context, rules ...redisstore.RateLimitRule) (bool, time.Duration, error) {
		captured = append(captured, rules...)
		return true, 0, nil
	}))
	engine := testEngine(guard.JSON(ActionChat), func(c *gin.Context) {
		payload, err := io.ReadAll(c.Request.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		c.String(http.StatusOK, string(payload))
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("1234"))
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "1234" {
		t.Fatalf("unexpected response: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if len(captured) != 2 {
		t.Fatalf("rate-limit rules = %d, want 2", len(captured))
	}
	if captured[0].Identifier != "alice" || captured[1].Identifier != "alice" {
		t.Fatalf("rate-limit identifiers = %q, %q", captured[0].Identifier, captured[1].Identifier)
	}
	if captured[0].Dimension != aggregateDimension || captured[0].Limit != 100 {
		t.Fatalf("aggregate rule = %#v", captured[0])
	}
	if captured[1].Dimension != string(ActionChat) || captured[1].Limit != 100 {
		t.Fatalf("action rule = %#v", captured[1])
	}
}

func TestMultipartSkipsJSONBodyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var limiterCalls atomic.Int64
	guard := New(testConfig(), LimiterFunc(func(context.Context, ...redisstore.RateLimitRule) (bool, time.Duration, error) {
		limiterCalls.Add(1)
		return true, 0, nil
	}))
	engine := testEngine(guard.Multipart(ActionImage), func(c *gin.Context) {
		payload, _ := io.ReadAll(c.Request.Body)
		c.String(http.StatusOK, string(payload))
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("larger than four bytes"))
	request.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "larger than four bytes" {
		t.Fatalf("unexpected response: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if limiterCalls.Load() != 1 {
		t.Fatalf("limiter calls = %d, want 1", limiterCalls.Load())
	}
}

func TestRateLimitAndUnavailableResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		result     bool
		retryAfter time.Duration
		err        error
		wantStatus int
		wantCode   code.Code
		wantRetry  string
	}{
		{
			name:       "quota exhausted",
			result:     false,
			retryAfter: 1500 * time.Millisecond,
			wantStatus: http.StatusTooManyRequests,
			wantCode:   code.CodeTooManyRequests,
			wantRetry:  "2",
		},
		{
			name:       "redis unavailable",
			err:        errors.New("unavailable"),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   code.CodeServerBusy,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			guard := New(testConfig(), LimiterFunc(func(context.Context, ...redisstore.RateLimitRule) (bool, time.Duration, error) {
				return test.result, test.retryAfter, test.err
			}))
			var handlerCalls atomic.Int64
			engine := testEngine(guard.JSON(ActionChat), func(c *gin.Context) {
				handlerCalls.Add(1)
				c.Status(http.StatusNoContent)
			})
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", nil))
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
			assertResponseCode(t, recorder, test.wantCode)
			if recorder.Header().Get("Retry-After") != test.wantRetry {
				t.Fatalf("Retry-After = %q, want %q", recorder.Header().Get("Retry-After"), test.wantRetry)
			}
			if handlerCalls.Load() != 0 {
				t.Fatalf("handler calls = %d, want 0", handlerCalls.Load())
			}
		})
	}
}

func TestInFlightCapsAreSharedAndReleased(t *testing.T) {
	gin.SetMode(gin.TestMode)
	config := testConfig()
	config.UserInFlight = 1
	config.GlobalInFlight = 2
	guard := New(config, allowLimiter())
	started := make(chan string, 2)
	unblock := make(chan struct{})
	engine := testEngineForUsers(guard.Multipart(ActionChat), func(c *gin.Context) {
		started <- c.GetString("userName")
		<-unblock
		c.Status(http.StatusNoContent)
	})

	firstDone := serveAsync(engine, requestForUser("alice"))
	if user := <-started; user != "alice" {
		t.Fatalf("first started user = %q", user)
	}

	sameUser := httptest.NewRecorder()
	engine.ServeHTTP(sameUser, requestForUser("alice"))
	if sameUser.Code != http.StatusTooManyRequests || sameUser.Header().Get("Retry-After") != "1" {
		t.Fatalf("same-user response: status=%d retry=%q", sameUser.Code, sameUser.Header().Get("Retry-After"))
	}

	secondDone := serveAsync(engine, requestForUser("bob"))
	if user := <-started; user != "bob" {
		t.Fatalf("second started user = %q", user)
	}
	global := httptest.NewRecorder()
	engine.ServeHTTP(global, requestForUser("charlie"))
	if global.Code != http.StatusTooManyRequests {
		t.Fatalf("global-cap status = %d, want %d", global.Code, http.StatusTooManyRequests)
	}

	close(unblock)
	if response := <-firstDone; response.Code != http.StatusNoContent {
		t.Fatalf("first response status = %d", response.Code)
	}
	if response := <-secondDone; response.Code != http.StatusNoContent {
		t.Fatalf("second response status = %d", response.Code)
	}

	afterRelease := httptest.NewRecorder()
	engine.ServeHTTP(afterRelease, requestForUser("alice"))
	if afterRelease.Code != http.StatusNoContent {
		t.Fatalf("status after release = %d, want %d", afterRelease.Code, http.StatusNoContent)
	}
}

func TestInFlightSlotReleasedDuringPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	config := testConfig()
	config.UserInFlight = 1
	config.GlobalInFlight = 1
	guard := New(config, allowLimiter())
	var calls atomic.Int64

	engine := gin.New()
	engine.Use(gin.CustomRecovery(func(c *gin.Context, _ any) {
		c.AbortWithStatus(http.StatusInternalServerError)
	}))
	engine.Use(func(c *gin.Context) {
		c.Set("userName", "alice")
		c.Next()
	})
	engine.POST("/", guard.JSON(ActionChat), func(c *gin.Context) {
		if calls.Add(1) == 1 {
			panic("test panic")
		}
		c.Status(http.StatusNoContent)
	})

	first := httptest.NewRecorder()
	engine.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/", nil))
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d, want %d", first.Code, http.StatusInternalServerError)
	}
	second := httptest.NewRecorder()
	engine.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/", nil))
	if second.Code != http.StatusNoContent {
		t.Fatalf("status after panic = %d, want %d", second.Code, http.StatusNoContent)
	}
}

func TestImageInFlightCapIsSharedAcrossUsers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	config := testConfig()
	config.UserInFlight = 10
	config.GlobalInFlight = 10
	config.ImageInFlight = 1
	guard := New(config, allowLimiter())
	started := make(chan struct{}, 1)
	unblock := make(chan struct{})
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("userName", c.GetHeader("X-Test-User"))
		c.Next()
	})
	engine.POST("/image", guard.Multipart(ActionImage), func(c *gin.Context) {
		started <- struct{}{}
		<-unblock
		c.Status(http.StatusNoContent)
	})
	engine.POST("/chat", guard.NoBody(ActionChat), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	firstDone := serveAsync(engine, requestForUserAt("alice", "/image"))
	<-started
	secondImage := httptest.NewRecorder()
	engine.ServeHTTP(secondImage, requestForUserAt("bob", "/image"))
	if secondImage.Code != http.StatusTooManyRequests {
		t.Fatalf("second image status = %d, want %d", secondImage.Code, http.StatusTooManyRequests)
	}
	chat := httptest.NewRecorder()
	engine.ServeHTTP(chat, requestForUserAt("bob", "/chat"))
	if chat.Code != http.StatusNoContent {
		t.Fatalf("non-image status = %d, want %d", chat.Code, http.StatusNoContent)
	}
	close(unblock)
	if response := <-firstDone; response.Code != http.StatusNoContent {
		t.Fatalf("first image status = %d", response.Code)
	}
}

func TestUserInFlightUsesCanonicalUsername(t *testing.T) {
	gin.SetMode(gin.TestMode)
	config := testConfig()
	config.UserInFlight = 1
	guard := New(config, allowLimiter())
	started := make(chan struct{}, 1)
	unblock := make(chan struct{})
	engine := testEngineForUsers(guard.NoBody(ActionChat), func(c *gin.Context) {
		started <- struct{}{}
		<-unblock
		c.Status(http.StatusNoContent)
	})

	firstDone := serveAsync(engine, requestForUser("Alice"))
	<-started
	variant := httptest.NewRecorder()
	engine.ServeHTTP(variant, requestForUser(" ALICE "))
	if variant.Code != http.StatusTooManyRequests {
		t.Fatalf("case/space variant status = %d, want %d", variant.Code, http.StatusTooManyRequests)
	}
	close(unblock)
	<-firstDone
}

func TestConfigFromEnvironment(t *testing.T) {
	t.Setenv(EnvJSONMaxBytes, "2048")
	t.Setenv(EnvChatRequests, "7")
	t.Setenv(EnvUserInFlight, "3")
	t.Setenv(EnvImageInFlight, "4")
	t.Setenv(EnvWindowSeconds, "90")
	config := ConfigFromEnvironment()
	if config.JSONMaxBytes != 2048 || config.ActionLimits[ActionChat] != 7 || config.UserInFlight != 3 || config.ImageInFlight != 4 || config.Window != 90*time.Second {
		t.Fatalf("unexpected environment config: %#v", config)
	}
}

func testConfig() Config {
	config := DefaultConfig()
	config.JSONMaxBytes = 4
	config.RequestsPerWindow = 100
	for action := range config.ActionLimits {
		config.ActionLimits[action] = 100
	}
	config.UserInFlight = 10
	config.GlobalInFlight = 20
	return config
}

func allowLimiter() Limiter {
	return LimiterFunc(func(context.Context, ...redisstore.RateLimitRule) (bool, time.Duration, error) {
		return true, 0, nil
	})
}

func testEngine(middleware gin.HandlerFunc, handler gin.HandlerFunc) *gin.Engine {
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("userName", "alice")
		c.Next()
	})
	engine.POST("/", middleware, handler)
	return engine
}

func testEngineForUsers(middleware gin.HandlerFunc, handler gin.HandlerFunc) *gin.Engine {
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("userName", c.GetHeader("X-Test-User"))
		c.Next()
	})
	engine.POST("/", middleware, handler)
	return engine
}

func requestForUser(userName string) *http.Request {
	return requestForUserAt(userName, "/")
}

func requestForUserAt(userName, path string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, path, nil)
	request.Header.Set("X-Test-User", userName)
	return request
}

func serveAsync(engine *gin.Engine, request *http.Request) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		done <- recorder
	}()
	return done
}

func assertResponseCode(t *testing.T, recorder *httptest.ResponseRecorder, expected code.Code) {
	t.Helper()
	response := new(controller.Response)
	if err := json.Unmarshal(recorder.Body.Bytes(), response); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, recorder.Body.String())
	}
	if response.StatusCode != expected {
		t.Fatalf("business status = %d, want %d", response.StatusCode, expected)
	}
}

type trackingBody struct {
	reads atomic.Int64
}

func (body *trackingBody) Read([]byte) (int, error) {
	body.reads.Add(1)
	return 0, errors.New("body should not be read")
}

func (*trackingBody) Close() error { return nil }
