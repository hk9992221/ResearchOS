package search

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/workspace"
)

// Parser versions written to index_docs.parser_version. A bump marks existing
// documents stale and schedules a background reparse (SPEC-10 §5.3).
const (
	parserVersionMarkdown = "md-1"
	parserVersionPDF      = "pdf-1"
	schemaVersion         = 1
)

// extractProblem is one source document that could not be (re)extracted but
// must keep its previous index entry (SPEC-10 §3.4: a missing Markdown file
// marks the document index_stale instead of deleting it silently).
type extractProblem struct {
	DocID string
	Err   error
}

// extractor reads the eight search sources and turns them into IndexDocs.
//
// SPEC-10 §2.1 requires reading entity modules without writing them. The
// projections below are read-only SELECTs over the frozen SPEC-01 v1.1 schema
// (plus the SPEC-04/05/06/07 migration columns); the adapters keep the exact
// field authority of the module services (notes.Service.Document, ideas,
// experiments snapshots). They are deliberately direct SQL because the
// services expose page-oriented APIs (ListNotes/ListRecords) that would make a
// whole-project rebuild N+1 and would filter out soft-deleted rows the index
// must still be able to mark. Note bodies and PDFs are read from the file
// system, which is their authority (SPEC-00 §1).
type extractor struct {
	ws    *workspace.Workspace
	clock clock.Clock
	parse func(ctx context.Context, absPath string, opts ParseOptions) (PdfDocument, error)
}

// extractOptions controls one extraction.
type extractOptions struct {
	// ReparsePDF ignores the stored file hash and re-parses the PDF (SPEC-10
	// §5.3 ReindexRequest.ReparsePDF).
	ReparsePDF bool
}

// entityDocs extracts every document of one entity. A soft-deleted entity
// yields no documents (the caller records a delete change).
func (e *extractor) entityDocs(ctx context.Context, projectID string, entityType EntityType, entityID string, opt extractOptions) ([]IndexDoc, []extractProblem, error) {
	if entityID == "" {
		return nil, nil, Errorf(CodeInvalidRequest, "实体 ID 为空")
	}
	switch entityType {
	case EntityNote:
		return e.noteDocs(ctx, projectID, entityID)
	case EntityDBRecord:
		return e.recordDocs(ctx, projectID, entityID)
	case EntityIdea:
		return e.ideaDocs(ctx, projectID, entityID)
	case EntityExperiment:
		return e.experimentDocs(ctx, projectID, entityID)
	case EntityPaper:
		return e.paperDocs(ctx, entityID, opt)
	default:
		return nil, nil, Errorf(CodeInvalidRequest, "不索引的实体类型 %q", string(entityType))
	}
}

// allProjectDocs enumerates every searchable document of one project. It is
// the rebuild path (SPEC-10 §5.3 Reindex).
func (e *extractor) allProjectDocs(ctx context.Context, projectID string, opt extractOptions) ([]IndexDoc, []extractProblem, error) {
	handle, err := e.ws.Project(ctx, projectID)
	if err != nil {
		return nil, nil, WrapError(CodeScopeNotFound, fmt.Sprintf("打开项目 %s 失败", projectID), err)
	}
	type ref struct {
		entityType EntityType
		entityID   string
	}
	var refs []ref
	lists := []struct {
		query string
		etype EntityType
	}{
		{`SELECT id FROM notes WHERE kind IN ('scratchpad','paper_note') AND deleted_at IS NULL ORDER BY id`, EntityNote},
		{`SELECT id FROM db_records WHERE deleted_at IS NULL ORDER BY id`, EntityDBRecord},
		{`SELECT id FROM ideas WHERE deleted_at IS NULL ORDER BY id`, EntityIdea},
		{`SELECT id FROM experiments WHERE deleted_at IS NULL ORDER BY id`, EntityExperiment},
	}
	for _, l := range lists {
		rows, err := handle.DB().QueryContext(ctx, l.query)
		if err != nil {
			return nil, nil, fmt.Errorf("search: 枚举 %s: %w", l.etype, err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, nil, fmt.Errorf("search: 枚举 %s: %w", l.etype, err)
			}
			refs = append(refs, ref{l.etype, id})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("search: 枚举 %s: %w", l.etype, err)
		}
		rows.Close()
	}
	var docs []IndexDoc
	var problems []extractProblem
	for _, r := range refs {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		d, p, err := e.entityDocs(ctx, projectID, r.entityType, r.entityID, opt)
		if err != nil {
			return nil, nil, err
		}
		docs = append(docs, d...)
		problems = append(problems, p...)
	}
	return docs, problems, nil
}

// allPaperDocs enumerates every paper projection of the global library.
func (e *extractor) allPaperDocs(ctx context.Context, opt extractOptions) ([]IndexDoc, []extractProblem, error) {
	rows, err := e.ws.Global().DB().QueryContext(ctx, `SELECT id FROM papers ORDER BY id`)
	if err != nil {
		return nil, nil, fmt.Errorf("search: 枚举文献: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("search: 枚举文献: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, fmt.Errorf("search: 枚举文献: %w", err)
	}
	rows.Close()

	var docs []IndexDoc
	var problems []extractProblem
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		d, p, err := e.paperDocs(ctx, id, opt)
		if err != nil {
			return nil, nil, err
		}
		docs = append(docs, d...)
		problems = append(problems, p...)
	}
	return docs, problems, nil
}

// noteRow is the read-only projection of one notes row.
type noteRow struct {
	ID        string
	Kind      string
	Title     string
	FilePath  string
	PaperID   sql.NullString
	UpdatedAt string
	DeletedAt sql.NullString
}

// noteDocs extracts one note. DB-record notes are owned by their record
// document (SPEC-10 §3.4), and overview notes are outside the search scope.
func (e *extractor) noteDocs(ctx context.Context, projectID, noteID string) ([]IndexDoc, []extractProblem, error) {
	handle, err := e.ws.Project(ctx, projectID)
	if err != nil {
		return nil, nil, WrapError(CodeScopeNotFound, fmt.Sprintf("打开项目 %s 失败", projectID), err)
	}
	var n noteRow
	err = handle.DB().QueryRowContext(ctx,
		`SELECT id, kind, title, file_path, paper_id, updated_at, deleted_at FROM notes WHERE id = ?`, noteID).
		Scan(&n.ID, &n.Kind, &n.Title, &n.FilePath, &n.PaperID, &n.UpdatedAt, &n.DeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("search: 读取笔记 %s: %w", noteID, err)
	}
	if n.DeletedAt.Valid {
		return nil, nil, nil
	}
	var source SourceType
	switch n.Kind {
	case "scratchpad":
		source = SourceScratchpad
	case "paper_note":
		source = SourcePaperNote
	default:
		return nil, nil, nil
	}
	docID := DocIDFor(EntityNote, n.ID)
	body, err := e.readNoteBody(n.FilePath)
	if err != nil {
		return nil, []extractProblem{{DocID: docID, Err: err}}, nil
	}
	doc := e.markdownDoc(docID, projectID, source, EntityNote, n.ID, n.Title, body, n.UpdatedAt)
	return []IndexDoc{doc}, nil, nil
}

// recordDocs extracts one research database record: "field name: value" lines
// located by FieldID plus the record's note body (SPEC-10 §3.4).
func (e *extractor) recordDocs(ctx context.Context, projectID, recordID string) ([]IndexDoc, []extractProblem, error) {
	handle, err := e.ws.Project(ctx, projectID)
	if err != nil {
		return nil, nil, WrapError(CodeScopeNotFound, fmt.Sprintf("打开项目 %s 失败", projectID), err)
	}
	var (
		id, databaseID, title, data, updatedAt string
		noteID, deletedAt                      sql.NullString
	)
	err = handle.DB().QueryRowContext(ctx,
		`SELECT id, database_id, title, data, note_id, updated_at, deleted_at FROM db_records WHERE id = ?`, recordID).
		Scan(&id, &databaseID, &title, &data, &noteID, &updatedAt, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("search: 读取记录 %s: %w", recordID, err)
	}
	if deletedAt.Valid {
		return nil, nil, nil
	}
	fields, err := e.databaseFields(ctx, handle.DB(), databaseID)
	if err != nil {
		return nil, nil, err
	}
	values := map[string]any{}
	if strings.TrimSpace(data) != "" {
		if err := json.Unmarshal([]byte(data), &values); err != nil {
			return nil, nil, fmt.Errorf("search: 解析记录 %s 的字段值: %w", recordID, err)
		}
	}
	type fieldEntry struct {
		name  string
		value string
	}
	var entries []fieldEntry
	for fieldID, raw := range values {
		text := formatValue(raw)
		if text == "" {
			continue
		}
		entries = append(entries, fieldEntry{name: fields[fieldID], value: text})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].name != entries[j].name {
			return entries[i].name < entries[j].name
		}
		return entries[i].value < entries[j].value
	})
	docID := DocIDFor(EntityDBRecord, recordID)
	var chunks []Chunk
	for _, entry := range entries {
		name := entry.name
		if name == "" {
			name = "字段"
		}
		text := name + ": " + entry.value
		chunks = append(chunks, Chunk{
			ChunkID:     chunkID(docID, len(chunks)),
			DocID:       docID,
			Ordinal:     len(chunks),
			Text:        text,
			ContentHash: hashText(text),
			Locator:     Locator{BlockNo: len(chunks), Heading: title},
		})
	}
	// The record's detail note stays part of the db_record source so the same
	// note never appears twice (SPEC-10 §3.4).
	if noteID.Valid && noteID.String != "" {
		var filePath string
		var noteDeleted sql.NullString
		err := handle.DB().QueryRowContext(ctx,
			`SELECT file_path, deleted_at FROM notes WHERE id = ? AND kind = 'db_record'`, noteID.String).
			Scan(&filePath, &noteDeleted)
		if err == nil && !noteDeleted.Valid {
			body, bodyErr := e.readNoteBody(filePath)
			if bodyErr != nil {
				return nil, []extractProblem{{DocID: docID, Err: bodyErr}}, nil
			}
			base := len(chunks)
			for i, c := range chunkMarkdown(docID, body) {
				c.Ordinal = base + i
				c.ChunkID = chunkID(docID, c.Ordinal)
				if c.Locator.Heading == "" {
					c.Locator.Heading = title
				}
				chunks = append(chunks, c)
			}
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, nil, fmt.Errorf("search: 读取记录 %s 的详情页: %w", recordID, err)
		}
	}
	text := title + "\n" + joinChunkText(chunks)
	doc := IndexDoc{
		DocID:         docID,
		ProjectID:     projectID,
		Source:        SourceDBRecord,
		EntityType:    EntityDBRecord,
		EntityID:      recordID,
		Title:         title,
		Lang:          detectLang(text),
		UpdatedAt:     parseTime(updatedAt, e.clock.Now()),
		ContentHash:   hashText(title + "\x00" + joinChunkText(chunks)),
		ParserVersion: parserVersionMarkdown,
		Chunks:        chunks,
	}
	return []IndexDoc{doc}, nil, nil
}

// databaseFields maps db_fields.id to its display name (read-only).
func (e *extractor) databaseFields(ctx context.Context, db *sql.DB, databaseID string) (map[string]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, name FROM db_fields WHERE database_id = ? AND deleted_at IS NULL`, databaseID)
	if err != nil {
		return nil, fmt.Errorf("search: 读取数据库字段: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("search: 读取数据库字段: %w", err)
		}
		out[id] = name
	}
	return out, rows.Err()
}

// ideaDocs extracts one idea: title, description, status and rejection reason
// (SPEC-10 §3.4).
func (e *extractor) ideaDocs(ctx context.Context, projectID, ideaID string) ([]IndexDoc, []extractProblem, error) {
	handle, err := e.ws.Project(ctx, projectID)
	if err != nil {
		return nil, nil, WrapError(CodeScopeNotFound, fmt.Sprintf("打开项目 %s 失败", projectID), err)
	}
	var (
		id, title, description, status, rejection, updatedAt string
		deletedAt                                            sql.NullString
	)
	err = handle.DB().QueryRowContext(ctx,
		`SELECT id, title, description, status, rejection_reason, updated_at, deleted_at FROM ideas WHERE id = ?`, ideaID).
		Scan(&id, &title, &description, &status, &rejection, &updatedAt, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("search: 读取想法 %s: %w", ideaID, err)
	}
	if deletedAt.Valid {
		return nil, nil, nil
	}
	body := title + "\n\n" + description
	if status != "" {
		body += "\n\n状态：" + status
	}
	if rejection != "" {
		body += "\n\n否定原因：" + rejection
	}
	doc := e.markdownDoc(DocIDFor(EntityIdea, id), projectID, SourceIdea, EntityIdea, id, title, body, updatedAt)
	return []IndexDoc{doc}, nil, nil
}

// experimentDocs extracts the latest Outline and Results analyses of one
// experiment. Runs and snapshots themselves are out of scope (SPEC-10 §1.3).
func (e *extractor) experimentDocs(ctx context.Context, projectID, experimentID string) ([]IndexDoc, []extractProblem, error) {
	handle, err := e.ws.Project(ctx, projectID)
	if err != nil {
		return nil, nil, WrapError(CodeScopeNotFound, fmt.Sprintf("打开项目 %s 失败", projectID), err)
	}
	var (
		id, title, updatedAt string
		deletedAt            sql.NullString
	)
	err = handle.DB().QueryRowContext(ctx,
		`SELECT id, title, updated_at, deleted_at FROM experiments WHERE id = ?`, experimentID).
		Scan(&id, &title, &updatedAt, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("search: 读取实验 %s: %w", experimentID, err)
	}
	if deletedAt.Valid {
		return nil, nil, nil
	}
	var docs []IndexDoc
	for _, kind := range []struct {
		analysis string
		source   SourceType
	}{{"outline", SourceExperimentOutline}, {"results", SourceExperimentResults}} {
		content, snapAt, err := e.latestAnalysis(ctx, handle.DB(), experimentID, kind.analysis)
		if err != nil {
			return nil, nil, err
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		doc := e.markdownDoc(ExperimentDocID(id, kind.source), projectID, kind.source, EntityExperiment, id, title, content, updatedAt)
		if snapAt.After(doc.UpdatedAt) {
			doc.UpdatedAt = snapAt
		}
		docs = append(docs, doc)
	}
	return docs, nil, nil
}

// latestAnalysis reads the newest snapshot content of one analysis kind.
func (e *extractor) latestAnalysis(ctx context.Context, db *sql.DB, experimentID, kind string) (string, time.Time, error) {
	var (
		payload   sql.NullString
		createdAt string
	)
	err := db.QueryRowContext(ctx,
		`SELECT ai_analysis, created_at FROM snapshots
		 WHERE target_type = 'experiment' AND target_id = ?
		   AND CASE WHEN json_valid(ai_analysis) THEN json_extract(ai_analysis, '$.kind') END = ?
		 ORDER BY created_at DESC, id DESC LIMIT 1`, experimentID, kind).
		Scan(&payload, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, nil
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("search: 读取实验 %s 的 %s: %w", experimentID, kind, err)
	}
	if !payload.Valid || strings.TrimSpace(payload.String) == "" {
		return "", time.Time{}, nil
	}
	var analysis struct {
		Kind      string `json:"kind"`
		ContentMD string `json:"content_md"`
	}
	if err := json.Unmarshal([]byte(payload.String), &analysis); err != nil {
		return "", time.Time{}, fmt.Errorf("search: 解析实验 %s 的 %s: %w", experimentID, kind, err)
	}
	return analysis.ContentMD, parseTime(createdAt, e.clock.Now()), nil
}

// paperRow is the read-only projection of one papers row.
type paperRow struct {
	ID        string
	Metadata  string
	FilePath  sql.NullString
	FileHash  sql.NullString
	FileState string
	UpdatedAt string
}

// paperDocs extracts the global metadata and full-text documents of one paper.
func (e *extractor) paperDocs(ctx context.Context, paperID string, opt extractOptions) ([]IndexDoc, []extractProblem, error) {
	var p paperRow
	err := e.ws.Global().DB().QueryRowContext(ctx,
		`SELECT id, metadata, file_path, file_hash, file_state, updated_at FROM papers WHERE id = ?`, paperID).
		Scan(&p.ID, &p.Metadata, &p.FilePath, &p.FileHash, &p.FileState, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("search: 读取文献 %s: %w", paperID, err)
	}
	meta, err := parsePaperMetadata(p.Metadata)
	if err != nil {
		return nil, nil, fmt.Errorf("search: 解析文献 %s 元数据: %w", paperID, err)
	}
	title := meta.Title
	if strings.TrimSpace(title) == "" {
		title = "未命名文献"
	}
	updated := parseTime(p.UpdatedAt, e.clock.Now())

	metaChunks := paperMetadataChunks(DocIDFor(EntityPaper, paperID), meta)
	metaDoc := IndexDoc{
		DocID:         DocIDFor(EntityPaper, paperID),
		Source:        SourcePaperMetadata,
		EntityType:    EntityPaper,
		EntityID:      paperID,
		Title:         title,
		Lang:          detectLang(metaText(meta)),
		UpdatedAt:     updated,
		ContentHash:   hashText(metaText(meta)),
		ParserVersion: parserVersionMarkdown,
		Chunks:        metaChunks,
	}
	docs := []IndexDoc{metaDoc}

	fullID := FullTextDocID(paperID)
	if !p.FilePath.Valid || strings.TrimSpace(p.FilePath.String) == "" {
		// No local copy: there is nothing to parse and no stale entry to keep.
		return docs, nil, nil
	}
	abs, err := e.ws.Paths().Abs(p.FilePath.String)
	if err != nil {
		return docs, []extractProblem{{DocID: fullID, Err: err}}, nil
	}
	if _, statErr := os.Stat(abs); statErr != nil {
		return docs, []extractProblem{{DocID: fullID, Err: WrapError(CodeSourceMissing, "PDF 副本缺失", statErr)}}, nil
	}
	// Unchanged file hash + parser version: reuse the stored chunks instead of
	// parsing the PDF again (SPEC-10 §3.2, §5.5; A15).
	if !opt.ReparsePDF {
		if reused, ok, err := e.reusePDFDoc(ctx, fullID, p, title, updated); err != nil {
			return docs, []extractProblem{{DocID: fullID, Err: err}}, nil
		} else if ok {
			return append(docs, reused), nil, nil
		}
	}
	parsed, err := e.parse(ctx, abs, ParseOptions{})
	if err != nil {
		if errors.Is(err, ErrNoTextLayer) {
			if stateErr := e.recordParseState(ctx, paperID, p.FileHash.String, 0); stateErr != nil {
				return docs, []extractProblem{{DocID: fullID, Err: stateErr}}, nil
			}
			docs = append(docs, IndexDoc{
				DocID:         fullID,
				Source:        SourcePaperFullText,
				EntityType:    EntityPaper,
				EntityID:      paperID,
				Title:         title,
				UpdatedAt:     updated,
				ContentHash:   hashText("no_text\x00" + p.FileHash.String + "\x00" + title),
				ParserVersion: parserVersionPDF,
				NoText:        true,
			})
			return docs, nil, nil
		}
		return docs, []extractProblem{{DocID: fullID, Err: err}}, nil
	}
	chunks := chunkPDFBlocks(fullID, parsed.Blocks)
	if stateErr := e.recordParseState(ctx, paperID, p.FileHash.String, parsed.PageCount); stateErr != nil {
		return docs, []extractProblem{{DocID: fullID, Err: stateErr}}, nil
	}
	docs = append(docs, IndexDoc{
		DocID:         fullID,
		Source:        SourcePaperFullText,
		EntityType:    EntityPaper,
		EntityID:      paperID,
		Title:         title,
		Lang:          detectLang(joinChunkText(chunks)),
		UpdatedAt:     updated,
		ContentHash:   hashText(p.FileHash.String + "\x00" + title + "\x00" + joinChunkText(chunks)),
		ParserVersion: parserVersionPDF,
		Chunks:        chunks,
	})
	return docs, nil, nil
}

// reusePDFDoc returns the stored full-text document when neither the file hash
// nor the parser version changed, avoiding a repeated parse (A15).
func (e *extractor) reusePDFDoc(ctx context.Context, docID string, p paperRow, title string, updated time.Time) (IndexDoc, bool, error) {
	var (
		stateHash, stateParser string
	)
	err := e.ws.Global().DB().QueryRowContext(ctx,
		`SELECT file_hash, parser_version FROM paper_parse_state WHERE paper_id = ?`, p.ID).
		Scan(&stateHash, &stateParser)
	if errors.Is(err, sql.ErrNoRows) {
		return IndexDoc{}, false, nil
	}
	if err != nil {
		return IndexDoc{}, false, fmt.Errorf("search: 读取 PDF 解析断点: %w", err)
	}
	if stateHash != p.FileHash.String || stateParser != parserVersionPDF {
		return IndexDoc{}, false, nil
	}
	var (
		parserVersion, status, lang string
	)
	err = e.ws.Global().DB().QueryRowContext(ctx,
		`SELECT parser_version, index_status, lang FROM paper_index_docs WHERE doc_id = ?`, docID).
		Scan(&parserVersion, &status, &lang)
	if errors.Is(err, sql.ErrNoRows) {
		return IndexDoc{}, false, nil
	}
	if err != nil {
		return IndexDoc{}, false, fmt.Errorf("search: 读取 PDF 索引状态: %w", err)
	}
	if parserVersion != parserVersionPDF || status == StatusDeleted || status == StatusFailed {
		return IndexDoc{}, false, nil
	}
	doc := IndexDoc{
		DocID:         docID,
		Source:        SourcePaperFullText,
		EntityType:    EntityPaper,
		EntityID:      p.ID,
		Title:         title,
		Lang:          lang,
		UpdatedAt:     updated,
		ParserVersion: parserVersionPDF,
	}
	if status == StatusNoText {
		doc.NoText = true
		doc.ContentHash = hashText("no_text\x00" + p.FileHash.String + "\x00" + title)
		return doc, true, nil
	}
	chunks, err := loadStoredChunks(ctx, e.ws.Global().DB(), "paper_chunks", docID)
	if err != nil {
		return IndexDoc{}, false, err
	}
	if len(chunks) == 0 {
		return IndexDoc{}, false, nil
	}
	doc.Chunks = chunks
	doc.ContentHash = hashText(p.FileHash.String + "\x00" + title + "\x00" + joinChunkText(chunks))
	if doc.Lang == "" {
		doc.Lang = detectLang(joinChunkText(chunks))
	}
	return doc, true, nil
}

// loadStoredChunks reads the stored chunk projection of one document.
func loadStoredChunks(ctx context.Context, db *sql.DB, table, docID string) ([]Chunk, error) {
	rows, err := db.QueryContext(ctx,
		fmt.Sprintf(`SELECT chunk_id, ordinal, text, locator_json, content_hash FROM %s WHERE doc_id = ? ORDER BY ordinal`, table),
		docID)
	if err != nil {
		return nil, fmt.Errorf("search: 读取已存分块: %w", err)
	}
	defer rows.Close()
	var out []Chunk
	for rows.Next() {
		var (
			c       Chunk
			locator string
		)
		if err := rows.Scan(&c.ChunkID, &c.Ordinal, &c.Text, &locator, &c.ContentHash); err != nil {
			return nil, fmt.Errorf("search: 读取已存分块: %w", err)
		}
		c.DocID = docID
		if locator != "" {
			if err := json.Unmarshal([]byte(locator), &c.Locator); err != nil {
				c.Locator = Locator{}
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// recordParseState stores the parse checkpoint of one paper.
func (e *extractor) recordParseState(ctx context.Context, paperID, fileHash string, pageCount int) error {
	_, err := e.ws.Global().DB().ExecContext(ctx,
		`INSERT INTO paper_parse_state (paper_id, file_hash, parser_version, page_count, parsed_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(paper_id) DO UPDATE SET file_hash = excluded.file_hash,
		   parser_version = excluded.parser_version, page_count = excluded.page_count, parsed_at = excluded.parsed_at`,
		paperID, fileHash, parserVersionPDF, pageCount, clock.Format(e.clock.Now()))
	if err != nil {
		return fmt.Errorf("search: 写入 PDF 解析断点: %w", err)
	}
	return nil
}

// markdownDoc builds a Markdown-backed document (note, idea, experiment).
func (e *extractor) markdownDoc(docID, projectID string, source SourceType, entityType EntityType, entityID, title, body, updatedAt string) IndexDoc {
	chunks := chunkMarkdown(docID, body)
	return IndexDoc{
		DocID:         docID,
		ProjectID:     projectID,
		Source:        source,
		EntityType:    entityType,
		EntityID:      entityID,
		Title:         title,
		Lang:          detectLang(title + "\n" + body),
		UpdatedAt:     parseTime(updatedAt, e.clock.Now()),
		ContentHash:   hashText(title + "\x00" + body),
		ParserVersion: parserVersionMarkdown,
		Chunks:        chunks,
	}
}

// readNoteBody reads a note's Markdown file (workspace-relative path). A
// missing file is a problem, not a deletion (SPEC-10 §3.4).
func (e *extractor) readNoteBody(rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", WrapError(CodeSourceMissing, "笔记文件路径为空", ErrSourceFileMissing)
	}
	abs, err := e.ws.Paths().Abs(rel)
	if err != nil {
		return "", WrapError(CodeSourceMissing, "笔记文件路径无效", err)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		return "", WrapError(CodeSourceMissing, "笔记文件缺失", err)
	}
	return string(body), nil
}

// paperMeta is the subset of the Zotero metadata the index searches.
type paperMeta struct {
	Title            string `json:"title"`
	ItemType         string `json:"itemType"`
	Date             string `json:"date"`
	PublicationTitle string `json:"publicationTitle"`
	DOI              string `json:"DOI"`
	URL              string `json:"url"`
	AbstractNote     string `json:"abstractNote"`
	Language         string `json:"language"`
	Creators         []struct {
		FirstName   string `json:"firstName"`
		LastName    string `json:"lastName"`
		Name        string `json:"name"`
		CreatorType string `json:"creatorType"`
	} `json:"creators"`
	Tags []struct {
		Tag string `json:"tag"`
	} `json:"tags"`
}

// parsePaperMetadata decodes the Zotero metadata JSON.
func parsePaperMetadata(raw string) (paperMeta, error) {
	var meta paperMeta
	if strings.TrimSpace(raw) == "" {
		return meta, nil
	}
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return paperMeta{}, err
	}
	return meta, nil
}

// metaText renders the searchable metadata text.
func metaText(meta paperMeta) string {
	var parts []string
	add := func(label, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		parts = append(parts, label+": "+value)
	}
	add("标题", meta.Title)
	add("类型", meta.ItemType)
	add("作者", metaAuthors(meta))
	add("年份", metaYear(meta.Date))
	add("期刊/会议", meta.PublicationTitle)
	add("DOI", meta.DOI)
	add("URL", meta.URL)
	add("摘要", meta.AbstractNote)
	add("标签", metaTags(meta))
	add("语言", meta.Language)
	return strings.Join(parts, "\n")
}

// paperMetadataChunks splits the metadata text into field-level chunks so a
// hit can point at the field that matched (SPEC-10 §3.4: no page).
func paperMetadataChunks(docID string, meta paperMeta) []Chunk {
	lines := strings.Split(metaText(meta), "\n")
	var chunks []Chunk
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		chunks = append(chunks, Chunk{
			ChunkID:     chunkID(docID, len(chunks)),
			DocID:       docID,
			Ordinal:     len(chunks),
			Text:        line,
			ContentHash: hashText(line),
			Locator:     Locator{BlockNo: i, Heading: "Metadata"},
		})
	}
	return chunks
}

// metaAuthors joins creator names in the Zotero ordering.
func metaAuthors(meta paperMeta) string {
	names := make([]string, 0, len(meta.Creators))
	for _, c := range meta.Creators {
		name := strings.TrimSpace(c.LastName + " " + c.FirstName)
		if name == "" {
			name = strings.TrimSpace(c.Name)
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, "; ")
}

// metaTags joins the tag list.
func metaTags(meta paperMeta) string {
	tags := make([]string, 0, len(meta.Tags))
	for _, t := range meta.Tags {
		if v := strings.TrimSpace(t.Tag); v != "" {
			tags = append(tags, v)
		}
	}
	return strings.Join(tags, ", ")
}

// metaYear extracts the leading four-digit year of a Zotero date string.
func metaYear(date string) string {
	fields := strings.FieldsFunc(date, func(r rune) bool { return r < '0' || r > '9' })
	for _, f := range fields {
		if len(f) == 4 {
			if n, err := strconv.Atoi(f); err == nil && n >= 1000 && n <= 2999 {
				return f
			}
		}
	}
	return ""
}

// paperMetaYear returns the metadata year as an int (0 when absent).
func paperMetaYear(raw string) int {
	meta, err := parsePaperMetadata(raw)
	if err != nil {
		return 0
	}
	y, _ := strconv.Atoi(metaYear(meta.Date))
	return y
}

// metaAuthorsList returns the normalized author names for filtering.
func metaAuthorsList(raw string) []string {
	meta, err := parsePaperMetadata(raw)
	if err != nil {
		return nil
	}
	var out []string
	for _, c := range meta.Creators {
		name := strings.TrimSpace(c.LastName + " " + c.FirstName)
		if name == "" {
			name = strings.TrimSpace(c.Name)
		}
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// formatValue renders one research database field value.
func formatValue(raw any) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if text := formatValue(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		body, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(body)
	default:
		return fmt.Sprint(v)
	}
}

// parseTime parses a stored timestamp, falling back to fallback.
func parseTime(raw string, fallback time.Time) time.Time {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	if t, err := clock.Parse(raw); err == nil {
		return t
	}
	return fallback
}

// joinChunkText concatenates chunk texts for hashing and language detection.
func joinChunkText(chunks []Chunk) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(c.Text)
		b.WriteByte('\n')
	}
	return b.String()
}
