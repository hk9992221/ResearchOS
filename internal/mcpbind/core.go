package mcpbind

import (
	"context"
	"encoding/json"

	"researchos/internal/mcp"
)

// newCoreProvider builds the always-available runtime tools: server status and
// the confirmation/job handles (SPEC-11 §5.3.1, §5.3.8).
func newCoreProvider(d Deps, ready func(context.Context) error) mcp.ToolProvider {
	mod := mcp.ModuleCore
	tools := []mcp.ToolBinding{
		tool(mod, mcp.CategoryRead, "server_status", "服务器状态",
			"返回 ResearchOS MCP 服务器的版本、协议版本、访问模式、工作区路径、已就绪模块与 AI/Zotero 可用性。",
			sObj(nil), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				status, err := statusOf(ctx, d)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				return okOut(status)
			}),

		tool(mod, mcp.CategoryRead, "confirmation_list", "确认请求列表",
			"列出当前客户端提交的确认请求（可按状态、项目过滤），按创建时间倒序。MCP 只能查看与取消，不能批准。",
			sObj(map[string]any{
				"state":      sEnum("确认状态", "requested", "approved", "executing", "executed", "failed", "denied", "expired", "cancelled"),
				"project_id": sStr("项目 ID"),
				"limit":      sInt("返回条数，默认 50，上限 200", 1, 200),
			}), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				if d.Confirmations == nil {
					return mcp.ToolOutcome{}, unavailablef(mcp.CodeInternal, "确认队列不可用")
				}
				f := mcp.ConfirmationFilter{}
				if s, ok := a.String("state"); ok {
					f.State = mcp.ConfirmationState(s)
				}
				f.ProjectID, _ = a.String("project_id")
				f.Limit = limitOf(a, 50, 200)
				list, err := d.Confirmations.List(ctx, p, f)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				items := make([]any, 0, len(list))
				for _, c := range list {
					items = append(items, confirmationView(c))
				}
				return okOut(map[string]any{"confirmations": items})
			}),

		tool(mod, mcp.CategoryRead, "confirmation_get", "确认请求详情",
			"返回一个确认请求的状态、影响摘要、结果或错误。该工具只读；MCP 不存在批准路径。",
			sObj(map[string]any{"confirmation_id": sStr("确认 ID")}, "confirmation_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				if d.Confirmations == nil {
					return mcp.ToolOutcome{}, unavailablef(mcp.CodeInternal, "确认队列不可用")
				}
				id, err := requireString(a, "confirmation_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				c, err := d.Confirmations.Get(ctx, p, id)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				return okOut(confirmationView(c))
			}),

		tool(mod, mcp.CategoryWrite, "confirmation_cancel", "取消确认请求",
			"取消本客户端发起、仍处于 requested 状态的确认请求。已处理或属于其他客户端的请求返回冲突。",
			sObj(map[string]any{"confirmation_id": sStr("确认 ID")}, "confirmation_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				if d.Confirmations == nil {
					return mcp.ToolOutcome{}, unavailablef(mcp.CodeInternal, "确认队列不可用")
				}
				id, err := requireString(a, "confirmation_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if err := d.Confirmations.CancelByClient(ctx, p, id); err != nil {
					return mcp.ToolOutcome{}, err
				}
				return okOut(map[string]any{"confirmation_id": id, "state": string(mcp.ConfirmationCancelled)})
			}),

		tool(mod, mcp.CategoryRead, "job_list", "后台作业列表",
			"列出当前客户端提交的后台作业（可按项目、状态过滤），按创建时间倒序。",
			sObj(map[string]any{
				"project_id": sStr("项目 ID"),
				"state":      sEnum("作业状态", "queued", "running", "succeeded", "failed", "cancelled"),
				"limit":      sInt("返回条数，默认 50，上限 200", 1, 200),
			}), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				if d.Jobs == nil {
					return mcp.ToolOutcome{}, unavailablef(mcp.CodeInternal, "作业运行器不可用")
				}
				f := mcp.JobFilter{}
				f.ProjectID, _ = a.String("project_id")
				if s, ok := a.String("state"); ok {
					f.State = mcp.JobState(s)
				}
				f.Limit = limitOf(a, 50, 200)
				list, err := d.Jobs.List(ctx, p, f)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				items := make([]any, 0, len(list))
				for _, j := range list {
					items = append(items, jobView(j, false))
				}
				return okOut(map[string]any{"jobs": items})
			}),

		tool(mod, mcp.CategoryRead, "job_get", "后台作业详情",
			"返回后台作业的状态、进度、阶段、结果或错误。",
			sObj(map[string]any{"job_id": sStr("作业 ID")}, "job_id"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				if d.Jobs == nil {
					return mcp.ToolOutcome{}, unavailablef(mcp.CodeInternal, "作业运行器不可用")
				}
				id, err := requireString(a, "job_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				j, err := d.Jobs.Get(ctx, p, id)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				return okOut(jobView(j, true))
			}),

		tool(mod, mcp.CategoryWrite, "job_cancel", "取消后台作业",
			"请求取消一个排队或运行中的后台作业。取消是尽力而为：已提交的文件写入不可回滚，但不会留下半成品业务实体。",
			sObj(map[string]any{"job_id": sStr("作业 ID")}, "job_id"), annWrite(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				if d.Jobs == nil {
					return mcp.ToolOutcome{}, unavailablef(mcp.CodeInternal, "作业运行器不可用")
				}
				id, err := requireString(a, "job_id")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if err := d.Jobs.Cancel(ctx, p, id); err != nil {
					return mcp.ToolOutcome{}, err
				}
				return okOut(map[string]any{"job_id": id, "state": "cancelling"})
			}),
	}
	return &provider{module: mod, tools: tools, ready: ready}
}

func statusOf(ctx context.Context, d Deps) (mcp.Status, error) {
	if d.Runtime != nil {
		return d.Runtime.Status(ctx)
	}
	return mcp.Status{
		AppVersion:      d.Version,
		MCPVersions:     []string{mcp.ProtocolVersion20260728, mcp.ProtocolVersion20251125},
		AccessMode:      mcp.AccessReadWrite,
		Transports:      []string{mcp.TransportStdio, mcp.TransportHTTP},
		Modules:         []string{},
		AIAvailable:     d.aiAvailable(),
		ZoteroAvailable: false,
	}, nil
}

func confirmationView(c mcp.Confirmation) map[string]any {
	out := map[string]any{
		"confirmation_id": c.ID,
		"tool":            c.Tool,
		"title":           c.Title,
		"summary":         c.Summary,
		"state":           string(c.State),
		"client_id":       c.ClientID,
		"created_at":      ts(c.CreatedAt),
		"expires_at":      ts(c.ExpiresAt),
		"resolved_at":     tsp(c.ResolvedAt),
		"resolved_by":     c.ResolvedBy,
	}
	if c.ProjectID != "" {
		out["project_id"] = c.ProjectID
	}
	if len(c.Impact) > 0 {
		out["impact"] = json.RawMessage(c.Impact)
	}
	if len(c.Result) > 0 {
		out["result"] = json.RawMessage(c.Result)
	}
	if c.ErrorCode != "" {
		out["error_code"] = c.ErrorCode
	}
	return out
}

func jobView(j mcp.Job, detail bool) map[string]any {
	out := map[string]any{
		"job_id":      j.ID,
		"tool":        j.Tool,
		"state":       string(j.State),
		"progress":    j.Progress,
		"stage":       j.Stage,
		"read_only":   j.ReadOnly,
		"created_at":  ts(j.CreatedAt),
		"started_at":  tsp(j.StartedAt),
		"finished_at": tsp(j.FinishedAt),
	}
	if j.ProjectID != "" {
		out["project_id"] = j.ProjectID
	}
	if detail {
		if len(j.Result) > 0 {
			out["result"] = json.RawMessage(j.Result)
		}
		if len(j.Error) > 0 {
			out["error"] = json.RawMessage(j.Error)
		}
	}
	return out
}
