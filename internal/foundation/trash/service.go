// Package trash implements soft deletion, restore, the trash listing and the
// irreversible purge (SPEC-01 §5.8). It is bound to one project database, its
// path resolver and its event writer. It never touches global papers.
package trash

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/paths"
	"researchos/internal/foundation/schema"
)

// Ref identifies one entity to delete, restore or purge.
type Ref struct {
	Type schema.EntityType
	ID   string
}

// Item is one v_trash row.
type Item struct {
	EntityType schema.EntityType
	ID         string
	Title      string
	DeletedAt  time.Time
}

// ListQuery filters the trash list.
type ListQuery struct {
	EntityTypes []schema.EntityType // empty = all
	Limit       int                 // default 100, capped at 500
	Offset      int
}

// PurgeRequest permanently deletes the given entities. It requires a
// KindPurgeEntity token scoped to exactly these refs.
type PurgeRequest struct {
	Refs    []Ref
	Confirm confirm.Token
	Actor   events.Actor
}

// PurgeResult reports what Purge/Empty did.
//
// Warnings is an implementation addition to the SPEC struct: file deletion
// happens after the rows are committed, so failures are reported instead of
// being silently swallowed (§5.8 behavior rule 1).
type PurgeResult struct {
	Purged       int
	RemovedFiles int
	RemovedDirs  int
	ClearedLinks int
	PurgedEvents int
	Warnings     []string
}

// EmptyRequest empties the whole project trash. It requires a KindEmptyTrash
// token scoped to every current v_trash item.
type EmptyRequest struct {
	Confirm confirm.Token
	Actor   events.Actor
}

// softDeletable maps the ten soft-deletable entity types to their table.
// db_fields is soft-deletable but intentionally absent from v_trash.
var softDeletable = map[schema.EntityType]string{
	schema.EntityNote:             "notes",
	schema.EntityResearchDatabase: "research_databases",
	schema.EntityDBField:          "db_fields",
	schema.EntityDBRecord:         "db_records",
	schema.EntityIdea:             "ideas",
	schema.EntityEvidence:         "evidence",
	schema.EntityExperiment:       "experiments",
	schema.EntityRun:              "runs",
	schema.EntityTask:             "tasks",
	schema.EntityWeeklyReport:     "weekly_reports",
}

// Service is the trash service of one project.
type Service struct {
	projectID string
	db        *sql.DB
	paths     paths.Paths
	events    events.Writer
	confirm   confirm.Service
	clock     clock.Clock
}

// New binds the trash service to a project database and its path/event/confirm
// collaborators.
func New(projectID string, db *sql.DB, p paths.Paths, writer events.Writer, conf confirm.Service, c clock.Clock) *Service {
	if c == nil {
		c = clock.System{}
	}
	return &Service{projectID: projectID, db: db, paths: p, events: writer, confirm: conf, clock: c}
}

// SoftDelete moves an entity into the trash. Deleting an already-deleted entity
// is a no-op that emits nothing. Note files and attachment directories stay.
func (s *Service) SoftDelete(ctx context.Context, tx *sql.Tx, r Ref, actor events.Actor) error {
	if tx == nil {
		return errors.New("trash: SoftDelete requires a transaction")
	}
	table, err := s.tableFor(r)
	if err != nil {
		return err
	}
	now := clock.Format(s.clock.Now())
	res, err := tx.ExecContext(ctx,
		`UPDATE `+table+` SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`, now, r.ID)
	if err != nil {
		return fmt.Errorf("trash: soft delete %s: %w", r.Type, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("trash: soft delete %s: %w", r.Type, err)
	}
	if rows == 0 {
		var deleted sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT deleted_at FROM `+table+` WHERE id = ?`, r.ID).Scan(&deleted)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("trash: soft delete %s %s: %w", r.Type, r.ID, sql.ErrNoRows)
		}
		if err != nil {
			return fmt.Errorf("trash: soft delete %s: %w", r.Type, err)
		}
		return nil // already in the trash: idempotent, no event
	}
	return s.events.Emit(ctx, tx, events.Event{
		Type:       schema.EventEntityDeleted,
		EntityType: r.Type,
		EntityID:   r.ID,
		Summary:    "移入回收站",
		Actor:      actor,
	})
}

// Restore clears deleted_at. Restoring a live entity is a no-op that emits
// nothing.
func (s *Service) Restore(ctx context.Context, tx *sql.Tx, r Ref, actor events.Actor) error {
	if tx == nil {
		return errors.New("trash: Restore requires a transaction")
	}
	table, err := s.tableFor(r)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE `+table+` SET deleted_at = NULL WHERE id = ? AND deleted_at IS NOT NULL`, r.ID)
	if err != nil {
		return fmt.Errorf("trash: restore %s: %w", r.Type, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("trash: restore %s: %w", r.Type, err)
	}
	if rows == 0 {
		var deleted sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT deleted_at FROM `+table+` WHERE id = ?`, r.ID).Scan(&deleted)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("trash: restore %s %s: %w", r.Type, r.ID, sql.ErrNoRows)
		}
		if err != nil {
			return fmt.Errorf("trash: restore %s: %w", r.Type, err)
		}
		return nil // already alive: idempotent, no event
	}
	return s.events.Emit(ctx, tx, events.Event{
		Type:       schema.EventEntityRestored,
		EntityType: r.Type,
		EntityID:   r.ID,
		Summary:    "从回收站恢复",
		Actor:      actor,
	})
}

// List reads v_trash, sorted by deleted_at DESC (ties broken by type and id)
// and paged in Go per §4.3.3.
func (s *Service) List(ctx context.Context, q ListQuery) ([]Item, error) {
	query := `SELECT entity_type, id, title, deleted_at FROM v_trash`
	var args []any
	if len(q.EntityTypes) > 0 {
		query += ` WHERE entity_type IN (` + placeholders(len(q.EntityTypes)) + `)`
		for _, t := range q.EntityTypes {
			args = append(args, string(t))
		}
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("trash: list: %w", err)
	}
	defer rows.Close()
	var items []Item
	for rows.Next() {
		var (
			item       Item
			entityType string
			deletedAt  string
		)
		if err := rows.Scan(&entityType, &item.ID, &item.Title, &deletedAt); err != nil {
			return nil, fmt.Errorf("trash: list: %w", err)
		}
		item.EntityType = schema.EntityType(entityType)
		at, err := clock.Parse(deletedAt)
		if err != nil {
			return nil, fmt.Errorf("trash: list deleted_at: %w", err)
		}
		item.DeletedAt = at
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("trash: list: %w", err)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].DeletedAt.Equal(items[j].DeletedAt) {
			return items[i].DeletedAt.After(items[j].DeletedAt)
		}
		if items[i].EntityType != items[j].EntityType {
			return items[i].EntityType < items[j].EntityType
		}
		return items[i].ID < items[j].ID
	})
	limit := normalizedLimit(q.Limit)
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return nil, nil
	}
	items = items[offset:]
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// Count returns the number of trash items.
func (s *Service) Count(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM v_trash`).Scan(&n); err != nil {
		return 0, fmt.Errorf("trash: count: %w", err)
	}
	return n, nil
}

// Purge permanently deletes the requested entities after consuming a matching
// confirmation token: relation rows first (with relation.removed events), then
// the entity rows (with entity.purged events), then their files.
func (s *Service) Purge(ctx context.Context, req PurgeRequest) (PurgeResult, error) {
	refs := dedupe(req.Refs)
	if err := s.validateRefs(refs); err != nil {
		return PurgeResult{}, err
	}
	if err := s.consume(ctx, confirm.KindPurgeEntity, req.Confirm, refs); err != nil {
		return PurgeResult{}, err
	}
	return s.purge(ctx, refs, req.Actor)
}

// Empty purges every current v_trash item with one KindEmptyTrash token.
func (s *Service) Empty(ctx context.Context, req EmptyRequest) (PurgeResult, error) {
	refs, err := s.trashRefs(ctx)
	if err != nil {
		return PurgeResult{}, err
	}
	if err := s.consume(ctx, confirm.KindEmptyTrash, req.Confirm, refs); err != nil {
		return PurgeResult{}, err
	}
	return s.purge(ctx, refs, req.Actor)
}

func (s *Service) validateRefs(refs []Ref) error {
	for _, ref := range refs {
		if _, ok := softDeletable[ref.Type]; !ok {
			return fmt.Errorf("%w: %s cannot be purged", errs.ErrBadEntityType, ref.Type)
		}
		if !ids.Valid(ref.ID) {
			return fmt.Errorf("%w: %q", errs.ErrBadEntityID, ref.ID)
		}
	}
	return nil
}

func (s *Service) purge(ctx context.Context, refs []Ref, actor events.Actor) (PurgeResult, error) {
	var res PurgeResult
	if len(refs) == 0 {
		return res, nil
	}
	if err := s.validateRefs(refs); err != nil {
		return res, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("trash: purge: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var noteIDs []string
	for _, ref := range refs {
		links, err := s.linksTouching(ctx, tx, ref)
		if err != nil {
			return res, err
		}
		for _, l := range links {
			if _, err := tx.ExecContext(ctx, `DELETE FROM entity_relations WHERE id = ?`, l.id); err != nil {
				return res, fmt.Errorf("trash: purge: clear relation: %w", err)
			}
			obj := l.object()
			if err := s.events.Emit(ctx, tx, events.Event{
				Type:       schema.EventRelationRemoved,
				EntityType: l.srcType,
				EntityID:   l.srcID,
				Old:        &obj,
				Summary:    "解除关系",
				Actor:      actor,
			}); err != nil {
				return res, err
			}
			res.ClearedLinks++
		}

		table := softDeletable[ref.Type]
		result, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE id = ?`, ref.ID)
		if err != nil {
			return res, fmt.Errorf("trash: purge %s: %w", ref.Type, err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return res, fmt.Errorf("trash: purge %s: %w", ref.Type, err)
		}
		if rows > 0 {
			if err := s.events.Emit(ctx, tx, events.Event{
				Type:       schema.EventEntityPurged,
				EntityType: ref.Type,
				EntityID:   ref.ID,
				Summary:    "永久删除",
				Actor:      actor,
			}); err != nil {
				return res, err
			}
			res.Purged++
			res.PurgedEvents++
		}
		if ref.Type == schema.EntityNote {
			noteIDs = append(noteIDs, ref.ID)
		}
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("trash: purge commit: %w", err)
	}

	// Files go after the commit: a file failure never rolls back rows.
	for _, noteID := range noteIDs {
		files, dirs, warnings := s.removeNoteFiles(noteID)
		res.RemovedFiles += files
		res.RemovedDirs += dirs
		res.Warnings = append(res.Warnings, warnings...)
	}
	return res, nil
}

type relationRow struct {
	id       string
	srcType  schema.EntityType
	srcID    string
	relation schema.Relation
	dstType  schema.EntityType
	dstID    string
}

func (l relationRow) object() events.Object {
	return events.Object{
		"src_type": string(l.srcType),
		"src_id":   l.srcID,
		"relation": string(l.relation),
		"dst_type": string(l.dstType),
		"dst_id":   l.dstID,
	}
}

// linksTouching returns every relation row that names ref on either side.
func (s *Service) linksTouching(ctx context.Context, tx *sql.Tx, ref Ref) ([]relationRow, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, src_type, src_id, relation, dst_type, dst_id FROM entity_relations
		 WHERE (src_type = ? AND src_id = ?) OR (dst_type = ? AND dst_id = ?)`,
		string(ref.Type), ref.ID, string(ref.Type), ref.ID)
	if err != nil {
		return nil, fmt.Errorf("trash: purge: read relations: %w", err)
	}
	var out []relationRow
	for rows.Next() {
		var (
			l        relationRow
			srcType  string
			relation string
			dstType  string
		)
		if err := rows.Scan(&l.id, &srcType, &l.srcID, &relation, &dstType, &l.dstID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("trash: purge: read relations: %w", err)
		}
		l.srcType = schema.EntityType(srcType)
		l.relation = schema.Relation(relation)
		l.dstType = schema.EntityType(dstType)
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("trash: purge: read relations: %w", err)
	}
	rows.Close()
	return out, nil
}

// removeNoteFiles deletes notes/<id>.md and attachments/<id>/ for a purged note.
// Paths are resolved and validated through paths.Abs; failures are warnings.
func (s *Service) removeNoteFiles(noteID string) (files, dirs int, warnings []string) {
	if abs, err := s.paths.Abs(s.paths.NoteFile(s.projectID, noteID)); err != nil {
		return 0, 0, []string{fmt.Sprintf("note %s: note file path rejected: %v", noteID, err)}
	} else if info, statErr := os.Stat(abs); statErr == nil {
		if !info.IsDir() {
			if rmErr := os.Remove(abs); rmErr != nil {
				warnings = append(warnings, fmt.Sprintf("note %s: remove file: %v", noteID, rmErr))
			} else {
				files++
			}
		}
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		warnings = append(warnings, fmt.Sprintf("note %s: stat file: %v", noteID, statErr))
	}

	abs, err := s.paths.Abs(s.paths.AttachmentsDir(s.projectID, noteID))
	if err != nil {
		return files, dirs, append(warnings, fmt.Sprintf("note %s: attachments path rejected: %v", noteID, err))
	}
	info, statErr := os.Stat(abs)
	if errors.Is(statErr, fs.ErrNotExist) {
		return files, dirs, warnings
	}
	if statErr != nil {
		return files, dirs, append(warnings, fmt.Sprintf("note %s: stat attachments: %v", noteID, statErr))
	}
	if !info.IsDir() {
		return files, dirs, append(warnings, fmt.Sprintf("note %s: attachments path is not a directory", noteID))
	}
	f, d := countTree(abs)
	if rmErr := os.RemoveAll(abs); rmErr != nil {
		return files, dirs, append(warnings, fmt.Sprintf("note %s: remove attachments: %v", noteID, rmErr))
	}
	return files + f, dirs + d + 1, warnings
}

func countTree(root string) (files, dirs int) {
	_ = fs.WalkDir(os.DirFS(root), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == "." {
			return nil
		}
		if d.IsDir() {
			dirs++
		} else {
			files++
		}
		return nil
	})
	return files, dirs
}

func (s *Service) consume(ctx context.Context, kind confirm.Kind, token confirm.Token, refs []Ref) error {
	if s.confirm == nil {
		return fmt.Errorf("%w: no confirmation service configured", errs.ErrUserConfirmationRequired)
	}
	types := make([]string, 0, len(refs))
	idsOut := make([]string, 0, len(refs))
	for _, ref := range refs {
		types = append(types, string(ref.Type))
		idsOut = append(idsOut, ref.ID)
	}
	return s.confirm.Consume(ctx, token, confirm.Scope{
		Kind:        kind,
		ProjectID:   s.projectID,
		EntityTypes: types,
		EntityIDs:   idsOut,
	})
}

func (s *Service) trashRefs(ctx context.Context) ([]Ref, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT entity_type, id FROM v_trash`)
	if err != nil {
		return nil, fmt.Errorf("trash: read v_trash: %w", err)
	}
	defer rows.Close()
	var refs []Ref
	for rows.Next() {
		var (
			ref        Ref
			entityType string
		)
		if err := rows.Scan(&entityType, &ref.ID); err != nil {
			return nil, fmt.Errorf("trash: read v_trash: %w", err)
		}
		ref.Type = schema.EntityType(entityType)
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("trash: read v_trash: %w", err)
	}
	return dedupe(refs), nil
}

func (s *Service) tableFor(r Ref) (string, error) {
	if !r.Type.Valid() {
		return "", fmt.Errorf("%w: %q", errs.ErrBadEntityType, r.Type)
	}
	table, ok := softDeletable[r.Type]
	if !ok {
		return "", fmt.Errorf("%w: %s is not soft-deletable", errs.ErrBadEntityType, r.Type)
	}
	if !ids.Valid(r.ID) {
		return "", fmt.Errorf("%w: %q", errs.ErrBadEntityID, r.ID)
	}
	return table, nil
}

func dedupe(refs []Ref) []Ref {
	seen := make(map[Ref]struct{}, len(refs))
	out := make([]Ref, 0, len(refs))
	for _, ref := range refs {
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func normalizedLimit(limit int) int {
	switch {
	case limit <= 0:
		return 100
	case limit > 500:
		return 500
	default:
		return limit
	}
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
