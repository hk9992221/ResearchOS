package diag

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/config"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/projects"
	"researchos/internal/foundation/workspace"
)

func setupDiag(t *testing.T, c *clock.Fixed) (*workspace.Workspace, config.Store, string) {
	t.Helper()
	ctx := context.Background()
	base := t.TempDir()
	cfg, err := config.NewStoreAt(filepath.Join(base, "config", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Init(ctx, filepath.Join(base, "ws"), workspace.Options{
		Clock: c, SkipBackup: true, Config: cfg,
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { ws.Close() })
	store := projects.New(ws.Global().DB(), c)
	var projectID string
	if err := ws.Global().InTx(ctx, func(tx *sql.Tx) error {
		var err error
		projectID, err = store.Create(ctx, tx, "P", "", "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := ws.CreateProjectData(ctx, projectID); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(ctx, config.AppConfig{SchemaVersion: 1, WorkspaceRoot: ws.Root()}); err != nil {
		t.Fatal(err)
	}
	return ws, cfg, projectID
}

func TestAC18Diagnostics(t *testing.T) {
	ctx := context.Background()
	c := &clock.Fixed{T: time.Date(2027, 10, 11, 12, 13, 14, 0, time.UTC)}
	ws, cfg, projectID := setupDiag(t, c)
	handle, err := ws.Project(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	now := clock.Format(c.Now())
	missingNote := ids.MustNew(c)
	if err := handle.InTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO notes(id, title, file_path, kind, created_at, updated_at, file_state)
			 VALUES (?, 'lost', ?, 'scratchpad', ?, ?, 'ok')`,
			missingNote, ws.Paths().NoteFile(projectID, missingNote), now, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := handle.InTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO external_refs(id, owner_type, owner_id, field, ref_kind, path, status, created_at, updated_at)
			 VALUES (?, 'experiment', ?, 'dir_ref', 'directory', '/nowhere', 'broken', ?, ?)`,
			ids.MustNew(c), ids.MustNew(c), now, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	unregistered := ids.MustNew(c)
	if err := os.MkdirAll(filepath.Join(ws.Root(), "projects", unregistered, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}

	rep, err := Run(ctx, ws, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.Global.IntegrityOK || rep.Global.SchemaVersion != 1 || rep.Global.TargetVersion != 1 {
		t.Fatalf("global report = %+v", rep.Global)
	}
	if rep.Global.Path != filepath.Join(ws.Root(), "global.db") || rep.Global.SizeBytes <= 0 {
		t.Fatalf("global path/size = %+v", rep.Global)
	}
	if len(rep.Projects) != 1 || rep.Projects[0].ProjectID != projectID {
		t.Fatalf("projects = %+v", rep.Projects)
	}
	pr := rep.Projects[0]
	if pr.DB == nil || !pr.DB.IntegrityOK || pr.DB.SchemaVersion != 1 {
		t.Fatalf("project DB report = %+v", pr.DB)
	}
	if !pr.DirExists || pr.NotesTotal != 1 || pr.NotesMissing != 0 || pr.BrokenRefs != 1 {
		t.Fatalf("project report = %+v", pr)
	}
	if rep.Config.Path != cfg.Path() || !rep.Config.Exists || rep.Config.WorkspaceRoot != ws.Root() || !rep.Config.RootReachable {
		t.Fatalf("config report = %+v", rep.Config)
	}

	scanned, err := Run(ctx, ws, Options{IncludeFileScan: true})
	if err != nil {
		t.Fatal(err)
	}
	if scanned.Projects[0].NotesMissing < 1 {
		t.Fatalf("IncludeFileScan NotesMissing = %d, want >= 1", scanned.Projects[0].NotesMissing)
	}

	scan, err := ws.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range scan.UnregisteredDirs {
		if id == unregistered {
			found = true
		}
	}
	if !found {
		t.Fatalf("Workspace.Scan did not report the unregistered directory: %+v", scan)
	}

	// The JSON report is stable across runs once GeneratedAt is normalized.
	first, err := Run(ctx, ws, Options{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(ctx, ws, Options{})
	if err != nil {
		t.Fatal(err)
	}
	first.GeneratedAt = time.Time{}
	second.GeneratedAt = time.Time{}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("diagnostic JSON is not stable:\n%s\n%s", firstJSON, secondJSON)
	}
	if !strings.Contains(string(firstJSON), `"Projects"`) {
		t.Fatalf("unexpected JSON shape: %s", firstJSON)
	}

	// A ProjectIDs filter reports missing databases without inventing data.
	filtered, err := Run(ctx, ws, Options{ProjectIDs: []string{ids.MustNew(c)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Projects) != 1 || filtered.Projects[0].DB != nil || filtered.Projects[0].DirExists {
		t.Fatalf("filtered projects = %+v", filtered.Projects)
	}

	// A corrupt config is quarantined and surfaced as CorruptBackup.
	if err := os.WriteFile(cfg.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	corrupt, err := Run(ctx, ws, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if corrupt.Config.CorruptBackup == "" {
		t.Fatalf("CorruptBackup = %q, want the quarantined file", corrupt.Config.CorruptBackup)
	}
	if _, err := os.Stat(corrupt.Config.CorruptBackup); err != nil {
		t.Fatalf("quarantined file missing: %v", err)
	}
}
