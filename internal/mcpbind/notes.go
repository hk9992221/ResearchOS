package mcpbind

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"mime"
	"path/filepath"
	"strings"

	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/notefile"
	"researchos/internal/foundation/relations"
	"researchos/internal/foundation/trash"
	"researchos/internal/mcp"
	"researchos/internal/notes"
)

// noteServices bundles the SPEC-05 services of one project.
type noteServices struct {
	notes       notes.NoteService
	databases   notes.DatabaseService
	records     notes.RecordService
	attachments notes.AttachmentService
	trash       *trash.Service
}

func (d *Deps) noteServices(ctx context.Context, projectID string) (*noteServices, error) {
	h, err := d.handle(ctx, projectID)
	if err != nil {
		return nil, err
	}
	c := d.clock()
	trashSvc := trash.New(h.ID, h.DB(), d.Workspace.Paths(), h.Events(), d.Confirm, c)
	opt := notes.ServiceOptions{Clock: c, Logger: d.Logger}
	noteStore := notefile.New(h.ID, h.DB(), d.Workspace.Paths(), h.Events(), c)
	relSvc := relations.New(h.DB(), d.Workspace.Global().DB(), h.Events(), c)
	return &noteServices{
		notes:       notes.NewNoteService(d.Workspace, projectID, opt),
		databases:   notes.NewDatabaseService(h.DB(), h.Events(), trashSvc, opt),
		records:     notes.NewRecordService(h.DB(), projectID, h.Events(), trashSvc, noteStore, relSvc, opt),
		attachments: notes.NewAttachmentService(projectID, h.DB(), d.Workspace.Paths(), c),
		trash:       trashSvc,
	}, nil
}

// newNotesProvider builds the SPEC-05 Notes/Database/Record domain (§5.3.3).
func newNotesProvider(d Deps, ready func(context.Context) error) mcp.ToolProvider {
	mod := mcp.ModuleNotes
	tools := []mcp.ToolBinding{
		tool(mod, mcp.CategoryRead, "note_list", "笔记列表",
			"列出项目笔记，可按类型、标题关键字、标签过滤。",
			sObj(map[string]any{
				"project_id":      sStr("项目 ID"),
				"kind":            sEnum("笔记类型", "scratchpad", "paper_note", "db_record", "overview"),
				"q":               sStr("标题关键字"),
				"include_deleted": sBool("包含已软删除的笔记"),
				"limit":           sInt("返回条数，默认 50，上限 200", 1, 200),
				"cursor":          sStr("上一页返回的 next_cursor"),
			}, "project_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.noteServices(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				offset, err := cursorOffset(stringsOf(a, "cursor"))
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				limit := limitOf(a, 50, 200)
				q := notes.ListNotesQuery{
					TitleLike:      stringsOf(a, "q"),
					IncludeDeleted: a.BoolDefault("include_deleted", false),
					Limit:          limit,
					Offset:         offset,
				}
				if kind, ok := a.String("kind"); ok {
					q.Kind = notes.NoteKind(kind)
				}
				pg, err := svc.notes.List(ctx, q)
				if err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				items := make([]any, 0, len(pg.Notes))
				for _, n := range pg.Notes {
					items = append(items, noteSummaryView(n))
				}
				return okOut(pageOf(items, nextOffsetCursor(offset, limit, pg.Total)))
			}),

		tool(mod, mcp.CategoryRead, "note_get", "笔记详情",
			"返回笔记元数据、Markdown 正文与附件列表。",
			sObj(map[string]any{
				"project_id":   sStr("项目 ID"),
				"note_id":      sStr("笔记 ID"),
				"include_body": sBool("是否返回正文，默认 true"),
			}, "project_id", "note_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, noteID, err := projectAndID(a, "note_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.noteServices(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				note, err := svc.notes.Get(ctx, noteID)
				if err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				out := map[string]any{"note": noteView(note)}
				if a.BoolDefault("include_body", true) {
					body, err := svc.notes.LoadBody(ctx, noteID)
					if err != nil {
						return mcp.ToolOutcome{}, mapNotesError(err)
					}
					out["body_md"] = body.Markdown
					out["body_hash"] = body.Hash
					out["body_state"] = string(body.State)
				}
				attachments, err := svc.attachments.ListAttachments(ctx, noteID)
				if err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				list := make([]any, 0, len(attachments))
				for _, att := range attachments {
					list = append(list, map[string]any{
						"path":          att.RelativePath,
						"markdown_path": att.MarkdownPath,
						"mime":          mimeType(att.RelativePath),
						"sha256":        att.SHA256,
						"bytes":         att.Bytes,
					})
				}
				out["attachments"] = list
				return okOut(out)
			}),

		tool(mod, mcp.CategoryWrite, "note_create", "创建自由笔记",
			"创建一篇自由笔记（scratchpad）。论文笔记、记录页与文献综述请使用各自专用工具。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"title":      sStrLen("标题", 1, 200),
				"kind":       sEnum("笔记类型（仅允许 scratchpad）", "scratchpad"),
				"body_md":    sStr("Markdown 正文"),
			}, "project_id", "title", "kind"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				title, err := requireString(a, "title")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				kind, err := requireString(a, "kind")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if kind != string(notes.KindScratchpad) {
					return mcp.ToolOutcome{}, invalidf("note_create 只允许 kind=scratchpad")
				}
				body, _ := a.String("body_md")
				svc, err := d.noteServices(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				note, err := svc.notes.Create(ctx, notes.CreateNoteInput{
					Kind:  notes.KindScratchpad,
					Title: title,
					Body:  body,
					Actor: eventActor(),
				})
				if err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				return okOut(map[string]any{"note": noteView(note)})
			}),

		tool(mod, mcp.CategoryWrite, "note_update_title", "修改笔记标题",
			"修改笔记标题。正文请使用 note_save_body。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"note_id":    sStr("笔记 ID"),
				"title":      sStrLen("新标题", 1, 200),
			}, "project_id", "note_id", "title"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, noteID, err := projectAndID(a, "note_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				title, err := requireString(a, "title")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.noteServices(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				note, err := svc.notes.UpdateTitle(ctx, noteID, title, eventActor())
				if err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				return okOut(map[string]any{"note": noteView(note)})
			}),

		tool(mod, mcp.CategoryWrite, "note_save_body", "保存笔记正文",
			"以乐观并发保存笔记正文。expected_hash 取自 note_get 的 body_hash；不一致返回 CONFLICT，不覆盖。同日多次保存合并为一条 note.updated。",
			sObj(map[string]any{
				"project_id":    sStr("项目 ID"),
				"note_id":       sStr("笔记 ID"),
				"body_md":       sStrLen("Markdown 正文", 0, mcp.MaxBodyBytes),
				"expected_hash": sStr("note_get 返回的 body_hash"),
			}, "project_id", "note_id", "body_md", "expected_hash"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, noteID, err := projectAndID(a, "note_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				body, err := requireString(a, "body_md")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				hash, err := requireString(a, "expected_hash")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.noteServices(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				res, err := svc.notes.SaveBody(ctx, notes.SaveBodyInput{
					NoteID:   noteID,
					Markdown: body,
					BaseHash: hash,
					Actor:    eventActor(),
				})
				if err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				if res.Conflict {
					return mcp.ToolOutcome{}, mcp.Errorf(mcp.CodeConflict, "笔记正文已被其他写入修改，请重新读取后再保存").
						WithDetail("disk_hash", res.DiskHash)
				}
				note, err := svc.notes.Get(ctx, noteID)
				if err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				return okOut(map[string]any{"note": noteView(note), "body_hash": res.Hash})
			}),

		tool(mod, mcp.CategoryWrite, "note_delete", "删除笔记",
			"软删除笔记（进入回收站，可恢复）。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"note_id":    sStr("笔记 ID"),
			}, "project_id", "note_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, noteID, err := projectAndID(a, "note_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.noteServices(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if err := svc.notes.SoftDelete(ctx, noteID, eventActor()); err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				return okOut(map[string]any{"deleted": true})
			}),

		tool(mod, mcp.CategoryRead, "database_list", "数据库列表",
			"列出项目的 Research Database。",
			sObj(map[string]any{
				"project_id":      sStr("项目 ID"),
				"include_deleted": sBool("包含已删除的数据库"),
			}, "project_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, err := requireID(a, "project_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.noteServices(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				dbs, err := svc.databases.ListDatabases(ctx, a.BoolDefault("include_deleted", false))
				if err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				items := make([]any, 0, len(dbs))
				for _, db := range dbs {
					items = append(items, databaseView(db))
				}
				return okOut(map[string]any{"databases": items})
			}),

		tool(mod, mcp.CategoryRead, "database_get", "数据库详情",
			"返回数据库定义、字段列表与记录数。",
			sObj(map[string]any{
				"project_id":  sStr("项目 ID"),
				"database_id": sStr("数据库 ID"),
			}, "project_id", "database_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, dbID, err := projectAndID(a, "database_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.noteServices(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				db, err := svc.databases.GetDatabase(ctx, dbID)
				if err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				fields, err := svc.databases.ListFields(ctx, dbID)
				if err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				fieldViews := make([]any, 0, len(fields))
				for _, f := range fields {
					fieldViews = append(fieldViews, fieldView(f))
				}
				page, err := svc.records.ListRecords(ctx, notes.RecordQuery{DatabaseID: dbID, Limit: 1})
				if err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				return okOut(map[string]any{
					"database":     databaseView(db),
					"fields":       fieldViews,
					"record_count": page.Total,
				})
			}),

		tool(mod, mcp.CategoryWrite, "database_create", "创建数据库",
			"创建 Research Database，可同时创建初始字段。",
			sObj(map[string]any{
				"project_id":  sStr("项目 ID"),
				"name":        sStrLen("数据库名称", 1, 200),
				"description": sStr("描述"),
				"fields":      sArr("初始字段", sFreeObj("字段 {name*, type*, options?}", "name", "type")),
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
				desc, _ := a.String("description")
				svc, err := d.noteServices(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				db, err := svc.databases.CreateDatabase(ctx, notes.CreateDatabaseInput{
					Name:        name,
					Description: desc,
					Actor:       eventActor(),
				})
				if err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				specs, _, err := a.Objects("fields")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				created := make([]any, 0, len(specs))
				for _, spec := range specs {
					field, err := svc.databases.CreateField(ctx, fieldInput(db.ID, spec, eventActor()))
					if err != nil {
						return mcp.ToolOutcome{}, mapNotesError(err)
					}
					created = append(created, fieldView(field))
				}
				return okOut(map[string]any{"database": databaseView(db), "fields": created})
			}),

		tool(mod, mcp.CategoryWrite, "database_update", "重命名数据库",
			"重命名 Research Database。",
			sObj(map[string]any{
				"project_id":  sStr("项目 ID"),
				"database_id": sStr("数据库 ID"),
				"name":        sStrLen("新名称", 1, 200),
			}, "project_id", "database_id", "name"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, dbID, err := projectAndID(a, "database_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				name, err := requireString(a, "name")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.noteServices(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				db, err := svc.databases.RenameDatabase(ctx, dbID, name, eventActor())
				if err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				return okOut(map[string]any{"database": databaseView(db)})
			}),

		tool(mod, mcp.CategoryWrite, "database_field_create", "新增字段",
			"为数据库新增字段。类型创建后不可修改。",
			sObj(map[string]any{
				"project_id":  sStr("项目 ID"),
				"database_id": sStr("数据库 ID"),
				"name":        sStrLen("字段名称", 1, 64),
				"type":        sEnum("字段类型", "text", "number", "select", "multi_select", "date", "checkbox", "url", "relation", "tags"),
				"options":     sAny("字段配置（如选项数组）"),
			}, "project_id", "database_id", "name", "type"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, dbID, err := projectAndID(a, "database_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				name, err := requireString(a, "name")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				typ, err := requireString(a, "type")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				config, err := configJSON(a, "options")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.noteServices(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				field, err := svc.databases.CreateField(ctx, notes.CreateFieldInput{
					DatabaseID: dbID,
					Name:       name,
					Type:       notes.FieldType(typ),
					Config:     config,
					Actor:      eventActor(),
				})
				if err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				return okOut(map[string]any{"field": fieldView(field)})
			}),

		tool(mod, mcp.CategoryWrite, "database_field_update", "更新字段",
			"更新字段名称或选项。字段类型不可修改。",
			sObj(map[string]any{
				"project_id":          sStr("项目 ID"),
				"database_id":         sStr("数据库 ID"),
				"field_id":            sStr("字段 ID"),
				"name":                sStr("新名称"),
				"options":             sAny("字段配置"),
				"expected_updated_at": sStr("并发令牌（可选）"),
			}, "project_id", "database_id", "field_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, _, err := projectAndID(a, "database_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				fieldID, err := requireString(a, "field_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				name, _ := a.String("name")
				config, err := configJSON(a, "options")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if name == "" && config == nil {
					return mcp.ToolOutcome{}, invalidf("至少提供 name 或 options")
				}
				svc, err := d.noteServices(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				field, err := svc.databases.UpdateField(ctx, notes.UpdateFieldInput{
					FieldID: fieldID,
					Name:    name,
					Config:  config,
					Actor:   eventActor(),
				})
				if err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				return okOut(map[string]any{"field": fieldView(field)})
			}),

		tool(mod, mcp.CategoryWrite, "database_field_delete", "删除字段",
			"软删除字段，已录入的值保留。",
			sObj(map[string]any{
				"project_id":          sStr("项目 ID"),
				"database_id":         sStr("数据库 ID"),
				"field_id":            sStr("字段 ID"),
				"expected_updated_at": sStr("并发令牌（可选）"),
			}, "project_id", "database_id", "field_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, _, err := projectAndID(a, "database_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				fieldID, err := requireString(a, "field_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.noteServices(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if err := svc.databases.DeleteField(ctx, fieldID, eventActor()); err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				return okOut(map[string]any{"deleted": true})
			}),

		tool(mod, mcp.CategoryWrite, "database_delete", "删除数据库",
			"软删除数据库（进入回收站，字段与记录保留）。",
			sObj(map[string]any{
				"project_id":  sStr("项目 ID"),
				"database_id": sStr("数据库 ID"),
			}, "project_id", "database_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, dbID, err := projectAndID(a, "database_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.noteServices(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if err := svc.databases.SoftDeleteDatabase(ctx, dbID, eventActor()); err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				return okOut(map[string]any{"deleted": true})
			}),

		tool(mod, mcp.CategoryRead, "record_list", "记录列表",
			"列出数据库记录，可带过滤树与排序。",
			sObj(map[string]any{
				"project_id":      sStr("项目 ID"),
				"database_id":     sStr("数据库 ID"),
				"filter":          sAny("过滤树 JSON"),
				"sort":            sArr("排序 [{field_id, desc}]", sFreeObj("排序键", "field_id")),
				"include_deleted": sBool("包含已删除记录"),
				"limit":           sInt("返回条数，默认 50，上限 200", 1, 200),
				"cursor":          sStr("上一页返回的 next_cursor"),
			}, "project_id", "database_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, dbID, err := projectAndID(a, "database_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				offset, err := cursorOffset(stringsOf(a, "cursor"))
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				limit := limitOf(a, 50, 200)
				q := notes.RecordQuery{
					DatabaseID:     dbID,
					IncludeDeleted: a.BoolDefault("include_deleted", false),
					Limit:          limit,
					Offset:         offset,
				}
				if raw, ok := a.Obj["filter"]; ok && raw != nil {
					body, err := json.Marshal(raw)
					if err != nil {
						return mcp.ToolOutcome{}, invalidf("filter 无效")
					}
					var node notes.FilterNode
					if err := json.Unmarshal(body, &node); err != nil {
						return mcp.ToolOutcome{}, invalidf("filter 无效")
					}
					q.Filter = &node
				}
				sorts, _, err := a.Objects("sort")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				for _, s := range sorts {
					fieldID, _ := s["field_id"].(string)
					desc, _ := s["desc"].(bool)
					q.Sorts = append(q.Sorts, notes.SortKey{FieldID: fieldID, Desc: desc})
				}
				svc, err := d.noteServices(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				pg, err := svc.records.ListRecords(ctx, q)
				if err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				items := make([]any, 0, len(pg.Records))
				for _, r := range pg.Records {
					items = append(items, recordView(r))
				}
				return okOut(pageOf(items, nextOffsetCursor(offset, limit, pg.Total)))
			}),

		tool(mod, mcp.CategoryRead, "record_get", "记录详情",
			"返回一条记录及其字段值与关联笔记 ID。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"record_id":  sStr("记录 ID"),
			}, "project_id", "record_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, recordID, err := projectAndID(a, "record_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.noteServices(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				record, err := svc.records.GetRecord(ctx, recordID)
				if err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				return okOut(map[string]any{
					"record":  recordView(record),
					"values":  json.RawMessage(record.Values),
					"note_id": record.NoteID,
				})
			}),

		tool(mod, mcp.CategoryWrite, "record_create", "创建记录",
			"在数据库中创建一条记录，服务会同时创建记录页笔记。",
			sObj(map[string]any{
				"project_id":  sStr("项目 ID"),
				"database_id": sStr("数据库 ID"),
				"values":      sFreeObj("字段值 {field_id: value}"),
			}, "project_id", "database_id", "values"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, dbID, err := projectAndID(a, "database_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				values, ok := a.Object("values")
				if !ok {
					return mcp.ToolOutcome{}, invalidf("values 必须是对象")
				}
				body, err := json.Marshal(values)
				if err != nil {
					return mcp.ToolOutcome{}, invalidf("values 无效")
				}
				svc, err := d.noteServices(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				record, err := svc.records.CreateRecord(ctx, notes.CreateRecordInput{
					DatabaseID: dbID,
					Values:     body,
					Actor:      eventActor(),
				})
				if err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				return okOut(map[string]any{"record": recordView(record), "note_id": record.NoteID})
			}),

		tool(mod, mcp.CategoryWrite, "record_update", "更新记录",
			"更新记录中提供的字段值（未提供的字段保持不变）。",
			sObj(map[string]any{
				"project_id":          sStr("项目 ID"),
				"record_id":           sStr("记录 ID"),
				"values":              sFreeObj("字段值 {field_id: value}"),
				"expected_updated_at": sStr("并发令牌（可选）"),
			}, "project_id", "record_id", "values"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, recordID, err := projectAndID(a, "record_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				values, ok := a.Object("values")
				if !ok {
					return mcp.ToolOutcome{}, invalidf("values 必须是对象")
				}
				body, err := json.Marshal(values)
				if err != nil {
					return mcp.ToolOutcome{}, invalidf("values 无效")
				}
				svc, err := d.noteServices(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				record, err := svc.records.UpdateValues(ctx, notes.UpdateValuesInput{
					RecordID: recordID,
					Values:   body,
					Actor:    eventActor(),
				})
				if err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				return okOut(map[string]any{"record": recordView(record)})
			}),

		tool(mod, mcp.CategoryWrite, "record_delete", "删除记录",
			"软删除记录，可选择同时删除记录页笔记。",
			sObj(map[string]any{
				"project_id":  sStr("项目 ID"),
				"record_id":   sStr("记录 ID"),
				"delete_note": sBool("同时删除记录页笔记"),
			}, "project_id", "record_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				projectID, recordID, err := projectAndID(a, "record_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				svc, err := d.noteServices(ctx, projectID)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if err := svc.records.SoftDeleteRecord(ctx, recordID, a.BoolDefault("delete_note", false), eventActor()); err != nil {
					return mcp.ToolOutcome{}, mapNotesError(err)
				}
				return okOut(map[string]any{"deleted": true})
			}),
	}
	return &provider{module: mod, tools: tools, ready: ready}
}

func projectAndID(a *mcp.Arguments, idName string) (string, string, error) {
	projectID, err := requireID(a, "project_id")
	if err != nil {
		return "", "", err
	}
	id, err := requireID(a, idName)
	if err != nil {
		return "", "", err
	}
	return projectID, id, nil
}

func sFreeObj(desc string, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	sortStrings(required)
	return map[string]any{
		"type":                 "object",
		"description":          desc,
		"additionalProperties": true,
		"required":             required,
	}
}

func sortStrings(list []string) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j] < list[j-1]; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

func configJSON(a *mcp.Arguments, name string) (json.RawMessage, error) {
	raw, ok := a.Obj[name]
	if !ok || raw == nil {
		return nil, nil
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return nil, invalidf("%s 无法序列化", name)
	}
	return body, nil
}

func fieldInput(databaseID string, spec map[string]any, actor events.Actor) notes.CreateFieldInput {
	name, _ := spec["name"].(string)
	typ, _ := spec["type"].(string)
	var config json.RawMessage
	if opts, ok := spec["options"]; ok && opts != nil {
		config, _ = json.Marshal(opts)
	}
	return notes.CreateFieldInput{
		DatabaseID: databaseID,
		Name:       name,
		Type:       notes.FieldType(typ),
		Config:     config,
		Actor:      actor,
	}
}

func noteView(n *notes.Note) map[string]any {
	if n == nil {
		return nil
	}
	return map[string]any{
		"note_id":      n.ID,
		"kind":         string(n.Kind),
		"title":        n.Title,
		"file_path":    n.FilePath,
		"body_state":   string(n.BodyState),
		"content_hash": n.ContentHash,
		"created_at":   ts(n.CreatedAt),
		"updated_at":   ts(n.UpdatedAt),
		"deleted_at":   tsp(n.DeletedAt),
	}
}

func noteSummaryView(n *notes.Note) map[string]any {
	return noteView(n)
}

func databaseView(db *notes.ResearchDatabase) map[string]any {
	if db == nil {
		return nil
	}
	return map[string]any{
		"database_id": db.ID,
		"name":        db.Name,
		"description": db.Description,
		"created_at":  ts(db.CreatedAt),
		"updated_at":  ts(db.UpdatedAt),
		"deleted_at":  tsp(db.DeletedAt),
	}
}

func fieldView(f *notes.Field) map[string]any {
	if f == nil {
		return nil
	}
	out := map[string]any{
		"field_id":    f.ID,
		"database_id": f.DatabaseID,
		"name":        f.Name,
		"type":        string(f.Type),
		"role":        string(f.Role),
		"position":    f.Position,
		"created_at":  ts(f.CreatedAt),
		"updated_at":  ts(f.UpdatedAt),
		"deleted_at":  tsp(f.DeletedAt),
	}
	if len(f.Config) > 0 {
		out["config"] = json.RawMessage(f.Config)
	}
	return out
}

func recordView(r *notes.Record) map[string]any {
	if r == nil {
		return nil
	}
	return map[string]any{
		"record_id":   r.ID,
		"database_id": r.DatabaseID,
		"note_id":     r.NoteID,
		"values":      json.RawMessage(r.Values),
		"created_at":  ts(r.CreatedAt),
		"updated_at":  ts(r.UpdatedAt),
		"deleted_at":  tsp(r.DeletedAt),
	}
}

// mapNotesError maps the SPEC-05 sentinel family to MCP codes.
// mimeType is a best-effort media type for an attachment path. The service
// stores no MIME column, so an unknown extension stays empty.
func mimeType(path string) string {
	return mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
}

func mapNotesError(err error) error {
	if err == nil {
		return nil
	}
	var me *mcp.Error
	if errors.As(err, &me) {
		return me
	}
	switch {
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, errs.ErrEntityDeleted), errors.Is(err, errs.ErrNoteFileMissing):
		return mcp.Errorf(mcp.CodeNotFound, "目标不存在或已删除").Wrap(err)
	case errors.Is(err, errs.ErrBadEntityID), errors.Is(err, errs.ErrBadEntityType), errors.Is(err, errs.ErrBadActor):
		return mcp.Errorf(mcp.CodeInvalidArgument, "实体标识或参数不合法").Wrap(err)
	case errors.Is(err, errs.ErrRelationNotAllowed), errors.Is(err, errs.ErrSelfRelation):
		return mcp.Errorf(mcp.CodeInvalidArgument, "关系类型不被允许").Wrap(err)
	case errors.Is(err, errs.ErrUserConfirmationRequired), errors.Is(err, errs.ErrConfirmScopeMismatch):
		return mcp.Errorf(mcp.CodeCapabilityInvalid, "该操作需要应用内人工确认").Wrap(err)
	case errors.Is(err, notes.ErrAttachmentValidation), errors.Is(err, notes.ErrTagValidation):
		return mcp.Errorf(mcp.CodeInvalidArgument, "附件或标签参数不合法").Wrap(err)
	}
	return asMCPerror(err, mcp.CodeInternal, "笔记模块执行失败")
}
