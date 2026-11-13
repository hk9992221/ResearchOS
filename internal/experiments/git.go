package experiments

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"researchos/internal/foundation/clock"
)

// Git inspection is strictly read-only (§3.10, §5.10, O-07):
//   - repository discovery and HEAD/commit resolution read plain files;
//   - status and diff use the local git binary with a whitelist of read-only
//     subcommands, GIT_OPTIONAL_LOCKS=0, no pager, no external diff and no
//     textconv. No fetch, no index write, no network.
const (
	gitDiffDefaultMax = 256 << 10
	gitStatusLimit    = 20000
)

// Inspect reads branch, HEAD, commit time and status of the repository
// containing dir. A missing repository is not an error: Detected is false.
func (s *Service) Inspect(ctx context.Context, dir string) (*GitInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrDirUnavailable, dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s is not a directory", ErrDirUnavailable, dir)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("experiments: absolutize %q: %w", dir, err)
	}
	root, gitDir, ok := findGitRoot(abs)
	if !ok {
		return &GitInfo{V: payloadVersion, Detected: false}, nil
	}
	out := &GitInfo{
		V:          payloadVersion,
		Detected:   true,
		RepoRoot:   root,
		CapturedAt: clock.Format(s.clock.Now()),
		Source:     GitReadAtAnalysis,
	}
	out.Branch, out.Commit = readHead(gitDir)
	if len(out.Commit) >= 7 {
		out.CommitShort = out.Commit[:7]
	}
	if out.Commit != "" {
		out.CommitTime = readCommitTime(gitDir, out.Commit)
		if out.CommitTime == "" {
			out.CommitTime = gitCommitTime(ctx, root, out.Commit)
		}
	}
	changed, untracked, err := gitStatusCounts(ctx, root)
	if err != nil {
		// Git may be unavailable. The repository metadata above is still
		// authoritative; status counts stay zero rather than failing the scan.
		return out, nil
	}
	out.ChangedFiles = changed
	out.UntrackedFiles = untracked
	out.Dirty = changed > 0 || untracked > 0
	if changed+untracked > gitStatusLimit {
		out.Truncated = true
	}
	return out, nil
}

// Diff returns a read-only diff, truncated to opts.MaxBytes (§5.10).
func (s *Service) Diff(ctx context.Context, dir string, opts DiffOptions) (*GitDiff, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("experiments: absolutize %q: %w", dir, err)
	}
	root, _, ok := findGitRoot(abs)
	if !ok {
		return &GitDiff{Files: []GitDiffFile{}}, nil
	}
	if err := validateGitRevision(opts.From); err != nil {
		return nil, err
	}
	if err := validateGitRevision(opts.To); err != nil {
		return nil, err
	}
	for _, spec := range opts.Pathspec {
		if strings.HasPrefix(spec, "-") || strings.ContainsAny(spec, "\n\r\x00") {
			return nil, fmt.Errorf("%w: invalid pathspec %q", ErrInvalidInput, spec)
		}
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = gitDiffDefaultMax
	}
	baseArgs := []string{"-C", root, "--no-pager", "-c", "core.fsmonitor=false", "-c", "core.pager=cat", "-c", "diff.external="}
	common := []string{"diff", "--no-color", "--no-ext-diff", "--no-textconv"}
	switch {
	case opts.From != "" && opts.To != "":
		common = append(common, opts.From+".."+opts.To)
	case opts.From != "":
		common = append(common, opts.From)
	case opts.To != "":
		common = append(common, opts.To)
	}
	if len(opts.Pathspec) > 0 {
		common = append(common, "--")
		common = append(common, opts.Pathspec...)
	}

	statArgs := append(append([]string{}, common...), "--numstat", "-z")
	statOut, err := runGit(ctx, baseArgs, statArgs)
	if err != nil {
		return nil, fmt.Errorf("experiments: git diff --numstat: %w", err)
	}
	files := parseNumstat(statOut)

	patchArgs := append(append([]string{}, common...), "--patch")
	patchOut, err := runGit(ctx, baseArgs, patchArgs)
	if err != nil {
		return nil, fmt.Errorf("experiments: git diff --patch: %w", err)
	}
	result := &GitDiff{Files: files, Patch: string(patchOut)}
	if len(patchOut) > int(maxBytes) {
		result.Patch = string(patchOut[:maxBytes])
		result.Truncated = true
	}
	return result, nil
}

// runGit executes one whitelisted read-only git command.
func runGit(ctx context.Context, baseArgs, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append(baseArgs, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, errors.New(message)
	}
	return out, nil
}

// validateGitRevision rejects revisions that could be parsed as options.
func validateGitRevision(rev string) error {
	if rev == "" {
		return nil
	}
	if strings.HasPrefix(rev, "-") || strings.ContainsAny(rev, " \t\n\r\x00") {
		return fmt.Errorf("%w: invalid git revision %q", ErrInvalidInput, rev)
	}
	return nil
}

// parseNumstat reads `git diff --numstat -z` output.
func parseNumstat(raw []byte) []GitDiffFile {
	files := []GitDiffFile{}
	fields := bytes.Split(raw, []byte{0})
	for i := 0; i < len(fields); i++ {
		field := fields[i]
		if len(field) == 0 {
			continue
		}
		parts := bytes.SplitN(field, []byte{'\t'}, 3)
		if len(parts) != 3 {
			continue
		}
		file := GitDiffFile{Path: string(parts[2])}
		if bytes.Equal(parts[0], []byte("-")) || bytes.Equal(parts[1], []byte("-")) {
			file.Binary = true
		} else {
			file.Added, _ = strconv.Atoi(string(parts[0]))
			file.Deleted, _ = strconv.Atoi(string(parts[1]))
		}
		files = append(files, file)
	}
	return files
}

// gitStatusCounts counts tracked changes and untracked files read-only.
func gitStatusCounts(ctx context.Context, root string) (changed, untracked int, err error) {
	out, err := runGit(ctx, []string{
		"-C", root, "--no-pager",
		"-c", "core.fsmonitor=false", "-c", "core.pager=cat", "-c", "core.quotepath=false",
	}, []string{"status", "--porcelain=v1", "-z", "--untracked-files=normal"})
	if err != nil {
		return 0, 0, err
	}
	records := bytes.Split(out, []byte{0})
	renameSkip := false
	for _, record := range records {
		if len(record) < 2 {
			continue
		}
		if renameSkip {
			renameSkip = false
			continue
		}
		status := string(record[:2])
		switch {
		case status == "??":
			untracked++
		default:
			changed++
			if status[0] == 'R' || status[1] == 'R' || status[0] == 'C' || status[1] == 'C' {
				renameSkip = true
			}
		}
	}
	return changed, untracked, nil
}

// findGitRoot walks up from dir looking for .git (a directory or a worktree
// pointer file) and returns the repository root and its git directory.
func findGitRoot(dir string) (root, gitDir string, ok bool) {
	current := dir
	for {
		dotGit := filepath.Join(current, ".git")
		if info, err := os.Stat(dotGit); err == nil {
			if info.IsDir() {
				return current, dotGit, true
			}
			if info.Mode().IsRegular() {
				if resolved, err := readGitFile(dotGit); err == nil {
					return current, resolved, true
				}
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", "", false
		}
		current = parent
	}
}

// readGitFile resolves a worktree ".git" file ("gitdir: <path>").
func readGitFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(raw))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return "", errors.New("not a gitdir file")
	}
	target := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return filepath.Clean(target), nil
}

// commonDir resolves the shared git directory of a linked worktree.
func commonDir(gitDir string) string {
	raw, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return gitDir
	}
	target := strings.TrimSpace(string(raw))
	if !filepath.IsAbs(target) {
		target = filepath.Join(gitDir, target)
	}
	return filepath.Clean(target)
}

// readHead returns the branch name and the HEAD commit (pure file reads).
func readHead(gitDir string) (branch, commit string) {
	raw, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", ""
	}
	line := strings.TrimSpace(string(raw))
	if strings.HasPrefix(line, "ref: ") {
		ref := strings.TrimSpace(strings.TrimPrefix(line, "ref: "))
		branch = strings.TrimPrefix(ref, "refs/heads/")
		commit = resolveRef(gitDir, ref)
		return branch, commit
	}
	if isHexSHA(line) {
		return "", line
	}
	return "", ""
}

// resolveRef reads one symbolic ref from the git directory or packed-refs.
func resolveRef(gitDir, ref string) string {
	common := commonDir(gitDir)
	for _, dir := range []string{gitDir, common} {
		raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(ref)))
		if err == nil {
			if value := strings.TrimSpace(string(raw)); isHexSHA(value) {
				return value
			}
		}
	}
	packed, err := os.ReadFile(filepath.Join(common, "packed-refs"))
	if err != nil {
		return ""
	}
	scanner := bufio.NewScanner(bytes.NewReader(packed))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == ref {
			return parts[0]
		}
	}
	return ""
}

// readCommitTime decompresses the commit object and parses its committer time.
func readCommitTime(gitDir, commit string) string {
	if len(commit) < 3 {
		return ""
	}
	common := commonDir(gitDir)
	objectPath := filepath.Join(common, "objects", commit[:2], commit[2:])
	f, err := os.Open(objectPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	reader, err := zlib.NewReader(f)
	if err != nil {
		return ""
	}
	defer reader.Close()
	raw, err := io.ReadAll(io.LimitReader(reader, 1<<20))
	if err != nil {
		return ""
	}
	// Object header: "<type> <size>\x00<data>".
	headerEnd := bytes.IndexByte(raw, 0)
	if headerEnd < 0 {
		return ""
	}
	body := string(raw[headerEnd+1:])
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "committer ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			return ""
		}
		seconds, err := strconv.ParseInt(fields[len(fields)-2], 10, 64)
		if err != nil {
			return ""
		}
		return clock.Format(time.Unix(seconds, 0).UTC())
	}
	return ""
}

// gitCommitTime asks the local git for the committer time of one commit when
// the object is packed. It uses the read-only `git show` whitelist entry.
func gitCommitTime(ctx context.Context, root, commit string) string {
	out, err := runGit(ctx, []string{"-C", root, "--no-pager", "-c", "core.pager=cat"},
		[]string{"show", "-s", "--no-color", "--format=%cI", commit})
	if err != nil {
		return ""
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(string(out)))
	if err != nil {
		return ""
	}
	return clock.Format(at)
}

// isHexSHA reports a full lowercase or uppercase hex object id.
func isHexSHA(s string) bool {
	if len(s) < 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
