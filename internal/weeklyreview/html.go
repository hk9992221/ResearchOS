package weeklyreview

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
)

// renderReportHTML builds the standalone HTML export: one self-contained file
// with the report body rendered from the Markdown subset the template writes.
// No external CSS, fonts or scripts, so the file opens offline and prints.
func renderReportHTML(report *WeeklyReport, info projectInfo, frontMatter bool, now time.Time) []byte {
	title := fmt.Sprintf("%s 周报", info.Name)
	period := fmt.Sprintf("%s – %s", report.Period.StartDate(), report.Period.EndDate())
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"zh-CN\">\n<head>\n")
	b.WriteString("<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	fmt.Fprintf(&b, "<title>%s · %s</title>\n", html.EscapeString(title), html.EscapeString(period))
	b.WriteString("<style>\n")
	b.WriteString(reportCSS)
	b.WriteString("</style>\n</head>\n<body>\n<main class=\"report\">\n")

	b.WriteString("<header class=\"report-head\">\n")
	b.WriteString("<p class=\"eyebrow\">Weekly Report</p>\n")
	fmt.Fprintf(&b, "<h1>%s</h1>\n", html.EscapeString(info.Name))
	fmt.Fprintf(&b, "<p class=\"period\">%s<span class=\"dot\">·</span>%d 天</p>\n",
		html.EscapeString(period), report.Period.Days())
	if frontMatter {
		status := "草稿"
		if report.Status == StatusConfirmed {
			status = "已确认"
		}
		fmt.Fprintf(&b, "<p class=\"meta\"><span class=\"pill %s\">%s</span>", report.Status, status)
		if report.ConfirmedAt != nil {
			fmt.Fprintf(&b, "<span>确认于 %s</span>", report.ConfirmedAt.Local().Format("2006-01-02 15:04"))
		}
		if report.Meta.Generation != nil && report.Meta.Generation.Mode == "ai" {
			b.WriteString("<span>AI 初稿</span>")
		}
		b.WriteString("</p>\n")
	}
	b.WriteString("</header>\n")

	b.WriteString("<article class=\"report-body\">\n")
	b.WriteString(renderMarkdownHTML(report.ContentMD))
	b.WriteString("</article>\n")

	b.WriteString("<footer class=\"report-foot\">")
	fmt.Fprintf(&b, "<span>ResearchOS · %s 导出</span>", html.EscapeString(now.Local().Format("2006-01-02 15:04")))
	fmt.Fprintf(&b, "<span>%s</span>", html.EscapeString(info.Name))
	b.WriteString("</footer>\n</main>\n</body>\n</html>\n")
	return []byte(b.String())
}

var (
	htmlRefToken   = regexp.MustCompile(`\[\[ref:([a-z_]+):([0-9a-fA-F-]{36})\]\]`)
	htmlLink       = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	htmlBold       = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	htmlItalic     = regexp.MustCompile(`\*([^*\n]+)\*`)
	htmlCodeSpan   = regexp.MustCompile("`([^`]+)`")
	htmlOrdered    = regexp.MustCompile(`^([0-9]{1,3})[.)]\s+(.*)$`)
	htmlBulletLine = regexp.MustCompile(`^[-*+]\s+(.*)$`)
)

// renderMarkdownHTML renders the subset of Markdown the report template emits:
// ATX headings, bullets (one nesting level), ordered lists, quotes, fenced
// code, rules and paragraphs. The single H1 is dropped because the header
// already carries the project name and period.
func renderMarkdownHTML(markdown string) string {
	var b strings.Builder
	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")
	var (
		inList  bool
		listTag string
		inCode  bool
		para    []string
		quote   []string
	)
	closeList := func() {
		if inList {
			fmt.Fprintf(&b, "</%s>\n", listTag)
			inList = false
			listTag = ""
		}
	}
	flushQuote := func() {
		if len(quote) == 0 {
			return
		}
		b.WriteString("<blockquote><p>")
		b.WriteString(inlineHTML(strings.Join(quote, " ")))
		b.WriteString("</p></blockquote>\n")
		quote = quote[:0]
	}
	flushPara := func() {
		if len(para) == 0 {
			return
		}
		b.WriteString("<p>")
		b.WriteString(inlineHTML(strings.Join(para, " ")))
		b.WriteString("</p>\n")
		para = para[:0]
	}

	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)
		if inCode {
			if strings.HasPrefix(trimmed, "```") {
				b.WriteString("</code></pre>\n")
				inCode = false
				continue
			}
			b.WriteString(html.EscapeString(raw))
			b.WriteByte('\n')
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			flushPara()
			closeList()
			flushQuote()
			b.WriteString("<pre><code>")
			inCode = true
			continue
		}
		if trimmed == "" {
			flushPara()
			closeList()
			flushQuote()
			continue
		}
		if level, text, ok := headingLine(trimmed); ok {
			flushPara()
			closeList()
			flushQuote()
			if level == 1 {
				// The page header repeats the project and the period.
				continue
			}
			fmt.Fprintf(&b, "<h%d>%s</h%d>\n", level, inlineHTML(text), level)
			continue
		}
		if trimmed == "---" || trimmed == "***" {
			flushPara()
			closeList()
			flushQuote()
			b.WriteString("<hr>\n")
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			flushPara()
			closeList()
			quote = append(quote, strings.TrimSpace(strings.TrimPrefix(trimmed, ">")))
			continue
		}
		if m := htmlBulletLine.FindStringSubmatch(trimmed); m != nil {
			flushPara()
			flushQuote()
			if !inList || listTag != "ul" {
				closeList()
				listTag = "ul"
				inList = true
				b.WriteString("<ul>\n")
			}
			// An indented bullet is one level deeper; the stylesheet draws
			// that level, the markup stays a single list.
			class := ""
			if len(line)-len(strings.TrimLeft(line, " \t")) >= 2 {
				class = ` class="nested"`
			}
			b.WriteString("<li" + class + ">" + inlineHTML(m[1]) + "</li>\n")
			continue
		}
		if m := htmlOrdered.FindStringSubmatch(trimmed); m != nil {
			flushPara()
			flushQuote()
			if !inList || listTag != "ol" {
				closeList()
				listTag = "ol"
				inList = true
				b.WriteString("<ol>\n")
			}
			b.WriteString("<li>" + inlineHTML(m[2]) + "</li>\n")
			continue
		}
		flushQuote()
		para = append(para, trimmed)
	}
	if inCode {
		b.WriteString("</code></pre>\n")
	}
	flushPara()
	closeList()
	flushQuote()
	return b.String()
}

// headingLine reports an ATX heading and its text.
func headingLine(line string) (int, string, bool) {
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level >= len(line) || line[level] != ' ' {
		return 0, "", false
	}
	return level, strings.TrimSpace(line[level+1:]), true
}

// inlineHTML escapes one line and applies the inline Markdown of the
// template: reference tokens, links, bold, italic and code spans.
func inlineHTML(text string) string {
	out := html.EscapeString(text)
	out = htmlRefToken.ReplaceAllStringFunc(out, func(match string) string {
		sub := htmlRefToken.FindStringSubmatch(match)
		short := strings.ReplaceAll(sub[2], "-", "")
		if len(short) > 8 {
			short = short[:8]
		}
		return fmt.Sprintf("<span class=\"ref\" title=\"%s:%s\">%s</span>", sub[1], sub[2], sub[1])
	})
	out = htmlLink.ReplaceAllStringFunc(out, func(match string) string {
		sub := htmlLink.FindStringSubmatch(match)
		if !safeLink(sub[2]) {
			return sub[1]
		}
		return fmt.Sprintf("<a href=\"%s\">%s</a>", sub[2], sub[1])
	})
	out = htmlBold.ReplaceAllString(out, "<strong>$1</strong>")
	out = htmlItalic.ReplaceAllString(out, "<em>$1</em>")
	out = htmlCodeSpan.ReplaceAllString(out, "<code>$1</code>")
	return out
}

// safeLink admits the schemes a report may reference.
func safeLink(url string) bool {
	lower := strings.ToLower(url)
	return strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "mailto:") ||
		strings.HasPrefix(lower, "#")
}

// reportCSS is the export's whole stylesheet: a reading column, one card per
// section, and a print layout without the page chrome.
const reportCSS = `:root {
  color-scheme: light dark;
  --fg: #1b1f24;
  --muted: #5b6470;
  --border: #e3e7ec;
  --surface: #ffffff;
  --accent: #3b6ef0;
  --accent-soft: #eef3ff;
  --page: #f6f7f9;
}
@media (prefers-color-scheme: dark) {
  :root {
    --fg: #e6e9ee;
    --muted: #9aa4b2;
    --border: #2b3138;
    --surface: #171a1e;
    --accent: #7aa2ff;
    --accent-soft: #1d2430;
    --page: #101215;
  }
}
* { box-sizing: border-box; }
body {
  margin: 0;
  padding: 48px 20px 64px;
  background: var(--page);
  color: var(--fg);
  font: 15px/1.7 "Inter", "Segoe UI", "PingFang SC", "Microsoft YaHei", system-ui, sans-serif;
  -webkit-font-smoothing: antialiased;
}
.report { max-width: 820px; margin: 0 auto; }
.report-head { padding: 0 4px 24px; border-bottom: 1px solid var(--border); }
.eyebrow {
  margin: 0 0 6px;
  font-size: 12px;
  letter-spacing: .14em;
  text-transform: uppercase;
  color: var(--accent);
}
h1 { margin: 0 0 8px; font-size: 30px; line-height: 1.25; letter-spacing: -.01em; }
.period { margin: 0; color: var(--muted); font-variant-numeric: tabular-nums; }
.dot { margin: 0 8px; opacity: .55; }
.meta { display: flex; flex-wrap: wrap; gap: 8px 16px; align-items: center; margin: 14px 0 0; color: var(--muted); font-size: 13px; }
.pill { padding: 2px 10px; border-radius: 999px; border: 1px solid var(--border); font-size: 12px; }
.pill.confirmed { color: #1a7f4b; border-color: #b7e2c9; background: #ecf8f1; }
.pill.draft { color: #9a5b00; border-color: #f0d9ae; background: #fdf4e3; }
@media (prefers-color-scheme: dark) {
  .pill.confirmed { color: #6fd39b; border-color: #1f4a35; background: #11251b; }
  .pill.draft { color: #e0ab5c; border-color: #4a3a1c; background: #241d10; }
}
.report-body { display: block; margin-top: 8px; }
.report-body h2 {
  margin: 34px 0 12px;
  padding: 14px 18px;
  font-size: 17px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-left: 3px solid var(--accent);
  border-radius: 10px;
}
.report-body h3 { margin: 22px 0 8px; font-size: 15px; }
.report-body p, .report-body ul, .report-body ol, .report-body blockquote, .report-body pre {
  margin: 0 0 10px;
}
.report-body ul, .report-body ol { padding-left: 22px; }
.report-body li { margin: 4px 0; }
.report-body li.nested { margin-left: 18px; color: var(--muted); }
.report-body strong { color: var(--fg); }
.report-body a { color: var(--accent); text-decoration: none; border-bottom: 1px solid color-mix(in srgb, var(--accent) 40%, transparent); }
.report-body code {
  padding: 1px 5px;
  border-radius: 5px;
  background: var(--accent-soft);
  font-family: "SFMono-Regular", ui-monospace, "Consolas", monospace;
  font-size: 13px;
}
.report-body pre {
  padding: 14px 16px;
  overflow: auto;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 10px;
}
.report-body pre code { background: none; padding: 0; }
.report-body blockquote {
  padding: 10px 16px;
  color: var(--muted);
  border-left: 3px solid var(--border);
  background: var(--surface);
}
.report-body hr { margin: 26px 0; border: 0; border-top: 1px solid var(--border); }
.ref {
  padding: 0 5px;
  border-radius: 4px;
  font-size: 11px;
  letter-spacing: .02em;
  color: var(--muted);
  background: var(--accent-soft);
  white-space: nowrap;
}
.report-foot {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  margin-top: 44px;
  padding-top: 16px;
  border-top: 1px solid var(--border);
  color: var(--muted);
  font-size: 12px;
}
@media print {
  body { padding: 0; background: #fff; color: #000; }
  .report { max-width: none; }
  .report-body h2 { break-after: avoid; page-break-inside: avoid; }
  .report-body li { break-inside: avoid; }
  .report-foot { display: none; }
}
`
