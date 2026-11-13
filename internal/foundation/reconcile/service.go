// Package reconcile compares the note files on disk with the notes metadata and
// applies user-chosen repairs (SPEC-01 §5.11). It never deletes files or rows:
// the DB is the metadata authority and the Markdown file is the body authority.
package reconcile

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/notefile"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/workspace"
)

// Report compares disk and database state for one project.
type Report struct {
	ProjectID   string
	Scanned     int
	Orphans     []Orphan
	Missing     []Missing
	Modified    []Modified
	GeneratedAt time.Time
}

// Orphan is a .md file with no notes row.
type Orphan struct {
	RelPath string
	Size    int64
	ModTime time.Time
}

// Missing is an active notes row whose file is gone.
type Missing struct {
	NoteID   string
	FilePath string
	Title    string
}

// Modified is an active notes row whose file hash differs from the stored one.
type Modified struct {
	NoteID   string
	FilePath string
	DBHash   string
	DiskHash string
}

// AdoptOptions configures ActionAdopt.
type AdoptOptions struct {
	Title string
	Kind  schema.NoteKind
	Actor events.Actor
}

// ActionKind is one repair action.
type ActionKind string

const (
	// ActionAdopt registers an orphan file as a note.
	ActionAdopt ActionKind = "adopt"
	// ActionAcceptExternalEdit accepts the on-disk content hash.
	ActionAcceptExternalEdit ActionKind = "accept_external_edit"
	// ActionMarkBroken marks the missing file state.
	ActionMarkBroken ActionKind = "mark_broken"
	// ActionReattach moves a note row to a recovered file.
	ActionReattach ActionKind = "reattach"
	// ActionRecreate recreates an empty body at the recorded path.
	ActionRecreate ActionKind = "recreate"
)

// Action is one repair request.
type Action struct {
	Kind       ActionKind
	RelPath    string // Adopt
	NoteID     string // all other actions
	NewRelPath string // Reattach
	AdoptOptions
}

// ActionResult reports one action; Err only affects that item.
type ActionResult struct {
	Action Action
	NoteID string
	Err    error
}

// ApplyResult is the ordered list of action results.
type ApplyResult struct {
	Items []ActionResult
}

// Scan walks projects/<pid>/notes/**.md (no symlink following, temp files
// skipped) and compares it with the notes table. It is read-only.
func Scan(ctx context.Context, w *workspace.Workspace, projectID string) (Report, error) {
	rep := Report{ProjectID: projectID, GeneratedAt: w.Clock().Now()}
	handle, err := w.Project(ctx, projectID)
	if err != nil {
		return rep, err
	}
	p := w.Paths()
	notesDir, err := p.Abs(p.NotesDir(projectID))
	if err != nil {
		return rep, err
	}

	disk := make(map[string]Orphan)
	hashes := make(map[string]string)
	walkErr := filepath.WalkDir(notesDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // never follow symlinks
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") || strings.Contains(name, ".tmp-") || !strings.HasSuffix(name, ".md") {
			return nil
		}
		rel, err := p.Rel(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		disk[rel] = Orphan{RelPath: rel, Size: info.Size(), ModTime: info.ModTime().UTC()}
		hashes[rel] = notefile.HashBytes(body)
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.ErrNotExist) {
		return rep, fmt.Errorf("reconcile: scan notes: %w", walkErr)
	}
	rep.Scanned = len(disk)

	type noteRow struct {
		id       string
		filePath string
		title    string
		hash     sql.NullString
		state    string
		deleted  bool
	}
	rowsByPath := make(map[string]noteRow)
	var active []noteRow

	rows, err := handle.DB().QueryContext(ctx,
		`SELECT id, file_path, title, file_hash, file_state, deleted_at FROM notes`)
	if err != nil {
		return rep, fmt.Errorf("reconcile: read notes: %w", err)
	}
	for rows.Next() {
		var (
			row     noteRow
			deleted sql.NullString
		)
		if err := rows.Scan(&row.id, &row.filePath, &row.title, &row.hash, &row.state, &deleted); err != nil {
			rows.Close()
			return rep, fmt.Errorf("reconcile: read notes: %w", err)
		}
		row.deleted = deleted.Valid
		rowsByPath[row.filePath] = row
		if !row.deleted {
			active = append(active, row)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return rep, fmt.Errorf("reconcile: read notes: %w", err)
	}
	rows.Close()

	for rel, orphan := range disk {
		if _, ok := rowsByPath[rel]; !ok {
			rep.Orphans = append(rep.Orphans, orphan)
		}
	}
	for _, row := range active {
		if _, ok := disk[row.filePath]; !ok {
			rep.Missing = append(rep.Missing, Missing{NoteID: row.id, FilePath: row.filePath, Title: row.title})
			continue
		}
		diskHash := hashes[row.filePath]
		dbHash := row.hash.String
		if row.state == "missing" {
			// The file reappeared while the row was flagged missing. Surface it
			// as a reconcilable difference so any action (adopt/reattach) can
			// clear the flag; otherwise the note would stay read-only forever.
			rep.Modified = append(rep.Modified, Modified{
				NoteID: row.id, FilePath: row.filePath, DBHash: dbHash, DiskHash: diskHash,
			})
			continue
		}
		if !row.hash.Valid || dbHash != diskHash {
			rep.Modified = append(rep.Modified, Modified{
				NoteID: row.id, FilePath: row.filePath, DBHash: dbHash, DiskHash: diskHash,
			})
		}
	}

	sort.Slice(rep.Orphans, func(i, j int) bool { return rep.Orphans[i].RelPath < rep.Orphans[j].RelPath })
	sort.Slice(rep.Missing, func(i, j int) bool { return rep.Missing[i].NoteID < rep.Missing[j].NoteID })
	sort.Slice(rep.Modified, func(i, j int) bool { return rep.Modified[i].NoteID < rep.Modified[j].NoteID })
	return rep, nil
}

// Apply runs each action in its own transaction; a failing action never blocks
// the rest. It never deletes a file or a row.
func Apply(ctx context.Context, w *workspace.Workspace, projectID string, actions []Action) (ApplyResult, error) {
	var out ApplyResult
	handle, err := w.Project(ctx, projectID)
	if err != nil {
		return out, err
	}
	for _, action := range actions {
		noteID, err := applyOne(ctx, w, handle, action)
		out.Items = append(out.Items, ActionResult{Action: action, NoteID: noteID, Err: err})
	}
	return out, nil
}

func applyOne(ctx context.Context, w *workspace.Workspace, handle *workspace.ProjectHandle, action Action) (string, error) {
	switch action.Kind {
	case ActionAdopt:
		return adopt(ctx, w, handle, action)
	case ActionAcceptExternalEdit:
		return action.NoteID, acceptExternalEdit(ctx, w, handle, action.NoteID, action.Actor)
	case ActionMarkBroken:
		return action.NoteID, markBroken(ctx, handle, action.NoteID)
	case ActionReattach:
		return action.NoteID, reattach(ctx, w, handle, action.NoteID, action.NewRelPath, action.Actor)
	case ActionRecreate:
		return action.NoteID, recreate(ctx, w, handle, action.NoteID, action.Actor)
	default:
		return action.NoteID, fmt.Errorf("reconcile: unknown action %q", action.Kind)
	}
}

func adopt(ctx context.Context, w *workspace.Workspace, handle *workspace.ProjectHandle, action Action) (string, error) {
	if action.RelPath == "" {
		return "", errors.New("reconcile: adopt requires a relative path")
	}
	p := w.Paths()
	abs, err := p.Abs(action.RelPath)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("reconcile: adopt %s: %w", action.RelPath, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return "", fmt.Errorf("reconcile: adopt %s: refusing to adopt a symlink", action.RelPath)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("reconcile: adopt %s: not a regular file", action.RelPath)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("reconcile: adopt %s: %w", action.RelPath, err)
	}
	hash := notefile.HashBytes(body)
	title := deriveTitle(action.AdoptOptions.Title, body, abs)
	// Action.Kind is the action selector; the note kind comes from the embedded
	// AdoptOptions (its Kind field is shadowed by the outer one).
	kind := action.AdoptOptions.Kind
	if kind == "" {
		kind = schema.NoteScratchpad
	}
	if !kind.Valid() {
		return "", fmt.Errorf("reconcile: adopt: invalid note kind %q", kind)
	}
	c := w.Clock()
	var noteID string
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM notes WHERE file_path = ?`, action.RelPath).Scan(&one)
		switch {
		case err == nil:
			return fmt.Errorf("reconcile: adopt %s: path is already registered", action.RelPath)
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("reconcile: adopt %s: %w", action.RelPath, err)
		}
		id, err := ids.New(c)
		if err != nil {
			return err
		}
		noteID = id
		now := clock.Format(c.Now())
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO notes(id, title, file_path, kind, created_at, updated_at, file_hash, file_mtime, file_state)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'ok')`,
			id, title, action.RelPath, string(kind), now, now, hash, clock.Format(info.ModTime())); err != nil {
			return fmt.Errorf("reconcile: adopt: %w", err)
		}
		return handle.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventEntityCreated,
			EntityType: schema.EntityNote,
			EntityID:   id,
			Summary:    "创建笔记：" + title,
			Actor:      action.Actor,
			At:         c.Now(),
		})
	})
	if err != nil {
		return "", err
	}
	return noteID, nil
}

func acceptExternalEdit(ctx context.Context, w *workspace.Workspace, handle *workspace.ProjectHandle, noteID string, actor events.Actor) error {
	filePath, err := noteFilePath(ctx, handle, noteID)
	if err != nil {
		return err
	}
	abs, err := w.Paths().Abs(filePath)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: note %s", errs.ErrNoteFileMissing, noteID)
	}
	if err != nil {
		return fmt.Errorf("reconcile: accept external edit %s: %w", noteID, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("reconcile: accept external edit %s: %w", noteID, err)
	}
	hash := notefile.HashBytes(body)
	c := w.Clock()
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		now := clock.Format(c.Now())
		res, err := tx.ExecContext(ctx,
			`UPDATE notes SET file_state = 'ok', file_hash = ?, file_mtime = ?, updated_at = ? WHERE id = ?`,
			hash, clock.Format(info.ModTime()), now, noteID)
		if err != nil {
			return fmt.Errorf("reconcile: accept external edit: %w", err)
		}
		if rows, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("reconcile: accept external edit: %w", err)
		} else if rows == 0 {
			return fmt.Errorf("reconcile: accept external edit %s: %w", noteID, sql.ErrNoRows)
		}
		return handle.Events().EmitNoteUpdated(ctx, tx, events.NoteUpdatedEvent{
			NoteID: noteID, Summary: "对账采纳磁盘内容", Actor: actor, At: c.Now(),
		})
	})
}

func markBroken(ctx context.Context, handle *workspace.ProjectHandle, noteID string) error {
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE notes SET file_state = 'missing' WHERE id = ?`, noteID)
		if err != nil {
			return fmt.Errorf("reconcile: mark broken: %w", err)
		}
		if rows, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("reconcile: mark broken: %w", err)
		} else if rows == 0 {
			return fmt.Errorf("reconcile: mark broken %s: %w", noteID, sql.ErrNoRows)
		}
		return nil
	})
}

func reattach(ctx context.Context, w *workspace.Workspace, handle *workspace.ProjectHandle, noteID, newRelPath string, actor events.Actor) error {
	if newRelPath == "" {
		return errors.New("reconcile: reattach requires a new relative path")
	}
	p := w.Paths()
	abs, err := p.Abs(newRelPath)
	if err != nil {
		return err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return fmt.Errorf("reconcile: reattach %s: %w", newRelPath, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("reconcile: reattach %s: not a regular file", newRelPath)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Errorf("reconcile: reattach %s: %w", newRelPath, err)
	}
	hash := notefile.HashBytes(body)
	c := w.Clock()
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		var current string
		if err := tx.QueryRowContext(ctx, `SELECT file_path FROM notes WHERE id = ?`, noteID).Scan(&current); err != nil {
			return fmt.Errorf("reconcile: reattach %s: %w", noteID, err)
		}
		var owner string
		err := tx.QueryRowContext(ctx, `SELECT id FROM notes WHERE file_path = ?`, newRelPath).Scan(&owner)
		switch {
		case err == nil && owner != noteID:
			return fmt.Errorf("reconcile: reattach %s: path is already used by note %s", newRelPath, owner)
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("reconcile: reattach: %w", err)
		}
		now := clock.Format(c.Now())
		if _, err := tx.ExecContext(ctx,
			`UPDATE notes SET file_path = ?, file_state = 'ok', file_hash = ?, file_mtime = ?, updated_at = ? WHERE id = ?`,
			newRelPath, hash, clock.Format(info.ModTime()), now, noteID); err != nil {
			return fmt.Errorf("reconcile: reattach: %w", err)
		}
		return handle.Events().EmitNoteUpdated(ctx, tx, events.NoteUpdatedEvent{
			NoteID: noteID, Summary: "重新关联文件", Actor: actor, At: c.Now(),
		})
	})
}

func recreate(ctx context.Context, w *workspace.Workspace, handle *workspace.ProjectHandle, noteID string, actor events.Actor) error {
	filePath, err := noteFilePath(ctx, handle, noteID)
	if err != nil {
		return err
	}
	abs, err := w.Paths().Abs(filePath)
	if err != nil {
		return err
	}
	stat, err := notefile.WriteAtomic(abs, nil)
	if err != nil {
		return err
	}
	c := w.Clock()
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		now := clock.Format(c.Now())
		res, err := tx.ExecContext(ctx,
			`UPDATE notes SET file_state = 'ok', file_hash = ?, file_mtime = ?, updated_at = ? WHERE id = ?`,
			stat.Hash, clock.Format(stat.ModTime), now, noteID)
		if err != nil {
			return fmt.Errorf("reconcile: recreate: %w", err)
		}
		if rows, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("reconcile: recreate: %w", err)
		} else if rows == 0 {
			return fmt.Errorf("reconcile: recreate %s: %w", noteID, sql.ErrNoRows)
		}
		return handle.Events().EmitNoteUpdated(ctx, tx, events.NoteUpdatedEvent{
			NoteID: noteID, Summary: "重建空正文", Actor: actor, At: c.Now(),
		})
	})
}

func noteFilePath(ctx context.Context, handle *workspace.ProjectHandle, noteID string) (string, error) {
	var filePath string
	if err := handle.DB().QueryRowContext(ctx, `SELECT file_path FROM notes WHERE id = ?`, noteID).Scan(&filePath); err != nil {
		return "", fmt.Errorf("reconcile: read note %s: %w", noteID, err)
	}
	return filePath, nil
}

// deriveTitle implements the §5.11 rule: explicit title, else the first "# "
// heading, else the file name without its extension.
func deriveTitle(explicit string, body []byte, abs string) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "# ") {
			if title := strings.TrimSpace(strings.TrimPrefix(line, "# ")); title != "" {
				return title
			}
		}
	}
	base := filepath.Base(abs)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
