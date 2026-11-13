package mcp

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"researchos/internal/foundation/clock"
)

// Install directory and file names of the stdio bridge (SPEC-11 §5.8).
const (
	installDirName = "ResearchOS"
	keyFileName    = "mcp.key"
	socketFileName = "mcp.sock"
	maxAuthFrame   = 8 << 10
)

// InstallDir returns (and creates) the per-user config directory that holds the
// stdio bridge socket and installation key.
func InstallDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("mcp: locate user config directory: %w", err)
	}
	dir := filepath.Join(base, installDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("mcp: create %s: %w", dir, err)
	}
	return dir, nil
}

// SocketPath returns the IPC socket path.
func SocketPath() (string, error) {
	dir, err := InstallDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, socketFileName), nil
}

// KeyPath returns the installation key path.
func KeyPath() (string, error) {
	dir, err := InstallDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, keyFileName), nil
}

// LoadOrCreateKey returns the installation key, generating a 32-byte key with
// 0600 permissions on first use. A short or unreadable existing key is
// regenerated, so a corrupt file cannot lock the bridge out permanently.
func LoadOrCreateKey() ([]byte, error) {
	path, err := KeyPath()
	if err != nil {
		return nil, err
	}
	if raw, err := os.ReadFile(path); err == nil {
		if key, err := hex.DecodeString(strings.TrimSpace(string(raw))); err == nil && len(key) == 32 {
			return key, nil
		}
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("mcp: generate installation key: %w", err)
	}
	body := []byte(hex.EncodeToString(key) + "\n")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return nil, fmt.Errorf("mcp: write installation key: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("mcp: set installation key permissions: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, fmt.Errorf("mcp: commit installation key: %w", err)
	}
	return key, nil
}

// LoadKey reads an existing installation key without creating one. The bridge
// uses it so a missing key means "no application has ever started".
func LoadKey() ([]byte, error) {
	path, err := KeyPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%w: installation key is invalid", ErrNotRunning)
	}
	return key, nil
}

// IPCServerOptions configures the local IPC listener.
type IPCServerOptions struct {
	Server *Server
	Key    []byte
	Clock  clock.Clock
	Logger Logger
}

// ServeIPC accepts stdio-bridge connections until ctx ends or ln fails. Each
// connection authenticates with the installation key before any JSON-RPC
// message is accepted; the payload is then newline-delimited JSON-RPC.
func ServeIPC(ctx context.Context, ln net.Listener, opts IPCServerOptions) error {
	if opts.Clock == nil {
		opts.Clock = clock.System{}
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return fmt.Errorf("mcp: ipc accept: %w", err)
			}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer conn.Close()
			handleIPCConn(ctx, conn, opts)
		}()
	}
}

func handleIPCConn(ctx context.Context, conn net.Conn, opts IPCServerOptions) {
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	reader := bufio.NewReaderSize(conn, 64<<10)
	line, err := readLimitedLine(reader, maxAuthFrame)
	if err != nil {
		return
	}
	var req request
	if err := json.Unmarshal(line, &req); err != nil || req.Method != "mcp/authenticate" {
		_ = writeFrame(conn, encode(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcInvalidRequest, Message: "需要先完成安装密钥认证"}}))
		return
	}
	params, err := decodeParams(req.Params)
	if err != nil {
		_ = writeFrame(conn, encode(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcInvalidParams, Message: "认证参数无效"}}))
		return
	}
	presented, err := hex.DecodeString(stringField(params, "key"))
	if err != nil || len(presented) != len(opts.Key) || subtle.ConstantTimeCompare(presented, opts.Key) != 1 {
		if opts.Logger != nil {
			opts.Logger.Warn("mcp: stdio bridge authentication failed")
		}
		_ = writeFrame(conn, encode(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcInvalidRequest, Message: "安装密钥不正确"}}))
		return
	}
	if err := writeFrame(conn, encode(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"ok": true}})); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	principal := Principal{ClientID: "stdio", ClientName: "stdio", Transport: TransportStdio}
	connID := fmt.Sprintf("stdio-%d", time.Now().UnixNano())
	if err := opts.Server.ServeStream(ctx, principal, connID, reader, conn); err != nil && opts.Logger != nil {
		opts.Logger.Debug("mcp: stdio bridge stream ended", "err", err)
	}
}

func readLimitedLine(r *bufio.Reader, limit int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(buf)+len(chunk) > limit {
			return nil, errors.New("mcp: auth frame too large")
		}
		buf = append(buf, chunk...)
		if err == nil {
			return []byte(strings.TrimSpace(string(buf))), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return nil, err
	}
}

func writeFrame(w io.Writer, body []byte) error {
	if _, err := w.Write(append(body, '\n')); err != nil {
		return err
	}
	if f, ok := w.(interface{ Sync() error }); ok {
		return f.Sync()
	}
	return nil
}

// DialIPC connects the stdio bridge to a running application. A missing socket
// or a refused connection yields ErrNotRunning so the bridge can print the
// actionable Chinese hint (§8.1 item 5).
func DialIPC(ctx context.Context, path string, key []byte) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	id := json.RawMessage(`"bridge"`)
	frame, _ := json.Marshal(request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "mcp/authenticate",
		Params:  mustJSON(map[string]any{"key": hex.EncodeToString(key)}),
	})
	if err := writeFrame(conn, frame); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	reader := bufio.NewReaderSize(conn, 64<<10)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := readLimitedLine(reader, maxAuthFrame)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	var resp struct {
		Error *rpcError `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil || resp.Error != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: installation key rejected", ErrNotRunning)
	}
	_ = conn.SetReadDeadline(time.Time{})
	return &bufferedConn{Conn: conn, reader: reader}, nil
}

// bufferedConn keeps the buffered reader in front of the socket.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

// Read implements io.Reader over the buffered prefix.
func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func mustJSON(v any) json.RawMessage {
	body, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return body
}
