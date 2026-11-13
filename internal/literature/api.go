package literature

import (
	"context"
	"time"
)

// ---------------------------------------------------------------------------
// §5.0 通用约定
// ---------------------------------------------------------------------------

type Actor string

const (
	ActorUser Actor = "user"
	ActorMCP  Actor = "mcp"
	ActorAI   Actor = "ai" // 本模块不产生 AI 写操作；保留以对齐 SPEC-00
)

type ProjectRef struct {
	ProjectID string
	Name      string
	Status    string // active | archived
}

type EntityRef struct {
	EntityType string // idea | note | paper
	EntityID   string
	Title      string
	Deleted    bool
}

type ImportError struct {
	ZoteroKey string
	Message   string
}

type SyncError struct {
	PaperID string
	Message string
}

type ReferenceResolution struct {
	Raw        string // 正文中的原始链接
	EntityType string
	EntityID   string
	Title      string
	Exists     bool
}

type ReconcileReport struct {
	PaperID     string
	FileExists  bool
	HashMatches bool
	StoredHash  string
	ActualHash  string
	SourceState string // ok | missing | unreadable
}

// ---------------------------------------------------------------------------
// §5.1 ZoteroConnectionService（LIT-01）
// ---------------------------------------------------------------------------

type DetectionSource string

const (
	DetectionUser    DetectionSource = "user"
	DetectionPrefs   DetectionSource = "prefs"
	DetectionDefault DetectionSource = "default"
)

type ZoteroCandidate struct {
	Path     string
	Source   DetectionSource
	Exists   bool
	Valid    bool
	Problems []string
}

type ZoteroDataDir struct {
	Path          string
	SQLitePath    string
	StorageDir    string
	Source        DetectionSource
	SchemaVersion int
	ZoteroVersion string
	Valid         bool
	Problems      []string
}

type LocalAPIStatus struct {
	Available bool
	BaseURL   string
	Version   string
	Error     string
}

type ZoteroConnectionStatus struct {
	DataDir   *ZoteroDataDir
	LocalAPI  LocalAPIStatus
	CheckedAt time.Time
}

type ZoteroConnectionService interface {
	// Detect 只读扫描候选目录，不写设置、不创建目录。
	Detect(ctx context.Context) ([]ZoteroCandidate, error)

	// Validate 校验给定目录是否为 Zotero 数据目录。
	Validate(ctx context.Context, dir string) (ZoteroDataDir, error)

	// SetDataDir 持久化手动目录并立即校验；空字符串表示清除手动设置、恢复自动检测。
	SetDataDir(ctx context.Context, dir string) (ZoteroDataDir, error)

	// Status 返回当前生效目录、候选来源、本地 API 可用性与诊断信息。
	Status(ctx context.Context) (ZoteroConnectionStatus, error)
}

// ---------------------------------------------------------------------------
// §5.2 ZoteroReader（LIT-02）
// ---------------------------------------------------------------------------

type MetadataSource string

const (
	SourceLocalAPI MetadataSource = "local_api"
	SourceSQLite   MetadataSource = "sqlite"
)

type ZoteroLibrary struct {
	ID   string // "0" 为个人库；组库为组 ID
	Name string
}

type ZoteroCreator struct {
	FirstName   string `json:"firstName"`
	LastName    string `json:"lastName"`
	Name        string `json:"name,omitempty"`
	CreatorType string `json:"creatorType"`
}

type ZoteroCollection struct {
	Key       string
	ParentKey string
	Name      string
	ItemCount int
}

type ZoteroItem struct {
	Key              string
	LibraryID        string
	Version          int
	ItemType         string
	Title            string
	Creators         []ZoteroCreator
	Date             string
	DOI              string
	URL              string
	AbstractNote     string
	PublicationTitle string
	Tags             []string
	Collections      []string
	Language         string
	Extra            string
	Deleted          bool
	Raw              map[string]any `json:"-"` // 未知 Zotero 字段原样保留（SPEC-04 §4.3）
}

type ZoteroAttachment struct {
	Key         string
	ParentKey   string
	LibraryID   string
	Filename    string
	ContentType string
	LinkMode    string // imported_file | imported_url | linked_file | linked_url
	Path        string // Zotero 记录值，可能带 attachments: 前缀
	Size        int64
}

type ItemPage struct {
	Items      []ZoteroItem
	Total      int
	SourceUsed MetadataSource
}

type ListItemsOptions struct {
	Offset int
	Limit  int // 1..100
	Query  string
}

type ResolvedFile struct {
	Path    string
	Size    int64
	ModTime time.Time
}

type ZoteroReader interface {
	Source() MetadataSource

	ListLibraries(ctx context.Context) ([]ZoteroLibrary, error)
	ListCollections(ctx context.Context, libraryID string) ([]ZoteroCollection, error)
	ListItems(ctx context.Context, libraryID, collectionKey string, opts ListItemsOptions) (ItemPage, error)
	GetItem(ctx context.Context, libraryID, key string) (ZoteroItem, error)
	ListAttachments(ctx context.Context, libraryID, parentKey string) ([]ZoteroAttachment, error)

	// ResolveAttachment 把附件解析为可读的本地文件路径；不修改文件。
	ResolveAttachment(ctx context.Context, att ZoteroAttachment) (ResolvedFile, error)
}

type ZoteroSource interface {
	// Reader 返回当前可用读取器；优先本地 API，失败回退 SQLite。
	Reader(ctx context.Context) (ZoteroReader, error)
	// Status 返回当前模式与最近一次回退原因，供 UI 展示。
	Status(ctx context.Context) (MetadataSource, string, error)
}

// ---------------------------------------------------------------------------
// §5.3 LiteratureQueryService（LIT-04）
// ---------------------------------------------------------------------------

type Paper struct {
	ID              string
	ZoteroKey       string
	ZoteroLibraryID string // v1 恒为 "0"（仅个人库）；建议列落地后从列读取
	Metadata        PaperMetadata
	FilePath        string // 相对工作区根目录
	FileHash        string
	ImportedAt      time.Time
	UpdatedAt       time.Time
}

type ZoteroTag struct {
	Tag  string `json:"tag"`
	Type int    `json:"type,omitempty"`
}

// PaperMetadata 与 papers.metadata 的 Zotero 原样字段一一对应；
// Year/Authors 为派生展示值，json:"-"，不写回数据库。
type PaperMetadata struct {
	Key              string          `json:"key"`
	Version          int             `json:"version"`
	ItemType         string          `json:"itemType"`
	Title            string          `json:"title"`
	Creators         []ZoteroCreator `json:"creators"`
	Date             string          `json:"date"`
	PublicationTitle string          `json:"publicationTitle"`
	DOI              string          `json:"DOI"`
	URL              string          `json:"url"`
	AbstractNote     string          `json:"abstractNote"`
	Tags             []ZoteroTag     `json:"tags"`
	Collections      []string        `json:"collections"`
	Language         string          `json:"language"`
	Extra            string          `json:"extra"`
	Year             int             `json:"-"`
	Authors          string          `json:"-"`
}

type PaperSort string

const (
	SortImportedAt PaperSort = "imported_at"
	SortTitle      PaperSort = "title"
	SortYear       PaperSort = "year"
	SortUpdatedAt  PaperSort = "updated_at"
)

type PaperFilter struct {
	Query          string   // 标题/作者/DOI/摘要/标签 的大小写不敏感子串
	CollectionKeys []string // Zotero Collection key
	Tags           []string
	YearFrom       int
	YearTo         int
	SourceState    []string // ok/missing/unreadable
	HasNote        *bool    // 仅项目查询有效
	Sort           PaperSort
	Desc           bool
	Offset         int
	Limit          int // 默认 100，上限 500
}

type PaperPage struct {
	Items []Paper
	Total int
}

type ProjectPaper struct {
	Paper    Paper
	LinkedAt time.Time
	NoteID   string // 空表示尚无 Paper Note
	Broken   bool   // 副本缺失
}

type ProjectPaperPage struct {
	Items []ProjectPaper
	Total int
}

type LiteratureQueryService interface {
	ListPapers(ctx context.Context, filter PaperFilter) (PaperPage, error)
	GetPaper(ctx context.Context, paperID string) (Paper, error)
	QueryProjectPapers(ctx context.Context, projectID string, filter PaperFilter) (ProjectPaperPage, error)
	// ListPaperProjects 返回引用该全局 Paper 的所有 Project（用于详情页显示多项目关联）。
	ListPaperProjects(ctx context.Context, paperID string) ([]ProjectRef, error)
}

// PaperBrief 与 SPEC-03 ports.PaperBrief 字段一一对应；适配层由 SPEC-03 负责。
type PaperBrief struct {
	ID       string
	Title    string
	Authors  string
	Year     int
	LinkedAt time.Time
	Missing  bool
}

// PaperIndex 供 SPEC-03 导出与真删引用计数使用；与 SPEC-03 §5.2 的消费侧端口签名一致。
type PaperIndex interface {
	ListProjectPaperIDs(ctx context.Context, projectID string) ([]string, error)
	CountPaperRefs(ctx context.Context, paperID, excludeProjectID string) (int, error)
	ListProjectPapers(ctx context.Context, projectID string, limit int) ([]PaperBrief, error)
}

// ---------------------------------------------------------------------------
// §5.4 ImportService（LIT-03、LIT-05）
// ---------------------------------------------------------------------------

type ImportSelection struct {
	ProjectID             string
	LibraryID             string // 默认 "0"
	CollectionKeys        []string
	IncludeSubcollections bool              // 默认 false
	ItemKeys              []string          // 与 Collection 可同时提供，合并去重
	AttachmentChoices     map[string]string // zotero item key -> attachment key
}

type ImportPreviewEntry struct {
	ZoteroKey       string
	LibraryID       string
	AttachmentKey   string
	Title           string
	Creators        []ZoteroCreator
	Year            int
	PDFAvailable    bool
	PDFCount        int
	ExistingPaperID string
	ExistingHash    string
	IncomingHash    string // 仅在可无副作用读取时填写；否则留空，导入时计算
	HashEqual       bool
	Warnings        []string
}

type ImportPreview struct {
	Entries []ImportPreviewEntry
	Errors  []ImportError
}

type ImportStatus string

const (
	ImportCreated          ImportStatus = "imported"
	ImportLinkedExisting   ImportStatus = "linked_existing"
	ImportSkippedNoPDF     ImportStatus = "skipped_no_pdf"
	ImportNeedsAttachment  ImportStatus = "needs_attachment_choice"
	ImportSkippedDuplicate ImportStatus = "already_linked"
	ImportFailed           ImportStatus = "failed"
)

type ImportOutcome struct {
	ZoteroKey string
	PaperID   string
	Status    ImportStatus
	Warnings  []string
	Error     string
}

type ImportResult struct {
	Outcomes []ImportOutcome
	Imported int
	Linked   int
	Skipped  int
	Failed   int
}

type ImportProgress struct {
	Index   int
	Total   int
	ItemKey string
	Status  ImportStatus
	Message string
}

type ProgressFunc func(ImportProgress)

type ImportService interface {
	// Preview 只读：展开 Collection、读取元数据与附件列表、检测已存在 Paper；不复制文件、不写 DB。
	Preview(ctx context.Context, sel ImportSelection) (ImportPreview, error)

	// Import 执行导入；逐条独立成败，不因单条失败回滚已成功条目。
	Import(ctx context.Context, sel ImportSelection, actor Actor, progress ProgressFunc) (ImportResult, error)
}

// ---------------------------------------------------------------------------
// §5.5 ProjectLinkService（LIT-05）
// ---------------------------------------------------------------------------

type LinkResult struct {
	PaperID string
	Added   bool // false 表示本来已关联
	Broken  bool // true 表示关联成功但副本缺失；UI 必须提示，不自动重建
}

type UnlinkResult struct {
	PaperID       string
	DetachedNotes []string // 该论文在本项目中的 Paper Note ID；仅提示，不删除
}

type ProjectLinkService interface {
	Link(ctx context.Context, projectID, paperID string, actor Actor) (LinkResult, error)
	Unlink(ctx context.Context, projectID, paperID string, actor Actor) (UnlinkResult, error)
	ListPaperProjects(ctx context.Context, paperID string) ([]ProjectRef, error)
}

// ---------------------------------------------------------------------------
// §5.6 PDFService（LIT-06）
// ---------------------------------------------------------------------------

type PDFTarget string

const (
	PDFImportedCopy PDFTarget = "imported_copy"
	PDFZoteroSource PDFTarget = "zotero_source"
)

type PDFLocation struct {
	PaperID string
	Path    string
	Exists  bool
	Size    int64
	Hash    string // 仅 imported_copy 返回已存哈希；源文件不计算
}

type PDFService interface {
	Locate(ctx context.Context, paperID string, target PDFTarget) (PDFLocation, error)
	Open(ctx context.Context, paperID string, target PDFTarget) error
}
