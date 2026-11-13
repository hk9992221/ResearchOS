// Package ports declares the consumer-side contracts SPEC-03 needs from the
// foundation layer and from sibling modules (SPEC-03 §5.1, §5.2).
//
// The package depends on foundation packages only. Sibling modules adapt their
// own services to these interfaces, so this package never imports them and no
// import cycle can form (§5.2 "禁止在 SPEC-03 内 import 其内部包").
package ports

import (
	"context"
	"time"

	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/schema"
)

// Actor is the origin of an event. It is the foundation actor type (§5.1
// "实现直接使用 SPEC-01 events.Actor"), so values pass through to the event
// writer unchanged.
type Actor = events.Actor

// Actor constants mirror the foundation registry (§5.1).
const (
	ActorUser Actor = schema.ActorUser
	ActorMCP  Actor = schema.ActorMCP
	ActorAI   Actor = schema.ActorAI
)

// EntityRef identifies one entity of the current project.
type EntityRef struct {
	Type string
	ID   string
}

// RouteKind enumerates the navigable destinations UI components link to.
type RouteKind string

const (
	RouteProject      RouteKind = "project"
	RouteOverview     RouteKind = "overview"
	RouteResources    RouteKind = "resources"
	RouteActivity     RouteKind = "activity"
	RouteSettings     RouteKind = "settings"
	RouteNote         RouteKind = "note"
	RouteIdea         RouteKind = "idea"
	RouteExperiment   RouteKind = "experiment"
	RouteRun          RouteKind = "run"
	RouteTask         RouteKind = "task"
	RoutePaper        RouteKind = "paper"
	RouteWeeklyReport RouteKind = "weekly_report"
	RouteTrash        RouteKind = "trash"
	RouteExport       RouteKind = "export"
)

// Route is a UI navigation target. Params carries view hints only (for example
// view=blocked on a task route).
type Route struct {
	Kind      RouteKind
	ProjectID string
	EntityID  string
	Params    map[string]string
}

// PaperBrief is a read-only literature projection. Missing reports that the
// local PDF copy is gone.
type PaperBrief struct {
	ID       string
	Title    string
	Authors  string
	Year     int
	LinkedAt time.Time
	Missing  bool
}

// NoteBrief is a read-only note projection. Deleted marks a soft-deleted note.
type NoteBrief struct {
	ID        string
	Title     string
	Kind      string
	UpdatedAt time.Time
	Deleted   bool
}

// ExperimentBrief is a read-only experiment projection.
type ExperimentBrief struct {
	ID            string
	Title         string
	LastRunStatus string
	UpdatedAt     time.Time
	Deleted       bool
}

// TaskBrief is a read-only task projection.
type TaskBrief struct {
	ID       string
	Title    string
	Column   string
	Priority int
	Deadline *time.Time
	Blocked  bool
	// BlockedReason belongs to the unconfirmed requirements of 功能说明 §16.2
	// (blocking reason / unblock condition). Until that field is adopted it is
	// always the empty string and UI must not display it (§5.1).
	BlockedReason string
	UpdatedAt     time.Time
}

// TrashItem is one row of a project's recycle bin.
type TrashItem struct {
	EntityType string
	EntityID   string
	Title      string
	DeletedAt  time.Time
}

// TrashRef identifies one entity for a purge.
type TrashRef struct {
	EntityType string
	EntityID   string
}

// Locker serializes workspace-wide and per-project write operations (§5.2).
// SPEC-01 provides no lock, so SPEC-03 implements it in process memory: it
// never crosses the process boundary and never touches disk.
type Locker interface {
	// WithWorkspaceLock is the migration lock: exclusive and fail-fast while
	// another workspace operation holds it.
	WithWorkspaceLock(ctx context.Context, fn func() error) error
	// WithProjectWriteLock is the per-project write lock. It blocks until the
	// lock is free or ctx is done, and fails fast while a workspace (migration)
	// lock is held.
	WithProjectWriteLock(ctx context.Context, projectID string, fn func() error) error
}

// PaperIndex is the SPEC-04 literature port. Without it the export and delete
// previews fall back to project_papers directly.
type PaperIndex interface {
	// ListProjectPaperIDs returns the de-duplicated paper ids linked to the
	// project.
	ListProjectPaperIDs(ctx context.Context, projectID string) ([]string, error)
	// CountPaperRefs returns how many other projects (archived included) still
	// reference the paper. It only informs the impact preview; the real GC is
	// executed by SPEC-01.
	CountPaperRefs(ctx context.Context, paperID, excludeProjectID string) (int, error)
	ListProjectPapers(ctx context.Context, projectID string, limit int) ([]PaperBrief, error)
}

// TaskCompleter is the SPEC-08 Kanban port used by Overview quick actions.
type TaskCompleter interface {
	CompleteTask(ctx context.Context, projectID, taskID string, actor Actor) error
}

// NoteCreator is the SPEC-05 Notes port. The created note is always a
// scratchpad (kind=scratchpad).
type NoteCreator interface {
	CreateScratchpad(ctx context.Context, projectID, title, bodyMD string, actor Actor) (EntityRef, error)
}

// TrashService is the SPEC-01 recycle-bin port. Purge requires an in-app
// confirmation token.
type TrashService interface {
	List(ctx context.Context, projectID string) ([]TrashItem, error)
	Restore(ctx context.Context, projectID, entityType, entityID string, actor Actor) error
	Purge(ctx context.Context, projectID string, items []TrashRef, token confirm.Token) error
}
