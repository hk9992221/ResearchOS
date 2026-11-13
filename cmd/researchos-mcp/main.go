// Command researchos-mcp is the stdio bridge of the ResearchOS MCP server
// (SPEC-11 §5.8). It opens no database and holds no business logic: it
// authenticates to the running application over the per-user local IPC socket
// with the installation key and relays newline-delimited JSON-RPC both ways.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"researchos/internal/mcp"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	for _, arg := range args {
		switch arg {
		case "--stdio", "stdio", "mcp":
		default:
			return fmt.Errorf("未知参数 %q：用法 researchos-mcp --stdio", arg)
		}
	}
	key, err := mcp.LoadKey()
	if err != nil {
		return errors.New("无法连接 ResearchOS：请先启动 ResearchOS 应用（未找到安装密钥）")
	}
	socket, err := mcp.SocketPath()
	if err != nil {
		return errors.New("无法连接 ResearchOS：请先启动 ResearchOS 应用（无法定位本地套接字）")
	}
	conn, err := mcp.DialIPC(ctx, socket, key)
	if err != nil {
		return fmt.Errorf("无法连接 ResearchOS：请先启动 ResearchOS 应用（%v）", err)
	}
	defer conn.Close()

	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		_ = conn.Close()
	}()
	_, err = io.Copy(os.Stdout, conn)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("与 ResearchOS 的连接中断：%w", err)
	}
	return nil
}
