package kanban

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/projects"
	"researchos/internal/foundation/relations"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/trash"
	"researchos/internal/foundation/workspace"
)

// Limits from SPEC-08 §5.2, §4.3 and §5.5.
const (
	maxTitleChars        = 200
	maxDescriptionRunes  = 100_000
	maxLabelRunes        = 32
	maxLabelsPerTask     = 20
	maxSubtasksPerParent = 100
	maxActiveTasks       = 10_000
	maxChecklistPerTask  = 100
	maxChecklistRunes    = 500

	sortStep = 1024 // SPEC-08 §5.2 rule 3, §4.2 position/sort_order step

	listDefaultLimit = 50
	listMaxLimit     = 200 // SPEC-08 §5.2 rule 1 (O-11)
)

// defaultColumnName / defaultColumnKind seed the board (SPEC-08 §4.3).
var defaultColumns = []struct {
	name   string
	kind   string
	isDone bool
}{
	{"Backlog", "backlog", false},
	{"Todo", "todo", false},
	{"In Progress", "in_progress", false},
	{"Review", "review", false},
	{"Done", "done", true},
}

// Options wires the module to its SPEC-01 collaborators. Trash and Relations
// are required for the corresponding services; Global is required for the
// archived-project write gate (SPEC-08 §4.1).
type Options struct {
	Clock     clock.Clock
	Trash     *trash.Service
	Relations *relations.Service
	// Global is the workspace global.db handle (projects row status read).
	Global *sql.DB
	// Logger receives dirty-data warnings (TargetMissing links).
	Logger workspace.Logger
}

// DepsFromWorkspace builds the collaborator set for one project, mirroring
// the foundationtest wiring (trash gets no confirm service: kanban never
// purges, SPEC-08 §1.3.5).
func DepsFromWorkspace(ws *workspace.Workspace, handle *workspace.ProjectHandle) Options {
	c := ws.Clock()
	return Options{
		Clock:     c,
		Trash:     trash.New(handle.ID, handle.DB(), ws.Paths(), handle.Events(), nil, c),
		Relations: relations.New(handle.DB(), ws.Global().DB(), handle.Events(), c),
		Global:    ws.Global().DB(),
	}
}

// Module bundles every SPEC-08 service for one project handle.
type Module struct {
	handle  *workspace.ProjectHandle
	clock   clock.Clock
	opts    Options
	tasks   *taskService
	columns *columnService
	checkl  *checklistService
	labels  *labelService
	links   *linkService
	views   *viewService
}

// New binds the SPEC-08 services to a single project handle (SPEC-08 §5).
func New(handle *workspace.ProjectHandle, opts Options) *Module {
	if opts.Clock == nil {
		opts.Clock = clock.System{}
	}
	m := &Module{handle: handle, clock: opts.Clock, opts: opts}
	m.tasks = &taskService{m: m}
	m.columns = &columnService{m: m}
	m.checkl = &checklistService{m: m}
	m.labels = &labelService{m: m}
	m.links = &linkService{m: m}
	m.views = &viewService{m: m}
	return m
}

// Handle returns the bound project handle.
func (m *Module) Handle() *workspace.ProjectHandle { return m.handle }

// Tasks exposes TaskService.
func (m *Module) Tasks() TaskService { return m.tasks }

// Columns exposes ColumnService.
func (m *Module) Columns() ColumnService { return m.columns }

// Checklist exposes ChecklistService.
func (m *Module) Checklist() ChecklistService { return m.checkl }

// Labels exposes LabelService.
func (m *Module) Labels() LabelService { return m.labels }

// Links exposes LinkService.
func (m *Module) Links() LinkService { return m.links }

// Views exposes ViewService.
func (m *Module) Views() ViewService { return m.views }

// ---------------------------------------------------------------------------
// shared plumbing
// ---------------------------------------------------------------------------

// execer unifies *sql.DB and *sql.Tx so repo helpers run either way.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (m *Module) db() execer { return m.handle.DB() }

// writer returns the project event writer.
func (m *Module) writer() events.Writer { return m.handle.Events() }

// now is the formatted clock timestamp used for storage writes.
func (m *Module) now() string { return clock.Format(m.clock.Now()) }

// kanbanActor defaults an empty actor to the local user (mirrors notes).
func kanbanActor(a events.Actor) events.Actor {
	if a == "" {
		return schema.ActorUser
	}
	return a
}

// assertWritable enforces the SPEC-08 §4.1 archived-project gate on every
// write method.
func (m *Module) assertWritable(ctx context.Context) error {
	if m.opts.Global == nil {
		return fmt.Errorf("kanban: no global database configured for the archived-project check")
	}
	store := projects.New(m.opts.Global, m.clock)
	row, err := store.Get(ctx, m.handle.ID)
	if err != nil {
		return fmt.Errorf("kanban: project status: %w", err)
	}
	if row.Status == schema.ProjectArchived {
		return ErrProjectArchived
	}
	return nil
}

// ensureDefaultColumns is the SPEC-08 §4.3 first-entry backfill: when the
// task_columns table is completely empty the five defaults are seeded in the
// caller transaction. It never touches existing rows, so user renames,
// re-orderings and deletions of default columns survive.
func (m *Module) ensureDefaultColumns(ctx context.Context, tx *sql.Tx) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM task_columns`).Scan(&n); err != nil {
		return fmt.Errorf("kanban: seed check: %w", err)
	}
	if n > 0 {
		return nil
	}
	at := m.now()
	for i, c := range defaultColumns {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO task_columns(name, kind, is_done, position, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			c.name, c.kind, boolInt(c.isDone), (i+1)*sortStep, at, at); err != nil {
			return fmt.Errorf("kanban: seed column %s: %w", c.name, err)
		}
	}
	return nil
}

// validateTaskID rejects ids outside the UUIDv7 shape before any query.
func validateTaskID(id string) error {
	if !ids.Valid(id) {
		return fmt.Errorf("%w: task id %q", ErrInvalidInput, id)
	}
	return nil
}

// timestampToken formats an instant as the Version token.
func timestampToken(t time.Time) Version { return clock.Format(t) }

// boolInt maps a Go bool to the 0/1 INTEGER storage convention.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullString adapts an optional string to a SQL parameter.
func nullString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// parentParam adapts an optional parent id to the `IS ?` comparison form.
func parentParam(p *string) any { return nullString(p) }

// inClause builds `?, ?, ...` for placeholders(n).
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
