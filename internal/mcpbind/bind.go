// Package mcpbind binds the ResearchOS module service layer to the MCP tool
// catalog (SPEC-11 §5.3, §5.9).
//
// Bindings only shape arguments, project results and map errors. They never
// re-implement business validation and never read a database directly: every
// operation goes through a module service, exactly like the UI.
package mcpbind

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"researchos/internal/ai/output"
	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/settings"
	"researchos/internal/foundation/workspace"
	"researchos/internal/literature"
	"researchos/internal/literature/library"
	"researchos/internal/mcp"
	"researchos/internal/mcpfacade"
)

// Deps is the composition-root surface the bindings need. Every field is an
// injected collaborator; nils disable the affected tools through Ready.
type Deps struct {
	Workspace *workspace.Workspace
	Clock     clock.Clock
	Logger    mcp.Logger
	Version   string
	Runtime   mcp.Runtime

	Confirm  confirm.Service
	Settings *settings.Store

	// AI is the SPEC-02 generation stack used by AI-backed tools.
	AI      output.GenerationService
	Outputs output.AIOutputService

	// Confirmations and Jobs are the runtime ports from internal/mcp.
	Confirmations mcp.ConfirmationBroker
	Jobs          mcp.JobRunner

	// Facade is the SPEC-03 project allow-list.
	Facade *mcpfacade.Service

	// Infra caches expensive collaborators (the Zotero-backed library
	// service) so a tool call does not reopen Zotero's database every time.
	Infra *Infra
}

// Infra holds lazily built, process-wide collaborators of the binding layer.
type Infra struct {
	mu     sync.Mutex
	lib    *library.Service
	source literature.ZoteroSource
}

func (d *Deps) clock() clock.Clock {
	if d != nil && d.Clock != nil {
		return d.Clock
	}
	return clock.System{}
}

// Ready reports whether the workspace is open.
func (d *Deps) Ready() error {
	if d == nil || d.Workspace == nil || d.Workspace.Global() == nil {
		return fmt.Errorf("mcpbind: workspace is not open")
	}
	return nil
}

// handle resolves one project handle after validating the id. A missing or
// unreadable project is NOT_FOUND, never an internal error, so a client cannot
// probe other projects.
func (d *Deps) handle(ctx context.Context, projectID string) (*workspace.ProjectHandle, error) {
	if !ids.Valid(projectID) {
		return nil, mcp.Errorf(mcp.CodeNotFound, "项目 %s 不存在", projectID)
	}
	h, err := d.Workspace.Project(ctx, projectID)
	if err != nil {
		return nil, mcp.Errorf(mcp.CodeNotFound, "项目 %s 不存在", projectID)
	}
	return h, nil
}

// archived reports the project status for the host's write gate.
func (d *Deps) archived(ctx context.Context, projectID string) (bool, error) {
	if !ids.Valid(projectID) {
		return false, mcp.Errorf(mcp.CodeNotFound, "项目 %s 不存在", projectID)
	}
	if d.Facade == nil {
		return false, nil
	}
	p, err := d.Facade.GetProject(ctx, projectID)
	if err != nil {
		return false, mcp.Errorf(mcp.CodeNotFound, "项目 %s 不存在", projectID)
	}
	return string(p.Status) == "archived", nil
}

// Gate returns the archived-project gate for the host.
func (d *Deps) Gate() mcp.ProjectGate {
	if d == nil {
		return nil
	}
	return mcp.GateFunc(d.archived)
}

// projectExists is a cheap existence check used by tools that only need the
// project to be real.
func (d *Deps) projectExists(ctx context.Context, projectID string) error {
	_, err := d.handle(ctx, projectID)
	return err
}

// ---------------------------------------------------------------------------
// provider construction
// ---------------------------------------------------------------------------

type provider struct {
	module mcp.ModuleID
	tools  []mcp.ToolBinding
	ready  func(ctx context.Context) error
}

func (p *provider) Module() mcp.ModuleID { return p.module }

func (p *provider) Ready(ctx context.Context) error {
	if p.ready == nil {
		return nil
	}
	return p.ready(ctx)
}

func (p *provider) Tools() []mcp.ToolBinding { return p.tools }

// Providers returns every SPEC-11 tool provider except the SPEC-10 search tools
// (search, search_index_status, search_reindex), which are added after SPEC-10
// lands on main.
func Providers(d Deps) []mcp.ToolProvider {
	baseReady := func(context.Context) error { return d.Ready() }
	return []mcp.ToolProvider{
		newCoreProvider(d, baseReady),
		newProjectProvider(d, baseReady),
		newLiteratureProvider(d, baseReady),
		newNotesProvider(d, baseReady),
		newIdeasProvider(d, baseReady),
		newExperimentsProvider(d, baseReady),
		newKanbanProvider(d, baseReady),
		newWeeklyProvider(d, baseReady),
		newMiscProvider(d, baseReady),
	}
}

type handler func(ctx context.Context, p mcp.Principal, a *mcp.Arguments) (mcp.ToolOutcome, error)

type toolOption func(*mcp.ToolSpec)

func allowArchived() toolOption {
	return func(s *mcp.ToolSpec) { s.AllowArchived = true }
}

func tool(module mcp.ModuleID, cat mcp.Category, name, title, description string, input json.RawMessage, ann mcp.ToolAnnotations, h handler, opts ...toolOption) mcp.ToolBinding {
	if cat == mcp.CategoryWrite || cat == mcp.CategoryConfirm || (cat == mcp.CategoryJob && !ann.ReadOnlyHint) {
		description += "\n\n写操作需要 idempotency_key（1–128 字符），放在 arguments.idempotency_key 或 params._meta.idempotencyKey。"
	}
	spec := mcp.ToolSpec{
		Name:         name,
		Title:        title,
		Description:  description,
		InputSchema:  input,
		OutputSchema: genericOutput(),
		Annotations:  ann,
		Category:     cat,
		Module:       module,
	}
	for _, opt := range opts {
		opt(&spec)
	}
	return mcp.ToolBinding{
		Spec: spec,
		Handle: func(ctx context.Context, p mcp.Principal, in json.RawMessage) (mcp.ToolOutcome, error) {
			args, err := mcp.DecodeArguments(in)
			if err != nil {
				return mcp.ToolOutcome{}, err
			}
			return h(ctx, p, args)
		},
	}
}

// ---------------------------------------------------------------------------
// JSON Schema builders (deterministic: encoding/json sorts map keys)
// ---------------------------------------------------------------------------

func sObj(props map[string]any, required ...string) json.RawMessage {
	if props == nil {
		props = map[string]any{}
	}
	if required == nil {
		required = []string{}
	}
	sort.Strings(required)
	body, err := json.Marshal(map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	})
	if err != nil {
		panic("mcpbind: encode schema: " + err.Error())
	}
	return body
}

func sProps(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	sort.Strings(required)
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	}
}

func sStr(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func sStrLen(desc string, minLen, maxLen int) map[string]any {
	return map[string]any{"type": "string", "description": desc, "minLength": minLen, "maxLength": maxLen}
}

func sEnum(desc string, values ...string) map[string]any {
	list := make([]any, 0, len(values))
	for _, v := range values {
		list = append(list, v)
	}
	return map[string]any{"type": "string", "description": desc, "enum": list}
}

func sInt(desc string, minV, maxV int) map[string]any {
	m := map[string]any{"type": "integer", "description": desc}
	if minV != 0 {
		m["minimum"] = minV
	}
	if maxV != 0 {
		m["maximum"] = maxV
	}
	return m
}

func sBool(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func sArr(desc string, item map[string]any) map[string]any {
	return map[string]any{"type": "array", "description": desc, "items": item}
}

func sObjMap(desc string, props map[string]any, required ...string) map[string]any {
	out := sProps(props, required...)
	out["description"] = desc
	return out
}

func sAny(desc string) map[string]any {
	return map[string]any{"description": desc}
}

func genericOutput() json.RawMessage {
	body, _ := json.Marshal(map[string]any{"type": "object", "additionalProperties": true})
	return body
}

// Annotation presets.
func annRead() mcp.ToolAnnotations {
	return mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
}

func annWrite() mcp.ToolAnnotations {
	return mcp.ToolAnnotations{IdempotentHint: true}
}

func annDelete() mcp.ToolAnnotations {
	return mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: true}
}

func annJob(readOnly bool) mcp.ToolAnnotations {
	return mcp.ToolAnnotations{ReadOnlyHint: readOnly, IdempotentHint: true}
}

func annConfirm() mcp.ToolAnnotations {
	return mcp.ToolAnnotations{DestructiveHint: true, IdempotentHint: true}
}

// ---------------------------------------------------------------------------
// outcomes
// ---------------------------------------------------------------------------

func okOut(structured any) (mcp.ToolOutcome, error) {
	return mcp.ToolOutcome{Status: mcp.StatusOK, Structured: structured}, nil
}

func okOutText(structured any, text string) (mcp.ToolOutcome, error) {
	return mcp.ToolOutcome{Status: mcp.StatusOK, Structured: structured, Text: text}, nil
}

func pendingOut(title, summary string, impact any, projectID string) (mcp.ToolOutcome, error) {
	return mcp.ToolOutcome{
		Status: mcp.StatusPending,
		Confirmation: &mcp.ConfirmationRequest{
			Title:     title,
			Summary:   summary,
			Impact:    impact,
			ProjectID: projectID,
		},
	}, nil
}

func jobOut(j mcp.Job) (mcp.ToolOutcome, error) {
	return mcp.ToolOutcome{Status: mcp.StatusJob, JobID: j.ID,
		Structured: map[string]any{"status": mcp.StatusJob, "job_id": j.ID, "poll_after_ms": 1000}}, nil
}

// ---------------------------------------------------------------------------
// error helpers
// ---------------------------------------------------------------------------

func invalidf(format string, args ...any) *mcp.Error {
	return mcp.Errorf(mcp.CodeInvalidArgument, format, args...)
}

func notFoundf(format string, args ...any) *mcp.Error {
	return mcp.Errorf(mcp.CodeNotFound, format, args...)
}

func conflictf(format string, args ...any) *mcp.Error {
	return mcp.Errorf(mcp.CodeConflict, format, args...)
}

func unavailablef(code mcp.Code, format string, args ...any) *mcp.Error {
	return mcp.Errorf(code, format, args...)
}

// asMCPerror passes through an *mcp.Error and falls back to ERR for anything
// else. Callers usually use the module-specific mappers below.
func asMCPerror(err error, fallback mcp.Code, fallbackMsg string) error {
	if err == nil {
		return nil
	}
	var me *mcp.Error
	if asError(err, &me) {
		return me
	}
	if errorsIsDeadline(err) {
		return mcp.Errorf(mcp.CodeInternal, "操作超时")
	}
	return (&mcp.Error{Code: fallback, Message: fallbackMsg}).Wrap(err)
}

func asError(err error, target **mcp.Error) bool {
	me, ok := err.(*mcp.Error)
	if ok {
		*target = me
	}
	return ok
}

func errorsIsDeadline(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "context deadline exceeded") ||
		strings.Contains(err.Error(), "context canceled"))
}

// ---------------------------------------------------------------------------
// value helpers
// ---------------------------------------------------------------------------

func ts(t time.Time) string { return clock.Format(t) }

func tsp(t *time.Time) any {
	if t == nil {
		return nil
	}
	return clock.Format(*t)
}

func ptr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func pageOf(items []any, next string) map[string]any {
	if items == nil {
		items = []any{}
	}
	out := map[string]any{"items": items}
	if next != "" {
		out["next_cursor"] = next
	}
	return out
}

func cursorOffset(cursor string) (int, error) {
	if strings.TrimSpace(cursor) == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, invalidf("分页游标无效")
	}
	text := string(raw)
	if !strings.HasPrefix(text, "o:") {
		return 0, invalidf("分页游标无效")
	}
	n, err := strconv.Atoi(strings.TrimPrefix(text, "o:"))
	if err != nil || n < 0 {
		return 0, invalidf("分页游标无效")
	}
	return n, nil
}

func nextOffsetCursor(offset, limit, total int) string {
	if limit <= 0 || offset+limit >= total {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte("o:" + strconv.Itoa(offset+limit)))
}

func limitOf(a *mcp.Arguments, def, max int) int {
	n, ok := a.Int("limit")
	if !ok || n <= 0 {
		return def
	}
	if max > 0 && n > max {
		return max
	}
	return n
}

// eventActor returns the MCP actor for direct writes.
func eventActor() events.Actor { return schema.ActorMCP }

// requireString reads a required non-empty string.
func requireString(a *mcp.Arguments, name string) (string, error) {
	return a.RequiredString(name)
}

// requireID reads a required UUID-shaped identifier. The service validates the
// exact UUID version; this only rejects an obviously malformed value early.
func requireID(a *mcp.Arguments, name string) (string, error) {
	s, err := a.RequiredString(name)
	if err != nil {
		return "", err
	}
	return s, nil
}

// optionalStringPtr returns nil when the argument is absent.
func optionalStringPtr(a *mcp.Arguments, name string) *string {
	s, ok := a.String(name)
	if !ok {
		return nil
	}
	return &s
}

// parseArgTime parses an RFC3339 timestamp argument.
func parseArgTime(a *mcp.Arguments, name string) (*time.Time, error) {
	raw, ok := a.String(name)
	if !ok || raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, invalidf("参数 %s 必须是 RFC3339 时间", name)
	}
	return &t, nil
}
