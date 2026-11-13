package mcpbind

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"

	"researchos/internal/export"
	"researchos/internal/foundation/errs"
	"researchos/internal/mcp"
	"researchos/internal/project"
)

// newProjectProvider builds the SPEC-03 project domain (§5.3.1).
func newProjectProvider(d Deps, ready func(context.Context) error) mcp.ToolProvider {
	mod := mcp.ModuleProject
	facadeReady := func(ctx context.Context) error {
		if err := ready(ctx); err != nil {
			return err
		}
		if d.Facade == nil {
			return unavailablef(mcp.CodeNotFound, "项目模块未就绪")
		}
		return nil
	}
	tools := []mcp.ToolBinding{
		tool(mod, mcp.CategoryRead, "project_list", "项目列表",
			"列出工作区项目，可按状态与关键词过滤。归档项目仅在 status=archived 或显式包含时返回。",
			sObj(map[string]any{
				"status": sEnum("项目状态", "active", "archived"),
				"q":      sStr("名称/摘要关键字"),
				"limit":  sInt("返回条数，默认 50，上限 200", 1, 200),
				"cursor": sStr("上一页返回的 next_cursor"),
			}), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				status, _ := a.String("status")
				q := project.ListQuery{
					Query:           stringsOf(a, "q"),
					IncludeArchived: status == "archived",
				}
				all, err := d.Facade.ListProjects(ctx, q)
				if err != nil {
					return mcp.ToolOutcome{}, mapProjectError(err)
				}
				filtered := make([]project.Project, 0, len(all))
				for _, pr := range all {
					archived := pr.Status == "archived"
					switch status {
					case "active":
						if archived {
							continue
						}
					case "archived":
						if !archived {
							continue
						}
					}
					filtered = append(filtered, pr)
				}
				offset, err := cursorOffset(stringsOf(a, "cursor"))
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				limit := limitOf(a, 50, 200)
				window, next := windowProjects(filtered, offset, limit)
				items := make([]any, 0, len(window))
				for _, pr := range window {
					items = append(items, projectView(pr))
				}
				return okOut(pageOf(items, next))
			}),

		tool(mod, mcp.CategoryRead, "project_get", "项目详情",
			"返回一个项目的元数据、实体计数与数据目录状态。",
			sObj(map[string]any{"project_id": sStr("项目 ID")}, "project_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				id, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				pr, err := d.Facade.GetProject(ctx, id)
				if err != nil {
					return mcp.ToolOutcome{}, mapProjectError(err)
				}
				stats, err := d.Facade.ProjectStats(ctx, id)
				if err != nil {
					return mcp.ToolOutcome{}, mapProjectError(err)
				}
				dirStatus := "ok"
				if _, err := d.handle(ctx, id); err != nil {
					dirStatus = "missing"
				}
				return okOut(map[string]any{
					"project":    projectView(*pr),
					"counts":     statsView(stats),
					"dir_status": dirStatus,
				})
			}),

		tool(mod, mcp.CategoryRead, "project_overview", "项目总览",
			"返回项目总览聚合（焦点、活跃 Idea、近期实验、我的任务、近期笔记、文献、阻塞任务、周进度）。",
			sObj(map[string]any{"project_id": sStr("项目 ID")}, "project_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				id, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				snapshot, err := d.Facade.Overview(ctx, id)
				if err != nil {
					return mcp.ToolOutcome{}, mapProjectError(err)
				}
				return okOut(asJSON(snapshot))
			}),

		tool(mod, mcp.CategoryWrite, "project_create", "创建项目",
			"创建一个新项目。创建后可在应用内继续编辑焦点与设置。",
			sObj(map[string]any{
				"name":    sStrLen("项目名称", 1, 200),
				"summary": sStr("项目摘要"),
				"goals":   sStr("项目目标"),
			}, "name"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				name, err := requireString(a, "name")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				summary, _ := a.String("summary")
				goals, _ := a.String("goals")
				pr, err := d.Facade.CreateProject(ctx, project.CreateInput{Name: name, Summary: summary, Goals: goals})
				if err != nil {
					return mcp.ToolOutcome{}, mapProjectError(err)
				}
				return okOut(map[string]any{"project": projectView(*pr)})
			}),

		tool(mod, mcp.CategoryWrite, "project_update", "更新项目",
			"更新项目名称、摘要、目标或焦点。重命名不会改变项目目录名。",
			sObj(map[string]any{
				"project_id":          sStr("项目 ID"),
				"expected_updated_at": sStr("并发令牌（可选）"),
				"name":                sStr("项目名称"),
				"summary":             sStr("项目摘要"),
				"goals":               sStr("项目目标"),
			}, "project_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				id, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				in := project.UpdateInput{ID: id}
				in.Name = optionalStringPtr(a, "name")
				in.Summary = optionalStringPtr(a, "summary")
				in.Goals = optionalStringPtr(a, "goals")
				if in.Name == nil && in.Summary == nil && in.Goals == nil {
					return mcp.ToolOutcome{}, invalidf("至少提供一个要更新的字段")
				}
				pr, err := d.Facade.UpdateProject(ctx, in)
				if err != nil {
					return mcp.ToolOutcome{}, mapProjectError(err)
				}
				return okOut(map[string]any{"project": projectView(*pr)})
			}),

		tool(mod, mcp.CategoryWrite, "project_archive", "归档项目",
			"归档项目：项目变为只读，数据与目录保留，可随时解除归档。",
			sObj(map[string]any{"project_id": sStr("项目 ID")}, "project_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				id, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if err := d.Facade.ArchiveProject(ctx, id); err != nil {
					return mcp.ToolOutcome{}, mapProjectError(err)
				}
				pr, err := d.Facade.GetProject(ctx, id)
				if err != nil {
					return mcp.ToolOutcome{}, mapProjectError(err)
				}
				return okOut(map[string]any{"project": projectView(*pr)})
			}),

		tool(mod, mcp.CategoryWrite, "project_unarchive", "解除归档",
			"解除项目归档，恢复读写。",
			sObj(map[string]any{"project_id": sStr("项目 ID")}, "project_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				id, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if err := d.Facade.UnarchiveProject(ctx, id); err != nil {
					return mcp.ToolOutcome{}, mapProjectError(err)
				}
				pr, err := d.Facade.GetProject(ctx, id)
				if err != nil {
					return mcp.ToolOutcome{}, mapProjectError(err)
				}
				return okOut(map[string]any{"project": projectView(*pr)})
			}, allowArchived()),

		tool(mod, mcp.CategoryConfirm, "project_delete", "永久删除项目",
			"请求永久删除项目及其数据。工具只计算影响并入队，必须在 ResearchOS 应用内人工批准；dry_run=true 只返回影响预览。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"dry_run":    sBool("只返回影响预览，不入队"),
			}, "project_id"), annConfirm(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				id, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				plan, err := d.Facade.PrepareDeleteProject(ctx, id)
				if err != nil {
					return mcp.ToolOutcome{}, mapProjectError(err)
				}
				impact := deleteImpact(plan)
				if a.BoolDefault("dry_run", false) {
					return okOut(map[string]any{
						"status":     "dry_run",
						"project_id": id,
						"impact":     impact,
					})
				}
				summary := "永久删除项目《" + plan.ProjectName + "》及其目录与数据库；共享文献保留，独占文献副本清理。"
				return pendingOut("永久删除项目", summary, impact, id)
			}, allowArchived()),

		tool(mod, mcp.CategoryJob, "project_export", "导出项目",
			"导出项目为 folder/zip/tar.gz 包，固定包含全部关联文献 PDF。返回 job_id，可用 job_get 轮询。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"dest_dir":   sStr("目标目录（绝对路径，必须存在且可写；省略时为 <工作区>/exports）"),
				"format":     sEnum("导出格式", "folder", "zip", "tar_gz"),
			}, "project_id", "format"), annJob(false),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				id, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				format, err := parseExportFormat(a)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				destDir, _ := a.String("dest_dir")
				if destDir != "" && !filepath.IsAbs(destDir) {
					return mcp.ToolOutcome{}, invalidf("dest_dir 必须是绝对目录")
				}
				params, _ := json.Marshal(map[string]any{"project_id": id, "dest_dir": destDir, "format": formatLabel(format)})
				job, err := d.Jobs.Submit(ctx, p, mcp.JobRequest{
					Tool:      "project_export",
					ProjectID: id,
					ClientID:  p.ClientID,
					Params:    params,
					TraceID:   mcp.TraceIDFrom(ctx),
					Run: func(jctx context.Context, progress mcp.ProgressFunc) (any, error) {
						jobID, err := d.Facade.StartExport(jctx, export.Request{
							ProjectID: id,
							DestDir:   destDir,
							Format:    format,
						})
						if err != nil {
							return nil, mapProjectError(err)
						}
						progress(5, "准备导出")
						if ch, err := d.Facade.ExportProgress(jctx, jobID); err == nil {
							go func() {
								for sample := range ch {
									progress(exportPercent(sample), exportStage(sample))
								}
							}()
						}
						result, err := d.Facade.AwaitExport(jctx, jobID)
						if err != nil {
							return nil, mapProjectError(err)
						}
						warnings := make([]any, 0, len(result.Warnings))
						for _, w := range result.Warnings {
							warnings = append(warnings, map[string]any{"code": w.Code, "path": w.Path})
						}
						return map[string]any{
							"artifact_path":   result.Path,
							"bytes":           result.Bytes,
							"format":          formatLabel(result.Format),
							"papers_included": result.PaperCount,
							"files_included":  result.FileCount,
							"archive_sha256":  result.ArchiveSHA256,
							"warnings":        warnings,
						}, nil
					},
				})
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				return jobOut(job)
			}, allowArchived()),
	}
	return &provider{module: mod, tools: tools, ready: facadeReady}
}

func stringsOf(a *mcp.Arguments, name string) string {
	s, _ := a.String(name)
	return s
}

func windowProjects(all []project.Project, offset, limit int) ([]project.Project, string) {
	total := len(all)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return all[offset:end], nextOffsetCursor(offset, limit, total)
}

func projectView(p project.Project) map[string]any {
	return map[string]any{
		"id":          p.ID,
		"name":        p.Name,
		"summary":     p.Summary,
		"goals":       p.Goals,
		"focus_md":    p.FocusMD,
		"status":      string(p.Status),
		"created_at":  ts(p.CreatedAt),
		"archived_at": tsp(p.ArchivedAt),
	}
}

func statsView(s project.Stats) map[string]any {
	return map[string]any{
		"notes":            s.NoteCount,
		"ideas_active":     s.IdeaActive,
		"experiments":      s.ExperimentCount,
		"tasks_open":       s.TaskOpen,
		"tasks_blocked":    s.TaskBlocked,
		"papers":           s.PaperCount,
		"trashed":          s.TrashedCount,
		"dir_bytes":        s.DirBytes,
		"last_activity_at": tsp(s.LastActivityAt),
	}
}

func deleteImpact(plan *project.DeletePlan) map[string]any {
	counts := map[string]any{}
	for k, v := range plan.EntityCounts {
		counts[k] = v
	}
	return map[string]any{
		"project_name":       plan.ProjectName,
		"entity_counts":      counts,
		"trashed":            plan.TrashedCount,
		"dir_bytes":          plan.DirBytes,
		"papers_shared":      len(plan.SharedPaperIDs),
		"papers_orphaned":    len(plan.ExclusivePaperIDs),
		"paper_impact_known": !plan.PaperImpactUnknown,
		"last_exported_at":   tsp(plan.LastExportedAt),
		"export_recommended": plan.ExportRecommended,
	}
}

func parseExportFormat(a *mcp.Arguments) (export.Format, error) {
	raw, err := requireString(a, "format")
	if err != nil {
		return "", err
	}
	switch raw {
	case "folder":
		return export.FormatFolder, nil
	case "zip":
		return export.FormatZip, nil
	case "tar_gz":
		return export.FormatTarGz, nil
	default:
		return "", invalidf("format 必须是 folder、zip 或 tar_gz")
	}
}

func formatLabel(f export.Format) string {
	if f == export.FormatTarGz {
		return "tar_gz"
	}
	return string(f)
}

func exportPercent(p export.Progress) int {
	switch p.Phase {
	case export.PhasePlanning:
		return 5
	case export.PhaseSnapshotDB:
		return 15
	case export.PhaseCopyNotes:
		return 35
	case export.PhaseCopyAttachments:
		return 55
	case export.PhaseCopyPapers:
		if p.FilesTotal > 0 {
			return 55 + 40*p.FilesDone/p.FilesTotal
		}
		return 60
	case export.PhaseFinalize:
		return 95
	default:
		if p.FilesTotal > 0 {
			return 10 + 80*p.FilesDone/p.FilesTotal
		}
		return 10
	}
}

func exportStage(p export.Progress) string {
	switch p.Phase {
	case export.PhasePlanning:
		return "准备导出"
	case export.PhaseSnapshotDB:
		return "快照数据库"
	case export.PhaseCopyNotes:
		return "复制笔记"
	case export.PhaseCopyAttachments:
		return "复制附件"
	case export.PhaseCopyPapers:
		if p.FilesTotal > 0 {
			return "复制文献 " + itoa(p.FilesDone) + "/" + itoa(p.FilesTotal)
		}
		return "复制文献"
	case export.PhaseFinalize:
		return "收尾"
	default:
		return "导出中"
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// mapProjectError maps SPEC-03 sentinels to MCP codes.
func mapProjectError(err error) error {
	if err == nil {
		return nil
	}
	var me *mcp.Error
	if errors.As(err, &me) {
		return me
	}
	switch {
	case errors.Is(err, project.ErrNotFound):
		return mcp.Errorf(mcp.CodeNotFound, "项目不存在").Wrap(err)
	case errors.Is(err, project.ErrArchived):
		return mcp.Errorf(mcp.CodeArchivedReadOnly, "项目已归档，不能写入").Wrap(err)
	case errors.Is(err, project.ErrInvalidActor):
		return mcp.Errorf(mcp.CodeInvalidArgument, "调用者身份不合法").Wrap(err)
	case errors.Is(err, export.ErrBusy):
		return mcp.Errorf(mcp.CodeRateLimited, "已有导出任务在运行").WithDetail("retry_after_ms", 2000)
	case errors.Is(err, export.ErrPlanExpired):
		return mcp.Errorf(mcp.CodeConflict, "导出计划已过期，请重试").Wrap(err)
	case errors.Is(err, errs.ErrUserConfirmationRequired), errors.Is(err, errs.ErrConfirmScopeMismatch):
		return mcp.Errorf(mcp.CodeCapabilityInvalid, "该操作需要应用内人工确认").Wrap(err)
	case errors.Is(err, sql.ErrNoRows):
		return mcp.Errorf(mcp.CodeNotFound, "项目不存在").Wrap(err)
	}
	return asMCPerror(err, mcp.CodeInternal, "项目模块执行失败")
}

// asJSON converts a typed projection to a generic JSON value with stable keys.
func asJSON(v any) any {
	body, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var out any
	if err := json.Unmarshal(body, &out); err != nil {
		return map[string]any{}
	}
	return out
}
