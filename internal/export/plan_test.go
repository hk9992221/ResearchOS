package export

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"researchos/internal/ports"
)

func TestPlanValidation(t *testing.T) {
	f, s := newFixture(t)
	dest := t.TempDir()
	projectDir := filepath.Join(f.Root, "projects", f.ProjectID)
	base := Request{ProjectID: f.ProjectID, DestDir: dest, Format: FormatZip, BaseName: "ok"}

	t.Run("destination inside project", func(t *testing.T) {
		req := base
		req.DestDir = projectDir
		if _, err := s.Plan(context.Background(), req); !errors.Is(err, ErrDestInsideProject) {
			t.Fatalf("got %v, want ErrDestInsideProject", err)
		}
	})

	t.Run("destination inside project subdirectory", func(t *testing.T) {
		req := base
		req.DestDir = filepath.Join(projectDir, "notes")
		if _, err := s.Plan(context.Background(), req); !errors.Is(err, ErrDestInsideProject) {
			t.Fatalf("got %v, want ErrDestInsideProject", err)
		}
	})

	t.Run("missing destination", func(t *testing.T) {
		req := base
		req.DestDir = filepath.Join(dest, "missing")
		if _, err := s.Plan(context.Background(), req); err == nil {
			t.Fatal("want an error for a missing destination")
		}
	})

	t.Run("relative destination", func(t *testing.T) {
		req := base
		req.DestDir = "relative/dir"
		if _, err := s.Plan(context.Background(), req); err == nil {
			t.Fatal("want an error for a relative destination")
		}
	})

	t.Run("destination is a file", func(t *testing.T) {
		file := filepath.Join(dest, "not-a-dir")
		writeTestFile(t, file, []byte("x"))
		req := base
		req.DestDir = file
		if _, err := s.Plan(context.Background(), req); err == nil {
			t.Fatal("want an error for a file destination")
		}
	})

	t.Run("unknown format", func(t *testing.T) {
		req := base
		req.Format = "rar"
		if _, err := s.Plan(context.Background(), req); err == nil {
			t.Fatal("want an error for an unknown format")
		}
	})

	t.Run("ai actor rejected", func(t *testing.T) {
		req := base
		req.Actor = ports.ActorAI
		if _, err := s.Plan(context.Background(), req); err == nil {
			t.Fatal("want an error for the ai actor")
		}
	})

	t.Run("unknown project", func(t *testing.T) {
		req := base
		req.ProjectID = "0192f3c1-1111-7000-8000-000000000000"
		_, err := s.Plan(context.Background(), req)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("got %v, want sql.ErrNoRows", err)
		}
	})

	t.Run("plan has no side effects", func(t *testing.T) {
		plan, err := s.Plan(context.Background(), base)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if _, err := os.Lstat(plan.OutputPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("output already exists: %v", err)
		}
		if _, err := os.Lstat(plan.StagingPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("staging already exists: %v", err)
		}
		if !strings.HasPrefix(plan.StagingPath, dest) {
			t.Fatalf("staging %q is not inside %q", plan.StagingPath, dest)
		}
	})
}

func TestPlanConflictSequence(t *testing.T) {
	f, s := newFixture(t)
	dest := t.TempDir()
	req := Request{ProjectID: f.ProjectID, DestDir: dest, Format: FormatFolder}

	first, err := s.Plan(context.Background(), req)
	if err != nil {
		t.Fatalf("plan 1: %v", err)
	}
	if got, want := filepath.Base(first.OutputPath), "Fixture"; got != want {
		t.Fatalf("base = %q, want %q", got, want)
	}
	if err := os.MkdirAll(first.OutputPath, 0o755); err != nil {
		t.Fatalf("occupy: %v", err)
	}

	second, err := s.Plan(context.Background(), req)
	if err != nil {
		t.Fatalf("plan 2: %v", err)
	}
	if got, want := filepath.Base(second.OutputPath), "Fixture-2"; got != want {
		t.Fatalf("base = %q, want %q", got, want)
	}
	if err := os.MkdirAll(second.OutputPath, 0o755); err != nil {
		t.Fatalf("occupy: %v", err)
	}

	third, err := s.Plan(context.Background(), req)
	if err != nil {
		t.Fatalf("plan 3: %v", err)
	}
	if got, want := filepath.Base(third.OutputPath), "Fixture-3"; got != want {
		t.Fatalf("base = %q, want %q", got, want)
	}
}

func TestPlanFreeSpaceInjected(t *testing.T) {
	f, s := newFixture(t, func(o *Options) {
		o.diskFree = func(string) (int64, int64, bool) { return 0, 1 << 40, true }
	})
	if _, err := s.Plan(context.Background(), Request{
		ProjectID: f.ProjectID, DestDir: t.TempDir(), Format: FormatZip,
	}); !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("got %v, want ErrInsufficientSpace", err)
	}
}

func TestPlanMissingPaperPDF(t *testing.T) {
	f, s := newFixture(t)
	paperID := addPaper(t, f, "MISSING1", []byte("%PDF-missing"))
	linkPaper(t, f, f.Project, paperID)
	dir := filepath.Join(f.Root, filepath.FromSlash(f.Workspace.Paths().PaperDir(paperID)))
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove paper dir: %v", err)
	}

	plan, err := s.Plan(context.Background(), Request{
		ProjectID: f.ProjectID, DestDir: t.TempDir(), Format: FormatZip,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.PaperCount != 0 {
		t.Fatalf("PaperCount = %d, want 0", plan.PaperCount)
	}
	found := false
	for _, m := range plan.Missing {
		if m.Kind == "paper_pdf" && m.Ref == paperID {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing paper not recorded: %+v", plan.Missing)
	}
	for _, pf := range plan.Files {
		if pf.Kind == KindPaperPDF {
			t.Fatalf("missing paper planned as %s", pf.BundlePath)
		}
	}
}

func TestPlanRejectsEscapingPaths(t *testing.T) {
	f, s := newFixture(t)
	addNoteWithPath(t, f, f.Project, "projects/../../evil.md")
	addNoteWithPath(t, f, f.Project, "C:/windows/evil.md")

	plan, err := s.Plan(context.Background(), Request{
		ProjectID: f.ProjectID, DestDir: t.TempDir(), Format: FormatZip,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var rejected int
	for _, w := range plan.Warnings {
		if w.Code == "path_rejected" {
			rejected++
		}
	}
	if rejected != 2 {
		t.Fatalf("path_rejected warnings = %d, want 2 (%+v)", rejected, plan.Warnings)
	}
	for _, pf := range plan.Files {
		if strings.Contains(pf.BundlePath, "..") {
			t.Fatalf("escaping path planned: %s", pf.BundlePath)
		}
	}
}

func TestPlanCountsAndSymlinkSkip(t *testing.T) {
	f, s := newFixture(t)
	live := addNote(t, f, f.Project, false, true)
	deleted := addNote(t, f, f.Project, true, true)
	addNote(t, f, f.Project, false, false) // metadata only: missing md
	addAttachment(t, f, f.ProjectID, live, "image.png", []byte("png"))

	// A symlink inside the attachment tree must be skipped on platforms that
	// allow creating one without privileges.
	linkPath := filepath.Join(f.Root, filepath.FromSlash(
		f.Workspace.Paths().AttachmentsDir(f.ProjectID, live)+"/link.bin"))
	symlinked := os.Symlink(filepath.Join(f.Root, "global.db"), linkPath) == nil

	plan, err := s.Plan(context.Background(), Request{
		ProjectID: f.ProjectID, DestDir: t.TempDir(), Format: FormatZip,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.NoteCount != 2 {
		t.Fatalf("NoteCount = %d, want 2", plan.NoteCount)
	}
	if plan.AttachmentCount != 1 {
		t.Fatalf("AttachmentCount = %d, want 1", plan.AttachmentCount)
	}
	var missingDirs int
	for _, w := range plan.Warnings {
		if w.Code == "attachment_dir_missing" {
			missingDirs++
		}
	}
	if missingDirs != 2 {
		t.Fatalf("attachment_dir_missing = %d, want 2 (%+v)", missingDirs, plan.Warnings)
	}

	var missingNotes int
	for _, m := range plan.Missing {
		if m.Kind == "note_md" {
			missingNotes++
		}
	}
	if missingNotes != 1 {
		t.Fatalf("missing note_md = %d, want 1", missingNotes)
	}
	paths := map[string]bool{}
	for _, pf := range plan.Files {
		paths[pf.BundlePath] = true
	}
	if !paths["workspace/"+f.Workspace.Paths().NoteFile(f.ProjectID, deleted)] {
		t.Fatal("soft-deleted note markdown not planned")
	}
	if symlinked {
		var skipped bool
		for _, w := range plan.Warnings {
			if w.Code == "symlink_skipped" {
				skipped = true
			}
		}
		if !skipped {
			t.Fatalf("symlink not reported: %+v", plan.Warnings)
		}
	}
}

// TestPlanRejectsPaperPathEscape covers §8.2 path safety for the literature
// side: a recorded path outside the library is rejected and the copy is
// reported missing instead of being bundled.
func TestPlanRejectsPaperPathEscape(t *testing.T) {
	f, s := newFixture(t)
	paperID := addPaper(t, f, "ESCAPE01", []byte("%PDF-escape"))
	linkPaper(t, f, f.Project, paperID)
	if _, err := f.Global().DB().Exec(`UPDATE papers SET file_path = '../outside.pdf' WHERE id = ?`, paperID); err != nil {
		t.Fatalf("set paper path: %v", err)
	}

	plan, err := s.Plan(context.Background(), Request{
		ProjectID: f.ProjectID, DestDir: t.TempDir(), Format: FormatZip,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var rejected bool
	for _, w := range plan.Warnings {
		if w.Code == "path_rejected" && strings.Contains(w.Path, "outside.pdf") {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("paper path not rejected: %+v", plan.Warnings)
	}
	if plan.PaperCount != 0 {
		t.Fatalf("PaperCount = %d, want 0", plan.PaperCount)
	}
	for _, pf := range plan.Files {
		if strings.Contains(pf.BundlePath, "outside.pdf") {
			t.Fatalf("escaping paper planned: %s", pf.BundlePath)
		}
	}
}
