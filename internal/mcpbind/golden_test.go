package mcpbind_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"researchos/internal/mcp"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/tools.golden.json")

// TestToolsGolden locks the published catalog byte for byte (§8.1 item 2,
// §8.9 item 36). Run with -update-golden after an intentional catalog change.
func TestToolsGolden(t *testing.T) {
	h := newHarness(t)
	tools, err := h.app.Host.Tools(h.ctx, h.principal())
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	payload, err := json.MarshalIndent(map[string]any{
		"tools":      tools,
		"ttlMs":      60000,
		"cacheScope": "private",
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, '\n')
	path := filepath.Join("testdata", "tools.golden.json")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, payload, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update-golden once): %v", err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(payload)) {
		t.Fatal("tools/list differs from testdata/tools.golden.json")
	}
	// MCP must not expose any approval path.
	for _, spec := range tools {
		lower := strings.ToLower(spec.Name)
		if strings.Contains(lower, "approve") || strings.Contains(lower, "permit") {
			t.Fatalf("approval-like tool %s is exposed", spec.Name)
		}
	}
	// Every published tool has a unique name and both schemas.
	seen := map[string]bool{}
	for _, spec := range tools {
		if seen[spec.Name] {
			t.Fatalf("duplicate tool %s", spec.Name)
		}
		seen[spec.Name] = true
		if len(spec.InputSchema) == 0 || len(spec.OutputSchema) == 0 {
			t.Fatalf("tool %s lacks a schema", spec.Name)
		}
		if spec.Category != mcp.CategoryRead && spec.Category != mcp.CategoryWrite &&
			spec.Category != mcp.CategoryConfirm && spec.Category != mcp.CategoryJob {
			t.Fatalf("tool %s has category %s", spec.Name, spec.Category)
		}
	}
}
