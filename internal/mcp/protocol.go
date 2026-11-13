package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"researchos/internal/foundation/clock"
)

// Supported protocol versions (SPEC-11 §1.2).
const (
	ProtocolVersion20260728 = "2026-07-28"
	ProtocolVersion20251125 = "2025-11-25"
)

// ServerOptions configures the protocol layer.
type ServerOptions struct {
	Host          *Host
	Notifier      *Notifier
	Clock         clock.Clock
	ServerName    string
	ServerVersion string
	Logger        Logger
}

// Server implements the MCP JSON-RPC protocol over any message stream.
type Server struct {
	host    *Host
	notify  *Notifier
	clock   clock.Clock
	name    string
	version string
	logger  Logger
}

// NewServer builds the protocol server.
func NewServer(opts ServerOptions) *Server {
	if opts.Clock == nil {
		opts.Clock = clock.System{}
	}
	if opts.ServerName == "" {
		opts.ServerName = "ResearchOS"
	}
	return &Server{
		host:    opts.Host,
		notify:  opts.Notifier,
		clock:   opts.Clock,
		name:    opts.ServerName,
		version: opts.ServerVersion,
		logger:  opts.Logger,
	}
}

// Host exposes the tool host.
func (s *Server) Host() *Host { return s.host }

// Versions returns the supported protocol versions.
func (s *Server) Versions() []string {
	return []string{ProtocolVersion20260728, ProtocolVersion20251125}
}

// Session is one client stream. A session remembers the negotiated protocol
// version; HTTP requests may instead carry a version hint per request.
type Session struct {
	server    *Server
	principal Principal
	version   string
	mu        sync.Mutex
}

// NewSession opens a stream session for p.
func (s *Server) NewSession(p Principal) *Session {
	return &Session{server: s, principal: p}
}

// SetVersionHint seeds the protocol version from a transport header.
func (sess *Session) SetVersionHint(v string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.version == "" && supportedVersion(v) {
		sess.version = v
	}
}

func (sess *Session) currentVersion() string {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.version
}

func (sess *Session) setVersion(v string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.version = v
}

func supportedVersion(v string) bool {
	return v == ProtocolVersion20260728 || v == ProtocolVersion20251125
}

// Handle processes one JSON-RPC message. It returns the response bytes and
// whether a response is due; notifications yield no response.
func (sess *Session) Handle(ctx context.Context, body []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return ParseErrorResponse(), true
	}
	var req request
	if err := json.Unmarshal(trimmed, &req); err != nil {
		return ParseErrorResponse(), true
	}
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return InvalidRequestResponse(req.ID), true
	}
	if req.Method == "" {
		return InvalidRequestResponse(req.ID), true
	}
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"

	resp, hasResp := sess.handleMethod(ctx, req, isNotification)
	if isNotification {
		return nil, false
	}
	if !hasResp {
		resp = encode(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	}
	return resp, true
}

func (sess *Session) handleMethod(ctx context.Context, req request, isNotification bool) ([]byte, bool) {
	srv := sess.server
	ok := func(result any) ([]byte, bool) {
		return encode(response{JSONRPC: "2.0", ID: req.ID, Result: result}), true
	}
	fail := func(code int, message string, data any) ([]byte, bool) {
		return encode(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: code, Message: message, Data: data}}), true
	}

	switch req.Method {
	case "server/discover":
		return ok(srv.discoverResult())
	case "initialize":
		params, err := decodeParams(req.Params)
		if err != nil {
			return fail(rpcInvalidParams, "initialize 参数无效", nil)
		}
		requested := stringField(params, "protocolVersion")
		if requested == "" {
			requested = ProtocolVersion20251125
		}
		if !supportedVersion(requested) {
			return fail(rpcUnsupportedProtocolVersion, "UnsupportedProtocolVersion", map[string]any{
				"supported": srv.Versions(),
			})
		}
		sess.setVersion(requested)
		return ok(map[string]any{
			"protocolVersion": requested,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
			"serverInfo":      map[string]any{"name": srv.name, "version": srv.version},
			"instructions":    "ResearchOS 科研数据服务器：可读写项目、文献、笔记、Idea、实验、任务与周报；破坏性操作需要应用内人工确认。",
		})
	case "ping":
		return ok(map[string]any{})
	case "tools/list":
		if verr := sess.requireVersion(req); verr != nil {
			return fail(verr.Code, verr.Message, verr.Data)
		}
		tools, err := srv.host.Tools(ctx, sess.principal)
		if err != nil {
			e := AsError(err)
			return fail(rpcInternalError, e.Message, map[string]any{"code": e.Code})
		}
		if tools == nil {
			tools = []ToolSpec{}
		}
		return ok(map[string]any{
			"tools":      tools,
			"ttlMs":      60000,
			"cacheScope": "private",
		})
	case "tools/call":
		if verr := sess.requireVersion(req); verr != nil {
			return fail(verr.Code, verr.Message, verr.Data)
		}
		params, err := decodeParams(req.Params)
		if err != nil {
			return fail(rpcInvalidParams, "tools/call 参数无效", nil)
		}
		name := stringField(params, "name")
		if name == "" {
			return fail(rpcInvalidParams, "缺少工具名称", nil)
		}
		args, _ := params["arguments"].(map[string]any)
		if args == nil {
			args = map[string]any{}
		}
		raw, err := Reencode(args)
		if err != nil {
			return fail(rpcInvalidParams, "arguments 必须是 JSON 对象", nil)
		}
		callReq := CallRequest{Name: name, Arguments: raw}
		if meta := metaField(params); meta != nil {
			if key, ok := meta["idempotencyKey"].(string); ok {
				callReq.IdempotencyKey = key
			}
			if token, ok := meta["progressToken"]; ok {
				callReq.ProgressToken = token
			}
			if trace, ok := meta["traceId"].(string); ok {
				callReq.TraceID = trace
			}
		}
		result, err := srv.host.Call(ctx, sess.principal, callReq)
		if err != nil {
			var e *Error
			if errors.As(err, &e) && e.Details["reason"] == "unknown_tool" {
				return encode(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{
					Code:    rpcInvalidParams,
					Message: e.Message,
					Data:    map[string]any{"code": e.Code, "details": e.Details},
				}}), true
			}
			e = AsError(err)
			return fail(rpcInternalError, e.Message, map[string]any{"code": e.Code})
		}
		return ok(result)
	case "notifications/initialized", "notifications/cancelled", "notifications/progress":
		return nil, false
	default:
		if isNotification {
			return nil, false
		}
		return fail(rpcMethodNotFound, "未知方法 "+req.Method, nil)
	}
}

func (sess *Session) requireVersion(req request) *rpcError {
	if sess.currentVersion() != "" {
		return nil
	}
	if params, err := decodeParams(req.Params); err == nil {
		if meta := metaField(params); meta != nil {
			if v, ok := meta["protocolVersion"].(string); ok && supportedVersion(v) {
				sess.setVersion(v)
				return nil
			}
			if v, ok := meta["io.modelcontextprotocol/protocolVersion"].(string); ok && supportedVersion(v) {
				sess.setVersion(v)
				return nil
			}
		}
	}
	return &rpcError{
		Code:    rpcUnsupportedProtocolVersion,
		Message: "UnsupportedProtocolVersion",
		Data: map[string]any{
			"supported": sess.server.Versions(),
			"hint":      "请先调用 initialize，或在 _meta.protocolVersion 中声明协议版本",
		},
	}
}

func (s *Server) discoverResult() map[string]any {
	return map[string]any{
		"protocolVersions": s.Versions(),
		"capabilities":     map[string]any{"tools": map[string]any{"listChanged": true}},
		"serverInfo":       map[string]any{"name": s.name, "version": s.version},
		"instructions":     "ResearchOS 科研数据服务器：可读写项目、文献、笔记、Idea、实验、任务与周报；破坏性操作需要应用内人工确认。",
	}
}

// ServeStream serves newline-delimited JSON-RPC until the reader ends. It also
// forwards tools/list_changed notifications when the host publishes them.
func (s *Server) ServeStream(ctx context.Context, p Principal, connID string, r io.Reader, w io.Writer) error {
	p.ConnectionID = connID
	sess := s.NewSession(p)
	var writeMu sync.Mutex
	write := func(body []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if _, err := w.Write(append(body, '\n')); err != nil {
			return err
		}
		if f, ok := w.(interface{ Sync() error }); ok {
			_ = f.Sync()
		}
		return nil
	}

	done := make(chan struct{})
	defer close(done)
	if s.notify != nil {
		events, cancel := s.notify.Subscribe()
		defer cancel()
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-done:
					return
				case _, open := <-events:
					if !open {
						return
					}
					body := encode(notification{JSONRPC: "2.0", Method: "notifications/tools/list_changed"})
					if err := write(body); err != nil {
						return
					}
				}
			}
		}()
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), MaxBodyBytes+MaxBodyToolOverhead)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		resp, has := sess.Handle(ctx, line)
		if has && len(resp) > 0 {
			if err := write(resp); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("mcp: read stream: %w", err)
	}
	return nil
}
