package shell

// The Project Overview page is the SPEC-03 §6.3 screen: the open project's
// header and the eight fixed widgets of §3.2, loaded through the overview
// aggregation service. The service owns the SQL, the 15s cache, the per-widget
// timeout and the degraded fallback; this page only renders a snapshot and
// turns clicks into routes. It never opens a database.

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"

	"researchos/internal/i18n"
	"researchos/internal/overview"
	"researchos/internal/ports"
	"researchos/internal/project"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/errtext"
	"researchos/internal/ui/formula"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// OverviewAPI is the part of overview.Service the page uses: the cached read,
// the single-widget refresh, and the invalidate before a manual reload. The
// composition root supplies the workspace-scoped service.
type OverviewAPI interface {
	Load(ctx context.Context, projectID string) (*overview.Snapshot, error)
	Refresh(ctx context.Context, projectID string, w overview.WidgetID) (*overview.Snapshot, error)
	Invalidate(projectID string)
}

// overviewWideWidth is the §6.3 breakpoint: a window at least this wide shows
// two card columns, anything narrower shows one.
const overviewWideWidth float32 = 1100

// overviewFocusRunes bounds the focus text a card shows. focus_md is allowed
// 20000 characters; the complete text stays reachable through the edit dialog.
const overviewFocusRunes = 1200

// overviewWidgetOrder is the §3.2 card order. The page renders this order even
// before the first snapshot arrives, so the skeleton has the final layout.
var overviewWidgetOrder = [...]overview.WidgetID{
	overview.WidgetCurrentFocus,
	overview.WidgetActiveIdeas,
	overview.WidgetRecentExperiments,
	overview.WidgetMyTasks,
	overview.WidgetRecentNotes,
	overview.WidgetLiterature,
	overview.WidgetBlockedTasks,
	overview.WidgetWeeklyProgress,
}

// overviewState is the page's session state: the last snapshot, the load and
// refresh in flight, and the failures the last read reported.
type overviewState struct {
	api OverviewAPI

	mu      sync.Mutex
	loading bool
	// loaded reports that the last read of this project finished, successfully
	// or not: it gates the entry-triggered read so a failure is not retried
	// every frame. Manual retry and the next entry clear it.
	loaded     bool
	project    string
	seq        uint64
	snap       *overview.Snapshot
	problems   []string
	refreshing overview.WidgetID

	// md is the Markdown reader of the focus card. Options are built once so
	// every frame does not re-parse the configuration.
	md *markdown.Options
}

// overviewPage renders the project header and the eight Overview widgets.
func overviewPage(ctx context.Context, a *app.App, projects *projectsPage, api OverviewAPI) Page {
	state := &overviewState{api: api}
	return func(v *components.UI, r ports.Route) {
		p := a.Project()
		if p == nil {
			state.noProject(v, a)
			return
		}
		if api == nil {
			state.notReady(v)
			return
		}
		state.syncProject(a, p, a.RouteSeq())
		snap, loading, problems := state.snapshot()
		state.render(v, a, projects, ctx, p, snap, loading, problems)
	}
}

// noProject is the honest state of an Overview without a project to describe.
func (o *overviewState) noProject(v *components.UI, a *app.App) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{Title: pageTitle(ports.RouteOverview)})
		v.EmptyState(components.EmptyStateOptions{
			Icon:        icons.Projects,
			Title:       i18n.T("shell.overview.empty_no_project_title"),
			Reason:      i18n.T("shell.overview.empty_no_project_reason"),
			ActionLabel: i18n.T("shell.overview.btn_projects"),
			OnAction:    func() { a.Navigate(ports.Route{Kind: app.RouteProjects}) },
		})
	}})
}

// notReady is the state before the composition root supplied the aggregation
// service. It should not happen in the running app; saying so beats an empty
// page that looks like a project with no data.
func (o *overviewState) notReady(v *components.UI) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{Title: pageTitle(ports.RouteOverview)})
		v.ErrorState(components.ErrorStateOptions{
			Title: i18n.T("shell.overview.not_ready_title"),
			Cause: i18n.T("shell.overview.not_ready_reason"),
		})
	}})
}

// snapshot returns the state the frame renders. The snapshot is treated as
// read-only, as the service documents.
func (o *overviewState) snapshot() (*overview.Snapshot, bool, []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.snap, o.loading, append([]string(nil), o.problems...)
}

// syncProject clears the cached snapshot when another project opens and
// starts a read when the page is entered or the project changed. A repaint
// alone keeps the cached snapshot.
func (o *overviewState) syncProject(a *app.App, p *project.Project, seq uint64) {
	projectID := p.ID
	o.mu.Lock()
	entered := seq != o.seq
	o.seq = seq
	if o.project != projectID {
		o.project = projectID
		o.snap = nil
		o.loaded = false
		o.problems = nil
		o.refreshing = ""
		entered = true
	}
	stale := entered && o.snap != nil && projectSnapshotStale(o.snap, p)
	if entered {
		// Keep the snapshot on screen; mark it stale so the read below runs
		// and the cards show last data while the new one arrives.
		o.loaded = false
	}
	need := !o.loading && !o.loaded
	o.mu.Unlock()
	if stale {
		// The edit dialog writes the project row on the Projects page, so a
		// cached snapshot can predate the new focus or goals. Only a visible
		// difference bypasses the 15s cache; an unchanged project rides it.
		o.api.Invalidate(projectID)
	}
	if need {
		o.reload(a, projectID)
	}
}

// projectSnapshotStale reports a cached snapshot whose project-derived fields
// no longer match the open project row. The header, goals and focus are
// written by the edit dialog; the archive toggle changes Writable.
func projectSnapshotStale(snap *overview.Snapshot, p *project.Project) bool {
	archived := p.Status == project.StatusArchived
	return snap.Project.Name != p.Name ||
		snap.Project.Summary != p.Summary ||
		snap.Project.Goals != p.Goals ||
		snap.CurrentFocus.FocusMD != p.FocusMD ||
		snap.Writable != !archived
}

// reload reads the snapshot off the UI thread. The previous snapshot stays on
// screen while the new read runs, as §6.3 asks.
func (o *overviewState) reload(a *app.App, projectID string) {
	api := o.api
	o.mu.Lock()
	if o.loading || o.refreshing != "" {
		o.mu.Unlock()
		return
	}
	o.loading = true
	o.mu.Unlock()

	a.RunTask(i18n.T("shell.overview.task_load"), func(ctx context.Context, t *app.Task) error {
		snap, err := api.Load(ctx, projectID)
		o.mu.Lock()
		o.loading = false
		if o.project == projectID {
			// Every finished attempt counts as loaded, so a failed read shows
			// its error once instead of retrying on the next frame.
			o.loaded = true
			switch {
			case err != nil:
				o.problems = append(o.problems[:0], overviewErrorText(err))
			case snap != nil:
				o.snap = snap
				o.problems = nil
			}
		}
		o.mu.Unlock()
		a.Notify()
		return nil
	})
}

// refreshAll drops the cached snapshot and reads it again, for the page-level
// refresh button.
func (o *overviewState) refreshAll(a *app.App, projectID string) {
	if o.busy() {
		return
	}
	o.api.Invalidate(projectID)
	o.reload(a, projectID)
}

// busy reports a load or a single-widget refresh in flight.
func (o *overviewState) busy() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.loading || o.refreshing != ""
}

// refreshWidget re-reads one degraded card. The service keeps every other
// card from the cached snapshot, so one slow query does not reload the page.
func (o *overviewState) refreshWidget(a *app.App, projectID string, w overview.WidgetID) {
	api := o.api
	o.mu.Lock()
	if o.loading || o.refreshing != "" {
		o.mu.Unlock()
		return
	}
	o.refreshing = w
	o.mu.Unlock()

	a.RunTask(i18n.T("shell.overview.task_refresh"), func(ctx context.Context, t *app.Task) error {
		snap, err := api.Refresh(ctx, projectID, w)
		o.mu.Lock()
		o.refreshing = ""
		if o.project == projectID {
			switch {
			case err != nil:
				o.problems = append(o.problems[:0], overviewErrorText(err))
			case snap != nil:
				o.snap = snap
				o.loaded = true
				o.problems = nil
			}
		}
		o.mu.Unlock()
		a.Notify()
		return nil
	})
}

// refreshingWidget reports the single card whose refresh is in flight.
func (o *overviewState) refreshingWidget(w overview.WidgetID) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.refreshing == w
}

// render builds the header, the state banners and the card grid.
func (o *overviewState) render(v *components.UI, a *app.App, projects *projectsPage, ctx context.Context, p *project.Project, snap *overview.Snapshot, loading bool, problems []string) {
	archived := p.Status == project.StatusArchived
	v.PageContainer(components.PageContainerOptions{MaxWidth: components.ReadingWidth * 1.4, Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       p.Name,
			Description: p.Summary,
			Actions: func() {
				busy := o.busy()
				v.IconButton(icons.Refresh, i18n.T("shell.overview.btn_refresh"), components.IconButtonOptions{
					Variant:  components.VariantGhost,
					Size:     components.SizeSmall,
					Loading:  loading,
					Disabled: busy,
					OnClick:  func() { o.refreshAll(a, p.ID) },
				})
				v.Button(i18n.T("shell.overview.btn_open_folder"), components.ButtonOptions{
					Variant: components.VariantGhost,
					Size:    components.SizeSmall,
					Icon:    icons.FolderOpen,
					OnClick: func() { openProjectFolder(a, p.ID) },
				})
				if !archived {
					v.Button(i18n.T("shell.overview.btn_edit"), components.ButtonOptions{
						Variant: components.VariantGhost,
						Size:    components.SizeSmall,
						Icon:    icons.Edit,
						OnClick: func() { requestDialog(a, editProjectParam, p.ID) },
					})
				}
				archiveLabel, archiveIcon := i18n.T("shell.overview.btn_archive"), icons.Archive
				if archived {
					archiveLabel, archiveIcon = i18n.T("shell.overview.btn_unarchive"), icons.ArchiveRestore
				}
				v.Button(archiveLabel, components.ButtonOptions{
					Variant: components.VariantGhost,
					Size:    components.SizeSmall,
					Icon:    archiveIcon,
					OnClick: func() { projects.archive(a, *p) },
				})
				v.Button(i18n.T("shell.overview.btn_close"), components.ButtonOptions{
					Variant: components.VariantGhost,
					Size:    components.SizeSmall,
					OnClick: func() { closeProject(a, ctx, p.ID) },
				})
			},
		})
		if archived {
			v.Banner(components.BannerOptions{
				Tone:    components.Warning,
				Title:   i18n.T("shell.overview.readonly_title"),
				Message: i18n.T("shell.overview.readonly_message"),
			})
		}
		for _, problem := range problems {
			v.Banner(components.BannerOptions{
				Tone:    components.Danger,
				Title:   i18n.T("shell.overview.banner_load_fail"),
				Message: problem,
			})
		}
		switch {
		case snap == nil && loading:
			o.skeletonGrid(v)
		case snap == nil:
			v.ErrorState(components.ErrorStateOptions{
				Title:      i18n.T("shell.overview.error_title"),
				Cause:      strings.Join(problems, "\n"),
				RetryLabel: i18n.T("shell.overview.error_retry"),
				OnRetry:    func() { o.refreshAll(a, p.ID) },
			})
		default:
			o.grid(v, a, p, snap, archived)
		}
	}})
}

// skeletonGrid reserves the final two-column layout while the first snapshot
// loads, so the cards do not jump in when the data arrives.
func (o *overviewState) skeletonGrid(v *components.UI) {
	two := overviewTwoColumn(v)
	cards := make([]func(), 0, len(overviewWidgetOrder))
	for _, w := range overviewWidgetOrder {
		w := w
		cards = append(cards, func() {
			card := v.Card(components.CardOptions{Title: cardTitle(w)}, func() {
				v.Skeleton(components.SkeletonOptions{Lines: 3})
			})
			if two {
				card.Grow(1)
			}
		})
	}
	overviewGrid(v, two, cards)
}

// grid renders the eight cards in §3.2 order and hides the ones the service
// reports unavailable.
func (o *overviewState) grid(v *components.UI, a *app.App, p *project.Project, snap *overview.Snapshot, archived bool) {
	two := overviewTwoColumn(v)
	cards := make([]func(), 0, len(overviewWidgetOrder))
	for _, w := range overviewWidgetOrder {
		st := widgetState(snap, w)
		if !st.Available {
			continue
		}
		w, st := w, st
		cards = append(cards, func() {
			o.card(v, a, p, snap, archived, w, st, two)
		})
	}
	overviewGrid(v, two, cards)
}

// overviewTwoColumn reports whether the window is wide enough for §6.3's two
// columns. The breakpoint is the window width, so it does not move when the
// sidebar collapses.
func overviewTwoColumn(v *components.UI) bool {
	width, _ := v.C.Size()
	return width >= overviewWideWidth
}

// overviewGrid lays the cards out in the §6.3 grid: two growing cards on each
// row when the window is wide, one column of full-width cards below 1100px. A
// row with a single card keeps an empty second column, so a hidden widget does
// not stretch the card beside it.
func overviewGrid(v *components.UI, two bool, cards []func()) {
	ui.Column(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
		if !two {
			for _, card := range cards {
				card()
			}
			return
		}
		for i := 0; i < len(cards); i += 2 {
			i := i
			ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).AlignItems(ui.Start).Children(func() {
				cards[i]()
				if i+1 < len(cards) {
					cards[i+1]()
				} else {
					ui.Spacer(v.C).Grow(1)
				}
			})
		}
	})
}

// card renders one widget: its title, the unconfirmed badge, the degraded
// refresh, and the "view all" action. Grow fills half of a two-column row.
func (o *overviewState) card(v *components.UI, a *app.App, p *project.Project, snap *overview.Snapshot, archived bool, w overview.WidgetID, st overview.WidgetState, grow bool) {
	viewAll := overviewViewAll(a, p.ID, snap, w, archived)
	card := v.Card(components.CardOptions{
		Title: cardTitle(w),
		Actions: func() {
			if st.PendingConfirmation {
				v.Badge(i18n.T("shell.overview.badge_pending"), components.Neutral).
					Tooltip(pendingTooltip(w))
			}
			if st.Degraded {
				refreshing := o.refreshingWidget(w)
				v.IconButton(icons.Refresh, i18n.T("shell.overview.btn_refresh_widget"), components.IconButtonOptions{
					Variant:  components.VariantGhost,
					Size:     components.SizeSmall,
					Loading:  refreshing,
					Disabled: refreshing,
					OnClick:  func() { o.refreshWidget(a, p.ID, w) },
				})
			}
			v.Button(i18n.T("shell.overview.btn_view_all"), components.ButtonOptions{
				Variant: components.VariantGhost,
				Size:    components.SizeSmall,
				OnClick: viewAll,
			})
		},
	}, func() {
		if st.Degraded {
			ui.Text(v.C, i18n.T("shell.overview.degraded_hint")).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(v.T.Palette.TextMuted))
		}
		o.cardBody(v, a, p, snap, w)
	})
	if grow {
		card.Grow(1)
	}
}

// cardBody renders the payload of one widget. Every branch ends in either real
// data or a compact empty state that says what is missing and where to add it.
func (o *overviewState) cardBody(v *components.UI, a *app.App, p *project.Project, snap *overview.Snapshot, w overview.WidgetID) {
	archived := p.Status == project.StatusArchived
	switch w {
	case overview.WidgetCurrentFocus:
		o.focusBody(v, a, p, snap.CurrentFocus, archived)
	case overview.WidgetActiveIdeas:
		if len(snap.ActiveIdeas) == 0 {
			o.empty(v, i18n.T("shell.overview.ideas_empty"), i18n.T("shell.overview.ideas_create"),
				func() { a.Navigate(ports.Route{Kind: app.RouteIdeas, ProjectID: p.ID}) })
			return
		}
		for _, idea := range snap.ActiveIdeas {
			idea := idea
			overviewRow(v, idea.Title, func() {
				v.Badge(ideaStatusLabel(idea.Status), ideaStatusTone(idea.Status))
			}, func() {
				a.Navigate(ports.Route{Kind: ports.RouteIdea, ProjectID: p.ID, EntityID: idea.ID})
			})
		}
	case overview.WidgetRecentExperiments:
		if len(snap.RecentExperiments) == 0 {
			o.empty(v, i18n.T("shell.overview.experiments_empty"), i18n.T("shell.overview.experiments_create"),
				func() { a.Navigate(ports.Route{Kind: app.RouteExperiments, ProjectID: p.ID}) })
			return
		}
		for _, exp := range snap.RecentExperiments {
			exp := exp
			overviewRow(v, exp.Title, func() {
				ui.Text(v.C, runStatusLabel(exp.LastRunStatus)).
					FontSize(tokens.Typography.Caption.Size).
					TextColor(mygo.Color(v.T.Palette.TextSecondary)).
					SingleLine()
				if exp.RunCount > 0 {
					ui.Text(v.C, i18n.Tf("shell.overview.experiment_runs", map[string]string{
						"n": strconv.Itoa(exp.RunCount),
					})).FontSize(tokens.Typography.Caption.Size).
						TextColor(mygo.Color(v.T.Palette.TextMuted)).
						SingleLine()
				}
			}, func() {
				a.Navigate(ports.Route{Kind: ports.RouteExperiment, ProjectID: p.ID, EntityID: exp.ID})
			})
		}
	case overview.WidgetMyTasks:
		if len(snap.MyTasks.Items) == 0 {
			o.empty(v, i18n.T("shell.overview.tasks_empty"), i18n.T("shell.overview.tasks_create"),
				func() { a.Navigate(ports.Route{Kind: app.RouteKanban, ProjectID: p.ID}) })
			return
		}
		for _, task := range snap.MyTasks.Items {
			task := task
			overviewRow(v, task.Title, func() {
				if task.Blocked {
					v.Badge(i18n.T("shell.overview.task_blocked"), components.Warning)
				}
				v.Badge(taskPriorityLabel(task.Priority), taskPriorityTone(task.Priority))
				if task.Deadline != nil {
					ui.Text(v.C, i18n.Tf("shell.overview.task_deadline", map[string]string{
						"date": task.Deadline.Local().Format("2006-01-02"),
					})).FontSize(tokens.Typography.Caption.Size).
						TextColor(mygo.Color(v.T.Palette.TextMuted)).
						SingleLine()
				}
			}, func() {
				a.Navigate(ports.Route{Kind: ports.RouteTask, ProjectID: p.ID, EntityID: task.ID})
			})
		}
	case overview.WidgetRecentNotes:
		if len(snap.RecentNotes) == 0 {
			o.empty(v, i18n.T("shell.overview.notes_empty"), i18n.T("shell.overview.notes_create"),
				func() { a.Navigate(ports.Route{Kind: app.RouteNotes, ProjectID: p.ID}) })
			return
		}
		for _, note := range snap.RecentNotes {
			note := note
			overviewRow(v, note.Title, func() {
				ui.Text(v.C, note.UpdatedAt.Local().Format("2006-01-02")).
					FontSize(tokens.Typography.Caption.Size).
					TextColor(mygo.Color(v.T.Palette.TextMuted)).
					FontFeatures("tnum")
			}, func() {
				a.Navigate(ports.Route{Kind: ports.RouteNote, ProjectID: p.ID, EntityID: note.ID})
			})
		}
	case overview.WidgetLiterature:
		if len(snap.Literature) == 0 {
			o.empty(v, i18n.T("shell.overview.literature_empty"), i18n.T("shell.overview.literature_add"),
				func() { a.Navigate(ports.Route{Kind: app.RouteLiterature, ProjectID: p.ID}) })
			return
		}
		for _, paper := range snap.Literature {
			paper := paper
			overviewRow(v, paper.Title, func() {
				if meta := paperMeta(paper); meta != "" {
					ui.Text(v.C, meta).
						FontSize(tokens.Typography.Caption.Size).
						TextColor(mygo.Color(v.T.Palette.TextMuted)).
						SingleLine()
				}
				if paper.Missing {
					v.Badge(i18n.T("shell.overview.paper_missing"), components.Warning)
				}
			}, func() {
				a.Navigate(ports.Route{Kind: ports.RoutePaper, EntityID: paper.ID})
			})
		}
	case overview.WidgetBlockedTasks:
		if len(snap.BlockedTasks.Items) == 0 {
			o.empty(v, i18n.T("shell.overview.blocked_empty"), i18n.T("shell.overview.blocked_open"),
				func() {
					a.Navigate(ports.Route{
						Kind:      app.RouteKanban,
						ProjectID: p.ID,
						Params:    map[string]string{"view": "blocked"},
					})
				})
			return
		}
		for _, task := range snap.BlockedTasks.Items {
			task := task
			overviewRow(v, task.Title, func() {
				v.Badge(i18n.T("shell.overview.task_blocked"), components.Warning)
			}, func() {
				a.Navigate(ports.Route{Kind: ports.RouteTask, ProjectID: p.ID, EntityID: task.ID})
			})
		}
	case overview.WidgetWeeklyProgress:
		o.weeklyBody(v, a, p, snap.WeeklyProgress)
	}
}

// focusBody renders Current Focus: focus_md, else the goals excerpt, plus the
// three newest active ideas. The edit dialog owns the complete text.
func (o *overviewState) focusBody(v *components.UI, a *app.App, p *project.Project, focus overview.FocusWidget, archived bool) {
	text := strings.TrimSpace(focus.FocusMD)
	if text == "" {
		text = strings.TrimSpace(focus.GoalsExcerpt)
	}
	if text == "" {
		var action func()
		if !archived {
			action = func() { requestDialog(a, editProjectParam, p.ID) }
		}
		o.empty(v, i18n.T("shell.overview.focus_empty"), i18n.T("shell.overview.btn_edit"), action)
	} else {
		o.markdown(v, truncateRunes(text, overviewFocusRunes))
	}
	if len(focus.TopIdeas) == 0 {
		return
	}
	ui.Text(v.C, i18n.T("shell.overview.card_ideas")).
		FontSize(tokens.Typography.Caption.Size).
		TextColor(mygo.Color(v.T.Palette.TextMuted))
	for _, idea := range focus.TopIdeas {
		idea := idea
		overviewRow(v, idea.Title, func() {
			v.Badge(ideaStatusLabel(idea.Status), ideaStatusTone(idea.Status))
		}, func() {
			a.Navigate(ports.Route{Kind: ports.RouteIdea, ProjectID: p.ID, EntityID: idea.ID})
		})
	}
}

// weeklyBody renders the confirmed report of the week, or the activity counts
// of the fallback. The counts are the unconfirmed weekly-planning projection,
// so the service marks that card pending; a confirmed report is not.
func (o *overviewState) weeklyBody(v *components.UI, a *app.App, p *project.Project, weekly overview.WeeklyWidget) {
	if weekly.ReportID != "" {
		v.Badge(i18n.T("shell.overview.weekly_confirmed"), components.Success)
		if strings.TrimSpace(weekly.Excerpt) == "" {
			o.empty(v, i18n.T("shell.overview.weekly_no_excerpt"), "", nil)
			return
		}
		ui.Text(v.C, weekly.Excerpt).
			FontSize(tokens.Typography.Body.Size).
			TextColor(mygo.Color(v.T.Palette.TextSecondary))
		return
	}
	if len(weekly.ActivityCounts) == 0 {
		o.empty(v, i18n.T("shell.overview.weekly_empty"), i18n.T("shell.overview.weekly_open"),
			func() { a.Navigate(ports.Route{Kind: app.RouteWeeklyReview, ProjectID: p.ID}) })
		return
	}
	ui.Text(v.C, i18n.T("shell.overview.weekly_activity")).
		FontSize(tokens.Typography.Caption.Size).
		TextColor(mygo.Color(v.T.Palette.TextMuted))
	for _, row := range activityRows(weekly.ActivityCounts) {
		ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
			ui.Text(v.C, activityLabel(row.label)).
				FontSize(tokens.Typography.Body.Size).
				Grow(1)
			ui.Text(v.C, strconv.Itoa(row.count)).
				FontSize(tokens.Typography.Numeric.Size).
				FontFeatures("tnum").
				TextColor(mygo.Color(v.T.Palette.TextSecondary))
		})
	}
}

// markdown renders the focus text with the same reader the note and idea pages
// use, so headings and lists keep their meaning.
func (o *overviewState) markdown(v *components.UI, text string) {
	if o.md == nil {
		o.md = &markdown.Options{
			Extensions: parser.Default | parser.ExtDollarMath,
			Math:       formula.Block,
			InlineMath: formula.Inline,
		}
	}
	markdown.View(v.C, text, o.md).FillWidth()
}

// empty is the compact empty state inside a card: what is missing and the
// action that opens the place where the record is created.
func (o *overviewState) empty(v *components.UI, text, actionLabel string, action func()) {
	ui.Column(v.C).FillWidth().Gap(tokens.Space2).AlignItems(ui.Start).Children(func() {
		ui.Text(v.C, text).
			FontSize(tokens.Typography.Secondary.Size).
			TextColor(mygo.Color(v.T.Palette.TextSecondary))
		if actionLabel != "" && action != nil {
			v.Button(actionLabel, components.ButtonOptions{
				Variant: components.VariantGhost,
				Size:    components.SizeSmall,
				OnClick: action,
			})
		}
	})
}

// overviewRow is one clickable entity row of a card. The whole row is a
// button, so Enter and Space open the entity as a pointer click does.
func overviewRow(v *components.UI, title string, meta func(), onClick func()) {
	p := v.T.Palette
	title = strings.TrimSpace(title)
	if title == "" {
		title = i18n.T("shell.overview.untitled")
	}
	row := ui.ButtonBase(v.C).
		FillWidth().
		MinHeight(tokens.Space8).
		Padding(tokens.Space1, tokens.Space2).
		Radius(tokens.RadiusMD).
		TextColor(mygo.Color(p.TextPrimary)).
		Gap(tokens.ComponentGap).
		AlignItems(ui.Center).
		Label(title)
	if row.Hovered() {
		row.Background(mygo.Color(p.BgHover))
	}
	row.OnClick(onClick)
	row.Children(func() {
		ui.Text(v.C, title).
			FontSize(tokens.Typography.Body.Size).
			Grow(1).
			SingleLine()
		if meta != nil {
			meta()
		}
	})
}

// overviewViewAll maps a card's "view all" action to the routes of §6.3. The
// current-focus card opens the project edit dialog, which owns the complete
// focus and goals text; an archived project has no write action, so the same
// entry opens the read-only Idea list.
func overviewViewAll(a *app.App, projectID string, snap *overview.Snapshot, w overview.WidgetID, archived bool) func() {
	switch w {
	case overview.WidgetCurrentFocus:
		if archived {
			return func() { a.Navigate(ports.Route{Kind: app.RouteIdeas, ProjectID: projectID}) }
		}
		return func() { requestDialog(a, editProjectParam, projectID) }
	case overview.WidgetActiveIdeas:
		return func() { a.Navigate(ports.Route{Kind: app.RouteIdeas, ProjectID: projectID}) }
	case overview.WidgetRecentExperiments:
		return func() { a.Navigate(ports.Route{Kind: app.RouteExperiments, ProjectID: projectID}) }
	case overview.WidgetMyTasks:
		return func() { a.Navigate(ports.Route{Kind: app.RouteKanban, ProjectID: projectID}) }
	case overview.WidgetRecentNotes:
		return func() { a.Navigate(ports.Route{Kind: app.RouteNotes, ProjectID: projectID}) }
	case overview.WidgetLiterature:
		return func() { a.Navigate(ports.Route{Kind: app.RouteLiterature, ProjectID: projectID}) }
	case overview.WidgetBlockedTasks:
		return func() {
			a.Navigate(ports.Route{
				Kind:      app.RouteKanban,
				ProjectID: projectID,
				Params:    map[string]string{"view": "blocked"},
			})
		}
	case overview.WidgetWeeklyProgress:
		if snap.WeeklyProgress.ReportID != "" {
			reportID := snap.WeeklyProgress.ReportID
			return func() {
				a.Navigate(ports.Route{Kind: ports.RouteWeeklyReport, ProjectID: projectID, EntityID: reportID})
			}
		}
		return func() { a.Navigate(ports.Route{Kind: app.RouteWeeklyReview, ProjectID: projectID}) }
	}
	return func() {}
}

// cardTitle names one widget of §3.2.
func cardTitle(w overview.WidgetID) string {
	switch w {
	case overview.WidgetCurrentFocus:
		return i18n.T("shell.overview.card_focus")
	case overview.WidgetActiveIdeas:
		return i18n.T("shell.overview.card_ideas")
	case overview.WidgetRecentExperiments:
		return i18n.T("shell.overview.card_experiments")
	case overview.WidgetMyTasks:
		return i18n.T("shell.overview.card_tasks")
	case overview.WidgetRecentNotes:
		return i18n.T("shell.overview.card_notes")
	case overview.WidgetLiterature:
		return i18n.T("shell.overview.card_literature")
	case overview.WidgetBlockedTasks:
		return i18n.T("shell.overview.card_blocked")
	case overview.WidgetWeeklyProgress:
		return i18n.T("shell.overview.card_weekly")
	}
	return string(w)
}

// pendingTooltip explains why a card carries the unconfirmed badge, per §6.3.
func pendingTooltip(w overview.WidgetID) string {
	switch w {
	case overview.WidgetRecentExperiments:
		return i18n.T("shell.overview.pending_tip_experiments")
	case overview.WidgetMyTasks:
		return i18n.T("shell.overview.pending_tip_tasks")
	case overview.WidgetBlockedTasks:
		return i18n.T("shell.overview.pending_tip_blocked")
	case overview.WidgetWeeklyProgress:
		return i18n.T("shell.overview.pending_tip_weekly")
	}
	return i18n.T("shell.overview.pending_tip_generic")
}

// widgetState returns the availability report of one widget. A snapshot always
// carries eight states; a missing slot is treated as unavailable rather than
// invented.
func widgetState(snap *overview.Snapshot, w overview.WidgetID) overview.WidgetState {
	for _, st := range snap.Widgets {
		if st.ID == w {
			return st
		}
	}
	return overview.WidgetState{ID: w}
}

// ideaStatusLabel localises the two active statuses of §3.2. Any other value
// is shown as stored, because the card must not claim a state it cannot translate.
func ideaStatusLabel(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "researching":
		return i18n.T("pages.ideas.status.researching")
	case "validating":
		return i18n.T("pages.ideas.status.validating")
	}
	return status
}

// ideaStatusTone reinforces the two active statuses without carrying meaning
// alone; the label always says which one it is.
func ideaStatusTone(status string) components.Tone {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "researching":
		return components.Accent
	case "validating":
		return components.Info
	}
	return components.Neutral
}

// runStatusLabel localises the last-Run summary. The service uses an em dash
// for an experiment without runs; that placeholder passes through unchanged.
func runStatusLabel(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running":
		return i18n.T("pages.experiments.run_running")
	case "finished":
		return i18n.T("pages.experiments.run_finished")
	case "failed":
		return i18n.T("pages.experiments.run_failed")
	case "unknown":
		return i18n.T("pages.experiments.run_unknown")
	}
	return status
}

// taskPriorityLabel maps the port's rank back to the Kanban label.
func taskPriorityLabel(rank int) string {
	switch rank {
	case 3:
		return i18n.T("pages.kanban.priority_high")
	case 2:
		return i18n.T("pages.kanban.priority_medium")
	case 1:
		return i18n.T("pages.kanban.priority_low")
	}
	return i18n.T("pages.kanban.priority_none")
}

// taskPriorityTone reinforces the priority; the badge text carries it.
func taskPriorityTone(rank int) components.Tone {
	switch rank {
	case 3:
		return components.Warning
	case 2:
		return components.Accent
	}
	return components.Neutral
}

// paperMeta joins the author line and the year, skipping whichever is absent.
func paperMeta(p overview.PaperCard) string {
	parts := make([]string, 0, 2)
	if authors := strings.TrimSpace(p.Authors); authors != "" {
		parts = append(parts, authors)
	}
	if p.Year > 0 {
		parts = append(parts, strconv.Itoa(p.Year))
	}
	return strings.Join(parts, " · ")
}

// activityRow is one activity count, sorted for a stable card.
type activityRow struct {
	label string
	count int
}

// activityRows orders the counts by descending count, then by entity type, so
// two loads of unchanged data render in the same order.
func activityRows(counts map[string]int) []activityRow {
	out := make([]activityRow, 0, len(counts))
	for label, count := range counts {
		out = append(out, activityRow{label: label, count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].count != out[j].count {
			return out[i].count > out[j].count
		}
		return out[i].label < out[j].label
	})
	return out
}

// activityLabel localises an activity entity type. Types outside the registry
// are shown as stored rather than hidden.
func activityLabel(entityType string) string {
	switch entityType {
	case "project":
		return i18n.T("shell.overview.activity.project")
	case "paper":
		return i18n.T("shell.overview.activity.paper")
	case "note":
		return i18n.T("shell.overview.activity.note")
	case "research_database":
		return i18n.T("shell.overview.activity.research_database")
	case "db_field":
		return i18n.T("shell.overview.activity.db_field")
	case "db_record":
		return i18n.T("shell.overview.activity.db_record")
	case "idea":
		return i18n.T("shell.overview.activity.idea")
	case "evidence":
		return i18n.T("shell.overview.activity.evidence")
	case "experiment":
		return i18n.T("shell.overview.activity.experiment")
	case "run":
		return i18n.T("shell.overview.activity.run")
	case "snapshot":
		return i18n.T("shell.overview.activity.snapshot")
	case "task":
		return i18n.T("shell.overview.activity.task")
	case "weekly_report":
		return i18n.T("shell.overview.activity.weekly_report")
	case "entity_relation":
		return i18n.T("shell.overview.activity.entity_relation")
	}
	return entityType
}

// overviewErrorText translates the one sentinel Load reports and falls back to
// the shared error text for everything else.
func overviewErrorText(err error) string {
	if errors.Is(err, overview.ErrNotFound) {
		return i18n.T("shell.overview.error_project_missing")
	}
	return errtext.Text(err)
}

// truncateRunes cuts s to at most n runes, marking the cut with one ellipsis
// character. It never splits a rune.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
