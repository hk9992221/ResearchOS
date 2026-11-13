package experiments

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// configValueLimit is the structured-value budget per file (§4.3).
const configValueLimit = 64 << 10

// Extract performs the deterministic metadata extraction of §3.3. It executes
// no code, opens no network connection and never writes the experiment
// directory.
func (s *Service) ExtractMetadata(ctx context.Context, scan *ScanResult) (*Metadata, error) {
	if scan == nil {
		return nil, fmt.Errorf("%w: nil scan", ErrInvalidInput)
	}
	meta := &Metadata{KindCounts: map[FileKind]int{}}
	for _, entry := range scan.Plan.Entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		meta.TotalFiles++
		meta.TotalBytes += entry.Size
		meta.KindCounts[entry.Kind]++
		if !entry.ContentEligible {
			continue
		}
		path := filepath.Join(scan.Plan.Root, filepath.FromSlash(entry.Path))
		switch entry.Kind {
		case KindConfig, KindMetrics:
			config := s.parseConfig(path, entry)
			if config.ParseError != nil {
				meta.Warnings = append(meta.Warnings, Warning{Code: "config_parse_failed", Path: entry.Path, Message: config.ParseError.Message})
			}
			meta.Configs = append(meta.Configs, config)
		case KindData:
			summary, err := parseCSV(path, entry.Path)
			if err != nil {
				meta.Warnings = append(meta.Warnings, Warning{Code: "csv_parse_failed", Path: entry.Path, Message: err.Error()})
			}
			meta.CSVs = append(meta.CSVs, summary)
		case KindReadme:
			summary, err := parseReadme(path, entry.Path)
			if err != nil {
				meta.Warnings = append(meta.Warnings, Warning{Code: "readme_parse_failed", Path: entry.Path, Message: err.Error()})
				continue
			}
			meta.Readmes = append(meta.Readmes, summary)
		}
		if deps := parseDependencies(path, entry.Path); deps != nil {
			meta.Dependencies = append(meta.Dependencies, *deps)
		}
		if entry.Kind == KindCode && isEntryCandidate(entry.Path) {
			meta.EntryPoints = append(meta.EntryPoints, EntryCandidate{Path: entry.Path, Heuristic: true})
		}
	}
	sort.Slice(meta.Configs, func(i, j int) bool { return meta.Configs[i].Path < meta.Configs[j].Path })
	sort.Slice(meta.CSVs, func(i, j int) bool { return meta.CSVs[i].Path < meta.CSVs[j].Path })
	sort.Slice(meta.Readmes, func(i, j int) bool { return meta.Readmes[i].Path < meta.Readmes[j].Path })
	sort.Slice(meta.Dependencies, func(i, j int) bool { return meta.Dependencies[i].Path < meta.Dependencies[j].Path })
	sort.Slice(meta.EntryPoints, func(i, j int) bool { return meta.EntryPoints[i].Path < meta.EntryPoints[j].Path })
	return meta, nil
}

// parseConfig reads one YAML/JSON/TOML config and records the parse error at
// its position when the syntax is invalid (§3.3).
func (s *Service) parseConfig(path string, entry FileEntry) ExtractedConfig {
	out := ExtractedConfig{Path: entry.Path, Values: map[string]any{}}
	if entry.SHA256 != nil {
		out.SHA256 = *entry.SHA256
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		out.ParseError = &ParseError{Message: err.Error()}
		return out
	}
	var values map[string]any
	switch strings.ToLower(filepath.Ext(entry.Path)) {
	case ".json":
		var parsed any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			out.ParseError = &ParseError{Message: err.Error()}
			return out
		}
		if object, ok := parsed.(map[string]any); ok {
			values = object
		} else {
			values = map[string]any{"value": parsed}
		}
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(raw, &values); err != nil {
			out.ParseError = yamlParseError(err)
			return out
		}
	case ".toml":
		if err := toml.Unmarshal(raw, &values); err != nil {
			out.ParseError = tomlParseError(err)
			return out
		}
	default:
		return out
	}
	if values == nil {
		values = map[string]any{}
	}
	values, truncated := truncateConfig(values)
	out.Values = values
	out.Truncated = truncated
	return out
}

// yamlParseError converts a YAML error to the versioned parse error.
func yamlParseError(err error) *ParseError {
	out := &ParseError{Message: err.Error()}
	var typeErr *yaml.TypeError
	if errors.As(err, &typeErr) && len(typeErr.Errors) > 0 {
		out.Message = strings.Join(typeErr.Errors, "; ")
	}
	return out
}

// tomlParseError converts a TOML parse error to the versioned parse error.
func tomlParseError(err error) *ParseError {
	out := &ParseError{Message: err.Error()}
	var parseErr toml.ParseError
	if errors.As(err, &parseErr) {
		out.Line = parseErr.Position.Line
		out.Column = parseErr.Position.Col
	}
	return out
}

// truncateConfig keeps top-level keys in sorted order until the 64 KiB budget
// is exhausted and reports whether anything was dropped (§4.3).
func truncateConfig(values map[string]any) (map[string]any, bool) {
	encoded, err := encodeJSON(values)
	if err != nil || len(encoded) <= configValueLimit {
		return values, false
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := map[string]any{}
	size := 0
	for _, key := range keys {
		part, err := encodeJSON(map[string]any{key: values[key]})
		if err != nil {
			continue
		}
		if size+len(part) > configValueLimit {
			break
		}
		out[key] = values[key]
		size += len(part)
	}
	return out, true
}

// parseCSV reads the header, column count and row count of one CSV/TSV file.
func parseCSV(path, rel string) (CSVSummary, error) {
	out := CSVSummary{Path: rel, Delimiter: ','}
	if strings.EqualFold(filepath.Ext(rel), ".tsv") {
		out.Delimiter = '\t'
	}
	f, err := os.Open(path)
	if err != nil {
		out.ParseError = &ParseError{Message: err.Error()}
		return out, err
	}
	defer f.Close()
	reader := csv.NewReader(bufio.NewReader(f))
	reader.Comma = out.Delimiter
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		out.ParseError = &ParseError{Message: err.Error()}
		return out, err
	}
	out.Header = header
	out.Columns = len(header)
	for {
		record, err := reader.Read()
		if errors.Is(err, csv.ErrFieldCount) {
			out.Rows++
			continue
		}
		if err != nil {
			break
		}
		_ = record
		out.Rows++
	}
	return out, nil
}

// parseReadme extracts the title and the heading list of a Markdown file.
func parseReadme(path, rel string) (ReadmeSummary, error) {
	out := ReadmeSummary{Path: rel}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		title := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		if title == "" {
			continue
		}
		out.Headings = append(out.Headings, title)
		if out.Title == "" && strings.HasPrefix(trimmed, "# ") {
			out.Title = title
		}
	}
	if err := scanner.Err(); err != nil {
		return out, err
	}
	if out.Title == "" {
		out.Title = strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
	}
	return out, nil
}

// parseDependencies reads declared dependencies only; nothing is installed or
// validated (§3.3).
func parseDependencies(path, rel string) *DependencySet {
	base := strings.ToLower(filepath.Base(rel))
	switch {
	case strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt"):
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		set := &DependencySet{Path: rel, Kind: "requirements"}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if idx := strings.Index(line, " #"); idx > 0 {
				line = strings.TrimSpace(line[:idx])
			}
			set.Dependencies = append(set.Dependencies, line)
		}
		return set
	case base == "pyproject.toml":
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var doc struct {
			Project struct {
				Dependencies []string `toml:"dependencies"`
			} `toml:"project"`
			Tool struct {
				Poetry struct {
					Dependencies map[string]any `toml:"dependencies"`
				} `toml:"poetry"`
			} `toml:"tool"`
		}
		if _, err := toml.Decode(string(raw), &doc); err != nil {
			return nil
		}
		set := &DependencySet{Path: rel, Kind: "pyproject"}
		set.Dependencies = append(set.Dependencies, doc.Project.Dependencies...)
		keys := make([]string, 0, len(doc.Tool.Poetry.Dependencies))
		for key := range doc.Tool.Poetry.Dependencies {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		set.Dependencies = append(set.Dependencies, keys...)
		return set
	case base == "environment.yml" || base == "environment.yaml":
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var doc struct {
			Dependencies []any `yaml:"dependencies"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil
		}
		set := &DependencySet{Path: rel, Kind: "environment"}
		for _, item := range doc.Dependencies {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				set.Dependencies = append(set.Dependencies, strings.TrimSpace(text))
			}
		}
		return set
	}
	return nil
}

// entryCandidatePattern matches the §3.3 entry-point naming heuristics.
var entryCandidatePattern = regexp.MustCompile(`^(main|train|run[^/]*|eval[^/]*|infer[^/]*)\.py$`)

// isEntryCandidate reports the naming heuristic (never a fact).
func isEntryCandidate(rel string) bool {
	return entryCandidatePattern.MatchString(strings.ToLower(filepath.Base(rel)))
}
