package boot

// This file owns the SPEC-02 AI stack of the open workspace. The stack binds
// to global.db (providers, models, role assignments) and is built once per
// workspace; the modules consume it through per-project generation and output
// services, because ai_outputs rows live in the project database.
//
// The pages and the MCP server share this value, so the desktop UI and the
// bridge reach the same AI capabilities (SPEC-11 §5.9, RQ-17). A nil stack is
// the documented degradation: every module keeps its manual path and reports
// AI as disabled.

import (
	"context"
	"errors"
	"sync"
	"time"

	"researchos/internal/ai/output"
	"researchos/internal/ai/role"
	"researchos/internal/ai/wire"
	"researchos/internal/foundation/clock"
	fworkspace "researchos/internal/foundation/workspace"
)

// errAIUnavailable is returned when the stack was not built, or the project
// services cannot be. Callers treat it as ai.disabled, never as a hard error.
var errAIUnavailable = errors.New("boot: AI 服务栈未构建")

// chatProbeTTL bounds how long a chat readiness answer is cached: the pages ask
// on every frame, while a provider assigned in Settings should light the AI
// entries up without reopening the workspace.
const chatProbeTTL = 30 * time.Second

// chatProbe answers "is a chat provider assigned to chat_main" from global.db
// only. role.Resolve reads the assignment, the provider row and the model row
// (never the network and never a secret), so the check is safe to run from the
// UI thread; the TTL keeps it off the hot path.
type chatProbe struct {
	roles role.Service

	mu    sync.Mutex
	at    time.Time
	ready bool
	known bool
}

func newChatProbe(roles role.Service) *chatProbe { return &chatProbe{roles: roles} }

// readyNow reports whether chat_main resolves to an enabled provider and an
// available model. A nil probe is not ready.
func (p *chatProbe) readyNow() bool {
	if p == nil || p.roles == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.known && time.Since(p.at) < chatProbeTTL {
		return p.ready
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := p.roles.Resolve(ctx, role.RoleChatMain)
	p.ready, p.at, p.known = err == nil, time.Now(), true
	return p.ready
}

// aiModule serves one open workspace. The stack is workspace-scoped; the
// generation and output services are project-scoped and cached by project id.
type aiModule struct {
	ws    *fworkspace.Workspace
	stack *wire.Stack
	// ready is the local chat_main assignment check the pages and MCP use for
	// their AI availability signals; the per-call role_unassigned error stays
	// the backstop when an assignment disappears mid-session.
	ready *chatProbe

	mu       sync.Mutex
	services map[string]aiProjectServices
}

// aiProjectServices is the SPEC-02 pair one project's database needs: the
// generation service writes the draft, the output service reads and confirms
// it.
type aiProjectServices struct {
	gen  output.GenerationService
	outs output.AIOutputService
}

func newAIModule(ws *fworkspace.Workspace, stack *wire.Stack) *aiModule {
	return &aiModule{ws: ws, stack: stack, ready: newChatProbe(stack.Roles), services: map[string]aiProjectServices{}}
}

// available reports whether the workspace has a usable stack. It is the flag
// the modules and the MCP status surface as AI availability.
func (m *aiModule) available() bool {
	return m != nil && m.stack != nil && m.stack.Gateway != nil
}

// chatReady reports whether the stack exists and a chat provider is actually
// assigned. The pages and the MCP surface use it instead of available() so a
// workspace without providers shows the disabled entry and the 去设置 hint
// (§SPEC-06 6.6, §SPEC-09) instead of failing per call.
func (m *aiModule) chatReady() bool {
	return m.available() && m.ready.readyNow()
}

// stackOrNil returns the stack for the page adapters, which construct their
// own per-project services.
func (m *aiModule) stackOrNil() *wire.Stack {
	if !m.available() {
		return nil
	}
	return m.stack
}

// servicesFor returns the generation and output services bound to one
// project's database, building them once. It is the MCP binding's AI factory.
func (m *aiModule) servicesFor(ctx context.Context, projectID string) (output.GenerationService, output.AIOutputService, error) {
	if !m.available() {
		return nil, nil, errAIUnavailable
	}
	m.mu.Lock()
	if cached, ok := m.services[projectID]; ok {
		m.mu.Unlock()
		return cached.gen, cached.outs, nil
	}
	m.mu.Unlock()

	handle, err := m.ws.Project(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	var c clock.Clock = clock.System{}
	if m.ws.Clock() != nil {
		c = m.ws.Clock()
	}
	readOnly := output.NewProjectReadOnly(m.ws.Global().DB(), projectID)
	outs, err := output.NewOutputService(handle.DB(), output.OutputOptions{
		Clock:     c,
		ProjectID: projectID,
		ReadOnly:  readOnly,
	})
	if err != nil {
		return nil, nil, err
	}
	gen, err := output.NewGenerationService(output.GenerationOptions{
		Outputs:  outs,
		Runner:   m.stack.Gateway,
		Events:   handle.Events(),
		ReadOnly: readOnly,
		Logger:   func(msg string, kv ...any) { appLogger{}.Warn(msg, kv...) },
	})
	if err != nil {
		return nil, nil, err
	}
	pair := aiProjectServices{gen: gen, outs: outs}
	m.mu.Lock()
	if m.services == nil {
		m.services = map[string]aiProjectServices{}
	}
	m.services[projectID] = pair
	m.mu.Unlock()
	return pair.gen, pair.outs, nil
}
