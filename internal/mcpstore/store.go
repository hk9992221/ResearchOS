// Package mcpstore implements the SPEC-11 runtime-state persistence for the MCP
// server: the confirmation queue, the job runner, the client token registry and
// the write-idempotency records, all in global.db (§4.2, §4.3).
//
// Only this package (and its migrations package) knows SQL. The protocol and
// host packages depend on the ports declared in internal/mcp.
package mcpstore

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/workspace"
	"researchos/internal/mcp"
)

// DefaultTTL is the confirmation lifetime when no setting is supplied.
const DefaultTTL = 900 * time.Second

// idempotencyTTL is the replay window of §5.7.
const idempotencyTTL = 24 * time.Hour

// Options configures a Store.
type Options struct {
	// Clock supplies timestamps and UUIDv7 prefixes. Nil means clock.System.
	Clock clock.Clock
	// ConfirmationTTL resolves mcp.confirmation_ttl_seconds. Nil means 900s.
	ConfirmationTTL func(ctx context.Context) time.Duration
	// Logger receives non-fatal warnings.
	Logger mcp.Logger
}

// Store owns the four runtime tables. It is safe for concurrent use.
type Store struct {
	global *workspace.GlobalHandle
	clock  clock.Clock
	ttl    func(ctx context.Context) time.Duration
	logger mcp.Logger

	mu           sync.Mutex
	cancels      map[string]context.CancelFunc
	projectWrite map[string]int
	totalWrite   int
}

// New builds a Store over the workspace global handle.
func New(global *workspace.GlobalHandle, opts Options) *Store {
	c := opts.Clock
	if c == nil {
		c = clock.System{}
	}
	ttl := opts.ConfirmationTTL
	if ttl == nil {
		ttl = func(context.Context) time.Duration { return DefaultTTL }
	}
	return &Store{
		global:       global,
		clock:        c,
		ttl:          ttl,
		logger:       opts.Logger,
		cancels:      make(map[string]context.CancelFunc),
		projectWrite: make(map[string]int),
	}
}

func (s *Store) db() *sql.DB {
	if s == nil || s.global == nil {
		return nil
	}
	return s.global.DB()
}

func (s *Store) now() time.Time { return s.clock.Now() }

func (s *Store) nowText() string { return clock.Format(s.now()) }

// parseTime reads a nullable TEXT timestamp.
func parseTime(ns sql.NullString) (time.Time, bool) {
	if !ns.Valid || ns.String == "" {
		return time.Time{}, false
	}
	t, err := clock.Parse(ns.String)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func parseTimePtr(ns sql.NullString) *time.Time {
	t, ok := parseTime(ns)
	if !ok {
		return nil
	}
	return &t
}

func timeText(t time.Time) string { return clock.Format(t) }

func timePtrText(t *time.Time) any {
	if t == nil {
		return nil
	}
	return clock.Format(*t)
}

// scanner is implemented by *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// RecoverAtStartup reconciles the runtime tables after a process restart:
// expired requests expire, approved-but-unexecuted requests return to
// requested, and unfinished jobs fail as interrupted (§5.5.1, §5.6).
func (s *Store) Recover(ctx context.Context) (mcp.RecoverReport, error) {
	var report mcp.RecoverReport
	db := s.db()
	if db == nil {
		return report, fmt.Errorf("mcpstore: no global database")
	}
	now := s.nowText()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return report, fmt.Errorf("mcpstore: recover: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		UPDATE mcp_confirmation SET state = 'expired', resolved_at = ?, resolved_by = 'system'
		WHERE state = 'requested' AND expires_at <= ?`, now, now)
	if err != nil {
		return report, fmt.Errorf("mcpstore: recover confirmations: %w", err)
	}
	expired, _ := res.RowsAffected()
	report.Expired = int(expired)

	res, err = tx.ExecContext(ctx, `
		UPDATE mcp_confirmation SET state = 'requested', resolved_at = NULL, resolved_by = NULL
		WHERE state = 'approved' AND expires_at > ?`, now)
	if err != nil {
		return report, fmt.Errorf("mcpstore: recover approved: %w", err)
	}
	reverted, _ := res.RowsAffected()
	report.Reverted = int(reverted)

	if _, err := tx.ExecContext(ctx, `
		UPDATE mcp_confirmation SET state = 'expired', resolved_at = ?, resolved_by = 'system'
		WHERE state = 'approved' AND expires_at <= ?`, now, now); err != nil {
		return report, fmt.Errorf("mcpstore: expire approved: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE mcp_confirmation SET state = 'failed', resolved_at = ?, resolved_by = 'system',
			error_code = 'INTERRUPTED'
		WHERE state = 'executing'`, now); err != nil {
		return report, fmt.Errorf("mcpstore: fail interrupted executions: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM mcp_idempotency WHERE state = 'pending'`); err != nil {
		return report, fmt.Errorf("mcpstore: clear stale idempotency: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE mcp_job SET state = 'failed', finished_at = ?,
			error_json = '{"code":"INTERRUPTED","message":"应用重启，作业中断，不自动重试"}'
		WHERE state IN ('queued','running')`, now); err != nil {
		return report, fmt.Errorf("mcpstore: recover jobs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("mcpstore: recover commit: %w", err)
	}
	return report, nil
}

// Purge removes settled runtime rows older than before. Retention is governed
// by mcp.retention_days (§5.5.1, §5.6). Idempotency rows follow their own TTL.
func (s *Store) Purge(ctx context.Context, before time.Time) (int, error) {
	db := s.db()
	if db == nil {
		return 0, fmt.Errorf("mcpstore: no global database")
	}
	stamp := timeText(before)
	total := 0
	for _, stmt := range []string{
		`DELETE FROM mcp_confirmation WHERE state IN ('executed','failed','denied','expired','cancelled') AND created_at < ?`,
		`DELETE FROM mcp_job WHERE state IN ('succeeded','failed','cancelled') AND created_at < ?`,
		`DELETE FROM mcp_idempotency WHERE expires_at < ?`,
	} {
		res, err := db.ExecContext(ctx, stmt, stamp)
		if err != nil {
			return total, fmt.Errorf("mcpstore: purge: %w", err)
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}
