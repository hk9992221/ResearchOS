package zotero

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"researchos/internal/literature"
)

var _ literature.ZoteroReader = (*SQLiteReader)(nil)

// SQLiteReader reads a Zotero database through a read-only SQLite connection.
// It never writes, moves, renames or deletes anything in the data directory.
//
// The schema follows Zotero 7 (§5.2, O-04). Version differences are confined to
// the queries in this file; a failed query returns a diagnostic error and never
// modifies the database.
//
// O-19: modernc.org/sqlite with mode=ro may still create a -shm side file for a
// WAL-mode database. If the P0 spike observes that on real Zotero data, switch
// to a snapshot copy or the SQLite backup API. This package does not copy or
// mutate the live database.
type SQLiteReader struct {
	db      *sql.DB
	dataDir string
	// LinkedBaseDir is Zotero's "Linked Attachment Base Directory"
	// (extensions.zotero.baseAttachmentPath). It is exported so callers and
	// tests can inject it; an empty value leaves "attachments:" paths
	// unresolved.
	LinkedBaseDir string
}

// NewSQLiteReader opens <dataDir>/zotero.sqlite read-only. The directory and
// the database file must already exist; nothing is created.
func NewSQLiteReader(dataDir string) (*SQLiteReader, error) {
	dir := canonicalPath(dataDir)
	if dir == "" {
		return nil, fmt.Errorf("zotero: 数据目录为空")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", literature.ErrZoteroDirInvalid, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s 不是目录", literature.ErrZoteroDirInvalid, dir)
	}
	path := filepath.Join(dir, "zotero.sqlite")
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", literature.ErrZoteroDirInvalid, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: zotero.sqlite 不是普通文件", literature.ErrZoteroDirInvalid)
	}
	db, err := openReadOnlySQLite(context.Background(), path, sqliteBusyTimeoutMS)
	if err != nil {
		return nil, err
	}
	return &SQLiteReader{db: db, dataDir: dir}, nil
}

// DataDir returns the canonical data directory.
func (r *SQLiteReader) DataDir() string { return r.dataDir }

// Close releases the database handle.
func (r *SQLiteReader) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

// Source implements literature.ZoteroReader.
func (r *SQLiteReader) Source() literature.MetadataSource { return literature.SourceSQLite }

// resolver returns the shared attachment resolver for this reader.
func (r *SQLiteReader) resolver() Resolver {
	return Resolver{DataDir: r.dataDir, LinkedBaseDir: r.LinkedBaseDir}
}

// ListLibraries returns only the personal library (SPEC-04 §5.2, O-05).
func (r *SQLiteReader) ListLibraries(ctx context.Context) ([]literature.ZoteroLibrary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []literature.ZoteroLibrary{{ID: "0", Name: "我的文库"}}, nil
}

// ListCollections returns collections with their top-level item counts.
func (r *SQLiteReader) ListCollections(ctx context.Context, libraryID string) ([]literature.ZoteroCollection, error) {
	lib, err := libraryNumber(libraryID)
	if err != nil {
		return nil, err
	}
	return retryOnBusy(ctx, func() ([]literature.ZoteroCollection, error) {
		const query = `
SELECT c.collectionID, c.key, c.collectionName, COALESCE(pc.key, ''), c.libraryID,
       (SELECT COUNT(*)
          FROM collectionItems ci
          JOIN items i ON i.itemID = ci.itemID
          JOIN itemTypes it ON it.itemTypeID = i.itemTypeID
          LEFT JOIN deletedItems d ON d.itemID = i.itemID
         WHERE ci.collectionID = c.collectionID
           AND d.itemID IS NULL
           AND it.typeName NOT IN ('attachment', 'note'))
  FROM collections c
  LEFT JOIN collections pc ON pc.collectionID = c.parentCollectionID
 WHERE c.libraryID = ?
 ORDER BY c.collectionName, c.key`
		rows, err := r.db.QueryContext(ctx, query, lib)
		if err != nil {
			return nil, fmt.Errorf("zotero: 读取 collections: %w", err)
		}
		defer rows.Close()

		var out []literature.ZoteroCollection
		for rows.Next() {
			var (
				id        int64
				key       string
				name      string
				parent    string
				library   int64
				itemCount int
			)
			if err := rows.Scan(&id, &key, &name, &parent, &library, &itemCount); err != nil {
				return nil, fmt.Errorf("zotero: 读取 collections: %w", err)
			}
			out = append(out, literature.ZoteroCollection{
				Key:       key,
				ParentKey: parent,
				Name:      name,
				ItemCount: itemCount,
			})
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("zotero: 读取 collections: %w", err)
		}
		return out, nil
	})
}

// ListItems returns one page of top-level bibliographic items, excluding
// deleted items, notes and attachments.
func (r *SQLiteReader) ListItems(ctx context.Context, libraryID, collectionKey string, opts literature.ListItemsOptions) (literature.ItemPage, error) {
	lib, err := libraryNumber(libraryID)
	if err != nil {
		return literature.ItemPage{}, err
	}
	limit := opts.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}
	return retryOnBusy(ctx, func() (literature.ItemPage, error) {
		return r.listItemsOnce(ctx, lib, collectionKey, opts.Query, limit, offset)
	})
}

func (r *SQLiteReader) listItemsOnce(ctx context.Context, library int64, collectionKey, query string, limit, offset int) (literature.ItemPage, error) {
	var (
		conditions = []string{
			"i.libraryID = ?",
			"d.itemID IS NULL",
			"it.typeName NOT IN ('attachment', 'note')",
		}
		args = []any{library}
	)
	if key := strings.TrimSpace(collectionKey); key != "" {
		conditions = append(conditions,
			`i.itemID IN (SELECT ci.itemID
			                FROM collectionItems ci
			                JOIN collections c ON c.collectionID = ci.collectionID
			               WHERE c.key = ?)`)
		args = append(args, key)
	}
	if q := strings.TrimSpace(query); q != "" {
		conditions = append(conditions,
			`i.itemID IN (SELECT idv.itemID
			                FROM itemData idv
			                JOIN fields f ON f.fieldID = idv.fieldID
			                JOIN itemDataValues v ON v.valueID = idv.valueID
			               WHERE f.fieldName = 'title'
			                 AND instr(lower(v.value), lower(?)) > 0)`)
		args = append(args, q)
	}
	where := " WHERE " + strings.Join(conditions, " AND ")

	countQuery := `SELECT COUNT(*)
	                 FROM items i
	                 JOIN itemTypes it ON it.itemTypeID = i.itemTypeID
	                 LEFT JOIN deletedItems d ON d.itemID = i.itemID` + where
	var total int
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return literature.ItemPage{}, fmt.Errorf("zotero: 统计条目: %w", err)
	}

	pageQuery := `SELECT i.itemID, i.key, i.version, it.typeName
	                FROM items i
	                JOIN itemTypes it ON it.itemTypeID = i.itemTypeID
	                LEFT JOIN deletedItems d ON d.itemID = i.itemID` + where + `
	               ORDER BY i.itemID
	               LIMIT ? OFFSET ?`
	rows, err := r.db.QueryContext(ctx, pageQuery, append(args, limit, offset)...)
	if err != nil {
		return literature.ItemPage{}, fmt.Errorf("zotero: 读取条目: %w", err)
	}
	defer rows.Close()

	type rowInfo struct {
		id       int64
		key      string
		version  int
		itemType string
	}
	var rowsOut []rowInfo
	for rows.Next() {
		var info rowInfo
		if err := rows.Scan(&info.id, &info.key, &info.version, &info.itemType); err != nil {
			return literature.ItemPage{}, fmt.Errorf("zotero: 读取条目: %w", err)
		}
		rowsOut = append(rowsOut, info)
	}
	if err := rows.Err(); err != nil {
		return literature.ItemPage{}, fmt.Errorf("zotero: 读取条目: %w", err)
	}

	ids := make([]int64, len(rowsOut))
	for i, info := range rowsOut {
		ids[i] = info.id
	}
	items, err := r.loadItems(ctx, ids)
	if err != nil {
		return literature.ItemPage{}, err
	}
	page := literature.ItemPage{Total: total, SourceUsed: literature.SourceSQLite, Items: make([]literature.ZoteroItem, 0, len(rowsOut))}
	for _, info := range rowsOut {
		item := items[info.id]
		item.Key = info.key
		item.Version = info.version
		item.ItemType = info.itemType
		item.LibraryID = strconv.FormatInt(library, 10)
		page.Items = append(page.Items, item)
	}
	return page, nil
}

// GetItem returns one non-deleted bibliographic item by key.
func (r *SQLiteReader) GetItem(ctx context.Context, libraryID, key string) (literature.ZoteroItem, error) {
	lib, err := libraryNumber(libraryID)
	if err != nil {
		return literature.ZoteroItem{}, err
	}
	return retryOnBusy(ctx, func() (literature.ZoteroItem, error) {
		const query = `
SELECT i.itemID, i.key, i.version, it.typeName
  FROM items i
  JOIN itemTypes it ON it.itemTypeID = i.itemTypeID
  LEFT JOIN deletedItems d ON d.itemID = i.itemID
 WHERE i.key = ? AND i.libraryID = ? AND d.itemID IS NULL
   AND it.typeName NOT IN ('attachment', 'note')
 LIMIT 1`
		var (
			id      int64
			itemKey string
			version int
			itemTyp string
		)
		err := r.db.QueryRowContext(ctx, query, key, lib).Scan(&id, &itemKey, &version, &itemTyp)
		if errors.Is(err, sql.ErrNoRows) {
			return literature.ZoteroItem{}, literature.ErrItemNotFound
		}
		if err != nil {
			return literature.ZoteroItem{}, fmt.Errorf("zotero: 读取条目 %s: %w", key, err)
		}
		items, err := r.loadItems(ctx, []int64{id})
		if err != nil {
			return literature.ZoteroItem{}, err
		}
		item := items[id]
		item.Key = itemKey
		item.Version = version
		item.ItemType = itemTyp
		item.LibraryID = strconv.FormatInt(lib, 10)
		return item, nil
	})
}

// ListAttachments returns every attachment of one item, including linked and
// non-PDF attachments. Callers filter for PDFs.
func (r *SQLiteReader) ListAttachments(ctx context.Context, libraryID, parentKey string) ([]literature.ZoteroAttachment, error) {
	lib, err := libraryNumber(libraryID)
	if err != nil {
		return nil, err
	}
	return retryOnBusy(ctx, func() ([]literature.ZoteroAttachment, error) {
		var parentID int64
		err := r.db.QueryRowContext(ctx,
			`SELECT itemID FROM items WHERE key = ? AND libraryID = ? LIMIT 1`, parentKey, lib).Scan(&parentID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, literature.ErrItemNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("zotero: 读取条目 %s: %w", parentKey, err)
		}

		const query = `
SELECT i.itemID, i.key, i.libraryID, ia.parentItemID,
       COALESCE(ia.linkMode, 0), COALESCE(ia.contentType, ''), COALESCE(ia.path, '')
  FROM itemAttachments ia
  JOIN items i ON i.itemID = ia.itemID
  LEFT JOIN deletedItems d ON d.itemID = i.itemID
 WHERE ia.parentItemID = ? AND d.itemID IS NULL
 ORDER BY i.itemID`
		rows, err := r.db.QueryContext(ctx, query, parentID)
		if err != nil {
			return nil, fmt.Errorf("zotero: 读取附件: %w", err)
		}
		defer rows.Close()

		res := r.resolver()
		var out []literature.ZoteroAttachment
		for rows.Next() {
			var (
				itemID      int64
				key         string
				library     int64
				parent      int64
				linkMode    int
				contentType string
				path        string
			)
			if err := rows.Scan(&itemID, &key, &library, &parent, &linkMode, &contentType, &path); err != nil {
				return nil, fmt.Errorf("zotero: 读取附件: %w", err)
			}
			att := literature.ZoteroAttachment{
				Key:         key,
				ParentKey:   parentKey,
				LibraryID:   strconv.FormatInt(library, 10),
				Filename:    attachmentFilename(path),
				ContentType: contentType,
				LinkMode:    linkModeName(linkMode),
				Path:        path,
			}
			if resolved, err := res.Resolve(att); err == nil {
				att.Size = resolved.Size
			}
			out = append(out, att)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("zotero: 读取附件: %w", err)
		}
		return out, nil
	})
}

// ResolveAttachment maps an attachment record to a readable local file.
func (r *SQLiteReader) ResolveAttachment(ctx context.Context, att literature.ZoteroAttachment) (literature.ResolvedFile, error) {
	if err := ctx.Err(); err != nil {
		return literature.ResolvedFile{}, err
	}
	return r.resolver().Resolve(att)
}

// loadItems batch-loads itemData, creators, tags and collections for ids to
// avoid N+1 queries. The returned map is keyed by itemID.
func (r *SQLiteReader) loadItems(ctx context.Context, ids []int64) (map[int64]literature.ZoteroItem, error) {
	out := make(map[int64]literature.ZoteroItem, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	for _, id := range ids {
		out[id] = literature.ZoteroItem{}
	}
	placeholders, idArgs := inPlaceholders(ids)

	// itemData: every field of the page. Known §4.3 fields are mapped onto the
	// typed item; everything else stays verbatim in Raw so unknown Zotero
	// fields survive (SPEC-04 §4.3).
	dataQuery := `
SELECT id.itemID, f.fieldName, v.value
  FROM itemData id
  JOIN fields f ON f.fieldID = id.fieldID
  JOIN itemDataValues v ON v.valueID = id.valueID
 WHERE id.itemID IN (` + placeholders + `)`
	rows, err := r.db.QueryContext(ctx, dataQuery, idArgs...)
	if err != nil {
		return nil, fmt.Errorf("zotero: 读取字段: %w", err)
	}
	for rows.Next() {
		var (
			id    int64
			field string
			value string
		)
		if err := rows.Scan(&id, &field, &value); err != nil {
			rows.Close()
			return nil, fmt.Errorf("zotero: 读取字段: %w", err)
		}
		item := out[id]
		if item.Raw == nil {
			item.Raw = make(map[string]any)
		}
		item.Raw[field] = value
		switch field {
		case "title":
			item.Title = value
		case "date":
			item.Date = value
		case "DOI":
			item.DOI = value
		case "url":
			item.URL = value
		case "abstractNote":
			item.AbstractNote = value
		case "publicationTitle":
			item.PublicationTitle = value
		case "language":
			item.Language = value
		case "extra":
			item.Extra = value
		}
		out[id] = item
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("zotero: 读取字段: %w", err)
	}
	rows.Close()

	creatorQuery := `
SELECT ic.itemID, c.firstName, c.lastName, c.fieldMode, ct.creatorType
  FROM itemCreators ic
  JOIN creators c ON c.creatorID = ic.creatorID
  JOIN creatorTypes ct ON ct.creatorTypeID = ic.creatorTypeID
 WHERE ic.itemID IN (` + placeholders + `)
 ORDER BY ic.itemID, ic.orderIndex`
	rows, err = r.db.QueryContext(ctx, creatorQuery, idArgs...)
	if err != nil {
		return nil, fmt.Errorf("zotero: 读取作者: %w", err)
	}
	for rows.Next() {
		var (
			id         int64
			firstName  string
			lastName   string
			fieldMode  int
			creatorTyp string
		)
		if err := rows.Scan(&id, &firstName, &lastName, &fieldMode, &creatorTyp); err != nil {
			rows.Close()
			return nil, fmt.Errorf("zotero: 读取作者: %w", err)
		}
		item := out[id]
		creator := literature.ZoteroCreator{CreatorType: creatorTyp}
		if fieldMode == 1 {
			creator.Name = lastName
		} else {
			creator.FirstName = firstName
			creator.LastName = lastName
		}
		item.Creators = append(item.Creators, creator)
		out[id] = item
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("zotero: 读取作者: %w", err)
	}
	rows.Close()

	tagQuery := `
SELECT it.itemID, t.name, it.type
  FROM itemTags it
  JOIN tags t ON t.tagID = it.tagID
 WHERE it.itemID IN (` + placeholders + `)
 ORDER BY it.itemID, t.name`
	rows, err = r.db.QueryContext(ctx, tagQuery, idArgs...)
	if err != nil {
		return nil, fmt.Errorf("zotero: 读取标签: %w", err)
	}
	tagTypes := make(map[int64][]literature.ZoteroTag)
	for rows.Next() {
		var (
			id      int64
			name    string
			tagType int
		)
		if err := rows.Scan(&id, &name, &tagType); err != nil {
			rows.Close()
			return nil, fmt.Errorf("zotero: 读取标签: %w", err)
		}
		item := out[id]
		item.Tags = append(item.Tags, name)
		out[id] = item
		tagTypes[id] = append(tagTypes[id], literature.ZoteroTag{Tag: name, Type: tagType})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("zotero: 读取标签: %w", err)
	}
	rows.Close()

	for id, tags := range tagTypes {
		raw := make([]any, 0, len(tags))
		for _, tag := range tags {
			raw = append(raw, map[string]any{"tag": tag.Tag, "type": tag.Type})
		}
		item := out[id]
		if item.Raw == nil {
			item.Raw = make(map[string]any)
		}
		item.Raw["tags"] = raw
		out[id] = item
	}

	collectionQuery := `
SELECT ci.itemID, c.key
  FROM collectionItems ci
  JOIN collections c ON c.collectionID = ci.collectionID
 WHERE ci.itemID IN (` + placeholders + `)
 ORDER BY ci.itemID, c.key`
	rows, err = r.db.QueryContext(ctx, collectionQuery, idArgs...)
	if err != nil {
		return nil, fmt.Errorf("zotero: 读取 collection 归属: %w", err)
	}
	for rows.Next() {
		var (
			id  int64
			key string
		)
		if err := rows.Scan(&id, &key); err != nil {
			rows.Close()
			return nil, fmt.Errorf("zotero: 读取 collection 归属: %w", err)
		}
		item := out[id]
		item.Collections = append(item.Collections, key)
		out[id] = item
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("zotero: 读取 collection 归属: %w", err)
	}
	rows.Close()

	// Zotero's raw item object carries collection keys; mirror them in Raw
	// without disturbing the other preserved fields.
	for id, item := range out {
		if len(item.Collections) == 0 {
			continue
		}
		if item.Raw == nil {
			item.Raw = make(map[string]any)
		}
		keys := make([]any, 0, len(item.Collections))
		for _, key := range item.Collections {
			keys = append(keys, key)
		}
		item.Raw["collections"] = keys
		out[id] = item
	}

	return out, nil
}

func inPlaceholders(ids []int64) (string, []any) {
	args := make([]any, len(ids))
	marks := make([]string, len(ids))
	for i, id := range ids {
		marks[i] = "?"
		args[i] = id
	}
	return strings.Join(marks, ","), args
}

func libraryNumber(libraryID string) (int64, error) {
	id := strings.TrimSpace(libraryID)
	if id == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("zotero: 非法的 library ID %q", libraryID)
	}
	return n, nil
}

// attachmentFilename extracts the display filename from a Zotero path value.
func attachmentFilename(path string) string {
	p := strings.TrimSpace(path)
	lower := strings.ToLower(p)
	switch {
	case strings.HasPrefix(lower, "storage:"):
		p = p[len("storage:"):]
	case strings.HasPrefix(lower, "attachments:"):
		p = p[len("attachments:"):]
	}
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return filepath.Base(filepath.FromSlash(p))
}
