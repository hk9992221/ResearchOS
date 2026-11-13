package experiments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"researchos/internal/foundation/clock"
)

// hashSkipTooLarge and hashSkipBinary mark rows whose content was not hashed.
const (
	hashSkipTooLarge = "too_large"
	hashSkipBinary   = "binary"
)

// scanFileLimit is the number of unreadable-file warnings kept per scan.
const scanWarningLimit = 200

// glob is one compiled include/exclude pattern with its gitignore-style scope.
type glob struct {
	pattern  string
	re       *regexp.Regexp
	dirOnly  bool
	baseOnly bool
}

// compileGlob validates and compiles a gitignore-style pattern (§3.2):
//   - a pattern without "/" matches a basename at any depth;
//   - a pattern containing "/" is anchored at the scan root;
//   - a trailing "/" restricts the pattern to directories;
//   - "**" spans zero or more path segments.
func compileGlob(pattern string) (glob, error) {
	raw := strings.TrimSpace(pattern)
	if raw == "" {
		return glob{}, fmt.Errorf("empty pattern")
	}
	if strings.HasPrefix(raw, "!") {
		return glob{}, fmt.Errorf("negated patterns are not supported")
	}
	g := glob{pattern: raw}
	if strings.HasSuffix(raw, "/") {
		g.dirOnly = true
		raw = strings.TrimSuffix(raw, "/")
	}
	anchored := strings.HasPrefix(raw, "/")
	raw = strings.TrimPrefix(raw, "/")
	g.baseOnly = !anchored && !strings.Contains(raw, "/")

	segments := strings.Split(raw, "/")
	var b strings.Builder
	b.WriteString("^")
	if g.baseOnly {
		b.WriteString("(?:.*/)?")
	}
	wroteSegment := false
	skipSeparator := false
	for i, segment := range segments {
		if segment == "" {
			return glob{}, fmt.Errorf("empty path segment")
		}
		if segment == "**" {
			if i == len(segments)-1 {
				b.WriteString(".*")
			} else {
				b.WriteString("(?:[^/]+/)*")
				skipSeparator = true
			}
			continue
		}
		if wroteSegment && !skipSeparator {
			b.WriteString("/")
		}
		part, err := translateSegment(segment)
		if err != nil {
			return g, err
		}
		b.WriteString(part)
		wroteSegment = true
		skipSeparator = false
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return g, err
	}
	g.re = re
	return g, nil
}

// translateSegment converts one path segment into a regex fragment.
func translateSegment(segment string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(segment); i++ {
		switch c := segment[i]; c {
		case '*':
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(segment[i:], ']')
			if end < 0 {
				return "", fmt.Errorf("unclosed character class")
			}
			class := segment[i : i+end+1]
			if strings.HasPrefix(class, "[!") {
				class = "[^" + class[2:]
			}
			b.WriteString(class)
			i += end
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String(), nil
}

// matchesPath reports whether rel matches g. Directories are tested with a
// trailing slash so that "outputs/**" prunes the directory itself.
func (g glob) matchesPath(rel string, isDir bool) bool {
	if g.dirOnly && !isDir {
		return false
	}
	target := strings.TrimPrefix(rel, "/")
	if isDir {
		if g.dirOnly {
			// A directory-only pattern names the directory itself.
			target = strings.TrimSuffix(target, "/")
		} else if !strings.HasSuffix(target, "/") {
			target += "/"
		}
	}
	if g.baseOnly {
		name := strings.TrimSuffix(target, "/")
		if isDir {
			return g.re.MatchString(name)
		}
		// A basename pattern also matches a file directly under the root.
		return g.re.MatchString(name)
	}
	return g.re.MatchString(target)
}

// ruleSet is the per-scan compiled rule set.
type ruleSet struct {
	rules   ScanRules
	include []glob
	exclude []glob
}

// compileRuleSet validates and compiles rules.
func compileRuleSet(rules ScanRules) (*ruleSet, error) {
	if err := rules.Validate(); err != nil {
		return nil, err
	}
	set := &ruleSet{rules: rules}
	for _, pattern := range rules.Include {
		g, err := compileGlob(pattern)
		if err != nil {
			return nil, fmt.Errorf("%w: include %q: %v", ErrInvalidInput, pattern, err)
		}
		set.include = append(set.include, g)
	}
	for _, pattern := range rules.Exclude {
		g, err := compileGlob(pattern)
		if err != nil {
			return nil, fmt.Errorf("%w: exclude %q: %v", ErrInvalidInput, pattern, err)
		}
		set.exclude = append(set.exclude, g)
	}
	return set, nil
}

// excluded reports whether rel is excluded, together with the display reason.
// Excludes win over includes (§3.2). Ancestor directories are tested so that a
// directory pattern excludes everything below it.
func (set *ruleSet) excluded(rel string, isDir bool) (bool, ExclusionReason) {
	rel = normalizeRelPath(rel)
	if rel == "" || rel == "." {
		return false, ""
	}
	if isDir {
		for _, g := range set.exclude {
			if g.matchesPath(rel, true) {
				return true, exclusionReasonFor(g.pattern)
			}
		}
	} else {
		ancestors := strings.Split(rel, "/")
		for i := 1; i < len(ancestors); i++ {
			dir := strings.Join(ancestors[:i], "/")
			for _, g := range set.exclude {
				if g.matchesPath(dir, true) {
					return true, exclusionReasonFor(g.pattern)
				}
			}
		}
		for _, g := range set.exclude {
			if g.matchesPath(rel, false) {
				return true, exclusionReasonFor(g.pattern)
			}
		}
	}
	if isDir {
		return false, ""
	}
	if len(set.include) > 0 {
		for _, g := range set.include {
			if g.matchesPath(rel, false) {
				return false, ""
			}
		}
		return true, ReasonUserExclude
	}
	return false, ""
}

// exclusionReasonFor labels a pruned path. Known cache directories are labelled
// cache; every other user-configurable pattern is a user exclusion.
func exclusionReasonFor(pattern string) ExclusionReason {
	cleaned := strings.TrimSuffix(strings.TrimPrefix(pattern, "/"), "/")
	switch cleaned {
	case ".git", ".hg", ".svn", ".venv", "venv", "__pycache__", ".mypy_cache",
		".pytest_cache", ".ipynb_checkpoints", "wandb", "mlruns", ".cache",
		"checkpoints", "node_modules":
		return ReasonCache
	}
	return ReasonUserExclude
}

// Plan walks the target directory read-only and returns the frozen list (§5.3).
func (s *Service) Plan(ctx context.Context, target TargetRef, override *ScanRules) (*ScanPlan, error) {
	root, rules, err := s.resolveTarget(ctx, target, override)
	if err != nil {
		return nil, err
	}
	set, err := compileRuleSet(rules)
	if err != nil {
		return nil, err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrDirUnavailable, root, err)
	}
	walk := &scanner{
		ctx:      ctx,
		root:     root,
		realRoot: realRoot,
		rules:    set,
		plan:     &ScanPlan{Target: target, Root: root},
	}
	if err := walk.run(); err != nil {
		return nil, err
	}
	return walk.finish(), nil
}

// scanner accumulates one deterministic walk.
type scanner struct {
	ctx       context.Context
	root      string
	realRoot  string
	rules     *ruleSet
	plan      *ScanPlan
	entries   []FileEntry
	pruned    []ExcludedEntry
	warnings  []Warning
	truncated bool
	// visitedDirs guards symlink cycles when follow_symlinks=true.
	visitedDirs map[string]bool
}

// run performs the walk in lexical order (filepath.WalkDir) and enforces the
// file-count limit before the path order is disturbed.
func (w *scanner) run() error {
	err := filepath.WalkDir(w.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := w.ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			w.warn("unreadable", path, walkErr.Error())
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == w.root {
			return nil
		}
		rel, err := filepath.Rel(w.root, path)
		if err != nil {
			w.warn("unreadable", path, err.Error())
			return nil
		}
		rel = normalizeRelPath(rel)
		return w.visit(path, rel, entry)
	})
	if err != nil {
		return err
	}
	return nil
}

// visit dispatches one walk entry (regular file, directory or symlink).
func (w *scanner) visit(path, rel string, entry fs.DirEntry) error {
	if entry.IsDir() {
		if forcedExcludedDir(entry.Name()) {
			w.prune(rel+"/", ReasonCache)
			return filepath.SkipDir
		}
		if excluded, reason := w.rules.excluded(rel, true); excluded {
			w.prune(rel+"/", reason)
			return filepath.SkipDir
		}
		return nil
	}
	if entry.Type()&fs.ModeSymlink != 0 {
		return w.symlink(path, rel, entry)
	}
	if !entry.Type().IsRegular() {
		w.prune(rel, ReasonUnreadable)
		return nil
	}
	return w.file(path, rel, entry)
}

// symlink follows a link only when allowed and inside the scan root (§3.2).
func (w *scanner) symlink(path, rel string, entry fs.DirEntry) error {
	if w.atFileLimit() {
		return filepath.SkipAll
	}
	mtime := entryMTime(entry)
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		w.warn("unreadable", rel, err.Error())
		w.prune(rel, ReasonUnreadable)
		return nil
	}
	if !withinDir(w.realRoot, real) {
		w.addIneligible(rel, KindBinary, 0, mtime, ReasonSymlinkOutside)
		return nil
	}
	if !w.rules.rules.FollowSymlinks {
		w.addIneligible(rel, KindBinary, 0, mtime, ReasonSymlinkOutside)
		return nil
	}
	info, err := os.Stat(real)
	if err != nil {
		w.prune(rel, ReasonUnreadable)
		return nil
	}
	if info.IsDir() {
		return w.walkSymlinkDir(real, rel)
	}
	if !info.Mode().IsRegular() {
		w.prune(rel, ReasonUnreadable)
		return nil
	}
	return w.file(real, rel, fs.FileInfoToDirEntry(info))
}

// walkSymlinkDir scans an in-root symlinked directory (follow_symlinks=true).
// A visited-realpath set prevents cycles.
func (w *scanner) walkSymlinkDir(real, rel string) error {
	if w.visitedDirs == nil {
		w.visitedDirs = map[string]bool{}
	}
	if w.visitedDirs[real] {
		w.prune(rel+"/", ReasonSymlinkOutside)
		return nil
	}
	w.visitedDirs[real] = true
	err := filepath.WalkDir(real, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := w.ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			w.warn("unreadable", path, walkErr.Error())
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		sub, err := filepath.Rel(real, path)
		if err != nil {
			return nil
		}
		if sub == "." {
			return nil
		}
		return w.visit(path, rel+"/"+normalizeRelPath(sub), entry)
	})
	return err
}

// entryMTime returns the lstat mtime of one walk entry.
func entryMTime(entry fs.DirEntry) string {
	info, err := entry.Info()
	if err != nil {
		return ""
	}
	return clock.Format(info.ModTime())
}

// atFileLimit reports and records a reached manifest limit (§3.2).
func (w *scanner) atFileLimit() bool {
	if len(w.entries) < w.rules.rules.MaxFiles {
		return false
	}
	if !w.truncated {
		w.truncated = true
		w.warn("scan_truncated", "", fmt.Sprintf("file limit %d reached", w.rules.rules.MaxFiles))
	}
	return true
}

// file classifies, hashes and records one regular file.
func (w *scanner) file(path, rel string, entry fs.DirEntry) error {
	if excluded, reason := w.rules.excluded(rel, false); excluded {
		w.prune(rel, reason)
		return nil
	}
	if w.atFileLimit() {
		return filepath.SkipAll
	}
	info, err := entry.Info()
	if err != nil {
		w.warn("unreadable", rel, err.Error())
		w.addIneligible(rel, KindOther, 0, entryMTime(entry), ReasonUnreadable)
		return nil
	}
	kind, reason := classifyFile(rel, info.Size(), func(n int) []byte { return w.head(path, n) })
	file := FileEntry{
		Path:            rel,
		Kind:            kind,
		Size:            info.Size(),
		MTime:           clock.Format(info.ModTime()),
		ContentEligible: reason == "",
	}
	if reason != "" {
		file.ExclusionReason = &reason
	}
	if kind == KindImage {
		// Image understanding is not implemented in v1 (O-13): the manifest
		// and the ContextBundle must agree that images never enter AI content,
		// regardless of the include_images_in_ai switch.
		userExclude := ReasonUserExclude
		file.ContentEligible = false
		file.ExclusionReason = &userExclude
	}
	if info.Size() > w.rules.rules.MaxFileBytes && file.ContentEligible {
		tooLarge := ReasonTooLarge
		file.ContentEligible = false
		file.ExclusionReason = &tooLarge
	}
	if info.Size() > w.rules.rules.HashMaxBytes {
		skipped := hashSkipTooLarge
		file.HashSkipped = &skipped
	} else if !hashableKind(kind) {
		skipped := hashSkipBinary
		file.HashSkipped = &skipped
	} else {
		sum, err := hashFile(path)
		if err != nil {
			w.warn("unreadable", rel, err.Error())
			unreadable := ReasonUnreadable
			file.ContentEligible = false
			file.ExclusionReason = &unreadable
		} else {
			file.SHA256 = &sum
		}
	}
	w.entries = append(w.entries, file)
	return nil
}

// head reads at most n bytes from path for binary detection.
func (w *scanner) head(path string, n int) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, n)
	read, _ := io.ReadFull(f, buf)
	return buf[:read]
}

// addIneligible records a file that is listed but never read.
func (w *scanner) addIneligible(rel string, kind FileKind, size int64, mtime string, reason ExclusionReason) {
	file := FileEntry{
		Path:            rel,
		Kind:            kind,
		Size:            size,
		MTime:           mtime,
		ContentEligible: false,
		ExclusionReason: &reason,
	}
	w.entries = append(w.entries, file)
}

// prune records an excluded directory or file.
func (w *scanner) prune(rel string, reason ExclusionReason) {
	w.pruned = append(w.pruned, ExcludedEntry{Path: rel, Reason: reason})
}

// warn appends a bounded warning.
func (w *scanner) warn(code, path, message string) {
	if len(w.warnings) >= scanWarningLimit {
		return
	}
	w.warnings = append(w.warnings, Warning{Code: code, Path: path, Message: message})
}

// finish sorts the manifest and computes the totals.
func (w *scanner) finish() *ScanPlan {
	sort.Slice(w.entries, func(i, j int) bool { return w.entries[i].Path < w.entries[j].Path })
	sort.Slice(w.pruned, func(i, j int) bool { return w.pruned[i].Path < w.pruned[j].Path })
	plan := w.plan
	plan.Entries = w.entries
	plan.Pruned = w.pruned
	plan.Truncated = w.truncated
	plan.Warnings = w.warnings
	var total int64
	var included int64
	for _, entry := range w.entries {
		plan.TotalFiles++
		total += entry.Size
		if entry.ContentEligible {
			plan.IncludedFiles++
			included += min(entry.Size, w.rules.rules.MaxFileBytes)
		}
	}
	plan.TotalBytes = total
	plan.IncludedBytes = included
	plan.ContextBytes = min(included, w.rules.rules.MaxTotalBytes)
	return plan
}

// Run performs Plan plus deterministic extraction and a read-only Git
// inspection. It never writes the database or the external directory (§5.3).
func (s *Service) Run(ctx context.Context, target TargetRef, override *ScanRules) (*ScanResult, error) {
	plan, err := s.Plan(ctx, target, override)
	if err != nil {
		return nil, err
	}
	result := &ScanResult{Plan: *plan}
	metadata, err := s.ExtractMetadata(ctx, result)
	if err != nil {
		return nil, err
	}
	result.Metadata = *metadata
	result.Configs = metadata.Configs
	result.Warnings = append(result.Warnings, metadata.Warnings...)
	if _, err := os.Stat(plan.Root); err == nil {
		gitInfo, gitErr := s.Inspect(ctx, plan.Root)
		switch {
		case gitErr != nil:
			result.Warnings = append(result.Warnings, Warning{Code: "git_unavailable", Path: plan.Root, Message: gitErr.Error()})
		case gitInfo != nil && gitInfo.Detected:
			result.GitInfo = gitInfo
		}
	}
	return result, nil
}

// rulesForTarget returns the effective rules of a target without scanning.
func (s *Service) rulesForTarget(ctx context.Context, target TargetRef, override *ScanRules) (ScanRules, error) {
	_, rules, err := s.resolveTarget(ctx, target, override)
	return rules, err
}

// resolveTarget validates a target, resolves its scan root (run directory,
// else experiment directory) and merges rule overrides (§5.3).
func (s *Service) resolveTarget(ctx context.Context, target TargetRef, override *ScanRules) (string, ScanRules, error) {
	handle, err := s.handle(ctx)
	if err != nil {
		return "", ScanRules{}, err
	}
	var (
		root  string
		rules ScanRules
	)
	switch target.Type {
	case TargetExperiment:
		exp, err := s.loadExperiment(ctx, handle.DB(), target.ID, false)
		if err != nil {
			return "", ScanRules{}, err
		}
		if exp.DirRef == nil || strings.TrimSpace(*exp.DirRef) == "" {
			return "", ScanRules{}, fmt.Errorf("%w: experiment %s has no directory", ErrDirUnavailable, exp.ID)
		}
		root = *exp.DirRef
		rules = exp.ScanRules
	case TargetRun:
		run, err := s.loadRun(ctx, handle.DB(), target.ID, false)
		if err != nil {
			return "", ScanRules{}, err
		}
		exp, err := s.loadExperiment(ctx, handle.DB(), run.ExperimentID, false)
		if err != nil {
			return "", ScanRules{}, err
		}
		rules = exp.ScanRules
		switch {
		case run.DirRef != nil && strings.TrimSpace(*run.DirRef) != "":
			root = *run.DirRef
		case exp.DirRef != nil && strings.TrimSpace(*exp.DirRef) != "":
			root = *exp.DirRef
		default:
			return "", ScanRules{}, fmt.Errorf("%w: run %s has no directory", ErrDirUnavailable, run.ID)
		}
	default:
		return "", ScanRules{}, fmt.Errorf("%w: invalid target type %q", ErrInvalidInput, target.Type)
	}
	if err := ensureDirExists(root); err != nil {
		return "", ScanRules{}, err
	}
	if override != nil {
		rules = mergeRules(rules, override)
	}
	if err := rules.Validate(); err != nil {
		return "", ScanRules{}, err
	}
	return root, rules, nil
}

// mergeRules overlays the non-zero fields of override on base.
func mergeRules(base ScanRules, override *ScanRules) ScanRules {
	out := base.clone()
	if override.V != 0 {
		out.V = override.V
	}
	if override.Include != nil {
		out.Include = append([]string(nil), override.Include...)
	}
	if override.Exclude != nil {
		out.Exclude = append([]string(nil), override.Exclude...)
	}
	if override.MaxFileBytes > 0 {
		out.MaxFileBytes = override.MaxFileBytes
	}
	if override.MaxTotalBytes > 0 {
		out.MaxTotalBytes = override.MaxTotalBytes
	}
	if override.MaxFiles > 0 {
		out.MaxFiles = override.MaxFiles
	}
	if override.HashMaxBytes > 0 {
		out.HashMaxBytes = override.HashMaxBytes
	}
	out.FollowSymlinks = override.FollowSymlinks
	out.IncludeImagesInAI = override.IncludeImagesInAI
	return out
}

// ensureDirExists reports ErrDirUnavailable for anything but a real directory.
func ensureDirExists(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrDirUnavailable, dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrDirUnavailable, dir)
	}
	return nil
}

// forcedExcludedDir reports the repository metadata directories that can never
// be scanned (§3.2).
func forcedExcludedDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn":
		return true
	}
	return false
}

// isGitDir reports whether entry is the .git directory.
func isGitDir(entry fs.DirEntry) bool { return entry.IsDir() && entry.Name() == ".git" }

// withinDir reports whether target is root or below it.
func withinDir(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

// hashableKind reports whether a kind is worth hashing (§3.2).
func hashableKind(kind FileKind) bool {
	switch kind {
	case KindBinary:
		return false
	}
	return true
}

// hashFile streams a SHA-256 sum in hex.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// secretName reports the sensitive file names that never enter AI content.
func secretName(base string) bool {
	lower := strings.ToLower(base)
	switch {
	case strings.HasPrefix(lower, ".env"):
		return true
	case strings.HasSuffix(lower, ".pem"), strings.HasSuffix(lower, ".key"):
		return true
	case strings.HasPrefix(lower, "id_rsa"):
		return true
	case strings.HasPrefix(lower, "credentials"):
		return true
	case lower == ".netrc":
		return true
	}
	return false
}

// extension sets of §3.2.
var (
	modelWeightExts = map[string]bool{
		".pt": true, ".pth": true, ".ckpt": true, ".safetensors": true,
		".onnx": true, ".h5": true, ".hdf5": true, ".pkl": true, ".joblib": true,
		".bin": true, ".gguf": true,
	}
	archiveMediaExts = map[string]bool{
		".zip": true, ".tar": true, ".gz": true, ".tgz": true, ".7z": true,
		".rar": true, ".mp4": true, ".mov": true, ".avi": true,
	}
	binaryDataExts = map[string]bool{
		".parquet": true, ".arrow": true, ".feather": true, ".npz": true,
		".npy": true, ".hdf5": true, ".sqlite": true, ".db": true,
	}
	imageExts = map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".bmp": true,
		".tiff": true, ".svg": true,
	}
	codeExts = map[string]bool{
		".py": true, ".go": true, ".r": true, ".jl": true, ".m": true,
		".c": true, ".cc": true, ".cpp": true, ".h": true, ".hpp": true,
		".java": true, ".js": true, ".ts": true, ".tsx": true, ".jsx": true,
		".sh": true, ".ps1": true,
	}
	configExts = map[string]bool{
		".yaml": true, ".yml": true, ".toml": true, ".ini": true, ".cfg": true,
	}
	dataExts        = map[string]bool{".csv": true, ".tsv": true}
	metricJSONNames = []string{"metrics.json", "results.json", "result.json", "scores.json", "eval.json", "stats.json"}
)

// classifyFile maps one path to its kind and default exclusion. head is only
// called for an unknown extension (binary probe).
func classifyFile(rel string, size int64, head func(int) []byte) (FileKind, ExclusionReason) {
	base := filepath.Base(rel)
	lower := strings.ToLower(base)
	ext := strings.ToLower(filepath.Ext(base))
	parent := strings.ToLower(strings.TrimSuffix(filepath.Dir(rel), "."))

	if secretName(base) {
		return KindOther, ReasonSecret
	}
	if modelWeightExts[ext] {
		return KindBinary, ReasonModelWeight
	}
	if archiveMediaExts[ext] {
		return KindBinary, ReasonArchiveOrMedia
	}
	if binaryDataExts[ext] {
		return KindBinary, ReasonBinary
	}
	if imageExts[ext] {
		return KindImage, ""
	}
	if ext == ".ipynb" {
		return KindNotebook, ""
	}
	if dataExts[ext] {
		return KindData, ""
	}
	if ext == ".json" {
		if metricJSONName(lower) || strings.Contains(parent, "metric") || strings.Contains(parent, "result") {
			return KindMetrics, ""
		}
		return KindConfig, ""
	}
	if configExts[ext] {
		return KindConfig, ""
	}
	if ext == ".md" || strings.HasPrefix(lower, "readme") {
		return KindReadme, ""
	}
	if ext == ".log" || hasPathSegment(rel, "logs") {
		return KindLog, ""
	}
	if codeExts[ext] {
		return KindCode, ""
	}
	if ext == ".txt" {
		return KindOther, ""
	}
	if ext == "" && strings.Contains(lower, "makefile") {
		return KindCode, ""
	}
	if head != nil && containsNUL(head(8<<10)) {
		return KindBinary, ReasonBinary
	}
	return KindOther, ""
}

// hasPathSegment reports whether rel contains a directory named segment.
func hasPathSegment(rel, segment string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i := 0; i < len(parts)-1; i++ {
		if strings.EqualFold(parts[i], segment) {
			return true
		}
	}
	return false
}

// metricJSONName reports the conventional metric file names.
func metricJSONName(lowerBase string) bool {
	for _, name := range metricJSONNames {
		if lowerBase == name {
			return true
		}
	}
	return false
}

// containsNUL implements the binary probe of §3.2.
func containsNUL(head []byte) bool {
	for _, b := range head {
		if b == 0 {
			return true
		}
	}
	return false
}
