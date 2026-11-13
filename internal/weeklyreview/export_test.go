package weeklyreview

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAC18ExportMarkdown(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	report := env.newReport(period, true)

	result, err := env.svc.Export(env.ctx, ExportInput{ReportID: report.ID, Format: "md", Actor: ActorUser})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	wantName := "2026-11-02_2026-11-08_weekly-report.md"
	if filepath.Base(result.Path) != wantName {
		t.Fatalf("file name = %s, want %s", filepath.Base(result.Path), wantName)
	}
	if result.Format != "md" || result.Status != StatusDraft {
		t.Fatalf("result = %+v", result)
	}
	raw, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	doc := string(raw)
	for _, line := range []string{
		"---\n",
		`project: "Alpha"`,
		`project_id: "` + testProjectID + `"`,
		`period_start: "2026-11-02"`,
		`period_end: "2026-11-08"`,
		`status: "draft"`,
		`generated_by: "manual"`,
		`exported_at: "2026-11-11T04:00:00Z"`,
	} {
		if !strings.Contains(doc, line) {
			t.Fatalf("front matter missing %q:\n%s", line, doc)
		}
	}
	if !strings.HasSuffix(doc, report.ContentMD) && !strings.HasSuffix(doc, report.ContentMD+"\n") {
		t.Fatalf("body does not match content_md:\n%s", doc)
	}
	if result.Bytes != int64(len(raw)) {
		t.Fatalf("bytes = %d, want %d", result.Bytes, len(raw))
	}
	if os.Getenv("UPDATE_GOLDEN") != "" {
		writeGolden(t, "export_report.md", doc)
	} else if want := readGolden(t, "export_report.md"); doc != want {
		t.Fatalf("export golden mismatch:\n--- got ---\n%s\n--- want ---\n%s", doc, want)
	}

	// Second export without overwrite is refused.
	if _, err := env.svc.Export(env.ctx, ExportInput{ReportID: report.ID, Format: "md"}); !IsCode(err, ErrExportExists) {
		t.Fatalf("second Export = %v, want ErrExportExists", err)
	}

	// Overwrite replaces atomically and leaves no temp files behind.
	updated, err := env.svc.UpdateContent(env.ctx, UpdateContentInput{
		ReportID: report.ID, ContentMD: "# Replaced\n\nnew body\n",
		ExpectedHash: report.ContentHash, Actor: ActorUser,
	})
	if err != nil {
		t.Fatalf("UpdateContent: %v", err)
	}
	_, err = env.svc.Export(env.ctx, ExportInput{
		ReportID: updated.ID, Format: "md", Overwrite: true,
	})
	if err != nil {
		t.Fatalf("overwrite Export: %v", err)
	}
	raw, err = os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("read overwritten export: %v", err)
	}
	if !strings.HasSuffix(string(raw), "# Replaced\n\nnew body\n") {
		t.Fatalf("overwrite did not replace the body:\n%s", raw)
	}
	entries, err := os.ReadDir(filepath.Dir(result.Path))
	if err != nil {
		t.Fatalf("read export dir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".part") {
			t.Fatalf("temporary export file left behind: %s", entry.Name())
		}
	}
}

func TestAC19DraftFrontMatter(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	report := env.newReport(period, true)

	result, err := env.svc.Export(env.ctx, ExportInput{ReportID: report.ID, Format: "md"})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	raw, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	if !strings.Contains(string(raw), `status: "draft"`) {
		t.Fatalf("draft status missing from front matter:\n%s", raw)
	}

	// FrontMatter=false emits the body only.
	dir := filepath.Join(env.ws.Root(), "exports", "plain")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	noFront := false
	plain, err := env.svc.Export(env.ctx, ExportInput{
		ReportID: report.ID, Format: "md", Dir: dir, FrontMatter: &noFront, Overwrite: true,
	})
	if err != nil {
		t.Fatalf("Export plain: %v", err)
	}
	raw, err = os.ReadFile(plain.Path)
	if err != nil {
		t.Fatalf("read plain export: %v", err)
	}
	if strings.HasPrefix(string(raw), "---") {
		t.Fatalf("FrontMatter=false still wrote a header:\n%s", raw)
	}
	if string(raw) != report.ContentMD {
		t.Fatalf("plain export body mismatch")
	}
}

func TestExportPathGuards(t *testing.T) {
	loc := mustLocation(t, "Asia/Shanghai")
	env := newTestEnv(t, envConfig{
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, loc),
		settings: map[string]string{"weekly.timezone": `"Asia/Shanghai"`},
	})
	period := periodOf(t, loc, "2026-11-02", "2026-11-08")
	report := env.newReport(period, true)

	if _, err := env.svc.Export(env.ctx, ExportInput{
		ReportID: report.ID, Format: "pdf",
	}); !IsCode(err, ErrUnsupportedFormat) {
		t.Fatalf("pdf export = %v, want ErrUnsupportedFormat", err)
	}
	if _, err := env.svc.Export(env.ctx, ExportInput{
		ReportID: report.ID, Format: "md", Dir: "../escape",
	}); !IsCode(err, ErrInvalidExportPath) {
		t.Fatalf("relative dir = %v, want ErrInvalidExportPath", err)
	}
	escape := filepath.Join(env.ws.Root(), "..", "escape")
	if _, err := env.svc.Export(env.ctx, ExportInput{
		ReportID: report.ID, Format: "md", Dir: escape,
	}); !IsCode(err, ErrInvalidExportPath) {
		t.Fatalf("escaping dir = %v, want ErrInvalidExportPath", err)
	}
	outside := t.TempDir()
	if _, err := env.svc.Export(env.ctx, ExportInput{
		ReportID: report.ID, Format: "md", Dir: outside,
	}); !IsCode(err, ErrInvalidExportPath) {
		t.Fatalf("outside workspace dir = %v, want ErrInvalidExportPath", err)
	}
	missing := filepath.Join(env.ws.Root(), "exports", "deep", "nested")
	if _, err := env.svc.Export(env.ctx, ExportInput{
		ReportID: report.ID, Format: "md", Dir: missing,
	}); !IsCode(err, ErrExportDirMissing) {
		t.Fatalf("missing parent dir = %v, want ErrExportDirMissing", err)
	}
}
