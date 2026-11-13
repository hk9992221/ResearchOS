package mcpstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"researchos/internal/foundation/ids"
	"researchos/internal/mcp"
)

// ClientService manages mcp_client rows and bearer-token authentication
// (SPEC-11 §5.8). Only the SHA-256 hash is stored; the plaintext token is
// returned exactly once.
type ClientService struct {
	store *Store
}

// NewClientService returns the client registry bound to store.
func NewClientService(store *Store) *ClientService { return &ClientService{store: store} }

// Create registers a client and returns the one-time plaintext token.
func (s *ClientService) Create(ctx context.Context, name, transport string) (mcp.Client, string, error) {
	if s == nil || s.store == nil || s.store.db() == nil {
		return mcp.Client{}, "", mcp.Errorf(mcp.CodeInternal, "客户端注册表不可用")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return mcp.Client{}, "", mcp.Errorf(mcp.CodeInvalidArgument, "客户端名称不能为空")
	}
	if transport == "" {
		transport = mcp.TransportHTTP
	}
	if transport != mcp.TransportHTTP && transport != mcp.TransportStdio {
		return mcp.Client{}, "", mcp.Errorf(mcp.CodeInvalidArgument, "传输方式必须是 http 或 stdio")
	}
	id, err := ids.New(s.store.clock)
	if err != nil {
		return mcp.Client{}, "", mcp.Errorf(mcp.CodeInternal, "生成客户端编号失败")
	}
	token, hash, err := newToken()
	if err != nil {
		return mcp.Client{}, "", mcp.Errorf(mcp.CodeInternal, "生成访问令牌失败")
	}
	now := s.store.now()
	_, err = s.store.db().ExecContext(ctx, `
		INSERT INTO mcp_client (id, name, token_hash, transport, created_at, enabled)
		VALUES (?, ?, ?, ?, ?, 1)`, id, name, hash, transport, timeText(now))
	if err != nil {
		return mcp.Client{}, "", mcp.Errorf(mcp.CodeInternal, "创建客户端失败").Wrap(err)
	}
	return mcp.Client{ID: id, Name: name, Transport: transport, CreatedAt: now, Enabled: true}, token, nil
}

// Rotate replaces the token hash and returns the new plaintext token.
func (s *ClientService) Rotate(ctx context.Context, id string) (string, error) {
	if s == nil || s.store == nil || s.store.db() == nil {
		return "", mcp.Errorf(mcp.CodeInternal, "客户端注册表不可用")
	}
	token, hash, err := newToken()
	if err != nil {
		return "", mcp.Errorf(mcp.CodeInternal, "生成访问令牌失败")
	}
	res, err := s.store.db().ExecContext(ctx, `UPDATE mcp_client SET token_hash = ? WHERE id = ?`, hash, id)
	if err != nil {
		return "", mcp.Errorf(mcp.CodeInternal, "轮换令牌失败").Wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", mcp.Errorf(mcp.CodeNotFound, "客户端 %s 不存在", id)
	}
	return token, nil
}

// Revoke deletes the client; its token stops working immediately.
func (s *ClientService) Revoke(ctx context.Context, id string) error {
	if s == nil || s.store == nil || s.store.db() == nil {
		return mcp.Errorf(mcp.CodeInternal, "客户端注册表不可用")
	}
	res, err := s.store.db().ExecContext(ctx, `DELETE FROM mcp_client WHERE id = ?`, id)
	if err != nil {
		return mcp.Errorf(mcp.CodeInternal, "吊销客户端失败").Wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return mcp.Errorf(mcp.CodeNotFound, "客户端 %s 不存在", id)
	}
	return nil
}

// SetEnabled toggles a client without revoking its token.
func (s *ClientService) SetEnabled(ctx context.Context, id string, enabled bool) error {
	if s == nil || s.store == nil || s.store.db() == nil {
		return mcp.Errorf(mcp.CodeInternal, "客户端注册表不可用")
	}
	res, err := s.store.db().ExecContext(ctx, `UPDATE mcp_client SET enabled = ? WHERE id = ?`, boolInt(enabled), id)
	if err != nil {
		return mcp.Errorf(mcp.CodeInternal, "更新客户端失败").Wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return mcp.Errorf(mcp.CodeNotFound, "客户端 %s 不存在", id)
	}
	return nil
}

// List returns every client, newest first.
func (s *ClientService) List(ctx context.Context) ([]mcp.Client, error) {
	if s == nil || s.store == nil || s.store.db() == nil {
		return nil, mcp.Errorf(mcp.CodeInternal, "客户端注册表不可用")
	}
	rows, err := s.store.db().QueryContext(ctx, `
		SELECT id, name, transport, created_at, last_seen_at, enabled
		FROM mcp_client ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, mcp.Errorf(mcp.CodeInternal, "读取客户端失败").Wrap(err)
	}
	defer rows.Close()
	var out []mcp.Client
	for rows.Next() {
		var (
			c          mcp.Client
			createdAt  sql.NullString
			lastSeen   sql.NullString
			enabledInt int
		)
		if err := rows.Scan(&c.ID, &c.Name, &c.Transport, &createdAt, &lastSeen, &enabledInt); err != nil {
			return nil, mcp.Errorf(mcp.CodeInternal, "读取客户端失败").Wrap(err)
		}
		c.CreatedAt, _ = parseTime(createdAt)
		c.LastSeenAt = parseTimePtr(lastSeen)
		c.Enabled = enabledInt == 1
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, mcp.Errorf(mcp.CodeInternal, "读取客户端失败").Wrap(err)
	}
	if out == nil {
		out = []mcp.Client{}
	}
	return out, nil
}

// Authenticate resolves a bearer token. The token is hashed before lookup and
// never logged; a disabled or unknown client is indistinguishable to callers.
func (s *ClientService) Authenticate(ctx context.Context, token string) (mcp.Principal, error) {
	if s == nil || s.store == nil || s.store.db() == nil {
		return mcp.Principal{}, fmt.Errorf("mcpstore: client registry unavailable")
	}
	hash := hashToken(token)
	var (
		client    mcp.Client
		createdAt sql.NullString
		lastSeen  sql.NullString
		enabled   int
	)
	err := s.store.db().QueryRowContext(ctx, `
		SELECT id, name, transport, created_at, last_seen_at, enabled
		FROM mcp_client WHERE token_hash = ?`, hash).
		Scan(&client.ID, &client.Name, &client.Transport, &createdAt, &lastSeen, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return mcp.Principal{}, fmt.Errorf("mcpstore: unknown token")
	}
	if err != nil {
		return mcp.Principal{}, fmt.Errorf("mcpstore: authenticate: %w", err)
	}
	if enabled != 1 {
		return mcp.Principal{}, fmt.Errorf("mcpstore: client disabled")
	}
	now := s.store.now()
	if _, err := s.store.db().ExecContext(ctx, `UPDATE mcp_client SET last_seen_at = ? WHERE id = ?`, timeText(now), client.ID); err != nil && s.store.logger != nil {
		s.store.logger.Warn("mcp: last_seen update failed", "err", err)
	}
	return mcp.Principal{
		ClientID:   client.ID,
		ClientName: client.Name,
		Transport:  client.Transport,
	}, nil
}

func newToken() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return token, hashToken(token), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

var _ mcp.Authenticator = (*ClientService)(nil)
