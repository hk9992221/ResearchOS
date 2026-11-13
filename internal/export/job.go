package export

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"researchos/internal/foundation/events"
	fpaths "researchos/internal/foundation/paths"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/storage"
	fworkspace "researchos/internal/foundation/workspace"
)

// job is one running export. The job registry is in memory: an interrupted
// process loses the job, which is exactly what a user restart implies.
type job struct {
	id     string
	planID string
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	closed   bool
	subs     []chan Progress
	progress Progress
	result   *Result
	err      error
}

// Start validates the plan and launches the export goroutine. It returns the
// job id, which equals the plan id because one plan yields at most one job.
func (s *Service) Start(ctx context.Context, planID string) (string, error) {
	if err := s.checkReady(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	s.mu.Lock()
	st, ok := s.plans[planID]
	if !ok {
		s.mu.Unlock()
		return "", fmt.Errorf("export: unknown plan %q", planID)
	}
	if s.active != nil {
		select {
		case <-s.active.done:
		default:
			s.mu.Unlock()
			return "", ErrBusy
		}
	}
	if s.clock.Now().After(st.plan.ExpiresAt) {
		delete(s.plans, planID)
		s.mu.Unlock()
		return "", ErrPlanExpired
	}

	// The job outlives the Start call, so it must not inherit the caller's
	// cancellation; only Cancel stops it.
	jobCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	j := &job{
		id:     st.plan.ID,
		planID: planID,
		ctx:    jobCtx,
		cancel: cancel,
		done:   make(chan struct{}),
		progress: Progress{
			Phase:      PhasePlanning,
			FilesTotal: len(st.plan.Files),
			BytesTotal: st.plan.TotalBytes,
		},
	}
	delete(s.plans, planID)
	s.jobs[j.id] = j
	s.active = j
	s.mu.Unlock()

	go s.run(j, st)
	return j.id, nil
}

// Progress subscribes to a job's progress stream. The channel is buffered and
// closes when the job settles; slow consumers lose samples instead of blocking
// the copy loop. An unknown job id is an error.
func (s *Service) Progress(ctx context.Context, jobID string) (<-chan Progress, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	j := s.jobs[jobID]
	s.mu.Unlock()
	if j == nil {
		return nil, fmt.Errorf("export: unknown job %q", jobID)
	}
	return j.subscribe(), nil
}

// Cancel stops a running job and is idempotent. An unknown or already finished
// job is a no-op that returns nil: cancelling something that is not running is
// not an error for the UI's "cancel" button.
func (s *Service) Cancel(ctx context.Context, jobID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	j := s.jobs[jobID]
	s.mu.Unlock()
	if j == nil {
		return nil
	}
	j.cancel()
	return nil
}

// Await blocks until the job settles and returns its result. On failure the
// result is nil; on cancellation the error wraps context.Canceled.
func (s *Service) Await(ctx context.Context, jobID string) (*Result, error) {
	s.mu.Lock()
	j := s.jobs[jobID]
	s.mu.Unlock()
	if j == nil {
		return nil, fmt.Errorf("export: unknown job %q", jobID)
	}
	select {
	case <-j.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.result, j.err
}

// subscribe registers one progress channel.
func (j *job) subscribe() <-chan Progress {
	ch := make(chan Progress, 64)
	j.mu.Lock()
	if j.closed {
		close(ch)
	} else {
		j.subs = append(j.subs, ch)
	}
	j.mu.Unlock()
	return ch
}

// publish fans one sample out without blocking the copy loop.
func (j *job) publish(p Progress) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return
	}
	j.progress = p
	for _, ch := range j.subs {
		select {
		case ch <- p:
		default:
		}
	}
}

// finish stores the outcome and closes every subscriber.
func (j *job) finish(res *Result, err error) {
	j.mu.Lock()
	if j.closed {
		j.mu.Unlock()
		return
	}
	j.result = res
	j.err = err
	j.closed = true
	for _, ch := range j.subs {
		close(ch)
	}
	j.subs = nil
	j.mu.Unlock()
	close(j.done)
}

// run executes the plan and records the duration.
func (s *Service) run(j *job, st *planState) {
	started := s.clock.Now()
	res, err := s.execute(j, st)
	if res != nil {
		res.Duration = s.clock.Now().Sub(started)
	}
	j.finish(res, err)
}

// executor holds the mutable state of one running bundle.
type executor struct {
	s        *Service
	job      *job
	st       *planState
	staging  string
	sink     bundleSink
	partPath string
	handle   *fworkspace.ProjectHandle

	files     []manifestFile
	counts    manifestCounts
	written   map[string]bool
	missing   []MissingRef
	warnings  []Warning
	filesDone int
	bytesDone int64
	closed    bool

	// lastManifestBytes is the serialized manifest size, reported through
	// Result.Bytes alongside the regular entries.
	lastManifestBytes int64
}

// execute runs the §5.9 pipeline: snapshot and notes under the short write
// lock, attachments and papers without it, then meta, manifest and the atomic
// publish. Every failure path removes the staging tree and .part file and
// leaves existing outputs untouched.
func (s *Service) execute(j *job, st *planState) (*Result, error) {
	plan := st.plan
	format := plan.Request.Format

	if err := os.MkdirAll(plan.StagingPath, fpaths.DirMode); err != nil {
		return nil, fmt.Errorf("export: %s: create staging: %w", PhasePlanning, err)
	}
	e := &executor{
		s:        s,
		job:      j,
		st:       st,
		staging:  plan.StagingPath,
		written:  make(map[string]bool),
		missing:  append([]MissingRef{}, plan.Missing...),
		warnings: append([]Warning{}, plan.Warnings...),
	}
	sink, part, err := newSink(format, plan.StagingPath, filepath.Dir(plan.OutputPath), st.base)
	if err != nil {
		_ = os.RemoveAll(e.staging)
		return nil, fmt.Errorf("export: %s: open output: %w", PhasePlanning, err)
	}
	e.sink = sink
	e.partPath = part

	published := false
	defer func() {
		if published {
			return
		}
		_ = e.closeSink()
		if e.partPath != "" {
			_ = os.Remove(e.partPath)
		}
		_ = os.RemoveAll(e.staging)
	}()

	handle, err := s.ws.Project(j.ctx, st.project.ID)
	if err != nil {
		return nil, fmt.Errorf("export: %s: open project database: %w", PhaseSnapshotDB, err)
	}
	e.handle = handle

	// §5.9: the write lock covers only the database snapshot and the notes.
	e.emitPhase(PhaseSnapshotDB)
	var innerErr error
	lockErr := s.locker.WithProjectWriteLock(j.ctx, st.project.ID, func() error {
		if err := e.snapshot(j.ctx); err != nil {
			innerErr = err
			return nil
		}
		innerErr = e.copyKind(j.ctx, KindNoteMD, PhaseCopyNotes)
		return nil
	})
	if innerErr != nil {
		return nil, innerErr
	}
	if lockErr != nil {
		if j.ctx.Err() != nil {
			return nil, e.wrap(PhaseSnapshotDB, "", j.ctx.Err())
		}
		return nil, fmt.Errorf("export: %s: %w (%v)", PhaseSnapshotDB, ErrBusy, lockErr)
	}

	if err := e.copyKind(j.ctx, KindAttachment, PhaseCopyAttachments); err != nil {
		return nil, err
	}
	if err := e.copyKind(j.ctx, KindPaperPDF, PhaseCopyPapers); err != nil {
		return nil, err
	}
	if err := e.finalize(j.ctx); err != nil {
		return nil, err
	}
	published = true

	// The bundle exists now; publish before the Activity so a cancelled caller
	// still sees the finished files. The event itself must not be lost to a
	// cancelled context, hence WithoutCancel (§5.4 cross-database rule).
	res := e.result()
	if err := e.emitActivity(context.WithoutCancel(j.ctx)); err != nil {
		return res, fmt.Errorf("export: finished but activity was not written: %w", err)
	}
	return res, nil
}

// wrap names the phase and path in a failure, as §5.9 requires.
func (e *executor) wrap(phase Phase, path string, err error) error {
	if path == "" {
		return fmt.Errorf("export: %s: %w", phase, err)
	}
	return fmt.Errorf("export: %s: %s: %w", phase, path, err)
}

// emitPhase publishes a phase transition with the current counters.
func (e *executor) emitPhase(phase Phase) {
	e.job.publish(Progress{
		Phase:      phase,
		FilesDone:  e.filesDone,
		FilesTotal: len(e.st.plan.Files),
		BytesDone:  e.bytesDone,
		BytesTotal: e.st.plan.TotalBytes,
	})
}

// emitFile publishes the entry currently being copied.
func (e *executor) emitFile(phase Phase, current string) {
	e.job.publish(Progress{
		Phase:      phase,
		FilesDone:  e.filesDone,
		FilesTotal: len(e.st.plan.Files),
		BytesDone:  e.bytesDone,
		BytesTotal: e.st.plan.TotalBytes,
		Current:    current,
	})
}

// snapshot writes the consistent database copy. VACUUM INTO is preferred; the
// checkpoint+copy fallback verifies PRAGMA integrity_check because it copies
// the live files.
func (e *executor) snapshot(ctx context.Context) error {
	bundlePath := "workspace/" + e.s.ws.Paths().ProjectDB(e.st.project.ID)
	if placer, ok := e.sink.(directPlacer); ok {
		dest, err := placer.placePath(bundlePath)
		if err != nil {
			return e.wrap(PhaseSnapshotDB, bundlePath, err)
		}
		if err := e.vacuumInto(ctx, dest); err != nil {
			return e.wrap(PhaseSnapshotDB, bundlePath, err)
		}
		sha, n, err := hashFile(ctx, dest)
		if err != nil {
			return e.wrap(PhaseSnapshotDB, bundlePath, err)
		}
		e.addEntry(bundlePath, KindProjectDB, sha, n)
		e.emitFile(PhaseSnapshotDB, bundlePath)
		return nil
	}

	tmp := filepath.Join(e.staging, "snapshot.db")
	if err := e.vacuumInto(ctx, tmp); err != nil {
		return e.wrap(PhaseSnapshotDB, bundlePath, err)
	}
	sha, n, err := e.sink.addFile(ctx, bundlePath, tmp, KindProjectDB)
	_ = os.Remove(tmp)
	if err != nil {
		return e.wrap(PhaseSnapshotDB, bundlePath, err)
	}
	e.addEntry(bundlePath, KindProjectDB, sha, n)
	e.emitFile(PhaseSnapshotDB, bundlePath)
	return nil
}

// vacuumInto materializes a consistent copy of the project database at dst.
func (e *executor) vacuumInto(ctx context.Context, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), fpaths.DirMode); err != nil {
		return err
	}
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := e.handle.DB().ExecContext(ctx, `VACUUM INTO ?`, dst); err == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Fallback: checkpoint, byte-copy and verify.
	if _, err := e.handle.DB().ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("wal checkpoint: %w", err)
	}
	src, err := e.s.ws.Paths().Abs(e.s.ws.Paths().ProjectDB(e.st.project.ID))
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fpaths.FileMode)
	if err != nil {
		in.Close()
		return err
	}
	_, _, copyErr := hashStream(ctx, out, in)
	closeErr := out.Close()
	in.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return closeErr
	}
	db, err := storage.OpenReadOnly(ctx, dst)
	if err != nil {
		return err
	}
	defer db.Close()
	var check string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&check); err != nil {
		return err
	}
	if !strings.EqualFold(check, "ok") {
		return fmt.Errorf("integrity check failed: %s", check)
	}
	return nil
}

// copyKind streams every planned entry of one kind.
func (e *executor) copyKind(ctx context.Context, kind FileKind, phase Phase) error {
	e.emitPhase(phase)
	for _, pf := range e.st.plan.Files {
		if pf.Kind != kind {
			continue
		}
		if err := ctx.Err(); err != nil {
			return e.wrap(phase, pf.BundlePath, err)
		}
		if e.s.hook != nil {
			e.s.hook(string(phase), pf.BundlePath)
		}
		e.emitFile(phase, pf.BundlePath)
		if err := e.writePlanned(ctx, pf); err != nil {
			return e.wrap(phase, pf.BundlePath, err)
		}
	}
	return nil
}

// writePlanned copies one planned file and records its manifest entry.
func (e *executor) writePlanned(ctx context.Context, pf PlannedFile) error {
	if pf.Kind == KindAttachment {
		return e.writeAttachment(ctx, pf)
	}
	sha, n, err := e.sink.addFile(ctx, pf.BundlePath, pf.SourceAbs, pf.Kind)
	if err != nil {
		return err
	}
	if pf.Kind == KindPaperPDF {
		e.checkPaperHash(pf.BundlePath, sha)
	}
	e.addEntry(pf.BundlePath, pf.Kind, sha, n)
	e.emitFile(phaseForKind(pf.Kind), "")
	return nil
}

// writeAttachment copies an attachment and detects concurrent modification.
//
// The folder writer can overwrite its staged copy, so a changed file is copied
// once more as §5.9 asks. The streaming archive writers cannot rewrite an entry,
// so there the change is reported as changed_during_copy instead; tar.gz goes
// through a stable staging copy because a size change would corrupt the tar
// stream.
func (e *executor) writeAttachment(ctx context.Context, pf PlannedFile) error {
	format := e.st.plan.Request.Format
	if format == FormatTarGz {
		tmp := filepath.Join(e.staging, "attachment.tmp")
		changed, err := stageStable(ctx, pf.SourceAbs, tmp)
		if err != nil {
			return err
		}
		if changed {
			e.warnings = append(e.warnings, Warning{Code: "changed_during_copy", Path: pf.BundlePath})
		}
		sha, n, err := e.sink.addFile(ctx, pf.BundlePath, tmp, KindAttachment)
		_ = os.Remove(tmp)
		if err != nil {
			return err
		}
		e.addEntry(pf.BundlePath, KindAttachment, sha, n)
		e.emitFile(PhaseCopyAttachments, "")
		return nil
	}

	before, err := os.Stat(pf.SourceAbs)
	if err != nil {
		return err
	}
	sha, n, err := e.sink.addFile(ctx, pf.BundlePath, pf.SourceAbs, KindAttachment)
	if err != nil {
		return err
	}
	after, err := os.Stat(pf.SourceAbs)
	if err != nil {
		return err
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		if format == FormatFolder {
			sha, n, err = e.sink.addFile(ctx, pf.BundlePath, pf.SourceAbs, KindAttachment)
			if err != nil {
				return err
			}
			if again, statErr := os.Stat(pf.SourceAbs); statErr == nil {
				if after.Size() != again.Size() || !after.ModTime().Equal(again.ModTime()) {
					e.warnings = append(e.warnings, Warning{Code: "changed_during_copy", Path: pf.BundlePath})
				}
			}
		} else {
			e.warnings = append(e.warnings, Warning{Code: "changed_during_copy", Path: pf.BundlePath})
		}
	}
	e.addEntry(pf.BundlePath, KindAttachment, sha, n)
	e.emitFile(PhaseCopyAttachments, "")
	return nil
}

// stageStable copies src to dst and retries once when the source changes
// between the copy and the follow-up stat. A still-changing source keeps the
// last copy and yields changed_during_copy via the returned warning.
func stageStable(ctx context.Context, src, dst string) (changed bool, err error) {
	for attempt := 0; attempt < 2; attempt++ {
		before, err := os.Stat(src)
		if err != nil {
			return false, err
		}
		if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fpaths.FileMode)
		if err != nil {
			return false, err
		}
		if err := copyFileInto(ctx, src, out); err != nil {
			out.Close()
			return false, err
		}
		if err := out.Close(); err != nil {
			return false, err
		}
		after, err := os.Stat(src)
		if err != nil {
			return false, err
		}
		if before.Size() == after.Size() && before.ModTime().Equal(after.ModTime()) {
			return false, nil
		}
	}
	return true, nil
}

// copyFileInto streams src into an already open destination.
func copyFileInto(ctx context.Context, srcPath string, dst *os.File) error {
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()
	buf := getBuffer()
	defer putBuffer(buf)
	_, err = copyCtx(ctx, dst, in, buf)
	return err
}

// checkPaperHash compares the copied PDF against the recorded hash. Only values
// that parse as SHA-256 are comparable; other recorded values (for example a
// Zotero attachment key) are left alone.
func (e *executor) checkPaperHash(bundlePath, actual string) {
	for i := range e.st.papers {
		p := &e.st.papers[i]
		if p.BundlePath != bundlePath {
			continue
		}
		expected := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(p.FileHash), "sha256:"))
		if len(expected) == 64 && expected != actual {
			e.warnings = append(e.warnings, Warning{Code: "hash_mismatch", Path: bundlePath})
		}
		return
	}
}

// addEntry records a written file in the manifest list and counters.
func (e *executor) addEntry(bundlePath string, kind FileKind, sha string, n int64) {
	e.files = append(e.files, manifestFile{Path: bundlePath, Bytes: n, SHA256: sha})
	e.written[bundlePath] = true
	switch kind {
	case KindNoteMD:
		e.counts.Notes++
	case KindAttachment:
		e.counts.Attachments++
	case KindPaperPDF:
		e.counts.Papers++
	}
	e.filesDone++
	e.bytesDone += n
}

// finalize writes meta JSON, verifies the planned/written correspondence, then
// writes the manifest and publishes the output atomically.
func (e *executor) finalize(ctx context.Context) error {
	plan := e.st.plan
	e.emitPhase(PhaseFinalize)

	projectJSON, err := metaProjectJSON(e.st.project)
	if err != nil {
		return e.wrap(PhaseFinalize, "meta/project.json", err)
	}
	sha, n, err := e.sink.addBytes(ctx, "meta/project.json", projectJSON)
	if err != nil {
		return e.wrap(PhaseFinalize, "meta/project.json", err)
	}
	e.addEntry("meta/project.json", KindMetaJSON, sha, n)

	papersJSON, err := metaPapersJSON(e.st.papers)
	if err != nil {
		return e.wrap(PhaseFinalize, "meta/papers.json", err)
	}
	sha, n, err = e.sink.addBytes(ctx, "meta/papers.json", papersJSON)
	if err != nil {
		return e.wrap(PhaseFinalize, "meta/papers.json", err)
	}
	e.addEntry("meta/papers.json", KindMetaJSON, sha, n)

	// §5.9 step 7: plan.Files and the written entries must correspond exactly.
	for _, pf := range plan.Files {
		if !e.written[pf.BundlePath] {
			return e.wrap(PhaseFinalize, pf.BundlePath, errors.New("planned file was not written"))
		}
	}
	if len(e.files) != len(plan.Files) {
		return fmt.Errorf("export: %s: wrote %d files, planned %d", PhaseFinalize, len(e.files), len(plan.Files))
	}

	globalVersion, err := querySchemaVersion(ctx, e.s.ws.Global().DB())
	if err != nil {
		return e.wrap(PhaseFinalize, "manifest.json", err)
	}
	projectVersion, err := querySchemaVersion(ctx, e.handle.DB())
	if err != nil {
		return e.wrap(PhaseFinalize, "manifest.json", err)
	}

	sortWarnings(e.warnings)
	sortMissing(e.missing)
	sort.Slice(e.files, func(i, j int) bool { return e.files[i].Path < e.files[j].Path })
	counts := e.counts
	counts.Files = len(e.files)
	counts.Bytes = 0
	for _, f := range e.files {
		counts.Bytes += f.Bytes
	}
	e.counts = counts

	m := buildManifest(e.st, plan.Request.Format, globalVersion, projectVersion,
		e.s.clock.Now(), e.files, counts, e.missing, e.warnings)
	data, err := marshalIndent(m)
	if err != nil {
		return e.wrap(PhaseFinalize, "manifest.json", err)
	}
	if _, n, err := e.sink.addBytes(ctx, "manifest.json", data); err != nil {
		return e.wrap(PhaseFinalize, "manifest.json", err)
	} else {
		e.lastManifestBytes = n
	}

	if err := e.closeSink(); err != nil {
		return e.wrap(PhaseFinalize, "manifest.json", err)
	}
	if err := e.publishOutput(); err != nil {
		return err
	}
	_ = os.RemoveAll(e.staging)
	e.emitPhase(PhaseFinalize)
	return nil
}

// publishOutput moves the completed container into place without overwriting.
func (e *executor) publishOutput() error {
	out := e.st.plan.OutputPath
	if _, err := os.Lstat(out); err == nil {
		return fmt.Errorf("export: %s: %w: %s", PhaseFinalize, ErrNameConflict, out)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("export: %s: %s: %w", PhaseFinalize, out, err)
	}
	source := e.staging
	if e.partPath != "" {
		source = e.partPath
	}
	if err := os.Rename(source, out); err != nil {
		return fmt.Errorf("export: %s: publish %s: %w", PhaseFinalize, out, err)
	}
	return nil
}

// closeSink finishes the container exactly once.
func (e *executor) closeSink() error {
	if e.closed {
		return nil
	}
	e.closed = true
	if e.sink == nil {
		return nil
	}
	return e.sink.finish()
}

// result converts the executor state into the public Result.
func (e *executor) result() *Result {
	res := &Result{
		JobID:      e.job.id,
		Path:       e.st.plan.OutputPath,
		Format:     e.st.plan.Request.Format,
		Bytes:      e.counts.Bytes + e.manifestBytes(),
		FileCount:  e.counts.Files,
		PaperCount: e.counts.Papers,
		Missing:    append([]MissingRef{}, e.missing...),
		Warnings:   append([]Warning{}, e.warnings...),
	}
	if as, ok := e.sink.(interface{ archiveSHA() string }); ok {
		res.ArchiveSHA256 = as.archiveSHA()
	}
	return res
}

// manifestBytes is the serialized manifest size, tracked for Result.Bytes.
func (e *executor) manifestBytes() int64 { return e.lastManifestBytes }

// emitActivity writes the §7.1 project.exported event after a successful
// finalize. It never carries an absolute path.
func (e *executor) emitActivity(ctx context.Context) error {
	summary := fmt.Sprintf("导出为 %s（%d 文件，%s）",
		e.st.plan.Request.Format.label(), e.counts.Files, humanBytes(e.counts.Bytes))
	ev := events.Event{
		Type:       schema.EventProjectExported,
		EntityType: schema.EntityProject,
		EntityID:   e.st.project.ID,
		Summary:    summary,
		Actor:      e.st.plan.Request.Actor,
	}
	return e.handle.Events().EmitStandalone(ctx, ev)
}

// querySchemaVersion reads the applied migration version.
func querySchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRowContext(ctx, `SELECT coalesce(max(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// phaseForKind maps a file kind to its progress phase.
func phaseForKind(kind FileKind) Phase {
	switch kind {
	case KindProjectDB:
		return PhaseSnapshotDB
	case KindNoteMD:
		return PhaseCopyNotes
	case KindAttachment:
		return PhaseCopyAttachments
	case KindPaperPDF:
		return PhaseCopyPapers
	default:
		return PhaseFinalize
	}
}

// humanBytes formats a byte count for the activity summary (77.5 MB style).
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
