package mcpstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"researchos/internal/foundation/ids"
	"researchos/internal/mcp"
)

const (
	confirmationColumns = `id, tool, args_json, args_hash, title, summary, impact_json,
		project_id, client_id, state, created_at, expires_at, resolved_at, resolved_by,
		result_json, error_code, trace_id`
	listDefaultLimit = 50
	listMaxLimit     = 200
)

// Enqueue implements mcp.ConfirmationBroker. The confirmation is bound to
// (tool, args hash, project, client); a duplicate request is a separate row and
// the idempotency layer is what prevents the duplicate enqueue.
func (s *Confirmations) Enqueue(ctx context.Context, p mcp.Principal, req mcp.ConfirmationRequest, args json.RawMessage, argsHash string) (mcp.Confirmation, error) {
	db := s.db()
	if db == nil {
		return mcp.Confirmation{}, mcp.Errorf(mcp.CodeInternal, "确认队列不可用")
	}
	if p.ClientID == "" {
		return mcp.Confirmation{}, mcp.Errorf(mcp.CodeInvalidArgument, "缺少客户端标识")
	}
	id, err := ids.New(s.clock)
	if err != nil {
		return mcp.Confirmation{}, mcp.Errorf(mcp.CodeInternal, "生成确认编号失败")
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = s.ttl(ctx)
	}
	now := s.now()
	expires := now.Add(ttl)
	title := req.Title
	if title == "" {
		title = req.Tool
	}
	var impact any
	var impactJSON json.RawMessage
	if req.Impact != nil {
		impactJSON, _ = json.Marshal(req.Impact)
		if len(impactJSON) > 0 {
			impact = string(impactJSON)
		}
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO mcp_confirmation (id, tool, args_json, args_hash, title, summary, impact_json,
			project_id, client_id, state, created_at, expires_at, trace_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'requested', ?, ?, ?)`,
		id, req.Tool, string(args), argsHash, title, req.Summary, impact,
		nullText(req.ProjectID), p.ClientID, timeText(now), timeText(expires), nullText(req.TraceID))
	if err != nil {
		return mcp.Confirmation{}, mcp.Errorf(mcp.CodeInternal, "写入确认队列失败").Wrap(err)
	}
	return mcp.Confirmation{
		ID:        id,
		Tool:      req.Tool,
		Args:      args,
		ArgsHash:  argsHash,
		Title:     title,
		Summary:   req.Summary,
		Impact:    impactJSON,
		ProjectID: req.ProjectID,
		ClientID:  p.ClientID,
		State:     mcp.ConfirmationRequested,
		CreatedAt: now,
		ExpiresAt: expires,
	}, nil
}

// Get implements mcp.ConfirmationBroker.
func (s *Confirmations) Get(ctx context.Context, p mcp.Principal, id string) (mcp.Confirmation, error) {
	conf, err := s.getRow(ctx, id)
	if err != nil {
		return mcp.Confirmation{}, err
	}
	if p.ClientID != "" && conf.ClientID != p.ClientID {
		return mcp.Confirmation{}, mcp.Errorf(mcp.CodeNotFound, "确认记录 %s 不存在", id)
	}
	if conf.State == mcp.ConfirmationRequested && !s.now().Before(conf.ExpiresAt) {
		if err := s.expire(ctx, id); err != nil {
			return mcp.Confirmation{}, err
		}
		conf.State = mcp.ConfirmationExpired
		resolved := s.now()
		conf.ResolvedAt = &resolved
		conf.ResolvedBy = "system"
	}
	return conf, nil
}

// List implements mcp.ConfirmationBroker. A principal with an empty ClientID
// (the in-app confirmation center) sees every client; an MCP client only sees
// its own requests.
func (s *Confirmations) List(ctx context.Context, p mcp.Principal, f mcp.ConfirmationFilter) ([]mcp.Confirmation, error) {
	db := s.db()
	if db == nil {
		return nil, mcp.Errorf(mcp.CodeInternal, "确认队列不可用")
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE mcp_confirmation SET state = 'expired', resolved_at = ?, resolved_by = 'system'
		WHERE state = 'requested' AND expires_at <= ?`, s.nowText(), s.nowText()); err != nil {
		return nil, mcp.Errorf(mcp.CodeInternal, "更新确认状态失败").Wrap(err)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = listDefaultLimit
	}
	if limit > listMaxLimit {
		limit = listMaxLimit
	}
	query := `SELECT ` + confirmationColumns + ` FROM mcp_confirmation WHERE 1 = 1`
	var args []any
	if f.State != "" {
		query += ` AND state = ?`
		args = append(args, string(f.State))
	}
	if f.ProjectID != "" {
		query += ` AND project_id = ?`
		args = append(args, f.ProjectID)
	}
	if p.ClientID != "" {
		query += ` AND client_id = ?`
		args = append(args, p.ClientID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, mcp.Errorf(mcp.CodeInternal, "读取确认队列失败").Wrap(err)
	}
	defer rows.Close()
	var out []mcp.Confirmation
	for rows.Next() {
		conf, err := scanConfirmation(rows)
		if err != nil {
			return nil, mcp.Errorf(mcp.CodeInternal, "读取确认队列失败").Wrap(err)
		}
		out = append(out, conf)
	}
	if err := rows.Err(); err != nil {
		return nil, mcp.Errorf(mcp.CodeInternal, "读取确认队列失败").Wrap(err)
	}
	if out == nil {
		out = []mcp.Confirmation{}
	}
	return out, nil
}

// CancelByClient implements mcp.ConfirmationBroker. Only the owning client may
// cancel, and only while the request is still requested.
func (s *Confirmations) CancelByClient(ctx context.Context, p mcp.Principal, id string) error {
	db := s.db()
	if db == nil {
		return mcp.Errorf(mcp.CodeInternal, "确认队列不可用")
	}
	if p.ClientID == "" {
		return mcp.Errorf(mcp.CodeInvalidArgument, "缺少客户端标识")
	}
	res, err := db.ExecContext(ctx, `
		UPDATE mcp_confirmation SET state = 'cancelled', resolved_at = ?, resolved_by = 'client'
		WHERE id = ? AND client_id = ? AND state = 'requested'`,
		s.nowText(), id, p.ClientID)
	if err != nil {
		return mcp.Errorf(mcp.CodeInternal, "取消确认失败").Wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	conf, err := s.getRow(ctx, id)
	if err != nil {
		return err
	}
	if conf.ClientID != p.ClientID {
		return mcp.Errorf(mcp.CodeNotFound, "确认记录 %s 不存在", id)
	}
	return mcp.Errorf(mcp.CodeConflict, "确认 %s 当前状态为 %s，不能取消", id, conf.State)
}

// Resolve implements mcp.ConfirmationAdmin: requested→approved or denied as a
// single CAS, so a double approval cannot execute twice.
func (s *Confirmations) Resolve(ctx context.Context, id string, approve bool, by string) (mcp.Confirmation, error) {
	db := s.db()
	if db == nil {
		return mcp.Confirmation{}, mcp.Errorf(mcp.CodeInternal, "确认队列不可用")
	}
	if by == "" {
		by = "user"
	}
	target := mcp.ConfirmationDenied
	if approve {
		target = mcp.ConfirmationApproved
	}
	now := s.now()
	res, err := db.ExecContext(ctx, `
		UPDATE mcp_confirmation SET state = ?, resolved_at = ?, resolved_by = ?
		WHERE id = ? AND state = 'requested' AND expires_at > ?`,
		target, timeText(now), by, id, timeText(now))
	if err != nil {
		return mcp.Confirmation{}, mcp.Errorf(mcp.CodeInternal, "更新确认失败").Wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		conf, err := s.getRow(ctx, id)
		if err != nil {
			return mcp.Confirmation{}, err
		}
		if conf.State == mcp.ConfirmationRequested {
			_ = s.expire(ctx, id)
			return mcp.Confirmation{}, mcp.Errorf(mcp.CodeConflict, "确认 %s 已过期", id)
		}
		return mcp.Confirmation{}, mcp.Errorf(mcp.CodeConflict, "确认 %s 当前状态为 %s，不能再次处理", id, conf.State)
	}
	return s.requireRow(ctx, id)
}

// ClaimExecution performs approved→executing so a confirmation executes once.
func (s *Confirmations) ClaimExecution(ctx context.Context, id string) (mcp.Confirmation, error) {
	res, err := s.db().ExecContext(ctx, `
		UPDATE mcp_confirmation SET state = 'executing', resolved_at = ?, resolved_by = 'user'
		WHERE id = ? AND state = 'approved'`, s.nowText(), id)
	if err != nil {
		return mcp.Confirmation{}, mcp.Errorf(mcp.CodeInternal, "抢占确认执行失败").Wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		conf, err := s.getRow(ctx, id)
		if err != nil {
			return mcp.Confirmation{}, err
		}
		return mcp.Confirmation{}, mcp.Errorf(mcp.CodeConflict, "确认 %s 当前状态为 %s，不能再次执行", id, conf.State)
	}
	return s.requireRow(ctx, id)
}

// Finish records the outcome of an approved confirmation.
func (s *Confirmations) Finish(ctx context.Context, id string, result any, errCode string) (mcp.Confirmation, error) {
	db := s.db()
	if db == nil {
		return mcp.Confirmation{}, mcp.Errorf(mcp.CodeInternal, "确认队列不可用")
	}
	target := mcp.ConfirmationExecuted
	if errCode != "" {
		target = mcp.ConfirmationFailed
	}
	var body any
	if result != nil {
		if raw, err := json.Marshal(result); err == nil {
			body = string(raw)
		}
	}
	res, err := db.ExecContext(ctx, `
		UPDATE mcp_confirmation SET state = ?, resolved_at = ?, resolved_by = 'user',
			result_json = ?, error_code = ?
		WHERE id = ? AND state IN ('approved','executing')`,
		target, s.nowText(), body, nullText(errCode), id)
	if err != nil {
		return mcp.Confirmation{}, mcp.Errorf(mcp.CodeInternal, "记录确认结果失败").Wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		conf, err := s.getRow(ctx, id)
		if err != nil {
			return mcp.Confirmation{}, err
		}
		return mcp.Confirmation{}, mcp.Errorf(mcp.CodeConflict, "确认 %s 当前状态为 %s，不能记录结果", id, conf.State)
	}
	return s.requireRow(ctx, id)
}

// ExpireNow expires a requested confirmation immediately (used by tests and the
// retention sweep).
func (s *Confirmations) ExpireNow(ctx context.Context, id string) error { return s.expire(ctx, id) }

func (s *Confirmations) expire(ctx context.Context, id string) error {
	_, err := s.db().ExecContext(ctx, `
		UPDATE mcp_confirmation SET state = 'expired', resolved_at = ?, resolved_by = 'system'
		WHERE id = ? AND state = 'requested'`, s.nowText(), id)
	if err != nil {
		return mcp.Errorf(mcp.CodeInternal, "更新确认状态失败").Wrap(err)
	}
	return nil
}

func (s *Confirmations) getRow(ctx context.Context, id string) (mcp.Confirmation, error) {
	row := s.db().QueryRowContext(ctx, `SELECT `+confirmationColumns+` FROM mcp_confirmation WHERE id = ?`, id)
	conf, err := scanConfirmation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return mcp.Confirmation{}, mcp.Errorf(mcp.CodeNotFound, "确认记录 %s 不存在", id)
	}
	if err != nil {
		return mcp.Confirmation{}, mcp.Errorf(mcp.CodeInternal, "读取确认记录失败").Wrap(err)
	}
	return conf, nil
}

func (s *Confirmations) requireRow(ctx context.Context, id string) (mcp.Confirmation, error) {
	conf, err := s.getRow(ctx, id)
	if err != nil {
		return mcp.Confirmation{}, err
	}
	return conf, nil
}

func scanConfirmation(sc scanner) (mcp.Confirmation, error) {
	var (
		conf                             mcp.Confirmation
		args, impact, result             sql.NullString
		projectID, resolvedBy, errorCode sql.NullString
		state                            string
		createdAt, expiresAt, resolvedAt sql.NullString
		traceID                          sql.NullString
	)
	if err := sc.Scan(&conf.ID, &conf.Tool, &args, &conf.ArgsHash, &conf.Title, &conf.Summary,
		&impact, &projectID, &conf.ClientID, &state, &createdAt, &expiresAt, &resolvedAt,
		&resolvedBy, &result, &errorCode, &traceID); err != nil {
		return mcp.Confirmation{}, err
	}
	conf.State = mcp.ConfirmationState(state)
	conf.Args = rawOrNil(args.String)
	conf.Impact = rawOrNil(impact.String)
	conf.Result = rawOrNil(result.String)
	conf.ProjectID = projectID.String
	conf.ResolvedBy = resolvedBy.String
	conf.ErrorCode = errorCode.String
	conf.TraceID = traceID.String
	conf.CreatedAt, _ = parseTime(createdAt)
	conf.ExpiresAt, _ = parseTime(expiresAt)
	conf.ResolvedAt = parseTimePtr(resolvedAt)
	return conf, nil
}

func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func rawOrNil(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

// Confirmations is the MCP-facing and in-app confirmation queue over the store.
type Confirmations struct{ *Store }

// NewConfirmations returns the confirmation queue.
func NewConfirmations(store *Store) *Confirmations { return &Confirmations{Store: store} }

var _ mcp.ConfirmationAdmin = (*Confirmations)(nil)

// RecoverAtStartup delegates to the shared runtime recovery.
func (s *Confirmations) RecoverAtStartup(ctx context.Context) (mcp.RecoverReport, error) {
	return s.Store.Recover(ctx)
}
