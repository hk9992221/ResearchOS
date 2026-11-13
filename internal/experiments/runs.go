package experiments

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/externalrefs"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/trash"
)

// Create adds one run under an existing experiment (§5.2). The status starts at
// unknown because completion detection is out of scope (O-02).
func (s *Service) CreateRun(ctx context.Context, in CreateRunInput) (*Run, error) {
	handle, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.assertWritable(ctx); err != nil {
		return nil, err
	}
	exp, err := s.loadExperiment(ctx, handle.DB(), in.ExperimentID, false)
	if err != nil {
		return nil, err
	}
	var dirPath string
	if strings.TrimSpace(in.DirPath) != "" {
		dirPath, err = s.resolveDir(in.DirPath)
		if err != nil {
			return nil, err
		}
	}
	params, err := normalizeParams(in.Params)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	run := Run{
		ExperimentID: exp.ID,
		Params:       params,
		GitInfo:      GitInfo{V: payloadVersion},
		Status:       RunUnknown,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	run.ID, err = ids.New(s.clock)
	if err != nil {
		return nil, fmt.Errorf("experiments: generate id: %w", err)
	}
	if dirPath != "" {
		run.DirRef = &dirPath
	}
	err = handle.InTx(ctx, func(tx *sql.Tx) error {
		if err := s.insertRun(ctx, tx, run); err != nil {
			return err
		}
		if dirPath != "" {
			refs := externalrefs.New(handle.DB(), handle.Events(), s.clock)
			if _, err := refs.Register(ctx, tx, externalrefs.Ref{
				OwnerType: schema.EntityRun,
				OwnerID:   run.ID,
				Field:     externalrefs.FieldDirRef,
				Kind:      externalrefs.KindDirectory,
				Path:      dirPath,
			}); err != nil {
				return fmt.Errorf("experiments: register run directory: %w", err)
			}
		}
		return handle.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventEntityCreated,
			EntityType: schema.EntityRun,
			EntityID:   run.ID,
			New:        &events.Object{"experiment_id": exp.ID},
			Summary:    "创建 Run：" + exp.Title,
			Actor:      actorOf(in.Actor),
		})
	})
	if err != nil {
		return nil, err
	}
	created, err := s.loadRun(ctx, handle.DB(), run.ID, false)
	if err != nil {
		return nil, err
	}
	return &created, nil
}

// UpdateParams replaces the complete parameter map. No Activity event exists
// for this edit (§7.1, O-09).
func (s *Service) UpdateRunParams(ctx context.Context, runID string, params map[string]ParamValue) error {
	handle, err := s.handle(ctx)
	if err != nil {
		return err
	}
	if err := s.assertWritable(ctx); err != nil {
		return err
	}
	normalized, err := normalizeParams(params)
	if err != nil {
		return err
	}
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		return s.updateRunParams(ctx, tx, runID, normalized)
	})
}

// SetDirectory binds or re-points a run directory. Clearing is unsupported in
// v1 because externalrefs has no delete path (§5.2).
func (s *Service) SetRunDirectory(ctx context.Context, runID, absPath string) error {
	dir, err := s.resolveDir(absPath)
	if err != nil {
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
		if _, err := s.loadRun(ctx, tx, runID, false); err != nil {
			return err
		}
		if err := s.setRunDirRef(ctx, tx, runID, dir); err != nil {
			return err
		}
		refs := externalrefs.New(handle.DB(), handle.Events(), s.clock)
		if _, err := refs.Register(ctx, tx, externalrefs.Ref{
			OwnerType: schema.EntityRun,
			OwnerID:   runID,
			Field:     externalrefs.FieldDirRef,
			Kind:      externalrefs.KindDirectory,
			Path:      dir,
		}); err != nil {
			return fmt.Errorf("experiments: register run directory: %w", err)
		}
		return nil
	})
}

// SetStatus updates runs.status. No status.changed event is emitted because the
// SPEC-01 producer list does not include runs (§5.2, O-19).
func (s *Service) SetRunStatus(ctx context.Context, runID string, status RunStatus) error {
	if !status.Valid() {
		return fmt.Errorf("%w: invalid run status %q", ErrInvalidInput, status)
	}
	handle, err := s.handle(ctx)
	if err != nil {
		return err
	}
	if err := s.assertWritable(ctx); err != nil {
		return err
	}
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		return s.setRunStatus(ctx, tx, runID, status)
	})
}

// SoftDelete moves one run into the trash. Its snapshots are kept.
func (s *Service) SoftDeleteRun(ctx context.Context, runID string, actor events.Actor) error {
	handle, err := s.handle(ctx)
	if err != nil {
		return err
	}
	if err := s.assertWritable(ctx); err != nil {
		return err
	}
	if _, err := s.loadRun(ctx, handle.DB(), runID, true); err != nil {
		return err
	}
	trashSvc := trash.New(s.projectID, handle.DB(), s.ws.Paths(), handle.Events(), nil, s.clock)
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		return trashSvc.SoftDelete(ctx, tx, trash.Ref{Type: schema.EntityRun, ID: runID}, actorOf(actor))
	})
}

// Restore clears the deleted_at of one run.
func (s *Service) RestoreRun(ctx context.Context, runID string, actor events.Actor) error {
	handle, err := s.handle(ctx)
	if err != nil {
		return err
	}
	if err := s.assertWritable(ctx); err != nil {
		return err
	}
	if _, err := s.loadRun(ctx, handle.DB(), runID, true); err != nil {
		return err
	}
	trashSvc := trash.New(s.projectID, handle.DB(), s.ws.Paths(), handle.Events(), nil, s.clock)
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		return trashSvc.Restore(ctx, tx, trash.Ref{Type: schema.EntityRun, ID: runID}, actorOf(actor))
	})
}

// Get returns one run with its stored directory references.
func (s *Service) GetRun(ctx context.Context, runID string) (*RunDetail, error) {
	handle, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	run, err := s.loadRun(ctx, handle.DB(), runID, false)
	if err != nil {
		return nil, err
	}
	detail := &RunDetail{Run: run}
	detail.DirRefs, err = s.loadDirRefs(ctx, handle.DB(), schema.EntityRun, run.ID)
	if err != nil {
		return nil, err
	}
	return detail, nil
}

// List returns the runs of one experiment ordered by creation.
func (s *Service) ListRuns(ctx context.Context, experimentID string, includeDeleted bool) ([]RunSummary, error) {
	handle, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.loadExperiment(ctx, handle.DB(), experimentID, true); err != nil {
		return nil, err
	}
	query := `SELECT r.id, r.dir_ref, r.params, r.status, r.deleted_at,
	                 (SELECT MAX(s.created_at) FROM snapshots s
	                    WHERE s.target_type = 'run' AND s.target_id = r.id)
	            FROM runs r WHERE r.experiment_id = ?`
	if !includeDeleted {
		query += ` AND r.deleted_at IS NULL`
	}
	query += ` ORDER BY r.created_at, r.id`
	rows, err := handle.DB().QueryContext(ctx, query, experimentID)
	if err != nil {
		return nil, fmt.Errorf("experiments: list runs: %w", err)
	}
	defer rows.Close()
	out := make([]RunSummary, 0, 8)
	for rows.Next() {
		var (
			item      RunSummary
			dirRef    sql.NullString
			rawParams string
			status    string
			deletedAt sql.NullString
			lastAnaly sql.NullString
		)
		if err := rows.Scan(&item.ID, &dirRef, &rawParams, &status, &deletedAt, &lastAnaly); err != nil {
			return nil, fmt.Errorf("experiments: list runs: %w", err)
		}
		if dirRef.Valid {
			item.DirRef = &dirRef.String
		}
		item.Params = decodeParams([]byte(rawParams))
		item.ParamCount = len(item.Params)
		item.Status = RunStatus(status)
		if !item.Status.Valid() {
			item.Status = RunUnknown
		}
		item.DisplayName = runDisplayName(item)
		if deletedAt.Valid {
			if at, err := clock.Parse(deletedAt.String); err == nil {
				item.DeletedAt = &at
			}
		}
		if lastAnaly.Valid {
			if at, err := clock.Parse(lastAnaly.String); err == nil {
				item.LastAnalyzed = &at
			}
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("experiments: list runs: %w", err)
	}
	return out, nil
}

// runDisplayName derives a display name from the directory or the parameters;
// it is a projection, never a stored field (§5.2).
func runDisplayName(item RunSummary) string {
	if item.DirRef != nil {
		if base := filepath.Base(*item.DirRef); base != "." && base != string(filepath.Separator) {
			return base
		}
	}
	if value, ok := item.Params["name"]; ok && strings.TrimSpace(value.Value) != "" {
		return strings.TrimSpace(value.Value)
	}
	keys := make([]string, 0, len(item.Params))
	for key := range item.Params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, 2)
	for _, key := range keys {
		parts = append(parts, key+"="+item.Params[key].Value)
		if len(parts) == 2 {
			break
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, ", ")
	}
	return "Run " + shortID(item.ID)
}

// shortID returns the first 8 characters of an id.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// normalizeParams validates a parameter map and its sources.
func normalizeParams(params map[string]ParamValue) (map[string]ParamValue, error) {
	out := make(map[string]ParamValue, len(params))
	for key, value := range params {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			return nil, fmt.Errorf("%w: empty parameter key", ErrInvalidInput)
		}
		if value.Source != nil {
			if strings.TrimSpace(value.Source.Path) == "" {
				return nil, fmt.Errorf("%w: parameter %q has a source without a path", ErrInvalidInput, trimmed)
			}
			if value.Source.Pointer != nil && strings.TrimSpace(*value.Source.Pointer) == "" {
				return nil, fmt.Errorf("%w: parameter %q has an empty source pointer", ErrInvalidInput, trimmed)
			}
		}
		out[trimmed] = value
	}
	return out, nil
}

// ListDirCandidates lists the first-level subdirectories of an experiment
// directory for the "create run from subdirectory" action (§5.2). It never
// creates a run and never writes (§3.9, O-14).
func (s *Service) ListDirCandidates(ctx context.Context, experimentID string) ([]DirCandidate, error) {
	root, rules, err := s.resolveTarget(ctx, TargetRef{Type: TargetExperiment, ID: experimentID}, nil)
	if err != nil {
		return nil, err
	}
	set, err := compileRuleSet(rules)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrDirUnavailable, root, err)
	}
	out := make([]DirCandidate, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		rel := name + "/"
		if forcedExcludedDir(name) {
			continue
		}
		if excluded, _ := set.excluded(rel, true); excluded {
			continue
		}
		count, err := countPreviewFiles(filepath.Join(root, name), set, dirCandidatePreviewLimit)
		if err != nil {
			count = 0
		}
		out = append(out, DirCandidate{Path: filepath.Join(root, name), FileCount: count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// countPreviewFiles counts regular files below dir, skipping excluded
// directories, and stops at limit.
func countPreviewFiles(dir string, rules *ruleSet, limit int) (int, error) {
	count := 0
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries in the preview
		}
		if path == dir {
			return nil
		}
		if entry.IsDir() {
			if forcedExcludedDir(entry.Name()) || isGitDir(entry) {
				return filepath.SkipDir
			}
			rel, relErr := filepath.Rel(filepath.Dir(dir), path)
			if relErr == nil {
				if excluded, _ := rules.excluded(rel, true); excluded {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if entry.Type().IsRegular() {
			count++
			if count >= limit {
				return filepath.SkipAll
			}
		}
		return nil
	})
	return count, err
}

// errorsIsNoRows is a tiny helper kept for readability at call sites.
func errorsIsNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
