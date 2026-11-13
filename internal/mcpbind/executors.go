package mcpbind

import (
	"context"
	"encoding/json"
	"errors"

	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/trash"
	"researchos/internal/ideas"
	"researchos/internal/literature"
	"researchos/internal/mcp"
	"researchos/internal/project"
	"researchos/internal/weeklyreview"
)

// ExecuteInput is the in-app decision for one confirmation.
type ExecuteInput struct {
	// ConfirmationID is the queued request.
	ConfirmationID string
	// Approve false denies the request (state=denied, no write).
	Approve bool
	// By is the resolving principal, always "user" for a human decision.
	By string
	// UserInput carries values only the user can supply (for example the
	// project name typed in the delete dialog).
	UserInput map[string]any
}

// ConfirmationExecutor is the application-side executor behind the confirmation
// center. It is never reachable from the MCP tool surface. The UI calls
// ResolveAndExecute after the user decides: approval issues the one-time
// confirm token, calls the module service with actor=user, consumes the token
// and records executed/failed (SPEC-11 §5.5.1).
type ConfirmationExecutor struct {
	admin   ConfirmationAdmin
	confirm confirm.Service
	deps    Deps
	project *project.Service
	logger  mcp.Logger

	handlers map[string]executeFunc
}

// ConfirmationAdmin is the subset of mcp.ConfirmationAdmin the executor needs.
type ConfirmationAdmin interface {
	Get(ctx context.Context, p mcp.Principal, id string) (mcp.Confirmation, error)
	Resolve(ctx context.Context, id string, approve bool, by string) (mcp.Confirmation, error)
	ClaimExecution(ctx context.Context, id string) (mcp.Confirmation, error)
	Finish(ctx context.Context, id string, result any, errCode string) (mcp.Confirmation, error)
}

type executeFunc func(ctx context.Context, e *ConfirmationExecutor, conf mcp.Confirmation, in ExecuteInput) (any, error)

// NewConfirmationExecutor wires the executor and its per-tool handlers.
func NewConfirmationExecutor(admin ConfirmationAdmin, deps Deps, confirmSvc confirm.Service, projectAdmin *project.Service, logger mcp.Logger) *ConfirmationExecutor {
	e := &ConfirmationExecutor{
		admin:   admin,
		confirm: confirmSvc,
		deps:    deps,
		project: projectAdmin,
		logger:  logger,
		handlers: map[string]executeFunc{
			"project_delete":   executeProjectDelete,
			"trash_empty":      executeTrashEmpty,
			"paper_sync_apply": executePaperSyncApply,
			"evidence_confirm": executeEvidenceConfirm,
			"report_confirm":   executeReportConfirm,
			"column_delete":    executeColumnDelete,
		},
	}
	return e
}

// Resolve records a decision without executing (state approved or denied).
func (e *ConfirmationExecutor) Resolve(ctx context.Context, id string, approve bool, by string) (mcp.Confirmation, error) {
	if by == "" {
		by = "user"
	}
	return e.admin.Resolve(ctx, id, approve, by)
}

// Execute runs an already-approved confirmation exactly once.
func (e *ConfirmationExecutor) Execute(ctx context.Context, id string, in ExecuteInput) (mcp.Confirmation, error) {
	conf, err := e.admin.Get(ctx, mcp.Principal{}, id)
	if err != nil {
		return mcp.Confirmation{}, err
	}
	if conf.State != mcp.ConfirmationApproved {
		return mcp.Confirmation{}, conflictf("确认 %s 当前状态为 %s，不能执行", id, conf.State)
	}
	if _, err := e.admin.ClaimExecution(ctx, id); err != nil {
		return mcp.Confirmation{}, err
	}
	handler, ok := e.handlers[conf.Tool]
	if !ok {
		_, _ = e.admin.Finish(ctx, id, nil, string(mcp.CodeCapabilityInvalid))
		return mcp.Confirmation{}, mcp.Errorf(mcp.CodeCapabilityInvalid, "确认类型 %s 没有应用内执行器", conf.Tool)
	}
	in.ConfirmationID = id
	result, execErr := handler(ctx, e, conf, in)
	if execErr != nil {
		code := string(mcp.CodeOf(execErr))
		set, finishErr := e.admin.Finish(ctx, id, result, code)
		if finishErr != nil && e.logger != nil {
			e.logger.Warn("mcp: confirmation finish failed", "confirmation", id, "err", finishErr)
		}
		if set.ID != "" {
			return set, execErr
		}
		return mcp.Confirmation{}, execErr
	}
	return e.admin.Finish(ctx, id, result, "")
}

// ResolveAndExecute combines approval and execution; denial only sets denied.
func (e *ConfirmationExecutor) ResolveAndExecute(ctx context.Context, in ExecuteInput) (mcp.Confirmation, error) {
	if !in.Approve {
		conf, err := e.Resolve(ctx, in.ConfirmationID, false, in.By)
		if err != nil {
			return mcp.Confirmation{}, err
		}
		return conf, nil
	}
	if _, err := e.Resolve(ctx, in.ConfirmationID, true, in.By); err != nil {
		return mcp.Confirmation{}, err
	}
	return e.Execute(ctx, in.ConfirmationID, in)
}

// ---------------------------------------------------------------------------
// per-tool executors
// ---------------------------------------------------------------------------

func executeProjectDelete(ctx context.Context, e *ConfirmationExecutor, conf mcp.Confirmation, in ExecuteInput) (any, error) {
	if e.project == nil {
		return nil, mcp.Errorf(mcp.CodeInternal, "项目服务不可用")
	}
	var args struct {
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(conf.Args, &args); err != nil || args.ProjectID == "" {
		return nil, invalidf("确认参数缺少 project_id")
	}
	plan, err := e.project.PrepareDelete(ctx, args.ProjectID)
	if err != nil {
		return nil, mapProjectError(err)
	}
	confirmName, _ := in.UserInput["confirm_name"].(string)
	if confirmName == "" || confirmName != plan.ProjectName {
		return nil, conflictf("项目名不匹配：请在应用中逐字输入项目名 %q", plan.ProjectName)
	}
	if e.confirm == nil {
		return nil, mcp.Errorf(mcp.CodeCapabilityInvalid, "确认令牌服务不可用")
	}
	scope := confirm.Scope{Kind: confirm.KindDeleteProject, ProjectID: args.ProjectID}
	token, err := e.confirm.Issue(ctx, scope)
	if err != nil {
		return nil, mcp.Errorf(mcp.CodeInternal, "签发确认令牌失败").Wrap(err)
	}
	result, err := e.project.PermanentDelete(ctx, project.PermanentDeleteRequest{
		ProjectID:   args.ProjectID,
		ConfirmName: confirmName,
		Token:       token,
	})
	if err != nil {
		return nil, mapProjectError(err)
	}
	return map[string]any{
		"project_id":      result.ProjectID,
		"removed_papers":  result.RemovedPapers,
		"retained_papers": result.RetainedPapers,
		"warnings":        result.Warnings,
	}, nil
}

func executeTrashEmpty(ctx context.Context, e *ConfirmationExecutor, conf mcp.Confirmation, in ExecuteInput) (any, error) {
	var args struct {
		ProjectID      string         `json:"project_id"`
		ExpectedCounts map[string]any `json:"expected_counts"`
	}
	if err := json.Unmarshal(conf.Args, &args); err != nil || args.ProjectID == "" {
		return nil, invalidf("确认参数缺少 project_id")
	}
	counts, _, err := e.deps.trashCounts(ctx, args.ProjectID)
	if err != nil {
		return nil, err
	}
	if !sameCounts(args.ExpectedCounts, counts) {
		return nil, conflictf("回收站内容在确认后发生变化，请重新预览").
			WithDetail("actual", counts)
	}
	h, err := e.deps.handle(ctx, args.ProjectID)
	if err != nil {
		return nil, err
	}
	if e.confirm == nil {
		return nil, mcp.Errorf(mcp.CodeCapabilityInvalid, "确认令牌服务不可用")
	}
	entityTypes := make([]string, 0, 16)
	entityIDs := make([]string, 0, 16)
	svc := trash.New(args.ProjectID, h.DB(), e.deps.Workspace.Paths(), h.Events(), e.confirm, e.deps.clock())
	offset := 0
	for {
		items, err := svc.List(ctx, trash.ListQuery{Limit: 500, Offset: offset})
		if err != nil {
			return nil, mapTrashError(err)
		}
		for _, item := range items {
			entityTypes = append(entityTypes, string(item.EntityType))
			entityIDs = append(entityIDs, item.ID)
		}
		if len(items) < 500 {
			break
		}
		offset += 500
	}
	scope := confirm.Scope{Kind: confirm.KindEmptyTrash, ProjectID: args.ProjectID, EntityTypes: entityTypes, EntityIDs: entityIDs}
	token, err := e.confirm.Issue(ctx, scope)
	if err != nil {
		return nil, mcp.Errorf(mcp.CodeInternal, "签发确认令牌失败").Wrap(err)
	}
	res, err := svc.Empty(ctx, trash.EmptyRequest{Confirm: token, Actor: schema.ActorUser})
	if err != nil {
		if errors.Is(err, errs.ErrConfirmScopeMismatch) {
			return nil, conflictf("回收站内容在确认后发生变化，请重新预览")
		}
		return nil, mapTrashError(err)
	}
	return map[string]any{
		"purged":        res.Purged,
		"removed_files": res.RemovedFiles,
		"removed_dirs":  res.RemovedDirs,
		"cleared_links": res.ClearedLinks,
		"warnings":      res.Warnings,
	}, nil
}

func executePaperSyncApply(ctx context.Context, e *ConfirmationExecutor, conf mcp.Confirmation, in ExecuteInput) (any, error) {
	var args struct {
		ProjectID       string `json:"project_id"`
		PaperID         string `json:"paper_id"`
		ExpectedNewHash string `json:"expected_new_hash"`
	}
	if err := json.Unmarshal(conf.Args, &args); err != nil || args.PaperID == "" {
		return nil, invalidf("确认参数缺少 paper_id")
	}
	lib := e.deps.libraryService()
	report, err := lib.Sync().Diff(ctx, literature.SyncScope{ProjectID: args.ProjectID, PaperIDs: []string{args.PaperID}}, true)
	if err != nil {
		return nil, mapLiteratureError(err)
	}
	var diff *literature.SyncDiff
	for i := range report.Diffs {
		if report.Diffs[i].PaperID == args.PaperID {
			diff = &report.Diffs[i]
			break
		}
	}
	if diff == nil || !diff.Changed {
		return nil, conflictf("文献副本已无需覆盖，请重新比对")
	}
	if args.ExpectedNewHash == "" || diff.SourceHash != args.ExpectedNewHash {
		return nil, conflictf("上游文件哈希已变化，拒绝覆盖").WithDetail("current_hash", diff.SourceHash)
	}
	res, err := lib.Sync().Apply(ctx, literature.SyncApplyRequest{
		ReportID: report.ID,
		Decisions: []literature.OverwriteDecision{{
			PaperID:            args.PaperID,
			ExpectedLocalHash:  diff.LocalHash,
			ExpectedSourceHash: diff.SourceHash,
			Confirm:            true,
		}},
		Actor: literature.ActorUser,
	})
	if err != nil {
		return nil, mapLiteratureError(err)
	}
	if len(res.Overwritten) == 0 {
		if len(res.Conflicted) > 0 {
			return nil, conflictf("本地副本与预期哈希不一致，未覆盖")
		}
		failure := "覆盖未执行"
		if len(res.Failed) > 0 {
			failure = res.Failed[0].Message
		}
		return nil, mcp.Errorf(mcp.CodeExternalUnavail, "%s", failure)
	}
	return map[string]any{"paper_id": args.PaperID, "overwritten": true}, nil
}

func executeEvidenceConfirm(ctx context.Context, e *ConfirmationExecutor, conf mcp.Confirmation, in ExecuteInput) (any, error) {
	var args struct {
		ProjectID   string   `json:"project_id"`
		IdeaID      string   `json:"idea_id"`
		EvidenceIDs []string `json:"evidence_ids"`
	}
	if err := json.Unmarshal(conf.Args, &args); err != nil || args.IdeaID == "" || len(args.EvidenceIDs) == 0 {
		return nil, invalidf("确认参数缺少 idea_id 或 evidence_ids")
	}
	svc, err := e.deps.ideaService(ctx, args.ProjectID)
	if err != nil {
		return nil, err
	}
	res, err := svc.ConfirmEvidence(ctx, ideas.ConfirmEvidenceInput{
		IdeaID:      args.IdeaID,
		EvidenceIDs: args.EvidenceIDs,
		Actor:       events.Actor(schema.ActorUser),
	})
	if err != nil {
		return nil, mapIdeasError(err)
	}
	if len(res.Failed) > 0 {
		return map[string]any{
			"confirmed": res.Confirmed,
			"failed":    asJSON(res.Failed),
		}, mcp.Errorf(mcp.CodeConflict, "部分证据未能确认").WithDetail("failed", len(res.Failed))
	}
	return map[string]any{"confirmed": res.Confirmed}, nil
}

func executeReportConfirm(ctx context.Context, e *ConfirmationExecutor, conf mcp.Confirmation, in ExecuteInput) (any, error) {
	var args struct {
		ProjectID string `json:"project_id"`
		ReportID  string `json:"report_id"`
	}
	if err := json.Unmarshal(conf.Args, &args); err != nil || args.ReportID == "" {
		return nil, invalidf("确认参数缺少 report_id")
	}
	svc, err := e.deps.weeklyService(ctx, args.ProjectID)
	if err != nil {
		return nil, err
	}
	report, err := svc.Confirm(ctx, weeklyreview.ConfirmInput{ReportID: args.ReportID, Actor: weeklyreview.ActorUser})
	if err != nil {
		return nil, mapWeeklyError(err)
	}
	return map[string]any{"report_id": report.ID, "status": string(report.Status)}, nil
}

func executeColumnDelete(ctx context.Context, e *ConfirmationExecutor, conf mcp.Confirmation, in ExecuteInput) (any, error) {
	var args struct {
		ProjectID string `json:"project_id"`
		Name      string `json:"name"`
		MigrateTo string `json:"migrate_to"`
	}
	if err := json.Unmarshal(conf.Args, &args); err != nil || args.Name == "" {
		return nil, invalidf("确认参数缺少列名")
	}
	module, err := e.deps.kanbanModule(ctx, args.ProjectID)
	if err != nil {
		return nil, err
	}
	if err := module.Columns().Delete(ctx, events.Actor(schema.ActorUser), args.Name, args.MigrateTo); err != nil {
		return nil, mapKanbanError(err)
	}
	return map[string]any{"deleted": true, "column": args.Name, "migrated_to": args.MigrateTo}, nil
}

// executeReportGenerate handles the replace-over-content confirmation: after
// the user approves, the same in-process generation runs with actor=mcp.
func executeReportGenerate(ctx context.Context, e *ConfirmationExecutor, conf mcp.Confirmation, in ExecuteInput) (any, error) {
	var args struct {
		ProjectID string `json:"project_id"`
		ReportID  string `json:"report_id"`
		Replace   bool   `json:"replace"`
	}
	if err := json.Unmarshal(conf.Args, &args); err != nil || args.ReportID == "" {
		return nil, invalidf("确认参数缺少 report_id")
	}
	svc, err := e.deps.weeklyService(ctx, args.ProjectID)
	if err != nil {
		return nil, err
	}
	out, err := svc.GenerateDraft(ctx, weeklyreview.GenerateInput{
		ReportID: args.ReportID,
		Replace:  true,
		Actor:    weeklyreview.ActorMCP,
	})
	if err != nil {
		return nil, mapWeeklyError(err)
	}
	return map[string]any{"report_id": out.Report.ID, "status": string(out.Report.Status)}, nil
}
