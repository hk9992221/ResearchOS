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
