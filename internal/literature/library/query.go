package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/papers"
	"researchos/internal/foundation/projectpapers"
	"researchos/internal/foundation/projects"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/settings"
	"researchos/internal/foundation/workspace"
	"researchos/internal/literature"
)

const (
	defaultPageLimit = 100
	maxPageLimit     = 500
	briefLimit       = 50
	maxBriefLimit    = 500
)

// noteQueryChunk bounds the number of ids per IN (...) lookup.
const noteQueryChunk = 400

var (
	_ literature.LiteratureQueryService = (*Service)(nil)
	_ literature.PaperIndex             = (*Service)(nil)
	_ literature.ProjectLinkService     = (*Service)(nil)
	_ literature.PDFService             = (*Service)(nil)
)

// Options configures a Service.
type Options struct {
	// Clock supplies timestamps; nil means the workspace clock.
	Clock clock.Clock
	// Zotero resolves Zotero source attachments for PDFTarget=zotero_source.
	// Nil means the feature reports literature.ErrZoteroNotConfigured.
	Zotero literature.ZoteroSource
	// Opener launches external applications. Nil means the OS default opener.
	Opener Opener
}

// Service implements the query, project-link and PDF contracts of SPEC-04.
type Service struct {
	ws       *workspace.Workspace
	clock    clock.Clock
	papers   *papers.Store
	projects *projects.Store
	files    *FileStore
	settings *settings.Store
	zotero   literature.ZoteroSource
	opener   Opener
	importMu *sync.Mutex
}

// New binds the service to an open workspace.
func New(ws *workspace.Workspace, opts Options) *Service {
	c := opts.Clock
	if c == nil {
		c = ws.Clock()
	}
	opener := opts.Opener
	if opener == nil {
		opener = systemOpener()
	}
	global := ws.Global()
	return &Service{
		ws:       ws,
		clock:    c,
		papers:   papers.New(global.DB(), c),
		projects: projects.New(global.DB(), c),
		files:    NewFileStore(ws),
		settings: settings.NewWorkspace(global.DB()).WithClock(c),
		zotero:   opts.Zotero,
		opener:   opener,
		importMu: importLockFor(ws.Root()),
	}
}

// ---------------------------------------------------------------------------
// §5.3 LiteratureQueryService
// ---------------------------------------------------------------------------

// ListPapers returns the global library filtered, sorted and paginated in the
// service layer (SPEC-04 §5.3).
func (s *Service) ListPapers(ctx context.Context, filter literature.PaperFilter) (literature.PaperPage, error) {
	rows, err := s.papers.List(ctx, papers.PaperQuery{})
	if err != nil {
		return literature.PaperPage{}, fmt.Errorf("library: 列出文献: %w", err)
	}
	entries := make([]paperEntry, 0, len(rows))
	for _, row := range rows {
		entry, err := newPaperEntry(row)
		if err != nil {
			return literature.PaperPage{}, err
		}
		if matchesFilter(entry, filter) {
			entries = append(entries, entry)
		}
	}
	slices.SortFunc(entries, func(a, b paperEntry) int {
		return compareEntries(a, b, filter.Sort, filter.Desc)
	})
	total := len(entries)
	start, end := pageBounds(total, filter.Offset, filter.Limit)
	items := make([]literature.Paper, 0, end-start)
	for _, entry := range entries[start:end] {
		items = append(items, entry.paper)
	}
	return literature.PaperPage{Items: items, Total: total}, nil
}

// GetPaper returns one global paper or literature.ErrPaperNotFound.
func (s *Service) GetPaper(ctx context.Context, paperID string) (literature.Paper, error) {
	row, err := s.papers.Get(ctx, paperID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return literature.Paper{}, fmt.Errorf("library: 读取文献 %s: %w", paperID, literature.ErrPaperNotFound)
		}
		return literature.Paper{}, fmt.Errorf("library: 读取文献 %s: %w", paperID, err)
	}
	entry, err := newPaperEntry(row)
	if err != nil {
		return literature.Paper{}, err
	}
	return entry.paper, nil
}

// QueryProjectPapers lists the papers associated with one project. Papers whose
// global row is gone are skipped (corrupt association, §5.3).
func (s *Service) QueryProjectPapers(ctx context.Context, projectID string, filter literature.PaperFilter) (literature.ProjectPaperPage, error) {
	if err := s.requireProject(ctx, projectID); err != nil {
		return literature.ProjectPaperPage{}, err
	}
	handle, err := s.ws.Project(ctx, projectID)
	if err != nil {
		return literature.ProjectPaperPage{}, fmt.Errorf("library: 打开项目 %s: %w", projectID, err)
	}
	links, err := s.projectPapers(handle).ByProject(ctx, projectID)
	if err != nil {
		return literature.ProjectPaperPage{}, err
	}
	if len(links) == 0 {
		return literature.ProjectPaperPage{Items: []literature.ProjectPaper{}, Total: 0}, nil
	}

	rows, err := s.papers.List(ctx, papers.PaperQuery{})
	if err != nil {
		return literature.ProjectPaperPage{}, fmt.Errorf("library: 列出文献: %w", err)
	}
	entries := make(map[string]paperEntry, len(rows))
	for _, row := range rows {
		entry, err := newPaperEntry(row)
		if err != nil {
			return literature.ProjectPaperPage{}, err
		}
		entries[row.ID] = entry
	}

	type candidate struct {
		entry    paperEntry
		linkedAt time.Time
	}
	candidates := make([]candidate, 0, len(links))
	paperIDs := make([]string, 0, len(links))
	for _, link := range links {
		entry, ok := entries[link.PaperID]
		if !ok {
			continue
		}
		if !matchesFilter(entry, filter) {
			continue
		}
		candidates = append(candidates, candidate{entry: entry, linkedAt: link.LinkedAt})
		paperIDs = append(paperIDs, link.PaperID)
	}

	noteIDs, err := s.noteIDsByPaper(ctx, handle, paperIDs)
	if err != nil {
		return literature.ProjectPaperPage{}, err
	}
	if filter.HasNote != nil {
		kept := candidates[:0]
		for _, c := range candidates {
			hasNote := noteIDs[c.entry.paper.ID] != ""
			if hasNote == *filter.HasNote {
				kept = append(kept, c)
			}
		}
		candidates = kept
	}

	slices.SortFunc(candidates, func(a, b candidate) int {
		return compareEntries(a.entry, b.entry, filter.Sort, filter.Desc)
	})
	total := len(candidates)
	start, end := pageBounds(total, filter.Offset, filter.Limit)
	items := make([]literature.ProjectPaper, 0, end-start)
	for _, c := range candidates[start:end] {
		items = append(items, literature.ProjectPaper{
			Paper:    c.entry.paper,
			LinkedAt: c.linkedAt,
			NoteID:   noteIDs[c.entry.paper.ID],
			Broken:   s.copyMissing(ctx, c.entry.paper.ID),
		})
	}
	return literature.ProjectPaperPage{Items: items, Total: total}, nil
}

// ListPaperProjects returns every registered project (archived included) that
// references paperID, ordered by project name then id.
func (s *Service) ListPaperProjects(ctx context.Context, paperID string) ([]literature.ProjectRef, error) {
	if err := validPaperID(paperID); err != nil {
		return nil, fmt.Errorf("library: 列出文献项目: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("library: 列出文献项目: %w", err)
	}
	rows, err := s.projects.List(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("library: 列出项目: %w", err)
	}
	out := make([]literature.ProjectRef, 0)
	for _, row := range rows {
		handle, err := s.ws.Project(ctx, row.ID)
		if err != nil {
			continue
		}
		links, err := s.projectPapers(handle).ByPaper(ctx, paperID)
		if err != nil {
			return nil, err
		}
		if len(links) == 0 {
			continue
		}
		out = append(out, literature.ProjectRef{
			ProjectID: row.ID,
			Name:      row.Name,
			Status:    string(row.Status),
		})
	}
	slices.SortFunc(out, func(a, b literature.ProjectRef) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.ProjectID, b.ProjectID)
	})
	return out, nil
}

// ---------------------------------------------------------------------------
// §5.3 PaperIndex
// ---------------------------------------------------------------------------

// ListProjectPaperIDs returns the union of the association table, relations
// pointing at a Paper and live evidence sourced from a Paper (§5.3).
func (s *Service) ListProjectPaperIDs(ctx context.Context, projectID string) ([]string, error) {
	if err := s.requireProject(ctx, projectID); err != nil {
		return nil, err
	}
	handle, err := s.ws.Project(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("library: 打开项目 %s: %w", projectID, err)
	}
	set := make(map[string]struct{})
	linked, err := s.projectPapers(handle).PaperIDs(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range linked {
		set[id] = struct{}{}
	}
	relRows, err := handle.DB().QueryContext(ctx,
		`SELECT DISTINCT dst_id FROM entity_relations
		 WHERE dst_type = 'paper' AND relation = 'references'`)
	if err != nil {
		return nil, fmt.Errorf("library: 读取文献关系: %w", err)
	}
	if err := collectIDs(relRows, set); err != nil {
		return nil, err
	}
	evRows, err := handle.DB().QueryContext(ctx,
		`SELECT DISTINCT source_id FROM evidence WHERE source_type = 'paper' AND deleted_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("library: 读取文献证据: %w", err)
	}
	if err := collectIDs(evRows, set); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	slices.Sort(out)
	return out, nil
}

// CountPaperRefs counts the registered projects, except excludeProjectID, that
// still reference paperID. A project counts at most once. Unreadable
// project_papers data is skipped, never counted (§5.3).
func (s *Service) CountPaperRefs(ctx context.Context, paperID, excludeProjectID string) (int, error) {
	if err := validPaperID(paperID); err != nil {
		return 0, fmt.Errorf("library: 统计文献引用: %w", err)
	}
	rows, err := s.projects.List(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("library: 列出项目: %w", err)
	}
	count := 0
	for _, row := range rows {
		if row.ID == excludeProjectID {
			continue
		}
		handle, err := s.ws.Project(ctx, row.ID)
		if err != nil {
			continue
		}
		linked, healthy, err := s.projectPapers(handle).CountRefs(ctx, paperID)
		if err != nil || !healthy {
			continue
		}
		if linked > 0 {
			count++
			continue
		}
		related, err := hasRow(ctx, handle.DB(),
			`SELECT 1 FROM entity_relations WHERE dst_type = 'paper' AND dst_id = ? AND relation = 'references' LIMIT 1`,
			paperID)
		if err != nil {
			continue
		}
		if related {
			count++
			continue
		}
		evidenced, err := hasRow(ctx, handle.DB(),
			`SELECT 1 FROM evidence WHERE source_type = 'paper' AND source_id = ? AND deleted_at IS NULL LIMIT 1`,
			paperID)
		if err != nil {
			continue
		}
		if evidenced {
			count++
		}
	}
	return count, nil
}

// ListProjectPapers returns the most recently associated papers of one project
// for export/overview consumers (§5.3).
func (s *Service) ListProjectPapers(ctx context.Context, projectID string, limit int) ([]literature.PaperBrief, error) {
	if err := s.requireProject(ctx, projectID); err != nil {
		return nil, err
	}
	handle, err := s.ws.Project(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("library: 打开项目 %s: %w", projectID, err)
	}
	links, err := s.projectPapers(handle).ByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = briefLimit
	}
	if limit > maxBriefLimit {
		limit = maxBriefLimit
	}
	slices.SortFunc(links, func(a, b projectpapers.Link) int {
		if c := b.LinkedAt.Compare(a.LinkedAt); c != 0 {
			return c
		}
		return strings.Compare(a.PaperID, b.PaperID)
	})

	rows, err := s.papers.List(ctx, papers.PaperQuery{})
	if err != nil {
		return nil, fmt.Errorf("library: 列出文献: %w", err)
	}
	entries := make(map[string]paperEntry, len(rows))
	for _, row := range rows {
		entry, err := newPaperEntry(row)
		if err != nil {
			return nil, err
		}
		entries[row.ID] = entry
	}
	out := make([]literature.PaperBrief, 0, limit)
	for _, link := range links {
		if len(out) >= limit {
			break
		}
		entry, ok := entries[link.PaperID]
		if !ok {
			continue
		}
		out = append(out, literature.PaperBrief{
			ID:       link.PaperID,
			Title:    entry.paper.Metadata.Title,
			Authors:  entry.paper.Metadata.Authors,
			Year:     entry.year,
			LinkedAt: link.LinkedAt,
			Missing:  s.copyMissing(ctx, link.PaperID),
		})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

// paperEntry is a decoded paper plus its filter/sort projections.
type paperEntry struct {
	paper       literature.Paper
	titleKey    string
	year        int
	sourceState string
	haystack    string
	tagKeys     []string
}

func newPaperEntry(row papers.Paper) (paperEntry, error) {
	md, err := decodeMetadata(row)
	if err != nil {
		return paperEntry{}, err
	}
	entry := paperEntry{
		paper: literature.Paper{
			ID:              row.ID,
			ZoteroKey:       row.ZoteroKey,
			ZoteroLibraryID: row.ZoteroLibraryID,
			Metadata:        md,
			FilePath:        row.FilePath,
			FileHash:        row.FileHash,
			ImportedAt:      row.ImportedAt,
			UpdatedAt:       row.UpdatedAt,
		},
		titleKey:    strings.ToLower(md.Title),
		year:        md.Year,
		sourceState: string(row.SourceState),
		haystack:    buildHaystack(md),
	}
	for _, tag := range md.Tags {
		entry.tagKeys = append(entry.tagKeys, strings.ToLower(tag.Tag))
	}
	return entry, nil
}

func decodeMetadata(row papers.Paper) (literature.PaperMetadata, error) {
	md := literature.PaperMetadata{}
	if len(row.Metadata) > 0 {
		if err := json.Unmarshal(row.Metadata, &md); err != nil {
			return literature.PaperMetadata{}, fmt.Errorf("library: 解析文献 %s 元数据: %w", row.ID, err)
		}
	}
	md.Year = yearOf(md.Date)
	md.Authors = authorsOf(md.Creators)
	return md, nil
}

// yearOf extracts the first plausible 4-digit year from a Zotero date string.
func yearOf(date string) int {
	for i := 0; i+4 <= len(date); i++ {
		if !isDigit(date[i]) || !isDigit(date[i+1]) || !isDigit(date[i+2]) || !isDigit(date[i+3]) {
			continue
		}
		if y, err := strconv.Atoi(date[i : i+4]); err == nil && y >= 1000 && y <= 2999 {
			return y
		}
		i += 3
	}
	return 0
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// authorsOf renders the author display string: creator order preserved, authors
// preferred and all creators used when no creator is typed as an author.
func authorsOf(creators []literature.ZoteroCreator) string {
	names := make([]string, 0, len(creators))
	for _, creator := range creators {
		if creator.CreatorType == "" || creator.CreatorType == "author" {
			if name := creatorName(creator); name != "" {
				names = append(names, name)
			}
		}
	}
	if len(names) == 0 {
		for _, creator := range creators {
			if name := creatorName(creator); name != "" {
				names = append(names, name)
			}
		}
	}
	return strings.Join(names, ", ")
}

func creatorName(c literature.ZoteroCreator) string {
	if c.Name != "" {
		return c.Name
	}
	switch {
	case c.FirstName != "" && c.LastName != "":
		return c.FirstName + " " + c.LastName
	case c.LastName != "":
		return c.LastName
	default:
		return c.FirstName
	}
}

// buildHaystack is the lowercase match surface of PaperFilter.Query.
func buildHaystack(md literature.PaperMetadata) string {
	var b strings.Builder
	b.WriteString(md.Title)
	b.WriteByte('\n')
	b.WriteString(md.Authors)
	b.WriteByte('\n')
	for _, creator := range md.Creators {
		b.WriteString(creatorName(creator))
		b.WriteByte('\n')
	}
	b.WriteString(md.DOI)
	b.WriteByte('\n')
	b.WriteString(md.AbstractNote)
	b.WriteByte('\n')
	for _, tag := range md.Tags {
		b.WriteString(tag.Tag)
		b.WriteByte('\n')
	}
	return strings.ToLower(b.String())
}

// matchesFilter applies every metadata filter. HasNote is project-specific and
// handled by QueryProjectPapers.
func matchesFilter(entry paperEntry, filter literature.PaperFilter) bool {
	if query := strings.ToLower(strings.TrimSpace(filter.Query)); query != "" &&
		!strings.Contains(entry.haystack, query) {
		return false
	}
	if len(filter.CollectionKeys) > 0 {
		found := false
		for _, key := range filter.CollectionKeys {
			if slices.Contains(entry.paper.Metadata.Collections, key) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, want := range filter.Tags {
		// Tags narrow like Zotero's tag selector: every selected tag must match.
		if !slices.Contains(entry.tagKeys, strings.ToLower(want)) {
			return false
		}
	}
	if filter.YearFrom > 0 || filter.YearTo > 0 {
		if entry.year == 0 {
			return false
		}
		if filter.YearFrom > 0 && entry.year < filter.YearFrom {
			return false
		}
		if filter.YearTo > 0 && entry.year > filter.YearTo {
			return false
		}
	}
	if len(filter.SourceState) > 0 && !slices.Contains(filter.SourceState, entry.sourceState) {
		return false
	}
	return true
}

// compareEntries is the total order of §5.3; Desc reverses the whole order.
func compareEntries(a, b paperEntry, sort literature.PaperSort, desc bool) int {
	c := compareEntriesAsc(a, b, sort)
	if desc {
		return -c
	}
	return c
}

func compareEntriesAsc(a, b paperEntry, sort literature.PaperSort) int {
	switch sort {
	case literature.SortTitle:
		if c := strings.Compare(a.titleKey, b.titleKey); c != 0 {
			return c
		}
	case literature.SortYear:
		if a.year != b.year {
			if a.year < b.year {
				return -1
			}
			return 1
		}
		if c := strings.Compare(a.titleKey, b.titleKey); c != 0 {
			return c
		}
	case literature.SortUpdatedAt:
		if c := a.paper.UpdatedAt.Compare(b.paper.UpdatedAt); c != 0 {
			return c
		}
	default: // SortImportedAt and any unknown value
		if c := a.paper.ImportedAt.Compare(b.paper.ImportedAt); c != 0 {
			return c
		}
	}
	return strings.Compare(a.paper.ID, b.paper.ID)
}

// pageBounds clamps offset/limit to the §5.3 defaults.
func pageBounds(total, offset, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = defaultPageLimit
	}
	if limit > maxPageLimit {
		limit = maxPageLimit
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return offset, end
}

func (s *Service) requireProject(ctx context.Context, projectID string) error {
	if !ids.Valid(projectID) {
		return fmt.Errorf("library: 项目 %q: %w", projectID, errs.ErrBadEntityID)
	}
	if _, err := s.projects.Get(ctx, projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("library: 项目 %s 未注册: %w", projectID, err)
		}
		return fmt.Errorf("library: 读取项目 %s: %w", projectID, err)
	}
	return nil
}

// activeProject loads a project and rejects the archived state (§5.0).
func (s *Service) activeProject(ctx context.Context, projectID string) (projects.Project, error) {
	if !ids.Valid(projectID) {
		return projects.Project{}, fmt.Errorf("library: 项目 %q: %w", projectID, errs.ErrBadEntityID)
	}
	project, err := s.projects.Get(ctx, projectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return projects.Project{}, fmt.Errorf("library: 项目 %s 未注册: %w", projectID, err)
		}
		return projects.Project{}, fmt.Errorf("library: 读取项目 %s: %w", projectID, err)
	}
	if project.Status != schema.ProjectActive {
		return project, fmt.Errorf("library: 项目《%s》已归档，只能查看: %w", project.Name, literature.ErrProjectArchived)
	}
	return project, nil
}

func (s *Service) projectPapers(handle *workspace.ProjectHandle) *projectpapers.Store {
	return projectpapers.New(handle.ID, handle.DB(), handle.Events(), s.clock)
}

// copyMissing reports whether the imported copy cannot be stat-ed. A stat error
// means the copy is not proven present, so it is reported as broken.
func (s *Service) copyMissing(ctx context.Context, paperID string) bool {
	_, _, err := s.files.Stat(ctx, paperID)
	return err != nil
}

// noteIDsByPaper maps paperID to its live Paper Note in this project. The
// relation contract is note --references--> paper (§4.4, §7.2).
func (s *Service) noteIDsByPaper(ctx context.Context, handle *workspace.ProjectHandle, paperIDs []string) (map[string]string, error) {
	out := make(map[string]string, len(paperIDs))
	for start := 0; start < len(paperIDs); start += noteQueryChunk {
		end := min(start+noteQueryChunk, len(paperIDs))
		chunk := paperIDs[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := handle.DB().QueryContext(ctx,
			`SELECT r.dst_id, n.id
			 FROM entity_relations r
			 JOIN notes n ON n.id = r.src_id
			 WHERE r.src_type = 'note' AND r.dst_type = 'paper' AND r.relation = 'references'
			   AND n.kind = 'paper_note' AND n.deleted_at IS NULL
			   AND r.dst_id IN (`+placeholders+`)
			 ORDER BY r.dst_id, n.created_at, n.id`, args...)
		if err != nil {
			return nil, fmt.Errorf("library: 读取 Paper Note 关联: %w", err)
		}
		for rows.Next() {
			var paperID, noteID string
			if err := rows.Scan(&paperID, &noteID); err != nil {
				rows.Close()
				return nil, fmt.Errorf("library: 读取 Paper Note 关联: %w", err)
			}
			if _, exists := out[paperID]; !exists {
				out[paperID] = noteID
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("library: 读取 Paper Note 关联: %w", err)
		}
		rows.Close()
	}
	return out, nil
}

// noteIDsForPaper returns the live Paper Note ids of one paper, sorted.
func (s *Service) noteIDsForPaper(ctx context.Context, handle *workspace.ProjectHandle, paperID string) ([]string, error) {
	byPaper, err := s.noteIDsByPaper(ctx, handle, []string{paperID})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, 1)
	if noteID := byPaper[paperID]; noteID != "" {
		out = append(out, noteID)
	}
	return out, nil
}

func collectIDs(rows *sql.Rows, set map[string]struct{}) error {
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("library: 读取文献引用: %w", err)
		}
		set[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("library: 读取文献引用: %w", err)
	}
	return nil
}

func hasRow(ctx context.Context, db *sql.DB, query string, args ...any) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
