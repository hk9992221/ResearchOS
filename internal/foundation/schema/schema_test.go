package schema

import (
	"slices"
	"testing"
)

func strings[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

func equalStrings[T ~string](t *testing.T, name string, got []T, want []string) {
	t.Helper()
	gotStrings := strings(got)
	if !slices.Equal(gotStrings, want) {
		t.Fatalf("%s = %v, want %v", name, gotStrings, want)
	}
}

func TestRegistriesMatchSpec(t *testing.T) {
	equalStrings(t, "EntityType", AllEntityTypes(), []string{
		"project", "paper", "note", "research_database", "db_field", "db_record",
		"idea", "evidence", "experiment", "run", "snapshot", "task",
		"weekly_report", "entity_relation",
	})
	equalStrings(t, "EventType", AllEventTypes(), []string{
		"entity.created", "entity.deleted", "entity.restored", "entity.updated",
		"entity.purged", "entity.relocated", "status.changed", "relation.added",
		"relation.removed", "note.updated", "ai.analysis_completed",
		"paper.imported", "paper.overwrite_confirmed", "paper.metadata_updated",
		"report.generated", "project.exported",
	})
	equalStrings(t, "Actor", AllActors(), []string{"user", "mcp", "ai"})
	equalStrings(t, "Relation", AllRelations(), []string{
		"references", "derived_from", "validates", "deliverable_of", "related_to",
	})
	equalStrings(t, "NoteKind", AllNoteKinds(), []string{
		"scratchpad", "paper_note", "db_record", "overview",
	})
	equalStrings(t, "IdeaStatus", AllIdeaStatuses(), []string{
		"New", "Researching", "Validating", "Supported",
		"PartiallySupported", "Inconclusive", "Rejected", "OnHold",
	})
	equalStrings(t, "ProjectStatus", AllProjectStatuses(), []string{"active", "archived"})
	equalStrings(t, "ReportStatus", AllReportStatuses(), []string{"draft", "confirmed"})
	equalStrings(t, "Direction", AllDirections(), []string{"support", "oppose"})
	equalStrings(t, "PaperFileState", AllPaperFileStates(), []string{"ok", "missing"})
	equalStrings(t, "PaperSourceState", AllPaperSourceStates(), []string{"ok", "missing", "unreadable"})
}

func TestRegistrySizes(t *testing.T) {
	if got := len(AllEntityTypes()); got != 14 {
		t.Errorf("entity types = %d, want 14", got)
	}
	if got := len(AllEventTypes()); got != 16 {
		t.Errorf("event types = %d, want 16", got)
	}
	if got := len(AllActors()); got != 3 {
		t.Errorf("actors = %d, want 3", got)
	}
	if got := len(AllRelations()); got != 5 {
		t.Errorf("relations = %d, want 5", got)
	}
	if got := len(AllIdeaStatuses()); got != 8 {
		t.Errorf("idea statuses = %d, want 8", got)
	}
	if got := len(AllPaperSourceStates()); got != 3 {
		t.Errorf("paper source states = %d, want 3", got)
	}
}

func TestValidators(t *testing.T) {
	if !EntityNote.Valid() || EntityType("bogus").Valid() {
		t.Error("EntityType.Valid is wrong")
	}
	if !EventNoteUpdated.Valid() || EventType("bogus").Valid() {
		t.Error("EventType.Valid is wrong")
	}
	if !ActorMCP.Valid() || Actor("robot").Valid() {
		t.Error("Actor.Valid is wrong")
	}
	if !RelationValidates.Valid() || Relation("includes").Valid() {
		t.Error("Relation.Valid is wrong")
	}
	if !IdeaPartiallySupported.Valid() || IdeaStatus("new").Valid() {
		t.Error("IdeaStatus.Valid is wrong")
	}
}

func TestRelationDirections(t *testing.T) {
	cases := []struct {
		src, dst EntityType
		rel      Relation
		want     bool
	}{
		{EntityNote, EntityNote, RelationReferences, true},
		{EntityNote, EntityPaper, RelationReferences, true},
		{EntityNote, EntityIdea, RelationReferences, false},
		{EntityIdea, EntityIdea, RelationDerivedFrom, true},
		{EntityIdea, EntityNote, RelationDerivedFrom, false},
		{EntityExperiment, EntityIdea, RelationValidates, true},
		{EntityExperiment, EntityNote, RelationValidates, false},
		{EntityTask, EntityNote, RelationDeliverableOf, true},
		{EntityTask, EntityIdea, RelationDeliverableOf, true},
		{EntityTask, EntityExperiment, RelationDeliverableOf, true},
		{EntityTask, EntityRun, RelationDeliverableOf, true},
		{EntityTask, EntityWeeklyReport, RelationDeliverableOf, true},
		{EntityTask, EntityPaper, RelationDeliverableOf, false},
		{EntityIdea, EntityTask, RelationRelatedTo, true},
		{EntityPaper, EntityNote, RelationRelatedTo, true},
		{EntityNote, EntityNote, Relation("includes"), false},
	}
	for _, c := range cases {
		if got := RelationAllowed(c.src, c.rel, c.dst); got != c.want {
			t.Errorf("RelationAllowed(%s, %s, %s) = %v, want %v", c.src, c.rel, c.dst, got, c.want)
		}
	}
	rule, ok := RelationRuleFor(RelationRelatedTo)
	if !ok || len(rule.Src) != 0 || len(rule.Dst) != 0 {
		t.Errorf("related_to rule = %+v, %v; want any/any", rule, ok)
	}
	if _, ok := RelationRuleFor(Relation("bogus")); ok {
		t.Error("RelationRuleFor(bogus) reported ok")
	}
}

func TestAllSlicesAreCopies(t *testing.T) {
	first := AllEntityTypes()
	first[0] = "mutated"
	if got := AllEntityTypes()[0]; got != EntityProject {
		t.Fatalf("AllEntityTypes leaked its backing array: %q", got)
	}
}
