package notes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/workspace"
)

// noteUUIDPattern matches the canonical lowercase UUIDv7 textual form.
const noteUUIDPattern = `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`

var (
	// noteRosLink matches the ros://<type>/<id> references written by the
	// editor for non-note entities (SPEC-05 §6.6).
	noteRosLink = regexp.MustCompile(`ros://([a-z_]+)/(` + noteUUIDPattern + `)`)
	// noteMarkdownLink matches [title](<note-id>.md) note references, with an
	// optional #anchor. Only the destination is captured.
	noteMarkdownLink = regexp.MustCompile(`\]\(\s*([^()\s]+?\.md)(?:#[^()\s]*)?\s*\)`)
)

// noteResolver resolves entity references against the project and global
// databases for the link renderer (SPEC-05 §5.1 ResolveTarget, §7.2).
type noteResolver struct {
	ws        *workspace.Workspace
	projectID string
}

// OutboundLinks parses ros:// and *.md references out of the note body and
// resolves each one. It writes nothing; a missing body yields no links.
func (s *noteService) OutboundLinks(ctx context.Context, noteID string) ([]ResolvedLink, error) {
	body, err := s.LoadBody(ctx, noteID)
	if err != nil {
		return nil, err
	}
	if body.State == BodyMissing {
		return nil, nil
	}
	handle, err := s.noteHandle(ctx)
	if err != nil {
		return nil, err
	}
	resolver := noteResolver{ws: s.ws, projectID: s.projectID}

	type hit struct {
		pos int
		raw string
		ref EntityRef
	}
	var hits []hit
	for _, m := range noteRosLink.FindAllStringSubmatchIndex(body.Markdown, -1) {
		raw := body.Markdown[m[0]:m[1]]
		typ := body.Markdown[m[2]:m[3]]
		id := body.Markdown[m[4]:m[5]]
		hits = append(hits, hit{pos: m[0], raw: raw, ref: EntityRef{Type: schema.EntityType(typ), ID: id}})
	}
	for _, m := range noteMarkdownLink.FindAllStringSubmatchIndex(body.Markdown, -1) {
		raw := body.Markdown[m[2]:m[3]]
		hits = append(hits, hit{pos: m[0], raw: raw, ref: noteRefFromMarkdown(raw)})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].pos < hits[j].pos })

	out := make([]ResolvedLink, 0, len(hits))
	for _, h := range hits {
		link := ResolvedLink{Raw: h.raw, Ref: h.ref}
		if noteRefValid(h.ref) {
			target, err := resolver.resolve(ctx, handle, h.ref)
			if err != nil {
				return nil, err
			}
			link.Title = target.Title
			link.Deleted = target.Deleted
			link.Missing = target.Missing
		} else {
			// A *.md link that is not <note-id>.md cannot be resolved; report
			// it as dangling instead of failing the whole read.
			link.Missing = true
		}
		out = append(out, link)
	}
	return out, nil
}

// ResolveTarget returns the title and lifecycle state of one reference.
func (s *noteService) ResolveTarget(ctx context.Context, ref EntityRef) (*LinkTarget, error) {
	if err := noteValidateRef(ref); err != nil {
		return nil, err
	}
	handle, err := s.noteHandle(ctx)
	if err != nil {
		return nil, err
	}
	resolver := noteResolver{ws: s.ws, projectID: s.projectID}
	return resolver.resolve(ctx, handle, ref)
}

// noteRefFromMarkdown maps a relative Markdown link to a note reference.
func noteRefFromMarkdown(target string) EntityRef {
	base := target
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, ".md")
	if !ids.Valid(base) {
		return EntityRef{}
	}
	return EntityRef{Type: schema.EntityNote, ID: base}
}

// noteRefValid reports whether ref can be resolved (type registered, UUIDv7).
func noteRefValid(ref EntityRef) bool {
	return ref.Type.Valid() && ids.Valid(ref.ID)
}

// noteValidateRef rejects malformed public ResolveTarget/relation inputs.
func noteValidateRef(ref EntityRef) error {
	if !ref.Type.Valid() {
		return fmt.Errorf("%w: %q", errs.ErrBadEntityType, ref.Type)
	}
	if !ids.Valid(ref.ID) {
		return fmt.Errorf("%w: %q", errs.ErrBadEntityID, ref.ID)
	}
	return nil
}

// resolve reports the title, soft-deletion and jump path of an entity. An
// unsupported (but registered) entity type is treated as missing: the notes UI
// only knows notes, records, papers, ideas and experiments.
func (r noteResolver) resolve(ctx context.Context, handle *workspace.ProjectHandle, ref EntityRef) (*LinkTarget, error) {
	target := &LinkTarget{Ref: ref}
	switch ref.Type {
	case schema.EntityNote:
		var (
			title    string
			filePath string
			deleted  sql.NullString
		)
		err := handle.DB().QueryRowContext(ctx,
			`SELECT title, file_path, deleted_at FROM notes WHERE id = ?`, ref.ID).
			Scan(&title, &filePath, &deleted)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			target.Missing = true
			return target, nil
		case err != nil:
			return nil, fmt.Errorf("notes: resolve note %s: %w", ref.ID, err)
		}
		target.Deleted = deleted.Valid
		target.FilePath = filePath
		if title == "" {
			title = r.deriveTitle(filePath)
		}
		target.Title = title
		return target, nil

	case schema.EntityDBRecord:
		var (
			title   string
			deleted sql.NullString
		)
		err := handle.DB().QueryRowContext(ctx, `SELECT title, deleted_at FROM db_records WHERE id = ?`, ref.ID).
			Scan(&title, &deleted)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			target.Missing = true
			return target, nil
		case err != nil:
			return nil, fmt.Errorf("notes: resolve db_record %s: %w", ref.ID, err)
		}
		target.Title = title
		target.Deleted = deleted.Valid
		return target, nil

	case schema.EntityIdea:
		return r.resolveProjectTitle(ctx, handle, "ideas", ref)

	case schema.EntityExperiment:
		return r.resolveProjectTitle(ctx, handle, "experiments", ref)

	case schema.EntityPaper:
		global := r.ws.Global()
		if global == nil {
			return nil, errors.New("notes: no global database")
		}
		var title string
		err := global.DB().QueryRowContext(ctx,
			`SELECT COALESCE(json_extract(metadata, '$.title'), '') FROM papers WHERE id = ?`, ref.ID).
			Scan(&title)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			target.Missing = true
			return target, nil
		case err != nil:
			return nil, fmt.Errorf("notes: resolve paper %s: %w", ref.ID, err)
		}
		target.Title = title
		return target, nil

	default:
		target.Missing = true
		return target, nil
	}
}

// resolveProjectTitle resolves the plain title/deleted_at pair of a
// soft-deletable project entity. The table name is a package constant, never
// user input.
func (r noteResolver) resolveProjectTitle(ctx context.Context, handle *workspace.ProjectHandle, table string, ref EntityRef) (*LinkTarget, error) {
	target := &LinkTarget{Ref: ref}
	var (
		title   string
		deleted sql.NullString
	)
	err := handle.DB().QueryRowContext(ctx, `SELECT title, deleted_at FROM `+table+` WHERE id = ?`, ref.ID).
		Scan(&title, &deleted)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		target.Missing = true
		return target, nil
	case err != nil:
		return nil, fmt.Errorf("notes: resolve %s %s: %w", ref.Type, ref.ID, err)
	}
	target.Title = title
	target.Deleted = deleted.Valid
	return target, nil
}

// deriveTitle falls back to the first body line when notes.title is empty
// (D-05-10: display only, never written back).
func (r noteResolver) deriveTitle(filePath string) string {
	abs, err := r.ws.Paths().Abs(filePath)
	if err != nil {
		return ""
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		return ""
	}
	return noteFirstLine(body)
}
