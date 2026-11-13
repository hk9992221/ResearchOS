package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"researchos/internal/foundation/clock"
)

// keyword is the SQLite KeywordIndex. Matching uses FTS5 trigram (O-02
// spike); queries whose every term is shorter than 3 runes fall back to a
// LIKE substring scan so two-character CJK terms still retrieve.
type keyword struct {
	db        *sql.DB
	clock     clock.Clock
	tb        tables
	partition Partition
}

var _ KeywordIndex = (*keyword)(nil)

// NewKeywordIndex returns the keyword primitive for one partition database.
func NewKeywordIndex(db *sql.DB, partition Partition, c clock.Clock) (KeywordIndex, error) {
	if db == nil {
		return nil, errors.New("index/store: nil database")
	}
	return &keyword{db: db, clock: clockOrDefault(c), tb: partition.tables(), partition: partition}, nil
}

// ReplaceDoc atomically replaces the doc row, chunks and FTS rows for one
// document. Vectors under any profile are untouched (separate primitive).
func (k *keyword) ReplaceDoc(ctx context.Context, doc IndexDoc, chunks []IndexChunk) error {
	if err := validateReplace(doc, chunks); err != nil {
		return err
	}
	now := clock.Format(k.clock.Now())

	return k.inTx(ctx, func(tx *sql.Tx) error {
		// Remove the previous projection first: the doc row is replaced
		// afterwards, and chunk inserts need the FK parent to exist at the
		// end state, not the interleaved one.
		if err := k.purgeDocChunks(ctx, tx, doc.DocID); err != nil {
			return err
		}
		if err := k.upsertDoc(ctx, tx, doc, now); err != nil {
			return err
		}
		for _, ch := range chunks {
			locator := string(ch.LocatorJSON)
			if len(ch.LocatorJSON) == 0 {
				locator = ""
			}
			if _, err := tx.ExecContext(ctx,
				fmt.Sprintf(`INSERT INTO %s (chunk_id, doc_id, ordinal, text, locator_json, content_hash)
					VALUES (?, ?, ?, ?, ?, ?)`, k.tb.chunks),
				ch.ChunkID, ch.DocID, ch.Ordinal, ch.Text, locator, ch.ContentHash); err != nil {
				return fmt.Errorf("index/store: insert chunk %s: %w", ch.ChunkID, err)
			}
			var rowid int64
			if err := tx.QueryRowContext(ctx,
				fmt.Sprintf(`SELECT rowid FROM %s WHERE chunk_id = ?`, k.tb.chunks),
				ch.ChunkID).Scan(&rowid); err != nil {
				return fmt.Errorf("index/store: chunk rowid: %w", err)
			}
			if _, err := tx.ExecContext(ctx,
				fmt.Sprintf(`INSERT INTO %s (rowid, chunk_id, body) VALUES (?, ?, ?)`, k.tb.fts),
				rowid, ch.ChunkID, ch.Text); err != nil {
				return fmt.Errorf("index/store: insert fts %s: %w", ch.ChunkID, err)
			}
		}
		return nil
	})
}

// DeleteDoc removes the document, its chunks and FTS rows; vector rows for
// the chunks disappear through the FK cascade. Idempotent (§5.10.3 rule 2).
func (k *keyword) DeleteDoc(ctx context.Context, projectID, docID string) error {
	if docID == "" {
		return ErrEmptyDocID
	}
	if k.tb.hasProject {
		var exists string
		err := k.db.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT doc_id FROM %s WHERE doc_id = ? AND project_id = ?`, k.tb.docs),
			docID, projectID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("index/store: doc lookup: %w", err)
		}
	}
	return k.inTx(ctx, func(tx *sql.Tx) error {
		return k.purgeDocChunks(ctx, tx, docID)
	})
}

// purgeDocChunks deletes FTS rows, chunks and the doc row in one tx. The
// vectors FK cascades keep profile data consistent (§5.10.3 rule 2).
func (k *keyword) purgeDocChunks(ctx context.Context, tx *sql.Tx, docID string) error {
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM %s WHERE chunk_id IN (SELECT chunk_id FROM %s WHERE doc_id = ?)`,
			k.tb.fts, k.tb.chunks), docID); err != nil {
		return fmt.Errorf("index/store: delete fts: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM %s WHERE doc_id = ?`, k.tb.chunks), docID); err != nil {
		return fmt.Errorf("index/store: delete chunks: %w", err)
	}
	_, err := tx.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM %s WHERE doc_id = ?`, k.tb.docs), docID)
	return err
}

func (k *keyword) upsertDoc(ctx context.Context, tx *sql.Tx, doc IndexDoc, now string) error {
	if k.tb.hasProject {
		_, err := tx.ExecContext(ctx,
			fmt.Sprintf(`INSERT INTO %s (doc_id, project_id, source, entity_type, entity_id, title, content_hash, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(doc_id) DO UPDATE SET
					project_id = excluded.project_id, source = excluded.source,
					entity_type = excluded.entity_type, entity_id = excluded.entity_id,
					title = excluded.title, content_hash = excluded.content_hash,
					updated_at = excluded.updated_at`, k.tb.docs),
			doc.DocID, doc.ProjectID, doc.Source, doc.EntityType, doc.EntityID, doc.Title, doc.ContentHash, now)
		return err
	}
	_, err := tx.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO %s (doc_id, source, entity_type, entity_id, title, content_hash, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(doc_id) DO UPDATE SET
				source = excluded.source, entity_type = excluded.entity_type,
				entity_id = excluded.entity_id, title = excluded.title,
				content_hash = excluded.content_hash, updated_at = excluded.updated_at`, k.tb.docs),
		doc.DocID, doc.Source, doc.EntityType, doc.EntityID, doc.Title, doc.ContentHash, now)
	return err
}

// Query ranks chunks with bm25; Score is -bm25 so higher sorts first.
func (k *keyword) Query(ctx context.Context, projectID, text string, topK int, filter IndexFilter) ([]ChunkHit, error) {
	if topK <= 0 {
		topK = 50
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, nil
	}
	tokens := ftsTokens(trimmed)
	if len(tokens) > 0 {
		return k.queryFTS(ctx, projectID, tokens, topK, filter)
	}
	return k.queryLike(ctx, projectID, trimmed, topK, filter)
}

func (k *keyword) queryFTS(ctx context.Context, projectID string, tokens []string, topK int, filter IndexFilter) ([]ChunkHit, error) {
	match := strings.Join(quoteFTS(tokens), " OR ")
	q := fmt.Sprintf(
		`SELECT c.chunk_id, c.doc_id, %s, c.text, c.locator_json, -bm25(%s) AS score
		 FROM %s f
		 JOIN %s c ON c.rowid = f.rowid
		 JOIN %s d ON d.doc_id = c.doc_id
		 WHERE %s MATCH ?%s%s
		 ORDER BY bm25(%s), c.doc_id, c.ordinal
		 LIMIT ?`,
		k.projectSel(), k.tb.fts, k.tb.fts, k.tb.chunks, k.tb.docs, k.tb.fts,
		k.projectCond(), filterCond(filter), k.tb.fts)
	args := []any{match}
	if k.tb.hasProject {
		args = append(args, projectID)
	}
	args = append(args, filterArgs(filter)...)
	args = append(args, topK)
	return k.collect(ctx, q, args, tokens)
}

func (k *keyword) queryLike(ctx context.Context, projectID, text string, topK int, filter IndexFilter) ([]ChunkHit, error) {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text)
	q := fmt.Sprintf(
		`SELECT c.chunk_id, c.doc_id, %s, c.text, c.locator_json, 0.0 AS score
		 FROM %s c
		 JOIN %s d ON d.doc_id = c.doc_id
		 WHERE c.text LIKE ? ESCAPE '\'%s%s
		 ORDER BY c.doc_id, c.ordinal
		 LIMIT ?`,
		k.projectSel(), k.tb.chunks, k.tb.docs, k.projectCond(), filterCond(filter))
	args := []any{"%" + escaped + "%"}
	if k.tb.hasProject {
		args = append(args, projectID)
	}
	args = append(args, filterArgs(filter)...)
	args = append(args, topK)
	return k.collect(ctx, q, args, []string{strings.ToLower(text)})
}

func (k *keyword) projectSel() string {
	if k.tb.hasProject {
		return "d.project_id"
	}
	return "''"
}

func (k *keyword) projectCond() string {
	if k.tb.hasProject {
		return " AND d.project_id = ?"
	}
	return ""
}

func (k *keyword) collect(ctx context.Context, q string, args []any, terms []string) ([]ChunkHit, error) {
	rows, err := k.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("index/store: query: %w", err)
	}
	defer rows.Close()
	var out []ChunkHit
	for rows.Next() {
		var (
			h       ChunkHit
			locator string
		)
		if err := rows.Scan(&h.ChunkID, &h.DocID, &h.ProjectID, &h.Text, &locator, &h.Score); err != nil {
			return nil, fmt.Errorf("index/store: scan hit: %w", err)
		}
		if locator != "" {
			h.LocatorJSON = json.RawMessage(locator)
		}
		h.MatchedTerms = terms
		out = append(out, h)
	}
	return out, rows.Err()
}

func (k *keyword) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := k.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("index/store: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ftsTokens splits on non-alphanumeric runes and keeps terms long enough
// for trigram matching (≥3 runes). Case is folded for the quoted phrases.
func ftsTokens(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r))
	})
	var out []string
	seen := map[string]bool{}
	for _, f := range fields {
		if len([]rune(f)) < 3 {
			continue
		}
		lower := strings.ToLower(f)
		if !seen[lower] {
			seen[lower] = true
			out = append(out, lower)
		}
	}
	return out
}

// quoteFTS wraps every term as an FTS5 phrase, doubling embedded quotes.
func quoteFTS(tokens []string) []string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		out[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	return out
}

func filterCond(f IndexFilter) string {
	var b strings.Builder
	if len(f.DocIDs) > 0 {
		b.WriteString(" AND d.doc_id IN (" + placeholders(len(f.DocIDs)) + ")")
	}
	if len(f.ExcludeDocIDs) > 0 {
		b.WriteString(" AND d.doc_id NOT IN (" + placeholders(len(f.ExcludeDocIDs)) + ")")
	}
	return b.String()
}

func filterArgs(f IndexFilter) []any {
	var out []any
	for _, id := range f.DocIDs {
		out = append(out, id)
	}
	for _, id := range f.ExcludeDocIDs {
		out = append(out, id)
	}
	return out
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
