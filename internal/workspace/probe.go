package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	foundationworkspace "researchos/internal/foundation/workspace"
)

// Probe is the read-only verdict on a candidate workspace root (SPEC-03 §5.10
// Probe rules). Problems collects the reasons a root must be refused or that
// warn the user; it is empty when the target looks usable.
type Probe struct {
	Root           string
	Exists         bool
	Empty          bool
	Writable       bool
	FreeBytes      int64
	Nested         bool // ancestor or descendant of the current root
	SameAsCurrent  bool
	HasForeignData bool // non-empty and not recognizable as a ResearchOS workspace
	Problems       []string
}

// Probe inspects a candidate root without creating or modifying anything that
// survives the call: the writability check is an mkstemp-style temp file that
// is removed immediately, and only inside an already existing directory.
func (s *Service) Probe(ctx context.Context, root string) (*Probe, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("workspace: probe: absolutize %q: %w", root, err)
	}
	p := &Probe{Root: abs}

	// Relation to the current root is pure path arithmetic; the target may not
	// exist, so no symlink resolution happens here (only case folding on
	// Windows, matching the platform's case-insensitive paths).
	current := s.ws.Root()
	switch relation(normPath(current), normPath(abs)) {
	case relationSame:
		p.SameAsCurrent = true
		p.Problems = append(p.Problems, fmt.Sprintf("%s is the current workspace root", abs))
	case relationAncestor:
		p.Nested = true
		p.Problems = append(p.Problems, fmt.Sprintf("%s contains the current workspace root %s", abs, current))
	case relationDescendant:
		p.Nested = true
		p.Problems = append(p.Problems, fmt.Sprintf("%s is inside the current workspace root %s", abs, current))
	}

	// Free space is reported for the nearest existing directory at or above
	// the target; nothing is stat'ed as a side effect. A failure to answer is
	// a Problem, never an error (SPEC-03 §5.10).
	anchor := firstExistingDir(abs)
	free, _, ok := diskSpace(anchor)
	p.FreeBytes = free
	if !ok {
		p.Problems = append(p.Problems, fmt.Sprintf("cannot determine free disk space for %s", abs))
	}

	info, statErr := os.Stat(abs)
	switch {
	case statErr == nil && !info.IsDir():
		p.Exists = true
		p.Writable = false
		p.Problems = append(p.Problems, fmt.Sprintf("%s exists but is not a directory", abs))
		return p, nil

	case statErr == nil:
		p.Exists = true
		empty, err := dirEmpty(abs)
		if err != nil {
			p.Problems = append(p.Problems, fmt.Sprintf("cannot read %s: %v", abs, err))
		}
		p.Empty = empty
		hasMarker := filePresent(filepath.Join(abs, foundationworkspace.MarkerName))
		hasGlobal := filePresent(filepath.Join(abs, "global.db"))
		if !p.Empty && (!hasMarker || !hasGlobal) {
			p.HasForeignData = true
			p.Writable = false
			p.Problems = append(p.Problems, fmt.Sprintf(
				"%s already contains files but is not a ResearchOS workspace (missing %s marker or global.db); it will not be touched",
				abs, foundationworkspace.MarkerName))
			return p, nil
		}
		p.Writable = writableDir(abs)
		if !p.Writable {
			p.Problems = append(p.Problems, fmt.Sprintf("%s is not writable", abs))
		}
		return p, nil

	case !errors.Is(statErr, fs.ErrNotExist):
		p.Problems = append(p.Problems, fmt.Sprintf("cannot inspect %s: %v", abs, statErr))
		fallthrough

	default: // does not exist: the parent must be an existing writable directory
		parent := filepath.Dir(abs)
		pInfo, err := os.Stat(parent)
		switch {
		case err != nil:
			p.Writable = false
			p.Problems = append(p.Problems, fmt.Sprintf("parent directory %s does not exist", parent))
		case !pInfo.IsDir():
			p.Writable = false
			p.Problems = append(p.Problems, fmt.Sprintf("parent %s is not a directory", parent))
		default:
			p.Writable = writableDir(parent)
			if !p.Writable {
				p.Problems = append(p.Problems, fmt.Sprintf("parent directory %s is not writable", parent))
			}
		}
		return p, nil
	}
}

// normPath makes paths comparable for the same/nested checks: cleaned, and
// lower-cased on Windows where paths are case-insensitive.
func normPath(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

// pathRelation classifies how target relates to base. filepath.Rel fails
// across volumes, which correctly yields relationNone. Only pure ".."
// segments make the target an ancestor; "..\..\sibling\path" (a shared
// ancestor but unrelated directory) must not be mistaken for nesting.
type pathRelation int

const (
	relationNone pathRelation = iota
	relationSame
	relationAncestor
	relationDescendant
)

func relation(base, target string) pathRelation {
	if base == target {
		return relationSame
	}
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return relationNone
	}
	segs := strings.Split(rel, string(filepath.Separator))
	upOnly := true
	for _, seg := range segs {
		if seg != ".." {
			upOnly = false
			break
		}
	}
	switch {
	case upOnly:
		return relationAncestor
	case segs[0] == ".." || segs[0] == ".":
		return relationNone
	default:
		return relationDescendant
	}
}

// dirEmpty reports whether dir holds no meaningful entries. The two OS
// droppings that foundation Init ignores (.DS_Store, desktop.ini) do not make
// a directory non-empty.
func dirEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		switch e.Name() {
		case ".DS_Store", "desktop.ini":
			continue
		}
		return false, nil
	}
	return true, nil
}

// firstExistingDir returns the deepest existing directory at or above path,
// or "" if none is found (which only happens on broken roots).
func firstExistingDir(path string) string {
	for {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return ""
		}
		path = parent
	}
}

// writableDir probes write access with a temp file that is deleted before
// returning. It never creates directories and leaves nothing behind even if
// cleanup misbehaves (best-effort remove after a successful create).
func writableDir(dir string) bool {
	f, err := os.CreateTemp(dir, ".researchos-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}
