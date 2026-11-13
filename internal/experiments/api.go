package experiments

import (
	"context"

	"researchos/internal/foundation/events"
)

// The service layer is split into the interfaces of §5. One *Service provides
// them all; these facades keep the method sets separate (the SPEC gives List,
// Get and Extract to several interfaces).

// Experiments returns the experiment facade.
func (s *Service) Experiments() ExperimentService { return &experimentAPI{s: s} }

// Runs returns the run facade.
func (s *Service) Runs() RunService { return &runAPI{s: s} }

// Scanning returns the scan facade.
func (s *Service) Scanning() ScanService { return &scanAPI{s: s} }

// Metadata returns the deterministic metadata facade.
func (s *Service) Metadata() MetadataService { return &metadataAPI{s: s} }

// Metrics returns the deterministic metric facade.
func (s *Service) Metrics() MetricService { return &metricAPI{s: s} }

// Context returns the context builder facade.
func (s *Service) Context() ContextBuilder { return &contextAPI{s: s} }

// Snapshots returns the history facade.
func (s *Service) Snapshots() SnapshotService { return &snapshotAPI{s: s} }

// Git returns the read-only Git facade.
func (s *Service) Git() GitService { return &gitAPI{s: s} }

// Ideas returns the idea-link facade.
func (s *Service) Ideas() IdeaLinkService { return &ideaAPI{s: s} }

type experimentAPI struct{ s *Service }

func (a *experimentAPI) Create(ctx context.Context, in CreateExperimentInput) (*Experiment, error) {
	return a.s.CreateExperiment(ctx, in)
}
func (a *experimentAPI) Update(ctx context.Context, in UpdateExperimentInput) (*Experiment, error) {
	return a.s.UpdateExperiment(ctx, in)
}
func (a *experimentAPI) SoftDelete(ctx context.Context, id string, actor events.Actor) error {
	return a.s.SoftDeleteExperiment(ctx, id, actor)
}
func (a *experimentAPI) Restore(ctx context.Context, id string, actor events.Actor) error {
	return a.s.RestoreExperiment(ctx, id, actor)
}
func (a *experimentAPI) Get(ctx context.Context, id string) (*ExperimentDetail, error) {
	return a.s.GetExperiment(ctx, id)
}
func (a *experimentAPI) List(ctx context.Context, f ExperimentFilter) ([]ExperimentSummary, error) {
	return a.s.ListExperiments(ctx, f)
}
func (a *experimentAPI) BindDirectory(ctx context.Context, experimentID, absPath string, rules ScanRules) (*DirReference, error) {
	return a.s.BindDirectory(ctx, experimentID, absPath, rules)
}
func (a *experimentAPI) RelocateDirectory(ctx context.Context, experimentID, absPath string, actor events.Actor) (*DirReference, error) {
	return a.s.RelocateDirectory(ctx, experimentID, absPath, actor)
}
func (a *experimentAPI) VerifyDirectory(ctx context.Context, experimentID string) ([]DirReference, error) {
	return a.s.VerifyDirectory(ctx, experimentID)
}
func (a *experimentAPI) SetScanRules(ctx context.Context, experimentID string, rules ScanRules) error {
	return a.s.SetScanRules(ctx, experimentID, rules)
}

type runAPI struct{ s *Service }

func (a *runAPI) Create(ctx context.Context, in CreateRunInput) (*Run, error) {
	return a.s.CreateRun(ctx, in)
}
func (a *runAPI) UpdateParams(ctx context.Context, runID string, params map[string]ParamValue) error {
	return a.s.UpdateRunParams(ctx, runID, params)
}
func (a *runAPI) SetDirectory(ctx context.Context, runID, absPath string) error {
	return a.s.SetRunDirectory(ctx, runID, absPath)
}
func (a *runAPI) SetStatus(ctx context.Context, runID string, status RunStatus) error {
	return a.s.SetRunStatus(ctx, runID, status)
}
func (a *runAPI) SoftDelete(ctx context.Context, runID string, actor events.Actor) error {
	return a.s.SoftDeleteRun(ctx, runID, actor)
}
func (a *runAPI) Restore(ctx context.Context, runID string, actor events.Actor) error {
	return a.s.RestoreRun(ctx, runID, actor)
}
func (a *runAPI) Get(ctx context.Context, runID string) (*RunDetail, error) {
	return a.s.GetRun(ctx, runID)
}
func (a *runAPI) List(ctx context.Context, experimentID string, includeDeleted bool) ([]RunSummary, error) {
	return a.s.ListRuns(ctx, experimentID, includeDeleted)
}
func (a *runAPI) ListDirCandidates(ctx context.Context, experimentID string) ([]DirCandidate, error) {
	return a.s.ListDirCandidates(ctx, experimentID)
}

type scanAPI struct{ s *Service }

func (a *scanAPI) Plan(ctx context.Context, target TargetRef, override *ScanRules) (*ScanPlan, error) {
	return a.s.Plan(ctx, target, override)
}
func (a *scanAPI) Run(ctx context.Context, target TargetRef, override *ScanRules) (*ScanResult, error) {
	return a.s.Run(ctx, target, override)
}

type metadataAPI struct{ s *Service }

func (a *metadataAPI) Extract(ctx context.Context, scan *ScanResult) (*Metadata, error) {
	return a.s.ExtractMetadata(ctx, scan)
}

type metricAPI struct{ s *Service }

func (a *metricAPI) Extract(ctx context.Context, scan *ScanResult) (*MetricSet, error) {
	return a.s.ExtractMetrics(ctx, scan)
}
func (a *metricAPI) CompareRuns(ctx context.Context, experimentID string) (*MetricComparison, error) {
	return a.s.CompareRuns(ctx, experimentID)
}

type contextAPI struct{ s *Service }

func (a *contextAPI) Build(ctx context.Context, scan *ScanResult, metrics *MetricSet, budget ContextBudget) (*ContextBundle, error) {
	return a.s.Build(ctx, scan, metrics, budget)
}
func (a *contextAPI) BuildForTarget(ctx context.Context, target TargetRef, opts AnalysisOptions) (*ContextBundle, error) {
	return a.s.BuildForTarget(ctx, target, opts)
}

type snapshotAPI struct{ s *Service }

func (a *snapshotAPI) List(ctx context.Context, target TargetRef, f SnapshotFilter) ([]SnapshotSummary, error) {
	return a.s.ListSnapshots(ctx, target, f)
}
func (a *snapshotAPI) Get(ctx context.Context, snapshotID string) (*Snapshot, error) {
	return a.s.GetSnapshot(ctx, snapshotID)
}
func (a *snapshotAPI) Latest(ctx context.Context, target TargetRef, kind AnalysisKind) (*Snapshot, error) {
	return a.s.LatestSnapshot(ctx, target, kind)
}

type gitAPI struct{ s *Service }

func (a *gitAPI) Inspect(ctx context.Context, dir string) (*GitInfo, error) {
	return a.s.Inspect(ctx, dir)
}
func (a *gitAPI) Diff(ctx context.Context, dir string, opts DiffOptions) (*GitDiff, error) {
	return a.s.Diff(ctx, dir, opts)
}

type ideaAPI struct{ s *Service }

func (a *ideaAPI) LinkIdea(ctx context.Context, experimentID, ideaID string, actor events.Actor) error {
	return a.s.LinkIdea(ctx, experimentID, ideaID, actor)
}
func (a *ideaAPI) UnlinkIdea(ctx context.Context, experimentID, ideaID string, actor events.Actor) error {
	return a.s.UnlinkIdea(ctx, experimentID, ideaID, actor)
}
func (a *ideaAPI) ListIdeas(ctx context.Context, experimentID string) ([]IdeaRef, error) {
	return a.s.ListIdeas(ctx, experimentID)
}

var (
	_ ExperimentService = (*experimentAPI)(nil)
	_ RunService        = (*runAPI)(nil)
	_ ScanService       = (*scanAPI)(nil)
	_ MetadataService   = (*metadataAPI)(nil)
	_ MetricService     = (*metricAPI)(nil)
	_ ContextBuilder    = (*contextAPI)(nil)
	_ SnapshotService   = (*snapshotAPI)(nil)
	_ GitService        = (*gitAPI)(nil)
	_ IdeaLinkService   = (*ideaAPI)(nil)
)
