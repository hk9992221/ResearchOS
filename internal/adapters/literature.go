package adapters

import (
	"context"
	"fmt"

	"researchos/internal/literature"
	"researchos/internal/ports"
)

// Literature adapts literature.PaperIndex to ports.PaperIndex. It is a thin
// pass-through: the SPEC-04 implementation owns de-duplication, reference
// counting and ordering.
type Literature struct {
	src literature.PaperIndex
}

var _ ports.PaperIndex = (*Literature)(nil)

// NewLiterature binds the adapter to src. A nil src makes every method return
// ErrUnavailable; the export and delete previews then fall back to
// project_papers, which is the SPEC-03 §5.2 degraded path.
func NewLiterature(src literature.PaperIndex) *Literature {
	return &Literature{src: src}
}

// ListProjectPaperIDs returns the paper ids linked to the project, in the order
// the literature module provides them.
func (l *Literature) ListProjectPaperIDs(ctx context.Context, projectID string) ([]string, error) {
	if l == nil || l.src == nil {
		return nil, fmt.Errorf("adapters: literature index: %w", ErrUnavailable)
	}
	return l.src.ListProjectPaperIDs(ctx, projectID)
}

// CountPaperRefs reports how many other projects still reference the paper.
func (l *Literature) CountPaperRefs(ctx context.Context, paperID, excludeProjectID string) (int, error) {
	if l == nil || l.src == nil {
		return 0, fmt.Errorf("adapters: literature index: %w", ErrUnavailable)
	}
	return l.src.CountPaperRefs(ctx, paperID, excludeProjectID)
}

// ListProjectPapers projects the literature briefs onto the port type. Order is
// preserved and the slice is copied so callers cannot alias module memory.
func (l *Literature) ListProjectPapers(ctx context.Context, projectID string, limit int) ([]ports.PaperBrief, error) {
	if l == nil || l.src == nil {
		return nil, fmt.Errorf("adapters: literature index: %w", ErrUnavailable)
	}
	items, err := l.src.ListProjectPapers(ctx, projectID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ports.PaperBrief, len(items))
	for i, p := range items {
		out[i] = ports.PaperBrief{
			ID:       p.ID,
			Title:    p.Title,
			Authors:  p.Authors,
			Year:     p.Year,
			LinkedAt: p.LinkedAt,
			Missing:  p.Missing,
		}
	}
	return out, nil
}
