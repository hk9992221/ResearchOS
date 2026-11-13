package export

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/storage"
)

// seedRichProject fills the default project with notes, attachments, papers
// (one shared with a second project) and history.
func seedRichProject(t *testing.T, f *foundationtest.Fixture, s *Service) (deletedNote, sharedPaper string) {
	t.Helper()
	ctx := context.Background()
	otherID, otherHandle, err := f.NewProject(ctx, "Other", "", "")
	if err != nil {
		t.Fatalf("second project: %v", err)
	}
	_ = otherID

	liveNote := addNote(t, f, f.Project, false, true)
	deletedNote = addNote(t, f, f.Project, true, true)
	addAttachment(t, f, f.ProjectID, liveNote, "image.png", bytes.Repeat([]byte("i"), 4096))
	addAttachment(t, f, f.ProjectID, liveNote, "nested/data.bin", bytes.Repeat([]byte("d"), 8192))

	mine := addPaper(t, f, "MINE0001", bytes.Repeat([]byte("%PDF-mine "), 2048))
	sharedPaper = addPaper(t, f, "SHARED01", bytes.Repeat([]byte("%PDF-shared "), 1024))
	otherPaper := addPaper(t, f, "OTHER001", bytes.Repeat([]byte("%PDF-other "), 1024))
	linkPaper(t, f, f.Project, mine)
	linkPaper(t, f, f.Project, sharedPaper)
	linkPaper(t, f, otherHandle, sharedPaper)
	linkPaper(t, f, otherHandle, otherPaper)
	addNote(t, f, otherHandle, false, true)

	addActivity(t, f.Project)
	addRelation(t, f, f.Project)
	return deletedNote, sharedPaper
}

func TestExportFormatsIdentical(t *testing.T) {
	f, s := newFixture(t)
	deletedNote, sharedPaper := seedRichProject(t, f, s)

	formats := []Format{FormatFolder, FormatZip, FormatTarGz}
	bundles := make([]map[string][]byte, 0, len(formats))
	for _, format := range formats {
		dest := t.TempDir()
		res := exportOnce(t, s, Request{
			ProjectID: f.ProjectID, DestDir: dest, Format: format, BaseName: "Bundle",
		})
		if res.Format != format {
			t.Fatalf("result format = %q, want %q", res.Format, format)
		}
		if res.FileCount == 0 || res.Bytes == 0 {
			t.Fatalf("%s: empty result %+v", format, res)
		}
		if format == FormatFolder && res.ArchiveSHA256 != "" {
			t.Fatalf("folder export has archive hash %q", res.ArchiveSHA256)
		}
		if format != FormatFolder && len(res.ArchiveSHA256) != 64 {
			t.Fatalf("%s: archive hash %q", format, res.ArchiveSHA256)
		}
		if format == FormatZip {
			zr, err := zip.OpenReader(res.Path)
			if err != nil {
				t.Fatalf("open zip: %v", err)
			}
			last := zr.File[len(zr.File)-1].Name
			if !strings.HasSuffix(last, "/manifest.json") {
				t.Fatalf("manifest is not the last zip entry: %s", last)
			}
			for _, f := range zr.File {
				switch {
				case strings.HasSuffix(f.Name, "/source.pdf") && f.Method != zip.Store:
					t.Fatalf("pdf %s uses method %d, want Store", f.Name, f.Method)
				case strings.HasSuffix(f.Name, ".md") && f.Method != zip.Deflate:
					t.Fatalf("markdown %s uses method %d, want Deflate", f.Name, f.Method)
				case strings.HasSuffix(f.Name, ".json") && f.Method != zip.Deflate:
					t.Fatalf("json %s uses method %d, want Deflate", f.Name, f.Method)
				}
			}
			zr.Close()
		}
		bundle := readBundle(t, format, res.Path, "Bundle")
		assertBundle(t, f, bundle, format)
		assertManifest(t, bundle)
		assertProjectDB(t, bundle)
		assertOnlyProject(t, f, bundle, deletedNote, sharedPaper)
		bundles = append(bundles, bundle)
	}

	base := bundles[0]
	for i := 1; i < len(bundles); i++ {
		compareBundles(t, formats[0], base, formats[i], bundles[i])
	}
}

// assertBundle checks the expected logical entries.
func assertBundle(t *testing.T, f *foundationtest.Fixture, bundle map[string][]byte, format Format) {
	t.Helper()
	if _, ok := bundle["workspace/"+f.Workspace.Paths().ProjectDB(f.ProjectID)]; !ok {
		t.Fatalf("%s: project.db missing", format)
	}
	for _, path := range []string{"meta/project.json", "meta/papers.json", "manifest.json"} {
		if _, ok := bundle[path]; !ok {
			t.Fatalf("%s: %s missing", format, path)
		}
	}
	var notes, attachments, pdfs int
	for path := range bundle {
		switch {
		case strings.Contains(path, "/notes/") && strings.HasSuffix(path, ".md"):
			notes++
		case strings.Contains(path, "/attachments/"):
			attachments++
		case strings.HasSuffix(path, "/source.pdf"):
			pdfs++
		}
	}
	if notes != 2 {
		t.Fatalf("%s: notes = %d, want 2", format, notes)
	}
	if attachments != 2 {
		t.Fatalf("%s: attachments = %d, want 2", format, attachments)
	}
	if pdfs != 2 {
		t.Fatalf("%s: pdfs = %d, want 2", format, pdfs)
	}
}

// assertManifest verifies §4.5 consistency.
func assertManifest(t *testing.T, bundle map[string][]byte) {
	t.Helper()
	m := decodeManifest(t, bundle)
	if m.ManifestVersion != manifestVersion {
		t.Fatalf("manifest version = %q", m.ManifestVersion)
	}
	if m.App.Name != "ResearchOS" || m.App.SchemaGlobal == 0 || m.App.SchemaProject == 0 {
		t.Fatalf("bad app block: %+v", m.App)
	}
	if _, err := time.Parse(time.RFC3339, m.ExportedAt); err != nil {
		t.Fatalf("exported_at %q: %v", m.ExportedAt, err)
	}

	seen := make(map[string]bool, len(m.Files))
	var bytes int64
	for _, entry := range m.Files {
		if entry.Path == "manifest.json" {
			t.Fatal("manifest lists itself")
		}
		seen[entry.Path] = true
		data, ok := bundle[entry.Path]
		if !ok {
			t.Fatalf("manifest file %s missing from bundle", entry.Path)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != entry.SHA256 {
			t.Fatalf("%s: sha = %s, manifest %s", entry.Path, got, entry.SHA256)
		}
		if int64(len(data)) != entry.Bytes {
			t.Fatalf("%s: bytes = %d, manifest %d", entry.Path, len(data), entry.Bytes)
		}
		bytes += entry.Bytes
	}
	for path := range bundle {
		if path == "manifest.json" {
			continue
		}
		if !seen[path] {
			t.Fatalf("%s missing from manifest.files", path)
		}
	}
	if m.Counts.Files != len(m.Files) || m.Counts.Bytes != bytes {
		t.Fatalf("counts = %+v, files = %d bytes = %d", m.Counts, len(m.Files), bytes)
	}

	byPath := make(map[string]manifestPaper, len(m.Papers))
	for _, p := range m.Papers {
		byPath[p.ID] = p
		if p.Missing {
			if p.SourcePDF != "" && seen[p.SourcePDF] {
				t.Fatalf("%s marked missing but present", p.ID)
			}
			continue
		}
		if p.SourcePDF == "" || !seen[p.SourcePDF] {
			t.Fatalf("%s: papers/source_pdf inconsistent", p.ID)
		}
	}
	if len(byPath) != m.Counts.Papers {
		t.Fatalf("papers %d, counts %d", len(byPath), m.Counts.Papers)
	}
	for _, p := range m.Papers {
		if !strings.HasPrefix(p.SourcePDF, "workspace/library/papers/"+p.ID+"/") && p.SourcePDF != "" {
			t.Fatalf("paper %s source_pdf = %q", p.ID, p.SourcePDF)
		}
	}
}

// assertProjectDB verifies the snapshot is a healthy database with history.
func assertProjectDB(t *testing.T, bundle map[string][]byte) {
	t.Helper()
	var dbPath string
	var data []byte
	for path, content := range bundle {
		if strings.HasSuffix(path, "/project.db") {
			dbPath, data = path, content
			break
		}
	}
	if dbPath == "" {
		t.Fatal("project.db missing")
	}
	file := filepath.Join(t.TempDir(), "project.db")
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	db, err := storage.OpenReadOnly(context.Background(), file)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer db.Close()
	var check string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if check != "ok" {
		t.Fatalf("integrity_check = %q", check)
	}
	var activities, relations int
	if err := db.QueryRow(`SELECT count(*) FROM activities`).Scan(&activities); err != nil {
		t.Fatalf("count activities: %v", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM entity_relations`).Scan(&relations); err != nil {
		t.Fatalf("count relations: %v", err)
	}
	if activities == 0 || relations == 0 {
		t.Fatalf("snapshot lost history: activities=%d relations=%d", activities, relations)
	}
}

// assertOnlyProject checks sharing and isolation. The seeded second project's
// id is read back from the fixture's global database so the test does not need
// to thread it through.
func assertOnlyProject(t *testing.T, f *foundationtest.Fixture, bundle map[string][]byte, deletedNote, sharedPaper string) {
	t.Helper()
	if _, ok := bundle["workspace/"+f.Workspace.Paths().NoteFile(f.ProjectID, deletedNote)]; !ok {
		t.Fatal("soft-deleted note markdown missing")
	}
	var otherIDs []string
	rows, err := f.Global().DB().Query(`SELECT id FROM projects WHERE id <> ?`, f.ProjectID)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatalf("scan project: %v", err)
		}
		otherIDs = append(otherIDs, id)
	}
	rows.Close()
	for path := range bundle {
		for _, id := range otherIDs {
			if strings.Contains(path, id) {
				t.Fatalf("foreign project path leaked: %s", path)
			}
		}
	}
	if _, ok := bundle["workspace/library/papers/"+sharedPaper+"/source.pdf"]; !ok {
		t.Fatal("shared paper PDF missing")
	}
}

// compareBundles checks that two formats carry the same logical files with the
// same bytes. manifest.json is excluded: it legitimately records the container
// format of the bundle it describes.
func compareBundles(t *testing.T, wantFormat Format, want map[string][]byte, gotFormat Format, got map[string][]byte) {
	t.Helper()
	delete(want, "manifest.json")
	delete(got, "manifest.json")
	wantKeys, gotKeys := sortedKeys(want), sortedKeys(got)
	if len(wantKeys) != len(gotKeys) {
		t.Fatalf("%s has %d entries, %s has %d", wantFormat, len(wantKeys), gotFormat, len(gotKeys))
	}
	for i := range wantKeys {
		if wantKeys[i] != gotKeys[i] {
			t.Fatalf("%s entry %q, %s entry %q", wantFormat, wantKeys[i], gotFormat, gotKeys[i])
		}
		// The project snapshot cannot be byte-identical between two sequential
		// exports: each successful export appends its own project.exported
		// Activity row to the live database that the next snapshot captures.
		// Compare its logical content instead; every other entry must hash the
		// same.
		if strings.HasSuffix(wantKeys[i], "/project.db") {
			compareProjectDBSnapshots(t, want[wantKeys[i]], got[gotKeys[i]])
			continue
		}
		sumWant := sha256.Sum256(want[wantKeys[i]])
		sumGot := sha256.Sum256(got[gotKeys[i]])
		if sumWant != sumGot {
			t.Fatalf("%s differs between %s and %s", wantKeys[i], wantFormat, gotFormat)
		}
	}
}

// compareProjectDBSnapshots compares the stable logical content of two
// snapshots, ignoring the append-only activity rows that exports themselves
// produce.
func compareProjectDBSnapshots(t *testing.T, want, got []byte) {
	t.Helper()
	queries := []string{
		`SELECT count(*) FROM notes`,
		`SELECT count(*) FROM project_papers`,
		`SELECT count(*) FROM entity_relations`,
		`SELECT coalesce(max(version), 0) FROM schema_migrations`,
	}
	for _, query := range queries {
		if a, b := querySingle(t, want, query), querySingle(t, got, query); a != b {
			t.Fatalf("project.db snapshot differs: %s: %d vs %d", query, a, b)
		}
	}
}

func querySingle(t *testing.T, data []byte, query string) int64 {
	t.Helper()
	file := filepath.Join(t.TempDir(), "snapshot.db")
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	db, err := storage.OpenReadOnly(context.Background(), file)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer db.Close()
	var n int64
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestExportArchivedProjectAndActivity(t *testing.T) {
	f, s := newFixture(t)
	addNote(t, f, f.Project, false, true)
	if _, err := f.Global().DB().Exec(
		`UPDATE projects SET status = 'archived', archived_at = ? WHERE id = ?`,
		clock.Format(f.Clock.Now()), f.ProjectID); err != nil {
		t.Fatalf("archive: %v", err)
	}

	res := exportOnce(t, s, Request{
		ProjectID: f.ProjectID, DestDir: t.TempDir(), Format: FormatZip, BaseName: "Archived",
	})
	if res.Path == "" {
		t.Fatal("no output path")
	}
	bundle := readBundle(t, FormatZip, res.Path, "Archived")
	m := decodeManifest(t, bundle)
	if m.Project.Status != "archived" {
		t.Fatalf("manifest status = %q", m.Project.Status)
	}

	var payload, actor string
	err := f.Project.DB().QueryRow(
		`SELECT payload, actor FROM activities WHERE event_type = 'project.exported' ORDER BY created_at DESC LIMIT 1`).
		Scan(&payload, &actor)
	if err != nil {
		t.Fatalf("read project.exported: %v", err)
	}
	if actor != "user" {
		t.Fatalf("actor = %q, want user", actor)
	}
	if strings.Contains(payload, f.Root) {
		t.Fatalf("activity payload leaks the absolute path: %s", payload)
	}
	var ev struct {
		EntityType string `json:"entity_type"`
		EntityID   string `json:"entity_id"`
		Summary    string `json:"summary"`
		Actor      string `json:"actor"`
	}
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if ev.EntityType != "project" || ev.EntityID != f.ProjectID || ev.Actor != "user" {
		t.Fatalf("payload = %+v", ev)
	}
	if !strings.Contains(ev.Summary, "zip") {
		t.Fatalf("summary = %q", ev.Summary)
	}
}

func TestExportActorMCP(t *testing.T) {
	f, s := newFixture(t)
	exportOnce(t, s, Request{
		ProjectID: f.ProjectID, DestDir: t.TempDir(), Format: FormatFolder,
		BaseName: "MCP", Actor: "mcp",
	})
	var actor string
	if err := f.Project.DB().QueryRow(
		`SELECT actor FROM activities WHERE event_type = 'project.exported'`).Scan(&actor); err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if actor != "mcp" {
		t.Fatalf("actor = %q", actor)
	}
}

func TestCancelLeavesNoArtefacts(t *testing.T) {
	ctx := context.Background()
	var (
		s    *Service
		f    *foundationtest.Fixture
		once sync.Once
	)
	f, s = newFixture(t, func(o *Options) {
		o.hook = func(stage, path string) {
			if stage != string(PhaseCopyAttachments) {
				return
			}
			once.Do(func() {
				s.mu.Lock()
				id := s.active.id
				s.mu.Unlock()
				_ = s.Cancel(context.Background(), id)
			})
		}
	})
	note := addNote(t, f, f.Project, false, true)
	addAttachment(t, f, f.ProjectID, note, "big.bin", bytes.Repeat([]byte("z"), 1<<20))
	addAttachment(t, f, f.ProjectID, note, "another.bin", bytes.Repeat([]byte("y"), 1<<20))

	dest := t.TempDir()
	plan, err := s.Plan(ctx, Request{
		ProjectID: f.ProjectID, DestDir: dest, Format: FormatZip, BaseName: "Cancel",
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	jobID, err := s.Start(ctx, plan.ID)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := s.Await(ctx, jobID); !errors.Is(err, context.Canceled) {
		t.Fatalf("await error = %v, want context.Canceled", err)
	}
	assertNoTempArtefacts(t, dest)
	if _, err := os.Lstat(plan.OutputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output exists after cancel: %v", err)
	}

	// A follow-up export with the same name succeeds.
	res := exportOnce(t, s, Request{
		ProjectID: f.ProjectID, DestDir: dest, Format: FormatZip, BaseName: "Cancel",
	})
	if filepath.Base(res.Path) != "Cancel.zip" {
		t.Fatalf("follow-up output = %s", res.Path)
	}
}

func assertNoTempArtefacts(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() && strings.HasPrefix(name, ".researchos-export-") {
			t.Fatalf("staging directory left behind: %s", path)
		}
		if !d.IsDir() && strings.HasSuffix(name, ".part") {
			t.Fatalf("partial file left behind: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk dest: %v", err)
	}
}

func TestExportNeverOverwrites(t *testing.T) {
	f, s := newFixture(t)
	addNote(t, f, f.Project, false, true)
	dest := t.TempDir()

	first := exportOnce(t, s, Request{
		ProjectID: f.ProjectID, DestDir: dest, Format: FormatFolder, BaseName: "Keep",
	})
	original := readBundle(t, FormatFolder, first.Path, "Keep")
	before := original["manifest.json"]

	second := exportOnce(t, s, Request{
		ProjectID: f.ProjectID, DestDir: dest, Format: FormatFolder, BaseName: "Keep",
	})
	if filepath.Base(second.Path) != "Keep-2" {
		t.Fatalf("second output = %s", second.Path)
	}
	after := readBundle(t, FormatFolder, first.Path, "Keep")
	if !bytes.Equal(before, after["manifest.json"]) {
		t.Fatal("first export was modified by the second")
	}
}

func TestProgressPhases(t *testing.T) {
	ctx := context.Background()
	entered := make(chan struct{})
	release := make(chan struct{})
	var hooked sync.Once

	f, s := newFixture(t, func(o *Options) {
		o.hook = func(stage, path string) {
			if stage == string(PhaseCopyNotes) {
				hooked.Do(func() {
					close(entered)
					<-release
				})
			}
		}
	})
	addNote(t, f, f.Project, false, true)
	addNote(t, f, f.Project, false, true)

	plan, err := s.Plan(ctx, Request{
		ProjectID: f.ProjectID, DestDir: t.TempDir(), Format: FormatFolder, BaseName: "Progress",
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	jobID, err := s.Start(ctx, plan.ID)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// Wait until the copy loop is inside the hook, then subscribe while the job
	// is guaranteed to still be running.
	<-entered
	progress, err := s.Progress(ctx, jobID)
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	close(release)

	var samples []Progress
	for p := range progress {
		samples = append(samples, p)
	}
	res, err := s.Await(ctx, jobID)
	if err != nil {
		t.Fatalf("await: %v", err)
	}
	if len(samples) == 0 {
		t.Fatal("no progress samples")
	}
	phases := map[Phase]bool{}
	for _, p := range samples {
		phases[p.Phase] = true
		if p.FilesTotal != len(plan.Files) {
			t.Fatalf("FilesTotal = %d, want %d", p.FilesTotal, len(plan.Files))
		}
		if p.FilesDone > p.FilesTotal {
			t.Fatalf("FilesDone %d > FilesTotal %d", p.FilesDone, p.FilesTotal)
		}
	}
	for _, want := range []Phase{PhaseCopyNotes, PhaseCopyAttachments, PhaseCopyPapers, PhaseFinalize} {
		if !phases[want] {
			t.Fatalf("phase %q missing from %v", want, phases)
		}
	}
	if res.FileCount != len(plan.Files) {
		t.Fatalf("FileCount = %d, want %d", res.FileCount, len(plan.Files))
	}
}

func TestJobAPIEdges(t *testing.T) {
	ctx := context.Background()
	f, s := newFixture(t)

	if _, err := s.Progress(ctx, "nope"); err == nil {
		t.Fatal("Progress on unknown job should fail")
	}
	if _, err := s.Await(ctx, "nope"); err == nil {
		t.Fatal("Await on unknown job should fail")
	}
	if err := s.Cancel(ctx, "nope"); err != nil {
		t.Fatalf("Cancel on unknown job = %v, want nil (idempotent)", err)
	}
	if _, err := s.Start(ctx, "nope"); err == nil {
		t.Fatal("Start on unknown plan should fail")
	}

	plan, err := s.Plan(ctx, Request{
		ProjectID: f.ProjectID, DestDir: t.TempDir(), Format: FormatFolder, BaseName: "Once",
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := s.Start(ctx, plan.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	// The plan was consumed by the first Start; a second attempt must fail.
	if _, err := s.Start(ctx, plan.ID); err == nil {
		t.Fatal("second Start should fail")
	}
	_ = s.Cancel(ctx, plan.ID)
}

// TestExpiredPlan checks the five-minute lifetime.
func TestExpiredPlan(t *testing.T) {
	ctx := context.Background()
	f, s := newFixture(t)
	plan, err := s.Plan(ctx, Request{
		ProjectID: f.ProjectID, DestDir: t.TempDir(), Format: FormatFolder, BaseName: "Late",
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	f.Clock.Advance(6 * time.Minute)
	if _, err := s.Start(ctx, plan.ID); !errors.Is(err, ErrPlanExpired) {
		t.Fatalf("start = %v, want ErrPlanExpired", err)
	}
}

// TestBusyWorkspaceLock proves a held workspace lock surfaces as ErrBusy.
func TestBusyWorkspaceLock(t *testing.T) {
	ctx := context.Background()
	f, s := newFixture(t)
	held := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = s.locker.WithWorkspaceLock(ctx, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	if _, err := s.Plan(ctx, Request{
		ProjectID: f.ProjectID, DestDir: t.TempDir(), Format: FormatZip,
	}); !errors.Is(err, ErrBusy) {
		close(release)
		t.Fatalf("plan = %v, want ErrBusy", err)
	}
	close(release)
}

// TestPapersFallbackToRelations covers the §7.3 fallback: with no
// project_papers rows, paper relations define the project's literature.
func TestPapersFallbackToRelations(t *testing.T) {
	ctx := context.Background()
	f, s := newFixture(t)
	paperID := addPaper(t, f, "REL00001", []byte("%PDF-related"))
	if _, err := f.Project.DB().Exec(
		`INSERT INTO entity_relations(id, src_type, src_id, relation, dst_type, dst_id, created_at)
		 VALUES (?, 'note', ?, 'references', 'paper', ?, ?)`,
		ids.MustNew(f.Clock), ids.MustNew(f.Clock), paperID, clock.Format(f.Clock.Now())); err != nil {
		t.Fatalf("insert relation: %v", err)
	}

	plan, err := s.Plan(ctx, Request{
		ProjectID: f.ProjectID, DestDir: t.TempDir(), Format: FormatZip,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.PaperCount != 1 {
		t.Fatalf("PaperCount = %d, want 1", plan.PaperCount)
	}
	found := false
	for _, pf := range plan.Files {
		if pf.Kind == KindPaperPDF && strings.Contains(pf.BundlePath, paperID) {
			found = true
		}
	}
	if !found {
		t.Fatal("relation-derived paper not planned")
	}
}

// TestMissingPaperFileState covers the papers.file_state != ok branch.
func TestMissingPaperFileState(t *testing.T) {
	ctx := context.Background()
	f, s := newFixture(t)
	paperID := addPaper(t, f, "BROKEN01", []byte("%PDF-broken"))
	linkPaper(t, f, f.Project, paperID)
	if _, err := f.Global().DB().Exec(`UPDATE papers SET file_state = 'missing' WHERE id = ?`, paperID); err != nil {
		t.Fatalf("mark missing: %v", err)
	}

	plan, err := s.Plan(ctx, Request{
		ProjectID: f.ProjectID, DestDir: t.TempDir(), Format: FormatZip,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	found := false
	for _, m := range plan.Missing {
		if m.Kind == "paper_pdf" && m.Ref == paperID {
			found = true
		}
	}
	if !found {
		t.Fatalf("paper with file_state=missing not recorded: %+v", plan.Missing)
	}
}
