package pages

// Paper is the in-app paper page of one project paper: the embedded PDF
// reader, the Paper Note and the research notes that link the paper. Every
// database, file and web view read runs in a managed task, so the frame only
// renders cached state; switching papers reloads everything the page shows.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"

	mg "github.com/egoist/mygo"
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
	// paperNotesPanelWidth is the width of the notes panel beside the reader.
	// It keeps the structured fields readable in one column.
	paperNotesPanelWidth float32 = 420
	// paperNoteFieldHeight is the height of one structured note field, and
	// paperNoteBodyHeight the height of the Markdown body.
	paperNoteFieldHeight float32 = 96
	paperNoteBodyHeight  float32 = 288
)

// paperCardChrome is the room a card's header and padding take above its body:
// the panel matches the reader card so both end at the same edge.
func paperCardChrome() float32 {
	return tokens.PanelPadding*2 +
		tokens.Typography.CardTitle.Size*1.25 + tokens.RelatedGap + 2
}

// paperReaderChrome is the vertical room everything but the reader's web view
// takes: the shell toolbar and its divider, the page margins, the title and
// citation rows, the gap before the reader card, and the card itself. MyGo text
// lines run about 1.25 times their font size, so the sum follows the tokens
// instead of fixed pixels.
func paperReaderChrome() float32 {
	return components.ToolbarHeight + 1 +
		tokens.PagePadding*2 +
		tokens.Typography.PageTitle.Size*1.25 + tokens.Space2 + tokens.Typography.Secondary.Size*1.25 +
		tokens.PageBlockGap + paperCardChrome()
}

// What the embedded reader's state is for the current paper.
const (
	// readerLoading resolves the copy and builds or navigates the web view.
	readerLoading = iota
	// readerReady has the web view showing the paper's copy.
	readerReady
	// readerMissing found no imported copy in the workspace.
	readerMissing
	// readerFailed could not embed the reader or load the copy.
	readerFailed
)

// errNoWindow reports a frame built before the composition root attached the
// window: headless tests and the wizard's first frame. The reader card then
// shows its fallback instead of embedding a web view.
var errNoWindow = errors.New("pages: no window is attached")

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

	// The embedded PDF reader. reader is one web view per page instance,
	// created lazily in a task, reused across papers through Page.LoadURL and
	// kept when the page is left: the framework hides a web view the frame
	// does not build and destroys it with the window. readerFor names the
	// paper the state describes, and readerSeq discards a task's result when
	// the user switched papers while it ran.
	reader     *mg.WebView
	readerFor  string
	readerSeq  int
	readerBusy bool
	readerKind int
	readerErr  string

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

	// The notes dock: notesOpen shows the panel beside the reader, noteOpen
	// and relatedOpen expand its two sections. Only the frame writes them,
	// so the zero value starts on the full-screen reader.
	notesOpen   bool
	noteOpen    bool
	relatedOpen bool
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
	p.load(a, project.ID, paperID)
	p.syncDrafts()

	paper, loaded, ok := p.paperSnapshot()
	if loaded && ok {
		p.ensureReader(a, paperID)
	}
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
				if ok {
					label, icon := i18n.T("pages.paper.btn_show_notes"), icons.ChevronRight
					if p.notesOpen {
						label, icon = i18n.T("pages.paper.btn_hide_notes"), icons.ChevronDown
					}
					if v.Button(label, components.ButtonOptions{
						Variant: components.VariantGhost,
						Icon:    icon,
					}).Clicked() {
						p.notesOpen = !p.notesOpen
					}
					ui.MenuButton(v.C, i18n.T("pages.paper.btn_open_pdf"), func(m *ui.Menu) {
						if m.Item(i18n.T("pages.paper.btn_open_pdf")).Chosen() {
							p.openPDF(a, paperID)
						}
						if m.Item(i18n.T("pages.paper.btn_open_with")).Chosen() {
							p.openWith(a, paperID)
						}
					})
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
		p.readerArea(v, a, project.ID, paperID)
	}})
}

// readerArea fills the viewport: the reader alone, or the reader with the notes
// panel beside it while the dock is open. Both keep the reader's height, so
// the row never leaves the viewport.
func (p *PaperPage) readerArea(v *components.UI, a *app.App, projectID, paperID string) {
	if !p.notesOpen {
		p.readerCard(v, a, paperID)
		return
	}
	ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).AlignItems(ui.Start).Children(func() {
		ui.Column(v.C).Grow(1).Children(func() {
			p.readerCard(v, a, paperID)
		})
		p.notesPanel(v, a, projectID, paperID)
	})
}

// notesPanel is the notes dock: the Paper Note form and the notes that link
// the paper, each behind a Collapsible that starts folded. The two sections
// name themselves, so the panel needs no title. It scrolls on its own, so an
// expanded form never pushes the reader.
func (p *PaperPage) notesPanel(v *components.UI, a *app.App, projectID, paperID string) {
	height := paperReaderHeight(v)
	ui.Column(v.C).Width(paperNotesPanelWidth).Height(height + paperCardChrome()).Children(func() {
		card := v.Card(components.CardOptions{}, func() {
			ui.Scroll(v.C).Fill().Children(func() {
				ui.Column(v.C).FillWidth().Gap(tokens.RelatedGap).Children(func() {
					ui.Collapsible(v.C, i18n.T("pages.paper.card_note"), &p.noteOpen, func() {
						p.noteForm(v, a, projectID, paperID)
					})
					ui.Collapsible(v.C, i18n.T("pages.paper.card_related"), &p.relatedOpen, func() {
						p.relatedList(v, a, projectID, paperID)
					})
				})
			})
		})
		card.FillHeight()
	})
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

// readerCard is the embedded PDF reader: the native web view fills the card,
// and its PDF viewer supplies the scrolling, zooming and paging, so the page
// never extracts or re-renders text. The card names the honest states of a
// missing copy and of a platform that cannot embed the reader.
func (p *PaperPage) readerCard(v *components.UI, a *app.App, paperID string) {
	p.mu.Lock()
	kind, detail, wv := p.readerKind, p.readerErr, p.reader
	p.mu.Unlock()
	height := paperReaderHeight(v)

	v.Card(components.CardOptions{Title: i18n.T("pages.paper.card_reader")}, func() {
		switch kind {
		case readerMissing:
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.FileText,
				Title:  i18n.T("pages.paper.text_missing_title"),
				Reason: i18n.T("pages.paper.text_missing_reason"),
			})
		case readerFailed:
			p.readerFallback(v, a, paperID, detail)
		case readerReady:
			if wv == nil {
				p.readerFallback(v, a, paperID, detail)
				return
			}
			ui.WebView(v.C, wv).FillWidth().Height(height).Radius(tokens.RadiusMD)
		default:
			// Resolving the copy and building the web view runs in a task;
			// the skeleton keeps the card's room until it lands.
			ui.Column(v.C).FillWidth().Height(height).Justify(ui.Center).Children(func() {
				v.Skeleton(components.SkeletonOptions{Lines: 8})
			})
		}
	})
}

// paperReaderHeight is the reader's room: the window minus the chrome around
// it, with a floor so a short window keeps a readable page. The page sits
// inside the shell's scroll container, where Fill measures an unbounded
// parent, so it reads the window like editorHeight (notes.go) and kanban's
// board do.
func paperReaderHeight(v *components.UI) float32 {
	_, h := v.C.Size()
	if h < 640 {
		return 360
	}
	return h - paperReaderChrome()
}

// readerFallback is the honest state of an environment that cannot embed the
// reader: the page explains it and offers the external applications instead.
func (p *PaperPage) readerFallback(v *components.UI, a *app.App, paperID, detail string) {
	ui.Column(v.C).FillWidth().
		Padding(tokens.PageBlockGap).
		Gap(tokens.RelatedGap).
		AlignItems(ui.Center).
		Children(func() {
			v.Icon(icons.FileText, tokens.Space6).TextColor(mygo.Color(v.T.Palette.TextMuted))
			ui.Text(v.C, i18n.T("pages.paper.reader_failed_title")).
				FontSize(tokens.Typography.CardTitle.Size).
				FontWeight(int(tokens.WeightSemibold)).
				TextColor(mygo.Color(v.T.Palette.TextPrimary))
			ui.Text(v.C, i18n.T("pages.paper.reader_failed_reason")).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.TextSecondary)).
				MaxWidth(tokens.Space12 * 8)
			if detail != "" {
				ui.Text(v.C, detail).
					FontSize(tokens.Typography.Caption.Size).
					TextColor(mygo.Color(v.T.Palette.TextMuted)).
					MaxWidth(tokens.Space12 * 8)
			}
			ui.Row(v.C).Gap(tokens.ComponentGap).Children(func() {
				v.Button(i18n.T("pages.paper.btn_open_pdf"), components.ButtonOptions{
					Variant: components.VariantPrimary,
					Icon:    icons.ExternalLink,
					OnClick: func() { p.openPDF(a, paperID) },
				})
				v.Button(i18n.T("pages.paper.btn_open_with"), components.ButtonOptions{
					Variant: components.VariantSecondary,
					OnClick: func() { p.openWith(a, paperID) },
				})
			})
		})
}

// noteForm is the Paper Note form: the six structured fields stacked in the
// panel's single column and the Markdown body, saved through the service's
// optimistic-concurrency tokens.
func (p *PaperPage) noteForm(v *components.UI, a *app.App, projectID, paperID string) {
	p.mu.Lock()
	note, saving, conflict := p.note, p.saving, p.conflict
	p.mu.Unlock()
	readOnly := note != nil && note.DeletedAt != nil
	dirty := p.draftsDirty()

	ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
		if text, tone := p.saveState(dirty); text != "" && !readOnly {
			v.StatusIndicator(tone, text)
		}
		ui.Spacer(v.C)
		if v.Button(i18n.T("pages.paper.btn_save_note"), components.ButtonOptions{
			Variant:  components.VariantPrimary,
			Size:     components.SizeSmall,
			Loading:  saving,
			Disabled: readOnly || saving || conflict || (note != nil && !dirty),
		}).Clicked() {
			p.save(a, projectID, paperID)
		}
	})
	ui.Column(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
		p.field(v, &p.drafts.Summary, "pages.paper.field_summary", readOnly)
		p.field(v, &p.drafts.Problem, "pages.paper.field_problem", readOnly)
		p.field(v, &p.drafts.Method, "pages.paper.field_method", readOnly)
		p.field(v, &p.drafts.Contribution, "pages.paper.field_contribution", readOnly)
		p.field(v, &p.drafts.Limitations, "pages.paper.field_limitations", readOnly)
		p.field(v, &p.drafts.Understanding, "pages.paper.field_understanding", readOnly)
		v.Textarea(&p.body, components.FieldOptions{
			Label:    i18n.T("pages.paper.field_body"),
			Disabled: readOnly,
		}).FillWidth().Height(paperNoteBodyHeight)
	})
}

// field is one structured note field, stacked in the panel's single column.
func (p *PaperPage) field(v *components.UI, value *string, label string, disabled bool) {
	v.Textarea(value, components.FieldOptions{Label: i18n.T(label), Disabled: disabled}).
		FillWidth().Height(paperNoteFieldHeight)
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

// relatedList lists the project notes that link the paper and offers the
// button that creates one already linked. The panel is narrow, so each note is
// one row: its title above the update time.
func (p *PaperPage) relatedList(v *components.UI, a *app.App, projectID, paperID string) {
	p.mu.Lock()
	related := append([]PaperRelatedNote(nil), p.related...)
	creating := p.creating
	p.mu.Unlock()

	ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
		ui.Text(v.C, i18n.Tf("pages.paper.card_related_desc", map[string]string{"count": strconv.Itoa(len(related))})).
			FontSize(tokens.Typography.Secondary.Size).
			TextColor(mygo.Color(v.T.Palette.TextSecondary)).
			Grow(1)
		if v.Button(i18n.T("pages.paper.btn_new_note"), components.ButtonOptions{
			Variant:  components.VariantSecondary,
			Size:     components.SizeSmall,
			Icon:     icons.Plus,
			Loading:  creating,
			Disabled: creating,
		}).Clicked() {
			p.createLinkedNote(a, projectID, paperID)
		}
	})
	if len(related) == 0 {
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.Note,
			Title:  i18n.T("pages.paper.related_empty_title"),
			Reason: i18n.T("pages.paper.related_empty_reason"),
		})
		return
	}
	ui.Column(v.C).FillWidth().Gap(tokens.Space2).Children(func() {
		for i := range related {
			note := related[i]
			row := ui.Column(v.C).FillWidth().Padding(tokens.Space2).Radius(tokens.RadiusMD)
			if row.Hovered() {
				row.Background(mygo.Color(v.T.Palette.BgHover))
			}
			row.Children(func() {
				if ui.Text(v.C, relatedNoteTitle(note)).SingleLine().FillWidth().RowClicked() {
					a.Navigate(ports.Route{Kind: ports.RouteNote, EntityID: note.ID})
				}
				if ui.Text(v.C, relativeTime(note.UpdatedAt)).
					FontSize(tokens.Typography.Caption.Size).
					TextColor(mygo.Color(v.T.Palette.TextMuted)).
					FontFeatures("tnum").
					RowClicked() {
					a.Navigate(ports.Route{Kind: ports.RouteNote, EntityID: note.ID})
				}
			})
		}
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
// papers retires the reader's state, because it belongs to the previous file;
// the web view itself is kept for reuse.
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
		// The reader state describes the paper that is leaving; a task still
		// resolving it lands as stale, and the next frame asks for the new
		// paper.
		p.readerFor = ""
		p.readerSeq++
		p.readerKind = readerLoading
		p.readerErr = ""
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

// openWith launches the PDF in an application the user picks, through the
// platform's chooser.
func (p *PaperPage) openWith(a *app.App, paperID string) {
	api := p.Services.Paper
	a.RunTask(i18n.T("pages.paper.task_open_with"), func(ctx context.Context, t *app.Task) error {
		if err := api.OpenPaperWith(ctx, paperID); err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.paper.toast_open_with_fail"), errtext.Text(err))
		}
		return nil
	})
}

// ensureReader resolves the paper's copy and brings the web view up. It starts
// one task per paper: the task checks the copy, creates the web view on the
// first paper of the page instance or navigates the existing one, and lands the
// state the card renders. Nothing here touches the file system or the window
// while the frame builds.
func (p *PaperPage) ensureReader(a *app.App, paperID string) {
	p.mu.Lock()
	if p.readerBusy || p.readerFor == paperID {
		p.mu.Unlock()
		return
	}
	p.readerFor = paperID
	p.readerSeq++
	seq := p.readerSeq
	p.readerKind, p.readerErr, p.readerBusy = readerLoading, "", true
	p.mu.Unlock()

	api := p.Services.Paper
	a.RunTask(i18n.T("pages.paper.task_reader"), func(ctx context.Context, t *app.Task) error {
		fileURL, err := api.PaperFileURL(ctx, paperID)

		// Take the state after the read: the user may have left this paper,
		// and then the result belongs to the reader state its successor owns.
		p.mu.Lock()
		stale := p.readerSeq != seq
		p.mu.Unlock()
		if stale {
			p.finishReader(seq, nil, nil)
			a.Notify()
			return nil
		}
		if err != nil {
			p.finishReader(seq, nil, err)
			a.Notify()
			return nil
		}

		p.mu.Lock()
		wv := p.reader
		p.mu.Unlock()

		// The web view is built once per page instance, in this task: the
		// window handle is read here, never while a frame builds. LoadURL
		// reuses it for every other paper.
		var loadErr error
		if wv == nil {
			var win *mg.Window
			if p.Services.Window != nil {
				win = p.Services.Window()
			}
			if win == nil {
				loadErr = errNoWindow
			} else {
				wv, loadErr = win.NewWebView(mg.WebViewOptions{URL: fileURL})
			}
		} else {
			loadErr = wv.Page().LoadURL(fileURL)
		}
		p.finishReader(seq, wv, loadErr)
		a.Notify()
		return nil
	})
}

// finishReader installs the result of a reader task. A result whose sequence
// is stale leaves the state to the newer request, but a web view it created is
// still kept, so the next paper reuses it.
func (p *PaperPage) finishReader(seq int, wv *mg.WebView, err error) {
	p.mu.Lock()
	p.readerBusy = false
	if wv != nil && p.reader == nil {
		p.reader = wv
	}
	if p.readerSeq == seq {
		switch {
		case err == nil:
			p.readerKind, p.readerErr = readerReady, ""
		case errors.Is(err, ErrPaperCopyMissing):
			p.readerKind, p.readerErr = readerMissing, ""
		default:
			p.readerKind, p.readerErr = readerFailed, errtext.Text(err)
		}
	}
	p.mu.Unlock()
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
