package experiments

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/schema"
)

// TestMetadataExtraction covers the deterministic parts of AC-06 and AC-07.
func TestMetadataExtraction(t *testing.T) {
	svc, _ := newFixture(t)
	ctx := context.Background()
	root := sampleTree(t)
	exp, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "meta", DirPath: root})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	scan, err := svc.Scanning().Run(ctx, TargetRef{Type: TargetExperiment, ID: exp.ID}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	meta := scan.Metadata
	if meta.TotalFiles == 0 || meta.TotalBytes == 0 {
		t.Fatalf("empty metadata: %+v", meta)
	}
	configs := map[string]ExtractedConfig{}
	for _, cfg := range meta.Configs {
		configs[cfg.Path] = cfg
	}
	base, ok := configs["configs/base.yaml"]
	if !ok {
		t.Fatalf("missing YAML config: %+v", configs)
	}
	if base.ParseError != nil {
		t.Fatalf("YAML parse error: %+v", base.ParseError)
	}
	model, _ := base.Values["model"].(map[string]any)
	if model["name"] != "resnet" {
		t.Errorf("model.name = %v", model["name"])
	}
	runJSON := configs["configs/run.json"]
	if runJSON.Values["lambda"] != 0.01 {
		t.Errorf("lambda = %v", runJSON.Values["lambda"])
	}
	tomlCfg := configs["configs/train.toml"]
	train, _ := tomlCfg.Values["train"].(map[string]any)
	if train["batch"] != int64(32) {
		t.Errorf("train.batch = %v (%T)", train["batch"], train["batch"])
	}

	if len(meta.CSVs) != 1 || meta.CSVs[0].Path != "data/samples.csv" {
		t.Fatalf("CSVs = %+v", meta.CSVs)
	}
	if meta.CSVs[0].Columns != 3 || meta.CSVs[0].Rows != 3 {
		t.Errorf("CSV shape = %d columns, %d rows", meta.CSVs[0].Columns, meta.CSVs[0].Rows)
	}
	if len(meta.Readmes) != 1 || meta.Readmes[0].Title != "Demo experiment" {
		t.Errorf("readme = %+v", meta.Readmes)
	}
	if len(meta.Readmes[0].Headings) != 2 {
		t.Errorf("headings = %v", meta.Readmes[0].Headings)
	}
	if len(meta.Dependencies) != 1 || !containsString(meta.Dependencies[0].Dependencies, "numpy==2.0") {
		t.Errorf("dependencies = %+v", meta.Dependencies)
	}
	entries := map[string]bool{}
	for _, candidate := range meta.EntryPoints {
		entries[candidate.Path] = candidate.Heuristic
	}
	if !entries["main.py"] || !entries["train.py"] {
		t.Errorf("entry candidates = %+v", entries)
	}
	if !entries["main.py"] {
		t.Error("entry candidates must be marked heuristic")
	}
}

// TestMetricExtractionAndComparison covers AC-11 and AC-12's deterministic part.
func TestMetricExtractionAndComparison(t *testing.T) {
	svc, f := newFixture(t)
	ctx := context.Background()
	root := sampleTree(t)
	exp, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "metrics", DirPath: root})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	scan, err := svc.Scanning().Run(ctx, TargetRef{Type: TargetExperiment, ID: exp.ID}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	set, err := svc.Metrics().Extract(ctx, scan)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	byKey := map[string]Metric{}
	for _, m := range set.Metrics {
		byKey[m.Key] = m
	}
	accuracy, ok := byKey["eval/accuracy"]
	if !ok {
		t.Fatalf("missing eval/accuracy: %+v", set.Metrics)
	}
	if accuracy.Value == nil || *accuracy.Value != 0.912 {
		t.Errorf("accuracy = %v", accuracy.Value)
	}
	if accuracy.Source.Path != "metrics.json" || accuracy.Source.Pointer == nil || *accuracy.Source.Pointer != "/eval/accuracy" {
		t.Errorf("accuracy source = %+v", accuracy.Source)
	}
	if accuracy.Source.SHA256 == nil || len(*accuracy.Source.SHA256) != 64 {
		t.Errorf("accuracy hash = %v", accuracy.Source.SHA256)
	}
	if byKey["train_loss"].Value == nil || *byKey["train_loss"].Value != 0.25 {
		t.Errorf("train_loss = %+v", byKey["train_loss"])
	}
	acc, ok := byKey["acc"]
	if !ok {
		t.Fatalf("missing CSV acc metric: %+v", byKey)
	}
	if acc.Value == nil || *acc.Value != 0.912 {
		t.Errorf("csv acc = %v", acc.Value)
	}
	if acc.Source.Line == nil || *acc.Source.Line != 4 {
		t.Errorf("csv acc line = %v", acc.Source.Line)
	}
	if acc.Source.Path != "data/samples.csv" {
		t.Errorf("csv acc path = %q", acc.Source.Path)
	}

	runA, err := svc.Runs().Create(ctx, CreateRunInput{ExperimentID: exp.ID, Params: map[string]ParamValue{"lr": {Value: "0.1", Confirmed: true}}})
	if err != nil {
		t.Fatalf("Create run A: %v", err)
	}
	runB, err := svc.Runs().Create(ctx, CreateRunInput{ExperimentID: exp.ID, Params: map[string]ParamValue{"lr": {Value: "0.01", Confirmed: true}}})
	if err != nil {
		t.Fatalf("Create run B: %v", err)
	}
	seedResultsSnapshot(t, svc, f, runA.ID, []Metric{metric("acc", 0.90, "metrics.json")})
	seedResultsSnapshot(t, svc, f, runB.ID, []Metric{metric("acc", 0.99, "metrics.json")})
	comparison, err := svc.Metrics().CompareRuns(ctx, exp.ID)
	if err != nil {
		t.Fatalf("CompareRuns: %v", err)
	}
	if len(comparison.Runs) != 2 {
		t.Fatalf("runs = %+v", comparison.Runs)
	}
	if len(comparison.Rows) != 1 || comparison.Rows[0].Key != "acc" {
		t.Fatalf("rows = %+v", comparison.Rows)
	}
	row := comparison.Rows[0]
	// The baseline is the first listed run; UUIDv7 ids within one millisecond
	// are not ordered, so both directions are valid.
	baseline := comparison.Runs[0].RunID
	other := runA.ID
	if baseline == runA.ID {
		other = runB.ID
	}
	delta, ok := row.Delta[other]
	if !ok || math.Abs(math.Abs(delta)-0.09) > 0.001 {
		t.Errorf("delta = %v (ok=%v)", delta, ok)
	}
	pct, ok := row.Pct[other]
	if !ok || math.Abs(math.Abs(pct)-9.6) > 0.6 {
		t.Errorf("pct = %v (ok=%v)", pct, ok)
	}
	if _, ok := row.Delta[baseline]; ok {
		t.Error("baseline run must not carry a delta")
	}
}

// TestRunLifecycleAndInheritance covers AC-08.
func TestRunLifecycleAndInheritance(t *testing.T) {
	svc, _ := newFixture(t)
	ctx := context.Background()
	expRoot := externalDir(t)
	write(t, filepath.Join(expRoot, "README.md"), "# exp\n")
	exp, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "runs", DirPath: expRoot})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// No run yet: scanning works on the experiment directory.
	if _, err := svc.Scanning().Plan(ctx, TargetRef{Type: TargetExperiment, ID: exp.ID}, nil); err != nil {
		t.Fatalf("Plan without run: %v", err)
	}
	run, err := svc.Runs().Create(ctx, CreateRunInput{
		ExperimentID: exp.ID,
		Params:       map[string]ParamValue{"lr": {Value: "0.01", Confirmed: true}},
		Actor:        schema.ActorUser,
	})
	if err != nil {
		t.Fatalf("Create run: %v", err)
	}
	if run.Status != RunUnknown || run.DirRef != nil {
		t.Errorf("new run = %+v", run)
	}
	plan, err := svc.Scanning().Plan(ctx, TargetRef{Type: TargetRun, ID: run.ID}, nil)
	if err != nil {
		t.Fatalf("Plan run: %v", err)
	}
	if plan.Root != expRoot {
		t.Errorf("inherited root = %q, want %q", plan.Root, expRoot)
	}
	if err := svc.Runs().SetStatus(ctx, run.ID, RunFinished); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if err := svc.Runs().SetStatus(ctx, run.ID, RunFinished); err != nil {
		t.Fatalf("SetStatus idempotent: %v", err)
	}
	detail, err := svc.Runs().Get(ctx, run.ID)
	if err != nil {
		t.Fatalf("Get run: %v", err)
	}
	if detail.Run.Status != RunFinished {
		t.Errorf("status = %q", detail.Run.Status)
	}
	if detail.Run.Params["lr"].Value != "0.01" {
		t.Errorf("params = %+v", detail.Run.Params)
	}
	if err := svc.Runs().SetStatus(ctx, run.ID, RunStatus("bogus")); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("invalid status error = %v", err)
	}

	// A run directory overrides the experiment directory.
	runRoot := externalDir(t)
	write(t, filepath.Join(runRoot, "run.txt"), "x")
	if err := svc.Runs().SetDirectory(ctx, run.ID, runRoot); err != nil {
		t.Fatalf("SetDirectory: %v", err)
	}
	plan, err = svc.Scanning().Plan(ctx, TargetRef{Type: TargetRun, ID: run.ID}, nil)
	if err != nil {
		t.Fatalf("Plan run with dir: %v", err)
	}
	if plan.Root != runRoot {
		t.Errorf("run root = %q, want %q", plan.Root, runRoot)
	}

	// Candidates list subdirectories and never create runs.
	write(t, filepath.Join(expRoot, "variant-a", "README.md"), "# a\n")
	write(t, filepath.Join(expRoot, "__pycache__", "x.pyc"), "cache")
	candidates, err := svc.Runs().ListDirCandidates(ctx, exp.ID)
	if err != nil {
		t.Fatalf("ListDirCandidates: %v", err)
	}
	if len(candidates) != 1 || filepath.Base(candidates[0].Path) != "variant-a" {
		t.Errorf("candidates = %+v", candidates)
	}
	if candidates[0].FileCount != 1 {
		t.Errorf("file count = %d", candidates[0].FileCount)
	}
	runs, err := svc.Runs().List(ctx, exp.ID, false)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, %v", runs, err)
	}

	// Run lifecycle events and soft delete.
	if err := svc.Runs().SoftDelete(ctx, run.ID, schema.ActorUser); err != nil {
		t.Fatalf("SoftDelete run: %v", err)
	}
	if runs, _ := svc.Runs().List(ctx, exp.ID, false); len(runs) != 0 {
		t.Errorf("deleted run still listed: %+v", runs)
	}
	if err := svc.Runs().Restore(ctx, run.ID, schema.ActorUser); err != nil {
		t.Fatalf("Restore run: %v", err)
	}
}

// TestSnapshotHistoryAndDrift covers AC-13 and AC-16 mechanics.
func TestSnapshotHistoryAndDrift(t *testing.T) {
	svc, f := newFixture(t)
	ctx := context.Background()
	root := externalDir(t)
	write(t, filepath.Join(root, "metrics.json"), `{"acc": 0.5}`)
	exp, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "history", DirPath: root})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	run, err := svc.Runs().Create(ctx, CreateRunInput{ExperimentID: exp.ID})
	if err != nil {
		t.Fatalf("Create run: %v", err)
	}
	first := seedResultsSnapshot(t, svc, f, run.ID, []Metric{metric("acc", 0.5, "metrics.json")})
	f.Clock.Advance(time.Minute)
	second := seedResultsSnapshot(t, svc, f, run.ID, []Metric{metric("acc", 0.7, "metrics.json")})
	if first.ID == second.ID {
		t.Fatal("snapshots share an id")
	}
	history, err := svc.Snapshots().List(ctx, TargetRef{Type: TargetRun, ID: run.ID}, SnapshotFilter{})
	if err != nil {
		t.Fatalf("List history: %v", err)
	}
	if len(history) != 2 || history[0].ID != second.ID || history[1].ID != first.ID {
		t.Fatalf("history order = %+v", history)
	}
	if history[0].Kind != AnalysisResults || history[0].MetricCount != 1 {
		t.Errorf("summary = %+v", history[0])
	}
	latest, err := svc.Snapshots().Latest(ctx, TargetRef{Type: TargetRun, ID: run.ID}, AnalysisResults)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.ID != second.ID {
		t.Errorf("latest = %s", latest.ID)
	}
	if _, err := svc.Snapshots().Latest(ctx, TargetRef{Type: TargetRun, ID: run.ID}, AnalysisOutline); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing kind error = %v", err)
	}
	// The first snapshot is immutable.
	loaded, err := svc.Snapshots().Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("Get first: %v", err)
	}
	if loaded.Metrics.Metrics[0].Value == nil || *loaded.Metrics.Metrics[0].Value != 0.5 {
		t.Errorf("first metric = %+v", loaded.Metrics.Metrics[0])
	}
	if loaded.AIAnalysis == nil || loaded.AIAnalysis.ContentMD != "" {
		t.Errorf("ai analysis = %+v", loaded.AIAnalysis)
	}

	// Drift detection uses the frozen manifest.
	scan, err := svc.Scanning().Plan(ctx, TargetRef{Type: TargetRun, ID: run.ID}, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if warnings := detectDrift(ctx, scan.Root, scan.Entries); len(warnings) != 0 {
		t.Errorf("unexpected drift = %v", warnings)
	}
	write(t, filepath.Join(root, "metrics.json"), `{"acc": 0.9, "extra": 1}`)
	warnings := detectDrift(ctx, scan.Root, scan.Entries)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "files_changed_during_analysis:metrics.json") {
		t.Errorf("drift = %v", warnings)
	}

	// Unknown sections are rejected, so a snapshot can never carry them.
	bad := &Snapshot{
		Target:     TargetRef{Type: TargetRun, ID: run.ID},
		AIAnalysis: &AIAnalysis{V: 1, Kind: AnalysisOutline, Sections: []AISection{{Key: "invented"}}},
	}
	if err := f.Project.InTx(ctx, func(tx *sql.Tx) error { return svc.saveSnapshot(ctx, tx, bad) }); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("unknown section error = %v", err)
	}
}

// TestIdeaRelations covers AC-19.
func TestIdeaRelations(t *testing.T) {
	svc, f := newFixture(t)
	ctx := context.Background()
	exp, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "relations"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ideaID := insertIdea(t, f, "Idea one")
	if err := svc.Ideas().LinkIdea(ctx, exp.ID, ideaID, schema.ActorUser); err != nil {
		t.Fatalf("LinkIdea: %v", err)
	}
	if err := svc.Ideas().LinkIdea(ctx, exp.ID, ideaID, schema.ActorUser); err != nil {
		t.Fatalf("LinkIdea twice: %v", err)
	}
	links, err := svc.Ideas().ListIdeas(ctx, exp.ID)
	if err != nil {
		t.Fatalf("ListIdeas: %v", err)
	}
	if len(links) != 1 || links[0].ID != ideaID || links[0].TargetDeleted {
		t.Fatalf("links = %+v", links)
	}
	acts, err := f.Project.Reader().List(ctx, events.Query{EntityID: exp.ID, Limit: 50})
	if err != nil {
		t.Fatalf("activities: %v", err)
	}
	added := 0
	for _, act := range acts {
		if act.Type == schema.EventRelationAdded {
			added++
		}
	}
	if added != 1 {
		t.Errorf("relation.added count = %d, want 1", added)
	}
	if err := svc.Ideas().UnlinkIdea(ctx, exp.ID, ideaID, schema.ActorUser); err != nil {
		t.Fatalf("UnlinkIdea: %v", err)
	}
	if err := svc.Ideas().UnlinkIdea(ctx, exp.ID, ideaID, schema.ActorUser); err != nil {
		t.Fatalf("UnlinkIdea twice: %v", err)
	}
	if links, _ := svc.Ideas().ListIdeas(ctx, exp.ID); len(links) != 0 {
		t.Errorf("links after unlink = %+v", links)
	}
	acts, _ = f.Project.Reader().List(ctx, events.Query{EntityID: exp.ID, Limit: 50})
	removed := 0
	for _, act := range acts {
		if act.Type == schema.EventRelationRemoved {
			removed++
		}
	}
	if removed != 1 {
		t.Errorf("relation.removed count = %d, want 1", removed)
	}

	// A soft-deleted idea keeps its relations but accepts no new ones.
	if err := svc.Ideas().LinkIdea(ctx, exp.ID, ideaID, schema.ActorUser); err != nil {
		t.Fatalf("relink: %v", err)
	}
	if _, err := f.Project.DB().Exec(`UPDATE ideas SET deleted_at = ? WHERE id = ?`, clock.Format(f.Clock.Now()), ideaID); err != nil {
		t.Fatalf("soft delete idea: %v", err)
	}
	links, err = svc.Ideas().ListIdeas(ctx, exp.ID)
	if err != nil {
		t.Fatalf("ListIdeas after idea delete: %v", err)
	}
	if len(links) != 1 || !links[0].TargetDeleted {
		t.Errorf("links after target delete = %+v", links)
	}
	if err := svc.Ideas().UnlinkIdea(ctx, exp.ID, ideaID, schema.ActorUser); err != nil {
		t.Fatalf("unlink deleted target: %v", err)
	}
	if err := svc.Ideas().LinkIdea(ctx, exp.ID, ideaID, schema.ActorUser); err == nil {
		t.Error("linking a soft-deleted idea must fail")
	}

	// Cross-project ids are rejected.
	otherID, otherHandle, err := f.NewProject(ctx, "Other", "", "")
	if err != nil {
		t.Fatalf("NewProject: %v", err)
	}
	otherIdea := newID(t)
	now := clock.Format(f.Clock.Now())
	if _, err := otherHandle.DB().Exec(
		`INSERT INTO ideas(id, title, description, status, rejection_reason, created_at, updated_at)
		 VALUES (?, 'foreign', '', 'New', '', ?, ?)`, otherIdea, now, now); err != nil {
		t.Fatalf("insert foreign idea: %v", err)
	}
	if err := svc.Ideas().LinkIdea(ctx, exp.ID, otherIdea, schema.ActorUser); err == nil {
		t.Errorf("cross-project idea %s (project %s) was accepted", otherIdea, otherID)
	}
	if err := svc.Ideas().LinkIdea(ctx, exp.ID, "not-a-uuid", schema.ActorUser); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("invalid idea id error = %v", err)
	}
}

// TestGitInspectionIsReadOnly covers AC-09.
func TestGitInspectionIsReadOnly(t *testing.T) {
	svc, _ := newFixture(t)
	ctx := context.Background()
	root := externalDir(t)
	runGitCmd(t, root, "init", "-b", "main")
	runGitCmd(t, root, "config", "user.email", "test@example.com")
	runGitCmd(t, root, "config", "user.name", "Test")
	write(t, filepath.Join(root, "train.py"), "print('v1')\n")
	runGitCmd(t, root, "add", "train.py")
	runGitCmd(t, root, "commit", "-m", "first")

	headBefore := readFile(t, filepath.Join(root, ".git", "HEAD"))
	indexBefore := fileHash(t, filepath.Join(root, ".git", "index"))

	info, err := svc.Git().Inspect(ctx, root)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !info.Detected || info.Branch != "main" || len(info.Commit) != 40 || info.CommitShort != info.Commit[:7] {
		t.Fatalf("git info = %+v", info)
	}
	if info.CommitTime == "" || info.Dirty || info.ChangedFiles != 0 || info.UntrackedFiles != 0 {
		t.Errorf("clean repo info = %+v", info)
	}
	if _, err := clock.Parse(info.CommitTime); err != nil {
		t.Errorf("commit time %q: %v", info.CommitTime, err)
	}

	write(t, filepath.Join(root, "train.py"), "print('v2')\n")
	write(t, filepath.Join(root, "new.txt"), "untracked\n")
	info, err = svc.Git().Inspect(ctx, root)
	if err != nil {
		t.Fatalf("Inspect dirty: %v", err)
	}
	if !info.Dirty || info.ChangedFiles != 1 || info.UntrackedFiles != 1 {
		t.Errorf("dirty info = %+v", info)
	}

	diff, err := svc.Git().Diff(ctx, root, DiffOptions{})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(diff.Files) != 1 || diff.Files[0].Path != "train.py" {
		t.Errorf("diff files = %+v", diff.Files)
	}
	if diff.Files[0].Added != 1 || diff.Files[0].Deleted != 1 {
		t.Errorf("diff counts = %+v", diff.Files[0])
	}
	if !strings.Contains(diff.Patch, "print('v2')") {
		t.Errorf("patch = %q", diff.Patch)
	}
	truncated, err := svc.Git().Diff(ctx, root, DiffOptions{MaxBytes: 10})
	if err != nil {
		t.Fatalf("Diff truncated: %v", err)
	}
	if !truncated.Truncated || len(truncated.Patch) != 10 {
		t.Errorf("truncated diff = %q (%v)", truncated.Patch, truncated.Truncated)
	}
	if _, err := svc.Git().Diff(ctx, root, DiffOptions{From: "-bad"}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("bad revision error = %v", err)
	}

	if after := readFile(t, filepath.Join(root, ".git", "HEAD")); after != headBefore {
		t.Errorf("HEAD changed: %q → %q", headBefore, after)
	}
	if after := fileHash(t, filepath.Join(root, ".git", "index")); after != indexBefore {
		t.Errorf("index changed: %q → %q", indexBefore, after)
	}
	commits := strings.TrimSpace(runGitCmd(t, root, "rev-list", "--count", "HEAD"))
	if commits != "1" {
		t.Errorf("commit count = %q", commits)
	}

	plain := externalDir(t)
	noRepo, err := svc.Git().Inspect(ctx, plain)
	if err != nil {
		t.Fatalf("Inspect without repo: %v", err)
	}
	if noRepo.Detected {
		t.Errorf("detected = true for %s", plain)
	}
}

// runGitCmd runs one git command in dir for test setup/assertions.
func runGitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_DATE=2026-11-10T10:00:00Z", "GIT_COMMITTER_DATE=2026-11-10T10:00:00Z")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// readFile reads a small test file.
func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// fileHash returns the SHA-256 of a file.
func fileHash(t *testing.T, path string) string {
	t.Helper()
	sum, err := hashFile(path)
	if err != nil {
		t.Fatalf("hash %s: %v", path, err)
	}
	return sum
}

// TestContextBudgetAndMalformedConfig covers AC-05's budget side and the
// parse-failure warning of AC-06.
func TestContextBudgetAndMalformedConfig(t *testing.T) {
	svc, _ := newFixture(t)
	ctx := context.Background()
	root := externalDir(t)
	write(t, filepath.Join(root, "README.md"), "# Doc\n"+strings.Repeat("readme line\n", 200))
	write(t, filepath.Join(root, "configs", "broken.yaml"), "model: [unclosed\n")
	write(t, filepath.Join(root, "configs", "good.json"), `{"lr": 0.1}`)
	write(t, filepath.Join(root, "logs", "run.log"), strings.Repeat("line\n", 500))
	exp, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "budget", DirPath: root})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	target := TargetRef{Type: TargetExperiment, ID: exp.ID}
	before := treeFingerprint(t, root)
	scan, err := svc.Scanning().Run(ctx, target, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var malformed bool
	for _, warning := range scan.Warnings {
		if warning.Code == "config_parse_failed" && warning.Path == "configs/broken.yaml" {
			malformed = true
		}
	}
	if !malformed {
		t.Errorf("malformed YAML warning missing: %+v", scan.Warnings)
	}
	for _, cfg := range scan.Configs {
		if cfg.Path == "configs/broken.yaml" && cfg.ParseError == nil {
			t.Error("broken.yaml parsed without an error")
		}
	}

	bundle, err := svc.Context().Build(ctx, scan, nil, ContextBudget{MaxBytes: 300})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if bundle.Bytes > 300 || !bundle.Truncated {
		t.Errorf("budget = %d bytes, truncated=%v", bundle.Bytes, bundle.Truncated)
	}
	if len(bundle.Entries) == 0 {
		t.Error("budgeted bundle has no entries")
	}
	if bundle.Entries[0].Kind != KindReadme {
		t.Errorf("first entry kind = %q, want readme priority", bundle.Entries[0].Kind)
	}
	if len(bundle.Excluded) == 0 {
		t.Error("budget truncation left no Excluded rows")
	}

	after := treeFingerprint(t, root)
	if len(before) != len(after) {
		t.Fatalf("Run changed the tree: %d → %d", len(before), len(after))
	}
	for path, want := range before {
		if after[path] != want {
			t.Errorf("Run changed %s", path)
		}
	}
}
