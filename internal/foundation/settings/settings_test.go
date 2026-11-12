package settings

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/storage"
)

var fixed = &clock.Fixed{T: time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)}

func openProjectDB(t *testing.T, kind storage.Kind) *sql.DB {
	t.Helper()
	path := t.TempDir() + "/db.db"
	db, err := storage.OpenDatabase(context.Background(), path, kind, storage.Options{Clock: fixed, SkipBackup: true})
	if err != nil {
		t.Fatalf("OpenDatabase(%s): %v", kind, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestExportDirRoundTrip(t *testing.T) {
	db := openProjectDB(t, storage.KindGlobal)
	store := NewWorkspace(db).WithClock(fixed)
	ctx := context.Background()

	if _, ok, err := store.Get(ctx, "export.dir"); err != nil || ok {
		t.Fatalf("Get(missing) = %v, %v", ok, err)
	}
	if err := store.Set(ctx, nil, "export.dir", "F:/exports"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	value, ok, err := store.Get(ctx, "export.dir")
	if err != nil || !ok || value != "F:/exports" {
		t.Fatalf("Get = %q, %v, %v", value, ok, err)
	}

	// UPSERT keeps one row and refreshes updated_at from the injected clock.
	fixed.Advance(time.Minute)
	if err := store.Set(ctx, nil, "export.dir", "F:/exports2"); err != nil {
		t.Fatalf("Set again: %v", err)
	}
	var count int
	var updatedAt string
	if err := db.QueryRowContext(ctx, `SELECT count(*), max(updated_at) FROM settings WHERE key = 'export.dir'`).Scan(&count, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("settings rows = %d, want 1", count)
	}
	if updatedAt != clock.Format(fixed.Now()) {
		t.Fatalf("updated_at = %q, want %q", updatedAt, clock.Format(fixed.Now()))
	}
	if value, _, _ := store.Get(ctx, "export.dir"); value != "F:/exports2" {
		t.Fatalf("Get after upsert = %q", value)
	}
}

func TestKeyValidation(t *testing.T) {
	db := openProjectDB(t, storage.KindGlobal)
	store := NewWorkspace(db).WithClock(fixed)
	ctx := context.Background()

	valid := []string{
		"export.dir",
		"app.last_project_id",
		"search.index_mode",
		"ui.dark-mode",
		"module.sub.key-1",
		"export.d_ir",
	}
	for _, key := range valid {
		if err := store.Set(ctx, nil, key, "v"); err != nil {
			t.Errorf("Set(%q) = %v, want nil", key, err)
		}
	}

	invalid := []string{
		"",
		"export",
		"Export.dir",
		"1export.dir",
		"export..dir",
		"export.",
		".export",
		"export.Dir",
		"export dir.key",
		"export.dir!",
		"-export.dir",
	}
	for _, key := range invalid {
		if _, _, err := store.Get(ctx, key); !errors.Is(err, errs.ErrBadSettingKey) {
			t.Errorf("Get(%q) = %v, want ErrBadSettingKey", key, err)
		}
		if err := store.Set(ctx, nil, key, "v"); !errors.Is(err, errs.ErrBadSettingKey) {
			t.Errorf("Set(%q) = %v, want ErrBadSettingKey", key, err)
		}
		if err := store.Delete(ctx, nil, key); !errors.Is(err, errs.ErrBadSettingKey) {
			t.Errorf("Delete(%q) = %v, want ErrBadSettingKey", key, err)
		}
	}
}

func TestProjectIsolation(t *testing.T) {
	ctx := context.Background()
	dbA := openProjectDB(t, storage.KindProject)
	dbB := openProjectDB(t, storage.KindProject)
	storeA := NewProject(dbA).WithClock(fixed)
	storeB := NewProject(dbB).WithClock(fixed)

	if err := storeA.Set(ctx, nil, "export.format", "pdf"); err != nil {
		t.Fatal(err)
	}
	if err := storeB.Set(ctx, nil, "export.format", "docx"); err != nil {
		t.Fatal(err)
	}
	if value, _, _ := storeA.Get(ctx, "export.format"); value != "pdf" {
		t.Fatalf("project A = %q, want pdf", value)
	}
	if value, _, _ := storeB.Get(ctx, "export.format"); value != "docx" {
		t.Fatalf("project B = %q, want docx", value)
	}

	// The same key in the workspace store is a different namespace.
	globalDB := openProjectDB(t, storage.KindGlobal)
	globalStore := NewWorkspace(globalDB).WithClock(fixed)
	if _, ok, err := globalStore.Get(ctx, "export.format"); err != nil || ok {
		t.Fatalf("global store leaked a project key: %v, %v", ok, err)
	}
}

func TestListPrefix(t *testing.T) {
	db := openProjectDB(t, storage.KindGlobal)
	store := NewWorkspace(db).WithClock(fixed)
	ctx := context.Background()
	for _, key := range []string{"export.dir", "export.format", "app.last_project_id", "search.mode"} {
		if err := store.Set(ctx, nil, key, key+"-v"); err != nil {
			t.Fatal(err)
		}
	}
	all, err := store.List(ctx, "")
	if err != nil {
		t.Fatalf("List(all): %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("List(all) = %v", all)
	}
	exports, err := store.List(ctx, "export")
	if err != nil {
		t.Fatalf("List(export): %v", err)
	}
	if len(exports) != 2 || exports["export.dir"] != "export.dir-v" {
		t.Fatalf("List(export) = %v", exports)
	}
	search, err := store.List(ctx, "search.")
	if err != nil {
		t.Fatalf("List(search.): %v", err)
	}
	if len(search) != 1 {
		t.Fatalf("List(search.) = %v", search)
	}
	// Prefix matching is literal: '_' must not act as a single-character wildcard.
	if err := store.Set(ctx, nil, "a.b_c", "1"); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(ctx, nil, "a.bxc", "2"); err != nil {
		t.Fatal(err)
	}
	literal, err := store.List(ctx, "a.b_")
	if err != nil {
		t.Fatal(err)
	}
	if len(literal) != 1 || literal["a.b_c"] != "1" {
		t.Fatalf("List(a.b_) = %v, want only a.b_c", literal)
	}
}

func TestJSONAndDelete(t *testing.T) {
	db := openProjectDB(t, storage.KindProject)
	store := NewProject(db).WithClock(fixed)
	ctx := context.Background()

	type payload struct {
		Mode  string `json:"mode"`
		Depth int    `json:"depth"`
	}
	if ok, err := store.GetJSON(ctx, "search.config", &payload{}); err != nil || ok {
		t.Fatalf("GetJSON(missing) = %v, %v", ok, err)
	}
	want := payload{Mode: "fast", Depth: 3}
	if err := store.SetJSON(ctx, nil, "search.config", want); err != nil {
		t.Fatalf("SetJSON: %v", err)
	}
	raw, _, err := store.Get(ctx, "search.config")
	if err != nil {
		t.Fatal(err)
	}
	if raw != `{"mode":"fast","depth":3}` {
		t.Fatalf("stored JSON = %q, want compact", raw)
	}
	var got payload
	ok, err := store.GetJSON(ctx, "search.config", &got)
	if err != nil || !ok || got != want {
		t.Fatalf("GetJSON = %+v, %v, %v", got, ok, err)
	}
	if err := store.Set(ctx, nil, "search.broken", "not json"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetJSON(ctx, "search.broken", &got); err == nil {
		t.Fatal("GetJSON accepted malformed JSON")
	}

	if err := store.Delete(ctx, nil, "search.config"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, err := store.Get(ctx, "search.config"); err != nil || ok {
		t.Fatalf("Get after delete = %v, %v", ok, err)
	}
	if err := store.Delete(ctx, nil, "search.config"); err != nil {
		t.Fatalf("Delete(missing) = %v, want nil", err)
	}
}

func TestAC23ReadPriority(t *testing.T) {
	ctx := context.Background()
	projectDB := openProjectDB(t, storage.KindProject)
	globalDB := openProjectDB(t, storage.KindGlobal)
	projectStore := NewProject(projectDB).WithClock(fixed)
	workspaceStore := NewWorkspace(globalDB).WithClock(fixed)
	const projectID = "0192f0c8-1234-7000-8000-000000000001"

	if err := workspaceStore.Set(ctx, nil, "export.dir", "/workspace"); err != nil {
		t.Fatal(err)
	}
	if value, ok, err := ResolvePriority(ctx, projectStore, workspaceStore, projectID, "export.dir"); err != nil || ok || value != "" {
		t.Fatalf("ResolvePriority(neither) = %q, %v, %v", value, ok, err)
	}
	compatKey := "project." + projectID + ".export.dir"
	if err := workspaceStore.Set(ctx, nil, compatKey, "/compat"); err != nil {
		t.Fatal(err)
	}
	if err := projectStore.Set(ctx, nil, "export.dir", "/project"); err != nil {
		t.Fatal(err)
	}
	if value, ok, err := ResolvePriority(ctx, projectStore, workspaceStore, projectID, "export.dir"); err != nil || !ok || value != "/project" {
		t.Fatalf("ResolvePriority(project first) = %q, %v, %v", value, ok, err)
	}
	if err := projectStore.Delete(ctx, nil, "export.dir"); err != nil {
		t.Fatal(err)
	}
	if value, ok, err := ResolvePriority(ctx, projectStore, workspaceStore, projectID, "export.dir"); err != nil || !ok || value != "/compat" {
		t.Fatalf("ResolvePriority(compat) = %q, %v, %v", value, ok, err)
	}
	if err := workspaceStore.Delete(ctx, nil, compatKey); err != nil {
		t.Fatal(err)
	}
	// The workspace-level key without the project prefix is not part of the
	// project read path.
	if value, ok, err := ResolvePriority(ctx, projectStore, workspaceStore, projectID, "export.dir"); err != nil || ok || value != "" {
		t.Fatalf("ResolvePriority(workspace only) = %q, %v, %v", value, ok, err)
	}
}
