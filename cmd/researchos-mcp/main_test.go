package main

import (
	"context"
	"strings"
	"testing"

	"researchos/internal/mcp"
)

func isolateConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	return dir
}

func TestBridgeWithoutApplication(t *testing.T) {
	isolateConfig(t)
	err := run(context.Background(), []string{"--stdio"})
	if err == nil || !strings.Contains(err.Error(), "请先启动 ResearchOS") {
		t.Fatalf("run without app = %v", err)
	}
}

func TestBridgeWithKeyButNoServer(t *testing.T) {
	isolateConfig(t)
	if _, err := mcp.LoadOrCreateKey(); err != nil {
		t.Fatal(err)
	}
	err := run(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "请先启动 ResearchOS") {
		t.Fatalf("run with key, no socket = %v", err)
	}
}

func TestBridgeRejectsUnknownArgs(t *testing.T) {
	isolateConfig(t)
	if err := run(context.Background(), []string{"--tcp"}); err == nil {
		t.Fatal("unknown argument accepted")
	}
}
