// Package experiments implements the deterministic backend of SPEC-07:
// experiment/run records, external experiment directories, read-only scanning,
// metadata and metric extraction, context budgets, read-only Git and the
// append-only analysis history.
//
// Implemented here: E01–E08 and the non-AI parts of E12 (experiment/run
// services, directory binding, archive read-only, Idea relations, snapshot
// history). SPEC-02 (AI service) is not part of the frozen foundation yet, so
// the AI tasks E09–E11 (Experiment Outline, Experiment Results, draft/confirm
// proxy) are intentionally absent: no temporary AI client, no stub, no
// fallback (SPEC-07 §2.1, O-18). The MyGo UI (E13/E14) and the MCP tool
// registry (SPEC-11) consume this same service layer; none of their code lives
// in this package.
package experiments

import (
	"context"
	"errors"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/workspace"
)

// ---------------------------------------------------------------------------
// Stable errors (SPEC-07 §5)
// ---------------------------------------------------------------------------

var (
	// ErrNotFound reports a missing or cross-project entity.
	ErrNotFound = errors.New("experiments: entity not found")
	// ErrReadOnlyProject reports a write on an archived project.
	ErrReadOnlyProject = errors.New("experiments: project is archived and read-only")
	// ErrInvalidInput reports a failed validation.
	ErrInvalidInput = errors.New("experiments: invalid input")
	// ErrDirUnavailable reports a directory that is missing, unreadable or not
	// a directory.
	ErrDirUnavailable = errors.New("experiments: experiment directory unavailable")
	// ErrDirGuard reports a workspace-internal path or a symlink escaping the
	// experiment root.
	ErrDirGuard = errors.New("experiments: directory path is not allowed")
	// ErrScanLimit reports a scan that hit an implementation limit.
	ErrScanLimit = errors.New("experiments: scan limit reached")
	// ErrNoContent reports a scan with no file that can enter AI content.
	ErrNoContent = errors.New("experiments: no content available for analysis")
	// ErrAIUnavailable reports that the AI service is not configured. It is
	// declared for the SPEC-02 integration (E09–E11), not used yet.
	ErrAIUnavailable = errors.New("experiments: AI service unavailable")
	// ErrInvalidAIOutput reports an AI payload that failed semantic validation.
	ErrInvalidAIOutput = errors.New("experiments: invalid AI output")
	// ErrSnapshotPersist reports a generated draft whose snapshot could not be
	// stored. It is declared for E09–E11, not used yet.
	ErrSnapshotPersist = errors.New("experiments: snapshot could not be stored")
)

// ---------------------------------------------------------------------------
// Target and status vocabulary (SPEC-07 §5)
// ---------------------------------------------------------------------------

// TargetType is the snapshots.target_type discriminator.
type TargetType string

// The two registered target types.
const (
	TargetExperiment TargetType = "experiment"
	TargetRun        TargetType = "run"
)

// Valid reports whether t is a registered target type.
func (t TargetType) Valid() bool { return t == TargetExperiment || t == TargetRun }

// TargetRef identifies an analysis target.
type TargetRef struct {
	Type TargetType
	ID   string
}

// RunStatus is the manual runs.status value (O-02: no completion detection).
type RunStatus string

// The four suggested run statuses (§5).
const (
	RunUnknown  RunStatus = "unknown"
	RunRunning  RunStatus = "running"
	RunFinished RunStatus = "finished"
	RunFailed   RunStatus = "failed"
)

// Valid reports whether s is a suggested status.
func (s RunStatus) Valid() bool {
	switch s {
	case RunUnknown, RunRunning, RunFinished, RunFailed:
		return true
	}
	return false
}

// DirStatus mirrors external_refs.status.
type DirStatus string

// The two external reference states (§4.3).
const (
	DirOK     DirStatus = "ok"
	DirBroken DirStatus = "broken"
)

// ---------------------------------------------------------------------------
// Entity projections
// ---------------------------------------------------------------------------

// Experiment is one experiments row. DirRef is the absolute directory path or
// nil when no directory is bound; health lives in external_refs (§4.2).
type Experiment struct {
	ID        string
	Title     string
	Purpose   string
	DirRef    *string
	ScanRules ScanRules
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// Run is one runs row. DirRef nil means "inherit the experiment directory".
type Run struct {
	ID           string
	ExperimentID string
	DirRef       *string
	Params       map[string]ParamValue
	GitInfo      GitInfo
	Status       RunStatus
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    *time.Time
}

// RunSummary is the sidebar projection of one run.
type RunSummary struct {
	ID           string
	DisplayName  string
	DirRef       *string
	Params       map[string]ParamValue
	Status       RunStatus
	ParamCount   int
	LastAnalyzed *time.Time
	DeletedAt    *time.Time
}

// RunDetail is a run with its directory status.
type RunDetail struct {
	Run     Run
	DirRefs []DirReference
}

// DirReference is one probed external reference (§4.3).
type DirReference struct {
	RefID         string
	OwnerType     TargetType
	OwnerID       string
	Path          string
	Status        DirStatus
	Exists        bool
	Reason        string
	LastCheckedAt *time.Time
}

// ExperimentSummary is the list projection (§5 ExperimentSummary).
type ExperimentSummary struct {
	ID             string
	Title          string
	Purpose        string
	DirStatus      DirStatus
	DirReason      string
	DirRef         *string
	RunCount       int
	LastRunStatus  RunStatus
	LastAnalyzedAt *time.Time
	LinkedIdeas    int
	UpdatedAt      time.Time
	DeletedAt      *time.Time
}

// ExperimentDetail is the detail projection (§5 ExperimentDetail).
type ExperimentDetail struct {
	Experiment      Experiment
	DirRefs         []DirReference
	Runs            []RunSummary
	RecentSnapshots []SnapshotSummary
	LinkedIdeas     []IdeaRef
}

// IdeaRef is one linked Idea, with its soft-deletion state (§3.6).
type IdeaRef struct {
	ID            string
	Title         string
	TargetDeleted bool
}

// SnapshotSummary is one history row (§3.7).
type SnapshotSummary struct {
	ID           string
	Target       TargetRef
	Kind         AnalysisKind
	CreatedAt    time.Time
	ProviderName string
	ModelID      string
	GitCommit    string
	MetricCount  int
	Warnings     int
	AIStatus     string
	HumanEdited  bool
	ConfirmedAt  *time.Time
}

// Snapshot is the logical snapshot view of §4.3.
type Snapshot struct {
	ID           string
	Target       TargetRef
	CreatedAt    time.Time
	FileManifest []FileEntry
	GitInfo      GitInfo
	Metrics      MetricSet
	AIAnalysis   *AIAnalysis
	ModelInfo    ModelInfo
}

// ---------------------------------------------------------------------------
// Requests and filters
// ---------------------------------------------------------------------------

// CreateExperimentInput creates one experiment (§5.1).
type CreateExperimentInput struct {
	Title   string
	Purpose string
	DirPath string // optional
	Rules   *ScanRules
	Actor   events.Actor
}

// UpdateExperimentInput edits title/purpose. Nil means unchanged.
type UpdateExperimentInput struct {
	ID      string
	Title   *string
	Purpose *string
}

// ExperimentFilter filters List. Zero values mean "no filter".
type ExperimentFilter struct {
	Title          string
	DirStatus      DirStatus
	IdeaID         string
	AnalyzedAfter  *time.Time
	IncludeDeleted bool
	Limit          int
	Offset         int
	Sort           string // "" = updated_at DESC, id
}

// CreateRunInput creates one run (§5.2).
type CreateRunInput struct {
	ExperimentID string
	DirPath      string // optional: inherit the experiment directory
	Params       map[string]ParamValue
	Actor        events.Actor
}

// DirCandidate is one first-level subdirectory of an experiment (§5.2).
type DirCandidate struct {
	Path      string
	FileCount int
}

// SnapshotFilter filters history reads (§5.9).
type SnapshotFilter struct {
	Kind   AnalysisKind // "" = all
	Limit  int
	Offset int
}

// DirCandidateLimit caps ListDirCandidates file previews.
const dirCandidatePreviewLimit = 200

// ---------------------------------------------------------------------------
// Scan / analysis projections
// ---------------------------------------------------------------------------

// Warning is one non-fatal scan or extraction problem.
type Warning struct {
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

// ScanPlan is the frozen file list of one scan (§5.3).
type ScanPlan struct {
	Target        TargetRef
	Root          string
	Entries       []FileEntry
	Pruned        []ExcludedEntry
	TotalFiles    int
	TotalBytes    int64
	IncludedFiles int
	IncludedBytes int64
	ContextBytes  int64
	Warnings      []Warning
	Truncated     bool
}

// ScanResult adds deterministic extraction to a plan (§5.3).
type ScanResult struct {
	Plan     ScanPlan
	Metadata Metadata
	Configs  []ExtractedConfig
	GitInfo  *GitInfo
	Warnings []Warning
}

// Metadata is the deterministic metadata of §3.3.
type Metadata struct {
	TotalFiles   int
	TotalBytes   int64
	KindCounts   map[FileKind]int
	Configs      []ExtractedConfig
	CSVs         []CSVSummary
	Readmes      []ReadmeSummary
	Dependencies []DependencySet
	EntryPoints  []EntryCandidate
	Warnings     []Warning
}

// EntryCandidate is a heuristic entry-point guess. Heuristic is always true in
// v1: the value is a naming convention, never a fact (§3.3).
type EntryCandidate struct {
	Path      string
	Heuristic bool
}

// CSVSummary describes one CSV/TSV file.
type CSVSummary struct {
	Path       string
	Delimiter  rune
	Header     []string
	Columns    int
	Rows       int64
	ParseError *ParseError
}

// ReadmeSummary is the title and heading list of one Markdown file.
type ReadmeSummary struct {
	Path     string
	Title    string
	Headings []string
}

// DependencySet is the declared dependencies of one environment file.
type DependencySet struct {
	Path         string
	Kind         string // requirements | pyproject | environment
	Dependencies []string
}

// ContextBundle is the budgeted AI context (§5.5).
type ContextBundle struct {
	Entries   []ContextEntry
	MetricSet *MetricSet
	Excluded  []ExcludedEntry
	Bytes     int64
	Truncated bool
}

// ContextEntry is one file content that would be sent to a model.
type ContextEntry struct {
	Path     string
	Kind     FileKind
	Bytes    int64
	SHA256   string
	Sampled  bool
	Content  string
	Priority int
}

// ---------------------------------------------------------------------------
// Git
// ---------------------------------------------------------------------------

// DiffOptions selects a read-only diff (§5.10).
type DiffOptions struct {
	From     string
	To       string
	Pathspec []string
	MaxBytes int64
}

// GitDiff is the read-only diff result.
type GitDiff struct {
	Files     []GitDiffFile
	Patch     string
	Truncated bool
}

// GitDiffFile is one file-level diff summary.
type GitDiffFile struct {
	Path    string
	Added   int
	Deleted int
	Binary  bool
}

// ---------------------------------------------------------------------------
// Service interfaces (SPEC-07 §5)
// ---------------------------------------------------------------------------

// ExperimentService manages experiments and their directory references.
type ExperimentService interface {
	Create(ctx context.Context, in CreateExperimentInput) (*Experiment, error)
	Update(ctx context.Context, in UpdateExperimentInput) (*Experiment, error)
	SoftDelete(ctx context.Context, id string, actor events.Actor) error
	Restore(ctx context.Context, id string, actor events.Actor) error
	Get(ctx context.Context, id string) (*ExperimentDetail, error)
	List(ctx context.Context, f ExperimentFilter) ([]ExperimentSummary, error)
	BindDirectory(ctx context.Context, experimentID, absPath string, rules ScanRules) (*DirReference, error)
	RelocateDirectory(ctx context.Context, experimentID, absPath string, actor events.Actor) (*DirReference, error)
	VerifyDirectory(ctx context.Context, experimentID string) ([]DirReference, error)
	SetScanRules(ctx context.Context, experimentID string, rules ScanRules) error
}

// RunService manages the optional second level.
type RunService interface {
	Create(ctx context.Context, in CreateRunInput) (*Run, error)
	UpdateParams(ctx context.Context, runID string, params map[string]ParamValue) error
	SetDirectory(ctx context.Context, runID, absPath string) error
	SetStatus(ctx context.Context, runID string, status RunStatus) error
	SoftDelete(ctx context.Context, runID string, actor events.Actor) error
	Restore(ctx context.Context, runID string, actor events.Actor) error
	Get(ctx context.Context, runID string) (*RunDetail, error)
	List(ctx context.Context, experimentID string, includeDeleted bool) ([]RunSummary, error)
	ListDirCandidates(ctx context.Context, experimentID string) ([]DirCandidate, error)
}

// ScanService walks an external directory without writing it.
type ScanService interface {
	Plan(ctx context.Context, target TargetRef, override *ScanRules) (*ScanPlan, error)
	Run(ctx context.Context, target TargetRef, override *ScanRules) (*ScanResult, error)
}

// MetadataService performs deterministic extraction only.
type MetadataService interface {
	Extract(ctx context.Context, scan *ScanResult) (*Metadata, error)
}

// MetricService extracts and compares metrics deterministically.
type MetricService interface {
	Extract(ctx context.Context, scan *ScanResult) (*MetricSet, error)
	CompareRuns(ctx context.Context, experimentID string) (*MetricComparison, error)
}

// ContextBuilder assembles the budgeted AI context.
type ContextBuilder interface {
	Build(ctx context.Context, scan *ScanResult, metrics *MetricSet, budget ContextBudget) (*ContextBundle, error)
	BuildForTarget(ctx context.Context, target TargetRef, opts AnalysisOptions) (*ContextBundle, error)
}

// ContextBudget bounds one ContextBundle.
type ContextBudget struct {
	MaxBytes int64
}

// AnalysisOptions tunes preview/analysis (§5.6). AI generation is deferred.
type AnalysisOptions struct {
	ExtraInstructions string
	ForceRehash       bool
	MaxContextBytes   int64
}

// SnapshotService reads history. Creation stays internal to the analysis
// pipeline (§5.9).
type SnapshotService interface {
	List(ctx context.Context, target TargetRef, f SnapshotFilter) ([]SnapshotSummary, error)
	Get(ctx context.Context, snapshotID string) (*Snapshot, error)
	Latest(ctx context.Context, target TargetRef, kind AnalysisKind) (*Snapshot, error)
}

// GitService reads Git state without any write or network operation.
type GitService interface {
	Inspect(ctx context.Context, dir string) (*GitInfo, error)
	Diff(ctx context.Context, dir string, opts DiffOptions) (*GitDiff, error)
}

// IdeaLinkService links experiments to ideas with the frozen validates
// relation (§5.8).
type IdeaLinkService interface {
	LinkIdea(ctx context.Context, experimentID, ideaID string, actor events.Actor) error
	UnlinkIdea(ctx context.Context, experimentID, ideaID string, actor events.Actor) error
	ListIdeas(ctx context.Context, experimentID string) ([]IdeaRef, error)
}

// Options configures a Service.
type Options struct {
	Clock  clock.Clock
	Logger workspace.Logger
}

// actorOf treats an empty actor as the local user, mirroring the notes service
// (§5: UI and MCP share the layer; the UI path always acts as user).
func actorOf(a events.Actor) events.Actor {
	if a == "" {
		return schema.ActorUser
	}
	return a
}
