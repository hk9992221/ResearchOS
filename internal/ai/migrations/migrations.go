// Package migrations ships the SPEC-02 schema additions in the reserved
// 0100–0199 band (SPEC-01 §10.4: provider cache columns, provider_models,
// ai_outputs) plus the index storage tables whose shape SPEC-00 v1.1 adopted
// from SPEC-10 §4.2 (N1/N2/N3). Source registries, dirty queues and index
// job tables remain with SPEC-10.
package migrations

import (
	"embed"
	"fmt"

	"researchos/internal/foundation/storage"
)

//go:embed global project
var embedded embed.FS

// All returns the SPEC-02 migrations keyed by database kind, ready for
// workspace.Options.Migrations.
func All() (map[storage.Kind][]storage.Migration, error) {
	global, err := storage.LoadFS(embedded, "global")
	if err != nil {
		return nil, fmt.Errorf("ai/migrations: global: %w", err)
	}
	project, err := storage.LoadFS(embedded, "project")
	if err != nil {
		return nil, fmt.Errorf("ai/migrations: project: %w", err)
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
		panic("ai/migrations: " + err.Error())
	}
	return m
}
