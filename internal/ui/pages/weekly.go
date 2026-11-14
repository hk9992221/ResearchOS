package pages

// The Weekly Review page is the SPEC-09 list of the open project: report
// history with page-number pagination, the generation dialog that creates or
// regenerates one report for a period, and the entry to the detail page. The
// actor of every write is the in-app user; weeklyreview declares its own Actor
// type, so none of these calls take events.Actor.
//
// The list and the detail share a generation counter: a write in either bumps
// it, so a returning list reloads instead of showing a stale row.

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"researchos/internal/foundation/schema"
	"researchos/internal/i18n"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
	"researchos/internal/weeklyreview"
)

// weeklyPageSize is the page size of the history table. SPEC-09 caps one page
// at 100; twenty is one screenful of rows.
const weeklyPageSize = 20

// weeklyListRows is how many table rows fit before the table scrolls inside
// its own box (design system row height 44px).
const weeklyListRows = 12

// weeklyShared is the session state the list and the detail share.
type weeklyShared struct {
	mu  sync.Mutex
	gen int
}

func (s *weeklyShared) bump() {
	s.mu.Lock()
	s.gen++
	s.mu.Unlock()
}

func (s *weeklyShared) version() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

// WeeklyAPI is the part of SPEC-09 the weekly pages use. Every call names the
// open project, because one adapter serves the workspace; a nil field means the
// module is not assembled and the page says so instead of faking data.
//
// AIEnabled reports whether a chat provider is assigned for this workspace
// (the local check, no network). False is a supported mode: generation then
// writes the deterministic seven-section skeleton and the dialog points at
// Settings.
//
// RevealFolder opens a directory in the operating system's file manager. It
// lives here because shell.revealFolder is package-private; the boot adapter
// carries the same fifteen lines (SPEC-09 §6.4 打开所在文件夹).
type WeeklyAPI interface {
	DefaultPeriod(ctx context.Context, projectID string) (weeklyreview.Period, error)
	List(ctx context.Context, projectID string, in weeklyreview.ListReportsInput) (weeklyreview.ReportPage, error)
	Get(ctx context.Context, projectID, reportID string) (*weeklyreview.WeeklyReport, error)
	CollectActivities(ctx context.Context, projectID string, in weeklyreview.CollectInput) (*weeklyreview.ActivityDigest, error)
	GenerateForPeriod(ctx context.Context, projectID string, in weeklyreview.GeneratePeriodInput) (*weeklyreview.GenerateOutcome, error)
	GenerateDraft(ctx context.Context, projectID string, in weeklyreview.GenerateInput) (*weeklyreview.GenerateOutcome, error)
	UpdateContent(ctx context.Context, projectID string, in weeklyreview.UpdateContentInput) (*weeklyreview.WeeklyReport, error)
	Confirm(ctx context.Context, projectID string, in weeklyreview.ConfirmInput) (*weeklyreview.WeeklyReport, error)
	RevertToDraft(ctx context.Context, projectID string, in weeklyreview.RevertInput) (*weeklyreview.WeeklyReport, error)
	DeleteReport(ctx context.Context, projectID, reportID string) error
	RestoreReport(ctx context.Context, projectID, reportID string) error
	Export(ctx context.Context, projectID string, in weeklyreview.ExportInput) (*weeklyreview.ExportResult, error)
	Skills(ctx context.Context, projectID string) (weeklyreview.SkillSet, error)
	SaveSkill(ctx context.Context, projectID string, in weeklyreview.SkillInput) (weeklyreview.SkillSet, error)
	DeleteSkill(ctx context.Context, projectID, skillID string) (weeklyreview.SkillSet, error)
	SetDefaultSkill(ctx context.Context, projectID, skillID string) (weeklyreview.SkillSet, error)
	AIEnabled(projectID string) bool
	RevealFolder(dir string) error
}

// Weekly renders the report history and the generation dialog.
type Weekly struct {
	Services *Services
	shared   *weeklyShared

	mu       sync.Mutex
	loading  bool
	loaded   bool
	project  string
	key      string
	version  int
	items    []weeklyreview.ReportSummary
	total    int
	problems []string

	// Frame-only state: filters, the generation dialog and the confirmation
	// dialogs. A task never writes here; it writes the fields above and asks
	// for a frame.
	page        int
	statusKey   string
	statusLabel string
	fromText    string
	toText      string
	query       string
	showDeleted bool
	table       *ui.ListState

	genOpen        bool
	defPeriod      weeklyreview.Period
	genPeriodReady bool
	needOverwrite  bool
	periodBlocked  bool
	genQuick       int
	genStart       time.Time
	genEnd         time.Time
	genLoaded      bool
	genAI          bool
	genOverwrite   bool
	generating     bool
	genErr         string
	genNote        string
	genDone        bool
	genReportID    string
	genTask        *app.Task

	// skills are the workspace writing skills and the generation dialog's
	// selection; skillEditor is the shared edit dialog.
	skills        weeklyreview.SkillSet
	skillsLoading bool
	skillsLoaded  bool
	genSkillID    string
	genSkillLabel string
	skillEditor   weeklySkillEditor

	delID    string
	delTitle string
	deleting bool
	delErr   string
	delDone  bool

	restoringID string
	restoreDone bool
}

// Page renders one frame of the report list.
func (n *Weekly) Page(v *components.UI, r ports.Route) {
	a := n.Services.App
	if a == nil {
		return
	}
	if n.Services.Weekly == nil {
		n.unavailable(v)
		return
	}
	project := a.Project()
	if project == nil {
		n.needsProject(v, a)
		return
	}
	n.prepare()
	n.tickDialogs()
	n.load(a, project.ID)
	items := n.visible()
	// An empty history shows its generate action in the empty state, so the
	// header does not repeat the same primary button.
	empty := len(items) == 0 && !n.blank()

	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.weekly_review.title"),
			Description: i18n.Tf("pages.weekly_review.description", map[string]string{"project": project.Name}),
			Actions: func() {
				if empty {
					return
				}
				if v.Button(i18n.T("pages.weekly_review.btn_generate"), components.ButtonOptions{
					Variant:  components.VariantPrimary,
					Icon:     icons.Plus,
					Disabled: project.ArchivedAt != nil,
				}).Clicked() {
					n.openGenerate(a, project.ID)
				}
			},
		})
		if project.ArchivedAt != nil {
			v.Banner(components.BannerOptions{
				Tone:    components.Warning,
				Title:   i18n.T("pages.weekly_review.archived_title"),
				Message: i18n.T("pages.weekly_review.archived_message"),
			})
		}
		n.banners(v)
		n.toolbar(v, project.ID)
		switch {
		case n.blank():
			v.Skeleton(components.SkeletonOptions{Lines: 6})
		case empty:
			v.EmptyState(components.EmptyStateOptions{
				Icon:        icons.WeeklyReview,
				Title:       i18n.T("pages.weekly_review.empty_title"),
				Reason:      i18n.T("pages.weekly_review.empty_reason"),
				ActionLabel: i18n.T("pages.weekly_review.btn_generate"),
				OnAction:    func() { n.openGenerate(a, project.ID) },
			})
		default:
			n.tableView(v, a, items, project.ArchivedAt == nil)
		}
		n.pagination(v)
		n.generateDialog(v, a, project.ID, project.ArchivedAt == nil)
		n.deleteDialog(v, a, project.ID)
	}})
}

// unavailable is the honest state of a workspace whose weekly service was
// never assembled.
func (n *Weekly) unavailable(v *components.UI) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{Title: i18n.T("pages.weekly_review.title")})
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.WeeklyReview,
			Title:  i18n.T("pages.weekly_review.disabled_title"),
			Reason: i18n.T("pages.weekly_review.disabled_reason"),
		})
	}})
}

// needsProject states the precondition: reports belong to a project.
func (n *Weekly) needsProject(v *components.UI, a *app.App) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.weekly_review.title"),
			Description: i18n.T("pages.weekly_review.needs_project_desc"),
		})
		v.EmptyState(components.EmptyStateOptions{
			Icon:        icons.WeeklyReview,
			Title:       i18n.T("pages.weekly_review.needs_project_title"),
			Reason:      i18n.T("pages.weekly_review.needs_project_reason"),
			ActionLabel: i18n.T("pages.weekly_review.btn_projects"),
			OnAction:    func() { a.Navigate(ports.Route{Kind: app.RouteProjects}) },
		})
	}})
}

func (n *Weekly) prepare() {
	if n.table == nil {
		n.table = components.NewTableState()
	}
	if n.page <= 0 {
		n.page = 1
	}
	if n.statusKey == "" {
		n.statusKey = "all"
	}
}

// tickDialogs closes a dialog whose task finished, so the task never writes a
// frame-only field.
func (n *Weekly) tickDialogs() {
	n.mu.Lock()
	deleted, restored, generated := n.delDone, n.restoreDone, n.genDone
	reportID := n.genReportID
	if deleted {
		n.delDone = false
		n.delErr = ""
	}
	if restored {
		n.restoreDone = false
	}
	if generated {
		n.genDone = false
		n.genReportID = ""
	}
	n.mu.Unlock()
	if deleted {
		n.delID = ""
		n.delTitle = ""
	}
	if generated && reportID != "" {
		n.genOpen = false
		n.Services.App.Navigate(ports.Route{
			Kind: ports.RouteWeeklyReport, ProjectID: n.project, EntityID: reportID,
		})
	}
}

func (n *Weekly) blank() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.loading && !n.loaded
}

func (n *Weekly) banners(v *components.UI) {
	n.mu.Lock()
	problems := append([]string(nil), n.problems...)
	n.mu.Unlock()
	for _, problem := range problems {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.weekly_review.banner_problem"),
			Message: problem,
		})
	}
}

// toolbar is the status and period filters, the title filter and the recycle
// bin switch. The controls share one 32px row: the placeholder names each
// field, so no label sits above it and the controls line up with the buttons.
func (n *Weekly) toolbar(v *components.UI, projectID string) {
	choices, keys := weeklyStatusChoices()
	if key, ok := weeklyStatusKey(n.statusLabel, choices, keys); ok {
		n.statusKey = key
	} else {
		// The select was just created: show the filter the key names.
		n.statusLabel = weeklyStatusLabel(n.statusKey, choices, keys)
	}
	ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).AlignItems(ui.Center).Wrap().Children(func() {
		v.Select(&n.statusLabel, components.SelectOptions{
			Label:   i18n.T("pages.weekly_review.field_status"),
			Options: choices,
			Compact: true,
			Width:   120,
		})
		v.SearchInput(&n.query, components.SearchOptions{
			Label:       i18n.T("pages.weekly_review.field_search"),
			Placeholder: i18n.T("pages.weekly_review.field_search_hint"),
			Compact:     true,
		}).Width(components.SearchFieldWidth)
		v.Input(&n.fromText, components.FieldOptions{
			Label:       i18n.T("pages.weekly_review.field_from"),
			Placeholder: i18n.T("pages.weekly_review.field_from_hint"),
			Compact:     true,
		}).Width(160)
		v.Input(&n.toText, components.FieldOptions{
			Label:       i18n.T("pages.weekly_review.field_to"),
			Placeholder: i18n.T("pages.weekly_review.field_to_hint"),
			Compact:     true,
		}).Width(160)
		ui.Spacer(v.C)
		v.Checkbox(&n.showDeleted, i18n.T("pages.weekly_review.field_deleted"), components.ToggleOptions{})
		if v.Button(i18n.T("pages.weekly_review.btn_reload"), components.ButtonOptions{
			Variant: components.VariantGhost,
			Icon:    icons.Refresh,
		}).Clicked() {
			n.mu.Lock()
			n.loaded = false
			n.mu.Unlock()
		}
	})
}

// tableView renders the report history.
func (n *Weekly) tableView(v *components.UI, a *app.App, items []weeklyreview.ReportSummary, writable bool) {
	items = sortedWeeklySummaries(items, n.table.Sort)
	open := func(item weeklyreview.ReportSummary) {
		a.Navigate(ports.Route{Kind: ports.RouteWeeklyReport, ProjectID: n.project, EntityID: item.ID})
	}
	table := v.Table(components.TableOptions{
		Columns: []components.Column{
			{Title: i18n.T("pages.weekly_review.col_period"), ID: "period", Width: 210, Sortable: true},
			{Title: i18n.T("pages.weekly_review.col_title"), ID: "title", Sortable: true},
			{Title: i18n.T("pages.weekly_review.col_status"), Width: 110},
			{Title: i18n.T("pages.weekly_review.col_updated"), Width: 140, Sortable: true},
			{Title: i18n.T("pages.weekly_review.col_confirmed"), Width: 140, Sortable: true},
			{Title: i18n.T("pages.weekly_review.col_actions"), Width: 120, Align: ui.End},
		},
		Rows:  len(items),
		State: n.table,
		Key:   func(i int) any { return items[i].ID },
		Cell: func(row, col int) {
			item := items[row]
			switch col {
			case 0:
				if ui.Text(v.C, weeklyPeriodText(item.Period)).FontFeatures("tnum").FillWidth().RowClicked() {
					open(item)
				}
			case 1:
				if ui.Text(v.C, item.Title).SingleLine().FillWidth().RowClicked() {
					open(item)
				}
			case 2:
				v.Badge(weeklyStatusText(item.Status), weeklyStatusTone(item.Status))
			case 3:
				if ui.Text(v.C, relativeTime(item.UpdatedAt)).FontFeatures("tnum").RowClicked() {
					open(item)
				}
			case 4:
				if item.ConfirmedAt != nil {
					ui.Text(v.C, relativeTime(*item.ConfirmedAt)).FontFeatures("tnum").RowClicked()
				} else {
					ui.Text(v.C, "—").TextColor(mygo.Color(v.T.Palette.TextMuted))
				}
			case 5:
				ui.Row(v.C).Gap(tokens.Space1).Justify(ui.End).Children(func() {
					if item.DeletedAt != nil {
						if v.Button(i18n.T("pages.weekly_review.btn_restore"), components.ButtonOptions{
							Variant:  components.VariantGhost,
							Size:     components.SizeSmall,
							Disabled: !writable || n.restoringID == item.ID,
							Loading:  n.restoringID == item.ID,
						}).Clicked() {
							n.restore(a, item.ID)
						}
						return
					}
					if v.IconButton(icons.Trash, i18n.T("pages.weekly_review.btn_delete"), components.IconButtonOptions{
						Variant:  components.VariantGhost,
						Size:     components.SizeSmall,
						Disabled: !writable,
					}).Clicked() {
						n.delID = item.ID
						n.delTitle = item.Title
						n.mu.Lock()
						n.delErr = ""
						n.mu.Unlock()
					}
					if v.IconButton(icons.ExternalLink, i18n.T("pages.weekly_review.btn_export"), components.IconButtonOptions{
						Variant: components.VariantGhost,
						Size:    components.SizeSmall,
					}).Clicked() {
						a.Navigate(ports.Route{
							Kind: ports.RouteWeeklyReport, ProjectID: n.project, EntityID: item.ID,
							Params: map[string]string{"action": "export"},
						})
					}
				})
			}
		},
	})
	table.Height(visibleWeeklyRows(len(items)))
}

// pagination walks the page-number API of SPEC-09 §5.3.
func (n *Weekly) pagination(v *components.UI) {
	n.mu.Lock()
	total, loading := n.total, n.loading
	n.mu.Unlock()
	pages := 1
	if total > 0 {
		pages = (total + weeklyPageSize - 1) / weeklyPageSize
	}
	if pages <= 1 {
		return
	}
	ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
		ui.Text(v.C, i18n.Tf("pages.weekly_review.page_of", map[string]string{
			"page":  strconv.Itoa(n.page),
			"pages": strconv.Itoa(pages),
			"total": strconv.Itoa(total),
		})).FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
		ui.Spacer(v.C)
		if v.Button(i18n.T("pages.weekly_review.btn_prev"), components.ButtonOptions{
			Variant:  components.VariantSecondary,
			Size:     components.SizeSmall,
			Icon:     icons.ChevronLeft,
			Disabled: loading || n.page <= 1,
		}).Clicked() {
			n.page--
		}
		if v.Button(i18n.T("pages.weekly_review.btn_next"), components.ButtonOptions{
			Variant:  components.VariantSecondary,
			Size:     components.SizeSmall,
			Disabled: loading || n.page >= pages,
		}).Clicked() {
			n.page++
		}
	})
}

// openGenerate opens the generation dialog and loads the configured default
// period once per project.
func (n *Weekly) openGenerate(a *app.App, projectID string) {
	n.genOpen = true
	n.genErr = ""
	n.genNote = ""
	n.genOverwrite = false
	n.periodBlocked = false
	n.genAI = n.Services.Weekly.AIEnabled(projectID)
	n.skillsLoad(a, projectID)
	if n.genLoaded && n.project == projectID {
		return
	}
	n.genLoaded = false
	api := n.Services.Weekly
	a.RunTask(i18n.T("pages.weekly_review.task_default_period"), func(ctx context.Context, t *app.Task) error {
		period, err := api.DefaultPeriod(ctx, projectID)
		if err != nil {
			n.mu.Lock()
			n.problems = append(n.problems[:0], weeklyError(err))
			n.mu.Unlock()
			a.Notify()
			return nil
		}
		// Only state under mu is written here; the next frame adopts the
		// period into the dialog's frame state.
		n.mu.Lock()
		n.defPeriod = weeklyCurrentWeek(period, time.Now())
		n.genPeriodReady = true
		n.mu.Unlock()
		a.Notify()
		return nil
	})
}

// skillsLoad reads the writing skills once per session. The generation dialog
// needs them for its selector even before the first generation.
func (n *Weekly) skillsLoad(a *app.App, projectID string) {
	api := n.Services.Weekly
	if api == nil || a == nil {
		return
	}
	// Refresh on every dialog open: the settings tab may have changed the
	// set since the last generation.
	n.mu.Lock()
	need := !n.skillsLoading
	if need {
		n.skillsLoading = true
	}
	n.mu.Unlock()
	if !need {
		return
	}
	a.RunTask(i18n.T("pages.weekly_review.skill_task_load"), func(ctx context.Context, t *app.Task) error {
		set, err := api.Skills(ctx, projectID)
		n.mu.Lock()
		n.skillsLoading = false
		n.skillsLoaded = true
		if err != nil {
			n.problems = append(n.problems[:0], weeklyError(err))
		} else {
			n.skills = set
		}
		n.mu.Unlock()
		a.Notify()
		return nil
	})
}

// adoptPeriod moves a period loaded by the task into the dialog's frame state.
func (n *Weekly) adoptPeriod() {
	n.mu.Lock()
	ready := n.genPeriodReady
	def := n.defPeriod
	n.mu.Unlock()
	if ready && !n.genLoaded {
		n.applyPeriod(def)
		n.genLoaded = true
		n.genQuick = 0
	}
}

// applyPeriod writes a period into the frame's quick picker and dates.
func (n *Weekly) applyPeriod(p weeklyreview.Period) {
	if p.Empty() {
		return
	}
	n.genStart = p.Start
	n.genEnd = p.EndExclusive.AddDate(0, 0, -1)
}

// load reads the page once per project, filter and shared generation.
func (n *Weekly) load(a *app.App, projectID string) {
	key := n.queryKey()
	n.mu.Lock()
	need := !n.loading && (!n.loaded || n.project != projectID || n.version != n.shared.version() || n.key != key)
	n.mu.Unlock()
	if need {
		n.reload(a, projectID, key)
	}
}

// queryKey names the list query of this frame: a filter or page change is a
// new key, and the running read is not reused for it.
func (n *Weekly) queryKey() string {
	return strconv.Itoa(n.page) + "|" + n.statusKey + "|" + n.fromText + "|" + n.toText + "|" + strconv.FormatBool(n.showDeleted)
}

func (n *Weekly) reload(a *app.App, projectID, key string) {
	api := n.Services.Weekly
	n.mu.Lock()
	if n.loading {
		n.mu.Unlock()
		return
	}
	n.loading = true
	if n.project != projectID {
		n.project = projectID
		n.items = nil
		n.total = 0
		n.loaded = false
		n.version = 0
	}
	n.mu.Unlock()

	page, size := n.page, weeklyPageSize
	in := weeklyreview.ListReportsInput{
		Page:           page,
		PageSize:       size,
		IncludeDeleted: n.showDeleted,
	}
	switch n.statusKey {
	case "draft":
		in.Status = []weeklyreview.Status{weeklyreview.StatusDraft}
	case "confirmed":
		in.Status = []weeklyreview.Status{weeklyreview.StatusConfirmed}
	}
	if from := parseWeeklyDate(n.fromText); from != nil {
		in.From = from
	}
	if to := parseWeeklyDate(n.toText); to != nil {
		in.To = to
	}
	gen := n.shared.version()

	a.RunTask(i18n.T("pages.weekly_review.task_load"), func(ctx context.Context, t *app.Task) error {
		pageData, err := api.List(ctx, projectID, in)
		n.mu.Lock()
		n.loading = false
		if err != nil {
			n.problems = append(n.problems[:0], weeklyError(err))
		} else {
			n.items = pageData.Items
			n.total = pageData.Total
			n.problems = nil
			n.loaded = true
			n.version = gen
			n.key = key
		}
		n.mu.Unlock()
		a.Notify()
		return nil
	})
}

// visible applies the title filter to the loaded page. The backend query has
// no keyword term (SPEC-09 §5.3), so the page says "on this page" instead of
// pretending it searched the history.
func (n *Weekly) visible() []weeklyreview.ReportSummary {
	n.mu.Lock()
	list := append([]weeklyreview.ReportSummary(nil), n.items...)
	n.mu.Unlock()
	query := strings.ToLower(strings.TrimSpace(n.query))
	if query == "" {
		return list
	}
	out := list[:0]
	for _, item := range list {
		if strings.Contains(strings.ToLower(item.Title), query) {
			out = append(out, item)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Generation
// ---------------------------------------------------------------------------

// generateDialog collects the period and runs the generation as a managed,
// cancellable task. Selecting a period only refreshes the preview; nothing is
// created until the button is pressed.
func (n *Weekly) generateDialog(v *components.UI, a *app.App, projectID string, writable bool) {
	if !n.genOpen {
		return
	}
	// The skill editor takes the dialog's place while it is open: one modal
	// at a time, and the generation state survives underneath.
	if n.skillEditor.open {
		n.skillEditor.dialog(v, a, n.Services, projectID, func(updated weeklyreview.SkillSet) {
			n.mu.Lock()
			n.skills = updated
			n.mu.Unlock()
			n.genSkillLabel = ""
			a.Notify()
		})
		return
	}
	n.adoptPeriod()
	n.mu.Lock()
	generating, errText, note := n.generating, n.genErr, n.genNote
	n.mu.Unlock()

	// The quick chips: 本周 / 上周 / 自定义.
	seg := ui.Segmented(v.C, &n.genQuick,
		i18n.T("pages.weekly_review.period_current"),
		i18n.T("pages.weekly_review.period_previous"),
		i18n.T("pages.weekly_review.period_custom"),
	).Label(i18n.T("pages.weekly_review.field_period"))
	if seg.Changed() {
		n.quickPeriod()
	}

	period, periodErr := n.period()
	existing, found := n.existingFor(period)
	n.needOverwrite = found && existing.Status == weeklyreview.StatusDraft && strings.TrimSpace(existing.Excerpt) != ""
	n.periodBlocked = found && existing.Status == weeklyreview.StatusConfirmed

	v.Dialog(&n.genOpen, components.DialogOptions{
		Title:       i18n.T("pages.weekly_review.gen_title"),
		Description: i18n.T("pages.weekly_review.gen_desc"),
		Width:       560,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				if n.genTask != nil {
					n.genTask.Cancel()
				}
				n.genOpen = false
			}
			if v.Button(i18n.T("pages.weekly_review.btn_generate_run"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Icon:     icons.WeeklyReview,
				Loading:  generating,
				Disabled: generating || !writable || periodErr != nil || n.periodBlocked,
			}).Clicked() {
				n.generate(a, projectID)
			}
		},
	}, func() {
		if !n.genLoaded {
			v.Skeleton(components.SkeletonOptions{Lines: 2})
			return
		}
		if n.genAI {
			set := n.skillSetSnapshot()
			weeklySkillPicker(v, a, n.Services, projectID, set, &n.genSkillLabel, &n.genSkillID, &n.skillEditor)
		}
		if n.genQuick == 2 {
			start := ui.DateInput(v.C, &n.genStart).Label(i18n.T("pages.weekly_review.field_start"))
			if start.Changed() {
				n.normalizeDates()
			}
			end := ui.DateInput(v.C, &n.genEnd).Label(i18n.T("pages.weekly_review.field_end"))
			if end.Changed() {
				n.normalizeDates()
			}
		}
		if periodErr != nil {
			ui.Text(v.C, weeklyError(periodErr)).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		} else {
			v.Card(components.CardOptions{Title: i18n.T("pages.weekly_review.preview_title")}, func() {
				ui.Text(v.C, i18n.Tf("pages.weekly_review.preview_period", map[string]string{
					"start": period.StartDate(),
					"end":   period.EndDate(),
					"days":  strconv.Itoa(period.Days()),
				})).FontSize(tokens.Typography.Secondary.Size)
				switch {
				case found && existing.Status == weeklyreview.StatusConfirmed:
					ui.Text(v.C, i18n.T("pages.weekly_review.preview_confirmed")).
						FontSize(tokens.Typography.Secondary.Size).
						TextColor(mygo.Color(v.T.Palette.StatusWarning))
				case found:
					ui.Text(v.C, i18n.Tf("pages.weekly_review.preview_exists", map[string]string{"title": existing.Title})).
						FontSize(tokens.Typography.Secondary.Size).
						TextColor(mygo.Color(v.T.Palette.TextSecondary))
				default:
					ui.Text(v.C, i18n.T("pages.weekly_review.preview_new")).
						FontSize(tokens.Typography.Secondary.Size).
						TextColor(mygo.Color(v.T.Palette.TextSecondary))
				}
				if !n.genAI {
					ui.Text(v.C, i18n.T("pages.weekly_review.preview_no_ai")).
						FontSize(tokens.Typography.Secondary.Size).
						TextColor(mygo.Color(v.T.Palette.StatusWarning))
					v.Button(i18n.T("pages.weekly_review.btn_settings"), components.ButtonOptions{
						Variant: components.VariantGhost,
						Size:    components.SizeSmall,
						Icon:    icons.Settings,
						OnClick: func() {
							a.Navigate(ports.Route{Kind: ports.RouteSettings, Params: map[string]string{"tab": "ai"}})
						},
					})
				}
				if n.needOverwrite {
					v.Checkbox(&n.genOverwrite, i18n.T("pages.weekly_review.preview_overwrite"), components.ToggleOptions{
						Description: i18n.T("pages.weekly_review.preview_overwrite_desc"),
					})
				}
				ui.Text(v.C, i18n.T("pages.weekly_review.preview_no_instructions")).
					FontSize(tokens.Typography.Caption.Size).
					TextColor(mygo.Color(v.T.Palette.TextMuted))
			})
		}
		if note != "" {
			v.Banner(components.BannerOptions{
				Tone:    components.Warning,
				Title:   i18n.T("pages.weekly_review.gen_note_title"),
				Message: note,
			})
		}
		if errText != "" {
			v.Banner(components.BannerOptions{
				Tone:    components.Danger,
				Title:   i18n.T("pages.weekly_review.gen_failed_title"),
				Message: errText,
			})
		}
		if generating {
			v.Progress(components.ProgressOptions{Value: -1, Label: i18n.T("pages.weekly_review.gen_running")})
		}
	})
}

// quickPeriod applies the quick choice to the dates.
func (n *Weekly) quickPeriod() {
	switch n.genQuick {
	case 0:
		n.mu.Lock()
		def := n.defPeriod
		n.mu.Unlock()
		n.applyPeriod(weeklyCurrentWeek(def, time.Now()))
	case 1:
		n.mu.Lock()
		def := n.defPeriod
		n.mu.Unlock()
		n.applyPeriod(weeklyShiftPeriod(weeklyCurrentWeek(def, time.Now()), -7))
	}
}

// normalizeDates drops the time of day a date input may carry.
func (n *Weekly) normalizeDates() {
	if !n.genStart.IsZero() {
		n.genStart = time.Date(n.genStart.Year(), n.genStart.Month(), n.genStart.Day(), 0, 0, 0, 0, time.Local)
	}
	if !n.genEnd.IsZero() {
		n.genEnd = time.Date(n.genEnd.Year(), n.genEnd.Month(), n.genEnd.Day(), 0, 0, 0, 0, time.Local)
	}
}

// skillSetSnapshot is the frame's copy of the loaded skills.
func (n *Weekly) skillSetSnapshot() weeklyreview.SkillSet {
	n.mu.Lock()
	defer n.mu.Unlock()
	set := n.skills
	set.Skills = append([]weeklyreview.Skill(nil), n.skills.Skills...)
	return set
}

// period builds the half-open period of the dialog's dates.
func (n *Weekly) period() (weeklyreview.Period, error) {
	if n.genStart.IsZero() || n.genEnd.IsZero() {
		return weeklyreview.Period{}, &weeklyreview.ServiceError{
			Code: weeklyreview.ErrInvalidPeriod, Message: "period is empty",
		}
	}
	loc := time.Local
	n.mu.Lock()
	if !n.defPeriod.Start.IsZero() {
		loc = n.defPeriod.Start.Location()
	}
	n.mu.Unlock()
	start := time.Date(n.genStart.Year(), n.genStart.Month(), n.genStart.Day(), 0, 0, 0, 0, loc)
	end := time.Date(n.genEnd.Year(), n.genEnd.Month(), n.genEnd.Day(), 0, 0, 0, 0, loc)
	if end.Before(start) {
		return weeklyreview.Period{}, &weeklyreview.ServiceError{
			Code: weeklyreview.ErrInvalidPeriod, Message: "end before start",
		}
	}
	return weeklyreview.Period{Start: start, EndExclusive: end.AddDate(0, 0, 1)}, nil
}

// existingFor finds a loaded summary of the chosen period.
func (n *Weekly) existingFor(p weeklyreview.Period) (weeklyreview.ReportSummary, bool) {
	if p.Empty() {
		return weeklyreview.ReportSummary{}, false
	}
	n.mu.Lock()
	items := append([]weeklyreview.ReportSummary(nil), n.items...)
	n.mu.Unlock()
	start, end := p.StartDate(), p.EndDate()
	for _, item := range items {
		if item.DeletedAt == nil && item.Period.StartDate() == start && item.Period.EndDate() == end {
			return item, true
		}
	}
	return weeklyreview.ReportSummary{}, false
}

// generate runs GenerateForPeriod. With AI disabled it asks for the
// deterministic skeleton; with AI enabled a failed model call still stores the
// skeleton and reports why, which the dialog shows as a degradation note.
func (n *Weekly) generate(a *app.App, projectID string) {
	period, err := n.period()
	if err != nil {
		a.Toast(app.LevelWarning, i18n.T("pages.weekly_review.toast_period_invalid"), weeklyError(err))
		return
	}
	n.mu.Lock()
	if n.generating {
		n.mu.Unlock()
		return
	}
	n.generating = true
	n.genErr = ""
	n.genNote = ""
	n.mu.Unlock()

	api := n.Services.Weekly
	includeAI := n.genAI
	replace := !n.needOverwrite || n.genOverwrite
	skillID := n.genSkillID
	task := a.RunTask(i18n.T("pages.weekly_review.task_generate"), func(ctx context.Context, t *app.Task) error {
		gctx, cancel := context.WithCancel(ctx)
		t.SetCancel(cancel)
		defer cancel()
		in := weeklyreview.GeneratePeriodInput{
			Period:    period,
			IncludeAI: includeAI,
			Replace:   replace,
			Actor:     weeklyreview.ActorUser,
			SkillID:   skillID,
		}
		if includeAI {
			in.Progress = weeklySectionProgress(t)
		}
		t.Stage(i18n.T("pages.weekly_review.stage_collect"))
		t.Progress(10, 100)
		outcome, genErr := api.GenerateForPeriod(gctx, projectID, in)
		t.Stage(i18n.T("pages.weekly_review.stage_store"))
		t.Progress(90, 100)

		var report *weeklyreview.WeeklyReport
		if outcome != nil {
			report = outcome.Report
		}
		if gctx.Err() != nil {
			// Canceled from the dialog or the task strip: no navigation, no
			// toast; the report may or may not have been written.
			n.mu.Lock()
			n.generating = false
			n.mu.Unlock()
			a.Notify()
			return nil
		}
		degraded := report != nil && weeklyDegraded(genErr)
		switch {
		case report != nil && (genErr == nil || degraded):
			n.mu.Lock()
			n.generating = false
			n.genDone = true
			n.genReportID = report.ID
			if degraded {
				n.genNote = weeklyError(genErr)
			}
			n.mu.Unlock()
			n.shared.bump()
			switch {
			case degraded:
				a.Toast(app.LevelWarning, i18n.T("pages.weekly_review.toast_generated_skeleton"), n.genNote)
			case !includeAI:
				a.Toast(app.LevelSuccess, i18n.T("pages.weekly_review.toast_generated_skeleton"), i18n.T("pages.weekly_review.toast_skeleton_note"))
			default:
				a.Toast(app.LevelSuccess, i18n.T("pages.weekly_review.toast_generated"), report.Title)
			}
		case genErr != nil:
			n.mu.Lock()
			n.generating = false
			n.genErr = weeklyError(genErr)
			n.mu.Unlock()
			a.Toast(app.LevelDanger, i18n.T("pages.weekly_review.toast_generate_fail"), n.genErr)
		default:
			n.mu.Lock()
			n.generating = false
			n.mu.Unlock()
		}
		t.Progress(100, 100)
		a.Notify()
		return nil
	})
	n.genTask = task
}

// deleteDialog confirms the soft deletion of one report.
func (n *Weekly) deleteDialog(v *components.UI, a *app.App, projectID string) {
	if n.delID == "" {
		return
	}
	n.mu.Lock()
	deleting, errText := n.deleting, n.delErr
	n.mu.Unlock()
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title: i18n.T("pages.weekly_review.delete_title"),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				n.delID = ""
			}
			if v.Button(i18n.T("pages.weekly_review.delete_confirm"), components.ButtonOptions{
				Variant:  components.VariantDanger,
				Loading:  deleting,
				Disabled: deleting,
			}).Clicked() {
				n.delete(a, projectID)
			}
		},
	}, func() {
		ui.Text(v.C, i18n.Tf("pages.weekly_review.delete_message", map[string]string{"title": n.delTitle})).
			FontSize(tokens.Typography.Body.Size)
		if errText != "" {
			ui.Text(v.C, errText).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
	if !open && !deleting {
		n.delID = ""
	}
}

func (n *Weekly) delete(a *app.App, projectID string) {
	reportID := n.delID
	if reportID == "" {
		return
	}
	n.mu.Lock()
	if n.deleting {
		n.mu.Unlock()
		return
	}
	n.deleting = true
	n.delErr = ""
	n.mu.Unlock()
	api := n.Services.Weekly
	a.RunTask(i18n.T("pages.weekly_review.task_delete"), func(ctx context.Context, t *app.Task) error {
		err := api.DeleteReport(ctx, projectID, reportID)
		n.mu.Lock()
		n.deleting = false
		if err != nil {
			n.delErr = weeklyError(err)
		} else {
			kept := n.items[:0]
			for _, item := range n.items {
				if item.ID != reportID {
					kept = append(kept, item)
				}
			}
			n.items = kept
			n.delDone = true
		}
		n.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.weekly_review.toast_delete_fail"), weeklyError(err))
			a.Notify()
			return nil
		}
		n.shared.bump()
		a.Toast(app.LevelSuccess, i18n.T("pages.weekly_review.toast_deleted"), "")
		a.Notify()
		return nil
	})
}

func (n *Weekly) restore(a *app.App, reportID string) {
	n.mu.Lock()
	if n.restoringID != "" {
		n.mu.Unlock()
		return
	}
	n.restoringID = reportID
	n.mu.Unlock()
	api := n.Services.Weekly
	a.RunTask(i18n.T("pages.weekly_review.task_restore"), func(ctx context.Context, t *app.Task) error {
		err := api.RestoreReport(ctx, projectID(n.Services.App), reportID)
		message := ""
		if err != nil {
			message = weeklyError(err)
		}
		n.mu.Lock()
		n.restoringID = ""
		if err == nil {
			n.restoreDone = true
		}
		n.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.weekly_review.toast_restore_fail"), message)
		} else {
			a.Toast(app.LevelSuccess, i18n.T("pages.weekly_review.toast_restored"), "")
		}
		n.shared.bump()
		a.Notify()
		return nil
	})
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// weeklyStatusChoices returns the localized status filter and its stable keys.
func weeklyStatusChoices() ([]string, []string) {
	keys := []string{"all", "draft", "confirmed"}
	labels := []string{
		i18n.T("pages.weekly_review.status_all"),
		i18n.T("pages.weekly_review.status_draft"),
		i18n.T("pages.weekly_review.status_confirmed"),
	}
	return labels, keys
}

func weeklyStatusLabel(key string, labels, keys []string) string {
	for i, k := range keys {
		if k == key {
			return labels[i]
		}
	}
	return labels[0]
}

func weeklyStatusKey(label string, labels, keys []string) (string, bool) {
	for i, l := range labels {
		if l == label {
			return keys[i], true
		}
	}
	return "", false
}

// weeklyStatusText is the badge label of a report status.
func weeklyStatusText(s weeklyreview.Status) string {
	switch s {
	case weeklyreview.StatusConfirmed:
		return i18n.T("pages.weekly_review.status_confirmed")
	case weeklyreview.StatusDraft:
		return i18n.T("pages.weekly_review.status_draft")
	}
	return string(s)
}

func weeklyStatusTone(s weeklyreview.Status) components.Tone {
	if s == weeklyreview.StatusConfirmed {
		return components.Success
	}
	return components.Info
}

// weeklyPeriodText renders the report window as the list shows it.
func weeklyPeriodText(p weeklyreview.Period) string {
	if p.Empty() {
		return "—"
	}
	return p.StartDate() + " – " + p.EndDate()
}

// weeklyCurrentWeek normalizes DefaultPeriod (which honours
// weekly.week_start_day and weekly.default_period) to the week containing now.
func weeklyCurrentWeek(def weeklyreview.Period, now time.Time) weeklyreview.Period {
	if def.Empty() {
		return def
	}
	loc := def.Start.Location()
	now = now.In(loc)
	p := def
	for now.Before(p.Start) {
		p = weeklyShiftPeriod(p, -7)
	}
	for !now.Before(p.EndExclusive) {
		p = weeklyShiftPeriod(p, 7)
	}
	return p
}

// weeklyShiftPeriod moves a week-aligned period by whole days.
func weeklyShiftPeriod(p weeklyreview.Period, days int) weeklyreview.Period {
	return weeklyreview.Period{
		Start:        p.Start.AddDate(0, 0, days),
		EndExclusive: p.EndExclusive.AddDate(0, 0, days),
	}
}

// parseWeeklyDate parses a YYYY-MM-DD filter bound; empty and invalid text
// are no bound.
func parseWeeklyDate(text string) *time.Time {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	t, err := time.ParseInLocation("2006-01-02", text, time.Local)
	if err != nil {
		return nil
	}
	return &t
}

// sortedWeeklySummaries orders the loaded page by the column header the table
// reports. The backend orders by period or updated_at; title and confirmation
// are ordered here, across the page the table shows.
func sortedWeeklySummaries(list []weeklyreview.ReportSummary, order *ui.SortOrder) []weeklyreview.ReportSummary {
	out := append([]weeklyreview.ReportSummary(nil), list...)
	if order == nil || order.Column == "" {
		return out
	}
	less := func(a, b weeklyreview.ReportSummary) bool {
		switch order.Column {
		case "title":
			return strings.ToLower(a.Title) < strings.ToLower(b.Title)
		case "updated":
			return a.UpdatedAt.Before(b.UpdatedAt)
		case "confirmed":
			if a.ConfirmedAt == nil || b.ConfirmedAt == nil {
				return a.ConfirmedAt == nil && b.ConfirmedAt != nil
			}
			return a.ConfirmedAt.Before(*b.ConfirmedAt)
		}
		return a.Period.Start.Before(b.Period.Start)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if order.Descending {
			return less(out[j], out[i])
		}
		return less(out[i], out[j])
	})
	return out
}

// visibleWeeklyRows is the table height for n rows.
func visibleWeeklyRows(n int) float32 {
	visible := n
	if visible > weeklyListRows {
		visible = weeklyListRows
	}
	return float32(visible)*44 + 40
}

// weeklyDegraded reports the two codes that leave a usable skeleton in the
// report: AI is not wired, or the model failed and the deterministic
// seven-section body was stored instead (AC-12).
// weeklySectionProgress renders the section-by-section generation in the task
// strip: "3/7 · 理论研究进展". The bar fills from 10% to 85%, leaving the tail
// for validation and the draft write.
func weeklySectionProgress(t *app.Task) weeklyreview.SectionProgress {
	return func(done, total int, section weeklyreview.SectionKey) {
		if total <= 0 {
			return
		}
		if section == "" {
			t.Stage(i18n.T("pages.weekly_review.stage_store"))
			t.Progress(90, 100)
			return
		}
		t.Stage(i18n.Tf("pages.weekly_review.stage_section", map[string]string{
			"done":  strconv.Itoa(done + 1),
			"total": strconv.Itoa(total),
			"title": weeklyreview.SectionTitle(section),
		}))
		t.Progress(10+done*75/total, 100)
	}
}

func weeklyDegraded(err error) bool {
	switch weeklyreview.CodeOf(err) {
	case weeklyreview.ErrAIUnavailable, weeklyreview.ErrAIOutputInvalid:
		return true
	}
	return false
}

// weeklyError localizes a SPEC-09 ServiceError by its stable code; foreign
// errors keep errtext's wording.
func weeklyError(err error) string {
	if err == nil {
		return ""
	}
	switch weeklyreview.CodeOf(err) {
	case weeklyreview.ErrNotFound:
		return i18n.T("pages.weekly_review.err_not_found")
	case weeklyreview.ErrInvalidPeriod:
		return i18n.T("pages.weekly_review.err_invalid_period")
	case weeklyreview.ErrPeriodOccupied:
		return i18n.T("pages.weekly_review.err_period_occupied")
	case weeklyreview.ErrProjectArchived:
		return i18n.T("pages.weekly_review.err_project_archived")
	case weeklyreview.ErrConfirmedReadOnly:
		return i18n.T("pages.weekly_review.err_confirmed_read_only")
	case weeklyreview.ErrReportNotEmpty:
		return i18n.T("pages.weekly_review.err_report_not_empty")
	case weeklyreview.ErrContentConflict:
		return i18n.T("pages.weekly_review.err_content_conflict")
	case weeklyreview.ErrContentTooLarge:
		return i18n.T("pages.weekly_review.err_content_too_large")
	case weeklyreview.ErrUnresolvedViolations:
		return i18n.T("pages.weekly_review.err_unresolved_violations")
	case weeklyreview.ErrConfirmationRequired:
		return i18n.T("pages.weekly_review.err_confirmation_required")
	case weeklyreview.ErrUnsupportedFormat:
		return i18n.T("pages.weekly_review.err_unsupported_format")
	case weeklyreview.ErrExportExists:
		return i18n.T("pages.weekly_review.err_export_exists")
	case weeklyreview.ErrInvalidExportPath:
		return i18n.T("pages.weekly_review.err_invalid_export_path")
	case weeklyreview.ErrExportDirMissing:
		return i18n.T("pages.weekly_review.err_export_dir_missing")
	case weeklyreview.ErrAIUnavailable:
		return i18n.T("pages.weekly_review.err_ai_unavailable")
	case weeklyreview.ErrAIOutputInvalid:
		return i18n.T("pages.weekly_review.err_ai_output_invalid")
	case weeklyreview.ErrInvalidInput:
		return i18n.T("pages.weekly_review.err_invalid_input")
	case weeklyreview.ErrInvalidActor:
		return i18n.T("pages.weekly_review.err_invalid_actor")
	case weeklyreview.ErrProjectNotFound:
		return i18n.T("pages.weekly_review.err_project_not_found")
	case weeklyreview.ErrInternal:
		return i18n.T("pages.weekly_review.err_internal")
	}
	return aiErrorText(err)
}

// weeklyEntityRoute maps a digest/reference entity type to its route kind.
func weeklyEntityRoute(typ string) (ports.RouteKind, bool) {
	switch typ {
	case string(schema.EntityIdea):
		return ports.RouteIdea, true
	case string(schema.EntityExperiment):
		return ports.RouteExperiment, true
	case string(schema.EntityRun):
		return ports.RouteRun, true
	case string(schema.EntityTask):
		return ports.RouteTask, true
	case string(schema.EntityNote):
		return ports.RouteNote, true
	case string(schema.EntityPaper):
		return ports.RoutePaper, true
	case string(schema.EntityWeeklyReport):
		return ports.RouteWeeklyReport, true
	}
	return "", false
}

// weeklyEntityLabel is the reader-facing name of a referenced entity type.
func weeklyEntityLabel(typ string) string {
	switch typ {
	case string(schema.EntityIdea):
		return i18n.T("pages.weekly_review.ref_type_idea")
	case string(schema.EntityExperiment):
		return i18n.T("pages.weekly_review.ref_type_experiment")
	case string(schema.EntityRun):
		return i18n.T("pages.weekly_review.ref_type_run")
	case string(schema.EntityTask):
		return i18n.T("pages.weekly_review.ref_type_task")
	case string(schema.EntityNote):
		return i18n.T("pages.weekly_review.ref_type_note")
	case string(schema.EntityPaper):
		return i18n.T("pages.weekly_review.ref_type_paper")
	case string(schema.EntityWeeklyReport):
		return i18n.T("pages.weekly_review.ref_type_weekly_report")
	}
	return typ
}

// weeklyRefPattern matches the [[ref:type:id]] references the report template
// and the prompt ask for.
var weeklyRefPattern = regexp.MustCompile(`\[\[ref:([a-z_]+):([^\]]+)\]\]`)

// weeklyRefMarkdown rewrites references into Markdown links so the renderer
// makes them clickable. A reference of an unknown type, or one the report did
// not record in meta.refs, shows as 引用失效 instead of a dead link; a target
// deleted after the report was written cannot be detected without another
// lookup, so it stays a link (documented gap).
func weeklyRefMarkdown(content string, refs map[string]bool) string {
	return weeklyRefPattern.ReplaceAllStringFunc(content, func(match string) string {
		groups := weeklyRefPattern.FindStringSubmatch(match)
		typ, id := groups[1], strings.TrimSpace(groups[2])
		if _, ok := weeklyEntityRoute(typ); !ok {
			return i18n.T("pages.weekly_review.ref_invalid")
		}
		if len(refs) > 0 && !refs[typ+":"+id] {
			return i18n.Tf("pages.weekly_review.ref_invalid_named", map[string]string{
				"type": weeklyEntityLabel(typ), "id": shortWeeklyID(id),
			})
		}
		label := weeklyEntityLabel(typ) + " " + shortWeeklyID(id)
		return "[" + label + "](researchos://entity/" + typ + "/" + id + ")"
	})
}

// shortWeeklyID is the readable part of a UUIDv7 identity.
func shortWeeklyID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// weeklyOpenRef navigates to an entity reference the renderer linked.
func weeklyOpenRef(a *app.App, url string) {
	const prefix = "researchos://entity/"
	if !strings.HasPrefix(url, prefix) {
		return
	}
	parts := strings.Split(strings.TrimPrefix(url, prefix), "/")
	if len(parts) != 2 {
		return
	}
	kind, ok := weeklyEntityRoute(parts[0])
	if !ok {
		return
	}
	a.Navigate(ports.Route{Kind: kind, EntityID: parts[1]})
}

// weeklyHeadings returns the level-2 headings of a report body in order.
func weeklyHeadings(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "## ") {
			title := strings.TrimSpace(strings.TrimPrefix(line, "## "))
			if title != "" {
				out = append(out, title)
			}
		}
	}
	return out
}

// weeklySectionHeading finds the heading of one template section.
func weeklySectionHeading(content string, key weeklyreview.SectionKey) string {
	title := ""
	for _, sec := range weeklyreview.DefaultTemplate().Sections {
		if sec.Key == key {
			title = sec.Title
			break
		}
	}
	if title == "" {
		return ""
	}
	for _, heading := range weeklyHeadings(content) {
		if strings.Contains(heading, title) {
			return heading
		}
	}
	return ""
}

// weeklySectionTitle is the template title of a section key.
func weeklySectionTitle(key weeklyreview.SectionKey) string {
	for _, sec := range weeklyreview.DefaultTemplate().Sections {
		if sec.Key == key {
			return sec.Title
		}
	}
	return string(key)
}
