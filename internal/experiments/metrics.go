package experiments

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"researchos/internal/foundation/ids"
)

// Extractor names are recorded on every metric so the origin stays auditable
// (§5.7). v1 ships baselines only; no rule DSL (O-01).
const (
	extractorJSONLeaves = "json_numeric_leaves.v1"
	extractorCSVScalars = "csv_scalar_columns.v1"
)

// FileAccessor reads scan-relative files without leaving the scan root.
type FileAccessor interface {
	Read(rel string) ([]byte, error)
}

// MetricExtractor is one deterministic metric rule (§5.7). Extractors are
// isolated: a failure of one never aborts the others.
type MetricExtractor interface {
	Name() string
	Supports(entry FileEntry) bool
	Extract(ctx context.Context, fs FileAccessor, entry FileEntry) ([]Metric, error)
}

// defaultMetricExtractors is the v1 baseline set. No rule DSL is exposed
// (O-01); new extractors are appended here.
var defaultMetricExtractors = []MetricExtractor{
	jsonLeafExtractor{},
	csvColumnExtractor{},
}

// rootAccessor resolves accessor paths against one scan root.
type rootAccessor struct{ root string }

// Read opens one scan-relative file read-only.
func (a rootAccessor) Read(rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(a.root, filepath.FromSlash(rel)))
}

// ExtractMetrics runs every registered extractor over the eligible files and
// merges the results deterministically (§5.7).
func (s *Service) ExtractMetrics(ctx context.Context, scan *ScanResult) (*MetricSet, error) {
	if scan == nil {
		return nil, fmt.Errorf("%w: nil scan", ErrInvalidInput)
	}
	set := &MetricSet{V: payloadVersion, Metrics: []Metric{}, Unparsed: []UnparsedFile{}}
	accessor := rootAccessor{root: scan.Plan.Root}
	for _, entry := range scan.Plan.Entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.ContentEligible || entry.SHA256 == nil {
			continue
		}
		matched := false
		for _, extractor := range defaultMetricExtractors {
			if !extractor.Supports(entry) {
				continue
			}
			matched = true
			metrics, err := extractor.Extract(ctx, accessor, entry)
			if err != nil {
				set.Unparsed = append(set.Unparsed, UnparsedFile{Path: entry.Path, Reason: extractor.Name() + ": " + err.Error()})
				continue
			}
			set.Metrics = append(set.Metrics, metrics...)
		}
		if !matched && (entry.Kind == KindMetrics || entry.Kind == KindData) {
			set.Unparsed = append(set.Unparsed, UnparsedFile{Path: entry.Path, Reason: "no_rule_match"})
		}
	}
	sort.SliceStable(set.Metrics, func(i, j int) bool {
		if set.Metrics[i].Source.Path != set.Metrics[j].Source.Path {
			return set.Metrics[i].Source.Path < set.Metrics[j].Source.Path
		}
		return set.Metrics[i].Key < set.Metrics[j].Key
	})
	return set, nil
}

// jsonLeafExtractor extracts every numeric leaf of a JSON document.
type jsonLeafExtractor struct{}

// Name returns the registered extractor name.
func (jsonLeafExtractor) Name() string { return extractorJSONLeaves }

// Supports accepts JSON config and metric files only; YAML/TOML are parsed by
// the metadata layer, not by the metric baseline.
func (jsonLeafExtractor) Supports(entry FileEntry) bool {
	if entry.Kind != KindMetrics && entry.Kind != KindConfig {
		return false
	}
	return strings.EqualFold(filepath.Ext(entry.Path), ".json")
}

// Extract implements MetricExtractor.
func (e jsonLeafExtractor) Extract(ctx context.Context, fs FileAccessor, entry FileEntry) ([]Metric, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := fs.Read(entry.Path)
	if err != nil {
		return nil, err
	}
	return extractJSONLeaves(raw, entry)
}

// csvColumnExtractor treats each numeric column as a scalar of the last row.
type csvColumnExtractor struct{}

// Name returns the registered extractor name.
func (csvColumnExtractor) Name() string { return extractorCSVScalars }

// Supports accepts CSV/TSV data files.
func (csvColumnExtractor) Supports(entry FileEntry) bool {
	if entry.Kind != KindData {
		return false
	}
	ext := strings.ToLower(filepath.Ext(entry.Path))
	return ext == ".csv" || ext == ".tsv"
}

// Extract implements MetricExtractor.
func (e csvColumnExtractor) Extract(ctx context.Context, fs FileAccessor, entry FileEntry) ([]Metric, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := fs.Read(entry.Path)
	if err != nil {
		return nil, err
	}
	return extractCSVColumns(raw, entry)
}

// extractJSONLeaves collects every numeric leaf of a JSON document (§5.7).
func extractJSONLeaves(raw []byte, entry FileEntry) ([]Metric, error) {
	var doc any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		return nil, err
	}
	metrics := make([]Metric, 0, 8)
	sha := entry.SHA256
	walkJSONLeaves(doc, "", func(pointer string, value any) {
		number, ok := value.(json.Number)
		if !ok {
			return
		}
		float, err := number.Float64()
		if err != nil || math.IsNaN(float) || math.IsInf(float, 0) {
			return
		}
		key := strings.TrimPrefix(pointer, "/")
		if key == "" {
			key = "(root)"
		}
		ptr := pointer
		metrics = append(metrics, Metric{
			Key:       key,
			Label:     metricLabel(pointer),
			Value:     &float,
			Raw:       number.String(),
			Source:    MetricSource{Path: entry.Path, Pointer: &ptr, SHA256: sha},
			Extractor: extractorJSONLeaves,
		})
	})
	sort.Slice(metrics, func(i, j int) bool { return metrics[i].Key < metrics[j].Key })
	return metrics, nil
}

// metricLabel derives a human label from the last pointer segment.
func metricLabel(pointer string) string {
	if pointer == "" {
		return "value"
	}
	segments := strings.Split(pointer, "/")
	for i := len(segments) - 1; i >= 0; i-- {
		if segments[i] != "" {
			return strings.ReplaceAll(strings.ReplaceAll(segments[i], "~1", "/"), "~0", "~")
		}
	}
	return "value"
}

// extractCSVColumns treats each numeric column as a scalar metric of the last
// data row (§5.7 "CSV 表头/单行或指定列的标量"). The source line records which
// row supplied the value.
func extractCSVColumns(raw []byte, entry FileEntry) ([]Metric, error) {
	reader := csv.NewReader(strings.NewReader(string(raw)))
	reader.FieldsPerRecord = -1
	if strings.EqualFold(filepath.Ext(entry.Path), ".tsv") {
		reader.Comma = '\t'
	}
	header, err := reader.Read()
	if err != nil {
		return nil, err
	}
	if len(header) == 0 {
		return nil, errors.New("empty header")
	}
	var (
		last     []string
		lastLine int
		line     = 1
	)
	for {
		record, err := reader.Read()
		if errors.Is(err, csv.ErrFieldCount) {
			continue
		}
		if err != nil {
			break
		}
		line++
		last = record
		lastLine = line
	}
	if last == nil {
		return nil, nil
	}
	metrics := make([]Metric, 0, len(header))
	for i, name := range header {
		if i >= len(last) {
			continue
		}
		text := strings.TrimSpace(last[i])
		if text == "" {
			continue
		}
		value, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		line := lastLine
		key := strings.TrimSpace(name)
		if key == "" {
			key = fmt.Sprintf("column_%d", i+1)
		}
		label := key
		metrics = append(metrics, Metric{
			Key:   key,
			Label: label,
			Value: &value,
			Raw:   text,
			Source: MetricSource{
				Path:   entry.Path,
				Line:   &line,
				SHA256: entry.SHA256,
			},
			Extractor: extractorCSVScalars,
		})
	}
	return metrics, nil
}

// CompareRuns aligns the metrics of every run of one experiment by key and
// context and computes deltas against the first run (§5.7). It never calls AI.
func (s *Service) CompareRuns(ctx context.Context, experimentID string) (*MetricComparison, error) {
	handle, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.loadExperiment(ctx, handle.DB(), experimentID, false); err != nil {
		return nil, err
	}
	runs, err := s.ListRuns(ctx, experimentID, false)
	if err != nil {
		return nil, err
	}
	comparison := &MetricComparison{Runs: make([]RunColumn, 0, len(runs)), Rows: []MetricRow{}}
	type aligned struct {
		key     string
		context map[string]any
		values  map[string]Metric
	}
	rows := map[string]*aligned{}
	order := []string{}
	for _, run := range runs {
		comparison.Runs = append(comparison.Runs, RunColumn{RunID: run.ID, Label: run.DisplayName})
		snap, err := s.latestResults(ctx, handle.DB(), run.ID)
		if err != nil {
			return nil, err
		}
		if snap == nil {
			continue
		}
		for _, metric := range snap.Metrics.Metrics {
			key := alignmentKey(metric)
			row, ok := rows[key]
			if !ok {
				row = &aligned{key: metric.Key, context: metric.Context, values: map[string]Metric{}}
				rows[key] = row
				order = append(order, key)
			}
			row.values[run.ID] = metric
		}
	}
	sort.Strings(order)
	var baselineID string
	if len(runs) > 0 {
		baselineID = runs[0].ID
	}
	for _, key := range order {
		row := rows[key]
		out := MetricRow{
			Key:     row.key,
			Context: row.context,
			Values:  row.values,
			Delta:   map[string]float64{},
			Pct:     map[string]float64{},
		}
		base, hasBase := row.values[baselineID]
		for runID, metric := range row.values {
			if runID == baselineID || !hasBase || base.Value == nil || metric.Value == nil {
				continue
			}
			out.Delta[runID] = *metric.Value - *base.Value
			if pct, ok := pctChange(*base.Value, *metric.Value); ok {
				out.Pct[runID] = pct
			}
		}
		comparison.Rows = append(comparison.Rows, out)
	}
	return comparison, nil
}

// latestResults returns the newest results snapshot of one run, or nil.
func (s *Service) latestResults(ctx context.Context, q queryer, runID string) (*Snapshot, error) {
	if !ids.Valid(runID) {
		return nil, nil
	}
	row := q.QueryRowContext(ctx,
		`SELECT `+snapshotColumns+` FROM snapshots
		  WHERE target_type = 'run' AND target_id = ?
		    AND CASE WHEN json_valid(ai_analysis) THEN json_extract(ai_analysis, '$.kind') END = 'results'
		  ORDER BY created_at DESC, id DESC LIMIT 1`, runID)
	snap, err := scanSnapshotRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("experiments: read results snapshot: %w", err)
	}
	return &snap, nil
}

// alignmentKey canonicalizes key plus context for cross-run alignment (O-15).
func alignmentKey(metric Metric) string {
	contextJSON, err := encodeJSON(metric.Context)
	if err != nil {
		contextJSON = nil
	}
	return metric.Key + "\x00" + string(contextJSON)
}
