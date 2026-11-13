package mcp

import (
	"errors"
	"fmt"
)

// Code is the machine-readable tool error code (SPEC-11 §5.7).
type Code string

// The closed error-code set of §5.7.
const (
	CodeInvalidArgument   Code = "INVALID_ARGUMENT"
	CodeNotFound          Code = "NOT_FOUND"
	CodeConflict          Code = "CONFLICT"
	CodeArchivedReadOnly  Code = "ARCHIVED_READ_ONLY"
	CodeAccessMode        Code = "ACCESS_MODE_READ_ONLY"
	CodeCapabilityInvalid Code = "CAPABILITY_INVALID"
	CodeAIUnavailable     Code = "AI_UNAVAILABLE"
	CodeExternalUnavail   Code = "EXTERNAL_UNAVAILABLE"
	CodeLimitExceeded     Code = "LIMIT_EXCEEDED"
	CodeRateLimited       Code = "RATE_LIMITED"
	CodeInternal          Code = "INTERNAL"
)

// Error is a tool error with a displayable Chinese message and a code.
type Error struct {
	Code    Code
	Message string
	Details map[string]any
	cause   error
}

// Error implements error.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Code == "" {
		return e.Message
	}
	return string(e.Code) + ": " + e.Message
}

// Unwrap exposes the wrapped sentinel.
func (e *Error) Unwrap() error { return e.cause }

// NewError builds an Error.
func NewError(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Errorf builds an Error with a formatted Chinese message.
func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// WithDetail returns the error with one extra machine-readable detail.
func (e *Error) WithDetail(key string, value any) *Error {
	if e.Details == nil {
		e.Details = make(map[string]any, 2)
	}
	e.Details[key] = value
	return e
}

// Wrap attaches the cause to the error.
func (e *Error) Wrap(err error) *Error {
	e.cause = err
	return e
}

// AsError converts err to an *Error, defaulting to INTERNAL.
func AsError(err error) *Error {
	var target *Error
	if errors.As(err, &target) {
		return target
	}
	e := Errorf(CodeInternal, "服务器内部错误")
	if err != nil {
		e.cause = err
	}
	return e
}

// CodeOf returns the code of err (INTERNAL when it is not an *Error).
func CodeOf(err error) Code {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return CodeInternal
}

// IsCode reports whether err carries code.
func IsCode(err error, code Code) bool { return CodeOf(err) == code }

// errorBody is the structured error shape inside a failed tool result.
type errorBody struct {
	Code    Code           `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}
