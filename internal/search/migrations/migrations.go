// Package migrations ships the SPEC-10 schema additions in the reserved
// 0300–0349 band (see the SQL headers for the split ruling): the per-source
// version registry, the persisted dirty queue and the index job tables on both
// databases. The doc/chunk/FTS/vector table groups belong to SPEC-02's 0110
// migration and are never recreated here.
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

//go:embed global project
var embedded embed.FS

// All returns the SPEC-10 migrations keyed by database kind.
func All() (map[storage.Kind][]storage.Migration, error) {
	global, err := storage.LoadFS(embedded, "global")
	if err != nil {
		return nil, fmt.Errorf("search/migrations: global: %w", err)
	}
	project, err := storage.LoadFS(embedded, "project")
	if err != nil {
		return nil, fmt.Errorf("search/migrations: project: %w", err)
	}
	return map[storage.Kind][]storage.Migration{
		storage.KindGlobal:  global,
		storage.KindProject: project,
	}, nil
}

// MustAll is All for wiring that treats schema registration as fatal.
func MustAll() map[storage.Kind][]storage.Migration {
	m, err := All()
	if err != nil {
		panic("search/migrations: " + err.Error())
	}
	return m
}
