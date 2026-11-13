package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/storage"
)

// Reopen opens the global database again at the workspace's current root after
// Close. It is additive (SPEC-03 §5.10 migration, T3-08): Close leaves the
// workspace unusable, and Repoint only rewrites the root and config, so the
// migration flow closes every handle while copying and reopens afterwards.
//
// Project handles stay closed; Project reopens them lazily on demand. The
// workspace options captured at Init/Open time (Clock, Config, storage limits,
// extra migrations) are reused, so a reopened workspace behaves exactly like
// the one that was closed. Reopen on an already open workspace is an error: it
// never replaces a live handle.
func (w *Workspace) Reopen(ctx context.Context) error {
	if w == nil {
		return errors.New("workspace: reopen: nil workspace")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	root, opts, open := w.root, w.opts, w.global != nil
	w.mu.Unlock()
	if open {
		return errors.New("workspace: reopen: workspace is already open")
	}

	// Validate the same marker/database invariants as Open, so a corrupted or
	// half-moved root cannot silently create a fresh empty global.db.
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w: %s is not a workspace directory", errs.ErrNotWorkspace, root)
	}
	_, state, err := readMarker(root)
	if err != nil {
		return err
	}
	switch state {
	case MarkerMissing:
		return fmt.Errorf("%w: %s has no marker", errs.ErrNotWorkspace, root)
	case MarkerInvalid:
		return fmt.Errorf("%w: %s has an invalid marker", errs.ErrWorkspaceCorrupt, root)
	}
	if !fileExists(filepath.Join(root, "global.db")) {
		return fmt.Errorf("%w: marker exists but global.db is missing in %s", errs.ErrWorkspaceCorrupt, root)
	}

	db, err := storage.OpenDatabase(ctx, filepath.Join(root, "global.db"), storage.KindGlobal, opts.storageOptions())
	if err != nil {
		return err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.global != nil {
		_ = db.Close()
		return errors.New("workspace: reopen: workspace is already open")
	}
	if w.root != root {
		_ = db.Close()
		return fmt.Errorf("workspace: reopen: root changed to %s while reopening", w.root)
	}
	w.global = newGlobalHandle(db)
	if w.projects == nil {
		w.projects = make(map[string]*ProjectHandle)
	}
	return nil
}
