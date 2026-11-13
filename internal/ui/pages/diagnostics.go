package pages

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"researchos/internal/foundation/diag"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
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
			v.PageHeader(components.PageHeaderOptions{Title: "Diagnostics"})
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.Diagnostics,
				Title:  "Diagnostics need an open workspace",
				Reason: "The self check reads the workspace databases and configuration. Set up a workspace first.",
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
			Title:       "Diagnostics",
			Description: "Read-only checks of the workspace databases, note files and configuration.",
		})
		if errText != nil {
			v.Banner(components.BannerOptions{Tone: components.Danger, Title: "The self check failed", Message: errText.Error()})
		}
		v.Toolbar("Diagnostics actions", func() {
			v.Checkbox(&s.includeFileScan, "Scan note files too", components.ToggleOptions{
				Description: "Checks that every active note body exists on disk; slower on large libraries.",
			})
			if s.includeFileScan != include {
				s.rescan()
			}
			v.Spacer()
			v.Button("Run again", components.ButtonOptions{
				Icon:    icons.Refresh,
				Loading: loading,
				OnClick: func() { s.rescan() },
			})
			v.Button("Copy report", components.ButtonOptions{
				Icon:     icons.Copy,
				Disabled: !loaded,
				OnClick: func() {
					v.C.WriteClipboard(diagText(report))
					s.Services.App.Toast(app.LevelSuccess, "Report copied", "The diagnostic report is on the clipboard.")
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
				Title:       "No report yet",
				Reason:      "Run the self check to read database versions, integrity and note-file state.",
				ActionLabel: "Run diagnostics",
				OnAction:    func() { s.rescan() },
			})
			return
		}

		v.Card(components.CardOptions{Title: "Global database", Description: report.Global.Path}, func() {
			ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
				s.row(v, "Schema version", versionText(report.Global))
				s.row(v, "Integrity", integrityText(report.Global))
				s.row(v, "Foreign keys", fkText(report.Global))
				s.row(v, "Size", humanBytes(report.Global.SizeBytes)+" + "+humanBytes(report.Global.WALBytes)+" WAL")
			})
		})

		v.Card(components.CardOptions{Title: "Projects", Description: fmt.Sprintf("%d registered", len(report.Projects))}, func() {
			if len(report.Projects) == 0 {
				v.StatusIndicator(components.Neutral, "No projects registered")
				return
			}
			rows := sortedProjectReports(report.Projects)
			cols := []components.Column{
				{Title: "Project"},
				{Title: "Schema", Width: 80},
				{Title: "Integrity", Width: 90},
				{Title: "Notes", Width: 70, Align: ui.End},
				{Title: "Missing", Width: 80, Align: ui.End},
				{Title: "Broken refs", Width: 100, Align: ui.End},
				{Title: "Folder", Width: 80},
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
							ui.Text(v.C, "unavailable")
							return
						}
						ui.Text(v.C, versionText(*p.DB)).FontFeatures("tnum")
					case 2:
						if p.DB == nil {
							ui.Text(v.C, "unavailable")
							return
						}
						if p.DB.IntegrityOK {
							v.StatusIndicator(components.Success, "ok")
						} else {
							v.StatusIndicator(components.Danger, "failed")
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
							v.StatusIndicator(components.Success, "present")
						} else {
							v.StatusIndicator(components.Danger, "missing")
						}
					}
				},
			})
			table.Height(tokens.Space8 + tokens.Space11*float32(len(rows)))
		})

		v.Card(components.CardOptions{Title: "Configuration"}, func() {
			ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
				s.row(v, "File", report.Config.Path)
				s.row(v, "Exists", boolText(report.Config.Exists))
				s.row(v, "Workspace root", report.Config.WorkspaceRoot)
				s.row(v, "Root reachable", boolText(report.Config.RootReachable))
				zotero := report.Config.ZoteroDataDir
				if zotero == "" {
					zotero = "Not configured"
				}
				s.row(v, "Zotero data", zotero)
				if report.Config.CorruptBackup != "" {
					s.row(v, "Corrupt backup", report.Config.CorruptBackup)
				}
			})
		})

		if len(report.Warnings) > 0 {
			v.Banner(components.BannerOptions{
				Tone:    components.Warning,
				Title:   fmt.Sprintf("%d warnings", len(report.Warnings)),
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
	s.Services.App.RunTask("Run diagnostics", func(ctx context.Context, t *app.Task) error {
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
	return fmt.Sprintf("%d / %d", db.SchemaVersion, db.TargetVersion)
}

// integrityText describes the integrity check.
func integrityText(db diag.DBReport) string {
	if db.IntegrityOK {
		return "ok"
	}
	if db.IntegrityErr != "" {
		return "failed: " + db.IntegrityErr
	}
	return "failed"
}

// fkText counts foreign key violations.
func fkText(db diag.DBReport) string {
	if len(db.ForeignKeyViolations) == 0 {
		return "0 violations"
	}
	return fmt.Sprintf("%d violations", len(db.ForeignKeyViolations))
}

func boolText(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

// sortedProjectReports orders projects by id, so the report is deterministic.
func sortedProjectReports(in []diag.ProjectReport) []diag.ProjectReport {
	out := append([]diag.ProjectReport(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ProjectID < out[j].ProjectID })
	return out
}

// diagText serializes the report for the clipboard: stable, human-readable and
// complete.
func diagText(rep diag.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "ResearchOS diagnostic report\nGenerated: %s\n\n", rep.GeneratedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(&b, "Global database: %s\n  schema %s\n  integrity %s\n  foreign keys %s\n  size %s + %s WAL\n\n",
		rep.Global.Path, versionText(rep.Global), integrityText(rep.Global), fkText(rep.Global),
		humanBytes(rep.Global.SizeBytes), humanBytes(rep.Global.WALBytes))
	fmt.Fprintf(&b, "Configuration: %s\n  exists %s\n  root %s\n  reachable %s\n  zotero %s\n\n",
		rep.Config.Path, boolText(rep.Config.Exists), rep.Config.WorkspaceRoot, boolText(rep.Config.RootReachable), rep.Config.ZoteroDataDir)
	b.WriteString("Projects:\n")
	for _, p := range sortedProjectReports(rep.Projects) {
		version := "unavailable"
		integrity := "unavailable"
		if p.DB != nil {
			version = versionText(*p.DB)
			integrity = integrityText(*p.DB)
		}
		fmt.Fprintf(&b, "  %s: schema %s, integrity %s, notes %d, missing %d, broken refs %d, folder %s\n",
			p.ProjectID, version, integrity, p.NotesTotal, p.NotesMissing, p.BrokenRefs, boolText(p.DirExists))
	}
	b.WriteString("\nWarnings:\n")
	if len(rep.Warnings) == 0 {
		b.WriteString("  none\n")
	}
	for _, w := range rep.Warnings {
		fmt.Fprintf(&b, "  - %s\n", w)
	}
	return b.String()
}
