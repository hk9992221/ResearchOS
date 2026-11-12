package workspace

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/papers"
	"researchos/internal/foundation/projectpapers"
	"researchos/internal/foundation/projects"
	"researchos/internal/foundation/relations"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/settings"
)

func mustCreateProject(t *testing.T, ctx context.Context, ws *Workspace, store *projects.Store, name string) string {
	t.Helper()
	var id string
	if err := ws.Global().InTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = store.Create(ctx, tx, name, "", "")
		return err
	}); err != nil {
		t.Fatalf("create project %s: %v", name, err)
	}
	if err := ws.CreateProjectData(ctx, id); err != nil {
		t.Fatalf("create project data: %v", err)
	}
	return id
}

func mustCreatePaper(t *testing.T, ctx context.Context, ws *Workspace, store *papers.Store, key string) string {
	t.Helper()
	var id string
	if err := ws.Global().InTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, _, err = store.Upsert(ctx, tx, papers.Paper{ZoteroKey: key})
		if err != nil {
			return err
		}
		return store.UpdateFile(ctx, tx, id, ws.Paths().PaperPDF(id), "hash-"+key, 10, schema.PaperFileOK)
	}); err != nil {
		t.Fatalf("create paper %s: %v", key, err)
	}
	abs, err := ws.Paths().Abs(ws.Paths().PaperDir(id))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(abs, "source.pdf"), []byte("pdf"), 0o644); err != nil {
		t.Fatal(err)
	}
	return id
}

func issueDelete(t *testing.T, opts Options, projectID string) confirm.Token {
	t.Helper()
	token, err := opts.Confirm.Issue(context.Background(), confirm.Scope{
		Kind:      confirm.KindDeleteProject,
		ProjectID: projectID,
	})
	if err != nil {
		t.Fatalf("issue delete token: %v", err)
	}
	return token
}

func TestAC09DeleteProjectAndPaperGC(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	opts, _ := testOptions(t, base)
	ws, err := Init(ctx, filepath.Join(base, "ws"), opts)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer ws.Close()

	projectStore := projects.New(ws.Global().DB(), opts.Clock)
	paperStore := papers.New(ws.Global().DB(), opts.Clock)
	a := mustCreateProject(t, ctx, ws, projectStore, "A")
	b := mustCreateProject(t, ctx, ws, projectStore, "B")

	shared := mustCreatePaper(t, ctx, ws, paperStore, "SHARED")
	viaRelation := mustCreatePaper(t, ctx, ws, paperStore, "VIARELATION")

	handleA, err := ws.Project(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	handleB, err := ws.Project(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	linksA := projectpapers.New(a, handleA.DB(), handleA.Events(), opts.Clock)
	linksB := projectpapers.New(b, handleB.DB(), handleB.Events(), opts.Clock)
	for _, pair := range []struct {
		handle *ProjectHandle
		store  *projectpapers.Store
		paper  string
	}{
		{handleA, linksA, shared},
		{handleB, linksB, shared},
		{handleA, linksA, viaRelation},
	} {
		pair := pair
		if err := pair.handle.InTx(ctx, func(tx *sql.Tx) error {
			_, err := pair.store.Link(ctx, tx, projectpapers.Link{
				ProjectID: pair.handle.ID, PaperID: pair.paper, LinkedBy: schema.ActorUser,
			})
			return err
		}); err != nil {
			t.Fatalf("link paper: %v", err)
		}
	}
	// B references viaRelation only through a Note -> Paper relation.
	now := opts.Clock.Now()
	noteID := ids.MustNew(opts.Clock)
	if err := handleB.InTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO notes(id, title, file_path, kind, created_at, updated_at)
			 VALUES (?, 'n', ?, 'scratchpad', ?, ?)`,
			noteID, ws.Paths().NoteFile(b, noteID), now.Format("2006-01-02T15:04:05.000Z"),
			now.Format("2006-01-02T15:04:05.000Z"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	relB := relations.New(handleB.DB(), ws.Global().DB(), handleB.Events(), opts.Clock)
	if err := handleB.InTx(ctx, func(tx *sql.Tx) error {
		_, err := relB.Link(ctx, tx, relations.Link{
			SrcType: schema.EntityNote, SrcID: noteID, Relation: schema.RelationReferences,
			DstType: schema.EntityPaper, DstID: viaRelation,
		}, schema.ActorUser)
		return err
	}); err != nil {
		t.Fatalf("link relation: %v", err)
	}

	globalSettings := settings.NewWorkspace(ws.Global().DB()).WithClock(opts.Clock)
	if err := globalSettings.Set(ctx, nil, "project."+a+".export.dir", "/a"); err != nil {
		t.Fatal(err)
	}
	if err := globalSettings.Set(ctx, nil, "project."+b+".export.dir", "/b"); err != nil {
		t.Fatal(err)
	}

	res, err := ws.DeleteProjectData(ctx, DeleteProjectRequest{ProjectID: a, Confirm: issueDelete(t, opts, a)})
	if err != nil {
		t.Fatalf("DeleteProjectData(A): %v", err)
	}
	if res.RemovedProjects != 1 || res.RemovedDirs != 1 {
		t.Fatalf("result = %+v", res)
	}
	if len(res.RemovedPapers) != 0 {
		t.Fatalf("RemovedPapers = %v, want none", res.RemovedPapers)
	}
	for _, want := range []string{shared, viaRelation} {
		if !slices.Contains(res.RetainedPapers, want) {
			t.Fatalf("RetainedPapers = %v, want %s", res.RetainedPapers, want)
		}
		if _, err := paperStore.Get(ctx, want); err != nil {
			t.Fatalf("paper %s was removed: %v", want, err)
		}
		abs, _ := ws.Paths().Abs(ws.Paths().PaperDir(want))
		if _, err := os.Stat(abs); err != nil {
			t.Fatalf("paper copy %s was removed: %v", want, err)
		}
	}
	if _, err := projectStore.Get(ctx, a); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("project A registration still exists: %v", err)
	}
	aDir, _ := ws.Paths().Abs(ws.Paths().ProjectDir(a))
	if _, err := os.Stat(aDir); !os.IsNotExist(err) {
		t.Fatalf("project A directory still exists")
	}
	if _, ok, err := globalSettings.Get(ctx, "project."+a+".export.dir"); err != nil || ok {
		t.Fatalf("project A compatible setting survived: %v, %v", ok, err)
	}
	if _, ok, err := globalSettings.Get(ctx, "project."+b+".export.dir"); err != nil || !ok {
		t.Fatalf("project B compatible setting was removed: %v, %v", ok, err)
	}

	// Deleting B removes the last references, so both copies are collected.
	res2, err := ws.DeleteProjectData(ctx, DeleteProjectRequest{ProjectID: b, Confirm: issueDelete(t, opts, b)})
	if err != nil {
		t.Fatalf("DeleteProjectData(B): %v", err)
	}
	for _, want := range []string{shared, viaRelation} {
		if !slices.Contains(res2.RemovedPapers, want) {
			t.Fatalf("RemovedPapers = %v, want %s", res2.RemovedPapers, want)
		}
		if _, err := paperStore.Get(ctx, want); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("paper %s row survived: %v", want, err)
		}
		abs, _ := ws.Paths().Abs(ws.Paths().PaperDir(want))
		if _, err := os.Stat(abs); !os.IsNotExist(err) {
			t.Fatalf("paper copy %s survived", want)
		}
	}
	if len(res2.RetainedPapers) != 0 {
		t.Fatalf("RetainedPapers = %v, want none", res2.RetainedPapers)
	}
}

func TestAC28PruneUnreferencedPapers(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	opts, _ := testOptions(t, base)
	ws, err := Init(ctx, filepath.Join(base, "ws"), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	projectStore := projects.New(ws.Global().DB(), opts.Clock)
	paperStore := papers.New(ws.Global().DB(), opts.Clock)
	projectID := mustCreateProject(t, ctx, ws, projectStore, "P")
	handle, err := ws.Project(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	links := projectpapers.New(projectID, handle.DB(), handle.Events(), opts.Clock)
	referenced := mustCreatePaper(t, ctx, ws, paperStore, "REFERENCED")
	free := mustCreatePaper(t, ctx, ws, paperStore, "FREE")
	softDeleted := mustCreatePaper(t, ctx, ws, paperStore, "SOFTDELETED")

	if err := handle.InTx(ctx, func(tx *sql.Tx) error {
		_, err := links.Link(ctx, tx, projectpapers.Link{
			ProjectID: projectID, PaperID: referenced, LinkedBy: schema.ActorUser,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// A soft-deleted evidence row still counts as a reference.
	now := opts.Clock.Now().Format("2006-01-02T15:04:05.000Z")
	ideaID := ids.MustNew(opts.Clock)
	if err := handle.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO ideas(id, title, created_at, updated_at) VALUES (?, 'i', ?, ?)`, ideaID, now, now); err != nil {
			return err
		}
		_, err := tx.Exec(
			`INSERT INTO evidence(id, idea_id, direction, source_type, source_id, created_at, updated_at, deleted_at)
			 VALUES (?, ?, 'support', 'paper', ?, ?, ?, ?)`,
			ids.MustNew(opts.Clock), ideaID, softDeleted, now, now, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	res, err := ws.PruneUnreferencedPapers(ctx, []string{free, referenced, softDeleted, ids.MustNew(opts.Clock)})
	if err != nil {
		t.Fatalf("PruneUnreferencedPapers: %v", err)
	}
	if !slices.Contains(res.DeletedPapers, free) {
		t.Fatalf("DeletedPapers = %v, want %s", res.DeletedPapers, free)
	}
	for _, want := range []string{referenced, softDeleted} {
		if !slices.Contains(res.Retained, want) {
			t.Fatalf("Retained = %v, want %s", res.Retained, want)
		}
	}
	if _, err := paperStore.Get(ctx, free); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("free paper row survived: %v", err)
	}
	freeDir, _ := ws.Paths().Abs(ws.Paths().PaperDir(free))
	if _, err := os.Stat(freeDir); !os.IsNotExist(err) {
		t.Fatalf("free paper copy survived")
	}
	for _, want := range []string{referenced, softDeleted} {
		abs, _ := ws.Paths().Abs(ws.Paths().PaperDir(want))
		if _, err := os.Stat(abs); err != nil {
			t.Fatalf("retained paper copy %s is gone: %v", want, err)
		}
	}

	// Second run is idempotent: nothing is deleted again.
	again, err := ws.PruneUnreferencedPapers(ctx, []string{free, referenced, softDeleted})
	if err != nil {
		t.Fatalf("second Prune: %v", err)
	}
	if len(again.DeletedPapers) != 0 {
		t.Fatalf("second prune deleted %v, want nothing", again.DeletedPapers)
	}
	if !slices.Contains(again.Retained, referenced) || !slices.Contains(again.Retained, softDeleted) {
		t.Fatalf("second prune retained = %v", again.Retained)
	}
}

func TestDeleteProjectAbortsPaperGCOnCorruptProject(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	opts, _ := testOptions(t, base)
	ws, err := Init(ctx, filepath.Join(base, "ws"), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	projectStore := projects.New(ws.Global().DB(), opts.Clock)
	paperStore := papers.New(ws.Global().DB(), opts.Clock)
	a := mustCreateProject(t, ctx, ws, projectStore, "A")
	b := mustCreateProject(t, ctx, ws, projectStore, "B")
	paperID := mustCreatePaper(t, ctx, ws, paperStore, "K")
	handleA, err := ws.Project(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if err := handleA.InTx(ctx, func(tx *sql.Tx) error {
		_, err := projectpapers.New(a, handleA.DB(), handleA.Events(), opts.Clock).Link(ctx, tx, projectpapers.Link{
			ProjectID: a, PaperID: paperID, LinkedBy: schema.ActorUser,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	handleB, err := ws.Project(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if err := handleB.InTx(ctx, func(tx *sql.Tx) error {
		_, err := projectpapers.New(b, handleB.DB(), handleB.Events(), opts.Clock).Link(ctx, tx, projectpapers.Link{
			ProjectID: b, PaperID: paperID, LinkedBy: schema.ActorUser,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Break B's database after closing its handle.
	if err := handleB.Close(); err != nil {
		t.Fatalf("close B: %v", err)
	}
	dbPath := filepath.Join(ws.Root(), "projects", b, "project.db")
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(dbPath + suffix)
	}
	if err := os.WriteFile(dbPath, []byte("this is not a database"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ws.DeleteProjectData(ctx, DeleteProjectRequest{ProjectID: a, Confirm: issueDelete(t, opts, a)})
	if err != nil {
		t.Fatalf("DeleteProjectData(A): %v", err)
	}
	if res.RemovedProjects != 1 {
		t.Fatalf("project A was not deleted: %+v", res)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("no warning was produced for the corrupt project database")
	}
	if !slices.Contains(res.RetainedPapers, paperID) {
		t.Fatalf("RetainedPapers = %v, want %s", res.RetainedPapers, paperID)
	}
	if _, err := paperStore.Get(ctx, paperID); err != nil {
		t.Fatalf("paper row was removed despite the corrupt project: %v", err)
	}
	abs, _ := ws.Paths().Abs(ws.Paths().PaperDir(paperID))
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("paper copy was removed despite the corrupt project: %v", err)
	}
	if _, err := projectStore.Get(ctx, a); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("project A registration survived: %v", err)
	}
	if _, err := projectStore.Get(ctx, b); err != nil {
		t.Fatalf("project B registration was touched: %v", err)
	}
}

func TestDeleteProjectWithoutConfirmService(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	opts, _ := testOptions(t, base)
	opts.Confirm = nil
	ws, err := Init(ctx, filepath.Join(base, "ws"), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	projectID := mustCreateProject(t, ctx, ws, projects.New(ws.Global().DB(), opts.Clock), "P")
	_, err = ws.DeleteProjectData(ctx, DeleteProjectRequest{ProjectID: projectID, Confirm: "token"})
	if !errors.Is(err, errs.ErrUserConfirmationRequired) {
		t.Fatalf("DeleteProjectData without confirm service = %v, want ErrUserConfirmationRequired", err)
	}
	if _, err := projects.New(ws.Global().DB(), opts.Clock).Get(ctx, projectID); err != nil {
		t.Fatalf("project was deleted without confirmation: %v", err)
	}
}
