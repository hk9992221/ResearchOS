// Package search implements the SPEC-10 unified retrieval layer: document
// extraction and chunking for the eight search sources, the incremental index
// coordinator (dirty queue, debounce, jobs, version reconciliation), and the
// project/global hybrid search pipeline (keyword + vector recall, RRF fusion,
// rerank degradation, source references and jump targets).
//
// Storage primitives live in internal/index/store (SPEC-02) and are never
// duplicated here (SPEC-10 §1.4). The index tables themselves are created by
// the SPEC-02 0110 migrations; this package only maintains the metadata columns
// SPEC-10 owns (parser_version, embedding_model_id, embedding_dim,
// index_status, indexed_at, error).
//
// The package writes no Activity rows and never mutates scientific entities:
// search, index maintenance and jump resolution are side-effect free with
// respect to the research data (SPEC-10 §5.1, §7.1).
package search

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// Search sources (SPEC-10 §3.4, §4.2)
// ---------------------------------------------------------------------------

// SourceType identifies one of the eight SPEC-10 search sources.
type SourceType string

// The eight registered search sources (SPEC-10 §3.4).
const (
	// SourcePaperMetadata is the global Zotero metadata projection.
	SourcePaperMetadata SourceType = "paper_metadata"
	// SourcePaperFullText is the global PDF full text, one chunk per page block.
	SourcePaperFullText SourceType = "paper_fulltext"
	// SourcePaperNote is a project paper note (notes.kind = paper_note).
	SourcePaperNote SourceType = "paper_note"
	// SourceDBRecord is one research database record and its detail note.
	SourceDBRecord SourceType = "db_record"
	// SourceScratchpad is a scratchpad note.
	SourceScratchpad SourceType = "scratchpad"
	// SourceIdea is one idea (title, description, status, rejection reason).
	SourceIdea SourceType = "idea"
	// SourceExperimentOutline is an experiment's latest Outline snapshot.
	SourceExperimentOutline SourceType = "experiment_outline"
	// SourceExperimentResults is an experiment's latest Results snapshot.
	SourceExperimentResults SourceType = "experiment_results"
)

var sourceTypes = []SourceType{
	SourcePaperMetadata, SourcePaperFullText, SourcePaperNote, SourceDBRecord,
	SourceScratchpad, SourceIdea, SourceExperimentOutline, SourceExperimentResults,
}

// AllSourceTypes returns the registry in declaration order.
func AllSourceTypes() []SourceType {
	out := make([]SourceType, len(sourceTypes))
	copy(out, sourceTypes)
	return out
}

// Valid reports whether s is a registered source.
func (s SourceType) Valid() bool {
	for _, v := range sourceTypes {
		if v == s {
			return true
		}
	}
	return false
}

// Global reports whether the source lives in the global literature index
// (project_id = "" partition) rather than in a project database.
func (s SourceType) Global() bool {
	return s == SourcePaperMetadata || s == SourcePaperFullText
}

// EntityType is the indexed entity kind (paper | note | db_record | idea |
// experiment), matching the foundation entity registry where one exists.
type EntityType string

// The five indexed entity types (SPEC-10 §4.2).
const (
	EntityPaper      EntityType = "paper"
	EntityNote       EntityType = "note"
	EntityDBRecord   EntityType = "db_record"
	EntityIdea       EntityType = "idea"
	EntityExperiment EntityType = "experiment"
)

// Valid reports whether e is an indexed entity type.
func (e EntityType) Valid() bool {
	switch e {
	case EntityPaper, EntityNote, EntityDBRecord, EntityIdea, EntityExperiment:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Document and chunk model (SPEC-10 §4.2)
// ---------------------------------------------------------------------------

// Rect is an optional normalized PDF highlight rectangle (0..1 in page space).
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Locator is the position of one chunk in its source. Pages are 1-based;
// CharStart/CharEnd are rune offsets inside the page (PDF) or the document
// (Markdown), CharStart inclusive and CharEnd exclusive.
type Locator struct {
	PageNo    int    `json:"page_no,omitempty"`
	BlockNo   int    `json:"block_no,omitempty"`
	CharStart int    `json:"char_start,omitempty"`
	CharEnd   int    `json:"char_end,omitempty"`
	Heading   string `json:"heading,omitempty"`
	FieldID   string `json:"field_id,omitempty"`
	Rects     []Rect `json:"rects,omitempty"`
}

// Chunk is one retrieval unit. ChunkID is stable across rebuilds
// (docID + "#" + ordinal).
type Chunk struct {
	ChunkID     string
	DocID       string
	Ordinal     int
	Text        string
	Locator     Locator
	ContentHash string
}

// IndexDoc is one indexable document projection.
type IndexDoc struct {
	DocID         string
	ProjectID     string // empty for the global literature partition
	Source        SourceType
	EntityType    EntityType
	EntityID      string
	Title         string
	Lang          string
	UpdatedAt     time.Time
	ContentHash   string
	ParserVersion string // "" means the source-specific default
	Chunks        []Chunk
	// NoText marks a parsed PDF without a text layer (status "no_text").
	NoText bool
}

// DocIDFor derives the idempotent document key of an entity document.
func DocIDFor(entityType EntityType, entityID string) string {
	return string(entityType) + ":" + entityID
}

// FullTextDocID is the document key of a paper's global PDF full text.
func FullTextDocID(paperID string) string { return "paper:" + paperID + ":fulltext" }

// ExperimentDocID is the document key of one experiment analysis kind.
func ExperimentDocID(experimentID string, source SourceType) string {
	suffix := "outline"
	if source == SourceExperimentResults {
		suffix = "results"
	}
	return "experiment:" + experimentID + ":" + suffix
}

// ---------------------------------------------------------------------------
// Change signals (SPEC-10 §5.3)
// ---------------------------------------------------------------------------

// ChangeKind is the kind of an index change signal.
type ChangeKind string

// The three change kinds.
const (
	ChangeUpsert  ChangeKind = "upsert"
	ChangeDelete  ChangeKind = "delete"
	ChangeRestore ChangeKind = "restore"
)

// Change is one post-commit index signal from an entity service. ProjectID is
// empty for global literature changes.
type Change struct {
	ProjectID  string
	EntityType EntityType
	EntityID   string
	Kind       ChangeKind
	At         time.Time
}

// ScopeKind selects a project or the global literature scope.
type ScopeKind string

// The two scope kinds.
const (
	ScopeProject ScopeKind = "project"
	ScopeGlobal  ScopeKind = "global"
)

// Scope is a search/index scope.
type Scope struct {
	Kind      ScopeKind
	ProjectID string // required when Kind == ScopeProject
}

// Zero reports whether the scope is the zero value.
func (s Scope) Zero() bool { return s.Kind == "" }

// Validate checks that the scope is well formed.
func (s Scope) Validate() error {
	switch s.Kind {
	case ScopeProject:
		if s.ProjectID == "" {
			return Errorf(CodeInvalidRequest, "项目作用域缺少 project_id")
		}
		return nil
	case ScopeGlobal:
		if s.ProjectID != "" {
			return Errorf(CodeInvalidRequest, "全局作用域不接受 project_id")
		}
		return nil
	case "":
		return Errorf(CodeInvalidRequest, "缺少检索作用域")
	default:
		return Errorf(CodeInvalidRequest, "未知作用域类型 %q", string(s.Kind))
	}
}

// ReindexRequest triggers a background rebuild (SPEC-10 §5.3).
type ReindexRequest struct {
	Scope         Scope
	ReparsePDF    bool
	RebuildVector bool
}

// JobID identifies a background reindex job.
type JobID string

// IndexStatus reports index health for one scope (SPEC-10 §5.3).
type IndexStatus struct {
	Scope          Scope
	Docs           int
	Chunks         int
	Dirty          int
	Failed         int
	Indexing       bool
	Progress       float64
	LastIndexedAt  time.Time
	SchemaVersion  string
	ParserVersion  string
	EmbeddingModel string
	Stale          bool
	LastError      string
}

// Doc index status values written to index_docs.index_status.
const (
	StatusPending       = "pending"
	StatusOK            = "ok"
	StatusVectorPending = "vector_pending"
	StatusFailed        = "failed"
	StatusNoText        = "no_text"
	StatusDeleted       = "deleted"
	StatusStale         = "stale"
	StatusFileStale     = "index_stale" // 正文文件丢失：保留上次索引（SPEC-10 §3.4）
)

// ---------------------------------------------------------------------------
// Search request and response (SPEC-10 §5.1, §5.2)
// ---------------------------------------------------------------------------

// Filters restricts a search. Empty slices mean "no restriction".
type Filters struct {
	Sources      []SourceType
	EntityTypes  []EntityType
	YearMin      *int
	YearMax      *int
	Authors      []string
	PaperIDs     []string
	UpdatedAfter *time.Time
}

// RerankMode selects the rerank policy.
type RerankMode string

// The three rerank modes.
const (
	RerankAuto  RerankMode = "auto"
	RerankOff   RerankMode = "off"
	RerankForce RerankMode = "force"
)

// SearchRequest is one query. An empty Query with Filters is browse mode
// (updated_at descending).
type SearchRequest struct {
	Query          string
	Scope          Scope
	Filters        Filters
	Limit          int
	Offset         int
	IncludeDeleted bool
	// IncludeArchived is kept for the frozen SPEC-10 request shape; archived
	// projects always participate in global search (SPEC-00 §3, A10). The zero
	// value is the documented default (true), so this field cannot express
	// exclusion.
	IncludeArchived bool
	Rerank          RerankMode
}

// Score is the auditable scoring breakdown of one result.
type Score struct {
	KeywordRank int
	VectorRank  int
	Fusion      float64
	Rerank      *float64
}

// JumpTarget is an executable navigation target. Opening it is a desktop
// concern and changes no research data (SPEC-10 §5.2, §7.3).
type JumpTarget struct {
	Kind      string // note | idea | experiment | db_record | paper
	ProjectID string
	EntityID  string
	Locator   Locator
	PaperID   string // Kind == paper / paper_note
	PDFPath   string // absolute path of the local PDF copy; empty when absent
}

// Snippet is one highlighted excerpt.
type Snippet struct {
	Text       string
	Highlights []Range
	Locator    Locator
}

// Range is a rune range inside Snippet.Text (Start inclusive, End exclusive).
type Range struct{ Start, End int }

// SearchResult is one document result (SPEC-10 §5.1).
type SearchResult struct {
	DocID       string
	Source      SourceType
	EntityType  EntityType
	EntityID    string
	ProjectID   string
	ProjectName string
	Archived    bool
	Deleted     bool
	Title       string
	Snippets    []Snippet
	Locator     Locator
	Score       Score
	UpdatedAt   time.Time
	Target      JumpTarget
}

// DegradeReason is a visible capability degradation (SPEC-00 §7 contract).
type DegradeReason string

// The degradation reasons of SPEC-10 §5.1.
const (
	DegradeVectorUnavailable DegradeReason = "vector_unavailable"
	DegradeEmbeddingFailed   DegradeReason = "embedding_failed"
	DegradeRerankUnavailable DegradeReason = "rerank_unavailable"
	DegradeRerankTimeout     DegradeReason = "rerank_timeout"
	DegradeProjectTimeout    DegradeReason = "project_timeout"
	DegradeIndexStale        DegradeReason = "index_stale"
)

// SearchResponse is one complete search answer.
type SearchResponse struct {
	Results    []SearchResult
	Total      int
	HasMore    bool
	Partial    bool
	Degraded   []DegradeReason
	Timings    map[string]int64 // milliseconds: normalize/keyword/vector/fusion/rerank/total
	IndexEpoch string
}

// SearchService is the SPEC-10 §5.2 retrieval contract shared by UI and MCP.
type SearchService interface {
	Search(ctx context.Context, req SearchRequest) (SearchResponse, error)
	ResolveTarget(ctx context.Context, ref EntityRef, loc Locator) (JumpTarget, error)
	OpenTarget(ctx context.Context, target JumpTarget) error
}

// IndexCoordinator is the SPEC-10 §5.3 index maintenance contract.
type IndexCoordinator interface {
	Notify(ctx context.Context, ch Change) error
	Flush(ctx context.Context, scope Scope) error
	Reindex(ctx context.Context, req ReindexRequest) (JobID, error)
	Status(ctx context.Context, scope Scope) (IndexStatus, error)
	CancelJob(ctx context.Context, jobID string) error
}

// EntityRef identifies one entity for ResolveTarget.
type EntityRef struct {
	ProjectID  string
	EntityType EntityType
	EntityID   string
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// Machine-readable error codes. They are stable strings so MCP and the UI can
// branch on them without parsing messages.
const (
	CodeInvalidRequest   = "search.invalid_request"
	CodeScopeNotFound    = "search.scope_not_found"
	CodeIndexUnavailable = "search.index_unavailable"
	CodeNoTextLayer      = "search.pdf_no_text_layer"
	CodePDFUnreadable    = "search.pdf_unreadable"
	CodeSourceMissing    = "search.source_file_missing"
	CodeJobNotFound      = "search.job_not_found"
	CodeTargetNotFound   = "search.target_not_found"
	CodeOpenUnsupported  = "search.open_unsupported"
	CodeOpenFailed       = "search.open_failed"
	CodeIndexWriteFailed = "search.index_write_failed"
)

// Error is a search error with a Chinese display message and a machine code.
type Error struct {
	Code    string
	Message string
	Err     error
}

// Error renders the display message and the cause.
func (e *Error) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

// Unwrap exposes the sentinel cause for errors.Is.
func (e *Error) Unwrap() error { return e.Err }

// Errorf builds an *Error.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// WrapError attaches a code and message to err.
func WrapError(code, message string, err error) *Error {
	return &Error{Code: code, Message: message, Err: err}
}

// Sentinel causes used with errors.Is.
var (
	ErrNoTextLayer       = errors.New("search: pdf has no text layer")
	ErrSourceFileMissing = errors.New("search: source file missing")
	ErrJobNotFound       = errors.New("search: job not found")
	ErrProjectNotFound   = errors.New("search: project not found")
	ErrEntityNotFound    = errors.New("search: entity not found")
	ErrOpenUnsupported   = errors.New("search: navigation is not available")
)

var (
	_ SearchService    = (*Service)(nil)
	_ IndexCoordinator = (*Service)(nil)
)
