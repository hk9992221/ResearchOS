// Package migrations ships the SPEC-04 project schema additions in the
// reserved 0200–0299 band: notes.paper_id / meta_json and the Paper Note and
// Literature Overview partial indexes (§4.4, §4.8).
//
// Register the set through workspace.Options.Migrations:
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

// All returns the SPEC-04 migrations keyed by database kind.
func All() (map[storage.Kind][]storage.Migration, error) {
	project, err := storage.LoadFS(embedded, "project")
	if err != nil {
		return nil, fmt.Errorf("literature/migrations: project: %w", err)
	}
	return map[storage.Kind][]storage.Migration{
		storage.KindProject: project,
	}, nil
}

// MustAll is All for wiring that treats schema registration as fatal.
func MustAll() map[storage.Kind][]storage.Migration {
	m, err := All()
	if err != nil {
		panic("literature/migrations: " + err.Error())
	}
	return m
}
