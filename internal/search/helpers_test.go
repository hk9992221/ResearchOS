package search

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"researchos/internal/ai/aierr"
	"researchos/internal/ai/gateway"
	aimigrations "researchos/internal/ai/migrations"
	"researchos/internal/ai/protocol"
	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/projects"
	"researchos/internal/foundation/storage"
	"researchos/internal/foundation/workspace"
	ideasmigrations "researchos/internal/ideas/migrations"
	litmigrations "researchos/internal/literature/migrations"
	notesmigrations "researchos/internal/notes/migrations"
	searchmigrations "researchos/internal/search/migrations"
)

// mergeMigrations combines every module band plus the SPEC-10 band so the test
// workspace has the full v1.1 schema (production wiring does the same).
func mergeMigrations() map[storage.Kind][]storage.Migration {
	out := map[storage.Kind][]storage.Migration{}
	for _, band := range []map[storage.Kind][]storage.Migration{
		aimigrations.MustAll(),
		notesmigrations.MustAll(),
		ideasmigrations.MustAll(),
		litmigrations.MustAll(),
		searchmigrations.MustAll(),
	} {
		for kind, ms := range band {
			out[kind] = append(out[kind], ms...)
		}
	}
	return out
}

// newWorkspace opens a fresh migrated workspace in a temp directory.
func newWorkspace(t testing.TB) *workspace.Workspace {
	t.Helper()
	root := t.TempDir()
	ws, err := workspace.Init(context.Background(), root, workspace.Options{
		Clock:        clock.System{},
		SkipBackup:   true,
		MaxOpenConns: 4,
		Migrations:   mergeMigrations(),
	})
	if err != nil {
		t.Fatalf("workspace init: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return ws
}

// createProject registers a project and creates its data directory.
func createProject(t testing.TB, ws *workspace.Workspace, name string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := ws.Global().InTx(ctx, func(tx *sql.Tx) error {
		created, err := projects.New(ws.Global().DB(), clock.System{}).Create(ctx, tx, name, "", "")
		id = created
		return err
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := ws.CreateProjectData(ctx, id); err != nil {
		t.Fatalf("create project data: %v", err)
	}
	return id
}

// archiveProject marks a project archived in global.db.
func archiveProject(t *testing.T, ws *workspace.Workspace, projectID string) {
	t.Helper()
	if _, err := ws.Global().DB().Exec(
		`UPDATE projects SET status = 'archived', archived_at = ? WHERE id = ?`,
		clock.Format(clock.System{}.Now()), projectID); err != nil {
		t.Fatalf("archive project: %v", err)
	}
}

// insertNote writes a note row plus its Markdown file and returns the note id.
func insertNote(t *testing.T, ws *workspace.Workspace, projectID, kind, title, body, paperID string) string {
	t.Helper()
	id := mustID(t)
	rel := "projects/" + projectID + "/notes/" + id + ".md"
	abs, err := ws.Paths().Abs(rel)
	if err != nil {
		t.Fatalf("note path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir note: %v", err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}
	now := clock.Format(clock.System{}.Now())
	handle, err := ws.Project(context.Background(), projectID)
	if err != nil {
		t.Fatalf("project handle: %v", err)
	}
	var paper any
	if paperID != "" {
		paper = paperID
	}
	if _, err := handle.DB().Exec(
		`INSERT INTO notes (id, title, file_path, kind, created_at, updated_at, paper_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, title, rel, kind, now, now, paper); err != nil {
		t.Fatalf("insert note: %v", err)
	}
	return id
}

// rewriteNote replaces the Markdown body of a note.
func rewriteNote(t *testing.T, ws *workspace.Workspace, projectID, noteID, body string) {
	t.Helper()
	handle, err := ws.Project(context.Background(), projectID)
	if err != nil {
		t.Fatalf("project handle: %v", err)
	}
	var rel string
	if err := handle.DB().QueryRow(`SELECT file_path FROM notes WHERE id = ?`, noteID).Scan(&rel); err != nil {
		t.Fatalf("note file path: %v", err)
	}
	abs, err := ws.Paths().Abs(rel)
	if err != nil {
		t.Fatalf("note abs path: %v", err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatalf("rewrite note: %v", err)
	}
	if _, err := handle.DB().Exec(`UPDATE notes SET updated_at = ? WHERE id = ?`,
		clock.Format(clock.System{}.Now()), noteID); err != nil {
		t.Fatalf("touch note: %v", err)
	}
}

// insertIdea creates one idea row.
func insertIdea(t *testing.T, ws *workspace.Workspace, projectID, title, description, status, rejection string) string {
	t.Helper()
	id := mustID(t)
	handle, err := ws.Project(context.Background(), projectID)
	if err != nil {
		t.Fatalf("project handle: %v", err)
	}
	now := clock.Format(clock.System{}.Now())
	if _, err := handle.DB().Exec(
		`INSERT INTO ideas (id, title, description, status, rejection_reason, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, title, description, status, rejection, now, now); err != nil {
		t.Fatalf("insert idea: %v", err)
	}
	return id
}

// insertRecord creates a database, one field and one record with a detail note.
func insertRecord(t *testing.T, ws *workspace.Workspace, projectID, title string) string {
	t.Helper()
	handle, err := ws.Project(context.Background(), projectID)
	if err != nil {
		t.Fatalf("project handle: %v", err)
	}
	now := clock.Format(clock.System{}.Now())
	dbID := mustID(t)
	fieldID := mustID(t)
	recordID := mustID(t)
	if _, err := handle.DB().Exec(
		`INSERT INTO research_databases (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		dbID, "文献笔记库", now, now); err != nil {
		t.Fatalf("insert database: %v", err)
	}
	if _, err := handle.DB().Exec(
		`INSERT INTO db_fields (id, database_id, name, type, role, sort_order, created_at, updated_at) VALUES (?, ?, ?, 'text', 'plain', 0, ?, ?)`,
		fieldID, dbID, "关键词", now, now); err != nil {
		t.Fatalf("insert field: %v", err)
	}
	values, _ := json.Marshal(map[string]any{fieldID: "梯度下降 收敛速度"})
	noteID := insertNote(t, ws, projectID, "db_record", title, "## 观察\n\n学习率过大时震荡。", "")
	if _, err := handle.DB().Exec(
		`INSERT INTO db_records (id, database_id, note_id, title, data, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		recordID, dbID, noteID, title, string(values), now, now); err != nil {
		t.Fatalf("insert record: %v", err)
	}
	if _, err := handle.DB().Exec(`UPDATE notes SET kind = 'db_record' WHERE id = ?`, noteID); err != nil {
		t.Fatalf("mark record note: %v", err)
	}
	return recordID
}

// insertExperiment creates one experiment and an analysis snapshot of kind.
func insertExperiment(t *testing.T, ws *workspace.Workspace, projectID, title, kind, content string) string {
	t.Helper()
	handle, err := ws.Project(context.Background(), projectID)
	if err != nil {
		t.Fatalf("project handle: %v", err)
	}
	now := clock.Format(clock.System{}.Now())
	expID := mustID(t)
	if _, err := handle.DB().Exec(
		`INSERT INTO experiments (id, title, purpose, created_at, updated_at) VALUES (?, ?, '', ?, ?)`,
		expID, title, now, now); err != nil {
		t.Fatalf("insert experiment: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{"kind": kind, "content_md": content})
	snapID := mustID(t)
	if _, err := handle.DB().Exec(
		`INSERT INTO snapshots (id, target_type, target_id, ai_analysis, created_at) VALUES (?, 'experiment', ?, ?, ?)`,
		snapID, expID, string(payload), now); err != nil {
		t.Fatalf("insert snapshot: %v", err)
	}
	return expID
}

// insertPaper creates a global paper row and its PDF copy.
func insertPaper(t *testing.T, ws *workspace.Workspace, title, year string, pdf []byte) string {
	t.Helper()
	id := mustID(t)
	metadata, _ := json.Marshal(map[string]any{
		"title":    title,
		"itemType": "journalArticle",
		"date":     year,
		"creators": []map[string]any{{"firstName": "Ada", "lastName": "Lovelace", "creatorType": "author"}},
		"tags":     []map[string]any{{"tag": "search"}},
	})
	rel := ""
	var hash any
	if pdf != nil {
		rel = "library/papers/" + id + "/source.pdf"
		abs, err := ws.Paths().Abs(rel)
		if err != nil {
			t.Fatalf("paper path: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir paper: %v", err)
		}
		if err := os.WriteFile(abs, pdf, 0o644); err != nil {
			t.Fatalf("write paper: %v", err)
		}
		sum := sha256.Sum256(pdf)
		hash = hex.EncodeToString(sum[:])
	}
	now := clock.Format(clock.System{}.Now())
	if _, err := ws.Global().DB().Exec(
		`INSERT INTO papers (id, zotero_key, metadata, file_path, file_hash, imported_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, "ZOT-"+id, string(metadata), nullString(rel), hash, now, now); err != nil {
		t.Fatalf("insert paper: %v", err)
	}
	return id
}

// linkPaper links a paper to a project.
func linkPaper(t *testing.T, ws *workspace.Workspace, projectID, paperID string) {
	t.Helper()
	handle, err := ws.Project(context.Background(), projectID)
	if err != nil {
		t.Fatalf("project handle: %v", err)
	}
	if _, err := handle.DB().Exec(
		`INSERT INTO project_papers (project_id, paper_id, linked_at, linked_by) VALUES (?, ?, ?, 'user')`,
		projectID, paperID, clock.Format(clock.System{}.Now())); err != nil {
		t.Fatalf("link paper: %v", err)
	}
}

// nullString returns nil for an empty string.
func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// mustID returns a new UUIDv7.
func mustID(t *testing.T) string {
	t.Helper()
	id, err := ids.New(clock.System{})
	if err != nil {
		t.Fatalf("id: %v", err)
	}
	return id
}

// newSearchService builds a search service over ws.
func newSearchService(t *testing.T, ws *workspace.Workspace, opt Options) *Service {
	t.Helper()
	svc, err := New(ws, opt)
	if err != nil {
		t.Fatalf("search.New: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

// indexEntities notifies the given entities and flushes synchronously.
func indexEntities(t *testing.T, svc *Service, projectID string, changes ...Change) {
	t.Helper()
	ctx := context.Background()
	for _, ch := range changes {
		if ch.ProjectID == "" && projectID != "" {
			ch.ProjectID = projectID
		}
		if ch.Kind == "" {
			ch.Kind = ChangeUpsert
		}
		if err := svc.Notify(ctx, ch); err != nil {
			t.Fatalf("notify %s:%s: %v", ch.EntityType, ch.EntityID, err)
		}
	}
	scope := Scope{Kind: ScopeProject, ProjectID: projectID}
	if projectID == "" {
		scope = Scope{Kind: ScopeGlobal}
	}
	if err := svc.Flush(ctx, scope); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

// waitIdle waits until no index work is pending.
func waitIdle(t *testing.T, svc *Service, scope Scope) IndexStatus {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		status, err := svc.Status(context.Background(), scope)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if !status.Indexing && status.Dirty == 0 {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("index did not settle: %+v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// search runs one query and fails the test on error.
func search(t *testing.T, svc *Service, req SearchRequest) SearchResponse {
	t.Helper()
	if req.Scope.Kind == "" {
		t.Fatalf("search request has no scope")
	}
	resp, err := svc.Search(context.Background(), req)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	return resp
}

// resultDocIDs returns the doc ids of a response.
func resultDocIDs(resp SearchResponse) []string {
	out := make([]string, 0, len(resp.Results))
	for _, r := range resp.Results {
		out = append(out, r.DocID)
	}
	return out
}

// containsDoc reports whether a response contains a doc id.
func containsDoc(resp SearchResponse, docID string) bool {
	for _, r := range resp.Results {
		if r.DocID == docID {
			return true
		}
	}
	return false
}

// findResult returns the result with docID.
func findResult(t *testing.T, resp SearchResponse, docID string) SearchResult {
	t.Helper()
	for _, r := range resp.Results {
		if r.DocID == docID {
			return r
		}
	}
	t.Fatalf("doc %s not in results %v", docID, resultDocIDs(resp))
	return SearchResult{}
}

// hasDegrade reports whether a response lists the reason.
func hasDegrade(resp SearchResponse, reason DegradeReason) bool {
	for _, d := range resp.Degraded {
		if d == reason {
			return true
		}
	}
	return false
}

// countRows returns COUNT(*) of a query.
func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeEmbedder returns deterministic vectors. Texts containing a keyword of a
// synonym group share a direction so semantic recall is testable.
type fakeEmbedder struct {
	model      string
	err        error
	profileErr error
	calls      atomic.Int64
	groups     [][]string
	vectors    map[string][]float32
}

func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) (gateway.EmbedResponse, error) {
	f.calls.Add(1)
	if f.err != nil {
		return gateway.EmbedResponse{}, f.err
	}
	vectors := make([][]float32, len(texts))
	for i, text := range texts {
		vectors[i] = f.vectorFor(text)
	}
	return gateway.EmbedResponse{Vectors: vectors, Model: f.model}, nil
}

func (f *fakeEmbedder) CurrentProfile(ctx context.Context) (string, int, error) {
	if f.profileErr != nil {
		return "", 0, f.profileErr
	}
	return f.model, 8, nil
}

// vectorFor maps text onto one of the fixed directions.
func (f *fakeEmbedder) vectorFor(text string) []float32 {
	lower := strings.ToLower(text)
	if vec, ok := f.vectors[text]; ok {
		return vec
	}
	if vec, ok := f.vectors[lower]; ok {
		return vec
	}
	vec := make([]float32, 8)
	for gi, group := range f.groups {
		for _, word := range group {
			if strings.Contains(lower, strings.ToLower(word)) {
				vec[gi%8] = 1
				return vec
			}
		}
	}
	vec[7] = 1
	return vec
}

// fakeReranker returns a fixed error (the production gateway reports
// unsupported_capability) or deterministic scores.
type fakeReranker struct {
	err   error
	order func(query string, docs []string) []int
	calls atomic.Int64
}

func (f *fakeReranker) Rerank(ctx context.Context, query string, docs []string, topN int) (gateway.RerankResponse, error) {
	f.calls.Add(1)
	if f.err != nil {
		return gateway.RerankResponse{}, f.err
	}
	order := make([]int, 0, len(docs))
	if f.order != nil {
		order = f.order(query, docs)
	} else {
		for i := range docs {
			order = append(order, i)
		}
	}
	resp := gateway.RerankResponse{}
	for rank, idx := range order {
		resp.Hits = append(resp.Hits, protocol.RerankHit{Index: idx, Score: float64(len(order) - rank)})
	}
	return resp, nil
}

// unsupportedRerankError mirrors the gateway's permanent unsupported result.
func unsupportedRerankError() error {
	return &aierr.AIError{Kind: aierr.ErrKindUnsupported, Message: "rerank protocol is not implemented"}
}

// fakeParser returns fixed documents and counts calls.
type fakeParser struct {
	docs  map[string]PdfDocument
	err   error
	calls atomic.Int64
}

func (f *fakeParser) Parse(ctx context.Context, absPath string, opts ParseOptions) (PdfDocument, error) {
	f.calls.Add(1)
	if f.err != nil {
		return PdfDocument{}, f.err
	}
	if doc, ok := f.docs[absPath]; ok {
		return doc, nil
	}
	return PdfDocument{}, fmt.Errorf("no fake pdf for %s", absPath)
}

// ---------------------------------------------------------------------------
// Minimal PDF builder (real parser tests)
// ---------------------------------------------------------------------------

// buildPDF writes a minimal uncompressed multi-page PDF with Helvetica text.
func buildPDF(pages ...[]string) []byte {
	type object struct {
		body string
	}
	firstPageObj := 4
	// objects: 1 catalog, 2 pages, 3 font, then page/content pairs.
	var objects []object
	objects = append(objects,
		object{"<< /Type /Catalog /Pages 2 0 R >>"},
		object{""}, // filled below
		object{"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"},
	)
	var kids []string
	for i := range pages {
		pageObj := firstPageObj + 2*i
		contentObj := pageObj + 1
		kids = append(kids, fmt.Sprintf("%d 0 R", pageObj))
		objects = append(objects, object{fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>",
			contentObj)})
		var stream strings.Builder
		y := 720.0
		for _, line := range pages[i] {
			fmt.Fprintf(&stream, "BT /F1 12 Tf 72 %.0f Td (%s) Tj ET\n", y, escapePDFText(line))
			y -= 28
		}
		body := stream.String()
		objects = append(objects, object{fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(body), body)})
	}
	objects[1] = object{fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages))}

	var buf strings.Builder
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for i, obj := range objects {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, obj.body)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objects)+1)
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return []byte(buf.String())
}

// escapePDFText escapes the characters that end a PDF literal string.
func escapePDFText(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`)
	return r.Replace(s)
}

// writeFile writes raw bytes into the workspace and returns the absolute path.
func writeFile(t *testing.T, ws *workspace.Workspace, rel string, body []byte) string {
	t.Helper()
	abs, err := ws.Paths().Abs(rel)
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return abs
}

// waitFor polls cond until it holds or the timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// activityCount counts activity rows of a project.
func activityCount(t *testing.T, ws *workspace.Workspace, projectID string) int {
	t.Helper()
	handle, err := ws.Project(context.Background(), projectID)
	if err != nil {
		t.Fatalf("project handle: %v", err)
	}
	return countRows(t, handle.DB(), `SELECT COUNT(*) FROM activities`)
}

// activityTypes returns the event types of a project (for diagnostics).
func activityTypes(t *testing.T, ws *workspace.Workspace, projectID string) []string {
	t.Helper()
	handle, err := ws.Project(context.Background(), projectID)
	if err != nil {
		t.Fatalf("project handle: %v", err)
	}
	rows, err := handle.DB().Query(`SELECT event_type FROM activities ORDER BY created_at`)
	if err != nil {
		t.Fatalf("activities: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("activities: %v", err)
		}
		out = append(out, v)
	}
	return out
}

// emitEvent writes a standalone activity row.
func emitEvent(t *testing.T, ws *workspace.Workspace, projectID string, typ events.Type, entityType events.EntityType, entityID string) {
	t.Helper()
	handle, err := ws.Project(context.Background(), projectID)
	if err != nil {
		t.Fatalf("project handle: %v", err)
	}
	if err := handle.Events().EmitStandalone(context.Background(), events.Event{
		Type:       typ,
		EntityType: entityType,
		EntityID:   entityID,
		Summary:    "test",
		Actor:      events.Actor("user"),
	}); err != nil {
		t.Fatalf("emit event: %v", err)
	}
}
