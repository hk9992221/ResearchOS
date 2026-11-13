// Package migrations ships the SPEC-06 project schema additions in the
// reserved 0200–0299 band (SPEC-01 §4.8.1): evidence.content_hash and the Idea
// / Evidence partial indexes. The ideas and evidence tables themselves are
// frozen in project schema v1.
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

// All returns the SPEC-06 migrations keyed by database kind.
func All() (map[storage.Kind][]storage.Migration, error) {
	project, err := storage.LoadFS(embedded, "project")
	if err != nil {
		return nil, fmt.Errorf("ideas/migrations: project: %w", err)
	}
	return map[storage.Kind][]storage.Migration{
		storage.KindProject: project,
	}, nil
}

// MustAll is All for wiring that treats schema registration as fatal.
func MustAll() map[storage.Kind][]storage.Migration {
	m, err := All()
	if err != nil {
		panic("ideas/migrations: " + err.Error())
	}
	return m
}
