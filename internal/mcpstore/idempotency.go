package mcpstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"researchos/internal/mcp"
)

// Claim implements mcp.IdempotencyStore (§5.7). The check and the reservation
// happen in one BEGIN IMMEDIATE transaction (the driver opens every tx that
// way), so two concurrent calls with the same key cannot both run.
func (s *Store) Claim(ctx context.Context, clientID, key, tool, argsHash string, now time.Time) (mcp.IdempotencyClaim, error) {
	db := s.db()
	if db == nil {
		return mcp.IdempotencyClaim{}, fmt.Errorf("mcpstore: no global database")
	}
	if clientID == "" || key == "" {
		return mcp.IdempotencyClaim{}, fmt.Errorf("mcpstore: idempotency requires a client and a key")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return mcp.IdempotencyClaim{}, fmt.Errorf("mcpstore: claim idempotency: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		storedTool string
		storedHash string
		state      string
		result     sql.NullString
		expiresAt  sql.NullString
	)
	err = tx.QueryRowContext(ctx, `
		SELECT tool, args_hash, state, result_json, expires_at
		FROM mcp_idempotency WHERE client_id = ? AND key = ?`, clientID, key).
		Scan(&storedTool, &storedHash, &state, &result, &expiresAt)
	switch {
	case err == nil:
		expires, _ := parseTime(expiresAt)
		if !now.Before(expires) {
			if _, err := tx.ExecContext(ctx, `DELETE FROM mcp_idempotency WHERE client_id = ? AND key = ?`, clientID, key); err != nil {
				return mcp.IdempotencyClaim{}, fmt.Errorf("mcpstore: expire idempotency: %w", err)
			}
			break
		}
		if storedTool != tool || storedHash != argsHash {
			return mcp.IdempotencyClaim{}, mcp.IdempotencyConflict
		}
		if state == "completed" {
			return mcp.IdempotencyClaim{Replayed: true, Result: json.RawMessage(result.String)}, nil
		}
		return mcp.IdempotencyClaim{}, mcp.IdempotencyInFlight
	case errors.Is(err, sql.ErrNoRows):
		// fall through to insert
	default:
		return mcp.IdempotencyClaim{}, fmt.Errorf("mcpstore: read idempotency: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO mcp_idempotency (client_id, key, tool, args_hash, state, created_at, expires_at)
		VALUES (?, ?, ?, ?, 'pending', ?, ?)`,
		clientID, key, tool, argsHash, timeText(now), timeText(now.Add(idempotencyTTL))); err != nil {
		return mcp.IdempotencyClaim{}, fmt.Errorf("mcpstore: reserve idempotency: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return mcp.IdempotencyClaim{}, fmt.Errorf("mcpstore: claim idempotency commit: %w", err)
	}
	return mcp.IdempotencyClaim{}, nil
}

// Complete stores the first result for replay.
func (s *Store) Complete(ctx context.Context, clientID, key string, result json.RawMessage) error {
	db := s.db()
	if db == nil {
		return fmt.Errorf("mcpstore: no global database")
	}
	_, err := db.ExecContext(ctx, `
		UPDATE mcp_idempotency SET state = 'completed', result_json = ?
		WHERE client_id = ? AND key = ?`, string(result), clientID, key)
	if err != nil {
		return fmt.Errorf("mcpstore: complete idempotency: %w", err)
	}
	return nil
}

// Release removes an in-flight reservation after a failed call, so the client
// can retry the same key.
func (s *Store) Release(ctx context.Context, clientID, key string) error {
	db := s.db()
	if db == nil {
		return fmt.Errorf("mcpstore: no global database")
	}
	_, err := db.ExecContext(ctx, `
		DELETE FROM mcp_idempotency WHERE client_id = ? AND key = ? AND state = 'pending'`, clientID, key)
	if err != nil {
		return fmt.Errorf("mcpstore: release idempotency: %w", err)
	}
	return nil
}

var _ mcp.IdempotencyStore = (*Store)(nil)
