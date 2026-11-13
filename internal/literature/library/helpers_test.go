package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/papers"
	"researchos/internal/foundation/projectpapers"
	"researchos/internal/foundation/projects"
	"researchos/internal/foundation/relations"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/workspace"
	"researchos/internal/literature"
)

func newFixture(t *testing.T) *foundationtest.Fixture {
	t.Helper()
	f, err := foundationtest.NewWithProject(t.TempDir(), foundationtest.Options{})
	if err != nil {
		t.Fatalf("new fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Errorf("close fixture: %v", err)
		}
	})
	return f
}

type paperSeed struct {
	Key         string
	Title       string
	Year        int
	DOI         string
	Abstract    string
	Tags        []string
	Collections []string
	Creators    []literature.ZoteroCreator
	SourceState schema.PaperSourceState
	Copy        bool
	Content     string
}

// seedPaper inserts one global paper with its metadata snapshot and, optionally,
// a real imported copy.
func seedPaper(t *testing.T, f *foundationtest.Fixture, seed paperSeed) string {
	t.Helper()
	ctx := context.Background()
	md := literature.PaperMetadata{
		Key:          seed.Key,
		Title:        seed.Title,
		DOI:          seed.DOI,
		AbstractNote: seed.Abstract,
		Collections:  seed.Collections,
		Creators:     seed.Creators,
	}
	if seed.Year > 0 {
		md.Date = fmt.Sprintf("%d-01-01", seed.Year)
	}
	for _, tag := range seed.Tags {
		md.Tags = append(md.Tags, literature.ZoteroTag{Tag: tag})
	}
	raw, err := json.Marshal(md)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	content := seed.Content
	if seed.Copy && content == "" {
		content = "pdf:" + seed.Key
	}
	var id string
	if err := f.Global().InTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, _, err = f.Papers().Upsert(ctx, tx, papers.Paper{
			ZoteroKey:   seed.Key,
			Metadata:    raw,
			SourceState: seed.SourceState,
		})
		if err != nil {
			return err
		}
		if seed.Copy {
			return f.Papers().UpdateFile(ctx, tx, id,
				f.Workspace.Paths().PaperPDF(id), "hash-"+seed.Key, int64(len(content)), schema.PaperFileOK)
		}
		return nil
	}); err != nil {
		t.Fatalf("seed paper %s: %v", seed.Key, err)
	}
	if seed.Copy {
		writeCopy(t, f, id, content)
	}
	return id
}

func writeCopy(t *testing.T, f *foundationtest.Fixture, paperID, content string) {
	t.Helper()
	dir, err := f.Workspace.Paths().Abs(f.Workspace.Paths().PaperDir(paperID))
	if err != nil {
		t.Fatalf("resolve copy dir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create copy dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "source.pdf"), []byte(content), 0o644); err != nil {
		t.Fatalf("write copy: %v", err)
	}
}

func linkPaper(t *testing.T, f *foundationtest.Fixture, handle *workspace.ProjectHandle, paperID string) {
	t.Helper()
	linkPaperAt(t, f, handle, paperID, time.Time{})
}

func linkPaperAt(t *testing.T, f *foundationtest.Fixture, handle *workspace.ProjectHandle, paperID string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	store := f.ProjectPapers(handle)
	if err := handle.InTx(ctx, func(tx *sql.Tx) error {
		_, err := store.Link(ctx, tx, projectpapers.Link{
			ProjectID: handle.ID,
			PaperID:   paperID,
			LinkedAt:  at,
			LinkedBy:  schema.ActorUser,
		})
		return err
	}); err != nil {
		t.Fatalf("link paper %s: %v", paperID, err)
	}
}

// seedNote inserts a live note directly (no note service exists yet) and
// returns its id.
func seedNote(t *testing.T, f *foundationtest.Fixture, handle *workspace.ProjectHandle, kind schema.NoteKind) string {
	t.Helper()
	ctx := context.Background()
	noteID := ids.MustNew(f.Clock)
	rel := f.Workspace.Paths().NoteFile(handle.ID, noteID)
	now := clock.Format(f.Clock.Now())
	if err := handle.InTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO notes(id, title, file_path, kind, created_at, updated_at, file_hash, file_state)
			 VALUES (?, ?, ?, ?, ?, ?, '', 'ok')`,
			noteID, "笔记", rel, string(kind), now, now)
		return err
	}); err != nil {
		t.Fatalf("seed note: %v", err)
	}
	return noteID
}

func linkNoteToPaper(t *testing.T, f *foundationtest.Fixture, handle *workspace.ProjectHandle, noteID, paperID string) {
	t.Helper()
	ctx := context.Background()
	service := f.Relations(handle)
	if err := handle.InTx(ctx, func(tx *sql.Tx) error {
		_, err := service.Link(ctx, tx, relations.Link{
			SrcType:  schema.EntityNote,
			SrcID:    noteID,
			Relation: schema.RelationReferences,
			DstType:  schema.EntityPaper,
			DstID:    paperID,
		}, schema.ActorUser)
		return err
	}); err != nil {
		t.Fatalf("link note %s -> paper %s: %v", noteID, paperID, err)
	}
}

// seedRawRelation inserts a relation row bypassing the service direction
// registry. Tests use it to simulate dirty data outside the allowed contract.
func seedRawRelation(t *testing.T, f *foundationtest.Fixture, handle *workspace.ProjectHandle, srcType schema.EntityType, srcID string, relation schema.Relation, dstType schema.EntityType, dstID string) {
	t.Helper()
	ctx := context.Background()
	if err := handle.InTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO entity_relations(id, src_type, src_id, relation, dst_type, dst_id, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			ids.MustNew(f.Clock), string(srcType), srcID, string(relation), string(dstType), dstID,
			clock.Format(f.Clock.Now()))
		return err
	}); err != nil {
		t.Fatalf("seed raw relation: %v", err)
	}
}

func softDeleteNote(t *testing.T, f *foundationtest.Fixture, handle *workspace.ProjectHandle, noteID string) {
	t.Helper()
	if err := handle.InTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(),
			`UPDATE notes SET deleted_at = ? WHERE id = ?`, clock.Format(f.Clock.Now()), noteID)
		return err
	}); err != nil {
		t.Fatalf("soft delete note: %v", err)
	}
}

// seedEvidence creates an idea plus one evidence row sourced from paperID.
func seedEvidence(t *testing.T, f *foundationtest.Fixture, handle *workspace.ProjectHandle, paperID string, deleted bool) {
	t.Helper()
	ctx := context.Background()
	ideaID := ids.MustNew(f.Clock)
	evidenceID := ids.MustNew(f.Clock)
	now := clock.Format(f.Clock.Now())
	var deletedAt any
	if deleted {
		deletedAt = now
	}
	if err := handle.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO ideas(id, title, status, created_at, updated_at) VALUES (?, 'seed', 'New', ?, ?)`,
			ideaID, now, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO evidence(id, idea_id, direction, content, source_type, source_id,
			                      ai_generated, confirmed, provenance, created_at, updated_at, deleted_at)
			 VALUES (?, ?, 'support', '', 'paper', ?, 0, 0, '{}', ?, ?, ?)`,
			evidenceID, ideaID, paperID, now, now, deletedAt)
		return err
	}); err != nil {
		t.Fatalf("seed evidence: %v", err)
	}
}

func countActivities(t *testing.T, handle *workspace.ProjectHandle, eventType, entityID string) int {
	t.Helper()
	query := `SELECT count(*) FROM activities WHERE event_type = ?`
	args := []any{eventType}
	if entityID != "" {
		query += ` AND entity_id = ?`
		args = append(args, entityID)
	}
	var n int
	if err := handle.DB().QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count activities: %v", err)
	}
	return n
}

func archiveProject(t *testing.T, f *foundationtest.Fixture, projectID string) {
	t.Helper()
	store := projects.New(f.Global().DB(), f.Clock)
	if err := store.SetStatus(context.Background(), nil, projectID, schema.ProjectArchived, schema.ActorUser); err != nil {
		t.Fatalf("archive project: %v", err)
	}
}
