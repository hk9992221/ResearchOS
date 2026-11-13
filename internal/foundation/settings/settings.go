// Package settings is the typed KV store over the workspace-level settings
// table and the per-project project_settings table (SPEC-01 §5.5). Both tables
// share the same shape (key, value, updated_at), so one implementation serves
// both; the constructor chooses the table.
package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
)

const (
	workspaceTable = "settings"
	projectTable   = "project_settings"
)

// keyPattern is the registered key namespace: a module segment followed by one
// or more dot-separated segments.
var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9][a-z0-9_-]*)+$`)

// Store is bound to one database and one settings table.
type Store struct {
	db    *sql.DB
	table string
	clock clock.Clock
}

// NewWorkspace returns a store over global.db's settings table.
func NewWorkspace(db *sql.DB) *Store {
	return &Store{db: db, table: workspaceTable, clock: clock.System{}}
}

// NewProject returns a store over project.db's project_settings table.
func NewProject(db *sql.DB) *Store {
	return &Store{db: db, table: projectTable, clock: clock.System{}}
}

// WithClock replaces the clock used for updated_at. It returns the store for
// chaining and must be called before concurrent use.
func (s *Store) WithClock(c clock.Clock) *Store {
	if c != nil {
		s.clock = c
	}
	return s
}

// Get returns the raw value and whether the key exists.
func (s *Store) Get(ctx context.Context, key string) (string, bool, error) {
	if err := validateKey(key); err != nil {
		return "", false, err
	}
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM `+s.table+` WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("settings: get %s: %w", key, err)
	}
	return value, true, nil
}

// Set upserts the value and stamps updated_at. A nil tx writes on the store's
// own connection.
func (s *Store) Set(ctx context.Context, tx *sql.Tx, key, value string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	_, err := s.exec(ctx, tx,
		`INSERT INTO `+s.table+` (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, clock.Format(s.clock.Now()))
	if err != nil {
		return fmt.Errorf("settings: set %s: %w", key, err)
	}
	return nil
}

// Put upserts the value outside an explicit transaction, for callers that
// have no transaction of their own, such as the UI preference store.
func (s *Store) Put(ctx context.Context, key, value string) error {
	return s.Set(ctx, nil, key, value)
}

// Delete removes the key. Deleting a missing key is a no-op.
func (s *Store) Delete(ctx context.Context, tx *sql.Tx, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if _, err := s.exec(ctx, tx, `DELETE FROM `+s.table+` WHERE key = ?`, key); err != nil {
		return fmt.Errorf("settings: delete %s: %w", key, err)
	}
	return nil
}

// GetJSON decodes a JSON value. A missing key returns (false, nil).
func (s *Store) GetJSON(ctx context.Context, key string, dst any) (bool, error) {
	raw, ok, err := s.Get(ctx, key)
	if err != nil || !ok {
		return ok, err
	}
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return true, fmt.Errorf("settings: decode %s: %w", key, err)
	}
	return true, nil
}

// SetJSON stores compact JSON for v.
func (s *Store) SetJSON(ctx context.Context, tx *sql.Tx, key string, v any) error {
	if err := validateKey(key); err != nil {
		return err
	}
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("settings: encode %s: %w", key, err)
	}
	return s.Set(ctx, tx, key, string(body))
}

// List returns every key with the given prefix ("" means all). Prefix matching
// is literal, so '_' and '%' in the prefix are not wildcards.
func (s *Store) List(ctx context.Context, prefix string) (map[string]string, error) {
	query := `SELECT key, value FROM ` + s.table
	var args []any
	if prefix != "" {
		query += ` WHERE key LIKE ? ESCAPE '\'`
		args = append(args, escapeLike(prefix)+"%")
	}
	query += ` ORDER BY key`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("settings: list %q: %w", prefix, err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("settings: list %q: %w", prefix, err)
		}
		out[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("settings: list %q: %w", prefix, err)
	}
	return out, nil
}

func (s *Store) exec(ctx context.Context, tx *sql.Tx, query string, args ...any) (sql.Result, error) {
	if tx != nil {
		return tx.ExecContext(ctx, query, args...)
	}
	return s.db.ExecContext(ctx, query, args...)
}

func validateKey(key string) error {
	if !keyPattern.MatchString(key) {
		return fmt.Errorf("%w: %q", errs.ErrBadSettingKey, key)
	}
	return nil
}

// escapeLike makes a prefix literal inside a LIKE pattern with ESCAPE '\'.
func escapeLike(prefix string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(prefix)
}

// ResolvePriority reads a setting with the §5.5 precedence: the project store
// first, then the workspace compatibility key "project.<projectID>.<key>", then
// not-found. Only the export keys defined by SPEC-03 use this path; callers
// write to the project store.
func ResolvePriority(ctx context.Context, project, workspace *Store, projectID, key string) (string, bool, error) {
	if err := validateKey(key); err != nil {
		return "", false, err
	}
	if project != nil {
		if value, ok, err := project.Get(ctx, key); err != nil || ok {
			return value, ok, err
		}
	}
	if workspace != nil {
		compat := "project." + projectID + "." + key
		return workspace.Get(ctx, compat)
	}
	return "", false, nil
}
