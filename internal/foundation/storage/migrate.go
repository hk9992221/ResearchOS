package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
)

// Result describes one Migrate call.
type Result struct {
	// From is the highest applied version before the call.
	From int
	// To is the highest applied version after the call.
	To int
	// Applied lists the versions applied by this call, in order.
	Applied []int
	// BackupPath is the pre-migration snapshot, empty when no backup was taken.
	BackupPath string
}

type appliedMigration struct {
	Version  int
	Name     string
	Checksum string
}

// Migrate brings db to the target version of kind. It applies only versions
// above the current maximum, one BEGIN IMMEDIATE transaction per migration,
// verifies the checksum of every already-applied migration, takes a
// pre-migration backup (unless disabled) and refuses databases that are newer
// than this build. It is idempotent: a second call is a no-op.
func Migrate(ctx context.Context, db *sql.DB, kind Kind, dbPath string, opt Options) (Result, error) {
	opt = opt.withDefaults()
	ms, err := migrationsFor(kind, opt.Extra[kind])
	if err != nil {
		return Result{}, err
	}
	applied, _, err := readApplied(ctx, db)
	if err != nil {
		return Result{}, err
	}
	res := Result{}
	for _, a := range applied {
		if a.Version > res.From {
			res.From = a.Version
		}
	}
	res.To = res.From

	target := 0
	if len(ms) > 0 {
		target = ms[len(ms)-1].Version
	}
	if res.From > target {
		return res, fmt.Errorf("%w: database %s is at version %d, this build targets %d",
			errs.ErrSchemaTooNew, dbPath, res.From, target)
	}

	byVersion := make(map[int]Migration, len(ms))
	for _, m := range ms {
		byVersion[m.Version] = m
	}
	for _, a := range applied {
		m, ok := byVersion[a.Version]
		if !ok {
			return res, fmt.Errorf("%w: version %04d (%s) is applied but the migration is gone",
				errs.ErrMigrationTampered, a.Version, a.Name)
		}
		if got := m.Checksum(); got != a.Checksum {
			return res, fmt.Errorf("%w: version %04d checksum %s does not match the recorded %s",
				errs.ErrMigrationTampered, a.Version, got, a.Checksum)
		}
	}

	var pending []Migration
	for _, m := range ms {
		if m.Version > res.From {
			pending = append(pending, m)
		}
	}
	if len(pending) == 0 {
		return res, nil
	}

	// Nothing of value exists in a brand new database, so only back up when
	// there is an applied schema to lose.
	if res.From > 0 && !opt.SkipBackup {
		backupPath, err := backup(ctx, db, dbPath, res.From, opt)
		if err != nil {
			return res, err
		}
		res.BackupPath = backupPath
	}

	for _, m := range pending {
		if err := applyOne(ctx, db, m, opt.Clock.Now()); err != nil {
			return res, fmt.Errorf("storage: migration %04d_%s failed: %w", m.Version, m.Name, err)
		}
		res.Applied = append(res.Applied, m.Version)
		res.To = m.Version
	}
	return res, nil
}

// readApplied returns the rows of schema_migrations and whether the table
// exists at all.
func readApplied(ctx context.Context, db *sql.DB) ([]appliedMigration, bool, error) {
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`,
	).Scan(&count); err != nil {
		return nil, false, fmt.Errorf("storage: inspect schema_migrations: %w", err)
	}
	if count == 0 {
		return nil, false, nil
	}
	rows, err := db.QueryContext(ctx,
		`SELECT version, name, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, true, fmt.Errorf("storage: read schema_migrations: %w", err)
	}
	defer rows.Close()
	var out []appliedMigration
	for rows.Next() {
		var a appliedMigration
		if err := rows.Scan(&a.Version, &a.Name, &a.Checksum); err != nil {
			return nil, true, fmt.Errorf("storage: scan schema_migrations: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, true, fmt.Errorf("storage: read schema_migrations: %w", err)
	}
	return out, true, nil
}

// applyOne executes a single migration and records it in the same transaction.
// The driver opens every transaction with BEGIN IMMEDIATE (_txlock), and SQLite
// DDL is transactional, so a failure leaves the database exactly as it was.
func applyOne(ctx context.Context, db *sql.DB, m Migration, now time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		m.Version, m.Name, m.Checksum(), clock.Format(now),
	); err != nil {
		return err
	}
	return tx.Commit()
}
