// Package papers is the minimal store over global.db.papers (SPEC-01 §5.13). It
// never writes Activity events: paper.* events belong to SPEC-04 and are written
// into the project database.
package papers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/schema"
)

// Paper is one row of global.db.papers.
type Paper struct {
	ID              string
	ZoteroLibraryID string
	ZoteroKey       string
	AttachmentKey   string
	Metadata        json.RawMessage
	FilePath        string
	FileHash        string
	FileSize        int64
	FileState       schema.PaperFileState
	SourceState     schema.PaperSourceState
	LastSyncedAt    *time.Time
	ImportedAt      time.Time
	UpdatedAt       time.Time
}

// PaperQuery filters List. Zero values mean "no filter"; TitleLike matches the
// Zotero metadata title.
type PaperQuery struct {
	ZoteroLibraryID string
	ZoteroKey       string
	TitleLike       string
	FileState       *schema.PaperFileState
	ImportedFrom    *time.Time
	ImportedTo      *time.Time
	Limit           int
	Offset          int
}

// Store reads and writes global.db.papers.
type Store struct {
	db    *sql.DB
	clock clock.Clock
}

// New binds a store to the global database. A nil clock means clock.System.
func New(db *sql.DB, c clock.Clock) *Store {
	if c == nil {
		c = clock.System{}
	}
	return &Store{db: db, clock: c}
}

// Upsert deduplicates by (zotero_library_id, zotero_key). An existing row is
// returned unchanged (in particular file_path/file_hash are never overwritten;
// PDF replacement goes through SPEC-04's hash comparison + user confirmation).
// A new row is inserted and created=true is returned.
func (s *Store) Upsert(ctx context.Context, tx *sql.Tx, p Paper) (string, bool, error) {
	if p.ZoteroKey == "" {
		return "", false, fmt.Errorf("papers: upsert: empty zotero key")
	}
	if p.ZoteroLibraryID == "" {
		p.ZoteroLibraryID = "0"
	}
	metadata := p.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	if !json.Valid(metadata) {
		return "", false, fmt.Errorf("papers: upsert: metadata is not valid JSON")
	}
	fileState := p.FileState
	if fileState == "" {
		fileState = schema.PaperFileOK
	}
	sourceState := p.SourceState
	if sourceState == "" {
		sourceState = schema.PaperSourceOK
	}
	newID, err := ids.New(s.clock)
	if err != nil {
		return "", false, fmt.Errorf("papers: generate id: %w", err)
	}
	now := clock.Format(s.clock.Now())
	var filePath, fileHash, fileSize, attachmentKey any
	if p.FilePath != "" {
		filePath = p.FilePath
	}
	if p.FileHash != "" {
		fileHash = p.FileHash
	}
	if p.FileSize != 0 {
		fileSize = p.FileSize
	}
	if p.AttachmentKey != "" {
		attachmentKey = p.AttachmentKey
	}

	res, err := s.exec(ctx, tx,
		`INSERT INTO papers(id, zotero_library_id, zotero_key, attachment_key, metadata,
		                    file_path, file_hash, file_size, file_state, source_state,
		                    last_synced_at, imported_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)
		 ON CONFLICT(zotero_library_id, zotero_key) DO NOTHING`,
		newID, p.ZoteroLibraryID, p.ZoteroKey, attachmentKey, string(metadata),
		filePath, fileHash, fileSize, string(fileState), string(sourceState), now, now)
	if err != nil {
		return "", false, fmt.Errorf("papers: upsert: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return "", false, fmt.Errorf("papers: upsert: %w", err)
	} else if n == 1 {
		return newID, true, nil
	}
	id, err := s.existingID(ctx, tx, p.ZoteroLibraryID, p.ZoteroKey)
	if err != nil {
		return "", false, err
	}
	return id, false, nil
}

func (s *Store) existingID(ctx context.Context, tx *sql.Tx, libraryID, zoteroKey string) (string, error) {
	query := `SELECT id FROM papers WHERE zotero_library_id = ? AND zotero_key = ?`
	var (
		id  string
		err error
	)
	if tx != nil {
		err = tx.QueryRowContext(ctx, query, libraryID, zoteroKey).Scan(&id)
	} else {
		err = s.db.QueryRowContext(ctx, query, libraryID, zoteroKey).Scan(&id)
	}
	if err != nil {
		return "", fmt.Errorf("papers: find identity (%s, %s): %w", libraryID, zoteroKey, err)
	}
	return id, nil
}

// Get returns one paper. A missing row surfaces sql.ErrNoRows.
func (s *Store) Get(ctx context.Context, id string) (Paper, error) {
	row := s.db.QueryRowContext(ctx, selectColumns+` FROM papers WHERE id = ?`, id)
	p, err := scanPaper(row)
	if err != nil {
		return Paper{}, fmt.Errorf("papers: get %s: %w", id, err)
	}
	return p, nil
}

// ByZotero looks a paper up by its Zotero identity.
func (s *Store) ByZotero(ctx context.Context, libraryID, zoteroKey string) (Paper, bool, error) {
	row := s.db.QueryRowContext(ctx,
		selectColumns+` FROM papers WHERE zotero_library_id = ? AND zotero_key = ?`,
		libraryID, zoteroKey)
	p, err := scanPaper(row)
	if err == sql.ErrNoRows {
		return Paper{}, false, nil
	}
	if err != nil {
		return Paper{}, false, fmt.Errorf("papers: by zotero (%s, %s): %w", libraryID, zoteroKey, err)
	}
	return p, true, nil
}

// List returns papers matching q, newest import first.
func (s *Store) List(ctx context.Context, q PaperQuery) ([]Paper, error) {
	var where []string
	var args []any
	if q.ZoteroLibraryID != "" {
		where = append(where, "zotero_library_id = ?")
		args = append(args, q.ZoteroLibraryID)
	}
	if q.ZoteroKey != "" {
		where = append(where, "zotero_key = ?")
		args = append(args, q.ZoteroKey)
	}
	if q.TitleLike != "" {
		where = append(where, `json_extract(metadata, '$.title') LIKE ?`)
		args = append(args, "%"+q.TitleLike+"%")
	}
	if q.FileState != nil {
		where = append(where, "file_state = ?")
		args = append(args, string(*q.FileState))
	}
	if q.ImportedFrom != nil {
		where = append(where, "imported_at >= ?")
		args = append(args, clock.Format(*q.ImportedFrom))
	}
	if q.ImportedTo != nil {
		where = append(where, "imported_at <= ?")
		args = append(args, clock.Format(*q.ImportedTo))
	}
	query := selectColumns + ` FROM papers`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, " AND ")
	}
	query += ` ORDER BY imported_at DESC, id`
	if q.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, q.Limit)
		if q.Offset > 0 {
			query += ` OFFSET ?`
			args = append(args, q.Offset)
		}
	} else if q.Offset > 0 {
		query += ` LIMIT -1 OFFSET ?`
		args = append(args, q.Offset)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("papers: list: %w", err)
	}
	defer rows.Close()
	var out []Paper
	for rows.Next() {
		p, err := scanPaper(rows)
		if err != nil {
			return nil, fmt.Errorf("papers: list: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("papers: list: %w", err)
	}
	return out, nil
}

// UpdateFile records the local PDF copy metadata. A missing row surfaces
// sql.ErrNoRows.
func (s *Store) UpdateFile(ctx context.Context, tx *sql.Tx, id, relPath, hash string, size int64, state schema.PaperFileState) error {
	if !state.Valid() {
		return fmt.Errorf("papers: update file %s: invalid file state %q", id, state)
	}
	var path, fileHash any
	if relPath != "" {
		path = relPath
	}
	if hash != "" {
		fileHash = hash
	}
	var sizeVal any
	if size != 0 {
		sizeVal = size
	}
	res, err := s.exec(ctx, tx,
		`UPDATE papers SET file_path = ?, file_hash = ?, file_size = ?, file_state = ?, updated_at = ? WHERE id = ?`,
		path, fileHash, sizeVal, string(state), clock.Format(s.clock.Now()), id)
	if err != nil {
		return fmt.Errorf("papers: update file %s: %w", id, err)
	}
	return requireOneRow(res, "update file", id)
}

// Delete removes a paper row. Only the copy GC (§5.9) calls this.
func (s *Store) Delete(ctx context.Context, tx *sql.Tx, id string) error {
	res, err := s.exec(ctx, tx, `DELETE FROM papers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("papers: delete %s: %w", id, err)
	}
	return requireOneRow(res, "delete", id)
}

const selectColumns = `SELECT id, zotero_library_id, zotero_key, attachment_key, metadata,
	file_path, file_hash, file_size, file_state, source_state, last_synced_at,
	imported_at, updated_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanPaper(row rowScanner) (Paper, error) {
	var (
		p             Paper
		attachmentKey sql.NullString
		metadata      string
		filePath      sql.NullString
		fileHash      sql.NullString
		fileSize      sql.NullInt64
		fileState     string
		sourceState   string
		lastSyncedAt  sql.NullString
		importedAt    string
		updatedAt     string
	)
	if err := row.Scan(&p.ID, &p.ZoteroLibraryID, &p.ZoteroKey, &attachmentKey, &metadata,
		&filePath, &fileHash, &fileSize, &fileState, &sourceState, &lastSyncedAt,
		&importedAt, &updatedAt); err != nil {
		return Paper{}, err
	}
	p.AttachmentKey = attachmentKey.String
	p.Metadata = json.RawMessage(metadata)
	p.FilePath = filePath.String
	p.FileHash = fileHash.String
	p.FileSize = fileSize.Int64
	p.FileState = schema.PaperFileState(fileState)
	p.SourceState = schema.PaperSourceState(sourceState)
	var err error
	if p.ImportedAt, err = clock.Parse(importedAt); err != nil {
		return Paper{}, fmt.Errorf("imported_at: %w", err)
	}
	if p.UpdatedAt, err = clock.Parse(updatedAt); err != nil {
		return Paper{}, fmt.Errorf("updated_at: %w", err)
	}
	if lastSyncedAt.Valid {
		t, err := clock.Parse(lastSyncedAt.String)
		if err != nil {
			return Paper{}, fmt.Errorf("last_synced_at: %w", err)
		}
		p.LastSyncedAt = &t
	}
	return p, nil
}

func (s *Store) exec(ctx context.Context, tx *sql.Tx, query string, args ...any) (sql.Result, error) {
	if tx != nil {
		return tx.ExecContext(ctx, query, args...)
	}
	return s.db.ExecContext(ctx, query, args...)
}

func requireOneRow(res sql.Result, op, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("papers: %s %s: %w", op, id, err)
	}
	if n == 0 {
		return fmt.Errorf("papers: %s %s: %w", op, id, sql.ErrNoRows)
	}
	return nil
}
