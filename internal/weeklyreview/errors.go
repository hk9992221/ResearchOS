package weeklyreview

import (
	"errors"
	"fmt"
)

// ErrorCode is the stable, machine-readable error identity of SPEC-09 §5.8.
type ErrorCode string

// The error codes of SPEC-09 §5.8.
const (
	ErrNotFound             ErrorCode = "report.not_found"
	ErrInvalidPeriod        ErrorCode = "report.invalid_period"
	ErrPeriodOccupied       ErrorCode = "report.period_occupied"
	ErrProjectArchived      ErrorCode = "project.archived"
	ErrConfirmedReadOnly    ErrorCode = "report.confirmed_read_only"
	ErrReportNotEmpty       ErrorCode = "report.not_empty"
	ErrContentConflict      ErrorCode = "report.content_conflict"
	ErrContentTooLarge      ErrorCode = "report.content_too_large"
	ErrUnresolvedViolations ErrorCode = "report.unresolved_violations"
	ErrConfirmationRequired ErrorCode = "confirmation.required"
	ErrUnsupportedFormat    ErrorCode = "export.unsupported_format"
	ErrExportExists         ErrorCode = "export.exists"
	ErrInvalidExportPath    ErrorCode = "export.invalid_path"
	ErrExportDirMissing     ErrorCode = "export.dir_missing"
	ErrAIUnavailable        ErrorCode = "ai.unavailable"
	ErrAIOutputInvalid      ErrorCode = "ai.output_invalid"
	ErrInvalidInput         ErrorCode = "input.invalid"
	ErrInvalidActor         ErrorCode = "permission.invalid_actor"
	ErrProjectNotFound      ErrorCode = "project.not_found"
	ErrInternal             ErrorCode = "internal.error"
)

// ServiceError is the typed error every service method returns.
type ServiceError struct {
	Code    ErrorCode
	Message string
	Details map[string]any
}

// Error implements error.
func (e *ServiceError) Error() string {
	if e.Message == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// serviceError builds a ServiceError.
func serviceError(code ErrorCode, format string, args ...any) *ServiceError {
	return &ServiceError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// withDetails attaches structured details to a ServiceError.
func (e *ServiceError) withDetails(details map[string]any) *ServiceError {
	e.Details = details
	return e
}

// CodeOf returns the ErrorCode of err, or the empty code for a foreign error.
func CodeOf(err error) ErrorCode {
	var se *ServiceError
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

// IsCode reports whether err carries code.
func IsCode(err error, code ErrorCode) bool { return CodeOf(err) == code }
