package user

import (
	myredis "GopherAI/common/redis"
	"context"
	"time"
)

const (
	loginIPLimit        = 30
	loginAccountLimit   = 10
	captchaIPLimit      = 20
	captchaEmailLimit   = 5
	captchaCooldown     = time.Minute
	loginWindow         = 5 * time.Minute
	loginAccountWindow  = 15 * time.Minute
	captchaWindow       = time.Hour
	captchaVerifyWindow = 5 * time.Minute
)

func AllowLoginAttempt(ctx context.Context, clientIP, username string) (bool, time.Duration, error) {
	return myredis.AllowRateLimits(ctx,
		myredis.RateLimitRule{Action: "login", Dimension: "ip", Identifier: clientIP, Limit: loginIPLimit, Window: loginWindow},
		myredis.RateLimitRule{Action: "login", Dimension: "account", Identifier: username, Limit: loginAccountLimit, Window: loginAccountWindow},
	)
}

func AllowCaptchaVerification(ctx context.Context, clientIP, email string) (bool, time.Duration, error) {
	return myredis.AllowRateLimits(ctx,
		myredis.RateLimitRule{Action: "captcha-verify", Dimension: "ip", Identifier: clientIP, Limit: 30, Window: captchaVerifyWindow},
		myredis.RateLimitRule{Action: "captcha-verify", Dimension: "email", Identifier: email, Limit: 5, Window: captchaVerifyWindow},
	)
}

func ClearLoginAccountLimit(ctx context.Context, username string) error {
	return myredis.ResetRateLimits(ctx,
		myredis.RateLimitRule{Action: "login", Dimension: "account", Identifier: username},
	)
}

func AllowCaptchaRequest(ctx context.Context, clientIP, email string) (bool, time.Duration, error) {
	return myredis.AllowRateLimits(ctx,
		myredis.RateLimitRule{Action: "captcha", Dimension: "ip", Identifier: clientIP, Limit: captchaIPLimit, Window: captchaWindow},
		myredis.RateLimitRule{Action: "captcha", Dimension: "email", Identifier: email, Limit: captchaEmailLimit, Window: captchaWindow},
		myredis.RateLimitRule{Action: "captcha", Dimension: "email-cooldown", Identifier: email, Limit: 1, Window: captchaCooldown},
	)
}
