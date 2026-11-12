package notefile

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/paths"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/storage"
)

func setupNoteStore(t *testing.T, c *clock.Fixed, loc *time.Location) (*Store, *sql.DB, paths.Paths, *events.Service, string) {
	t.Helper()
	projectID := ids.MustNew(c)
	root := filepath.Join(t.TempDir(), "ws")
	projectDir := filepath.Join(root, "projects", projectID)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := paths.New(root)
	if err != nil {
		t.Fatalf("paths.New: %v", err)
	}
	db, err := storage.OpenDatabase(context.Background(), filepath.Join(projectDir, "project.db"),
		storage.KindProject, storage.Options{Clock: c, SkipBackup: true})
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	writer := events.New(db, events.Options{Clock: c, Location: loc})
	return New(projectID, db, p, writer, c), db, p, writer, projectID
}

func inTx(t *testing.T, db *sql.DB, fn func(tx *sql.Tx) error) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("transaction body: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func createNote(t *testing.T, store *Store, db *sql.DB, c *clock.Fixed, projectID string, title string, body []byte) CreateResult {
	t.Helper()
	var res CreateResult
	inTx(t, db, func(tx *sql.Tx) error {
		var err error
		res, err = store.Create(context.Background(), tx, CreateRequest{
			ProjectID: projectID,
			Kind:      schema.NoteScratchpad,
			Title:     title,
			Body:      body,
			Actor:     schema.ActorUser,
		})
		return err
	})
	return res
}

func TestCreateReadAndAttachments(t *testing.T) {
	c := &clock.Fixed{T: time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)}
	store, db, p, writer, projectID := setupNoteStore(t, c, time.UTC)
	body := []byte("# Title\n\nbody")
	res := createNote(t, store, db, c, projectID, "Title", body)
	if !ids.Valid(res.NoteID) {
		t.Fatalf("note id = %q", res.NoteID)
	}
	if res.FilePath != p.NoteFile(projectID, res.NoteID) {
		t.Fatalf("file path = %q", res.FilePath)
	}
	abs, err := p.Abs(res.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(abs)
	if err != nil || string(onDisk) != string(body) {
		t.Fatalf("file content = %q, %v", onDisk, err)
	}
	var noteHash, noteState string
	if err := db.QueryRow(`SELECT file_hash, file_state FROM notes WHERE id = ?`, res.NoteID).Scan(&noteHash, &noteState); err != nil {
		t.Fatal(err)
	}
	if noteHash != HashBytes(body) {
		t.Fatalf("file_hash = %q, want %q", noteHash, HashBytes(body))
	}
	if noteState != "ok" {
		t.Fatalf("file_state = %q", noteState)
	}
	created, err := writer.List(context.Background(), events.Query{EventTypes: []events.Type{schema.EventEntityCreated}})
	if err != nil || len(created) != 1 {
		t.Fatalf("entity.created = %+v, %v", created, err)
	}
	if created[0].Payload.Summary != "创建笔记：Title" {
		t.Fatalf("summary = %q", created[0].Payload.Summary)
	}

	readBody, stat, err := store.Read(context.Background(), res.NoteID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(readBody) != string(body) || stat.Hash != HashBytes(body) || stat.Size != int64(len(body)) {
		t.Fatalf("Read = %q, %+v", readBody, stat)
	}
	attachments, err := store.AttachmentsDir(res.NoteID)
	if err != nil {
		t.Fatalf("AttachmentsDir: %v", err)
	}
	if info, err := os.Stat(attachments); err != nil || !info.IsDir() {
		t.Fatalf("attachments dir missing: %v", err)
	}
}

func TestAC06WriteMergesNoteUpdated(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*60*60)
	c := &clock.Fixed{T: time.Date(2026, 5, 6, 15, 0, 0, 0, loc)}
	store, db, p, writer, projectID := setupNoteStore(t, c, loc)
	res := createNote(t, store, db, c, projectID, "n", []byte("v0"))

	first := c.Now()
	var stats []FileStat
	for i, body := range []string{"v1", "v2", "v3"} {
		if i > 0 {
			c.Advance(10 * time.Minute)
		}
		bodyCopy := []byte(body)
		inTx(t, db, func(tx *sql.Tx) error {
			stat, err := store.Write(context.Background(), tx, res.NoteID, bodyCopy, schema.ActorUser)
			if err != nil {
				return err
			}
			stats = append(stats, stat)
			return nil
		})
	}
	lastWrite := c.Now()

	var (
		payloadJSON string
		createdAt   string
	)
	if err := db.QueryRow(`SELECT payload, created_at FROM activities WHERE event_type = 'note.updated'`).Scan(&payloadJSON, &createdAt); err != nil {
		t.Fatal(err)
	}
	if createdAt != clock.Format(first) {
		t.Fatalf("created_at = %q, want first write %q", createdAt, clock.Format(first))
	}
	var payload events.Payload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SaveCount != 3 || payload.LastSavedAt != clock.Format(lastWrite) {
		t.Fatalf("payload = %+v", payload)
	}
	if n, err := writer.Count(context.Background(), events.Query{EventTypes: []events.Type{schema.EventNoteUpdated}}); err != nil || n != 1 {
		t.Fatalf("note.updated rows = %d, %v", n, err)
	}

	// The last body wins and is hashed.
	abs, _ := p.Abs(res.FilePath)
	onDisk, err := os.ReadFile(abs)
	if err != nil || string(onDisk) != "v3" {
		t.Fatalf("file = %q, %v", onDisk, err)
	}
	if stats[2].Hash != HashBytes([]byte("v3")) {
		t.Fatalf("stat hash = %q", stats[2].Hash)
	}

	// Crossing the local midnight starts a second merged row.
	c.T = time.Date(2026, 5, 7, 0, 10, 0, 0, loc)
	inTx(t, db, func(tx *sql.Tx) error {
		_, err := store.Write(context.Background(), tx, res.NoteID, []byte("v4"), schema.ActorUser)
		return err
	})
	if n, err := writer.Count(context.Background(), events.Query{EventTypes: []events.Type{schema.EventNoteUpdated}}); err != nil || n != 2 {
		t.Fatalf("note.updated rows after day change = %d, %v", n, err)
	}
}

func TestAC19WriteProtectionAndMissingState(t *testing.T) {
	c := &clock.Fixed{T: time.Date(2027, 2, 3, 4, 5, 6, 0, time.UTC)}
	store, db, p, _, projectID := setupNoteStore(t, c, time.UTC)
	original := []byte("original body")
	res := createNote(t, store, db, c, projectID, "n", original)
	abs, _ := p.Abs(res.FilePath)

	if _, err := db.Exec(`UPDATE notes SET deleted_at = ? WHERE id = ?`, clock.Format(c.Now()), res.NoteID); err != nil {
		t.Fatal(err)
	}
	inTx(t, db, func(tx *sql.Tx) error {
		if _, err := store.Write(context.Background(), tx, res.NoteID, []byte("changed"), schema.ActorUser); !errors.Is(err, errs.ErrEntityDeleted) {
			t.Errorf("Write(soft-deleted) = %v, want ErrEntityDeleted", err)
		}
		return nil
	})
	onDisk, err := os.ReadFile(abs)
	if err != nil || string(onDisk) != string(original) {
		t.Fatalf("file changed after a rejected write: %q, %v", onDisk, err)
	}

	if _, err := db.Exec(`UPDATE notes SET deleted_at = NULL, file_state = 'missing' WHERE id = ?`, res.NoteID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Read(context.Background(), res.NoteID); !errors.Is(err, errs.ErrNoteFileMissing) {
		t.Fatalf("Read(missing state) = %v, want ErrNoteFileMissing", err)
	}
	if _, err := db.Exec(`UPDATE notes SET file_state = 'ok' WHERE id = ?`, res.NoteID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Read(context.Background(), res.NoteID); !errors.Is(err, errs.ErrNoteFileMissing) {
		t.Fatalf("Read(absent file) = %v, want ErrNoteFileMissing", err)
	}
}

func TestCreateFileFailureLeavesNoRow(t *testing.T) {
	c := &clock.Fixed{T: time.Date(2027, 3, 4, 5, 6, 7, 0, time.UTC)}
	store, db, _, _, projectID := setupNoteStore(t, c, time.UTC)
	base := filepath.Join(store.paths.Root(), "projects", projectID)
	blocker := filepath.Join(base, "notes")
	if err := os.RemoveAll(blocker); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, []byte("block"), 0o644); err != nil {
		t.Fatal(err)
	}
	inTx(t, db, func(tx *sql.Tx) error {
		_, err := store.Create(context.Background(), tx, CreateRequest{
			ProjectID: projectID, Kind: schema.NoteScratchpad, Title: "x", Body: []byte("b"), Actor: schema.ActorUser,
		})
		if err == nil {
			t.Error("Create succeeded although the notes path is a file")
		}
		return nil
	})
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM notes`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("notes rows = %d, want 0", count)
	}
}

func TestWriteAtomicTargetIntegrity(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.md")
	assertNoTemps := func() {
		t.Helper()
		matches, err := filepath.Glob(filepath.Join(dir, ".*.tmp-*"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 0 {
			t.Fatalf("temp files left behind: %v", matches)
		}
	}

	stat, err := WriteAtomic(target, []byte("one"))
	if err != nil {
		t.Fatalf("WriteAtomic: %v", err)
	}
	if stat.Hash != HashBytes([]byte("one")) || stat.Size != 3 {
		t.Fatalf("stat = %+v", stat)
	}
	assertNoTemps()

	if _, err := WriteAtomic(target, []byte("two")); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if body, _ := os.ReadFile(target); string(body) != "two" {
		t.Fatalf("content after overwrite = %q", body)
	}
	assertNoTemps()

	// Injected failure before the rename: the old target must remain intact.
	old := renameFile
	renameFile = func(src, dst string) error { return errors.New("injected rename failure") }
	if _, err := WriteAtomic(target, []byte("three")); err == nil {
		t.Fatal("WriteAtomic succeeded with an injected rename failure")
	}
	renameFile = old
	if body, _ := os.ReadFile(target); string(body) != "two" {
		t.Fatalf("target changed before the rename: %q", body)
	}
	assertNoTemps()

	// A directory at the target path makes the rename fail; no partial file and
	// no temp file may remain.
	dirTarget := filepath.Join(dir, "sub")
	if err := os.MkdirAll(dirTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteAtomic(dirTarget, []byte("x")); err == nil {
		t.Fatal("WriteAtomic replaced a directory")
	}
	if info, err := os.Stat(dirTarget); err != nil || !info.IsDir() {
		t.Fatalf("the directory target was damaged: %v", err)
	}
	assertNoTemps()
}
