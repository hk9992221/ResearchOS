package notes

import (
	"os"
	"testing"
	"time"

	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/notefile"
	"researchos/internal/foundation/schema"
)

// coreIssue finds the first issue of kind for noteID (empty noteID matches
// orphan issues).
func coreIssue(report *ReconcileReport, kind, noteID, path string) *ReconcileIssue {
	for i := range report.Issues {
		issue := report.Issues[i]
		if issue.Kind != kind {
			continue
		}
		if noteID != "" && issue.NoteID != noteID {
			continue
		}
		if path != "" && issue.Path != path {
			continue
		}
		return &issue
	}
	return nil
}

// TestCoreReconcileOrphan covers acceptance item 5's orphan half.
func TestCoreReconcileOrphan(t *testing.T) {
	f := coreNewFixture(t)
	rel := f.fx.Workspace.Paths().NoteFile(f.fx.ProjectID, "orphan-manual")
	content := "\n\nFirst line of the orphan\n# Heading\n"
	f.write(rel, content)

	report, err := f.recon.Scan(f.ctx, f.fx.ProjectID)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if coreIssue(report, IssueOrphanFile, "", rel) == nil {
		t.Fatalf("orphan_file not reported: %+v", report.Issues)
	}

	report, err = f.recon.Apply(f.ctx, []ReconcileDecision{{
		Kind: IssueOrphanFile, Path: rel, Action: ActionRegisterOrphan,
	}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if coreIssue(report, IssueOrphanFile, "", rel) != nil {
		t.Fatal("orphan still reported after registering it")
	}

	var noteID, kind, title, hash string
	if err := f.handle.DB().QueryRow(
		`SELECT id, kind, title, COALESCE(file_hash, '') FROM notes WHERE file_path = ?`, rel).
		Scan(&noteID, &kind, &title, &hash); err != nil {
		t.Fatalf("adopted row: %v", err)
	}
	if !ids.Valid(noteID) {
		t.Fatalf("adopted id %q is not a UUIDv7", noteID)
	}
	if kind != string(KindScratchpad) {
		t.Fatalf("kind = %q, want scratchpad", kind)
	}
	if title != "First line of the orphan" {
		t.Fatalf("title = %q", title)
	}
	if hash != notefile.HashBytes([]byte(content)) {
		t.Fatalf("hash = %q", hash)
	}
	if n := len(f.eventsOfType(noteID, schema.EventEntityCreated)); n != 1 {
		t.Fatalf("entity.created = %d", n)
	}

	// The adopted note is a normal scratchpad note: listed and editable.
	page, err := f.notes.List(f.ctx, ListNotesQuery{TitleLike: "the orphan"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Total != 1 || page.Notes[0].ID != noteID {
		t.Fatalf("scratchpad list = %+v", page)
	}
	body := f.loadBody(noteID)
	if body.State != BodyOK || body.Markdown != content {
		t.Fatalf("body = %+v", body)
	}
}

// TestCoreReconcileMissingAndRecreate covers acceptance item 5's missing half:
// Scan flags the row, LoadBody keeps it read-only, create_empty restores it.
func TestCoreReconcileMissingAndRecreate(t *testing.T) {
	f := coreNewFixture(t)
	note := f.create("Lost body", "will vanish")
	if err := os.Remove(f.abs(note.FilePath)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	before := len(f.eventsOfType(note.ID, schema.EventNoteUpdated))

	report, err := f.recon.Scan(f.ctx, f.fx.ProjectID)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if coreIssue(report, IssueMissingFile, note.ID, note.FilePath) == nil {
		t.Fatalf("missing_file not reported: %+v", report.Issues)
	}
	if got := coreMustScanString(t, f.handle.DB(), `SELECT file_state FROM notes WHERE id = ?`, note.ID); got != "missing" {
		t.Fatalf("file_state = %q, want missing", got)
	}
	if got := f.loadBody(note.ID).State; got != BodyMissing {
		t.Fatalf("body state = %q", got)
	}
	if n := len(f.eventsOfType(note.ID, schema.EventNoteUpdated)); n != before {
		t.Fatalf("scan wrote %d note.updated events", n-before)
	}

	report, err = f.recon.Apply(f.ctx, []ReconcileDecision{{
		Kind: IssueMissingFile, NoteID: note.ID, Path: note.FilePath, Action: ActionCreateEmpty,
	}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if coreIssue(report, IssueMissingFile, note.ID, "") != nil {
		t.Fatal("missing_file still reported after recreation")
	}
	if got := f.read(note.FilePath); got != "" {
		t.Fatalf("recreated file = %q, want empty", got)
	}
	if got := f.loadBody(note.ID); got.State != BodyOK || got.Markdown != "" {
		t.Fatalf("body after recreate = %+v", got)
	}
	if n := len(f.eventsOfType(note.ID, schema.EventNoteUpdated)); n != before+1 {
		t.Fatalf("note.updated after recreate = %d, want %d", n, before+1)
	}
}

// TestCoreReconcileAdoptExternal covers acceptance item 4's adopt branch:
// hash and updated_at move to the on-disk content with one note.updated.
func TestCoreReconcileAdoptExternal(t *testing.T) {
	f := coreNewFixture(t)
	note := f.create("External", "baseline")
	original, err := f.notes.Get(f.ctx, note.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	f.clock.Advance(2 * time.Second)
	external := "edited by another editor"
	f.write(note.FilePath, external)

	report, err := f.recon.Scan(f.ctx, f.fx.ProjectID)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if coreIssue(report, IssueExternalChange, note.ID, note.FilePath) == nil {
		t.Fatalf("external_change not reported: %+v", report.Issues)
	}

	report, err = f.recon.Apply(f.ctx, []ReconcileDecision{{
		Kind: IssueExternalChange, NoteID: note.ID, Path: note.FilePath, Action: ActionAdoptExternal,
	}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if coreIssue(report, IssueExternalChange, note.ID, "") != nil {
		t.Fatal("external_change still reported after adopting")
	}
	got, err := f.notes.Get(f.ctx, note.ID)
	if err != nil {
		t.Fatalf("get after adopt: %v", err)
	}
	if got.ContentHash != notefile.HashBytes([]byte(external)) {
		t.Fatalf("hash = %q", got.ContentHash)
	}
	if !got.UpdatedAt.After(original.UpdatedAt) {
		t.Fatalf("updated_at did not move: %s -> %s", original.UpdatedAt, got.UpdatedAt)
	}
	if n := len(f.eventsOfType(note.ID, schema.EventNoteUpdated)); n != 1 {
		t.Fatalf("note.updated = %d, want 1", n)
	}
	if f.read(note.FilePath) != external {
		t.Fatal("adopt rewrote the file")
	}
}

// TestCoreReconcileAttachmentReportOnly covers acceptance item 5/§5.8: an
// unreferenced attachment is reported and never deleted.
func TestCoreReconcileAttachmentReportOnly(t *testing.T) {
	f := coreNewFixture(t)
	note := f.create("With attachment", "no images yet")
	dir := f.fx.Workspace.Paths().AttachmentsDir(f.fx.ProjectID, note.ID)
	picRel := dir + "/pic.png"
	f.write(picRel, "not really a png")

	report, err := f.recon.Scan(f.ctx, f.fx.ProjectID)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if coreIssue(report, IssueOrphanAttachment, note.ID, picRel) == nil {
		t.Fatalf("orphan_attachment not reported: %+v", report.Issues)
	}

	report, err = f.recon.Apply(f.ctx, []ReconcileDecision{{
		Kind: IssueOrphanAttachment, NoteID: note.ID, Path: picRel, Action: ActionIgnore,
	}})
	if err != nil {
		t.Fatalf("apply ignore: %v", err)
	}
	if _, err := os.Stat(f.abs(picRel)); err != nil {
		t.Fatalf("ignore deleted the attachment: %v", err)
	}
	if coreIssue(report, IssueOrphanAttachment, note.ID, picRel) == nil {
		t.Fatal("ignored attachment disappeared from the report")
	}

	// Referencing the file clears the issue.
	markdown := "![shot](../attachments/" + note.ID + "/pic.png)"
	f.save(note.ID, markdown, f.loadBody(note.ID).Hash)
	report, err = f.recon.Scan(f.ctx, f.fx.ProjectID)
	if err != nil {
		t.Fatalf("scan after reference: %v", err)
	}
	if coreIssue(report, IssueOrphanAttachment, note.ID, picRel) != nil {
		t.Fatalf("referenced attachment still reported: %+v", report.Issues)
	}
}

// TestCoreReconcileFailureIsolation checks each decision commits on its own and
// a failing decision does not block the rest (SPEC-05 §5.8).
func TestCoreReconcileFailureIsolation(t *testing.T) {
	f := coreNewFixture(t)
	lost := f.create("Lost", "body")
	if err := os.Remove(f.abs(lost.FilePath)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	report, err := f.recon.Scan(f.ctx, f.fx.ProjectID)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if coreIssue(report, IssueMissingFile, lost.ID, "") == nil {
		t.Fatal("missing_file not reported")
	}

	report, err = f.recon.Apply(f.ctx, []ReconcileDecision{
		{Kind: IssueMissingFile, NoteID: ids.MustNew(f.clock), Action: ActionCreateEmpty},
		{Kind: IssueMissingFile, NoteID: lost.ID, Action: ActionCreateEmpty},
	})
	if err == nil {
		t.Fatal("failing decision was not reported")
	}
	if got := f.loadBody(lost.ID); got.State != BodyOK {
		t.Fatalf("valid decision was rolled back: %+v", got)
	}
	if coreIssue(report, IssueMissingFile, lost.ID, "") != nil {
		t.Fatal("recreated note still missing")
	}
}

// TestCoreReconcileUnknownAction rejects a malformed decision before applying
// anything.
func TestCoreReconcileUnknownAction(t *testing.T) {
	f := coreNewFixture(t)
	note := f.create("N", "body")
	if err := os.Remove(f.abs(note.FilePath)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := f.recon.Apply(f.ctx, []ReconcileDecision{{
		Kind: IssueMissingFile, NoteID: note.ID, Action: "bogus",
	}}); err == nil {
		t.Fatal("unknown action accepted")
	}
	if _, err := os.Stat(f.abs(note.FilePath)); !os.IsNotExist(err) {
		t.Fatal("apply ran before rejecting the decision")
	}
}
