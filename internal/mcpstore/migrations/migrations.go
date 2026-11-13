// Package migrations ships the SPEC-11 global runtime tables in the reserved
// 0350–0399 band (orchestrator ruling 1: SPEC-10 owns 0300–0349).
//
// Register the set through workspace.Options.Migrations:
//
//	opts.Migrations = append(opts.Migrations, migrations.MustAll()...)
package migrations

import (
	"embed"
	"fmt"

	"researchos/internal/foundation/storage"
)

//go:embed global
var embedded embed.FS

// All returns the SPEC-11 migrations keyed by database kind.
func All() (map[storage.Kind][]storage.Migration, error) {
	global, err := storage.LoadFS(embedded, "global")
	if err != nil {
		return nil, fmt.Errorf("mcpstore/migrations: global: %w", err)
	}
	return map[storage.Kind][]storage.Migration{
		storage.KindGlobal: global,
	}, nil
}

// MustAll is All for wiring that treats schema registration as fatal.
func MustAll() map[storage.Kind][]storage.Migration {
	m, err := All()
	if err != nil {
		panic("mcpstore/migrations: " + err.Error())
	}
	return m
}
