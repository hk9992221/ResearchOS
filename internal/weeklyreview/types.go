package weeklyreview

import "time"

// Status is the weekly report lifecycle state (SPEC-09 §4.5).
type Status string

// Registered statuses.
const (
	StatusDraft     Status = "draft"
	StatusConfirmed Status = "confirmed"
)

// Valid reports whether s is a registered report status.
func (s Status) Valid() bool { return s == StatusDraft || s == StatusConfirmed }

// SectionKey identifies one of the seven default sections.
type SectionKey string

// The fixed default-7 sections (SPEC-09 §5.5).
const (
	SectionWork       SectionKey = "work"
	SectionLiterature SectionKey = "literature"
	SectionTheory     SectionKey = "theory"
	SectionExperiment SectionKey = "experiment"
	SectionIdea       SectionKey = "idea"
	SectionProblem    SectionKey = "problem"
	SectionNextWeek   SectionKey = "next_week"
)

// Actor identifies who performs a write (SPEC-09 §5.1).
type Actor string

// Registered actors.
const (
	ActorUser Actor = "user"
	ActorMCP  Actor = "mcp"
	ActorAI   Actor = "ai"
)

// Period is the half-open report window [Start, EndExclusive) in the report
// timezone (SPEC-09 §4.4). Dates are midnight in that timezone.
type Period struct {
	Start        time.Time
	EndExclusive time.Time
}

// Date returns the YYYY-MM-DD start date.
func (p Period) StartDate() string { return p.Start.Format(dateLayout) }

// EndDate returns the YYYY-MM-DD inclusive end date.
func (p Period) EndDate() string {
	return p.EndExclusive.AddDate(0, 0, -1).Format(dateLayout)
}

// Days returns the number of calendar days in the period.
func (p Period) Days() int {
	days := 0
	for cur := p.Start; cur.Before(p.EndExclusive); cur = cur.AddDate(0, 0, 1) {
		days++
	}
	return days
}

// Empty reports whether the period is unset.
func (p Period) Empty() bool { return p.Start.IsZero() || p.EndExclusive.IsZero() }

const dateLayout = "2006-01-02"

// EntityRef names one project entity or global paper.
type EntityRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// Ack records an in-app acknowledgement of one validation finding.
type Ack struct {
	Actor string    `json:"actor"`
	At    time.Time `json:"at"`
	Note  string    `json:"note,omitempty"`
}

// Violation is one Validate finding (SPEC-09 §5.4).
type Violation struct {
	Rule        string     `json:"rule"`
	Severity    string     `json:"severity"` // error|warning
	Fingerprint string     `json:"fingerprint"`
	Section     SectionKey `json:"section"`
	Ref         *EntityRef `json:"ref,omitempty"`
	Message     string     `json:"message"`
	Ack         *Ack       `json:"ack,omitempty"`
}

// Severity constants.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// GenerationMeta is the provenance snapshot inside meta.generation.
type GenerationMeta struct {
	Mode       string `json:"mode"`
	AIStatus   string `json:"ai_status"`
	AIOutputID string `json:"ai_output_id,omitempty"`
	// AIOutputIDs lists every section call that contributed to the body, in
	// template order. Confirm confirms all of them in one transaction.
	AIOutputIDs     []string  `json:"ai_output_ids,omitempty"`
	ProviderID      string    `json:"provider_id,omitempty"`
	ModelID         string    `json:"model_id,omitempty"`
	PromptVersion   string    `json:"prompt_version,omitempty"`
	TemplateID      string    `json:"template_id"`
	TemplateVersion int       `json:"template_version"`
	GeneratedAt     time.Time `json:"generated_at,omitempty"`
	DigestHash      string    `json:"digest_hash,omitempty"`
	Attempts        int       `json:"attempts"`
	Error           string    `json:"error,omitempty"`
	// Notes explains a partial result, such as sections that fell back to
	// the deterministic skeleton.
	Notes string `json:"notes,omitempty"`
}

// Coverage is the digest coverage snapshot inside meta.coverage.
type Coverage struct {
	PeriodStart     string         `json:"period_start"`
	PeriodEnd       string         `json:"period_end"`
	Timezone        string         `json:"timezone"`
	EventCount      int            `json:"event_count"`
	CountsByType    map[string]int `json:"counts_by_type"`
	CountsBySection map[string]int `json:"counts_by_section"`
	Truncated       bool           `json:"truncated"`
	DigestHash      string         `json:"digest_hash"`
	ComputedAt      time.Time      `json:"computed_at"`
}

// CheckState is the validation snapshot inside meta.checks.
type CheckState struct {
	CheckedAt  time.Time   `json:"checked_at"`
	Violations []Violation `json:"violations"`
	// Truncated marks that violations were capped at 200 (SPEC-09 §4.3).
	Truncated bool `json:"truncated,omitempty"`
}

// ReportMeta is the weekly_reports.meta JSON document (SPEC-09 §4.3).
type ReportMeta struct {
	Generation    *GenerationMeta `json:"generation,omitempty"`
	Coverage      *Coverage       `json:"coverage,omitempty"`
	Checks        *CheckState     `json:"checks,omitempty"`
	ConfirmedHash string          `json:"confirmed_hash,omitempty"`
	Refs          []EntityRef     `json:"refs,omitempty"`
}

// WeeklyReport is one weekly_reports row plus its derived content hash.
type WeeklyReport struct {
	ID          string
	Period      Period
	Title       string
	ContentMD   string
	ContentHash string
	Status      Status
	Meta        ReportMeta
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ConfirmedAt *time.Time
	DeletedAt   *time.Time
}

// ReportSummary is the list projection (SPEC-09 §5.3 ListReports).
type ReportSummary struct {
	ID          string
	Title       string
	Period      Period
	Status      Status
	UpdatedAt   time.Time
	ConfirmedAt *time.Time
	DeletedAt   *time.Time
	Excerpt     string
}

// ReportPage is one page of report summaries.
type ReportPage struct {
	Items    []ReportSummary
	Total    int
	Page     int
	PageSize int
}

// DigestItem is one normalized activity entry (SPEC-09 §4.4).
type DigestItem struct {
	Ref               EntityRef
	Kind              string
	State             string
	Summary           string
	Detail            string
	Old               string
	New               string
	Actor             string
	FirstAt           time.Time
	LastAt            time.Time
	EventIDs          []string
	UnconfirmedSource bool
	Broken            bool
}

// DigestSection is one rendered digest section.
type DigestSection struct {
	Key   SectionKey
	Title string
	Items []DigestItem
}

// BlockedTask is one currently blocked task (SPEC-09 §4.4).
type BlockedTask struct {
	Ref        EntityRef
	Title      string
	ColumnName string
	IsDone     bool
	Reason     string
}

// PlannedTask is one next-week candidate (SPEC-09 §4.4).
type PlannedTask struct {
	Ref        EntityRef
	Title      string
	ColumnName string
	IsDone     bool
	Deadline   *time.Time
	WeekPlan   *string
	Origin     string
}

// DigestStats aggregates digest counts.
type DigestStats struct {
	CountsByType    map[string]int
	CountsBySection map[SectionKey]int
	CountsByActor   map[string]int
}

// ActivityDigest is the deterministic activity summary of one period. It is
// never persisted; only meta.coverage and meta.refs are stored.
type ActivityDigest struct {
	Period      Period
	Timezone    string
	CollectedAt time.Time
	Sections    []DigestSection
	Blocked     []BlockedTask
	Planned     []PlannedTask
	Stats       DigestStats
	Truncated   bool
	Warnings    []string
	DigestHash  string
	// Housekeeping holds filing actions (created / imported / deleted,
	// exports, self-generated reports). They are not progress: they never
	// reach a body section or the prompt, and only keep their refs known to
	// the validator.
	Housekeeping []DigestItem
}

// EntityBrief is the read-only entity summary used by the digest.
type EntityBrief struct {
	Title                string
	NoteKind             string
	AIContentUnconfirmed bool
	Broken               bool
}

// TemplateSection is one default-7 section definition.
type TemplateSection struct {
	Key   SectionKey
	Title string
	Num   int
}

// Template is the fixed report structure.
type Template struct {
	ID       string
	Version  int
	Sections []TemplateSection
}

// CreateReportInput creates one draft report (SPEC-09 §5.3).
type CreateReportInput struct {
	Period   Period
	Title    string
	Skeleton bool
	Force    bool
	Actor    Actor
}

// ListReportsInput filters report history.
type ListReportsInput struct {
	Status         []Status
	From           *time.Time
	To             *time.Time
	IncludeDeleted bool
	Page           int
	PageSize       int
	Order          string
}

// CollectInput configures a digest query.
type CollectInput struct {
	Period    Period
	Timezone  *time.Location
	MaxEvents int
}

// GenerateInput regenerates one report draft.
type GenerateInput struct {
	ReportID string
	Replace  bool
	Actor    Actor
	// Progress reports each section as it is generated. Nil is fine.
	Progress SectionProgress
}

// SectionProgress reports one finished section generation.
type SectionProgress func(done, total int, section SectionKey)

// GeneratePeriodInput is the combined "generate this week" entry.
type GeneratePeriodInput struct {
	Period    Period
	IncludeAI bool
	Replace   bool
	Actor     Actor
	// Progress reports each section as it is generated. Nil is fine.
	Progress SectionProgress
}

// GenerateOutcome carries the report, digest and validation findings.
type GenerateOutcome struct {
	Report     *WeeklyReport
	Digest     *ActivityDigest
	Violations []Violation
}

// UpdateContentInput edits draft content with optimistic concurrency.
type UpdateContentInput struct {
	ReportID          string
	ContentMD         string
	ExpectedHash      string
	ExpectedUpdatedAt *time.Time
	Actor             Actor
}

// ConfirmInput confirms a draft report. Actor must be user.
type ConfirmInput struct {
	ReportID        string
	Actor           Actor
	AckFingerprints []string
	// AckNotes optionally attaches a verification note to an acknowledged
	// fingerprint (SPEC-09 §4.3 ack.note).
	AckNotes map[string]string
}

// RevertInput returns a confirmed report to draft.
type RevertInput struct {
	ReportID string
	Actor    Actor
}

// ExportInput renders one report as Markdown or HTML.
type ExportInput struct {
	ReportID    string
	Format      string
	Dir         string
	Overwrite   bool
	FrontMatter *bool
	Actor       Actor
}

// ExportResult reports a finished export.
type ExportResult struct {
	Path   string
	Format string
	Bytes  int64
	Status Status
}
