package mcpbind

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/trash"
	"researchos/internal/mcp"
	"researchos/internal/notes"
	"researchos/internal/project"
)

// newMiscProvider builds Activity, Relation and Trash tools (§5.3.7, §5.3.8).
func newMiscProvider(d Deps, ready func(context.Context) error) mcp.ToolProvider {
	mod := mcp.ModuleNotes
	tools := []mcp.ToolBinding{
		tool(mod, mcp.CategoryRead, "activity_list", "活动列表",
			"读取项目 Activity（只读，按时间倒序）。MCP 与 AI 的写入分别以 actor=mcp / ai 记录。",
			sObj(map[string]any{
				"project_id":  sStr("项目 ID"),
				"since":       sStr("起始时间 RFC3339（含）"),
				"until":       sStr("结束时间 RFC3339（含）"),
				"event_types": sArr("事件类型", sStr("事件类型")),
				"actor":       sEnum("操作者", "user", "mcp", "ai"),
				"entity_type": sStr("实体类型"),
				"entity_id":   sStr("实体 ID"),
				"limit":       sInt("返回条数，默认 50，上限 200", 1, 200),
				"cursor":      sStr("上一页返回的 next_cursor"),
			}, "project_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				h, err := d.handle(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				since, err := parseArgTime(a, "since")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				until, err := parseArgTime(a, "until")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				offset, err := cursorOffset(stringsOf(a, "cursor"))
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				q := events.Query{Since: since, Until: until, Offset: offset, Limit: limitOf(a, 50, 200)}
				if list, _, err := a.Strings("event_types"); err != nil {
					return mcp.ToolOutcome{}, err
				} else {
					for _, v := range list {
						q.EventTypes = append(q.EventTypes, events.Type(v))
					}
				}
				if actor, ok := a.String("actor"); ok {
					q.Actors = []events.Actor{events.Actor(actor)}
				}
				if entityType, ok := a.String("entity_type"); ok && entityType != "" {
					q.EntityTypes = []events.EntityType{events.EntityType(entityType)}
				}
				q.EntityID, _ = a.String("entity_id")
				list, err := h.Reader().List(ctx, q)
				if err != nil {
					return mcp.ToolOutcome{}, asMCPerror(err, mcp.CodeInternal, "读取活动失败")
				}
				items := make([]any, 0, len(list))
				for _, activity := range list {
					items = append(items, map[string]any{
						"activity_id": activity.ID,
						"event_type":  string(activity.Type),
						"entity_type": string(activity.EntityType),
						"entity_id":   activity.EntityID,
						"payload":     asJSON(activity.Payload),
						"actor":       string(activity.Actor),
						"created_at":  ts(activity.CreatedAt),
					})
				}
				total, err := h.Reader().Count(ctx, q)
				if err != nil {
					return mcp.ToolOutcome{}, asMCPerror(err, mcp.CodeInternal, "读取活动失败")
				}
				return okOut(pageOf(items, nextOffsetCursor(offset, q.Limit, total)))
			}),

		tool(mod, mcp.CategoryRead, "relation_list", "关系列表",
			"列出一个实体的关系（按来源或目标）。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"src_type":   sStr("来源实体类型"),
				"src_id":     sStr("来源实体 ID"),
				"dst_type":   sStr("目标实体类型"),
				"dst_id":     sStr("目标实体 ID"),
				"relation":   sEnum("关系类型", "references", "derived_from", "validates", "deliverable_of", "related_to"),
			}, "project_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				relSvc, err := d.noteRelationService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				relation, _ := a.String("relation")
				srcType, srcID := stringsOf(a, "src_type"), stringsOf(a, "src_id")
				dstType, dstID := stringsOf(a, "dst_type"), stringsOf(a, "dst_id")
				var links []notes.ResolvedLink
				switch {
				case srcID != "":
					links, err = relSvc.ListFrom(ctx, notes.EntityRef{Type: schema.EntityType(srcType), ID: srcID}, relation)
				case dstID != "":
					links, err = relSvc.ListTo(ctx, notes.EntityRef{Type: schema.EntityType(dstType), ID: dstID}, relation)
				default:
					return mcp.ToolOutcome{}, invalidf("必须提供 src_id 或 dst_id")
				}
				if err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				items := make([]any, 0, len(links))
				for _, link := range links {
					items = append(items, map[string]any{
						"src_type": string(link.Src.Type),
						"src_id":   link.Src.ID,
						"relation": string(link.Relation),
						"dst_type": string(link.Dst.Type),
						"dst_id":   link.Dst.ID,
						"title":    link.Title,
						"deleted":  link.Deleted,
						"missing":  link.Missing,
					})
				}
				return okOut(map[string]any{"relations": items})
			}),

		tool(mod, mcp.CategoryWrite, "relation_add", "添加关系",
			"在两个实体间添加关系。仅接受注册表内的关系类型；跨项目关系被拒绝。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"src_type":   sStr("来源实体类型"),
				"src_id":     sStr("来源实体 ID"),
				"relation":   sEnum("关系类型", "references", "derived_from", "validates", "deliverable_of", "related_to"),
				"dst_type":   sStr("目标实体类型"),
				"dst_id":     sStr("目标实体 ID"),
			}, "project_id", "src_type", "src_id", "relation", "dst_type", "dst_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				relSvc, err := d.noteRelationService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				edge, err := relationEdgeFrom(a)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if err := relSvc.Add(ctx, edge); err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				return okOut(map[string]any{"added": true})
			}),

		tool(mod, mcp.CategoryWrite, "relation_remove", "移除关系",
			"移除两个实体间的关系。服务层没有按关系 ID 删除的入口，因此除 relation_id 外必须同时提供关系三元组。",
			sObj(map[string]any{
				"project_id":  sStr("项目 ID"),
				"relation_id": sStr("关系 ID（用于审计回显）"),
				"src_type":    sStr("来源实体类型"),
				"src_id":      sStr("来源实体 ID"),
				"relation":    sEnum("关系类型", "references", "derived_from", "validates", "deliverable_of", "related_to"),
				"dst_type":    sStr("目标实体类型"),
				"dst_id":      sStr("目标实体 ID"),
			}, "project_id", "relation_id", "src_type", "src_id", "relation", "dst_type", "dst_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if _, err := requireString(a, "relation_id"); err != nil {
					return mcp.ToolOutcome{}, err
				}
				relSvc, err := d.noteRelationService(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				edge, err := relationEdgeFrom(a)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if err := relSvc.Remove(ctx, edge); err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				return okOut(map[string]any{"removed": true})
			}),

		tool(mod, mcp.CategoryRead, "trash_list", "回收站列表",
			"列出回收站内容。省略 project_id 时聚合所有项目。",
			sObj(map[string]any{"project_id": sStr("项目 ID（可选）")}), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, _ := a.String("project_id")
				items, err := d.trashItems(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				return okOut(map[string]any{"items": items})
			}),

		tool(mod, mcp.CategoryWrite, "trash_restore", "从回收站恢复",
			"恢复一个软删除实体。归档项目拒绝恢复，需先解归档。",
			sObj(map[string]any{
				"project_id":  sStr("项目 ID"),
				"entity_type": sStr("实体类型"),
				"entity_id":   sStr("实体 ID"),
			}, "project_id", "entity_type", "entity_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				entityType, err := requireString(a, "entity_type")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				entityID, err := requireString(a, "entity_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if archived, err := d.archived(ctx, projectID); err != nil {
					return mcp.ToolOutcome{}, err
				} else if archived {
					return mcp.ToolOutcome{}, mcp.Errorf(mcp.CodeArchivedReadOnly, "项目已归档，恢复前请先解除归档")
				}
				h, err := d.handle(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc := trash.New(projectID, h.DB(), d.Workspace.Paths(), h.Events(), d.Confirm, d.clock())
				ref := trash.Ref{Type: schema.EntityType(entityType), ID: entityID}
				err = h.InTx(ctx, func(tx *sql.Tx) error {
					return svc.Restore(ctx, tx, ref, eventActor())
				})
				if err != nil {
					return mcp.ToolOutcome{}, mapTrashError(err)
				}
				return okOut(map[string]any{"restored": true})
			}),

		tool(mod, mcp.CategoryRead, "trash_empty_preview", "清空回收站预览",
			"返回回收站按类型计数与最早删除时间。清空前必须用该结果作为 expected_counts。",
			sObj(map[string]any{"project_id": sStr("项目 ID")}, "project_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				counts, oldest, err := d.trashCounts(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				return okOut(map[string]any{"counts": counts, "oldest_deleted_at": tsp(oldest)})
			}),

		tool(mod, mcp.CategoryConfirm, "trash_empty", "清空回收站",
			"请求永久清空项目回收站。必须携带预览得到的 expected_counts；执行前重算，不一致则冲突。实际清空需在应用内人工批准。",
			sObj(map[string]any{
				"project_id":      sStr("项目 ID"),
				"expected_counts": sFreeObj("预览得到的 {entity_type: count}"),
				"dry_run":         sBool("只返回预览，不入队"),
			}, "project_id", "expected_counts"), annConfirm(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				expected, ok := a.Object("expected_counts")
				if !ok {
					return mcp.ToolOutcome{}, invalidf("expected_counts 必须是对象")
				}
				counts, oldest, err := d.trashCounts(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if !sameCounts(expected, counts) {
					return mcp.ToolOutcome{}, conflictf("回收站内容已变化，请重新预览后再确认").
						WithDetail("actual", counts)
				}
				if a.BoolDefault("dry_run", false) {
					return okOut(map[string]any{"status": "dry_run", "counts": counts, "oldest_deleted_at": tsp(oldest)})
				}
				return pendingOut("清空回收站", "永久删除回收站中的 "+itoa(totalCounts(counts))+" 个实体；此操作不可撤销。", map[string]any{"counts": counts, "oldest_deleted_at": tsp(oldest)}, projectID)
			}),
	}
	return &provider{module: mod, tools: tools, ready: ready}
}

// noteRelationService builds the SPEC-05 relation service for one project.
func (d *Deps) noteRelationService(ctx context.Context, projectID string) (notes.RelationService, error) {
	if _, err := d.handle(ctx, projectID); err != nil {
		return nil, err
	}
	return notes.NewRelationService(d.Workspace, projectID, notes.ServiceOptions{Clock: d.clock(), Logger: d.Logger}), nil
}

func relationEdgeFrom(a *mcp.Arguments) (notes.RelationEdge, error) {
	srcType, err := requireString(a, "src_type")
	if err != nil {
		return notes.RelationEdge{}, err
	}
	srcID, err := requireString(a, "src_id")
	if err != nil {
		return notes.RelationEdge{}, err
	}
	relation, err := requireString(a, "relation")
	if err != nil {
		return notes.RelationEdge{}, err
	}
	dstType, err := requireString(a, "dst_type")
	if err != nil {
		return notes.RelationEdge{}, err
	}
	dstID, err := requireString(a, "dst_id")
	if err != nil {
		return notes.RelationEdge{}, err
	}
	return notes.RelationEdge{
		Src:      notes.EntityRef{Type: schema.EntityType(srcType), ID: srcID},
		Dst:      notes.EntityRef{Type: schema.EntityType(dstType), ID: dstID},
		Relation: schema.Relation(relation),
		Actor:    eventActor(),
	}, nil
}

// trashItems aggregates the trash of one project or every project.
func (d *Deps) trashItems(ctx context.Context, projectID string) ([]any, error) {
	projectIDs := []string{projectID}
	if projectID == "" {
		if d.Facade == nil {
			return nil, mcp.Errorf(mcp.CodeInternal, "项目服务不可用")
		}
		list, err := d.Facade.ListProjects(ctx, project.ListQuery{IncludeArchived: true})
		if err != nil {
			return nil, mapProjectError(err)
		}
		projectIDs = projectIDs[:0]
		for _, p := range list {
			projectIDs = append(projectIDs, p.ID)
		}
		sort.Strings(projectIDs)
	}
	out := make([]any, 0, 16)
	for _, pid := range projectIDs {
		h, err := d.handle(ctx, pid)
		if err != nil {
			continue
		}
		svc := trash.New(pid, h.DB(), d.Workspace.Paths(), h.Events(), d.Confirm, d.clock())
		offset := 0
		for {
			items, err := svc.List(ctx, trash.ListQuery{Limit: 500, Offset: offset})
			if err != nil {
				return nil, mapTrashError(err)
			}
			for _, item := range items {
				out = append(out, map[string]any{
					"project_id":  pid,
					"entity_type": string(item.EntityType),
					"entity_id":   item.ID,
					"title":       item.Title,
					"deleted_at":  ts(item.DeletedAt),
				})
			}
			if len(items) < 500 {
				break
			}
			offset += 500
		}
	}
	return out, nil
}

func (d *Deps) trashCounts(ctx context.Context, projectID string) (map[string]int, *time.Time, error) {
	h, err := d.handle(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	svc := trash.New(projectID, h.DB(), d.Workspace.Paths(), h.Events(), d.Confirm, d.clock())
	counts := map[string]int{}
	var oldest *time.Time
	offset := 0
	for {
		items, err := svc.List(ctx, trash.ListQuery{Limit: 500, Offset: offset})
		if err != nil {
			return nil, nil, mapTrashError(err)
		}
		for _, item := range items {
			counts[string(item.EntityType)]++
			if oldest == nil || item.DeletedAt.Before(*oldest) {
				t := item.DeletedAt
				oldest = &t
			}
		}
		if len(items) < 500 {
			break
		}
		offset += 500
	}
	return counts, oldest, nil
}

func sameCounts(expected map[string]any, actual map[string]int) bool {
	for key, value := range expected {
		want, ok := numberToInt(value)
		if !ok || actual[key] != want {
			return false
		}
	}
	for key, value := range actual {
		if value == 0 {
			continue
		}
		if _, ok := expected[key]; !ok {
			return false
		}
	}
	return true
}

func numberToInt(v any) (int, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

func totalCounts(counts map[string]int) int {
	total := 0
	for _, n := range counts {
		total += n
	}
	return total
}

func mapTrashError(err error) error {
	if err == nil {
		return nil
	}
	var me *mcp.Error
	if errors.As(err, &me) {
		return me
	}
	switch {
	case errors.Is(err, errs.ErrUserConfirmationRequired), errors.Is(err, errs.ErrConfirmScopeMismatch):
		return mcp.Errorf(mcp.CodeCapabilityInvalid, "该操作需要应用内人工确认").Wrap(err)
	case errors.Is(err, errs.ErrBadEntityType), errors.Is(err, errs.ErrBadEntityID):
		return mcp.Errorf(mcp.CodeInvalidArgument, "实体类型或 ID 不合法").Wrap(err)
	}
	return asMCPerror(err, mcp.CodeInternal, "回收站操作失败")
}

var _ = notes.KindScratchpad
