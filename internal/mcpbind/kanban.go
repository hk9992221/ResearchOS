package mcpbind

import (
	"context"
	"errors"

	"researchos/internal/foundation/relations"
	"researchos/internal/kanban"
	"researchos/internal/mcp"
)

func (d *Deps) kanbanModule(ctx context.Context, projectID string) (*kanban.Module, error) {
	h, err := d.handle(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return kanban.New(h, kanban.DepsFromWorkspace(d.Workspace, h)), nil
}

// newKanbanProvider builds the SPEC-08 Kanban domain (§5.3.6). task_set_week_plan
// stays unregistered until KBN-P02 is confirmed.
func newKanbanProvider(d Deps, ready func(context.Context) error) mcp.ToolProvider {
	mod := mcp.ModuleKanban
	tools := []mcp.ToolBinding{
		tool(mod, mcp.CategoryRead, "task_list", "任务列表",
			"列出项目任务，可按列、阻塞、截止日期、周计划、关键字过滤。",
			sObj(map[string]any{
				"project_id":       sStr("项目 ID"),
				"column_id":        sStr("列名"),
				"blocked":          sBool("只看阻塞/未阻塞"),
				"deadline_before":  sStr("截止日期上界 YYYY-MM-DD"),
				"week_plan":        sStr("周计划 YYYY-Www"),
				"q":                sStr("标题/描述关键字"),
				"include_archived": sBool("包含已归档任务"),
				"include_deleted":  sBool("包含已删除任务"),
				"limit":            sInt("返回条数，默认 50，上限 200", 1, 200),
				"cursor":           sStr("上一页返回的 next_cursor"),
			}, "project_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				m, err := d.kanbanModule(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				q := kanban.Query{
					Text:            stringsOf(a, "q"),
					IncludeArchived: a.BoolDefault("include_archived", false),
					IncludeDeleted:  a.BoolDefault("include_deleted", false),
					Limit:           limitOf(a, 50, 200),
					Cursor:          stringsOf(a, "cursor"),
				}
				if v, ok := a.String("column_id"); ok {
					q.Column = &v
				}
				if v, ok := a.Bool("blocked"); ok {
					q.Blocked = &v
				}
				if v, ok := a.String("deadline_before"); ok && v != "" {
					q.DeadlineTo = &v
				}
				if v, ok := a.String("week_plan"); ok && v != "" {
					q.WeekPlan = &v
				}
				pg, err := m.Tasks().List(ctx, q)
				if err != nil {
					return mcp.ToolOutcome{}, mapKanbanError(err)
				}
				items := make([]any, 0, len(pg.Items))
				for _, t := range pg.Items {
					items = append(items, taskSummaryView(t))
				}
				return okOut(pageOf(items, pg.NextCursor))
			}),

		tool(mod, mcp.CategoryRead, "task_get", "任务详情",
			"返回任务、子任务、检查项、关系链接与所在列。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"task_id":    sStr("任务 ID"),
			}, "project_id", "task_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, taskID, err := projectAndID(a, "task_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				m, err := d.kanbanModule(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				detail, err := m.Tasks().Get(ctx, taskID)
				if err != nil {
					return mcp.ToolOutcome{}, mapKanbanError(err)
				}
				return okOut(taskDetailView(detail))
			}),

		tool(mod, mcp.CategoryWrite, "task_create", "创建任务",
			"创建任务。省略 column_id/parent_id 即为快速捕获（Inbox）。",
			sObj(map[string]any{
				"project_id":     sStr("项目 ID"),
				"title":          sStrLen("标题", 1, 200),
				"description_md": sStr("Markdown 描述"),
				"column_id":      sStr("目标列名"),
				"parent_id":      sStr("父任务 ID"),
				"priority":       sEnum("优先级", "high", "medium", "low", "none"),
				"deadline":       sStr("截止日期 YYYY-MM-DD"),
				"labels":         sArr("标签", sStrLen("标签", 1, 32)),
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
				in := kanban.CreateInput{Title: title}
				in.Column = stringsOf(a, "column_id")
				in.DescriptionMD = stringsOf(a, "description_md")
				if v, ok := a.String("priority"); ok {
					in.Priority = kanban.Priority(v)
				}
				if v, ok := a.String("deadline"); ok && v != "" {
					in.Deadline = &v
				}
				if v, ok := a.String("parent_id"); ok && v != "" {
					in.ParentTaskID = &v
				}
				if list, ok, err := a.Strings("labels"); err != nil {
					return mcp.ToolOutcome{}, err
				} else if ok {
					in.Labels = list
				}
				m, err := d.kanbanModule(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				task, err := m.Tasks().Create(ctx, eventActor(), in)
				if err != nil {
					return mcp.ToolOutcome{}, mapKanbanError(err)
				}
				return okOut(map[string]any{"task": taskView(task)})
			}),

		tool(mod, mcp.CategoryWrite, "task_update", "更新任务",
			"部分更新任务。提供 expected_updated_at 时执行乐观并发检查；不一致返回 CONFLICT。",
			sObj(map[string]any{
				"project_id":          sStr("项目 ID"),
				"task_id":             sStr("任务 ID"),
				"expected_updated_at": sStr("并发令牌"),
				"title":               sStrLen("标题", 1, 200),
				"description_md":      sStr("Markdown 描述"),
				"priority":            sEnum("优先级", "high", "medium", "low", "none"),
				"deadline":            sStr("截止日期 YYYY-MM-DD；空字符串表示清除"),
				"blocked":             sBool("是否阻塞"),
				"labels":              sArr("标签", sStrLen("标签", 1, 32)),
				"column_id":           sStr("目标列名"),
			}, "project_id", "task_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, taskID, err := projectAndID(a, "task_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				in := kanban.UpdateInput{ID: taskID}
				if v, ok := a.String("expected_updated_at"); ok && v != "" {
					in.ExpectedUpdatedAt = &v
				}
				in.Title = optionalStringPtr(a, "title")
				in.DescriptionMD = optionalStringPtr(a, "description_md")
				if v, ok := a.String("priority"); ok {
					p := kanban.Priority(v)
					in.Priority = &p
				}
				if a.Has("deadline") {
					v, _ := a.String("deadline")
					var target *string
					if v != "" {
						target = &v
					}
					in.Deadline = &target
				}
				if v, ok := a.Bool("blocked"); ok {
					in.Blocked = &v
				}
				if list, ok, err := a.Strings("labels"); err != nil {
					return mcp.ToolOutcome{}, err
				} else if ok {
					in.Labels = &list
				}
				if v, ok := a.String("column_id"); ok && v != "" {
					in.Column = &v
				}
				m, err := d.kanbanModule(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				task, err := m.Tasks().Update(ctx, eventActor(), in)
				if err != nil {
					return mcp.ToolOutcome{}, mapKanbanError(err)
				}
				return okOut(map[string]any{"task": taskView(task)})
			}),

		tool(mod, mcp.CategoryWrite, "task_move", "移动任务",
			"把任务移动到目标列并可指定前后锚点。仅列变化时发 status.changed。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"task_id":    sStr("任务 ID"),
				"column_id":  sStr("目标列名"),
				"before_id":  sStr("插入到该任务之前"),
				"after_id":   sStr("插入到该任务之后"),
			}, "project_id", "task_id", "column_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, taskID, err := projectAndID(a, "task_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				column, err := requireString(a, "column_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				in := kanban.MoveInput{ID: taskID, Column: column}
				if v, ok := a.String("before_id"); ok && v != "" {
					in.BeforeID = &v
				}
				if v, ok := a.String("after_id"); ok && v != "" {
					in.AfterID = &v
				}
				m, err := d.kanbanModule(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				task, err := m.Tasks().Move(ctx, eventActor(), in)
				if err != nil {
					return mcp.ToolOutcome{}, mapKanbanError(err)
				}
				return okOut(map[string]any{"task": taskView(task)})
			}),

		tool(mod, mcp.CategoryWrite, "task_archive", "归档任务",
			"归档任务（不级联子任务，不产生 Activity）。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"task_id":    sStr("任务 ID"),
			}, "project_id", "task_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, taskID, err := projectAndID(a, "task_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				m, err := d.kanbanModule(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				task, err := m.Tasks().Archive(ctx, eventActor(), taskID)
				if err != nil {
					return mcp.ToolOutcome{}, mapKanbanError(err)
				}
				return okOut(map[string]any{"task": taskView(task)})
			}),

		tool(mod, mcp.CategoryWrite, "task_unarchive", "解除任务归档",
			"解除任务归档。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"task_id":    sStr("任务 ID"),
			}, "project_id", "task_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, taskID, err := projectAndID(a, "task_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				m, err := d.kanbanModule(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				task, err := m.Tasks().Unarchive(ctx, eventActor(), taskID)
				if err != nil {
					return mcp.ToolOutcome{}, mapKanbanError(err)
				}
				return okOut(map[string]any{"task": taskView(task)})
			}),

		tool(mod, mcp.CategoryWrite, "task_delete", "删除任务",
			"软删除任务，级联其未删除的子任务。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"task_id":    sStr("任务 ID"),
			}, "project_id", "task_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, taskID, err := projectAndID(a, "task_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				m, err := d.kanbanModule(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if err := m.Tasks().SoftDelete(ctx, eventActor(), taskID); err != nil {
					return mcp.ToolOutcome{}, mapKanbanError(err)
				}
				return okOut(map[string]any{"deleted": true})
			}),

		tool(mod, mcp.CategoryRead, "column_list", "看板列列表",
			"列出看板列（名称、类型、是否为完成列、位置）。",
			sObj(map[string]any{"project_id": sStr("项目 ID")}, "project_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				m, err := d.kanbanModule(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				cols, err := m.Columns().List(ctx)
				if err != nil {
					return mcp.ToolOutcome{}, mapKanbanError(err)
				}
				return okOut(map[string]any{"columns": columnViews(cols)})
			}),

		tool(mod, mcp.CategoryWrite, "column_create", "新建看板列",
			"新建看板列，可指定插入位置与是否为完成列。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"name":       sStrLen("列名", 1, 40),
				"kind":       sEnum("列类型", "backlog", "todo", "in_progress", "review", "done", "custom"),
				"is_done":    sBool("是否为完成列"),
				"after":      sStr("插入到该列之后"),
			}, "project_id", "name"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				name, err := requireString(a, "name")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				in := kanban.CreateColumnInput{Name: name, Kind: stringsOf(a, "kind"), IsDone: a.BoolDefault("is_done", false)}
				if v, ok := a.String("after"); ok && v != "" {
					in.After = &v
				}
				m, err := d.kanbanModule(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				col, err := m.Columns().Create(ctx, eventActor(), in)
				if err != nil {
					return mcp.ToolOutcome{}, mapKanbanError(err)
				}
				return okOut(map[string]any{"column": columnView(col)})
			}),

		tool(mod, mcp.CategoryWrite, "column_rename", "重命名看板列",
			"重命名看板列；该列全部任务（含已删除）的列值同步更新。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"old_name":   sStr("原列名"),
				"new_name":   sStrLen("新列名", 1, 40),
			}, "project_id", "old_name", "new_name"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				oldName, err := requireString(a, "old_name")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				newName, err := requireString(a, "new_name")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				m, err := d.kanbanModule(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				col, err := m.Columns().Rename(ctx, eventActor(), oldName, newName)
				if err != nil {
					return mcp.ToolOutcome{}, mapKanbanError(err)
				}
				return okOut(map[string]any{"column": columnView(col)})
			}),

		tool(mod, mcp.CategoryWrite, "column_move", "移动看板列",
			"调整列顺序（0 起始的目标位置）。",
			sObj(map[string]any{
				"project_id":  sStr("项目 ID"),
				"name":        sStr("列名"),
				"to_position": sInt("目标位置（0 起始）", 0, 0),
			}, "project_id", "name", "to_position"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				name, err := requireString(a, "name")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				pos, ok := a.Int("to_position")
				if !ok || pos < 0 {
					return mcp.ToolOutcome{}, invalidf("to_position 必须是非负整数")
				}
				m, err := d.kanbanModule(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				cols, err := m.Columns().Move(ctx, eventActor(), name, pos)
				if err != nil {
					return mcp.ToolOutcome{}, mapKanbanError(err)
				}
				return okOut(map[string]any{"columns": columnViews(cols)})
			}),

		tool(mod, mcp.CategoryWrite, "column_set_done", "设置完成列",
			"标记或取消列的完成语义；不能移除最后一个完成列。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"name":       sStr("列名"),
				"done":       sBool("是否为完成列"),
			}, "project_id", "name", "done"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				name, err := requireString(a, "name")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				done, ok := a.Bool("done")
				if !ok {
					return mcp.ToolOutcome{}, invalidf("缺少必填参数 done")
				}
				m, err := d.kanbanModule(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				col, err := m.Columns().SetDone(ctx, eventActor(), name, done)
				if err != nil {
					return mcp.ToolOutcome{}, mapKanbanError(err)
				}
				return okOut(map[string]any{"column": columnView(col)})
			}),

		tool(mod, mcp.CategoryConfirm, "column_delete", "删除看板列",
			"请求删除看板列。任务必须迁移到 migrate_to 指定列；实际删除需在应用内人工批准。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"name":       sStr("要删除的列名"),
				"migrate_to": sStr("任务迁移到的列名"),
			}, "project_id", "name", "migrate_to"), annConfirm(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				name, err := requireString(a, "name")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				target, err := requireString(a, "migrate_to")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if name == target {
					return mcp.ToolOutcome{}, invalidf("migrate_to 不能与要删除的列相同")
				}
				m, err := d.kanbanModule(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				count := 0
				column := name
				pg, err := m.Tasks().List(ctx, kanban.Query{Column: &column, IncludeDeleted: true, Limit: 200})
				if err != nil {
					return mcp.ToolOutcome{}, mapKanbanError(err)
				}
				count = len(pg.Items)
				impact := map[string]any{
					"column":         name,
					"migrate_to":     target,
					"task_count":     count,
					"count_at_least": pg.NextCursor != "",
				}
				return pendingOut("删除看板列", "删除列「"+name+"」并把 "+itoa(count)+" 个任务迁移到「"+target+"」。", impact, projectID)
			}),

		tool(mod, mcp.CategoryRead, "checklist_list", "检查项列表",
			"列出任务的检查项。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"task_id":    sStr("任务 ID"),
			}, "project_id", "task_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, taskID, err := projectAndID(a, "task_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				m, err := d.kanbanModule(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				detail, err := m.Tasks().Get(ctx, taskID)
				if err != nil {
					return mcp.ToolOutcome{}, mapKanbanError(err)
				}
				return okOut(map[string]any{"items": checklistViews(detail.Checklist)})
			}),

		tool(mod, mcp.CategoryWrite, "checklist_add", "添加检查项",
			"为任务添加检查项（不改变父任务 updated_at，不产生 Activity）。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"task_id":    sStr("任务 ID"),
				"text":       sStrLen("检查项文本", 1, 500),
				"before_id":  sStr("插入到该检查项之前"),
			}, "project_id", "task_id", "text"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, taskID, err := projectAndID(a, "task_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				text, err := requireString(a, "text")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				var before *string
				if v, ok := a.String("before_id"); ok && v != "" {
					before = &v
				}
				m, err := d.kanbanModule(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				item, err := m.Checklist().Add(ctx, eventActor(), taskID, text, before)
				if err != nil {
					return mcp.ToolOutcome{}, mapKanbanError(err)
				}
				return okOut(map[string]any{"item": checklistView(item)})
			}),

		tool(mod, mcp.CategoryWrite, "checklist_update", "修改检查项",
			"修改检查项文本。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"item_id":    sStr("检查项 ID"),
				"text":       sStrLen("检查项文本", 1, 500),
			}, "project_id", "item_id", "text"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, itemID, err := projectAndID(a, "item_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				text, err := requireString(a, "text")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				m, err := d.kanbanModule(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				item, err := m.Checklist().Update(ctx, eventActor(), itemID, text)
				if err != nil {
					return mcp.ToolOutcome{}, mapKanbanError(err)
				}
				return okOut(map[string]any{"item": checklistView(item)})
			}),

		tool(mod, mcp.CategoryWrite, "checklist_set_done", "勾选检查项",
			"设置检查项完成状态。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"item_id":    sStr("检查项 ID"),
				"done":       sBool("是否完成"),
			}, "project_id", "item_id", "done"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, itemID, err := projectAndID(a, "item_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				done, ok := a.Bool("done")
				if !ok {
					return mcp.ToolOutcome{}, invalidf("缺少必填参数 done")
				}
				m, err := d.kanbanModule(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				item, err := m.Checklist().SetDone(ctx, eventActor(), itemID, done)
				if err != nil {
					return mcp.ToolOutcome{}, mapKanbanError(err)
				}
				return okOut(map[string]any{"item": checklistView(item)})
			}),

		tool(mod, mcp.CategoryWrite, "checklist_remove", "删除检查项",
			"物理删除检查项。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"item_id":    sStr("检查项 ID"),
			}, "project_id", "item_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, itemID, err := projectAndID(a, "item_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				m, err := d.kanbanModule(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if err := m.Checklist().Remove(ctx, eventActor(), itemID); err != nil {
					return mcp.ToolOutcome{}, mapKanbanError(err)
				}
				return okOut(map[string]any{"removed": true})
			}),

		tool(mod, mcp.CategoryWrite, "checklist_reorder", "重排检查项",
			"按给定顺序重排任务的检查项（必须覆盖全部检查项）。",
			sObj(map[string]any{
				"project_id":  sStr("项目 ID"),
				"task_id":     sStr("任务 ID"),
				"ordered_ids": sArr("检查项 ID 顺序", sStr("检查项 ID")),
			}, "project_id", "task_id", "ordered_ids"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, taskID, err := projectAndID(a, "task_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				ids, _, err := a.Strings("ordered_ids")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				m, err := d.kanbanModule(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if err := m.Checklist().Reorder(ctx, eventActor(), taskID, ids); err != nil {
					return mcp.ToolOutcome{}, mapKanbanError(err)
				}
				detail, err := m.Tasks().Get(ctx, taskID)
				if err != nil {
					return mcp.ToolOutcome{}, mapKanbanError(err)
				}
				return okOut(map[string]any{"items": checklistViews(detail.Checklist)})
			}),
	}
	return &provider{module: mod, tools: tools, ready: ready}
}

func taskView(t kanban.Task) map[string]any {
	return map[string]any{
		"task_id":        t.ID,
		"title":          t.Title,
		"column":         t.Column,
		"parent_task_id": deref(t.ParentTaskID),
		"description_md": t.DescriptionMD,
		"priority":       string(t.Priority),
		"deadline":       deref(t.Deadline),
		"blocked":        t.Blocked,
		"labels":         t.Labels,
		"week_plan":      deref(t.WeekPlan),
		"completed_at":   tsp(t.CompletedAt),
		"archived_at":    tsp(t.ArchivedAt),
		"deleted_at":     tsp(t.DeletedAt),
		"created_at":     ts(t.CreatedAt),
		"updated_at":     ts(t.UpdatedAt),
	}
}

func taskSummaryView(t kanban.TaskSummary) map[string]any {
	out := map[string]any{
		"task_id":         t.ID,
		"title":           t.Title,
		"column":          t.Column,
		"parent_task_id":  deref(t.ParentTaskID),
		"priority":        string(t.Priority),
		"deadline":        deref(t.Deadline),
		"blocked":         t.Blocked,
		"labels":          t.Labels,
		"subtask_done":    t.SubtaskDone,
		"subtask_total":   t.SubtaskTotal,
		"checklist_done":  t.ChecklistDone,
		"checklist_total": t.ChecklistTotal,
		"link_count":      t.LinkCount,
		"completed_at":    tsp(t.CompletedAt),
		"archived_at":     tsp(t.ArchivedAt),
		"updated_at":      ts(t.UpdatedAt),
	}
	return out
}

func taskDetailView(d kanban.TaskDetail) map[string]any {
	out := taskView(d.Task)
	out["column_detail"] = columnView(d.Column)
	out["subtasks"] = taskSummaryViews(d.Subtasks)
	out["checklist"] = checklistViews(d.Checklist)
	links := make([]any, 0, len(d.Links))
	for _, l := range d.Links {
		links = append(links, map[string]any{
			"relation_id":  l.RelationID,
			"relation":     string(l.Relation),
			"dst_type":     string(l.DstType),
			"dst_id":       l.DstID,
			"dst_title":    l.DstTitle,
			"target_state": targetStateName(l.TargetState),
		})
	}
	out["links"] = links
	return out
}

func taskSummaryViews(list []kanban.TaskSummary) []any {
	out := make([]any, 0, len(list))
	for _, t := range list {
		out = append(out, taskSummaryView(t))
	}
	return out
}

func columnView(c kanban.Column) map[string]any {
	return map[string]any{
		"name":     c.Name,
		"kind":     c.Kind,
		"is_done":  c.IsDone,
		"position": c.Position,
	}
}

func columnViews(cols []kanban.Column) []any {
	out := make([]any, 0, len(cols))
	for _, c := range cols {
		out = append(out, columnView(c))
	}
	return out
}

func checklistView(i kanban.ChecklistItem) map[string]any {
	return map[string]any{
		"item_id":    i.ID,
		"task_id":    i.TaskID,
		"text":       i.Text,
		"done":       i.Done,
		"sort_order": i.SortOrder,
	}
}

func checklistViews(items []kanban.ChecklistItem) []any {
	out := make([]any, 0, len(items))
	for _, i := range items {
		out = append(out, checklistView(i))
	}
	return out
}

func targetStateName(s relations.TargetState) string {
	switch s {
	case relations.TargetAlive:
		return "alive"
	case relations.TargetSoftDeleted:
		return "deleted"
	case relations.TargetMissing:
		return "missing"
	default:
		return ""
	}
}

// mapKanbanError maps SPEC-08 sentinels to MCP codes.
func mapKanbanError(err error) error {
	if err == nil {
		return nil
	}
	var me *mcp.Error
	if errors.As(err, &me) {
		return me
	}
	switch {
	case errors.Is(err, kanban.ErrTaskNotFound), errors.Is(err, kanban.ErrColumnNotFound):
		return mcp.Errorf(mcp.CodeNotFound, "任务或列不存在").Wrap(err)
	case errors.Is(err, kanban.ErrStaleRevision):
		return mcp.Errorf(mcp.CodeConflict, "任务已被修改，请重新读取后再提交").Wrap(err)
	case errors.Is(err, kanban.ErrTaskArchived):
		return mcp.Errorf(mcp.CodeConflict, "任务已归档，请先解除归档").Wrap(err)
	case errors.Is(err, kanban.ErrProjectArchived):
		return mcp.Errorf(mcp.CodeArchivedReadOnly, "项目已归档，不能写入").Wrap(err)
	case errors.Is(err, kanban.ErrInvalidInput), errors.Is(err, kanban.ErrInvalidParent),
		errors.Is(err, kanban.ErrSubtaskOnSubtask), errors.Is(err, kanban.ErrColumnNotEmpty),
		errors.Is(err, kanban.ErrLastDoneColumn), errors.Is(err, kanban.ErrLastColumn),
		errors.Is(err, kanban.ErrDuplicateColumnName), errors.Is(err, kanban.ErrTaskLimitExceeded):
		return mcp.Errorf(mcp.CodeInvalidArgument, "%s", kanbanMessage(err)).Wrap(err)
	}
	return asMCPerror(err, mcp.CodeInternal, "看板模块执行失败")
}

func kanbanMessage(err error) string {
	msg := err.Error()
	return msg
}
