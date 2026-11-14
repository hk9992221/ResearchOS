package pages

// The AI tab of Settings is the SPEC-02 §6.1–§6.3 configuration screen: the
// provider list and its form, the model cache per provider, and the three role
// assignments. It renders only key-free views; a stored API key is never read
// back, shown, or copied.
//
// Every service call runs through App.RunTask. Mutations reload the page data
// after they succeed, so the table, the model cache and the role state always
// describe what the services just accepted.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"researchos/internal/ai/aierr"
	"researchos/internal/ai/model"
	"researchos/internal/ai/provider"
	"researchos/internal/ai/role"
	"researchos/internal/ai/secret"
	"researchos/internal/i18n"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// settingsAI is the session state of the AI tab. Task-written fields are
// guarded by mu; the dialog and role draft fields are written by the frame
// only, and the frame is the sole reader of the drafts.
type settingsAI struct {
	mu       sync.Mutex
	loading  bool
	loaded   bool
	problems []string

	providers []provider.ProviderView
	models    map[string][]model.ModelInfo
	roles     []role.RoleView
	chatReady bool

	actionBusy string // "test:<id>" / "sync:<id>"
	actionErr  string
	actionWarn string
	actionOK   string

	providerSaving bool
	providerDone   bool
	providerErr    string

	deleteBusy bool
	deleteDone bool
	deleteErr  string

	manualSaving bool
	manualDone   bool
	manualErr    string

	modelDeleteBusy bool
	modelDeleteDone bool
	modelDeleteErr  string

	roleBusy       role.Role
	roleBusyAction string
	roleErrRole    role.Role
	roleErrText    string
	roleSaved      role.Role
	reindexNotice  bool

	// Frame-owned dialog and draft state.
	formOpen           bool
	formProvider       aiProviderForm
	providerDeleteOpen bool
	providerDeleteID   string
	providerDeleteName string

	manualOpen          bool
	manualProviderID    string
	manualModelID       string
	manualDisplayName   string
	modelDeleteOpen     bool
	modelDeleteProvider string
	modelDeleteModelID  string
	modelDeleteName     string

	modelProviderLabel string
	modelProviderID    string

	roleChat      settingsRoleForm
	roleEmbedding settingsRoleForm
	roleRerank    settingsRoleForm
}

// aiProviderForm is the write-only provider form. apiKey is cleared when the
// dialog closes; it is never populated from a stored view.
type aiProviderForm struct {
	name          string
	protocolLabel string
	baseURL       string
	apiKey        string
	enabled       bool
	clearKey      bool
	hasKey        bool
	prevDefault   string
	editingID     string
	nameErr       string
	urlErr        string
	err           string
}

// settingsRoleForm is one role's unsaved choice. dirty keeps a reload from
// overwriting what the user is still editing.
type settingsRoleForm struct {
	providerLabel string
	modelLabel    string
	dirty         bool
	err           string
}

// aiView is one frame's read-only copy of the AI state.
type aiView struct {
	loaded, loading bool
	problems        []string
	providers       []provider.ProviderView
	models          map[string][]model.ModelInfo
	roles           []role.RoleView
	chatReady       bool

	actionBusy string
	actionErr  string
	actionWarn string
	actionOK   string

	providerErr string

	reindexNotice bool
	roleBusy      role.Role
	roleBusyAct   string
	roleErrRole   role.Role
	roleErrText   string
}

// aiLoad starts the first read of the tab.
func (s *Settings) aiLoad() {
	api := s.Services.AI
	if api == nil || !api.Available() {
		return
	}
	if s.Services.App == nil {
		return
	}
	s.ai.mu.Lock()
	need := !s.ai.loading && !s.ai.loaded
	s.ai.mu.Unlock()
	if need {
		s.aiReload()
	}
}

// aiReload reads providers, every provider's model cache and the role list off
// the UI thread. A failed read keeps the previous tables and reports why.
func (s *Settings) aiReload() {
	api := s.Services.AI
	a := s.Services.App
	if api == nil || a == nil {
		return
	}
	s.ai.mu.Lock()
	if s.ai.loading {
		s.ai.mu.Unlock()
		return
	}
	s.ai.loading = true
	s.ai.mu.Unlock()

	a.RunTask(i18n.T("pages.settings.ai.task_load"), func(ctx context.Context, t *app.Task) error {
		data, problems, err := fetchAIData(ctx, api)
		s.ai.mu.Lock()
		s.ai.loading = false
		s.ai.loaded = true
		if err != nil {
			s.ai.problems = []string{aiErrText(err)}
		} else {
			models := make(map[string][]model.ModelInfo, len(data.models))
			for _, p := range data.providers {
				if list, ok := data.models[p.ID]; ok {
					models[p.ID] = list
				} else if prev, ok := s.ai.models[p.ID]; ok {
					// A provider whose cache read failed keeps the table it
					// had; the problem banner names the failed read.
					models[p.ID] = prev
				}
			}
			s.ai.providers = data.providers
			s.ai.models = models
			if data.roles != nil {
				s.ai.roles = data.roles
			}
			s.ai.chatReady = data.chatReady
			s.ai.problems = problems
		}
		s.ai.mu.Unlock()
		a.Notify()
		return nil
	})
}

// aiData is one read of the three SPEC-02 views.
type aiData struct {
	providers []provider.ProviderView
	models    map[string][]model.ModelInfo
	roles     []role.RoleView
	chatReady bool
}

// fetchAIData reads the key-free views. A provider whose model cache cannot be
// read does not fail the whole tab; its problem is reported and the others
// still render.
func fetchAIData(ctx context.Context, api AIAPI) (aiData, []string, error) {
	d := aiData{models: map[string][]model.ModelInfo{}, chatReady: api.ChatReady()}
	providers, err := api.ListProviders(ctx)
	if err != nil {
		return d, nil, err
	}
	d.providers = providers
	var problems []string
	for _, p := range providers {
		models, err := api.ListModels(ctx, model.ModelQuery{
			ProviderID:         p.ID,
			IncludeUnavailable: true,
		})
		if err != nil {
			problems = append(problems, i18n.Tf("pages.settings.ai.error.models_load",
				map[string]string{"provider": p.Name, "error": aiErrText(err)}))
			continue
		}
		d.models[p.ID] = models
	}
	roles, err := api.ListRoles(ctx)
	if err != nil {
		problems = append(problems, aiErrText(err))
	} else {
		d.roles = roles
	}
	return d, problems, nil
}

// aiView returns the frame's copy of the state.
func (s *Settings) aiView() aiView {
	s.ai.mu.Lock()
	defer s.ai.mu.Unlock()
	return aiView{
		loaded: s.ai.loaded, loading: s.ai.loading,
		problems:   append([]string(nil), s.ai.problems...),
		providers:  s.ai.providers,
		models:     s.ai.models,
		roles:      s.ai.roles,
		chatReady:  s.ai.chatReady,
		actionBusy: s.ai.actionBusy, actionErr: s.ai.actionErr,
		actionWarn: s.ai.actionWarn, actionOK: s.ai.actionOK,
		providerErr:   s.ai.providerErr,
		reindexNotice: s.ai.reindexNotice,
		roleBusy:      s.ai.roleBusy, roleBusyAct: s.ai.roleBusyAction,
		roleErrRole: s.ai.roleErrRole, roleErrText: s.ai.roleErrText,
	}
}

// aiBusy reports any service operation in flight; every mutating control is
// disabled while it runs.
func (s *Settings) aiBusy() bool {
	s.ai.mu.Lock()
	defer s.ai.mu.Unlock()
	return s.ai.loading || s.ai.actionBusy != "" || s.ai.providerSaving ||
		s.ai.deleteBusy || s.ai.manualSaving || s.ai.modelDeleteBusy ||
		s.ai.roleBusy != ""
}

// aiTab renders the AI configuration page.
func (s *Settings) aiTab(v *components.UI, a *app.App) {
	s.aiTickDialogs()
	api := s.Services.AI
	if api == nil || !api.Available() || a == nil {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.settings.ai.unavailable_title"),
			Message: i18n.T("pages.settings.ai.unavailable_message"),
		})
		return
	}
	view := s.aiView()
	if !view.loaded {
		v.Skeleton(components.SkeletonOptions{Lines: 6})
		return
	}
	for _, problem := range view.problems {
		v.Banner(components.BannerOptions{
			Tone:    components.Warning,
			Title:   i18n.T("pages.settings.ai.load_problem"),
			Message: problem,
		})
	}
	s.aiReadiness(v, view)
	s.aiActionBanners(v, view)
	s.aiProviderCard(v, a, view)
	s.aiModelCard(v, a, view)
	s.aiRoleCard(v, a, view)
	s.aiDialogs(v, a)
}

// aiReadiness states whether chat generation can start. It uses the role view
// (fresh after every assignment) and falls back to the same local probe the
// modules read when the role list could not be loaded.
func (s *Settings) aiReadiness(v *components.UI, view aiView) {
	chat, found := findRoleView(view.roles, role.RoleChatMain)
	ready := view.chatReady
	target := ""
	if found {
		ready = chat.Resolved
		if chat.Assignment != nil && chat.Assignment.ModelID != "" {
			target = chat.Assignment.ModelID
			if chat.ProviderName != "" {
				target = chat.ProviderName + " / " + target
			}
		}
	}
	if !ready {
		v.Banner(components.BannerOptions{
			Tone:    components.Warning,
			Title:   i18n.T("pages.settings.ai.not_ready_title"),
			Message: i18n.T("pages.settings.ai.not_ready_message"),
		})
		return
	}
	if target == "" {
		target = i18n.T("pages.settings.ai.ready_generic")
	}
	v.Banner(components.BannerOptions{
		Tone:    components.Success,
		Title:   i18n.T("pages.settings.ai.ready_title"),
		Message: i18n.Tf("pages.settings.ai.ready_message", map[string]string{"target": target}),
	})
}

// aiActionBanners reports the result of the last row action. Errors stay on
// the page; the provider's test result is also in its table cell.
func (s *Settings) aiActionBanners(v *components.UI, view aiView) {
	if !s.ai.formOpen && view.providerErr != "" {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.settings.ai.action_failed"),
			Message: view.providerErr,
		})
	}
	if view.actionErr != "" {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.settings.ai.action_failed"),
			Message: view.actionErr,
		})
	}
	if view.actionWarn != "" {
		v.Banner(components.BannerOptions{
			Tone:    components.Warning,
			Title:   i18n.T("pages.settings.ai.action_warning"),
			Message: view.actionWarn,
		})
	}
	if view.actionOK != "" {
		v.Banner(components.BannerOptions{
			Tone:    components.Success,
			Title:   i18n.T("pages.settings.ai.action_done"),
			Message: view.actionOK,
		})
	}
}

// aiProviderCard is the provider list and its add button.
func (s *Settings) aiProviderCard(v *components.UI, a *app.App, view aiView) {
	busy := s.aiBusy()
	v.Card(components.CardOptions{
		Title: i18n.T("pages.settings.ai.card_providers"),
		Actions: func() {
			v.Button(i18n.T("pages.settings.ai.btn_add_provider"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Size:     components.SizeSmall,
				Icon:     icons.Plus,
				Disabled: busy,
				OnClick:  func() { s.aiOpenProviderForm(nil) },
			})
		},
	}, func() {
		if len(view.providers) == 0 {
			s.aiEmpty(v, i18n.T("pages.settings.ai.empty_providers"),
				i18n.T("pages.settings.ai.btn_add_provider"), func() { s.aiOpenProviderForm(nil) })
			return
		}
		rows := view.providers
		table := v.Table(components.TableOptions{
			Columns: []components.Column{
				{Title: i18n.T("pages.settings.ai.col_name"), MinWidth: 140},
				{Title: i18n.T("pages.settings.ai.col_protocol"), Width: 190},
				{Title: i18n.T("pages.settings.ai.col_base_url"), MinWidth: 150},
				{Title: i18n.T("pages.settings.ai.col_enabled"), Width: 74},
				{Title: i18n.T("pages.settings.ai.col_key"), Width: 84},
				{Title: i18n.T("pages.settings.ai.col_cache"), Width: 132},
				{Title: i18n.T("pages.settings.ai.col_test"), MinWidth: 170},
				{Title: i18n.T("pages.settings.ai.col_actions"), Width: 290},
			},
			Rows: len(rows),
			Key:  func(i int) any { return rows[i].ID },
			Cell: func(row, col int) {
				p := rows[row]
				switch col {
				case 0:
					ui.Text(v.C, aiProviderName(p)).SingleLine()
				case 1:
					ui.Text(v.C, protocolLabel(p.Protocol)).SingleLine()
				case 2:
					ui.Text(v.C, p.BaseURL).SingleLine()
				case 3:
					tone, text := components.Neutral, i18n.T("pages.settings.ai.disabled")
					if p.Enabled {
						tone, text = components.Success, i18n.T("pages.settings.ai.enabled")
					}
					v.Badge(text, tone)
				case 4:
					text := i18n.T("pages.settings.ai.key_missing")
					if p.HasKey {
						text = i18n.T("pages.settings.ai.key_saved")
					}
					ui.Text(v.C, text)
				case 5:
					if p.ModelsFetchedAt == nil {
						ui.Text(v.C, "—").TextColor(mygo.Color(v.T.Palette.TextMuted))
					} else {
						ui.Text(v.C, p.ModelsFetchedAt.Local().Format("2006-01-02 15:04")).
							FontFeatures("tnum")
					}
					if p.LastTest != nil && strings.TrimSpace(p.ModelsError) != "" {
						v.StatusIndicator(components.Warning, i18n.T("pages.settings.ai.cache_failed")).
							Tooltip(aiStoredErrorText(p.ModelsError))
					}
				case 6:
					s.aiProviderTestCell(v, p)
				case 7:
					s.aiProviderActions(v, a, p, view.actionBusy)
				}
			},
		})
		table.Height(tokens.Space8 + tokens.Space11*float32(len(rows)))
	})
}

// aiProviderTestCell shows the last test result, or the last model-cache
// failure while no test has run. The message is the sanitized one the service
// persisted.
func (s *Settings) aiProviderTestCell(v *components.UI, p provider.ProviderView) {
	if p.LastTest != nil {
		if p.LastTest.OK {
			v.StatusIndicator(components.Success, i18n.Tf("pages.settings.ai.test_ok", map[string]string{
				"ms": strconv.FormatInt(p.LastTest.LatencyMS, 10),
				"n":  strconv.Itoa(p.LastTest.ModelCount),
			}))
			return
		}
		v.StatusIndicator(components.Danger, aiErrorKindText(p.LastTest.ErrorKind))
		if msg := strings.TrimSpace(p.LastTest.Message); msg != "" {
			ui.Text(v.C, aierr.Truncate(aierr.Redact(msg))).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(v.T.Palette.TextMuted)).
				MaxLines(2)
		}
		return
	}
	if msg := strings.TrimSpace(p.ModelsError); msg != "" {
		v.StatusIndicator(components.Warning, i18n.T("pages.settings.ai.cache_failed"))
		ui.Text(v.C, aiStoredErrorText(msg)).
			FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(v.T.Palette.TextMuted)).
			MaxLines(2)
		return
	}
	ui.Text(v.C, "—").TextColor(mygo.Color(v.T.Palette.TextMuted))
}

// aiProviderActions is the per-row button group.
func (s *Settings) aiProviderActions(v *components.UI, a *app.App, p provider.ProviderView, actionBusy string) {
	busy := s.aiBusy()
	ui.Row(v.C).Gap(tokens.Space1).AlignItems(ui.Center).Children(func() {
		v.Button(i18n.T("pages.settings.ai.btn_test"), components.ButtonOptions{
			Variant: components.VariantGhost, Size: components.SizeSmall,
			Disabled: busy, Loading: actionBusy == "test:"+p.ID,
			OnClick: func() { s.aiTestProvider(a, p.ID) },
		})
		v.Button(i18n.T("pages.settings.ai.btn_sync"), components.ButtonOptions{
			Variant: components.VariantGhost, Size: components.SizeSmall,
			Disabled: busy, Loading: actionBusy == "sync:"+p.ID,
			OnClick: func() { s.aiSyncModels(a, p.ID) },
		})
		v.Button(i18n.T("pages.settings.ai.btn_edit"), components.ButtonOptions{
			Variant: components.VariantGhost, Size: components.SizeSmall,
			Disabled: busy,
			OnClick:  func() { s.aiOpenProviderForm(&p) },
		})
		v.Button(i18n.T("pages.settings.ai.btn_delete"), components.ButtonOptions{
			Variant: components.VariantGhost, Size: components.SizeSmall,
			Disabled: busy,
			OnClick:  func() { s.aiAskDeleteProvider(p) },
		})
	})
}

// aiModelCard is the cached model table of one provider.
func (s *Settings) aiModelCard(v *components.UI, a *app.App, view aiView) {
	busy := s.aiBusy()
	v.Card(components.CardOptions{Title: i18n.T("pages.settings.ai.card_models")}, func() {
		if len(view.providers) == 0 {
			s.aiEmpty(v, i18n.T("pages.settings.ai.empty_no_provider"), "", nil)
			return
		}
		labels, ids := aiProviderOptions(view.providers)
		index := indexOf(ids, s.ai.modelProviderID)
		if index < 0 {
			index = 0
			s.ai.modelProviderID = ids[index]
			s.ai.modelProviderLabel = labels[index]
		} else if indexOf(labels, s.ai.modelProviderLabel) < 0 {
			// A provider rename changed the label; keep the selection by id.
			s.ai.modelProviderLabel = labels[index]
		}
		ui.Row(v.C).FillWidth().Gap(tokens.RelatedGap).AlignItems(ui.End).Wrap().Children(func() {
			sel := v.Select(&s.ai.modelProviderLabel, components.SelectOptions{
				Label:    i18n.T("pages.settings.ai.field_provider"),
				Options:  labels,
				Width:    340,
				Disabled: busy,
			})
			if sel.Changed() {
				if i := indexOf(labels, s.ai.modelProviderLabel); i >= 0 {
					s.ai.modelProviderID = ids[i]
				}
			}
			v.Button(i18n.T("pages.settings.ai.btn_sync"), components.ButtonOptions{
				Variant: components.VariantSecondary, Size: components.SizeSmall,
				Icon:     icons.Refresh,
				Disabled: busy,
				Loading:  view.actionBusy == "sync:"+s.ai.modelProviderID,
				OnClick:  func() { s.aiSyncModels(a, s.ai.modelProviderID) },
			})
			v.Button(i18n.T("pages.settings.ai.btn_add_manual"), components.ButtonOptions{
				Variant: components.VariantSecondary, Size: components.SizeSmall,
				Icon:     icons.Plus,
				Disabled: busy,
				OnClick:  func() { s.aiOpenManualModel(s.ai.modelProviderID) },
			})
		})
		models := view.models[s.ai.modelProviderID]
		if len(models) == 0 {
			s.aiEmpty(v, i18n.T("pages.settings.ai.empty_models"), "", nil)
			return
		}
		rows := models
		table := v.Table(components.TableOptions{
			Columns: []components.Column{
				{Title: i18n.T("pages.settings.ai.col_model_id"), MinWidth: 200},
				{Title: i18n.T("pages.settings.ai.col_model_name"), MinWidth: 160},
				{Title: i18n.T("pages.settings.ai.col_capability"), MinWidth: 170},
				{Title: i18n.T("pages.settings.ai.col_source"), Width: 90},
				{Title: i18n.T("pages.settings.ai.col_available"), Width: 100},
				{Title: i18n.T("pages.settings.ai.col_actions"), Width: 90},
			},
			Rows: len(rows),
			Key:  func(i int) any { return rows[i].ModelID },
			Cell: func(row, col int) {
				m := rows[row]
				switch col {
				case 0:
					ui.Text(v.C, m.ModelID).SingleLine()
				case 1:
					name := m.DisplayName
					if strings.TrimSpace(name) == "" {
						name = "—"
					}
					ui.Text(v.C, name).SingleLine()
				case 2:
					ui.Row(v.C).Gap(tokens.Space1).Children(func() {
						for _, c := range m.Capabilities {
							v.Badge(capabilityLabel(c), components.Neutral)
						}
					})
				case 3:
					text := i18n.T("pages.settings.ai.source_listed")
					if m.Source == model.SourceManual {
						text = i18n.T("pages.settings.ai.source_manual")
					}
					ui.Text(v.C, text)
				case 4:
					if m.Available {
						v.StatusIndicator(components.Success, i18n.T("pages.settings.ai.available"))
					} else {
						v.StatusIndicator(components.Warning, i18n.T("pages.settings.ai.unavailable"))
					}
				case 5:
					if m.Source == model.SourceManual {
						v.Button(i18n.T("pages.settings.ai.btn_delete"), components.ButtonOptions{
							Variant: components.VariantGhost, Size: components.SizeSmall,
							Disabled: busy,
							OnClick:  func() { s.aiAskDeleteModel(s.ai.modelProviderID, m) },
						})
					}
				}
			},
		})
		table.Height(tokens.Space8 + tokens.Space11*float32(len(rows)))
	})
}

// aiRoleCard renders the three role assignments.
func (s *Settings) aiRoleCard(v *components.UI, a *app.App, view aiView) {
	s.aiSyncRoleDrafts(view)
	v.Card(components.CardOptions{Title: i18n.T("pages.settings.ai.card_roles")}, func() {
		if view.reindexNotice {
			v.Banner(components.BannerOptions{
				Tone:        components.Warning,
				Title:       i18n.T("pages.settings.ai.reindex_title"),
				Message:     i18n.T("pages.settings.ai.reindex_message"),
				ActionLabel: i18n.T("pages.settings.ai.reindex_dismiss"),
				OnAction:    func() { s.aiDismissReindex() },
			})
		}
		ui.Column(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
			for _, r := range role.AllRoles() {
				s.aiRoleRow(v, a, view, r)
			}
		})
	})
}

// aiRoleRow is one role: its state, the provider/model selects and save/clear.
func (s *Settings) aiRoleRow(v *components.UI, a *app.App, view aiView, r role.Role) {
	current, found := findRoleView(view.roles, r)
	form := s.aiRoleForm(r)
	busy := s.aiBusy()
	p := v.T.Palette

	ui.Column(v.C).FillWidth().Gap(tokens.Space2).Children(func() {
		ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
			ui.Text(v.C, roleLabel(r)).
				FontSize(tokens.Typography.Body.Size).
				FontWeight(int(tokens.WeightSemibold))
			if found {
				v.Badge(capabilityLabel(current.RequiredCapability), components.Neutral)
			}
		})
		s.aiRoleStatus(v, current, found)
		if r == role.RoleRerank {
			ui.Text(v.C, i18n.T("pages.settings.ai.rerank_pending")).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(p.TextSecondary))
			ui.Row(v.C).Gap(tokens.ComponentGap).Children(func() {
				v.Button(i18n.T("pages.settings.ai.btn_save_role"), components.ButtonOptions{
					Variant: components.VariantSecondary, Size: components.SizeSmall, Disabled: true,
				})
				clearDisabled := busy || !found || current.Assignment == nil
				v.Button(i18n.T("pages.settings.ai.btn_clear_role"), components.ButtonOptions{
					Variant: components.VariantGhost, Size: components.SizeSmall,
					Disabled: clearDisabled,
					Loading:  view.roleBusy == r && view.roleBusyAct == "clear",
					OnClick:  func() { s.aiClearRole(a, r) },
				})
			})
			return
		}

		required := provider.CapabilityChat
		if found {
			required = current.RequiredCapability
		}
		labels, ids := aiProviderOptions(filterProviders(view.providers, required))
		if form.providerLabel == "" {
			form.providerLabel = aiPlaceholderProvider()
		}
		if form.modelLabel == "" {
			form.modelLabel = aiPlaceholderModel()
		}
		ui.Row(v.C).FillWidth().Gap(tokens.RelatedGap).AlignItems(ui.End).Wrap().Children(func() {
			providerSel := v.Select(&form.providerLabel, components.SelectOptions{
				Label:    i18n.Tf("pages.settings.ai.role_provider_label", map[string]string{"role": roleLabel(r)}),
				Options:  append([]string{aiPlaceholderProvider()}, labels...),
				Compact:  true,
				Width:    260,
				Disabled: busy,
			})
			if providerSel.Changed() {
				form.dirty = true
				form.err = ""
				form.modelLabel = aiPlaceholderModel()
			}
			providerID := ""
			if i := indexOf(labels, form.providerLabel); i >= 0 {
				providerID = ids[i]
			}
			var modelLabels []string
			if providerID != "" {
				modelLabels = aiModelOptions(view.models[providerID], required)
			}
			modelSel := v.Select(&form.modelLabel, components.SelectOptions{
				Label:    i18n.Tf("pages.settings.ai.role_model_label", map[string]string{"role": roleLabel(r)}),
				Options:  append([]string{aiPlaceholderModel()}, modelLabels...),
				Compact:  true,
				Width:    280,
				Disabled: busy || providerID == "",
			})
			if modelSel.Changed() {
				form.dirty = true
				form.err = ""
			}
			v.Button(i18n.T("pages.settings.ai.btn_save_role"), components.ButtonOptions{
				Variant:  components.VariantSecondary,
				Size:     components.SizeSmall,
				Disabled: busy || providerID == "" || form.modelLabel == aiPlaceholderModel(),
				Loading:  view.roleBusy == r && view.roleBusyAct == "save",
				OnClick:  func() { s.aiSaveRole(a, r, view) },
			})
			clearDisabled := busy || !found || current.Assignment == nil
			v.Button(i18n.T("pages.settings.ai.btn_clear_role"), components.ButtonOptions{
				Variant: components.VariantGhost, Size: components.SizeSmall,
				Disabled: clearDisabled,
				Loading:  view.roleBusy == r && view.roleBusyAct == "clear",
				OnClick:  func() { s.aiClearRole(a, r) },
			})
		})
		if form.err != "" {
			ui.Text(v.C, form.err).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(p.StatusDanger))
		}
		if view.roleErrRole == r && view.roleErrText != "" {
			ui.Text(v.C, view.roleErrText).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(p.StatusDanger))
		}
	})
}

// aiRoleStatus shows the resolved assignment or the localized problem.
func (s *Settings) aiRoleStatus(v *components.UI, current role.RoleView, found bool) {
	p := v.T.Palette
	if !found || current.Assignment == nil {
		v.StatusIndicator(components.Neutral, i18n.T("pages.settings.ai.role_unassigned"))
		return
	}
	if current.Resolved {
		target := strings.TrimSpace(current.ProviderName)
		if current.Assignment.ModelID != "" {
			if target != "" {
				target += " / "
			}
			target += current.Assignment.ModelID
		}
		if target == "" {
			target = i18n.T("pages.settings.ai.role_unassigned")
		}
		v.StatusIndicator(components.Success, i18n.Tf("pages.settings.ai.role_current",
			map[string]string{"target": target}))
		return
	}
	v.StatusIndicator(components.Warning, roleProblemText(current.Problem))
	ui.Text(v.C, current.Assignment.ModelID).
		FontSize(tokens.Typography.Caption.Size).
		TextColor(mygo.Color(p.TextMuted)).
		SingleLine()
}

// aiSyncRoleDrafts fills untouched role drafts from the server view. A dirty
// draft survives a reload; a saved one is marked clean by aiTickDialogs.
func (s *Settings) aiSyncRoleDrafts(view aiView) {
	if view.loading {
		return
	}
	for _, r := range role.AllRoles() {
		form := s.aiRoleForm(r)
		if form == nil || form.dirty {
			continue
		}
		form.providerLabel = ""
		form.modelLabel = ""
		form.err = ""
		current, found := findRoleView(view.roles, r)
		if !found || current.Assignment == nil {
			continue
		}
		required := current.RequiredCapability
		labels, ids := aiProviderOptions(filterProviders(view.providers, required))
		index := indexOf(ids, current.Assignment.ProviderID)
		if index < 0 {
			continue
		}
		form.providerLabel = labels[index]
		if modelInList(view.models[current.Assignment.ProviderID], current.Assignment.ModelID, required) {
			form.modelLabel = current.Assignment.ModelID
		}
	}
}

// aiRoleForm returns the frame-owned draft of one role.
func (s *Settings) aiRoleForm(r role.Role) *settingsRoleForm {
	switch r {
	case role.RoleChatMain:
		return &s.ai.roleChat
	case role.RoleEmbedding:
		return &s.ai.roleEmbedding
	case role.RoleRerank:
		return &s.ai.roleRerank
	}
	return nil
}

// aiClearTransient drops write-only form state when the tab is left, so a
// typed key does not stay in memory behind another settings page.
func (s *Settings) aiClearTransient() {
	s.ai.formOpen = false
	s.ai.formProvider = aiProviderForm{}
	s.ai.roleChat, s.ai.roleEmbedding, s.ai.roleRerank = settingsRoleForm{}, settingsRoleForm{}, settingsRoleForm{}
}

// aiTickDialogs applies task completion flags and clears a write-only key
// whenever its dialog is closed.
func (s *Settings) aiTickDialogs() {
	s.ai.mu.Lock()
	providerDone, deleteDone := s.ai.providerDone, s.ai.deleteDone
	manualDone, modelDeleteDone := s.ai.manualDone, s.ai.modelDeleteDone
	roleSaved := s.ai.roleSaved
	s.ai.providerDone, s.ai.deleteDone = false, false
	s.ai.manualDone, s.ai.modelDeleteDone = false, false
	s.ai.roleSaved = ""
	s.ai.mu.Unlock()

	if providerDone {
		s.ai.formOpen = false
		s.ai.formProvider = aiProviderForm{}
		s.ai.mu.Lock()
		s.ai.providerErr = ""
		s.ai.mu.Unlock()
	}
	if deleteDone {
		s.ai.providerDeleteOpen = false
		s.ai.mu.Lock()
		s.ai.deleteErr = ""
		s.ai.mu.Unlock()
	}
	if manualDone {
		s.ai.manualOpen = false
		s.ai.manualModelID, s.ai.manualDisplayName = "", ""
		s.ai.mu.Lock()
		s.ai.manualErr = ""
		s.ai.mu.Unlock()
	}
	if modelDeleteDone {
		s.ai.modelDeleteOpen = false
		s.ai.mu.Lock()
		s.ai.modelDeleteErr = ""
		s.ai.mu.Unlock()
	}
	if roleSaved != "" {
		if form := s.aiRoleForm(roleSaved); form != nil {
			form.dirty = false
			form.err = ""
		}
	}
	if !s.ai.formOpen {
		s.ai.formProvider.apiKey = ""
	}
}

// aiEmpty is the compact empty state inside a card.
func (s *Settings) aiEmpty(v *components.UI, text, actionLabel string, action func()) {
	ui.Column(v.C).FillWidth().Gap(tokens.Space2).AlignItems(ui.Start).Children(func() {
		ui.Text(v.C, text).
			FontSize(tokens.Typography.Secondary.Size).
			TextColor(mygo.Color(v.T.Palette.TextSecondary))
		if actionLabel != "" && action != nil {
			v.Button(actionLabel, components.ButtonOptions{
				Variant: components.VariantSecondary, Size: components.SizeSmall, Icon: icons.Plus,
				OnClick: action,
			})
		}
	})
}

// aiOpenProviderForm opens the add or edit dialog. The key field always starts
// empty: a stored key is never read back.
func (s *Settings) aiOpenProviderForm(edit *provider.ProviderView) {
	if s.aiBusy() {
		return
	}
	proto := provider.ProtocolOpenAIChat
	f := aiProviderForm{
		enabled:       true,
		protocolLabel: protocolLabel(proto),
		baseURL:       provider.DefaultBaseURL(proto),
		prevDefault:   provider.DefaultBaseURL(proto),
	}
	if edit != nil {
		f = aiProviderForm{
			name:          edit.Name,
			protocolLabel: protocolLabel(edit.Protocol),
			baseURL:       edit.BaseURL,
			enabled:       edit.Enabled,
			hasKey:        edit.HasKey,
			prevDefault:   provider.DefaultBaseURL(edit.Protocol),
			editingID:     edit.ID,
		}
	}
	s.ai.mu.Lock()
	s.ai.providerErr = ""
	s.ai.providerDone = false
	s.ai.mu.Unlock()
	s.ai.formProvider = f
	s.ai.formOpen = true
}

// aiSaveProvider creates or updates one provider.
func (s *Settings) aiSaveProvider(a *app.App) {
	api := s.Services.AI
	if api == nil || a == nil || s.aiBusy() {
		return
	}
	f := s.ai.formProvider
	name := strings.TrimSpace(f.name)
	proto, ok := protocolOfLabel(f.protocolLabel)
	f.nameErr, f.urlErr, f.err = "", "", ""
	if name == "" {
		f.nameErr = i18n.T("pages.settings.ai.error.name")
		s.ai.formProvider = f
		return
	}
	if !ok {
		f.err = i18n.T("pages.settings.ai.error.protocol")
		s.ai.formProvider = f
		return
	}
	baseURL, err := provider.NormalizeBaseURL(f.baseURL)
	if err != nil {
		f.urlErr = i18n.T("pages.settings.ai.error.base_url")
		s.ai.formProvider = f
		return
	}
	key := strings.TrimSpace(f.apiKey)
	if f.clearKey && key != "" {
		f.err = i18n.T("pages.settings.ai.error.key_and_clear")
		s.ai.formProvider = f
		return
	}
	s.ai.formProvider = f

	editingID := f.editingID
	enabled := f.enabled
	clearKey := f.clearKey
	hasKey := f.hasKey
	s.ai.mu.Lock()
	s.ai.providerSaving = true
	s.ai.providerDone = false
	s.ai.providerErr = ""
	s.ai.mu.Unlock()

	a.RunTask(i18n.T("pages.settings.ai.task_save"), func(ctx context.Context, t *app.Task) error {
		var err error
		switch {
		case editingID == "":
			_, err = api.CreateProvider(ctx, provider.CreateProviderInput{
				Name: name, Protocol: proto, BaseURL: baseURL, APIKey: key, Enabled: enabled,
			})
		default:
			in := provider.UpdateProviderInput{
				Name: &name, Protocol: &proto, BaseURL: &baseURL, Enabled: &enabled,
				ClearKey: clearKey && hasKey,
			}
			if key != "" {
				in.APIKey = &key
			}
			_, err = api.UpdateProvider(ctx, editingID, in)
		}
		s.ai.mu.Lock()
		s.ai.providerSaving = false
		if err != nil {
			s.ai.providerErr = aiErrText(err)
		} else {
			s.ai.providerDone = true
		}
		s.ai.mu.Unlock()
		a.Notify()
		if err == nil {
			s.aiReload()
		}
		return nil
	})
}

// aiTestProvider runs the connection test and reloads the list so the stored
// last test result is what the table shows.
func (s *Settings) aiTestProvider(a *app.App, id string) {
	api := s.Services.AI
	if api == nil || a == nil || s.aiBusy() {
		return
	}
	s.aiStartAction("test:" + id)
	a.RunTask(i18n.T("pages.settings.ai.task_test"), func(ctx context.Context, t *app.Task) error {
		res, err := api.TestProvider(ctx, id)
		s.ai.mu.Lock()
		s.ai.actionBusy = ""
		switch {
		case err != nil:
			s.ai.actionErr = aiErrText(err)
		case res.OK:
			s.ai.actionOK = aiTestText(res)
		default:
			s.ai.actionErr = aiTestText(res)
		}
		s.ai.mu.Unlock()
		a.Notify()
		if err == nil {
			s.aiReload()
		}
		return nil
	})
}

// aiSyncModels refreshes one provider's upstream model cache. On failure the
// service keeps the old cache; the page reload shows the persisted error.
func (s *Settings) aiSyncModels(a *app.App, id string) {
	api := s.Services.AI
	if api == nil || a == nil || id == "" || s.aiBusy() {
		return
	}
	s.aiStartAction("sync:" + id)
	a.RunTask(i18n.T("pages.settings.ai.task_sync"), func(ctx context.Context, t *app.Task) error {
		res, err := api.SyncModels(ctx, id)
		s.ai.mu.Lock()
		s.ai.actionBusy = ""
		switch {
		case err != nil:
			s.ai.actionErr = aiErrText(err)
		case res.Warning != "":
			s.ai.actionWarn = i18n.Tf("pages.settings.ai.sync_warning", map[string]string{
				"total": strconv.Itoa(res.Total),
			})
		default:
			s.ai.actionOK = i18n.Tf("pages.settings.ai.sync_ok", map[string]string{
				"added":       strconv.Itoa(res.Added),
				"unavailable": strconv.Itoa(res.MarkedUnavailable),
				"total":       strconv.Itoa(res.Total),
			})
		}
		s.ai.mu.Unlock()
		a.Notify()
		s.aiReload()
		return nil
	})
}

// aiStartAction clears the last inline result and marks one row action busy.
func (s *Settings) aiStartAction(busy string) {
	s.ai.mu.Lock()
	s.ai.actionBusy = busy
	s.ai.actionErr, s.ai.actionWarn, s.ai.actionOK = "", "", ""
	s.ai.providerErr = ""
	s.ai.mu.Unlock()
}

// aiAskDeleteProvider opens the confirmation dialog.
func (s *Settings) aiAskDeleteProvider(p provider.ProviderView) {
	s.ai.mu.Lock()
	s.ai.deleteErr = ""
	s.ai.deleteDone = false
	s.ai.mu.Unlock()
	s.ai.providerDeleteID = p.ID
	s.ai.providerDeleteName = aiProviderName(p)
	s.ai.providerDeleteOpen = true
}

// aiDeleteProvider deletes after the in-page confirmation.
func (s *Settings) aiDeleteProvider(a *app.App) {
	api := s.Services.AI
	if api == nil || a == nil || s.aiBusy() {
		return
	}
	id := s.ai.providerDeleteID
	if id == "" {
		return
	}
	s.ai.mu.Lock()
	s.ai.deleteBusy = true
	s.ai.deleteErr = ""
	s.ai.deleteDone = false
	s.ai.mu.Unlock()
	a.RunTask(i18n.T("pages.settings.ai.task_delete_provider"), func(ctx context.Context, t *app.Task) error {
		err := api.DeleteProvider(ctx, id)
		s.ai.mu.Lock()
		s.ai.deleteBusy = false
		if err != nil {
			s.ai.deleteErr = aiErrText(err)
		} else {
			s.ai.deleteDone = true
		}
		s.ai.mu.Unlock()
		a.Notify()
		if err == nil {
			s.aiReload()
		}
		return nil
	})
}

// aiOpenManualModel opens the manual-model dialog for one provider.
func (s *Settings) aiOpenManualModel(providerID string) {
	if providerID == "" || s.aiBusy() {
		return
	}
	s.ai.manualProviderID = providerID
	s.ai.manualModelID, s.ai.manualDisplayName = "", ""
	s.ai.mu.Lock()
	s.ai.manualErr = ""
	s.ai.manualDone = false
	s.ai.mu.Unlock()
	s.ai.manualOpen = true
}

// aiAddManualModel stores one hand-entered model.
func (s *Settings) aiAddManualModel(a *app.App) {
	api := s.Services.AI
	if api == nil || a == nil || s.aiBusy() {
		return
	}
	modelID := strings.TrimSpace(s.ai.manualModelID)
	display := strings.TrimSpace(s.ai.manualDisplayName)
	if modelID == "" {
		s.ai.mu.Lock()
		s.ai.manualErr = i18n.T("pages.settings.ai.error.model_empty")
		s.ai.mu.Unlock()
		return
	}
	providerID := s.ai.manualProviderID
	s.ai.mu.Lock()
	s.ai.manualSaving = true
	s.ai.manualErr = ""
	s.ai.manualDone = false
	s.ai.mu.Unlock()
	a.RunTask(i18n.T("pages.settings.ai.task_add_model"), func(ctx context.Context, t *app.Task) error {
		err := api.AddManualModel(ctx, providerID, modelID, display)
		s.ai.mu.Lock()
		s.ai.manualSaving = false
		if err != nil {
			s.ai.manualErr = aiErrText(err)
		} else {
			s.ai.manualDone = true
		}
		s.ai.mu.Unlock()
		a.Notify()
		if err == nil {
			s.aiReload()
		}
		return nil
	})
}

// aiAskDeleteModel opens the manual-model deletion confirmation.
func (s *Settings) aiAskDeleteModel(providerID string, m model.ModelInfo) {
	name := m.DisplayName
	if strings.TrimSpace(name) == "" {
		name = m.ModelID
	}
	s.ai.modelDeleteProvider = providerID
	s.ai.modelDeleteModelID = m.ModelID
	s.ai.modelDeleteName = name
	s.ai.mu.Lock()
	s.ai.modelDeleteErr = ""
	s.ai.modelDeleteDone = false
	s.ai.mu.Unlock()
	s.ai.modelDeleteOpen = true
}

// aiDeleteModel removes one manual model.
func (s *Settings) aiDeleteModel(a *app.App) {
	api := s.Services.AI
	if api == nil || a == nil || s.aiBusy() {
		return
	}
	providerID, modelID := s.ai.modelDeleteProvider, s.ai.modelDeleteModelID
	if providerID == "" || modelID == "" {
		return
	}
	s.ai.mu.Lock()
	s.ai.modelDeleteBusy = true
	s.ai.modelDeleteErr = ""
	s.ai.modelDeleteDone = false
	s.ai.mu.Unlock()
	a.RunTask(i18n.T("pages.settings.ai.task_delete_model"), func(ctx context.Context, t *app.Task) error {
		err := api.RemoveManualModel(ctx, providerID, modelID)
		s.ai.mu.Lock()
		s.ai.modelDeleteBusy = false
		if err != nil {
			s.ai.modelDeleteErr = aiErrText(err)
		} else {
			s.ai.modelDeleteDone = true
		}
		s.ai.mu.Unlock()
		a.Notify()
		if err == nil {
			s.aiReload()
		}
		return nil
	})
}

// aiSaveRole assigns the selected provider/model to one role.
func (s *Settings) aiSaveRole(a *app.App, r role.Role, view aiView) {
	api := s.Services.AI
	if api == nil || a == nil || s.aiBusy() {
		return
	}
	current, found := findRoleView(view.roles, r)
	required := provider.CapabilityChat
	if found {
		required = current.RequiredCapability
	}
	labels, ids := aiProviderOptions(filterProviders(view.providers, required))
	index := indexOf(labels, s.aiRoleForm(r).providerLabel)
	if index < 0 {
		s.aiRoleForm(r).err = i18n.T("pages.settings.ai.error.role_provider")
		s.aiRoleForm(r).dirty = true
		return
	}
	providerID := ids[index]
	modelID := s.aiRoleForm(r).modelLabel
	if modelID == "" || modelID == aiPlaceholderModel() ||
		!modelInList(view.models[providerID], modelID, required) {
		s.aiRoleForm(r).err = i18n.T("pages.settings.ai.error.role_model")
		s.aiRoleForm(r).dirty = true
		return
	}
	s.aiRoleForm(r).err = ""
	s.ai.mu.Lock()
	s.ai.roleBusy = r
	s.ai.roleBusyAction = "save"
	s.ai.roleErrRole = ""
	s.ai.roleErrText = ""
	s.ai.mu.Unlock()
	a.RunTask(i18n.T("pages.settings.ai.task_role"), func(ctx context.Context, t *app.Task) error {
		res, err := api.AssignRole(ctx, r, providerID, modelID)
		s.ai.mu.Lock()
		s.ai.roleBusy = ""
		s.ai.roleBusyAction = ""
		if err != nil {
			s.ai.roleErrRole = r
			s.ai.roleErrText = aiErrText(err)
		} else {
			s.ai.roleSaved = r
			if res.ReindexRequired {
				s.ai.reindexNotice = true
			}
		}
		s.ai.mu.Unlock()
		a.Notify()
		if err == nil {
			s.aiReload()
		}
		return nil
	})
}

// aiClearRole removes one assignment.
func (s *Settings) aiClearRole(a *app.App, r role.Role) {
	api := s.Services.AI
	if api == nil || a == nil || s.aiBusy() {
		return
	}
	s.ai.mu.Lock()
	s.ai.roleBusy = r
	s.ai.roleBusyAction = "clear"
	s.ai.roleErrRole = ""
	s.ai.roleErrText = ""
	s.ai.mu.Unlock()
	a.RunTask(i18n.T("pages.settings.ai.task_role"), func(ctx context.Context, t *app.Task) error {
		err := api.ClearRole(ctx, r)
		s.ai.mu.Lock()
		s.ai.roleBusy = ""
		s.ai.roleBusyAction = ""
		if err != nil {
			s.ai.roleErrRole = r
			s.ai.roleErrText = aiErrText(err)
		} else {
			s.ai.roleSaved = r
		}
		s.ai.mu.Unlock()
		a.Notify()
		if err == nil {
			s.aiReload()
		}
		return nil
	})
}

// aiDismissReindex hides the embedding rebuild notice.
func (s *Settings) aiDismissReindex() {
	s.ai.mu.Lock()
	s.ai.reindexNotice = false
	s.ai.mu.Unlock()
}

// aiDialogs renders every dialog of the tab.
func (s *Settings) aiDialogs(v *components.UI, a *app.App) {
	s.aiProviderDialog(v, a)
	s.aiProviderDeleteDialog(v, a)
	s.aiManualDialog(v, a)
	s.aiModelDeleteDialog(v, a)
}

// aiProviderDialog is the write-only provider form.
func (s *Settings) aiProviderDialog(v *components.UI, a *app.App) {
	if !s.ai.formOpen {
		return
	}
	f := &s.ai.formProvider
	s.ai.mu.Lock()
	saving, saveErr := s.ai.providerSaving, s.ai.providerErr
	s.ai.mu.Unlock()
	title := i18n.T("pages.settings.ai.dialog_add_title")
	if f.editingID != "" {
		title = i18n.T("pages.settings.ai.dialog_edit_title")
	}
	desc := i18n.T("pages.settings.ai.dialog_desc")
	v.Dialog(&s.ai.formOpen, components.DialogOptions{
		Title:       title,
		Description: desc,
		Width:       tokens.Space12 * 12,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				s.ai.formOpen = false
			}
			if v.Button(i18n.T("pages.settings.ai.btn_save"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Loading:  saving,
				Disabled: saving,
			}).Clicked() {
				s.aiSaveProvider(a)
			}
		},
	}, func() {
		ui.Column(v.C).FillWidth().Gap(tokens.RelatedGap).Children(func() {
			nameIn := v.Input(&f.name, components.FieldOptions{
				Label:       i18n.T("pages.settings.ai.field_name"),
				Placeholder: i18n.T("pages.settings.ai.field_name_hint"),
				Error:       f.nameErr,
				AutoFocus:   true,
				Disabled:    saving,
			})
			if nameIn.Changed() {
				f.err, f.nameErr = "", ""
			}
			sel := v.Select(&f.protocolLabel, components.SelectOptions{
				Label:    i18n.T("pages.settings.ai.field_protocol"),
				Options:  aiProtocolLabels(),
				Disabled: saving,
			})
			if sel.Changed() {
				f.err = ""
				proto, ok := protocolOfLabel(f.protocolLabel)
				if ok {
					if strings.TrimSpace(f.baseURL) == "" || f.baseURL == f.prevDefault {
						f.baseURL = provider.DefaultBaseURL(proto)
					}
					f.prevDefault = provider.DefaultBaseURL(proto)
				}
			}
			baseIn := v.Input(&f.baseURL, components.FieldOptions{
				Label:       i18n.T("pages.settings.ai.field_base_url"),
				Placeholder: i18n.T("pages.settings.ai.field_base_url_hint"),
				Error:       f.urlErr,
				Disabled:    saving,
			})
			if baseIn.Changed() {
				f.err, f.urlErr = "", ""
			}
			placeholder := i18n.T("pages.settings.ai.api_key_create_hint")
			if f.editingID != "" && f.hasKey {
				placeholder = i18n.T("pages.settings.ai.api_key_edit_hint")
			}
			v.Input(&f.apiKey, components.FieldOptions{
				Label:       i18n.T("pages.settings.ai.field_api_key"),
				Placeholder: placeholder,
				Password:    true,
				Disabled:    saving,
			})
			if f.editingID != "" && f.hasKey {
				v.Checkbox(&f.clearKey, i18n.T("pages.settings.ai.clear_key"), components.ToggleOptions{Disabled: saving})
			}
			v.Switch(&f.enabled, i18n.T("pages.settings.ai.field_enabled"), components.ToggleOptions{Disabled: saving})
			if s.ai.formProvider.err != "" {
				ui.Text(v.C, s.ai.formProvider.err).
					FontSize(tokens.Typography.Secondary.Size).
					TextColor(mygo.Color(v.T.Palette.StatusDanger))
			}
			if saveErr != "" {
				ui.Text(v.C, saveErr).
					FontSize(tokens.Typography.Secondary.Size).
					TextColor(mygo.Color(v.T.Palette.StatusDanger))
			}
		})
	})
}

// aiProviderDeleteDialog confirms a provider deletion. The backend refuses an
// in-use provider; the refusal stays in the dialog.
func (s *Settings) aiProviderDeleteDialog(v *components.UI, a *app.App) {
	if !s.ai.providerDeleteOpen {
		return
	}
	s.ai.mu.Lock()
	busy, delErr := s.ai.deleteBusy, s.ai.deleteErr
	s.ai.mu.Unlock()
	v.Dialog(&s.ai.providerDeleteOpen, components.DialogOptions{
		Title:       i18n.T("pages.settings.ai.delete_provider_title"),
		Description: i18n.Tf("pages.settings.ai.delete_provider_message", map[string]string{"name": s.ai.providerDeleteName}),
		Width:       tokens.Space12 * 9,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				s.ai.providerDeleteOpen = false
			}
			if v.Button(i18n.T("pages.settings.ai.btn_delete"), components.ButtonOptions{
				Variant: components.VariantDanger, Loading: busy, Disabled: busy,
			}).Clicked() {
				s.aiDeleteProvider(a)
			}
		},
	}, func() {
		if delErr != "" {
			ui.Text(v.C, delErr).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
}

// aiManualDialog adds a manual model to the selected provider.
func (s *Settings) aiManualDialog(v *components.UI, a *app.App) {
	if !s.ai.manualOpen {
		return
	}
	s.ai.mu.Lock()
	saving, addErr := s.ai.manualSaving, s.ai.manualErr
	s.ai.mu.Unlock()
	v.Dialog(&s.ai.manualOpen, components.DialogOptions{
		Title:       i18n.T("pages.settings.ai.manual_title"),
		Description: i18n.T("pages.settings.ai.manual_desc"),
		Width:       tokens.Space12 * 9,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				s.ai.manualOpen = false
			}
			if v.Button(i18n.T("pages.settings.ai.btn_save"), components.ButtonOptions{
				Variant: components.VariantPrimary, Loading: saving, Disabled: saving,
			}).Clicked() {
				s.aiAddManualModel(a)
			}
		},
	}, func() {
		ui.Column(v.C).FillWidth().Gap(tokens.RelatedGap).Children(func() {
			v.Input(&s.ai.manualModelID, components.FieldOptions{
				Label:       i18n.T("pages.settings.ai.field_model_id"),
				Placeholder: i18n.T("pages.settings.ai.field_model_id_hint"),
				AutoFocus:   true,
				Disabled:    saving,
			})
			v.Input(&s.ai.manualDisplayName, components.FieldOptions{
				Label:       i18n.T("pages.settings.ai.field_model_name"),
				Placeholder: i18n.T("pages.settings.ai.field_model_name_hint"),
				Disabled:    saving,
			})
			if addErr != "" {
				ui.Text(v.C, addErr).
					FontSize(tokens.Typography.Secondary.Size).
					TextColor(mygo.Color(v.T.Palette.StatusDanger))
			}
		})
	})
}

// aiModelDeleteDialog confirms one manual model removal.
func (s *Settings) aiModelDeleteDialog(v *components.UI, a *app.App) {
	if !s.ai.modelDeleteOpen {
		return
	}
	s.ai.mu.Lock()
	busy, delErr := s.ai.modelDeleteBusy, s.ai.modelDeleteErr
	s.ai.mu.Unlock()
	v.Dialog(&s.ai.modelDeleteOpen, components.DialogOptions{
		Title:       i18n.T("pages.settings.ai.model_delete_title"),
		Description: i18n.Tf("pages.settings.ai.model_delete_message", map[string]string{"model": s.ai.modelDeleteName}),
		Width:       tokens.Space12 * 8,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				s.ai.modelDeleteOpen = false
			}
			if v.Button(i18n.T("pages.settings.ai.btn_delete"), components.ButtonOptions{
				Variant: components.VariantDanger, Loading: busy, Disabled: busy,
			}).Clicked() {
				s.aiDeleteModel(a)
			}
		},
	}, func() {
		if delErr != "" {
			ui.Text(v.C, delErr).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
}

// aiProviderName falls back to the protocol when a provider has no name.
func aiProviderName(p provider.ProviderView) string {
	if strings.TrimSpace(p.Name) != "" {
		return p.Name
	}
	return i18n.T("pages.settings.ai.unnamed_provider")
}

// aiProtocols returns the four registered protocols in display order.
func aiProtocolChoices() []struct {
	label string
	proto provider.Protocol
} {
	return []struct {
		label string
		proto provider.Protocol
	}{
		{i18n.T("pages.settings.ai.protocol.openai_chat"), provider.ProtocolOpenAIChat},
		{i18n.T("pages.settings.ai.protocol.openai_responses"), provider.ProtocolOpenAIResponses},
		{i18n.T("pages.settings.ai.protocol.gemini"), provider.ProtocolGemini},
		{i18n.T("pages.settings.ai.protocol.anthropic"), provider.ProtocolAnthropic},
	}
}

func aiProtocolLabels() []string {
	choices := aiProtocolChoices()
	labels := make([]string, len(choices))
	for i, c := range choices {
		labels[i] = c.label
	}
	return labels
}

func protocolLabel(p provider.Protocol) string {
	for _, c := range aiProtocolChoices() {
		if c.proto == p {
			return c.label
		}
	}
	return string(p)
}

func protocolOfLabel(label string) (provider.Protocol, bool) {
	for _, c := range aiProtocolChoices() {
		if c.label == label {
			return c.proto, true
		}
	}
	return "", false
}

// aiProviderOptions builds unique labels for provider selects. The protocol
// keeps same-named providers apart; the suffix covers the remaining case.
func aiProviderOptions(providers []provider.ProviderView) (labels, ids []string) {
	seen := map[string]int{}
	for _, p := range providers {
		label := aiProviderName(p) + " · " + protocolLabel(p.Protocol)
		seen[label]++
		if seen[label] > 1 {
			label = fmt.Sprintf("%s #%d", label, seen[label])
		}
		labels = append(labels, label)
		ids = append(ids, p.ID)
	}
	return labels, ids
}

func filterProviders(providers []provider.ProviderView, required provider.Capability) []provider.ProviderView {
	out := make([]provider.ProviderView, 0, len(providers))
	for _, p := range providers {
		if p.Enabled && provider.Has(p.Protocol, required) {
			out = append(out, p)
		}
	}
	return out
}

func aiModelOptions(models []model.ModelInfo, required provider.Capability) []string {
	var labels []string
	for _, m := range models {
		if !m.Available || !hasCapability(m.Capabilities, required) {
			continue
		}
		labels = append(labels, m.ModelID)
	}
	return labels
}

func modelInList(models []model.ModelInfo, modelID string, required provider.Capability) bool {
	for _, m := range models {
		if m.ModelID == modelID && m.Available && hasCapability(m.Capabilities, required) {
			return true
		}
	}
	return false
}

func hasCapability(caps []provider.Capability, required provider.Capability) bool {
	for _, c := range caps {
		if c == required {
			return true
		}
	}
	return false
}

func aiPlaceholderProvider() string { return i18n.T("pages.settings.ai.provider_placeholder") }
func aiPlaceholderModel() string    { return i18n.T("pages.settings.ai.model_placeholder") }

func capabilityLabel(c provider.Capability) string {
	switch c {
	case provider.CapabilityChat:
		return i18n.T("pages.settings.ai.capability.chat")
	case provider.CapabilityEmbedding:
		return i18n.T("pages.settings.ai.capability.embedding")
	case provider.CapabilityRerank:
		return i18n.T("pages.settings.ai.capability.rerank")
	}
	return string(c)
}

func roleLabel(r role.Role) string {
	switch r {
	case role.RoleChatMain:
		return i18n.T("pages.settings.ai.role.chat_main")
	case role.RoleEmbedding:
		return i18n.T("pages.settings.ai.role.embedding")
	case role.RoleRerank:
		return i18n.T("pages.settings.ai.role.rerank")
	}
	return string(r)
}

func roleProblemText(problem string) string {
	switch problem {
	case role.ProblemRoleUnassigned:
		return i18n.T("pages.settings.ai.problem.role_unassigned")
	case role.ProblemProviderDisabled:
		return i18n.T("pages.settings.ai.problem.provider_disabled")
	case role.ProblemModelUnavailable:
		return i18n.T("pages.settings.ai.problem.model_unavailable")
	case role.ProblemCapabilityMissing:
		return i18n.T("pages.settings.ai.problem.capability_missing")
	}
	return i18n.T("pages.settings.ai.role_unassigned")
}

func findRoleView(views []role.RoleView, r role.Role) (role.RoleView, bool) {
	for _, v := range views {
		if v.Role == r {
			return v, true
		}
	}
	return role.RoleView{}, false
}

func indexOf(values []string, want string) int {
	for i, v := range values {
		if v == want {
			return i
		}
	}
	return -1
}

// aiTestText is the inline result of a connection test.
func aiTestText(res provider.TestResult) string {
	if res.OK {
		return i18n.Tf("pages.settings.ai.test_ok", map[string]string{
			"ms": strconv.FormatInt(res.LatencyMS, 10),
			"n":  strconv.Itoa(res.ModelCount),
		})
	}
	text := aiErrorKindText(res.ErrorKind)
	if msg := strings.TrimSpace(res.Message); msg != "" {
		text += "：" + aierr.Truncate(aierr.Redact(msg))
	}
	return text
}

// aiStoredErrorText localizes the persisted "kind|message" form of a model
// cache failure, and the "kind: message" form of a key read.
func aiStoredErrorText(s string) string {
	if kind, rest, ok := strings.Cut(s, "|"); ok {
		return aiErrorKindText(kind) + "：" + aierr.Truncate(aierr.Redact(rest))
	}
	if rest, ok := strings.CutPrefix(s, "secret_unavailable: "); ok {
		return aiErrorKindText(string(aierr.ErrKindSecretUnavailable)) + "：" + aierr.Truncate(aierr.Redact(rest))
	}
	return aierr.Truncate(aierr.Redact(s))
}

// aiErrText maps the typed provider/model/role errors to readable text. No
// branch ever sees or prints key material.
func aiErrText(err error) string {
	if err == nil {
		return ""
	}
	var pc *provider.ProtocolConflictError
	if errors.As(err, &pc) {
		return i18n.Tf("pages.settings.ai.error.protocol_conflict", map[string]string{
			"roles": roleNames(pc.Conflicts),
		})
	}
	var pi *provider.InUseError
	if errors.As(err, &pi) {
		return i18n.Tf("pages.settings.ai.error.provider_in_use", map[string]string{
			"roles": joinRoleNames(pi.Roles),
		})
	}
	var mi *model.InUseError
	if errors.As(err, &mi) {
		return i18n.Tf("pages.settings.ai.error.model_in_use", map[string]string{
			"roles": joinRoleNames(mi.Roles),
		})
	}
	var re *role.Error
	if errors.As(err, &re) {
		return roleProblemText(re.Problem)
	}
	switch {
	case errors.Is(err, provider.ErrInvalidName):
		return i18n.T("pages.settings.ai.error.name")
	case errors.Is(err, provider.ErrInvalidProtocol):
		return i18n.T("pages.settings.ai.error.protocol")
	case errors.Is(err, provider.ErrInvalidBaseURL):
		return i18n.T("pages.settings.ai.error.base_url")
	case errors.Is(err, provider.ErrKeyConflict):
		return i18n.T("pages.settings.ai.error.key_and_clear")
	case errors.Is(err, provider.ErrSecretStoreGone):
		return i18n.T("pages.settings.ai.error.secret_store")
	case errors.Is(err, secret.ErrSecretStoreUnavailable), errors.Is(err, secret.ErrSecretNotFound):
		return i18n.T("pages.settings.ai.error.secret_store")
	case errors.Is(err, provider.ErrNotFound):
		return i18n.T("pages.settings.ai.error.provider_missing")
	case errors.Is(err, provider.ErrListerUnavailable):
		return i18n.T("pages.settings.ai.error.lister")
	case errors.Is(err, model.ErrModelNotFound):
		return i18n.T("pages.settings.ai.error.model_missing")
	case errors.Is(err, model.ErrModelExists):
		return i18n.T("pages.settings.ai.error.model_exists")
	case errors.Is(err, model.ErrNotManual):
		return i18n.T("pages.settings.ai.error.model_not_manual")
	case errors.Is(err, model.ErrEmptyModelID):
		return i18n.T("pages.settings.ai.error.model_empty")
	case errors.Is(err, role.ErrUnknownRole):
		return i18n.T("pages.settings.ai.error.role_unknown")
	}
	return aiErrorText(err)
}

func roleNames(conflicts []provider.RoleConflict) string {
	names := make([]string, len(conflicts))
	for i, c := range conflicts {
		names[i] = roleLabel(role.Role(c.Role))
	}
	return strings.Join(names, roleSeparator())
}

func joinRoleNames(raw []string) string {
	names := make([]string, len(raw))
	for i, r := range raw {
		names[i] = roleLabel(role.Role(r))
	}
	return strings.Join(names, roleSeparator())
}

func roleSeparator() string {
	return i18n.T("pages.settings.ai.role_separator")
}
