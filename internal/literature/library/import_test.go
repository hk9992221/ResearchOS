package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/foundationtest"
	"researchos/internal/foundation/schema"
	"researchos/internal/literature"
)

// importReader is a controllable literature.ZoteroReader for import tests. It
// serves real files from t.TempDir() through ResolveAttachment.
type importReader struct {
	collections        []literature.ZoteroCollection
	collectionItems    map[string][]literature.ZoteroItem
	items              map[string]literature.ZoteroItem
	attachments        map[string][]literature.ZoteroAttachment
	resolved           map[string]literature.ResolvedFile
	resolveErr         map[string]error
	attachmentsErr     map[string]error
	listItemsErr       error
	listCollectionsErr error
}

func newImportReader() *importReader {
	return &importReader{
		collectionItems: map[string][]literature.ZoteroItem{},
		items:           map[string]literature.ZoteroItem{},
		attachments:     map[string][]literature.ZoteroAttachment{},
		resolved:        map[string]literature.ResolvedFile{},
		resolveErr:      map[string]error{},
		attachmentsErr:  map[string]error{},
	}
}

func (r *importReader) Source() literature.MetadataSource { return literature.SourceSQLite }

func (r *importReader) ListLibraries(context.Context) ([]literature.ZoteroLibrary, error) {
	return nil, nil
}

func (r *importReader) ListCollections(context.Context, string) ([]literature.ZoteroCollection, error) {
	return r.collections, r.listCollectionsErr
}

func (r *importReader) ListItems(_ context.Context, _, collectionKey string, opts literature.ListItemsOptions) (literature.ItemPage, error) {
	if r.listItemsErr != nil {
		return literature.ItemPage{}, r.listItemsErr
	}
	all := r.collectionItems[collectionKey]
	start := opts.Offset
	if start < 0 {
		start = 0
	}
	if start > len(all) {
		start = len(all)
	}
	end := len(all)
	if opts.Limit > 0 && start+opts.Limit < end {
		end = start + opts.Limit
	}
	return literature.ItemPage{
		Items:      append([]literature.ZoteroItem(nil), all[start:end]...),
		Total:      len(all),
		SourceUsed: literature.SourceSQLite,
	}, nil
}

func (r *importReader) GetItem(_ context.Context, _, key string) (literature.ZoteroItem, error) {
	item, ok := r.items[key]
	if !ok {
		return literature.ZoteroItem{}, literature.ErrItemNotFound
	}
	return item, nil
}

func (r *importReader) ListAttachments(_ context.Context, _, parentKey string) ([]literature.ZoteroAttachment, error) {
	if err := r.attachmentsErr[parentKey]; err != nil {
		return nil, err
	}
	return r.attachments[parentKey], nil
}

func (r *importReader) ResolveAttachment(_ context.Context, att literature.ZoteroAttachment) (literature.ResolvedFile, error) {
	if err := r.resolveErr[att.Key]; err != nil {
		return literature.ResolvedFile{}, err
	}
	resolved, ok := r.resolved[att.Key]
	if !ok {
		return literature.ResolvedFile{}, literature.ErrAttachmentUnresolved
	}
	return resolved, nil
}

func importItem(key, title string, year int) literature.ZoteroItem {
	return literature.ZoteroItem{
		Key:       key,
		LibraryID: "0",
		ItemType:  "journalArticle",
		Title:     title,
		Date:      fmt.Sprintf("%d", year),
		Creators:  []literature.ZoteroCreator{{FirstName: "Ada", LastName: "Lovelace", CreatorType: "author"}},
	}
}

func (r *importReader) addItem(collectionKey string, item literature.ZoteroItem, attachments ...literature.ZoteroAttachment) {
	r.items[item.Key] = item
	if collectionKey != "" {
		r.collectionItems[collectionKey] = append(r.collectionItems[collectionKey], item)
	}
	r.attachments[item.Key] = attachments
}

// pdfAttachment writes a real PDF file and registers the attachment.
func pdfAttachment(t *testing.T, r *importReader, key, content string) literature.ZoteroAttachment {
	t.Helper()
	path := filepath.Join(t.TempDir(), key+".pdf")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	r.resolved[key] = literature.ResolvedFile{Path: path, Size: int64(len(content))}
	return literature.ZoteroAttachment{
		Key:         key,
		ContentType: "application/pdf",
		Filename:    key + ".pdf",
		LinkMode:    "imported_file",
		Size:        int64(len(content)),
	}
}

func importService(f *foundationtest.Fixture, reader literature.ZoteroReader) *Service {
	return newPDFService(f, fakeSource{reader: reader}, nil)
}

func paperDirs(t *testing.T, f *foundationtest.Fixture) []string {
	t.Helper()
	dir, err := f.Workspace.Paths().Abs("library/papers")
	if err != nil {
		t.Fatalf("resolve library dir: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read library dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func globalPaperCount(t *testing.T, f *foundationtest.Fixture) int {
	t.Helper()
	var n int
	if err := f.Global().DB().QueryRowContext(context.Background(), `SELECT count(*) FROM papers`).Scan(&n); err != nil {
		t.Fatalf("count papers: %v", err)
	}
	return n
}

func previewKeys(preview literature.ImportPreview) []string {
	out := make([]string, 0, len(preview.Entries))
	for _, entry := range preview.Entries {
		out = append(out, entry.ZoteroKey)
	}
	return out
}

func outcomeFor(t *testing.T, result literature.ImportResult, key string) literature.ImportOutcome {
	t.Helper()
	for _, outcome := range result.Outcomes {
		if outcome.ZoteroKey == key {
			return outcome
		}
	}
	t.Fatalf("no outcome for %s", key)
	return literature.ImportOutcome{}
}

func paperImportedEvents(t *testing.T, reader events.Reader) []events.Activity {
	t.Helper()
	list, err := reader.List(context.Background(), events.Query{
		EventTypes: []events.Type{schema.EventPaperImported},
		Limit:      500,
	})
	if err != nil {
		t.Fatalf("list paper.imported: %v", err)
	}
	return list
}

func TestImportSingleCreatesPaperCopyAndEvent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	reader := newImportReader()
	item := importItem("IT-1", "Paper One", 2024)
	reader.addItem("", item, pdfAttachment(t, reader, "ATT-1", "pdf-one"))
	service := importService(f, reader)

	var progress []literature.ImportProgress
	result, err := service.Import(ctx, literature.ImportSelection{
		ProjectID: f.ProjectID,
		ItemKeys:  []string{"IT-1"},
	}, literature.ActorUser, func(p literature.ImportProgress) { progress = append(progress, p) })
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Imported != 1 || result.Linked != 0 || result.Skipped != 0 || result.Failed != 0 {
		t.Fatalf("result counts = %+v", result)
	}
	outcome := outcomeFor(t, result, "IT-1")
	if outcome.Status != literature.ImportCreated || outcome.PaperID == "" {
		t.Fatalf("outcome = %+v", outcome)
	}

	paper, err := service.GetPaper(ctx, outcome.PaperID)
	if err != nil {
		t.Fatalf("get paper: %v", err)
	}
	if paper.Metadata.Title != "Paper One" || paper.Metadata.Year != 2024 {
		t.Errorf("metadata = %+v", paper.Metadata)
	}
	if paper.FileHash != sha256Hex([]byte("pdf-one")) {
		t.Errorf("hash = %s, want sha256 of the source", paper.FileHash)
	}
	if paper.FilePath != f.Workspace.Paths().PaperPDF(outcome.PaperID) {
		t.Errorf("file path = %q", paper.FilePath)
	}
	if _, info, err := NewFileStore(f.Workspace).Stat(ctx, outcome.PaperID); err != nil || info.Size() != int64(len("pdf-one")) {
		t.Errorf("stat = %v, %v", info, err)
	}
	if dirs := paperDirs(t, f); len(dirs) != 1 || dirs[0] != outcome.PaperID {
		t.Errorf("library dirs = %v", dirs)
	}
	if n := countActivities(t, f.Project, "paper.imported", outcome.PaperID); n != 1 {
		t.Errorf("paper.imported events = %d, want 1", n)
	}
	if n := countActivities(t, f.Project, "relation.added", ""); n != 0 {
		t.Errorf("relation.added events = %d, want 0 (import uses paper.imported)", n)
	}
	if len(progress) != 1 {
		t.Fatalf("progress = %+v", progress)
	}
	if progress[0].Index != 1 || progress[0].Total != 1 || progress[0].ItemKey != "IT-1" ||
		progress[0].Status != literature.ImportCreated || progress[0].Message == "" {
		t.Errorf("progress = %+v", progress[0])
	}
	list := paperImportedEvents(t, f.Project.Reader())
	if len(list) != 1 {
		t.Fatalf("paper.imported rows = %d", len(list))
	}
	if list[0].Payload.Summary != "导入文献《Paper One》" {
		t.Errorf("summary = %q", list[0].Payload.Summary)
	}
	if list[0].Payload.New == nil {
		t.Fatal("payload.new is nil")
	}
	newObj := *list[0].Payload.New
	if newObj["zotero_key"] != "IT-1" || newObj["reused"] != false || newObj["project_id"] != f.ProjectID {
		t.Errorf("payload.new = %+v", newObj)
	}
}

func TestImportSecondProjectReusesOneCopy(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	reader := newImportReader()
	item := importItem("IT-2", "Shared Paper", 2023)
	reader.addItem("", item, pdfAttachment(t, reader, "ATT-2", "shared pdf"))
	service := importService(f, reader)

	first, err := service.Import(ctx, literature.ImportSelection{ProjectID: f.ProjectID, ItemKeys: []string{"IT-2"}}, literature.ActorUser, nil)
	if err != nil || first.Imported != 1 {
		t.Fatalf("first import = %+v, %v", first, err)
	}
	secondProjectID, secondHandle, err := f.NewProject(ctx, "Second", "", "")
	if err != nil {
		t.Fatalf("new project: %v", err)
	}
	second, err := service.Import(ctx, literature.ImportSelection{ProjectID: secondProjectID, ItemKeys: []string{"IT-2"}}, literature.ActorMCP, nil)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if second.Imported != 0 || second.Linked != 1 || second.Skipped != 0 || second.Failed != 0 {
		t.Fatalf("second counts = %+v", second)
	}
	if second.Outcomes[0].Status != literature.ImportLinkedExisting {
		t.Errorf("second status = %s", second.Outcomes[0].Status)
	}
	if second.Outcomes[0].PaperID != first.Outcomes[0].PaperID {
		t.Errorf("paper ids differ: %s vs %s", second.Outcomes[0].PaperID, first.Outcomes[0].PaperID)
	}
	if n := globalPaperCount(t, f); n != 1 {
		t.Errorf("global papers = %d, want 1", n)
	}
	if dirs := paperDirs(t, f); len(dirs) != 1 {
		t.Errorf("copy dirs = %v, want 1", dirs)
	}
	if n := countActivities(t, secondHandle, "paper.imported", ""); n != 1 {
		t.Errorf("second project events = %d, want 1", n)
	}
	list := paperImportedEvents(t, secondHandle.Reader())
	if len(list) != 1 || list[0].Payload.New == nil || (*list[0].Payload.New)["reused"] != true {
		t.Errorf("second event payload = %+v", list)
	}
	if n := countActivities(t, f.Project, "paper.imported", ""); n != 1 {
		t.Errorf("first project events = %d, want 1", n)
	}
	if n := countActivities(t, secondHandle, "relation.added", ""); n != 0 {
		t.Errorf("relation.added in second project = %d, want 0", n)
	}
}

func TestImportDuplicateProjectIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	reader := newImportReader()
	item := importItem("IT-3", "Same Project", 2022)
	reader.addItem("", item, pdfAttachment(t, reader, "ATT-3", "same pdf"))
	service := importService(f, reader)
	sel := literature.ImportSelection{ProjectID: f.ProjectID, ItemKeys: []string{"IT-3"}}

	first, err := service.Import(ctx, sel, literature.ActorUser, nil)
	if err != nil || first.Imported != 1 {
		t.Fatalf("first import = %+v, %v", first, err)
	}
	second, err := service.Import(ctx, sel, literature.ActorUser, nil)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if second.Imported != 0 || second.Skipped != 1 || second.Linked != 0 {
		t.Fatalf("second counts = %+v", second)
	}
	if second.Outcomes[0].Status != literature.ImportSkippedDuplicate {
		t.Errorf("status = %s, want already_linked", second.Outcomes[0].Status)
	}
	if n := countActivities(t, f.Project, "paper.imported", ""); n != 1 {
		t.Errorf("paper.imported events = %d, want 1", n)
	}
	if n := globalPaperCount(t, f); n != 1 {
		t.Errorf("global papers = %d", n)
	}
	if dirs := paperDirs(t, f); len(dirs) != 1 {
		t.Errorf("copy dirs = %v", dirs)
	}
	if n := countActivities(t, f.Project, "relation.added", ""); n != 0 {
		t.Errorf("relation.added events = %d, want 0", n)
	}
}

func TestImportNoPDFSkipped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	reader := newImportReader()
	reader.addItem("", importItem("NO-PDF", "No Attachment", 2020))
	service := importService(f, reader)

	result, err := service.Import(ctx, literature.ImportSelection{ProjectID: f.ProjectID, ItemKeys: []string{"NO-PDF"}}, literature.ActorUser, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Skipped != 1 || result.Imported != 0 || result.Failed != 0 {
		t.Fatalf("counts = %+v", result)
	}
	if result.Outcomes[0].Status != literature.ImportSkippedNoPDF {
		t.Errorf("status = %s", result.Outcomes[0].Status)
	}
	if n := globalPaperCount(t, f); n != 0 {
		t.Errorf("global papers = %d, want 0", n)
	}
	if dirs := paperDirs(t, f); len(dirs) != 0 {
		t.Errorf("copy dirs = %v, want none", dirs)
	}
	if n := countActivities(t, f.Project, "paper.imported", ""); n != 0 {
		t.Errorf("events = %d, want 0", n)
	}
}

func TestImportMultiplePDFRequiresChoice(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	reader := newImportReader()
	item := importItem("MULTI", "Two PDFs", 2021)
	reader.addItem("", item,
		pdfAttachment(t, reader, "FIRST", "first pdf"),
		pdfAttachment(t, reader, "SECOND", "second pdf"),
	)
	service := importService(f, reader)
	base := literature.ImportSelection{ProjectID: f.ProjectID, ItemKeys: []string{"MULTI"}}

	undecided, err := service.Import(ctx, base, literature.ActorUser, nil)
	if err != nil {
		t.Fatalf("undecided import: %v", err)
	}
	if undecided.Outcomes[0].Status != literature.ImportNeedsAttachment || undecided.Skipped != 1 {
		t.Fatalf("undecided = %+v", undecided)
	}
	if n := globalPaperCount(t, f); n != 0 {
		t.Errorf("undecided import created %d papers", n)
	}
	if dirs := paperDirs(t, f); len(dirs) != 0 {
		t.Errorf("undecided import created %v", dirs)
	}

	base.AttachmentChoices = map[string]string{"MULTI": "SECOND"}
	decided, err := service.Import(ctx, base, literature.ActorUser, nil)
	if err != nil {
		t.Fatalf("decided import: %v", err)
	}
	if decided.Imported != 1 {
		t.Fatalf("decided = %+v", decided)
	}
	paper, err := service.GetPaper(ctx, decided.Outcomes[0].PaperID)
	if err != nil {
		t.Fatalf("get paper: %v", err)
	}
	if paper.FileHash != sha256Hex([]byte("second pdf")) {
		t.Errorf("stored the wrong attachment: %s", paper.FileHash)
	}
}

func TestImportExistingPaperWarnsAndKeepsCopy(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	existing := seedPaper(t, f, paperSeed{Key: "EX-1", Title: "Old Title", Copy: true, Content: "old copy"})
	reader := newImportReader()
	reader.addItem("", importItem("EX-1", "New Source Title", 2024), pdfAttachment(t, reader, "EX-ATT", "new source"))
	service := importService(f, reader)

	result, err := service.Import(ctx, literature.ImportSelection{ProjectID: f.ProjectID, ItemKeys: []string{"EX-1"}}, literature.ActorUser, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Linked != 1 || result.Imported != 0 {
		t.Fatalf("counts = %+v", result)
	}
	outcome := result.Outcomes[0]
	if outcome.Status != literature.ImportLinkedExisting || outcome.PaperID != existing {
		t.Fatalf("outcome = %+v", outcome)
	}
	if !strings.Contains(strings.Join(outcome.Warnings, ","), "source_hash_differs") {
		t.Errorf("warnings = %v, want source_hash_differs", outcome.Warnings)
	}
	paper, err := service.GetPaper(ctx, existing)
	if err != nil {
		t.Fatalf("get paper: %v", err)
	}
	if paper.FileHash != "hash-EX-1" {
		t.Errorf("hash changed: %s", paper.FileHash)
	}
	abs, _, err := NewFileStore(f.Workspace).Stat(ctx, existing)
	if err != nil {
		t.Fatalf("stat copy: %v", err)
	}
	if got, _ := os.ReadFile(abs); string(got) != "old copy" {
		t.Errorf("copy overwritten: %q", got)
	}
	if n := countActivities(t, f.Project, "paper.imported", existing); n != 1 {
		t.Errorf("events = %d, want 1", n)
	}
	list := paperImportedEvents(t, f.Project.Reader())
	if len(list) != 1 || (*list[0].Payload.New)["reused"] != true || (*list[0].Payload.New)["file_hash"] != "hash-EX-1" {
		t.Errorf("event payload = %+v", list)
	}

	// Re-importing into the same project must not emit another event.
	again, err := service.Import(ctx, literature.ImportSelection{ProjectID: f.ProjectID, ItemKeys: []string{"EX-1"}}, literature.ActorUser, nil)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if again.Outcomes[0].Status != literature.ImportSkippedDuplicate {
		t.Errorf("second status = %s", again.Outcomes[0].Status)
	}
	if n := countActivities(t, f.Project, "paper.imported", existing); n != 1 {
		t.Errorf("events after duplicate = %d, want 1", n)
	}
}

func TestImportExistingPaperMissingCopyOnlyWarns(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	existing := seedPaper(t, f, paperSeed{Key: "EX-2", Title: "No Copy"})
	reader := newImportReader()
	reader.addItem("", importItem("EX-2", "No Copy", 2021), pdfAttachment(t, reader, "EX2-ATT", "source"))
	service := importService(f, reader)

	result, err := service.Import(ctx, literature.ImportSelection{ProjectID: f.ProjectID, ItemKeys: []string{"EX-2"}}, literature.ActorUser, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	outcome := result.Outcomes[0]
	if outcome.Status != literature.ImportLinkedExisting {
		t.Fatalf("outcome = %+v", outcome)
	}
	if !strings.Contains(strings.Join(outcome.Warnings, ","), "local_file_missing") {
		t.Errorf("warnings = %v, want local_file_missing", outcome.Warnings)
	}
	if _, _, err := NewFileStore(f.Workspace).Stat(ctx, existing); !errors.Is(err, literature.ErrPaperFileMissing) {
		t.Errorf("import rebuilt a missing copy: %v", err)
	}
	if dirs := paperDirs(t, f); len(dirs) != 0 {
		t.Errorf("import created a copy directory: %v", dirs)
	}
}

func TestImportProjectTxFailureCompensates(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	reader := newImportReader()
	reader.addItem("", importItem("FAIL-1", "Compensate", 2020), pdfAttachment(t, reader, "FAIL-ATT", "compensate"))
	service := importService(f, reader)
	if _, err := f.Project.DB().ExecContext(ctx, `DROP TABLE project_papers`); err != nil {
		t.Fatalf("break project_papers: %v", err)
	}

	result, err := service.Import(ctx, literature.ImportSelection{ProjectID: f.ProjectID, ItemKeys: []string{"FAIL-1"}}, literature.ActorUser, nil)
	if err != nil {
		t.Fatalf("import returned an error: %v", err)
	}
	if result.Failed != 1 || result.Imported != 0 {
		t.Fatalf("counts = %+v", result)
	}
	if result.Outcomes[0].Status != literature.ImportFailed || result.Outcomes[0].Error == "" {
		t.Errorf("outcome = %+v", result.Outcomes[0])
	}
	if _, ok, err := f.Papers().ByZotero(ctx, "0", "FAIL-1"); err != nil || ok {
		t.Errorf("global row survived: ok=%v err=%v", ok, err)
	}
	if dirs := paperDirs(t, f); len(dirs) != 0 {
		t.Errorf("copy directories left behind: %v", dirs)
	}
	if n := countActivities(t, f.Project, "paper.imported", ""); n != 0 {
		t.Errorf("paper.imported events = %d, want 0", n)
	}
	if n := countActivities(t, f.Project, "relation.added", ""); n != 0 {
		t.Errorf("relation.added events = %d, want 0", n)
	}
}

func TestImportProgressOrder(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	reader := newImportReader()
	reader.addItem("", importItem("P-A", "A", 2020), pdfAttachment(t, reader, "PA", "a"))
	reader.addItem("", importItem("P-B", "B", 2021))
	reader.addItem("", importItem("P-C", "C", 2022), pdfAttachment(t, reader, "PC", "c"))
	service := importService(f, reader)

	var progress []literature.ImportProgress
	result, err := service.Import(ctx, literature.ImportSelection{
		ProjectID: f.ProjectID,
		ItemKeys:  []string{"P-A", "P-B", "P-C"},
	}, literature.ActorUser, func(p literature.ImportProgress) { progress = append(progress, p) })
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(progress) != 3 {
		t.Fatalf("progress = %+v", progress)
	}
	wantKeys := []string{"P-A", "P-B", "P-C"}
	wantStatus := []literature.ImportStatus{
		literature.ImportCreated,
		literature.ImportSkippedNoPDF,
		literature.ImportCreated,
	}
	for i, p := range progress {
		if p.Index != i+1 || p.Total != 3 || p.ItemKey != wantKeys[i] || p.Status != wantStatus[i] {
			t.Errorf("progress[%d] = %+v", i, p)
		}
	}
	for i, outcome := range result.Outcomes {
		if outcome.ZoteroKey != wantKeys[i] {
			t.Errorf("outcome[%d] = %+v", i, outcome)
		}
	}
}

func TestImportSingleFlight(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	base := newImportReader()
	base.collections = []literature.ZoteroCollection{{Key: "C1", Name: "Collection"}}
	base.addItem("C1", importItem("LOCK-1", "Locked", 2020), pdfAttachment(t, base, "LOCK-ATT", "locked"))
	blocking := &blockingImportReader{importReader: base, entered: make(chan struct{}), release: make(chan struct{})}
	service := importService(f, blocking)
	sel := literature.ImportSelection{ProjectID: f.ProjectID, CollectionKeys: []string{"C1"}}

	firstDone := make(chan error, 1)
	go func() {
		_, err := service.Import(ctx, sel, literature.ActorUser, nil)
		firstDone <- err
	}()
	select {
	case <-blocking.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first import never reached the reader")
	}

	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(blocking.release) }) }
	defer release()

	secondDone := make(chan error, 1)
	go func() {
		_, err := service.Import(ctx, sel, literature.ActorUser, nil)
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		if !errors.Is(err, literature.ErrBusy) {
			t.Fatalf("second import error = %v, want ErrBusy", err)
		}
	case <-time.After(5 * time.Second):
		release()
		t.Fatal("second import did not fail with ErrBusy")
	}
	release()
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first import: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first import did not finish")
	}
}

// blockingImportReader blocks the first ListItems call until release is closed.
type blockingImportReader struct {
	*importReader
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingImportReader) ListItems(ctx context.Context, libraryID, collectionKey string, opts literature.ListItemsOptions) (literature.ItemPage, error) {
	r.once.Do(func() { close(r.entered) })
	select {
	case <-r.release:
	case <-ctx.Done():
		return literature.ItemPage{}, ctx.Err()
	}
	return r.importReader.ListItems(ctx, libraryID, collectionKey, opts)
}

func TestImportRejectsInvalidInputs(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	reader := newImportReader()
	reader.addItem("", importItem("BAD-1", "Bad", 2020), pdfAttachment(t, reader, "BAD-ATT", "bad"))
	sel := literature.ImportSelection{ProjectID: f.ProjectID, ItemKeys: []string{"BAD-1"}}

	t.Run("zotero required", func(t *testing.T) {
		plain := New(f.Workspace, Options{Clock: f.Clock})
		if _, err := plain.Import(ctx, sel, literature.ActorUser, nil); !errors.Is(err, literature.ErrZoteroNotConfigured) {
			t.Errorf("import error = %v", err)
		}
		if _, err := plain.Preview(ctx, sel); !errors.Is(err, literature.ErrZoteroNotConfigured) {
			t.Errorf("preview error = %v", err)
		}
	})

	service := importService(f, reader)
	t.Run("ai actor rejected", func(t *testing.T) {
		if _, err := service.Import(ctx, sel, literature.ActorAI, nil); !errors.Is(err, errs.ErrBadActor) {
			t.Errorf("error = %v, want ErrBadActor", err)
		}
	})
	t.Run("group library rejected", func(t *testing.T) {
		groupSel := sel
		groupSel.LibraryID = "12345"
		if _, err := service.Import(ctx, groupSel, literature.ActorUser, nil); err == nil {
			t.Error("group library accepted")
		}
	})
	t.Run("archived project rejected", func(t *testing.T) {
		archiveProject(t, f, f.ProjectID)
		if _, err := service.Import(ctx, sel, literature.ActorUser, nil); !errors.Is(err, literature.ErrProjectArchived) {
			t.Errorf("error = %v, want ErrProjectArchived", err)
		}
	})
}

func TestImportKeepsManualLinkEventsSeparate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	reader := newImportReader()
	reader.addItem("", importItem("MAN-1", "Manual Link", 2020), pdfAttachment(t, reader, "MAN-ATT", "manual"))
	service := importService(f, reader)

	result, err := service.Import(ctx, literature.ImportSelection{ProjectID: f.ProjectID, ItemKeys: []string{"MAN-1"}}, literature.ActorUser, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	paperID := result.Outcomes[0].PaperID
	if n := countActivities(t, f.Project, "relation.added", paperID); n != 0 {
		t.Fatalf("import emitted relation.added: %d", n)
	}
	if _, err := service.Unlink(ctx, f.ProjectID, paperID, literature.ActorUser); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	linked, err := service.Link(ctx, f.ProjectID, paperID, literature.ActorUser)
	if err != nil {
		t.Fatalf("manual link: %v", err)
	}
	if !linked.Added {
		t.Error("manual link reported Added=false")
	}
	if n := countActivities(t, f.Project, "relation.added", paperID); n != 1 {
		t.Errorf("manual relation.added events = %d, want 1", n)
	}
	if n := countActivities(t, f.Project, "paper.imported", paperID); n != 1 {
		t.Errorf("paper.imported events = %d, want 1", n)
	}
}

func TestPreviewExpandsCollectionsAndFiltersItems(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	reader := newImportReader()
	// C1 -> C1A -> C1A1, with a cycle back to C1 from C1A1.
	reader.collections = []literature.ZoteroCollection{
		{Key: "C1", Name: "Root"},
		{Key: "C1A", ParentKey: "C1", Name: "Child"},
		{Key: "C1A1", ParentKey: "C1A", Name: "Grandchild"},
		{Key: "C2", Name: "Other"},
	}
	// The cycle: C1 appears as a child of C1A1 as well.
	reader.collections = append(reader.collections, literature.ZoteroCollection{Key: "C1", ParentKey: "C1A1", Name: "Root"})

	itemA := importItem("IT-A", "A", 2020)
	itemB := importItem("IT-B", "B", 2021)
	itemC := importItem("IT-C", "C", 2022)
	itemD := importItem("IT-D", "D", 2023)
	itemE := importItem("IT-E", "E", 2024)
	noteItem := literature.ZoteroItem{Key: "IT-NOTE", ItemType: "note", Title: "note"}
	attachmentItem := literature.ZoteroItem{Key: "IT-ATT", ItemType: "attachment", Title: "attachment"}
	reader.addItem("C1", itemA, pdfAttachment(t, reader, "ATTA", "a"))
	reader.addItem("C1", noteItem)
	reader.addItem("C1A", itemB, pdfAttachment(t, reader, "ATTB", "b"))
	reader.addItem("C1A1", itemC, pdfAttachment(t, reader, "ATTC", "c"))
	reader.addItem("C1A1", attachmentItem)
	reader.addItem("C2", itemD, pdfAttachment(t, reader, "ATTD", "d"))
	reader.addItem("", itemE, pdfAttachment(t, reader, "ATTE", "e"))
	service := importService(f, reader)

	deep, err := service.Preview(ctx, literature.ImportSelection{
		ProjectID:             f.ProjectID,
		CollectionKeys:        []string{"C1"},
		IncludeSubcollections: true,
		ItemKeys:              []string{"IT-B", "IT-E", "MISSING"},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	want := []string{"IT-A", "IT-B", "IT-C", "IT-E"}
	if got := previewKeys(deep); !equalStrings(got, want) {
		t.Errorf("preview order = %v, want %v", got, want)
	}
	if len(deep.Errors) != 1 || deep.Errors[0].ZoteroKey != "MISSING" {
		t.Errorf("preview errors = %+v", deep.Errors)
	}

	shallow, err := service.Preview(ctx, literature.ImportSelection{
		ProjectID:      f.ProjectID,
		CollectionKeys: []string{"C1"},
		ItemKeys:       []string{"IT-B", "IT-E"},
	})
	if err != nil {
		t.Fatalf("shallow preview: %v", err)
	}
	if got, want := previewKeys(shallow), []string{"IT-A", "IT-B", "IT-E"}; !equalStrings(got, want) {
		t.Errorf("shallow order = %v, want %v", got, want)
	}

	// Preview stays read-only.
	if n := globalPaperCount(t, f); n != 0 {
		t.Errorf("preview wrote %d papers", n)
	}
	if dirs := paperDirs(t, f); len(dirs) != 0 {
		t.Errorf("preview wrote files: %v", dirs)
	}
	if n := countActivities(t, f.Project, "", ""); n != 0 {
		t.Errorf("preview wrote %d activities", n)
	}
}

func TestPreviewExistingPaperFields(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	withCopy := seedPaper(t, f, paperSeed{Key: "PV-1", Title: "Existing", Copy: true})
	noCopy := seedPaper(t, f, paperSeed{Key: "PV-2", Title: "Missing copy"})
	reader := newImportReader()
	reader.addItem("", importItem("PV-1", "Existing Preview", 2019),
		pdfAttachment(t, reader, "PV1-A", "a"),
		pdfAttachment(t, reader, "PV1-B", "b"),
	)
	reader.addItem("", importItem("PV-2", "Missing Preview", 2018), pdfAttachment(t, reader, "PV2-A", "c"))
	service := importService(f, reader)

	preview, err := service.Preview(ctx, literature.ImportSelection{ProjectID: f.ProjectID, ItemKeys: []string{"PV-1", "PV-2"}})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.Entries) != 2 {
		t.Fatalf("entries = %+v", preview.Entries)
	}
	first := preview.Entries[0]
	if first.ExistingPaperID != withCopy || first.ExistingHash != "hash-PV-1" {
		t.Errorf("existing fields = %+v", first)
	}
	if first.PDFCount != 2 || !first.PDFAvailable || first.AttachmentKey != "" {
		t.Errorf("pdf fields = %+v", first)
	}
	if first.IncomingHash != "" || first.HashEqual {
		t.Errorf("preview must not hash sources: %+v", first)
	}
	if first.Year != 2019 || first.Title != "Existing Preview" || len(first.Creators) != 1 {
		t.Errorf("metadata fields = %+v", first)
	}
	second := preview.Entries[1]
	if second.ExistingPaperID != noCopy {
		t.Errorf("existing id = %q, want %q", second.ExistingPaperID, noCopy)
	}
	if !containsString(second.Warnings, "local_file_missing") {
		t.Errorf("warnings = %v, want local_file_missing", second.Warnings)
	}

	withChoice, err := service.Preview(ctx, literature.ImportSelection{
		ProjectID:         f.ProjectID,
		ItemKeys:          []string{"PV-1"},
		AttachmentChoices: map[string]string{"PV-1": "PV1-B"},
	})
	if err != nil {
		t.Fatalf("preview choice: %v", err)
	}
	if withChoice.Entries[0].AttachmentKey != "PV1-B" {
		t.Errorf("attachment key = %q", withChoice.Entries[0].AttachmentKey)
	}
	if n := globalPaperCount(t, f); n != 2 {
		t.Errorf("preview changed paper count: %d", n)
	}
	if n := countActivities(t, f.Project, "", ""); n != 0 {
		t.Errorf("preview wrote activities: %d", n)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
