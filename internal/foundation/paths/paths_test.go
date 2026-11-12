package paths

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"researchos/internal/foundation/errs"
)

func newRoot(t *testing.T) (string, Paths) {
	t.Helper()
	root := t.TempDir()
	p, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return root, p
}

func TestNewRejectsInvalidRoots(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(""); err == nil {
		t.Error("New(\"\") succeeded")
	}
	if _, err := New(filepath.Join(dir, "missing")); err == nil {
		t.Error("New(missing) succeeded")
	}
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(file); err == nil {
		t.Error("New(file) succeeded")
	}
}

func TestRelativeHelpers(t *testing.T) {
	_, p := newRoot(t)
	const pid = "0192f0c8-1234-7000-8000-000000000001"
	const nid = "0192f0c8-1234-7000-8000-000000000002"
	cases := map[string]string{
		p.NoteFile(pid, nid):       "projects/" + pid + "/notes/" + nid + ".md",
		p.AttachmentsDir(pid, nid): "projects/" + pid + "/attachments/" + nid,
		p.ProjectDir(pid):          "projects/" + pid,
		p.ProjectDB(pid):           "projects/" + pid + "/project.db",
		p.PaperDir("p1"):           "library/papers/p1",
		p.PaperPDF("p1"):           "library/papers/p1/source.pdf",
		p.ExportsDir():             "exports",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("relative path = %q, want %q", got, want)
		}
		if strings.Contains(got, "\\") {
			t.Errorf("relative path %q contains a backslash", got)
		}
	}
}

func TestValidateInternal(t *testing.T) {
	_, p := newRoot(t)
	for _, rel := range []string{
		"projects/p/notes/n.md",
		"library/papers/p/source.pdf",
		"exports",
	} {
		if err := p.ValidateInternal(rel); err != nil {
			t.Errorf("ValidateInternal(%q) = %v, want nil", rel, err)
		}
	}
	for _, rel := range []string{
		"",
		"../x",
		"a/../b",
		"a/./b",
		"/abs",
		"\\abs",
		"a//b",
		"a/",
		"C:/x",
		"c:x",
		"\\\\server\\share",
		"a\\b",
	} {
		err := p.ValidateInternal(rel)
		if !errors.Is(err, errs.ErrPathEscape) {
			t.Errorf("ValidateInternal(%q) = %v, want ErrPathEscape", rel, err)
		}
	}
}

func TestAbs(t *testing.T) {
	root, p := newRoot(t)
	abs, err := p.Abs("projects/p/notes/n.md")
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	if !filepath.IsAbs(abs) || !strings.HasPrefix(abs, root) {
		t.Fatalf("Abs returned %q outside root %q", abs, root)
	}
	for _, rel := range []string{"../x", "C:/x", "/x", "a/../b"} {
		if _, err := p.Abs(rel); !errors.Is(err, errs.ErrPathEscape) {
			t.Errorf("Abs(%q) = %v, want ErrPathEscape", rel, err)
		}
	}
}

func TestRel(t *testing.T) {
	root, p := newRoot(t)
	if err := os.MkdirAll(filepath.Join(root, "projects", "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	rel, err := p.Rel(filepath.Join(root, "projects", "p", "project.db"))
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	if want := "projects/p/project.db"; rel != want {
		t.Fatalf("Rel = %q, want %q", rel, want)
	}
	if _, err := p.Rel(filepath.Join(root, "..", "elsewhere")); !errors.Is(err, errs.ErrPathEscape) {
		t.Errorf("Rel outside = %v, want ErrPathEscape", err)
	}
	if _, err := p.Rel(root); !errors.Is(err, errs.ErrPathEscape) {
		t.Errorf("Rel(root) = %v, want ErrPathEscape", err)
	}
	if _, err := p.Rel("projects/p"); !errors.Is(err, errs.ErrPathEscape) {
		t.Errorf("Rel(relative) = %v, want ErrPathEscape", err)
	}
}

func TestAC19SymlinkEscape(t *testing.T) {
	root, p := newRoot(t)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "linkdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, filepath.Join(root, "linkdir", "escape"))
	if _, err := p.Abs("linkdir/escape/pwn.txt"); !errors.Is(err, errs.ErrPathEscape) {
		t.Fatalf("Abs through escaping symlink = %v, want ErrPathEscape", err)
	}
	// A symlinked leaf that points outside is caught as well.
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, filepath.Join(outside, "secret.txt"), filepath.Join(root, "evil.md"))
	if _, err := p.Abs("evil.md"); !errors.Is(err, errs.ErrPathEscape) {
		t.Fatalf("Abs(evil.md) = %v, want ErrPathEscape", err)
	}
	if p.IsInternal(filepath.Join(outside, "secret.txt")) {
		t.Fatal("IsInternal reported an external file as internal")
	}
}

func TestIsInternal(t *testing.T) {
	root, p := newRoot(t)
	if !p.IsInternal(root) {
		t.Error("IsInternal(root) = false")
	}
	if !p.IsInternal(filepath.Join(root, "global.db")) {
		t.Error("IsInternal(global.db) = false")
	}
	if p.IsInternal(filepath.Join(root, "..", "outside")) {
		t.Error("IsInternal(outside) = true")
	}
	if p.IsInternal("projects/x") {
		t.Error("IsInternal(relative) = true")
	}
}

func TestAbsResolvesSymlinkInsideRoot(t *testing.T) {
	root, p := newRoot(t)
	if err := os.MkdirAll(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, filepath.Join(root, "real"), filepath.Join(root, "alias"))
	abs, err := p.Abs("alias/file.txt")
	if err != nil {
		t.Fatalf("Abs through internal symlink: %v", err)
	}
	if !strings.HasPrefix(abs, filepath.Join(root, "real")) {
		t.Fatalf("Abs = %q, want it under %q", abs, filepath.Join(root, "real"))
	}
}

// symlinkOrSkip creates a symlink or skips the test. On Windows an unprivileged
// os.Symlink call blocks for roughly 30 seconds before reporting the missing
// privilege, so the tests are opt-in there via RESEARCHOS_TEST_SYMLINKS=1.
func symlinkOrSkip(t *testing.T, oldname, newname string) {
	t.Helper()
	if runtime.GOOS == "windows" && os.Getenv("RESEARCHOS_TEST_SYMLINKS") != "1" {
		t.Skip("symlink tests are opt-in on Windows (set RESEARCHOS_TEST_SYMLINKS=1); unprivileged symlink creation blocks for ~30s")
	}
	if err := os.Symlink(oldname, newname); err != nil {
		t.Skipf("cannot create symlink on this host: %v", err)
	}
}
