package pages

import (
	"context"
	"strconv"
	"sync"

	"github.com/egoist/mygo/ui"

	"researchos/internal/foundation/config"
	foundationworkspace "researchos/internal/foundation/workspace"
	"researchos/internal/i18n"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/errtext"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
	"researchos/internal/workspace"
)

// Settings is the workspace settings page of §16.2.
type Settings struct {
	Services *Services

	tab int
	// tabSeq is the route generation whose tab parameter was already applied,
	// so a later user tab click is not overridden by the same entry.
	tabSeq uint64

	mu      sync.Mutex
	loading bool
	loaded  bool
	info    *workspace.Info
	scan    foundationworkspace.WorkspaceScan
	cfg     config.AppConfig
	loadErr error

	zotero string

	// langTag is the language value the configuration holds ("" is "system")
	// and langLabel the option label the language select shows. The select
	// writes the chosen label into langLabel directly, as a match filter
	// does, and the next frame turns it back into a tag.
	langTag   string
	langLabel string

	// ai is the SPEC-02 configuration tab: providers, model cache and role
	// assignments, with its own lock because the action tasks write it while
	// the page renders.
	ai settingsAI
	// mcp is the SPEC-11 server tab: lifecycle, settings, clients and tools.
	mcp settingsMCP
}

// The Settings tabs in display order. The AI tab sits before maintenance so
// the configuration the modules depend on is easy to find.
const (
	settingsTabWorkspace = iota
	settingsTabExternal
	settingsTabAI
	settingsTabMCP
	settingsTabStorage
)

// Page renders the settings groups.
func (s *Settings) Page(v *components.UI, r ports.Route) {
	s.applyRouteTab(r)
	s.load()
	if s.tab == settingsTabAI {
		s.aiLoad()
	} else {
		s.aiClearTransient()
	}
	if s.tab == settingsTabMCP {
		s.mcpLoad(s.Services.App)
	} else {
		s.mcpClearTransient()
	}
	s.mu.Lock()
	info, scan, cfg, loadErr, loaded, loading := s.info, s.scan, s.cfg, s.loadErr, s.loaded, s.loading
	s.mu.Unlock()

	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.settings.title"),
			Description: i18n.T("pages.settings.description"),
		})
		if loadErr != nil {
			v.Banner(components.BannerOptions{Tone: components.Warning, Title: i18n.T("pages.settings.banner_load_fail"), Message: errtext.Text(loadErr)})
		}
		if !loaded && loading {
			v.Skeleton(components.SkeletonOptions{Lines: 5})
		}
		v.Tabs(&s.tab,
			i18n.T("pages.settings.tab_workspace"),
			i18n.T("pages.settings.tab_external"),
			i18n.T("pages.settings.tab_ai"),
			i18n.T("pages.settings.tab_mcp"),
			i18n.T("pages.settings.tab_storage"),
		)
		switch s.tab {
		case settingsTabWorkspace:
			s.workspaceTab(v, info, scan, cfg)
		case settingsTabExternal:
			s.externalTab(v, cfg)
		case settingsTabAI:
			s.aiTab(v, s.Services.App)
		case settingsTabMCP:
			s.mcpTab(v, s.Services.App)
		default:
			s.storageTab(v, info)
		}
	}})
}

// applyRouteTab honors Settings deep links once per route entry. A module's
// 「前往设置配置 AI」 navigates with Params{"tab":"ai"}; the user's subsequent
// tab clicks remain untouched because RouteSeq does not change.
func (s *Settings) applyRouteTab(r ports.Route) {
	a := s.Services.App
	if a == nil {
		return
	}
	if seq := a.RouteSeq(); seq != s.tabSeq {
		s.tabSeq = seq
		if tab, ok := settingsTabFromParam(r.Params["tab"]); ok {
			s.tab = tab
		}
	}
}

// settingsTabFromParam maps the public tab names of a Settings route.
func settingsTabFromParam(param string) (int, bool) {
	switch param {
	case "workspace":
		return settingsTabWorkspace, true
	case "external":
		return settingsTabExternal, true
	case "ai":
		return settingsTabAI, true
	case "mcp":
		return settingsTabMCP, true
	case "storage":
		return settingsTabStorage, true
	}
	return 0, false
}

func (s *Settings) load() {
	s.mu.Lock()
	if s.loading || s.loaded {
		s.mu.Unlock()
		return
	}
	s.loading = true
	s.mu.Unlock()
	if s.Services.App == nil {
		s.mu.Lock()
		s.loading = false
		s.loaded = true
		s.mu.Unlock()
		return
	}
	s.Services.App.RunTask(i18n.T("pages.settings.task_load"), func(ctx context.Context, t *app.Task) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		defer func() {
			s.loading = false
			s.loaded = true
		}()
		if s.Services.Workspace != nil {
			info, err := s.Services.Workspace.Info(ctx)
			if err != nil {
				s.loadErr = err
			}
			s.info = info
		}
		if s.Services.Scan != nil {
			scan, err := s.Services.Scan(ctx)
			if err != nil {
				s.loadErr = err
			}
			s.scan = scan
		}
		if s.Services.Config != nil {
			cfg, err := s.Services.Config.Load(ctx)
			if err != nil {
				s.loadErr = err
			}
			s.cfg = cfg
			s.zotero = cfg.ZoteroDataDir
			s.langTag = cfg.Language
			s.langLabel = ""
		}
		return nil
	})
}

func (s *Settings) workspaceTab(v *components.UI, info *workspace.Info, scan foundationworkspace.WorkspaceScan, cfg config.AppConfig) {
	p := v.T.Palette
	root := cfg.WorkspaceRoot
	if info != nil && info.Root != "" {
		root = info.Root
	}
	v.Card(components.CardOptions{Title: i18n.T("pages.settings.tab_workspace")}, func() {
		ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
			s.row(v, i18n.T("pages.settings.row_location"), root)
			if info != nil {
				s.row(v, i18n.T("pages.settings.row_projects"), strconvPair(info.ProjectCount, info.ArchivedCount))
				s.row(v, i18n.T("pages.settings.row_database"), components.HumanBytes(info.DBBytes))
				s.row(v, i18n.T("pages.settings.row_library"), components.HumanBytes(info.LibraryBytes))
				s.row(v, i18n.T("pages.settings.row_free_space"), components.HumanBytes(info.FreeBytes))
			}
			s.row(v, i18n.T("pages.settings.row_marker"), markerLabel(scan.Marker))
			if len(scan.SchemaIssues) > 0 {
				v.StatusIndicator(components.Warning, i18n.Tf("pages.settings.schema_issues", map[string]string{"count": strconv.Itoa(len(scan.SchemaIssues))}))
			}
			v.Divider()
			ui.Text(v.C, i18n.T("pages.settings.migration_note")).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(p.TextSecondary))
			v.Button(i18n.T("pages.settings.btn_change_location"), components.ButtonOptions{
				Variant: components.VariantSecondary,
				Size:    components.SizeSmall,
				OnClick: func() { s.Services.App.Navigate(ports.Route{Kind: app.RouteMigration}) },
			})
		})
	})
	s.languageCard(v)
}

func (s *Settings) externalTab(v *components.UI, cfg config.AppConfig) {
	p := v.T.Palette
	v.Card(components.CardOptions{Title: i18n.T("pages.settings.tab_external")}, func() {
		ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
			ui.Text(v.C, i18n.T("pages.settings.zotero_label")).
				FontSize(tokens.Typography.Body.Size).
				FontWeight(int(tokens.WeightSemibold))
			zotero := s.currentZotero()
			in := v.Input(&zotero, components.FieldOptions{
				Label:       i18n.T("pages.settings.zotero_label"),
				Description: i18n.T("pages.settings.zotero_desc"),
				Placeholder: "C:/Users/you/Zotero",
			})
			if in.Changed() {
				s.setZotero(zotero)
			}
			ui.Row(v.C).Gap(tokens.ComponentGap).Children(func() {
				v.Button(i18n.T("pages.common.btn_browse"), components.ButtonOptions{
					Variant: components.VariantSecondary,
					OnClick: func() {
						if s.Services.ChooseFolder == nil {
							return
						}
						if dir, err := s.Services.ChooseFolder(); err == nil && dir != "" {
							s.setZotero(dir)
						}
					},
				})
				v.Button(i18n.T("common.save"), components.ButtonOptions{
					Variant: components.VariantPrimary,
					OnClick: func() { s.saveZotero() },
				})
			})
			status := i18n.T("pages.settings.status_not_configured")
			tone := components.Neutral
			if cfg.ZoteroDataDir != "" {
				status = i18n.Tf("pages.settings.status_configured", map[string]string{"path": cfg.ZoteroDataDir})
				tone = components.Success
			}
			v.StatusIndicator(tone, status)
			_ = p
		})
	})
}

func (s *Settings) storageTab(v *components.UI, info *workspace.Info) {
	v.Card(components.CardOptions{Title: i18n.T("pages.settings.tab_storage")}, func() {
		ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
			if info != nil {
				s.row(v, i18n.T("pages.settings.row_workspace_size"), components.HumanBytes(info.DBBytes+info.LibraryBytes+info.ExportsBytes))
			}
			ui.Row(v.C).Gap(tokens.ComponentGap).Children(func() {
				v.Button(i18n.T("pages.settings.btn_recycle_bin"), components.ButtonOptions{
					Icon: icons.Trash,
					OnClick: func() {
						s.Services.App.Navigate(ports.Route{Kind: ports.RouteTrash})
					},
				})
				v.Button(i18n.T("pages.settings.btn_file_consistency"), components.ButtonOptions{
					Icon: icons.Scan,
					OnClick: func() {
						s.Services.App.Navigate(ports.Route{Kind: app.RouteConsistency})
					},
				})
				v.Button(i18n.T("pages.settings.btn_diagnostics"), components.ButtonOptions{
					Icon: icons.Diagnostics,
					OnClick: func() {
						s.Services.App.Navigate(ports.Route{Kind: app.RouteDiagnostics})
					},
				})
			})
		})
	})
}

// saveZotero writes the Zotero directory through the configuration service.
// The configuration is read again inside the task instead of reusing the
// snapshot the page was rendered with, so a change made meanwhile, such as the
// language, is not overwritten with the value the page last saw.
func (s *Settings) saveZotero() {
	if s.Services.Config == nil || s.Services.App == nil {
		return
	}
	dir := s.zotero
	s.Services.App.RunTask(i18n.T("pages.settings.task_save_zotero"), func(ctx context.Context, t *app.Task) error {
		cfg, err := s.Services.Config.Load(ctx)
		if err != nil {
			return err
		}
		cfg.ZoteroDataDir = dir
		if err := s.Services.Config.Save(ctx, cfg); err != nil {
			return err
		}
		s.Services.App.Toast(app.LevelSuccess, i18n.T("pages.settings.toast_saved_title"), i18n.T("pages.settings.toast_saved_message"))
		return nil
	})
}

// languageCard renders the interface language choice. The choice lives in the
// application configuration, not in workspace settings, because it applies to
// the first-run wizard too. A choice takes effect at once: every label is read
// through i18n while the frame is built, and App.Notify repaints the window,
// as a theme change does.
func (s *Settings) languageCard(v *components.UI) {
	if !s.isLoaded() {
		return
	}
	labels, tags := languageChoices()
	expected := languageChoiceLabel(labels, tags, s.languageTag())

	s.mu.Lock()
	if s.langLabel == "" {
		s.langLabel = expected
	}
	chosen := s.langLabel
	s.mu.Unlock()

	// The select writes the chosen option's label into langLabel, so a label
	// that no longer matches the configured tag is the user's new choice.
	if chosen != expected {
		if tag, ok := languageTagOf(labels, tags, chosen); ok {
			s.applyLanguage(tag)
			labels, tags = languageChoices()
			expected = languageChoiceLabel(labels, tags, tag)
		}
		s.mu.Lock()
		s.langLabel = expected
		s.mu.Unlock()
	}

	v.Card(components.CardOptions{Title: i18n.T("settings.language.title")}, func() {
		v.Select(&s.langLabel, components.SelectOptions{
			Label:       i18n.T("settings.language.label"),
			Description: i18n.T("settings.language.description"),
			Options:     labels,
		})
	})
}

// applyLanguage switches the interface language and stores the choice through
// the configuration service, the path the Zotero directory uses. The
// configuration is read again inside the task so a concurrent save is not
// lost.
func (s *Settings) applyLanguage(tag string) {
	i18n.SetLanguage(tag)
	s.mu.Lock()
	s.langTag = tag
	s.cfg.Language = tag
	s.mu.Unlock()

	a := s.Services.App
	if a == nil {
		return
	}
	if svc := s.Services.Config; svc != nil {
		a.RunTask(i18n.T("pages.settings.task_save_language"), func(ctx context.Context, t *app.Task) error {
			cfg, err := svc.Load(ctx)
			if err != nil {
				return err
			}
			cfg.Language = tag
			if err := svc.Save(ctx, cfg); err != nil {
				return err
			}
			a.Toast(app.LevelSuccess, i18n.T("settings.language.title"), i18n.T("settings.language.saved"))
			return nil
		})
	}
	a.Notify()
}

// isLoaded reports whether the stored configuration has been read, so the
// language select never shows a guess and then corrects itself.
func (s *Settings) isLoaded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loaded
}

// languageTag is the configured language value, with "" read as "system".
func (s *Settings) languageTag() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.langTag == "" {
		return "system"
	}
	return s.langTag
}

// languageChoices returns the select's option labels and the tags behind
// them: following the system first, showing what the system resolves to, then
// every pack by its native name.
func languageChoices() (labels, tags []string) {
	labels = []string{i18n.Tf("settings.language.system_hint", map[string]string{
		"language": languageName(i18n.SystemLanguage()),
	})}
	tags = []string{"system"}
	for _, o := range i18n.Available() {
		labels = append(labels, o.NativeName)
		tags = append(tags, o.Tag)
	}
	return labels, tags
}

// languageName is a pack's native name, or the tag when no pack carries it.
func languageName(tag string) string {
	for _, o := range i18n.Available() {
		if o.Tag == tag {
			return o.NativeName
		}
	}
	return tag
}

// languageTagOf is the tag of an option's label.
func languageTagOf(labels, tags []string, label string) (string, bool) {
	for i, l := range labels {
		if l == label {
			return tags[i], true
		}
	}
	return "", false
}

// languageChoiceLabel is the label of tag. A tag no option names, as a value
// written by a newer build, falls back to the language actually in use.
func languageChoiceLabel(labels, tags []string, tag string) string {
	if label, ok := labelOfTag(labels, tags, tag); ok {
		return label
	}
	if label, ok := labelOfTag(labels, tags, i18n.Language()); ok {
		return label
	}
	return labels[0]
}

// labelOfTag is the option label of a tag, if an option names it.
func labelOfTag(labels, tags []string, tag string) (string, bool) {
	for i, t := range tags {
		if t == tag {
			return labels[i], true
		}
	}
	return "", false
}

// row renders a label and value pair.
func (s *Settings) row(v *components.UI, label, value string) {
	p := v.T.Palette
	ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
		ui.Text(v.C, label).
			Width(140).
			FontSize(tokens.Typography.Secondary.Size).
			TextColor(mygo.Color(p.TextSecondary))
		ui.Text(v.C, value).
			Grow(1).
			FontSize(tokens.Typography.Body.Size).
			TextColor(mygo.Color(p.TextPrimary))
	})
}

// markerLabel names the marker state.
func markerLabel(m foundationworkspace.MarkerState) string {
	switch m.String() {
	case "present":
		return i18n.T("pages.settings.marker_present")
	case "missing":
		return i18n.T("pages.settings.marker_missing")
	case "invalid":
		return i18n.T("pages.settings.marker_damaged")
	}
	return m.String()
}

func strconvPair(total, archived int) string {
	return i18n.Tf("pages.settings.projects_pair", map[string]string{
		"total":    strconv.Itoa(total),
		"archived": strconv.Itoa(archived),
	})
}

func (s *Settings) currentZotero() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.zotero
}

func (s *Settings) setZotero(v string) {
	s.mu.Lock()
	s.zotero = v
	s.mu.Unlock()
}
