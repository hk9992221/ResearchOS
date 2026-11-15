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
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// literaturePageSize is the browser page size: one screenful of table rows,
// so 上一页/下一页 walk the library instead of sitting disabled while a
// 100-item page holds the whole collection. The Zotero reader accepts up to
// 100 items per call (SPEC-04 §5.2); a page of ten stays well inside it.
const literaturePageSize = litVisibleRows

// litVisibleRows is how many table rows fit before the list scrolls inside its
// own box (design system row height 44px).
const litVisibleRows = 10

// litCheckWidth is the column of the row check boxes: one square control plus
// the cell padding of the design system.
const litCheckWidth float32 = 44

// literatureShared is the generation the paper page bumps after it writes a
// Paper Note or links a research note: the project table then reloads its note
// badge instead of showing the row it kept from before the write.
type literatureShared struct {
	mu  sync.Mutex
	gen int
}

// bump marks the literature state as stale.
func (s *literatureShared) bump() {
	s.mu.Lock()
	s.gen++
	s.mu.Unlock()
}

// version reports the current generation.
func (s *literatureShared) version() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

// Literature renders the page.
type Literature struct {
	Services *Services
	shared   *literatureShared

	mu       sync.Mutex
	loading  bool
	loaded   bool
	project  string
	version  int
	state    litState
	problems []string

	// importOpen folds the Zotero browser: the page reads the project library
	// only until the import is expanded, because reading Zotero is the slow
	// part of the page. zoteroLoaded records that the browser data was read
	// once, so folding and expanding again costs nothing, and zloading says a
	// browser read is in flight.
	importOpen   bool
	zoteroLoaded bool
	zloading     bool

	// These are written while a frame is built only: the collection name shown
	// in the picker, the search text, the chosen item keys and the open flag of
	// the preview dialog.
	collection  string
	query       string
	previewOpen bool
	sel         *ui.Selection[string]

	// includeSub widens a collection import to its subcollections, and pending
	// is the selection the preview dialog confirms: checked item keys, or the
	// collection keys of a whole-collection import, which the import then
	// expands again so it stays one request.
	includeSub bool
	pending    literature.ImportSelection

	// resultOpen shows the receipt of the last import: a counts line alone
	// would hide which entry failed and why.
	resultOpen bool

	// paperSel chooses rows of the project table for a batch delete; the delete
	// dialog is the confirmation both the row action and the batch share.
	paperSel *ui.Selection[string]
	delOpen  bool
	delIDs   []string
	delTitle string
	delNotes int
	delBusy  bool
	delErr   string
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
	// titles labels a preview entry and its outcome by Zotero key: the result
	// receipt names the paper the user imported, not its key.
	titles map[string]string
	result *literature.ImportResult
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
	if l.paperSel == nil {
		l.paperSel = components.NewSelection[string]()
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
	open := l.importShown()
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.literature.title"),
			Description: i18n.Tf("pages.literature.description", map[string]string{"project": project.Name}),
			Actions: func() {
				label, icon := i18n.T("pages.literature.btn_expand_import"), icons.ChevronRight
				if open {
					label, icon = i18n.T("pages.literature.btn_collapse_import"), icons.ChevronDown
				}
				if v.Button(label, components.ButtonOptions{
					Variant: components.VariantGhost,
					Icon:    icon,
				}).Clicked() {
					l.toggleImport(a)
				}
				if v.Button(i18n.T("pages.literature.btn_refresh"), components.ButtonOptions{
					Variant: components.VariantGhost,
					Icon:    icons.Refresh,
				}).Clicked() {
					l.reload(a, project.ID)
				}
			},
		})
		l.banners(v)
		if open {
			l.browser(v, a)
		}
		l.linked(v, a)
		if open {
			l.dialog(v, a, project.ID)
		}
		l.resultDialog(v)
		l.deleteDialog(v, a, project.ID)
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

// banners reports load failures and the items the last import could not bring
// in: the receipt dialog shows every status, so the banner only keeps the
// failure that must not scroll away.
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
	if result == nil || result.Failed == 0 {
		return
	}
	v.Banner(components.BannerOptions{
		Tone: components.Warning,
		Title: i18n.Tf("pages.literature.result_failed_title", map[string]string{
			"failed": strconv.Itoa(result.Failed),
		}),
		Message: strings.TrimSpace(failuresText(result)),
	})
}

// failuresText lists the per-item failures; an import that dropped one PDF must
// not hide why (SPEC-04 §5.4: outcomes are per entry).
func failuresText(result *literature.ImportResult) string {
	var lines []string
	for _, outcome := range result.Outcomes {
		if outcome.Status != literature.ImportFailed {
			continue
		}
		lines = append(lines, outcome.ZoteroKey+": "+outcome.Error)
	}
	return strings.Join(lines, "\n")
}

// browser is the Zotero picker: collection, search, paging and the checked
// items that feed the preview.
func (l *Literature) browser(v *components.UI, a *app.App) {
	l.mu.Lock()
	s := l.state
	loaded, busy := l.zoteroLoaded, l.loading || l.zloading
	l.mu.Unlock()

	names, keys := collectionLabels(s.collections)
	selector := labelForKey(l.collection, names, keys)

	v.Card(components.CardOptions{
		Title:       i18n.T("pages.literature.card_items"),
		Description: i18n.T("pages.literature.card_items_desc"),
	}, func() {
		// The panel starts as a skeleton until the Zotero read lands: an empty
		// table would claim the library has no items.
		if !loaded {
			v.Skeleton(components.SkeletonOptions{Lines: 5})
			return
		}
		ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).AlignItems(ui.End).Children(func() {
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
				Compact:     true,
			}).Width(components.SearchFieldWidth)
			if searchField.Submitted() {
				l.loadItems(a)
			}
			if v.Button(i18n.T("pages.literature.btn_search"), components.ButtonOptions{
				Variant: components.VariantSecondary,
				Align:   ui.End,
			}).Clicked() {
				l.loadItems(a)
			}
		})

		// A collection import expands the collection in the service, so its
		// subcollections are a choice of that action, not of the picker.
		if l.collection != "" {
			sub := l.includeSub
			if v.Checkbox(&sub, i18n.T("pages.literature.field_include_sub"), components.ToggleOptions{}).Changed() {
				l.includeSub = sub
				a.Notify()
			}
		}

		if len(s.items) == 0 {
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.Literature,
				Title:  i18n.T("pages.literature.items_empty_title"),
				Reason: i18n.T("pages.literature.items_empty_reason"),
			})
			return
		}

		items, linked := s.items, s.linked
		state := components.NewTableState()
		state.Key = func(i int) any { return items[i].Key }
		table := v.Table(components.TableOptions{
			Columns: []components.Column{
				{Title: "", ID: "select", Width: litCheckWidth, Align: ui.Center},
				{Title: i18n.T("pages.literature.col_title")},
				{Title: i18n.T("pages.literature.col_type"), Width: 140},
				{Title: i18n.T("pages.literature.col_date"), Width: 100},
				{Title: i18n.T("pages.literature.col_state"), Width: 110},
			},
			Rows:      len(items),
			State:     state,
			Key:       func(i int) any { return items[i].Key },
			Selection: l.sel,
			HeaderCell: func(col int) {
				if col != 0 {
					return
				}
				l.selectPageCheckbox(v, a, items)
			},
			Cell: func(row, col int) {
				item := items[row]
				switch col {
				case 0:
					checked := l.sel.Has(item.Key)
					box := ui.Checkbox(v.C, &checked, "").Label(itemDisplayTitle(item))
					if box.Changed() {
						l.setChecked(item.Key, checked)
						a.Notify()
					}
				case 1:
					ui.Text(v.C, itemDisplayTitle(item)).SingleLine()
				case 2:
					ui.Text(v.C, item.ItemType).SingleLine()
				case 3:
					ui.Text(v.C, item.Date).SingleLine()
				case 4:
					if linked[item.Key] {
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
			count := l.sel.Len()
			if v.Button(i18n.Tf("pages.literature.btn_import_selected", map[string]string{
				"count": strconv.Itoa(count),
			}), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Disabled: busy || count == 0,
			}).Clicked() {
				l.openPreview(a)
			}
			if l.collection != "" {
				if v.Button(i18n.T("pages.literature.btn_import_collection"), components.ButtonOptions{
					Variant:  components.VariantSecondary,
					Disabled: busy,
				}).Clicked() {
					l.openCollectionPreview(a)
				}
			}
			if count > 0 {
				if v.Button(i18n.T("pages.literature.btn_clear_selection"), components.ButtonOptions{
					Variant: components.VariantGhost,
				}).Clicked() {
					l.sel.Clear()
					a.Notify()
				}
			}
		})
	})
}

// selectPageCheckbox is the check box of the browser's header: it chooses every
// row on the page, or clears them when the whole page is already chosen.
func (l *Literature) selectPageCheckbox(v *components.UI, a *app.App, items []literature.ZoteroItem) {
	state := ui.CheckOff
	chosen := 0
	for _, item := range items {
		if l.sel.Has(item.Key) {
			chosen++
		}
	}
	switch {
	case chosen == len(items) && chosen > 0:
		state = ui.CheckOn
	case chosen > 0:
		state = ui.CheckMixed
	}
	box := pageCheckBox(v, &state)
	if !box.Changed() {
		return
	}
	for _, item := range items {
		l.setChecked(item.Key, state == ui.CheckOn)
	}
	a.Notify()
}

// pageCheckBox is the header check box of a batch table: MyGo's base control
// has no look of its own, so the page draws the square, a tick when every row
// is chosen and a dash while only some are.
func pageCheckBox(v *components.UI, state *ui.CheckState) ui.Element {
	p := v.T.Palette
	box := ui.CheckboxStateBase(v.C, state).Label(i18n.T("pages.literature.select_all_page"))
	box.Children(func() {
		square := ui.Row(v.C).
			Width(tokens.Space4).
			Height(tokens.Space4).
			Radius(tokens.RadiusSM).
			Justify(ui.Center).
			AlignItems(ui.Center)
		if *state == ui.CheckOff {
			square.Border(1, mygo.Color(p.BorderStrong))
		} else {
			square.Background(mygo.Color(p.AccentDefault)).Border(1, mygo.Color(p.AccentDefault))
		}
		square.Children(func() {
			switch *state {
			case ui.CheckOn:
				v.Icon(icons.Check, tokens.Space3).TextColor(mygo.Color(p.BgSurface))
			case ui.CheckMixed:
				ui.Row(v.C).Width(tokens.Space2).Height(2).Background(mygo.Color(p.BgSurface))
			}
		})
	})
	return box
}

// setChecked adds or removes one key from the browser selection.
func (l *Literature) setChecked(key string, checked bool) {
	if checked {
		l.sel.Add(key)
		return
	}
	l.sel.Remove(key)
}

// linked lists the papers the project already holds, each removable from the
// project without touching its copy or its Paper Note.
func (l *Literature) linked(v *components.UI, a *app.App) {
	l.mu.Lock()
	papers := append([]literature.ProjectPaper(nil), l.state.papers...)
	l.mu.Unlock()

	open := func(paperID string) {
		a.Navigate(ports.Route{Kind: ports.RoutePaper, EntityID: paperID})
	}
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
	count := l.paperSel.Len()
	v.Card(components.CardOptions{
		Title:       title,
		Description: i18n.Tf("pages.literature.card_project_desc", map[string]string{"count": strconv.Itoa(len(papers))}),
		Actions: func() {
			all := ui.CheckOff
			switch {
			case count == len(papers) && count > 0:
				all = ui.CheckOn
			case count > 0:
				all = ui.CheckMixed
			}
			if box := pageCheckBox(v, &all); box.Changed() {
				for _, p := range papers {
					if all == ui.CheckOn {
						l.paperSel.Add(p.Paper.ID)
					} else {
						l.paperSel.Remove(p.Paper.ID)
					}
				}
				a.Notify()
			}
			if count == 0 {
				return
			}
			if v.Button(i18n.Tf("pages.literature.btn_delete_selected", map[string]string{
				"count": strconv.Itoa(count),
			}), components.ButtonOptions{
				Variant:  components.VariantDanger,
				Size:     components.SizeSmall,
				Icon:     icons.Trash,
				Disabled: l.delBusy,
			}).Clicked() {
				l.requestDelete(papers, nil)
			}
		},
	}, func() {
		state := components.NewTableState()
		state.Key = func(i int) any { return papers[i].Paper.ID }
		table := v.Table(components.TableOptions{
			Columns: []components.Column{
				{Title: "", ID: "select", Width: litCheckWidth, Align: ui.Center},
				{Title: i18n.T("pages.literature.col_title")},
				{Title: i18n.T("pages.literature.col_year"), Width: 80},
				{Title: i18n.T("pages.literature.col_imported"), Width: 160},
				{Title: i18n.T("pages.literature.col_copy"), Width: 120},
				{Title: i18n.T("pages.literature.col_note"), Width: 120},
				{Title: "", ID: "actions", Width: 96, Align: ui.End},
			},
			Rows:      len(papers),
			Key:       func(i int) any { return papers[i].Paper.ID },
			Selection: l.paperSel,
			Cell: func(row, col int) {
				p := papers[row]
				switch col {
				case 0:
					checked := l.paperSel.Has(p.Paper.ID)
					box := ui.Checkbox(v.C, &checked, "").Label(p.Paper.Metadata.Title)
					if box.Changed() {
						if checked {
							l.paperSel.Add(p.Paper.ID)
						} else {
							l.paperSel.Remove(p.Paper.ID)
						}
						a.Notify()
					}
				case 1:
					if ui.Text(v.C, p.Paper.Metadata.Title).SingleLine().FillWidth().RowClicked() {
						open(p.Paper.ID)
					}
				case 2:
					if ui.Text(v.C, strconv.Itoa(p.Paper.Metadata.Year)).FontFeatures("tnum").RowClicked() {
						open(p.Paper.ID)
					}
				case 3:
					if ui.Text(v.C, p.LinkedAt.Format("2006-01-02")).FontFeatures("tnum").RowClicked() {
						open(p.Paper.ID)
					}
				case 4:
					if p.Broken {
						v.StatusIndicator(components.Danger, i18n.T("pages.literature.copy_missing"))
					} else {
						v.StatusIndicator(components.Success, i18n.T("pages.literature.copy_present"))
					}
				case 5:
					if p.NoteID != "" {
						v.Badge(i18n.T("pages.literature.note_present"), components.Success)
					} else {
						v.Badge(i18n.T("pages.literature.note_absent"), components.Neutral)
					}
				case 6:
					ui.Row(v.C).Gap(tokens.Space1).AlignItems(ui.Center).Children(func() {
						if v.IconButton(icons.ExternalLink, i18n.T("pages.literature.btn_open_paper"), components.IconButtonOptions{
							Variant: components.VariantGhost,
							Size:    components.SizeSmall,
						}).Clicked() {
							open(p.Paper.ID)
						}
						if v.IconButton(icons.Trash, i18n.T("pages.literature.btn_unlink"), components.IconButtonOptions{
							Variant:  components.VariantGhost,
							Size:     components.SizeSmall,
							Disabled: l.delBusy,
						}).Clicked() {
							l.requestDelete(papers, []string{p.Paper.ID})
						}
					})
				}
			},
		})
		table.Height(visibleTableHeight(len(papers)))
	})
}

// requestDelete opens the confirmation for the chosen papers: ids nil means
// the rows checked in the table, a list names exactly those. The dialog states
// that the Paper Notes stay, and how many of the papers hold one.
func (l *Literature) requestDelete(papers []literature.ProjectPaper, ids []string) {
	if ids == nil {
		ids = make([]string, 0, l.paperSel.Len())
		for _, p := range papers {
			if l.paperSel.Has(p.Paper.ID) {
				ids = append(ids, p.Paper.ID)
			}
		}
	}
	if len(ids) == 0 {
		return
	}
	title, notes := "", 0
	for _, p := range papers {
		for _, id := range ids {
			if p.Paper.ID != id {
				continue
			}
			title = p.Paper.Metadata.Title
			if p.NoteID != "" {
				notes++
			}
		}
	}
	l.mu.Lock()
	l.delOpen = true
	l.delIDs = ids
	l.delTitle = title
	l.delNotes = notes
	l.delErr = ""
	l.mu.Unlock()
}

// deleteDialog confirms detaching the papers from the project. The copies, the
// PDFs and the Paper Notes stay (SPEC-04 §5.5), so the dialog says so before
// the service runs rather than reporting it afterwards.
func (l *Literature) deleteDialog(v *components.UI, a *app.App, projectID string) {
	l.mu.Lock()
	open, ids := l.delOpen, len(l.delIDs)
	title, notes, busy, problem := l.delTitle, l.delNotes, l.delBusy, l.delErr
	l.mu.Unlock()
	if !open {
		return
	}
	message := i18n.Tf("pages.literature.delete_message", map[string]string{"title": title})
	if ids > 1 {
		message = i18n.Tf("pages.literature.delete_batch_message", map[string]string{
			"count": strconv.Itoa(ids),
		})
	}
	if notes > 0 {
		message += "\n" + i18n.Tf("pages.literature.delete_notes_kept", map[string]string{
			"count": strconv.Itoa(notes),
		})
	}
	show := true
	v.Dialog(&show, components.DialogOptions{
		Title:       i18n.T("pages.literature.delete_title"),
		Description: message,
		Width:       520,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				l.closeDelete()
			}
			if v.Button(i18n.T("pages.literature.btn_delete_confirm"), components.ButtonOptions{
				Variant:  components.VariantDanger,
				Loading:  busy,
				Disabled: busy,
			}).Clicked() {
				l.runDelete(a, projectID)
			}
		},
	}, func() {
		if problem != "" {
			ui.Text(v.C, problem).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
	if !show && !busy {
		// Escape or a press outside closed the dialog.
		l.closeDelete()
	}
}

// closeDelete drops the pending confirmation.
func (l *Literature) closeDelete() {
	l.mu.Lock()
	l.delOpen = false
	l.delIDs = nil
	l.delTitle = ""
	l.delNotes = 0
	l.delErr = ""
	l.mu.Unlock()
}

// runDelete detaches every chosen paper, one service call each so one failure
// never hides the others, and reloads the project table.
func (l *Literature) runDelete(a *app.App, projectID string) {
	api := l.Services.Literature
	l.mu.Lock()
	ids := append([]string(nil), l.delIDs...)
	if l.delBusy || len(ids) == 0 {
		l.mu.Unlock()
		return
	}
	l.delBusy = true
	l.delErr = ""
	l.mu.Unlock()

	a.RunTask(i18n.T("pages.literature.task_delete"), func(ctx context.Context, t *app.Task) error {
		t.Stage(i18n.T("pages.literature.stage_delete"))
		var removed, detached int
		var problems []string
		for i, id := range ids {
			if err := ctx.Err(); err != nil {
				break
			}
			t.Progress(i, len(ids))
			result, err := api.Unlink(ctx, projectID, id, ports.ActorUser)
			if err != nil {
				problems = append(problems, errtext.Text(err))
				continue
			}
			removed++
			detached += len(result.DetachedNotes)
		}
		t.Progress(len(ids), len(ids))

		papers, papersErr := api.ProjectPapers(ctx, projectID)
		if papersErr != nil {
			problems = append(problems, errtext.Text(papersErr))
		}

		l.mu.Lock()
		l.delBusy = false
		l.mu.Unlock()
		if papersErr == nil {
			l.setPapers(papers)
		} else {
			l.dropPapers(ids)
		}
		l.paperSel.Clear()
		if len(problems) > 0 {
			l.mu.Lock()
			l.delErr = problems[0]
			l.mu.Unlock()
		}
		if removed > 0 {
			l.closeDelete()
			if l.shared != nil {
				l.shared.bump()
			}
		}
		if l.Services.App != nil {
			l.Services.App.Notify()
		}
		switch {
		case removed == 0:
			a.Toast(app.LevelDanger, i18n.T("pages.literature.toast_delete_fail"), strings.Join(problems, "\n"))
		case len(problems) > 0:
			a.Toast(app.LevelWarning, i18n.Tf("pages.literature.toast_delete_partial", map[string]string{
				"failed": strconv.Itoa(len(problems)),
			}), strings.Join(problems, "\n"))
		default:
			message := ""
			if detached > 0 {
				message = i18n.Tf("pages.literature.toast_notes_kept", map[string]string{
					"count": strconv.Itoa(detached),
				})
			}
			a.Toast(app.LevelSuccess, i18n.Tf("pages.literature.toast_deleted", map[string]string{
				"count": strconv.Itoa(removed),
			}), message)
		}
		return nil
	})
}

// hasPaperID reports whether ids holds id.
func hasPaperID(ids []string, id string) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

// dropPapers removes the detached papers from the cached table when the reload
// after a delete failed: rows that are gone must not stay clickable.
func (l *Literature) dropPapers(ids []string) {
	l.mu.Lock()
	kept := make([]literature.ProjectPaper, 0, len(l.state.papers))
	for _, p := range l.state.papers {
		if !hasPaperID(ids, p.Paper.ID) {
			kept = append(kept, p)
		}
	}
	l.mu.Unlock()
	l.setPapers(kept)
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
	linked := make(map[string]bool, len(l.state.linked))
	for key, in := range l.state.linked {
		linked[key] = in
	}
	l.mu.Unlock()

	v.Dialog(&l.previewOpen, components.DialogOptions{
		Title:       i18n.T("pages.literature.dialog_title"),
		Description: i18n.T("pages.literature.dialog_desc"),
		Width:       820,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				l.previewOpen = false
			}
			if v.Button(i18n.Tf("pages.literature.btn_import_count", map[string]string{
				"count": strconv.Itoa(len(preview)),
			}), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Disabled: loading || len(preview) == 0,
				Loading:  loading,
			}).Clicked() {
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
				{Title: i18n.T("pages.literature.col_creators"), Width: 180},
				{Title: i18n.T("pages.literature.col_year"), Width: 60, Align: ui.End},
				{Title: i18n.T("pages.literature.col_pdf"), Width: 60, Align: ui.End},
				{Title: i18n.T("pages.literature.col_state"), Width: 220},
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
					ui.Text(v.C, creatorsText(e.Creators)).SingleLine()
				case 2:
					ui.Text(v.C, strconv.Itoa(e.Year)).FontFeatures("tnum")
				case 3:
					ui.Text(v.C, strconv.Itoa(e.PDFCount)).FontFeatures("tnum")
				case 4:
					ui.Row(v.C).Gap(tokens.Space1).AlignItems(ui.Center).Children(func() {
						tone, label := entryState(linked[e.ZoteroKey], e)
						v.Badge(label, tone)
						for _, warning := range e.Warnings {
							v.Badge(warningText(warning), components.Warning)
						}
					})
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
	linked := make(map[string]bool, len(l.state.linked))
	for key, in := range l.state.linked {
		linked[key] = in
	}
	l.mu.Unlock()

	var ambiguous []literature.ImportPreviewEntry
	for _, e := range preview {
		// An entry the project already holds is linked, not copied, so no PDF
		// has to be chosen for it.
		if linked[e.ZoteroKey] {
			continue
		}
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
// detection change in the settings or a refresh.
func (l *Literature) load(a *app.App, projectID string) {
	l.mu.Lock()
	need := !l.loading && (!l.loaded || l.project != projectID)
	if !need && l.shared != nil && l.version != l.shared.version() {
		need = true
	}
	l.mu.Unlock()
	if need {
		l.reload(a, projectID)
	}
}

// reload reads the project papers, and the Zotero browser too while the import
// panel is expanded: a folded page never touches Zotero, which is the slow read
// that made the page stall on a large library.
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
		// A fresh project opens folded: the browser cache and the preview
		// belong to the previous project, so the panel starts over.
		l.importOpen = false
		l.previewOpen = false
		l.state.offset = 0
		l.state.preview = nil
		l.state.previewErrs = nil
		l.state.titles = nil
		l.pending = literature.ImportSelection{}
	}
	withBrowser := l.importOpen && !l.zloading
	if withBrowser {
		l.zloading = true
	}
	l.mu.Unlock()
	if first && l.sel != nil {
		l.sel.Clear()
	}
	if first && l.paperSel != nil {
		l.paperSel.Clear()
	}
	gen := 0
	if l.shared != nil {
		gen = l.shared.version()
	}

	a.RunTask(i18n.T("pages.literature.task_load"), func(ctx context.Context, t *app.Task) error {
		if withBrowser {
			l.readBrowser(ctx, t, api, true)
		}

		t.Stage(i18n.T("pages.literature.stage_project"))
		papers, err := api.ProjectPapers(ctx, projectID)
		l.record(err)
		if err == nil {
			l.setPapers(papers)
		}
		if l.shared != nil {
			l.mu.Lock()
			l.version = gen
			l.mu.Unlock()
		}

		l.finish(true)
		if withBrowser {
			l.endBrowser()
		}
		return nil
	})
}

// loadItems re-reads only the browser page: the collection, the search text and
// the paging buttons change nothing else.
func (l *Literature) loadItems(a *app.App) {
	l.loadBrowser(a, false)
}

// toggleImport flips the import panel: expanding reads the browser once, while
// folding keeps its rows for the next expand, so the Zotero read is not paid
// again, and closes the preview dialog.
func (l *Literature) toggleImport(a *app.App) {
	l.mu.Lock()
	l.importOpen = !l.importOpen
	open := l.importOpen
	if !open {
		l.previewOpen = false
	}
	read := open && !l.zoteroLoaded
	l.mu.Unlock()
	if read {
		l.loadBrowser(a, true)
	}
}

// loadBrowser reads the Zotero side of the panel in a managed task: the frame
// only renders the cache, so a large library never blocks it. withCollections
// is true on the first expand and on a refresh, false while the rows alone
// change.
func (l *Literature) loadBrowser(a *app.App, withCollections bool) {
	api := l.Services.Literature
	l.mu.Lock()
	if l.zloading {
		l.mu.Unlock()
		return
	}
	l.zloading = true
	l.mu.Unlock()

	a.RunTask(i18n.T("pages.literature.task_items"), func(ctx context.Context, t *app.Task) error {
		l.readBrowser(ctx, t, api, withCollections)
		l.endBrowser()
		return nil
	})
}

// readBrowser reads the collections and the current page of items. Both callers
// run it inside a task, so no Zotero read happens on the frame thread.
func (l *Literature) readBrowser(ctx context.Context, t *app.Task, api LiteratureAPI, withCollections bool) {
	t.Stage(i18n.T("pages.literature.stage_items"))
	if withCollections {
		collections, err := api.Collections(ctx, "")
		l.record(err)
		if err == nil {
			l.mu.Lock()
			l.state.collections = collections
			l.mu.Unlock()
		}
	}
	l.readItems(ctx, api)
}

// readItems reads the current browser page into the frame state; a failed read
// keeps the rows already on screen.
func (l *Literature) readItems(ctx context.Context, api LiteratureAPI) {
	l.mu.Lock()
	offset, collection, query := l.state.offset, l.collection, l.query
	l.mu.Unlock()

	page, err := api.Items(ctx, "", collection, literature.ListItemsOptions{
		Offset: offset,
		Limit:  literaturePageSize,
		Query:  query,
	})
	l.record(err)
	if err != nil {
		return
	}
	l.mu.Lock()
	l.state.items, l.state.total = page.Items, page.Total
	l.mu.Unlock()
}

// endBrowser clears the browser loading flag; a failed read still marks the
// panel loaded, so it shows its empty state and the problem banner instead of a
// skeleton, and a refresh retries.
func (l *Literature) endBrowser() {
	l.mu.Lock()
	l.zloading = false
	l.zoteroLoaded = true
	l.mu.Unlock()
	if l.Services.App != nil {
		l.Services.App.Notify()
	}
}

// importShown reports whether the import panel is expanded.
func (l *Literature) importShown() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.importOpen
}

// setPapers stores the project list and rebuilds the "In project" map the
// browser badges read; the map is empty when nothing was imported, and a nil
// map read only reports false.
func (l *Literature) setPapers(papers []literature.ProjectPaper) {
	linked := make(map[string]bool, len(papers))
	for _, p := range papers {
		if key := p.Paper.ZoteroKey; key != "" {
			linked[key] = true
		}
	}
	l.mu.Lock()
	l.state.papers = papers
	l.state.linked = linked
	l.mu.Unlock()
}

// openPreview previews the checked items. The dialog belongs to the expanded
// panel, so a folded page can never open it.
func (l *Literature) openPreview(a *app.App) {
	if !l.importShown() {
		return
	}
	keys := l.selectedKeys()
	if len(keys) == 0 {
		return
	}
	l.startPreview(a, literature.ImportSelection{ItemKeys: keys})
}

// openCollectionPreview previews every item of the chosen collection: the
// service expands it, so a collection with more items than the browser page
// imports whole.
func (l *Literature) openCollectionPreview(a *app.App) {
	l.mu.Lock()
	key, sub := l.collection, l.includeSub
	l.mu.Unlock()
	if !l.importShown() || key == "" {
		return
	}
	l.startPreview(a, literature.ImportSelection{
		CollectionKeys:        []string{key},
		IncludeSubcollections: sub,
	})
}

// startPreview reads what the selection would do without writing, then leaves
// the confirmation to the dialog.
func (l *Literature) startPreview(a *app.App, sel literature.ImportSelection) {
	api := l.Services.Literature
	l.previewOpen = true
	l.mu.Lock()
	sel.ProjectID = l.project
	l.pending = sel
	l.loading = true
	l.state.preview = nil
	l.state.previewErrs = nil
	l.state.choices = map[string][]literature.ZoteroAttachment{}
	l.state.titles = map[string]string{}
	l.state.result = nil
	l.mu.Unlock()

	a.RunTask(i18n.T("pages.literature.task_preview"), func(ctx context.Context, t *app.Task) error {
		t.Stage(i18n.T("pages.literature.stage_preview"))
		preview, err := api.Preview(ctx, sel)
		l.record(err)
		if err != nil {
			l.finish(false)
			return nil
		}
		// An item with several PDFs needs a choice; the service never guesses
		// (SPEC-04 §5.4), so the dialog asks with the real filenames.
		choices := map[string][]literature.ZoteroAttachment{}
		titles := make(map[string]string, len(preview.Entries))
		for _, entry := range preview.Entries {
			titles[entry.ZoteroKey] = entry.Title
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
		l.state.titles = titles
		l.mu.Unlock()
		l.finish(true)
		return nil
	})
}

// startImport runs the pending selection, with or without a preceding preview:
// duplicates of existing papers are deduplicated by the service (linked or
// skipped), never copied twice. Progress reaches the task, so the shell draws
// the entry being written in the task strip while the import copies PDFs.
func (l *Literature) startImport(a *app.App, projectID string) {
	api := l.Services.Literature
	l.mu.Lock()
	sel := l.pending
	sel.ProjectID = projectID
	titles := make(map[string]string, len(l.state.titles))
	for key, title := range l.state.titles {
		titles[key] = title
	}
	l.loading = true
	l.state.result = nil
	l.mu.Unlock()
	if len(sel.ItemKeys) == 0 && len(sel.CollectionKeys) == 0 {
		l.finish(true)
		return
	}

	a.RunTask(i18n.T("pages.literature.task_import"), func(ctx context.Context, t *app.Task) error {
		t.Stage(i18n.T("pages.literature.stage_import"))
		result, err := api.Import(ctx, sel, func(p literature.ImportProgress) {
			if p.Total > 0 {
				t.Progress(p.Index, p.Total)
			}
			t.Stage(i18n.Tf("pages.literature.import_stage", map[string]string{
				"title": entryTitle(titles, p.ItemKey),
				"state": importStatusLabel(p.Status),
			}))
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
			if papersErr == nil {
				l.setPapers(papers)
			}
			l.mu.Lock()
			l.state.preview = nil
			l.resultOpen = true
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

// resultDialog shows the receipt of the last import: every entry with the
// status the service reported, so a batch is verifiable item by item.
func (l *Literature) resultDialog(v *components.UI) {
	if !l.resultOpen {
		return
	}
	l.mu.Lock()
	result := l.state.result
	titles := make(map[string]string, len(l.state.titles))
	for key, title := range l.state.titles {
		titles[key] = title
	}
	l.mu.Unlock()
	if result == nil {
		l.resultOpen = false
		return
	}
	outcomes := result.Outcomes
	v.Dialog(&l.resultOpen, components.DialogOptions{
		Title: i18n.T("pages.literature.result_title"),
		Description: i18n.Tf("pages.literature.result_counts", map[string]string{
			"imported": strconv.Itoa(result.Imported),
			"linked":   strconv.Itoa(result.Linked),
			"skipped":  strconv.Itoa(result.Skipped),
			"failed":   strconv.Itoa(result.Failed),
		}),
		Width: 760,
		Actions: func() {
			if v.Button(i18n.T("pages.literature.btn_close"), components.ButtonOptions{}).Clicked() {
				l.resultOpen = false
			}
		},
	}, func() {
		if len(outcomes) == 0 {
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.FileText,
				Title:  i18n.T("pages.literature.preview_empty_title"),
				Reason: i18n.T("pages.literature.preview_empty_reason"),
			})
			return
		}
		state := components.NewTableState()
		state.Key = func(i int) any { return outcomes[i].ZoteroKey }
		table := v.Table(components.TableOptions{
			Columns: []components.Column{
				{Title: i18n.T("pages.literature.col_title")},
				{Title: i18n.T("pages.literature.col_state"), Width: 150},
				{Title: i18n.T("pages.literature.col_detail"), Width: 300},
			},
			Rows:  len(outcomes),
			State: state,
			Key:   func(i int) any { return outcomes[i].ZoteroKey },
			Cell: func(row, col int) {
				outcome := outcomes[row]
				switch col {
				case 0:
					ui.Text(v.C, entryTitle(titles, outcome.ZoteroKey)).SingleLine()
				case 1:
					tone, label := outcomeState(outcome)
					v.Badge(label, tone)
				case 2:
					ui.Text(v.C, outcomeDetail(outcome)).SingleLine()
				}
			},
		})
		table.Height(visibleTableHeight(len(outcomes)))
	})
}

// entryTitle names a Zotero key by the title the preview read, and falls back
// to the key when the preview never saw it.
func entryTitle(titles map[string]string, key string) string {
	if title := titles[key]; title != "" {
		return title
	}
	return key
}

// outcomeState labels one import outcome.
func outcomeState(outcome literature.ImportOutcome) (components.Tone, string) {
	switch outcome.Status {
	case literature.ImportCreated:
		return components.Success, i18n.T("pages.literature.status_imported")
	case literature.ImportLinkedExisting:
		return components.Success, i18n.T("pages.literature.status_linked")
	case literature.ImportSkippedNoPDF:
		return components.Warning, i18n.T("pages.literature.status_skipped_no_pdf")
	case literature.ImportNeedsAttachment:
		return components.Warning, i18n.T("pages.literature.status_needs_choice")
	case literature.ImportSkippedDuplicate:
		return components.Neutral, i18n.T("pages.literature.status_already_linked")
	default:
		return components.Danger, i18n.T("pages.literature.status_failed")
	}
}

// outcomeDetail is the error or the warnings of one outcome, in the order that
// matters: a failure explains itself before any warning does.
func outcomeDetail(outcome literature.ImportOutcome) string {
	if outcome.Error != "" {
		return outcome.Error
	}
	labels := make([]string, 0, len(outcome.Warnings))
	for _, warning := range outcome.Warnings {
		labels = append(labels, warningText(warning))
	}
	return strings.Join(labels, "，")
}

// importStatusLabel is the status text the task strip shows for the entry being
// written.
func importStatusLabel(status literature.ImportStatus) string {
	switch status {
	case literature.ImportCreated:
		return i18n.T("pages.literature.status_imported")
	case literature.ImportLinkedExisting:
		return i18n.T("pages.literature.status_linked")
	case literature.ImportSkippedNoPDF:
		return i18n.T("pages.literature.status_skipped_no_pdf")
	case literature.ImportNeedsAttachment:
		return i18n.T("pages.literature.status_needs_choice")
	case literature.ImportSkippedDuplicate:
		return i18n.T("pages.literature.status_already_linked")
	case literature.ImportFailed:
		return i18n.T("pages.literature.status_failed")
	default:
		return ""
	}
}

// warningText names one service warning code.
func warningText(warning string) string {
	switch warning {
	case "local_file_missing":
		return i18n.T("pages.literature.warning_file_missing")
	case "source_hash_differs":
		return i18n.T("pages.literature.warning_hash_differs")
	default:
		return warning
	}
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

func (l *Literature) chosenKey(itemKey string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pending.AttachmentChoices[itemKey]
}

// setChosen records the PDF the user picked for one item: the pending selection
// carries it into Import, so the service sees the same choice the dialog showed.
func (l *Literature) setChosen(itemKey, attachmentKey string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pending.AttachmentChoices == nil {
		l.pending.AttachmentChoices = map[string]string{}
	}
	l.pending.AttachmentChoices[itemKey] = attachmentKey
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

// creatorsText joins the creators of one entry, authors first, so a preview row
// names who wrote the paper.
func creatorsText(creators []literature.ZoteroCreator) string {
	names := make([]string, 0, len(creators))
	for _, creator := range creators {
		if creator.CreatorType != "" && creator.CreatorType != "author" {
			continue
		}
		name := creator.Name
		if name == "" {
			name = strings.TrimSpace(creator.LastName + " " + creator.FirstName)
		}
		if name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	const shown = 3
	if len(names) > shown {
		return strings.Join(names[:shown], ", ") + " et al."
	}
	return strings.Join(names, ", ")
}

// entryState labels what the import would do with one preview entry, so the
// user sees reuse, duplicate and missing-PDF cases before confirming.
func entryState(linked bool, entry literature.ImportPreviewEntry) (components.Tone, string) {
	switch {
	case linked:
		return components.Neutral, i18n.T("pages.literature.state_already_linked")
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
