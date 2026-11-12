package foundationtest

import (
	"context"
	"database/sql"
	"fmt"
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

func TestZeroConfigFixture(t *testing.T) {
	ctx := context.Background()
	fx, err := NewWithProject(t.TempDir(), Options{})
	if err != nil {
		t.Fatalf("NewWithProject: %v", err)
	}
	defer fx.Close()
	if fx.Project == nil || fx.ProjectID == "" {
		t.Fatal("fixture has no default project")
	}
	if err := fx.SeedNotes(ctx, fx.Project, 10, 3); err != nil {
		t.Fatalf("SeedNotes: %v", err)
	}
	var notes, trash int
	if err := fx.Project.DB().QueryRow(`SELECT count(*) FROM notes`).Scan(&notes); err != nil {
		t.Fatal(err)
	}
	if err := fx.Project.DB().QueryRow(`SELECT count(*) FROM v_trash`).Scan(&trash); err != nil {
		t.Fatal(err)
	}
	if notes != 10 || trash != 3 {
		t.Fatalf("notes=%d trash=%d, want 10/3", notes, trash)
	}
	paperID, err := fx.SeedPaper(ctx, "K1")
	if err != nil {
		t.Fatalf("SeedPaper: %v", err)
	}
	if err := fx.LinkProjectPaper(ctx, paperID); err != nil {
		t.Fatalf("LinkProjectPaper: %v", err)
	}
	var links int
	if err := fx.Project.DB().QueryRow(`SELECT count(*) FROM project_papers`).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 1 {
		t.Fatalf("project_papers = %d", links)
	}

	// Golden schema comparison utility.
	golden := filepath.Join("..", "storage", "testdata", "schema", "project_v1.golden.sql")
	if err := fx.CompareSchema(ctx, fx.Project.DB(), golden); err != nil {
		t.Fatalf("CompareSchema: %v", err)
	}
	if err := fx.CompareSchema(ctx, fx.Global().DB(), filepath.Join("..", "storage", "testdata", "schema", "global_v1.golden.sql")); err != nil {
		t.Fatalf("CompareSchema(global): %v", err)
	}

	// Concurrency helper.
	if err := RunConcurrentInTx(ctx, fx.Global(), 2, 5, func(gctx context.Context, tx *sql.Tx, g, r int) error {
		_, err := tx.ExecContext(gctx,
			`INSERT INTO settings(key, value, updated_at) VALUES (?, 'v', ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			fmt.Sprintf("test.g%d.r%d", g, r), "2026-01-02T03:04:05.000Z")
		return err
	}); err != nil {
		t.Fatalf("RunConcurrentInTx: %v", err)
	}
	var settingsCount int
	if err := fx.Global().DB().QueryRow(`SELECT count(*) FROM settings WHERE key LIKE 'test.%'`).Scan(&settingsCount); err != nil {
		t.Fatal(err)
	}
	if settingsCount != 10 {
		t.Fatalf("settings = %d, want 10", settingsCount)
	}
}
