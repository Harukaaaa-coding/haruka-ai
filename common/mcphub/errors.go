package mcphub

import (
	"errors"
	"fmt"
)

const (
	ErrorInvalidConfig       = "invalid_config"
	ErrorServerNotAllowed    = "server_not_allowed"
	ErrorServerUnavailable   = "server_unavailable"
	ErrorToolNotFound        = "tool_not_found"
	ErrorToolNotAllowed      = "tool_not_allowed"
	ErrorInvalidArguments    = "invalid_arguments"
	ErrorApprovalRequired    = "approval_required"
	ErrorApprovalInvalid     = "approval_invalid"
	ErrorApprovalNotRequired = "approval_not_required"
	ErrorTimeout             = "timeout"
	ErrorCanceled            = "canceled"
	ErrorProtocol            = "protocol_error"
	ErrorToolExecution       = "tool_execution_error"
	ErrorInternal            = "internal_error"
)

// Error exposes a stable, non-sensitive code/message while retaining the
// internal cause for errors.Is/errors.As and server-side diagnostics.
type Error struct {
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func newError(code, message string, cause error) error {
	return &Error{Code: code, Message: message, Cause: cause}
}

func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var hubErr *Error
	if errors.As(err, &hubErr) {
		return hubErr.Code
	}
	return ErrorInternal
}

func PublicErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	var hubErr *Error
	if errors.As(err, &hubErr) {
		return hubErr.Message
	}
	return "MCP Hub internal error"
}

type ApprovalRequiredError struct {
	ToolName string
	Risk     RiskLevel
}

func (e *ApprovalRequiredError) Error() string {
	return fmt.Sprintf("tool %s requires explicit approval", e.ToolName)
}

func (e *ApprovalRequiredError) HubError() *Error {
	return &Error{Code: ErrorApprovalRequired, Message: e.Error(), Cause: e}
}
