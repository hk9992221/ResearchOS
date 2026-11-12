package notefile

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/paths"
	"researchos/internal/foundation/schema"
)

// CreateRequest creates a note row plus its Markdown body.
type CreateRequest struct {
	ProjectID string
	Kind      schema.NoteKind
	Title     string
	Body      []byte
	Actor     events.Actor
}

// CreateResult is the new note identity and its workspace-relative path.
type CreateResult struct {
	NoteID   string
	FilePath string
}

// fileStateMissing is the notes.file_state value for a lost body file.
const fileStateMissing = "missing"

// Store is the note-file service of one project.
type Store struct {
	projectID string
	db        *sql.DB
	paths     paths.Paths
	clock     clock.Clock
	events    events.Writer
}

// New binds the note-file store to a project database and its collaborators.
func New(projectID string, db *sql.DB, p paths.Paths, writer events.Writer, c clock.Clock) *Store {
	if c == nil {
		c = clock.System{}
	}
	return &Store{projectID: projectID, db: db, paths: p, clock: c, events: writer}
}

// Create writes the body first (atomically) and then the metadata row and
// entity.created event in the caller transaction. A file failure leaves no row;
// a DB failure after the file write removes the file on a best-effort basis.
func (s *Store) Create(ctx context.Context, tx *sql.Tx, req CreateRequest) (CreateResult, error) {
	if tx == nil {
		return CreateResult{}, errors.New("notefile: Create requires a transaction")
	}
	if req.ProjectID != s.projectID {
		return CreateResult{}, fmt.Errorf("notefile: project %q does not match the store project %q", req.ProjectID, s.projectID)
	}
	if !req.Kind.Valid() {
		return CreateResult{}, fmt.Errorf("notefile: invalid note kind %q", req.Kind)
	}
	noteID, err := ids.New(s.clock)
	if err != nil {
		return CreateResult{}, fmt.Errorf("notefile: generate id: %w", err)
	}
	rel := s.paths.NoteFile(s.projectID, noteID)
	abs, err := s.paths.Abs(rel)
	if err != nil {
		return CreateResult{}, err
	}
	stat, err := WriteAtomic(abs, req.Body)
	if err != nil {
		return CreateResult{}, err
	}
	now := s.clock.Now()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO notes(id, title, file_path, kind, created_at, updated_at, file_hash, file_mtime, file_state)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'ok')`,
		noteID, req.Title, rel, string(req.Kind), clock.Format(now), clock.Format(now),
		stat.Hash, clock.Format(stat.ModTime)); err != nil {
		_ = os.Remove(abs)
		return CreateResult{}, fmt.Errorf("notefile: insert note: %w", err)
	}
	if err := s.events.Emit(ctx, tx, events.Event{
		Type:       schema.EventEntityCreated,
		EntityType: schema.EntityNote,
		EntityID:   noteID,
		Summary:    "创建笔记：" + req.Title,
		Actor:      req.Actor,
		At:         now,
	}); err != nil {
		_ = os.Remove(abs)
		return CreateResult{}, err
	}
	return CreateResult{NoteID: noteID, FilePath: rel}, nil
}

// Read returns the body and its current stat. A row marked file_state='missing'
// or an absent file yields ErrNoteFileMissing.
func (s *Store) Read(ctx context.Context, noteID string) ([]byte, FileStat, error) {
	var (
		filePath  string
		fileState string
	)
	err := s.db.QueryRowContext(ctx, `SELECT file_path, file_state FROM notes WHERE id = ?`, noteID).
		Scan(&filePath, &fileState)
	if err != nil {
		return nil, FileStat{}, fmt.Errorf("notefile: read %s: %w", noteID, err)
	}
	if fileState == fileStateMissing {
		return nil, FileStat{}, fmt.Errorf("%w: note %s", errs.ErrNoteFileMissing, noteID)
	}
	abs, err := s.paths.Abs(filePath)
	if err != nil {
		return nil, FileStat{}, err
	}
	body, err := os.ReadFile(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, FileStat{}, fmt.Errorf("%w: note %s", errs.ErrNoteFileMissing, noteID)
	}
	if err != nil {
		return nil, FileStat{}, fmt.Errorf("notefile: read %s: %w", noteID, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, FileStat{}, fmt.Errorf("%w: note %s", errs.ErrNoteFileMissing, noteID)
		}
		return nil, FileStat{}, fmt.Errorf("notefile: stat %s: %w", noteID, err)
	}
	return body, FileStat{Hash: HashBytes(body), ModTime: info.ModTime().UTC(), Size: info.Size()}, nil
}

// Write atomically replaces the body, refreshes the metadata row and emits the
// merged note.updated event. A soft-deleted note yields ErrEntityDeleted and
// the file is not touched.
func (s *Store) Write(ctx context.Context, tx *sql.Tx, noteID string, body []byte, actor events.Actor) (FileStat, error) {
	if tx == nil {
		return FileStat{}, errors.New("notefile: Write requires a transaction")
	}
	var (
		filePath  string
		deletedAt sql.NullString
	)
	err := tx.QueryRowContext(ctx, `SELECT file_path, deleted_at FROM notes WHERE id = ?`, noteID).
		Scan(&filePath, &deletedAt)
	if err != nil {
		return FileStat{}, fmt.Errorf("notefile: write %s: %w", noteID, err)
	}
	if deletedAt.Valid {
		return FileStat{}, fmt.Errorf("%w: note %s", errs.ErrEntityDeleted, noteID)
	}
	abs, err := s.paths.Abs(filePath)
	if err != nil {
		return FileStat{}, err
	}
	stat, err := WriteAtomic(abs, body)
	if err != nil {
		return FileStat{}, err
	}
	now := s.clock.Now()
	res, err := tx.ExecContext(ctx,
		`UPDATE notes SET file_hash = ?, file_mtime = ?, file_state = 'ok', updated_at = ? WHERE id = ?`,
		stat.Hash, clock.Format(stat.ModTime), clock.Format(now), noteID)
	if err != nil {
		return FileStat{}, fmt.Errorf("notefile: update note %s: %w", noteID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return FileStat{}, fmt.Errorf("notefile: update note %s: %w", noteID, err)
	}
	if rows == 0 {
		return FileStat{}, fmt.Errorf("notefile: write %s: %w", noteID, sql.ErrNoRows)
	}
	if err := s.events.EmitNoteUpdated(ctx, tx, events.NoteUpdatedEvent{
		NoteID:  noteID,
		Summary: "更新笔记",
		Actor:   actor,
		At:      now,
	}); err != nil {
		return FileStat{}, err
	}
	return stat, nil
}

// AttachmentsDir returns the absolute attachment directory of noteID, creating
// it when needed.
func (s *Store) AttachmentsDir(noteID string) (string, error) {
	if !ids.Valid(noteID) {
		return "", fmt.Errorf("%w: %q", errs.ErrBadEntityID, noteID)
	}
	abs, err := s.paths.Abs(s.paths.AttachmentsDir(s.projectID, noteID))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, paths.DirMode); err != nil {
		return "", fmt.Errorf("notefile: create attachments dir: %w", err)
	}
	return abs, nil
}
