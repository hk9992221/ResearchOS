package mcpbind

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"researchos/internal/foundation/config"
	"researchos/internal/foundation/indexhook"
	"researchos/internal/literature"
	"researchos/internal/literature/library"
	"researchos/internal/literature/zotero"
	"researchos/internal/mcp"
)

// libraryService returns the cached SPEC-04 service. Zotero may be unconfigured;
// the read paths that do not need it keep working.
func (d *Deps) libraryService() *library.Service {
	if d.Infra == nil {
		d.Infra = &Infra{}
	}
	d.Infra.mu.Lock()
	defer d.Infra.mu.Unlock()
	if d.Infra.lib != nil {
		return d.Infra.lib
	}
	c := d.clock()
	var conn *zotero.Connection
	var sqlite *zotero.SQLiteReader
	var api *zotero.LocalAPIReader
	if d.Workspace != nil && d.Workspace.Global() != nil {
		conn = zotero.NewConnection(zotero.Options{AppConfig: d.Workspace.Config(), Clock: c})
		api = conn.NewAPIReader()
		if status, err := conn.Status(context.Background()); err == nil && status.DataDir != nil {
			// A dedicated resolver keeps attachment paths resolvable even when
			// the SQLite handle is unavailable (live library, locked database).
			if api != nil {
				api.WithResolver(zotero.Resolver{
					DataDir:       status.DataDir.Path,
					LinkedBaseDir: conn.LinkedAttachmentBaseDir(),
				})
			}
			// A locked database is not fatal: the local API may still answer.
			if reader, err := zotero.NewSQLiteReader(status.DataDir.Path); err == nil {
				sqlite = reader
			}
		}
	}
	source := zotero.NewSource(conn, sqlite, api)
	d.Infra.source = source
	// The imported paper is indexed post-commit (SPEC-10 §5.3). Without the
	// search module the hook stays nil and the index is simply not updated.
	var indexer indexhook.Indexer
	if d.Search != nil {
		indexer = d.Search
	}
	d.Infra.lib = library.New(d.Workspace, library.Options{Clock: c, Zotero: source, Indexer: indexer})
	return d.Infra.lib
}

// zoteroReader returns the Zotero metadata reader (local API first, SQLite
// fallback) for the read-only browsing tools.
func (d *Deps) zoteroReader(ctx context.Context) (literature.ZoteroReader, error) {
	d.libraryService()
	d.Infra.mu.Lock()
	source := d.Infra.source
	d.Infra.mu.Unlock()
	if source == nil {
		return nil, literature.ErrZoteroNotConfigured
	}
	return source.Reader(ctx)
}

// zoteroConnection builds the SPEC-04 connection service. The manual data
// directory comes from the application configuration, the same value the
// settings page writes, never from workspace settings.
func (d *Deps) zoteroConnection() *zotero.Connection {
	c := d.clock()
	var appConfig config.Store
	if d.Workspace != nil {
		appConfig = d.Workspace.Config()
	}
	return zotero.NewConnection(zotero.Options{AppConfig: appConfig, Clock: c})
}

// newLiteratureProvider builds the SPEC-04 literature domain (§5.3.2).
func newLiteratureProvider(d Deps, ready func(context.Context) error) mcp.ToolProvider {
	mod := mcp.ModuleLiterature
	tools := []mcp.ToolBinding{
		tool(mod, mcp.CategoryRead, "zotero_status", "Zotero 状态",
			"返回 Zotero 连接状态：数据目录、metadata 来源（本地 API/SQLite）与最近错误。",
			sObj(nil), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				conn := d.zoteroConnection()
				status, err := conn.Status(ctx)
				if err != nil {
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				out := map[string]any{
					"connected": status.DataDir != nil,
					"api_mode":  "sqlite",
					"local_api": map[string]any{
						"available": status.LocalAPI.Available,
						"base_url":  status.LocalAPI.BaseURL,
					},
					"checked_at": ts(status.CheckedAt),
				}
				if status.DataDir != nil {
					out["data_dir"] = status.DataDir.Path
					out["zotero_version"] = status.DataDir.ZoteroVersion
				}
				if status.LocalAPI.Available {
					out["api_mode"] = "local_api"
				}
				if status.LocalAPI.Error != "" {
					out["last_error"] = status.LocalAPI.Error
				}
				return okOut(out)
			}),

		tool(mod, mcp.CategoryRead, "zotero_libraries", "Zotero 文库列表",
			"列出 Zotero 文库。",
			sObj(nil), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				reader, err := d.zoteroReader(ctx)
				if err != nil {
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				libs, err := reader.ListLibraries(ctx)
				if err != nil {
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				return okOut(map[string]any{"libraries": asJSON(libs)})
			}),

		tool(mod, mcp.CategoryRead, "zotero_collections", "Zotero 分类列表",
			"列出 Zotero 文库下的分类。",
			sObj(map[string]any{
				"library_id": sStr("文库 ID，默认 0"),
				"parent_key": sStr("父分类 key（可选）"),
			}), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				reader, err := d.zoteroReader(ctx)
				if err != nil {
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				libraryID, _ := a.String("library_id")
				if libraryID == "" {
					libraryID = "0"
				}
				cols, err := reader.ListCollections(ctx, libraryID)
				if err != nil {
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				if parent, ok := a.String("parent_key"); ok && parent != "" {
					filtered := make([]literature.ZoteroCollection, 0, len(cols))
					for _, c := range cols {
						if c.ParentKey == parent {
							filtered = append(filtered, c)
						}
					}
					cols = filtered
				}
				return okOut(map[string]any{"collections": asJSON(cols)})
			}),

		tool(mod, mcp.CategoryRead, "zotero_items", "Zotero 条目列表",
			"浏览 Zotero 条目（导入前预览，不创建任何数据）。",
			sObj(map[string]any{
				"library_id":     sStr("文库 ID，默认 0"),
				"collection_key": sStr("分类 key"),
				"q":              sStr("查询关键字"),
				"limit":          sInt("返回条数，默认 50，上限 100", 1, 100),
				"cursor":         sStr("上一页返回的 next_cursor"),
			}), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				reader, err := d.zoteroReader(ctx)
				if err != nil {
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				libraryID, _ := a.String("library_id")
				if libraryID == "" {
					libraryID = "0"
				}
				collection, _ := a.String("collection_key")
				offset, err := cursorOffset(stringsOf(a, "cursor"))
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				limit := limitOf(a, 50, 100)
				page, err := reader.ListItems(ctx, libraryID, collection, literature.ListItemsOptions{
					Offset: offset,
					Limit:  limit,
					Query:  stringsOf(a, "q"),
				})
				if err != nil {
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				items := make([]any, 0, len(page.Items))
				for _, item := range page.Items {
					hasPDF := false
					if atts, err := reader.ListAttachments(ctx, libraryID, item.Key); err == nil {
						for _, att := range atts {
							if att.ContentType == "application/pdf" || strings.HasSuffix(strings.ToLower(att.Filename), ".pdf") {
								hasPDF = true
								break
							}
						}
					}
					items = append(items, map[string]any{
						"key":      item.Key,
						"title":    item.Title,
						"creators": asJSON(item.Creators),
						"year":     literatureYear(item.Date),
						"has_pdf":  hasPDF,
					})
				}
				return okOut(pageOf(items, nextOffsetCursor(offset, limit, page.Total)))
			}),

		tool(mod, mcp.CategoryRead, "paper_list", "文献列表",
			"列出全局文献，或某项目关联的文献。",
			sObj(map[string]any{
				"project_id":     sStr("项目 ID（可选；提供时列出项目关联文献）"),
				"q":              sStr("标题/作者关键字"),
				"imported_after": sStr("导入时间下界 RFC3339（实现不支持时忽略）"),
				"limit":          sInt("返回条数，默认 50，上限 200", 1, 200),
				"cursor":         sStr("上一页返回的 next_cursor"),
			}), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				offset, err := cursorOffset(stringsOf(a, "cursor"))
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				limit := limitOf(a, 50, 200)
				filter := literature.PaperFilter{Query: stringsOf(a, "q"), Offset: offset, Limit: limit}
				svc := d.libraryService()
				if projectID, ok := a.String("project_id"); ok && projectID != "" {
					page, err := svc.QueryProjectPapers(ctx, projectID, filter)
					if err != nil {
						return mcp.ToolOutcome{}, mapLiteratureError(err)
					}
					items := make([]any, 0, len(page.Items))
					for _, pp := range page.Items {
						view := paperView(pp.Paper, d)
						view["linked_at"] = ts(pp.LinkedAt)
						view["note_id"] = pp.NoteID
						view["broken"] = pp.Broken
						items = append(items, view)
					}
					return okOut(pageOf(items, nextOffsetCursor(offset, limit, page.Total)))
				}
				page, err := svc.ListPapers(ctx, filter)
				if err != nil {
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				items := make([]any, 0, len(page.Items))
				for _, paper := range page.Items {
					items = append(items, paperView(paper, d))
				}
				return okOut(pageOf(items, nextOffsetCursor(offset, limit, page.Total)))
			}),

		tool(mod, mcp.CategoryRead, "paper_get", "文献详情",
			"返回文献元数据、文件状态与关联项目、Paper Note。",
			sObj(map[string]any{"paper_id": sStr("文献 ID")}, "paper_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				paperID, err := requireID(a, "paper_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc := d.libraryService()
				paper, err := svc.GetPaper(ctx, paperID)
				if err != nil {
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				projects, err := svc.ListPaperProjects(ctx, paperID)
				if err != nil {
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				notes, err := svc.PaperNotes().ListByPaper(ctx, paperID)
				if err != nil {
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				out := paperView(paper, d)
				out["projects"] = asJSON(projects)
				out["notes"] = asJSON(notes)
				return okOut(out)
			}),

		tool(mod, mcp.CategoryWrite, "paper_link", "关联文献到项目",
			"把全局文献关联到项目（只增删项目关联，不动全局副本）。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"paper_id":   sStr("文献 ID"),
			}, "project_id", "paper_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, paperID, err := projectAndID(a, "paper_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				res, err := d.libraryService().Link(ctx, projectID, paperID, literature.ActorMCP)
				if err != nil {
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				return okOut(map[string]any{"linked": true, "added": res.Added, "broken": res.Broken})
			}),

		tool(mod, mcp.CategoryWrite, "paper_unlink", "解除文献关联",
			"解除项目与文献的关联。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"paper_id":   sStr("文献 ID"),
			}, "project_id", "paper_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, paperID, err := projectAndID(a, "paper_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				res, err := d.libraryService().Unlink(ctx, projectID, paperID, literature.ActorMCP)
				if err != nil {
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				return okOut(map[string]any{"unlinked": true, "detached_notes": res.DetachedNotes})
			}),

		tool(mod, mcp.CategoryJob, "paper_import", "导入文献",
			"从 Zotero 批量导入文献到项目（按 zotero_key 去重，固定复制 PDF）。条目与分类可同时给出，合并去重。返回 job_id。",
			sObj(map[string]any{
				"project_id":             sStr("项目 ID"),
				"library_id":             sStr("Zotero 文库 ID，默认 0"),
				"collection_keys":        sArr("分类 key；与 item_keys 合并去重", sStr("分类 key")),
				"include_subcollections": sBool("分类导入是否包含子分类，默认 false"),
				"item_keys":              sArr("条目 key", sStr("条目 key")),
				"attachment_choices": sArr("多 PDF 条目的附件选择（不选则返回 needs_attachment_choice，不猜测）", sObjMap("一个条目的附件选择", map[string]any{
					"item_key":       sStr("Zotero 条目 key"),
					"attachment_key": sStr("Zotero 附件 key"),
				}, "item_key", "attachment_key")),
				"copy_pdfs": sBool("是否复制 PDF（仅允许 true）"),
			}, "project_id"), annJob(false),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if a.Has("copy_pdfs") && !a.BoolDefault("copy_pdfs", true) {
					return mcp.ToolOutcome{}, invalidf("论文导入固定包含 PDF 副本，不支持 copy_pdfs=false")
				}
				libraryID, _ := a.String("library_id")
				collections, _, err := a.Strings("collection_keys")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				itemKeys, _, err := a.Strings("item_keys")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				choices, err := literatureAttachmentChoices(a)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				includeSub := a.BoolDefault("include_subcollections", false)
				svc := d.libraryService()
				params, _ := json.Marshal(map[string]any{
					"project_id":             projectID,
					"library_id":             libraryID,
					"collection_keys":        collections,
					"include_subcollections": includeSub,
					"item_keys":              itemKeys,
					"attachment_choices":     choices,
				})
				job, err := d.Jobs.Submit(ctx, p, mcp.JobRequest{
					Tool:      "paper_import",
					ProjectID: projectID,
					ClientID:  p.ClientID,
					Params:    params,
					TraceID:   mcp.TraceIDFrom(ctx),
					Run: func(jctx context.Context, progress mcp.ProgressFunc) (any, error) {
						progress(5, "读取 Zotero 元数据")
						sel := literature.ImportSelection{
							ProjectID:             projectID,
							LibraryID:             libraryID,
							CollectionKeys:        collections,
							IncludeSubcollections: includeSub,
							ItemKeys:              itemKeys,
							AttachmentChoices:     choices,
						}
						res, err := svc.Import(jctx, sel, literature.ActorMCP, func(ip literature.ImportProgress) {
							if ip.Total > 0 {
								progress(10+85*ip.Index/ip.Total, "写入 "+itoa(ip.Index)+"/"+itoa(ip.Total))
							}
						})
						if err != nil {
							return nil, mapLiteratureError(err)
						}
						buckets := map[string][]any{
							"imported": {}, "reused": {}, "skipped": {}, "needs_attachment_choice": {}, "failed": {},
						}
						for _, outcome := range res.Outcomes {
							entry := map[string]any{
								"key":      outcome.ZoteroKey,
								"paper_id": outcome.PaperID,
								"status":   string(outcome.Status),
								"warnings": outcome.Warnings,
							}
							switch outcome.Status {
							case literature.ImportFailed:
								entry["error"] = outcome.Error
								buckets["failed"] = append(buckets["failed"], entry)
							case literature.ImportNeedsAttachment:
								buckets["needs_attachment_choice"] = append(buckets["needs_attachment_choice"], entry)
							case literature.ImportSkippedNoPDF, literature.ImportSkippedDuplicate:
								buckets["skipped"] = append(buckets["skipped"], entry)
							case literature.ImportLinkedExisting, literature.ImportCreated:
								if outcome.Error != "" {
									entry["error"] = outcome.Error
									buckets["failed"] = append(buckets["failed"], entry)
									continue
								}
								if outcome.Status == literature.ImportLinkedExisting {
									buckets["reused"] = append(buckets["reused"], entry)
									continue
								}
								buckets["imported"] = append(buckets["imported"], entry)
							}
						}
						return map[string]any{
							"imported":    buckets["imported"],
							"reused":      buckets["reused"],
							"skipped":     buckets["skipped"],
							"unsatisfied": buckets["needs_attachment_choice"],
							"failed":      buckets["failed"],
							"counts": map[string]any{
								"imported": res.Imported,
								"linked":   res.Linked,
								"skipped":  res.Skipped,
								"failed":   res.Failed,
							},
						}, nil
					},
				})
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				return jobOut(job)
			}),

		tool(mod, mcp.CategoryJob, "paper_sync_check", "检查文献同步差异",
			"只读比对 Zotero 上游与本地副本（不写数据、不覆盖）。返回 job_id。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID（可选）"),
				"paper_ids":  sArr("文献 ID（可选）", sStr("文献 ID")),
			}), annJob(true),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, _ := a.String("project_id")
				paperIDs, _, err := a.Strings("paper_ids")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if projectID == "" && len(paperIDs) == 0 {
					return mcp.ToolOutcome{}, invalidf("必须提供 project_id 或 paper_ids")
				}
				svc := d.libraryService()
				params, _ := json.Marshal(map[string]any{"project_id": projectID, "paper_ids": paperIDs})
				job, err := d.Jobs.Submit(ctx, p, mcp.JobRequest{
					Tool:      "paper_sync_check",
					ProjectID: projectID,
					ClientID:  p.ClientID,
					ReadOnly:  true,
					Params:    params,
					TraceID:   mcp.TraceIDFrom(ctx),
					Run: func(jctx context.Context, progress mcp.ProgressFunc) (any, error) {
						progress(20, "比对哈希")
						report, err := svc.Sync().Diff(jctx, literature.SyncScope{ProjectID: projectID, PaperIDs: paperIDs}, true)
						if err != nil {
							return nil, mapLiteratureError(err)
						}
						items := make([]any, 0, len(report.Diffs))
						for _, diff := range report.Diffs {
							items = append(items, map[string]any{
								"paper_id":      diff.PaperID,
								"state":         syncState(diff),
								"changed":       diff.Changed,
								"old_hash":      diff.LocalHash,
								"new_hash":      diff.SourceHash,
								"upstream_path": diff.SourcePath,
								"local_size":    diff.LocalSize,
								"source_size":   diff.SourceSize,
								"warnings":      diff.Warnings,
							})
						}
						return map[string]any{
							"items":   items,
							"checked": len(report.Diffs),
							"errors":  asJSON(report.Errors),
						}, nil
					},
				})
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				return jobOut(job)
			}),

		tool(mod, mcp.CategoryConfirm, "paper_sync_apply", "确认覆盖本地文献副本",
			"请求用 Zotero 上游文件覆盖一篇文献的本地副本。工具只入队并冻结差异，必须在应用内逐篇人工批准；执行前重新校验上游哈希，变化则冲突不覆盖。",
			sObj(map[string]any{
				"project_id":        sStr("项目 ID"),
				"paper_id":          sStr("文献 ID"),
				"expected_new_hash": sStr("批准时所基于的上游哈希"),
			}, "project_id", "paper_id", "expected_new_hash"), annConfirm(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, paperID, err := projectAndID(a, "paper_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				expected, err := requireString(a, "expected_new_hash")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc := d.libraryService()
				report, err := svc.Sync().Diff(ctx, literature.SyncScope{ProjectID: projectID, PaperIDs: []string{paperID}}, true)
				if err != nil {
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				var diff *literature.SyncDiff
				for i := range report.Diffs {
					if report.Diffs[i].PaperID == paperID {
						diff = &report.Diffs[i]
						break
					}
				}
				if diff == nil || !diff.Changed {
					return okOut(map[string]any{"paper_id": paperID, "changed": false})
				}
				impact := map[string]any{
					"paper_id":          paperID,
					"old_hash":          diff.LocalHash,
					"new_hash":          diff.SourceHash,
					"old_bytes":         diff.LocalSize,
					"new_bytes":         diff.SourceSize,
					"expected_new_hash": expected,
					"upstream_path":     diff.SourcePath,
					"source_state":      diff.SourceState,
					"local_state":       diff.LocalState,
				}
				return pendingOut("覆盖本地文献副本", "用 Zotero 上游文件覆盖论文 "+paperID+" 的本地副本（旧哈希 "+shortHash(diff.LocalHash)+" → 新哈希 "+shortHash(diff.SourceHash)+"）。", impact, projectID)
			}),

		tool(mod, mcp.CategoryJob, "paper_refresh_metadata", "刷新文献元数据",
			"只刷新文献元数据，不触碰 PDF 副本。返回 job_id。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID（可选）"),
				"paper_ids":  sArr("文献 ID（可选）", sStr("文献 ID")),
			}), annJob(false),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, _ := a.String("project_id")
				paperIDs, _, err := a.Strings("paper_ids")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if projectID == "" && len(paperIDs) == 0 {
					return mcp.ToolOutcome{}, invalidf("必须提供 project_id 或 paper_ids")
				}
				svc := d.libraryService()
				params, _ := json.Marshal(map[string]any{"project_id": projectID, "paper_ids": paperIDs})
				job, err := d.Jobs.Submit(ctx, p, mcp.JobRequest{
					Tool:      "paper_refresh_metadata",
					ProjectID: projectID,
					ClientID:  p.ClientID,
					Params:    params,
					TraceID:   mcp.TraceIDFrom(ctx),
					Run: func(jctx context.Context, progress mcp.ProgressFunc) (any, error) {
						progress(20, "读取 Zotero 元数据")
						res, err := svc.Sync().RefreshMetadata(jctx, literature.SyncScope{ProjectID: projectID, PaperIDs: paperIDs}, literature.ActorMCP)
						if err != nil {
							return nil, mapLiteratureError(err)
						}
						progress(95, "写入元数据")
						return map[string]any{
							"updated":      res.Updated,
							"missing":      res.Missing,
							"failed":       asJSON(res.Failed),
							"refreshed_at": ts(res.RefreshedAt),
						}, nil
					},
				})
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				return jobOut(job)
			}),

		tool(mod, mcp.CategoryRead, "paper_reconcile", "文献一致性诊断",
			"只读诊断文献副本：文件是否存在、哈希是否一致、上游状态。不自动修改。",
			sObj(map[string]any{"paper_id": sStr("文献 ID")}, "paper_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				paperID, err := requireID(a, "paper_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				report, err := d.libraryService().Maintenance().ReconcilePaper(ctx, paperID)
				if err != nil {
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				return okOut(map[string]any{"report": asJSON(report)})
			}),

		tool(mod, mcp.CategoryRead, "paper_note_get", "论文笔记读取",
			"读取论文的 Paper Note（结构化字段、正文、关联）。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"paper_id":   sStr("文献 ID"),
			}, "project_id", "paper_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, paperID, err := projectAndID(a, "paper_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				note, err := d.libraryService().PaperNotes().Get(ctx, projectID, paperID)
				if err != nil {
					if errors.Is(err, literature.ErrPaperNoteNotFound) {
						return okOut(map[string]any{"note": nil})
					}
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				return okOut(map[string]any{"note": paperNoteView(note)})
			}),

		tool(mod, mcp.CategoryWrite, "paper_note_create", "创建论文笔记",
			"为项目已关联的论文创建 Paper Note。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"paper_id":   sStr("文献 ID"),
			}, "project_id", "paper_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, paperID, err := projectAndID(a, "paper_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				note, err := d.libraryService().PaperNotes().Create(ctx, projectID, paperID, literature.ActorMCP)
				if err != nil {
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				return okOut(map[string]any{"note": paperNoteView(&note)})
			}),

		tool(mod, mcp.CategoryWrite, "paper_note_update", "更新论文笔记字段",
			"更新 Paper Note 的结构化字段或标题（整体替换 fields，未提供则不改）。expected_updated_at 必填。",
			sObj(map[string]any{
				"project_id":          sStr("项目 ID"),
				"note_id":             sStr("笔记 ID"),
				"title":               sStr("标题"),
				"fields":              sFreeObj("结构化字段 {summary, problem, method, contribution, limitations, understanding}"),
				"expected_updated_at": sStr("必填并发令牌 RFC3339"),
			}, "project_id", "note_id", "expected_updated_at"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, noteID, err := projectAndID(a, "note_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				_ = projectID
				expected, err := parseArgTime(a, "expected_updated_at")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if expected == nil {
					return mcp.ToolOutcome{}, invalidf("expected_updated_at 必填")
				}
				patch := literature.PaperNotePatch{Title: optionalStringPtr(a, "title")}
				if a.Has("fields") {
					obj, ok := a.Object("fields")
					if !ok {
						return mcp.ToolOutcome{}, invalidf("fields 必须是对象")
					}
					body, err := json.Marshal(obj)
					if err != nil {
						return mcp.ToolOutcome{}, invalidf("fields 无效")
					}
					var fields literature.PaperNoteFields
					if err := json.Unmarshal(body, &fields); err != nil {
						return mcp.ToolOutcome{}, invalidf("fields 无效")
					}
					patch.Fields = &fields
				}
				if patch.Title == nil && patch.Fields == nil {
					return mcp.ToolOutcome{}, invalidf("至少提供 title 或 fields")
				}
				note, err := d.libraryService().PaperNotes().Update(ctx, noteID, patch, *expected, literature.ActorMCP)
				if err != nil {
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				return okOut(map[string]any{"note": paperNoteView(&note)})
			}),

		tool(mod, mcp.CategoryWrite, "paper_note_save_body", "保存论文笔记正文",
			"以乐观并发保存 Paper Note 正文；base hash 不一致返回 CONFLICT。",
			sObj(map[string]any{
				"project_id":          sStr("项目 ID"),
				"note_id":             sStr("笔记 ID"),
				"body_md":             sStrLen("Markdown 正文", 0, mcp.MaxBodyBytes),
				"expected_updated_at": sStr("必填并发令牌 RFC3339"),
			}, "project_id", "note_id", "body_md", "expected_updated_at"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, noteID, err := projectAndID(a, "note_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				body, err := requireString(a, "body_md")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				expected, err := parseArgTime(a, "expected_updated_at")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if expected == nil {
					return mcp.ToolOutcome{}, invalidf("expected_updated_at 必填")
				}
				_ = projectID
				baseHash, err := d.libraryService().PaperNotes().Update(ctx, noteID, literature.PaperNotePatch{}, *expected, literature.ActorMCP)
				if err != nil {
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				res, err := d.libraryService().PaperNotes().SaveBody(ctx, noteID, body, baseHash.BodyHash, literature.ActorMCP)
				if err != nil {
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				if res.Conflict {
					return mcp.ToolOutcome{}, mcp.Errorf(mcp.CodeConflict, "论文笔记正文已被其他写入修改，请重新读取后再保存").
						WithDetail("disk_hash", res.DiskHash)
				}
				return okOut(map[string]any{
					"note":      paperNoteView(&res.Note),
					"body_hash": res.Note.BodyHash,
				})
			}),

		tool(mod, mcp.CategoryRead, "literature_overview_get", "文献综述读取",
			"读取项目的 Literature Overview（Markdown 正文）。",
			sObj(map[string]any{"project_id": sStr("项目 ID")}, "project_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				ov, err := d.libraryService().Overviews().Get(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				if ov == nil {
					return okOut(map[string]any{"overview": nil})
				}
				return okOut(map[string]any{"overview": map[string]any{
					"note_id":    ov.NoteID,
					"body_md":    ov.BodyMD,
					"body_hash":  ov.BodyHash,
					"updated_at": ts(ov.UpdatedAt),
				}})
			}),

		tool(mod, mcp.CategoryWrite, "literature_overview_update", "保存文献综述",
			"保存 Literature Overview 正文；可带 expected_hash 做乐观并发。",
			sObj(map[string]any{
				"project_id":    sStr("项目 ID"),
				"body_md":       sStrLen("Markdown 正文", 0, mcp.MaxBodyBytes),
				"expected_hash": sStr("读取时的 body_hash（可选）"),
			}, "project_id", "body_md"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				body, err := requireString(a, "body_md")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				base, _ := a.String("expected_hash")
				svc := d.libraryService()
				if base == "" {
					if current, err := svc.Overviews().Get(ctx, projectID); err == nil && current != nil {
						base = current.BodyHash
					}
				}
				res, err := svc.Overviews().Save(ctx, projectID, body, base, literature.ActorMCP)
				if err != nil {
					return mcp.ToolOutcome{}, mapLiteratureError(err)
				}
				if res.Conflict {
					return mcp.ToolOutcome{}, mcp.Errorf(mcp.CodeConflict, "文献综述已被其他写入修改，请重新读取后再保存").
						WithDetail("disk_hash", res.DiskHash)
				}
				return okOut(map[string]any{"overview": map[string]any{
					"note_id":    res.Overview.NoteID,
					"body_md":    res.Overview.BodyMD,
					"body_hash":  res.Overview.BodyHash,
					"updated_at": ts(res.Overview.UpdatedAt),
				}})
			}),
	}
	return &provider{module: mod, tools: tools, ready: ready}
}

func paperView(p literature.Paper, d Deps) map[string]any {
	out := map[string]any{
		"paper_id":    p.ID,
		"zotero_key":  p.ZoteroKey,
		"library_id":  p.ZoteroLibraryID,
		"title":       p.Metadata.Title,
		"authors":     p.Metadata.Authors,
		"year":        p.Metadata.Year,
		"doi":         p.Metadata.DOI,
		"imported_at": ts(p.ImportedAt),
		"updated_at":  ts(p.UpdatedAt),
		"file": map[string]any{
			"path":   p.FilePath,
			"hash":   p.FileHash,
			"exists": fileExists(d, p.FilePath),
		},
	}
	return out
}

func fileExists(d Deps, rel string) bool {
	if rel == "" || d.Workspace == nil {
		return false
	}
	abs, err := d.Workspace.Paths().Abs(rel)
	if err != nil {
		return false
	}
	info, err := os.Stat(abs)
	return err == nil && !info.IsDir()
}

func paperNoteView(n *literature.PaperNote) map[string]any {
	if n == nil {
		return nil
	}
	return map[string]any{
		"note_id":       n.NoteID,
		"project_id":    n.ProjectID,
		"paper_id":      n.PaperID,
		"title":         n.Title,
		"fields":        n.Fields,
		"body_md":       n.BodyMD,
		"body_hash":     n.BodyHash,
		"related_ideas": asJSON(n.RelatedIdeas),
		"related_notes": asJSON(n.RelatedNotes),
		"created_at":    ts(n.CreatedAt),
		"updated_at":    ts(n.UpdatedAt),
		"deleted_at":    tsp(n.DeletedAt),
	}
}

func syncState(d literature.SyncDiff) string {
	switch {
	case d.LocalState == "missing":
		return "missing"
	case d.LocalState == "corrupt":
		return "corrupt"
	case d.Changed:
		return "changed"
	default:
		return "ok"
	}
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// literatureAttachmentChoices reads the per-item PDF choices of an import.
// The service never guesses between several PDFs (§5.4), so a batch that needs
// a choice reports needs_attachment_choice until the client names one.
func literatureAttachmentChoices(a *mcp.Arguments) (map[string]string, error) {
	entries, ok, err := a.Objects("attachment_choices")
	if err != nil || !ok {
		return nil, err
	}
	out := make(map[string]string, len(entries))
	for i, entry := range entries {
		itemKey, _ := entry["item_key"].(string)
		attachmentKey, _ := entry["attachment_key"].(string)
		if itemKey == "" || attachmentKey == "" {
			return nil, invalidf("参数 attachment_choices[%d] 需要 item_key 与 attachment_key", i)
		}
		out[itemKey] = attachmentKey
	}
	return out, nil
}

func literatureYear(date string) int {
	for i := 0; i+4 <= len(date); i++ {
		segment := date[i : i+4]
		year := 0
		valid := true
		for _, r := range segment {
			if r < '0' || r > '9' {
				valid = false
				break
			}
			year = year*10 + int(r-'0')
		}
		if valid && year >= 1000 && year <= 2999 {
			return year
		}
	}
	return 0
}

// mapLiteratureError maps SPEC-04 sentinels to MCP codes.
func mapLiteratureError(err error) error {
	if err == nil {
		return nil
	}
	var me *mcp.Error
	if errors.As(err, &me) {
		return me
	}
	switch {
	case errors.Is(err, literature.ErrPaperNotFound), errors.Is(err, literature.ErrPaperNoteNotFound),
		errors.Is(err, literature.ErrItemNotFound), errors.Is(err, literature.ErrNoteDeleted):
		return mcp.Errorf(mcp.CodeNotFound, "文献或笔记不存在").Wrap(err)
	case errors.Is(err, literature.ErrProjectArchived):
		return mcp.Errorf(mcp.CodeArchivedReadOnly, "项目已归档，不能写入").Wrap(err)
	case errors.Is(err, literature.ErrConfirmationRequired):
		return mcp.Errorf(mcp.CodeCapabilityInvalid, "该操作需要应用内人工确认").Wrap(err)
	case errors.Is(err, literature.ErrSyncReportExpired), errors.Is(err, literature.ErrSyncSourceChanged),
		errors.Is(err, literature.ErrSyncConflict), errors.Is(err, literature.ErrPaperNoteExists),
		errors.Is(err, literature.ErrProjectLinkExists):
		return mcp.Errorf(mcp.CodeConflict, "数据已变化或状态冲突，请重新比对").Wrap(err)
	case errors.Is(err, literature.ErrZoteroNotConfigured), errors.Is(err, literature.ErrZoteroDirInvalid):
		return mcp.Errorf(mcp.CodeExternalUnavail, "Zotero 尚未配置").Wrap(err)
	case errors.Is(err, literature.ErrZoteroUnavailable), errors.Is(err, literature.ErrLocalAPIDisabled),
		errors.Is(err, literature.ErrZoteroBusy):
		return mcp.Errorf(mcp.CodeExternalUnavail, "Zotero 暂不可用").Wrap(err)
	case errors.Is(err, literature.ErrProjectLinkMissing), errors.Is(err, literature.ErrNoPDFAttachment),
		errors.Is(err, literature.ErrAttachmentChoice), errors.Is(err, literature.ErrAttachmentUnresolved),
		errors.Is(err, literature.ErrPathOutsideStorage), errors.Is(err, literature.ErrInvalidPDF):
		return mcp.Errorf(mcp.CodeInvalidArgument, "文献关联或附件参数不合法").Wrap(err)
	case errors.Is(err, literature.ErrBusy):
		return mcp.Errorf(mcp.CodeRateLimited, "另一导入或同步任务正在运行").WithDetail("retry_after_ms", 2000)
	}
	return asMCPerror(err, mcp.CodeInternal, "文献模块执行失败")
}
