package zotero

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"researchos/internal/literature"
)

func openFixtureReader(t *testing.T, f *zoteroFixture) *SQLiteReader {
	t.Helper()
	reader, err := NewSQLiteReader(f.Dir)
	if err != nil {
		t.Fatalf("NewSQLiteReader: %v", err)
	}
	reader.LinkedBaseDir = f.LinkedDir
	t.Cleanup(func() { reader.Close() })
	return reader
}

func TestSQLiteReaderListItems(t *testing.T) {
	f := newZoteroFixture(t)
	reader := openFixtureReader(t, f)
	ctx := context.Background()

	libs, err := reader.ListLibraries(ctx)
	if err != nil {
		t.Fatalf("ListLibraries: %v", err)
	}
	if len(libs) != 1 || libs[0].ID != "0" || libs[0].Name != "我的文库" {
		t.Fatalf("ListLibraries = %+v", libs)
	}
	if reader.Source() != literature.SourceSQLite {
		t.Fatalf("Source = %q", reader.Source())
	}

	page, err := reader.ListItems(ctx, "0", "", literature.ListItemsOptions{Limit: 2})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if page.Total != 3 {
		t.Fatalf("Total = %d, want 3", page.Total)
	}
	if page.SourceUsed != literature.SourceSQLite {
		t.Fatalf("SourceUsed = %q", page.SourceUsed)
	}
	if len(page.Items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(page.Items))
	}
	first := page.Items[0]
	if first.Key != "ITEM0001" || first.ItemType != "journalArticle" || first.Version != 101 {
		t.Fatalf("first item identity = %+v", first)
	}
	if first.Title != "深度学习在气候预测中的应用" {
		t.Errorf("title = %q", first.Title)
	}
	if first.Date != "2024-05-01" || first.DOI != "10.1000/abc" || first.URL != "https://example.org/a" {
		t.Errorf("metadata = %+v", first)
	}
	if first.AbstractNote != "摘要正文" || first.PublicationTitle != "气象学报" || first.Language != "zh-CN" {
		t.Errorf("metadata = %+v", first)
	}
	if first.Extra != "Citation Key: test1" {
		t.Errorf("extra = %q", first.Extra)
	}
	if len(first.Creators) != 2 {
		t.Fatalf("creators = %+v", first.Creators)
	}
	if first.Creators[0].FirstName != "Ada" || first.Creators[0].LastName != "Lovelace" ||
		first.Creators[0].CreatorType != "author" {
		t.Errorf("creator 0 = %+v", first.Creators[0])
	}
	if first.Creators[1].LastName != "张" || first.Creators[1].FirstName != "三" {
		t.Errorf("creator 1 = %+v", first.Creators[1])
	}
	if len(first.Tags) != 2 || first.Tags[0] != "climate" || first.Tags[1] != "深度学习" {
		t.Errorf("tags = %+v", first.Tags)
	}
	if len(first.Collections) != 1 || first.Collections[0] != "COLLAAAA" {
		t.Errorf("collections = %+v", first.Collections)
	}
	if first.Raw["volume"] != "12" {
		t.Errorf("unknown field volume not preserved in Raw: %+v", first.Raw)
	}
	if rawCollections, ok := first.Raw["collections"].([]any); !ok || len(rawCollections) != 1 || rawCollections[0] != "COLLAAAA" {
		t.Errorf("raw collections = %+v", first.Raw["collections"])
	}

	body, err := MetadataJSON(first)
	if err != nil {
		t.Fatalf("MetadataJSON: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(body), &metadata); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if metadata["volume"] != "12" {
		t.Errorf("MetadataJSON dropped volume: %s", body)
	}
	rawTags, ok := metadata["tags"].([]any)
	if !ok || len(rawTags) != 2 {
		t.Fatalf("metadata tags = %v", metadata["tags"])
	}
	if tag := rawTags[0].(map[string]any); tag["tag"] != "climate" || tag["type"] != float64(1) {
		t.Errorf("metadata tag 0 = %v", tag)
	}
	meta := ToPaperMetadata(first)
	if meta.Year != 2024 {
		t.Errorf("year = %d", meta.Year)
	}
	if meta.Authors != "Lovelace, Ada, 张, 三" {
		t.Errorf("authors = %q", meta.Authors)
	}
	if len(meta.Tags) != 2 || meta.Tags[0].Tag != "climate" || meta.Tags[0].Type != 1 {
		t.Errorf("normalized tags = %+v", meta.Tags)
	}
	if meta.Tags[1].Tag != "深度学习" || meta.Tags[1].Type != 0 {
		t.Errorf("normalized tags = %+v", meta.Tags)
	}

	second := page.Items[1]
	if second.Key != "ITEM0002" || second.Title != "Boring Systems" {
		t.Errorf("second item = %+v", second)
	}
	if len(second.Creators) != 2 {
		t.Fatalf("second creators = %+v", second.Creators)
	}
	if second.Creators[0].Name != "World Health Organization" || second.Creators[0].LastName != "" {
		t.Errorf("single-field creator = %+v", second.Creators[0])
	}
	if second.Creators[1].CreatorType != "editor" {
		t.Errorf("creator type = %q", second.Creators[1].CreatorType)
	}
	if ToPaperMetadata(second).Authors != "World Health Organization, Knuth, Donald" {
		t.Errorf("authors = %q", ToPaperMetadata(second).Authors)
	}
	if ToPaperMetadata(second).Year != 2019 {
		t.Errorf("year = %d", ToPaperMetadata(second).Year)
	}

	page2, err := reader.ListItems(ctx, "0", "", literature.ListItemsOptions{Offset: 2, Limit: 2})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if page2.Total != 3 || len(page2.Items) != 1 || page2.Items[0].Key != "ITEM0003" {
		t.Fatalf("page 2 = total %d items %+v", page2.Total, page2.Items)
	}

	query, err := reader.ListItems(ctx, "0", "", literature.ListItemsOptions{Query: "pagination", Limit: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if query.Total != 1 || len(query.Items) != 1 || query.Items[0].Key != "ITEM0003" {
		t.Fatalf("query = %+v", query)
	}
	chinese, err := reader.ListItems(ctx, "0", "", literature.ListItemsOptions{Query: "气候", Limit: 10})
	if err != nil {
		t.Fatalf("query chinese: %v", err)
	}
	if chinese.Total != 1 || len(chinese.Items) != 1 || chinese.Items[0].Key != "ITEM0001" {
		t.Fatalf("chinese query = %+v", chinese)
	}
	empty, err := reader.ListItems(ctx, "0", "", literature.ListItemsOptions{Query: "no such title", Limit: 10})
	if err != nil {
		t.Fatalf("empty query: %v", err)
	}
	if empty.Total != 0 || len(empty.Items) != 0 {
		t.Fatalf("empty query = %+v", empty)
	}

	all, err := reader.ListItems(ctx, "0", "", literature.ListItemsOptions{Limit: 500})
	if err != nil {
		t.Fatalf("clamped limit: %v", err)
	}
	seen := map[string]bool{}
	for _, item := range all.Items {
		seen[item.Key] = true
	}
	for _, forbidden := range []string{"ITEMDEL1", "NOTE0001", "ATTTOP01"} {
		if seen[forbidden] {
			t.Errorf("deleted/note/attachment item %s leaked into list", forbidden)
		}
	}
}

func TestSQLiteReaderGetItem(t *testing.T) {
	f := newZoteroFixture(t)
	reader := openFixtureReader(t, f)
	ctx := context.Background()

	item, err := reader.GetItem(ctx, "0", "ITEM0001")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Key != "ITEM0001" || item.Title != "深度学习在气候预测中的应用" || item.LibraryID != "0" {
		t.Fatalf("item = %+v", item)
	}
	if item.Version != 101 {
		t.Errorf("version = %d", item.Version)
	}

	for _, key := range []string{"NOPE0000", "ITEMDEL1", "NOTE0001", "ATTTOP01"} {
		if _, err := reader.GetItem(ctx, "0", key); !errors.Is(err, literature.ErrItemNotFound) {
			t.Errorf("GetItem(%s) error = %v, want ErrItemNotFound", key, err)
		}
	}
	if _, err := reader.GetItem(ctx, "999", "ITEM0001"); !errors.Is(err, literature.ErrItemNotFound) {
		t.Errorf("wrong library error = %v", err)
	}
}

func TestSQLiteReaderListCollections(t *testing.T) {
	f := newZoteroFixture(t)
	reader := openFixtureReader(t, f)

	collections, err := reader.ListCollections(context.Background(), "0")
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(collections) != 2 {
		t.Fatalf("collections = %+v", collections)
	}
	byKey := map[string]literature.ZoteroCollection{}
	for _, c := range collections {
		byKey[c.Key] = c
	}
	root, ok := byKey["COLLAAAA"]
	if !ok {
		t.Fatalf("missing root collection: %+v", collections)
	}
	if root.Name != "气候" || root.ParentKey != "" || root.ItemCount != 2 {
		t.Errorf("root collection = %+v", root)
	}
	child, ok := byKey["COLLBBBB"]
	if !ok {
		t.Fatalf("missing child collection: %+v", collections)
	}
	if child.Name != "子集" || child.ParentKey != "COLLAAAA" || child.ItemCount != 1 {
		t.Errorf("child collection = %+v", child)
	}

	if _, err := reader.ListCollections(context.Background(), "not-a-number"); err == nil {
		t.Error("expected error for invalid library ID")
	}
}

func TestSQLiteReaderAttachmentsAndResolve(t *testing.T) {
	f := newZoteroFixture(t)
	reader := openFixtureReader(t, f)
	ctx := context.Background()

	atts, err := reader.ListAttachments(ctx, "0", "ITEM0001")
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(atts) != 2 {
		t.Fatalf("attachments = %+v", atts)
	}
	if atts[0].Key != "ATT00001" || atts[0].Filename != "paper-one.pdf" ||
		atts[0].LinkMode != "imported_file" || atts[0].ContentType != "application/pdf" ||
		atts[0].ParentKey != "ITEM0001" || atts[0].LibraryID != "0" {
		t.Errorf("attachment 0 = %+v", atts[0])
	}
	if atts[0].Size != int64(len("%PDF-1.4 one")) {
		t.Errorf("attachment 0 size = %d", atts[0].Size)
	}
	if atts[1].Key != "ATT00002" || atts[1].Size != int64(len("%PDF-1.4 two")) {
		t.Errorf("attachment 1 = %+v", atts[1])
	}

	linked, err := reader.ListAttachments(ctx, "0", "ITEM0002")
	if err != nil {
		t.Fatalf("ListAttachments linked: %v", err)
	}
	if len(linked) != 2 {
		t.Fatalf("linked attachments = %+v", linked)
	}
	if linked[0].LinkMode != "linked_file" || linked[0].Size == 0 || linked[0].Filename != "paper.pdf" {
		t.Errorf("linked attachment = %+v", linked[0])
	}
	if linked[1].LinkMode != "linked_url" || linked[1].Size != 0 {
		t.Errorf("linked url attachment = %+v", linked[1])
	}

	resolved, err := reader.ResolveAttachment(ctx, atts[0])
	if err != nil {
		t.Fatalf("ResolveAttachment: %v", err)
	}
	want := filepath.Join(f.Dir, "storage", "ATT00001", "paper-one.pdf")
	if resolved.Path != want {
		t.Errorf("resolved path = %q, want %q", resolved.Path, want)
	}
	if resolved.Size != int64(len("%PDF-1.4 one")) || resolved.ModTime.IsZero() {
		t.Errorf("resolved = %+v", resolved)
	}
	if _, err := os.Stat(resolved.Path); err != nil {
		t.Errorf("resolved file not readable: %v", err)
	}

	linkedResolved, err := reader.ResolveAttachment(ctx, linked[0])
	if err != nil {
		t.Fatalf("ResolveAttachment linked: %v", err)
	}
	if want := filepath.Join(f.LinkedDir, "linked", "paper.pdf"); linkedResolved.Path != want {
		t.Errorf("linked path = %q, want %q", linkedResolved.Path, want)
	}

	if _, err := reader.ResolveAttachment(ctx, linked[1]); !errors.Is(err, literature.ErrAttachmentUnresolved) {
		t.Errorf("linked URL error = %v", err)
	}
	escape := literature.ZoteroAttachment{Key: "ATT00001", Path: "storage:../../evil.pdf"}
	if _, err := reader.ResolveAttachment(ctx, escape); !errors.Is(err, literature.ErrPathOutsideStorage) {
		t.Errorf("escape error = %v, want ErrPathOutsideStorage", err)
	}
	missing := literature.ZoteroAttachment{Key: "ATT00002", Path: "storage:missing.pdf"}
	if _, err := reader.ResolveAttachment(ctx, missing); !errors.Is(err, literature.ErrAttachmentUnresolved) {
		t.Errorf("missing error = %v", err)
	}
	if _, err := reader.ListAttachments(ctx, "0", "NOPE0000"); !errors.Is(err, literature.ErrItemNotFound) {
		t.Errorf("missing parent error = %v", err)
	}
}

func TestSQLiteReaderIsReadOnly(t *testing.T) {
	f := newZoteroFixture(t)
	before := snapshotFiles(t, f.Dir)

	reader, err := NewSQLiteReader(f.Dir)
	if err != nil {
		t.Fatalf("NewSQLiteReader: %v", err)
	}
	reader.LinkedBaseDir = f.LinkedDir
	ctx := context.Background()
	if _, err := reader.ListItems(ctx, "0", "", literature.ListItemsOptions{Limit: 10}); err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if _, err := reader.GetItem(ctx, "0", "ITEM0001"); err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if _, err := reader.ListCollections(ctx, "0"); err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	atts, err := reader.ListAttachments(ctx, "0", "ITEM0001")
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if _, err := reader.ResolveAttachment(ctx, atts[0]); err != nil {
		t.Fatalf("ResolveAttachment: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	after := snapshotFiles(t, f.Dir)
	assertSnapshotUnchanged(t, before, after)
}

func TestNewSQLiteReaderRejectsInvalidDirs(t *testing.T) {
	if _, err := NewSQLiteReader(""); err == nil {
		t.Error("empty dir: expected error")
	}
	if _, err := NewSQLiteReader(t.TempDir()); !errors.Is(err, literature.ErrZoteroDirInvalid) {
		t.Errorf("missing sqlite error = %v", err)
	}
}

func TestReadOnlyDSN(t *testing.T) {
	path := filepath.Join("data", "Zotero", "zotero.sqlite")
	dsn := readOnlyDSN(path, 3000)
	wants := []string{"mode=ro", "_pragma=query_only(1)", "_pragma=busy_timeout(3000)", filepath.ToSlash(path)}
	for _, want := range wants {
		if !strings.Contains(dsn, want) {
			t.Errorf("dsn %q missing %q", dsn, want)
		}
	}
	if strings.Contains(dsn, "immutable=1") {
		t.Errorf("dsn must not be immutable: %q", dsn)
	}
}
