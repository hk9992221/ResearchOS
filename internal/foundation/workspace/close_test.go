package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"researchos/internal/foundation/ids"
)

// TestCloseProjectEvictsHandle verifies that CloseProject closes and evicts the
// cached handle, so a later Project call reopens a usable database.
func TestCloseProjectEvictsHandle(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	opts, _ := testOptions(t, base)
	ws, err := Init(ctx, filepath.Join(base, "ws"), opts)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { ws.Close() })

	id := ids.MustNew(ws.Clock())
	if err := ws.CreateProjectData(ctx, id); err != nil {
		t.Fatalf("CreateProjectData: %v", err)
	}
	first, err := ws.Project(ctx, id)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if err := ws.CloseProject(id); err != nil {
		t.Fatalf("CloseProject: %v", err)
	}
	if err := first.DB().PingContext(ctx); err == nil {
		t.Fatal("closed handle still answers pings")
	}
	second, err := ws.Project(ctx, id)
	if err != nil {
		t.Fatalf("Project after CloseProject: %v", err)
	}
	if second == first {
		t.Fatal("CloseProject did not evict the cached handle")
	}
	if err := second.DB().PingContext(ctx); err != nil {
		t.Fatalf("reopened handle: %v", err)
	}
	// Closing an unknown project is a no-op.
	if err := ws.CloseProject(ids.MustNew(ws.Clock())); err != nil {
		t.Fatalf("CloseProject(unknown): %v", err)
	}
}
