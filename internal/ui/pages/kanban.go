package pages

// The SPEC-08 kanban page of the shell: the research-task board, the list and
// blocked projections, the archive view and the task detail in the inspector.
// Every call runs in a managed shell task, so a frame renders cached state
// only; a failed move reloads the server state, which rolls the card back to
// where the database keeps it (SPEC-08 §6.5).

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"researchos/internal/foundation/schema"
	"researchos/internal/i18n"
	"researchos/internal/kanban"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/errtext"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// KanbanAPI is the part of SPEC-08 the page uses. The adapter is bound to the
// open workspace and takes the project id on every call, because the kanban
// module itself is bound to one project handle (SPEC-08 §5). A nil value means
// the composition root never assembled the module, and the page says so.
type KanbanAPI interface {
	Board(ctx context.Context, projectID string, q kanban.BoardQuery) (kanban.BoardView, error)
	Columns(ctx context.Context, projectID string) ([]kanban.Column, error)
	List(ctx context.Context, projectID string, q kanban.Query) (kanban.Page[kanban.TaskSummary], error)
	ListArchived(ctx context.Context, projectID string, q kanban.Query) (kanban.Page[kanban.TaskSummary], error)
	Blocked(ctx context.Context, projectID string) (kanban.Page[kanban.TaskSummary], error)

	GetTask(ctx context.Context, projectID, taskID string) (kanban.TaskDetail, error)
	CreateTask(ctx context.Context, projectID string, in kanban.CreateInput) (kanban.Task, error)
	UpdateTask(ctx context.Context, projectID string, in kanban.UpdateInput) (kanban.Task, error)
	MoveTask(ctx context.Context, projectID string, in kanban.MoveInput) (kanban.Task, error)
	ArchiveTask(ctx context.Context, projectID, taskID string) (kanban.Task, error)
	UnarchiveTask(ctx context.Context, projectID, taskID string) (kanban.Task, error)
	DeleteTask(ctx context.Context, projectID, taskID string) error

	CreateColumn(ctx context.Context, projectID string, in kanban.CreateColumnInput) (kanban.Column, error)
	RenameColumn(ctx context.Context, projectID, oldName, newName string) (kanban.Column, error)
	MoveColumn(ctx context.Context, projectID, name string, toPosition int) ([]kanban.Column, error)
	SetColumnDone(ctx context.Context, projectID, name string, done bool) (kanban.Column, error)
	DeleteColumn(ctx context.Context, projectID, name, migrateTo string) error

	ChecklistAdd(ctx context.Context, projectID, taskID, text string, beforeID *string) (kanban.ChecklistItem, error)
	ChecklistUpdate(ctx context.Context, projectID, itemID, text string) (kanban.ChecklistItem, error)
	ChecklistSetDone(ctx context.Context, projectID, itemID string, done bool) (kanban.ChecklistItem, error)
	ChecklistRemove(ctx context.Context, projectID, itemID string) error
	ChecklistReorder(ctx context.Context, projectID, taskID string, orderedIDs []string) error

	AddLabel(ctx context.Context, projectID, taskID, label string, expected *kanban.Version) (kanban.Task, error)
	RemoveLabel(ctx context.Context, projectID, taskID, label string, expected *kanban.Version) (kanban.Task, error)

	Links(ctx context.Context, projectID, taskID string) ([]kanban.EntityLink, error)
	AddLink(ctx context.Context, projectID, taskID string, dstType schema.EntityType, dstID string) error
	RemoveLink(ctx context.Context, projectID, taskID string, dstType schema.EntityType, dstID string) error
	// LinkCandidates lists what a task may be related to, grouped by type for
	// the picker. Only related_to is offered from the UI (SPEC-08 §5.6).
	LinkCandidates(ctx context.Context, projectID string) ([]LinkCandidate, error)
}

// The three tabs of the page. My Week, Calendar and Timeline stay out until
// their requirements are confirmed (SPEC-08 §6.6).
const (
	kanbanBoard = iota
	kanbanListView
	kanbanBlockedView
)

// Board geometry from SPEC-08 §6.2: the columns divide the board's width, so
// a normal window shows every column without scrolling. They stop shrinking at
// kanbanColumnMinWidth, beyond which the board scrolls horizontally, and stop
// growing at kanbanColumnMaxWidth, so a wide window does not stretch cards
// past a readable measure and leaves the surplus to the end of the board.
const (
	kanbanColumnMinWidth float32 = 220
	kanbanColumnMaxWidth float32 = 340
	kanbanColumnRows             = 10
)

// kanbanBoardColumnWidth divides the board's width over its columns and
// reports whether they overflow it. The width is the board's own room, one
// gap short of the columns it counts.
func kanbanBoardColumnWidth(available float32, columns int) (width float32, scroll bool) {
	if columns <= 0 {
		return kanbanColumnMinWidth, false
	}
	if available <= 0 {
		return kanbanColumnMinWidth, true
	}
	width = (available - tokens.SectionGap*float32(columns-1)) / float32(columns)
	if width < kanbanColumnMinWidth {
		return kanbanColumnMinWidth, true
	}
	if width > kanbanColumnMaxWidth {
		return kanbanColumnMaxWidth, false
	}
	return width, false
}

// kanbanTaskLimit is how many rows the list and archive views hold: one page
// of the service's 200-row cap.
const kanbanTaskLimit = 200

// kanbanTitleRunes bounds a card title. Runes, not bytes, keep a mixed
// Chinese/English title from losing its end (§6.5).
const kanbanTitleRunes = 120

// Column dialogs.
const (
	kanbanNoDialog = iota
	kanbanCreateColumn
	kanbanRenameColumn
	kanbanDeleteColumn
)

// kanbanTaskDrag is the value a card drags. It carries the identity Move
// needs, never the rendered row.
type kanbanTaskDrag struct {
	ID     string
	Column string
}

// kanbanColumnDrag is the value a column header drags.
type kanbanColumnDrag struct {
	Name string
}

// kanbanFilter is the query the toolbar edits. Priorities are OR-ed, labels
// are AND-ed, exactly as BoardQuery and Query read them (SPEC-08 §5.2, §5.7).
type kanbanFilter struct {
	text       string
	priorities [4]bool
	labels     string
	blocked    int // 0 any, 1 blocked, 2 not blocked
}

func (f kanbanFilter) active() bool {
	return strings.TrimSpace(f.text) != "" ||
		strings.TrimSpace(f.labels) != "" ||
		f.blocked != 0 ||
		f.priorities[0] || f.priorities[1] || f.priorities[2] || f.priorities[3]
}

func (f kanbanFilter) prioritiesList() []kanban.Priority {
	all := []kanban.Priority{kanban.PriorityHigh, kanban.PriorityMedium, kanban.PriorityLow, kanban.PriorityNone}
	out := make([]kanban.Priority, 0, len(all))
	for i, p := range all {
		if f.priorities[i] {
			out = append(out, p)
		}
	}
	return out
}

func (f kanbanFilter) labelsList() []string {
	var out []string
	for _, part := range strings.Split(f.labels, ",") {
		if s := strings.TrimSpace(part); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (f kanbanFilter) blockedValue() *bool {
	switch f.blocked {
	case 1:
		yes := true
		return &yes
	case 2:
		no := false
		return &no
	}
	return nil
}

// Kanban renders the SPEC-08 page.
type Kanban struct {
	Services *Services

	mu      sync.Mutex
	loading bool
	loaded  bool
	project string
	board   kanban.BoardView
	columns []kanban.Column
	list    []kanban.TaskSummary
	blocked []kanban.TaskSummary
	archive []kanban.TaskSummary
	// projectArchived mirrors project.ArchivedAt: every write control greys
	// out and a banner explains why (SPEC-08 §6.3 rule 5).
	projectArchived bool
	problems        []string
	// dirty asks the next frame for a reload after a write.
	dirty bool
	// loadedView and loadedArchive record what the cache holds, so a tab or
	// archive switch reloads the projection it needs.
	loadedView    int
	loadedArchive bool
	// filterSeen is the filter the cache was built with; a different one
	// reloads.
	filterSeen kanbanFilter

	// Frame-only state: the chosen tab, the toolbar, the dialogs and the
	// cached table state.
	view        int
	showArchive bool
	filterOpen  bool
	filter      kanbanFilter
	focusFilter bool
	table       *ui.ListState
	archiveTbl  *ui.ListState

	addColumn string
	addText   string
	addErr    string

	createOpen   bool
	createTitle  string
	createCol    string
	createPriori string
	createDead   *time.Time
	createErr    string

	colDialog int
	colName   string
	colKind   string
	colFrom   string
	colDone   bool
	colAfter  string
	colMoveTo int // -1 no re-position after create
	colTarget string
	colErr    string

	detail *taskDetail
	// routeTask is the task the current ports.RouteTask deep link opened,
	// so Escape does not reopen it on the next frame.
	routeTask string
	// viewSeq is the route generation whose view=blocked hint was applied, so
	// pressing the Board tab afterwards is not overridden on later frames.
	viewSeq uint64
}

// prepare initializes the frame-local state a zero page does not have.
func (k *Kanban) prepare() {
	if k.table == nil {
		k.table = components.NewTableState()
	}
	if k.archiveTbl == nil {
		k.archiveTbl = components.NewTableState()
	}
	if k.detail == nil {
		k.detail = &taskDetail{}
	}
}

// Page renders one frame of the kanban module.
func (k *Kanban) Page(v *components.UI, r ports.Route) {
	// A plain visit to the board resets the deep link, so the task route
	// opens its task once instead of reopening it after Escape.
	k.routeTask = ""
	if r.Params["view"] == "blocked" && k.Services.App != nil {
		if seq := k.Services.App.RouteSeq(); seq != k.viewSeq {
			k.viewSeq = seq
			k.view = kanbanBlockedView
		}
	}
	k.render(v, r)
}

// render draws the page; Page and TaskPage share it.
func (k *Kanban) render(v *components.UI, r ports.Route) {
	a := k.Services.App
	if a == nil {
		return
	}
	if k.Services.Kanban == nil {
		k.unavailable(v)
		return
	}
	project := a.Project()
	if project == nil {
		k.needsProject(v, a)
		return
	}
	k.prepare()
	k.syncArchived(a)
	if k.filter != k.filterSeen || k.view != k.loadedView || k.showArchive != k.loadedArchive {
		k.filterSeen = k.filter
		k.requestReload(a, project.ID)
	}
	k.load(a, project.ID)
	k.keys(v)
	k.tickDetail(a, project.ID)

	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.kanban.title"),
			Description: i18n.Tf("pages.kanban.description", map[string]string{"project": project.Name}),
			Actions: func() {
				if v.Button(i18n.T("pages.kanban.btn_new_task"), components.ButtonOptions{
					Variant:  components.VariantPrimary,
					Icon:     icons.Plus,
					Disabled: k.readOnly(),
				}).Clicked() {
					k.startCreate("")
				}
			},
		})
		k.banners(v)
		k.toolbar(v, a, project.ID)
		if k.filterOpen {
			k.filterPanel(v, a, project.ID)
		}
		switch {
		case k.blank():
			v.Skeleton(components.SkeletonOptions{Lines: 6})
		case k.showArchive:
			k.archiveView(v, a, project.ID)
		case k.view == kanbanListView:
			k.listView(v, a, project.ID)
		case k.view == kanbanBlockedView:
			k.blockedView(v, a, project.ID)
		default:
			k.boardView(v, a, project.ID)
		}
		k.createDialog(v, a, project.ID)
		k.columnDialogs(v, a, project.ID)
		k.detail.dialogs(v, a, k, project.ID)
	}})
}

// TaskPage is the ports.RouteTask target: the kanban page with one task open
// in the inspector, so a link from another module lands on the task it names.
func (k *Kanban) TaskPage(v *components.UI, r ports.Route) {
	a := k.Services.App
	if a != nil && r.EntityID != "" && k.routeTask != r.EntityID {
		if project := a.Project(); project != nil {
			k.prepare()
			k.routeTask = r.EntityID
			k.openTask(a, project.ID, r.EntityID)
		}
	}
	k.render(v, ports.Route{Kind: app.RouteKanban, ProjectID: r.ProjectID})
}

// unavailable is the honest state of a workspace whose module was not
// assembled.
func (k *Kanban) unavailable(v *components.UI) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{Title: i18n.T("pages.kanban.title")})
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.Kanban,
			Title:  i18n.T("pages.kanban.disabled_title"),
			Reason: i18n.T("pages.kanban.disabled_reason"),
		})
	}})
}

// needsProject states the precondition: tasks belong to a project.
func (k *Kanban) needsProject(v *components.UI, a *app.App) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.kanban.title"),
			Description: i18n.T("pages.kanban.needs_project_desc"),
		})
		v.EmptyState(components.EmptyStateOptions{
			Icon:        icons.Kanban,
			Title:       i18n.T("pages.kanban.needs_project_title"),
			Reason:      i18n.T("pages.kanban.needs_project_reason"),
			ActionLabel: i18n.T("pages.kanban.btn_projects"),
			OnAction:    func() { a.Navigate(ports.Route{Kind: app.RouteProjects}) },
		})
	}})
}

// readOnly reports the archived-project gate.
func (k *Kanban) readOnly() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.projectArchived
}

// syncArchived mirrors the project row the shell holds, so the banner shows
// in the first frame without a service probe.
func (k *Kanban) syncArchived(a *app.App) {
	p := a.Project()
	if p == nil {
		return
	}
	k.mu.Lock()
	k.projectArchived = p.ArchivedAt != nil
	k.mu.Unlock()
}

// blank reports whether the first load is still running.
func (k *Kanban) blank() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.loading && !k.loaded
}

// banners reports the archived-project state and load failures.
func (k *Kanban) banners(v *components.UI) {
	k.mu.Lock()
	problems := append([]string(nil), k.problems...)
	archived := k.projectArchived
	k.mu.Unlock()
	if archived {
		v.Banner(components.BannerOptions{
			Tone:    components.Warning,
			Title:   i18n.T("pages.kanban.archived_title"),
			Message: i18n.T("pages.kanban.archived_message"),
		})
	}
	for _, problem := range problems {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.kanban.banner_problem"),
			Message: problem,
		})
	}
}

// toolbar holds the view tabs, the filter toggle and the archive switch.
func (k *Kanban) toolbar(v *components.UI, a *app.App, projectID string) {
	ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).AlignItems(ui.Center).Wrap().Children(func() {
		v.Tabs(&k.view,
			i18n.T("pages.kanban.tab_board"),
			i18n.T("pages.kanban.tab_list"),
			i18n.T("pages.kanban.tab_blocked"),
		).Label(i18n.T("pages.kanban.tabs_label"))
		ui.Spacer(v.C)
		if !k.showArchive {
			variant := components.VariantSecondary
			if k.filter.active() || k.filterOpen {
				variant = components.VariantPrimary
			}
			if v.Button(i18n.T("pages.kanban.btn_filter"), components.ButtonOptions{
				Variant: variant,
				Icon:    icons.Search,
			}).Clicked() {
				k.filterOpen = !k.filterOpen
				if k.filterOpen {
					k.focusFilter = true
				}
			}
		}
		variant := components.VariantSecondary
		if k.showArchive {
			variant = components.VariantPrimary
		}
		if v.Button(i18n.T("pages.kanban.btn_archive"), components.ButtonOptions{
			Variant: variant,
			Icon:    icons.FileText,
		}).Clicked() {
			k.showArchive = !k.showArchive
			k.filterOpen = false
			k.requestReload(a, projectID)
		}
	})
}

// filterPanel edits the board and list query.
func (k *Kanban) filterPanel(v *components.UI, a *app.App, projectID string) {
	v.Card(components.CardOptions{
		Title:       i18n.T("pages.kanban.filter_title"),
		Description: i18n.T("pages.kanban.filter_desc"),
		Actions: func() {
			if v.Button(i18n.T("pages.kanban.filter_clear"), components.ButtonOptions{
				Variant: components.VariantGhost,
			}).Clicked() {
				k.filter = kanbanFilter{}
				k.requestReload(a, projectID)
			}
		},
	}, func() {
		ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).AlignItems(ui.End).Wrap().Children(func() {
			field := v.SearchInput(&k.filter.text, components.SearchOptions{
				Label:       i18n.T("pages.kanban.filter_text"),
				Placeholder: i18n.T("pages.kanban.filter_text_hint"),
			})
			if k.focusFilter {
				field.Focus()
				k.focusFilter = false
			}
			if field.Submitted() {
				k.requestReload(a, projectID)
			}
			labelField := v.Input(&k.filter.labels, components.FieldOptions{
				Label:       i18n.T("pages.kanban.filter_labels"),
				Placeholder: i18n.T("pages.kanban.filter_labels_hint"),
			})
			if labelField.Submitted() {
				k.requestReload(a, projectID)
			}
			blocked := strconv.Itoa(k.filter.blocked)
			sel := v.Select(&blocked, components.SelectOptions{
				Label: i18n.T("pages.kanban.filter_blocked"),
				Options: []string{
					i18n.T("pages.kanban.filter_blocked_any"),
					i18n.T("pages.kanban.filter_blocked_only"),
					i18n.T("pages.kanban.filter_blocked_none"),
				},
				Compact: true,
				Width:   140,
			})
			if sel.Changed() {
				n, _ := strconv.Atoi(blocked)
				k.filter.blocked = n
				k.requestReload(a, projectID)
			}
		})
		ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).Wrap().AlignItems(ui.Center).Children(func() {
			ui.Text(v.C, i18n.T("pages.kanban.filter_priority")).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.TextSecondary))
			for i, label := range []string{
				i18n.T("pages.kanban.priority_high"),
				i18n.T("pages.kanban.priority_medium"),
				i18n.T("pages.kanban.priority_low"),
				i18n.T("pages.kanban.priority_none"),
			} {
				v.Checkbox(&k.filter.priorities[i], label, components.ToggleOptions{})
			}
		})
	})
}

// boardView renders the columns with drag and drop (SPEC-08 §6.2).
func (k *Kanban) boardView(v *components.UI, a *app.App, projectID string) {
	k.mu.Lock()
	board := k.board
	archived := k.projectArchived
	k.mu.Unlock()
	if len(board.Columns) == 0 {
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.Kanban,
			Title:  i18n.T("pages.kanban.no_columns_title"),
			Reason: i18n.T("pages.kanban.no_columns_reason"),
		})
		return
	}
	// An empty board needs no banner above the columns: each column states
	// that it holds no card and offers its own add action, and the page
	// header already carries the create button.
	//
	// v.C.Size() is the window, which counts the sidebar and the inspector as
	// board; the shell's frame carries the room the page really has.
	_, height := v.C.Size()
	width := v.ContentWidth
	if width <= 0 {
		w, _ := v.C.Size()
		width = w - components.SidebarWidth - 1
	}
	width -= 2 * tokens.PagePadding
	if width < 1 {
		width = 1
	}
	colWidth, scroll := kanbanBoardColumnWidth(width, len(board.Columns))
	boardHeight := height - 260
	if boardHeight < 320 {
		boardHeight = 320
	}
	// The board owns exactly its width, so the scroll container measures its
	// own overflow: filled, it would take the width of its columns instead
	// and the surplus would only be clipped by the page. When the columns no
	// longer fit, the bar stays visible, in a style that reads on the board's
	// own background, so the off-screen columns are discoverable without
	// hovering.
	boardEl := ui.ScrollHorizontal(v.C).
		Width(width).
		Height(boardHeight).
		Gap(tokens.SectionGap).
		AlignItems(ui.Start).
		ScrollbarAxes(true, false).
		KeepScrollbars(scroll)
	if scroll {
		boardEl = boardEl.ScrollbarStyle(kanbanBoardScrollbar(v))
	}
	boardEl.Children(func() {
		for _, col := range board.Columns {
			k.column(v, a, projectID, col, colWidth, archived)
		}
	})
}

// kanbanBoardScrollbar styles the board's horizontal bar: the app's own
// scroll bars are overlay thumbnails that a user has to hover to find, which
// leaves a clipped column looking like the end of the board.
func kanbanBoardScrollbar(v *components.UI) ui.ScrollbarStyle {
	p := v.T.Palette
	return ui.ScrollbarStyle{
		ThumbWidth: 8,
		Thumb:      mygo.Color(p.TextMuted),
		Hover:      mygo.Color(p.TextSecondary),
		Active:     mygo.Color(p.TextPrimary),
		Track:      mygo.Color(p.BgSubtle),
	}
}

// column renders one board column: header, cards, quick add.
func (k *Kanban) column(v *components.UI, a *app.App, projectID string, col kanban.BoardColumn, colWidth float32, archived bool) {
	p := v.T.Palette
	c := ui.Column(v.C).
		Key("kanban-col:" + col.Column.Name).
		Width(colWidth).
		Gap(tokens.Space2).
		Padding(tokens.Space3).
		Background(mygo.Color(p.BgSubtle)).
		Radius(tokens.RadiusLG)
	if !archived {
		c.Drag(kanbanColumnDrag{Name: col.Column.Name})
	}
	if d, ok := ui.DragOver[kanbanColumnDrag](c); ok && d.Name != col.Column.Name {
		c.Border(2, mygo.Color(p.AccentDefault))
	}
	if d, ok := ui.Drop[kanbanColumnDrag](c); ok && d.Name != col.Column.Name && !archived {
		k.moveColumn(a, projectID, d.Name, k.columnTargetIndex(d.Name, col.Column.Name))
	}
	if d, ok := ui.Drop[kanbanTaskDrag](c); ok && !archived {
		// Dropped on the column itself: append to its end.
		k.moveTask(a, projectID, d, col.Column.Name, "", "")
	}

	c.Children(func() {
		ui.Row(v.C).FillWidth().Gap(tokens.Space2).AlignItems(ui.Center).Children(func() {
			title := ui.Text(v.C, col.Column.Name).
				Grow(1).
				SingleLine().
				FontSize(tokens.Typography.Body.Size).
				FontWeight(int(tokens.WeightSemibold))
			if col.Column.IsDone {
				v.Icon(icons.Success, icons.SizeDefault).TextColor(mygo.Color(p.StatusSuccess))
				_ = title
			}
			v.Badge(strconv.Itoa(len(col.Tasks)), components.Neutral)
			v.IconButton(icons.More, i18n.T("pages.kanban.col_menu"), components.IconButtonOptions{
				Variant:  components.VariantGhost,
				Size:     components.SizeSmall,
				Disabled: archived,
			}).Menu(func(m *ui.Menu) { k.columnMenu(m, col.Column) })
		})
		if len(col.Tasks) == 0 {
			ui.Text(v.C, i18n.T("pages.kanban.col_empty")).
				Padding(tokens.Space2).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(p.TextMuted))
		}
		for i, task := range col.Tasks {
			k.card(v, a, projectID, col, task, i, archived)
		}
		if archived {
			return
		}
		quick := v.Button(i18n.T("pages.kanban.btn_add_card"), components.ButtonOptions{
			Variant: components.VariantGhost,
			Size:    components.SizeSmall,
			Icon:    icons.Plus,
		})
		if k.addColumn == col.Column.Name {
			in := v.Input(&k.addText, components.FieldOptions{
				Label:       i18n.T("pages.kanban.quick_label"),
				Placeholder: i18n.T("pages.kanban.quick_hint"),
			})
			if in.Submitted() {
				k.quickAdd(a, projectID, col.Column.Name)
			}
			if k.addErr != "" {
				ui.Text(v.C, k.addErr).
					FontSize(tokens.Typography.Caption.Size).
					TextColor(mygo.Color(p.StatusDanger))
			}
		} else if quick.Clicked() {
			k.addColumn, k.addText, k.addErr = col.Column.Name, "", ""
		}
	})
}

// card renders one task card with the SPEC-08 §6.2 information.
func (k *Kanban) card(v *components.UI, a *app.App, projectID string, col kanban.BoardColumn, task kanban.TaskSummary, index int, archived bool) {
	p := v.T.Palette
	card := ui.Column(v.C).
		Key("kanban-card:"+task.ID).
		FillWidth().
		Padding(tokens.Space3).
		Gap(tokens.Space1).
		Background(mygo.Color(p.BgSurface)).
		Border(1, mygo.Color(p.BorderDefault)).
		Radius(tokens.RadiusMD)
	if !archived {
		card.Drag(kanbanTaskDrag{ID: task.ID, Column: task.Column})
	}
	if card.Dragging() {
		card.Opacity(0.4)
	}
	if d, ok := ui.DragOver[kanbanTaskDrag](card); ok && d.ID != task.ID && !archived {
		card.Border(2, mygo.Color(p.AccentDefault))
	}
	if d, ok := ui.Drop[kanbanTaskDrag](card); ok && d.ID != task.ID && !archived {
		k.moveTask(a, projectID, d, col.Column.Name, task.ID, "")
	}
	// The card menu is the documented fallback for a gesture the user cannot
	// complete (SPEC-08 §6.5): move to another column, reorder, archive.
	card.ContextMenu(func(m *ui.Menu) {
		if m.Item(i18n.T("pages.kanban.menu_open")).ID("open").Chosen() {
			k.openTask(a, projectID, task.ID)
		}
		if archived {
			return
		}
		m.Separator()
		m.Submenu(i18n.T("pages.kanban.menu_move_to"), func(sub *ui.Menu) {
			for _, other := range k.columnNames() {
				if other == col.Column.Name {
					continue
				}
				if sub.Item(other).ID("move:" + other).Chosen() {
					k.moveTask(a, projectID, kanbanTaskDrag{ID: task.ID, Column: task.Column}, other, "", "")
				}
			}
		})
		up := m.Item(i18n.T("pages.kanban.menu_move_up")).ID("up")
		up.Disabled(index == 0)
		if up.Chosen() && index > 0 {
			k.moveTask(a, projectID, kanbanTaskDrag{ID: task.ID, Column: task.Column}, col.Column.Name, col.Tasks[index-1].ID, "")
		}
		down := m.Item(i18n.T("pages.kanban.menu_move_down")).ID("down")
		down.Disabled(index+1 >= len(col.Tasks))
		if down.Chosen() && index+1 < len(col.Tasks) {
			k.moveTask(a, projectID, kanbanTaskDrag{ID: task.ID, Column: task.Column}, col.Column.Name, "", col.Tasks[index+1].ID)
		}
		m.Separator()
		if m.Item(i18n.T("pages.kanban.menu_archive")).ID("archive").Chosen() {
			k.archiveTask(a, projectID, task.ID)
		}
	})

	card.Children(func() {
		// The priority bar: the color, never alone — the dot in the meta row
		// carries the same color and its name in a tooltip and an accessible
		// label (ui_plan.md §2.3). An unset priority paints blue, so every
		// card carries the bar and the bar's color means one thing only.
		ui.Box(v.C).
			FillWidth().
			Height(3).
			Radius(tokens.RadiusSM).
			Background(mygo.Color(priorityColor(v, task.Priority)))
		ui.Row(v.C).FillWidth().Gap(tokens.Space1).AlignItems(ui.Start).Children(func() {
			ui.Text(v.C, kanbanTitle(task.Title)).
				Grow(1).
				MaxLines(2).
				FontSize(tokens.Typography.Body.Size).
				TextColor(mygo.Color(p.TextPrimary)).
				OnClick(func() { k.openTask(a, projectID, task.ID) })
			if task.Blocked {
				v.Icon(icons.Warning, icons.SizeDefault).TextColor(mygo.Color(p.StatusDanger))
			}
		})
		hasDeadline := task.Deadline != nil && *task.Deadline != ""
		// The dot is always there: an unset priority shows blue rather than
		// nothing, so "no priority" is a state the board states rather than
		// an absence the reader has to infer.
		ui.Row(v.C).FillWidth().Gap(tokens.Space2).AlignItems(ui.Center).Children(func() {
			priorityDot(v, task.Priority)
			if hasDeadline {
				overdue := kanbanOverdue(*task.Deadline)
				ui.Row(v.C).Gap(tokens.Space1).AlignItems(ui.Center).Children(func() {
					v.Icon(icons.Clock, icons.SizeDefault)
					text := ui.Text(v.C, *task.Deadline).
						FontSize(tokens.Typography.Caption.Size).
						FontFeatures("tnum")
					if overdue {
						text.TextColor(mygo.Color(p.StatusDanger))
					}
				})
			}
			if task.SubtaskTotal > 0 {
				ui.Text(v.C, i18n.Tf("pages.kanban.card_subtasks", map[string]string{
					"done":  strconv.Itoa(task.SubtaskDone),
					"total": strconv.Itoa(task.SubtaskTotal),
				})).FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(p.TextSecondary))
			}
			if task.ChecklistTotal > 0 {
				ui.Text(v.C, i18n.Tf("pages.kanban.card_checklist", map[string]string{
					"done":  strconv.Itoa(task.ChecklistDone),
					"total": strconv.Itoa(task.ChecklistTotal),
				})).FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(p.TextSecondary))
			}
		})
		if len(task.Labels) > 0 {
			ui.Row(v.C).FillWidth().Gap(tokens.Space1).Wrap().Children(func() {
				for _, label := range task.Labels {
					v.Badge(label, components.Neutral)
				}
			})
		}
	})
}

// moveTask calls TaskService.Move and reloads, which rolls the visual position
// back when the write fails (SPEC-08 §6.3 rule 3).
func (k *Kanban) moveTask(a *app.App, projectID string, drag kanbanTaskDrag, column, beforeID, afterID string) {
	api := k.Services.Kanban
	if api == nil {
		return
	}
	// Optimistic reorder: the card moves now; a failure reloads the server
	// state, which puts it back.
	k.mu.Lock()
	k.applyMoveLocked(drag, column, beforeID, afterID)
	k.mu.Unlock()

	in := kanban.MoveInput{ID: drag.ID, Column: column}
	if beforeID != "" {
		in.BeforeID = &beforeID
	}
	if afterID != "" {
		in.AfterID = &afterID
	}
	a.RunTask(i18n.T("pages.kanban.task_move"), func(ctx context.Context, t *app.Task) error {
		_, err := api.MoveTask(ctx, projectID, in)
		k.mu.Lock()
		if err != nil {
			k.problems = append(k.problems[:0], kanbanErrorText(err))
		}
		k.dirty = true
		k.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.kanban.toast_move_fail"), kanbanErrorText(err))
		}
		a.Notify()
		return nil
	})
}

// applyMoveLocked moves the card in the cached board so the next frame shows
// the new position before the write lands.
func (k *Kanban) applyMoveLocked(drag kanbanTaskDrag, column, beforeID, afterID string) {
	var card kanban.TaskSummary
	found := false
	for ci := range k.board.Columns {
		tasks := k.board.Columns[ci].Tasks
		for ti := range tasks {
			if tasks[ti].ID == drag.ID {
				card = tasks[ti]
				k.board.Columns[ci].Tasks = append(tasks[:ti], tasks[ti+1:]...)
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		return
	}
	card.Column = column
	for ci := range k.board.Columns {
		if k.board.Columns[ci].Column.Name != column {
			continue
		}
		tasks := k.board.Columns[ci].Tasks
		at := len(tasks)
		if beforeID != "" {
			for i := range tasks {
				if tasks[i].ID == beforeID {
					at = i
					break
				}
			}
		} else if afterID != "" {
			for i := range tasks {
				if tasks[i].ID == afterID {
					at = i + 1
					break
				}
			}
		}
		tasks = append(tasks, kanban.TaskSummary{})
		copy(tasks[at+1:], tasks[at:])
		tasks[at] = card
		k.board.Columns[ci].Tasks = tasks
		return
	}
}

// quickAdd creates a task at the end of one column and keeps the input for
// continuous entry (SPEC-08 §6.3 rule 1).
func (k *Kanban) quickAdd(a *app.App, projectID, column string) {
	title := strings.TrimSpace(k.addText)
	if title == "" {
		k.addErr = i18n.T("pages.kanban.title_required")
		return
	}
	api := k.Services.Kanban
	k.addErr = ""
	a.RunTask(i18n.T("pages.kanban.task_create"), func(ctx context.Context, t *app.Task) error {
		if _, err := api.CreateTask(ctx, projectID, kanban.CreateInput{Title: title, Column: column}); err != nil {
			k.addErr = kanbanErrorText(err)
			a.Toast(app.LevelDanger, i18n.T("pages.kanban.toast_create_fail"), kanbanErrorText(err))
			a.Notify()
			return nil
		}
		k.addText = ""
		k.requestReload(a, projectID)
		return nil
	})
}

// startCreate opens the new-task dialog, optionally preselecting a column.
func (k *Kanban) startCreate(column string) {
	k.createOpen = true
	k.createTitle = ""
	k.createErr = ""
	k.createPriori = i18n.T("pages.kanban.priority_none")
	k.createDead = nil
	k.createCol = column
}

// createDialog collects a top-level task with priority and deadline; the
// board's quick add covers the fast path.
func (k *Kanban) createDialog(v *components.UI, a *app.App, projectID string) {
	if !k.createOpen {
		return
	}
	cols := k.columnNames()
	if k.createCol == "" && len(cols) > 0 {
		k.createCol = cols[0]
	}
	v.Dialog(&k.createOpen, components.DialogOptions{
		Title:       i18n.T("pages.kanban.create_title"),
		Description: i18n.T("pages.kanban.create_desc"),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				k.createOpen = false
			}
			if v.Button(i18n.T("pages.kanban.create_submit"), components.ButtonOptions{
				Variant: components.VariantPrimary,
			}).Clicked() {
				k.create(a, projectID)
			}
		},
	}, func() {
		field := v.Input(&k.createTitle, components.FieldOptions{
			Label:       i18n.T("pages.kanban.field_title"),
			Placeholder: i18n.T("pages.kanban.field_title_hint"),
			AutoFocus:   true,
			Error:       k.createErr,
		})
		if field.Submitted() {
			k.create(a, projectID)
		}
		v.Select(&k.createCol, components.SelectOptions{
			Label:   i18n.T("pages.kanban.field_column"),
			Options: cols,
		})
		v.Select(&k.createPriori, components.SelectOptions{
			Label: i18n.T("pages.kanban.field_priority"),
			Options: []string{
				i18n.T("pages.kanban.priority_high"),
				i18n.T("pages.kanban.priority_medium"),
				i18n.T("pages.kanban.priority_low"),
				i18n.T("pages.kanban.priority_none"),
			},
		})
		_ = deadlineField(v, &k.createDead, i18n.T("pages.kanban.field_deadline"))
	})
}

// create runs the create call of the dialog.
func (k *Kanban) create(a *app.App, projectID string) {
	title := strings.TrimSpace(k.createTitle)
	if title == "" {
		k.createErr = i18n.T("pages.kanban.title_required")
		return
	}
	in := kanban.CreateInput{
		Title:    title,
		Column:   k.createCol,
		Priority: priorityFromLabel(k.createPriori),
		Deadline: deadlineString(k.createDead),
	}
	api := k.Services.Kanban
	k.createErr = ""
	a.RunTask(i18n.T("pages.kanban.task_create"), func(ctx context.Context, t *app.Task) error {
		if _, err := api.CreateTask(ctx, projectID, in); err != nil {
			k.createErr = kanbanErrorText(err)
			a.Toast(app.LevelDanger, i18n.T("pages.kanban.toast_create_fail"), kanbanErrorText(err))
			a.Notify()
			return nil
		}
		k.createOpen = false
		k.requestReload(a, projectID)
		a.Toast(app.LevelSuccess, i18n.T("pages.kanban.toast_created"), title)
		return nil
	})
}

// columnMenu is the SPEC-08 §6.3 rule 4 header menu.
func (k *Kanban) columnMenu(m *ui.Menu, col kanban.Column) {
	k.mu.Lock()
	columns := append([]kanban.Column(nil), k.columns...)
	archived := k.projectArchived
	k.mu.Unlock()
	if m.Item(i18n.T("pages.kanban.col_menu_rename")).ID("rename").Chosen() {
		k.colDialog, k.colName, k.colFrom, k.colErr = kanbanRenameColumn, col.Name, col.Name, ""
	}
	m.Separator()
	if m.Item(i18n.T("pages.kanban.col_menu_insert_left")).ID("left").Chosen() {
		k.columnCreateDialog(columns, col, true)
	}
	if m.Item(i18n.T("pages.kanban.col_menu_insert_right")).ID("right").Chosen() {
		k.columnCreateDialog(columns, col, false)
	}
	m.Separator()
	lastDone := col.IsDone && countDone(columns) <= 1
	if col.IsDone {
		item := m.Item(i18n.T("pages.kanban.col_menu_unset_done")).ID("undone").Checked(true)
		item.Disabled(lastDone || archived)
		if item.Chosen() {
			k.setColumnDone(k.Services.App, k.projectID(), col.Name, false)
		}
	} else {
		item := m.Item(i18n.T("pages.kanban.col_menu_set_done")).ID("done")
		item.Disabled(archived)
		if item.Chosen() {
			k.setColumnDone(k.Services.App, k.projectID(), col.Name, true)
		}
	}
	m.Separator()
	del := m.Item(i18n.T("pages.kanban.col_menu_delete")).ID("delete").Danger()
	del.Disabled(len(columns) <= 1 || archived)
	if del.Chosen() && len(columns) > 1 && !archived {
		k.colDialog, k.colName, k.colErr = kanbanDeleteColumn, col.Name, ""
		k.colTarget = defaultMigrateTarget(columns, col.Name)
	}
}

// columnCreateDialog prepares the create dialog for an insert left or right of
// one column. The service inserts after a named column, so a left insert of the
// first column creates at the end and then moves the column to index 0.
func (k *Kanban) columnCreateDialog(columns []kanban.Column, col kanban.Column, left bool) {
	k.colDialog, k.colErr, k.colMoveTo = kanbanCreateColumn, "", -1
	k.colName, k.colDone, k.colKind = "", false, ""
	if left {
		k.colAfter = ""
		names := make([]string, 0, len(columns))
		for _, c := range columns {
			names = append(names, c.Name)
		}
		k.colMoveTo = indexOfName(names, col.Name)
		return
	}
	k.colAfter = col.Name
}

// setColumnDone marks or clears the terminal flag.
func (k *Kanban) setColumnDone(a *app.App, projectID, name string, done bool) {
	api := k.Services.Kanban
	a.RunTask(i18n.T("pages.kanban.task_column"), func(ctx context.Context, t *app.Task) error {
		if _, err := api.SetColumnDone(ctx, projectID, name, done); err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.kanban.toast_column_fail"), kanbanErrorText(err))
			a.Notify()
			return nil
		}
		k.requestReload(a, projectID)
		return nil
	})
}

// moveColumn re-sequences the columns (SPEC-08 §6.4).
func (k *Kanban) moveColumn(a *app.App, projectID, name string, to int) {
	api := k.Services.Kanban
	a.RunTask(i18n.T("pages.kanban.task_column"), func(ctx context.Context, t *app.Task) error {
		if _, err := api.MoveColumn(ctx, projectID, name, to); err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.kanban.toast_column_fail"), kanbanErrorText(err))
			a.Notify()
			return nil
		}
		k.requestReload(a, projectID)
		return nil
	})
}

// columnTargetIndex is where a dragged column lands: the position the column
// it was dropped on holds once the dragged one is out of the order.
func (k *Kanban) columnTargetIndex(dragged, droppedOn string) int {
	k.mu.Lock()
	names := make([]string, 0, len(k.columns))
	for _, c := range k.columns {
		if c.Name != dragged {
			names = append(names, c.Name)
		}
	}
	k.mu.Unlock()
	return indexOfName(names, droppedOn)
}

// columnDialogs renders the create, rename and delete-column dialogs.
func (k *Kanban) columnDialogs(v *components.UI, a *app.App, projectID string) {
	if k.colDialog == kanbanNoDialog {
		return
	}
	switch k.colDialog {
	case kanbanCreateColumn:
		k.createColumnDialog(v, a, projectID)
	case kanbanRenameColumn:
		k.renameColumnDialog(v, a, projectID)
	case kanbanDeleteColumn:
		k.deleteColumnDialog(v, a, projectID)
	}
}

func (k *Kanban) createColumnDialog(v *components.UI, a *app.App, projectID string) {
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title: i18n.T("pages.kanban.column_create_title"),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				open = false
			}
			if v.Button(i18n.T("pages.kanban.column_create_submit"), components.ButtonOptions{
				Variant: components.VariantPrimary,
			}).Clicked() {
				k.createColumn(a, projectID)
			}
		},
	}, func() {
		field := v.Input(&k.colName, components.FieldOptions{
			Label:       i18n.T("pages.kanban.field_column_name"),
			Placeholder: i18n.T("pages.kanban.field_column_name_hint"),
			AutoFocus:   true,
			Error:       k.colErr,
		})
		if field.Submitted() {
			k.createColumn(a, projectID)
		}
		kindOptions := []string{
			i18n.T("pages.kanban.kind_backlog"),
			i18n.T("pages.kanban.kind_todo"),
			i18n.T("pages.kanban.kind_in_progress"),
			i18n.T("pages.kanban.kind_review"),
			i18n.T("pages.kanban.kind_done"),
			i18n.T("pages.kanban.kind_custom"),
		}
		if k.colKind == "" {
			k.colKind = kindOptions[len(kindOptions)-1]
		}
		v.Select(&k.colKind, components.SelectOptions{
			Label:   i18n.T("pages.kanban.field_column_kind"),
			Options: kindOptions,
		})
		after := k.colAfter
		if after == "" {
			after = i18n.T("pages.kanban.column_after_end")
		}
		opts := append([]string{i18n.T("pages.kanban.column_after_end")}, k.columnNames()...)
		if v.Select(&after, components.SelectOptions{
			Label:   i18n.T("pages.kanban.field_column_after"),
			Options: opts,
		}).Changed() {
			if after == i18n.T("pages.kanban.column_after_end") {
				k.colAfter, k.colMoveTo = "", -1
			} else {
				k.colAfter, k.colMoveTo = after, -1
			}
		}
		v.Switch(&k.colDone, i18n.T("pages.kanban.field_column_done"), components.ToggleOptions{})
	})
	if !open && k.colName == "" {
		k.colDialog = kanbanNoDialog
	}
}

func (k *Kanban) renameColumnDialog(v *components.UI, a *app.App, projectID string) {
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title: i18n.T("pages.kanban.column_rename_title"),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				open = false
			}
			if v.Button(i18n.T("pages.kanban.column_rename_submit"), components.ButtonOptions{
				Variant: components.VariantPrimary,
			}).Clicked() {
				k.renameColumn(a, projectID)
			}
		},
	}, func() {
		field := v.Input(&k.colName, components.FieldOptions{
			Label:     i18n.T("pages.kanban.field_column_name"),
			AutoFocus: true,
			Error:     k.colErr,
		})
		if field.Submitted() {
			k.renameColumn(a, projectID)
		}
	})
	if !open {
		k.colDialog, k.colName, k.colFrom = kanbanNoDialog, "", ""
	}
}

func (k *Kanban) deleteColumnDialog(v *components.UI, a *app.App, projectID string) {
	k.mu.Lock()
	columns := append([]kanban.Column(nil), k.columns...)
	k.mu.Unlock()
	targets := make([]string, 0, len(columns))
	targetDone := false
	for _, c := range withoutColumn(columns, k.colName) {
		targets = append(targets, c.Name)
		if c.Name == k.colTarget {
			targetDone = c.IsDone
		}
	}
	if len(targets) == 0 {
		k.colDialog = kanbanNoDialog
		return
	}
	if !kanbanContains(targets, k.colTarget) {
		k.colTarget = defaultMigrateTarget(columns, k.colName)
		for _, c := range columns {
			if c.Name == k.colTarget {
				targetDone = c.IsDone
			}
		}
	}
	count := k.columnTaskCount(k.colName)
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title: i18n.T("pages.kanban.column_delete_title"),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				open = false
			}
			if v.Button(i18n.T("pages.kanban.column_delete_submit"), components.ButtonOptions{
				Variant: components.VariantDanger,
			}).Clicked() {
				k.deleteColumn(a, projectID)
			}
		},
	}, func() {
		ui.Text(v.C, i18n.Tf("pages.kanban.column_delete_message", map[string]string{
			"count":  strconv.Itoa(count),
			"target": k.colTarget,
		})).FontSize(tokens.Typography.Body.Size)
		if v.Select(&k.colTarget, components.SelectOptions{
			Label:   i18n.T("pages.kanban.field_migrate_to"),
			Options: targets,
		}).Changed() {
			k.colErr = ""
		}
		if targetDone {
			v.Banner(components.BannerOptions{
				Tone:    components.Warning,
				Title:   i18n.T("pages.kanban.migrate_done_title"),
				Message: i18n.T("pages.kanban.migrate_done_message"),
			})
		}
		if k.colErr != "" {
			ui.Text(v.C, k.colErr).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
	if !open {
		k.colDialog, k.colName, k.colErr = kanbanNoDialog, "", ""
	}
}

// createColumn writes the new column, then re-positions it when it was
// inserted to the left of a column.
func (k *Kanban) createColumn(a *app.App, projectID string) {
	name := strings.TrimSpace(k.colName)
	if name == "" {
		k.colErr = i18n.T("pages.kanban.column_name_required")
		return
	}
	in := kanban.CreateColumnInput{Name: name, Kind: columnKindFromLabel(k.colKind), IsDone: k.colDone}
	if k.colAfter != "" {
		after := k.colAfter
		in.After = &after
	}
	moveTo := k.colMoveTo
	api := k.Services.Kanban
	k.colErr = ""
	a.RunTask(i18n.T("pages.kanban.task_column"), func(ctx context.Context, t *app.Task) error {
		if _, err := api.CreateColumn(ctx, projectID, in); err != nil {
			k.colErr = kanbanErrorText(err)
			a.Notify()
			return nil
		}
		if moveTo >= 0 {
			if _, err := api.MoveColumn(ctx, projectID, name, moveTo); err != nil {
				a.Toast(app.LevelDanger, i18n.T("pages.kanban.toast_column_fail"), kanbanErrorText(err))
			}
		}
		k.colDialog, k.colName, k.colDone, k.colAfter, k.colMoveTo = kanbanNoDialog, "", false, "", -1
		k.colKind = ""
		k.requestReload(a, projectID)
		a.Toast(app.LevelSuccess, i18n.T("pages.kanban.toast_column_created"), name)
		return nil
	})
}

// renameColumn renames the column and cascades its tasks.
func (k *Kanban) renameColumn(a *app.App, projectID string) {
	name := strings.TrimSpace(k.colName)
	if name == "" {
		k.colErr = i18n.T("pages.kanban.column_name_required")
		return
	}
	from := k.colFrom
	api := k.Services.Kanban
	k.colErr = ""
	a.RunTask(i18n.T("pages.kanban.task_column"), func(ctx context.Context, t *app.Task) error {
		if _, err := api.RenameColumn(ctx, projectID, from, name); err != nil {
			k.colErr = kanbanErrorText(err)
			a.Notify()
			return nil
		}
		k.colDialog, k.colName, k.colFrom = kanbanNoDialog, "", ""
		k.requestReload(a, projectID)
		return nil
	})
}

// deleteColumn deletes the column after migrating its tasks.
func (k *Kanban) deleteColumn(a *app.App, projectID string) {
	if k.colName == "" || k.colTarget == "" {
		return
	}
	api := k.Services.Kanban
	name, target := k.colName, k.colTarget
	k.colErr = ""
	a.RunTask(i18n.T("pages.kanban.task_column"), func(ctx context.Context, t *app.Task) error {
		if err := api.DeleteColumn(ctx, projectID, name, target); err != nil {
			k.colErr = kanbanErrorText(err)
			a.Notify()
			return nil
		}
		k.colDialog, k.colName, k.colTarget = kanbanNoDialog, "", ""
		k.requestReload(a, projectID)
		a.Toast(app.LevelSuccess, i18n.T("pages.kanban.toast_column_deleted"), name)
		return nil
	})
}

// listView renders the sortable table of the open project. An empty
// projection shows the empty state: mygo's list reconciles the shared
// ListState's previous rows against the new table, and a zero-row Table would
// still ask the Key closure for row 0 of an empty slice (SPEC-08 §6.6).
func (k *Kanban) listView(v *components.UI, a *app.App, projectID string) {
	k.mu.Lock()
	rows := append([]kanban.TaskSummary(nil), k.list...)
	k.mu.Unlock()
	if len(rows) == 0 {
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.Kanban,
			Title:  i18n.T("pages.kanban.list_empty_title"),
			Reason: i18n.T("pages.kanban.list_empty_reason"),
		})
		return
	}
	k.taskTable(v, a, projectID, rows, k.table, false)
}

// blockedView renders the blocked projection; the empty state says nothing is
// blocked (SPEC-08 §5.7).
func (k *Kanban) blockedView(v *components.UI, a *app.App, projectID string) {
	k.mu.Lock()
	rows := append([]kanban.TaskSummary(nil), k.blocked...)
	k.mu.Unlock()
	if len(rows) == 0 {
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.Success,
			Title:  i18n.T("pages.kanban.blocked_empty_title"),
			Reason: i18n.T("pages.kanban.blocked_empty_reason"),
		})
		return
	}
	k.taskTable(v, a, projectID, rows, k.table, true)
}

// archiveView lists archived tasks with the unarchive action. It is not the
// recycle bin: archived tasks never enter the trash (SPEC-08 §6.2).
func (k *Kanban) archiveView(v *components.UI, a *app.App, projectID string) {
	k.mu.Lock()
	rows := append([]kanban.TaskSummary(nil), k.archive...)
	k.mu.Unlock()
	if len(rows) == 0 {
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.FileText,
			Title:  i18n.T("pages.kanban.archive_empty_title"),
			Reason: i18n.T("pages.kanban.archive_empty_reason"),
		})
		return
	}
	sorted := sortSummaries(rows, k.archiveTbl.Sort)
	table := v.Table(components.TableOptions{
		Columns: []components.Column{
			{Title: i18n.T("pages.kanban.col_title"), ID: "title", Sortable: true},
			{Title: i18n.T("pages.kanban.col_column"), ID: "column", Width: 140, Sortable: true},
			{Title: i18n.T("pages.kanban.col_archived"), ID: "archived", Width: 150, Sortable: true},
			{Title: i18n.T("pages.kanban.col_actions"), Width: 110, Align: ui.End},
		},
		Rows:  len(sorted),
		State: k.archiveTbl,
		Key:   func(i int) any { return sorted[i].ID },
		Cell: func(row, col int) {
			task := sorted[row]
			switch col {
			case 0:
				ui.Text(v.C, kanbanTitle(task.Title)).SingleLine()
			case 1:
				ui.Text(v.C, task.Column).SingleLine()
			case 2:
				ui.Text(v.C, archivedAt(task)).FontFeatures("tnum")
			case 3:
				if v.Button(i18n.T("pages.kanban.btn_unarchive"), components.ButtonOptions{
					Variant:  components.VariantGhost,
					Size:     components.SizeSmall,
					Disabled: k.readOnly(),
				}).Clicked() {
					k.unarchiveTask(a, projectID, task.ID)
				}
			}
		},
	})
	table.Height(kanbanTableHeight(len(sorted)))
}

// taskTable draws one table of tasks for the list and blocked views.
func (k *Kanban) taskTable(v *components.UI, a *app.App, projectID string, rows []kanban.TaskSummary, state *ui.ListState, fixedBlocked bool) {
	sorted := sortSummaries(rows, state.Sort)
	open := func(task kanban.TaskSummary) { k.openTask(a, projectID, task.ID) }
	table := v.Table(components.TableOptions{
		Columns: []components.Column{
			{Title: i18n.T("pages.kanban.col_title"), ID: "title", Sortable: true},
			{Title: i18n.T("pages.kanban.col_column"), ID: "column", Width: 130, Sortable: true},
			{Title: i18n.T("pages.kanban.col_priority"), ID: "priority", Width: 100, Sortable: true},
			{Title: i18n.T("pages.kanban.col_deadline"), ID: "deadline", Width: 120, Sortable: true},
			{Title: i18n.T("pages.kanban.col_labels"), ID: "labels", Width: 160},
			{Title: i18n.T("pages.kanban.col_subtasks"), ID: "subtasks", Width: 90, Sortable: true},
			{Title: i18n.T("pages.kanban.col_blocked"), ID: "blocked", Width: 80, Sortable: true},
			{Title: i18n.T("pages.kanban.col_updated"), ID: "updated", Width: 150, Sortable: true},
		},
		Rows:  len(sorted),
		State: state,
		Key:   func(i int) any { return sorted[i].ID },
		Cell: func(row, col int) {
			task := sorted[row]
			switch col {
			case 0:
				if ui.Text(v.C, kanbanTitle(task.Title)).SingleLine().FillWidth().RowClicked() {
					open(task)
				}
			case 1:
				if ui.Text(v.C, task.Column).SingleLine().RowClicked() {
					open(task)
				}
			case 2:
				// One dot for every task, blue when the priority is unset, so
				// the column keeps one shape and the color keeps one meaning.
				cell := ui.Row(v.C).Gap(tokens.Space1).AlignItems(ui.Center)
				cell.Children(func() { priorityDot(v, task.Priority) })
				if cell.RowClicked() {
					open(task)
				}
			case 3:
				if task.Deadline == nil || *task.Deadline == "" {
					ui.Text(v.C, i18n.T("pages.kanban.no_deadline")).TextColor(mygo.Color(v.T.Palette.TextMuted))
					return
				}
				text := ui.Text(v.C, *task.Deadline).FontFeatures("tnum")
				if kanbanOverdue(*task.Deadline) {
					text.TextColor(mygo.Color(v.T.Palette.StatusDanger))
				}
				if text.RowClicked() {
					open(task)
				}
			case 4:
				ui.Row(v.C).Gap(tokens.Space1).Children(func() {
					for _, label := range task.Labels {
						v.Badge(label, components.Neutral)
					}
				})
			case 5:
				if task.SubtaskTotal == 0 {
					ui.Text(v.C, "—").TextColor(mygo.Color(v.T.Palette.TextMuted))
					return
				}
				if ui.Text(v.C, i18n.Tf("pages.kanban.count_pair", map[string]string{
					"done":  strconv.Itoa(task.SubtaskDone),
					"total": strconv.Itoa(task.SubtaskTotal),
				})).FontFeatures("tnum").RowClicked() {
					open(task)
				}
			case 6:
				if task.Blocked {
					v.StatusIndicator(components.Danger, i18n.T("pages.kanban.blocked_yes"))
				} else if !fixedBlocked {
					ui.Text(v.C, "—").TextColor(mygo.Color(v.T.Palette.TextMuted))
				}
			case 7:
				if ui.Text(v.C, task.UpdatedAt.Local().Format("2006-01-02 15:04")).FontFeatures("tnum").RowClicked() {
					open(task)
				}
			}
		},
	})
	table.Height(kanbanTableHeight(len(sorted)))
}

// keys installs the page shortcuts: n opens a new task, / focuses the filter
// (SPEC-08 §6.3 rule 7). A focused text input handles the key first, so an
// IME composition never triggers them.
func (k *Kanban) keys(v *components.UI) {
	if k.readOnly() {
		return
	}
	if v.C.Shortcut(0, ui.KeyN) {
		k.startCreate("")
	}
	if v.C.Shortcut(0, ui.KeyEscape) && k.filterOpen {
		k.filterOpen = false
	}
	if v.C.Shortcut(0, ui.KeySlash) {
		k.filterOpen = true
		k.focusFilter = true
	}
}

// tickDetail switches project state and drives the inspector detail.
func (k *Kanban) tickDetail(a *app.App, projectID string) {
	k.mu.Lock()
	switched := k.project != projectID
	if switched {
		k.project = projectID
		k.board = kanban.BoardView{}
		k.columns = nil
		k.list, k.blocked, k.archive = nil, nil, nil
		k.loaded = false
		k.dirty = true
		k.problems = nil
	}
	k.mu.Unlock()
	if switched && k.detail != nil {
		k.detail.close(a)
	}
	if k.detail != nil {
		k.detail.tick(k, a, projectID)
	}
}

// requestReload asks the next frame for a reload and repaints.
func (k *Kanban) requestReload(a *app.App, projectID string) {
	k.mu.Lock()
	k.dirty = true
	k.mu.Unlock()
	if projectID != "" {
		k.reload(a, projectID)
	}
}

// load reads the visible projection once per project, again after a write and
// when the archive switch changes.
func (k *Kanban) load(a *app.App, projectID string) {
	k.mu.Lock()
	need := !k.loading && (!k.loaded || k.project != projectID || k.dirty)
	k.mu.Unlock()
	if need {
		k.reload(a, projectID)
	}
}

// reload reloads the projection the chosen view needs.
func (k *Kanban) reload(a *app.App, projectID string) {
	api := k.Services.Kanban
	if api == nil {
		return
	}
	k.mu.Lock()
	if k.loading {
		k.mu.Unlock()
		return
	}
	k.loading = true
	k.dirty = false
	k.mu.Unlock()

	view, archive := k.view, k.showArchive
	boardQuery := kanban.BoardQuery{
		Priority: k.filter.prioritiesList(),
		Labels:   k.filter.labelsList(),
		Blocked:  k.filter.blockedValue(),
		Text:     strings.TrimSpace(k.filter.text),
	}
	query := kanban.Query{
		OnlyTopLevel: true,
		Priority:     boardQuery.Priority,
		Labels:       boardQuery.Labels,
		Blocked:      boardQuery.Blocked,
		Text:         boardQuery.Text,
		Sort:         kanban.SortUpdatedDesc,
		Limit:        kanbanTaskLimit,
	}

	a.RunTask(i18n.T("pages.kanban.task_load"), func(ctx context.Context, t *app.Task) error {
		var problems []string
		board, err := api.Board(ctx, projectID, boardQuery)
		if err != nil {
			problems = append(problems, kanbanErrorText(err))
		}
		cols, cerr := api.Columns(ctx, projectID)
		if cerr != nil {
			problems = append(problems, kanbanErrorText(cerr))
		}
		var list, blocked, archived []kanban.TaskSummary
		if view == kanbanListView || view == kanbanBlockedView {
			if page, lerr := api.List(ctx, projectID, query); lerr != nil {
				problems = append(problems, kanbanErrorText(lerr))
			} else {
				list = page.Items
			}
		}
		if view == kanbanBlockedView {
			if page, berr := api.Blocked(ctx, projectID); berr != nil {
				problems = append(problems, kanbanErrorText(berr))
			} else {
				blocked = page.Items
			}
		}
		if archive {
			if page, aerr := api.ListArchived(ctx, projectID, kanban.Query{Sort: kanban.SortUpdatedDesc, Limit: kanbanTaskLimit}); aerr != nil {
				problems = append(problems, kanbanErrorText(aerr))
			} else {
				archived = page.Items
			}
		}
		k.mu.Lock()
		k.loading = false
		k.loaded = err == nil
		if err == nil {
			k.board = board
		}
		if cerr == nil {
			k.columns = cols
		}
		k.list, k.blocked, k.archive = list, blocked, archived
		k.problems = problems
		k.loadedView, k.loadedArchive = view, archive
		k.mu.Unlock()
		a.Notify()
		return nil
	})
}

// columnNames lists the cached column names in display order.
func (k *Kanban) columnNames() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]string, 0, len(k.columns))
	for _, c := range k.columns {
		out = append(out, c.Name)
	}
	return out
}

// columnTaskCount counts the cards the board holds for one column.
func (k *Kanban) columnTaskCount(name string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, c := range k.board.Columns {
		if c.Column.Name == name {
			return len(c.Tasks)
		}
	}
	return 0
}

// projectID returns the open project id.
func (k *Kanban) projectID() string {
	if k.Services != nil && k.Services.App != nil {
		if p := k.Services.App.Project(); p != nil {
			return p.ID
		}
	}
	return ""
}

// openTask opens the inspector detail of one task.
func (k *Kanban) openTask(a *app.App, projectID, taskID string) {
	if k.detail == nil {
		k.detail = &taskDetail{}
	}
	k.detail.open(a, k, projectID, taskID)
}

// archiveTask archives one task and closes its detail.
func (k *Kanban) archiveTask(a *app.App, projectID, taskID string) {
	api := k.Services.Kanban
	a.RunTask(i18n.T("pages.kanban.task_archive"), func(ctx context.Context, t *app.Task) error {
		if _, err := api.ArchiveTask(ctx, projectID, taskID); err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.kanban.toast_archive_fail"), kanbanErrorText(err))
			a.Notify()
			return nil
		}
		if k.detail != nil {
			k.detail.close(a)
		}
		k.requestReload(a, projectID)
		a.Toast(app.LevelSuccess, i18n.T("pages.kanban.toast_archived"), "")
		return nil
	})
}

// unarchiveTask returns one task to its column and position.
func (k *Kanban) unarchiveTask(a *app.App, projectID, taskID string) {
	api := k.Services.Kanban
	a.RunTask(i18n.T("pages.kanban.task_unarchive"), func(ctx context.Context, t *app.Task) error {
		if _, err := api.UnarchiveTask(ctx, projectID, taskID); err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.kanban.toast_unarchive_fail"), kanbanErrorText(err))
			a.Notify()
			return nil
		}
		k.requestReload(a, projectID)
		a.Toast(app.LevelSuccess, i18n.T("pages.kanban.toast_unarchived"), "")
		return nil
	})
}

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

// priorityLabel is the localised name of a priority; the stored value is never
// shown (SPEC-08 §4.3).
func priorityLabel(p kanban.Priority) string {
	switch p {
	case kanban.PriorityHigh:
		return i18n.T("pages.kanban.priority_high")
	case kanban.PriorityMedium:
		return i18n.T("pages.kanban.priority_medium")
	case kanban.PriorityLow:
		return i18n.T("pages.kanban.priority_low")
	}
	return i18n.T("pages.kanban.priority_none")
}

// priorityFromLabel maps a select label back to the stored value.
func priorityFromLabel(label string) kanban.Priority {
	switch label {
	case i18n.T("pages.kanban.priority_high"):
		return kanban.PriorityHigh
	case i18n.T("pages.kanban.priority_medium"):
		return kanban.PriorityMedium
	case i18n.T("pages.kanban.priority_low"):
		return kanban.PriorityLow
	}
	return kanban.PriorityNone
}

// priorityColor is the card bar and the dot color: the theme resolves one step
// per appearance, so a priority reads on the light surface and on the dark one
// without the page carrying a color of its own (ui_plan.md §2.3). An unset
// priority is blue; a value the theme does not know is the secondary color.
func priorityColor(v *components.UI, p kanban.Priority) tokens.Color {
	return v.T.Priority(string(p))
}

// priorityDot paints a priority as a small colored dot. The name is the dot's
// tooltip and its accessible label, so the meaning is never carried by color
// alone (ui_plan.md §2.3).
func priorityDot(v *components.UI, p kanban.Priority) ui.Element {
	name := priorityLabel(p)
	dot := ui.Box(v.C).
		Size(tokens.Space2, tokens.Space2).
		Radius(tokens.Space1).
		Background(mygo.Color(priorityColor(v, p))).
		Role(ui.RoleImage).
		Label(name)
	return v.Tooltip(dot, name)
}

// kanbanTitle truncates by runes, then lets the layout cut the second line.
func kanbanTitle(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= kanbanTitleRunes {
		return string(r)
	}
	return string(r[:kanbanTitleRunes]) + "…"
}

// kanbanOverdue reports whether a YYYY-MM-DD deadline is before today.
func kanbanOverdue(deadline string) bool {
	d, err := time.Parse("2006-01-02", deadline)
	if err != nil {
		return false
	}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	return d.Before(today)
}

// deadlineString converts a picked day back to the stored YYYY-MM-DD form.
func deadlineString(d *time.Time) *string {
	if d == nil || d.IsZero() {
		return nil
	}
	s := d.Format("2006-01-02")
	return &s
}

// deadlineTime parses the stored form into a picker value; the picker edits a
// real calendar day, so a zero value is a valid "unset" input.
func deadlineTime(s *string) *time.Time {
	if s == nil || *s == "" {
		return nil
	}
	d, err := time.ParseInLocation("2006-01-02", *s, time.Local)
	if err != nil {
		return nil
	}
	return &d
}

// deadlineField renders the pure-date picker with its clear button. An unset
// deadline is a button that seeds today, so the picker never shows 1/1/1.
func deadlineField(v *components.UI, date **time.Time, label string) bool {
	changed := false
	ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.End).Children(func() {
		if *date == nil {
			if v.Button(i18n.T("pages.kanban.btn_set_date"), components.ButtonOptions{
				Variant: components.VariantGhost,
				Size:    components.SizeSmall,
				Icon:    icons.Clock,
				Label:   label,
				Align:   ui.End,
			}).Clicked() {
				now := time.Now()
				today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
				*date = &today
				changed = true
			}
			return
		}
		if ui.DateInput(v.C, *date).Label(label).Changed() {
			changed = true
		}
		if v.Button(i18n.T("pages.kanban.btn_clear_date"), components.ButtonOptions{
			Variant: components.VariantGhost,
			Size:    components.SizeSmall,
			Icon:    icons.Close,
			Label:   label,
			Align:   ui.End,
		}).Clicked() {
			*date = nil
			changed = true
		}
	})
	return changed
}

// columnKindFromLabel maps a localised kind label back to the stored value.
func columnKindFromLabel(label string) string {
	switch label {
	case i18n.T("pages.kanban.kind_backlog"):
		return "backlog"
	case i18n.T("pages.kanban.kind_todo"):
		return "todo"
	case i18n.T("pages.kanban.kind_in_progress"):
		return "in_progress"
	case i18n.T("pages.kanban.kind_review"):
		return "review"
	case i18n.T("pages.kanban.kind_done"):
		return "done"
	}
	return "custom"
}

// countDone counts the terminal columns.
func countDone(cols []kanban.Column) int {
	n := 0
	for _, c := range cols {
		if c.IsDone {
			n++
		}
	}
	return n
}

// withoutColumn returns the columns except the named one.
func withoutColumn(cols []kanban.Column, name string) []kanban.Column {
	out := make([]kanban.Column, 0, len(cols))
	for _, c := range cols {
		if c.Name != name {
			out = append(out, c)
		}
	}
	return out
}

// indexOfName returns the index of name, or 0 when it is absent.
func indexOfName(names []string, name string) int {
	for i, n := range names {
		if n == name {
			return i
		}
	}
	return 0
}

func kanbanContains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// defaultMigrateTarget is the leftmost non-terminal column other than the one
// being deleted (SPEC-08 §6.3 rule 4).
func defaultMigrateTarget(cols []kanban.Column, deleted string) string {
	for _, c := range cols {
		if c.Name != deleted && !c.IsDone {
			return c.Name
		}
	}
	for _, c := range cols {
		if c.Name != deleted {
			return c.Name
		}
	}
	return ""
}

// sortSummaries orders the list locally for its sortable headers.
func sortSummaries(rows []kanban.TaskSummary, order *ui.SortOrder) []kanban.TaskSummary {
	out := append([]kanban.TaskSummary(nil), rows...)
	if order == nil || order.Column == "" {
		return out
	}
	rank := map[kanban.Priority]int{
		kanban.PriorityHigh: 3, kanban.PriorityMedium: 2, kanban.PriorityLow: 1, kanban.PriorityNone: 0,
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		cmp := 0
		switch order.Column {
		case "column":
			cmp = strings.Compare(strings.ToLower(a.Column), strings.ToLower(b.Column))
		case "priority":
			cmp = rank[a.Priority] - rank[b.Priority]
		case "deadline":
			da, db := "", ""
			if a.Deadline != nil {
				da = *a.Deadline
			}
			if b.Deadline != nil {
				db = *b.Deadline
			}
			// Rows without a deadline come last in either direction.
			switch {
			case da == "" && db == "":
				cmp = 0
			case da == "":
				return false
			case db == "":
				return true
			default:
				cmp = strings.Compare(da, db)
			}
		case "subtasks":
			cmp = a.SubtaskTotal - b.SubtaskTotal
		case "blocked":
			cmp = boolCmp(a.Blocked, b.Blocked)
		case "updated":
			cmp = a.UpdatedAt.Compare(b.UpdatedAt)
		case "archived":
			cmp = strings.Compare(archivedSortKey(a), archivedSortKey(b))
		default:
			cmp = strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
		}
		if order.Descending {
			return cmp > 0
		}
		return cmp < 0
	})
	return out
}

// boolCmp orders false before true.
func boolCmp(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	}
	return 1
}

func archivedSortKey(t kanban.TaskSummary) string {
	if t.ArchivedAt == nil {
		return ""
	}
	return t.ArchivedAt.Format(time.RFC3339Nano)
}

func archivedAt(t kanban.TaskSummary) string {
	if t.ArchivedAt == nil {
		return ""
	}
	return t.ArchivedAt.Local().Format("2006-01-02 15:04")
}

// kanbanTableHeight gives a table room for its rows inside its own scroll box.
func kanbanTableHeight(rows int) float32 {
	n := rows
	if n > kanbanColumnRows {
		n = kanbanColumnRows
	}
	if n < 1 {
		n = 1
	}
	return float32(n)*44 + 40
}

// isKanbanError matches one of the module's sentinels.
func isKanbanError(err error, target error) bool { return errors.Is(err, target) }

// kanbanErrorText maps the module's sentinels to readable text and falls back
// to the error itself.
func kanbanErrorText(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case isKanbanError(err, kanban.ErrProjectArchived):
		return i18n.T("pages.kanban.err_project_archived")
	case isKanbanError(err, kanban.ErrTaskArchived):
		return i18n.T("pages.kanban.err_task_archived")
	case isKanbanError(err, kanban.ErrStaleRevision):
		return i18n.T("pages.kanban.err_stale")
	case isKanbanError(err, kanban.ErrLastDoneColumn):
		return i18n.T("pages.kanban.err_last_done")
	case isKanbanError(err, kanban.ErrLastColumn):
		return i18n.T("pages.kanban.err_last_column")
	case isKanbanError(err, kanban.ErrDuplicateColumnName):
		return i18n.T("pages.kanban.err_duplicate_column")
	case isKanbanError(err, kanban.ErrColumnNotEmpty):
		return i18n.T("pages.kanban.err_column_not_empty")
	case isKanbanError(err, kanban.ErrColumnNotFound):
		return i18n.T("pages.kanban.err_column_not_found")
	case isKanbanError(err, kanban.ErrTaskNotFound):
		return i18n.T("pages.kanban.err_task_not_found")
	case isKanbanError(err, kanban.ErrSubtaskOnSubtask):
		return i18n.T("pages.kanban.err_subtask_nested")
	case isKanbanError(err, kanban.ErrTaskLimitExceeded):
		return i18n.T("pages.kanban.err_task_limit")
	case isKanbanError(err, kanban.ErrInvalidInput):
		return i18n.T("pages.kanban.err_invalid")
	}
	return errtext.Text(err)
}
