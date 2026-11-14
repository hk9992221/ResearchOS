package mcpbind

import (
	"context"
	"encoding/json"
	"errors"

	"researchos/internal/ideas"
	"researchos/internal/mcp"
)

func (d *Deps) ideaService(ctx context.Context, projectID string) (ideas.Service, error) {
	if err := d.projectExists(ctx, projectID); err != nil {
		return nil, err
	}
	opts := ideas.Options{
		Clock:   d.clock(),
		Logger:  d.Logger,
		Sources: ideas.NewSourceResolver(d.Workspace),
	}
	if gen, _, ok := d.ai(ctx, projectID); ok {
		opts.AI = ideas.NewSpec02ChatClient(gen, projectID)
	}
	return ideas.New(d.Workspace, projectID, opts), nil
}

// newIdeasProvider builds the SPEC-06 Idea/Evidence domain (§5.3.4).
func newIdeasProvider(d Deps, ready func(context.Context) error) mcp.ToolProvider {
	mod := mcp.ModuleIdeas
	tools := []mcp.ToolBinding{
		tool(mod, mcp.CategoryRead, "idea_list", "Idea 列表",
			"列出项目 Idea，可按状态、关键字过滤。",
			sObj(map[string]any{
				"project_id":      sStr("项目 ID"),
				"status":          sEnum("状态", "New", "Researching", "Validating", "Supported", "PartiallySupported", "Inconclusive", "Rejected", "OnHold"),
				"q":               sStr("标题/描述关键字"),
				"include_deleted": sBool("包含已软删除的 Idea"),
				"limit":           sInt("返回条数，默认 50，上限 200", 1, 200),
				"cursor":          sStr("上一页返回的 next_cursor"),
			}, "project_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.ideaService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				q := ideas.ListIdeasQuery{
					Text:           stringsOf(a, "q"),
					IncludeDeleted: a.BoolDefault("include_deleted", false),
					PageSize:       limitOf(a, 50, 200),
					Cursor:         stringsOf(a, "cursor"),
				}
				if s, ok := a.String("status"); ok {
					q.Statuses = []ideas.Status{ideas.Status(s)}
				}
				pg, err := svc.ListIdeas(ctx, q)
				if err != nil {
					return mcp.ToolOutcome{}, mapIdeasError(err)
				}
				items := make([]any, 0, len(pg.Items))
				for _, it := range pg.Items {
					items = append(items, ideaSummaryView(it))
				}
				return okOut(pageOf(items, pg.NextCursor))
			}),

		tool(mod, mcp.CategoryRead, "idea_get", "Idea 详情",
			"返回 Idea、证据、关系与近期历史。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"idea_id":    sStr("Idea ID"),
			}, "project_id", "idea_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, ideaID, err := projectAndID(a, "idea_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.ideaService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				detail, err := svc.GetIdea(ctx, ideaID)
				if err != nil {
					return mcp.ToolOutcome{}, mapIdeasError(err)
				}
				evidence, err := svc.ListEvidence(ctx, ideas.ListEvidenceQuery{IdeaID: ideaID, PageSize: 100})
				if err != nil {
					return mcp.ToolOutcome{}, mapIdeasError(err)
				}
				evidenceViews := make([]any, 0, len(evidence.Items))
				for _, ev := range evidence.Items {
					evidenceViews = append(evidenceViews, evidenceView(ev))
				}
				return okOut(map[string]any{
					"idea":      ideaView(&detail.Idea),
					"summary":   asJSON(detail.Evidence),
					"evidence":  evidenceViews,
					"relations": asJSON(detail.Relations),
					"history":   asJSON(detail.HistoryBrief),
				})
			}),

		tool(mod, mcp.CategoryWrite, "idea_create", "创建 Idea",
			"创建 Idea，初始状态恒为 New。",
			sObj(map[string]any{
				"project_id":  sStr("项目 ID"),
				"title":       sStrLen("标题", 1, 200),
				"description": sStr("Markdown 描述"),
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
				desc, _ := a.String("description")
				svc, err := d.ideaService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				idea, err := svc.CreateIdea(ctx, ideas.CreateIdeaInput{Title: title, DescriptionMD: desc, Actor: eventActor()})
				if err != nil {
					return mcp.ToolOutcome{}, mapIdeasError(err)
				}
				return okOut(map[string]any{"idea": ideaView(idea)})
			}),

		tool(mod, mcp.CategoryWrite, "idea_update", "更新 Idea",
			"更新 Idea 标题或描述。提供 expected_updated_at 时做乐观并发检查。",
			sObj(map[string]any{
				"project_id":          sStr("项目 ID"),
				"idea_id":             sStr("Idea ID"),
				"title":               sStr("标题"),
				"description":         sStr("Markdown 描述"),
				"expected_updated_at": sStr("并发令牌 RFC3339"),
			}, "project_id", "idea_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, ideaID, err := projectAndID(a, "idea_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				in := ideas.UpdateIdeaInput{ID: ideaID, Actor: eventActor()}
				in.Title = optionalStringPtr(a, "title")
				in.DescriptionMD = optionalStringPtr(a, "description")
				if in.Title == nil && in.DescriptionMD == nil {
					return mcp.ToolOutcome{}, invalidf("至少提供 title 或 description")
				}
				svc, err := d.ideaService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if expected, ok := a.String("expected_updated_at"); ok && expected != "" {
					current, err := svc.GetIdea(ctx, ideaID)
					if err != nil {
						return mcp.ToolOutcome{}, mapIdeasError(err)
					}
					if ts(current.UpdatedAt) != expected {
						return mcp.ToolOutcome{}, conflictf("Idea 已被修改，请重新读取后再提交").
							WithDetail("current_updated_at", ts(current.UpdatedAt))
					}
				}
				idea, err := svc.UpdateIdea(ctx, in)
				if err != nil {
					return mcp.ToolOutcome{}, mapIdeasError(err)
				}
				return okOut(map[string]any{"idea": ideaView(idea)})
			}),

		tool(mod, mcp.CategoryWrite, "idea_change_status", "变更 Idea 状态",
			"变更 Idea 状态；Rejected 必须提供 rejection_reason。",
			sObj(map[string]any{
				"project_id":       sStr("项目 ID"),
				"idea_id":          sStr("Idea ID"),
				"status":           sEnum("新状态", "New", "Researching", "Validating", "Supported", "PartiallySupported", "Inconclusive", "Rejected", "OnHold"),
				"rejection_reason": sStr("拒绝原因（Rejected 时必填）"),
				"note":             sStr("备注（可选）"),
			}, "project_id", "idea_id", "status"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, ideaID, err := projectAndID(a, "idea_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				status, err := requireString(a, "status")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				reason, _ := a.String("rejection_reason")
				svc, err := d.ideaService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				idea, err := svc.ChangeStatus(ctx, ideas.ChangeStatusInput{
					ID:              ideaID,
					To:              ideas.Status(status),
					RejectionReason: reason,
					Actor:           eventActor(),
				})
				if err != nil {
					return mcp.ToolOutcome{}, mapIdeasError(err)
				}
				return okOut(map[string]any{"idea": ideaView(idea)})
			}),

		tool(mod, mcp.CategoryWrite, "idea_derive", "衍生 Idea",
			"从父 Idea 衍生新 Idea，并写入 derived_from 关系；初始状态恒为 New。",
			sObj(map[string]any{
				"project_id":  sStr("项目 ID"),
				"parent_id":   sStr("父 Idea ID"),
				"title":       sStrLen("标题", 1, 200),
				"description": sStr("Markdown 描述"),
			}, "project_id", "parent_id", "title"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				parentID, err := requireString(a, "parent_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				title, err := requireString(a, "title")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				desc, _ := a.String("description")
				svc, err := d.ideaService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				idea, err := svc.DeriveIdea(ctx, ideas.DeriveIdeaInput{
					SourceIdeaID:  parentID,
					Title:         title,
					DescriptionMD: desc,
					Actor:         eventActor(),
				})
				if err != nil {
					return mcp.ToolOutcome{}, mapIdeasError(err)
				}
				return okOut(map[string]any{"idea": ideaView(idea)})
			}),

		tool(mod, mcp.CategoryWrite, "idea_delete", "删除 Idea",
			"软删除 Idea（进入回收站）。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"idea_id":    sStr("Idea ID"),
			}, "project_id", "idea_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, ideaID, err := projectAndID(a, "idea_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.ideaService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if err := svc.DeleteIdea(ctx, ideaID, eventActor()); err != nil {
					return mcp.ToolOutcome{}, mapIdeasError(err)
				}
				return okOut(map[string]any{"deleted": true})
			}),

		tool(mod, mcp.CategoryRead, "evidence_list", "证据列表",
			"列出 Idea 证据，可按方向与确认状态过滤。",
			sObj(map[string]any{
				"project_id":      sStr("项目 ID"),
				"idea_id":         sStr("Idea ID（可选）"),
				"confirmed":       sBool("是否已确认"),
				"direction":       sEnum("方向", "support", "oppose"),
				"include_deleted": sBool("包含已删除证据"),
				"limit":           sInt("返回条数，默认 50，上限 200", 1, 200),
				"cursor":          sStr("上一页返回的 next_cursor"),
			}, "project_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.ideaService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				q := ideas.ListEvidenceQuery{
					IdeaID:         stringsOf(a, "idea_id"),
					IncludeDeleted: a.BoolDefault("include_deleted", false),
					PageSize:       limitOf(a, 50, 200),
					Cursor:         stringsOf(a, "cursor"),
				}
				if v, ok := a.Bool("confirmed"); ok {
					q.Confirmed = &v
				}
				if v, ok := a.String("direction"); ok {
					q.Directions = []ideas.Direction{ideas.Direction(v)}
				}
				pg, err := svc.ListEvidence(ctx, q)
				if err != nil {
					return mcp.ToolOutcome{}, mapIdeasError(err)
				}
				items := make([]any, 0, len(pg.Items))
				for _, ev := range pg.Items {
					items = append(items, evidenceView(ev))
				}
				return okOut(pageOf(items, pg.NextCursor))
			}),

		tool(mod, mcp.CategoryWrite, "evidence_create", "新增证据",
			"为 Idea 新增人工证据（ai_generated=false, confirmed=true）。",
			sObj(map[string]any{
				"project_id":  sStr("项目 ID"),
				"idea_id":     sStr("Idea ID"),
				"direction":   sEnum("方向", "support", "oppose"),
				"content":     sStrLen("证据内容", 1, 8000),
				"source_type": sEnum("来源类型", "experiment", "run", "note", "paper"),
				"source_id":   sStr("来源实体 ID"),
			}, "project_id", "idea_id", "direction", "content"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, ideaID, err := projectAndID(a, "idea_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				direction, err := requireString(a, "direction")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				content, err := requireString(a, "content")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				sourceType, _ := a.String("source_type")
				sourceID, _ := a.String("source_id")
				if sourceType == "" || sourceID == "" {
					return mcp.ToolOutcome{}, invalidf("证据必须提供 source_type 与 source_id")
				}
				svc, err := d.ideaService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				ev, err := svc.AddEvidence(ctx, ideas.AddEvidenceInput{
					IdeaID:    ideaID,
					Direction: ideas.Direction(direction),
					ContentMD: content,
					Source:    ideas.SourceRef{Type: ideas.SourceType(sourceType), ID: sourceID},
					Actor:     eventActor(),
				})
				if err != nil {
					return mcp.ToolOutcome{}, mapIdeasError(err)
				}
				return okOut(map[string]any{"evidence": evidenceView(ideas.EvidenceView{Evidence: *ev})})
			}),

		tool(mod, mcp.CategoryWrite, "evidence_update", "更新证据",
			"更新草稿证据的方向或内容；已确认证据不可改方向。",
			sObj(map[string]any{
				"project_id":          sStr("项目 ID"),
				"evidence_id":         sStr("证据 ID"),
				"direction":           sEnum("方向", "support", "oppose"),
				"content":             sStrLen("证据内容", 1, 8000),
				"source_type":         sEnum("来源类型", "experiment", "run", "note", "paper"),
				"source_id":           sStr("来源实体 ID"),
				"expected_updated_at": sStr("并发令牌（可选）"),
			}, "project_id", "evidence_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, evidenceID, err := projectAndID(a, "evidence_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if a.Has("source_type") || a.Has("source_id") {
					return mcp.ToolOutcome{}, invalidf("服务层不支持修改证据来源；请删除后重建证据")
				}
				in := ideas.UpdateEvidenceInput{EvidenceID: evidenceID, Actor: eventActor()}
				if v, ok := a.String("direction"); ok {
					d := ideas.Direction(v)
					in.Direction = &d
				}
				in.ContentMD = optionalStringPtr(a, "content")
				if in.Direction == nil && in.ContentMD == nil {
					return mcp.ToolOutcome{}, invalidf("至少提供 direction 或 content")
				}
				svc, err := d.ideaService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				ev, err := svc.UpdateEvidence(ctx, in)
				if err != nil {
					return mcp.ToolOutcome{}, mapIdeasError(err)
				}
				return okOut(map[string]any{"evidence": evidenceView(ideas.EvidenceView{Evidence: *ev})})
			}),

		tool(mod, mcp.CategoryWrite, "evidence_discard", "丢弃草稿证据",
			"丢弃 AI 草稿证据（仅草稿，软删除）。",
			sObj(map[string]any{
				"project_id":  sStr("项目 ID"),
				"idea_id":     sStr("Idea ID"),
				"evidence_id": sStr("证据 ID"),
			}, "project_id", "idea_id", "evidence_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, ideaID, err := projectAndID(a, "idea_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				evidenceID, err := requireString(a, "evidence_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.ideaService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				res, err := svc.DiscardEvidence(ctx, ideas.DiscardEvidenceInput{
					IdeaID:      ideaID,
					EvidenceIDs: []string{evidenceID},
					Actor:       eventActor(),
				})
				if err != nil {
					return mcp.ToolOutcome{}, mapIdeasError(err)
				}
				if len(res.Discarded) == 0 {
					return mcp.ToolOutcome{}, evidenceFailure(res.Failed, "丢弃证据失败")
				}
				return okOut(map[string]any{"discarded": true})
			}),

		tool(mod, mcp.CategoryWrite, "evidence_delete", "删除已确认证据",
			"软删除已确认证据。草稿请使用 evidence_discard。",
			sObj(map[string]any{
				"project_id":  sStr("项目 ID"),
				"evidence_id": sStr("证据 ID"),
			}, "project_id", "evidence_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, evidenceID, err := projectAndID(a, "evidence_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.ideaService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if err := svc.DeleteEvidence(ctx, evidenceID, eventActor()); err != nil {
					return mcp.ToolOutcome{}, mapIdeasError(err)
				}
				return okOut(map[string]any{"deleted": true})
			}),

		tool(mod, mcp.CategoryJob, "evidence_extract", "AI 提取证据草稿",
			"从实验/运行/笔记来源提取证据草稿（AI 产出，actor=ai，不自动确认）。返回 job_id。",
			sObj(map[string]any{
				"project_id":  sStr("项目 ID"),
				"idea_id":     sStr("Idea ID"),
				"source_refs": sArr("来源 [{type, id}]", sFreeObj("来源", "type", "id")),
			}, "project_id", "idea_id"), annJob(false),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, ideaID, err := projectAndID(a, "idea_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				refs, _, err := a.Objects("source_refs")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				sources := make([]ideas.SourceRef, 0, len(refs))
				for _, ref := range refs {
					typ, _ := ref["type"].(string)
					id, _ := ref["id"].(string)
					sources = append(sources, ideas.SourceRef{Type: ideas.SourceType(typ), ID: id})
				}
				if !d.aiAvailable() {
					return mcp.ToolOutcome{}, mcp.Errorf(mcp.CodeAIUnavailable, "AI 服务未配置，无法提取证据草稿")
				}
				svc, err := d.ideaService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				params, _ := json.Marshal(map[string]any{"idea_id": ideaID})
				job, err := d.Jobs.Submit(ctx, p, mcp.JobRequest{
					Tool:      "evidence_extract",
					ProjectID: projectID,
					ClientID:  p.ClientID,
					Params:    params,
					TraceID:   mcp.TraceIDFrom(ctx),
					Run: func(jctx context.Context, progress mcp.ProgressFunc) (any, error) {
						progress(20, "读取来源内容")
						res, err := svc.GenerateEvidenceDrafts(jctx, ideas.GenerateEvidenceInput{
							IdeaID:  ideaID,
							Sources: sources,
						})
						if err != nil {
							return nil, mapIdeasError(err)
						}
						progress(90, "写入草稿")
						draftIDs := make([]string, 0, len(res.Drafts))
						for _, d := range res.Drafts {
							draftIDs = append(draftIDs, d.ID)
						}
						skipped := make([]any, 0, len(res.Skipped))
						for _, s := range res.Skipped {
							skipped = append(skipped, map[string]any{"source": s.Source, "reason": s.Reason})
						}
						return map[string]any{
							"draft_ids": draftIDs,
							"skipped":   skipped,
							"model_info": map[string]any{
								"provider": res.Provider,
								"model":    res.Model,
							},
						}, nil
					},
				})
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				return jobOut(job)
			}),

		tool(mod, mcp.CategoryConfirm, "evidence_confirm", "确认证据",
			"请求把 AI 草稿证据转为已确认。工具只入队，必须在应用内人工批准后以 actor=user 逐条执行。",
			sObj(map[string]any{
				"project_id":   sStr("项目 ID"),
				"idea_id":      sStr("Idea ID"),
				"evidence_ids": sArr("证据 ID", sStr("证据 ID")),
			}, "project_id", "idea_id", "evidence_ids"), annConfirm(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, ideaID, err := projectAndID(a, "idea_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				evidenceIDs, _, err := a.Strings("evidence_ids")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if len(evidenceIDs) == 0 {
					return mcp.ToolOutcome{}, invalidf("evidence_ids 不能为空")
				}
				svc, err := d.ideaService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				drafts := make([]any, 0, len(evidenceIDs))
				for _, id := range evidenceIDs {
					ev, err := svc.ListEvidence(ctx, ideas.ListEvidenceQuery{IdeaID: ideaID, PageSize: 200})
					if err != nil {
						return mcp.ToolOutcome{}, mapIdeasError(err)
					}
					found := false
					for _, item := range ev.Items {
						if item.ID == id {
							drafts = append(drafts, evidenceView(item))
							found = true
							break
						}
					}
					if !found {
						return mcp.ToolOutcome{}, notFoundf("证据 %s 不存在", id)
					}
				}
				return pendingOut("确认证据", "把 "+itoa(len(evidenceIDs))+" 条证据草稿标记为已确认。", map[string]any{"idea_id": ideaID, "drafts": drafts}, projectID)
			}),
	}
	return &provider{module: mod, tools: tools, ready: ready}
}

func evidenceFailure(failures []ideas.EvidenceFailure, fallback string) error {
	if len(failures) == 0 {
		return mcp.Errorf(mcp.CodeConflict, "%s", fallback)
	}
	first := failures[0]
	return mcp.Errorf(mcp.CodeConflict, "%s", first.Message).WithDetail("code", string(first.Code)).WithDetail("evidence_id", first.ID)
}

func ideaView(i *ideas.Idea) map[string]any {
	if i == nil {
		return nil
	}
	return map[string]any{
		"idea_id":          i.ID,
		"title":            i.Title,
		"description_md":   i.DescriptionMD,
		"status":           string(i.Status),
		"rejection_reason": i.RejectionReason,
		"created_at":       ts(i.CreatedAt),
		"updated_at":       ts(i.UpdatedAt),
		"deleted_at":       tsp(i.DeletedAt),
	}
}

func ideaSummaryView(s ideas.IdeaSummary) map[string]any {
	out := ideaView(&s.Idea)
	out["support_count"] = s.SupportCount
	out["oppose_count"] = s.OpposeCount
	out["draft_count"] = s.DraftCount
	out["linked_papers"] = s.LinkedPapers
	out["linked_notes"] = s.LinkedNotes
	out["linked_experiments"] = s.LinkedExps
	out["derived_from_id"] = s.DerivedFromID
	return out
}

func evidenceView(v ideas.EvidenceView) map[string]any {
	e := v.Evidence
	return map[string]any{
		"evidence_id":    e.ID,
		"idea_id":        e.IdeaID,
		"direction":      string(e.Direction),
		"content":        e.ContentMD,
		"source":         map[string]any{"type": string(e.Source.Type), "id": e.Source.ID},
		"source_title":   v.SourceTitle,
		"source_deleted": v.SourceDeleted,
		"source_broken":  v.SourceBroken,
		"ai_generated":   e.AIGenerated,
		"confirmed":      e.Confirmed,
		"content_hash":   e.ContentHash,
		"created_at":     ts(e.CreatedAt),
		"updated_at":     ts(e.UpdatedAt),
		"deleted_at":     tsp(e.DeletedAt),
	}
}

// mapIdeasError maps SPEC-06 ErrorCodes to MCP codes.
func mapIdeasError(err error) error {
	if err == nil {
		return nil
	}
	var me *mcp.Error
	if errors.As(err, &me) {
		return me
	}
	switch ideas.CodeOf(err) {
	case ideas.ErrIdeaNotFound, ideas.ErrEvidenceNotFound, ideas.ErrTargetNotFound:
		return mcp.Errorf(mcp.CodeNotFound, "目标不存在").Wrap(err)
	case ideas.ErrIdeaDeleted, ideas.ErrEvidenceDeleted, ideas.ErrEvidenceConfirmed, ideas.ErrTargetDeleted:
		return mcp.Errorf(mcp.CodeConflict, "目标状态不允许该操作").Wrap(err)
	case ideas.ErrProjectReadOnly:
		return mcp.Errorf(mcp.CodeArchivedReadOnly, "项目已归档，不能写入").Wrap(err)
	case ideas.ErrConfirmationRequired:
		return mcp.Errorf(mcp.CodeCapabilityInvalid, "该操作需要应用内人工确认").Wrap(err)
	case ideas.ErrAIDisabled:
		return mcp.Errorf(mcp.CodeAIUnavailable, "AI 服务未配置").Wrap(err)
	case ideas.ErrSourceUnavailable, ideas.ErrSourceEmpty:
		return mcp.Errorf(mcp.CodeExternalUnavail, "证据来源不可用").Wrap(err)
	case ideas.ErrInvalidStatus, ideas.ErrRejectionReasonRequired, ideas.ErrNotRejected,
		ideas.ErrInvalidSource, ideas.ErrInvalidInput, ideas.ErrInvalidActor,
		ideas.ErrEvidenceDirectionLocked, ideas.ErrEvidenceNotAIDraft, ideas.ErrLineageTooDeep,
		ideas.ErrAIOutputInvalid:
		return mcp.Errorf(mcp.CodeInvalidArgument, "参数或状态不合法").Wrap(err)
	}
	return asMCPerror(err, mcp.CodeInternal, "Idea 模块执行失败")
}
