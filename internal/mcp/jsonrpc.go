package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// JSON-RPC 2.0 and MCP error codes.
const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternalError  = -32603
	// rpcUnsupportedProtocolVersion is the MCP code for an unacceptable
	// protocol version.
	rpcUnsupportedProtocolVersion = -32001
)

// request is one JSON-RPC request or notification.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// response is one JSON-RPC response.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is the JSON-RPC error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// notification is a server-initiated message.
type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

func encode(v any) []byte {
	body, err := json.Marshal(v)
	if err != nil {
		body, _ = json.Marshal(response{JSONRPC: "2.0", Error: &rpcError{Code: rpcInternalError, Message: "内部错误"}})
	}
	return body
}

// ErrorResponse builds a JSON-RPC error envelope for transport-level failures
// such as an unparsable body. It never echoes the request content.
func ErrorResponse(id json.RawMessage, code int, message string) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return encode(response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}

// ParseErrorResponse is the -32700 envelope.
func ParseErrorResponse() []byte {
	return ErrorResponse(json.RawMessage("null"), rpcParseError, "无法解析 JSON-RPC 请求")
}

// InvalidRequestResponse is the -32600 envelope.
func InvalidRequestResponse(id json.RawMessage) []byte {
	return ErrorResponse(id, rpcInvalidRequest, "无效的 JSON-RPC 请求")
}

// UnsupportedVersionResponse is the -32001 envelope.
func UnsupportedVersionResponse(id json.RawMessage) []byte {
	return ErrorResponse(id, rpcUnsupportedProtocolVersion, "UnsupportedProtocolVersion")
}

func decodeParams(raw json.RawMessage) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, fmt.Errorf("params: %w", err)
	}
	if obj == nil {
		obj = map[string]any{}
	}
	return obj, nil
}

func stringField(obj map[string]any, name string) string {
	if v, ok := obj[name].(string); ok {
		return v
	}
	return ""
}

func metaField(obj map[string]any) map[string]any {
	if meta, ok := obj["_meta"].(map[string]any); ok {
		return meta
	}
	return nil
}
