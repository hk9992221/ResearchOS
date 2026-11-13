package experiments

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// Context sampling sizes of §5.5. They are implementation defaults (O-10).
const (
	contextWindowBytes = 32 << 10
	contextSmallFull   = 64 << 10
	contextLogLines    = 100
	contextCSVHeadRows = 200
	contextCSVTailRows = 20
)

// contentPriority orders entries by the §5.5 selection priority: README and
// documentation first, then configuration, metrics, code, data, logs.
func contentPriority(kind FileKind) int {
	switch kind {
	case KindReadme:
		return 0
	case KindConfig:
		return 1
	case KindMetrics:
		return 2
	case KindCode, KindNotebook:
		return 3
	case KindData:
		return 4
	case KindLog:
		return 5
	case KindImage:
		return 6
	default:
		return 7
	}
}

// Build assembles the budgeted context bundle (§5.5). Every file that does not
// enter the bundle is reported in Excluded with its reason.
func (s *Service) Build(ctx context.Context, scan *ScanResult, metrics *MetricSet, budget ContextBudget) (*ContextBundle, error) {
	if scan == nil {
		return nil, fmt.Errorf("%w: nil scan", ErrInvalidInput)
	}
	maxBytes := budget.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxTotalBytes
	}
	bundle := &ContextBundle{
		Entries:   []ContextEntry{},
		MetricSet: metrics,
		Excluded:  []ExcludedEntry{},
	}
	for _, pruned := range scan.Plan.Pruned {
		bundle.Excluded = append(bundle.Excluded, pruned)
	}
	candidates := make([]FileEntry, 0, len(scan.Plan.Entries))
	for _, entry := range scan.Plan.Entries {
		if !entry.ContentEligible {
			reason := ReasonUserExclude
			if entry.ExclusionReason != nil {
				reason = *entry.ExclusionReason
			}
			bundle.Excluded = append(bundle.Excluded, ExcludedEntry{Path: entry.Path, Reason: reason})
			continue
		}
		if entry.Kind == KindImage {
			// Image understanding is not implemented: v1 records metadata only
			// (§3.2, O-13).
			bundle.Excluded = append(bundle.Excluded, ExcludedEntry{Path: entry.Path, Reason: ReasonUserExclude})
			continue
		}
		candidates = append(candidates, entry)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		pi, pj := contentPriority(candidates[i].Kind), contentPriority(candidates[j].Kind)
		if pi != pj {
			return pi < pj
		}
		return candidates[i].Path < candidates[j].Path
	})
	for _, entry := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if bundle.Bytes >= maxBytes {
			bundle.Truncated = true
			bundle.Excluded = append(bundle.Excluded, ExcludedEntry{Path: entry.Path, Reason: ReasonTooLarge})
			continue
		}
		path := filepath.Join(scan.Plan.Root, filepath.FromSlash(entry.Path))
		content, sampled, err := readSample(path, entry)
		if err != nil {
			bundle.Excluded = append(bundle.Excluded, ExcludedEntry{Path: entry.Path, Reason: ReasonUnreadable})
			continue
		}
		remaining := maxBytes - bundle.Bytes
		if int64(len(content)) > remaining {
			if remaining <= 0 {
				bundle.Truncated = true
				bundle.Excluded = append(bundle.Excluded, ExcludedEntry{Path: entry.Path, Reason: ReasonTooLarge})
				continue
			}
			content = truncateUTF8(content, int(remaining))
			sampled = true
			bundle.Truncated = true
		}
		sha := ""
		if entry.SHA256 != nil {
			sha = *entry.SHA256
		}
		bundle.Entries = append(bundle.Entries, ContextEntry{
			Path:     entry.Path,
			Kind:     entry.Kind,
			Bytes:    int64(len(content)),
			SHA256:   sha,
			Sampled:  sampled,
			Content:  content,
			Priority: contentPriority(entry.Kind),
		})
		bundle.Bytes += int64(len(content))
	}
	return bundle, nil
}

// BuildForTarget runs the deterministic pipeline and returns the same bundle a
// PreviewContext call would show (§5.5). It performs no AI call.
func (s *Service) BuildForTarget(ctx context.Context, target TargetRef, opts AnalysisOptions) (*ContextBundle, error) {
	scan, err := s.Run(ctx, target, nil)
	if err != nil {
		return nil, err
	}
	metrics, err := s.ExtractMetrics(ctx, scan)
	if err != nil {
		return nil, err
	}
	budget := ContextBudget{MaxBytes: opts.MaxContextBytes}
	if budget.MaxBytes <= 0 {
		budget.MaxBytes = scanRuleMaxTotal(ctx, s, target)
	}
	return s.Build(ctx, scan, metrics, budget)
}

// scanRuleMaxTotal resolves the effective max_total_bytes of a target.
func scanRuleMaxTotal(ctx context.Context, s *Service, target TargetRef) int64 {
	rules, err := s.rulesForTarget(ctx, target, nil)
	if err != nil {
		return DefaultMaxTotalBytes
	}
	return rules.MaxTotalBytes
}

// readSample reads one file content with the §5.5 sampling rule of its kind.
func readSample(path string, entry FileEntry) (string, bool, error) {
	switch entry.Kind {
	case KindLog:
		return readLineWindow(path, contextLogLines)
	case KindData:
		return readCSVWindow(path, contextCSVHeadRows, contextCSVTailRows)
	default:
		return readByteWindow(path, contextWindowBytes, contextSmallFull)
	}
}

// readByteWindow returns the whole file when small, otherwise head and tail
// windows aligned to UTF-8 boundaries with an explicit omission marker.
func readByteWindow(path string, window int64, small int64) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", false, err
	}
	if info.Size() <= small {
		raw, err := io.ReadAll(io.LimitReader(f, small+1))
		return string(raw), false, err
	}
	head := make([]byte, window)
	if _, err := io.ReadFull(f, head); err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", false, err
	}
	if _, err := f.Seek(-window, io.SeekEnd); err != nil {
		return "", false, err
	}
	tail := make([]byte, window)
	n, err := io.ReadFull(f, tail)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", false, err
	}
	headText := trimPartialRune(string(head), false)
	tailText := trimPartialRune(string(tail[:n]), true)
	return headText + "\n…（中间内容已省略）…\n" + tailText, true, nil
}

// trimPartialRune drops an incomplete UTF-8 sequence at a window edge.
func trimPartialRune(text string, atStart bool) string {
	if atStart {
		for len(text) > 0 && !utf8.ValidString(text) {
			_, size := utf8.DecodeRuneInString(text)
			if size == 0 {
				break
			}
			text = text[size:]
		}
		return text
	}
	for len(text) > 0 && !utf8.ValidString(text) {
		_, size := utf8.DecodeLastRuneInString(text)
		if size == 0 {
			break
		}
		text = text[:len(text)-size]
	}
	return text
}

// truncateUTF8 cuts text to at most limit bytes on a rune boundary.
func truncateUTF8(text string, limit int) string {
	if limit >= len(text) {
		return text
	}
	if limit <= 0 {
		return ""
	}
	cut := text[:limit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// readLineWindow returns the first and last n lines with an omission marker.
func readLineWindow(path string, n int) (string, bool, error) {
	lines, err := readAllLines(path)
	if err != nil {
		return "", false, err
	}
	if len(lines) <= 2*n {
		return strings.Join(lines, "\n"), false, nil
	}
	head := strings.Join(lines[:n], "\n")
	tail := strings.Join(lines[len(lines)-n:], "\n")
	return head + "\n…（中间日志已省略）…\n" + tail, true, nil
}

// readAllLines reads a text file line by line.
func readAllLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, strings.TrimRight(scanner.Text(), "\r"))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

// readCSVWindow returns the header, the first head rows and the last tail
// rows (§5.5 "CSV 取表头 + 前 200 行 + 尾 20 行").
func readCSVWindow(path string, head, tail int) (string, bool, error) {
	lines, err := readAllLines(path)
	if err != nil {
		return "", false, err
	}
	if len(lines) == 0 {
		return "", false, nil
	}
	body := lines[1:]
	if len(body) <= head+tail {
		return strings.Join(lines, "\n"), false, nil
	}
	var b strings.Builder
	b.WriteString(lines[0])
	b.WriteString("\n")
	b.WriteString(strings.Join(body[:head], "\n"))
	b.WriteString("\n…（中间数据行已省略）…\n")
	b.WriteString(strings.Join(body[len(body)-tail:], "\n"))
	return b.String(), true, nil
}
