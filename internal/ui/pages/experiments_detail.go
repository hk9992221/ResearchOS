package pages

// ExperimentDetail is the SPEC-07 experiment and run view (§6.2–§6.9). The
// same page serves ports.RouteExperiment and ports.RouteRun: a run route
// resolves its experiment first, then opens the view with that run as the
// analysis target.

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown"

	"researchos/internal/experiments"
	"researchos/internal/i18n"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// Tab indexes of §6.2.
const (
	expTabOverview = iota
	expTabOutline
	expTabResults
	expTabFiles
	expTabHistory
	expTabGit
)

// ExperimentDetail renders one experiment, or one run of it.
type ExperimentDetail struct {
	Services *Services
	API      ExperimentsAPI
	shared   *experimentsShared

	mu          sync.Mutex
	loading     bool
	loadedKey   string
	project     string
	version     int
	exp         *experiments.ExperimentDetail
	run         *experiments.RunDetail
	dirRefs     []experiments.DirReference
	git         *experiments.GitInfo
	snapshots   []experiments.SnapshotSummary
	outline     *experiments.Snapshot
	outlineMeta *experiments.SnapshotSummary
	results     *experiments.Snapshot
	resultsMeta *experiments.SnapshotSummary
	plan        *experiments.ScanPlan
	planAt      timeMark
	bundle      *experiments.ContextBundle
	comparison  *experiments.MetricComparison
	metrics     *experiments.MetricSet
	historySnap *experiments.Snapshot
	diff        *experiments.GitDiff
	diffAt      string

	problems   []string
	genErr     string
	genStage   string
	retryOut   string
	busy       map[string]bool
	saveErr    string
	confirmErr string
	scanErr    string
	scanInfo   string
	diffErr    string
	fileErr    string
	bundleErr  string

	// Frame-only state.
	tab        int
	scanView   bool
	routeKey   string
	sideSize   float32
	planMode   int
	planKind   string
	planQuery  string
	dirOpen    map[string]*bool
	selected   string
	editor     *editor.State
	editorFor  string
	editorLine int
	md         *markdown.Options
	diffView   *editor.DiffState
	diffFor    string
	diffFrom   string
	diffTo     string
	contextPre bool

	editing        bool
	editText       string
	editFor        string
	metricTable    *ui.ListState
	compareTable   *ui.ListState
	historyTable   *ui.ListState
	confirmingSnap string

	createRunOpen bool
	runParams     []expParamRow
	runDir        string
	runErr        string
	candidates    []experiments.DirCandidate
	candOpen      bool
	candErr       string

	linkOpen bool
	linkID   string
	linkErr  string

	editOpen     bool
	editTitle    string
	editPurpose  string
	editErr      string
	dirOpenDlg   bool
	dirPick      string
	dirErr       string
	rulesOpen    bool
	rulesInclude string
	rulesExclude string
	rulesMaxFile string
	rulesMaxTot  string
	rulesMaxN    string
	rulesHash    string
	rulesErr     string
}

// timeMark is the session timestamp of the last completed scan. It is kept
// without the clock type so the page never imports the foundation clock.
type timeMark struct {
	set bool
	at  string
}

// expParamRow is one editable key/value row of the run params table.
type expParamRow struct {
	Key   string
	Value string
}

// Page renders one frame of the experiment or run view.
func (d *ExperimentDetail) Page(v *components.UI, r ports.Route) {
	a := d.Services.App
	if a == nil {
		return
	}
	if d.md == nil {
		d.md = &markdown.Options{}
	}
	if d.busy == nil {
		d.busy = map[string]bool{}
	}
	if d.dirOpen == nil {
		d.dirOpen = map[string]*bool{}
	}
	if d.sideSize == 0 {
		d.sideSize = tokens.Space12 * 11
	}
	if d.metricTable == nil {
		d.metricTable = components.NewTableState()
	}
	if d.compareTable == nil {
		d.compareTable = components.NewTableState()
	}
	if d.historyTable == nil {
		d.historyTable = components.NewTableState()
	}
	if d.API == nil {
		d.unavailable(v)
		return
	}
	project := a.Project()
	if project == nil {
		d.needsProject(v, a)
		return
	}
	d.syncRoute(r)
	d.load(a, project.ID, r)
	d.tickDialogs()

	exp, run, refs, git, snaps, problems := d.core()
	if d.blank() {
		v.PageContainer(components.PageContainerOptions{Children: func() {
			v.PageHeader(components.PageHeaderOptions{Title: i18n.T("pages.experiments.detail_title")})
			v.Skeleton(components.SkeletonOptions{Lines: 8})
		}})
		return
	}
	if exp == nil {
		v.PageContainer(components.PageContainerOptions{Children: func() {
			v.PageHeader(components.PageHeaderOptions{Title: i18n.T("pages.experiments.detail_title")})
			if len(problems) == 0 {
				problems = []string{i18n.T("pages.experiments.err_not_found")}
			}
			for _, problem := range problems {
				v.Banner(components.BannerOptions{
					Tone:    components.Danger,
					Title:   i18n.T("pages.experiments.banner_problem"),
					Message: problem,
				})
			}
			v.EmptyState(components.EmptyStateOptions{
				Icon:        icons.Experiments,
				Title:       i18n.T("pages.experiments.notfound_title"),
				Reason:      i18n.T("pages.experiments.notfound_reason"),
				ActionLabel: i18n.T("pages.experiments.btn_back_list"),
				OnAction:    func() { a.Navigate(ports.Route{Kind: app.RouteExperiments}) },
			})
		}})
		return
	}

	readOnly := project.ArchivedAt != nil
	dirRef := exp.Experiment.DirRef
	title := exp.Experiment.Title

	v.PageContainer(components.PageContainerOptions{Children: func() {
		chosen := -1
		v.PageHeader(components.PageHeaderOptions{
			Breadcrumbs:      []string{i18n.T("pages.experiments.title"), title},
			BreadcrumbChosen: &chosen,
			Title:            title,
			Description:      i18n.T("pages.experiments.detail_desc"),
			Actions: func() {
				if v.Button(i18n.T("pages.experiments.btn_refresh"), components.ButtonOptions{
					Variant: components.VariantGhost,
					Icon:    icons.Refresh,
				}).Clicked() {
					d.invalidate()
					a.Notify()
				}
				if v.Button(i18n.T("pages.experiments.btn_scan_preview"), components.ButtonOptions{
					Variant:  components.VariantSecondary,
					Icon:     icons.Scan,
					Disabled: dirRef == nil,
				}).Clicked() {
					d.scanView = true
				}
			},
		})
		if chosen >= 0 {
			a.Navigate(ports.Route{Kind: app.RouteExperiments})
		}
		if readOnly {
			v.Banner(components.BannerOptions{
				Tone:    components.Warning,
				Title:   i18n.T("pages.experiments.readonly_title"),
				Message: i18n.T("pages.experiments.readonly_message"),
			})
		}
		if run != nil {
			v.Banner(components.BannerOptions{
				Tone:    components.Info,
				Title:   i18n.Tf("pages.experiments.run_banner_title", map[string]string{"name": expRunName(run)}),
				Message: i18n.T("pages.experiments.run_banner_message"),
			})
		}
		d.problemBanners(v, problems)
		d.statusBar(v, a, project.ID, exp, refs, git, snaps, readOnly)

		if d.scanView {
			d.scanPreview(v, a, project.ID, exp, refs, readOnly)
		} else {
			d.split(v, a, project.ID, exp, run, refs, snaps, readOnly)
		}

		d.dialogs(v, a, project.ID, exp, readOnly)
		d.contextDialog(v, a, project.ID, exp, run)
	}})
}

// unavailable is the honest state of a workspace whose experiments module was
// never assembled.
func (d *ExperimentDetail) unavailable(v *components.UI) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{Title: i18n.T("pages.experiments.detail_title")})
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.Experiments,
			Title:  i18n.T("pages.experiments.disabled_title"),
			Reason: i18n.T("pages.experiments.disabled_reason"),
		})
	}})
}

// needsProject states the precondition of the module.
func (d *ExperimentDetail) needsProject(v *components.UI, a *app.App) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{Title: i18n.T("pages.experiments.detail_title")})
		v.EmptyState(components.EmptyStateOptions{
			Icon:        icons.Experiments,
			Title:       i18n.T("pages.experiments.needs_project_title"),
			Reason:      i18n.T("pages.experiments.needs_project_reason"),
			ActionLabel: i18n.T("pages.experiments.btn_projects"),
			OnAction:    func() { a.Navigate(ports.Route{Kind: app.RouteProjects}) },
		})
	}})
}

// syncRoute resets the frame state when the route changes, and applies the
// one-shot view hints the list passes (view=scan|outline|results).
func (d *ExperimentDetail) syncRoute(r ports.Route) {
	key := string(r.Kind) + "|" + r.EntityID + "|" + r.Params["view"]
	if key == d.routeKey {
		return
	}
	d.routeKey = key
	d.tab = expTabOverview
	d.scanView = false
	d.selected = ""
	d.editor = nil
	d.editorFor = ""
	d.historySnap = nil
	d.diffView = nil
	d.diffFor = ""
	d.planMode = 0
	d.fileErr = ""
	d.bundleErr = ""
	d.scanErr = ""
	d.scanInfo = ""
	d.genErr = ""
	d.saveErr = ""
	d.confirmErr = ""
	switch r.Params["view"] {
	case "scan":
		d.scanView = true
	case "outline":
		d.tab = expTabOutline
	case "results":
		d.tab = expTabResults
	}
	d.mu.Lock()
	d.historySnap = nil
	d.mu.Unlock()
}

// load reads the experiment, its runs, ideas, snapshots and Git state once per
// route or shared generation.
func (d *ExperimentDetail) load(a *app.App, projectID string, r ports.Route) {
	runID := ""
	expID := ""
	if r.Kind == ports.RouteRun {
		runID = r.EntityID
	} else {
		expID = r.EntityID
	}
	if expID == "" && runID == "" {
		return
	}
	key := expID + "|" + runID
	d.mu.Lock()
	need := !d.loading && (d.loadedKey != key || d.project != projectID || d.version != d.shared.version())
	if need {
		d.loading = true
		d.project = projectID
		if d.loadedKey != key {
			d.loadedKey = ""
			d.plan = nil
			d.bundle = nil
			d.comparison = nil
			d.diff = nil
			d.diffView = nil
		}
	}
	d.mu.Unlock()
	if !need {
		return
	}

	api := d.API
	a.RunTask(i18n.T("pages.experiments.task_load_detail"), func(ctx context.Context, t *app.Task) error {
		t.Stage(i18n.T("pages.experiments.stage_read"))
		var problems []string
		var run *experiments.RunDetail
		var exp *experiments.ExperimentDetail
		if runID != "" {
			detail, err := api.GetRun(ctx, projectID, runID)
			if err != nil {
				problems = append(problems, expErrText(err))
			} else {
				run = detail
				expID = detail.Run.ExperimentID
			}
		}
		if expID != "" {
			detail, err := api.GetExperiment(ctx, projectID, expID)
			if err != nil {
				problems = append(problems, expErrText(err))
			} else {
				exp = detail
			}
		}
		var refs []experiments.DirReference
		var git *experiments.GitInfo
		var snaps []experiments.SnapshotSummary
		var outline, results *experiments.Snapshot
		var outlineMeta, resultsMeta *experiments.SnapshotSummary
		if exp != nil {
			if health, err := api.VerifyDirectory(ctx, projectID, exp.Experiment.ID); err == nil {
				refs = health
			} else {
				refs = exp.DirRefs
			}
			if dir := exp.Experiment.DirRef; dir != nil && strings.TrimSpace(*dir) != "" {
				if info, err := api.Inspect(ctx, projectID, *dir); err == nil {
					git = info
				}
			}
			if list, err := api.ListSnapshots(ctx, projectID,
				experiments.TargetRef{Type: experiments.TargetExperiment, ID: exp.Experiment.ID},
				experiments.SnapshotFilter{Limit: 100}); err == nil {
				snaps = list
			}
			target := experiments.TargetRef{Type: experiments.TargetExperiment, ID: exp.Experiment.ID}
			if run != nil {
				target = experiments.TargetRef{Type: experiments.TargetRun, ID: run.Run.ID}
			}
			t.Stage(i18n.T("pages.experiments.stage_snapshots"))
			if list, err := api.ListSnapshots(ctx, projectID,
				experiments.TargetRef{Type: experiments.TargetExperiment, ID: exp.Experiment.ID},
				experiments.SnapshotFilter{Kind: experiments.AnalysisOutline, Limit: 1}); err == nil && len(list) > 0 {
				outlineMeta = &list[0]
				if snap, err := api.GetSnapshot(ctx, projectID, list[0].ID); err == nil {
					outline = snap
				}
			}
			if list, err := api.ListSnapshots(ctx, projectID, target,
				experiments.SnapshotFilter{Kind: experiments.AnalysisResults, Limit: 1}); err == nil && len(list) > 0 {
				resultsMeta = &list[0]
				if snap, err := api.GetSnapshot(ctx, projectID, list[0].ID); err == nil {
					results = snap
					d.mu.Lock()
					d.metrics = &snap.Metrics
					d.mu.Unlock()
				}
			}
		}
		d.mu.Lock()
		d.loading = false
		d.exp = exp
		d.run = run
		d.dirRefs = refs
		d.git = git
		d.snapshots = snaps
		d.outline = outline
		d.outlineMeta = outlineMeta
		d.results = results
		d.resultsMeta = resultsMeta
		d.problems = problems
		if exp != nil {
			d.loadedKey = key
			d.version = d.shared.version()
		}
		d.mu.Unlock()
		a.Notify()
		return nil
	})
}

// blank reports whether the first load is still running.
func (d *ExperimentDetail) blank() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.loading && d.exp == nil
}

// core returns the loaded data a frame renders.
func (d *ExperimentDetail) core() (*experiments.ExperimentDetail, *experiments.RunDetail, []experiments.DirReference, *experiments.GitInfo, []experiments.SnapshotSummary, []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.exp, d.run, append([]experiments.DirReference(nil), d.dirRefs...),
		d.git, append([]experiments.SnapshotSummary(nil), d.snapshots...), append([]string(nil), d.problems...)
}

// invalidate asks the next frame to reload from the service.
func (d *ExperimentDetail) invalidate() {
	d.mu.Lock()
	d.loadedKey = ""
	d.version = -1
	d.mu.Unlock()
	d.shared.bump()
}

// problemBanners reports load and action failures.
func (d *ExperimentDetail) problemBanners(v *components.UI, problems []string) {
	for _, problem := range problems {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.experiments.banner_problem"),
			Message: problem,
		})
	}
	d.mu.Lock()
	genErr, scanErr, saveErr, confirmErr, fileErr, bundleErr, diffErr := d.genErr, d.scanErr, d.saveErr, d.confirmErr, d.fileErr, d.bundleErr, d.diffErr
	retry := d.retryOut
	d.mu.Unlock()
	if genErr != "" {
		action := ""
		onAction := func() {}
		if retry != "" {
			action = i18n.T("pages.experiments.btn_retry_save")
			onAction = func() { d.retrySave(projectIDOf(d.Services), retry) }
		}
		_ = onAction
		v.Banner(components.BannerOptions{
			Tone:        components.Danger,
			Title:       i18n.T("pages.experiments.banner_ai_failed"),
			Message:     genErr,
			ActionLabel: action,
			OnAction:    onAction,
		})
	}
	if scanErr != "" {
		v.Banner(components.BannerOptions{Tone: components.Danger, Title: i18n.T("pages.experiments.banner_scan_failed"), Message: scanErr})
	}
	if saveErr != "" {
		v.Banner(components.BannerOptions{Tone: components.Danger, Title: i18n.T("pages.experiments.banner_save_failed"), Message: saveErr})
	}
	if confirmErr != "" {
		v.Banner(components.BannerOptions{Tone: components.Danger, Title: i18n.T("pages.experiments.banner_confirm_failed"), Message: confirmErr})
	}
	if fileErr != "" {
		v.Banner(components.BannerOptions{Tone: components.Danger, Title: i18n.T("pages.experiments.banner_file_failed"), Message: fileErr})
	}
	if bundleErr != "" {
		v.Banner(components.BannerOptions{Tone: components.Warning, Title: i18n.T("pages.experiments.banner_bundle_failed"), Message: bundleErr})
	}
	if diffErr != "" {
		v.Banner(components.BannerOptions{Tone: components.Danger, Title: i18n.T("pages.experiments.banner_diff_failed"), Message: diffErr})
	}
}

// projectIDOf is a tolerant reader for the toast paths: the App may have no
// project while a task finishes after a switch.
func projectIDOf(s *Services) string {
	if s == nil || s.App == nil {
		return ""
	}
	return projectID(s.App)
}

// statusBar renders the §6.2 top bar: directory, health, last scan/analysis,
// Git summary, and the relocate action of a broken directory.
func (d *ExperimentDetail) statusBar(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail, refs []experiments.DirReference, git *experiments.GitInfo, snaps []experiments.SnapshotSummary, readOnly bool) {
	dirRef := exp.Experiment.DirRef
	status := experiments.DirStatus("")
	reason := ""
	var checkedAt *string
	for _, ref := range refs {
		if ref.OwnerType != experiments.TargetExperiment || ref.OwnerID != exp.Experiment.ID {
			continue
		}
		status = ref.Status
		reason = ref.Reason
		if ref.LastCheckedAt != nil {
			t := expTime(*ref.LastCheckedAt)
			checkedAt = &t
		}
	}
	if dirRef == nil || strings.TrimSpace(*dirRef) == "" {
		status = experiments.DirStatus("")
	}
	tone, label := expDirBadge(status)
	lastAnalysis := ""
	for _, snap := range snaps {
		if !snap.CreatedAt.IsZero() {
			lastAnalysis = expTime(snap.CreatedAt)
			break
		}
	}
	d.mu.Lock()
	planAt := d.planAt
	d.mu.Unlock()

	p := v.T.Palette
	card := ui.Column(v.C).FillWidth().Padding(tokens.Space3, tokens.PanelPadding).
		Gap(tokens.Space2).Background(mygo.Color(p.BgSubtle)).Radius(tokens.RadiusLG)
	card.Children(func() {
		ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Wrap().Children(func() {
			ui.Text(v.C, i18n.T("pages.experiments.bar_dir")).
				FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(p.TextMuted))
			if dirRef != nil && *dirRef != "" {
				ui.Text(v.C, *dirRef).Font(tokens.SystemFonts.Mono).
					FontSize(tokens.Typography.Secondary.Size).TextColor(mygo.Color(p.TextPrimary))
			} else {
				ui.Text(v.C, i18n.T("pages.experiments.dir_unbound")).TextColor(mygo.Color(p.TextMuted))
			}
			v.Badge(label, tone)
			if status == experiments.DirBroken && !readOnly {
				if v.Button(i18n.T("pages.experiments.btn_relocate"), components.ButtonOptions{
					Variant: components.VariantSecondary, Size: components.SizeSmall, Icon: icons.FolderOpen,
				}).Clicked() {
					d.openDirectoryDialog()
				}
			} else if dirRef == nil && !readOnly {
				if v.Button(i18n.T("pages.experiments.btn_bind"), components.ButtonOptions{
					Variant: components.VariantSecondary, Size: components.SizeSmall, Icon: icons.FolderOpen,
				}).Clicked() {
					d.openDirectoryDialog()
				}
			}
		})
		ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).AlignItems(ui.Center).Wrap().Children(func() {
			ui.Text(v.C, i18n.Tf("pages.experiments.bar_scan", map[string]string{
				"value": expScanMark(planAt, checkedAt),
			})).FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(p.TextMuted))
			ui.Text(v.C, i18n.Tf("pages.experiments.bar_analysis", map[string]string{
				"value": expOrNone(lastAnalysis),
			})).FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(p.TextMuted))
			ui.Text(v.C, i18n.Tf("pages.experiments.bar_git", map[string]string{
				"value": expGitSummary(git),
			})).FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(p.TextMuted))
		})
		if reason != "" {
			ui.Text(v.C, i18n.Tf("pages.experiments.dir_reason", map[string]string{"reason": reason})).
				FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(p.StatusDanger))
		}
	})
}

// expScanMark renders the last scan timestamp.
func expScanMark(mark timeMark, checked *string) string {
	if mark.set {
		return mark.at
	}
	if checked != nil && *checked != "" {
		return *checked
	}
	return i18n.T("pages.experiments.value_never")
}

// expOrNone renders an optional timestamp or the empty marker.
func expOrNone(value string) string {
	if value == "" {
		return i18n.T("pages.experiments.value_never")
	}
	return value
}

// expGitSummary renders the one-line Git state of the status bar.
func expGitSummary(git *experiments.GitInfo) string {
	if git == nil || !git.Detected {
		return i18n.T("pages.experiments.git_none")
	}
	text := git.Branch
	if text == "" {
		text = i18n.T("pages.experiments.git_detached")
	}
	if git.CommitShort != "" {
		text += " · " + git.CommitShort
	}
	if git.Dirty {
		text += " · " + i18n.T("pages.experiments.git_dirty")
	}
	return text
}

// split renders the §6.2 layout: the run/idea sidebar and the tabbed main area.
func (d *ExperimentDetail) split(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail, run *experiments.RunDetail, refs []experiments.DirReference, snaps []experiments.SnapshotSummary, readOnly bool) {
	v.SplitPanel(components.SplitPanelOptions{
		Size:       &d.sideSize,
		ShowSecond: true,
		First:      func() { d.sidebar(v, a, projectID, exp, run, readOnly) },
		Second: func() {
			d.main(v, a, projectID, exp, run, snaps, readOnly)
		},
	})
}

// sidebar lists the runs and the linked ideas of §6.2/§6.9.
func (d *ExperimentDetail) sidebar(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail, run *experiments.RunDetail, readOnly bool) {
	ui.Column(v.C).Fill().Padding(0, tokens.Space3).Gap(tokens.SectionGap).Children(func() {
		v.Card(components.CardOptions{
			Title:       i18n.T("pages.experiments.card_runs"),
			Description: i18n.T("pages.experiments.runs_manual_note"),
			Actions: func() {
				if v.Button(i18n.T("pages.experiments.btn_new_run"), components.ButtonOptions{
					Variant:  components.VariantPrimary,
					Size:     components.SizeSmall,
					Icon:     icons.Plus,
					Disabled: readOnly,
					Tooltip:  expReadOnlyTooltip(readOnly),
				}).Clicked() {
					d.openCreateRun()
				}
			},
		}, func() {
			if len(exp.Runs) == 0 {
				v.EmptyState(components.EmptyStateOptions{
					Icon:   icons.Experiments,
					Title:  i18n.T("pages.experiments.runs_empty_title"),
					Reason: i18n.T("pages.experiments.runs_empty_reason"),
				})
			} else {
				for i := range exp.Runs {
					d.runRow(v, a, projectID, exp, exp.Runs[i], run, readOnly)
				}
			}
			if !readOnly {
				if v.Button(i18n.T("pages.experiments.btn_run_from_dir"), components.ButtonOptions{
					Variant: components.VariantGhost,
					Size:    components.SizeSmall,
					Icon:    icons.FolderOpen,
				}).Clicked() {
					d.openCandidates(a, projectID, exp.Experiment.ID)
				}
			}
		})
		v.Card(components.CardOptions{
			Title:       i18n.T("pages.experiments.card_ideas"),
			Description: i18n.T("pages.experiments.ideas_desc"),
			Actions: func() {
				if v.Button(i18n.T("pages.experiments.btn_link_idea"), components.ButtonOptions{
					Variant:  components.VariantSecondary,
					Size:     components.SizeSmall,
					Disabled: readOnly,
					Tooltip:  expReadOnlyTooltip(readOnly),
				}).Clicked() {
					d.linkOpen = true
					d.linkErr = ""
				}
			},
		}, func() {
			if len(exp.LinkedIdeas) == 0 {
				ui.Text(v.C, i18n.T("pages.experiments.ideas_empty")).
					FontSize(tokens.Typography.Secondary.Size).
					TextColor(mygo.Color(v.T.Palette.TextMuted))
			}
			for _, idea := range exp.LinkedIdeas {
				ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
					col := ui.Column(v.C).Grow(1)
					col.Children(func() {
						text := idea.Title
						if text == "" {
							text = idea.ID
						}
						ui.Text(v.C, text).FontSize(tokens.Typography.Body.Size).SingleLine()
						if idea.TargetDeleted {
							v.Badge(i18n.T("pages.experiments.idea_deleted"), components.Warning)
						}
					})
					if !readOnly {
						if v.IconButton(icons.Close, i18n.T("pages.experiments.btn_unlink"), components.IconButtonOptions{
							Variant: components.VariantGhost,
							Size:    components.SizeSmall,
						}).Clicked() {
							d.unlinkIdea(a, projectID, exp.Experiment.ID, idea.ID)
						}
					}
				})
			}
		})
	})
}

// runRow renders one run: display name, params summary, status badge and the
// manual status menu.
func (d *ExperimentDetail) runRow(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail, item experiments.RunSummary, current *experiments.RunDetail, readOnly bool) {
	selected := current != nil && current.Run.ID == item.ID
	row := ui.Column(v.C).FillWidth().Padding(tokens.Space2).Radius(tokens.RadiusMD)
	if selected {
		row.Background(mygo.Color(v.T.Palette.AccentSubtle))
	} else if row.Hovered() {
		row.Background(mygo.Color(v.T.Palette.BgHover))
	}
	row.Children(func() {
		ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
			name := item.DisplayName
			if strings.TrimSpace(name) == "" {
				name = i18n.T("pages.experiments.run_unnamed")
			}
			if ui.Text(v.C, name).FontSize(tokens.Typography.Body.Size).Grow(1).SingleLine().RowClicked() {
				a.Navigate(ports.Route{Kind: ports.RouteRun, ProjectID: projectID, EntityID: item.ID})
			}
			v.Badge(expRunStatusLabel(item.Status), expRunTone(item.Status))
		})
		ui.Text(v.C, expParamSummary(item.Params)).
			FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(v.T.Palette.TextMuted)).
			SingleLine()
		if !readOnly {
			ui.MenuButton(v.C, i18n.T("pages.experiments.btn_set_status"), func(m *ui.Menu) {
				for _, status := range []experiments.RunStatus{
					experiments.RunUnknown, experiments.RunRunning,
					experiments.RunFinished, experiments.RunFailed,
				} {
					if m.Item(expRunStatusLabel(status)).Checked(item.Status == status).Chosen() {
						d.setRunStatus(a, projectID, item.ID, status)
					}
				}
			})
		}
	})
}

// main renders the tab bar and the chosen tab.
func (d *ExperimentDetail) main(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail, run *experiments.RunDetail, snaps []experiments.SnapshotSummary, readOnly bool) {
	labels := []string{
		i18n.T("pages.experiments.tab_overview"),
		i18n.T("pages.experiments.tab_outline"),
		i18n.T("pages.experiments.tab_results"),
		i18n.T("pages.experiments.tab_files"),
		i18n.T("pages.experiments.tab_history"),
		i18n.T("pages.experiments.tab_git"),
	}
	v.Tabs(&d.tab, labels...)
	switch d.tab {
	case expTabOutline:
		d.outlineTab(v, a, projectID, exp, readOnly)
	case expTabResults:
		d.resultsTab(v, a, projectID, exp, run, readOnly)
	case expTabFiles:
		d.filesTab(v, a, projectID, exp, run)
	case expTabHistory:
		d.historyTab(v, a, projectID, exp, snaps)
	case expTabGit:
		d.gitTab(v, a, projectID, exp, readOnly)
	default:
		d.overview(v, a, projectID, exp, run, snaps, readOnly)
	}
}

// overview is the landing tab: identity, directory and rules, counts, recent
// analyses and the non-intrusion promise.
func (d *ExperimentDetail) overview(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail, run *experiments.RunDetail, snaps []experiments.SnapshotSummary, readOnly bool) {
	ui.Column(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
		v.Card(components.CardOptions{
			Title: i18n.T("pages.experiments.card_about"),
			Actions: func() {
				if v.Button(i18n.T("pages.experiments.btn_edit"), components.ButtonOptions{
					Variant:  components.VariantGhost,
					Size:     components.SizeSmall,
					Disabled: readOnly,
					Tooltip:  expReadOnlyTooltip(readOnly),
				}).Clicked() {
					d.editOpen = true
					d.editTitle = exp.Experiment.Title
					d.editPurpose = exp.Experiment.Purpose
					d.editErr = ""
				}
			},
		}, func() {
			ui.Text(v.C, i18n.Tf("pages.experiments.about_purpose", map[string]string{
				"value": expOrNone(strings.TrimSpace(exp.Experiment.Purpose)),
			})).FontSize(tokens.Typography.Body.Size)
			ui.Text(v.C, i18n.Tf("pages.experiments.about_counts", map[string]string{
				"runs":      itoa(len(exp.Runs)),
				"ideas":     itoa(len(exp.LinkedIdeas)),
				"snapshots": itoa(len(snaps)),
			})).FontSize(tokens.Typography.Secondary.Size).TextColor(mygo.Color(v.T.Palette.TextSecondary))
		})
		v.Banner(components.BannerOptions{
			Tone:    components.Info,
			Title:   i18n.T("pages.experiments.nonintrusive_title"),
			Message: i18n.T("pages.experiments.nonintrusive_message"),
		})
		v.Card(components.CardOptions{
			Title:       i18n.T("pages.experiments.card_rules"),
			Description: i18n.T("pages.experiments.card_rules_desc"),
			Actions: func() {
				if v.Button(i18n.T("pages.experiments.btn_rules"), components.ButtonOptions{
					Variant:  components.VariantSecondary,
					Size:     components.SizeSmall,
					Disabled: readOnly,
					Tooltip:  expReadOnlyTooltip(readOnly),
				}).Clicked() {
					d.openRules(exp.Experiment.ScanRules)
				}
			},
		}, func() {
			rules := exp.Experiment.ScanRules
			ui.Text(v.C, i18n.Tf("pages.experiments.rules_summary", map[string]string{
				"include": itoa(len(rules.Include)),
				"exclude": itoa(len(rules.Exclude)),
				"file":    humanBytes(rules.MaxFileBytes),
				"total":   humanBytes(rules.MaxTotalBytes),
				"files":   itoa(rules.MaxFiles),
				"images":  expBoolLabel(rules.IncludeImagesInAI),
			})).FontSize(tokens.Typography.Secondary.Size).TextColor(mygo.Color(v.T.Palette.TextSecondary))
			ui.Text(v.C, i18n.T("pages.experiments.rules_git_forced")).
				FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
		})
		v.Card(components.CardOptions{
			Title:       i18n.T("pages.experiments.card_recent"),
			Description: i18n.T("pages.experiments.card_recent_desc"),
		}, func() {
			if len(snaps) == 0 {
				ui.Text(v.C, i18n.T("pages.experiments.recent_empty")).
					FontSize(tokens.Typography.Secondary.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
				return
			}
			for i, snap := range snaps {
				if i == 5 {
					break
				}
				d.snapshotRow(v, a, snap)
			}
		})
	})
}

// expBoolLabel renders a rule flag.
func expBoolLabel(on bool) string {
	if on {
		return i18n.T("pages.experiments.value_yes")
	}
	return i18n.T("pages.experiments.value_no")
}

// itoa formats a count for a label.
func itoa(n int) string { return strconv.Itoa(n) }

// fmtInt64 formats a byte count for the rules editor.
func fmtInt64(n int64) string { return strconv.FormatInt(n, 10) }

// snapshotRow is one read-only history row used by the overview.
func (d *ExperimentDetail) snapshotRow(v *components.UI, a *app.App, snap experiments.SnapshotSummary) {
	row := ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center)
	row.Children(func() {
		ui.Text(v.C, expTime(snap.CreatedAt)).FontSize(tokens.Typography.Secondary.Size).
			TextColor(mygo.Color(v.T.Palette.TextSecondary)).FontFeatures("tnum")
		v.Badge(expAnalysisLabel(snap.Kind), components.Neutral)
		tone, label := expAIStatus(snap.AIStatus)
		v.Badge(label, tone)
		ui.Text(v.C, snap.ModelID).FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(v.T.Palette.TextMuted)).SingleLine()
	})
	if row.Hovered() {
		row.Background(mygo.Color(v.T.Palette.BgHover))
	}
}

// openCreateRun resets and opens the new Run dialog.
func (d *ExperimentDetail) openCreateRun() {
	d.createRunOpen = true
	d.runErr = ""
	d.runDir = ""
	if len(d.runParams) == 0 {
		d.runParams = []expParamRow{{}}
	}
}

// openCandidates lists the first-level subdirectories the service offers.
func (d *ExperimentDetail) openCandidates(a *app.App, projectID, experimentID string) {
	d.candOpen = true
	d.candErr = ""
	d.candidates = nil
	api := d.API
	a.RunTask(i18n.T("pages.experiments.task_candidates"), func(ctx context.Context, t *app.Task) error {
		list, err := api.ListDirCandidates(ctx, projectID, experimentID)
		d.mu.Lock()
		if err != nil {
			d.candErr = expErrText(err)
		} else {
			d.candidates = list
		}
		d.mu.Unlock()
		a.Notify()
		return nil
	})
}

// openDirectoryDialog resets the bind/relocate dialog.
func (d *ExperimentDetail) openDirectoryDialog() {
	d.dirOpenDlg = true
	d.dirPick = ""
	d.dirErr = ""
}

// openRules copies the effective rules into the editor fields.
func (d *ExperimentDetail) openRules(rules experiments.ScanRules) {
	d.rulesOpen = true
	d.rulesErr = ""
	d.rulesInclude = strings.Join(rules.Include, "\n")
	d.rulesExclude = strings.Join(rules.Exclude, "\n")
	d.rulesMaxFile = fmtInt64(rules.MaxFileBytes)
	d.rulesMaxTot = fmtInt64(rules.MaxTotalBytes)
	d.rulesMaxN = itoa(rules.MaxFiles)
	d.rulesHash = fmtInt64(rules.HashMaxBytes)
}

// tickDialogs closes a dialog whose service call finished.
func (d *ExperimentDetail) tickDialogs() {
	// The dialogs below close themselves on success; nothing is deferred.
}

// setRunStatus writes one manual status change (SPEC-07 O-02: no detection).
func (d *ExperimentDetail) setRunStatus(a *app.App, projectID, runID string, status experiments.RunStatus) {
	api := d.API
	a.RunTask(i18n.T("pages.experiments.task_run_status"), func(ctx context.Context, t *app.Task) error {
		if err := api.SetRunStatus(ctx, projectID, runID, status); err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.experiments.toast_status_failed"), expErrText(err))
		} else {
			a.Toast(app.LevelSuccess, i18n.T("pages.experiments.toast_status_saved"), "")
		}
		d.invalidate()
		a.Notify()
		return nil
	})
}

// unlinkIdea removes the validates relation.
func (d *ExperimentDetail) unlinkIdea(a *app.App, projectID, experimentID, ideaID string) {
	api := d.API
	a.RunTask(i18n.T("pages.experiments.task_unlink"), func(ctx context.Context, t *app.Task) error {
		if err := api.UnlinkIdea(ctx, projectID, experimentID, ideaID, ports.ActorUser); err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.experiments.toast_unlink_failed"), expErrText(err))
		}
		d.invalidate()
		a.Notify()
		return nil
	})
}

// retrySave retries the snapshot write for an already generated draft.
func (d *ExperimentDetail) retrySave(projectID, aiOutputID string) {
	a := d.Services.App
	if a == nil || projectID == "" || aiOutputID == "" {
		return
	}
	api := d.API
	d.mu.Lock()
	d.busy["save"] = true
	d.mu.Unlock()
	a.RunTask(i18n.T("pages.experiments.task_retry_save"), func(ctx context.Context, t *app.Task) error {
		snap, err := api.RetrySaveSnapshot(ctx, projectID, aiOutputID)
		d.mu.Lock()
		d.busy["save"] = false
		if err != nil {
			d.genErr = expErrText(err)
		} else {
			d.retryOut = ""
			d.genErr = ""
			if snap.Target.Type == experiments.TargetExperiment {
				d.outline = snap
			}
		}
		d.mu.Unlock()
		d.invalidate()
		a.Notify()
		return nil
	})
}

// name of a run for banners and toasts.
func expRunName(run *experiments.RunDetail) string {
	if run == nil {
		return ""
	}
	if len(run.Run.Params) > 0 {
		return expParamSummary(run.Run.Params)
	}
	if run.Run.DirRef != nil && *run.Run.DirRef != "" {
		return *run.Run.DirRef
	}
	return run.Run.ID
}
