// Package overview implements the SPEC-03 §5.6 Project Overview aggregation
// (T3-10): the eight fixed widgets of §3.2, their parallel load with a
// per-widget timeout and per-widget degradation, the 15s snapshot cache, and the
// two quick actions that run through the SPEC-08 and SPEC-05 vendor ports.
//
// The package reads global.db.projects itself instead of importing
// internal/project, because §5.6 wants a self-contained ProjectHeader projection
// and no import cycle. It only ever reads; every write goes to the module that
// owns the entity, through the ports of §5.2.
//
// Event-bus wiring (subscribe and Invalidate on any event of the same project,
// §5.6 and §7.2) is scheduled for T3-17 and is deliberately absent here: callers
// that mutate a project call Invalidate themselves.
package overview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"researchos/internal/foundation/clock"
	foundationpaths "researchos/internal/foundation/paths"
	"researchos/internal/foundation/schema"
	foundationworkspace "researchos/internal/foundation/workspace"
	"researchos/internal/ports"
)

// Defaults of §5.6: a 15s result cache and a 500ms budget per widget.
const (
	defaultCacheTTL     = 15 * time.Second
	defaultWidgetBudget = 500 * time.Millisecond
)

// Snapshot is one Overview read: the project header, the eight widget payloads
// and their availability states (§5.6). Widgets always holds eight entries in
// the §3.2 order, so Widgets[i] is the state of widgetOrder[i].
//
// A returned snapshot is shared with the cache and must be treated as
// read-only by callers.
type Snapshot struct {
	Project  ProjectHeader
	Writable bool

	CurrentFocus      FocusWidget
	ActiveIdeas       []IdeaCard
	RecentExperiments []ExperimentCard
	MyTasks           TaskWidget
	RecentNotes       []NoteCard
	Literature        []PaperCard
	BlockedTasks      BlockedWidget
	WeeklyProgress    WeeklyWidget

	Widgets     []WidgetState
	GeneratedAt time.Time
}

// Options configures a Service. Only Workspace is required; a missing optional
// dependency degrades exactly the feature that needs it (§5.6).
type Options struct {
	// Workspace is the SPEC-01 workspace whose global.db registers the projects
	// and whose project handles answer the widget queries.
	Workspace *foundationworkspace.Workspace
	// Clock is the only time source: it stamps GeneratedAt, bounds the weekly
	// window and supplies the quick-note title. Nil uses the workspace clock,
	// then clock.System.
	Clock clock.Clock
	// PaperIndex is the SPEC-04 literature port. Nil falls back to
	// project_papers joined against global.db.papers; when that is impossible
	// too, the Literature widget reports Available=false.
	PaperIndex ports.PaperIndex
	// Tasks is the SPEC-08 Kanban port behind CompleteTask.
	Tasks ports.TaskCompleter
	// Notes is the SPEC-05 Notes port behind QuickAddNote.
	Notes ports.NoteCreator
	// CheckWritable overrides the §5.8 read-only guard of the quick actions.
	// The caller usually wires project.Service's guard so the archived check and
	// its error type stay identical on every write path. Its error is passed
	// through unchanged. Nil means the built-in check: an archived project
	// yields ErrArchived, a missing project row ErrNotFound.
	CheckWritable func(ctx context.Context, projectID string) error
	// CacheTTL is how long a snapshot answers Load without requerying. Zero or
	// negative uses 15s.
	CacheTTL time.Duration
	// WidgetTimeout is the per-widget budget. Zero or negative uses 500ms.
	WidgetTimeout time.Duration
}

// Service is the Overview aggregation. It is safe for concurrent use: the
// snapshot cache is guarded by mu, while widget queries run without holding it.
type Service struct {
	ws    *foundationworkspace.Workspace
	clock clock.Clock
	paths foundationpaths.Paths

	papers ports.PaperIndex
	tasks  ports.TaskCompleter
	notes  ports.NoteCreator

	checkWritable func(ctx context.Context, projectID string) error
	widgetTimeout time.Duration
	cacheTTL      time.Duration

	// beforeWidget runs inside every widget goroutine before its query, with the
	// widget deadline already ticking. It is a test hook for simulating a
	// hanging widget and is never part of the public API.
	beforeWidget func(WidgetID)

	mu    sync.Mutex
	cache map[string]*cacheEntry
}

// New builds a Service from opts. A nil Workspace yields a service whose every
// call reports errNoWorkspace rather than panicking.
func New(opts Options) *Service {
	s := &Service{
		ws:            opts.Workspace,
		clock:         opts.Clock,
		papers:        opts.PaperIndex,
		tasks:         opts.Tasks,
		notes:         opts.Notes,
		checkWritable: opts.CheckWritable,
		widgetTimeout: opts.WidgetTimeout,
		cacheTTL:      opts.CacheTTL,
		cache:         make(map[string]*cacheEntry),
	}
	if s.ws != nil {
		if s.clock == nil {
			s.clock = s.ws.Clock()
		}
		s.paths = s.ws.Paths()
	}
	if s.clock == nil {
		s.clock = clock.System{}
	}
	if s.widgetTimeout <= 0 {
		s.widgetTimeout = defaultWidgetBudget
	}
	if s.cacheTTL <= 0 {
		s.cacheTTL = defaultCacheTTL
	}
	return s
}

// Load returns the Overview snapshot of one project. A snapshot younger than
// CacheTTL answers from the cache; otherwise all eight widgets are requeried in
// parallel.
//
// Load only fails when the workspace is unavailable, when the project does not
// exist (ErrNotFound) or when ctx is already done: a widget failure degrades
// that widget alone and still returns a snapshot (§5.6).
func (s *Service) Load(ctx context.Context, projectID string) (*Snapshot, error) {
	if s.ws == nil {
		return nil, errNoWorkspace
	}
	// A caller that already walked away gets its context error even when the
	// cache could answer: a snapshot is only useful to a live request.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := s.clock.Now()
	if entry := s.entry(projectID); entry != nil && entry.fresh(now, s.cacheTTL) {
		return entry.snap, nil
	}

	head, err := s.header(ctx, projectID)
	if err != nil {
		return nil, err
	}
	l := s.loaderFor(ctx, head)
	outcomes := s.runWidgets(ctx, l, widgetOrder[:], s.previous(projectID))
	snap := assemble(head, outcomes, now)
	s.put(projectID, snap, now)
	return snap, nil
}

// Refresh recomputes one widget and returns a full snapshot. The other widgets
// reuse the cached payload, so refreshing a degraded card does not re-run seven
// unrelated queries (§5.6 "Refresh 强制重查单个组件"). Without a cached snapshot
// it behaves like Load.
//
// An unknown widget id is an error rather than a silent full reload.
func (s *Service) Refresh(ctx context.Context, projectID string, w WidgetID) (*Snapshot, error) {
	if s.ws == nil {
		return nil, errNoWorkspace
	}
	if !w.valid() {
		return nil, fmt.Errorf("overview: refresh: unknown widget %q", w)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Capture the previous snapshot once. Reading the cache twice (once to test
	// for it, once to copy it) races with a concurrent Invalidate and would
	// dereference nil. Without a snapshot there is nothing to graft the
	// refreshed widget onto, so a full Load is the only correct answer.
	prev := s.previous(projectID)
	if prev == nil {
		return s.Load(ctx, projectID)
	}
	now := s.clock.Now()
	head, err := s.header(ctx, projectID)
	if err != nil {
		return nil, err
	}

	// Start from the cached snapshot so the untouched widgets keep their data,
	// then overwrite the header-derived fields and the one reloaded widget.
	snap := *prev
	snap.Project = head.ProjectHeader
	snap.Writable = head.Writable
	snap.Widgets = append([]WidgetState(nil), prev.Widgets...)
	clearWidget(&snap, w)

	l := s.loaderFor(ctx, head)
	out := s.runWidgets(ctx, l, []WidgetID{w}, prev)[w]
	snap.Widgets[indexOf(w)] = out.state
	applyWidget(&snap, w, out)
	// FocusEditable is a projection of the header, not of a query: a refresh of
	// any other widget must not leave the focus card editable on a project
	// archived since the previous load.
	snap.CurrentFocus.FocusEditable = head.Writable
	snap.GeneratedAt = now
	s.put(projectID, &snap, now)
	return &snap, nil
}

// Invalidate drops the cached snapshot of one project so the next Load requery
// everything. It is idempotent and safe to call concurrently (§5.6, §7.2).
func (s *Service) Invalidate(projectID string) {
	s.mu.Lock()
	delete(s.cache, projectID)
	s.mu.Unlock()
}

// headerRow is the project header plus the columns only widgets consume.
type headerRow struct {
	ProjectHeader
	focusMD  string
	Writable bool
}

// header reads the project row straight from global.db (§5.6). Only "archived"
// blocks writes, so Writable is the negation of Archived and an unknown status
// stays writable (§5.8).
func (s *Service) header(ctx context.Context, projectID string) (headerRow, error) {
	global := s.ws.Global()
	if global == nil {
		return headerRow{}, errNoWorkspace
	}
	var id, name, summary, goals, focusMD, status, createdAt string
	var archivedAt sql.NullString
	err := global.DB().QueryRowContext(ctx,
		`SELECT id, name, summary, goals, focus_md, status, created_at, archived_at
		   FROM projects WHERE id = ?`, projectID).
		Scan(&id, &name, &summary, &goals, &focusMD, &status, &createdAt, &archivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return headerRow{}, ErrNotFound
	}
	if err != nil {
		return headerRow{}, fmt.Errorf("overview: project %s: %w", projectID, err)
	}
	at, err := clock.Parse(createdAt)
	if err != nil {
		return headerRow{}, fmt.Errorf("overview: project %s: created_at: %w", projectID, err)
	}
	head := headerRow{
		ProjectHeader: ProjectHeader{
			ID:        id,
			Name:      name,
			Summary:   summary,
			Goals:     goals,
			Status:    status,
			CreatedAt: at,
			Archived:  status == string(schema.ProjectArchived),
		},
		focusMD: focusMD,
	}
	head.Writable = statusWritable(status)
	return head, nil
}

// statusWritable is the §5.8 rule: only "archived" blocks content writes, so an
// unknown status is passed through for reconcile to report instead of being
// silently treated as read-only. The global schema CHECK constrains the column
// to the two known values, which is why the rule is a helper with its own test.
func statusWritable(status string) bool { return status != string(schema.ProjectArchived) }

// loaderFor binds the widget queries to one project. The workspace caches
// project handles, so opening is cheap; a failed open is not fatal here but
// becomes the degradation reason of every widget that needs project.db.
func (s *Service) loaderFor(ctx context.Context, head headerRow) *loader {
	l := &loader{
		clock:  s.clock,
		paths:  s.paths,
		head:   head,
		papers: s.papers,
	}
	if global := s.ws.Global(); global != nil {
		l.gdb = global.DB()
	}
	handle, err := s.ws.Project(ctx, head.ID)
	if err != nil {
		l.pdbErr = fmt.Errorf("overview: open project db: %w", err)
		return l
	}
	l.pdb = handle.DB()
	return l
}

// runWidgets executes the given widgets in parallel, each with its own budget,
// and returns one outcome per widget. A widget that misses its deadline or fails
// keeps the payload of prev and is marked Degraded (§5.6, §6.3 "保留上次数据并
// 显示刷新按钮").
func (s *Service) runWidgets(ctx context.Context, l *loader, want []WidgetID, prev *Snapshot) map[WidgetID]widgetOutcome {
	type slot struct {
		w      WidgetID
		out    chan widgetOutcome
		cancel context.CancelFunc
		done   <-chan struct{}
	}
	slots := make([]slot, 0, len(want))
	for _, w := range want {
		wctx, cancel := context.WithTimeout(ctx, s.widgetTimeout)
		out := make(chan widgetOutcome, 1)
		go func(w WidgetID, wctx context.Context) {
			defer close(out)
			if s.beforeWidget != nil {
				s.beforeWidget(w)
			}
			res, err := l.load(wctx, w)
			res.err = err
			out <- res
		}(w, wctx)
		slots = append(slots, slot{w: w, out: out, cancel: cancel, done: wctx.Done()})
	}

	results := make(map[WidgetID]widgetOutcome, len(want))
	for _, sl := range slots {
		out, ok := resolveSlot(sl.out, sl.done)
		if !ok {
			if ctxErr := ctx.Err(); ctxErr != nil {
				out.err = ctxErr
			}
			out = degrade(sl.w, prev, classify(out.err), out.err)
		}
		results[sl.w] = out
		sl.cancel()
	}
	return results
}

// resolveSlot collects one widget. A result that already landed wins over the
// deadline: the widgets share the project connection pool, so a fast query can
// finish while an earlier sibling was still being collected, and reporting that
// as a timeout would be wrong.
func resolveSlot(out chan widgetOutcome, done <-chan struct{}) (widgetOutcome, bool) {
	take := func() (widgetOutcome, bool, bool) {
		select {
		case o, ok := <-out:
			return o, ok && o.err == nil, true
		default:
			return widgetOutcome{}, false, false
		}
	}
	if o, ok, got := take(); got {
		return o, ok
	}
	select {
	case o, recvOK := <-out:
		return o, recvOK && o.err == nil
	case <-done:
		// The budget is spent. Take the outcome if the goroutine beat the
		// deadline by a hair, otherwise abandon the widget: its query runs under
		// wctx and stops on its own.
		if o, ok, got := take(); got {
			return o, ok
		}
		return widgetOutcome{err: context.DeadlineExceeded}, false
	}
}

// classify names the failure class stored in WidgetState.Reason (§5.6 lists
// "timeout"; the other two classes let the UI say whether the caller walked
// away or the query itself was rejected).
func classify(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return reasonTimeout
	case errors.Is(err, context.Canceled):
		return reasonCanceled
	default:
		return reasonQueryError
	}
}

// degrade builds the outcome of a widget that did not answer: its previous
// payload (or the zero value) plus a degraded state. Literature is the one
// widget that may hide instead of showing an empty card, so without previous
// data it reports unavailable (§5.6 "端口与回退均不可用 → 隐藏组件").
func degrade(w WidgetID, prev *Snapshot, reason string, err error) widgetOutcome {
	out := widgetPayload(prev, w)
	st := WidgetState{
		ID:       w,
		Reason:   reason,
		Degraded: true,
		// A degraded card stays on screen so the UI can offer the refresh
		// button: hiding belongs to the "no usable source" state of §5.6.
		Available:           true,
		PendingConfirmation: w.pendingDefault(),
	}
	if prev != nil {
		for _, p := range prev.Widgets {
			if p.ID == w {
				st.Available = p.Available
				st.PendingConfirmation = p.PendingConfirmation
				break
			}
		}
	}
	out.state = st
	out.err = err
	return out
}

// assemble builds the snapshot from the header and the widget outcomes. Every
// widget reports a state, so Widgets always has exactly eight entries.
func assemble(head headerRow, outcomes map[WidgetID]widgetOutcome, now time.Time) *Snapshot {
	snap := &Snapshot{
		Project:     head.ProjectHeader,
		Writable:    head.Writable,
		Widgets:     make([]WidgetState, len(widgetOrder)),
		GeneratedAt: now,
	}
	for i, w := range widgetOrder {
		out, ok := outcomes[w]
		if !ok {
			// No outcome means the widget never ran: report it degraded rather
			// than presenting an empty card as real data.
			out = degrade(w, nil, reasonQueryError, nil)
		}
		snap.Widgets[i] = out.state
		applyWidget(snap, w, out)
	}
	return snap
}
