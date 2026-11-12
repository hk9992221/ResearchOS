// Package externalrefs registers and probes external references (experiment
// directories and Git repositories) without ever writing to them (SPEC-01
// §5.12). Probing is os.Stat based: no repository object is read and no git
// command is executed.
package externalrefs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
)

// Field and kind/status literals of external_refs.
const (
	FieldDirRef  = "dir_ref"
	FieldGitRepo = "git_repo"

	KindDirectory     = "directory"
	KindFile          = "file"
	KindGitRepository = "git_repository"

	StatusOK     = "ok"
	StatusBroken = "broken"
)

// Ref is one external_refs row.
type Ref struct {
	ID            string
	OwnerType     events.EntityType // experiment | run
	OwnerID       string
	Field         string // dir_ref | git_repo
	Kind          string // directory | file | git_repository
	Path          string // absolute
	Status        string // ok | broken
	LastCheckedAt *time.Time
}

// Status is a probe result.
type Status struct {
	Ref
	Exists bool
	Reason string
}

// RelocateRequest asks to re-point one reference.
type RelocateRequest struct {
	ProjectID string
	RefID     string
	NewPath   string
}

// RelocateResult reports the re-pointed reference.
type RelocateResult struct {
	Status Status
}

// Service reads and writes external_refs of one project database.
type Service struct {
	db     *sql.DB
	clock  clock.Clock
	events events.Writer
}

// New binds the service to a project database and its event writer.
func New(db *sql.DB, writer events.Writer, c clock.Clock) *Service {
	if c == nil {
		c = clock.System{}
	}
	return &Service{db: db, clock: c, events: writer}
}

// Register creates or updates the (owner_type, owner_id, field) registration.
// The path must already exist and match the kind, else ErrRefNotFound. The
// caller composes this call with the owning-column write in the same tx.
func (s *Service) Register(ctx context.Context, tx *sql.Tx, r Ref) (string, error) {
	if tx == nil {
		return "", errors.New("externalrefs: Register requires a transaction")
	}
	if err := validateOwner(r.OwnerType, r.OwnerID); err != nil {
		return "", err
	}
	if r.Field != FieldDirRef && r.Field != FieldGitRepo {
		return "", fmt.Errorf("externalrefs: invalid field %q", r.Field)
	}
	if !validKind(r.Kind) {
		return "", fmt.Errorf("externalrefs: invalid ref kind %q", r.Kind)
	}
	if !filepath.IsAbs(r.Path) {
		return "", fmt.Errorf("externalrefs: path %q must be absolute", r.Path)
	}
	exists, reason := probe(r.Kind, r.Path)
	if !exists {
		return "", fmt.Errorf("%w: %s (%s)", errs.ErrRefNotFound, r.Path, reason)
	}
	now := clock.Format(s.clock.Now())

	var id string
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM external_refs WHERE owner_type = ? AND owner_id = ? AND field = ?`,
		string(r.OwnerType), r.OwnerID, r.Field).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		newID, idErr := ids.New(s.clock)
		if idErr != nil {
			return "", fmt.Errorf("externalrefs: generate id: %w", idErr)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO external_refs(id, owner_type, owner_id, field, ref_kind, path, status, last_checked_at, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, 'ok', ?, ?, ?)`,
			newID, string(r.OwnerType), r.OwnerID, r.Field, r.Kind, r.Path, now, now, now); err != nil {
			return "", fmt.Errorf("externalrefs: register: %w", err)
		}
		return newID, nil
	case err != nil:
		return "", fmt.Errorf("externalrefs: register: %w", err)
	default:
		if _, err := tx.ExecContext(ctx,
			`UPDATE external_refs SET ref_kind = ?, path = ?, status = 'ok', last_checked_at = ?, updated_at = ?
			 WHERE id = ?`, r.Kind, r.Path, now, now, id); err != nil {
			return "", fmt.Errorf("externalrefs: register: %w", err)
		}
		return id, nil
	}
}

// Check probes the given references read-only. An empty refIDs means all
// registered references.
func (s *Service) Check(ctx context.Context, projectID string, refIDs []string) ([]Status, error) {
	refs, err := s.load(ctx, refIDs)
	if err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(refs))
	for _, ref := range refs {
		out = append(out, probeStatus(ref))
	}
	return out, nil
}

// CheckAndRecord probes and writes status/last_checked_at in one transaction.
func (s *Service) CheckAndRecord(ctx context.Context, projectID string, refIDs []string) ([]Status, error) {
	statuses, err := s.Check(ctx, projectID, refIDs)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("externalrefs: check and record: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := clock.Format(s.clock.Now())
	for _, st := range statuses {
		status := StatusOK
		if !st.Exists {
			status = StatusBroken
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE external_refs SET status = ?, last_checked_at = ?, updated_at = ? WHERE id = ?`,
			status, now, now, st.ID); err != nil {
			return nil, fmt.Errorf("externalrefs: check and record: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("externalrefs: check and record: %w", err)
	}
	return statuses, nil
}

// CheckOwner probes every reference of one owner.
func (s *Service) CheckOwner(ctx context.Context, projectID string, ownerType events.EntityType, ownerID string) ([]Status, error) {
	refs, err := s.loadOwner(ctx, ownerType, ownerID)
	if err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(refs))
	for _, ref := range refs {
		out = append(out, probeStatus(ref))
	}
	return out, nil
}

// Relocate validates the new path and, in one transaction, updates the owning
// column (dir_ref only; git_repo has no baseline column), the reference row and
// emits entity.relocated. History is never cleared.
func (s *Service) Relocate(ctx context.Context, req RelocateRequest, actor events.Actor) (RelocateResult, error) {
	var result RelocateResult
	ref, ok, err := s.byID(ctx, req.RefID)
	if err != nil {
		return result, err
	}
	if !ok {
		return result, fmt.Errorf("%w: reference %s", errs.ErrRefNotFound, req.RefID)
	}
	if !filepath.IsAbs(req.NewPath) {
		return result, fmt.Errorf("externalrefs: path %q must be absolute", req.NewPath)
	}
	exists, reason := probe(ref.Kind, req.NewPath)
	if !exists {
		return result, fmt.Errorf("%w: %s (%s)", errs.ErrRefNotFound, req.NewPath, reason)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("externalrefs: relocate: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := clock.Format(s.clock.Now())

	if ref.Field == FieldDirRef {
		table, err := owningTable(ref.OwnerType)
		if err != nil {
			return result, err
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE `+table+` SET dir_ref = ?, updated_at = ? WHERE id = ?`, req.NewPath, now, ref.OwnerID)
		if err != nil {
			return result, fmt.Errorf("externalrefs: update owning entity: %w", err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return result, fmt.Errorf("externalrefs: update owning entity: %w", err)
		}
		if rows == 0 {
			return result, fmt.Errorf("externalrefs: owning %s %s not found", ref.OwnerType, ref.OwnerID)
		}
	}
	// git_repo has no baseline column in v1; only the reference row moves.
	if _, err := tx.ExecContext(ctx,
		`UPDATE external_refs SET path = ?, status = 'ok', last_checked_at = ?, updated_at = ? WHERE id = ?`,
		req.NewPath, now, now, req.RefID); err != nil {
		return result, fmt.Errorf("externalrefs: update reference: %w", err)
	}
	oldObj := events.Object{"path": ref.Path}
	newObj := events.Object{"path": req.NewPath}
	if err := s.events.Emit(ctx, tx, events.Event{
		Type:       schema.EventEntityRelocated,
		EntityType: ref.OwnerType,
		EntityID:   ref.OwnerID,
		Old:        &oldObj,
		New:        &newObj,
		Summary:    "重新定位",
		Actor:      actor,
		At:         s.clock.Now(),
	}); err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("externalrefs: relocate: %w", err)
	}

	checked := s.clock.Now()
	ref.Path = req.NewPath
	ref.Status = StatusOK
	ref.LastCheckedAt = &checked
	result.Status = Status{Ref: ref, Exists: true}
	return result, nil
}

func (s *Service) load(ctx context.Context, refIDs []string) ([]Ref, error) {
	query := `SELECT id, owner_type, owner_id, field, ref_kind, path, status, last_checked_at
	          FROM external_refs`
	var args []any
	if len(refIDs) > 0 {
		query += ` WHERE id IN (` + placeholders(len(refIDs)) + `)`
		for _, id := range refIDs {
			args = append(args, id)
		}
	}
	query += ` ORDER BY created_at, id`
	return s.queryRefs(ctx, query, args...)
}

func (s *Service) loadOwner(ctx context.Context, ownerType events.EntityType, ownerID string) ([]Ref, error) {
	return s.queryRefs(ctx,
		`SELECT id, owner_type, owner_id, field, ref_kind, path, status, last_checked_at
		 FROM external_refs WHERE owner_type = ? AND owner_id = ? ORDER BY created_at, id`,
		string(ownerType), ownerID)
}

func (s *Service) byID(ctx context.Context, id string) (Ref, bool, error) {
	refs, err := s.queryRefs(ctx,
		`SELECT id, owner_type, owner_id, field, ref_kind, path, status, last_checked_at
		 FROM external_refs WHERE id = ?`, id)
	if err != nil {
		return Ref{}, false, err
	}
	if len(refs) == 0 {
		return Ref{}, false, nil
	}
	return refs[0], true, nil
}

func (s *Service) queryRefs(ctx context.Context, query string, args ...any) ([]Ref, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("externalrefs: query: %w", err)
	}
	defer rows.Close()
	var out []Ref
	for rows.Next() {
		var (
			ref           Ref
			ownerType     string
			lastCheckedAt sql.NullString
		)
		if err := rows.Scan(&ref.ID, &ownerType, &ref.OwnerID, &ref.Field, &ref.Kind,
			&ref.Path, &ref.Status, &lastCheckedAt); err != nil {
			return nil, fmt.Errorf("externalrefs: scan: %w", err)
		}
		ref.OwnerType = events.EntityType(ownerType)
		if lastCheckedAt.Valid {
			at, err := clock.Parse(lastCheckedAt.String)
			if err != nil {
				return nil, fmt.Errorf("externalrefs: last_checked_at: %w", err)
			}
			ref.LastCheckedAt = &at
		}
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("externalrefs: query: %w", err)
	}
	return out, nil
}

func probeStatus(ref Ref) Status {
	exists, reason := probe(ref.Kind, ref.Path)
	return Status{Ref: ref, Exists: exists, Reason: reason}
}

// probe applies the §5.12 rules with os.Stat only.
func probe(kind, path string) (bool, string) {
	info, err := os.Stat(path)
	if err != nil {
		switch {
		case os.IsNotExist(err):
			return false, "不存在"
		case os.IsPermission(err):
			return false, "权限拒绝"
		default:
			return false, err.Error()
		}
	}
	switch kind {
	case KindDirectory:
		if !info.IsDir() {
			return false, "不是目录"
		}
		return true, ""
	case KindFile:
		if info.IsDir() {
			return false, "不是文件"
		}
		return true, ""
	case KindGitRepository:
		if !info.IsDir() {
			return false, "不是目录"
		}
		// A .git directory or a .git file (worktrees) both qualify.
		if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
			return false, "不是 git 仓库"
		}
		return true, ""
	default:
		return false, "未知引用类型"
	}
}

func validKind(kind string) bool {
	switch kind {
	case KindDirectory, KindFile, KindGitRepository:
		return true
	default:
		return false
	}
}

func validateOwner(ownerType events.EntityType, ownerID string) error {
	switch ownerType {
	case schema.EntityExperiment, schema.EntityRun:
	default:
		return fmt.Errorf("%w: external references belong to experiment or run, got %q", errs.ErrBadEntityType, ownerType)
	}
	if !ids.Valid(ownerID) {
		return fmt.Errorf("%w: %q", errs.ErrBadEntityID, ownerID)
	}
	return nil
}

func owningTable(ownerType events.EntityType) (string, error) {
	switch ownerType {
	case schema.EntityExperiment:
		return "experiments", nil
	case schema.EntityRun:
		return "runs", nil
	default:
		return "", fmt.Errorf("%w: %q", errs.ErrBadEntityType, ownerType)
	}
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
