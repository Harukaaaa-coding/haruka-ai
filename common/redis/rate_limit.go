package redis

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type RateLimitRule struct {
	Action     string
	Dimension  string
	Identifier string
	Limit      int64
	Window     time.Duration
}

const reserveRateLimitScript = `
local retry = 0
for i, key in ipairs(KEYS) do
  local limit = tonumber(ARGV[(i - 1) * 2 + 1])
  local current = tonumber(redis.call('GET', key) or '0')
  if current >= limit then
    local ttl = redis.call('PTTL', key)
	if ttl <= 0 then
	  ttl = tonumber(ARGV[(i - 1) * 2 + 2])
	  redis.call('PEXPIRE', key, ttl)
	end
    if ttl > retry then retry = ttl end
  end
end
if retry > 0 then return {0, retry} end
for i, key in ipairs(KEYS) do
  local window = tonumber(ARGV[(i - 1) * 2 + 2])
  local current = redis.call('INCR', key)
  if current == 1 then redis.call('PEXPIRE', key, window) end
end
return {1, 0}
`

func AllowRateLimits(ctx context.Context, rules ...RateLimitRule) (bool, time.Duration, error) {
	if Rdb == nil {
		return false, 0, errors.New("redis is not initialized")
	}
	if len(rules) == 0 {
		return true, 0, nil
	}
	keys := make([]string, 0, len(rules))
	args := make([]interface{}, 0, len(rules)*2)
	for _, rule := range rules {
		if rule.Action == "" || rule.Dimension == "" || rule.Identifier == "" || rule.Limit <= 0 || rule.Window <= 0 {
			return false, 0, errors.New("invalid rate-limit rule")
		}
		keys = append(keys, rateLimitKey(rule.Action, rule.Dimension, rule.Identifier))
		args = append(args, rule.Limit, rule.Window.Milliseconds())
	}
	result, err := Rdb.Eval(ctx, reserveRateLimitScript, keys, args...).Result()
	if err != nil {
		return false, 0, fmt.Errorf("reserve rate limit: %w", err)
	}
	values, ok := result.([]interface{})
	if !ok || len(values) != 2 {
		return false, 0, fmt.Errorf("reserve rate limit: unexpected response %T", result)
	}
	allowed, ok := redisInt64(values[0])
	if !ok {
		return false, 0, errors.New("reserve rate limit: invalid allowed value")
	}
	retryMillis, ok := redisInt64(values[1])
	if !ok {
		return false, 0, errors.New("reserve rate limit: invalid retry value")
	}
	return allowed == 1, time.Duration(retryMillis) * time.Millisecond, nil
}

func ResetRateLimits(ctx context.Context, rules ...RateLimitRule) error {
	if Rdb == nil {
		return errors.New("redis is not initialized")
	}
	keys := make([]string, 0, len(rules))
	for _, rule := range rules {
		if rule.Action == "" || rule.Dimension == "" || rule.Identifier == "" {
			continue
		}
		keys = append(keys, rateLimitKey(rule.Action, rule.Dimension, rule.Identifier))
	}
	if len(keys) == 0 {
		return nil
	}
	return Rdb.Del(ctx, keys...).Err()
}

func redisInt64(value interface{}) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	default:
		return 0, false
	}
}
