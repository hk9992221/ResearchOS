package ideas

import (
	"context"
	"time"

	"researchos/internal/foundation/events"
)

// Actor is the origin of a write. UI and MCP pass user or mcp; ai is rejected
// by every write method.
type Actor = events.Actor

// Status is one of the eight Idea statuses. The persisted strings are the
// frozen SPEC-00/SPEC-01 values (PascalCase); they never change without a
// migration.
type Status string

// The eight Idea statuses (SPEC-00 §4, SPEC-06 §4.2.1).
const (
	StatusNew                Status = "New"
	StatusResearching        Status = "Researching"
	StatusValidating         Status = "Validating"
	StatusSupported          Status = "Supported"
	StatusPartiallySupported Status = "PartiallySupported"
	StatusInconclusive       Status = "Inconclusive"
	StatusRejected           Status = "Rejected"
	StatusOnHold             Status = "OnHold"
)

// DisplayName is the localised label used by events and UI (SPEC-06 O9).
func (s Status) DisplayName() string {
	switch s {
	case StatusNew:
		return "新建"
	case StatusResearching:
		return "研究中"
	case StatusValidating:
		return "验证中"
	case StatusSupported:
		return "已支持"
	case StatusPartiallySupported:
		return "部分支持"
	case StatusInconclusive:
		return "不确定"
	case StatusRejected:
		return "已否定"
	case StatusOnHold:
		return "搁置"
	default:
		return string(s)
	}
}

// Valid reports whether s is a registered status.
func (s Status) Valid() bool { return validStatus(s) }

func validStatus(s Status) bool {
	switch s {
	case StatusNew, StatusResearching, StatusValidating, StatusSupported,
		StatusPartiallySupported, StatusInconclusive, StatusRejected, StatusOnHold:
		return true
	default:
		return false
	}
}

// Direction is the evidence direction.
type Direction string

// The two evidence directions.
const (
	DirectionSupport Direction = "support"
	DirectionOppose  Direction = "oppose"
)

// Valid reports whether d is support or oppose.
func (d Direction) Valid() bool { return d == DirectionSupport || d == DirectionOppose }

// DisplayName is the localised label used by events and UI.
func (d Direction) DisplayName() string {
	if d == DirectionSupport {
		return "支持"
	}
	if d == DirectionOppose {
		return "反对"
	}
	return string(d)
}

// SourceType is the closed set of Evidence sources (SPEC-06 §4.3.2).
type SourceType string

// The four Evidence source types.
const (
	SourceExperiment SourceType = "experiment"
	SourceRun        SourceType = "run"
	SourceNote       SourceType = "note"
	SourcePaper      SourceType = "paper"
)

// Valid reports whether t is a registered source type.
func (t SourceType) Valid() bool {
	switch t {
	case SourceExperiment, SourceRun, SourceNote, SourcePaper:
		return true
	default:
		return false
	}
}

// SourceRef is a polymorphic source reference: a type plus the target id.
type SourceRef struct {
	Type SourceType
	ID   string
}

// Idea is one ideas row.
type Idea struct {
	ID              string
	Title           string
	DescriptionMD   string
	Status          Status
	RejectionReason string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       *time.Time
}

// ProvenanceSource is one resolved input of an AI generation, written to the
// evidence provenance snapshot.
type ProvenanceSource struct {
	Type    SourceType `json:"type"`
	ID      string     `json:"id"`
	Version string     `json:"version"`
}

// Provenance is the AI provenance snapshot stored in evidence.provenance
// (SPEC-00 §7, SPEC-06 §4.4 A2).
type Provenance struct {
	ProviderID    string             `json:"provider_id"`
	ModelID       string             `json:"model_id"`
	PromptVersion string             `json:"prompt_version"`
	Skill         string             `json:"skill"`
	Sources       []ProvenanceSource `json:"sources"`
	Rationale     string             `json:"rationale,omitempty"`
	GeneratedAt   time.Time          `json:"generated_at"`
	Truncated     bool               `json:"truncated,omitempty"`
}

// Evidence is one evidence row.
type Evidence struct {
	ID          string
	IdeaID      string
	Direction   Direction
	ContentMD   string
	Source      SourceRef
	AIGenerated bool
	Confirmed   bool
	ContentHash string
	Provenance  *Provenance
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
}

// Page is the project-wide pagination envelope. Total is the unpaged match
// count and NextCursor is empty on the last page.
type Page[T any] struct {
	Items      []T
	Total      int
	NextCursor string
}

// CreateIdeaInput creates an Idea. The initial status is always New.
type CreateIdeaInput struct {
	Title         string
	DescriptionMD string
	Actor         Actor
}

// UpdateIdeaInput edits the title and/or description. Nil leaves a field
// untouched.
type UpdateIdeaInput struct {
	ID            string
	Title         *string
	DescriptionMD *string
	Actor         Actor
}

// ListIdeasQuery filters ListIdeas. Text matches title or description
// case-insensitively; semantic search belongs to SPEC-10.
type ListIdeasQuery struct {
	Statuses       []Status
	Text           string
	IncludeDeleted bool // true = trash view (deleted rows only)
	Sort           string
	PageSize       int
	Cursor         string
}

// Sort keys accepted by ListIdeasQuery.
const (
	SortUpdatedDesc = "updated_desc"
	SortCreatedDesc = "created_desc"
	SortTitleAsc    = "title_asc"
)

// IdeaSummary is a list row: the Idea plus count aggregates.
type IdeaSummary struct {
	Idea
	SupportCount  int
	OpposeCount   int
	DraftCount    int
	LinkedPapers  int
	LinkedNotes   int
	LinkedExps    int
	DerivedFromID string
}

// EvidenceSummary is the confirmed/draft evidence count block.
type EvidenceSummary struct {
	Support int
	Oppose  int
	Drafts  int
}

// IdeaDetail is the detail projection.
type IdeaDetail struct {
	Idea
	Evidence     EvidenceSummary
	Relations    RelationSet
	HistoryBrief []HistoryEntry
}

// EntityRef is a resolved relation endpoint. Title is a read-time snapshot;
// Deleted and BrokenRef describe a missing target.
type EntityRef struct {
	Type      string
	ID        string
	Title     string
	Deleted   bool
	BrokenRef bool
}

// RelationSet groups the Idea's relations by kind.
type RelationSet struct {
	Papers      []EntityRef
	Notes       []EntityRef
	Experiments []EntityRef
	DerivedFrom []EntityRef
	Derived     []EntityRef
}

// ChangeStatusInput changes the status. RejectionReason is required when To is
// Rejected.
type ChangeStatusInput struct {
	ID              string
	To              Status
	RejectionReason string
	Actor           Actor
}

// UpdateRejectionReasonInput edits the reason of a Rejected Idea.
type UpdateRejectionReasonInput struct {
	ID     string
	Reason string
	Actor  Actor
}

// LinkInput associates an Idea with a paper, note or experiment.
type LinkInput struct {
	IdeaID     string
	TargetType string
	TargetID   string
	Actor      Actor
}

// Link target types accepted by LinkInput.
const (
	TargetPaper      = "paper"
	TargetNote       = "note"
	TargetExperiment = "experiment"
)

// DeriveIdeaInput creates a new Idea derived from SourceIdeaID.
type DeriveIdeaInput struct {
	SourceIdeaID  string
	Title         string
	DescriptionMD string
	Actor         Actor
}

// AddEvidenceInput adds a manually confirmed Evidence.
type AddEvidenceInput struct {
	IdeaID    string
	Direction Direction
	ContentMD string
	Source    SourceRef
	Actor     Actor
}

// UpdateEvidenceInput edits Evidence. Direction may change only while the
// Evidence is an unconfirmed draft.
type UpdateEvidenceInput struct {
	EvidenceID string
	Direction  *Direction
	ContentMD  *string
	Actor      Actor
}

// ConfirmEvidenceInput confirms AI drafts of one Idea.
type ConfirmEvidenceInput struct {
	IdeaID      string
	EvidenceIDs []string
	Actor       Actor
}

// EvidenceFailure is one refused batch item.
type EvidenceFailure struct {
	ID      string
	Code    ErrorCode
	Message string
}

// ConfirmEvidenceResult reports the batch outcome.
type ConfirmEvidenceResult struct {
	Confirmed []string
	Failed    []EvidenceFailure
}

// DiscardEvidenceInput discards AI drafts of one Idea.
type DiscardEvidenceInput struct {
	IdeaID      string
	EvidenceIDs []string
	Actor       Actor
}

// DiscardEvidenceResult reports the batch outcome.
type DiscardEvidenceResult struct {
	Discarded []string
	Failed    []EvidenceFailure
}

// ListEvidenceQuery filters ListEvidence.
type ListEvidenceQuery struct {
	IdeaID         string
	Directions     []Direction
	Confirmed      *bool
	IncludeDeleted bool // true = trash view (deleted rows only)
	PageSize       int
	Cursor         string
}

// EvidenceView is Evidence plus the resolved source projection.
type EvidenceView struct {
	Evidence
	SourceTitle   string
	SourceDeleted bool
	SourceBroken  bool
}

// GenerateEvidenceInput triggers the evidence.normalize skill.
type GenerateEvidenceInput struct {
	IdeaID      string
	Sources     []SourceRef
	MaxDrafts   int
	Instruction string
	Actor       Actor
}

// ResolvedSource is one SourceResolver result.
type ResolvedSource struct {
	Ref       SourceRef
	Title     string
	Text      string
	Version   string
	Truncated bool
	Deleted   bool
}

// SourceResolver reads source text from SPEC-04/05/07. A module that is not
// wired yet returns an error, which degrades the skill to ErrSourceUnavailable
// (SPEC-06 §2.1).
type SourceResolver interface {
	Resolve(ctx context.Context, projectID string, ref SourceRef) (ResolvedSource, error)
}

// SkippedDraft is one AI item that was not persisted.
type SkippedDraft struct {
	Source      SourceRef
	Reason      string
	ContentHash string
}

// TokenUsage reports provider token usage.
type TokenUsage struct {
	InputTokens  int
	OutputTokens int
}

// GenerateEvidenceResult is the skill outcome.
type GenerateEvidenceResult struct {
	RunID    string
	Drafts   []Evidence
	Skipped  []SkippedDraft
	Usage    TokenUsage
	Provider string
	Model    string
}

// HistoryKind classifies one History entry.
type HistoryKind string

// The eight History kinds.
const (
	HistoryCreated  HistoryKind = "created"
	HistoryStatus   HistoryKind = "status"
	HistoryRelation HistoryKind = "relation"
	HistoryEvidence HistoryKind = "evidence"
	HistoryDeleted  HistoryKind = "deleted"
	HistoryRestored HistoryKind = "restored"
	HistoryAI       HistoryKind = "ai"
	HistoryDerived  HistoryKind = "derived"
)

// HistoryQuery filters History.
type HistoryQuery struct {
	IdeaID   string
	Kinds    []HistoryKind
	PageSize int
	Cursor   string
}

// HistoryEntry is one merged, newest-first History row.
type HistoryEntry struct {
	At          time.Time
	Kind        HistoryKind
	EventType   string
	Summary     string
	Actor       Actor
	Old         string
	New         string
	Ref         *EntityRef
	Synthesized bool
}

// Service is the Ideas module service of one project. UI and MCP share it.
type Service interface {
	// Idea lifecycle.
	CreateIdea(ctx context.Context, in CreateIdeaInput) (*Idea, error)
	UpdateIdea(ctx context.Context, in UpdateIdeaInput) (*Idea, error)
	GetIdea(ctx context.Context, ideaID string) (*IdeaDetail, error)
	ListIdeas(ctx context.Context, q ListIdeasQuery) (Page[IdeaSummary], error)
	DeleteIdea(ctx context.Context, ideaID string, actor Actor) error
	RestoreIdea(ctx context.Context, ideaID string, actor Actor) error

	// Status and rejection reason.
	ChangeStatus(ctx context.Context, in ChangeStatusInput) (*Idea, error)
	UpdateRejectionReason(ctx context.Context, in UpdateRejectionReasonInput) (*Idea, error)

	// Relations and derivation.
	LinkEntity(ctx context.Context, in LinkInput) error
	UnlinkEntity(ctx context.Context, in LinkInput) error
	ListRelations(ctx context.Context, ideaID string) (RelationSet, error)
	DeriveIdea(ctx context.Context, in DeriveIdeaInput) (*Idea, error)

	// Evidence.
	AddEvidence(ctx context.Context, in AddEvidenceInput) (*Evidence, error)
	UpdateEvidence(ctx context.Context, in UpdateEvidenceInput) (*Evidence, error)
	ConfirmEvidence(ctx context.Context, in ConfirmEvidenceInput) (ConfirmEvidenceResult, error)
	DiscardEvidence(ctx context.Context, in DiscardEvidenceInput) (DiscardEvidenceResult, error)
	DeleteEvidence(ctx context.Context, evidenceID string, actor Actor) error
	RestoreEvidence(ctx context.Context, evidenceID string, actor Actor) error
	ListEvidence(ctx context.Context, q ListEvidenceQuery) (Page[EvidenceView], error)

	// AI skill.
	GenerateEvidenceDrafts(ctx context.Context, in GenerateEvidenceInput) (*GenerateEvidenceResult, error)

	// History.
	History(ctx context.Context, q HistoryQuery) (Page[HistoryEntry], error)
}
