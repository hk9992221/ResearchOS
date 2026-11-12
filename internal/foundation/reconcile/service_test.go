package reconcile

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/notefile"
	"researchos/internal/foundation/projects"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/workspace"
)

func setupProject(t *testing.T, c *clock.Fixed) (*workspace.Workspace, string) {
	t.Helper()
	ctx := context.Background()
	base := t.TempDir()
	ws, err := workspace.Init(ctx, filepath.Join(base, "ws"), workspace.Options{Clock: c, SkipBackup: true})
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
	return ws, projectID
}

func writeFile(t *testing.T, relAbs string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(relAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(relAbs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func noteRel(ws *workspace.Workspace, projectID, name string) string {
	return ws.Paths().NoteFile(projectID, name)
}

func noteAbs(t *testing.T, ws *workspace.Workspace, rel string) string {
	t.Helper()
	abs, err := ws.Paths().Abs(rel)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func countNotes(t *testing.T, ws *workspace.Workspace, projectID string) int {
	t.Helper()
	handle, err := ws.Project(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := handle.DB().QueryRow(`SELECT count(*) FROM notes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAC10ReconcileFlow(t *testing.T) {
	ctx := context.Background()
	c := &clock.Fixed{T: time.Date(2027, 5, 6, 7, 8, 9, 0, time.UTC)}
	ws, projectID := setupProject(t, c)

	orphanRel := noteRel(ws, projectID, "orphan")
	plainRel := noteRel(ws, projectID, "plain")
	customRel := noteRel(ws, projectID, "custom")
	writeFile(t, noteAbs(t, ws, orphanRel), "# Derived Title\n\nbody")
	writeFile(t, noteAbs(t, ws, plainRel), "no heading here")
	writeFile(t, noteAbs(t, ws, customRel), "custom body")
	// Noise that must be ignored by the scan.
	writeFile(t, filepath.Join(filepath.Dir(noteAbs(t, ws, orphanRel)), ".hidden.md"), "x")
	writeFile(t, filepath.Join(filepath.Dir(noteAbs(t, ws, orphanRel)), ".foo.md.tmp-1"), "x")
	writeFile(t, filepath.Join(filepath.Dir(noteAbs(t, ws, orphanRel)), "data.txt"), "x")

	rep, err := Scan(ctx, ws, projectID)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Orphans) != 3 || rep.Scanned != 3 {
		t.Fatalf("initial scan = %+v", rep)
	}
	if !rep.GeneratedAt.Equal(c.Now()) {
		t.Fatalf("GeneratedAt = %v, want %v", rep.GeneratedAt, c.Now())
	}

	res, err := Apply(ctx, ws, projectID, []Action{
		{Kind: ActionAdopt, RelPath: orphanRel, AdoptOptions: AdoptOptions{Actor: schema.ActorUser}},
		{Kind: ActionAdopt, RelPath: plainRel, AdoptOptions: AdoptOptions{Actor: schema.ActorUser}},
		{Kind: ActionAdopt, RelPath: customRel, AdoptOptions: AdoptOptions{Title: "Custom", Kind: schema.NoteOverview, Actor: schema.ActorUser}},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Items) != 3 {
		t.Fatalf("items = %d", len(res.Items))
	}
	for i, item := range res.Items {
		if item.Err != nil {
			t.Fatalf("adopt %d: %v", i, item.Err)
		}
		if !ids.Valid(item.NoteID) {
			t.Fatalf("adopt %d id = %q", i, item.NoteID)
		}
	}
	handle, err := ws.Project(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	titles := map[string]struct {
		title string
		kind  string
	}{
		res.Items[0].NoteID: {"Derived Title", string(schema.NoteScratchpad)},
		res.Items[1].NoteID: {"plain", string(schema.NoteScratchpad)},
		res.Items[2].NoteID: {"Custom", string(schema.NoteOverview)},
	}
	for id, want := range titles {
		var title, kind, state, hash string
		if err := handle.DB().QueryRow(
			`SELECT title, kind, file_state, file_hash FROM notes WHERE id = ?`, id).
			Scan(&title, &kind, &state, &hash); err != nil {
			t.Fatalf("read note %s: %v", id, err)
		}
		if title != want.title || kind != want.kind || state != "ok" {
			t.Fatalf("note %s = (%q, %q, %q)", id, title, kind, state)
		}
	}
	var created int
	if err := handle.DB().QueryRow(`SELECT count(*) FROM activities WHERE event_type = 'entity.created'`).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created != 3 {
		t.Fatalf("entity.created = %d, want 3", created)
	}
	if rep, err = Scan(ctx, ws, projectID); err != nil || len(rep.Orphans) != 0 {
		t.Fatalf("scan after adopt = %+v, %v", rep, err)
	}

	// Deleting the file surfaces a Missing entry; MarkBroken keeps the row.
	if err := os.Remove(noteAbs(t, ws, orphanRel)); err != nil {
		t.Fatal(err)
	}
	rep, err = Scan(ctx, ws, projectID)
	if err != nil || len(rep.Missing) != 1 || rep.Missing[0].NoteID != res.Items[0].NoteID {
		t.Fatalf("missing scan = %+v, %v", rep, err)
	}
	rowsBefore := countNotes(t, ws, projectID)
	if res, err = Apply(ctx, ws, projectID, []Action{{Kind: ActionMarkBroken, NoteID: rep.Missing[0].NoteID}}); err != nil {
		t.Fatalf("Apply(mark broken): %v", err)
	}
	if res.Items[0].Err != nil {
		t.Fatalf("mark broken: %v", res.Items[0].Err)
	}
	var state string
	if err := handle.DB().QueryRow(`SELECT file_state FROM notes WHERE id = ?`, rep.Missing[0].NoteID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "missing" {
		t.Fatalf("file_state = %q", state)
	}
	if countNotes(t, ws, projectID) != rowsBefore {
		t.Fatal("MarkBroken deleted a row")
	}

	// Reattach to a recovered file.
	movedRel := noteRel(ws, projectID, "moved")
	writeFile(t, noteAbs(t, ws, movedRel), "# Moved\ncontent")
	if res, err = Apply(ctx, ws, projectID, []Action{{
		Kind: ActionReattach, NoteID: rep.Missing[0].NoteID, NewRelPath: movedRel, AdoptOptions: AdoptOptions{Actor: schema.ActorUser},
	}}); err != nil {
		t.Fatalf("Apply(reattach): %v", err)
	}
	if res.Items[0].Err != nil {
		t.Fatalf("reattach: %v", res.Items[0].Err)
	}
	var filePath, hash string
	if err := handle.DB().QueryRow(`SELECT file_path, file_hash, file_state FROM notes WHERE id = ?`, rep.Missing[0].NoteID).
		Scan(&filePath, &hash, &state); err != nil {
		t.Fatal(err)
	}
	if filePath != movedRel || state != "ok" || hash != notefile.HashBytes([]byte("# Moved\ncontent")) {
		t.Fatalf("reattach = (%q, %q, %q)", filePath, state, hash)
	}
	if rep, err = Scan(ctx, ws, projectID); err != nil || len(rep.Missing) != 0 {
		t.Fatalf("scan after reattach = %+v, %v", rep, err)
	}

	// External edit: hash refresh only, file and title untouched.
	writeFile(t, noteAbs(t, ws, movedRel), "changed content")
	rep, err = Scan(ctx, ws, projectID)
	if err != nil || len(rep.Modified) != 1 || rep.Modified[0].DBHash == rep.Modified[0].DiskHash {
		t.Fatalf("modified scan = %+v, %v", rep, err)
	}
	if res, err = Apply(ctx, ws, projectID, []Action{{
		Kind: ActionAcceptExternalEdit, NoteID: rep.Modified[0].NoteID, AdoptOptions: AdoptOptions{Actor: schema.ActorUser},
	}}); err != nil {
		t.Fatalf("Apply(accept): %v", err)
	}
	if res.Items[0].Err != nil {
		t.Fatalf("accept: %v", res.Items[0].Err)
	}
	body, err := os.ReadFile(noteAbs(t, ws, movedRel))
	if err != nil || string(body) != "changed content" {
		t.Fatalf("accept changed the file: %q, %v", body, err)
	}
	if rep, err = Scan(ctx, ws, projectID); err != nil || len(rep.Modified) != 0 {
		t.Fatalf("scan after accept = %+v, %v", rep, err)
	}

	// Recreate writes an empty body at the recorded path.
	if err := os.Remove(noteAbs(t, ws, movedRel)); err != nil {
		t.Fatal(err)
	}
	rep, err = Scan(ctx, ws, projectID)
	if err != nil || len(rep.Missing) != 1 {
		t.Fatalf("missing before recreate = %+v, %v", rep, err)
	}
	if res, err = Apply(ctx, ws, projectID, []Action{{
		Kind: ActionRecreate, NoteID: rep.Missing[0].NoteID, AdoptOptions: AdoptOptions{Actor: schema.ActorUser},
	}}); err != nil {
		t.Fatalf("Apply(recreate): %v", err)
	}
	if res.Items[0].Err != nil {
		t.Fatalf("recreate: %v", res.Items[0].Err)
	}
	recreated, err := os.ReadFile(noteAbs(t, ws, movedRel))
	if err != nil || len(recreated) != 0 {
		t.Fatalf("recreated file = %q, %v", recreated, err)
	}
	if err := handle.DB().QueryRow(`SELECT file_hash, file_state FROM notes WHERE id = ?`, rep.Missing[0].NoteID).
		Scan(&hash, &state); err != nil {
		t.Fatal(err)
	}
	if hash != notefile.HashBytes(nil) || state != "ok" {
		t.Fatalf("recreate row = (%q, %q)", hash, state)
	}
	if rep, err = Scan(ctx, ws, projectID); err != nil || len(rep.Missing) != 0 {
		t.Fatalf("scan after recreate = %+v, %v", rep, err)
	}
	if countNotes(t, ws, projectID) != rowsBefore {
		t.Fatal("the reconcile flow deleted a note row")
	}
	// The untouched orphan files are still on disk.
	for _, rel := range []string{plainRel, customRel} {
		if _, err := os.Stat(noteAbs(t, ws, rel)); err != nil {
			t.Fatalf("file %s disappeared: %v", rel, err)
		}
	}
}

func TestApplyPerActionIndependence(t *testing.T) {
	ctx := context.Background()
	c := &clock.Fixed{T: time.Date(2027, 6, 7, 8, 9, 10, 0, time.UTC)}
	ws, projectID := setupProject(t, c)
	rel := noteRel(ws, projectID, "orphan")
	writeFile(t, noteAbs(t, ws, rel), "# Orphan")
	res, err := Apply(ctx, ws, projectID, []Action{
		{Kind: ActionAdopt, RelPath: rel, AdoptOptions: AdoptOptions{Actor: schema.ActorUser}},
		{Kind: ActionMarkBroken, NoteID: ids.MustNew(c)},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("items = %d", len(res.Items))
	}
	if res.Items[0].Err != nil {
		t.Fatalf("good action failed: %v", res.Items[0].Err)
	}
	if res.Items[1].Err == nil {
		t.Fatal("bad action reported success")
	}
	if countNotes(t, ws, projectID) != 1 {
		t.Fatal("the good action was not committed")
	}
}

func TestAdoptRejectsOccupiedPath(t *testing.T) {
	ctx := context.Background()
	c := &clock.Fixed{T: time.Date(2027, 7, 8, 9, 10, 11, 0, time.UTC)}
	ws, projectID := setupProject(t, c)
	rel := noteRel(ws, projectID, "dup")
	writeFile(t, noteAbs(t, ws, rel), "body")
	if _, err := Apply(ctx, ws, projectID, []Action{
		{Kind: ActionAdopt, RelPath: rel, AdoptOptions: AdoptOptions{Actor: schema.ActorUser}},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(ctx, ws, projectID, []Action{
		{Kind: ActionAdopt, RelPath: rel, AdoptOptions: AdoptOptions{Actor: schema.ActorUser}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].Err == nil {
		t.Fatal("adopting an occupied path succeeded")
	}
	if countNotes(t, ws, projectID) != 1 {
		t.Fatal("occupied adopt inserted a row")
	}
}

func TestScanNullHashBaselineIsModified(t *testing.T) {
	ctx := context.Background()
	c := &clock.Fixed{T: time.Date(2027, 8, 9, 10, 11, 12, 0, time.UTC)}
	ws, projectID := setupProject(t, c)
	handle, err := ws.Project(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	noteID := ids.MustNew(c)
	rel := noteRel(ws, projectID, "baseline")
	writeFile(t, noteAbs(t, ws, rel), "body")
	now := clock.Format(c.Now())
	if err := handle.InTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO notes(id, title, file_path, kind, created_at, updated_at, file_hash)
			 VALUES (?, 't', ?, 'scratchpad', ?, ?, NULL)`, noteID, rel, now, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rep, err := Scan(ctx, ws, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Modified) != 1 || rep.Modified[0].NoteID != noteID || rep.Modified[0].DBHash != "" {
		t.Fatalf("Modified = %+v, want one row with an empty DB hash", rep.Modified)
	}
	if rep.Modified[0].DiskHash != notefile.HashBytes([]byte("body")) {
		t.Fatalf("DiskHash = %q", rep.Modified[0].DiskHash)
	}
}

func TestScanSkipsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" && os.Getenv("RESEARCHOS_TEST_SYMLINKS") != "1" {
		t.Skip("symlink tests are opt-in on Windows (set RESEARCHOS_TEST_SYMLINKS=1)")
	}
	ctx := context.Background()
	c := &clock.Fixed{T: time.Date(2027, 9, 10, 11, 12, 13, 0, time.UTC)}
	ws, projectID := setupProject(t, c)
	outside := filepath.Join(t.TempDir(), "outside.md")
	writeFile(t, outside, "# outside")
	linkRel := noteRel(ws, projectID, "link")
	linkAbs := noteAbs(t, ws, linkRel)
	if err := os.Symlink(outside, linkAbs); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	rep, err := Scan(ctx, ws, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scanned != 0 || len(rep.Orphans) != 0 {
		t.Fatalf("symlink was scanned: %+v", rep)
	}
	res, err := Apply(ctx, ws, projectID, []Action{
		{Kind: ActionAdopt, RelPath: linkRel, AdoptOptions: AdoptOptions{Actor: schema.ActorUser}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].Err == nil {
		t.Fatal("adopting a symlink succeeded")
	}
}
