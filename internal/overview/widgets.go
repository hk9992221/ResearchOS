package overview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"researchos/internal/foundation/clock"
	foundationpaths "researchos/internal/foundation/paths"
	"researchos/internal/ports"
)

// WidgetID names one Overview component (§5.6). The eight values are the fixed
// card set of §3.2; the UI addresses them with Refresh.
type WidgetID string

// The eight Overview widgets, in the §3.2 display order.
const (
	WidgetCurrentFocus      WidgetID = "current_focus"
	WidgetActiveIdeas       WidgetID = "active_ideas"
	WidgetRecentExperiments WidgetID = "recent_experiments"
	WidgetMyTasks           WidgetID = "my_tasks"
	WidgetRecentNotes       WidgetID = "recent_notes"
	WidgetLiterature        WidgetID = "literature"
	WidgetBlockedTasks      WidgetID = "blocked_tasks"
	WidgetWeeklyProgress    WidgetID = "weekly_progress"
)

// widgetOrder is the canonical card order of §3.2. The Widgets slice of a
// Snapshot always follows it, so index i is stable across loads.
var widgetOrder = [...]WidgetID{
	WidgetCurrentFocus,
	WidgetActiveIdeas,
	WidgetRecentExperiments,
	WidgetMyTasks,
	WidgetRecentNotes,
	WidgetLiterature,
	WidgetBlockedTasks,
	WidgetWeeklyProgress,
}

// valid reports whether w is one of the eight known widgets.
func (w WidgetID) valid() bool {
	for _, known := range widgetOrder {
		if w == known {
			return true
		}
	}
	return false
}

// pendingDefault is the PendingConfirmation value a widget carries when it never
// loaded successfully and there is no previous state to inherit: My Tasks and
// Blocked Tasks are permanently unconfirmed (§10.2 items 2 and 3).
func (w WidgetID) pendingDefault() bool {
	return w == WidgetMyTasks || w == WidgetBlockedTasks
}

// Reason literals used in WidgetState.Reason. §5.6 names "module_not_registered",
// "pending_confirmation", "timeout" and "no_data"; "query_error" and "canceled"
// are the error classes of the two remaining failure modes (a rejected query and
// a cancelled caller), so the UI can distinguish them from a timeout.
const (
	reasonModuleNotRegistered = "module_not_registered"
	reasonPending             = "pending_confirmation"
	reasonTimeout             = "timeout"
	reasonQueryError          = "query_error"
	reasonCanceled            = "canceled"
)

// WidgetState is the per-card availability report of §5.6.
type WidgetState struct {
	ID        WidgetID
	Available bool
	Degraded  bool
	Reason    string
	// PendingConfirmation renders the 「待确认」badge: the card shows data that a
	// §16.2 requirement still calls unconfirmed (§8.1 PRJ-04).
	PendingConfirmation bool
}

// ProjectHeader is the self-contained global.db.projects projection of §5.6.
// overview carries its own copy so the two packages cannot form an import
// cycle; focus_md is deliberately absent because it is only shown inside
// FocusWidget.
type ProjectHeader struct {
	ID        string
	Name      string
	Summary   string
	Goals     string
	Status    string
	CreatedAt time.Time
	Archived  bool
}

// FocusWidget is Current Focus: goals plus focus_md plus the newest active
// ideas (§3.2, §5.6).
type FocusWidget struct {
	// GoalsExcerpt is projects.goals truncated to goalsExcerptRunes runes with
	// an ellipsis, so the card stays scannable. The full text lives in
	// ProjectHeader.Goals for the settings view.
	GoalsExcerpt string
	// FocusMD is projects.focus_md verbatim. An empty value is not a failure:
	// the card then shows GoalsExcerpt only (§5.6).
	FocusMD string
	// FocusEditable is Writable: an archived project shows the focus read-only.
	FocusEditable bool
	TopIdeas      []IdeaCard
}

// IdeaCard is one idea row. Deleted is always false because both idea queries
// exclude soft-deleted rows; the field mirrors the §5.6 contract.
type IdeaCard struct {
	ID        string
	Title     string
	Status    string
	UpdatedAt time.Time
	Deleted   bool
}

// ExperimentCard is one experiment with its newest Run summary. LastRunStatus is
// the em dash placeholder when the experiment has no run.
type ExperimentCard struct {
	ID            string
	Title         string
	LastRunStatus string
	RunCount      int
	UpdatedAt     time.Time
}

// TaskWidget is My Tasks. WeekPlanned is always false in this build: the
// week_plan filter is an unconfirmed §16.2 item, so the card orders by deadline
// and priority instead (§5.6).
type TaskWidget struct {
	WeekPlanned bool
	Items       []ports.TaskBrief
}

// NoteCard is one note row.
type NoteCard struct {
	ID        string
	Title     string
	Kind      string
	UpdatedAt time.Time
}

// PaperCard is one linked paper. Missing reports that the local PDF copy is
// gone or its path cannot be resolved.
type PaperCard struct {
	ID      string
	Title   string
	Authors string
	Year    int
	Missing bool
}

// BlockedWidget is Blocked Tasks. Only the title and the blocked flag are
// meaningful: blocking reason and unblock condition are §10.2 item 2, so
// ports.TaskBrief.BlockedReason stays empty.
type BlockedWidget struct {
	Items []ports.TaskBrief
}

// WeeklyWidget is Weekly Progress: the confirmed report of the current week, or
// the activity counts of the week while the weekly-report module has nothing
// confirmed (§3.2, §5.6).
type WeeklyWidget struct {
	ReportID       string
	Excerpt        string
	Confirmed      bool
	ActivityCounts map[string]int
}

// widgetOutcome is the internal carrier of one widget load: its state plus the
// single payload field that belongs to that widget. Keeping the payloads in one
// struct lets a failing loader hand its result over the same channel as a
// successful one.
type widgetOutcome struct {
	state WidgetState
	err   error

	focus       *FocusWidget
	ideas       []IdeaCard
	experiments []ExperimentCard
	myTasks     *TaskWidget
	notes       []NoteCard
	literature  []PaperCard
	blocked     *BlockedWidget
	weekly      *WeeklyWidget
}

// widgetPayload projects one widget of a snapshot back into an outcome, so a
// degraded widget can keep the data of the previous load (§6.3 「保留上次数据并
// 显示刷新按钮」). A nil snapshot yields the zero payload.
func widgetPayload(snap *Snapshot, w WidgetID) widgetOutcome {
	var out widgetOutcome
	if snap == nil {
		return out
	}
	for i := range snap.Widgets {
		if snap.Widgets[i].ID == w {
			out.state = snap.Widgets[i]
			break
		}
	}
	switch w {
	case WidgetCurrentFocus:
		v := snap.CurrentFocus
		out.focus = &v
	case WidgetActiveIdeas:
		out.ideas = snap.ActiveIdeas
	case WidgetRecentExperiments:
		out.experiments = snap.RecentExperiments
	case WidgetMyTasks:
		v := snap.MyTasks
		out.myTasks = &v
	case WidgetRecentNotes:
		out.notes = snap.RecentNotes
	case WidgetLiterature:
		out.literature = snap.Literature
	case WidgetBlockedTasks:
		v := snap.BlockedTasks
		out.blocked = &v
	case WidgetWeeklyProgress:
		v := snap.WeeklyProgress
		out.weekly = &v
	}
	return out
}

// indexOf returns the canonical slot of one widget in Snapshot.Widgets.
func indexOf(w WidgetID) int {
	for i, known := range widgetOrder {
		if known == w {
			return i
		}
	}
	return -1
}

// clearWidget drops the payload and the state of one widget, so a reload cannot
// leave a stale card behind when the reload itself produced nothing.
func clearWidget(snap *Snapshot, w WidgetID) {
	if i := indexOf(w); i >= 0 && i < len(snap.Widgets) {
		snap.Widgets[i] = WidgetState{ID: w}
	}
	switch w {
	case WidgetCurrentFocus:
		snap.CurrentFocus = FocusWidget{}
	case WidgetActiveIdeas:
		snap.ActiveIdeas = nil
	case WidgetRecentExperiments:
		snap.RecentExperiments = nil
	case WidgetMyTasks:
		snap.MyTasks = TaskWidget{}
	case WidgetRecentNotes:
		snap.RecentNotes = nil
	case WidgetLiterature:
		snap.Literature = nil
	case WidgetBlockedTasks:
		snap.BlockedTasks = BlockedWidget{}
	case WidgetWeeklyProgress:
		snap.WeeklyProgress = WeeklyWidget{}
	}
}

// apply writes one widget payload into the snapshot. The state slot is the
// caller's job (assemble and Refresh both know the canonical index).
func applyWidget(snap *Snapshot, w WidgetID, out widgetOutcome) {
	switch w {
	case WidgetCurrentFocus:
		if out.focus != nil {
			snap.CurrentFocus = *out.focus
		}
	case WidgetActiveIdeas:
		snap.ActiveIdeas = out.ideas
	case WidgetRecentExperiments:
		snap.RecentExperiments = out.experiments
	case WidgetMyTasks:
		if out.myTasks != nil {
			snap.MyTasks = *out.myTasks
		}
	case WidgetRecentNotes:
		snap.RecentNotes = out.notes
	case WidgetLiterature:
		snap.Literature = out.literature
	case WidgetBlockedTasks:
		if out.blocked != nil {
			snap.BlockedTasks = *out.blocked
		}
	case WidgetWeeklyProgress:
		if out.weekly != nil {
			snap.WeeklyProgress = *out.weekly
		}
	}
}

// Limits of §5.6: three focus ideas, five cards everywhere else, eight tasks.
const (
	focusTopIdeas = 3
	widgetLimit   = 5
	myTasksLimit  = 8
)

// goalsExcerptRunes and reportExcerptRunes are the documented truncation
// budgets. §5.6 asks for an excerpt ("摘要") without a number; 200 runes for the
// goals line and 400 for the weekly report keep both cards inside one screenful.
// truncateRunes appends a single ellipsis character when it cuts.
const (
	goalsExcerptRunes  = 200
	reportExcerptRunes = 400

	// noRunPlaceholder is the Run summary of an experiment without runs.
	noRunPlaceholder = "—"
)

// taskBaseSelect is the shared projection of the two task widgets; it matches
// ports.TaskBrief field for field so the scan order cannot drift.
const taskBaseSelect = `SELECT id, title, "column", priority, deadline, blocked, updated_at FROM tasks WHERE `

// loader holds everything the eight widget queries need. It is created per
// Load/Refresh call and used from the widget goroutines; each method touches
// only its own fields, so no locking is needed.
type loader struct {
	clock  clock.Clock
	pdb    *sql.DB // project.db; nil when the handle could not be opened
	pdbErr error
	gdb    *sql.DB // global.db for the literature fallback
	paths  foundationpaths.Paths
	head   headerRow
	// papers is the SPEC-04 port; nil selects the project_papers fallback.
	papers ports.PaperIndex
}

// load runs one widget query. The returned error is the raw query failure: the
// caller turns it into WidgetState.Degraded and keeps the previous data.
func (l *loader) load(ctx context.Context, w WidgetID) (widgetOutcome, error) {
	switch w {
	case WidgetCurrentFocus:
		return l.currentFocus(ctx)
	case WidgetActiveIdeas:
		return l.activeIdeas(ctx)
	case WidgetRecentExperiments:
		return l.recentExperiments(ctx)
	case WidgetMyTasks:
		return l.myTasks(ctx)
	case WidgetRecentNotes:
		return l.recentNotes(ctx)
	case WidgetLiterature:
		return l.literature(ctx)
	case WidgetBlockedTasks:
		return l.blockedTasks(ctx)
	case WidgetWeeklyProgress:
		return l.weeklyProgress(ctx)
	default:
		return widgetOutcome{}, fmt.Errorf("overview: unknown widget %q", w)
	}
}

// project returns the project database or the reason it is unavailable.
func (l *loader) project() (*sql.DB, error) {
	if l.pdb == nil {
		if l.pdbErr != nil {
			return nil, l.pdbErr
		}
		return nil, errNoWorkspace
	}
	return l.pdb, nil
}

// okState is the state of a widget that loaded and is usable.
func okState(w WidgetID) WidgetState {
	return WidgetState{ID: w, Available: true}
}

// pendingState is the state of a widget that loaded but shows unconfirmed
// §16.2 semantics.
func pendingState(w WidgetID) WidgetState {
	st := okState(w)
	st.PendingConfirmation = true
	st.Reason = reasonPending
	return st
}

// currentFocus reads goals and focus_md from the project row plus the three
// newest active ideas (§5.6).
func (l *loader) currentFocus(ctx context.Context) (widgetOutcome, error) {
	ideas, err := l.ideas(ctx, focusTopIdeas)
	if err != nil {
		return widgetOutcome{}, err
	}
	return widgetOutcome{
		state: okState(WidgetCurrentFocus),
		focus: &FocusWidget{
			GoalsExcerpt:  truncateRunes(l.head.Goals, goalsExcerptRunes),
			FocusMD:       l.head.focusMD,
			FocusEditable: l.head.Writable,
			TopIdeas:      ideas,
		},
	}, nil
}

// activeIdeas reads the five newest active ideas (§5.6).
func (l *loader) activeIdeas(ctx context.Context) (widgetOutcome, error) {
	ideas, err := l.ideas(ctx, widgetLimit)
	if err != nil {
		return widgetOutcome{}, err
	}
	return widgetOutcome{state: okState(WidgetActiveIdeas), ideas: ideas}, nil
}

// ideas runs the shared active-idea query. "Researching" and "Validating" are
// the two in-progress statuses of §3.2; updated_at drives the order, so the
// SPEC-00 §11.3 traceability requirement applies to every writer.
func (l *loader) ideas(ctx context.Context, limit int) ([]IdeaCard, error) {
	db, err := l.project()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, title, status, updated_at
		   FROM ideas
		  WHERE status IN ('Researching','Validating') AND deleted_at IS NULL
		  ORDER BY updated_at DESC, id
		  LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("overview: ideas: %w", err)
	}
	defer rows.Close()
	var out []IdeaCard
	for rows.Next() {
		var (
			id, title, status, stamped string
			card                       IdeaCard
		)
		if err := rows.Scan(&id, &title, &status, &stamped); err != nil {
			return nil, fmt.Errorf("overview: ideas: %w", err)
		}
		at, err := clock.Parse(stamped)
		if err != nil {
			return nil, fmt.Errorf("overview: ideas %s: updated_at: %w", id, err)
		}
		card = IdeaCard{ID: id, Title: title, Status: status, UpdatedAt: at}
		out = append(out, card)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("overview: ideas: %w", err)
	}
	return out, nil
}

// recentExperiments reads the five newest experiments with their latest run.
// The two-level Experiment/Run summary is an unconfirmed §16.2 item, so a
// project without any run is reported with the placeholder plus the 「待确认」
// badge instead of an empty column (§5.6).
func (l *loader) recentExperiments(ctx context.Context) (widgetOutcome, error) {
	db, err := l.project()
	if err != nil {
		return widgetOutcome{}, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT e.id, e.title, e.updated_at,
		        (SELECT r.status FROM runs r
		          WHERE r.experiment_id = e.id AND r.deleted_at IS NULL
		          ORDER BY r.updated_at DESC, r.created_at DESC, r.id LIMIT 1),
		        (SELECT count(*) FROM runs r
		          WHERE r.experiment_id = e.id AND r.deleted_at IS NULL)
		   FROM experiments e
		  WHERE e.deleted_at IS NULL
		  ORDER BY e.updated_at DESC, e.id
		  LIMIT ?`, widgetLimit)
	if err != nil {
		return widgetOutcome{}, fmt.Errorf("overview: experiments: %w", err)
	}
	defer rows.Close()
	var cards []ExperimentCard
	for rows.Next() {
		var (
			id, title, stamped string
			last               sql.NullString
			count              int
		)
		if err := rows.Scan(&id, &title, &stamped, &last, &count); err != nil {
			return widgetOutcome{}, fmt.Errorf("overview: experiments: %w", err)
		}
		at, err := clock.Parse(stamped)
		if err != nil {
			return widgetOutcome{}, fmt.Errorf("overview: experiments %s: updated_at: %w", id, err)
		}
		status := last.String
		if !last.Valid || status == "" {
			status = noRunPlaceholder
		}
		cards = append(cards, ExperimentCard{
			ID: id, Title: title, LastRunStatus: status, RunCount: count, UpdatedAt: at,
		})
	}
	if err := rows.Err(); err != nil {
		return widgetOutcome{}, fmt.Errorf("overview: experiments: %w", err)
	}

	var runs int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM runs WHERE deleted_at IS NULL`).Scan(&runs); err != nil {
		return widgetOutcome{}, fmt.Errorf("overview: experiment run count: %w", err)
	}
	st := okState(WidgetRecentExperiments)
	if runs == 0 {
		st.PendingConfirmation = true
		st.Reason = reasonPending
	}
	return widgetOutcome{state: st, experiments: cards}, nil
}

// myTasks reads the eight open tasks ordered by deadline then priority (§5.6).
func (l *loader) myTasks(ctx context.Context) (widgetOutcome, error) {
	// deadline first ascending with NULLs last, then the numeric rank of the
	// TEXT priority column descending.
	order := ` ORDER BY (deadline IS NULL OR deadline = ''), deadline, ` +
		priorityRankSQL + ` DESC, updated_at DESC, id`
	items, err := l.taskRows(ctx, `deleted_at IS NULL`, order, myTasksLimit)
	if err != nil {
		return widgetOutcome{}, err
	}
	return widgetOutcome{
		state:   pendingState(WidgetMyTasks),
		myTasks: &TaskWidget{WeekPlanned: false, Items: items},
	}, nil
}

// blockedTasks reads the five most recently touched blocked tasks. Only the
// title and the flag are shown: the blocking reason and the unblock condition
// are unconfirmed (§10.2 item 2).
func (l *loader) blockedTasks(ctx context.Context) (widgetOutcome, error) {
	items, err := l.taskRows(ctx, `blocked = 1 AND deleted_at IS NULL`,
		` ORDER BY updated_at DESC, id`, widgetLimit)
	if err != nil {
		return widgetOutcome{}, err
	}
	return widgetOutcome{
		state:   pendingState(WidgetBlockedTasks),
		blocked: &BlockedWidget{Items: items},
	}, nil
}

// priorityRankSQL maps the TEXT priority of the foundation schema to the
// ordering rank of ports.TaskBrief.Priority: high=3, medium=2, low=1 and
// anything else 0. priorityRank is the same mapping in Go, so the number the
// widget reports and the number it sorts by cannot disagree.
const priorityRankSQL = `CASE lower(COALESCE(priority, ''))
		                    WHEN 'high'   THEN 3
		                    WHEN 'medium' THEN 2
		                    WHEN 'low'    THEN 1
		                    ELSE 0 END`

func priorityRank(p sql.NullString) int {
	switch strings.ToLower(strings.TrimSpace(p.String)) {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

// taskRows runs a task query and projects it onto ports.TaskBrief. where must
// start with the deleted_at guard; the terminal-column scope of SPEC-08 is
// appended when the local schema version has those columns.
func (l *loader) taskRows(ctx context.Context, where, order string, limit int) ([]ports.TaskBrief, error) {
	db, err := l.project()
	if err != nil {
		return nil, err
	}
	scope, err := taskTerminalScope(ctx, db)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, taskBaseSelect+where+scope+order+` LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("overview: tasks: %w", err)
	}
	defer rows.Close()
	var out []ports.TaskBrief
	for rows.Next() {
		var (
			id, column, stamped string
			title               string
			priority            sql.NullString
			deadline            sql.NullString
			blocked             int
		)
		if err := rows.Scan(&id, &title, &column, &priority, &deadline, &blocked, &stamped); err != nil {
			return nil, fmt.Errorf("overview: tasks: %w", err)
		}
		at, err := clock.Parse(stamped)
		if err != nil {
			return nil, fmt.Errorf("overview: tasks %s: updated_at: %w", id, err)
		}
		brief := ports.TaskBrief{
			ID:       id,
			Title:    title,
			Column:   column,
			Priority: priorityRank(priority),
			Blocked:  blocked != 0,
			// BlockedReason stays empty until §16.2 adopts the field (§5.1).
			UpdatedAt: at,
		}
		if d := strings.TrimSpace(deadline.String); d != "" {
			if parsed, err := parseDeadline(d); err == nil {
				brief.Deadline = &parsed
			}
			// An unparseable deadline is a data problem of the Kanban module;
			// the Overview shows the task without a deadline rather than
			// degrading the whole card.
		}
		out = append(out, brief)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("overview: tasks: %w", err)
	}
	return out, nil
}

// parseDeadline accepts both storage shapes a deadline can carry: the canonical
// clock timestamp and a bare natural day.
func parseDeadline(s string) (time.Time, error) {
	if t, err := clock.Parse(s); err == nil {
		return t, nil
	}
	return time.ParseInLocation(time.DateOnly, s, time.UTC)
}

// taskTerminalScope returns the SQL fragment that excludes done and archived
// tasks when the SPEC-08 columns exist. The foundation schema does not have
// them yet, so the fallback is deleted_at only; the table is never altered.
func taskTerminalScope(ctx context.Context, db *sql.DB) (string, error) {
	cols, err := tableColumns(ctx, db, "tasks")
	if err != nil {
		return "", err
	}
	var scope strings.Builder
	if cols["completed_at"] {
		scope.WriteString(" AND completed_at IS NULL")
	}
	if cols["archived_at"] {
		scope.WriteString(" AND archived_at IS NULL")
	}
	return scope.String(), nil
}

// tableColumns reads PRAGMA table_info for a fixed table name.
func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, fmt.Errorf("overview: inspect %s: %w", table, err)
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
			return nil, fmt.Errorf("overview: inspect %s: %w", table, err)
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("overview: inspect %s: %w", table, err)
	}
	return cols, nil
}

// recentNotes reads the five most recently edited live notes (§5.6).
func (l *loader) recentNotes(ctx context.Context) (widgetOutcome, error) {
	db, err := l.project()
	if err != nil {
		return widgetOutcome{}, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, title, kind, updated_at
		   FROM notes
		  WHERE deleted_at IS NULL
		  ORDER BY updated_at DESC, id
		  LIMIT ?`, widgetLimit)
	if err != nil {
		return widgetOutcome{}, fmt.Errorf("overview: notes: %w", err)
	}
	defer rows.Close()
	var out []NoteCard
	for rows.Next() {
		var id, title, kind, stamped string
		if err := rows.Scan(&id, &title, &kind, &stamped); err != nil {
			return widgetOutcome{}, fmt.Errorf("overview: notes: %w", err)
		}
		at, err := clock.Parse(stamped)
		if err != nil {
			return widgetOutcome{}, fmt.Errorf("overview: notes %s: updated_at: %w", id, err)
		}
		out = append(out, NoteCard{ID: id, Title: title, Kind: kind, UpdatedAt: at})
	}
	if err := rows.Err(); err != nil {
		return widgetOutcome{}, fmt.Errorf("overview: notes: %w", err)
	}
	return widgetOutcome{state: okState(WidgetRecentNotes), notes: out}, nil
}

// literature reads the five most recently linked papers. SPEC-04 answers when
// the port is registered; otherwise project_papers joined against the global
// papers table is the source of truth (SPEC-00 v1.1). When neither source
// answers, Available=false and the UI hides the card without an error (§5.6).
func (l *loader) literature(ctx context.Context) (widgetOutcome, error) {
	if l.papers != nil {
		briefs, err := l.papers.ListProjectPapers(ctx, l.head.ID, widgetLimit)
		if err != nil {
			return widgetOutcome{}, fmt.Errorf("overview: literature port: %w", err)
		}
		cards := make([]PaperCard, 0, len(briefs))
		for _, b := range briefs {
			cards = append(cards, PaperCard{
				ID: b.ID, Title: b.Title, Authors: b.Authors, Year: b.Year, Missing: b.Missing,
			})
		}
		return widgetOutcome{state: okState(WidgetLiterature), literature: cards}, nil
	}
	if l.gdb == nil {
		return noLiterature(), nil
	}
	cards, err := l.literatureFallback(ctx)
	if err != nil {
		// The fallback is the last source: hide the card, never invent data.
		return widgetOutcome{state: WidgetState{
			ID:     WidgetLiterature,
			Reason: reasonModuleNotRegistered,
		}}, nil
	}
	return widgetOutcome{state: okState(WidgetLiterature), literature: cards}, nil
}

// noLiterature is the hidden Literature state.
func noLiterature() widgetOutcome {
	return widgetOutcome{state: WidgetState{
		ID:     WidgetLiterature,
		Reason: reasonModuleNotRegistered,
	}}
}

// literatureFallback joins project_papers with the global papers table in two
// steps: the two tables live in different databases, so there is no cross-DB
// JOIN to write.
func (l *loader) literatureFallback(ctx context.Context) ([]PaperCard, error) {
	db, err := l.project()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT paper_id FROM project_papers
		  ORDER BY linked_at DESC, paper_id
		  LIMIT ?`, widgetLimit)
	if err != nil {
		return nil, fmt.Errorf("overview: literature links: %w", err)
	}
	type link struct {
		paperID string
	}
	var links []link
	for rows.Next() {
		var ln link
		if err := rows.Scan(&ln.paperID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("overview: literature links: %w", err)
		}
		links = append(links, ln)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("overview: literature links: %w", err)
	}
	rows.Close()
	if len(links) == 0 {
		return nil, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(links)), ",")
	args := make([]any, 0, len(links))
	for _, ln := range links {
		args = append(args, ln.paperID)
	}
	prows, err := l.gdb.QueryContext(ctx,
		`SELECT id, metadata, file_path, file_state FROM papers WHERE id IN (`+placeholders+`)`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("overview: literature rows: %w", err)
	}
	defer prows.Close()
	type paperRow struct {
		metadata  string
		filePath  sql.NullString
		fileState string
	}
	found := make(map[string]paperRow, len(links))
	for prows.Next() {
		var (
			id  string
			row paperRow
		)
		if err := prows.Scan(&id, &row.metadata, &row.filePath, &row.fileState); err != nil {
			return nil, fmt.Errorf("overview: literature rows: %w", err)
		}
		found[id] = row
	}
	if err := prows.Err(); err != nil {
		return nil, fmt.Errorf("overview: literature rows: %w", err)
	}

	cards := make([]PaperCard, 0, len(links))
	for _, ln := range links {
		row, ok := found[ln.paperID]
		if !ok {
			// The link points at a paper row that is already gone: show the
			// entry as missing rather than dropping a real association.
			cards = append(cards, PaperCard{ID: ln.paperID, Missing: true})
			continue
		}
		title, authors, year := parseZoteroMetadata(json.RawMessage(row.metadata))
		cards = append(cards, PaperCard{
			ID:      ln.paperID,
			Title:   title,
			Authors: authors,
			Year:    year,
			Missing: l.paperMissing(row.filePath, row.fileState),
		})
	}
	return cards, nil
}

// paperMissing implements the §5.6 Missing rule: a non-ok file state or a path
// that cannot be resolved inside the workspace.
func (l *loader) paperMissing(filePath sql.NullString, fileState string) bool {
	if fileState != "ok" || !filePath.Valid {
		return true
	}
	_, err := l.paths.Abs(filePath.String)
	return err != nil
}

// parseZoteroMetadata extracts title, authors and year from a Zotero metadata
// JSON blob. It is deliberately forgiving: any shape it does not recognise
// yields the zero value, so an unexpected payload can never fail the widget.
func parseZoteroMetadata(raw json.RawMessage) (title, authors string, year int) {
	if len(raw) == 0 {
		return "", "", 0
	}
	var doc struct {
		Title    string `json:"title"`
		Date     string `json:"date"`
		Creators []struct {
			LastName  string `json:"lastName"`
			FirstName string `json:"firstName"`
			Name      string `json:"name"`
		} `json:"creators"`
		Author []struct {
			LastName  string `json:"lastName"`
			FirstName string `json:"firstName"`
			Name      string `json:"name"`
			Literal   string `json:"literal"`
		} `json:"author"`
		Issued struct {
			DateParts [][]int `json:"date-parts"`
		} `json:"issued"`
		Year any `json:"year"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		// Not an object (or malformed): nothing to show, but nothing failed.
		return "", "", 0
	}
	names := make([]string, 0, len(doc.Creators))
	for _, c := range doc.Creators {
		if s := personName(c.FirstName, c.LastName, c.Name, ""); s != "" {
			names = append(names, s)
		}
	}
	if len(names) == 0 {
		for _, a := range doc.Author {
			if s := personName(a.FirstName, a.LastName, a.Name, a.Literal); s != "" {
				names = append(names, s)
			}
		}
	}
	if len(doc.Issued.DateParts) > 0 && len(doc.Issued.DateParts[0]) > 0 {
		year = doc.Issued.DateParts[0][0]
	}
	if year == 0 {
		year = leadingYear(doc.Date)
	}
	if y, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(doc.Year))); err == nil && year == 0 {
		year = y
	}
	return doc.Title, strings.Join(names, ", "), year
}

// personName renders one creator entry: "First Last", or the single-field name
// of an institutional creator, or the CSL literal.
func personName(first, last, name, literal string) string {
	if s := strings.TrimSpace(name); s != "" {
		return s
	}
	if s := strings.TrimSpace(literal); s != "" {
		return s
	}
	first = strings.TrimSpace(first)
	last = strings.TrimSpace(last)
	switch {
	case first != "" && last != "":
		return first + " " + last
	case last != "":
		return last
	default:
		return first
	}
}

// leadingYear picks the first four-digit group of a Zotero free-form date.
func leadingYear(s string) int {
	for i := 0; i+4 <= len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			continue
		}
		digits := s[i : i+4]
		allDigits := true
		for _, c := range digits {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if !allDigits {
			continue
		}
		if n, err := strconv.Atoi(digits); err == nil && n >= 1000 {
			return n
		}
	}
	return 0
}

// weeklyProgress shows the confirmed report of the current ISO week, or the
// activity counts of that week while no confirmed draft overlaps it. The
// Weekly Planning link is unconfirmed (§10.2 item 3), so the fallback carries
// the 「待确认」badge.
func (l *loader) weeklyProgress(ctx context.Context) (widgetOutcome, error) {
	db, err := l.project()
	if err != nil {
		return widgetOutcome{}, err
	}
	start, end := weekWindow(l.clock.Now())
	firstDay := start.Format(time.DateOnly)
	lastDay := end.AddDate(0, 0, -1).Format(time.DateOnly)

	var (
		id, content string
	)
	err = db.QueryRowContext(ctx,
		`SELECT id, content_md
		   FROM weekly_reports
		  WHERE status = 'confirmed' AND deleted_at IS NULL
		    AND period_start <= ? AND period_end >= ?
		  ORDER BY period_start DESC, updated_at DESC, id DESC
		  LIMIT 1`, lastDay, firstDay).Scan(&id, &content)
	switch {
	case err == nil:
		return widgetOutcome{
			state: okState(WidgetWeeklyProgress),
			weekly: &WeeklyWidget{
				ReportID:  id,
				Excerpt:   truncateRunes(content, reportExcerptRunes),
				Confirmed: true,
			},
		}, nil
	case errors.Is(err, sql.ErrNoRows):
		// No confirmed draft for this week: fall through to the counters.
	default:
		return widgetOutcome{}, fmt.Errorf("overview: weekly report: %w", err)
	}

	rows, err := db.QueryContext(ctx,
		`SELECT entity_type, count(*)
		   FROM activities
		  WHERE created_at >= ? AND created_at < ?
		  GROUP BY entity_type
		  ORDER BY count(*) DESC, entity_type
		  LIMIT 6`, clock.Format(start), clock.Format(end))
	if err != nil {
		return widgetOutcome{}, fmt.Errorf("overview: weekly activities: %w", err)
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var (
			entityType string
			n          int
		)
		if err := rows.Scan(&entityType, &n); err != nil {
			return widgetOutcome{}, fmt.Errorf("overview: weekly activities: %w", err)
		}
		counts[entityType] = n
	}
	if err := rows.Err(); err != nil {
		return widgetOutcome{}, fmt.Errorf("overview: weekly activities: %w", err)
	}
	return widgetOutcome{
		state:  pendingState(WidgetWeeklyProgress),
		weekly: &WeeklyWidget{ActivityCounts: counts},
	}, nil
}

// weekWindow returns the half-open [Monday 00:00, next Monday 00:00) span that
// contains now, in the location of now itself (§5.6 「本周（周一 00:00 起，
// ISO-8601）」). The clock is the only time source, so a test pins the zone by
// putting the zone in the clock value; clock.System yields the UTC week.
func weekWindow(now time.Time) (start, end time.Time) {
	y, m, d := now.Date()
	monday := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	// Monday=0 … Sunday=6 for the ISO week, while time.Weekday is Sunday=0.
	offset := (int(monday.Weekday()) + 6) % 7
	start = monday.AddDate(0, 0, -offset)
	return start, start.AddDate(0, 0, 7)
}

// truncateRunes cuts s to at most n runes, marking the cut with one ellipsis
// character. It never splits a rune.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return strings.TrimRight(string(r[:n-1]), " \t\n") + "…"
}
