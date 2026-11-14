package pages

// Experiments is the SPEC-07 module of the shell: the experiment list, the
// experiment/run detail with its six tabs, the read-only scan preview and the
// append-only analysis history.
//
// Non-intrusion is the invariant of the whole module: the pages only read the
// bound directory. They never write a file, never execute anything and never
// offer a control that would. Every long operation runs in a managed task so
// the shell's task strip owns the progress and the cancel button.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"researchos/internal/experiments"
	"researchos/internal/i18n"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/shell"
	"researchos/internal/ui/tokens"
)

// ExperimentsAPI is the part of the SPEC-07 service layer the pages use. The
// composition root binds it to one workspace; every call carries the project
// id because the adapter owns no project state. Nil means the module is not
// assembled, and the page says so instead of faking data.
//
// Every mutation passes ports.ActorUser: the page is the user path, while MCP
// reaches the same service methods with ActorMCP.
type ExperimentsAPI interface {
	CreateExperiment(ctx context.Context, projectID string, in experiments.CreateExperimentInput) (*experiments.Experiment, error)
	UpdateExperiment(ctx context.Context, projectID string, in experiments.UpdateExperimentInput) (*experiments.Experiment, error)
	SoftDeleteExperiment(ctx context.Context, projectID, experimentID string, actor ports.Actor) error
	GetExperiment(ctx context.Context, projectID, experimentID string) (*experiments.ExperimentDetail, error)
	ListExperiments(ctx context.Context, projectID string, f experiments.ExperimentFilter) ([]experiments.ExperimentSummary, error)
	BindDirectory(ctx context.Context, projectID, experimentID, absPath string, rules experiments.ScanRules) (*experiments.DirReference, error)
	RelocateDirectory(ctx context.Context, projectID, experimentID, absPath string, actor ports.Actor) (*experiments.DirReference, error)
	VerifyDirectory(ctx context.Context, projectID, experimentID string) ([]experiments.DirReference, error)
	SetScanRules(ctx context.Context, projectID, experimentID string, rules experiments.ScanRules) error

	CreateRun(ctx context.Context, projectID string, in experiments.CreateRunInput) (*experiments.Run, error)
	SetRunStatus(ctx context.Context, projectID, runID string, status experiments.RunStatus) error
	GetRun(ctx context.Context, projectID, runID string) (*experiments.RunDetail, error)
	ListRuns(ctx context.Context, projectID, experimentID string, includeDeleted bool) ([]experiments.RunSummary, error)
	ListDirCandidates(ctx context.Context, projectID, experimentID string) ([]experiments.DirCandidate, error)

	Plan(ctx context.Context, projectID string, target experiments.TargetRef, override *experiments.ScanRules) (*experiments.ScanPlan, error)
	RunScan(ctx context.Context, projectID string, target experiments.TargetRef, override *experiments.ScanRules) (*experiments.ScanResult, error)
	CompareRuns(ctx context.Context, projectID, experimentID string) (*experiments.MetricComparison, error)
	PreviewContext(ctx context.Context, projectID string, target experiments.TargetRef, opts experiments.AnalysisOptions) (*experiments.ContextBundle, error)
	GenerateOutline(ctx context.Context, projectID, experimentID string, opts experiments.AnalysisOptions) (*experiments.Snapshot, error)
	GenerateResults(ctx context.Context, projectID string, target experiments.TargetRef, opts experiments.AnalysisOptions) (*experiments.Snapshot, error)
	UpdateContent(ctx context.Context, projectID, snapshotID, contentMD string, actor ports.Actor) error
	Confirm(ctx context.Context, projectID, snapshotID string, actor ports.Actor) error
	RetrySaveSnapshot(ctx context.Context, projectID, aiOutputID string) (*experiments.Snapshot, error)
	ListSnapshots(ctx context.Context, projectID string, target experiments.TargetRef, f experiments.SnapshotFilter) ([]experiments.SnapshotSummary, error)
	GetSnapshot(ctx context.Context, projectID, snapshotID string) (*experiments.Snapshot, error)
	Inspect(ctx context.Context, projectID, dir string) (*experiments.GitInfo, error)
	Diff(ctx context.Context, projectID, dir string, opts experiments.DiffOptions) (*experiments.GitDiff, error)
	LinkIdea(ctx context.Context, projectID, experimentID, ideaID string, actor ports.Actor) error
	UnlinkIdea(ctx context.Context, projectID, experimentID, ideaID string, actor ports.Actor) error
	ListIdeas(ctx context.Context, projectID, experimentID string) ([]experiments.IdeaRef, error)

	// AIAvailable reports whether a chat provider is assigned for this
	// workspace. A false value disables the generation footer with the 去设置
	// hint instead of failing per call (SPEC-07 AC-15).
	AIAvailable() bool
}

const (
	// expListLimit is the page size of the list: the service pages with
	// limit/offset and the page filters the returned window locally.
	expListLimit = 500
	// expTreeLimit is how many file entries the scan preview and the Files
	// tree render before they ask the user to narrow the filters (SPEC-07
	// §6.10: past 2 000 entries the Tree is paginated).
	expTreeLimit = 2000
	// expVisibleRows is how many rows a table shows before it scrolls.
	expVisibleRows = 12
)

// ExperimentsPages returns the SPEC-07 pages keyed by route: the list, the
// experiment detail and the run view. The detail page is one instance for both
// entity routes so navigation between an experiment and its runs keeps the
// loaded state and the tab choice.
func ExperimentsPages(s *Services) map[ports.RouteKind]shell.Page {
	shared := &experimentsShared{}
	list := &ExperimentsList{Services: s, API: s.Experiments, shared: shared}
	detail := &ExperimentDetail{Services: s, API: s.Experiments, shared: shared}
	return map[ports.RouteKind]shell.Page{
		app.RouteExperiments:  list.Page,
		ports.RouteExperiment: detail.Page,
		ports.RouteRun:        detail.Page,
	}
}

// experimentsShared is the session state the list and the detail share: the
// detail bumps the generation after a write, so a returning list reloads
// instead of showing the row it just changed.
type experimentsShared struct {
	mu  sync.Mutex
	gen int
}

func (s *experimentsShared) bump() {
	s.mu.Lock()
	s.gen++
	s.mu.Unlock()
}

func (s *experimentsShared) version() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

// ExperimentsList renders the experiment list of the open project.
type ExperimentsList struct {
	Services *Services
	API      ExperimentsAPI
	shared   *experimentsShared

	mu          sync.Mutex
	loading     bool
	loaded      bool
	project     string
	version     int
	list        []experiments.ExperimentSummary
	problems    []string
	creating    bool
	createErr   string
	createDone  bool
	deleting    bool
	deleteErr   string
	deleteDone  bool
	createOpen  bool
	createTitle string
	createPurp  string
	createDir   string
	deleteID    string
	deleteTitle string

	// Frame-only state: the toolbar choices persist for the session.
	query      string
	brokenOnly bool
	unseenOnly bool
	table      *ui.ListState
}

// Page renders one frame of the experiment list.
func (l *ExperimentsList) Page(v *components.UI, r ports.Route) {
	a := l.Services.App
	if a == nil {
		return
	}
	if l.table == nil {
		l.table = components.NewTableState()
	}
	if l.API == nil {
		l.unavailable(v)
		return
	}
	project := a.Project()
	if project == nil {
		l.needsProject(v, a)
		return
	}
	l.load(a, project.ID)
	l.tickDialogs()
	list := l.visible()
	readOnly := project.ArchivedAt != nil
	// The empty state offers the create action when the page lists nothing
	// and no filter narrows it; the header keeps only the reload button.
	empty := len(list) == 0 && strings.TrimSpace(l.query) == "" && !l.brokenOnly && !l.unseenOnly

	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.experiments.title"),
			Description: i18n.Tf("pages.experiments.description", map[string]string{"project": project.Name}),
			Actions: func() {
				if v.Button(i18n.T("pages.experiments.btn_refresh"), components.ButtonOptions{
					Variant: components.VariantGhost,
					Icon:    icons.Refresh,
				}).Clicked() {
					l.reload(a, project.ID)
				}
				if empty {
					return
				}
				if v.Button(i18n.T("pages.experiments.btn_create"), components.ButtonOptions{
					Variant:  components.VariantPrimary,
					Icon:     icons.Plus,
					Disabled: readOnly,
					Tooltip:  expReadOnlyTooltip(readOnly),
				}).Clicked() {
					l.createOpen = true
				}
			},
		})
		if readOnly {
			v.Banner(components.BannerOptions{
				Tone:    components.Warning,
				Title:   i18n.T("pages.experiments.readonly_title"),
				Message: i18n.T("pages.experiments.readonly_message"),
			})
		}
		l.banners(v)
		l.toolbar(v)
		if l.blank() {
			v.Skeleton(components.SkeletonOptions{Lines: 6})
		} else if len(list) == 0 {
			empty := components.EmptyStateOptions{
				Icon:   icons.Experiments,
				Title:  i18n.T("pages.experiments.empty_title"),
				Reason: i18n.T("pages.experiments.empty_reason"),
			}
			if !readOnly {
				empty.ActionLabel = i18n.T("pages.experiments.btn_create")
				empty.OnAction = func() { l.createOpen = true }
			}
			v.EmptyState(empty)
		} else {
			l.tableView(v, a, project.ID, list, readOnly)
		}
		l.createDialog(v, a, project.ID, readOnly)
		l.deleteDialog(v, a, project.ID, readOnly)
	}})
}

// unavailable is the honest state of a workspace whose experiments module was
// never assembled.
func (l *ExperimentsList) unavailable(v *components.UI) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{Title: i18n.T("pages.experiments.title")})
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.Experiments,
			Title:  i18n.T("pages.experiments.disabled_title"),
			Reason: i18n.T("pages.experiments.disabled_reason"),
		})
	}})
}

// needsProject states the precondition of the module.
func (l *ExperimentsList) needsProject(v *components.UI, a *app.App) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.experiments.title"),
			Description: i18n.T("pages.experiments.needs_project_desc"),
		})
		v.EmptyState(components.EmptyStateOptions{
			Icon:        icons.Experiments,
			Title:       i18n.T("pages.experiments.needs_project_title"),
			Reason:      i18n.T("pages.experiments.needs_project_reason"),
			ActionLabel: i18n.T("pages.experiments.btn_projects"),
			OnAction:    func() { a.Navigate(ports.Route{Kind: app.RouteProjects}) },
		})
	}})
}

// banners reports the failures of the last load and of a dialog action.
func (l *ExperimentsList) banners(v *components.UI) {
	l.mu.Lock()
	problems := append([]string(nil), l.problems...)
	createErr := l.createErr
	deleteErr := l.deleteErr
	l.mu.Unlock()
	for _, problem := range problems {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.experiments.banner_problem"),
			Message: problem,
		})
	}
	if createErr != "" {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.experiments.banner_create_failed"),
			Message: createErr,
		})
	}
	if deleteErr != "" {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.experiments.banner_delete_failed"),
			Message: deleteErr,
		})
	}
}

// toolbar is the search field and the two filters of §6.1.
func (l *ExperimentsList) toolbar(v *components.UI) {
	ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).AlignItems(ui.Center).Wrap().Children(func() {
		ui.Row(v.C).Grow(1).MinWidth(tokens.Space12 * 10).Children(func() {
			v.SearchInput(&l.query, components.SearchOptions{
				Label:       i18n.T("pages.experiments.field_search"),
				Placeholder: i18n.T("pages.experiments.field_search_hint"),
				Compact:     true,
			}).Width(components.SearchFieldWidth)
		})
		v.Checkbox(&l.brokenOnly, i18n.T("pages.experiments.filter_broken"), components.ToggleOptions{})
		v.Checkbox(&l.unseenOnly, i18n.T("pages.experiments.filter_unanalyzed"), components.ToggleOptions{})
	})
}

// tableView renders the list as the sortable table of §6.1.
func (l *ExperimentsList) tableView(v *components.UI, a *app.App, projectID string, list []experiments.ExperimentSummary, readOnly bool) {
	sorted := expSortSummaries(list, l.table.Sort)
	open := func(row experiments.ExperimentSummary, view string) {
		route := ports.Route{Kind: ports.RouteExperiment, ProjectID: projectID, EntityID: row.ID}
		if view != "" {
			route.Params = map[string]string{"view": view}
		}
		a.Navigate(route)
	}
	table := v.Table(components.TableOptions{
		Columns: []components.Column{
			{Title: i18n.T("pages.experiments.col_title"), ID: "title", Sortable: true},
			{Title: i18n.T("pages.experiments.col_dir"), ID: "dir", Width: 168, Sortable: true},
			{Title: i18n.T("pages.experiments.col_last_run"), ID: "run", Width: 120, Sortable: true},
			{Title: i18n.T("pages.experiments.col_analyzed"), ID: "analyzed", Width: 140, Sortable: true},
			{Title: i18n.T("pages.experiments.col_ideas"), ID: "ideas", Width: 96, Align: ui.End, Sortable: true},
			{Title: i18n.T("pages.experiments.col_updated"), ID: "updated", Width: 140, Sortable: true},
			{Title: i18n.T("pages.experiments.col_actions"), Width: 96, Align: ui.End},
		},
		Rows:  len(sorted),
		State: l.table,
		Key:   func(i int) any { return sorted[i].ID },
		Cell: func(row, col int) {
			item := sorted[row]
			switch col {
			case 0:
				if ui.Text(v.C, item.Title).SingleLine().FillWidth().RowClicked() {
					open(item, "")
				}
			case 1:
				tone, label := expDirBadge(item.DirStatus)
				cell := ui.Column(v.C).OnClick(func() { open(item, "") })
				cell.Children(func() {
					v.Badge(label, tone)
					if item.DirStatus == experiments.DirBroken && item.DirReason != "" {
						ui.Text(v.C, item.DirReason).
							FontSize(tokens.Typography.Caption.Size).
							TextColor(mygo.Color(v.T.Palette.TextMuted)).
							SingleLine()
					}
				})
			case 2:
				if item.LastRunStatus == "" {
					ui.Text(v.C, i18n.T("pages.experiments.value_none")).TextColor(mygo.Color(v.T.Palette.TextMuted))
				} else {
					cell := ui.Row(v.C).Gap(tokens.Space2).AlignItems(ui.Center).OnClick(func() { open(item, "") })
					cell.Children(func() {
						v.Badge(expRunStatusLabel(item.LastRunStatus), expRunTone(item.LastRunStatus))
					})
				}
			case 3:
				if item.LastAnalyzedAt == nil {
					ui.Text(v.C, i18n.T("pages.experiments.value_never")).TextColor(mygo.Color(v.T.Palette.TextMuted))
				} else if ui.Text(v.C, expTime(*item.LastAnalyzedAt)).SingleLine().RowClicked() {
					open(item, "")
				}
			case 4:
				if ui.Text(v.C, fmt.Sprintf("%d", item.LinkedIdeas)).RowClicked() {
					open(item, "")
				}
			case 5:
				if ui.Text(v.C, expTime(item.UpdatedAt)).SingleLine().RowClicked() {
					open(item, "")
				}
			case 6:
				ui.MenuButton(v.C, i18n.T("pages.experiments.row_actions"), func(m *ui.Menu) {
					if m.Item(i18n.T("pages.experiments.action_open")).Chosen() {
						open(item, "")
					}
					if m.Item(i18n.T("pages.experiments.action_scan")).Chosen() {
						open(item, "scan")
					}
					if m.Item(i18n.T("pages.experiments.action_analyze")).Chosen() {
						open(item, "outline")
					}
					if !readOnly {
						m.Separator()
						if m.Item(i18n.T("pages.experiments.action_delete")).Danger().Chosen() {
							l.askDelete(item)
						}
					}
				})
			}
		},
	})
	table.Height(expTableHeight(len(sorted)))
}

// askDelete opens the confirmation dialog for one experiment.
func (l *ExperimentsList) askDelete(item experiments.ExperimentSummary) {
	l.deleteID = item.ID
	l.deleteTitle = item.Title
	l.mu.Lock()
	l.deleteErr = ""
	l.mu.Unlock()
}

// createDialog collects the title, the purpose and an optional directory.
func (l *ExperimentsList) createDialog(v *components.UI, a *app.App, projectID string, readOnly bool) {
	if !l.createOpen || readOnly {
		return
	}
	l.mu.Lock()
	creating, errText := l.creating, l.createErr
	l.mu.Unlock()
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title:       i18n.T("pages.experiments.create_title"),
		Description: i18n.T("pages.experiments.create_desc"),
		Width:       tokens.Space12 * 14,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				l.createOpen = false
			}
			if v.Button(i18n.T("pages.experiments.create_confirm"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Loading:  creating,
				Disabled: creating,
			}).Clicked() {
				l.create(a, projectID)
			}
		},
	}, func() {
		field := v.Input(&l.createTitle, components.FieldOptions{
			Label:       i18n.T("pages.experiments.field_title"),
			Placeholder: i18n.T("pages.experiments.field_title_hint"),
			AutoFocus:   true,
		})
		if field.Submitted() {
			l.create(a, projectID)
		}
		v.Textarea(&l.createPurp, components.FieldOptions{
			Label:       i18n.T("pages.experiments.field_purpose"),
			Placeholder: i18n.T("pages.experiments.field_purpose_hint"),
		})
		l.directoryField(v, "create")
		ui.Text(v.C, i18n.T("pages.experiments.create_defaults")).
			FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(v.T.Palette.TextMuted))
		if errText != "" {
			ui.Text(v.C, errText).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
	if !open && !creating {
		l.createOpen = false
	}
}

// directoryField is the shared folder picker: the OS dialog plus a drop zone.
// The path is displayed in full so a wrong pick is visible before the bind.
func (l *ExperimentsList) directoryField(v *components.UI, target string) {
	path := &l.createDir
	if target != "create" {
		return
	}
	ui.Row(v.C).Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
		ui.Row(v.C).Grow(1).Children(func() {
			v.Input(path, components.FieldOptions{
				Label:       i18n.T("pages.experiments.field_dir"),
				Placeholder: i18n.T("pages.experiments.field_dir_hint"),
				ReadOnly:    true,
				Description: i18n.T("pages.experiments.field_dir_desc"),
			})
		})
		if v.Button(i18n.T("pages.experiments.btn_choose_folder"), components.ButtonOptions{
			Icon:  icons.FolderOpen,
			Align: ui.Center,
		}).Clicked() {
			l.pickFolder(path)
		}
	})
	zone := ui.Column(v.C).FillWidth().Padding(tokens.Space3).Radius(tokens.RadiusLG).
		Border(1, mygo.Color(v.Palette().BorderDefault))
	if zone.FileDragOver() {
		zone.Border(2, mygo.Color(v.Palette().AccentDefault))
	}
	zone.Children(func() {
		ui.Text(v.C, i18n.T("pages.experiments.drop_dir")).
			FontSize(tokens.Typography.Secondary.Size).
			TextColor(mygo.Color(v.Palette().TextMuted))
	})
	if files := zone.DroppedFiles(); len(files) > 0 {
		if dir := expFolderFromDrop(files); dir != "" {
			*path = dir
		}
	}
}

// pickFolder runs the OS directory picker. The dialog is stateful: a failure
// leaves the previous path untouched.
func (l *ExperimentsList) pickFolder(dst *string) {
	if l.Services.ChooseFolder == nil {
		return
	}
	dir, err := l.Services.ChooseFolder()
	if err != nil || strings.TrimSpace(dir) == "" {
		return
	}
	*dst = dir
}

// create runs the create call. A directory is optional; when given the service
// validates it before binding (workspace paths and symlink escapes are
// refused with a reason).
func (l *ExperimentsList) create(a *app.App, projectID string) {
	title := strings.TrimSpace(l.createTitle)
	if title == "" {
		a.Toast(app.LevelWarning, i18n.T("pages.experiments.title_required"), "")
		return
	}
	l.mu.Lock()
	if l.creating {
		l.mu.Unlock()
		return
	}
	l.creating = true
	l.createErr = ""
	l.mu.Unlock()

	api := l.API
	purpose := l.createPurp
	dir := strings.TrimSpace(l.createDir)
	a.RunTask(i18n.T("pages.experiments.task_create"), func(ctx context.Context, t *app.Task) error {
		created, err := api.CreateExperiment(ctx, projectID, experiments.CreateExperimentInput{
			Title:   title,
			Purpose: purpose,
			DirPath: dir,
			Actor:   ports.ActorUser,
		})
		l.mu.Lock()
		l.creating = false
		if err != nil {
			l.createErr = expErrText(err)
		}
		l.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.experiments.toast_create_failed"), expErrText(err))
			a.Notify()
			return nil
		}
		l.mu.Lock()
		l.createDone = true
		l.mu.Unlock()
		l.shared.bump()
		a.Toast(app.LevelSuccess, i18n.T("pages.experiments.toast_created"), created.Title)
		a.Navigate(ports.Route{Kind: ports.RouteExperiment, ProjectID: projectID, EntityID: created.ID})
		return nil
	})
}

// tickDialogs closes a dialog whose service call finished, so a task never
// writes a frame-only field.
func (l *ExperimentsList) tickDialogs() {
	l.mu.Lock()
	created, deleted := l.createDone, l.deleteDone
	if created {
		l.createDone = false
		l.createErr = ""
	}
	if deleted {
		l.deleteDone = false
		l.deleteErr = ""
	}
	l.mu.Unlock()
	if created {
		l.createOpen = false
		l.createTitle = ""
		l.createPurp = ""
		l.createDir = ""
	}
	if deleted {
		l.deleteID = ""
		l.deleteTitle = ""
	}
}

// deleteDialog confirms the soft deletion of one experiment.
func (l *ExperimentsList) deleteDialog(v *components.UI, a *app.App, projectID string, readOnly bool) {
	if l.deleteID == "" || readOnly {
		return
	}
	l.mu.Lock()
	deleting, errText := l.deleting, l.deleteErr
	l.mu.Unlock()
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title: i18n.T("pages.experiments.delete_title"),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				l.deleteID = ""
			}
			if v.Button(i18n.T("pages.experiments.delete_confirm"), components.ButtonOptions{
				Variant:  components.VariantDanger,
				Loading:  deleting,
				Disabled: deleting,
			}).Clicked() {
				l.delete(a, projectID)
			}
		},
	}, func() {
		ui.Text(v.C, i18n.Tf("pages.experiments.delete_message", map[string]string{"title": l.deleteTitle})).
			FontSize(tokens.Typography.Body.Size)
		if errText != "" {
			ui.Text(v.C, errText).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
	if !open && !deleting {
		l.deleteID = ""
	}
}

// delete soft-deletes the experiment. Files and snapshots stay: the recycle
// bin restores the row (SPEC-07 AC-01).
func (l *ExperimentsList) delete(a *app.App, projectID string) {
	id := l.deleteID
	if id == "" {
		return
	}
	l.mu.Lock()
	if l.deleting {
		l.mu.Unlock()
		return
	}
	l.deleting = true
	l.deleteErr = ""
	l.mu.Unlock()

	api := l.API
	a.RunTask(i18n.T("pages.experiments.task_delete"), func(ctx context.Context, t *app.Task) error {
		err := api.SoftDeleteExperiment(ctx, projectID, id, ports.ActorUser)
		l.mu.Lock()
		l.deleting = false
		if err != nil {
			l.deleteErr = expErrText(err)
		} else {
			kept := l.list[:0]
			for _, item := range l.list {
				if item.ID != id {
					kept = append(kept, item)
				}
			}
			l.list = kept
			l.deleteDone = true
		}
		l.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.experiments.toast_delete_failed"), expErrText(err))
			a.Notify()
			return nil
		}
		a.Toast(app.LevelSuccess, i18n.T("pages.experiments.toast_deleted"), "")
		a.Notify()
		return nil
	})
}

// load starts a reload when the cache is stale or the shared generation moved.
func (l *ExperimentsList) load(a *app.App, projectID string) {
	l.mu.Lock()
	need := !l.loading && (!l.loaded || l.project != projectID || l.version != l.shared.version())
	l.mu.Unlock()
	if need {
		l.reload(a, projectID)
	}
}

// reload reads the list in a managed task.
func (l *ExperimentsList) reload(a *app.App, projectID string) {
	api := l.API
	l.mu.Lock()
	if l.loading {
		l.mu.Unlock()
		return
	}
	l.loading = true
	if l.project != projectID {
		l.project = projectID
		l.list = nil
		l.loaded = false
		l.version = 0
	}
	l.mu.Unlock()
	gen := l.shared.version()

	a.RunTask(i18n.T("pages.experiments.task_load"), func(ctx context.Context, t *app.Task) error {
		list, err := api.ListExperiments(ctx, projectID, experiments.ExperimentFilter{Limit: expListLimit})
		l.mu.Lock()
		l.loading = false
		if err != nil {
			l.problems = append(l.problems[:0], expErrText(err))
		} else {
			l.list = list
			l.problems = nil
			l.loaded = true
			l.version = gen
		}
		l.mu.Unlock()
		a.Notify()
		return nil
	})
}

// blank reports whether the first load is still running.
func (l *ExperimentsList) blank() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.loading && !l.loaded
}

// visible applies the search and the two filters to the cached list.
func (l *ExperimentsList) visible() []experiments.ExperimentSummary {
	l.mu.Lock()
	list := append([]experiments.ExperimentSummary(nil), l.list...)
	l.mu.Unlock()
	query := strings.ToLower(strings.TrimSpace(l.query))
	out := make([]experiments.ExperimentSummary, 0, len(list))
	for _, item := range list {
		if l.brokenOnly && item.DirStatus != experiments.DirBroken {
			continue
		}
		if l.unseenOnly && item.LastAnalyzedAt != nil {
			continue
		}
		if query != "" &&
			!strings.Contains(strings.ToLower(item.Title), query) &&
			!strings.Contains(strings.ToLower(item.Purpose), query) {
			continue
		}
		out = append(out, item)
	}
	return out
}

// expSortSummaries sorts the list by the table's chosen column.
func expSortSummaries(list []experiments.ExperimentSummary, order *ui.SortOrder) []experiments.ExperimentSummary {
	out := append([]experiments.ExperimentSummary(nil), list...)
	if order == nil || order.Column == "" {
		return out
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		var less, equal bool
		switch order.Column {
		case "dir":
			less, equal = a.DirStatus < b.DirStatus, a.DirStatus == b.DirStatus
		case "run":
			less, equal = a.LastRunStatus < b.LastRunStatus, a.LastRunStatus == b.LastRunStatus
		case "analyzed":
			switch {
			case a.LastAnalyzedAt == nil && b.LastAnalyzedAt == nil:
				equal = true
			case a.LastAnalyzedAt == nil:
				less = true
			case b.LastAnalyzedAt == nil:
				less = false
			default:
				less = a.LastAnalyzedAt.Before(*b.LastAnalyzedAt)
				equal = a.LastAnalyzedAt.Equal(*b.LastAnalyzedAt)
			}
		case "ideas":
			less, equal = a.LinkedIdeas < b.LinkedIdeas, a.LinkedIdeas == b.LinkedIdeas
		case "updated":
			less, equal = a.UpdatedAt.Before(b.UpdatedAt), a.UpdatedAt.Equal(b.UpdatedAt)
		default:
			left, right := strings.ToLower(a.Title), strings.ToLower(b.Title)
			less, equal = left < right, left == right
		}
		if order.Descending {
			return !less && !equal
		}
		return less
	})
	return out
}

// expTableHeight keeps a page of rows visible and scrolls the rest.
func expTableHeight(rows int) float32 {
	if rows > expVisibleRows {
		rows = expVisibleRows
	}
	return float32(rows+1) * 44
}

// expErrText localises the SPEC-07 sentinels. The module returns plain errors,
// so the mapping is an errors.Is table rather than a code switch.
func expErrText(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, experiments.ErrReadOnlyProject):
		return i18n.T("pages.experiments.err_read_only")
	case errors.Is(err, experiments.ErrDirUnavailable):
		return i18n.T("pages.experiments.err_dir_unavailable")
	case errors.Is(err, experiments.ErrDirGuard):
		return i18n.T("pages.experiments.err_dir_guard")
	case errors.Is(err, experiments.ErrScanLimit):
		return i18n.T("pages.experiments.err_scan_limit")
	case errors.Is(err, experiments.ErrNoContent):
		return i18n.T("pages.experiments.err_no_content")
	case errors.Is(err, experiments.ErrAIUnavailable):
		return i18n.T("pages.experiments.err_ai_unavailable")
	case errors.Is(err, experiments.ErrInvalidAIOutput):
		return i18n.T("pages.experiments.err_invalid_ai_output")
	case errors.Is(err, experiments.ErrSnapshotPersist):
		return i18n.T("pages.experiments.err_snapshot_persist")
	case errors.Is(err, experiments.ErrInvalidInput):
		return i18n.T("pages.experiments.err_invalid_input")
	case errors.Is(err, experiments.ErrNotFound):
		return i18n.T("pages.experiments.err_not_found")
	}
	return aiErrorText(err)
}

// expReadOnlyTooltip names why a write control is disabled.
func expReadOnlyTooltip(readOnly bool) string {
	if !readOnly {
		return ""
	}
	return i18n.T("pages.experiments.readonly_tooltip")
}

// expDirBadge maps the directory status to its badge.
func expDirBadge(status experiments.DirStatus) (components.Tone, string) {
	switch status {
	case experiments.DirOK:
		return components.Success, i18n.T("pages.experiments.dir_ok")
	case experiments.DirBroken:
		return components.Danger, i18n.T("pages.experiments.dir_broken")
	}
	return components.Neutral, i18n.T("pages.experiments.dir_unbound")
}

// expRunTone maps a run status to its badge tone.
func expRunTone(status experiments.RunStatus) components.Tone {
	switch status {
	case experiments.RunFinished:
		return components.Success
	case experiments.RunRunning:
		return components.Accent
	case experiments.RunFailed:
		return components.Danger
	}
	return components.Neutral
}

// expRunStatusLabel localises a run status. The value is manual only: no
// completion detection exists in v1 (SPEC-07 O-02).
func expRunStatusLabel(status experiments.RunStatus) string {
	switch status {
	case experiments.RunRunning:
		return i18n.T("pages.experiments.run_running")
	case experiments.RunFinished:
		return i18n.T("pages.experiments.run_finished")
	case experiments.RunFailed:
		return i18n.T("pages.experiments.run_failed")
	}
	return i18n.T("pages.experiments.run_unknown")
}

// expKindLabel localises a file kind.
func expKindLabel(kind experiments.FileKind) string {
	switch kind {
	case experiments.KindCode:
		return i18n.T("pages.experiments.kind_code")
	case experiments.KindReadme:
		return i18n.T("pages.experiments.kind_readme")
	case experiments.KindConfig:
		return i18n.T("pages.experiments.kind_config")
	case experiments.KindData:
		return i18n.T("pages.experiments.kind_data")
	case experiments.KindMetrics:
		return i18n.T("pages.experiments.kind_metrics")
	case experiments.KindLog:
		return i18n.T("pages.experiments.kind_log")
	case experiments.KindImage:
		return i18n.T("pages.experiments.kind_image")
	case experiments.KindNotebook:
		return i18n.T("pages.experiments.kind_notebook")
	case experiments.KindBinary:
		return i18n.T("pages.experiments.kind_binary")
	}
	return i18n.T("pages.experiments.kind_other")
}

// expReasonLabel localises an exclusion reason. The preview states every
// reason; nothing is a hidden switch (SPEC-07 §6.3).
func expReasonLabel(reason experiments.ExclusionReason) string {
	switch reason {
	case experiments.ReasonTooLarge:
		return i18n.T("pages.experiments.reason_too_large")
	case experiments.ReasonModelWeight:
		return i18n.T("pages.experiments.reason_model_weight")
	case experiments.ReasonCache:
		return i18n.T("pages.experiments.reason_cache")
	case experiments.ReasonBinary:
		return i18n.T("pages.experiments.reason_binary")
	case experiments.ReasonSecret:
		return i18n.T("pages.experiments.reason_secret")
	case experiments.ReasonUserExclude:
		return i18n.T("pages.experiments.reason_user_exclude")
	case experiments.ReasonArchiveOrMedia:
		return i18n.T("pages.experiments.reason_archive")
	case experiments.ReasonSymlinkOutside:
		return i18n.T("pages.experiments.reason_symlink")
	case experiments.ReasonUnreadable:
		return i18n.T("pages.experiments.reason_unreadable")
	}
	return i18n.T("pages.experiments.reason_other")
}

// expAnalysisLabel localises the analysis kind.
func expAnalysisLabel(kind experiments.AnalysisKind) string {
	if kind == experiments.AnalysisOutline {
		return i18n.T("pages.experiments.kind_outline")
	}
	return i18n.T("pages.experiments.kind_results")
}

// expTargetLabel names a snapshot target.
func expTargetLabel(ref experiments.TargetRef) string {
	if ref.Type == experiments.TargetRun {
		return i18n.T("pages.experiments.target_run")
	}
	return i18n.T("pages.experiments.target_experiment")
}

// expAIStatus maps the SPEC-02 output status to a badge. The status comes from
// the history summary and is never written back.
func expAIStatus(status string) (components.Tone, string) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "confirmed":
		return components.Success, i18n.T("pages.experiments.ai_confirmed")
	case "draft":
		return components.Accent, i18n.T("pages.experiments.ai_draft")
	}
	return components.Neutral, i18n.T("pages.experiments.ai_unknown")
}

// expTime formats a timestamp for a table cell.
func expTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04")
}

// expShortHash shortens a commit or a file hash for a cell.
func expShortHash(hash string) string {
	hash = strings.TrimSpace(hash)
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

// expParamSummary renders a run's parameters in one line.
func expParamSummary(params map[string]experiments.ParamValue) string {
	if len(params) == 0 {
		return i18n.T("pages.experiments.params_none")
	}
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, 3)
	for _, key := range keys {
		parts = append(parts, key+"="+params[key].Value)
		if len(parts) == 3 {
			break
		}
	}
	text := strings.Join(parts, "  ")
	if len(keys) > 3 {
		text += " …"
	}
	return text
}

// expFloat formats a metric value without trailing noise.
func expFloat(value float64) string {
	if value == float64(int64(value)) {
		return fmt.Sprintf("%d", int64(value))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", value), "0"), ".")
}

// expMetricValue formats one metric cell.
func expMetricValue(metric experiments.Metric) string {
	if metric.Value != nil {
		return expFloat(*metric.Value)
	}
	if strings.TrimSpace(metric.Raw) != "" {
		return metric.Raw
	}
	return i18n.T("pages.experiments.value_none")
}

// expMetricSource renders a metric's origin.
func expMetricSource(source experiments.MetricSource) string {
	text := source.Path
	if source.Pointer != nil && *source.Pointer != "" {
		text += " · " + *source.Pointer
	}
	if source.Line != nil {
		text += fmt.Sprintf(":%d", *source.Line)
	}
	return text
}

// expFolderFromDrop turns a native drop into the directory it names: the path
// itself when it is a folder, else the folder that holds it.
func expFolderFromDrop(paths []string) string {
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err == nil && info.IsDir() {
			return path
		}
		if err != nil {
			continue
		}
		return expParentDir(path)
	}
	return ""
}

// expParentDir returns the parent directory of a slash path; a top-level name
// has no parent, which the tree builder needs to stop at its root.
func expParentDir(file string) string {
	file = strings.ReplaceAll(file, "\\", "/")
	file = strings.TrimSuffix(file, "/")
	if idx := strings.LastIndex(file, "/"); idx > 0 {
		return file[:idx]
	}
	return ""
}

// expRevealFolder shows a directory in the system file manager. It is the body
// of shell.revealFolder, copied here because that helper is package-private and
// the Files panel's only external action is this read-only reveal
// (SPEC-07 §6.8).
func expRevealFolder(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("pages: %s is not a folder", dir)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", dir)
	case "darwin":
		cmd = exec.Command("open", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	return cmd.Start()
}

// expReveal reveals a directory off the UI thread; a failure is a toast.
func expReveal(a *app.App, dir string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	go func() {
		if err := expRevealFolder(dir); err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.experiments.toast_reveal_failed"), err.Error())
		}
	}()
}
