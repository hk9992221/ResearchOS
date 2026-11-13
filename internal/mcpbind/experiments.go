package mcpbind

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"researchos/internal/experiments"
	"researchos/internal/mcp"
)

func (d *Deps) experimentService(ctx context.Context, projectID string) (*experiments.Service, error) {
	if err := d.projectExists(ctx, projectID); err != nil {
		return nil, err
	}
	opts := experiments.Options{Clock: d.clock(), Logger: d.Logger}
	if d.AI != nil && d.Outputs != nil {
		opts.AI = &experiments.AIConfig{Generator: d.AI, Outputs: d.Outputs}
	}
	return experiments.New(d.Workspace, projectID, opts), nil
}

// newExperimentsProvider builds the SPEC-07 Experiment/Run domain (§5.3.5).
// Snapshot tools are registered because SPEC-07 has landed the snapshot store.
func newExperimentsProvider(d Deps, ready func(context.Context) error) mcp.ToolProvider {
	mod := mcp.ModuleExperiments
	tools := []mcp.ToolBinding{
		tool(mod, mcp.CategoryRead, "experiment_list", "实验列表",
			"列出项目实验，可按标题过滤。",
			sObj(map[string]any{
				"project_id":      sStr("项目 ID"),
				"q":               sStr("标题关键字"),
				"include_deleted": sBool("包含已删除实验"),
				"limit":           sInt("返回条数，默认 50，上限 200", 1, 200),
				"cursor":          sStr("上一页返回的 next_cursor"),
			}, "project_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.experimentService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				offset, err := cursorOffset(stringsOf(a, "cursor"))
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				limit := limitOf(a, 50, 200)
				list, err := svc.Experiments().List(ctx, experiments.ExperimentFilter{
					Title:          stringsOf(a, "q"),
					IncludeDeleted: a.BoolDefault("include_deleted", false),
					Limit:          limit,
					Offset:         offset,
				})
				if err != nil {
					return mcp.ToolOutcome{}, mapExperimentsError(err)
				}
				items := make([]any, 0, len(list))
				for i := range list {
					items = append(items, experimentSummaryView(&list[i]))
				}
				next := ""
				if len(list) == limit {
					next = nextOffsetCursor(offset, limit, offset+limit+1)
				}
				return okOut(pageOf(items, next))
			}),

		tool(mod, mcp.CategoryRead, "experiment_get", "实验详情",
			"返回实验、目录引用、运行、快照与关联 Idea。",
			sObj(map[string]any{
				"project_id":    sStr("项目 ID"),
				"experiment_id": sStr("实验 ID"),
			}, "project_id", "experiment_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, expID, err := projectAndID(a, "experiment_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.experimentService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				detail, err := svc.Experiments().Get(ctx, expID)
				if err != nil {
					return mcp.ToolOutcome{}, mapExperimentsError(err)
				}
				return okOut(experimentDetailView(detail))
			}),

		tool(mod, mcp.CategoryWrite, "experiment_create", "创建实验",
			"创建实验；提供 dir_ref 时同时绑定目录（必须是已存在的绝对目录且不在工作区内）。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"title":      sStrLen("标题", 1, 200),
				"purpose":    sStr("实验目的"),
				"dir_ref":    sStr("实验目录绝对路径"),
				"scan_rules": sFreeObj("扫描规则"),
			}, "project_id", "title"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				title, err := requireString(a, "title")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				purpose, _ := a.String("purpose")
				dirRef, _ := a.String("dir_ref")
				rules, err := scanRulesArg(a)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.experimentService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				exp, err := svc.Experiments().Create(ctx, experiments.CreateExperimentInput{
					Title:   title,
					Purpose: purpose,
					DirPath: dirRef,
					Rules:   rules,
					Actor:   eventActor(),
				})
				if err != nil {
					return mcp.ToolOutcome{}, mapExperimentsError(err)
				}
				if rules != nil {
					if err := svc.SetScanRules(ctx, exp.ID, *rules); err != nil {
						return mcp.ToolOutcome{}, mapExperimentsError(err)
					}
				}
				out := map[string]any{"experiment": experimentView(exp)}
				if exp.DirRef != nil {
					out["dir_ref"] = *exp.DirRef
				}
				return okOut(out)
			}),

		tool(mod, mcp.CategoryWrite, "experiment_update", "更新实验",
			"更新实验标题、目的或扫描规则。",
			sObj(map[string]any{
				"project_id":    sStr("项目 ID"),
				"experiment_id": sStr("实验 ID"),
				"title":         sStr("标题"),
				"purpose":       sStr("实验目的"),
				"scan_rules":    sFreeObj("扫描规则"),
			}, "project_id", "experiment_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, expID, err := projectAndID(a, "experiment_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				in := experiments.UpdateExperimentInput{ID: expID}
				in.Title = optionalStringPtr(a, "title")
				in.Purpose = optionalStringPtr(a, "purpose")
				if in.Title == nil && in.Purpose == nil && !a.Has("scan_rules") {
					return mcp.ToolOutcome{}, invalidf("至少提供 title、purpose 或 scan_rules")
				}
				svc, err := d.experimentService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				var exp *experiments.Experiment
				if in.Title != nil || in.Purpose != nil {
					exp, err = svc.Experiments().Update(ctx, in)
					if err != nil {
						return mcp.ToolOutcome{}, mapExperimentsError(err)
					}
				}
				if a.Has("scan_rules") {
					rules, err := scanRulesArg(a)
					if err != nil {
						return mcp.ToolOutcome{}, err
					}
					if rules != nil {
						if err := svc.SetScanRules(ctx, expID, *rules); err != nil {
							return mcp.ToolOutcome{}, mapExperimentsError(err)
						}
					}
				}
				if exp == nil {
					current, err := svc.Experiments().Get(ctx, expID)
					if err != nil {
						return mcp.ToolOutcome{}, mapExperimentsError(err)
					}
					exp = &current.Experiment
				}
				return okOut(map[string]any{"experiment": experimentView(exp)})
			}),

		tool(mod, mcp.CategoryWrite, "experiment_delete", "删除实验",
			"软删除实验（进入回收站）。",
			sObj(map[string]any{
				"project_id":    sStr("项目 ID"),
				"experiment_id": sStr("实验 ID"),
			}, "project_id", "experiment_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, expID, err := projectAndID(a, "experiment_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.experimentService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if err := svc.Experiments().SoftDelete(ctx, expID, eventActor()); err != nil {
					return mcp.ToolOutcome{}, mapExperimentsError(err)
				}
				return okOut(map[string]any{"deleted": true})
			}),

		tool(mod, mcp.CategoryWrite, "experiment_relink_dir", "重新绑定实验目录",
			"把实验重新定位到已存在的绝对目录（修复 broken 引用，不清空旧快照）。",
			sObj(map[string]any{
				"project_id":    sStr("项目 ID"),
				"experiment_id": sStr("实验 ID"),
				"dir_ref":       sStr("目录绝对路径"),
			}, "project_id", "experiment_id", "dir_ref"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, expID, err := projectAndID(a, "experiment_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				dir, err := requireString(a, "dir_ref")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if !filepath.IsAbs(dir) {
					return mcp.ToolOutcome{}, invalidf("dir_ref 必须是绝对路径")
				}
				svc, err := d.experimentService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				ref, err := svc.Experiments().RelocateDirectory(ctx, expID, dir, eventActor())
				if err != nil {
					return mcp.ToolOutcome{}, mapExperimentsError(err)
				}
				return okOut(map[string]any{"experiment_id": expID, "dir_status": asJSON(ref)})
			}),

		tool(mod, mcp.CategoryJob, "experiment_scan", "扫描实验目录",
			"只读扫描实验目录并返回文件清单与排除项（不落库）。返回 job_id。",
			sObj(map[string]any{
				"project_id":    sStr("项目 ID"),
				"experiment_id": sStr("实验 ID"),
				"refresh":       sBool("预留：当前扫描总是重新计算"),
			}, "project_id", "experiment_id"), annJob(true),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, expID, err := projectAndID(a, "experiment_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.experimentService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				params, _ := json.Marshal(map[string]any{"experiment_id": expID})
				job, err := d.Jobs.Submit(ctx, p, mcp.JobRequest{
					Tool:      "experiment_scan",
					ProjectID: projectID,
					ClientID:  p.ClientID,
					ReadOnly:  true,
					Params:    params,
					TraceID:   mcp.TraceIDFrom(ctx),
					Run: func(jctx context.Context, progress mcp.ProgressFunc) (any, error) {
						progress(20, "读取实验目录")
						plan, err := svc.Scanning().Plan(jctx, experiments.TargetRef{Type: experiments.TargetExperiment, ID: expID}, nil)
						if err != nil {
							return nil, mapExperimentsError(err)
						}
						progress(90, "整理文件清单")
						files := make([]any, 0, len(plan.Entries))
						for _, entry := range plan.Entries {
							files = append(files, map[string]any{
								"path":  entry.Path,
								"kind":  string(entry.Kind),
								"bytes": entry.Size,
								"mtime": entry.MTime,
								"hash":  entry.SHA256,
							})
						}
						excluded := make([]any, 0, len(plan.Pruned))
						for _, entry := range plan.Pruned {
							excluded = append(excluded, map[string]any{"path": entry.Path, "reason": string(entry.Reason)})
						}
						return map[string]any{
							"files":     files,
							"excluded":  excluded,
							"truncated": plan.Truncated,
						}, nil
					},
				})
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				return jobOut(job)
			}),

		tool(mod, mcp.CategoryJob, "experiment_generate_outline", "生成实验提纲",
			"用 AI 生成实验提纲草稿并保存为快照（不覆盖旧快照）。返回 job_id。",
			sObj(map[string]any{
				"project_id":    sStr("项目 ID"),
				"experiment_id": sStr("实验 ID"),
			}, "project_id", "experiment_id"), annJob(false),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, expID, err := projectAndID(a, "experiment_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if d.AI == nil {
					return mcp.ToolOutcome{}, mcp.Errorf(mcp.CodeAIUnavailable, "AI 服务未配置，无法生成实验提纲")
				}
				svc, err := d.experimentService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				params, _ := json.Marshal(map[string]any{"experiment_id": expID})
				job, err := d.Jobs.Submit(ctx, p, mcp.JobRequest{
					Tool:      "experiment_generate_outline",
					ProjectID: projectID,
					ClientID:  p.ClientID,
					Params:    params,
					TraceID:   mcp.TraceIDFrom(ctx),
					Run: func(jctx context.Context, progress mcp.ProgressFunc) (any, error) {
						progress(20, "收集实验上下文")
						snapshot, err := svc.Analysis().GenerateOutline(jctx, expID, experiments.AnalysisOptions{})
						if err != nil {
							return nil, mapExperimentsError(err)
						}
						progress(95, "保存快照")
						return map[string]any{
							"snapshot_id": snapshot.ID,
							"outline":     snapshotContent(snapshot),
						}, nil
					},
				})
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				return jobOut(job)
			}),

		tool(mod, mcp.CategoryJob, "experiment_analyze_results", "分析实验结果",
			"用 AI 分析实验结果并保存快照（不覆盖旧快照；指标解析遵循 SPEC-07）。返回 job_id。",
			sObj(map[string]any{
				"project_id":    sStr("项目 ID"),
				"experiment_id": sStr("实验 ID"),
				"run_ids":       sArr("运行 ID（可选）", sStr("运行 ID")),
			}, "project_id", "experiment_id"), annJob(false),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, expID, err := projectAndID(a, "experiment_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if d.AI == nil {
					return mcp.ToolOutcome{}, mcp.Errorf(mcp.CodeAIUnavailable, "AI 服务未配置，无法分析实验结果")
				}
				runIDs, _, err := a.Strings("run_ids")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.experimentService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				params, _ := json.Marshal(map[string]any{"experiment_id": expID, "run_ids": runIDs})
				job, err := d.Jobs.Submit(ctx, p, mcp.JobRequest{
					Tool:      "experiment_analyze_results",
					ProjectID: projectID,
					ClientID:  p.ClientID,
					Params:    params,
					TraceID:   mcp.TraceIDFrom(ctx),
					Run: func(jctx context.Context, progress mcp.ProgressFunc) (any, error) {
						targets := []experiments.TargetRef{{Type: experiments.TargetExperiment, ID: expID}}
						if len(runIDs) > 0 {
							targets = targets[:0]
							for _, id := range runIDs {
								targets = append(targets, experiments.TargetRef{Type: experiments.TargetRun, ID: id})
							}
						}
						snapshots := make([]any, 0, len(targets))
						for i, target := range targets {
							progress(10+80*i/len(targets), "分析 "+itoa(i+1)+"/"+itoa(len(targets)))
							snapshot, err := svc.Analysis().GenerateResults(jctx, target, experiments.AnalysisOptions{})
							if err != nil {
								return nil, mapExperimentsError(err)
							}
							snapshots = append(snapshots, map[string]any{
								"snapshot_id": snapshot.ID,
								"content":     snapshotContent(snapshot),
								"metrics":     asJSON(snapshot.Metrics),
							})
						}
						if len(snapshots) == 1 {
							return snapshots[0], nil
						}
						return map[string]any{"snapshots": snapshots}, nil
					},
				})
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				return jobOut(job)
			}),

		tool(mod, mcp.CategoryRead, "experiment_snapshot_list", "快照列表",
			"列出实验或运行的 AI 分析快照。",
			sObj(map[string]any{
				"project_id":    sStr("项目 ID"),
				"experiment_id": sStr("实验 ID"),
				"run_id":        sStr("运行 ID（可选，替代 experiment_id 目标）"),
			}, "project_id", "experiment_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, expID, err := projectAndID(a, "experiment_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				target := experiments.TargetRef{Type: experiments.TargetExperiment, ID: expID}
				if runID, ok := a.String("run_id"); ok && runID != "" {
					target = experiments.TargetRef{Type: experiments.TargetRun, ID: runID}
				}
				svc, err := d.experimentService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				list, err := svc.Snapshots().List(ctx, target, experiments.SnapshotFilter{})
				if err != nil {
					return mcp.ToolOutcome{}, mapExperimentsError(err)
				}
				return okOut(map[string]any{"snapshots": asJSON(list)})
			}),

		tool(mod, mcp.CategoryRead, "experiment_snapshot_get", "快照详情",
			"返回快照的文件清单、Git 信息、指标、AI 分析与模型信息。",
			sObj(map[string]any{
				"project_id":  sStr("项目 ID"),
				"snapshot_id": sStr("快照 ID"),
			}, "project_id", "snapshot_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, snapshotID, err := projectAndID(a, "snapshot_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.experimentService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				snapshot, err := svc.Snapshots().Get(ctx, snapshotID)
				if err != nil {
					return mcp.ToolOutcome{}, mapExperimentsError(err)
				}
				return okOut(map[string]any{"snapshot": asJSON(snapshot)})
			}),

		tool(mod, mcp.CategoryRead, "experiment_git_info", "实验 Git 信息",
			"只读检查实验或运行目录的 Git 仓库信息（分支、提交、脏状态）。",
			sObj(map[string]any{
				"project_id":    sStr("项目 ID"),
				"experiment_id": sStr("实验 ID"),
				"run_id":        sStr("运行 ID（可选）"),
			}, "project_id", "experiment_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, expID, err := projectAndID(a, "experiment_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				runID, _ := a.String("run_id")
				svc, err := d.experimentService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				dir, err := experimentDir(ctx, svc, expID, runID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				info, err := svc.Git().Inspect(ctx, dir)
				if err != nil {
					return mcp.ToolOutcome{}, mapExperimentsError(err)
				}
				return okOut(map[string]any{
					"repo":        info.RepoRoot,
					"branch":      info.Branch,
					"commit":      info.Commit,
					"dirty":       info.Dirty,
					"captured_at": info.CapturedAt,
					"detected":    info.Detected,
				})
			}),

		tool(mod, mcp.CategoryRead, "run_list", "运行列表",
			"列出实验下的运行。",
			sObj(map[string]any{
				"project_id":    sStr("项目 ID"),
				"experiment_id": sStr("实验 ID"),
			}, "project_id", "experiment_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, expID, err := projectAndID(a, "experiment_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.experimentService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				runs, err := svc.Runs().List(ctx, expID, false)
				if err != nil {
					return mcp.ToolOutcome{}, mapExperimentsError(err)
				}
				return okOut(map[string]any{"runs": asJSON(runs)})
			}),

		tool(mod, mcp.CategoryWrite, "run_create", "创建运行",
			"在实验下创建运行，可指定目录与参数。",
			sObj(map[string]any{
				"project_id":    sStr("项目 ID"),
				"experiment_id": sStr("实验 ID"),
				"title":         sStrLen("标题", 1, 200),
				"dir_ref":       sStr("运行目录绝对路径"),
				"params":        sFreeObj("参数 {name: {value, source?, confirmed?}}"),
			}, "project_id", "experiment_id", "title"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, expID, err := projectAndID(a, "experiment_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if _, err := requireString(a, "title"); err != nil {
					return mcp.ToolOutcome{}, err
				}
				params, err := paramMap(a)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				dir, _ := a.String("dir_ref")
				svc, err := d.experimentService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				run, err := svc.Runs().Create(ctx, experiments.CreateRunInput{
					ExperimentID: expID,
					DirPath:      dir,
					Params:       params,
					Actor:        eventActor(),
				})
				if err != nil {
					return mcp.ToolOutcome{}, mapExperimentsError(err)
				}
				out := map[string]any{"run": asJSON(run)}
				return okOut(out)
			}),

		tool(mod, mcp.CategoryWrite, "run_update", "更新运行",
			"单次调用只允许一组变更（params、status 或 dir_ref 之一）。状态仅接受 SPEC-07 定义的合法值，不做完成推断。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"run_id":     sStr("运行 ID"),
				"params":     sFreeObj("参数 {name: {value, source?, confirmed?}}"),
				"status":     sEnum("运行状态", "unknown", "running", "finished", "failed"),
				"dir_ref":    sStr("运行目录绝对路径"),
			}, "project_id", "run_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, runID, err := projectAndID(a, "run_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				groups := 0
				if a.Has("params") {
					groups++
				}
				status, hasStatus := a.String("status")
				if hasStatus {
					groups++
				}
				dir, hasDir := a.String("dir_ref")
				if hasDir {
					groups++
				}
				if groups != 1 {
					return mcp.ToolOutcome{}, invalidf("params、status、dir_ref 每次只能提交一组")
				}
				svc, err := d.experimentService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				switch {
				case a.Has("params"):
					params, err := paramMap(a)
					if err != nil {
						return mcp.ToolOutcome{}, err
					}
					if err := svc.Runs().UpdateParams(ctx, runID, params); err != nil {
						return mcp.ToolOutcome{}, mapExperimentsError(err)
					}
				case hasStatus:
					if err := svc.Runs().SetStatus(ctx, runID, experiments.RunStatus(status)); err != nil {
						return mcp.ToolOutcome{}, mapExperimentsError(err)
					}
				case hasDir:
					if !filepath.IsAbs(dir) {
						return mcp.ToolOutcome{}, invalidf("dir_ref 必须是绝对路径")
					}
					if err := svc.Runs().SetDirectory(ctx, runID, dir); err != nil {
						return mcp.ToolOutcome{}, mapExperimentsError(err)
					}
				}
				detail, err := svc.Runs().Get(ctx, runID)
				if err != nil {
					return mcp.ToolOutcome{}, mapExperimentsError(err)
				}
				return okOut(map[string]any{"run": asJSON(detail)})
			}),

		tool(mod, mcp.CategoryWrite, "run_delete", "删除运行",
			"软删除运行（进入回收站）。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"run_id":     sStr("运行 ID"),
			}, "project_id", "run_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, runID, err := projectAndID(a, "run_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.experimentService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if err := svc.Runs().SoftDelete(ctx, runID, eventActor()); err != nil {
					return mcp.ToolOutcome{}, mapExperimentsError(err)
				}
				return okOut(map[string]any{"deleted": true})
			}),
	}
	return &provider{module: mod, tools: tools, ready: ready}
}

func experimentDir(ctx context.Context, svc *experiments.Service, experimentID, runID string) (string, error) {
	if runID != "" {
		detail, err := svc.Runs().Get(ctx, runID)
		if err != nil {
			return "", mapExperimentsError(err)
		}
		if len(detail.DirRefs) > 0 && detail.DirRefs[0].Exists {
			return detail.DirRefs[0].Path, nil
		}
		return "", mcp.Errorf(mcp.CodeNotFound, "运行没有可用的目录绑定")
	}
	detail, err := svc.Experiments().Get(ctx, experimentID)
	if err != nil {
		return "", mapExperimentsError(err)
	}
	if len(detail.DirRefs) > 0 && detail.DirRefs[0].Exists {
		return detail.DirRefs[0].Path, nil
	}
	return "", mcp.Errorf(mcp.CodeNotFound, "实验没有可用的目录绑定")
}

func scanRulesArg(a *mcp.Arguments) (*experiments.ScanRules, error) {
	if !a.Has("scan_rules") {
		return nil, nil
	}
	obj, ok := a.Object("scan_rules")
	if !ok {
		return nil, invalidf("scan_rules 必须是对象")
	}
	body, err := json.Marshal(obj)
	if err != nil {
		return nil, invalidf("scan_rules 无效")
	}
	var rules experiments.ScanRules
	if err := json.Unmarshal(body, &rules); err != nil {
		return nil, invalidf("scan_rules 无效")
	}
	return &rules, nil
}

func paramMap(a *mcp.Arguments) (map[string]experiments.ParamValue, error) {
	if !a.Has("params") {
		return nil, nil
	}
	obj, ok := a.Object("params")
	if !ok {
		return nil, invalidf("params 必须是对象")
	}
	body, err := json.Marshal(obj)
	if err != nil {
		return nil, invalidf("params 无效")
	}
	var params map[string]experiments.ParamValue
	if err := json.Unmarshal(body, &params); err != nil {
		return nil, invalidf("params 无效")
	}
	return params, nil
}

func experimentView(e *experiments.Experiment) map[string]any {
	if e == nil {
		return nil
	}
	out := map[string]any{
		"experiment_id": e.ID,
		"title":         e.Title,
		"purpose":       e.Purpose,
		"scan_rules":    asJSON(e.ScanRules),
		"created_at":    ts(e.CreatedAt),
		"updated_at":    ts(e.UpdatedAt),
		"deleted_at":    tsp(e.DeletedAt),
	}
	if e.DirRef != nil {
		out["dir_ref"] = *e.DirRef
	}
	return out
}

func experimentDetailView(d *experiments.ExperimentDetail) map[string]any {
	if d == nil {
		return nil
	}
	dirRefs := make([]any, 0, len(d.DirRefs))
	for _, ref := range d.DirRefs {
		dirRefs = append(dirRefs, asJSON(ref))
	}
	runs := make([]any, 0, len(d.Runs))
	for i := range d.Runs {
		runs = append(runs, asJSON(d.Runs[i]))
	}
	return map[string]any{
		"experiment":       experimentView(&d.Experiment),
		"dir_refs":         dirRefs,
		"runs":             runs,
		"recent_snapshots": asJSON(d.RecentSnapshots),
		"linked_ideas":     asJSON(d.LinkedIdeas),
	}
}

func experimentSummaryView(e *experiments.ExperimentSummary) map[string]any {
	if e == nil {
		return nil
	}
	return map[string]any{
		"experiment_id":    e.ID,
		"title":            e.Title,
		"purpose":          e.Purpose,
		"dir_status":       string(e.DirStatus),
		"dir_reason":       e.DirReason,
		"run_count":        e.RunCount,
		"last_run_status":  string(e.LastRunStatus),
		"last_analyzed_at": tsp(e.LastAnalyzedAt),
		"linked_ideas":     e.LinkedIdeas,
		"updated_at":       ts(e.UpdatedAt),
		"deleted_at":       tsp(e.DeletedAt),
	}
}

func snapshotContent(s *experiments.Snapshot) string {
	if s == nil || s.AIAnalysis == nil {
		return ""
	}
	return s.AIAnalysis.ContentMD
}

// mapExperimentsError maps SPEC-07 sentinels to MCP codes.
func mapExperimentsError(err error) error {
	if err == nil {
		return nil
	}
	var me *mcp.Error
	if errors.As(err, &me) {
		return me
	}
	switch {
	case errors.Is(err, experiments.ErrNotFound):
		return mcp.Errorf(mcp.CodeNotFound, "实验、运行或快照不存在").Wrap(err)
	case errors.Is(err, experiments.ErrReadOnlyProject):
		return mcp.Errorf(mcp.CodeArchivedReadOnly, "项目已归档，不能写入").Wrap(err)
	case errors.Is(err, experiments.ErrAIUnavailable):
		return mcp.Errorf(mcp.CodeAIUnavailable, "AI 服务未配置").Wrap(err)
	case errors.Is(err, experiments.ErrDirUnavailable), errors.Is(err, experiments.ErrDirGuard):
		return mcp.Errorf(mcp.CodeInvalidArgument, "目录不可用或不允许使用工作区内部路径").Wrap(err)
	case errors.Is(err, experiments.ErrInvalidInput):
		return mcp.Errorf(mcp.CodeInvalidArgument, "参数不合法").Wrap(err)
	case errors.Is(err, experiments.ErrNoContent):
		return mcp.Errorf(mcp.CodeInvalidArgument, "没有可用于分析的内容").Wrap(err)
	case errors.Is(err, experiments.ErrInvalidAIOutput):
		return mcp.Errorf(mcp.CodeAIUnavailable, "AI 输出未通过校验，可稍后重试").Wrap(err)
	}
	return asMCPerror(err, mcp.CodeInternal, "实验模块执行失败")
}
