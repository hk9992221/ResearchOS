package library

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
	"time"

	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
	"researchos/internal/literature"
)

func newService(f *foundationtest.Fixture) *Service {
	return New(f.Workspace, Options{Clock: f.Clock})
}

func paperIDs(t *testing.T, page literature.PaperPage) []string {
	t.Helper()
	out := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		out = append(out, item.ID)
	}
	return out
}

func projectPaperIDs(t *testing.T, page literature.ProjectPaperPage) []string {
	t.Helper()
	out := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		out = append(out, item.Paper.ID)
	}
	return out
}

func assertIDs(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
}

func seedQueryPapers(t *testing.T, f *foundationtest.Fixture) (p1, p2, p3 string) {
	t.Helper()
	p1 = seedPaper(t, f, paperSeed{
		Key:         "KEY1",
		Title:       "Alpha Study",
		Year:        2020,
		DOI:         "10.1000/XYZ",
		Abstract:    "deep learning methods",
		Tags:        []string{"Anomaly", "Physics"},
		Collections: []string{"COLL1"},
		Creators: []literature.ZoteroCreator{
			{FirstName: "Ada", LastName: "Lovelace", CreatorType: "author"},
		},
		Copy: true,
	})
	f.Clock.Advance(time.Second)
	p2 = seedPaper(t, f, paperSeed{
		Key:         "KEY2",
		Title:       "beta survey",
		Year:        2019,
		DOI:         "10.2000/abc",
		Tags:        []string{"physics"},
		Collections: []string{"COLL2"},
		Creators: []literature.ZoteroCreator{
			{FirstName: "Alan", LastName: "Turing", CreatorType: "author"},
		},
		Copy: true,
	})
	f.Clock.Advance(time.Second)
	p3 = seedPaper(t, f, paperSeed{
		Key:         "KEY3",
		Title:       "深度学习综述",
		Year:        2021,
		Tags:        []string{"中文"},
		Collections: []string{"COLL1"},
		Creators: []literature.ZoteroCreator{
			{Name: "王小明", CreatorType: "author"},
		},
		SourceState: schema.PaperSourceMissing,
	})
	return p1, p2, p3
}

func TestPageBounds(t *testing.T) {
	cases := []struct {
		name                 string
		total, offset, limit int
		wantStart, wantEnd   int
	}{
		{"default limit", 5, 0, 0, 0, 5},
		{"cap at 500", 600, 0, 1000, 0, 500},
		{"negative offset", 5, -3, 2, 0, 2},
		{"offset beyond total", 5, 10, 2, 5, 5},
		{"exact window", 5, 2, 2, 2, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end := pageBounds(tc.total, tc.offset, tc.limit)
			if start != tc.wantStart || end != tc.wantEnd {
				t.Errorf("pageBounds(%d,%d,%d) = %d,%d want %d,%d",
					tc.total, tc.offset, tc.limit, start, end, tc.wantStart, tc.wantEnd)
			}
		})
	}
}

func TestListPapersFilters(t *testing.T) {
	f := newFixture(t)
	service := newService(f)
	p1, p2, p3 := seedQueryPapers(t, f)
	ctx := context.Background()

	cases := []struct {
		name      string
		filter    literature.PaperFilter
		want      []string
		wantTotal int
	}{
		{"title case insensitive", literature.PaperFilter{Query: "ALPHA"}, []string{p1}, 1},
		{"author", literature.PaperFilter{Query: "lovelace"}, []string{p1}, 1},
		{"doi", literature.PaperFilter{Query: "10.1000/xyz"}, []string{p1}, 1},
		{"chinese title", literature.PaperFilter{Query: "深度"}, []string{p3}, 1},
		{"abstract", literature.PaperFilter{Query: "deep learning"}, []string{p1}, 1},
		{"tag", literature.PaperFilter{Query: "anomaly"}, []string{p1}, 1},
		{"no match", literature.PaperFilter{Query: "quantum"}, nil, 0},
		{"year from", literature.PaperFilter{YearFrom: 2020, Sort: literature.SortYear}, []string{p1, p3}, 2},
		{"year to", literature.PaperFilter{YearTo: 2019, Sort: literature.SortYear}, []string{p2}, 1},
		{"year range", literature.PaperFilter{YearFrom: 2020, YearTo: 2020, Sort: literature.SortYear}, []string{p1}, 1},
		{"collection", literature.PaperFilter{CollectionKeys: []string{"COLL1"}, Sort: literature.SortTitle}, []string{p1, p3}, 2},
		{"tag exact case insensitive", literature.PaperFilter{Tags: []string{"PHYSICS"}, Sort: literature.SortTitle}, []string{p1, p2}, 2},
		{"tags are all required", literature.PaperFilter{Tags: []string{"physics", "anomaly"}, Sort: literature.SortTitle}, []string{p1}, 1},
		{"source state missing", literature.PaperFilter{SourceState: []string{"missing"}, Sort: literature.SortTitle}, []string{p3}, 1},
		{"source state ok", literature.PaperFilter{SourceState: []string{"ok"}, Sort: literature.SortTitle}, []string{p1, p2}, 2},
		{"combined", literature.PaperFilter{Query: "study", YearFrom: 2020, Sort: literature.SortTitle}, []string{p1}, 1},
		{"combined empty", literature.PaperFilter{Query: "study", YearFrom: 2021}, nil, 0},
		{"title sort", literature.PaperFilter{Sort: literature.SortTitle}, []string{p1, p2, p3}, 3},
		{"title sort desc", literature.PaperFilter{Sort: literature.SortTitle, Desc: true}, []string{p3, p2, p1}, 3},
		{"year sort desc", literature.PaperFilter{Sort: literature.SortYear, Desc: true}, []string{p3, p1, p2}, 3},
		{"default sort is imported_at", literature.PaperFilter{}, []string{p1, p2, p3}, 3},
		{"imported desc", literature.PaperFilter{Sort: literature.SortImportedAt, Desc: true}, []string{p3, p2, p1}, 3},
		{"pagination", literature.PaperFilter{Sort: literature.SortTitle, Offset: 1, Limit: 2}, []string{p2, p3}, 3},
		{"offset beyond total", literature.PaperFilter{Sort: literature.SortTitle, Offset: 10}, nil, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := service.ListPapers(ctx, tc.filter)
			if err != nil {
				t.Fatalf("list papers: %v", err)
			}
			assertIDs(t, paperIDs(t, page), tc.want)
			if page.Total != tc.wantTotal {
				t.Errorf("total = %d, want %d", page.Total, tc.wantTotal)
			}
		})
	}
}

func TestListPapersPaginationTotalAndUnion(t *testing.T) {
	f := newFixture(t)
	service := newService(f)
	ctx := context.Background()
	var seeded []string
	for i, title := range []string{"alpha", "bravo", "charlie", "delta", "echo"} {
		seeded = append(seeded, seedPaper(t, f, paperSeed{Key: "K" + title, Title: title, Year: 2000 + i, Copy: true}))
		f.Clock.Advance(time.Second)
	}

	page, err := service.ListPapers(ctx, literature.PaperFilter{Sort: literature.SortTitle, Offset: 1, Limit: 2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Total != 5 {
		t.Errorf("total = %d, want 5", page.Total)
	}
	assertIDs(t, paperIDs(t, page), []string{seeded[1], seeded[2]})

	seen := make([]string, 0, 5)
	for offset := 0; offset < 5; offset += 2 {
		part, err := service.ListPapers(ctx, literature.PaperFilter{Sort: literature.SortTitle, Offset: offset, Limit: 2})
		if err != nil {
			t.Fatalf("list offset %d: %v", offset, err)
		}
		seen = append(seen, paperIDs(t, part)...)
	}
	full, err := service.ListPapers(ctx, literature.PaperFilter{Sort: literature.SortTitle, Limit: 1000})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	assertIDs(t, seen, paperIDs(t, full))
}

func TestListPapersUpdatedAtSort(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first := seedPaper(t, f, paperSeed{Key: "A", Title: "first", Copy: true})
	second := seedPaper(t, f, paperSeed{Key: "B", Title: "second", Copy: true})
	f.Clock.Advance(time.Minute)
	if err := f.Global().InTx(ctx, func(tx *sql.Tx) error {
		return f.Papers().UpdateFile(ctx, tx, first,
			f.Workspace.Paths().PaperPDF(first), "hash-A", 6, schema.PaperFileOK)
	}); err != nil {
		t.Fatalf("touch paper: %v", err)
	}
	service := newService(f)
	page, err := service.ListPapers(ctx, literature.PaperFilter{Sort: literature.SortUpdatedAt, Desc: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	assertIDs(t, paperIDs(t, page), []string{first, second})
}

func TestGetPaper(t *testing.T) {
	f := newFixture(t)
	service := newService(f)
	paperID := seedPaper(t, f, paperSeed{
		Key:         "GET1",
		Title:       "Metadata Title",
		Year:        2018,
		DOI:         "10.1/get",
		Tags:        []string{"tag-a"},
		Collections: []string{"COLL"},
		Creators: []literature.ZoteroCreator{
			{FirstName: "Grace", LastName: "Hopper", CreatorType: "author"},
			{FirstName: "Edsger", LastName: "Dijkstra", CreatorType: "editor"},
		},
		Copy: true,
	})
	paper, err := service.GetPaper(context.Background(), paperID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if paper.ID != paperID || paper.ZoteroKey != "GET1" {
		t.Errorf("identity = %s/%s", paper.ID, paper.ZoteroKey)
	}
	if paper.Metadata.Title != "Metadata Title" || paper.Metadata.Year != 2018 {
		t.Errorf("metadata = %+v", paper.Metadata)
	}
	if paper.Metadata.Authors != "Grace Hopper" {
		t.Errorf("authors = %q, want %q", paper.Metadata.Authors, "Grace Hopper")
	}
	if paper.FileHash != "hash-GET1" || paper.FilePath == "" {
		t.Errorf("file fields = %q/%q", paper.FilePath, paper.FileHash)
	}
	if paper.ImportedAt.IsZero() || paper.UpdatedAt.IsZero() {
		t.Error("timestamps are zero")
	}

	if _, err := service.GetPaper(context.Background(), ids.MustNew(f.Clock)); !errors.Is(err, literature.ErrPaperNotFound) {
		t.Errorf("unknown paper error = %v, want ErrPaperNotFound", err)
	}
}

func TestQueryProjectPapersOnlyLinked(t *testing.T) {
	f := newFixture(t)
	service := newService(f)
	ctx := context.Background()
	otherProject, otherHandle, err := f.NewProject(ctx, "Zulu", "", "")
	if err != nil {
		t.Fatalf("new project: %v", err)
	}
	p1 := seedPaper(t, f, paperSeed{Key: "P1", Title: "Alpha", Year: 2020, Copy: true})
	p2 := seedPaper(t, f, paperSeed{Key: "P2", Title: "Beta", Year: 2021, Copy: true})
	p3 := seedPaper(t, f, paperSeed{Key: "P3", Title: "Gamma", Year: 2022, Copy: true})
	linkPaper(t, f, f.Project, p1)
	linkPaper(t, f, f.Project, p2)
	linkPaper(t, f, otherHandle, p3)

	page, err := service.QueryProjectPapers(ctx, f.ProjectID, literature.PaperFilter{Sort: literature.SortTitle})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	assertIDs(t, projectPaperIDs(t, page), []string{p1, p2})
	if page.Total != 2 {
		t.Errorf("total = %d, want 2", page.Total)
	}
	for _, item := range page.Items {
		if item.LinkedAt.IsZero() {
			t.Error("linked_at is zero")
		}
		if item.Broken {
			t.Errorf("paper %s reported broken", item.Paper.ID)
		}
		if item.NoteID != "" {
			t.Errorf("note id = %q, want empty", item.NoteID)
		}
	}

	filtered, err := service.QueryProjectPapers(ctx, f.ProjectID, literature.PaperFilter{Query: "beta"})
	if err != nil {
		t.Fatalf("filtered query: %v", err)
	}
	assertIDs(t, projectPaperIDs(t, filtered), []string{p2})

	otherPage, err := service.QueryProjectPapers(ctx, otherProject, literature.PaperFilter{})
	if err != nil {
		t.Fatalf("other project: %v", err)
	}
	assertIDs(t, projectPaperIDs(t, otherPage), []string{p3})

	if _, err := service.QueryProjectPapers(ctx, ids.MustNew(f.Clock), literature.PaperFilter{}); err == nil {
		t.Fatal("unknown project was accepted")
	} else if errors.Is(err, literature.ErrPaperNotFound) {
		t.Errorf("unknown project error = %v, must not be ErrPaperNotFound", err)
	}
}

func TestQueryProjectPapersBrokenCopy(t *testing.T) {
	f := newFixture(t)
	service := newService(f)
	ctx := context.Background()
	okPaper := seedPaper(t, f, paperSeed{Key: "OK", Title: "present", Copy: true})
	brokenPaper := seedPaper(t, f, paperSeed{Key: "MISSING", Title: "absent"})
	linkPaper(t, f, f.Project, okPaper)
	linkPaper(t, f, f.Project, brokenPaper)

	page, err := service.QueryProjectPapers(ctx, f.ProjectID, literature.PaperFilter{Sort: literature.SortTitle})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(page.Items))
	}
	broken := map[string]bool{}
	for _, item := range page.Items {
		broken[item.Paper.ID] = item.Broken
	}
	if broken[okPaper] {
		t.Error("paper with copy reported broken")
	}
	if !broken[brokenPaper] {
		t.Error("paper without copy reported healthy")
	}
}

func TestQueryProjectPapersHasNote(t *testing.T) {
	f := newFixture(t)
	service := newService(f)
	ctx := context.Background()
	p1 := seedPaper(t, f, paperSeed{Key: "N1", Title: "Alpha", Copy: true})
	p2 := seedPaper(t, f, paperSeed{Key: "N2", Title: "Beta", Copy: true})
	p3 := seedPaper(t, f, paperSeed{Key: "N3", Title: "Gamma", Copy: true})
	linkPaper(t, f, f.Project, p1)
	linkPaper(t, f, f.Project, p2)
	linkPaper(t, f, f.Project, p3)

	liveNote := seedNote(t, f, f.Project, schema.NotePaperNote)
	linkNoteToPaper(t, f, f.Project, liveNote, p1)

	scratch := seedNote(t, f, f.Project, schema.NoteScratchpad)
	linkNoteToPaper(t, f, f.Project, scratch, p2)

	deletedNote := seedNote(t, f, f.Project, schema.NotePaperNote)
	linkNoteToPaper(t, f, f.Project, deletedNote, p3)
	softDeleteNote(t, f, f.Project, deletedNote)

	hasNote := true
	withNotes, err := service.QueryProjectPapers(ctx, f.ProjectID,
		literature.PaperFilter{HasNote: &hasNote, Sort: literature.SortTitle})
	if err != nil {
		t.Fatalf("has note query: %v", err)
	}
	assertIDs(t, projectPaperIDs(t, withNotes), []string{p1})
	if len(withNotes.Items) != 1 || withNotes.Items[0].NoteID != liveNote {
		t.Errorf("note id = %+v, want %s", withNotes.Items, liveNote)
	}

	noNote := false
	withoutNotes, err := service.QueryProjectPapers(ctx, f.ProjectID,
		literature.PaperFilter{HasNote: &noNote, Sort: literature.SortTitle})
	if err != nil {
		t.Fatalf("without note query: %v", err)
	}
	assertIDs(t, projectPaperIDs(t, withoutNotes), []string{p2, p3})
	for _, item := range withoutNotes.Items {
		if item.NoteID != "" {
			t.Errorf("paper %s note id = %q, want empty", item.Paper.ID, item.NoteID)
		}
	}
}

func TestQueryProjectPapersPagination(t *testing.T) {
	f := newFixture(t)
	service := newService(f)
	ctx := context.Background()
	p1 := seedPaper(t, f, paperSeed{Key: "PG1", Title: "alpha", Copy: true})
	p2 := seedPaper(t, f, paperSeed{Key: "PG2", Title: "bravo", Copy: true})
	linkPaper(t, f, f.Project, p1)
	linkPaper(t, f, f.Project, p2)

	page, err := service.QueryProjectPapers(ctx, f.ProjectID,
		literature.PaperFilter{Sort: literature.SortTitle, Offset: 1, Limit: 1})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	assertIDs(t, projectPaperIDs(t, page), []string{p2})
	if page.Total != 2 {
		t.Errorf("total = %d, want 2", page.Total)
	}
}

func TestListPaperProjects(t *testing.T) {
	f := newFixture(t)
	service := newService(f)
	ctx := context.Background()
	alphaID, alphaHandle, err := f.NewProject(ctx, "Alpha", "", "")
	if err != nil {
		t.Fatalf("new project: %v", err)
	}
	zuluID, zuluHandle, err := f.NewProject(ctx, "zulu", "", "")
	if err != nil {
		t.Fatalf("new project: %v", err)
	}
	archiveProject(t, f, alphaID)

	paperID := seedPaper(t, f, paperSeed{Key: "MULTI", Title: "multi", Copy: true})
	linkPaper(t, f, f.Project, paperID)
	linkPaper(t, f, alphaHandle, paperID)
	linkPaper(t, f, zuluHandle, paperID)

	refs, err := service.ListPaperProjects(ctx, paperID)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	wantNames := []string{"Alpha", "Fixture", "zulu"}
	wantStatus := []string{"archived", "active", "active"}
	wantIDs := []string{alphaID, f.ProjectID, zuluID}
	if len(refs) != 3 {
		t.Fatalf("refs = %+v, want 3", refs)
	}
	for i, ref := range refs {
		if ref.Name != wantNames[i] || ref.Status != wantStatus[i] || ref.ProjectID != wantIDs[i] {
			t.Errorf("ref[%d] = %+v, want name=%s status=%s id=%s", i, ref, wantNames[i], wantStatus[i], wantIDs[i])
		}
	}

	unlinked := seedPaper(t, f, paperSeed{Key: "UNLINKED", Title: "unlinked", Copy: true})
	empty, err := service.ListPaperProjects(ctx, unlinked)
	if err != nil {
		t.Fatalf("list unlinked: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("unlinked refs = %+v, want none", empty)
	}
}

func TestListProjectPaperIDsUnion(t *testing.T) {
	f := newFixture(t)
	service := newService(f)
	ctx := context.Background()
	linked := seedPaper(t, f, paperSeed{Key: "U1", Title: "linked", Copy: true})
	related := seedPaper(t, f, paperSeed{Key: "U2", Title: "related", Copy: true})
	evidenced := seedPaper(t, f, paperSeed{Key: "U3", Title: "evidenced", Copy: true})
	both := seedPaper(t, f, paperSeed{Key: "U4", Title: "both", Copy: true})
	ignored := seedPaper(t, f, paperSeed{Key: "U5", Title: "related only", Copy: true})

	linkPaper(t, f, f.Project, linked)
	linkPaper(t, f, f.Project, both)
	noteID := seedNote(t, f, f.Project, schema.NoteScratchpad)
	linkNoteToPaper(t, f, f.Project, noteID, related)
	linkNoteToPaper(t, f, f.Project, noteID, both)
	seedRawRelation(t, f, f.Project, schema.EntityNote, noteID, schema.RelationRelatedTo, schema.EntityPaper, ignored)
	seedEvidence(t, f, f.Project, evidenced, false)
	seedEvidence(t, f, f.Project, both, false)

	got, err := service.ListProjectPaperIDs(ctx, f.ProjectID)
	if err != nil {
		t.Fatalf("list ids: %v", err)
	}
	want := []string{linked, related, evidenced, both}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
}

func TestCountPaperRefs(t *testing.T) {
	f := newFixture(t)
	service := newService(f)
	ctx := context.Background()
	secondID, secondHandle, err := f.NewProject(ctx, "Second", "", "")
	if err != nil {
		t.Fatalf("new project: %v", err)
	}
	thirdID, thirdHandle, err := f.NewProject(ctx, "Third", "", "")
	if err != nil {
		t.Fatalf("new project: %v", err)
	}
	if thirdID == "" {
		t.Fatal("third project id is empty")
	}
	if _, _, err := f.NewProject(ctx, "Empty", "", ""); err != nil {
		t.Fatalf("new project: %v", err)
	}

	paperID := seedPaper(t, f, paperSeed{Key: "REF", Title: "referenced", Copy: true})
	linkPaper(t, f, f.Project, paperID)

	noteID := seedNote(t, f, secondHandle, schema.NotePaperNote)
	linkNoteToPaper(t, f, secondHandle, noteID, paperID)
	seedEvidence(t, f, thirdHandle, paperID, false)

	count, err := service.CountPaperRefs(ctx, paperID, "")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
	count, err = service.CountPaperRefs(ctx, paperID, f.ProjectID)
	if err != nil {
		t.Fatalf("count excluded: %v", err)
	}
	if count != 2 {
		t.Errorf("count excluding default project = %d, want 2", count)
	}
	count, err = service.CountPaperRefs(ctx, paperID, secondID)
	if err != nil {
		t.Fatalf("count excluded second: %v", err)
	}
	if count != 2 {
		t.Errorf("count excluding second = %d, want 2", count)
	}

	deletedEvidencePaper := seedPaper(t, f, paperSeed{Key: "REF2", Title: "deleted evidence", Copy: true})
	seedEvidence(t, f, thirdHandle, deletedEvidencePaper, true)
	count, err = service.CountPaperRefs(ctx, deletedEvidencePaper, "")
	if err != nil {
		t.Fatalf("count deleted evidence: %v", err)
	}
	if count != 0 {
		t.Errorf("deleted evidence counted: %d", count)
	}

	relatedOnly := seedPaper(t, f, paperSeed{Key: "REF3", Title: "related only", Copy: true})
	relatedNote := seedNote(t, f, secondHandle, schema.NoteScratchpad)
	seedRawRelation(t, f, secondHandle, schema.EntityNote, relatedNote, schema.RelationRelatedTo, schema.EntityPaper, relatedOnly)
	count, err = service.CountPaperRefs(ctx, relatedOnly, "")
	if err != nil {
		t.Fatalf("count related_to only: %v", err)
	}
	if count != 0 {
		t.Errorf("related_to was counted as a reference: %d", count)
	}
}

func TestListProjectPapers(t *testing.T) {
	f := newFixture(t)
	service := newService(f)
	ctx := context.Background()
	base := f.Clock.Now()
	p1 := seedPaper(t, f, paperSeed{Key: "L1", Title: "oldest", Year: 2001})
	p2 := seedPaper(t, f, paperSeed{Key: "L2", Title: "middle", Year: 2002, Copy: true, Creators: []literature.ZoteroCreator{{FirstName: "Ada", LastName: "Lovelace", CreatorType: "author"}}})
	p3 := seedPaper(t, f, paperSeed{Key: "L3", Title: "newest", Year: 2003, Copy: true})
	linkPaperAt(t, f, f.Project, p1, base)
	linkPaperAt(t, f, f.Project, p2, base.Add(time.Minute))
	linkPaperAt(t, f, f.Project, p3, base.Add(2*time.Minute))

	briefs, err := service.ListProjectPapers(ctx, f.ProjectID, 2)
	if err != nil {
		t.Fatalf("list briefs: %v", err)
	}
	if len(briefs) != 2 {
		t.Fatalf("briefs = %d, want 2", len(briefs))
	}
	if briefs[0].ID != p3 || briefs[1].ID != p2 {
		t.Errorf("order = %s,%s, want newest first", briefs[0].ID, briefs[1].ID)
	}
	if briefs[0].Title != "newest" || briefs[0].Year != 2003 || briefs[0].Missing {
		t.Errorf("brief[0] = %+v", briefs[0])
	}
	if briefs[1].Authors != "Ada Lovelace" {
		t.Errorf("authors = %q", briefs[1].Authors)
	}

	all, err := service.ListProjectPapers(ctx, f.ProjectID, 0)
	if err != nil {
		t.Fatalf("list all briefs: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all briefs = %d, want 3", len(all))
	}
	if all[2].ID != p1 || !all[2].Missing {
		t.Errorf("oldest brief = %+v, want missing copy", all[2])
	}

	if _, err := service.ListProjectPapers(ctx, ids.MustNew(f.Clock), 0); err == nil {
		t.Fatal("unknown project was accepted")
	}
}
