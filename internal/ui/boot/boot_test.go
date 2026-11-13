package boot_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"researchos/internal/foundation/config"
	"researchos/internal/ui/boot"
)

func newStore(t *testing.T) config.Store {
	t.Helper()
	store, err := config.NewStoreAt(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestNoWorkspaceShowsTheSetupScreen(t *testing.T) {
	rt, err := boot.NewWithConfig(context.Background(), newStore(t))
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	tt := ui.NewTester(rt.View, 1024, 700)
	if !tt.HasText("Set up ResearchOS") {
		t.Fatalf("wizard welcome missing; texts %q", tt.Texts())
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "Get started"); !ok {
		t.Fatalf("wizard has no start action; texts %q", tt.Texts())
	}
	tt.Click("Get started")
	tt.Frame()
	for _, want := range []string{"Folder path", "Validate folder"} {
		if !tt.HasText(want) {
			t.Errorf("wizard directory step lacks %q; texts %q", want, tt.Texts())
		}
	}
}

func TestUseWorkspaceInitializesAndShowsTheShell(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	rt, err := boot.NewWithConfig(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	dir := t.TempDir()
	tt := ui.NewTester(rt.View, 1440, 900)
	if err := rt.UseWorkspace(ctx, dir); err != nil {
		t.Fatalf("use workspace: %v", err)
	}
	if err := rt.App().RefreshProjects(ctx); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	for _, want := range []string{"Home", "Your research workspace at a glance."} {
		if !tt.HasText(want) {
			t.Errorf("shell lacks %q; texts %q", want, tt.Texts())
		}
	}
	if rt.WorkspaceRoot() == "" {
		t.Error("no workspace root after opening")
	}

	cfg, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkspaceRoot != dir {
		t.Errorf("saved root = %q, want %q", cfg.WorkspaceRoot, dir)
	}
}

func TestConfiguredWorkspaceOpensOnStart(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	dir := t.TempDir()

	first, err := boot.NewWithConfig(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.UseWorkspace(ctx, dir); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()

	second, err := boot.NewWithConfig(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.App() == nil {
		t.Fatal("configured workspace did not open a shell")
	}
	tt := ui.NewTester(second.View, 1440, 900)
	if !tt.HasText("Home") {
		t.Errorf("shell did not render Home; texts %q", tt.Texts())
	}
}

func TestNonEmptyFolderReportsAReadableError(t *testing.T) {
	ctx := context.Background()
	rt, err := boot.NewWithConfig(ctx, newStore(t))
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = rt.UseWorkspace(ctx, dir)
	if err == nil {
		t.Fatal("a non-empty non-workspace folder was accepted")
	}
	if !strings.Contains(err.Error(), "not empty") {
		t.Errorf("error = %v, want ErrDirNotEmpty", err)
	}
	if string(rt.WorkspaceRoot()) != "" {
		t.Error("a failed open left a workspace behind")
	}
}
