package ideas

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
	"researchos/internal/ideas/migrations"
)

type fixture struct {
	t   *testing.T
	fx  *foundationtest.Fixture
	svc Service
	ctx context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	fx, err := foundationtest.NewWithProject(t.TempDir(), foundationtest.Options{
		Migrations: migrations.MustAll(),
	})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	t.Cleanup(func() { _ = fx.Close() })
	return &fixture{t: t, fx: fx, svc: New(fx.Workspace, fx.ProjectID, Options{Clock: fx.Clock}), ctx: context.Background()}
}

func (f *fixture) createIdea(title, desc string) *Idea {
	f.t.Helper()
	idea, err := f.svc.CreateIdea(f.ctx, CreateIdeaInput{Title: title, DescriptionMD: desc, Actor: schema.ActorUser})
	if err != nil {
		f.t.Fatalf("CreateIdea: %v", err)
	}
	return idea
}

func (f *fixture) addEvidence(ideaID string, dir Direction, content string, ref SourceRef) *Evidence {
	f.t.Helper()
	ev, err := f.svc.AddEvidence(f.ctx, AddEvidenceInput{
		IdeaID: ideaID, Direction: dir, ContentMD: content, Source: ref, Actor: schema.ActorUser,
	})
	if err != nil {
		f.t.Fatalf("AddEvidence: %v", err)
	}
	return ev
}

// note inserts one live note and returns its id.
func (f *fixture) note(title string) string {
	f.t.Helper()
	id := ids.MustNew(f.fx.Clock)
	now := clock.Format(f.fx.Clock.Now())
	if _, err := f.fx.Project.DB().Exec(
		`INSERT INTO notes(id, title, file_path, kind, created_at, updated_at)
		 VALUES (?, ?, ?, 'scratchpad', ?, ?)`,
		id, title, "projects/"+f.fx.ProjectID+"/notes/"+id+".md", now, now); err != nil {
		f.t.Fatalf("insert note: %v", err)
	}
	return id
}

func (f *fixture) experiment(title string) string {
	f.t.Helper()
	id := ids.MustNew(f.fx.Clock)
	now := clock.Format(f.fx.Clock.Now())
	if _, err := f.fx.Project.DB().Exec(
		`INSERT INTO experiments(id, title, purpose, created_at, updated_at) VALUES (?, ?, '', ?, ?)`,
		id, title, now, now); err != nil {
		f.t.Fatalf("insert experiment: %v", err)
	}
	return id
}

func (f *fixture) run(experimentID, status string) string {
	f.t.Helper()
	id := ids.MustNew(f.fx.Clock)
	now := clock.Format(f.fx.Clock.Now())
	if _, err := f.fx.Project.DB().Exec(
		`INSERT INTO runs(id, experiment_id, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		id, experimentID, status, now, now); err != nil {
		f.t.Fatalf("insert run: %v", err)
	}
	return id
}

func (f *fixture) activities(entityType schema.EntityType, entityID string) []events.Activity {
	f.t.Helper()
	rows, err := f.fx.Project.Reader().List(f.ctx, events.Query{
		EntityTypes: []schema.EntityType{entityType}, EntityID: entityID, Limit: 500,
	})
	if err != nil {
		f.t.Fatalf("activities: %v", err)
	}
	return rows
}

func (f *fixture) countActivities(entityType schema.EntityType, entityID, eventType string) int {
	n := 0
	for _, a := range f.activities(entityType, entityID) {
		if string(a.Type) == eventType {
			n++
		}
	}
	return n
}

func TestCreateUpdateAndIdempotentStatus(t *testing.T) {
	f := newFixture(t)
	idea := f.createIdea("  收敛性假设  ", "初始描述")
	if idea.Status != StatusNew || idea.Title != "收敛性假设" {
		t.Fatalf("idea = %+v", idea)
	}
	if got := f.countActivities(schema.EntityIdea, idea.ID, "entity.created"); got != 1 {
		t.Fatalf("entity.created = %d, want 1", got)
	}

	newTitle := "收敛性假设 v2"
	desc := "更新后的描述"
	f.fx.Clock.Advance(time.Millisecond)
	updated, err := f.svc.UpdateIdea(f.ctx, UpdateIdeaInput{ID: idea.ID, Title: &newTitle, DescriptionMD: &desc})
	if err != nil {
		t.Fatalf("UpdateIdea: %v", err)
	}
	if updated.Title != newTitle || updated.DescriptionMD != desc || !updated.UpdatedAt.After(idea.UpdatedAt) {
		t.Fatalf("updated = %+v", updated)
	}
	if got := len(f.activities(schema.EntityIdea, idea.ID)); got != 1 {
		t.Fatalf("activities after edit = %d, want 1 (no event)", got)
	}

	// Repeating the current status is idempotent: no event, updated_at unchanged.
	before := updated.UpdatedAt
	again, err := f.svc.ChangeStatus(f.ctx, ChangeStatusInput{ID: idea.ID, To: StatusNew, Actor: schema.ActorUser})
	if err != nil {
		t.Fatalf("ChangeStatus same: %v", err)
	}
	if !again.UpdatedAt.Equal(before) || f.countActivities(schema.EntityIdea, idea.ID, "status.changed") != 0 {
		t.Fatalf("idempotent status change wrote state")
	}
}

func TestStatusMachineRejectionReason(t *testing.T) {
	f := newFixture(t)
	idea := f.createIdea("待验证", "")
	steps := []Status{StatusResearching, StatusValidating, StatusSupported}
	for _, st := range steps {
		f.fx.Clock.Advance(time.Millisecond)
		if _, err := f.svc.ChangeStatus(f.ctx, ChangeStatusInput{ID: idea.ID, To: st, Actor: schema.ActorUser}); err != nil {
			t.Fatalf("ChangeStatus(%s): %v", st, err)
		}
	}
	acts := f.activities(schema.EntityIdea, idea.ID)
	var changes []events.Activity
	for _, a := range acts {
		if a.Type == schema.EventStatusChanged {
			changes = append(changes, a)
		}
	}
	if len(changes) != 3 {
		t.Fatalf("status.changed = %d, want 3", len(changes))
	}
	wantNew := []string{string(StatusResearching), string(StatusValidating), string(StatusSupported)}
	for i := len(changes) - 1; i >= 0; i-- {
		if got := (*changes[i].Payload.New)["status"]; got != wantNew[len(changes)-1-i] {
			t.Fatalf("event %d new = %v, want %s", i, got, wantNew[len(changes)-1-i])
		}
	}

	// Rejected without a reason is refused and leaves no trace.
	before := len(f.activities(schema.EntityIdea, idea.ID))
	_, err := f.svc.ChangeStatus(f.ctx, ChangeStatusInput{ID: idea.ID, To: StatusRejected, Actor: schema.ActorUser})
	if !IsCode(err, ErrRejectionReasonRequired) {
		t.Fatalf("ChangeStatus rejected empty reason = %v", err)
	}
	if len(f.activities(schema.EntityIdea, idea.ID)) != before {
		t.Fatalf("failed status change wrote an event")
	}

	f.fx.Clock.Advance(time.Millisecond)
	rejected, err := f.svc.ChangeStatus(f.ctx, ChangeStatusInput{
		ID: idea.ID, To: StatusRejected, RejectionReason: "反例构造成功", Actor: schema.ActorUser,
	})
	if err != nil || rejected.Status != StatusRejected || rejected.RejectionReason != "反例构造成功" {
		t.Fatalf("reject = %+v, %v", rejected, err)
	}
	var summary string
	for _, a := range f.activities(schema.EntityIdea, idea.ID) {
		if a.Type == schema.EventStatusChanged && (*a.Payload.New)["status"] == string(StatusRejected) {
			summary = a.Payload.Summary
		}
	}
	if !strings.Contains(summary, "反例构造成功") {
		t.Fatalf("rejection summary = %q", summary)
	}

	f.fx.Clock.Advance(time.Millisecond)
	back, err := f.svc.ChangeStatus(f.ctx, ChangeStatusInput{ID: idea.ID, To: StatusResearching, Actor: schema.ActorUser})
	if err != nil {
		t.Fatalf("leave rejected: %v", err)
	}
	if back.RejectionReason != "反例构造成功" {
		t.Fatalf("rejection reason lost: %q", back.RejectionReason)
	}
}

func TestDeleteRestoreKeepsEvidenceAndRelations(t *testing.T) {
	f := newFixture(t)
	noteID := f.note("理论笔记")
	idea := f.createIdea("删除恢复", "")
	if err := f.svc.LinkEntity(f.ctx, LinkInput{
		IdeaID: idea.ID, TargetType: TargetNote, TargetID: noteID, Actor: schema.ActorUser,
	}); err != nil {
		t.Fatalf("LinkEntity: %v", err)
	}
	f.addEvidence(idea.ID, DirectionSupport, "手工证据内容", SourceRef{Type: SourceNote, ID: noteID})

	if err := f.svc.DeleteIdea(f.ctx, idea.ID, schema.ActorUser); err != nil {
		t.Fatalf("DeleteIdea: %v", err)
	}
	page, err := f.svc.ListIdeas(f.ctx, ListIdeasQuery{})
	if err != nil || page.Total != 0 {
		t.Fatalf("live list after delete = %+v, %v", page, err)
	}
	trash, err := f.svc.ListIdeas(f.ctx, ListIdeasQuery{IncludeDeleted: true})
	if err != nil || trash.Total != 1 || trash.Items[0].DeletedAt == nil {
		t.Fatalf("trash list = %+v, %v", trash, err)
	}
	var evidenceCount int
	if err := f.fx.Project.DB().QueryRow(`SELECT count(*) FROM evidence WHERE idea_id = ?`, idea.ID).Scan(&evidenceCount); err != nil {
		t.Fatalf("count evidence: %v", err)
	}
	if evidenceCount != 1 {
		t.Fatalf("evidence rows = %d, want 1 (no physical delete)", evidenceCount)
	}
	if err := f.svc.RestoreIdea(f.ctx, idea.ID, schema.ActorUser); err != nil {
		t.Fatalf("RestoreIdea: %v", err)
	}
	detail, err := f.svc.GetIdea(f.ctx, idea.ID)
	if err != nil {
		t.Fatalf("GetIdea: %v", err)
	}
	if detail.DeletedAt != nil || len(detail.Relations.Notes) != 1 || detail.Evidence.Support != 1 {
		t.Fatalf("restored detail = %+v", detail)
	}
}

func TestArchivedProjectIsReadOnly(t *testing.T) {
	f := newFixture(t)
	idea := f.createIdea("归档只读", "")
	if _, err := f.fx.Global().DB().Exec(`UPDATE projects SET status = 'archived' WHERE id = ?`, f.fx.ProjectID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	_, err := f.svc.ChangeStatus(f.ctx, ChangeStatusInput{ID: idea.ID, To: StatusResearching, Actor: schema.ActorUser})
	if !IsCode(err, ErrProjectReadOnly) {
		t.Fatalf("ChangeStatus archived = %v, want project.read_only", err)
	}
	if _, err := f.svc.GetIdea(f.ctx, idea.ID); err != nil {
		t.Fatalf("GetIdea archived should work: %v", err)
	}
}

func TestRelationsAndDerivation(t *testing.T) {
	f := newFixture(t)
	noteID := f.note("笔记")
	expID := f.experiment("实验")
	paperID, err := f.fx.SeedPaper(f.ctx, "zotero-key-1")
	if err != nil {
		t.Fatalf("SeedPaper: %v", err)
	}
	if err := f.fx.LinkProjectPaper(f.ctx, paperID); err != nil {
		t.Fatalf("LinkProjectPaper: %v", err)
	}
	idea := f.createIdea("关系", "")
	for _, target := range []struct{ typ, id string }{
		{TargetPaper, paperID}, {TargetNote, noteID}, {TargetExperiment, expID},
	} {
		if err := f.svc.LinkEntity(f.ctx, LinkInput{
			IdeaID: idea.ID, TargetType: target.typ, TargetID: target.id, Actor: schema.ActorUser,
		}); err != nil {
			t.Fatalf("LinkEntity(%s): %v", target.typ, err)
		}
		// Duplicate link is idempotent.
		if err := f.svc.LinkEntity(f.ctx, LinkInput{
			IdeaID: idea.ID, TargetType: target.typ, TargetID: target.id, Actor: schema.ActorUser,
		}); err != nil {
			t.Fatalf("duplicate LinkEntity: %v", err)
		}
	}
	if got := f.countActivities(schema.EntityIdea, idea.ID, "relation.added"); got != 3 {
		t.Fatalf("relation.added = %d, want 3", got)
	}
	set, err := f.svc.ListRelations(f.ctx, idea.ID)
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if len(set.Papers) != 1 || len(set.Notes) != 1 || len(set.Experiments) != 1 {
		t.Fatalf("relation set = %+v", set)
	}
	if err := f.svc.UnlinkEntity(f.ctx, LinkInput{
		IdeaID: idea.ID, TargetType: TargetNote, TargetID: noteID, Actor: schema.ActorUser,
	}); err != nil {
		t.Fatalf("UnlinkEntity: %v", err)
	}
	if got := f.countActivities(schema.EntityIdea, idea.ID, "relation.removed"); got != 1 {
		t.Fatalf("relation.removed = %d, want 1", got)
	}

	f.fx.Clock.Advance(time.Millisecond)
	source, err := f.svc.GetIdea(f.ctx, idea.ID)
	if err != nil {
		t.Fatalf("GetIdea: %v", err)
	}
	child, err := f.svc.DeriveIdea(f.ctx, DeriveIdeaInput{
		SourceIdeaID: idea.ID, Title: "衍生想法", Actor: schema.ActorUser,
	})
	if err != nil {
		t.Fatalf("DeriveIdea: %v", err)
	}
	after, err := f.svc.GetIdea(f.ctx, idea.ID)
	if err != nil {
		t.Fatalf("GetIdea after derive: %v", err)
	}
	if !after.UpdatedAt.Equal(source.UpdatedAt) {
		t.Fatalf("source updated_at changed on derive")
	}
	childDetail, err := f.svc.GetIdea(f.ctx, child.ID)
	if err != nil {
		t.Fatalf("child detail: %v", err)
	}
	if len(childDetail.Relations.DerivedFrom) != 1 || childDetail.Relations.DerivedFrom[0].ID != idea.ID {
		t.Fatalf("child derived_from = %+v", childDetail.Relations.DerivedFrom)
	}
	hist, err := f.svc.History(f.ctx, HistoryQuery{IdeaID: idea.ID})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	found := false
	for _, e := range hist.Items {
		if e.Synthesized && e.Kind == HistoryDerived && strings.Contains(e.Summary, "衍生想法") {
			found = true
		}
	}
	if !found {
		t.Fatalf("synthesized derived entry missing: %+v", hist.Items)
	}

	// Deleting the source marks the child's relation target as deleted.
	if err := f.svc.DeleteIdea(f.ctx, idea.ID, schema.ActorUser); err != nil {
		t.Fatalf("DeleteIdea source: %v", err)
	}
	childDetail, err = f.svc.GetIdea(f.ctx, child.ID)
	if err != nil {
		t.Fatalf("child detail after source delete: %v", err)
	}
	if !childDetail.Relations.DerivedFrom[0].Deleted {
		t.Fatalf("source idea not marked deleted: %+v", childDetail.Relations.DerivedFrom)
	}
	if err := f.svc.RestoreIdea(f.ctx, idea.ID, schema.ActorUser); err != nil {
		t.Fatalf("RestoreIdea: %v", err)
	}
}

func TestEvidenceLifecycle(t *testing.T) {
	f := newFixture(t)
	noteID := f.note("证据笔记")
	idea := f.createIdea("证据", "")
	ref := SourceRef{Type: SourceNote, ID: noteID}
	ev := f.addEvidence(idea.ID, DirectionSupport, "手工添加的支持证据内容", ref)
	if ev.AIGenerated || !ev.Confirmed {
		t.Fatalf("manual evidence flags = %+v", ev)
	}
	if got := f.countActivities(schema.EntityEvidence, ev.ID, "entity.created"); got != 1 {
		t.Fatalf("evidence entity.created = %d, want 1", got)
	}
	if _, err := f.svc.AddEvidence(f.ctx, AddEvidenceInput{
		IdeaID: idea.ID, Direction: DirectionOppose, ContentMD: "反对证据内容",
		Source: ref, Actor: schema.ActorUser,
	}); err != nil {
		t.Fatalf("AddEvidence oppose: %v", err)
	}
	detail, err := f.svc.GetIdea(f.ctx, idea.ID)
	if err != nil {
		t.Fatalf("GetIdea: %v", err)
	}
	if detail.Evidence.Support != 1 || detail.Evidence.Oppose != 1 {
		t.Fatalf("counts = %+v", detail.Evidence)
	}

	// Direction of confirmed evidence is locked; content edits do not emit.
	opp := DirectionOppose
	if _, err := f.svc.UpdateEvidence(f.ctx, UpdateEvidenceInput{
		EvidenceID: ev.ID, Direction: &opp, Actor: schema.ActorUser,
	}); !IsCode(err, ErrEvidenceDirectionLocked) {
		t.Fatalf("locked direction = %v", err)
	}
	edited := "手工证据内容（订正）"
	if _, err := f.svc.UpdateEvidence(f.ctx, UpdateEvidenceInput{
		EvidenceID: ev.ID, ContentMD: &edited, Actor: schema.ActorUser,
	}); err != nil {
		t.Fatalf("UpdateEvidence content: %v", err)
	}
	if got := len(f.activities(schema.EntityEvidence, ev.ID)); got != 1 {
		t.Fatalf("content edit wrote an event: %d", got)
	}

	// Source deleted: the evidence survives and is flagged.
	if _, err := f.fx.Project.DB().Exec(
		`UPDATE notes SET deleted_at = ? WHERE id = ?`, clock.Format(f.fx.Clock.Now()), noteID); err != nil {
		t.Fatalf("delete note: %v", err)
	}
	views, err := f.svc.ListEvidence(f.ctx, ListEvidenceQuery{IdeaID: idea.ID})
	if err != nil {
		t.Fatalf("ListEvidence: %v", err)
	}
	if len(views.Items) != 2 || !views.Items[0].SourceDeleted {
		t.Fatalf("views after note delete = %+v", views.Items)
	}

	// Delete and restore.
	if err := f.svc.DeleteEvidence(f.ctx, ev.ID, schema.ActorUser); err != nil {
		t.Fatalf("DeleteEvidence: %v", err)
	}
	if err := f.svc.RestoreEvidence(f.ctx, ev.ID, schema.ActorUser); err != nil {
		t.Fatalf("RestoreEvidence: %v", err)
	}
}

func TestEvidenceUnderDeletedIdea(t *testing.T) {
	f := newFixture(t)
	noteID := f.note("笔记")
	idea := f.createIdea("删除中的证据", "")
	ev := f.addEvidence(idea.ID, DirectionSupport, "证据内容", SourceRef{Type: SourceNote, ID: noteID})
	if err := f.svc.DeleteIdea(f.ctx, idea.ID, schema.ActorUser); err != nil {
		t.Fatalf("DeleteIdea: %v", err)
	}
	content := "修改"
	if _, err := f.svc.UpdateEvidence(f.ctx, UpdateEvidenceInput{
		EvidenceID: ev.ID, ContentMD: &content, Actor: schema.ActorUser,
	}); !IsCode(err, ErrIdeaDeleted) {
		t.Fatalf("edit under deleted idea = %v", err)
	}
	if _, err := f.svc.ConfirmEvidence(f.ctx, ConfirmEvidenceInput{
		IdeaID: idea.ID, EvidenceIDs: []string{ev.ID}, Actor: schema.ActorUser,
	}); !IsCode(err, ErrIdeaDeleted) {
		t.Fatalf("confirm under deleted idea = %v", err)
	}
}

func TestActorAIRejected(t *testing.T) {
	f := newFixture(t)
	idea := f.createIdea("actor", "")
	if _, err := f.svc.ChangeStatus(f.ctx, ChangeStatusInput{ID: idea.ID, To: StatusResearching, Actor: schema.ActorAI}); !IsCode(err, ErrInvalidActor) {
		t.Fatalf("ChangeStatus ai = %v", err)
	}
	if _, err := f.svc.DeriveIdea(f.ctx, DeriveIdeaInput{SourceIdeaID: idea.ID, Title: "x", Actor: schema.ActorAI}); !IsCode(err, ErrInvalidActor) {
		t.Fatalf("DeriveIdea ai = %v", err)
	}
	if _, err := f.svc.ConfirmEvidence(f.ctx, ConfirmEvidenceInput{IdeaID: idea.ID, Actor: schema.ActorAI}); !IsCode(err, ErrInvalidActor) {
		t.Fatalf("ConfirmEvidence ai = %v", err)
	}
}

type fakeAI struct {
	responses []string
	err       error
	calls     int
	last      StructuredRequest
}

func (f *fakeAI) CompleteStructured(_ context.Context, req StructuredRequest) (StructuredResponse, error) {
	f.last = req
	if f.err != nil {
		return StructuredResponse{}, f.err
	}
	idx := f.calls
	if idx >= len(f.responses) {
		idx = len(f.responses) - 1
	}
	f.calls++
	return StructuredResponse{Content: f.responses[idx], Provider: "fake-provider", Model: "fake-model"}, nil
}

type fakeSources struct {
	data map[string]string
	err  error
}

func (f fakeSources) Resolve(_ context.Context, _ string, ref SourceRef) (ResolvedSource, error) {
	if f.err != nil {
		return ResolvedSource{}, f.err
	}
	text, ok := f.data[string(ref.Type)+":"+ref.ID]
	if !ok {
		return ResolvedSource{}, errors.New("missing")
	}
	return ResolvedSource{Ref: ref, Title: "source " + string(ref.Type), Text: text, Version: "v1"}, nil
}

const aiValidOutput = `{"items":[
 {"direction":"support","content":"在三种随机种子下收敛步数较基线下降百分之十八，与加速假设一致。","source_index":1,"rationale":"Run 收敛曲线段落"},
 {"direction":"oppose","content":"大学习率下出现震荡，与稳定收敛假设不一致，需要额外条件。","source_index":2,"rationale":"笔记稳定性段落"}
]}`

func newAIFixture(t *testing.T, ai ChatClient, sources SourceResolver) (*fixture, *Idea, []SourceRef) {
	f := newFixture(t)
	f.svc = New(f.fx.Workspace, f.fx.ProjectID, Options{Clock: f.fx.Clock, AI: ai, Sources: sources})
	noteID := f.note("note-source")
	expID := f.experiment("exp-source")
	runID := f.run(expID, "failed")
	idea := f.createIdea("AI 草稿", "研究假设描述")
	return f, idea, []SourceRef{{Type: SourceRun, ID: runID}, {Type: SourceNote, ID: noteID}}
}

func TestGenerateEvidenceNoProvider(t *testing.T) {
	f := newFixture(t)
	noteID := f.note("note")
	idea := f.createIdea("no provider", "")
	_, err := f.svc.GenerateEvidenceDrafts(f.ctx, GenerateEvidenceInput{
		IdeaID: idea.ID, Sources: []SourceRef{{Type: SourceNote, ID: noteID}}, Actor: schema.ActorUser,
	})
	if !IsCode(err, ErrAIDisabled) {
		t.Fatalf("GenerateEvidenceDrafts without provider = %v", err)
	}
	// Manual evidence still works.
	f.addEvidence(idea.ID, DirectionSupport, "手工证据仍然可用", SourceRef{Type: SourceNote, ID: noteID})
}

func TestGenerateEvidenceDraftsAndDedupe(t *testing.T) {
	ai := &fakeAI{responses: []string{aiValidOutput}}
	sources := fakeSources{data: map[string]string{}}
	f, idea, refs := newAIFixture(t, ai, sources)
	sources.data[string(refs[0].Type)+":"+refs[0].ID] = "run analysis text"
	sources.data[string(refs[1].Type)+":"+refs[1].ID] = "note markdown text"
	f.svc = New(f.fx.Workspace, f.fx.ProjectID, Options{Clock: f.fx.Clock, AI: ai, Sources: sources})

	result, err := f.svc.GenerateEvidenceDrafts(f.ctx, GenerateEvidenceInput{
		IdeaID: idea.ID, Sources: refs, Actor: schema.ActorUser,
	})
	if err != nil {
		t.Fatalf("GenerateEvidenceDrafts: %v", err)
	}
	if len(result.Drafts) != 2 || len(result.Skipped) != 0 {
		t.Fatalf("result = %+v", result)
	}
	for _, d := range result.Drafts {
		if !d.AIGenerated || d.Confirmed || d.Provenance == nil {
			t.Fatalf("draft = %+v", d)
		}
		if d.Provenance.PromptVersion != "evidence.normalize/1.0" || d.Provenance.ProviderID != "fake-provider" {
			t.Fatalf("provenance = %+v", d.Provenance)
		}
	}
	if got := f.countActivities(schema.EntityIdea, idea.ID, "ai.analysis_completed"); got != 1 {
		t.Fatalf("ai.analysis_completed = %d, want 1", got)
	}
	if got := f.countActivities(schema.EntityEvidence, result.Drafts[0].ID, "entity.created"); got != 0 {
		t.Fatalf("draft wrote entity.created")
	}

	// A second run skips the same contents as duplicate_existing.
	second, err := f.svc.GenerateEvidenceDrafts(f.ctx, GenerateEvidenceInput{
		IdeaID: idea.ID, Sources: refs, Actor: schema.ActorUser,
	})
	if err != nil {
		t.Fatalf("second GenerateEvidenceDrafts: %v", err)
	}
	if len(second.Drafts) != 0 || len(second.Skipped) != 2 {
		t.Fatalf("second result = %+v", second)
	}
	for _, s := range second.Skipped {
		if s.Reason != "duplicate_existing" {
			t.Fatalf("skip reason = %s", s.Reason)
		}
	}

	// Confirm one draft, then edit-then-confirm another (AC-27).
	confirm, err := f.svc.ConfirmEvidence(f.ctx, ConfirmEvidenceInput{
		IdeaID: idea.ID, EvidenceIDs: []string{result.Drafts[0].ID}, Actor: schema.ActorUser,
	})
	if err != nil || len(confirm.Confirmed) != 1 {
		t.Fatalf("ConfirmEvidence = %+v, %v", confirm, err)
	}
	dir := DirectionSupport
	if _, err := f.svc.UpdateEvidence(f.ctx, UpdateEvidenceInput{
		EvidenceID: result.Drafts[0].ID, Direction: &dir, Actor: schema.ActorUser,
	}); !IsCode(err, ErrEvidenceDirectionLocked) {
		t.Fatalf("confirmed draft direction = %v", err)
	}
	if got := f.countActivities(schema.EntityEvidence, result.Drafts[0].ID, "status.changed"); got != 1 {
		t.Fatalf("confirm event = %d, want 1", got)
	}

	newContent := "人工编辑后的证据文本，保留原始 rationale 作为 provenance 依据。"
	if _, err := f.svc.UpdateEvidence(f.ctx, UpdateEvidenceInput{
		EvidenceID: result.Drafts[1].ID, ContentMD: &newContent, Actor: schema.ActorUser,
	}); err != nil {
		t.Fatalf("edit draft: %v", err)
	}
	if _, err := f.svc.ConfirmEvidence(f.ctx, ConfirmEvidenceInput{
		IdeaID: idea.ID, EvidenceIDs: []string{result.Drafts[1].ID}, Actor: schema.ActorUser,
	}); err != nil {
		t.Fatalf("confirm edited draft: %v", err)
	}
	stored, err := f.svc.ListEvidence(f.ctx, ListEvidenceQuery{IdeaID: idea.ID})
	if err != nil {
		t.Fatalf("ListEvidence: %v", err)
	}
	foundEdited := false
	for _, v := range stored.Items {
		if v.ID == result.Drafts[1].ID {
			foundEdited = v.ContentMD == newContent && v.Provenance != nil && v.Provenance.Rationale != ""
		}
	}
	if !foundEdited {
		t.Fatalf("edited draft not stored with provenance")
	}
	detail, err := f.svc.GetIdea(f.ctx, idea.ID)
	if err != nil {
		t.Fatalf("GetIdea: %v", err)
	}
	if detail.Evidence.Support+detail.Evidence.Oppose != 2 {
		t.Fatalf("confirmed counts = %+v", detail.Evidence)
	}
}

func TestGenerateEvidenceInvalidAndFailures(t *testing.T) {
	invalid := `{"items":[{"direction":"maybe","content":"短","source_index":9,"rationale":""}]}`
	ai := &fakeAI{responses: []string{invalid}}
	sources := fakeSources{data: map[string]string{}}
	f, idea, refs := newAIFixture(t, ai, sources)
	sources.data[string(refs[0].Type)+":"+refs[0].ID] = "text"
	sources.data[string(refs[1].Type)+":"+refs[1].ID] = "text"
	f.svc = New(f.fx.Workspace, f.fx.ProjectID, Options{Clock: f.fx.Clock, AI: ai, Sources: sources})
	if _, err := f.svc.GenerateEvidenceDrafts(f.ctx, GenerateEvidenceInput{
		IdeaID: idea.ID, Sources: refs, Actor: schema.ActorUser,
	}); !IsCode(err, ErrAIOutputInvalid) {
		t.Fatalf("all-invalid output = %v", err)
	}

	// Partial invalid output keeps the valid item and skips the rest.
	mixed := `{"items":[
	 {"direction":"support","content":"这条证据完整且足够长，可以正常落入草稿并等待人工确认。","source_index":1,"rationale":"ok"},
	 {"direction":"sideways","content":"非法方向","source_index":1,"rationale":""},
	 {"direction":"oppose","content":"这条证据的来源序号越界因此必须被服务端丢弃，而不会被写入数据库。","source_index":7,"rationale":""}
	]}`
	ai2 := &fakeAI{responses: []string{mixed}}
	f.svc = New(f.fx.Workspace, f.fx.ProjectID, Options{Clock: f.fx.Clock, AI: ai2, Sources: sources})
	result, err := f.svc.GenerateEvidenceDrafts(f.ctx, GenerateEvidenceInput{
		IdeaID: idea.ID, Sources: refs, Actor: schema.ActorUser,
	})
	if err != nil {
		t.Fatalf("mixed output: %v", err)
	}
	if len(result.Drafts) != 1 || len(result.Skipped) != 2 {
		t.Fatalf("mixed result = %+v", result)
	}

	// Unparseable twice: no writes, no ai event.
	ai3 := &fakeAI{responses: []string{"not json", "still not json"}}
	f3, idea3, refs3 := newAIFixture(t, ai3, sources)
	sources.data[string(refs3[0].Type)+":"+refs3[0].ID] = "run text"
	sources.data[string(refs3[1].Type)+":"+refs3[1].ID] = "note text"
	f3.svc = New(f3.fx.Workspace, f3.fx.ProjectID, Options{Clock: f3.fx.Clock, AI: ai3, Sources: sources})
	if _, err := f3.svc.GenerateEvidenceDrafts(f3.ctx, GenerateEvidenceInput{
		IdeaID: idea3.ID, Sources: refs3, Actor: schema.ActorUser,
	}); !IsCode(err, ErrAIOutputInvalid) {
		t.Fatalf("unparseable output = %v", err)
	}
	if ai3.calls != 2 {
		t.Fatalf("repair calls = %d, want 2", ai3.calls)
	}
	var count int
	if err := f3.fx.Project.DB().QueryRow(`SELECT count(*) FROM evidence WHERE idea_id = ?`, idea3.ID).Scan(&count); err != nil {
		t.Fatalf("count evidence: %v", err)
	}
	if count != 0 || f3.countActivities(schema.EntityIdea, idea3.ID, "ai.analysis_completed") != 0 {
		t.Fatalf("failed run wrote data")
	}

	// Source resolution failure aborts the whole run.
	f4, idea4, refs4 := newAIFixture(t, &fakeAI{responses: []string{aiValidOutput}}, fakeSources{err: errors.New("gone")})
	if _, err := f4.svc.GenerateEvidenceDrafts(f4.ctx, GenerateEvidenceInput{
		IdeaID: idea4.ID, Sources: refs4, Actor: schema.ActorUser,
	}); !IsCode(err, ErrSourceUnavailable) {
		t.Fatalf("source failure = %v", err)
	}
}

func TestHistoryContainsLifecycle(t *testing.T) {
	f := newFixture(t)
	noteID := f.note("hist")
	idea := f.createIdea("历史", "")
	f.fx.Clock.Advance(time.Millisecond)
	if _, err := f.svc.ChangeStatus(f.ctx, ChangeStatusInput{ID: idea.ID, To: StatusResearching, Actor: schema.ActorUser}); err != nil {
		t.Fatalf("status: %v", err)
	}
	f.fx.Clock.Advance(time.Millisecond)
	if err := f.svc.LinkEntity(f.ctx, LinkInput{
		IdeaID: idea.ID, TargetType: TargetNote, TargetID: noteID, Actor: schema.ActorUser,
	}); err != nil {
		t.Fatalf("link: %v", err)
	}
	f.fx.Clock.Advance(time.Millisecond)
	f.addEvidence(idea.ID, DirectionSupport, "证据", SourceRef{Type: SourceNote, ID: noteID})
	f.fx.Clock.Advance(time.Millisecond)
	if err := f.svc.DeleteIdea(f.ctx, idea.ID, schema.ActorUser); err != nil {
		t.Fatalf("delete: %v", err)
	}
	f.fx.Clock.Advance(time.Millisecond)
	if err := f.svc.RestoreIdea(f.ctx, idea.ID, schema.ActorUser); err != nil {
		t.Fatalf("restore: %v", err)
	}
	page, err := f.svc.History(f.ctx, HistoryQuery{IdeaID: idea.ID, PageSize: 3})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if page.Total != 6 || len(page.Items) != 3 || page.NextCursor == "" {
		t.Fatalf("history page = %+v", page)
	}
	next, err := f.svc.History(f.ctx, HistoryQuery{IdeaID: idea.ID, PageSize: 3, Cursor: page.NextCursor})
	if err != nil {
		t.Fatalf("History next: %v", err)
	}
	if len(next.Items) != 3 {
		t.Fatalf("history next = %+v", next)
	}
	seen := map[string]bool{}
	for _, e := range append(page.Items, next.Items...) {
		seen[e.EventType] = true
	}
	for _, want := range []string{"entity.created", "status.changed", "relation.added", "entity.deleted", "entity.restored"} {
		if !seen[want] {
			t.Fatalf("history missing %s: %+v", want, seen)
		}
	}
	// Filter works.
	only, err := f.svc.History(f.ctx, HistoryQuery{IdeaID: idea.ID, Kinds: []HistoryKind{HistoryRelation}})
	if err != nil || only.Total != 1 {
		t.Fatalf("filtered history = %+v, %v", only, err)
	}
}

func TestListAndCursor(t *testing.T) {
	f := newFixture(t)
	for i := range 5 {
		f.createIdea("idea-"+string(rune('a'+i)), "")
		f.fx.Clock.Advance(time.Millisecond)
	}
	first, err := f.svc.ListIdeas(f.ctx, ListIdeasQuery{PageSize: 2})
	if err != nil {
		t.Fatalf("ListIdeas: %v", err)
	}
	if first.Total != 5 || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	seen := map[string]bool{}
	cursor := first.NextCursor
	for _, item := range first.Items {
		seen[item.ID] = true
	}
	for cursor != "" {
		page, err := f.svc.ListIdeas(f.ctx, ListIdeasQuery{PageSize: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("ListIdeas cursor: %v", err)
		}
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatalf("duplicate item %s", item.ID)
			}
			seen[item.ID] = true
		}
		cursor = page.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("seen = %d, want 5", len(seen))
	}

	sorted, err := f.svc.ListIdeas(f.ctx, ListIdeasQuery{Sort: SortTitleAsc, PageSize: 10})
	if err != nil {
		t.Fatalf("title sort: %v", err)
	}
	for i := 1; i < len(sorted.Items); i++ {
		if sorted.Items[i-1].Title > sorted.Items[i].Title {
			t.Fatalf("title sort broken: %v", sorted.Items)
		}
	}
}

func TestEventPayloadStatusShape(t *testing.T) {
	f := newFixture(t)
	idea := f.createIdea("payload", "")
	if _, err := f.svc.ChangeStatus(f.ctx, ChangeStatusInput{ID: idea.ID, To: StatusValidating, Actor: schema.ActorMCP}); err != nil {
		t.Fatalf("ChangeStatus: %v", err)
	}
	for _, a := range f.activities(schema.EntityIdea, idea.ID) {
		if a.Type != schema.EventStatusChanged {
			continue
		}
		if a.Actor != schema.ActorMCP {
			t.Fatalf("actor = %s", a.Actor)
		}
		raw, _ := json.Marshal(a.Payload)
		if !strings.Contains(string(raw), `"status":"New"`) || !strings.Contains(string(raw), `"status":"Validating"`) {
			t.Fatalf("payload = %s", raw)
		}
	}
}

func TestMigrationAppliesToExistingProject(t *testing.T) {
	f := newFixture(t)
	var columns int
	if err := f.fx.Project.DB().QueryRow(
		`SELECT count(*) FROM pragma_table_info('evidence') WHERE name = 'content_hash'`).Scan(&columns); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	if columns != 1 {
		t.Fatalf("evidence.content_hash missing")
	}
	var indexes int
	if err := f.fx.Project.DB().QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_evidence_dedupe'`).Scan(&indexes); err != nil {
		t.Fatalf("index query: %v", err)
	}
	if indexes != 1 {
		t.Fatalf("idx_evidence_dedupe missing")
	}
}

func TestEventFailureRollsBackBusinessWrite(t *testing.T) {
	f := newFixture(t)
	if _, err := f.fx.Project.DB().Exec(`ALTER TABLE activities RENAME TO activities_backup`); err != nil {
		t.Fatalf("rename activities: %v", err)
	}
	_, err := f.svc.CreateIdea(f.ctx, CreateIdeaInput{Title: "回滚", Actor: schema.ActorUser})
	if err == nil {
		t.Fatalf("CreateIdea succeeded without an activities table")
	}
	var count int
	if err := f.fx.Project.DB().QueryRow(`SELECT count(*) FROM ideas`).Scan(&count); err != nil {
		t.Fatalf("count ideas: %v", err)
	}
	if count != 0 {
		t.Fatalf("idea row survived a failed event write (AC-33)")
	}
}

func TestDiscardDraftKeepsItADraftAfterRestore(t *testing.T) {
	f := newFixture(t)
	noteID := f.note("draft note")
	idea := f.createIdea("draft", "")
	draftID := ids.MustNew(f.fx.Clock)
	now := clock.Format(f.fx.Clock.Now())
	if _, err := f.fx.Project.DB().Exec(
		`INSERT INTO evidence(id, idea_id, direction, content, source_type, source_id,
		   ai_generated, confirmed, content_hash, provenance, created_at, updated_at)
		 VALUES (?, ?, 'support', '待确认草稿内容', 'note', ?, 1, 0, '', '{}', ?, ?)`,
		draftID, idea.ID, noteID, now, now); err != nil {
		t.Fatalf("insert draft: %v", err)
	}
	result, err := f.svc.DiscardEvidence(f.ctx, DiscardEvidenceInput{
		IdeaID: idea.ID, EvidenceIDs: []string{draftID}, Actor: schema.ActorUser,
	})
	if err != nil || len(result.Discarded) != 1 {
		t.Fatalf("DiscardEvidence = %+v, %v", result, err)
	}
	trash, err := f.svc.ListEvidence(f.ctx, ListEvidenceQuery{IdeaID: idea.ID, IncludeDeleted: true})
	if err != nil || len(trash.Items) != 1 {
		t.Fatalf("trash evidence = %+v, %v", trash, err)
	}
	if err := f.svc.RestoreEvidence(f.ctx, draftID, schema.ActorUser); err != nil {
		t.Fatalf("RestoreEvidence: %v", err)
	}
	live, err := f.svc.ListEvidence(f.ctx, ListEvidenceQuery{IdeaID: idea.ID, Confirmed: boolPtr(false)})
	if err != nil || len(live.Items) != 1 || live.Items[0].Confirmed {
		t.Fatalf("restored draft = %+v, %v", live, err)
	}
	detail, err := f.svc.GetIdea(f.ctx, idea.ID)
	if err != nil {
		t.Fatalf("GetIdea: %v", err)
	}
	if detail.Evidence.Drafts != 1 || detail.Evidence.Support != 0 {
		t.Fatalf("counts after discard/restore = %+v", detail.Evidence)
	}

	// A confirmed evidence cannot be discarded, only deleted.
	confirmed := f.addEvidence(idea.ID, DirectionOppose, "已确认证据内容", SourceRef{Type: SourceNote, ID: noteID})
	refused, err := f.svc.DiscardEvidence(f.ctx, DiscardEvidenceInput{
		IdeaID: idea.ID, EvidenceIDs: []string{confirmed.ID}, Actor: schema.ActorUser,
	})
	if err != nil || len(refused.Failed) != 1 || refused.Failed[0].Code != ErrEvidenceConfirmed {
		t.Fatalf("discard confirmed = %+v, %v", refused, err)
	}
}

func boolPtr(v bool) *bool { return &v }
