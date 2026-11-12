package workspace_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"researchos/internal/foundation/events"
	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/storage"
	"researchos/internal/foundation/trash"
)

// TestAC15PerformanceBaselines records the §8 performance numbers with 10k
// notes and 50k activities. They are logged, not asserted (the spec calls them
// a baseline, not a hard gate).
func TestAC15PerformanceBaselines(t *testing.T) {
	ctx := context.Background()
	fx, err := foundationtest.NewWithProject(t.TempDir(), foundationtest.Options{})
	if err != nil {
		t.Fatalf("NewWithProject: %v", err)
	}
	defer fx.Close()
	handle := fx.Project
	const notes = 10_000
	const activities = 50_000
	// All notes are in the trash so the v_trash baseline has 10k rows.
	if err := fx.SeedNotes(ctx, handle, notes, notes); err != nil {
		t.Fatalf("SeedNotes: %v", err)
	}
	if err := fx.SeedActivities(ctx, handle, activities); err != nil {
		t.Fatalf("SeedActivities: %v", err)
	}

	reader := events.New(handle.DB(), events.Options{Clock: fx.Clock})
	start := time.Now()
	recent, err := reader.List(ctx, events.Query{Limit: 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	t.Logf("AC-15(a): recent-50 activity query over %d activities: %s (%d rows)",
		activities, time.Since(start), len(recent))

	trashService := trash.New(handle.ID, handle.DB(), fx.Workspace.Paths(), handle.Events(), fx.Confirm, fx.Clock)
	start = time.Now()
	page, err := trashService.List(ctx, trash.ListQuery{Limit: 100})
	if err != nil {
		t.Fatalf("trash List: %v", err)
	}
	t.Logf("AC-15(c): v_trash page over %d trash items: %s (%d rows)",
		notes, time.Since(start), len(page))

	dbPath := filepath.Join(fx.Root, "projects", handle.ID, "project.db")
	start = time.Now()
	db, err := storage.OpenDatabase(ctx, dbPath, storage.KindProject, storage.Options{
		Clock: fx.Clock, SkipBackup: true,
	})
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	elapsed := time.Since(start)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("AC-15(d): open project database (with migration check): %s", elapsed)
}
