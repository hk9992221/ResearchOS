package pages

import (
	"context"
	"strconv"
	"sync"

	"github.com/egoist/mygo/ui"

	"researchos/internal/foundation/config"
	foundationworkspace "researchos/internal/foundation/workspace"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
	"researchos/internal/workspace"
)

// Settings is the workspace settings page of §16.2.
type Settings struct {
	Services *Services

	tab int

	mu      sync.Mutex
	loading bool
	loaded  bool
	info    *workspace.Info
	scan    foundationworkspace.WorkspaceScan
	cfg     config.AppConfig
	loadErr error

	zotero string
}

// Page renders the settings groups.
func (s *Settings) Page(v *components.UI, r ports.Route) {
	s.load()
	s.mu.Lock()
	info, scan, cfg, loadErr, loaded, loading := s.info, s.scan, s.cfg, s.loadErr, s.loaded, s.loading
	s.mu.Unlock()

	v.PageContainer(components.PageContainerOptions{MaxWidth: components.ReadingWidth * 1.3, Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       "Settings",
			Description: "Workspace, external data, and maintenance.",
		})
		if loadErr != nil {
			v.Banner(components.BannerOptions{Tone: components.Warning, Title: "Some settings could not be read", Message: loadErr.Error()})
		}
		if !loaded && loading {
			v.Skeleton(components.SkeletonOptions{Lines: 5})
		}
		v.Tabs(&s.tab, "Workspace", "External Data", "Storage & Maintenance")
		switch s.tab {
		case 0:
			s.workspaceTab(v, info, scan, cfg)
		case 1:
			s.externalTab(v, cfg)
		default:
			s.storageTab(v, info)
		}
	}})
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
	s.Services.App.RunTask("Load workspace settings", func(ctx context.Context, t *app.Task) error {
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
	v.Card(components.CardOptions{Title: "Workspace"}, func() {
		ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
			s.row(v, "Location", root)
			if info != nil {
				s.row(v, "Projects", strconvPair(info.ProjectCount, info.ArchivedCount))
				s.row(v, "Database", humanBytes(info.DBBytes))
				s.row(v, "Library", humanBytes(info.LibraryBytes))
				s.row(v, "Free space", humanBytes(info.FreeBytes))
			}
			s.row(v, "Marker file", markerLabel(scan.Marker))
			if len(scan.SchemaIssues) > 0 {
				v.StatusIndicator(components.Warning, "Schema issues: "+strconv.Itoa(len(scan.SchemaIssues)))
			}
			v.Divider()
			ui.Text(v.C, "To move the workspace, plan a migration: it copies into an empty folder and verifies the copy before switching.").
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(p.TextSecondary))
			v.Button("Change workspace location…", components.ButtonOptions{
				Variant: components.VariantSecondary,
				Size:    components.SizeSmall,
				OnClick: func() { s.Services.App.Navigate(ports.Route{Kind: app.RouteMigration}) },
			})
		})
	})
}

func (s *Settings) externalTab(v *components.UI, cfg config.AppConfig) {
	p := v.T.Palette
	v.Card(components.CardOptions{Title: "External Data"}, func() {
		ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
			ui.Text(v.C, "Zotero data directory").
				FontSize(tokens.Typography.Body.Size).
				FontWeight(int(tokens.WeightSemibold))
			zotero := s.currentZotero()
			in := v.Input(&zotero, components.FieldOptions{
				Label:       "Zotero data directory",
				Description: "ResearchOS reads Zotero's local storage here. Detection arrives with the Literature module (SPEC-04).",
				Placeholder: "C:/Users/you/Zotero",
			})
			if in.Changed() {
				s.setZotero(zotero)
			}
			ui.Row(v.C).Gap(tokens.ComponentGap).Children(func() {
				v.Button("Browse…", components.ButtonOptions{
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
				v.Button("Save", components.ButtonOptions{
					Variant: components.VariantPrimary,
					OnClick: func() { s.saveZotero(cfg) },
				})
			})
			status := "Not configured"
			tone := components.Neutral
			if cfg.ZoteroDataDir != "" {
				status = "Configured: " + cfg.ZoteroDataDir
				tone = components.Success
			}
			v.StatusIndicator(tone, status)
			_ = p
		})
	})
}

func (s *Settings) storageTab(v *components.UI, info *workspace.Info) {
	v.Card(components.CardOptions{Title: "Storage & Maintenance"}, func() {
		ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
			if info != nil {
				s.row(v, "Workspace size", humanBytes(info.DBBytes+info.LibraryBytes+info.ExportsBytes))
			}
			ui.Row(v.C).Gap(tokens.ComponentGap).Children(func() {
				v.Button("Recycle Bin", components.ButtonOptions{
					Icon: icons.Trash,
					OnClick: func() {
						s.Services.App.Navigate(ports.Route{Kind: ports.RouteTrash})
					},
				})
				v.Button("File Consistency", components.ButtonOptions{
					Icon: icons.Scan,
					OnClick: func() {
						s.Services.App.Navigate(ports.Route{Kind: app.RouteConsistency})
					},
				})
				v.Button("Diagnostics", components.ButtonOptions{
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
func (s *Settings) saveZotero(cfg config.AppConfig) {
	if s.Services.Config == nil || s.Services.App == nil {
		return
	}
	dir := s.zotero
	s.Services.App.RunTask("Save external data settings", func(ctx context.Context, t *app.Task) error {
		cfg.ZoteroDataDir = dir
		if err := s.Services.Config.Save(ctx, cfg); err != nil {
			return err
		}
		s.Services.App.Toast(app.LevelSuccess, "Settings saved", "The Zotero data directory was updated.")
		return nil
	})
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
		return "Present"
	case "missing":
		return "Missing"
	case "invalid":
		return "Damaged"
	}
	return m.String()
}

func strconvPair(total, archived int) string {
	return strconv.Itoa(total) + " (" + strconv.Itoa(archived) + " archived)"
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
