package zotero

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"researchos/internal/literature"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"
)

const (
	sqliteBusyTimeoutMS = 3000
	sqliteRetryDelay    = 500 * time.Millisecond
)

// openReadOnlySQLite opens an existing SQLite database without ever writing to
// it: mode=ro, query_only(1) and a friendly busy timeout. immutable=1 is
// deliberately absent (SPEC-04 §5.2): the database may be live.
func openReadOnlySQLite(ctx context.Context, path string, busyMS int) (*sql.DB, error) {
	if busyMS <= 0 {
		busyMS = sqliteBusyTimeoutMS
	}
	db, err := sql.Open("sqlite", readOnlyDSN(path, busyMS))
	if err != nil {
		return nil, fmt.Errorf("zotero: 打开 %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		if isBusy(err) {
			return nil, fmt.Errorf("%w: %v", literature.ErrZoteroBusy, err)
		}
		return nil, fmt.Errorf("zotero: 打开 %s: %w", path, err)
	}
	return db, nil
}

// readOnlyDSN builds the non-invasive DSN: mode=ro, query_only(1), a busy
// timeout, and deliberately no immutable=1 (SPEC-04 §5.2, O-19).
func readOnlyDSN(path string, busyMS int) string {
	return "file:" + escapeDSNPath(filepath.ToSlash(path)) +
		"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(" + strconv.Itoa(busyMS) + ")"
}

// escapeDSNPath percent-encodes the characters that are meaningful inside a
// SQLite URI filename.
func escapeDSNPath(p string) string {
	return strings.NewReplacer(
		"%", "%25",
		"?", "%3F",
		"#", "%23",
		" ", "%20",
	).Replace(p)
}

// readZoteroSchemaVersion reads PRAGMA user_version and, when it is zero,
// falls back to the legacy `version` table. A missing/unknown version is not an
// error: known is false and the caller records a warning.
func readZoteroSchemaVersion(ctx context.Context, path string) (version int, known bool, err error) {
	db, err := openReadOnlySQLite(ctx, path, 1000)
	if err != nil {
		return 0, false, err
	}
	defer db.Close()

	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, false, err
	}
	if version > 0 {
		return version, true, nil
	}

	var n int
	if err := db.QueryRowContext(ctx, `SELECT version FROM version LIMIT 1`).Scan(&n); err == nil {
		return n, true, nil
	}
	var text string
	if err := db.QueryRowContext(ctx, `SELECT schema FROM version LIMIT 1`).Scan(&text); err == nil {
		if n, convErr := strconv.Atoi(strings.TrimSpace(text)); convErr == nil {
			return n, true, nil
		}
	}
	return 0, false, nil
}

// isBusy reports a SQLITE_BUSY / locked database across driver versions.
func isBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToUpper(err.Error())
	return strings.Contains(msg, "SQLITE_BUSY") ||
		strings.Contains(msg, "SQLITE_LOCKED") ||
		strings.Contains(strings.ToLower(err.Error()), "database is locked")
}

// retryOnBusy runs fn once and, only when it fails with SQLITE_BUSY, waits
// 500ms and runs it exactly once more. There is no background or unlimited
// retry loop (SPEC-04 §5.2).
func retryOnBusy[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	out, err := fn()
	if err == nil || !isBusy(err) {
		return out, err
	}
	timer := time.NewTimer(sqliteRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		var zero T
		return zero, fmt.Errorf("%w: %v", literature.ErrZoteroBusy, ctx.Err())
	case <-timer.C:
	}
	out, err = fn()
	if err != nil && isBusy(err) {
		var zero T
		return zero, fmt.Errorf("%w: %v", literature.ErrZoteroBusy, err)
	}
	return out, err
}
