// tags.go implements the project-wide tag dictionary (SPEC-05 §5.4, NOTE-06).
// Tags live in project.db (tables tags / entity_tags, created by the SPEC-05
// migration) and are shared between notes and records. Tags are NOT in the
// SPEC-01 schema.EntityType registry, so the foundation trash service cannot
// manage them: soft delete and restore are implemented directly here and
// deliberately emit no events. entity_tags join rows survive tag soft
// deletion so a restore brings the assignments back (D-05-07).

package notes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
)

// ErrTagValidation reports invalid tag input: a bad name or color, an unknown
// tag, a deleted tag used for a write, a duplicate rename target, or an
// assignment to a missing, deleted or unsupported entity.
var ErrTagValidation = errors.New("invalid tag input")

// maxTagNameRunes bounds tags.name after trimming (SPEC-05 §5.4 parity with
// field names: at most 64 runes).
const maxTagNameRunes = 64

// tagService is the TagService implementation bound to one project.db.
type tagService struct {
	db  *sql.DB
	clk clock.Clock
}

// NewTagService returns the tag service of one project database. A nil clock
// uses clock.System.
func NewTagService(db *sql.DB, clk clock.Clock) TagService {
	if clk == nil {
		clk = clock.System{}
	}
	return &tagService{db: db, clk: clk}
}

// ListTags returns the dictionary ordered case-insensitively by name, ties on
// id. Deleted tags are included only when includeDeleted is true.
func (s *tagService) ListTags(ctx context.Context, includeDeleted bool) ([]*Tag, error) {
	q := `SELECT id, name, color, created_at, updated_at, deleted_at FROM tags`
	if !includeDeleted {
		q += ` WHERE deleted_at IS NULL`
	}
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("tags: list: %w", err)
	}
	defer rows.Close()
	var out []*Tag
	for rows.Next() {
		t, err := scanTag(rows)
		if err != nil {
			return nil, fmt.Errorf("tags: list: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tags: list: %w", err)
	}
	sortTagsByName(out)
	return out, nil
}

// CreateTag adds a tag. The name is trimmed and must be 1..64 runes; color is
// optional. Case-insensitively the name is unique among live tags: an
// existing live tag with the same folded name is returned unchanged and no
// duplicate row is created (SPEC-05 §8 item 9).
func (s *tagService) CreateTag(ctx context.Context, name, color string, _ events.Actor) (*Tag, error) {
	name, err := validateTagName(name)
	if err != nil {
		return nil, err
	}
	color, err = validateTagColor(color)
	if err != nil {
		return nil, err
	}
	live, err := s.liveTags(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range live {
		if strings.EqualFold(t.Name, name) {
			return t, nil // idempotent: same dictionary entry, no second row
		}
	}
	id, err := ids.New(s.clk)
	if err != nil {
		return nil, fmt.Errorf("tags: create: %w", err)
	}
	now := clock.Format(s.clk.Now())
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO tags(id, name, color, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		id, name, color, now, now); err != nil {
		return nil, fmt.Errorf("tags: create: %w", err)
	}
	return s.getTag(ctx, id)
}

// RenameTag changes the name and/or color of a live tag. An empty color keeps
// the current one. Taking another live tag's name (case-insensitive) is a
// validation error.
func (s *tagService) RenameTag(ctx context.Context, tagID, name, color string, _ events.Actor) (*Tag, error) {
	if !ids.Valid(tagID) {
		return nil, fmt.Errorf("%w: tag id %q is not a UUIDv7", errs.ErrBadEntityID, tagID)
	}
	t, err := s.getTag(ctx, tagID)
	if err != nil {
		return nil, err
	}
	if t.DeletedAt != nil {
		return nil, fmt.Errorf("%w: tag %s is deleted", ErrTagValidation, tagID)
	}
	name, err = validateTagName(name)
	if err != nil {
		return nil, err
	}
	if color == "" {
		color = t.Color
	} else {
		color, err = validateTagColor(color)
		if err != nil {
			return nil, err
		}
	}
	if !strings.EqualFold(color, t.Color) || !strings.EqualFold(name, t.Name) {
		live, err := s.liveTags(ctx)
		if err != nil {
			return nil, err
		}
		for _, other := range live {
			if other.ID != tagID && strings.EqualFold(other.Name, name) {
				return nil, fmt.Errorf("%w: tag name %q already exists", ErrTagValidation, name)
			}
		}
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tags SET name = ?, color = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		name, color, clock.Format(s.clk.Now()), tagID); err != nil {
		return nil, fmt.Errorf("tags: rename: %w", err)
	}
	return s.getTag(ctx, tagID)
}

// SoftDeleteTag sets deleted_at. Join rows in entity_tags are kept (D-05-07).
// Deleting an already deleted tag is an idempotent no-op; no event is emitted
// because tags are not a registered entity type.
func (s *tagService) SoftDeleteTag(ctx context.Context, tagID string, _ events.Actor) error {
	t, err := s.requireTag(ctx, tagID)
	if err != nil {
		return err
	}
	if t.DeletedAt != nil {
		return nil
	}
	now := clock.Format(s.clk.Now())
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tags SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		now, now, tagID); err != nil {
		return fmt.Errorf("tags: soft delete: %w", err)
	}
	return nil
}

// RestoreTag clears deleted_at. A live tag with the same folded name that
// appeared while the tag was deleted blocks the restore with a validation
// error. Restoring a live tag is an idempotent no-op. No event is emitted.
func (s *tagService) RestoreTag(ctx context.Context, tagID string, _ events.Actor) error {
	t, err := s.requireTag(ctx, tagID)
	if err != nil {
		return err
	}
	if t.DeletedAt == nil {
		return nil
	}
	live, err := s.liveTags(ctx)
	if err != nil {
		return err
	}
	for _, other := range live {
		if other.ID != tagID && strings.EqualFold(other.Name, t.Name) {
			return fmt.Errorf("%w: live tag %q already uses the name %q", ErrTagValidation, other.ID, t.Name)
		}
	}
	now := clock.Format(s.clk.Now())
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tags SET deleted_at = NULL, updated_at = ? WHERE id = ?`, now, tagID); err != nil {
		return fmt.Errorf("tags: restore: %w", err)
	}
	return nil
}

// Assign attaches a live tag to the given notes and records. It is idempotent
// (the entity_tags primary key absorbs repeats) and rejects unknown, deleted
// or unsupported (neither note nor db_record) refs with a validation error.
func (s *tagService) Assign(ctx context.Context, refs []EntityRef, tagID string, _ events.Actor) error {
	if _, err := s.requireLiveTag(ctx, tagID); err != nil {
		return err
	}
	for _, ref := range refs {
		if err := s.checkRef(ctx, ref); err != nil {
			return err
		}
	}
	if len(refs) == 0 {
		return nil
	}
	dbTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("tags: assign: begin: %w", err)
	}
	defer dbTx.Rollback() //nolint:errcheck // after Commit it is a no-op
	now := clock.Format(s.clk.Now())
	for _, ref := range refs {
		if _, err := dbTx.ExecContext(ctx,
			`INSERT OR IGNORE INTO entity_tags(tag_id, entity_type, entity_id, created_at)
			 VALUES (?, ?, ?, ?)`,
			tagID, string(ref.Type), ref.ID, now); err != nil {
			return fmt.Errorf("tags: assign: %w", err)
		}
	}
	if err := dbTx.Commit(); err != nil {
		return fmt.Errorf("tags: assign: commit: %w", err)
	}
	return nil
}

// Unassign removes exactly the given (tag, entity) join rows. Missing rows
// are not an error. Tag changes never write entity_relations and never emit
// activity events (SPEC-05 §5.4).
func (s *tagService) Unassign(ctx context.Context, refs []EntityRef, tagID string, _ events.Actor) error {
	if _, err := s.requireLiveTag(ctx, tagID); err != nil {
		return err
	}
	for _, ref := range refs {
		if err := tagRefShape(ref); err != nil {
			return err
		}
	}
	if len(refs) == 0 {
		return nil
	}
	dbTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("tags: unassign: begin: %w", err)
	}
	defer dbTx.Rollback() //nolint:errcheck // after Commit it is a no-op
	for _, ref := range refs {
		if _, err := dbTx.ExecContext(ctx,
			`DELETE FROM entity_tags WHERE tag_id = ? AND entity_type = ? AND entity_id = ?`,
			tagID, string(ref.Type), ref.ID); err != nil {
			return fmt.Errorf("tags: unassign: %w", err)
		}
	}
	if err := dbTx.Commit(); err != nil {
		return fmt.Errorf("tags: unassign: commit: %w", err)
	}
	return nil
}

// EntitiesByTag returns every entity carrying the tag, including entities
// that are now soft-deleted (the UI resolves deletion state). Unknown tags
// yield a validation error; deleted tags are still queryable.
func (s *tagService) EntitiesByTag(ctx context.Context, tagID string) ([]EntityRef, error) {
	if !ids.Valid(tagID) {
		return nil, fmt.Errorf("%w: tag id %q is not a UUIDv7", errs.ErrBadEntityID, tagID)
	}
	if _, err := s.getTag(ctx, tagID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT entity_type, entity_id FROM entity_tags WHERE tag_id = ?
		 ORDER BY entity_type, entity_id`, tagID)
	if err != nil {
		return nil, fmt.Errorf("tags: entities: %w", err)
	}
	defer rows.Close()
	var out []EntityRef
	for rows.Next() {
		var typ, id string
		if err := rows.Scan(&typ, &id); err != nil {
			return nil, fmt.Errorf("tags: entities: %w", err)
		}
		out = append(out, EntityRef{Type: schema.EntityType(typ), ID: id})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tags: entities: %w", err)
	}
	return out, nil
}

// getTag loads one tag row; a missing row is a validation error.
func (s *tagService) getTag(ctx context.Context, tagID string) (*Tag, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, color, created_at, updated_at, deleted_at FROM tags WHERE id = ?`, tagID)
	t, err := scanTag(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: no such tag %s", ErrTagValidation, tagID)
	}
	if err != nil {
		return nil, fmt.Errorf("tags: get: %w", err)
	}
	return t, nil
}

// requireTag loads a tag, validating the id shape first.
func (s *tagService) requireTag(ctx context.Context, tagID string) (*Tag, error) {
	if !ids.Valid(tagID) {
		return nil, fmt.Errorf("%w: tag id %q is not a UUIDv7", errs.ErrBadEntityID, tagID)
	}
	return s.getTag(ctx, tagID)
}

// requireLiveTag loads a tag and rejects deleted ones.
func (s *tagService) requireLiveTag(ctx context.Context, tagID string) (*Tag, error) {
	t, err := s.requireTag(ctx, tagID)
	if err != nil {
		return nil, err
	}
	if t.DeletedAt != nil {
		return nil, fmt.Errorf("%w: tag %s is deleted", ErrTagValidation, tagID)
	}
	return t, nil
}

// liveTags returns every non-deleted tag, used for case-insensitive
// uniqueness checks (the dictionary is small; no SQL fold index is assumed).
func (s *tagService) liveTags(ctx context.Context) ([]*Tag, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, color, created_at, updated_at, deleted_at FROM tags WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("tags: live set: %w", err)
	}
	defer rows.Close()
	var out []*Tag
	for rows.Next() {
		t, err := scanTag(rows)
		if err != nil {
			return nil, fmt.Errorf("tags: live set: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tags: live set: %w", err)
	}
	return out, nil
}

// tagRefShape validates the ref type and id without touching the database.
func tagRefShape(ref EntityRef) error {
	switch ref.Type {
	case schema.EntityNote, schema.EntityDBRecord:
	default:
		return fmt.Errorf("%w: tags attach to notes and records only, not %q",
			ErrTagValidation, ref.Type)
	}
	if !ids.Valid(ref.ID) {
		return fmt.Errorf("%w: entity id %q is not a UUIDv7", errs.ErrBadEntityID, ref.ID)
	}
	return nil
}

// checkRef validates shape plus the live existence of the target row.
func (s *tagService) checkRef(ctx context.Context, ref EntityRef) error {
	if err := tagRefShape(ref); err != nil {
		return err
	}
	table := "notes"
	if ref.Type == schema.EntityDBRecord {
		table = "db_records"
	}
	var deleted sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT deleted_at FROM `+table+` WHERE id = ?`, ref.ID).Scan(&deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: no such %s %s", ErrTagValidation, ref.Type, ref.ID)
	}
	if err != nil {
		return fmt.Errorf("tags: check %s: %w", ref.Type, err)
	}
	if deleted.Valid {
		return fmt.Errorf("%w: %s %s is deleted", errs.ErrEntityDeleted, ref.Type, ref.ID)
	}
	return nil
}

// scanTag reads one tags row. color may be NULL for rows created outside this
// service; it scans as an empty string.
func scanTag(row tagRowScanner) (*Tag, error) {
	var (
		t       Tag
		color   sql.NullString
		created string
		updated string
		deleted sql.NullString
	)
	if err := row.Scan(&t.ID, &t.Name, &color, &created, &updated, &deleted); err != nil {
		return nil, err
	}
	t.Color = color.String
	var err error
	if t.CreatedAt, err = clock.Parse(created); err != nil {
		return nil, fmt.Errorf("tags: created_at: %w", err)
	}
	if t.UpdatedAt, err = clock.Parse(updated); err != nil {
		return nil, fmt.Errorf("tags: updated_at: %w", err)
	}
	if deleted.Valid {
		ts, err := clock.Parse(deleted.String)
		if err != nil {
			return nil, fmt.Errorf("tags: deleted_at: %w", err)
		}
		t.DeletedAt = &ts
	}
	return &t, nil
}

// tagRowScanner is satisfied by *sql.Row and *sql.Rows.
type tagRowScanner interface {
	Scan(dest ...any) error
}

// sortTagsByName orders case-insensitively by name, then by id.
func sortTagsByName(tags []*Tag) {
	for i := 1; i < len(tags); i++ {
		for j := i; j > 0 && !tagLess(tags[j-1], tags[j]); j-- {
			tags[j-1], tags[j] = tags[j], tags[j-1]
		}
	}
}

func tagLess(a, b *Tag) bool {
	an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name)
	if an != bn {
		return an < bn
	}
	return a.ID < b.ID
}

// validateTagName trims and bounds the name, rejecting control characters.
func validateTagName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%w: tag name is empty", ErrTagValidation)
	}
	if utf8.RuneCountInString(name) > maxTagNameRunes {
		return "", fmt.Errorf("%w: tag name exceeds %d characters", ErrTagValidation, maxTagNameRunes)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: tag name contains control characters", ErrTagValidation)
		}
	}
	return name, nil
}

// validateTagColor accepts "" (no color), "#RGB", "#RRGGBB" or a plain
// preset name of ASCII letters (red, teal, ...). Anything else, including
// spaces and control characters, is rejected.
func validateTagColor(color string) (string, error) {
	if color == "" {
		return "", nil
	}
	for _, r := range color {
		if unicode.IsControl(r) || r == ' ' {
			return "", fmt.Errorf("%w: invalid color %q", ErrTagValidation, color)
		}
	}
	if strings.HasPrefix(color, "#") {
		hex := color[1:]
		if len(hex) != 3 && len(hex) != 6 {
			return "", fmt.Errorf("%w: color %q is not #RGB or #RRGGBB", ErrTagValidation, color)
		}
		for _, r := range hex {
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return "", fmt.Errorf("%w: color %q is not #RGB or #RRGGBB", ErrTagValidation, color)
			}
		}
		return color, nil
	}
	if utf8.RuneCountInString(color) > 32 {
		return "", fmt.Errorf("%w: color name %q too long", ErrTagValidation, color)
	}
	for _, r := range color {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return "", fmt.Errorf("%w: color %q is not a hex code or letter preset", ErrTagValidation, color)
		}
	}
	return color, nil
}
