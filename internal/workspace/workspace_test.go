package workspace_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/projects"
	"researchos/internal/foundation/schema"
	workspace "researchos/internal/workspace"
)

func newFixture(t *testing.T) *foundationtest.Fixture {
	t.Helper()
	f, err := foundationtest.New(t.TempDir(), foundationtest.Options{})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), size), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// walkBytes is the tests' own recursive sum of regular files, mirroring the
// SPEC-03 §5.10 measurement rules without reusing package internals.
func walkBytes(t *testing.T, dir string) int64 {
	t.Helper()
	var total int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			info, ierr := d.Info()
			if ierr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return total
}

// dbFamilyBytes sums a database file plus its existing -wal/-shm siblings.
func dbFamilyBytes(t *testing.T, base string) int64 {
	t.Helper()
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(base + suffix)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			t.Fatalf("stat %s%s: %v", base, suffix, err)
		default:
			total += info.Size()
		}
	}
	return total
}

func entryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

func archiveProject(t *testing.T, ctx context.Context, f *foundationtest.Fixture, id string) {
	t.Helper()
	store := projects.New(f.Global().DB(), f.Clock)
	if err := store.SetStatus(ctx, nil, id, schema.ProjectArchived, schema.ActorUser); err != nil {
		t.Fatalf("archive %s: %v", id, err)
	}
}

// registerOnlyProject inserts the global.db.projects row without creating any
// data directory, standing in for half-registered projects.
func registerOnlyProject(t *testing.T, ctx context.Context, f *foundationtest.Fixture, name string) string {
	t.Helper()
	store := projects.New(f.Global().DB(), f.Clock)
	id, err := store.Create(ctx, nil, name, "", "")
	if err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	return id
}

func TestInfoCountsAndSizes(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	root := f.Workspace.Root()

	if _, _, err := f.NewProject(ctx, "Alpha", "s", "g"); err != nil {
		t.Fatal(err)
	}
	betaID, _, err := f.NewProject(ctx, "Beta", "s", "g")
	if err != nil {
		t.Fatal(err)
	}
	archiveProject(t, ctx, f, betaID)

	writeFile(t, filepath.Join(root, "library", "papers", "p1", "source.pdf"), 1234)
	writeFile(t, filepath.Join(root, "library", "loose.bin"), 7)
	writeFile(t, filepath.Join(root, "exports", "dump.txt"), 77)

	svc := workspace.New(f.Workspace)
	info, err := svc.Info(ctx)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}

	if info.Root != root {
		t.Errorf("Root = %q, want %q", info.Root, root)
	}
	if want := walkBytes(t, filepath.Join(root, "library")); info.LibraryBytes != want {
		t.Errorf("LibraryBytes = %d, want walk %d", info.LibraryBytes, want)
	}
	if want := int64(1234 + 7); info.LibraryBytes != want {
		t.Errorf("LibraryBytes = %d, want literal %d", info.LibraryBytes, want)
	}
	if want := walkBytes(t, filepath.Join(root, "exports")); info.ExportsBytes != want {
		t.Errorf("ExportsBytes = %d, want walk %d", info.ExportsBytes, want)
	}
	if want := dbFamilyBytes(t, filepath.Join(root, "global.db")); info.DBBytes != want {
		t.Errorf("DBBytes = %d, want db family %d", info.DBBytes, want)
	}
	if info.ProjectCount != 2 {
		t.Errorf("ProjectCount = %d, want 2", info.ProjectCount)
	}
	if info.ArchivedCount != 1 {
		t.Errorf("ArchivedCount = %d, want 1", info.ArchivedCount)
	}
	if info.FreeBytes < 0 || info.TotalBytes < 0 {
		t.Errorf("disk space negative: free=%d total=%d", info.FreeBytes, info.TotalBytes)
	}
}

func TestFootprint(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	root := f.Workspace.Root()

	alphaID, alphaHandle, err := f.NewProject(ctx, "Alpha", "s", "g")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.SeedNotes(ctx, alphaHandle, 3, 1); err != nil { // 2 live rows
		t.Fatalf("SeedNotes: %v", err)
	}
	betaID, _, err := f.NewProject(ctx, "Beta", "s", "g")
	if err != nil {
		t.Fatal(err)
	}
	archiveProject(t, ctx, f, betaID)

	// Registered but without project.db: dir sizes still count.
	gammaID := registerOnlyProject(t, ctx, f, "Gamma no db")
	writeFile(t, filepath.Join(root, "projects", gammaID, "data.bin"), 64)
	// Registered but without any directory at all.
	registerOnlyProject(t, ctx, f, "Delta no dir")

	writeFile(t, filepath.Join(root, "projects", alphaID, "notes", "a.md"), 512)
	writeFile(t, filepath.Join(root, "projects", alphaID, "attachments", "y.png"), 100)
	writeFile(t, filepath.Join(root, "projects", alphaID, "attachments", "img", "x.png"), 256)
	writeFile(t, filepath.Join(root, "library", "papers", "p1", "source.pdf"), 400)
	writeFile(t, filepath.Join(root, "exports", "out.md"), 50)

	svc := workspace.New(f.Workspace)
	fp, err := svc.Footprint(ctx)
	if err != nil {
		t.Fatalf("Footprint: %v", err)
	}

	if len(fp.Projects) != 4 {
		t.Fatalf("len(Projects) = %d, want 4", len(fp.Projects))
	}
	wantNames := []string{"Alpha", "Beta", "Delta no dir", "Gamma no db"}
	for i, want := range wantNames {
		if fp.Projects[i].Name != want {
			t.Errorf("Projects[%d].Name = %q, want %q", i, fp.Projects[i].Name, want)
		}
	}

	byID := map[string]workspace.ProjectFootprint{}
	for _, p := range fp.Projects {
		byID[p.ID] = p
	}
	alpha := byID[alphaID]
	if alpha.Archived {
		t.Error("Alpha should not be Archived")
	}
	if alpha.NoteCount != 2 {
		t.Errorf("Alpha NoteCount = %d, want 2 (soft-deleted rows excluded)", alpha.NoteCount)
	}
	if want := int64(100 + 256); alpha.AttachmentBytes != want {
		t.Errorf("Alpha AttachmentBytes = %d, want %d", alpha.AttachmentBytes, want)
	}
	if want := walkBytes(t, filepath.Join(root, "projects", alphaID)); alpha.Bytes != want {
		t.Errorf("Alpha Bytes = %d, want walk %d", alpha.Bytes, want)
	}

	beta := byID[betaID]
	if !beta.Archived {
		t.Error("Beta should be Archived")
	}
	if beta.NoteCount != 0 || beta.AttachmentBytes != 0 {
		t.Errorf("Beta footprint = %+v, want zero notes/attachments", beta)
	}

	gamma := byID[gammaID]
	if gamma.Bytes != 64 || gamma.AttachmentBytes != 0 || gamma.NoteCount != 0 {
		t.Errorf("Gamma footprint = %+v, want Bytes=64 zero notes (missing db tolerated)", gamma)
	}
	for _, p := range fp.Projects {
		if p.Name == "Delta no dir" {
			if p.Bytes != 0 || p.NoteCount != 0 || p.Archived {
				t.Errorf("Delta footprint = %+v, want all zero", p)
			}
		}
	}

	if want := walkBytes(t, filepath.Join(root, "library")); fp.LibraryBytes != want {
		t.Errorf("LibraryBytes = %d, want %d", fp.LibraryBytes, want)
	}
	if want := walkBytes(t, filepath.Join(root, "exports")); fp.ExportsBytes != want {
		t.Errorf("ExportsBytes = %d, want %d", fp.ExportsBytes, want)
	}
	wantTotal := fp.LibraryBytes + fp.ExportsBytes + dbFamilyBytes(t, filepath.Join(root, "global.db"))
	for _, p := range fp.Projects {
		wantTotal += p.Bytes
	}
	if fp.TotalBytes != wantTotal {
		t.Errorf("TotalBytes = %d, want %d (projects+library+exports+global.db family)", fp.TotalBytes, wantTotal)
	}
}

func TestProbe(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	root := f.Workspace.Root()
	svc := workspace.New(f.Workspace)
	other := t.TempDir()

	t.Run("nonexistent-with-writable-parent", func(t *testing.T) {
		target := filepath.Join(other, "clean", "..", "newroot")
		p, err := svc.Probe(ctx, target)
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if want := filepath.Join(other, "newroot"); p.Root != want {
			t.Errorf("Root = %q, want cleaned %q", p.Root, want)
		}
		if p.Exists || p.Empty || p.Nested || p.SameAsCurrent || p.HasForeignData {
			t.Errorf("flags = %+v, want only Writable", p)
		}
		if !p.Writable {
			t.Errorf("Writable = false, want true (parent is writable): problems=%v", p.Problems)
		}
		if len(p.Problems) != 0 {
			t.Errorf("Problems = %v, want none", p.Problems)
		}
		if p.FreeBytes < 0 {
			t.Errorf("FreeBytes = %d", p.FreeBytes)
		}
		if _, err := os.Stat(filepath.Join(other, "newroot")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("probe created the target directory: %v", err)
		}
	})

	t.Run("empty-existing-dir", func(t *testing.T) {
		dir := filepath.Join(other, "empty")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		before := entryNames(t, dir)
		p, err := svc.Probe(ctx, dir)
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if !p.Exists || !p.Empty || !p.Writable || p.HasForeignData {
			t.Errorf("flags = %+v, want Exists/Empty/Writable, no foreign data", p)
		}
		if len(p.Problems) != 0 {
			t.Errorf("Problems = %v, want none", p.Problems)
		}
		if after := entryNames(t, dir); !slices.Equal(before, after) {
			t.Errorf("probe left files behind: before=%v after=%v", before, after)
		}
	})

	t.Run("non-empty-without-marker", func(t *testing.T) {
		dir := filepath.Join(other, "foreign")
		writeFile(t, filepath.Join(dir, "data.txt"), 10)
		before := entryNames(t, dir)
		p, err := svc.Probe(ctx, dir)
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if !p.Exists || p.Empty || !p.HasForeignData {
			t.Errorf("flags = %+v, want Exists, not Empty, HasForeignData", p)
		}
		if p.Writable {
			t.Error("Writable = true, want false for foreign data")
		}
		if len(p.Problems) == 0 {
			t.Error("Problems empty, want refusal reason")
		}
		if after := entryNames(t, dir); !slices.Equal(before, after) {
			t.Errorf("probe changed directory contents: before=%v after=%v", before, after)
		}
	})

	t.Run("current-root", func(t *testing.T) {
		before := entryNames(t, root)
		p, err := svc.Probe(ctx, root)
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if !p.SameAsCurrent || !p.Exists || p.Empty || p.HasForeignData {
			t.Errorf("flags = %+v, want SameAsCurrent on a healthy root", p)
		}
		if len(p.Problems) == 0 || !strings.Contains(p.Problems[0], "current workspace root") {
			t.Errorf("Problems = %v, want same-as-current reason", p.Problems)
		}
		if after := entryNames(t, root); !slices.Equal(before, after) {
			t.Errorf("probe left files behind in root: before=%v after=%v", before, after)
		}
	})

	t.Run("nested-descendant", func(t *testing.T) {
		for _, target := range []string{
			filepath.Join(root, "projects"),           // existing
			filepath.Join(root, "spec03-migrate-tmp"), // does not exist yet
		} {
			p, err := svc.Probe(ctx, target)
			if err != nil {
				t.Fatalf("Probe(%s): %v", target, err)
			}
			if !p.Nested || p.SameAsCurrent {
				t.Errorf("Probe(%s): Nested=%v SameAsCurrent=%v, want nested", target, p.Nested, p.SameAsCurrent)
			}
			if len(p.Problems) == 0 || !strings.Contains(strings.Join(p.Problems, "; "), "current workspace root") {
				t.Errorf("Probe(%s): Problems = %v", target, p.Problems)
			}
		}
	})

	t.Run("nested-ancestor", func(t *testing.T) {
		p, err := svc.Probe(ctx, filepath.Dir(root))
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if !p.Nested || p.SameAsCurrent {
			t.Errorf("Nested = %v, want ancestor of the root flagged", p.Nested)
		}
		if len(p.Problems) == 0 {
			t.Error("Problems empty, want nesting reason")
		}
	})

	t.Run("unrelated-sibling-not-nested", func(t *testing.T) {
		// other shares an ancestor with root but must not count as nested.
		p, err := svc.Probe(ctx, filepath.Join(other, "sibling-check"))
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if p.Nested || p.SameAsCurrent {
			t.Errorf("Nested=%v SameAsCurrent=%v, want false for unrelated path", p.Nested, p.SameAsCurrent)
		}
	})
}

func TestProbeOnOtherVolumeOrNonexistentParent(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	root := f.Workspace.Root()
	svc := workspace.New(f.Workspace)

	t.Run("nonexistent-parent", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "missing-parent", "deeper", "target")
		p, err := svc.Probe(ctx, target)
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if p.Exists || p.Writable || p.Empty {
			t.Errorf("flags = %+v, want Exists/Writable/Empty all false", p)
		}
		if len(p.Problems) == 0 {
			t.Error("Problems empty, want parent-does-not-exist reason")
		}
		if p.FreeBytes < 0 {
			t.Errorf("FreeBytes = %d, want >= 0", p.FreeBytes)
		}
	})

	t.Run("other-volume", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			probeAnotherVolume(t, svc, root)
		}
		// On single-volume systems the nonexistent-parent case above covers
		// the "no error for awkward roots" contract; nothing more to assert.
	})
}

func probeAnotherVolume(t *testing.T, svc *workspace.Service, root string) {
	t.Helper()
	current := strings.ToUpper(filepath.VolumeName(root)) // e.g. "F:"
	for letter := 'A'; letter <= 'Z'; letter++ {
		name := string(letter) + ":"
		if strings.EqualFold(name, current) {
			continue
		}
		vol := name + string(filepath.Separator)
		info, err := os.Stat(vol)
		if err != nil || !info.IsDir() {
			continue
		}
		p, err := svc.Probe(context.Background(), filepath.Join(vol, "researchos-spec03-probe-target"))
		if err != nil {
			t.Fatalf("Probe on volume %s: %v", name, err)
		}
		if p.SameAsCurrent || p.Nested {
			t.Errorf("volume %s flagged SameAsCurrent=%v Nested=%v, want false", name, p.SameAsCurrent, p.Nested)
		}
		if p.FreeBytes < 0 {
			t.Errorf("FreeBytes = %d on %s, want >= 0", p.FreeBytes, name)
		}
		return
	}
	t.Skip("no second volume available on this machine")
}
