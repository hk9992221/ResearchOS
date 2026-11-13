package library

import (
	"context"
	"errors"
	"testing"

	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
	"researchos/internal/literature"
)

func TestLinkIsIdempotentAndEmitsOneEvent(t *testing.T) {
	f := newFixture(t)
	service := newService(f)
	ctx := context.Background()
	paperID := seedPaper(t, f, paperSeed{Key: "LINK1", Title: "link", Copy: true})

	first, err := service.Link(ctx, f.ProjectID, paperID, literature.ActorUser)
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	if !first.Added {
		t.Error("first link reported Added=false")
	}
	if first.Broken {
		t.Error("link with a copy reported Broken=true")
	}
	if first.PaperID != paperID {
		t.Errorf("paper id = %s, want %s", first.PaperID, paperID)
	}

	second, err := service.Link(ctx, f.ProjectID, paperID, literature.ActorUser)
	if err != nil {
		t.Fatalf("second link: %v", err)
	}
	if second.Added {
		t.Error("second link reported Added=true")
	}
	if n := countActivities(t, f.Project, "relation.added", paperID); n != 1 {
		t.Errorf("relation.added events = %d, want 1", n)
	}

	page, err := service.QueryProjectPapers(ctx, f.ProjectID, literature.PaperFilter{})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if page.Total != 1 {
		t.Errorf("project papers = %d, want 1", page.Total)
	}
}

func TestLinkAllowsBrokenCopy(t *testing.T) {
	f := newFixture(t)
	service := newService(f)
	paperID := seedPaper(t, f, paperSeed{Key: "LINK2", Title: "broken"})

	result, err := service.Link(context.Background(), f.ProjectID, paperID, literature.ActorUser)
	if err != nil {
		t.Fatalf("link broken: %v", err)
	}
	if !result.Added || !result.Broken {
		t.Errorf("result = %+v, want added and broken", result)
	}
}

func TestLinkRejectsArchivedProject(t *testing.T) {
	f := newFixture(t)
	service := newService(f)
	ctx := context.Background()
	paperID := seedPaper(t, f, paperSeed{Key: "LINK3", Title: "archived", Copy: true})
	archiveProject(t, f, f.ProjectID)

	if _, err := service.Link(ctx, f.ProjectID, paperID, literature.ActorUser); !errors.Is(err, literature.ErrProjectArchived) {
		t.Errorf("link error = %v, want ErrProjectArchived", err)
	}
	if _, err := service.Unlink(ctx, f.ProjectID, paperID, literature.ActorUser); !errors.Is(err, literature.ErrProjectArchived) {
		t.Errorf("unlink error = %v, want ErrProjectArchived", err)
	}
}

func TestLinkPaperNotFound(t *testing.T) {
	f := newFixture(t)
	service := newService(f)
	_, err := service.Link(context.Background(), f.ProjectID, ids.MustNew(f.Clock), literature.ActorUser)
	if !errors.Is(err, literature.ErrPaperNotFound) {
		t.Errorf("error = %v, want ErrPaperNotFound", err)
	}
}

func TestLinkUnknownProject(t *testing.T) {
	f := newFixture(t)
	service := newService(f)
	paperID := seedPaper(t, f, paperSeed{Key: "LINK4", Title: "unknown project", Copy: true})
	if _, err := service.Link(context.Background(), ids.MustNew(f.Clock), paperID, literature.ActorUser); err == nil {
		t.Fatal("unknown project was accepted")
	}
}

func TestUnlinkKeepsPaperCopyAndNotes(t *testing.T) {
	f := newFixture(t)
	service := newService(f)
	ctx := context.Background()
	paperID := seedPaper(t, f, paperSeed{Key: "UNLINK1", Title: "unlink", Copy: true})
	if _, err := service.Link(ctx, f.ProjectID, paperID, literature.ActorUser); err != nil {
		t.Fatalf("link: %v", err)
	}
	noteID := seedNote(t, f, f.Project, schema.NotePaperNote)
	linkNoteToPaper(t, f, f.Project, noteID, paperID)

	result, err := service.Unlink(ctx, f.ProjectID, paperID, literature.ActorUser)
	if err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if result.PaperID != paperID {
		t.Errorf("paper id = %s, want %s", result.PaperID, paperID)
	}
	if len(result.DetachedNotes) != 1 || result.DetachedNotes[0] != noteID {
		t.Errorf("detached notes = %v, want [%s]", result.DetachedNotes, noteID)
	}
	if n := countActivities(t, f.Project, "relation.removed", paperID); n != 1 {
		t.Errorf("relation.removed events = %d, want 1", n)
	}

	page, err := service.QueryProjectPapers(ctx, f.ProjectID, literature.PaperFilter{})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if page.Total != 0 {
		t.Errorf("project papers after unlink = %d, want 0", page.Total)
	}
	if _, err := service.GetPaper(ctx, paperID); err != nil {
		t.Fatalf("global paper disappeared: %v", err)
	}
	if _, _, err := NewFileStore(f.Workspace).Stat(ctx, paperID); err != nil {
		t.Errorf("copy disappeared: %v", err)
	}
	var deleted any
	if err := f.Project.DB().QueryRowContext(ctx,
		`SELECT deleted_at FROM notes WHERE id = ?`, noteID).Scan(&deleted); err != nil {
		t.Fatalf("note disappeared: %v", err)
	}
	if deleted != nil {
		t.Errorf("note was deleted: %v", deleted)
	}

	// A second unlink is idempotent (the note relation keeps reporting the note).
	again, err := service.Unlink(ctx, f.ProjectID, paperID, literature.ActorUser)
	if err != nil {
		t.Fatalf("second unlink: %v", err)
	}
	if len(again.DetachedNotes) != 1 || again.DetachedNotes[0] != noteID {
		t.Errorf("second detached notes = %v", again.DetachedNotes)
	}
	if n := countActivities(t, f.Project, "relation.removed", paperID); n != 1 {
		t.Errorf("relation.removed events after repeat = %d, want 1", n)
	}
}

func TestUnlinkMissingAssociationIsIdempotent(t *testing.T) {
	f := newFixture(t)
	service := newService(f)
	paperID := seedPaper(t, f, paperSeed{Key: "UNLINK2", Title: "never linked", Copy: true})
	result, err := service.Unlink(context.Background(), f.ProjectID, paperID, literature.ActorUser)
	if err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if result.PaperID != paperID || len(result.DetachedNotes) != 0 {
		t.Errorf("result = %+v", result)
	}
	if n := countActivities(t, f.Project, "relation.removed", paperID); n != 0 {
		t.Errorf("relation.removed events = %d, want 0", n)
	}
}
