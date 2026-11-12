package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"researchos/internal/foundation/errs"
)

func newStore(t *testing.T) (Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ResearchOS", ConfigFile)
	store, err := NewStoreAt(path)
	if err != nil {
		t.Fatalf("NewStoreAt: %v", err)
	}
	return store, path
}

func TestAC17SaveLoadRoundTrip(t *testing.T) {
	store, path := newStore(t)
	ctx := context.Background()

	cfg, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load(missing) = %v", err)
	}
	if cfg != (AppConfig{}) {
		t.Fatalf("Load(missing) = %+v, want zero value", cfg)
	}

	want := AppConfig{SchemaVersion: 1, WorkspaceRoot: `F:\Data\ResearchOS`, ZoteroDataDir: `F:\Zotero`}
	if err := store.Save(ctx, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
		t.Fatalf("Load = %+v, want %+v", got, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if runtime.GOOS != "windows" {
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("config mode = %o, want 600", mode)
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "api_key") {
		t.Fatalf("config file mentions api_key: %s", body)
	}
	if store.Path() != path {
		t.Fatalf("Path = %q, want %q", store.Path(), path)
	}
}

func TestAC17MissingFileIsZero(t *testing.T) {
	store, path := newStore(t)
	if err := store.Save(context.Background(), AppConfig{SchemaVersion: 1, WorkspaceRoot: "F:/ws"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load after delete: %v", err)
	}
	if cfg != (AppConfig{}) {
		t.Fatalf("Load after delete = %+v, want zero", cfg)
	}
}

func TestAC17CorruptJSON(t *testing.T) {
	store, path := newStore(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := store.Load(ctx)
	if !errors.Is(err, errs.ErrConfigCorrupt) {
		t.Fatalf("Load(corrupt) = %v, want ErrConfigCorrupt", err)
	}
	if cfg != (AppConfig{}) {
		t.Fatalf("Load(corrupt) = %+v, want zero", cfg)
	}
	matches, globErr := filepath.Glob(path + ".corrupt-*")
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 1 {
		t.Fatalf("corrupt backups = %v, want exactly one", matches)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("original config still exists after quarantine")
	}
	// A second load is clean because the corrupt file is gone.
	if cfg, err := store.Load(ctx); err != nil || cfg != (AppConfig{}) {
		t.Fatalf("Load after quarantine = %+v, %v", cfg, err)
	}
}

func TestSaveCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "config")
	store, err := NewStoreAt(filepath.Join(dir, ConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), AppConfig{WorkspaceRoot: "F:/ws"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ConfigFile)); err != nil {
		t.Fatalf("config file missing: %v", err)
	}
}

func TestSaveDefaultsSchemaVersion(t *testing.T) {
	store, _ := newStore(t)
	if err := store.Save(context.Background(), AppConfig{WorkspaceRoot: "F:/ws"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SchemaVersion != 1 {
		t.Fatalf("SchemaVersion = %d, want 1", cfg.SchemaVersion)
	}
}

func TestNewStoreAtRejectsEmpty(t *testing.T) {
	if _, err := NewStoreAt(""); err == nil {
		t.Fatal("NewStoreAt(\"\") succeeded")
	}
}
