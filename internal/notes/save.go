package notes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/notefile"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/workspace"
)

// SaveBody implements the SPEC-05 §5.7 save flow. The client submits the
// Markdown body only; the service joins it with the database-authoritative
// metadata (and any unknown frontmatter keys found on disk) before comparing
// and writing, because the stored token is the hash of the whole file:
//
//  1. hash the current file content (the client's BaseHash is the token it last
//     read or last saved);
//  2. equal → atomic write, refresh file_hash/file_mtime/updated_at and emit
//     exactly one merged note.updated;
//  3. different and the submitted content differs too → Conflict, nothing is
//     written;
//  4. different but the submitted content equals the disk content → accept and
//     refresh the baseline.
//
// A write failure leaves the previous file untouched (notefile.WriteAtomic
// renames a same-directory temp file into place).
func (s *noteService) SaveBody(ctx context.Context, in SaveBodyInput) (*SaveResult, error) {
	if !ids.Valid(in.NoteID) {
		return nil, fmt.Errorf("%w: %q", errs.ErrBadEntityID, in.NoteID)
	}
	note, err := s.Get(ctx, in.NoteID)
	if err != nil {
		return nil, err
	}
	if note.DeletedAt != nil {
		return nil, fmt.Errorf("%w: note %s", errs.ErrEntityDeleted, in.NoteID)
	}
	handle, err := s.noteHandle(ctx)
	if err != nil {
		return nil, err
	}
	disk, present, err := s.readBodyFile(ctx, handle, in.NoteID)
	if err != nil {
		return nil, err
	}
	diskHash := ""
	if present {
		diskHash = notefile.HashBytes(disk)
	}
	// The file keeps the row's metadata; the submitted body replaces only the
	// Markdown part. The frontmatter timestamp is the row value read before
	// this save so that case 4 still recognises an unchanged submission.
	meta, err := s.noteFileMeta(ctx, handle, note)
	if err != nil {
		return nil, err
	}
	diskMeta, _ := notefile.Split(disk)
	submitted := notefile.Join(notefile.Merge(meta, diskMeta), []byte(in.Markdown))
	submittedHash := notefile.HashBytes(submitted)
	if diskHash != in.BaseHash && submittedHash != diskHash {
		// Hand the UI the token it needs to retry with (adopt the disk) after
		// the user decides (SPEC-05 §5.7 step 3).
		return &SaveResult{
			Hash:      diskHash,
			UpdatedAt: note.UpdatedAt,
			Conflict:  true,
			DiskHash:  diskHash,
		}, nil
	}

	var stat notefile.FileStat
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		var err error
		stat, err = s.noteFiles(handle).Write(ctx, tx, in.NoteID, submitted, noteActor(in.Actor))
		return err
	})
	if err != nil {
		return nil, err
	}
	updatedAt := s.clock.Now()
	if stored, err := noteUpdatedAt(ctx, handle.DB(), in.NoteID); err == nil {
		updatedAt = stored
	}
	s.notifyIndex(ctx, in.NoteID, schema.EventNoteUpdated)
	return &SaveResult{Hash: stat.Hash, UpdatedAt: updatedAt}, nil
}

// readBodyFile reads the raw body file of a note. present is false when the
// file is gone; a row flagged file_state='missing' is not consulted, because a
// file that reappeared since must not be treated as empty.
func (s *noteService) readBodyFile(ctx context.Context, handle *workspace.ProjectHandle, noteID string) ([]byte, bool, error) {
	var filePath string
	if err := handle.DB().QueryRowContext(ctx, `SELECT file_path FROM notes WHERE id = ?`, noteID).Scan(&filePath); err != nil {
		return nil, false, fmt.Errorf("notes: read body %s: %w", noteID, err)
	}
	abs, err := s.ws.Paths().Abs(filePath)
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("notes: read %s: %w", noteID, err)
	}
	return data, true, nil
}

// noteFileMeta assembles the frontmatter a note file must carry from the notes
// row and its live tags.
func (s *noteService) noteFileMeta(ctx context.Context, handle *workspace.ProjectHandle, note *Note) (notefile.Meta, error) {
	tags, err := noteTagNames(ctx, handle.DB(), note.ID)
	if err != nil {
		return notefile.Meta{}, err
	}
	return notefile.Meta{
		ID:          note.ID,
		Kind:        string(note.Kind),
		Title:       note.Title,
		Description: note.Description,
		Tags:        tags,
		Created:     note.CreatedAt,
		Updated:     note.UpdatedAt,
	}, nil
}

// noteTagNames returns the live tag names of a note, ordered by name.
func noteTagNames(ctx context.Context, db *sql.DB, noteID string) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT t.name FROM entity_tags et JOIN tags t ON t.id = et.tag_id
		 WHERE et.entity_type = 'note' AND et.entity_id = ? AND t.deleted_at IS NULL
		 ORDER BY t.name`, noteID)
	if err != nil {
		return nil, fmt.Errorf("notes: tags of %s: %w", noteID, err)
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("notes: tags of %s: %w", noteID, err)
		}
		tags = append(tags, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notes: tags of %s: %w", noteID, err)
	}
	return tags, nil
}

// noteUpdatedAt reads the row timestamp so SaveResult carries the same instant
// the database stored.
func noteUpdatedAt(ctx context.Context, db *sql.DB, noteID string) (time.Time, error) {
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT updated_at FROM notes WHERE id = ?`, noteID).Scan(&raw); err != nil {
		return time.Time{}, err
	}
	return clock.Parse(raw)
}
