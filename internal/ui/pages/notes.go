package pages

// Research Notes is the SPEC-05 page of the shell: the Markdown notes of the
// open project, as cards or as a table, and the note editor that opens on
// ports.RouteNote. Every database and file read runs in a managed task, so a
// frame only renders cached state; the editor keeps its unsaved buffer across
// frames and saves it through the SPEC-05 §5.7 flow, 600 ms after typing
// stops, on blur, before another note opens and when the page is left.

import (
	"context"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"

	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
	"researchos/internal/i18n"
	"researchos/internal/notes"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/errtext"
	"researchos/internal/ui/formula"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// noteListLimit is how many notes the page holds: the note service caps one
// page at 500, and a note list is browsed rather than paged (SPEC-05 §5.3).
const noteListLimit = 500

// noteCard widths keep a card readable while the row wraps into as many
// columns as the window has room for.
const (
	noteCardMinWidth float32 = 260
	noteCardMaxWidth float32 = 380
)

// noteListRows is how many table rows fit before the table scrolls inside its
// own box (design system row height 44px).
const noteListRows = 12

// noteSaveDelay is the SPEC-05 §5.7 debounce: a save runs 600 ms after the
// last edit.
const noteSaveDelay = 600 * time.Millisecond

// noteExcerptRunes is the length of a body excerpt on a card.
const noteExcerptRunes = 160

// noteFlushTimeout bounds the close-time flush: the window must close even
// when the disk or the service hangs.
const noteFlushTimeout = 2 * time.Second

// The two views of the list.
const (
	notesViewCards = iota
	notesViewList
)

// What a card or a row previews: the description, a body excerpt, or nothing.
// The description is the default because it needs no file read.
const (
	notesPreviewDescription = iota
	notesPreviewExcerpt
	notesPreviewNone
)

// notesShared is the session state the list and the editor share: the editor
// bumps the generation after it writes, so the list reloads instead of
// rendering the row it just changed.
type notesShared struct {
	mu  sync.Mutex
	gen int
}

func (s *notesShared) bump() {
	s.mu.Lock()
	s.gen++
	s.mu.Unlock()
}

func (s *notesShared) version() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

// Notes renders the note list of the open project.
type Notes struct {
	Services *Services
	shared   *notesShared

	mu             sync.Mutex
	loading        bool
	loaded         bool
	project        string
	version        int
	list           []*notes.Note
	problems       []string
	excerpts       map[string]string
	excerptLoading map[string]bool
	creating       bool
	createErr      string
	createDone     bool
	deleting       bool
	deleteErr      string
	deleteDone     bool

	// Frame-only state: the toolbar choices persist for the session, and the
	// dialogs are opened and closed by the frame that renders them.
	query       string
	view        int
	preview     int
	table       *ui.ListState
	createOpen  bool
	createTitle string
	createDesc  string
	deleteID    string
	deleteTitle string
}

// NoteEditor renders one note: its metadata and its Markdown body.
type NoteEditor struct {
	Services *Services
	shared   *notesShared

	mu         sync.Mutex
	loading    bool
	loadedID   string
	project    string
	note       *notes.Note
	bodyMark   string
	bodyState  notes.BodyState
	hash       string
	imageDir   string
	ed         *editor.State
	problems   []string
	saveErr    string
	conflict   bool
	diskHash   string
	saving     bool
	accepting  bool
	savedAt    time.Time
	lastChange time.Time

	// Frame-only state: the drafts, the view mode and the focus and
	// composition of the last frame. They decide when the buffer is saved.
	md                    *markdown.Options
	mdDir                 string
	mode                  int
	title                 string
	description           string
	draftsFor             string
	draftsAt              time.Time
	wasEditorFocused      bool
	wasTitleFocused       bool
	wasDescriptionFocused bool
	composing             bool
	savedPending          bool
	lastVersion           uint64
	frameC                *ui.Context
}

// Page renders one frame of the note list.
func (n *Notes) Page(v *components.UI, r ports.Route) {
	a := n.Services.App
	if a == nil {
		return
	}
	if n.Services.Notes == nil {
		n.unavailable(v)
		return
	}
	project := a.Project()
	if project == nil {
		n.needsProject(v, a)
		return
	}
	n.prepare()
	n.load(a, project.ID)
	list := n.visible()
	n.tickDialogs()

	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.notes.title"),
			Description: i18n.Tf("pages.notes.description", map[string]string{"project": project.Name}),
			Actions: func() {
				if v.Button(i18n.T("pages.notes.btn_create"), components.ButtonOptions{
					Variant: components.VariantPrimary,
					Icon:    icons.Plus,
				}).Clicked() {
					n.createOpen = true
				}
			},
		})
		n.banners(v)
		n.toolbar(v)
		switch {
		case len(list) == 0 && n.blank():
			v.Skeleton(components.SkeletonOptions{Lines: 5})
		case len(list) == 0 && n.query != "":
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.Search,
				Title:  i18n.T("pages.notes.no_results_title"),
				Reason: i18n.T("pages.notes.no_results_reason"),
			})
		case len(list) == 0:
			v.EmptyState(components.EmptyStateOptions{
				Icon:        icons.Note,
				Title:       i18n.T("pages.notes.empty_title"),
				Reason:      i18n.T("pages.notes.empty_reason"),
				ActionLabel: i18n.T("pages.notes.btn_create"),
				OnAction:    func() { n.createOpen = true },
			})
		case n.view == notesViewList:
			n.tableView(v, a, project.ID, list)
		default:
			n.cards(v, a, project.ID, list)
		}
		n.createDialog(v, a, project.ID)
		n.deleteDialog(v, a, project.ID)
	}})
}

// unavailable is the honest state of a workspace whose note service was never
// assembled.
func (n *Notes) unavailable(v *components.UI) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{Title: i18n.T("pages.notes.title")})
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.Note,
			Title:  i18n.T("pages.notes.disabled_title"),
			Reason: i18n.T("pages.notes.disabled_reason"),
		})
	}})
}

// needsProject states the precondition: notes belong to a project.
func (n *Notes) needsProject(v *components.UI, a *app.App) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.notes.title"),
			Description: i18n.T("pages.notes.needs_project_desc"),
		})
		v.EmptyState(components.EmptyStateOptions{
			Icon:        icons.Note,
			Title:       i18n.T("pages.notes.needs_project_title"),
			Reason:      i18n.T("pages.notes.needs_project_reason"),
			ActionLabel: i18n.T("pages.notes.btn_projects"),
			OnAction:    func() { a.Navigate(ports.Route{Kind: app.RouteProjects}) },
		})
	}})
}

// prepare initializes the state a zero page does not have yet.
func (n *Notes) prepare() {
	if n.table == nil {
		n.table = components.NewTableState()
	}
	n.mu.Lock()
	if n.excerpts == nil {
		n.excerpts = map[string]string{}
	}
	if n.excerptLoading == nil {
		n.excerptLoading = map[string]bool{}
	}
	n.mu.Unlock()
}

// tickDialogs closes a dialog whose service call finished, so the task never
// writes a frame-only field.
func (n *Notes) tickDialogs() {
	n.mu.Lock()
	created, deleted := n.createDone, n.deleteDone
	if created {
		n.createDone = false
		n.createErr = ""
	}
	if deleted {
		n.deleteDone = false
		n.deleteErr = ""
	}
	n.mu.Unlock()
	if created {
		n.createOpen = false
		n.createTitle = ""
		n.createDesc = ""
	}
	if deleted {
		n.deleteID = ""
		n.deleteTitle = ""
	}
}

// blank reports whether the first load is still running.
func (n *Notes) blank() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.loading && !n.loaded
}

// banners reports the failures of the last load and of a dialog's action.
func (n *Notes) banners(v *components.UI) {
	n.mu.Lock()
	problems := append([]string(nil), n.problems...)
	n.mu.Unlock()
	for _, problem := range problems {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.notes.banner_problem"),
			Message: problem,
		})
	}
}

// toolbar is the search field and the two choices that shape the list.
func (n *Notes) toolbar(v *components.UI) {
	ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).AlignItems(ui.End).Wrap().Children(func() {
		v.SearchInput(&n.query, components.SearchOptions{
			Label:       i18n.T("pages.notes.field_search"),
			Placeholder: i18n.T("pages.notes.field_search_hint"),
		})
		ui.Spacer(v.C)
		ui.Segmented(v.C, &n.view,
			i18n.T("pages.notes.view_cards"),
			i18n.T("pages.notes.view_list"),
		).Label(i18n.T("pages.notes.view_label"))
		ui.Segmented(v.C, &n.preview,
			i18n.T("pages.notes.preview_description"),
			i18n.T("pages.notes.preview_excerpt"),
			i18n.T("pages.notes.preview_none"),
		).Label(i18n.T("pages.notes.preview_label"))
	})
}

// cards lays the notes out in a wrapping grid of cards.
func (n *Notes) cards(v *components.UI, a *app.App, projectID string, list []*notes.Note) {
	n.ensureExcerpts(a, projectID, list)
	ui.Row(v.C).FillWidth().Wrap().Gap(tokens.SectionGap).AlignItems(ui.Start).Children(func() {
		for _, note := range list {
			n.card(v, a, note)
		}
	})
}

// card renders one note. Its title and preview open the editor; the delete
// button sits outside the clickable area, so a click on the menu never opens
// the note.
func (n *Notes) card(v *components.UI, a *app.App, note *notes.Note) {
	p := v.T.Palette
	open := func() { a.Navigate(ports.Route{Kind: ports.RouteNote, EntityID: note.ID}) }
	card := ui.Column(v.C).
		Padding(tokens.PanelPadding).
		Gap(tokens.RelatedGap).
		Background(mygo.Color(p.BgSurface)).
		Border(1, mygo.Color(p.BorderDefault)).
		Radius(tokens.RadiusLG).
		Grow(1).
		MinWidth(noteCardMinWidth).
		MaxWidth(noteCardMaxWidth)
	if card.Hovered() {
		card.Border(1, mygo.Color(p.BorderStrong))
	}
	card.Children(func() {
		ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Start).Children(func() {
			head := ui.Column(v.C).Grow(1).Gap(tokens.Space1).OnClick(open)
			head.Children(func() {
				ui.Text(v.C, noteTitle(note)).
					FontSize(tokens.Typography.CardTitle.Size).
					FontWeight(int(tokens.Typography.CardTitle.Weight)).
					TextColor(mygo.Color(p.TextPrimary)).
					SingleLine()
				ui.Text(v.C, relativeTime(note.UpdatedAt)).
					FontSize(tokens.Typography.Caption.Size).
					TextColor(mygo.Color(p.TextMuted))
			})
			if v.IconButton(icons.Trash, i18n.T("pages.notes.btn_delete"), components.IconButtonOptions{
				Variant: components.VariantGhost,
				Size:    components.SizeSmall,
			}).Clicked() {
				n.askDelete(note)
			}
		})
		if text := n.previewText(note); text != "" {
			ui.Text(v.C, text).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(p.TextSecondary)).
				MaxLines(4).
				OnClick(open)
		}
	})
}

// tableView renders the same notes as a sortable table.
func (n *Notes) tableView(v *components.UI, a *app.App, projectID string, list []*notes.Note) {
	n.ensureExcerpts(a, projectID, list)
	sorted := sortNotes(list, n.table.Sort)
	open := func(note *notes.Note) {
		a.Navigate(ports.Route{Kind: ports.RouteNote, EntityID: note.ID})
	}
	table := v.Table(components.TableOptions{
		Columns: []components.Column{
			{Title: i18n.T("pages.notes.col_title"), ID: "title", Sortable: true},
			{Title: i18n.T("pages.notes.col_description"), ID: "description", Sortable: true},
			{Title: i18n.T("pages.notes.col_updated"), ID: "updated", Width: 140, Sortable: true},
			{Title: i18n.T("pages.notes.col_actions"), Width: 64, Align: ui.End},
		},
		Rows:  len(sorted),
		State: n.table,
		Key:   func(i int) any { return sorted[i].ID },
		Cell: func(row, col int) {
			note := sorted[row]
			switch col {
			case 0:
				if ui.Text(v.C, noteTitle(note)).SingleLine().FillWidth().RowClicked() {
					open(note)
				}
			case 1:
				if ui.Text(v.C, n.previewText(note)).SingleLine().FillWidth().RowClicked() {
					open(note)
				}
			case 2:
				if ui.Text(v.C, relativeTime(note.UpdatedAt)).FontFeatures("tnum").RowClicked() {
					open(note)
				}
			case 3:
				if v.IconButton(icons.Trash, i18n.T("pages.notes.btn_delete"), components.IconButtonOptions{
					Variant: components.VariantGhost,
					Size:    components.SizeSmall,
				}).Clicked() {
					n.askDelete(note)
				}
			}
		},
	})
	table.Height(visibleNoteRows(len(sorted)))
}

// askDelete opens the confirmation dialog for one note.
func (n *Notes) askDelete(note *notes.Note) {
	n.deleteID = note.ID
	n.deleteTitle = noteTitle(note)
	n.mu.Lock()
	n.deleteErr = ""
	n.mu.Unlock()
}

// createDialog collects a title and an optional description, then creates the
// note. A failure keeps the dialog and its input so the user can retry.
func (n *Notes) createDialog(v *components.UI, a *app.App, projectID string) {
	if !n.createOpen {
		return
	}
	n.mu.Lock()
	creating, errText := n.creating, n.createErr
	n.mu.Unlock()
	v.Dialog(&n.createOpen, components.DialogOptions{
		Title:       i18n.T("pages.notes.dialog_title"),
		Description: i18n.T("pages.notes.dialog_desc"),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				n.createOpen = false
			}
			if v.Button(i18n.T("pages.notes.dialog_create"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Loading:  creating,
				Disabled: creating,
			}).Clicked() {
				n.create(a, projectID)
			}
		},
	}, func() {
		field := v.Input(&n.createTitle, components.FieldOptions{
			Label:       i18n.T("pages.notes.field_title"),
			Placeholder: i18n.T("pages.notes.field_title_hint"),
			AutoFocus:   true,
		})
		if field.Submitted() {
			n.create(a, projectID)
		}
		v.Textarea(&n.createDesc, components.FieldOptions{
			Label:       i18n.T("pages.notes.field_description"),
			Placeholder: i18n.T("pages.notes.field_description_hint"),
		})
		if errText != "" {
			ui.Text(v.C, errText).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
}

// deleteDialog confirms the soft deletion of one note. The file stays in the
// project for the recycle bin to restore (SPEC-05 §5.1).
func (n *Notes) deleteDialog(v *components.UI, a *app.App, projectID string) {
	if n.deleteID == "" {
		return
	}
	n.mu.Lock()
	deleting, errText := n.deleting, n.deleteErr
	n.mu.Unlock()
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title: i18n.T("pages.notes.delete_title"),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				n.deleteID = ""
			}
			if v.Button(i18n.T("pages.notes.delete_confirm"), components.ButtonOptions{
				Variant:  components.VariantDanger,
				Loading:  deleting,
				Disabled: deleting,
			}).Clicked() {
				n.delete(a, projectID)
			}
		},
	}, func() {
		ui.Text(v.C, i18n.Tf("pages.notes.delete_message", map[string]string{"title": n.deleteTitle})).
			FontSize(tokens.Typography.Body.Size)
		if errText != "" {
			ui.Text(v.C, errText).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
	if !open && !deleting {
		// Escape or a press outside closed the dialog.
		n.deleteID = ""
	}
}

// create runs the create call. The dialog closes on the next frame, which the
// notify asks for.
func (n *Notes) create(a *app.App, projectID string) {
	title := strings.TrimSpace(n.createTitle)
	if title == "" {
		a.Toast(app.LevelWarning, i18n.T("pages.notes.title_required"), "")
		return
	}
	n.mu.Lock()
	if n.creating {
		n.mu.Unlock()
		return
	}
	n.creating = true
	n.createErr = ""
	n.mu.Unlock()

	api := n.Services.Notes
	description := n.createDesc
	a.RunTask(i18n.T("pages.notes.task_create"), func(ctx context.Context, t *app.Task) error {
		note, err := api.Create(ctx, projectID, notes.CreateNoteInput{
			Kind:        notes.KindScratchpad,
			Title:       title,
			Description: description,
			Actor:       ports.ActorUser,
		})
		n.mu.Lock()
		n.creating = false
		if err != nil {
			n.createErr = errtext.Text(err)
		}
		n.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.notes.toast_create_fail"), errtext.Text(err))
			a.Notify()
			return nil
		}
		n.mu.Lock()
		n.createDone = true
		n.mu.Unlock()
		n.shared.bump()
		a.Toast(app.LevelSuccess, i18n.T("pages.notes.toast_created"), noteTitle(note))
		a.Navigate(ports.Route{Kind: ports.RouteNote, ProjectID: projectID, EntityID: note.ID})
		return nil
	})
}

// delete soft-deletes the note the dialog names.
func (n *Notes) delete(a *app.App, projectID string) {
	noteID := n.deleteID
	if noteID == "" {
		return
	}
	n.mu.Lock()
	if n.deleting {
		n.mu.Unlock()
		return
	}
	n.deleting = true
	n.deleteErr = ""
	n.mu.Unlock()

	api := n.Services.Notes
	a.RunTask(i18n.T("pages.notes.task_delete"), func(ctx context.Context, t *app.Task) error {
		err := api.SoftDelete(ctx, projectID, noteID, ports.ActorUser)
		n.mu.Lock()
		n.deleting = false
		if err != nil {
			n.deleteErr = errtext.Text(err)
		} else {
			kept := n.list[:0]
			for _, note := range n.list {
				if note.ID != noteID {
					kept = append(kept, note)
				}
			}
			n.list = kept
			n.deleteDone = true
		}
		n.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.notes.toast_delete_fail"), errtext.Text(err))
			a.Notify()
			return nil
		}
		n.shared.bump()
		a.Toast(app.LevelSuccess, i18n.T("pages.notes.toast_deleted"), "")
		a.Notify()
		return nil
	})
}

// load reads the list once per project and again after the editor wrote.
func (n *Notes) load(a *app.App, projectID string) {
	n.mu.Lock()
	need := !n.loading && (!n.loaded || n.project != projectID || n.version != n.shared.version())
	n.mu.Unlock()
	if need {
		n.reload(a, projectID)
	}
}

func (n *Notes) reload(a *app.App, projectID string) {
	api := n.Services.Notes
	n.mu.Lock()
	if n.loading {
		n.mu.Unlock()
		return
	}
	n.loading = true
	if n.project != projectID {
		n.project = projectID
		n.list = nil
		n.loaded = false
		n.version = 0
	}
	n.mu.Unlock()
	gen := n.shared.version()

	a.RunTask(i18n.T("pages.notes.task_load"), func(ctx context.Context, t *app.Task) error {
		page, err := api.List(ctx, projectID, notes.ListNotesQuery{Limit: noteListLimit})
		n.mu.Lock()
		n.loading = false
		if err != nil {
			n.problems = append(n.problems[:0], errtext.Text(err))
		} else {
			n.list = page.Notes
			n.problems = nil
			n.loaded = true
			n.version = gen
		}
		n.mu.Unlock()
		a.Notify()
		return nil
	})
}

// visible applies the title search to the cached list.
func (n *Notes) visible() []*notes.Note {
	n.mu.Lock()
	list := append([]*notes.Note(nil), n.list...)
	n.mu.Unlock()
	query := strings.ToLower(strings.TrimSpace(n.query))
	if query == "" {
		return list
	}
	out := list[:0]
	for _, note := range list {
		if strings.Contains(strings.ToLower(note.Title), query) {
			out = append(out, note)
		}
	}
	return out
}

// previewText is what a card or a row shows under its title.
func (n *Notes) previewText(note *notes.Note) string {
	switch n.preview {
	case notesPreviewExcerpt:
		return n.excerpt(note)
	case notesPreviewDescription:
		return note.Description
	default:
		return ""
	}
}

// excerpt reads the cached assumption of a body excerpt. A note whose body was
// not read yet shows nothing until the task lands, never a frame-long wait.
func (n *Notes) excerpt(note *notes.Note) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.excerpts[excerptKey(note)]
}

// ensureExcerpts reads the bodies the excerpt view needs, once per note and
// hash: a save or an external change makes a new key, so a stale excerpt never
// shows.
func (n *Notes) ensureExcerpts(a *app.App, projectID string, list []*notes.Note) {
	if n.preview != notesPreviewExcerpt {
		return
	}
	api := n.Services.Notes
	var todo []*notes.Note
	n.mu.Lock()
	for _, note := range list {
		key := excerptKey(note)
		if _, ok := n.excerpts[key]; ok || n.excerptLoading[key] {
			continue
		}
		n.excerptLoading[key] = true
		todo = append(todo, note)
	}
	n.mu.Unlock()
	if len(todo) == 0 {
		return
	}

	a.RunTask(i18n.T("pages.notes.task_excerpts"), func(ctx context.Context, t *app.Task) error {
		for i, note := range todo {
			key := excerptKey(note)
			text := ""
			body, err := api.LoadBody(ctx, projectID, note.ID)
			if err == nil && body.State == notes.BodyOK {
				text = excerptText(body.Markdown)
			}
			n.mu.Lock()
			n.excerpts[key] = text
			delete(n.excerptLoading, key)
			n.mu.Unlock()
			t.Progress(i+1, len(todo))
		}
		a.Notify()
		return nil
	})
}

// ---------------------------------------------------------------------------
// The note editor
// ---------------------------------------------------------------------------

// Page renders one frame of the note editor.
func (e *NoteEditor) Page(v *components.UI, r ports.Route) {
	a := e.Services.App
	if a == nil {
		return
	}
	if e.Services.Notes == nil {
		e.unavailable(v)
		return
	}
	project := a.Project()
	if project == nil {
		e.needsProject(v, a)
		return
	}
	noteID := r.EntityID
	if noteID == "" {
		a.Navigate(ports.Route{Kind: app.RouteNotes})
		return
	}
	e.frameC = v.C
	e.syncDrafts()
	e.leavePrevious(a, noteID)
	e.load(a, project.ID, noteID)
	e.ensureEditor()

	note := e.noteSnapshot()
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title: noteHeader(note),
			Actions: func() {
				if v.Button(i18n.T("pages.notes.btn_back"), components.ButtonOptions{
					Variant: components.VariantGhost,
					Icon:    icons.Back,
				}).Clicked() {
					e.saveNow(a, project.ID)
					a.Navigate(ports.Route{Kind: app.RouteNotes})
				}
			},
		})
		e.banners(v, a, project.ID)
		if note == nil {
			if e.blank() {
				v.Skeleton(components.SkeletonOptions{Lines: 6})
			}
			return
		}
		e.metadata(v, a, project.ID, note)
		e.body(v, a, project.ID, note)
		e.tick(v, a, project.ID)
	}})
}

// unavailable mirrors the list's honest state.
func (e *NoteEditor) unavailable(v *components.UI) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{Title: i18n.T("pages.notes.editor_title")})
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.Note,
			Title:  i18n.T("pages.notes.disabled_title"),
			Reason: i18n.T("pages.notes.disabled_reason"),
		})
	}})
}

func (e *NoteEditor) needsProject(v *components.UI, a *app.App) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.notes.editor_title"),
			Description: i18n.T("pages.notes.needs_project_desc"),
		})
		v.EmptyState(components.EmptyStateOptions{
			Icon:        icons.Note,
			Title:       i18n.T("pages.notes.needs_project_title"),
			Reason:      i18n.T("pages.notes.needs_project_reason"),
			ActionLabel: i18n.T("pages.notes.btn_projects"),
			OnAction:    func() { a.Navigate(ports.Route{Kind: app.RouteProjects}) },
		})
	}})
}

// banners reports load failures, the unsaved write, the conflict and the two
// honest read-only states.
func (e *NoteEditor) banners(v *components.UI, a *app.App, projectID string) {
	e.mu.Lock()
	problems := append([]string(nil), e.problems...)
	saveErr := e.saveErr
	conflict, diskHash := e.conflict, e.diskHash
	note, bodyState := e.note, e.bodyState
	e.mu.Unlock()

	for _, problem := range problems {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.notes.banner_problem"),
			Message: problem,
		})
	}
	if note != nil && note.DeletedAt != nil {
		v.Banner(components.BannerOptions{
			Tone:    components.Warning,
			Title:   i18n.T("pages.notes.deleted_title"),
			Message: i18n.T("pages.notes.deleted_message"),
		})
	}
	if bodyState == notes.BodyMissing {
		v.Banner(components.BannerOptions{
			Tone:    components.Warning,
			Title:   i18n.T("pages.notes.missing_title"),
			Message: i18n.T("pages.notes.missing_message"),
		})
	}
	if conflict {
		v.Banner(components.BannerOptions{
			Tone:    components.Warning,
			Title:   i18n.T("pages.notes.conflict_title"),
			Message: i18n.T("pages.notes.conflict_message"),
		})
		ui.Row(v.C).Gap(tokens.ComponentGap).Children(func() {
			if v.Button(i18n.T("pages.notes.btn_accept_disk"), components.ButtonOptions{
				Variant: components.VariantSecondary,
			}).Clicked() {
				e.acceptDisk(a, projectID)
			}
			if v.Button(i18n.T("pages.notes.btn_overwrite"), components.ButtonOptions{
				Variant:  components.VariantDanger,
				Disabled: diskHash == "",
			}).Clicked() {
				e.overwrite(a, projectID)
			}
		})
	} else if saveErr != "" {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.notes.state_unsaved"),
			Message: saveErr,
		})
	}
}

// metadata is the title and description form. A title commits on Enter and on
// blur; the description on blur.
func (e *NoteEditor) metadata(v *components.UI, a *app.App, projectID string, note *notes.Note) {
	readOnly := note.DeletedAt != nil
	field := v.Input(&e.title, components.FieldOptions{
		Label:    i18n.T("pages.notes.field_note_title"),
		Disabled: readOnly,
	})
	titleFocused := field.Focused()
	if field.Submitted() || (e.wasTitleFocused && !titleFocused) {
		e.commitMeta(a, projectID, note.ID)
	}
	e.wasTitleFocused = titleFocused

	description := v.Textarea(&e.description, components.FieldOptions{
		Label:    i18n.T("pages.notes.field_note_description"),
		Disabled: readOnly,
	})
	descFocused := description.Focused()
	if e.wasDescriptionFocused && !descFocused {
		e.commitMeta(a, projectID, note.ID)
	}
	e.wasDescriptionFocused = descFocused
}

// body is the source/preview switch and the editor or the rendered Markdown.
func (e *NoteEditor) body(v *components.UI, a *app.App, projectID string, note *notes.Note) {
	e.mu.Lock()
	ed := e.ed
	bodyState := e.bodyState
	e.mu.Unlock()
	readOnly := note.DeletedAt != nil || bodyState == notes.BodyMissing

	ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).AlignItems(ui.Center).Children(func() {
		ui.Segmented(v.C, &e.mode,
			i18n.T("pages.notes.mode_source"),
			i18n.T("pages.notes.mode_preview"),
		).Label(i18n.T("pages.notes.mode_label"))
		ui.Spacer(v.C)
		if text, tone := e.saveState(readOnly); text != "" {
			v.StatusIndicator(tone, text)
		}
	})

	if ed == nil {
		v.Skeleton(components.SkeletonOptions{Lines: 8})
		return
	}
	ed.ReadOnly = readOnly
	if e.mode == 1 {
		e.composing = false
		e.preview(v, ed)
		return
	}
	view := editor.View(v.C, ed).FillWidth().Height(editorHeight(v))
	focused := view.Focused()
	e.composing = view.Composing()
	if e.wasEditorFocused && !focused {
		e.saveNow(a, projectID)
	}
	e.wasEditorFocused = focused
}

// preview renders the current buffer, so the preview shows what will be saved.
func (e *NoteEditor) preview(v *components.UI, ed *editor.State) {
	if e.md == nil {
		e.md = &markdown.Options{
			Extensions: parser.Default | parser.ExtDollarMath,
			Math:       formula.Block,
			InlineMath: formula.Inline,
			OnLink:     e.openLink,
		}
	}
	e.mu.Lock()
	dir := e.imageDir
	e.mu.Unlock()
	if dir != e.mdDir {
		e.mdDir = dir
		if dir == "" {
			e.md.LoadImage = nil
		} else {
			e.md.LoadImage = markdown.FileImages(dir)
		}
	}
	markdown.View(v.C, ed.Buffer.String(), e.md).FillWidth()
}

// saveState is the one line that says whether the buffer is on disk.
func (e *NoteEditor) saveState(readOnly bool) (string, components.Tone) {
	e.mu.Lock()
	saving, accepting, saveErr, conflict, savedAt := e.saving, e.accepting, e.saveErr, e.conflict, e.savedAt
	ed := e.ed
	e.mu.Unlock()
	if readOnly {
		return "", components.Neutral
	}
	switch {
	case saving || accepting:
		return i18n.T("pages.notes.state_saving"), components.Info
	case conflict:
		return i18n.T("pages.notes.state_conflict"), components.Warning
	case saveErr != "":
		return i18n.T("pages.notes.state_unsaved"), components.Danger
	case ed != nil && ed.Buffer.Modified():
		return i18n.T("pages.notes.state_unsaved"), components.Warning
	case !savedAt.IsZero():
		return i18n.T("pages.notes.state_saved"), components.Success
	}
	return "", components.Neutral
}

// tick detects edits and schedules the debounced save. A conflict, a failed
// write and an IME composition each pause the automatic save; the first two
// wait for an explicit decision or for the next edit.
func (e *NoteEditor) tick(v *components.UI, a *app.App, projectID string) {
	e.mu.Lock()
	ed := e.ed
	saving, conflict, saveErr := e.saving, e.conflict, e.saveErr
	e.mu.Unlock()
	if ed == nil || saving || conflict {
		e.savedPending = false
		return
	}
	version := ed.Buffer.Version()
	if version != e.lastVersion {
		e.lastVersion = version
		e.lastChange = v.C.Now()
		e.savedPending = false
		if saveErr != "" {
			e.mu.Lock()
			e.saveErr = ""
			e.mu.Unlock()
		}
	}
	if !ed.Buffer.Modified() || saveErr != "" || e.composing {
		e.savedPending = false
		return
	}
	left := noteSaveDelay - v.C.Now().Sub(e.lastChange)
	if left > 0 {
		if !e.savedPending {
			e.savedPending = true
			v.C.After(left)
		}
		return
	}
	e.savedPending = false
	e.save(a, projectID)
}

// Flush writes the editor's unsaved buffer synchronously, for the window's
// close callback: a save started through the shell's tasks would not finish
// before the process exits (SPEC-05 §5.7). A conflict or a write error leaves
// the file on disk untouched, because no frame is left to ask; the buffer
// stays in memory until the process ends.
func (e *NoteEditor) Flush() {
	if e == nil || e.Services == nil || e.Services.Notes == nil {
		return
	}
	e.waitSaving()
	e.mu.Lock()
	note, noteID, projectID := e.note, e.loadedID, e.project
	ed, base, state := e.ed, e.hash, e.bodyState
	title, description := e.title, e.description
	conflict := e.conflict
	e.mu.Unlock()
	if note == nil || noteID == "" || ed == nil || note.DeletedAt != nil ||
		state == notes.BodyMissing {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), noteFlushTimeout)
	defer cancel()
	api := e.Services.Notes
	written := true
	if ed.Buffer.Modified() {
		version := ed.Buffer.Version()
		result, err := api.SaveBody(ctx, projectID, notes.SaveBodyInput{
			NoteID:   noteID,
			Markdown: ed.Buffer.String(),
			BaseHash: base,
			Actor:    ports.ActorUser,
		})
		if err != nil || result == nil || result.Conflict {
			written = false
		} else {
			e.mu.Lock()
			e.hash = result.Hash
			if ed.Buffer.Version() == version {
				ed.Buffer.MarkSaved()
			}
			e.mu.Unlock()
		}
	}
	// Metadata is written after the body: UpdateMeta rewrites the file, so a
	// body save after it would carry a stale whole-file token.
	name := strings.TrimSpace(title)
	if !written || conflict || name == "" || (name == note.Title && description == note.Description) {
		return
	}
	_, _ = api.UpdateMeta(ctx, projectID, notes.UpdateMetaInput{
		NoteID:      noteID,
		Title:       name,
		Description: description,
		Actor:       ports.ActorUser,
	})
}

// waitSaving lets an in-flight debounced save finish, so the flush submits the
// hash that save installed instead of a stale token. It is bounded: closing
// must not hang on the disk.
func (e *NoteEditor) waitSaving() {
	deadline := time.Now().Add(noteFlushTimeout)
	for {
		e.mu.Lock()
		saving := e.saving || e.accepting
		e.mu.Unlock()
		if !saving || time.Now().After(deadline) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// saveNow flushes the buffer at once, for a blur, another note or a page
// leave.
func (e *NoteEditor) saveNow(a *app.App, projectID string) {
	e.mu.Lock()
	ed := e.ed
	e.mu.Unlock()
	if ed == nil || !ed.Buffer.Modified() {
		return
	}
	e.save(a, projectID)
}

// save submits the buffer through the SPEC-05 §5.7 flow. The whole-file hash
// is the token the service compares, so an external edit comes back as a
// conflict instead of being overwritten.
func (e *NoteEditor) save(a *app.App, projectID string) {
	e.mu.Lock()
	if e.saving || e.accepting || e.note == nil || e.loadedID == "" ||
		e.note.DeletedAt != nil || e.bodyState == notes.BodyMissing {
		e.mu.Unlock()
		return
	}
	ed := e.ed
	if ed == nil || !ed.Buffer.Modified() {
		e.mu.Unlock()
		return
	}
	text := ed.Buffer.String()
	version := ed.Buffer.Version()
	base := e.hash
	noteID := e.loadedID
	e.saving = true
	e.saveErr = ""
	e.mu.Unlock()

	api := e.Services.Notes
	a.RunTask(i18n.T("pages.notes.task_save"), func(ctx context.Context, t *app.Task) error {
		result, err := api.SaveBody(ctx, projectID, notes.SaveBodyInput{
			NoteID:   noteID,
			Markdown: text,
			BaseHash: base,
			Actor:    ports.ActorUser,
		})
		bumped := false
		e.mu.Lock()
		e.saving = false
		if e.loadedID != noteID {
			// Another note opened while this save ran: its result belongs to
			// the note it wrote, so nothing of it lands on the editor now.
			e.mu.Unlock()
			if err != nil {
				a.Toast(app.LevelDanger, i18n.T("pages.notes.toast_save_fail"), errtext.Text(err))
			}
			e.shared.bump()
			a.Notify()
			return nil
		}
		switch {
		case err != nil:
			e.saveErr = errtext.Text(err)
		case result.Conflict:
			e.conflict = true
			e.diskHash = result.DiskHash
			e.saveErr = ""
		default:
			e.conflict = false
			e.diskHash = ""
			e.hash = result.Hash
			e.savedAt = result.UpdatedAt
			e.saveErr = ""
			if ed := e.ed; ed != nil && ed.Buffer.Version() == version {
				ed.Buffer.MarkSaved()
			}
			bumped = true
		}
		e.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.notes.toast_save_fail"), errtext.Text(err))
		}
		if bumped {
			e.shared.bump()
		}
		a.Notify()
		return nil
	})
}

// overwrite retries the save with the disk token, replacing the external
// change with the buffer (SPEC-05 §5.7).
func (e *NoteEditor) overwrite(a *app.App, projectID string) {
	e.mu.Lock()
	base := e.diskHash
	ok := base != ""
	if ok {
		e.hash = base
	}
	e.conflict = false
	e.diskHash = ""
	e.mu.Unlock()
	if !ok {
		return
	}
	e.save(a, projectID)
}

// acceptDisk reloads the body and adopts it, dropping the buffer's content.
func (e *NoteEditor) acceptDisk(a *app.App, projectID string) {
	api := e.Services.Notes
	e.mu.Lock()
	if e.accepting || e.loadedID == "" {
		e.mu.Unlock()
		return
	}
	noteID := e.loadedID
	e.accepting = true
	e.mu.Unlock()

	a.RunTask(i18n.T("pages.notes.task_accept"), func(ctx context.Context, t *app.Task) error {
		body, err := api.LoadBody(ctx, projectID, noteID)
		e.mu.Lock()
		e.accepting = false
		if e.loadedID != noteID {
			e.mu.Unlock()
			a.Notify()
			return nil
		}
		if err != nil {
			e.saveErr = errtext.Text(err)
		} else {
			if body.State == notes.BodyOK {
				if ed := e.ed; ed != nil {
					ed.Buffer.Reset(body.Markdown)
				}
			}
			e.bodyState = body.State
			e.hash = body.Hash
			e.conflict = false
			e.diskHash = ""
			e.saveErr = ""
		}
		e.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.notes.toast_save_fail"), errtext.Text(err))
		}
		a.Notify()
		return nil
	})
}

// commitMeta writes the title and description through UpdateMeta. The body is
// never touched (D-05-01).
func (e *NoteEditor) commitMeta(a *app.App, projectID, noteID string) {
	e.mu.Lock()
	note := e.note
	e.mu.Unlock()
	if note == nil || note.DeletedAt != nil {
		return
	}
	title := strings.TrimSpace(e.title)
	if title == "" {
		e.title = note.Title
		if note.Title != "" {
			a.Toast(app.LevelWarning, i18n.T("pages.notes.title_required"), "")
		}
		return
	}
	description := e.description
	if title == note.Title && description == note.Description {
		return
	}
	api := e.Services.Notes
	a.RunTask(i18n.T("pages.notes.task_meta"), func(ctx context.Context, t *app.Task) error {
		updated, err := api.UpdateMeta(ctx, projectID, notes.UpdateMetaInput{
			NoteID:      noteID,
			Title:       title,
			Description: description,
			Actor:       ports.ActorUser,
		})
		e.mu.Lock()
		if err != nil {
			e.problems = append(e.problems[:0], errtext.Text(err))
		} else if e.loadedID == noteID {
			e.note = updated
			// RewriteMeta wrote the file, so its whole-file token changed; the
			// next body save must carry the new one.
			e.hash = updated.ContentHash
			e.problems = nil
		}
		e.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.notes.toast_meta_fail"), errtext.Text(err))
		} else {
			e.shared.bump()
		}
		a.Notify()
		return nil
	})
}

// syncDrafts copies the loaded note's metadata into the inputs once, and again
// when a metadata write lands while the field has no focus.
func (e *NoteEditor) syncDrafts() {
	e.mu.Lock()
	note := e.note
	id := e.loadedID
	e.mu.Unlock()
	if note == nil || id == "" {
		return
	}
	if e.draftsFor != id {
		e.title = note.Title
		e.description = note.Description
		e.draftsFor = id
		e.draftsAt = note.UpdatedAt
		return
	}
	if note.UpdatedAt.After(e.draftsAt) {
		if !e.wasTitleFocused {
			e.title = note.Title
		}
		if !e.wasDescriptionFocused {
			e.description = note.Description
		}
		e.draftsAt = note.UpdatedAt
	}
}

// leavePrevious flushes an unsaved buffer and drops the previous note when the
// route opens another one.
func (e *NoteEditor) leavePrevious(a *app.App, noteID string) {
	e.mu.Lock()
	stale := e.loadedID != "" && e.loadedID != noteID
	previous := e.ed
	e.mu.Unlock()
	if !stale {
		return
	}
	if previous != nil && previous.Buffer.Modified() {
		e.save(a, e.project)
	}
	e.mu.Lock()
	if e.loadedID != noteID {
		e.loadedID = ""
		e.note = nil
		e.bodyMark = ""
		e.bodyState = ""
		e.hash = ""
		e.ed = nil
		e.imageDir = ""
		e.problems = nil
		e.saveErr = ""
		e.conflict = false
		e.diskHash = ""
	}
	e.mu.Unlock()
	e.lastVersion = 0
	e.draftsFor = ""
	e.wasEditorFocused = false
	e.wasTitleFocused = false
	e.wasDescriptionFocused = false
}

// blank reports whether the note is still loading.
func (e *NoteEditor) blank() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loading && e.note == nil
}

// noteSnapshot returns the loaded note, or nil.
func (e *NoteEditor) noteSnapshot() *notes.Note {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.note
}

// load reads one note and its body once per route.
func (e *NoteEditor) load(a *app.App, projectID, noteID string) {
	e.mu.Lock()
	if e.loading || (e.loadedID == noteID && e.project == projectID && e.note != nil) {
		e.mu.Unlock()
		return
	}
	e.loading = true
	e.project = projectID
	e.mu.Unlock()

	api := e.Services.Notes
	a.RunTask(i18n.T("pages.notes.task_load_body"), func(ctx context.Context, t *app.Task) error {
		var problems []string
		note, err := api.Get(ctx, projectID, noteID)
		if err != nil {
			problems = append(problems, errtext.Text(err))
		}
		var body *notes.Body
		if note != nil {
			body, err = api.LoadBody(ctx, projectID, noteID)
			if err != nil {
				problems = append(problems, errtext.Text(err))
			}
		}
		var dir string
		if note != nil {
			dir, err = api.ImageBaseDir(projectID, noteID)
			if err != nil {
				dir = ""
			}
		}
		e.mu.Lock()
		e.loading = false
		e.problems = problems
		if note != nil {
			e.note = note
		}
		if body != nil {
			e.bodyState = body.State
			e.hash = body.Hash
			e.bodyMark = body.Markdown
		}
		e.imageDir = dir
		e.loadedID = noteID
		e.mu.Unlock()
		a.Notify()
		return nil
	})
}

// ensureEditor builds the source editor once the body arrived. The buffer is
// created in the frame, so its state matches the frame that will draw it.
func (e *NoteEditor) ensureEditor() {
	e.mu.Lock()
	if e.ed != nil || e.loadedID == "" || e.note == nil {
		e.mu.Unlock()
		return
	}
	source := e.bodyMark
	buffer := editor.NewBuffer(source)
	buffer.SetLanguage(editor.LanguageFor("note.md"))
	e.ed = &editor.State{Buffer: buffer}
	e.mu.Unlock()
}

// openLink handles a click in the preview: ros:// targets open their page,
// .md links open the note they name, and an http address opens in the system.
func (e *NoteEditor) openLink(url string) {
	a := e.Services.App
	if a == nil {
		return
	}
	if ref, ok := parseRosURL(url); ok {
		e.openRef(a, ref)
		return
	}
	lower := strings.ToLower(url)
	if i := strings.IndexAny(lower, "?#"); i >= 0 {
		lower, url = lower[:i], url[:i]
	}
	if strings.HasSuffix(lower, ".md") {
		name := path.Base(strings.ReplaceAll(url, "\\", "/"))
		id := strings.TrimSuffix(name, ".md")
		if ids.Valid(id) {
			a.Navigate(ports.Route{Kind: ports.RouteNote, EntityID: id})
			return
		}
	}
	if ui.SafeToOpen(url) && e.frameC != nil {
		e.frameC.OpenURL(url)
	}
}

// openRef resolves a ros:// reference and opens its page. A missing target is
// reported instead of opening a page that would not find it.
func (e *NoteEditor) openRef(a *app.App, ref notes.EntityRef) {
	kind, ok := routeFor(ref)
	if !ok {
		return
	}
	e.mu.Lock()
	projectID := e.project
	e.mu.Unlock()
	api := e.Services.Notes
	r := ports.Route{Kind: kind, EntityID: ref.ID}
	a.RunTask(i18n.T("pages.notes.task_link"), func(ctx context.Context, t *app.Task) error {
		target, err := api.ResolveTarget(ctx, projectID, ref)
		if err == nil && (target == nil || target.Missing) {
			a.Toast(app.LevelWarning, i18n.T("pages.notes.toast_link_missing"), "")
			a.Notify()
			return nil
		}
		a.Navigate(r)
		return nil
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// excerptKey keys a cached excerpt by the note and the hash it was read at.
func excerptKey(note *notes.Note) string {
	return note.ID + ":" + note.ContentHash
}

// noteTitle is the title a list row shows; an empty title reads as untitled.
func noteTitle(note *notes.Note) string {
	if strings.TrimSpace(note.Title) == "" {
		return i18n.T("pages.notes.untitled")
	}
	return note.Title
}

// noteHeader is the editor's page title.
func noteHeader(note *notes.Note) string {
	if note == nil || strings.TrimSpace(note.Title) == "" {
		return i18n.T("pages.notes.editor_title")
	}
	return note.Title
}

// visibleNoteRows caps a table at noteListRows rows so the page stays usable.
func visibleNoteRows(rows int) float32 {
	visible := rows
	if visible > noteListRows {
		visible = noteListRows
	}
	return float32(visible)*44 + 40
}

// editorHeight fills the window below the metadata form, with a floor so the
// editor stays usable in a short window.
func editorHeight(v *components.UI) float32 {
	_, h := v.C.Size()
	if h < 640 {
		return 360
	}
	return h - 320
}

// sortNotes orders the table's rows by the column its header reports. An
// empty value stays last whichever direction the column asks.
func sortNotes(list []*notes.Note, order *ui.SortOrder) []*notes.Note {
	out := append([]*notes.Note(nil), list...)
	if order == nil || order.Column == "" {
		return out
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if order.Column == "updated" {
			if order.Descending {
				return a.UpdatedAt.After(b.UpdatedAt)
			}
			return a.UpdatedAt.Before(b.UpdatedAt)
		}
		left, right := a.Title, b.Title
		if order.Column == "description" {
			left, right = a.Description, b.Description
		}
		le, re := strings.TrimSpace(left) == "", strings.TrimSpace(right) == ""
		if le != re {
			return re
		}
		c := strings.Compare(strings.ToLower(left), strings.ToLower(right))
		if order.Descending {
			return c > 0
		}
		return c < 0
	})
	return out
}

// relativeTime renders a timestamp the way a note list reads it: minutes and
// hours while they are recent, the date after a week.
func relativeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return i18n.T("pages.notes.time_just_now")
	case d < time.Hour:
		return i18n.Tf("pages.notes.time_minutes", map[string]string{"n": strconv.Itoa(int(d.Minutes()))})
	case d < 24*time.Hour:
		return i18n.Tf("pages.notes.time_hours", map[string]string{"n": strconv.Itoa(int(d.Hours()))})
	case d < 7*24*time.Hour:
		return i18n.Tf("pages.notes.time_days", map[string]string{"n": strconv.Itoa(int(d.Hours() / 24))})
	}
	return t.Format("2006-01-02")
}

// excerptText strips the Markdown syntax and keeps the first noteExcerptRunes
// runes, so a card previews prose rather than markup.
func excerptText(src string) string {
	var b strings.Builder
	inFence := false
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			b.WriteString(trimmed)
			b.WriteByte(' ')
			continue
		}
		b.WriteString(stripMarkdown(line))
		b.WriteByte(' ')
	}
	text := strings.Join(strings.Fields(b.String()), " ")
	return truncateRunes(text, noteExcerptRunes)
}

// stripMarkdown removes the block and inline markers of one line.
func stripMarkdown(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimLeft(line, "#>")
	line = strings.TrimSpace(line)
	if len(line) > 1 && strings.ContainsRune("-*+", rune(line[0])) && line[1] == ' ' {
		line = strings.TrimSpace(line[2:])
	} else {
		digits := 0
		for digits < len(line) && line[digits] >= '0' && line[digits] <= '9' {
			digits++
		}
		if digits > 0 && digits+1 < len(line) && (line[digits] == '.' || line[digits] == ')') && line[digits+1] == ' ' {
			line = strings.TrimSpace(line[digits+2:])
		}
	}
	return stripInline(strings.TrimSpace(line))
}

// stripInline removes emphasis, code fences, links and HTML tags from one
// line, keeping their text.
func stripInline(line string) string {
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '*', '_', '~', '`', '$':
			continue
		case '!':
			if i+1 < len(line) && line[i+1] == '[' {
				continue
			}
		case '[':
			text, rest, ok := takeBracket(line[i:])
			if !ok {
				b.WriteByte(line[i])
				continue
			}
			b.WriteString(text)
			i += len(line[i:]) - len(rest) - 1
		case '<':
			if end := strings.IndexByte(line[i:], '>'); end >= 0 {
				i += end
				continue
			}
			b.WriteByte(line[i])
		default:
			b.WriteByte(line[i])
		}
	}
	return b.String()
}

// takeBracket reads "[text](dest)" or "[text]" at the start of s and returns
// its text and the rest after it.
func takeBracket(s string) (text, rest string, ok bool) {
	end := strings.IndexByte(s, ']')
	if end < 0 {
		return "", s, false
	}
	text = s[1:end]
	rest = s[end+1:]
	if strings.HasPrefix(rest, "(") {
		if close := strings.IndexByte(rest, ')'); close >= 0 {
			rest = rest[close+1:]
		}
	}
	return text, rest, true
}

// truncateRunes cuts s to n runes and marks the cut.
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// parseRosURL reads "ros://<type>/<id>", ignoring a fragment.
func parseRosURL(raw string) (notes.EntityRef, bool) {
	rest, ok := strings.CutPrefix(raw, "ros://")
	if !ok {
		return notes.EntityRef{}, false
	}
	kind, id, ok := strings.Cut(rest, "/")
	if !ok || kind == "" || id == "" {
		return notes.EntityRef{}, false
	}
	if i := strings.IndexAny(id, "?#"); i >= 0 {
		id = id[:i]
	}
	return notes.EntityRef{Type: schema.EntityType(kind), ID: id}, true
}

// routeFor maps a resolved entity type to its page.
func routeFor(ref notes.EntityRef) (ports.RouteKind, bool) {
	switch ref.Type {
	case schema.EntityNote:
		return ports.RouteNote, true
	case schema.EntityPaper:
		return ports.RoutePaper, true
	case schema.EntityIdea:
		return ports.RouteIdea, true
	case schema.EntityExperiment:
		return ports.RouteExperiment, true
	}
	return "", false
}
