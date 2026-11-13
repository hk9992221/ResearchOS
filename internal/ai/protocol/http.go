package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"researchos/internal/ai/aierr"
	"researchos/internal/ai/provider"
)

// Anthropic protocol version header value sent with every request.
const anthropicVersion = "2023-05-31"

// base carries the per-call context shared by all adapters.
type base struct {
	cfg    provider.ProviderConfig
	key    provider.Secret
	client *http.Client
}

func (b base) protocol() provider.Protocol { return b.cfg.Protocol }

// url joins the normalized base URL with an API path. The caller supplies
// paths beginning with "/"; adapters append suffixes like ":generateContent"
// after escaping path segments themselves.
func (b base) url(path string) string {
	return strings.TrimRight(b.cfg.BaseURL, "/") + path
}

// request executes one JSON call and decodes into out (nil discards the body).
func (b base) request(ctx context.Context, method, path string, body any, out any, headers ...[2]string) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return &aierr.AIError{Kind: aierr.ErrKindInvalidRequest, Protocol: string(b.cfg.Protocol), Message: aierr.Truncate("marshal: " + err.Error()), Cause: err}
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.url(path), rdr)
	if err != nil {
		return &aierr.AIError{Kind: aierr.ErrKindInvalidRequest, Protocol: string(b.cfg.Protocol), Message: aierr.Truncate(err.Error()), Cause: err}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, h := range headers {
		req.Header.Set(h[0], h[1])
	}
	b.applyAuth(req)

	resp, err := b.httpClient().Do(req)
	if err != nil {
		return b.transportError(err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
	}()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return b.wrap(aierr.ErrKindNetwork, 0, 0, "read response: "+err.Error())
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return b.mapHTTP(resp, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return b.wrap(aierr.ErrKindServer, resp.StatusCode, 0,
				"decode response: "+err.Error()+" body="+firstFragment(data))
		}
	}
	return nil
}

func (b base) httpClient() *http.Client {
	if b.client != nil {
		return b.client
	}
	return http.DefaultClient
}

// applyAuth sets the protocol's auth header; an empty key means the keyless
// local service and sends no auth header at all (SPEC-00 v1.1 §7).
func (b base) applyAuth(req *http.Request) {
	if len(b.key) == 0 {
		return
	}
	switch b.cfg.Protocol {
	case provider.ProtocolOpenAIChat, provider.ProtocolOpenAIResponses:
		req.Header.Set("Authorization", "Bearer "+string(b.key))
	case provider.ProtocolGemini:
		req.Header.Set("x-goog-api-key", string(b.key))
	case provider.ProtocolAnthropic:
		req.Header.Set("x-api-key", string(b.key))
		req.Header.Set("anthropic-version", anthropicVersion)
	}
}

// transportError classifies a client.Do failure.
func (b base) transportError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return b.wrapKind(aierr.ErrKindCanceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return b.wrapKind(aierr.ErrKindTimeout, err.Error())
	default:
		return b.wrapKind(aierr.ErrKindNetwork, err.Error())
	}
}

// mapHTTP converts a non-2xx response into an AIError, extracting the
// upstream error message where the JSON shape is recognized.
func (b base) mapHTTP(resp *http.Response, data []byte) error {
	kind := kindForStatus(resp.StatusCode)
	msg := extractUpstreamMessage(data)
	retryAfter := parseRetryAfter(resp)
	if kind == aierr.ErrKindInvalidRequest && looksContextTooLong(msg) {
		kind = aierr.ErrKindContextTooLong
	}
	if resp.StatusCode == http.StatusNotFound && b.cfg.Protocol == provider.ProtocolAnthropic {
		kind = aierr.ErrKindModelNotFound
	}
	return b.wrap(kind, resp.StatusCode, retryAfter, msg)
}

func kindForStatus(code int) aierr.ErrorKind {
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return aierr.ErrKindAuth
	case code == http.StatusRequestTimeout:
		return aierr.ErrKindTimeout
	case code == http.StatusTooManyRequests:
		return aierr.ErrKindRateLimit
	case code == http.StatusNotFound:
		return aierr.ErrKindModelNotFound
	case code >= 500:
		return aierr.ErrKindServer
	default:
		return aierr.ErrKindInvalidRequest
	}
}

// looksContextTooLong detects the context-window failures the upstreams
// report inside 400-class payloads (§5.7 不静默截断).
func looksContextTooLong(msg string) bool {
	lower := strings.ToLower(msg)
	for _, probe := range []string{
		"context_length_exceeded", "maximum context length", "prompt is too long",
		"exceeds the model", "token limit", "exceeds context window", "input token count",
	} {
		if strings.Contains(lower, probe) {
			return true
		}
	}
	return false
}

// extractUpstreamMessage pulls the provider-native error text out of the
// JSON body, falling back to a bounded raw fragment.
func extractUpstreamMessage(data []byte) string {
	var v struct {
		Error struct {
			Message string          `json:"message"`
			Type    string          `json:"type"`
			Code    json.RawMessage `json:"code"`
			Status  string          `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &v); err == nil && v.Error.Message != "" {
		parts := []string{v.Error.Message}
		if v.Error.Type != "" {
			parts = append(parts, v.Error.Type)
		}
		return strings.Join(parts, ": ")
	}
	return firstFragment(data)
}

func firstFragment(data []byte) string {
	s := strings.TrimSpace(string(data))
	if s == "" {
		return "empty response body"
	}
	return aierr.Truncate(s)
}

func parseRetryAfter(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func (b base) wrap(kind aierr.ErrorKind, status int, retryAfter time.Duration, message string) error {
	return &aierr.AIError{
		Kind:       kind,
		ProviderID: b.cfg.ID,
		Protocol:   string(b.cfg.Protocol),
		StatusCode: status,
		RetryAfter: retryAfter,
		Message:    aierr.RedactWith(message, b.key),
	}
}

func (b base) wrapKind(kind aierr.ErrorKind, message string) error {
	return b.wrap(kind, 0, 0, message)
}

// unsupported builds the capability error used by missing adapters (§5.2).
func (b base) unsupported(capability provider.Capability) error {
	return b.wrapKind(aierr.ErrKindUnsupported,
		fmt.Sprintf("protocol %q does not support %s", b.cfg.Protocol, capability))
}
