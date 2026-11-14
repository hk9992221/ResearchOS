package boot

// The paper page reaches one project paper through this adapter: the extracted
// PDF text of the imported copy, the Paper Note fields and the research notes
// whose Markdown links the paper. It reuses the literature module's service so
// the page and the browser always read the same workspace, and every write is
// made as the in-app user.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"researchos/internal/foundation/schema"
	"researchos/internal/literature"
	"researchos/internal/literature/library"
	"researchos/internal/notes"
	"researchos/internal/search"
	"researchos/internal/ui/pages"
)

var _ pages.PaperAPI = (*literatureModule)(nil)

// paperRelatedScanBatch is the notes page size of the reverse lookup: the notes
// package caps one page at 500, and the lookup walks every page of the project.
const paperRelatedScanBatch = 500

// libraryFor returns the library service of the open workspace.
func (m *literatureModule) libraryFor(ctx context.Context) (*library.Service, error) {
	if err := m.ensure(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lib == nil {
		return nil, literature.ErrZoteroNotConfigured
	}
	return m.lib, nil
}

// notesFor returns the note service of one project, written as the user.
func (m *literatureModule) notesFor(projectID string) notes.NoteService {
	return notes.NewNoteService(m.ws, projectID, notes.ServiceOptions{Clock: m.clock})
}

func (m *literatureModule) Paper(ctx context.Context, paperID string) (literature.Paper, error) {
	lib, err := m.libraryFor(ctx)
	if err != nil {
		return literature.Paper{}, err
	}
	return lib.GetPaper(ctx, paperID)
}

// ReadPaperText parses one window of the imported copy. The parser works on the
// workspace path, so the page never joins workspace paths itself, and the
// failures the page names in its own words are returned as its sentinels.
func (m *literatureModule) ReadPaperText(ctx context.Context, paperID string, startPage, maxPages int) (pages.PaperText, error) {
	lib, err := m.libraryFor(ctx)
	if err != nil {
		return pages.PaperText{}, err
	}
	paper, err := lib.GetPaper(ctx, paperID)
	if err != nil {
		return pages.PaperText{}, err
	}
	if paper.FilePath == "" {
		return pages.PaperText{}, pages.ErrPaperCopyMissing
	}
	abs, err := m.ws.Paths().Abs(paper.FilePath)
	if err != nil {
		return pages.PaperText{}, fmt.Errorf("boot: 解析文献副本路径: %w", err)
	}
	doc, parseErr := search.ParsePDFFile(ctx, abs, search.ParseOptions{StartPage: startPage, MaxPage: maxPages})
	out := pages.PaperText{PageCount: doc.PageCount, Pages: make([]pages.PaperPageText, 0, maxPages)}
	// Blocks arrive in document order: append each to its page and create the
	// page entry on its first block, so a blank page simply has none.
	index := make(map[int]int, maxPages)
	for _, block := range doc.Blocks {
		at, ok := index[block.PageNo]
		if !ok {
			at = len(out.Pages)
			index[block.PageNo] = at
			out.Pages = append(out.Pages, pages.PaperPageText{PageNo: block.PageNo})
		}
		out.Pages[at].Blocks = append(out.Pages[at].Blocks, block.Text)
	}
	switch {
	case parseErr == nil:
		return out, nil
	case errors.Is(parseErr, fs.ErrNotExist):
		return out, pages.ErrPaperCopyMissing
	case errors.Is(parseErr, search.ErrNoTextLayer):
		return out, pages.ErrPaperNoTextLayer
	default:
		return out, parseErr
	}
}

func (m *literatureModule) OpenPaper(ctx context.Context, paperID string) error {
	lib, err := m.libraryFor(ctx)
	if err != nil {
		return err
	}
	return lib.Open(ctx, paperID, literature.PDFImportedCopy)
}

func (m *literatureModule) PaperNote(ctx context.Context, projectID, paperID string) (*literature.PaperNote, error) {
	lib, err := m.libraryFor(ctx)
	if err != nil {
		return nil, err
	}
	return lib.PaperNotes().Get(ctx, projectID, paperID)
}

func (m *literatureModule) CreatePaperNote(ctx context.Context, projectID, paperID string) (literature.PaperNote, error) {
	lib, err := m.libraryFor(ctx)
	if err != nil {
		return literature.PaperNote{}, err
	}
	return lib.PaperNotes().Create(ctx, projectID, paperID, literature.ActorUser)
}

func (m *literatureModule) UpdatePaperNote(ctx context.Context, noteID string, patch literature.PaperNotePatch, expectedUpdatedAt time.Time) (literature.PaperNote, error) {
	lib, err := m.libraryFor(ctx)
	if err != nil {
		return literature.PaperNote{}, err
	}
	return lib.PaperNotes().Update(ctx, noteID, patch, expectedUpdatedAt, literature.ActorUser)
}

func (m *literatureModule) SavePaperNoteBody(ctx context.Context, noteID, bodyMD, baseHash string) (literature.PaperNoteSaveResult, error) {
	lib, err := m.libraryFor(ctx)
	if err != nil {
		return literature.PaperNoteSaveResult{}, err
	}
	return lib.PaperNotes().SaveBody(ctx, noteID, bodyMD, baseHash, literature.ActorUser)
}

// RelatedNotes reverse-looks-up the notes that link the paper. The link is the
// body's ros://paper/<id> reference, so the lookup reads the Markdown bodies:
// a research note written in the editor stores the reference in its body, not
// in entity_relations (only the Paper Note itself is linked there). The walk
// runs in the caller's task and skips the Paper Note and the literature
// overview, which are not research notes.
func (m *literatureModule) RelatedNotes(ctx context.Context, projectID, paperID string) ([]pages.PaperRelatedNote, error) {
	svc := m.notesFor(projectID)
	needle := "ros://paper/" + paperID
	out := make([]pages.PaperRelatedNote, 0)
	offset := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := svc.List(ctx, notes.ListNotesQuery{Limit: paperRelatedScanBatch, Offset: offset})
		if err != nil {
			return nil, err
		}
		for _, note := range page.Notes {
			if note.Kind == notes.KindPaperNote || note.Kind == notes.KindOverview {
				continue
			}
			body, err := svc.LoadBody(ctx, note.ID)
			if err != nil || body.State != notes.BodyOK {
				continue
			}
			if !strings.Contains(body.Markdown, needle) {
				continue
			}
			out = append(out, pages.PaperRelatedNote{ID: note.ID, Title: note.Title, UpdatedAt: note.UpdatedAt})
		}
		offset += len(page.Notes)
		if len(page.Notes) == 0 || offset >= page.Total {
			break
		}
	}
	return out, nil
}

// CreateLinkedNote creates a research note whose Markdown links the paper, so
// the editor and the reverse lookup both see the association.
func (m *literatureModule) CreateLinkedNote(ctx context.Context, projectID, paperID, title string) (*notes.Note, error) {
	text := strings.NewReplacer("[", `\[`, "]", `\]`).Replace(title)
	body := fmt.Sprintf("[%s](ros://paper/%s)\n", text, paperID)
	return m.notesFor(projectID).Create(ctx, notes.CreateNoteInput{
		Kind:  notes.KindScratchpad,
		Title: title,
		Body:  body,
		Actor: schema.ActorUser,
	})
}
