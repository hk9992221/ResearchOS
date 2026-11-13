// Package migrations ships the SPEC-08 Kanban project schema additions in the
// reserved 0200–0299 band (SPEC-01 §4.8.1, §10.4 S-03): task_columns, the
// tasks extensions parent_task_id / description_md / completed_at /
// archived_at, and task_checklist_items.
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

// All returns the SPEC-08 migrations keyed by database kind, ready for
// workspace.Options.Migrations. SPEC-08 only touches project.db.
func All() (map[storage.Kind][]storage.Migration, error) {
	project, err := storage.LoadFS(embedded, "project")
	if err != nil {
		return nil, fmt.Errorf("kanban/migrations: project: %w", err)
	}
	return map[storage.Kind][]storage.Migration{
		storage.KindProject: project,
	}, nil
}

// MustAll is All for wiring that treats schema registration as fatal.
func MustAll() map[storage.Kind][]storage.Migration {
	m, err := All()
	if err != nil {
		panic("kanban/migrations: " + err.Error())
	}
	return m
}
