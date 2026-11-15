package weeklyreview

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"researchos/internal/ai/output"
	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/settings"
	"researchos/internal/foundation/trash"
	"researchos/internal/foundation/workspace"
)

// Limits and defaults of SPEC-09.
const (
	maxContentBytes   = 512 << 10
	defaultMaxEvents  = 2000
	maxSectionItems   = 80
	maxSummaryRunes   = 300
	maxBriefRunes     = 400
	maxRefs           = 500
	maxViolations     = 200
	defaultPageSize   = 20
	maxPageSize       = 100
	defaultReportTmpl = "default-7"
	reportTemplateVer = 1
	promptVersion     = "weekly_report/v3"
	taskKind          = "weekly_report"

	settingWeekStart     = "weekly.week_start_day"
	settingTimezone      = "weekly.timezone"
	settingDefaultPeriod = "weekly.default_period"
	settingMaxEvents     = "weekly.max_digest_events"
	settingExportDir     = "export.dir"
)

// Options wires the weekly review service to its collaborators.
//
// AI and Outputs are the SPEC-02 generation/output services bound to the same
// project database. When AI is nil the module degrades to deterministic
// skeleton generation (IncludeAI=false paths stay fully usable).
type Options struct {
	// Clock supplies timestamps and UUIDv7 prefixes. Nil means clock.System.
	Clock clock.Clock
	// AI is the SPEC-02 generation entry (weekly_report task kind).
	AI output.GenerationService
	// Outputs is the SPEC-02 AI output service (ConfirmTx on confirm).
	Outputs output.AIOutputService
	// Trash performs soft delete/restore and writes the matching events.
	Trash *trash.Service
	// Settings is the workspace-level settings store. Nil resolves
	// settings.NewWorkspace from the workspace global database.
	Settings *settings.Store
	// Logger receives non-fatal warnings.
	Logger workspace.Logger
}

// Service is the SPEC-09 service surface shared by the UI and MCP.
type Service interface {
	DefaultPeriod(ctx context.Context) (Period, error)
	CreateReport(ctx context.Context, in CreateReportInput) (*WeeklyReport, error)
	ListReports(ctx context.Context, in ListReportsInput) (ReportPage, error)
	GetReport(ctx context.Context, id string) (*WeeklyReport, error)
	CollectActivities(ctx context.Context, in CollectInput) (*ActivityDigest, error)
	GenerateDraft(ctx context.Context, in GenerateInput) (*GenerateOutcome, error)
	GenerateForPeriod(ctx context.Context, in GeneratePeriodInput) (*GenerateOutcome, error)
	// RegenerateSection rewrites one section with temporary instructions
	// (see revision.go); the rest of the draft stays untouched.
	RegenerateSection(ctx context.Context, in SectionRevisionInput) (*SectionRevisionOutcome, error)
	UpdateContent(ctx context.Context, in UpdateContentInput) (*WeeklyReport, error)
	Confirm(ctx context.Context, in ConfirmInput) (*WeeklyReport, error)
	RevertToDraft(ctx context.Context, in RevertInput) (*WeeklyReport, error)
	DeleteReport(ctx context.Context, id string, actor Actor) error
	RestoreReport(ctx context.Context, id string, actor Actor) error
	Export(ctx context.Context, in ExportInput) (*ExportResult, error)

	// Skills are the workspace-level writing instruction sets behind report
	// generation (see skills.go).
	Skills(ctx context.Context) (SkillSet, error)
	SaveSkill(ctx context.Context, in SkillInput) (SkillSet, error)
	DeleteSkill(ctx context.Context, id string) (SkillSet, error)
	SetDefaultSkill(ctx context.Context, id string) (SkillSet, error)
}

type service struct {
	ws        *workspace.Workspace
	projectID string
	clock     clock.Clock
	ai        output.GenerationService
	outputs   output.AIOutputService
	trash     *trash.Service
	settings  *settings.Store
	logger    workspace.Logger
}

var _ Service = (*service)(nil)

// New binds the service to one project of ws.
func New(ws *workspace.Workspace, projectID string, opt Options) Service {
	c := opt.Clock
	if c == nil {
		c = clock.System{}
	}
	s := &service{
		ws:        ws,
		projectID: projectID,
		clock:     c,
		ai:        opt.AI,
		outputs:   opt.Outputs,
		trash:     opt.Trash,
		logger:    opt.Logger,
	}
	s.settings = opt.Settings
	if s.settings == nil && ws != nil && ws.Global() != nil {
		s.settings = settings.NewWorkspace(ws.Global().DB()).WithClock(c)
	}
	return s
}

// DepsFromWorkspace builds the collaborator set for one project handle,
// mirroring the wiring used by the other module packages.
func DepsFromWorkspace(ws *workspace.Workspace, handle *workspace.ProjectHandle) Options {
	c := ws.Clock()
	return Options{
		Clock:    c,
		Trash:    trash.New(handle.ID, handle.DB(), ws.Paths(), handle.Events(), nil, c),
		Settings: settings.NewWorkspace(ws.Global().DB()).WithClock(c),
	}
}

func (s *service) now() string { return clock.Format(s.clock.Now()) }

func (s *service) handle(ctx context.Context) (*workspace.ProjectHandle, error) {
	if s.ws == nil {
		return nil, errors.New("weeklyreview: service has no workspace")
	}
	return s.ws.Project(ctx, s.projectID)
}

// requireWritable rejects writes on an archived project (SPEC-09 §4.5).
func (s *service) requireWritable(ctx context.Context) error {
	if s.ws == nil || s.ws.Global() == nil {
		return errors.New("weeklyreview: service has no global database")
	}
	var status string
	err := s.ws.Global().DB().QueryRowContext(ctx,
		`SELECT status FROM projects WHERE id = ?`, s.projectID).Scan(&status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return serviceError(ErrProjectNotFound, "项目 %s 不存在", s.projectID)
	case err != nil:
		return fmt.Errorf("weeklyreview: read project status: %w", err)
	case schema.ProjectStatus(status) == schema.ProjectArchived:
		return serviceError(ErrProjectArchived, "项目已归档，周报为只读")
	default:
		return nil
	}
}

// projectInfo is the read-only project metadata fed to prompts and titles.
type projectInfo struct {
	ID      string
	Name    string
	Summary string
	Goals   string
}

func (s *service) projectInfo(ctx context.Context) (projectInfo, error) {
	var p projectInfo
	p.ID = s.projectID
	err := s.ws.Global().DB().QueryRowContext(ctx,
		`SELECT name, summary, goals FROM projects WHERE id = ?`, s.projectID).
		Scan(&p.Name, &p.Summary, &p.Goals)
	if errors.Is(err, sql.ErrNoRows) {
		return p, serviceError(ErrProjectNotFound, "项目 %s 不存在", s.projectID)
	}
	if err != nil {
		return p, fmt.Errorf("weeklyreview: read project: %w", err)
	}
	return p, nil
}

// weeklySettings is the resolved weekly.* configuration.
type weeklySettings struct {
	WeekStartDay  int
	Location      *time.Location
	DefaultPeriod string
	MaxEvents     int
}

func (s *service) loadSettings(ctx context.Context) weeklySettings {
	cfg := weeklySettings{
		WeekStartDay:  1,
		Location:      time.Local,
		DefaultPeriod: "current",
		MaxEvents:     defaultMaxEvents,
	}
	if s.settings == nil {
		return cfg
	}
	if n, ok := s.intSetting(ctx, settingWeekStart); ok && n >= 0 && n <= 6 {
		cfg.WeekStartDay = n
	}
	if raw, ok, err := s.settings.Get(ctx, settingTimezone); err == nil && ok {
		name := unquoteSetting(raw)
		if loc, err := time.LoadLocation(name); err == nil {
			cfg.Location = loc
		}
	}
	if raw, ok, err := s.settings.Get(ctx, settingDefaultPeriod); err == nil && ok {
		switch unquoteSetting(raw) {
		case "previous":
			cfg.DefaultPeriod = "previous"
		case "current":
			cfg.DefaultPeriod = "current"
		}
	}
	if n, ok := s.intSetting(ctx, settingMaxEvents); ok && n > 0 {
		cfg.MaxEvents = n
	}
	return cfg
}

func (s *service) intSetting(ctx context.Context, key string) (int, bool) {
	if s.settings == nil {
		return 0, false
	}
	raw, ok, err := s.settings.Get(ctx, key)
	if err != nil || !ok {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(unquoteSetting(raw)))
	if err != nil {
		return 0, false
	}
	return n, true
}

func unquoteSetting(raw string) string {
	var s string
	if err := json.Unmarshal([]byte(raw), &s); err == nil {
		return s
	}
	return strings.TrimSpace(raw)
}

// DefaultPeriod returns the configured current or previous report week
// (SPEC-09 §5.3 DefaultPeriod).
func (s *service) DefaultPeriod(ctx context.Context) (Period, error) {
	cfg := s.loadSettings(ctx)
	loc := cfg.Location
	now := s.clock.Now().In(loc)
	start := weekStart(now, cfg.WeekStartDay, loc)
	if cfg.DefaultPeriod == "previous" {
		start = start.AddDate(0, 0, -7)
	}
	return Period{Start: start, EndExclusive: start.AddDate(0, 0, 7)}, nil
}

// weekStart returns midnight of the most recent week_start_day on or before t.
func weekStart(t time.Time, startDay int, loc *time.Location) time.Time {
	t = t.In(loc)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	delta := (int(day.Weekday()) - startDay + 7) % 7
	return day.AddDate(0, 0, -delta)
}

func validatePeriod(p Period, loc *time.Location) error {
	if p.Empty() || !p.EndExclusive.After(p.Start) {
		return serviceError(ErrInvalidPeriod, "周期必须为非空区间")
	}
	start := p.Start.In(loc)
	if start.Hour() != 0 || start.Minute() != 0 || start.Second() != 0 || start.Nanosecond() != 0 {
		return serviceError(ErrInvalidPeriod, "周期起点必须为当日 00:00")
	}
	end := p.EndExclusive.In(loc)
	if end.Hour() != 0 || end.Minute() != 0 || end.Second() != 0 || end.Nanosecond() != 0 {
		return serviceError(ErrInvalidPeriod, "周期终点必须为次日 00:00")
	}
	if end.Before(start.AddDate(0, 0, 1)) {
		return serviceError(ErrInvalidPeriod, "周期至少一天")
	}
	if end.After(start.AddDate(0, 0, 367)) {
		return serviceError(ErrInvalidPeriod, "周期最长 366 天")
	}
	return nil
}

func periodFromDates(start, end string, loc *time.Location) (Period, error) {
	from, err := time.ParseInLocation(dateLayout, start, loc)
	if err != nil {
		return Period{}, serviceError(ErrInvalidPeriod, "周期起点无效：%s", start)
	}
	to, err := time.ParseInLocation(dateLayout, end, loc)
	if err != nil {
		return Period{}, serviceError(ErrInvalidPeriod, "周期终点无效：%s", end)
	}
	return Period{Start: from, EndExclusive: to.AddDate(0, 0, 1)}, nil
}

func (s *service) newID() (string, error) {
	id, err := ids.New(s.clock)
	if err != nil {
		return "", fmt.Errorf("weeklyreview: id: %w", err)
	}
	return id, nil
}

// writeActor maps the caller actor to the event actor. An empty actor means
// the in-app user; ai is never accepted for report writes.
func writeActor(a Actor) (events.Actor, error) {
	switch a {
	case "", ActorUser:
		return schema.ActorUser, nil
	case ActorMCP:
		return schema.ActorMCP, nil
	case ActorAI:
		return "", serviceError(ErrInvalidActor, "ai 不能直接写周报")
	default:
		return "", serviceError(ErrInvalidActor, "未知操作者 %q", string(a))
	}
}

// contentHash returns the lowercase hex sha256 of content.
func contentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// digestHash returns "sha256:" + hex of canonical bytes.
func digestHash(canonical any) (string, error) {
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("weeklyreview: digest hash: %w", err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (s *service) defaultTitle(p projectInfo, period Period) string {
	return fmt.Sprintf("%s 周报（%s – %s）", p.Name, period.StartDate(), period.EndDate())
}
