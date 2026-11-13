// Package aierr defines the AI failure contract shared by protocol adapters
// and the gateway (SPEC-02 §5.7, §5.8). It is a leaf package so that adapters
// can map upstream failures to AIError without importing the gateway.
package aierr

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ErrorKind is the stable machine-readable classification of an AI failure.
type ErrorKind string

// Failure kinds (SPEC-02 §5.8).
const (
	ErrKindAuth              ErrorKind = "auth"
	ErrKindRateLimit         ErrorKind = "rate_limited"
	ErrKindTimeout           ErrorKind = "timeout"
	ErrKindNetwork           ErrorKind = "network"
	ErrKindInvalidRequest    ErrorKind = "invalid_request"
	ErrKindModelNotFound     ErrorKind = "model_not_found"
	ErrKindUnsupported       ErrorKind = "unsupported_capability"
	ErrKindContextTooLong    ErrorKind = "context_too_long"
	ErrKindServer            ErrorKind = "server_error"
	ErrKindCanceled          ErrorKind = "canceled"
	ErrKindRoleUnassigned    ErrorKind = "role_unassigned"
	ErrKindProviderDisabled  ErrorKind = "provider_disabled"
	ErrKindSecretUnavailable ErrorKind = "secret_unavailable"
	// ErrKindModelUnavailable reports an assigned model that is not cached or
	// no longer available; the role view surfaces the same problem (§5.6).
	ErrKindModelUnavailable ErrorKind = "model_unavailable"
	// ErrKindPersist reports a local database failure after a successful
	// generation (§5.9.3); it is never retried against the upstream.
	ErrKindPersist ErrorKind = "persist_failed"
)

// AIError is the single typed error crossing the gateway boundary. Message is
// always sanitized: it never contains an API key, Authorization header or
// request body (SPEC-02 §5.3.6, §5.8.6).
type AIError struct {
	Kind         ErrorKind
	ProviderID   string
	ProviderName string
	Protocol     string
	Model        string
	StatusCode   int
	RetryAfter   time.Duration
	Attempts     int
	Message      string // 已脱敏
	Cause        error
}

// Error renders the full locating context the UI must show (§5.8.6).
func (e *AIError) Error() string {
	var b strings.Builder
	b.WriteString("ai: ")
	b.WriteString(string(e.Kind))
	if e.ProviderName != "" {
		fmt.Fprintf(&b, ": provider %q", e.ProviderName)
	}
	if e.Protocol != "" {
		proto := e.Protocol
		if e.Model != "" {
			proto += "/" + e.Model
		}
		b.WriteString(" (" + proto + ")")
	}
	if e.StatusCode != 0 {
		fmt.Fprintf(&b, " [HTTP %d]", e.StatusCode)
	}
	if e.Attempts > 0 {
		fmt.Fprintf(&b, " attempts=%d", e.Attempts)
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	return b.String()
}

// Unwrap exposes the transport or upstream cause for errors.Is matching.
func (e *AIError) Unwrap() error { return e.Cause }

var (
	skKeyRe    = regexp.MustCompile(`sk-[A-Za-z0-9_\-]{4,}`)
	bearerRe   = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/\-=:]+`)
	keyValueRe = regexp.MustCompile(`(?i)\b(api[_-]?key|authorization|x[-_]api[-_]key|x[-_]goog[-_]api[-_]key)\s*[=:]\s*"?[^",\s}]+`)
)

// Redact removes key-like material from an upstream or transport message
// before it can reach an error, log line or the database. It is best-effort:
// callers holding the exact secret should use RedactWith.
func Redact(s string) string {
	s = bearerRe.ReplaceAllString(s, "Bearer ***")
	s = skKeyRe.ReplaceAllString(s, "sk-***")
	s = keyValueRe.ReplaceAllString(s, "$1=***")
	return Truncate(s)
}

// RedactWith additionally scrubs the literal secret bytes from the message.
func RedactWith(s string, secrets ...[]byte) string {
	for _, sec := range secrets {
		if len(sec) == 0 {
			continue
		}
		s = strings.ReplaceAll(s, string(sec), "***")
	}
	return Redact(s)
}

// Truncate bounds an error message at a stable length (200 runes), collapsing
// newlines so single-line logs and TEXT columns stay readable.
func Truncate(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= 200 {
		return s
	}
	return string(r[:197]) + "..."
}
