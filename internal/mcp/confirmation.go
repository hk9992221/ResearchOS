package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ConfirmationState is the §5.5 CAS state machine.
type ConfirmationState string

// The confirmation states.
const (
	ConfirmationRequested ConfirmationState = "requested"
	ConfirmationApproved  ConfirmationState = "approved"
	ConfirmationExecuting ConfirmationState = "executing"
	ConfirmationExecuted  ConfirmationState = "executed"
	ConfirmationFailed    ConfirmationState = "failed"
	ConfirmationDenied    ConfirmationState = "denied"
	ConfirmationExpired   ConfirmationState = "expired"
	ConfirmationCancelled ConfirmationState = "cancelled"
)

// Confirmation is one row of mcp_confirmation (SPEC-11 §4.2).
type Confirmation struct {
	ID         string            `json:"confirmation_id"`
	Tool       string            `json:"tool"`
	Args       json.RawMessage   `json:"args,omitempty"`
	ArgsHash   string            `json:"args_hash"`
	Title      string            `json:"title"`
	Summary    string            `json:"summary"`
	Impact     json.RawMessage   `json:"impact,omitempty"`
	ProjectID  string            `json:"project_id,omitempty"`
	ClientID   string            `json:"client_id"`
	State      ConfirmationState `json:"state"`
	CreatedAt  time.Time         `json:"created_at"`
	ExpiresAt  time.Time         `json:"expires_at"`
	ResolvedAt *time.Time        `json:"resolved_at,omitempty"`
	ResolvedBy string            `json:"resolved_by,omitempty"`
	Result     json.RawMessage   `json:"result,omitempty"`
	ErrorCode  string            `json:"error_code,omitempty"`
	TraceID    string            `json:"-"`
}

// ConfirmationFilter narrows ConfirmationBroker.List.
type ConfirmationFilter struct {
	State     ConfirmationState
	ProjectID string
	Limit     int
}

// ConfirmationBroker is the MCP-facing queue port. It deliberately has no
// approve path: MCP can create, read and cancel its own requests only
// (SPEC-11 §5.5.1, §8.4 item 18).
type ConfirmationBroker interface {
	// Enqueue stores a requested confirmation bound to (tool, args hash,
	// project, client).
	Enqueue(ctx context.Context, p Principal, req ConfirmationRequest, args json.RawMessage, argsHash string) (Confirmation, error)
	// Get returns one confirmation visible to p (same client) or any for the
	// local UI.
	Get(ctx context.Context, p Principal, id string) (Confirmation, error)
	// List returns confirmations, newest first.
	List(ctx context.Context, p Principal, f ConfirmationFilter) ([]Confirmation, error)
	// CancelByClient cancels a requested confirmation owned by p.ClientID.
	CancelByClient(ctx context.Context, p Principal, id string) error
}

// ConfirmationAdmin is the in-app side of the queue: the confirmation center
// resolves requests, the executor reports the outcome, and startup recovers
// interrupted approvals. It is wired only into the app, never into a tool.
type ConfirmationAdmin interface {
	ConfirmationBroker
	// Resolve applies requested→approved|denied as a CAS. A non-requested
	// state yields a CONFLICT *Error.
	Resolve(ctx context.Context, id string, approve bool, by string) (Confirmation, error)
	// ClaimExecution performs the single approved→executing CAS that makes an
	// approval executable at most once (§5.5.1).
	ClaimExecution(ctx context.Context, id string) (Confirmation, error)
	// Finish records executed/failed with a result payload.
	Finish(ctx context.Context, id string, result any, errCode string) (Confirmation, error)
	// RecoverAtStartup expires stale requested rows and rolls approved rows
	// that never executed back to requested (SPEC-11 §5.5.1).
	RecoverAtStartup(ctx context.Context) (RecoverReport, error)
	// Purge removes settled rows older than before.
	Purge(ctx context.Context, before time.Time) (int, error)
}

// RecoverReport summarizes startup recovery.
type RecoverReport struct {
	Expired  int
	Reverted int
}

// JobState is the §5.6 job state machine.
type JobState string

// The job states.
const (
	JobQueued    JobState = "queued"
	JobRunning   JobState = "running"
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
	JobCancelled JobState = "cancelled"
)

// Job is one mcp_job row.
type Job struct {
	ID         string          `json:"job_id"`
	Tool       string          `json:"tool"`
	ProjectID  string          `json:"project_id,omitempty"`
	ClientID   string          `json:"client_id"`
	State      JobState        `json:"state"`
	Progress   int             `json:"progress"`
	Stage      string          `json:"stage"`
	ReadOnly   bool            `json:"read_only"`
	Params     json.RawMessage `json:"params,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      json.RawMessage `json:"error,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	StartedAt  *time.Time      `json:"started_at,omitempty"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
	TraceID    string          `json:"-"`
}

// ProgressFunc reports monotonic progress in 0–100 plus a short Chinese stage.
type ProgressFunc func(percent int, stage string)

// JobRequest submits work to the runner.
type JobRequest struct {
	Tool      string
	ProjectID string
	ClientID  string
	ReadOnly  bool
	Params    json.RawMessage
	TraceID   string
	Run       func(ctx context.Context, progress ProgressFunc) (any, error)
}

// JobFilter narrows JobRunner.List.
type JobFilter struct {
	ProjectID string
	State     JobState
	Limit     int
}

// JobRunner owns long-running jobs (SPEC-11 §5.6).
type JobRunner interface {
	Submit(ctx context.Context, p Principal, req JobRequest) (Job, error)
	Get(ctx context.Context, p Principal, id string) (Job, error)
	List(ctx context.Context, p Principal, f JobFilter) ([]Job, error)
	Cancel(ctx context.Context, p Principal, id string) error
	// RecoverAtStartup marks running jobs failed(interrupted) and drops stale
	// temp state. It never retries.
	RecoverAtStartup(ctx context.Context) error
	// Purge removes terminal rows older than before.
	Purge(ctx context.Context, before time.Time) (int, error)
}

// IdempotencyConflict reports a reused key with different tool or arguments.
var IdempotencyConflict = errors.New("mcp: idempotency key reused with different arguments")

// IdempotencyInFlight reports a reused key whose first call has not finished.
var IdempotencyInFlight = errors.New("mcp: idempotency key already in flight")

// IdempotencyClaim is the outcome of claiming a key.
type IdempotencyClaim struct {
	Replayed bool
	Result   json.RawMessage
}

// IdempotencyStore persists (client_id, key) replay records for 24 hours.
type IdempotencyStore interface {
	Claim(ctx context.Context, clientID, key, tool, argsHash string, now time.Time) (IdempotencyClaim, error)
	Complete(ctx context.Context, clientID, key string, result json.RawMessage) error
	Release(ctx context.Context, clientID, key string) error
	Purge(ctx context.Context, before time.Time) (int, error)
}

// Client is one mcp_client row.
type Client struct {
	ID         string     `json:"client_id"`
	Name       string     `json:"name"`
	Transport  string     `json:"transport"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	Enabled    bool       `json:"enabled"`
}

// Authenticator resolves a bearer token to a principal. The store hashes the
// presented token and never logs it (SPEC-11 §5.8).
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (Principal, error)
}
