// Package migrations ships the SPEC-07 Experiments project schema additions.
//
// 0400_experiments_run_title sits above the 0200–0299 module band on purpose:
// the engine only applies versions above the database's highest applied
// version, and existing project databases are already at 0310, so a band
// version would never reach them (the same ruling notes' 0310 made).
//
// Register the set through workspace.Options.Migrations (or
// foundationtest.Options.Migrations in tests):
//
//	opts.Migrations = migrations.MustAll()
package migrations

import (
	"embed"
	"fmt"

	"researchos/internal/foundation/storage"
)

//go:embed project
var embedded embed.FS

// All returns the SPEC-07 migrations keyed by database kind, ready for
// workspace.Options.Migrations. SPEC-07 only touches project.db.
func All() (map[storage.Kind][]storage.Migration, error) {
	project, err := storage.LoadFS(embedded, "project")
	if err != nil {
		return nil, fmt.Errorf("experiments/migrations: project: %w", err)
	}
	return map[storage.Kind][]storage.Migration{
		storage.KindProject: project,
	}, nil
}

// MustAll is All for wiring that treats schema registration as fatal.
func MustAll() map[storage.Kind][]storage.Migration {
	m, err := All()
	if err != nil {
		panic("experiments/migrations: " + err.Error())
	}
	return m
}
