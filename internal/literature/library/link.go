package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"researchos/internal/foundation/events"
	"researchos/internal/foundation/projectpapers"
	"researchos/internal/literature"
)

// Link associates a global paper with a project (SPEC-04 §5.5). An archived
// project is rejected; a missing copy does not block the association and is
// reported through Broken. Re-linking is idempotent and emits no extra event.
func (s *Service) Link(ctx context.Context, projectID, paperID string, actor literature.Actor) (literature.LinkResult, error) {
	if _, err := s.activeProject(ctx, projectID); err != nil {
		return literature.LinkResult{}, fmt.Errorf("library: 关联文献: %w", err)
	}
	if err := validPaperID(paperID); err != nil {
		return literature.LinkResult{}, fmt.Errorf("library: 关联文献: %w", err)
	}
	if _, err := s.papers.Get(ctx, paperID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return literature.LinkResult{}, fmt.Errorf("library: 关联文献 %s: %w", paperID, literature.ErrPaperNotFound)
		}
		return literature.LinkResult{}, fmt.Errorf("library: 关联文献 %s: %w", paperID, err)
	}
	handle, err := s.ws.Project(ctx, projectID)
	if err != nil {
		return literature.LinkResult{}, fmt.Errorf("library: 打开项目 %s: %w", projectID, err)
	}
	var added bool
	if err := handle.InTx(ctx, func(tx *sql.Tx) error {
		var linkErr error
		added, linkErr = s.projectPapers(handle).Link(ctx, tx, projectpapers.Link{
			ProjectID: projectID,
			PaperID:   paperID,
			LinkedBy:  events.Actor(actor),
		})
		return linkErr
	}); err != nil {
		return literature.LinkResult{}, fmt.Errorf("library: 关联文献 %s: %w", paperID, err)
	}
	return literature.LinkResult{
		PaperID: paperID,
		Added:   added,
		Broken:  s.copyMissing(ctx, paperID),
	}, nil
}

// Unlink removes the project association only: the paper, its copy and every
// Paper Note survive (§5.5, INV-05). DetachedNotes lists the notes that still
// reference the paper so the UI can explain that they were kept.
func (s *Service) Unlink(ctx context.Context, projectID, paperID string, actor literature.Actor) (literature.UnlinkResult, error) {
	if _, err := s.activeProject(ctx, projectID); err != nil {
		return literature.UnlinkResult{}, fmt.Errorf("library: 解除文献关联: %w", err)
	}
	if err := validPaperID(paperID); err != nil {
		return literature.UnlinkResult{}, fmt.Errorf("library: 解除文献关联: %w", err)
	}
	handle, err := s.ws.Project(ctx, projectID)
	if err != nil {
		return literature.UnlinkResult{}, fmt.Errorf("library: 打开项目 %s: %w", projectID, err)
	}
	notes, err := s.noteIDsForPaper(ctx, handle, paperID)
	if err != nil {
		return literature.UnlinkResult{}, err
	}
	if err := handle.InTx(ctx, func(tx *sql.Tx) error {
		_, unlinkErr := s.projectPapers(handle).Unlink(ctx, tx, projectID, paperID, events.Actor(actor))
		return unlinkErr
	}); err != nil {
		return literature.UnlinkResult{}, fmt.Errorf("library: 解除文献关联 %s: %w", paperID, err)
	}
	return literature.UnlinkResult{PaperID: paperID, DetachedNotes: notes}, nil
}
