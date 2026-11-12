package foundationtest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"researchos/internal/foundation/config"
	"researchos/internal/foundation/workspace"
)

func TestFixtureBuildsIsolatedWorkspace(t *testing.T) {
	dir := t.TempDir()
	fx, err := New(dir, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer fx.Close()

	if fx.Root != filepath.Join(dir, "workspace") {
		t.Fatalf("Root = %q", fx.Root)
	}
	if _, err := os.Stat(filepath.Join(fx.Root, workspace.MarkerName)); err != nil {
		t.Fatalf("marker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fx.Root, "global.db")); err != nil {
		t.Fatalf("global.db: %v", err)
	}
	if fx.Global() == nil {
		t.Fatal("Global() = nil")
	}
	if fx.Confirm == nil {
		t.Fatal("Confirm = nil")
	}
	if _, err := fx.Global().DB().ExecContext(context.Background(), `SELECT 1`); err != nil {
		t.Fatalf("query global: %v", err)
	}

	// The config and backup roots stay outside the workspace so that the
	// AC-01 layout assertion is not polluted.
	if err := fx.Config.Save(context.Background(), config.AppConfig{
		SchemaVersion: 1,
		WorkspaceRoot: fx.Root,
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config", config.ConfigFile)); err != nil {
		t.Fatalf("config file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fx.Root, "config")); !os.IsNotExist(err) {
		t.Fatalf("config leaked into the workspace: %v", err)
	}
	if fx.BackupRoot == "" || filepath.Dir(fx.BackupRoot) != filepath.Join(dir, "cache") {
		t.Fatalf("BackupRoot = %q", fx.BackupRoot)
	}
	if !fx.Clock.Now().Equal(DefaultTime) {
		t.Fatalf("clock = %v, want %v", fx.Clock.Now(), DefaultTime)
	}
}

func TestNewProjectAndSeedActivities(t *testing.T) {
	ctx := context.Background()
	fx, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer fx.Close()

	id, handle, err := fx.NewProject(ctx, "Alpha", "summary", "goals")
	if err != nil {
		t.Fatalf("NewProject: %v", err)
	}
	if handle.ID != id {
		t.Fatalf("handle id = %q, want %q", handle.ID, id)
	}
	if err := fx.SeedActivities(ctx, handle, 7); err != nil {
		t.Fatalf("SeedActivities: %v", err)
	}
	var count int
	if err := handle.DB().QueryRow(`SELECT count(*) FROM activities`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 7 {
		t.Fatalf("activities = %d, want 7", count)
	}
	if err := fx.SeedActivities(ctx, handle, 0); err != nil {
		t.Fatalf("SeedActivities(0): %v", err)
	}
	if err := fx.SeedActivities(ctx, handle, -1); err == nil {
		t.Fatal("SeedActivities(-1) succeeded")
	}
}
