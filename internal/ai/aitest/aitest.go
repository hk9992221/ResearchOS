// Package aitest provides SPEC-02 fixtures: a foundation workspace with the
// AI migrations applied. It is test-support only; runtime code must not
// import it.
package aitest

import (
	"testing"

	"researchos/internal/ai/migrations"
	"researchos/internal/foundation/foundationtest"
)

// Options mirrors the foundation fixture options for SPEC-02 tests.
type Options = foundationtest.Options

// Fixture opens a zero-config workspace with the SPEC-02 schema band.
func Fixture(t *testing.T) *foundationtest.Fixture {
	t.Helper()
	return New(t, foundationtest.Options{})
}

// FixtureWithProject additionally creates the default project (project.db
// carries ai_outputs and the project index tables).
func FixtureWithProject(t *testing.T) *foundationtest.Fixture {
	t.Helper()
	return NewWithProject(t, foundationtest.Options{})
}

// New opens a fixture with explicit options, always merging the SPEC-02
// migrations.
func New(t *testing.T, opts foundationtest.Options) *foundationtest.Fixture {
	t.Helper()
	migs, err := migrations.All()
	if err != nil {
		t.Fatalf("ai migrations: %v", err)
	}
	opts.Migrations = migs
	f, err := foundationtest.New(t.TempDir(), opts)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// NewWithProject builds the project-primed fixture.
func NewWithProject(t *testing.T, opts foundationtest.Options) *foundationtest.Fixture {
	t.Helper()
	migs, err := migrations.All()
	if err != nil {
		t.Fatalf("ai migrations: %v", err)
	}
	opts.Migrations = migs
	f, err := foundationtest.NewWithProject(t.TempDir(), opts)
	if err != nil {
		t.Fatalf("fixture with project: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}
