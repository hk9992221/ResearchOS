package overview

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"researchos/internal/ports"
)

func TestLoadServesTheCacheUntilInvalidate(t *testing.T) {
	e := newEnv(t, nil)
	e.idea("第一条研究中的想法", "Researching", -time.Hour, false)
	first := e.load()
	if len(first.ActiveIdeas) != 1 {
		t.Fatalf("ActiveIdeas = %+v", first.ActiveIdeas)
	}

	e.idea("第二条研究中的想法", "Researching", -30*time.Minute, false)
	second := e.load()
	if len(second.ActiveIdeas) != 1 {
		t.Fatalf("a fresh snapshot must answer from the cache, got %d ideas", len(second.ActiveIdeas))
	}
	if second != first {
		t.Fatal("the cache must hand back the same snapshot pointer")
	}
	if got := e.state(second, WidgetActiveIdeas); got.Degraded {
		t.Fatalf("cached read reported degraded: %+v", got)
	}

	e.svc.Invalidate(e.pid)
	third := e.load()
	if len(third.ActiveIdeas) != 2 {
		t.Fatalf("after Invalidate ActiveIdeas = %d, want 2", len(third.ActiveIdeas))
	}
	// Invalidate is idempotent and safe to repeat.
	e.svc.Invalidate(e.pid)
	e.svc.Invalidate("unknown-project")
	if len(e.load().ActiveIdeas) != 2 {
		t.Fatal("repeated Invalidate broke the cache")
	}
}

func TestCacheExpiresAfterTTL(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.CacheTTL = 50 * time.Millisecond })
	e.idea("第一条", "Researching", -time.Hour, false)
	e.load()
	e.idea("第二条", "Validating", -30*time.Minute, false)

	// The fixture clock only moves when the test advances it.
	e.clock.Advance(60 * time.Millisecond)
	if got := len(e.load().ActiveIdeas); got != 2 {
		t.Fatalf("ActiveIdeas = %d, want the expired cache to reload", got)
	}
}

func TestRefreshReloadsOnlyOneWidget(t *testing.T) {
	e := newEnv(t, nil)
	e.idea("旧想法", "Researching", -time.Hour, false)
	e.note("旧笔记", "scratchpad", -time.Hour, false)
	before := e.load()
	if len(before.ActiveIdeas) != 1 || len(before.RecentNotes) != 1 {
		t.Fatalf("before = %+v / %+v", before.ActiveIdeas, before.RecentNotes)
	}

	e.idea("新想法", "Validating", -45*time.Minute, false)
	e.note("新笔记", "scratchpad", -45*time.Minute, false)

	e.clock.Advance(time.Millisecond)
	snap, err := e.svc.Refresh(context.Background(), e.pid, WidgetActiveIdeas)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(snap.ActiveIdeas) != 2 {
		t.Fatalf("Refresh must pick up the new idea, got %+v", snap.ActiveIdeas)
	}
	if len(snap.RecentNotes) != 1 {
		t.Fatalf("Refresh must not reload other widgets, got notes %+v", snap.RecentNotes)
	}
	if snap.GeneratedAt.Equal(before.GeneratedAt) {
		t.Fatal("GeneratedAt must move on a refresh")
	}
	// The refreshed snapshot is cached, so a plain Load agrees with it.
	if got := e.load(); len(got.ActiveIdeas) != 2 || len(got.RecentNotes) != 1 {
		t.Fatalf("cached snapshot after Refresh = %+v / %+v", got.ActiveIdeas, got.RecentNotes)
	}
}

func TestRefreshWithoutCacheDoesAFullLoad(t *testing.T) {
	e := newEnv(t, nil)
	e.idea("唯一的想法", "Researching", -time.Hour, false)
	snap, err := e.svc.Refresh(context.Background(), e.pid, WidgetWeeklyProgress)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(snap.ActiveIdeas) != 1 {
		t.Fatalf("ActiveIdeas = %+v, want a full snapshot", snap.ActiveIdeas)
	}
	for _, st := range snap.Widgets {
		if st.Degraded && st.ID != WidgetLiterature && st.ID != WidgetRecentExperiments {
			t.Fatalf("%s degraded on a healthy full load: %+v", st.ID, st)
		}
	}
}

func TestRefreshRejectsUnknownWidget(t *testing.T) {
	e := newEnv(t, nil)
	e.load()
	if _, err := e.svc.Refresh(context.Background(), e.pid, WidgetID("nope")); err == nil {
		t.Fatal("Refresh with an unknown widget = nil error")
	} else if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error = %v, want it to name the widget", err)
	}
}

func TestRefreshReportsAMissingProject(t *testing.T) {
	e := newEnv(t, nil)
	e.load()
	e.execGlobal(`DELETE FROM projects WHERE id = ?`, e.pid)
	if _, err := e.svc.Refresh(context.Background(), e.pid, WidgetMyTasks); !isNotFound(err) {
		t.Fatalf("Refresh = %v, want ErrNotFound", err)
	}
}

// hangBudget is the per-widget budget of the two hanging-widget tests. It is
// deliberately generous: every widget shares the one-connection project pool, so
// a tight budget would make the suite depend on machine load instead of on the
// behaviour under test.
const hangBudget = 2 * time.Second

func TestSlowWidgetDegradesOnlyItself(t *testing.T) {
	release := make(chan struct{})
	e := newEnv(t, func(o *Options) { o.WidgetTimeout = hangBudget })
	e.idea("健康的组件不受影响", "Researching", -time.Hour, false)
	e.note("健康的笔记", "scratchpad", -time.Hour, false)
	e.svc.beforeWidget = func(w WidgetID) {
		if w == WidgetRecentNotes {
			<-release
		}
	}
	snap, err := e.svc.Load(context.Background(), e.pid)
	if err != nil {
		t.Fatalf("Load = %v, want a snapshot with one degraded widget", err)
	}
	close(release)

	st := e.state(snap, WidgetRecentNotes)
	if !st.Degraded || st.Reason != "timeout" {
		t.Fatalf("RecentNotes state = %+v, want a timeout degradation", st)
	}
	if len(snap.RecentNotes) != 0 {
		t.Fatalf("RecentNotes = %+v, want no data on the first load", snap.RecentNotes)
	}
	for _, w := range widgetOrder {
		if w == WidgetRecentNotes {
			continue
		}
		if got := e.state(snap, w); got.Degraded {
			t.Fatalf("%s degraded because a sibling hung: %+v", w, got)
		}
	}
	if len(snap.ActiveIdeas) != 1 {
		t.Fatalf("ActiveIdeas = %+v", snap.ActiveIdeas)
	}
}

func TestTimedOutWidgetKeepsPreviousData(t *testing.T) {
	e := newEnv(t, func(o *Options) {
		o.WidgetTimeout = hangBudget
		o.CacheTTL = 10 * time.Millisecond
	})
	e.note("上一轮的笔记", "scratchpad", -time.Hour, false)
	first := e.load()
	if len(first.RecentNotes) != 1 {
		t.Fatalf("first = %+v", first.RecentNotes)
	}

	// The cache ages out (Invalidate would drop the fallback data as well), so
	// the reload runs and only the hung widget falls back to the old payload.
	e.clock.Advance(time.Second)
	release := make(chan struct{})
	defer close(release)
	e.svc.beforeWidget = func(w WidgetID) {
		if w == WidgetRecentNotes {
			<-release
		}
	}
	second, err := e.svc.Load(context.Background(), e.pid)
	if err != nil {
		t.Fatalf("Load = %v", err)
	}
	if len(second.RecentNotes) != 1 || second.RecentNotes[0].Title != "上一轮的笔记" {
		t.Fatalf("RecentNotes = %+v, want the previous data kept", second.RecentNotes)
	}
	st := e.state(second, WidgetRecentNotes)
	if !st.Degraded || !st.Available || st.Reason != "timeout" {
		t.Fatalf("state = %+v, want degraded-but-visible with the refresh affordance", st)
	}
}

func TestParentContextCancellationCancelsTheLoad(t *testing.T) {
	e := newEnv(t, nil)
	e.idea("想法", "Researching", -time.Hour, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.svc.Load(ctx, e.pid); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load(cancelled ctx) = %v, want the context error", err)
	}

	// A warm cache is an optimisation, not a reason to answer a dead request.
	e.load()
	warm, cancelWarm := context.WithCancel(context.Background())
	cancelWarm()
	if _, err := e.svc.Load(warm, e.pid); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load(cancelled ctx, warm cache) = %v, want the context error", err)
	}
}

// TestRefreshTracksAnArchivedHeader checks that a single-widget refresh still
// refreshes the header projection: archiving a project must make the focus card
// read-only on the next refresh, whichever card asked for it.
func TestRefreshTracksAnArchivedHeader(t *testing.T) {
	e := newEnv(t, nil)
	e.idea("归档前", "Researching", -time.Hour, false)
	before := e.load()
	if !before.Writable || !before.CurrentFocus.FocusEditable {
		t.Fatalf("active snapshot = %+v writable=%v", before.CurrentFocus, before.Writable)
	}

	e.setProjectStatus("archived", true)
	snap, err := e.svc.Refresh(context.Background(), e.pid, WidgetActiveIdeas)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !snap.Project.Archived || snap.Writable {
		t.Fatalf("refreshed header = %+v writable=%v", snap.Project, snap.Writable)
	}
	if snap.CurrentFocus.FocusEditable {
		t.Fatal("FocusEditable must follow the fresh header, not the cached payload")
	}
	if len(snap.ActiveIdeas) != 1 {
		t.Fatalf("ActiveIdeas = %+v, want the refreshed widget", snap.ActiveIdeas)
	}
}

func TestServiceIsConcurrencySafe(t *testing.T) {
	e := newEnv(t, nil)
	e.idea("并发读的想法", "Researching", -time.Hour, false)
	var wg sync.WaitGroup
	for range 4 {
		for i := range 12 {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				ctx := context.Background()
				switch i % 4 {
				case 0:
					_, _ = e.svc.Load(ctx, e.pid)
				case 1:
					_, _ = e.svc.Refresh(ctx, e.pid, widgetOrder[i%len(widgetOrder)])
				case 2:
					e.svc.Invalidate(e.pid)
				case 3:
					_, _ = e.svc.Refresh(ctx, e.pid, WidgetActiveIdeas)
				}
			}(i)
		}
	}
	wg.Wait()
	snap, err := e.svc.Load(context.Background(), e.pid)
	if err != nil {
		t.Fatalf("Load after concurrent use: %v", err)
	}
	if len(snap.Widgets) != len(widgetOrder) {
		t.Fatalf("Widgets = %d entries", len(snap.Widgets))
	}
	for _, st := range snap.Widgets {
		if st.ID == "" {
			t.Fatalf("empty widget id in %+v", snap.Widgets)
		}
	}
	if len(snap.ActiveIdeas) != 1 {
		t.Fatalf("ActiveIdeas = %+v, want the one seeded idea", snap.ActiveIdeas)
	}
}

func TestTruncateRunesAndWeekWindow(t *testing.T) {
	if got := truncateRunes("abc", 10); got != "abc" {
		t.Fatalf("truncateRunes = %q", got)
	}
	if got := truncateRunes("中文混abc", 4); got != "中文混…" {
		t.Fatalf("truncateRunes = %q, want a rune-safe cut", got)
	}
	if got := truncateRunes(strings.Repeat("x", 500), reportExcerptRunes); len([]rune(got)) != reportExcerptRunes {
		t.Fatalf("length = %d", len([]rune(got)))
	}

	// The ISO week of Wednesday 2026-03-04 runs Mon 03-02 through Sun 03-08.
	start, end := weekWindow(testTime)
	if got, want := start.Format(time.DateOnly), "2026-03-02"; got != want {
		t.Fatalf("week start = %s, want %s", got, want)
	}
	if got, want := end.Format(time.DateOnly), "2026-03-09"; got != want {
		t.Fatalf("week end = %s, want %s", got, want)
	}
	// Sunday is the last day of the same week.
	sunday := time.Date(2026, 3, 8, 23, 0, 0, 0, time.UTC)
	if got, _ := weekWindow(sunday); got.Format(time.DateOnly) != "2026-03-02" {
		t.Fatalf("week of Sunday = %s, want 2026-03-02", got.Format(time.DateOnly))
	}
	// Monday 00:00 sharp already belongs to the new week.
	monday := time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)
	if got, _ := weekWindow(monday); got.Format(time.DateOnly) != "2026-03-09" {
		t.Fatalf("week of Monday = %s, want 2026-03-09", got.Format(time.DateOnly))
	}
	// A zone offset shifts the boundary with the wall clock, not the instant.
	zoned := time.Date(2026, 3, 2, 0, 30, 0, 0, time.FixedZone("UTC+8", 8*3600))
	if got, _ := weekWindow(zoned); got.Format(time.DateOnly) != "2026-03-02" {
		t.Fatalf("zoned week = %s", got.Format(time.DateOnly))
	}
}

func TestWidgetOrderAndApplyRoundTrip(t *testing.T) {
	snap := &Snapshot{Widgets: make([]WidgetState, len(widgetOrder))}
	for i, w := range widgetOrder {
		if indexOf(w) != i {
			t.Fatalf("indexOf(%s) = %d, want %d", w, indexOf(w), i)
		}
		if !w.valid() {
			t.Fatalf("%s must be a known widget", w)
		}
	}
	if WidgetID("other").valid() {
		t.Fatal("an unknown widget id must be rejected")
	}
	if got := indexOf(WidgetID("other")); got != -1 {
		t.Fatalf("indexOf(unknown) = %d, want -1", got)
	}

	out := widgetOutcome{
		state:       WidgetState{ID: WidgetActiveIdeas, Available: true},
		ideas:       []IdeaCard{{ID: "i-1", Title: "想法"}},
		literature:  []PaperCard{{ID: "p-1"}},
		myTasks:     &TaskWidget{Items: []ports.TaskBrief{{ID: "t-1"}}},
		blocked:     &BlockedWidget{Items: []ports.TaskBrief{{ID: "t-2"}}},
		weekly:      &WeeklyWidget{Confirmed: true},
		focus:       &FocusWidget{FocusMD: "f"},
		experiments: []ExperimentCard{{ID: "e-1"}},
		notes:       []NoteCard{{ID: "n-1"}},
	}
	for _, w := range widgetOrder {
		applyWidget(snap, w, out)
	}
	if snap.CurrentFocus.FocusMD != "f" || len(snap.ActiveIdeas) != 1 || len(snap.RecentExperiments) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.MyTasks.Items == nil || snap.BlockedTasks.Items == nil || !snap.WeeklyProgress.Confirmed {
		t.Fatalf("snapshot = %+v", snap)
	}
	if len(snap.RecentNotes) != 1 || len(snap.Literature) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}

	// A degraded widget reads its payload back out of the snapshot unchanged.
	back := widgetPayload(snap, WidgetActiveIdeas)
	if len(back.ideas) != 1 || back.ideas[0].ID != "i-1" {
		t.Fatalf("widgetPayload = %+v", back)
	}
	if got := widgetPayload(nil, WidgetActiveIdeas); got.ideas != nil {
		t.Fatalf("widgetPayload(nil) = %+v", got)
	}

	clearWidget(snap, WidgetActiveIdeas)
	if len(snap.ActiveIdeas) != 0 || snap.Widgets[indexOf(WidgetActiveIdeas)].Available {
		t.Fatalf("clearWidget left %+v / %+v", snap.ActiveIdeas, snap.Widgets)
	}
}
