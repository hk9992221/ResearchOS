package mcpbind

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"researchos/internal/mcp"
	"researchos/internal/weeklyreview"
)

func (d *Deps) weeklyService(ctx context.Context, projectID string) (weeklyreview.Service, error) {
	h, err := d.handle(ctx, projectID)
	if err != nil {
		return nil, err
	}
	opts := weeklyreview.DepsFromWorkspace(d.Workspace, h)
	if gen, outs, ok := d.ai(ctx, projectID); ok {
		opts.AI = gen
		opts.Outputs = outs
	}
	opts.Settings = d.Settings
	opts.Logger = d.Logger
	return weeklyreview.New(d.Workspace, projectID, opts), nil
}

// newWeeklyProvider builds the SPEC-09 weekly report domain (§5.3.6). There is
// no report_revert tool: reverting a confirmed report is UI-only.
func newWeeklyProvider(d Deps, ready func(context.Context) error) mcp.ToolProvider {
	mod := mcp.ModuleWeekly
	tools := []mcp.ToolBinding{
		tool(mod, mcp.CategoryRead, "report_list", "周报列表",
			"列出项目周报，按周期倒序。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"limit":      sInt("每页条数，默认 20，上限 100", 1, 100),
				"cursor":     sStr("上一页返回的 next_cursor"),
			}, "project_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.weeklyService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				offset, err := cursorOffset(stringsOf(a, "cursor"))
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				limit := limitOf(a, 20, 100)
				page := offset/limit + 1
				res, err := svc.ListReports(ctx, weeklyreview.ListReportsInput{Page: page, PageSize: limit})
				if err != nil {
					return mcp.ToolOutcome{}, mapWeeklyError(err)
				}
				items := make([]any, 0, len(res.Items))
				for _, r := range res.Items {
					items = append(items, reportSummaryView(r))
				}
				return okOut(pageOf(items, nextOffsetCursor(offset, limit, res.Total)))
			}),

		tool(mod, mcp.CategoryRead, "report_get", "周报详情",
			"返回周报元数据与 Markdown 正文。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"report_id":  sStr("周报 ID"),
			}, "project_id", "report_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, reportID, err := projectAndID(a, "report_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.weeklyService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				report, err := svc.GetReport(ctx, reportID)
				if err != nil {
					return mcp.ToolOutcome{}, mapWeeklyError(err)
				}
				return okOut(map[string]any{
					"report":     reportView(report),
					"content_md": report.ContentMD,
				})
			}),

		tool(mod, mcp.CategoryWrite, "report_create", "创建周报",
			"创建一个空 draft 周报。内容可用 report_generate 生成或 report_update 写入。",
			sObj(map[string]any{
				"project_id":   sStr("项目 ID"),
				"period_start": sStrLen("周期开始日期 YYYY-MM-DD", 10, 10),
				"period_end":   sStrLen("周期结束日期 YYYY-MM-DD", 10, 10),
			}, "project_id", "period_start", "period_end"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				period, err := parsePeriodArgs(a)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.weeklyService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				report, err := svc.CreateReport(ctx, weeklyreview.CreateReportInput{
					Period: period,
					Actor:  weeklyreview.ActorMCP,
				})
				if err != nil {
					return mcp.ToolOutcome{}, mapWeeklyError(err)
				}
				return okOut(map[string]any{"report_id": report.ID, "status": string(report.Status)})
			}),

		tool(mod, mcp.CategoryJob, "report_generate", "生成周报草稿",
			"依据活动汇总生成周报 draft（AI 不可用时降级为确定性骨架）。返回 job_id。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"report_id":  sStr("周报 ID"),
				"replace":    sBool("是否覆盖已有内容，默认 false"),
				"skill_id":   sStr("写作 Skill ID，省略使用工作区默认"),
			}, "project_id", "report_id"), annJob(false),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, reportID, err := projectAndID(a, "report_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				replace := a.BoolDefault("replace", false)
				skillID, _ := a.String("skill_id")
				svc, err := d.weeklyService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if replace {
					current, err := svc.GetReport(ctx, reportID)
					if err != nil {
						return mcp.ToolOutcome{}, mapWeeklyError(err)
					}
					if strings.TrimSpace(current.ContentMD) != "" {
						// SPEC-09 requires an in-app decision before overwriting
						// content, so this variant is a confirmation, not a job.
						return pendingOut("重新生成周报",
							"覆盖周报《"+current.Title+"》的现有正文（重新生成会替换全部内容）。",
							map[string]any{
								"report_id":    reportID,
								"status":       string(current.Status),
								"content_hash": current.ContentHash,
							}, projectID)
					}
				}
				params, _ := json.Marshal(map[string]any{"report_id": reportID, "replace": replace, "skill_id": skillID})
				job, err := d.Jobs.Submit(ctx, p, mcp.JobRequest{
					Tool:      "report_generate",
					ProjectID: projectID,
					ClientID:  p.ClientID,
					Params:    params,
					TraceID:   mcp.TraceIDFrom(ctx),
					Run: func(jctx context.Context, progress mcp.ProgressFunc) (any, error) {
						progress(10, "汇总活动")
						out, err := svc.GenerateDraft(jctx, weeklyreview.GenerateInput{
							ReportID: reportID,
							Replace:  replace,
							Actor:    weeklyreview.ActorMCP,
							SkillID:  skillID,
							Progress: func(done, total int, section weeklyreview.SectionKey) {
								if section == "" || total <= 0 {
									return
								}
								progress(10+done*80/total, "生成分区 "+weeklyreview.SectionTitle(section))
							},
						})
						if err != nil {
							return nil, mapWeeklyError(err)
						}
						progress(95, "写入草稿")
						return map[string]any{
							"report_id":  out.Report.ID,
							"status":     string(out.Report.Status),
							"updated_at": ts(out.Report.UpdatedAt),
						}, nil
					},
				})
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				return jobOut(job)
			}),

		tool(mod, mcp.CategoryWrite, "report_update", "编辑周报正文",
			"替换周报 Markdown 正文（仅 draft 可改）。可带 expected_updated_at 做乐观并发。",
			sObj(map[string]any{
				"project_id":          sStr("项目 ID"),
				"report_id":           sStr("周报 ID"),
				"content_md":          sStr("Markdown 正文"),
				"expected_updated_at": sStr("并发令牌 RFC3339"),
			}, "project_id", "report_id", "content_md"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, reportID, err := projectAndID(a, "report_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				content, err := requireString(a, "content_md")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				expected, err := parseArgTime(a, "expected_updated_at")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.weeklyService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				in := weeklyreview.UpdateContentInput{
					ReportID:          reportID,
					ContentMD:         content,
					ExpectedUpdatedAt: expected,
					Actor:             weeklyreview.ActorMCP,
				}
				if in.ExpectedUpdatedAt == nil {
					current, err := svc.GetReport(ctx, reportID)
					if err != nil {
						return mcp.ToolOutcome{}, mapWeeklyError(err)
					}
					in.ExpectedHash = current.ContentHash
				}
				report, err := svc.UpdateContent(ctx, in)
				if err != nil {
					return mcp.ToolOutcome{}, mapWeeklyError(err)
				}
				return okOut(map[string]any{"report": reportView(report)})
			}),

		tool(mod, mcp.CategoryConfirm, "report_confirm", "确认周报",
			"请求确认周报。工具只入队，必须在应用内人工批准后以 actor=user 执行。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"report_id":  sStr("周报 ID"),
			}, "project_id", "report_id"), annConfirm(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, reportID, err := projectAndID(a, "report_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.weeklyService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				report, err := svc.GetReport(ctx, reportID)
				if err != nil {
					return mcp.ToolOutcome{}, mapWeeklyError(err)
				}
				preview := report.ContentMD
				if len(preview) > 4000 {
					preview = preview[:4000] + "\n…（预览已截断）"
				}
				impact := map[string]any{
					"report_id":    report.ID,
					"period_start": report.Period.StartDate(),
					"period_end":   report.Period.EndDate(),
					"status":       string(report.Status),
					"preview_md":   preview,
				}
				return pendingOut("确认周报", "确认周报《"+report.Title+"》，确认后正文只读。", impact, projectID)
			}),

		tool(mod, mcp.CategoryWrite, "report_delete", "删除周报",
			"软删除周报（进入回收站）。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"report_id":  sStr("周报 ID"),
			}, "project_id", "report_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, reportID, err := projectAndID(a, "report_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.weeklyService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if err := svc.DeleteReport(ctx, reportID, weeklyreview.ActorMCP); err != nil {
					return mcp.ToolOutcome{}, mapWeeklyError(err)
				}
				return okOut(map[string]any{"deleted": true})
			}),

		tool(mod, mcp.CategoryJob, "report_export", "导出周报",
			"把周报导出为 Markdown 或 HTML 文件。返回 job_id。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"report_id":  sStr("周报 ID"),
				"dest_dir":   sStr("目标目录（绝对路径，可省略）"),
				"format":     sEnum("导出格式", "md", "html"),
			}, "project_id", "report_id", "format"), annJob(false),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, reportID, err := projectAndID(a, "report_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				format, err := requireString(a, "format")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if format != "md" && format != "html" {
					return mcp.ToolOutcome{}, invalidf("周报导出仅支持 format=md 或 format=html")
				}
				destDir, _ := a.String("dest_dir")
				svc, err := d.weeklyService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				params, _ := json.Marshal(map[string]any{"report_id": reportID, "dest_dir": destDir, "format": format})
				job, err := d.Jobs.Submit(ctx, p, mcp.JobRequest{
					Tool:      "report_export",
					ProjectID: projectID,
					ClientID:  p.ClientID,
					Params:    params,
					TraceID:   mcp.TraceIDFrom(ctx),
					Run: func(jctx context.Context, progress mcp.ProgressFunc) (any, error) {
						progress(30, "渲染周报")
						res, err := svc.Export(jctx, weeklyreview.ExportInput{
							ReportID: reportID,
							Format:   format,
							Dir:      destDir,
							Actor:    weeklyreview.ActorMCP,
						})
						if err != nil {
							return nil, mapWeeklyError(err)
						}
						progress(100, "已导出")
						return map[string]any{"artifact_path": res.Path, "bytes": res.Bytes, "format": res.Format}, nil
					},
				})
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				return jobOut(job)
			}),
	}
	return &provider{module: mod, tools: tools, ready: ready}
}

func parsePeriodArgs(a *mcp.Arguments) (weeklyreview.Period, error) {
	startRaw, err := requireString(a, "period_start")
	if err != nil {
		return weeklyreview.Period{}, err
	}
	endRaw, err := requireString(a, "period_end")
	if err != nil {
		return weeklyreview.Period{}, err
	}
	start, err := time.ParseInLocation("2006-01-02", startRaw, time.Local)
	if err != nil {
		return weeklyreview.Period{}, invalidf("period_start 必须是 YYYY-MM-DD")
	}
	end, err := time.ParseInLocation("2006-01-02", endRaw, time.Local)
	if err != nil {
		return weeklyreview.Period{}, invalidf("period_end 必须是 YYYY-MM-DD")
	}
	return weeklyreview.Period{Start: start, EndExclusive: end.AddDate(0, 0, 1)}, nil
}

func reportView(r *weeklyreview.WeeklyReport) map[string]any {
	if r == nil {
		return nil
	}
	out := map[string]any{
		"report_id":    r.ID,
		"period_start": r.Period.StartDate(),
		"period_end":   r.Period.EndDate(),
		"title":        r.Title,
		"content_hash": r.ContentHash,
		"status":       string(r.Status),
		"created_at":   ts(r.CreatedAt),
		"updated_at":   ts(r.UpdatedAt),
		"confirmed_at": tsp(r.ConfirmedAt),
		"deleted_at":   tsp(r.DeletedAt),
	}
	out["meta"] = asJSON(r.Meta)
	return out
}

func reportSummaryView(r weeklyreview.ReportSummary) map[string]any {
	return map[string]any{
		"report_id":    r.ID,
		"title":        r.Title,
		"period_start": r.Period.StartDate(),
		"period_end":   r.Period.EndDate(),
		"status":       string(r.Status),
		"excerpt":      r.Excerpt,
		"updated_at":   ts(r.UpdatedAt),
		"confirmed_at": tsp(r.ConfirmedAt),
		"deleted_at":   tsp(r.DeletedAt),
	}
}

// mapWeeklyError maps SPEC-09 ErrorCodes to MCP codes.
func mapWeeklyError(err error) error {
	if err == nil {
		return nil
	}
	var me *mcp.Error
	if errors.As(err, &me) {
		return me
	}
	switch weeklyreview.CodeOf(err) {
	case weeklyreview.ErrNotFound, weeklyreview.ErrProjectNotFound:
		return mcp.Errorf(mcp.CodeNotFound, "周报或项目不存在").Wrap(err)
	case weeklyreview.ErrConfirmedReadOnly, weeklyreview.ErrContentConflict, weeklyreview.ErrPeriodOccupied,
		weeklyreview.ErrReportNotEmpty, weeklyreview.ErrUnresolvedViolations, weeklyreview.ErrExportExists:
		return mcp.Errorf(mcp.CodeConflict, "周报当前状态不允许该操作").Wrap(err)
	case weeklyreview.ErrProjectArchived:
		return mcp.Errorf(mcp.CodeArchivedReadOnly, "项目已归档，不能写入").Wrap(err)
	case weeklyreview.ErrConfirmationRequired:
		return mcp.Errorf(mcp.CodeCapabilityInvalid, "该操作需要应用内人工确认").Wrap(err)
	case weeklyreview.ErrAIUnavailable:
		return mcp.Errorf(mcp.CodeAIUnavailable, "AI 服务不可用，周报生成已跳过").Wrap(err)
	case weeklyreview.ErrUnsupportedFormat:
		return mcp.Errorf(mcp.CodeInvalidArgument, "不支持的导出格式").Wrap(err)
	case weeklyreview.ErrInvalidExportPath, weeklyreview.ErrExportDirMissing:
		return mcp.Errorf(mcp.CodeInvalidArgument, "导出路径不可用").Wrap(err)
	case weeklyreview.ErrInvalidPeriod, weeklyreview.ErrInvalidInput, weeklyreview.ErrContentTooLarge,
		weeklyreview.ErrInvalidActor, weeklyreview.ErrAIOutputInvalid:
		return mcp.Errorf(mcp.CodeInvalidArgument, "参数不合法").Wrap(err)
	}
	return asMCPerror(err, mcp.CodeInternal, "周报模块执行失败")
}
