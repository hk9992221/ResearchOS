// Package project implements the SPEC-03 project service: project CRUD over
// global.db.projects, the §5.4 creation pipeline, the single in-process project
// session, archive/unarchive and the §5.8 read-only guard.
//
// Cross-database invariant: global.db holds the registration row while
// project.db holds the Activity event. They can never share a transaction, so
// every write follows the fixed order "directory/database → registration →
// event". An event failure keeps the business change and surfaces
// ErrEventMissing instead of rolling back (§5.4, §7.1).
package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/projects"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/settings"
	"researchos/internal/foundation/workspace"
	"researchos/internal/ports"
)

// lastProjectKey is the browse-state setting remembering the last opened
// project. Opening a project is navigation, so it never writes an Activity.
const lastProjectKey = "app.last_project_id"

// LifecycleListener is notified around the project session lifecycle (§5.12).
// OnProjectOpen runs synchronously before the session becomes current; a
// listener error aborts the open and closes the listeners that already
// succeeded.
type LifecycleListener interface {
	OnProjectOpen(ctx context.Context, s *Session) error
	OnProjectClose(ctx context.Context, projectID string) error
}

// Options configures a Service. Every dependency is optional: nil ones are
// derived from Workspace or replaced by a safe default.
type Options struct {
	// Workspace is the SPEC-01 workspace this service is bound to.
	Workspace *workspace.Workspace
	// Projects overrides the global project store. Nil derives one from
	// Workspace.Global().DB().
	Projects *projects.Store
	// Settings overrides the workspace settings store. Nil derives one from
	// Workspace.Global().DB().
	Settings *settings.Store
	// Clock supplies timestamps and UUIDv7 prefixes. Nil uses the workspace
	// clock, then clock.System.
	Clock clock.Clock
	// Locker overrides the process-wide write lock. Nil uses an in-process
	// locker.
	Locker ports.Locker
	// PaperIndex, Notes, Tasks and Trash are the sibling-module ports of §5.2.
	// A missing port only disables the corresponding feature.
	PaperIndex ports.PaperIndex
	Notes      ports.NoteCreator
	Tasks      ports.TaskCompleter
	Trash      ports.TrashService

	// Busy probes the export job registry: it returns a blocker code (for
	// example "export_running") or "" when the project is idle. Nil means only
	// the workspace locker is probed by PrepareDelete (§5.4).
	Busy func(ctx context.Context, projectID string) (code string, err error)
	// CreateProjectData replaces Workspace.CreateProjectData. Testing seam: a
	// registration pipeline failure must roll back cleanly (§8.1 PRJ-01).
	CreateProjectData func(ctx context.Context, projectID string) error
	// InsertProject replaces the global projects INSERT. Testing seam for the
	// same rollback rule.
	InsertProject func(ctx context.Context, tx *sql.Tx, id, name, summary, goals string) error
}

// Service is safe for concurrent use. Session state is guarded by mu; database
// access relies on the foundation pools.
type Service struct {
	ws       *workspace.Workspace
	store    *projects.Store
	settings *settings.Store
	clock    clock.Clock
	locker   ports.Locker

	papers ports.PaperIndex
	notes  ports.NoteCreator
	tasks  ports.TaskCompleter
	trash  ports.TrashService

	busy          func(ctx context.Context, projectID string) (string, error)
	createData    func(ctx context.Context, projectID string) error
	insertProject func(ctx context.Context, tx *sql.Tx, id, name, summary, goals string) error

	mu        sync.Mutex
	session   *Session
	listeners []LifecycleListener
}

// New builds a Service from opts.
func New(opts Options) *Service {
	s := &Service{
		ws:            opts.Workspace,
		store:         opts.Projects,
		settings:      opts.Settings,
		clock:         opts.Clock,
		locker:        opts.Locker,
		papers:        opts.PaperIndex,
		notes:         opts.Notes,
		tasks:         opts.Tasks,
		trash:         opts.Trash,
		busy:          opts.Busy,
		createData:    opts.CreateProjectData,
		insertProject: opts.InsertProject,
	}
	if s.clock == nil && opts.Workspace != nil {
		s.clock = opts.Workspace.Clock()
	}
	if s.clock == nil {
		s.clock = clock.System{}
	}
	if opts.Workspace != nil {
		if g := opts.Workspace.Global(); g != nil {
			if s.store == nil {
				s.store = projects.New(g.DB(), s.clock)
			}
			if s.settings == nil {
				s.settings = settings.NewWorkspace(g.DB()).WithClock(s.clock)
			}
		}
	}
	if s.locker == nil {
		s.locker = newMemoryLocker()
	}
	return s
}

// RegisterListener adds a lifecycle listener. Listeners run in registration
// order.
func (s *Service) RegisterListener(l LifecycleListener) {
	if l == nil {
		return
	}
	s.mu.Lock()
	s.listeners = append(s.listeners, l)
	s.mu.Unlock()
}

// Current returns a copy of the active session, or nil when none is open.
func (s *Service) Current() *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil {
		return nil
	}
	cp := *s.session
	return &cp
}

// Create validates the input, creates the project directory and database, the
// global registration row and the entity.created event, then opens the session
// (§5.4). The returned error is nil on full success; a project created without
// its Activity event is returned together with an ErrEventMissing error.
func (s *Service) Create(ctx context.Context, in CreateInput) (*Project, error) {
	name, err := normalizeName(in.Name)
	if err != nil {
		return nil, err
	}
	if err := validateText("summary", in.Summary, maxSummaryRunes); err != nil {
		return nil, err
	}
	if err := validateText("goals", in.Goals, maxGoalsRunes); err != nil {
		return nil, err
	}
	store, global, err := s.globals()
	if err != nil {
		return nil, err
	}

	// The directory name must equal the id (§5.4 step 1), so SPEC-03 generates
	// the UUIDv7 itself instead of letting the store do it.
	id, err := ids.New(s.clock)
	if err != nil {
		return nil, fmt.Errorf("project: generate id: %w", err)
	}
	if err := s.createProjectData(ctx, id); err != nil {
		return nil, s.rollbackCreate(ctx, id, fmt.Errorf("project: create data for %s: %w", id, err))
	}

	ev := events.Event{
		Type:       schema.EventEntityCreated,
		EntityType: schema.EntityProject,
		EntityID:   id,
		Summary:    fmt.Sprintf("创建项目「%s」", name),
		Actor:      ports.ActorUser,
	}

	// Steps 3a and 3b of the pipeline under one workspace lock. The event error
	// is remembered instead of returned so the transaction commits and the
	// project survives (§5.4 "b 失败不回滚已创建的项目").
	var eventErr error
	err = s.locker.WithWorkspaceLock(ctx, func() error {
		if err := global.InTx(ctx, func(tx *sql.Tx) error {
			return s.insertProjectRow(ctx, tx, id, name, in.Summary, in.Goals)
		}); err != nil {
			return fmt.Errorf("project: register %s: %w", id, err)
		}
		if eerr := s.emitProjectEvent(ctx, id, ev); eerr != nil {
			eventErr = eerr
		}
		return nil
	})
	if err != nil {
		return nil, s.rollbackCreate(ctx, id, err)
	}

	row, err := store.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("project: read back created project %s: %w", id, err)
	}
	created := toProject(row)
	if eventErr != nil {
		return &created, fmt.Errorf("%w: project %s: %w", ErrEventMissing, id, eventErr)
	}

	// Step 4: the new project becomes the open session.
	sess, err := s.Open(ctx, id)
	if err != nil {
		return &created, fmt.Errorf("project: created %s but opening it failed: %w", id, err)
	}
	out := sess.Project
	return &out, nil
}

// Update edits name/summary/goals/focus_md. Only fields whose value actually
// changes are written and reported; a no-op returns the unchanged project
// without an event (§5.3, §7.1).
func (s *Service) Update(ctx context.Context, in UpdateInput) (*Project, error) {
	if err := s.assertWritable(ctx, in.ID); err != nil {
		return nil, err
	}
	actor, err := updateActor(in.Actor)
	if err != nil {
		return nil, err
	}

	var patch projects.ProjectPatch
	oldObj, newObj := events.Object{}, events.Object{}
	if in.Name != nil {
		name, err := normalizeName(*in.Name)
		if err != nil {
			return nil, err
		}
		in.Name = &name
	}
	if in.Summary != nil {
		if err := validateText("summary", *in.Summary, maxSummaryRunes); err != nil {
			return nil, err
		}
	}
	if in.Goals != nil {
		if err := validateText("goals", *in.Goals, maxGoalsRunes); err != nil {
			return nil, err
		}
	}
	if in.Focus != nil {
		if err := validateText("focus_md", *in.Focus, maxFocusRunes); err != nil {
			return nil, err
		}
	}

	store, global, err := s.globals()
	if err != nil {
		return nil, err
	}
	cur, err := store.Get(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if in.Name != nil && *in.Name != cur.Name {
		patch.Name = in.Name
		oldObj["name"], newObj["name"] = cur.Name, *in.Name
	}
	if in.Summary != nil && *in.Summary != cur.Summary {
		patch.Summary = in.Summary
		oldObj["summary"], newObj["summary"] = cur.Summary, *in.Summary
	}
	if in.Goals != nil && *in.Goals != cur.Goals {
		patch.Goals = in.Goals
		oldObj["goals"], newObj["goals"] = cur.Goals, *in.Goals
	}
	if in.Focus != nil && *in.Focus != cur.FocusMD {
		patch.FocusMD = in.Focus
		oldObj["focus_md"], newObj["focus_md"] = cur.FocusMD, *in.Focus
	}

	updated := toProject(cur)
	applyPatch(&updated, patch)
	if len(newObj) == 0 {
		// Nothing changed: neither write nor emit.
		return &updated, nil
	}

	if err := global.InTx(ctx, func(tx *sql.Tx) error {
		return store.Update(ctx, tx, in.ID, patch)
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("project: update %s: %w", in.ID, err)
	}

	// The registration row is committed; the event follows in its own database
	// transaction. A failure leaves the edit applied and reports the gap.
	ev := events.Event{
		Type:       schema.EventEntityUpdated,
		EntityType: schema.EntityProject,
		EntityID:   in.ID,
		Old:        &oldObj,
		New:        &newObj,
		Summary:    fmt.Sprintf("编辑项目「%s」", updated.Name),
		Actor:      actor,
	}
	if err := s.emitProjectEvent(ctx, in.ID, ev); err != nil {
		return &updated, fmt.Errorf("%w: project %s updated but the activity event is missing: %w", ErrEventMissing, in.ID, err)
	}
	return &updated, nil
}

// Get returns one project. A missing row yields ErrNotFound.
func (s *Service) Get(ctx context.Context, id string) (*Project, error) {
	store, _, err := s.globals()
	if err != nil {
		return nil, err
	}
	row, err := store.Get(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p := toProject(row)
	return &p, nil
}

// List returns active projects by default. With IncludeArchived archived
// projects follow the active ones; each group is ordered by created_at DESC
// with the id as tie-break. Query is a case-insensitive substring of name or
// summary. Sorting happens in Go: no index or SQL LIKE is added (§5.3).
func (s *Service) List(ctx context.Context, q ListQuery) ([]Project, error) {
	store, _, err := s.globals()
	if err != nil {
		return nil, err
	}
	rows, err := store.List(ctx, nil)
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(q.Query))
	out := make([]Project, 0, len(rows))
	for _, row := range rows {
		p := toProject(row)
		if !q.IncludeArchived && p.Status == StatusArchived {
			continue
		}
		if needle != "" &&
			!strings.Contains(strings.ToLower(p.Name), needle) &&
			!strings.Contains(strings.ToLower(p.Summary), needle) {
			continue
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Status == StatusArchived) != (b.Status == StatusArchived) {
			return a.Status != StatusArchived
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.ID > b.ID
	})
	return out, nil
}

// Stats aggregates one project database (§5.3). Archived projects are allowed.
// Task state falls back to deleted_at only when the SPEC-08 terminal columns
// (completed_at/archived_at) are absent from this schema version.
func (s *Service) Stats(ctx context.Context, id string) (Stats, error) {
	var st Stats
	if _, err := s.Get(ctx, id); err != nil {
		return st, err
	}
	if s.ws == nil {
		return st, errNoWorkspace
	}
	h, err := s.ws.Project(ctx, id)
	if err != nil {
		return st, fmt.Errorf("project: stats %s: %w", id, err)
	}
	db := h.DB()

	if st.NoteCount, err = countRows(ctx, db, `SELECT count(*) FROM notes WHERE deleted_at IS NULL`); err != nil {
		return st, err
	}
	if st.IdeaActive, err = countRows(ctx, db,
		`SELECT count(*) FROM ideas WHERE status IN ('Researching','Validating') AND deleted_at IS NULL`); err != nil {
		return st, err
	}
	if st.ExperimentCount, err = countRows(ctx, db, `SELECT count(*) FROM experiments WHERE deleted_at IS NULL`); err != nil {
		return st, err
	}
	taskScope, err := taskTerminalScope(ctx, db)
	if err != nil {
		return st, err
	}
	if st.TaskOpen, err = countRows(ctx, db, `SELECT count(*) FROM tasks WHERE deleted_at IS NULL`+taskScope); err != nil {
		return st, err
	}
	if st.TaskBlocked, err = countRows(ctx, db,
		`SELECT count(*) FROM tasks WHERE deleted_at IS NULL AND blocked = 1`+taskScope); err != nil {
		return st, err
	}
	if st.PaperCount, err = countRows(ctx, db, `SELECT count(*) FROM project_papers`); err != nil {
		return st, err
	}
	if st.TrashedCount, err = countRows(ctx, db, `SELECT count(*) FROM v_trash`); err != nil {
		return st, err
	}
	if st.DirBytes, err = s.dirBytes(ctx, id); err != nil {
		return st, err
	}

	var last sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT max(created_at) FROM activities`).Scan(&last); err != nil {
		return st, fmt.Errorf("project: stats %s: last activity: %w", id, err)
	}
	if last.Valid {
		at, err := clock.Parse(last.String)
		if err != nil {
			return st, fmt.Errorf("project: stats %s: last activity: %w", id, err)
		}
		st.LastActivityAt = &at
	}
	return st, nil
}

// Open makes id the single active session (§5.3). A previous session is closed
// first. Listeners run synchronously; a listener error aborts the open and
// notifies the listeners that already succeeded. Open never takes the write
// lock: opening is navigation.
func (s *Service) Open(ctx context.Context, id string) (*Session, error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// Close the previous session before the new one is announced so modules
	// flush the old project first (§5.3).
	if prev := s.Current(); prev != nil {
		if err := s.Close(ctx, prev.Project.ID); err != nil {
			return nil, err
		}
	}

	sess := &Session{Project: *p, Writable: p.Status != StatusArchived}
	listeners := s.snapshotListeners()
	succeeded := make([]LifecycleListener, 0, len(listeners))
	for _, l := range listeners {
		if err := l.OnProjectOpen(ctx, sess); err != nil {
			for _, done := range succeeded {
				_ = done.OnProjectClose(ctx, sess.Project.ID)
			}
			return nil, fmt.Errorf("project: open %s: listener: %w", id, err)
		}
		succeeded = append(succeeded, l)
	}

	s.mu.Lock()
	s.session = sess
	s.mu.Unlock()

	// Browse behavior: remember the last project, no Activity (§5.3).
	if err := s.persistLastProject(ctx, id); err != nil {
		s.mu.Lock()
		if s.session == sess {
			s.session = nil
		}
		s.mu.Unlock()
		for i := len(succeeded) - 1; i >= 0; i-- {
			_ = succeeded[i].OnProjectClose(ctx, id)
		}
		return nil, err
	}
	cp := *sess
	return &cp, nil
}

// Close flushes listeners, closes the SPEC-01 project handle and clears the
// session. An empty id closes whatever session is open; an id that is not the
// current session is a no-op. Close is idempotent.
func (s *Service) Close(ctx context.Context, id string) error {
	s.mu.Lock()
	sess := s.session
	if sess == nil || (id != "" && sess.Project.ID != id) {
		s.mu.Unlock()
		return nil
	}
	s.session = nil
	listeners := slices.Clone(s.listeners)
	s.mu.Unlock()

	var errList []error
	for _, l := range listeners {
		if err := l.OnProjectClose(ctx, sess.Project.ID); err != nil {
			errList = append(errList, fmt.Errorf("project: close %s: listener: %w", sess.Project.ID, err))
		}
	}
	if s.ws != nil {
		// Closing only the handle would leave a closed pool in the workspace
		// cache; CloseProject also evicts it so the project can be reopened.
		if err := s.ws.CloseProject(sess.Project.ID); err != nil {
			errList = append(errList, fmt.Errorf("project: close %s: %w", sess.Project.ID, err))
		}
	}
	return errors.Join(errList...)
}

// Archive moves an active project to read-only archived state (§5.3, PRJ-03).
// Only user and mcp may call it; a no-op archive writes no event.
func (s *Service) Archive(ctx context.Context, id string, actor ports.Actor) error {
	return s.setStatus(ctx, id, StatusArchived, actor)
}

// Unarchive restores an archived project to writable active state.
func (s *Service) Unarchive(ctx context.Context, id string, actor ports.Actor) error {
	return s.setStatus(ctx, id, StatusActive, actor)
}

// assertWritable is the §5.8 read-only guard: only "archived" blocks writes, so
// an unknown status is passed through for reconcile to report.
func (s *Service) assertWritable(ctx context.Context, projectID string) error {
	store, _, err := s.globals()
	if err != nil {
		return err
	}
	row, err := store.Get(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if row.Status == schema.ProjectArchived {
		return &ArchivedError{ProjectID: projectID}
	}
	return nil
}

// setStatus implements the archive state machine. Migration lock contention is
// detected through the workspace lock (fail-fast ErrBusy). Export-job
// contention needs the job registry that arrives with T3-05/T3-08; it is not
// faked here — a running export simply holds the project write lock, so this
// call waits for it.
func (s *Service) setStatus(ctx context.Context, id string, target Status, actor ports.Actor) error {
	actor, err := statusActor(actor)
	if err != nil {
		return err
	}
	cur, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if cur.Status == target {
		// Idempotent: keep the session flag consistent, write nothing, emit
		// nothing.
		s.refreshSession(ctx, id)
		return nil
	}
	if err := s.locker.WithWorkspaceLock(ctx, func() error { return nil }); err != nil {
		return err
	}

	store, global, err := s.globals()
	if err != nil {
		return err
	}
	return s.locker.WithProjectWriteLock(ctx, id, func() error {
		// Modules flush before the status flips (§5.3 Archive step 1).
		if err := s.notifyClose(ctx, id); err != nil {
			return err
		}
		if err := global.InTx(ctx, func(tx *sql.Tx) error {
			return store.SetStatus(ctx, tx, id, schemaStatus(target), actor)
		}); err != nil {
			return fmt.Errorf("project: set status %s: %w", id, err)
		}

		ev := events.Event{
			Type:       schema.EventStatusChanged,
			EntityType: schema.EntityProject,
			EntityID:   id,
			Old:        &events.Object{"status": string(cur.Status)},
			New:        &events.Object{"status": string(target)},
			Summary:    statusSummary(target, cur.Name),
			Actor:      actor,
		}
		emitErr := s.emitProjectEvent(ctx, id, ev)

		// The database is the source of truth: the session mirrors it even when
		// only the audit record failed.
		s.refreshSession(ctx, id)
		if emitErr != nil {
			return fmt.Errorf("%w: project %s changed status but the activity event is missing: %w", ErrEventMissing, id, emitErr)
		}
		return nil
	})
}

func statusSummary(target Status, name string) string {
	if target == StatusArchived {
		return fmt.Sprintf("归档项目「%s」", name)
	}
	return fmt.Sprintf("取消归档项目「%s」", name)
}

// refreshSession re-reads a project after a state change so the open session
// mirrors status, archived_at and writability. A missing row or a closed
// workspace leaves the session untouched.
func (s *Service) refreshSession(ctx context.Context, projectID string) {
	p, err := s.Get(ctx, projectID)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil && s.session.Project.ID == projectID {
		s.session.Project = *p
		s.session.Writable = p.Status != StatusArchived
	}
}

// notifyClose calls OnProjectClose without clearing the session: Archive keeps
// the handle for read-only queries (§5.3).
func (s *Service) notifyClose(ctx context.Context, projectID string) error {
	var errList []error
	for _, l := range s.snapshotListeners() {
		if err := l.OnProjectClose(ctx, projectID); err != nil {
			errList = append(errList, fmt.Errorf("project: %s: listener: %w", projectID, err))
		}
	}
	return errors.Join(errList...)
}

// persistLastProject stores the browse-state key in global settings.
func (s *Service) persistLastProject(ctx context.Context, id string) error {
	global, err := s.globalHandle()
	if err != nil {
		return err
	}
	if s.settings == nil {
		return nil
	}
	if err := global.InTx(ctx, func(tx *sql.Tx) error {
		return s.settings.Set(ctx, tx, lastProjectKey, id)
	}); err != nil {
		return fmt.Errorf("project: persist %s: %w", lastProjectKey, err)
	}
	return nil
}

// emitProjectEvent appends one Activity row to the project database. The
// caller owns ordering: the business change is already committed.
func (s *Service) emitProjectEvent(ctx context.Context, projectID string, ev events.Event) error {
	if s.ws == nil {
		return errNoWorkspace
	}
	h, err := s.ws.Project(ctx, projectID)
	if err != nil {
		return err
	}
	return h.InTx(ctx, func(tx *sql.Tx) error {
		return h.Events().Emit(ctx, tx, ev)
	})
}

// createProjectData creates projects/<id>/{notes,attachments} and project.db.
func (s *Service) createProjectData(ctx context.Context, id string) error {
	if s.createData != nil {
		return s.createData(ctx, id)
	}
	if s.ws == nil {
		return errNoWorkspace
	}
	return s.ws.CreateProjectData(ctx, id)
}

// insertProjectRow registers the project in global.db.
func (s *Service) insertProjectRow(ctx context.Context, tx *sql.Tx, id, name, summary, goals string) error {
	if s.insertProject != nil {
		return s.insertProject(ctx, tx, id, name, summary, goals)
	}
	if s.store == nil {
		return errNoWorkspace
	}
	return s.store.CreateWithID(ctx, tx, id, name, summary, goals)
}

// rollbackCreate undoes a partially created project. Nothing else can observe
// the project yet, so deleting the registration row and the directory is safe
// (§5.4). An incomplete cleanup names the orphan for startup reconcile.
func (s *Service) rollbackCreate(ctx context.Context, id string, cause error) error {
	if s.ws == nil {
		return cause
	}
	// The cleanup must finish even when ctx is why the creation failed.
	ctx = context.WithoutCancel(ctx)
	var cleanup []error
	// Evict the cached handle before touching the directory: on Windows an open
	// project.db keeps the files locked.
	if err := s.ws.CloseProject(id); err != nil {
		cleanup = append(cleanup, fmt.Errorf("close project handle: %w", err))
	}
	if s.store != nil {
		if global := s.ws.Global(); global != nil {
			if err := global.InTx(ctx, func(tx *sql.Tx) error {
				err := s.store.Delete(ctx, tx, id)
				if errors.Is(err, sql.ErrNoRows) {
					return nil
				}
				return err
			}); err != nil {
				cleanup = append(cleanup, fmt.Errorf("remove registration: %w", err))
			}
		}
	}
	dir := filepath.Join(s.ws.Root(), filepath.FromSlash(s.ws.Paths().ProjectDir(id)))
	if err := os.RemoveAll(dir); err != nil {
		cleanup = append(cleanup, fmt.Errorf("remove orphan directory %s: %w", dir, err))
	}
	if len(cleanup) > 0 {
		return fmt.Errorf("%w; rollback of %s is incomplete and startup reconcile must repair it: %w",
			cause, id, errors.Join(cleanup...))
	}
	return cause
}

// ProjectDir returns the absolute path of the project's directory inside the
// workspace, projects/<id>. It does not check existence: callers that act on
// the folder surface their own errors.
func (s *Service) ProjectDir(id string) (string, error) {
	if s.ws == nil {
		return "", errNoWorkspace
	}
	resolver := s.ws.Paths()
	dir, err := resolver.Abs(resolver.ProjectDir(id))
	if err != nil {
		return "", fmt.Errorf("project: dir %s: %w", id, err)
	}
	return dir, nil
}

// dirBytes sums the regular files under projects/<id>. A missing directory is
// reported as zero bytes: broken project data is surfaced by reconcile, not by
// failing the stats page.
func (s *Service) dirBytes(ctx context.Context, id string) (int64, error) {
	if s.ws == nil {
		return 0, errNoWorkspace
	}
	resolver := s.ws.Paths()
	dir, err := resolver.Abs(resolver.ProjectDir(id))
	if err != nil {
		return 0, fmt.Errorf("project: stats %s: %w", id, err)
	}
	var total int64
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		total += info.Size()
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("project: stats %s: walk: %w", id, err)
	}
	return total, nil
}

// taskTerminalScope returns the SQL fragment excluding terminal/archived tasks
// when the SPEC-08 columns exist. The foundation schema does not have them yet,
// so the fallback is deleted_at only; the table is never altered.
func taskTerminalScope(ctx context.Context, db *sql.DB) (string, error) {
	cols, err := tableColumns(ctx, db, "tasks")
	if err != nil {
		return "", err
	}
	scope := ""
	if cols["completed_at"] {
		scope += " AND completed_at IS NULL"
	}
	if cols["archived_at"] {
		scope += " AND archived_at IS NULL"
	}
	return scope, nil
}

// tableColumns reads PRAGMA table_info for a fixed table name.
func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, fmt.Errorf("project: inspect %s: %w", table, err)
	}
	defer rows.Close()
	cols := make(map[string]bool)
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return nil, fmt.Errorf("project: inspect %s: %w", table, err)
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("project: inspect %s: %w", table, err)
	}
	return cols, nil
}

// countRows runs one counting query.
func countRows(ctx context.Context, db *sql.DB, query string, args ...any) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("project: count: %w", err)
	}
	return n, nil
}

// applyPatch mirrors the store's UPDATE on the in-memory projection so Update
// does not need a second read.
func applyPatch(p *Project, patch projects.ProjectPatch) {
	if patch.Name != nil {
		p.Name = *patch.Name
	}
	if patch.Summary != nil {
		p.Summary = *patch.Summary
	}
	if patch.Goals != nil {
		p.Goals = *patch.Goals
	}
	if patch.FocusMD != nil {
		p.FocusMD = *patch.FocusMD
	}
}

// globals returns the store and global handle together; both are required by
// writers.
func (s *Service) globals() (*projects.Store, *workspace.GlobalHandle, error) {
	if s.store == nil {
		return nil, nil, errNoWorkspace
	}
	global, err := s.globalHandle()
	if err != nil {
		return nil, nil, err
	}
	return s.store, global, nil
}

func (s *Service) globalHandle() (*workspace.GlobalHandle, error) {
	if s.ws == nil {
		return nil, errNoWorkspace
	}
	global := s.ws.Global()
	if global == nil {
		return nil, errNoWorkspace
	}
	return global, nil
}

func (s *Service) snapshotListeners() []LifecycleListener {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.listeners)
}
