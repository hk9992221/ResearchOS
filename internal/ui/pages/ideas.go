package pages

// The Ideas page is the SPEC-06 module of the shell: the Idea list of the open
// project, in a sortable table or grouped by status, and the Idea detail page
// that opens on ports.RouteIdea, with its overview, evidence, relations and
// history tabs.
//
// Every database read and write runs in a managed task, so a frame only
// renders cached state; the list and the detail share the generation the
// detail bumps after a write, so a returning list reloads instead of showing a
// stale row. Status labels are keyed by the persisted Status value, never by
// ideas.Status.DisplayName(), whose text is not localised (SPEC-06 §6.8).

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"researchos/internal/i18n"
	"researchos/internal/ideas"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/errtext"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// ideasListLimit is how many Ideas the list holds. The service pages at most
// 200 rows per call; a project's Idea list is browsed rather than paged, and
// the search field narrows it on the server instead of scrolling.
const ideasListLimit = 200

// ideasListRows is how many table rows fit before the table scrolls inside its
// own box (design system row height 44px).
const ideasListRows = 12

// ideasCard widths keep a status-group card readable while the row wraps into
// as many columns as the window has room for.
const (
	ideasCardMinWidth float32 = 220
	ideasCardMaxWidth float32 = 340
)

// ideasSearchDelay debounces the server search: a title or description query
// is sent once typing pauses, so a word does not run one task per letter.
const ideasSearchDelay = 250 * time.Millisecond

// Field limits of SPEC-06 §4 that the page enforces before a call so the
// inline error names the rule instead of an opaque service code.
const (
	ideasMaxTitleRunes       = 200
	ideasMaxInstructionRunes = 500
)

// ideasSidebarWidth is the width of the detail page's sidebar card.
const ideasSidebarWidth float32 = 320

// The two views of the list.
const (
	ideasViewList = iota
	ideasViewGroups
)

// ideasSortKeys are the orders the toolbar menu offers; the value is the
// service's ListIdeasQuery.Sort key.
var ideasSortKeys = []string{ideas.SortUpdatedDesc, ideas.SortCreatedDesc, ideas.SortTitleAsc}

// ideasSortLabels are the i18n keys of ideasSortKeys, in the same order.
var ideasSortLabels = []string{
	"pages.ideas.sort_updated",
	"pages.ideas.sort_created",
	"pages.ideas.sort_title",
}

// IdeasAPI is the part of SPEC-06 the Ideas pages use: the Idea lifecycle, the
// evidence panel, the relation set, the derivation chain, the AI evidence
// skill and the history timeline. The composition root binds it to the Ideas
// service, always as ActorUser: the page is the user path, while MCP reaches
// the same methods with ActorMCP. Every call names the open project, because
// one adapter serves the workspace; a nil Services.Ideas means the module was
// never assembled, and the page says so instead of faking an empty list.
type IdeasAPI interface {
	CreateIdea(ctx context.Context, projectID string, in ideas.CreateIdeaInput) (*ideas.Idea, error)
	UpdateIdea(ctx context.Context, projectID string, in ideas.UpdateIdeaInput) (*ideas.Idea, error)
	GetIdea(ctx context.Context, projectID, ideaID string) (*ideas.IdeaDetail, error)
	ListIdeas(ctx context.Context, projectID string, q ideas.ListIdeasQuery) (ideas.Page[ideas.IdeaSummary], error)
	DeleteIdea(ctx context.Context, projectID, ideaID string, actor ports.Actor) error
	ChangeStatus(ctx context.Context, projectID string, in ideas.ChangeStatusInput) (*ideas.Idea, error)
	UpdateRejectionReason(ctx context.Context, projectID string, in ideas.UpdateRejectionReasonInput) (*ideas.Idea, error)
	LinkEntity(ctx context.Context, projectID string, in ideas.LinkInput) error
	UnlinkEntity(ctx context.Context, projectID string, in ideas.LinkInput) error
	ListRelations(ctx context.Context, projectID, ideaID string) (ideas.RelationSet, error)
	DeriveIdea(ctx context.Context, projectID string, in ideas.DeriveIdeaInput) (*ideas.Idea, error)
	AddEvidence(ctx context.Context, projectID string, in ideas.AddEvidenceInput) (*ideas.Evidence, error)
	UpdateEvidence(ctx context.Context, projectID string, in ideas.UpdateEvidenceInput) (*ideas.Evidence, error)
	ConfirmEvidence(ctx context.Context, projectID string, in ideas.ConfirmEvidenceInput) (ideas.ConfirmEvidenceResult, error)
	DiscardEvidence(ctx context.Context, projectID string, in ideas.DiscardEvidenceInput) (ideas.DiscardEvidenceResult, error)
	DeleteEvidence(ctx context.Context, projectID, evidenceID string, actor ports.Actor) error
	ListEvidence(ctx context.Context, projectID string, q ideas.ListEvidenceQuery) (ideas.Page[ideas.EvidenceView], error)
	GenerateEvidenceDrafts(ctx context.Context, projectID string, in ideas.GenerateEvidenceInput) (*ideas.GenerateEvidenceResult, error)
	History(ctx context.Context, projectID string, q ideas.HistoryQuery) (ideas.Page[ideas.HistoryEntry], error)
	// ResolveSource previews one evidence source, so the pickers can say how
	// much text a choice carries before a generation spends tokens on it.
	ResolveSource(ctx context.Context, projectID string, ref ideas.SourceRef) (ideas.ResolvedSource, error)
	// LinkCandidates lists the live papers, notes, experiments and runs of the
	// project that an Idea can be linked to, filtered by title text. The type
	// is one of ideas.TargetPaper/TargetNote/TargetExperiment or
	// string(ideas.SourceRun).
	LinkCandidates(ctx context.Context, projectID, targetType, text string) ([]ideas.EntityRef, error)
	// PaperURL resolves the Zotero selection URI of a paper, so a click on a
	// paper relation opens it in the library that owns it.
	PaperURL(ctx context.Context, projectID, paperID string) (string, error)
	// AIAvailable reports whether a chat provider is assigned for this workspace
	// (the local check, no network). A false value disables the evidence skill
	// with a pointer to Settings (SPEC-06 §6.6, AC-21).
	AIAvailable() bool
}

// ideasShared is the session state the list and the detail share: the detail
// bumps the generation after it writes, so the list reloads instead of
// rendering the row it just changed.
type ideasShared struct {
	mu  sync.Mutex
	gen int
}

func (s *ideasShared) bump() {
	s.mu.Lock()
	s.gen++
	s.mu.Unlock()
}

func (s *ideasShared) version() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

// ideaDrag is the value a list card drags between status groups. The typed
// transfer keeps a card from landing on an unrelated drop target.
type ideaDrag struct {
	ID     string
	Title  string
	Status ideas.Status
}

// ideaReasonForm is the state of the rejection-reason dialog. §4.2.2 requires
// a non-blank trimmed reason before a transition to Rejected; the dialog keeps
// the drop or the menu choice pending until one is given.
type ideaReasonForm struct {
	open   bool
	ideaID string
	title  string
	from   ideas.Status
	to     ideas.Status
	text   string
	errTxt string
	busy   bool
	done   bool
}

// Ideas renders the Idea list of the open project.
type Ideas struct {
	Services *Services
	shared   *ideasShared

	mu       sync.Mutex
	loading  bool
	loaded   bool
	project  string
	version  int
	list     []ideas.IdeaSummary
	total    int
	problems []string
	action   string
	creating bool
	createEr string
	createOK bool
	changing bool
	reason   ideaReasonForm

	// The applied filter is what the cached list was read with; the frame
	// compares it with the controls and reloads after the search debounce.
	appliedQuery    string
	appliedStatuses string
	appliedSort     string
	lastQuery       string
	queryAt         time.Time
	searchPending   bool

	// Frame-only state: the toolbar choices persist for the session, and the
	// dialogs are opened and closed by the frame that renders them.
	query      string
	statuses   []ideas.Status
	sort       string
	view       int
	table      *ui.ListState
	rows       []ideas.IdeaSummary
	createOpen bool
	createTitl string
	createDesc string
}

// Page renders one frame of the Idea list.
func (n *Ideas) Page(v *components.UI, r ports.Route) {
	a := n.Services.App
	if a == nil {
		return
	}
	if n.Services.Ideas == nil {
		n.unavailable(v)
		return
	}
	project := a.Project()
	if project == nil {
		n.needsProject(v, a)
		return
	}
	n.prepare()
	readOnly := project.ArchivedAt != nil
	n.tickLoad(v, a, project.ID)
	list := n.snapshot()
	n.tickDialogs()
	// An unfiltered empty list shows its create action in the empty state, so
	// the header does not repeat the same primary button.
	empty := len(list) == 0 && !n.blank() && !readOnly &&
		strings.TrimSpace(n.query) == "" && len(n.statuses) == 0

	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.ideas.title"),
			Description: i18n.Tf("pages.ideas.description", map[string]string{"project": project.Name}),
			Actions: func() {
				if empty {
					return
				}
				if v.Button(i18n.T("pages.ideas.btn_create"), components.ButtonOptions{
					Variant:  components.VariantPrimary,
					Icon:     icons.Plus,
					Disabled: readOnly,
					Tooltip:  readOnlyTooltip(readOnly),
				}).Clicked() {
					n.createOpen = true
				}
			},
		})
		if readOnly {
			v.Banner(components.BannerOptions{
				Tone:    components.Warning,
				Title:   i18n.T("pages.ideas.readonly_title"),
				Message: i18n.T("pages.ideas.readonly_message"),
			})
		}
		n.banners(v)
		n.toolbar(v)
		switch {
		case len(list) == 0 && n.blank():
			v.Skeleton(components.SkeletonOptions{Lines: 5})
		case len(list) == 0 && (strings.TrimSpace(n.query) != "" || len(n.statuses) > 0):
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.Search,
				Title:  i18n.T("pages.ideas.no_results_title"),
				Reason: i18n.T("pages.ideas.no_results_reason"),
			})
		case len(list) == 0:
			v.EmptyState(components.EmptyStateOptions{
				Icon:        icons.Ideas,
				Title:       i18n.T("pages.ideas.empty_title"),
				Reason:      i18n.T("pages.ideas.empty_reason"),
				ActionLabel: i18n.T("pages.ideas.btn_create"),
				OnAction:    func() { n.createOpen = !readOnly },
			})
		case n.view == ideasViewGroups:
			n.groups(v, a, project.ID, list, readOnly)
		default:
			n.tableView(v, a, project.ID, list)
		}
		n.createDialog(v, a, project.ID, readOnly)
		n.reasonDialog(v, a, project.ID)
	}})
}

// unavailable is the honest state of a workspace whose Ideas module was never
// assembled.
func (n *Ideas) unavailable(v *components.UI) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{Title: i18n.T("pages.ideas.title")})
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.Ideas,
			Title:  i18n.T("pages.ideas.disabled_title"),
			Reason: i18n.T("pages.ideas.disabled_reason"),
		})
	}})
}

// needsProject states the precondition: Ideas belong to a project.
func (n *Ideas) needsProject(v *components.UI, a *app.App) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.ideas.title"),
			Description: i18n.T("pages.ideas.needs_project_desc"),
		})
		v.EmptyState(components.EmptyStateOptions{
			Icon:        icons.Ideas,
			Title:       i18n.T("pages.ideas.needs_project_title"),
			Reason:      i18n.T("pages.ideas.needs_project_reason"),
			ActionLabel: i18n.T("pages.ideas.btn_projects"),
			OnAction:    func() { a.Navigate(ports.Route{Kind: app.RouteProjects}) },
		})
	}})
}

// prepare initializes the state a zero page does not have yet.
func (n *Ideas) prepare() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.sort == "" {
		n.sort = ideas.SortUpdatedDesc
	}
	if n.table == nil {
		n.table = components.NewTableState()
	}
	if n.table.Sort != nil && n.table.Sort.Column == "" {
		n.table.Sort = &ui.SortOrder{Column: "updated", Descending: true}
	}
}

// tickDialogs closes a dialog whose service call finished, so the task never
// writes a frame-only field.
func (n *Ideas) tickDialogs() {
	n.mu.Lock()
	created := n.createOK
	if created {
		n.createOK = false
		n.createEr = ""
	}
	n.mu.Unlock()
	if created {
		n.createOpen = false
		n.createTitl = ""
		n.createDesc = ""
	}
	if n.reason.done {
		n.reason = ideaReasonForm{}
	}
}

// blank reports whether the first load is still running.
func (n *Ideas) blank() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.loading && !n.loaded
}

// banners reports the failures of the last load and of the last action.
func (n *Ideas) banners(v *components.UI) {
	n.mu.Lock()
	problems := append([]string(nil), n.problems...)
	action := n.action
	n.mu.Unlock()
	for _, problem := range problems {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.ideas.banner_problem"),
			Message: problem,
		})
	}
	if action != "" {
		v.Banner(components.BannerOptions{
			Tone:    components.Warning,
			Title:   i18n.T("pages.ideas.banner_action"),
			Message: action,
		})
	}
}

// toolbar is the search field, the status chips, the sort menu and the view
// switch.
func (n *Ideas) toolbar(v *components.UI) {
	ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).AlignItems(ui.Center).Wrap().Children(func() {
		n.searchField(v)
		ui.Spacer(v.C)
		n.sortMenu(v)
		ui.Segmented(v.C, &n.view,
			i18n.T("pages.ideas.view_list"),
			i18n.T("pages.ideas.view_groups"),
		).Label(i18n.T("pages.ideas.view_label"))
	})
	n.statusChips(v)
}

// searchField is the server-side title/description search.
func (n *Ideas) searchField(v *components.UI) {
	v.SearchInput(&n.query, components.SearchOptions{
		Label:       i18n.T("pages.ideas.field_search"),
		Placeholder: i18n.T("pages.ideas.field_search_hint"),
		Compact:     true,
	}).Width(components.SearchFieldWidth)
}

// sortMenu chooses the service sort of the list.
func (n *Ideas) sortMenu(v *components.UI) {
	label := i18n.T("pages.ideas.sort_" + sortSuffix(n.sort))
	ui.MenuButton(v.C, label, func(m *ui.Menu) {
		for i, key := range ideasSortKeys {
			item := m.Item(i18n.T(ideasSortLabels[i])).ID(key).Checked(n.sort == key)
			if item.Chosen() {
				n.chooseSort(key)
			}
		}
	}).Label(i18n.T("pages.ideas.sort_label"))
}

// chooseSort applies a toolbar sort and keeps the table header in step for the
// columns the service can order.
func (n *Ideas) chooseSort(key string) {
	n.sort = key
	column, desc := "updated", true
	switch key {
	case ideas.SortCreatedDesc:
		column = "created"
	case ideas.SortTitleAsc:
		column, desc = "title", false
	}
	n.mu.Lock()
	if n.table != nil {
		n.table.Sort = &ui.SortOrder{Column: column, Descending: desc}
	}
	n.mu.Unlock()
}

// sortSuffix is the i18n suffix of a sort key: updated_desc → updated.
func sortSuffix(key string) string {
	if i := strings.IndexByte(key, '_'); i > 0 {
		return key[:i]
	}
	return key
}

// statusChips are the multi-select status filters, with 全部 first.
func (n *Ideas) statusChips(v *components.UI) {
	ui.Row(v.C).FillWidth().Gap(tokens.Space1).Wrap().AlignItems(ui.Center).Children(func() {
		n.statusChip(v, i18n.T("pages.ideas.chip_all"), len(n.statuses) == 0, func() {
			n.statuses = nil
		})
		for _, status := range ideaStatusOrder {
			status := status
			on := n.hasStatus(status)
			n.statusChip(v, ideaStatusLabel(status), on, func() {
				n.toggleStatus(status)
			})
		}
	})
}

// statusChip draws one filter chip. The check icon carries the selected state,
// so the chip is not legible by color alone.
func (n *Ideas) statusChip(v *components.UI, label string, selected bool, onClick func()) {
	options := components.ButtonOptions{
		Variant: components.VariantSecondary,
		Size:    components.SizeSmall,
		OnClick: onClick,
	}
	if selected {
		options.Variant = components.VariantPrimary
		options.Icon = icons.Check
	}
	v.Button(label, options)
}

func (n *Ideas) hasStatus(status ideas.Status) bool {
	for _, s := range n.statuses {
		if s == status {
			return true
		}
	}
	return false
}

func (n *Ideas) toggleStatus(status ideas.Status) {
	out := make([]ideas.Status, 0, len(n.statuses)+1)
	found := false
	for _, s := range n.statuses {
		if s == status {
			found = true
			continue
		}
		out = append(out, s)
	}
	if !found {
		out = append(out, status)
	}
	n.statuses = out
}

// tableView renders the list as a sortable, resizable table.
func (n *Ideas) tableView(v *components.UI, a *app.App, projectID string, list []ideas.IdeaSummary) {
	// The table state keeps the row-key closure from the first frame, so the
	// closure reads this frame's rows through the page instead of capturing a
	// slice the next reload would leave behind.
	n.rows = sortIdeaRows(list, n.table.Sort)
	rows := n.rows
	open := func(idea *ideas.IdeaSummary) {
		a.Navigate(ports.Route{Kind: ports.RouteIdea, ProjectID: projectID, EntityID: idea.ID})
	}
	table := v.Table(components.TableOptions{
		Columns: []components.Column{
			{Title: i18n.T("pages.ideas.col_title"), ID: "title", Sortable: true, MinWidth: 200},
			{Title: i18n.T("pages.ideas.col_status"), ID: "status", Width: 132, Sortable: true},
			{Title: i18n.T("pages.ideas.col_support"), ID: "support", Width: 104, Sortable: true, Align: ui.End},
			{Title: i18n.T("pages.ideas.col_drafts"), ID: "drafts", Width: 88, Sortable: true, Align: ui.End},
			{Title: i18n.T("pages.ideas.col_links"), ID: "links", Width: 104, Sortable: true, Align: ui.End},
			{Title: i18n.T("pages.ideas.col_created"), ID: "created", Width: 128, Sortable: true},
			{Title: i18n.T("pages.ideas.col_updated"), ID: "updated", Width: 128, Sortable: true},
		},
		Rows:  len(rows),
		State: n.table,
		Key: func(i int) any {
			if i < 0 || i >= len(n.rows) {
				return ""
			}
			return n.rows[i].ID
		},
		Cell: func(row, col int) {
			idea := &rows[row]
			switch col {
			case 0:
				if ui.Text(v.C, ideaTitle(idea.Title)).SingleLine().FillWidth().RowClicked() {
					open(idea)
				}
			case 1:
				v.Badge(ideaStatusLabel(idea.Status), ideaStatusTone(idea.Status))
			case 2:
				if ui.Text(v.C, ideaCounts(idea.SupportCount, idea.OpposeCount)).FontFeatures("tnum").RowClicked() {
					open(idea)
				}
			case 3:
				if ui.Text(v.C, strconv.Itoa(idea.DraftCount)).FontFeatures("tnum").RowClicked() {
					open(idea)
				}
			case 4:
				links := ideaLinkCounts(idea.LinkedPapers, idea.LinkedNotes, idea.LinkedExps)
				if ui.Text(v.C, links).FontFeatures("tnum").RowClicked() {
					open(idea)
				}
			case 5:
				if ui.Text(v.C, relativeTime(idea.CreatedAt)).RowClicked() {
					open(idea)
				}
			case 6:
				if ui.Text(v.C, relativeTime(idea.UpdatedAt)).RowClicked() {
					open(idea)
				}
			}
		},
	})
	table.Height(ideasRows(len(rows)))
}

// groups renders the status-grouped view: eight vertical groups, On Hold
// last, with draggable cards. Dropping a card on another group calls
// ChangeStatus; dropping on Rejected asks for the reason first.
func (n *Ideas) groups(v *components.UI, a *app.App, projectID string, list []ideas.IdeaSummary, readOnly bool) {
	n.mu.Lock()
	changing := n.changing
	n.mu.Unlock()
	byStatus := map[ideas.Status][]*ideas.IdeaSummary{}
	for i := range list {
		idea := &list[i]
		byStatus[idea.Status] = append(byStatus[idea.Status], idea)
	}
	ui.Column(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
		for _, status := range ideaStatusOrder {
			n.group(v, a, projectID, status, byStatus[status], readOnly, changing)
		}
	})
}

// group is one status section and its drop target.
func (n *Ideas) group(v *components.UI, a *app.App, projectID string, status ideas.Status, items []*ideas.IdeaSummary, readOnly, changing bool) {
	p := v.T.Palette
	group := ui.Column(v.C).
		FillWidth().
		Padding(tokens.PanelPadding).
		Gap(tokens.RelatedGap).
		Background(mygo.Color(p.BgSurface)).
		Border(1, mygo.Color(p.BorderDefault)).
		Radius(tokens.RadiusLG).
		MinHeight(tokens.Space10)
	if !readOnly {
		if dragged, over := ui.DragOver[ideaDrag](group); over && dragged.Status != status {
			group.Border(2, mygo.Color(p.AccentDefault))
		}
	}
	group.Children(func() {
		ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
			v.Badge(ideaStatusLabel(status), ideaStatusTone(status))
			ui.Text(v.C, strconv.Itoa(len(items))).
				FontSize(tokens.Typography.Caption.Size).
				FontFeatures("tnum").
				TextColor(mygo.Color(p.TextMuted))
			ui.Spacer(v.C)
			if changing {
				v.Spinner(icons.SizeDefault)
			}
		})
		if len(items) == 0 {
			ui.Text(v.C, i18n.T("pages.ideas.group_empty")).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(p.TextMuted))
			return
		}
		ui.Row(v.C).FillWidth().Wrap().Gap(tokens.ComponentGap).AlignItems(ui.Start).Children(func() {
			for _, idea := range items {
				n.groupCard(v, a, projectID, idea, readOnly)
			}
		})
	})
	if readOnly {
		return
	}
	if dropped, ok := ui.Drop[ideaDrag](group); ok && dropped.Status != status {
		n.drop(a, projectID, dropped, status)
	}
}

// groupCard is one draggable card of a status group.
func (n *Ideas) groupCard(v *components.UI, a *app.App, projectID string, idea *ideas.IdeaSummary, readOnly bool) {
	p := v.T.Palette
	open := func() {
		a.Navigate(ports.Route{Kind: ports.RouteIdea, ProjectID: projectID, EntityID: idea.ID})
	}
	card := ui.Column(v.C).
		Padding(tokens.RelatedGap).
		Gap(tokens.Space1).
		Background(mygo.Color(p.BgSubtle)).
		Border(1, mygo.Color(p.BorderDefault)).
		Radius(tokens.RadiusMD).
		MinWidth(ideasCardMinWidth).
		MaxWidth(ideasCardMaxWidth).
		OnClick(open)
	if !readOnly {
		card.Drag(ideaDrag{ID: idea.ID, Title: idea.Title, Status: idea.Status})
	}
	if card.Hovered() {
		card.Border(1, mygo.Color(p.BorderStrong))
	}
	if !readOnly && card.Dragging() {
		card.Opacity(0.5)
	}
	card.Children(func() {
		ui.Text(v.C, ideaTitle(idea.Title)).
			FontSize(tokens.Typography.Body.Size).
			FontWeight(int(tokens.WeightMedium)).
			TextColor(mygo.Color(p.TextPrimary)).
			MaxLines(2)
		meta := ideaCounts(idea.SupportCount, idea.OpposeCount)
		if idea.DraftCount > 0 {
			meta += " · " + i18n.Tf("pages.ideas.drafts_badge", map[string]string{"n": strconv.Itoa(idea.DraftCount)})
		}
		ui.Text(v.C, meta).
			FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(p.TextSecondary))
		ui.Text(v.C, relativeTime(idea.UpdatedAt)).
			FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(p.TextMuted))
	})
}

// drop applies a card dropped on a status group. Rejected asks for the reason
// first; every other transition applies at once (§6.4).
func (n *Ideas) drop(a *app.App, projectID string, dropped ideaDrag, to ideas.Status) {
	if to == ideas.StatusRejected {
		n.reason = ideaReasonForm{
			open:   true,
			ideaID: dropped.ID,
			title:  dropped.Title,
			from:   dropped.Status,
			to:     to,
		}
		return
	}
	n.changeStatus(a, projectID, dropped.ID, to, "")
}

// changeStatus applies one transition, then reloads the list.
func (n *Ideas) changeStatus(a *app.App, projectID, ideaID string, to ideas.Status, reason string) {
	n.mu.Lock()
	if n.changing {
		n.mu.Unlock()
		return
	}
	n.changing = true
	n.action = ""
	n.mu.Unlock()

	api := n.Services.Ideas
	a.RunTask(i18n.T("pages.ideas.task_status"), func(ctx context.Context, t *app.Task) error {
		_, err := api.ChangeStatus(ctx, projectID, ideas.ChangeStatusInput{
			ID:              ideaID,
			To:              to,
			RejectionReason: strings.TrimSpace(reason),
			Actor:           ports.ActorUser,
		})
		n.mu.Lock()
		n.changing = false
		if err != nil {
			n.action = ideaErrText(err)
		}
		n.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.ideas.toast_status_fail"), ideaErrText(err))
			a.Notify()
			return nil
		}
		n.shared.bump()
		a.Toast(app.LevelSuccess, i18n.T("pages.ideas.toast_status"), ideaStatusLabel(to))
		a.Notify()
		return nil
	})
}

// createDialog collects a title and an optional description, then creates the
// Idea. A failure keeps the dialog and its input so the user can retry.
func (n *Ideas) createDialog(v *components.UI, a *app.App, projectID string, readOnly bool) {
	if !n.createOpen {
		return
	}
	n.mu.Lock()
	creating, errText := n.creating, n.createEr
	n.mu.Unlock()
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title:       i18n.T("pages.ideas.dialog_title"),
		Description: i18n.T("pages.ideas.dialog_desc"),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				n.createOpen = false
			}
			if v.Button(i18n.T("pages.ideas.dialog_create"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Loading:  creating,
				Disabled: creating || readOnly,
			}).Clicked() {
				n.create(a, projectID, readOnly)
			}
		},
	}, func() {
		field := v.Input(&n.createTitl, components.FieldOptions{
			Label:       i18n.T("pages.ideas.field_title"),
			Placeholder: i18n.T("pages.ideas.field_title_hint"),
			AutoFocus:   true,
			Disabled:    readOnly,
		})
		if field.Submitted() {
			n.create(a, projectID, readOnly)
		}
		v.Textarea(&n.createDesc, components.FieldOptions{
			Label:       i18n.T("pages.ideas.field_description"),
			Placeholder: i18n.T("pages.ideas.field_description_hint"),
			Disabled:    readOnly,
		})
		if errText != "" {
			ui.Text(v.C, errText).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
	if !open && !creating {
		n.createOpen = false
	}
}

// create runs the create call.
func (n *Ideas) create(a *app.App, projectID string, readOnly bool) {
	if readOnly {
		return
	}
	title := strings.TrimSpace(n.createTitl)
	if title == "" {
		a.Toast(app.LevelWarning, i18n.T("pages.ideas.title_required"), "")
		return
	}
	if utf8.RuneCountInString(title) > ideasMaxTitleRunes {
		a.Toast(app.LevelWarning, i18n.T("pages.ideas.title_too_long"), "")
		return
	}
	n.mu.Lock()
	if n.creating {
		n.mu.Unlock()
		return
	}
	n.creating = true
	n.createEr = ""
	n.mu.Unlock()

	api := n.Services.Ideas
	description := n.createDesc
	a.RunTask(i18n.T("pages.ideas.task_create"), func(ctx context.Context, t *app.Task) error {
		idea, err := api.CreateIdea(ctx, projectID, ideas.CreateIdeaInput{
			Title:         title,
			DescriptionMD: description,
			Actor:         ports.ActorUser,
		})
		n.mu.Lock()
		n.creating = false
		if err != nil {
			n.createEr = ideaErrText(err)
		}
		n.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.ideas.toast_create_fail"), ideaErrText(err))
			a.Notify()
			return nil
		}
		n.mu.Lock()
		n.createOK = true
		n.mu.Unlock()
		n.shared.bump()
		a.Toast(app.LevelSuccess, i18n.T("pages.ideas.toast_created"), ideaTitle(idea.Title))
		a.Navigate(ports.Route{Kind: ports.RouteIdea, ProjectID: projectID, EntityID: idea.ID})
		return nil
	})
}

// reasonDialog collects the mandatory reason of a transition to Rejected. An
// empty or blank reason refuses the transition: the button stays disabled and
// the service is never called (§4.2.2, AC-04).
func (n *Ideas) reasonDialog(v *components.UI, a *app.App, projectID string) {
	form := &n.reason
	if !form.open {
		return
	}
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title: i18n.T("pages.ideas.reason_title"),
		Description: i18n.Tf("pages.ideas.reason_desc", map[string]string{
			"title": ideaTitle(form.title),
		}),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				n.reason = ideaReasonForm{}
				return
			}
			valid := strings.TrimSpace(form.text) != ""
			if v.Button(i18n.T("pages.ideas.reason_confirm"), components.ButtonOptions{
				Variant:  components.VariantDanger,
				Loading:  form.busy,
				Disabled: form.busy || !valid,
			}).Clicked() && valid {
				n.submitReason(a, projectID)
			}
		},
	}, func() {
		field := v.Textarea(&form.text, components.FieldOptions{
			Label:       i18n.T("pages.ideas.field_reason"),
			Placeholder: i18n.T("pages.ideas.field_reason_hint"),
			AutoFocus:   true,
		})
		if field.Submitted() {
			n.submitReason(a, projectID)
		}
		if strings.TrimSpace(form.text) == "" && form.errTxt == "" {
			ui.Text(v.C, i18n.T("pages.ideas.reason_required")).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(v.T.Palette.TextMuted))
		}
		if form.errTxt != "" {
			ui.Text(v.C, form.errTxt).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
	if !open && !form.busy {
		n.reason = ideaReasonForm{}
	}
}

// submitReason validates and applies the pending transition to Rejected.
func (n *Ideas) submitReason(a *app.App, projectID string) {
	form := &n.reason
	reason := strings.TrimSpace(form.text)
	if reason == "" {
		form.errTxt = i18n.T("pages.ideas.reason_required")
		return
	}
	if form.busy {
		return
	}
	form.busy = true
	form.errTxt = ""
	to, ideaID := form.to, form.ideaID
	api := n.Services.Ideas
	a.RunTask(i18n.T("pages.ideas.task_status"), func(ctx context.Context, t *app.Task) error {
		_, err := api.ChangeStatus(ctx, projectID, ideas.ChangeStatusInput{
			ID:              ideaID,
			To:              to,
			RejectionReason: reason,
			Actor:           ports.ActorUser,
		})
		form.busy = false
		if err != nil {
			form.errTxt = ideaErrText(err)
			form.open = true
			a.Notify()
			return nil
		}
		form.done = true
		n.shared.bump()
		a.Toast(app.LevelSuccess, i18n.T("pages.ideas.toast_status"), ideaStatusLabel(to))
		a.Notify()
		return nil
	})
}

// tickLoad reloads the list when the project, the status filter or the sort
// changed, and after the search debounce when the query did.
func (n *Ideas) tickLoad(v *components.UI, a *app.App, projectID string) {
	n.mu.Lock()
	query := strings.TrimSpace(n.query)
	if query != n.lastQuery {
		n.lastQuery = query
		n.queryAt = v.C.Now()
		n.searchPending = false
	}
	statuses := statusFilterKey(n.statuses)
	stale := n.loading
	filters := n.project != projectID || !n.loaded || n.version != n.shared.version() ||
		statuses != n.appliedStatuses || n.sort != n.appliedSort
	queryDirty := query != n.appliedQuery
	wait := ideasSearchDelay - v.C.Now().Sub(n.queryAt)
	n.mu.Unlock()

	if stale {
		return
	}
	switch {
	case filters:
		n.reload(a, projectID)
	case queryDirty:
		if wait > 0 {
			n.mu.Lock()
			if !n.searchPending {
				n.searchPending = true
				v.C.After(wait)
			}
			n.mu.Unlock()
			return
		}
		n.reload(a, projectID)
	}
}

// reload reads the list with the current filter, sort and query.
func (n *Ideas) reload(a *app.App, projectID string) {
	api := n.Services.Ideas
	n.mu.Lock()
	if n.loading {
		n.mu.Unlock()
		return
	}
	n.loading = true
	if n.project != projectID {
		n.project = projectID
		n.list = nil
		n.loaded = false
		n.version = 0
	}
	query := ideas.ListIdeasQuery{
		Statuses: append([]ideas.Status(nil), n.statuses...),
		Text:     strings.TrimSpace(n.query),
		Sort:     n.sort,
		PageSize: ideasListLimit,
	}
	n.appliedStatuses = statusFilterKey(n.statuses)
	n.appliedQuery = query.Text
	n.appliedSort = n.sort
	n.searchPending = false
	gen := n.shared.version()
	n.mu.Unlock()

	a.RunTask(i18n.T("pages.ideas.task_load"), func(ctx context.Context, t *app.Task) error {
		page, err := api.ListIdeas(ctx, projectID, query)
		n.mu.Lock()
		n.loading = false
		if err != nil {
			n.problems = append(n.problems[:0], ideaErrText(err))
			n.appliedQuery = ""
			n.appliedStatuses = ""
			n.appliedSort = ""
		} else {
			n.list = page.Items
			n.total = page.Total
			n.problems = nil
			n.loaded = true
			n.version = gen
		}
		n.mu.Unlock()
		a.Notify()
		return nil
	})
}

// snapshot returns the cached rows.
func (n *Ideas) snapshot() []ideas.IdeaSummary {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]ideas.IdeaSummary(nil), n.list...)
}

// statusFilterKey is the canonical identity of a status filter set.
func statusFilterKey(statuses []ideas.Status) string {
	if len(statuses) == 0 {
		return ""
	}
	parts := make([]string, 0, len(statuses))
	for _, s := range statuses {
		parts = append(parts, string(s))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// ideaStatusOrder is the display order of the eight statuses, with On Hold
// last (SPEC-06 §6.2).
var ideaStatusOrder = []ideas.Status{
	ideas.StatusNew,
	ideas.StatusResearching,
	ideas.StatusValidating,
	ideas.StatusSupported,
	ideas.StatusPartiallySupported,
	ideas.StatusInconclusive,
	ideas.StatusRejected,
	ideas.StatusOnHold,
}

// ideaStatusKey is the i18n key of a persisted status value. The label is
// keyed off ideas.Status, never off DisplayName(), whose text is not
// localised.
func ideaStatusKey(status ideas.Status) string {
	switch status {
	case ideas.StatusNew:
		return "pages.ideas.status.new"
	case ideas.StatusResearching:
		return "pages.ideas.status.researching"
	case ideas.StatusValidating:
		return "pages.ideas.status.validating"
	case ideas.StatusSupported:
		return "pages.ideas.status.supported"
	case ideas.StatusPartiallySupported:
		return "pages.ideas.status.partially_supported"
	case ideas.StatusInconclusive:
		return "pages.ideas.status.inconclusive"
	case ideas.StatusRejected:
		return "pages.ideas.status.rejected"
	case ideas.StatusOnHold:
		return "pages.ideas.status.on_hold"
	}
	return string(status)
}

// ideaStatusLabel is the localised name of a status.
func ideaStatusLabel(status ideas.Status) string { return i18n.T(ideaStatusKey(status)) }

// ideaStatusTone is the fixed status tone of the design system: the badge
// colour comes from the palette, and the label always carries the meaning.
func ideaStatusTone(status ideas.Status) components.Tone {
	switch status {
	case ideas.StatusSupported, ideas.StatusPartiallySupported:
		return components.Success
	case ideas.StatusValidating:
		return components.Accent
	case ideas.StatusResearching:
		return components.Info
	case ideas.StatusInconclusive:
		return components.Warning
	case ideas.StatusRejected:
		return components.Danger
	}
	return components.Neutral
}

// ideaNextSteps is the 「常用下一步」 grouping of the selector (§4.2.2). It is
// a suggestion list, never a restriction: every transition stays reachable
// through 「其他状态」.
func ideaNextSteps(from ideas.Status) []ideas.Status {
	switch from {
	case ideas.StatusNew:
		return []ideas.Status{ideas.StatusResearching, ideas.StatusOnHold}
	case ideas.StatusResearching:
		return []ideas.Status{ideas.StatusValidating, ideas.StatusOnHold}
	case ideas.StatusValidating:
		return []ideas.Status{
			ideas.StatusSupported,
			ideas.StatusPartiallySupported,
			ideas.StatusInconclusive,
			ideas.StatusRejected,
			ideas.StatusOnHold,
		}
	case ideas.StatusSupported, ideas.StatusPartiallySupported, ideas.StatusInconclusive:
		return []ideas.Status{ideas.StatusRejected, ideas.StatusOnHold}
	case ideas.StatusRejected:
		return []ideas.Status{ideas.StatusResearching, ideas.StatusOnHold}
	case ideas.StatusOnHold:
		return []ideas.Status{ideas.StatusResearching, ideas.StatusValidating}
	}
	return nil
}

// ideaOtherStatuses is every status the 「其他状态」 group offers: all eight,
// without the current one and without the suggestions.
func ideaOtherStatuses(from ideas.Status) []ideas.Status {
	suggested := ideaNextSteps(from)
	out := make([]ideas.Status, 0, len(ideaStatusOrder))
	for _, status := range ideaStatusOrder {
		if status == from {
			continue
		}
		skip := false
		for _, s := range suggested {
			if s == status {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, status)
		}
	}
	return out
}

// sortIdeaRows orders the table's rows by the column its header reports. An
// empty header order keeps the service order.
func sortIdeaRows(list []ideas.IdeaSummary, order *ui.SortOrder) []ideas.IdeaSummary {
	out := append([]ideas.IdeaSummary(nil), list...)
	if order == nil || order.Column == "" {
		return out
	}
	desc := order.Descending
	less := func(i, j int) bool { return false }
	switch order.Column {
	case "title":
		less = func(i, j int) bool {
			return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
		}
	case "status":
		less = func(i, j int) bool { return ideaStatusRank(out[i].Status) < ideaStatusRank(out[j].Status) }
	case "support":
		less = func(i, j int) bool {
			return out[i].SupportCount < out[j].SupportCount
		}
	case "drafts":
		less = func(i, j int) bool { return out[i].DraftCount < out[j].DraftCount }
	case "links":
		less = func(i, j int) bool { return ideaLinks(out[i]) < ideaLinks(out[j]) }
	case "created":
		less = func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) }
	default:
		less = func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) }
	}
	sort.SliceStable(out, func(i, j int) bool {
		if desc {
			return less(j, i)
		}
		return less(i, j)
	})
	return out
}

// ideaStatusRank is the position of a status in the display order, for the
// table's status sort.
func ideaStatusRank(status ideas.Status) int {
	for i, s := range ideaStatusOrder {
		if s == status {
			return i
		}
	}
	return len(ideaStatusOrder)
}

func ideaLinks(idea ideas.IdeaSummary) int {
	return idea.LinkedPapers + idea.LinkedNotes + idea.LinkedExps
}

// ideasRows is the table's height: the header row over one 44px row per
// record, capped at ideasListRows. Without the header's room the first row
// would be clipped behind it.
func ideasRows(rows int) float32 {
	if rows < 1 {
		rows = 1
	}
	if rows > ideasListRows {
		rows = ideasListRows
	}
	return float32(rows)*tokens.Space11 + tokens.Space10
}

// ideaTitle renders a title, treating an empty one as untitled.
func ideaTitle(title string) string {
	if strings.TrimSpace(title) == "" {
		return i18n.T("pages.ideas.untitled")
	}
	return title
}

// ideaCounts renders the support/oppose pair of a list row.
func ideaCounts(support, oppose int) string {
	return strconv.Itoa(support) + " / " + strconv.Itoa(oppose)
}

// ideaLinkCounts renders the paper/note/experiment link counts.
func ideaLinkCounts(papers, notes, experiments int) string {
	return strconv.Itoa(papers) + " / " + strconv.Itoa(notes) + " / " + strconv.Itoa(experiments)
}

// readOnlyTooltip explains a disabled write control of an archived project.
func readOnlyTooltip(readOnly bool) string {
	if readOnly {
		return i18n.T("pages.ideas.readonly_tooltip")
	}
	return ""
}

// ideaErrText localises an Ideas error: the service's ErrorCode names the
// rule, and the page maps the documented codes to its own messages instead of
// adding them to the generic errtext table (which stays module-free).
func ideaErrText(err error) string {
	if err == nil {
		return ""
	}
	if code := ideas.CodeOf(err); code != "" {
		key := ideaErrorKey(code)
		if text := i18n.T(key); text != key {
			return text
		}
	}
	return errtext.Text(err)
}

// ideaErrCode is the machine code shown next to an AI failure (§6.6).
func ideaErrCode(err error) string {
	if err == nil {
		return ""
	}
	if code := ideas.CodeOf(err); code != "" {
		return string(code)
	}
	return "error"
}

// ideaErrorKey maps an ErrorCode to its message key: idea.not_found becomes
// pages.ideas.error.idea_not_found.
func ideaErrorKey(code ideas.ErrorCode) string {
	return "pages.ideas.error." + strings.ReplaceAll(string(code), ".", "_")
}
