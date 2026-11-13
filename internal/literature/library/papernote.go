package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/relations"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/trash"
	"researchos/internal/foundation/workspace"
	"researchos/internal/literature"
	"researchos/internal/notes"
)

const paperNoteMetaKey = "paper_note"

func libraryActor(a literature.Actor) events.Actor {
	switch a {
	case literature.ActorMCP:
		return schema.ActorMCP
	case literature.ActorAI:
		return schema.ActorAI
	default:
		return schema.ActorUser
	}
}

func (s *Service) notesFor(ctx context.Context, projectID string) (notes.NoteService, *workspace.ProjectHandle, error) {
	handle, err := s.ws.Project(ctx, projectID)
	if err != nil {
		return nil, nil, fmt.Errorf("library: 打开项目 %s: %w", projectID, err)
	}
	return notes.NewNoteService(s.ws, projectID, notes.ServiceOptions{Clock: s.clock}), handle, nil
}

func (s *Service) relationsFor(handle *workspace.ProjectHandle) *relations.Service {
	return relations.New(handle.DB(), s.ws.Global().DB(), handle.Events(), s.clock)
}

func (s *Service) trashFor(handle *workspace.ProjectHandle) *trash.Service {
	return trash.New(handle.ID, handle.DB(), s.ws.Paths(), handle.Events(), nil, s.clock)
}

type paperNoteRow struct {
	NoteID    string
	Title     string
	MetaJSON  string
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

const paperNoteColumns = `id, title, meta_json, created_at, updated_at, deleted_at`

func scanPaperNoteRow(row interface{ Scan(...any) error }) (paperNoteRow, error) {
	var (
		out       paperNoteRow
		metaRaw   sql.NullString
		created   string
		updated   string
		deletedAt sql.NullString
	)
	if err := row.Scan(&out.NoteID, &out.Title, &metaRaw, &created, &updated, &deletedAt); err != nil {
		return out, err
	}
	out.MetaJSON = metaRaw.String
	at, err := parseDBTime(created)
	if err != nil {
		return out, err
	}
	out.CreatedAt = at
	if out.UpdatedAt, err = parseDBTime(updated); err != nil {
		return out, err
	}
	if out.UpdatedAt, err = parseDBTime(updated); err != nil {
		return out, err
	}
	if deletedAt.Valid {
		t, err := parseDBTime(deletedAt.String)
		if err != nil {
			return out, err
		}
		out.DeletedAt = &t
	}
	return out, nil
}

func parseDBTime(raw string) (time.Time, error) {
	return time.Parse("2006-01-02T15:04:05.000Z", raw)
}

func decodePaperNoteFields(raw string) literature.PaperNoteFields {
	if strings.TrimSpace(raw) == "" {
		return literature.PaperNoteFields{}
	}
	var wrapper struct {
		PaperNote literature.PaperNoteFields `json:"paper_note"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err == nil && (wrapper.PaperNote != literature.PaperNoteFields{}) {
		return wrapper.PaperNote
	}
	var direct literature.PaperNoteFields
	_ = json.Unmarshal([]byte(raw), &direct)
	return direct
}

func encodePaperNoteFields(f literature.PaperNoteFields) (string, error) {
	raw, err := json.Marshal(map[string]any{paperNoteMetaKey: f})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// Get returns the Paper Note of one project/paper pair, or (nil, nil).
func (s *Service) getPaperNote(ctx context.Context, projectID, paperID string) (*literature.PaperNote, error) {
	handle, err := s.ws.Project(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("library: 打开项目 %s: %w", projectID, err)
	}
	row := handle.DB().QueryRowContext(ctx,
		`SELECT `+paperNoteColumns+` FROM notes
		  WHERE kind = 'paper_note' AND paper_id = ?
		  ORDER BY created_at, id LIMIT 1`, paperID)
	note, err := scanPaperNoteRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("library: 读取 Paper Note: %w", err)
	}
	return s.paperNoteProjection(ctx, projectID, paperID, note)
}

func (s *Service) paperNoteProjection(ctx context.Context, projectID, paperID string, row paperNoteRow) (*literature.PaperNote, error) {
	svc, handle, err := s.notesFor(ctx, projectID)
	if err != nil {
		return nil, err
	}
	body, err := svc.LoadBody(ctx, row.NoteID)
	if err != nil && !errors.Is(err, errs.ErrNoteFileMissing) {
		return nil, err
	}
	out := &literature.PaperNote{
		NoteID:    row.NoteID,
		ProjectID: projectID,
		PaperID:   paperID,
		Title:     row.Title,
		Fields:    decodePaperNoteFields(row.MetaJSON),
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
		DeletedAt: row.DeletedAt,
	}
	if body != nil {
		out.BodyMD = body.Markdown
		out.BodyHash = body.Hash
	}
	rels := s.relationsFor(handle)
	links, err := rels.From(ctx, schema.EntityNote, row.NoteID, schema.RelationRelatedTo)
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		ref := literature.EntityRef{EntityType: string(l.DstType), EntityID: l.DstID}
		switch l.DstType {
		case schema.EntityIdea:
			out.RelatedIdeas = append(out.RelatedIdeas, ref)
		case schema.EntityNote:
			out.RelatedNotes = append(out.RelatedNotes, ref)
		}
	}
	return out, nil
}

// Create makes the Paper Note for a linked paper (LIT-07).
func (s *Service) createPaperNote(ctx context.Context, projectID, paperID string, actor literature.Actor) (literature.PaperNote, error) {
	if _, err := s.activeProject(ctx, projectID); err != nil {
		return literature.PaperNote{}, err
	}
	paper, err := s.papers.Get(ctx, paperID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return literature.PaperNote{}, literature.ErrPaperNotFound
		}
		return literature.PaperNote{}, err
	}
	handle, err := s.ws.Project(ctx, projectID)
	if err != nil {
		return literature.PaperNote{}, err
	}
	var linked int
	if err := handle.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM project_papers WHERE project_id = ? AND paper_id = ?`,
		projectID, paperID).Scan(&linked); err != nil {
		return literature.PaperNote{}, err
	}
	if linked == 0 {
		return literature.PaperNote{}, literature.ErrProjectLinkMissing
	}
	// Existing live/deleted note wins over creation.
	var (
		noteID  string
		deleted sql.NullString
	)
	err = handle.DB().QueryRowContext(ctx,
		`SELECT id, deleted_at FROM notes WHERE kind='paper_note' AND paper_id = ?
		  ORDER BY created_at, id LIMIT 1`, paperID).Scan(&noteID, &deleted)
	switch {
	case err == nil && !deleted.Valid:
		return literature.PaperNote{}, literature.ErrPaperNoteExists
	case err == nil && deleted.Valid:
		return literature.PaperNote{}, literature.ErrNoteDeleted
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return literature.PaperNote{}, err
	}

	svc, _, err := s.notesFor(ctx, projectID)
	if err != nil {
		return literature.PaperNote{}, err
	}
	md, err := decodeMetadata(paper)
	if err != nil {
		return literature.PaperNote{}, err
	}
	title := fmt.Sprintf("《%s》笔记", strings.TrimSpace(md.Title))
	if strings.TrimSpace(md.Title) == "" {
		title = "文献笔记"
	}
	created, err := svc.Create(ctx, notes.CreateNoteInput{
		Kind: notes.NoteKind(schema.NotePaperNote), Title: title, Actor: libraryActor(actor),
	})
	if err != nil {
		return literature.PaperNote{}, err
	}
	metaRaw, err := encodePaperNoteFields(literature.PaperNoteFields{})
	if err != nil {
		_ = svc.SoftDelete(ctx, created.ID, libraryActor(actor))
		return literature.PaperNote{}, err
	}
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE notes SET paper_id = ?, meta_json = ?, updated_at = ? WHERE id = ?`,
			paperID, metaRaw, nowString(s.clock.Now()), created.ID); err != nil {
			return fmt.Errorf("library: 写 Paper Note 索引: %w", err)
		}
		_, err := s.relationsFor(handle).Link(ctx, tx, relations.Link{
			SrcType: schema.EntityNote, SrcID: created.ID,
			Relation: schema.RelationReferences,
			DstType:  schema.EntityPaper, DstID: paperID,
		}, libraryActor(actor))
		return err
	})
	if err != nil {
		_ = svc.SoftDelete(ctx, created.ID, libraryActor(actor))
		return literature.PaperNote{}, err
	}
	note, err := s.getPaperNote(ctx, projectID, paperID)
	if err != nil || note == nil {
		return literature.PaperNote{}, err
	}
	return *note, nil
}

// Update edits title and/or structured fields.
func (s *Service) updatePaperNote(ctx context.Context, noteID string, patch literature.PaperNotePatch, expectedUpdatedAt time.Time, actor literature.Actor) (literature.PaperNote, error) {
	rec, handle, err := s.paperNoteByID(ctx, noteID)
	if err != nil {
		return literature.PaperNote{}, err
	}
	if rec.note.DeletedAt != nil {
		return literature.PaperNote{}, literature.ErrNoteDeleted
	}
	if !expectedUpdatedAt.IsZero() && !rec.note.UpdatedAt.Equal(expectedUpdatedAt) {
		return literature.PaperNote{}, literature.ErrSyncReportExpired
	}
	if err := s.requireWritable(ctx, handle.ID); err != nil {
		return literature.PaperNote{}, err
	}
	if patch.Title != nil {
		svc, _, err := s.notesFor(ctx, handle.ID)
		if err != nil {
			return literature.PaperNote{}, err
		}
		if _, err := svc.UpdateTitle(ctx, noteID, *patch.Title, libraryActor(actor)); err != nil {
			return literature.PaperNote{}, err
		}
	}
	if patch.Fields != nil {
		metaRaw, err := encodePaperNoteFields(*patch.Fields)
		if err != nil {
			return literature.PaperNote{}, err
		}
		err = handle.InTx(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx,
				`UPDATE notes SET meta_json = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
				metaRaw, nowString(s.clock.Now()), noteID)
			if err != nil {
				return err
			}
			if rows, _ := res.RowsAffected(); rows == 0 {
				return literature.ErrNoteDeleted
			}
			return handle.Events().EmitNoteUpdated(ctx, tx, events.NoteUpdatedEvent{
				NoteID: noteID, Summary: "更新文献笔记字段", Actor: libraryActor(actor), At: s.clock.Now(),
			})
		})
		if err != nil {
			return literature.PaperNote{}, err
		}
	}
	result, _, err := s.paperNoteByID(ctx, noteID)
	if err != nil {
		return literature.PaperNote{}, err
	}
	return *result.note, nil
}

type paperNoteRecord struct {
	note   *literature.PaperNote
	handle *workspace.ProjectHandle
}

func (s *Service) paperNoteByID(ctx context.Context, noteID string) (*paperNoteRecord, *workspace.ProjectHandle, error) {
	if !ids.Valid(noteID) {
		return nil, nil, literature.ErrPaperNoteNotFound
	}
	ids, err := s.projectIDsForNote(ctx, noteID)
	if err != nil {
		return nil, nil, err
	}
	if len(ids) == 0 {
		return nil, nil, literature.ErrPaperNoteNotFound
	}
	return s.paperNoteInProject(ctx, ids[0], noteID)
}

func (s *Service) paperNoteInProject(ctx context.Context, projectID, noteID string) (*paperNoteRecord, *workspace.ProjectHandle, error) {
	handle, err := s.ws.Project(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	var paperID string
	err = handle.DB().QueryRowContext(ctx,
		`SELECT COALESCE(paper_id, '') FROM notes WHERE id = ? AND kind = 'paper_note'`, noteID).Scan(&paperID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, literature.ErrPaperNoteNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	note, err := s.getPaperNote(ctx, projectID, paperID)
	if err != nil {
		return nil, nil, err
	}
	if note == nil {
		return nil, nil, literature.ErrPaperNoteNotFound
	}
	return &paperNoteRecord{note: note}, handle, nil
}

// projectIDsForNote scans registered project databases for a note id.
func (s *Service) projectIDsForNote(ctx context.Context, noteID string) ([]string, error) {
	rows, err := s.ws.Global().DB().QueryContext(ctx, `SELECT id FROM projects ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		handle, err := s.ws.Project(ctx, id)
		if err != nil {
			continue
		}
		var n int
		if err := handle.DB().QueryRowContext(ctx,
			`SELECT count(*) FROM notes WHERE id = ? AND kind = 'paper_note'`, noteID).Scan(&n); err == nil && n > 0 {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

// SaveBody saves the Markdown body through SPEC-05's hash token.
func (s *Service) savePaperNoteBody(ctx context.Context, noteID, bodyMD, baseHash string, actor literature.Actor) (literature.PaperNoteSaveResult, error) {
	rec, handle, err := s.paperNoteByID(ctx, noteID)
	if err != nil {
		return literature.PaperNoteSaveResult{}, err
	}
	if err := s.requireWritable(ctx, handle.ID); err != nil {
		return literature.PaperNoteSaveResult{}, err
	}
	svc, _, err := s.notesFor(ctx, handle.ID)
	if err != nil {
		return literature.PaperNoteSaveResult{}, err
	}
	res, err := svc.SaveBody(ctx, notes.SaveBodyInput{
		NoteID: noteID, Markdown: bodyMD, BaseHash: baseHash, Actor: libraryActor(actor),
	})
	if err != nil {
		return literature.PaperNoteSaveResult{}, err
	}
	out := literature.PaperNoteSaveResult{Conflict: res.Conflict, DiskHash: res.DiskHash}
	if !res.Conflict {
		refreshed, err := s.getPaperNote(ctx, rec.note.ProjectID, rec.note.PaperID)
		if err != nil || refreshed == nil {
			return out, err
		}
		out.Note = *refreshed
	}
	return out, nil
}

// ListByPaper lists Paper Notes across all registered projects.
func (s *Service) listPaperNotes(ctx context.Context, paperID string) ([]literature.PaperNoteRef, error) {
	rows, err := s.ws.Global().DB().QueryContext(ctx, `SELECT id FROM projects ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []literature.PaperNoteRef
	for rows.Next() {
		var projectID string
		if err := rows.Scan(&projectID); err != nil {
			return nil, err
		}
		handle, err := s.ws.Project(ctx, projectID)
		if err != nil {
			continue
		}
		noteRows, err := handle.DB().QueryContext(ctx,
			`SELECT id, title, updated_at, deleted_at FROM notes
			  WHERE kind='paper_note' AND paper_id = ? ORDER BY created_at, id`, paperID)
		if err != nil {
			noteRows.Close()
			return nil, err
		}
		for noteRows.Next() {
			var (
				id, title, updated string
				deleted            sql.NullString
			)
			if err := noteRows.Scan(&id, &title, &updated, &deleted); err != nil {
				noteRows.Close()
				return nil, err
			}
			at, _ := parseDBTime(updated)
			out = append(out, literature.PaperNoteRef{
				ProjectID: projectID, NoteID: id, Title: title, UpdatedAt: at, Deleted: deleted.Valid,
			})
		}
		noteRows.Close()
	}
	return out, rows.Err()
}

func (s *Service) addRelated(ctx context.Context, noteID, dstID string, dstType schema.EntityType, actor literature.Actor) error {
	rec, handle, err := s.paperNoteByID(ctx, noteID)
	if err != nil {
		return err
	}
	if err := s.requireWritable(ctx, handle.ID); err != nil {
		return err
	}
	if rec.note.DeletedAt != nil {
		return literature.ErrNoteDeleted
	}
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		_, err := s.relationsFor(handle).Link(ctx, tx, relations.Link{
			SrcType: schema.EntityNote, SrcID: noteID,
			Relation: schema.RelationRelatedTo,
			DstType:  dstType, DstID: dstID,
		}, libraryActor(actor))
		return err
	})
}

func (s *Service) removeRelated(ctx context.Context, noteID, dstID string, dstType schema.EntityType, actor literature.Actor) error {
	rec, handle, err := s.paperNoteByID(ctx, noteID)
	if err != nil {
		return err
	}
	if err := s.requireWritable(ctx, handle.ID); err != nil {
		return err
	}
	if rec.note.DeletedAt != nil {
		return literature.ErrNoteDeleted
	}
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		return s.relationsFor(handle).Unlink(ctx, tx, relations.Link{
			SrcType: schema.EntityNote, SrcID: noteID,
			Relation: schema.RelationRelatedTo,
			DstType:  dstType, DstID: dstID,
		}, libraryActor(actor))
	})
}

// AddRelatedIdea links a Paper Note to an Idea with related_to.
func (s *Service) addRelatedIdea(ctx context.Context, noteID, ideaID string, actor literature.Actor) error {
	return s.addRelated(ctx, noteID, ideaID, schema.EntityIdea, actor)
}

// RemoveRelatedIdea removes the related_to link to an Idea.
func (s *Service) removeRelatedIdea(ctx context.Context, noteID, ideaID string, actor literature.Actor) error {
	return s.removeRelated(ctx, noteID, ideaID, schema.EntityIdea, actor)
}

// AddRelatedNote links a Paper Note to another Note with related_to.
func (s *Service) addRelatedNote(ctx context.Context, noteID, targetNoteID string, actor literature.Actor) error {
	return s.addRelated(ctx, noteID, targetNoteID, schema.EntityNote, actor)
}

// RemoveRelatedNote removes the related_to link to another Note.
func (s *Service) removeRelatedNote(ctx context.Context, noteID, targetNoteID string, actor literature.Actor) error {
	return s.removeRelated(ctx, noteID, targetNoteID, schema.EntityNote, actor)
}

func nowString(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func (s *Service) requireWritable(ctx context.Context, projectID string) error {
	project, err := s.activeProject(ctx, projectID)
	if err != nil {
		return err
	}
	if project.Status == schema.ProjectArchived {
		return literature.ErrProjectArchived
	}
	return nil
}

// PaperNotes exposes the LIT-07 surface. The facade exists because the
// Service already has a Get(projectID) method for the overview.
func (s *Service) PaperNotes() literature.PaperNoteService { return &paperNoteAPI{s} }

type paperNoteAPI struct{ *Service }

var _ literature.PaperNoteService = (*paperNoteAPI)(nil)

func (a *paperNoteAPI) Get(ctx context.Context, projectID, paperID string) (*literature.PaperNote, error) {
	return a.getPaperNote(ctx, projectID, paperID)
}
func (a *paperNoteAPI) Create(ctx context.Context, projectID, paperID string, actor literature.Actor) (literature.PaperNote, error) {
	return a.createPaperNote(ctx, projectID, paperID, actor)
}
func (a *paperNoteAPI) Update(ctx context.Context, noteID string, patch literature.PaperNotePatch, expected time.Time, actor literature.Actor) (literature.PaperNote, error) {
	return a.updatePaperNote(ctx, noteID, patch, expected, actor)
}
func (a *paperNoteAPI) SaveBody(ctx context.Context, noteID, bodyMD, baseHash string, actor literature.Actor) (literature.PaperNoteSaveResult, error) {
	return a.savePaperNoteBody(ctx, noteID, bodyMD, baseHash, actor)
}
func (a *paperNoteAPI) ListByPaper(ctx context.Context, paperID string) ([]literature.PaperNoteRef, error) {
	return a.listPaperNotes(ctx, paperID)
}
func (a *paperNoteAPI) AddRelatedIdea(ctx context.Context, noteID, ideaID string, actor literature.Actor) error {
	return a.addRelatedIdea(ctx, noteID, ideaID, actor)
}
func (a *paperNoteAPI) RemoveRelatedIdea(ctx context.Context, noteID, ideaID string, actor literature.Actor) error {
	return a.removeRelatedIdea(ctx, noteID, ideaID, actor)
}
func (a *paperNoteAPI) AddRelatedNote(ctx context.Context, noteID, targetNoteID string, actor literature.Actor) error {
	return a.addRelatedNote(ctx, noteID, targetNoteID, actor)
}
func (a *paperNoteAPI) RemoveRelatedNote(ctx context.Context, noteID, targetNoteID string, actor literature.Actor) error {
	return a.removeRelatedNote(ctx, noteID, targetNoteID, actor)
}
