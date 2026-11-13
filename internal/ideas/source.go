package ideas

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// querier is the read surface shared by *sql.DB and *sql.Tx.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// targetInfo is the resolved projection of one relation/evidence target.
type targetInfo struct {
	Type    string
	ID      string
	Title   string
	Deleted bool
	Broken  bool
}

// paperTitle reads a paper title from global.db.papers.metadata.
func paperTitle(ctx context.Context, global querier, paperID string) (string, bool, error) {
	var title string
	err := global.QueryRowContext(ctx,
		`SELECT COALESCE(json_extract(metadata, '$.title'), '') FROM papers WHERE id = ?`,
		paperID).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("ideas: read paper %s: %w", paperID, err)
	}
	return title, true, nil
}

// resolveTarget reads one target through q. It never fails on a missing target;
// Broken reports it. Project-local types are read through q so the caller's
// transaction sees its own writes.
func (s *service) resolveTarget(ctx context.Context, q querier, typ, id string) (targetInfo, error) {
	info := targetInfo{Type: typ, ID: id}
	switch typ {
	case TargetPaper:
		if s.ws == nil || s.ws.Global() == nil {
			return info, errors.New("ideas: service has no global database")
		}
		title, ok, err := paperTitle(ctx, s.ws.Global().DB(), id)
		if err != nil {
			return info, err
		}
		if !ok {
			info.Broken = true
			return info, nil
		}
		var linked int
		if err := q.QueryRowContext(ctx,
			`SELECT count(*) FROM project_papers WHERE project_id = ? AND paper_id = ?`,
			s.projectID, id).Scan(&linked); err != nil {
			return info, fmt.Errorf("ideas: read project paper %s: %w", id, err)
		}
		info.Title = title
		info.Broken = linked == 0
		return info, nil
	case string(SourceNote):
		var (
			title   string
			deleted sql.NullString
		)
		err := q.QueryRowContext(ctx, `SELECT title, deleted_at FROM notes WHERE id = ?`, id).
			Scan(&title, &deleted)
		if errors.Is(err, sql.ErrNoRows) {
			info.Broken = true
			return info, nil
		}
		if err != nil {
			return info, fmt.Errorf("ideas: read note %s: %w", id, err)
		}
		info.Title = title
		info.Deleted = deleted.Valid
		return info, nil
	case string(SourceExperiment):
		var (
			title   string
			deleted sql.NullString
		)
		err := q.QueryRowContext(ctx, `SELECT title, deleted_at FROM experiments WHERE id = ?`, id).
			Scan(&title, &deleted)
		if errors.Is(err, sql.ErrNoRows) {
			info.Broken = true
			return info, nil
		}
		if err != nil {
			return info, fmt.Errorf("ideas: read experiment %s: %w", id, err)
		}
		info.Title = title
		info.Deleted = deleted.Valid
		return info, nil
	case string(SourceRun):
		var (
			expTitle     string
			runDeleted   sql.NullString
			expDeleted   sql.NullString
			experimentID string
		)
		err := q.QueryRowContext(ctx,
			`SELECT r.experiment_id, e.title, r.deleted_at, e.deleted_at
			 FROM runs r JOIN experiments e ON e.id = r.experiment_id WHERE r.id = ?`, id).
			Scan(&experimentID, &expTitle, &runDeleted, &expDeleted)
		if errors.Is(err, sql.ErrNoRows) {
			info.Broken = true
			return info, nil
		}
		if err != nil {
			return info, fmt.Errorf("ideas: read run %s: %w", id, err)
		}
		info.Title = runTitle(expTitle, id)
		info.Deleted = runDeleted.Valid || expDeleted.Valid
		return info, nil
	case "idea":
		var (
			title   string
			deleted sql.NullString
		)
		err := q.QueryRowContext(ctx, `SELECT title, deleted_at FROM ideas WHERE id = ?`, id).Scan(&title, &deleted)
		if errors.Is(err, sql.ErrNoRows) {
			info.Broken = true
			return info, nil
		}
		if err != nil {
			return info, fmt.Errorf("ideas: read idea %s: %w", id, err)
		}
		info.Title = title
		info.Deleted = deleted.Valid
		return info, nil
	default:
		return info, serviceError(ErrInvalidSource, "source type %q is not supported", typ)
	}
}

// resolvedSourceTitle returns the display title for an event summary.
func (s *service) resolvedSourceTitle(ctx context.Context, q querier, ref SourceRef) string {
	info, err := s.resolveTarget(ctx, q, string(ref.Type), ref.ID)
	if err != nil {
		return ""
	}
	return info.Title
}

// validateEvidenceSource requires the target to exist, be live and, for a
// paper, be linked to the current project (SPEC-06 §4.3.2).
func (s *service) validateEvidenceSource(ctx context.Context, q querier, ref SourceRef) (string, error) {
	if !ref.Type.Valid() {
		return "", serviceError(ErrInvalidSource, "source type %q is not registered", string(ref.Type))
	}
	info, err := s.resolveTarget(ctx, q, string(ref.Type), ref.ID)
	if err != nil {
		return "", err
	}
	switch {
	case info.Broken:
		return "", serviceError(ErrInvalidSource, "source %s %s does not exist or is not linked to the project",
			ref.Type, ref.ID)
	case info.Deleted:
		return "", serviceError(ErrInvalidSource, "source %s %s is deleted", ref.Type, ref.ID)
	default:
		return info.Title, nil
	}
}

// runTitle renders a run's display title: the experiment title plus the run
// id prefix (runs carry no title column).
func runTitle(experimentTitle, runID string) string {
	short := runID
	if len(short) > 8 {
		short = short[:8]
	}
	if strings.TrimSpace(experimentTitle) == "" {
		return "Run " + short
	}
	return experimentTitle + " · Run " + short
}

// relationLabel is the Chinese label used in relation event summaries.
func relationLabel(typ string) string {
	switch typ {
	case TargetPaper:
		return "论文"
	case TargetNote:
		return "笔记"
	case TargetExperiment:
		return "实验"
	case string(SourceRun):
		return "Run"
	default:
		return typ
	}
}
