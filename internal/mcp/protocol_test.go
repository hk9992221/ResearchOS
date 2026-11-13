package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	provider := newFakeProvider(ModuleNotes, "note", CategoryRead, true)
	host := newTestHost(t, []ToolProvider{provider}, nil)
	return NewServer(ServerOptions{Host: host, Clock: nil, ServerName: "test", ServerVersion: "1.0"})
}

func rpc(t *testing.T, sess *Session, msg string) map[string]any {
	t.Helper()
	resp, has := sess.Handle(context.Background(), []byte(msg))
	if !has {
		t.Fatal("expected a response")
	}
	var out map[string]any
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("decode response: %v (%s)", err, resp)
	}
	return out
}

func TestDiscoverAndVersionNegotiation(t *testing.T) {
	srv := newTestServer(t)
	sess := srv.NewSession(principal())

	discover := rpc(t, sess, `{"jsonrpc":"2.0","id":1,"method":"server/discover"}`)
	result := discover["result"].(map[string]any)
	versions := result["protocolVersions"].([]any)
	if len(versions) != 2 || versions[0] != ProtocolVersion20260728 || versions[1] != ProtocolVersion20251125 {
		t.Fatalf("versions = %v", versions)
	}
	caps := result["capabilities"].(map[string]any)["tools"].(map[string]any)
	if caps["listChanged"] != true {
		t.Fatal("tools.listChanged missing")
	}

	// Without initialize and without a _meta version, tools/list is refused.
	fresh := srv.NewSession(principal())
	refused := rpc(t, fresh, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	errObj := refused["error"].(map[string]any)
	if int(errObj["code"].(float64)) != rpcUnsupportedProtocolVersion {
		t.Fatalf("uninitialized tools/list = %v", refused)
	}

	// The 2025-11-25 handshake makes the same session usable.
	initResp := rpc(t, fresh, `{"jsonrpc":"2.0","id":3,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`)
	if initResp["result"].(map[string]any)["protocolVersion"] != ProtocolVersion20251125 {
		t.Fatalf("initialize = %v", initResp)
	}
	list := rpc(t, fresh, `{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)
	if len(list["result"].(map[string]any)["tools"].([]any)) != 1 {
		t.Fatalf("tools/list after initialize = %v", list)
	}

	// 2026-07-28 clients may state the version in _meta per request.
	modern := srv.NewSession(principal())
	list = rpc(t, modern, `{"jsonrpc":"2.0","id":5,"method":"tools/list","params":{"_meta":{"protocolVersion":"2026-07-28"}}}`)
	if list["error"] != nil {
		t.Fatalf("modern tools/list = %v", list)
	}
	unsupported := rpc(t, srv.NewSession(principal()), `{"jsonrpc":"2.0","id":6,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	if int(unsupported["error"].(map[string]any)["code"].(float64)) != rpcUnsupportedProtocolVersion {
		t.Fatalf("unsupported initialize = %v", unsupported)
	}
}

func TestToolsListShapeAndDeterminism(t *testing.T) {
	srv := newTestServer(t)
	sess := srv.NewSession(principal())
	sess.SetVersionHint(ProtocolVersion20260728)
	resp1, _ := sess.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	resp2, _ := sess.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if !bytes.Equal(resp1, resp2) {
		t.Fatal("tools/list bytes differ between calls")
	}
	var out struct {
		Result struct {
			Tools []struct {
				Name        string          `json:"name"`
				Input       json.RawMessage `json:"inputSchema"`
				Output      json.RawMessage `json:"outputSchema"`
				Annotations map[string]any  `json:"annotations"`
			} `json:"tools"`
			TTL        int    `json:"ttlMs"`
			CacheScope string `json:"cacheScope"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp1, &out); err != nil {
		t.Fatal(err)
	}
	if out.Result.TTL != 60000 || out.Result.CacheScope != "private" {
		t.Fatalf("ttl/cacheScope = %v/%v", out.Result.TTL, out.Result.CacheScope)
	}
	tool := out.Result.Tools[0]
	if len(tool.Input) == 0 || len(tool.Output) == 0 || len(tool.Annotations) != 4 {
		t.Fatalf("tool shape = %+v", tool)
	}
}

func TestUnknownToolAndToolError(t *testing.T) {
	srv := newTestServer(t)
	sess := srv.NewSession(principal())
	sess.SetVersionHint(ProtocolVersion20260728)

	unknown := rpc(t, sess, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"missing","arguments":{}}}`)
	if int(unknown["error"].(map[string]any)["code"].(float64)) != rpcInvalidParams {
		t.Fatalf("unknown tool = %v", unknown)
	}
	// Invalid arguments are a structured tool error, not a JSON-RPC error that
	// drops the connection.
	invalid := rpc(t, sess, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"note_tool","arguments":{"bad":true}}}`)
	result := invalid["result"].(map[string]any)
	if result["isError"] != true || result["resultType"] != "complete" {
		t.Fatalf("invalid args result = %v", result)
	}
	if len(result["content"].([]any)) == 0 {
		t.Fatal("tool error has no content summary")
	}
	// The session keeps working.
	again := rpc(t, sess, `{"jsonrpc":"2.0","id":3,"method":"ping"}`)
	if again["result"] == nil {
		t.Fatalf("session unusable after tool error: %v", again)
	}
}

func TestParseErrorDoesNotDropSession(t *testing.T) {
	srv := newTestServer(t)
	sess := srv.NewSession(principal())
	resp, has := sess.Handle(context.Background(), []byte(`{not json`))
	if !has {
		t.Fatal("parse error produced no response")
	}
	var out map[string]any
	_ = json.Unmarshal(resp, &out)
	if int(out["error"].(map[string]any)["code"].(float64)) != rpcParseError {
		t.Fatalf("parse error = %s", resp)
	}
	if rpc(t, sess, `{"jsonrpc":"2.0","id":1,"method":"server/discover"}`)["result"] == nil {
		t.Fatal("session died after parse error")
	}
}

func TestNotificationsHaveNoResponse(t *testing.T) {
	srv := newTestServer(t)
	sess := srv.NewSession(principal())
	if _, has := sess.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); has {
		t.Fatal("notification produced a response")
	}
}

func TestToolsCallWithoutArguments(t *testing.T) {
	srv := newTestServer(t)
	sess := srv.NewSession(principal())
	sess.SetVersionHint(ProtocolVersion20260728)
	resp := rpc(t, sess, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"note_tool"}}`)
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing arguments = %v", resp)
	}
	// The fake tool requires "value", so the structured tool error proves the
	// request reached validation instead of failing on a null argument object.
	if result["isError"] != true {
		t.Fatalf("result = %v", result)
	}
}
