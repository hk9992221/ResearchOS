package mcpbind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"researchos/internal/mcp"
	"researchos/internal/search"
)

// newSearchProvider builds the SPEC-10 search domain (SPEC-11 §5.3.7): one
// retrieval tool, one index-status tool and a read-only reindex job. The
// provider is ready only after the composition root injects the search
// service; without it the whole module is hidden (§2.2).
func newSearchProvider(d Deps, ready func(context.Context) error) mcp.ToolProvider {
	mod := mcp.ModuleSearch
	providerReady := func(ctx context.Context) error {
		if err := ready(ctx); err != nil {
			return err
		}
		if d.Search == nil {
			return fmt.Errorf("mcpbind: search service is not assembled")
		}
		return nil
	}
	tools := []mcp.ToolBinding{
		tool(mod, mcp.CategoryRead, "search", "检索",
			"检索项目或全局文献索引（关键词与向量融合；rerank 不可用时降级为融合排序）。"+
				"scope=global 时归档项目默认参与；kinds 用实体类型过滤；只读，不记 Activity。",
			sObj(map[string]any{
				"query":           sStr("检索词"),
				"scope":           sEnum("检索作用域（project 时必须给 project_id）", "project", "global"),
				"project_id":      sStr("项目 ID"),
				"kinds":           sArr("实体类型过滤", sEnum("实体类型", "paper", "note", "db_record", "idea", "experiment")),
				"limit":           sInt("返回条数，默认 20，上限 100", 1, 100),
				"offset":          sInt("偏移量，默认 0", 0, 0),
				"rerank":          sBool("是否启用 rerank，默认 true；不可用时降级并附 warning，不报错"),
				"include_deleted": sBool("是否包含已删除文档，默认 false"),
			}, "query", "scope"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				svc, err := d.searchService()
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				query, err := requireString(a, "query")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				scope, err := searchScopeArg(a)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				kinds, _, err := a.Strings("kinds")
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				offset, _ := a.Int("offset")
				req := search.SearchRequest{
					Query:          query,
					Scope:          scope,
					Limit:          limitOf(a, 20, 100),
					Offset:         offset,
					IncludeDeleted: a.BoolDefault("include_deleted", false),
					Rerank:         search.RerankOff,
				}
				if a.BoolDefault("rerank", true) {
					req.Rerank = search.RerankAuto
				}
				for _, kind := range kinds {
					req.Filters.EntityTypes = append(req.Filters.EntityTypes, search.EntityType(kind))
				}
				resp, err := svc.Search(ctx, req)
				if err != nil {
					return mcp.ToolOutcome{}, mapSearchError(err)
				}
				hits := make([]any, 0, len(resp.Results))
				for _, result := range resp.Results {
					hits = append(hits, searchHitView(result))
				}
				out := map[string]any{
					"hits":     hits,
					"took_ms":  resp.Timings["total"],
					"total":    resp.Total,
					"has_more": resp.HasMore,
					"partial":  resp.Partial,
					"degraded": degradeCodes(resp.Degraded),
				}
				if hasDegrade(resp.Degraded, search.DegradeRerankUnavailable) {
					out["warning"] = map[string]any{"code": "RERANK_UNAVAILABLE"}
				}
				return okOut(out)
			}),

		tool(mod, mcp.CategoryRead, "search_index_status", "索引状态",
			"返回项目或全局文献索引的健康度、进度与最近错误（只读）。",
			sObj(map[string]any{
				"scope":      sEnum("索引作用域（project 时必须给 project_id）", "project", "global"),
				"project_id": sStr("项目 ID"),
			}, "scope"), annRead(),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				svc, err := d.searchService()
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				scope, err := searchScopeArg(a)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				status, err := svc.Status(ctx, scope)
				if err != nil {
					return mcp.ToolOutcome{}, mapSearchError(err)
				}
				return okOut(indexStatusView(status))
			}),

		tool(mod, mcp.CategoryJob, "search_reindex", "重建索引",
			"提交索引重建作业。只读作业：不改变科研数据、不需要确认、不记 Activity。"+
				"返回 MCP job_id；作业结果中的 job_id 是索引作业 ID。",
			sObj(map[string]any{
				"scope":          sEnum("索引作用域（project 时必须给 project_id）", "project", "global"),
				"project_id":     sStr("项目 ID"),
				"reparse_pdf":    sBool("是否重新解析 PDF，默认 false"),
				"rebuild_vector": sBool("是否重建向量，默认 false"),
			}, "scope"), annJob(true),
			func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error) {
				svc, err := d.searchService()
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				if d.Jobs == nil {
					return mcp.ToolOutcome{}, unavailablef(mcp.CodeInternal, "作业运行器不可用")
				}
				scope, err := searchScopeArg(a)
				if err != nil {
					return mcp.ToolOutcome{}, err
				}
				// Validate the request shape before enqueueing: an invalid
				// scope must fail the call, not a detached background job.
				if err := scope.Validate(); err != nil {
					return mcp.ToolOutcome{}, mapSearchError(err)
				}
				reparse := a.BoolDefault("reparse_pdf", false)
				rebuild := a.BoolDefault("rebuild_vector", false)
				params, _ := json.Marshal(map[string]any{
					"scope":          searchScopeView(scope),
					"reparse_pdf":    reparse,
					"rebuild_vector": rebuild,
				})
				job, err := d.Jobs.Submit(ctx, p, mcp.JobRequest{
					Tool:      "search_reindex",
					ProjectID: scope.ProjectID,
					ClientID:  p.ClientID,
					ReadOnly:  true,
					Params:    params,
					TraceID:   mcp.TraceIDFrom(ctx),
					Run: func(jctx context.Context, progress mcp.ProgressFunc) (any, error) {
						progress(10, "提交索引重建")
						id, err := svc.Reindex(jctx, search.ReindexRequest{
							Scope:         scope,
							ReparsePDF:    reparse,
							RebuildVector: rebuild,
						})
						if err != nil {
							return nil, mapSearchError(err)
						}
						progress(100, "索引重建已入队")
						return map[string]any{
							"job_id": string(id),
							"scope":  searchScopeView(scope),
						}, nil
					},
				})
				if err != nil {
					return mcp.ToolOutcome{}, asMCPerror(err, mcp.CodeInternal, "提交索引重建作业失败")
				}
				return jobOut(job)
			}, allowArchived()),
	}
	return &provider{module: mod, tools: tools, ready: providerReady}
}

// searchService guards the injected service; the provider readiness check
// already hides the tools when it is nil.
func (d *Deps) searchService() (*search.Service, error) {
	if d == nil || d.Search == nil {
		return nil, mcp.Errorf(mcp.CodeInternal, "检索服务尚未装配")
	}
	return d.Search, nil
}

// searchScopeArg shapes the flat scope/project_id arguments into a search
// scope. Validation stays in search.Scope.Validate (no second rule set).
func searchScopeArg(a *mcp.Arguments) (search.Scope, error) {
	kind, err := requireString(a, "scope")
	if err != nil {
		return search.Scope{}, err
	}
	scope := search.Scope{Kind: search.ScopeKind(kind)}
	if projectID, ok := a.String("project_id"); ok {
		scope.ProjectID = projectID
	}
	return scope, nil
}

func searchScopeView(scope search.Scope) map[string]any {
	out := map[string]any{"kind": string(scope.Kind)}
	if scope.ProjectID != "" {
		out["project_id"] = scope.ProjectID
	}
	return out
}

// searchHitView projects one SearchResult onto the §5.3.7 hit shape.
func searchHitView(result search.SearchResult) map[string]any {
	out := map[string]any{
		"entity_type": string(result.EntityType),
		"entity_id":   result.EntityID,
		"title":       result.Title,
		"snippet":     bestSnippet(result.Snippets),
		"score":       result.Score.Fusion,
		"source":      searchSourceView(result.Locator),
	}
	if result.ProjectID != "" {
		out["project_id"] = result.ProjectID
	}
	return out
}

// bestSnippet returns the text of the first non-empty excerpt. The service
// emits excerpts in rank order, so the first is the best one.
func bestSnippet(snippets []search.Snippet) string {
	for _, snippet := range snippets {
		if strings.TrimSpace(snippet.Text) != "" {
			return snippet.Text
		}
	}
	return ""
}

// searchSourceView projects a locator: page only when it is known (SPEC-00 §3).
func searchSourceView(loc search.Locator) map[string]any {
	out := map[string]any{}
	if loc.PageNo > 0 {
		out["page"] = loc.PageNo
	}
	if view := searchLocatorView(loc); len(view) > 0 {
		out["loc"] = view
	}
	return out
}

func searchLocatorView(loc search.Locator) map[string]any {
	out := map[string]any{}
	if loc.PageNo > 0 {
		out["page_no"] = loc.PageNo
	}
	if loc.BlockNo > 0 {
		out["block_no"] = loc.BlockNo
	}
	if loc.CharStart > 0 {
		out["char_start"] = loc.CharStart
	}
	if loc.CharEnd > 0 {
		out["char_end"] = loc.CharEnd
	}
	if loc.Heading != "" {
		out["heading"] = loc.Heading
	}
	if loc.FieldID != "" {
		out["field_id"] = loc.FieldID
	}
	return out
}

// indexStatusView projects a search.IndexStatus (SPEC-10 §5.6).
func indexStatusView(status search.IndexStatus) map[string]any {
	out := map[string]any{
		"scope":           searchScopeView(status.Scope),
		"docs":            status.Docs,
		"chunks":          status.Chunks,
		"dirty":           status.Dirty,
		"failed":          status.Failed,
		"indexing":        status.Indexing,
		"progress":        status.Progress,
		"schema_version":  status.SchemaVersion,
		"parser_version":  status.ParserVersion,
		"embedding_model": status.EmbeddingModel,
		"stale":           status.Stale,
	}
	if !status.LastIndexedAt.IsZero() {
		out["last_indexed_at"] = ts(status.LastIndexedAt)
	}
	if status.LastError != "" {
		out["last_error"] = status.LastError
	}
	return out
}

// degradeCodes renders the degradation reasons in a stable order.
func degradeCodes(reasons []search.DegradeReason) []string {
	out := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		out = append(out, string(reason))
	}
	sort.Strings(out)
	return out
}

func hasDegrade(reasons []search.DegradeReason, want search.DegradeReason) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}

// mapSearchError maps SPEC-10 error codes onto the MCP codes of §5.7.
func mapSearchError(err error) error {
	if err == nil {
		return nil
	}
	var me *mcp.Error
	if errors.As(err, &me) {
		return me
	}
	var se *search.Error
	if errors.As(err, &se) {
		switch se.Code {
		case search.CodeInvalidRequest:
			return mcp.Errorf(mcp.CodeInvalidArgument, "%s", se.Error()).Wrap(err)
		case search.CodeScopeNotFound, search.CodeTargetNotFound, search.CodeJobNotFound:
			return mcp.Errorf(mcp.CodeNotFound, "%s", se.Error()).Wrap(err)
		case search.CodeNoTextLayer, search.CodePDFUnreadable, search.CodeSourceMissing:
			return mcp.Errorf(mcp.CodeInvalidArgument, "%s", se.Error()).Wrap(err)
		case search.CodeIndexUnavailable, search.CodeIndexWriteFailed:
			return mcp.Errorf(mcp.CodeInternal, "%s", se.Error()).Wrap(err)
		}
	}
	if errors.Is(err, search.ErrProjectNotFound) || errors.Is(err, search.ErrEntityNotFound) {
		return mcp.Errorf(mcp.CodeNotFound, "检索目标不存在").Wrap(err)
	}
	return asMCPerror(err, mcp.CodeInternal, "检索服务执行失败")
}
