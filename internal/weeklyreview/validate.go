package weeklyreview

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var (
	refPattern    = regexp.MustCompile(`\[\[ref:([a-z_]+):([0-9a-f-]{36})\]\]`)
	numberPattern = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?%?`)
	claimWords    = []string{"表明", "证明", "发现", "显示", "提升", "改进", "显著", "成功", "有效", "证实", "结果表明"}
)

type parsedReport struct {
	sections map[SectionKey][]string
	order    []SectionKey
	unknown  []string
}

// Validate is the deterministic SPEC-09 §5.4 checker. It is a pure function:
// the only inputs are the content, the template and the digest.
func Validate(contentMD string, tpl Template, digest *ActivityDigest) []Violation {
	parsed := parseReport(contentMD)
	var out []Violation

	// V6 structure first so a malformed draft is always visible.
	out = append(out, structureViolations(parsed, tpl)...)

	seenSections := map[SectionKey]bool{}
	for _, sec := range parsed.order {
		seenSections[sec] = true
	}
	for _, sec := range tpl.Sections {
		if !seenSections[sec.Key] {
			out = append(out, Violation{
				Rule: "V6", Severity: SeverityWarning,
				Fingerprint: fingerprint("V6", sec.Key, "", "missing"),
				Section:     sec.Key,
				Message:     fmt.Sprintf("缺少章节「%d. %s」", sec.Num, sec.Title),
			})
		}
	}

	if digest != nil {
		out = append(out, violationV1(parsed, digest)...)
		out = append(out, violationV3(parsed, digest)...)
		out = append(out, violationV5(parsed, digest)...)
		out = append(out, violationV4(parsed, digest)...)
		out = append(out, violationV2(parsed, digest)...)
	}
	return out
}

func structureViolations(parsed parsedReport, tpl Template) []Violation {
	var out []Violation
	expectedOrder := make(map[SectionKey]int, len(tpl.Sections))
	for i, sec := range tpl.Sections {
		expectedOrder[sec.Key] = i
	}
	last := -1
	for _, key := range parsed.order {
		idx, ok := expectedOrder[key]
		if !ok {
			continue
		}
		if idx < last {
			out = append(out, Violation{
				Rule: "V6", Severity: SeverityWarning,
				Fingerprint: fingerprint("V6", key, "", "order"),
				Section:     key,
				Message:     "章节顺序与默认七节结构不一致",
			})
			break
		}
		last = idx
	}
	for _, name := range parsed.unknown {
		out = append(out, Violation{
			Rule: "V6", Severity: SeverityWarning,
			Fingerprint: fingerprint("V6", "", "", name),
			Section:     "",
			Message:     fmt.Sprintf("未识别的章节标题「%s」", name),
		})
	}
	return out
}

func parseReport(content string) parsedReport {
	p := parsedReport{sections: map[SectionKey][]string{}}
	var current SectionKey
	started := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "##") && !strings.HasPrefix(trimmed, "###") {
			key, ok := normalizeHeading(trimmed)
			if !ok {
				p.unknown = append(p.unknown, strings.TrimSpace(strings.TrimPrefix(trimmed, "##")))
				current = ""
				continue
			}
			if !started || !containsSection(p.order, key) {
				p.order = append(p.order, key)
			}
			started = true
			current = key
			continue
		}
		if current != "" {
			p.sections[current] = append(p.sections[current], line)
		}
	}
	return p
}

func containsSection(list []SectionKey, key SectionKey) bool {
	for _, k := range list {
		if k == key {
			return true
		}
	}
	return false
}

func violationV1(parsed parsedReport, digest *ActivityDigest) []Violation {
	var out []Violation
	for _, task := range digest.Planned {
		title := normalizeForMatch(task.Title)
		if len([]rune(title)) < 4 {
			continue
		}
		for _, sec := range parsed.order {
			if sec == SectionNextWeek {
				continue
			}
			for _, line := range parsed.sections[sec] {
				if strings.Contains(normalizeForMatch(line), title) {
					ref := task.Ref
					out = append(out, Violation{
						Rule: "V1", Severity: SeverityError,
						Fingerprint: fingerprint("V1", sec, ref.ID, title),
						Section:     sec,
						Ref:         &ref,
						Message:     fmt.Sprintf("计划任务「%s」出现在「%s」中", task.Title, sectionTitle(sec)),
					})
					break
				}
			}
		}
	}
	return out
}

func violationV2(parsed parsedReport, digest *ActivityDigest) []Violation {
	allowed := allowedNumbers(digest)
	var out []Violation
	for _, sec := range parsed.order {
		reported := map[string]bool{}
		for _, line := range parsed.sections[sec] {
			if strings.HasPrefix(strings.TrimSpace(line), "##") {
				continue
			}
			// Reference tokens carry UUIDs; they are identifiers, not
			// reported measurements (SPEC-09 §5.4 V2).
			scanned := refPattern.ReplaceAllString(line, "")
			for _, tok := range numberPattern.FindAllString(scanned, -1) {
				canon := canonNumber(tok)
				if allowed[canon] || reported[canon] || ignorableNumber(tok) {
					continue
				}
				reported[canon] = true
				out = append(out, Violation{
					Rule: "V2", Severity: SeverityError,
					Fingerprint: fingerprint("V2", sec, "", tok),
					Section:     sec,
					Message:     fmt.Sprintf("数值「%s」在活动汇总中无来源", tok),
				})
			}
		}
	}
	return out
}

func allowedNumbers(digest *ActivityDigest) map[string]bool {
	allowed := map[string]bool{}
	add := func(s string) {
		for _, tok := range numberPattern.FindAllString(s, -1) {
			allowed[canonNumber(tok)] = true
		}
	}
	add(digest.Period.StartDate())
	add(digest.Period.EndDate())
	for _, sec := range digest.Sections {
		for _, item := range sec.Items {
			add(item.Summary)
			add(item.Detail)
			add(item.Old)
			add(item.New)
		}
	}
	for _, t := range digest.Blocked {
		add(t.Title)
		add(t.ColumnName)
	}
	for _, t := range digest.Planned {
		add(t.Title)
		add(t.ColumnName)
		if t.Deadline != nil {
			add(t.Deadline.Format(dateLayout))
		}
		if t.WeekPlan != nil {
			add(*t.WeekPlan)
		}
	}
	return allowed
}

// canonNumber normalizes a numeric token so "08", "8" and "8%" compare equal.
func canonNumber(tok string) string {
	raw := strings.TrimSuffix(tok, "%")
	if strings.Contains(raw, ".") {
		raw = strings.TrimRight(strings.TrimRight(raw, "0"), ".")
	}
	raw = strings.TrimLeft(raw, "0")
	if raw == "" {
		raw = "0"
	}
	return raw
}

func ignorableNumber(tok string) bool {
	raw := strings.TrimSuffix(tok, "%")
	if n, err := strconv.Atoi(raw); err == nil && n >= 1 && n <= 7 && raw == tok {
		return true // section/list numbering
	}
	if len(raw) == 4 {
		if n, err := strconv.Atoi(raw); err == nil && n >= 1900 && n <= 2100 {
			return true // years
		}
	}
	return false
}

func violationV3(parsed parsedReport, digest *ActivityDigest) []Violation {
	unconfirmed := map[string]bool{}
	for _, sec := range digest.Sections {
		for _, item := range sec.Items {
			if item.UnconfirmedSource {
				unconfirmed[item.Ref.Type+":"+item.Ref.ID] = true
			}
		}
	}
	var out []Violation
	for _, sec := range []SectionKey{SectionLiterature, SectionTheory, SectionExperiment, SectionIdea} {
		for _, line := range parsed.sections[sec] {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "-") {
				continue
			}
			if strings.Contains(trimmed, "待确认") {
				continue
			}
			refs := refPattern.FindAllStringSubmatch(trimmed, -1)
			hasUnconfirmed := false
			for _, m := range refs {
				if unconfirmed[m[1]+":"+m[2]] {
					hasUnconfirmed = true
				}
			}
			if hasUnconfirmed {
				out = append(out, Violation{
					Rule: "V3", Severity: SeverityError,
					Fingerprint: fingerprint("V3", sec, "", normalizeForMatch(trimmed)),
					Section:     sec,
					Ref:         firstRef(refs),
					Message:     "引用了未确认的 AI 结论，但未标注「（待确认）」",
				})
				continue
			}
			if len(refs) == 0 && containsClaim(trimmed) {
				out = append(out, Violation{
					Rule: "V3", Severity: SeverityError,
					Fingerprint: fingerprint("V3", sec, "", normalizeForMatch(trimmed)),
					Section:     sec,
					Message:     "成果性表述缺少来源引用，且未标注「（待确认）」",
				})
			}
		}
	}
	return out
}

func firstRef(matches [][]string) *EntityRef {
	if len(matches) == 0 {
		return nil
	}
	return &EntityRef{Type: matches[0][1], ID: matches[0][2]}
}

func containsClaim(line string) bool {
	for _, word := range claimWords {
		if strings.Contains(line, word) {
			return true
		}
	}
	return false
}

func violationV4(parsed parsedReport, digest *ActivityDigest) []Violation {
	known := map[string]bool{}
	for _, sec := range digest.Sections {
		for _, item := range sec.Items {
			known[item.Ref.Type+":"+item.Ref.ID] = true
		}
	}
	for _, t := range digest.Blocked {
		known[t.Ref.Type+":"+t.Ref.ID] = true
	}
	for _, t := range digest.Planned {
		known[t.Ref.Type+":"+t.Ref.ID] = true
	}
	// Filing actions stay out of the body, but a report may still cite the
	// entity they recorded (a paper imported this week, say). Known, so the
	// reference is not reported as broken.
	for _, item := range digest.Housekeeping {
		known[item.Ref.Type+":"+item.Ref.ID] = true
	}
	var out []Violation
	for _, sec := range parsed.order {
		for _, line := range parsed.sections[sec] {
			for _, m := range refPattern.FindAllStringSubmatch(line, -1) {
				if known[m[1]+":"+m[2]] {
					continue
				}
				ref := EntityRef{Type: m[1], ID: m[2]}
				out = append(out, Violation{
					Rule: "V4", Severity: SeverityWarning,
					Fingerprint: fingerprint("V4", sec, m[2], m[1]),
					Section:     sec,
					Ref:         &ref,
					Message:     "引用不在本周期活动汇总中，可能已失效",
				})
			}
		}
	}
	return out
}

func violationV5(parsed parsedReport, digest *ActivityDigest) []Violation {
	planned := map[string]bool{}
	titles := make([]string, 0, len(digest.Planned))
	for _, t := range digest.Planned {
		planned[t.Ref.ID] = true
		if title := normalizeForMatch(t.Title); len([]rune(title)) >= 4 {
			titles = append(titles, title)
		}
	}
	var out []Violation
	for _, line := range parsed.sections[SectionNextWeek] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, "本周期无记录") {
			continue
		}
		matches := refPattern.FindAllStringSubmatch(trimmed, -1)
		if len(matches) == 0 {
			// A candidate task named by title (without a ref) is grounded;
			// anything else is a new plan entry without a candidate (§5.4 V5).
			normalized := normalizeForMatch(trimmed)
			grounded := false
			for _, title := range titles {
				if strings.Contains(normalized, title) {
					grounded = true
					break
				}
			}
			if grounded {
				continue
			}
			out = append(out, Violation{
				Rule: "V5", Severity: SeverityError,
				Fingerprint: fingerprint("V5", SectionNextWeek, "", normalized),
				Section:     SectionNextWeek,
				Message:     "下周计划条目缺少候选任务引用",
			})
			continue
		}
		valid := false
		for _, m := range matches {
			if m[1] == "task" && planned[m[2]] {
				valid = true
				break
			}
		}
		if valid {
			continue
		}
		ref := firstRef(matches)
		out = append(out, Violation{
			Rule: "V5", Severity: SeverityError,
			Fingerprint: fingerprint("V5", SectionNextWeek, refID(matches), normalizeForMatch(trimmed)),
			Section:     SectionNextWeek,
			Ref:         ref,
			Message:     "下周计划包含不在候选中的任务",
		})
	}
	return out
}

// refID returns the first task reference id, or empty for non-task refs.
func refID(matches [][]string) string {
	for _, m := range matches {
		if m[1] == "task" {
			return m[2]
		}
	}
	return ""
}

func fingerprint(rule string, section SectionKey, refID, text string) string {
	sum := sha256.Sum256([]byte(rule + "|" + string(section) + "|" + refID + "|" + text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func normalizeForMatch(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsSpace(r) {
			continue
		}
		switch r {
		case '（':
			r = '('
		case '）':
			r = ')'
		case '，':
			r = ','
		case '。':
			r = '.'
		case '：':
			r = ':'
		}
		b.WriteRune(r)
	}
	return b.String()
}
