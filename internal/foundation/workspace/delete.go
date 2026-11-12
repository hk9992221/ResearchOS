package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/papers"
	"researchos/internal/foundation/paths"
	"researchos/internal/foundation/storage"
)

// DeleteProjectRequest authorizes deleting one project and its data.
type DeleteProjectRequest struct {
	ProjectID string
	Confirm   confirm.Token
}

// DeleteProjectResult reports a project deletion.
type DeleteProjectResult struct {
	RemovedProjects int
	RemovedDirs     int
	RemovedPapers   []string
	RetainedPapers  []string
	Warnings        []string
}

// PruneResult reports a paper-copy GC pass.
type PruneResult struct {
	DeletedPapers []string
	Retained      []string
	Warnings      []string
}

// DeleteProjectData permanently deletes a project: candidate papers first, then
// the directory, then the registration + compatibility settings, then the
// cross-project paper-copy GC (§5.9). A missing project.db aborts before
// anything is touched.
func (w *Workspace) DeleteProjectData(ctx context.Context, req DeleteProjectRequest) (DeleteProjectResult, error) {
	var res DeleteProjectResult
	if !ids.Valid(req.ProjectID) {
		return res, fmt.Errorf("%w: %q", errs.ErrBadEntityID, req.ProjectID)
	}

	w.mu.Lock()
	p := w.p
	global := w.global
	opts := w.opts
	w.mu.Unlock()
	if global == nil {
		return res, errors.New("workspace: delete project: workspace is closed")
	}
	if opts.Confirm == nil {
		return res, fmt.Errorf("%w: no confirmation service configured", errs.ErrUserConfirmationRequired)
	}

	var one int
	err := global.DB().QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id = ?`, req.ProjectID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return res, fmt.Errorf("workspace: delete project %s: %w", req.ProjectID, sql.ErrNoRows)
	}
	if err != nil {
		return res, fmt.Errorf("workspace: delete project %s: %w", req.ProjectID, err)
	}
	if err := opts.Confirm.Consume(ctx, req.Confirm, confirm.Scope{
		Kind:      confirm.KindDeleteProject,
		ProjectID: req.ProjectID,
	}); err != nil {
		return res, err
	}

	// Step 1: gather the candidate paper set while the project database still
	// exists. A database that cannot be opened aborts the whole deletion.
	projectDir, err := p.Abs(p.ProjectDir(req.ProjectID))
	if err != nil {
		return res, fmt.Errorf("workspace: delete project %s: %w", req.ProjectID, err)
	}
	var candidates []string
	dbPath := filepath.Join(projectDir, "project.db")
	if fileExists(dbPath) {
		candidates, err = candidatePapers(ctx, dbPath, opts.storageOptions())
		if err != nil {
			return res, fmt.Errorf("workspace: delete project %s: read project database: %w", req.ProjectID, err)
		}
	}

	// Step 2: delete the directory. On failure the registration row stays, so
	// the workspace never ends up with a registration but no discoverable data.
	w.mu.Lock()
	if h, ok := w.projects[req.ProjectID]; ok {
		_ = h.Close()
		delete(w.projects, req.ProjectID)
	}
	w.mu.Unlock()
	if err := os.RemoveAll(projectDir); err != nil {
		return res, fmt.Errorf("workspace: delete project %s: remove directory: %w", req.ProjectID, err)
	}
	res.RemovedDirs = 1

	// Step 3: registration row + project.<id>.* compatibility keys, one tx.
	if err := deleteProjectRegistration(ctx, global.DB(), req.ProjectID); err != nil {
		return res, fmt.Errorf("workspace: delete project %s: %w", req.ProjectID, err)
	}
	res.RemovedProjects = 1

	// Step 4: reference-count GC. Failures here do not roll back steps 1-3.
	prune, err := w.prunePapers(ctx, candidates)
	if err != nil {
		res.Warnings = append(res.Warnings, "paper GC: "+err.Error())
		return res, nil
	}
	res.RemovedPapers = prune.DeletedPapers
	res.RetainedPapers = prune.Retained
	res.Warnings = append(res.Warnings, prune.Warnings...)
	return res, nil
}

// PruneUnreferencedPapers is the idempotent compensation entry for the copy GC:
// a candidate survives unless every registered project has zero references to
// it across project_papers, entity_relations(dst_type='paper') and
// evidence(source_type='paper').
func (w *Workspace) PruneUnreferencedPapers(ctx context.Context, candidates []string) (PruneResult, error) {
	w.mu.Lock()
	global := w.global
	w.mu.Unlock()
	if global == nil {
		return PruneResult{}, errors.New("workspace: prune papers: workspace is closed")
	}
	return w.prunePapers(ctx, candidates)
}

// candidatePapers reads the §5.9 candidate set: the union of project_papers,
// entity_relations(dst_type='paper') and evidence(source_type='paper'). Soft
// deleted rows are included.
func candidatePapers(ctx context.Context, dbPath string, opt storage.Options) ([]string, error) {
	db, err := storage.OpenDatabase(ctx, dbPath, storage.KindProject, opt)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	queries := []string{
		`SELECT DISTINCT paper_id FROM project_papers`,
		`SELECT DISTINCT dst_id FROM entity_relations WHERE dst_type = 'paper'`,
		`SELECT DISTINCT source_id FROM evidence WHERE source_type = 'paper'`,
	}
	seen := make(map[string]struct{})
	for _, query := range queries {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", query, err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			seen[id] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return sortedKeys(seen), nil
}

func deleteProjectRegistration(ctx context.Context, db *sql.DB, projectID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, projectID); err != nil {
		return fmt.Errorf("delete registration: %w", err)
	}
	prefix := "project." + projectID + "."
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM settings WHERE key LIKE ? ESCAPE '\'`, escapeLike(prefix)+"%"); err != nil {
		return fmt.Errorf("delete project settings: %w", err)
	}
	return tx.Commit()
}

// prunePapers implements the shared reference counting and copy deletion. It
// scans every registered project before deleting anything, so an unreadable or
// corrupt project retains all candidates.
func (w *Workspace) prunePapers(ctx context.Context, candidates []string) (PruneResult, error) {
	var res PruneResult

	w.mu.Lock()
	root := w.root
	p := w.p
	global := w.global
	opts := w.opts
	w.mu.Unlock()
	if global == nil {
		return res, errors.New("workspace: prune papers: workspace is closed")
	}

	idsOut, err := registeredProjectIDs(ctx, global.DB())
	if err != nil {
		return res, err
	}
	cands := dedupeSorted(candidates)
	for _, id := range cands {
		if !ids.Valid(id) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("invalid paper id %q skipped", id))
		}
	}

	existing := make([]string, 0, len(cands))
	for _, id := range cands {
		if !ids.Valid(id) {
			continue
		}
		ok, err := paperExists(ctx, global.DB(), id)
		if err != nil {
			return res, err
		}
		if ok {
			existing = append(existing, id)
		}
	}
	if len(existing) == 0 {
		return res, nil
	}

	scanner := &refScanner{
		ctx:        ctx,
		p:          p,
		root:       root,
		opt:        opts.storageOptions(),
		projectIDs: idsOut,
		dbs:        make(map[string]*sql.DB),
	}
	defer scanner.close()

	referenced := make(map[string]bool, len(existing))
	for _, id := range existing {
		referenced[id] = scanner.references(id)
		if scanner.unhealthy {
			break
		}
	}
	if scanner.unhealthy {
		// Abort the whole GC: retain every candidate.
		res.Retained = append(res.Retained, existing...)
		res.Warnings = append(res.Warnings, scanner.warnings...)
		return res, nil
	}

	for _, id := range existing {
		if referenced[id] {
			res.Retained = append(res.Retained, id)
			continue
		}
		if err := w.deletePaperCopy(ctx, global, id); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("paper %s: %v", id, err))
			res.Retained = append(res.Retained, id)
			continue
		}
		res.DeletedPapers = append(res.DeletedPapers, id)
	}
	res.Warnings = append(res.Warnings, scanner.warnings...)
	return res, nil
}

// deletePaperCopy removes library/papers/<id>/ and then the papers row. When the
// directory cannot be removed the row is kept so a later prune can retry.
func (w *Workspace) deletePaperCopy(ctx context.Context, global *GlobalHandle, paperID string) error {
	w.mu.Lock()
	p := w.p
	c := w.opts.Clock
	w.mu.Unlock()
	dir, err := p.Abs(p.PaperDir(paperID))
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove copy: %w", err)
	}
	tx, err := global.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := papers.New(global.DB(), c).Delete(ctx, tx, paperID); err != nil {
		return fmt.Errorf("delete papers row: %w", err)
	}
	return tx.Commit()
}

type refScanner struct {
	ctx        context.Context
	p          paths.Paths
	root       string
	opt        storage.Options
	projectIDs []string
	dbs        map[string]*sql.DB
	warnings   []string
	unhealthy  bool
}

var refQueries = []string{
	`SELECT count(*) FROM project_papers WHERE paper_id = ?`,
	`SELECT count(*) FROM entity_relations WHERE dst_type = 'paper' AND dst_id = ?`,
	`SELECT count(*) FROM evidence WHERE source_type = 'paper' AND source_id = ?`,
}

// references reports whether any registered project references paperID. On any
// unreadable database or broken structure it marks the scanner unhealthy and
// returns true so the caller retains everything.
func (s *refScanner) references(paperID string) bool {
	for _, projectID := range s.projectIDs {
		db, ok := s.dbFor(projectID)
		if !ok {
			return true
		}
		for _, query := range refQueries {
			var n int
			if err := db.QueryRowContext(s.ctx, query, paperID).Scan(&n); err != nil {
				s.warnings = append(s.warnings, fmt.Sprintf("%s: %v", projectID, err))
				s.unhealthy = true
				return true
			}
			if n > 0 {
				return true
			}
		}
	}
	return false
}

func (s *refScanner) dbFor(projectID string) (*sql.DB, bool) {
	if db, ok := s.dbs[projectID]; ok {
		return db, db != nil
	}
	rel := s.p.ProjectDB(projectID)
	abs, err := s.p.Abs(rel)
	if err != nil {
		s.warnings = append(s.warnings, fmt.Sprintf("%s: %v", projectID, err))
		s.unhealthy = true
		s.dbs[projectID] = nil
		return nil, false
	}
	db, err := storage.OpenReadOnly(s.ctx, abs)
	if err != nil {
		s.warnings = append(s.warnings, fmt.Sprintf("%s: %v", projectID, err))
		s.unhealthy = true
		s.dbs[projectID] = nil
		return nil, false
	}
	s.dbs[projectID] = db
	return db, true
}

func (s *refScanner) close() {
	for _, db := range s.dbs {
		if db != nil {
			_ = db.Close()
		}
	}
}

func registeredProjectIDs(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT id FROM projects ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("workspace: list projects: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("workspace: list projects: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workspace: list projects: %w", err)
	}
	return out, nil
}

func paperExists(ctx context.Context, db *sql.DB, paperID string) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM papers WHERE id = ?`, paperID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("workspace: read paper %s: %w", paperID, err)
	}
	return true, nil
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func dedupeSorted(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func escapeLike(prefix string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(prefix)
}
