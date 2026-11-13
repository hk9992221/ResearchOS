// Package provider owns ProviderConfig CRUD, the protocol capability matrix
// and Base URL rules (SPEC-02 §5.2, §5.3; SPEC-00 §7). It is the only writer
// of the providers table.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Protocol is one of the four supported upstream wire protocols. Adding a
// protocol requires a SPEC-00 revision.
type Protocol string

// The four registered protocols (SPEC-00 §7).
const (
	ProtocolOpenAIChat      Protocol = "openai_chat"
	ProtocolOpenAIResponses Protocol = "openai_responses"
	ProtocolGemini          Protocol = "gemini"
	ProtocolAnthropic       Protocol = "anthropic"
)

// Capability is a protocol-derived ability.
type Capability string

// Registered capabilities (SPEC-02 §4.2).
const (
	CapabilityChat      Capability = "chat"
	CapabilityEmbedding Capability = "embedding"
	CapabilityRerank    Capability = "rerank"
)

// Secret is key material that exists only transiently on the call stack
// (SPEC-02 §5.7). It must never be serialized or logged.
type Secret []byte

// Sentinel validation and lifecycle errors. UI maps these to visible messages.
var (
	ErrNotFound        = errors.New("provider not found")
	ErrInvalidName     = errors.New("provider name must not be empty")
	ErrInvalidProtocol = errors.New("invalid protocol")
	ErrInvalidBaseURL  = errors.New("invalid base url")
	ErrKeyConflict     = errors.New("api key replacement and key clearing are mutually exclusive")
	ErrSecretStoreGone = errors.New("os secret store unavailable")
)

// ProtocolConflictError rejects a protocol change that would strand role
// assignments (SPEC-02 §5.3.2, AC-01.5).
type ProtocolConflictError struct {
	ProviderID string
	Conflicts  []RoleConflict
}

// RoleConflict names one role whose required capability the new protocol
// cannot provide.
type RoleConflict struct {
	Role     string
	Required Capability
}

func (e *ProtocolConflictError) Error() string {
	parts := make([]string, len(e.Conflicts))
	for i, c := range e.Conflicts {
		parts[i] = fmt.Sprintf("%s(requires %s)", c.Role, c.Required)
	}
	return "provider: protocol change would break roles: " + strings.Join(parts, ", ")
}

// InUseError rejects deleting a provider that roles still reference (§5.3.3,
// AC-01.4).
type InUseError struct {
	ProviderID string
	Roles      []string
}

func (e *InUseError) Error() string {
	return "provider: in use by roles: " + strings.Join(e.Roles, ", ")
}

// capabilities maps each protocol to its abilities; it is the single source
// of truth shared by UI filtering and server-side validation (§5.2).
var capabilities = map[Protocol]map[Capability]bool{
	ProtocolOpenAIChat:      {CapabilityChat: true, CapabilityEmbedding: true},
	ProtocolOpenAIResponses: {CapabilityChat: true, CapabilityEmbedding: true},
	ProtocolGemini:          {CapabilityChat: true, CapabilityEmbedding: true},
	ProtocolAnthropic:       {CapabilityChat: true},
}

// Capabilities returns the ability set derived from the protocol. Unknown
// protocols are an error, never an empty set.
func Capabilities(p Protocol) (map[Capability]bool, error) {
	caps, ok := capabilities[p]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrInvalidProtocol, p)
	}
	out := make(map[Capability]bool, len(caps))
	for c, v := range caps {
		out[c] = v
	}
	return out, nil
}

// Has reports whether protocol p offers capability c; unknown protocols have
// no capabilities.
func Has(p Protocol, c Capability) bool {
	return capabilities[p][c]
}

// CapabilitiesList returns the protocol's capabilities in stable order for
// persistence as a JSON array.
func CapabilitiesList(p Protocol) []Capability {
	var out []Capability
	for _, c := range []Capability{CapabilityChat, CapabilityEmbedding, CapabilityRerank} {
		if capabilities[p][c] {
			out = append(out, c)
		}
	}
	return out
}

// CapabilitiesJSON encodes the protocol capability list.
func CapabilitiesJSON(p Protocol) (string, error) {
	raw, err := json.Marshal(CapabilitiesList(p))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// DefaultBaseURL is the prefilled upstream address when a provider is created
// (§5.2 table); the user's explicit value is what gets stored.
func DefaultBaseURL(p Protocol) string {
	switch p {
	case ProtocolOpenAIChat, ProtocolOpenAIResponses:
		return "https://api.openai.com/v1"
	case ProtocolGemini:
		return "https://generativelanguage.googleapis.com/v1beta"
	case ProtocolAnthropic:
		return "https://api.anthropic.com/v1"
	}
	return ""
}

// NormalizeBaseURL validates and canonicalizes a user-supplied address:
// non-empty, http/https only, no embedded credentials, no trailing slash.
// localhost and private addresses are allowed (§5.2 Base URL 规则).
func NormalizeBaseURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidBaseURL)
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%w: %q", ErrInvalidBaseURL, raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%w: scheme %q", ErrInvalidBaseURL, u.Scheme)
	}
	if u.User != nil {
		return "", fmt.Errorf("%w: userinfo in %q", ErrInvalidBaseURL, raw)
	}
	return strings.TrimRight(s, "/"), nil
}

// ValidProtocol reports whether p is one of the four registered protocols.
func ValidProtocol(p Protocol) bool {
	_, ok := capabilities[p]
	return ok
}

// ProviderConfig is the internal row shape, exactly SPEC-00 §7. APIKeyRef
// never leaves this package except toward role/gateway resolution; views use
// ProviderView instead.
type ProviderConfig struct {
	ID        string
	Name      string
	Protocol  Protocol
	BaseURL   string
	APIKeyRef string
	Enabled   bool
}

// HasKey reports whether the provider is configured with a secret reference.
// An empty reference is a valid keyless (local) service, not an error
// (SPEC-00 v1.1 §7).
func (c ProviderConfig) HasKey() bool { return c.APIKeyRef != "" }

// TestResult is the outcome of a connection test (§5.3.5). ErrorKind is an
// aierr.ErrorKind string; the test never throws into the normal UI flow.
type TestResult struct {
	OK         bool
	LatencyMS  int64
	ModelCount int
	ErrorKind  string
	Message    string
	TestedAt   time.Time
}

// ProviderView is the outward shape for UI and MCP: it carries no APIKeyRef
// and no key material (§5.3, AC-02.2, AC-14.1).
type ProviderView struct {
	ID              string
	Name            string
	Protocol        Protocol
	BaseURL         string
	HasKey          bool
	Enabled         bool
	ModelsFetchedAt *time.Time
	ModelsError     string
	LastTest        *TestResult
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// CreateProviderInput is the create payload. APIKey empty means a keyless
// local service (§4.5).
type CreateProviderInput struct {
	Name     string
	Protocol Protocol
	BaseURL  string
	APIKey   string
	Enabled  bool
}

// UpdateProviderInput is a patch payload. A nil field means "unchanged".
type UpdateProviderInput struct {
	Name     *string
	Protocol *Protocol
	BaseURL  *string
	APIKey   *string // nil=不变；非 nil=替换
	ClearKey bool    // true 时删除密钥；与 APIKey 互斥
	Enabled  *bool
}

// UpstreamModel is one entry of the upstream model listing before caching
// (§5.7). Capabilities may narrow the protocol set, never widen it.
type UpstreamModel struct {
	ModelID      string
	DisplayName  string
	Capabilities []Capability
	Raw          json.RawMessage
}

// ModelLister is the narrow seam ProviderService and ModelService need from
// the protocol layer. Keeping it here avoids a provider→protocol import
// cycle; protocol.Factory satisfies it structurally.
type ModelLister interface {
	ListModels(ctx context.Context, cfg ProviderConfig, key Secret) ([]UpstreamModel, error)
}

// Service is the ProviderService business entry point (SPEC-02 §5.3).
type Service interface {
	List(ctx context.Context) ([]ProviderView, error)
	Get(ctx context.Context, id string) (ProviderView, error)
	Create(ctx context.Context, in CreateProviderInput) (ProviderView, error)
	Update(ctx context.Context, id string, in UpdateProviderInput) (ProviderView, error)
	Delete(ctx context.Context, id string) error
	SetEnabled(ctx context.Context, id string, enabled bool) error
	Test(ctx context.Context, id string) (TestResult, error)
}

// TimeoutList bounds a connection test / model listing call (§5.8.2).
const TimeoutList = 30 * time.Second
