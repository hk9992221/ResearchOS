package experiments

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/externalrefs"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/relations"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/trash"
	"researchos/internal/foundation/workspace"
)

// Service is the SPEC-07 service layer of one project. It is the single entry
// point shared by the UI and the MCP server (§1.1, §5). Use Experiments(),
// Runs(), Scanning(), Metadata(), Metrics(), Context(), Snapshots(), Git() and
// Ideas() to obtain the §5 interfaces.
type Service struct {
	ws        *workspace.Workspace
	projectID string
	clock     clock.Clock
	logger    workspace.Logger
	ai        *AIConfig
	mu        sync.Mutex
	pending   map[string]Snapshot
}

// New binds the experiment service to one project of ws.
func New(ws *workspace.Workspace, projectID string, opt Options) *Service {
	if opt.Clock == nil {
		opt.Clock = clock.System{}
	}
	return &Service{
		ws:        ws,
		projectID: projectID,
		clock:     opt.Clock,
		logger:    opt.Logger,
		ai:        opt.AI,
		pending:   map[string]Snapshot{},
	}
}

// handle resolves the project database handle.
func (s *Service) handle(ctx context.Context) (*workspace.ProjectHandle, error) {
	h, err := s.ws.Project(ctx, s.projectID)
	if err != nil {
		return nil, fmt.Errorf("experiments: open project %s: %w", s.projectID, err)
	}
	return h, nil
}

// assertWritable enforces the archived read-only rule (§5, AC-17). The status
// lives in global.db.projects, owned by SPEC-03; it is read here rather than
// imported to keep the module boundary one-way.
func (s *Service) assertWritable(ctx context.Context) error {
	global := s.ws.Global()
	if global == nil {
		return fmt.Errorf("%w: workspace is closed", ErrReadOnlyProject)
	}
	var status string
	err := global.DB().QueryRowContext(ctx, `SELECT status FROM projects WHERE id = ?`, s.projectID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("project %s", s.projectID)
	}
	if err != nil {
		return fmt.Errorf("experiments: read project status: %w", err)
	}
	if schema.ProjectStatus(status) == schema.ProjectArchived {
		return fmt.Errorf("%w: project %s", ErrReadOnlyProject, s.projectID)
	}
	return nil
}

// ---------------------------------------------------------------------------
// ExperimentService
// ---------------------------------------------------------------------------

// Create inserts one experiment and, when a directory is supplied, registers
// its external reference in the same transaction (§5.1).
func (s *Service) CreateExperiment(ctx context.Context, in CreateExperimentInput) (*Experiment, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, fmt.Errorf("%w: title must not be empty", ErrInvalidInput)
	}
	rules := DefaultScanRules()
	if in.Rules != nil {
		rules = in.Rules.withDefaults()
	}
	if err := rules.Validate(); err != nil {
		return nil, err
	}
	var (
		dirPath string
		err     error
	)
	if strings.TrimSpace(in.DirPath) != "" {
		dirPath, err = s.resolveDir(in.DirPath)
		if err != nil {
			return nil, err
		}
	}
	handle, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.assertWritable(ctx); err != nil {
		return nil, err
	}
	actor := actorOf(in.Actor)
	now := s.clock.Now()
	exp := Experiment{
		Title:     title,
		Purpose:   in.Purpose,
		ScanRules: rules,
		CreatedAt: now,
		UpdatedAt: now,
	}
	exp.ID, err = ids.New(s.clock)
	if err != nil {
		return nil, fmt.Errorf("experiments: generate id: %w", err)
	}
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		if err := s.insertExperiment(ctx, tx, exp); err != nil {
			return err
		}
		if dirPath != "" {
			if err := s.setExperimentDirRef(ctx, tx, exp.ID, dirPath); err != nil {
				return err
			}
			refs := externalrefs.New(handle.DB(), handle.Events(), s.clock)
			if _, err := refs.Register(ctx, tx, externalrefs.Ref{
				OwnerType: schema.EntityExperiment,
				OwnerID:   exp.ID,
				Field:     externalrefs.FieldDirRef,
				Kind:      externalrefs.KindDirectory,
				Path:      dirPath,
			}); err != nil {
				return fmt.Errorf("experiments: register directory: %w", err)
			}
		}
		return handle.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventEntityCreated,
			EntityType: schema.EntityExperiment,
			EntityID:   exp.ID,
			New:        &events.Object{"title": title},
			Summary:    "创建实验：" + title,
			Actor:      actor,
		})
	})
	if err != nil {
		return nil, err
	}
	created, err := s.loadExperiment(ctx, handle.DB(), exp.ID, false)
	if err != nil {
		return nil, err
	}
	return &created, nil
}

// Update edits title/purpose. No Activity event exists for entity edits
// (§7.1, O-09), so none is written.
func (s *Service) UpdateExperiment(ctx context.Context, in UpdateExperimentInput) (*Experiment, error) {
	if in.Title == nil && in.Purpose == nil {
		return nil, fmt.Errorf("%w: nothing to update", ErrInvalidInput)
	}
	if in.Title != nil {
		title := strings.TrimSpace(*in.Title)
		if title == "" {
			return nil, fmt.Errorf("%w: title must not be empty", ErrInvalidInput)
		}
		in.Title = &title
	}
	handle, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.assertWritable(ctx); err != nil {
		return nil, err
	}
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		return s.updateExperiment(ctx, tx, in.ID, in.Title, in.Purpose)
	})
	if err != nil {
		return nil, err
	}
	updated, err := s.loadExperiment(ctx, handle.DB(), in.ID, false)
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

// SoftDelete moves one experiment into the trash. Runs and snapshots stay.
func (s *Service) SoftDeleteExperiment(ctx context.Context, id string, actor events.Actor) error {
	handle, err := s.handle(ctx)
	if err != nil {
		return err
	}
	if err := s.assertWritable(ctx); err != nil {
		return err
	}
	if _, err := s.loadExperiment(ctx, handle.DB(), id, true); err != nil {
		return err
	}
	trashSvc := trash.New(s.projectID, handle.DB(), s.ws.Paths(), handle.Events(), nil, s.clock)
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		return trashSvc.SoftDelete(ctx, tx, trash.Ref{Type: schema.EntityExperiment, ID: id}, actorOf(actor))
	})
}

// Restore clears the deleted_at of one experiment. Archived projects reject it.
func (s *Service) RestoreExperiment(ctx context.Context, id string, actor events.Actor) error {
	handle, err := s.handle(ctx)
	if err != nil {
		return err
	}
	if err := s.assertWritable(ctx); err != nil {
		return err
	}
	if _, err := s.loadExperiment(ctx, handle.DB(), id, true); err != nil {
		return err
	}
	trashSvc := trash.New(s.projectID, handle.DB(), s.ws.Paths(), handle.Events(), nil, s.clock)
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		return trashSvc.Restore(ctx, tx, trash.Ref{Type: schema.EntityExperiment, ID: id}, actorOf(actor))
	})
}

// Get returns the experiment detail. Directory status comes from storage and
// never probes the filesystem (§5.1).
func (s *Service) GetExperiment(ctx context.Context, id string) (*ExperimentDetail, error) {
	handle, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	exp, err := s.loadExperiment(ctx, handle.DB(), id, false)
	if err != nil {
		return nil, err
	}
	detail := &ExperimentDetail{Experiment: exp}
	detail.DirRefs, err = s.loadDirRefs(ctx, handle.DB(), schema.EntityExperiment, exp.ID)
	if err != nil {
		return nil, err
	}
	detail.Runs, err = s.ListRuns(ctx, exp.ID, false)
	if err != nil {
		return nil, err
	}
	detail.RecentSnapshots, err = s.ListSnapshots(ctx, TargetRef{Type: TargetExperiment, ID: exp.ID}, SnapshotFilter{Limit: 10})
	if err != nil {
		return nil, err
	}
	detail.LinkedIdeas, err = s.ListIdeas(ctx, exp.ID)
	if err != nil {
		return nil, err
	}
	return detail, nil
}

// List returns experiment summaries, excluding soft-deleted rows by default.
func (s *Service) ListExperiments(ctx context.Context, f ExperimentFilter) ([]ExperimentSummary, error) {
	handle, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	query := `SELECT e.id, e.title, e.purpose, e.dir_ref, e.updated_at, e.deleted_at,
	        (SELECT COUNT(*) FROM runs r WHERE r.experiment_id = e.id AND r.deleted_at IS NULL),
	        (SELECT r.status FROM runs r WHERE r.experiment_id = e.id AND r.deleted_at IS NULL
	           ORDER BY r.created_at DESC, r.id DESC LIMIT 1),
	        (SELECT MAX(s.created_at) FROM snapshots s
	           WHERE (s.target_type = 'experiment' AND s.target_id = e.id)
	              OR (s.target_type = 'run' AND s.target_id IN
	                    (SELECT r2.id FROM runs r2 WHERE r2.experiment_id = e.id))),
	        (SELECT COUNT(*) FROM entity_relations er
	           WHERE er.src_type = 'experiment' AND er.src_id = e.id AND er.relation = 'validates'),
	        (SELECT x.status FROM external_refs x
	           WHERE x.owner_type = 'experiment' AND x.owner_id = e.id AND x.field = 'dir_ref')
	      FROM experiments e WHERE 1 = 1`
	var args []any
	if !f.IncludeDeleted {
		query += ` AND e.deleted_at IS NULL`
	}
	if title := strings.TrimSpace(f.Title); title != "" {
		query += ` AND e.title LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(title)+"%")
	}
	if f.DirStatus != "" {
		query += ` AND (SELECT x.status FROM external_refs x
		                  WHERE x.owner_type = 'experiment' AND x.owner_id = e.id AND x.field = 'dir_ref') = ?`
		args = append(args, string(f.DirStatus))
	}
	if f.IdeaID != "" {
		query += ` AND EXISTS (SELECT 1 FROM entity_relations er2
		                     WHERE er2.src_type = 'experiment' AND er2.src_id = e.id
		                       AND er2.relation = 'validates' AND er2.dst_type = 'idea' AND er2.dst_id = ?)`
		args = append(args, f.IdeaID)
	}
	if f.AnalyzedAfter != nil {
		query += ` AND (SELECT MAX(s.created_at) FROM snapshots s
		                  WHERE (s.target_type = 'experiment' AND s.target_id = e.id)
		                     OR (s.target_type = 'run' AND s.target_id IN
		                           (SELECT r3.id FROM runs r3 WHERE r3.experiment_id = e.id))) >= ?`
		args = append(args, formatTime(*f.AnalyzedAfter))
	}
	query += ` ORDER BY ` + experimentOrder(f.Sort)
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query += ` LIMIT ? OFFSET ?`
	args = append(args, limit, max(f.Offset, 0))

	rows, err := handle.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("experiments: list: %w", err)
	}
	defer rows.Close()
	out := make([]ExperimentSummary, 0, limit)
	for rows.Next() {
		var (
			item      ExperimentSummary
			dirRef    sql.NullString
			updatedAt string
			deletedAt sql.NullString
			lastRun   sql.NullString
			lastAnaly sql.NullString
			dirStatus sql.NullString
		)
		if err := rows.Scan(&item.ID, &item.Title, &item.Purpose, &dirRef, &updatedAt, &deletedAt,
			&item.RunCount, &lastRun, &lastAnaly, &item.LinkedIdeas, &dirStatus); err != nil {
			return nil, fmt.Errorf("experiments: list: %w", err)
		}
		if dirRef.Valid {
			item.DirRef = &dirRef.String
		}
		if dirStatus.Valid {
			item.DirStatus = DirStatus(dirStatus.String)
		}
		item.LastRunStatus = RunUnknown
		if lastRun.Valid {
			item.LastRunStatus = RunStatus(lastRun.String)
		}
		if lastAnaly.Valid {
			if at, err := clock.Parse(lastAnaly.String); err == nil {
				item.LastAnalyzedAt = &at
			}
		}
		at, err := clock.Parse(updatedAt)
		if err != nil {
			return nil, fmt.Errorf("experiments: list updated_at: %w", err)
		}
		item.UpdatedAt = at
		if deletedAt.Valid {
			if at, err := clock.Parse(deletedAt.String); err == nil {
				item.DeletedAt = &at
			}
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("experiments: list: %w", err)
	}
	return out, nil
}

// experimentOrder whitelists the sort column.
func experimentOrder(sortKey string) string {
	switch sortKey {
	case "title":
		return "e.title ASC, e.id ASC"
	case "created_at":
		return "e.created_at DESC, e.id DESC"
	case "updated_at", "":
		return "e.updated_at DESC, e.id DESC"
	default:
		return "e.updated_at DESC, e.id DESC"
	}
}

// BindDirectory binds an external directory to an experiment (§5.1).
func (s *Service) BindDirectory(ctx context.Context, experimentID, absPath string, rules ScanRules) (*DirReference, error) {
	dir, err := s.resolveDir(absPath)
	if err != nil {
		return nil, err
	}
	if rules.isZero() {
		rules = DefaultScanRules()
	} else {
		rules = rules.withDefaults()
		if err := rules.Validate(); err != nil {
			return nil, err
		}
	}
	handle, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.assertWritable(ctx); err != nil {
		return nil, err
	}
	var refID string
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := s.loadExperiment(ctx, tx, experimentID, false); err != nil {
			return err
		}
		if err := s.setExperimentDirRef(ctx, tx, experimentID, dir); err != nil {
			return err
		}
		refs := externalrefs.New(handle.DB(), handle.Events(), s.clock)
		id, err := refs.Register(ctx, tx, externalrefs.Ref{
			OwnerType: schema.EntityExperiment,
			OwnerID:   experimentID,
			Field:     externalrefs.FieldDirRef,
			Kind:      externalrefs.KindDirectory,
			Path:      dir,
		})
		if err != nil {
			return fmt.Errorf("experiments: register directory: %w", err)
		}
		refID = id
		return nil
	})
	if err != nil {
		return nil, err
	}
	statuses, err := s.checkRefsOwner(ctx, handle, schema.EntityExperiment, experimentID, []string{refID})
	if err != nil {
		return nil, err
	}
	if len(statuses) == 0 {
		return nil, notFound("directory reference of %s", experimentID)
	}
	return &statuses[0], nil
}

// RelocateDirectory re-points an existing binding. Old snapshots keep their
// original paths and hashes (§5.1).
func (s *Service) RelocateDirectory(ctx context.Context, experimentID, absPath string, actor events.Actor) (*DirReference, error) {
	dir, err := s.resolveDir(absPath)
	if err != nil {
		return nil, err
	}
	handle, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.assertWritable(ctx); err != nil {
		return nil, err
	}
	if _, err := s.loadExperiment(ctx, handle.DB(), experimentID, false); err != nil {
		return nil, err
	}
	refID, err := s.dirRefID(ctx, handle.DB(), schema.EntityExperiment, experimentID)
	if err != nil {
		return nil, err
	}
	refs := externalrefs.New(handle.DB(), handle.Events(), s.clock)
	result, err := refs.Relocate(ctx, externalrefs.RelocateRequest{
		ProjectID: s.projectID,
		RefID:     refID,
		NewPath:   dir,
	}, actorOf(actor))
	if err != nil {
		return nil, fmt.Errorf("experiments: relocate directory: %w", err)
	}
	ref := convertRef(result.Status)
	return &ref, nil
}

// VerifyDirectory probes experiment and run directory references read-only
// (§5.1). Nothing is written.
func (s *Service) VerifyDirectory(ctx context.Context, experimentID string) ([]DirReference, error) {
	handle, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.loadExperiment(ctx, handle.DB(), experimentID, false); err != nil {
		return nil, err
	}
	refs := externalrefs.New(handle.DB(), handle.Events(), s.clock)
	expRefs, err := s.loadDirRefs(ctx, handle.DB(), schema.EntityExperiment, experimentID)
	if err != nil {
		return nil, err
	}
	refIDs := make([]string, 0, len(expRefs)+2)
	for _, ref := range expRefs {
		refIDs = append(refIDs, ref.RefID)
	}
	runs, err := s.ListRuns(ctx, experimentID, false)
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		runRefs, err := s.loadDirRefs(ctx, handle.DB(), schema.EntityRun, run.ID)
		if err != nil {
			return nil, err
		}
		for _, ref := range runRefs {
			refIDs = append(refIDs, ref.RefID)
		}
	}
	if len(refIDs) == 0 {
		return []DirReference{}, nil
	}
	statuses, err := refs.CheckAndRecord(ctx, s.projectID, refIDs)
	if err != nil {
		return nil, fmt.Errorf("experiments: verify directories: %w", err)
	}
	out := make([]DirReference, 0, len(statuses))
	for _, st := range statuses {
		out = append(out, convertRef(st))
	}
	return out, nil
}

// SetScanRules validates and stores the full rule set (§5.1).
func (s *Service) SetScanRules(ctx context.Context, experimentID string, rules ScanRules) error {
	rules = rules.withDefaults()
	if !containsPattern(rules.Exclude, ".git/") {
		rules.Exclude = append(rules.Exclude, ".git/")
	}
	if err := rules.Validate(); err != nil {
		return err
	}
	handle, err := s.handle(ctx)
	if err != nil {
		return err
	}
	if err := s.assertWritable(ctx); err != nil {
		return err
	}
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		return s.writeScanRules(ctx, tx, experimentID, rules)
	})
}

// containsPattern reports whether want is already in patterns.
func containsPattern(patterns []string, want string) bool {
	for _, pattern := range patterns {
		if strings.EqualFold(strings.TrimSpace(pattern), want) {
			return true
		}
	}
	return false
}

// loadDirRefs reads the stored external references of one owner without
// probing the filesystem.
func (s *Service) loadDirRefs(ctx context.Context, q queryer, owner events.EntityType, ownerID string) ([]DirReference, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, owner_type, owner_id, path, status, last_checked_at
		   FROM external_refs WHERE owner_type = ? AND owner_id = ? AND field = ? ORDER BY created_at, id`,
		string(owner), ownerID, externalrefs.FieldDirRef)
	if err != nil {
		return nil, fmt.Errorf("experiments: read directory references: %w", err)
	}
	defer rows.Close()
	out := make([]DirReference, 0, 1)
	for rows.Next() {
		var (
			ref    DirReference
			oType  string
			status string
			last   sql.NullString
		)
		if err := rows.Scan(&ref.RefID, &oType, &ref.OwnerID, &ref.Path, &status, &last); err != nil {
			return nil, fmt.Errorf("experiments: read directory references: %w", err)
		}
		ref.OwnerType = TargetType(oType)
		ref.Status = DirStatus(status)
		ref.Exists = ref.Status == DirOK
		if last.Valid {
			if at, err := clock.Parse(last.String); err == nil {
				ref.LastCheckedAt = &at
			}
		}
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("experiments: read directory references: %w", err)
	}
	return out, nil
}

// checkRefsOwner probes specific references read-only.
func (s *Service) checkRefsOwner(ctx context.Context, handle *workspace.ProjectHandle, owner events.EntityType, ownerID string, refIDs []string) ([]DirReference, error) {
	refs := externalrefs.New(handle.DB(), handle.Events(), s.clock)
	statuses, err := refs.CheckOwner(ctx, s.projectID, owner, ownerID)
	if err != nil {
		return nil, fmt.Errorf("experiments: probe directory: %w", err)
	}
	out := make([]DirReference, 0, len(statuses))
	for _, st := range statuses {
		if len(refIDs) > 0 && !containsString(refIDs, st.ID) {
			continue
		}
		out = append(out, convertRef(st))
	}
	return out, nil
}

// dirRefID returns the external_refs id of one owner's directory binding.
func (s *Service) dirRefID(ctx context.Context, q queryer, owner events.EntityType, ownerID string) (string, error) {
	var id string
	err := q.QueryRowContext(ctx,
		`SELECT id FROM external_refs WHERE owner_type = ? AND owner_id = ? AND field = ?`,
		string(owner), ownerID, externalrefs.FieldDirRef).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: %s %s has no directory binding", ErrDirUnavailable, owner, ownerID)
	}
	if err != nil {
		return "", fmt.Errorf("experiments: read directory reference: %w", err)
	}
	return id, nil
}

// convertRef maps an externalrefs probe result to the public projection. The
// probe result carries the stored status in Ref; the live answer is Exists, so
// broken wins whenever the path cannot be probed (§4.3).
func convertRef(st externalrefs.Status) DirReference {
	status := DirBroken
	if st.Exists {
		status = DirOK
	}
	return DirReference{
		RefID:         st.ID,
		OwnerType:     TargetType(st.OwnerType),
		OwnerID:       st.OwnerID,
		Path:          st.Path,
		Status:        status,
		Exists:        st.Exists,
		Reason:        st.Reason,
		LastCheckedAt: st.LastCheckedAt,
	}
}

// resolveDir validates and cleans an external directory path without writing
// or resolving the stored value (§5.1 BindDirectory, §3.2 non-invasive rule).
func (s *Service) resolveDir(absPath string) (string, error) {
	trimmed := strings.TrimSpace(absPath)
	if trimmed == "" {
		return "", fmt.Errorf("%w: empty directory path", ErrInvalidInput)
	}
	if !filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("%w: %q must be an absolute path", ErrInvalidInput, trimmed)
	}
	cleaned := filepath.Clean(trimmed)
	info, err := os.Stat(cleaned)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrDirUnavailable, cleaned, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s is not a directory", ErrDirUnavailable, cleaned)
	}
	if err := checkReadableDir(cleaned); err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrDirUnavailable, cleaned, err)
	}
	if s.ws.Paths().IsInternal(cleaned) {
		return "", fmt.Errorf("%w: %s is inside the workspace", ErrDirGuard, cleaned)
	}
	if real, err := filepath.EvalSymlinks(cleaned); err == nil && s.ws.Paths().IsInternal(real) {
		return "", fmt.Errorf("%w: %s resolves inside the workspace", ErrDirGuard, cleaned)
	}
	return cleaned, nil
}

// checkReadableDir opens the directory read-only and reads one entry. io.EOF
// is the expected answer for an empty directory.
func checkReadableDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Readdirnames(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// escapeLike escapes the LIKE wildcards of a user-supplied substring.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	return strings.ReplaceAll(s, `_`, `\_`)
}

// containsString reports whether list contains want.
func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// mathInf guards percentage computation against division by zero.
func pctChange(base, value float64) (float64, bool) {
	if base == 0 {
		return 0, false
	}
	return (value - base) / math.Abs(base) * 100, true
}

// relationsService builds the foundation relation service for this project.
func (s *Service) relationsService(handle *workspace.ProjectHandle) *relations.Service {
	var global *sql.DB
	if g := s.ws.Global(); g != nil {
		global = g.DB()
	}
	return relations.New(handle.DB(), global, handle.Events(), s.clock)
}
