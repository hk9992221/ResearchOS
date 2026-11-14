package pages

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"researchos/internal/foundation/diag"
	"researchos/internal/i18n"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/errtext"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// Diagnostics is the diagnostic page of §16.7.
type Diagnostics struct {
	Services *Services

	mu              sync.Mutex
	loading         bool
	loaded          bool
	report          diag.Report
	err             error
	includeFileScan bool
}

// Page renders the diagnostic report.
func (s *Diagnostics) Page(v *components.UI, r ports.Route) {
	if s.Services.Diagnostics == nil || s.Services.App == nil {
		v.PageContainer(components.PageContainerOptions{MaxWidth: components.ReadingWidth, Children: func() {
			v.PageHeader(components.PageHeaderOptions{Title: i18n.T("pages.diagnostics.title")})
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.Diagnostics,
				Title:  i18n.T("pages.diagnostics.empty_no_ws_title"),
				Reason: i18n.T("pages.diagnostics.empty_no_ws_reason"),
			})
		}})
		return
	}
	s.load()
	s.mu.Lock()
	report, loaded, loading, errText, include := s.report, s.loaded, s.loading, s.err, s.includeFileScan
	s.mu.Unlock()

	v.PageContainer(components.PageContainerOptions{MaxWidth: components.ReadingWidth * 1.5, Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.diagnostics.title"),
			Description: i18n.T("pages.diagnostics.description"),
		})
		if errText != nil {
			v.Banner(components.BannerOptions{Tone: components.Danger, Title: i18n.T("pages.diagnostics.banner_failed"), Message: errtext.Text(errText)})
		}
		v.Toolbar(i18n.T("pages.diagnostics.toolbar_label"), func() {
			v.Checkbox(&s.includeFileScan, i18n.T("pages.diagnostics.check_file_scan"), components.ToggleOptions{
				Description: i18n.T("pages.diagnostics.check_file_scan_desc"),
			})
			if s.includeFileScan != include {
				s.rescan()
			}
			v.Spacer()
			v.Button(i18n.T("pages.diagnostics.btn_run_again"), components.ButtonOptions{
				Icon:    icons.Refresh,
				Loading: loading,
				OnClick: func() { s.rescan() },
			})
			v.Button(i18n.T("pages.diagnostics.btn_copy"), components.ButtonOptions{
				Icon:     icons.Copy,
				Disabled: !loaded,
				OnClick: func() {
					v.C.WriteClipboard(diagText(report))
					s.Services.App.Toast(app.LevelSuccess, i18n.T("pages.diagnostics.toast_copied_title"), i18n.T("pages.diagnostics.toast_copied_message"))
				},
			})
		})

		if !loaded && loading {
			v.Skeleton(components.SkeletonOptions{Lines: 6})
			return
		}
		if !loaded {
			v.EmptyState(components.EmptyStateOptions{
				Icon:        icons.Diagnostics,
				Title:       i18n.T("pages.diagnostics.empty_title"),
				Reason:      i18n.T("pages.diagnostics.empty_reason"),
				ActionLabel: i18n.T("pages.diagnostics.btn_run"),
				OnAction:    func() { s.rescan() },
			})
			return
		}

		v.Card(components.CardOptions{Title: i18n.T("pages.diagnostics.card_global"), Description: report.Global.Path}, func() {
			ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
				s.row(v, i18n.T("pages.diagnostics.row_schema"), versionText(report.Global))
				s.row(v, i18n.T("pages.diagnostics.row_integrity"), integrityText(report.Global))
				s.row(v, i18n.T("pages.diagnostics.row_foreign_keys"), fkText(report.Global))
				s.row(v, i18n.T("pages.diagnostics.row_size"), i18n.Tf("pages.diagnostics.size_wal", map[string]string{
					"main": components.HumanBytes(report.Global.SizeBytes),
					"wal":  components.HumanBytes(report.Global.WALBytes),
				}))
			})
		})

		v.Card(components.CardOptions{Title: i18n.T("pages.diagnostics.card_projects"), Description: i18n.Tf("pages.diagnostics.projects_registered", map[string]string{"count": strconv.Itoa(len(report.Projects))})}, func() {
			if len(report.Projects) == 0 {
				v.StatusIndicator(components.Neutral, i18n.T("pages.diagnostics.projects_none"))
				return
			}
			rows := sortedProjectReports(report.Projects)
			cols := []components.Column{
				{Title: i18n.T("pages.diagnostics.col_project")},
				{Title: i18n.T("pages.diagnostics.col_schema"), Width: 80},
				{Title: i18n.T("pages.diagnostics.col_integrity"), Width: 90},
				{Title: i18n.T("pages.diagnostics.col_notes"), Width: 70, Align: ui.End},
				{Title: i18n.T("pages.diagnostics.col_missing"), Width: 80, Align: ui.End},
				{Title: i18n.T("pages.diagnostics.col_broken_refs"), Width: 100, Align: ui.End},
				{Title: i18n.T("pages.diagnostics.col_folder"), Width: 80},
			}
			state := components.NewTableState()
			state.Key = func(i int) any { return rows[i].ProjectID }
			table := v.Table(components.TableOptions{
				Columns: cols,
				Rows:    len(rows),
				State:   state,
				Key:     func(i int) any { return rows[i].ProjectID },
				Cell: func(row, col int) {
					p := rows[row]
					switch col {
					case 0:
						ui.Text(v.C, p.ProjectID).SingleLine()
					case 1:
						if p.DB == nil {
							ui.Text(v.C, i18n.T("pages.common.status_unavailable"))
							return
						}
						ui.Text(v.C, versionText(*p.DB)).FontFeatures("tnum")
					case 2:
						if p.DB == nil {
							ui.Text(v.C, i18n.T("pages.common.status_unavailable"))
							return
						}
						if p.DB.IntegrityOK {
							v.StatusIndicator(components.Success, i18n.T("pages.diagnostics.val_ok"))
						} else {
							v.StatusIndicator(components.Danger, i18n.T("pages.diagnostics.val_failed"))
						}
					case 3:
						ui.Text(v.C, fmt.Sprint(p.NotesTotal)).FontFeatures("tnum")
					case 4:
						if p.NotesMissing > 0 {
							v.StatusIndicator(components.Warning, fmt.Sprint(p.NotesMissing))
						} else {
							ui.Text(v.C, "0").FontFeatures("tnum")
						}
					case 5:
						if p.BrokenRefs > 0 {
							v.StatusIndicator(components.Warning, fmt.Sprint(p.BrokenRefs))
						} else {
							ui.Text(v.C, "0").FontFeatures("tnum")
						}
					case 6:
						if p.DirExists {
							v.StatusIndicator(components.Success, i18n.T("pages.diagnostics.val_present"))
						} else {
							v.StatusIndicator(components.Danger, i18n.T("pages.diagnostics.val_missing"))
						}
					}
				},
			})
			table.Height(tokens.Space8 + tokens.Space11*float32(len(rows)))
		})

		v.Card(components.CardOptions{Title: i18n.T("pages.diagnostics.card_config")}, func() {
			ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
				s.row(v, i18n.T("pages.diagnostics.row_file"), report.Config.Path)
				s.row(v, i18n.T("pages.diagnostics.row_exists"), boolText(report.Config.Exists))
				s.row(v, i18n.T("pages.diagnostics.row_workspace_root"), report.Config.WorkspaceRoot)
				s.row(v, i18n.T("pages.diagnostics.row_root_reachable"), boolText(report.Config.RootReachable))
				zotero := report.Config.ZoteroDataDir
				if zotero == "" {
					zotero = i18n.T("pages.settings.status_not_configured")
				}
				s.row(v, i18n.T("pages.diagnostics.row_zotero"), zotero)
				if report.Config.CorruptBackup != "" {
					s.row(v, i18n.T("pages.diagnostics.row_corrupt_backup"), report.Config.CorruptBackup)
				}
			})
		})

		if s.Services.MCPStatus != nil {
			status := s.Services.MCPStatus()
			v.Card(components.CardOptions{Title: i18n.T("pages.diagnostics.card_mcp"), Description: i18n.T("pages.diagnostics.card_mcp_desc")}, func() {
				ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
					state := i18n.T("pages.diagnostics.val_mcp_stopped")
					if status.Running {
						state = i18n.T("pages.diagnostics.val_mcp_running")
					}
					s.row(v, i18n.T("pages.diagnostics.row_mcp_state"), state)
					s.row(v, i18n.T("pages.diagnostics.row_mcp_socket"), status.Socket)
				})
			})
			if status.Error != "" {
				v.Banner(components.BannerOptions{
					Tone:    components.Danger,
					Title:   i18n.T("pages.diagnostics.banner_mcp_failed"),
					Message: status.Error,
				})
			}
		}

		if len(report.Warnings) > 0 {
			v.Banner(components.BannerOptions{
				Tone:    components.Warning,
				Title:   i18n.Tf("pages.diagnostics.banner_warnings", map[string]string{"count": strconv.Itoa(len(report.Warnings))}),
				Message: strings.Join(report.Warnings, "\n"),
			})
		}
	}})
}

func (s *Diagnostics) row(v *components.UI, label, value string) {
	p := v.T.Palette
	ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
		ui.Text(v.C, label).Width(140).FontSize(tokens.Typography.Secondary.Size).TextColor(mygo.Color(p.TextSecondary))
		ui.Text(v.C, value).Grow(1).FontSize(tokens.Typography.Body.Size).TextColor(mygo.Color(p.TextPrimary))
	})
}

// load runs the self check once.
func (s *Diagnostics) load() {
	s.mu.Lock()
	if s.loading || s.loaded {
		s.mu.Unlock()
		return
	}
	s.loading = true
	include := s.includeFileScan
	s.mu.Unlock()
	s.Services.App.RunTask(i18n.T("pages.diagnostics.task_run"), func(ctx context.Context, t *app.Task) error {
		rep, err := s.Services.Diagnostics.Run(ctx, diag.Options{IncludeFileScan: include})
		s.mu.Lock()
		s.loading = false
		s.loaded = true
		s.report = rep
		s.err = err
		s.mu.Unlock()
		return err
	})
}

// rescan runs the self check again with the current options.
func (s *Diagnostics) rescan() {
	s.mu.Lock()
	s.loaded = false
	s.err = nil
	s.mu.Unlock()
	s.load()
}

// versionText formats a schema version pair.
func versionText(db diag.DBReport) string {
	return i18n.Tf("pages.diagnostics.version_pair", map[string]string{
		"current": strconv.Itoa(db.SchemaVersion),
		"target":  strconv.Itoa(db.TargetVersion),
	})
}

// integrityText describes the integrity check. The detail text is the raw
// database message: diagnostic detail, not interface copy, so it passes
// through untranslated.
func integrityText(db diag.DBReport) string {
	if db.IntegrityOK {
		return i18n.T("pages.diagnostics.val_ok")
	}
	if db.IntegrityErr != "" {
		return i18n.Tf("pages.diagnostics.integrity_failed_detail", map[string]string{"error": db.IntegrityErr})
	}
	return i18n.T("pages.diagnostics.val_failed")
}

// fkText counts foreign key violations.
func fkText(db diag.DBReport) string {
	if len(db.ForeignKeyViolations) == 0 {
		return i18n.T("pages.diagnostics.fk_none")
	}
	return i18n.Tf("pages.diagnostics.fk_count", map[string]string{"count": strconv.Itoa(len(db.ForeignKeyViolations))})
}

func boolText(b bool) string {
	if b {
		return i18n.T("pages.common.bool_yes")
	}
	return i18n.T("pages.common.bool_no")
}

// sortedProjectReports orders projects by id, so the report is deterministic.
func sortedProjectReports(in []diag.ProjectReport) []diag.ProjectReport {
	out := append([]diag.ProjectReport(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ProjectID < out[j].ProjectID })
	return out
}

// diagText serializes the report for the clipboard: stable, human-readable and
// complete. Its labels follow the interface language; paths, project ids and
// numbers stay verbatim.
func diagText(rep diag.Report) string {
	var b strings.Builder
	b.WriteString(i18n.Tf("pages.diagnostics.clip_header", map[string]string{
		"time": rep.GeneratedAt.Format("2006-01-02T15:04:05Z07:00"),
	}))
	b.WriteString(i18n.Tf("pages.diagnostics.clip_global", map[string]string{
		"path":      rep.Global.Path,
		"schema":    versionText(rep.Global),
		"integrity": integrityText(rep.Global),
		"keys":      fkText(rep.Global),
		"size": i18n.Tf("pages.diagnostics.size_wal", map[string]string{
			"main": components.HumanBytes(rep.Global.SizeBytes),
			"wal":  components.HumanBytes(rep.Global.WALBytes),
		}),
	}))
	b.WriteString(i18n.Tf("pages.diagnostics.clip_config", map[string]string{
		"path":      rep.Config.Path,
		"exists":    boolText(rep.Config.Exists),
		"root":      rep.Config.WorkspaceRoot,
		"reachable": boolText(rep.Config.RootReachable),
		"zotero":    rep.Config.ZoteroDataDir,
	}))
	b.WriteString(i18n.T("pages.diagnostics.clip_projects"))
	for _, p := range sortedProjectReports(rep.Projects) {
		version := i18n.T("pages.common.status_unavailable")
		integrity := i18n.T("pages.common.status_unavailable")
		if p.DB != nil {
			version = versionText(*p.DB)
			integrity = integrityText(*p.DB)
		}
		b.WriteString(i18n.Tf("pages.diagnostics.clip_project_row", map[string]string{
			"id":        p.ProjectID,
			"schema":    version,
			"integrity": integrity,
			"notes":     strconv.Itoa(p.NotesTotal),
			"missing":   strconv.Itoa(p.NotesMissing),
			"refs":      strconv.Itoa(p.BrokenRefs),
			"folder":    boolText(p.DirExists),
		}))
	}
	b.WriteString(i18n.T("pages.diagnostics.clip_warnings"))
	if len(rep.Warnings) == 0 {
		b.WriteString(i18n.T("pages.diagnostics.clip_warnings_none"))
	}
	for _, w := range rep.Warnings {
		b.WriteString(i18n.Tf("pages.diagnostics.clip_warning_item", map[string]string{"warning": w}))
	}
	return b.String()
}
