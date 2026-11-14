package weeklyreview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"researchos/internal/foundation/events"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/trash"
)

// CreateReport creates one draft report (SPEC-09 §5.3).
func (s *service) CreateReport(ctx context.Context, in CreateReportInput) (*WeeklyReport, error) {
	actor, err := writeActor(in.Actor)
	if err != nil {
		return nil, err
	}
	cfg := s.loadSettings(ctx)
	if err := validatePeriod(in.Period, cfg.Location); err != nil {
		return nil, err
	}
	if err := s.requireWritable(ctx); err != nil {
		return nil, err
	}
	h, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	info, err := s.projectInfo(ctx)
	if err != nil {
		return nil, err
	}
	id, err := s.newID()
	if err != nil {
		return nil, err
	}
	start, end := in.Period.StartDate(), in.Period.EndDate()

	content := ""
	if in.Skeleton {
		content = BuildSkeleton(info.Name, in.Period, nil)
	}
	meta := ReportMeta{
		Generation: &GenerationMeta{
			Mode:            "manual",
			AIStatus:        "skipped",
			TemplateID:      defaultReportTmpl,
			TemplateVersion: reportTemplateVer,
		},
	}
	metaRaw, err := encodeMeta(meta)
	if err != nil {
		return nil, err
	}
	now := s.now()

	err = h.InTx(ctx, func(tx *sql.Tx) error {
		if !in.Force {
			var existing string
			err := tx.QueryRowContext(ctx,
				`SELECT id FROM weekly_reports
				  WHERE period_start = ? AND period_end = ? AND deleted_at IS NULL
				  ORDER BY created_at, id LIMIT 1`, start, end).Scan(&existing)
			if err == nil {
				return serviceError(ErrPeriodOccupied, "该周期已存在周报 %s", existing).
					withDetails(map[string]any{"existing_id": existing})
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("weeklyreview: period check: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO weekly_reports(id, period_start, period_end, content_md, status, meta,
			        confirmed_at, ai_output_id, created_at, updated_at, deleted_at)
			 VALUES (?, ?, ?, ?, 'draft', ?, NULL, NULL, ?, ?, NULL)`,
			id, start, end, content, metaRaw, now, now); err != nil {
			return fmt.Errorf("weeklyreview: create report: %w", err)
		}
		return h.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventEntityCreated,
			EntityType: schema.EntityWeeklyReport,
			EntityID:   id,
			Summary:    fmt.Sprintf("创建周报 %s–%s", start, end),
			Actor:      actor,
			At:         s.clock.Now(),
		})
	})
	if err != nil {
		return nil, err
	}
	return s.reportByID(ctx, h.DB(), id, false)
}

// GetReport returns one live report.
func (s *service) GetReport(ctx context.Context, id string) (*WeeklyReport, error) {
	return s.getReport(ctx, id)
}

// ListReports lists report summaries.
func (s *service) ListReports(ctx context.Context, in ListReportsInput) (ReportPage, error) {
	return s.listReports(ctx, in)
}

// DeleteReport soft-deletes a report; trash writes entity.deleted.
func (s *service) DeleteReport(ctx context.Context, id string, actor Actor) error {
	a, err := writeActor(actor)
	if err != nil {
		return err
	}
	if err := s.requireWritable(ctx); err != nil {
		return err
	}
	if s.trash == nil {
		return serviceError(ErrInternal, "回收站服务未配置")
	}
	h, err := s.handle(ctx)
	if err != nil {
		return err
	}
	return h.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := s.reportTx(ctx, tx, id, true); err != nil {
			return err
		}
		return s.trash.SoftDelete(ctx, tx, trash.Ref{Type: schema.EntityWeeklyReport, ID: id}, a)
	})
}

// RestoreReport restores a soft-deleted report; trash writes entity.restored.
func (s *service) RestoreReport(ctx context.Context, id string, actor Actor) error {
	a, err := writeActor(actor)
	if err != nil {
		return err
	}
	if err := s.requireWritable(ctx); err != nil {
		return err
	}
	if s.trash == nil {
		return serviceError(ErrInternal, "回收站服务未配置")
	}
	h, err := s.handle(ctx)
	if err != nil {
		return err
	}
	return h.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := s.reportTx(ctx, tx, id, true); err != nil {
			return err
		}
		return s.trash.Restore(ctx, tx, trash.Ref{Type: schema.EntityWeeklyReport, ID: id}, a)
	})
}

// UpdateContent edits draft content with optimistic concurrency. It writes no
// Activity event (SPEC-09 §7.2).
func (s *service) UpdateContent(ctx context.Context, in UpdateContentInput) (*WeeklyReport, error) {
	if _, err := writeActor(in.Actor); err != nil {
		return nil, err
	}
	if len(in.ContentMD) > maxContentBytes {
		return nil, serviceError(ErrContentTooLarge, "正文超过 512 KiB 上限")
	}
	if in.ExpectedHash == "" && in.ExpectedUpdatedAt == nil {
		return nil, serviceError(ErrContentConflict, "缺少并发令牌（expected_hash 或 expected_updated_at）")
	}
	if err := s.requireWritable(ctx); err != nil {
		return nil, err
	}
	h, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	report, err := s.reportByID(ctx, h.DB(), in.ReportID, false)
	if err != nil {
		return nil, err
	}
	if report.Status == StatusConfirmed {
		return nil, serviceError(ErrConfirmedReadOnly, "已确认周报不可编辑；请先撤回为草稿")
	}
	if in.ExpectedHash != "" && in.ExpectedHash != report.ContentHash {
		return nil, serviceError(ErrContentConflict, "内容已在别处修改").
			withDetails(map[string]any{"updated_at": report.UpdatedAt, "content_hash": report.ContentHash})
	}
	if in.ExpectedUpdatedAt != nil && !in.ExpectedUpdatedAt.Equal(report.UpdatedAt) {
		return nil, serviceError(ErrContentConflict, "内容已在别处修改").
			withDetails(map[string]any{"updated_at": report.UpdatedAt, "content_hash": report.ContentHash})
	}

	digest, err := s.CollectActivities(ctx, CollectInput{Period: report.Period})
	if err != nil {
		return nil, err
	}
	violations := Validate(in.ContentMD, defaultTemplate, digest)
	violations = mergeAcks(violations, report.Meta.Checks)
	violations = capViolations(violations)

	err = h.InTx(ctx, func(tx *sql.Tx) error {
		current, err := s.reportTx(ctx, tx, in.ReportID, false)
		if err != nil {
			return err
		}
		if current.Status == StatusConfirmed {
			return serviceError(ErrConfirmedReadOnly, "已确认周报不可编辑")
		}
		if in.ExpectedHash != "" && current.ContentHash != in.ExpectedHash {
			return serviceError(ErrContentConflict, "内容已在别处修改")
		}
		if in.ExpectedUpdatedAt != nil && !in.ExpectedUpdatedAt.Equal(current.UpdatedAt) {
			return serviceError(ErrContentConflict, "内容已在别处修改")
		}
		current.ContentMD = in.ContentMD
		current.ContentHash = contentHash(in.ContentMD)
		current.UpdatedAt = s.clock.Now().UTC()
		current.Meta.Checks = buildCheckState(violations, s.clock.Now())
		return updateReport(ctx, tx, current)
	})
	if err != nil {
		return nil, err
	}
	return s.getReport(ctx, in.ReportID)
}

// Confirm flips a draft to confirmed after all validation findings are acked.
func (s *service) Confirm(ctx context.Context, in ConfirmInput) (*WeeklyReport, error) {
	if in.Actor == ActorMCP || in.Actor == ActorAI {
		return nil, serviceError(ErrConfirmationRequired, "确认必须由应用内用户在确认队列中批准")
	}
	if in.Actor != "" && in.Actor != ActorUser {
		return nil, serviceError(ErrInvalidActor, "未知操作者 %q", string(in.Actor))
	}
	if err := s.requireWritable(ctx); err != nil {
		return nil, err
	}
	h, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	report, err := s.reportByID(ctx, h.DB(), in.ReportID, false)
	if err != nil {
		return nil, err
	}
	if report.Status != StatusDraft {
		return nil, serviceError(ErrInvalidInput, "只有草稿可以确认")
	}
	digest, err := s.CollectActivities(ctx, CollectInput{Period: report.Period})
	if err != nil {
		return nil, err
	}
	violations := mergeAcks(Validate(report.ContentMD, defaultTemplate, digest), report.Meta.Checks)
	violations = applyAckFingerprints(violations, in.AckFingerprints, in.AckNotes, s.clock.Now().UTC())
	var unresolved []Violation
	for _, v := range violations {
		if v.Ack == nil {
			unresolved = append(unresolved, v)
		}
	}
	if len(unresolved) > 0 {
		return nil, serviceError(ErrUnresolvedViolations, "存在未解决的校验项，无法确认").
			withDetails(map[string]any{"violations": unresolved})
	}

	now := s.clock.Now().UTC()
	err = h.InTx(ctx, func(tx *sql.Tx) error {
		current, err := s.reportTx(ctx, tx, in.ReportID, false)
		if err != nil {
			return err
		}
		if current.Status != StatusDraft {
			return serviceError(ErrInvalidInput, "只有草稿可以确认")
		}
		if current.ContentHash != report.ContentHash {
			return serviceError(ErrContentConflict, "报告内容在校验后发生变化，请重新确认")
		}
		if ids := sectionOutputIDs(current.Meta.Generation); len(ids) > 0 {
			if s.outputs == nil {
				return serviceError(ErrInternal, "AI 输出服务未配置，无法在同一事务内确认")
			}
			for _, id := range ids {
				if _, err := s.outputs.ConfirmTx(ctx, tx, id, "user"); err != nil {
					return fmt.Errorf("weeklyreview: confirm ai output: %w", err)
				}
			}
		}
		current.Status = StatusConfirmed
		current.ConfirmedAt = &now
		current.UpdatedAt = now
		current.Meta.ConfirmedHash = contentHash(current.ContentMD)
		current.Meta.Checks = buildCheckState(violations, now)
		if err := updateReport(ctx, tx, current); err != nil {
			return err
		}
		oldObj := events.Object{"status": string(StatusDraft)}
		newObj := events.Object{"status": string(StatusConfirmed)}
		return h.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventStatusChanged,
			EntityType: schema.EntityWeeklyReport,
			EntityID:   current.ID,
			Old:        &oldObj,
			New:        &newObj,
			Summary:    fmt.Sprintf("确认周报 %s–%s", current.Period.StartDate(), current.Period.EndDate()),
			Actor:      schema.ActorUser,
			At:         s.clock.Now(),
		})
	})
	if err != nil {
		return nil, err
	}
	return s.getReport(ctx, in.ReportID)
}

// RevertToDraft reopens a confirmed report for editing.
func (s *service) RevertToDraft(ctx context.Context, in RevertInput) (*WeeklyReport, error) {
	if in.Actor == ActorMCP || in.Actor == ActorAI {
		return nil, serviceError(ErrConfirmationRequired, "撤回仅允许应用内用户操作")
	}
	if in.Actor != "" && in.Actor != ActorUser {
		return nil, serviceError(ErrInvalidActor, "未知操作者 %q", string(in.Actor))
	}
	if err := s.requireWritable(ctx); err != nil {
		return nil, err
	}
	h, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	report, err := s.reportByID(ctx, h.DB(), in.ReportID, false)
	if err != nil {
		return nil, err
	}
	if report.Status != StatusConfirmed {
		return nil, serviceError(ErrInvalidInput, "只有已确认周报可以撤回")
	}
	now := s.clock.Now().UTC()
	err = h.InTx(ctx, func(tx *sql.Tx) error {
		current, err := s.reportTx(ctx, tx, in.ReportID, false)
		if err != nil {
			return err
		}
		if current.Status != StatusConfirmed {
			return serviceError(ErrInvalidInput, "只有已确认周报可以撤回")
		}
		current.Status = StatusDraft
		current.ConfirmedAt = nil
		current.UpdatedAt = now
		if err := updateReport(ctx, tx, current); err != nil {
			return err
		}
		oldObj := events.Object{"status": string(StatusConfirmed)}
		newObj := events.Object{"status": string(StatusDraft)}
		return h.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventStatusChanged,
			EntityType: schema.EntityWeeklyReport,
			EntityID:   current.ID,
			Old:        &oldObj,
			New:        &newObj,
			Summary:    fmt.Sprintf("撤回周报 %s–%s", current.Period.StartDate(), current.Period.EndDate()),
			Actor:      schema.ActorUser,
			At:         s.clock.Now(),
		})
	})
	if err != nil {
		return nil, err
	}
	return s.getReport(ctx, in.ReportID)
}

// sectionOutputIDs lists the AI outputs behind a report body: the per-section
// calls recorded by the section-wise generator, with the legacy single field
// as fallback for reports written before it.
func sectionOutputIDs(gen *GenerationMeta) []string {
	if gen == nil {
		return nil
	}
	if len(gen.AIOutputIDs) > 0 {
		return gen.AIOutputIDs
	}
	if gen.AIOutputID != "" {
		return []string{gen.AIOutputID}
	}
	return nil
}

func mergeAcks(violations []Violation, state *CheckState) []Violation {
	if state == nil {
		return violations
	}
	acks := make(map[string]*Ack, len(state.Violations))
	for i := range state.Violations {
		if state.Violations[i].Ack != nil {
			acks[state.Violations[i].Fingerprint] = state.Violations[i].Ack
		}
	}
	for i := range violations {
		if ack, ok := acks[violations[i].Fingerprint]; ok {
			violations[i].Ack = ack
		}
	}
	return violations
}

func applyAckFingerprints(violations []Violation, fingerprints []string, notes map[string]string, now time.Time) []Violation {
	if len(fingerprints) == 0 {
		return violations
	}
	want := make(map[string]bool, len(fingerprints))
	for _, fp := range fingerprints {
		want[fp] = true
	}
	for i := range violations {
		if want[violations[i].Fingerprint] && violations[i].Ack == nil {
			violations[i].Ack = &Ack{Actor: "user", At: now, Note: notes[violations[i].Fingerprint]}
		}
	}
	return violations
}

// buildCheckState snapshots a validation run, flagging truncation at the
// 200-violation cap (SPEC-09 §4.3).
func buildCheckState(violations []Violation, at time.Time) *CheckState {
	return &CheckState{
		CheckedAt:  at,
		Violations: capViolations(violations),
		Truncated:  len(violations) > maxViolations,
	}
}

// capViolations keeps errors first and caps the list at 200.
func capViolations(violations []Violation) []Violation {
	if len(violations) <= maxViolations {
		return violations
	}
	sorted := append([]Violation(nil), violations...)
	sortViolationsBySeverity(sorted)
	return sorted[:maxViolations]
}

func sortViolationsBySeverity(v []Violation) {
	// Stable insertion: errors first, then warnings; order otherwise preserved.
	out := make([]Violation, 0, len(v))
	for _, x := range v {
		if x.Severity == SeverityError {
			out = append(out, x)
		}
	}
	for _, x := range v {
		if x.Severity != SeverityError {
			out = append(out, x)
		}
	}
	copy(v, out)
}
