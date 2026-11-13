package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/workspace"
)

// DeletePlan is the read-only impact preview of PrepareDelete (§5.4). It never
// carries a confirmation token: the token is issued by the UI at the final
// confirmation step.
type DeletePlan struct {
	ProjectID          string
	ProjectName        string
	EntityCounts       map[string]int
	TrashedCount       int
	DirBytes           int64
	PaperIDs           []string
	SharedPaperIDs     []string
	ExclusivePaperIDs  []string
	PaperImpactUnknown bool
	LastExportedAt     *time.Time
	ExportRecommended  bool
	Blockers           []Blocker
	PreparedAt         time.Time
}

// PermanentDeleteRequest authorizes deleting a project permanently. ConfirmName
// must equal the trimmed project name; Token is a one-time in-app confirmation
// token (Kind=KindDeleteProject) consumed by SPEC-01's executor.
type PermanentDeleteRequest struct {
	ProjectID   string
	ConfirmName string
	Token       confirm.Token
}

// Blocker is one reason the delete wizard must not proceed yet (§5.4).
type Blocker struct {
	Code    string
	Message string
}

// DeleteResult summarizes an executed permanent deletion.
type DeleteResult struct {
	ProjectID      string
	RemovedPapers  []string
	RetainedPapers []string
	Warnings       []string
	Duration       time.Duration
}

// deleteCountTables is the §5.4 entity count list. Every table keeps purged
// rows out by deleting them, so count(*) is the "all non-purged" count.
var deleteCountTables = []struct {
	table string
	key   string
}{
	{"notes", "note"},
	{"ideas", "idea"},
	{"experiments", "experiment"},
	{"runs", "run"},
	{"tasks", "task"},
	{"evidence", "evidence"},
	{"weekly_reports", "weekly_report"},
}

// PrepareDelete builds the impact plan without modifying anything (§5.4). It
// holds no lock across the reads and issues no token; MCP's dry_run maps here.
// The only lock interaction is the fail-fast blocker probe at the end.
func (s *Service) PrepareDelete(ctx context.Context, id string) (*DeletePlan, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.ws == nil {
		return nil, errNoWorkspace
	}
	h, err := s.ws.Project(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("project: prepare delete %s: %w", id, err)
	}
	db := h.DB()

	plan := &DeletePlan{
		ProjectID:    id,
		ProjectName:  cur.Name,
		EntityCounts: make(map[string]int, len(deleteCountTables)),
	}
	for _, tc := range deleteCountTables {
		n, err := countRows(ctx, db, `SELECT count(*) FROM `+tc.table)
		if err != nil {
			return nil, fmt.Errorf("project: prepare delete %s: %s count: %w", id, tc.key, err)
		}
		plan.EntityCounts[tc.key] = n
	}
	if plan.TrashedCount, err = countRows(ctx, db, `SELECT count(*) FROM v_trash`); err != nil {
		return nil, fmt.Errorf("project: prepare delete %s: trash count: %w", id, err)
	}
	if plan.DirBytes, err = s.dirBytes(ctx, id); err != nil {
		return nil, err
	}

	plan.PaperIDs, plan.SharedPaperIDs, plan.ExclusivePaperIDs, plan.PaperImpactUnknown, err =
		s.paperImpact(ctx, id, db)
	if err != nil {
		return nil, fmt.Errorf("project: prepare delete %s: paper impact: %w", id, err)
	}

	plan.LastExportedAt, err = lastExportedAt(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("project: prepare delete %s: export history: %w", id, err)
	}
	plan.ExportRecommended = plan.LastExportedAt == nil

	if plan.Blockers, err = s.deleteBlockers(ctx, id); err != nil {
		return nil, err
	}
	plan.PreparedAt = s.clock.Now()
	return plan, nil
}

// PermanentDelete executes the §5.4 deletion pipeline. The in-app confirmation
// is validated before any side effect; SPEC-01's executor consumes the token.
// No Activity is written: the project database disappears with the project.
func (s *Service) PermanentDelete(ctx context.Context, req PermanentDeleteRequest) (*DeleteResult, error) {
	if s.ws == nil {
		return nil, errNoWorkspace
	}
	cur, err := s.Get(ctx, req.ProjectID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.ConfirmName) == "" || req.Token == "" {
		return nil, ErrDeleteNotConfirmed
	}
	if strings.TrimSpace(req.ConfirmName) != strings.TrimSpace(cur.Name) {
		return nil, ErrDeleteNotConfirmed
	}

	start := time.Now()
	var result *DeleteResult
	err = s.locker.WithWorkspaceLock(ctx, func() error {
		// Modules flush their buffers before the directory disappears. Close is
		// a no-op when this project is not the active session.
		if err := s.Close(ctx, req.ProjectID); err != nil {
			return err
		}
		// PrepareDelete/Stats may have cached a handle without opening a
		// session; evict it so Windows can remove the directory.
		if err := s.ws.CloseProject(req.ProjectID); err != nil {
			return fmt.Errorf("project: delete %s: close handle: %w", req.ProjectID, err)
		}

		// The candidate set must be read while the project database exists; it
		// is the compensation/impact baseline of §5.4 step 4.
		candidates := s.capturePaperIDs(ctx, req.ProjectID)

		wr, derr := s.ws.DeleteProjectData(ctx, workspace.DeleteProjectRequest{
			ProjectID: req.ProjectID,
			Confirm:   req.Token,
		})
		result = &DeleteResult{
			ProjectID:      req.ProjectID,
			RemovedPapers:  wr.RemovedPapers,
			RetainedPapers: wr.RetainedPapers,
			Warnings:       wr.Warnings,
		}
		if derr != nil {
			if errors.Is(derr, sql.ErrNoRows) {
				return fmt.Errorf("%w: project %s disappeared during deletion: %w", ErrNotFound, req.ProjectID, derr)
			}
			return derr
		}
		if len(candidates) > 0 {
			covered := len(wr.RemovedPapers) + len(wr.RetainedPapers)
			if covered < len(candidates) {
				result.Warnings = append(result.Warnings, fmt.Sprintf(
					"paper copy check covered %d of %d linked papers", covered, len(candidates)))
			}
		}
		return nil
	})
	if result != nil {
		result.Duration = time.Since(start)
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

// paperImpact returns the candidate papers and the shared/exclusive preview.
// A PaperIndex port overrides the project_papers fallback. Any unreadable
// project is reported through PaperImpactUnknown instead of inventing a
// retention decision.
func (s *Service) paperImpact(ctx context.Context, projectID string, db *sql.DB) (
	all, shared, exclusive []string, unknown bool, err error,
) {
	var candidates []string
	if s.papers != nil {
		candidates, err = s.papers.ListProjectPaperIDs(ctx, projectID)
		if err != nil {
			unknown = true
			candidates = nil
		}
	} else {
		candidates, err = projectPaperIDs(ctx, db)
		if err != nil {
			return nil, nil, nil, false, err
		}
	}
	candidates = dedupeSorted(candidates)
	if unknown {
		// PaperIndex failed: we do not know the candidate set either.
		return nil, nil, nil, true, nil
	}
	if len(candidates) == 0 {
		return candidates, nil, nil, false, nil
	}

	sharedSet := make(map[string]bool, len(candidates))
	if s.papers != nil {
		for _, id := range candidates {
			n, cerr := s.papers.CountPaperRefs(ctx, id, projectID)
			if cerr != nil {
				unknown = true
				continue
			}
			if n > 0 {
				sharedSet[id] = true
			}
		}
	} else {
		others, lerr := s.otherProjectIDs(ctx, projectID)
		if lerr != nil {
			return nil, nil, nil, false, lerr
		}
		cand := make(map[string]bool, len(candidates))
		for _, id := range candidates {
			cand[id] = true
		}
		for _, other := range others {
			h, oerr := s.ws.Project(ctx, other)
			if oerr != nil {
				unknown = true
				continue
			}
			ids, qerr := projectPaperIDs(ctx, h.DB())
			if qerr != nil {
				unknown = true
				continue
			}
			for _, id := range ids {
				if cand[id] {
					sharedSet[id] = true
				}
			}
		}
	}

	for _, id := range candidates {
		if sharedSet[id] {
			shared = append(shared, id)
		}
	}
	if !unknown {
		for _, id := range candidates {
			if !sharedSet[id] {
				exclusive = append(exclusive, id)
			}
		}
	}
	return candidates, shared, exclusive, unknown, nil
}

// projectPaperIDs reads the fallback candidate set: project_papers first, then
// references relations pointing at a paper when the association table is empty
// (§5.4).
func projectPaperIDs(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT paper_id FROM project_papers`)
	if err != nil {
		return nil, fmt.Errorf("project_papers: %w", err)
	}
	seen := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		seen[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(seen) == 0 {
		rows, err := db.QueryContext(ctx,
			`SELECT DISTINCT dst_id FROM entity_relations WHERE dst_type = ?`, string(schema.EntityPaper))
		if err != nil {
			return nil, fmt.Errorf("entity_relations: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			seen[id] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return sortedKeys(seen), nil
}

// otherProjectIDs lists every registered project except the one being deleted.
func (s *Service) otherProjectIDs(ctx context.Context, exclude string) ([]string, error) {
	if s.store == nil {
		return nil, errNoWorkspace
	}
	rows, err := s.store.List(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.ID != exclude {
			out = append(out, row.ID)
		}
	}
	return out, nil
}

// capturePaperIDs collects the best-effort candidate list before deletion.
func (s *Service) capturePaperIDs(ctx context.Context, projectID string) []string {
	if s.ws == nil {
		return nil
	}
	h, err := s.ws.Project(ctx, projectID)
	if err != nil {
		return nil
	}
	ids, err := projectPaperIDs(ctx, h.DB())
	if err != nil {
		return nil
	}
	return ids
}

// lastExportedAt returns the newest project.exported timestamp, or nil when the
// project has never been exported.
func lastExportedAt(ctx context.Context, db *sql.DB) (*time.Time, error) {
	var raw sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT max(created_at) FROM activities WHERE event_type = ?`,
		string(schema.EventProjectExported)).Scan(&raw); err != nil {
		return nil, err
	}
	if !raw.Valid {
		return nil, nil
	}
	at, err := clock.Parse(raw.String)
	if err != nil {
		return nil, err
	}
	return &at, nil
}

// deleteBlockers probes the workspace lock and the optional export-job
// registry. A nil Busy means only the locker is probed.
func (s *Service) deleteBlockers(ctx context.Context, projectID string) ([]Blocker, error) {
	var out []Blocker
	err := s.locker.WithWorkspaceLock(ctx, func() error { return nil })
	switch {
	case errors.Is(err, ErrBusy):
		out = append(out, Blocker{
			Code:    "migration_running",
			Message: "a workspace migration is running",
		})
	case err != nil:
		return nil, err
	}
	if s.busy != nil {
		code, err := s.busy(ctx, projectID)
		if err != nil {
			return nil, err
		}
		if code != "" {
			out = append(out, Blocker{Code: code, Message: blockerMessage(code)})
		}
	}
	return out, nil
}

func blockerMessage(code string) string {
	switch code {
	case "export_running":
		return "an export job is running for this project"
	case "project_open_busy":
		return "the project is busy and cannot be deleted yet"
	case "migration_running":
		return "a workspace migration is running"
	default:
		return code
	}
}

// dedupeSorted returns the distinct values in ascending order.
func dedupeSorted(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
