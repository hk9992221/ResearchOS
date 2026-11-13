package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/workspace"
	"researchos/internal/ports"
)

// IssueKind enumerates the startup reconcile findings (§5.4 table). IssueSchema
// is the additive kind used to surface Workspace.Scan's per-database schema
// problems, which have no dedicated kind in §5.3.
type IssueKind string

const (
	IssueRegisteredMissingDir IssueKind = "registered_missing_dir"
	IssueOrphanProjectDir     IssueKind = "orphan_project_dir"
	IssueOrphanPaperDir       IssueKind = "orphan_paper_dir"
	IssueRegisteredWithoutDB  IssueKind = "registered_without_db"
	IssuePaperFileMissing     IssueKind = "paper_row_missing_file"
	IssueUnknownStatus        IssueKind = "unknown_status"
	IssueSchema               IssueKind = "schema_broken"
)

// Issue is one reconcile finding. ProjectID is the project id for project
// issues and the paper id for paper issues; it may be empty for global schema
// problems.
type Issue struct {
	Kind      IssueKind
	ProjectID string
	DirPath   string
	Detail    string
}

// IssueRef identifies one finding without carrying its detail text.
type IssueRef struct {
	Kind      IssueKind
	ProjectID string
	DirPath   string
}

// ReconcileAction is a user-chosen repair.
type ReconcileAction string

const (
	ActionCompleteDelete  ReconcileAction = "complete_delete"
	ActionRegisterProject ReconcileAction = "register_project"
	ActionDeleteOrphanDir ReconcileAction = "delete_orphan_dir"
	ActionMarkBroken      ReconcileAction = "mark_broken"
	ActionIgnore          ReconcileAction = "ignore"
)

// Reconcile compares the registration, the project directories, the global
// paper library and the project statuses. It never repairs anything.
func (s *Service) Reconcile(ctx context.Context) ([]Issue, error) {
	if s.ws == nil || s.store == nil {
		return nil, errNoWorkspace
	}
	scan, err := s.ws.Scan(ctx)
	if err != nil {
		return nil, err
	}
	resolver := s.ws.Paths()
	var issues []Issue

	for _, id := range scan.RegisteredWithoutDir {
		issues = append(issues, Issue{
			Kind:      IssueRegisteredMissingDir,
			ProjectID: id,
			DirPath:   resolver.ProjectDir(id),
			Detail:    "registered project directory is missing",
		})
	}
	for _, id := range scan.RegisteredWithoutDB {
		issues = append(issues, Issue{
			Kind:      IssueRegisteredWithoutDB,
			ProjectID: id,
			DirPath:   resolver.ProjectDir(id),
			Detail:    "project.db is missing; the database is not recreated automatically",
		})
	}
	for _, name := range scan.UnregisteredDirs {
		issues = append(issues, Issue{
			Kind:      IssueOrphanProjectDir,
			ProjectID: name,
			DirPath:   resolver.ProjectDir(name),
			Detail:    "directory has no registration row",
		})
	}
	for _, raw := range scan.SchemaIssues {
		id, detail := splitSchemaIssue(raw)
		iss := Issue{Kind: IssueSchema, ProjectID: id, Detail: detail}
		if id != "" {
			iss.DirPath = resolver.ProjectDir(id)
		}
		issues = append(issues, iss)
	}

	rows, err := s.store.List(ctx, nil)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if schema.ProjectStatus(row.Status).Valid() {
			continue
		}
		issues = append(issues, Issue{
			Kind:      IssueUnknownStatus,
			ProjectID: row.ID,
			DirPath:   resolver.ProjectDir(row.ID),
			Detail:    fmt.Sprintf("unknown project status %q", string(row.Status)),
		})
	}

	global, err := s.globalHandle()
	if err != nil {
		return nil, err
	}
	globalDB := global.DB()

	papersDir := filepath.Join(s.ws.Root(), filepath.FromSlash("library/papers"))
	entries, rerr := os.ReadDir(papersDir)
	switch {
	case errors.Is(rerr, fs.ErrNotExist):
	case rerr != nil:
		return nil, fmt.Errorf("project: reconcile: read paper library: %w", rerr)
	default:
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			exists, err := paperExists(ctx, globalDB, e.Name())
			if err != nil {
				return nil, err
			}
			if exists {
				continue
			}
			issues = append(issues, Issue{
				Kind:      IssueOrphanPaperDir,
				ProjectID: e.Name(),
				DirPath:   resolver.PaperDir(e.Name()),
				Detail:    "paper directory has no papers row",
			})
		}
	}

	paperRows, err := globalDB.QueryContext(ctx,
		`SELECT id FROM papers WHERE file_state = ?`, string(schema.PaperFileOK))
	if err != nil {
		return nil, fmt.Errorf("project: reconcile: read papers: %w", err)
	}
	for paperRows.Next() {
		var id string
		if err := paperRows.Scan(&id); err != nil {
			paperRows.Close()
			return nil, err
		}
		abs, err := resolver.Abs(resolver.PaperPDF(id))
		if err != nil {
			paperRows.Close()
			return nil, err
		}
		_, statErr := os.Stat(abs)
		if statErr == nil {
			continue
		}
		if !errors.Is(statErr, fs.ErrNotExist) {
			paperRows.Close()
			return nil, fmt.Errorf("project: reconcile: stat paper %s: %w", id, statErr)
		}
		issues = append(issues, Issue{
			Kind:      IssuePaperFileMissing,
			ProjectID: id,
			DirPath:   resolver.PaperDir(id),
			Detail:    "papers.file_state is ok but source.pdf is missing",
		})
	}
	if err := paperRows.Err(); err != nil {
		paperRows.Close()
		return nil, err
	}
	paperRows.Close()

	sort.SliceStable(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.ProjectID != b.ProjectID {
			return a.ProjectID < b.ProjectID
		}
		if a.DirPath != b.DirPath {
			return a.DirPath < b.DirPath
		}
		return a.Detail < b.Detail
	})
	return issues, nil
}

// splitSchemaIssue turns "<id>: <reason>" or "global: <reason>" from
// Workspace.Scan into its project scope and detail.
func splitSchemaIssue(raw string) (id, detail string) {
	if i := strings.Index(raw, ": "); i > 0 {
		id = raw[:i]
		detail = raw[i+2:]
	} else {
		detail = raw
	}
	if id == "global" {
		id = ""
	}
	return id, detail
}

// Resolve applies one reconcile repair. All repairs are idempotent: repeating
// the same call succeeds without duplicating rows or emitting extra events.
func (s *Service) Resolve(ctx context.Context, ref IssueRef, action ReconcileAction) error {
	if s.ws == nil || s.store == nil {
		return errNoWorkspace
	}
	if !allowedAction(ref.Kind, action) {
		return fmt.Errorf("project: reconcile: action %q is not supported for issue %q", action, ref.Kind)
	}
	switch action {
	case ActionIgnore:
		return nil
	case ActionCompleteDelete:
		return s.resolveCompleteDelete(ctx, ref)
	case ActionRegisterProject:
		return s.resolveRegisterProject(ctx, ref)
	case ActionDeleteOrphanDir:
		return s.resolveDeleteOrphanDir(ctx, ref)
	case ActionMarkBroken:
		return s.resolveMarkBroken(ctx, ref)
	default:
		return fmt.Errorf("project: reconcile: unknown action %q", action)
	}
}

func allowedAction(kind IssueKind, action ReconcileAction) bool {
	var actions []ReconcileAction
	switch kind {
	case IssueRegisteredMissingDir:
		actions = []ReconcileAction{ActionCompleteDelete, ActionIgnore}
	case IssueRegisteredWithoutDB:
		actions = []ReconcileAction{ActionCompleteDelete, ActionDeleteOrphanDir, ActionIgnore}
	case IssueOrphanProjectDir:
		actions = []ReconcileAction{ActionRegisterProject, ActionDeleteOrphanDir, ActionIgnore}
	case IssueOrphanPaperDir:
		actions = []ReconcileAction{ActionDeleteOrphanDir, ActionIgnore}
	case IssuePaperFileMissing:
		actions = []ReconcileAction{ActionMarkBroken, ActionIgnore}
	case IssueUnknownStatus, IssueSchema:
		actions = []ReconcileAction{ActionIgnore}
	default:
		return false
	}
	for _, a := range actions {
		if a == action {
			return true
		}
	}
	return false
}

// resolveCompleteDelete removes only the global registration row. The deletion
// was already confirmed by the user when the crash left the row behind, so no
// token is requested here (§5.4).
func (s *Service) resolveCompleteDelete(ctx context.Context, ref IssueRef) error {
	id := strings.TrimSpace(ref.ProjectID)
	if id == "" && ref.DirPath != "" {
		id = path.Base(canonicalRel(ref.DirPath))
	}
	if !ids.Valid(id) {
		return fmt.Errorf("project: reconcile: invalid project id %q", id)
	}
	if ref.DirPath != "" && canonicalRel(ref.DirPath) != s.ws.Paths().ProjectDir(id) {
		return fmt.Errorf("project: reconcile: path %q does not match the project layout", ref.DirPath)
	}
	global, err := s.globalHandle()
	if err != nil {
		return err
	}
	err = s.locker.WithWorkspaceLock(ctx, func() error {
		if err := s.ws.CloseProject(id); err != nil {
			return fmt.Errorf("project: reconcile: close %s: %w", id, err)
		}
		return global.InTx(ctx, func(tx *sql.Tx) error {
			if err := s.store.Delete(ctx, tx, id); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil
				}
				return err
			}
			return nil
		})
	})
	if err != nil {
		return fmt.Errorf("project: reconcile: complete delete %s: %w", id, err)
	}
	return nil
}

// resolveRegisterProject inserts a registration row for a recovered directory.
// The name is the directory id (§5.4); the entity.created event is written in
// the recovered project database with actor=user.
func (s *Service) resolveRegisterProject(ctx context.Context, ref IssueRef) error {
	id, err := s.projectIDFor(ref)
	if err != nil {
		return err
	}
	if !ids.Valid(id) {
		return fmt.Errorf("project: reconcile: %q is not a valid project id", id)
	}
	dir, err := s.ws.Paths().Abs(s.ws.Paths().ProjectDir(id))
	if err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("project: reconcile: directory %s does not exist", s.ws.Paths().ProjectDir(id))
	}
	if err != nil {
		return fmt.Errorf("project: reconcile: stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("project: reconcile: %s is not a directory", dir)
	}

	global, err := s.globalHandle()
	if err != nil {
		return err
	}
	return s.locker.WithWorkspaceLock(ctx, func() error {
		_, gerr := s.store.Get(ctx, id)
		if gerr == nil {
			// Already registered: idempotent, no second event.
			return nil
		}
		if !errors.Is(gerr, sql.ErrNoRows) {
			return gerr
		}
		if err := global.InTx(ctx, func(tx *sql.Tx) error {
			return s.insertProjectRow(ctx, tx, id, id, "", "")
		}); err != nil {
			return fmt.Errorf("project: reconcile: register %s: %w", id, err)
		}
		ev := newRecoveredProjectEvent(id)
		if err := s.emitProjectEvent(ctx, id, ev); err != nil {
			return fmt.Errorf("%w: recovered project %s is registered but the activity event is missing: %w",
				ErrEventMissing, id, err)
		}
		return nil
	})
}

// resolveDeleteOrphanDir removes projects/<id> or library/papers/<id> after
// validating the exact layout. A registered project is never deleted, and a
// paper directory with a papers row is never deleted.
func (s *Service) resolveDeleteOrphanDir(ctx context.Context, ref IssueRef) error {
	var (
		rel       string
		projectID string
		paperID   string
	)
	switch ref.Kind {
	case IssueOrphanProjectDir:
		id, err := s.projectIDFor(ref)
		if err != nil {
			return err
		}
		projectID = id
		rel = s.ws.Paths().ProjectDir(id)
	case IssueRegisteredWithoutDB:
		id, err := s.projectIDFor(ref)
		if err != nil {
			return err
		}
		projectID = id
		rel = s.ws.Paths().ProjectDir(id)
	case IssueOrphanPaperDir:
		id, err := s.paperIDFor(ref)
		if err != nil {
			return err
		}
		paperID = id
		rel = s.ws.Paths().PaperDir(id)
	default:
		return fmt.Errorf("project: reconcile: delete_orphan_dir is not supported for issue %q", ref.Kind)
	}

	global, err := s.globalHandle()
	if err != nil {
		return err
	}
	return s.locker.WithWorkspaceLock(ctx, func() error {
		if projectID != "" {
			registered, err := projectRegistered(ctx, global.DB(), projectID)
			if err != nil {
				return err
			}
			if registered && ref.Kind == IssueOrphanProjectDir {
				return fmt.Errorf("project: reconcile: project %s is registered; refusing to delete its data", projectID)
			}
			if err := s.ws.CloseProject(projectID); err != nil {
				return fmt.Errorf("project: reconcile: close %s: %w", projectID, err)
			}
		}
		if paperID != "" {
			exists, err := paperExists(ctx, global.DB(), paperID)
			if err != nil {
				return err
			}
			if exists {
				return fmt.Errorf("project: reconcile: paper %s still has a row; refusing to delete its copy", paperID)
			}
		}
		if err := removeWorkspaceDir(s.ws, rel); err != nil {
			return err
		}
		if ref.Kind == IssueRegisteredWithoutDB {
			// The broken project is gone as a whole; leaving the registration
			// would only shift the problem to registered_missing_dir.
			if err := global.InTx(ctx, func(tx *sql.Tx) error {
				if err := s.store.Delete(ctx, tx, projectID); err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				return nil
			}); err != nil {
				return fmt.Errorf("project: reconcile: remove registration %s: %w", projectID, err)
			}
		}
		return nil
	})
}

// resolveMarkBroken flips a paper whose local copy disappeared to file_state
// 'missing' and keeps the record (§5.4).
func (s *Service) resolveMarkBroken(ctx context.Context, ref IssueRef) error {
	id, err := s.paperIDFor(ref)
	if err != nil {
		return err
	}
	global, err := s.globalHandle()
	if err != nil {
		return err
	}
	return global.InTx(ctx, func(tx *sql.Tx) error {
		var state string
		err := tx.QueryRowContext(ctx, `SELECT file_state FROM papers WHERE id = ?`, id).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("project: reconcile: paper %s not found", id)
		}
		if err != nil {
			return fmt.Errorf("project: reconcile: read paper %s: %w", id, err)
		}
		if state == string(schema.PaperFileMissing) {
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE papers SET file_state = ?, updated_at = ? WHERE id = ?`,
			string(schema.PaperFileMissing), clock.Format(s.clock.Now()), id); err != nil {
			return fmt.Errorf("project: reconcile: mark paper %s broken: %w", id, err)
		}
		return nil
	})
}

// projectIDFor resolves and validates the project id against the expected
// projects/<id> layout.
func (s *Service) projectIDFor(ref IssueRef) (string, error) {
	id := strings.TrimSpace(ref.ProjectID)
	if id == "" && ref.DirPath != "" {
		id = path.Base(canonicalRel(ref.DirPath))
	}
	if id == "" {
		return "", errors.New("project: reconcile: missing project id")
	}
	if ref.DirPath != "" {
		if got, want := canonicalRel(ref.DirPath), s.ws.Paths().ProjectDir(id); got != want {
			return "", fmt.Errorf("project: reconcile: path %q does not match %q", ref.DirPath, want)
		}
	}
	return id, nil
}

// paperIDFor resolves and validates the paper id against library/papers/<id>.
func (s *Service) paperIDFor(ref IssueRef) (string, error) {
	id := strings.TrimSpace(ref.ProjectID)
	if id == "" && ref.DirPath != "" {
		id = path.Base(canonicalRel(ref.DirPath))
	}
	if id == "" {
		return "", errors.New("project: reconcile: missing paper id")
	}
	if !ids.Valid(id) {
		return "", fmt.Errorf("project: reconcile: %q is not a valid paper id", id)
	}
	if ref.DirPath != "" {
		if got, want := canonicalRel(ref.DirPath), s.ws.Paths().PaperDir(id); got != want {
			return "", fmt.Errorf("project: reconcile: path %q does not match %q", ref.DirPath, want)
		}
	}
	return id, nil
}

// canonicalRel normalizes a user-supplied workspace-relative path for the
// strict layout comparison. Anything absolute or containing ".." cannot equal
// the expected path and is therefore rejected.
func canonicalRel(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	return path.Clean(p)
}

func removeWorkspaceDir(ws *workspace.Workspace, rel string) error {
	abs, err := ws.Paths().Abs(rel)
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("project: reconcile: stat %s: %w", rel, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("project: reconcile: %s is not a directory", rel)
	}
	if err := os.RemoveAll(abs); err != nil {
		return fmt.Errorf("project: reconcile: remove %s: %w", rel, err)
	}
	return nil
}

func projectRegistered(ctx context.Context, db *sql.DB, id string) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("project: reconcile: read registration %s: %w", id, err)
	}
	return true, nil
}

func paperExists(ctx context.Context, db *sql.DB, id string) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM papers WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("project: reconcile: read paper %s: %w", id, err)
	}
	return true, nil
}

func newRecoveredProjectEvent(id string) events.Event {
	return events.Event{
		Type:       schema.EventEntityCreated,
		EntityType: schema.EntityProject,
		EntityID:   id,
		Summary:    fmt.Sprintf("登记项目「%s」", id),
		Actor:      ports.ActorUser,
	}
}
