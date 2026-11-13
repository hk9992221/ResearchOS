package experiments

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
)

// queryer is the shared query surface of *sql.DB and *sql.Tx.
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// rowScanner is the scan surface shared by *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

const experimentColumns = `id, title, purpose, dir_ref, scan_rules, created_at, updated_at, deleted_at`

const runColumns = `id, experiment_id, dir_ref, params, git_info, status, created_at, updated_at, deleted_at`

const snapshotColumns = `id, target_type, target_id, created_at, file_manifest, git_info, metrics, ai_analysis, model_info`

// notFound wraps a missing row into the stable sentinel.
func notFound(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrNotFound, fmt.Sprintf(format, args...))
}

// loadExperiment reads one experiment row.
func (s *Service) loadExperiment(ctx context.Context, q queryer, id string, includeDeleted bool) (Experiment, error) {
	if !ids.Valid(id) {
		return Experiment{}, notFound("experiment %q", id)
	}
	query := `SELECT ` + experimentColumns + ` FROM experiments WHERE id = ?`
	if !includeDeleted {
		query += ` AND deleted_at IS NULL`
	}
	row, err := scanExperimentRow(q.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Experiment{}, notFound("experiment %s", id)
	}
	if err != nil {
		return Experiment{}, fmt.Errorf("experiments: load experiment %s: %w", id, err)
	}
	return row, nil
}

// loadRun reads one run row.
func (s *Service) loadRun(ctx context.Context, q queryer, id string, includeDeleted bool) (Run, error) {
	if !ids.Valid(id) {
		return Run{}, notFound("run %q", id)
	}
	query := `SELECT ` + runColumns + ` FROM runs WHERE id = ?`
	if !includeDeleted {
		query += ` AND deleted_at IS NULL`
	}
	row, err := scanRunRow(q.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, notFound("run %s", id)
	}
	if err != nil {
		return Run{}, fmt.Errorf("experiments: load run %s: %w", id, err)
	}
	return row, nil
}

// scanExperimentRow decodes one experiments row.
func scanExperimentRow(row rowScanner) (Experiment, error) {
	var (
		exp       Experiment
		dirRef    sql.NullString
		rawRules  string
		createdAt string
		updatedAt string
		deletedAt sql.NullString
	)
	if err := row.Scan(&exp.ID, &exp.Title, &exp.Purpose, &dirRef, &rawRules, &createdAt, &updatedAt, &deletedAt); err != nil {
		return Experiment{}, err
	}
	rules, err := decodeScanRules([]byte(rawRules))
	if err != nil {
		// A damaged scan_rules column must not hide the experiment: fall back to
		// the defaults, the same tolerance §E01 requires for damaged JSON.
		rules = DefaultScanRules()
	}
	exp.ScanRules = rules
	if dirRef.Valid {
		exp.DirRef = &dirRef.String
	}
	exp.CreatedAt, err = clock.Parse(createdAt)
	if err != nil {
		return Experiment{}, fmt.Errorf("created_at: %w", err)
	}
	exp.UpdatedAt, err = clock.Parse(updatedAt)
	if err != nil {
		return Experiment{}, fmt.Errorf("updated_at: %w", err)
	}
	if deletedAt.Valid {
		at, err := clock.Parse(deletedAt.String)
		if err != nil {
			return Experiment{}, fmt.Errorf("deleted_at: %w", err)
		}
		exp.DeletedAt = &at
	}
	return exp, nil
}

// scanRunRow decodes one runs row.
func scanRunRow(row rowScanner) (Run, error) {
	var (
		run       Run
		dirRef    sql.NullString
		rawParams string
		rawGit    string
		status    string
		createdAt string
		updatedAt string
		deletedAt sql.NullString
	)
	if err := row.Scan(&run.ID, &run.ExperimentID, &dirRef, &rawParams, &rawGit, &status, &createdAt, &updatedAt, &deletedAt); err != nil {
		return Run{}, err
	}
	if dirRef.Valid {
		run.DirRef = &dirRef.String
	}
	run.Params = decodeParams([]byte(rawParams))
	run.GitInfo = decodeGitInfo([]byte(rawGit))
	run.Status = RunStatus(status)
	if !run.Status.Valid() {
		run.Status = RunUnknown
	}
	var err error
	run.CreatedAt, err = clock.Parse(createdAt)
	if err != nil {
		return Run{}, fmt.Errorf("created_at: %w", err)
	}
	run.UpdatedAt, err = clock.Parse(updatedAt)
	if err != nil {
		return Run{}, fmt.Errorf("updated_at: %w", err)
	}
	if deletedAt.Valid {
		at, err := clock.Parse(deletedAt.String)
		if err != nil {
			return Run{}, fmt.Errorf("deleted_at: %w", err)
		}
		run.DeletedAt = &at
	}
	return run, nil
}

// insertExperiment stores one experiment row.
func (s *Service) insertExperiment(ctx context.Context, tx *sql.Tx, exp Experiment) error {
	rules, err := encodeJSON(exp.ScanRules)
	if err != nil {
		return fmt.Errorf("experiments: encode scan rules: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO experiments(id, title, purpose, dir_ref, scan_rules, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		exp.ID, exp.Title, exp.Purpose, nullableString(exp.DirRef), string(rules),
		formatTime(exp.CreatedAt), formatTime(exp.UpdatedAt))
	if err != nil {
		return fmt.Errorf("experiments: insert experiment: %w", err)
	}
	return nil
}

// updateExperiment writes the non-nil patch fields.
func (s *Service) updateExperiment(ctx context.Context, tx *sql.Tx, id string, title, purpose *string) error {
	sets := []string{"updated_at = ?"}
	args := []any{formatTime(s.clock.Now())}
	if title != nil {
		sets = append(sets, "title = ?")
		args = append(args, *title)
	}
	if purpose != nil {
		sets = append(sets, "purpose = ?")
		args = append(args, *purpose)
	}
	args = append(args, id)
	res, err := tx.ExecContext(ctx, `UPDATE experiments SET `+strings.Join(sets, ", ")+` WHERE id = ? AND deleted_at IS NULL`, args...)
	if err != nil {
		return fmt.Errorf("experiments: update experiment: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return notFound("experiment %s", id)
	}
	return nil
}

// writeScanRules stores the complete rule set, preserving unknown top-level
// keys of the previous payload (§4.3 forward compatibility).
func (s *Service) writeScanRules(ctx context.Context, tx *sql.Tx, id string, rules ScanRules) error {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT scan_rules FROM experiments WHERE id = ? AND deleted_at IS NULL`, id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("experiment %s", id)
		}
		return fmt.Errorf("experiments: read scan rules: %w", err)
	}
	encoded, err := mergeUnknownKeys([]byte(raw), rules, knownScanRuleKeys)
	if err != nil {
		return fmt.Errorf("experiments: encode scan rules: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE experiments SET scan_rules = ?, updated_at = ? WHERE id = ?`,
		string(encoded), formatTime(s.clock.Now()), id); err != nil {
		return fmt.Errorf("experiments: write scan rules: %w", err)
	}
	return nil
}

// knownScanRuleKeys are the ScanRules fields; everything else is carried over.
var knownScanRuleKeys = []string{
	"v", "include", "exclude", "max_file_bytes", "max_total_bytes",
	"max_files", "hash_max_bytes", "follow_symlinks", "include_images_in_ai",
}

// mergeUnknownKeys re-encodes typed next to the unknown keys of existing.
func mergeUnknownKeys(existing []byte, next any, known []string) ([]byte, error) {
	encoded, err := encodeJSON(next)
	if err != nil {
		return nil, err
	}
	old, err := decodeJSONObject(existing)
	if err != nil || len(old) == 0 {
		return encoded, nil
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &merged); err != nil {
		return nil, err
	}
	knownSet := make(map[string]struct{}, len(known))
	for _, key := range known {
		knownSet[key] = struct{}{}
	}
	extra := false
	for key, value := range old {
		if _, ok := knownSet[key]; ok {
			continue
		}
		merged[key] = value
		extra = true
	}
	if !extra {
		return encoded, nil
	}
	return encodeJSON(merged)
}

// insertRun stores one run row.
func (s *Service) insertRun(ctx context.Context, tx *sql.Tx, run Run) error {
	params, err := encodeJSON(run.Params)
	if err != nil {
		return fmt.Errorf("experiments: encode params: %w", err)
	}
	gitInfo, err := encodeJSON(run.GitInfo)
	if err != nil {
		return fmt.Errorf("experiments: encode git info: %w", err)
	}
	if run.Status == "" {
		run.Status = RunUnknown
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO runs(id, experiment_id, dir_ref, params, git_info, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.ExperimentID, nullableString(run.DirRef), string(params), string(gitInfo),
		string(run.Status), formatTime(run.CreatedAt), formatTime(run.UpdatedAt))
	if err != nil {
		return fmt.Errorf("experiments: insert run: %w", err)
	}
	return nil
}

// updateRunParams replaces the whole parameter map.
func (s *Service) updateRunParams(ctx context.Context, tx *sql.Tx, id string, params map[string]ParamValue) error {
	if params == nil {
		params = map[string]ParamValue{}
	}
	encoded, err := encodeJSON(params)
	if err != nil {
		return fmt.Errorf("experiments: encode params: %w", err)
	}
	res, err := tx.ExecContext(ctx, `UPDATE runs SET params = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		string(encoded), formatTime(s.clock.Now()), id)
	if err != nil {
		return fmt.Errorf("experiments: update params: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return notFound("run %s", id)
	}
	return nil
}

// setRunStatus writes runs.status. The same value is a no-op (§5.2).
func (s *Service) setRunStatus(ctx context.Context, tx *sql.Tx, id string, status RunStatus) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE runs SET status = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL AND status <> ?`,
		string(status), formatTime(s.clock.Now()), id, string(status))
	if err != nil {
		return fmt.Errorf("experiments: set run status: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		var current string
		err := tx.QueryRowContext(ctx, `SELECT status FROM runs WHERE id = ? AND deleted_at IS NULL`, id).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("run %s", id)
		}
		if err != nil {
			return fmt.Errorf("experiments: read run status: %w", err)
		}
		return nil
	}
	return nil
}

// setRunDirRef writes runs.dir_ref after externalrefs.Register.
func (s *Service) setRunDirRef(ctx context.Context, tx *sql.Tx, id, absPath string) error {
	res, err := tx.ExecContext(ctx, `UPDATE runs SET dir_ref = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		absPath, formatTime(s.clock.Now()), id)
	if err != nil {
		return fmt.Errorf("experiments: set run directory: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return notFound("run %s", id)
	}
	return nil
}

// setExperimentDirRef writes experiments.dir_ref after externalrefs.Register.
func (s *Service) setExperimentDirRef(ctx context.Context, tx *sql.Tx, id, absPath string) error {
	res, err := tx.ExecContext(ctx, `UPDATE experiments SET dir_ref = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		absPath, formatTime(s.clock.Now()), id)
	if err != nil {
		return fmt.Errorf("experiments: set experiment directory: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return notFound("experiment %s", id)
	}
	return nil
}

// insertSnapshot is the append-only snapshot write of E08. It is called only by
// the analysis pipeline (and by tests); no public API creates snapshots (§3.7).
func (s *Service) insertSnapshot(ctx context.Context, tx *sql.Tx, snap Snapshot) error {
	manifest, err := encodeJSON(nonNilEntries(snap.FileManifest))
	if err != nil {
		return fmt.Errorf("experiments: encode manifest: %w", err)
	}
	gitInfo, err := encodeJSON(snap.GitInfo)
	if err != nil {
		return fmt.Errorf("experiments: encode git info: %w", err)
	}
	metrics, err := encodeJSON(snap.Metrics)
	if err != nil {
		return fmt.Errorf("experiments: encode metrics: %w", err)
	}
	modelInfo, err := encodeJSON(snap.ModelInfo)
	if err != nil {
		return fmt.Errorf("experiments: encode model info: %w", err)
	}
	var analysis any
	aiOutputID := any(nil)
	if snap.AIAnalysis != nil {
		encoded, err := encodeJSON(snap.AIAnalysis)
		if err != nil {
			return fmt.Errorf("experiments: encode ai analysis: %w", err)
		}
		analysis = string(encoded)
		if snap.AIAnalysis.AIOutputID != "" {
			aiOutputID = snap.AIAnalysis.AIOutputID
		}
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO snapshots(id, target_type, target_id, file_manifest, git_info, metrics, ai_analysis, ai_output_id, model_info, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		snap.ID, string(snap.Target.Type), snap.Target.ID, string(manifest), string(gitInfo),
		string(metrics), analysis, aiOutputID, string(modelInfo), formatTime(snap.CreatedAt))
	if err != nil {
		return fmt.Errorf("experiments: insert snapshot: %w", err)
	}
	return nil
}

// loadSnapshot reads one snapshot by id.
func (s *Service) loadSnapshot(ctx context.Context, q queryer, id string) (*Snapshot, error) {
	if !ids.Valid(id) {
		return nil, notFound("snapshot %q", id)
	}
	row := q.QueryRowContext(ctx, `SELECT `+snapshotColumns+` FROM snapshots WHERE id = ?`, id)
	snap, err := scanSnapshotRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("snapshot %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("experiments: load snapshot %s: %w", id, err)
	}
	return &snap, nil
}

// scanSnapshotRow decodes one snapshots row.
func scanSnapshotRow(row rowScanner) (Snapshot, error) {
	var (
		snap       Snapshot
		targetType string
		createdAt  string
		manifest   string
		gitInfo    string
		metrics    string
		analysis   sql.NullString
		modelInfo  string
	)
	if err := row.Scan(&snap.ID, &targetType, &snap.Target.ID, &createdAt,
		&manifest, &gitInfo, &metrics, &analysis, &modelInfo); err != nil {
		return Snapshot{}, err
	}
	snap.Target.Type = TargetType(targetType)
	snap.FileManifest = decodeFileManifest([]byte(manifest))
	snap.GitInfo = decodeGitInfo([]byte(gitInfo))
	snap.Metrics = decodeMetricSet([]byte(metrics))
	snap.ModelInfo = decodeModelInfo([]byte(modelInfo))
	if analysis.Valid {
		snap.AIAnalysis = decodeAIAnalysis([]byte(analysis.String))
	}
	at, err := clock.Parse(createdAt)
	if err != nil {
		return Snapshot{}, fmt.Errorf("created_at: %w", err)
	}
	snap.CreatedAt = at
	return snap, nil
}

// listSnapshots reads history for one target, newest first.
func (s *Service) listSnapshots(ctx context.Context, q queryer, target TargetRef, f SnapshotFilter) ([]SnapshotSummary, error) {
	if !target.Type.Valid() || !ids.Valid(target.ID) {
		return nil, fmt.Errorf("%w: invalid snapshot target", ErrInvalidInput)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	query := `SELECT ` + snapshotColumns + ` FROM snapshots WHERE target_type = ? AND target_id = ?`
	args := []any{string(target.Type), target.ID}
	if f.Kind != "" {
		if !f.Kind.Valid() {
			return nil, fmt.Errorf("%w: invalid analysis kind %q", ErrInvalidInput, f.Kind)
		}
		// kind lives in the ai_analysis envelope: json_extract works on stored
		// JSON without an extra column (§4.4).
		query += ` AND CASE WHEN json_valid(ai_analysis) THEN json_extract(ai_analysis, '$.kind') END = ?`
		args = append(args, string(f.Kind))
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, max(f.Offset, 0))

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("experiments: list snapshots: %w", err)
	}
	snapshots := make([]Snapshot, 0, limit)
	for rows.Next() {
		snap, err := scanSnapshotRow(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("experiments: list snapshots: %w", err)
		}
		snapshots = append(snapshots, snap)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("experiments: list snapshots: %w", err)
	}
	rows.Close()
	out := make([]SnapshotSummary, 0, len(snapshots))
	for _, snap := range snapshots {
		out = append(out, s.snapshotSummary(ctx, q, snap))
	}
	return out, nil
}

// snapshotSummary folds one snapshot row into its history projection. AI output
// status is read from SPEC-02 ai_outputs when that table exists (O-18) and is
// never written back to the snapshot.
func (s *Service) snapshotSummary(ctx context.Context, q queryer, snap Snapshot) SnapshotSummary {
	summary := SnapshotSummary{
		ID:           snap.ID,
		Target:       snap.Target,
		CreatedAt:    snap.CreatedAt,
		ProviderName: snap.ModelInfo.ProviderName,
		ModelID:      snap.ModelInfo.ModelID,
		GitCommit:    snap.GitInfo.CommitShort,
		MetricCount:  len(snap.Metrics.Metrics),
	}
	if snap.AIAnalysis != nil {
		summary.Kind = snap.AIAnalysis.Kind
		summary.Warnings = len(snap.AIAnalysis.Warnings)
		summary.AIStatus, summary.HumanEdited, summary.ConfirmedAt = s.aiOutputState(ctx, q, snap.AIAnalysis.AIOutputID)
	}
	return summary
}

// aiOutputState reads the SPEC-02 output state when available.
func (s *Service) aiOutputState(ctx context.Context, q queryer, aiOutputID string) (string, bool, *time.Time) {
	if aiOutputID == "" || !s.tableExists(ctx, q, "ai_outputs") {
		return "", false, nil
	}
	var (
		status      string
		humanEdited int
		confirmedAt sql.NullString
	)
	err := q.QueryRowContext(ctx,
		`SELECT status, human_edited, confirmed_at FROM ai_outputs WHERE id = ?`, aiOutputID).
		Scan(&status, &humanEdited, &confirmedAt)
	if err != nil {
		return "", false, nil
	}
	var confirmed *time.Time
	if confirmedAt.Valid {
		if at, err := clock.Parse(confirmedAt.String); err == nil {
			confirmed = &at
		}
	}
	return status, humanEdited != 0, confirmed
}

// tableExists reports whether the project database carries table.
func (s *Service) tableExists(ctx context.Context, q queryer, table string) bool {
	var name string
	err := q.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
	return err == nil
}

// nullableString maps an optional path to a database value.
func nullableString(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

// nonNilEntries returns an empty slice instead of nil for JSON encoding.
func nonNilEntries(entries []FileEntry) []FileEntry {
	if entries == nil {
		return []FileEntry{}
	}
	return entries
}

// ownerRef validates and returns one external reference owner.
func ownerRef(t TargetType, id string) (events.EntityType, error) {
	if !ids.Valid(id) {
		return "", fmt.Errorf("%w: invalid id %q", ErrInvalidInput, id)
	}
	switch t {
	case TargetExperiment:
		return schema.EntityExperiment, nil
	case TargetRun:
		return schema.EntityRun, nil
	}
	return "", fmt.Errorf("%w: invalid target type %q", ErrInvalidInput, t)
}
