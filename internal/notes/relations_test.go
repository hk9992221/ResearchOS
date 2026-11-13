package notes

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
)

// coreEventQuery filters activities by one event type.
func coreEventQuery(typ schema.EventType) events.Query {
	return events.Query{EventTypes: []events.Type{typ}}
}

// TestCoreOutboundLinks covers acceptance item 11's read side: ros:// and
// <note-id>.md links resolve, dangling targets are reported as missing and a
// soft-deleted target is flagged without being dropped.
func TestCoreOutboundLinks(t *testing.T) {
	f := coreNewFixture(t)
	target := f.create("Target note", "target body")
	paperID := f.seedPaper("Reference paper")
	ideaID := f.seedIdea("Idea about notes")
	missingID := "018f0000-0000-7000-8000-000000000000"

	body := fmt.Sprintf(
		"See [Target](%s.md), [Paper](ros://paper/%s), [Idea](ros://idea/%s), [Gone](ros://note/%s).",
		target.ID, paperID, ideaID, missingID)
	note := f.create("Linking note", body)

	links, err := f.notes.OutboundLinks(f.ctx, note.ID)
	if err != nil {
		t.Fatalf("outbound: %v", err)
	}
	if len(links) != 4 {
		t.Fatalf("links = %d, want 4: %+v", len(links), links)
	}
	if links[0].Ref.Type != schema.EntityNote || links[0].Ref.ID != target.ID ||
		links[0].Title != "Target note" {
		t.Fatalf("first link = %+v", links[0])
	}
	if links[0].Raw != target.ID+".md" {
		t.Fatalf("raw = %q", links[0].Raw)
	}
	if links[1].Ref.Type != schema.EntityPaper || links[1].Title != "Reference paper" || links[1].Missing {
		t.Fatalf("paper link = %+v", links[1])
	}
	if links[2].Ref.Type != schema.EntityIdea || links[2].Title != "Idea about notes" {
		t.Fatalf("idea link = %+v", links[2])
	}
	if !links[3].Missing || links[3].Ref.ID != missingID {
		t.Fatalf("dangling link = %+v", links[3])
	}

	// Soft delete keeps the link and marks it deleted.
	if err := f.notes.SoftDelete(f.ctx, target.ID, ""); err != nil {
		t.Fatalf("soft delete target: %v", err)
	}
	links, err = f.notes.OutboundLinks(f.ctx, note.ID)
	if err != nil {
		t.Fatalf("outbound after delete: %v", err)
	}
	if !links[0].Deleted || links[0].Missing {
		t.Fatalf("deleted target link = %+v", links[0])
	}
}

// TestCoreResolveTarget checks titles and lifecycle flags across entity types.
func TestCoreResolveTarget(t *testing.T) {
	f := coreNewFixture(t)
	note := f.create("", "First line as title\nsecond")
	paperID := f.seedPaper("A paper title")
	ideaID := f.seedIdea("An idea title")

	got, err := f.notes.ResolveTarget(f.ctx, EntityRef{Type: schema.EntityNote, ID: note.ID})
	if err != nil {
		t.Fatalf("resolve note: %v", err)
	}
	if got.Title != "First line as title" || got.Deleted || got.Missing {
		t.Fatalf("note target = %+v", got)
	}
	if got.FilePath == "" {
		t.Fatal("note target has no file path")
	}

	got, err = f.notes.ResolveTarget(f.ctx, EntityRef{Type: schema.EntityPaper, ID: paperID})
	if err != nil {
		t.Fatalf("resolve paper: %v", err)
	}
	if got.Title != "A paper title" {
		t.Fatalf("paper title = %q", got.Title)
	}

	got, err = f.notes.ResolveTarget(f.ctx, EntityRef{Type: schema.EntityIdea, ID: ideaID})
	if err != nil {
		t.Fatalf("resolve idea: %v", err)
	}
	if got.Title != "An idea title" {
		t.Fatalf("idea title = %q", got.Title)
	}

	got, err = f.notes.ResolveTarget(f.ctx, EntityRef{
		Type: schema.EntityNote, ID: "018f0000-0000-7000-8000-000000000000",
	})
	if err != nil {
		t.Fatalf("resolve missing: %v", err)
	}
	if !got.Missing {
		t.Fatalf("missing target = %+v", got)
	}

	if _, err := f.notes.ResolveTarget(f.ctx, EntityRef{Type: "bogus", ID: note.ID}); !errors.Is(err, errs.ErrBadEntityType) {
		t.Fatalf("invalid type error = %v", err)
	}
	if _, err := f.notes.ResolveTarget(f.ctx, EntityRef{Type: schema.EntityNote, ID: "x"}); !errors.Is(err, errs.ErrBadEntityID) {
		t.Fatalf("invalid id error = %v", err)
	}
}

// TestCoreRelationMapping covers the §5.5 table, self-relation rejection,
// duplicate idempotency and the no-symmetric-copy rule.
func TestCoreRelationMapping(t *testing.T) {
	f := coreNewFixture(t)
	src := f.create("Source", "body")
	other := f.create("Other note", "body")
	paperID := f.seedPaper("Paper")
	ideaID := f.seedIdea("Idea")
	dbID, _ := f.seedDatabase("Rows")
	page := f.create("Row page", "body")
	record, err := f.notes.PromoteToDatabase(f.ctx, PromoteInput{NoteID: page.ID, DatabaseID: dbID})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	recordRef := EntityRef{Type: schema.EntityDBRecord, ID: record.ID}

	noteRef := func(id string) EntityRef { return EntityRef{Type: schema.EntityNote, ID: id} }

	// note -> paper and note -> note are references.
	if err := f.rels.Add(f.ctx, RelationEdge{Src: noteRef(src.ID), Dst: EntityRef{Type: schema.EntityPaper, ID: paperID}}); err != nil {
		t.Fatalf("note->paper: %v", err)
	}
	if err := f.rels.Add(f.ctx, RelationEdge{Src: noteRef(src.ID), Dst: noteRef(other.ID)}); err != nil {
		t.Fatalf("note->note: %v", err)
	}
	// note -> idea is related_to.
	if err := f.rels.Add(f.ctx, RelationEdge{Src: noteRef(src.ID), Dst: EntityRef{Type: schema.EntityIdea, ID: ideaID}}); err != nil {
		t.Fatalf("note->idea: %v", err)
	}

	from, err := f.rels.ListFrom(f.ctx, noteRef(src.ID), "")
	if err != nil {
		t.Fatalf("list from: %v", err)
	}
	if len(from) != 3 {
		t.Fatalf("edges = %d, want 3", len(from))
	}
	byRelation := map[schema.Relation]int{}
	for _, link := range from {
		byRelation[link.Relation]++
		if link.Ref.ID == "" || link.Title == "" {
			t.Fatalf("unresolved link: %+v", link)
		}
	}
	if byRelation[schema.RelationReferences] != 2 || byRelation[schema.RelationRelatedTo] != 1 {
		t.Fatalf("relation counts = %+v", byRelation)
	}

	// The wrong relation for a pair is rejected.
	if err := f.rels.Add(f.ctx, RelationEdge{
		Src:      noteRef(src.ID),
		Relation: schema.RelationRelatedTo,
		Dst:      EntityRef{Type: schema.EntityPaper, ID: paperID},
	}); !errors.Is(err, errs.ErrRelationNotAllowed) {
		t.Fatalf("wrong relation error = %v", err)
	}
	// Self relations are rejected by the foundation.
	if err := f.rels.Add(f.ctx, RelationEdge{Src: noteRef(src.ID), Dst: noteRef(src.ID)}); !errors.Is(err, errs.ErrSelfRelation) {
		t.Fatalf("self relation error = %v", err)
	}
	// A db_record source is normalized to its backing note: record -> paper
	// uses references and is stored with src_type='note' (SPEC-01 §4.6, D-05-03).
	if err := f.rels.Add(f.ctx, RelationEdge{
		Src: recordRef,
		Dst: EntityRef{Type: schema.EntityPaper, ID: paperID},
	}); err != nil {
		t.Fatalf("db_record->paper: %v", err)
	}
	var (
		storedSrcType string
		storedSrcID   string
	)
	if err := f.handle.DB().QueryRow(
		`SELECT src_type, src_id FROM entity_relations WHERE src_id = ? AND dst_id = ? AND relation = 'references'`,
		page.ID, paperID).Scan(&storedSrcType, &storedSrcID); err != nil {
		t.Fatalf("record relation row: %v", err)
	}
	if storedSrcType != string(schema.EntityNote) || storedSrcID != page.ID {
		t.Fatalf("record edge stored as %s/%s, want note/%s", storedSrcType, storedSrcID, page.ID)
	}
	// A record can be related_to an idea.
	if err := f.rels.Add(f.ctx, RelationEdge{
		Src: recordRef,
		Dst: EntityRef{Type: schema.EntityIdea, ID: ideaID},
	}); err != nil {
		t.Fatalf("db_record->idea: %v", err)
	}

	// Duplicates are idempotent: no new row and no second relation.added.
	before := len(f.activities(coreEventQuery(schema.EventRelationAdded)))
	if err := f.rels.Add(f.ctx, RelationEdge{Src: noteRef(src.ID), Dst: EntityRef{Type: schema.EntityPaper, ID: paperID}}); err != nil {
		t.Fatalf("duplicate add: %v", err)
	}
	if after := len(f.activities(coreEventQuery(schema.EventRelationAdded))); after != before {
		t.Fatalf("duplicate add wrote %d events", after-before)
	}
	// No symmetric copy: the note only has outbound edges.
	inboundOfSrc, err := f.rels.ListTo(f.ctx, noteRef(src.ID), "")
	if err != nil {
		t.Fatalf("list to: %v", err)
	}
	if len(inboundOfSrc) != 0 {
		t.Fatalf("symmetric copies exist: %+v", inboundOfSrc)
	}

	// ListTo finds the edge on the target side.
	inbound, err := f.rels.ListTo(f.ctx, noteRef(other.ID), string(schema.RelationReferences))
	if err != nil {
		t.Fatalf("list to target: %v", err)
	}
	if len(inbound) != 1 || inbound[0].Ref.ID != src.ID || inbound[0].Title != "Source" {
		t.Fatalf("inbound = %+v", inbound)
	}
}

// TestCoreRelationSoftDeletedSource checks ListTo still resolves a source that
// has been trashed.
func TestCoreRelationSoftDeletedSource(t *testing.T) {
	f := coreNewFixture(t)
	src := f.create("Trashed source", "body")
	dst := f.create("Survivor", "body")
	if err := f.rels.Add(f.ctx, RelationEdge{
		Src: EntityRef{Type: schema.EntityNote, ID: src.ID},
		Dst: EntityRef{Type: schema.EntityNote, ID: dst.ID},
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := f.notes.SoftDelete(f.ctx, src.ID, ""); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	links, err := f.rels.ListTo(f.ctx, EntityRef{Type: schema.EntityNote, ID: dst.ID}, "")
	if err != nil {
		t.Fatalf("list to: %v", err)
	}
	if len(links) != 1 || !links[0].Deleted || links[0].Title != "Trashed source" {
		t.Fatalf("link = %+v", links)
	}
	if got := len(f.activities(coreEventQuery(schema.EventRelationRemoved))); got != 0 {
		t.Fatalf("soft delete removed %d edges", got)
	}
}

// TestCoreRelationRemove checks removal and its idempotency.
func TestCoreRelationRemove(t *testing.T) {
	f := coreNewFixture(t)
	src := f.create("Source", "body")
	dst := f.create("Target", "body")
	edge := RelationEdge{
		Src: EntityRef{Type: schema.EntityNote, ID: src.ID},
		Dst: EntityRef{Type: schema.EntityNote, ID: dst.ID},
	}
	if err := f.rels.Add(f.ctx, edge); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := f.rels.Remove(f.ctx, edge); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := f.rels.Remove(f.ctx, edge); err != nil {
		t.Fatalf("remove again: %v", err)
	}
	links, err := f.rels.ListFrom(f.ctx, edge.Src, "")
	if err != nil {
		t.Fatalf("list from: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("edges left = %d", len(links))
	}
	if got := len(f.activities(coreEventQuery(schema.EventRelationRemoved))); got != 1 {
		t.Fatalf("relation.removed = %d, want 1", got)
	}
}

// TestCoreRelationValidation checks the argument guards.
func TestCoreRelationValidation(t *testing.T) {
	f := coreNewFixture(t)
	note := f.create("N", "body")
	noteRef := EntityRef{Type: schema.EntityNote, ID: note.ID}

	if err := f.rels.Add(f.ctx, RelationEdge{Src: noteRef, Dst: EntityRef{Type: schema.EntityTask, ID: note.ID}}); !errors.Is(err, errs.ErrRelationNotAllowed) {
		t.Fatalf("unsupported dst error = %v", err)
	}
	if _, err := f.rels.ListFrom(f.ctx, noteRef, "nonsense"); !errors.Is(err, errs.ErrRelationNotAllowed) {
		t.Fatalf("bad filter error = %v", err)
	}
	if _, err := f.rels.ListFrom(f.ctx, EntityRef{Type: "bogus", ID: note.ID}, ""); !errors.Is(err, errs.ErrBadEntityType) {
		t.Fatalf("bad type error = %v", err)
	}
}

// TestCoreRecordRelationsUseBackingNote proves a record behaves as the relation
// owner (SPEC-05 §5.5, D-05-03): outbound edges are stored on its backing note,
// ListFrom(record) reads them, ListTo(note) sees them, and inbound edges to the
// record itself stay listable.
func TestCoreRecordRelationsUseBackingNote(t *testing.T) {
	f := coreNewFixture(t)
	dbID, _ := f.seedDatabase("Records")
	page := f.create("Record page", "body")
	record, err := f.notes.PromoteToDatabase(f.ctx, PromoteInput{NoteID: page.ID, DatabaseID: dbID})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	paperID := f.seedPaper("Cited paper")
	recordRef := EntityRef{Type: schema.EntityDBRecord, ID: record.ID}
	pageRef := EntityRef{Type: schema.EntityNote, ID: page.ID}

	// Add(record -> paper) succeeds and stores the edge on the backing note.
	if err := f.rels.Add(f.ctx, RelationEdge{Src: recordRef, Dst: EntityRef{Type: schema.EntityPaper, ID: paperID}}); err != nil {
		t.Fatalf("record->paper: %v", err)
	}
	var (
		srcType  string
		srcID    string
		relation string
	)
	if err := f.handle.DB().QueryRow(
		`SELECT src_type, src_id, relation FROM entity_relations WHERE dst_id = ?`, paperID).
		Scan(&srcType, &srcID, &relation); err != nil {
		t.Fatalf("relation row: %v", err)
	}
	if srcType != string(schema.EntityNote) || srcID != page.ID || relation != string(schema.RelationReferences) {
		t.Fatalf("stored edge = %s/%s/%s", srcType, srcID, relation)
	}

	// ListFrom(record) returns it, resolved to the paper.
	from, err := f.rels.ListFrom(f.ctx, recordRef, "")
	if err != nil {
		t.Fatalf("list from record: %v", err)
	}
	if len(from) != 1 || from[0].Ref.Type != schema.EntityPaper || from[0].Ref.ID != paperID ||
		from[0].Title != "Cited paper" || from[0].Relation != schema.RelationReferences {
		t.Fatalf("record outbound = %+v", from)
	}

	// ListTo(paper) sees the edge; its source resolves to the backing note.
	inboundPaper, err := f.rels.ListTo(f.ctx, EntityRef{Type: schema.EntityPaper, ID: paperID}, string(schema.RelationReferences))
	if err != nil {
		t.Fatalf("list to paper: %v", err)
	}
	if len(inboundPaper) != 1 || inboundPaper[0].Ref.ID != page.ID ||
		inboundPaper[0].Title != "Record page" || inboundPaper[0].Relation != schema.RelationReferences {
		t.Fatalf("paper inbound = %+v", inboundPaper)
	}
	// The edge is stored with the note as source, so inbound-to-the-note is
	// empty: ListTo really means dst = ref.
	inboundPage, err := f.rels.ListTo(f.ctx, pageRef, "")
	if err != nil {
		t.Fatalf("list to page note: %v", err)
	}
	if len(inboundPage) != 0 {
		t.Fatalf("unexpected inbound to the page note = %+v", inboundPage)
	}

	// A record can reference a note; ListTo(that note) resolves the record's
	// backing note as the source.
	other := f.create("Other note", "body")
	if err := f.rels.Add(f.ctx, RelationEdge{
		Src: recordRef,
		Dst: EntityRef{Type: schema.EntityNote, ID: other.ID},
	}); err != nil {
		t.Fatalf("record->note: %v", err)
	}
	inboundOther, err := f.rels.ListTo(f.ctx, EntityRef{Type: schema.EntityNote, ID: other.ID}, "")
	if err != nil {
		t.Fatalf("list to other note: %v", err)
	}
	if len(inboundOther) != 1 || inboundOther[0].Ref.ID != page.ID ||
		inboundOther[0].Title != "Record page" || inboundOther[0].Relation != schema.RelationReferences {
		t.Fatalf("other inbound = %+v", inboundOther)
	}

	// Inbound edges that point at the record are not normalized: a record can
	// be a relation target (note -> record = related_to).
	if err := f.rels.Add(f.ctx, RelationEdge{
		Src: EntityRef{Type: schema.EntityNote, ID: page.ID},
		Dst: recordRef,
	}); err != nil {
		t.Fatalf("note->record: %v", err)
	}
	inboundRecord, err := f.rels.ListTo(f.ctx, recordRef, string(schema.RelationRelatedTo))
	if err != nil {
		t.Fatalf("list to record: %v", err)
	}
	if len(inboundRecord) != 1 || inboundRecord[0].Ref.ID != page.ID ||
		inboundRecord[0].Relation != schema.RelationRelatedTo {
		t.Fatalf("record inbound = %+v", inboundRecord)
	}

	// Remove uses the same normalization.
	if err := f.rels.Remove(f.ctx, RelationEdge{Src: recordRef, Dst: EntityRef{Type: schema.EntityPaper, ID: paperID}}); err != nil {
		t.Fatalf("remove record edge: %v", err)
	}
	from, err = f.rels.ListFrom(f.ctx, recordRef, "")
	if err != nil {
		t.Fatalf("list from after remove: %v", err)
	}
	if len(from) != 2 {
		t.Fatalf("outbound after remove = %+v", from)
	}
	for _, link := range from {
		if link.Ref.Type == schema.EntityPaper && link.Ref.ID == paperID {
			t.Fatalf("removed paper edge still listed: %+v", link)
		}
	}
	if from[0].Ref.ID != other.ID && from[1].Ref.ID != other.ID {
		t.Fatalf("record->note edge lost: %+v", from)
	}
	if from[0].Ref.ID != record.ID && from[1].Ref.ID != record.ID {
		t.Fatalf("note->record edge lost: %+v", from)
	}
}

// TestCoreRecordRelationErrors covers records that cannot own relations.
func TestCoreRecordRelationErrors(t *testing.T) {
	f := coreNewFixture(t)
	paperID := f.seedPaper("Paper")
	unknown := EntityRef{
		Type: schema.EntityDBRecord,
		ID:   "018f0000-0000-7000-8000-000000000000",
	}
	if _, err := f.rels.ListFrom(f.ctx, unknown, ""); err == nil {
		t.Fatal("unknown record accepted")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown record error = %v", err)
	}
	if err := f.rels.Add(f.ctx, RelationEdge{Src: unknown, Dst: EntityRef{Type: schema.EntityPaper, ID: paperID}}); err == nil {
		t.Fatal("unknown record accepted by Add")
	}

	// A record row without a backing note (note_id NULL) cannot own relations.
	dbID, _ := f.seedDatabase("No note")
	recordID := ids.MustNew(f.clock)
	now := clock.Format(f.clock.Now())
	if _, err := f.handle.DB().Exec(
		`INSERT INTO db_records(id, database_id, note_id, title, data, sort_order, created_at, updated_at)
		 VALUES (?, ?, NULL, '', '{}', 0, ?, ?)`, recordID, dbID, now, now); err != nil {
		t.Fatalf("insert record: %v", err)
	}
	noNote := EntityRef{Type: schema.EntityDBRecord, ID: recordID}
	_, err := f.rels.ListFrom(f.ctx, noNote, "")
	if err == nil || !strings.Contains(err.Error(), "no backing note") {
		t.Fatalf("record without note error = %v", err)
	}
	if err := f.rels.Add(f.ctx, RelationEdge{Src: noNote, Dst: EntityRef{Type: schema.EntityPaper, ID: paperID}}); err == nil {
		t.Fatal("record without note accepted by Add")
	}

	// A soft-deleted record is rejected with the deleted sentinel.
	dbID2, _ := f.seedDatabase("Deleted")
	page := f.create("Page", "body")
	rec, err := f.notes.PromoteToDatabase(f.ctx, PromoteInput{NoteID: page.ID, DatabaseID: dbID2})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if err := f.notes.DetachFromDatabase(f.ctx, rec.ID, false, ""); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if _, err := f.rels.ListFrom(f.ctx, EntityRef{Type: schema.EntityDBRecord, ID: rec.ID}, ""); !errors.Is(err, errs.ErrEntityDeleted) {
		t.Fatalf("deleted record error = %v", err)
	}
}
