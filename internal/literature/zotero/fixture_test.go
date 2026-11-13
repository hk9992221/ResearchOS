package zotero

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// zoteroFixture is a Zotero-7-shaped database plus storage files, built in a
// temporary directory. It covers the §5.2 tables the reader touches.
type zoteroFixture struct {
	Dir       string
	LinkedDir string
}

const fixtureSchema = `
CREATE TABLE itemTypes (itemTypeID INTEGER PRIMARY KEY, typeName TEXT NOT NULL);
CREATE TABLE items (
    itemID INTEGER PRIMARY KEY,
    itemTypeID INTEGER NOT NULL,
    key TEXT NOT NULL,
    dateAdded TEXT,
    dateModified TEXT,
    libraryID INTEGER NOT NULL DEFAULT 0,
    version INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE deletedItems (itemID INTEGER PRIMARY KEY, dateDeleted TEXT);
CREATE TABLE fields (fieldID INTEGER PRIMARY KEY, fieldName TEXT NOT NULL);
CREATE TABLE itemDataValues (valueID INTEGER PRIMARY KEY, value TEXT);
CREATE TABLE itemData (
    itemID INTEGER NOT NULL,
    fieldID INTEGER NOT NULL,
    valueID INTEGER NOT NULL,
    PRIMARY KEY (itemID, fieldID)
);
CREATE TABLE creators (
    creatorID INTEGER PRIMARY KEY,
    firstName TEXT,
    lastName TEXT,
    fieldMode INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE creatorTypes (creatorTypeID INTEGER PRIMARY KEY, creatorType TEXT NOT NULL);
CREATE TABLE itemCreators (
    itemID INTEGER NOT NULL,
    creatorID INTEGER NOT NULL,
    creatorTypeID INTEGER NOT NULL,
    orderIndex INTEGER NOT NULL
);
CREATE TABLE collections (
    collectionID INTEGER PRIMARY KEY,
    collectionName TEXT NOT NULL,
    parentCollectionID INTEGER,
    key TEXT NOT NULL,
    libraryID INTEGER NOT NULL DEFAULT 0,
    version INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE collectionItems (
    collectionID INTEGER NOT NULL,
    itemID INTEGER NOT NULL,
    orderIndex INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE itemAttachments (
    itemID INTEGER PRIMARY KEY,
    parentItemID INTEGER,
    linkMode INTEGER NOT NULL DEFAULT 0,
    contentType TEXT,
    path TEXT,
    storageModTime INTEGER,
    storageHash TEXT
);
CREATE TABLE tags (tagID INTEGER PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE itemTags (
    itemID INTEGER NOT NULL,
    tagID INTEGER NOT NULL,
    type INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (itemID, tagID)
);
CREATE TABLE libraries (
    libraryID INTEGER PRIMARY KEY,
    type TEXT NOT NULL,
    editable INTEGER NOT NULL DEFAULT 1
);
PRAGMA user_version = 128;
`

func newZoteroFixture(t *testing.T) *zoteroFixture {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "Zotero")
	linked := filepath.Join(base, "linked-base")
	for _, d := range []string{
		dir,
		filepath.Join(dir, "storage"),
		filepath.Join(dir, "storage", "ATT00001"),
		filepath.Join(dir, "storage", "ATT00002"),
		filepath.Join(dir, "storage", "ATTTOP01"),
		filepath.Join(linked, "linked"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	writeFile(t, filepath.Join(dir, "storage", "ATT00001", "paper-one.pdf"), "%PDF-1.4 one")
	writeFile(t, filepath.Join(dir, "storage", "ATT00002", "paper-two.pdf"), "%PDF-1.4 two")
	writeFile(t, filepath.Join(dir, "storage", "ATTTOP01", "index.html"), "<html></html>")
	writeFile(t, filepath.Join(linked, "linked", "paper.pdf"), "%PDF-1.4 linked")

	sqlitePath := filepath.Join(dir, "zotero.sqlite")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(sqlitePath))
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(fixtureSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	fixture := &zoteroFixture{Dir: dir, LinkedDir: linked}
	seedFixture(t, db)
	return fixture
}

func seedFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}

	exec(`INSERT INTO itemTypes(itemTypeID, typeName) VALUES
		(1, 'journalArticle'), (2, 'book'), (3, 'attachment'), (4, 'note')`)
	exec(`INSERT INTO libraries(libraryID, type, editable) VALUES (0, 'user', 1)`)

	exec(`INSERT INTO fields(fieldID, fieldName) VALUES
		(1, 'title'), (2, 'date'), (3, 'DOI'), (4, 'url'),
		(5, 'abstractNote'), (6, 'publicationTitle'), (7, 'language'), (8, 'extra'),
		(9, 'volume')`)

	exec(`INSERT INTO items(itemID, itemTypeID, key, libraryID, version) VALUES
		(1, 1, 'ITEM0001', 0, 101),
		(2, 2, 'ITEM0002', 0, 102),
		(3, 1, 'ITEM0003', 0, 103),
		(4, 1, 'ITEMDEL1', 0, 104),
		(5, 4, 'NOTE0001', 0, 105),
		(6, 3, 'ATTTOP01', 0, 106),
		(8, 3, 'ATT00001', 0, 107),
		(9, 3, 'ATT00002', 0, 108),
		(10, 3, 'ATT00003', 0, 109),
		(11, 3, 'ATT00004', 0, 110)`)
	exec(`INSERT INTO deletedItems(itemID, dateDeleted) VALUES (4, '2025-01-01')`)

	exec(`INSERT INTO creators(creatorID, firstName, lastName, fieldMode) VALUES
		(1, 'Ada', 'Lovelace', 0),
		(2, '三', '张', 0),
		(3, '', 'World Health Organization', 1),
		(4, 'Donald', 'Knuth', 0)`)
	exec(`INSERT INTO creatorTypes(creatorTypeID, creatorType) VALUES (1, 'author'), (2, 'editor')`)
	exec(`INSERT INTO itemCreators(itemID, creatorID, creatorTypeID, orderIndex) VALUES
		(1, 1, 1, 0), (1, 2, 1, 1),
		(2, 3, 1, 0), (2, 4, 2, 1),
		(3, 4, 1, 0)`)

	exec(`INSERT INTO itemDataValues(valueID, value) VALUES
		(1, '深度学习在气候预测中的应用'), (2, '2024-05-01'), (3, '10.1000/abc'),
		(4, 'https://example.org/a'), (5, '摘要正文'), (6, '气象学报'),
		(7, 'zh-CN'), (8, 'Citation Key: test1'),
		(9, 'Boring Systems'), (10, 'May 2019'), (11, 'Example Press'), (12, 'en'),
		(13, 'Pagination Target'), (14, '2021-11'), (15, '10.1000/page'),
		(16, 'Deleted Paper'), (17, '2018'), (18, '12')`)
	exec(`INSERT INTO itemData(itemID, fieldID, valueID) VALUES
		(1, 1, 1), (1, 2, 2), (1, 3, 3), (1, 4, 4), (1, 5, 5), (1, 6, 6), (1, 7, 7), (1, 8, 8), (1, 9, 18),
		(2, 1, 9), (2, 2, 10), (2, 6, 11), (2, 7, 12),
		(3, 1, 13), (3, 2, 14), (3, 3, 15),
		(4, 1, 16), (4, 2, 17)`)

	exec(`INSERT INTO collections(collectionID, collectionName, parentCollectionID, key, libraryID) VALUES
		(1, '气候', NULL, 'COLLAAAA', 0),
		(2, '子集', 1, 'COLLBBBB', 0)`)
	exec(`INSERT INTO collectionItems(collectionID, itemID, orderIndex) VALUES
		(1, 1, 0), (1, 3, 1), (2, 2, 0)`)

	exec(`INSERT INTO tags(tagID, name) VALUES
		(1, '深度学习'), (2, 'climate'), (3, '方法'), (4, 'automatic-tag')`)
	exec(`INSERT INTO itemTags(itemID, tagID, type) VALUES
		(1, 1, 0), (1, 2, 1), (2, 3, 0), (3, 4, 1)`)

	exec(`INSERT INTO itemAttachments(itemID, parentItemID, linkMode, contentType, path) VALUES
		(8, 1, 0, 'application/pdf', 'storage:paper-one.pdf'),
		(9, 1, 0, 'application/pdf', 'storage:paper-two.pdf'),
		(10, 2, 2, 'application/pdf', 'attachments:linked/paper.pdf'),
		(11, 2, 3, 'application/pdf', 'https://example.org/x.pdf'),
		(6, NULL, 0, 'text/html', 'storage:index.html')`)

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit fixture: %v", err)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

type fileState struct {
	Hash    string
	Size    int64
	ModTime int64
}

// snapshotFiles captures every regular file below root.
func snapshotFiles(t *testing.T, root string) map[string]fileState {
	t.Helper()
	out := map[string]fileState{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[rel] = fileState{Hash: hex.EncodeToString(sum[:]), Size: info.Size(), ModTime: info.ModTime().UnixNano()}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return out
}

func assertSnapshotUnchanged(t *testing.T, before, after map[string]fileState) {
	t.Helper()
	if reflect.DeepEqual(before, after) {
		return
	}
	for name, state := range after {
		if old, ok := before[name]; !ok {
			t.Errorf("read-only violation: new file %s", name)
		} else if old != state {
			t.Errorf("read-only violation: %s changed: before=%+v after=%+v", name, old, state)
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			t.Errorf("read-only violation: file %s disappeared", name)
		}
	}
}
