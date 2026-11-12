package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"

	"researchos/internal/foundation/errs"
)

// Inspection is the read-only schema health report of one database file, used
// by workspace.Scan and the diagnostics page. It never migrates or repairs.
type Inspection struct {
	Path               string
	Version            int
	Target             int
	HasMigrationsTable bool
	Issues             []string
}

// Inspect opens path read-only and compares its schema state with the target
// version of the given kind.
func Inspect(ctx context.Context, path string, kind Kind, opt Options) (Inspection, error) {
	ins := Inspection{Path: path}
	ms, err := migrationsFor(kind, opt.Extra[kind])
	if err != nil {
		return ins, err
	}
	if len(ms) > 0 {
		ins.Target = ms[len(ms)-1].Version
	}
	if _, err := os.Stat(path); err != nil {
		return ins, err
	}
	db, err := openReadOnly(ctx, path)
	if err != nil {
		return ins, err
	}
	defer db.Close()

	applied, hasTable, err := readApplied(ctx, db)
	if err != nil {
		return ins, err
	}
	ins.HasMigrationsTable = hasTable
	for _, a := range applied {
		if a.Version > ins.Version {
			ins.Version = a.Version
		}
	}
	if !hasTable {
		ins.Issues = append(ins.Issues, "missing schema_migrations table")
	} else if ins.Version > ins.Target {
		ins.Issues = append(ins.Issues, fmt.Sprintf("schema version %d is newer than target %d", ins.Version, ins.Target))
	}
	byVersion := make(map[int]Migration, len(ms))
	for _, m := range ms {
		byVersion[m.Version] = m
	}
	for _, a := range applied {
		m, ok := byVersion[a.Version]
		if !ok {
			ins.Issues = append(ins.Issues, fmt.Sprintf("migration %04d (%s) is recorded but missing from the build: %v", a.Version, a.Name, errs.ErrMigrationTampered))
			continue
		}
		if got := m.Checksum(); got != a.Checksum {
			ins.Issues = append(ins.Issues, fmt.Sprintf("migration %04d checksum mismatch: %v", a.Version, errs.ErrMigrationTampered))
		}
	}
	sort.Strings(ins.Issues)
	return ins, nil
}

// openReadOnly opens a database with mode=ro and without write pragmas.
func openReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", sqliteDSN(path, true))
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("storage: open %s: %w", path, err)
	}
	return db, nil
}
