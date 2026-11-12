// Package schema is the single source of truth for the entity, event, actor,
// relation and enum registries (SPEC-01 §4.6). Every DDL CHECK constraint must
// agree with these values; the golden schema test enforces it.
package schema

import "slices"

// EntityType enumerates the 14 foundation entity types.
type EntityType string

const (
	EntityProject          EntityType = "project"
	EntityPaper            EntityType = "paper"
	EntityNote             EntityType = "note"
	EntityResearchDatabase EntityType = "research_database"
	EntityDBField          EntityType = "db_field"
	EntityDBRecord         EntityType = "db_record"
	EntityIdea             EntityType = "idea"
	EntityEvidence         EntityType = "evidence"
	EntityExperiment       EntityType = "experiment"
	EntityRun              EntityType = "run"
	EntitySnapshot         EntityType = "snapshot"
	EntityTask             EntityType = "task"
	EntityWeeklyReport     EntityType = "weekly_report"
	EntityEntityRelation   EntityType = "entity_relation"
)

var entityTypes = []EntityType{
	EntityProject,
	EntityPaper,
	EntityNote,
	EntityResearchDatabase,
	EntityDBField,
	EntityDBRecord,
	EntityIdea,
	EntityEvidence,
	EntityExperiment,
	EntityRun,
	EntitySnapshot,
	EntityTask,
	EntityWeeklyReport,
	EntityEntityRelation,
}

// AllEntityTypes returns the registry in declaration order.
func AllEntityTypes() []EntityType { return slices.Clone(entityTypes) }

// Valid reports whether e is a registered entity type.
func (e EntityType) Valid() bool { return slices.Contains(entityTypes, e) }

// String implements fmt.Stringer.
func (e EntityType) String() string { return string(e) }

// EventType enumerates the 16 Activity event types (SPEC-00 §6 v1.1).
type EventType string

const (
	EventEntityCreated           EventType = "entity.created"
	EventEntityDeleted           EventType = "entity.deleted"
	EventEntityRestored          EventType = "entity.restored"
	EventEntityUpdated           EventType = "entity.updated"
	EventEntityPurged            EventType = "entity.purged"
	EventEntityRelocated         EventType = "entity.relocated"
	EventStatusChanged           EventType = "status.changed"
	EventRelationAdded           EventType = "relation.added"
	EventRelationRemoved         EventType = "relation.removed"
	EventNoteUpdated             EventType = "note.updated"
	EventAIAnalysisCompleted     EventType = "ai.analysis_completed"
	EventPaperImported           EventType = "paper.imported"
	EventPaperOverwriteConfirmed EventType = "paper.overwrite_confirmed"
	EventPaperMetadataUpdated    EventType = "paper.metadata_updated"
	EventReportGenerated         EventType = "report.generated"
	EventProjectExported         EventType = "project.exported"
)

var eventTypes = []EventType{
	EventEntityCreated,
	EventEntityDeleted,
	EventEntityRestored,
	EventEntityUpdated,
	EventEntityPurged,
	EventEntityRelocated,
	EventStatusChanged,
	EventRelationAdded,
	EventRelationRemoved,
	EventNoteUpdated,
	EventAIAnalysisCompleted,
	EventPaperImported,
	EventPaperOverwriteConfirmed,
	EventPaperMetadataUpdated,
	EventReportGenerated,
	EventProjectExported,
}

// AllEventTypes returns the registry in declaration order.
func AllEventTypes() []EventType { return slices.Clone(eventTypes) }

// Valid reports whether t is a registered event type.
func (t EventType) Valid() bool { return slices.Contains(eventTypes, t) }

// String implements fmt.Stringer.
func (t EventType) String() string { return string(t) }

// Actor enumerates who produced an event.
type Actor string

const (
	ActorUser Actor = "user"
	ActorMCP  Actor = "mcp"
	ActorAI   Actor = "ai"
)

var actors = []Actor{ActorUser, ActorMCP, ActorAI}

// AllActors returns the registry in declaration order.
func AllActors() []Actor { return slices.Clone(actors) }

// Valid reports whether a is a registered actor.
func (a Actor) Valid() bool { return slices.Contains(actors, a) }

// String implements fmt.Stringer.
func (a Actor) String() string { return string(a) }

// Relation enumerates the five generic entity relation types (SPEC-00 §5).
type Relation string

const (
	RelationReferences    Relation = "references"
	RelationDerivedFrom   Relation = "derived_from"
	RelationValidates     Relation = "validates"
	RelationDeliverableOf Relation = "deliverable_of"
	RelationRelatedTo     Relation = "related_to"
)

var relations = []Relation{
	RelationReferences,
	RelationDerivedFrom,
	RelationValidates,
	RelationDeliverableOf,
	RelationRelatedTo,
}

// AllRelations returns the registry in declaration order.
func AllRelations() []Relation { return slices.Clone(relations) }

// Valid reports whether r is a registered relation type.
func (r Relation) Valid() bool { return slices.Contains(relations, r) }

// String implements fmt.Stringer.
func (r Relation) String() string { return string(r) }

// RelationRule restricts which entity types may sit on either end of a
// relation. Empty Src/Dst means "any entity type".
type RelationRule struct {
	Src []EntityType
	Dst []EntityType
}

// Allows reports whether src and dst are permitted by the rule. Self-relations
// (same type and same id) are the caller's responsibility.
func (r RelationRule) Allows(src, dst EntityType) bool {
	return (len(r.Src) == 0 || slices.Contains(r.Src, src)) &&
		(len(r.Dst) == 0 || slices.Contains(r.Dst, dst))
}

// relationRules is the direction table of SPEC-01 §4.6.
var relationRules = map[Relation]RelationRule{
	RelationReferences:    {Src: []EntityType{EntityNote}, Dst: []EntityType{EntityNote, EntityPaper}},
	RelationDerivedFrom:   {Src: []EntityType{EntityIdea}, Dst: []EntityType{EntityIdea}},
	RelationValidates:     {Src: []EntityType{EntityExperiment}, Dst: []EntityType{EntityIdea}},
	RelationDeliverableOf: {Src: []EntityType{EntityTask}, Dst: []EntityType{EntityNote, EntityIdea, EntityExperiment, EntityRun, EntityWeeklyReport}},
	RelationRelatedTo:     {},
}

// RelationRuleFor returns the direction rule for rel.
func RelationRuleFor(rel Relation) (RelationRule, bool) {
	rule, ok := relationRules[rel]
	return rule, ok
}

// RelationAllowed reports whether (src --rel--> dst) is a legal combination.
func RelationAllowed(src EntityType, rel Relation, dst EntityType) bool {
	rule, ok := relationRules[rel]
	return ok && rule.Allows(src, dst)
}

// NoteKind enumerates the four note kinds.
type NoteKind string

const (
	NoteScratchpad NoteKind = "scratchpad"
	NotePaperNote  NoteKind = "paper_note"
	NoteDBRecord   NoteKind = "db_record"
	NoteOverview   NoteKind = "overview"
)

var noteKinds = []NoteKind{NoteScratchpad, NotePaperNote, NoteDBRecord, NoteOverview}

// AllNoteKinds returns the registry in declaration order.
func AllNoteKinds() []NoteKind { return slices.Clone(noteKinds) }

// Valid reports whether k is a registered note kind.
func (k NoteKind) Valid() bool { return slices.Contains(noteKinds, k) }

// String implements fmt.Stringer.
func (k NoteKind) String() string { return string(k) }

// IdeaStatus enumerates the eight idea statuses.
type IdeaStatus string

const (
	IdeaNew                IdeaStatus = "New"
	IdeaResearching        IdeaStatus = "Researching"
	IdeaValidating         IdeaStatus = "Validating"
	IdeaSupported          IdeaStatus = "Supported"
	IdeaPartiallySupported IdeaStatus = "PartiallySupported"
	IdeaInconclusive       IdeaStatus = "Inconclusive"
	IdeaRejected           IdeaStatus = "Rejected"
	IdeaOnHold             IdeaStatus = "OnHold"
)

var ideaStatuses = []IdeaStatus{
	IdeaNew, IdeaResearching, IdeaValidating, IdeaSupported,
	IdeaPartiallySupported, IdeaInconclusive, IdeaRejected, IdeaOnHold,
}

// AllIdeaStatuses returns the registry in declaration order.
func AllIdeaStatuses() []IdeaStatus { return slices.Clone(ideaStatuses) }

// Valid reports whether s is a registered idea status.
func (s IdeaStatus) Valid() bool { return slices.Contains(ideaStatuses, s) }

// String implements fmt.Stringer.
func (s IdeaStatus) String() string { return string(s) }

// ProjectStatus enumerates project lifecycle states.
type ProjectStatus string

const (
	ProjectActive   ProjectStatus = "active"
	ProjectArchived ProjectStatus = "archived"
)

var projectStatuses = []ProjectStatus{ProjectActive, ProjectArchived}

// AllProjectStatuses returns the registry in declaration order.
func AllProjectStatuses() []ProjectStatus { return slices.Clone(projectStatuses) }

// Valid reports whether s is a registered project status.
func (s ProjectStatus) Valid() bool { return slices.Contains(projectStatuses, s) }

// String implements fmt.Stringer.
func (s ProjectStatus) String() string { return string(s) }

// ReportStatus enumerates weekly report lifecycle states.
type ReportStatus string

const (
	ReportDraft     ReportStatus = "draft"
	ReportConfirmed ReportStatus = "confirmed"
)

var reportStatuses = []ReportStatus{ReportDraft, ReportConfirmed}

// AllReportStatuses returns the registry in declaration order.
func AllReportStatuses() []ReportStatus { return slices.Clone(reportStatuses) }

// Valid reports whether s is a registered report status.
func (s ReportStatus) Valid() bool { return slices.Contains(reportStatuses, s) }

// String implements fmt.Stringer.
func (s ReportStatus) String() string { return string(s) }

// Direction enumerates evidence directions.
type Direction string

const (
	DirectionSupport Direction = "support"
	DirectionOppose  Direction = "oppose"
)

var directions = []Direction{DirectionSupport, DirectionOppose}

// AllDirections returns the registry in declaration order.
func AllDirections() []Direction { return slices.Clone(directions) }

// Valid reports whether d is a registered direction.
func (d Direction) Valid() bool { return slices.Contains(directions, d) }

// String implements fmt.Stringer.
func (d Direction) String() string { return string(d) }

// PaperFileState enumerates the state of the local PDF copy.
type PaperFileState string

const (
	PaperFileOK      PaperFileState = "ok"
	PaperFileMissing PaperFileState = "missing"
)

var paperFileStates = []PaperFileState{PaperFileOK, PaperFileMissing}

// AllPaperFileStates returns the registry in declaration order.
func AllPaperFileStates() []PaperFileState { return slices.Clone(paperFileStates) }

// Valid reports whether s is a registered paper file state.
func (s PaperFileState) Valid() bool { return slices.Contains(paperFileStates, s) }

// String implements fmt.Stringer.
func (s PaperFileState) String() string { return string(s) }

// PaperSourceState enumerates the state of the Zotero attachment behind a paper.
type PaperSourceState string

const (
	PaperSourceOK         PaperSourceState = "ok"
	PaperSourceMissing    PaperSourceState = "missing"
	PaperSourceUnreadable PaperSourceState = "unreadable"
)

var paperSourceStates = []PaperSourceState{PaperSourceOK, PaperSourceMissing, PaperSourceUnreadable}

// AllPaperSourceStates returns the registry in declaration order.
func AllPaperSourceStates() []PaperSourceState { return slices.Clone(paperSourceStates) }

// Valid reports whether s is a registered paper source state.
func (s PaperSourceState) Valid() bool { return slices.Contains(paperSourceStates, s) }

// String implements fmt.Stringer.
func (s PaperSourceState) String() string { return string(s) }
