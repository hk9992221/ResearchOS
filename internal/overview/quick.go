package overview

import (
	"context"
	"strings"

	"researchos/internal/ports"
)

// quickNoteTitleLayout is the auto-generated title of a quick note: a local
// timestamp, so a burst of notes stays distinguishable in the list (§5.6).
const quickNoteTitleLayout = "2006-01-02 15:04"

// QuickNoteInput is the payload of QuickAddNote. An empty Title is generated
// from the clock; BodyMD is stored verbatim (it may be empty).
type QuickNoteInput struct {
	Title  string
	BodyMD string
}

// CompleteTask ticks a task off from the Overview through the SPEC-08 port with
// actor=user (§5.6, §3.2). It reports ErrFeatureUnavailable when Kanban is not
// registered, and ErrArchived (or the injected guard's error) for a read-only
// project.
func (s *Service) CompleteTask(ctx context.Context, projectID, taskID string) error {
	if err := s.guardWritable(ctx, projectID); err != nil {
		return err
	}
	if s.tasks == nil {
		return ErrFeatureUnavailable
	}
	if err := s.tasks.CompleteTask(ctx, projectID, taskID, ports.ActorUser); err != nil {
		return err
	}
	// The Kanban module owns the write and its own event; Overview only knows
	// its snapshot went stale. Bus-driven invalidation arrives with T3-17.
	s.Invalidate(projectID)
	return nil
}

// QuickAddNote creates a scratchpad note through the SPEC-05 port with
// actor=user (§5.6, §3.2 "添加笔记 → NoteCreator"). An empty title becomes
// "Quick note 2026-01-02 15:04" in the clock's own zone.
func (s *Service) QuickAddNote(ctx context.Context, projectID string, in QuickNoteInput) (ports.EntityRef, error) {
	if err := s.guardWritable(ctx, projectID); err != nil {
		return ports.EntityRef{}, err
	}
	if s.notes == nil {
		return ports.EntityRef{}, ErrFeatureUnavailable
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = "Quick note " + s.clock.Now().Format(quickNoteTitleLayout)
	}
	ref, err := s.notes.CreateScratchpad(ctx, projectID, title, in.BodyMD, ports.ActorUser)
	if err != nil {
		return ports.EntityRef{}, err
	}
	s.Invalidate(projectID)
	return ref, nil
}

// guardWritable is the §5.8 read-only check of the quick actions. An injected
// CheckWritable owns the decision entirely so its error type passes through
// unchanged; otherwise the project row answers: only "archived" blocks a write,
// and a missing row is ErrNotFound.
func (s *Service) guardWritable(ctx context.Context, projectID string) error {
	if s.checkWritable != nil {
		return s.checkWritable(ctx, projectID)
	}
	if s.ws == nil {
		return errNoWorkspace
	}
	head, err := s.header(ctx, projectID)
	if err != nil {
		return err
	}
	if head.Archived {
		return ErrArchived
	}
	return nil
}
