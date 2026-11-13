package experiments

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
)

// newFixture builds a workspace with one project and returns the SPEC-07
// service bound to it.
func newFixture(t *testing.T) (*Service, *foundationtest.Fixture) {
	t.Helper()
	f, err := foundationtest.NewWithProject(t.TempDir(), foundationtest.Options{
		Clock: &clock.Fixed{T: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return New(f.Workspace, f.ProjectID, Options{Clock: f.Clock}), f
}

// write creates a file with parents and returns its absolute path.
func write(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// externalDir returns a directory outside the workspace.
func externalDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// sampleTree creates the representative experiment directory of AC-06.
func sampleTree(t *testing.T) string {
	t.Helper()
	root := externalDir(t)
	write(t, filepath.Join(root, "README.md"), "# Demo experiment\n\n## Setup\n\nRun `train.py`.\n")
	write(t, filepath.Join(root, "train.py"), "print('train')\n")
	write(t, filepath.Join(root, "main.py"), "print('main')\n")
	write(t, filepath.Join(root, "configs", "base.yaml"), "seed: 42\nmodel:\n  name: resnet\n  layers: 18\n")
	write(t, filepath.Join(root, "configs", "run.json"), `{"lambda": 0.01, "epochs": 10}`)
	write(t, filepath.Join(root, "configs", "train.toml"), "[train]\nlr = 0.001\nbatch = 32\n")
	write(t, filepath.Join(root, "data", "samples.csv"), "epoch,loss,acc\n1,0.9,0.60\n2,0.5,0.75\n3,0.25,0.912\n")
	write(t, filepath.Join(root, "metrics.json"), `{"eval": {"accuracy": 0.912, "f1": 0.87}, "train_loss": 0.25}`)
	write(t, filepath.Join(root, "logs", "train.log"), "start\nwarn: warmup\nerror: none\n")
	write(t, filepath.Join(root, "requirements.txt"), "numpy==2.0\n# comment\n\nscipy\n")
	write(t, filepath.Join(root, "weights", "model.pt"), "binary-weights")
	write(t, filepath.Join(root, ".env"), "TOKEN=secret")
	write(t, filepath.Join(root, "__pycache__", "train.cpython-312.pyc"), "cache")
	write(t, filepath.Join(root, "outputs", "result.bin"), "user-excluded")
	write(t, filepath.Join(root, "notes.txt"), "plain text notes\n")
	return root
}

// treeFingerprint captures path, size and mtime of every file below root.
func treeFingerprint(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			out[path] = "dir"
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		out[path] = fmt.Sprintf("%d/%d", info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatalf("fingerprint %s: %v", root, err)
	}
	return out
}

func TestExperimentLifecycle(t *testing.T) {
	svc, f := newFixture(t)
	ctx := context.Background()

	exp, err := svc.Experiments().Create(ctx, CreateExperimentInput{
		Title:   "  Baseline experiment  ",
		Purpose: "validate idea",
		Actor:   schema.ActorUser,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if exp.Title != "Baseline experiment" {
		t.Errorf("Title = %q", exp.Title)
	}
	if exp.DirRef != nil {
		t.Errorf("DirRef = %v, want nil", *exp.DirRef)
	}
	if len(exp.ScanRules.Exclude) == 0 {
		t.Error("default scan rules were not stored")
	}

	list, err := svc.Experiments().List(ctx, ExperimentFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != exp.ID {
		t.Fatalf("List = %+v", list)
	}

	purpose := "updated purpose"
	if _, err := svc.Experiments().Update(ctx, UpdateExperimentInput{ID: exp.ID, Purpose: &purpose}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if err := svc.Experiments().SoftDelete(ctx, exp.ID, schema.ActorUser); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	list, err = svc.Experiments().List(ctx, ExperimentFilter{})
	if err != nil || len(list) != 0 {
		t.Fatalf("List after delete = %v, %v", list, err)
	}
	if _, err := svc.Experiments().Get(ctx, exp.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get deleted error = %v, want ErrNotFound", err)
	}
	if err := svc.Experiments().Restore(ctx, exp.ID, schema.ActorUser); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	detail, err := svc.Experiments().Get(ctx, exp.ID)
	if err != nil {
		t.Fatalf("Get after restore: %v", err)
	}
	if detail.Experiment.Purpose != purpose {
		t.Errorf("Purpose = %q", detail.Experiment.Purpose)
	}

	acts, err := f.Project.Reader().List(ctx, events.Query{EntityID: exp.ID, Limit: 50})
	if err != nil {
		t.Fatalf("activities: %v", err)
	}
	seen := map[schema.EventType]int{}
	for _, act := range acts {
		seen[act.Type]++
		if act.Payload.Actor != schema.ActorUser {
			t.Errorf("actor = %q", act.Payload.Actor)
		}
		if act.Payload.EntityID != exp.ID || act.Payload.EntityType != schema.EntityExperiment {
			t.Errorf("payload identity = %+v", act.Payload)
		}
	}
	if seen[schema.EventEntityCreated] != 1 || seen[schema.EventEntityDeleted] != 1 || seen[schema.EventEntityRestored] != 1 {
		t.Errorf("event counts = %v", seen)
	}
	for _, act := range acts {
		if act.Type == schema.EventEntityCreated {
			if act.Payload.New == nil || (*act.Payload.New)["title"] != exp.Title {
				t.Errorf("created payload new = %+v", act.Payload.New)
			}
		}
	}
}

func TestCreateValidationAndDirectoryGuard(t *testing.T) {
	svc, f := newFixture(t)
	ctx := context.Background()

	if _, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "   "}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("blank title error = %v", err)
	}
	if _, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "x", DirPath: "relative/path"}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("relative path error = %v", err)
	}
	inside := filepath.Join(f.Workspace.Root(), "projects")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "x", DirPath: inside}); !errors.Is(err, ErrDirGuard) {
		t.Errorf("workspace-internal path error = %v, want ErrDirGuard", err)
	}
	if _, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "x", DirPath: filepath.Join(externalDir(t), "missing")}); !errors.Is(err, ErrDirUnavailable) {
		t.Errorf("missing dir error = %v, want ErrDirUnavailable", err)
	}
	list, err := svc.Experiments().List(ctx, ExperimentFilter{IncludeDeleted: true})
	if err != nil || len(list) != 0 {
		t.Fatalf("failed creates left rows: %v, %v", list, err)
	}
}

// TestScanIsNonInvasiveAndDeterministic covers AC-02 and AC-04.
func TestScanIsNonInvasiveAndDeterministic(t *testing.T) {
	svc, _ := newFixture(t)
	ctx := context.Background()
	root := sampleTree(t)
	before := treeFingerprint(t, root)

	exp, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "scan", DirPath: root})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	plan1, err := svc.Scanning().Plan(ctx, TargetRef{Type: TargetExperiment, ID: exp.ID}, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	plan2, err := svc.Scanning().Plan(ctx, TargetRef{Type: TargetExperiment, ID: exp.ID}, nil)
	if err != nil {
		t.Fatalf("Plan twice: %v", err)
	}
	if len(plan1.Entries) == 0 {
		t.Fatal("no entries")
	}
	if len(plan1.Entries) != len(plan2.Entries) {
		t.Fatalf("entry counts differ: %d vs %d", len(plan1.Entries), len(plan2.Entries))
	}
	for i := range plan1.Entries {
		a, b := plan1.Entries[i], plan2.Entries[i]
		if a.Path != b.Path || a.Kind != b.Kind || a.Size != b.Size || a.ContentEligible != b.ContentEligible || a.MTime != b.MTime {
			t.Fatalf("entry %d differs: %+v vs %+v", i, a, b)
		}
		if (a.SHA256 == nil) != (b.SHA256 == nil) || (a.SHA256 != nil && *a.SHA256 != *b.SHA256) {
			t.Fatalf("hash %s differs", a.Path)
		}
	}
	after := treeFingerprint(t, root)
	if len(before) != len(after) {
		t.Fatalf("scan changed the entry count: %d → %d", len(before), len(after))
	}
	for path, want := range before {
		if after[path] != want {
			t.Errorf("scan changed %s: %q → %q", path, want, after[path])
		}
	}
}

// TestScanClassificationAndExclusions covers AC-05 and AC-06.
func TestScanClassificationAndExclusions(t *testing.T) {
	svc, _ := newFixture(t)
	ctx := context.Background()
	root := sampleTree(t)
	rules := DefaultScanRules()
	rules.Exclude = append(rules.Exclude, "outputs/")
	exp, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "kinds", DirPath: root, Rules: &rules})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	plan, err := svc.Scanning().Plan(ctx, TargetRef{Type: TargetExperiment, ID: exp.ID}, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	kinds := map[string]FileKind{}
	reasons := map[string]ExclusionReason{}
	for _, entry := range plan.Entries {
		kinds[entry.Path] = entry.Kind
		if entry.ExclusionReason != nil {
			reasons[entry.Path] = *entry.ExclusionReason
		}
	}
	want := map[string]FileKind{
		"train.py":           KindCode,
		"README.md":          KindReadme,
		"configs/base.yaml":  KindConfig,
		"configs/run.json":   KindConfig,
		"configs/train.toml": KindConfig,
		"data/samples.csv":   KindData,
		"metrics.json":       KindMetrics,
		"logs/train.log":     KindLog,
		"requirements.txt":   KindOther,
		"weights/model.pt":   KindBinary,
		".env":               KindOther,
	}
	for path, kind := range want {
		if kinds[path] != kind {
			t.Errorf("%s kind = %q, want %q", path, kinds[path], kind)
		}
	}
	if reasons["weights/model.pt"] != ReasonModelWeight {
		t.Errorf("model weight reason = %q", reasons["weights/model.pt"])
	}
	if reasons[".env"] != ReasonSecret {
		t.Errorf(".env reason = %q", reasons[".env"])
	}
	pruned := map[string]ExclusionReason{}
	for _, entry := range plan.Pruned {
		pruned[entry.Path] = entry.Reason
	}
	if pruned["__pycache__/"] != ReasonCache {
		t.Errorf("__pycache__ prune reason = %q", pruned["__pycache__/"])
	}
	if pruned["outputs/"] != ReasonUserExclude {
		t.Errorf("outputs prune reason = %q", pruned["outputs/"])
	}

	// The context bundle never carries model weights, caches, secrets or
	// user-excluded output, and names every exclusion.
	scan, err := svc.Scanning().Run(ctx, TargetRef{Type: TargetExperiment, ID: exp.ID}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	bundle, err := svc.Context().Build(ctx, scan, nil, ContextBudget{MaxBytes: DefaultMaxTotalBytes})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	excluded := map[string]ExclusionReason{}
	for _, entry := range bundle.Excluded {
		excluded[entry.Path] = entry.Reason
	}
	for _, path := range []string{"weights/model.pt", ".env", "outputs/", "__pycache__/"} {
		if _, ok := excluded[path]; !ok {
			t.Errorf("%s missing from Excluded", path)
		}
	}
	for _, entry := range bundle.Entries {
		switch entry.Path {
		case "weights/model.pt", ".env":
			t.Errorf("%s entered the context", entry.Path)
		}
	}
	if _, ok := excluded["weights/model.pt"]; ok && excluded["weights/model.pt"] != ReasonModelWeight {
		t.Errorf("bundle reason = %q", excluded["weights/model.pt"])
	}
}

// TestScanLimitsAndCancel covers AC-21 and cancellation.
func TestScanLimitsAndCancel(t *testing.T) {
	svc, _ := newFixture(t)
	ctx := context.Background()
	root := externalDir(t)
	for i := range 10 {
		write(t, filepath.Join(root, fmt.Sprintf("f%02d.txt", i)), "x")
	}
	exp, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "limit", DirPath: root})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	rules := DefaultScanRules()
	rules.MaxFiles = 4
	plan, err := svc.Scanning().Plan(ctx, TargetRef{Type: TargetExperiment, ID: exp.ID}, &rules)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plan.Truncated {
		t.Error("Truncated = false, want true")
	}
	if len(plan.Entries) != 4 {
		t.Errorf("entries = %d, want 4", len(plan.Entries))
	}
	if len(plan.Warnings) == 0 {
		t.Error("missing truncation warning")
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := svc.Scanning().Plan(cancelled, TargetRef{Type: TargetExperiment, ID: exp.ID}, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled scan error = %v", err)
	}
}

// TestDirectoryBrokenAndRelocate covers AC-03.
func TestDirectoryBrokenAndRelocate(t *testing.T) {
	svc, f := newFixture(t)
	ctx := context.Background()
	oldRoot := externalDir(t)
	write(t, filepath.Join(oldRoot, "README.md"), "# old\n")
	exp, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "move", DirPath: oldRoot})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	moved := oldRoot + "-moved"
	if err := os.Rename(oldRoot, moved); err != nil {
		t.Fatalf("rename: %v", err)
	}
	statuses, err := svc.Experiments().VerifyDirectory(ctx, exp.ID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Status != DirBroken || statuses[0].Exists {
		t.Fatalf("statuses = %+v", statuses)
	}
	if statuses[0].Reason == "" {
		t.Error("broken status has no reason")
	}
	// The probe records the broken state so the list and detail pages agree.
	detail, err := svc.Experiments().Get(ctx, exp.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(detail.DirRefs) != 1 || detail.DirRefs[0].Status != DirBroken {
		t.Errorf("stored status = %+v", detail.DirRefs)
	}

	ok, err := svc.Experiments().RelocateDirectory(ctx, exp.ID, moved, schema.ActorUser)
	if err != nil {
		t.Fatalf("Relocate: %v", err)
	}
	if ok.Status != DirOK || ok.Path != moved {
		t.Errorf("relocated = %+v", ok)
	}
	reloaded, err := svc.Experiments().Get(ctx, exp.ID)
	if err != nil {
		t.Fatalf("Get after relocate: %v", err)
	}
	if reloaded.Experiment.DirRef == nil || *reloaded.Experiment.DirRef != moved {
		t.Errorf("dir_ref = %v", reloaded.Experiment.DirRef)
	}
	if len(reloaded.DirRefs) != 1 || reloaded.DirRefs[0].Path != moved || reloaded.DirRefs[0].Status != DirOK {
		t.Errorf("external ref = %+v", reloaded.DirRefs)
	}
	acts, err := f.Project.Reader().List(ctx, events.Query{EntityID: exp.ID, Limit: 20})
	if err != nil {
		t.Fatalf("activities: %v", err)
	}
	found := false
	for _, act := range acts {
		if act.Type == schema.EventEntityRelocated {
			found = true
		}
	}
	if !found {
		t.Error("entity.relocated was not emitted by externalrefs")
	}
}

// TestBindDirectoryRules covers SetScanRules and rule storage.
func TestBindDirectoryRules(t *testing.T) {
	svc, _ := newFixture(t)
	ctx := context.Background()
	root := externalDir(t)
	write(t, filepath.Join(root, "keep.txt"), "x")
	exp, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "rules", DirPath: root})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	rules := DefaultScanRules()
	rules.Include = []string{"**/*.txt"}
	rules.Exclude = []string{"skip/"}
	rules.MaxFileBytes = 1024
	rules.MaxTotalBytes = 4096
	if err := svc.Experiments().SetScanRules(ctx, exp.ID, rules); err != nil {
		t.Fatalf("SetScanRules: %v", err)
	}
	detail, err := svc.Experiments().Get(ctx, exp.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if detail.Experiment.ScanRules.MaxFileBytes != 1024 {
		t.Errorf("stored max_file_bytes = %d", detail.Experiment.ScanRules.MaxFileBytes)
	}
	if !containsPattern(detail.Experiment.ScanRules.Exclude, ".git/") {
		t.Error(".git/ was not force-appended")
	}
	bad := DefaultScanRules()
	bad.MaxTotalBytes = 1
	if err := svc.Experiments().SetScanRules(ctx, exp.ID, bad); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("invalid thresholds error = %v", err)
	}
	if err := svc.Experiments().SetScanRules(ctx, exp.ID, ScanRules{Include: []string{"["}}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("invalid glob error = %v", err)
	}
}

// TestArchivedProjectIsReadOnly covers AC-17.
func TestArchivedProjectIsReadOnly(t *testing.T) {
	svc, f := newFixture(t)
	ctx := context.Background()
	exp, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "before archive"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.Workspace.Global().DB().ExecContext(ctx,
		`UPDATE projects SET status = 'archived', archived_at = ? WHERE id = ?`,
		clock.Format(f.Clock.Now()), f.ProjectID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	before := countRows(t, f, "experiments")
	if _, err := svc.Experiments().Create(ctx, CreateExperimentInput{Title: "blocked"}); !errors.Is(err, ErrReadOnlyProject) {
		t.Errorf("Create error = %v", err)
	}
	if _, err := svc.Experiments().Update(ctx, UpdateExperimentInput{ID: exp.ID, Title: new("x")}); !errors.Is(err, ErrReadOnlyProject) {
		t.Errorf("Update error = %v", err)
	}
	if err := svc.Experiments().SoftDelete(ctx, exp.ID, schema.ActorUser); !errors.Is(err, ErrReadOnlyProject) {
		t.Errorf("SoftDelete error = %v", err)
	}
	if err := svc.Experiments().SetScanRules(ctx, exp.ID, DefaultScanRules()); !errors.Is(err, ErrReadOnlyProject) {
		t.Errorf("SetScanRules error = %v", err)
	}
	if _, err := svc.Runs().Create(ctx, CreateRunInput{ExperimentID: exp.ID}); !errors.Is(err, ErrReadOnlyProject) {
		t.Errorf("CreateRun error = %v", err)
	}
	if after := countRows(t, f, "experiments"); after != before {
		t.Errorf("rows changed under archive: %d → %d", before, after)
	}
	// Reads still work.
	if _, err := svc.Experiments().Get(ctx, exp.ID); err != nil {
		t.Errorf("Get under archive: %v", err)
	}
	if _, err := svc.Experiments().List(ctx, ExperimentFilter{}); err != nil {
		t.Errorf("List under archive: %v", err)
	}
}

// countRows counts experiments in the project database.
func countRows(t *testing.T, f *foundationtest.Fixture, table string) int {
	t.Helper()
	var n int
	if err := f.Project.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// insertIdea inserts an idea row directly; SPEC-06 is not part of this module.
func insertIdea(t *testing.T, f *foundationtest.Fixture, title string) string {
	t.Helper()
	id := newID(t)
	now := clock.Format(f.Clock.Now())
	if _, err := f.Project.DB().Exec(
		`INSERT INTO ideas(id, title, description, status, rejection_reason, created_at, updated_at)
		 VALUES (?, ?, '', 'New', '', ?, ?)`, id, title, now, now); err != nil {
		t.Fatalf("insert idea: %v", err)
	}
	return id
}

// newID returns a valid UUIDv7.
func newID(t *testing.T) string {
	t.Helper()
	return ids.MustNew(nil)
}

// seedResultsSnapshot appends one results snapshot for a run.
func seedResultsSnapshot(t *testing.T, svc *Service, f *foundationtest.Fixture, runID string, metrics []Metric) *Snapshot {
	t.Helper()
	ctx := context.Background()
	snap := &Snapshot{
		Target:    TargetRef{Type: TargetRun, ID: runID},
		CreatedAt: f.Clock.Now(),
		GitInfo:   GitInfo{Detected: false},
		Metrics:   MetricSet{V: 1, Metrics: metrics, Unparsed: []UnparsedFile{}},
		AIAnalysis: &AIAnalysis{
			V:             1,
			Kind:          AnalysisResults,
			PromptVersion: "experiments.results.v1",
			Sections:      []AISection{},
			Config:        AIAnalysisConfig{ScanRules: DefaultScanRules(), RunParams: map[string]ParamValue{}, Extracted: []ExtractedConfig{}},
			Warnings:      []string{},
		},
		ModelInfo: ModelInfo{V: 1, ProviderName: "test", ModelID: "m1"},
	}
	snap.ID = ids.MustNew(f.Clock)
	if err := f.Project.InTx(ctx, func(tx *sql.Tx) error {
		return svc.saveSnapshot(ctx, tx, snap)
	}); err != nil {
		t.Fatalf("saveSnapshot: %v", err)
	}
	return snap
}

// metric builds one metric row.
func metric(key string, value float64, path string) Metric {
	line := 3
	ptr := "/" + key
	return Metric{
		Key:       key,
		Label:     key,
		Value:     new(value),
		Raw:       strings.TrimSpace(fmt.Sprintf("%g", value)),
		Source:    MetricSource{Path: path, Pointer: &ptr, Line: &line},
		Extractor: extractorJSONLeaves,
	}
}

// TestScanRulesJSONRoundTrip covers E01: defaults, validation and preservation
// of unknown top-level keys.
func TestScanRulesJSONRoundTrip(t *testing.T) {
	rules, err := decodeScanRules([]byte(`{}`))
	if err != nil {
		t.Fatalf("decode empty: %v", err)
	}
	if rules.MaxFiles != DefaultMaxFiles || len(rules.Exclude) == 0 {
		t.Errorf("defaults = %+v", rules)
	}
	if _, err := decodeScanRules([]byte(`{"v":1,"include":["["]}`)); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("invalid glob error = %v", err)
	}
	merged, err := mergeUnknownKeys([]byte(`{"v":1,"future_flag":true,"max_files":7}`), DefaultScanRules(), knownScanRuleKeys)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(merged, &object); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}
	if object["future_flag"] != true {
		t.Errorf("unknown key lost: %s", merged)
	}
	if object["max_files"] != float64(DefaultMaxFiles) {
		t.Errorf("known key not overwritten: %s", merged)
	}
	roundTrip, err := decodeScanRules(merged)
	if err != nil || roundTrip.MaxFiles != DefaultMaxFiles {
		t.Errorf("round trip = %+v, %v", roundTrip, err)
	}
}
