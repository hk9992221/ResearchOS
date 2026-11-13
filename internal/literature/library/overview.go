package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"

	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
	"researchos/internal/literature"
	"researchos/internal/notes"
)

var rosRefPattern = regexp.MustCompile(`ros://(note|paper|idea)/([0-9a-f-]{36})`)

const overviewTitle = "文献综述"

func (s *Service) overviewNote(ctx context.Context, projectID string) (noteID, bodyPath string, createdAt, updatedAt string, err error) {
	handle, err := s.ws.Project(ctx, projectID)
	if err != nil {
		return "", "", "", "", fmt.Errorf("library: 打开项目 %s: %w", projectID, err)
	}
	err = handle.DB().QueryRowContext(ctx,
		`SELECT id, file_path, created_at, updated_at FROM notes
		  WHERE kind = 'overview' AND deleted_at IS NULL
		  ORDER BY created_at, id LIMIT 1`).Scan(&noteID, &bodyPath, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", "", nil
	}
	if err != nil {
		return "", "", "", "", fmt.Errorf("library: 读取 Literature Overview: %w", err)
	}
	return noteID, bodyPath, createdAt, updatedAt, nil
}

// Get returns the project's overview note, or (nil, nil).
func (s *Service) getOverview(ctx context.Context, projectID string) (*literature.LiteratureOverview, error) {
	noteID, _, created, updated, err := s.overviewNote(ctx, projectID)
	if err != nil || noteID == "" {
		return nil, err
	}
	svc, _, err := s.notesFor(ctx, projectID)
	if err != nil {
		return nil, err
	}
	body, err := svc.LoadBody(ctx, noteID)
	if err != nil && !isMissingBody(err) {
		return nil, err
	}
	out := &literature.LiteratureOverview{NoteID: noteID, ProjectID: projectID}
	if body != nil {
		out.BodyMD = body.Markdown
		out.BodyHash = body.Hash
	}
	if t, err := parseDBTime(created); err == nil {
		out.CreatedAt = t
	}
	if t, err := parseDBTime(updated); err == nil {
		out.UpdatedAt = t
	}
	refs, err := s.resolveReferences(ctx, projectID, out.BodyMD)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		if ref.EntityType == "note" && ref.Exists {
			out.ReferencedNoteIDs = append(out.ReferencedNoteIDs, ref.EntityID)
		}
	}
	return out, nil
}

func isMissingBody(err error) bool { return errors.Is(err, errs.ErrNoteFileMissing) }

// Save creates the singleton overview note on first use and saves its body.
func (s *Service) saveOverview(ctx context.Context, projectID, bodyMD, baseHash string, actor literature.Actor) (literature.OverviewSaveResult, error) {
	if err := s.requireWritable(ctx, projectID); err != nil {
		return literature.OverviewSaveResult{}, err
	}
	noteID, _, _, _, err := s.overviewNote(ctx, projectID)
	if err != nil {
		return literature.OverviewSaveResult{}, err
	}
	svc, _, err := s.notesFor(ctx, projectID)
	if err != nil {
		return literature.OverviewSaveResult{}, err
	}
	if noteID == "" {
		if baseHash != "" {
			return literature.OverviewSaveResult{Conflict: true}, nil
		}
		created, err := svc.Create(ctx, notes.CreateNoteInput{
			Kind: notes.NoteKind(schema.NoteOverview), Title: overviewTitle, Body: bodyMD,
			Actor: libraryActor(actor),
		})
		if err != nil {
			return literature.OverviewSaveResult{}, err
		}
		noteID = created.ID
	} else {
		res, err := svc.SaveBody(ctx, notes.SaveBodyInput{
			NoteID: noteID, Markdown: bodyMD, BaseHash: baseHash, Actor: libraryActor(actor),
		})
		if err != nil {
			return literature.OverviewSaveResult{}, err
		}
		if res.Conflict {
			return literature.OverviewSaveResult{Conflict: true, DiskHash: res.DiskHash}, nil
		}
	}
	overview, err := s.getOverview(ctx, projectID)
	if err != nil || overview == nil {
		return literature.OverviewSaveResult{}, err
	}
	return literature.OverviewSaveResult{Overview: *overview}, nil
}

// Comparison projects the linked papers and their Paper Note fields.
func (s *Service) comparison(ctx context.Context, projectID string, paperIDs []string) ([]literature.OverviewComparisonRow, error) {
	if _, err := s.ws.Project(ctx, projectID); err != nil {
		return nil, err
	}
	handle, err := s.ws.Project(ctx, projectID)
	if err != nil {
		return nil, err
	}
	rows, err := handle.DB().QueryContext(ctx,
		`SELECT paper_id FROM project_papers WHERE project_id = ? ORDER BY linked_at, paper_id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("library: 列出项目文献: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(paperIDs) > 0 {
		want := make(map[string]bool, len(paperIDs))
		for _, id := range paperIDs {
			want[id] = true
		}
		filtered := ids[:0]
		for _, id := range ids {
			if want[id] {
				filtered = append(filtered, id)
			}
		}
		ids = filtered
	}
	out := make([]literature.OverviewComparisonRow, 0, len(ids))
	for _, paperID := range ids {
		paper, err := s.papers.Get(ctx, paperID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return nil, err
		}
		md, err := decodeMetadata(paper)
		if err != nil {
			return nil, err
		}
		row := literature.OverviewComparisonRow{
			PaperID:     paperID,
			Title:       md.Title,
			Year:        md.Year,
			Venue:       md.PublicationTitle,
			Authors:     md.Authors,
			SourceState: string(paper.SourceState),
		}
		note, err := s.getPaperNote(ctx, projectID, paperID)
		if err != nil {
			return nil, err
		}
		if note != nil && note.DeletedAt == nil {
			row.NoteID = note.NoteID
			row.Fields = note.Fields
		}
		out = append(out, row)
	}
	return out, nil
}

// ResolveReferences resolves ros:// links without modifying the body.
func (s *Service) resolveReferences(ctx context.Context, projectID, bodyMD string) ([]literature.ReferenceResolution, error) {
	matches := rosRefPattern.FindAllStringSubmatch(bodyMD, -1)
	if len(matches) == 0 {
		return nil, nil
	}
	handle, err := s.ws.Project(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]literature.ReferenceResolution, 0, len(matches))
	for _, m := range matches {
		ref := literature.ReferenceResolution{
			Raw: m[0], EntityType: m[1], EntityID: m[2],
			Exists: ids.Valid(m[2]),
		}
		switch m[1] {
		case "note":
			var title string
			err := handle.DB().QueryRowContext(ctx, `SELECT title FROM notes WHERE id = ?`, m[2]).Scan(&title)
			if err == nil {
				ref.Title, ref.Exists = title, true
			} else if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		case "idea":
			var title string
			err := handle.DB().QueryRowContext(ctx, `SELECT title FROM ideas WHERE id = ?`, m[2]).Scan(&title)
			if err == nil {
				ref.Title, ref.Exists = title, true
			} else if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		case "paper":
			paper, err := s.papers.Get(ctx, m[2])
			if err == nil {
				md, derr := decodeMetadata(paper)
				if derr != nil {
					return nil, derr
				}
				ref.Title, ref.Exists = md.Title, true
			} else if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}
		out = append(out, ref)
	}
	return out, nil
}

// Overviews exposes the LIT-08 surface.
func (s *Service) Overviews() literature.OverviewService { return &overviewAPI{s} }

type overviewAPI struct{ *Service }

var _ literature.OverviewService = (*overviewAPI)(nil)

func (a *overviewAPI) Get(ctx context.Context, projectID string) (*literature.LiteratureOverview, error) {
	return a.getOverview(ctx, projectID)
}
func (a *overviewAPI) Save(ctx context.Context, projectID, bodyMD, baseHash string, actor literature.Actor) (literature.OverviewSaveResult, error) {
	return a.saveOverview(ctx, projectID, bodyMD, baseHash, actor)
}
func (a *overviewAPI) Comparison(ctx context.Context, projectID string, paperIDs []string) ([]literature.OverviewComparisonRow, error) {
	return a.comparison(ctx, projectID, paperIDs)
}
func (a *overviewAPI) ResolveReferences(ctx context.Context, projectID, bodyMD string) ([]literature.ReferenceResolution, error) {
	return a.resolveReferences(ctx, projectID, bodyMD)
}
