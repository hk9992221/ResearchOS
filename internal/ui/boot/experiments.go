package boot

// The Experiments pages reach SPEC-07 through this adapter, the same way the
// other modules reach theirs: the composition root owns the workspace, the
// page only calls the narrow interface. Construction mirrors the MCP binding
// (internal/mcpbind/experiments.go) so the two entry points behave the same.
//
// The service is per project, so the adapter caches one per project id. The
// SPEC-02 AI stack is assembled per project as well (generation writes into
// that project's ai_outputs); when the stack is absent the module is built
// without AI and every AI call degrades to ErrAIUnavailable while scanning,
// metrics, Git and history keep working (SPEC-07 AC-15).

import (
	"context"
	"log"
	"sync"

	"researchos/internal/ai/output"
	"researchos/internal/ai/wire"
	"researchos/internal/experiments"
	"researchos/internal/foundation/clock"
	fworkspace "researchos/internal/foundation/workspace"
	"researchos/internal/ports"
	"researchos/internal/ui/pages"
)

// experimentsModule serves one open workspace. The service is bound to a
// project, so a call resolves the project's service from the cache.
type experimentsModule struct {
	ws    *fworkspace.Workspace
	stack *wire.Stack
	// ready reports whether a chat provider is assigned; nil disables the AI
	// actions, exactly like a nil stack.
	ready func() bool

	mu       sync.Mutex
	services map[string]*experiments.Service
}

var _ pages.ExperimentsAPI = (*experimentsModule)(nil)

func newExperimentsModule(ws *fworkspace.Workspace, stack *wire.Stack, ready func() bool) *experimentsModule {
	return &experimentsModule{
		ws:       ws,
		stack:    stack,
		ready:    ready,
		services: make(map[string]*experiments.Service),
	}
}

// service returns the experiment service of one project, building it once.
func (m *experimentsModule) service(ctx context.Context, projectID string) *experiments.Service {
	m.mu.Lock()
	cached := m.services[projectID]
	m.mu.Unlock()
	if cached != nil {
		return cached
	}

	var c clock.Clock = clock.System{}
	if m.ws != nil && m.ws.Clock() != nil {
		c = m.ws.Clock()
	}
	opts := experiments.Options{Clock: c, Logger: appLogger{}}
	if m.stack != nil && m.stack.Gateway != nil {
		if cfg, err := m.aiConfig(ctx, projectID, c); err == nil {
			opts.AI = cfg
		} else {
			log.Printf("boot: experiments AI unavailable for project %s: %v", projectID, err)
		}
	}
	svc := experiments.New(m.ws, projectID, opts)

	m.mu.Lock()
	if existing := m.services[projectID]; existing != nil {
		m.mu.Unlock()
		return existing
	}
	m.services[projectID] = svc
	m.mu.Unlock()
	return svc
}

// AIAvailable reports whether a chat provider is assigned for this workspace:
// the SPEC-02 stack must exist and chat_main must resolve locally, so the
// generation footer disables its entry with the 去设置 hint instead of failing
// per call (SPEC-07 AC-15).
func (m *experimentsModule) AIAvailable() bool {
	return m != nil && m.stack != nil && m.stack.Gateway != nil && m.ready != nil && m.ready()
}

// aiConfig builds the SPEC-02 generation and output services of one project.
// Both share the same read-only guard, so an archived project is refused
// before any network call.
func (m *experimentsModule) aiConfig(ctx context.Context, projectID string, c clock.Clock) (*experiments.AIConfig, error) {
	handle, err := m.ws.Project(ctx, projectID)
	if err != nil {
		return nil, err
	}
	readOnly := output.NewProjectReadOnly(m.ws.Global().DB(), projectID)
	outs, err := output.NewOutputService(handle.DB(), output.OutputOptions{
		Clock:     c,
		ProjectID: projectID,
		ReadOnly:  readOnly,
	})
	if err != nil {
		return nil, err
	}
	gen, err := output.NewGenerationService(output.GenerationOptions{
		Outputs:  outs,
		Runner:   m.stack.Gateway,
		Events:   handle.Events(),
		ReadOnly: readOnly,
		Logger: func(msg string, kv ...any) {
			log.Printf("experiments ai: %s %v", msg, kv)
		},
	})
	if err != nil {
		return nil, err
	}
	return &experiments.AIConfig{Generator: gen, Outputs: outs}, nil
}

// wa returns the service of projectID, or nil when the workspace is gone.
func (m *experimentsModule) wa(ctx context.Context, projectID string) *experiments.Service {
	if m == nil || m.ws == nil || projectID == "" {
		return nil
	}
	return m.service(ctx, projectID)
}

func (m *experimentsModule) CreateExperiment(ctx context.Context, projectID string, in experiments.CreateExperimentInput) (*experiments.Experiment, error) {
	return m.wa(ctx, projectID).CreateExperiment(ctx, in)
}

func (m *experimentsModule) UpdateExperiment(ctx context.Context, projectID string, in experiments.UpdateExperimentInput) (*experiments.Experiment, error) {
	return m.wa(ctx, projectID).UpdateExperiment(ctx, in)
}

func (m *experimentsModule) SoftDeleteExperiment(ctx context.Context, projectID, experimentID string, actor ports.Actor) error {
	return m.wa(ctx, projectID).SoftDeleteExperiment(ctx, experimentID, actor)
}

func (m *experimentsModule) GetExperiment(ctx context.Context, projectID, experimentID string) (*experiments.ExperimentDetail, error) {
	return m.wa(ctx, projectID).GetExperiment(ctx, experimentID)
}

func (m *experimentsModule) ListExperiments(ctx context.Context, projectID string, f experiments.ExperimentFilter) ([]experiments.ExperimentSummary, error) {
	return m.wa(ctx, projectID).ListExperiments(ctx, f)
}

func (m *experimentsModule) BindDirectory(ctx context.Context, projectID, experimentID, absPath string, rules experiments.ScanRules) (*experiments.DirReference, error) {
	return m.wa(ctx, projectID).BindDirectory(ctx, experimentID, absPath, rules)
}

func (m *experimentsModule) RelocateDirectory(ctx context.Context, projectID, experimentID, absPath string, actor ports.Actor) (*experiments.DirReference, error) {
	return m.wa(ctx, projectID).RelocateDirectory(ctx, experimentID, absPath, actor)
}

func (m *experimentsModule) VerifyDirectory(ctx context.Context, projectID, experimentID string) ([]experiments.DirReference, error) {
	return m.wa(ctx, projectID).VerifyDirectory(ctx, experimentID)
}

func (m *experimentsModule) SetScanRules(ctx context.Context, projectID, experimentID string, rules experiments.ScanRules) error {
	return m.wa(ctx, projectID).SetScanRules(ctx, experimentID, rules)
}

func (m *experimentsModule) CreateRun(ctx context.Context, projectID string, in experiments.CreateRunInput) (*experiments.Run, error) {
	return m.wa(ctx, projectID).CreateRun(ctx, in)
}

func (m *experimentsModule) SetRunStatus(ctx context.Context, projectID, runID string, status experiments.RunStatus, actor ports.Actor) error {
	return m.wa(ctx, projectID).SetRunStatus(ctx, runID, status, actor)
}

func (m *experimentsModule) GetRun(ctx context.Context, projectID, runID string) (*experiments.RunDetail, error) {
	return m.wa(ctx, projectID).GetRun(ctx, runID)
}

func (m *experimentsModule) ListRuns(ctx context.Context, projectID, experimentID string, includeDeleted bool) ([]experiments.RunSummary, error) {
	return m.wa(ctx, projectID).ListRuns(ctx, experimentID, includeDeleted)
}

func (m *experimentsModule) ListDirCandidates(ctx context.Context, projectID, experimentID string) ([]experiments.DirCandidate, error) {
	return m.wa(ctx, projectID).ListDirCandidates(ctx, experimentID)
}

func (m *experimentsModule) Plan(ctx context.Context, projectID string, target experiments.TargetRef, override *experiments.ScanRules) (*experiments.ScanPlan, error) {
	return m.wa(ctx, projectID).Plan(ctx, target, override)
}

func (m *experimentsModule) RunScan(ctx context.Context, projectID string, target experiments.TargetRef, override *experiments.ScanRules) (*experiments.ScanResult, error) {
	return m.wa(ctx, projectID).Run(ctx, target, override)
}

func (m *experimentsModule) CompareRuns(ctx context.Context, projectID, experimentID string) (*experiments.MetricComparison, error) {
	return m.wa(ctx, projectID).CompareRuns(ctx, experimentID)
}

func (m *experimentsModule) PreviewContext(ctx context.Context, projectID string, target experiments.TargetRef, opts experiments.AnalysisOptions) (*experiments.ContextBundle, error) {
	return m.wa(ctx, projectID).PreviewContext(ctx, target, opts)
}

func (m *experimentsModule) GenerateOutline(ctx context.Context, projectID, experimentID string, opts experiments.AnalysisOptions) (*experiments.Snapshot, error) {
	return m.wa(ctx, projectID).GenerateOutline(ctx, experimentID, opts)
}

func (m *experimentsModule) GenerateResults(ctx context.Context, projectID string, target experiments.TargetRef, opts experiments.AnalysisOptions) (*experiments.Snapshot, error) {
	return m.wa(ctx, projectID).GenerateResults(ctx, target, opts)
}

func (m *experimentsModule) UpdateContent(ctx context.Context, projectID, snapshotID, contentMD string, actor ports.Actor) error {
	return m.wa(ctx, projectID).UpdateContent(ctx, snapshotID, contentMD, actor)
}

func (m *experimentsModule) Confirm(ctx context.Context, projectID, snapshotID string, actor ports.Actor) error {
	return m.wa(ctx, projectID).Confirm(ctx, snapshotID, actor)
}

func (m *experimentsModule) RetrySaveSnapshot(ctx context.Context, projectID, aiOutputID string) (*experiments.Snapshot, error) {
	return m.wa(ctx, projectID).RetrySaveSnapshot(ctx, aiOutputID)
}

func (m *experimentsModule) ListSnapshots(ctx context.Context, projectID string, target experiments.TargetRef, f experiments.SnapshotFilter) ([]experiments.SnapshotSummary, error) {
	return m.wa(ctx, projectID).ListSnapshots(ctx, target, f)
}

func (m *experimentsModule) GetSnapshot(ctx context.Context, projectID, snapshotID string) (*experiments.Snapshot, error) {
	return m.wa(ctx, projectID).GetSnapshot(ctx, snapshotID)
}

func (m *experimentsModule) Inspect(ctx context.Context, projectID, dir string) (*experiments.GitInfo, error) {
	return m.wa(ctx, projectID).Inspect(ctx, dir)
}

func (m *experimentsModule) Diff(ctx context.Context, projectID, dir string, opts experiments.DiffOptions) (*experiments.GitDiff, error) {
	return m.wa(ctx, projectID).Diff(ctx, dir, opts)
}

func (m *experimentsModule) LinkIdea(ctx context.Context, projectID, experimentID, ideaID string, actor ports.Actor) error {
	return m.wa(ctx, projectID).LinkIdea(ctx, experimentID, ideaID, actor)
}

func (m *experimentsModule) UnlinkIdea(ctx context.Context, projectID, experimentID, ideaID string, actor ports.Actor) error {
	return m.wa(ctx, projectID).UnlinkIdea(ctx, experimentID, ideaID, actor)
}

func (m *experimentsModule) ListIdeas(ctx context.Context, projectID, experimentID string) ([]experiments.IdeaRef, error) {
	return m.wa(ctx, projectID).ListIdeas(ctx, experimentID)
}
