// Package pages implements the SPEC-01 system pages of ui_plan.md §16: the
// first-run wizard, workspace settings, migration progress, the recycle bin,
// the file consistency panel and diagnostics.
//
// Every page is a pure view bound to the shell's Page type: it renders from
// page state and calls the service layer through the narrow interfaces below.
// A page never opens SQLite and never joins workspace paths; tests bind fakes
// with the same signatures.
package pages

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"researchos/internal/foundation/config"
	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/diag"
	"researchos/internal/foundation/externalrefs"
	"researchos/internal/foundation/reconcile"
	foundationworkspace "researchos/internal/foundation/workspace"
	"researchos/internal/i18n"
	"researchos/internal/literature"
	"researchos/internal/notes"
	"researchos/internal/ui/app"
	"researchos/internal/ui/shell"
	"researchos/internal/workspace"

	"researchos/internal/ports"
)

// Services binds the pages to the service layer. The composition root supplies
// the real implementations; tests supply fakes.
type Services struct {
	App *app.App
	// Workspace is the application workspace service (Info, Probe,
	// PlanMigration, Migrate, CancelMigration). Nil before a workspace opens.
	Workspace WorkspaceAPI
	// Setup inspects candidate folders and opens or initializes workspaces.
	Setup SetupAPI
	Trash TrashAPI
	// Reconcile scans and repairs note files. Nil before a workspace opens.
	Reconcile   ReconcileAPI
	Diagnostics DiagnosticsAPI
	External    ExternalAPI
	Config      ConfigAPI
	// MCPStatus reports the in-process MCP server of SPEC-11 §5.8. It is the
	// only place a user can see whether the stdio bridge has something to talk
	// to; a nil function means the composition root never started one.
	MCPStatus func() MCPStatus
	// Literature binds the SPEC-04 Zotero and import services. Nil means the
	// module is not assembled, and the page says so instead of faking data.
	Literature LiteratureAPI
	// Notes binds the SPEC-05 note service. Nil means the module is not
	// assembled, and the page says so instead of rendering an empty list.
	Notes NotesAPI
	// Scan reports the marker and registry state of the open workspace.
	Scan func(ctx context.Context) (foundationworkspace.WorkspaceScan, error)
	// ChooseFolder opens the OS directory picker.
	ChooseFolder func() (string, error)
	// Confirm issues and consumes destructive-operation tokens.
	Confirm confirm.Service
	// OnMigrated reports a completed workspace migration to the composition
	// root, which updates the window's workspace name and configuration.
	OnMigrated func(ctx context.Context, newRoot string)
}

// WorkspaceAPI is the part of workspace.Service the pages use.
type WorkspaceAPI interface {
	Info(ctx context.Context) (*workspace.Info, error)
	Probe(ctx context.Context, root string) (*workspace.Probe, error)
	PlanMigration(ctx context.Context, opts workspace.MigrateOptions) (*workspace.MigratePlan, error)
	Migrate(ctx context.Context, planID string) (<-chan workspace.MigrateProgress, error)
	CancelMigration(ctx context.Context, planID string) error
}

// ConfigAPI is the part of config.Store the pages use.
type ConfigAPI interface {
	Load(ctx context.Context) (config.AppConfig, error)
	Save(ctx context.Context, c config.AppConfig) error
}

// ReconcileAPI is the consistency check and repair service.
type ReconcileAPI interface {
	Scan(ctx context.Context, projectID string) (reconcile.Report, error)
	Apply(ctx context.Context, projectID string, actions []reconcile.Action) (reconcile.ApplyResult, error)
}

// DiagnosticsAPI is the read-only self check.
type DiagnosticsAPI interface {
	Run(ctx context.Context, opts diag.Options) (diag.Report, error)
}

// ExternalAPI is the external-reference check and relocation service.
type ExternalAPI interface {
	CheckOwner(ctx context.Context, projectID string, ownerType, ownerID string) ([]externalrefs.Status, error)
	Relocate(ctx context.Context, req externalrefs.RelocateRequest) (externalrefs.RelocateResult, error)
}

// MCPStatus is the state of the in-process MCP server (SPEC-11 §5.8). Socket is
// the local endpoint the stdio bridge connects to; Error explains why the
// server is not running when it is not.
type MCPStatus struct {
	Running bool
	Socket  string
	Error   string
}

// LiteratureAPI is the part of SPEC-04 the Literature page uses: the Zotero
// connection, the item browser, and the preview-and-import pipeline. The
// composition root binds it to the services, always as ActorUser: the page is
// the user path, while MCP reaches the same methods with ActorMCP.
type LiteratureAPI interface {
	ZoteroStatus(ctx context.Context) (literature.ZoteroConnectionStatus, error)
	// MetadataSource reports which reader is in use (local API or SQLite) and
	// why it fell back, as SPEC-04 §5.2 requires the UI to show.
	MetadataSource(ctx context.Context) (literature.MetadataSource, string, error)
	Collections(ctx context.Context, libraryID string) ([]literature.ZoteroCollection, error)
	Items(ctx context.Context, libraryID, collectionKey string, opts literature.ListItemsOptions) (literature.ItemPage, error)
	Attachments(ctx context.Context, libraryID, itemKey string) ([]literature.ZoteroAttachment, error)
	Preview(ctx context.Context, sel literature.ImportSelection) (literature.ImportPreview, error)
	Import(ctx context.Context, sel literature.ImportSelection, progress literature.ProgressFunc) (literature.ImportResult, error)
	ProjectPapers(ctx context.Context, projectID string) ([]literature.ProjectPaper, error)
}

// NotesAPI is the part of SPEC-05 the Research Notes page uses: the note list,
// the editor's read and save calls, and the link resolution of the preview.
// The composition root binds it to the note service, always as ActorUser: the
// page is the user path, while MCP reaches the same methods with ActorMCP.
// Every call names the open project, because one adapter serves the workspace.
type NotesAPI interface {
	Create(ctx context.Context, projectID string, in notes.CreateNoteInput) (*notes.Note, error)
	Get(ctx context.Context, projectID, noteID string) (*notes.Note, error)
	List(ctx context.Context, projectID string, q notes.ListNotesQuery) (notes.NotePage, error)
	// LoadBody reads the Markdown without its frontmatter; Hash is the
	// whole-file token the next save carries as BaseHash.
	LoadBody(ctx context.Context, projectID, noteID string) (*notes.Body, error)
	SaveBody(ctx context.Context, projectID string, in notes.SaveBodyInput) (*notes.SaveResult, error)
	UpdateMeta(ctx context.Context, projectID string, in notes.UpdateMetaInput) (*notes.Note, error)
	SoftDelete(ctx context.Context, projectID, noteID string, actor ports.Actor) error
	ResolveTarget(ctx context.Context, projectID string, ref notes.EntityRef) (*notes.LinkTarget, error)
	// ImageBaseDir is the absolute directory Markdown image paths of a note
	// resolve from. The composition root owns paths; the page never joins
	// workspace paths itself.
	ImageBaseDir(projectID, noteID string) (string, error)
}

// TrashRef identifies one trash entry for restore or purge.
type TrashRef struct {
	Type string
	ID   string
}

// TrashItem is one recycle-bin row.
type TrashItem struct {
	Type      string
	ID        string
	Title     string
	DeletedAt time.Time
	// FileMissing marks a note whose body file is gone. It is false when the
	// state is unknown.
	FileMissing bool
}

// TrashAPI is the project-scoped recycle bin.
type TrashAPI interface {
	List(ctx context.Context, projectID string) ([]TrashItem, error)
	Restore(ctx context.Context, projectID string, refs []TrashRef) error
	Purge(ctx context.Context, projectID string, refs []TrashRef, token confirm.Token) error
	Empty(ctx context.Context, projectID string, token confirm.Token) (int, error)
}

// SetupState is what a candidate folder turned out to be (§16.1).
type SetupState int

const (
	// SetupEmpty is an empty or missing folder that can be initialized.
	SetupEmpty SetupState = iota
	// SetupExisting is a valid ResearchOS workspace.
	SetupExisting
	// SetupMarkerMissing is a workspace whose marker file is gone.
	SetupMarkerMissing
	// SetupMarkerCorrupt is a workspace whose marker file cannot be read.
	SetupMarkerCorrupt
	// SetupForeign is a non-empty folder that is not a workspace.
	SetupForeign
)

// SetupInspection is the result of inspecting a candidate folder.
type SetupInspection struct {
	Root    string
	State   SetupState
	Message string
}

// SetupAPI implements the wizard's workspace operations.
type SetupAPI interface {
	Inspect(ctx context.Context, root string) (SetupInspection, error)
	// Initialize creates a workspace in an empty folder and opens it.
	Initialize(ctx context.Context, root string) error
	// Open opens an existing workspace and installs the services.
	Open(ctx context.Context, root string) error
	// CurrentRoot is the open workspace root, or "".
	CurrentRoot() string
}

// System returns the system pages keyed by route.
func System(s Services) map[ports.RouteKind]shell.Page {
	settings := &Settings{Services: &s}
	migration := &Migration{Services: &s}
	trash := &Trash{Services: &s}
	consistency := &Consistency{Services: &s}
	diagnostics := &Diagnostics{Services: &s}
	return map[ports.RouteKind]shell.Page{
		ports.RouteSettings:  settings.Page,
		app.RouteMigration:   migration.Page,
		ports.RouteTrash:     trash.Page,
		app.RouteConsistency: consistency.Page,
		app.RouteDiagnostics: diagnostics.Page,
	}
}

// Research returns the module pages that operate on the open project. Today
// that is the SPEC-04 literature browser, the SPEC-05 note list and editor;
// the remaining modules replace their shell placeholders as they are built.
func Research(s Services) map[ports.RouteKind]shell.Page {
	literature := &Literature{Services: &s}
	// The list and the editor share the generation the editor bumps after a
	// write, so a returning list reloads instead of showing a stale row.
	shared := &notesShared{}
	notesList := &Notes{Services: &s, shared: shared}
	noteEditor := &NoteEditor{Services: &s, shared: shared}
	return map[ports.RouteKind]shell.Page{
		app.RouteLiterature: literature.Page,
		app.RouteNotes:      notesList.Page,
		ports.RouteNote:     noteEditor.Page,
	}
}

// projectID returns the open project id, or "".
func projectID(a *app.App) string {
	if p := a.Project(); p != nil {
		return p.ID
	}
	return ""
}

// statusTone is the tone of a healthy or broken state.
func brokenTone(broken bool) string {
	if broken {
		return "danger"
	}
	return "success"
}

// humanBytes formats a byte count for display.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return i18n.Tf("pages.common.size_bytes", map[string]string{"n": strconv.FormatInt(n, 10)})
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return i18n.Tf("pages.common.size_units", map[string]string{
		"value": fmt.Sprintf("%.1f", float64(n)/float64(div)),
		"unit":  string("KMGTP"[exp]),
	})
}
