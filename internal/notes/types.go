package notes

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/indexhook"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/workspace"
)

// ---------------------------------------------------------------------------
// Notes (SPEC-05 §5, §5.1)
// ---------------------------------------------------------------------------

// NoteKind is the notes.kind discriminator. It mirrors schema.NoteKind.
type NoteKind string

// The four registered note kinds (SPEC-05 §5).
const (
	KindScratchpad NoteKind = "scratchpad"
	KindPaperNote  NoteKind = "paper_note"
	KindDBRecord   NoteKind = "db_record"
	KindOverview   NoteKind = "overview"
)

// Valid reports whether k is a registered note kind.
func (k NoteKind) Valid() bool { return schema.NoteKind(k).Valid() }

// BodyState mirrors notes.file_state: ok when the Markdown file is present,
// missing when it was lost (SPEC-05 §5.1 LoadBody).
type BodyState string

// The two body states (SPEC-05 §5).
const (
	BodyOK      BodyState = "ok"
	BodyMissing BodyState = "missing"
)

// Note is one notes row. The Markdown body lives in the workspace file named
// by FilePath; project.db only carries metadata (SPEC-00 §1).
type Note struct {
	ID          string
	Kind        NoteKind
	Title       string
	Description string
	FilePath    string // relative to the workspace root
	BodyState   BodyState
	ContentHash string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
}

// Body is the Markdown body plus the hash token used for save conflict
// detection. Markdown never includes the file frontmatter; Hash is the sha256
// of the whole file, frontmatter included. State missing means the file is
// gone; Markdown and Hash are empty.
type Body struct {
	NoteID   string
	Markdown string
	Hash     string // sha256 of the on-disk content read now (the client token)
	State    BodyState
}

// NotePage is one page of List plus the unpaged total.
type NotePage struct {
	Notes []*Note
	Total int
}

// CreateNoteInput creates one note and its Markdown file.
type CreateNoteInput struct {
	Kind        NoteKind // empty means scratchpad
	Title       string
	Description string
	Body        string // Markdown, may be empty
	Actor       events.Actor
}

// ListNotesQuery filters List. TitleLike is a case-insensitive substring match.
type ListNotesQuery struct {
	Kind           NoteKind
	TitleLike      string
	TagIDs         []string
	IncludeDeleted bool
	Limit, Offset  int
}

// SaveBodyInput is one debounced editor save (SPEC-05 §5.7).
type SaveBodyInput struct {
	NoteID   string
	Markdown string
	BaseHash string // hash the client last read or last saved
	Actor    events.Actor
}

// SaveResult reports a save. Conflict true means nothing was written and
// DiskHash carries the current on-disk token to retry with.
type SaveResult struct {
	Hash      string
	UpdatedAt time.Time
	Conflict  bool
	DiskHash  string
}

// UpdateMetaInput edits the metadata a note carries in project.db and in its
// file frontmatter. The body is never touched (SPEC-05 §5, D-05-01).
type UpdateMetaInput struct {
	NoteID      string
	Title       string
	Description string
	Actor       events.Actor
}

// PromoteInput moves a scratchpad note into a research database (D-05-04: the
// note kind and its Markdown file are unchanged).
type PromoteInput struct {
	NoteID     string
	DatabaseID string
	Actor      events.Actor
}

// NoteService is the note service of one project. UI and MCP share it
// (SPEC-00 §12).
type NoteService interface {
	Create(ctx context.Context, in CreateNoteInput) (*Note, error)
	Get(ctx context.Context, noteID string) (*Note, error)
	List(ctx context.Context, q ListNotesQuery) (NotePage, error)

	LoadBody(ctx context.Context, noteID string) (*Body, error)
	SaveBody(ctx context.Context, in SaveBodyInput) (*SaveResult, error)

	UpdateTitle(ctx context.Context, noteID, title string, actor events.Actor) (*Note, error)
	UpdateMeta(ctx context.Context, in UpdateMetaInput) (*Note, error)
	SoftDelete(ctx context.Context, noteID string, actor events.Actor) error
	Restore(ctx context.Context, noteID string, actor events.Actor) error

	// PromoteToDatabase turns the note into the detail page of a new record.
	PromoteToDatabase(ctx context.Context, in PromoteInput) (*Record, error)
	DetachFromDatabase(ctx context.Context, recordID string, deleteNote bool, actor events.Actor) error

	OutboundLinks(ctx context.Context, noteID string) ([]ResolvedLink, error)
	ResolveTarget(ctx context.Context, ref EntityRef) (*LinkTarget, error)
}

// ---------------------------------------------------------------------------
// Research databases and fields (SPEC-05 §4.2, §5.2)
// ---------------------------------------------------------------------------

// FieldRole is db_fields.role: exactly one title field per database.
type FieldRole string

// The two field roles (SPEC-05 §4.2).
const (
	RoleTitle FieldRole = "title"
	RolePlain FieldRole = "plain"
)

// FieldType is db_fields.type (SPEC-05 §4.4).
type FieldType string

// The nine field types of SPEC-05 §4.4.
const (
	FieldTypeText        FieldType = "text"
	FieldTypeNumber      FieldType = "number"
	FieldTypeSelect      FieldType = "select"
	FieldTypeMultiSelect FieldType = "multi_select"
	FieldTypeDate        FieldType = "date"
	FieldTypeCheckbox    FieldType = "checkbox"
	FieldTypeURL         FieldType = "url"
	FieldTypeRelation    FieldType = "relation"
	FieldTypeTags        FieldType = "tags"
)

// FieldTypeValid reports whether t is a registered field type.
func FieldTypeValid(t FieldType) bool {
	switch t {
	case FieldTypeText, FieldTypeNumber, FieldTypeSelect, FieldTypeMultiSelect,
		FieldTypeDate, FieldTypeCheckbox, FieldTypeURL, FieldTypeRelation, FieldTypeTags:
		return true
	}
	return false
}

// ResearchDatabase is one research_databases row (soft-deleted databases keep
// their fields and records).
type ResearchDatabase struct {
	ID          string
	Name        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
}

// Field is one db_fields row. Position maps to db_fields.sort_order; Config is
// the type-specific JSON (options, relation targets, ...).
type Field struct {
	ID         string
	DatabaseID string
	Name       string
	Type       FieldType
	Config     json.RawMessage
	Role       FieldRole
	Position   int
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  *time.Time
}

// CreateDatabaseInput creates a database plus its mandatory title field.
type CreateDatabaseInput struct {
	Name        string
	Description string
	Actor       events.Actor
}

// CreateFieldInput adds a field to an existing database.
type CreateFieldInput struct {
	DatabaseID string
	Name       string
	Type       FieldType
	Config     json.RawMessage
	Actor      events.Actor
}

// UpdateFieldInput renames or reconfigures a field. The type never changes in
// v1 (SPEC-05 §4.4: a type change means a new field).
type UpdateFieldInput struct {
	FieldID string
	Name    string
	Config  json.RawMessage
	Actor   events.Actor
}

// DatabaseService manages research databases and their field schemas.
type DatabaseService interface {
	CreateDatabase(ctx context.Context, in CreateDatabaseInput) (*ResearchDatabase, error)
	GetDatabase(ctx context.Context, dbID string) (*ResearchDatabase, error)
	ListDatabases(ctx context.Context, includeDeleted bool) ([]*ResearchDatabase, error)
	RenameDatabase(ctx context.Context, dbID, name string, actor events.Actor) (*ResearchDatabase, error)
	SoftDeleteDatabase(ctx context.Context, dbID string, actor events.Actor) error
	RestoreDatabase(ctx context.Context, dbID string, actor events.Actor) error

	CreateField(ctx context.Context, in CreateFieldInput) (*Field, error)
	UpdateField(ctx context.Context, in UpdateFieldInput) (*Field, error)
	ReorderFields(ctx context.Context, dbID string, orderedFieldIDs []string, actor events.Actor) error
	DeleteField(ctx context.Context, fieldID string, actor events.Actor) error
	RestoreField(ctx context.Context, fieldID string, actor events.Actor) error
	ListFields(ctx context.Context, dbID string) ([]*Field, error)
}

// ---------------------------------------------------------------------------
// Records, filters and sorting (SPEC-05 §4.5, §5.3)
// ---------------------------------------------------------------------------

// LogicOp combines child filter nodes.
type LogicOp string

// The two combination operators.
const (
	LogicAnd LogicOp = "and"
	LogicOr  LogicOp = "or"
)

// FilterNode is one node of the filter tree: a combination node when Children
// is non-empty, a leaf when FieldID is set. Depth is capped at 3 and leaves at
// 30 by the record service.
type FilterNode struct {
	Op       LogicOp
	Children []FilterNode
	FieldID  string
	Operator string
	Value    json.RawMessage
}

// Built-in sort keys accepted by SortKey.FieldID in addition to field ids.
const (
	SortTitle     = "__title__"
	SortCreatedAt = "__created_at__"
	SortUpdatedAt = "__updated_at__"
)

// SortKey is one sort column. Empty values always sort last; ties fall back to
// the record id for a stable order.
type SortKey struct {
	FieldID string
	Desc    bool
}

// Record is one db_records row. Values maps field id to the JSON value of
// SPEC-05 §4.4 (stored in the db_records.data column).
type Record struct {
	ID         string
	DatabaseID string
	NoteID     string
	Values     json.RawMessage
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  *time.Time
}

// RecordPage is one page of ListRecords plus the unpaged total.
type RecordPage struct {
	Records []*Record
	Total   int
}

// RecordQuery filters one table view.
type RecordQuery struct {
	DatabaseID     string
	Filter         *FilterNode
	Sorts          []SortKey
	IncludeDeleted bool
	Limit, Offset  int
}

// CreateRecordInput creates a row. An empty NoteID makes the service create an
// empty db_record note and point the row at it (D-05-03).
type CreateRecordInput struct {
	DatabaseID string
	NoteID     string
	Values     json.RawMessage
	Actor      events.Actor
}

// UpdateValuesInput replaces the record's field values. Unknown field ids are
// preserved; each known field is validated against its type.
type UpdateValuesInput struct {
	RecordID string
	Values   json.RawMessage
	Actor    events.Actor
}

// RecordService manages database rows.
type RecordService interface {
	ListRecords(ctx context.Context, q RecordQuery) (RecordPage, error)
	GetRecord(ctx context.Context, recordID string) (*Record, error)
	CreateRecord(ctx context.Context, in CreateRecordInput) (*Record, error)
	UpdateValues(ctx context.Context, in UpdateValuesInput) (*Record, error)
	SetTags(ctx context.Context, recordID string, tagIDs []string, actor events.Actor) error
	SoftDeleteRecord(ctx context.Context, recordID string, deleteNote bool, actor events.Actor) error
	RestoreRecord(ctx context.Context, recordID string, actor events.Actor) error
	ReorderRecords(ctx context.Context, dbID string, orderedRecordIDs []string, actor events.Actor) error
}

// ---------------------------------------------------------------------------
// Tags (SPEC-05 §4.2, §5.4)
// ---------------------------------------------------------------------------

// Tag is one row of the project-wide tag dictionary.
type Tag struct {
	ID        string
	Name      string
	Color     string
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// TagService manages tags and their assignments to notes and records.
type TagService interface {
	ListTags(ctx context.Context, includeDeleted bool) ([]*Tag, error)
	CreateTag(ctx context.Context, name, color string, actor events.Actor) (*Tag, error)
	RenameTag(ctx context.Context, tagID, name, color string, actor events.Actor) (*Tag, error)
	SoftDeleteTag(ctx context.Context, tagID string, actor events.Actor) error
	RestoreTag(ctx context.Context, tagID string, actor events.Actor) error
	Assign(ctx context.Context, refs []EntityRef, tagID string, actor events.Actor) error
	Unassign(ctx context.Context, refs []EntityRef, tagID string, actor events.Actor) error
	EntitiesByTag(ctx context.Context, tagID string) ([]EntityRef, error)
}

// ---------------------------------------------------------------------------
// Relations and link resolution (SPEC-05 §5.5, §7.2)
// ---------------------------------------------------------------------------

// EntityRef identifies one entity by registry type and UUIDv7.
type EntityRef struct {
	Type schema.EntityType
	ID   string
}

// LinkTarget is a resolved entity reference for the link renderer.
type LinkTarget struct {
	Ref      EntityRef
	Title    string
	Deleted  bool   // soft-deleted: keep the id, show the deleted marker
	Missing  bool   // no such row (dangling link or purged entity)
	FilePath string // note markdown path, empty for non-note targets
}

// ResolvedLink is one relation edge or body link with its target resolution.
type ResolvedLink struct {
	Src      EntityRef
	Relation schema.Relation
	Dst      EntityRef
	// Ref is the endpoint this result describes: the target for ListFrom and
	// OutboundLinks, the source for ListTo.
	Ref     EntityRef
	Title   string
	Deleted bool
	Missing bool
	// Raw is the link destination as written in the body (body links only).
	Raw string
}

// RelationEdge is one relation triple plus the acting principal. Src must be a
// note or db_record; Dst a paper, note, idea or experiment (SPEC-05 §5.5).
type RelationEdge struct {
	Src      EntityRef
	Relation schema.Relation // empty = derive from Src/Dst per the §5.5 mapping
	Dst      EntityRef
	Actor    events.Actor
}

// RelationService is the notes-facing relation API. It never writes symmetric
// copies: reverse lookups go through ListTo (D-05-08).
type RelationService interface {
	Add(ctx context.Context, edge RelationEdge) error
	Remove(ctx context.Context, edge RelationEdge) error
	ListFrom(ctx context.Context, ref EntityRef, relation string) ([]ResolvedLink, error)
	ListTo(ctx context.Context, ref EntityRef, relation string) ([]ResolvedLink, error)
}

// ---------------------------------------------------------------------------
// Attachments (SPEC-05 §5.6)
// ---------------------------------------------------------------------------

// Attachment is one image stored under attachments/<note-id>/.
type Attachment struct {
	RelativePath string // relative to the workspace root
	MarkdownPath string // ../attachments/<note-id>/<file>, usable from the note
	SHA256       string
	Bytes        int64
}

// AddImageInput inserts one image. MaxBytes 0 means the default limit.
type AddImageInput struct {
	NoteID   string
	Filename string
	Data     io.Reader
	MaxBytes int64
	Actor    events.Actor
}

// AttachmentIssueKind classifies CheckReferences results.
type AttachmentIssueKind string

// The two reference problems reported by CheckReferences.
const (
	AttachmentMissingReference AttachmentIssueKind = "missing_reference"
	AttachmentUnreferencedFile AttachmentIssueKind = "unreferenced_file"
)

// AttachmentIssue is one CheckReferences finding. Nothing is ever deleted
// automatically (SPEC-05 §10 O-09).
type AttachmentIssue struct {
	Kind         AttachmentIssueKind
	NoteID       string
	MarkdownPath string // the ../attachments/... path used in the body
	Path         string // relative path on disk, when the file exists
	Detail       string
}

// AttachmentService manages note images.
type AttachmentService interface {
	AddImage(ctx context.Context, in AddImageInput) (*Attachment, error)
	ListAttachments(ctx context.Context, noteID string) ([]*Attachment, error)
	CheckReferences(ctx context.Context, noteID string) ([]AttachmentIssue, error)
}

// ---------------------------------------------------------------------------
// Reconcile (SPEC-05 §5.8)
// ---------------------------------------------------------------------------

// Reconcile issue kinds (SPEC-05 §5.8).
const (
	IssueOrphanFile       = "orphan_file"
	IssueMissingFile      = "missing_file"
	IssueExternalChange   = "external_change"
	IssueOrphanAttachment = "orphan_attachment"
)

// Reconcile actions (SPEC-05 §5.8).
const (
	ActionRegisterOrphan = "register_orphan"
	ActionCreateEmpty    = "create_empty"
	ActionAdoptExternal  = "adopt_external"
	ActionIgnore         = "ignore"
)

// ReconcileIssue is one file/DB divergence found by Scan.
type ReconcileIssue struct {
	Kind   string
	NoteID string // empty for orphan_file
	Path   string
	Detail string
}

// ReconcileReport is the outcome of one Scan or Apply.
type ReconcileReport struct {
	Issues    []ReconcileIssue
	ScannedAt time.Time
}

// ReconcileDecision is one user choice. Action is one of the Action* constants;
// Title is optional for register_orphan.
type ReconcileDecision struct {
	Kind   string
	NoteID string
	Path   string
	Action string
	Title  string
	Actor  events.Actor
}

// ReconcileService compares note files with metadata and applies manual
// repairs. Each decision is its own transaction (SPEC-05 §5.8).
type ReconcileService interface {
	Scan(ctx context.Context, projectID string) (*ReconcileReport, error)
	Apply(ctx context.Context, decisions []ReconcileDecision) (*ReconcileReport, error)
}

// ---------------------------------------------------------------------------
// Shared service plumbing
// ---------------------------------------------------------------------------

// NoteDocument is the minimal indexable note shape (SPEC-05 §2, §10 O-14).
// The final field set is owned by SPEC-10; it is produced by
// (*noteService).Document for indexers that need the extracted text.
type NoteDocument struct {
	ID         string
	Title      string
	Markdown   string
	Tags       []string
	DatabaseID string // database whose table shows this note as a row, if any
}

// ServiceOptions are the optional collaborators shared by the notes services.
type ServiceOptions struct {
	// Clock supplies timestamps and ids. Nil means clock.System.
	Clock clock.Clock
	// Indexer is the post-commit search-index hook (SPEC-01 §4.7). Nil
	// disables indexing; its failures never fail a business write.
	Indexer indexhook.Indexer
	// Logger receives index-hook failures and other non-fatal warnings.
	Logger workspace.Logger
}
