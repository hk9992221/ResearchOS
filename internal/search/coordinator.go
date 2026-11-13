package search

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/indexhook"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/workspace"
	"researchos/internal/index/store"
)

// ---------------------------------------------------------------------------
// Dirty queue rows
// ---------------------------------------------------------------------------

// dirtyRow is one persisted dirty-queue entry.
type dirtyRow struct {
	ProjectID  string
	EntityType string
	EntityID   string
	Op         string
	Attempts   int
	RunAfter   string
	LastError  string
	CreatedAt  string
}

// queueColumns is the uniform column list of both dirty queues.
const queueColumns = `project_id, entity_type, entity_id, op, attempts, run_after, COALESCE(last_error,''), created_at`

// scanDirtyRows reads dirty rows for one scope database.
func scanDirtyRows(rows *sql.Rows) ([]dirtyRow, error) {
	defer rows.Close()
	var out []dirtyRow
	for rows.Next() {
		var r dirtyRow
		if err := rows.Scan(&r.ProjectID, &r.EntityType, &r.EntityID, &r.Op, &r.Attempts, &r.RunAfter, &r.LastError, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// dueRows returns the rows of one scope that are ready to run.
func dueRows(ctx context.Context, sc *indexScope, force bool, maxAttempts int, now time.Time) ([]dirtyRow, error) {
	query := fmt.Sprintf(`SELECT %s FROM %s`, queueColumns, sc.tables.dirty)
	var args []any
	if !force {
		query += ` WHERE run_after <= ? AND attempts < ?`
		args = append(args, clock.Format(now), maxAttempts)
	}
	query += ` ORDER BY run_after, entity_id`
	rows, err := sc.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search: 读取脏队列: %w", err)
	}
	return scanDirtyRows(rows)
}

// upsertDirty writes/refreshes one dirty row.
func upsertDirty(ctx context.Context, sc *indexScope, ch Change, debounce time.Duration, now time.Time) error {
	runAfter := clock.Format(now.Add(debounce))
	_, err := sc.db.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO %s (project_id, entity_type, entity_id, op, attempts, run_after, last_error, created_at, updated_at)
			VALUES (?, ?, ?, ?, 0, ?, NULL, ?, ?)
			ON CONFLICT(project_id, entity_type, entity_id) DO UPDATE SET
				op = excluded.op, attempts = 0, run_after = excluded.run_after,
				last_error = NULL, updated_at = excluded.updated_at`, sc.tables.dirty),
		ch.ProjectID, string(ch.EntityType), ch.EntityID, string(ch.Kind), runAfter, clock.Format(now), clock.Format(now))
	if err != nil {
		return fmt.Errorf("search: 写入脏队列: %w", err)
	}
	return nil
}

// rescheduleDirty applies exponential backoff after a failed attempt.
func rescheduleDirty(ctx context.Context, sc *indexScope, row dirtyRow, attempts int, runAfter time.Time, now time.Time, cause error) error {
	_, err := sc.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s SET attempts = ?, run_after = ?, last_error = ?, updated_at = ? WHERE project_id = ? AND entity_type = ? AND entity_id = ?`,
			sc.tables.dirty),
		attempts, clock.Format(runAfter), cause.Error(), clock.Format(now),
		row.ProjectID, row.EntityType, row.EntityID)
	if err != nil {
		return fmt.Errorf("search: 更新脏队列: %w", err)
	}
	return nil
}

// deleteDirty removes one dirty row.
func deleteDirty(ctx context.Context, sc *indexScope, row dirtyRow) error {
	_, err := sc.db.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM %s WHERE project_id = ? AND entity_type = ? AND entity_id = ?`, sc.tables.dirty),
		row.ProjectID, row.EntityType, row.EntityID)
	if err != nil {
		return fmt.Errorf("search: 删除脏队列行: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Runner
// ---------------------------------------------------------------------------

// runner is the single writer of one scope. It serializes every keyword and
// vector write of its database (SPEC-10 §5.3: single-writer, serial commits).
type runner struct {
	svc   *Service
	scope *indexScope

	mu      sync.Mutex
	wake    chan struct{}
	stop    chan struct{}
	stopped bool
	busy    atomic.Bool
}

// runnerFor returns (starting if needed) the runner of a scope.
func (s *Service) runnerFor(sc *indexScope) (*runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("search: service is closed")
	}
	if r, ok := s.runners[sc.key]; ok {
		return r, nil
	}
	r := &runner{
		svc:   s,
		scope: sc,
		wake:  make(chan struct{}, 1),
		stop:  make(chan struct{}),
	}
	s.runners[sc.key] = r
	go r.loop()
	return r, nil
}

// signal nudges the loop to recompute its next due time.
func (r *runner) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// stopOnce closes the stop channel exactly once.
func (r *runner) stopOnce() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.stopped {
		r.stopped = true
		close(r.stop)
	}
}

// loop waits for due dirty rows and processes them serially.
func (r *runner) loop() {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		select {
		case <-r.stop:
			return
		default:
		}
		delay, ok := r.nextDelay()
		var wait <-chan time.Time
		if ok && delay <= 0 {
			r.processDue(r.svc.baseCtx, false)
			continue
		}
		if ok {
			timer.Reset(delay)
			wait = timer.C
		}
		select {
		case <-r.wake:
			if !timer.Stop() && ok {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-wait:
		case <-r.stop:
			return
		}
	}
}

// nextDelay returns the delay until the next due row.
func (r *runner) nextDelay() (time.Duration, bool) {
	var raw sql.NullString
	err := r.scope.db.QueryRow(
		fmt.Sprintf(`SELECT MIN(run_after) FROM %s WHERE attempts < ?`, r.scope.tables.dirty),
		r.svc.cfg.maxAttempts).Scan(&raw)
	if err != nil || !raw.Valid || raw.String == "" {
		return 0, false
	}
	due, err := clock.Parse(raw.String)
	if err != nil {
		return 0, true
	}
	return due.Sub(r.svc.cfg.Clock.Now()), true
}

// processDue processes the currently due dirty rows. force also processes rows
// still inside their debounce/backoff window (Flush).
func (r *runner) processDue(ctx context.Context, force bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	r.busy.Store(true)
	defer r.busy.Store(false)
	rows, err := dueRows(ctx, r.scope, force, r.svc.cfg.maxAttempts, r.svc.cfg.Clock.Now())
	if err != nil {
		if r.svc.cfg.Logger != nil {
			r.svc.cfg.Logger.Warn("search: 读取脏队列失败", "scope", r.scope.key, "error", err)
		}
		return
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return
		}
		r.processRow(ctx, row)
	}
}

// processRow indexes one dirty row, applying retries and the terminal failure.
func (r *runner) processRow(ctx context.Context, row dirtyRow) {
	if row.Attempts >= r.svc.cfg.maxAttempts {
		return // terminal failure, waiting for a manual reindex
	}
	ch := Change{
		ProjectID:  row.ProjectID,
		EntityType: EntityType(row.EntityType),
		EntityID:   row.EntityID,
		Kind:       ChangeKind(row.Op),
	}
	var err error
	switch ch.Kind {
	case ChangeDelete:
		err = r.svc.markDeleted(ctx, r.scope, ch.EntityType, ch.EntityID)
	case ChangeUpsert, ChangeRestore:
		err = r.svc.indexEntity(ctx, r.scope, ch)
	default:
		err = Errorf(CodeInvalidRequest, "未知的索引变更 %q", row.Op)
	}
	if err == nil {
		if delErr := deleteDirty(ctx, r.scope, row); delErr != nil && r.svc.cfg.Logger != nil {
			r.svc.cfg.Logger.Warn("search: 清理脏队列失败", "scope", r.scope.key, "error", delErr)
		}
		return
	}
	attempts := row.Attempts + 1
	if attempts >= r.svc.cfg.maxAttempts {
		// Keep the row as the visible terminal failure (Status.Failed and
		// LastError) instead of losing the cause; a manual reindex clears it.
		if err := rescheduleDirty(ctx, r.scope, row, attempts, r.svc.cfg.Clock.Now(), r.svc.cfg.Clock.Now(), err); err != nil && r.svc.cfg.Logger != nil {
			r.svc.cfg.Logger.Warn("search: 记录索引失败失败", "scope", r.scope.key, "error", err)
		}
		if failErr := r.svc.markFailed(ctx, r.scope, ch.EntityType, ch.EntityID, err); failErr != nil && r.svc.cfg.Logger != nil {
			r.svc.cfg.Logger.Warn("search: 标记索引失败", "scope", r.scope.key, "error", failErr)
		}
		r.svc.cfg.Logger.Warn("search: 索引失败已达重试上限", "scope", r.scope.key,
			"entity_type", row.EntityType, "entity_id", row.EntityID, "error", err)
		return
	}
	backoff := r.svc.cfg.backoff[min(attempts-1, len(r.svc.cfg.backoff)-1)]
	if err := rescheduleDirty(ctx, r.scope, row, attempts, r.svc.cfg.Clock.Now().Add(backoff), r.svc.cfg.Clock.Now(), err); err != nil && r.svc.cfg.Logger != nil {
		r.svc.cfg.Logger.Warn("search: 重排索引重试失败", "scope", r.scope.key, "error", err)
	}
	r.signal()
}

// indexEntity extracts and writes every document of one entity. An entity
// that no longer exists is marked deleted, keeping include_deleted retrieval.
func (s *Service) indexEntity(ctx context.Context, sc *indexScope, ch Change) error {
	docs, problems, err := s.ex.entityDocs(ctx, sc.projectID, ch.EntityType, ch.EntityID, extractOptions{})
	if err != nil {
		return err
	}
	for _, problem := range problems {
		if markErr := s.markProblem(ctx, sc, problem.DocID, problem.Err); markErr != nil {
			return markErr
		}
	}
	if len(docs) == 0 && len(problems) == 0 {
		return s.markDeleted(ctx, sc, ch.EntityType, ch.EntityID)
	}
	for _, doc := range docs {
		if err := s.applyDoc(ctx, sc, doc, writeOptions{}); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Notify / Flush
// ---------------------------------------------------------------------------

// Notify enqueues one post-commit change signal. It only writes the persisted
// dirty queue and arms the debounce timer; no extraction or heavy work happens
// here (SPEC-10 §5.3).
func (s *Service) Notify(ctx context.Context, ch Change) error {
	if !ch.EntityType.Valid() {
		return Errorf(CodeInvalidRequest, "不索引的实体类型 %q", string(ch.EntityType))
	}
	if ch.Kind == "" {
		ch.Kind = ChangeUpsert
	}
	if ch.ProjectID == "" && ch.EntityType != EntityPaper {
		return Errorf(CodeInvalidRequest, "%s 变更必须带 project_id", string(ch.EntityType))
	}
	switch ch.Kind {
	case ChangeUpsert, ChangeDelete, ChangeRestore:
	default:
		return Errorf(CodeInvalidRequest, "未知的索引变更 %q", string(ch.Kind))
	}
	if ch.At.IsZero() {
		ch.At = s.cfg.Clock.Now()
	}
	sc, err := s.resolveScope(ctx, scopeOfChange(ch))
	if err != nil {
		return err
	}
	if err := upsertDirty(ctx, sc, ch, s.cfg.debounce, s.cfg.Clock.Now()); err != nil {
		return err
	}
	r, err := s.runnerFor(sc)
	if err != nil {
		return err
	}
	r.signal()
	return nil
}

// scopeOfChange maps a change to its index scope.
func scopeOfChange(ch Change) Scope {
	if ch.ProjectID == "" {
		return Scope{Kind: ScopeGlobal}
	}
	return Scope{Kind: ScopeProject, ProjectID: ch.ProjectID}
}

// Flush processes the dirty documents of a scope immediately. A zero scope
// flushes every scope that has queued work (application exit / blur).
func (s *Service) Flush(ctx context.Context, scope Scope) error {
	if scope.Zero() {
		return s.flushAll(ctx)
	}
	sc, err := s.resolveScope(ctx, scope)
	if err != nil {
		return err
	}
	r, err := s.runnerFor(sc)
	if err != nil {
		return err
	}
	r.processDue(ctx, true)
	return nil
}

// flushAll flushes the global scope and every registered project.
func (s *Service) flushAll(ctx context.Context) error {
	scopes := []Scope{{Kind: ScopeGlobal}}
	rows, err := s.ws.Global().DB().QueryContext(ctx, `SELECT id FROM projects ORDER BY id`)
	if err != nil {
		return fmt.Errorf("search: 枚举项目: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("search: 枚举项目: %w", err)
		}
		scopes = append(scopes, Scope{Kind: ScopeProject, ProjectID: id})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("search: 枚举项目: %w", err)
	}
	rows.Close()
	var errList []error
	for _, scope := range scopes {
		sc, err := s.resolveScope(ctx, scope)
		if err != nil {
			errList = append(errList, err)
			continue
		}
		r, err := s.runnerFor(sc)
		if err != nil {
			errList = append(errList, err)
			continue
		}
		r.processDue(ctx, true)
	}
	return errors.Join(errList...)
}

// ---------------------------------------------------------------------------
// Start / reconciliation
// ---------------------------------------------------------------------------

// Start prepares the index after the workspace opens: it syncs the source
// version registry, marks version-mismatched documents stale and enqueues them,
// and restarts the workers so a crash-resumed dirty queue is processed
// (SPEC-10 §5.3, A16).
func (s *Service) Start(ctx context.Context) error {
	scopes := []Scope{{Kind: ScopeGlobal}}
	rows, err := s.ws.Global().DB().QueryContext(ctx, `SELECT id FROM projects ORDER BY id`)
	if err != nil {
		return fmt.Errorf("search: 启动索引: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("search: 启动索引: %w", err)
		}
		scopes = append(scopes, Scope{Kind: ScopeProject, ProjectID: id})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("search: 启动索引: %w", err)
	}
	rows.Close()

	s.applySettings(ctx)
	var errList []error
	for _, scope := range scopes {
		sc, err := s.resolveScope(ctx, scope)
		if err != nil {
			errList = append(errList, err)
			continue
		}
		if err := s.syncSourceRegistry(ctx, sc); err != nil {
			errList = append(errList, err)
		}
		if err := s.reconcile(ctx, sc); err != nil {
			errList = append(errList, err)
		}
		r, err := s.runnerFor(sc)
		if err != nil {
			errList = append(errList, err)
			continue
		}
		r.signal()
	}
	return errors.Join(errList...)
}

// syncSourceRegistry refreshes the parser/schema baseline of every source.
func (s *Service) syncSourceRegistry(ctx context.Context, sc *indexScope) error {
	rows, err := sc.db.QueryContext(ctx, fmt.Sprintf(`SELECT source FROM %s ORDER BY source`, sc.tables.sources))
	if err != nil {
		return fmt.Errorf("search: 读取源注册: %w", err)
	}
	var sources []string
	for rows.Next() {
		var source string
		if err := rows.Scan(&source); err != nil {
			rows.Close()
			return fmt.Errorf("search: 读取源注册: %w", err)
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("search: 读取源注册: %w", err)
	}
	rows.Close()
	for _, source := range sources {
		_, err := sc.db.ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s SET schema_version = ?, parser_version = ?, updated_at = ? WHERE source = ?`, sc.tables.sources),
			schemaVersion, parserVersionFor(SourceType(source)), clock.Format(s.cfg.Clock.Now()), source)
		if err != nil {
			return fmt.Errorf("search: 更新源注册 %s: %w", source, err)
		}
	}
	return nil
}

// parserVersionFor returns the current parser version of a source.
func parserVersionFor(source SourceType) string {
	if source == SourcePaperFullText {
		return parserVersionPDF
	}
	return parserVersionMarkdown
}

// reconcile marks version-mismatched documents stale and enqueues them
// (SPEC-10 §5.3). A document is stale when its schema version, parser version
// or embedding profile no longer matches the current baseline; a no_text
// document carries no vectors and therefore ignores the profile check.
func (s *Service) reconcile(ctx context.Context, sc *indexScope) error {
	exp, err := s.expectedEmbeddingModel(ctx)
	if err != nil {
		return err
	}
	rows, err := sc.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT doc_id, source, entity_type, entity_id, index_status, schema_version, parser_version, embedding_model_id
			FROM %s WHERE index_status IN (?, ?, ?)`, sc.tables.docs),
		StatusOK, StatusVectorPending, StatusNoText)
	if err != nil {
		return fmt.Errorf("search: 版本对账: %w", err)
	}
	type docRef struct {
		docID, source, entityType, entityID string
		status                              string
		schemaVersion                       int
		parserVersion, embeddingModel       string
	}
	var refs []docRef
	for rows.Next() {
		var ref docRef
		if err := rows.Scan(&ref.docID, &ref.source, &ref.entityType, &ref.entityID,
			&ref.status, &ref.schemaVersion, &ref.parserVersion, &ref.embeddingModel); err != nil {
			rows.Close()
			return fmt.Errorf("search: 版本对账: %w", err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("search: 版本对账: %w", err)
	}
	rows.Close()
	for _, ref := range refs {
		stale := ref.schemaVersion != schemaVersion || ref.parserVersion != parserVersionFor(SourceType(ref.source))
		if !stale && exp != "" && ref.status == StatusOK && ref.embeddingModel != "" && ref.embeddingModel != exp {
			stale = true // never mix vector spaces (SPEC-10 §5.3)
		}
		if !stale {
			continue
		}
		if _, err := sc.db.ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s SET index_status = ? WHERE doc_id = ?`, sc.tables.docs),
			StatusStale, ref.docID); err != nil {
			return fmt.Errorf("search: 标记陈旧文档 %s: %w", ref.docID, err)
		}
		ch := Change{
			ProjectID:  sc.projectID,
			EntityType: EntityType(ref.entityType),
			EntityID:   ref.entityID,
			Kind:       ChangeUpsert,
			At:         s.cfg.Clock.Now(),
		}
		if err := upsertDirty(ctx, sc, ch, 0, s.cfg.Clock.Now()); err != nil {
			return err
		}
	}
	return nil
}

// expectedEmbeddingModel returns the current embedding profile ("" when the
// embedding role is unavailable).
func (s *Service) expectedEmbeddingModel(ctx context.Context) (string, error) {
	if s.cfg.Embedding == nil {
		return "", nil
	}
	id, _, err := s.cfg.Embedding.CurrentProfile(ctx)
	if err != nil {
		return "", nil
	}
	return id, nil
}

// applySettings reads the search.* workspace settings and overrides defaults.
// Missing or invalid keys keep the code default (SPEC-01 §5.5: no registry).
func (s *Service) applySettings(ctx context.Context) {
	db := s.ws.Global().DB()
	read := func(key string) (string, bool) {
		var value string
		err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
		if err != nil {
			return "", false
		}
		return value, true
	}
	if raw, ok := read("search.debounce_ms"); ok {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 && n <= 60000 {
			s.cfg.debounce = time.Duration(n) * time.Millisecond
		}
	}
	if raw, ok := read("search.keyword_topk"); ok {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 500 {
			s.cfg.keywordTopK = n
		}
	}
	if raw, ok := read("search.vector_topk"); ok {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 500 {
			s.cfg.vectorTopK = n
		}
	}
	if raw, ok := read("search.rerank_topn"); ok {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
			s.cfg.rerankTopN = n
		}
	}
	if raw, ok := read("search.project_timeout_ms"); ok {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 60000 {
			s.cfg.projectTimeout = time.Duration(n) * time.Millisecond
		}
	}
	if raw, ok := read("search.snippet_runes"); ok {
		if n, err := strconv.Atoi(raw); err == nil && n >= 50 && n <= 1000 {
			s.cfg.snippetRunes = n
		}
	}
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

// Status reports index health for one scope (SPEC-10 §5.3).
func (s *Service) Status(ctx context.Context, scope Scope) (IndexStatus, error) {
	sc, err := s.resolveScope(ctx, scope)
	if err != nil {
		return IndexStatus{}, err
	}
	status := IndexStatus{Scope: scope}
	if err := sc.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE index_status <> ?`, sc.tables.docs), StatusDeleted).
		Scan(&status.Docs); err != nil {
		return IndexStatus{}, fmt.Errorf("search: 统计文档: %w", err)
	}
	if err := sc.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM (SELECT c.doc_id FROM %s c
			JOIN %s d ON d.doc_id = c.doc_id WHERE d.index_status <> ?)`, sc.tables.chunks, sc.tables.docs), StatusDeleted).
		Scan(&status.Chunks); err != nil {
		return IndexStatus{}, fmt.Errorf("search: 统计分块: %w", err)
	}
	if err := sc.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM %s`, sc.tables.dirty)).Scan(&status.Dirty); err != nil {
		return IndexStatus{}, fmt.Errorf("search: 统计脏队列: %w", err)
	}
	if err := sc.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE index_status = ?`, sc.tables.docs), StatusFailed).
		Scan(&status.Failed); err != nil {
		return IndexStatus{}, fmt.Errorf("search: 统计失败文档: %w", err)
	}
	var lastIndexed, lastError sql.NullString
	if err := sc.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT MAX(indexed_at), (SELECT error FROM %s WHERE error IS NOT NULL AND error <> '' ORDER BY indexed_at DESC LIMIT 1) FROM %s`,
			sc.tables.docs, sc.tables.docs)).Scan(&lastIndexed, &lastError); err != nil {
		return IndexStatus{}, fmt.Errorf("search: 统计索引时间: %w", err)
	}
	if lastIndexed.Valid && lastIndexed.String != "" {
		if t, err := clock.Parse(lastIndexed.String); err == nil {
			status.LastIndexedAt = t
		}
	}
	if lastError.Valid {
		status.LastError = lastError.String
	}
	status.SchemaVersion = strconv.Itoa(schemaVersion)
	status.ParserVersion = s.parserVersions(ctx, sc)
	if profile, _, ok := s.profile(ctx); ok {
		status.EmbeddingModel = profile
	}
	var staleCount int
	if err := sc.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE index_status = ?`, sc.tables.docs), StatusStale).
		Scan(&staleCount); err != nil {
		return IndexStatus{}, fmt.Errorf("search: 统计陈旧文档: %w", err)
	}
	status.Stale = staleCount > 0
	var exhausted int
	var queueError sql.NullString
	if err := sc.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT
			COALESCE(SUM(CASE WHEN attempts >= ? THEN 1 ELSE 0 END), 0),
			(SELECT last_error FROM %s WHERE last_error IS NOT NULL AND last_error <> '' ORDER BY updated_at DESC LIMIT 1)
		 FROM %s`, sc.tables.dirty, sc.tables.dirty),
		s.cfg.maxAttempts).Scan(&exhausted, &queueError); err != nil {
		return IndexStatus{}, fmt.Errorf("search: 统计失败索引: %w", err)
	}
	status.Failed += exhausted
	if queueError.Valid && status.LastError == "" {
		status.LastError = queueError.String
	}

	if job, ok, err := latestJob(ctx, sc); err != nil {
		return IndexStatus{}, err
	} else if ok {
		status.Indexing = job.Status == "pending" || job.Status == "running"
		if job.Total > 0 {
			status.Progress = float64(job.Processed) / float64(job.Total)
			if status.Progress > 1 {
				status.Progress = 1
			}
		}
		if job.LastError != "" {
			status.LastError = job.LastError
		}
	}
	s.mu.Lock()
	if r, ok := s.runners[sc.key]; ok && r.busy.Load() {
		status.Indexing = true
	}
	s.mu.Unlock()
	return status, nil
}

// parserVersions joins the distinct parser versions of the source registry.
func (s *Service) parserVersions(ctx context.Context, sc *indexScope) string {
	rows, err := sc.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT DISTINCT parser_version FROM %s WHERE parser_version <> '' ORDER BY parser_version`, sc.tables.sources))
	if err != nil {
		return parserVersionMarkdown
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return parserVersionMarkdown
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return parserVersionMarkdown
	}
	return strings.Join(out, ",")
}

// ---------------------------------------------------------------------------
// Jobs
// ---------------------------------------------------------------------------

// jobRow is one index job.
type jobRow struct {
	JobID           string
	ScopeKind       string
	ScopeID         string
	ReparsePDF      bool
	RebuildVector   bool
	Status          string
	Total           int
	Processed       int
	Failed          int
	LastError       string
	CancelRequested bool
}

// latestJob returns the newest job of a scope.
func latestJob(ctx context.Context, sc *indexScope) (jobRow, bool, error) {
	var job jobRow
	err := sc.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT job_id, scope_kind, scope_id, reparse_pdf, rebuild_vector, status, total, processed,
			failed, COALESCE(last_error,''), cancel_requested FROM %s
			ORDER BY created_at DESC, job_id DESC LIMIT 1`, sc.tables.jobs)).
		Scan(&job.JobID, &job.ScopeKind, &job.ScopeID, &job.ReparsePDF, &job.RebuildVector, &job.Status,
			&job.Total, &job.Processed, &job.Failed, &job.LastError, &job.CancelRequested)
	if errors.Is(err, sql.ErrNoRows) {
		return jobRow{}, false, nil
	}
	if err != nil {
		return jobRow{}, false, fmt.Errorf("search: 读取索引作业: %w", err)
	}
	return job, true, nil
}

// Reindex starts a background rebuild job and returns its id (SPEC-10 §5.3).
func (s *Service) Reindex(ctx context.Context, req ReindexRequest) (JobID, error) {
	sc, err := s.resolveScope(ctx, req.Scope)
	if err != nil {
		return "", err
	}
	jobID, err := ids.New(s.cfg.Clock)
	if err != nil {
		return "", fmt.Errorf("search: 生成作业 ID: %w", err)
	}
	scopeKind := "project"
	if req.Scope.Kind == ScopeGlobal {
		scopeKind = "global"
	}
	_, err = sc.db.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO %s (job_id, scope_kind, scope_id, reparse_pdf, rebuild_vector, status, created_at)
			VALUES (?, ?, ?, ?, ?, 'pending', ?)`, sc.tables.jobs),
		jobID, scopeKind, req.Scope.ProjectID, boolInt(req.ReparsePDF), boolInt(req.RebuildVector),
		clock.Format(s.cfg.Clock.Now()))
	if err != nil {
		return "", fmt.Errorf("search: 创建索引作业: %w", err)
	}
	job := jobRow{
		JobID: jobID, ScopeKind: scopeKind, ScopeID: req.Scope.ProjectID,
		ReparsePDF: req.ReparsePDF, RebuildVector: req.RebuildVector, Status: "pending",
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", errors.New("search: service is closed")
	}
	s.mu.Unlock()
	go s.runJob(sc, job)
	return JobID(jobID), nil
}

// boolInt converts a bool to the SQLite integer.
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// runJob executes one rebuild serially with the scope's normal writer.
func (s *Service) runJob(sc *indexScope, job jobRow) {
	ctx := s.baseCtx
	r, err := s.runnerFor(sc)
	if err != nil {
		return
	}
	r.busy.Store(true)
	defer r.busy.Store(false)
	// The job takes the single-writer lock per document instead of for its
	// whole lifetime, so live user edits queued by Notify interleave with the
	// rebuild (SPEC-10 §5.3 priority: visible documents before bulk work).
	writeDoc := func(doc IndexDoc, opt writeOptions) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.stopped {
			return context.Canceled
		}
		return s.applyDoc(ctx, sc, doc, opt)
	}
	s.updateJob(ctx, sc, job.JobID, "running", 0, 0, 0, "")
	opt := extractOptions{ReparsePDF: job.ReparsePDF}
	var (
		docs     []IndexDoc
		problems []extractProblem
	)
	if sc.partition == store.PartitionGlobal {
		docs, problems, err = s.ex.allPaperDocs(ctx, opt)
	} else {
		docs, problems, err = s.ex.allProjectDocs(ctx, sc.projectID, opt)
	}
	if err != nil {
		s.finishJob(ctx, sc, job.JobID, "failed", 0, 0, err)
		return
	}
	total := len(docs) + len(problems)
	s.updateJob(ctx, sc, job.JobID, "running", total, 0, 0, "")
	processed, failed := 0, 0
	seen := make(map[string]bool, len(docs))
	for _, doc := range docs {
		if s.jobCanceled(ctx, sc, job.JobID) {
			s.finishJob(ctx, sc, job.JobID, "canceled", processed, failed, nil)
			return
		}
		seen[doc.DocID] = true
		writeOpt := writeOptions{Force: job.ReparsePDF || job.RebuildVector}
		if job.RebuildVector {
			writeOpt.Force = true
		}
		if err := writeDoc(doc, writeOpt); err != nil {
			failed++
			if s.cfg.Logger != nil {
				s.cfg.Logger.Warn("search: 重建文档失败", "scope", sc.key, "doc", doc.DocID, "error", err)
			}
		}
		processed++
		s.updateProgress(ctx, sc, job.JobID, processed, failed)
	}
	for _, problem := range problems {
		if s.jobCanceled(ctx, sc, job.JobID) {
			s.finishJob(ctx, sc, job.JobID, "canceled", processed, failed, nil)
			return
		}
		r.mu.Lock()
		err := s.markProblem(ctx, sc, problem.DocID, problem.Err)
		r.mu.Unlock()
		if err != nil {
			failed++
		}
		seen[problem.DocID] = true
		processed++
		s.updateProgress(ctx, sc, job.JobID, processed, failed)
	}
	// Documents whose source entity disappeared while the app was down are
	// marked deleted rather than left searchable.
	r.mu.Lock()
	err = s.markVanished(ctx, sc, seen)
	r.mu.Unlock()
	if err != nil {
		failed++
	}
	s.finishJob(ctx, sc, job.JobID, "done", processed, failed, nil)
}

// markVanished marks indexed documents not produced by this rebuild deleted.
func (s *Service) markVanished(ctx context.Context, sc *indexScope, seen map[string]bool) error {
	rows, err := sc.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT doc_id FROM %s WHERE index_status <> ?`, sc.tables.docs), StatusDeleted)
	if err != nil {
		return fmt.Errorf("search: 读取现存文档: %w", err)
	}
	var vanished []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("search: 读取现存文档: %w", err)
		}
		if !seen[id] {
			vanished = append(vanished, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("search: 读取现存文档: %w", err)
	}
	rows.Close()
	for _, id := range vanished {
		if _, err := sc.db.ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s SET index_status = ?, error = NULL WHERE doc_id = ?`, sc.tables.docs),
			StatusDeleted, id); err != nil {
			return fmt.Errorf("search: 标记消失文档 %s: %w", id, err)
		}
	}
	return nil
}

// jobCanceled reads the cancel flag of a running job.
func (s *Service) jobCanceled(ctx context.Context, sc *indexScope, jobID string) bool {
	var cancel int
	if err := sc.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT cancel_requested FROM %s WHERE job_id = ?`, sc.tables.jobs), jobID).Scan(&cancel); err != nil {
		return ctx.Err() != nil
	}
	return cancel != 0 || ctx.Err() != nil
}

// updateJob writes the job status and counters.
func (s *Service) updateJob(ctx context.Context, sc *indexScope, jobID, status string, total, processed, failed int, errText string) {
	var errValue any
	if strings.TrimSpace(errText) != "" {
		errValue = errText
	}
	var started any
	if status == "running" {
		started = clock.Format(s.cfg.Clock.Now())
	}
	_, err := sc.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s SET status = ?, total = ?, processed = ?, failed = ?, last_error = ?,
			started_at = COALESCE(started_at, ?) WHERE job_id = ?`, sc.tables.jobs),
		status, total, processed, failed, errValue, started, jobID)
	if err != nil && s.cfg.Logger != nil {
		s.cfg.Logger.Warn("search: 更新索引作业失败", "job", jobID, "error", err)
	}
}

// updateProgress writes the processed/failed counters of a running job.
func (s *Service) updateProgress(ctx context.Context, sc *indexScope, jobID string, processed, failed int) {
	if _, err := sc.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s SET processed = ?, failed = ? WHERE job_id = ?`, sc.tables.jobs),
		processed, failed, jobID); err != nil && s.cfg.Logger != nil {
		s.cfg.Logger.Warn("search: 更新作业进度失败", "job", jobID, "error", err)
	}
}

// finishJob writes the terminal state and notifies the hook.
func (s *Service) finishJob(ctx context.Context, sc *indexScope, jobID, status string, processed, failed int, cause error) {
	errText := ""
	if cause != nil {
		errText = cause.Error()
	}
	var errValue any
	if errText != "" {
		errValue = errText
	}
	_, err := sc.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s SET status = ?, processed = ?, failed = ?, last_error = ?, finished_at = ? WHERE job_id = ?`,
			sc.tables.jobs),
		status, processed, failed, errValue, clock.Format(s.cfg.Clock.Now()), jobID)
	if err != nil && s.cfg.Logger != nil {
		s.cfg.Logger.Warn("search: 结束索引作业失败", "job", jobID, "error", err)
	}
	if s.cfg.hooks.JobFinished != nil {
		s.cfg.hooks.JobFinished(sc.key, jobID, status)
	}
}

// CancelJob requests cancellation of one job (SPEC-10 §5.3).
func (s *Service) CancelJob(ctx context.Context, jobID string) error {
	if jobID == "" {
		return Errorf(CodeInvalidRequest, "缺少作业 ID")
	}
	scopes, err := s.allScopes(ctx)
	if err != nil {
		return err
	}
	for _, scope := range scopes {
		sc, err := s.resolveScope(ctx, scope)
		if err != nil {
			continue
		}
		res, err := sc.db.ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s SET cancel_requested = 1,
				status = CASE WHEN status = 'pending' THEN 'canceled' ELSE status END,
				finished_at = CASE WHEN status = 'pending' THEN ? ELSE finished_at END
				WHERE job_id = ?`, sc.tables.jobs), clock.Format(s.cfg.Clock.Now()), jobID)
		if err != nil {
			return fmt.Errorf("search: 取消作业: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			if r, rerr := s.runnerFor(sc); rerr == nil && r != nil {
				r.signal()
			}
			return nil
		}
	}
	return WrapError(CodeJobNotFound, fmt.Sprintf("索引作业 %s 不存在", jobID), ErrJobNotFound)
}

// allScopes lists the global scope and every registered project.
func (s *Service) allScopes(ctx context.Context) ([]Scope, error) {
	scopes := []Scope{{Kind: ScopeGlobal}}
	rows, err := s.ws.Global().DB().QueryContext(ctx, `SELECT id FROM projects ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("search: 枚举项目: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("search: 枚举项目: %w", err)
		}
		scopes = append(scopes, Scope{Kind: ScopeProject, ProjectID: id})
	}
	return scopes, rows.Err()
}

// ---------------------------------------------------------------------------
// foundation indexhook.Indexer
// ---------------------------------------------------------------------------

// projectIndexer binds the generic indexhook to one project, so the entity
// services never need to know the project id inside their event.
type projectIndexer struct {
	svc       *Service
	projectID string
}

// ProjectIndexer returns the foundation indexhook.Indexer of one project. Wire
// it into notes/ideas/experiments ServiceOptions.Indexer.
func (s *Service) ProjectIndexer(projectID string) indexhook.Indexer {
	return &projectIndexer{svc: s, projectID: projectID}
}

// OnEntityChanged maps one post-commit activity to an index change (SPEC-10
// §7.2). A failure is returned to the caller, which must only log it.
func (p *projectIndexer) OnEntityChanged(ctx context.Context, ev events.Activity) error {
	entityType := EntityType(ev.EntityType)
	switch entityType {
	case EntityNote, EntityDBRecord, EntityIdea, EntityExperiment:
	default:
		return nil
	}
	kind := ChangeUpsert
	switch ev.Type {
	case schema.EventEntityDeleted:
		kind = ChangeDelete
	case schema.EventEntityRestored:
		kind = ChangeRestore
	case schema.EventEntityPurged:
		kind = ChangeDelete
	}
	return p.svc.Notify(ctx, Change{
		ProjectID:  p.projectID,
		EntityType: entityType,
		EntityID:   ev.EntityID,
		Kind:       kind,
		At:         p.svc.cfg.Clock.Now(),
	})
}

// Rebuild rebuilds one project's index (idempotent, SPEC-01 §4.7).
func (p *projectIndexer) Rebuild(ctx context.Context, scope indexhook.Scope) error {
	projectID := scope.ProjectID
	if projectID == "" {
		projectID = p.projectID
	}
	if projectID == "" {
		return Errorf(CodeInvalidRequest, "重建缺少项目 ID")
	}
	_, err := p.svc.Reindex(ctx, ReindexRequest{Scope: Scope{Kind: ScopeProject, ProjectID: projectID}})
	return err
}

// OnEntityChanged implements indexhook.Indexer for global literature signals.
func (s *Service) OnEntityChanged(ctx context.Context, ev events.Activity) error {
	if EntityType(ev.EntityType) != EntityPaper {
		return nil
	}
	kind := ChangeUpsert
	switch ev.Type {
	case schema.EventEntityDeleted, schema.EventEntityPurged:
		kind = ChangeDelete
	case schema.EventEntityRestored:
		kind = ChangeRestore
	}
	return s.Notify(ctx, Change{
		EntityType: EntityPaper,
		EntityID:   ev.EntityID,
		Kind:       kind,
		At:         s.cfg.Clock.Now(),
	})
}

// Rebuild implements indexhook.Indexer for the global literature index.
func (s *Service) Rebuild(ctx context.Context, scope indexhook.Scope) error {
	_, err := s.Reindex(ctx, ReindexRequest{Scope: Scope{Kind: ScopeGlobal}})
	return err
}

var (
	_ indexhook.Indexer = (*projectIndexer)(nil)
	_ indexhook.Indexer = (*Service)(nil)
	_ workspace.Logger  = slogDiscard{}
)
