package pages

// Paper is the in-app paper page of one project paper: the extracted PDF text,
// the Paper Note and the research notes that link the paper. Every database,
// file and PDF read runs in a managed task, so the frame only renders cached
// state; switching papers reloads everything the page shows.

import (
	"context"
	"errors"
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

const (
	// paperPagesPerView is how many PDF pages one read and one screen show: a
	// large paper is parsed a window at a time instead of whole, so opening the
	// page never waits for the entire document.
	paperPagesPerView = 2
	// paperNoteFieldHeight is the height of one structured note field, and
	// paperNoteBodyHeight the height of the Markdown body.
	paperNoteFieldHeight float32 = 96
	paperNoteBodyHeight  float32 = 288
	// paperFieldMinWidth keeps the structured fields from squeezing below a
	// readable column when the window is narrow.
	paperFieldMinWidth float32 = 320
)

// What one PDF window read turned into.
const (
	paperTextOK = iota
	paperTextMissing
	paperTextScan
	paperTextFailed
)

// paperWindowPage is one page of the window the frame renders.
type paperWindowPage struct {
	no     int
	blocks []string
	seen   bool
}

// PaperPage renders one paper.
type PaperPage struct {
	Services *Services
	shared   *literatureShared

	mu       sync.Mutex
	loading  bool
	loadedID string
	project  string
	version  int
	paper    literature.Paper
	paperOK  bool
	note     *literature.PaperNote
	related  []PaperRelatedNote
	problems []string

	// The in-app reader's cached windows. pdfSeen marks a page whose read
	// finished, including a blank one; pdfFailFrom names the window whose read
	// failed, so it is not retried every frame.
	pdfLoading  bool
	pdfCount    int
	pdfFrom     int
	pdfPages    map[int][]string
	pdfSeen     map[int]bool
	pdfFailFrom int
	pdfFailKind int
	pdfFailMsg  string

	// The note form's server-side state. drafts hold what the user typed and
	// are written by the frame only; saving, saveErr, conflict and diskHash
	// travel between the frame and the save task.
	drafts    literature.PaperNoteFields
	body      string
	draftKey  string
	draftDrop bool
	saving    bool
	saveErr   string
	conflict  bool
	diskHash  string

	creating  bool
	createErr string
}

// Page renders one frame of the paper page.
func (p *PaperPage) Page(v *components.UI, r ports.Route) {
	a := p.Services.App
	if a == nil {
		return
	}
	if p.Services.Paper == nil {
		p.unavailable(v)
		return
	}
	project := a.Project()
	if project == nil {
		p.needsProject(v, a)
		return
	}
	paperID := r.EntityID
	if paperID == "" {
		a.Navigate(ports.Route{Kind: app.RouteLiterature})
		return
	}
	p.initWindow()
	p.load(a, project.ID, paperID)
	p.syncDrafts()
	p.ensureText(a, paperID)

	paper, loaded, ok := p.paperSnapshot()
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       paperHeading(paper, ok),
			Description: paperDescription(paper),
			Actions: func() {
				if v.Button(i18n.T("pages.paper.btn_back"), components.ButtonOptions{
					Variant: components.VariantGhost,
					Icon:    icons.Back,
				}).Clicked() {
					if p.draftsDirty() {
						p.save(a, project.ID, paperID)
					}
					a.Navigate(ports.Route{Kind: app.RouteLiterature})
				}
				if ok && v.Button(i18n.T("pages.paper.btn_open_pdf"), components.ButtonOptions{
					Variant: components.VariantSecondary,
					Icon:    icons.ExternalLink,
				}).Clicked() {
					p.openPDF(a, paperID)
				}
			},
		})
		p.banners(v, a, project.ID, paperID)
		if !loaded {
			v.Skeleton(components.SkeletonOptions{Lines: 6})
			return
		}
		if !ok {
			v.EmptyState(components.EmptyStateOptions{
				Icon:        icons.Literature,
				Title:       i18n.T("pages.paper.paper_missing_title"),
				Reason:      i18n.T("pages.paper.paper_missing_reason"),
				ActionLabel: i18n.T("pages.paper.btn_back"),
				OnAction:    func() { a.Navigate(ports.Route{Kind: app.RouteLiterature}) },
			})
			return
		}
		p.textCard(v, a, paperID)
		p.noteCard(v, a, project.ID, paperID)
		p.relatedCard(v, a, project.ID, paperID)
	}})
}

// initWindow allocates the reader's cache once; the first window starts at the
// first page, and no window has failed yet.
func (p *PaperPage) initWindow() {
	if p.pdfPages != nil {
		return
	}
	p.pdfPages = map[int][]string{}
	p.pdfSeen = map[int]bool{}
	p.pdfFrom = 1
	p.pdfFailFrom = -1
}

// unavailable is the honest state of a workspace whose paper services were
// never assembled.
func (p *PaperPage) unavailable(v *components.UI) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{Title: i18n.T("pages.paper.title")})
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.Literature,
			Title:  i18n.T("pages.paper.disabled_title"),
			Reason: i18n.T("pages.paper.disabled_reason"),
		})
	}})
}

// needsProject states the precondition: a paper is linked to a project.
func (p *PaperPage) needsProject(v *components.UI, a *app.App) {
	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.paper.title"),
			Description: i18n.T("pages.paper.needs_project_desc"),
		})
		v.EmptyState(components.EmptyStateOptions{
			Icon:        icons.Literature,
			Title:       i18n.T("pages.paper.needs_project_title"),
			Reason:      i18n.T("pages.paper.needs_project_reason"),
			ActionLabel: i18n.T("pages.paper.btn_projects"),
			OnAction:    func() { a.Navigate(ports.Route{Kind: app.RouteProjects}) },
		})
	}})
}

// banners reports read failures, the failed save, the conflict and the failed
// note creation.
func (p *PaperPage) banners(v *components.UI, a *app.App, projectID, paperID string) {
	p.mu.Lock()
	problems := append([]string(nil), p.problems...)
	conflict, diskHash := p.conflict, p.diskHash
	saveErr := p.saveErr
	createErr := p.createErr
	note := p.note
	p.mu.Unlock()

	for _, problem := range problems {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.paper.banner_problem"),
			Message: problem,
		})
	}
	if note != nil && note.DeletedAt != nil {
		v.Banner(components.BannerOptions{
			Tone:    components.Warning,
			Title:   i18n.T("pages.paper.note_deleted_title"),
			Message: i18n.T("pages.paper.note_deleted_message"),
		})
	}
	if createErr != "" {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.paper.banner_create_fail"),
			Message: createErr,
		})
	}
	if conflict {
		v.Banner(components.BannerOptions{
			Tone:    components.Warning,
			Title:   i18n.T("pages.paper.conflict_title"),
			Message: i18n.T("pages.paper.conflict_message"),
		})
		ui.Row(v.C).Gap(tokens.ComponentGap).Children(func() {
			if v.Button(i18n.T("pages.paper.btn_reload"), components.ButtonOptions{
				Variant: components.VariantSecondary,
			}).Clicked() {
				p.acceptDisk(a, projectID, paperID)
			}
			if v.Button(i18n.T("pages.paper.btn_overwrite"), components.ButtonOptions{
				Variant:  components.VariantDanger,
				Disabled: diskHash == "",
			}).Clicked() {
				p.overwrite(a, paperID)
			}
		})
	} else if saveErr != "" {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.paper.banner_save_fail"),
			Message: saveErr,
		})
	}
}

// textCard is the in-app reader: the current window of pages, its pager and the
// honest states of a copy that is gone or a page without a text layer.
func (p *PaperPage) textCard(v *components.UI, a *app.App, paperID string) {
	p.mu.Lock()
	from, count := p.pdfFrom, p.pdfCount
	loading := p.pdfLoading
	failFrom, failKind, failMsg := p.pdfFailFrom, p.pdfFailKind, p.pdfFailMsg
	last := from + paperPagesPerView - 1
	if count > 0 && last > count {
		last = count
	}
	window := make([]paperWindowPage, 0, paperPagesPerView)
	for n := from; n <= last; n++ {
		blocks, seen := p.pdfPages[n], p.pdfSeen[n]
		window = append(window, paperWindowPage{no: n, blocks: blocks, seen: seen})
	}
	p.mu.Unlock()

	failed := failFrom == from
	v.Card(components.CardOptions{
		Title: i18n.T("pages.paper.card_text"),
		Actions: func() {
			if v.Button(i18n.T("pages.paper.btn_prev"), components.ButtonOptions{
				Variant:  components.VariantGhost,
				Icon:     icons.ChevronLeft,
				Disabled: loading || from <= 1,
			}).Clicked() {
				p.moveText(a, paperID, -1)
			}
			if count > 0 {
				v.StatusIndicator(components.Neutral, i18n.Tf("pages.paper.page_range", map[string]string{
					"from":  strconv.Itoa(from),
					"to":    strconv.Itoa(last),
					"total": strconv.Itoa(count),
				}))
			}
			if v.Button(i18n.T("pages.paper.btn_next"), components.ButtonOptions{
				Variant:  components.VariantGhost,
				Icon:     icons.ChevronRight,
				Disabled: loading || count == 0 || last >= count,
			}).Clicked() {
				p.moveText(a, paperID, 1)
			}
		},
	}, func() {
		switch {
		case failed && failKind == paperTextMissing:
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.FileText,
				Title:  i18n.T("pages.paper.text_missing_title"),
				Reason: i18n.T("pages.paper.text_missing_reason"),
			})
			return
		case failed && failKind == paperTextScan:
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.FileText,
				Title:  i18n.T("pages.paper.text_scan_title"),
				Reason: i18n.T("pages.paper.text_scan_reason"),
			})
			return
		case failed && failKind == paperTextFailed:
			v.Banner(components.BannerOptions{
				Tone:    components.Danger,
				Title:   i18n.T("pages.paper.text_failed_title"),
				Message: failMsg,
			})
			return
		}
		if count == 0 && loading {
			v.Skeleton(components.SkeletonOptions{Lines: 8})
			return
		}
		if count == 0 {
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.FileText,
				Title:  i18n.T("pages.paper.text_empty_title"),
				Reason: i18n.T("pages.paper.text_empty_reason"),
			})
			return
		}
		for _, page := range window {
			p.renderPage(v, page)
		}
	})
}

// renderPage prints one page's paragraphs under its page number. A page whose
// read is still running shows its skeleton in place, so the window keeps its
// height instead of jumping.
func (p *PaperPage) renderPage(v *components.UI, page paperWindowPage) {
	ui.Column(v.C).FillWidth().Gap(tokens.Space2).Children(func() {
		ui.Text(v.C, i18n.Tf("pages.paper.page_index", map[string]string{"n": strconv.Itoa(page.no)})).
			FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(v.T.Palette.TextMuted))
		if !page.seen {
			v.Skeleton(components.SkeletonOptions{Lines: 5})
			return
		}
		shown := false
		for _, block := range page.blocks {
			if strings.TrimSpace(block) == "" {
				continue
			}
			shown = true
			ui.Text(v.C, block).
				FontSize(tokens.Typography.Body.Size).
				MaxWidth(components.ReadingWidth)
		}
		if !shown {
			ui.Text(v.C, i18n.T("pages.paper.page_blank")).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.TextMuted))
		}
	})
}

// noteCard is the Paper Note form: the six structured fields and the Markdown
// body, saved through the service's optimistic-concurrency tokens.
func (p *PaperPage) noteCard(v *components.UI, a *app.App, projectID, paperID string) {
	p.mu.Lock()
	note, saving, conflict := p.note, p.saving, p.conflict
	p.mu.Unlock()
	readOnly := note != nil && note.DeletedAt != nil
	dirty := p.draftsDirty()

	v.Card(components.CardOptions{
		Title: i18n.T("pages.paper.card_note"),
		Actions: func() {
			if text, tone := p.saveState(dirty); text != "" && !readOnly {
				v.StatusIndicator(tone, text)
			}
			if v.Button(i18n.T("pages.paper.btn_save_note"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Loading:  saving,
				Disabled: readOnly || saving || conflict || (note != nil && !dirty),
			}).Clicked() {
				p.save(a, projectID, paperID)
			}
		},
	}, func() {
		ui.Row(v.C).FillWidth().Wrap().Gap(tokens.SectionGap).AlignItems(ui.Start).Children(func() {
			p.field(v, &p.drafts.Summary, "pages.paper.field_summary", readOnly)
			p.field(v, &p.drafts.Problem, "pages.paper.field_problem", readOnly)
			p.field(v, &p.drafts.Method, "pages.paper.field_method", readOnly)
			p.field(v, &p.drafts.Contribution, "pages.paper.field_contribution", readOnly)
			p.field(v, &p.drafts.Limitations, "pages.paper.field_limitations", readOnly)
			p.field(v, &p.drafts.Understanding, "pages.paper.field_understanding", readOnly)
		})
		v.Textarea(&p.body, components.FieldOptions{
			Label:    i18n.T("pages.paper.field_body"),
			Disabled: readOnly,
		}).Height(paperNoteBodyHeight)
	})
}

// field is one structured note field in its own wrapping column.
func (p *PaperPage) field(v *components.UI, value *string, label string, disabled bool) {
	ui.Column(v.C).Grow(1).MinWidth(paperFieldMinWidth).Children(func() {
		v.Textarea(value, components.FieldOptions{Label: i18n.T(label), Disabled: disabled}).Height(paperNoteFieldHeight)
	})
}

// saveState is the one line that says whether the note is on disk.
func (p *PaperPage) saveState(dirty bool) (string, components.Tone) {
	p.mu.Lock()
	saving, conflict, saveErr, note := p.saving, p.conflict, p.saveErr, p.note
	p.mu.Unlock()
	switch {
	case saving:
		return i18n.T("pages.paper.state_saving"), components.Info
	case conflict:
		return i18n.T("pages.paper.state_conflict"), components.Warning
	case saveErr != "":
		return i18n.T("pages.paper.state_unsaved"), components.Danger
	case dirty:
		return i18n.T("pages.paper.state_unsaved"), components.Warning
	case note != nil:
		return i18n.T("pages.paper.state_saved"), components.Success
	}
	return "", components.Neutral
}

// relatedCard lists the project notes that link the paper and offers the button
// that creates one already linked.
func (p *PaperPage) relatedCard(v *components.UI, a *app.App, projectID, paperID string) {
	p.mu.Lock()
	related := append([]PaperRelatedNote(nil), p.related...)
	creating := p.creating
	p.mu.Unlock()

	v.Card(components.CardOptions{
		Title:       i18n.T("pages.paper.card_related"),
		Description: i18n.Tf("pages.paper.card_related_desc", map[string]string{"count": strconv.Itoa(len(related))}),
		Actions: func() {
			if v.Button(i18n.T("pages.paper.btn_new_note"), components.ButtonOptions{
				Variant:  components.VariantSecondary,
				Icon:     icons.Plus,
				Loading:  creating,
				Disabled: creating,
			}).Clicked() {
				p.createLinkedNote(a, projectID, paperID)
			}
		},
	}, func() {
		if len(related) == 0 {
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.Note,
				Title:  i18n.T("pages.paper.related_empty_title"),
				Reason: i18n.T("pages.paper.related_empty_reason"),
			})
			return
		}
		state := components.NewTableState()
		table := v.Table(components.TableOptions{
			Columns: []components.Column{
				{Title: i18n.T("pages.paper.col_note")},
				{Title: i18n.T("pages.paper.col_updated"), Width: 140},
			},
			Rows:  len(related),
			State: state,
			Key:   func(i int) any { return related[i].ID },
			Cell: func(row, col int) {
				note := related[row]
				switch col {
				case 0:
					if ui.Text(v.C, relatedNoteTitle(note)).SingleLine().FillWidth().RowClicked() {
						a.Navigate(ports.Route{Kind: ports.RouteNote, EntityID: note.ID})
					}
				case 1:
					if ui.Text(v.C, relativeTime(note.UpdatedAt)).FontFeatures("tnum").RowClicked() {
						a.Navigate(ports.Route{Kind: ports.RouteNote, EntityID: note.ID})
					}
				}
			},
		})
		table.Height(visibleTableHeight(len(related)))
	})
}

// load runs the reads once per paper and again after another page changed the
// literature generation.
func (p *PaperPage) load(a *app.App, projectID, paperID string) {
	p.mu.Lock()
	need := !p.loading && (p.loadedID != paperID || p.project != projectID)
	if !need && p.shared != nil && p.version != p.shared.version() {
		need = true
	}
	p.mu.Unlock()
	if need {
		p.reload(a, projectID, paperID)
	}
}

// reload reads the paper, its Paper Note and the notes that link it. Switching
// papers drops every cached window, because the pages belong to the previous
// file.
func (p *PaperPage) reload(a *app.App, projectID, paperID string) {
	api := p.Services.Paper
	p.mu.Lock()
	if p.loading && p.loadedID == paperID {
		// The same paper is already being read.
		p.mu.Unlock()
		return
	}
	// A different paper may still be loading: its task discards its result,
	// because loadedID no longer names it, and this one takes over the flag.
	first := p.loadedID != paperID || p.project != projectID
	p.loading = true
	p.project = projectID
	p.loadedID = paperID
	if first {
		p.paper = literature.Paper{}
		p.paperOK = false
		p.note = nil
		p.related = nil
		p.problems = nil
		p.saveErr, p.conflict, p.diskHash = "", false, ""
		p.createErr = ""
		p.pdfCount, p.pdfFrom = 0, 1
		p.pdfPages = map[int][]string{}
		p.pdfSeen = map[int]bool{}
		p.pdfLoading = false
		p.pdfFailFrom, p.pdfFailKind, p.pdfFailMsg = -1, paperTextOK, ""
		p.draftDrop = true
	}
	p.mu.Unlock()
	gen := 0
	if p.shared != nil {
		gen = p.shared.version()
	}

	a.RunTask(i18n.T("pages.paper.task_load"), func(ctx context.Context, t *app.Task) error {
		paper, paperErr := api.Paper(ctx, paperID)
		note, noteErr := api.PaperNote(ctx, projectID, paperID)
		related, relatedErr := api.RelatedNotes(ctx, projectID, paperID)

		p.mu.Lock()
		if p.loadedID != paperID {
			// Another paper opened while this read ran: its result belongs to
			// the paper it names, and the newer load owns the flags.
			p.mu.Unlock()
			a.Notify()
			return nil
		}
		p.loading = false
		problems := make([]string, 0, 3)
		if paperErr != nil {
			problems = append(problems, errtext.Text(paperErr))
			p.paperOK = false
		} else {
			p.paper, p.paperOK = paper, true
		}
		if noteErr != nil {
			problems = append(problems, errtext.Text(noteErr))
		} else {
			p.note = note
		}
		if relatedErr != nil {
			problems = append(problems, errtext.Text(relatedErr))
		} else {
			p.related = related
		}
		p.problems = problems
		p.version = gen
		p.mu.Unlock()
		a.Notify()
		return nil
	})
}

// syncDrafts copies the loaded note into the form when the drafts are not the
// user's unsaved work. A forced drop (another paper, or an accepted disk copy)
// wins over the drafts.
func (p *PaperPage) syncDrafts() {
	p.mu.Lock()
	note, drop, conflict := p.note, p.draftDrop, p.conflict
	p.mu.Unlock()
	if conflict && !drop {
		// The user must decide between the drafts and the disk copy.
		return
	}
	key := ""
	if note != nil {
		key = note.NoteID + ":" + note.UpdatedAt.String()
	}
	if !drop && key == p.draftKey {
		return
	}
	if !drop && p.draftKey != "" && p.draftsDirty() {
		return
	}
	p.drafts = literature.PaperNoteFields{}
	p.body = ""
	if note != nil {
		p.drafts = note.Fields
		p.body = note.BodyMD
	}
	p.draftKey = key
	if drop {
		p.mu.Lock()
		p.draftDrop = false
		p.mu.Unlock()
	}
}

// draftsDirty reports whether the form differs from the note on the server.
func (p *PaperPage) draftsDirty() bool {
	p.mu.Lock()
	note := p.note
	p.mu.Unlock()
	if note == nil {
		return p.drafts != literature.PaperNoteFields{} || strings.TrimSpace(p.body) != ""
	}
	return p.drafts != note.Fields || p.body != note.BodyMD
}

// save writes the form: the first save creates the Paper Note, the fields go
// through Update with the note's timestamp, and the body through the hash
// token. A conflict keeps the drafts and asks for a decision.
func (p *PaperPage) save(a *app.App, projectID, paperID string) {
	p.mu.Lock()
	if p.saving {
		p.mu.Unlock()
		return
	}
	note := p.note
	if note != nil && note.DeletedAt != nil {
		p.mu.Unlock()
		return
	}
	p.saving, p.saveErr = true, ""
	p.mu.Unlock()

	fields, body := p.drafts, p.body
	api := p.Services.Paper
	a.RunTask(i18n.T("pages.paper.task_save"), func(ctx context.Context, t *app.Task) error {
		current := note
		created := false
		if current == nil {
			got, err := api.CreatePaperNote(ctx, projectID, paperID)
			if err != nil {
				p.failSave(a, paperID, err)
				return nil
			}
			current, created = &got, true
		}
		if current.Fields != fields {
			got, err := api.UpdatePaperNote(ctx, current.NoteID, literature.PaperNotePatch{Fields: &fields}, current.UpdatedAt)
			if err != nil {
				p.failSave(a, paperID, err)
				return nil
			}
			current = &got
		}
		if current.BodyMD != body {
			result, err := api.SavePaperNoteBody(ctx, current.NoteID, body, current.BodyHash)
			if err != nil {
				p.failSave(a, paperID, err)
				return nil
			}
			if result.Conflict {
				p.succeedConflict(a, paperID, result.DiskHash, current, created)
				return nil
			}
			current = &result.Note
		}
		p.succeedSave(a, paperID, current, created)
		return nil
	})
}

// succeedSave installs the saved note; a Paper Note that was just created also
// bumps the literature generation so the project table shows its badge.
func (p *PaperPage) succeedSave(a *app.App, paperID string, note *literature.PaperNote, created bool) {
	p.mu.Lock()
	p.saving = false
	if p.loadedID != paperID {
		p.mu.Unlock()
		a.Notify()
		return
	}
	p.note = note
	p.conflict, p.diskHash, p.saveErr = false, "", ""
	p.mu.Unlock()
	if created && p.shared != nil {
		p.shared.bump()
	}
	a.Toast(app.LevelSuccess, i18n.T("pages.paper.toast_saved"), "")
	a.Notify()
}

// failSave reports a write error without touching the drafts.
func (p *PaperPage) failSave(a *app.App, paperID string, err error) {
	p.mu.Lock()
	p.saving = false
	if p.loadedID == paperID {
		p.saveErr = errtext.Text(err)
	}
	p.mu.Unlock()
	a.Toast(app.LevelDanger, i18n.T("pages.paper.toast_save_fail"), errtext.Text(err))
	a.Notify()
}

// succeedConflict keeps the drafts and records the disk token the overwrite
// needs. The note itself still installs, so a Paper Note created by this save
// is not lost while the body waits for the decision.
func (p *PaperPage) succeedConflict(a *app.App, paperID, diskHash string, note *literature.PaperNote, created bool) {
	p.mu.Lock()
	p.saving = false
	if p.loadedID == paperID {
		p.conflict = true
		p.diskHash = diskHash
		p.saveErr = ""
		if note != nil {
			p.note = note
		}
	}
	p.mu.Unlock()
	if created && p.shared != nil {
		p.shared.bump()
	}
	a.Notify()
}

// acceptDisk reloads the Paper Note from disk and replaces the drafts with it.
func (p *PaperPage) acceptDisk(a *app.App, projectID, paperID string) {
	api := p.Services.Paper
	a.RunTask(i18n.T("pages.paper.task_reload"), func(ctx context.Context, t *app.Task) error {
		note, err := api.PaperNote(ctx, projectID, paperID)
		p.mu.Lock()
		if p.loadedID == paperID {
			if err != nil {
				p.saveErr = errtext.Text(err)
			} else {
				p.note = note
				p.conflict, p.diskHash, p.saveErr = false, "", ""
				p.draftDrop = true
			}
		}
		p.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.paper.toast_save_fail"), errtext.Text(err))
		}
		a.Notify()
		return nil
	})
}

// overwrite retries the body save with the disk token, replacing the external
// change with the buffer.
func (p *PaperPage) overwrite(a *app.App, paperID string) {
	p.mu.Lock()
	base, note := p.diskHash, p.note
	p.mu.Unlock()
	if base == "" || note == nil {
		return
	}
	body := p.body
	api := p.Services.Paper
	a.RunTask(i18n.T("pages.paper.task_save"), func(ctx context.Context, t *app.Task) error {
		result, err := api.SavePaperNoteBody(ctx, note.NoteID, body, base)
		if err != nil {
			p.failSave(a, paperID, err)
			return nil
		}
		if result.Conflict {
			p.succeedConflict(a, paperID, result.DiskHash, note, false)
			return nil
		}
		p.succeedSave(a, paperID, &result.Note, false)
		return nil
	})
}

// createLinkedNote creates a research note whose body links the paper, then
// opens it so the user writes in the editor.
func (p *PaperPage) createLinkedNote(a *app.App, projectID, paperID string) {
	p.mu.Lock()
	if p.creating {
		p.mu.Unlock()
		return
	}
	title := i18n.T("pages.paper.note_title_untitled")
	if name := strings.TrimSpace(p.paper.Metadata.Title); name != "" {
		title = i18n.Tf("pages.paper.note_title", map[string]string{"title": name})
	}
	p.creating, p.createErr = true, ""
	p.mu.Unlock()

	api := p.Services.Paper
	a.RunTask(i18n.T("pages.paper.task_note"), func(ctx context.Context, t *app.Task) error {
		note, err := api.CreateLinkedNote(ctx, projectID, paperID, title)
		p.mu.Lock()
		p.creating = false
		if err != nil {
			p.createErr = errtext.Text(err)
		}
		p.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.paper.toast_create_fail"), errtext.Text(err))
			a.Notify()
			return nil
		}
		if p.shared != nil {
			p.shared.bump()
		}
		a.Toast(app.LevelSuccess, i18n.T("pages.paper.toast_note_created"), noteTitle(note))
		a.Navigate(ports.Route{Kind: ports.RouteNote, ProjectID: projectID, EntityID: note.ID})
		return nil
	})
}

// openPDF launches the PDF in the application the workspace setting selects.
func (p *PaperPage) openPDF(a *app.App, paperID string) {
	api := p.Services.Paper
	a.RunTask(i18n.T("pages.paper.task_open"), func(ctx context.Context, t *app.Task) error {
		if err := api.OpenPaper(ctx, paperID); err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.paper.toast_open_fail"), errtext.Text(err))
		}
		return nil
	})
}

// ensureText reads the current window when its pages are not cached. It runs no
// I/O on the frame: a cached window returns at once.
func (p *PaperPage) ensureText(a *app.App, paperID string) {
	p.mu.Lock()
	if p.pdfLoading || !p.paperOK {
		p.mu.Unlock()
		return
	}
	from := p.pdfFrom
	if p.pdfFailFrom == from && p.pdfFailKind != paperTextOK {
		p.mu.Unlock()
		return
	}
	last := from + paperPagesPerView - 1
	if p.pdfCount > 0 && last > p.pdfCount {
		last = p.pdfCount
	}
	complete := true
	for n := from; n <= last; n++ {
		if !p.pdfSeen[n] {
			complete = false
			break
		}
	}
	if complete {
		p.mu.Unlock()
		return
	}
	p.pdfLoading = true
	p.mu.Unlock()

	api := p.Services.Paper
	a.RunTask(i18n.T("pages.paper.task_text"), func(ctx context.Context, t *app.Task) error {
		text, err := api.ReadPaperText(ctx, paperID, from, paperPagesPerView)
		p.mu.Lock()
		if p.loadedID != paperID {
			// Another paper opened: the newer load owns the reader state.
			p.mu.Unlock()
			a.Notify()
			return nil
		}
		p.pdfLoading = false
		if p.pdfFrom != from {
			// The window moved while this read ran; its pages are still useful
			// for the cache, so they land, but the state of the new window is
			// not overwritten.
			p.storeWindowLocked(from, text, nil)
			p.mu.Unlock()
			a.Notify()
			return nil
		}
		if text.PageCount > 0 {
			p.pdfCount = text.PageCount
		}
		p.storeWindowLocked(from, text, err)
		if err == nil {
			p.pdfFailFrom, p.pdfFailKind, p.pdfFailMsg = -1, paperTextOK, ""
		} else {
			p.pdfFailFrom = from
			switch {
			case errors.Is(err, ErrPaperCopyMissing):
				p.pdfFailKind, p.pdfFailMsg = paperTextMissing, ""
			case errors.Is(err, ErrPaperNoTextLayer):
				p.pdfFailKind, p.pdfFailMsg = paperTextScan, ""
			default:
				p.pdfFailKind, p.pdfFailMsg = paperTextFailed, errtext.Text(err)
			}
		}
		p.mu.Unlock()
		a.Notify()
		return nil
	})
}

// storeWindowLocked caches the pages a read returned and marks every page of
// the window as read only when the whole window succeeded.
func (p *PaperPage) storeWindowLocked(from int, text PaperText, err error) {
	for _, page := range text.Pages {
		blocks := append([]string(nil), page.Blocks...)
		p.pdfPages[page.PageNo] = blocks
	}
	if err != nil {
		return
	}
	last := from + paperPagesPerView - 1
	if p.pdfCount > 0 && last > p.pdfCount {
		last = p.pdfCount
	}
	for n := from; n <= last; n++ {
		if _, ok := p.pdfPages[n]; !ok {
			p.pdfPages[n] = nil
		}
		p.pdfSeen[n] = true
	}
}

// moveText shifts the reader's window by one screenful. The last window starts
// on the page that still shows the final page, so the pager never opens an
// empty window.
func (p *PaperPage) moveText(a *app.App, paperID string, delta int) {
	p.mu.Lock()
	from, count := p.pdfFrom, p.pdfCount
	next := from + delta*paperPagesPerView
	if next < 1 {
		next = 1
	}
	if delta > 0 && count > 0 {
		lastStart := ((count-1)/paperPagesPerView)*paperPagesPerView + 1
		if next > lastStart {
			next = lastStart
		}
	}
	if next == from {
		p.mu.Unlock()
		return
	}
	p.pdfFrom = next
	p.pdfFailFrom, p.pdfFailKind, p.pdfFailMsg = -1, paperTextOK, ""
	p.mu.Unlock()
	p.ensureText(a, paperID)
}

// paperSnapshot returns the paper and the load state the frame renders.
func (p *PaperPage) paperSnapshot() (literature.Paper, bool, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paper, !p.loading || p.paperOK, p.paperOK
}

// paperHeading is the page title: the paper's title, or the neutral page name
// while it loads.
func paperHeading(paper literature.Paper, ok bool) string {
	if !ok {
		return i18n.T("pages.paper.title")
	}
	if name := strings.TrimSpace(paper.Metadata.Title); name != "" {
		return name
	}
	return i18n.T("pages.paper.untitled")
}

// paperDescription is the citation line under the title, with the parts that
// exist: authors, year and venue.
func paperDescription(paper literature.Paper) string {
	md := paper.Metadata
	parts := make([]string, 0, 3)
	if md.Authors != "" {
		parts = append(parts, md.Authors)
	}
	if md.Year > 0 {
		parts = append(parts, strconv.Itoa(md.Year))
	}
	if md.PublicationTitle != "" {
		parts = append(parts, md.PublicationTitle)
	}
	return strings.Join(parts, " · ")
}

// relatedNoteTitle falls back to the neutral name for an untitled note.
func relatedNoteTitle(note PaperRelatedNote) string {
	if name := strings.TrimSpace(note.Title); name != "" {
		return name
	}
	return i18n.T("pages.notes.untitled")
}
