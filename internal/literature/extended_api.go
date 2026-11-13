package literature

import (
	"context"
	"time"
)

// ---------------------------------------------------------------------------
// §5.7 PaperNoteService（LIT-07）
// ---------------------------------------------------------------------------

// PaperNoteFields is the structured meta_json payload of one Paper Note.
type PaperNoteFields struct {
	Summary       string `json:"summary"`
	Problem       string `json:"problem"`
	Method        string `json:"method"`
	Contribution  string `json:"contribution"`
	Limitations   string `json:"limitations"`
	Understanding string `json:"understanding"`
}

// PaperNote is one kind=paper_note Note plus its structured fields.
type PaperNote struct {
	NoteID       string
	ProjectID    string
	PaperID      string
	Title        string
	Fields       PaperNoteFields
	BodyMD       string
	BodyHash     string
	RelatedIdeas []EntityRef
	RelatedNotes []EntityRef
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    *time.Time
}

// PaperNotePatch updates title and/or the whole structured field set.
type PaperNotePatch struct {
	Title  *string
	Fields *PaperNoteFields
}

// PaperNoteSaveResult reports a body save; Conflict means nothing was written.
type PaperNoteSaveResult struct {
	Note     PaperNote
	Conflict bool
	DiskHash string
}

// PaperNoteRef is the cross-project summary of one Paper Note.
type PaperNoteRef struct {
	ProjectID string
	NoteID    string
	Title     string
	UpdatedAt time.Time
	Deleted   bool
}

// PaperNoteService owns Paper Notes (LIT-07).
type PaperNoteService interface {
	Get(ctx context.Context, projectID, paperID string) (*PaperNote, error)
	Create(ctx context.Context, projectID, paperID string, actor Actor) (PaperNote, error)
	Update(ctx context.Context, noteID string, patch PaperNotePatch, expectedUpdatedAt time.Time, actor Actor) (PaperNote, error)
	SaveBody(ctx context.Context, noteID, bodyMD, baseHash string, actor Actor) (PaperNoteSaveResult, error)
	ListByPaper(ctx context.Context, paperID string) ([]PaperNoteRef, error)
	AddRelatedIdea(ctx context.Context, noteID, ideaID string, actor Actor) error
	RemoveRelatedIdea(ctx context.Context, noteID, ideaID string, actor Actor) error
	AddRelatedNote(ctx context.Context, noteID, targetNoteID string, actor Actor) error
	RemoveRelatedNote(ctx context.Context, noteID, targetNoteID string, actor Actor) error
}

// ---------------------------------------------------------------------------
// §5.8 OverviewService（LIT-08）
// ---------------------------------------------------------------------------

// LiteratureOverview is the singleton kind=overview note of one project.
type LiteratureOverview struct {
	NoteID            string
	ProjectID         string
	BodyMD            string
	BodyHash          string
	ReferencedNoteIDs []string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// OverviewSaveResult reports an overview save; Conflict means nothing written.
type OverviewSaveResult struct {
	Overview LiteratureOverview
	Conflict bool
	DiskHash string
}

// OverviewComparisonRow is one live comparison-table row.
type OverviewComparisonRow struct {
	PaperID     string
	NoteID      string
	Title       string
	Year        int
	Venue       string
	Authors     string
	Fields      PaperNoteFields
	SourceState string
}

// OverviewService owns the per-project literature overview (LIT-08).
type OverviewService interface {
	Get(ctx context.Context, projectID string) (*LiteratureOverview, error)
	Save(ctx context.Context, projectID, bodyMD, baseHash string, actor Actor) (OverviewSaveResult, error)
	Comparison(ctx context.Context, projectID string, paperIDs []string) ([]OverviewComparisonRow, error)
	ResolveReferences(ctx context.Context, projectID, bodyMD string) ([]ReferenceResolution, error)
}

// ---------------------------------------------------------------------------
// §5.9 SyncService（LIT-09）
// ---------------------------------------------------------------------------

// SyncScope selects papers for Diff/RefreshMetadata.
type SyncScope struct {
	ProjectID string
	PaperIDs  []string
}

// SyncDiff is one paper's source/local comparison.
type SyncDiff struct {
	PaperID        string
	Title          string
	ZoteroKey      string
	LibraryID      string
	SourceState    string
	SourcePath     string
	LocalState     string
	LocalHash      string
	SourceHash     string
	LocalSize      int64
	SourceSize     int64
	LocalModTime   time.Time
	SourceModTime  time.Time
	Changed        bool
	LocalCorrupted bool
	Warnings       []string
}

// SyncReport is an in-memory, TTL-bound comparison.
type SyncReport struct {
	ID        string
	CreatedAt time.Time
	Scope     SyncScope
	Diffs     []SyncDiff
	Unchanged int
	Errors    []SyncError
}

// OverwriteDecision is one explicit per-paper overwrite approval.
type OverwriteDecision struct {
	PaperID            string
	ExpectedLocalHash  string
	ExpectedSourceHash string
	Confirm            bool
}

// SyncApplyRequest applies explicit overwrite decisions.
type SyncApplyRequest struct {
	ReportID  string
	Decisions []OverwriteDecision
	Actor     Actor
}

// SyncApplyResult reports per-paper outcomes.
type SyncApplyResult struct {
	Overwritten []string
	Skipped     []string
	Conflicted  []string
	Failed      []SyncError
}

// ConfirmationRequired is returned to MCP callers before any write.
type ConfirmationRequired struct {
	ReportID string
	Diffs    []SyncDiff
}

// Error implements error.
func (e *ConfirmationRequired) Error() string { return ErrConfirmationRequired.Error() }

// Unwrap exposes the sentinel for errors.Is.
func (e *ConfirmationRequired) Unwrap() error { return ErrConfirmationRequired }

// MetadataRefreshResult reports a metadata-only refresh.
type MetadataRefreshResult struct {
	Updated     []string
	Missing     []string
	Failed      []SyncError
	RefreshedAt time.Time
}

// SyncService owns manual PDF synchronisation and metadata refresh (LIT-09).
type SyncService interface {
	Diff(ctx context.Context, scope SyncScope, verifyLocalCopy bool) (SyncReport, error)
	Apply(ctx context.Context, req SyncApplyRequest) (SyncApplyResult, error)
	RefreshMetadata(ctx context.Context, scope SyncScope, actor Actor) (MetadataRefreshResult, error)
}

// ---------------------------------------------------------------------------
// §5.10 清理与导出支持
// ---------------------------------------------------------------------------

// PaperExportEntry is one exported paper copy.
type PaperExportEntry struct {
	PaperID      string
	RelativePath string
	FileHash     string
	Metadata     PaperMetadata
}

// LibraryMaintenance serves export planning and read-only diagnostics.
type LibraryMaintenance interface {
	ListForExport(ctx context.Context, projectID string) ([]PaperExportEntry, error)
	ReconcilePaper(ctx context.Context, paperID string) (ReconcileReport, error)
}
