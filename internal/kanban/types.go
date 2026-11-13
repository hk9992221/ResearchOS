// Package kanban is the SPEC-08 research-task board: tasks, custom status
// columns, subtasks, checklists, labels, archiving, entity links and the
// read-only Board/List/Blocked projections.
//
// It is UI- and MCP-agnostic: the desktop UI, the SPEC-03 Overview port and
// the SPEC-11 MCP server call exactly these services, so validation and
// Activity events live in one place (SPEC-08 §1.1). All multi-step writes run
// through workspace.ProjectHandle.InTx; events go through foundation/events,
// soft deletion through foundation/trash and relations through
// foundation/relations only. The module never writes activities directly and
// never touches Idea/Experiment/Run lifecycle state (SPEC-08 §3.3).
//
// A service instance is bound to a single project's *workspace.ProjectHandle
// (SPEC-08 §5); the project_id → handle resolution for MCP happens in the
// adapter layer, not here.
package kanban

import (
	"context"
	"errors"
	"time"

	"researchos/internal/foundation/events"
	"researchos/internal/foundation/relations"
	"researchos/internal/foundation/schema"
)

// Priority is tasks.priority. none is stored as SQL NULL and normalized back
// to none on read (SPEC-08 §4.3).
type Priority string

// The four registered priorities (TASK-04).
const (
	PriorityHigh   Priority = "high"
	PriorityMedium Priority = "medium"
	PriorityLow    Priority = "low"
	PriorityNone   Priority = "none"
)

func (p Priority) valid() bool {
	switch p {
	case PriorityHigh, PriorityMedium, PriorityLow, PriorityNone, "":
		return true
	}
	return false
}

// stored maps a priority to its column value; none (and the empty input) is
// NULL in the database.
func (p Priority) stored() any {
	if p == PriorityNone || p == "" {
		return nil
	}
	return string(p)
}

// Version is the optimistic-lock token: clock.Format(updated_at), the UTC
// RFC3339 millisecond string (SPEC-08 §5.1, O-2/N5).
type Version = string

// Task is one tasks row with the SPEC-08 extension columns (M-02~M-05).
type Task struct {
	ID            string
	Title         string
	Column        string // tasks."column": the task_columns.name key
	ParentTaskID  *string
	DescriptionMD string
	Priority      Priority
	Deadline      *string // "YYYY-MM-DD", local date, no time zone (TASK-05)
	Blocked       bool
	Labels        []string
	WeekPlan      *string // "YYYY-Www" ISO week (KBN-P02 data layer only)
	SortOrder     int
	CompletedAt   *time.Time
	ArchivedAt    *time.Time
	DeletedAt     *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Version returns the task's concurrency token.
func (t Task) Version() Version { return timestampToken(t.UpdatedAt) }

// TaskSummary is the Board / List / archive / back-link projection that
// carries the card counters instead of the full detail (SPEC-08 §5.1).
type TaskSummary struct {
	ID             string
	Title          string
	Column         string
	ParentTaskID   *string
	Priority       Priority
	Deadline       *string
	Blocked        bool
	Labels         []string
	SortOrder      int
	SubtaskDone    int
	SubtaskTotal   int
	ChecklistDone  int
	ChecklistTotal int
	LinkCount      int
	CompletedAt    *time.Time
	ArchivedAt     *time.Time
	UpdatedAt      time.Time
}

// TaskDetail is Get's full projection: the row plus checklist, live subtasks,
// links and the column definition.
type TaskDetail struct {
	Task
	Checklist []ChecklistItem
	Subtasks  []TaskSummary
	Links     []EntityLink
	Column    Column
}

// Column is one task_columns row. Name doubles as the tasks."column" key.
type Column struct {
	Name     string
	Kind     string // backlog|todo|in_progress|review|done|custom
	IsDone   bool
	Position int
}

// ChecklistItem is one task_checklist_items row (TASK-07).
type ChecklistItem struct {
	ID        string
	TaskID    string
	Text      string
	Done      bool
	SortOrder int
}

// EntityLink is a task → research-entity relation with the resolved target
// title and lifecycle state (TASK-11).
type EntityLink struct {
	RelationID  string
	Relation    schema.Relation
	DstType     schema.EntityType
	DstID       string
	DstTitle    string
	TargetState relations.TargetState
}

// Page is one cursor-paginated result (SPEC-08 §5.2 rule 11).
type Page[T any] struct {
	Items      []T
	NextCursor string // empty = no more pages
}

// LabelCount is one aggregated label.
type LabelCount struct {
	Label string
	Count int
}

// TaskSort selects the display ordering (SPEC-08 §5.2 rule 10).
type TaskSort string

// The four orderings. position_asc is the Board default, updated_desc the
// List default.
const (
	SortPositionAsc  TaskSort = "position_asc"
	SortPriorityDesc TaskSort = "priority_desc"
	SortDeadlineAsc  TaskSort = "deadline_asc"
	SortUpdatedDesc  TaskSort = "updated_desc"
)

func (s TaskSort) valid() bool {
	switch s {
	case SortPositionAsc, SortPriorityDesc, SortDeadlineAsc, SortUpdatedDesc:
		return true
	}
	return false
}

// Error sentinels (SPEC-08 §5.1). UI and MCP map them to readable messages.
var (
	ErrTaskNotFound        = errors.New("kanban: task not found")
	ErrColumnNotFound      = errors.New("kanban: column not found")
	ErrProjectArchived     = errors.New("kanban: project archived (read-only)")
	ErrStaleRevision       = errors.New("kanban: stale updated_at token")
	ErrInvalidParent       = errors.New("kanban: parent task invalid (deleted, archived or cross-project)")
	ErrSubtaskOnSubtask    = errors.New("kanban: subtasks cannot nest")
	ErrColumnNotEmpty      = errors.New("kanban: column has tasks, migration target required")
	ErrLastDoneColumn      = errors.New("kanban: at least one done column required")
	ErrLastColumn          = errors.New("kanban: cannot delete the last column")
	ErrDuplicateColumnName = errors.New("kanban: duplicate column name")
	ErrTaskLimitExceeded   = errors.New("kanban: task limit exceeded")
	ErrTaskArchived        = errors.New("kanban: task archived")
	ErrInvalidInput        = errors.New("kanban: invalid input")
)

// CreateInput is TaskService.Create's argument (TASK-01/06). A zero Column
// selects the non-terminal column with the smallest position.
type CreateInput struct {
	Title         string
	Column        string
	ParentTaskID  *string
	DescriptionMD string
	Priority      Priority
	Deadline      *string
	Labels        []string
	WeekPlan      *string
}

// UpdateInput is TaskService.Update's argument (TASK-02). Only the non-nil
// fields are written. Deadline uses the double pointer convention: nil means
// "do not change", a pointer to nil means "clear".
type UpdateInput struct {
	ID                string
	ExpectedUpdatedAt *Version
	Title             *string
	DescriptionMD     *string
	Priority          *Priority
	Deadline          **string
	Labels            *[]string
	Blocked           *bool
	Column            *string // change delegates to Move (append to target end)
}

// MoveInput is TaskService.Move's argument (TASK-09). BeforeID and AfterID
// are anchors inside the target column; at most one may be set, both empty
// appends to the end.
type MoveInput struct {
	ID       string
	Column   string
	BeforeID *string
	AfterID  *string
}

// Query is the List / ListArchived filter set (SPEC-08 §5.2). Defaults:
// live-and-unarchived rows only, Sort position_asc, Limit 50 capped at 200.
type Query struct {
	Column          *string
	ParentID        *string // non-nil lists subtasks
	OnlyTopLevel    bool
	Priority        []Priority
	Labels          []string // AND semantics
	DeadlineFrom    *string  // inclusive
	DeadlineTo      *string  // inclusive
	Blocked         *bool
	WeekPlan        *string // data-layer filter; UI use waits for KBN-P02
	Text            string  // title/description containment
	IncludeArchived bool
	IncludeDeleted  bool
	Sort            TaskSort // empty = position_asc
	Limit           int      // 0 = 50, capped at 200
	Cursor          string
}

// CreateColumnInput is ColumnService.Create's argument (SPEC-08 §5.4).
type CreateColumnInput struct {
	Name   string
	Kind   string // empty = custom
	IsDone bool
	After  *string // nil = append to the far right
}

// BoardQuery narrows the Board projection (SPEC-08 §5.7).
type BoardQuery struct {
	Priority []Priority
	Labels   []string
	Blocked  *bool
	Text     string
}

// BoardColumn is one board column with its top-level live cards.
type BoardColumn struct {
	Column Column
	Tasks  []TaskSummary
}

// BoardView is the whole board ordered by task_columns.position.
type BoardView struct {
	Columns []BoardColumn
}

// TaskService is the single entry point for the UI, the SPEC-03 port and MCP
// (SPEC-08 §5.2). Every write method rejects an archived project with
// ErrProjectArchived and a missing/soft-deleted task with ErrTaskNotFound.
type TaskService interface {
	// Create writes the row and entity.created in one transaction;
	// sort_order appends to the target column+parent domain.
	Create(ctx context.Context, actor events.Actor, in CreateInput) (Task, error)

	// Get loads the full detail, including soft-deleted and archived tasks.
	Get(ctx context.Context, id string) (TaskDetail, error)

	// List pages live tasks by default; IncludeArchived/IncludeDeleted widen
	// the scope. Default Limit 50, cap 200.
	List(ctx context.Context, q Query) (Page[TaskSummary], error)

	// Update applies a partial edit. A non-nil ExpectedUpdatedAt is checked
	// against the stored updated_at; mismatch → ErrStaleRevision. A column
	// change delegates to Move (append to target end) and emits
	// status.changed; a blocked toggle emits status.changed; plain field
	// edits emit nothing and only advance updated_at.
	Update(ctx context.Context, actor events.Actor, in UpdateInput) (Task, error)

	// Move is the only entry for drag and menu repositioning. Anchors are
	// at most one of BeforeID/AfterID inside the target column. A column
	// change maintains completed_at and emits status.changed; same-column
	// reordering emits nothing. No concurrency token is required.
	Move(ctx context.Context, actor events.Actor, in MoveInput) (Task, error)

	// SetWeekPlan writes or clears week_plan and emits nothing (SPEC-09
	// assumption, KBN-P02 data layer).
	SetWeekPlan(ctx context.Context, actor events.Actor, id string, week *string, expected *Version) (Task, error)

	// Complete appends the task to the end of the leftmost is_done column;
	// already-terminal tasks return unchanged without an event.
	Complete(ctx context.Context, actor events.Actor, id string) (Task, error)

	// Archive/Unarchive set or clear archived_at with status.changed (O-3).
	// An archived task rejects every write except Unarchive, SoftDelete and
	// Restore with ErrTaskArchived; archiving keeps column, sort_order and
	// completed_at (TASK-10).
	Archive(ctx context.Context, actor events.Actor, id string) (Task, error)
	Unarchive(ctx context.Context, actor events.Actor, id string) (Task, error)
	// ListArchived returns archived, non-deleted tasks; q.IncludeDeleted is
	// ignored.
	ListArchived(ctx context.Context, q Query) (Page[TaskSummary], error)

	// SoftDelete moves the task into the trash through trash.SoftDelete;
	// deleting a parent cascades to every live subtask (each its own
	// entity.deleted). Archived tasks may be deleted.
	SoftDelete(ctx context.Context, actor events.Actor, id string) error
	// Restore brings the task (and, for a parent, its deleted subtasks) back
	// through trash.Restore; restoring a subtask whose parent is still in
	// the trash → ErrInvalidParent.
	Restore(ctx context.Context, actor events.Actor, id string) (Task, error)
}

// ColumnService manages the custom status columns (TASK-03, SPEC-08 §5.4).
// Column management never writes Activity; every write still enforces the
// archived-project gate.
type ColumnService interface {
	List(ctx context.Context) ([]Column, error)
	// Create places the new column after After (or at the far right) and
	// enforces name uniqueness.
	Create(ctx context.Context, actor events.Actor, in CreateColumnInput) (Column, error)
	// Rename cascades tasks."column" in the same transaction.
	Rename(ctx context.Context, actor events.Actor, oldName, newName string) (Column, error)
	// Move re-sequences positions only (toPosition is the 0-based target
	// index among the remaining columns).
	Move(ctx context.Context, actor events.Actor, name string, toPosition int) ([]Column, error)
	// SetDone marks/unmarks a terminal column; clearing the last one →
	// ErrLastDoneColumn. Existing completed_at values survive unmarking.
	SetDone(ctx context.Context, actor events.Actor, name string, done bool) (Column, error)
	// Delete requires a migration target when the column still holds
	// undeleted tasks; the cascade appends them to the target in their
	// original order without per-task events.
	Delete(ctx context.Context, actor events.Actor, name, migrateTo string) error
}

// ChecklistService manages the lightweight step list (TASK-07). Operations
// never emit events and never touch the parent task's updated_at.
type ChecklistService interface {
	Add(ctx context.Context, actor events.Actor, taskID, text string, beforeID *string) (ChecklistItem, error)
	Update(ctx context.Context, actor events.Actor, itemID, text string) (ChecklistItem, error)
	SetDone(ctx context.Context, actor events.Actor, itemID string, done bool) (ChecklistItem, error)
	Remove(ctx context.Context, actor events.Actor, itemID string) error // physical delete
	Reorder(ctx context.Context, actor events.Actor, taskID string, orderedIDs []string) error
}

// LabelService is the label convenience surface (TASK-08, SPEC-08 §5.3).
type LabelService interface {
	// List aggregates labels from live tasks.labels; there is no global tag
	// table.
	List(ctx context.Context) ([]LabelCount, error)
	Add(ctx context.Context, actor events.Actor, taskID, label string, expected *Version) (Task, error)
	Remove(ctx context.Context, actor events.Actor, taskID, label string, expected *Version) (Task, error)
}

// LinkService wraps foundation/relations for TASK-11. Events are written by
// the relations service; links of a soft-deleted target survive and resolve
// to TargetSoftDeleted.
type LinkService interface {
	Add(ctx context.Context, taskID string, rel schema.Relation, dstType schema.EntityType, dstID string, actor events.Actor) (string, error)
	Remove(ctx context.Context, taskID string, rel schema.Relation, dstType schema.EntityType, dstID string, actor events.Actor) error
	// List returns the task's outbound links with titles and target states;
	// TargetMissing rows are dirty data, dropped with a warning.
	List(ctx context.Context, taskID string) ([]EntityLink, error)
	// ListTasksByTarget is the back-link for the other entity's detail view.
	ListTasksByTarget(ctx context.Context, dstType schema.EntityType, dstID string) ([]TaskSummary, error)
}

// ViewService is the read-only aggregation layer (SPEC-08 §5.7). Board and
// Blocked always exclude archived and deleted tasks; card counters come from
// one aggregate query, no N+1.
type ViewService interface {
	Board(ctx context.Context, q BoardQuery) (BoardView, error)
	Blocked(ctx context.Context) (Page[TaskSummary], error)
}
