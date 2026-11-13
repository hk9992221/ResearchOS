package search

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/index/store"
)

// docFilter is the resolved document restriction of one target.
type docFilter struct {
	// Allowed, when restricted is true, is the exact set of searchable docs.
	Allowed    []string
	Restricted bool
	// Excluded lists soft-deleted docs (kept in the index, hidden by default).
	Excluded []string
}

// resolveDocFilter pushes the structured filters down to the document table:
// chunk retrieval is then limited to the allowed doc ids, so a filter never
// silently loses a hit to the top-k cut (SPEC-10 §5.2 step 3).
func (s *Service) resolveDocFilter(ctx context.Context, t *searchTarget, filters Filters, includeDeleted bool) (docFilter, error) {
	var (
		conds []string
		args  []any
	)
	base := fmt.Sprintf(`SELECT doc_id FROM %s`, t.tables.docs)
	if t.partition == store.PartitionProject {
		conds = append(conds, "project_id = ?")
		args = append(args, t.projectID)
	}
	if len(filters.Sources) > 0 {
		ph := make([]string, 0, len(filters.Sources))
		for _, source := range filters.Sources {
			if !source.Valid() {
				return docFilter{}, Errorf(CodeInvalidRequest, "未知检索源 %q", string(source))
			}
			ph = append(ph, "?")
			args = append(args, string(source))
		}
		conds = append(conds, "source IN ("+strings.Join(ph, ",")+")")
	}
	if len(filters.EntityTypes) > 0 {
		ph := make([]string, 0, len(filters.EntityTypes))
		for _, et := range filters.EntityTypes {
			if !et.Valid() {
				return docFilter{}, Errorf(CodeInvalidRequest, "未知实体类型 %q", string(et))
			}
			ph = append(ph, "?")
			args = append(args, string(et))
		}
		conds = append(conds, "entity_type IN ("+strings.Join(ph, ",")+")")
	}
	if filters.UpdatedAfter != nil {
		conds = append(conds, "updated_at >= ?")
		args = append(args, clock.Format(*filters.UpdatedAfter))
	}

	out := docFilter{}
	// A project target always restricts the global partition to its linked
	// papers (SEARCH-06); a filter restricts the doc id set further.
	restricted := t.restrictDocs != nil
	allowed := t.restrictDocs
	if len(conds) > 0 {
		query := base + " WHERE " + strings.Join(conds, " AND ") + " ORDER BY doc_id"
		rows, err := t.db.QueryContext(ctx, query, args...)
		if err != nil {
			return docFilter{}, fmt.Errorf("search: 解析文档过滤: %w", err)
		}
		sqlAllowed, err := scanIDs(rows)
		if err != nil {
			return docFilter{}, fmt.Errorf("search: 解析文档过滤: %w", err)
		}
		if restricted {
			allowed = intersect(sqlAllowed, allowed)
		} else {
			allowed = sqlAllowed
		}
		restricted = true
	}

	// Paper-scoped filters need the global library (year, authors, paper ids).
	paperIDs, paperRestricted, err := s.paperScopeIDs(ctx, filters)
	if err != nil {
		return docFilter{}, err
	}
	if paperRestricted {
		scoped, err := s.paperDocIDs(ctx, t, paperIDs)
		if err != nil {
			return docFilter{}, err
		}
		if restricted {
			allowed = intersect(allowed, scoped)
		} else {
			allowed = scoped
		}
		restricted = true
	}
	if restricted && allowed == nil {
		allowed = []string{}
	}
	out.Allowed = allowed
	out.Restricted = restricted
	if !includeDeleted {
		excluded, err := s.deletedDocIDs(ctx, t)
		if err != nil {
			return docFilter{}, err
		}
		out.Excluded = excluded
	}
	return out, nil
}

// scanIDs reads a single-column string result set.
func scanIDs(rows *sql.Rows) ([]string, error) {
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// deletedDocIDs lists the soft-deleted documents of one target.
func (s *Service) deletedDocIDs(ctx context.Context, t *searchTarget) ([]string, error) {
	query := fmt.Sprintf(`SELECT doc_id FROM %s WHERE index_status = ?`, t.tables.docs)
	args := []any{StatusDeleted}
	if t.partition == store.PartitionProject {
		query += ` AND project_id = ?`
		args = append(args, t.projectID)
	}
	rows, err := t.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search: 读取已删除文档: %w", err)
	}
	return scanIDs(rows)
}

// paperScopeIDs resolves the paper-id restriction of a filter. restricted is
// false when the filter does not constrain the paper set at all.
func (s *Service) paperScopeIDs(ctx context.Context, f Filters) ([]string, bool, error) {
	if len(f.PaperIDs) == 0 && f.YearMin == nil && f.YearMax == nil && len(f.Authors) == 0 {
		return nil, false, nil
	}
	candidates := map[string]bool{}
	for _, id := range f.PaperIDs {
		candidates[id] = true
	}
	needMeta := f.YearMin != nil || f.YearMax != nil || len(f.Authors) > 0
	if !needMeta {
		out := make([]string, 0, len(candidates))
		for id := range candidates {
			out = append(out, id)
		}
		return out, true, nil
	}
	rows, err := s.ws.Global().DB().QueryContext(ctx, `SELECT id, metadata FROM papers ORDER BY id`)
	if err != nil {
		return nil, false, fmt.Errorf("search: 读取文献元数据: %w", err)
	}
	type row struct{ id, metadata string }
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.metadata); err != nil {
			rows.Close()
			return nil, false, fmt.Errorf("search: 读取文献元数据: %w", err)
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, false, fmt.Errorf("search: 读取文献元数据: %w", err)
	}
	rows.Close()
	out := make([]string, 0, len(all))
	for _, p := range all {
		if len(candidates) > 0 && !candidates[p.id] {
			continue
		}
		year := paperMetaYear(p.metadata)
		if f.YearMin != nil && (year == 0 || year < *f.YearMin) {
			continue
		}
		if f.YearMax != nil && (year == 0 || year > *f.YearMax) {
			continue
		}
		if len(f.Authors) > 0 && !matchesAuthors(metaAuthorsList(p.metadata), f.Authors) {
			continue
		}
		out = append(out, p.id)
	}
	return out, true, nil
}

// matchesAuthors reports whether any filter author is a case-insensitive
// substring of an author name.
func matchesAuthors(authors, wanted []string) bool {
	for _, w := range wanted {
		w = strings.ToLower(strings.TrimSpace(w))
		if w == "" {
			continue
		}
		for _, a := range authors {
			if strings.Contains(strings.ToLower(a), w) {
				return true
			}
		}
	}
	return false
}

// paperDocIDs maps a paper id set to the documents of one target. In a project
// database that means the project's paper notes of those papers; in the global
// partition it means both the metadata and full-text document.
func (s *Service) paperDocIDs(ctx context.Context, t *searchTarget, paperIDs []string) ([]string, error) {
	out := make([]string, 0, len(paperIDs)*2)
	if len(paperIDs) == 0 {
		return out, nil
	}
	if t.partition == store.PartitionGlobal {
		for _, id := range paperIDs {
			out = append(out, DocIDFor(EntityPaper, id), FullTextDocID(id))
		}
		return out, nil
	}
	query := `SELECT id FROM notes WHERE paper_id IN (` + placeholders(len(paperIDs)) + `)`
	args := make([]any, 0, len(paperIDs))
	for _, id := range paperIDs {
		args = append(args, id)
	}
	rows, err := t.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search: 读取项目文献: %w", err)
	}
	noteIDs, err := scanIDs(rows)
	if err != nil {
		return nil, fmt.Errorf("search: 读取项目文献: %w", err)
	}
	for _, id := range noteIDs {
		out = append(out, DocIDFor(EntityNote, id))
	}
	return out, nil
}

// linkedPaperDocIDs returns the global paper documents linked to one project
// (SPEC-04 project_papers is authoritative; SPEC-10 §4.3).
func (s *Service) linkedPaperDocIDs(ctx context.Context, projectID string) ([]string, error) {
	paperIDs, err := s.projectPaperIDs(ctx, projectID)
	if err != nil || len(paperIDs) == 0 {
		return nil, err
	}
	out := make([]string, 0, len(paperIDs)*2)
	for _, id := range paperIDs {
		out = append(out, DocIDFor(EntityPaper, id), FullTextDocID(id))
	}
	return out, nil
}

// projectPaperIDs returns the raw paper ids linked to a project.
func (s *Service) projectPaperIDs(ctx context.Context, projectID string) ([]string, error) {
	handle, err := s.ws.Project(ctx, projectID)
	if err != nil {
		return nil, err
	}
	rows, err := handle.DB().QueryContext(ctx, `SELECT paper_id FROM project_papers ORDER BY paper_id`)
	if err != nil {
		return nil, fmt.Errorf("search: 读取项目文献范围: %w", err)
	}
	return scanIDs(rows)
}

// intersect returns the sorted intersection of two doc-id sets.
func intersect(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, v := range b {
		set[v] = true
	}
	out := make([]string, 0, min(len(a), len(b)))
	for _, v := range a {
		if set[v] {
			out = append(out, v)
		}
	}
	return out
}

// placeholders returns n comma-separated bind placeholders.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// docMeta is the document row needed to assemble one result.
type docMeta struct {
	DocID      string
	Source     string
	EntityType string
	EntityID   string
	Title      string
	Lang       string
	UpdatedAt  time.Time
	Status     string
}

// loadDocMeta reads metadata for the requested doc ids.
func loadDocMeta(ctx context.Context, t *searchTarget, docIDs []string) (map[string]docMeta, error) {
	out := map[string]docMeta{}
	if len(docIDs) == 0 {
		return out, nil
	}
	query := fmt.Sprintf(`SELECT doc_id, source, entity_type, entity_id, title, lang, updated_at, index_status
		FROM %s WHERE doc_id IN (`+placeholders(len(docIDs))+`)`, t.tables.docs)
	args := make([]any, 0, len(docIDs))
	for _, id := range docIDs {
		args = append(args, id)
	}
	rows, err := t.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search: 读取文档元数据: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			m       docMeta
			updated string
		)
		if err := rows.Scan(&m.DocID, &m.Source, &m.EntityType, &m.EntityID, &m.Title, &m.Lang, &updated, &m.Status); err != nil {
			return nil, fmt.Errorf("search: 读取文档元数据: %w", err)
		}
		if ts, err := clock.Parse(updated); err == nil {
			m.UpdatedAt = ts
		}
		out[m.DocID] = m
	}
	return out, rows.Err()
}

// projectDisplay returns the name and archived flag of a project.
func (s *Service) projectDisplay(ctx context.Context, projectID string) (string, bool, error) {
	var (
		name   string
		status string
	)
	err := s.ws.Global().DB().QueryRowContext(ctx,
		`SELECT name, status FROM projects WHERE id = ?`, projectID).Scan(&name, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, Errorf(CodeScopeNotFound, "项目 %s 不存在", projectID)
	}
	if err != nil {
		return "", false, fmt.Errorf("search: 读取项目: %w", err)
	}
	return name, status == "archived", nil
}

// locatorFromJSON decodes a stored LocatorJSON (empty means the zero locator).
func locatorFromJSON(raw json.RawMessage) Locator {
	var loc Locator
	if len(raw) == 0 {
		return loc
	}
	if err := json.Unmarshal(raw, &loc); err != nil {
		return Locator{}
	}
	return loc
}
