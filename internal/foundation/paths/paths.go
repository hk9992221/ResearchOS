// Package paths owns every workspace path decision (SPEC-01 §5.3). Internal
// paths are recorded relative to the workspace root with forward slashes;
// external paths are absolute and are never written by the foundation layer.
package paths

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"researchos/internal/foundation/errs"
)

// Directory and file modes used by the foundation layer.
const (
	DirMode  fs.FileMode = 0o755
	FileMode fs.FileMode = 0o644
)

// Paths resolves workspace-relative and absolute paths for one workspace root.
type Paths struct {
	root string // absolute, symlink-resolved, OS separators
}

// New builds a Paths rooted at root. The root must exist and be a directory;
// the stored root is absolute with symlinks resolved.
func New(root string) (Paths, error) {
	if strings.TrimSpace(root) == "" {
		return Paths{}, fmt.Errorf("paths: empty workspace root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return Paths{}, fmt.Errorf("paths: absolutize root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Paths{}, fmt.Errorf("paths: stat root: %w", err)
	}
	if !info.IsDir() {
		return Paths{}, fmt.Errorf("paths: root %q is not a directory", abs)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return Paths{}, fmt.Errorf("paths: resolve root: %w", err)
	}
	return Paths{root: filepath.Clean(resolved)}, nil
}

// Root returns the absolute, resolved workspace root.
func (p Paths) Root() string { return p.root }

// NoteFile returns projects/<projectID>/notes/<noteID>.md.
func (p Paths) NoteFile(projectID, noteID string) string {
	return "projects/" + projectID + "/notes/" + noteID + ".md"
}

// AttachmentsDir returns projects/<projectID>/attachments/<noteID>.
func (p Paths) AttachmentsDir(projectID, noteID string) string {
	return "projects/" + projectID + "/attachments/" + noteID
}

// ProjectDir returns projects/<projectID>.
func (p Paths) ProjectDir(projectID string) string { return "projects/" + projectID }

// NotesDir returns projects/<projectID>/notes.
func (p Paths) NotesDir(projectID string) string { return p.ProjectDir(projectID) + "/notes" }

// ProjectDB returns projects/<projectID>/project.db.
func (p Paths) ProjectDB(projectID string) string { return p.ProjectDir(projectID) + "/project.db" }

// PaperDir returns library/papers/<paperID>.
func (p Paths) PaperDir(paperID string) string { return "library/papers/" + paperID }

// PaperPDF returns library/papers/<paperID>/source.pdf.
func (p Paths) PaperPDF(paperID string) string { return p.PaperDir(paperID) + "/source.pdf" }

// ExportsDir returns the default export directory.
func (p Paths) ExportsDir() string { return "exports" }

// Abs converts an internal relative path to an absolute OS path. The result is
// resolved through symlinks and re-checked against the root, so a link that
// points outside the workspace yields ErrPathEscape.
func (p Paths) Abs(rel string) (string, error) {
	if err := p.ValidateInternal(rel); err != nil {
		return "", err
	}
	joined := filepath.Join(p.root, filepath.FromSlash(rel))
	resolved, err := resolveExisting(joined)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", errs.ErrPathEscape, rel, err)
	}
	if !within(p.root, resolved) {
		return "", fmt.Errorf("%w: %s resolves outside the workspace root", errs.ErrPathEscape, rel)
	}
	return resolved, nil
}

// Rel converts an absolute path inside the workspace to a canonical internal
// path (forward slashes, no leading separator). Paths outside the root yield
// ErrPathEscape.
func (p Paths) Rel(abs string) (string, error) {
	if !filepath.IsAbs(abs) {
		return "", fmt.Errorf("%w: %q is not absolute", errs.ErrPathEscape, abs)
	}
	resolved, err := resolveExisting(filepath.Clean(abs))
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", errs.ErrPathEscape, abs, err)
	}
	if !within(p.root, resolved) {
		return "", fmt.Errorf("%w: %s is outside the workspace root", errs.ErrPathEscape, abs)
	}
	rel, err := filepath.Rel(p.root, resolved)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", errs.ErrPathEscape, abs, err)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return "", fmt.Errorf("%w: path is the workspace root", errs.ErrPathEscape)
	}
	return rel, nil
}

// ValidateInternal rejects anything that is not a canonical workspace-relative
// path: empty, absolute, drive-qualified, UNC, leading separator, backslashes,
// empty segments, or "." / ".." segments.
func (p Paths) ValidateInternal(rel string) error {
	if rel == "" {
		return fmt.Errorf("%w: empty relative path", errs.ErrPathEscape)
	}
	if strings.ContainsRune(rel, '\\') {
		return fmt.Errorf("%w: %q must use forward slashes", errs.ErrPathEscape, rel)
	}
	if strings.HasPrefix(rel, "/") {
		return fmt.Errorf("%w: %q must be relative", errs.ErrPathEscape, rel)
	}
	if filepath.VolumeName(rel) != "" || driveLetter(rel) {
		return fmt.Errorf("%w: %q must not carry a drive", errs.ErrPathEscape, rel)
	}
	if filepath.IsAbs(filepath.FromSlash(rel)) {
		return fmt.Errorf("%w: %q must be relative", errs.ErrPathEscape, rel)
	}
	for _, seg := range strings.Split(rel, "/") {
		switch seg {
		case "":
			return fmt.Errorf("%w: %q has an empty segment", errs.ErrPathEscape, rel)
		case ".", "..":
			return fmt.Errorf("%w: %q has a %q segment", errs.ErrPathEscape, rel, seg)
		}
	}
	return nil
}

// IsInternal reports whether abs lies inside the workspace root.
func (p Paths) IsInternal(abs string) bool {
	if !filepath.IsAbs(abs) {
		return false
	}
	resolved, err := resolveExisting(filepath.Clean(abs))
	if err != nil {
		return false
	}
	return within(p.root, resolved)
}

// resolveExisting resolves symlinks for the deepest existing ancestor of path
// and re-appends the missing tail. Callers therefore get a stable answer even
// for files that have not been created yet.
func resolveExisting(path string) (string, error) {
	rest := ""
	cur := filepath.Clean(path)
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if rest == "" {
				return resolved, nil
			}
			return filepath.Join(resolved, rest), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// within reports whether target is root itself or sits below it.
func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return !filepath.IsAbs(rel)
}

// driveLetter reports a Windows-style "C:" prefix even on non-Windows hosts.
func driveLetter(rel string) bool {
	if len(rel) < 2 {
		return false
	}
	c := rel[0]
	if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
		return false
	}
	return rel[1] == ':'
}
