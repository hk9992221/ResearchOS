package experiments

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"researchos/internal/foundation/ids"
)

// ListSnapshots reads the append-only history of one target (§3.7, §5.9),
// newest first. AI output state comes from SPEC-02 ai_outputs when available
// and is never written back.
func (s *Service) ListSnapshots(ctx context.Context, target TargetRef, f SnapshotFilter) ([]SnapshotSummary, error) {
	handle, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	return s.listSnapshots(ctx, handle.DB(), target, f)
}

// GetSnapshot returns one snapshot with its frozen evidence (§5.9).
func (s *Service) GetSnapshot(ctx context.Context, snapshotID string) (*Snapshot, error) {
	handle, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	return s.loadSnapshot(ctx, handle.DB(), snapshotID)
}

// LatestSnapshot returns the newest snapshot of one kind. A target without
// history yields ErrNotFound.
func (s *Service) LatestSnapshot(ctx context.Context, target TargetRef, kind AnalysisKind) (*Snapshot, error) {
	handle, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	if !target.Type.Valid() || !ids.Valid(target.ID) {
		return nil, fmt.Errorf("%w: invalid snapshot target", ErrInvalidInput)
	}
	if kind != "" && !kind.Valid() {
		return nil, fmt.Errorf("%w: invalid analysis kind %q", ErrInvalidInput, kind)
	}
	query := `SELECT ` + snapshotColumns + ` FROM snapshots WHERE target_type = ? AND target_id = ?`
	args := []any{string(target.Type), target.ID}
	if kind != "" {
		query += ` AND CASE WHEN json_valid(ai_analysis) THEN json_extract(ai_analysis, '$.kind') END = ?`
		args = append(args, string(kind))
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT 1`
	snap, err := scanSnapshotRow(handle.DB().QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("snapshot of %s %s", target.Type, target.ID)
	}
	if err != nil {
		return nil, fmt.Errorf("experiments: latest snapshot: %w", err)
	}
	return &snap, nil
}

// saveSnapshot persists one snapshot inside the caller transaction. It is the
// only creation path; the public API stays read-only (§3.7, §5.9). The analysis
// pipeline (SPEC-02 integration) calls it after semantic validation.
func (s *Service) saveSnapshot(ctx context.Context, tx *sql.Tx, snap *Snapshot) error {
	if tx == nil {
		return fmt.Errorf("%w: snapshot needs a transaction", ErrInvalidInput)
	}
	if snap == nil {
		return fmt.Errorf("%w: nil snapshot", ErrInvalidInput)
	}
	if !snap.Target.Type.Valid() || !ids.Valid(snap.Target.ID) {
		return fmt.Errorf("%w: invalid snapshot target", ErrInvalidInput)
	}
	if snap.AIAnalysis != nil {
		if err := snap.AIAnalysis.Validate(); err != nil {
			return err
		}
	}
	if snap.ID == "" {
		id, err := ids.New(s.clock)
		if err != nil {
			return fmt.Errorf("experiments: generate snapshot id: %w", err)
		}
		snap.ID = id
	} else if !ids.Valid(snap.ID) {
		return fmt.Errorf("%w: invalid snapshot id %q", ErrInvalidInput, snap.ID)
	}
	if snap.CreatedAt.IsZero() {
		snap.CreatedAt = s.clock.Now()
	}
	if snap.Metrics.V == 0 {
		snap.Metrics = MetricSet{V: payloadVersion, Metrics: []Metric{}, Unparsed: []UnparsedFile{}}
	}
	if snap.ModelInfo.V == 0 {
		snap.ModelInfo = ModelInfo{V: payloadVersion}
	}
	if snap.GitInfo.V == 0 {
		snap.GitInfo.V = payloadVersion
	}
	return s.insertSnapshot(ctx, tx, *snap)
}

// detectDrift re-checks the frozen manifest after analysis and reports files
// whose size or mtime changed (§5.6 step 8, AC-16). It never rewrites hashes.
func detectDrift(ctx context.Context, root string, manifest []FileEntry) []string {
	warnings := []string{}
	for _, entry := range manifest {
		if err := ctx.Err(); err != nil {
			warnings = append(warnings, "analysis_cancelled")
			return warnings
		}
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(entry.Path)))
		switch {
		case err != nil:
			warnings = append(warnings, "file_disappeared_during_analysis:"+entry.Path)
		case info.Size() != entry.Size || formatTime(info.ModTime()) != entry.MTime:
			warnings = append(warnings, "files_changed_during_analysis:"+entry.Path)
		}
	}
	return warnings
}
