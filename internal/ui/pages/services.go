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
	"errors"
	"sync"
	"time"

	mg "github.com/egoist/mygo"

	"researchos/internal/ai/model"
	"researchos/internal/ai/provider"
	"researchos/internal/ai/role"
	"researchos/internal/foundation/config"
	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/diag"
	"researchos/internal/foundation/externalrefs"
	"researchos/internal/foundation/reconcile"
	foundationworkspace "researchos/internal/foundation/workspace"
	"researchos/internal/literature"
	"researchos/internal/mcp"
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
	// MCP binds the SPEC-11 server lifecycle, settings and client registry.
	// Nil means no workspace is open; the MCP tab says so instead of faking a
	// configuration.
	MCP MCPAPI
	// AI binds the SPEC-02 provider/model/role stack. Nil means the stack was
	// not built, and the AI settings tab says so instead of faking a
	// configuration; Available reports the same fact behind a non-nil value.
	AI AIAPI
	// Literature binds the SPEC-04 Zotero and import services. Nil means the
	// module is not assembled, and the page says so instead of faking data.
	Literature LiteratureAPI
	// Paper binds the in-app paper reader, its Paper Note and the research
	// notes that link it. Nil means the module is not assembled, and the page
	// says so instead of rendering an empty paper.
	Paper PaperAPI
	// Window returns the native window the shell draws in, or nil before the
	// window exists (the wizard, tests). The paper page embeds its PDF reader
	// in it; a nil window makes the page show its honest unavailable state.
	Window func() *mg.Window
	// Notes binds the SPEC-05 note service. Nil means the module is not
	// assembled, and the page says so instead of rendering an empty list.
	Notes NotesAPI
	// Experiments binds the SPEC-07 experiment service. Nil means the module
	// is not assembled, and the pages say so instead of faking data.
	Experiments ExperimentsAPI
	// Kanban binds the SPEC-08 task board. Nil means the module is not
	// assembled, and the page says so instead of rendering an empty board.
	Kanban KanbanAPI
	// Weekly binds the SPEC-09 weekly review service. Nil means the module is
	// not assembled, and the page says so instead of faking history.
	Weekly WeeklyAPI
	// Ideas binds the SPEC-06 Idea, evidence and history service. Nil means
	// the module is not assembled, and the page says so instead of faking an
	// empty list.
	Ideas IdeasAPI
	// NoteFlush is the note editor's synchronous flush. The composition
	// root allocates it, pages.Research installs the editor, and the window's
	// close callback runs it before the process exits (SPEC-05 §5.7).
	NoteFlush *NoteFlush
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

// MCPStatus is the state of the in-process MCP server (SPEC-11 §5.8). Socket
// is the local endpoint the stdio bridge connects to; Error explains why the
// server is not running when it is not. The remaining fields are the settings
// snapshot the MCP tab and the diagnostics card show.
type MCPStatus struct {
	Running       bool
	Socket        string
	Error         string
	Enabled       bool
	AccessMode    string
	HTTPEnabled   bool
	HTTPAddr      string
	Endpoint      string
	Version       string
	Protocols     []string
	Modules       []string
	ToolCount     int
	WorkspaceRoot string
}

// MCPSettingsPatch is a partial update of the mcp.* settings. A nil field is
// left unchanged; the MCP tab sends one card's fields at a time.
type MCPSettingsPatch struct {
	Enabled       *bool
	HTTPEnabled   *bool
	HTTPAddr      *string
	AccessMode    *string
	RetentionDays *int
}

// ErrMCPUnavailable reports that the MCP runtime was not assembled for this
// workspace. The MCP page shows its unavailable state instead of faking data.
var ErrMCPUnavailable = errors.New("pages: MCP service unavailable")

// ErrMCPClosed reports a call from a page whose workspace has closed; the
// manager bound to that workspace refuses instead of touching the next one.
var ErrMCPClosed = errors.New("pages: workspace closed")

// MCPToolInfo is one registered tool for the read-only catalog.
type MCPToolInfo struct {
	Name        string
	Module      string
	Category    string
	Title       string
	Description string
	ReadOnly    bool
	Destructive bool
}

// MCPAPI is the MCP lifecycle, settings and client registry the Settings page
// uses. It is bound to one workspace; a stale page after a workspace switch
// gets an explicit error rather than the next workspace's server.
type MCPAPI interface {
	GetMCPSettings(ctx context.Context) (mcp.SettingsValues, error)
	SaveMCPSettings(ctx context.Context, patch MCPSettingsPatch) (mcp.SettingsValues, error)
	Status() MCPStatus
	Tools() []MCPToolInfo
	Clients(ctx context.Context) ([]mcp.Client, error)
	CreateClient(ctx context.Context, name string) (mcp.Client, string, error)
	RotateClient(ctx context.Context, id string) (string, error)
	RevokeClient(ctx context.Context, id string) error
	SetClientEnabled(ctx context.Context, id string, enabled bool) error
	BridgeCommand() (string, error)
}

// AIAPI is the part of SPEC-02 the AI settings tab uses. ProviderView and the
// model/role views never carry key material or api_key_ref; the page never
// asks for either.
//
// Available reports whether the stack was built; ChatReady is the same local
// chat_main probe the modules use. Methods return the backend's typed errors
// unchanged so the page can map protocol conflicts and in-use refusals to
// readable messages.
type AIAPI interface {
	Available() bool
	ChatReady() bool

	ListProviders(ctx context.Context) ([]provider.ProviderView, error)
	CreateProvider(ctx context.Context, in provider.CreateProviderInput) (provider.ProviderView, error)
	UpdateProvider(ctx context.Context, id string, in provider.UpdateProviderInput) (provider.ProviderView, error)
	DeleteProvider(ctx context.Context, id string) error
	TestProvider(ctx context.Context, id string) (provider.TestResult, error)

	ListModels(ctx context.Context, q model.ModelQuery) ([]model.ModelInfo, error)
	SyncModels(ctx context.Context, providerID string) (model.ModelSyncResult, error)
	AddManualModel(ctx context.Context, providerID, modelID, displayName string) error
	RemoveManualModel(ctx context.Context, providerID, modelID string) error

	ListRoles(ctx context.Context) ([]role.RoleView, error)
	AssignRole(ctx context.Context, r role.Role, providerID, modelID string) (role.AssignResult, error)
	ClearRole(ctx context.Context, r role.Role) error
}

// LiteratureAPI is the part of SPEC-04 the Literature page uses: the item
// browser and the preview-and-import pipeline. The composition root binds it to
// the services, always as ActorUser: the page is the user path, while MCP
// reaches the same methods with ActorMCP.
type LiteratureAPI interface {
	Collections(ctx context.Context, libraryID string) ([]literature.ZoteroCollection, error)
	Items(ctx context.Context, libraryID, collectionKey string, opts literature.ListItemsOptions) (literature.ItemPage, error)
	Attachments(ctx context.Context, libraryID, itemKey string) ([]literature.ZoteroAttachment, error)
	Preview(ctx context.Context, sel literature.ImportSelection) (literature.ImportPreview, error)
	Import(ctx context.Context, sel literature.ImportSelection, progress literature.ProgressFunc) (literature.ImportResult, error)
	ProjectPapers(ctx context.Context, projectID string) ([]literature.ProjectPaper, error)
	// Unlink detaches one paper from the project. The global copy, the PDF
	// and the Paper Notes stay: the page says so before it asks (SPEC-04
	// §5.5), and reports the detached notes after.
	Unlink(ctx context.Context, projectID, paperID string, actor ports.Actor) (literature.UnlinkResult, error)
}

// PaperRelatedNote is one note whose Markdown body links the paper.
type PaperRelatedNote struct {
	ID        string
	Title     string
	UpdatedAt time.Time
}

// ErrPaperCopyMissing reports that the project's PDF copy is gone. The page
// shows it as a state of the reader card, not as an exception.
var ErrPaperCopyMissing = errors.New("pages: the paper PDF copy is missing")

// PaperAPI is the part of SPEC-04/05 the paper page uses: the embedded PDF
// reader, the external openers, the Paper Note fields and the research notes
// that link the paper. The composition root binds it always as ActorUser.
type PaperAPI interface {
	Paper(ctx context.Context, paperID string) (literature.Paper, error)
	// PaperFileURL resolves the imported copy to the file URL the embedded
	// web view shows.
	PaperFileURL(ctx context.Context, paperID string) (string, error)
	// OpenPaper launches the application the workspace setting selects
	// (system default or Zotero).
	OpenPaper(ctx context.Context, paperID string) error
	// OpenPaperWith lets the user pick another application for the imported
	// copy: the system "Open with" dialog where the platform has one.
	OpenPaperWith(ctx context.Context, paperID string) error

	PaperNote(ctx context.Context, projectID, paperID string) (*literature.PaperNote, error)
	CreatePaperNote(ctx context.Context, projectID, paperID string) (literature.PaperNote, error)
	UpdatePaperNote(ctx context.Context, noteID string, patch literature.PaperNotePatch, expectedUpdatedAt time.Time) (literature.PaperNote, error)
	SavePaperNoteBody(ctx context.Context, noteID, bodyMD, baseHash string) (literature.PaperNoteSaveResult, error)

	// RelatedNotes lists the project notes whose Markdown body links the
	// paper with ros://paper/<id>.
	RelatedNotes(ctx context.Context, projectID, paperID string) ([]PaperRelatedNote, error)
	// CreateLinkedNote creates a research note whose body links the paper.
	CreateLinkedNote(ctx context.Context, projectID, paperID, title string) (*notes.Note, error)
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

// NoteFlush carries the note editor's close-time flush between the page and
// the composition root: a save started through the shell's task machinery
// would not finish before the process exits, so the window's close callback
// calls it on the main thread instead. It holds one callback, set when the
// pages are built and nil-safe while it is unset.
type NoteFlush struct {
	mu sync.Mutex
	fn func()
}

// Set installs the editor's flush.
func (f *NoteFlush) Set(fn func()) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.fn = fn
	f.mu.Unlock()
}

// Run runs the installed flush, if any.
func (f *NoteFlush) Run() {
	if f == nil {
		return
	}
	f.mu.Lock()
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		fn()
	}
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
	// The literature list and the paper page share the generation the paper
	// page bumps after it writes a Paper Note or links a research note, so the
	// list's note badge is current when the user comes back.
	litShared := &literatureShared{}
	literature := &Literature{Services: &s, shared: litShared}
	paperPage := &PaperPage{Services: &s, shared: litShared}
	// The list and the editor share the generation the editor bumps after a
	// write, so a returning list reloads instead of showing a stale row.
	shared := &notesShared{}
	notesList := &Notes{Services: &s, shared: shared}
	noteEditor := &NoteEditor{Services: &s, shared: shared}
	s.NoteFlush.Set(noteEditor.Flush)
	kanbanPage := &Kanban{Services: &s}
	// The experiment list and the detail share the generation the detail
	// bumps after a write, so a returning list reloads its row.
	experimentsPages := ExperimentsPages(&s)
	// The weekly report list and detail share a generation counter for the
	// same reason as the notes pair.
	weeklyList := &Weekly{Services: &s, shared: &weeklyShared{}}
	weeklyReport := &WeeklyReportView{Services: &s, shared: weeklyList.shared}
	// The idea list and the detail share the generation the detail bumps
	// after a write, so a returning list reloads its row.
	ideasShared := &ideasShared{}
	ideasList := &Ideas{Services: &s, shared: ideasShared}
	ideaPage := &IdeaPage{Services: &s, shared: ideasShared}
	return map[ports.RouteKind]shell.Page{
		app.RouteLiterature: literature.Page,
		ports.RoutePaper:    paperPage.Page,
		app.RouteNotes:      notesList.Page,
		ports.RouteNote:     noteEditor.Page,

		app.RouteKanban: kanbanPage.Page,
		ports.RouteTask: kanbanPage.TaskPage,

		app.RouteExperiments:  experimentsPages[app.RouteExperiments],
		ports.RouteExperiment: experimentsPages[ports.RouteExperiment],
		ports.RouteRun:        experimentsPages[ports.RouteRun],

		app.RouteWeeklyReview:   weeklyList.Page,
		ports.RouteWeeklyReport: weeklyReport.Page,

		app.RouteIdeas:  ideasList.Page,
		ports.RouteIdea: ideaPage.Page,
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
