// Package dashboard implements the SPEC-03 §5.11 global Dashboard service: the
// project card grid, the per-project statistics, the workspace summary and the
// cross-project activity feed the UI renders before a project is opened.
//
// The package is read-only by design. Listing, loading and switching are
// navigation, so they never write an Activity (§5.11). The only persisted side
// effect belongs to project.Service.Open, which stores the browse-state key
// app.last_project_id and is documented there.
//
// Load never fails because one project is broken: its card is reported in
// Snapshot.Degraded with zero statistics and the UI renders "—" (§5.11).
package dashboard

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"researchos/internal/foundation/clock"
	foundationworkspace "researchos/internal/foundation/workspace"
	"researchos/internal/project"
	wsservice "researchos/internal/workspace"
)

const (
	// statsWorkers caps the parallel per-project statistics reads (§5.11).
	statsWorkers = 8
	// statsBudget is the per-project statistics deadline. A project that misses
	// it is degraded; the page still renders.
	statsBudget = 200 * time.Millisecond
	// defaultCacheTTL is how long a whole snapshot answers Load (§5.11).
	defaultCacheTTL = 60 * time.Second

	// activityDefaultLimit, activityMaxLimit, activityPerProject and
	// activityMaxProjects are the merged feed bounds of §5.11.
	activityDefaultLimit = 20
	activityMaxLimit     = 100
	activityPerProject   = 50
	activityMaxProjects  = 100
)

// errNoProject reports a service built without a project service. It is a
// wiring error, never a data condition.
var errNoProject = errors.New("dashboard: service has no project service")

// Snapshot is one Dashboard read. Load returns an independent copy: mutating the
// slices or timestamps of a returned snapshot never changes the cache.
type Snapshot struct {
	Projects         []ProjectCard
	CurrentProjectID string
	Workspace        wsservice.Info
	RecentActivity   []project.ActivityItem
	Totals           Totals
	// Degraded lists the project ids whose statistics could not be read.
	Degraded    []string
	GeneratedAt time.Time
}

// ProjectCard is one project tile: the registration row plus its statistics.
// LastActivityAt mirrors Stats.LastActivityAt and is nil when the project has
// no activities.
type ProjectCard struct {
	Project        project.Project
	Stats          project.Stats
	LastActivityAt *time.Time
	Archived       bool
}

// Totals aggregates the dashboard counters. Active and Archived count every
// project; Notes, Papers and OpenTasks sum the statistics that could be read.
type Totals struct {
	Active    int
	Archived  int
	Notes     int
	Papers    int
	OpenTasks int
}

// Options configures a Service. Project is the only required dependency; the
// others fall back to the workspace or to a safe default.
type Options struct {
	// Workspace is the open SPEC-01 workspace. It supplies the default clock
	// and, when WorkspaceService is nil, the workspace summary service.
	Workspace *foundationworkspace.Workspace
	// Project answers the project list, the per-project statistics and Open.
	Project *project.Service
	// WorkspaceService answers the workspace summary. Nil derives one from
	// Workspace; when both are nil the summary is left zero.
	WorkspaceService *wsservice.Service
	// Clock is the only time source: it stamps GeneratedAt, bounds the activity
	// window and dates the cache. Nil uses the workspace clock, then
	// clock.System.
	Clock clock.Clock
	// CacheTTL is how long one snapshot answers Load. Zero or negative uses
	// 60s.
	CacheTTL time.Duration
}

// Service is the Dashboard aggregation. It is safe for concurrent use: the
// snapshot cache is guarded by mu, while the per-project reads run without
// holding it.
type Service struct {
	project   *project.Service
	workspace *wsservice.Service
	clock     clock.Clock
	cacheTTL  time.Duration

	mu      sync.Mutex
	gen     uint64
	cached  *Snapshot
	expires time.Time
}

// New builds a Service from opts.
func New(opts Options) *Service {
	s := &Service{
		project:   opts.Project,
		workspace: opts.WorkspaceService,
		clock:     opts.Clock,
		cacheTTL:  opts.CacheTTL,
	}
	if s.workspace == nil && opts.Workspace != nil {
		s.workspace = wsservice.New(opts.Workspace)
	}
	if s.clock == nil && opts.Workspace != nil {
		s.clock = opts.Workspace.Clock()
	}
	if s.clock == nil {
		s.clock = clock.System{}
	}
	if s.cacheTTL <= 0 {
		s.cacheTTL = defaultCacheTTL
	}
	return s
}

// Load builds the whole Dashboard read model, or returns the cached snapshot
// while it is fresh (§5.11). Only a failing project list is fatal: a statistics
// timeout or a workspace summary failure degrades that part of the page.
func (d *Service) Load(ctx context.Context) (*Snapshot, error) {
	if d == nil || d.project == nil {
		return nil, errNoProject
	}
	// A caller that already walked away gets its context error even when the
	// cache could answer.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	gen, cached := d.cachedSnapshot()
	if cached != nil {
		return cached, nil
	}

	snap, err := d.build(ctx)
	if err != nil {
		return nil, err
	}
	d.storeSnapshot(gen, snap)
	return cloneSnapshot(snap), nil
}

// build reads everything once. It is called without holding mu so that a slow
// project never blocks Invalidate.
func (d *Service) build(ctx context.Context) (*Snapshot, error) {
	projects, err := d.project.List(ctx, project.ListQuery{IncludeArchived: true})
	if err != nil {
		return nil, err
	}

	cards := make([]ProjectCard, len(projects))
	healthy := make([]bool, len(projects))
	parallelEach(len(projects), statsWorkers, func(i int) {
		if ctx.Err() != nil {
			return
		}
		cards[i], healthy[i] = d.card(ctx, projects[i])
	})

	var degraded []string
	for i, ok := range healthy {
		if !ok {
			degraded = append(degraded, cards[i].Project.ID)
		}
	}
	sort.Strings(degraded)

	snap := &Snapshot{
		Projects:         cards,
		CurrentProjectID: d.currentProjectID(),
		Totals:           totalsOf(cards),
		Degraded:         degraded,
		GeneratedAt:      d.clock.Now(),
	}
	// The workspace summary is presentation only; a failure leaves it zero
	// instead of blanking the page (§5.11).
	if d.workspace != nil {
		if info, infoErr := d.workspace.Info(ctx); infoErr == nil && info != nil {
			snap.Workspace = *info
		}
	}
	// The card list is already loaded, so the feed is merged from it instead of
	// listing the projects a second time.
	snap.RecentActivity = d.mergeActivity(ctx, projects, activityDefaultLimit)
	return snap, nil
}

// card reads one project's statistics under the per-project budget. A failed
// read reports zero statistics so the caller can render "—".
func (d *Service) card(ctx context.Context, p project.Project) (ProjectCard, bool) {
	card := ProjectCard{Project: p, Archived: p.Status == project.StatusArchived}
	sctx, cancel := context.WithTimeout(ctx, statsBudget)
	defer cancel()
	stats, err := d.project.Stats(sctx, p.ID)
	if err != nil {
		return card, false
	}
	card.Stats = stats
	if stats.LastActivityAt != nil {
		at := *stats.LastActivityAt
		card.LastActivityAt = &at
	}
	return card, true
}

// RefreshCard recomputes one card and grafts it onto the cached snapshot when
// one is present (§5.11). Without a cached snapshot the card is still returned;
// the next Load rebuilds the whole page.
//
// An unknown project yields project.ErrNotFound. A statistics failure keeps the
// card with zero statistics and marks the project degraded, mirroring Load.
func (d *Service) RefreshCard(ctx context.Context, projectID string) (ProjectCard, error) {
	if d == nil || d.project == nil {
		return ProjectCard{}, errNoProject
	}
	p, err := d.project.Get(ctx, projectID)
	if err != nil {
		return ProjectCard{}, err
	}
	card, healthy := d.card(ctx, *p)
	d.updateCachedCard(card, healthy)
	return card, nil
}

// SwitchProject opens projectID and returns the session (§5.11). Archived
// projects are allowed: they open read-only. Switching is navigation, so it
// writes no Activity; it does invalidate the cached snapshot because
// CurrentProjectID changed.
func (d *Service) SwitchProject(ctx context.Context, projectID string) (*project.Session, error) {
	if d == nil || d.project == nil {
		return nil, errNoProject
	}
	// Even a failed open may have closed the previous session, so the cached
	// CurrentProjectID is not trustworthy afterwards.
	defer d.Invalidate(projectID)
	return d.project.Open(ctx, projectID)
}

// RecentActivity merges the newest activities of every project (active and
// archived) into one feed ordered by CreatedAt DESC with the id as tie-break
// (§5.11). limit defaults to 20 and is capped at 100. Duplicate ids are merged
// once. It never writes an event.
//
// A project whose timeline cannot be read is skipped: one broken database must
// not empty the dashboard feed.
func (d *Service) RecentActivity(ctx context.Context, limit int) ([]project.ActivityItem, error) {
	if d == nil || d.project == nil {
		return nil, errNoProject
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	projects, err := d.project.List(ctx, project.ListQuery{IncludeArchived: true})
	if err != nil {
		return nil, err
	}
	return d.mergeActivity(ctx, projects, limit), nil
}

// mergeActivity reads up to activityPerProject events from each project and
// merges them. Only the activityMaxProjects most recently created projects are
// queried (§5.11).
func (d *Service) mergeActivity(ctx context.Context, projects []project.Project, limit int) []project.ActivityItem {
	limit = clampActivityLimit(limit)
	if len(projects) == 0 {
		return nil
	}

	candidates := projects
	if len(candidates) > activityMaxProjects {
		candidates = append([]project.Project(nil), candidates...)
		sort.SliceStable(candidates, func(i, j int) bool {
			if !candidates[i].CreatedAt.Equal(candidates[j].CreatedAt) {
				return candidates[i].CreatedAt.After(candidates[j].CreatedAt)
			}
			return candidates[i].ID > candidates[j].ID
		})
		candidates = candidates[:activityMaxProjects]
	}

	// An explicit window keeps Activity away from its 30-day default: the feed
	// shows the newest events whenever they happened (§5.11).
	to := d.clock.Now()
	from := time.Unix(0, 0).UTC()
	batches := make([][]project.ActivityItem, len(candidates))
	parallelEach(len(candidates), statsWorkers, func(i int) {
		if ctx.Err() != nil {
			return
		}
		page, err := d.project.Activity(ctx, candidates[i].ID, project.ActivityQuery{
			From:  &from,
			To:    &to,
			Limit: activityPerProject,
		})
		if err != nil || page == nil {
			return
		}
		batches[i] = page.Items
	})

	seen := make(map[string]struct{}, len(candidates)*activityPerProject)
	all := make([]project.ActivityItem, 0, len(candidates)*activityPerProject)
	for _, batch := range batches {
		for _, item := range batch {
			if _, dup := seen[item.ID]; dup {
				continue
			}
			seen[item.ID] = struct{}{}
			all = append(all, item)
		}
	}

	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ID > all[j].ID
	})
	if len(all) > limit {
		all = all[:limit]
	}
	return all
}

// totalsOf sums the counters. Degraded cards carry zero statistics, which keeps
// the sums equal to "the ones that could be read" (§5.11).
func totalsOf(cards []ProjectCard) Totals {
	var t Totals
	for _, c := range cards {
		if c.Archived {
			t.Archived++
		} else {
			t.Active++
		}
		t.Notes += c.Stats.NoteCount
		t.Papers += c.Stats.PaperCount
		t.OpenTasks += c.Stats.TaskOpen
	}
	return t
}

// currentProjectID returns the id of the open session, or "" when none is open.
func (d *Service) currentProjectID() string {
	sess := d.project.Current()
	if sess == nil {
		return ""
	}
	return sess.Project.ID
}

// clampActivityLimit applies the §5.11 default and cap.
func clampActivityLimit(limit int) int {
	if limit <= 0 {
		return activityDefaultLimit
	}
	if limit > activityMaxLimit {
		return activityMaxLimit
	}
	return limit
}

// parallelEach calls fn for every index with at most limit goroutines. Every
// call writes a distinct index of the caller's result slice, so no lock is
// needed.
func parallelEach(n, limit int, fn func(i int)) {
	if n <= 0 {
		return
	}
	if limit <= 0 {
		limit = 1
	}
	if limit > n {
		limit = n
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := range n {
		sem <- struct{}{}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}(i)
	}
	wg.Wait()
}
