package notes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/indexhook"
	"researchos/internal/foundation/notefile"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/trash"
	"researchos/internal/foundation/workspace"
)

// Note list limits (SPEC-05 §5.3: default 100, cap 500; the record service
// applies the same rule to table rows).
const (
	noteListDefaultLimit = 100
	noteListMaxLimit     = 500
)

// noteService implements NoteService for one project. It resolves the project
// database lazily through the workspace so it stays valid across re-opens.
type noteService struct {
	ws        *workspace.Workspace
	projectID string
	clock     clock.Clock
	indexer   indexhook.Indexer
	logger    workspace.Logger
}

var _ NoteService = (*noteService)(nil)

// NewNoteService binds the note service to one project of ws.
func NewNoteService(ws *workspace.Workspace, projectID string, opt ServiceOptions) *noteService {
	if opt.Clock == nil {
		opt.Clock = clock.System{}
	}
	return &noteService{
		ws:        ws,
		projectID: projectID,
		clock:     opt.Clock,
		indexer:   opt.Indexer,
		logger:    opt.Logger,
	}
}

// noteActor treats an empty actor as the local user (SPEC-05 inputs are shared
// by UI and MCP; the UI path always acts as user).
func noteActor(a events.Actor) events.Actor {
	if a == "" {
		return schema.ActorUser
	}
	return a
}

// noteHandle resolves the project database handle.
func (s *noteService) noteHandle(ctx context.Context) (*workspace.ProjectHandle, error) {
	return s.ws.Project(ctx, s.projectID)
}

// noteFiles returns the note-file store bound to handle.
func (s *noteService) noteFiles(handle *workspace.ProjectHandle) *notefile.Store {
	return notefile.New(s.projectID, handle.DB(), s.ws.Paths(), handle.Events(), s.clock)
}

// Create writes the body file and the notes row in one transaction
// (notefile.Create emits entity.created and removes the file when the DB write
// fails).
func (s *noteService) Create(ctx context.Context, in CreateNoteInput) (*Note, error) {
	kind := in.Kind
	if kind == "" {
		kind = KindScratchpad
	}
	if !kind.Valid() {
		return nil, fmt.Errorf("notes: invalid note kind %q", kind)
	}
	handle, err := s.noteHandle(ctx)
	if err != nil {
		return nil, err
	}
	var noteID string
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		res, err := s.noteFiles(handle).Create(ctx, tx, notefile.CreateRequest{
			ProjectID:   s.projectID,
			Kind:        schema.NoteKind(kind),
			Title:       in.Title,
			Description: in.Description,
			Body:        []byte(in.Body),
			Actor:       noteActor(in.Actor),
		})
		if err != nil {
			return err
		}
		noteID = res.NoteID
		return nil
	})
	if err != nil {
		return nil, err
	}
	note, err := s.Get(ctx, noteID)
	if err != nil {
		return nil, err
	}
	s.notifyIndex(ctx, noteID, schema.EventEntityCreated)
	return note, nil
}

// Get reads one note row, including soft-deleted ones (the trash view and the
// reconcile panel need them).
func (s *noteService) Get(ctx context.Context, noteID string) (*Note, error) {
	if !ids.Valid(noteID) {
		return nil, fmt.Errorf("%w: %q", errs.ErrBadEntityID, noteID)
	}
	handle, err := s.noteHandle(ctx)
	if err != nil {
		return nil, err
	}
	return noteGet(ctx, handle.DB(), noteID)
}

// noteGet reads one notes row by id.
func noteGet(ctx context.Context, db *sql.DB, noteID string) (*Note, error) {
	row := db.QueryRowContext(ctx,
		`SELECT id, title, description, file_path, kind, created_at, updated_at, deleted_at, file_hash, file_state
		 FROM notes WHERE id = ?`, noteID)
	note, err := noteScanRow(row)
	if err != nil {
		return nil, fmt.Errorf("notes: get %s: %w", noteID, err)
	}
	return note, nil
}

// noteScanRow decodes one notes row.
func noteScanRow(row interface{ Scan(dest ...any) error }) (*Note, error) {
	var (
		note      Note
		kind      string
		createdAt string
		updatedAt string
		deletedAt sql.NullString
		fileHash  sql.NullString
		fileState string
	)
	if err := row.Scan(&note.ID, &note.Title, &note.Description, &note.FilePath, &kind, &createdAt, &updatedAt,
		&deletedAt, &fileHash, &fileState); err != nil {
		return nil, err
	}
	note.Kind = NoteKind(kind)
	note.ContentHash = fileHash.String
	note.BodyState = BodyState(fileState)
	var err error
	note.CreatedAt, err = clock.Parse(createdAt)
	if err != nil {
		return nil, fmt.Errorf("notes: created_at: %w", err)
	}
	note.UpdatedAt, err = clock.Parse(updatedAt)
	if err != nil {
		return nil, fmt.Errorf("notes: updated_at: %w", err)
	}
	if deletedAt.Valid {
		at, err := clock.Parse(deletedAt.String)
		if err != nil {
			return nil, fmt.Errorf("notes: deleted_at: %w", err)
		}
		note.DeletedAt = &at
	}
	return &note, nil
}

// List returns notes ordered by updated_at DESC (Scratchpad order), with the
// unpaged total.
func (s *noteService) List(ctx context.Context, q ListNotesQuery) (NotePage, error) {
	handle, err := s.noteHandle(ctx)
	if err != nil {
		return NotePage{}, err
	}
	var (
		where []string
		args  []any
	)
	if q.Kind != "" {
		if !q.Kind.Valid() {
			return NotePage{}, fmt.Errorf("notes: invalid note kind %q", q.Kind)
		}
		where = append(where, "kind = ?")
		args = append(args, string(q.Kind))
	}
	if q.TitleLike != "" {
		// Case-insensitive substring search; CJK compares as plain text.
		where = append(where, "instr(lower(title), lower(?)) > 0")
		args = append(args, q.TitleLike)
	}
	if !q.IncludeDeleted {
		where = append(where, "deleted_at IS NULL")
	}
	if len(q.TagIDs) > 0 {
		where = append(where, `EXISTS (SELECT 1 FROM entity_tags et
			WHERE et.entity_type = 'note' AND et.entity_id = notes.id AND et.tag_id IN (`+
			notePlaceholders(len(q.TagIDs))+`))`)
		for _, tagID := range q.TagIDs {
			args = append(args, tagID)
		}
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := handle.DB().QueryRowContext(ctx, `SELECT count(*) FROM notes`+clause, args...).Scan(&total); err != nil {
		return NotePage{}, fmt.Errorf("notes: count: %w", err)
	}
	limit := q.Limit
	switch {
	case limit <= 0:
		limit = noteListDefaultLimit
	case limit > noteListMaxLimit:
		limit = noteListMaxLimit
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	rows, err := handle.DB().QueryContext(ctx,
		`SELECT id, title, description, file_path, kind, created_at, updated_at, deleted_at, file_hash, file_state
		 FROM notes`+clause+` ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return NotePage{}, fmt.Errorf("notes: list: %w", err)
	}
	defer rows.Close()
	var out []*Note
	for rows.Next() {
		note, err := noteScanRow(rows)
		if err != nil {
			return NotePage{}, fmt.Errorf("notes: list: %w", err)
		}
		out = append(out, note)
	}
	if err := rows.Err(); err != nil {
		return NotePage{}, fmt.Errorf("notes: list: %w", err)
	}
	return NotePage{Notes: out, Total: total}, nil
}

// LoadBody reads the Markdown body without its frontmatter. A missing file is
// not an error: it returns Body{State: missing} and records file_state='missing'
// once (SPEC-05 §5.1). Hash stays the token of the whole file, frontmatter
// included, because that is what a save conflict compares.
func (s *noteService) LoadBody(ctx context.Context, noteID string) (*Body, error) {
	note, err := s.Get(ctx, noteID)
	if err != nil {
		return nil, err
	}
	handle, err := s.noteHandle(ctx)
	if err != nil {
		return nil, err
	}
	body, stat, err := s.noteFiles(handle).Read(ctx, noteID)
	if errors.Is(err, errs.ErrNoteFileMissing) {
		if note.BodyState != BodyMissing {
			if err := noteMarkFileState(ctx, handle.DB(), noteID, BodyMissing); err != nil {
				return nil, err
			}
		}
		return &Body{NoteID: noteID, State: BodyMissing}, nil
	}
	if err != nil {
		return nil, err
	}
	_, markdown := notefile.Split(body)
	return &Body{NoteID: noteID, Markdown: string(markdown), Hash: stat.Hash, State: BodyOK}, nil
}

// noteMarkFileState flips notes.file_state without bumping updated_at: it
// records a storage fact, not a content change (SPEC-05 §5.1).
func noteMarkFileState(ctx context.Context, db *sql.DB, noteID string, state BodyState) error {
	if _, err := db.ExecContext(ctx,
		`UPDATE notes SET file_state = ? WHERE id = ? AND file_state <> ?`,
		string(state), noteID, string(state)); err != nil {
		return fmt.Errorf("notes: mark %s as %s: %w", noteID, state, err)
	}
	return nil
}

// UpdateTitle changes only the metadata title (D-05-01), in the notes row and
// in the file frontmatter, and emits one merged note.updated event. An
// unchanged title writes nothing.
func (s *noteService) UpdateTitle(ctx context.Context, noteID, title string, actor events.Actor) (*Note, error) {
	return s.updateMeta(ctx, noteID, title, nil, actor, "更新标题")
}

// UpdateMeta replaces the title and description of a note (D-05-01): the row
// and the file frontmatter are written in one transaction, the body and any
// unknown frontmatter keys are preserved. An unchanged write emits nothing.
func (s *noteService) UpdateMeta(ctx context.Context, in UpdateMetaInput) (*Note, error) {
	return s.updateMeta(ctx, in.NoteID, in.Title, &in.Description, in.Actor, "更新笔记元数据")
}

// updateMeta is the shared metadata write. description nil keeps the current
// description (UpdateTitle). The file is refreshed through notefile.RewriteMeta,
// which is a no-op when the body file is gone: metadata stays editable on a
// note whose body was lost.
func (s *noteService) updateMeta(ctx context.Context, noteID, title string, description *string, actor events.Actor, summary string) (*Note, error) {
	if !ids.Valid(noteID) {
		return nil, fmt.Errorf("%w: %q", errs.ErrBadEntityID, noteID)
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("notes: update meta %s: title must not be empty", noteID)
	}
	handle, err := s.noteHandle(ctx)
	if err != nil {
		return nil, err
	}
	changed := false
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		var (
			currentTitle       string
			currentDescription string
			deleted            sql.NullString
		)
		if err := tx.QueryRowContext(ctx,
			`SELECT title, description, deleted_at FROM notes WHERE id = ?`, noteID).
			Scan(&currentTitle, &currentDescription, &deleted); err != nil {
			return fmt.Errorf("notes: update meta %s: %w", noteID, err)
		}
		if deleted.Valid {
			return fmt.Errorf("%w: note %s", errs.ErrEntityDeleted, noteID)
		}
		newDescription := currentDescription
		if description != nil {
			newDescription = *description
		}
		if currentTitle == title && currentDescription == newDescription {
			return nil
		}
		changed = true
		now := s.clock.Now()
		if _, err := tx.ExecContext(ctx,
			`UPDATE notes SET title = ?, description = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
			title, newDescription, clock.Format(now), noteID); err != nil {
			return fmt.Errorf("notes: update meta %s: %w", noteID, err)
		}
		if err := s.noteFiles(handle).RewriteMeta(ctx, tx, noteID); err != nil {
			return err
		}
		return handle.Events().EmitNoteUpdated(ctx, tx, events.NoteUpdatedEvent{
			NoteID:  noteID,
			Summary: summary,
			Actor:   noteActor(actor),
			At:      now,
		})
	})
	if err != nil {
		return nil, err
	}
	note, err := s.Get(ctx, noteID)
	if err != nil {
		return nil, err
	}
	if changed {
		s.notifyIndex(ctx, noteID, schema.EventNoteUpdated)
	}
	return note, nil
}

// SoftDelete moves the note into the trash. The Markdown file and the
// attachment directory are preserved for restore (SPEC-05 §5.1). The event is
// emitted by trash.SoftDelete only.
func (s *noteService) SoftDelete(ctx context.Context, noteID string, actor events.Actor) error {
	handle, err := s.noteHandle(ctx)
	if err != nil {
		return err
	}
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		return s.trash(handle).SoftDelete(ctx, tx, trash.Ref{Type: schema.EntityNote, ID: noteID}, noteActor(actor))
	})
	if err != nil {
		return err
	}
	s.notifyIndex(ctx, noteID, schema.EventEntityDeleted)
	return nil
}

// Restore clears deleted_at, flags a still-lost body file as missing and lets
// the index pick the note up again (SPEC-05 §5.1). The event is emitted by
// trash.Restore only.
func (s *noteService) Restore(ctx context.Context, noteID string, actor events.Actor) error {
	handle, err := s.noteHandle(ctx)
	if err != nil {
		return err
	}
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		if err := s.trash(handle).Restore(ctx, tx, trash.Ref{Type: schema.EntityNote, ID: noteID}, noteActor(actor)); err != nil {
			return err
		}
		var filePath string
		if err := tx.QueryRowContext(ctx, `SELECT file_path FROM notes WHERE id = ?`, noteID).Scan(&filePath); err != nil {
			return fmt.Errorf("notes: restore %s: %w", noteID, err)
		}
		abs, err := s.ws.Paths().Abs(filePath)
		if err != nil {
			return err
		}
		if _, statErr := os.Stat(abs); errors.Is(statErr, fs.ErrNotExist) {
			if _, err := tx.ExecContext(ctx,
				`UPDATE notes SET file_state = 'missing' WHERE id = ? AND file_state <> 'missing'`,
				noteID); err != nil {
				return fmt.Errorf("notes: restore %s: %w", noteID, err)
			}
		} else if statErr != nil {
			return fmt.Errorf("notes: restore %s: %w", noteID, statErr)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.notifyIndex(ctx, noteID, schema.EventEntityRestored)
	return nil
}

// trash binds the foundation trash service to handle. The confirmation service
// is only needed by Purge/Empty, which SPEC-05 does not expose.
func (s *noteService) trash(handle *workspace.ProjectHandle) *trash.Service {
	return trash.New(s.projectID, handle.DB(), s.ws.Paths(), handle.Events(), nil, s.clock)
}

// PromoteToDatabase makes the note the detail page of a new record. The body
// file is untouched and note.kind keeps its value (D-05-04).
func (s *noteService) PromoteToDatabase(ctx context.Context, in PromoteInput) (*Record, error) {
	if !ids.Valid(in.NoteID) {
		return nil, fmt.Errorf("%w: note %q", errs.ErrBadEntityID, in.NoteID)
	}
	if !ids.Valid(in.DatabaseID) {
		return nil, fmt.Errorf("%w: database %q", errs.ErrBadEntityID, in.DatabaseID)
	}
	handle, err := s.noteHandle(ctx)
	if err != nil {
		return nil, err
	}
	var record *Record
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		var (
			noteTitle string
			deleted   sql.NullString
		)
		if err := tx.QueryRowContext(ctx, `SELECT title, deleted_at FROM notes WHERE id = ?`, in.NoteID).
			Scan(&noteTitle, &deleted); err != nil {
			return fmt.Errorf("notes: promote %s: %w", in.NoteID, err)
		}
		if deleted.Valid {
			return fmt.Errorf("%w: note %s", errs.ErrEntityDeleted, in.NoteID)
		}

		var dbDeleted sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT deleted_at FROM research_databases WHERE id = ?`, in.DatabaseID).
			Scan(&dbDeleted); err != nil {
			return fmt.Errorf("notes: promote %s: %w", in.NoteID, err)
		}
		if dbDeleted.Valid {
			return fmt.Errorf("%w: database %s", errs.ErrEntityDeleted, in.DatabaseID)
		}

		// The live partial unique index (0270) only constrains rows with
		// deleted_at IS NULL; a trashed record releases the note page
		// (SPEC-05 §4.2, D-05-04 round-trip).
		var existing string
		switch err := tx.QueryRowContext(ctx,
			`SELECT id FROM db_records WHERE note_id = ? AND deleted_at IS NULL`, in.NoteID).Scan(&existing); {
		case err == nil:
			return fmt.Errorf("notes: note %s is already the page of record %s", in.NoteID, existing)
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("notes: promote %s: %w", in.NoteID, err)
		}

		var titleFieldID string
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM db_fields WHERE database_id = ? AND role = 'title' AND deleted_at IS NULL`,
			in.DatabaseID).Scan(&titleFieldID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("notes: database %s has no title field", in.DatabaseID)
			}
			return fmt.Errorf("notes: promote %s: %w", in.NoteID, err)
		}

		var position int
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(sort_order), -1) + 1 FROM db_records WHERE database_id = ?`,
			in.DatabaseID).Scan(&position); err != nil {
			return fmt.Errorf("notes: promote %s: %w", in.NoteID, err)
		}

		values, err := json.Marshal(map[string]string{titleFieldID: noteTitle})
		if err != nil {
			return fmt.Errorf("notes: promote %s: %w", in.NoteID, err)
		}
		recordID, err := ids.New(s.clock)
		if err != nil {
			return fmt.Errorf("notes: promote %s: %w", in.NoteID, err)
		}
		now := s.clock.Now()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO db_records(id, database_id, note_id, title, data, sort_order, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			recordID, in.DatabaseID, in.NoteID, noteTitle, string(values), position,
			clock.Format(now), clock.Format(now)); err != nil {
			return fmt.Errorf("notes: promote %s: %w", in.NoteID, err)
		}
		if err := handle.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventEntityCreated,
			EntityType: schema.EntityDBRecord,
			EntityID:   recordID,
			Summary:    "加入数据库：" + noteTitle,
			Actor:      noteActor(in.Actor),
			At:         now,
		}); err != nil {
			return err
		}
		record = &Record{
			ID:         recordID,
			DatabaseID: in.DatabaseID,
			NoteID:     in.NoteID,
			Values:     json.RawMessage(values),
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return record, nil
}

// DetachFromDatabase soft-deletes the record and, when deleteNote is true, its
// backing note in the same transaction (D-05-05). Files are preserved by the
// trash service.
func (s *noteService) DetachFromDatabase(ctx context.Context, recordID string, deleteNote bool, actor events.Actor) error {
	if !ids.Valid(recordID) {
		return fmt.Errorf("%w: %q", errs.ErrBadEntityID, recordID)
	}
	handle, err := s.noteHandle(ctx)
	if err != nil {
		return err
	}
	var noteID string
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		var note sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT note_id FROM db_records WHERE id = ?`, recordID).Scan(&note); err != nil {
			return fmt.Errorf("notes: detach %s: %w", recordID, err)
		}
		noteID = note.String
		svc := s.trash(handle)
		if err := svc.SoftDelete(ctx, tx, trash.Ref{Type: schema.EntityDBRecord, ID: recordID}, noteActor(actor)); err != nil {
			return err
		}
		if deleteNote && note.Valid {
			return svc.SoftDelete(ctx, tx, trash.Ref{Type: schema.EntityNote, ID: note.String}, noteActor(actor))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if deleteNote && noteID != "" {
		s.notifyIndex(ctx, noteID, schema.EventEntityDeleted)
	}
	return nil
}

// Document assembles the indexable note shape used by indexers (SPEC-05 §2,
// O-14). A missing body yields an empty Markdown field.
func (s *noteService) Document(ctx context.Context, noteID string) (*NoteDocument, error) {
	note, err := s.Get(ctx, noteID)
	if err != nil {
		return nil, err
	}
	handle, err := s.noteHandle(ctx)
	if err != nil {
		return nil, err
	}
	doc := &NoteDocument{ID: note.ID, Title: note.Title}
	if body, err := s.LoadBody(ctx, noteID); err == nil && body.State == BodyOK {
		doc.Markdown = body.Markdown
	}
	rows, err := handle.DB().QueryContext(ctx,
		`SELECT t.name FROM entity_tags et JOIN tags t ON t.id = et.tag_id
		 WHERE et.entity_type = 'note' AND et.entity_id = ? AND t.deleted_at IS NULL
		 ORDER BY t.name`, noteID)
	if err != nil {
		return nil, fmt.Errorf("notes: document tags: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, fmt.Errorf("notes: document tags: %w", err)
		}
		doc.Tags = append(doc.Tags, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("notes: document tags: %w", err)
	}
	rows.Close()
	if err := handle.DB().QueryRowContext(ctx,
		`SELECT database_id FROM db_records WHERE note_id = ? AND deleted_at IS NULL`,
		noteID).Scan(&doc.DatabaseID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("notes: document database: %w", err)
	}
	return doc, nil
}

// notifyIndex calls the post-commit index hook. A hook failure is logged and
// swallowed: the business write is already committed (SPEC-01 §4.7).
func (s *noteService) notifyIndex(ctx context.Context, noteID string, typ schema.EventType) {
	if s.indexer == nil {
		return
	}
	ev := events.Activity{
		Type:       typ,
		EntityType: schema.EntityNote,
		EntityID:   noteID,
		Payload: events.Payload{
			EntityType: schema.EntityNote,
			EntityID:   noteID,
			Summary:    "index hook",
			Actor:      schema.ActorUser,
		},
	}
	if err := s.indexer.OnEntityChanged(ctx, ev); err != nil && s.logger != nil {
		s.logger.Warn("notes: index hook failed", "note_id", noteID, "event", string(typ), "error", err)
	}
}

// notePlaceholders returns n comma-separated bind parameters.
func notePlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// noteFirstLine derives the display title from a body: the first non-empty
// line, stripped of leading Markdown heading markers, capped at 80 runes
// (D-05-10, display only — never written back).
func noteFirstLine(body []byte) string {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) > 80 {
			runes = runes[:80]
		}
		return string(runes)
	}
	return ""
}
