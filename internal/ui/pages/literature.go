package pages

// Literature is the SPEC-04 page of the shell: the item browser and the
// read-only preview that precedes the import into the open project. Every
// database and Zotero read runs in a managed task, so the frame only renders
// cached state; the import progress is reported through the shell's task
// strip.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"researchos/internal/i18n"
	"researchos/internal/literature"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/errtext"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/tokens"
)

// literaturePageSize is the browser page size. The Zotero reader accepts at
// most 100 items per call (SPEC-04 §5.2).
const literaturePageSize = 100

// litVisibleRows is how many table rows fit before the list scrolls inside its
// own box (design system row height 44px).
const litVisibleRows = 10

// Literature renders the page.
type Literature struct {
	Services *Services

	mu       sync.Mutex
	loading  bool
	loaded   bool
	project  string
	state    litState
	problems []string

	// These are written while a frame is built only: the collection name shown
	// in the picker, the search text, the chosen item keys and the open flag of
	// the preview dialog.
	collection  string
	query       string
	previewOpen bool
	sel         *ui.Selection[string]
}

// litState is the cached service state the frame renders.
type litState struct {
	collections []literature.ZoteroCollection
	items       []literature.ZoteroItem
	total       int
	offset      int
	linked      map[string]bool
	papers      []literature.ProjectPaper
	preview     []literature.ImportPreviewEntry
	previewErrs []literature.ImportError
	choices     map[string][]literature.ZoteroAttachment
	chosen      map[string]string
	result      *literature.ImportResult
}

// Page renders one frame of the literature module.
func (l *Literature) Page(v *components.UI, r ports.Route) {
	a := l.Services.App
	if a == nil {
		return
	}
	if l.sel == nil {
		l.sel = components.NewSelection[string]()
	}
	if l.Services.Literature == nil {
		l.unavailable(v)
		return
	}
	project := a.Project()
	if project == nil {
		l.needsProject(v, a)
		return
	}

	l.load(a, project.ID)
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.literature.title"),
			Description: i18n.Tf("pages.literature.description", map[string]string{"project": project.Name}),
			Actions: func() {
				if v.Button(i18n.T("pages.literature.btn_refresh"), components.ButtonOptions{
					Variant: components.VariantGhost,
					Icon:    icons.Refresh,
				}).Clicked() {
					l.reload(a, project.ID)
				}
			},
		})
		l.banners(v)
		l.browser(v, a, project.ID)
		l.linked(v)
		l.dialog(v, a, project.ID)
	}})
}

// unavailable is the honest state of a workspace whose literature module was
// never assembled: it names the cause instead of rendering an empty browser.
func (l *Literature) unavailable(v *components.UI) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{Title: i18n.T("pages.literature.title")})
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.Literature,
			Title:  i18n.T("pages.literature.disabled_title"),
			Reason: i18n.T("pages.literature.disabled_reason"),
		})
	}})
}

// needsProject states the precondition of the module: papers are imported into
// a project, so one has to be open first.
func (l *Literature) needsProject(v *components.UI, a *app.App) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.literature.title"),
			Description: i18n.T("pages.literature.needs_project_desc"),
		})
		v.EmptyState(components.EmptyStateOptions{
			Icon:        icons.Literature,
			Title:       i18n.T("pages.literature.needs_project_title"),
			Reason:      i18n.T("pages.literature.needs_project_reason"),
			ActionLabel: i18n.T("pages.literature.btn_projects"),
			OnAction:    func() { a.Navigate(ports.Route{Kind: app.RouteProjects}) },
		})
	}})
}

// banners reports load failures and the outcome of the last import.
func (l *Literature) banners(v *components.UI) {
	l.mu.Lock()
	problems, result := append([]string(nil), l.problems...), l.state.result
	l.mu.Unlock()
	for _, problem := range problems {
		v.Banner(components.BannerOptions{
			Tone:    components.Warning,
			Title:   i18n.T("pages.literature.banner_problem"),
			Message: problem,
		})
	}
	if result == nil {
		return
	}
	tone := components.Success
	if result.Failed > 0 {
		tone = components.Warning
	}
	v.Banner(components.BannerOptions{
		Tone:  tone,
		Title: i18n.T("pages.literature.result_title"),
		Message: i18n.Tf("pages.literature.result_counts", map[string]string{
			"imported": strconv.Itoa(result.Imported),
			"linked":   strconv.Itoa(result.Linked),
			"skipped":  strconv.Itoa(result.Skipped),
			"failed":   strconv.Itoa(result.Failed),
		}) + failuresText(result),
	})
}

// failuresText lists the per-item failures; an import that dropped one PDF must
// not hide why (SPEC-04 §5.4: outcomes are per entry).
func failuresText(result *literature.ImportResult) string {
	var lines []string
	for _, outcome := range result.Outcomes {
		if outcome.Status == literature.ImportFailed {
			lines = append(lines, "  "+outcome.ZoteroKey+": "+outcome.Error)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n" + i18n.T("pages.literature.result_failures") + "\n" + strings.Join(lines, "\n")
}

// browser is the Zotero picker: collection, search, paging and the selection
// that feeds the preview.
func (l *Literature) browser(v *components.UI, a *app.App, projectID string) {
	l.mu.Lock()
	s, loading := l.state, l.loading
	l.mu.Unlock()

	names, keys := collectionLabels(s.collections)
	selector := labelForKey(l.collection, names, keys)
	chosen := l.previewChosen()

	v.Card(components.CardOptions{
		Title:       i18n.T("pages.literature.card_items"),
		Description: i18n.T("pages.literature.card_items_desc"),
	}, func() {
		ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
			picker := v.Select(&selector, components.SelectOptions{
				Label:   i18n.T("pages.literature.field_collection"),
				Options: names,
				Width:   260,
			})
			if picker.Changed() {
				l.setCollection(keyForLabel(selector, names, keys))
				l.loadItems(a)
			}
			searchField := v.SearchInput(&l.query, components.SearchOptions{
				Label:       i18n.T("pages.literature.field_search"),
				Placeholder: i18n.T("pages.literature.field_search_hint"),
			})
			if searchField.Submitted() {
				l.loadItems(a)
			}
			if v.Button(i18n.T("pages.literature.btn_search"), components.ButtonOptions{
				Variant: components.VariantSecondary,
			}).Clicked() {
				l.loadItems(a)
			}
		})

		if len(s.items) == 0 {
			if loading {
				v.Skeleton(components.SkeletonOptions{Lines: 5})
				return
			}
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.Literature,
				Title:  i18n.T("pages.literature.items_empty_title"),
				Reason: i18n.T("pages.literature.items_empty_reason"),
			})
			return
		}

		items := s.items
		state := components.NewTableState()
		state.Key = func(i int) any { return items[i].Key }
		table := v.Table(components.TableOptions{
			Columns: []components.Column{
				{Title: i18n.T("pages.literature.col_title")},
				{Title: i18n.T("pages.literature.col_type"), Width: 140},
				{Title: i18n.T("pages.literature.col_date"), Width: 100},
				{Title: i18n.T("pages.literature.col_state"), Width: 110},
			},
			Rows:      len(items),
			State:     state,
			Key:       func(i int) any { return items[i].Key },
			Selection: l.sel,
			Cell: func(row, col int) {
				item := items[row]
				switch col {
				case 0:
					ui.Text(v.C, itemDisplayTitle(item)).SingleLine()
				case 1:
					ui.Text(v.C, item.ItemType).SingleLine()
				case 2:
					ui.Text(v.C, item.Date).SingleLine()
				case 3:
					if s.linked[item.Key] {
						v.Badge(i18n.T("pages.literature.badge_imported"), components.Success)
					} else {
						v.Badge(i18n.T("pages.literature.badge_new"), components.Neutral)
					}
				}
			},
		})

		// The rows scroll inside the table: an unbounded table would push the
		// actions below the window on a large library.
		table.Height(visibleTableHeight(len(items)))

		from, to := s.offset+1, s.offset+len(items)
		ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).Children(func() {
			if v.Button(i18n.T("pages.literature.btn_prev"), components.ButtonOptions{
				Variant:  components.VariantGhost,
				Disabled: s.offset <= 0,
			}).Clicked() {
				l.movePage(a, -literaturePageSize)
			}
			if v.Button(i18n.T("pages.literature.btn_next"), components.ButtonOptions{
				Variant:  components.VariantGhost,
				Disabled: to >= s.total,
			}).Clicked() {
				l.movePage(a, literaturePageSize)
			}
			ui.Spacer(v.C)
			ui.Text(v.C, i18n.Tf("pages.literature.pager", map[string]string{
				"from": strconv.Itoa(from), "to": strconv.Itoa(to),
				"total": strconv.Itoa(s.total),
			})).FontSize(tokens.Typography.Secondary.Size)
		})

		ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).Children(func() {
			if v.Button(i18n.T("pages.literature.btn_preview"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Disabled: loading || l.sel.Len() == 0,
			}).Clicked() {
				l.openPreview(a)
			}
			ui.Text(v.C, i18n.Tf("pages.literature.selected_count", map[string]string{
				"count": strconv.Itoa(l.sel.Len()),
			})).FontSize(tokens.Typography.Secondary.Size)
			if len(chosen) > 0 {
				ui.Text(v.C, i18n.T("pages.literature.hint_choices_kept")).
					FontSize(tokens.Typography.Caption.Size)
			}
		})
	})
}

// linked lists the papers the project already holds, so the result of an
// import is visible without leaving the page.
func (l *Literature) linked(v *components.UI) {
	l.mu.Lock()
	papers := append([]literature.ProjectPaper(nil), l.state.papers...)
	l.mu.Unlock()

	title := i18n.T("pages.literature.card_project")
	if len(papers) == 0 {
		v.Card(components.CardOptions{Title: title}, func() {
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.FileText,
				Title:  i18n.T("pages.literature.project_empty_title"),
				Reason: i18n.T("pages.literature.project_empty_reason"),
			})
		})
		return
	}
	state := components.NewTableState()
	state.Key = func(i int) any { return papers[i].Paper.ID }
	v.Card(components.CardOptions{
		Title:       title,
		Description: i18n.Tf("pages.literature.card_project_desc", map[string]string{"count": strconv.Itoa(len(papers))}),
	}, func() {
		table := v.Table(components.TableOptions{
			Columns: []components.Column{
				{Title: i18n.T("pages.literature.col_title")},
				{Title: i18n.T("pages.literature.col_year"), Width: 80},
				{Title: i18n.T("pages.literature.col_imported"), Width: 160},
				{Title: i18n.T("pages.literature.col_copy"), Width: 120},
			},
			Rows: len(papers),
			Key:  func(i int) any { return papers[i].Paper.ID },
			Cell: func(row, col int) {
				p := papers[row]
				switch col {
				case 0:
					ui.Text(v.C, p.Paper.Metadata.Title).SingleLine()
				case 1:
					ui.Text(v.C, strconv.Itoa(p.Paper.Metadata.Year)).FontFeatures("tnum")
				case 2:
					ui.Text(v.C, p.LinkedAt.Format("2006-01-02")).FontFeatures("tnum")
				case 3:
					if p.Broken {
						v.StatusIndicator(components.Danger, i18n.T("pages.literature.copy_missing"))
					} else {
						v.StatusIndicator(components.Success, i18n.T("pages.literature.copy_present"))
					}
				}
			},
		})
		table.Height(visibleTableHeight(len(papers)))
	})
}

// dialog previews the import read-only and then runs it. The preview never
// copies a file or writes a row (SPEC-04 §5.4), so it can be reopened freely.
func (l *Literature) dialog(v *components.UI, a *app.App, projectID string) {
	if !l.previewOpen {
		return
	}
	l.mu.Lock()
	preview, loading := append([]literature.ImportPreviewEntry(nil), l.state.preview...), l.loading
	previewErrs := append([]literature.ImportError(nil), l.state.previewErrs...)
	l.mu.Unlock()

	v.Dialog(&l.previewOpen, components.DialogOptions{
		Title:       i18n.T("pages.literature.dialog_title"),
		Description: i18n.T("pages.literature.dialog_desc"),
		Width:       760,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				l.previewOpen = false
			}
			confirm := v.Button(i18n.T("pages.literature.btn_import"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Disabled: loading || len(preview) == 0,
			})
			if confirm.Clicked() {
				l.previewOpen = false
				l.startImport(a, projectID)
			}
		},
	}, func() {
		if len(preview) == 0 {
			if loading {
				v.Skeleton(components.SkeletonOptions{Lines: 4})
				return
			}
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.FileText,
				Title:  i18n.T("pages.literature.preview_empty_title"),
				Reason: i18n.T("pages.literature.preview_empty_reason"),
			})
			return
		}
		state := components.NewTableState()
		state.Key = func(i int) any { return preview[i].ZoteroKey }
		table := v.Table(components.TableOptions{
			Columns: []components.Column{
				{Title: i18n.T("pages.literature.col_title")},
				{Title: i18n.T("pages.literature.col_year"), Width: 70},
				{Title: i18n.T("pages.literature.col_pdf"), Width: 90, Align: ui.End},
				{Title: i18n.T("pages.literature.col_state"), Width: 150},
			},
			Rows:  len(preview),
			State: state,
			Key:   func(i int) any { return preview[i].ZoteroKey },
			Cell: func(row, col int) {
				e := preview[row]
				switch col {
				case 0:
					ui.Text(v.C, e.Title).SingleLine()
				case 1:
					ui.Text(v.C, strconv.Itoa(e.Year)).FontFeatures("tnum")
				case 2:
					ui.Text(v.C, strconv.Itoa(e.PDFCount)).FontFeatures("tnum")
				case 3:
					tone, label := entryState(e)
					v.Badge(label, tone)
				}
			},
		})
		table.Height(visibleTableHeight(len(preview)))
		l.attachmentChoices(v, preview)
		previewProblem(v, previewErrs)
	})
}

// previewProblem reports the entries Preview could not read: an import that
// silently drops one paper would hide a broken Zotero item (SPEC-04 §5.4).
func previewProblem(v *components.UI, errs []literature.ImportError) {
	if len(errs) == 0 {
		return
	}
	lines := make([]string, 0, len(errs))
	for _, e := range errs {
		lines = append(lines, "  "+e.ZoteroKey+": "+e.Message)
	}
	v.Banner(components.BannerOptions{
		Tone:    components.Warning,
		Title:   i18n.T("pages.literature.preview_errors"),
		Message: strings.Join(lines, "\n"),
	})
}

// attachmentChoices asks for a PDF when an item carries several: the service
// reports needs_attachment_choice rather than guessing (SPEC-04 §5.4).
func (l *Literature) attachmentChoices(v *components.UI, preview []literature.ImportPreviewEntry) {
	l.mu.Lock()
	choices := make(map[string][]literature.ZoteroAttachment, len(l.state.choices))
	for key, list := range l.state.choices {
		choices[key] = list
	}
	l.mu.Unlock()

	var ambiguous []literature.ImportPreviewEntry
	for _, e := range preview {
		if e.PDFCount > 1 && len(choices[e.ZoteroKey]) > 1 {
			ambiguous = append(ambiguous, e)
		}
	}
	if len(ambiguous) == 0 {
		return
	}
	ui.Column(v.C).Gap(tokens.ComponentGap).Children(func() {
		ui.Text(v.C, i18n.T("pages.literature.choices_title")).
			FontSize(tokens.Typography.Secondary.Size)
		for _, e := range ambiguous {
			attachments := choices[e.ZoteroKey]
			labels := make([]string, len(attachments))
			keys := make([]string, len(attachments))
			for i, att := range attachments {
				labels[i] = att.Filename
				keys[i] = att.Key
			}
			current := l.chosenKey(e.ZoteroKey)
			if current == "" {
				current = labels[0]
			}
			picker := v.Select(&current, components.SelectOptions{
				Label:   e.Title,
				Options: labels,
				Width:   420,
			})
			if picker.Changed() {
				l.setChosen(e.ZoteroKey, keyForLabel(current, labels, keys))
			}
		}
	})
}

// visibleTableHeight caps a list at litVisibleRows rows so the actions under it
// stay reachable; MyGo scrolls the remaining rows inside the table box.
func visibleTableHeight(rows int) float32 {
	visible := rows
	if visible > litVisibleRows {
		visible = litVisibleRows
	}
	return float32(visible)*44 + 40
}

// load runs the module load once per project; reload re-runs it after a
// detection change in the settings.
func (l *Literature) load(a *app.App, projectID string) {
	l.mu.Lock()
	if l.loading || (l.loaded && l.project == projectID) {
		l.mu.Unlock()
		return
	}
	l.mu.Unlock()
	l.reload(a, projectID)
}

func (l *Literature) reload(a *app.App, projectID string) {
	api := l.Services.Literature
	l.mu.Lock()
	if l.loading {
		l.mu.Unlock()
		return
	}
	first := l.project != projectID
	l.project = projectID
	l.loading = true
	l.loaded = false
	l.problems = nil
	if first {
		l.state.offset = 0
	}
	l.mu.Unlock()
	if first && l.sel != nil {
		l.sel.Clear()
	}

	a.RunTask(i18n.T("pages.literature.task_load"), func(ctx context.Context, t *app.Task) error {
		t.Stage(i18n.T("pages.literature.stage_items"))
		collections, collErr := api.Collections(ctx, "")
		l.record(collErr)
		l.mu.Lock()
		if collErr == nil {
			l.state.collections = collections
		}
		offset := l.state.offset
		if first {
			offset = 0
			l.state.offset = 0
		}
		l.mu.Unlock()
		page, itemsErr := api.Items(ctx, "", l.collectionKey(), literature.ListItemsOptions{
			Offset: offset,
			Limit:  literaturePageSize,
			Query:  l.queryText(),
		})
		l.record(itemsErr)
		l.mu.Lock()
		if itemsErr == nil {
			l.state.items, l.state.total = page.Items, page.Total
		}
		l.mu.Unlock()

		t.Stage(i18n.T("pages.literature.stage_project"))
		papers, papersErr := api.ProjectPapers(ctx, projectID)
		l.record(papersErr)
		l.mu.Lock()
		if papersErr == nil {
			l.state.papers = papers
			linked := make(map[string]bool, len(papers))
			for _, p := range papers {
				linked[p.Paper.ZoteroKey] = true
			}
			l.state.linked = linked
		}
		l.mu.Unlock()

		l.finish(true)
		return nil
	})
}

// loadItems re-reads only the browser page: the picker, the search text and the
// paging buttons change nothing else.
func (l *Literature) loadItems(a *app.App) {
	api := l.Services.Literature
	l.mu.Lock()
	if l.loading {
		l.mu.Unlock()
		return
	}
	l.loading = true
	l.mu.Unlock()

	a.RunTask(i18n.T("pages.literature.task_items"), func(ctx context.Context, t *app.Task) error {
		t.Stage(i18n.T("pages.literature.stage_items"))
		l.mu.Lock()
		offset, collection, query := l.state.offset, l.collection, l.query
		l.mu.Unlock()
		page, err := api.Items(ctx, "", collection, literature.ListItemsOptions{
			Offset: offset,
			Limit:  literaturePageSize,
			Query:  query,
		})
		l.record(err)
		l.mu.Lock()
		if err == nil {
			l.state.items, l.state.total = page.Items, page.Total
		}
		l.mu.Unlock()
		l.finish(err == nil)
		return nil
	})
}

// openPreview takes the selected item keys and previews them without writing.
func (l *Literature) openPreview(a *app.App) {
	keys := l.selectedKeys()
	if len(keys) == 0 {
		return
	}
	l.previewOpen = true
	api := l.Services.Literature
	l.mu.Lock()
	l.loading = true
	l.state.preview = nil
	l.state.previewErrs = nil
	l.state.choices = map[string][]literature.ZoteroAttachment{}
	l.state.result = nil
	l.mu.Unlock()

	a.RunTask(i18n.T("pages.literature.task_preview"), func(ctx context.Context, t *app.Task) error {
		t.Stage(i18n.T("pages.literature.stage_preview"))
		sel := l.selection(ctx, api, keys)
		preview, err := api.Preview(ctx, sel)
		l.record(err)
		if err != nil {
			l.finish(false)
			return nil
		}
		choices := map[string][]literature.ZoteroAttachment{}
		for _, entry := range preview.Entries {
			if entry.PDFCount <= 1 {
				continue
			}
			attachments, err := api.Attachments(ctx, entry.LibraryID, entry.ZoteroKey)
			if err != nil {
				l.record(err)
				continue
			}
			var pdfs []literature.ZoteroAttachment
			for _, att := range attachments {
				if isPDF(att) {
					pdfs = append(pdfs, att)
				}
			}
			if len(pdfs) > 1 {
				choices[entry.ZoteroKey] = pdfs
			}
		}
		l.mu.Lock()
		l.state.preview = preview.Entries
		l.state.previewErrs = preview.Errors
		l.state.choices = choices
		l.mu.Unlock()
		l.finish(true)
		return nil
	})
}

// selection builds the import selection from the chosen keys, honouring the
// per-item PDF choices the dialog collected.
func (l *Literature) selection(ctx context.Context, api LiteratureAPI, keys []string) literature.ImportSelection {
	l.mu.Lock()
	chosen := make(map[string]string, len(l.state.chosen))
	for key, attachment := range l.state.chosen {
		chosen[key] = attachment
	}
	l.mu.Unlock()
	return literature.ImportSelection{
		ProjectID:         l.project,
		LibraryID:         "",
		ItemKeys:          keys,
		AttachmentChoices: chosen,
	}
}

// startImport runs the pipeline. Progress reaches the task, so the shell draws
// it in the task strip while the import copies PDFs.
func (l *Literature) startImport(a *app.App, projectID string) {
	api := l.Services.Literature
	l.mu.Lock()
	keys := make([]string, 0, len(l.state.preview))
	for _, entry := range l.state.preview {
		keys = append(keys, entry.ZoteroKey)
	}
	chosen := make(map[string]string, len(l.state.chosen))
	for key, attachment := range l.state.chosen {
		chosen[key] = attachment
	}
	l.loading = true
	l.state.result = nil
	l.mu.Unlock()
	if len(keys) == 0 {
		l.finish(true)
		return
	}

	sel := literature.ImportSelection{
		ProjectID:         projectID,
		ItemKeys:          keys,
		AttachmentChoices: chosen,
	}
	a.RunTask(i18n.T("pages.literature.task_import"), func(ctx context.Context, t *app.Task) error {
		result, err := api.Import(ctx, sel, func(p literature.ImportProgress) {
			t.Progress(p.Index, p.Total)
		})
		l.record(err)
		l.mu.Lock()
		if err == nil || result.Imported+result.Linked+result.Skipped+result.Failed > 0 {
			l.state.result = &result
		}
		l.mu.Unlock()

		// The project list is only refreshed when the import really ran, so a
		// failure leaves the previous rows on screen.
		if err == nil {
			papers, papersErr := api.ProjectPapers(ctx, projectID)
			l.record(papersErr)
			l.mu.Lock()
			if papersErr == nil {
				l.state.papers = papers
				linked := make(map[string]bool, len(papers))
				for _, p := range papers {
					linked[p.Paper.ZoteroKey] = true
				}
				l.state.linked = linked
			}
			l.state.preview = nil
			l.mu.Unlock()
			l.sel.Clear()
		}
		l.finish(err == nil)
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.literature.toast_import_fail"), errtext.Text(err))
		}
		return nil
	})
}

// movePage shifts the browser by one page and reloads the rows.
func (l *Literature) movePage(a *app.App, delta int) {
	l.mu.Lock()
	next := l.state.offset + delta
	if next < 0 {
		next = 0
	}
	if total := l.state.total; total > 0 && next >= total {
		next = (total - 1) / literaturePageSize * literaturePageSize
	}
	l.state.offset = next
	l.mu.Unlock()
	l.loadItems(a)
}

// setCollection stores the chosen collection key and restarts the paging.
func (l *Literature) setCollection(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.collection = key
	l.state.offset = 0
}

// record appends a service failure to the banner list.
func (l *Literature) record(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		l.problems = append(l.problems, errtext.Text(err))
	}
}

// finish clears the loading flag and asks for a repaint.
func (l *Literature) finish(loaded bool) {
	l.mu.Lock()
	l.loading = false
	l.loaded = loaded
	l.mu.Unlock()
	if l.Services.App != nil {
		l.Services.App.Notify()
	}
}

// selectedKeys returns the chosen Zotero item keys, sorted so a rerun of the
// same selection previews in the same order.
func (l *Literature) selectedKeys() []string {
	keys := make([]string, 0, l.sel.Len())
	for key := range l.sel.All() {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// previewChosen reports whether any PDF choice was made, so the browser can
// hint that the choices carry over into the next preview.
func (l *Literature) previewChosen() map[string]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state.chosen
}

func (l *Literature) chosenKey(itemKey string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state.chosen[itemKey]
}

func (l *Literature) setChosen(itemKey, attachmentKey string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state.chosen == nil {
		l.state.chosen = map[string]string{}
	}
	l.state.chosen[itemKey] = attachmentKey
}

func (l *Literature) collectionKey() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.collection
}

func (l *Literature) queryText() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.query
}

// collectionLabels pairs the picker entries with their keys; the first entry is
// the whole library, so an empty selection needs no special case in the page.
func collectionLabels(collections []literature.ZoteroCollection) (names, keys []string) {
	names = append(names, i18n.T("pages.literature.collection_all"))
	keys = append(keys, "")
	counts := map[string]int{}
	for _, c := range collections {
		counts[c.Name]++
	}
	seen := map[string]int{}
	for _, c := range collections {
		name := c.Name
		if counts[name] > 1 {
			seen[name]++
			name = fmt.Sprintf("%s (%s)", name, c.Key)
		}
		names = append(names, name)
		keys = append(keys, c.Key)
	}
	return names, keys
}

// labelForKey is the picker entry of a collection key; an unknown or empty key
// is the whole library, which is the first entry.
func labelForKey(key string, names, keys []string) string {
	for i, k := range keys {
		if k == key {
			return names[i]
		}
	}
	return names[0]
}

// keyForLabel maps a picker label back to its option key.
func keyForLabel(label string, labels, keys []string) string {
	for i, name := range labels {
		if name == label {
			return keys[i]
		}
	}
	return ""
}

// itemDisplayTitle falls back to the type when Zotero has no title.
func itemDisplayTitle(item literature.ZoteroItem) string {
	if item.Title != "" {
		return item.Title
	}
	return item.ItemType
}

// entryState labels what the import would do with one preview entry, so the
// user sees reuse and missing-PDF cases before confirming.
func entryState(entry literature.ImportPreviewEntry) (components.Tone, string) {
	switch {
	case !entry.PDFAvailable:
		return components.Warning, i18n.T("pages.literature.state_no_pdf")
	case entry.PDFCount > 1 && entry.AttachmentKey == "":
		return components.Warning, i18n.T("pages.literature.state_needs_choice")
	case entry.ExistingPaperID != "" && entry.HashEqual:
		return components.Success, i18n.T("pages.literature.state_reuse")
	case entry.ExistingPaperID != "":
		return components.Info, i18n.T("pages.literature.state_existing")
	default:
		return components.Neutral, i18n.T("pages.literature.state_new")
	}
}

func isPDF(att literature.ZoteroAttachment) bool {
	return att.ContentType == "application/pdf" || strings.HasSuffix(strings.ToLower(att.Filename), ".pdf")
}
