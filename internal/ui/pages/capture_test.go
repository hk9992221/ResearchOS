package pages_test

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"researchos/internal/foundation/config"
	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/diag"
	"researchos/internal/foundation/reconcile"
	fworkspace "researchos/internal/foundation/workspace"
	"researchos/internal/ports"
	"researchos/internal/project"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/pages"
	"researchos/internal/ui/shell"
	"researchos/internal/ui/theme"
	"researchos/internal/workspace"
)

// TestCaptureScreens renders the shell and every system page at both reference
// sizes in Light and Dark, saving PNGs for visual acceptance (AC-UI-08). It
// only runs when RESEARCHOS_CAPTURE=1: the CPU raster is a verification tool,
// never the production path.
func TestCaptureScreens(t *testing.T) {
	if os.Getenv("RESEARCHOS_CAPTURE") == "" {
		t.Skip("set RESEARCHOS_CAPTURE=1 to render the acceptance screenshots")
	}
	// A total text-stack failure draws nothing, silently: fail loudly instead
	// of writing blank goldens.
	if families := ui.FontFamilies(); len(families) == 0 {
		t.Fatal("no font families available: text would render blank")
	}

	dir := filepath.Join("..", "..", "..", "docs", "ui", "screenshots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	services := seedServices(t)
	pagesMap := shell.DefaultPages(context.Background(), services.App)
	for kind, page := range pages.System(services.Services) {
		pagesMap[kind] = page
	}
	ctx := context.Background()
	sh := shell.New(shell.Config{App: services.App, Theme: services.provider, Pages: pagesMap, Ctx: ctx})
	tt := ui.NewTester(sh.View, 1440, 900)

	for _, c := range []struct {
		name  string
		route ports.Route
		title string
	}{
		{"settings", ports.Route{Kind: ports.RouteSettings}, "Settings"},
		{"migration", ports.Route{Kind: app.RouteMigration}, "Move workspace"},
		{"trash", ports.Route{Kind: ports.RouteTrash}, "Recycle Bin"},
		{"consistency", ports.Route{Kind: app.RouteConsistency}, "File Consistency"},
		{"diagnostics", ports.Route{Kind: app.RouteDiagnostics}, "Diagnostics"},
	} {
		for _, mode := range []theme.Mode{theme.Light, theme.Dark} {
			if err := services.provider.SetMode(ctx, mode); err != nil {
				t.Fatal(err)
			}
			for _, size := range [][2]int{{1440, 900}, {1024, 700}} {
				tt.SetSize(size[0], size[1])
				services.App.SetProject(&projectSeed)
				services.App.Navigate(c.route)
				settle(t, tt, services.App)
				tt.Frame()
				img := tt.Image()
				if img.Bounds().Dx() != size[0] || img.Bounds().Dy() != size[1] {
					t.Fatalf("%s %dx%d: image is %v", c.name, size[0], size[1], img.Bounds())
				}
				if r, ok := tt.Find(c.title); !ok || r.W*r.H == 0 {
					t.Fatalf("%s: page title %q missing or clipped", c.name, c.title)
				}
				savePNG(t, filepath.Join(dir, captureName(c.name, size[0], size[1], mode)), img)
				// No control is covered by another: the top bar action stays
				// clickable on every page and size. Toggle it back so the
				// captures keep the same sidebar state.
				if err := tt.Click("Toggle sidebar"); err != nil {
					t.Fatalf("%s %dx%d: top bar control is occluded: %v", c.name, size[0], size[1], err)
				}
				if err := tt.Click("Toggle sidebar"); err != nil {
					t.Fatalf("%s %dx%d: sidebar toggle failed: %v", c.name, size[0], size[1], err)
				}
			}
		}
	}

	// The first-run wizard is the no-workspace screen, not a shell route.
	wizard := &pages.Wizard{Services: &pages.Services{App: services.App, Setup: services.Services.Setup}}
	wt := ui.NewTester(func(c *ui.Context) {
		mygo.Apply(c, services.provider)
		ui.Column(c).Fill().Clip().Children(func() {
			wizard.Page(components.New(c, services.provider.Theme()), ports.Route{})
		})
	}, 1440, 900)
	wt.Click("Get started")
	wt.Frame()
	wt.Click("Validate folder")
	wt.Frame()
	for _, mode := range []theme.Mode{theme.Light, theme.Dark} {
		if err := services.provider.SetMode(ctx, mode); err != nil {
			t.Fatal(err)
		}
		for _, size := range [][2]int{{1440, 900}, {1024, 700}} {
			wt.SetSize(size[0], size[1])
			wt.Frame()
			if r, ok := wt.Find("Folder check"); !ok || r.W*r.H == 0 {
				t.Fatalf("wizard review title missing at %dx%d", size[0], size[1])
			}
			savePNG(t, filepath.Join(dir, captureName("wizard", size[0], size[1], mode)), wt.Image())
		}
	}
}

func captureName(page string, w, h int, mode theme.Mode) string {
	return fmt.Sprintf("%s-%dx%d-%s.png", page, w, h, mode)
}

// savePNG writes one capture, naming it in the log for the review.
func savePNG(t *testing.T, file string, img *image.RGBA) {
	t.Helper()
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", file)
}

var projectSeed = project.Project{ID: "p1", Name: "Protein Folding", Summary: "A study of chaperone-assisted folding."}

// seedServices builds the shell's services with populated fakes, so the
// captures show real rows instead of empty states.
func seedServices(t *testing.T) *seed {
	t.Helper()
	a := newApp(t)
	a.SetProject(&projectSeed)
	p := theme.New()
	tr := &fakeTrash{confirm: confirm.New(confirm.Options{}), items: []pages.TrashItem{
		{Type: "note", ID: "n1", Title: "Batch effects in RNA-seq", DeletedAt: time.Date(2026, 11, 11, 15, 4, 0, 0, time.UTC)},
		{Type: "paper", ID: "pa1", Title: "AlphaFold 3 methods", DeletedAt: time.Date(2026, 11, 10, 11, 30, 0, 0, time.UTC), FileMissing: true},
		{Type: "idea", ID: "i1", Title: "Chaperone knockout screen", DeletedAt: time.Date(2026, 11, 9, 9, 10, 0, 0, time.UTC)},
		{Type: "task", ID: "t1", Title: "Review folding pipeline", DeletedAt: time.Date(2026, 11, 8, 17, 45, 0, 0, time.UTC)},
	}}
	rc := &fakeReconcile{report: reconcile.Report{
		GeneratedAt: time.Date(2026, 11, 13, 8, 45, 0, 0, time.UTC),
		Orphans: []reconcile.Orphan{
			{RelPath: "notes/2026-11-02-meeting.md", Size: 2048},
			{RelPath: "notes/scratch-ideas.md", Size: 512},
		},
		Missing:  []reconcile.Missing{{NoteID: "n7", FilePath: "notes/folding-log.md", Title: "Folding log"}},
		Modified: []reconcile.Modified{{NoteID: "n9", FilePath: "notes/analysis.md", DBHash: "a1b2", DiskHash: "c3d4"}},
	}}
	rep := diag.Report{
		GeneratedAt: time.Date(2026, 11, 13, 8, 46, 0, 0, time.UTC),
		Global:      diag.DBReport{Path: "global.db", SchemaVersion: 3, TargetVersion: 3, IntegrityOK: true, SizeBytes: 184320, WALBytes: 4096},
		Projects: []diag.ProjectReport{
			{ProjectID: "p1", DB: &diag.DBReport{Path: "projects/p1/project.db", SchemaVersion: 3, TargetVersion: 3, IntegrityOK: true, SizeBytes: 356352}, DirExists: true, NotesTotal: 128, NotesMissing: 1, BrokenRefs: 2},
			{ProjectID: "p2", DB: &diag.DBReport{Path: "projects/p2/project.db", SchemaVersion: 2, TargetVersion: 3, IntegrityOK: false, IntegrityErr: "row 12: index corrupt"}, DirExists: false, NotesTotal: 14, NotesMissing: 0, BrokenRefs: 0},
		},
		Config:   diag.ConfigReport{Path: "config.json", Exists: true, WorkspaceRoot: "F:/ResearchOS-Workspace", RootReachable: true, ZoteroDataDir: "C:/Users/ada/Zotero"},
		Warnings: []string{"project p2: directory is missing", "note n7: body file is missing"},
	}
	ws := &fakeWorkspace{
		info: &workspace.Info{Root: "F:/ResearchOS-Workspace", FreeBytes: 512 << 30, TotalBytes: 1 << 40, DBBytes: 540672, LibraryBytes: 2 << 30, ExportsBytes: 4 << 20, ProjectCount: 2, ArchivedCount: 0},
		plan: &workspace.MigratePlan{ID: "plan-1", SourceRoot: "F:/ResearchOS-Workspace", TargetRoot: "E:/ResearchOS-Workspace", Files: 4218, Bytes: 2<<30 + 540672},
	}
	cfg := &fakeConfig{cfg: config.AppConfig{SchemaVersion: 1, WorkspaceRoot: "F:/ResearchOS-Workspace", ZoteroDataDir: "C:/Users/ada/Zotero"}}
	setup := &fakeSetup{root: "F:/ResearchOS-Workspace", insp: pages.SetupInspection{
		Root: "F:/ResearchOS-Workspace", State: pages.SetupExisting, Message: "This is an existing ResearchOS workspace.",
	}}
	svc := pages.Services{
		App:         a,
		Workspace:   ws,
		Setup:       setup,
		Trash:       tr,
		Reconcile:   rc,
		Diagnostics: &fakeDiag{report: rep},
		Config:      cfg,
		Confirm:     tr.confirm,
		Scan: func(context.Context) (fworkspace.WorkspaceScan, error) {
			return fworkspace.WorkspaceScan{Marker: fworkspace.MarkerPresent}, nil
		},
	}
	return &seed{App: a, provider: p, Services: svc}
}

// seed bundles the capture fixture.
type seed struct {
	App      *app.App
	provider *theme.Provider
	Services pages.Services
}
