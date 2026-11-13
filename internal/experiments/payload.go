package experiments

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"researchos/internal/foundation/clock"
)

// payloadVersion is the "v" of every versioned JSON envelope defined here
// (SPEC-07 §4.3). Arrays of elements (FileEntry, Metric) carry no own version.
const payloadVersion = 1

// ---------------------------------------------------------------------------
// ScanRules (SPEC-07 §4.3)
// ---------------------------------------------------------------------------

// ScanRules is the configurable scan rule set stored in experiments.scan_rules.
// All thresholds are implementation defaults, not requirements (§3.2, O-10).
type ScanRules struct {
	V                 int      `json:"v"`
	Include           []string `json:"include"`
	Exclude           []string `json:"exclude"`
	MaxFileBytes      int64    `json:"max_file_bytes"`
	MaxTotalBytes     int64    `json:"max_total_bytes"`
	MaxFiles          int      `json:"max_files"`
	HashMaxBytes      int64    `json:"hash_max_bytes"`
	FollowSymlinks    bool     `json:"follow_symlinks"`
	IncludeImagesInAI bool     `json:"include_images_in_ai"`
}

// Scan threshold defaults (§3.2). These are calibratable initial values (O-10)
// and never requirement constants.
const (
	DefaultMaxFileBytes  int64 = 512 << 10
	DefaultMaxTotalBytes int64 = 4 << 20
	DefaultMaxFiles            = 20000
	DefaultHashMaxBytes  int64 = 64 << 20
)

// DefaultExclusions are the directories excluded by default. .git is not in
// this list because it is force-excluded and cannot be re-enabled (§3.2).
func DefaultExclusions() []string {
	return []string{
		".git/", ".hg/", ".svn/", "node_modules/", ".venv/", "venv/",
		"__pycache__/", ".mypy_cache/", ".pytest_cache/", ".ipynb_checkpoints/",
		"wandb/", "mlruns/", ".cache/", "checkpoints/",
	}
}

// DefaultScanRules returns the rule set used when experiments.scan_rules is
// empty or '{}' (§4.3).
func DefaultScanRules() ScanRules {
	return ScanRules{
		V:             payloadVersion,
		Include:       []string{"**/*"},
		Exclude:       DefaultExclusions(),
		MaxFileBytes:  DefaultMaxFileBytes,
		MaxTotalBytes: DefaultMaxTotalBytes,
		MaxFiles:      DefaultMaxFiles,
		HashMaxBytes:  DefaultHashMaxBytes,
	}
}

// Validate checks glob syntax and threshold ranges (§5.1 SetScanRules).
func (r ScanRules) Validate() error {
	for _, pattern := range append(append([]string{}, r.Include...), r.Exclude...) {
		if strings.TrimSpace(pattern) == "" {
			return fmt.Errorf("%w: empty glob in scan rules", ErrInvalidInput)
		}
		if _, err := compileGlob(pattern); err != nil {
			return fmt.Errorf("%w: glob %q: %v", ErrInvalidInput, pattern, err)
		}
	}
	if r.MaxFileBytes <= 0 || r.MaxTotalBytes <= 0 || r.MaxFiles <= 0 || r.HashMaxBytes <= 0 {
		return fmt.Errorf("%w: scan thresholds must be positive", ErrInvalidInput)
	}
	if r.MaxTotalBytes < r.MaxFileBytes {
		return fmt.Errorf("%w: max_total_bytes must be >= max_file_bytes", ErrInvalidInput)
	}
	return nil
}

// isZero reports whether no rule field was supplied.
func (r ScanRules) isZero() bool {
	return r.V == 0 && r.Include == nil && r.Exclude == nil && r.MaxFileBytes == 0 &&
		r.MaxTotalBytes == 0 && r.MaxFiles == 0 && r.HashMaxBytes == 0 &&
		!r.FollowSymlinks && !r.IncludeImagesInAI
}

// withDefaults fills every unset field with the §3.2 default. Nil slices mean
// "not supplied", so an explicit empty list is only distinguishable through the
// typed value (Go cannot encode the difference in stored JSON either).
func (r ScanRules) withDefaults() ScanRules {
	d := DefaultScanRules()
	out := r.clone()
	if out.V == 0 {
		out.V = payloadVersion
	}
	if out.Include == nil {
		out.Include = d.Include
	}
	if out.Exclude == nil {
		out.Exclude = d.Exclude
	}
	if out.MaxFileBytes <= 0 {
		out.MaxFileBytes = d.MaxFileBytes
	}
	if out.MaxTotalBytes <= 0 {
		out.MaxTotalBytes = d.MaxTotalBytes
	}
	if out.MaxFiles <= 0 {
		out.MaxFiles = d.MaxFiles
	}
	if out.HashMaxBytes <= 0 {
		out.HashMaxBytes = d.HashMaxBytes
	}
	return out
}

// clone returns a deep copy of the string slices.
func (r ScanRules) clone() ScanRules {
	out := r
	out.Include = append([]string(nil), r.Include...)
	out.Exclude = append([]string(nil), r.Exclude...)
	return out
}

// decodeScanRules reads a stored scan_rules payload. An empty object means the
// defaults; unknown fields are ignored here and preserved by the store on write
// (§4.3).
func decodeScanRules(raw json.RawMessage) (ScanRules, error) {
	rules := DefaultScanRules()
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return rules, nil
	}
	if err := json.Unmarshal(raw, &rules); err != nil {
		return ScanRules{}, fmt.Errorf("experiments: decode scan_rules: %w", err)
	}
	rules = rules.withDefaults()
	if err := rules.Validate(); err != nil {
		return ScanRules{}, err
	}
	return rules, nil
}

// ---------------------------------------------------------------------------
// FileEntry (SPEC-07 §4.3)
// ---------------------------------------------------------------------------

// FileKind is the FileEntry kind discriminator.
type FileKind string

// The ten registered file kinds.
const (
	KindCode     FileKind = "code"
	KindReadme   FileKind = "readme"
	KindConfig   FileKind = "config"
	KindData     FileKind = "data"
	KindMetrics  FileKind = "metrics"
	KindLog      FileKind = "log"
	KindImage    FileKind = "image"
	KindNotebook FileKind = "notebook"
	KindBinary   FileKind = "binary"
	KindOther    FileKind = "other"
)

// ExclusionReason explains why a file never enters AI content (§4.3).
type ExclusionReason string

// The nine registered exclusion reasons.
const (
	ReasonTooLarge       ExclusionReason = "too_large"
	ReasonModelWeight    ExclusionReason = "model_weight"
	ReasonCache          ExclusionReason = "cache"
	ReasonBinary         ExclusionReason = "binary"
	ReasonSecret         ExclusionReason = "secret"
	ReasonUserExclude    ExclusionReason = "user_exclude"
	ReasonArchiveOrMedia ExclusionReason = "archive_or_media"
	ReasonSymlinkOutside ExclusionReason = "symlink_outside"
	ReasonUnreadable     ExclusionReason = "unreadable"
)

// FileEntry is one file_manifest element: scan metadata without content.
type FileEntry struct {
	Path            string           `json:"path"`
	Kind            FileKind         `json:"kind"`
	Size            int64            `json:"size"`
	MTime           string           `json:"mtime"`
	SHA256          *string          `json:"sha256"`
	HashSkipped     *string          `json:"hash_skipped"`
	ContentEligible bool             `json:"content_eligible"`
	ExclusionReason *ExclusionReason `json:"exclusion_reason"`
	Sampled         bool             `json:"sampled"`
}

// ExcludedEntry names a path that does not enter AI content and the reason.
// Pruned directories are reported as a single entry with a trailing slash.
type ExcludedEntry struct {
	Path   string          `json:"path"`
	Reason ExclusionReason `json:"reason"`
}

// ---------------------------------------------------------------------------
// ParamValue (SPEC-07 §4.3)
// ---------------------------------------------------------------------------

// ParamSource locates a parameter value inside a scanned file.
type ParamSource struct {
	Path    string  `json:"path"`
	Pointer *string `json:"pointer,omitempty"`
	SHA256  *string `json:"sha256,omitempty"`
}

// ParamValue is one Run parameter. Source is nil for hand-entered values; file
// candidates are only written after user confirmation (§3.3).
type ParamValue struct {
	Value     string       `json:"value"`
	Source    *ParamSource `json:"source,omitempty"`
	Confirmed bool         `json:"confirmed"`
}

// ---------------------------------------------------------------------------
// GitInfo (SPEC-07 §4.3)
// ---------------------------------------------------------------------------

// GitSource records how GitInfo was captured.
type GitSource string

// v1 only ever produces read_at_analysis (O-06).
const (
	GitReadAtAnalysis GitSource = "read_at_analysis"
	GitRuntimeMeta    GitSource = "runtime_metadata"
)

// GitInfo is the read-only Git snapshot stored on runs and snapshots (§3.10).
type GitInfo struct {
	V              int       `json:"v"`
	Detected       bool      `json:"detected"`
	RepoRoot       string    `json:"repo_root"`
	Branch         string    `json:"branch"`
	Commit         string    `json:"commit"`
	CommitShort    string    `json:"commit_short"`
	CommitTime     string    `json:"commit_time"`
	Dirty          bool      `json:"dirty"`
	ChangedFiles   int       `json:"changed_files"`
	UntrackedFiles int       `json:"untracked_files"`
	CapturedAt     string    `json:"captured_at"`
	Source         GitSource `json:"source"`
	Truncated      bool      `json:"truncated"`
}

// ---------------------------------------------------------------------------
// Metric / MetricSet (SPEC-07 §4.3)
// ---------------------------------------------------------------------------

// MetricSource locates a metric value: relative path, JSON pointer or
// line:N, and the manifest hash of the file.
type MetricSource struct {
	Path    string  `json:"path"`
	Pointer *string `json:"pointer"`
	Line    *int    `json:"line"`
	SHA256  *string `json:"sha256"`
}

// Metric is one deterministically extracted value.
type Metric struct {
	Key       string         `json:"key"`
	Label     string         `json:"label"`
	Value     *float64       `json:"value"`
	Raw       string         `json:"raw"`
	Unit      *string        `json:"unit"`
	Context   map[string]any `json:"context,omitempty"`
	Source    MetricSource   `json:"source"`
	Extractor string         `json:"extractor"`
}

// UnparsedFile records a file no extractor could read.
type UnparsedFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// MetricSet is the deterministic metric table of one analysis.
type MetricSet struct {
	V        int            `json:"v"`
	Metrics  []Metric       `json:"metrics"`
	Unparsed []UnparsedFile `json:"unparsed"`
}

// MetricComparison aligns metrics of the runs of one experiment by key and
// context and computes deltas against the baseline run (§5.7).
type MetricComparison struct {
	Runs []RunColumn `json:"runs"`
	Rows []MetricRow `json:"rows"`
}

// RunColumn is one run participating in a comparison.
type RunColumn struct {
	RunID string `json:"run_id"`
	Label string `json:"label"`
}

// MetricRow is one aligned metric across runs.
type MetricRow struct {
	Key     string             `json:"key"`
	Context map[string]any     `json:"context,omitempty"`
	Values  map[string]Metric  `json:"values"` // run id → metric
	Delta   map[string]float64 `json:"delta"`
	Pct     map[string]float64 `json:"pct"`
}

// ---------------------------------------------------------------------------
// AIAnalysis envelope (SPEC-07 §4.3)
// ---------------------------------------------------------------------------

// AnalysisKind is the analysis flavour of a snapshot.
type AnalysisKind string

// The two registered analysis kinds.
const (
	AnalysisOutline AnalysisKind = "outline"
	AnalysisResults AnalysisKind = "results"
)

// Valid reports whether k is a registered analysis kind.
func (k AnalysisKind) Valid() bool {
	return k == AnalysisOutline || k == AnalysisResults
}

// SectionKey is one AI outline/results section key (§3.4, §3.5).
type SectionKey string

// Outline section keys.
const (
	SectionPurpose        SectionKey = "purpose"
	SectionDesign         SectionKey = "design"
	SectionModelAlgorithm SectionKey = "model_algorithm"
	SectionConfig         SectionKey = "config"
	SectionData           SectionKey = "data"
	SectionPipeline       SectionKey = "pipeline"
	SectionMetrics        SectionKey = "metrics"
	SectionObservations   SectionKey = "observations"
	SectionSummary        SectionKey = "summary"
	SectionComparison     SectionKey = "comparison"
	SectionLimitations    SectionKey = "limitations"
	SectionConclusion     SectionKey = "conclusion"
)

// validSection enforces the closed enum of §3.4/§3.5.
func validSection(k SectionKey) bool {
	switch k {
	case SectionPurpose, SectionDesign, SectionModelAlgorithm, SectionConfig,
		SectionData, SectionPipeline, SectionMetrics, SectionObservations,
		SectionSummary, SectionComparison, SectionLimitations, SectionConclusion:
		return true
	}
	return false
}

// SourceRef is a snapshot-relative source citation of one section.
type SourceRef struct {
	Path       string   `json:"path"`
	SHA256     string   `json:"sha256,omitempty"`
	MetricKeys []string `json:"metric_keys,omitempty"`
}

// AISection is one generated section with its citations.
type AISection struct {
	Key               SectionKey  `json:"key"`
	Title             string      `json:"title"`
	TextMD            string      `json:"text_md"`
	Sources           []SourceRef `json:"sources"`
	Unconfirmed       bool        `json:"unconfirmed"`
	UnconfirmedReason *string     `json:"unconfirmed_reason"`
}

// UnverifiedClaim is an AI number that no metric or source text backs (§3.5).
type UnverifiedClaim struct {
	Section string `json:"section"`
	Text    string `json:"text"`
	Reason  string `json:"reason"`
}

// ExtractedConfig is the deterministic key/value summary of one config file.
type ExtractedConfig struct {
	Path       string         `json:"path"`
	SHA256     string         `json:"sha256"`
	Values     map[string]any `json:"values"`
	Truncated  bool           `json:"truncated,omitempty"`
	ParseError *ParseError    `json:"parse_error,omitempty"`
}

// ParseError locates a config parse failure.
type ParseError struct {
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Message string `json:"message"`
}

// AIAnalysisConfig is the deterministic audit context of an analysis (§4.3).
type AIAnalysisConfig struct {
	ScanRules ScanRules             `json:"scan_rules"`
	RunParams map[string]ParamValue `json:"run_params"`
	Extracted []ExtractedConfig     `json:"extracted"`
	// Comparison is the deterministic cross-run delta table the model saw.
	// It is evidence, never AI text (§3.5, §6.5).
	Comparison *MetricComparison `json:"comparison,omitempty"`
}

// AIAnalysis is the frozen AI text plus audit context stored in
// snapshots.ai_analysis (§4.3).
type AIAnalysis struct {
	V                int               `json:"v"`
	Kind             AnalysisKind      `json:"kind"`
	AIOutputID       string            `json:"ai_output_id"`
	PromptVersion    string            `json:"prompt_version"`
	ContentMD        string            `json:"content_md"`
	ContentSHA256    string            `json:"content_sha256"`
	Sections         []AISection       `json:"sections"`
	UnverifiedClaims []UnverifiedClaim `json:"unverified_claims"`
	Config           AIAnalysisConfig  `json:"config"`
	Warnings         []string          `json:"warnings"`
}

// Validate enforces the closed section enum and the version.
func (a AIAnalysis) Validate() error {
	if !a.Kind.Valid() {
		return fmt.Errorf("%w: invalid analysis kind %q", ErrInvalidInput, a.Kind)
	}
	for _, s := range a.Sections {
		if !validSection(s.Key) {
			return fmt.Errorf("%w: unknown section key %q", ErrInvalidInput, s.Key)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// ModelInfo (SPEC-07 §4.3)
// ---------------------------------------------------------------------------

// ProvenanceRef is one source reference reported by SPEC-02 provenance.
type ProvenanceRef struct {
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	Locator    string `json:"locator"`
	Title      string `json:"title"`
}

// ModelInfo is the frozen SPEC-02 provenance of one analysis.
type ModelInfo struct {
	V             int             `json:"v"`
	ProviderID    string          `json:"provider_id"`
	ProviderName  string          `json:"provider_name"`
	Protocol      string          `json:"protocol"`
	ModelID       string          `json:"model_id"`
	PromptVersion string          `json:"prompt_version"`
	Parameters    map[string]any  `json:"parameters"`
	SourceRefs    []ProvenanceRef `json:"source_refs"`
	GeneratedAt   string          `json:"generated_at"`
}

// ---------------------------------------------------------------------------
// JSON helpers
// ---------------------------------------------------------------------------

// encodeJSON marshals a payload without HTML escaping so hashes and paths stay
// byte-identical across round trips.
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// decodeJSONObject decodes a stored JSON object, tolerating NULL and empty
// values by returning nil.
func decodeJSONObject(raw []byte) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// decodeFileManifest reads snapshots.file_manifest. Damaged JSON yields an
// empty manifest rather than failing the whole history read (§E01).
func decodeFileManifest(raw []byte) []FileEntry {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return []FileEntry{}
	}
	var out []FileEntry
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return []FileEntry{}
	}
	return out
}

// decodeMetricSet reads snapshots.metrics, tolerating damage.
func decodeMetricSet(raw []byte) MetricSet {
	out := MetricSet{V: payloadVersion, Metrics: []Metric{}, Unparsed: []UnparsedFile{}}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return out
	}
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return MetricSet{V: payloadVersion, Metrics: []Metric{}, Unparsed: []UnparsedFile{}}
	}
	if out.V == 0 {
		out.V = payloadVersion
	}
	if out.Metrics == nil {
		out.Metrics = []Metric{}
	}
	if out.Unparsed == nil {
		out.Unparsed = []UnparsedFile{}
	}
	return out
}

// decodeGitInfo reads a stored git_info object.
func decodeGitInfo(raw []byte) GitInfo {
	out := GitInfo{V: payloadVersion}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return out
	}
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return GitInfo{V: payloadVersion}
	}
	if out.V == 0 {
		out.V = payloadVersion
	}
	return out
}

// decodeModelInfo reads a stored model_info object.
func decodeModelInfo(raw []byte) ModelInfo {
	out := ModelInfo{V: payloadVersion, Parameters: map[string]any{}, SourceRefs: []ProvenanceRef{}}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return out
	}
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return ModelInfo{V: payloadVersion, Parameters: map[string]any{}, SourceRefs: []ProvenanceRef{}}
	}
	if out.V == 0 {
		out.V = payloadVersion
	}
	return out
}

// decodeAIAnalysis reads snapshots.ai_analysis. A NULL column yields nil.
func decodeAIAnalysis(raw []byte) *AIAnalysis {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	var out AIAnalysis
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return nil
	}
	if out.V == 0 {
		out.V = payloadVersion
	}
	return &out
}

// decodeParams reads runs.params. Damaged JSON yields an empty map.
func decodeParams(raw []byte) map[string]ParamValue {
	out := map[string]ParamValue{}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return out
	}
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return map[string]ParamValue{}
	}
	if out == nil {
		out = map[string]ParamValue{}
	}
	return out
}

// walkJSONLeaves visits every scalar leaf under v, appending its JSON pointer
// and value. Object keys are visited in sorted order so extraction is stable.
func walkJSONLeaves(v any, pointer string, fn func(pointer string, value any)) {
	switch typed := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for k := range typed {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walkJSONLeaves(typed[k], pointer+"/"+jsonPointerEscape(k), fn)
		}
	case []any:
		for i, item := range typed {
			walkJSONLeaves(item, fmt.Sprintf("%s/%d", pointer, i), fn)
		}
	default:
		fn(pointer, typed)
	}
}

// jsonPointerEscape implements RFC 6901 escaping.
func jsonPointerEscape(key string) string {
	key = strings.ReplaceAll(key, "~", "~0")
	return strings.ReplaceAll(key, "/", "~1")
}

// normalizeRelPath canonicalizes a scan-relative path to forward slashes
// without a leading separator (§3.2, SPEC-01 O-17).
func normalizeRelPath(rel string) string {
	rel = strings.ReplaceAll(rel, "\\", "/")
	return strings.TrimPrefix(path.Clean("/"+rel), "/")
}

// formatTime renders at in the single storage layout.
func formatTime(at time.Time) string { return clock.Format(at) }
