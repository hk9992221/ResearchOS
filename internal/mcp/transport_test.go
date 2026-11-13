package mcp

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeAuth struct{ token string }

func (a fakeAuth) Authenticate(_ context.Context, token string) (Principal, error) {
	if token != a.token {
		return Principal{}, Errorf(CodeCapabilityInvalid, "bad token")
	}
	return Principal{ClientID: "http-client", ClientName: "http", Transport: TransportHTTP}, nil
}

func newHTTP(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	srv := newTestServer(t)
	api := httptest.NewServer(NewHTTPHandler(srv, fakeAuth{token: "secret"}, nil))
	t.Cleanup(api.Close)
	return api, srv
}

func post(t *testing.T, api *httptest.Server, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, api.URL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestHTTPAuthOriginAndHeaders(t *testing.T) {
	api, _ := newHTTP(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"server/discover"}`
	base := map[string]string{"Mcp-Method": "server/discover"}

	resp := post(t, api, body, boundaryHeaders(base, nil))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = post(t, api, body, boundaryHeaders(base, map[string]string{"Authorization": "Bearer wrong"}))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	auth := map[string]string{"Authorization": "Bearer secret"}
	resp = post(t, api, body, boundaryHeaders(base, mergeHeaders(auth, map[string]string{"Origin": "https://evil.example"})))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bad origin status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = post(t, api, body, boundaryHeaders(nil, auth))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing Mcp-Method status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	wrong := map[string]string{"Mcp-Method": "tools/list"}
	resp = post(t, api, body, boundaryHeaders(wrong, auth))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("tampered Mcp-Method status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = post(t, api, body, boundaryHeaders(base, mergeHeaders(auth, map[string]string{"Origin": "http://127.0.0.1:1234"})))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid request status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// GET must be refused: there is no subscription stream.
	getReq, _ := http.NewRequest(http.MethodGet, api.URL, nil)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	if getResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d", getResp.StatusCode)
	}
	getResp.Body.Close()

	// Broken JSON yields a JSON-RPC error and the transport stays up.
	resp = post(t, api, `{oops`, boundaryHeaders(base, auth))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad JSON status = %d", resp.StatusCode)
	}
	var rpcErr map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&rpcErr)
	resp.Body.Close()
	if rpcErr["error"] == nil {
		t.Fatalf("bad JSON body = %v", rpcErr)
	}
}

func TestHTTPToolsCallHeaderName(t *testing.T) {
	api, _ := newHTTP(t)
	auth := map[string]string{"Authorization": "Bearer secret", "Mcp-Method": "tools/call"}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"note_tool","arguments":{"value":"x"}}}`
	resp := post(t, api, body, boundaryHeaders(auth, nil))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing Mcp-Name status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	withName := mergeHeaders(auth, map[string]string{"Mcp-Name": "note_tool", "MCP-Protocol-Version": ProtocolVersion20260728})
	resp = post(t, api, body, boundaryHeaders(withName, nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid tools/call status = %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestListenRejectsNonLoopback(t *testing.T) {
	if _, err := Listen("0.0.0.0:0"); err == nil {
		t.Fatal("non-loopback address was accepted")
	}
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("loopback listen: %v", err)
	}
	ln.Close()
}

func TestIPCBridgeRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "mcp.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("unix sockets unsupported: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := newTestServer(t)
	go func() { _ = ServeIPC(ctx, ln, IPCServerOptions{Server: srv, Key: key}) }()
	defer ln.Close()

	conn, err := DialIPC(ctx, socket, key)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	if _, err := io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"method":"server/discover"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(line, &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, line)
	}
	if out["result"] == nil {
		t.Fatalf("discover response = %s", line)
	}
}

func TestIPCWrongKeyRejected(t *testing.T) {
	key := make([]byte, 32)
	other := make([]byte, 32)
	other[0] = 1
	socket := filepath.Join(t.TempDir(), "mcp.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("unix sockets unsupported: %v", err)
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = ServeIPC(ctx, ln, IPCServerOptions{Server: newTestServer(t), Key: key}) }()

	if _, err := DialIPC(ctx, socket, other); err == nil {
		t.Fatal("bridge authenticated with a wrong installation key")
	}
	if _, err := DialIPC(ctx, filepath.Join(t.TempDir(), "missing.sock"), key); err == nil {
		t.Fatal("dial to a missing socket succeeded")
	}
}

func boundaryHeaders(base, extra map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range extra {
		out[k] = v
	}
	for k, v := range base {
		out[k] = v
	}
	return out
}

func mergeHeaders(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
