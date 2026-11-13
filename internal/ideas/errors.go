package ideas

import (
	"errors"
	"fmt"
)

// ErrorCode is the stable, machine-readable error identity of SPEC-06 §5.9.
type ErrorCode string

// The error codes of SPEC-06 §5.9 plus the documented additive codes.
const (
	ErrIdeaNotFound            ErrorCode = "idea.not_found"
	ErrIdeaDeleted             ErrorCode = "idea.deleted"
	ErrEvidenceNotFound        ErrorCode = "evidence.not_found"
	ErrEvidenceDeleted         ErrorCode = "evidence.deleted"
	ErrEvidenceConfirmed       ErrorCode = "evidence.confirmed"
	ErrEvidenceDirectionLocked ErrorCode = "evidence.direction_locked"
	ErrEvidenceNotAIDraft      ErrorCode = "evidence.not_ai_draft"
	ErrInvalidStatus           ErrorCode = "idea.invalid_status"
	ErrRejectionReasonRequired ErrorCode = "idea.rejection_reason_required"
	ErrNotRejected             ErrorCode = "idea.not_rejected"
	ErrInvalidSource           ErrorCode = "evidence.invalid_source"
	ErrSourceUnavailable       ErrorCode = "evidence.source_unavailable"
	ErrSourceEmpty             ErrorCode = "evidence.source_empty"
	ErrProjectReadOnly         ErrorCode = "project.read_only"
	ErrInvalidActor            ErrorCode = "permission.invalid_actor"
	ErrAIDisabled              ErrorCode = "ai.disabled"
	ErrAIOutputInvalid         ErrorCode = "ai.output_invalid"
	ErrConfirmationRequired    ErrorCode = "confirmation.required"
	ErrLineageTooDeep          ErrorCode = "idea.lineage_too_deep"

	// Additive codes documented in doc.go.
	ErrInvalidInput    ErrorCode = "input.invalid"
	ErrProjectNotFound ErrorCode = "project.not_found"
	ErrTargetNotFound  ErrorCode = "relation.target_not_found"
	ErrTargetDeleted   ErrorCode = "relation.target_deleted"
)

// ServiceError is the typed error every service method returns. UI localises
// by Code; MCP returns Code plus Message (SPEC-11).
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
