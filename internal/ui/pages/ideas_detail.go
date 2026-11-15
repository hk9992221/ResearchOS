package pages

// The Idea detail page is the SPEC-06 §6.3-§6.7 screen: the inline-editable
// title, the grouped status selector, the derivation and delete actions, and
// the four tabs (overview, evidence, relations, history). It shares the list's
// generation, so a write here makes the returning list reload.

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"

	"researchos/internal/i18n"
	"researchos/internal/ideas"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/formula"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// ideasEvidenceLimit is how many evidence rows the panel holds. The service
// caps a page at 200; an Idea's evidence is reviewed as one list.
const ideasEvidenceLimit = 200

// ideaEditorHeight is the height of the description editor.
const ideaEditorHeight float32 = 360

// ideasNarrowWidth is where the evidence columns fall back to tabs: below it
// two Markdown columns would be too narrow to read (SPEC-06 §6.5).
const ideasNarrowWidth float32 = 900

// The four tabs of the detail page.
const (
	ideaTabOverview = iota
	ideaTabEvidence
	ideaTabRelations
	ideaTabHistory
)

// The source and view modes of the description area.
const (
	ideaDescSource = iota
	ideaDescPreview
)

// The link picker kinds, in display order.
var ideaLinkKinds = []struct {
	typ   string
	label string
}{
	{ideas.TargetPaper, "pages.ideas.link_paper"},
	{ideas.TargetNote, "pages.ideas.link_note"},
	{ideas.TargetExperiment, "pages.ideas.link_experiment"},
}

// IdeaPage renders one Idea: its header, its description and its evidence,
// relation and history tabs.
type IdeaPage struct {
	Services *Services
	shared   *ideasShared

	mu          sync.Mutex
	loading     bool
	loaded      bool
	loadedID    string
	project     string
	version     int
	detail      *ideas.IdeaDetail
	evidence    []ideas.EvidenceView
	problems    []string
	action      string
	titleSaving bool
	titleErr    string
	statusBusy  bool
	savingDesc  bool
	descErr     string
	deleting    bool
	deriving    bool
	derivingErr string
	paperURL    string
	paperBusy   bool
	reason      ideaReasonForm
	candidates  ideaCandidates

	// Evidence panel state: the batch failures to show, the manual add form
	// and the narrow-window column choice.
	evidenceFailures []ideas.EvidenceFailure
	evidenceBusy     bool
	evidenceSide     int
	// evidenceConfirm is the short gate in front of accepting one AI draft.
	evidenceConfirmOpen bool
	evidenceConfirmID   string
	addOpen             bool
	addDirection        ideas.Direction
	addContent          string
	addKind             int
	addQuery            string
	addSource           ideas.SourceRef
	addErr              string
	addBusy             bool
	addCandidates       ideaCandidates

	// AI draft review: the source selection, the generated drafts and the
	// provenance of the run that produced them.
	aiOpen        bool
	aiInstruction string
	aiSelected    map[string]ideas.SourceRef
	aiOrder       []string
	aiLens        map[string]string
	aiResolving   map[string]bool
	aiGroups      []ideaCandidates
	aiBusy        bool
	aiFinished    bool
	aiErrCode     string
	aiErrText     string
	aiDrafts      []*ideaDraft
	aiSkipped     []ideas.SkippedDraft
	aiProvider    string
	aiModel       string
	aiTask        *app.Task

	// History tab state.
	history      ideaHistory
	historyKinds []ideas.HistoryKind

	// Frame-only state: the title draft, the tabs, the dialogs and the
	// description buffer, all rebuilt or decided by the frame that draws them.
	tab        int
	lastTab    int
	title      string
	titleFor   string
	titleTouch bool
	titleWasFo bool
	reasonOpen bool
	md         *markdown.Options
	descMode   int
	ed         *editor.State
	edFor      string
	deriveOpen bool
	deriveTit  string
	deleteOpen bool
	linkOpen   bool
	linkKind   int
	linkQuery  string
	linkPick   string
	linkBusy   bool
	frameC     *ui.Context
}

// Page renders one frame of the Idea detail.
func (e *IdeaPage) Page(v *components.UI, r ports.Route) {
	a := e.Services.App
	if a == nil {
		return
	}
	if e.Services.Ideas == nil {
		e.unavailable(v)
		return
	}
	project := a.Project()
	if project == nil {
		e.needsProject(v, a)
		return
	}
	ideaID := r.EntityID
	if ideaID == "" {
		a.Navigate(ports.Route{Kind: app.RouteIdeas})
		return
	}
	e.prepare()
	e.frameC = v.C
	e.tickLoad(a, project.ID, ideaID)
	e.syncTitle()

	detail := e.detailSnapshot()
	archived := project.ArchivedAt != nil
	readOnly := archived || (detail != nil && detail.DeletedAt != nil)
	e.ensureEditor()
	e.tickTabSwitch(a, project.ID, readOnly)
	e.openPendingPaper(v)

	v.PageContainer(components.PageContainerOptions{Children: func() {
		if detail == nil {
			e.header(v, a, project.ID, nil, readOnly)
			if e.blank() {
				v.Skeleton(components.SkeletonOptions{Lines: 6})
			}
			return
		}
		e.header(v, a, project.ID, detail, readOnly)
		if archived {
			v.Banner(components.BannerOptions{
				Tone:    components.Warning,
				Title:   i18n.T("pages.ideas.readonly_title"),
				Message: i18n.T("pages.ideas.readonly_message"),
			})
		}
		if detail.DeletedAt != nil {
			v.Banner(components.BannerOptions{
				Tone:    components.Warning,
				Title:   i18n.T("pages.ideas.deleted_title"),
				Message: i18n.T("pages.ideas.deleted_message"),
			})
		}
		e.banners(v)
		v.Tabs(&e.tab,
			i18n.T("pages.ideas.tab_overview"),
			i18n.T("pages.ideas.tab_evidence"),
			i18n.T("pages.ideas.tab_relations"),
			i18n.T("pages.ideas.tab_history"),
		)
		switch e.tab {
		case ideaTabEvidence:
			e.evidenceTab(v, a, project.ID, detail, readOnly)
		case ideaTabRelations:
			e.relationsTab(v, a, project.ID, detail, readOnly)
		case ideaTabHistory:
			e.historyTab(v, a, project.ID, detail)
		default:
			e.overviewTab(v, a, project.ID, detail, readOnly)
		}
		e.reasonDialog(v, a, project.ID)
		e.deriveDialog(v, a, project.ID, detail, readOnly)
		e.deleteDialog(v, a, project.ID, detail, readOnly)
		e.addEvidenceDialog(v, a, project.ID, detail, readOnly)
		e.linkDialog(v, a, project.ID, detail, readOnly)
		e.aiDialog(v, a, project.ID, detail, readOnly)
	}})
}

// unavailable is the honest state of a workspace whose Ideas module was never
// assembled.
func (e *IdeaPage) unavailable(v *components.UI) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{Title: i18n.T("pages.ideas.detail_title")})
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.Ideas,
			Title:  i18n.T("pages.ideas.disabled_title"),
			Reason: i18n.T("pages.ideas.disabled_reason"),
		})
	}})
}

func (e *IdeaPage) needsProject(v *components.UI, a *app.App) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.ideas.detail_title"),
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

// prepare initializes the frame state a zero page does not have yet.
func (e *IdeaPage) prepare() {
	if e.md == nil {
		e.md = &markdown.Options{
			Extensions: parser.Default | parser.ExtDollarMath,
			Math:       formula.Block,
			InlineMath: formula.Inline,
			OnLink:     e.openLink,
		}
	}
	if e.aiGroups == nil {
		// 0: experiments, 1: runs, 2: notes — the groups §6.6 offers.
		e.aiGroups = make([]ideaCandidates, 3)
	}
	if e.aiSelected == nil {
		e.aiSelected = map[string]ideas.SourceRef{}
	}
	if e.aiLens == nil {
		e.aiLens = map[string]string{}
	}
	if e.aiResolving == nil {
		e.aiResolving = map[string]bool{}
	}
	if e.addDirection == "" {
		e.addDirection = ideas.DirectionSupport
	}
}

// tickLoad reloads the detail when the route changed or the shared generation
// moved (the page's own writes reload explicitly).
func (e *IdeaPage) tickLoad(a *app.App, projectID, ideaID string) {
	e.mu.Lock()
	need := !e.loading && (e.project != projectID || !e.loaded || e.loadedID != ideaID || e.version != e.shared.version())
	e.mu.Unlock()
	if need {
		e.reload(a, projectID, ideaID)
	}
}

// reload reads the Idea and its evidence.
func (e *IdeaPage) reload(a *app.App, projectID, ideaID string) {
	api := e.Services.Ideas
	e.mu.Lock()
	if e.loading {
		e.mu.Unlock()
		return
	}
	e.loading = true
	if e.project != projectID || e.loadedID != ideaID {
		e.detail = nil
		e.evidence = nil
		e.loaded = false
		e.version = 0
		e.evidenceConfirmOpen = false
		e.evidenceConfirmID = ""
	}
	gen := e.shared.version()
	e.mu.Unlock()

	a.RunTask(i18n.T("pages.ideas.task_detail"), func(ctx context.Context, t *app.Task) error {
		detail, err := api.GetIdea(ctx, projectID, ideaID)
		var evidence []ideas.EvidenceView
		if err == nil {
			var page ideas.Page[ideas.EvidenceView]
			page, err = api.ListEvidence(ctx, projectID, ideas.ListEvidenceQuery{
				IdeaID:   ideaID,
				PageSize: ideasEvidenceLimit,
			})
			if err == nil {
				evidence = page.Items
			}
		}
		e.mu.Lock()
		e.loading = false
		e.project = projectID
		e.loadedID = ideaID
		e.loaded = true
		e.version = gen
		if err != nil {
			e.problems = append(e.problems[:0], ideaErrText(err))
		} else {
			e.detail = detail
			e.evidence = evidence
			e.problems = nil
		}
		e.mu.Unlock()
		a.Notify()
		return nil
	})
}

// blank reports whether the first load is still running.
func (e *IdeaPage) blank() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loading && !e.loaded
}

func (e *IdeaPage) detailSnapshot() *ideas.IdeaDetail {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.detail
}

func (e *IdeaPage) evidenceSnapshot() []ideas.EvidenceView {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]ideas.EvidenceView(nil), e.evidence...)
}

// banners reports load failures and the last action's failure.
func (e *IdeaPage) banners(v *components.UI) {
	e.mu.Lock()
	problems := append([]string(nil), e.problems...)
	action := e.action
	titleErr := e.titleErr
	descErr := e.descErr
	e.mu.Unlock()
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
	for _, text := range []string{titleErr, descErr} {
		if text == "" {
			continue
		}
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.ideas.banner_save"),
			Message: text,
		})
	}
}

// header is the title draft, the status selector and the row actions.
func (e *IdeaPage) header(v *components.UI, a *app.App, projectID string, detail *ideas.IdeaDetail, readOnly bool) {
	title := i18n.T("pages.ideas.detail_title")
	if detail != nil {
		title = ideaTitle(detail.Title)
	}
	ui.Column(v.C).FillWidth().Gap(tokens.Space2).Children(func() {
		ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
			if v.IconButton(icons.Back, i18n.T("pages.ideas.btn_back"), components.IconButtonOptions{
				Variant: components.VariantGhost,
			}).Clicked() {
				e.saveDescription(a, projectID, detail, readOnly, false)
				a.Navigate(ports.Route{Kind: app.RouteIdeas})
			}
			if detail == nil {
				ui.Text(v.C, title).
					Grow(1).
					FontSize(tokens.Typography.PageTitle.Size).
					FontWeight(int(tokens.Typography.PageTitle.Weight))
				return
			}
			ui.Column(v.C).Grow(1).Gap(tokens.Space1).Children(func() {
				input := v.Input(&e.title, components.FieldOptions{
					Label:       i18n.T("pages.ideas.field_idea_title"),
					Placeholder: i18n.T("pages.ideas.field_title_hint"),
					Disabled:    readOnly,
				})
				input.FillWidth()
				focused := input.Focused()
				composing := input.Composing()
				if input.Changed() {
					e.titleTouch = true
				}
				switch {
				case composing:
					// A composition is never committed mid-word (§6.3).
				case input.Submitted():
					e.commitTitle(a, projectID, detail, readOnly)
				case e.titleWasFo && !focused && e.titleTouch:
					e.commitTitle(a, projectID, detail, readOnly)
				case focused && v.C.Shortcut(0, ui.KeyEscape):
					e.title = detail.Title
					e.titleTouch = false
				}
				e.titleWasFo = focused
			})
			e.statusSelector(v, a, projectID, detail, readOnly)
			v.Button(i18n.T("pages.ideas.btn_derive"), components.ButtonOptions{
				Variant:  components.VariantSecondary,
				Icon:     icons.Plus,
				Align:    ui.Center,
				Disabled: readOnly,
				Tooltip:  readOnlyTooltip(readOnly),
				OnClick: func() {
					e.deriveOpen = true
					e.deriveTit = i18n.Tf("pages.ideas.derive_prefill", map[string]string{"title": detail.Title})
					e.mu.Lock()
					e.derivingErr = ""
					e.mu.Unlock()
				},
			})
			v.IconButton(icons.Trash, i18n.T("pages.ideas.btn_delete"), components.IconButtonOptions{
				Variant:  components.VariantGhost,
				Disabled: readOnly,
				OnClick:  func() { e.deleteOpen = true },
			})
		})
		if detail == nil {
			return
		}
		ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Wrap().Children(func() {
			v.Badge(ideaStatusLabel(detail.Status), ideaStatusTone(detail.Status))
			ui.Text(v.C, i18n.Tf("pages.ideas.updated_at", map[string]string{
				"time": ideaTimestamp(detail.UpdatedAt),
			})).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(v.T.Palette.TextMuted))
			if detail.RejectionReason != "" {
				e.rejectionSummary(v, detail)
			}
		})
	})
}

// rejectionSummary shows the rejection reason beside the badge and keeps it
// there after the status left Rejected (§6.4, AC-05).
func (e *IdeaPage) rejectionSummary(v *components.UI, detail *ideas.IdeaDetail) {
	label := i18n.T("pages.ideas.rejection_label")
	text := detail.RejectionReason
	if !e.reasonOpen {
		text = truncateRunes(strings.ReplaceAll(text, "\n", " "), 60)
	}
	row := ui.Row(v.C).Gap(tokens.Space1).AlignItems(ui.Center).Padding(tokens.Space1, tokens.Space2).
		Background(mygo.Color(v.T.Palette.BgSubtle)).
		Radius(tokens.RadiusSM).
		OnClick(func() { e.reasonOpen = !e.reasonOpen })
	row.Children(func() {
		ui.Text(v.C, label+"：").
			FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(v.T.Palette.TextSecondary))
		ui.Text(v.C, text).
			FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(v.T.Palette.TextPrimary)).
			MaxLines(1)
	})
}

// statusSelector is the grouped status menu: the next-step suggestions first,
// then every other status. The graph never restricts a transition.
func (e *IdeaPage) statusSelector(v *components.UI, a *app.App, projectID string, detail *ideas.IdeaDetail, readOnly bool) {
	e.mu.Lock()
	busy := e.statusBusy
	e.mu.Unlock()
	button := ui.MenuButton(v.C, ideaStatusLabel(detail.Status), func(m *ui.Menu) {
		m.Submenu(i18n.T("pages.ideas.status_next"), func(m *ui.Menu) {
			for _, status := range ideaNextSteps(detail.Status) {
				item := m.Item(ideaStatusLabel(status)).ID(string(status)).Checked(status == detail.Status)
				if item.Chosen() {
					e.chooseStatus(a, projectID, detail, status)
				}
			}
		})
		others := ideaOtherStatuses(detail.Status)
		if len(others) > 0 {
			m.Submenu(i18n.T("pages.ideas.status_other"), func(m *ui.Menu) {
				for _, status := range others {
					item := m.Item(ideaStatusLabel(status)).ID(string(status)).Checked(status == detail.Status)
					if item.Chosen() {
						e.chooseStatus(a, projectID, detail, status)
					}
				}
			})
		}
	}).Label(i18n.T("pages.ideas.status_label"))
	if readOnly || busy {
		button.Disabled(true)
	}
}

// chooseStatus applies a menu choice, or asks for the reason Rejected needs.
func (e *IdeaPage) chooseStatus(a *app.App, projectID string, detail *ideas.IdeaDetail, to ideas.Status) {
	if to == detail.Status {
		return
	}
	if to == ideas.StatusRejected {
		e.reason = ideaReasonForm{
			open:   true,
			ideaID: detail.ID,
			title:  detail.Title,
			from:   detail.Status,
			to:     to,
		}
		return
	}
	e.applyStatus(a, projectID, detail.ID, to, "")
}

// applyStatus is the immediate transition (§6.4: no second confirmation except
// Rejected).
func (e *IdeaPage) applyStatus(a *app.App, projectID, ideaID string, to ideas.Status, reason string) {
	e.mu.Lock()
	if e.statusBusy {
		e.mu.Unlock()
		return
	}
	e.statusBusy = true
	e.action = ""
	e.mu.Unlock()

	api := e.Services.Ideas
	a.RunTask(i18n.T("pages.ideas.task_status"), func(ctx context.Context, t *app.Task) error {
		_, err := api.ChangeStatus(ctx, projectID, ideas.ChangeStatusInput{
			ID:              ideaID,
			To:              to,
			RejectionReason: strings.TrimSpace(reason),
			Actor:           ports.ActorUser,
		})
		e.mu.Lock()
		e.statusBusy = false
		if err != nil {
			e.action = ideaErrText(err)
		}
		e.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.ideas.toast_status_fail"), ideaErrText(err))
			a.Notify()
			return nil
		}
		e.shared.bump()
		a.Toast(app.LevelSuccess, i18n.T("pages.ideas.toast_status"), ideaStatusLabel(to))
		a.Notify()
		return nil
	})
}

// reasonDialog collects the reason of a transition to Rejected.
func (e *IdeaPage) reasonDialog(v *components.UI, a *app.App, projectID string) {
	form := &e.reason
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
				e.reason = ideaReasonForm{}
				return
			}
			valid := strings.TrimSpace(form.text) != ""
			if v.Button(i18n.T("pages.ideas.reason_confirm"), components.ButtonOptions{
				Variant:  components.VariantDanger,
				Loading:  form.busy,
				Disabled: form.busy || !valid,
			}).Clicked() && valid {
				e.submitReason(a, projectID)
			}
		},
	}, func() {
		field := v.Textarea(&form.text, components.FieldOptions{
			Label:       i18n.T("pages.ideas.field_reason"),
			Placeholder: i18n.T("pages.ideas.field_reason_hint"),
			AutoFocus:   true,
		})
		if field.Submitted() {
			e.submitReason(a, projectID)
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
		e.reason = ideaReasonForm{}
	}
}

// submitReason applies the pending transition with its reason.
func (e *IdeaPage) submitReason(a *app.App, projectID string) {
	form := &e.reason
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
	api := e.Services.Ideas
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
		form.open = false
		e.shared.bump()
		a.Toast(app.LevelSuccess, i18n.T("pages.ideas.toast_status"), ideaStatusLabel(to))
		a.Notify()
		return nil
	})
}

// syncTitle keeps the title draft in step with the loaded Idea: a new route
// resets it, and a reload refreshes it only while it is untouched.
func (e *IdeaPage) syncTitle() {
	detail := e.detailSnapshot()
	if detail == nil {
		return
	}
	if e.titleFor != detail.ID {
		e.titleFor = detail.ID
		e.title = detail.Title
		e.titleTouch = false
		return
	}
	if !e.titleTouch && e.title != detail.Title {
		e.title = detail.Title
	}
}

// commitTitle saves the title draft through UpdateIdea.
func (e *IdeaPage) commitTitle(a *app.App, projectID string, detail *ideas.IdeaDetail, readOnly bool) {
	title := strings.TrimSpace(e.title)
	if readOnly {
		e.title = detail.Title
		e.titleTouch = false
		return
	}
	if title == detail.Title {
		e.titleTouch = false
		return
	}
	if title == "" {
		a.Toast(app.LevelWarning, i18n.T("pages.ideas.title_required"), "")
		e.title = detail.Title
		e.titleTouch = false
		return
	}
	if utf8.RuneCountInString(title) > ideasMaxTitleRunes {
		a.Toast(app.LevelWarning, i18n.T("pages.ideas.title_too_long"), "")
		return
	}
	e.mu.Lock()
	if e.titleSaving {
		e.mu.Unlock()
		return
	}
	e.titleSaving = true
	e.titleErr = ""
	e.mu.Unlock()

	api := e.Services.Ideas
	a.RunTask(i18n.T("pages.ideas.task_title"), func(ctx context.Context, t *app.Task) error {
		_, err := api.UpdateIdea(ctx, projectID, ideas.UpdateIdeaInput{
			ID:    detail.ID,
			Title: &title,
			Actor: ports.ActorUser,
		})
		e.mu.Lock()
		e.titleSaving = false
		if err != nil {
			e.titleErr = ideaErrText(err)
		}
		e.mu.Unlock()
		if err != nil {
			a.Notify()
			return nil
		}
		e.titleTouch = false
		e.shared.bump()
		a.Notify()
		return nil
	})
}

// ---------------------------------------------------------------------------
// Overview
// ---------------------------------------------------------------------------

// overviewTab is the Markdown description and the sidebar of §6.3.
func (e *IdeaPage) overviewTab(v *components.UI, a *app.App, projectID string, detail *ideas.IdeaDetail, readOnly bool) {
	ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).AlignItems(ui.Start).Children(func() {
		ui.Column(v.C).Grow(1).Gap(tokens.RelatedGap).Children(func() {
			if readOnly {
				e.readOnlyDescription(v, detail)
				return
			}
			e.descriptionControls(v, a, projectID, detail)
		})
		ui.Column(v.C).Width(ideasSidebarWidth).Gap(tokens.RelatedGap).Children(func() {
			e.sidebar(v, a, projectID, detail, readOnly)
		})
	})
}

// readOnlyDescription renders the Markdown of an archived or deleted Idea.
func (e *IdeaPage) readOnlyDescription(v *components.UI, detail *ideas.IdeaDetail) {
	v.Card(components.CardOptions{Title: i18n.T("pages.ideas.overview_card")}, func() {
		if strings.TrimSpace(detail.DescriptionMD) == "" {
			ui.Text(v.C, i18n.T("pages.ideas.description_empty")).
				FontSize(tokens.Typography.Body.Size).
				TextColor(mygo.Color(v.T.Palette.TextMuted))
			return
		}
		markdown.View(v.C, detail.DescriptionMD, e.md).FillWidth()
	})
}

// descriptionControls is the source/preview switch, the editor and the save
// action.
func (e *IdeaPage) descriptionControls(v *components.UI, a *app.App, projectID string, detail *ideas.IdeaDetail) {
	e.mu.Lock()
	ed, saving, descErr := e.ed, e.savingDesc, e.descErr
	e.mu.Unlock()

	v.Card(components.CardOptions{Title: i18n.T("pages.ideas.overview_card")}, func() {
		ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).AlignItems(ui.Center).Children(func() {
			ui.Segmented(v.C, &e.descMode,
				i18n.T("pages.ideas.desc_source"),
				i18n.T("pages.ideas.desc_preview"),
			).Label(i18n.T("pages.ideas.desc_label"))
			ui.Spacer(v.C)
			if descErr != "" {
				ui.Text(v.C, descErr).
					FontSize(tokens.Typography.Caption.Size).
					TextColor(mygo.Color(v.T.Palette.StatusDanger))
			}
			if ed != nil && ed.Buffer.Modified() && e.descMode == ideaDescPreview {
				ui.Text(v.C, i18n.T("pages.ideas.desc_draft")).
					FontSize(tokens.Typography.Caption.Size).
					TextColor(mygo.Color(v.T.Palette.TextMuted))
			}
			if v.Button(i18n.T("pages.ideas.btn_save"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Loading:  saving,
				Disabled: saving || ed == nil || !ed.Buffer.Modified(),
			}).Clicked() {
				e.saveDescription(a, projectID, detail, false, true)
			}
		})
		if ed == nil {
			v.Skeleton(components.SkeletonOptions{Lines: 8})
			return
		}
		if e.descMode == ideaDescPreview {
			if strings.TrimSpace(ed.Buffer.String()) == "" {
				ui.Text(v.C, i18n.T("pages.ideas.description_empty")).
					FontSize(tokens.Typography.Body.Size).
					TextColor(mygo.Color(v.T.Palette.TextMuted))
				return
			}
			markdown.View(v.C, ed.Buffer.String(), e.md).FillWidth()
			return
		}
		editor.View(v.C, ed).FillWidth().Height(ideaEditorHeight)
	})
}

// ensureEditor builds the description buffer once the Idea arrived. The buffer
// survives frames; it is rebuilt only for another Idea or when the stored text
// changed underneath an unmodified buffer.
func (e *IdeaPage) ensureEditor() {
	detail := e.detailSnapshot()
	if detail == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ed != nil && e.edFor == detail.ID {
		if e.ed.Buffer.Modified() || e.ed.Buffer.String() == detail.DescriptionMD {
			return
		}
	}
	buffer := editor.NewBuffer(detail.DescriptionMD)
	buffer.SetLanguage(editor.LanguageFor("idea.md"))
	e.ed = &editor.State{Buffer: buffer}
	e.edFor = detail.ID
}

// tickTabSwitch saves an unsaved description when the user leaves the overview
// tab, so switching cannot lose an edit.
func (e *IdeaPage) tickTabSwitch(a *app.App, projectID string, readOnly bool) {
	if e.lastTab == e.tab {
		return
	}
	previous := e.lastTab
	e.lastTab = e.tab
	if previous != ideaTabOverview || readOnly {
		return
	}
	e.saveDescription(a, projectID, e.detailSnapshot(), readOnly, false)
}

// saveDescription writes the description buffer through UpdateIdea. A save
// asked for by the button also reports why it refused or failed.
func (e *IdeaPage) saveDescription(a *app.App, projectID string, detail *ideas.IdeaDetail, readOnly, explicit bool) {
	if detail == nil || readOnly {
		return
	}
	e.mu.Lock()
	ed := e.ed
	if e.savingDesc || ed == nil || !ed.Buffer.Modified() {
		e.mu.Unlock()
		return
	}
	text := ed.Buffer.String()
	e.savingDesc = true
	e.descErr = ""
	e.mu.Unlock()

	api := e.Services.Ideas
	ideaID := detail.ID
	a.RunTask(i18n.T("pages.ideas.task_description"), func(ctx context.Context, t *app.Task) error {
		_, err := api.UpdateIdea(ctx, projectID, ideas.UpdateIdeaInput{
			ID:            ideaID,
			DescriptionMD: &text,
			Actor:         ports.ActorUser,
		})
		e.mu.Lock()
		e.savingDesc = false
		if err != nil {
			e.descErr = ideaErrText(err)
		}
		e.mu.Unlock()
		if err != nil {
			if explicit {
				a.Toast(app.LevelDanger, i18n.T("pages.ideas.toast_description_fail"), ideaErrText(err))
			}
			a.Notify()
			return nil
		}
		ed.Buffer.MarkSaved()
		e.shared.bump()
		if explicit {
			a.Toast(app.LevelSuccess, i18n.T("pages.ideas.toast_description"), "")
		}
		a.Notify()
		return nil
	})
}

// sidebar is the overview's information card: times, the historical rejection
// reason and the derivation links.
func (e *IdeaPage) sidebar(v *components.UI, a *app.App, projectID string, detail *ideas.IdeaDetail, readOnly bool) {
	v.Card(components.CardOptions{Title: i18n.T("pages.ideas.sidebar_title")}, func() {
		timeRow(v, i18n.T("pages.ideas.sidebar_created"), ideaTimestamp(detail.CreatedAt))
		timeRow(v, i18n.T("pages.ideas.sidebar_updated"), ideaTimestamp(detail.UpdatedAt))
		if detail.RejectionReason != "" {
			ui.Column(v.C).Gap(tokens.Space1).Children(func() {
				ui.Text(v.C, i18n.T("pages.ideas.rejection_label")).
					FontSize(tokens.Typography.Caption.Size).
					FontWeight(int(tokens.WeightSemibold)).
					TextColor(mygo.Color(v.T.Palette.TextSecondary))
				ui.Text(v.C, detail.RejectionReason).
					FontSize(tokens.Typography.Secondary.Size).
					TextColor(mygo.Color(v.T.Palette.TextPrimary)).
					MaxLines(6)
			})
		}
		if len(detail.Relations.DerivedFrom) > 0 {
			ui.Column(v.C).Gap(tokens.Space1).Children(func() {
				ui.Text(v.C, i18n.T("pages.ideas.sidebar_derived_from")).
					FontSize(tokens.Typography.Caption.Size).
					FontWeight(int(tokens.WeightSemibold)).
					TextColor(mygo.Color(v.T.Palette.TextSecondary))
				for _, ref := range detail.Relations.DerivedFrom {
					e.relationRow(v, a, projectID, ref, "", readOnly)
				}
			})
		}
		if len(detail.Relations.Derived) > 0 {
			ui.Column(v.C).Gap(tokens.Space1).Children(func() {
				ui.Text(v.C, i18n.T("pages.ideas.sidebar_derived")).
					FontSize(tokens.Typography.Caption.Size).
					FontWeight(int(tokens.WeightSemibold)).
					TextColor(mygo.Color(v.T.Palette.TextSecondary))
				for _, ref := range detail.Relations.Derived {
					e.relationRow(v, a, projectID, ref, "", readOnly)
				}
			})
		}
		e.overviewHistory(v, detail)
	})
}

// overviewHistory is the short history list of the overview tab.
func (e *IdeaPage) overviewHistory(v *components.UI, detail *ideas.IdeaDetail) {
	if len(detail.HistoryBrief) == 0 {
		return
	}
	ui.Column(v.C).Gap(tokens.Space1).Children(func() {
		ui.Text(v.C, i18n.T("pages.ideas.sidebar_history")).
			FontSize(tokens.Typography.Caption.Size).
			FontWeight(int(tokens.WeightSemibold)).
			TextColor(mygo.Color(v.T.Palette.TextSecondary))
		for i, entry := range detail.HistoryBrief {
			if i >= 4 {
				break
			}
			ui.Text(v.C, entry.Summary).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(v.T.Palette.TextPrimary)).
				MaxLines(2)
		}
	})
}

// timeRow is one labelled timestamp.
func timeRow(v *components.UI, label, value string) {
	ui.Row(v.C).FillWidth().Gap(tokens.Space2).AlignItems(ui.Center).Children(func() {
		ui.Text(v.C, label).
			FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(v.T.Palette.TextSecondary))
		ui.Spacer(v.C)
		ui.Text(v.C, value).
			FontSize(tokens.Typography.Caption.Size).
			FontFeatures("tnum").
			TextColor(mygo.Color(v.T.Palette.TextPrimary))
	})
}

// ---------------------------------------------------------------------------
// Relations
// ---------------------------------------------------------------------------

// relationsTab shows the four relation groups and the add-relation entry.
func (e *IdeaPage) relationsTab(v *components.UI, a *app.App, projectID string, detail *ideas.IdeaDetail, readOnly bool) {
	relations := detail.Relations
	ui.Column(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
		e.linkOpener(v, readOnly)
		e.relationSection(v, a, projectID, i18n.T("pages.ideas.rel_papers"), relations.Papers, ideas.TargetPaper, readOnly)
		e.relationSection(v, a, projectID, i18n.T("pages.ideas.rel_notes"), relations.Notes, ideas.TargetNote, readOnly)
		e.relationSection(v, a, projectID, i18n.T("pages.ideas.rel_experiments"), relations.Experiments, ideas.TargetExperiment, readOnly)
		v.Card(components.CardOptions{Title: i18n.T("pages.ideas.rel_derivation")}, func() {
			if len(relations.DerivedFrom) == 0 && len(relations.Derived) == 0 {
				ui.Text(v.C, i18n.T("pages.ideas.rel_derivation_empty")).
					FontSize(tokens.Typography.Secondary.Size).
					TextColor(mygo.Color(v.T.Palette.TextMuted))
				return
			}
			ui.Row(v.C).FillWidth().Gap(tokens.RelatedGap).AlignItems(ui.Start).Children(func() {
				ui.Column(v.C).Grow(1).Gap(tokens.Space1).Children(func() {
					ui.Text(v.C, i18n.T("pages.ideas.sidebar_derived_from")).
						FontSize(tokens.Typography.Caption.Size).
						TextColor(mygo.Color(v.T.Palette.TextSecondary))
					if len(relations.DerivedFrom) == 0 {
						ui.Text(v.C, i18n.T("pages.ideas.rel_none")).
							FontSize(tokens.Typography.Caption.Size).
							TextColor(mygo.Color(v.T.Palette.TextMuted))
					}
					for _, ref := range relations.DerivedFrom {
						e.relationRow(v, a, projectID, ref, "", readOnly)
					}
				})
				ui.Column(v.C).Grow(1).Gap(tokens.Space1).Children(func() {
					ui.Text(v.C, i18n.T("pages.ideas.sidebar_derived")).
						FontSize(tokens.Typography.Caption.Size).
						TextColor(mygo.Color(v.T.Palette.TextSecondary))
					if len(relations.Derived) == 0 {
						ui.Text(v.C, i18n.T("pages.ideas.rel_none")).
							FontSize(tokens.Typography.Caption.Size).
							TextColor(mygo.Color(v.T.Palette.TextMuted))
					}
					for _, ref := range relations.Derived {
						e.relationRow(v, a, projectID, ref, "", readOnly)
					}
				})
			})
		})
	})
}

// relationSection is one titled group of related entities.
func (e *IdeaPage) relationSection(v *components.UI, a *app.App, projectID, title string, items []ideas.EntityRef, targetType string, readOnly bool) {
	v.Card(components.CardOptions{Title: title}, func() {
		if len(items) == 0 {
			ui.Text(v.C, i18n.T("pages.ideas.rel_none")).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.TextMuted))
			return
		}
		ui.Column(v.C).Gap(tokens.Space1).Children(func() {
			for _, ref := range items {
				e.relationRow(v, a, projectID, ref, targetType, readOnly)
			}
		})
	})
}

// relationRow is one relation: its title, its jump and its unlink action.
func (e *IdeaPage) relationRow(v *components.UI, a *app.App, projectID string, ref ideas.EntityRef, targetType string, readOnly bool) {
	missing := ref.Deleted || ref.BrokenRef
	ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
		label := ref.Title
		if strings.TrimSpace(label) == "" {
			label = ref.ID
		}
		if missing {
			ui.Text(v.C, label+" · "+i18n.T("pages.ideas.target_deleted")).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.TextMuted)).
				Grow(1).
				SingleLine()
		} else {
			ui.Text(v.C, label).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.AccentDefault)).
				Grow(1).
				SingleLine().
				OnClick(func() { e.jump(a, projectID, ref) })
			v.IconButton(icons.ExternalLink, i18n.T("pages.ideas.btn_open"), components.IconButtonOptions{
				Variant: components.VariantGhost,
				Size:    components.SizeSmall,
				OnClick: func() { e.jump(a, projectID, ref) },
			})
		}
		if targetType != "" && !readOnly {
			v.IconButton(icons.Close, i18n.T("pages.ideas.btn_unlink"), components.IconButtonOptions{
				Variant: components.VariantGhost,
				Size:    components.SizeSmall,
				OnClick: func() { e.unlink(a, projectID, ref, targetType) },
			})
		}
	})
}

// unlink removes one relation.
func (e *IdeaPage) unlink(a *app.App, projectID string, ref ideas.EntityRef, targetType string) {
	api := e.Services.Ideas
	a.RunTask(i18n.T("pages.ideas.task_unlink"), func(ctx context.Context, t *app.Task) error {
		err := api.UnlinkEntity(ctx, projectID, ideas.LinkInput{
			IdeaID:     e.currentID(),
			TargetType: targetType,
			TargetID:   ref.ID,
			Actor:      ports.ActorUser,
		})
		e.mu.Lock()
		if err != nil {
			e.action = ideaErrText(err)
		}
		e.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.ideas.toast_unlink_fail"), ideaErrText(err))
			a.Notify()
			return nil
		}
		e.shared.bump()
		a.Notify()
		return nil
	})
}

// currentID is the Idea the page shows.
func (e *IdeaPage) currentID() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.detail == nil {
		return e.loadedID
	}
	return e.detail.ID
}

// linkOpener is the button that opens the add-relation dialog.
func (e *IdeaPage) linkOpener(v *components.UI, readOnly bool) {
	if readOnly {
		return
	}
	v.Button(i18n.T("pages.ideas.btn_link"), components.ButtonOptions{
		Variant: components.VariantSecondary,
		Icon:    icons.Plus,
		OnClick: func() {
			e.linkOpen = true
			e.linkQuery = ""
			e.linkPick = ""
		},
	})
}

// linkDialog links a paper, note or experiment to the Idea. It holds the
// picker so the tab itself keeps the four relation cards only.
func (e *IdeaPage) linkDialog(v *components.UI, a *app.App, projectID string, detail *ideas.IdeaDetail, readOnly bool) {
	if !e.linkOpen || readOnly || detail == nil {
		return
	}
	e.mu.Lock()
	busy := e.linkBusy
	e.mu.Unlock()

	open := true
	v.Dialog(&open, components.DialogOptions{
		Title: i18n.T("pages.ideas.link_title"),
		Width: tokens.Space12 * 12,
		Actions: func() {
			if v.Button(i18n.T("pages.ideas.link_close"), components.ButtonOptions{}).Clicked() {
				e.closeLinkDialog()
				return
			}
			kind := ideaLinkKinds[e.linkKind%len(ideaLinkKinds)]
			prefix := kind.typ + "/"
			valid := strings.HasPrefix(e.linkPick, prefix)
			if v.Button(i18n.T("pages.ideas.link_confirm"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Loading:  busy,
				Disabled: busy || !valid,
			}).Clicked() && valid {
				e.link(a, projectID, kind.typ, strings.TrimPrefix(e.linkPick, prefix))
			}
		},
	}, func() {
		ui.Segmented(v.C, &e.linkKind,
			i18n.T(ideaLinkKinds[0].label),
			i18n.T(ideaLinkKinds[1].label),
			i18n.T(ideaLinkKinds[2].label),
		).Label(i18n.T("pages.ideas.link_kind"))
		v.SearchInput(&e.linkQuery, components.SearchOptions{
			Label:       i18n.T("pages.ideas.link_search"),
			Placeholder: i18n.T("pages.ideas.link_search_hint"),
		})
		kind := ideaLinkKinds[e.linkKind%len(ideaLinkKinds)]
		e.candidates.ensure(a, e.Services.Ideas, projectID, kind.typ, e.linkQuery)
		loading, items, errText := e.candidates.snapshot()
		if errText != "" {
			ui.Text(v.C, errText).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
			return
		}
		if loading && len(items) == 0 {
			v.Skeleton(components.SkeletonOptions{Lines: 3})
			return
		}
		// A relation already on the page is not offered again: the cards
		// above show it, with its own unlink action.
		linked := linkedTargets(detail.Relations)
		choices := make([]components.RadioOption[string], 0, len(items))
		for _, ref := range items {
			if linked[ref.Type+"/"+ref.ID] {
				continue
			}
			choices = append(choices, components.RadioOption[string]{
				Value: kind.typ + "/" + ref.ID,
				Label: ref.Title,
			})
		}
		if len(choices) == 0 {
			ui.Text(v.C, i18n.T("pages.ideas.link_empty")).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.TextMuted))
			return
		}
		v.RadioGroup(&e.linkPick, choices, components.RadioOptions{})
	})
	if !open && !busy {
		e.closeLinkDialog()
	}
}

// closeLinkDialog puts the picker away and forgets the query, so the next open
// starts from the live candidate list.
func (e *IdeaPage) closeLinkDialog() {
	e.linkOpen = false
	e.linkQuery = ""
	e.linkPick = ""
}

// linkedTargets keys the relation set by target type and id.
func linkedTargets(relations ideas.RelationSet) map[string]bool {
	out := map[string]bool{}
	add := func(typ string, items []ideas.EntityRef) {
		for _, ref := range items {
			out[typ+"/"+ref.ID] = true
		}
	}
	add(ideas.TargetPaper, relations.Papers)
	add(ideas.TargetNote, relations.Notes)
	add(ideas.TargetExperiment, relations.Experiments)
	add("idea", relations.DerivedFrom)
	add("idea", relations.Derived)
	return out
}

// link adds one relation.
func (e *IdeaPage) link(a *app.App, projectID, targetType, targetID string) {
	e.mu.Lock()
	if e.linkBusy {
		e.mu.Unlock()
		return
	}
	e.linkBusy = true
	e.mu.Unlock()

	api := e.Services.Ideas
	ideaID := e.currentID()
	a.RunTask(i18n.T("pages.ideas.task_link"), func(ctx context.Context, t *app.Task) error {
		err := api.LinkEntity(ctx, projectID, ideas.LinkInput{
			IdeaID:     ideaID,
			TargetType: targetType,
			TargetID:   targetID,
			Actor:      ports.ActorUser,
		})
		e.mu.Lock()
		e.linkBusy = false
		if err != nil {
			e.action = ideaErrText(err)
		}
		e.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.ideas.toast_link_fail"), ideaErrText(err))
			a.Notify()
			return nil
		}
		// The dialog closes on success, so the refreshed card above is what
		// the user sees next, as the evidence form does.
		e.closeLinkDialog()
		e.shared.bump()
		a.Toast(app.LevelSuccess, i18n.T("pages.ideas.toast_linked"), "")
		a.Notify()
		return nil
	})
}

// jump opens the page of a related entity. A paper opens in Zotero, because
// the library owns it (SPEC-06 §6.7); a missing target is not jumpable.
func (e *IdeaPage) jump(a *app.App, projectID string, ref ideas.EntityRef) {
	if ref.Deleted || ref.BrokenRef {
		return
	}
	switch ref.Type {
	case ideas.TargetPaper:
		e.openPaper(a, projectID, ref.ID)
	case ideas.TargetNote:
		a.Navigate(ports.Route{Kind: ports.RouteNote, ProjectID: projectID, EntityID: ref.ID})
	case ideas.TargetExperiment:
		a.Navigate(ports.Route{Kind: ports.RouteExperiment, ProjectID: projectID, EntityID: ref.ID})
	case string(ideas.SourceRun):
		a.Navigate(ports.Route{Kind: ports.RouteRun, ProjectID: projectID, EntityID: ref.ID})
	case "idea":
		a.Navigate(ports.Route{Kind: ports.RouteIdea, ProjectID: projectID, EntityID: ref.ID})
	}
}

// openPaper resolves the Zotero URI of a paper and hands it to the frame that
// follows, which opens it in the system.
func (e *IdeaPage) openPaper(a *app.App, projectID, paperID string) {
	e.mu.Lock()
	if e.paperBusy {
		e.mu.Unlock()
		return
	}
	e.paperBusy = true
	e.mu.Unlock()

	api := e.Services.Ideas
	a.RunTask(i18n.T("pages.ideas.task_paper"), func(ctx context.Context, t *app.Task) error {
		url, err := api.PaperURL(ctx, projectID, paperID)
		e.mu.Lock()
		e.paperBusy = false
		if err != nil {
			e.action = ideaErrText(err)
		} else {
			e.paperURL = url
		}
		e.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.ideas.toast_paper_fail"), ideaErrText(err))
		}
		a.Notify()
		return nil
	})
}

// openPendingPaper opens a resolved paper URI in the frame that owns the
// window, so the task never touches the UI context.
func (e *IdeaPage) openPendingPaper(v *components.UI) {
	e.mu.Lock()
	url := e.paperURL
	e.paperURL = ""
	e.mu.Unlock()
	if url != "" && ui.SafeToOpen(url) {
		v.C.OpenURL(url)
	}
}

// openLink follows a link clicked in a rendered Markdown block.
func (e *IdeaPage) openLink(url string) {
	if ui.SafeToOpen(url) && e.frameC != nil {
		e.frameC.OpenURL(url)
	}
}

// ---------------------------------------------------------------------------
// Derivation and deletion
// ---------------------------------------------------------------------------

// deriveDialog collects the title of a derived Idea; the prefill is the source
// title plus 「（衍生）」 (§4.2.3).
func (e *IdeaPage) deriveDialog(v *components.UI, a *app.App, projectID string, detail *ideas.IdeaDetail, readOnly bool) {
	if !e.deriveOpen || detail == nil {
		return
	}
	e.mu.Lock()
	busy, errText := e.deriving, e.derivingErr
	e.mu.Unlock()
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title:       i18n.T("pages.ideas.derive_title"),
		Description: i18n.T("pages.ideas.derive_desc"),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				e.deriveOpen = false
			}
			if v.Button(i18n.T("pages.ideas.derive_confirm"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Loading:  busy,
				Disabled: busy || readOnly,
			}).Clicked() {
				e.derive(a, projectID, detail, readOnly)
			}
		},
	}, func() {
		field := v.Input(&e.deriveTit, components.FieldOptions{
			Label:     i18n.T("pages.ideas.field_title"),
			AutoFocus: true,
		})
		if field.Submitted() {
			e.derive(a, projectID, detail, readOnly)
		}
		ui.Text(v.C, i18n.T("pages.ideas.derive_no_evidence")).
			FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(v.T.Palette.TextMuted))
		if errText != "" {
			ui.Text(v.C, errText).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
	if !open && !busy {
		e.deriveOpen = false
	}
}

// derive creates the derived Idea and opens it.
func (e *IdeaPage) derive(a *app.App, projectID string, detail *ideas.IdeaDetail, readOnly bool) {
	if readOnly {
		return
	}
	title := strings.TrimSpace(e.deriveTit)
	if title == "" {
		a.Toast(app.LevelWarning, i18n.T("pages.ideas.title_required"), "")
		return
	}
	if utf8.RuneCountInString(title) > ideasMaxTitleRunes {
		a.Toast(app.LevelWarning, i18n.T("pages.ideas.title_too_long"), "")
		return
	}
	e.mu.Lock()
	if e.deriving {
		e.mu.Unlock()
		return
	}
	e.deriving = true
	e.derivingErr = ""
	e.mu.Unlock()

	api := e.Services.Ideas
	sourceID := detail.ID
	a.RunTask(i18n.T("pages.ideas.task_derive"), func(ctx context.Context, t *app.Task) error {
		idea, err := api.DeriveIdea(ctx, projectID, ideas.DeriveIdeaInput{
			SourceIdeaID: sourceID,
			Title:        title,
			Actor:        ports.ActorUser,
		})
		e.mu.Lock()
		e.deriving = false
		if err != nil {
			e.derivingErr = ideaErrText(err)
		}
		e.mu.Unlock()
		if err != nil {
			a.Notify()
			return nil
		}
		e.deriveOpen = false
		e.shared.bump()
		a.Toast(app.LevelSuccess, i18n.T("pages.ideas.toast_derived"), ideaTitle(idea.Title))
		a.Navigate(ports.Route{Kind: ports.RouteIdea, ProjectID: projectID, EntityID: idea.ID})
		return nil
	})
}

// deleteDialog confirms the soft deletion of the Idea.
func (e *IdeaPage) deleteDialog(v *components.UI, a *app.App, projectID string, detail *ideas.IdeaDetail, readOnly bool) {
	if !e.deleteOpen || detail == nil {
		return
	}
	e.mu.Lock()
	busy := e.deleting
	e.mu.Unlock()
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title: i18n.T("pages.ideas.delete_title"),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				e.deleteOpen = false
			}
			if v.Button(i18n.T("pages.ideas.delete_confirm"), components.ButtonOptions{
				Variant:  components.VariantDanger,
				Loading:  busy,
				Disabled: busy || readOnly,
			}).Clicked() {
				e.delete(a, projectID, detail, readOnly)
			}
		},
	}, func() {
		ui.Text(v.C, i18n.Tf("pages.ideas.delete_message", map[string]string{
			"title": ideaTitle(detail.Title),
		})).FontSize(tokens.Typography.Body.Size)
	})
	if !open && !busy {
		e.deleteOpen = false
	}
}

// delete soft-deletes the Idea and returns to the list.
func (e *IdeaPage) delete(a *app.App, projectID string, detail *ideas.IdeaDetail, readOnly bool) {
	if readOnly {
		return
	}
	e.mu.Lock()
	if e.deleting {
		e.mu.Unlock()
		return
	}
	e.deleting = true
	e.mu.Unlock()

	api := e.Services.Ideas
	ideaID := detail.ID
	a.RunTask(i18n.T("pages.ideas.task_delete"), func(ctx context.Context, t *app.Task) error {
		err := api.DeleteIdea(ctx, projectID, ideaID, ports.ActorUser)
		e.mu.Lock()
		e.deleting = false
		if err != nil {
			e.action = ideaErrText(err)
		}
		e.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.ideas.toast_delete_fail"), ideaErrText(err))
			a.Notify()
			return nil
		}
		e.deleteOpen = false
		e.shared.bump()
		a.Toast(app.LevelSuccess, i18n.T("pages.ideas.toast_deleted"), "")
		a.Navigate(ports.Route{Kind: app.RouteIdeas})
		return nil
	})
}

// ideaTimestamp renders an absolute timestamp for the sidebar.
func ideaTimestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04")
}

// ideaCandidates caches one candidate query of the link, evidence and AI
// pickers. The frame only reads the cache; the task fills it.
type ideaCandidates struct {
	mu      sync.Mutex
	typ     string
	text    string
	loading bool
	items   []ideas.EntityRef
	errText string
}

// ensure loads the candidates of a type and query once.
func (c *ideaCandidates) ensure(a *app.App, api IdeasAPI, projectID, typ, text string) {
	text = strings.TrimSpace(text)
	c.mu.Lock()
	if c.loading || (c.typ == typ && c.text == text) {
		c.mu.Unlock()
		return
	}
	c.loading = true
	c.typ, c.text = typ, text
	c.mu.Unlock()

	a.RunTask(i18n.T("pages.ideas.task_candidates"), func(ctx context.Context, t *app.Task) error {
		items, err := api.LinkCandidates(ctx, projectID, typ, text)
		c.mu.Lock()
		c.loading = false
		if err != nil {
			c.items = nil
			c.errText = ideaErrText(err)
		} else {
			c.items = items
			c.errText = ""
		}
		c.mu.Unlock()
		a.Notify()
		return nil
	})
}

// snapshot returns the cached candidates.
func (c *ideaCandidates) snapshot() (bool, []ideas.EntityRef, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loading, append([]ideas.EntityRef(nil), c.items...), c.errText
}
