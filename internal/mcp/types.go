package mcp

import (
	"context"
	"encoding/json"
	"time"
)

// ModuleID names the module a tool belongs to (SPEC-11 §5.2).
type ModuleID string

// The module identifiers of the tool catalog.
const (
	ModuleCore        ModuleID = "core"
	ModuleProject     ModuleID = "project"
	ModuleLiterature  ModuleID = "literature"
	ModuleNotes       ModuleID = "notes"
	ModuleIdeas       ModuleID = "ideas"
	ModuleExperiments ModuleID = "experiments"
	ModuleKanban      ModuleID = "kanban"
	ModuleWeekly      ModuleID = "weekly"
	ModuleSearch      ModuleID = "search"
)

// Category is the permission class of a tool (SPEC-11 §5.4).
type Category string

// The four permission classes.
const (
	// CategoryRead is a read-only tool; it never writes an Activity.
	CategoryRead Category = "R"
	// CategoryWrite writes directly as actor=mcp.
	CategoryWrite Category = "W"
	// CategoryConfirm enqueues an in-app confirmation.
	CategoryConfirm Category = "C"
	// CategoryJob runs as a long job (read-only or writing).
	CategoryJob Category = "J"
)

// Access modes of the server (mcp.access_mode).
const (
	AccessReadOnly  = "read_only"
	AccessReadWrite = "read_write"
)

// Transports.
const (
	TransportHTTP  = "http"
	TransportStdio = "stdio"
)

// Principal identifies the calling client and its granted access mode.
type Principal struct {
	ClientID   string
	ClientName string
	Transport  string
	AccessMode string
	// ConnectionID scopes the per-connection rate limit. HTTP sets it to the
	// client id (one token per connection); IPC sets it to the stream id.
	ConnectionID string
}

// ToolAnnotations carries the four MCP behaviour hints (always serialized).
type ToolAnnotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint"`
	IdempotentHint  bool `json:"idempotentHint"`
	OpenWorldHint   bool `json:"openWorldHint"`
}

// ToolSpec is one published tool.
type ToolSpec struct {
	Name         string          `json:"name"`
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema"`
	Annotations  ToolAnnotations `json:"annotations"`
	Category     Category        `json:"category"`
	Module       ModuleID        `json:"module"`

	// AllowArchived marks the two project lifecycle tools that stay available
	// on an archived project (project_unarchive, project_delete, §5.4 rule 2).
	AllowArchived bool `json:"-"`
	// RequiresProjectID documents that the arguments carry project_id, which
	// the archived gate reads. Derived from the schema at registration.
	RequiresProjectID bool `json:"-"`
}

// ToolBinding pairs a spec with its handler.
type ToolBinding struct {
	Spec   ToolSpec
	Handle func(ctx context.Context, p Principal, in json.RawMessage) (ToolOutcome, error)
}

// ToolProvider is implemented by each module binding package (SPEC-11 §5.2).
type ToolProvider interface {
	Module() ModuleID
	// Ready reports whether the module dependencies are usable. A non-nil
	// error hides the module's tools and makes calls report NOT_FOUND with
	// details.module.
	Ready(ctx context.Context) error
	Tools() []ToolBinding
}

// ToolOutcome is a handler result before protocol wrapping.
type ToolOutcome struct {
	// Status is "ok", "pending" or "job".
	Status     string
	Structured any
	Text       string
	// Confirmation is set when Status is "pending"; the host enqueues it.
	Confirmation *ConfirmationRequest
	// JobID is set when Status is "job" and the handler already submitted it.
	JobID string
}

// Tool outcome statuses.
const (
	StatusOK      = "ok"
	StatusPending = "pending"
	StatusJob     = "job"
)

// ContentBlock is one MCP content item.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// CallRequest is one tools/call invocation (SPEC-11 §5.2).
type CallRequest struct {
	Name           string
	Arguments      json.RawMessage
	IdempotencyKey string
	ProgressToken  any
	TraceID        string
}

// CallResult is the protocol-level tools/call result.
type CallResult struct {
	ResultType string         `json:"resultType"`
	Content    []ContentBlock `json:"content"`
	Structured any            `json:"structuredContent,omitempty"`
	IsError    bool           `json:"isError"`
	Meta       map[string]any `json:"_meta,omitempty"`
}

// ToolHost is the arbitration entry point shared by every transport.
type ToolHost interface {
	Tools(ctx context.Context, p Principal) ([]ToolSpec, error)
	Call(ctx context.Context, p Principal, req CallRequest) (CallResult, error)
}

// Runtime reports server status for the core server_status tool.
type Runtime interface {
	Status(ctx context.Context) (Status, error)
}

// Status is the server_status payload (SPEC-11 §5.3.1).
type Status struct {
	AppVersion      string   `json:"app_version"`
	MCPVersions     []string `json:"mcp_versions"`
	AccessMode      string   `json:"access_mode"`
	WorkspaceRoot   string   `json:"workspace_root"`
	Modules         []string `json:"modules"`
	AIAvailable     bool     `json:"ai_available"`
	ZoteroAvailable bool     `json:"zotero_available"`
	Transports      []string `json:"transports"`
}

// ProjectGate answers the two project-level access questions the host needs
// before dispatching a write. Implemented by the binding layer over the
// project service; the host itself never reads a database.
type ProjectGate interface {
	// IsArchived reports whether the project is archived. A missing project
	// returns a NOT_FOUND *Error.
	IsArchived(ctx context.Context, projectID string) (bool, error)
}

// SettingStore is the read side of foundation/settings (SPEC-01 §5.5). The
// mcp.* keys have no registry: consumers fall back to the defaults in
// SettingsValues.
type SettingStore interface {
	Get(ctx context.Context, key string) (string, bool, error)
}

// Logger is the foundation logging shape.
type Logger interface {
	Debug(msg string, kv ...any)
	Info(msg string, kv ...any)
	Warn(msg string, kv ...any)
	Error(msg string, kv ...any)
}

// TraceID returns a fresh opaque trace id.
type TraceID func() string

// Notifier broadcasts tool-list changes to connected streams.
type Notifier struct {
	subs map[int]chan struct{}
	next int
}

// NewNotifier returns an empty notifier.
func NewNotifier() *Notifier { return &Notifier{subs: map[int]chan struct{}{}} }

// Subscribe registers a listener. The returned cancel function must be called
// when the stream ends.
func (n *Notifier) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	if n == nil {
		return ch, func() {}
	}
	n.next++
	id := n.next
	n.subs[id] = ch
	return ch, func() {
		if _, ok := n.subs[id]; ok {
			delete(n.subs, id)
			close(ch)
		}
	}
}

// Notify wakes every listener without blocking.
func (n *Notifier) Notify() {
	if n == nil {
		return
	}
	for _, ch := range n.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// ConfirmationRequest is what a C-class handler hands to the broker.
type ConfirmationRequest struct {
	// Tool is filled in by the host.
	Tool      string
	Title     string
	Summary   string
	Impact    any
	ProjectID string
	TTL       time.Duration
	// TraceID links the request to the confirmation row (§5.10 item 10).
	TraceID string
}
