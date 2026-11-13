package mcpstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"researchos/internal/foundation/ids"
	"researchos/internal/mcp"
)

// Concurrency limits of §5.6.
const (
	maxWritesPerProject = 2
	maxWritesGlobal     = 4
)

const jobColumns = `id, tool, project_id, client_id, state, progress, stage, read_only,
	params_json, result_json, error_json, created_at, started_at, finished_at, trace_id`

// Submit implements mcp.JobRunner. It enforces the write-job concurrency caps
// synchronously so the caller sees RATE_LIMITED immediately.
func (s *Jobs) Submit(ctx context.Context, p mcp.Principal, req mcp.JobRequest) (mcp.Job, error) {
	db := s.db()
	if db == nil {
		return mcp.Job{}, mcp.Errorf(mcp.CodeInternal, "作业运行器不可用")
	}
	if req.Run == nil {
		return mcp.Job{}, mcp.Errorf(mcp.CodeInternal, "作业缺少执行体")
	}
	clientID := p.ClientID
	if clientID == "" {
		clientID = "local"
	}
	id, err := ids.New(s.clock)
	if err != nil {
		return mcp.Job{}, mcp.Errorf(mcp.CodeInternal, "生成作业编号失败")
	}
	params := req.Params
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	now := s.now()

	s.mu.Lock()
	if !req.ReadOnly {
		if s.totalWrite >= maxWritesGlobal || s.projectWrite[req.ProjectID] >= maxWritesPerProject {
			s.mu.Unlock()
			return mcp.Job{}, mcp.Errorf(mcp.CodeRateLimited, "后台写作业并发已达上限（每项目 2、全局 4），请稍后重试").
				WithDetail("retry_after_ms", 5000)
		}
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO mcp_job (id, tool, project_id, client_id, state, progress, stage, read_only,
			params_json, created_at, trace_id)
		VALUES (?, ?, ?, ?, 'queued', 0, '排队中', ?, ?, ?, ?)`,
		id, req.Tool, nullText(req.ProjectID), clientID, boolInt(req.ReadOnly), string(params),
		timeText(now), nullText(req.TraceID))
	if err != nil {
		s.mu.Unlock()
		return mcp.Job{}, mcp.Errorf(mcp.CodeInternal, "创建作业失败").Wrap(err)
	}
	if !req.ReadOnly {
		s.totalWrite++
		s.projectWrite[req.ProjectID]++
	}
	s.mu.Unlock()

	jobCtx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancels[id] = cancel
	s.mu.Unlock()

	go s.runJob(jobCtx, id, req)

	return mcp.Job{
		ID:        id,
		Tool:      req.Tool,
		ProjectID: req.ProjectID,
		ClientID:  clientID,
		State:     mcp.JobQueued,
		Progress:  0,
		Stage:     "排队中",
		ReadOnly:  req.ReadOnly,
		Params:    params,
		CreatedAt: now,
		TraceID:   req.TraceID,
	}, nil
}

func (s *Jobs) runJob(ctx context.Context, id string, req mcp.JobRequest) {
	db := s.db()
	started := s.now()
	if db != nil {
		_, _ = db.ExecContext(context.Background(), `
			UPDATE mcp_job SET state = 'running', started_at = ?, stage = '开始执行'
			WHERE id = ? AND state = 'queued'`, timeText(started), id)
	}

	var (
		progressMu sync.Mutex
		lastPct    = 0
	)
	reportProgress := func(percent int, stage string) {
		if percent < 0 {
			percent = 0
		}
		if percent > 100 {
			percent = 100
		}
		progressMu.Lock()
		if percent < lastPct {
			percent = lastPct
		}
		lastPct = percent
		progressMu.Unlock()
		_, _ = db.ExecContext(context.Background(), `
			UPDATE mcp_job SET progress = ?, stage = ? WHERE id = ? AND state IN ('queued','running')`,
			percent, stage, id)
	}

	result, runErr := invokeJob(ctx, req.Run, reportProgress)

	state := mcp.JobSucceeded
	var errBody any
	switch {
	case ctx.Err() != nil:
		state = mcp.JobCancelled
	case runErr != nil:
		state = mcp.JobFailed
		e := mcp.AsError(runErr)
		errBody = map[string]any{"code": e.Code, "message": e.Message, "details": e.Details}
	default:
		state = mcp.JobSucceeded
	}

	var resultJSON any
	if result != nil {
		if raw, err := json.Marshal(result); err == nil {
			resultJSON = string(raw)
		}
	}
	var errorJSON any
	if errBody != nil {
		if raw, err := json.Marshal(errBody); err == nil {
			errorJSON = string(raw)
		}
	}
	_, _ = db.ExecContext(context.Background(), `
		UPDATE mcp_job SET state = ?, progress = ?, stage = ?, result_json = ?, error_json = ?, finished_at = ?
		WHERE id = ?`,
		string(state), terminalProgress(state, lastPct), terminalStage(state),
		resultJSON, errorJSON, timeText(s.now()), id)

	s.mu.Lock()
	delete(s.cancels, id)
	if !req.ReadOnly {
		s.totalWrite--
		s.projectWrite[req.ProjectID]--
	}
	s.mu.Unlock()
}

func invokeJob(ctx context.Context, run func(context.Context, mcp.ProgressFunc) (any, error), progress mcp.ProgressFunc) (result any, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if s, ok := recovered.(error); ok {
				err = mcp.Errorf(mcp.CodeInternal, "作业执行时发生内部错误").Wrap(s)
			} else {
				err = mcp.Errorf(mcp.CodeInternal, "作业执行时发生内部错误").WithDetail("panic", fmt.Sprint(recovered))
			}
			if progress != nil {
				progress(0, "内部错误")
			}
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return run(ctx, progress)
}

func terminalProgress(state mcp.JobState, last int) int {
	switch state {
	case mcp.JobSucceeded:
		return 100
	case mcp.JobCancelled:
		return last
	default:
		return last
	}
}

func terminalStage(state mcp.JobState) string {
	switch state {
	case mcp.JobSucceeded:
		return "已完成"
	case mcp.JobFailed:
		return "失败"
	case mcp.JobCancelled:
		return "已取消"
	default:
		return ""
	}
}

// Get implements mcp.JobRunner.
func (s *Jobs) Get(ctx context.Context, p mcp.Principal, id string) (mcp.Job, error) {
	row := s.db().QueryRowContext(ctx, `SELECT `+jobColumns+` FROM mcp_job WHERE id = ?`, id)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return mcp.Job{}, mcp.Errorf(mcp.CodeNotFound, "作业 %s 不存在", id)
	}
	if err != nil {
		return mcp.Job{}, mcp.Errorf(mcp.CodeInternal, "读取作业失败").Wrap(err)
	}
	if p.ClientID != "" && job.ClientID != p.ClientID {
		return mcp.Job{}, mcp.Errorf(mcp.CodeNotFound, "作业 %s 不存在", id)
	}
	return job, nil
}

// List implements mcp.JobRunner.
func (s *Jobs) List(ctx context.Context, p mcp.Principal, f mcp.JobFilter) ([]mcp.Job, error) {
	db := s.db()
	if db == nil {
		return nil, mcp.Errorf(mcp.CodeInternal, "作业运行器不可用")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = listDefaultLimit
	}
	if limit > listMaxLimit {
		limit = listMaxLimit
	}
	query := `SELECT ` + jobColumns + ` FROM mcp_job WHERE 1 = 1`
	var args []any
	if f.ProjectID != "" {
		query += ` AND project_id = ?`
		args = append(args, f.ProjectID)
	}
	if f.State != "" {
		query += ` AND state = ?`
		args = append(args, string(f.State))
	}
	if p.ClientID != "" {
		query += ` AND client_id = ?`
		args = append(args, p.ClientID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, mcp.Errorf(mcp.CodeInternal, "读取作业失败").Wrap(err)
	}
	defer rows.Close()
	var out []mcp.Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, mcp.Errorf(mcp.CodeInternal, "读取作业失败").Wrap(err)
		}
		out = append(out, job)
	}
	if err := rows.Err(); err != nil {
		return nil, mcp.Errorf(mcp.CodeInternal, "读取作业失败").Wrap(err)
	}
	if out == nil {
		out = []mcp.Job{}
	}
	return out, nil
}

// Cancel implements mcp.JobRunner. Cancellation is best effort: a job that
// ignores its context settles on its own. A terminal job is a CONFLICT.
func (s *Jobs) Cancel(ctx context.Context, p mcp.Principal, id string) error {
	job, err := s.Get(ctx, p, id)
	if err != nil {
		return err
	}
	switch job.State {
	case mcp.JobQueued, mcp.JobRunning:
	default:
		return mcp.Errorf(mcp.CodeConflict, "作业 %s 已结束（%s），不能取消", id, job.State)
	}
	s.mu.Lock()
	cancel := s.cancels[id]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func scanJob(sc scanner) (mcp.Job, error) {
	var (
		job                                       mcp.Job
		projectID, params, result, errJSON, trace sql.NullString
		startedAt, finishedAt                     sql.NullString
		state                                     string
		readOnly                                  int
		createdAt                                 sql.NullString
	)
	if err := sc.Scan(&job.ID, &job.Tool, &projectID, &job.ClientID, &state, &job.Progress,
		&job.Stage, &readOnly, &params, &result, &errJSON, &createdAt, &startedAt, &finishedAt,
		&trace); err != nil {
		return mcp.Job{}, err
	}
	job.State = mcp.JobState(state)
	job.ReadOnly = readOnly == 1
	job.ProjectID = projectID.String
	job.Params = rawOrNil(params.String)
	job.Result = rawOrNil(result.String)
	job.Error = rawOrNil(errJSON.String)
	job.TraceID = trace.String
	job.CreatedAt, _ = parseTime(createdAt)
	job.StartedAt = parseTimePtr(startedAt)
	job.FinishedAt = parseTimePtr(finishedAt)
	return job, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// Jobs is the job runner over the store.
type Jobs struct{ *Store }

// NewJobs returns the job runner.
func NewJobs(store *Store) *Jobs { return &Jobs{Store: store} }

var _ mcp.JobRunner = (*Jobs)(nil)

// RecoverAtStartup marks unfinished jobs failed(interrupted).
func (s *Jobs) RecoverAtStartup(ctx context.Context) error {
	_, err := s.Store.Recover(ctx)
	return err
}
