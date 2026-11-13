package notes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"researchos/internal/foundation/notefile"
	"researchos/internal/foundation/schema"
)

// coreNoTempFiles fails when the notes directory still holds an atomic-write
// temp file (acceptance item 2).
func coreNoTempFiles(t *testing.T, f *coreFixture) {
	t.Helper()
	dir := filepath.Dir(f.abs(f.fx.Workspace.Paths().NoteFile(f.fx.ProjectID, "probe")))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read notes dir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %s", entry.Name())
		}
	}
}

// TestCoreSaveMergesEvents covers acceptance items 2 and 3: N saves on one day
// leave one merged note.updated with save_count=N and no temp files.
func TestCoreSaveMergesEvents(t *testing.T) {
	f := coreNewFixture(t)
	note := f.create("Save target", "v0")

	base := f.loadBody(note.ID).Hash
	for i, text := range []string{"v1", "v2", "v3"} {
		res := f.save(note.ID, text, base)
		if res.Conflict {
			t.Fatalf("save %d conflicted", i)
		}
		base = res.Hash
		f.clock.Advance(time.Second)
	}
	if got := f.read(note.FilePath); got != "v3" {
		t.Fatalf("disk = %q, want v3", got)
	}
	acts := f.eventsOfType(note.ID, schema.EventNoteUpdated)
	if len(acts) != 1 {
		t.Fatalf("note.updated rows = %d, want 1", len(acts))
	}
	if acts[0].Payload.SaveCount != 3 {
		t.Fatalf("save_count = %d, want 3", acts[0].Payload.SaveCount)
	}
	if acts[0].Payload.LastSavedAt == "" {
		t.Fatal("last_saved_at is empty")
	}
	coreNoTempFiles(t, f)

	// The row hash follows the last save.
	got, err := f.notes.Get(f.ctx, note.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ContentHash != notefile.HashBytes([]byte("v3")) {
		t.Fatalf("content hash = %q", got.ContentHash)
	}
}

// TestCoreSaveMergesPerDay proves the merge key is per natural day: a save on
// the next day creates a second note.updated row.
func TestCoreSaveMergesPerDay(t *testing.T) {
	f := coreNewFixture(t)
	note := f.create("Daily", "v0")
	base := f.loadBody(note.ID).Hash
	base = f.save(note.ID, "v1", base).Hash
	f.clock.Advance(24 * time.Hour)
	f.save(note.ID, "v2", base)

	acts := f.eventsOfType(note.ID, schema.EventNoteUpdated)
	if len(acts) != 2 {
		t.Fatalf("note.updated rows = %d, want 2", len(acts))
	}
	for _, act := range acts {
		if act.Payload.SaveCount != 1 {
			t.Fatalf("save_count = %d, want 1 per day", act.Payload.SaveCount)
		}
	}
}

// TestCoreSaveConflict covers acceptance item 4: a stale BaseHash never
// overwrites an externally modified file.
func TestCoreSaveConflict(t *testing.T) {
	f := coreNewFixture(t)
	note := f.create("Conflict", "original")
	base := f.loadBody(note.ID).Hash

	external := "edited outside the app"
	f.write(note.FilePath, external)
	externalHash := notefile.HashBytes([]byte(external))

	res := f.save(note.ID, "my buffer", base)
	if !res.Conflict {
		t.Fatal("expected a conflict")
	}
	if res.DiskHash != externalHash || res.Hash != externalHash {
		t.Fatalf("hashes = %+v, want %q", res, externalHash)
	}
	if got := f.read(note.FilePath); got != external {
		t.Fatalf("disk overwritten: %q", got)
	}
	if n := len(f.eventsOfType(note.ID, schema.EventNoteUpdated)); n != 0 {
		t.Fatalf("note.updated on conflict = %d", n)
	}

	// Re-submitting with the disk token overwrites.
	res = f.save(note.ID, "my buffer", res.DiskHash)
	if res.Conflict {
		t.Fatal("retry with the disk hash conflicted")
	}
	if got := f.read(note.FilePath); got != "my buffer" {
		t.Fatalf("disk = %q", got)
	}
	if n := len(f.eventsOfType(note.ID, schema.EventNoteUpdated)); n != 1 {
		t.Fatalf("note.updated after accepted save = %d", n)
	}
}

// TestCoreSaveAcceptsSubmittedEqualToDisk covers the third §5.7 branch: the
// client re-submits exactly what is on disk, so the baseline is refreshed
// instead of reporting a conflict.
func TestCoreSaveAcceptsSubmittedEqualToDisk(t *testing.T) {
	f := coreNewFixture(t)
	note := f.create("Equal", "original")
	base := f.loadBody(note.ID).Hash

	external := "same content"
	f.write(note.FilePath, external)

	res := f.save(note.ID, external, base)
	if res.Conflict {
		t.Fatalf("unexpected conflict: %+v", res)
	}
	if res.Hash != notefile.HashBytes([]byte(external)) {
		t.Fatalf("hash = %q", res.Hash)
	}
	got, err := f.notes.Get(f.ctx, note.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ContentHash != res.Hash {
		t.Fatalf("row hash = %q, want %q", got.ContentHash, res.Hash)
	}
	if n := len(f.eventsOfType(note.ID, schema.EventNoteUpdated)); n != 1 {
		t.Fatalf("note.updated = %d, want 1", n)
	}
}

// TestCoreSaveRecreatesMissingFile checks that a lost body can be re-saved
// from a client that read the missing state (empty token).
func TestCoreSaveRecreatesMissingFile(t *testing.T) {
	f := coreNewFixture(t)
	note := f.create("Gone", "content")
	if err := os.Remove(f.abs(note.FilePath)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	body := f.loadBody(note.ID)
	if body.State != BodyMissing {
		t.Fatalf("state = %q", body.State)
	}
	res := f.save(note.ID, "recovered", body.Hash)
	if res.Conflict {
		t.Fatal("recreating a missing body conflicted")
	}
	if got := f.read(note.FilePath); got != "recovered" {
		t.Fatalf("disk = %q", got)
	}
	if got, _ := f.notes.Get(f.ctx, note.ID); got.BodyState != BodyOK {
		t.Fatalf("file_state = %q", got.BodyState)
	}
}

// TestCoreSaveReappearedFileConflicts proves a stale empty token cannot
// overwrite a body file that reappeared after the row was flagged missing.
func TestCoreSaveReappearedFileConflicts(t *testing.T) {
	f := coreNewFixture(t)
	note := f.create("Reappeared", "content")
	if err := os.Remove(f.abs(note.FilePath)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	body := f.loadBody(note.ID) // marks file_state='missing'
	if body.State != BodyMissing {
		t.Fatalf("state = %q", body.State)
	}
	f.write(note.FilePath, "restored externally")

	res := f.save(note.ID, "editor buffer", body.Hash)
	if !res.Conflict {
		t.Fatalf("expected a conflict, got %+v", res)
	}
	if got := f.read(note.FilePath); got != "restored externally" {
		t.Fatalf("reappeared file overwritten: %q", got)
	}
}

// TestCoreSaveValidation checks the argument guards.
func TestCoreSaveValidation(t *testing.T) {
	f := coreNewFixture(t)
	note := f.create("Guards", "body")
	if _, err := f.notes.SaveBody(f.ctx, SaveBodyInput{NoteID: "not-a-uuid", Markdown: "x"}); err == nil {
		t.Fatal("invalid note id accepted")
	}
	if _, err := f.notes.LoadBody(f.ctx, note.ID[:8]); err == nil {
		t.Fatal("invalid note id accepted by LoadBody")
	}
	if _, err := f.notes.Get(f.ctx, note.ID[:8]); err == nil {
		t.Fatal("invalid note id accepted by Get")
	}
}
