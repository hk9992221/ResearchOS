package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"researchos/internal/foundation/ids"
)

// MigrateMode selects the verification strength of a migration (SPEC-03 §5.10).
type MigrateMode string

const (
	// VerifyFast checks the file count, the total bytes and the full SHA256 of
	// every .db and .md file. It is the default: databases and notes are the
	// data that must not be lost or corrupted, and hashing everything else
	// (PDFs, attachments) mostly costs time.
	VerifyFast MigrateMode = "fast"
	// VerifyFull checks the file count, the total bytes and the full SHA256 of
	// every copied file.
	VerifyFull MigrateMode = "full"
)

// MigrateOptions configures a workspace migration (SPEC-03 §5.10).
type MigrateOptions struct {
	// NewRoot is the target workspace root. It must be empty (OS droppings
	// aside) or not exist yet with an existing writable parent.
	NewRoot string
	// IncludeExports copies the exports/ directory. When false the target gets
	// an empty exports/ directory so the workspace layout stays complete.
	IncludeExports bool
	// Verify is the verification strength. The empty value means VerifyFast.
	Verify MigrateMode
	// KeepSource preserves the old root. It must be true in this version: the
	// field exists so a future release can offer cleanup after user
	// confirmation (SPEC-03 §5.10).
	KeepSource bool
}

// MigratePlan is the read-only result of PlanMigration. Planning never creates
// the staging directory or touches the target.
type MigratePlan struct {
	ID          string
	SourceRoot  string
	TargetRoot  string
	StagingRoot string
	Files       int
	Bytes       int64
	Options     MigrateOptions
}

// MigrateProgress is one progress sample. Phase follows the flow:
// probe/lock/copy/verify/commit/reopen/done.
//
// Err is additive: the terminal sample carries the migration error (nil on
// success) so a subscriber that only reads the channel learns the outcome. The
// channel always closes when the job settles.
type MigrateProgress struct {
	Phase      string
	FilesDone  int
	FilesTotal int
	BytesDone  int64
	BytesTotal int64
	Current    string
	Err        error
}

// Migration phases (§5.10).
const (
	phaseProbe  = "probe"
	phaseLock   = "lock"
	phaseCopy   = "copy"
	phaseVerify = "verify"
	phaseCommit = "commit"
	phaseReopen = "reopen"
	phaseDone   = "done"
)

// stagingPrefix is the staging directory name inside the target root.
const stagingPrefix = ".researchos-migrate-"

// Migration sentinel errors. Callers match them with errors.Is.
var (
	// ErrBusy reports a running migration or an existing plan: only one
	// migration may exist at a time (§5.10).
	ErrBusy = errors.New("workspace: another migration is running")
	// ErrInvalidPlan reports a malformed or stale plan, an invalid option, or a
	// target the probe refused.
	ErrInvalidPlan = errors.New("workspace: invalid migration plan")
	// ErrInsufficientSpace reports a target volume that cannot hold the copy
	// with the 10% headroom of §5.10.
	ErrInsufficientSpace = errors.New("workspace: insufficient disk space")
	// ErrTargetNotEmpty reports a target that is not empty (other than OS
	// droppings). Migration never merges two workspaces (§5.10).
	ErrTargetNotEmpty = errors.New("workspace: migration target is not empty")
)

// migrateJob is one running migration. The plan and job live in memory only:
// an interrupted process leaves at most a stale staging directory, which the
// next plan of the same id could not pick up because ids are UUIDv7.
type migrateJob struct {
	plan   *MigratePlan
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	ch     chan MigrateProgress

	mu     sync.Mutex
	closed bool
	err    error
	last   MigrateProgress
}

// PlanMigration probes the target and the source and returns a read-only plan.
// It rejects the same/nested/foreign/non-empty/unwritable targets of §5.10, an
// unknown verification mode, KeepSource=false, a held workspace lock and an
// already existing plan with ErrBusy. No files are created or modified.
func (s *Service) PlanMigration(ctx context.Context, opts MigrateOptions) (*MigratePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.ws == nil {
		return nil, errors.New("workspace: plan migration: service has no workspace")
	}

	s.mu.Lock()
	running := s.active != nil
	if running {
		select {
		case <-s.active.done:
			running = false
		default:
		}
	}
	hasPlan := len(s.plans) > 0
	s.mu.Unlock()
	switch {
	case running:
		return nil, ErrBusy
	case hasPlan:
		return nil, fmt.Errorf("%w: a migration plan already exists", ErrBusy)
	}
	if s.ws.Global() == nil {
		return nil, errors.New("workspace: plan migration: workspace is closed")
	}

	// A project service sharing the locker holds this lock for writes that must
	// not overlap a migration; Probe here with an empty critical section so a
	// held lock fails fast instead of starting a doomed plan.
	if err := s.locker.WithWorkspaceLock(ctx, func() error { return nil }); err != nil {
		return nil, err
	}

	switch opts.Verify {
	case "":
		opts.Verify = VerifyFast
	case VerifyFast, VerifyFull:
	default:
		return nil, fmt.Errorf("%w: unknown verification mode %q", ErrInvalidPlan, opts.Verify)
	}
	if !opts.KeepSource {
		return nil, fmt.Errorf("%w: KeepSource must be true in this version", ErrInvalidPlan)
	}
	if strings.TrimSpace(opts.NewRoot) == "" {
		return nil, fmt.Errorf("%w: empty target root", ErrInvalidPlan)
	}

	probe, err := s.Probe(ctx, opts.NewRoot)
	if err != nil {
		return nil, err
	}
	switch {
	case probe.SameAsCurrent:
		return nil, fmt.Errorf("%w: %s is the current workspace root", ErrInvalidPlan, probe.Root)
	case probe.Nested:
		return nil, fmt.Errorf("%w: %s and the current workspace root %s nest", ErrInvalidPlan, probe.Root, s.ws.Root())
	case probe.Exists && !probe.Empty:
		return nil, fmt.Errorf("%w: %s (existing files are never merged)", ErrTargetNotEmpty, probe.Root)
	case !probe.Writable:
		return nil, fmt.Errorf("%w: %s is not writable: %s", ErrInvalidPlan, probe.Root, strings.Join(probe.Problems, "; "))
	case len(probe.Problems) > 0:
		return nil, fmt.Errorf("%w: %s: %s", ErrInvalidPlan, probe.Root, strings.Join(probe.Problems, "; "))
	}

	source := s.ws.Root()
	files, bytes, err := scanMigrationSource(ctx, source, opts.IncludeExports)
	if err != nil {
		return nil, err
	}
	free, _, ok := s.diskFree(probe.Root)
	if !ok {
		return nil, fmt.Errorf("%w: cannot determine free disk space for %s", ErrInvalidPlan, probe.Root)
	}
	required := bytes + (bytes+9)/10 // §5.10: expected size * 1.1, rounded up
	if free < required {
		return nil, fmt.Errorf("%w: %s needs %d bytes, %d available", ErrInsufficientSpace, probe.Root, required, free)
	}

	id, err := ids.New(s.ws.Clock())
	if err != nil {
		return nil, fmt.Errorf("workspace: plan migration: generate id: %w", err)
	}
	plan := &MigratePlan{
		ID:          id,
		SourceRoot:  source,
		TargetRoot:  probe.Root,
		StagingRoot: filepath.Join(probe.Root, stagingPrefix+id),
		Files:       files,
		Bytes:       bytes,
		Options:     opts,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// A second PlanMigration may have raced past the check above; only one plan
	// survives. Finished jobs stay in the registry so Wait remains idempotent
	// for any caller that still holds a plan id.
	if len(s.plans) > 0 || s.active != nil {
		return nil, fmt.Errorf("%w: a migration plan already exists", ErrBusy)
	}
	s.plans[id] = plan
	return plan, nil
}

// Migrate validates the plan and starts the migration. It returns a buffered
// progress channel that closes when the job settles; the copy runs in its own
// goroutine so the caller can render progress. Only one job may run at a time
// (ErrBusy).
func (s *Service) Migrate(ctx context.Context, planID string) (<-chan MigrateProgress, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.ws == nil {
		return nil, errors.New("workspace: migrate: service has no workspace")
	}

	s.mu.Lock()
	plan, ok := s.plans[planID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: unknown migration plan %q", ErrInvalidPlan, planID)
	}
	if s.active != nil {
		select {
		case <-s.active.done:
		default:
			s.mu.Unlock()
			return nil, ErrBusy
		}
	}
	if s.ws.Global() == nil {
		s.mu.Unlock()
		return nil, errors.New("workspace: migrate: workspace is closed")
	}
	delete(s.plans, planID)

	// The job outlives the Migrate call, so it must not inherit the caller's
	// cancellation; only CancelMigration stops the copy.
	jobCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	job := &migrateJob{
		plan:   plan,
		ctx:    jobCtx,
		cancel: cancel,
		done:   make(chan struct{}),
		ch:     make(chan MigrateProgress, 64),
	}
	s.jobs[planID] = job
	s.active = job
	s.mu.Unlock()

	go s.runMigration(job)
	return job.ch, nil
}

// CancelMigration cancels the running job. It is idempotent and returns nil for
// an unknown or already finished plan: cancelling something that is not running
// is not an error for the UI's cancel button. Cancellation is honored during
// the copy and verification phases; once the commit starts it progresses to a
// decisive end so the workspace cannot be left half-moved.
func (s *Service) CancelMigration(ctx context.Context, planID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	job := s.jobs[planID]
	s.mu.Unlock()
	if job == nil {
		return nil
	}
	job.cancel()
	return nil
}

// Wait blocks until the job settles and returns its error. It is the Await
// style companion of Migrate for callers and tests that do not consume
// progress. An unknown plan id is an error; a cancelled caller gets ctx.Err().
func (s *Service) Wait(ctx context.Context, planID string) error {
	if s == nil {
		return errors.New("workspace: wait: nil service")
	}
	s.mu.Lock()
	job := s.jobs[planID]
	s.mu.Unlock()
	if job == nil {
		return fmt.Errorf("%w: unknown migration %q", ErrInvalidPlan, planID)
	}
	select {
	case <-job.done:
		job.mu.Lock()
		defer job.mu.Unlock()
		return job.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runMigration takes the exclusive workspace lock and settles the job.
func (s *Service) runMigration(job *migrateJob) {
	err := s.locker.WithWorkspaceLock(job.ctx, func() error {
		return s.migrateLocked(job)
	})
	job.finish(err)
	s.mu.Lock()
	if s.active == job {
		s.active = nil
	}
	s.mu.Unlock()
	close(job.done)
}

// publish records and emits a progress sample. Samples are dropped oldest-first
// when the buffer is full, so the copy never blocks on a slow consumer and the
// terminal sample in finish is always retained.
func (job *migrateJob) publish(p MigrateProgress) {
	job.mu.Lock()
	if job.closed {
		job.mu.Unlock()
		return
	}
	job.last = p
	job.mu.Unlock()
	for {
		select {
		case job.ch <- p:
			return
		default:
		}
		select {
		case <-job.ch:
		default:
		}
	}
}

// finish stores the terminal error, emits the last sample (with Err) and closes
// the channel.
func (job *migrateJob) finish(err error) {
	job.mu.Lock()
	if job.closed {
		job.mu.Unlock()
		return
	}
	last := job.last
	last.Err = err
	if err == nil {
		last.Phase = phaseDone
	}
	job.err = err
	job.closed = true
	job.mu.Unlock()
	for {
		select {
		case job.ch <- last:
			close(job.ch)
			return
		default:
		}
		select {
		case <-job.ch:
		default:
		}
	}
}

// migrateLocked is the §5.10 flow. The workspace lock is already held.
func (s *Service) migrateLocked(job *migrateJob) error {
	plan := job.plan
	oldRoot := s.ws.Root()
	if oldRoot != plan.SourceRoot {
		return fmt.Errorf("%w: source root changed from %s to %s", ErrInvalidPlan, plan.SourceRoot, oldRoot)
	}

	job.publish(MigrateProgress{Phase: phaseProbe, FilesTotal: plan.Files, BytesTotal: plan.Bytes})
	// Re-check right before touching anything: the target may have received
	// files between planning and starting. A target that is no longer empty is
	// never merged into (§5.10).
	if err := checkMigrationTarget(plan.TargetRoot); err != nil {
		return err
	}

	job.publish(MigrateProgress{Phase: phaseLock, FilesTotal: plan.Files, BytesTotal: plan.Bytes})
	if err := s.ws.Close(); err != nil {
		// Close may have closed only some handles; reopen so the workspace
		// keeps working even though the migration never started.
		_ = s.ws.Reopen(context.Background())
		return fmt.Errorf("workspace: migrate: close handles: %w", err)
	}

	var (
		moved  []string
		routed bool // Repoint was attempted: config may point at the target
	)
	fail := func(err error) error {
		var problems []error
		problems = append(problems, err)
		if removeErr := os.RemoveAll(plan.StagingRoot); removeErr != nil {
			problems = append(problems, fmt.Errorf("workspace: migrate: remove staging: %w", removeErr))
		}
		if !routed {
			// The target was empty before this job, so anything renamed into it
			// belongs to this failed attempt and is safe to discard. The old
			// root is untouched.
			for _, name := range moved {
				if removeErr := os.RemoveAll(filepath.Join(plan.TargetRoot, name)); removeErr != nil {
					problems = append(problems, fmt.Errorf("workspace: migrate: clean target %s: %w", name, removeErr))
				}
			}
		}
		if routed || s.ws.Root() != oldRoot {
			if rollbackErr := s.rollbackTo(oldRoot); rollbackErr != nil {
				problems = append(problems, rollbackErr)
			}
		} else if reopenErr := s.ws.Reopen(context.Background()); reopenErr != nil {
			problems = append(problems, fmt.Errorf("workspace: migrate: reopen old root: %w", reopenErr))
		}
		return errors.Join(problems...)
	}

	// Staging lives inside the target, so the final rename stays on one volume.
	// A missing target is created here (the plan already proved the parent is
	// writable); an existing empty target is reused.
	if err := os.MkdirAll(plan.TargetRoot, 0o755); err != nil {
		return fail(fmt.Errorf("workspace: migrate: create target: %w", err))
	}
	if err := os.Mkdir(plan.StagingRoot, 0o755); err != nil {
		return fail(fmt.Errorf("workspace: migrate: create staging: %w", err))
	}

	manifest, err := s.copyTree(job, plan)
	if err != nil {
		return fail(err)
	}
	if s.hookAfterCopy != nil {
		if err := s.hookAfterCopy(plan); err != nil {
			return fail(err)
		}
	}
	if err := verifyMigration(job, plan, manifest); err != nil {
		return fail(err)
	}
	if s.hookBeforeCommit != nil {
		if err := s.hookBeforeCommit(plan); err != nil {
			return fail(err)
		}
	}
	// Cancellation is honored up to this point; the commit itself must reach a
	// decisive end or roll back as a unit.
	if err := job.ctx.Err(); err != nil {
		return fail(err)
	}
	if err := checkTargetCommit(plan.TargetRoot, plan.StagingRoot); err != nil {
		return fail(err)
	}

	// §5.10 step 6a: lift every top-level entry out of staging. The target was
	// empty, so each rename is a plain move on the same volume.
	job.publish(MigrateProgress{Phase: phaseCommit, FilesTotal: plan.Files, BytesTotal: plan.Bytes})
	entries, err := os.ReadDir(plan.StagingRoot)
	if err != nil {
		return fail(fmt.Errorf("workspace: migrate: read staging: %w", err))
	}
	for _, entry := range entries {
		src := filepath.Join(plan.StagingRoot, entry.Name())
		dst := filepath.Join(plan.TargetRoot, entry.Name())
		if err := os.Rename(src, dst); err != nil {
			return fail(fmt.Errorf("workspace: migrate: commit %s: %w", entry.Name(), err))
		}
		moved = append(moved, entry.Name())
	}
	// §5.10: an excluded exports/ directory is recreated empty so the layout
	// stays complete.
	if err := os.MkdirAll(filepath.Join(plan.TargetRoot, "exports"), 0o755); err != nil {
		return fail(fmt.Errorf("workspace: migrate: create exports: %w", err))
	}

	// §5.10 step 6b/7: atomic config commit, then reopen at the new root. The
	// commit context ignores cancellation: cancelling here would leave config
	// and handles in an undecidable state, so cancel wins only before commit.
	commitCtx := context.WithoutCancel(job.ctx)
	routed = true
	job.publish(MigrateProgress{Phase: phaseReopen, FilesTotal: plan.Files, FilesDone: plan.Files, BytesTotal: plan.Bytes, BytesDone: plan.Bytes})
	if err := s.ws.Repoint(commitCtx, plan.TargetRoot); err != nil {
		return fail(fmt.Errorf("workspace: migrate: repoint to %s: %w", plan.TargetRoot, err))
	}
	if err := s.ws.Reopen(commitCtx); err != nil {
		return fail(fmt.Errorf("workspace: migrate: reopen %s: %w", plan.TargetRoot, err))
	}

	// §5.10 step 8: staging is empty now; remove it. The old root is kept as
	// the user's backup and is never deleted by the application.
	_ = os.Remove(plan.StagingRoot)
	if s.onMoved != nil {
		if err := s.onMoved(commitCtx, oldRoot, s.ws.Root()); err != nil {
			// Data and config are already at the new root: this is not a
			// rollback case, but the caller must learn that modules may hold
			// stale paths.
			return fmt.Errorf("workspace: migrate: on moved callback: %w", err)
		}
	}
	job.publish(MigrateProgress{Phase: phaseDone, FilesTotal: plan.Files, FilesDone: plan.Files, BytesTotal: plan.Bytes, BytesDone: plan.Bytes})
	return nil
}

// rollbackTo restores the configured root after a failed commit and reopens the
// workspace there. The target tree is left in place: the old root still holds
// every byte, and deleting user-visible data after a partial commit is worse
// than leaving a copy behind.
func (s *Service) rollbackTo(oldRoot string) error {
	var problems []error
	if s.ws.Root() != oldRoot {
		if err := s.ws.Repoint(context.Background(), oldRoot); err != nil {
			problems = append(problems, fmt.Errorf("workspace: migrate: rollback config to %s: %w", oldRoot, err))
			// Without a restored root Reopen would open the wrong directory.
			return errors.Join(problems...)
		}
	}
	if err := s.ws.Reopen(context.Background()); err != nil {
		problems = append(problems, fmt.Errorf("workspace: migrate: reopen %s: %w", oldRoot, err))
	}
	return errors.Join(problems...)
}

// checkMigrationTarget rejects a target that is not missing or empty (OS
// droppings aside).
func checkMigrationTarget(target string) error {
	info, err := os.Stat(target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		parent := filepath.Dir(target)
		parentInfo, parentErr := os.Stat(parent)
		if parentErr != nil || !parentInfo.IsDir() {
			return fmt.Errorf("%w: parent directory %s does not exist", ErrInvalidPlan, parent)
		}
		return nil
	case err != nil:
		return fmt.Errorf("%w: cannot inspect %s: %v", ErrInvalidPlan, target, err)
	case !info.IsDir():
		return fmt.Errorf("%w: %s is not a directory", ErrInvalidPlan, target)
	}
	empty, err := dirEmpty(target)
	if err != nil {
		return fmt.Errorf("%w: cannot read %s: %v", ErrInvalidPlan, target, err)
	}
	if !empty {
		return fmt.Errorf("%w: %s", ErrTargetNotEmpty, target)
	}
	return nil
}

// checkTargetCommit verifies that between planning and commit nobody added
// entries beside the staging directory.
func checkTargetCommit(target, staging string) error {
	entries, err := os.ReadDir(target)
	if err != nil {
		return fmt.Errorf("%w: cannot read %s: %v", ErrInvalidPlan, target, err)
	}
	for _, entry := range entries {
		if filepath.Join(target, entry.Name()) == staging {
			continue
		}
		switch entry.Name() {
		case ".DS_Store", "desktop.ini":
			continue
		}
		return fmt.Errorf("%w: %s gained entries during the migration", ErrTargetNotEmpty, target)
	}
	return nil
}
