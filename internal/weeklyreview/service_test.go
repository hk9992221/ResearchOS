package weeklyreview

import (
	"errors"
	"strings"
	"testing"
	"time"

	"researchos/internal/foundation/schema"
)

func (e *testEnv) newReport(period Period, skeleton bool) *WeeklyReport {
	e.t.Helper()
	report, err := e.svc.CreateReport(e.ctx, CreateReportInput{
		Period: period, Skeleton: skeleton, Actor: ActorUser,
	})
	if err != nil {
		e.t.Fatalf("CreateReport: %v", err)
	}
	return report
}

func TestAC14ContentConflict(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	report := env.newReport(period, true)

	_, err := env.svc.UpdateContent(env.ctx, UpdateContentInput{
		ReportID: report.ID, ContentMD: "# Rewritten\n", ExpectedHash: "deadbeef", Actor: ActorUser,
	})
	if !IsCode(err, ErrContentConflict) {
		t.Fatalf("UpdateContent = %v, want ErrContentConflict", err)
	}
	_, content, _ := env.reportRow(report.ID)
	if content != report.ContentMD {
		t.Fatalf("content changed on conflict: %q", content)
	}

	// The correct hash succeeds.
	_, err = env.svc.UpdateContent(env.ctx, UpdateContentInput{
		ReportID: report.ID, ContentMD: "# Rewritten\n", ExpectedHash: report.ContentHash, Actor: ActorUser,
	})
	if err != nil {
		t.Fatalf("UpdateContent with correct hash: %v", err)
	}
}

func TestAC15ConfirmedReadOnlyAndRevert(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	report := env.newReport(period, true)

	confirmed, err := env.svc.Confirm(env.ctx, ConfirmInput{ReportID: report.ID, Actor: ActorUser})
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if confirmed.Status != StatusConfirmed || confirmed.ConfirmedAt == nil {
		t.Fatalf("confirmed report = %+v", confirmed)
	}

	if _, err := env.svc.UpdateContent(env.ctx, UpdateContentInput{
		ReportID: report.ID, ContentMD: "changed", ExpectedHash: confirmed.ContentHash, Actor: ActorUser,
	}); !IsCode(err, ErrConfirmedReadOnly) {
		t.Fatalf("UpdateContent confirmed = %v, want ErrConfirmedReadOnly", err)
	}
	if _, err := env.svc.GenerateDraft(env.ctx, GenerateInput{
		ReportID: report.ID, Replace: true, Actor: ActorUser,
	}); !IsCode(err, ErrConfirmedReadOnly) {
		t.Fatalf("GenerateDraft confirmed = %v, want ErrConfirmedReadOnly", err)
	}

	env.clock.Advance(time.Hour)
	reverted, err := env.svc.RevertToDraft(env.ctx, RevertInput{ReportID: report.ID, Actor: ActorUser})
	if err != nil {
		t.Fatalf("RevertToDraft: %v", err)
	}
	if reverted.Status != StatusDraft || reverted.ConfirmedAt != nil {
		t.Fatalf("reverted report = %+v", reverted)
	}
	if _, err := env.svc.UpdateContent(env.ctx, UpdateContentInput{
		ReportID: report.ID, ContentMD: "# Edited after revert\n",
		ExpectedHash: reverted.ContentHash, Actor: ActorUser,
	}); err != nil {
		t.Fatalf("UpdateContent after revert: %v", err)
	}

	transitions := env.eventsOfType(schema.EventStatusChanged)
	if len(transitions) != 2 {
		t.Fatalf("status.changed count = %d, want 2", len(transitions))
	}
	// Newest first: revert then confirm.
	revert := transitions[0]
	confirm := transitions[1]
	if confirm.Payload.Old == nil || confirm.Payload.New == nil {
		t.Fatalf("confirm payload = %+v", confirm.Payload)
	}
	if (*confirm.Payload.Old)["status"] != "draft" || (*confirm.Payload.New)["status"] != "confirmed" {
		t.Fatalf("confirm old/new = %v/%v", *confirm.Payload.Old, *confirm.Payload.New)
	}
	if (*revert.Payload.Old)["status"] != "confirmed" || (*revert.Payload.New)["status"] != "draft" {
		t.Fatalf("revert old/new = %v/%v", *revert.Payload.Old, *revert.Payload.New)
	}
	for _, ev := range transitions {
		if ev.Actor != schema.ActorUser {
			t.Fatalf("status.changed actor = %s, want user", ev.Actor)
		}
	}
}

func TestAC16TrashAndArchivedProject(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	report := env.newReport(period, true)

	if err := env.svc.DeleteReport(env.ctx, report.ID, ActorUser); err != nil {
		t.Fatalf("DeleteReport: %v", err)
	}
	if _, err := env.svc.GetReport(env.ctx, report.ID); !IsCode(err, ErrNotFound) {
		t.Fatalf("GetReport deleted = %v, want ErrNotFound", err)
	}
	trashPage, err := env.svc.ListReports(env.ctx, ListReportsInput{IncludeDeleted: true})
	if err != nil {
		t.Fatalf("ListReports trash: %v", err)
	}
	if len(trashPage.Items) != 1 || trashPage.Items[0].DeletedAt == nil {
		t.Fatalf("trash page = %+v", trashPage.Items)
	}
	// Deleting an already-deleted report is idempotent and emits nothing.
	deletedEvents := len(env.eventsOfType(schema.EventEntityDeleted))
	if err := env.svc.DeleteReport(env.ctx, report.ID, ActorUser); err != nil {
		t.Fatalf("second DeleteReport: %v", err)
	}
	if got := len(env.eventsOfType(schema.EventEntityDeleted)); got != deletedEvents {
		t.Fatalf("second delete emitted an extra event: %d -> %d", deletedEvents, got)
	}
	if err := env.svc.RestoreReport(env.ctx, report.ID, ActorUser); err != nil {
		t.Fatalf("RestoreReport: %v", err)
	}
	restored, err := env.svc.GetReport(env.ctx, report.ID)
	if err != nil {
		t.Fatalf("GetReport restored: %v", err)
	}
	if restored.Status != StatusDraft || restored.DeletedAt != nil {
		t.Fatalf("restored = %+v", restored)
	}

	// Archive the project; every write is rejected, reads and export stay.
	env.execGlobal(`UPDATE projects SET status = 'archived' WHERE id = ?`, testProjectID)
	if _, err := env.svc.CreateReport(env.ctx, CreateReportInput{Period: period, Actor: ActorUser}); !IsCode(err, ErrProjectArchived) {
		t.Fatalf("CreateReport archived = %v, want ErrProjectArchived", err)
	}
	live, err := env.svc.GetReport(env.ctx, report.ID)
	if err != nil {
		t.Fatalf("GetReport archived: %v", err)
	}
	if _, err := env.svc.UpdateContent(env.ctx, UpdateContentInput{
		ReportID: live.ID, ContentMD: "# x\n", ExpectedHash: live.ContentHash, Actor: ActorUser,
	}); !IsCode(err, ErrProjectArchived) {
		t.Fatalf("UpdateContent archived = %v, want ErrProjectArchived", err)
	}
	if _, err := env.svc.GenerateDraft(env.ctx, GenerateInput{ReportID: live.ID, Actor: ActorUser}); !IsCode(err, ErrProjectArchived) {
		t.Fatalf("GenerateDraft archived = %v, want ErrProjectArchived", err)
	}
	if _, err := env.svc.Confirm(env.ctx, ConfirmInput{ReportID: live.ID, Actor: ActorUser}); !IsCode(err, ErrProjectArchived) {
		t.Fatalf("Confirm archived = %v, want ErrProjectArchived", err)
	}
	if err := env.svc.DeleteReport(env.ctx, live.ID, ActorUser); !IsCode(err, ErrProjectArchived) {
		t.Fatalf("DeleteReport archived = %v, want ErrProjectArchived", err)
	}
	if _, err := env.svc.ListReports(env.ctx, ListReportsInput{}); err != nil {
		t.Fatalf("ListReports archived: %v", err)
	}
	exported, err := env.svc.Export(env.ctx, ExportInput{ReportID: live.ID, Format: "md", Actor: ActorUser})
	if err != nil {
		t.Fatalf("Export archived: %v", err)
	}
	if exported.Path == "" {
		t.Fatal("export path empty")
	}
}

func TestAC17PeriodOccupied(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	first := env.newReport(period, true)

	_, err := env.svc.CreateReport(env.ctx, CreateReportInput{Period: period, Actor: ActorUser})
	if !IsCode(err, ErrPeriodOccupied) {
		t.Fatalf("CreateReport duplicate = %v, want ErrPeriodOccupied", err)
	}
	var se *ServiceError
	if !errors.As(err, &se) {
		t.Fatalf("duplicate error = %T", err)
	}
	if se.Details["existing_id"] != first.ID {
		t.Fatalf("existing_id = %v, want %s", se.Details["existing_id"], first.ID)
	}
	if _, err := env.svc.CreateReport(env.ctx, CreateReportInput{
		Period: period, Force: true, Actor: ActorUser,
	}); err != nil {
		t.Fatalf("CreateReport force: %v", err)
	}
	page, err := env.svc.ListReports(env.ctx, ListReportsInput{})
	if err != nil {
		t.Fatalf("ListReports: %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("total = %d, want 2", page.Total)
	}
}

func TestAC20NoBrowseEvents(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	report := env.newReport(period, true)
	before := env.activityCount()

	if _, err := env.svc.ListReports(env.ctx, ListReportsInput{}); err != nil {
		t.Fatalf("ListReports: %v", err)
	}
	if _, err := env.svc.CollectActivities(env.ctx, CollectInput{Period: period}); err != nil {
		t.Fatalf("CollectActivities: %v", err)
	}
	if _, err := env.svc.Export(env.ctx, ExportInput{ReportID: report.ID, Format: "md"}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if _, err := env.svc.GetReport(env.ctx, report.ID); err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if after := env.activityCount(); after != before {
		t.Fatalf("read/export wrote activities: %d -> %d", before, after)
	}
}

func TestAC21MCPActorAndConfirmation(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")

	// Creation through the MCP actor is recorded as actor=mcp.
	created, err := env.svc.CreateReport(env.ctx, CreateReportInput{
		Period: period, Skeleton: true, Actor: ActorMCP,
	})
	if err != nil {
		t.Fatalf("CreateReport mcp: %v", err)
	}
	createdEvents := env.eventsOfType(schema.EventEntityCreated)
	if len(createdEvents) != 1 || createdEvents[0].Actor != schema.ActorMCP {
		t.Fatalf("entity.created actor = %+v", createdEvents)
	}

	// Confirm is never allowed for MCP callers.
	if _, err := env.svc.Confirm(env.ctx, ConfirmInput{ReportID: created.ID, Actor: ActorMCP}); !IsCode(err, ErrConfirmationRequired) {
		t.Fatalf("Confirm mcp = %v, want ErrConfirmationRequired", err)
	}
	if _, err := env.svc.RevertToDraft(env.ctx, RevertInput{ReportID: created.ID, Actor: ActorMCP}); !IsCode(err, ErrConfirmationRequired) {
		t.Fatalf("RevertToDraft mcp = %v, want ErrConfirmationRequired", err)
	}

	// Replacing manually-authored content through MCP requires in-app approval.
	if _, err := env.svc.GenerateDraft(env.ctx, GenerateInput{
		ReportID: created.ID, Replace: true, Actor: ActorMCP,
	}); !IsCode(err, ErrConfirmationRequired) {
		t.Fatalf("GenerateDraft replace mcp = %v, want ErrConfirmationRequired", err)
	}

	// report_generate (IncludeAI=false) creates with actor=mcp.
	mcpPeriod := periodOf(t, loc, "2026-11-09", "2026-11-15")
	generated, err := env.svc.GenerateForPeriod(env.ctx, GeneratePeriodInput{
		Period: mcpPeriod, IncludeAI: false, Actor: ActorMCP,
	})
	if err != nil {
		t.Fatalf("GenerateForPeriod mcp: %v", err)
	}
	if generated.Report.ID == "" {
		t.Fatal("GenerateForPeriod returned no report")
	}
	createdEvents = env.eventsOfType(schema.EventEntityCreated)
	if len(createdEvents) != 2 {
		t.Fatalf("entity.created count = %d, want 2", len(createdEvents))
	}
	for _, ev := range createdEvents {
		if ev.Actor != schema.ActorMCP {
			t.Fatalf("entity.created actor = %s, want mcp", ev.Actor)
		}
	}

	// Delete via MCP carries actor=mcp and keeps the row in the trash.
	if err := env.svc.DeleteReport(env.ctx, created.ID, ActorMCP); err != nil {
		t.Fatalf("DeleteReport mcp: %v", err)
	}
	deleted := env.eventsOfType(schema.EventEntityDeleted)
	if len(deleted) != 1 || deleted[0].Actor != schema.ActorMCP {
		t.Fatalf("entity.deleted actor = %+v", deleted)
	}
	if status, _, _ := env.reportRow(created.ID); status != string(StatusDraft) {
		t.Fatalf("deleted report status = %s", status)
	}
}

func TestAC23ContentTooLarge(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	report := env.newReport(period, false)
	huge := "# Huge\n" + strings.Repeat("x", 600<<10)
	if _, err := env.svc.UpdateContent(env.ctx, UpdateContentInput{
		ReportID: report.ID, ContentMD: huge, ExpectedHash: report.ContentHash, Actor: ActorUser,
	}); !IsCode(err, ErrContentTooLarge) {
		t.Fatalf("UpdateContent huge = %v, want ErrContentTooLarge", err)
	}
	if _, content, _ := env.reportRow(report.ID); content != "" {
		t.Fatalf("oversized content was written: %d bytes", len(content))
	}
}

func TestListReportsFiltersAndOrder(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	older := env.newReport(periodOf(t, loc, "2026-10-26", "2026-11-01"), true)
	newer := env.newReport(periodOf(t, loc, "2026-11-02", "2026-11-08"), true)
	env.clock.Advance(time.Hour)
	if _, err := env.svc.Confirm(env.ctx, ConfirmInput{ReportID: newer.ID, Actor: ActorUser}); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	page, err := env.svc.ListReports(env.ctx, ListReportsInput{})
	if err != nil {
		t.Fatalf("ListReports: %v", err)
	}
	if page.Total != 2 || page.Items[0].ID != newer.ID {
		t.Fatalf("default order = %+v", page.Items)
	}
	confirmedPage, err := env.svc.ListReports(env.ctx, ListReportsInput{Status: []Status{StatusConfirmed}})
	if err != nil {
		t.Fatalf("ListReports status: %v", err)
	}
	if confirmedPage.Total != 1 || confirmedPage.Items[0].ID != newer.ID {
		t.Fatalf("confirmed filter = %+v", confirmedPage.Items)
	}
	from := dateAt(t, loc, "2026-10-20")
	to := dateAt(t, loc, "2026-11-03")
	rangePage, err := env.svc.ListReports(env.ctx, ListReportsInput{From: &from, To: &to})
	if err != nil {
		t.Fatalf("ListReports range: %v", err)
	}
	if rangePage.Total != 2 {
		t.Fatalf("range total = %d, want 2", rangePage.Total)
	}
	if page.Items[1].ID != older.ID {
		t.Fatalf("second item = %s, want %s", page.Items[1].ID, older.ID)
	}
}

func TestErrorContracts(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	// Empty / non-midnight periods are rejected.
	if _, err := env.svc.CreateReport(env.ctx, CreateReportInput{
		Period: Period{Start: dateAt(t, loc, "2026-11-02"), EndExclusive: dateAt(t, loc, "2026-11-02")},
	}); !IsCode(err, ErrInvalidPeriod) {
		t.Fatalf("empty period = %v, want ErrInvalidPeriod", err)
	}
	if _, err := env.svc.CreateReport(env.ctx, CreateReportInput{
		Period: Period{
			Start:        dateAt(t, loc, "2026-11-02").Add(time.Hour),
			EndExclusive: dateAt(t, loc, "2026-11-09"),
		},
	}); !IsCode(err, ErrInvalidPeriod) {
		t.Fatalf("non-midnight period = %v, want ErrInvalidPeriod", err)
	}
	if _, err := env.svc.CreateReport(env.ctx, CreateReportInput{
		Period: Period{
			Start:        dateAt(t, loc, "2025-01-01"),
			EndExclusive: dateAt(t, loc, "2026-12-31"),
		},
	}); !IsCode(err, ErrInvalidPeriod) {
		t.Fatalf("oversized period = %v, want ErrInvalidPeriod", err)
	}

	report := env.newReport(periodOf(t, loc, "2026-11-02", "2026-11-08"), true)
	if _, err := env.svc.GenerateDraft(env.ctx, GenerateInput{
		ReportID: report.ID, Actor: ActorUser,
	}); !IsCode(err, ErrReportNotEmpty) {
		t.Fatalf("GenerateDraft non-empty = %v, want ErrReportNotEmpty", err)
	}
	if _, err := env.svc.GetReport(env.ctx, "0192f2a0-0000-7000-8000-00000000dead"); !IsCode(err, ErrNotFound) {
		t.Fatalf("GetReport unknown = %v, want ErrNotFound", err)
	}
}
