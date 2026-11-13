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

// SaveBody implements the SPEC-05 §5.7 save flow:
//
//  1. hash the current disk content (the client's BaseHash is the token it last
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
	diskHash, err := s.diskHash(ctx, handle, in.NoteID)
	if err != nil {
		return nil, err
	}
	submitted := []byte(in.Markdown)
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

// diskHash returns the sha256 of the current body file, or "" when the file is
// gone (a missing body has an empty token, matching Body.Hash of LoadBody).
func (s *noteService) diskHash(ctx context.Context, handle *workspace.ProjectHandle, noteID string) (string, error) {
	_, stat, err := s.noteFiles(handle).Read(ctx, noteID)
	if err == nil {
		return stat.Hash, nil
	}
	if !errors.Is(err, errs.ErrNoteFileMissing) {
		return "", err
	}
	// notefile.Read also refuses a row already flagged missing. A file can have
	// reappeared since (user restored it without running reconcile), so read it
	// directly: returning "" would let the next save silently overwrite real
	// content.
	var filePath string
	if err := handle.DB().QueryRowContext(ctx, `SELECT file_path FROM notes WHERE id = ?`, noteID).Scan(&filePath); err != nil {
		return "", fmt.Errorf("notes: disk hash %s: %w", noteID, err)
	}
	abs, err := s.ws.Paths().Abs(filePath)
	if err != nil {
		return "", err
	}
	body, err := os.ReadFile(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("notes: read %s: %w", noteID, err)
	}
	return notefile.HashBytes(body), nil
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
