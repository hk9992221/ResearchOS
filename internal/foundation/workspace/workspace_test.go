package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/config"
	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/papers"
	"researchos/internal/foundation/projects"
	"researchos/internal/foundation/schema"
)

func fixedClock() *clock.Fixed {
	return &clock.Fixed{T: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)}
}

func testOptions(t *testing.T, base string) (Options, config.Store) {
	t.Helper()
	cfg, err := config.NewStoreAt(filepath.Join(base, "config", "config.json"))
	if err != nil {
		t.Fatalf("config.NewStoreAt: %v", err)
	}
	clock := fixedClock()
	return Options{
		Clock:      clock,
		SkipBackup: true,
		Config:     cfg,
		Confirm:    confirm.New(confirm.Options{Clock: clock}),
	}, cfg
}

func TestAC01InitLayoutAndIdempotency(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	opts, _ := testOptions(t, base)

	ws, err := Init(ctx, root, opts)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(root, MarkerName))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	var marker Marker
	if err := json.Unmarshal(body, &marker); err != nil {
		t.Fatalf("marker is not valid JSON: %v", err)
	}
	if marker.App != MarkerApp || marker.SchemaVersion != MarkerSchemaVersion {
		t.Fatalf("marker = %+v", marker)
	}
	if _, err := clock.Parse(marker.CreatedAt); err != nil {
		t.Fatalf("marker created_at %q: %v", marker.CreatedAt, err)
	}

	for _, rel := range []string{"global.db", "library/papers", "projects", "exports"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("layout entry %s: %v", rel, err)
		}
	}

	// Seed a project so idempotency is observable.
	store := projects.New(ws.Global().DB(), opts.Clock)
	var projectID string
	if err := ws.Global().InTx(ctx, func(tx *sql.Tx) error {
		var err error
		projectID, err = store.Create(ctx, tx, "Seeded", "", "")
		return err
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	markerBefore := append([]byte(nil), body...)
	if err := ws.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got := treePaths(t, root)
	want := []string{".researchos", "exports", "global.db", "library", "library/papers", "projects"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("layout = %v, want %v", got, want)
	}

	// Re-init with an advanced clock: data survives and the marker is untouched.
	opts.Clock.(*clock.Fixed).Advance(time.Hour)
	ws2, err := Init(ctx, root, opts)
	if err != nil {
		t.Fatalf("re-Init: %v", err)
	}
	defer ws2.Close()
	after, err := os.ReadFile(filepath.Join(root, MarkerName))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(markerBefore) {
		t.Fatalf("re-Init rewrote the marker:\nbefore %s\nafter  %s", markerBefore, after)
	}
	gotProject, err := projects.New(ws2.Global().DB(), opts.Clock).Get(ctx, projectID)
	if err != nil {
		t.Fatalf("project lost on re-Init: %v", err)
	}
	if gotProject.Name != "Seeded" {
		t.Fatalf("project name = %q", gotProject.Name)
	}
}

func TestAC25MarkerAndCorruption(t *testing.T) {
	ctx := context.Background()

	t.Run("marker without db", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "ws")
		opts, _ := testOptions(t, base)
		ws, err := Init(ctx, root, opts)
		if err != nil {
			t.Fatal(err)
		}
		ws.Close()
		if err := os.Remove(filepath.Join(root, "global.db")); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(ctx, root, opts); !errors.Is(err, errs.ErrWorkspaceCorrupt) {
			t.Fatalf("Open = %v, want ErrWorkspaceCorrupt", err)
		}
	})

	t.Run("db without marker", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "ws")
		opts, _ := testOptions(t, base)
		ws, err := Init(ctx, root, opts)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(root, MarkerName)); err != nil {
			t.Fatal(err)
		}
		// Scan re-reads the marker from disk, so the still-open workspace reports it.
		scan, err := ws.Scan(ctx)
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		if scan.Marker != MarkerMissing {
			t.Fatalf("Scan.Marker = %v, want MarkerMissing", scan.Marker)
		}
		ws.Close()
		if _, err := Open(ctx, root, opts); !errors.Is(err, errs.ErrWorkspaceCorrupt) {
			t.Fatalf("Open = %v, want ErrWorkspaceCorrupt", err)
		}
	})

	t.Run("invalid marker", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "ws")
		opts, _ := testOptions(t, base)
		ws, err := Init(ctx, root, opts)
		if err != nil {
			t.Fatal(err)
		}
		ws.Close()
		if err := os.WriteFile(filepath.Join(root, MarkerName), []byte(`{"app":"other"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(ctx, root, opts); !errors.Is(err, errs.ErrWorkspaceCorrupt) {
			t.Fatalf("Open = %v, want ErrWorkspaceCorrupt", err)
		}
	})

	t.Run("neither marker nor db", func(t *testing.T) {
		base := t.TempDir()
		opts, _ := testOptions(t, base)
		if _, err := Open(ctx, filepath.Join(base, "empty"), opts); !errors.Is(err, errs.ErrNotWorkspace) {
			t.Fatalf("Open(empty) = %v, want ErrNotWorkspace", err)
		}
		if _, err := Open(ctx, filepath.Join(base, "missing"), opts); !errors.Is(err, errs.ErrNotWorkspace) {
			t.Fatalf("Open(missing) = %v, want ErrNotWorkspace", err)
		}
	})

	t.Run("non-empty directory", func(t *testing.T) {
		base := t.TempDir()
		opts, _ := testOptions(t, base)
		dir := filepath.Join(base, "full")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Init(ctx, dir, opts); !errors.Is(err, errs.ErrDirNotEmpty) {
			t.Fatalf("Init(non-empty) = %v, want ErrDirNotEmpty", err)
		}

		// The two OS droppings are tolerated.
		lenient := filepath.Join(base, "lenient")
		if err := os.MkdirAll(lenient, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{".DS_Store", "desktop.ini"} {
			if err := os.WriteFile(filepath.Join(lenient, name), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		ws, err := Init(ctx, lenient, opts)
		if err != nil {
			t.Fatalf("Init with OS droppings: %v", err)
		}
		ws.Close()
	})

	t.Run("repoint to marker-less directory", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "ws")
		opts, cfg := testOptions(t, base)
		ws, err := Init(ctx, root, opts)
		if err != nil {
			t.Fatal(err)
		}
		defer ws.Close()
		if err := cfg.Save(ctx, config.AppConfig{SchemaVersion: 1, WorkspaceRoot: root}); err != nil {
			t.Fatal(err)
		}
		other := filepath.Join(base, "other")
		if err := os.MkdirAll(other, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := ws.Repoint(ctx, other); !errors.Is(err, errs.ErrNotWorkspace) {
			t.Fatalf("Repoint = %v, want ErrNotWorkspace", err)
		}
		got, err := cfg.Load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got.WorkspaceRoot != root {
			t.Fatalf("config WorkspaceRoot = %q, want %q (unchanged)", got.WorkspaceRoot, root)
		}
	})
}

func TestAC13RepointAfterCopy(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	root1 := filepath.Join(base, "ws1")
	root2 := filepath.Join(base, "ws2")
	opts, cfg := testOptions(t, base)

	ws, err := Init(ctx, root1, opts)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	projectStore := projects.New(ws.Global().DB(), opts.Clock)
	var projectID string
	if err := ws.Global().InTx(ctx, func(tx *sql.Tx) error {
		var err error
		projectID, err = projectStore.Create(ctx, tx, "Move Me", "", "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := ws.CreateProjectData(ctx, projectID); err != nil {
		t.Fatalf("CreateProjectData: %v", err)
	}
	project, err := ws.Project(ctx, projectID)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	noteID := ids.MustNew(opts.Clock)
	if err := project.InTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO notes(id, title, file_path, kind, created_at, updated_at)
			VALUES (?, 'n', ?, 'scratchpad', ?, ?)`,
			noteID, ws.Paths().NoteFile(projectID, noteID), clock.Format(opts.Clock.Now()), clock.Format(opts.Clock.Now()))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	paperStore := papers.New(ws.Global().DB(), opts.Clock)
	var paperID string
	if err := ws.Global().InTx(ctx, func(tx *sql.Tx) error {
		var err error
		paperID, _, err = paperStore.Upsert(ctx, tx, papers.Paper{ZoteroKey: "K1"})
		if err != nil {
			return err
		}
		return paperStore.UpdateFile(ctx, tx, paperID, ws.Paths().PaperPDF(paperID), "hash", 1, schema.PaperFileOK)
	}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(ctx, config.AppConfig{SchemaVersion: 1, WorkspaceRoot: root1}); err != nil {
		t.Fatal(err)
	}
	if err := ws.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	copyTree(t, root1, root2)

	ws2, err := Open(ctx, root1, opts)
	if err != nil {
		t.Fatalf("Open(root1): %v", err)
	}
	if err := ws2.Repoint(ctx, root2); err != nil {
		t.Fatalf("Repoint: %v", err)
	}
	got, err := cfg.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(got.WorkspaceRoot) != filepath.Clean(mustEval(t, root2)) {
		t.Fatalf("config WorkspaceRoot = %q, want %q", got.WorkspaceRoot, root2)
	}

	ws3, err := Open(ctx, root2, opts)
	if err != nil {
		t.Fatalf("Open(root2): %v", err)
	}
	defer ws3.Close()
	project2, err := ws3.Project(ctx, projectID)
	if err != nil {
		t.Fatalf("Project after repoint: %v", err)
	}
	var notePath string
	if err := project2.DB().QueryRow(`SELECT file_path FROM notes WHERE id = ?`, noteID).Scan(&notePath); err != nil {
		t.Fatalf("read note: %v", err)
	}
	paper, err := papers.New(ws3.Global().DB(), opts.Clock).Get(ctx, paperID)
	if err != nil {
		t.Fatalf("read paper: %v", err)
	}
	// Year 1: both stored paths stay internal and portable.
	for label, p := range map[string]string{"note": notePath, "paper": paper.FilePath} {
		if filepath.IsAbs(p) || strings.Contains(p, `\`) || strings.Contains(p, ":") {
			t.Fatalf("%s file_path %q is not a portable relative path", label, p)
		}
		if !strings.HasPrefix(p, "projects/") && !strings.HasPrefix(p, "library/") {
			t.Fatalf("%s file_path %q has an unexpected prefix", label, p)
		}
	}
}

func TestAC14ConcurrentAndNestedInTx(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	opts, _ := testOptions(t, base)
	ws, err := Init(ctx, root, opts)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer ws.Close()
	global := ws.Global()

	const goroutines = 2
	const rounds = 200
	errCh := make(chan error, goroutines*rounds)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			// Each goroutine owns its context value, which is the normal Go
			// convention and what keeps the per-handle tx registry separated.
			gctx := context.WithValue(context.Background(), goroutineKey{}, g)
			for r := range rounds {
				key := fmt.Sprintf("test.g%d.r%d", g, r)
				err := global.InTx(gctx, func(tx *sql.Tx) error {
					_, err := tx.Exec(`INSERT INTO settings(key, value, updated_at) VALUES (?, 'v', ?)
						ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
						key, clock.Format(opts.Clock.Now()))
					return err
				})
				if err != nil {
					errCh <- fmt.Errorf("goroutine %d round %d: %w", g, r, err)
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("InTx: %v", err)
	}
	var count int
	if err := global.DB().QueryRow(`SELECT count(*) FROM settings WHERE key LIKE 'test.%'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != goroutines*rounds {
		t.Fatalf("settings rows = %d, want %d", count, goroutines*rounds)
	}

	// Nested InTx with the same context reuses the outer transaction.
	var outer, inner *sql.Tx
	if err := global.InTx(ctx, func(tx *sql.Tx) error {
		outer = tx
		return global.InTx(ctx, func(tx2 *sql.Tx) error {
			inner = tx2
			_, err := tx2.Exec(`INSERT INTO settings(key, value, updated_at) VALUES ('nested', 'v', ?)`, clock.Format(opts.Clock.Now()))
			return err
		})
	}); err != nil {
		t.Fatalf("nested InTx: %v", err)
	}
	if outer == nil || inner == nil || outer != inner {
		t.Fatalf("nested InTx opened a second transaction: outer=%p inner=%p", outer, inner)
	}
	var nested int
	if err := global.DB().QueryRow(`SELECT count(*) FROM settings WHERE key = 'nested'`).Scan(&nested); err != nil {
		t.Fatal(err)
	}
	if nested != 1 {
		t.Fatal("nested write was not committed")
	}

	// An outer error rolls the nested write back.
	boom := errors.New("boom")
	err = global.InTx(ctx, func(tx *sql.Tx) error {
		if err := global.InTx(ctx, func(tx2 *sql.Tx) error {
			_, err := tx2.Exec(`INSERT INTO settings(key, value, updated_at) VALUES ('rolled-back', 'v', ?)`, clock.Format(opts.Clock.Now()))
			return err
		}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("outer InTx = %v, want boom", err)
	}
	var rolled int
	if err := global.DB().QueryRow(`SELECT count(*) FROM settings WHERE key = 'rolled-back'`).Scan(&rolled); err != nil {
		t.Fatal(err)
	}
	if rolled != 0 {
		t.Fatal("nested write survived the outer rollback")
	}
}

type goroutineKey struct{}

func TestProjectDataAndHandleCache(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	opts, _ := testOptions(t, base)
	ws, err := Init(ctx, filepath.Join(base, "ws"), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	projectID := ids.MustNew(opts.Clock)
	if err := ws.CreateProjectData(ctx, projectID); err != nil {
		t.Fatalf("CreateProjectData: %v", err)
	}
	for _, sub := range []string{"notes", "attachments"} {
		if _, err := os.Stat(filepath.Join(ws.Root(), "projects", projectID, sub)); err != nil {
			t.Fatalf("%s: %v", sub, err)
		}
	}
	if _, err := os.Stat(filepath.Join(ws.Root(), "projects", projectID, "project.db")); err != nil {
		t.Fatalf("project.db: %v", err)
	}
	first, err := ws.Project(ctx, projectID)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	second, err := ws.Project(ctx, projectID)
	if err != nil {
		t.Fatalf("Project again: %v", err)
	}
	if first != second {
		t.Fatal("Project did not cache the handle")
	}
	var version int
	if err := first.DB().QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("project schema version = %d, want 1", version)
	}

	if err := ws.CreateProjectData(ctx, "not-a-uuid"); err == nil {
		t.Fatal("CreateProjectData accepted an invalid id")
	}
	if _, err := ws.Project(ctx, "01920000-0000-7000-8000-000000000000"); err == nil {
		t.Fatal("Project opened a project without a directory")
	}
}

func TestScan(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	opts, _ := testOptions(t, base)
	root := filepath.Join(base, "ws")
	ws, err := Init(ctx, root, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	store := projects.New(ws.Global().DB(), opts.Clock)
	createProject := func(name string) string {
		var id string
		if err := ws.Global().InTx(ctx, func(tx *sql.Tx) error {
			var err error
			id, err = store.Create(ctx, tx, name, "", "")
			return err
		}); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return id
	}

	withData := createProject("with-data")
	if err := ws.CreateProjectData(ctx, withData); err != nil {
		t.Fatal(err)
	}
	withoutDir := createProject("without-dir")
	withoutDB := createProject("without-db")
	if err := os.MkdirAll(filepath.Join(root, "projects", withoutDB), 0o755); err != nil {
		t.Fatal(err)
	}
	unregistered := ids.MustNew(opts.Clock)
	if err := os.MkdirAll(filepath.Join(root, "projects", unregistered, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}

	scan, err := ws.Scan(ctx)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if scan.Marker != MarkerPresent {
		t.Fatalf("Marker = %v, want MarkerPresent", scan.Marker)
	}
	if !contains(scan.RegisteredWithoutDir, withoutDir) {
		t.Errorf("RegisteredWithoutDir = %v, want %s", scan.RegisteredWithoutDir, withoutDir)
	}
	if !contains(scan.RegisteredWithoutDB, withoutDB) {
		t.Errorf("RegisteredWithoutDB = %v, want %s", scan.RegisteredWithoutDB, withoutDB)
	}
	if !contains(scan.UnregisteredDirs, unregistered) {
		t.Errorf("UnregisteredDirs = %v, want %s", scan.UnregisteredDirs, unregistered)
	}
	if len(scan.SchemaIssues) != 0 {
		t.Errorf("SchemaIssues = %v, want none", scan.SchemaIssues)
	}

	// A database newer than the build is reported, not repaired.
	project, err := ws.Project(ctx, withData)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.InTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (99, 'future', 'x', ?)`,
			clock.Format(opts.Clock.Now()))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scan, err = ws.Scan(ctx)
	if err != nil {
		t.Fatalf("Scan after bump: %v", err)
	}
	reported := false
	for _, issue := range scan.SchemaIssues {
		if strings.Contains(issue, withData) && strings.Contains(issue, "newer than target") {
			reported = true
		}
	}
	if !reported {
		t.Fatalf("SchemaIssues = %v, want a newer-than-target entry for %s", scan.SchemaIssues, withData)
	}
}

func TestRepointRejectsNestedConfigDir(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	opts, _ := testOptions(t, base)
	ws, err := Init(ctx, root, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	// Move the whole workspace under a nested site by pointing the config file
	// inside the new workspace root.
	inner := filepath.Join(root, "config")
	cfg, err := config.NewStoreAt(filepath.Join(inner, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	opts.Config = cfg
	ws.opts.Config = cfg
	if err := ws.Repoint(ctx, root); err == nil {
		t.Fatal("Repoint accepted a config directory nested in the workspace")
	}
}

func TestCloseTwice(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	opts, _ := testOptions(t, base)
	ws, err := Init(ctx, filepath.Join(base, "ws"), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := ws.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if ws.Global() != nil {
		t.Fatal("Global() after Close is not nil")
	}
}

// --- helpers ---

func treePaths(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(out)
	return out
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o644)
	})
	if err != nil {
		t.Fatalf("copy %s -> %s: %v", src, dst, err)
	}
}

func mustEval(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", path, err)
	}
	return resolved
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestProjectEventsWiring(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	opts, _ := testOptions(t, base)
	ws, err := Init(ctx, filepath.Join(base, "ws"), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	projectID := ids.MustNew(opts.Clock)
	if err := ws.CreateProjectData(ctx, projectID); err != nil {
		t.Fatalf("CreateProjectData: %v", err)
	}
	handle, err := ws.Project(ctx, projectID)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	entityID := ids.MustNew(opts.Clock)
	ev := events.Event{
		Type:       schema.EventEntityCreated,
		EntityType: schema.EntityTask,
		EntityID:   entityID,
		Summary:    "created",
		Actor:      schema.ActorUser,
	}
	if err := handle.InTx(ctx, func(tx *sql.Tx) error {
		return handle.Events().Emit(ctx, tx, ev)
	}); err != nil {
		t.Fatalf("Emit through handle: %v", err)
	}
	list, err := handle.Reader().List(ctx, events.Query{EntityID: entityID})
	if err != nil {
		t.Fatalf("Reader.List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("Reader.List returned %d rows, want 1", len(list))
	}
	if !list[0].CreatedAt.Equal(opts.Clock.Now()) {
		t.Fatalf("CreatedAt = %v, want %v", list[0].CreatedAt, opts.Clock.Now())
	}
}
