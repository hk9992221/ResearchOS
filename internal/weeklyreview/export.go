package weeklyreview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"researchos/internal/foundation/paths"
	"researchos/internal/foundation/settings"
)

// Export writes one report as a Markdown file (SPEC-09 §5.3 Export).
func (s *service) Export(ctx context.Context, in ExportInput) (*ExportResult, error) {
	format := strings.ToLower(strings.TrimSpace(in.Format))
	if format == "" {
		format = "md"
	}
	if format != "md" {
		return nil, serviceError(ErrUnsupportedFormat, "仅支持 Markdown（md）导出")
	}
	if in.Actor != "" {
		if _, err := writeActor(in.Actor); err != nil {
			return nil, err
		}
	}
	h, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	report, err := s.reportByID(ctx, h.DB(), in.ReportID, false)
	if err != nil {
		return nil, err
	}
	info, err := s.projectInfo(ctx)
	if err != nil {
		return nil, err
	}

	frontMatter := true
	if in.FrontMatter != nil {
		frontMatter = *in.FrontMatter
	}
	payload := renderReportDocument(report, info, frontMatter, s.clock.Now().UTC())

	dir, err := s.resolveExportDir(ctx, in.Dir, info.Name)
	if err != nil {
		return nil, err
	}
	base := fmt.Sprintf("%s_%s_weekly-report.md", report.Period.StartDate(), report.Period.EndDate())
	target := filepath.Join(dir, base)
	if _, statErr := os.Lstat(target); statErr == nil && !in.Overwrite {
		return nil, serviceError(ErrExportExists, "目标文件已存在：%s", target)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("weeklyreview: stat export target: %w", statErr)
	}

	if err := atomicWriteFile(dir, base, payload); err != nil {
		return nil, err
	}
	return &ExportResult{
		Path:   target,
		Format: "md",
		Bytes:  int64(len(payload)),
		Status: report.Status,
	}, nil
}

// resolveExportDir returns the directory the report file is written to.
func (s *service) resolveExportDir(ctx context.Context, explicit, projectName string) (string, error) {
	slug := slugify(projectName)
	p := s.ws.Paths()
	if explicit != "" {
		if !filepath.IsAbs(explicit) {
			return "", serviceError(ErrInvalidExportPath, "导出目录必须是绝对路径")
		}
		clean := filepath.Clean(explicit)
		if hasDotDot(clean) || !p.IsInternal(clean) {
			return "", serviceError(ErrInvalidExportPath, "导出目录必须在工作区内")
		}
		info, err := os.Stat(clean)
		switch {
		case err == nil:
			if !info.IsDir() {
				return "", serviceError(ErrExportDirMissing, "导出目标不是目录：%s", clean)
			}
			return clean, nil
		case errors.Is(err, os.ErrNotExist):
			parent := filepath.Dir(clean)
			if pinfo, perr := os.Stat(parent); perr != nil || !pinfo.IsDir() {
				return "", serviceError(ErrExportDirMissing, "导出目录不存在：%s", clean)
			}
			if err := os.Mkdir(clean, paths.DirMode); err != nil {
				return "", serviceError(ErrExportDirMissing, "无法创建导出目录：%s", clean)
			}
			return clean, nil
		default:
			return "", fmt.Errorf("weeklyreview: stat export dir: %w", err)
		}
	}

	base, err := s.defaultExportRoot(ctx)
	if err != nil {
		return "", err
	}
	if slug == "project" {
		slug += "-" + shortID(s.projectID)
	}
	dir := filepath.Join(base, slug)
	if err := os.MkdirAll(dir, paths.DirMode); err != nil {
		return "", serviceError(ErrExportDirMissing, "无法创建导出目录：%s", dir)
	}
	return dir, nil
}

// defaultExportRoot resolves export.dir (project settings first, then the
// workspace store), falling back to <workspace>/exports.
func (s *service) defaultExportRoot(ctx context.Context) (string, error) {
	h, err := s.handle(ctx)
	if err != nil {
		return "", err
	}
	projectStore := settings.NewProject(h.DB()).WithClock(s.clock)
	value, ok, err := settings.ResolvePriority(ctx, projectStore, s.settings, s.projectID, settingExportDir)
	if err != nil {
		return "", fmt.Errorf("weeklyreview: read export.dir: %w", err)
	}
	if ok && strings.TrimSpace(value) != "" {
		dir := unquoteSetting(value)
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(s.ws.Root(), filepath.FromSlash(dir))
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return "", serviceError(ErrExportDirMissing, "导出目录不存在：%s", dir)
		}
		return dir, nil
	}
	return filepath.Join(s.ws.Root(), filepath.FromSlash(s.ws.Paths().ExportsDir())), nil
}

func hasDotDot(clean string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(clean), "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

func shortID(id string) string {
	clean := strings.ReplaceAll(id, "-", "")
	if len(clean) > 8 {
		return clean[:8]
	}
	return clean
}

func slugify(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == ' ' || r == '-' || r == '_' || r == '/' || r == '\\':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "project"
	}
	return out
}

// renderReportDocument builds the exported Markdown document.
func renderReportDocument(report *WeeklyReport, info projectInfo, frontMatter bool, now time.Time) []byte {
	var b strings.Builder
	if frontMatter {
		generatedBy := "manual"
		prompt := ""
		if report.Meta.Generation != nil {
			if report.Meta.Generation.Mode == "ai" {
				generatedBy = "ai"
			}
			prompt = report.Meta.Generation.PromptVersion
		}
		b.WriteString("---\n")
		fmt.Fprintf(&b, "project: %s\n", strconv.Quote(info.Name))
		fmt.Fprintf(&b, "project_id: %s\n", strconv.Quote(info.ID))
		fmt.Fprintf(&b, "period_start: %s\n", strconv.Quote(report.Period.StartDate()))
		fmt.Fprintf(&b, "period_end: %s\n", strconv.Quote(report.Period.EndDate()))
		fmt.Fprintf(&b, "status: %s\n", strconv.Quote(string(report.Status)))
		fmt.Fprintf(&b, "generated_by: %s\n", strconv.Quote(generatedBy))
		if prompt != "" {
			fmt.Fprintf(&b, "prompt_version: %s\n", strconv.Quote(prompt))
		}
		fmt.Fprintf(&b, "exported_at: %s\n", strconv.Quote(now.Format(time.RFC3339)))
		b.WriteString("---\n\n")
	}
	b.WriteString(report.ContentMD)
	if !strings.HasSuffix(report.ContentMD, "\n") {
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// atomicWriteFile writes data to dir/base through a same-directory temp file
// and rename; a cross-device failure falls back to copy+unlink.
func atomicWriteFile(dir, base string, data []byte) error {
	tmp, err := os.CreateTemp(dir, "."+base+".*.part")
	if err != nil {
		return fmt.Errorf("weeklyreview: create temp export: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("weeklyreview: write export: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("weeklyreview: sync export: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("weeklyreview: close export: %w", err)
	}
	target := filepath.Join(dir, base)
	if err := os.Rename(tmpName, target); err != nil {
		// Cross-device fallback: copy then remove the temp file.
		if copyErr := copyFile(tmpName, target); copyErr != nil {
			_ = os.Remove(tmpName)
			return fmt.Errorf("weeklyreview: publish export: %w", err)
		}
		_ = os.Remove(tmpName)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, paths.FileMode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}
