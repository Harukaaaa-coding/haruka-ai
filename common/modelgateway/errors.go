package modelgateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type ErrorKind string

const (
	ErrorTimeout        ErrorKind = "timeout"
	ErrorCanceled       ErrorKind = "canceled"
	ErrorAuthentication ErrorKind = "authentication"
	ErrorRateLimited    ErrorKind = "rate_limited"
	ErrorUnavailable    ErrorKind = "unavailable"
	ErrorInvalidRequest ErrorKind = "invalid_request"
	ErrorUpstream       ErrorKind = "upstream_error"
)

// GatewayError provides a stable category without returning provider response
// bodies, endpoints or credentials to callers and logs.
type GatewayError struct {
	Kind      ErrorKind
	Provider  string
	Operation string
	cause     error
}

func (e *GatewayError) Error() string {
	provider := strings.TrimSpace(e.Provider)
	if provider == "" {
		provider = "model"
	}
	operation := strings.TrimSpace(e.Operation)
	if operation == "" {
		operation = "request"
	}
	return fmt.Sprintf("model gateway %s %s failed: %s", provider, operation, e.Kind)
}

func (e *GatewayError) Unwrap() error { return e.cause }

func NewUnavailableError(provider, operation string, cause error) error {
	return &GatewayError{Kind: ErrorUnavailable, Provider: provider, Operation: operation, cause: cause}
}

func NormalizeError(provider, operation string, err error) error {
	if err == nil {
		return nil
	}
	var normalized *GatewayError
	if errors.As(err, &normalized) {
		return err
	}

	kind := ErrorUpstream
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		kind = ErrorTimeout
	case errors.Is(err, context.Canceled):
		kind = ErrorCanceled
	default:
		message := strings.ToLower(err.Error())
		switch {
		case containsAny(message, "unauthorized", "authentication", "invalid api key", "invalid_api_key", "status code: 401", "error code: 401", "status=401"):
			kind = ErrorAuthentication
		case containsAny(message, "rate limit", "rate_limit", "too many requests", "status code: 429", "error code: 429", "status=429"):
			kind = ErrorRateLimited
		case containsAny(message, "connection refused", "no such host", "service unavailable", "modelnotopen", "status code: 502", "status code: 503", "error code: 502", "error code: 503", "status=502", "status=503"):
			kind = ErrorUnavailable
		case containsAny(message, "invalid request", "bad request", "status code: 400", "error code: 400", "status=400"):
			kind = ErrorInvalidRequest
		}
	}
	return &GatewayError{Kind: kind, Provider: provider, Operation: operation, cause: err}
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
