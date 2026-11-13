// Package workspace implements the SPEC-03 §5.10 workspace service for the
// T3-07 scope: workspace Info, disk Footprint accounting and root Probe.
// It composes the frozen foundation workspace (SPEC-01) and only reads:
// Probe never creates anything that survives the call. Migration
// (PlanMigration/Migrate/CancelMigration, T3-08) is out of scope here.
package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"researchos/internal/foundation/schema"
	foundationworkspace "researchos/internal/foundation/workspace"
)

// footprintWorkers caps the concurrent per-project measurements.
const footprintWorkers = 4

// Info is the workspace-level summary shown on the Dashboard (SPEC-03 §5.10).
type Info struct {
	Root          string
	FreeBytes     int64
	TotalBytes    int64
	DBBytes       int64
	LibraryBytes  int64
	ExportsBytes  int64
	ProjectCount  int
	ArchivedCount int
}

// Footprint is the disk-usage breakdown over projects, library and exports.
type Footprint struct {
	Projects     []ProjectFootprint
	LibraryBytes int64
	ExportsBytes int64
	TotalBytes   int64
}

// ProjectFootprint is the usage of one registered project. NoteCount counts
// live notes (deleted_at IS NULL); it is 0 when the project database cannot be
// opened, which does not fail the whole Footprint call.
type ProjectFootprint struct {
	ID              string
	Name            string
	Bytes           int64
	NoteCount       int
	AttachmentBytes int64
	Archived        bool
}

// Service answers workspace questions against an open foundation workspace.
// It holds no mutable state and is safe for concurrent use.
type Service struct {
	ws *foundationworkspace.Workspace
}

// New binds a workspace service to an open foundation workspace.
func New(ws *foundationworkspace.Workspace) *Service {
	return &Service{ws: ws}
}

// Info reports the root, disk space, database/library/export sizes and the
// project counts. Unknown project statuses count toward ProjectCount only.
func (s *Service) Info(ctx context.Context) (*Info, error) {
	global := s.ws.Global()
	if global == nil {
		return nil, fmt.Errorf("workspace: info: workspace is closed")
	}
	root := s.ws.Root()
	info := &Info{Root: root}
	info.DBBytes = dbFilesSize(filepath.Join(root, "global.db"))
	var err error
	if info.LibraryBytes, err = dirSize(ctx, filepath.Join(root, "library")); err != nil {
		return nil, fmt.Errorf("workspace: info: library size: %w", err)
	}
	if info.ExportsBytes, err = dirSize(ctx, filepath.Join(root, "exports")); err != nil {
		return nil, fmt.Errorf("workspace: info: exports size: %w", err)
	}
	rows, err := global.DB().QueryContext(ctx, `SELECT status, COUNT(*) FROM projects GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("workspace: info: project counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			status string
			count  int
		)
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("workspace: info: project counts: %w", err)
		}
		info.ProjectCount += count
		if status == string(schema.ProjectArchived) {
			info.ArchivedCount += count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workspace: info: project counts: %w", err)
	}
	info.FreeBytes, info.TotalBytes, _ = diskSpace(root)
	return info, nil
}

// Footprint measures every registered project plus library and exports. The
// per-project measurements run in a worker pool capped at footprintWorkers and
// honor context cancellation.
func (s *Service) Footprint(ctx context.Context) (*Footprint, error) {
	global := s.ws.Global()
	if global == nil {
		return nil, fmt.Errorf("workspace: footprint: workspace is closed")
	}
	root := s.ws.Root()
	projects, err := s.projectRows(ctx, global)
	if err != nil {
		return nil, err
	}

	results := make([]ProjectFootprint, len(projects))
	var wg sync.WaitGroup
	sem := make(chan struct{}, footprintWorkers)
	for i, project := range projects {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return nil, ctx.Err()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = s.measureProject(ctx, root, project)
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	fp := &Footprint{Projects: results}
	if fp.LibraryBytes, err = dirSize(ctx, filepath.Join(root, "library")); err != nil {
		return nil, fmt.Errorf("workspace: footprint: library size: %w", err)
	}
	if fp.ExportsBytes, err = dirSize(ctx, filepath.Join(root, "exports")); err != nil {
		return nil, fmt.Errorf("workspace: footprint: exports size: %w", err)
	}
	fp.TotalBytes = fp.LibraryBytes + fp.ExportsBytes + dbFilesSize(filepath.Join(root, "global.db"))
	for i := range fp.Projects {
		fp.TotalBytes += fp.Projects[i].Bytes
	}
	return fp, nil
}

// projectRows reads the project registry ordered by name then id, so the
// result is already in the Footprint display order.
func (s *Service) projectRows(ctx context.Context, global *foundationworkspace.GlobalHandle) ([]ProjectFootprint, error) {
	rows, err := global.DB().QueryContext(ctx, `SELECT id, name, status FROM projects ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("workspace: footprint: project rows: %w", err)
	}
	defer rows.Close()
	var out []ProjectFootprint
	for rows.Next() {
		var (
			id     string
			name   string
			status string
		)
		if err := rows.Scan(&id, &name, &status); err != nil {
			return nil, fmt.Errorf("workspace: footprint: project rows: %w", err)
		}
		out = append(out, ProjectFootprint{ID: id, Name: name, Archived: status == string(schema.ProjectArchived)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workspace: footprint: project rows: %w", err)
	}
	return out, nil
}

// measureProject fills in the sizes and the live note count of one project.
// A missing or unopenable project database yields a zero NoteCount without an
// error: the directory sizes still count (SPEC-03 §5.10).
func (s *Service) measureProject(ctx context.Context, root string, base ProjectFootprint) ProjectFootprint {
	fp := base
	dir := filepath.Join(root, "projects", fp.ID)
	fp.Bytes, _ = dirSize(ctx, dir)
	fp.AttachmentBytes, _ = dirSize(ctx, filepath.Join(dir, "attachments"))
	if !filePresent(filepath.Join(dir, "project.db")) {
		return fp
	}
	handle, err := s.ws.Project(ctx, fp.ID)
	if err != nil {
		return fp
	}
	var count int
	if err := handle.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM notes WHERE deleted_at IS NULL`).Scan(&count); err == nil {
		fp.NoteCount = count
	}
	return fp
}
