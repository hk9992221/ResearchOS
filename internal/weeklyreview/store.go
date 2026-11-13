package weeklyreview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"researchos/internal/foundation/clock"
)

const reportColumns = `id, period_start, period_end, content_md, status, meta, confirmed_at,
	ai_output_id, created_at, updated_at, deleted_at`

type rowScanner interface{ Scan(dest ...any) error }

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// scanReport loads one row in the caller's timezone. The location is needed to
// rebuild the Period from the stored calendar dates.
func scanReport(row rowScanner, loc *time.Location) (*WeeklyReport, error) {
	var (
		rep         WeeklyReport
		start, end  string
		status      string
		metaRaw     string
		confirmedAt sql.NullString
		aiOutputID  sql.NullString
		createdAt   string
		updatedAt   string
		deletedAt   sql.NullString
	)
	if err := row.Scan(&rep.ID, &start, &end, &rep.ContentMD, &status, &metaRaw,
		&confirmedAt, &aiOutputID, &createdAt, &updatedAt, &deletedAt); err != nil {
		return nil, err
	}
	period, err := periodFromDates(start, end, loc)
	if err != nil {
		return nil, err
	}
	rep.Period = period
	rep.Status = Status(status)
	if metaRaw != "" && metaRaw != "null" {
		if err := json.Unmarshal([]byte(metaRaw), &rep.Meta); err != nil {
			return nil, fmt.Errorf("weeklyreview: report %s meta: %w", rep.ID, err)
		}
	}
	if aiOutputID.Valid {
		if rep.Meta.Generation == nil {
			rep.Meta.Generation = &GenerationMeta{Mode: "ai", AIStatus: "ok"}
		}
		if rep.Meta.Generation.AIOutputID == "" {
			rep.Meta.Generation.AIOutputID = aiOutputID.String
		}
	}
	rep.ContentHash = contentHash(rep.ContentMD)
	if rep.CreatedAt, err = clock.Parse(createdAt); err != nil {
		return nil, fmt.Errorf("weeklyreview: report %s created_at: %w", rep.ID, err)
	}
	if rep.UpdatedAt, err = clock.Parse(updatedAt); err != nil {
		return nil, fmt.Errorf("weeklyreview: report %s updated_at: %w", rep.ID, err)
	}
	if confirmedAt.Valid {
		t, err := clock.Parse(confirmedAt.String)
		if err != nil {
			return nil, fmt.Errorf("weeklyreview: report %s confirmed_at: %w", rep.ID, err)
		}
		rep.ConfirmedAt = &t
	}
	if deletedAt.Valid {
		t, err := clock.Parse(deletedAt.String)
		if err != nil {
			return nil, fmt.Errorf("weeklyreview: report %s deleted_at: %w", rep.ID, err)
		}
		rep.DeletedAt = &t
	}
	return &rep, nil
}

func (s *service) reportByID(ctx context.Context, q execer, id string, allowDeleted bool) (*WeeklyReport, error) {
	cfg := s.loadSettings(ctx)
	query := `SELECT ` + reportColumns + ` FROM weekly_reports WHERE id = ?`
	if !allowDeleted {
		query += ` AND deleted_at IS NULL`
	}
	rep, err := scanReport(q.QueryRowContext(ctx, query, id), cfg.Location)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, serviceError(ErrNotFound, "周报 %s 不存在", id)
	}
	if err != nil {
		return nil, fmt.Errorf("weeklyreview: get report: %w", err)
	}
	if info, err := s.projectInfo(ctx); err == nil {
		rep.Title = s.defaultTitle(info, rep.Period)
	}
	return rep, nil
}

func (s *service) reportTx(ctx context.Context, tx *sql.Tx, id string, allowDeleted bool) (*WeeklyReport, error) {
	cfg := s.loadSettings(ctx)
	query := `SELECT ` + reportColumns + ` FROM weekly_reports WHERE id = ?`
	if !allowDeleted {
		query += ` AND deleted_at IS NULL`
	}
	rep, err := scanReport(tx.QueryRowContext(ctx, query, id), cfg.Location)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, serviceError(ErrNotFound, "周报 %s 不存在", id)
	}
	if err != nil {
		return nil, fmt.Errorf("weeklyreview: get report: %w", err)
	}
	return rep, nil
}

func encodeMeta(meta ReportMeta) (string, error) {
	raw, err := json.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("weeklyreview: encode meta: %w", err)
	}
	return string(raw), nil
}

// listReports implements SPEC-09 §5.3 ListReports.
func (s *service) listReports(ctx context.Context, in ListReportsInput) (ReportPage, error) {
	cfg := s.loadSettings(ctx)
	page := in.Page
	if page <= 0 {
		page = 1
	}
	size := in.PageSize
	if size <= 0 {
		size = defaultPageSize
	}
	if size > maxPageSize {
		size = maxPageSize
	}

	h, err := s.handle(ctx)
	if err != nil {
		return ReportPage{}, err
	}

	var (
		where []string
		args  []any
	)
	if !in.IncludeDeleted {
		where = append(where, "deleted_at IS NULL")
	}
	if len(in.Status) > 0 {
		ph := make([]string, len(in.Status))
		for i, st := range in.Status {
			if !st.Valid() {
				return ReportPage{}, serviceError(ErrInvalidInput, "无效状态 %q", string(st))
			}
			ph[i] = "?"
			args = append(args, string(st))
		}
		where = append(where, "status IN ("+strings.Join(ph, ",")+")")
	}
	if in.From != nil {
		where = append(where, "period_start >= ?")
		args = append(args, in.From.In(cfg.Location).Format(dateLayout))
	}
	if in.To != nil {
		where = append(where, "period_start <= ?")
		args = append(args, in.To.In(cfg.Location).Format(dateLayout))
	}
	order := "period_start DESC, created_at DESC, id DESC"
	switch in.Order {
	case "", "-period_start":
	case "period_start":
		order = "period_start ASC, created_at ASC, id ASC"
	case "updated_at":
		order = "updated_at DESC, id DESC"
	default:
		return ReportPage{}, serviceError(ErrInvalidInput, "无效排序 %q", in.Order)
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	db := h.DB()
	var total int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM weekly_reports`+clause, args...).Scan(&total); err != nil {
		return ReportPage{}, fmt.Errorf("weeklyreview: count reports: %w", err)
	}
	query := `SELECT ` + reportColumns + ` FROM weekly_reports` + clause +
		` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
	pageArgs := append(append([]any{}, args...), size, (page-1)*size)
	rows, err := db.QueryContext(ctx, query, pageArgs...)
	if err != nil {
		return ReportPage{}, fmt.Errorf("weeklyreview: list reports: %w", err)
	}
	defer rows.Close()

	info, _ := s.projectInfo(ctx)
	out := ReportPage{Page: page, PageSize: size, Total: total}
	for rows.Next() {
		rep, err := scanReport(rows, cfg.Location)
		if err != nil {
			return ReportPage{}, fmt.Errorf("weeklyreview: list reports: %w", err)
		}
		rep.Title = s.defaultTitle(info, rep.Period)
		out.Items = append(out.Items, ReportSummary{
			ID:          rep.ID,
			Title:       rep.Title,
			Period:      rep.Period,
			Status:      rep.Status,
			UpdatedAt:   rep.UpdatedAt,
			ConfirmedAt: rep.ConfirmedAt,
			DeletedAt:   rep.DeletedAt,
			Excerpt:     excerpt(rep.ContentMD),
		})
	}
	if err := rows.Err(); err != nil {
		return ReportPage{}, fmt.Errorf("weeklyreview: list reports: %w", err)
	}
	return out, nil
}

// excerpt is the first non-empty, non-heading content line, capped at 120 runes.
func excerpt(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return truncateRunes(line, 120)
	}
	return ""
}

func (s *service) getReport(ctx context.Context, id string) (*WeeklyReport, error) {
	h, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	return s.reportByID(ctx, h.DB(), id, false)
}

// updateReport writes content/meta/status columns in the caller transaction.
func updateReport(ctx context.Context, tx *sql.Tx, rep *WeeklyReport) error {
	metaRaw, err := encodeMeta(rep.Meta)
	if err != nil {
		return err
	}
	var confirmedAt any
	if rep.ConfirmedAt != nil {
		confirmedAt = clock.Format(*rep.ConfirmedAt)
	}
	aiOutputID := ""
	if rep.Meta.Generation != nil {
		aiOutputID = rep.Meta.Generation.AIOutputID
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE weekly_reports
		    SET content_md = ?, status = ?, meta = ?, confirmed_at = ?, ai_output_id = ?,
		        updated_at = ?, deleted_at = ?
		  WHERE id = ?`,
		rep.ContentMD, string(rep.Status), metaRaw, confirmedAt, nullableString(aiOutputID),
		clock.Format(rep.UpdatedAt), nullableTime(rep.DeletedAt), rep.ID)
	if err != nil {
		return fmt.Errorf("weeklyreview: update report: %w", err)
	}
	rep.ContentHash = contentHash(rep.ContentMD)
	return nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return clock.Format(*t)
}
