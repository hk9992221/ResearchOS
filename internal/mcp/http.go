package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPHandler is the loopback Streamable HTTP transport of SPEC-11 §5.8.
type HTTPHandler struct {
	server        *Server
	authenticator Authenticator
	logger        Logger
}

// NewHTTPHandler returns the POST /mcp handler. authenticator resolves bearer
// tokens; nil makes every request unauthorized.
func NewHTTPHandler(server *Server, auth Authenticator, logger Logger) *HTTPHandler {
	return &HTTPHandler{server: server, authenticator: auth, logger: logger}
}

// Listen binds addr after validating that it is loopback. A non-loopback
// address is refused before the socket is opened (§8.1 item 4).
func Listen(addr string) (net.Listener, error) {
	if err := ValidateListenAddr(addr); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("mcp: listen %s: %w", addr, err)
	}
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok && !tcp.IP.IsLoopback() {
		_ = ln.Close()
		return nil, fmt.Errorf("mcp: refusing non-loopback listener %s", addr)
	}
	return ln, nil
}

// ServeHTTP implements http.Handler.
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "只支持 POST /mcp", http.StatusMethodNotAllowed)
		return
	}
	if !originAllowed(r.Header.Get("Origin")) {
		http.Error(w, "拒绝来自非回环来源的请求", http.StatusForbidden)
		return
	}
	principal, ok := h.authenticate(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="ResearchOS MCP"`)
		http.Error(w, "无效的访问令牌", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes+MaxBodyToolOverhead))
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.Write(ErrorResponse(nil, rpcInvalidRequest, "请求体超过上限"))
		return
	}
	if len(bytes.TrimSpace(body)) == 0 {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(ParseErrorResponse())
		return
	}
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(ParseErrorResponse())
		return
	}
	headerMethod := r.Header.Get("Mcp-Method")
	if headerMethod == "" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(InvalidRequestResponse(req.ID))
		return
	}
	if headerMethod != req.Method {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(InvalidRequestResponse(req.ID))
		return
	}
	if req.Method == "tools/call" {
		params, _ := decodeParams(req.Params)
		name := stringField(params, "name")
		headerName := r.Header.Get("Mcp-Name")
		if headerName == "" || name == "" || headerName != name {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(InvalidRequestResponse(req.ID))
			return
		}
	}

	if settings, err := h.server.host.Settings(r.Context()); err == nil {
		principal.AccessMode = settings.AccessMode
	}
	sess := h.server.NewSession(principal)
	sess.SetVersionHint(r.Header.Get("MCP-Protocol-Version"))

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	resp, has := sess.Handle(ctx, body)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if !has || len(resp) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resp)
}

func (h *HTTPHandler) authenticate(r *http.Request) (Principal, bool) {
	if h.authenticator == nil {
		return Principal{}, false
	}
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return Principal{}, false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return Principal{}, false
	}
	p, err := h.authenticator.Authenticate(r.Context(), token)
	if err != nil {
		if h.logger != nil {
			h.logger.Warn("mcp: rejected bearer token", "reason", "authentication failed")
		}
		return Principal{}, false
	}
	p.ConnectionID = p.ClientID
	if p.Transport == "" {
		p.Transport = TransportHTTP
	}
	return p, true
}

// originAllowed accepts an absent Origin (CLI clients) and loopback origins
// only. A hostname origin is rejected because it can resolve off-box.
func originAllowed(origin string) bool {
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	return IsLoopbackHost(host)
}

// ErrNotRunning is the sentinel the stdio bridge reports when no application
// instance is listening.
var ErrNotRunning = errors.New("mcp: ResearchOS is not running")
