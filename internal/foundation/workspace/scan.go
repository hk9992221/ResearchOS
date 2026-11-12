package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"researchos/internal/foundation/storage"
)

// WorkspaceScan is the structured result of Scan. It reports, never repairs
// (SPEC-01 §5.4).
type WorkspaceScan struct {
	// Marker is the state of .researchos.
	Marker MarkerState
	// RegisteredWithoutDir lists registered projects whose directory is gone.
	RegisteredWithoutDir []string
	// RegisteredWithoutDB lists registered projects whose project.db is gone.
	RegisteredWithoutDB []string
	// UnregisteredDirs lists projects/<id>/ directories with no registration.
	UnregisteredDirs []string
	// SchemaIssues lists per-database schema problems ("<projectID>: <reason>";
	// the workspace global database uses the "global" prefix).
	SchemaIssues []string
}

// Scan compares the global project registry with the directory tree and checks
// the schema state of every project database.
func (w *Workspace) Scan(ctx context.Context) (WorkspaceScan, error) {
	var scan WorkspaceScan

	w.mu.Lock()
	root := w.root
	global := w.global
	opts := w.opts
	w.mu.Unlock()

	_, state, err := readMarker(root)
	if err != nil {
		return scan, err
	}
	scan.Marker = state
	if global == nil {
		return scan, errors.New("workspace: scan: workspace has no open global database")
	}

	registered := make(map[string]bool)
	rows, err := global.DB().QueryContext(ctx, `SELECT id FROM projects`)
	if err != nil {
		return scan, fmt.Errorf("workspace: scan: read project registry: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return scan, fmt.Errorf("workspace: scan: read project registry: %w", err)
		}
		registered[id] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return scan, fmt.Errorf("workspace: scan: read project registry: %w", err)
	}
	rows.Close()

	projectsDir := filepath.Join(root, "projects")
	dirSet := make(map[string]bool)
	entries, err := os.ReadDir(projectsDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return scan, fmt.Errorf("workspace: scan: read projects directory: %w", err)
	default:
		for _, e := range entries {
			if e.IsDir() {
				dirSet[e.Name()] = true
			}
		}
	}

	for id := range registered {
		if !dirSet[id] {
			scan.RegisteredWithoutDir = append(scan.RegisteredWithoutDir, id)
			continue
		}
		dbPath := filepath.Join(projectsDir, id, "project.db")
		if !fileExists(dbPath) {
			scan.RegisteredWithoutDB = append(scan.RegisteredWithoutDB, id)
			continue
		}
		ins, err := storage.Inspect(ctx, dbPath, storage.KindProject, opts.storageOptions())
		if err != nil {
			scan.SchemaIssues = append(scan.SchemaIssues, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		for _, issue := range ins.Issues {
			scan.SchemaIssues = append(scan.SchemaIssues, fmt.Sprintf("%s: %s", id, issue))
		}
	}
	for id := range dirSet {
		if !registered[id] {
			scan.UnregisteredDirs = append(scan.UnregisteredDirs, id)
		}
	}

	globalInsp, err := storage.Inspect(ctx, filepath.Join(root, "global.db"), storage.KindGlobal, opts.storageOptions())
	if err != nil {
		scan.SchemaIssues = append(scan.SchemaIssues, fmt.Sprintf("global: %v", err))
	} else {
		for _, issue := range globalInsp.Issues {
			scan.SchemaIssues = append(scan.SchemaIssues, "global: "+issue)
		}
	}

	sort.Strings(scan.RegisteredWithoutDir)
	sort.Strings(scan.RegisteredWithoutDB)
	sort.Strings(scan.UnregisteredDirs)
	sort.Strings(scan.SchemaIssues)
	return scan, nil
}
