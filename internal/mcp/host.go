package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/ids"
)

// HostOptions configures a Host.
type HostOptions struct {
	Clock       clock.Clock
	Settings    SettingStore
	Broker      ConfirmationBroker
	Jobs        JobRunner
	Idempotency IdempotencyStore
	Gate        ProjectGate
	Runtime     Runtime
	Limiter     *Limiter
	Notifier    *Notifier
	Logger      Logger
	// ServerName and ServerVersion are reported in _meta and server/discover.
	ServerName    string
	ServerVersion string
	// Providers are the module bindings.
	Providers []ToolProvider
}

// Host is the ToolHost implementation (SPEC-11 §5.2).
type Host struct {
	opts HostOptions

	bindings []compiled
	byName   map[string]*compiled
	modules  map[ModuleID]ToolProvider

	ready map[ModuleID]bool
	mode  string

	mu sync.Mutex
}

type compiled struct {
	binding  ToolBinding
	provider ToolProvider
	schema   *Schema
}

// NewHost compiles the provider catalog. A duplicate tool name, a malformed
// schema or a missing handler is a construction error.
func NewHost(opts HostOptions) (*Host, error) {
	if opts.Clock == nil {
		opts.Clock = clock.System{}
	}
	if opts.ServerName == "" {
		opts.ServerName = "ResearchOS"
	}
	h := &Host{
		opts:    opts,
		byName:  make(map[string]*compiled),
		modules: make(map[ModuleID]ToolProvider),
		ready:   make(map[ModuleID]bool),
	}
	for _, provider := range opts.Providers {
		if provider == nil {
			continue
		}
		h.modules[provider.Module()] = provider
		for _, binding := range provider.Tools() {
			spec := binding.Spec
			if spec.Name == "" {
				return nil, fmt.Errorf("mcp: provider %s registered a tool without a name", provider.Module())
			}
			if _, dup := h.byName[spec.Name]; dup {
				return nil, fmt.Errorf("mcp: duplicate tool name %q", spec.Name)
			}
			if binding.Handle == nil {
				return nil, fmt.Errorf("mcp: tool %q has no handler", spec.Name)
			}
			schema, err := ParseSchema(spec.InputSchema)
			if err != nil {
				return nil, fmt.Errorf("mcp: tool %q: %w", spec.Name, err)
			}
			cb := &compiled{binding: binding, provider: provider, schema: schema}
			h.bindings = append(h.bindings, *cb)
			h.byName[spec.Name] = cb
		}
	}
	sort.Slice(h.bindings, func(i, j int) bool {
		if h.bindings[i].binding.Spec.Module != h.bindings[j].binding.Spec.Module {
			return h.bindings[i].binding.Spec.Module < h.bindings[j].binding.Spec.Module
		}
		return h.bindings[i].binding.Spec.Name < h.bindings[j].binding.Spec.Name
	})
	return h, nil
}

// Binding exposes one registered binding for tests and the composition root.
func (h *Host) Binding(name string) (ToolBinding, bool) {
	cb, ok := h.byName[name]
	if !ok {
		return ToolBinding{}, false
	}
	return cb.binding, true
}

// Names returns every registered tool name in catalog order.
func (h *Host) Names() []string {
	out := make([]string, 0, len(h.bindings))
	for _, cb := range h.bindings {
		out = append(out, cb.binding.Spec.Name)
	}
	return out
}

// Settings resolves the current mcp.* values.
func (h *Host) Settings(ctx context.Context) (SettingsValues, error) {
	return LoadSettings(ctx, h.opts.Settings)
}

// ReadyModules lists the modules whose tools are currently registered.
func (h *Host) ReadyModules(ctx context.Context) []string {
	out := make([]string, 0, len(h.modules))
	for module := range h.modules {
		if h.moduleReady(ctx, module) {
			out = append(out, string(module))
		}
	}
	sort.Strings(out)
	return out
}

// Refresh recomputes module readiness and broadcasts tools/list_changed when
// the visible set changed. It returns whether it changed.
func (h *Host) Refresh(ctx context.Context) bool {
	changed := false
	if settings, err := h.Settings(ctx); err == nil {
		h.mu.Lock()
		if h.mode == "" {
			h.mode = settings.AccessMode
		} else if h.mode != settings.AccessMode {
			h.mode = settings.AccessMode
			changed = true
		}
		h.mu.Unlock()
	}
	h.mu.Lock()
	for module, provider := range h.modules {
		ready := provider.Ready(ctx) == nil
		if h.ready[module] != ready {
			h.ready[module] = ready
			changed = true
		}
	}
	h.mu.Unlock()
	if changed && h.opts.Notifier != nil {
		h.opts.Notifier.Notify()
	}
	return changed
}

// WatchReady refreshes readiness every interval until ctx ends. It is the
// mechanism behind the list_changed notification for late-starting modules.
func (h *Host) WatchReady(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	h.Refresh(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.Refresh(ctx)
		}
	}
}

func (h *Host) moduleReady(ctx context.Context, module ModuleID) bool {
	h.mu.Lock()
	ready, seen := h.ready[module]
	h.mu.Unlock()
	if seen {
		return ready
	}
	provider := h.modules[module]
	ready = provider != nil && provider.Ready(ctx) == nil
	h.mu.Lock()
	h.ready[module] = ready
	h.mu.Unlock()
	return ready
}

// Tools implements ToolHost.
func (h *Host) Tools(ctx context.Context, p Principal) ([]ToolSpec, error) {
	h.Refresh(ctx)
	settings, err := h.Settings(ctx)
	if err != nil {
		return nil, Errorf(CodeInternal, "读取 MCP 设置失败：%v", err)
	}
	mode := p.AccessMode
	if mode == "" {
		mode = settings.AccessMode
	}
	out := make([]ToolSpec, 0, len(h.bindings))
	for _, cb := range h.bindings {
		if !h.moduleReady(ctx, cb.binding.Spec.Module) {
			continue
		}
		if !visibleUnder(cb.binding.Spec, mode) {
			continue
		}
		spec := cb.binding.Spec
		spec.RequiresProjectID = cb.schema.HasProperty("project_id")
		out = append(out, spec)
	}
	return out, nil
}

func visibleUnder(spec ToolSpec, mode string) bool {
	if mode != AccessReadOnly {
		return true
	}
	switch spec.Category {
	case CategoryRead:
		return true
	case CategoryJob:
		return spec.Annotations.ReadOnlyHint
	default:
		return false
	}
}

// Call implements ToolHost.
func (h *Host) Call(ctx context.Context, p Principal, req CallRequest) (CallResult, error) {
	h.Refresh(ctx)
	traceID := req.TraceID
	if traceID == "" {
		traceID = h.newTraceID()
	}
	cb, ok := h.byName[req.Name]
	if !ok {
		return CallResult{}, Errorf(CodeInvalidArgument, "未知工具 %q", req.Name).
			WithDetail("reason", "unknown_tool").WithDetail("tool", req.Name)
	}
	spec := cb.binding.Spec
	spec.RequiresProjectID = cb.schema.HasProperty("project_id")

	if !h.moduleReady(ctx, spec.Module) {
		err := Errorf(CodeNotFound, "模块 %s 尚未就绪，工具 %s 暂不可用", spec.Module, spec.Name).
			WithDetail("module", string(spec.Module))
		return h.errorResult(spec, err, traceID), nil
	}

	settings, err := h.Settings(ctx)
	if err != nil {
		return h.errorResult(spec, Errorf(CodeInternal, "读取 MCP 设置失败：%v", err), traceID), nil
	}
	mode := p.AccessMode
	if mode == "" {
		mode = settings.AccessMode
	}
	if !visibleUnder(spec, mode) {
		return h.errorResult(spec, Errorf(CodeAccessMode, "当前为只读模式，工具 %s 不可用", spec.Name).
			WithDetail("access_mode", AccessReadOnly), traceID), nil
	}

	key := p.ConnectionID
	if key == "" {
		key = p.ClientID
	}
	if retry, allowed := h.opts.Limiter.Allow(key); !allowed {
		ms := retry.Milliseconds()
		if ms < 1 {
			ms = 1
		}
		return h.errorResult(spec, Errorf(CodeRateLimited, "调用频率超过限制，请稍后重试").
			WithDetail("retry_after_ms", ms), traceID), nil
	}

	args, argumentKey, err := h.prepareArguments(req)
	if err != nil {
		return h.errorResult(spec, AsError(err), traceID), nil
	}
	projectID, _ := args.String("project_id")

	if spec.RequiresProjectID && projectID != "" && !spec.AllowArchived && spec.Category != CategoryRead {
		if err := h.checkArchived(ctx, projectID); err != nil {
			return h.errorResult(spec, AsError(err), traceID), nil
		}
	}

	sanitized, err := Reencode(args.Obj)
	if err != nil {
		return h.errorResult(spec, Errorf(CodeInternal, "参数编码失败"), traceID), nil
	}
	if PayloadTooLarge(args.Obj, len(sanitized)) {
		return h.errorResult(spec, Errorf(CodeLimitExceeded, "请求载荷超过上限（1 MiB，正文 5 MiB）").
			WithDetail("limit_bytes", MaxRequestBytes), traceID), nil
	}
	if err := cb.schema.Validate(sanitized); err != nil {
		var ve *ValidationError
		e := Errorf(CodeInvalidArgument, "参数校验失败")
		if asValidation(err, &ve) {
			e = Errorf(CodeInvalidArgument, "参数校验失败：%s", ve.Message).WithDetail("path", ve.Path)
		}
		return h.errorResult(spec, e, traceID), nil
	}

	idemKey := strings.TrimSpace(req.IdempotencyKey)
	if argumentKey != "" {
		if idemKey != "" && idemKey != argumentKey {
			return h.errorResult(spec, Errorf(CodeInvalidArgument, "idempotency_key 在两处取值不一致"), traceID), nil
		}
		idemKey = argumentKey
	}
	needsIdempotency := spec.Category == CategoryWrite || spec.Category == CategoryConfirm ||
		(spec.Category == CategoryJob && !spec.Annotations.ReadOnlyHint)
	if needsIdempotency {
		if idemKey == "" {
			return h.errorResult(spec, Errorf(CodeInvalidArgument, "写操作必须提供 idempotency_key（1–128 字符）").
				WithDetail("required", "idempotency_key"), traceID), nil
		}
		if len(idemKey) > 128 {
			return h.errorResult(spec, Errorf(CodeInvalidArgument, "idempotency_key 最长 128 字符"), traceID), nil
		}
	}

	argsHash := hashArgs(sanitized)
	claimed := false
	if needsIdempotency {
		claim, err := h.opts.Idempotency.Claim(ctx, p.ClientID, idemKey, spec.Name, argsHash, h.opts.Clock.Now())
		switch {
		case err == nil:
			if claim.Replayed {
				var stored CallResult
				if err := json.Unmarshal(claim.Result, &stored); err == nil {
					if stored.Meta == nil {
						stored.Meta = map[string]any{}
					}
					stored.Meta["traceId"] = traceID
					stored.Meta["replayed"] = true
					return stored, nil
				}
			}
			claimed = true
		case isIdemConflict(err):
			return h.errorResult(spec, Errorf(CodeConflict, "idempotency_key 已用于不同参数").
				WithDetail("idempotency_key", idemKey), traceID), nil
		case isIdemInFlight(err):
			return h.errorResult(spec, Errorf(CodeConflict, "相同 idempotency_key 的调用正在进行").
				WithDetail("idempotency_key", idemKey), traceID), nil
		default:
			return h.errorResult(spec, Errorf(CodeInternal, "幂等记录不可用"), traceID), nil
		}
	}

	outcome, callErr := h.invoke(ctx, p, cb, sanitized, idemKey, traceID)
	if callErr != nil {
		if claimed {
			_ = h.opts.Idempotency.Release(ctx, p.ClientID, idemKey)
		}
		return h.errorResult(spec, AsError(callErr), traceID), nil
	}

	result := h.wrapOutcome(ctx, p, spec, outcome, sanitized, argsHash, traceID)
	if claimed {
		if body, err := json.Marshal(result); err == nil {
			if err := h.opts.Idempotency.Complete(ctx, p.ClientID, idemKey, body); err != nil && h.opts.Logger != nil {
				h.opts.Logger.Warn("mcp: idempotency record not completed", "tool", spec.Name, "err", err)
			}
		}
	}
	return result, nil
}

func (h *Host) invoke(ctx context.Context, p Principal, cb *compiled, args json.RawMessage, idemKey, traceID string) (tool ToolOutcome, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = Errorf(CodeInternal, "工具执行时发生内部错误").WithDetail("panic", fmt.Sprint(recovered))
		}
	}()
	callCtx := ctx
	if idemKey != "" {
		callCtx = context.WithValue(callCtx, idempotencyContextKey{}, idemKey)
	}
	callCtx = context.WithValue(callCtx, traceContextKey{}, traceID)
	return cb.binding.Handle(callCtx, p, args)
}

type idempotencyContextKey struct{}
type traceContextKey struct{}

// TraceIDFrom returns the trace id of a tool call.
func TraceIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(traceContextKey{}).(string); ok {
		return v
	}
	return ""
}

// IdempotencyKeyFrom returns the idempotency key of a tool call.
func IdempotencyKeyFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(idempotencyContextKey{}).(string); ok {
		return v
	}
	return ""
}

func (h *Host) prepareArguments(req CallRequest) (*Arguments, string, error) {
	args, err := DecodeArguments(req.Arguments)
	if err != nil {
		return nil, "", err
	}
	key, _ := args.String("idempotency_key")
	delete(args.Obj, "idempotency_key")
	return args, strings.TrimSpace(key), nil
}

func (h *Host) checkArchived(ctx context.Context, projectID string) error {
	if h.opts.Gate == nil {
		return nil
	}
	archived, err := h.opts.Gate.IsArchived(ctx, projectID)
	if err != nil {
		return err
	}
	if archived {
		return Errorf(CodeArchivedReadOnly, "项目 %s 已归档，不能写入；请先在应用内解除归档", projectID).
			WithDetail("project_id", projectID)
	}
	return nil
}

// GateFunc adapts a function to ProjectGate.
type GateFunc func(ctx context.Context, projectID string) (bool, error)

// IsArchived implements ProjectGate.
func (f GateFunc) IsArchived(ctx context.Context, projectID string) (bool, error) {
	return f(ctx, projectID)
}

func (h *Host) wrapOutcome(ctx context.Context, p Principal, spec ToolSpec, outcome ToolOutcome, args json.RawMessage, argsHash, traceID string) CallResult {
	var structured any = outcome.Structured
	text := outcome.Text

	switch outcome.Status {
	case StatusPending:
		req := ConfirmationRequest{}
		if outcome.Confirmation != nil {
			req = *outcome.Confirmation
		}
		req.Tool = spec.Name
		req.TraceID = traceID
		conf, err := h.opts.Broker.Enqueue(ctx, p, req, args, argsHash)
		if err != nil {
			return h.errorResult(spec, AsError(err), traceID)
		}
		structured = map[string]any{
			"status":          StatusPending,
			"confirmation_id": conf.ID,
			"expires_at":      clock.Format(conf.ExpiresAt),
			"poll_after_ms":   2000,
			"title":           conf.Title,
			"summary":         conf.Summary,
			"impact":          jsonOrNil(conf.Impact),
			"requires_in_app": true,
		}
		if text == "" {
			text = fmt.Sprintf("已提交确认请求 **%s**。请在 ResearchOS 的确认中心批准后执行（有效期至 %s），可使用 confirmation_get 轮询状态。",
				conf.ID, clock.Format(conf.ExpiresAt))
		}
	case StatusJob:
		structured = map[string]any{
			"status":        StatusJob,
			"job_id":        outcome.JobID,
			"poll_after_ms": 1000,
		}
		if text == "" {
			text = fmt.Sprintf("已提交后台作业 **%s**。可使用 job_get 查询进度与结果。", outcome.JobID)
		}
	default:
		if text == "" {
			text = defaultSummary(spec, structured)
		}
	}
	return CallResult{
		ResultType: "complete",
		Content:    []ContentBlock{{Type: "text", Text: text}},
		Structured: structured,
		IsError:    false,
		Meta:       h.meta(traceID),
	}
}

func (h *Host) errorResult(spec ToolSpec, err *Error, traceID string) CallResult {
	body := errorBody{Code: err.Code, Message: err.Message, Details: err.Details}
	structured := map[string]any{"error": body}
	text := fmt.Sprintf("**%s 执行失败**\n\n- 错误码：`%s`\n- 说明：%s", spec.Name, err.Code, err.Message)
	if len(err.Details) > 0 {
		if body, err := json.MarshalIndent(err.Details, "", "  "); err == nil {
			text += "\n\n```json\n" + string(body) + "\n```"
		}
	}
	return CallResult{
		ResultType: "complete",
		Content:    []ContentBlock{{Type: "text", Text: text}},
		Structured: structured,
		IsError:    true,
		Meta:       h.meta(traceID),
	}
}

func (h *Host) meta(traceID string) map[string]any {
	return map[string]any{
		"traceId": traceID,
		"serverInfo": map[string]any{
			"name":    h.opts.ServerName,
			"version": h.opts.ServerVersion,
		},
	}
}

func (h *Host) newTraceID() string {
	id, err := ids.New(h.opts.Clock)
	if err != nil {
		return fmt.Sprintf("trace-%d", h.opts.Clock.Now().UnixNano())
	}
	return id
}

func defaultSummary(spec ToolSpec, structured any) string {
	title := spec.Title
	if title == "" {
		title = spec.Name
	}
	body := "{}"
	if structured != nil {
		if raw, err := json.MarshalIndent(structured, "", "  "); err == nil {
			body = string(raw)
		}
	}
	const max = 8000
	if len(body) > max {
		body = body[:max] + "\n…（已截断）"
	}
	return fmt.Sprintf("已执行 **%s**。\n\n```json\n%s\n```", title, body)
}

func jsonOrNil(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}

func hashArgs(raw json.RawMessage) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func asValidation(err error, target **ValidationError) bool {
	ve, ok := err.(*ValidationError)
	if ok {
		*target = ve
	}
	return ok
}

func isIdemConflict(err error) bool { return errors.Is(err, IdempotencyConflict) }
func isIdemInFlight(err error) bool { return errors.Is(err, IdempotencyInFlight) }

// HasProperty reports whether the schema declares the property.
func (s *Schema) HasProperty(name string) bool {
	if s == nil || s.root == nil {
		return false
	}
	_, ok := s.root.properties[name]
	return ok
}
