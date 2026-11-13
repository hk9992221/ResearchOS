package kanban

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// validateTitle trims and checks the TASK-01 title rules (>0 after trim,
// ≤200 characters, matching the tasks.title CHECK on top of the service
// limit). It returns the trimmed value.
func validateTitle(title string) (string, error) {
	t := strings.TrimSpace(title)
	if t == "" {
		return "", fmt.Errorf("%w: title is empty", ErrInvalidInput)
	}
	if utf8.RuneCountInString(t) > maxTitleChars {
		return "", fmt.Errorf("%w: title exceeds %d characters", ErrInvalidInput, maxTitleChars)
	}
	return t, nil
}

// validateDescription checks the 100k application-level cap (SPEC-08 §4.2).
func validateDescription(md string) error {
	if utf8.RuneCountInString(md) > maxDescriptionRunes {
		return fmt.Errorf("%w: description exceeds %d characters", ErrInvalidInput, maxDescriptionRunes)
	}
	return nil
}

// validPriorityInput accepts the empty string as none.
func validPriorityInput(p Priority) error {
	if !p.valid() {
		return fmt.Errorf("%w: priority %q", ErrInvalidInput, string(p))
	}
	return nil
}

// deadlineLayout is the local-date storage format (TASK-05).
const deadlineLayout = "2006-01-02"

var deadlinePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// validateDeadline accepts nil (no deadline) or a real YYYY-MM-DD date. Past
// dates are legal: overdue is a display state only (TASK-05).
func validateDeadline(d *string) error {
	if d == nil {
		return nil
	}
	if !deadlinePattern.MatchString(*d) {
		return fmt.Errorf("%w: deadline %q is not YYYY-MM-DD", ErrInvalidInput, *d)
	}
	if _, err := time.Parse(deadlineLayout, *d); err != nil {
		return fmt.Errorf("%w: deadline %q is not a real date", ErrInvalidInput, *d)
	}
	return nil
}

var weekPattern = regexp.MustCompile(`^(\d{4})-W(\d{2})$`)

// validateWeekPlan accepts nil or a real ISO week `YYYY-Www` (Monday start,
// SPEC-08 §4.3). Week 53 exists only in long ISO years.
func validateWeekPlan(w *string) error {
	if w == nil {
		return nil
	}
	m := weekPattern.FindStringSubmatch(*w)
	if m == nil {
		return fmt.Errorf("%w: week_plan %q is not YYYY-Www", ErrInvalidInput, *w)
	}
	year, _ := strconv.Atoi(m[1])
	week, _ := strconv.Atoi(m[2])
	if week < 1 || week > isoWeeksInYear(year) {
		return fmt.Errorf("%w: week_plan %q is not a real ISO week", ErrInvalidInput, *w)
	}
	return nil
}

// isoWeeksInYear returns 53 when Jan 1 falls on a Thursday or (leap year and
// Wednesday), otherwise 52.
func isoWeeksInYear(year int) int {
	t := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	leap := year%4 == 0 && (year%100 != 0 || year%400 == 0)
	if t.Weekday() == time.Thursday || (t.Weekday() == time.Wednesday && leap) {
		return 53
	}
	return 52
}

// sanitizeLabels trims, rejects empty/oversized members, dedupes
// case-sensitively in order and caps the group size (TASK-08).
func sanitizeLabels(labels []string) ([]string, error) {
	out := make([]string, 0, len(labels))
	seen := make(map[string]bool, len(labels))
	for _, raw := range labels {
		l := strings.TrimSpace(raw)
		if l == "" {
			return nil, fmt.Errorf("%w: label is empty after trim", ErrInvalidInput)
		}
		if utf8.RuneCountInString(l) > maxLabelRunes {
			return nil, fmt.Errorf("%w: label %q exceeds %d characters", ErrInvalidInput, l, maxLabelRunes)
		}
		if seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	if len(out) > maxLabelsPerTask {
		return nil, fmt.Errorf("%w: more than %d labels", ErrInvalidInput, maxLabelsPerTask)
	}
	return out, nil
}

// validateLabel is the single-label form used by LabelService.Add.
func validateLabel(label string) (string, error) {
	l := strings.TrimSpace(label)
	if l == "" {
		return "", fmt.Errorf("%w: label is empty", ErrInvalidInput)
	}
	if utf8.RuneCountInString(l) > maxLabelRunes {
		return "", fmt.Errorf("%w: label %q exceeds %d characters", ErrInvalidInput, l, maxLabelRunes)
	}
	return l, nil
}

// encodeLabels serializes the label array; the empty group is `[]`.
func encodeLabels(labels []string) (string, error) {
	if labels == nil {
		labels = []string{}
	}
	body, err := json.Marshal(labels)
	if err != nil {
		return "", fmt.Errorf("kanban: encode labels: %w", err)
	}
	return string(body), nil
}

// decodeLabels reads tasks.labels; corrupt JSON degrades to the empty group
// (the column is app-owned, but a hand-edited database must stay readable).
func decodeLabels(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []string{}
	}
	if out == nil {
		return []string{}
	}
	return out
}

// validateChecklistText trims and applies the ≤500 character cap (§5.5).
func validateChecklistText(text string) (string, error) {
	t := strings.TrimSpace(text)
	if t == "" {
		return "", fmt.Errorf("%w: checklist text is empty", ErrInvalidInput)
	}
	if utf8.RuneCountInString(t) > maxChecklistRunes {
		return "", fmt.Errorf("%w: checklist text exceeds %d characters", ErrInvalidInput, maxChecklistRunes)
	}
	return t, nil
}

// validateColumnName trims and requires a non-empty column name (§5.4).
func validateColumnName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return "", fmt.Errorf("%w: column name is empty", ErrInvalidInput)
	}
	return n, nil
}

var columnKinds = map[string]bool{
	"backlog": true, "todo": true, "in_progress": true,
	"review": true, "done": true, "custom": true,
}

// normalizeColumnKind defaults the empty kind to custom (TASK-03).
func normalizeColumnKind(kind string) (string, error) {
	if kind == "" {
		return "custom", nil
	}
	if !columnKinds[kind] {
		return "", fmt.Errorf("%w: column kind %q", ErrInvalidInput, kind)
	}
	return kind, nil
}
