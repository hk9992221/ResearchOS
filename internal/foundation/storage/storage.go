// Package storage owns database opening, per-connection PRAGMAs, the migration
// engine and pre-migration backups (SPEC-01 §4.1.8, §4.8).
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	"researchos/internal/foundation/clock"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"
)

// Kind selects which migration set applies to a database file.
type Kind string

const (
	// KindGlobal is the workspace-level global.db.
	KindGlobal Kind = "global"
	// KindProject is a per-project project.db.
	KindProject Kind = "project"
)

// Logger is the minimal logging surface needed by the migration engine. It is
// a subset of workspace.Logger, so callers pass the same value.
type Logger interface {
	Warn(msg string, kv ...any)
}

// Options configures opening and migrating one database.
type Options struct {
	// Clock supplies migration timestamps. Nil means clock.System.
	Clock clock.Clock
	// SkipBackup disables the pre-migration snapshot. Tests only.
	SkipBackup bool
	// BackupRoot overrides the backup root directory. Empty means
	// <os.UserCacheDir()>/ResearchOS/backups. Tests inject a temp directory.
	BackupRoot string
	// Logger receives non-fatal warnings (backup directory unavailable, ...).
	Logger Logger
	// MaxOpenConns limits the connection pool. Zero means 1.
	MaxOpenConns int
	// MaxIdleConns caps idle connections. Zero means 4.
	MaxIdleConns int
	// Extra supplies additional migrations per kind, for module packages and
	// tests. Versions must be unique across the embedded and extra sets.
	Extra map[Kind][]Migration
}

func (o Options) withDefaults() Options {
	if o.Clock == nil {
		o.Clock = clock.System{}
	}
	if o.MaxOpenConns <= 0 {
		o.MaxOpenConns = 1
	}
	if o.MaxIdleConns <= 0 {
		o.MaxIdleConns = 4
	}
	return o
}

func (o Options) warn(msg string, kv ...any) {
	if o.Logger != nil {
		o.Logger.Warn(msg, kv...)
	}
}

// Open opens the SQLite database at path, applies the per-connection PRAGMAs
// and verifies that a connection can be established. It does not migrate.
func Open(ctx context.Context, path string, opt Options) (*sql.DB, error) {
	opt = opt.withDefaults()
	db, err := sql.Open("sqlite", sqliteDSN(path, false))
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(opt.MaxOpenConns)
	db.SetMaxIdleConns(opt.MaxIdleConns)
	db.SetConnMaxLifetime(0)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("storage: open %s: %w", path, err)
	}
	return db, nil
}

// OpenDatabase opens and migrates the database at path.
func OpenDatabase(ctx context.Context, path string, kind Kind, opt Options) (*sql.DB, error) {
	db, err := Open(ctx, path, opt)
	if err != nil {
		return nil, err
	}
	if _, err := Migrate(ctx, db, kind, path, opt); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// sqliteDSN builds the driver DSN. PRAGMAs are attached per connection through
// the driver's _pragma mechanism, which satisfies §4.1.8 for every connection
// the pool creates. _txlock=immediate makes every BeginTx a BEGIN IMMEDIATE
// transaction, as required for migrations (§4.8.2).
func sqliteDSN(path string, readOnly bool) string {
	var b strings.Builder
	b.WriteString("file:")
	b.WriteString(escapeDSNPath(filepath.ToSlash(path)))
	b.WriteString("?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if readOnly {
		b.WriteString("&mode=ro")
	} else {
		b.WriteString("&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate")
	}
	return b.String()
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
