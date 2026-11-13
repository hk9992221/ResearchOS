package weeklyreview

import (
	"fmt"
	"strings"
)

// defaultTemplate is the fixed default-7 structure (SPEC-09 §5.5).
var defaultTemplate = Template{
	ID:      defaultReportTmpl,
	Version: reportTemplateVer,
	Sections: []TemplateSection{
		{Key: SectionWork, Title: "本周工作内容", Num: 1},
		{Key: SectionLiterature, Title: "文献调研进展", Num: 2},
		{Key: SectionTheory, Title: "理论研究进展", Num: 3},
		{Key: SectionExperiment, Title: "实验工作与结果", Num: 4},
		{Key: SectionIdea, Title: "Idea 进展", Num: 5},
		{Key: SectionProblem, Title: "遇到的问题", Num: 6},
		{Key: SectionNextWeek, Title: "下周计划", Num: 7},
	},
}

// DefaultTemplate returns the v1 template.
func DefaultTemplate() Template { return defaultTemplate }

func sectionTitle(key SectionKey) string {
	for _, sec := range defaultTemplate.Sections {
		if sec.Key == key {
			return sec.Title
		}
	}
	return string(key)
}

func templateSection(key SectionKey) (TemplateSection, bool) {
	for _, sec := range defaultTemplate.Sections {
		if sec.Key == key {
			return sec, true
		}
	}
	return TemplateSection{}, false
}

// BuildSkeleton renders the deterministic seven-section report. A nil digest
// yields an empty skeleton; entries come only from digest facts and planned
// candidates (never invented).
func BuildSkeleton(projectName string, p Period, digest *ActivityDigest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Weekly Research Report — %s（%s – %s）\n\n",
		projectName, p.StartDate(), p.EndDate())
	for _, sec := range defaultTemplate.Sections {
		fmt.Fprintf(&b, "## %d. %s\n", sec.Num, sec.Title)
		lines := skeletonLines(sec.Key, p, digest)
		if len(lines) == 0 {
			lines = []string{"- 本周期无记录"}
		}
		for _, line := range lines {
			b.WriteString(line)
			b.WriteByte('\n')
		}
		if sec.Num != len(defaultTemplate.Sections) {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func skeletonLines(key SectionKey, p Period, digest *ActivityDigest) []string {
	if digest == nil {
		return nil
	}
	var lines []string
	switch key {
	case SectionProblem:
		for _, task := range digest.Blocked {
			lines = append(lines, fmt.Sprintf("- 受阻任务：%s（列：%s；原因未记录）[[ref:task:%s]]",
				task.Title, task.ColumnName, task.Ref.ID))
		}
		for _, item := range sectionItems(digest, SectionProblem) {
			lines = append(lines, renderDigestItem(item, p))
		}
		return lines
	case SectionNextWeek:
		for _, task := range digest.Planned {
			lines = append(lines, fmt.Sprintf("- %s（来源：%s）[[ref:task:%s]]", task.Title, originLabel(task.Origin), task.Ref.ID))
		}
		return lines
	}
	for _, item := range sectionItems(digest, key) {
		lines = append(lines, renderDigestItem(item, p))
	}
	return lines
}

func sectionItems(d *ActivityDigest, key SectionKey) []DigestItem {
	for _, sec := range d.Sections {
		if sec.Key == key {
			return sec.Items
		}
	}
	return nil
}

func renderDigestItem(item DigestItem, p Period) string {
	when := item.FirstAt.In(p.Start.Location()).Format("01-02 15:04")
	summary := strings.TrimSpace(item.Summary)
	if summary == "" {
		summary = "活动记录"
	}
	line := fmt.Sprintf("- %s（%s）", summary, when)
	if item.Detail != "" && item.Detail != summary {
		line = fmt.Sprintf("- %s（%s；%s）", summary, when, item.Detail)
	}
	if item.Ref.Type != "" && item.Ref.ID != "" {
		line += fmt.Sprintf("[[ref:%s:%s]]", item.Ref.Type, item.Ref.ID)
	}
	return line
}

func originLabel(origin string) string {
	switch origin {
	case "week_plan":
		return "周计划"
	case "deadline":
		return "截止日"
	case "carry_over":
		return "本周计划顺延"
	default:
		return origin
	}
}

// normalizeHeading recognizes "## 1. 本周工作内容", "## 1、本周工作内容",
// "## 本周工作内容" and full-width bracket variants.
func normalizeHeading(line string) (SectionKey, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "##") || strings.HasPrefix(line, "###") {
		return "", false
	}
	text := strings.TrimSpace(strings.TrimPrefix(line, "##"))
	text = strings.TrimSpace(strings.TrimLeft(text, "0123456789"))
	text = strings.TrimLeft(text, ".、,，:：)）]】")
	text = strings.TrimSpace(text)
	text = strings.NewReplacer("（", "(", "）", ")").Replace(text)
	for _, sec := range defaultTemplate.Sections {
		if text == sec.Title {
			return sec.Key, true
		}
	}
	return "", false
}
