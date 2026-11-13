package pages_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/config"
	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/diag"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/externalrefs"
	"researchos/internal/foundation/reconcile"
	fworkspace "researchos/internal/foundation/workspace"
	"researchos/internal/ports"
	"researchos/internal/project"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/pages"
	"researchos/internal/ui/tokens"
	"researchos/internal/workspace"
)

// ---- fakes -------------------------------------------------------------

type fakeSetup struct {
	insp        pages.SetupInspection
	inspErr     error
	initErr     error
	openErr     error
	root        string
	initialized bool
	opened      bool
	initPath    string
}

func (f *fakeSetup) Inspect(context.Context, string) (pages.SetupInspection, error) {
	return f.insp, f.inspErr
}
func (f *fakeSetup) Initialize(_ context.Context, root string) error {
	f.initialized = true
	f.initPath = root
	return f.initErr
}
func (f *fakeSetup) Open(context.Context, string) error {
	f.opened = true
	return f.openErr
}
func (f *fakeSetup) CurrentRoot() string { return f.root }

type fakeTrash struct {
	items    []pages.TrashItem
	confirm  confirm.Service
	purged   []pages.TrashRef
	restored []pages.TrashRef
	emptied  int
	emptyTok confirm.Token
	purgeTok confirm.Token
}

func (f *fakeTrash) List(context.Context, string) ([]pages.TrashItem, error) {
	return f.items, nil
}
func (f *fakeTrash) Restore(_ context.Context, _ string, refs []pages.TrashRef) error {
	f.restored = append(f.restored, refs...)
	return nil
}
func (f *fakeTrash) Purge(ctx context.Context, projectID string, refs []pages.TrashRef, tok confirm.Token) error {
	scope := confirm.Scope{Kind: confirm.KindPurgeEntity, ProjectID: projectID}
	for _, r := range refs {
		scope.EntityTypes = append(scope.EntityTypes, r.Type)
		scope.EntityIDs = append(scope.EntityIDs, r.ID)
	}
	if err := f.confirm.Consume(ctx, tok, scope); err != nil {
		return err
	}
	f.purged = append(f.purged, refs...)
	f.purgeTok = tok
	return nil
}
func (f *fakeTrash) Empty(ctx context.Context, projectID string, tok confirm.Token) (int, error) {
	scope := confirm.Scope{Kind: confirm.KindEmptyTrash, ProjectID: projectID}
	if err := f.confirm.Consume(ctx, tok, scope); err != nil {
		return 0, err
	}
	f.emptied = len(f.items)
	f.emptyTok = tok
	return f.emptied, nil
}

type fakeReconcile struct {
	report  reconcile.Report
	applied []reconcile.Action
	result  reconcile.ApplyResult
	err     error
}

func (f *fakeReconcile) Scan(context.Context, string) (reconcile.Report, error) {
	return f.report, nil
}
func (f *fakeReconcile) Apply(_ context.Context, _ string, actions []reconcile.Action) (reconcile.ApplyResult, error) {
	f.applied = append(f.applied, actions...)
	return f.result, f.err
}

type fakeDiag struct {
	report diag.Report
}

func (f *fakeDiag) Run(context.Context, diag.Options) (diag.Report, error) {
	return f.report, nil
}

type fakeConfig struct{ cfg config.AppConfig }

func (f *fakeConfig) Load(context.Context) (config.AppConfig, error) { return f.cfg, nil }
func (f *fakeConfig) Save(_ context.Context, c config.AppConfig) error {
	f.cfg = c
	return nil
}

type fakeWorkspace struct {
	info     *workspace.Info
	plan     *workspace.MigratePlan
	planErr  error
	migrate  chan workspace.MigrateProgress
	migrated bool
}

func (f *fakeWorkspace) Info(context.Context) (*workspace.Info, error) { return f.info, nil }
func (f *fakeWorkspace) Probe(context.Context, string) (*workspace.Probe, error) {
	return &workspace.Probe{}, nil
}
func (f *fakeWorkspace) PlanMigration(context.Context, workspace.MigrateOptions) (*workspace.MigratePlan, error) {
	return f.plan, f.planErr
}
func (f *fakeWorkspace) Migrate(context.Context, string) (<-chan workspace.MigrateProgress, error) {
	f.migrated = true
	return f.migrate, nil
}
func (f *fakeWorkspace) CancelMigration(context.Context, string) error { return nil }

// ---- helpers -----------------------------------------------------------

func newApp(t *testing.T) *app.App {
	t.Helper()
	fixed := &clock.Fixed{T: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)}
	a := app.New(app.Options{WorkspaceName: "ResearchOS", Clock: fixed})
	a.SetProject(&project.Project{ID: "p1", Name: "Alpha"})
	return a
}

// tester renders a page headlessly with the light theme applied.
func tester(t *testing.T, page func(v *components.UI, r ports.Route), w, h int) *ui.Tester {
	t.Helper()
	return ui.NewTester(func(c *ui.Context) {
		c.SetTheme(mygo.Style(tokens.LightTheme()))
		ui.Column(c).Fill().Clip().Size(float32(w), float32(h)).Children(func() {
			page(components.New(c, tokens.LightTheme()), ports.Route{})
		})
	}, w, h)
}

// settle frames the page until the task finishes.
func settle(t *testing.T, tt *ui.Tester, a *app.App) {
	t.Helper()
	for range 200 {
		tt.Frame()
		done := true
		for _, task := range a.Tasks() {
			if !task.Finished {
				done = false
			}
		}
		if done {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("task did not settle")
}

// ---- wizard ------------------------------------------------------------

func TestWizardStates(t *testing.T) {
	cases := []struct {
		name     string
		state    pages.SetupState
		action   string
		guidance string
	}{
		{"empty", pages.SetupEmpty, "Initialize workspace", ""},
		{"existing", pages.SetupExisting, "Open workspace", ""},
		{"marker missing", pages.SetupMarkerMissing, "", "marker is gone"},
		{"marker corrupt", pages.SetupMarkerCorrupt, "", "cannot be read"},
		{"foreign", pages.SetupForeign, "", "never initializes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setup := &fakeSetup{
				root: "F:/Research",
				insp: pages.SetupInspection{State: c.state, Root: "F:/Research", Message: "state"},
			}
			a := newApp(t)
			w := &pages.Wizard{Services: &pages.Services{App: a, Setup: setup}}
			tt := tester(t, w.Page, 900, 700)
			tt.Click("Get started")
			tt.Frame()
			if err := tt.Click("Validate folder"); err != nil {
				t.Fatalf("validate: %v", err)
			}
			tt.Frame()
			if c.action != "" {
				if _, ok := tt.AccessByRole(ui.RoleButton, c.action); !ok {
					t.Fatalf("no %q action; texts %q", c.action, tt.Texts())
				}
			} else if _, ok := tt.AccessByRole(ui.RoleButton, "Initialize workspace"); ok {
				t.Error("a refused folder offers initialization")
			}
			if c.guidance != "" && !strings.Contains(strings.Join(tt.Texts(), " "), c.guidance) {
				t.Errorf("guidance %q missing; texts %q", c.guidance, tt.Texts())
			}
		})
	}
}

func TestWizardInitializesAndCompletes(t *testing.T) {
	setup := &fakeSetup{root: "F:/Research", insp: pages.SetupInspection{State: pages.SetupEmpty, Root: "F:/Research"}}
	a := newApp(t)
	w := &pages.Wizard{Services: &pages.Services{App: a, Setup: setup}}
	tt := tester(t, w.Page, 900, 700)
	tt.Click("Get started")
	tt.Frame()
	tt.Click("Validate folder")
	tt.Frame()
	tt.Click("Initialize workspace")
	settle(t, tt, a)
	tt.Frame()
	if !setup.initialized || setup.initPath != "F:/Research" {
		t.Fatalf("initialize called = %v with %q", setup.initialized, setup.initPath)
	}
	if !tt.HasText("Workspace ready") {
		t.Errorf("completion missing; texts %q", tt.Texts())
	}
}

func TestWizardShowsInitializationFailure(t *testing.T) {
	setup := &fakeSetup{root: "F:/Research", insp: pages.SetupInspection{State: pages.SetupEmpty, Root: "F:/Research"}, initErr: errors.New("disk full")}
	a := newApp(t)
	w := &pages.Wizard{Services: &pages.Services{App: a, Setup: setup}}
	tt := tester(t, w.Page, 900, 700)
	tt.Click("Get started")
	tt.Frame()
	tt.Click("Validate folder")
	tt.Frame()
	tt.Click("Initialize workspace")
	settle(t, tt, a)
	tt.Frame()
	if !tt.HasText("Setup failed") || !strings.Contains(strings.Join(tt.Texts(), " "), "disk full") {
		t.Fatalf("failure not shown; texts %q", tt.Texts())
	}
}

// ---- trash -------------------------------------------------------------

func newTrash(t *testing.T) (*pages.Trash, *fakeTrash, *ui.Tester, *app.App) {
	t.Helper()
	a := newApp(t)
	svc := confirm.New(confirm.Options{})
	tr := &fakeTrash{confirm: svc, items: []pages.TrashItem{
		{Type: "note", ID: "n1", Title: "Old note", DeletedAt: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)},
		{Type: "note", ID: "n2", Title: "Second note", DeletedAt: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC), FileMissing: true},
	}}
	p := &pages.Trash{Services: &pages.Services{App: a, Trash: tr, Confirm: svc}}
	tt := tester(t, p.Page, 1100, 800)
	settle(t, tt, a)
	tt.Frame()
	return p, tr, tt, a
}

func TestTrashListsAndRestores(t *testing.T) {
	_, tr, tt, a := newTrash(t)
	if !tt.HasText("Old note") || !tt.HasText("File missing") {
		t.Fatalf("trash rows missing; texts %q", tt.Texts())
	}
	tt.Click("Old note")
	tt.Frame()
	tt.Click("Restore")
	settle(t, tt, a)
	if len(tr.restored) != 1 || tr.restored[0].ID != "n1" {
		t.Fatalf("restored = %+v", tr.restored)
	}
}

func TestTrashPurgeNeedsConfirmation(t *testing.T) {
	_, tr, tt, a := newTrash(t)
	tt.Click("Old note")
	tt.Frame()
	if err := tt.Click("Delete permanently"); err != nil {
		t.Fatalf("open confirm: %v", err)
	}
	tt.Frame()
	if !tt.HasText("Permanently delete items?") {
		t.Fatalf("confirmation did not open; texts %q", tt.Texts())
	}
	// Canceling leaves the items alone: no token was issued.
	tt.Click("Cancel")
	tt.Frame()
	if len(tr.purged) != 0 {
		t.Fatalf("purge ran without confirmation: %+v", tr.purged)
	}

	tt.Click("Delete permanently")
	tt.Frame()
	tt.Click("Yes, delete permanently")
	settle(t, tt, a)
	if len(tr.purged) != 1 || tr.purged[0].ID != "n1" {
		t.Fatalf("purged = %+v", tr.purged)
	}
}

func TestTrashEmptyUsesEmptyScope(t *testing.T) {
	_, tr, tt, a := newTrash(t)
	tt.Click("Empty bin")
	tt.Frame()
	if err := tt.Click("Yes, empty the bin"); err != nil {
		t.Fatalf("confirm empty: %v", err)
	}
	settle(t, tt, a)
	if tr.emptied != 2 || tr.emptyTok == "" {
		t.Fatalf("emptied = %d, token = %q", tr.emptied, tr.emptyTok)
	}
}

func TestConfirmTokenGating(t *testing.T) {
	svc := confirm.New(confirm.Options{})
	scope := confirm.Scope{Kind: confirm.KindPurgeEntity, ProjectID: "p1", EntityTypes: []string{"note"}, EntityIDs: []string{"n1"}}
	tr := &fakeTrash{confirm: svc}
	if err := tr.Purge(context.Background(), "p1", []pages.TrashRef{{Type: "note", ID: "n1"}}, ""); !errors.Is(err, errs.ErrUserConfirmationRequired) {
		t.Fatalf("no token: %v", err)
	}
	tok, err := svc.Issue(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Purge(context.Background(), "p2", []pages.TrashRef{{Type: "note", ID: "n1"}}, tok); !errors.Is(err, errs.ErrConfirmScopeMismatch) {
		t.Fatalf("wrong scope: %v", err)
	}
	if err := tr.Purge(context.Background(), "p1", []pages.TrashRef{{Type: "note", ID: "n1"}}, tok); err != nil {
		t.Fatalf("right scope: %v", err)
	}
	if err := tr.Purge(context.Background(), "p1", []pages.TrashRef{{Type: "note", ID: "n1"}}, tok); !errors.Is(err, errs.ErrUserConfirmationRequired) {
		t.Fatalf("token reused: %v", err)
	}
}

// ---- consistency -------------------------------------------------------

func TestConsistencyAppliesAndShowsPartialFailure(t *testing.T) {
	a := newApp(t)
	rc := &fakeReconcile{
		report: reconcile.Report{
			GeneratedAt: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC),
			Orphans:     []reconcile.Orphan{{RelPath: "notes/a.md", Size: 10}},
			Missing:     []reconcile.Missing{{NoteID: "n1", FilePath: "notes/b.md", Title: "B"}},
			Modified:    []reconcile.Modified{{NoteID: "n2", FilePath: "notes/c.md"}},
		},
		result: reconcile.ApplyResult{Items: []reconcile.ActionResult{
			{Action: reconcile.Action{Kind: reconcile.ActionAdopt}, NoteID: "new"},
			{Action: reconcile.Action{Kind: reconcile.ActionAdopt}, Err: errors.New("permission denied")},
		}},
	}
	p := &pages.Consistency{Services: &pages.Services{App: a, Reconcile: rc}}
	tt := tester(t, p.Page, 1100, 900)
	settle(t, tt, a)
	tt.Frame()
	for _, want := range []string{"Orphans (1)", "Missing (1)", "Modified (1)", "notes/a.md", "B", "notes/c.md"} {
		if !tt.HasText(want) {
			t.Errorf("page lacks %q; texts %q", want, tt.Texts())
		}
	}
	tt.Click("Register")
	settle(t, tt, a)
	tt.Frame()
	if len(rc.applied) != 1 || rc.applied[0].RelPath != "notes/a.md" {
		t.Fatalf("applied = %+v", rc.applied)
	}
	if !strings.Contains(strings.Join(tt.Texts(), " "), "permission denied") {
		t.Errorf("partial failure not shown; texts %q", tt.Texts())
	}
}

// ---- diagnostics -------------------------------------------------------

func TestDiagnosticsRendersEverySectionAndCopies(t *testing.T) {
	a := newApp(t)
	rep := diag.Report{
		GeneratedAt: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC),
		Global:      diag.DBReport{Path: "global.db", SchemaVersion: 3, TargetVersion: 3, IntegrityOK: true, SizeBytes: 4096},
		Projects: []diag.ProjectReport{{
			ProjectID: "p1", NotesTotal: 12, NotesMissing: 1, BrokenRefs: 2, DirExists: true,
			DB: &diag.DBReport{Path: "p1/project.db", SchemaVersion: 2, TargetVersion: 3, IntegrityOK: false, IntegrityErr: "row 4"},
		}},
		Config:   diag.ConfigReport{Path: "config.json", Exists: true, WorkspaceRoot: "F:/R", RootReachable: true, ZoteroDataDir: "C:/Z"},
		Warnings: []string{"project p2 directory missing"},
	}
	p := &pages.Diagnostics{Services: &pages.Services{App: a, Diagnostics: &fakeDiag{report: rep}}}
	tt := tester(t, p.Page, 1200, 900)
	settle(t, tt, a)
	tt.Frame()
	for _, want := range []string{"Global database", "3 / 3", "Projects", "p1", "12", "Configuration", "C:/Z", "project p2 directory missing"} {
		if !tt.HasText(want) {
			t.Errorf("diagnostics lacks %q; texts %q", want, tt.Texts())
		}
	}
	tt.Click("Copy report")
	tt.Frame()
	clip := tt.Clipboard()
	for _, want := range []string{"ResearchOS diagnostic report", "schema 2 / 3", "integrity failed: row 4", "missing 1", "project p2 directory missing"} {
		if !strings.Contains(clip, want) {
			t.Errorf("copied report lacks %q:\n%s", want, clip)
		}
	}
}

// ---- settings ----------------------------------------------------------

func TestSettingsShowsWorkspaceGroups(t *testing.T) {
	a := newApp(t)
	cfg := &fakeConfig{cfg: config.AppConfig{WorkspaceRoot: "F:/Research", ZoteroDataDir: ""}}
	ws := &fakeWorkspace{info: &workspace.Info{Root: "F:/Research", ProjectCount: 4, ArchivedCount: 1, FreeBytes: 1 << 30}}
	p := &pages.Settings{Services: &pages.Services{
		App: a, Workspace: ws, Config: cfg,
		Scan: func(context.Context) (fworkspace.WorkspaceScan, error) {
			return fworkspace.WorkspaceScan{Marker: fworkspace.MarkerPresent}, nil
		},
	}}
	tt := tester(t, p.Page, 1100, 850)
	settle(t, tt, a)
	tt.Frame()
	for _, want := range []string{"Workspace", "Location", "F:/Research", "4 (1 archived)", "Marker file", "Present"} {
		if !tt.HasText(want) {
			t.Errorf("settings lacks %q; texts %q", want, tt.Texts())
		}
	}
	tt.Click("External Data")
	tt.Frame()
	if !tt.HasText("Zotero data directory") {
		t.Errorf("external group missing; texts %q", tt.Texts())
	}
	tt.Click("Storage & Maintenance")
	tt.Frame()
	for _, want := range []string{"Recycle Bin", "File Consistency", "Diagnostics"} {
		if !tt.HasText(want) {
			t.Errorf("maintenance lacks %q; texts %q", want, tt.Texts())
		}
	}
}

// ---- migration ---------------------------------------------------------

func TestMigrationPlansAndRuns(t *testing.T) {
	a := newApp(t)
	ch := make(chan workspace.MigrateProgress, 4)
	close(ch)
	ws := &fakeWorkspace{plan: &workspace.MigratePlan{ID: "plan1", SourceRoot: "F:/A", TargetRoot: "F:/B", Files: 10, Bytes: 1024}, migrate: ch}
	p := &pages.Migration{Services: &pages.Services{App: a, Workspace: ws, Setup: &fakeSetup{root: "F:/A"}}}
	tt := tester(t, p.Page, 1100, 850)
	// The target field starts from the current root; plan it.
	tt.Click("Plan migration")
	settle(t, tt, a)
	tt.Frame()
	if !tt.HasText("Plan") || !tt.HasText("F:/A → F:/B") {
		t.Fatalf("plan missing; texts %q", tt.Texts())
	}
	tt.Click("Start migration")
	settle(t, tt, a)
	tt.Frame()
	if !ws.migrated {
		t.Error("migrate was not called")
	}
	if !tt.HasText("Migration finished") {
		t.Errorf("completion missing; texts %q", tt.Texts())
	}
}

func TestExternalWarningShowsOnlyBrokenRefs(t *testing.T) {
	called := false
	view := func(c *ui.Context) {
		c.SetTheme(mygo.Style(tokens.LightTheme()))
		v := components.New(c, tokens.LightTheme())
		ui.Column(c).Fill().Children(func() {
			pages.ExternalWarning(v, externalrefs.Status{
				Ref:    externalrefs.Ref{ID: "r1", Path: "D:/labs/folding", Kind: externalrefs.KindDirectory},
				Exists: false,
				Reason: "directory is gone",
			}, func() { called = true })
		})
	}
	tt := ui.NewTester(view, 700, 300)
	if !tt.HasText("Referenced folder not found") || !tt.HasText("D:/labs/folding · directory is gone") {
		t.Fatalf("warning missing; texts %q", tt.Texts())
	}
	tt.Click("Relocate")
	if !called {
		t.Error("relocate action did not run")
	}

	healthy := func(c *ui.Context) {
		c.SetTheme(mygo.Style(tokens.LightTheme()))
		v := components.New(c, tokens.LightTheme())
		ui.Column(c).Fill().Children(func() {
			pages.ExternalWarning(v, externalrefs.Status{Ref: externalrefs.Ref{ID: "r1", Path: "D:/labs"}, Exists: true}, nil)
		})
	}
	ok := ui.NewTester(healthy, 700, 300)
	if ok.HasText("Referenced folder not found") {
		t.Error("a healthy reference rendered a warning")
	}
}

func TestConsistencyReattachUsesRelativePath(t *testing.T) {
	a := newApp(t)
	rc := &fakeReconcile{report: reconcile.Report{
		GeneratedAt: time.Now(),
		Missing:     []reconcile.Missing{{NoteID: "n1", FilePath: "notes/b.md", Title: "B"}},
	}}
	p := &pages.Consistency{Services: &pages.Services{App: a, Reconcile: rc}}
	tt := tester(t, p.Page, 1100, 800)
	settle(t, tt, a)
	tt.Frame()
	tt.Click("Reattach…")
	tt.Frame()
	if !tt.HasText("Reattach note file") {
		t.Fatalf("reattach dialog missing; texts %q", tt.Texts())
	}
	tt.Click("Reattach")
	settle(t, tt, a)
	tt.Frame()
	if len(rc.applied) != 1 || rc.applied[0].Kind != reconcile.ActionReattach || rc.applied[0].NewRelPath != "notes/b.md" {
		t.Fatalf("applied = %+v", rc.applied)
	}
}
