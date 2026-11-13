// Package mcpfacade exposes the safe subset of the SPEC-03 project, overview and
// export services that SPEC-11 MCP tools are allowed to call (§5.7).
//
// The facade is a deliberate allow-list, not a wrapper that hides methods at
// runtime: it holds no permanent-delete executor and offers no edit, settings,
// quick-action or trash entry point. A tool that needs one of those must go
// through the UI, where the in-app confirmation dialog issues the one-time
// token consumed by project.Service.PermanentDelete (§5.4, §5.7).
package mcpfacade

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"researchos/internal/export"
	foundationworkspace "researchos/internal/foundation/workspace"
	"researchos/internal/overview"
	"researchos/internal/ports"
	"researchos/internal/project"
)

// ErrUnavailable reports a facade method whose required service was not wired.
// Callers match it with errors.Is; every other error is passed through from the
// underlying service.
var ErrUnavailable = errors.New("mcpfacade: dependency unavailable")

// Options configures a Service. Each service enables exactly the methods that
// need it; a missing one makes those methods report ErrUnavailable instead of
// panicking.
type Options struct {
	// Project backs the project read methods, archive/unarchive and the delete
	// preparation.
	Project *project.Service
	// Overview backs Overview.
	Overview *overview.Service
	// Export backs StartExport, ExportProgress and AwaitExport.
	Export *export.Service
	// Workspace documents the SPEC-01 workspace the services are bound to. The
	// facade itself only delegates to the services above, so it is not
	// retained; it is accepted for wiring parity with the other layer
	// constructors.
	Workspace *foundationworkspace.Workspace
}

// Service is the MCP facade. It is safe for concurrent use; the only mutable
// state is the in-memory plan → job mapping of started exports.
type Service struct {
	project  *project.Service
	overview *overview.Service
	export   *export.Service

	mu   sync.Mutex
	jobs map[string]string
}

// New builds a Service from opts.
func New(opts Options) *Service {
	return &Service{
		project:  opts.Project,
		overview: opts.Overview,
		export:   opts.Export,
		jobs:     make(map[string]string, 1),
	}
}

// ListProjects returns the projects matching q; archived ones only with
// IncludeArchived (§5.3).
func (m *Service) ListProjects(ctx context.Context, q project.ListQuery) ([]project.Project, error) {
	svc, err := m.projects()
	if err != nil {
		return nil, err
	}
	return svc.List(ctx, q)
}

// GetProject returns one project; a missing id yields project.ErrNotFound.
func (m *Service) GetProject(ctx context.Context, id string) (*project.Project, error) {
	svc, err := m.projects()
	if err != nil {
		return nil, err
	}
	return svc.Get(ctx, id)
}

// ProjectStats aggregates one project database. Archived projects are allowed.
func (m *Service) ProjectStats(ctx context.Context, id string) (project.Stats, error) {
	svc, err := m.projects()
	if err != nil {
		return project.Stats{}, err
	}
	return svc.Stats(ctx, id)
}

// ArchiveProject archives a project as actor=mcp, so the emitted status.changed
// event is attributable to the tool call (§7.1).
func (m *Service) ArchiveProject(ctx context.Context, id string) error {
	svc, err := m.projects()
	if err != nil {
		return err
	}
	return svc.Archive(ctx, id, ports.ActorMCP)
}

// UnarchiveProject restores an archived project as actor=mcp.
func (m *Service) UnarchiveProject(ctx context.Context, id string) error {
	svc, err := m.projects()
	if err != nil {
		return err
	}
	return svc.Unarchive(ctx, id, ports.ActorMCP)
}

// PrepareDeleteProject builds the read-only delete impact plan behind SPEC-11's
// project_delete dry_run/pending path. It modifies nothing and issues no
// confirmation token: the token comes from the in-app dialog (§5.4, §5.7).
func (m *Service) PrepareDeleteProject(ctx context.Context, id string) (*project.DeletePlan, error) {
	svc, err := m.projects()
	if err != nil {
		return nil, err
	}
	return svc.PrepareDelete(ctx, id)
}

// Overview returns the §5.6 snapshot of one project.
func (m *Service) Overview(ctx context.Context, projectID string) (*overview.Snapshot, error) {
	if m == nil || m.overview == nil {
		return nil, fmt.Errorf("%w: overview service", ErrUnavailable)
	}
	return m.overview.Load(ctx, projectID)
}

// StartExport plans and starts an export as actor=mcp, so the recorded
// project.exported activity is attributable to the tool call. An invalid
// request fails in Plan and is passed through unchanged. It returns the job id
// SPEC-11 should poll (§5.7, §5.9).
func (m *Service) StartExport(ctx context.Context, req export.Request) (string, error) {
	if m == nil || m.export == nil {
		return "", fmt.Errorf("%w: export service", ErrUnavailable)
	}
	req.Actor = ports.ActorMCP
	plan, err := m.export.Plan(ctx, req)
	if err != nil {
		return "", err
	}
	jobID, err := m.export.Start(ctx, plan.ID)
	if err != nil {
		return "", err
	}
	if jobID != plan.ID {
		// Today Start returns the plan id; keep the mapping for a future
		// implementation so both ids resolve.
		m.rememberJob(plan.ID, jobID)
	}
	return jobID, nil
}

// ExportProgress subscribes to a started export job's progress stream. The
// channel closes when the job settles.
func (m *Service) ExportProgress(ctx context.Context, jobID string) (<-chan export.Progress, error) {
	if m == nil || m.export == nil {
		return nil, fmt.Errorf("%w: export service", ErrUnavailable)
	}
	return m.export.Progress(ctx, m.resolveJob(jobID))
}

// AwaitExport blocks until the job settles and returns its result.
func (m *Service) AwaitExport(ctx context.Context, jobID string) (*export.Result, error) {
	if m == nil || m.export == nil {
		return nil, fmt.Errorf("%w: export service", ErrUnavailable)
	}
	return m.export.Await(ctx, m.resolveJob(jobID))
}

// projects returns the project service or ErrUnavailable.
func (m *Service) projects() (*project.Service, error) {
	if m == nil || m.project == nil {
		return nil, fmt.Errorf("%w: project service", ErrUnavailable)
	}
	return m.project, nil
}

// rememberJob records a plan id → job id mapping for exports whose job id is
// not the plan id.
func (m *Service) rememberJob(planID, jobID string) {
	m.mu.Lock()
	if m.jobs == nil {
		m.jobs = make(map[string]string, 1)
	}
	m.jobs[planID] = jobID
	m.mu.Unlock()
}

// resolveJob accepts either a job id or the plan id of the same export.
func (m *Service) resolveJob(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if jobID, ok := m.jobs[id]; ok {
		return jobID
	}
	return id
}
