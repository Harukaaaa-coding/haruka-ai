// Package costguard protects authenticated endpoints that can consume model,
// tool, or indexing capacity.
package costguard

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"GopherAI/common/code"
	redisstore "GopherAI/common/redis"
	"GopherAI/controller"

	"github.com/gin-gonic/gin"
)

type Action string

const (
	ActionChat  Action = "chat"
	ActionVoice Action = "voice"
	ActionRAG   Action = "rag"
	ActionMCP   Action = "mcp"
	ActionAgent Action = "agent"
	ActionImage Action = "image"
)

const (
	EnvJSONMaxBytes          = "GOPHERAI_COST_GUARD_JSON_MAX_BYTES"
	EnvWindowSeconds         = "GOPHERAI_COST_GUARD_WINDOW_SECONDS"
	EnvRequestsPerWindow     = "GOPHERAI_COST_GUARD_REQUESTS_PER_WINDOW"
	EnvChatRequests          = "GOPHERAI_COST_GUARD_CHAT_REQUESTS"
	EnvVoiceRequests         = "GOPHERAI_COST_GUARD_VOICE_REQUESTS"
	EnvRAGRequests           = "GOPHERAI_COST_GUARD_RAG_REQUESTS"
	EnvMCPRequests           = "GOPHERAI_COST_GUARD_MCP_REQUESTS"
	EnvAgentRequests         = "GOPHERAI_COST_GUARD_AGENT_REQUESTS"
	EnvImageRequests         = "GOPHERAI_COST_GUARD_IMAGE_REQUESTS"
	EnvUserInFlight          = "GOPHERAI_COST_GUARD_USER_INFLIGHT"
	EnvGlobalInFlight        = "GOPHERAI_COST_GUARD_GLOBAL_INFLIGHT"
	EnvImageInFlight         = "GOPHERAI_COST_GUARD_IMAGE_INFLIGHT"
	EnvConcurrencyRetryAfter = "GOPHERAI_COST_GUARD_RETRY_AFTER_SECONDS"
)

const aggregateDimension = "all"

// Config controls both the distributed time-window quota and the local
// in-flight caps. All limits must be positive; New replaces invalid values
// with the safe defaults.
type Config struct {
	JSONMaxBytes      int64
	Window            time.Duration
	RequestsPerWindow int64
	ActionLimits      map[Action]int64
	UserInFlight      int
	GlobalInFlight    int
	ImageInFlight     int
	RetryAfter        time.Duration
}

func DefaultConfig() Config {
	return Config{
		JSONMaxBytes:      1 << 20,
		Window:            time.Minute,
		RequestsPerWindow: 60,
		ActionLimits: map[Action]int64{
			ActionChat:  20,
			ActionVoice: 10,
			ActionRAG:   10,
			ActionMCP:   30,
			ActionAgent: 10,
			ActionImage: 10,
		},
		UserInFlight:   2,
		GlobalInFlight: 32,
		ImageInFlight:  1,
		RetryAfter:     time.Second,
	}
}

// ConfigFromEnvironment applies positive integer environment overrides to the
// defaults. Invalid overrides are ignored, keeping protection enabled.
func ConfigFromEnvironment() Config {
	config := DefaultConfig()
	applyPositiveInt64(EnvJSONMaxBytes, &config.JSONMaxBytes)
	applyPositiveDurationSeconds(EnvWindowSeconds, &config.Window)
	applyPositiveInt64(EnvRequestsPerWindow, &config.RequestsPerWindow)
	applyActionLimit(EnvChatRequests, config.ActionLimits, ActionChat)
	applyActionLimit(EnvVoiceRequests, config.ActionLimits, ActionVoice)
	applyActionLimit(EnvRAGRequests, config.ActionLimits, ActionRAG)
	applyActionLimit(EnvMCPRequests, config.ActionLimits, ActionMCP)
	applyActionLimit(EnvAgentRequests, config.ActionLimits, ActionAgent)
	applyActionLimit(EnvImageRequests, config.ActionLimits, ActionImage)
	applyPositiveInt(EnvUserInFlight, &config.UserInFlight)
	applyPositiveInt(EnvGlobalInFlight, &config.GlobalInFlight)
	applyPositiveInt(EnvImageInFlight, &config.ImageInFlight)
	applyPositiveDurationSeconds(EnvConcurrencyRetryAfter, &config.RetryAfter)
	return config
}

// Limiter is implemented by the shared Redis rate limiter and is injectable
// so the middleware can be tested without external services.
type Limiter interface {
	Allow(context.Context, ...redisstore.RateLimitRule) (bool, time.Duration, error)
}

type LimiterFunc func(context.Context, ...redisstore.RateLimitRule) (bool, time.Duration, error)

func (function LimiterFunc) Allow(ctx context.Context, rules ...redisstore.RateLimitRule) (bool, time.Duration, error) {
	return function(ctx, rules...)
}

type redisLimiter struct{}

func (redisLimiter) Allow(ctx context.Context, rules ...redisstore.RateLimitRule) (bool, time.Duration, error) {
	return redisstore.AllowRateLimits(ctx, rules...)
}

// Guard must be shared by all protected routes in one process so the global
// and per-user in-flight counts cover every expensive endpoint.
type Guard struct {
	config  Config
	limiter Limiter

	mu       sync.Mutex
	inFlight int
	byUser   map[string]int
	byAction map[Action]int
}

func NewDefault() *Guard {
	return New(ConfigFromEnvironment(), redisLimiter{})
}

func New(config Config, limiter Limiter) *Guard {
	config = normalizeConfig(config)
	if limiter == nil {
		limiter = redisLimiter{}
	}
	return &Guard{
		config:   config,
		limiter:  limiter,
		byUser:   make(map[string]int),
		byAction: make(map[Action]int),
	}
}

// JSON protects an endpoint whose request body is JSON. The complete body is
// bounded before it reaches the controller, including chunked requests.
func (guard *Guard) JSON(action Action) gin.HandlerFunc {
	return guard.protect(action, true)
}

// Multipart protects an upload/inference endpoint without applying the JSON
// body limit. Upload-specific size validation remains the controller's job.
func (guard *Guard) Multipart(action Action) gin.HandlerFunc {
	return guard.NoBody(action)
}

// NoBody protects an endpoint without buffering or applying the JSON body
// limit. It is suitable for bodyless control operations and for endpoints
// whose controller applies a type-specific streaming body limit.
func (guard *Guard) NoBody(action Action) gin.HandlerFunc {
	return guard.protect(action, false)
}

// Conditional runs middleware only when condition is true. The unprotected
// branch continues the Gin chain normally, which is useful for GET endpoints
// whose optional refresh mode performs upstream work.
func Conditional(condition func(*gin.Context) bool, middleware gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if condition != nil && condition(c) {
			middleware(c)
			return
		}
		c.Next()
	}
}

func (guard *Guard) protect(action Action, limitJSON bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		userName := strings.ToLower(strings.TrimSpace(c.GetString("userName")))
		if userName == "" {
			abortWithCode(c, http.StatusUnauthorized, code.CodeInvalidToken)
			return
		}

		actionLimit := guard.config.ActionLimits[action]
		if actionLimit <= 0 {
			actionLimit = guard.config.RequestsPerWindow
		}
		allowed, retryAfter, err := guard.limiter.Allow(c.Request.Context(),
			redisstore.RateLimitRule{
				Action:     "cost-guard",
				Dimension:  aggregateDimension,
				Identifier: userName,
				Limit:      guard.config.RequestsPerWindow,
				Window:     guard.config.Window,
			},
			redisstore.RateLimitRule{
				Action:     "cost-guard",
				Dimension:  string(action),
				Identifier: userName,
				Limit:      actionLimit,
				Window:     guard.config.Window,
			},
		)
		if err != nil {
			log.Printf("cost guard rate limiter unavailable for %s: %v", action, err)
			abortWithCode(c, http.StatusServiceUnavailable, code.CodeServerBusy)
			return
		}
		if !allowed {
			writeRateLimit(c, retryAfter)
			return
		}

		// Reserve distributed quota before occupying a process-local slot or
		// waiting for a request body. Slow clients therefore cannot hold scarce
		// capacity without being counted in the time window.
		release, acquired := guard.acquire(userName, action)
		if !acquired {
			writeRateLimit(c, guard.config.RetryAfter)
			return
		}
		defer release()

		if limitJSON && !guard.limitBody(c) {
			return
		}

		c.Next()
	}
}

func (guard *Guard) limitBody(c *gin.Context) bool {
	request := c.Request
	if request.Body == nil || request.Body == http.NoBody {
		return true
	}
	if request.ContentLength > guard.config.JSONMaxBytes {
		abortWithCode(c, http.StatusRequestEntityTooLarge, code.CodeInvalidParams)
		return false
	}

	readLimit := guard.config.JSONMaxBytes + 1
	if readLimit <= guard.config.JSONMaxBytes { // overflow guard for caller-supplied configs
		readLimit = guard.config.JSONMaxBytes
	}
	originalBody := request.Body
	payload, err := io.ReadAll(io.LimitReader(originalBody, readLimit))
	_ = originalBody.Close()
	if err != nil {
		abortWithCode(c, http.StatusBadRequest, code.CodeInvalidParams)
		return false
	}
	if int64(len(payload)) > guard.config.JSONMaxBytes {
		abortWithCode(c, http.StatusRequestEntityTooLarge, code.CodeInvalidParams)
		return false
	}
	request.Body = io.NopCloser(bytes.NewReader(payload))
	request.ContentLength = int64(len(payload))
	return true
}

func (guard *Guard) acquire(userName string, action Action) (func(), bool) {
	guard.mu.Lock()
	imageSaturated := action == ActionImage && guard.byAction[ActionImage] >= guard.config.ImageInFlight
	if guard.inFlight >= guard.config.GlobalInFlight || guard.byUser[userName] >= guard.config.UserInFlight || imageSaturated {
		guard.mu.Unlock()
		return nil, false
	}
	guard.inFlight++
	guard.byUser[userName]++
	guard.byAction[action]++
	guard.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			guard.mu.Lock()
			guard.inFlight--
			guard.byUser[userName]--
			guard.byAction[action]--
			if guard.byUser[userName] == 0 {
				delete(guard.byUser, userName)
			}
			if guard.byAction[action] == 0 {
				delete(guard.byAction, action)
			}
			guard.mu.Unlock()
		})
	}, true
}

func writeRateLimit(c *gin.Context, retryAfter time.Duration) {
	seconds := int64(retryAfter / time.Second)
	if retryAfter%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	c.Header("Retry-After", strconv.FormatInt(seconds, 10))
	abortWithCode(c, http.StatusTooManyRequests, code.CodeTooManyRequests)
}

func abortWithCode(c *gin.Context, status int, responseCode code.Code) {
	response := new(controller.Response)
	c.AbortWithStatusJSON(status, response.CodeOf(responseCode))
}

func normalizeConfig(config Config) Config {
	defaults := DefaultConfig()
	if config.JSONMaxBytes <= 0 {
		config.JSONMaxBytes = defaults.JSONMaxBytes
	}
	if config.Window <= 0 {
		config.Window = defaults.Window
	}
	if config.RequestsPerWindow <= 0 {
		config.RequestsPerWindow = defaults.RequestsPerWindow
	}
	limits := make(map[Action]int64, len(defaults.ActionLimits))
	for action, fallback := range defaults.ActionLimits {
		limit := config.ActionLimits[action]
		if limit <= 0 {
			limit = fallback
		}
		limits[action] = limit
	}
	config.ActionLimits = limits
	if config.UserInFlight <= 0 {
		config.UserInFlight = defaults.UserInFlight
	}
	if config.GlobalInFlight <= 0 {
		config.GlobalInFlight = defaults.GlobalInFlight
	}
	if config.ImageInFlight <= 0 {
		config.ImageInFlight = defaults.ImageInFlight
	}
	if config.RetryAfter <= 0 {
		config.RetryAfter = defaults.RetryAfter
	}
	return config
}

func applyActionLimit(name string, limits map[Action]int64, action Action) {
	value := limits[action]
	applyPositiveInt64(name, &value)
	limits[action] = value
}

func applyPositiveInt64(name string, destination *int64) {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return
	}
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		log.Printf("ignoring invalid positive integer environment variable %s", name)
		return
	}
	*destination = value
}

func applyPositiveInt(name string, destination *int) {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		log.Printf("ignoring invalid positive integer environment variable %s", name)
		return
	}
	*destination = value
}

func applyPositiveDurationSeconds(name string, destination *time.Duration) {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || seconds <= 0 || seconds > int64((1<<63-1)/time.Second) {
		log.Printf("ignoring invalid positive duration environment variable %s", name)
		return
	}
	*destination = time.Duration(seconds) * time.Second
}
